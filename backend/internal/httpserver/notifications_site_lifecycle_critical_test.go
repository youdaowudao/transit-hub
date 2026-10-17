package httpserver

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
)

// 主代理冻结的核心业务期望：停用不能改写历史，也不能继续参与当前提醒。
func notificationLifecycleSite(t *testing.T, f *notificationAPIFixture, id string, enabled bool) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO upstream_sites(id,user_id,admin_account_id,name,base_url,platform,requested_platform,account,enabled,status,metrics,created_at,updated_at) VALUES($1,'user','workspace-a',$1,'https://fixture.invalid','sub2api','sub2api','fixture',$2,'connected','{}',now(),now())`, id, enabled)
	if err != nil {
		t.Fatal("cannot prepare isolated lifecycle site")
	}
}

func notificationLifecycleNotice(t *testing.T, f *notificationAPIFixture, site string, level notifications.Level, key string, at time.Time) string {
	t.Helper()
	id := f.publish(t, "workspace-a", level, key, at)
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET subject_type='upstream_site',subject_id=$2 WHERE id=$1`, id, site); err != nil {
		t.Fatal("cannot prepare isolated site notice")
	}
	return id
}

func TestNotificationAPISiteLifecycleAllLevelsAndHistoryPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "active", true)
	notificationLifecycleSite(t, f, "disabled", false)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var disabledUrgent string
	for level := notifications.LevelNormal; level <= notifications.LevelUrgent; level++ {
		for _, site := range []string{"active", "disabled", "deleted"} {
			id := notificationLifecycleNotice(t, f, site, level, fmt.Sprintf("%s-%d", site, level), start)
			if site == "disabled" && level == notifications.LevelUrgent {
				disabledUrgent = id
			}
		}
	}
	f.publish(t, "workspace-a", notifications.LevelNormal, "other-subject", start)
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["unreadCount"] != float64(5) || body["pendingCriticalCount"] != float64(1) {
		t.Fatalf("disabled and deleted sites must leave all current counts while unrelated subjects remain: unread=%v pending=%v", body["unreadCount"], body["pendingCriticalCount"])
	}
	for _, filter := range []string{"all", "unread", "pending"} {
		history := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/")+"&filter="+filter, nil, true), http.StatusOK)
		want := map[string]int{"all": 13, "unread": 5, "pending": 1}[filter]
		if len(history["items"].([]any)) != want {
			t.Fatalf("history %s returned %d; want %d without deleting facts", filter, len(history["items"].([]any)), want)
		}
	}
	detail := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+disabledUrgent), nil, true), http.StatusOK)["item"].(map[string]any)
	if detail["subjectActive"] != false || detail["subjectInactiveMessage"] == "" || detail["readAt"] != nil || detail["reminderCompletedAt"] != nil {
		t.Fatal("disabled history must explain inactivity without pretending it was read or acknowledged")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE upstream_sites SET enabled=true WHERE id='disabled'`); err != nil {
		t.Fatal(err)
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["unreadCount"] != float64(9) || body["pendingCriticalCount"] != float64(2) {
		t.Fatal("re-enabling must resume original pending facts without reviving a deleted site")
	}
}

func TestNotificationAPISiteLifecycleSamplingOnlyEnabledPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "needs-check", true)
	notificationLifecycleSite(t, f, "no-longer-managed", false)
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	state := body["samplingState"].(map[string]any)
	if state["unconfirmedSiteCount"] != float64(1) || len(state["sites"].([]any)) != 1 || state["sites"].([]any)[0].(map[string]any)["siteName"] != "needs-check" {
		t.Fatal("sampling must not keep asking an unmanaged site to synchronize")
	}
}

func TestNotificationAPISiteLifecycleDisplayedIDsAreReadOnlyPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "active", true)
	notificationLifecycleSite(t, f, "disabled", false)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	active := notificationLifecycleNotice(t, f, "active", notifications.LevelUrgent, "displayed-active", at)
	disabled := notificationLifecycleNotice(t, f, "disabled", notifications.LevelUrgent, "displayed-disabled", at)
	foreign := f.publish(t, "foreign-workspace", notifications.LevelUrgent, "displayed-foreign", at)
	ids := []string{disabled, active, foreign, "nonexistent"}
	body := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", map[string]any{"workspaceAdminAccountId": "workspace-a", "displayedIds": ids}, true), http.StatusOK)
	states := body["displayedStates"].([]any)
	if len(states) != len(ids) {
		t.Fatal("every displayed ID needs an explicit scoped result, regardless of summary pagination")
	}
	for i, raw := range states {
		state := raw.(map[string]any)
		if state["id"] != ids[i] || state["subjectActive"] != (i == 1) || state["readAt"] != nil || state["reminderCompletedAt"] != nil {
			t.Fatalf("displayed state %d must preserve order, scope and untouched progress", i)
		}
	}
	var changed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications WHERE read_at IS NOT NULL OR reminder_completed_at IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("POST summary must never save progress, including inaccessible IDs")
	}
}

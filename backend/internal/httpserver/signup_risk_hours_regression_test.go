package httpserver

import (
	"net/http"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
)

func riskHoursBody(enabled bool, start, end, zone string) map[string]any {
	return map[string]any{"workspaceAdminAccountId": "workspace-a", "enabled": enabled, "workStart": start, "workEnd": end, "timeZone": zone}
}

func TestNotificationAPISignupRiskHoursWholeSaveAndInitializationPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	path := "/api/notifications/signup-risk-preferences"
	body := riskHoursBody(true, "09:00", "18:00", "Asia/Singapore")
	saved := notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusOK)
	for k, v := range body {
		if saved[k] != v {
			t.Fatalf("whole saved configuration lost %s", k)
		}
	}
	if saved["serverTime"] == nil || saved["nextStartAt"] == nil {
		t.Fatal("enabled settings must include server time and the next working start")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE signup_risk_settings SET initialized=true WHERE admin_account_id='workspace-a'; INSERT INTO signup_risk_processed_users VALUES('user','workspace-a',repeat('a',64),'42')`); err != nil {
		t.Fatal("cannot prepare completed initial scan")
	}
	body["workEnd"] = "17:00"
	changed := notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusOK)
	if changed["initialized"] != true || changed["updatedAt"] == saved["updatedAt"] {
		t.Fatal("changing hours must keep initial scan and invalidate older rounds")
	}
	same := notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusOK)
	if same["updatedAt"] != changed["updatedAt"] || same["initialized"] != true {
		t.Fatal("saving the same configuration must not reset or invalidate it")
	}
	reread := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath(path), nil, true), http.StatusOK)
	for k, v := range body {
		if reread[k] != v {
			t.Fatalf("reread lost saved %s", k)
		}
	}
	body["enabled"] = false
	closed := notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusOK)
	if closed["initialized"] != false || closed["workEnd"] != "17:00" || closed["activeUntil"] != nil || closed["nextStartAt"] != nil {
		t.Fatal("manual close must retain hours and remove automatic eligibility")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM signup_risk_processed_users`).Scan(&count); err != nil || count != 1 {
		t.Fatal("saving settings removed processed identities")
	}
	old := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusOK)
	if old["intervalSeconds"] != float64(300) {
		t.Fatal("registration save changed the ordinary notification interval")
	}
}

func TestNotificationAPISignupRiskHoursRejectPartialAndInvalidWithoutOverwritePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	path := "/api/notifications/signup-risk-preferences"
	original := notificationResponse(t, f.request(t, http.MethodPut, path, riskHoursBody(false, "09:00", "18:00", "Asia/Tokyo"), true), http.StatusOK)
	for _, body := range []map[string]any{
		{"workspaceAdminAccountId": "workspace-a", "enabled": true},
		riskHoursBody(true, "18:00", "09:00", "Asia/Tokyo"),
		riskHoursBody(true, "09:00", "09:00", "Asia/Tokyo"),
		riskHoursBody(true, "09:00", "18:00", "Local"),
		riskHoursBody(true, "24:00", "18:00", "UTC"),
	} {
		notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusBadRequest)
	}
	after := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath(path), nil, true), http.StatusOK)
	for _, key := range []string{"enabled", "workStart", "workEnd", "timeZone", "updatedAt"} {
		if after[key] != original[key] {
			t.Fatalf("rejected save changed %s", key)
		}
	}
}

func TestNotificationAPISignupRiskHoursOutsideHidesBothSurfacesKeepsHistoryPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	now := time.Now().UTC()
	start, end := "00:00", "00:01"
	if now.Hour() == 0 && now.Minute() < 2 {
		start, end = "12:00", "13:00"
	}
	notificationResponse(t, f.request(t, http.MethodPut, "/api/notifications/signup-risk-preferences", riskHoursBody(true, start, end, "UTC"), true), http.StatusOK)
	id := f.publish(t, "workspace-a", notifications.LevelImportant, "risk-hours", now)
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET kind='sub2api.signup_risk' WHERE id=$1`, id); err != nil {
		t.Fatal("cannot prepare risk fact")
	}
	f.publish(t, "workspace-a", notifications.LevelImportant, "ordinary-hours", now.Add(time.Second))
	summary := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	risk := summary["signupRisk"].(map[string]any)
	if risk["enabled"] != true || risk["activeUntil"] != nil || risk["nextStartAt"] == nil || len(risk["items"].([]any)) != 0 {
		t.Fatal("off-hours must retain enabled and a future start without showing risks")
	}
	important := summary["active"].(map[string]any)["important"].(map[string]any)
	if important["totalCount"] != float64(1) || len(important["items"].([]any)) != 1 || important["items"].([]any)[0].(map[string]any)["id"] == id {
		t.Fatal("ordinary important surface leaked off-hours risk or lost ordinary notice")
	}
	if summary["unreadCount"] != float64(2) {
		t.Fatal("off-hours erased unread history")
	}
	history := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"), nil, true), http.StatusOK)
	if len(history["items"].([]any)) != 2 {
		t.Fatal("history must remain available outside working time")
	}
}

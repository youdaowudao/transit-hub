package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

func TestNotificationAPISiteLifecycleFilteringPrecedesSummaryAndHistoryPaginationPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "active", true)
	notificationLifecycleSite(t, f, "disabled", false)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 23; i++ {
		notificationLifecycleNotice(t, f, "disabled", notifications.LevelUrgent, fmt.Sprintf("disabled-%02d", i), at.Add(time.Duration(i)*time.Second))
	}
	activeIDs := make(map[string]bool)
	for i := 0; i < 21; i++ {
		id := notificationLifecycleNotice(t, f, "active", notifications.LevelUrgent, fmt.Sprintf("active-%02d", i), at.Add(time.Duration(i+23)*time.Second))
		activeIDs[id] = true
	}
	first := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	bucket := first["active"].(map[string]any)["critical"].(map[string]any)
	if first["pendingCriticalCount"] != float64(21) || bucket["totalCount"] != float64(21) || len(bucket["items"].([]any)) != 20 {
		t.Fatal("ineligible earlier rows must not occupy the 20-row summary page or count")
	}
	cursor := bucket["nextCursor"].(string)
	second := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", map[string]any{"workspaceAdminAccountId": "workspace-a", "criticalCursor": cursor, "displayedIds": []string{bucket["items"].([]any)[19].(map[string]any)["id"].(string)}}, true), http.StatusOK)
	secondBucket := second["active"].(map[string]any)["critical"].(map[string]any)
	if len(secondBucket["items"].([]any)) != 1 || secondBucket["nextCursor"] != nil || second["displayedStates"].([]any)[0].(map[string]any)["subjectActive"] != true {
		t.Fatal("second page and explicit first-page ID must remain independently valid")
	}
	for _, filter := range []string{"unread", "pending"} {
		history := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/")+"&filter="+filter, nil, true), http.StatusOK)
		items := history["items"].([]any)
		if len(items) != 20 || history["nextCursor"] == nil {
			t.Fatal("filtered history must fill its first eligible page")
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			if !activeIDs[item["id"].(string)] || item["subjectActive"] != true || item["subjectInactiveMessage"] != "" {
				t.Fatal("filtered history leaked inactive site or inconsistent projection")
			}
		}
		next := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/")+"&filter="+filter+"&cursor="+url.QueryEscape(history["nextCursor"].(string)), nil, true), http.StatusOK)
		if len(next["items"].([]any)) != 1 || next["nextCursor"] != nil {
			t.Fatal("history must not yield a false empty page from disabled rows")
		}
	}
}

func TestNotificationAPISiteLifecycleHistoryManualProgressAndScopedPGReadPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "disabled", false)
	notificationLifecycleSite(t, f, "active", true)
	at := time.Now()
	disabled := notificationLifecycleNotice(t, f, "disabled", notifications.LevelUrgent, "manual-disabled", at)
	active := notificationLifecycleNotice(t, f, "active", notifications.LevelUrgent, "manual-active", at)
	deleted := notificationLifecycleNotice(t, f, "deleted", notifications.LevelUrgent, "manual-deleted", at)
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET content=jsonb_set(content,'{action}','{"kind":"group_rates","siteId":"disabled"}') WHERE id=$1`, disabled); err != nil {
		t.Fatal(err)
	}
	detail := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+disabled), nil, true), http.StatusOK)
	if action := detail["checkAction"].(map[string]any); action["available"] != true || !strings.Contains(action["message"].(string), "不会自动刷新") {
		t.Fatal("stopped but existing site must keep saved-data inspection with clear stopped explanation")
	}
	removed := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+deleted), nil, true), http.StatusOK)["item"].(map[string]any)
	if removed["subjectActive"] != false || removed["subjectInactiveMessage"] != "相关网站已删除，这条消息保留为历史，不再主动提醒。" {
		t.Fatal("deleted historical notice must have a distinct current explanation")
	}
	progress := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/progress", map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": []string{disabled}, "action": "ack"}, true), http.StatusOK)
	if len(progress["items"].([]any)) != 1 || progress["items"].([]any)[0].(map[string]any)["id"] != disabled {
		t.Fatal("manual history acknowledgement must retain explicit ID semantics")
	}
	var activeRead, activeCompleted *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, active).Scan(&activeRead, &activeCompleted); err != nil || activeRead != nil || activeCompleted != nil {
		t.Fatal("reading stopped history must not mark another active notice")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE upstream_sites SET enabled=true WHERE id='disabled'`); err != nil {
		t.Fatal(err)
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["unreadCount"] != float64(1) || body["pendingCriticalCount"] != float64(1) {
		t.Fatal("resuming must not resurrect explicitly acknowledged stopped history")
	}
	repo := upstream.NewRepository(f.pool)
	for _, scope := range []struct {
		u, w, id string
		want     bool
	}{{"user", "workspace-a", "active", true}, {"other-user", "workspace-a", "active", false}, {"user", "workspace-b", "active", false}, {"user", "workspace-a", "missing", false}} {
		got, err := repo.IsSiteNotificationEnabled(t.Context(), scope.u, scope.w, scope.id)
		if err != nil || got != scope.want {
			t.Fatal("persisted eligibility must require matching user, workspace and existing enabled site")
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE upstream_sites SET enabled=false WHERE id='active'`); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.IsSiteNotificationEnabled(t.Context(), "user", "workspace-a", "active"); err != nil || got {
		t.Fatal("PG commit must be visible without any runtime cache publish")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := repo.IsSiteNotificationEnabled(canceled, "user", "workspace-a", "active"); got || err == nil {
		t.Fatal("read failure must remain an error instead of an inactive success")
	}
}

func TestNotificationAPISiteLifecycleSummaryPOSTLimitsAndReadOnlyPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "untouched", time.Now())
	ids := make([]string, 60)
	for i := range ids {
		ids[i] = fmt.Sprintf("unavailable-%d", i)
	}
	for _, list := range [][]string{nil, {}, ids} {
		body := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", map[string]any{"workspaceAdminAccountId": "workspace-a", "displayedIds": list}, true), http.StatusOK)
		states := body["displayedStates"].([]any)
		if len(states) != len(list) {
			t.Fatal("empty or sixty-ID read must return complete bounded status list")
		}
		for i, raw := range states {
			state := raw.(map[string]any)
			if state["id"] != list[i] || state["subjectActive"] != false || state["subjectInactiveMessage"] == "" {
				t.Fatal("unknown IDs require ordered non-disclosing inactive results")
			}
		}
	}
	for _, invalid := range []any{append(append([]string(nil), ids...), "sixty-first"), []string{id, id}, []string{""}, []string{strings.Repeat("a", 513)}, []string{"<script>"}, 17, []any{id, 17}} {
		body := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", map[string]any{"workspaceAdminAccountId": "workspace-a", "displayedIds": invalid}, true), http.StatusBadRequest)
		assertNotificationError(t, body, "workspace-a")
	}
	for _, extra := range []map[string]any{{"workspaceAdminAccountId": "workspace-a", "unexpected": true}, {"workspaceAdminAccountId": "workspace-a", "criticalCursor": "invalid"}} {
		notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", extra, true), http.StatusBadRequest)
	}
	oversized := `{"workspaceAdminAccountId":"workspace-a","displayedIds":["` + strings.Repeat("a", 65536) + `"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/summary", bytes.NewBufferString(oversized))
	req.Header.Set("Authorization", "Bearer "+notificationFixtureToken)
	response := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(response, req)
	body := notificationResponse(t, response, http.StatusBadRequest)
	if !strings.Contains(fmt.Sprint(body), "64KiB") {
		t.Fatal("oversized read body must identify the actual limit")
	}
	var changed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications WHERE read_at IS NOT NULL OR reminder_completed_at IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("all summary POST outcomes must leave original message progress untouched")
	}
	var preferences int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notification_preferences`).Scan(&preferences); err != nil || preferences != 0 {
		t.Fatal("read-only summary must not create or change preference settings")
	}
}

func TestNotificationAPISiteLifecycleSamplingFailureAndPartialStatesOnlyEnabledPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	for i := 0; i < 12; i++ {
		activeID := fmt.Sprintf("active-%02d", i)
		stoppedID := fmt.Sprintf("stopped-%02d", i)
		notificationLifecycleSite(t, f, activeID, true)
		notificationLifecycleSite(t, f, stoppedID, false)
		metrics := `{"rateNoticeObservation":{"confirmed":false,"observedAt":"2030-01-01T00:00:00Z","reason":"本次只读到部分分组，请重新同步"}}`
		if i%2 == 0 {
			metrics = `{"rateNoticeObservation":{"confirmed":false,"observedAt":"2030-01-01T00:00:00Z","reason":"上游读取中断，请稍后重试"}}`
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE upstream_sites SET metrics=$1::jsonb WHERE id=ANY($2::text[])`, metrics, []string{activeID, stoppedID}); err != nil {
			t.Fatal(err)
		}
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	state := body["samplingState"].(map[string]any)
	if state["unconfirmedSiteCount"] != float64(12) || len(state["sites"].([]any)) != 10 {
		t.Fatal("sampling count and safe first-ten page must share only enabled sites")
	}
	for _, raw := range state["sites"].([]any) {
		if !strings.HasPrefix(raw.(map[string]any)["siteName"].(string), "active-") {
			t.Fatal("disabled partial/failed observation leaked into sampling page")
		}
	}
}

type notificationLifecycleFailingCache struct {
	upstream.SiteCache
	site     upstream.Site
	setCalls int
}

func (c *notificationLifecycleFailingCache) Get(context.Context, string) (*upstream.Site, error) {
	site := c.site
	return &site, nil
}
func (c *notificationLifecycleFailingCache) Set(context.Context, *upstream.Site) error {
	c.setCalls++
	return fmt.Errorf("隔离测试的运行数据发布失败")
}

type notificationLifecycleWorkspaceResolver struct{}

func (notificationLifecycleWorkspaceResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "workspace-a", nil
}

func TestNotificationAPISiteLifecyclePGCommitCachePublishFailureStillSilencesPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "publish-failure", true)
	id := notificationLifecycleNotice(t, f, "publish-failure", notifications.LevelUrgent, "cache-publish-failed", time.Now())
	repo := upstream.NewRepository(f.pool)
	sites, err := repo.ListSitesForUser(t.Context(), "user")
	if err != nil || len(sites) != 1 {
		t.Fatal("cannot read original persisted lifecycle fixture")
	}
	cache := &notificationLifecycleFailingCache{site: sites[0]}
	service := upstream.NewService(nil, repo, nil, cache)
	service.SetBackgroundTasksDisabled(true)
	service.SetAdminAccountResolver(notificationLifecycleWorkspaceResolver{})
	t.Cleanup(service.Close)
	_, err = service.UpdateEnabled(t.Context(), "user", "publish-failure", false)
	var saveErr *upstream.RequestError
	if !errors.As(err, &saveErr) || !strings.Contains(saveErr.UpstreamMessage, "本站资料已保存") || cache.setCalls != 1 || !cache.site.IsEnabled() {
		t.Fatalf("must exercise actual committed-PG and failed-runtime-publish outcome: err=%v writes=%d", err, cache.setCalls)
	}
	var enabled bool
	if err := f.pool.QueryRow(t.Context(), `SELECT enabled FROM upstream_sites WHERE id='publish-failure'`).Scan(&enabled); err != nil || enabled {
		t.Fatal("partial-success test must actually commit disabled PG state")
	}
	body := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/summary", map[string]any{"workspaceAdminAccountId": "workspace-a", "displayedIds": []string{id}}, true), http.StatusOK)
	if body["unreadCount"] != float64(0) || body["pendingCriticalCount"] != float64(0) || body["displayedStates"].([]any)[0].(map[string]any)["subjectActive"] != false {
		t.Fatal("notifications must reflect PG lifecycle despite failed cache publication")
	}
	notificationClearCooldown(t, "publish-failure")
	sender := &notificationFakeSender{}
	checkBalanceWarning(t.Context(), sender, service, repo, notificationLifecycleStrategy(), "user", "workspace-a", "publish-failure", "site", notificationLifecycleMetrics(1), notificationLifecycleMetrics(2))
	checkMultiplierChanges(t.Context(), sender, repo, notificationLifecycleStrategy(), "user", "workspace-a", "publish-failure", "site", notificationLifecycleMetrics(1), notificationLifecycleMetrics(2))
	if len(sender.sends) != 0 || notificationHasCooldown("publish-failure") {
		t.Fatal("PG disabled state must suppress actual callback reads even while cached site stays enabled")
	}
}

func TestNotificationAPISiteLifecycleQualificationReadFailureIsNotEmptySuccessPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	notificationLifecycleSite(t, f, "active", true)
	id := notificationLifecycleNotice(t, f, "active", notifications.LevelUrgent, "qualification-read-failure", time.Now())
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE upstream_sites RENAME TO unavailable_upstream_sites`); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := notificationScopePath("/api/notifications/summary")
		var requestBody any
		if method == http.MethodPost {
			path = "/api/notifications/summary"
			requestBody = map[string]any{"workspaceAdminAccountId": "workspace-a", "displayedIds": []string{id}}
		}
		body := notificationResponse(t, f.request(t, method, path, requestBody, true), http.StatusServiceUnavailable)
		assertNotificationError(t, body, "workspace-a")
		for _, field := range []string{"unreadCount", "pendingCriticalCount", "active", "displayedStates"} {
			if _, ok := body[field]; ok {
				t.Fatal("failed subject qualification must not publish invented zero counts or inactive-ID results")
			}
		}
	}
	var changed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications WHERE read_at IS NOT NULL OR reminder_completed_at IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("qualification read failure must leave persisted progress unchanged")
	}
}

package httpserver

import (
	"net/http"
	"testing"
)

func TestNotificationAPISignupRiskPreferencesScopeValidationAndOldIntervalPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	path := "/api/notifications/signup-risk-preferences"
	unauth := f.request(t, http.MethodGet, notificationScopePath(path), nil, false)
	notificationResponse(t, unauth, http.StatusUnauthorized)
	defaults := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath(path), nil, true), http.StatusOK)
	if defaults["enabled"] != false || defaults["initialized"] != false || defaults["updatedAt"] != nil {
		t.Fatal("wrong default risk preferences")
	}
	for _, scope := range []string{"workspace-b", "foreign-workspace"} {
		w := f.request(t, http.MethodPut, path, map[string]any{"workspaceAdminAccountId": scope, "enabled": true, "workStart": "09:00", "workEnd": "18:00", "timeZone": "Asia/Singapore"}, true)
		if w.Code == http.StatusOK {
			t.Fatal("foreign or non-current workspace settings changed")
		}
	}
	for _, extra := range []map[string]any{{}, {"enabled": "true"}, {"enabled": nil}, {"enabled": true, "level": 1}} {
		body := map[string]any{"workspaceAdminAccountId": "workspace-a"}
		for k, v := range extra {
			body[k] = v
		}
		notificationResponse(t, f.request(t, http.MethodPut, path, body, true), http.StatusBadRequest)
	}
	saved := notificationResponse(t, f.request(t, http.MethodPut, path, map[string]any{"workspaceAdminAccountId": "workspace-a", "enabled": true, "workStart": "09:00", "workEnd": "18:00", "timeZone": "Asia/Singapore"}, true), http.StatusOK)
	if saved["enabled"] != true || saved["updatedAt"] == nil {
		t.Fatal("risk switch was not confirmed")
	}
	interval := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusOK)
	if interval["intervalSeconds"] != float64(300) {
		t.Fatal("risk preference overwrote original interval")
	}
	summary := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	risk, ok := summary["signupRisk"].(map[string]any)
	if !ok || risk["enabled"] != true || len(risk["items"].([]any)) != 0 {
		t.Fatal("summary omitted the local risk switch or invented notifications")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE admin_accounts SET platform='new-api' WHERE id='workspace-a'`); err != nil {
		t.Fatal("cannot prepare non-Sub2API fixture")
	}
	notificationResponse(t, f.request(t, http.MethodPut, path, map[string]any{"workspaceAdminAccountId": "workspace-a", "enabled": true, "workStart": "09:00", "workEnd": "18:00", "timeZone": "Asia/Singapore"}, true), http.StatusBadRequest)
}

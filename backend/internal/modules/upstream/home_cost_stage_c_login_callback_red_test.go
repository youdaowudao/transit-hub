package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 新凭据登录恢复只通知已成功完成的修改；名称/倍率编辑和失败不触发。
func TestHomeCostStageCCredentialLoginNotifiesSuccessfulUpdateOnly(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var rejected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/user/self" && rejected.Load() {
			w.WriteHeader(401)
			writeJSON(w, map[string]any{"success": false})
			return
		}
		if r.URL.Path == "/api/user/self" {
			writeJSON(w, map[string]any{"data": map[string]any{"id": 1, "quota": 5000000}})
			return
		}
		if r.URL.Path == "/api/log/self/stat" {
			writeJSON(w, map[string]any{"data": map[string]any{"quota": 500000}})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{}})
	}))
	defer server.Close()
	pl := NewPlatformService(NewHTTPClient(server.Client()))
	pl.now = func() time.Time { return now }
	svc := NewService(pl, nil, nil, newSyncTestCache())
	svc.now = func() time.Time { return now }
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	defer svc.Close()
	var notices atomic.Int32
	if setter, ok := any(svc).(interface {
		SetSiteLoginSuccessCallback(func(string, string, string))
	}); ok {
		setter.SetSiteLoginSuccessCallback(func(user, workspace, siteID string) {
			if user != "user" || workspace != "workspace" || siteID == "" {
				t.Error("notification lost local scope")
			}
			notices.Add(1)
		})
	}
	created, err := svc.Create(t.Context(), "user", CreateRequest{Name: "验收-凭据", SiteURL: server.URL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, UserID: "1", AccessToken: "fixture-only", RechargeRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	update := UpdateRequest{Name: "验收-凭据", SiteURL: server.URL, Platform: PlatformNewAPI, AuthMode: AuthModeUserKey, Account: "1", UserID: "1", RechargeRate: 2}
	if _, err := svc.Update(t.Context(), "user", created.ID, update); err != nil {
		t.Fatal(err)
	}
	if notices.Load() != 0 {
		t.Error("creation or non-credential edit cleared existing manual block")
	}
	update.AccessToken = "fixture-new"
	rejected.Store(true)
	if _, err := svc.Update(t.Context(), "user", created.ID, update); err == nil {
		t.Fatal("fixture login should be rejected")
	}
	if notices.Load() != 0 {
		t.Error("failed login cleared manual block")
	}
	rejected.Store(false)
	if _, err := svc.Update(t.Context(), "user", created.ID, update); err != nil {
		t.Fatal(err)
	}
	if notices.Load() != 1 {
		t.Errorf("successful credential login notifications=%d want=1", notices.Load())
	}
}

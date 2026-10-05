package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 主代理固定同步阈值，与其他入口/关闭自动刷新反向边界。
func TestHomeCostStageCSyncTokenRefreshUsesIntervalPlusFiveMinutes(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, item := range []struct {
		name      string
		sync      bool
		enabled   bool
		remaining time.Duration
		want      int32
	}{
		{"sync_inside_interval_plus_five", true, true, 8*time.Minute - time.Second, 1},
		{"sync_outside_interval_plus_five", true, true, 8*time.Minute + time.Second, 0},
		{"disabled_four_minutes", true, false, 4 * time.Minute, 0},
		{"disabled_fifty_seconds", true, false, 50 * time.Second, 1},
		{"other_path_four_minutes", false, true, 4 * time.Minute, 0},
		{"other_path_fifty_seconds", false, true, 50 * time.Second, 1},
	} {
		t.Run(item.name, func(t *testing.T) {
			var refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/refresh":
					refreshes.Add(1)
					writeJSON(w, map[string]any{"data": map[string]any{"access_token": "fixture-new", "refresh_token": "fixture-refresh", "expires_in": 3600}})
				case "/api/v1/auth/me":
					writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
				case "/api/v1/usage/dashboard/stats":
					writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
				default:
					writeJSON(w, map[string]any{"data": []any{}})
				}
			}))
			defer server.Close()
			expires := now.Add(item.remaining).UnixMilli()
			session := Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-old", RefreshToken: "fixture-refresh", ExpiresAt: &expires}
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			if item.sync {
				svc := NewService(platform, nil, nil, newSyncTestCache(newTestSite("site", "user", "workspace", 1, &session)))
				svc.now = func() time.Time { return now }
				defer svc.Close()
				svc.SetWorkspaceRefreshConfig("user", "workspace", RefreshConfig{Enabled: item.enabled, Interval: 3 * time.Minute})
				if _, err := svc.syncOnce(t.Context(), "site"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := platform.RefreshSessionContext(t.Context(), session); err != nil {
					t.Fatal(err)
				}
			}
			if refreshes.Load() != item.want {
				t.Errorf("refresh requests=%d want=%d", refreshes.Load(), item.want)
			}
		})
	}
}

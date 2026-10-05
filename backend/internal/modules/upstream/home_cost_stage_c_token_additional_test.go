package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHomeCostStageCSyncTokenRefreshWorkspaceAndThresholdBoundaries(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, item := range []struct {
		name                        string
		configUser, configWorkspace string
		config                      RefreshConfig
		remaining                   time.Duration
		want                        int32
	}{
		{"extended_below_one_ms", "user", "workspace", RefreshConfig{Enabled: true, Interval: 3 * time.Minute}, 8*time.Minute - time.Millisecond, 1},
		{"extended_equal", "user", "workspace", RefreshConfig{Enabled: true, Interval: 3 * time.Minute}, 8 * time.Minute, 0},
		{"extended_above_one_ms", "user", "workspace", RefreshConfig{Enabled: true, Interval: 3 * time.Minute}, 8*time.Minute + time.Millisecond, 0},
		{"workspace_interval_ten_minutes", "user", "workspace", RefreshConfig{Enabled: true, Interval: 10 * time.Minute}, 12 * time.Minute, 1},
		{"foreign_workspace", "user", "other", RefreshConfig{Enabled: true, Interval: 3 * time.Minute}, 4 * time.Minute, 0},
		{"foreign_user", "other", "workspace", RefreshConfig{Enabled: true, Interval: 3 * time.Minute}, 4 * time.Minute, 0},
		{"disabled_default_equal", "user", "workspace", RefreshConfig{Enabled: false, Interval: 3 * time.Minute}, time.Minute, 1},
		{"disabled_default_above_one_ms", "user", "workspace", RefreshConfig{Enabled: false, Interval: 3 * time.Minute}, time.Minute + time.Millisecond, 0},
		{"no_interval_keeps_default", "user", "workspace", RefreshConfig{Enabled: true}, 4 * time.Minute, 0},
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
			svc := NewService(platform, nil, nil, newSyncTestCache(newTestSite("site", "user", "workspace", 1, &session)))
			svc.now = func() time.Time { return now }
			defer svc.Close()
			svc.SetWorkspaceRefreshConfig(item.configUser, item.configWorkspace, item.config)
			if _, err := svc.syncOnce(t.Context(), "site"); err != nil {
				t.Fatal(err)
			}
			if refreshes.Load() != item.want {
				t.Errorf("workspace refresh requests=%d want=%d", refreshes.Load(), item.want)
			}
		})
	}
}

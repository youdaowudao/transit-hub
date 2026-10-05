package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostReviewCollectorPersistsRotatedSession(t *testing.T) {
	for _, readFails := range []bool{false, true} {
		name := "complete"
		if readFails {
			name = "subsequent_read_failure"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			var refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes.Add(1)
					writeJSON(w, map[string]any{"data": map[string]any{"access_token": "fixture-new-access", "refresh_token": "fixture-rotated-refresh", "token_type": "Bearer"}})
					return
				}
				if r.Header.Get("Authorization") != "Bearer fixture-new-access" {
					t.Error("collector did not use refreshed credentials")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/api/v1/usage/dashboard/stats":
					if readFails {
						w.WriteHeader(http.StatusForbidden)
						writeJSON(w, map[string]any{"code": "INSUFFICIENT_BALANCE"})
					} else {
						writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
					}
				case "/api/v1/keys":
					writeJSON(w, map[string]any{"data": map[string]any{"items": []any{map[string]any{"id": 1, "name": "验收-Key"}}, "total": 1}})
				case "/api/v1/usage/dashboard/api-keys-usage":
					writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 3}}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			expires := now.Add(50 * time.Second).UnixMilli()
			session := Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: server.URL, AccessToken: "fixture-old-access", RefreshToken: "fixture-old-refresh", TokenType: "Bearer", ExpiresAt: &expires}
			site := newTestSite("site", "user", "workspace", 1, &session)
			site.BaseURL = server.URL
			amount := 3.0
			site.Metrics.TodayConsume = MetricValue{Value: &amount}
			site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
			cache := newKeySnapshotTestCache(site)
			repo := &homeCostCredentialRepository{}
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			svc := NewService(platform, repo, nil, cache)
			svc.now = func() time.Time { return now }
			defer svc.Close()
			svc.collectKeyUsageSnapshot(*site)
			stored, err := cache.Get(t.Context(), site.ID)
			if err != nil || stored == nil || stored.Session == nil || stored.Session.AccessToken != "fixture-new-access" || stored.Session.RefreshToken != "fixture-rotated-refresh" {
				t.Error("successful token rotation was not saved in the site cache")
			}
			if !repo.committed || repo.saved.Session == nil || repo.saved.Session.AccessToken != "fixture-new-access" || repo.saved.Session.RefreshToken != "fixture-rotated-refresh" {
				t.Error("successful token rotation was not saved durably for the next sync/restart")
			}
			if repo.committed && (repo.saved.UserID != site.UserID || repo.saved.AdminAccountID != site.AdminAccountID || repo.saved.ID != site.ID || repo.saved.Metrics.TodayConsumeDate != site.Metrics.TodayConsumeDate || repo.saved.Metrics.TodayConsume.Value == nil || *repo.saved.Metrics.TodayConsume.Value != amount) {
				t.Error("session persistence changed scope or original metrics")
			}
			snapshot, err := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
			if err != nil || snapshot == nil || snapshot.Complete == readFails || refreshes.Load() != 1 {
				t.Errorf("collection outcome changed: snapshot present=%t complete=%t refreshes=%d", snapshot != nil, snapshot != nil && snapshot.Complete, refreshes.Load())
			}
		})
	}
}

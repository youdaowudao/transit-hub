package upstream

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostSyncKeepsActualCompletionTimeForRecovery(t *testing.T) {
	startedAt := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var elapsed atomic.Int64
	now := func() time.Time { return startedAt.Add(time.Duration(elapsed.Load()) * time.Second) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
		case "/api/v1/usage/dashboard/stats":
			elapsed.Store(20)
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	cache := newSyncTestCache(site)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = now
	service := NewService(platform, nil, nil, cache)
	service.now = now
	t.Cleanup(service.Close)
	if _, err := service.syncOnce(t.Context(), site.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := cache.Get(t.Context(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(20 * time.Second)
	if stored.LastSyncedAt == nil || *stored.LastSyncedAt != completedAt.UnixMilli() {
		t.Errorf("sync completion was replaced by its start: got=%v want=%d", stored.LastSyncedAt, completedAt.UnixMilli())
	}
	if stored.Metrics.TodayConsumeAt == nil || !stored.Metrics.TodayConsumeAt.Equal(completedAt) {
		t.Errorf("metric observation predates the actual read: got=%v want=%s", stored.Metrics.TodayConsumeAt, completedAt)
	}
	if stored.Metrics.TodayConsumeDate != businesstime.DateAt(startedAt) {
		t.Errorf("sync business date changed: %s", stored.Metrics.TodayConsumeDate)
	}
}

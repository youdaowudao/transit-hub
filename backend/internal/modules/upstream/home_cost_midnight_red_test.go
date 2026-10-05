package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostCollectorCrossMidnightNeverChangesToHistoricalUsageRoute(t *testing.T) {
	before := time.Date(2031, 2, 3, 15, 59, 59, 0, time.UTC)
	var crossed atomic.Bool
	var todayRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/keys":
			crossed.Store(true)
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/dashboard/api-keys-usage":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/usage/stats":
			q := r.URL.Query()
			if q.Get("period") != "today" || q.Has("timezone") || q.Has("start_date") || q.Has("end_date") {
				t.Error("collector crossing midnight changed its server-today route to a historical query")
			} else {
				todayRequests.Add(1)
			}
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 1}})
		default:
			t.Errorf("unexpected collection route: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time {
		if crossed.Load() {
			return before.Add(2 * time.Second)
		}
		return before
	}
	_, err := platform.collectKeyUsageToday(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"}, nil, businesstime.DateAt(before))
	if err != nil || todayRequests.Load() != 1 {
		t.Fatalf("today collection requests=%d err=%v", todayRequests.Load(), err)
	}
}

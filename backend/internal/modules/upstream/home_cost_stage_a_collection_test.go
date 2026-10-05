package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostStageASub2APIBatches100(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var batches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/keys":
			var rows []map[string]any
			page := r.URL.Query().Get("page")
			if page == "1" {
				for i := 1; i <= 100; i++ {
					rows = append(rows, map[string]any{"id": i, "name": fmt.Sprint(i)})
				}
			} else {
				rows = append(rows, map[string]any{"id": 101, "name": "101"})
			}
			writeJSON(w, map[string]any{"data": rows, "total": 101})
		case "/api/v1/usage/dashboard/api-keys-usage":
			batches.Add(1)
			var body struct {
				IDs []int64 `json:"api_key_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.IDs) > 100 || len(body.IDs) == 0 {
				t.Errorf("invalid batch size %d", len(body.IDs))
			}
			stats := map[string]any{}
			for _, id := range body.IDs {
				stats[fmt.Sprint(id)] = map[string]any{"today_actual_cost": 1, "total_actual_cost": 99}
			}
			writeJSON(w, map[string]any{"data": map[string]any{"stats": stats}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	items, err := platform.FetchKeyUsageTodayIncludingZeroWithContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"}, nil, businesstime.DateAt(now))
	if err != nil || len(items) != 101 || batches.Load() != 2 {
		t.Fatalf("batch result entries=%d batches=%d err=%v", len(items), batches.Load(), err)
	}
	for _, item := range items {
		if item.TodayAmount != 1 {
			t.Fatalf("today used cumulative amount: %v", item)
		}
	}
}

func TestHomeCostStageASub2APIBatchFallbackUsesServerToday(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, status := range []int{404, 405} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var batches atomic.Int32
			var fallback atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/keys":
					writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
				case "/api/v1/usage/dashboard/api-keys-usage":
					batches.Add(1)
					w.WriteHeader(status)
				case "/api/v1/usage/stats":
					fallback.Add(1)
					q := r.URL.Query()
					if q.Get("period") != "today" || q.Get("timezone") != "" || q.Get("start_date") != "" || q.Get("end_date") != "" {
						t.Errorf("fallback query is not upstream server today: %v", q)
					}
					writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 2}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			svc := NewPlatformService(NewHTTPClient(server.Client()))
			svc.now = func() time.Time { return now }
			for i := 0; i < 2; i++ {
				items, err := svc.FetchKeyUsageTodayIncludingZeroWithContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"}, nil, businesstime.DateAt(now))
				if err != nil || len(items) != 1 || items[0].TodayAmount != 2 {
					t.Fatalf("fallback result %v %v", items, err)
				}
			}
			if batches.Load() != 1 || fallback.Load() != 2 {
				t.Fatalf("capability not cached: batch=%d fallback=%d", batches.Load(), fallback.Load())
			}
		})
	}
}

func TestHomeCostStageANewAPIRespectsCancellation(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeJSON(w, map[string]any{"success": true, "data": []any{}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	_, err := platform.FetchKeyUsageTodayIncludingZeroWithContext(ctx, Session{Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "fixture", UserID: "1"}, nil, businesstime.DateAt(now))
	if err == nil || requests.Load() != 0 {
		t.Fatalf("canceled context ignored: requests=%d err=%v", requests.Load(), err)
	}
}

func TestHomeCostStageASnapshotReadInterfaceAvailable(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	type snapshots interface {
		CachedKeyUsageForDate(context.Context, string, string, string) (KeyUsageForDateResult, error)
	}
	svc := NewService(nil, nil, nil, newFakeSiteCache())
	svc.now = func() time.Time { return now }
	reader, ok := any(svc).(snapshots)
	if !ok {
		t.Fatal("read-only daily snapshot reader unavailable")
	}
	result, err := reader.CachedKeyUsageForDate(context.Background(), "user", "workspace", businesstime.DateAt(now))
	if err != nil || result.ExpectedSites != 0 {
		t.Fatalf("empty workspace snapshot failed: %v %v", result, err)
	}
}

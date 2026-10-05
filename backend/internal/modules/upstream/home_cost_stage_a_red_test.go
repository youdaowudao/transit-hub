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

// 主代理固定 D1/D2 的关键反向期望；实现者不得用实时查询补齐缺快照。
func TestHomeCostStageAReadMissingSnapshotMakesNoUpstreamRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		default:
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 9}})
		}
	}))
	defer server.Close()
	cache := newFakeSiteCache()
	cache.add(newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}))
	svc := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	items, _ := svc.KeyUsageToday(context.Background(), "user")
	if got := requests.Load(); got != 0 {
		t.Fatalf("missing snapshot caused %d upstream requests; want zero", got)
	}
	if len(items) != 0 {
		t.Fatalf("missing snapshot became readable costs: %#v", items)
	}
}

// D5：相同名称且任一 Token 无分组时，全名称只查一次，不分摊。
func TestHomeCostStageANewAPISameNameCostsAreCountedOnce(t *testing.T) {
	var statsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token/":
			writeJSON(w, map[string]any{"success": true, "data": []map[string]any{{"id": 1, "name": "shared", "group": "vip"}, {"id": 2, "name": "shared"}}, "total": 2})
		case "/api/log/self/stat":
			statsRequests.Add(1)
			if r.URL.Query().Get("group") != "" {
				t.Error("same-name Token with a missing group must query the full name")
			}
			writeJSON(w, map[string]any{"success": true, "data": map[string]any{"quota": 500000}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	items, err := platform.FetchKeyUsageTodayIncludingZeroWithContext(context.Background(), Session{Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "fixture-only", UserID: "1"}, nil, date)
	if err != nil {
		t.Fatal(err)
	}
	var total float64
	for _, item := range items {
		total += item.TodayAmount
	}
	if statsRequests.Load() != 1 || len(items) != 1 || total != 1 {
		t.Fatalf("same-name costs duplicated: requests=%d entries=%d total=%v", statsRequests.Load(), len(items), total)
	}
}

func TestHomeCostStageAOrdinary429RetriesExactlyOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"success": false})
	}))
	defer server.Close()
	_, err := NewPlatformService(NewHTTPClient(server.Client())).requestKeyUsageJSONWithContext(context.Background(), server.URL, requestOptions{})
	if err == nil || requests.Load() != 2 {
		t.Fatalf("ordinary 429 retry result: error=%v requests=%d; want failure after 2", err, requests.Load())
	}
}

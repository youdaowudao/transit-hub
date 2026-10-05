package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostStageANewAPIGroupedSameNamesCountEachScopeOnce(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "all_grouped"
		if missing {
			name = "one_without_group"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/token/":
					rows := []map[string]any{{"id": 1, "name": "same", "group": "vip"}, {"id": 2, "name": "same", "group": "vip"}, {"id": 3, "name": "same", "group": "standard"}}
					if missing {
						rows[2]["group"] = ""
					}
					writeJSON(w, map[string]any{"data": rows, "total": 3})
				case "/api/log/self/stat":
					calls.Add(1)
					group := r.URL.Query().Get("group")
					quota := float64(500000)
					if missing {
						if group != "" {
							t.Error("mixed missing-group name was split")
						}
						quota = 1000000
					} else if group == "standard" {
						quota = 500000
					} else if group != "vip" {
						t.Error("unexpected grouped scope")
					}
					writeJSON(w, map[string]any{"data": map[string]any{"quota": quota}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			rows, err := platform.FetchKeyUsageTodayIncludingZeroWithContext(t.Context(), Session{Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "fixture", UserID: "1"}, nil, businesstime.DateAt(now))
			if err != nil {
				t.Fatal(err)
			}
			total := float64(0)
			ids := 0
			merged := 0
			for _, row := range rows {
				total += row.TodayAmount
				ids += len(row.KeyIDs)
				if row.Merged {
					merged++
				}
			}
			expectedCalls := int32(2)
			if missing {
				expectedCalls = 1
			}
			if calls.Load() != expectedCalls || len(rows) != int(expectedCalls) || total != 2 || ids != 3 || merged != 1 {
				t.Fatalf("same-name scope duplication calls=%d rows=%d total=%v ids=%d merged=%d", calls.Load(), len(rows), total, ids, merged)
			}
		})
	}
}

func TestHomeCostStageABothPlatformsPropagateOneCancelableContext(t *testing.T) {
	for _, kind := range []Platform{PlatformSub2API, PlatformNewAPI} {
		t.Run(string(kind), func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			serverRelease := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/keys" || r.URL.Path == "/api/token/" {
					writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one", "group": "vip"}}, "total": 1})
					return
				}
				select {
				case <-r.Context().Done():
				case <-serverRelease:
				}
			}))
			defer server.Close()
			defer close(serverRelease)
			client := server.Client()
			client.Timeout = 80 * time.Millisecond
			platform := NewPlatformService(NewHTTPClient(client))
			platform.now = func() time.Time { return now }
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err := platform.FetchKeyUsageTodayIncludingZeroWithContext(ctx, Session{Platform: kind, BaseURL: server.URL, AccessToken: "fixture", Cookie: "fixture", UserID: "1"}, nil, businesstime.DateAt(now))
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 200*time.Millisecond {
				t.Fatalf("shared context not honored error=%v elapsed=%s", err, time.Since(started))
			}
		})
	}
}

func TestHomeCostStageAExplicitHistoricalDateRetainedEvenWhenDateIsToday(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	var historical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/stats":
			q := r.URL.Query()
			if q.Get("start_date") != date || q.Get("end_date") != date || q.Get("timezone") != businesstime.Timezone || q.Has("period") {
				t.Error("explicit-date closing became server-today")
			}
			historical.Add(1)
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 3}})
		default:
			t.Errorf("unexpected historical closing path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	cache := newKeySnapshotTestCache(site)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, nil, nil, cache)
	result, err := svc.KeyUsageForDate(t.Context(), "user", "workspace", date)
	if err != nil || historical.Load() != 1 || result.CompletedSites != 1 || result.Sites[0].Items[0].TodayAmount != 3 {
		t.Fatalf("historical result %v requests=%d error=%v", result, historical.Load(), err)
	}
}

type homeCostRemovalRepo struct {
	enabledTestRepository
	failure error
}

func (r *homeCostRemovalRepo) DeleteSite(context.Context, string, string) error { return r.failure }

func TestHomeCostStageARemovalRollbackPreservesOriginalSnapshot(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	site := newTestSite("one", "user", "workspace", 2, nil)
	cache := newKeySnapshotTestCache(site)
	original := fixtureKeySnapshot(now, 3)
	original.FailureReason = ErrorRateLimited
	failedAt := now.Add(time.Second)
	original.FailureAt = &failedAt
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, original); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, &homeCostRemovalRepo{failure: errors.New("fixture delete rejected")}, nil, cache)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	svc.SetSiteReferenceChecker(&failureFixReferences{})
	if err := svc.Remove(t.Context(), "user", site.ID); err == nil {
		t.Fatal("delete failure became success")
	}
	restored, err := cache.Get(t.Context(), site.ID)
	snapshot, snapshotErr := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	if err != nil || snapshotErr != nil || restored == nil || restored.RechargeRate != 2 || snapshot == nil || snapshot.Items[0].TodayAmount != 3 || !snapshot.StartedAt.Equal(original.StartedAt) || snapshot.FailureReason != ErrorRateLimited || snapshot.FailureAt == nil {
		t.Fatalf("failed delete lost original site/snapshot: site=%v snapshot=%v err=%v/%v", restored, snapshot, err, snapshotErr)
	}
}

func TestHomeCostStageARemovalDuringCollectionNeverResurrectsSnapshot(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		name := "site_remove"
		if workspace {
			name = "workspace_cleanup"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			entered := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/usage/dashboard/stats":
					writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
				case "/api/v1/keys":
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
				case "/api/v1/usage/dashboard/api-keys-usage":
					writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 9}}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
			site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
			cache := newKeySnapshotTestCache(site)
			_ = cache.SaveKeyUsageSnapshot(t.Context(), site.ID, fixtureKeySnapshot(now.Add(-time.Minute), 3))
			platform := NewPlatformService(NewHTTPClient(server.Client()))
			platform.now = func() time.Time { return now }
			svc := NewService(platform, nil, nil, cache)
			svc.now = func() time.Time { return now }
			svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
			svc.SetSiteReferenceChecker(&failureFixReferences{})
			svc.enqueueKeyUsageCollection(*site)
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				t.Fatal("collector did not start")
			}
			var err error
			if workspace {
				err = svc.CleanupDeletedWorkspaceSites(t.Context(), "user", []string{site.ID})
			} else {
				err = svc.Remove(t.Context(), "user", site.ID)
			}
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			snapshot, _ := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
			if snapshot != nil {
				close(release)
				t.Fatal("removal left previous snapshot")
			}
			close(release)
			waitKeySnapshotFlights(t, svc)
			snapshot, _ = cache.GetKeyUsageSnapshot(t.Context(), site.ID)
			if snapshot != nil {
				t.Fatal("late collector resurrected deleted snapshot")
			}
		})
	}
}

func TestHomeCostStageASyncRefreshWaitsForSupplementAndSkipsFailedStation(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var offset atomic.Int64
	var keys atomic.Int32
	var failedKeys atomic.Int32
	entered := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer failed-fixture" {
			if r.URL.Path == "/api/v1/keys" {
				failedKeys.Add(1)
			}
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": []any{}})
		case "/api/v1/keys":
			keys.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/dashboard/api-keys-usage":
			writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": float64(keys.Load())}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(release)
	success := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	failed := newTestSite("failed", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "failed-fixture"})
	cache := newKeySnapshotTestCache(success, failed)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, nil, nil, cache)
	svc.now = func() time.Time { return now.Add(time.Duration(offset.Add(1)) * time.Millisecond) }
	svc.enqueueKeyUsageCollection(*success)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old collection did not start")
	}
	startedAt := now.Add(50 * time.Millisecond)
	offset.Store(100)
	type response struct {
		result KeyUsageForDateResult
		err    error
	}
	done := make(chan response, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	go func() {
		result, err := svc.SyncAndCollectKeyUsage(ctx, "user", "workspace", businesstime.DateAt(now), startedAt)
		done <- response{result, err}
	}()
	waitForHomeCostSupplement(t, svc, success.ID)
	select {
	case result := <-done:
		t.Fatalf("refresh returned before queued collection: %v", result)
	default:
	}
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("supplement did not start")
	}
	select {
	case result := <-done:
		t.Fatalf("refresh accepted earlier collector result: %v", result)
	default:
	}
	release <- struct{}{}
	select {
	case response := <-done:
		if response.err != nil || response.result.ExpectedSites != 2 || response.result.CompletedSites != 1 {
			t.Fatalf("supplement result %+v %v", response.result, response.err)
		}
		for _, site := range response.result.Sites {
			if site.SiteID == success.ID {
				if site.ConsumeDate != response.result.BusinessDate {
					t.Errorf("synchronized snapshot date %s differs from injected business date %s", site.ConsumeDate, response.result.BusinessDate)
				}
				if site.CollectedAt == nil || businesstime.DateAt(*site.CollectedAt) != response.result.BusinessDate {
					t.Error("collection date cannot be used for same-day reconciliation")
				}
				if !site.StartedAt.After(startedAt) || len(site.Items) != 1 || site.Items[0].RawAmount != 2 {
					t.Fatalf("old collector accepted: %+v", site)
				}
			} else if site.Status != "missing" || len(site.Items) != 0 {
				t.Fatalf("failed sync waited/collected: %+v", site)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("refresh still waits after its supplement finished")
	}
	waitKeySnapshotFlights(t, svc)
	if keys.Load() != 2 || failedKeys.Load() != 0 {
		t.Fatalf("collection counts successful=%d failed=%d", keys.Load(), failedKeys.Load())
	}
}

func waitForHomeCostSupplement(t *testing.T, svc *Service, id string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		svc.mu.Lock()
		flight := svc.keyUsageFlights[id]
		pending := flight != nil && flight.pending != nil
		changed := svc.keyUsageChanged
		svc.mu.Unlock()
		if pending {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("sync did not register one supplement")
		}
	}
}

func TestHomeCostStageAOrdinary429FailsOnlyItsStation(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var rejected atomic.Int32
	var successful atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/dashboard/api-keys-usage":
			if r.Header.Get("Authorization") == "Bearer limited-fixture" {
				rejected.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(429)
				writeJSON(w, map[string]any{"success": false})
				return
			}
			successful.Add(1)
			writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 3}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	limited := newTestSite("limited", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "limited-fixture"})
	good := newTestSite("good", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	for _, site := range []*Site{limited, good} {
		site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
	}
	cache := newKeySnapshotTestCache(limited, good)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, nil, nil, cache)
	svc.now = func() time.Time { return now }
	svc.enqueueKeyUsageCollection(*limited)
	svc.enqueueKeyUsageCollection(*good)
	waitKeySnapshotFlights(t, svc)
	result, err := svc.CachedKeyUsageForDate(t.Context(), "user", "workspace", businesstime.DateAt(now))
	if err != nil || result.CompletedSites != 1 || result.ExpectedSites != 2 || rejected.Load() != 2 || successful.Load() != 1 {
		t.Fatalf("429 affected other station result=%v counts=%d/%d err=%v", result, rejected.Load(), successful.Load(), err)
	}
	for _, site := range result.Sites {
		if site.SiteID == "limited" {
			if site.Status != "missing" || len(site.Items) != 0 || site.Error != ErrorRateLimited {
				t.Fatalf("429 failure counted as cost: %+v", site)
			}
		} else if site.Status != "ok" || site.Items[0].TodayAmount != 3 {
			t.Fatalf("successful station lost: %+v", site)
		}
	}
	current, _ := cache.Get(t.Context(), limited.ID)
	if current.Status != StatusConnected {
		t.Fatal("key collection failure changed connection state")
	}
}

func TestHomeCostStageACollectionKeepsSeparateTotalsAndDateMismatch(t *testing.T) {
	before := time.Date(2031, 2, 3, 15, 59, 59, 0, time.UTC)
	after := before.Add(2 * time.Second)
	var totals atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/usage/dashboard/stats":
			totals.Add(1)
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 9}})
		case "/api/v1/keys":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/dashboard/api-keys-usage":
			writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 8}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	amount := float64(7)
	site.Metrics.TodayConsume.Value = &amount
	site.Metrics.TodayConsumeDate = businesstime.DateAt(before)
	cache := newKeySnapshotTestCache(site)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return after }
	svc := NewService(platform, nil, nil, cache)
	svc.now = func() time.Time { return after }
	svc.enqueueKeyUsageCollection(*site)
	waitKeySnapshotFlights(t, svc)
	result, err := svc.CachedKeyUsageForDate(t.Context(), "user", "workspace", businesstime.DateAt(after))
	if err != nil || result.CompletedSites != 1 || totals.Load() != 1 {
		t.Fatalf("collection result=%v totals=%d err=%v", result, totals.Load(), err)
	}
	collected := result.Sites[0]
	if collected.ConsumeDate == result.BusinessDate || collected.SyncedRawCost == nil || *collected.SyncedRawCost != 7 || collected.CollectedRawCost == nil || *collected.CollectedRawCost != 9 || collected.Items[0].RawAmount != 8 {
		t.Fatalf("cross-day totals/date were hidden or zeroed: %+v", collected)
	}
}

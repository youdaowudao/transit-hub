package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/shared/businesstime"
)

// Explicit store opt-in avoids starting collectors in unrelated legacy fixtures.
type keySnapshotTestCache struct {
	*syncTestCache
	snapshotsMu sync.Mutex
	snapshots   map[string]KeyUsageSnapshot
	writes      chan KeyUsageSnapshot
}

func (c *keySnapshotTestCache) add(site *Site) { _ = c.Set(context.Background(), site) }
func newKeySnapshotTestCache(sites ...*Site) *keySnapshotTestCache {
	return &keySnapshotTestCache{syncTestCache: newSyncTestCache(sites...), snapshots: make(map[string]KeyUsageSnapshot), writes: make(chan KeyUsageSnapshot, 16)}
}
func (c *keySnapshotTestCache) GetKeyUsageSnapshot(_ context.Context, id string) (*KeyUsageSnapshot, error) {
	c.snapshotsMu.Lock()
	defer c.snapshotsMu.Unlock()
	value, ok := c.snapshots[id]
	if !ok {
		return nil, nil
	}
	copy := value
	return &copy, nil
}
func (c *keySnapshotTestCache) SaveKeyUsageSnapshot(_ context.Context, id string, value KeyUsageSnapshot) error {
	c.snapshotsMu.Lock()
	defer c.snapshotsMu.Unlock()
	previous, exists := c.snapshots[id]
	var prior *KeyUsageSnapshot
	if exists {
		prior = &previous
	}
	next, write := newerKeyUsageSnapshot(prior, value)
	if !write {
		return nil
	}
	value = next
	c.snapshots[id] = value
	select {
	case c.writes <- value:
	default:
	}
	return nil
}
func (c *keySnapshotTestCache) DeleteKeyUsageSnapshot(_ context.Context, id string) error {
	c.snapshotsMu.Lock()
	defer c.snapshotsMu.Unlock()
	delete(c.snapshots, id)
	return nil
}
func fixtureKeySnapshot(now time.Time, amount float64) KeyUsageSnapshot {
	return KeyUsageSnapshot{BusinessDate: businesstime.DateAt(now), StartedAt: now, AttemptStartedAt: now, CollectedAt: now.Add(time.Second), ConsumeDate: businesstime.DateAt(now), SyncedRawCost: &amount, CollectedRawCost: &amount, Complete: true, Items: []KeyUsageTodayStat{{KeyID: "1", KeyIDs: []string{"1"}, KeyName: "one", TodayAmount: amount}}}
}

func TestHomeCostStageAReadsSameDateSnapshotsWithCurrentRateAndRetention(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	primary := newTestSite("one", "user", "workspace", 2, nil)
	foreign := newTestSite("foreign", "user", "other", 2, nil)
	disabled := newTestSite("disabled", "user", "workspace", 2, nil)
	disabled.Enabled = boolPointer(false)
	missing := newTestSite("missing", "user", "workspace", 1, nil)
	cache := newKeySnapshotTestCache(primary, foreign, disabled, missing)
	value := fixtureKeySnapshot(now, 3)
	value.FailureReason = ErrorRateLimited
	failure := now.Add(2 * time.Second)
	value.FailureAt = &failure
	_ = cache.SaveKeyUsageSnapshot(context.Background(), primary.ID, value)
	_ = cache.SaveKeyUsageSnapshot(context.Background(), foreign.ID, fixtureKeySnapshot(now, 99))
	_ = cache.SaveKeyUsageSnapshot(context.Background(), disabled.ID, fixtureKeySnapshot(now, 99))
	_ = cache.SaveKeyUsageSnapshot(context.Background(), missing.ID, fixtureKeySnapshot(now.Add(-24*time.Hour), 99))
	svc := NewService(nil, nil, nil, cache)
	svc.now = func() time.Time { return now }
	result, err := svc.CachedKeyUsageForDate(context.Background(), "user", "workspace", "")
	if err != nil || result.ExpectedSites != 2 || result.CompletedSites != 1 {
		t.Fatalf("snapshot coverage %v %v", result, err)
	}
	for _, site := range result.Sites {
		if site.SiteID == "one" {
			if site.Status != "retained" || len(site.Items) != 1 || site.Items[0].TodayAmount != 6 || site.CollectedAt == nil || site.Error != ErrorRateLimited {
				t.Fatalf("retained snapshot %v", site)
			}
		} else if site.Status != "missing" || len(site.Items) != 0 {
			t.Fatalf("previous-day snapshot used: %v", site)
		}
	}
	primary.RechargeRate = 4
	_ = cache.Set(context.Background(), primary)
	result, _ = svc.CachedKeyUsageForDate(context.Background(), "user", "workspace", "")
	for _, site := range result.Sites {
		if site.SiteID == "one" && site.Items[0].TodayAmount != 12 {
			t.Fatal("stored rate overrode current rate")
		}
	}
}

func TestHomeCostStageANotifiesSyncBeforeCollectingAndQueuesOnlyOnce(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var keyReads atomic.Int32
	entered := make(chan struct{}, 3)
	release := make(chan struct{}, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 3}})
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": []any{}})
		case "/api/v1/keys":
			keyReads.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "one"}}, "total": 1})
		case "/api/v1/usage/dashboard/api-keys-usage":
			writeJSON(w, map[string]any{"data": map[string]any{"stats": map[string]any{"1": map[string]any{"today_actual_cost": 3}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	cache := newKeySnapshotTestCache(site)
	platform := NewPlatformService(NewHTTPClient(server.Client()))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, nil, nil, cache)
	svc.now = func() time.Time { return now }
	done := make(chan struct{})
	go func() { _, _ = svc.sync(context.Background(), site.ID); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sync waited for blocked key collector")
	}
	fresh, _ := cache.Get(context.Background(), site.ID)
	svc.enqueueKeyUsageCollection(*fresh)
	svc.enqueueKeyUsageCollection(*fresh)
	svc.enqueueKeyUsageCollection(*fresh)
	release <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("queued supplemental collection did not run")
	}
	release <- struct{}{}
	waitKeySnapshotFlights(t, svc)
	if keyReads.Load() != 2 {
		t.Fatalf("pending collection count=%d want2", keyReads.Load())
	}
}

func waitKeySnapshotFlights(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		svc.mu.Lock()
		active := len(svc.keyUsageFlights) > 0
		changed := svc.keyUsageChanged
		svc.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("collector not reclaimed")
		}
	}
}

func TestHomeCostStageAFailedSyncDoesNotCollect(t *testing.T) {
	var keyReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/keys" {
			keyReads.Add(1)
		}
		w.WriteHeader(401)
	}))
	defer server.Close()
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	cache := newKeySnapshotTestCache(site)
	svc := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	results := svc.SyncSites(context.Background(), "user", "workspace", []string{site.ID}, true)
	if len(results) != 1 || results[0].Status == "success" || keyReads.Load() != 0 {
		t.Fatalf("failed sync collected: %v %d", results, keyReads.Load())
	}
}

func TestHomeCostStageACollectorRetainsFailureAndWorkspaceCleanup(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer server.Close()
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
	cache := newKeySnapshotTestCache(site)
	_ = cache.SaveKeyUsageSnapshot(context.Background(), site.ID, fixtureKeySnapshot(now.Add(-time.Minute), 3))
	svc := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	svc.now = func() time.Time { return now }
	svc.collectKeyUsageSnapshot(*site)
	value, _ := cache.GetKeyUsageSnapshot(context.Background(), site.ID)
	if !value.Complete || value.Items[0].TodayAmount != 3 || value.FailureAt == nil || value.FailureReason == "" {
		t.Fatalf("same-day complete snapshot not retained %v", value)
	}
	if err := svc.CleanupDeletedWorkspaceSites(context.Background(), "user", []string{site.ID}); err != nil {
		t.Fatal(err)
	}
	value, _ = cache.GetKeyUsageSnapshot(context.Background(), site.ID)
	if value != nil {
		t.Fatal("workspace removal left key snapshot")
	}
	svc.collectKeyUsageSnapshot(*site)
	value, _ = cache.GetKeyUsageSnapshot(context.Background(), site.ID)
	if value != nil {
		t.Fatal("deleted site snapshot resurrected")
	}
}

func TestHomeCostStageAQuota429NeverRetriesAndHeaderDelayBounded(t *testing.T) {
	for _, code := range []string{"API_KEY_QUOTA_EXHAUSTED", "INSUFFICIENT_QUOTA"} {
		t.Run(code, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(429)
				writeJSON(w, map[string]any{"code": code, "message": "do not persist this"})
			}))
			defer server.Close()
			_, err := NewPlatformService(NewHTTPClient(server.Client())).requestKeyUsageJSONWithContext(context.Background(), server.URL, requestOptions{})
			if err == nil || requests.Load() != 1 {
				t.Fatalf("quota refusal retried %d %v", requests.Load(), err)
			}
		})
	}
	if keyUsageRetryAfter("999", time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)) != 2*time.Second || keyUsageRetryAfter("-1", time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)) != 0 {
		t.Fatal("Retry-After cap is not two seconds")
	}
}

func TestHomeCostStageACollectionRequestsShare45SecondDeadline(t *testing.T) {
	var deadlineRemaining time.Duration
	client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("collector request lacks deadline")
		}
		deadlineRemaining = time.Until(deadline)
		return nil, errors.New("fixture failure")
	})}
	site := newTestSite("one", "user", "workspace", 1, &Session{Platform: PlatformNewAPI, BaseURL: "http://127.0.0.1", Cookie: "fixture", UserID: "1"})
	cache := newKeySnapshotTestCache(site)
	svc := NewService(NewPlatformService(NewHTTPClient(client)), nil, nil, cache)
	svc.collectKeyUsageSnapshot(*site)
	if deadlineRemaining <= 0 || deadlineRemaining > keyUsageCollectionTimeout {
		t.Fatalf("deadline outside45s: %s", deadlineRemaining)
	}
}

type keyUsageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f keyUsageRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

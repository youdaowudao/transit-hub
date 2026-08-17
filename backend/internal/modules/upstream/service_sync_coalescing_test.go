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
)

type syncTestCache struct {
	mu    sync.RWMutex
	sites map[string]*Site
}

func newSyncTestCache(sites ...*Site) *syncTestCache {
	cache := &syncTestCache{sites: make(map[string]*Site, len(sites))}
	for _, site := range sites {
		cache.Set(context.Background(), site)
	}
	return cache
}

func (c *syncTestCache) Get(_ context.Context, id string) (*Site, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	site := c.sites[id]
	if site == nil {
		return nil, nil
	}
	copy := *site
	return &copy, nil
}

func (c *syncTestCache) Set(_ context.Context, site *Site) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *site
	c.sites[site.ID] = &copy
	return nil
}

func (c *syncTestCache) Delete(_ context.Context, id string, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sites, id)
	return nil
}

func (c *syncTestCache) ListByUser(_ context.Context, userID string) ([]*Site, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]*Site, 0, len(c.sites))
	for _, site := range c.sites {
		if site.UserID != userID {
			continue
		}
		copy := *site
		result = append(result, &copy)
	}
	return result, nil
}

func (c *syncTestCache) Flush(context.Context) error { return nil }

func TestSyncCoalescesConcurrentCallsForSameSite(t *testing.T) {
	var metricsCalls atomic.Int32
	metricsStarted := make(chan struct{}, 2)
	releaseMetrics := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			metricsCalls.Add(1)
			metricsStarted <- struct{}{}
			<-releaseMetrics
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10.0, "total_recharged": 20.0}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1.0}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "group-1", "name": "default", "rate_multiplier": 1.0}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"group-1": 1.0}})
		default:
			t.Fatalf("unexpected upstream request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	cache := newSyncTestCache(newTestSite("site-1", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	}))
	service := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := service.Sync(context.Background(), "user-1", "site-1")
			results <- err
		}()
	}

	select {
	case <-metricsStarted:
	case <-time.After(time.Second):
		t.Fatal("first sync did not reach metric fetch")
	}
	select {
	case <-metricsStarted:
		close(releaseMetrics)
		t.Fatalf("same-site sync started more than one metrics fetch")
	case <-time.After(100 * time.Millisecond):
		close(releaseMetrics)
	}

	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}
	if got := metricsCalls.Load(); got != 1 {
		t.Fatalf("metrics fetches = %d, want 1", got)
	}
}

func TestSyncDifferentSitesRunConcurrently(t *testing.T) {
	metricsStarted := make(chan string, 2)
	releaseMetrics := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			token := r.Header.Get("Authorization")
			metricsStarted <- token
			<-releaseMetrics
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10.0, "total_recharged": 20.0}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1.0}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "group-1", "name": "default", "rate_multiplier": 1.0}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"group-1": 1.0}})
		default:
			t.Fatalf("unexpected upstream request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseMetrics) }) }
	t.Cleanup(release)

	cache := newSyncTestCache(
		newTestSite("site-1", "user-1", "workspace-1", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token-1"}),
		newTestSite("site-2", "user-1", "workspace-1", 1, &Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token-2"}),
	)
	service := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})

	results := make(chan error, 2)
	for _, siteID := range []string{"site-1", "site-2"} {
		go func(id string) {
			_, err := service.Sync(context.Background(), "user-1", id)
			results <- err
		}(siteID)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case token := <-metricsStarted:
			seen[token] = true
		case <-time.After(time.Second):
			t.Fatal("different-site syncs did not run concurrently")
		}
	}
	release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("metric fetches = %v, want both site tokens", seen)
	}
}

func TestSyncSitesAutomaticReusesCachedStateAndEnforcesWorkspace(t *testing.T) {
	cache := newSyncTestCache(
		newTestSite("site-1", "user-1", "workspace-1", 1, &Session{Platform: PlatformSub2API, AccessToken: "token-1"}),
		newTestSite("site-2", "user-1", "workspace-2", 1, &Session{Platform: PlatformSub2API, AccessToken: "token-2"}),
	)
	cache.sites["site-1"].Status = StatusConnected
	cache.sites["site-2"].Status = StatusConnected
	service := NewService(nil, nil, nil, cache)

	results := service.SyncSites(context.Background(), "user-1", "workspace-1", []string{"site-2", "site-1", "site-1"}, false)
	if len(results) != 2 {
		t.Fatalf("automatic results = %+v, want deduplicated sites", results)
	}
	if results[0].SiteID != "site-1" || results[0].Status != "success" {
		t.Fatalf("site-1 automatic result = %+v, want cached success", results[0])
	}
	if results[1].SiteID != "site-2" || results[1].Status != "unavailable" || results[1].ErrorKey != "site_sync_unavailable" {
		t.Fatalf("cross-workspace automatic result = %+v, want safe unavailable", results[1])
	}
}

func TestSyncReleaseMapsUpstreamFailureStages(t *testing.T) {
	keys := []struct {
		upstreamKey string
		status      string
		errorKey    string
	}{
		{upstreamKey: ErrorNetwork, status: "unavailable", errorKey: "site_sync_network"},
		{upstreamKey: ErrorInvalidResponse, status: "unavailable", errorKey: "site_sync_invalid_response"},
		{upstreamKey: ErrorRequest, status: "unavailable", errorKey: "site_sync_request"},
	}
	for _, item := range keys {
		errorKey := item.upstreamKey
		result := syncSiteResult("site-1", Response{Status: StatusError, ErrorKey: &errorKey}, nil)
		if result.Status != item.status || result.ErrorKey != item.errorKey {
			t.Fatalf("syncSiteResult(%q) = %+v, want status=%q errorKey=%q", item.upstreamKey, result, item.status, item.errorKey)
		}
	}
}

func TestSyncReleaseMapsHTTPTimeoutToTimeoutResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" {
			time.Sleep(50 * time.Millisecond)
			return
		}
		t.Fatalf("unexpected upstream request: %s", r.URL.Path)
	}))
	t.Cleanup(server.Close)

	cache := newSyncTestCache(newTestSite("site-1", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	}))
	service := NewService(NewPlatformService(NewHTTPClient(&http.Client{Timeout: 10 * time.Millisecond})), nil, nil, cache)
	result := service.SyncSites(context.Background(), "user-1", "workspace-1", []string{"site-1"}, true)
	if len(result) != 1 || result[0].Status != "timeout" || result[0].ErrorKey != "site_sync_timeout" {
		t.Fatalf("timeout sync result = %+v, want timeout terminal result", result)
	}
}

func TestSyncCancellationDoesNotCancelSharedSameSiteFlight(t *testing.T) {
	metricsStarted := make(chan struct{}, 1)
	releaseMetrics := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseMetrics) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			metricsStarted <- struct{}{}
			<-releaseMetrics
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10.0, "total_recharged": 20.0}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1.0}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "group-1", "name": "default", "rate_multiplier": 1.0}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"group-1": 1.0}})
		default:
			t.Fatalf("unexpected upstream request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(release)

	cache := newSyncTestCache(newTestSite("site-1", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	}))
	service := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})

	firstResult := make(chan error, 1)
	go func() {
		_, err := service.Sync(context.Background(), "user-1", "site-1")
		firstResult <- err
	}()
	select {
	case <-metricsStarted:
	case <-time.After(time.Second):
		t.Fatal("shared sync did not reach metric fetch")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan error, 1)
	go func() {
		_, err := service.Sync(ctx, "user-1", "site-1")
		secondResult <- err
	}()
	cancel()
	select {
	case err := <-secondResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Sync() error = %v, want context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cancelled waiter stayed blocked on the shared sync")
	}

	release()
	if err := <-firstResult; err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
}

func TestSyncReleasesFlightAfterFailure(t *testing.T) {
	var authMeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			if authMeCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10.0, "total_recharged": 20.0}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1.0}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "group-1", "name": "default", "rate_multiplier": 1.0}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"group-1": 1.0}})
		default:
			t.Fatalf("unexpected upstream request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	cache := newSyncTestCache(newTestSite("site-1", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	}))
	service := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})

	first, err := service.Sync(context.Background(), "user-1", "site-1")
	if err != nil {
		t.Fatalf("first Sync() error = %v, want terminal site status", err)
	}
	if first.Status != StatusError {
		t.Fatalf("first Sync() status = %s, want %s", first.Status, StatusError)
	}
	second, err := service.Sync(context.Background(), "user-1", "site-1")
	if err != nil {
		t.Fatalf("second Sync() error = %v, want a fresh retry", err)
	}
	if second.Status != StatusConnected {
		t.Fatalf("second Sync() status = %s, want %s", second.Status, StatusConnected)
	}
	if got := authMeCalls.Load(); got != 2 {
		t.Fatalf("metric fetch attempts = %d, want 2 after retry", got)
	}
}

type panicOnceSnapshotWriter struct {
	calls atomic.Int32
}

func (w *panicOnceSnapshotWriter) SaveSiteSnapshot(context.Context, string, string, string, string, Platform, []SnapshotGroup) error {
	if w.calls.Add(1) == 1 {
		panic("snapshot writer panic")
	}
	return nil
}

func TestSyncReleasesFlightAfterPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10.0, "total_recharged": 20.0}})
		case "/api/v1/usage/dashboard/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"today_actual_cost": 1.0}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []map[string]any{{"id": "group-1", "name": "default", "rate_multiplier": 1.0}}})
		case "/api/v1/groups/rates":
			writeJSON(w, map[string]any{"data": map[string]any{"group-1": 1.0}})
		default:
			t.Fatalf("unexpected upstream request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	cache := newSyncTestCache(newTestSite("site-1", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	}))
	service := NewService(NewPlatformService(NewHTTPClient(server.Client())), nil, &panicOnceSnapshotWriter{}, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})

	if _, err := service.Sync(context.Background(), "user-1", "site-1"); err == nil {
		t.Fatal("panic Sync() error = nil, want safe terminal error")
	}
	if _, err := service.Sync(context.Background(), "user-1", "site-1"); err != nil {
		t.Fatalf("retry after panic error = %v, want a released flight", err)
	}
}

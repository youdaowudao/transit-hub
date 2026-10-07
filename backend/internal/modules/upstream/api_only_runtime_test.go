package upstream

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type c5APIOnlyRepository struct {
	*enabledTestRepository
	listCalls int
}

type c5APIOnlyCostCache struct {
	*syncTestCache
	GroupCostStore
	started chan struct{}
}

func (c *c5APIOnlyCostCache) TryStartGroupCostSampling(context.Context, string, time.Duration) (bool, error) {
	c.started <- struct{}{}
	return false, nil
}

func (c *c5APIOnlyCostCache) GetGroupCostSamplingState(context.Context, string) (GroupCostSamplingState, error) {
	return GroupCostSamplingState{}, nil
}

func (c *c5APIOnlyCostCache) ListGroupCostSamples(context.Context, string, string, string) ([]GroupCostSample, error) {
	return nil, nil
}

func (r *c5APIOnlyRepository) ListSites(ctx context.Context) ([]Site, error) {
	r.listCalls++
	return r.enabledTestRepository.ListSites(ctx)
}

func TestC5APIOnlyUpstreamPreventsEveryScheduleAndKeepsDefaultEnabled(t *testing.T) {
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AccessToken: "synthetic"})
	repo := &c5APIOnlyRepository{enabledTestRepository: &enabledTestRepository{sites: []Site{*site}}}
	s := NewService(nil, repo, nil, newSyncTestCache(site))
	defer s.Close()
	s.SetWorkspaceRefreshConfig("user", "workspace", RefreshConfig{Enabled: true, Interval: time.Hour})
	if s.timers[site.ID] == nil || repo.listCalls != 1 {
		t.Fatal("default instance no longer schedules its workspace")
	}
	s.SetBackgroundTasksDisabled(true)
	s.SetWorkspaceRefreshConfig("user", "workspace", RefreshConfig{Enabled: true, Interval: time.Hour})
	s.mu.Lock()
	regular := s.scheduleSyncLocked(site.ID, site)
	initial := s.scheduleInitialSyncLocked(site.ID, site)
	s.mu.Unlock()
	if regular || initial || len(s.timers) != 0 || repo.listCalls != 1 {
		t.Fatal("API-only reloaded strategy sites or rearmed a timer")
	}
	s.SetBackgroundTasksDisabled(false)
	s.SetWorkspaceRefreshConfig("user", "workspace", RefreshConfig{Enabled: true, Interval: time.Hour})
	if s.timers[site.ID] == nil || repo.listCalls != 2 {
		t.Fatal("process-local disable changed normal persisted scheduling behavior")
	}
}

func TestC5APIOnlyManualSyncPersistsWithoutTimersOrBackgroundHooks(t *testing.T) {
	session := c5ImportSession(PlatformNewAPI)
	site := newTestSite("site", "user", "workspace", 1, &session)
	site.Platform = PlatformNewAPI
	platform := c5ImportService(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/user/self":
			return c5ImportResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"id": 1, "quota": 5000000}})
		case "/api/log/self/stat":
			return c5ImportResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"quota": 1500000}})
		case "/api/user/self/groups":
			return c5ImportResponse(r, 200, map[string]any{"success": true, "data": map[string]any{"default": map[string]any{"ratio": 1.0}}})
		default:
			return c5ImportResponse(r, 200, map[string]any{"success": true, "data": map[string]any{}})
		}
	})
	repo := &enabledTestRepository{}
	cache := &c5APIOnlyCostCache{syncTestCache: newSyncTestCache(site), started: make(chan struct{}, 1)}
	s := NewService(platform, repo, nil, cache)
	defer s.Close()
	s.SetBackgroundTasksDisabled(true)
	s.SetWorkspaceRefreshConfig("user", "workspace", RefreshConfig{Enabled: true, Interval: time.Hour})
	var hooks atomic.Int32
	s.AfterSync = func(context.Context, string, string, string, string, Metrics, Metrics, Status, Status) { hooks.Add(1) }
	response, err := s.syncOnce(context.Background(), site.ID)
	if err != nil || response.Status != StatusConnected || len(repo.saved) != 1 || len(s.timers) != 0 || hooks.Load() != 0 {
		t.Fatal("API-only manual sync failed, did not persist, or started background work")
	}
	if len(response.Metrics.Groups) == 0 {
		t.Fatal("fixture did not provide sampleable groups")
	}
	select {
	case <-cache.started:
		t.Fatal("API-only started detached cost sampling")
	case <-time.After(20 * time.Millisecond):
	}
	if hooks.Load() != 0 {
		t.Fatal("API-only ran a delayed automatic post-sync hook")
	}
}

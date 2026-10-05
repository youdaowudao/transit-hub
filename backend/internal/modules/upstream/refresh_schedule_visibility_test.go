package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Observe the actual timer duration, then associate the returned timer with its
// site ID. Production logs are not part of the scheduling contract.
func observeScheduledDelays(service *Service) func() map[string]string {
	timerDelays := make(map[*time.Timer]time.Duration)
	service.afterFunc = func(delay time.Duration, callback func()) *time.Timer {
		timer := time.AfterFunc(delay, callback)
		timerDelays[timer] = delay
		return timer
	}
	return func() map[string]string {
		service.mu.Lock()
		defer service.mu.Unlock()
		delays := make(map[string]string)
		for siteID, timer := range service.timers {
			if delay, ok := timerDelays[timer]; ok {
				delays[siteID] = delay.String()
			}
		}
		return delays
	}
}

func newInitialScheduleService(siteCount int) (*Service, *enabledTestRepository, []*Site) {
	sites := make([]*Site, 0, siteCount)
	repository := &enabledTestRepository{}
	cache := newFakeSiteCache()
	for index := 0; index < siteCount; index++ {
		site := newTestSite(
			"site-jitter-"+time.Date(2026, 8, 22, 0, 0, index, 0, time.UTC).Format("150405"),
			"user-1",
			"workspace-1",
			1,
			&Session{Platform: PlatformSub2API, AccessToken: "test-only"},
		)
		sites = append(sites, site)
		repository.sites = append(repository.sites, *site)
		cache.add(site)
	}
	return NewService(nil, repository, nil, cache), repository, sites
}

func TestInitialWorkspaceSchedulesUseStableJitterOnlyOnce(t *testing.T) {
	const interval = 20 * time.Minute
	firstService, _, sites := newInitialScheduleService(26)
	t.Cleanup(firstService.Close)
	firstObservation := observeScheduledDelays(firstService)
	firstService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	firstDelays := firstObservation()
	if len(firstDelays) != len(sites) {
		t.Fatalf("initial scheduled sites = %d, want %d; delays=%v", len(firstDelays), len(sites), firstDelays)
	}
	distinct := make(map[string]struct{})
	for _, delay := range firstDelays {
		distinct[delay] = struct{}{}
		parsed, err := time.ParseDuration(delay)
		if err != nil {
			t.Fatalf("parse initial delay %q: %v", delay, err)
		}
		if parsed < interval || parsed >= interval+time.Minute {
			t.Fatalf("initial delay = %s, want [%s, %s)", parsed, interval, interval+time.Minute)
		}
	}
	if len(distinct) < 2 {
		t.Fatalf("26 initial schedules used no stable jitter: %v", firstDelays)
	}

	firstService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	laterDelays := firstObservation()
	for siteID, delay := range laterDelays {
		if delay != interval.String() {
			t.Fatalf("subsequent schedule for %s = %s, want unchanged interval %s", siteID, delay, interval)
		}
	}

	replayService, _, _ := newInitialScheduleService(26)
	t.Cleanup(replayService.Close)
	replayObservation := observeScheduledDelays(replayService)
	replayService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	replayedDelays := replayObservation()
	for siteID, delay := range firstDelays {
		if replayedDelays[siteID] != delay {
			t.Fatalf("stable initial delay for %s changed from %s to %s", siteID, delay, replayedDelays[siteID])
		}
	}
}

func TestSyncAllStreamReportsBusinessFailureAsErrorEvent(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired test token"}`))
	}))
	t.Cleanup(upstreamServer.Close)

	site := newTestSite("site-business-failure", "user-1", "workspace-1", 1, &Session{
		Platform: PlatformSub2API, BaseURL: upstreamServer.URL, AccessToken: "test-only",
	})
	cache := newSyncTestCache(site)
	service := NewService(NewPlatformService(NewHTTPClient(upstreamServer.Client())), nil, nil, cache)
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user-1": "workspace-1"}})
	events := make([]SyncEvent, 0)

	if err := service.SyncAllStream(context.Background(), "user-1", func(event SyncEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("SyncAllStream() error = %v", err)
	}

	var hasError, hasDone bool
	for _, event := range events {
		if event.SiteID != site.ID {
			continue
		}
		hasError = hasError || event.Event == SyncEventError
		hasDone = hasDone || event.Event == SyncEventDone
	}
	if !hasError || hasDone {
		t.Fatalf("business failure events = %#v, want error and no done", events)
	}
}

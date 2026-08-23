package upstream

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var scheduledDelayPattern = regexp.MustCompile(`id=([^ ]+) delay=([^ ]+)`)

func captureScheduledDelays(t *testing.T, output string) map[string]string {
	t.Helper()
	delays := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		match := scheduledDelayPattern.FindStringSubmatch(line)
		if len(match) == 3 {
			delays[match[1]] = match[2]
		}
	}
	return delays
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
	originalLogWriter := log.Writer()
	originalLogFlags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalLogWriter)
		log.SetFlags(originalLogFlags)
	})

	var firstLog bytes.Buffer
	log.SetOutput(&firstLog)
	firstService, _, sites := newInitialScheduleService(26)
	t.Cleanup(firstService.Close)
	firstService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	firstDelays := captureScheduledDelays(t, firstLog.String())
	if len(firstDelays) != len(sites) {
		t.Fatalf("initial scheduled sites = %d, want %d; log=%s", len(firstDelays), len(sites), firstLog.String())
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

	var laterLog bytes.Buffer
	log.SetOutput(&laterLog)
	firstService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	laterDelays := captureScheduledDelays(t, laterLog.String())
	for siteID, delay := range laterDelays {
		if delay != interval.String() {
			t.Fatalf("subsequent schedule for %s = %s, want unchanged interval %s", siteID, delay, interval)
		}
	}

	var replayLog bytes.Buffer
	log.SetOutput(&replayLog)
	replayService, _, _ := newInitialScheduleService(26)
	t.Cleanup(replayService.Close)
	replayService.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	replayedDelays := captureScheduledDelays(t, replayLog.String())
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

package upstream

import (
	"bytes"
	"log"
	"testing"
	"time"
)

func TestWorkspaceRefreshConfigSchedulesOnlyMatchingSites(t *testing.T) {
	siteA := newTestSite("site-a", "user-1", "workspace-a", 1, &Session{Platform: PlatformSub2API, AccessToken: "token-a"})
	siteB := newTestSite("site-b", "user-1", "workspace-b", 1, &Session{Platform: PlatformSub2API, AccessToken: "token-b"})
	repository := &enabledTestRepository{sites: []Site{*siteA, *siteB}}
	service := NewService(nil, repository, nil, newFakeSiteCache())
	t.Cleanup(service.Close)

	service.SetWorkspaceRefreshConfig("user-1", "workspace-a", RefreshConfig{Enabled: true, Interval: time.Hour})
	if service.timers[siteA.ID] == nil {
		t.Fatal("workspace-a site was not scheduled")
	}
	if service.timers[siteB.ID] != nil {
		t.Fatal("workspace-b site inherited workspace-a refresh config")
	}

	service.SetWorkspaceRefreshConfig("user-1", "workspace-b", RefreshConfig{Enabled: true, Interval: 2 * time.Hour})
	if service.timers[siteA.ID] == nil || service.timers[siteB.ID] == nil {
		t.Fatalf("expected both workspace timers, got site-a=%v site-b=%v", service.timers[siteA.ID], service.timers[siteB.ID])
	}

	service.SetWorkspaceRefreshConfig("user-1", "workspace-a", RefreshConfig{Enabled: false})
	if service.timers[siteA.ID] != nil {
		t.Fatal("disabled workspace-a timer still exists")
	}
	if service.timers[siteB.ID] == nil {
		t.Fatal("disabling workspace-a cleared workspace-b timer")
	}
}

func TestInitialSyncDelayUsesNoOffsetForSubsecondWindow(t *testing.T) {
	const interval = 9 * time.Second
	if delay := initialSyncDelay("site-short-interval", interval); delay != interval {
		t.Fatalf("initial delay = %s, want exact interval %s when jitter window is below one second", delay, interval)
	}
}

func TestDisabledConfigDoesNotConsumeInitialScheduleJitter(t *testing.T) {
	const interval = 20 * time.Minute
	site := newTestSite("site-disabled-then-enabled", "user-1", "workspace-1", 1, &Session{Platform: PlatformSub2API, AccessToken: "test-only"})
	repository := &enabledTestRepository{sites: []Site{*site}}
	service := NewService(nil, repository, nil, newFakeSiteCache())
	t.Cleanup(service.Close)

	originalLogWriter := log.Writer()
	originalLogFlags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(originalLogWriter)
		log.SetFlags(originalLogFlags)
	})

	var output bytes.Buffer
	log.SetOutput(&output)
	service.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: false, Interval: interval})
	if service.timers[site.ID] != nil {
		t.Fatal("disabled config unexpectedly created a timer")
	}

	output.Reset()
	service.SetWorkspaceRefreshConfig("user-1", "workspace-1", RefreshConfig{Enabled: true, Interval: interval})
	delay := captureScheduledDelays(t, output.String())[site.ID]
	want := initialSyncDelay(site.ID, interval).String()
	if delay != want {
		t.Fatalf("first real schedule after disabled config = %s, want initial jitter %s; log=%s", delay, want, output.String())
	}
}

package upstream

import (
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

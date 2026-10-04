package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func TestFailureFixStageDUnreadableReferencePreservesOnlyExistingLiveEntries(t *testing.T) {
	for _, sample := range []struct {
		name           string
		cached         bool
		otherWorkspace bool
		expired        bool
		force          bool
		preserve       bool
	}{
		{name: "current_workspace", cached: true, preserve: true},
		{name: "force_does_not_refresh_unreadable_site", cached: true, force: true, preserve: true},
		{name: "uncached_does_not_create_entry"},
		{name: "expired_retention_still_cleans", cached: true, expired: true},
		{name: "other_workspace_is_unchanged", cached: true, otherWorkspace: true, preserve: true},
		{name: "other_workspace_expired_retention_still_cleans", cached: true, otherWorkspace: true, expired: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			cache := &failureFixRecoverableSiteRead{err: errors.New("fixture cache unavailable")}
			reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}}}
			workspace := "ws1"
			if sample.otherWorkspace {
				workspace = "ws2"
			}
			cacheKey := multiplierSnapshotKey("user1", workspace, "site-1")
			before := time.Now()
			lastAccess := before.Add(-time.Minute)
			if sample.expired {
				lastAccess = before.Add(-multiplierSnapshotRetention - time.Minute)
			}
			deadline := before.Add(30 * time.Minute)
			original := &multiplierSnapshotEntry{
				workspaceKey: cacheKey, siteID: "site-1", userID: "user1", adminAccountID: workspace,
				platform: upstream.PlatformSub2API, bindingSignature: "key-1", keyIDs: []string{"key-1"},
				generation: 3, status: MultiplierResolutionUnavailable, lastAccessAt: lastAccess,
				nextRetryAt: deadline, consecutiveFailures: 3,
			}
			service := &Service{mySites: reader, sites: cache, multiplierSnapshots: map[string]*multiplierSnapshotEntry{}}
			if sample.cached {
				service.multiplierSnapshots[cacheKey] = original
			}
			lookup := service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, sample.force)
			if !lookup.unavailable || lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
				t.Error("unreadable reference did not stay unavailable")
			}
			if direct, lists := reader.callCounts("site-1"); direct != 0 || lists != 0 {
				t.Error("unreadable reference requested metadata")
			}
			preserved := service.multiplierSnapshots[cacheKey]
			if sample.preserve {
				if preserved != original {
					t.Fatal("live referenced snapshot was discarded")
				}
				if preserved.nextRetryAt != deadline || preserved.consecutiveFailures != 3 || preserved.generation != 3 {
					t.Error("unreadable reference changed existing retry or binding state")
				}
				if sample.otherWorkspace {
					if preserved.lastAccessAt != lastAccess {
						t.Error("lookup refreshed another workspace's access time")
					}
				} else if preserved.lastAccessAt.Before(before) {
					t.Error("referenced snapshot access was not recorded")
				}
			} else if preserved != nil {
				t.Error("uncached or retention-expired reference retained an entry")
			}
			if sample.otherWorkspace && service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")] != nil {
				t.Error("unreadable reference created a new current-workspace entry")
			}
		})
	}
}

func TestFailureFixStageDUnreadableReferenceDoesNotProtectAnotherWorkspaceUnlink(t *testing.T) {
	cache := &failureFixRecoverableSiteRead{err: errors.New("fixture cache unavailable")}
	reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}}}
	service := &Service{mySites: reader, sites: cache, multiplierSnapshots: map[string]*multiplierSnapshotEntry{}}
	for _, workspace := range []string{"ws1", "ws2"} {
		key := multiplierSnapshotKey("user1", workspace, "site-1")
		service.multiplierSnapshots[key] = &multiplierSnapshotEntry{workspaceKey: key, siteID: "site-1", userID: "user1", adminAccountID: workspace, lastAccessAt: time.Now()}
	}
	service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
	lookup := service.multiplierLookupForWorkspaceWithConnections(context.Background(), "user1", "ws2", string(upstream.PlatformSub2API), nil, true, false, false, false)
	if service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")] == nil {
		t.Error("another workspace's unlink discarded the unreadable reference")
	}
	if service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws2", "site-1")] != nil || lookup.unavailable {
		t.Error("read failure in another workspace protected an unlinked snapshot")
	}
	if direct, lists := reader.callCounts("site-1"); direct != 0 || lists != 0 {
		t.Error("read failure or unlink requested metadata")
	}
}

func TestFailureFixStageDRecoveryStillInvalidatesChangedBindingOrFingerprint(t *testing.T) {
	for _, change := range []string{"binding", "fingerprint"} {
		t.Run(change, func(t *testing.T) {
			site := snapshotSite("site-1")
			cache := &failureFixRecoverableSiteRead{site: site}
			reader := &snapshotMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
				directErrs:        map[string]error{"site-1": &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}},
			}
			service := &Service{mySites: reader, sites: cache}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if service.Shutdown(ctx) != nil {
					t.Error("refresh cleanup failed")
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			refresh := func(force bool) {
				service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
			}
			for i := range 3 {
				refresh(i != 0)
			}
			key := multiplierSnapshotKey("user1", "ws1", "site-1")
			original := service.multiplierSnapshots[key]
			deadline := original.nextRetryAt
			cache.set(nil, errors.New("fixture cache unavailable"))
			refresh(false)
			if preserved := service.multiplierSnapshots[key]; preserved != original || preserved.nextRetryAt != deadline || preserved.consecutiveFailures != 3 {
				t.Fatal("read failure changed existing snapshot state")
			}
			changedSite := *site
			if change == "binding" {
				reader.fakeMySitesReader.connections = []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-2")}
			} else {
				changedSite.Status = upstream.StatusError
			}
			cache.set(&changedSite, nil)
			direct, lists := reader.callCounts("site-1")
			refresh(false)
			replacement := service.multiplierSnapshots[key]
			if replacement == original || replacement.generation != original.generation+1 || replacement.consecutiveFailures != 1 {
				t.Error("changed binding or fingerprint retained obsolete retry history")
			}
			if d, l := reader.callCounts("site-1"); d != direct+1 || l != lists {
				t.Error("recovery did not refresh changed binding or fingerprint once")
			}
			failureFixRetryInterval(t, replacement, 5*time.Minute)
		})
	}
}

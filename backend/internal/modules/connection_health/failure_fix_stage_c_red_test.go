package connection_health

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func failureFixRetryInterval(t *testing.T, entry *multiplierSnapshotEntry, want time.Duration) {
	t.Helper()
	actual := time.Until(entry.nextRetryAt)
	if actual > want || actual < want-time.Second {
		t.Errorf("retry interval=%v, want %v", actual, want)
	}
}

func TestFailureFixStageCMultiplierAllFailureBranchesUseIncreasingBackoff(t *testing.T) {
	for _, branch := range []string{"request_failure", "per_key_failure", "retained_stale", "partial"} {
		t.Run(branch, func(t *testing.T) {
			entry := &multiplierSnapshotEntry{workspaceKey: "fixture", siteID: "fixture-site", keys: map[string]upstreamKeyMetadata{}}
			if branch == "retained_stale" {
				entry.keys["old"] = upstreamKeyMetadata{id: "old"}
			}
			service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{"fixture": entry}}
			for _, want := range []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
				captured := *entry
				var err error
				var failures map[string]string
				keys := map[string]upstreamKeyMetadata{}
				if branch == "request_failure" {
					err = &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}
				} else {
					failures = map[string]string{"key": "auth_failed"}
				}
				if branch == "partial" {
					keys["good"] = upstreamKeyMetadata{id: "good"}
				}
				service.finishMultiplierSnapshotLocked(entry, &captured, keys, failures, multiplierSiteMetadata{}, err)
				failureFixRetryInterval(t, entry, want)
			}
			captured := *entry
			service.finishMultiplierSnapshotLocked(entry, &captured, map[string]upstreamKeyMetadata{}, nil, multiplierSiteMetadata{}, nil)
			if !entry.nextRetryAt.IsZero() {
				t.Error("successful refresh did not clear backoff")
			}
			captured = *entry
			service.finishMultiplierSnapshotLocked(entry, &captured, nil, nil, multiplierSiteMetadata{}, &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401})
			failureFixRetryInterval(t, entry, 5*time.Minute)
		})
	}
}

func TestFailureFixStageCMultiplierRefreshHonorsBackoffManualBypassAndReset(t *testing.T) {
	reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}}, directErrs: map[string]error{"site-1": &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}}}
	site := snapshotSite("site-1")
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": site}}}
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
	entry := func() *multiplierSnapshotEntry {
		return service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")]
	}
	refresh(false)
	failureFixRetryInterval(t, entry(), 5*time.Minute)
	firstDirect, firstList := reader.callCounts("site-1")
	refresh(false)
	if d, l := reader.callCounts("site-1"); d != firstDirect || l != firstList {
		t.Error("automatic refresh retried inside backoff")
	}
	refresh(true)
	failureFixRetryInterval(t, entry(), 15*time.Minute)
	if d, _ := reader.callCounts("site-1"); d <= firstDirect {
		t.Error("manual refresh did not bypass backoff")
	}
	refresh(true)
	failureFixRetryInterval(t, entry(), 30*time.Minute)
	refresh(true)
	failureFixRetryInterval(t, entry(), 30*time.Minute)
	old := entry()
	site.Status = upstream.StatusError
	refresh(false)
	if entry() == old {
		t.Error("site fingerprint change did not replace snapshot")
	}
	failureFixRetryInterval(t, entry(), 5*time.Minute)
	old = entry()
	reader.fakeMySitesReader.connections = []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-2")}
	refresh(false)
	if entry() == old {
		t.Error("binding change did not replace snapshot")
	}
	failureFixRetryInterval(t, entry(), 5*time.Minute)
	reader.mu.Lock()
	delete(reader.directErrs, "site-1")
	reader.directItems = map[string]upstream.Sub2APIKeyItem{"site-1|key-2": {ID: "key-2", GroupID: "group-1", GroupName: "vip"}}
	reader.mu.Unlock()
	refresh(true)
	if !entry().nextRetryAt.IsZero() {
		t.Error("successful upstream read did not reset retry")
	}
	reader.mu.Lock()
	reader.directErrs["site-1"] = &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}
	reader.mu.Unlock()
	refresh(true)
	failureFixRetryInterval(t, entry(), 5*time.Minute)
}

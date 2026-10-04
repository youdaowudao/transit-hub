package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type failureFixRecoverableSiteRead struct {
	mu   sync.Mutex
	site *upstream.Site
	err  error
}

func (r *failureFixRecoverableSiteRead) GetSite(context.Context, string) (*upstream.Site, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.site, r.err
}

func (r *failureFixRecoverableSiteRead) set(site *upstream.Site, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.site, r.err = site, err
}

func TestFailureFixStageDReadFailurePreservesExistingBackoffAndRecovery(t *testing.T) {
	for _, scenario := range []string{"nil_with_error", "partial_with_error", "confirmed_deleted", "unlinked"} {
		t.Run(scenario, func(t *testing.T) {
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
			refresh := func(force bool) upstreamMultiplierLookup {
				return service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
			}
			entry := func() *multiplierSnapshotEntry {
				service.multiplierSnapshotMu.Lock()
				defer service.multiplierSnapshotMu.Unlock()
				return service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")]
			}
			refresh(false)
			refresh(true)
			refresh(true)
			original := entry()
			failureFixRetryInterval(t, original, 30*time.Minute)
			deadline := original.nextRetryAt
			direct, lists := reader.callCounts("site-1")
			switch scenario {
			case "nil_with_error":
				cache.set(nil, errors.New("fixture cache unavailable"))
			case "partial_with_error":
				cache.set(site, errors.New("fixture cache unavailable"))
			case "confirmed_deleted":
				cache.set(nil, nil)
			case "unlinked":
				reader.fakeMySitesReader.connections = nil
			}
			lookup := refresh(false)
			if d, l := reader.callCounts("site-1"); d != direct || l != lists {
				t.Error("unreadable, deleted or unlinked site requested metadata")
			}
			if scenario == "confirmed_deleted" || scenario == "unlinked" {
				if entry() != nil {
					t.Error("deleted or unlinked site retained an unused snapshot")
				}
				if lookup.unavailable {
					t.Error("deleted or unlinked site made lookup unavailable")
				}
				if scenario == "confirmed_deleted" && lookup.byAccount["account-1"].status != MultiplierResolutionMissing {
					t.Error("deleted binding lost missing explanation")
				}
				return
			}
			if !lookup.unavailable || lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
				t.Error("read failure was hidden")
			}
			if preserved := entry(); preserved != original || preserved.nextRetryAt != deadline || preserved.consecutiveFailures != 3 {
				t.Error("temporary read failure discarded an existing backoff")
			}
			cache.set(site, nil)
			refresh(false)
			if d, l := reader.callCounts("site-1"); d != direct || l != lists {
				t.Error("recovery retried before the existing deadline")
			}
			if restored := entry(); restored != original || restored.nextRetryAt != deadline || restored.consecutiveFailures != 3 {
				t.Error("recovery reset unchanged binding backoff")
			}
			refresh(true)
			if d, _ := reader.callCounts("site-1"); d <= direct {
				t.Error("manual refresh did not bypass preserved backoff")
			}
			failureFixRetryInterval(t, entry(), 30*time.Minute)
		})
	}
}

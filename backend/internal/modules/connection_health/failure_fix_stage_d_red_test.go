package connection_health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type failureFixSiteRead struct {
	site *upstream.Site
	err  error
}

type failureFixTransientSiteRead struct {
	calls   atomic.Int64
	partial bool
}

func (r *failureFixTransientSiteRead) GetSite(context.Context, string) (*upstream.Site, error) {
	if r.calls.Add(1) == 1 {
		if r.partial {
			return snapshotSite("site-1"), errors.New("fixture cache unavailable")
		}
		return nil, errors.New("fixture cache unavailable")
	}
	return snapshotSite("site-1"), nil
}

func TestFailureFixStageDInitialReadFailureCannotBecomeMissingOrResolved(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "nil_site_and_error"
		if partial {
			name = "partial_site_and_error"
		}
		t.Run(name, func(t *testing.T) {
			metadata := &snapshotMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
				directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
			}
			service := &Service{mySites: metadata, sites: &failureFixTransientSiteRead{partial: partial}}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if service.Shutdown(ctx) != nil {
					t.Error("refresh cleanup failed")
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			lookup := service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
			if !lookup.unavailable || lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
				t.Error("later successful read erased the initial read error")
			}
		})
	}
}

func (r failureFixSiteRead) GetSite(context.Context, string) (*upstream.Site, error) {
	return r.site, r.err
}

func TestFailureFixStageDDeletedSiteDoesNotEnterMultiplierRefreshRC2(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reader  failureFixSiteRead
		missing bool
	}{
		{"deleted", failureFixSiteRead{}, true},
		{"cache_read_failure", failureFixSiteRead{err: errors.New("fixture cache unavailable")}, false},
		{"present_without_session", failureFixSiteRead{site: &upstream.Site{ID: "site-1", Platform: upstream.PlatformSub2API}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}}}
			service := &Service{mySites: metadata, sites: tc.reader}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if service.Shutdown(ctx) != nil {
					t.Error("refresh cleanup failed")
				}
				waitForMultiplierRefreshDispatcherIdle(t)
			})
			lookup := service.multiplierLookupForWorkspaceWithOptions(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
			if tc.missing {
				if lookup.unavailable || lookup.byAccount["account-1"].status != MultiplierResolutionMissing {
					t.Error("deleted site still makes lookup unavailable instead of account missing")
				}
				if len(service.multiplierSnapshots) != 0 {
					t.Error("deleted site created a retry snapshot")
				}
			} else if !lookup.unavailable || lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
				t.Error("real read/session failure was mistaken for deleted site")
			}
			if d, l := metadata.callCounts("site-1"); d != 0 || l != 0 {
				t.Error("unreadable or deleted site caused an upstream metadata request")
			}
		})
	}
}

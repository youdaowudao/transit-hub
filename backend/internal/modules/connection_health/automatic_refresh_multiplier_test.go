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

type automaticRefreshConnectionsReader struct {
	*snapshotMetadataReader
	reads     atomic.Int32
	failFirst bool
}

func (r *automaticRefreshConnectionsReader) ListRealConnectionsForWorkspace(ctx context.Context, userID, workspace string) ([]my_sites.RealConnection, error) {
	if r.reads.Add(1) == 1 && r.failFirst {
		return nil, errors.New("first connection read failed")
	}
	return r.snapshotMetadataReader.ListRealConnectionsForWorkspace(ctx, userID, workspace)
}

func TestRefreshRunAutomaticReusesConnectionsAndReturnsBeforeMultiplierReader(t *testing.T) {
	for _, test := range []struct {
		name        string
		failFirst   bool
		wantReads   int32
		wantSyncKey string
	}{
		{name: "successful discovery", wantReads: 1},
		{name: "first read fails", failFirst: true, wantReads: 2, wantSyncKey: "site_sync_connections"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &automaticRefreshConnectionsReader{
				failFirst: test.failFirst,
				snapshotMetadataReader: &snapshotMetadataReader{
					fakeMySitesReader: fakeMySitesReader{
						connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
						session:     upstream.Session{Platform: upstream.PlatformSub2API},
					},
					directItems: map[string]upstream.Sub2APIKeyItem{
						"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
					},
					started: make(chan string, 1),
					release: make(chan struct{}),
				},
			}
			platform := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}}
			service := newAdminGroupsService(platform, reader, newFakeRepository())
			service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
			syncer := &recordingUpstreamSyncCoordinator{}
			service.SetUpstreamSyncCoordinator(syncer)
			registerSnapshotReaderCleanup(t, service, reader.release)

			run, _, err := service.startOrJoinAdminGroupsRefreshRun(t.Context(), "user1", adminGroupsRefreshModeAutomatic)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case site := <-reader.started:
				if site != "site-1" {
					t.Fatalf("multiplier read site = %q", site)
				}
			case <-time.After(time.Second):
				t.Fatal("automatic refresh did not schedule the multiplier")
			}
			snapshot, ok := waitForRefreshRunSnapshot(run, time.Second, func(s adminGroupsRefreshSnapshot) bool { return s.Terminal != nil })
			if !ok {
				t.Fatal("automatic refresh waited for the blocked multiplier reader")
			}
			terminal := snapshot.Terminal
			if terminal.Status != "success" || terminal.Groups == nil || len(*terminal.Groups) != 1 {
				t.Fatalf("automatic terminal before multiplier read = %+v", terminal)
			}
			if terminal.Refresh.ErrorKey != test.wantSyncKey || reader.reads.Load() != test.wantReads {
				t.Fatalf("automatic result = %+v, connection reads = %d", terminal.Refresh, reader.reads.Load())
			}
			if test.failFirst {
				if len(syncer.calls) != 0 || len(terminal.Refresh.Sites) != 0 {
					t.Fatalf("failed discovery should not report a multiplier or site sync as completed: %+v", terminal.Refresh)
				}
			} else if len(syncer.calls) != 1 || syncer.calls[0].force || len(terminal.Refresh.Sites) != 1 || terminal.Refresh.Sites[0].Status != "success" {
				t.Fatalf("automatic refresh should contain only the existing site-sync result: %+v", terminal.Refresh)
			}
		})
	}
}

func TestMultiplierSnapshotKeepsSuccessfulValueForThirtyMinutes(t *testing.T) {
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	service := &Service{
		mySites: reader,
		sites:   snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}},
		multiplierNow: func() time.Time {
			return time.Unix(0, clock.Load())
		},
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
		waitForMultiplierRefreshDispatcherIdle(t)
	})

	read := func() upstreamMultiplierResolution {
		return service.multiplierLookupForWorkspace(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true).byAccount["account-1"]
	}
	if initial := read(); initial.status != MultiplierResolutionResolved {
		t.Fatalf("first multiplier = %+v", initial)
	}
	clock.Add(int64(29 * time.Minute))
	if reused := read(); reused.status != MultiplierResolutionResolved {
		t.Fatalf("multiplier at 29 minutes = %+v", reused)
	}
	if calls, _ := reader.callCounts("site-1"); calls != 1 {
		t.Fatalf("29-minute snapshot caused %d upstream reads, want 1", calls)
	}
	clock.Add(int64(time.Minute))
	if updated := read(); updated.status != MultiplierResolutionResolved {
		t.Fatalf("multiplier at 30 minutes = %+v", updated)
	}
	if calls, _ := reader.callCounts("site-1"); calls != 2 {
		t.Fatalf("expired snapshot caused %d upstream reads, want 2", calls)
	}
}

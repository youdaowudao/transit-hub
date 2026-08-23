package connection_health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type timeoutMultiplierReader struct {
	fakeMySitesReader
	calls atomic.Int32
	block bool
}

type timeoutBeforeUpstreamSiteLookup struct{}

func (timeoutBeforeUpstreamSiteLookup) GetSite(ctx context.Context, _ string) (*upstream.Site, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type panicGenerationMultiplierReader struct {
	fakeMySitesReader
	mu       sync.Mutex
	started  chan string
	releases map[string]chan struct{}
	items    map[string]upstream.Sub2APIKeyItem
}

func (r *panicGenerationMultiplierReader) GetUpstreamKeyForWorkspace(_ context.Context, _, _, _, keyID string) (upstream.Sub2APIKeyItem, error) {
	r.started <- keyID
	<-r.releases[keyID]
	if keyID == "key-panic" {
		panic("test generation panic")
	}
	return r.items[keyID], nil
}

func (*panicGenerationMultiplierReader) ListUpstreamKeysForWorkspace(context.Context, string, string, string) ([]upstream.Sub2APIKeyItem, error) {
	return nil, nil
}

func (r *panicGenerationMultiplierReader) replaceConnections(connections []my_sites.RealConnection) {
	r.mu.Lock()
	r.fakeMySitesReader.connections = connections
	r.mu.Unlock()
}

func (r *panicGenerationMultiplierReader) ListRealConnectionsForWorkspace(ctx context.Context, userID string, adminAccountID string) ([]my_sites.RealConnection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakeMySitesReader.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
}

func (r *timeoutMultiplierReader) GetUpstreamKeyForWorkspace(ctx context.Context, _, _, _, keyID string) (upstream.Sub2APIKeyItem, error) {
	r.calls.Add(1)
	if r.block {
		<-ctx.Done()
		return upstream.Sub2APIKeyItem{}, ctx.Err()
	}
	return upstream.Sub2APIKeyItem{ID: keyID, GroupID: "group-1", GroupName: "vip"}, nil
}

func (*timeoutMultiplierReader) ListUpstreamKeysForWorkspace(context.Context, string, string, string) ([]upstream.Sub2APIKeyItem, error) {
	return nil, nil
}

func newTimeoutMultiplierEntry() (*Service, *multiplierSnapshotEntry, *multiplierSnapshotEntry) {
	key := multiplierSnapshotKey("user1", "ws1", "site-1")
	target := &multiplierSnapshotEntry{
		workspaceKey: key, siteID: "site-1", userID: "user1", adminAccountID: "ws1",
		platform: upstream.PlatformSub2API, bindingSignature: "key-1", keyIDs: []string{"key-1"},
		generation: 1, capability: multiplierDirectUnknown, status: multiplierResolutionUpdating,
		inFlight: true, done: make(chan struct{}),
	}
	captured := *target
	captured.keyIDs = append([]string(nil), target.keyIDs...)
	service := &Service{
		sites:               snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}},
		multiplierSnapshots: map[string]*multiplierSnapshotEntry{key: target},
	}
	return service, target, &captured
}

func mergedMultiplierErrorKey(service *Service) string {
	return mergeAdminGroupsRefreshSummary(nil, service.multiplierRefreshSummary("user1", "ws1"), "").Sites[0].ErrorKey
}

func TestMultiplierQueueBudgetExpiredBeforeWorkerSkipsUpstreamRequest(t *testing.T) {
	service, target, captured := newTimeoutMultiplierEntry()
	waiter := target.done
	reader := &timeoutMultiplierReader{}
	captured.lastAccessAt = time.Now().Add(-multiplierRefreshTimeout - time.Second)

	service.refreshMultiplierSnapshot(context.Background(), reader, captured, target)

	if calls := reader.calls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want 0 after queue budget expired", calls)
	}
	if got := mergedMultiplierErrorKey(service); got != "multiplier_queue_timeout" {
		t.Fatalf("queue timeout errorKey = %q, want multiplier_queue_timeout", got)
	}
	select {
	case <-waiter:
	default:
		t.Fatal("queue timeout did not release the generation waiter")
	}
}

func TestMultiplierRequestTimeoutAfterUpstreamStartsUsesDistinctError(t *testing.T) {
	service, target, captured := newTimeoutMultiplierEntry()
	waiter := target.done
	reader := &timeoutMultiplierReader{block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	service.refreshMultiplierSnapshot(ctx, reader, captured, target)

	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("upstream calls = %d, want one started request", calls)
	}
	if got := mergedMultiplierErrorKey(service); got != "multiplier_request_timeout" {
		t.Fatalf("request timeout errorKey = %q, want multiplier_request_timeout", got)
	}
	select {
	case <-waiter:
	default:
		t.Fatal("request timeout did not release the generation waiter")
	}
}

func TestMultiplierBudgetExpiresDuringLocalSiteLookupWithoutStartingUpstreamUsesQueueTimeout(t *testing.T) {
	service, target, captured := newTimeoutMultiplierEntry()
	service.sites = timeoutBeforeUpstreamSiteLookup{}
	waiter := target.done
	reader := &timeoutMultiplierReader{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	service.refreshMultiplierSnapshot(ctx, reader, captured, target)

	if calls := reader.calls.Load(); calls != 0 {
		t.Fatalf("upstream calls = %d, want 0 when local site lookup consumes the budget", calls)
	}
	if got := mergedMultiplierErrorKey(service); got != "multiplier_queue_timeout" {
		t.Fatalf("pre-request timeout errorKey = %q, want multiplier_queue_timeout", got)
	}
	select {
	case <-waiter:
	default:
		t.Fatal("pre-request timeout did not release the generation waiter")
	}
}

func TestMultiplierGenerationReplacementTransfersWaitersAcrossWorkerInterleavings(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		oldResult      error
		finishOldFirst bool
	}{
		{name: "old worker finishes first", finishOldFirst: true},
		{name: "new worker finishes first", finishOldFirst: false},
		{name: "old worker panic terminal", oldResult: errors.New("multiplier snapshot refresh panic"), finishOldFirst: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			key := multiplierSnapshotKey("user1", "ws1", "site-1")
			oldDone := make(chan struct{})
			newDone := make(chan struct{})
			oldEntry := &multiplierSnapshotEntry{workspaceKey: key, bindingSignature: "key-1", generation: 1, inFlight: true, done: oldDone}
			newEntry := &multiplierSnapshotEntry{
				workspaceKey: key, bindingSignature: "key-2", generation: 2, inFlight: true, done: newDone,
				supersededDone: []chan struct{}{oldDone},
			}
			service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{key: newEntry}}
			oldCaptured, newCaptured := *oldEntry, *newEntry

			finishOld := func() {
				service.finishMultiplierSnapshot(oldEntry, &oldCaptured, nil, nil, multiplierSiteMetadata{}, testCase.oldResult)
			}
			finishNew := func() {
				service.finishMultiplierSnapshot(newEntry, &newCaptured, map[string]upstreamKeyMetadata{}, nil, multiplierSiteMetadata{}, nil)
			}
			if testCase.finishOldFirst {
				finishOld()
				for _, waiter := range []<-chan struct{}{oldDone, newDone} {
					select {
					case <-waiter:
						t.Fatal("old generation closed a waiter owned by the replacement generation")
					default:
					}
				}
				finishNew()
			} else {
				finishNew()
				finishOld()
			}
			for _, waiter := range []<-chan struct{}{oldDone, newDone} {
				select {
				case <-waiter:
				case <-time.After(time.Second):
					t.Fatal("replacement terminal did not release every transferred waiter")
				}
			}
		})
	}
}

func TestMultiplierSiteDeletionWithoutSuccessorReleasesCurrentWaiters(t *testing.T) {
	release := make(chan struct{})
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
		started:           make(chan string, 1), release: release,
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}
	var closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(release) }) })

	waiterDone := make(chan struct{})
	go func() {
		service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		close(waiterDone)
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("multiplier generation did not start")
	}

	service.prepareMultiplierSnapshots(context.Background(), reader, "user1", "ws1", upstream.PlatformSub2API, map[string]map[string]struct{}{}, false, false)
	select {
	case <-waiterDone:
	case <-time.After(100 * time.Millisecond):
		closeOnce.Do(func() { close(release) })
		t.Fatal("site deletion without a successor left the current waiter blocked")
	}
}

func TestMultiplierOldWorkerPanicCannotCloseTransferredWaiters(t *testing.T) {
	panicRelease := make(chan struct{})
	replacementRelease := make(chan struct{})
	reader := &panicGenerationMultiplierReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-panic")}},
		started:           make(chan string, 2),
		releases: map[string]chan struct{}{
			"key-panic":       panicRelease,
			"key-replacement": replacementRelease,
		},
		items: map[string]upstream.Sub2APIKeyItem{
			"key-replacement": {ID: "key-replacement", GroupID: "group-1", GroupName: "vip"},
		},
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}
	var panicCloseOnce, replacementCloseOnce sync.Once
	t.Cleanup(func() {
		panicCloseOnce.Do(func() { close(panicRelease) })
		replacementCloseOnce.Do(func() { close(replacementRelease) })
	})

	oldWaiter := make(chan struct{})
	go func() {
		service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		close(oldWaiter)
	}()
	select {
	case keyID := <-reader.started:
		if keyID != "key-panic" {
			t.Fatalf("old generation key = %q, want key-panic", keyID)
		}
	case <-time.After(time.Second):
		t.Fatal("old panic generation did not start")
	}

	reader.replaceConnections([]my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-replacement")})
	newWaiter := make(chan struct{})
	go func() {
		service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		close(newWaiter)
	}()
	select {
	case keyID := <-reader.started:
		if keyID != "key-replacement" {
			t.Fatalf("replacement generation key = %q, want key-replacement", keyID)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement generation did not start")
	}

	panicCloseOnce.Do(func() { close(panicRelease) })
	time.Sleep(25 * time.Millisecond)
	for _, waiter := range []<-chan struct{}{oldWaiter, newWaiter} {
		select {
		case <-waiter:
			t.Fatal("panicking old worker closed a waiter transferred to the replacement generation")
		default:
		}
	}

	replacementCloseOnce.Do(func() { close(replacementRelease) })
	for _, waiter := range []<-chan struct{}{oldWaiter, newWaiter} {
		select {
		case <-waiter:
		case <-time.After(time.Second):
			t.Fatal("replacement terminal did not release transferred waiters after old worker panic")
		}
	}
}

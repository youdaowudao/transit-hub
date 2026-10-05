package connection_health

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestActionCheckpointCleanupConcurrentEmptyGroupRestoration(t *testing.T) {
	s, repo, lister, now, _ := actionCleanupHarness()
	s.repo = actionCleanupConcurrentRepo{repo}
	platform := &fakePlatformActioner{}
	s.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
	visible := actionCleanupPair("1", *now).Target
	actionCleanupStore(repo, RemoteActionCheckpoints{Target: visible})
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	inventory := &adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, snapshotStartedAt: *now,
		groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g1", Name: "zero"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "1", Name: "分组账号", Status: "inactive"}}}},
	}
	cache := adminInventoryCache{"u|w": {inventory: inventory}}
	lister.entered, lister.release = make(chan struct{}, 1), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { close(lister.release); s.actionDispatchWG.Wait() }()
	s.startDeletedAccountSweeps(ctx, cache)
	actionCleanupWait(t, lister.entered)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for n := 0; n < 100; n++ {
			if _, err := s.PrioritySyncStatus(ctx, "u"); err != nil {
				t.Error(err)
			}
		}
	}()
	s.restoreEmptySub2APIGroups(ctx, []TargetActionState{*visible}, cache)
	readers.Wait()
	if got := inventory.groups[0].accounts[0].Status; got != "active" {
		t.Fatalf("actual safeguard did not mutate shared tick inventory: %s", got)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "1" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("safeguard dispatch=%+v", platform.sub2APICalls)
	}
	if lister.calls.Load() != 1 {
		t.Fatal("status or safeguard caused an additional full account read")
	}
}

type actionCleanupLockWaitRepo struct {
	*fakeRepository
	entered chan struct{}
}

func (r actionCleanupLockWaitRepo) ClearDeletedAccountCheckpoint(ctx context.Context, scope RemoteActionScope, startedAt, now time.Time) (bool, error) {
	r.entered <- struct{}{}
	return r.fakeRepository.ClearDeletedAccountCheckpoint(ctx, scope, startedAt, now)
}

func TestActionCheckpointCleanupReentryWhileWaitingForRowLock(t *testing.T) {
	s, repo, lister, now, inventory := actionCleanupHarness()
	pair := actionCleanupPair("2", *now)
	actionCleanupStore(repo, pair)
	before := cloneActionPair(pair)
	entered := make(chan struct{}, 1)
	s.repo = actionCleanupLockWaitRepo{fakeRepository: repo, entered: entered}
	// Model the existing workspace/row lock waiting after successful list evidence.
	repo.actionMu.Lock()
	locked := true
	defer func() {
		if locked {
			repo.actionMu.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", inventory)
	}()
	actionCleanupWait(t, entered)
	actionCleanupRemember(s, now.Add(time.Second), true, upstream.AdminGroupAccountInfo{ID: "2", Name: "锁等待期间回组"})
	repo.actionMu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish after lock release")
	}
	if lister.calls.Load() != 1 || !reflect.DeepEqual(before, repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"})) {
		t.Fatal("lock-time visibility check failed to preserve the original pair")
	}
	if view := actionCleanupView(t, s); view.departed["2"] {
		t.Fatal("reentered account retained departed conclusion")
	}
}

func TestActionDiagnosticsCanonicalAccountDeduplication(t *testing.T) {
	now := actionCleanupNow()
	repo := newFakeRepository()
	priority := actionCleanupPair(" 1 ", now)
	priority.Target = nil
	actionCleanupPending(&priority, ActionKindPriority, DispatchUncertain, now)
	actionCleanupStore(repo, priority)
	target := actionCleanupPair("1", now)
	target.Priority = nil
	actionCleanupPending(&target, ActionKindTarget, DispatchSending, now)
	actionCleanupStore(repo, target)
	s := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, actionNow: func() time.Time { return now }}
	actionCleanupRemember(s, now, true, upstream.AdminGroupAccountInfo{ID: " 1 ", Name: "同一账号"})
	status, err := s.PrioritySyncStatus(context.Background(), "u")
	if err != nil || len(status.ActionDiagnostics) != 1 || status.ActionDiagnostics[0].AccountID != "1" || status.ActionDiagnostics[0].Reason != "uncertain" || status.ActionDiagnostics[0].AccountName != "同一账号" {
		t.Fatalf("canonical account projection=%+v err=%v", status, err)
	}
}

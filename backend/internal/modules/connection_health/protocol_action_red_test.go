package connection_health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// T22: a Priority pending claim occupies the target even though the target
// action uses a different runtime lease. The forbidden side must send zero HTTP.
func TestProtocolContractPriorityClaimBlocksTargetAction(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	target.InventoryComplete = true
	target.TestConfiguration = defaultTestConfiguration()
	target.TestMemberships = []TestConfigurationSource{{AdminGroupID: "g1"}}
	pending := 1001
	repo.priorityStates["user1|ws1|"+target.TargetID] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: target.TargetID, OriginalPriority: 1000, LastAppliedPriority: 1000, PendingPriority: &pending}
	inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1", Name: "fixture"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}}})
	_, _ = service.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", inventory.session, target, []probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory))
	if len(platform.sub2APICalls) != 0 {
		t.Errorf("Priority claim bypassed: forbidden target-action HTTP=%d, want 0", len(platform.sub2APICalls))
	}
	if state := repo.priorityStates["user1|ws1|"+target.TargetID]; state.PendingPriority == nil {
		t.Error("existing other-table claim was erased")
	}
}

type protocolPausedCheckpointRepository struct {
	*fakeRepository
	prepared chan RemoteActionClaim
	resume   chan struct{}
	paused   atomic.Bool
}

func (r *protocolPausedCheckpointRepository) AcquireSub2APIMutationLease(context.Context, string, string) (func(), error) {
	return func() {}, nil
}
func (r *protocolPausedCheckpointRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	allowed, err := r.fakeRepository.ClaimRemoteAction(ctx, claim)
	if allowed && err == nil && r.paused.CompareAndSwap(false, true) {
		r.prepared <- claim
		select {
		case <-r.resume:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return allowed, err
}

// T22 replays the RED persist-pending -> send gap at the new Claim boundary.
// Expiry is explicit while A and its Lost observer remain paused.
func TestProtocolContractPausedWorkerCannotSendAfterTakeover(t *testing.T) {
	repo := &protocolPausedCheckpointRepository{fakeRepository: newFakeRepository(), prepared: make(chan RemoteActionClaim, 1), resume: make(chan struct{})}
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo.fakeRepository, "acc-1")
	target.InventoryComplete = true
	target.TestConfiguration = defaultTestConfiguration()
	target.TestMemberships = []TestConfigurationSource{{AdminGroupID: "g1"}}
	inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}}})
	call := func() {
		_, _ = service.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", inventory.session, target, []probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory))
	}
	done := make(chan struct{})
	go func() { defer close(done); call() }()
	var original RemoteActionClaim
	select {
	case original = <-repo.prepared:
	case <-time.After(3 * time.Second):
		close(repo.resume)
		t.Fatal("A never reached the prepared barrier")
	}
	repo.expireActionLeaseForTest(original.OwnerID, false)
	call()
	if len(platform.sub2APICalls) != 1 {
		t.Errorf("takeover must be the only sender before A resumes, calls=%d", len(platform.sub2APICalls))
	}
	close(repo.resume)
	<-done
	if len(platform.sub2APICalls) != 1 {
		t.Errorf("paused stale worker sent after takeover: total HTTP=%d, want B=1 A=0", len(platform.sub2APICalls))
	}
}

// T25 explicitly replaces the old blind restore behavior for invisible targets.
func TestProtocolContractInvisibleTargetRetainsCheckpoint(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, platformGroups: fakePlatformGroupReader{}, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	const target = "sub2api:ws1:acc-1"
	stored := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: target, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	repo.targetActionStates["user1|ws1|"+target] = stored
	service.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 0 {
		t.Errorf("invisible target was blindly restored: HTTP=%d", len(platform.sub2APICalls))
	}
	if _, ok := repo.targetActionStates["user1|ws1|"+target]; !ok {
		t.Error("invisible target checkpoint deleted without current-value evidence")
	}
}

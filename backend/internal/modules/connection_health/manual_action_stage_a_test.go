package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestStageAPersistentPendingFreezesClaimAndCurrentGroups(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repo: repo}
	inventory := sub2APITestInventory(
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "old"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "2", Status: "active"}}},
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "current"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "1", Status: "active"}, {ID: "3", Status: "active"}}},
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "other"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "4", Status: "active"}, {ID: "5", Status: "active"}}},
	)
	repo.targetActionStates["u|w|sub2api:w:1"] = TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingActionKind: TargetMutationDelete, PendingSource: ActionSourceManualDelete, PendingGroupIDs: []string{"old"}, PendingDispatchID: "persistent", PendingOwnerID: "old-process", PendingDispatchPhase: DispatchUncertain}
	guard := newWorkspaceFloorGuard()
	if err := service.overlayPersistentPending(t.Context(), "u", "w", guard, *inventory); err != nil {
		t.Fatal(err)
	}
	if guard.freezeWorkspace {
		t.Fatal("complete current membership froze unrelated workspace")
	}
	for _, id := range []string{"old", "current"} {
		if _, ok := guard.frozenGroups[id]; !ok {
			t.Fatalf("group %s not frozen", id)
		}
	}
	if _, ok := guard.frozenGroups["other"]; ok {
		t.Fatal("unrelated group frozen")
	}
	// Reading an equal value or an expired owner does not release uncertainty.
	pair, err := service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: RemoteActionScope{"u", "w", "sub2api:w:1"}, InventoryComplete: true, Visible: true, Status: "active", SnapshotStartedAt: time.Now()})
	if err != nil || pair.pendingCount() != 1 {
		t.Fatal("persistent pending disappeared")
	}
	inventory.groups[1].accounts = inventory.groups[1].accounts[1:]
	if err := service.overlayPersistentPending(t.Context(), "u", "w", guard, *inventory); err != nil {
		t.Fatal(err)
	}
	if guard.freezeWorkspace {
		t.Fatal("complete inventory with claimed groups froze unrelated workspace")
	}
	if _, ok := guard.frozenGroups["old"]; !ok {
		t.Fatal("absent account lost claim group protection")
	}
	if _, ok := guard.frozenGroups["other"]; ok {
		t.Fatal("absent account froze unrelated group")
	}
	other, _ := findActionInventoryTarget("sub2api:ws1:4", *inventory)
	if result := guard.reserveSub2APISchedulableFalse(other, *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != "" {
		t.Fatalf("unrelated group closure blocked by absent claimed account: %s", result.remoteAction)
	}
	claimed, _ := findActionInventoryTarget("sub2api:ws1:2", *inventory)
	if result := guard.reserveSub2APISchedulableFalse(claimed, *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != RemoteActionAwaitingConfirmation {
		t.Fatalf("claim group closure escaped persistent protection: %s", result.remoteAction)
	}
	pair, err = service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: RemoteActionScope{"u", "w", "sub2api:w:1"}, InventoryComplete: true, Visible: false, SnapshotStartedAt: time.Now()})
	if err != nil || pair.pendingCount() != 1 {
		t.Fatal("absence without confirmed deletion released uncertainty")
	}
}

func TestStageAManualClaimIgnoresBrowserCancellationAndPreservesBaseline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actioner := &fakeTargetSchedulableActioner{}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
	actioner.afterWrite = func(bool) { cancel() }
	// Keep the existing write callback to update the fake source while also
	// disconnecting the caller immediately after a request reaches the endpoint.
	reader := service.platformGroups.(fakePlatformGroupReader)
	actioner.afterWrite = func(value bool) {
		list := reader.accountsByGrp["g1"]
		list[0].Schedulable = boolPointer(value)
		reader.accountsByGrp["g1"] = list
		cancel()
	}
	before := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1515", OriginalStatus: "active", LastAppliedStatus: "inactive", OriginalWeight: intPointer(71), LastAppliedWeight: intPointer(9), Conflict: true}
	repo.targetActionStates["user1|ws1|sub2api:ws1:1515"] = before
	result, err := service.SetTargetSchedulable(ctx, "user1", "sub2api:ws1:1515", false)
	if err != nil || result.Schedulable || actioner.calls != 1 {
		t.Fatalf("browser disconnect abandoned permitted command: %v", err)
	}
	after := repo.targetActionStates["user1|ws1|sub2api:ws1:1515"]
	if targetActionPending(&after) || after.OriginalStatus != before.OriginalStatus || after.LastAppliedStatus != before.LastAppliedStatus || *after.OriginalWeight != 71 || *after.LastAppliedWeight != 9 || !after.Conflict {
		t.Fatal("manual switch rewrote baseline")
	}
}

func TestStageAMutationLeaseLossCancelsSending(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repo: repo}
	ctx, release, err := service.prepareManualAction(t.Context(), "u", "w", "sub2api:w:1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mutation := mutationLeaseFromContext(ctx)
	started := make(chan struct{})
	done := make(chan error, 1)
	state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingStatus: "inactive", PendingSource: ActionSourceManual, PendingActionKind: TargetMutationStatus}
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", "sub2api:w:1"}, Kind: ActionKindTarget, Target: &state}
	go func() {
		_, err := service.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
			close(started)
			<-sendCtx.Done()
			return "failed", sendCtx.Err()
		})
		done <- err
	}()
	<-started
	repo.expireActionLeaseForTest(mutation.OwnerID, true)
	if err := <-done; err == nil {
		t.Fatal("lost write lease did not cancel HTTP")
	}
	pair := repo.actionPair(claim.RemoteActionScope)
	if pair.pendingCount() != 1 || pair.Target.PendingDispatchPhase != DispatchUncertain {
		t.Fatal("lost write lease released uncertain claim")
	}
}

type stageAPermitBarrierRepo struct {
	*fakeRepository
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (r *stageAPermitBarrierRepo) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.proceed:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return r.fakeRepository.PermitRemoteAction(ctx, claim)
}

func TestStageAShutdownClosesAdmissionAndDrainsRegisteredPermit(t *testing.T) {
	repo := &stageAPermitBarrierRepo{fakeRepository: newFakeRepository(), started: make(chan struct{}), proceed: make(chan struct{})}
	service := &Service{repo: repo}
	sendStarted := make(chan struct{})
	finish := make(chan struct{})
	actionDone := make(chan error, 1)
	scope := RemoteActionScope{"u", "w", "sub2api:w:1"}
	claim := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindTarget, Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, PendingStatus: "inactive"}}
	go func() {
		_, err := service.dispatchRemoteAction(t.Context(), claim, func(ctx context.Context) (string, error) {
			close(sendStarted)
			select {
			case <-finish:
				return "applied", nil
			case <-ctx.Done():
				return "failed", ctx.Err()
			}
		})
		actionDone <- err
	}()
	<-repo.started
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.Shutdown(t.Context()) }()
	// The public admission flag is protected by the same lock used for register.
	for {
		service.actionDispatchMu.Lock()
		closed := service.actionDispatchClosed
		service.actionDispatchMu.Unlock()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	calls := 0
	_, err := service.dispatchRemoteAction(t.Context(), claim, func(context.Context) (string, error) { calls++; return "bad", nil })
	if err == nil || calls != 0 {
		t.Fatal("shutdown accepted a new command")
	}
	select {
	case <-shutdownDone:
		t.Fatal("shutdown missed registered permit")
	default:
	}
	close(repo.proceed)
	<-sendStarted
	select {
	case <-shutdownDone:
		t.Fatal("shutdown returned before permitted send receipt")
	default:
	}
	close(finish)
	if err := <-actionDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	pair := repo.actionPair(scope)
	if pair.Target.PendingDispatchPhase != DispatchConfirmedApplied {
		t.Fatal("drain omitted receipt")
	}
}

func TestStageAIncompleteIdempotentSuccessRetainsPendingAndBlocksReplay(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{}
	service, repo := schedulableActionServiceWithGroups([]upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}}, map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "1515", Status: "active", Schedulable: boolPointer(false)}}}, map[string]error{"g2": errors.New("unavailable")}, actioner)
	result, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1515", false)
	if err != nil || result.Schedulable || actioner.calls != 1 {
		t.Fatalf("known idempotent result lost old API success: %v", err)
	}
	pair := repo.actionPair(RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"})
	if pair.pendingCount() != 1 || pair.Target.PendingDispatchPhase != DispatchConfirmedApplied {
		t.Fatal("incomplete inventory prematurely released claim")
	}
	if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1515", true); err == nil || actioner.calls != 1 {
		t.Fatal("incomplete pending allowed opposite replay")
	}
}

type stageAWriteWaitRepository struct {
	*fakeRepository
	waiting chan struct{}
	once    sync.Once
}

func (r *stageAWriteWaitRepository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	if len(key) >= len("connection-health:sub2api-mutation:") && key[:len("connection-health:sub2api-mutation:")] == "connection-health:sub2api-mutation:" {
		r.once.Do(func() { close(r.waiting) })
	}
	return r.fakeRepository.AcquireActionLease(ctx, key, wait)
}

func TestStageALockWaitUsesFreshPolicyAndMembershipForAllClosures(t *testing.T) {
	for _, entry := range []string{"manual", "compatibility", "automatic"} {
		for _, change := range []string{"policy", "membership"} {
			t.Run(entry+"/"+change, func(t *testing.T) {
				actioner := &fakeTargetSchedulableActioner{}
				service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
				reader := service.platformGroups.(fakePlatformGroupReader)
				platform := &fakePlatformActioner{}
				service.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
				target := sub2APISuspendedTargetFixture(repo, "1515")
				if entry == "automatic" { // Match current core health evidence without replacing expectations.
					state := repo.states[target.TargetID]["model-a"]
					state.UserID = "user1"
					state.AdminAccountID = "ws1"
					state.HealthEvidenceStatus = HealthEvidenceValid
					protocol := TestProtocolChatCompletions
					state.HealthEvidenceProtocol = &protocol
					repo.states[target.TargetID]["model-a"] = state
				}
				wrapped := &stageAWriteWaitRepository{fakeRepository: repo, waiting: make(chan struct{})}
				service.repo = wrapped
				// The preceding action holds the same persistent write key. The new sender
				// must wait, then read policy and membership once that key is available.
				held, _, err := repo.AcquireActionLease(t.Context(), mutationRuntimeLeaseKey("user1", "ws1"), true)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
				done := make(chan error, 1)
				go func() {
					if entry == "manual" {
						_, err := service.SetTargetSchedulable(t.Context(), "user1", target.TargetID, false)
						done <- err
						return
					}
					if entry == "compatibility" {
						session, _ := service.mySites.RequireSession(t.Context(), "user1", "ws1")
						_, err := service.runCompatibilityStatusAction(t.Context(), "user1", "ws1", session, "1515", "inactive")
						done <- err
						return
					}
					old := sub2APITestInventory(adminInventoryGroup{group: reader.groups[0], accounts: append([]upstream.AdminGroupAccountInfo{}, reader.accountsByGrp["g1"]...)})
					result, err := service.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", old.session, target, []probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), old, fullFloorTestMonitoringScope(*old))
					if err == nil && result.remoteAction != RemoteActionSkippedSub2APILastActive {
						err = errors.New("automatic close did not apply new floor")
					}
					done <- err
				}()
				<-wrapped.waiting
				if change == "policy" {
					assignments := repo.assignments[:0]
					for _, a := range repo.assignments {
						if a.TargetID == target.TargetID {
							assignments = append(assignments, a)
						}
					}
					repo.assignments = assignments
				} else {
					reader.accountsByGrp["g1"] = reader.accountsByGrp["g1"][:1]
				}
				held.Release()
				err = <-done
				if entry != "automatic" && (err == nil || err.Error() != ErrorSub2APIGroupLastUsable) {
					t.Fatalf("lock wait used old %s snapshot: %v", change, err)
				}
				if entry == "automatic" && err != nil {
					t.Fatal(err)
				}
				if actioner.calls != 0 || len(platform.sub2APICalls) != 0 {
					t.Fatal("lock wait sent after final reserve disappeared")
				}
			})
		}
	}
}

func TestStageAAbsentPendingStillFreezesWhenGroupsUnknown(t *testing.T) {
	for _, sample := range []struct {
		name       string
		incomplete bool
		priority   bool
		groups     []string
	}{
		{name: "legacy-no-claim"},
		{name: "priority-no-claim", priority: true},
		{name: "incomplete-with-claim", incomplete: true, groups: []string{"old"}},
	} {
		t.Run(sample.name, func(t *testing.T) {
			repo := newFakeRepository()
			service := &Service{repo: repo}
			inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "other"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "4", Status: "active"}, {ID: "5", Status: "active"}}})
			if sample.incomplete {
				inventory.groupsComplete = false
			}
			if sample.priority {
				repo.priorityStates["u|w|sub2api:w:1"] = PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingDispatchID: "persistent", PendingOwnerID: "old-process", PendingDispatchPhase: DispatchUncertain}
			} else {
				repo.targetActionStates["u|w|sub2api:w:1"] = TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingActionKind: TargetMutationDelete, PendingSource: ActionSourceManualDelete, PendingGroupIDs: sample.groups, PendingDispatchID: "persistent", PendingOwnerID: "old-process", PendingDispatchPhase: DispatchUncertain}
			}
			guard := newWorkspaceFloorGuard()
			if err := service.overlayPersistentPending(t.Context(), "u", "w", guard, *inventory); err != nil || !guard.freezeWorkspace {
				t.Fatalf("unknown ownership did not retain workspace protection: %v", err)
			}
		})
	}
}

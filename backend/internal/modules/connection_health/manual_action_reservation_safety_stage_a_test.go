package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type stageAReservationFaultRepository struct {
	*fakeRepository
	receiptErr      error
	reconcileErr    error
	beforeReconcile func()
	afterReceipt    func()
}

func (r *stageAReservationFaultRepository) RecordRemoteActionReceipt(ctx context.Context, claim RemoteActionClaim, phase RemoteDispatchPhase) error {
	if r.receiptErr != nil {
		return r.receiptErr
	}
	err := r.fakeRepository.RecordRemoteActionReceipt(ctx, claim, phase)
	if err == nil && r.afterReceipt != nil {
		r.afterReceipt()
	}
	return err
}

func (r *stageAReservationFaultRepository) ReconcileRemoteAction(ctx context.Context, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	if r.beforeReconcile != nil {
		r.beforeReconcile()
	}
	pair, err := r.fakeRepository.ReconcileRemoteAction(ctx, observation)
	if err == nil && r.reconcileErr != nil {
		return pair, r.reconcileErr
	}
	return pair, err
}

func TestStageAAutomaticNoSideEffectReleasesReservationWithoutDueJobs(t *testing.T) {
	for _, outcome := range []string{upstream.MutationNotSent, upstream.MutationConfirmedRejected} {
		t.Run(outcome, func(t *testing.T) {
			actioner := &fakeTargetSchedulableActioner{afterWrite: func(bool) {}}
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
			platform := &fakePlatformActioner{sub2APIErr: &upstream.RequestError{MessageKey: upstream.ErrorNetwork, MutationOutcome: outcome}}
			service.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
			target := sub2APISuspendedTargetFixture(repo, "1515")
			state := repo.states[target.TargetID]["model-a"]
			state.UserID, state.AdminAccountID, state.HealthEvidenceStatus = "user1", "ws1", HealthEvidenceValid
			protocol := TestProtocolChatCompletions
			state.HealthEvidenceProtocol = &protocol
			repo.states[target.TargetID]["model-a"] = state
			inventory, err := service.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
			if err != nil {
				t.Fatal(err)
			}
			guard := service.sub2APIFloorGuardFor("user1", "ws1")
			_, err = service.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", inventory.session, target, []probeModelSpec{sub2APIActionTestSpec()}, guard, inventory, fullFloorTestMonitoringScope(*inventory))
			if err == nil || len(platform.sub2APICalls) != 1 {
				t.Fatalf("automatic attempt must send once and fail: %v", err)
			}
			pair := repo.actionPair(RemoteActionScope{"user1", "ws1", target.TargetID})
			if pair.Target == nil || pair.Target.PendingDispatchPhase != RemoteDispatchPhase(outcome) || pair.Target.LastAppliedStatus != "active" || pair.Target.OriginalStatus != "active" {
				t.Fatal("automatic no-effect receipt changed its baseline or was lost")
			}
			// The existing beginning-of-tick reconciliation runs without due jobs.
			policies := repo.policies
			repo.policies = nil
			service.runSchedulerTick(t.Context())
			repo.policies = policies
			if service.sub2APIFloorGuardFor("user1", "ws1") != guard || len(platform.sub2APICalls) != 1 {
				t.Fatal("no-job tick replaced the guard or sent a new automatic request")
			}
			if pair := repo.actionPair(RemoteActionScope{"user1", "ws1", target.TargetID}); pair.pendingCount() != 0 {
				t.Fatal("no-job tick failed to settle no-effect receipt")
			}
			actioner.afterWriteForAccount = func(id string, value bool) {
				reader := service.platformGroups.(fakePlatformGroupReader)
				for i := range reader.accountsByGrp["g1"] {
					if reader.accountsByGrp["g1"][i].ID == id {
						reader.accountsByGrp["g1"][i].Schedulable = boolPointer(value)
					}
				}
			}
			if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1616", false); err != nil || actioner.calls != 1 {
				t.Fatalf("settled automatic action still consumed usable capacity: %v", err)
			}
			if _, err := service.SetTargetSchedulable(t.Context(), "user1", target.TargetID, false); err == nil || actioner.calls != 1 {
				t.Fatal("settling automatic no-effect action bypassed the last usable floor")
			}
		})
	}
}

func TestStageAReservationRemainsForUncertainReceiptAndFailedSettlement(t *testing.T) {
	for _, fault := range []string{"unknown", "receipt", "reconciliation"} {
		t.Run(fault, func(t *testing.T) {
			outcome := upstream.MutationNotSent
			if fault == "unknown" {
				outcome = upstream.MutationUncertain
			}
			actioner := &fakeTargetSchedulableActioner{err: &upstream.RequestError{MessageKey: upstream.ErrorNetwork, MutationOutcome: outcome}, afterWrite: func(bool) {}}
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
			wrapper := &stageAReservationFaultRepository{fakeRepository: repo}
			if fault == "receipt" {
				wrapper.receiptErr = errors.New("receipt not committed")
			}
			service.repo = wrapper
			if fault == "reconciliation" {
				// Return an error after the result has been computed, modeling an
				// uncertain commit: a cleared pair alone is insufficient evidence.
				wrapper.afterReceipt = func() { wrapper.reconcileErr = errors.New("reconcile commit uncertain") }
			}
			if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1515", false); err == nil || actioner.calls != 1 {
				t.Fatal("first faulted action must report failure after one request")
			}
			wrapper.reconcileErr = nil
			actioner.err = nil
			if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1616", false); err == nil || actioner.calls != 1 {
				t.Fatalf("unreliable action released protected capacity: %v", err)
			}
		})
	}
}

func TestStageADoublePendingCannotReleaseReservation(t *testing.T) {
	actioner := &fakeTargetSchedulableActioner{afterWrite: func(bool) {}}
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
	scope := RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"}
	repo.targetActionStates["user1|ws1|"+scope.TargetID] = TargetActionState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, PendingStatus: "inactive", PendingDispatchID: "target", PendingDispatchPhase: DispatchNotSent}
	priority := 20
	repo.priorityStates["user1|ws1|"+scope.TargetID] = PrioritySyncState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, PendingPriority: &priority, PendingDispatchID: "priority", PendingDispatchPhase: DispatchNotSent}
	inventory, err := service.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	guard := service.sub2APIFloorGuardFor("user1", "ws1")
	guard.rememberInventory(*inventory)
	if err := service.overlayPersistentPending(t.Context(), "user1", "ws1", guard, *inventory); err != nil {
		t.Fatal(err)
	}
	pair, err := service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: scope})
	if err != nil || pair.pendingCount() != 2 {
		t.Fatal("double pending protection was changed")
	}
	if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1616", false); err == nil || actioner.calls != 0 {
		t.Fatal("double pending released peer capacity")
	}
}

func TestStageAPreparedReservationReleasesOnlyAfterOwnerLoss(t *testing.T) {
	for _, ownerValid := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner-lost", true: "owner-valid"}[ownerValid], func(t *testing.T) {
			actioner := &fakeTargetSchedulableActioner{afterWrite: func(bool) {}}
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
			scope := RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"}
			owner := "old-owner"
			var release func()
			if ownerValid {
				leased, done, err := service.prepareManualAction(t.Context(), scope.UserID, scope.AdminAccountID, scope.TargetID)
				if err != nil {
					t.Fatal(err)
				}
				release = done
				owner = actionLeaseFromContext(leased).OwnerID
			}
			repo.targetActionStates["user1|ws1|"+scope.TargetID] = TargetActionState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "active", OriginalWeight: intPointer(77), LastAppliedWeight: intPointer(44), Conflict: true, PendingStatus: "inactive", PendingActionKind: TargetMutationStatus, PendingSource: ActionSourceManual, PendingHadAutomaticBaseline: true, PendingDispatchID: "prepared", PendingOwnerID: owner, PendingDispatchPhase: DispatchPrepared}
			inventory, err := service.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
			if err != nil {
				if release != nil {
					release()
				}
				t.Fatal(err)
			}
			guard := service.sub2APIFloorGuardFor("user1", "ws1")
			target, _ := findActionInventoryTarget(scope.TargetID, *inventory)
			if result := guard.reserveSub2APISchedulableFalse(target, *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != "" {
				if release != nil {
					release()
				}
				t.Fatal("initial reservation failed")
			}
			pair, err := service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: scope})
			if release != nil {
				release()
			}
			if err != nil || (pair.pendingCount() == 1) != ownerValid || pair.Target == nil || pair.Target.OriginalStatus != "active" || pair.Target.LastAppliedStatus != "active" || *pair.Target.OriginalWeight != 77 || *pair.Target.LastAppliedWeight != 44 || !pair.Target.Conflict {
				t.Fatal("prepared settlement changed its protection or original automatic baseline")
			}
			actioner.afterWriteForAccount = func(id string, value bool) {
				reader := service.platformGroups.(fakePlatformGroupReader)
				for i := range reader.accountsByGrp["g1"] {
					if reader.accountsByGrp["g1"][i].ID == id {
						reader.accountsByGrp["g1"][i].Schedulable = boolPointer(value)
					}
				}
			}
			_, err = service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1616", false)
			if ownerValid {
				if err == nil || actioner.calls != 0 {
					t.Fatal("live prepared owner released peer capacity")
				}
			} else if err != nil || actioner.calls != 1 {
				t.Fatalf("proven unsent prepared action retained its reservation: %v", err)
			}
		})
	}
}

func TestStageANoEffectSettlementPreservesNewerReservation(t *testing.T) {
	for _, changedInventory := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-inventory", true: "new-fingerprint"}[changedInventory], func(t *testing.T) {
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), &fakeTargetSchedulableActioner{})
			reader := service.platformGroups.(fakePlatformGroupReader)
			reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "1717", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
			inventory, err := service.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
			if err != nil {
				t.Fatal(err)
			}
			guard := service.sub2APIFloorGuardFor("user1", "ws1")
			target, _ := findActionInventoryTarget("sub2api:ws1:1515", *inventory)
			scope := fullFloorTestMonitoringScope(*inventory)
			if result := guard.reserveSub2APISchedulableFalse(target, *inventory, scope); result.remoteAction != "" {
				t.Fatal("initial reservation failed")
			}
			priority := 20
			repo.priorityStates["user1|ws1|"+target.TargetID] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: target.TargetID, PendingPriority: &priority, PendingDispatchID: "old-priority", PendingDispatchPhase: DispatchNotSent}
			wrapper := &stageAReservationFaultRepository{fakeRepository: repo}
			wrapper.beforeReconcile = func() {
				if changedInventory {
					inventory.groups[0].group.Name = "updated"
				}
				if result := guard.reserveSub2APISchedulableFalse(target, *inventory, scope); result.remoteAction != "" {
					t.Fatal("new reservation was not admitted")
				}
			}
			service.repo = wrapper
			pair, err := service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: RemoteActionScope{"user1", "ws1", target.TargetID}})
			if err != nil || pair.pendingCount() != 0 {
				t.Fatal("old no-effect action failed to reconcile")
			}
			peer, _ := findActionInventoryTarget("sub2api:ws1:1616", *inventory)
			if result := guard.reserveSub2APISchedulableFalse(peer, *inventory, scope); result.remoteAction != "" {
				t.Fatal("first remaining peer should retain usable capacity")
			}
			last, _ := findActionInventoryTarget("sub2api:ws1:1717", *inventory)
			if result := guard.reserveSub2APISchedulableFalse(last, *inventory, scope); result.remoteAction != RemoteActionSkippedSub2APILastActive {
				t.Fatal("settling the old Priority action released a newer target reservation")
			}
		})
	}
}

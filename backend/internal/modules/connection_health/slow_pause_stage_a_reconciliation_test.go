package connection_health

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type stageAPreparationCancelRepository struct {
	*fakeRepository
	cancel      context.CancelFunc
	afterPermit bool
}

func (r *stageAPreparationCancelRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	ok, err := r.fakeRepository.ClaimRemoteAction(ctx, claim)
	if ok && err == nil && !r.afterPermit {
		r.cancel()
	}
	return ok, err
}

func (r *stageAPreparationCancelRepository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	ok, err := r.fakeRepository.PermitRemoteAction(ctx, claim)
	if ok && err == nil && r.afterPermit {
		r.cancel()
	}
	return ok, err
}

func TestStageAPreparationCancellationBeforeAndAfterPermit(t *testing.T) {
	for _, permitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-permit", true: "after-permit"}[permitted], func(t *testing.T) {
			repo := &stageAPreparationCancelRepository{fakeRepository: newFakeRepository(), afterPermit: permitted}
			service := &Service{repo: repo}
			leased, release, err := service.prepareManualAction(t.Context(), "u", "w", "sub2api:w:1")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(leased)
			defer cancel()
			repo.cancel = cancel
			claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", "sub2api:w:1"}, Kind: ActionKindTarget, Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingStatus: "inactive", PendingActionKind: TargetMutationStatus, PendingSource: ActionSourceManual}}
			calls := 0
			_, err = service.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
				calls++
				if sendCtx.Err() != nil {
					t.Fatal("permitted HTTP inherited preparation cancellation")
				}
				deadline, bounded := sendCtx.Deadline()
				if !bounded || time.Until(deadline) > 5*time.Second {
					t.Fatal("HTTP has no independent five-second budget")
				}
				return "applied", nil
			})
			pair := repo.actionPair(claim.RemoteActionScope)
			if permitted {
				if err != nil || calls != 1 || pair.Target.PendingDispatchPhase != DispatchConfirmedApplied {
					t.Fatal("permitted send or receipt was abandoned")
				}
			} else {
				if err == nil || calls != 0 {
					t.Fatal("canceled preparation reached HTTP")
				}
				release()
				pair, err = service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
				if err != nil || pair.pendingCount() != 0 || pair.Target != nil {
					t.Fatal("unpermitted released claim did not settle as no-send")
				}
			}
		})
	}
}

// A08/A10 cover the source branch before old automatic baseline logic. These
// are supplementary tests; the key old-code RED is preserved separately.
func TestStageAManualStatusReconciliationPreservesAutomaticBaseline(t *testing.T) {
	for _, sample := range []struct {
		name                  string
		hadBaseline, conflict bool
		last                  string
	}{
		{"temporary", false, false, ""},
		{"active-baseline", true, false, "active"},
		{"prior-conflict", true, true, "active"},
		{"already-inactive-baseline", true, false, "inactive"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			originalWeight, lastWeight := 73, 19
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", OriginalWeight: &originalWeight, LastAppliedStatus: sample.last, LastAppliedWeight: &lastWeight, Conflict: sample.conflict,
				PendingStatus: "inactive", PendingActionKind: TargetMutationStatus, PendingSource: ActionSourceManual, PendingHadAutomaticBaseline: sample.hadBaseline, PendingDispatchID: "manual-close", PendingOwnerID: "owner", PendingDispatchPhase: DispatchConfirmedApplied}
			pair := RemoteActionCheckpoints{Target: &state}
			if !reconcileRemoteAction(&pair, RemoteActionObservation{InventoryComplete: true, Visible: true, Status: "inactive", SnapshotStartedAt: time.Now()}, func(string) bool { return true }) {
				t.Fatal("confirmed manual close failed source reconciliation")
			}
			if pair.pendingCount() != 0 {
				t.Fatal("confirmed manual close left pending")
			}
			if !sample.hadBaseline {
				if pair.Target != nil {
					t.Fatal("temporary row survived reconciliation")
				}
				return
			}
			if pair.Target == nil || pair.Target.OriginalStatus != "active" || pair.Target.LastAppliedStatus != sample.last || !reflect.DeepEqual(pair.Target.OriginalWeight, &originalWeight) || !reflect.DeepEqual(pair.Target.LastAppliedWeight, &lastWeight) || !pair.Target.Conflict {
				t.Fatalf("manual close rewrote automatic baseline or lost manual precedence: %+v", pair.Target)
			}
		})
	}
}

func TestStageAManualSchedulableReconciliationUsesKnownSwitch(t *testing.T) {
	for _, sample := range []struct {
		name              string
		visible, complete bool
		observed          *bool
		wantRelease       bool
	}{
		{"known-matching", true, true, boolPointer(false), true},
		{"known-opposite", true, true, boolPointer(true), false},
		{"unknown", true, true, nil, false},
		{"invisible", false, true, boolPointer(false), false},
		{"incomplete", true, false, boolPointer(false), false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingActionKind: TargetMutationSchedulable, PendingSource: ActionSourceManual, PendingSchedulable: boolPointer(false), PendingHadAutomaticBaseline: false, PendingDispatchID: "manual-switch", PendingOwnerID: "o", PendingDispatchPhase: DispatchConfirmedApplied}
			pair := RemoteActionCheckpoints{Target: &state}
			changed := reconcileRemoteAction(&pair, RemoteActionObservation{InventoryComplete: sample.complete, Visible: sample.visible, Status: "active", Schedulable: sample.observed, SnapshotStartedAt: time.Now()}, func(string) bool { return true })
			if changed != sample.wantRelease || (pair.pendingCount() == 0) != sample.wantRelease {
				t.Fatalf("switch=%s released=%v pending=%d", sample.name, changed, pair.pendingCount())
			}
			if sample.wantRelease && pair.Target != nil {
				t.Fatal("temporary switch row became automatic baseline")
			}
		})
	}
}

func TestStageADeleteReceiptRequiresFreshCompleteAbsence(t *testing.T) {
	for _, sample := range []struct {
		name                                  string
		complete, visible, fresh, wantRelease bool
	}{
		{"deleted", true, false, true, true},
		{"still-visible", true, true, true, false},
		{"partial-absence", false, false, true, false},
		{"old-absence", true, false, false, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			receiptAt := time.Now()
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingActionKind: TargetMutationDelete, PendingSource: ActionSourceManualDelete, PendingDispatchID: "delete-id", PendingOwnerID: "o", PendingDispatchPhase: DispatchConfirmedApplied, UpdatedAt: receiptAt}
			pair := RemoteActionCheckpoints{Target: &state}
			snapshot := receiptAt.Add(-time.Second)
			if sample.fresh {
				snapshot = receiptAt.Add(time.Second)
			}
			changed := reconcileRemoteAction(&pair, RemoteActionObservation{InventoryComplete: sample.complete, Visible: sample.visible, SnapshotStartedAt: snapshot}, func(string) bool { return true })
			if changed != sample.wantRelease || (pair.pendingCount() == 0) != sample.wantRelease {
				t.Fatalf("delete=%s released=%v pending=%d", sample.name, changed, pair.pendingCount())
			}
		})
	}
}

func TestStageAManualNoSideEffectReceiptsClearTemporaryRows(t *testing.T) {
	for _, kind := range []string{TargetMutationStatus, TargetMutationSchedulable, TargetMutationDelete} {
		for _, phase := range []RemoteDispatchPhase{DispatchNotSent, DispatchConfirmedRejected} {
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingStatus: "inactive", PendingActionKind: kind, PendingSource: ActionSourceManual, PendingDispatchID: "d", PendingOwnerID: "o", PendingDispatchPhase: phase}
			pair := RemoteActionCheckpoints{Target: &state}
			if !reconcileRemoteAction(&pair, RemoteActionObservation{}, func(string) bool { return true }) || pair.Target != nil {
				t.Fatalf("temporary %s/%s not removed without inventory", kind, phase)
			}
		}
	}
}

package connection_health

import (
	"context"
	"testing"
	"time"
)

func actionClaimFixture(t *testing.T, repo *fakeRepository, kind, id string) RemoteActionClaim {
	t.Helper()
	lease, acquired, err := repo.AcquireActionLease(context.Background(), "fixture:"+kind+":"+id, false)
	if err != nil || !acquired {
		t.Fatalf("lease: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(lease.Release)
	scope := RemoteActionScope{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1"}
	claim := RemoteActionClaim{RemoteActionScope: scope, Kind: kind, DispatchID: id, OwnerID: lease.OwnerID, LeaseKey: lease.Key}
	priority := 7
	if kind == ActionKindPriority {
		claim.Priority = &PrioritySyncState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalPriority: 10, LastAppliedPriority: 10, PendingPriority: &priority}
	} else {
		claim.Target = &TargetActionState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}
	}
	return claim
}

func TestActionCheckpointBothKindsExcludeSecondClaimAndHTTP(t *testing.T) {
	for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
		t.Run(kind, func(t *testing.T) {
			repo := newFakeRepository()
			other := ActionKindTarget
			if kind == other {
				other = ActionKindPriority
			}
			winner := actionClaimFixture(t, repo, kind, "a")
			loser := actionClaimFixture(t, repo, other, "b")
			if ok, err := repo.ClaimRemoteAction(context.Background(), winner); !ok || err != nil {
				t.Fatal(ok, err)
			}
			for _, phase := range []RemoteDispatchPhase{DispatchPrepared, DispatchSending, DispatchUncertain, DispatchConfirmedApplied} {
				if phase == DispatchSending {
					if ok, err := repo.PermitRemoteAction(context.Background(), winner); !ok || err != nil {
						t.Fatal(ok, err)
					}
				}
				if phase == DispatchUncertain || phase == DispatchConfirmedApplied {
					if err := repo.RecordRemoteActionReceipt(context.Background(), winner, phase); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				if ok, _ := repo.ClaimRemoteAction(context.Background(), loser); ok {
					if allowed, _ := repo.PermitRemoteAction(context.Background(), loser); allowed {
						calls++
					}
				}
				if calls != 0 {
					t.Fatalf("%s allowed opposite HTTP", phase)
				}
				pair := repo.actionPair(winner.RemoteActionScope)
				if pair.pendingCount() != 1 {
					t.Fatalf("%s created second claim: %+v", phase, pair)
				}
			}
		})
	}
}

func TestActionCheckpointPreparedTakeoverAndSendingHold(t *testing.T) {
	for _, sending := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "sending"}[sending], func(t *testing.T) {
			repo := newFakeRepository()
			old := actionClaimFixture(t, repo, ActionKindTarget, "old")
			if ok, err := repo.ClaimRemoteAction(context.Background(), old); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if sending {
				if ok, err := repo.PermitRemoteAction(context.Background(), old); !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			repo.expireActionLeaseForTest(old.OwnerID, false)
			pair, err := repo.ReconcileRemoteAction(context.Background(), RemoteActionObservation{RemoteActionScope: old.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: time.Now(), Status: "active"})
			if err != nil {
				t.Fatal(err)
			}
			if sending && pair.pendingCount() != 1 {
				t.Fatal("sending claim was released by old-value readback")
			}
			if !sending && pair.pendingCount() != 0 {
				t.Fatal("expired prepared was not revoked")
			}
			newClaim := actionClaimFixture(t, repo, ActionKindPriority, "new")
			newHTTP := 0
			if ok, _ := repo.ClaimRemoteAction(context.Background(), newClaim); ok {
				if permitted, _ := repo.PermitRemoteAction(context.Background(), newClaim); permitted {
					newHTTP++
				}
			}
			oldHTTP := 0
			if permitted, _ := repo.PermitRemoteAction(context.Background(), old); permitted {
				oldHTTP++
			}
			if oldHTTP != 0 || (!sending && newHTTP != 1) || (sending && newHTTP != 0) {
				t.Fatalf("old=%d new=%d sending=%v", oldHTTP, newHTTP, sending)
			}
		})
	}
}

func TestActionCheckpointReceiptRequiresLaterCompleteVisibleSnapshot(t *testing.T) {
	repo := newFakeRepository()
	claim := actionClaimFixture(t, repo, ActionKindTarget, "original")
	if ok, err := repo.ClaimRemoteAction(context.Background(), claim); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := repo.PermitRemoteAction(context.Background(), claim); !ok || err != nil {
		t.Fatal(ok, err)
	}
	oldSnapshot := time.Now()
	if err := repo.RecordRemoteActionReceipt(context.Background(), claim, DispatchConfirmedApplied); err != nil {
		t.Fatal(err)
	}
	for _, observation := range []RemoteActionObservation{
		{InventoryComplete: true, Visible: true, SnapshotStartedAt: oldSnapshot, Status: "inactive"},
		{InventoryComplete: false, Visible: true, SnapshotStartedAt: time.Now(), Status: "inactive"},
		{InventoryComplete: true, Visible: false, SnapshotStartedAt: time.Now(), Status: "inactive"},
	} {
		observation.RemoteActionScope = claim.RemoteActionScope
		pair, err := repo.ReconcileRemoteAction(context.Background(), observation)
		if err != nil || pair.pendingCount() != 1 {
			t.Fatalf("unsafe observation released claim: %+v err=%v", observation, err)
		}
	}
	pair, err := repo.ReconcileRemoteAction(context.Background(), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: time.Now(), Status: "inactive"})
	if err != nil || pair.pendingCount() != 0 || pair.Target.LastAppliedStatus != "inactive" {
		t.Fatalf("later actual snapshot failed reconciliation: %+v %v", pair, err)
	}
	if err := repo.RecordRemoteActionReceipt(context.Background(), claim, DispatchConfirmedApplied); err == nil {
		t.Fatal("late receipt reclaimed a closed ID")
	}
}

func TestActionCheckpointIllegalDualPreparedBlocksBothAndDelete(t *testing.T) {
	repo := newFakeRepository()
	priority := actionClaimFixture(t, repo, ActionKindPriority, "p")
	target := actionClaimFixture(t, repo, ActionKindTarget, "t")
	priority.Priority.PendingDispatchID = priority.DispatchID
	priority.Priority.PendingOwnerID = priority.OwnerID
	priority.Priority.PendingDispatchPhase = DispatchPrepared
	target.Target.PendingDispatchID = target.DispatchID
	target.Target.PendingOwnerID = target.OwnerID
	target.Target.PendingDispatchPhase = DispatchPrepared
	key := "u|w|sub2api:w:1"
	repo.priorityStates[key] = *priority.Priority
	repo.targetActionStates[key] = *target.Target
	for _, claim := range []RemoteActionClaim{priority, target} {
		if ok, _ := repo.PermitRemoteAction(context.Background(), claim); ok {
			t.Fatal("dual claim permitted HTTP")
		}
	}
	if err := repo.DeletePrioritySyncState(context.Background(), "u", "w", "sub2api:w:1"); err == nil {
		t.Fatal("delete bypassed dual claim")
	}
	cleared := *target.Target
	clearTargetDispatch(&cleared)
	if err := repo.UpsertTargetActionState(context.Background(), cleared); err == nil {
		t.Fatal("upsert erased outstanding claim")
	}
	if pair := repo.actionPair(priority.RemoteActionScope); pair.pendingCount() != 2 {
		t.Fatal("legacy conflict was silently reduced")
	}
}

func TestActionCheckpointDuplicateIDCannotChangeIntentionOrScope(t *testing.T) {
	repo := newFakeRepository()
	claim := actionClaimFixture(t, repo, ActionKindPriority, "fixed")
	foreign := claim
	foreignState := *claim.Priority
	foreignState.AdminAccountID = "other"
	foreign.Priority = &foreignState
	if ok, _ := repo.ClaimRemoteAction(context.Background(), foreign); ok {
		t.Fatal("checkpoint was inserted outside the locked workspace")
	}
	if ok, err := repo.ClaimRemoteAction(context.Background(), claim); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := repo.ClaimRemoteAction(context.Background(), claim); !ok || err != nil {
		t.Fatalf("identical prepared read is not idempotent: %v %v", ok, err)
	}
	changed := claim
	changedState := *claim.Priority
	otherValue := 99
	changedState.PendingPriority = &otherValue
	changed.Priority = &changedState
	if ok, _ := repo.ClaimRemoteAction(context.Background(), changed); ok {
		t.Fatal("same ID changed intended priority")
	}
	if ok, _ := repo.PermitRemoteAction(context.Background(), changed); ok {
		t.Fatal("sending gate authorized a value different from the recorded intention")
	}
	if ok, err := repo.PermitRemoteAction(context.Background(), claim); !ok || err != nil {
		t.Fatalf("original intention lost its permit: %v %v", ok, err)
	}
}

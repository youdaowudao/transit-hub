package connection_health

import (
	"testing"
	"time"
)

func protocolClaims() (RemoteActionClaim, RemoteActionClaim) {
	scope := RemoteActionScope{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1"}
	pending := 1001
	priority := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindPriority, DispatchID: "priority-1", OwnerID: "p-owner", LeaseKey: "p-lease", Priority: &PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalPriority: 1000, LastAppliedPriority: 1000, PendingPriority: &pending}}
	target := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindTarget, DispatchID: "target-1", OwnerID: "t-owner", LeaseKey: "t-lease", Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}}
	return priority, target
}

func TestProtocolClaimBothDirectionsAndOneTimeSend(t *testing.T) {
	for _, firstKind := range []string{ActionKindPriority, ActionKindTarget} {
		t.Run(firstKind, func(t *testing.T) {
			priority, target := protocolClaims()
			first, second := priority, target
			if firstKind == ActionKindTarget {
				first, second = target, priority
			}
			pair := RemoteActionCheckpoints{}
			if allowed, err := claimRemoteAction(&pair, first); err != nil || !allowed {
				t.Fatalf("first claim: allowed=%v err=%v", allowed, err)
			}
			if allowed, _ := claimRemoteAction(&pair, second); allowed {
				t.Error("two prepared claims accepted")
			}
			if pair.pendingCount() != 1 {
				t.Fatalf("two-table pending count=%d", pair.pendingCount())
			}
			if permitRemoteAction(&pair, second) {
				t.Error("loser obtained permission")
			}
			if !permitRemoteAction(&pair, first) {
				t.Fatal("winner lost permission")
			}
			if permitRemoteAction(&pair, first) {
				t.Error("same dispatch ID sent twice")
			}
			if allowed, _ := claimRemoteAction(&pair, second); allowed {
				t.Error("sending claim was displaced")
			}
			if !receiptRemoteAction(&pair, first, DispatchUncertain, time.Now()) {
				t.Fatal("uncertain receipt lost")
			}
			if allowed, _ := claimRemoteAction(&pair, second); allowed {
				t.Error("uncertain claim was displaced")
			}
		})
	}
}

func TestProtocolClaimIllegalDualPreparedBlocksBoth(t *testing.T) {
	p, targ := protocolClaims()
	left, right := RemoteActionCheckpoints{}, RemoteActionCheckpoints{}
	_, _ = claimRemoteAction(&left, p)
	_, _ = claimRemoteAction(&right, targ)
	pair := RemoteActionCheckpoints{Priority: left.Priority, Target: right.Target}
	if pair.pendingCount() != 2 {
		t.Fatal("invalid fixture")
	}
	if permitRemoteAction(&pair, p) || permitRemoteAction(&pair, targ) {
		t.Error("legacy double claim was silently assigned a winner")
	}
	if pair.pendingCount() != 2 {
		t.Error("conflicting claims were deleted to allow a send")
	}
}

func TestProtocolClaimReceiptAndFreshObservationAreSeparate(t *testing.T) {
	p, _ := protocolClaims()
	pair := RemoteActionCheckpoints{}
	_, _ = claimRemoteAction(&pair, p)
	if !permitRemoteAction(&pair, p) {
		t.Fatal("permit")
	}
	at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	old := RemoteActionObservation{RemoteActionScope: p.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: at.Add(-time.Second), Priority: p.Priority.PendingPriority}
	if !receiptRemoteAction(&pair, p, DispatchConfirmedApplied, at) {
		t.Fatal("receipt")
	}
	if reconcileRemoteAction(&pair, old, func(string) bool { return false }) || pair.pendingCount() != 1 {
		t.Error("pre-receipt inventory released terminal claim")
	}
	late := p
	late.DispatchID = "different-id"
	if receiptRemoteAction(&pair, late, DispatchConfirmedRejected, at.Add(time.Second)) {
		t.Error("different dispatch receipt mutated existing claim")
	}
	fresh := old
	fresh.SnapshotStartedAt = at.Add(time.Second)
	if !reconcileRemoteAction(&pair, fresh, func(string) bool { return false }) || pair.pendingCount() != 0 {
		t.Error("fresh confirmed observation did not release claim")
	}
	if pair.Priority.LastAppliedPriority != 1001 {
		t.Errorf("wrong applied priority: %+v", pair.Priority)
	}
}

func TestProtocolClaimUncertainNeverExpiresFromSnapshot(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchSending, DispatchUncertain} {
		t.Run(string(phase), func(t *testing.T) {
			p, _ := protocolClaims()
			pair := RemoteActionCheckpoints{}
			_, _ = claimRemoteAction(&pair, p)
			_ = permitRemoteAction(&pair, p)
			at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
			if phase == DispatchUncertain {
				_ = receiptRemoteAction(&pair, p, phase, at)
			}
			for _, current := range []int{1000, 1001, 777} {
				obs := RemoteActionObservation{RemoteActionScope: p.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: at.Add(24 * time.Hour), Priority: &current}
				if reconcileRemoteAction(&pair, obs, func(string) bool { return false }) || pair.pendingCount() != 1 {
					t.Errorf("%s released by current=%d and expired owner", phase, current)
				}
			}
		})
	}
}

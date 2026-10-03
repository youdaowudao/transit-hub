package connection_health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type unknownPermitRepository struct{ *fakeRepository }

func (r *unknownPermitRepository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	if permitted, err := r.fakeRepository.PermitRemoteAction(ctx, claim); !permitted || err != nil {
		return permitted, err
	}
	return false, errors.New("commit outcome unknown")
}

func TestActionDispatchUnknownPermissionCommitSendsZeroHTTP(t *testing.T) {
	repo := &unknownPermitRepository{newFakeRepository()}
	svc := &Service{repo: repo}
	scope := RemoteActionScope{"u", "w", "sub2api:w:1"}
	claim := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindTarget, Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}}
	calls := 0
	_, err := svc.dispatchRemoteAction(context.Background(), claim, func(context.Context) (string, error) { calls++; return "applied", nil })
	if err == nil || calls != 0 {
		t.Fatalf("unknown commit authorized HTTP: calls=%d err=%v", calls, err)
	}
	pair := repo.actionPair(scope)
	if !targetActionPending(pair.Target) || pair.Target.PendingDispatchPhase != DispatchSending {
		t.Fatalf("unknown permission lost its checkpoint: %+v", pair.Target)
	}
}

func TestActionDispatchLostLeaseCancelsRequestAndBlocksOtherKind(t *testing.T) {
	repo := newFakeRepository()
	svc := &Service{repo: repo}
	scope := RemoteActionScope{"u", "w", "sub2api:w:1"}
	claim := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindTarget, Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}}
	started := make(chan *RuntimeLeaseHandle, 1)
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := svc.dispatchRemoteAction(t.Context(), claim, func(ctx context.Context) (string, error) {
			calls.Add(1)
			started <- actionLeaseFromContext(ctx)
			<-ctx.Done()
			return "failed", ctx.Err()
		})
		done <- err
	}()
	lease := <-started
	repo.expireActionLeaseForTest(lease.OwnerID, true)
	if err := <-done; err == nil {
		t.Fatal("lost context request unexpectedly succeeded")
	}
	priority := 5
	other := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindPriority, Priority: &PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalPriority: 10, LastAppliedPriority: 10, PendingPriority: &priority}}
	_, err := svc.dispatchRemoteAction(context.Background(), other, func(context.Context) (string, error) { calls.Add(1); return "priority", nil })
	if err == nil || calls.Load() != 1 {
		t.Fatalf("lost sending allowed opposite HTTP: calls=%d err=%v", calls.Load(), err)
	}
	pair := repo.actionPair(scope)
	if pair.pendingCount() != 1 || pair.Target.PendingDispatchPhase != DispatchUncertain {
		t.Fatalf("cancelled sent call must remain uncertain: %+v", pair.Target)
	}
}

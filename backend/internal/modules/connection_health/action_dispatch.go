package connection_health

import (
	"context"
	"errors"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func (s *Service) reconcileActionObservation(ctx context.Context, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	repository, ok := s.repo.(actionCheckpointRepository)
	if !ok {
		return RemoteActionCheckpoints{}, ErrRemoteActionPending
	}
	return repository.ReconcileRemoteAction(ctx, observation)
}

func actionGuardForTarget(target AdminProbeTarget, states []ConnectionHealthState, models []string) RemoteActionHealthGuard {
	return RemoteActionHealthGuard{Required: true, Memberships: target.TestMemberships, InventoryComplete: target.InventoryComplete, Configuration: target.TestConfiguration, Models: models, ExpectedStates: states}
}

// A claim returning an uncertain commit is never dispatched. Each successful
// permission is consumed once; all receipts keep the ID until a later inventory.
func (s *Service) dispatchRemoteAction(ctx context.Context, claim RemoteActionClaim, send func(context.Context) (string, error)) (actionResult string, actionErr error) {
	repository, ok := s.repo.(actionCheckpointRepository)
	if !ok {
		return RemoteActionAwaitingConfirmation, ErrRemoteActionPending
	}
	handle := actionLeaseFromContext(ctx)
	if handle == nil {
		var release func()
		var acquired bool
		var err error
		ctx, release, acquired, err = s.acquireActionTargetLease(ctx, claim.TargetID, true)
		if err != nil || !acquired {
			if err == nil {
				err = ErrRemoteActionLeaseLost
			}
			return RemoteActionAwaitingConfirmation, err
		}
		defer release()
		handle = actionLeaseFromContext(ctx)
	}
	if err := ctx.Err(); err != nil {
		return RemoteActionAwaitingConfirmation, err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	claim.DispatchID, claim.OwnerID, claim.LeaseKey = id, handle.OwnerID, handle.Key
	claimed, err := repository.ClaimRemoteAction(ctx, claim)
	if err != nil || !claimed {
		if err == nil {
			err = ErrRemoteActionPending
		}
		return RemoteActionAwaitingConfirmation, err
	}
	permitted, err := repository.PermitRemoteAction(ctx, claim)
	if err != nil || !permitted {
		if err == nil {
			err = ErrRemoteActionPending
		}
		return RemoteActionAwaitingConfirmation, err
	}
	phase := DispatchUncertain
	// Receipts describe an attempted call even when its lease context has expired.
	// A bounded independent context cannot authorize another remote write.
	defer func() {
		receiptCtx, cancel := context.WithTimeout(context.Background(), runtimeLeaseQueryTimeout)
		defer cancel()
		if receiptErr := repository.RecordRemoteActionReceipt(receiptCtx, claim, phase); receiptErr != nil {
			actionErr = errors.Join(actionErr, receiptErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		phase = DispatchNotSent
		return RemoteActionAwaitingConfirmation, err
	}
	action, err := send(ctx)
	if err == nil {
		phase = DispatchConfirmedApplied
		if action == RemoteActionUnsupported {
			phase = DispatchNotSent
		}
	}
	return action, err
}

func (s *Service) dispatchSafeTargetAction(ctx context.Context, session upstream.Session, target AdminProbeTarget, stored TargetActionState, guard RemoteActionHealthGuard) (string, error) {
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{stored.UserID, stored.AdminAccountID, stored.TargetID}, Kind: ActionKindTarget, Target: &stored, Guard: guard}
	return s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		return s.dispatcher.ApplyTargetState(sendCtx, session, target, stored.PendingWeight, stored.PendingStatus)
	})
}

func (s *Service) dispatchSafePriorityAction(ctx context.Context, session upstream.Session, target AdminProbeTarget, stored PrioritySyncState, guard RemoteActionHealthGuard) error {
	if stored.PendingPriority == nil {
		return errors.New("missing intended priority")
	}
	desired := *stored.PendingPriority
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{stored.UserID, stored.AdminAccountID, stored.TargetID}, Kind: ActionKindPriority, Priority: &stored, Guard: guard}
	_, err := s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		return "priority", s.updateAdminTargetPriority(sendCtx, session, target.AccountID, desired)
	})
	return err
}

func targetObservation(userID, adminAccountID string, target AdminProbeTarget, inventory *adminWorkspaceInventory) RemoteActionObservation {
	observation := RemoteActionObservation{RemoteActionScope: RemoteActionScope{userID, adminAccountID, target.TargetID}, Status: target.AccountStatus, Weight: target.AccountWeight}
	if inventory == nil {
		return observation
	}
	observation.InventoryComplete = adminInventoryComplete(*inventory)
	observation.SnapshotStartedAt = inventory.snapshotStartedAt
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == target.AccountID {
				observation.Visible = true
				observation.Priority = cloneIntPointer(account.Priority)
			}
		}
	}
	return observation
}

func healthStatesUsableForTarget(target AdminProbeTarget, states []ConnectionHealthState, expectedModels int) bool {
	configuration := target.TestConfiguration
	if !target.InventoryComplete || (configuration.Status != "default" && configuration.Status != "inherited") || len(states) != expectedModels || expectedModels == 0 {
		return false
	}
	for _, state := range states {
		if !healthEvidenceMatches(state, configuration.Protocol) {
			return false
		}
	}
	return true
}

// Keep the time type in the checkpoint boundary explicit for call sites that
// reuse an inventory rather than manufacturing a fresh observation timestamp.
func inventorySnapshotTime(inventory *adminWorkspaceInventory) time.Time {
	if inventory == nil {
		return time.Time{}
	}
	return inventory.snapshotStartedAt
}

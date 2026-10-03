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
const actionPreparationTimeout = 30 * time.Second
const actionPermitMinimumBudget = 10 * time.Second

// Registration and closing admission share one mutex, so Shutdown cannot miss
// a sender that has already passed its permit gate.
func (s *Service) registerActionDispatch() (func(), error) {
	s.actionDispatchMu.Lock()
	defer s.actionDispatchMu.Unlock()
	if s.actionDispatchClosed {
		return nil, errors.New("remote action service shutting down")
	}
	s.actionDispatchWG.Add(1)
	return s.actionDispatchWG.Done, nil
}
func (s *Service) closeActionAdmission() {
	s.actionDispatchMu.Lock()
	s.actionDispatchClosed = true
	s.actionDispatchMu.Unlock()
}
func (s *Service) drainActionDispatches(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.actionDispatchWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) dispatchRemoteAction(ctx context.Context, claim RemoteActionClaim, send func(context.Context) (string, error)) (actionResult string, actionErr error) {
	repository, ok := s.repo.(actionCheckpointRepository)
	if !ok {
		return RemoteActionAwaitingConfirmation, ErrRemoteActionPending
	}
	complete, err := s.registerActionDispatch()
	if err != nil {
		return RemoteActionAwaitingConfirmation, err
	}
	defer complete()
	prepCtx, prepCancel := context.WithTimeout(ctx, actionPreparationTimeout)
	defer prepCancel()
	handle := actionLeaseFromContext(prepCtx)
	if handle == nil {
		var release func()
		var acquired bool
		prepCtx, release, acquired, err = s.acquireActionTargetLease(prepCtx, claim.TargetID, true)
		if err != nil || !acquired {
			if err == nil {
				err = ErrRemoteActionLeaseLost
			}
			return RemoteActionAwaitingConfirmation, err
		}
		defer release()
		handle = actionLeaseFromContext(prepCtx)
	}
	if err := prepCtx.Err(); err != nil {
		return RemoteActionAwaitingConfirmation, err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	claim.DispatchID, claim.OwnerID, claim.LeaseKey = id, handle.OwnerID, handle.Key
	mutation := mutationLeaseFromContext(prepCtx)
	if mutation != nil {
		claim.MutationLeaseKey, claim.MutationOwnerID = mutation.Key, mutation.OwnerID
	}
	claimed, err := repository.ClaimRemoteAction(prepCtx, claim)
	if err != nil || !claimed {
		if err == nil {
			err = ErrRemoteActionPending
		}
		return RemoteActionAwaitingConfirmation, err
	}
	// Preparation budget is checked after claim and immediately before permit.
	deadline, bounded := prepCtx.Deadline()
	if !bounded || time.Until(deadline) < actionPermitMinimumBudget {
		receiptCtx, cancel := context.WithTimeout(context.Background(), runtimeLeaseQueryTimeout)
		err := repository.RecordRemoteActionReceipt(receiptCtx, claim, DispatchNotSent)
		cancel()
		return RemoteActionAwaitingConfirmation, errors.Join(context.DeadlineExceeded, err)
	}
	permitted, err := repository.PermitRemoteAction(prepCtx, claim)
	if err != nil || !permitted {
		if err == nil {
			err = ErrRemoteActionPending
		}
		return RemoteActionAwaitingConfirmation, err
	}
	phase := DispatchUncertain
	// After permit use separate HTTP and receipt budgets. Lease loss still cancels
	// HTTP; a browser or preparation deadline cannot abandon the permitted send.
	sendCtx, sendCancel := context.WithTimeout(context.WithoutCancel(prepCtx), 5*time.Second)
	stopTarget := context.AfterFunc(handle.Context, sendCancel)
	var stopMutation func() bool
	if mutation != nil {
		stopMutation = context.AfterFunc(mutation.Context, sendCancel)
	}
	defer func() {
		sendCancel()
		stopTarget()
		if stopMutation != nil {
			stopMutation()
		}
		receiptCtx, cancel := context.WithTimeout(context.Background(), runtimeLeaseQueryTimeout)
		defer cancel()
		if receiptErr := repository.RecordRemoteActionReceipt(receiptCtx, claim, phase); receiptErr != nil {
			actionErr = errors.Join(actionErr, receiptErr)
		}
	}()
	if handle.Context.Err() != nil || (mutation != nil && mutation.Context.Err() != nil) {
		phase = DispatchNotSent
		return RemoteActionAwaitingConfirmation, ErrRemoteActionLeaseLost
	}
	action, err := send(sendCtx)
	phase = RemoteDispatchPhase(upstream.RemoteMutationOutcome(err))
	if action == RemoteActionUnsupported && err == nil {
		phase = DispatchNotSent
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
	observation := RemoteActionObservation{RemoteActionScope: RemoteActionScope{userID, adminAccountID, target.TargetID}, Status: target.AccountStatus, Weight: target.AccountWeight, Schedulable: target.Schedulable}
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
				observation.Status = account.Status
				observation.Schedulable = account.Schedulable
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

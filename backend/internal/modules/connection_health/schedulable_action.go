package connection_health

import (
	"context"
	"errors"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	ErrorSchedulableActionFailed    = "admin.connectionHealth.errors.schedulableActionFailed"
	ErrorSchedulableReadbackFailed  = "admin.connectionHealth.errors.schedulableReadbackFailed"
	ErrorSchedulableAuditFailed     = "admin.connectionHealth.errors.schedulableAuditFailed"
	ErrorSchedulableUnsupported     = "admin.connectionHealth.errors.schedulableUnsupported"
	ErrorSub2APIGroupLastUsable     = "admin.connectionHealth.errors.sub2apiGroupLastUsable"
	ErrorSub2APIInventoryIncomplete = "admin.connectionHealth.errors.sub2apiInventoryIncomplete"

	SchedulableActionSucceeded = "schedulable_user_action_succeeded"
	SchedulableActionFailed    = "schedulable_user_action_failed"
	ActionSourceUser           = "user_action"
	ActionSourceHealthProbe    = "health_probe"

	RemoteActionSchedulableEnabled       = "sub2api_schedulable_enabled"
	RemoteActionSchedulableDisabled      = "sub2api_schedulable_disabled"
	RemoteActionSchedulableEnableFailed  = "sub2api_schedulable_enable_failed"
	RemoteActionSchedulableDisableFailed = "sub2api_schedulable_disable_failed"
)

// TargetSchedulableActioner is the only write capability for the Sub2API business
// scheduling switch. The scheduler and state machine do not depend on this interface.
type TargetSchedulableActioner interface {
	SetSub2APIAdminAccountSchedulable(session upstream.Session, accountID string, schedulable bool) error
}

type TargetSchedulableActionResult struct {
	TargetID     string    `json:"targetId"`
	Schedulable  bool      `json:"schedulable"`
	ActionSource string    `json:"actionSource"`
	ActionAt     time.Time `json:"actionAt"`
}

// SetTargetSchedulable executes an explicit user command, then re-reads the upstream
// account. A successful write without a matching, parseable readback is still a failure.
type schedulableActionReadback struct {
	Account   upstream.AdminGroupAccountInfo
	StartedAt time.Time
}

func (s *Service) SetTargetSchedulable(ctx context.Context, userID string, targetID string, schedulable bool) (TargetSchedulableActionResult, error) {
	result, _, err := s.setTargetSchedulable(ctx, userID, targetID, schedulable, nil)
	return result, err
}
func (s *Service) setTargetSchedulable(ctx context.Context, userID string, targetID string, schedulable bool, extraCheck func(context.Context, adminTargetRefresh) error) (TargetSchedulableActionResult, schedulableActionReadback, error) {
	session, workspace, accountID, err := s.resolveManualSession(ctx, userID, targetID)
	if err != nil {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
	}
	actioner, ok := s.schedulableActions.(TargetSchedulableContextActioner)
	if session.Platform != upstream.PlatformSub2API || !ok {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorSchedulableUnsupported)
	}
	ctx, release, err := s.prepareManualAction(ctx, userID, workspace, targetID)
	if err != nil {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
	}
	defer release()
	refresh, err := s.refreshAdminTarget(ctx, session, workspace, accountID)
	if err != nil {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
	}
	target := refresh.target
	s.sub2APIFloorGuardFor(userID, workspace).rememberInventory(refresh.inventory)
	if !refresh.found || target.TargetID != targetID {
		if refresh.accountsReadError {
			return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorAccountsFetch)
		}
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorProbeTargetNotFound)
	}
	pair, err := s.reconcileActionObservation(ctx, targetObservation(userID, workspace, target, &refresh.inventory))
	if err != nil || pair.pendingCount() != 0 {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, errors.Join(err, ErrRemoteActionPending)
	}
	if extraCheck != nil {
		if err := extraCheck(ctx, refresh); err != nil {
			return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
		}
	}
	if !schedulable {
		if err := s.checkManualFloor(ctx, userID, workspace, TargetMutationSchedulable, target, &refresh.inventory); err != nil {
			_ = s.recordManualFloorBlockedEvent(ctx, userID, workspace, target, refresh.inventory, err, RemoteActionSkippedSub2APILastActive)
			return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
		}
	}
	state := manualTargetCheckpoint(pair, userID, workspace, target, TargetMutationSchedulable, ActionSourceManual, "", &schedulable, &refresh.inventory)
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}, Kind: ActionKindTarget, Target: &state}
	_, err = s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		returned, sendErr := actioner.SetSub2APIAdminAccountSchedulableContext(sendCtx, session, target.AccountID, schedulable)
		if sendErr == nil && (returned.ID != target.AccountID || returned.Schedulable == nil || *returned.Schedulable != schedulable) {
			sendErr = errors.New("schedulable response did not confirm intended account state")
		}
		return schedulableRemoteAction(schedulable), sendErr
	})
	if err != nil {
		_, _ = s.reconcileActionObservation(context.WithoutCancel(ctx), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
		_ = s.recordSchedulableActionEvent(ctx, userID, workspace, target, SchedulableActionFailed, ErrorSchedulableActionFailed, schedulableFailedRemoteAction(schedulable))
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorSchedulableActionFailed)
	}
	readbackStarted := time.Now()
	readbackTarget, account, found, _, readbackErr := s.readbackManualTarget(ctx, session, workspace, accountID, refresh.memberships)
	if readbackErr != nil || !found || readbackTarget.TargetID != targetID || account.Schedulable == nil || *account.Schedulable != schedulable {
		_ = s.recordSchedulableActionEvent(ctx, userID, workspace, target, SchedulableActionFailed, ErrorSchedulableReadbackFailed, schedulableFailedRemoteAction(schedulable))
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorSchedulableReadbackFailed)
	}
	_, err = s.reconcileActionObservation(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, InventoryComplete: adminInventoryComplete(refresh.inventory), Visible: true, SnapshotStartedAt: readbackStarted, Status: readbackTarget.AccountStatus, Schedulable: account.Schedulable})
	if err != nil {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, err
	}
	actionAt := time.Now().UTC()
	if err := s.recordSchedulableActionEvent(ctx, userID, workspace, readbackTarget, SchedulableActionSucceeded, "", schedulableRemoteAction(schedulable)); err != nil {
		return TargetSchedulableActionResult{}, schedulableActionReadback{}, requestError(ErrorSchedulableAuditFailed)
	}
	return TargetSchedulableActionResult{TargetID: targetID, Schedulable: schedulable, ActionSource: ActionSourceUser, ActionAt: actionAt}, schedulableActionReadback{account, readbackStarted}, nil
}

func schedulableRemoteAction(schedulable bool) string {
	if schedulable {
		return RemoteActionSchedulableEnabled
	}
	return RemoteActionSchedulableDisabled
}

func schedulableFailedRemoteAction(schedulable bool) string {
	if schedulable {
		return RemoteActionSchedulableEnableFailed
	}
	return RemoteActionSchedulableDisableFailed
}

func (s *Service) recordManualFloorBlockedEvent(ctx context.Context, userID, workspace string, target AdminProbeTarget, inventory adminWorkspaceInventory, err error, lastUsableAction string) error {
	errorKey, action := ErrorSub2APIGroupLastUsable, lastUsableAction
	switch {
	case errors.Is(err, ErrRemoteActionPending):
		errorKey, action = "admin.connectionHealth.errors.remoteActionPending", RemoteActionAwaitingConfirmation
	case errors.Is(err, requestError(ErrorSub2APIInventoryIncomplete)):
		errorKey, action = ErrorSub2APIInventoryIncomplete, RemoteActionSkippedSub2APIInventory
	}
	var blocked *RemoteActionBlockedError
	if errors.As(err, &blocked) && blocked.GroupID != "" {
		target.AdminGroupID, target.AdminGroupName = blocked.GroupID, blocked.GroupName
	} else if errorKey == ErrorSub2APIInventoryIncomplete {
		if id, name, incomplete := firstIncompleteAdminInventoryGroup(inventory); incomplete {
			target.AdminGroupID, target.AdminGroupName = id, name
		}
	}
	return s.recordSchedulableActionEvent(ctx, userID, workspace, target, SchedulableActionFailed, errorKey, action)
}

func (s *Service) recordSchedulableActionEvent(ctx context.Context, userID string, adminAccountID string, target AdminProbeTarget, result string, errorKey string, remoteAction string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	event := ConnectionHealthEvent{
		ID: id, ConnectionID: target.TargetID, ModelName: "*", UserID: userID, AdminAccountID: adminAccountID,
		AdminGroupID: target.AdminGroupID, OwnGroupName: target.AdminGroupName, UpstreamGroupName: target.AdminGroupName,
		Result: result, ErrorKey: errorKey, RemoteAction: remoteAction, ActionSource: ActionSourceUser, Source: EventSourceManual, CreatedAt: time.Now().UTC(),
	}
	return s.repo.InsertEvent(ctx, event)
}

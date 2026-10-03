package connection_health

import (
	"context"
	"errors"
	"sort"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type detachedActionLeaseKey struct{}

type RemoteActionBlockedError struct {
	Cause     error
	GroupID   string
	GroupName string
}

func (e *RemoteActionBlockedError) Error() string { return e.Cause.Error() }
func (e *RemoteActionBlockedError) Unwrap() error { return e.Cause }
func (e *RemoteActionBlockedError) SafeDeletionGroup() (string, string) {
	return e.GroupID, e.GroupName
}

func findActionInventoryTarget(targetID string, inventory adminWorkspaceInventory) (AdminProbeTarget, bool) {
	parsed, ok := parseTargetID(targetID)
	if !ok {
		return AdminProbeTarget{TargetID: targetID}, false
	}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == parsed.accountID {
				return AdminProbeTarget{TargetID: targetID, Platform: parsed.platform, AccountID: account.ID, AccountName: account.Name, AccountStatus: account.Status, Schedulable: account.Schedulable, AccountWeight: account.Weight, AdminGroupID: group.group.ID, AdminGroupName: group.group.Name}, true
			}
		}
	}
	return AdminProbeTarget{TargetID: targetID, AccountID: parsed.accountID, Platform: parsed.platform}, false
}

func inventoryTargetGroups(inventory *adminWorkspaceInventory, accountID string) []string {
	ids := []string{}
	if inventory == nil {
		return ids
	}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == accountID {
				ids = append(ids, group.group.ID)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// Persisted uncertainty protects both the original claim groups and current
// memberships. An incomplete current membership freezes the workspace.
func (s *Service) overlayPersistentPending(ctx context.Context, userID, workspace string, guard *workspaceFloorGuard, inventory adminWorkspaceInventory) error {
	targets, err := s.repo.ListTargetActionStates(ctx, userID, workspace)
	if err != nil {
		return err
	}
	priorities, err := s.repo.ListPrioritySyncStates(ctx, userID, workspace)
	if err != nil {
		return err
	}
	frozen := make(map[string]struct{})
	reserved := make(map[string]struct{})
	all := false
	protect := func(targetID string, claimed []string) {
		parsed, ok := parseTargetID(targetID)
		if !ok {
			all = true
			return
		}
		current := inventoryTargetGroups(&inventory, parsed.accountID)
		// No member row cannot distinguish deletion from lost group visibility.
		if !adminInventoryComplete(inventory) || len(current) == 0 {
			all = true
		}
		for _, id := range append(append([]string{}, claimed...), current...) {
			frozen[id] = struct{}{}
		}
		reserved[targetID] = struct{}{}
	}
	for _, state := range targets {
		if targetActionPending(&state) {
			protect(state.TargetID, state.PendingGroupIDs)
		}
	}
	for _, state := range priorities {
		if priorityActionPending(&state) {
			protect(state.TargetID, nil)
		}
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.frozenGroups, guard.freezeWorkspace = frozen, all
	for id := range reserved {
		guard.reservedUnavailable[id] = struct{}{}
	}
	return nil
}

func (s *Service) prepareManualAction(ctx context.Context, userID, workspace, targetID string) (context.Context, func(), error) {
	// Once accepted, an explicit command completes independently of its browser.
	prep, cancel := context.WithTimeout(context.WithValue(context.WithoutCancel(ctx), detachedActionLeaseKey{}, true), actionPreparationTimeout)
	leased, releaseTarget, acquired, err := s.acquireActionTargetLease(prep, targetID, true)
	if err != nil || !acquired {
		cancel()
		if err == nil {
			err = ErrRemoteActionLeaseLost
		}
		return ctx, nil, err
	}
	// The lease lifetime is detached; retain the preparation budget in the context
	// used for inventory, claim and permit.
	leased, deadlineCancel := context.WithDeadline(leased, mustContextDeadline(prep))
	leased, releaseMutation, err := s.acquireActionMutationLease(leased, userID, workspace)
	if err != nil {
		deadlineCancel()
		releaseTarget()
		cancel()
		return ctx, nil, err
	}
	leased, budgetCancel := context.WithDeadline(leased, mustContextDeadline(prep))
	return leased, func() { budgetCancel(); releaseMutation(); deadlineCancel(); releaseTarget(); cancel() }, nil
}
func mustContextDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

func manualTargetCheckpoint(pair RemoteActionCheckpoints, userID, workspace string, target AdminProbeTarget, kind, source string, status string, schedulable *bool, inventory *adminWorkspaceInventory) TargetActionState {
	state := TargetActionState{UserID: userID, AdminAccountID: workspace, TargetID: target.TargetID, OriginalStatus: normalizeTargetStatus(target.Platform, target.AccountStatus), LastAppliedStatus: normalizeTargetStatus(target.Platform, target.AccountStatus), OriginalWeight: cloneIntPointer(target.AccountWeight), LastAppliedWeight: cloneIntPointer(target.AccountWeight)}
	if pair.Target != nil {
		state = *pair.Target
		state.PendingHadAutomaticBaseline = true
	}
	state.PendingActionKind, state.PendingSource, state.PendingStatus, state.PendingSchedulable = kind, source, status, cloneBoolPointer(schedulable)
	state.PendingGroupIDs = inventoryTargetGroups(inventory, target.AccountID)
	return state
}

func (s *Service) checkManualFloor(ctx context.Context, userID, workspace, mutationKind string, target AdminProbeTarget, inventory *adminWorkspaceInventory) error {
	if inventory == nil {
		return requestError(ErrorSub2APIInventoryIncomplete)
	}
	// Closing an already unavailable account retains its idempotent exemption.
	// Deletion removes the resource itself, so it still requires complete
	// inventory and the persistent pending protection below.
	if (mutationKind == TargetMutationStatus || mutationKind == TargetMutationSchedulable) && inventoryTargetAlreadyUnavailable(*inventory, target.TargetID) {
		return nil
	}
	if !adminInventoryComplete(*inventory) {
		return requestError(ErrorSub2APIInventoryIncomplete)
	}
	guard := s.sub2APIFloorGuardFor(userID, workspace)
	if err := s.overlayPersistentPending(ctx, userID, workspace, guard, *inventory); err != nil {
		return err
	}
	scope, err := s.loadAdminMonitoringScope(ctx, userID, workspace, *inventory)
	if err != nil {
		return requestError(ErrorSub2APIInventoryIncomplete)
	}
	result := guard.reserveSub2APIMutation(target, *inventory, scope)
	if result.remoteAction == RemoteActionAwaitingConfirmation {
		return &RemoteActionBlockedError{Cause: ErrRemoteActionPending, GroupID: result.adminGroupID, GroupName: result.adminGroupName}
	}
	if result.remoteAction == RemoteActionSkippedSub2APIInventory {
		return &RemoteActionBlockedError{Cause: requestError(ErrorSub2APIInventoryIncomplete), GroupID: result.adminGroupID, GroupName: result.adminGroupName}
	}
	if result.remoteAction != "" {
		return &RemoteActionBlockedError{Cause: requestError(ErrorSub2APIGroupLastUsable), GroupID: result.adminGroupID, GroupName: result.adminGroupName}
	}
	return nil
}

type TargetSchedulableContextActioner interface {
	SetSub2APIAdminAccountSchedulableContext(context.Context, upstream.Session, string, bool) (upstream.AdminGroupAccountInfo, error)
}
type TargetDeleteContextActioner interface {
	DeleteSub2APIAdminAccountContext(context.Context, upstream.Session, string) error
}

// DeleteManagedSub2APIAccount is injected into my_sites. A nil return proves
// both a confirmed remote delete and a subsequent complete absence snapshot.
func (s *Service) DeleteManagedSub2APIAccount(ctx context.Context, userID, workspace string, session upstream.Session, accountID, source string) error {
	if session.Platform != upstream.PlatformSub2API {
		return requestError(ErrorSchedulableUnsupported)
	}
	actioner, ok := s.platformGroups.(TargetDeleteContextActioner)
	if !ok {
		return requestError(ErrorSchedulableUnsupported)
	}
	targetID := buildTargetID(string(session.Platform), workspace, accountID)
	ctx, release, err := s.prepareManualAction(ctx, userID, workspace, targetID)
	if err != nil {
		return err
	}
	defer release()
	inventory, err := s.loadAdminInventory(ctx, userID, workspace, adminInventoryCache{})
	if err != nil {
		return err
	}
	target, visible := findActionInventoryTarget(targetID, *inventory)
	pair, err := s.reconcileActionObservation(ctx, targetObservation(userID, workspace, target, inventory))
	if err != nil || pair.pendingCount() != 0 {
		return errors.Join(err, ErrRemoteActionPending)
	}
	if !visible {
		return requestError(ErrorProbeTargetNotFound)
	}
	if err := s.checkManualFloor(ctx, userID, workspace, TargetMutationDelete, target, inventory); err != nil {
		return err
	}
	if source != ActionSourceCompensateDelete {
		source = ActionSourceManualDelete
	}
	state := manualTargetCheckpoint(pair, userID, workspace, target, TargetMutationDelete, source, "", nil, inventory)
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}, Kind: ActionKindTarget, Target: &state}
	_, err = s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		return "sub2api_deleted", actioner.DeleteSub2APIAdminAccountContext(sendCtx, session, accountID)
	})
	if err != nil {
		_, _ = s.reconcileActionObservation(context.WithoutCancel(ctx), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
		return err
	}
	// Use the same bounded preparation context for inventory confirmation, while
	// the receipt itself already had a separate budget.
	fresh, err := s.loadAdminInventory(ctx, userID, workspace, adminInventoryCache{})
	if err != nil {
		return err
	}
	observed, _ := findActionInventoryTarget(targetID, *fresh)
	pair, err = s.reconcileActionObservation(ctx, targetObservation(userID, workspace, observed, fresh))
	if err != nil || pair.pendingCount() != 0 {
		return errors.Join(err, ErrRemoteActionPending)
	}
	return nil
}

func (s *Service) runCompatibilityStatusAction(ctx context.Context, userID, workspace string, session upstream.Session, accountID, desired string) (string, error) {
	targetID := buildTargetID(string(upstream.PlatformSub2API), workspace, accountID)
	ctx, release, err := s.prepareManualAction(ctx, userID, workspace, targetID)
	if err != nil {
		return "", err
	}
	defer release()
	inventory, err := s.loadAdminInventory(ctx, userID, workspace, adminInventoryCache{})
	if err != nil {
		blocked := AdminProbeTarget{TargetID: targetID, Platform: string(session.Platform), AccountID: accountID}
		_ = s.recordSchedulableActionEvent(ctx, userID, workspace, blocked, SchedulableActionFailed, ErrorSub2APIInventoryIncomplete, RemoteActionSkippedSub2APIInventory)
		return "", requestError(ErrorSub2APIInventoryIncomplete)
	}
	target, visible := findActionInventoryTarget(targetID, *inventory)
	pair, err := s.reconcileActionObservation(ctx, targetObservation(userID, workspace, target, inventory))
	if err != nil || pair.pendingCount() != 0 {
		return "", errors.Join(err, ErrRemoteActionPending)
	}
	if !visible {
		_ = s.recordSchedulableActionEvent(ctx, userID, workspace, target, SchedulableActionFailed, ErrorSub2APIInventoryIncomplete, RemoteActionSkippedSub2APIInventory)
		return "", requestError(ErrorSub2APIInventoryIncomplete)
	}
	if desired == "inactive" {
		if err := s.checkManualFloor(ctx, userID, workspace, TargetMutationStatus, target, inventory); err != nil {
			errorKey, action := ErrorSub2APIGroupLastUsable, RemoteActionSkippedSub2APILastUsable
			if err.Error() == ErrorSub2APIInventoryIncomplete {
				errorKey, action = ErrorSub2APIInventoryIncomplete, RemoteActionSkippedSub2APIInventory
			}
			blocked := target
			if errorKey == ErrorSub2APIInventoryIncomplete {
				if id, name, incomplete := firstIncompleteAdminInventoryGroup(*inventory); incomplete {
					blocked.AdminGroupID, blocked.AdminGroupName = id, name
				}
			}
			_ = s.recordSchedulableActionEvent(ctx, userID, workspace, blocked, SchedulableActionFailed, errorKey, action)
			return "", err
		}
	}
	state := manualTargetCheckpoint(pair, userID, workspace, target, TargetMutationStatus, ActionSourceManual, desired, nil, inventory)
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}, Kind: ActionKindTarget, Target: &state}
	action, err := s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		return s.dispatcher.ApplyTargetState(sendCtx, session, target, nil, desired)
	})
	if err != nil {
		_, _ = s.reconcileActionObservation(context.WithoutCancel(ctx), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
		return action, err
	}
	readbackStarted := time.Now()
	memberships := []adminTargetMembership{}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == accountID {
				memberships = append(memberships, adminTargetMembership{groupID: group.group.ID, groupName: group.group.Name})
				break
			}
		}
	}
	observed, account, found, _, err := s.readbackManualTarget(ctx, session, workspace, accountID, memberships)
	if err != nil || !found || normalizeTargetStatus(target.Platform, account.Status) != desired {
		return action, requestError(ErrorSchedulableReadbackFailed)
	}
	_, err = s.reconcileActionObservation(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, InventoryComplete: adminInventoryComplete(*inventory), Visible: true, SnapshotStartedAt: readbackStarted, Status: observed.AccountStatus, Schedulable: observed.Schedulable})
	if err != nil {
		return action, err
	}
	return action, nil
}

package connection_health

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	AccountEditSuccess            = "success"
	AccountEditNotSent            = "not_sent"
	AccountEditPending            = "pending"
	AccountEditNoop               = "noop"
	PriorityManualSetAction       = "sub2api_priority_manual_set"
	PriorityManualSetFailedAction = "sub2api_priority_manual_set_failed"
	ConcurrencySetAction          = "sub2api_concurrency_set"
	ConcurrencySetFailedAction    = "sub2api_concurrency_set_failed"
)

type TargetPriorityOwnerInput struct {
	Mode     string `json:"mode"`
	Priority *int   `json:"priority,omitempty"`
}
type TargetAccountEditResult struct {
	TargetID    string `json:"targetId"`
	Result      string `json:"result"`
	Priority    *int   `json:"priority,omitempty"`
	Concurrency *int   `json:"concurrency,omitempty"`
	LoadFactor  *int   `json:"loadFactor,omitempty"`
	ErrorKey    string `json:"errorKey,omitempty"`
}
type TargetConcurrencyContextActioner interface {
	UpdateSub2APIAdminAccountConcurrencyContext(context.Context, upstream.Session, string, int, *int) error
}

func (s *Service) SetTargetPriorityOwner(ctx context.Context, user, targetID string, input TargetPriorityOwnerInput) (TargetAccountEditResult, error) {
	targetID, input.Mode = strings.TrimSpace(targetID), strings.TrimSpace(input.Mode)
	if (input.Mode != "manual" && input.Mode != "auto") || (input.Mode == "manual" && (input.Priority == nil || *input.Priority < 1 || *input.Priority > 9)) || (input.Mode == "auto" && input.Priority != nil) {
		return TargetAccountEditResult{}, requestError(ErrorRequest)
	}
	if _, err := s.accountTierWorkspace(ctx, user, targetID); err != nil {
		return TargetAccountEditResult{}, err
	}
	session, workspace, accountID, err := s.resolveManualSession(ctx, user, targetID)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	if session.Platform != upstream.PlatformSub2API || s.priorityActions == nil {
		return TargetAccountEditResult{}, requestError(ErrorPrioritySyncUnavailable)
	}
	wait, cancelWait := context.WithTimeout(context.WithValue(ctx, detachedActionLeaseKey{}, true), 5*time.Second)
	leased, releaseWorkspace, acquired, err := s.acquireActionLease(wait, priorityRuntimeLeaseKey(user, workspace), true)
	cancelWait()
	if err != nil || !acquired {
		return TargetAccountEditResult{}, requestError(ErrorPrioritySyncBusy)
	}
	workspaceLease := actionLeaseFromContext(leased)
	leased = context.WithValue(leased, workspacePriorityLeaseContextKey{}, workspaceLease)
	wake := false
	defer func() {
		releaseWorkspace()
		if wake {
			s.triggerHealthPrioritySyncAfterCommit(user, workspace)
		}
	}()
	ctx, release, err := s.prepareManualAction(leased, user, workspace, targetID)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	defer release()
	ctx, cancelLost := context.WithCancel(ctx)
	stopWorkspace := context.AfterFunc(workspaceLease.Context, cancelLost)
	defer func() { stopWorkspace(); cancelLost() }()
	inventory, err := s.loadAdminInventory(ctx, user, workspace, adminInventoryCache{})
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	if !adminInventoryComplete(*inventory) {
		return TargetAccountEditResult{}, requestError(ErrorPriorityInventoryIncomplete)
	}
	target, visible := findActionInventoryTarget(targetID, *inventory)
	account, found := findSettingsInventoryAccount(accountID, *inventory)
	if !visible || !found {
		return TargetAccountEditResult{}, requestError(ErrorProbeTargetNotFound)
	}
	if account.Priority == nil {
		return TargetAccountEditResult{}, requestError(ErrorPriorityInventoryIncomplete)
	}
	target.InventoryComplete, target.Models = true, splitModelList(account.Models)
	pair, err := s.reconcileActionObservation(ctx, targetObservation(user, workspace, target, inventory))
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	if pair.pendingCount() != 0 {
		return TargetAccountEditResult{}, ErrRemoteActionPending
	}
	settings, err := s.workspaceHealthSettings(ctx, user, workspace)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	target.ConfigGeneration, target.ConfigGenerationKnown = settings.ConfigGeneration, true
	decision := ManualPriorityOwnerDecision{RemoteActionScope: RemoteActionScope{user, workspace, targetID}, ExpectedConfigGeneration: settings.ConfigGeneration, Target: target, GroupIDs: inventoryTargetGroups(inventory, accountID), CurrentPriority: *account.Priority, Mode: input.Mode}
	if input.Priority != nil {
		decision.Priority = *input.Priority
	}
	// The precheck is repeated from transaction-owned reads by Prepare below.
	policies, err := s.repo.ListPolicies(ctx, user, workspace)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	direct, err := s.repo.ListPolicyAssignmentsByWorkspace(ctx, user, workspace)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	groups, err := s.repo.ListGroupPolicyAssignmentsByWorkspace(ctx, user, workspace)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	exclusions, err := s.repo.ListGroupTargetExclusionsByWorkspace(ctx, user, workspace)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	effective := manualPriorityPolicies(decision, policies, direct, groups, exclusions)
	if err := validateManualPriorityOwner(decision, effective); err != nil {
		return TargetAccountEditResult{}, err
	}
	if *account.Priority >= 1 && *account.Priority <= 9 && pair.Priority != nil && !oldPriorityComparisonBaseline(pair.Priority) {
		if err := s.mutateIdlePriority(ctx, target, user, workspace, pair.Priority, nil); err != nil {
			return TargetAccountEditResult{}, err
		}
		pair.Priority = nil
	}
	repository, ok := s.repo.(manualPriorityOwnerRepository)
	if !ok {
		return TargetAccountEditResult{}, requestError(ErrorPrioritySyncUnavailable)
	}
	prepared, err := repository.PrepareManualPriorityOwner(ctx, decision)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	result := TargetAccountEditResult{TargetID: targetID, Result: AccountEditNoop, Priority: cloneIntPointer(account.Priority)}
	if prepared.Noop {
		return result, nil
	}
	state := PrioritySyncState{UserID: user, AdminAccountID: workspace, TargetID: targetID, OriginalPriority: *account.Priority}
	if pair.Priority != nil {
		state = *pair.Priority
	}
	state.PendingPriority = intPointerValue(prepared.Priority)
	claim := RemoteActionClaim{RemoteActionScope: decision.RemoteActionScope, Kind: ActionKindPriority, Priority: &state, Guard: RemoteActionHealthGuard{ConfigGeneration: &prepared.ConfigGeneration}, ClearPriorityConflict: true, ReleasePriorityOnConfirm: input.Mode == "manual", ExpectedPriorityCheckpoint: pair.Priority}
	outcome := upstream.MutationUncertain
	_, dispatchErr := s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		writeErr := s.updateAdminTargetPriority(sendCtx, session, accountID, prepared.Priority)
		outcome = upstream.RemoteMutationOutcome(writeErr)
		return "priority", writeErr
	})
	if outcome == upstream.MutationUncertain && (errors.Is(dispatchErr, ErrRemoteActionLeaseLost) || errors.Is(dispatchErr, ErrRemoteActionEvidenceChanged)) {
		outcome = upstream.MutationNotSent
	}
	result.Result, result.ErrorKey = AccountEditPending, ErrorPriorityWriteFailed
	if outcome == upstream.MutationNotSent || outcome == upstream.MutationConfirmedRejected {
		closed, closeErr := s.reconcileActionObservation(context.WithoutCancel(ctx), RemoteActionObservation{RemoteActionScope: decision.RemoteActionScope})
		if closeErr == nil && closed.pendingCount() == 0 {
			result.Result = AccountEditNotSent
			result.ErrorKey = accountEditErrorKey(dispatchErr, ErrorPriorityWriteFailed)
		}
	} else if dispatchErr == nil && outcome == upstream.MutationConfirmedApplied {
		fresh, readErr := s.loadAdminInventory(ctx, user, workspace, adminInventoryCache{})
		if readErr == nil {
			observed, _ := findActionInventoryTarget(targetID, *fresh)
			closed, closeErr := s.reconcileActionObservation(ctx, targetObservation(user, workspace, observed, fresh))
			readback, readable := findSettingsInventoryAccount(accountID, *fresh)
			if closeErr == nil && closed.pendingCount() == 0 && adminInventoryComplete(*fresh) && readable && readback.Priority != nil && *readback.Priority == prepared.Priority {
				result.Result, result.ErrorKey = AccountEditSuccess, ""
				result.Priority = cloneIntPointer(readback.Priority)
				wake = true
			}
		}
	}
	if err := s.recordAccountEditEvent(context.WithoutCancel(ctx), user, workspace, target, result, input.Mode); err != nil {
		log.Printf("[connection-health] priority account edit audit persistence failed target_id=%s result=%s", targetID, result.Result)
	}
	return result, nil
}

func (s *Service) SetTargetConcurrency(ctx context.Context, user, targetID string, concurrency int) (TargetAccountEditResult, error) {
	if concurrency < 1 || concurrency > 1000 {
		return TargetAccountEditResult{}, requestError(ErrorRequest)
	}
	targetID = strings.TrimSpace(targetID)
	if _, err := s.accountTierWorkspace(ctx, user, targetID); err != nil {
		return TargetAccountEditResult{}, err
	}
	session, workspace, accountID, err := s.resolveManualSession(ctx, user, targetID)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	actioner := s.concurrencyActions
	if actioner == nil {
		actioner, _ = s.platformGroups.(TargetConcurrencyContextActioner)
	}
	if session.Platform != upstream.PlatformSub2API || actioner == nil {
		return TargetAccountEditResult{}, requestError(ErrorConcurrencyUnsupported)
	}
	ctx, release, err := s.prepareManualAction(ctx, user, workspace, targetID)
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	defer release()
	inventory, err := s.loadAdminInventory(ctx, user, workspace, adminInventoryCache{})
	if err != nil {
		return TargetAccountEditResult{}, err
	}
	if !adminInventoryComplete(*inventory) {
		return TargetAccountEditResult{}, requestError(ErrorPriorityInventoryIncomplete)
	}
	target, visible := findActionInventoryTarget(targetID, *inventory)
	account, found := findSettingsInventoryAccount(accountID, *inventory)
	if !visible || !found {
		return TargetAccountEditResult{}, requestError(ErrorProbeTargetNotFound)
	}
	result := TargetAccountEditResult{TargetID: targetID, Result: AccountEditPending, Concurrency: cloneIntPointer(account.Concurrency), LoadFactor: cloneIntPointer(account.LoadFactor), ErrorKey: ErrorConcurrencyUnconfirmed}
	var load *int
	if account.LoadFactor != nil && *account.LoadFactor > 0 {
		load = intPointerValue(concurrency)
	}
	handle, mutation := actionLeaseFromContext(ctx), mutationLeaseFromContext(ctx)
	if handle == nil || handle.Context.Err() != nil || mutation == nil || mutation.Context.Err() != nil {
		return TargetAccountEditResult{}, ErrRemoteActionLeaseLost
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	stopTarget, stopMutation := context.AfterFunc(handle.Context, cancel), context.AfterFunc(mutation.Context, cancel)
	writeErr := actioner.UpdateSub2APIAdminAccountConcurrencyContext(sendCtx, session, accountID, concurrency, load)
	cancel()
	stopTarget()
	stopMutation()
	switch upstream.RemoteMutationOutcome(writeErr) {
	case upstream.MutationNotSent, upstream.MutationConfirmedRejected:
		result.Result = AccountEditNotSent
		result.ErrorKey = accountEditErrorKey(writeErr, ErrorConcurrencyUnconfirmed)
	case upstream.MutationConfirmedApplied:
		fresh, err := s.loadAdminInventory(ctx, user, workspace, adminInventoryCache{})
		if err == nil && adminInventoryComplete(*fresh) {
			readback, found := findSettingsInventoryAccount(accountID, *fresh)
			if found && readback.Concurrency != nil && *readback.Concurrency == concurrency && (load == nil || (readback.LoadFactor != nil && *readback.LoadFactor == *load)) {
				result.Result, result.ErrorKey = AccountEditSuccess, ""
				result.Concurrency = cloneIntPointer(readback.Concurrency)
				result.LoadFactor = cloneIntPointer(readback.LoadFactor)
			}
		}
	}
	if err := s.recordAccountEditEvent(context.WithoutCancel(ctx), user, workspace, target, result, ""); err != nil {
		log.Printf("[connection-health] concurrency account edit audit persistence failed target_id=%s result=%s", targetID, result.Result)
	}
	return result, nil
}

func findSettingsInventoryAccount(accountID string, inventory adminWorkspaceInventory) (upstream.AdminGroupAccountInfo, bool) {
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID == accountID {
				return account, true
			}
		}
	}
	return upstream.AdminGroupAccountInfo{}, false
}
func intPointerValue(value int) *int { return &value }
func accountEditErrorKey(err error, fallback string) string {
	var request requestError
	if errors.As(err, &request) {
		return request.Error()
	}
	var remote *upstream.RequestError
	if errors.As(err, &remote) && remote.MessageKey != "" {
		return remote.MessageKey
	}
	return fallback
}
func (s *Service) recordAccountEditEvent(ctx context.Context, user, workspace string, target AdminProbeTarget, result TargetAccountEditResult, mode string) error {
	action := PriorityManualSetAction
	if result.Concurrency != nil || mode == "" {
		action = ConcurrencySetAction
	}
	if result.Result != AccountEditSuccess {
		if action == PriorityManualSetAction {
			action = PriorityManualSetFailedAction
		} else {
			action = ConcurrencySetFailedAction
		}
	}
	id, err := newID()
	if err != nil {
		return err
	}
	detail := result.Result
	if mode != "" {
		detail = mode + ":" + result.Result
	}
	return s.repo.InsertEvent(ctx, ConnectionHealthEvent{ID: id, ConnectionID: target.TargetID, ModelName: "*", UserID: user, AdminAccountID: workspace, AdminGroupID: target.AdminGroupID, OwnGroupName: target.AdminGroupName, UpstreamGroupName: target.AdminGroupName, Result: "account_edit_" + result.Result, ErrorKey: result.ErrorKey, ErrorDetail: detail, RemoteAction: action, ActionSource: ActionSourceUser, Source: EventSourceManual, CreatedAt: time.Now().UTC()})
}

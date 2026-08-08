package connection_health

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const safetyQueuePollInterval = 250 * time.Millisecond

func (s *Service) runSafetyQueueWorker(ctx context.Context, workerIndex int) {
	workerID, err := newID()
	if err != nil {
		log.Printf("[connection-health] create safety worker id failed index=%d err=%v", workerIndex, err)
		return
	}
	ticker := time.NewTicker(safetyQueuePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			item, claimErr := s.safetyRepo.ClaimAbnormalQueueItem(ctx, workerID, s.schedulerNow())
			if claimErr != nil {
				log.Printf("[connection-health] claim safety queue failed worker=%s err=%v", workerID, claimErr)
				continue
			}
			if item == nil {
				continue
			}
			stopHeartbeat := s.startSafetyQueueClaimHeartbeat(ctx, item.ID, workerID)
			func() {
				defer stopHeartbeat()
				if processErr := s.processSafetyQueueItem(ctx, workerID, *item); processErr != nil && !errors.Is(processErr, context.Canceled) {
					log.Printf("[connection-health] process safety queue failed worker=%s item=%s kind=%s err=%v", workerID, item.ID, item.Kind, processErr)
				}
			}()
		}
	}
}

func (s *Service) startSafetyQueueClaimHeartbeat(ctx context.Context, itemID, workerID string) func() {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(safetyQueueClaimTTL / 4)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				heartbeatCtx, cancel := context.WithTimeout(context.Background(), mutationLeaseQueryTimeout)
				err := s.safetyRepo.HeartbeatAbnormalQueueClaim(heartbeatCtx, itemID, workerID)
				cancel()
				if err != nil {
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (s *Service) processSafetyQueueItem(ctx context.Context, workerID string, item AbnormalQueueItem) error {
	if item.State == QueueStateDispatching {
		return s.recoverUncertainSafetyDispatch(ctx, workerID, item)
	}
	currentEpoch, err := s.safetyRepo.GetAbnormalQueueEpoch(ctx, item.UserID, item.WorkspaceID)
	if err != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "epoch_read_failed")
	}
	if currentEpoch != item.QueueEpoch {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_abnormal_queue_epoch")
	}
	if item.Kind != QueueKindCanary {
		currentGeneration, generationErr := s.safetyRepo.MutationGeneration(ctx, item.UserID, item.WorkspaceID, item.AccountID)
		if generationErr != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "generation_read_failed")
		}
		if currentGeneration != item.MutationGeneration {
			return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_manual_generation")
		}
	}
	if item.Kind != QueueKindCanary {
		incident, circuitErr := s.safetyRepo.GetIncidentCircuit(ctx, item.UserID, item.WorkspaceID, item.FaultDomain)
		if circuitErr != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "circuit_read_failed")
		}
		if incident != nil && incident.State != CircuitClosed {
			return s.cancelClaimedSafetyItem(ctx, workerID, item, "circuit_open")
		}
	}
	settings, ok := s.workspaceSafetySettings(ctx, item.UserID, item.WorkspaceID)
	if !ok {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "safety_settings_unavailable")
	}
	releaseSlot, acquired := s.sharedProbeLimiter().acquireAutomatic(ctx, item.UserID+"|"+item.WorkspaceID, false, settings.ManualReservedSlots)
	if !acquired {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, safetyQueuePollInterval, "probe_capacity_busy")
	}
	defer releaseSlot()

	switch item.Kind {
	case QueueKindConfirmation, QueueKindCanary:
		return s.processSafetyProbeItem(ctx, workerID, item)
	case QueueKindStatusIntent:
		return s.processSafetyStatusIntent(ctx, workerID, item)
	case QueueKindPriorityIntent:
		return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, "incident_priority_not_required")
	default:
		return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, "unknown_queue_kind")
	}
}

func (s *Service) requeueClaimedSafetyItem(ctx context.Context, workerID string, item AbnormalQueueItem, delay time.Duration, reason string) error {
	item.NextAttemptAt = s.schedulerNow().Add(delay)
	item.LastResult = reason
	return s.safetyRepo.RequeueAbnormalQueueItem(ctx, item, workerID)
}

func (s *Service) cancelClaimedSafetyItem(ctx context.Context, workerID string, item AbnormalQueueItem, reason string) error {
	if item.Kind == QueueKindStatusIntent {
		if err := s.clearIncidentTargetActionIfOwned(ctx, item); err != nil {
			return err
		}
	}
	return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, reason)
}

func (s *Service) rescheduleDispatchedSafetyItem(ctx context.Context, workerID string, item AbnormalQueueItem, delay time.Duration, reason string) error {
	item.NextAttemptAt = s.schedulerNow().Add(delay)
	item.LastResult = reason
	return s.safetyRepo.RescheduleDispatchedAbnormalQueueItem(ctx, item, workerID)
}

func (s *Service) rescheduleUncertainStatusDispatch(ctx context.Context, workerID string, item AbnormalQueueItem, delay time.Duration, reason string) error {
	item.NextAttemptAt = s.schedulerNow().Add(delay)
	item.LastResult = reason
	return s.safetyRepo.RescheduleUncertainStatusDispatch(ctx, item, workerID)
}

func (s *Service) safetySnapshotTarget(userID, workspaceID, targetID string) (upstream.Session, AdminProbeTarget, upstream.AdminGroupAccountInfo, bool) {
	snapshot, valid := s.getAdminInventorySnapshot(userID, workspaceID, s.schedulerNow())
	if !valid || snapshot.inventory == nil || !snapshot.inventory.complete || snapshot.inventory.session.Platform != upstream.PlatformSub2API {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, false
	}
	parsed, ok := parseTargetID(targetID)
	if !ok || parsed.adminAccountID != workspaceID || parsed.platform != string(snapshot.inventory.session.Platform) {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, false
	}
	var target AdminProbeTarget
	var account upstream.AdminGroupAccountInfo
	found := false
	models := make(map[string]struct{})
	groups := make([]adminTargetMembership, 0)
	seenGroups := make(map[string]struct{})
	for _, group := range snapshot.inventory.groups {
		if group.err != nil {
			return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, false
		}
		for _, candidate := range group.accounts {
			if candidate.ID != parsed.accountID {
				continue
			}
			if !found {
				account = candidate
				target = AdminProbeTarget{
					TargetID: targetID, Platform: parsed.platform, AccountID: candidate.ID,
					AccountName: candidate.Name, AccountStatus: candidate.Status,
					Schedulable:   cloneBoolPointer(candidate.Schedulable),
					AccountWeight: cloneIntPointer(candidate.Weight), ProviderFamily: candidate.Platform,
				}
				found = true
			}
			for _, model := range splitModelList(candidate.Models) {
				models[model] = struct{}{}
			}
			if _, seen := seenGroups[group.group.ID]; !seen {
				seenGroups[group.group.ID] = struct{}{}
				groups = append(groups, adminTargetMembership{groupID: group.group.ID, groupName: group.group.Name})
			}
		}
	}
	if !found {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, false
	}
	for model := range models {
		target.Models = append(target.Models, model)
	}
	sort.Strings(target.Models)
	if len(groups) > 0 {
		target.AdminGroupID = groups[0].groupID
		target.AdminGroupName = groups[0].groupName
	}
	return snapshot.inventory.session, target, account, true
}

func (s *Service) processSafetyProbeItem(ctx context.Context, workerID string, item AbnormalQueueItem) error {
	session, target, account, found := s.safetySnapshotTarget(item.UserID, item.WorkspaceID, item.TargetID)
	if !found {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "inventory_snapshot_unavailable")
	}
	if item.Kind == QueueKindCanary {
		incident, err := s.safetyRepo.GetIncidentCircuit(ctx, item.UserID, item.WorkspaceID, item.FaultDomain)
		if err != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "circuit_read_failed")
		}
		if incident == nil || incident.ID != item.IncidentID || incident.State == CircuitClosed || incident.CanaryTargetID != item.TargetID {
			return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, "stale_canary")
		}
		if len(target.Models) == 0 {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "canary_model_inventory_unknown")
		}
		if !containsSafetyValue(target.Models, item.ModelName) {
			item.ModelName = target.Models[0]
		}
	}
	cred, err := s.resolveProbeCredential(ctx, session, account)
	if err != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "credential_unavailable")
	}
	currentEpoch, err := s.safetyRepo.GetAbnormalQueueEpoch(ctx, item.UserID, item.WorkspaceID)
	if err != nil || currentEpoch != item.QueueEpoch {
		if err != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "epoch_read_failed")
		}
		return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, "stale_abnormal_queue_epoch")
	}
	if item.Kind != QueueKindCanary {
		incident, err := s.safetyRepo.GetIncidentCircuit(ctx, item.UserID, item.WorkspaceID, confirmationFaultDomain(cred.BaseURL, target, ResultKey(item.ExpectedResult)))
		if err != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "circuit_read_failed")
		}
		if incident != nil && incident.State != CircuitClosed {
			return s.safetyRepo.CancelAbnormalQueueItem(ctx, item.ID, workerID, "circuit_open")
		}
	}
	dispatching, err := s.safetyRepo.MarkAbnormalQueueDispatching(ctx, item.ID, workerID, item.QueueEpoch)
	if err != nil {
		return err
	}
	if !dispatching {
		return nil
	}
	providerFamily := item.ProviderFamily
	if item.Kind == QueueKindCanary && target.ProviderFamily != "" {
		providerFamily = target.ProviderFamily
	}
	outcome := s.probeRunner.Probe(ctx, ProbeRequest{
		BaseURL: cred.BaseURL, UpstreamKey: cred.Key, ProviderFamily: providerFamily,
		ModelName: item.ModelName, MaxTokens: item.MaxProbeTokens, ProbePrompt: item.ProbePrompt,
	})
	currentEpoch, err = s.safetyRepo.GetAbnormalQueueEpoch(ctx, item.UserID, item.WorkspaceID)
	if err != nil || currentEpoch != item.QueueEpoch {
		if epochRepo, ok := s.repo.(epochObservationRepository); ok {
			_ = epochRepo.InsertSafetyAudit(ctx, item.UserID, item.WorkspaceID,
				"stale_confirmation_response", item.TargetID+":"+item.ModelName+":"+string(outcome.Result))
		}
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_abnormal_queue_epoch")
	}
	if item.Kind == QueueKindCanary {
		return s.finishSafetyCanary(ctx, workerID, item, outcome)
	}
	return s.finishSafetyConfirmation(ctx, workerID, item, outcome)
}

func safetyRetryDelay(outcome ProbeOutcome) time.Duration {
	if outcome.RetryAfterSeconds > 0 {
		return time.Duration(outcome.RetryAfterSeconds) * time.Second
	}
	return 30 * time.Second
}

func safetyStatusRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << minInt(attempt, 5)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (s *Service) finishSafetyConfirmation(ctx context.Context, workerID string, item AbnormalQueueItem, outcome ProbeOutcome) error {
	if outcome.Result == ResultRateLimited {
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyRetryDelay(outcome), string(ResultRateLimited))
	}
	if outcome.Result == ResultOK || outcome.Result == ResultSlowResponse {
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "confirmation_recovered")
	}
	if string(outcome.Result) != item.ExpectedResult {
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "confirmation_result_changed:"+string(outcome.Result))
	}
	item.Attempt++
	item.LastResult = string(outcome.Result)
	if item.Attempt < item.RequiredAttempts {
		settings := SafetySettings{
			ConfirmationObservationCount: item.RequiredAttempts,
			ConfirmationDelaysSeconds:    append([]int(nil), item.ConfirmationDelays...),
			ConfirmationJitterSeconds:    item.ConfirmationJitter,
		}
		delay := confirmationDelay(settings, item.Attempt, nil)
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, delay, string(outcome.Result))
	}
	if outcome.Result == ResultAuth {
		if err := s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "confirmed:"+string(outcome.Result)); err != nil {
			return err
		}
		return s.recordSafetyGuardHeld(ctx, item, "authentication_failure_not_account_health")
	}
	if outcome.Result == ResultModelNotFound || outcome.Result == ResultInvalidResponse {
		if err := s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "confirmed:"+string(outcome.Result)); err != nil {
			return err
		}
		return s.recordSafetyGuardHeld(ctx, item, "model_or_request_specific_failure")
	}
	// Persist the destructive intent while the confirmation item is still
	// dispatching. If admission fails, retain the item for a bounded retry so a
	// transient database/queue failure cannot silently lose the incident.
	if err := s.enqueueConfirmedStatusIntent(ctx, item); err != nil {
		item.Attempt++
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "status_intent_enqueue_failed")
	}
	return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "confirmed:"+string(outcome.Result))
}

func (s *Service) finishSafetyCanary(ctx context.Context, workerID string, item AbnormalQueueItem, outcome ProbeOutcome) error {
	if outcome.Result == ResultRateLimited {
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyRetryDelay(outcome), string(ResultRateLimited))
	}
	succeeded := outcome.Result == ResultOK || outcome.Result == ResultSlowResponse
	_, err := s.safetyRepo.AdvanceIncidentCanary(ctx, item, succeeded, "", s.schedulerNow())
	if err != nil {
		if errors.Is(err, errStaleMutation) {
			return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "canary_stale")
		}
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, time.Second, "canary_transition_failed")
	}
	if err := s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "canary:"+string(outcome.Result)); err != nil {
		return err
	}
	return nil
}

func (s *Service) enqueueConfirmedStatusIntent(ctx context.Context, confirmation AbnormalQueueItem) error {
	settings, ok := s.workspaceSafetySettings(ctx, confirmation.UserID, confirmation.WorkspaceID)
	if !ok {
		return errors.New("safety settings unavailable")
	}
	intent := confirmation
	intent.ID = ""
	intent.Kind = QueueKindStatusIntent
	intent.ActionKey = fmt.Sprintf(
		"status:%s:inactive:%d:%d",
		confirmation.TargetID,
		confirmation.QueueEpoch,
		confirmation.MutationGeneration,
	)
	intent.Attempt = 0
	intent.RequiredAttempts = 0
	intent.NextAttemptAt = s.schedulerNow()
	intent.State = QueueStateQueued
	intent.ClaimedBy = ""
	intent.ClaimExpiresAt = nil
	intent.ExpectedResult = "inactive"
	intent.LastResult = "confirmed_failure"
	queued, _, err := s.safetyRepo.EnqueueAbnormalQueueItem(ctx, intent, settings.AbnormalQueueCapacity)
	if err != nil {
		return err
	}
	if queued.State == QueueStateGuardHeld {
		return s.recordSafetyGuardHeld(ctx, confirmation, "abnormal_queue_capacity")
	}
	return nil
}

func (s *Service) safetyTargetMemberships(userID, workspaceID, targetID string) ([]string, bool) {
	snapshot, valid := s.getAdminInventorySnapshot(userID, workspaceID, s.schedulerNow())
	if !valid || snapshot.inventory == nil || !snapshot.inventory.complete {
		return nil, false
	}
	groups := make(map[string]struct{})
	for _, group := range snapshot.inventory.groups {
		if group.err != nil {
			return nil, false
		}
		for _, account := range group.accounts {
			if buildTargetID(string(snapshot.inventory.session.Platform), workspaceID, account.ID) == targetID {
				groups[group.group.ID] = struct{}{}
				break
			}
		}
	}
	if len(groups) == 0 {
		return nil, false
	}
	values := make([]string, 0, len(groups))
	for groupID := range groups {
		values = append(values, groupID)
	}
	sort.Strings(values)
	return values, true
}

func (s *Service) safetyControlledScopes(ctx context.Context, userID, workspaceID, targetID string) ([]string, []string, error) {
	policies, err := s.repo.ListPolicies(ctx, userID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	groupAssignments, err := s.repo.ListGroupPolicyAssignmentsByWorkspace(ctx, userID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	directAssignments, err := s.repo.ListPolicyAssignmentsByWorkspace(ctx, userID, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	policyByID := make(map[string]Policy, len(policies))
	for _, policy := range policies {
		if policy.Enabled && policyRemoteActionEnabled(policy) {
			policyByID[policy.ID] = policy
		}
	}
	groupSet := make(map[string]struct{})
	policySet := make(map[string]struct{})
	for _, assignment := range groupAssignments {
		if _, ok := policyByID[assignment.PolicyID]; !ok {
			continue
		}
		groupSet[assignment.AdminGroupID] = struct{}{}
		policySet[assignment.PolicyID] = struct{}{}
	}
	memberships := make(map[string][]string)
	for _, assignment := range directAssignments {
		if _, ok := policyByID[assignment.PolicyID]; !ok {
			continue
		}
		groupsForTarget, loaded := memberships[assignment.TargetID]
		if !loaded {
			var complete bool
			groupsForTarget, complete = s.safetyTargetMemberships(userID, workspaceID, assignment.TargetID)
			if !complete {
				return nil, nil, errors.New("controlled target membership snapshot unavailable")
			}
			memberships[assignment.TargetID] = groupsForTarget
		}
		for _, groupID := range groupsForTarget {
			groupSet[groupID] = struct{}{}
		}
		policySet[assignment.PolicyID] = struct{}{}
	}
	if _, complete := s.safetyTargetMemberships(userID, workspaceID, targetID); !complete {
		return nil, nil, errors.New("target membership snapshot unavailable")
	}
	modelSet := make(map[string]struct{})
	for policyID := range policySet {
		for _, target := range policyByID[policyID].ModelTargets {
			if target.Enabled && strings.TrimSpace(target.ModelName) != "" {
				modelSet[strings.TrimSpace(target.ModelName)] = struct{}{}
			}
		}
	}
	groups := make([]string, 0, len(groupSet))
	for groupID := range groupSet {
		groups = append(groups, groupID)
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(groups)
	sort.Strings(models)
	return groups, models, nil
}

func (s *Service) processSafetyStatusIntent(ctx context.Context, workerID string, item AbnormalQueueItem) error {
	groups, models, err := s.safetyControlledScopes(ctx, item.UserID, item.WorkspaceID, item.TargetID)
	if err != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "controlled_scope_read_failed")
	}
	reservation, err := s.safetyRepo.ReserveSafetyFloor(ctx, FloorReservationRequest{
		UserID: item.UserID, WorkspaceID: item.WorkspaceID, AccountID: item.AccountID,
		IncidentID: item.IncidentID, ControlledGroupIDs: groups, ControlledModels: models,
		ReservationTTL: 2 * time.Minute,
	}, s.schedulerNow())
	if err != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "floor_reservation_failed")
	}
	if reservation.GuardHeld {
		if completeErr := s.cancelClaimedSafetyItem(ctx, workerID, item, "guard-held:"+reservation.Reason); completeErr != nil {
			return completeErr
		}
		return s.recordSafetyGuardHeld(ctx, item, reservation.Reason)
	}
	readback := false
	snapshotInvalidated := false
	retainReservation := false
	defer func() {
		if readback {
			_ = s.safetyRepo.CompleteFloorReservation(context.Background(), reservation.ID, true, snapshotInvalidated, s.schedulerNow())
			return
		}
		if !retainReservation {
			_ = s.safetyRepo.ReleaseFloorReservation(context.Background(), reservation.ID)
		}
	}()
	if s.mutationCoordinator == nil {
		return errors.New("mutation coordinator unavailable")
	}
	key := MutationKey{UserID: item.UserID, WorkspaceID: item.WorkspaceID, AccountID: item.AccountID}
	mutation, err := s.mutationCoordinator.BeginAutomatic(ctx, key, false)
	if err != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "mutation_busy")
	}
	defer mutation.Release()
	if mutation.Generation != item.MutationGeneration || mutation.Validate(ctx) != nil {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_manual_generation")
	}
	session, target, _, workspaceID, err := s.resolveSafetyTargetFresh(ctx, item)
	if err != nil || workspaceID != item.WorkspaceID {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "target_read_failed")
	}
	current, currentErr := s.safetyStatusIntentCurrent(ctx, item, true)
	if currentErr != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "status_intent_guard_read_failed")
	}
	if !current {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_or_open_incident")
	}
	if target.Schedulable == nil || !*target.Schedulable {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "guard-held:upstream_scheduling_disabled")
	}
	active, statusKnown := strictSub2APIStatus(target.AccountStatus)
	if !statusKnown {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "guard-held:status_unknown")
	}
	if !active {
		readback = true
		s.invalidateAdminInventorySnapshot(item.UserID, item.WorkspaceID)
		snapshotInvalidated = true
		// A previous status dispatch may have reached the upstream while its
		// response was lost. In that case the incident checkpoint is still the
		// authoritative local intent and must be finalized before the queue item
		// is closed. An account that was already inactive without that matching
		// checkpoint remains an external/manual state and is left untouched.
		stored, checkpointErr := s.repo.GetTargetActionState(ctx, item.UserID, item.WorkspaceID, item.TargetID)
		if checkpointErr != nil {
			return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "incident_checkpoint_read_failed")
		}
		if !incidentTargetActionMatches(item, stored) {
			return s.cancelClaimedSafetyItem(ctx, workerID, item, "already_inactive")
		}
		if err := s.finalizeSafetyInactiveAction(ctx, item, stored, RemoteActionSub2APIStatusInactive); err != nil {
			if errors.Is(err, errStaleMutation) {
				return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_incident_checkpoint")
			}
			return err
		}
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "inactive_readback_confirmed")
	}
	stored, err := s.prepareIncidentTargetAction(ctx, item, target)
	if err != nil {
		return err
	}
	current, currentErr = s.safetyStatusIntentCurrent(ctx, item, true)
	if currentErr != nil {
		return s.requeueClaimedSafetyItem(ctx, workerID, item, time.Second, "status_intent_guard_read_failed")
	}
	if !current || mutation.Validate(ctx) != nil {
		return s.cancelClaimedSafetyItem(ctx, workerID, item, "stale_before_dispatch")
	}
	dispatching, err := s.safetyRepo.MarkAbnormalQueueDispatching(ctx, item.ID, workerID, item.QueueEpoch)
	if err != nil || !dispatching {
		return err
	}
	if err := s.safetyRepo.MarkFloorReservationDispatching(ctx, reservation.ID, s.schedulerNow()); err != nil {
		abandonErr := s.safetyRepo.AbandonFloorReservationBeforeDispatch(ctx, reservation.ID)
		item.Attempt++
		rescheduleErr := s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "floor_dispatch_marker_failed")
		return errors.Join(err, abandonErr, rescheduleErr)
	}
	action, actionErr := s.dispatcher.ApplyTargetState(ctx, session, target, nil, "inactive")
	_, readbackTarget, _, _, readbackErr := s.resolveSafetyTargetFresh(ctx, item)
	if readbackErr != nil {
		retainReservation = true
		item.Attempt++
		reason := "status_readback_unknown"
		if actionErr != nil {
			reason = "status_write_and_readback_unknown:" + action
		}
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), reason)
	}
	readbackActive, readbackKnown := strictSub2APIStatus(readbackTarget.AccountStatus)
	if !readbackKnown {
		retainReservation = true
		item.Attempt++
		reason := "status_readback_not_applied"
		if actionErr != nil {
			reason = "status_write_failed_not_applied:" + action
		}
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), reason)
	}
	readback = true
	if readbackActive {
		item.Attempt++
		reason := "status_readback_not_applied"
		if actionErr != nil {
			reason = "status_write_failed_not_applied:" + action
		}
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), reason)
	}
	s.invalidateAdminInventorySnapshot(item.UserID, item.WorkspaceID)
	snapshotInvalidated = true
	current, currentErr = s.safetyStatusIntentEpochAndGenerationCurrent(ctx, item)
	mutationErr := mutation.Validate(ctx)
	if currentErr != nil {
		item.Attempt++
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "status_intent_guard_read_failed_after_write")
	}
	if !current || mutationErr != nil {
		_ = s.clearIncidentTargetActionIfOwned(ctx, item)
		if auditRepo, ok := s.repo.(epochObservationRepository); ok {
			_ = auditRepo.InsertSafetyAudit(ctx, item.UserID, item.WorkspaceID,
				"stale_status_dispatch", item.TargetID+":"+action)
		}
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_after_dispatch")
	}
	action = RemoteActionSub2APIStatusInactive
	if err := s.finalizeSafetyInactiveAction(ctx, item, stored, action); err != nil {
		return err
	}
	return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, action)
}

func (s *Service) safetyStatusIntentEpochAndGenerationCurrent(ctx context.Context, item AbnormalQueueItem) (bool, error) {
	epoch, err := s.safetyRepo.GetAbnormalQueueEpoch(ctx, item.UserID, item.WorkspaceID)
	if err != nil {
		return false, err
	}
	if epoch != item.QueueEpoch {
		return false, nil
	}
	generation, err := s.safetyRepo.MutationGeneration(ctx, item.UserID, item.WorkspaceID, item.AccountID)
	if err != nil {
		return false, err
	}
	return generation == item.MutationGeneration, nil
}

func (s *Service) safetyStatusIntentCurrent(ctx context.Context, item AbnormalQueueItem, requireClosedCircuit bool) (bool, error) {
	current, err := s.safetyStatusIntentEpochAndGenerationCurrent(ctx, item)
	if err != nil || !current || !requireClosedCircuit {
		return current, err
	}
	incident, err := s.safetyRepo.GetIncidentCircuit(ctx, item.UserID, item.WorkspaceID, item.FaultDomain)
	if err != nil {
		return false, err
	}
	if incident != nil && incident.State != CircuitClosed {
		return false, nil
	}
	return true, nil
}

func (s *Service) clearIncidentTargetActionIfOwned(ctx context.Context, item AbnormalQueueItem) error {
	stored, err := s.repo.GetTargetActionState(ctx, item.UserID, item.WorkspaceID, item.TargetID)
	if err != nil || !incidentTargetActionMatches(item, stored) {
		return err
	}
	clearTargetActionPending(stored)
	return s.repo.UpsertTargetActionState(ctx, *stored)
}

func (s *Service) finalizeSafetyInactiveAction(ctx context.Context, item AbnormalQueueItem, stored *TargetActionState, action string) error {
	if stored == nil {
		var err error
		stored, err = s.repo.GetTargetActionState(ctx, item.UserID, item.WorkspaceID, item.TargetID)
		if err != nil {
			return err
		}
		if stored == nil {
			return errors.New("incident target action checkpoint unavailable")
		}
	}
	if stored.PendingMutationGeneration != item.MutationGeneration ||
		stored.PendingSource != SafetySourceHealthIncident || stored.PendingEpoch != item.QueueEpoch {
		return errStaleMutation
	}
	stored.LastAppliedStatus = "inactive"
	clearTargetActionPending(stored)
	if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
		return err
	}
	return s.markTargetSuspendedAfterConfirmedAction(ctx, item, action)
}

func incidentTargetActionMatches(item AbnormalQueueItem, stored *TargetActionState) bool {
	if stored == nil || stored.PendingStatus != "inactive" ||
		stored.PendingSource != SafetySourceHealthIncident ||
		stored.PendingMutationGeneration != item.MutationGeneration ||
		stored.PendingEpoch != item.QueueEpoch {
		return false
	}
	return stored.PendingActionKey == "" || stored.PendingActionKey == item.ActionKey
}

func (s *Service) resolveSafetyTargetFresh(ctx context.Context, item AbnormalQueueItem) (upstream.Session, AdminProbeTarget, upstream.AdminGroupAccountInfo, string, error) {
	session, err := s.mySites.RequireSession(ctx, item.UserID, item.WorkspaceID)
	if err != nil {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, "", err
	}
	if session.Platform != upstream.PlatformSub2API {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, "", errors.New("safety target is not Sub2API")
	}
	target, account, found, accountsReadError, err := s.findAdminTarget(ctx, session, item.WorkspaceID, item.AccountID)
	if err != nil {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, "", err
	}
	if !found || accountsReadError || target.TargetID != item.TargetID {
		return upstream.Session{}, AdminProbeTarget{}, upstream.AdminGroupAccountInfo{}, "", errors.New("safety target readback incomplete")
	}
	return session, target, account, item.WorkspaceID, nil
}

func (s *Service) prepareIncidentTargetAction(ctx context.Context, item AbnormalQueueItem, target AdminProbeTarget) (*TargetActionState, error) {
	stored, err := s.repo.GetTargetActionState(ctx, item.UserID, item.WorkspaceID, item.TargetID)
	if err != nil {
		return nil, err
	}
	currentStatus := normalizeTargetStatus(target.Platform, target.AccountStatus)
	if stored == nil {
		stored = &TargetActionState{
			UserID: item.UserID, AdminAccountID: item.WorkspaceID, TargetID: item.TargetID,
			OriginalStatus: currentStatus, LastAppliedStatus: currentStatus,
		}
	} else if targetActionCheckpointConflicted(target, stored, currentStatus, nil) || stored.Conflict {
		return nil, errors.New("target action checkpoint conflict")
	}
	stored.PendingStatus = "inactive"
	stored.PendingWeight = nil
	stored.PendingMutationGeneration = item.MutationGeneration
	stored.PendingSource = SafetySourceHealthIncident
	stored.PendingEpoch = item.QueueEpoch
	stored.PendingActionKey = item.ActionKey
	if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *Service) markTargetSuspendedAfterConfirmedAction(ctx context.Context, item AbnormalQueueItem, action string) error {
	states, err := s.repo.ListStatesByConnection(ctx, item.TargetID)
	if err != nil {
		return err
	}
	epochRepo, ok := s.repo.(epochObservationRepository)
	if !ok {
		return errors.New("automatic observation epoch repository unavailable")
	}
	for _, state := range states {
		if state.State == StateSuspended && state.LastRemoteAction == action {
			continue
		}
		previous := state.State
		state.State = StateSuspended
		state.CurrentWeight = 0
		state.LastRemoteAction = action
		committed, err := epochRepo.UpsertStateIfAbnormalQueueEpoch(ctx, state, item.QueueEpoch)
		if err != nil {
			return err
		}
		if !committed {
			return errStaleMutation
		}
		target := AdminProbeTarget{TargetID: item.TargetID, Platform: string(upstream.PlatformSub2API), AccountID: item.AccountID}
		if err := s.recordTargetEventAtEpoch(ctx, item.UserID, item.WorkspaceID, target, "", state.ModelName,
			"confirmed_failure", string(previous), string(StateSuspended), nil,
			state.LastErrorKey, state.LastErrorDetail, action, EventSourceScheduled, &item.QueueEpoch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordSafetyGuardHeld(ctx context.Context, item AbnormalQueueItem, reason string) error {
	if epochRepo, ok := s.repo.(epochObservationRepository); ok {
		return epochRepo.InsertSafetyAudit(ctx, item.UserID, item.WorkspaceID, "guard-held", item.TargetID+":"+item.ModelName+":"+reason)
	}
	return nil
}

func (s *Service) recoverUncertainSafetyDispatch(ctx context.Context, workerID string, item AbnormalQueueItem) error {
	if item.Kind == QueueKindConfirmation || item.Kind == QueueKindCanary {
		currentEpoch, err := s.safetyRepo.GetAbnormalQueueEpoch(ctx, item.UserID, item.WorkspaceID)
		if err != nil {
			return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, time.Second, "epoch_read_failed_recovery")
		}
		if currentEpoch != item.QueueEpoch {
			return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_abnormal_queue_epoch")
		}
		if item.Kind != QueueKindCanary {
			currentGeneration, generationErr := s.safetyRepo.MutationGeneration(ctx, item.UserID, item.WorkspaceID, item.AccountID)
			if generationErr != nil {
				return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, time.Second, "generation_read_failed_recovery")
			}
			if currentGeneration != item.MutationGeneration {
				return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_manual_generation")
			}
		}
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, time.Second, "uncertain_probe_retry")
	}
	if item.Kind != QueueKindStatusIntent {
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "uncertain_non_status_dispatch_completed")
	}
	if s.mutationCoordinator == nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "mutation_coordinator_unavailable")
	}
	mutation, err := s.mutationCoordinator.BeginAutomatic(ctx, MutationKey{UserID: item.UserID, WorkspaceID: item.WorkspaceID, AccountID: item.AccountID}, false)
	if err != nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "mutation_busy_recovery")
	}
	defer mutation.Release()

	_, target, _, _, err := s.resolveSafetyTargetFresh(ctx, item)
	if err != nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "uncertain_status_readback_failed")
	}
	active, known := strictSub2APIStatus(target.AccountStatus)
	if !known {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "uncertain_status_readback_unknown")
	}
	current, currentErr := s.safetyStatusIntentEpochAndGenerationCurrent(ctx, item)
	if currentErr != nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "status_intent_guard_read_failed_recovery")
	}
	if !active {
		s.invalidateAdminInventorySnapshot(item.UserID, item.WorkspaceID)
		if err := s.safetyRepo.ResolveIncidentFloorReservations(ctx, item.UserID, item.WorkspaceID, item.AccountID, item.IncidentID, true, s.schedulerNow()); err != nil {
			item.Attempt++
			return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "floor_readback_commit_failed")
		}
	} else if err := s.safetyRepo.ResolveIncidentFloorReservations(ctx, item.UserID, item.WorkspaceID, item.AccountID, item.IncidentID, false, s.schedulerNow()); err != nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "floor_readback_commit_failed")
	}
	if !current {
		_ = s.clearIncidentTargetActionIfOwned(ctx, item)
		if auditRepo, ok := s.repo.(epochObservationRepository); ok {
			_ = auditRepo.InsertSafetyAudit(ctx, item.UserID, item.WorkspaceID, "stale_status_dispatch_recovered", item.TargetID)
		}
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_after_dispatch")
	}
	if active {
		item.Attempt++
		return s.rescheduleDispatchedSafetyItem(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "uncertain_status_not_applied")
	}
	if mutation.Generation != item.MutationGeneration || mutation.Validate(ctx) != nil {
		_ = s.clearIncidentTargetActionIfOwned(ctx, item)
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_manual_generation")
	}
	stored, checkpointErr := s.repo.GetTargetActionState(ctx, item.UserID, item.WorkspaceID, item.TargetID)
	if checkpointErr != nil {
		item.Attempt++
		return s.rescheduleUncertainStatusDispatch(ctx, workerID, item, safetyStatusRetryDelay(item.Attempt), "incident_checkpoint_read_failed_recovery")
	}
	if !incidentTargetActionMatches(item, stored) {
		return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "inactive_readback_without_owned_checkpoint")
	}
	if err := s.finalizeSafetyInactiveAction(ctx, item, stored, RemoteActionSub2APIStatusInactive); err != nil {
		if errors.Is(err, errStaleMutation) {
			_ = s.clearIncidentTargetActionIfOwned(ctx, item)
			return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "stale_incident_checkpoint")
		}
		return err
	}
	return s.safetyRepo.CompleteAbnormalQueueItem(ctx, item.ID, workerID, "inactive_readback_confirmed")
}

func safetyWorkerError(item AbnormalQueueItem, reason string) error {
	return fmt.Errorf("safety item %s (%s): %s", item.ID, item.Kind, reason)
}

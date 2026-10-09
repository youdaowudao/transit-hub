package connection_health

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type questionAnswerSchedulePreparation struct {
	outcome      QuestionAnswerSchedulePreparationOutcome
	reservations map[string]*questionAnswerTargetStart
	credentials  map[string]upstream.ProbeCredential
}

type questionAnswerScheduleTargetFailure struct {
	target QuestionAnswerScheduleExecutionTarget
	reason string
}

type questionAnswerScheduleHandle struct {
	identity       QuestionAnswerScheduleHandleIdentity
	execution      QuestionAnswerScheduleExecution
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	prepareDone    chan struct{}
	preparation    questionAnswerSchedulePreparation
	buildDone      chan struct{}
	batchIDs       map[string]string
	admitted       bool
	targetFailures map[string]questionAnswerScheduleTargetFailure
}

func (s *Service) wakeQuestionAnswerScheduleCoordinator() {
	s.questionAnswerScheduleMu.Lock()
	wake := s.questionAnswerScheduleWake
	s.questionAnswerScheduleMu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
func (s *Service) cancelQuestionAnswerScheduleHandle(id string) {
	s.questionAnswerScheduleMu.Lock()
	handle := s.questionAnswerScheduleHandles[id]
	s.questionAnswerScheduleMu.Unlock()
	if handle != nil {
		handle.cancel()
	}
}

// Called after C1 schema/abandoned-record finalization and before the runner.
// APIOnly construction never loads or applies singleton capacity.
func (s *Service) StartQuestionAnswerScheduleCoordinator(parent context.Context) error {
	if s.backgroundTasksDisabled {
		return requestError(ErrorQuestionAnswerServiceStopped)
	}
	if s.questionAnswerSchedules == nil {
		return requestError(ErrorQuestionAnswerStorage)
	}
	settings, err := s.questionAnswerSchedules.GetQuestionAnswerRuntimeSettings(parent)
	if err != nil {
		return err
	}
	if settings.UpdatedAt != nil {
		s.SetQuestionAnswerConcurrency(settings.QuestionAnswerConcurrency, settings.Version)
	}
	s.questionAnswerScheduleMu.Lock()
	if s.questionAnswerScheduleCtx != nil {
		s.questionAnswerScheduleMu.Unlock()
		return nil
	}
	s.questionAnswerScheduleCtx, s.questionAnswerScheduleCancel = context.WithCancel(parent)
	s.questionAnswerScheduleDone = make(chan struct{})
	s.questionAnswerScheduleWake = make(chan struct{}, 1)
	s.questionAnswerScheduleHandles = map[string]*questionAnswerScheduleHandle{}
	s.questionAnswerScheduleMu.Unlock()
	go s.runQuestionAnswerScheduleCoordinator()
	return nil
}

func (s *Service) runQuestionAnswerScheduleCoordinator() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	defer close(s.questionAnswerScheduleDone)
	for {
		if err := s.reconcileQuestionAnswerScheduleExecutions(s.questionAnswerScheduleCtx); err != nil && s.questionAnswerScheduleCtx.Err() == nil {
			log.Printf("[question-answer-schedules] reconciliation failed: %v", err)
		}
		select {
		case <-s.questionAnswerScheduleCtx.Done():
			handles := s.questionAnswerScheduleHandleSnapshot()
			for _, handle := range handles {
				handle.cancel()
			}
			for _, handle := range handles {
				handle.mu.Lock()
				s.waitQuestionAnswerScheduleCalls(context.Background(), handle)
				s.releaseQuestionAnswerScheduleReservations(handle)
				handle.mu.Unlock()
			}
			return
		case <-ticker.C:
		case <-s.questionAnswerScheduleWake:
		}
	}
}

func (s *Service) questionAnswerScheduleHandleSnapshot() []*questionAnswerScheduleHandle {
	s.questionAnswerScheduleMu.Lock()
	defer s.questionAnswerScheduleMu.Unlock()
	result := make([]*questionAnswerScheduleHandle, 0, len(s.questionAnswerScheduleHandles))
	for _, handle := range s.questionAnswerScheduleHandles {
		result = append(result, handle)
	}
	return result
}
func (s *Service) removeQuestionAnswerScheduleHandle(handle *questionAnswerScheduleHandle) {
	s.questionAnswerScheduleMu.Lock()
	if s.questionAnswerScheduleHandles[handle.identity.ExecutionID] == handle {
		delete(s.questionAnswerScheduleHandles, handle.identity.ExecutionID)
	}
	s.questionAnswerScheduleMu.Unlock()
	handle.cancel()
}
func (s *Service) ensureQuestionAnswerScheduleHandle(execution QuestionAnswerScheduleExecution) *questionAnswerScheduleHandle {
	s.questionAnswerScheduleMu.Lock()
	defer s.questionAnswerScheduleMu.Unlock()
	if handle := s.questionAnswerScheduleHandles[execution.ID]; handle != nil {
		return handle
	}
	ctx, cancel := context.WithDeadline(s.questionAnswerScheduleCtx, execution.deadline())
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{UserID: execution.UserID, AdminAccountID: execution.AdminAccountID, ScheduleID: execution.ScheduleID, ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
	for _, target := range execution.Targets {
		handle.batchIDs[target.TargetID] = target.ID
	}
	s.questionAnswerScheduleHandles[execution.ID] = handle
	return handle
}

func (s *Service) processDueQuestionAnswerSchedule(ctx context.Context, schedule QuestionAnswerSchedule) error {
	lock := s.questionAnswerScheduleWorkspaceLock(schedule.UserID, schedule.AdminAccountID)
	lock.Lock()
	defer lock.Unlock()
	_, err := s.questionAnswerSchedules.ProcessDueQuestionAnswerSchedule(ctx, schedule.UserID, schedule.AdminAccountID, schedule.ID, s.scheduleNow)
	return err
}

func (s *Service) reconcileQuestionAnswerScheduleExecutions(ctx context.Context) error {
	// Parent checks use all retained handles, including executions already gone
	// from SQL and cleanup jobs completed in a separate APIOnly process.
	handles := s.questionAnswerScheduleHandleSnapshot()
	identities := make([]QuestionAnswerScheduleHandleIdentity, 0, len(handles))
	for _, h := range handles {
		identities = append(identities, h.identity)
	}
	var errs []error
	if len(identities) > 0 {
		parents, err := s.questionAnswerSchedules.ListQuestionAnswerScheduleHandleParents(ctx, identities)
		if err != nil {
			errs = append(errs, err)
		} else {
			deletedWorkspaces := map[string]bool{}
			for _, h := range handles {
				if !parents[h.identity.ExecutionID] {
					key := h.identity.UserID + "|" + h.identity.AdminAccountID
					if !deletedWorkspaces[key] {
						deletedWorkspaces[key] = true
						cleanupCtx, stop := context.WithTimeout(ctx, 5*time.Second)
						err := s.CleanupDeletedQuestionAnswerSchedules(cleanupCtx, h.identity.UserID, h.identity.AdminAccountID)
						stop()
						if err != nil {
							errs = append(errs, err)
						}
					}
				}
			}
		}
	}
	executions, err := s.questionAnswerSchedules.ListNonterminalQuestionAnswerScheduleExecutions(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	// Reconcile already accepted execution snapshots before advancing cursors.
	workspaces := map[string]QuestionAnswerScheduleHandleIdentity{}
	for _, execution := range executions {
		workspaces[execution.UserID+"|"+execution.AdminAccountID] = QuestionAnswerScheduleHandleIdentity{UserID: execution.UserID, AdminAccountID: execution.AdminAccountID}
		if execution.Status == "active" {
			current, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleExecution(ctx, execution.UserID, execution.AdminAccountID, execution.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if current == nil {
				continue
			}
			handle := s.ensureQuestionAnswerScheduleHandle(*current)
			if err := s.reconcileQuestionAnswerScheduleHandle(ctx, handle, *current); err != nil {
				errs = append(errs, err)
			}
		}
	}
	due, err := s.questionAnswerSchedules.ListDueQuestionAnswerSchedules(ctx, s.scheduleNow())
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, schedule := range due {
			workspaces[schedule.UserID+"|"+schedule.AdminAccountID] = QuestionAnswerScheduleHandleIdentity{UserID: schedule.UserID, AdminAccountID: schedule.AdminAccountID}
			if err := s.processDueQuestionAnswerSchedule(ctx, schedule); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, workspace := range workspaces {
		for {
			lock := s.questionAnswerScheduleWorkspaceLock(workspace.UserID, workspace.AdminAccountID)
			lock.Lock()
			execution, err := s.questionAnswerSchedules.ClaimNextQuestionAnswerScheduleExecution(ctx, workspace.UserID, workspace.AdminAccountID, s.scheduleNow)
			lock.Unlock()
			if err != nil {
				errs = append(errs, err)
				break
			}
			if execution == nil {
				break
			}
			handle := s.ensureQuestionAnswerScheduleHandle(*execution)
			if err := s.reconcileQuestionAnswerScheduleHandle(ctx, handle, *execution); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Service) launchQuestionAnswerSchedulePreparation(handle *questionAnswerScheduleHandle) {
	handle.prepareDone = make(chan struct{})
	go func() {
		result := s.resolveQuestionAnswerScheduleExecution(handle.ctx, handle.execution)
		handle.preparation = result
		close(handle.prepareDone)
		s.wakeQuestionAnswerScheduleCoordinator()
	}()
}

func (s *Service) resolveQuestionAnswerScheduleExecution(ctx context.Context, execution QuestionAnswerScheduleExecution) questionAnswerSchedulePreparation {
	result := questionAnswerSchedulePreparation{reservations: map[string]*questionAnswerTargetStart{}, credentials: map[string]upstream.ProbeCredential{}}
	fail := func(reason, block string) questionAnswerSchedulePreparation {
		result.outcome = QuestionAnswerSchedulePreparationOutcome{Status: "failed", Reason: reason, BlockedReason: block}
		return result
	}
	requested := execution.ConfigSnapshot.Requested
	config := requested.Schedule
	inventory, err := s.resolveQuestionAnswerScheduleTargets(ctx, execution.UserID, execution.AdminAccountID, config)
	if err != nil {
		return fail("test_configuration_unavailable", "")
	}
	if inventory.blockedReason != "" {
		return fail(inventory.blockedReason, inventory.blockedReason)
	}
	targets := make([]QuestionAnswerScheduleExecutionTarget, 0, len(inventory.preview))
	now := s.scheduleNow()
	for _, entry := range inventory.preview {
		id, err := newID()
		if err != nil {
			return fail("storage_error", "")
		}
		name := entry.AccountName
		if entry.Missing {
			for _, saved := range requested.Selection.TargetPreview {
				if saved.TargetID == entry.TargetID && saved.AccountName != "" {
					name = saved.AccountName
					break
				}
			}
		}
		target := QuestionAnswerScheduleExecutionTarget{ID: id, ExecutionID: execution.ID, TargetID: entry.TargetID, AccountSnapshot: QuestionAnswerScheduleAccountSnapshot{AccountID: entry.AccountID, AccountName: name, Platform: entry.Platform}, MatchedGroupsSnapshot: entry.MatchedGroups, RequestedModels: append([]string{}, config.Models...), AvailableModels: []string{}, UnavailableModels: []QuestionAnswerScheduleUnavailableModel{}, Status: "pending", CreatedAt: now, UpdatedAt: now}
		if entry.Missing {
			target.Status, target.StatusReason = "failed", "account_not_found"
			target.CompletedAt = &now
		}
		targets = append(targets, target)
	}
	if inventory.allMissing {
		missing := []string{}
		for _, target := range targets {
			missing = append(missing, target.TargetID)
		}
		result.outcome = QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "all_accounts_missing", BlockedReason: "all_accounts_missing", Resolved: &QuestionAnswerScheduleResolved{Questions: []TestQuestion{}, ResolvedAt: now, MissingTargetIDs: missing}, Targets: targets}
		return result
	}
	if len(targets) == 0 {
		result.outcome = QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "empty_scope"}
		return result
	}
	questions, configs, err := s.questionAnswerSchedules.ReadQuestionAnswerSchedulePreparationConfiguration(ctx, execution.UserID, execution.AdminAccountID, config.QuestionIDs)
	if err != nil {
		if errors.Is(err, errQuestionAnswerUnavailable) {
			return fail("question_unavailable", "question_unavailable")
		}
		return fail("test_configuration_unavailable", "")
	}
	if len(questions) != len(config.QuestionIDs) {
		return fail("question_unavailable", "question_unavailable")
	}
	questions = append([]TestQuestion{}, questions...)
	for i := range questions {
		questions[i].Keywords = append([]string{}, questions[i].Keywords...)
	}
	// One local configuration read freezes every target before any credential or
	// model access. All targets use this exact question/configuration collection.
	for i := range targets {
		target := &targets[i]
		if target.Status != "pending" {
			continue
		}
		memberships := inventory.memberships[target.TargetID]
		effective := ResolveGroupTestConfiguration(string(upstream.PlatformSub2API), memberships, true, configs)
		target.TestConfigurationSnapshot = QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: execution.AdminAccountID, Memberships: append([]TestConfigurationSource{}, memberships...), InventoryComplete: true}, Protocol: effective.Protocol}
		if !effective.usable() {
			target.Status, target.StatusReason = "failed", "test_configuration_conflict"
			target.CompletedAt = &now
		}
	}
	for i := range targets {
		target := &targets[i]
		if target.Status != "pending" {
			continue
		}
		if ctx.Err() != nil {
			return fail("preparation_cancelled", "")
		}
		reservation, err := s.reserveQuestionAnswerTargetStart(ctx, execution.UserID, target.TargetID, target.ID)
		if err != nil {
			target.Status, target.StatusReason = "skipped", "target_busy"
			target.CompletedAt = &now
			continue
		}
		result.reservations[target.TargetID] = reservation
		cred, err := s.resolveProbeCredential(reservation.ctx, inventory.session, inventory.accounts[target.TargetID])
		if err != nil {
			target.Status, target.StatusReason = "failed", "credential_unavailable"
			target.CompletedAt = &now
			continue
		}
		discovered, err := s.modelDiscovery.ListModels(reservation.ctx, cred.BaseURL, cred.Key)
		if err != nil {
			target.Status, target.StatusReason = "failed", "model_discovery_failed"
			target.CompletedAt = &now
			continue
		}
		models := map[string]bool{}
		for _, model := range discovered {
			models[model.ID] = true
		}
		for _, model := range config.Models {
			if models[model] {
				target.AvailableModels = append(target.AvailableModels, model)
			} else {
				target.UnavailableModels = append(target.UnavailableModels, QuestionAnswerScheduleUnavailableModel{ModelName: model, Reason: "model_unavailable"})
			}
		}
		if len(target.AvailableModels) == 0 {
			target.Status, target.StatusReason = "failed", "all_models_unavailable"
			target.CompletedAt = &now
			continue
		}
		target.PlannedRequestCount = len(target.AvailableModels) * len(questions) * config.RepeatCount
		result.credentials[target.TargetID] = cred
	}
	missingTargetIDs := []string{}
	for _, target := range targets {
		if target.StatusReason == "account_not_found" {
			missingTargetIDs = append(missingTargetIDs, target.TargetID)
		}
	}
	result.outcome = QuestionAnswerSchedulePreparationOutcome{Resolved: &QuestionAnswerScheduleResolved{Questions: questions, ResolvedAt: s.scheduleNow(), MissingTargetIDs: missingTargetIDs}, Targets: targets}
	return result
}

func (s *Service) releaseQuestionAnswerScheduleReservations(handle *questionAnswerScheduleHandle) {
	if handle.prepareDone != nil && !questionAnswerChannelClosed(handle.prepareDone) {
		return
	}
	for _, reservation := range handle.preparation.reservations {
		s.releaseQuestionAnswerTargetStart(reservation)
	}
}

func (s *Service) launchQuestionAnswerScheduleBuild(handle *questionAnswerScheduleHandle, execution QuestionAnswerScheduleExecution) {
	handle.buildDone = make(chan struct{})
	for _, target := range execution.Targets {
		handle.batchIDs[target.TargetID] = target.ID
	}
	go func() {
		defer func() { close(handle.buildDone); s.wakeQuestionAnswerScheduleCoordinator() }()
		// Targets arrive sorted by canonical targetId from both preparation and SQL.
		for _, target := range execution.Targets {
			if target.Status != "pending" && target.Status != "starting" {
				continue
			}
			if target.BatchAvailable {
				continue
			}
			if handle.ctx.Err() != nil {
				return
			}
			reservation := handle.preparation.reservations[target.TargetID]
			cred, ready := handle.preparation.credentials[target.TargetID]
			var err error
			if reservation == nil {
				reservation, err = s.reserveQuestionAnswerTargetStart(handle.ctx, execution.UserID, target.TargetID, target.ID)
				if err != nil {
					s.failQuestionAnswerScheduleBuildTarget(handle, execution, target, nil, "target_busy")
					continue
				}
			}
			if !ready {
				// Recovery obtains credentials only. Frozen memberships/protocol/questions
				// and discovered models are never replaced by present-day configuration.
				cred, err = s.resolveQuestionAnswerScheduleRecoveryCredential(reservation.ctx, execution, target)
				if err != nil {
					s.failQuestionAnswerScheduleBuildTarget(handle, execution, target, reservation, "credential_unavailable")
					continue
				}
			}
			_, err = s.startQuestionAnswerBatchCore(reservation, cred, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
				return s.questionAnswerSchedules.CreateScheduledQuestionAnswerBatch(ctx, execution.UserID, execution.AdminAccountID, execution.ID, target.TargetID, target.ID)
			})
			s.releaseQuestionAnswerTargetStart(reservation)
			if err != nil {
				s.failQuestionAnswerScheduleBuildTarget(handle, execution, target, nil, "batch_creation_failed")
			}
		}
	}()
}
func (s *Service) failQuestionAnswerScheduleBuildTarget(handle *questionAnswerScheduleHandle, execution QuestionAnswerScheduleExecution, target QuestionAnswerScheduleExecutionTarget, reservation *questionAnswerTargetStart, reason string) {
	s.releaseQuestionAnswerTargetStart(reservation)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.questionAnswerSchedules.FailQuestionAnswerScheduleTarget(ctx, execution.UserID, execution.AdminAccountID, execution.ID, target.TargetID, target.ID, reason, s.scheduleNow); err != nil {
		handle.targetFailures[target.TargetID] = questionAnswerScheduleTargetFailure{target: target, reason: reason}
		log.Printf("[question-answer-schedules] target completion failed: %v", err)
	}
}
func (s *Service) resolveQuestionAnswerScheduleRecoveryCredential(ctx context.Context, execution QuestionAnswerScheduleExecution, target QuestionAnswerScheduleExecutionTarget) (upstream.ProbeCredential, error) {
	if err := questionAnswerScheduleOwnedTarget(execution.UserID, execution.AdminAccountID, target.TargetID); err != nil {
		return upstream.ProbeCredential{}, err
	}
	session, err := s.mySites.RequireSession(ctx, execution.UserID, execution.AdminAccountID)
	if err != nil {
		return upstream.ProbeCredential{}, err
	}
	reader, ok := s.platformGroups.(interface {
		ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error)
	})
	if !ok {
		return upstream.ProbeCredential{}, requestError(ErrorTestConfigurationUnavailable)
	}
	accounts, err := reader.ListSub2APIAdminAccountsContext(ctx, session)
	if err != nil {
		return upstream.ProbeCredential{}, err
	}
	parsed, _ := parseTargetID(target.TargetID)
	for _, account := range accounts {
		if account.ID == parsed.accountID {
			return s.resolveProbeCredential(ctx, session, account)
		}
	}
	return upstream.ProbeCredential{}, requestError(ErrorProbeTargetNotFound)
}

func (s *Service) reconcileQuestionAnswerScheduleHandle(ctx context.Context, handle *questionAnswerScheduleHandle, execution QuestionAnswerScheduleExecution) error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	now := s.scheduleNow()
	if !questionAnswerScheduleExecutionActive(execution.Status) {
		s.releaseQuestionAnswerScheduleReservations(handle)
		s.removeQuestionAnswerScheduleHandle(handle)
		return nil
	}
	// The repository first recognizes records already terminal. Deadline cannot
	// turn finalizer-only waiting into a newly accepted timeout.
	if execution.TerminationCause == "" && !now.Before(execution.deadline()) {
		lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
		lock.Lock()
		result, err := s.questionAnswerSchedules.DecideQuestionAnswerScheduleTermination(ctx, execution.UserID, execution.AdminAccountID, execution.ID, "execution_timeout", nil, s.scheduleNow)
		lock.Unlock()
		if err != nil {
			return err
		}
		execution = result.Execution
	}
	if execution.TerminationCause != "" {
		handle.cancel()
		if !s.questionAnswerScheduleCallsExited(handle) {
			return nil
		}
		s.releaseQuestionAnswerScheduleReservations(handle)
		if err := s.settleQuestionAnswerScheduleBatches(ctx, handle, execution, true); err != nil {
			return s.persistQuestionAnswerScheduleSettling(ctx, execution, false, "c1_finalization_failed", err)
		}
		return s.persistQuestionAnswerScheduleSettling(ctx, execution, true, "", nil)
	}
	if execution.ConfigSnapshot.Resolved == nil {
		if handle.prepareDone == nil {
			if now.After(execution.startWindowEnd()) {
				lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
				lock.Lock()
				finished, err := s.questionAnswerSchedules.FinishQuestionAnswerSchedulePreparation(ctx, execution.UserID, execution.AdminAccountID, execution.ID, QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "start_window_expired"}, s.scheduleNow)
				lock.Unlock()
				if err == nil && finished != nil && !questionAnswerScheduleExecutionActive(finished.Status) {
					s.removeQuestionAnswerScheduleHandle(handle)
				}
				return err
			}
			s.launchQuestionAnswerSchedulePreparation(handle)
			return nil
		}
		if !questionAnswerChannelClosed(handle.prepareDone) {
			return nil
		}
		outcome := handle.preparation.outcome
		if outcome.Status != "" {
			s.releaseQuestionAnswerScheduleReservations(handle)
			lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
			lock.Lock()
			finished, err := s.questionAnswerSchedules.FinishQuestionAnswerSchedulePreparation(ctx, execution.UserID, execution.AdminAccountID, execution.ID, outcome, s.scheduleNow)
			lock.Unlock()
			if err == nil && finished != nil && !questionAnswerScheduleExecutionActive(finished.Status) {
				s.removeQuestionAnswerScheduleHandle(handle)
			}
			return err
		}
		lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
		lock.Lock()
		reason, err := s.questionAnswerSchedules.FreezeQuestionAnswerScheduleExecution(ctx, execution.UserID, execution.AdminAccountID, execution.ID, *outcome.Resolved, outcome.Targets, s.scheduleNow)
		lock.Unlock()
		if err != nil {
			return err
		}
		if reason != "" {
			s.releaseQuestionAnswerScheduleReservations(handle)
			lock.Lock()
			finished, err := s.questionAnswerSchedules.FinishQuestionAnswerSchedulePreparation(ctx, execution.UserID, execution.AdminAccountID, execution.ID, QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: reason}, s.scheduleNow)
			lock.Unlock()
			if err == nil && finished != nil && !questionAnswerScheduleExecutionActive(finished.Status) {
				s.removeQuestionAnswerScheduleHandle(handle)
			}
			return err
		}
		handle.admitted = true
		execution.ConfigSnapshot.Resolved = outcome.Resolved
		execution.Targets = outcome.Targets
		s.launchQuestionAnswerScheduleBuild(handle, execution)
		return nil
	}
	if handle.buildDone == nil {
		s.launchQuestionAnswerScheduleBuild(handle, execution)
		return nil
	}
	if !questionAnswerChannelClosed(handle.buildDone) {
		return nil
	}
	s.releaseQuestionAnswerScheduleReservations(handle)
	for targetID, failure := range handle.targetFailures {
		lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
		lock.Lock()
		err := s.questionAnswerSchedules.FailQuestionAnswerScheduleTarget(ctx, execution.UserID, execution.AdminAccountID, execution.ID, targetID, failure.target.ID, failure.reason, s.scheduleNow)
		lock.Unlock()
		if err != nil {
			return err
		}
		delete(handle.targetFailures, targetID)
	}
	for _, target := range execution.Targets {
		if target.Stats.Requests.InProgress > 0 || (!target.BatchAvailable && (target.Status == "pending" || target.Status == "starting")) {
			return nil
		}
	}
	if err := s.settleQuestionAnswerScheduleBatches(ctx, handle, execution, false); err != nil {
		reason := "c1_finalization_failed"
		if errors.Is(err, errQuestionAnswerScheduleFinalizationPending) {
			reason = "c1_finalization_pending"
		}
		return s.persistQuestionAnswerScheduleSettling(ctx, execution, false, reason, err)
	}
	return s.persistQuestionAnswerScheduleSettling(ctx, execution, true, "", nil)
}

var errQuestionAnswerScheduleFinalizationPending = errors.New("question answer schedule C1 finalization pending")

func (s *Service) reconcileQuestionAnswerScheduleCancellationConflict(ctx context.Context, execution QuestionAnswerScheduleExecution) (*QuestionAnswerScheduleExecution, error) {
	s.questionAnswerScheduleMu.Lock()
	handle := s.questionAnswerScheduleHandles[execution.ID]
	s.questionAnswerScheduleMu.Unlock()
	if handle == nil {
		// No preparation/build can belong to an execution without its retained
		// handle. Exact C1 runs still need checking after startup reconciliation.
		handle = &questionAnswerScheduleHandle{batchIDs: map[string]string{}}
		for _, target := range execution.Targets {
			handle.batchIDs[target.TargetID] = target.ID
		}
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if s.questionAnswerScheduleCallsExited(handle) {
		s.releaseQuestionAnswerScheduleReservations(handle)
		settleErr := s.settleQuestionAnswerScheduleBatches(ctx, handle, execution, false)
		reason := ""
		if settleErr != nil {
			reason = "c1_finalization_failed"
			if errors.Is(settleErr, errQuestionAnswerScheduleFinalizationPending) {
				reason = "c1_finalization_pending"
			}
		}
		lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
		lock.Lock()
		finished, err := s.questionAnswerSchedules.FinishQuestionAnswerScheduleExecution(ctx, execution.UserID, execution.AdminAccountID, execution.ID, settleErr == nil, reason, s.scheduleNow)
		lock.Unlock()
		if err != nil {
			return nil, err
		}
		if finished != nil && !questionAnswerScheduleExecutionActive(finished.Status) && handle.cancel != nil {
			s.removeQuestionAnswerScheduleHandle(handle)
		}
	}
	return s.questionAnswerSchedules.GetQuestionAnswerScheduleExecution(ctx, execution.UserID, execution.AdminAccountID, execution.ID)
}

func (s *Service) settleQuestionAnswerScheduleBatches(ctx context.Context, handle *questionAnswerScheduleHandle, execution QuestionAnswerScheduleExecution, cancel bool) error {
	for targetID, batchID := range handle.batchIDs {
		// Capture the original exact run under C1's mutex. Another newer batch must
		// never participate in this execution's lifecycle.
		s.questionAnswerMu.Lock()
		run := s.questionAnswerRuns[questionAnswerRunKey(execution.UserID, targetID)]
		if run != nil && run.batchID != batchID {
			run = nil
		}
		var finalErr error
		if run != nil {
			finalErr = run.finalErr
		}
		s.questionAnswerMu.Unlock()
		if cancel || finalErr != nil {
			_, err := s.stopQuestionAnswerBatchCore(ctx, execution.UserID, targetID, batchID)
			if err != nil && !errors.Is(err, requestError(ErrorQuestionAnswerBatchNotFound)) {
				return err
			}
		}
		if run != nil && !questionAnswerChannelClosed(run.done) {
			return errQuestionAnswerScheduleFinalizationPending
		}
	}
	return nil
}
func (s *Service) persistQuestionAnswerScheduleSettling(ctx context.Context, execution QuestionAnswerScheduleExecution, settled bool, reason string, cause error) error {
	lock := s.questionAnswerScheduleWorkspaceLock(execution.UserID, execution.AdminAccountID)
	lock.Lock()
	finished, err := s.questionAnswerSchedules.FinishQuestionAnswerScheduleExecution(ctx, execution.UserID, execution.AdminAccountID, execution.ID, settled, reason, s.scheduleNow)
	lock.Unlock()
	if err == nil && finished != nil && !questionAnswerScheduleExecutionActive(finished.Status) {
		s.questionAnswerScheduleMu.Lock()
		handle := s.questionAnswerScheduleHandles[execution.ID]
		s.questionAnswerScheduleMu.Unlock()
		if handle != nil {
			s.removeQuestionAnswerScheduleHandle(handle)
		}
	}
	if errors.Is(cause, errQuestionAnswerScheduleFinalizationPending) {
		cause = nil
	}
	return errors.Join(err, cause)
}
func (s *Service) questionAnswerScheduleCallsExited(handle *questionAnswerScheduleHandle) bool {
	return (handle.prepareDone == nil || questionAnswerChannelClosed(handle.prepareDone)) && (handle.buildDone == nil || questionAnswerChannelClosed(handle.buildDone))
}
func (s *Service) waitQuestionAnswerScheduleCalls(ctx context.Context, handle *questionAnswerScheduleHandle) error {
	for _, done := range []chan struct{}{handle.prepareDone, handle.buildDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) CleanupDeletedQuestionAnswerSchedules(ctx context.Context, user, workspace string) error {
	var errs []error
	for _, handle := range s.questionAnswerScheduleHandleSnapshot() {
		if handle.identity.UserID != user || handle.identity.AdminAccountID != workspace {
			continue
		}
		handle.cancel()
		handle.mu.Lock()
		if err := s.waitQuestionAnswerScheduleCalls(ctx, handle); err != nil {
			handle.mu.Unlock()
			errs = append(errs, err)
			continue
		}
		s.releaseQuestionAnswerScheduleReservations(handle)
		err := s.settleQuestionAnswerScheduleBatches(ctx, handle, handle.execution, true)
		handle.mu.Unlock()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.removeQuestionAnswerScheduleHandle(handle)
	}
	return errors.Join(errs...)
}

func (s *Service) ShutdownQuestionAnswerSchedules(ctx context.Context) error {
	s.questionAnswerScheduleMu.Lock()
	cancel, done := s.questionAnswerScheduleCancel, s.questionAnswerScheduleDone
	s.questionAnswerScheduleMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

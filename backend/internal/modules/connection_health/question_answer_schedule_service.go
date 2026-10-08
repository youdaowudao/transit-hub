package connection_health

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

func (s *Service) scheduleNow() time.Time {
	if s.questionAnswerScheduleNow != nil {
		return s.questionAnswerScheduleNow().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) questionAnswerScheduleWorkspaceLock(user, workspace string) *sync.Mutex {
	lock, _ := s.questionAnswerScheduleWorkspaceLocks.LoadOrStore(user+"|"+workspace, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
func (s *Service) scheduleWorkspace(ctx context.Context, user string) (string, error) {
	workspace, err := s.currentAdminAccountID(ctx, user)
	if err != nil {
		return "", err
	}
	if s.questionAnswerSchedules == nil {
		return "", requestError(ErrorQuestionAnswerStorage)
	}
	return workspace, nil
}

func (s *Service) GetQuestionAnswerRuntimeSettings(ctx context.Context, user string) (QuestionAnswerRuntimeSettings, error) {
	if _, err := s.scheduleWorkspace(ctx, user); err != nil {
		return QuestionAnswerRuntimeSettings{}, err
	}
	result, err := s.questionAnswerSchedules.GetQuestionAnswerRuntimeSettings(ctx)
	return result, questionAnswerScheduleError(err)
}
func (s *Service) SaveQuestionAnswerRuntimeSettings(ctx context.Context, user string, limit int, version int64) (QuestionAnswerRuntimeSettings, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerRuntimeSettings{}, err
	}
	if limit < 1 || limit > 50 || version < 0 {
		return QuestionAnswerRuntimeSettings{}, requestError(ErrorRequest)
	}
	if _, err := s.scheduleWorkspace(ctx, user); err != nil {
		return QuestionAnswerRuntimeSettings{}, err
	}
	result, err := s.questionAnswerSchedules.SaveQuestionAnswerRuntimeSettings(ctx, limit, version)
	if err == nil {
		s.SetQuestionAnswerConcurrency(result.QuestionAnswerConcurrency, result.Version)
	}
	return result, questionAnswerScheduleError(err)
}
func (s *Service) GetQuestionAnswerScheduleLimits(ctx context.Context, user string) (QuestionAnswerScheduleLimits, error) {
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleLimits{}, err
	}
	result, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleLimits(ctx, user, workspace)
	return result, questionAnswerScheduleError(err)
}
func validQuestionAnswerScheduleLimits(l QuestionAnswerScheduleLimits) bool {
	return l.MaxEnabledQuestionAnswerSchedules >= 1 && l.MaxEnabledQuestionAnswerSchedules <= 100 && l.MaxScheduleTargets >= 1 && l.MaxScheduleTargets <= 200 && l.MaxScheduleRequestsPerExecution >= 1 && l.MaxScheduleRequestsPerExecution <= 10000 && l.DailyScheduledRequestLimit >= 1 && l.DailyScheduledRequestLimit <= 1000000 && l.MaxActiveScheduleExecutions >= 1 && l.MaxActiveScheduleExecutions <= 20 && l.MaxQueuedScheduledRequests >= 0 && l.MaxQueuedScheduledRequests <= 100000 && l.ScheduleExecutionTimeoutMinutes >= 30 && l.ScheduleExecutionTimeoutMinutes <= 1440 && l.ScheduleLateGraceMinutes >= 0 && l.ScheduleLateGraceMinutes <= 30
}
func (s *Service) SaveQuestionAnswerScheduleLimits(ctx context.Context, user string, input QuestionAnswerScheduleLimits, version int64) (QuestionAnswerScheduleLimits, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerScheduleLimits{}, err
	}
	if !validQuestionAnswerScheduleLimits(input) || version < 0 {
		return QuestionAnswerScheduleLimits{}, requestError(ErrorRequest)
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleLimits{}, err
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	defer lock.Unlock()
	result, err := s.questionAnswerSchedules.SaveQuestionAnswerScheduleLimits(ctx, user, workspace, input, version, s.scheduleNow)
	return result, questionAnswerScheduleError(err)
}

func normalizeQuestionAnswerScheduleConfig(config QuestionAnswerScheduleConfig, workspace string, withTime bool) (QuestionAnswerScheduleConfig, error) {
	config.Name = strings.TrimSpace(config.Name)
	config.SelectedGroupIDs = uniqueNonEmpty(config.SelectedGroupIDs)
	config.SelectedAccountTargetIDs = uniqueNonEmpty(config.SelectedAccountTargetIDs)
	config.Models = uniqueNonEmpty(config.Models)
	config.QuestionIDs = uniqueNonEmpty(config.QuestionIDs)
	if withTime && (config.Name == "" || utf8.RuneCountInString(config.Name) > 120) {
		return config, requestError(ErrorRequest)
	}
	if (config.TargetMode == "groups" && (len(config.SelectedGroupIDs) == 0 || len(config.SelectedAccountTargetIDs) > 0)) || (config.TargetMode == "accounts" && (len(config.SelectedAccountTargetIDs) == 0 || len(config.SelectedGroupIDs) > 0)) || (config.TargetMode != "groups" && config.TargetMode != "accounts") {
		return config, requestError(ErrorRequest)
	}
	for _, id := range config.SelectedAccountTargetIDs {
		if err := questionAnswerScheduleOwnedTarget("user", workspace, id); err != nil {
			return config, err
		}
	}
	if len(config.Models) == 0 || len(config.QuestionIDs) == 0 {
		return config, requestError(ErrorQuestionAnswerSelection)
	}
	effort, err := normalizeQuestionAnswerReasoningEffort(config.ReasoningEffort)
	if err != nil {
		return config, err
	}
	config.ReasoningEffort = string(effort)
	if config.RepeatCount < 1 || config.RepeatCount > QuestionAnswerRepeatCountLimit {
		return config, requestError(ErrorQuestionAnswerRepeatCount)
	}
	if _, err := questionAnswerSubmissionCount(len(config.Models), len(config.QuestionIDs), config.RepeatCount); err != nil {
		return config, err
	}
	if withTime {
		if err := validateQuestionAnswerScheduleTime(config); err != nil {
			return config, err
		}
	}
	return config, nil
}

type questionAnswerScheduleInventory struct {
	session       upstream.Session
	preview       []QuestionAnswerSchedulePreviewTarget
	accounts      map[string]upstream.AdminGroupAccountInfo
	memberships   map[string][]TestConfigurationSource
	allMissing    bool
	blockedReason string
}

func (s *Service) resolveQuestionAnswerScheduleTargets(ctx context.Context, user, workspace string, config QuestionAnswerScheduleConfig) (questionAnswerScheduleInventory, error) {
	result := questionAnswerScheduleInventory{preview: []QuestionAnswerSchedulePreviewTarget{}, accounts: map[string]upstream.AdminGroupAccountInfo{}, memberships: map[string][]TestConfigurationSource{}}
	if s.platformGroups == nil || s.mySites == nil {
		return result, requestError(ErrorTestConfigurationUnavailable)
	}
	session, err := s.mySites.RequireSession(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	if session.Platform != upstream.PlatformSub2API {
		return result, requestError(ErrorRequest)
	}
	result.session = session
	groups, err := s.fetchAdminAllGroups(ctx, session)
	if err != nil {
		return result, err
	}
	selected := map[string]bool{}
	for _, id := range config.SelectedGroupIDs {
		selected[id] = true
	}
	known := map[string]bool{}
	matched := map[string][]QuestionAnswerScheduleGroupRef{}
	selectedAccounts := map[string]bool{}
	for _, group := range groups {
		known[group.ID] = true
		members, err := s.listAdminGroupAccounts(ctx, session, group)
		if err != nil {
			return result, requestError(ErrorTestConfigurationUnavailable)
		}
		seen := map[string]bool{}
		for _, account := range members {
			id := buildTargetID(string(session.Platform), workspace, account.ID)
			if account.ID == "" || seen[id] {
				continue
			}
			seen[id] = true
			result.accounts[id] = account
			result.memberships[id] = append(result.memberships[id], TestConfigurationSource{AdminGroupID: group.ID, AdminGroupName: group.Name})
			if selected[group.ID] {
				selectedAccounts[id] = true
				matched[id] = append(matched[id], QuestionAnswerScheduleGroupRef{ID: group.ID, Name: group.Name})
			}
		}
	}
	for _, id := range config.SelectedGroupIDs {
		if !known[id] {
			result.blockedReason = "group_not_found"
			return result, nil
		}
	}
	targetIDs := []string{}
	if config.TargetMode == "accounts" {
		reader, ok := s.platformGroups.(interface {
			ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error)
		})
		if !ok {
			return result, requestError(ErrorTestConfigurationUnavailable)
		}
		accounts, err := reader.ListSub2APIAdminAccountsContext(ctx, session)
		if err != nil {
			return result, requestError(ErrorTestConfigurationUnavailable)
		}
		result.accounts = map[string]upstream.AdminGroupAccountInfo{}
		for _, account := range accounts {
			result.accounts[buildTargetID(string(session.Platform), workspace, account.ID)] = account
		}
		targetIDs = append(targetIDs, config.SelectedAccountTargetIDs...)
	} else {
		for id := range selectedAccounts {
			targetIDs = append(targetIDs, id)
		}
	}
	sort.Strings(targetIDs)
	missing := 0
	for _, id := range targetIDs {
		account, found := result.accounts[id]
		parsed, _ := parseTargetID(id)
		entry := QuestionAnswerSchedulePreviewTarget{TargetID: id, AccountID: parsed.accountID, AccountName: id, Platform: string(session.Platform), MatchedGroups: []QuestionAnswerScheduleGroupRef{}, ModelNames: []string{}, Missing: !found}
		if !found {
			missing++
		} else {
			entry.AccountName = account.Name
			entry.Platform = account.Platform
			entry.ModelNames = splitModelList(account.Models)
		}
		if config.TargetMode == "groups" {
			entry.MatchedGroups = append(entry.MatchedGroups, matched[id]...)
		} else if found {
			for _, m := range result.memberships[id] {
				entry.MatchedGroups = append(entry.MatchedGroups, QuestionAnswerScheduleGroupRef{ID: m.AdminGroupID, Name: m.AdminGroupName})
			}
		}
		result.preview = append(result.preview, entry)
	}
	result.allMissing = config.TargetMode == "accounts" && len(targetIDs) > 0 && missing == len(targetIDs)
	return result, nil
}

func (s *Service) previewQuestionAnswerSchedule(ctx context.Context, user, workspace string, config QuestionAnswerScheduleConfig) (QuestionAnswerScheduleSelectionSnapshot, []string, string, error) {
	inventory, err := s.resolveQuestionAnswerScheduleTargets(ctx, user, workspace, config)
	if err != nil {
		return QuestionAnswerScheduleSelectionSnapshot{}, nil, "", err
	}
	now := s.scheduleNow()
	snapshot := QuestionAnswerScheduleSelectionSnapshot{TargetPreview: inventory.preview, PreviewedAt: now, EstimatedTargetCount: len(inventory.preview), EstimatedRequestsPerTarget: len(config.Models) * len(config.QuestionIDs) * config.RepeatCount}
	snapshot.EstimatedRequestsPerExecution = snapshot.EstimatedTargetCount * snapshot.EstimatedRequestsPerTarget
	if config.PeakStart != "" {
		slots, err := questionAnswerScheduleSlotsBetween(config, now, now.Add(24*time.Hour))
		if err != nil {
			return snapshot, nil, "", err
		}
		snapshot.EstimatedRequestsPerDay = len(slots) * snapshot.EstimatedRequestsPerExecution
	}
	candidates := append([]string{}, config.Models...)
	for _, target := range inventory.preview {
		candidates = append(candidates, target.ModelNames...)
	}
	candidates = uniqueNonEmpty(candidates)
	sort.Strings(candidates)
	if inventory.blockedReason != "" {
		return snapshot, candidates, inventory.blockedReason, nil
	}
	if inventory.allMissing {
		return snapshot, candidates, "all_accounts_missing", nil
	}
	questions, _, err := s.questionAnswerSchedules.ReadQuestionAnswerSchedulePreparationConfiguration(ctx, user, workspace, config.QuestionIDs)
	if err != nil {
		if errors.Is(err, errQuestionAnswerUnavailable) {
			return snapshot, candidates, "question_unavailable", nil
		}
		return snapshot, candidates, "", err
	}
	if len(questions) != len(config.QuestionIDs) {
		return snapshot, candidates, "question_unavailable", nil
	}
	return snapshot, candidates, "", nil
}

func (s *Service) ListQuestionAnswerSchedules(ctx context.Context, user, status string, page, pageSize int) (QuestionAnswerSchedulePage, error) {
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedulePage{}, err
	}
	result, err := s.questionAnswerSchedules.ListQuestionAnswerSchedules(ctx, user, workspace, status, page, pageSize)
	return result, questionAnswerScheduleError(err)
}
func (s *Service) GetQuestionAnswerSchedule(ctx context.Context, user, id string) (QuestionAnswerSchedule, error) {
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedule{}, err
	}
	result, err := s.questionAnswerSchedules.GetQuestionAnswerSchedule(ctx, user, workspace, id)
	if err != nil {
		return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
	}
	if result == nil {
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	return *result, nil
}
func (s *Service) CreateQuestionAnswerSchedule(ctx context.Context, user string, input QuestionAnswerScheduleInput) (QuestionAnswerSchedule, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerSchedule{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedule{}, err
	}
	if input.Enabled == nil || input.ExpectedVersion != nil {
		return QuestionAnswerSchedule{}, requestError(ErrorRequest)
	}
	config, err := normalizeQuestionAnswerScheduleConfig(input.QuestionAnswerScheduleConfig, workspace, true)
	if err != nil {
		return QuestionAnswerSchedule{}, err
	}
	limits, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleLimits(ctx, user, workspace)
	if err != nil {
		return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
	}
	selection, _, reason, err := s.previewQuestionAnswerSchedule(ctx, user, workspace, config)
	if err != nil {
		return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
	}
	if reason == "" {
		reason = questionAnswerScheduleHardLimitReason(config, limits)
	}
	if reason != "" {
		return QuestionAnswerSchedule{}, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid}
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	defer lock.Unlock()
	result, err := s.questionAnswerSchedules.CreateQuestionAnswerSchedule(ctx, user, workspace, config, *input.Enabled, selection, s.scheduleNow)
	if result == nil {
		if err != nil {
			return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
		}
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	return *result, questionAnswerScheduleError(err)
}
func (s *Service) UpdateQuestionAnswerSchedule(ctx context.Context, user, id string, input QuestionAnswerScheduleInput) (QuestionAnswerSchedule, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerSchedule{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedule{}, err
	}
	if input.Enabled != nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		return QuestionAnswerSchedule{}, requestError(ErrorRequest)
	}
	current, err := s.questionAnswerSchedules.GetQuestionAnswerSchedule(ctx, user, workspace, id)
	if err != nil {
		return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
	}
	if current == nil {
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	if current.Version != *input.ExpectedVersion {
		return *current, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleVersionConflict, Current: current}
	}
	config, err := normalizeQuestionAnswerScheduleConfig(input.QuestionAnswerScheduleConfig, workspace, true)
	if err != nil {
		return *current, err
	}
	selection, _, reason, err := s.previewQuestionAnswerSchedule(ctx, user, workspace, config)
	if err != nil {
		return *current, questionAnswerScheduleError(err)
	}
	limits, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleLimits(ctx, user, workspace)
	if err != nil {
		return *current, questionAnswerScheduleError(err)
	}
	if reason == "" {
		reason = questionAnswerScheduleHardLimitReason(config, limits)
	}
	if reason != "" {
		return *current, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: current}
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	defer lock.Unlock()
	result, err := s.questionAnswerSchedules.UpdateQuestionAnswerSchedule(ctx, user, workspace, id, config, selection, *input.ExpectedVersion, s.scheduleNow)
	if result == nil {
		if err != nil {
			return *current, questionAnswerScheduleError(err)
		}
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	return *result, questionAnswerScheduleError(err)
}
func (s *Service) PreviewQuestionAnswerSchedule(ctx context.Context, user string, input QuestionAnswerSchedulePreviewInput) (QuestionAnswerSchedulePreview, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerSchedulePreview{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedulePreview{}, err
	}
	config := QuestionAnswerScheduleConfig{TargetMode: input.TargetMode, SelectedGroupIDs: input.SelectedGroupIDs, SelectedAccountTargetIDs: input.SelectedAccountTargetIDs, Models: input.Models, QuestionIDs: input.QuestionIDs, ReasoningEffort: input.ReasoningEffort, RepeatCount: input.RepeatCount}
	config, err = normalizeQuestionAnswerScheduleConfig(config, workspace, false)
	if err != nil {
		return QuestionAnswerSchedulePreview{}, err
	}
	var baseline *QuestionAnswerSchedule
	if input.ScheduleID != "" {
		baseline, err = s.questionAnswerSchedules.GetQuestionAnswerSchedule(ctx, user, workspace, input.ScheduleID)
		if err != nil {
			return QuestionAnswerSchedulePreview{}, questionAnswerScheduleError(err)
		}
		if baseline == nil {
			return QuestionAnswerSchedulePreview{}, requestError(ErrorQuestionAnswerScheduleNotFound)
		}
	}
	selection, candidates, reason, err := s.previewQuestionAnswerSchedule(ctx, user, workspace, config)
	if err != nil {
		return QuestionAnswerSchedulePreview{}, questionAnswerScheduleError(err)
	}
	result := QuestionAnswerSchedulePreview{EstimatedTargetCount: selection.EstimatedTargetCount, EstimatedRequestsPerTarget: selection.EstimatedRequestsPerTarget, EstimatedRequestsPerExecution: selection.EstimatedRequestsPerExecution, TargetPreview: selection.TargetPreview, ModelCandidates: candidates, Warnings: []string{}, PreviewedAt: selection.PreviewedAt, TargetChanges: QuestionAnswerScheduleTargetChanges{Added: []QuestionAnswerSchedulePreviewTarget{}, Removed: []QuestionAnswerSchedulePreviewTarget{}}}
	if reason != "" {
		result.Warnings = append(result.Warnings, reason)
	}
	if baseline != nil {
		result.ScheduleVersion = &baseline.Version
		old, new := map[string]bool{}, map[string]bool{}
		for _, t := range baseline.TargetSelectionSnapshot.TargetPreview {
			old[t.TargetID] = true
		}
		for _, t := range result.TargetPreview {
			new[t.TargetID] = true
			if !old[t.TargetID] {
				result.TargetChanges.Added = append(result.TargetChanges.Added, t)
			}
		}
		for _, t := range baseline.TargetSelectionSnapshot.TargetPreview {
			if !new[t.TargetID] {
				result.TargetChanges.Removed = append(result.TargetChanges.Removed, t)
			}
		}
	}
	return result, nil
}
func (s *Service) SetQuestionAnswerScheduleState(ctx context.Context, user, id, action string, version int64) (QuestionAnswerSchedule, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerSchedule{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerSchedule{}, err
	}
	if version < 0 || (action != "enable" && action != "disable" && action != "revalidate" && action != "delete") {
		return QuestionAnswerSchedule{}, requestError(ErrorRequest)
	}
	current, err := s.questionAnswerSchedules.GetQuestionAnswerSchedule(ctx, user, workspace, id)
	if err != nil {
		return QuestionAnswerSchedule{}, questionAnswerScheduleError(err)
	}
	if current == nil {
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	if current.Version != version {
		return *current, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleVersionConflict, Current: current}
	}
	reason := ""
	if action == "enable" || action == "revalidate" {
		_, _, reason, err = s.previewQuestionAnswerSchedule(ctx, user, workspace, current.QuestionAnswerScheduleConfig)
		if err != nil {
			return *current, questionAnswerScheduleError(err)
		}
		limits, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleLimits(ctx, user, workspace)
		if err != nil {
			return *current, questionAnswerScheduleError(err)
		}
		if reason == "" {
			reason = questionAnswerScheduleHardLimitReason(current.QuestionAnswerScheduleConfig, limits)
		}
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	defer lock.Unlock()
	result, err := s.questionAnswerSchedules.SetQuestionAnswerScheduleState(ctx, user, workspace, id, action, reason, version, s.scheduleNow)
	if result == nil {
		if err != nil {
			return *current, questionAnswerScheduleError(err)
		}
		return QuestionAnswerSchedule{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	return *result, questionAnswerScheduleError(err)
}
func (s *Service) RunQuestionAnswerSchedule(ctx context.Context, user, id, requestID string) (QuestionAnswerScheduleExecution, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerScheduleExecution{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleExecution{}, err
	}
	if !validQuestionAnswerScheduleRequestID(requestID) {
		return QuestionAnswerScheduleExecution{}, requestError(ErrorRequest)
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	result, err := s.questionAnswerSchedules.CreateRunNowQuestionAnswerScheduleExecution(ctx, user, workspace, id, requestID, s.scheduleNow)
	lock.Unlock()
	if err != nil {
		return QuestionAnswerScheduleExecution{}, questionAnswerScheduleError(err)
	}
	if result == nil {
		return QuestionAnswerScheduleExecution{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	s.wakeQuestionAnswerScheduleCoordinator()
	return *result, nil
}
func validQuestionAnswerScheduleRequestID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
func (s *Service) ListQuestionAnswerScheduleExecutions(ctx context.Context, user, id string, page, pageSize int) (QuestionAnswerScheduleExecutionPage, error) {
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleExecutionPage{}, err
	}
	result, err := s.questionAnswerSchedules.ListQuestionAnswerScheduleExecutions(ctx, user, workspace, id, page, pageSize)
	return result, questionAnswerScheduleError(err)
}
func (s *Service) GetQuestionAnswerScheduleExecution(ctx context.Context, user, id string) (QuestionAnswerScheduleExecution, error) {
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleExecution{}, err
	}
	result, err := s.questionAnswerSchedules.GetQuestionAnswerScheduleExecution(ctx, user, workspace, id)
	if err != nil {
		return QuestionAnswerScheduleExecution{}, questionAnswerScheduleError(err)
	}
	if result == nil {
		return QuestionAnswerScheduleExecution{}, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	return *result, nil
}
func (s *Service) CancelQuestionAnswerScheduleExecution(ctx context.Context, user, id string, version int64) (QuestionAnswerScheduleTerminationResult, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return QuestionAnswerScheduleTerminationResult{}, err
	}
	workspace, err := s.scheduleWorkspace(ctx, user)
	if err != nil {
		return QuestionAnswerScheduleTerminationResult{}, err
	}
	if version < 0 {
		return QuestionAnswerScheduleTerminationResult{}, requestError(ErrorRequest)
	}
	lock := s.questionAnswerScheduleWorkspaceLock(user, workspace)
	lock.Lock()
	result, err := s.questionAnswerSchedules.DecideQuestionAnswerScheduleTermination(ctx, user, workspace, id, "user_cancel", &version, s.scheduleNow)
	lock.Unlock()
	if err == nil && result.Accepted {
		s.cancelQuestionAnswerScheduleHandle(id)
		s.wakeQuestionAnswerScheduleCoordinator()
	} else if err == nil && result.Conflict && result.Execution.Status == "active" && result.Execution.StatusReason == "c1_finalization_pending" {
		// The transaction already decided that all business work is terminal.
		// Confirm the original runtime has also exited before reporting current;
		// this must never turn normal completion into a newly accepted cancel.
		current, settleErr := s.reconcileQuestionAnswerScheduleCancellationConflict(ctx, result.Execution)
		if settleErr != nil {
			return result, questionAnswerScheduleError(settleErr)
		}
		if current == nil {
			return result, requestError(ErrorQuestionAnswerScheduleNotFound)
		}
		result.Execution = *current
		s.wakeQuestionAnswerScheduleCoordinator()
	}
	return result, questionAnswerScheduleError(err)
}

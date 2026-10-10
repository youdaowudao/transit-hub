package connection_health

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"transithub/backend/internal/modules/upstream"
	"unicode/utf8"
)

func (s *Service) modelControlScope(ctx context.Context, user, target string) (string, error) {
	workspace, err := s.currentAdminAccountID(ctx, user)
	if err != nil {
		return "", err
	}
	if target != "" {
		if err = questionAnswerScheduleOwnedTarget(user, workspace, target); err != nil {
			return "", err
		}
	}
	session, err := s.mySites.RequireSession(ctx, user, workspace)
	if err != nil {
		return "", err
	}
	if session.Platform != upstream.PlatformSub2API {
		return "", requestError(modelControlError("Unsupported"))
	}
	if s.modelControls == nil {
		return "", requestError(modelControlError("Storage"))
	}
	return workspace, nil
}
func validModelControlModel(model string) bool {
	return strings.TrimSpace(model) == model && model != "" && utf8.RuneCountInString(model) <= 200
}
func (s *Service) modelControlItems(ctx context.Context, user, workspace string) ([]ModelControlItem, []modelControlTarget, error) {
	targets, err := s.modelControls.ListModelControlTargets(ctx, user, workspace)
	if err != nil {
		return nil, nil, requestError(modelControlError("Storage"))
	}
	settings, err := s.modelControls.GetModelControlSettings(ctx, user, workspace)
	if err != nil {
		return nil, nil, requestError(modelControlError("Storage"))
	}
	rounds := map[modelControlPair][]modelControlRound{}
	coverage := map[modelControlPair][]modelControlScheduleCoverage{}
	for start := 0; start < len(targets); start += 50 {
		end := start + 50
		if end > len(targets) {
			end = len(targets)
		}
		pairs := []modelControlPair{}
		for _, t := range targets[start:end] {
			pairs = append(pairs, modelControlPair{t.TargetID, t.ModelName})
		}
		rs, err := s.modelControls.ListModelControlRounds(ctx, user, pairs)
		if err != nil {
			return nil, nil, requestError(modelControlError("Storage"))
		}
		cs, err := s.modelControls.ListModelControlCoverage(ctx, user, workspace, pairs)
		if err != nil {
			return nil, nil, requestError(modelControlError("Storage"))
		}
		for p, r := range rs {
			rounds[p] = r
		}
		for p, c := range cs {
			coverage[p] = c
		}
	}
	now := time.Now().UTC()
	accountPending := map[string]*modelControlAccountPending{}
	for _, t := range targets {
		if t.Pending != nil {
			accountPending[t.TargetID] = &modelControlAccountPending{t.ModelName, t.Pending.Operation, modelControlPendingState(t.Pending, now)}
		}
	}
	items := []ModelControlItem{}
	for _, t := range targets {
		rule := modelControlWorkspaceRule(settings, t.ModelName)
		p := modelControlPair{t.TargetID, t.ModelName}
		item := buildModelControlItem(t, rule, rounds[p], accountPending[t.TargetID], now)
		item.Coverage.Schedules = coverage[p]
		if item.Coverage.Schedules == nil {
			item.Coverage.Schedules = []modelControlScheduleCoverage{}
		}
		state, err := s.repo.GetState(ctx, t.TargetID, t.ModelName)
		if err != nil {
			return nil, nil, requestError(modelControlError("Storage"))
		}
		if state != nil {
			value := string(state.State)
			item.Health.State = &value
			item.Health.RecentlyProbed = state.LastProbeAt != nil && now.Sub(*state.LastProbeAt) <= 24*time.Hour
		}
		items = append(items, item)
	}
	return items, targets, nil
}
func buildModelControlItem(t modelControlTarget, rule ModelControlRule, rounds []modelControlRound, accountPending *modelControlAccountPending, now time.Time) ModelControlItem {
	latest, previous, _ := selectModelControlRounds(rounds, rule)
	decision := evaluateModelControlDecision(latest, rule)
	if latest != nil && !latest.Running && !modelControlRoundIsToday(latest, now) {
		decision.Decision, decision.Reason = "no_evidence", "not_today"
	}
	basis := modelControlBasis{RuleVersion: rule.Version, Decision: decision.Decision, BusinessDay: now.In(questionAnswerScheduleLocation).Format("2006-01-02")}
	if latest != nil {
		basis.BatchID = latest.BatchID
	}
	control := modelControlControl{ClosedEntries: cloneModelMapping(t.ClosedEntries), ClosedAt: t.ClosedAt, ClosedAccuracyPercent: t.ClosedAccuracyPercent, AccountPending: accountPending, ConflictReason: t.ConflictReason, Observation: t.Observation, LastAttempt: t.LastAttempt}
	if control.Observation.Sources == nil {
		control.Observation.Sources = []modelSourceCount{}
	}
	if t.Pending != nil {
		p := t.Pending
		control.Pending = &modelControlPendingView{p.Operation, p.Phase, p.Receipt, modelControlPendingState(p, now), p.StartedAt, p.SendStartedAt}
	}
	if u := t.UnconfirmedClose; u != nil && now.Before(u.SentAt.Add(24*time.Hour)) {
		control.UnconfirmedClose = &modelControlUnconfirmedView{cloneModelMapping(u.Entries), u.SentAt, u.SentAt.Add(24 * time.Hour)}
	}
	item := ModelControlItem{TargetID: t.TargetID, AccountName: t.AccountName, ModelName: t.ModelName, Version: t.Version, Rule: rule, Round: latest, PreviousRound: previous, Decision: decision.Decision, DecisionReason: decision.Reason, Basis: basis, Control: control, Coverage: modelControlCoverage{Schedules: []modelControlScheduleCoverage{}}}
	item.Attention = modelControlNeedsAttention(item)
	return item
}
func (s *Service) getModelControlItem(ctx context.Context, user, workspace, target, model string) (ModelControlItem, modelControlTarget, error) {
	items, targets, err := s.modelControlItems(ctx, user, workspace)
	if err != nil {
		return ModelControlItem{}, modelControlTarget{}, err
	}
	for n, item := range items {
		if item.TargetID == target && item.ModelName == model {
			return item, targets[n], nil
		}
	}
	return ModelControlItem{}, modelControlTarget{}, requestError(ErrorProbeTargetNotFound)
}
func modelControlWorkspaceRule(settings ModelControlSettings, model string) ModelControlRule {
	return ModelControlRule{ModelName: model, MinAccuracyPercent: settings.MinAccuracyPercent, MinJudgedAnswers: settings.MinJudgedAnswers, IncludeManual: true, IncludeScheduled: true, Version: settings.Version}
}
func (s *Service) GetModelControlSettings(ctx context.Context, user string) (ModelControlSettings, error) {
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return ModelControlSettings{}, err
	}
	return s.modelControls.GetModelControlSettings(ctx, user, ws)
}
func (s *Service) SaveModelControlSettings(ctx context.Context, user string, input ModelControlSettings, expected int64) (ModelControlSettings, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return input, err
	}
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return input, err
	}
	if input.MinAccuracyPercent < 1 || input.MinAccuracyPercent > 100 || input.MinJudgedAnswers < 1 || input.MinJudgedAnswers > 50 || expected < 0 {
		return input, requestError(ErrorRequest)
	}
	return s.modelControls.SaveModelControlSettings(ctx, user, ws, input, expected)
}
func (s *Service) AddManagedModel(ctx context.Context, user, target, model string) (ModelControlItem, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return ModelControlItem{}, err
	}
	ws, err := s.modelControlScope(ctx, user, target)
	if err != nil {
		return ModelControlItem{}, err
	}
	if !validModelControlModel(model) {
		return ModelControlItem{}, requestError(ErrorRequest)
	}
	_, err = s.modelControls.InsertModelControlTarget(ctx, user, ws, target, model)
	if err != nil {
		return ModelControlItem{}, err
	}
	result, err := s.VerifyModelControl(ctx, user, []string{target})
	verifyKey := ""
	if err != nil {
		verifyKey = modelControlError("AccountReadFailed")
	} else if len(result.Errors) > 0 {
		verifyKey = result.Errors[0].ReasonKey
	}
	item, _, err := s.getModelControlItem(ctx, user, ws, target, model)
	item.VerifyErrorKey = verifyKey
	return item, err
}
func (s *Service) RemoveManagedModel(ctx context.Context, user, target, model string, version int64, abandon bool) error {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return err
	}
	ws, err := s.modelControlScope(ctx, user, target)
	if err != nil {
		return err
	}
	leased, release, ok, err := s.acquireActionTargetLease(ctx, target, false)
	if err != nil {
		return err
	}
	if !ok {
		return modelControlConflict("Busy", nil)
	}
	defer release()
	item, object, err := s.getModelControlItem(leased, user, ws, target, model)
	if err != nil {
		return err
	}
	err = s.modelControls.mutateModelControlOwnership(leased, user, ws, target, false, "remove", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
		for _, t := range objects {
			if t.Pending != nil {
				return nil, modelControlConflict("Pending", item)
			}
		}
		for _, t := range objects {
			if t.ID != object.ID {
				continue
			}
			if t.Version != version {
				return nil, modelControlConflict("VersionConflict", item)
			}
			hasUnconfirmed := t.UnconfirmedClose != nil && now.Before(t.UnconfirmedClose.SentAt.Add(24*time.Hour))
			if (!abandon && (len(t.ClosedEntries) > 0 || hasUnconfirmed)) || t.Observation.State == "account_missing" && !abandon {
				return nil, modelControlConflict("AbandonRequired", item)
			}
			event := "managed_removed"
			if abandon {
				event = "managed_abandoned"
			}
			detail := map[string]any{"closedEntries": t.ClosedEntries, "unconfirmedClose": t.UnconfirmedClose}
			t.deleted = true
			return []ModelControlEvent{modelControlNewEvent(user, ws, target, model, event, nil, detail)}, nil
		}
		return nil, requestError(ErrorProbeTargetNotFound)
	})
	var commit *modelControlCommitError
	if errors.As(err, &commit) {
		_, committed, readErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
		if readErr != nil {
			return readErr
		}
		if !committed {
			return modelControlConflict("Storage", item)
		}
		return nil
	}
	return err
}
func (s *Service) ListModelControlAccountSummaries(ctx context.Context, user, workspace string, targetIDs []string) (map[string]*ModelControlAccountSummary, error) {
	if reader, ok := s.modelControls.(interface {
		ListModelControlAccountSummaries(context.Context, string, string, []string) (map[string]*ModelControlAccountSummary, error)
	}); ok {
		return reader.ListModelControlAccountSummaries(ctx, user, workspace, targetIDs)
	}
	return listModelControlAccountSummaries(ctx, s.modelControls, user, workspace, targetIDs)
}
func (r *Repository) ListModelControlAccountSummaries(ctx context.Context, user, workspace string, targetIDs []string) (map[string]*ModelControlAccountSummary, error) {
	return r.queryModelControlAccountSummaries(ctx, user, workspace, targetIDs)
}
func listModelControlAccountSummaries(ctx context.Context, repo modelControlRepository, user, workspace string, targetIDs []string) (map[string]*ModelControlAccountSummary, error) {
	result := map[string]*ModelControlAccountSummary{}
	if repo == nil {
		return result, nil
	}
	all, err := repo.ListModelControlTargets(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	settings, err := repo.GetModelControlSettings(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range targetIDs {
		wanted[id] = true
	}
	targets := []modelControlTarget{}
	for _, t := range all {
		if wanted[t.TargetID] {
			targets = append(targets, t)
		}
	}
	now := time.Now().UTC()
	for start := 0; start < len(targets); start += 50 {
		end := start + 50
		if end > len(targets) {
			end = len(targets)
		}
		pairs := []modelControlPair{}
		for _, t := range targets[start:end] {
			pairs = append(pairs, modelControlPair{t.TargetID, t.ModelName})
		}
		rounds, err := repo.ListModelControlRounds(ctx, user, pairs)
		if err != nil {
			return nil, err
		}
		for _, t := range targets[start:end] {
			summary := result[t.TargetID]
			if summary == nil {
				summary = &ModelControlAccountSummary{}
				result[t.TargetID] = summary
			}
			item := buildModelControlItem(t, modelControlWorkspaceRule(settings, t.ModelName), rounds[modelControlPair{t.TargetID, t.ModelName}], nil, now)
			appendModelControlSummary(summary, t, item)
		}
	}
	for _, summary := range result {
		sortModelControlSummary(summary)
	}
	return result, nil
}
func (s *Service) ModelControlVerifyTargets(ctx context.Context, user string) ([]string, error) {
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return nil, err
	}
	targets, err := s.modelControls.ListModelControlTargets(ctx, user, ws)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := []string{}
	for _, t := range targets {
		if !seen[t.TargetID] {
			seen[t.TargetID] = true
			result = append(result, t.TargetID)
		}
	}
	sort.Strings(result)
	return result, nil
}

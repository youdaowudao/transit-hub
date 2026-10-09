package connection_health

import (
	"context"
	"errors"
	"log"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type modelControlComputedPlan struct {
	Preview ModelControlPreview
	Close   modelClosePlan
	Restore modelRestorePlan
	Detail  upstream.Sub2APIModelControlAccount
	Refresh adminTargetRefresh
	At      time.Time
}

func modelControlAdmission(item ModelControlItem, op string) error {
	if item.Control.AccountPending != nil {
		return modelControlConflict("Pending", item)
	}
	if op == "close" || op == "close_account" {
		if item.Decision != "close_recommended" {
			return modelControlConflict("NotEligible", item)
		}
	} else if op == "restore" {
		if len(item.Control.ClosedEntries) == 0 || item.Decision == "testing" {
			return modelControlConflict("NotEligible", item)
		}
	} else {
		return requestError(ErrorRequest)
	}
	return nil
}
func modelControlBasisMatches(a, b modelControlBasis) bool {
	return a.BatchID == b.BatchID && a.RuleVersion == b.RuleVersion && a.Decision == b.Decision
}
func (s *Service) modelControlInput(ctx context.Context, user, target, model, op string, basis modelControlBasis) (upstream.Session, string, string, ModelControlItem, modelControlTarget, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return upstream.Session{}, "", "", ModelControlItem{}, modelControlTarget{}, err
	}
	ws, err := s.modelControlScope(ctx, user, target)
	if err != nil {
		return upstream.Session{}, "", "", ModelControlItem{}, modelControlTarget{}, err
	}
	if !validModelControlModel(model) {
		return upstream.Session{}, "", "", ModelControlItem{}, modelControlTarget{}, requestError(ErrorRequest)
	}
	session, workspace, account, err := s.resolveManualSession(ctx, user, target)
	if err != nil {
		return session, workspace, account, ModelControlItem{}, modelControlTarget{}, err
	}
	if s.modelControlActions == nil {
		return session, workspace, account, ModelControlItem{}, modelControlTarget{}, requestError(modelControlError("Unsupported"))
	}
	item, object, err := s.getModelControlItem(ctx, user, ws, target, model)
	if err != nil {
		return session, workspace, account, item, object, err
	}
	if !modelControlBasisMatches(basis, item.Basis) {
		return session, workspace, account, item, object, modelControlConflict("BasisChanged", item)
	}
	return session, workspace, account, item, object, modelControlAdmission(item, op)
}
func (s *Service) modelControlReservations(ctx context.Context, user, workspace string) (map[string]bool, map[string]map[string]bool, error) {
	health, err := s.repo.ListTargetActionStates(ctx, user, workspace)
	if err != nil {
		return nil, nil, err
	}
	hp := map[string]bool{}
	for _, state := range health {
		if targetActionPending(&state) {
			if p, ok := parseTargetID(state.TargetID); ok {
				hp[p.accountID] = true
			}
		}
	}
	objects, err := s.modelControls.ListModelControlTargets(ctx, user, workspace)
	if err != nil {
		return nil, nil, err
	}
	reserved := map[string]map[string]bool{}
	now := time.Now()
	for _, t := range objects {
		p, ok := parseTargetID(t.TargetID)
		if !ok {
			continue
		}
		keys := reserved[p.accountID]
		if keys == nil {
			keys = map[string]bool{}
			reserved[p.accountID] = keys
		}
		for k := range t.ClosedEntries {
			keys[k] = true
		}
		if t.Pending != nil {
			keys["*"] = true
			for k := range t.Pending.Entries {
				keys[k] = true
			}
		}
		if u := t.UnconfirmedClose; u != nil && now.Before(u.SentAt.Add(24*time.Hour)) {
			for k := range u.Entries {
				keys[k] = true
			}
		}
	}
	return hp, reserved, nil
}
func (s *Service) computeModelControlPlan(ctx context.Context, user, workspace, op string, item ModelControlItem, object modelControlTarget, detail upstream.Sub2APIModelControlAccount, refresh adminTargetRefresh, at time.Time) (modelControlComputedPlan, error) {
	p := modelControlComputedPlan{Preview: ModelControlPreview{Item: item, Entries: []ModelControlEntry{}, Groups: []modelSourceCount{}, AccountStatus: detail.Status, AccountSchedulable: detail.Schedulable}, Detail: detail, Refresh: refresh, At: at}
	p.Close = planModelClose(detail, item.ModelName)
	entries := map[string]string{}
	switch op {
	case "close":
		entries = p.Close.Entries
		p.Preview.Entries = modelMappingEntries(entries, "to_close")
	case "restore":
		p.Restore = planModelRestore(detail, object.ClosedEntries)
		entries = p.Restore.Entries
		p.Preview.Entries = modelMappingEntries(entries, "to_restore")
		p.Preview.Entries = append(p.Preview.Entries, modelMappingEntries(p.Restore.ManualRestored, "manually_restored")...)
		p.Preview.Entries = append(p.Preview.Entries, modelMappingEntries(p.Restore.ManualChanged, "manually_changed")...)
		p.Preview.NoRemoteWrite = p.Restore.NoRemoteWrite
	case "close_account":
		entries = detail.ModelMapping
		p.Preview.Entries = modelMappingEntries(entries, "to_close")
	}
	p.Preview.PlanFingerprint = modelControlPlanFingerprint(op, detail.ModelMapping, entries, detail.Schedulable)
	hp, reserved, err := s.modelControlReservations(ctx, user, workspace)
	if err != nil {
		return p, requestError(modelControlError("Storage"))
	}
	p.Preview.Groups = countModelSources(refresh.inventory, detail.AdminGroupAccountInfo, entries, hp, reserved, time.Now().UTC())
	switch {
	case refresh.accountsReadError || !adminInventoryComplete(refresh.inventory):
		p.Preview.BlockReasonKey = modelControlError("InventoryIncomplete")
	case op == "close" && p.Close.ReasonKey != "":
		p.Preview.BlockReasonKey = p.Close.ReasonKey
	case op == "close_account" && p.Close.State != "last_model":
		p.Preview.BlockReasonKey = modelControlError("NotEligible")
	case op == "close_account" && detail.Schedulable != nil && !*detail.Schedulable:
		p.Preview.BlockReasonKey = modelControlError("AccountAlreadyUnschedulable")
	case op == "restore" && p.Restore.ReasonKey != "":
		p.Preview.BlockReasonKey = p.Restore.ReasonKey
	}
	if p.Preview.BlockReasonKey == "" && (op == "close" || op == "close_account") && modelControlTargetUsable(detail.AdminGroupAccountInfo) {
		for _, g := range p.Preview.Groups {
			if !g.OK {
				p.Preview.BlockReasonKey = modelControlError("FloorInsufficient")
				break
			}
		}
	}
	if p.Preview.BlockReasonKey == "" && op == "restore" && !p.Restore.NoRemoteWrite {
		allowed := true
		p.Preview.RequestHealth.Checked = true
		p.Preview.RequestHealth.Allowed = &allowed
		specs, _, ok := s.currentScheduledProbeSpecs(ctx, user, workspace, refresh.target, refresh.memberships, nil)
		if !ok {
			p.Preview.BlockReasonKey = modelControlError("ProbeSpecsUnavailable")
		} else {
			for _, spec := range specs {
				if spec.modelName != item.ModelName {
					continue
				}
				state, err := s.repo.GetState(ctx, item.TargetID, item.ModelName)
				if err != nil {
					return p, requestError(modelControlError("Storage"))
				}
				if state != nil && (state.State == StateSuspended || state.State == StateDisabled) {
					p.Preview.BlockReasonKey = modelControlError("RequestHealthSuspended")
				}
			}
		}
		if p.Preview.BlockReasonKey != "" {
			allowed = false
			p.Preview.RequestHealth.ReasonKey = p.Preview.BlockReasonKey
		}
	}
	return p, nil
}
func modelControlObservedState(detail upstream.Sub2APIModelControlAccount, object modelControlTarget) (string, string) {
	close := planModelClose(detail, object.ModelName)
	if len(object.ClosedEntries) > 0 {
		if len(close.Entries) > 0 {
			return "partially_closed", close.ReasonKey
		}
		return "closed", close.ReasonKey
	}
	return close.State, close.ReasonKey
}
func (s *Service) persistModelControlPlan(ctx context.Context, object modelControlTarget, op string, plan modelControlComputedPlan) (modelControlTarget, error) {
	object.AccountName = plan.Detail.Name
	object.Observation.AccountStatus = plan.Detail.Status
	object.Observation.AccountSchedulable = plan.Detail.Schedulable
	object.Observation.State, object.Observation.ReasonKey = modelControlObservedState(plan.Detail, object)
	object.Observation.Sources = plan.Preview.Groups
	setAttempt := false
	event := ""
	if plan.Preview.BlockReasonKey != "" {
		setAttempt = true
		object.LastAttempt = &modelControlAttempt{Operation: op, Outcome: "blocked", ReasonKey: plan.Preview.BlockReasonKey, Entries: plan.Preview.Entries, Groups: plan.Preview.Groups, At: plan.At}
		if op == "restore" {
			if plan.Restore.ReasonKey != "" {
				object.ConflictReason = plan.Restore.ReasonKey
				event = "conflict_detected"
			} else {
				event = "restore_blocked"
			}
		} else if op == "close_account" {
			event = "account_schedulable_blocked"
		} else {
			event = "close_blocked"
		}
	} else if op == "restore" {
		object.ConflictReason = ""
	}
	updated, err := s.modelControls.observeModelControlTarget(ctx, object, plan.At, setAttempt, event)
	if err != nil {
		return object, requestError(modelControlError("ReasonSaveFailed"))
	}
	return updated, nil
}
func (s *Service) loadModelControlPlan(ctx context.Context, user, workspace, account, op string, session upstream.Session, item ModelControlItem, object modelControlTarget) (modelControlComputedPlan, error) {
	refresh, err := s.refreshAdminTarget(ctx, session, workspace, account)
	if err != nil {
		refresh = adminTargetRefresh{accountsReadError: true, target: AdminProbeTarget{TargetID: object.TargetID, AccountID: account, Platform: string(session.Platform)}}
	}
	at := time.Now().UTC()
	detail, err := s.modelControlActions.ReadSub2APIModelControlAccountContext(ctx, session, account)
	if err != nil {
		return modelControlComputedPlan{At: at}, err
	}
	if refresh.target.TargetID == "" {
		refresh.target = AdminProbeTarget{TargetID: object.TargetID, AccountID: account, Platform: string(session.Platform), ProviderFamily: detail.Platform}
	}
	return s.computeModelControlPlan(ctx, user, workspace, op, item, object, detail, refresh, at)
}
func (s *Service) PreviewModelControl(ctx context.Context, user, target, model, op string, basis modelControlBasis) (ModelControlPreview, error) {
	session, ws, account, item, object, err := s.modelControlInput(ctx, user, target, model, op, basis)
	if err != nil {
		return ModelControlPreview{}, err
	}
	plan, err := s.loadModelControlPlan(ctx, user, ws, account, op, session, item, object)
	if errors.Is(err, upstream.ErrSub2APIModelControlAccountMissing) {
		if err = s.resolveMissingModelControlAccount(ctx, user, ws, target, account, session, plan.At); err != nil {
			return ModelControlPreview{}, err
		}
		latest, _, readErr := s.getModelControlItem(ctx, user, ws, target, model)
		if readErr != nil {
			return ModelControlPreview{}, readErr
		}
		return ModelControlPreview{Item: latest, Entries: []ModelControlEntry{}, Groups: []modelSourceCount{}, BlockReasonKey: modelControlError("AccountMissing")}, nil
	}
	if err != nil {
		return ModelControlPreview{}, requestError(modelControlError("AccountReadFailed"))
	}
	_, err = s.persistModelControlPlan(ctx, object, op, plan)
	if err != nil {
		return ModelControlPreview{}, err
	}
	plan.Preview.Item, _, err = s.getModelControlItem(ctx, user, ws, target, model)
	return plan.Preview, err
}
func (s *Service) modelControlResult(ctx context.Context, user, ws, target, model, outcome, reason string, entries []ModelControlEntry, groups []modelSourceCount) (ModelControlResult, error) {
	item, _, err := s.readModelControlItemAfterCommit(ctx, user, ws, target, model)
	if err != nil {
		return ModelControlResult{}, err
	}
	if entries == nil {
		entries = []ModelControlEntry{}
	}
	if groups == nil {
		groups = []modelSourceCount{}
	}
	result := ModelControlResult{Item: item, Outcome: outcome, ReasonKey: reason, Entries: entries, Groups: groups, HintKeys: []string{}}
	if item.Control.Observation.AccountSchedulable != nil && !*item.Control.Observation.AccountSchedulable {
		result.HintKeys = append(result.HintKeys, modelControlError("AccountUnschedulable"))
	}
	return result, err
}
func modelControlRemoteContext(prep context.Context, duration time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(prep), duration)
	stops := []func() bool{}
	for _, h := range []*RuntimeLeaseHandle{actionLeaseFromContext(prep), mutationLeaseFromContext(prep)} {
		if h != nil {
			stops = append(stops, context.AfterFunc(h.Context, cancel))
		}
	}
	return ctx, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}
func (s *Service) modelControlReceipt(prep context.Context, user, ws, target, id, receipt string) {
	ctx, cancel := modelControlStorageContext(prep)
	defer cancel()
	if err := s.modelControls.recordModelControlReceipt(ctx, user, ws, target, id, receipt); err != nil {
		log.Printf("model-control receipt storage failed")
	}
}
func modelControlClearClosedBasis(t *modelControlTarget) {
	if len(t.ClosedEntries) == 0 {
		t.ClosedAt = nil
		t.ClosedBy = nil
		t.ClosedBatchID = nil
		t.ClosedAccuracyPercent = nil
	}
}
func modelControlApplyBasis(t *modelControlTarget, basis modelControlBasis, actor string, now time.Time) {
	t.ClosedAt = &now
	t.ClosedBy = &actor
	t.ClosedBatchID = &basis.BatchID
	t.ClosedAccuracyPercent = basis.AccuracyPercent
}

// Returns events while preserving original-value reservations until their expiry.
func modelControlReconcileLate(t *modelControlTarget, mapping map[string]string, now time.Time) []ModelControlEvent {
	u := t.UnconfirmedClose
	if u == nil {
		return nil
	}
	events := []ModelControlEvent{}
	if !now.Before(u.SentAt.Add(24 * time.Hour)) {
		t.UnconfirmedClose = nil
		return events
	}
	late, changed := map[string]string{}, map[string]string{}
	for k, v := range u.Entries {
		actual, exists := mapping[k]
		if !exists {
			if _, owned := t.ClosedEntries[k]; !owned {
				t.ClosedEntries[k] = v
				late[k] = v
			}
			delete(u.Entries, k)
		} else if actual != v {
			changed[k] = v
			delete(u.Entries, k)
		}
	}
	if len(late) > 0 {
		modelControlApplyBasis(t, u.Basis, u.ActorUserID, now)
		events = append(events, modelControlNewEvent(t.UserID, t.AdminAccountID, t.TargetID, t.ModelName, "close_late_applied", u.Basis, map[string]any{"entries": modelMappingEntries(late, "closed")}))
	}
	if len(changed) > 0 {
		events = append(events, modelControlNewEvent(t.UserID, t.AdminAccountID, t.TargetID, t.ModelName, "manual_takeover", nil, map[string]any{"entries": modelMappingEntries(changed, "manually_changed")}))
	}
	if len(u.Entries) == 0 {
		t.UnconfirmedClose = nil
	}
	return events
}
func (s *Service) reconcileModelControlLate(ctx context.Context, user, ws, target string, detail upstream.Sub2APIModelControlAccount, mutation bool) error {
	objects, err := s.modelControls.ListModelControlTargets(ctx, user, ws)
	if err != nil {
		return err
	}
	needed := false
	for _, o := range objects {
		if o.TargetID == target && o.UnconfirmedClose != nil {
			needed = true
		}
	}
	if !needed {
		return nil
	}
	err = s.modelControls.mutateModelControlOwnership(ctx, user, ws, target, mutation, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
		events := []ModelControlEvent{}
		for _, o := range objects {
			events = append(events, modelControlReconcileLate(o, detail.ModelMapping, now)...)
		}
		return events, nil
	})
	var commit *modelControlCommitError
	if errors.As(err, &commit) {
		_, committed, readErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
		if readErr != nil {
			return readErr
		}
		if !committed {
			return modelControlConflict("Storage", nil)
		}
		return nil
	}
	return err
}
func (s *Service) ExecuteModelControl(ctx context.Context, user, target, model, op string, basis modelControlBasis, fingerprint string, confirm bool) (ModelControlResult, error) {
	session, ws, account, _, _, err := s.modelControlInput(ctx, user, target, model, op, basis)
	if err != nil {
		return ModelControlResult{}, err
	}
	complete, err := s.registerActionDispatch()
	if err != nil {
		return ModelControlResult{}, err
	}
	defer complete()
	prep, release, err := s.prepareManualAction(ctx, user, ws, target)
	if err != nil {
		return ModelControlResult{}, err
	}
	defer release()
	item, object, err := s.getModelControlItem(prep, user, ws, target, model)
	if err != nil {
		return ModelControlResult{}, err
	}
	if !modelControlBasisMatches(basis, item.Basis) {
		return ModelControlResult{}, modelControlConflict("BasisChanged", item)
	}
	if err = modelControlAdmission(item, op); err != nil {
		return ModelControlResult{}, err
	}
	if op == "restore" && item.Decision != "usable" && !confirm {
		return ModelControlResult{}, modelControlConflict("ConfirmationRequired", item)
	}
	plan, err := s.loadModelControlPlan(prep, user, ws, account, op, session, item, object)
	if errors.Is(err, upstream.ErrSub2APIModelControlAccountMissing) {
		err = s.resolveMissingModelControlAccount(prep, user, ws, target, account, session, plan.At)
		if err != nil {
			return ModelControlResult{}, err
		}
		return s.modelControlResult(ctx, user, ws, target, model, "blocked", modelControlError("AccountMissing"), nil, nil)
	}
	if err != nil {
		return ModelControlResult{}, requestError(modelControlError("AccountReadFailed"))
	}
	if err = s.reconcileModelControlLate(prep, user, ws, target, plan.Detail, true); err != nil {
		return ModelControlResult{}, err
	}
	item, object, err = s.getModelControlItem(prep, user, ws, target, model)
	if err != nil {
		return ModelControlResult{}, err
	}
	// Late attribution may have changed closed entries; use the same plan function again.
	if op == "restore" {
		plan, err = s.computeModelControlPlan(prep, user, ws, op, item, object, plan.Detail, plan.Refresh, plan.At)
		if err != nil {
			return ModelControlResult{}, err
		}
	}
	_, err = s.persistModelControlPlan(prep, object, op, plan)
	if err != nil {
		return ModelControlResult{}, err
	}
	plan.Preview.Item, _, err = s.getModelControlItem(prep, user, ws, target, model)
	if err != nil {
		return ModelControlResult{}, err
	}
	if fingerprint == "" || fingerprint != plan.Preview.PlanFingerprint {
		return ModelControlResult{}, modelControlConflict("PlanChanged", plan.Preview)
	}
	if plan.Preview.BlockReasonKey != "" {
		return s.modelControlResult(prep, user, ws, target, model, "blocked", plan.Preview.BlockReasonKey, plan.Preview.Entries, plan.Preview.Groups)
	}
	basis = item.Basis
	basis.ConfirmWithoutEvidence = confirm
	if item.Round != nil {
		basis.AccuracyPercent = item.Round.AccuracyPercent
	}
	id, err := newID()
	if err != nil {
		return ModelControlResult{}, err
	}
	pending := &modelControlPending{ID: id, Operation: op, Basis: basis, BeforeMappingHash: modelControlPlanFingerprint(op, plan.Detail.ModelMapping, nil), Phase: "prepared", StartedAt: time.Now().UTC(), ActorUserID: user}
	if op == "close" {
		pending.Entries = plan.Close.Entries
		pending.AfterMapping = plan.Close.AfterMapping
	} else {
		pending.Entries = plan.Restore.Entries
		pending.AfterMapping = plan.Restore.AfterMapping
	}
	if op == "restore" && plan.Restore.NoRemoteWrite {
		err = s.modelControls.mutateModelControlOwnership(prep, user, ws, target, true, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
			for _, o := range objects {
				if o.Pending != nil {
					return nil, modelControlConflict("Pending", item)
				}
			}
			for _, o := range objects {
				if o.ID == object.ID {
					if o.Version != object.Version {
						return nil, modelControlConflict("VersionConflict", item)
					}
					attr := attributeModelControlEntries(plan.Detail.ModelMapping, o.ClosedEntries, nil)
					o.ClosedEntries = attr.ClosedEntries
					modelControlClearClosedBasis(o)
					setModelControlOutcomeObservation(o, plan.Detail, plan.At, plan.Preview.Groups, "restore", "", attr.Entries)
					events := []ModelControlEvent{modelControlNewEvent(user, ws, target, model, "restore_succeeded", basis, map[string]any{"entries": attr.Entries, "noRemoteWrite": true})}
					if len(attr.ManualEntries) > 0 {
						events = append(events, modelControlNewEvent(user, ws, target, model, "manual_takeover", nil, attr.ManualEntries))
					}
					return events, nil
				}
			}
			return nil, requestError(ErrorProbeTargetNotFound)
		})
		if err != nil {
			var commit *modelControlCommitError
			if !errors.As(err, &commit) {
				return ModelControlResult{}, err
			}
			_, committed, readErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
			if readErr != nil {
				return ModelControlResult{}, readErr
			}
			if !committed {
				return s.modelControlResult(ctx, user, ws, target, model, "unknown", modelControlError("Storage"), nil, nil)
			}
		}
		return s.modelControlResult(ctx, user, ws, target, model, "restored", "", plan.Preview.Entries, plan.Preview.Groups)
	}
	err = s.modelControls.mutateModelControlOwnership(prep, user, ws, target, true, "prepared", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
		for _, o := range objects {
			if o.Pending != nil {
				return nil, modelControlConflict("Pending", item)
			}
		}
		for _, o := range objects {
			if o.ID != object.ID {
				continue
			}
			if o.Version != object.Version {
				return nil, modelControlConflict("VersionConflict", item)
			}
			events := []ModelControlEvent{}
			if op == "restore" {
				for k := range plan.Restore.ManualRestored {
					delete(o.ClosedEntries, k)
				}
				for k := range plan.Restore.ManualChanged {
					delete(o.ClosedEntries, k)
				}
				if len(plan.Restore.ManualChanged)+len(plan.Restore.ManualRestored) > 0 {
					events = append(events, modelControlNewEvent(user, ws, target, model, "manual_takeover", nil, plan.Preview.Entries))
				}
				modelControlClearClosedBasis(o)
			}
			pending.StartedAt = now
			o.Pending = pending
			return events, nil
		}
		return nil, requestError(ErrorProbeTargetNotFound)
	})
	if err != nil {
		var commit *modelControlCommitError
		if !errors.As(err, &commit) {
			return ModelControlResult{}, err
		}
		_, fresh, readErr := s.readModelControlItemAfterCommit(ctx, user, ws, target, model)
		if readErr != nil {
			return ModelControlResult{}, requestError(modelControlError("StorageUnknown"))
		}
		if fresh.Pending == nil || fresh.Pending.ID != id {
			return s.modelControlResult(ctx, user, ws, target, model, "unknown", modelControlError("Storage"), nil, nil)
		}
	}
	if deadline, ok := prep.Deadline(); ok && time.Until(deadline) < actionPermitMinimumBudget {
		storage, cancel := modelControlStorageContext(prep)
		defer cancel()
		_ = s.modelControls.mutateModelControlOwnership(storage, user, ws, target, true, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
			for _, o := range objects {
				if o.Pending != nil && o.Pending.ID == id {
					o.Pending = nil
					o.LastPendingID = id
				}
			}
			return nil, nil
		})
		return ModelControlResult{}, modelControlConflict("PreparationTimeout", nil)
	}
	storage, cancel := modelControlStorageContext(prep)
	err = s.modelControls.mutateModelControlOwnership(storage, user, ws, target, true, "sending", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
		for _, o := range objects {
			if o.Pending != nil && o.Pending.ID == id && o.Pending.Phase == "prepared" {
				o.Pending.Phase = "sending"
				o.Pending.SendStartedAt = &now
				return nil, nil
			}
		}
		return nil, ErrRemoteActionLeaseLost
	})
	cancel()
	if err != nil {
		var commit *modelControlCommitError
		if errors.As(err, &commit) {
			_, _, readErr := s.readModelControlItemAfterCommit(ctx, user, ws, target, model)
			if readErr != nil {
				return ModelControlResult{}, requestError(modelControlError("StorageUnknown"))
			}
		}
		return s.modelControlResult(ctx, user, ws, target, model, "unknown", modelControlError("LeaseLost"), nil, plan.Preview.Groups)
	}
	if !modelControlLeasesValid(prep, true) {
		s.modelControlReceipt(prep, user, ws, target, id, "not_sent")
		return s.modelControlResult(ctx, user, ws, target, model, "unknown", modelControlError("LeaseLost"), nil, plan.Preview.Groups)
	}
	send, sendCancel := modelControlRemoteContext(prep, 10*time.Second)
	sendErr := s.modelControlActions.UpdateSub2APIAdminAccountModelMappingContext(send, session, account, pending.AfterMapping)
	sendCancel()
	outcome := upstream.RemoteMutationOutcome(sendErr)
	switch outcome {
	case upstream.MutationNotSent:
		s.modelControlReceipt(prep, user, ws, target, id, "not_sent")
	case upstream.MutationConfirmedApplied:
		s.modelControlReceipt(prep, user, ws, target, id, "applied")
	case upstream.MutationConfirmedRejected:
		s.modelControlReceipt(prep, user, ws, target, id, "rejected")
	}
	readback, readCancel := modelControlRemoteContext(prep, 10*time.Second)
	readAt := time.Now().UTC()
	detail, readErr := s.modelControlActions.ReadSub2APIModelControlAccountContext(readback, session, account)
	readCancel()
	var attr modelControlAttribution
	if outcome != upstream.MutationUncertain && readErr == nil {
		storage, cancel = modelControlStorageContext(prep)
		err = s.modelControls.mutateModelControlOwnership(storage, user, ws, target, true, "finalize", func(objects []*modelControlTarget, now time.Time) ([]ModelControlEvent, error) {
			for _, o := range objects {
				if o.Pending == nil || o.Pending.ID != id {
					continue
				}
				events := modelControlReconcileLate(o, detail.ModelMapping, now)
				attr = attributeModelControlEntries(detail.ModelMapping, o.ClosedEntries, o.Pending)
				newClosed := false
				for k := range attr.ClosedEntries {
					if _, exists := o.ClosedEntries[k]; !exists {
						newClosed = true
					}
				}
				if op == "close" && newClosed {
					modelControlApplyBasis(o, o.Pending.Basis, user, now)
				}
				o.ClosedEntries = attr.ClosedEntries
				o.Pending = nil
				o.LastPendingID = id
				modelControlClearClosedBasis(o)
				reason := ""
				if attr.Outcome != "succeeded" {
					reason = modelControlError("AccountReadFailed")
				}
				setModelControlOutcomeObservation(o, detail, readAt, plan.Preview.Groups, op, reason, attr.Entries)
				events = append(events, modelControlNewEvent(user, ws, target, model, op+"_"+attr.Outcome, basis, map[string]any{"entries": attr.Entries, "groups": plan.Preview.Groups}))
				if len(attr.ManualEntries) > 0 {
					events = append(events, modelControlNewEvent(user, ws, target, model, "manual_takeover", nil, attr.ManualEntries))
				}
				return events, nil
			}
			return nil, ErrRemoteActionLeaseLost
		})
		cancel()
		if err != nil {
			var commit *modelControlCommitError
			if errors.As(err, &commit) {
				_, committed, probeErr := s.confirmModelControlCommit(ctx, user, ws, target, commit)
				if probeErr != nil {
					return ModelControlResult{}, requestError(modelControlError("StorageUnknown"))
				}
				if committed {
					err = nil
				}
			}
		}
		if err == nil {
			resultOutcome := attr.Outcome
			if resultOutcome == "succeeded" {
				if op == "close" {
					resultOutcome = "closed"
				} else {
					resultOutcome = "restored"
				}
			}
			reason := ""
			if attr.Outcome != "succeeded" {
				reason = modelControlError("AccountReadFailed")
			}
			return s.modelControlResult(ctx, user, ws, target, model, resultOutcome, reason, attr.Entries, plan.Preview.Groups)
		}
	}
	_ = s.recordModelControlEvent(ctx, modelControlNewEvent(user, ws, target, model, op+"_unknown", basis, map[string]any{"reasonKey": modelControlError("Storage"), "entries": plan.Preview.Entries}))
	return s.modelControlResult(ctx, user, ws, target, model, "unknown", modelControlError("Storage"), nil, plan.Preview.Groups)
}
func (s *Service) observeModelControlOutcome(ctx context.Context, object modelControlTarget, detail upstream.Sub2APIModelControlAccount, at time.Time, groups []modelSourceCount, reason string, entries []ModelControlEntry) error {
	short, cancel := modelControlStorageContext(ctx)
	defer cancel()
	_, fresh, err := s.getModelControlItem(short, object.UserID, object.AdminAccountID, object.TargetID, object.ModelName)
	if err != nil {
		return requestError(modelControlError("StorageUnknown"))
	}
	fresh.AccountName = detail.Name
	fresh.Observation.State, fresh.Observation.ReasonKey = modelControlObservedState(detail, fresh)
	fresh.Observation.AccountStatus = detail.Status
	fresh.Observation.AccountSchedulable = detail.Schedulable
	if groups != nil {
		fresh.Observation.Sources = groups
	}
	fresh.ConflictReason = ""
	fresh.LastAttempt = nil
	if reason != "" {
		fresh.LastAttempt = &modelControlAttempt{Operation: "close_account", Outcome: "failed", ReasonKey: reason, Entries: entries, Groups: groups, At: at}
	}
	if _, err = s.modelControls.observeModelControlTarget(short, fresh, at, true, ""); err != nil || short.Err() != nil {
		return requestError(modelControlError("StorageUnknown"))
	}
	return nil
}

func setModelControlOutcomeObservation(t *modelControlTarget, detail upstream.Sub2APIModelControlAccount, at time.Time, groups []modelSourceCount, operation, reason string, entries []ModelControlEntry) {
	t.AccountName = detail.Name
	t.Observation.State, t.Observation.ReasonKey = modelControlObservedState(detail, *t)
	t.Observation.AccountStatus = detail.Status
	t.Observation.AccountSchedulable = detail.Schedulable
	if groups != nil {
		t.Observation.Sources = groups
	}
	t.ConflictReason = ""
	t.LastAttempt = nil
	if reason != "" {
		t.LastAttempt = &modelControlAttempt{Operation: operation, Outcome: "failed", ReasonKey: reason, Entries: entries, Groups: groups, At: at}
	}
	t.observationAt = &at
	t.updateAttempt = true
}

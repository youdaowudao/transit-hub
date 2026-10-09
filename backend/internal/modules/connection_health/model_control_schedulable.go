package connection_health

import (
	"context"
	"errors"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func (s *Service) CloseAccountForModel(ctx context.Context, user, target, model string, basis modelControlBasis, fingerprint string) (ModelControlResult, error) {
	session, ws, account, _, object, err := s.modelControlInput(ctx, user, target, model, "close_account", basis)
	if err != nil {
		return ModelControlResult{}, err
	}
	var plan modelControlComputedPlan
	core, readback, coreErr := s.setTargetSchedulable(ctx, user, target, false, func(leased context.Context, refresh adminTargetRefresh) error {
		item, current, err := s.getModelControlItem(leased, user, ws, target, model)
		if err != nil {
			return err
		}
		if !modelControlBasisMatches(basis, item.Basis) {
			return modelControlConflict("BasisChanged", item)
		}
		if err = modelControlAdmission(item, "close_account"); err != nil {
			return err
		}
		at := time.Now().UTC()
		detail, err := s.modelControlActions.ReadSub2APIModelControlAccountContext(leased, session, account)
		if err != nil {
			return err
		}
		plan, err = s.computeModelControlPlan(leased, user, ws, "close_account", item, current, detail, refresh, at)
		if err != nil {
			return err
		}
		_, err = s.persistModelControlPlan(leased, current, "close_account", plan)
		if err != nil {
			return err
		}
		plan.Preview.Item, _, err = s.getModelControlItem(leased, user, ws, target, model)
		if err != nil {
			return err
		}
		if fingerprint == "" || fingerprint != plan.Preview.PlanFingerprint {
			return modelControlConflict("PlanChanged", plan.Preview)
		}
		if plan.Preview.BlockReasonKey != "" {
			return modelControlConflict("NotEligible", plan.Preview)
		}
		return nil
	})
	var conflict *ModelControlConflictError
	if errors.As(coreErr, &conflict) {
		return ModelControlResult{}, coreErr
	}
	if coreErr != nil {
		at := time.Now().UTC()
		detail, readErr := s.modelControlActions.ReadSub2APIModelControlAccountContext(ctx, session, account)
		if errors.Is(readErr, upstream.ErrSub2APIModelControlAccountMissing) {
			if err = s.resolveMissingModelControlAccount(ctx, user, ws, target, account, session, at); err != nil {
				return ModelControlResult{}, err
			}
			return s.modelControlResult(ctx, user, ws, target, model, "blocked", modelControlError("AccountMissing"), nil, nil)
		}
		if errors.Is(coreErr, upstream.ErrSub2APIModelControlAccountMissing) || errors.Is(coreErr, requestError(ErrorProbeTargetNotFound)) {
			coreErr = requestError(modelControlError("AccountReadFailed"))
		}
		if readErr == nil {
			if err = s.observeModelControlOutcome(ctx, object, detail, at, plan.Preview.Groups, coreErr.Error(), nil); err != nil {
				return ModelControlResult{}, err
			}
		} else {
			object.Observation.AccountStatus = ""
			object.Observation.AccountSchedulable = nil
			object.LastAttempt = &modelControlAttempt{Operation: "close_account", Outcome: "failed", ReasonKey: coreErr.Error(), Groups: plan.Preview.Groups, At: at}
			short, cancel := modelControlStorageContext(ctx)
			_, err = s.modelControls.observeModelControlTarget(short, object, at, true, "")
			storageErr := short.Err()
			cancel()
			if err != nil || storageErr != nil {
				return ModelControlResult{}, requestError(modelControlError("StorageUnknown"))
			}
		}
		if err = s.recordModelControlEvent(ctx, modelControlNewEvent(user, ws, target, model, "account_schedulable_failed", basis, map[string]any{"reasonKey": coreErr.Error()})); err != nil {
			return ModelControlResult{}, err
		}
		result, err := s.modelControlResult(ctx, user, ws, target, model, "failed", coreErr.Error(), nil, plan.Preview.Groups)
		result.ErrorKey = coreErr.Error()
		return result, err
	}
	detail := plan.Detail
	detail.AdminGroupAccountInfo = readback.Account
	if err = s.observeModelControlOutcome(ctx, object, detail, readback.StartedAt, plan.Preview.Groups, "", nil); err != nil {
		return ModelControlResult{}, err
	}
	if err = s.recordModelControlEvent(ctx, modelControlNewEvent(user, ws, target, model, "account_schedulable_closed", basis, map[string]any{"groups": plan.Preview.Groups})); err != nil {
		return ModelControlResult{}, err
	}
	result, err := s.modelControlResult(ctx, user, ws, target, model, "closed", "", nil, plan.Preview.Groups)
	result.SchedulableResult = &core
	return result, err
}

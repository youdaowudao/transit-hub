package connection_health

import (
	"context"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type modelControlAutoCandidate struct {
	TargetID, ModelName, Operation string
	Basis                          modelControlBasis
	EvidenceAt                     *time.Time
}

func (s *Service) prepareAutomaticModelControlAction(ctx context.Context, user, ws, target string) (context.Context, func(), error) {
	prep, cancel := context.WithTimeout(context.WithValue(context.WithoutCancel(ctx), detachedActionLeaseKey{}, true), actionPreparationTimeout)
	leased, releaseTarget, acquired, err := s.acquireActionTargetLease(prep, target, false)
	if err != nil || !acquired {
		cancel()
		if err == nil {
			err = modelControlConflict("Busy", nil)
		}
		return ctx, nil, err
	}
	leased, deadlineCancel := context.WithDeadline(leased, mustContextDeadline(prep))
	leased, releaseMutation, err := s.acquireActionMutationLease(leased, user, ws)
	if err != nil {
		deadlineCancel()
		releaseTarget()
		cancel()
		return ctx, nil, err
	}
	leased, budgetCancel := context.WithDeadline(leased, mustContextDeadline(prep))
	return leased, func() { budgetCancel(); releaseMutation(); deadlineCancel(); releaseTarget(); cancel() }, nil
}
func (s *Service) modelControlAutoStillEligible(ctx context.Context, run *modelControlRun, item ModelControlItem, object modelControlTarget) (bool, int64, error) {
	settings, err := s.modelControlAuto(ctx, run.user, run.ws)
	if err != nil {
		return false, 0, requestError(modelControlError("Storage"))
	}
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return false, 0, nil
	}
	savedAt, err := r.GetModelControlRuleSavedAt(ctx, run.user, run.ws)
	if err != nil {
		return false, 0, requestError(modelControlError("Storage"))
	}
	at := modelControlItemEvidenceAt(item)
	status := modelControlAutoStatus(modelControlAutoStatusInput{Settings: settings, RuleSavedAt: savedAt, Item: item, Object: object, EvidenceAt: at, Now: time.Now().UTC(), IgnoreRetry: true})
	return status.State == "eligible" && status.Operation == run.auto.Operation && modelControlAutoBasisMatches(autoBasis(item.Basis, at), autoBasis(run.auto.Basis, run.auto.EvidenceAt)) && modelControlAdmission(item, run.op) == nil, settings.Version, nil
}
func (s *Service) executeModelControlAuto(ctx context.Context, user, ws string, session upstream.Session, candidate modelControlAutoCandidate) string {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return modelControlAutoReason(err)
	}
	if err := questionAnswerScheduleOwnedTarget(user, ws, candidate.TargetID); err != nil {
		return modelControlAutoReason(err)
	}
	parsed, ok := parseTargetID(candidate.TargetID)
	if !ok || parsed.platform != string(upstream.PlatformSub2API) || !validModelControlModel(candidate.ModelName) || (candidate.Operation != "close" && candidate.Operation != "restore") || s.modelControlActions == nil {
		return modelControlError("Unsupported")
	}
	ctx = withModelControlAutoActor(ctx)
	result, err := s.runModelControl(ctx, &modelControlRun{user: user, ws: ws, account: parsed.accountID, target: candidate.TargetID, model: candidate.ModelName, op: candidate.Operation, actor: "", session: session, submitted: candidate.Basis, prepare: s.prepareAutomaticModelControlAction, auto: &candidate})
	if err != nil {
		return modelControlAutoReason(err)
	}
	if result.Outcome == "blocked" {
		if result.ReasonKey == modelControlError("AutoManualChange") {
			if repo, ok := s.modelControls.(modelControlAutoRepository); ok {
				storage, cancel := modelControlStorageContext(ctx)
				defer cancel()
				if err := repo.MarkModelControlAutoVerify(storage, user, ws, candidate.TargetID, nil); err != nil {
					return modelControlError("Storage")
				}
			}
		}
		return result.ReasonKey
	}
	return ""
}

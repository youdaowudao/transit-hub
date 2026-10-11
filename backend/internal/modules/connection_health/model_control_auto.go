package connection_health

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type modelControlAutoActorKey struct{}

func withModelControlAutoActor(ctx context.Context) context.Context {
	return context.WithValue(ctx, modelControlAutoActorKey{}, true)
}
func modelControlAutoActor(ctx context.Context) bool {
	v, _ := ctx.Value(modelControlAutoActorKey{}).(bool)
	return v
}
func (s *Service) modelControlAuto(ctx context.Context, user, ws string) (ModelControlAutoSettings, error) {
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return defaultModelControlAutoSettings(), nil
	}
	return r.GetModelControlAutoSettings(ctx, user, ws)
}
func (s *Service) GetModelControlAutoSettings(ctx context.Context, user string) (ModelControlAutoSettings, error) {
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return ModelControlAutoSettings{}, err
	}
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return ModelControlAutoSettings{}, requestError(modelControlError("Unsupported"))
	}
	return r.GetModelControlAutoSettings(ctx, user, ws)
}
func (s *Service) SaveModelControlAutoSettings(ctx context.Context, user string, in ModelControlAutoSettings, expected int64) (ModelControlAutoSettings, error) {
	if err := s.questionAnswerScheduleWriteGuard(); err != nil {
		return in, err
	}
	if in.VerifyIntervalMinutes < 5 || in.VerifyIntervalMinutes > 60 || in.RetryIntervalMinutes < 1 || in.RetryIntervalMinutes > 60 || expected < 0 {
		return in, requestError(ErrorRequest)
	}
	ws, err := s.modelControlScope(ctx, user, "")
	if err != nil {
		return in, err
	}
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return in, requestError(modelControlError("Unsupported"))
	}
	return r.SaveModelControlAutoSettings(ctx, user, ws, in, expected)
}
func (s *Service) modelFloorReserved(ctx context.Context, user, ws string) (map[string]map[string]bool, error) {
	settings, err := s.modelControlAuto(ctx, user, ws)
	if err != nil {
		return nil, err
	}
	if !settings.HealthCoordinationEnabled {
		return nil, nil
	}
	_, reserved, err := s.modelControlReservations(ctx, user, ws)
	return reserved, err
}
func (s *Service) modelControlExcluded(ctx context.Context, user, ws string) (map[string]map[string]struct{}, error) {
	settings, err := s.modelControlAuto(ctx, user, ws)
	if err != nil {
		return nil, err
	}
	if !settings.HealthCoordinationEnabled {
		return nil, nil
	}
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return nil, nil
	}
	return r.ListModelControlFullyClosed(ctx, user, ws)
}
func (s *Service) startModelControlAutoScans(ctx context.Context) {
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok || ctx.Err() != nil || s.questionAnswerScheduleWriteGuard() != nil {
		return
	}
	workspaces, err := r.ListModelControlAutoWorkspaces(ctx)
	if err != nil {
		log.Printf("[connection-health] automatic model workspace read failed")
		return
	}
	for _, w := range workspaces {
		if ctx.Err() != nil {
			return
		}
		key := w.UserID + "|" + w.AdminAccountID
		if _, loaded := s.modelControlScanFlights.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		done, err := s.registerActionDispatch()
		if err != nil {
			s.modelControlScanFlights.Delete(key)
			return
		}
		s.modelControlScanWG.Add(1)
		go func(w modelControlAutoWorkspace, key string) {
			defer done()
			defer s.modelControlScanWG.Done()
			defer s.modelControlScanFlights.Delete(key)
			defer func() {
				if recover() != nil {
					log.Printf("[connection-health] automatic model scan panic recovered")
				}
			}()
			s.runModelControlAutoScan(ctx, w.UserID, w.AdminAccountID)
		}(w, key)
	}
}
func (s *Service) runModelControlAutoScan(ctx context.Context, user, ws string) {
	r, ok := s.modelControls.(modelControlAutoRepository)
	if !ok {
		return
	}
	settings, err := s.modelControlAuto(ctx, user, ws)
	if err != nil {
		log.Printf("[connection-health] automatic model settings read failed")
		return
	}
	if !settings.enabled() {
		return
	}
	started := time.Now().UTC()
	errorKey := ""
	if err = r.MarkModelControlAutoScan(ctx, user, ws, &started, nil, ""); err != nil {
		log.Printf("[connection-health] automatic model scan start save failed")
	}
	defer func() {
		finished := time.Now().UTC()
		storage, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.MarkModelControlAutoScan(storage, user, ws, nil, &finished, errorKey); err != nil {
			log.Printf("[connection-health] automatic model scan finish save failed")
		}
	}()
	session, err := s.mySites.RequireSession(ctx, user, ws)
	if err != nil {
		errorKey = modelControlAutoReason(err)
		return
	}
	if session.Platform != upstream.PlatformSub2API || s.modelControlActions == nil {
		errorKey = modelControlError("Unsupported")
		return
	}
	if settings.AutoCloseEnabled || settings.AutoRestoreEnabled {
		items, objects, err := s.modelControlItems(ctx, user, ws)
		if err != nil {
			errorKey = modelControlAutoReason(err)
			return
		}
		indices := []int{}
		for n, item := range items {
			if item.Auto != nil && item.Auto.State == "eligible" {
				indices = append(indices, n)
			}
		}
		sort.Slice(indices, func(i, j int) bool {
			a, b := objects[indices[i]], objects[indices[j]]
			if a.AutoState == nil && b.AutoState != nil {
				return true
			}
			if a.AutoState != nil && b.AutoState == nil {
				return false
			}
			if a.AutoState != nil && b.AutoState != nil && !a.AutoState.AttemptAt.Equal(b.AutoState.AttemptAt) {
				return a.AutoState.AttemptAt.Before(b.AutoState.AttemptAt)
			}
			if a.TargetID != b.TargetID {
				return a.TargetID < b.TargetID
			}
			return a.ModelName < b.ModelName
		})
		if len(indices) > 10 {
			indices = indices[:10]
		}
		for _, n := range indices {
			if ctx.Err() != nil {
				break
			}
			item, object := items[n], objects[n]
			attempt := &modelControlAutoAttempt{Operation: item.Auto.Operation, Basis: autoBasis(item.Basis, modelControlItemEvidenceAt(item)), AttemptAt: time.Now().UTC()}
			if err := r.SaveModelControlAutoState(ctx, user, ws, object.ID, attempt); err != nil {
				log.Printf("[connection-health] automatic model attempt save failed")
				continue
			}
			candidate := modelControlAutoCandidate{TargetID: item.TargetID, ModelName: item.ModelName, Operation: item.Auto.Operation, Basis: item.Basis, EvidenceAt: modelControlItemEvidenceAt(item)}
			if reason := s.executeModelControlAuto(ctx, user, ws, session, candidate); reason != "" {
				attempt.ReasonKey = reason
				storage, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				err := r.SaveModelControlAutoState(storage, user, ws, object.ID, attempt)
				cancel()
				if err != nil {
					log.Printf("[connection-health] automatic model attempt reason save failed")
				}
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	// Recheck all switches immediately before selecting due accounts; the verify
	// core checks again before inventory and after each account lock.
	settings, err = s.modelControlAuto(ctx, user, ws)
	if err != nil {
		errorKey = modelControlAutoReason(err)
		return
	}
	if !settings.enabled() {
		return
	}
	ids, err := r.ListModelControlVerifyDue(ctx, user, ws, settings.VerifyIntervalMinutes, time.Now().UTC(), 20)
	if err != nil {
		errorKey = modelControlError("Storage")
		return
	}
	if len(ids) > 0 {
		_, err = s.verifyModelControlTargets(withModelControlAutoActor(ctx), user, ws, ids, modelControlVerifyOptions{Session: &session, Background: true})
		if err != nil {
			errorKey = modelControlAutoReason(err)
		}
	}
}
func modelControlAutoReason(err error) string {
	if err == nil {
		return ""
	}
	var conflict *ModelControlConflictError
	if errors.As(err, &conflict) {
		return conflict.Key
	}
	var req requestError
	if errors.As(err, &req) {
		return req.Error()
	}
	return modelControlError("AutoNotStarted")
}

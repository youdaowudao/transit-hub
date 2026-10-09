package connection_health

import (
	"context"
	"encoding/json"
	"log"
)

type modelControlCommitTarget struct {
	ID        string
	Version   int64
	Ownership string
	Deleted   bool
}

func modelControlStorageContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), runtimeLeaseQueryTimeout)
}

// Compare the complete account object set with independent serialized snapshots.
// Observation updates neither participate in this proof nor change its versions.
func modelControlCommitMatches(proof []modelControlCommitTarget, fresh []modelControlTarget, target string) bool {
	if proof == nil {
		return false
	}
	actual := map[string]modelControlTarget{}
	for _, object := range fresh {
		if object.TargetID == target {
			actual[object.ID] = object
		}
	}
	for _, expected := range proof {
		object, found := actual[expected.ID]
		if expected.Deleted {
			if found {
				return false
			}
			continue
		}
		if !found || object.Version != expected.Version {
			return false
		}
		encoded, err := json.Marshal(modelControlOwnershipSnapshot(object))
		if err != nil || string(encoded) != expected.Ownership {
			return false
		}
		delete(actual, expected.ID)
	}
	return len(actual) == 0
}

func (s *Service) confirmModelControlCommit(ctx context.Context, user, workspace, target string, commit *modelControlCommitError) ([]modelControlTarget, bool, error) {
	short, cancel := modelControlStorageContext(ctx)
	defer cancel()
	fresh, err := s.modelControls.ListModelControlTargets(short, user, workspace)
	if err != nil || short.Err() != nil {
		return nil, false, requestError(modelControlError("StorageUnknown"))
	}
	return fresh, modelControlCommitMatches(commit.proof, fresh, target), nil
}

func (s *Service) readModelControlItemAfterCommit(ctx context.Context, user, workspace, target, model string) (ModelControlItem, modelControlTarget, error) {
	short, cancel := modelControlStorageContext(ctx)
	defer cancel()
	item, object, err := s.getModelControlItem(short, user, workspace, target, model)
	if err != nil || short.Err() != nil {
		return ModelControlItem{}, modelControlTarget{}, requestError(modelControlError("StorageUnknown"))
	}
	return item, object, nil
}

func (s *Service) recordModelControlEvent(ctx context.Context, event ModelControlEvent) error {
	short, cancel := modelControlStorageContext(ctx)
	defer cancel()
	if err := s.modelControls.InsertModelControlEvent(short, event); err != nil || short.Err() != nil {
		eventType := "other"
		switch event.EventType {
		case "close_unknown", "restore_unknown", "account_schedulable_failed", "account_schedulable_closed":
			eventType = event.EventType
		}
		failure := "storage"
		if short.Err() != nil {
			failure = "timeout"
		}
		log.Printf("model-control event storage failed event_type=%s failure=%s", eventType, failure)
		return requestError(modelControlError("StorageUnknown"))
	}
	return nil
}

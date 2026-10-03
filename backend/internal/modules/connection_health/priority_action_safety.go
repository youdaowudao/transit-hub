package connection_health

import (
	"context"

	"transithub/backend/internal/modules/upstream"
)

func (s *Service) syncSafePriorityTarget(ctx context.Context, session upstream.Session, userID, workspace, targetID string, item *priorityTargetInventory, stored *PrioritySyncState, desired int, multiplier *float64, healthStates []ConnectionHealthState, generation string, restore bool, mayWrite bool) error {
	observation := RemoteActionObservation{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}}
	if item != nil {
		observation.Visible = true
		observation.InventoryComplete = item.target.InventoryComplete
		observation.SnapshotStartedAt = item.snapshotStartedAt
		if item.priorityPresent {
			current := item.currentPriority
			observation.Priority = &current
		}
		observation.Status = item.target.AccountStatus
		observation.Weight = item.target.AccountWeight
		observation.Schedulable = item.target.Schedulable
	}
	pair, err := s.reconcileActionObservation(ctx, observation)
	if err != nil {
		return err
	}
	if pair.pendingCount() != 0 {
		return ErrRemoteActionPending
	}
	if item == nil || !observation.InventoryComplete || !item.priorityPresent {
		return ErrRemoteActionEvidenceChanged
	}
	stored = pair.Priority
	if stored == nil {
		if restore || !mayWrite {
			return nil
		}
		stored = &PrioritySyncState{UserID: userID, AdminAccountID: workspace, TargetID: targetID, OriginalPriority: item.currentPriority, LastAppliedPriority: item.currentPriority}
	}
	if stored.Conflict {
		return nil
	}
	if item.currentPriority != stored.LastAppliedPriority {
		stored.Conflict = true
		current := item.currentPriority
		stored.LastConflictPriority = &current
		return s.repo.UpsertPrioritySyncState(ctx, *stored)
	}
	if !mayWrite {
		return nil
	}
	if restore {
		desired = stored.OriginalPriority
	}
	if item.currentPriority == desired {
		if restore {
			return s.repo.DeletePrioritySyncState(ctx, userID, workspace, targetID)
		}
		if multiplier != nil {
			stored.EffectiveMultiplier = *multiplier
		}
		return s.repo.UpsertPrioritySyncState(ctx, *stored)
	}
	guard := RemoteActionHealthGuard{ExpectedPriorityGeneration: &generation}
	if !restore && !hasMultiplierOnlyPolicy(item.policies) {
		models := activeHealthPriorityModels(item)
		modelNames := make([]string, 0, len(models))
		for model := range models {
			modelNames = append(modelNames, model)
		}
		guard = actionGuardForTarget(item.target, activeHealthPriorityStates(healthStates, models), modelNames)
		guard.ExpectedPriorityGeneration = &generation
	}
	if multiplier != nil {
		stored.EffectiveMultiplier = *multiplier
	}
	stored.PendingPriority = &desired
	return s.dispatchSafePriorityAction(ctx, session, item.target, *stored, guard)
}

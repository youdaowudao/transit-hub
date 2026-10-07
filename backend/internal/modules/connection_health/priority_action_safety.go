package connection_health

import (
	"context"

	"transithub/backend/internal/modules/upstream"
)

func (s *Service) syncSafePriorityTarget(ctx context.Context, session upstream.Session, userID, workspace, targetID string, item *priorityTargetInventory, stored *PrioritySyncState, desired int, multiplier *float64, healthStates []ConnectionHealthState, generation string, restore bool, mayWrite bool) error {
	// The workspace lease orders a whole ranking round; it cannot substitute
	// for the account lease that serializes edits and paired checkpoints.
	if handle := actionLeaseFromContext(ctx); handle == nil || handle.Key != "connection-health:target:"+targetID {
		leased, release, acquired, err := s.acquireActionTargetLease(ctx, targetID, true)
		if err != nil || !acquired {
			if err == nil {
				err = ErrRemoteActionLeaseLost
			}
			return err
		}
		defer release()
		ctx = leased
	}
	observation := RemoteActionObservation{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}}
	if item != nil {
		observation.Visible, observation.InventoryComplete = true, item.target.InventoryComplete
		observation.SnapshotStartedAt = item.snapshotStartedAt
		if item.priorityPresent {
			value := item.currentPriority
			observation.Priority = &value
		}
		observation.Status, observation.Weight, observation.Schedulable = item.target.AccountStatus, item.target.AccountWeight, item.target.Schedulable
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
	expectedCheckpoint := stored
	legacy := hasMultiplierOnlyPolicy(item.policies) || (oldPriorityComparisonBaseline(stored) && (restore || item.currentPriority <= 9))
	manual := item.currentPriority >= 1 && item.currentPriority <= 9
	if manual && !legacy {
		if stored != nil {
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, nil)
		}
		return nil
	}
	if !mayWrite {
		return nil
	}
	resetUnwritten := legacy && isUnwrittenHealthPriority(stored)
	if restore {
		if stored == nil {
			return nil
		}
		if !legacy && (item.currentPriority <= 9 || stored.LastAppliedPriority == 0 || item.currentPriority != stored.LastAppliedPriority) {
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, nil)
		}
		desired = stored.OriginalPriority
	}
	if legacy && stored != nil && !resetUnwritten {
		if stored.Conflict {
			return nil
		}
		if restore && item.currentPriority == stored.OriginalPriority {
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, nil)
		}
		if item.currentPriority != stored.LastAppliedPriority {
			changed := *stored
			changed.Conflict = true
			current := item.currentPriority
			changed.LastConflictPriority = &current
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, &changed)
		}
	}
	// Observing the desired value is not proof of a previous machine write.
	if item.currentPriority == desired {
		if legacy && !restore {
			// Multiplier-only retains its historical observed comparison baseline,
			// including a first round that already has the desired value.
			changed := PrioritySyncState{UserID: userID, AdminAccountID: workspace, TargetID: targetID, OriginalPriority: item.currentPriority, LastAppliedPriority: item.currentPriority}
			if stored != nil && !resetUnwritten {
				changed = *stored
			}
			if multiplier != nil {
				changed.EffectiveMultiplier = *multiplier
			}
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, &changed)
		}
		if stored == nil {
			return nil
		}
		if restore || isUnwrittenHealthPriority(stored) {
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, nil)
		}
		if stored.Conflict {
			changed := *stored
			changed.Conflict, changed.LastConflictPriority = false, nil
			return s.mutateIdlePriority(ctx, item.target, userID, workspace, stored, &changed)
		}
		return nil
	}
	clearConflict := stored != nil && stored.Conflict && !legacy
	if stored == nil || resetUnwritten {
		last := 0
		if legacy {
			last = item.currentPriority
		}
		stored = &PrioritySyncState{UserID: userID, AdminAccountID: workspace, TargetID: targetID, OriginalPriority: item.currentPriority, LastAppliedPriority: last}
	} else {
		copy := *stored
		stored = &copy
	}
	guard := RemoteActionHealthGuard{ExpectedPriorityGeneration: &generation}
	if item.target.ConfigGenerationKnown {
		value := item.target.ConfigGeneration
		guard.ConfigGeneration = &value
	}
	if !restore && !hasMultiplierOnlyPolicy(item.policies) {
		models := activeHealthPriorityModels(item)
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		guard = actionGuardForTarget(item.target, activeHealthPriorityStates(healthStates, models), names)
		guard.ExpectedPriorityGeneration = &generation
	}
	if multiplier != nil {
		stored.EffectiveMultiplier = *multiplier
	}
	stored.PendingPriority = &desired
	if mutationLeaseFromContext(ctx) == nil {
		leased, release, err := s.acquireActionMutationLease(ctx, userID, workspace)
		if err != nil {
			return err
		}
		defer release()
		ctx = leased
	}
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{userID, workspace, targetID}, Kind: ActionKindPriority, Priority: stored, Guard: guard, ClearPriorityConflict: clearConflict, ResetUnwrittenPriority: resetUnwritten, ReleasePriorityOnConfirm: restore, ExpectedPriorityCheckpoint: expectedCheckpoint}
	_, err = s.dispatchRemoteAction(ctx, claim, func(sendCtx context.Context) (string, error) {
		return "priority", s.updateAdminTargetPriority(sendCtx, session, item.target.AccountID, desired)
	})
	return err
}

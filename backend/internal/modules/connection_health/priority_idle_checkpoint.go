package connection_health

import (
	"context"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
)

// Idle mutations compare the previously observed checkpoint under the same
// workspace and paired row locks as claims. They never erase a newer claim.
type IdlePriorityCheckpointMutation struct {
	RemoteActionScope
	Expected         *PrioritySyncState
	Replacement      *PrioritySyncState
	ConfigGeneration *int64
}

type idlePriorityCheckpointRepository interface {
	MutateIdlePriorityCheckpoint(context.Context, IdlePriorityCheckpointMutation) (RemoteActionCheckpoints, error)
}

func mutateIdlePriorityCheckpoint(pair *RemoteActionCheckpoints, change IdlePriorityCheckpointMutation) error {
	if pair.pendingCount() != 0 {
		return ErrRemoteActionPending
	}
	if !reflect.DeepEqual(pair.Priority, change.Expected) {
		return ErrRemoteActionEvidenceChanged
	}
	if change.Replacement != nil {
		state := *change.Replacement
		if state.UserID != change.UserID || state.AdminAccountID != change.AdminAccountID || state.TargetID != change.TargetID || priorityActionPending(&state) {
			return ErrRemoteActionEvidenceChanged
		}
		pair.Priority = &state
	} else {
		pair.Priority = nil
	}
	return nil
}

func (r *Repository) MutateIdlePriorityCheckpoint(ctx context.Context, change IdlePriorityCheckpointMutation) (RemoteActionCheckpoints, error) {
	return r.actionCheckpointTransaction(ctx, change.RemoteActionScope, nil, false, func(tx pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if err := validateRemoteActionHealthTx(ctx, tx, RemoteActionClaim{RemoteActionScope: change.RemoteActionScope, Guard: RemoteActionHealthGuard{ConfigGeneration: change.ConfigGeneration}}); err != nil {
			return err
		}
		return mutateIdlePriorityCheckpoint(pair, change)
	})
}

func (s *Service) mutateIdlePriority(ctx context.Context, target AdminProbeTarget, user, workspace string, old, replacement *PrioritySyncState) error {
	repo, ok := s.repo.(idlePriorityCheckpointRepository)
	if !ok {
		return ErrRemoteActionPending
	}
	var generation *int64
	if target.ConfigGenerationKnown {
		value := target.ConfigGeneration
		generation = &value
	}
	_, err := repo.MutateIdlePriorityCheckpoint(ctx, IdlePriorityCheckpointMutation{RemoteActionScope: RemoteActionScope{user, workspace, target.TargetID}, Expected: old, Replacement: replacement, ConfigGeneration: generation})
	return err
}

func oldPriorityComparisonBaseline(state *PrioritySyncState) bool {
	return state != nil && state.LastAppliedPriority >= 1 && state.LastAppliedPriority <= 9
}

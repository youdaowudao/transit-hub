package connection_health

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const priorityCheckpointColumns = `user_id,admin_account_id,target_id,original_priority,last_applied_priority,pending_priority,effective_multiplier,conflict,last_conflict_priority,pending_dispatch_id,pending_owner_id,pending_dispatch_phase,updated_at`
const targetCheckpointColumns = `user_id,admin_account_id,target_id,original_status,original_weight,last_applied_status,last_applied_weight,pending_status,pending_weight,conflict,pending_dispatch_id,pending_owner_id,pending_dispatch_phase,pending_action_kind,pending_schedulable,pending_source,pending_group_ids,pending_had_automatic_baseline,updated_at`

func scanPriorityCheckpoint(row pgx.Row) (*PrioritySyncState, error) {
	var state PrioritySyncState
	err := row.Scan(&state.UserID, &state.AdminAccountID, &state.TargetID, &state.OriginalPriority, &state.LastAppliedPriority, &state.PendingPriority, &state.EffectiveMultiplier, &state.Conflict, &state.LastConflictPriority, &state.PendingDispatchID, &state.PendingOwnerID, &state.PendingDispatchPhase, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &state, err
}

func scanTargetCheckpoint(row pgx.Row) (*TargetActionState, error) {
	var state TargetActionState
	err := row.Scan(&state.UserID, &state.AdminAccountID, &state.TargetID, &state.OriginalStatus, &state.OriginalWeight, &state.LastAppliedStatus, &state.LastAppliedWeight, &state.PendingStatus, &state.PendingWeight, &state.Conflict, &state.PendingDispatchID, &state.PendingOwnerID, &state.PendingDispatchPhase, &state.PendingActionKind, &state.PendingSchedulable, &state.PendingSource, &state.PendingGroupIDs, &state.PendingHadAutomaticBaseline, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &state, err
}

func lockActionCheckpoints(ctx context.Context, tx pgx.Tx, scope RemoteActionScope) (RemoteActionCheckpoints, error) {
	var pair RemoteActionCheckpoints
	var err error
	pair.Priority, err = scanPriorityCheckpoint(tx.QueryRow(ctx, `SELECT `+priorityCheckpointColumns+` FROM connection_health_priority_sync_states WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3 FOR UPDATE`, scope.UserID, scope.AdminAccountID, scope.TargetID))
	if err != nil {
		return pair, err
	}
	pair.Target, err = scanTargetCheckpoint(tx.QueryRow(ctx, `SELECT `+targetCheckpointColumns+` FROM connection_health_target_action_states WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3 FOR UPDATE`, scope.UserID, scope.AdminAccountID, scope.TargetID))
	return pair, err
}

func writePriorityCheckpointTx(ctx context.Context, tx pgx.Tx, state PrioritySyncState) error {
	_, err := tx.Exec(ctx, `INSERT INTO connection_health_priority_sync_states (`+priorityCheckpointColumns+`)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	ON CONFLICT(user_id,admin_account_id,target_id) DO UPDATE SET original_priority=EXCLUDED.original_priority,
	last_applied_priority=EXCLUDED.last_applied_priority,pending_priority=EXCLUDED.pending_priority,
	effective_multiplier=EXCLUDED.effective_multiplier,conflict=EXCLUDED.conflict,last_conflict_priority=EXCLUDED.last_conflict_priority,
	pending_dispatch_id=EXCLUDED.pending_dispatch_id,pending_owner_id=EXCLUDED.pending_owner_id,pending_dispatch_phase=EXCLUDED.pending_dispatch_phase,updated_at=EXCLUDED.updated_at`,
		state.UserID, state.AdminAccountID, state.TargetID, state.OriginalPriority, state.LastAppliedPriority, state.PendingPriority, state.EffectiveMultiplier, state.Conflict, state.LastConflictPriority, state.PendingDispatchID, state.PendingOwnerID, state.PendingDispatchPhase, state.UpdatedAt)
	return err
}

func writeTargetCheckpointTx(ctx context.Context, tx pgx.Tx, state TargetActionState) error {
	if state.PendingGroupIDs == nil {
		state.PendingGroupIDs = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO connection_health_target_action_states (`+targetCheckpointColumns+`)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
	ON CONFLICT(user_id,admin_account_id,target_id) DO UPDATE SET original_status=EXCLUDED.original_status,original_weight=EXCLUDED.original_weight,
	last_applied_status=EXCLUDED.last_applied_status,last_applied_weight=EXCLUDED.last_applied_weight,
	pending_status=EXCLUDED.pending_status,pending_weight=EXCLUDED.pending_weight,conflict=EXCLUDED.conflict,
	pending_dispatch_id=EXCLUDED.pending_dispatch_id,pending_owner_id=EXCLUDED.pending_owner_id,pending_dispatch_phase=EXCLUDED.pending_dispatch_phase,pending_action_kind=EXCLUDED.pending_action_kind,pending_schedulable=EXCLUDED.pending_schedulable,pending_source=EXCLUDED.pending_source,pending_group_ids=EXCLUDED.pending_group_ids,pending_had_automatic_baseline=EXCLUDED.pending_had_automatic_baseline,updated_at=EXCLUDED.updated_at`,
		state.UserID, state.AdminAccountID, state.TargetID, state.OriginalStatus, state.OriginalWeight, state.LastAppliedStatus, state.LastAppliedWeight, state.PendingStatus, state.PendingWeight, state.Conflict, state.PendingDispatchID, state.PendingOwnerID, state.PendingDispatchPhase, state.PendingActionKind, state.PendingSchedulable, state.PendingSource, state.PendingGroupIDs, state.PendingHadAutomaticBaseline, state.UpdatedAt)
	return err
}

// This is the sole checkpoint mutation transaction: W, optional lease row,
// Priority row, target row. Callbacks only use tx and never call the network.
func (r *Repository) actionCheckpointTransaction(ctx context.Context, scope RemoteActionScope, lease *RemoteActionClaim, requireLease bool, mutate func(pgx.Tx, *RemoteActionCheckpoints, time.Time) error) (RemoteActionCheckpoints, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, scope.UserID, scope.AdminAccountID)
	if err != nil {
		return RemoteActionCheckpoints{}, err
	}
	defer tx.Rollback(ctx)
	var leaseOwner string
	var leaseExpires time.Time
	var mutationExpires time.Time
	var workspaceExpires time.Time
	if requireLease && lease != nil && lease.WorkspaceLeaseKey != "" {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT owner_id,expires_at FROM connection_health_runtime_leases WHERE lease_key=$1 FOR UPDATE`, lease.WorkspaceLeaseKey).Scan(&owner, &workspaceExpires); err != nil || owner != lease.WorkspaceOwnerID {
			return RemoteActionCheckpoints{}, ErrRemoteActionLeaseLost
		}
	}
	if lease != nil && lease.LeaseKey != "" {
		err = tx.QueryRow(ctx, `SELECT owner_id,expires_at FROM connection_health_runtime_leases WHERE lease_key=$1 FOR UPDATE`, lease.LeaseKey).Scan(&leaseOwner, &leaseExpires)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return RemoteActionCheckpoints{}, err
		}
		if requireLease && (err != nil || leaseOwner != lease.OwnerID) {
			return RemoteActionCheckpoints{}, ErrRemoteActionLeaseLost
		}
	} else if requireLease {
		return RemoteActionCheckpoints{}, ErrRemoteActionLeaseLost
	}
	if requireLease && lease != nil && lease.MutationLeaseKey != "" {
		var mutationOwner string
		if err := tx.QueryRow(ctx, `SELECT owner_id,expires_at FROM connection_health_runtime_leases WHERE lease_key=$1 FOR UPDATE`, lease.MutationLeaseKey).Scan(&mutationOwner, &mutationExpires); err != nil || mutationOwner != lease.MutationOwnerID {
			return RemoteActionCheckpoints{}, ErrRemoteActionLeaseLost
		}
	}
	pair, err := lockActionCheckpoints(ctx, tx, scope)
	if err != nil {
		return pair, err
	}
	before := RemoteActionCheckpoints{}
	if pair.Priority != nil {
		copy := *pair.Priority
		before.Priority = &copy
	}
	if pair.Target != nil {
		copy := *pair.Target
		before.Target = &copy
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return pair, err
	}
	// Evaluate the database clock after all row locks have actually been
	// acquired; a timestamp computed before a lock wait is not a permit.
	if requireLease && (!leaseExpires.After(now) || (lease.MutationLeaseKey != "" && !mutationExpires.After(now)) || (lease.WorkspaceLeaseKey != "" && !workspaceExpires.After(now))) {
		return pair, ErrRemoteActionLeaseLost
	}
	if err = mutate(tx, &pair, now); err != nil {
		return pair, err
	}
	if !reflect.DeepEqual(before.Priority, pair.Priority) {
		if pair.Priority == nil {
			_, err = tx.Exec(ctx, `DELETE FROM connection_health_priority_sync_states WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3`, scope.UserID, scope.AdminAccountID, scope.TargetID)
		} else {
			pair.Priority.UpdatedAt = now
			err = writePriorityCheckpointTx(ctx, tx, *pair.Priority)
		}
		if err != nil {
			return pair, err
		}
	}
	if !reflect.DeepEqual(before.Target, pair.Target) {
		if pair.Target == nil {
			_, err = tx.Exec(ctx, `DELETE FROM connection_health_target_action_states WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3`, scope.UserID, scope.AdminAccountID, scope.TargetID)
		} else {
			pair.Target.UpdatedAt = now
			err = writeTargetCheckpointTx(ctx, tx, *pair.Target)
		}
		if err != nil {
			return pair, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return pair, err
	}
	return pair, nil
}

func (r *Repository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	claimed := false
	_, err := r.actionCheckpointTransaction(ctx, claim.RemoteActionScope, &claim, true, func(tx pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if err := validateRemoteActionHealthTx(ctx, tx, claim); err != nil {
			return err
		}
		var err error
		claimed, err = claimRemoteAction(pair, claim)
		return err
	})
	return claimed && err == nil, err
}

func (r *Repository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	permitted := false
	_, err := r.actionCheckpointTransaction(ctx, claim.RemoteActionScope, &claim, true, func(tx pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if err := validateRemoteActionHealthTx(ctx, tx, claim); err != nil {
			return err
		}
		permitted = permitRemoteAction(pair, claim)
		if !permitted {
			return ErrRemoteActionPending
		}
		return nil
	})
	// A failed/unknown commit never authorizes HTTP, even if PostgreSQL committed.
	return permitted && err == nil, err
}

func (r *Repository) RecordRemoteActionReceipt(ctx context.Context, claim RemoteActionClaim, phase RemoteDispatchPhase) error {
	_, err := r.actionCheckpointTransaction(ctx, claim.RemoteActionScope, &claim, false, func(_ pgx.Tx, pair *RemoteActionCheckpoints, now time.Time) error {
		if !receiptRemoteAction(pair, claim, phase, now) {
			return ErrRemoteActionPending
		}
		return nil
	})
	return err
}

func (r *Repository) ReconcileRemoteAction(ctx context.Context, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	return r.actionCheckpointTransaction(ctx, observation.RemoteActionScope, nil, false, func(tx pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		ownerValidity := make(map[string]bool)
		for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
			_, owner, phase, pending := pair.dispatch(kind)
			if pending && phase == DispatchPrepared && owner != "" {
				var valid bool
				err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_runtime_leases WHERE owner_id=$1 AND expires_at>clock_timestamp())`, owner).Scan(&valid)
				if err != nil {
					return err
				}
				ownerValidity[owner] = valid
			}
		}
		reconcileRemoteAction(pair, observation, func(owner string) bool { return ownerValidity[owner] })
		return nil
	})
}

func validateRemoteActionHealthTx(ctx context.Context, tx pgx.Tx, claim RemoteActionClaim) error {
	guard := claim.Guard
	if guard.ConfigGeneration != nil {
		settings, err := getWorkspaceHealthSettings(ctx, tx, claim.UserID, claim.AdminAccountID)
		if err != nil {
			return err
		}
		if settings.ConfigGeneration != *guard.ConfigGeneration {
			return ErrRemoteActionEvidenceChanged
		}
	}
	if guard.ExpectedPriorityGeneration != nil {
		var current string
		err := tx.QueryRow(ctx, `SELECT pending_signature FROM connection_health_priority_workspace_sync_states WHERE user_id=$1 AND admin_account_id=$2`, claim.UserID, claim.AdminAccountID).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != *guard.ExpectedPriorityGeneration {
			return ErrRemoteActionEvidenceChanged
		}
	}
	if !guard.Required {
		return nil
	}
	configurations, err := listGroupTestConfigurationsTx(ctx, tx, claim.UserID, claim.AdminAccountID)
	if err != nil {
		return err
	}
	current := ResolveGroupTestConfiguration(guard.Memberships, guard.InventoryComplete, configurations)
	if (current.Status != "default" && current.Status != "inherited") || current.Protocol != guard.Configuration.Protocol || current.ProbeTimeoutSeconds != guard.Configuration.ProbeTimeoutSeconds {
		return ErrRemoteActionEvidenceChanged
	}
	if len(guard.Models) == 0 {
		return ErrRemoteActionEvidenceChanged
	}
	for _, model := range guard.Models {
		state, err := getStateTx(ctx, tx, claim.TargetID, model)
		if err != nil {
			return err
		}
		if state == nil || state.UserID != claim.UserID || state.AdminAccountID != claim.AdminAccountID || !healthEvidenceMatches(*state, current.Protocol) {
			return ErrRemoteActionEvidenceChanged
		}
		for _, expected := range guard.ExpectedStates {
			if expected.ModelName == model && !sameRemoteHealthDecisionState(*state, expected) {
				return ErrRemoteActionEvidenceChanged
			}
		}
	}
	return nil
}

func isSub2APIActionTarget(targetID string) bool { return strings.HasPrefix(targetID, "sub2api:") }

func (r *Repository) storePriorityCheckpoint(ctx context.Context, state PrioritySyncState) error {
	_, err := r.actionCheckpointTransaction(ctx, RemoteActionScope{state.UserID, state.AdminAccountID, state.TargetID}, nil, false, func(_ pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if err := validatePriorityCheckpointUpdate(*pair, state); err != nil {
			return err
		}
		pair.Priority = &state
		return nil
	})
	return err
}

func (r *Repository) storeTargetCheckpoint(ctx context.Context, state TargetActionState) error {
	_, err := r.actionCheckpointTransaction(ctx, RemoteActionScope{state.UserID, state.AdminAccountID, state.TargetID}, nil, false, func(_ pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if err := validateTargetCheckpointUpdate(*pair, state); err != nil {
			return err
		}
		pair.Target = &state
		return nil
	})
	return err
}

func (r *Repository) deleteActionCheckpoint(ctx context.Context, scope RemoteActionScope, kind string) error {
	_, err := r.actionCheckpointTransaction(ctx, scope, nil, false, func(_ pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		if isSub2APIActionTarget(scope.TargetID) && pair.pendingCount() != 0 {
			return ErrRemoteActionPending
		}
		if kind == ActionKindPriority {
			pair.Priority = nil
		} else {
			pair.Target = nil
		}
		return nil
	})
	return err
}

// The workspace W lock already serializes writers. Lock the Sub2API rows in
// that scope and match IDs in Go so all TrimSpace aliases (including Unicode)
// use the same identity as grouped and complete upstream inventories.
func lockDeletedAccountAliases(ctx context.Context, tx pgx.Tx, scope RemoteActionScope) (map[string]RemoteActionCheckpoints, error) {
	aliases := make(map[string]RemoteActionCheckpoints)
	id, valid := scopedActionAccountID(scope.TargetID, scope.AdminAccountID)
	if !valid {
		return aliases, nil
	}
	prefix := "sub2api:" + scope.AdminAccountID + ":"
	for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
		columns, table := priorityCheckpointColumns, "connection_health_priority_sync_states"
		if kind == ActionKindTarget {
			columns, table = targetCheckpointColumns, "connection_health_target_action_states"
		}
		rows, err := tx.Query(ctx, `SELECT `+columns+` FROM `+table+` WHERE user_id=$1 AND admin_account_id=$2 AND left(target_id,length($3))=$3 ORDER BY target_id FOR UPDATE`, scope.UserID, scope.AdminAccountID, prefix)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var targetID string
			var priority *PrioritySyncState
			var target *TargetActionState
			if kind == ActionKindPriority {
				priority, err = scanPriorityCheckpoint(rows)
				if err == nil {
					targetID = priority.TargetID
				}
			} else {
				target, err = scanTargetCheckpoint(rows)
				if err == nil {
					targetID = target.TargetID
				}
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
			if aliasID, valid := scopedActionAccountID(targetID, scope.AdminAccountID); valid && aliasID == id {
				pair := aliases[targetID]
				if priority != nil {
					pair.Priority = priority
				} else {
					pair.Target = target
				}
				aliases[targetID] = pair
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return aliases, nil
}

// Complete list evidence replaces reconciliation only for accounts proven deleted.
// One transaction checks and clears every raw alias of the normalized account.
func (r *Repository) ClearDeletedAccountCheckpoint(ctx context.Context, scope RemoteActionScope, snapshotStartedAt, now time.Time) (bool, error) {
	cleared := false
	_, err := r.actionCheckpointTransaction(ctx, scope, nil, false, func(tx pgx.Tx, pair *RemoteActionCheckpoints, _ time.Time) error {
		aliases, err := lockDeletedAccountAliases(ctx, tx, scope)
		if err != nil {
			return err
		}
		// The additional alias locks may have waited; use the clock after all locks.
		var dbNow time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
			return err
		}
		if actionCheckpointBecameVisible(ctx, scope) {
			return nil
		}
		if !clearDeletedAccountCheckpointAliases(aliases, scope, snapshotStartedAt, dbNow) {
			return nil
		}
		otherAliases := make([]string, 0, len(aliases))
		for targetID := range aliases {
			if targetID != scope.TargetID {
				otherAliases = append(otherAliases, targetID)
			}
		}
		if len(otherAliases) > 0 {
			for _, table := range []string{"connection_health_priority_sync_states", "connection_health_target_action_states"} {
				if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id=$1 AND admin_account_id=$2 AND target_id=ANY($3)`, scope.UserID, scope.AdminAccountID, otherAliases); err != nil {
					return err
				}
			}
		}
		// The existing transaction writer deletes the exact scope's pair.
		pair.Priority, pair.Target = nil, nil
		cleared = true
		return nil
	})
	return cleared && err == nil, err
}

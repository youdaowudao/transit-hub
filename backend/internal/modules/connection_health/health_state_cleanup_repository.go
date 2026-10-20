package connection_health

import "context"

type healthStateWorkspace struct {
	userID         string
	adminAccountID string
}

func (r *Repository) ListSub2APIHealthWorkspaces(ctx context.Context) ([]healthStateWorkspace, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT user_id,admin_account_id FROM connection_health_states WHERE connection_id LIKE 'sub2api:%' ORDER BY user_id,admin_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := []healthStateWorkspace{}
	for rows.Next() {
		var workspace healthStateWorkspace
		if err := rows.Scan(&workspace.userID, &workspace.adminAccountID); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

// Both operations retain the existing workspace transaction lock. They never
// initialize settings and never use a snapshot to bypass live checkpoint guards.
func (r *Repository) AbandonConflictedTargetAction(ctx context.Context, scope RemoteActionScope, expectedGeneration int64, event ConnectionHealthEvent) (bool, error) {
	parsed, valid := parseTargetID(scope.TargetID)
	if !valid || parsed.platform != "sub2api" || parsed.adminAccountID != scope.AdminAccountID {
		return false, nil
	}
	tx, err := r.beginWorkspaceTransaction(ctx, scope.UserID, scope.AdminAccountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	settings, err := getWorkspaceHealthSettings(ctx, tx, scope.UserID, scope.AdminAccountID)
	if err != nil || settings.ConfigGeneration != expectedGeneration {
		return false, err
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM connection_health_target_action_states AS target
		WHERE target.user_id=$1 AND target.admin_account_id=$2 AND target.target_id=$3
		AND target.conflict=true AND target.pending_dispatch_phase='' AND target.pending_status=''
		AND NOT EXISTS (SELECT 1 FROM connection_health_priority_sync_states AS priority
			WHERE priority.user_id=$1 AND priority.admin_account_id=$2 AND priority.target_id=$3
			AND (priority.pending_dispatch_phase<>'' OR priority.pending_priority IS NOT NULL))`, scope.UserID, scope.AdminAccountID, scope.TargetID)
	if err != nil || deleted.RowsAffected() == 0 {
		return false, err
	}
	event.UserID, event.AdminAccountID, event.ConnectionID = scope.UserID, scope.AdminAccountID, scope.TargetID
	event.Result = "policy_unmanaged_abandoned"
	if err := insertEventWithExecutor(ctx, tx, event); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) DeleteUnmonitoredStates(ctx context.Context, userID, adminAccountID, targetID string, expectedGeneration int64) (int64, error) {
	parsed, valid := parseTargetID(targetID)
	if !valid || parsed.platform != "sub2api" || parsed.adminAccountID != adminAccountID {
		return 0, nil
	}
	tx, err := r.beginWorkspaceTransaction(ctx, userID, adminAccountID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	settings, err := getWorkspaceHealthSettings(ctx, tx, userID, adminAccountID)
	if err != nil || settings.ConfigGeneration != expectedGeneration {
		return 0, err
	}
	deleted, err := tx.Exec(ctx, `DELETE FROM connection_health_states AS health
		WHERE health.user_id=$1 AND health.admin_account_id=$2 AND health.connection_id=$3
		AND NOT EXISTS (SELECT 1 FROM connection_health_target_action_states AS target WHERE target.user_id=$1 AND target.admin_account_id=$2 AND target.target_id=$3)
		AND NOT EXISTS (SELECT 1 FROM connection_health_priority_sync_states AS priority WHERE priority.user_id=$1 AND priority.admin_account_id=$2 AND priority.target_id=$3)`, userID, adminAccountID, targetID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return deleted.RowsAffected(), nil
}

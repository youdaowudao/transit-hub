package connection_health

import "context"

type epochObservationRepository interface {
	UpsertStateIfAbnormalQueueEpoch(ctx context.Context, state ConnectionHealthState, expectedEpoch int64) (bool, error)
	InsertEventIfAbnormalQueueEpoch(ctx context.Context, event ConnectionHealthEvent, expectedEpoch int64) (bool, error)
	InsertSafetyAudit(ctx context.Context, userID, workspaceID, auditType, detail string) error
}

func (r *Repository) UpsertStateIfAbnormalQueueEpoch(ctx context.Context, state ConnectionHealthState, expectedEpoch int64) (bool, error) {
	command, err := r.db.Exec(ctx, `
		INSERT INTO connection_health_states (
			connection_id, model_name, user_id, admin_account_id, own_group_id, own_group_name,
			upstream_site_id, upstream_group_id, upstream_group_name, state, current_weight,
			consecutive_failures, consecutive_successes, last_probe_at, last_success_at, last_failure_at,
			cooldown_until, observing_until, last_latency_ms, last_success_latency_ms, last_probe_decision_key,
			last_error_key, last_error_detail, last_remote_action, updated_at
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,now()
		FROM connection_health_safety_epochs
		WHERE user_id=$3 AND admin_account_id=$4 AND abnormal_queue_epoch=$25
		ON CONFLICT (connection_id, model_name) DO UPDATE SET
			user_id=EXCLUDED.user_id,admin_account_id=EXCLUDED.admin_account_id,
			own_group_id=EXCLUDED.own_group_id,own_group_name=EXCLUDED.own_group_name,
			upstream_site_id=EXCLUDED.upstream_site_id,upstream_group_id=EXCLUDED.upstream_group_id,
			upstream_group_name=EXCLUDED.upstream_group_name,state=EXCLUDED.state,
			current_weight=EXCLUDED.current_weight,consecutive_failures=EXCLUDED.consecutive_failures,
			consecutive_successes=EXCLUDED.consecutive_successes,last_probe_at=EXCLUDED.last_probe_at,
			last_success_at=EXCLUDED.last_success_at,last_failure_at=EXCLUDED.last_failure_at,
			cooldown_until=EXCLUDED.cooldown_until,observing_until=EXCLUDED.observing_until,
			last_latency_ms=EXCLUDED.last_latency_ms,last_success_latency_ms=EXCLUDED.last_success_latency_ms,
			last_probe_decision_key=EXCLUDED.last_probe_decision_key,last_error_key=EXCLUDED.last_error_key,
			last_error_detail=EXCLUDED.last_error_detail,last_remote_action=EXCLUDED.last_remote_action,
			updated_at=now()
	`, state.ConnectionID, state.ModelName, state.UserID, state.AdminAccountID,
		state.OwnGroupID, state.OwnGroupName, state.UpstreamSiteID, state.UpstreamGroupID,
		state.UpstreamGroupName, string(state.State), state.CurrentWeight,
		state.ConsecutiveFailures, state.ConsecutiveSuccesses, state.LastProbeAt,
		state.LastSuccessAt, state.LastFailureAt, state.CooldownUntil, state.ObservingUntil,
		state.LastLatencyMs, state.LastSuccessLatencyMs, state.LastProbeDecisionKey,
		state.LastErrorKey, state.LastErrorDetail, state.LastRemoteAction, expectedEpoch)
	return command.RowsAffected() == 1, err
}

func (r *Repository) InsertEventIfAbnormalQueueEpoch(ctx context.Context, event ConnectionHealthEvent, expectedEpoch int64) (bool, error) {
	command, err := r.db.Exec(ctx, `
		INSERT INTO connection_health_events (
			id,connection_id,model_name,user_id,admin_account_id,policy_id,admin_group_id,own_group_name,
			upstream_site_id,upstream_group_name,result,from_state,to_state,latency_ms,error_key,error_detail,
			remote_action,action_source,source,created_at
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,now()
		FROM connection_health_safety_epochs
		WHERE user_id=$4 AND admin_account_id=$5 AND abnormal_queue_epoch=$20
	`, event.ID, event.ConnectionID, event.ModelName, event.UserID, event.AdminAccountID,
		event.PolicyID, event.AdminGroupID, event.OwnGroupName, event.UpstreamSiteID,
		event.UpstreamGroupName, event.Result, event.FromState, event.ToState, event.LatencyMs,
		event.ErrorKey, event.ErrorDetail, event.RemoteAction, event.ActionSource, event.Source,
		expectedEpoch)
	return command.RowsAffected() == 1, err
}

func (r *Repository) InsertSafetyAudit(ctx context.Context, userID, workspaceID, auditType, detail string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO connection_health_safety_audits
			(id,user_id,admin_account_id,audit_type,actor,old_value,new_value,detail,created_at)
		VALUES($1,$2,$3,$4,'','{}'::jsonb,'{}'::jsonb,$5,now())
	`, id, userID, workspaceID, auditType, detail)
	return err
}

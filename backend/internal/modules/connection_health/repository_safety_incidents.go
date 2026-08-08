package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const workspaceCircuitFaultDomain = "workspace:*"

func scanIncident(row pgx.Row) (*IncidentCircuitState, error) {
	var incident IncidentCircuitState
	err := row.Scan(
		&incident.ID, &incident.UserID, &incident.WorkspaceID, &incident.FaultDomain,
		&incident.State, &incident.NormalGeneration, &incident.CanaryTargetID,
		&incident.SuccessfulCanaryTarget, &incident.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &incident, nil
}

func incidentColumns() string {
	return `id,user_id,admin_account_id,fault_domain,state,normal_generation,
		canary_target_id,successful_canary_target_id,updated_at`
}

func clearCancelledIncidentPendingTx(ctx context.Context, tx pgx.Tx, userID, workspaceID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_target_action_states target
		SET pending_status='',pending_weight=NULL,pending_mutation_generation=0,
			pending_source='',pending_epoch=0,pending_action_key='',updated_at=now()
		FROM connection_health_abnormal_queue queue
		WHERE target.user_id=$1 AND target.admin_account_id=$2
			AND target.pending_source=$3 AND target.pending_action_key=queue.action_key
			AND queue.user_id=target.user_id AND queue.admin_account_id=target.admin_account_id
			AND queue.source=$3 AND queue.state='cancelled'
	`, userID, workspaceID, SafetySourceHealthIncident); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE connection_health_priority_sync_states priority
		SET pending_priority=NULL,pending_mutation_generation=0,
			pending_source='',pending_epoch=0,pending_action_key='',updated_at=now()
		FROM connection_health_abnormal_queue queue
		WHERE priority.user_id=$1 AND priority.admin_account_id=$2
			AND priority.pending_source=$3 AND priority.pending_action_key=queue.action_key
			AND queue.user_id=priority.user_id AND queue.admin_account_id=priority.admin_account_id
			AND queue.source=$3 AND queue.state='cancelled'
	`, userID, workspaceID, SafetySourceHealthIncident)
	return err
}

func (r *Repository) ObserveIncidentFailure(ctx context.Context, item AbnormalQueueItem, canaryTargetID string) (IncidentCircuitState, bool, error) {
	if item.ExpectedResult != string(ResultServerError) && item.ExpectedResult != string(ResultNetworkFluctuation) {
		return IncidentCircuitState{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IncidentCircuitState{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_epochs(user_id,admin_account_id)
		VALUES($1,$2) ON CONFLICT(user_id,admin_account_id) DO NOTHING
	`, item.UserID, item.WorkspaceID); err != nil {
		return IncidentCircuitState{}, false, err
	}
	var currentEpoch int64
	if err := tx.QueryRow(ctx, `
		SELECT abnormal_queue_epoch FROM connection_health_safety_epochs
		WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE
	`, item.UserID, item.WorkspaceID).Scan(&currentEpoch); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if currentEpoch != item.QueueEpoch {
		return IncidentCircuitState{}, false, nil
	}
	// Serialize observations for one domain and normal generation. This keeps
	// the account threshold and canary creation atomic across workers.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended(
			jsonb_build_array($1,$2,$3,$4)::text, 0
		))
	`, item.UserID, item.WorkspaceID, item.FaultDomain, item.NormalGeneration); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_incident_observations
			(user_id,admin_account_id,fault_domain,normal_generation,account_id,target_id,result,observed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT(user_id,admin_account_id,fault_domain,normal_generation,account_id)
		DO UPDATE SET target_id=EXCLUDED.target_id,result=EXCLUDED.result,observed_at=now()
	`, item.UserID, item.WorkspaceID, item.FaultDomain, item.NormalGeneration,
		item.AccountID, item.TargetID, item.ExpectedResult); err != nil {
		return IncidentCircuitState{}, false, err
	}
	var observations int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT account_id)::int FROM connection_health_incident_observations
		WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3 AND normal_generation=$4
	`, item.UserID, item.WorkspaceID, item.FaultDomain, item.NormalGeneration).Scan(&observations); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if observations < 2 {
		if err := tx.Commit(ctx); err != nil {
			return IncidentCircuitState{}, false, err
		}
		return IncidentCircuitState{}, false, nil
	}

	if canaryTargetID == "" {
		canaryTargetID = item.TargetID
	}
	incident, err := scanIncident(tx.QueryRow(ctx, `
		SELECT `+incidentColumns()+` FROM connection_health_incidents
		WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
			AND state IN ('open','half_open') AND normal_generation=$4
		FOR UPDATE
	`, item.UserID, item.WorkspaceID, item.FaultDomain, item.NormalGeneration))
	if err != nil {
		return IncidentCircuitState{}, false, err
	}
	if incident == nil {
		incidentID, idErr := newID()
		if idErr != nil {
			return IncidentCircuitState{}, false, idErr
		}
		incident, err = scanIncident(tx.QueryRow(ctx, `
			INSERT INTO connection_health_incidents
				(id,user_id,admin_account_id,fault_domain,state,normal_generation,canary_target_id,
					successful_canary_target_id,updated_at)
			VALUES($1,$2,$3,$4,'open',$5,$6,'',now())
			ON CONFLICT(user_id,admin_account_id,fault_domain) DO UPDATE SET
				state='open',normal_generation=EXCLUDED.normal_generation,
				canary_target_id=EXCLUDED.canary_target_id,
				successful_canary_target_id='',updated_at=now()
			RETURNING `+incidentColumns()+`
		`, incidentID, item.UserID, item.WorkspaceID, item.FaultDomain,
			item.NormalGeneration, canaryTargetID))
		if err != nil {
			return IncidentCircuitState{}, false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='cancelled',
			last_result='circuit_open',claimed_by='',claim_expires_at=NULL,updated_at=now()
		WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
			AND source=$4 AND queue_kind<>$5 AND state IN ('queued','claimed')
	`, item.UserID, item.WorkspaceID, item.FaultDomain,
		SafetySourceHealthIncident, QueueKindCanary); err != nil {
		return IncidentCircuitState{}, false, err
	}

	var openDomains int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT fault_domain)::int FROM connection_health_incidents
		WHERE user_id=$1 AND admin_account_id=$2 AND normal_generation=$3
			AND state='open' AND fault_domain<>$4
	`, item.UserID, item.WorkspaceID, item.NormalGeneration, workspaceCircuitFaultDomain).Scan(&openDomains); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if openDomains >= 2 {
		workspaceIncident, workspaceErr := scanIncident(tx.QueryRow(ctx, `
			SELECT `+incidentColumns()+` FROM connection_health_incidents
			WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
				AND state IN ('open','half_open') AND normal_generation=$4
			FOR UPDATE
		`, item.UserID, item.WorkspaceID, workspaceCircuitFaultDomain, item.NormalGeneration))
		if workspaceErr != nil {
			return IncidentCircuitState{}, false, workspaceErr
		}
		if workspaceIncident == nil {
			workspaceIncidentID, idErr := newID()
			if idErr != nil {
				return IncidentCircuitState{}, false, idErr
			}
			workspaceIncident, workspaceErr = scanIncident(tx.QueryRow(ctx, `
				INSERT INTO connection_health_incidents
					(id,user_id,admin_account_id,fault_domain,state,normal_generation,canary_target_id,
						successful_canary_target_id,updated_at)
				VALUES($1,$2,$3,$4,'open',$5,$6,'',now())
				ON CONFLICT(user_id,admin_account_id,fault_domain) DO UPDATE SET
					state='open',normal_generation=EXCLUDED.normal_generation,
					canary_target_id=EXCLUDED.canary_target_id,successful_canary_target_id='',updated_at=now()
				RETURNING `+incidentColumns()+`
			`, workspaceIncidentID, item.UserID, item.WorkspaceID, workspaceCircuitFaultDomain,
				item.NormalGeneration, canaryTargetID))
			if workspaceErr != nil {
				return IncidentCircuitState{}, false, workspaceErr
			}
		}
		incident = workspaceIncident
		if _, err := tx.Exec(ctx, `
			UPDATE connection_health_abnormal_queue SET state='cancelled',
				last_result='workspace_circuit_open',claimed_by='',claim_expires_at=NULL,updated_at=now()
			WHERE user_id=$1 AND admin_account_id=$2 AND source=$3
				AND queue_kind<>$4 AND state IN ('queued','claimed')
		`, item.UserID, item.WorkspaceID, SafetySourceHealthIncident, QueueKindCanary); err != nil {
			return IncidentCircuitState{}, false, err
		}
	}
	if err := clearCancelledIncidentPendingTx(ctx, tx, item.UserID, item.WorkspaceID); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if err := enqueueIncidentCanaryTx(ctx, tx, item, *incident, incident.CanaryTargetID, time.Now().UTC().Add(probeSchedulerScanInterval), ""); err != nil {
		return IncidentCircuitState{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IncidentCircuitState{}, false, err
	}
	return *incident, true, nil
}

func enqueueIncidentCanaryTx(ctx context.Context, tx pgx.Tx, item AbnormalQueueItem, incident IncidentCircuitState, targetID string, nextAttemptAt time.Time, excludedID string) error {
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM connection_health_abnormal_queue
		WHERE user_id=$1 AND admin_account_id=$2 AND incident_id=$3
			AND queue_kind=$4 AND state IN ('queued','claimed','dispatching')
			AND ($5='' OR id<>$5)
	`, item.UserID, item.WorkspaceID, incident.ID, QueueKindCanary, excludedID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return nil
	}
	if targetID == "" {
		targetID = item.TargetID
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC()
	}
	accountID := item.AccountID
	if parsed, ok := parseTargetID(targetID); ok {
		accountID = parsed.accountID
	}
	var mutationGeneration int64
	generationErr := tx.QueryRow(ctx, `
		SELECT generation FROM connection_health_mutation_fences
		WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
	`, item.UserID, item.WorkspaceID, accountID).Scan(&mutationGeneration)
	if errors.Is(generationErr, pgx.ErrNoRows) {
		mutationGeneration = 0
	} else if generationErr != nil {
		return generationErr
	}
	delays, err := json.Marshal(item.ConfirmationDelays)
	if err != nil {
		return err
	}
	canaryID, err := newID()
	if err != nil {
		return err
	}
	// Every canary dispatch gets its own action key. Reusing a key for a retry on
	// the same account would make ON CONFLICT turn the currently dispatching row
	// back into queued before its worker can complete it.
	actionKey := "canary:" + incident.ID + ":" + targetID + ":" + canaryID
	_, err = tx.Exec(ctx, `
		INSERT INTO connection_health_abnormal_queue (
			id,user_id,admin_account_id,target_id,account_id,model_name,provider_family,probe_prompt,
			max_probe_tokens,queue_kind,source,incident_id,fault_domain,observation_epoch,
			normal_generation,abnormal_queue_epoch,attempt,required_attempts,
			confirmation_delays_seconds,confirmation_jitter_seconds,next_attempt_at,action_key,
			mutation_generation,state,claimed_by,claim_expires_at,expected_result,last_result,created_at,updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,0,0,
			$17::jsonb,$18,$19,$20,$21,'',NULL,$22,$23,now(),now())
		ON CONFLICT(user_id,admin_account_id,action_key) DO UPDATE SET
			target_id=EXCLUDED.target_id,account_id=EXCLUDED.account_id,model_name=EXCLUDED.model_name,
			provider_family=EXCLUDED.provider_family,probe_prompt=EXCLUDED.probe_prompt,
			max_probe_tokens=EXCLUDED.max_probe_tokens,queue_kind=EXCLUDED.queue_kind,
			source=EXCLUDED.source,incident_id=EXCLUDED.incident_id,fault_domain=EXCLUDED.fault_domain,
			observation_epoch=EXCLUDED.observation_epoch,normal_generation=EXCLUDED.normal_generation,
			abnormal_queue_epoch=EXCLUDED.abnormal_queue_epoch,attempt=0,required_attempts=0,
			confirmation_delays_seconds=EXCLUDED.confirmation_delays_seconds,
			confirmation_jitter_seconds=EXCLUDED.confirmation_jitter_seconds,next_attempt_at=EXCLUDED.next_attempt_at,
			mutation_generation=EXCLUDED.mutation_generation,state='queued',claimed_by='',claim_expires_at=NULL,
			expected_result=EXCLUDED.expected_result,last_result=EXCLUDED.last_result,updated_at=now()
		`, canaryID, item.UserID, item.WorkspaceID, targetID, accountID, item.ModelName,
		item.ProviderFamily, item.ProbePrompt, item.MaxProbeTokens, QueueKindCanary,
		SafetySourceHealthIncident, incident.ID, incident.FaultDomain, item.ObservationEpoch,
		item.NormalGeneration, item.QueueEpoch, delays, item.ConfirmationJitter, nextAttemptAt, actionKey,
		mutationGeneration, item.ExpectedResult, "circuit_open")
	return err
}

func (r *Repository) GetIncidentCircuit(ctx context.Context, userID, workspaceID, faultDomain string) (*IncidentCircuitState, error) {
	workspace, err := scanIncident(r.db.QueryRow(ctx, `
		SELECT `+incidentColumns()+` FROM connection_health_incidents
		WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
			AND state IN ('open','half_open')
	`, userID, workspaceID, workspaceCircuitFaultDomain))
	if err != nil || workspace != nil {
		return workspace, err
	}
	return scanIncident(r.db.QueryRow(ctx, `
		SELECT `+incidentColumns()+` FROM connection_health_incidents
		WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
			AND state IN ('open','half_open')
	`, userID, workspaceID, faultDomain))
}

func (r *Repository) TargetCircuitOpen(ctx context.Context, userID, workspaceID, targetID string) (bool, error) {
	var open bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM connection_health_incidents i
			WHERE i.user_id=$1 AND i.admin_account_id=$2 AND i.state IN ('open','half_open')
				AND (i.fault_domain=$4 OR EXISTS(
					SELECT 1 FROM connection_health_incident_observations o
					WHERE o.user_id=i.user_id AND o.admin_account_id=i.admin_account_id
						AND o.fault_domain=i.fault_domain AND o.target_id=$3
				))
		)
	`, userID, workspaceID, targetID, workspaceCircuitFaultDomain).Scan(&open)
	return open, err
}

func (r *Repository) AdvanceIncidentCanary(ctx context.Context, item AbnormalQueueItem, succeeded bool, nextTargetID string, now time.Time) (IncidentCircuitState, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IncidentCircuitState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_epochs(user_id,admin_account_id)
		VALUES($1,$2) ON CONFLICT(user_id,admin_account_id) DO NOTHING
	`, item.UserID, item.WorkspaceID); err != nil {
		return IncidentCircuitState{}, err
	}
	var currentEpoch int64
	if err := tx.QueryRow(ctx, `
		SELECT abnormal_queue_epoch FROM connection_health_safety_epochs
		WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE
	`, item.UserID, item.WorkspaceID).Scan(&currentEpoch); err != nil {
		return IncidentCircuitState{}, err
	}
	if currentEpoch != item.QueueEpoch {
		return IncidentCircuitState{}, errStaleMutation
	}
	incident, err := scanIncident(tx.QueryRow(ctx, `
		SELECT `+incidentColumns()+` FROM connection_health_incidents
		WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 FOR UPDATE
	`, item.IncidentID, item.UserID, item.WorkspaceID))
	if err != nil {
		return IncidentCircuitState{}, err
	}
	if incident == nil || incident.State == CircuitClosed || incident.NormalGeneration != item.NormalGeneration ||
		incident.CanaryTargetID != item.TargetID {
		return IncidentCircuitState{}, errStaleMutation
	}
	retryCurrentTarget := false
	if !succeeded {
		incident.State = CircuitOpen
		incident.SuccessfulCanaryTarget = ""
		if nextTargetID != "" {
			incident.CanaryTargetID = nextTargetID
		} else {
			incident.CanaryTargetID = item.TargetID
		}
	} else if incident.State == CircuitOpen {
		if nextTargetID == "" && incident.FaultDomain == workspaceCircuitFaultDomain {
			scanErr := tx.QueryRow(ctx, `
				SELECT target_id FROM connection_health_incident_observations
				WHERE user_id=$1 AND admin_account_id=$2 AND normal_generation=$3 AND target_id<>$4
				ORDER BY target_id LIMIT 1
			`, item.UserID, item.WorkspaceID, incident.NormalGeneration, item.TargetID).Scan(&nextTargetID)
			if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
				return IncidentCircuitState{}, scanErr
			}
		} else if nextTargetID == "" {
			scanErr := tx.QueryRow(ctx, `
				SELECT target_id FROM connection_health_incident_observations
				WHERE user_id=$1 AND admin_account_id=$2 AND fault_domain=$3
					AND normal_generation=$4 AND target_id<>$5
				ORDER BY target_id LIMIT 1
			`, item.UserID, item.WorkspaceID, incident.FaultDomain,
				incident.NormalGeneration, item.TargetID).Scan(&nextTargetID)
			if scanErr != nil && !errors.Is(scanErr, pgx.ErrNoRows) {
				return IncidentCircuitState{}, scanErr
			}
		}
		if nextTargetID == "" {
			// The contract requires two successful canaries on different accounts.
			// If the second account is not currently provable, keep the circuit open
			// and retry this target later instead of closing or dropping the canary.
			incident.State = CircuitOpen
			incident.SuccessfulCanaryTarget = ""
			incident.CanaryTargetID = item.TargetID
			retryCurrentTarget = true
		} else {
			incident.State = CircuitHalfOpen
			incident.SuccessfulCanaryTarget = item.TargetID
			incident.CanaryTargetID = nextTargetID
		}
	} else if incident.State == CircuitHalfOpen && incident.SuccessfulCanaryTarget != "" && incident.SuccessfulCanaryTarget != item.TargetID {
		incident.State = CircuitClosed
		incident.CanaryTargetID = ""
		incident.SuccessfulCanaryTarget = ""
	} else {
		return IncidentCircuitState{}, errStaleMutation
	}
	incident.UpdatedAt = now.UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_incidents SET state=$2,canary_target_id=$3,
			successful_canary_target_id=$4,updated_at=$5 WHERE id=$1
	`, incident.ID, incident.State, incident.CanaryTargetID,
		incident.SuccessfulCanaryTarget, incident.UpdatedAt); err != nil {
		return IncidentCircuitState{}, err
	}
	if incident.State == CircuitClosed && incident.FaultDomain == workspaceCircuitFaultDomain {
		// The workspace circuit temporarily supersedes its child endpoint
		// circuits. Once two independent workspace canaries succeed, close those
		// same-generation children as well so ordinary probes can resume and
		// independently reopen any domain that is still failing.
		if _, err := tx.Exec(ctx, `
			UPDATE connection_health_incidents SET state='closed',canary_target_id='',
				successful_canary_target_id='',updated_at=$4
			WHERE user_id=$1 AND admin_account_id=$2 AND normal_generation=$3
				AND fault_domain<>$5 AND state IN ('open','half_open')
		`, item.UserID, item.WorkspaceID, item.NormalGeneration, incident.UpdatedAt, workspaceCircuitFaultDomain); err != nil {
			return IncidentCircuitState{}, err
		}
	}
	if incident.State != CircuitClosed {
		nextItem := item
		nextItem.ID = ""
		nextItem.Kind = QueueKindCanary
		nextItem.IncidentID = incident.ID
		nextItem.FaultDomain = incident.FaultDomain
		nextItem.TargetID = incident.CanaryTargetID
		nextItem.Attempt = 0
		nextItem.RequiredAttempts = 0
		nextItem.State = QueueStateQueued
		nextItem.ClaimedBy = ""
		nextItem.ClaimExpiresAt = nil
		nextItem.LastResult = "canary_scheduled"
		nextAt := now
		if !succeeded || retryCurrentTarget {
			nextAt = now.Add(probeSchedulerScanInterval)
		}
		if err := enqueueIncidentCanaryTx(ctx, tx, nextItem, *incident, incident.CanaryTargetID, nextAt, item.ID); err != nil {
			return IncidentCircuitState{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return IncidentCircuitState{}, err
	}
	return *incident, nil
}

func (r *Repository) PersistSafetyInventorySnapshot(ctx context.Context, snapshot SafetyInventorySnapshot) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_inventory_snapshots
			(user_id,admin_account_id,generation,complete,expires_at,created_at)
		VALUES($1,$2,$3,$4,$5,now())
		ON CONFLICT(user_id,admin_account_id,generation) DO UPDATE SET
			complete=EXCLUDED.complete,expires_at=EXCLUDED.expires_at
	`, snapshot.UserID, snapshot.WorkspaceID, snapshot.Generation, snapshot.Complete, snapshot.ExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_safety_inventory_accounts
		WHERE user_id=$1 AND admin_account_id=$2 AND generation=$3
	`, snapshot.UserID, snapshot.WorkspaceID, snapshot.Generation); err != nil {
		return err
	}
	for _, account := range snapshot.Accounts {
		models, marshalErr := json.Marshal(account.Models)
		if marshalErr != nil {
			return marshalErr
		}
		groups, marshalErr := json.Marshal(account.GroupIDs)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO connection_health_safety_inventory_accounts (
				user_id,admin_account_id,generation,account_id,target_id,active,schedulable,
				status_known,schedulable_known,capability_known,membership_known,models,group_ids,
				last_success_at,confirmed_failure_models
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13::jsonb,$14,$15)
		`, snapshot.UserID, snapshot.WorkspaceID, snapshot.Generation, account.AccountID,
			account.TargetID, account.Active, account.Schedulable, account.StatusKnown,
			account.SchedulableKnown, account.CapabilityKnown, account.MembershipKnown,
			models, groups, nullableSafetyTime(account.LastSuccessAt), account.ConfirmedFailureModels); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_safety_inventory_accounts
		WHERE user_id=$1 AND admin_account_id=$2 AND generation<>$3
	`, snapshot.UserID, snapshot.WorkspaceID, snapshot.Generation); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_safety_inventory_snapshots
		WHERE user_id=$1 AND admin_account_id=$2 AND generation<>$3
	`, snapshot.UserID, snapshot.WorkspaceID, snapshot.Generation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullableSafetyTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func scanSafetyInventoryAccounts(rows pgx.Rows) ([]SafetyInventoryAccount, error) {
	accounts := make([]SafetyInventoryAccount, 0)
	for rows.Next() {
		var account SafetyInventoryAccount
		var models, groups []byte
		var lastSuccess *time.Time
		if err := rows.Scan(
			&account.AccountID, &account.TargetID, &account.Active, &account.Schedulable,
			&account.StatusKnown, &account.SchedulableKnown, &account.CapabilityKnown,
			&account.MembershipKnown, &models, &groups, &lastSuccess,
			&account.ConfirmedFailureModels); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(models, &account.Models); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(groups, &account.GroupIDs); err != nil {
			return nil, err
		}
		if lastSuccess != nil {
			account.LastSuccessAt = *lastSuccess
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func containsSafetyValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueSortedSafetyValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (r *Repository) ReserveSafetyFloor(ctx context.Context, request FloorReservationRequest, now time.Time) (FloorReservation, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return FloorReservation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_floor_reservations
		WHERE user_id=$1 AND admin_account_id=$2 AND expires_at<=$3
			AND dispatching_at IS NULL AND readback_at IS NULL
	`, request.UserID, request.WorkspaceID, now); err != nil {
		return FloorReservation{}, err
	}
	var generation int64
	var complete bool
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT generation,complete,expires_at
		FROM connection_health_safety_inventory_snapshots
		WHERE user_id=$1 AND admin_account_id=$2
		ORDER BY generation DESC LIMIT 1 FOR UPDATE
	`, request.UserID, request.WorkspaceID).Scan(&generation, &complete, &expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FloorReservation{GuardHeld: true, Reason: "inventory_unknown"}, tx.Commit(ctx)
		}
		return FloorReservation{}, err
	}
	if !complete || !now.Before(expiresAt) || (request.ExpectedGeneration != 0 && request.ExpectedGeneration != generation) {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "inventory_stale_or_incomplete"}, tx.Commit(ctx)
	}
	groups := uniqueSortedSafetyValues(request.ControlledGroupIDs)
	models := uniqueSortedSafetyValues(request.ControlledModels)
	if len(groups) == 0 || len(models) == 0 {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "controlled_scope_unknown"}, tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `
		SELECT account_id,target_id,active,schedulable,status_known,schedulable_known,
			capability_known,membership_known,models,group_ids,last_success_at,confirmed_failure_models
		FROM connection_health_safety_inventory_accounts
		WHERE user_id=$1 AND admin_account_id=$2 AND generation=$3
		ORDER BY account_id
	`, request.UserID, request.WorkspaceID, generation)
	if err != nil {
		return FloorReservation{}, err
	}
	accounts, err := scanSafetyInventoryAccounts(rows)
	rows.Close()
	if err != nil {
		return FloorReservation{}, err
	}
	removed := make(map[string]struct{})
	var ownedReservation *FloorReservation
	reservationRows, err := tx.Query(ctx, `
		SELECT id,account_id,incident_id,inventory_generation
		FROM connection_health_floor_reservations
		WHERE user_id=$1 AND admin_account_id=$2 AND readback_at IS NULL
			AND (dispatching_at IS NOT NULL OR expires_at>$3)
		FOR UPDATE
	`, request.UserID, request.WorkspaceID, now)
	if err != nil {
		return FloorReservation{}, err
	}
	for reservationRows.Next() {
		var reservationID, accountID, incidentID string
		var reservationGeneration int64
		if err := reservationRows.Scan(&reservationID, &accountID, &incidentID, &reservationGeneration); err != nil {
			reservationRows.Close()
			return FloorReservation{}, err
		}
		if accountID == request.AccountID && incidentID == request.IncidentID {
			ownedReservation = &FloorReservation{ID: reservationID, Generation: reservationGeneration}
			continue
		}
		removed[accountID] = struct{}{}
	}
	reservationRows.Close()
	if err := reservationRows.Err(); err != nil {
		return FloorReservation{}, err
	}
	if ownedReservation != nil {
		ttl := request.ReservationTTL
		if ttl <= 0 {
			ttl = 2 * time.Minute
		}
		if _, err := tx.Exec(ctx, `
			UPDATE connection_health_floor_reservations SET expires_at=$2 WHERE id=$1
		`, ownedReservation.ID, now.Add(ttl)); err != nil {
			return FloorReservation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return FloorReservation{}, err
		}
		return *ownedReservation, nil
	}

	var target *SafetyInventoryAccount
	for index := range accounts {
		if accounts[index].AccountID == request.AccountID {
			target = &accounts[index]
			break
		}
	}
	if target == nil {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "target_missing"}, tx.Commit(ctx)
	}
	if !target.StatusKnown || !target.SchedulableKnown || !target.CapabilityKnown || !target.MembershipKnown {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "target_fields_unknown"}, tx.Commit(ctx)
	}
	if !target.Active || !target.Schedulable {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "target_not_survivor_eligible"}, tx.Commit(ctx)
	}
	if _, alreadyReserved := removed[request.AccountID]; alreadyReserved {
		return FloorReservation{Generation: generation, GuardHeld: true, Reason: "target_already_reserved"}, tx.Commit(ctx)
	}

	for _, account := range accounts {
		relevant := false
		for _, groupID := range groups {
			relevant = relevant || containsSafetyValue(account.GroupIDs, groupID)
		}
		for _, model := range models {
			relevant = relevant || containsSafetyValue(account.Models, model)
		}
		if relevant && (!account.StatusKnown || !account.SchedulableKnown || !account.CapabilityKnown || !account.MembershipKnown) {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: "survivor_fields_unknown"}, tx.Commit(ctx)
		}
	}

	chooseSticky := func(scopeKind, scopeID string, candidates []SafetyInventoryAccount) (string, string, error) {
		candidateByID := make(map[string]SafetyInventoryAccount, len(candidates))
		for _, candidate := range candidates {
			candidateByID[candidate.AccountID] = candidate
		}
		stickyEligible := func(candidate SafetyInventoryAccount) bool {
			if _, reserved := removed[candidate.AccountID]; reserved {
				return false
			}
			return candidate.StatusKnown && candidate.SchedulableKnown && candidate.CapabilityKnown &&
				candidate.MembershipKnown && candidate.Active && candidate.Schedulable
		}
		var accountID string
		err := tx.QueryRow(ctx, `
			SELECT account_id FROM connection_health_incident_survivors
			WHERE user_id=$1 AND admin_account_id=$2 AND incident_id=$3
				AND scope_kind=$4 AND scope_id=$5
		`, request.UserID, request.WorkspaceID, request.IncidentID, scopeKind, scopeID).Scan(&accountID)
		if err == nil {
			candidate, exists := candidateByID[accountID]
			if !exists {
				return "", "sticky_survivor_missing", nil
			}
			if !stickyEligible(candidate) {
				return "", "sticky_survivor_unavailable", nil
			}
			return accountID, "", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
		survivors := make([]SurvivorCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if _, reserved := removed[candidate.AccountID]; reserved {
				continue
			}
			survivors = append(survivors, SurvivorCandidate{
				AccountID: candidate.AccountID, Active: candidate.Active, Schedulable: candidate.Schedulable,
				StatusKnown: candidate.StatusKnown, SchedulableKnown: candidate.SchedulableKnown,
				CapabilityKnown: candidate.CapabilityKnown, MembershipKnown: candidate.MembershipKnown,
				LastSuccessAt: candidate.LastSuccessAt, ConfirmedFailureModels: candidate.ConfirmedFailureModels,
			})
		}
		selected, ok := stableSurvivor(survivors)
		if !ok {
			return "", "sticky_survivor_unavailable", nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO connection_health_incident_survivors
				(user_id,admin_account_id,incident_id,scope_kind,scope_id,account_id,created_at)
			VALUES($1,$2,$3,$4,$5,$6,now())
			ON CONFLICT(user_id,admin_account_id,incident_id,scope_kind,scope_id) DO NOTHING
		`, request.UserID, request.WorkspaceID, request.IncidentID, scopeKind, scopeID, selected.AccountID); err != nil {
			return "", "", err
		}
		return selected.AccountID, "", nil
	}

	for _, groupID := range groups {
		if !containsSafetyValue(target.GroupIDs, groupID) {
			continue
		}
		candidates := make([]SafetyInventoryAccount, 0)
		for _, account := range accounts {
			if containsSafetyValue(account.GroupIDs, groupID) {
				candidates = append(candidates, account)
			}
		}
		sticky, reason, err := chooseSticky("group", groupID, candidates)
		if err != nil {
			return FloorReservation{}, err
		}
		if reason != "" {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: reason}, tx.Commit(ctx)
		}
		if sticky == request.AccountID {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: "sticky_group_survivor"}, tx.Commit(ctx)
		}
	}
	for _, model := range models {
		if !containsSafetyValue(target.Models, model) {
			continue
		}
		candidates := make([]SafetyInventoryAccount, 0)
		for _, account := range accounts {
			if containsSafetyValue(account.Models, model) {
				candidates = append(candidates, account)
			}
		}
		sticky, reason, err := chooseSticky("model", model, candidates)
		if err != nil {
			return FloorReservation{}, err
		}
		if reason != "" {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: reason}, tx.Commit(ctx)
		}
		if sticky == request.AccountID {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: "sticky_model_survivor"}, tx.Commit(ctx)
		}
	}

	removed[request.AccountID] = struct{}{}
	eligible := func(account SafetyInventoryAccount) bool {
		if _, excluded := removed[account.AccountID]; excluded {
			return false
		}
		return account.StatusKnown && account.SchedulableKnown && account.CapabilityKnown &&
			account.MembershipKnown && account.Active && account.Schedulable
	}
	for _, groupID := range groups {
		remaining := 0
		for _, account := range accounts {
			if eligible(account) && containsSafetyValue(account.GroupIDs, groupID) {
				remaining++
			}
		}
		if remaining == 0 {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: "group_survivor_floor"}, tx.Commit(ctx)
		}
	}
	for _, model := range models {
		remaining := 0
		for _, account := range accounts {
			if eligible(account) && containsSafetyValue(account.Models, model) {
				remaining++
			}
		}
		if remaining == 0 {
			return FloorReservation{Generation: generation, GuardHeld: true, Reason: "model_survivor_floor"}, tx.Commit(ctx)
		}
	}

	reservationID, err := newID()
	if err != nil {
		return FloorReservation{}, err
	}
	ttl := request.ReservationTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_floor_reservations
			(id,user_id,admin_account_id,account_id,incident_id,reason,inventory_generation,expires_at,created_at)
		VALUES($1,$2,$3,$4,$5,'confirmed_health_incident',$6,$7,now())
	`, reservationID, request.UserID, request.WorkspaceID, request.AccountID,
		request.IncidentID, generation, now.Add(ttl)); err != nil {
		return FloorReservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FloorReservation{}, err
	}
	return FloorReservation{ID: reservationID, Generation: generation}, nil
}

func (r *Repository) MarkFloorReservationDispatching(ctx context.Context, reservationID string, now time.Time) error {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_floor_reservations
		SET dispatching_at=COALESCE(dispatching_at,$2)
		WHERE id=$1 AND readback_at IS NULL
	`, reservationID, now)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) CompleteFloorReservation(ctx context.Context, reservationID string, readback bool, snapshotInvalidated bool, now time.Time) error {
	_, err := r.db.Exec(ctx, `
		WITH completed AS (
			UPDATE connection_health_floor_reservations SET
				readback_at=CASE WHEN $2 THEN $4 ELSE readback_at END,
				snapshot_invalidated_at=CASE WHEN $3 THEN $4 ELSE snapshot_invalidated_at END
			WHERE id=$1
			RETURNING user_id,admin_account_id,inventory_generation
		)
		UPDATE connection_health_safety_inventory_snapshots snapshot
		SET complete=false,expires_at=LEAST(snapshot.expires_at,$4)
		FROM completed
		WHERE $3 AND snapshot.user_id=completed.user_id
			AND snapshot.admin_account_id=completed.admin_account_id
			AND snapshot.generation=completed.inventory_generation
	`, reservationID, readback, snapshotInvalidated, now)
	return err
}

func (r *Repository) ReleaseFloorReservation(ctx context.Context, reservationID string) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM connection_health_floor_reservations
		WHERE id=$1 AND dispatching_at IS NULL AND readback_at IS NULL
	`, reservationID)
	return err
}

func (r *Repository) AbandonFloorReservationBeforeDispatch(ctx context.Context, reservationID string) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM connection_health_floor_reservations
		WHERE id=$1 AND readback_at IS NULL
	`, reservationID)
	return err
}

func (r *Repository) ResolveIncidentFloorReservations(
	ctx context.Context,
	userID string,
	workspaceID string,
	accountID string,
	incidentID string,
	snapshotInvalidated bool,
	now time.Time,
) error {
	_, err := r.db.Exec(ctx, `
		WITH completed AS (
			UPDATE connection_health_floor_reservations SET
				readback_at=$6,
				snapshot_invalidated_at=CASE WHEN $5 THEN $6 ELSE snapshot_invalidated_at END
			WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
				AND incident_id=$4 AND readback_at IS NULL
			RETURNING user_id,admin_account_id,inventory_generation
		)
		UPDATE connection_health_safety_inventory_snapshots snapshot
		SET complete=false,expires_at=LEAST(snapshot.expires_at,$6)
		FROM completed
		WHERE $5 AND snapshot.user_id=completed.user_id
			AND snapshot.admin_account_id=completed.admin_account_id
			AND snapshot.generation=completed.inventory_generation
	`, userID, workspaceID, accountID, incidentID, snapshotInvalidated, now)
	return err
}

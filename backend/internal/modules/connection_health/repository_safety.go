package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	mutationLeaseTTL          = 5 * time.Minute
	mutationLeaseQueryTimeout = 5 * time.Second
)

func (r *Repository) GetSafetySettings(ctx context.Context, userID, workspaceID string) (SafetySettings, error) {
	settings := DefaultSafetySettings()
	var delays []byte
	err := r.db.QueryRow(ctx, `
		SELECT confirmation_observation_count, confirmation_delays_seconds,
			confirmation_jitter_seconds, abnormal_queue_capacity, manual_reserved_slots,
			updated_by, updated_at
		FROM connection_health_safety_settings WHERE user_id=$1 AND admin_account_id=$2
	`, userID, workspaceID).Scan(&settings.ConfirmationObservationCount, &delays,
		&settings.ConfirmationJitterSeconds, &settings.AbnormalQueueCapacity,
		&settings.ManualReservedSlots, &settings.UpdatedBy, &settings.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		settings.UserID = userID
		settings.WorkspaceID = workspaceID
		return settings, nil
	}
	if err != nil {
		return SafetySettings{}, err
	}
	if err := json.Unmarshal(delays, &settings.ConfirmationDelaysSeconds); err != nil {
		return SafetySettings{}, err
	}
	if err := settings.Validate(); err != nil {
		return SafetySettings{}, err
	}
	settings.UserID = userID
	settings.WorkspaceID = workspaceID
	return settings, nil
}

func (r *Repository) UpsertSafetySettings(ctx context.Context, settings SafetySettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	delays, err := json.Marshal(settings.ConfirmationDelaysSeconds)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	effectiveAt := time.Now().UTC()
	settings.UpdatedAt = effectiveAt

	oldSettings := DefaultSafetySettings()
	oldSettings.UserID = settings.UserID
	oldSettings.WorkspaceID = settings.WorkspaceID
	var oldDelays []byte
	readErr := tx.QueryRow(ctx, `
		SELECT confirmation_observation_count,confirmation_delays_seconds,
			confirmation_jitter_seconds,abnormal_queue_capacity,manual_reserved_slots,
			updated_by,updated_at
		FROM connection_health_safety_settings
		WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE
	`, settings.UserID, settings.WorkspaceID).Scan(
		&oldSettings.ConfirmationObservationCount, &oldDelays,
		&oldSettings.ConfirmationJitterSeconds, &oldSettings.AbnormalQueueCapacity,
		&oldSettings.ManualReservedSlots, &oldSettings.UpdatedBy, &oldSettings.UpdatedAt)
	if readErr == nil {
		if err := json.Unmarshal(oldDelays, &oldSettings.ConfirmationDelaysSeconds); err != nil {
			return err
		}
	} else if !errors.Is(readErr, pgx.ErrNoRows) {
		return readErr
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_settings (
			user_id, admin_account_id, confirmation_observation_count,
			confirmation_delays_seconds, confirmation_jitter_seconds,
			abnormal_queue_capacity, manual_reserved_slots, updated_by, updated_at
		) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9)
		ON CONFLICT (user_id, admin_account_id) DO UPDATE SET
			confirmation_observation_count=EXCLUDED.confirmation_observation_count,
			confirmation_delays_seconds=EXCLUDED.confirmation_delays_seconds,
			confirmation_jitter_seconds=EXCLUDED.confirmation_jitter_seconds,
			abnormal_queue_capacity=EXCLUDED.abnormal_queue_capacity,
			manual_reserved_slots=EXCLUDED.manual_reserved_slots,
			updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at
	`, settings.UserID, settings.WorkspaceID, settings.ConfirmationObservationCount,
		delays, settings.ConfirmationJitterSeconds, settings.AbnormalQueueCapacity,
		settings.ManualReservedSlots, settings.UpdatedBy, effectiveAt); err != nil {
		return err
	}
	oldJSON, err := json.Marshal(oldSettings)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_audits
			(id,user_id,admin_account_id,audit_type,actor,old_value,new_value,detail,created_at)
		VALUES($1,$2,$3,'settings_updated',$4,$5::jsonb,$6::jsonb,'',$7)
	`, auditID, settings.UserID, settings.WorkspaceID, settings.UpdatedBy, oldJSON, newJSON, effectiveAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) GetLatestEmergencyClear(ctx context.Context, userID, workspaceID string) (*EmergencyClearResult, error) {
	var cached []byte
	err := r.db.QueryRow(ctx, `
		SELECT result FROM connection_health_emergency_clears
		WHERE user_id=$1 AND admin_account_id=$2
		ORDER BY created_at DESC LIMIT 1
	`, userID, workspaceID).Scan(&cached)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result EmergencyClearResult
	if err := json.Unmarshal(cached, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *Repository) GetSafetyQueueSummary(ctx context.Context, userID, workspaceID string) (SafetyQueueSummary, error) {
	var summary SafetyQueueSummary
	err := r.db.QueryRow(ctx, `
		SELECT
			count(*) FILTER (
				WHERE q.state='queued' AND q.abnormal_queue_epoch=COALESCE(e.abnormal_queue_epoch,0)
			)::int,
			count(*) FILTER (
				WHERE q.state='claimed' AND q.abnormal_queue_epoch=COALESCE(e.abnormal_queue_epoch,0)
			)::int,
			count(*) FILTER (WHERE q.state='dispatching')::int,
			count(*) FILTER (
				WHERE q.state='guard-held' AND q.abnormal_queue_epoch=COALESCE(e.abnormal_queue_epoch,0)
			)::int,
			count(DISTINCT q.incident_id) FILTER (
				WHERE q.incident_id<>'' AND (
					q.state='dispatching' OR (
						q.state IN ('queued','claimed','guard-held')
						AND q.abnormal_queue_epoch=COALESCE(e.abnormal_queue_epoch,0)
					)
				)
			)::int
		FROM connection_health_abnormal_queue q
		LEFT JOIN connection_health_safety_epochs e
			ON e.user_id=q.user_id AND e.admin_account_id=q.admin_account_id
		WHERE q.user_id=$1 AND q.admin_account_id=$2 AND q.source=$3
	`, userID, workspaceID, SafetySourceHealthIncident).Scan(
		&summary.Queued, &summary.Claimed, &summary.Dispatching, &summary.GuardHeld, &summary.Incidents)
	return summary, err
}

func (r *Repository) GetAbnormalQueueEpoch(ctx context.Context, userID, workspaceID string) (int64, error) {
	var epoch int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO connection_health_safety_epochs (user_id, admin_account_id)
		VALUES ($1,$2)
		ON CONFLICT (user_id, admin_account_id)
		DO UPDATE SET updated_at=connection_health_safety_epochs.updated_at
		RETURNING abnormal_queue_epoch
	`, userID, workspaceID).Scan(&epoch)
	return epoch, err
}

func queueItemColumns(prefix string) string {
	return prefix + `id,` + prefix + `user_id,` + prefix + `admin_account_id,` + prefix + `target_id,` +
		prefix + `account_id,` + prefix + `model_name,` + prefix + `provider_family,` + prefix + `probe_prompt,` +
		prefix + `max_probe_tokens,` + prefix + `queue_kind,` + prefix + `source,` + prefix + `incident_id,` +
		prefix + `fault_domain,` + prefix + `observation_epoch,` + prefix + `normal_generation,` +
		prefix + `abnormal_queue_epoch,` + prefix + `attempt,` + prefix + `required_attempts,` +
		prefix + `confirmation_delays_seconds,` + prefix + `confirmation_jitter_seconds,` +
		prefix + `next_attempt_at,` + prefix + `action_key,` + prefix + `mutation_generation,` +
		prefix + `state,` + prefix + `claimed_by,` + prefix + `claim_expires_at,` +
		prefix + `expected_result,` + prefix + `last_result,` + prefix + `created_at,` + prefix + `updated_at`
}

type queueItemScanner interface {
	Scan(dest ...any) error
}

func scanAbnormalQueueItem(row queueItemScanner) (AbnormalQueueItem, error) {
	var item AbnormalQueueItem
	var delays []byte
	err := row.Scan(
		&item.ID, &item.UserID, &item.WorkspaceID, &item.TargetID, &item.AccountID,
		&item.ModelName, &item.ProviderFamily, &item.ProbePrompt, &item.MaxProbeTokens,
		&item.Kind, &item.Source, &item.IncidentID, &item.FaultDomain,
		&item.ObservationEpoch, &item.NormalGeneration, &item.QueueEpoch,
		&item.Attempt, &item.RequiredAttempts, &delays, &item.ConfirmationJitter,
		&item.NextAttemptAt, &item.ActionKey, &item.MutationGeneration, &item.State,
		&item.ClaimedBy, &item.ClaimExpiresAt, &item.ExpectedResult, &item.LastResult,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return AbnormalQueueItem{}, err
	}
	if len(delays) > 0 {
		if err := json.Unmarshal(delays, &item.ConfirmationDelays); err != nil {
			return AbnormalQueueItem{}, err
		}
	}
	return item, nil
}

func (r *Repository) EnqueueAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, capacity int) (AbnormalQueueItem, bool, error) {
	if item.Source != SafetySourceHealthIncident {
		return AbnormalQueueItem{}, false, errors.New("abnormal queue source must be health_incident")
	}
	if capacity <= 0 {
		capacity = DefaultSafetySettings().AbnormalQueueCapacity
	}
	if item.ID == "" {
		id, err := newID()
		if err != nil {
			return AbnormalQueueItem{}, false, err
		}
		item.ID = id
	}
	if item.State == "" {
		item.State = QueueStateQueued
	}
	if item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = time.Now().UTC()
	}
	delays, err := json.Marshal(item.ConfirmationDelays)
	if err != nil {
		return AbnormalQueueItem{}, false, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return AbnormalQueueItem{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_epochs(user_id,admin_account_id)
		VALUES($1,$2) ON CONFLICT(user_id,admin_account_id) DO NOTHING
	`, item.UserID, item.WorkspaceID); err != nil {
		return AbnormalQueueItem{}, false, err
	}
	var currentEpoch int64
	if err := tx.QueryRow(ctx, `
		SELECT abnormal_queue_epoch FROM connection_health_safety_epochs
		WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE
	`, item.UserID, item.WorkspaceID).Scan(&currentEpoch); err != nil {
		return AbnormalQueueItem{}, false, err
	}
	if currentEpoch != item.QueueEpoch {
		item.State = QueueStateCancelled
		item.LastResult = "stale_abnormal_queue_epoch"
		return item, false, nil
	}

	existing, readErr := scanAbnormalQueueItem(tx.QueryRow(ctx, `
		SELECT `+queueItemColumns("")+` FROM connection_health_abnormal_queue
		WHERE user_id=$1 AND admin_account_id=$2 AND action_key=$3 FOR UPDATE
	`, item.UserID, item.WorkspaceID, item.ActionKey))
	if readErr == nil && (existing.State == QueueStateQueued || existing.State == QueueStateClaimed || existing.State == QueueStateDispatching) {
		if err := tx.Commit(ctx); err != nil {
			return AbnormalQueueItem{}, false, err
		}
		return existing, false, nil
	}
	if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
		return AbnormalQueueItem{}, false, readErr
	}
	if readErr == nil {
		item.ID = existing.ID
	}

	var active int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM connection_health_abnormal_queue
		WHERE user_id=$1 AND admin_account_id=$2 AND source=$3
			AND state IN ('queued','claimed','dispatching')
	`, item.UserID, item.WorkspaceID, SafetySourceHealthIncident).Scan(&active); err != nil {
		return AbnormalQueueItem{}, false, err
	}
	if active >= capacity {
		item.State = QueueStateGuardHeld
		item.LastResult = "abnormal_queue_capacity"
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO connection_health_abnormal_queue (
			id,user_id,admin_account_id,target_id,account_id,model_name,provider_family,probe_prompt,
			max_probe_tokens,queue_kind,source,incident_id,fault_domain,observation_epoch,
			normal_generation,abnormal_queue_epoch,attempt,required_attempts,
			confirmation_delays_seconds,confirmation_jitter_seconds,next_attempt_at,action_key,
			mutation_generation,state,claimed_by,claim_expires_at,expected_result,last_result,
			created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			$19::jsonb,$20,$21,$22,$23,$24,'',NULL,$25,$26,now(),now())
		ON CONFLICT (user_id,admin_account_id,action_key) DO UPDATE SET
			target_id=EXCLUDED.target_id,account_id=EXCLUDED.account_id,model_name=EXCLUDED.model_name,
			provider_family=EXCLUDED.provider_family,probe_prompt=EXCLUDED.probe_prompt,
			max_probe_tokens=EXCLUDED.max_probe_tokens,queue_kind=EXCLUDED.queue_kind,
			source=EXCLUDED.source,incident_id=EXCLUDED.incident_id,fault_domain=EXCLUDED.fault_domain,
			observation_epoch=EXCLUDED.observation_epoch,normal_generation=EXCLUDED.normal_generation,
			abnormal_queue_epoch=EXCLUDED.abnormal_queue_epoch,attempt=EXCLUDED.attempt,
			required_attempts=EXCLUDED.required_attempts,
			confirmation_delays_seconds=EXCLUDED.confirmation_delays_seconds,
			confirmation_jitter_seconds=EXCLUDED.confirmation_jitter_seconds,
			next_attempt_at=EXCLUDED.next_attempt_at,mutation_generation=EXCLUDED.mutation_generation,
			state=EXCLUDED.state,claimed_by='',claim_expires_at=NULL,
			expected_result=EXCLUDED.expected_result,last_result=EXCLUDED.last_result,updated_at=now()
	`, item.ID, item.UserID, item.WorkspaceID, item.TargetID, item.AccountID, item.ModelName,
		item.ProviderFamily, item.ProbePrompt, item.MaxProbeTokens, item.Kind, item.Source,
		item.IncidentID, item.FaultDomain, item.ObservationEpoch, item.NormalGeneration,
		item.QueueEpoch, item.Attempt, item.RequiredAttempts, delays, item.ConfirmationJitter,
		item.NextAttemptAt, item.ActionKey, item.MutationGeneration, item.State,
		item.ExpectedResult, item.LastResult)
	if err != nil {
		return AbnormalQueueItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AbnormalQueueItem{}, false, err
	}
	return item, item.State == QueueStateQueued, nil
}

func (r *Repository) ClaimAbnormalQueueItem(ctx context.Context, workerID string, now time.Time) (*AbnormalQueueItem, error) {
	item, err := scanAbnormalQueueItem(r.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT q.id FROM connection_health_abnormal_queue q
			WHERE q.source=$1 AND (
				(q.state='queued' AND q.next_attempt_at <= $2) OR
				(q.state='claimed' AND q.claim_expires_at <= $2) OR
				(q.state='dispatching' AND q.claim_expires_at <= $2)
			) AND NOT EXISTS (
				SELECT 1 FROM connection_health_abnormal_queue active
				WHERE active.id<>q.id AND active.user_id=q.user_id
					AND active.admin_account_id=q.admin_account_id
					AND active.fault_domain<>'' AND active.fault_domain=q.fault_domain
					AND active.state IN ('claimed','dispatching')
					AND active.claim_expires_at>$2
			)
			AND pg_try_advisory_xact_lock(hashtextextended(
				jsonb_build_array(q.user_id,q.admin_account_id,q.fault_domain)::text, 0
			))
			ORDER BY CASE q.state WHEN 'dispatching' THEN 0 WHEN 'claimed' THEN 1 ELSE 2 END,
				q.next_attempt_at,q.created_at LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE connection_health_abnormal_queue q SET
			state=CASE WHEN q.state='dispatching' THEN 'dispatching' ELSE 'claimed' END,
			claimed_by=$3,claim_expires_at=$2+interval '2 minutes',updated_at=now()
		FROM candidate WHERE q.id=candidate.id
		RETURNING `+queueItemColumns("q.")+`
	`, SafetySourceHealthIncident, now, workerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// The partial unique index is the cross-process fault-domain claim fence.
		// Losing that race is ordinary queue contention, not a worker failure.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) HeartbeatAbnormalQueueClaim(ctx context.Context, id, workerID string) error {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue
		SET claim_expires_at=now()+interval '2 minutes',updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND state IN ('claimed','dispatching')
	`, id, workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) RequeueAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, workerID string) error {
	delays, err := json.Marshal(item.ConfirmationDelays)
	if err != nil {
		return err
	}
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='queued',attempt=$3,
			next_attempt_at=$4,last_result=$5,claimed_by='',claim_expires_at=NULL,
			confirmation_delays_seconds=$6::jsonb,confirmation_jitter_seconds=$7,updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND state='claimed'
	`, item.ID, workerID, item.Attempt, item.NextAttemptAt, item.LastResult, delays, item.ConfirmationJitter)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) RescheduleDispatchedAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, workerID string) error {
	delays, err := json.Marshal(item.ConfirmationDelays)
	if err != nil {
		return err
	}
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='queued',attempt=$3,
			next_attempt_at=$4,last_result=$5,claimed_by='',claim_expires_at=NULL,
			confirmation_delays_seconds=$6::jsonb,confirmation_jitter_seconds=$7,updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND state='dispatching'
	`, item.ID, workerID, item.Attempt, item.NextAttemptAt, item.LastResult, delays, item.ConfirmationJitter)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) RescheduleUncertainStatusDispatch(ctx context.Context, item AbnormalQueueItem, workerID string) error {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='dispatching',attempt=$3,
			next_attempt_at=$4,last_result=$5,claimed_by='',claim_expires_at=$4,updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND state='dispatching' AND queue_kind=$6
	`, item.ID, workerID, item.Attempt, item.NextAttemptAt, item.LastResult, QueueKindStatusIntent)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) CancelAbnormalQueueItem(ctx context.Context, id, workerID, reason string) error {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='cancelled',last_result=$3,
			claimed_by='',claim_expires_at=NULL,updated_at=now()
		WHERE id=$1 AND state IN ('queued','claimed') AND ($2='' OR claimed_by=$2)
	`, id, workerID, reason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) MarkAbnormalQueueDispatching(ctx context.Context, id, workerID string, queueEpoch int64) (bool, error) {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue q
		SET state='dispatching',claim_expires_at=now()+interval '2 minutes',updated_at=now()
		FROM connection_health_safety_epochs e
		WHERE q.id=$1 AND q.claimed_by=$2 AND q.state='claimed'
			AND q.abnormal_queue_epoch=$3 AND e.user_id=q.user_id
			AND e.admin_account_id=q.admin_account_id
			AND e.abnormal_queue_epoch=q.abnormal_queue_epoch
	`, id, workerID, queueEpoch)
	return command.RowsAffected() == 1, err
}

func (r *Repository) CompleteAbnormalQueueItem(ctx context.Context, id, workerID, result string) error {
	command, err := r.db.Exec(ctx, `
		UPDATE connection_health_abnormal_queue SET state='completed',last_result=$3,
			claim_expires_at=NULL,updated_at=now()
		WHERE id=$1 AND claimed_by=$2 AND state='dispatching'
	`, id, workerID, result)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errStaleMutation
	}
	return nil
}

func (r *Repository) EmergencyClear(ctx context.Context, userID, workspaceID, idempotencyKey string, now time.Time) (EmergencyClearResult, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return EmergencyClearResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_epochs(user_id,admin_account_id)
		VALUES($1,$2) ON CONFLICT(user_id,admin_account_id) DO NOTHING
	`, userID, workspaceID); err != nil {
		return EmergencyClearResult{}, err
	}
	var currentEpoch int64
	if err := tx.QueryRow(ctx, `
		SELECT abnormal_queue_epoch FROM connection_health_safety_epochs
		WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE
	`, userID, workspaceID).Scan(&currentEpoch); err != nil {
		return EmergencyClearResult{}, err
	}
	var cached []byte
	cacheErr := tx.QueryRow(ctx, `
		SELECT result FROM connection_health_emergency_clears
		WHERE user_id=$1 AND admin_account_id=$2 AND idempotency_key=$3 AND expires_at>$4
	`, userID, workspaceID, idempotencyKey, now).Scan(&cached)
	if cacheErr == nil {
		var result EmergencyClearResult
		if err := json.Unmarshal(cached, &result); err != nil {
			return EmergencyClearResult{}, err
		}
		result.Idempotent = true
		if err := tx.Commit(ctx); err != nil {
			return EmergencyClearResult{}, err
		}
		return result, nil
	}
	if !errors.Is(cacheErr, pgx.ErrNoRows) {
		return EmergencyClearResult{}, cacheErr
	}
	// The primary key is also the 24-hour idempotency key. Remove only the
	// expired matching record before reusing that key; the append-only safety
	// audit remains the long-term diagnostic history.
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_emergency_clears
		WHERE user_id=$1 AND admin_account_id=$2 AND idempotency_key=$3 AND expires_at<=$4
	`, userID, workspaceID, idempotencyKey, now); err != nil {
		return EmergencyClearResult{}, err
	}
	epoch := currentEpoch + 1
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_safety_epochs SET abnormal_queue_epoch=$3,updated_at=now()
		WHERE user_id=$1 AND admin_account_id=$2
	`, userID, workspaceID, epoch); err != nil {
		return EmergencyClearResult{}, err
	}
	var cancelled, incidents int
	if err := tx.QueryRow(ctx, `
		WITH changed AS (
			UPDATE connection_health_abnormal_queue SET state='cancelled',
				last_result='emergency_clear',claimed_by='',claim_expires_at=NULL,updated_at=now()
			WHERE user_id=$1 AND admin_account_id=$2 AND source=$3
				AND state IN ('queued','claimed') RETURNING incident_id
		) SELECT count(*)::int,count(DISTINCT NULLIF(incident_id,''))::int FROM changed
	`, userID, workspaceID, SafetySourceHealthIncident).Scan(&cancelled, &incidents); err != nil {
		return EmergencyClearResult{}, err
	}
	// A claimed worker may have persisted an incident intent checkpoint just
	// before this transaction acquired the epoch row. Clear only that source;
	// normal priority/restore ownership remains intact for the next scan.
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_priority_sync_states
		SET pending_priority=NULL, pending_mutation_generation=0, pending_source='',
			pending_epoch=0, pending_action_key='', updated_at=now()
		WHERE user_id=$1 AND admin_account_id=$2 AND pending_source=$3
			AND pending_epoch < $4
	`, userID, workspaceID, SafetySourceHealthIncident, epoch); err != nil {
		return EmergencyClearResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_target_action_states
		SET pending_status='', pending_weight=NULL, pending_mutation_generation=0,
			pending_source='', pending_epoch=0, pending_action_key='', updated_at=now()
		WHERE user_id=$1 AND admin_account_id=$2 AND pending_source=$3
			AND pending_epoch < $4
	`, userID, workspaceID, SafetySourceHealthIncident, epoch); err != nil {
		return EmergencyClearResult{}, err
	}
	var openIncidents int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM connection_health_incidents
		WHERE user_id=$1 AND admin_account_id=$2 AND state IN ('open','half_open')
	`, userID, workspaceID).Scan(&openIncidents); err != nil {
		return EmergencyClearResult{}, err
	}
	if openIncidents > incidents {
		incidents = openIncidents
	}
	if _, err := tx.Exec(ctx, `
		UPDATE connection_health_incidents SET state='closed',normal_generation=0,
			canary_target_id='',successful_canary_target_id='',updated_at=now()
		WHERE user_id=$1 AND admin_account_id=$2 AND state IN ('open','half_open')
	`, userID, workspaceID); err != nil {
		return EmergencyClearResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM connection_health_incident_survivors
		WHERE user_id=$1 AND admin_account_id=$2
	`, userID, workspaceID); err != nil {
		return EmergencyClearResult{}, err
	}
	var dispatching int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM connection_health_abnormal_queue
		WHERE user_id=$1 AND admin_account_id=$2 AND source=$3 AND state='dispatching'
	`, userID, workspaceID, SafetySourceHealthIncident).Scan(&dispatching); err != nil {
		return EmergencyClearResult{}, err
	}
	result := EmergencyClearResult{
		WorkspaceID: workspaceID, QueueEpoch: epoch, Cancelled: cancelled,
		Incidents: incidents, Dispatching: dispatching, CompletedAt: now.UTC(),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return EmergencyClearResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_emergency_clears
			(user_id,admin_account_id,idempotency_key,result,expires_at)
		VALUES($1,$2,$3,$4::jsonb,$5)
	`, userID, workspaceID, idempotencyKey, encoded, now.Add(24*time.Hour)); err != nil {
		return EmergencyClearResult{}, err
	}
	auditID, err := newID()
	if err != nil {
		return EmergencyClearResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connection_health_safety_audits
			(id,user_id,admin_account_id,audit_type,actor,old_value,new_value,detail,created_at)
		VALUES($1,$2,$3,'emergency_clear',$2,'{}'::jsonb,$4::jsonb,$5,$6)
	`, auditID, userID, workspaceID, encoded, idempotencyKey, now); err != nil {
		return EmergencyClearResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EmergencyClearResult{}, err
	}
	return result, nil
}

func (r *Repository) BumpMutationGeneration(ctx context.Context, userID, workspaceID, accountID string) (int64, error) {
	var generation int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO connection_health_mutation_fences
			(user_id,admin_account_id,account_id,generation,updated_at)
		VALUES($1,$2,$3,1,now())
		ON CONFLICT(user_id,admin_account_id,account_id)
		DO UPDATE SET generation=connection_health_mutation_fences.generation+1,updated_at=now()
		RETURNING generation
	`, userID, workspaceID, accountID).Scan(&generation)
	return generation, err
}

func (r *Repository) MutationGeneration(ctx context.Context, userID, workspaceID, accountID string) (int64, error) {
	var generation int64
	err := r.db.QueryRow(ctx, `
		SELECT generation FROM connection_health_mutation_fences
		WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
	`, userID, workspaceID, accountID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return generation, err
}

func (r *Repository) AcquireMutationLease(ctx context.Context, userID, workspaceID, accountID string, wait bool) (RepositoryMutationLease, error) {
	owner, err := newID()
	if err != nil {
		return RepositoryMutationLease{}, err
	}
	for {
		var generation, token int64
		err = r.db.QueryRow(ctx, `
			INSERT INTO connection_health_mutation_fences
				(user_id,admin_account_id,account_id,generation,lease_owner,fencing_token,lease_expires_at,updated_at)
				VALUES($1,$2,$3,0,$4,1,now()+interval '5 minutes',now())
				ON CONFLICT(user_id,admin_account_id,account_id) DO UPDATE SET
					lease_owner=$4,fencing_token=connection_health_mutation_fences.fencing_token+1,
					lease_expires_at=now()+interval '5 minutes',updated_at=now()
			WHERE connection_health_mutation_fences.lease_owner=''
				OR connection_health_mutation_fences.lease_expires_at IS NULL
				OR connection_health_mutation_fences.lease_expires_at<=now()
			RETURNING generation,fencing_token
		`, userID, workspaceID, accountID, owner).Scan(&generation, &token)
		if err == nil {
			done := make(chan struct{})
			var once sync.Once
			go func() {
				ticker := time.NewTicker(mutationLeaseTTL / 4)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						heartbeatCtx, cancelHeartbeat := context.WithTimeout(context.Background(), mutationLeaseQueryTimeout)
						command, heartbeatErr := r.db.Exec(heartbeatCtx, `
							UPDATE connection_health_mutation_fences
							SET lease_expires_at=now()+interval '5 minutes',updated_at=now()
							WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
								AND lease_owner=$4 AND fencing_token=$5
						`, userID, workspaceID, accountID, owner, token)
						cancelHeartbeat()
						if heartbeatErr != nil || command.RowsAffected() != 1 {
							return
						}
					}
				}
			}()
			release := func() {
				once.Do(func() {
					close(done)
					releaseCtx, cancelRelease := context.WithTimeout(context.Background(), mutationLeaseQueryTimeout)
					defer cancelRelease()
					_, _ = r.db.Exec(releaseCtx, `
						UPDATE connection_health_mutation_fences
						SET lease_owner='',lease_expires_at=NULL,updated_at=now()
						WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
							AND lease_owner=$4 AND fencing_token=$5
					`, userID, workspaceID, accountID, owner, token)
				})
			}
			validate := func(validateCtx context.Context) error {
				var currentGeneration int64
				validateErr := r.db.QueryRow(validateCtx, `
						UPDATE connection_health_mutation_fences
						SET lease_expires_at=now()+interval '5 minutes',updated_at=now()
						WHERE user_id=$1 AND admin_account_id=$2 AND account_id=$3
							AND lease_owner=$4 AND fencing_token=$5 AND lease_expires_at>now()
						RETURNING generation
					`, userID, workspaceID, accountID, owner, token).Scan(&currentGeneration)
				if errors.Is(validateErr, pgx.ErrNoRows) || currentGeneration != generation {
					return errStaleMutation
				}
				return validateErr
			}
			return RepositoryMutationLease{
				Generation: generation, FencingToken: token, Validate: validate, Release: release,
			}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return RepositoryMutationLease{}, err
		}
		if !wait {
			return RepositoryMutationLease{}, errMutationLeaseBusy
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return RepositoryMutationLease{}, ctx.Err()
		case <-timer.C:
		}
	}
}

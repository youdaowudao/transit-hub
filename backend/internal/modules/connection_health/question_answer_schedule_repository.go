package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Atomic methods never wait for inventory, runtime reservations or finalizers.
// Their caller holds the workspace service mutex; SQL takes the workspace
// guard before schedule, execution, target and C1 target locks in that order.
type questionAnswerScheduleRepository interface {
	GetQuestionAnswerRuntimeSettings(context.Context) (QuestionAnswerRuntimeSettings, error)
	SaveQuestionAnswerRuntimeSettings(context.Context, int, int64) (QuestionAnswerRuntimeSettings, error)
	GetQuestionAnswerScheduleLimits(context.Context, string, string) (QuestionAnswerScheduleLimits, error)
	SaveQuestionAnswerScheduleLimits(context.Context, string, string, QuestionAnswerScheduleLimits, int64, func() time.Time) (QuestionAnswerScheduleLimits, error)
	ListQuestionAnswerSchedules(context.Context, string, string, string, int, int) (QuestionAnswerSchedulePage, error)
	GetQuestionAnswerSchedule(context.Context, string, string, string) (*QuestionAnswerSchedule, error)
	CreateQuestionAnswerSchedule(context.Context, string, string, QuestionAnswerScheduleConfig, bool, QuestionAnswerScheduleSelectionSnapshot, func() time.Time) (*QuestionAnswerSchedule, error)
	UpdateQuestionAnswerSchedule(context.Context, string, string, string, QuestionAnswerScheduleConfig, QuestionAnswerScheduleSelectionSnapshot, int64, func() time.Time) (*QuestionAnswerSchedule, error)
	SetQuestionAnswerScheduleState(context.Context, string, string, string, string, string, int64, func() time.Time) (*QuestionAnswerSchedule, error)
	ListDueQuestionAnswerSchedules(context.Context, time.Time) ([]QuestionAnswerSchedule, error)
	ProcessDueQuestionAnswerSchedule(context.Context, string, string, string, func() time.Time) ([]QuestionAnswerScheduleExecution, error)
	CreateRunNowQuestionAnswerScheduleExecution(context.Context, string, string, string, string, func() time.Time) (*QuestionAnswerScheduleExecution, error)
	ClaimNextQuestionAnswerScheduleExecution(context.Context, string, string, func() time.Time) (*QuestionAnswerScheduleExecution, error)
	ListNonterminalQuestionAnswerScheduleExecutions(context.Context) ([]QuestionAnswerScheduleExecution, error)
	ListQuestionAnswerScheduleExecutions(context.Context, string, string, string, int, int) (QuestionAnswerScheduleExecutionPage, error)
	GetQuestionAnswerScheduleExecution(context.Context, string, string, string) (*QuestionAnswerScheduleExecution, error)
	ListQuestionAnswerScheduleExecutionTargets(context.Context, string, string, string) ([]QuestionAnswerScheduleExecutionTarget, error)
	ReadQuestionAnswerSchedulePreparationConfiguration(context.Context, string, string, []string) ([]TestQuestion, []GroupTestConfig, error)
	FreezeQuestionAnswerScheduleExecution(context.Context, string, string, string, QuestionAnswerScheduleResolved, []QuestionAnswerScheduleExecutionTarget, func() time.Time) (string, error)
	FinishQuestionAnswerSchedulePreparation(context.Context, string, string, string, QuestionAnswerSchedulePreparationOutcome, func() time.Time) (*QuestionAnswerScheduleExecution, error)
	DecideQuestionAnswerScheduleTermination(context.Context, string, string, string, string, *int64, func() time.Time) (QuestionAnswerScheduleTerminationResult, error)
	FinishQuestionAnswerScheduleExecution(context.Context, string, string, string, bool, string, func() time.Time) (*QuestionAnswerScheduleExecution, error)
	FailQuestionAnswerScheduleTarget(context.Context, string, string, string, string, string, string, func() time.Time) error
	CreateScheduledQuestionAnswerBatch(context.Context, string, string, string, string, string) ([]QuestionAnswerRecord, bool, error)
	ListQuestionAnswerScheduleHandleParents(context.Context, []QuestionAnswerScheduleHandleIdentity) (map[string]bool, error)
}

type scheduleQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) GetQuestionAnswerRuntimeSettings(ctx context.Context) (QuestionAnswerRuntimeSettings, error) {
	s := QuestionAnswerRuntimeSettings{QuestionAnswerConcurrency: 15}
	err := r.db.QueryRow(ctx, `SELECT question_answer_concurrency,version,updated_at FROM connection_health_question_answer_runtime_settings WHERE singleton_key='global'`).Scan(&s.QuestionAnswerConcurrency, &s.Version, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}

func (r *Repository) SaveQuestionAnswerRuntimeSettings(ctx context.Context, limit int, version int64) (QuestionAnswerRuntimeSettings, error) {
	if limit < 1 || limit > 50 || version < 0 {
		return QuestionAnswerRuntimeSettings{}, requestError(ErrorRequest)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return QuestionAnswerRuntimeSettings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('connection-health:question-answer:global',0))`); err != nil {
		return QuestionAnswerRuntimeSettings{}, err
	}
	current := QuestionAnswerRuntimeSettings{QuestionAnswerConcurrency: 15}
	err = tx.QueryRow(ctx, `SELECT question_answer_concurrency,version,updated_at FROM connection_health_question_answer_runtime_settings WHERE singleton_key='global' FOR UPDATE`).Scan(&current.QuestionAnswerConcurrency, &current.Version, &current.UpdatedAt)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return current, err
	}
	if current.Version != version {
		return current, &QuestionAnswerScheduleConflictError{Key: ErrorSettingsConflict, Current: current}
	}
	if missing {
		err = tx.QueryRow(ctx, `INSERT INTO connection_health_question_answer_runtime_settings(singleton_key,question_answer_concurrency,version) VALUES('global',$1,1) RETURNING question_answer_concurrency,version,updated_at`, limit).Scan(&current.QuestionAnswerConcurrency, &current.Version, &current.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `UPDATE connection_health_question_answer_runtime_settings SET question_answer_concurrency=$1,version=version+1,updated_at=clock_timestamp() WHERE singleton_key='global' RETURNING question_answer_concurrency,version,updated_at`, limit).Scan(&current.QuestionAnswerConcurrency, &current.Version, &current.UpdatedAt)
	}
	if err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}

const scheduleLimitsColumns = `max_enabled_question_answer_schedules,max_schedule_targets,max_schedule_requests_per_execution,daily_scheduled_request_limit,max_active_schedule_executions,max_queued_scheduled_requests,schedule_execution_timeout_minutes,schedule_late_grace_minutes,question_answer_schedule_limits_version,updated_at`

func getQuestionAnswerScheduleLimits(ctx context.Context, q scheduleQueryer, user, workspace string) (QuestionAnswerScheduleLimits, error) {
	s := defaultQuestionAnswerScheduleLimits()
	err := q.QueryRow(ctx, `SELECT `+scheduleLimitsColumns+` FROM connection_health_workspace_settings WHERE user_id=$1 AND admin_account_id=$2`, user, workspace).Scan(&s.MaxEnabledQuestionAnswerSchedules, &s.MaxScheduleTargets, &s.MaxScheduleRequestsPerExecution, &s.DailyScheduledRequestLimit, &s.MaxActiveScheduleExecutions, &s.MaxQueuedScheduledRequests, &s.ScheduleExecutionTimeoutMinutes, &s.ScheduleLateGraceMinutes, &s.Version, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return s, err
	}
	err = q.QueryRow(ctx, `SELECT COALESCE(sum(reserved_request_count),0) FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND (scheduled_for AT TIME ZONE 'Asia/Singapore')::date=(clock_timestamp() AT TIME ZONE 'Asia/Singapore')::date`, user, workspace).Scan(&s.TodayReservedRequests)
	return s, err
}
func (r *Repository) GetQuestionAnswerScheduleLimits(ctx context.Context, user, workspace string) (QuestionAnswerScheduleLimits, error) {
	return getQuestionAnswerScheduleLimits(ctx, r.db, user, workspace)
}

func (r *Repository) SaveQuestionAnswerScheduleLimits(ctx context.Context, user, workspace string, input QuestionAnswerScheduleLimits, version int64, clock func() time.Time) (QuestionAnswerScheduleLimits, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return input, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO connection_health_workspace_settings(user_id,admin_account_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, user, workspace); err != nil {
		return input, err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM connection_health_workspace_settings WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, user, workspace); err != nil {
		return input, err
	}
	current, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return current, err
	}
	if current.Version != version {
		return current, &QuestionAnswerScheduleConflictError{Key: ErrorSettingsConflict, Current: current}
	}
	plans, err := queryQuestionAnswerSchedules(ctx, tx, ` WHERE user_id=$1 AND admin_account_id=$2 AND deleted_at IS NULL ORDER BY id FOR UPDATE`, user, workspace)
	if err != nil {
		return input, err
	}
	// All affected plan locks are held before computing any new cursor.
	now := clock().UTC()
	_, err = tx.Exec(ctx, `UPDATE connection_health_workspace_settings SET max_enabled_question_answer_schedules=$3,max_schedule_targets=$4,max_schedule_requests_per_execution=$5,daily_scheduled_request_limit=$6,max_active_schedule_executions=$7,max_queued_scheduled_requests=$8,schedule_execution_timeout_minutes=$9,schedule_late_grace_minutes=$10,question_answer_schedule_limits_version=question_answer_schedule_limits_version+1,updated_at=$11 WHERE user_id=$1 AND admin_account_id=$2`, user, workspace, input.MaxEnabledQuestionAnswerSchedules, input.MaxScheduleTargets, input.MaxScheduleRequestsPerExecution, input.DailyScheduledRequestLimit, input.MaxActiveScheduleExecutions, input.MaxQueuedScheduledRequests, input.ScheduleExecutionTimeoutMinutes, input.ScheduleLateGraceMinutes, now)
	if err != nil {
		return input, err
	}
	rebuiltCursors := []*QuestionAnswerSchedule{}
	for i := range plans {
		plan := &plans[i]
		// Local settings cannot prove that an inventory/question failure has
		// recovered. Keep that evidence until explicit validation reads it.
		if plan.BlockedReason != "" && !questionAnswerScheduleLocalLimitReason(plan.BlockedReason) {
			continue
		}
		reason := questionAnswerScheduleHardLimitReason(plan.QuestionAnswerScheduleConfig, input)
		if reason == "" && !questionAnswerScheduleLocalLimitReason(plan.BlockedReason) {
			continue
		}
		now = clock().UTC()
		var next *time.Time
		if reason == "" && plan.Enabled {
			at, e := nextQuestionAnswerScheduleSlot(plan.QuestionAnswerScheduleConfig, now, true)
			if e != nil {
				return input, e
			}
			next = &at
		}
		if plan.BlockedReason == reason && equalScheduleTime(plan.NextRunAt, next) {
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason=$4,next_run_at=$5,version=version+1,updated_at=$6 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, plan.ID, reason, next, now); err != nil {
			return input, err
		}
		if next != nil {
			plan.BlockedReason = reason
			plan.NextRunAt = next
			plan.UpdatedAt = now
			plan.Version++
			rebuiltCursors = append(rebuiltCursors, plan)
		}
	}
	current, err = getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return current, err
	}
	if err = finalizeQuestionAnswerScheduleCursors(ctx, tx, rebuiltCursors, clock); err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}

func equalScheduleTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

const scheduleColumns = `id,user_id,admin_account_id,name,target_mode,selected_group_ids,selected_account_target_ids,target_selection_snapshot,model_names,question_ids,reasoning_effort,repeat_count,to_char(peak_start,'HH24:MI'),to_char(peak_end,'HH24:MI'),peak_interval_minutes,off_peak_interval_minutes,enabled,blocked_reason,next_run_at,version,created_by,created_at,updated_at,deleted_at`

func scanQuestionAnswerSchedule(row rowScanner) (*QuestionAnswerSchedule, error) {
	var s QuestionAnswerSchedule
	var selection []byte
	err := row.Scan(&s.ID, &s.UserID, &s.AdminAccountID, &s.Name, &s.TargetMode, &s.SelectedGroupIDs, &s.SelectedAccountTargetIDs, &selection, &s.Models, &s.QuestionIDs, &s.ReasoningEffort, &s.RepeatCount, &s.PeakStart, &s.PeakEnd, &s.PeakIntervalMinutes, &s.OffPeakIntervalMinutes, &s.Enabled, &s.BlockedReason, &s.NextRunAt, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.DeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(selection, &s.TargetSelectionSnapshot); err != nil {
		return nil, err
	}
	s.EstimatedTargetCount = s.TargetSelectionSnapshot.EstimatedTargetCount
	s.EstimatedRequestsPerTarget = s.TargetSelectionSnapshot.EstimatedRequestsPerTarget
	s.EstimatedRequestsPerExecution = s.TargetSelectionSnapshot.EstimatedRequestsPerExecution
	s.EstimatedRequestsPerDay = s.TargetSelectionSnapshot.EstimatedRequestsPerDay
	s.PreviewedAt = s.TargetSelectionSnapshot.PreviewedAt
	return &s, nil
}
func queryQuestionAnswerSchedules(ctx context.Context, q scheduleQueryer, suffix string, args ...any) ([]QuestionAnswerSchedule, error) {
	rows, err := q.Query(ctx, `SELECT `+scheduleColumns+` FROM connection_health_question_answer_schedules`+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QuestionAnswerSchedule{}
	for rows.Next() {
		s, e := scanQuestionAnswerSchedule(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, *s)
	}
	return items, rows.Err()
}
func getQuestionAnswerScheduleTx(ctx context.Context, q scheduleQueryer, user, workspace, id string, lock bool) (*QuestionAnswerSchedule, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanQuestionAnswerSchedule(q.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM connection_health_question_answer_schedules WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`+suffix, user, workspace, id))
}
func (r *Repository) GetQuestionAnswerSchedule(ctx context.Context, user, workspace, id string) (*QuestionAnswerSchedule, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	s, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, false)
	if err != nil || s == nil {
		return s, err
	}
	if err = r.attachQuestionAnswerScheduleLastExecution(ctx, tx, s); err != nil {
		return nil, err
	}
	return s, tx.Commit(ctx)
}

func (r *Repository) ListQuestionAnswerSchedules(ctx context.Context, user, workspace, status string, page, pageSize int) (QuestionAnswerSchedulePage, error) {
	result := QuestionAnswerSchedulePage{Items: []QuestionAnswerSchedule{}, Page: page, PageSize: pageSize, Status: status}
	if page < 1 || pageSize < 1 || pageSize > 100 || (status != "active" && status != "deleted") {
		return result, requestError(ErrorRequest)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	clause := ` WHERE user_id=$1 AND admin_account_id=$2 AND (($3='deleted' AND deleted_at IS NOT NULL) OR ($3='active' AND deleted_at IS NULL))`
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedules`+clause, user, workspace, status).Scan(&result.Total); err != nil {
		return result, err
	}
	if result.Total > 0 {
		result.TotalPages = 1 + (result.Total-1)/pageSize
	}
	if page > result.TotalPages {
		return result, tx.Commit(ctx)
	}
	items, err := queryQuestionAnswerSchedules(ctx, tx, clause+` ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5`, user, workspace, status, pageSize, int64(page-1)*int64(pageSize))
	if err != nil {
		return result, err
	}
	for i := range items {
		if err = r.attachQuestionAnswerScheduleLastExecution(ctx, tx, &items[i]); err != nil {
			return result, err
		}
	}
	result.Items = items
	return result, tx.Commit(ctx)
}

func scheduleNextAt(config QuestionAnswerScheduleConfig, enabled bool, now time.Time) (*time.Time, error) {
	if !enabled {
		return nil, nil
	}
	at, err := nextQuestionAnswerScheduleSlot(config, now, true)
	return &at, err
}

// Only cursors rebuilt by the current mutation enter this helper. An existing
// healthy due cursor remains owned by the due-slot transaction. The initial
// plan write (including INSERT's parent FK wait), its readback and all other
// mutation work have finished before this final check, with every plan lock
// already held. Repairs keep the mutation's single version increment.
func finalizeQuestionAnswerScheduleCursors(ctx context.Context, tx pgx.Tx, plans []*QuestionAnswerSchedule, clock func() time.Time) error {
	if len(plans) == 0 {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := clock().UTC()
		repaired := false
		for _, plan := range plans {
			if plan == nil || !plan.Enabled || plan.DeletedAt != nil || plan.BlockedReason != "" || plan.NextRunAt == nil || plan.NextRunAt.After(now) {
				continue
			}
			next, err := nextQuestionAnswerScheduleSlot(plan.QuestionAnswerScheduleConfig, now, true)
			if err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `UPDATE connection_health_question_answer_schedules SET next_run_at=$4,updated_at=$5 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3 RETURNING next_run_at,updated_at`, plan.UserID, plan.AdminAccountID, plan.ID, next, now).Scan(&plan.NextRunAt, &plan.UpdatedAt); err != nil {
				return err
			}
			repaired = true
		}
		if !repaired {
			return nil
		}
		// A later row's repair can consume an earlier row's remaining time.
		// Recheck the complete rebuilt cohort after all writes/readbacks, then
		// return directly to COMMIT with every cursor strictly in the future.
	}
}

func (r *Repository) CreateQuestionAnswerSchedule(ctx context.Context, user, workspace string, config QuestionAnswerScheduleConfig, enabled bool, selection QuestionAnswerScheduleSelectionSnapshot, clock func() time.Time) (*QuestionAnswerSchedule, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	limits, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return nil, err
	}
	if questionAnswerScheduleHardLimitReason(config, limits) != "" {
		return nil, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid}
	}
	if enabled {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedules WHERE user_id=$1 AND admin_account_id=$2 AND enabled AND deleted_at IS NULL`, user, workspace).Scan(&n); err != nil {
			return nil, err
		}
		if n >= limits.MaxEnabledQuestionAnswerSchedules {
			return nil, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: limits}
		}
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(selection)
	if err != nil {
		return nil, err
	}
	now := clock().UTC()
	next, err := scheduleNextAt(config, enabled, now)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO connection_health_question_answer_schedules(id,user_id,admin_account_id,name,target_mode,selected_group_ids,selected_account_target_ids,target_selection_snapshot,model_names,question_ids,reasoning_effort,repeat_count,peak_start,peak_end,peak_interval_minutes,off_peak_interval_minutes,enabled,next_run_at,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::time,$14::time,$15,$16,$17,$18,$2,$19,$19)`, id, user, workspace, config.Name, config.TargetMode, config.SelectedGroupIDs, config.SelectedAccountTargetIDs, data, config.Models, config.QuestionIDs, config.ReasoningEffort, config.RepeatCount, config.PeakStart, config.PeakEnd, config.PeakIntervalMinutes, config.OffPeakIntervalMinutes, enabled, next, now)
	if err != nil {
		return nil, err
	}
	s, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return nil, err
	}
	if err = finalizeQuestionAnswerScheduleCursors(ctx, tx, []*QuestionAnswerSchedule{s}, clock); err != nil {
		return nil, err
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) UpdateQuestionAnswerSchedule(ctx context.Context, user, workspace, id string, config QuestionAnswerScheduleConfig, selection QuestionAnswerScheduleSelectionSnapshot, version int64, clock func() time.Time) (*QuestionAnswerSchedule, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	s, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, true)
	if err != nil || s == nil {
		return s, err
	}
	if s.Version != version {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleVersionConflict, Current: s}
	}
	if s.DeletedAt != nil {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleDeleted, Current: s}
	}
	limits, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return nil, err
	}
	if questionAnswerScheduleHardLimitReason(config, limits) != "" {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: s}
	}
	data, err := json.Marshal(selection)
	if err != nil {
		return nil, err
	}
	now := clock().UTC()
	next, err := scheduleNextAt(config, s.Enabled, now)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET name=$4,target_mode=$5,selected_group_ids=$6,selected_account_target_ids=$7,target_selection_snapshot=$8,model_names=$9,question_ids=$10,reasoning_effort=$11,repeat_count=$12,peak_start=$13::time,peak_end=$14::time,peak_interval_minutes=$15,off_peak_interval_minutes=$16,blocked_reason='',next_run_at=$17,version=version+1,updated_at=$18 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, config.Name, config.TargetMode, config.SelectedGroupIDs, config.SelectedAccountTargetIDs, data, config.Models, config.QuestionIDs, config.ReasoningEffort, config.RepeatCount, config.PeakStart, config.PeakEnd, config.PeakIntervalMinutes, config.OffPeakIntervalMinutes, next, now)
	if err != nil {
		return nil, err
	}
	s, err = getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return nil, err
	}
	if err = finalizeQuestionAnswerScheduleCursors(ctx, tx, []*QuestionAnswerSchedule{s}, clock); err != nil {
		return nil, err
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) SetQuestionAnswerScheduleState(ctx context.Context, user, workspace, id, action, blockedReason string, version int64, clock func() time.Time) (*QuestionAnswerSchedule, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	s, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, true)
	if err != nil || s == nil {
		return s, err
	}
	if s.Version != version {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleVersionConflict, Current: s}
	}
	if s.DeletedAt != nil {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleDeleted, Current: s}
	}
	if (action == "enable" || action == "revalidate") && (blockedReason == "" || questionAnswerScheduleLocalLimitReason(blockedReason)) {
		limits, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
		if err != nil {
			return nil, err
		}
		blockedReason = questionAnswerScheduleHardLimitReason(s.QuestionAnswerScheduleConfig, limits)
	}
	now := clock().UTC()
	enabled, deleted := s.Enabled, s.DeletedAt
	reason := s.BlockedReason
	switch action {
	case "disable":
		enabled = false
	case "delete":
		deleted = &now
	case "enable", "revalidate":
		reason = blockedReason
	case "":
		return nil, requestError(ErrorRequest)
	default:
		return nil, requestError(ErrorRequest)
	}
	if action == "enable" && reason == "" {
		enabled = true
	}
	if action == "enable" && enabled && !s.Enabled {
		limits, e := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
		if e != nil {
			return nil, e
		}
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedules WHERE user_id=$1 AND admin_account_id=$2 AND enabled AND deleted_at IS NULL`, user, workspace).Scan(&n); e != nil {
			return nil, e
		}
		if n >= limits.MaxEnabledQuestionAnswerSchedules {
			return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: s}
		}
	}
	now = clock().UTC()
	var next *time.Time
	rebuiltCursor := false
	if enabled && deleted == nil && reason == "" {
		if enabled == s.Enabled && reason == s.BlockedReason {
			// Rechecking an unchanged healthy schedule does not abandon its
			// already accepted cursor or invalidate earlier executions.
			next = s.NextRunAt
		} else {
			next, err = scheduleNextAt(s.QuestionAnswerScheduleConfig, true, now)
			if err != nil {
				return nil, err
			}
			rebuiltCursor = true
		}
	}
	changed := enabled != s.Enabled || reason != s.BlockedReason || !equalScheduleTime(deleted, s.DeletedAt) || !equalScheduleTime(next, s.NextRunAt)
	if changed {
		_, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET enabled=$4,blocked_reason=$5,next_run_at=$6,deleted_at=$7,version=version+1,updated_at=$8 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, enabled, reason, next, deleted, now)
		if err != nil {
			return nil, err
		}
	}
	s, err = getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return nil, err
	}
	if rebuiltCursor {
		if err = finalizeQuestionAnswerScheduleCursors(ctx, tx, []*QuestionAnswerSchedule{s}, clock); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	if blockedReason != "" && (action == "enable" || action == "revalidate") {
		return s, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: s}
	}
	return s, nil
}

func (r *Repository) ListDueQuestionAnswerSchedules(ctx context.Context, now time.Time) ([]QuestionAnswerSchedule, error) {
	return queryQuestionAnswerSchedules(ctx, r.db, ` WHERE enabled AND deleted_at IS NULL AND blocked_reason='' AND next_run_at<=$1 ORDER BY next_run_at,created_at,id`, now)
}

func sortQuestionAnswerScheduleTargets(targets []QuestionAnswerScheduleExecutionTarget) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].TargetID < targets[j].TargetID })
}

package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const scheduleExecutionColumns = `id,user_id,admin_account_id,schedule_id,trigger,request_id,scheduled_for,status,status_reason,config_snapshot,planned_target_count,reserved_request_count,COALESCE(missed_count,0),missed_from,missed_through,cancel_requested_at,termination_cause,version,created_at,started_at,completed_at,updated_at`

func scanQuestionAnswerScheduleExecution(row rowScanner) (*QuestionAnswerScheduleExecution, error) {
	var e QuestionAnswerScheduleExecution
	var config []byte
	err := row.Scan(&e.ID, &e.UserID, &e.AdminAccountID, &e.ScheduleID, &e.Trigger, &e.RequestID, &e.ScheduledFor, &e.Status, &e.StatusReason, &config, &e.PlannedTargetCount, &e.ReservedRequestCount, &e.MissedCount, &e.MissedFrom, &e.MissedThrough, &e.CancelRequestedAt, &e.TerminationCause, &e.Version, &e.CreatedAt, &e.StartedAt, &e.CompletedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(config, &e.ConfigSnapshot); err != nil {
		return nil, err
	}
	e.Active = questionAnswerScheduleExecutionActive(e.Status)
	e.Targets = []QuestionAnswerScheduleExecutionTarget{}
	e.Stats = aggregateQuestionAnswerStats(nil)
	return &e, nil
}
func getQuestionAnswerScheduleExecutionTx(ctx context.Context, q scheduleQueryer, user, workspace, id string, lock bool) (*QuestionAnswerScheduleExecution, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanQuestionAnswerScheduleExecution(q.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`+suffix, user, workspace, id))
}
func queryQuestionAnswerScheduleExecutions(ctx context.Context, q scheduleQueryer, suffix string, args ...any) ([]QuestionAnswerScheduleExecution, error) {
	rows, err := q.Query(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions`+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QuestionAnswerScheduleExecution{}
	for rows.Next() {
		e, x := scanQuestionAnswerScheduleExecution(rows)
		if x != nil {
			return nil, x
		}
		items = append(items, *e)
	}
	return items, rows.Err()
}
func scheduleRequested(plan QuestionAnswerSchedule, limits QuestionAnswerScheduleLimits) QuestionAnswerScheduleSnapshot {
	return QuestionAnswerScheduleSnapshot{Requested: QuestionAnswerScheduleRequested{Schedule: plan.QuestionAnswerScheduleConfig, ScheduleVersion: plan.Version, Enabled: plan.Enabled, Selection: plan.TargetSelectionSnapshot, Limits: limits, Timezone: "Asia/Singapore"}}
}
func insertQuestionAnswerScheduleExecution(ctx context.Context, tx pgx.Tx, plan QuestionAnswerSchedule, limits QuestionAnswerScheduleLimits, trigger, requestID, status, reason string, slot, now time.Time, missed []time.Time) (*QuestionAnswerScheduleExecution, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(scheduleRequested(plan, limits))
	if err != nil {
		return nil, err
	}
	var missedCount *int
	var from, through, completed *time.Time
	if len(missed) > 0 {
		n := len(missed)
		missedCount = &n
		from = &missed[0]
		through = &missed[len(missed)-1]
	}
	if !questionAnswerScheduleExecutionActive(status) {
		completed = &now
	}
	_, err = tx.Exec(ctx, `INSERT INTO connection_health_question_answer_schedule_executions(id,user_id,admin_account_id,schedule_id,trigger,request_id,scheduled_for,status,status_reason,config_snapshot,missed_count,missed_from,missed_through,created_at,updated_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,$15) ON CONFLICT DO NOTHING`, id, plan.UserID, plan.AdminAccountID, plan.ID, trigger, requestID, slot, status, reason, data, missedCount, from, through, now, completed)
	if err != nil {
		return nil, err
	}
	if trigger == "scheduled" {
		return scanQuestionAnswerScheduleExecution(tx.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 AND trigger='scheduled' AND scheduled_for=$4`, plan.UserID, plan.AdminAccountID, plan.ID, slot))
	}
	return scanQuestionAnswerScheduleExecution(tx.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 AND trigger='run_now' AND request_id=$4`, plan.UserID, plan.AdminAccountID, plan.ID, requestID))
}

// All due entry points use this one transaction. now is evaluated only after
// the plan lock; a failed or uncertain commit is retried from the SQL cursor.
func (r *Repository) ProcessDueQuestionAnswerSchedule(ctx context.Context, user, workspace, id string, clock func() time.Time) ([]QuestionAnswerScheduleExecution, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	plan, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, true)
	if err != nil {
		return nil, err
	}
	items := []QuestionAnswerScheduleExecution{}
	if plan == nil || !plan.Enabled || plan.DeletedAt != nil || plan.BlockedReason != "" || plan.NextRunAt == nil {
		return items, nil
	}
	limits, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return nil, err
	}
	now := clock().UTC()
	if plan.NextRunAt.After(now) {
		return items, nil
	}
	slots, err := questionAnswerScheduleSlotsBetween(plan.QuestionAnswerScheduleConfig, *plan.NextRunAt, now)
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 {
		return items, nil
	}
	keep := -1
	latest := slots[len(slots)-1]
	if !now.After(latest.Add(time.Duration(limits.ScheduleLateGraceMinutes) * time.Minute)) {
		keep = len(slots) - 1
	}
	missed := slots
	if keep >= 0 {
		missed = slots[:keep]
	}
	if len(missed) > 0 {
		e, x := insertQuestionAnswerScheduleExecution(ctx, tx, *plan, limits, "scheduled", "", "skipped", "misfire_no_catchup", missed[0], now, missed)
		if x != nil {
			return nil, x
		}
		items = append(items, *e)
	}
	if keep >= 0 {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_question_answer_schedule_executions WHERE schedule_id=$1 AND status IN ('pending','active'))`, id).Scan(&active); err != nil {
			return nil, err
		}
		status, reason := "pending", ""
		if active {
			status, reason = "skipped", "previous_execution_active"
		}
		e, x := insertQuestionAnswerScheduleExecution(ctx, tx, *plan, limits, "scheduled", "", status, reason, latest, now, nil)
		if x != nil {
			return nil, x
		}
		items = append(items, *e)
	}
	next, err := nextQuestionAnswerScheduleSlot(plan.QuestionAnswerScheduleConfig, now, true)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET next_run_at=$4,updated_at=$5 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, next, now); err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}

func (r *Repository) CreateRunNowQuestionAnswerScheduleExecution(ctx context.Context, user, workspace, id, requestID string, clock func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	plan, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, id, true)
	if err != nil || plan == nil {
		return nil, err
	}
	prior, err := scanQuestionAnswerScheduleExecution(tx.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 AND trigger='run_now' AND request_id=$4`, user, workspace, id, requestID))
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return prior, nil
	}
	if plan.DeletedAt != nil {
		return nil, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleDeleted, Current: plan}
	}
	if plan.BlockedReason != "" {
		return nil, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleInvalid, Current: plan}
	}
	active, err := scanQuestionAnswerScheduleExecution(tx.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 AND status IN ('pending','active')`, user, workspace, id))
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, &QuestionAnswerScheduleConflictError{Key: ErrorQuestionAnswerScheduleActive, Current: active, ActiveExecutionID: active.ID}
	}
	limits, err := getQuestionAnswerScheduleLimits(ctx, tx, user, workspace)
	if err != nil {
		return nil, err
	}
	now := clock().UTC()
	e, err := insertQuestionAnswerScheduleExecution(ctx, tx, *plan, limits, "run_now", requestID, "pending", "", now, now, nil)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

func (r *Repository) ClaimNextQuestionAnswerScheduleExecution(ctx context.Context, user, workspace string, clock func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for {
		e, x := scanQuestionAnswerScheduleExecution(tx.QueryRow(ctx, `SELECT `+scheduleExecutionColumns+` FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND status='pending' ORDER BY scheduled_for,created_at,id LIMIT 1 FOR UPDATE`, user, workspace))
		if x != nil {
			return nil, x
		}
		if e == nil {
			if x = tx.Commit(ctx); x != nil {
				return nil, x
			}
			return nil, nil
		}
		// Each FIFO candidate may have waited for its own execution lock.
		now := clock().UTC()
		reason := ""
		if !now.Before(e.deadline()) {
			reason = "execution_timeout_before_start"
		} else if now.After(e.startWindowEnd()) {
			reason = "start_window_expired"
		}
		if reason != "" {
			cause := ""
			if reason == "execution_timeout_before_start" {
				cause = "execution_timeout"
			}
			if _, x = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET status='skipped',status_reason=$4,termination_cause=$5,completed_at=$6,updated_at=$6,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, e.ID, reason, cause, now); x != nil {
				return nil, x
			}
			continue
		}
		var count int
		if x = tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND status='active'`, user, workspace).Scan(&count); x != nil {
			return nil, x
		}
		now = clock().UTC()
		if !now.Before(e.deadline()) || now.After(e.startWindowEnd()) {
			// Revisit this still-pending locked candidate to persist the same
			// expiry classification before testing capacity or starting work.
			continue
		}
		if count >= e.ConfigSnapshot.Requested.Limits.MaxActiveScheduleExecutions {
			if x = tx.Commit(ctx); x != nil {
				return nil, x
			}
			return nil, nil
		}
		if _, x = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET status='active',started_at=$4,updated_at=$4,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, e.ID, now); x != nil {
			return nil, x
		}
		e, x = getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, e.ID, false)
		if x != nil {
			return nil, x
		}
		return e, tx.Commit(ctx)
	}
}
func (r *Repository) ListNonterminalQuestionAnswerScheduleExecutions(ctx context.Context) ([]QuestionAnswerScheduleExecution, error) {
	return queryQuestionAnswerScheduleExecutions(ctx, r.db, ` WHERE status IN ('pending','active') ORDER BY scheduled_for,created_at,id`)
}

func (r *Repository) attachQuestionAnswerScheduleLastExecution(ctx context.Context, q scheduleQueryer, s *QuestionAnswerSchedule) error {
	if s == nil {
		return nil
	}
	s.LastExecution = nil
	s.LastActualTargetCount = 0
	items, err := queryQuestionAnswerScheduleExecutions(ctx, q, ` WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 ORDER BY created_at DESC,id DESC LIMIT 1`, s.UserID, s.AdminAccountID, s.ID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	if err = r.attachScheduleExecutionFacts(ctx, q, s.UserID, s.AdminAccountID, items); err != nil {
		return err
	}
	s.LastExecution = &items[0]
	s.LastActualTargetCount = items[0].PlannedTargetCount
	return nil
}
func (r *Repository) ListQuestionAnswerScheduleExecutions(ctx context.Context, user, workspace, scheduleID string, page, pageSize int) (QuestionAnswerScheduleExecutionPage, error) {
	result := QuestionAnswerScheduleExecutionPage{Items: []QuestionAnswerScheduleExecution{}, Page: page, PageSize: pageSize}
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return result, requestError(ErrorRequest)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	plan, err := getQuestionAnswerScheduleTx(ctx, tx, user, workspace, scheduleID, false)
	if err != nil {
		return result, err
	}
	if plan == nil {
		return result, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3`, user, workspace, scheduleID).Scan(&result.Total); err != nil {
		return result, err
	}
	if result.Total > 0 {
		result.TotalPages = 1 + (result.Total-1)/pageSize
	}
	if page > result.TotalPages {
		return result, tx.Commit(ctx)
	}
	items, err := queryQuestionAnswerScheduleExecutions(ctx, tx, ` WHERE user_id=$1 AND admin_account_id=$2 AND schedule_id=$3 ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5`, user, workspace, scheduleID, pageSize, int64(page-1)*int64(pageSize))
	if err != nil {
		return result, err
	}
	if err = r.attachScheduleExecutionFacts(ctx, tx, user, workspace, items); err != nil {
		return result, err
	}
	result.Items = items
	return result, tx.Commit(ctx)
}
func (r *Repository) GetQuestionAnswerScheduleExecution(ctx context.Context, user, workspace, id string) (*QuestionAnswerScheduleExecution, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, false)
	if err != nil || e == nil {
		return e, err
	}
	items := []QuestionAnswerScheduleExecution{*e}
	if err = r.attachScheduleExecutionFacts(ctx, tx, user, workspace, items); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &items[0], nil
}

const scheduleTargetColumns = `t.id,t.execution_id,t.target_id,t.account_snapshot,t.matched_groups_snapshot,t.test_configuration_snapshot,t.requested_models,t.available_models,t.unavailable_models,t.planned_request_count,t.status,t.status_reason,t.created_at,t.started_at,t.completed_at,t.updated_at`

func scanQuestionAnswerScheduleTarget(row rowScanner) (QuestionAnswerScheduleExecutionTarget, error) {
	var t QuestionAnswerScheduleExecutionTarget
	var account, groups, config, models []byte
	err := row.Scan(&t.ID, &t.ExecutionID, &t.TargetID, &account, &groups, &config, &t.RequestedModels, &t.AvailableModels, &models, &t.PlannedRequestCount, &t.Status, &t.StatusReason, &t.CreatedAt, &t.StartedAt, &t.CompletedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	for _, part := range []struct {
		b []byte
		v any
	}{{account, &t.AccountSnapshot}, {groups, &t.MatchedGroupsSnapshot}, {config, &t.TestConfigurationSnapshot}, {models, &t.UnavailableModels}} {
		if err = json.Unmarshal(part.b, part.v); err != nil {
			return t, err
		}
	}
	t.Stats = aggregateQuestionAnswerStats(nil)
	return t, nil
}
func queryScheduleTargets(ctx context.Context, q scheduleQueryer, user, workspace string, ids []string) ([]QuestionAnswerScheduleExecutionTarget, error) {
	rows, err := q.Query(ctx, `SELECT `+scheduleTargetColumns+` FROM connection_health_question_answer_schedule_execution_targets t JOIN connection_health_question_answer_schedule_executions e ON e.id=t.execution_id WHERE e.user_id=$1 AND e.admin_account_id=$2 AND e.id=ANY($3::text[]) ORDER BY t.target_id,t.id`, user, workspace, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QuestionAnswerScheduleExecutionTarget{}
	for rows.Next() {
		t, x := scanQuestionAnswerScheduleTarget(rows)
		if x != nil {
			return nil, x
		}
		items = append(items, t)
	}
	return items, rows.Err()
}
func (r *Repository) ListQuestionAnswerScheduleExecutionTargets(ctx context.Context, user, workspace, id string) ([]QuestionAnswerScheduleExecutionTarget, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	targets, err := queryScheduleTargets(ctx, tx, user, workspace, []string{id})
	if err != nil {
		return nil, err
	}
	records, err := queryScheduledQuestionAnswerRecords(ctx, tx, user, workspace, []string{id})
	if err != nil {
		return nil, err
	}
	byBatch := map[string][]QuestionAnswerRecord{}
	for _, record := range records {
		byBatch[record.BatchID] = append(byBatch[record.BatchID], record)
	}
	for i := range targets {
		setScheduleTargetBatchFacts(&targets[i], byBatch[targets[i].ID])
	}
	return targets, tx.Commit(ctx)
}
func queryScheduledQuestionAnswerRecords(ctx context.Context, q scheduleQueryer, user, workspace string, executionIDs []string) ([]QuestionAnswerRecord, error) {
	selectSummary := questionAnswerRecordSummarySelect()
	rows, err := q.Query(ctx, selectSummary+` WHERE user_id=$1 AND EXISTS(SELECT 1 FROM connection_health_question_answer_schedule_execution_targets t JOIN connection_health_question_answer_schedule_executions e ON e.id=t.execution_id WHERE t.id=connection_health_question_answer_records.batch_id AND t.target_id=connection_health_question_answer_records.target_id AND e.user_id=$1 AND e.admin_account_id=$2 AND e.id=ANY($3::text[])) ORDER BY created_at,id`, user, workspace, executionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestionAnswerRecords(rows)
}
func setScheduleTargetBatchFacts(target *QuestionAnswerScheduleExecutionTarget, records []QuestionAnswerRecord) {
	if len(records) > 0 {
		target.BatchAvailable = true
		target.BatchID = target.ID
		target.Stats = aggregateQuestionAnswerStats(records)
	}
}
func (r *Repository) attachScheduleExecutionFacts(ctx context.Context, q scheduleQueryer, user, workspace string, items []QuestionAnswerScheduleExecution) error {
	ids := []string{}
	for _, e := range items {
		ids = append(ids, e.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	targets, err := queryScheduleTargets(ctx, q, user, workspace, ids)
	if err != nil {
		return err
	}
	records, err := queryScheduledQuestionAnswerRecords(ctx, q, user, workspace, ids)
	if err != nil {
		return err
	}
	byBatch := map[string][]QuestionAnswerRecord{}
	for _, record := range records {
		byBatch[record.BatchID] = append(byBatch[record.BatchID], record)
	}
	byExec := map[string][]QuestionAnswerScheduleExecutionTarget{}
	for i := range targets {
		setScheduleTargetBatchFacts(&targets[i], byBatch[targets[i].ID])
		byExec[targets[i].ExecutionID] = append(byExec[targets[i].ExecutionID], targets[i])
	}
	for i := range items {
		e := &items[i]
		e.BatchCreatedTargetCount = 0
		e.RequestRecordCount = 0
		e.Targets = byExec[e.ID]
		if e.Targets == nil {
			e.Targets = []QuestionAnswerScheduleExecutionTarget{}
		}
		all := []QuestionAnswerRecord{}
		for _, target := range e.Targets {
			if target.BatchAvailable {
				e.BatchCreatedTargetCount++
				all = append(all, byBatch[target.ID]...)
			}
		}
		e.RequestRecordCount = len(all)
		e.Stats = aggregateQuestionAnswerStats(all)
	}
	return nil
}

func (r *Repository) ListQuestionAnswerScheduleHandleParents(ctx context.Context, handles []QuestionAnswerScheduleHandleIdentity) (map[string]bool, error) {
	result := map[string]bool{}
	if len(handles) == 0 {
		return result, nil
	}
	data, err := json.Marshal(handles)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT h."ExecutionID",EXISTS(SELECT 1 FROM admin_accounts a JOIN connection_health_question_answer_schedules s ON s.admin_account_id=a.id JOIN connection_health_question_answer_schedule_executions e ON e.schedule_id=s.id WHERE a.user_id=h."UserID" AND a.id=h."AdminAccountID" AND s.user_id=h."UserID" AND s.id=h."ScheduleID" AND e.user_id=h."UserID" AND e.admin_account_id=h."AdminAccountID" AND e.id=h."ExecutionID") FROM jsonb_to_recordset($1::jsonb) AS h("UserID" text,"AdminAccountID" text,"ScheduleID" text,"ExecutionID" text)`, data)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var exists bool
		if err = rows.Scan(&id, &exists); err != nil {
			return nil, err
		}
		result[id] = exists
	}
	return result, rows.Err()
}

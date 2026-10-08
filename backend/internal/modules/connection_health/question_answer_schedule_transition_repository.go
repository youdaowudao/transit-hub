package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *Repository) ReadQuestionAnswerSchedulePreparationConfiguration(ctx context.Context, user, workspace string, questionIDs []string) ([]TestQuestion, []GroupTestConfig, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,name,body,keywords,enabled,is_default,created_at,updated_at FROM connection_health_test_questions WHERE user_id=$1 AND enabled AND id=ANY($2::text[]) ORDER BY id`, user, questionIDs)
	if err != nil {
		return nil, nil, err
	}
	questions := []TestQuestion{}
	for rows.Next() {
		q, err := scanTestQuestion(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		questions = append(questions, *q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(questions) != len(questionIDs) {
		return nil, nil, errQuestionAnswerUnavailable
	}
	configs, err := listGroupTestConfigurationsTx(ctx, tx, user, workspace)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return questions, configs, nil
}
func nonnilScheduleStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
func insertQuestionAnswerScheduleTargets(ctx context.Context, tx pgx.Tx, e QuestionAnswerScheduleExecution, targets []QuestionAnswerScheduleExecutionTarget, now time.Time) error {
	sortQuestionAnswerScheduleTargets(targets)
	seen := map[string]bool{}
	seenID := map[string]bool{}
	for _, target := range targets {
		if target.ID == "" || seen[target.TargetID] || seenID[target.ID] || questionAnswerScheduleOwnedTarget(e.UserID, e.AdminAccountID, target.TargetID) != nil || target.PlannedRequestCount < 0 {
			return requestError(ErrorQuestionAnswerStorage)
		}
		seen[target.TargetID] = true
		seenID[target.ID] = true
		account, err := json.Marshal(target.AccountSnapshot)
		if err != nil {
			return err
		}
		groups := target.MatchedGroupsSnapshot
		if groups == nil {
			groups = []QuestionAnswerScheduleGroupRef{}
		}
		groupJSON, err := json.Marshal(groups)
		if err != nil {
			return err
		}
		config, err := json.Marshal(target.TestConfigurationSnapshot)
		if err != nil {
			return err
		}
		unavailable := target.UnavailableModels
		if unavailable == nil {
			unavailable = []QuestionAnswerScheduleUnavailableModel{}
		}
		models, err := json.Marshal(unavailable)
		if err != nil {
			return err
		}
		var completed *time.Time
		if target.Status == "failed" || target.Status == "skipped" || target.Status == "cancelled" {
			completed = &now
		}
		if _, err = tx.Exec(ctx, `INSERT INTO connection_health_question_answer_schedule_execution_targets(id,execution_id,target_id,account_snapshot,matched_groups_snapshot,test_configuration_snapshot,requested_models,available_models,unavailable_models,planned_request_count,status,status_reason,created_at,updated_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$14)`, target.ID, e.ID, target.TargetID, account, groupJSON, config, nonnilScheduleStrings(target.RequestedModels), nonnilScheduleStrings(target.AvailableModels), models, target.PlannedRequestCount, target.Status, target.StatusReason, now, completed); err != nil {
			return err
		}
	}
	return nil
}
func scheduleQueueUsed(ctx context.Context, q scheduleQueryer, user, workspace string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(CASE
  WHEN t.status IN ('pending','starting') AND NOT EXISTS(SELECT 1 FROM connection_health_question_answer_records r WHERE r.user_id=e.user_id AND r.target_id=t.target_id AND r.batch_id=t.id) THEN t.planned_request_count
  ELSE (SELECT count(*) FROM connection_health_question_answer_records r WHERE r.user_id=e.user_id AND r.target_id=t.target_id AND r.batch_id=t.id AND r.status='pending') END),0)
 FROM connection_health_question_answer_schedule_execution_targets t JOIN connection_health_question_answer_schedule_executions e ON e.id=t.execution_id WHERE e.user_id=$1 AND e.admin_account_id=$2 AND e.status IN ('pending','active')`, user, workspace).Scan(&count)
	return count, err
}
func (r *Repository) FreezeQuestionAnswerScheduleExecution(ctx context.Context, user, workspace, id string, resolved QuestionAnswerScheduleResolved, targets []QuestionAnswerScheduleExecutionTarget, clock func() time.Time) (string, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, true)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	now := clock().UTC()
	if e.ConfigSnapshot.Resolved != nil {
		return "", nil
	}
	if e.Status != "active" || e.TerminationCause != "" {
		return "execution_terminated", nil
	}
	if !now.Before(e.deadline()) {
		return "execution_timeout", nil
	}
	limits := e.ConfigSnapshot.Requested.Limits
	reason := ""
	requests := 0
	if len(targets) > limits.MaxScheduleTargets {
		reason = "target_limit"
	}
	for _, t := range targets {
		if t.Status == "pending" || t.Status == "starting" {
			if t.PlannedRequestCount > QuestionAnswerBatchRecordLimit {
				reason = "batch_request_limit"
			}
			requests += t.PlannedRequestCount
		} else if t.PlannedRequestCount != 0 {
			return "", requestError(ErrorQuestionAnswerStorage)
		}
	}
	if requests > limits.MaxScheduleRequestsPerExecution {
		reason = "execution_request_limit"
	}
	if reason != "" {
		return reason, nil
	}
	queued, err := scheduleQueueUsed(ctx, tx, user, workspace)
	if err != nil {
		return "", err
	}
	if queued+requests > limits.MaxQueuedScheduledRequests {
		return "queue_full", nil
	}
	var daily int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(reserved_request_count),0) FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND (scheduled_for AT TIME ZONE 'Asia/Singapore')::date=($3::timestamptz AT TIME ZONE 'Asia/Singapore')::date`, user, workspace, e.ScheduledFor).Scan(&daily); err != nil {
		return "", err
	}
	if daily+requests > limits.DailyScheduledRequestLimit {
		return "daily_limit", nil
	}
	// Queue and daily-budget reads may take time even after the locks were
	// acquired. Do not freeze or reserve an execution that expired meanwhile.
	now = clock().UTC()
	if !now.Before(e.deadline()) {
		return "execution_timeout", nil
	}
	if err = insertQuestionAnswerScheduleTargets(ctx, tx, *e, targets, now); err != nil {
		return "", err
	}
	e.ConfigSnapshot.Resolved = &resolved
	data, err := json.Marshal(e.ConfigSnapshot)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET config_snapshot=$4,planned_target_count=$5,reserved_request_count=$6,version=version+1,updated_at=$7 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, data, len(targets), requests, now)
	if err != nil {
		return "", err
	}
	if !clock().UTC().Before(e.deadline()) {
		return "execution_timeout", nil
	}
	return "", tx.Commit(ctx)
}

// Called only after the preparation worker exited and its unconverted target
// reservations were released. The final transaction rereads termination and
// deadline before atomically writing pure-skip evidence or a preparation error.
func (r *Repository) FinishQuestionAnswerSchedulePreparation(ctx context.Context, user, workspace, id string, outcome QuestionAnswerSchedulePreparationOutcome, clock func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var plan *QuestionAnswerSchedule
	if outcome.BlockedReason != "" {
		var planID string
		if err = tx.QueryRow(ctx, `SELECT schedule_id FROM connection_health_question_answer_schedule_executions WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id).Scan(&planID); errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		plan, err = getQuestionAnswerScheduleTx(ctx, tx, user, workspace, planID, true)
		if err != nil {
			return nil, err
		}
	}
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, true)
	if err != nil || e == nil {
		return e, err
	}
	if !questionAnswerScheduleExecutionActive(e.Status) {
		return e, nil
	}
	if e.TerminationCause != "" {
		return e, nil
	}
	// Conditional plan and execution locks are both held at this point.
	now := clock().UTC()
	finishTimeout := func() (*QuestionAnswerScheduleExecution, error) {
		if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET termination_cause='execution_timeout',version=version+1,updated_at=$4 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, now); err != nil {
			return nil, err
		}
		e, err = getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, false)
		if err != nil {
			return nil, err
		}
		return e, tx.Commit(ctx)
	}
	if !now.Before(e.deadline()) {
		return finishTimeout()
	}
	if e.ConfigSnapshot.Resolved != nil {
		return e, nil
	}
	if outcome.Status != "skipped" && outcome.Status != "failed" {
		return nil, requestError(ErrorQuestionAnswerStorage)
	}
	if outcome.Reason == "all_accounts_missing" {
		if outcome.Status != "skipped" || outcome.Resolved == nil || len(outcome.Targets) == 0 || len(outcome.Resolved.MissingTargetIDs) != len(outcome.Targets) {
			return nil, requestError(ErrorQuestionAnswerStorage)
		}
		for _, t := range outcome.Targets {
			if t.Status != "failed" || t.StatusReason != "account_not_found" || t.PlannedRequestCount != 0 || len(t.AvailableModels) > 0 || len(t.UnavailableModels) > 0 {
				return nil, requestError(ErrorQuestionAnswerStorage)
			}
		}
		e.ConfigSnapshot.Resolved = outcome.Resolved
		e.PlannedTargetCount = len(outcome.Targets)
	} else if outcome.Resolved != nil || len(outcome.Targets) > 0 {
		return nil, requestError(ErrorQuestionAnswerStorage)
	}
	data, err := json.Marshal(e.ConfigSnapshot)
	if err != nil {
		return nil, err
	}
	now = clock().UTC()
	if !now.Before(e.deadline()) {
		return finishTimeout()
	}
	// Keep the original W/plan/execution locks outside this savepoint. If
	// evidence writes cross the deadline, roll back only those writes before
	// accepting timeout, without publishing missing-account evidence or a
	// version-protected plan block that lost to timeout.
	if _, err = tx.Exec(ctx, `SAVEPOINT question_answer_schedule_preparation`); err != nil {
		return nil, err
	}
	if outcome.Reason == "all_accounts_missing" {
		if err = insertQuestionAnswerScheduleTargets(ctx, tx, *e, outcome.Targets, now); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET status=$4,status_reason=$5,config_snapshot=$6,planned_target_count=$7,reserved_request_count=0,completed_at=$8,updated_at=$8,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, outcome.Status, outcome.Reason, data, e.PlannedTargetCount, now); err != nil {
		return nil, err
	}
	if plan != nil && plan.DeletedAt == nil && plan.Version == e.ConfigSnapshot.Requested.ScheduleVersion && plan.BlockedReason != outcome.BlockedReason {
		if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedules SET blocked_reason=$4,next_run_at=NULL,updated_at=$5,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, plan.ID, outcome.BlockedReason, now); err != nil {
			return nil, err
		}
	}
	e, err = getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return nil, err
	}
	now = clock().UTC()
	if !now.Before(e.deadline()) {
		if _, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT question_answer_schedule_preparation`); err != nil {
			return nil, err
		}
		return finishTimeout()
	}
	return e, tx.Commit(ctx)
}

// A single SQL statement provides the cancellation/summarization business
// snapshot. It observes every target and its exact C1 batch together.
func scheduleExecutionBusinessFacts(ctx context.Context, tx pgx.Tx, e *QuestionAnswerScheduleExecution) ([]QuestionAnswerScheduleExecutionTarget, error) {
	var snapshot, facts []byte
	err := tx.QueryRow(ctx, `SELECT e.config_snapshot,COALESCE((SELECT jsonb_agg(jsonb_build_object(
  'id',t.id,'executionId',t.execution_id,'targetId',t.target_id,'status',t.status,'statusReason',t.status_reason,
  'unavailableModels',t.unavailable_models,'batchAvailable',r.submitted>0,'batchId',CASE WHEN r.submitted>0 THEN t.id ELSE '' END,
  'stats',jsonb_build_object('requests',jsonb_build_object('submitted',r.submitted,'inProgress',r.in_progress,'succeeded',r.succeeded,'failed',r.failed,'cancelled',r.cancelled),'reviews',jsonb_build_object('unreviewed',r.unreviewed,'correct',r.correct,'incorrect',r.incorrect))
 ) ORDER BY t.target_id,t.id) FROM connection_health_question_answer_schedule_execution_targets t LEFT JOIN LATERAL (
  SELECT count(*) AS submitted,count(*) FILTER(WHERE status IN ('pending','running')) AS in_progress,count(*) FILTER(WHERE status='succeeded') AS succeeded,count(*) FILTER(WHERE status='failed') AS failed,count(*) FILTER(WHERE status='cancelled') AS cancelled,
   count(*) FILTER(WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN ('correct','incorrect'))) AS unreviewed,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='correct') AS correct,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='incorrect') AS incorrect
  FROM connection_health_question_answer_records WHERE user_id=e.user_id AND target_id=t.target_id AND batch_id=t.id
 ) r ON true WHERE t.execution_id=e.id),'[]'::jsonb) FROM connection_health_question_answer_schedule_executions e WHERE e.user_id=$1 AND e.admin_account_id=$2 AND e.id=$3`, e.UserID, e.AdminAccountID, e.ID).Scan(&snapshot, &facts)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(snapshot, &e.ConfigSnapshot); err != nil {
		return nil, err
	}
	targets := []QuestionAnswerScheduleExecutionTarget{}
	if err = json.Unmarshal(facts, &targets); err != nil {
		return nil, err
	}
	return targets, nil
}
func updateScheduleExecutionReason(ctx context.Context, tx pgx.Tx, e *QuestionAnswerScheduleExecution, reason string, now time.Time) error {
	if e.StatusReason == reason {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET status_reason=$4,version=version+1,updated_at=$5 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, e.UserID, e.AdminAccountID, e.ID, reason, now); err != nil {
		return err
	}
	e.StatusReason = reason
	e.Version++
	e.UpdatedAt = now
	return nil
}

func lockQuestionAnswerScheduleTargets(ctx context.Context, tx pgx.Tx, executionID string) error {
	rows, err := tx.Query(ctx, `SELECT id FROM connection_health_question_answer_schedule_execution_targets WHERE execution_id=$1 ORDER BY target_id,id FOR UPDATE`, executionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *Repository) DecideQuestionAnswerScheduleTermination(ctx context.Context, user, workspace, id, cause string, version *int64, clock func() time.Time) (QuestionAnswerScheduleTerminationResult, error) {
	result := QuestionAnswerScheduleTerminationResult{}
	if cause != "user_cancel" && cause != "execution_timeout" {
		return result, requestError(ErrorRequest)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, true)
	if err != nil {
		return result, err
	}
	if e == nil {
		return result, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	if err = lockQuestionAnswerScheduleTargets(ctx, tx, id); err != nil {
		return result, err
	}
	now := clock().UTC()
	// Cancellation replies have the same complete C1 projection as GET. The
	// locked execution and its targets cannot disappear between these reads.
	finish := func(accepted, conflict, commit bool) (QuestionAnswerScheduleTerminationResult, error) {
		items := []QuestionAnswerScheduleExecution{*e}
		if err := r.attachScheduleExecutionFacts(ctx, tx, user, workspace, items); err != nil {
			return QuestionAnswerScheduleTerminationResult{}, err
		}
		current := QuestionAnswerScheduleTerminationResult{Execution: items[0], Accepted: accepted, Conflict: conflict}
		if commit {
			return current, tx.Commit(ctx)
		}
		return current, nil
	}
	if e.TerminationCause == "user_cancel" {
		return finish(false, false, false)
	}
	if e.TerminationCause != "" || !questionAnswerScheduleExecutionActive(e.Status) {
		return finish(false, true, false)
	}
	targets, err := scheduleExecutionBusinessFacts(ctx, tx, e)
	if err != nil {
		return result, err
	}
	now = clock().UTC()
	status, _ := summarizeQuestionAnswerScheduleExecution(*e, targets)
	if e.ConfigSnapshot.Resolved != nil && status != "active" {
		if err = updateScheduleExecutionReason(ctx, tx, e, "c1_finalization_pending", now); err != nil {
			return result, err
		}
		return finish(false, true, true)
	}
	if cause == "execution_timeout" && now.Before(e.deadline()) {
		return finish(false, false, false)
	}
	if cause == "user_cancel" && (version == nil || *version != e.Version) {
		return finish(false, true, false)
	}
	var cancel *time.Time
	if cause == "user_cancel" {
		cancel = &now
	}
	status = e.Status
	reason := e.StatusReason
	var completed *time.Time
	if e.Status == "pending" && e.ConfigSnapshot.Resolved == nil && len(targets) == 0 {
		completed = &now
		if cause == "user_cancel" {
			status, reason = "cancelled", "user_cancel"
		} else {
			status, reason = "skipped", "execution_timeout_before_start"
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET termination_cause=$4,cancel_requested_at=$5,status=$6,status_reason=$7,completed_at=$8,updated_at=$9,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, cause, cancel, status, reason, completed, now); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_execution_targets t SET status='cancelled',status_reason=$4,completed_at=$5,updated_at=$5 WHERE execution_id=$3 AND EXISTS(SELECT 1 FROM connection_health_question_answer_schedule_executions e WHERE e.id=t.execution_id AND e.user_id=$1 AND e.admin_account_id=$2) AND status IN ('pending','starting') AND NOT EXISTS(SELECT 1 FROM connection_health_question_answer_records r WHERE r.user_id=$1 AND r.target_id=t.target_id AND r.batch_id=t.id)`, user, workspace, id, cause, now); err != nil {
		return result, err
	}
	e, err = getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return result, err
	}
	return finish(true, false, true)
}

func (r *Repository) FinishQuestionAnswerScheduleExecution(ctx context.Context, user, workspace, id string, settled bool, reason string, clock func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, true)
	if err != nil || e == nil {
		return e, err
	}
	if !questionAnswerScheduleExecutionActive(e.Status) {
		return e, nil
	}
	targets, err := scheduleExecutionBusinessFacts(ctx, tx, e)
	if err != nil {
		return nil, err
	}
	status, resultReason := summarizeQuestionAnswerScheduleExecution(*e, targets)
	if status == "active" {
		return e, nil
	}
	now := clock().UTC()
	if !settled {
		if reason == "" {
			reason = "c1_finalization_pending"
		}
		if err = updateScheduleExecutionReason(ctx, tx, e, reason, now); err != nil {
			return nil, err
		}
		return e, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET status=$4,status_reason=$5,completed_at=$6,updated_at=$6,version=version+1 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, id, status, resultReason, now); err != nil {
		return nil, err
	}
	e, err = getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, id, false)
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

func (r *Repository) FailQuestionAnswerScheduleTarget(ctx context.Context, user, workspace, executionID, targetID, batchID, reason string, clock func() time.Time) error {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, executionID, true)
	if err != nil {
		return err
	}
	if e == nil || !questionAnswerScheduleExecutionActive(e.Status) {
		return nil
	}
	var state string
	if err = tx.QueryRow(ctx, `SELECT status FROM connection_health_question_answer_schedule_execution_targets WHERE execution_id=$1 AND target_id=$2 AND id=$3 FOR UPDATE`, executionID, targetID, batchID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if state != "pending" && state != "starting" {
		return nil
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=$2 AND batch_id=$3)`, user, targetID, batchID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	now := clock().UTC()
	status := "failed"
	if e.TerminationCause != "" {
		status = "cancelled"
		reason = e.TerminationCause
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_execution_targets SET status=$4,status_reason=$5,completed_at=$6,updated_at=$6 WHERE execution_id=$1 AND target_id=$2 AND id=$3`, executionID, targetID, batchID, status, reason, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET version=version+1,updated_at=$4 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, executionID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

package connection_health

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Preallocated C2 target ID and C1 records are changed in the same transaction.
// An existing exact batch is checked first, including owner and frozen matrix;
// it can never be mistaken for a different active batch or registered twice.
func (r *Repository) CreateScheduledQuestionAnswerBatch(ctx context.Context, user, workspace, executionID, targetID, batchID string) ([]QuestionAnswerRecord, bool, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	e, err := getQuestionAnswerScheduleExecutionTx(ctx, tx, user, workspace, executionID, true)
	if err != nil {
		return nil, false, err
	}
	if e == nil {
		return nil, false, requestError(ErrorQuestionAnswerScheduleNotFound)
	}
	target, err := scanQuestionAnswerScheduleTarget(tx.QueryRow(ctx, `SELECT `+scheduleTargetColumns+` FROM connection_health_question_answer_schedule_execution_targets t WHERE t.execution_id=$1 AND t.target_id=$2 AND t.id=$3 FOR UPDATE`, executionID, targetID, batchID))
	if err != nil {
		return nil, false, err
	}
	if questionAnswerScheduleOwnedTarget(user, workspace, targetID) != nil || e.ConfigSnapshot.Resolved == nil || target.TestConfigurationSnapshot.AdminAccountID != workspace || !target.TestConfigurationSnapshot.InventoryComplete || !validTestProtocol(target.TestConfigurationSnapshot.Protocol) {
		return nil, false, requestError(ErrorQuestionAnswerStorage)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, fmt.Sprintf("question-answer|%s|%s", user, targetID)); err != nil {
		return nil, false, err
	}
	var total, owned int
	if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE user_id=$2 AND target_id=$3) FROM connection_health_question_answer_records WHERE batch_id=$1`, batchID, user, targetID).Scan(&total, &owned); err != nil {
		return nil, false, err
	}
	if total > 0 {
		if total != owned {
			return nil, false, requestError(ErrorQuestionAnswerStorage)
		}
		rows, err := tx.Query(ctx, questionAnswerRecordSelect+` WHERE batch_id=$1 ORDER BY created_at,id`, batchID)
		if err != nil {
			return nil, false, err
		}
		records, err := scanQuestionAnswerRecords(rows)
		rows.Close()
		if err != nil {
			return nil, false, err
		}
		if !consistentScheduledQuestionAnswerRecords(*e, target, records) {
			return nil, false, requestError(ErrorQuestionAnswerStorage)
		}
		return records, false, nil
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, false, err
	}
	if e.Status != "active" || e.TerminationCause != "" || !now.Before(e.deadline()) || (target.Status != "pending" && target.Status != "starting") {
		return nil, false, requestError(ErrorQuestionAnswerServiceStopped)
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=$2 AND status IN ('pending','running'))`, user, targetID).Scan(&active); err != nil {
		return nil, false, err
	}
	if active {
		return nil, false, errQuestionAnswerActive
	}
	config := e.ConfigSnapshot.Requested.Schedule
	questions := e.ConfigSnapshot.Resolved.Questions
	if len(target.AvailableModels) == 0 || len(questions) != len(config.QuestionIDs) || config.RepeatCount < 1 || config.RepeatCount > 10 || target.PlannedRequestCount != len(target.AvailableModels)*len(questions)*config.RepeatCount || target.PlannedRequestCount > QuestionAnswerBatchRecordLimit {
		return nil, false, requestError(ErrorQuestionAnswerStorage)
	}
	questionSet := map[string]bool{}
	for _, id := range config.QuestionIDs {
		questionSet[id] = true
	}
	for _, q := range questions {
		if !questionSet[q.ID] {
			return nil, false, requestError(ErrorQuestionAnswerStorage)
		}
		delete(questionSet, q.ID)
	}
	if len(questionSet) != 0 {
		return nil, false, requestError(ErrorQuestionAnswerStorage)
	}
	effort, err := normalizeQuestionAnswerReasoningEffort(config.ReasoningEffort)
	if err != nil {
		return nil, false, requestError(ErrorQuestionAnswerStorage)
	}
	records := []QuestionAnswerRecord{}
	for _, model := range target.AvailableModels {
		for _, question := range questions {
			for index := 1; index <= config.RepeatCount; index++ {
				id, err := newID()
				if err != nil {
					return nil, false, err
				}
				protocol := target.TestConfigurationSnapshot.Protocol
				repeat := index
				record := QuestionAnswerRecord{ID: id, TargetID: targetID, BatchID: batchID, ModelName: model, RequestProtocol: &protocol, QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body, QuestionKeywordSnapshot: append([]string{}, question.Keywords...), ReasoningEffort: questionAnswerReasoningEffortPointer(effort), Status: QuestionAnswerPending, RepeatIndex: &repeat}
				if err = tx.QueryRow(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,question_keyword_snapshot,reasoning_effort,request_protocol,repeat_index,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending') RETURNING created_at,updated_at`, record.ID, user, targetID, batchID, model, question.ID, question.Name, question.Body, record.QuestionKeywordSnapshot, effort, protocol, index).Scan(&record.CreatedAt, &record.UpdatedAt); err != nil {
					return nil, false, err
				}
				records = append(records, record)
			}
		}
	}
	if !consistentScheduledQuestionAnswerRecords(*e, target, records) {
		return nil, false, requestError(ErrorQuestionAnswerStorage)
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_execution_targets SET status='batch_created',started_at=$4,updated_at=$4 WHERE execution_id=$1 AND target_id=$2 AND id=$3`, executionID, targetID, batchID, now); err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE connection_health_question_answer_schedule_executions SET version=version+1,updated_at=$4 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, workspace, executionID, now); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return nil, false, err
		}
		return nil, false, &uncertainQuestionAnswerCreateError{cause: err}
	}
	return records, true, nil
}

func consistentScheduledQuestionAnswerRecords(e QuestionAnswerScheduleExecution, target QuestionAnswerScheduleExecutionTarget, records []QuestionAnswerRecord) bool {
	if e.ConfigSnapshot.Resolved == nil {
		return false
	}
	config := e.ConfigSnapshot.Requested.Schedule
	expected := len(target.AvailableModels) * len(e.ConfigSnapshot.Resolved.Questions) * config.RepeatCount
	if len(records) != expected || expected == 0 || expected != target.PlannedRequestCount {
		return false
	}
	questions := map[string]TestQuestion{}
	for _, q := range e.ConfigSnapshot.Resolved.Questions {
		if _, exists := questions[q.ID]; exists {
			return false
		}
		questions[q.ID] = q
	}
	models := map[string]bool{}
	for _, model := range target.AvailableModels {
		if models[model] || !slices.Contains(target.RequestedModels, model) {
			return false
		}
		models[model] = true
	}
	type recordIdentity struct {
		model, question string
		repeat          int
	}
	seen := map[recordIdentity]bool{}
	for _, record := range records {
		q, ok := questions[record.QuestionID]
		if !ok || !models[record.ModelName] || record.BatchID != target.ID || record.TargetID != target.TargetID || record.QuestionName != q.Name || record.QuestionBody != q.Body || !slices.Equal(record.QuestionKeywordSnapshot, q.Keywords) || record.RequestProtocol == nil || *record.RequestProtocol != target.TestConfigurationSnapshot.Protocol || record.ReasoningEffort == nil || string(*record.ReasoningEffort) != config.ReasoningEffort || record.RepeatIndex == nil || *record.RepeatIndex < 1 || *record.RepeatIndex > config.RepeatCount {
			return false
		}
		key := recordIdentity{record.ModelName, record.QuestionID, *record.RepeatIndex}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

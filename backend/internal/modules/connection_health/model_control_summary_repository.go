package connection_health

import (
	"context"
	"encoding/json"
	"time"
)

// Admin-groups and recent summaries add one local query; C1 remains the only
// evidence source. Each account/model scan stays bounded to 1,000 records.
func (r *Repository) queryModelControlAccountSummaries(ctx context.Context, user, workspace string, targetIDs []string) (map[string]*ModelControlAccountSummary, error) {
	result := map[string]*ModelControlAccountSummary{}
	if len(targetIDs) == 0 {
		return result, nil
	}
	rows, err := r.db.Query(ctx, `SELECT to_jsonb(t),to_jsonb(rule),COALESCE(rounds.items,'[]'::jsonb)
 FROM connection_health_model_control_targets t JOIN connection_health_model_control_rules rule ON rule.user_id=t.user_id AND rule.admin_account_id=t.admin_account_id AND rule.model_name=t.model_name
 LEFT JOIN LATERAL(SELECT jsonb_agg(jsonb_build_object('batchId',b.batch_id,'source',COALESCE(e.trigger,'manual'),'scheduleName','','createdAt',b.created_at,'completedAt',b.completed_at,'running',b.running,'correct',b.correct,'incorrect',b.incorrect,'unreviewed',b.unreviewed,'failed',b.failed,'cancelled',b.cancelled) ORDER BY b.created_at DESC,b.batch_id DESC) AS items FROM(
 SELECT batch_id,min(created_at) created_at,max(completed_at) completed_at,bool_or(status IN('pending','running')) running,
 count(*) FILTER(WHERE status='succeeded' AND answer_judgment='correct') correct,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='incorrect') incorrect,
 count(*) FILTER(WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN('correct','incorrect'))) unreviewed,count(*) FILTER(WHERE status='failed') failed,count(*) FILTER(WHERE status='cancelled') cancelled
 FROM(SELECT batch_id,created_at,completed_at,status,answer_judgment FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=t.target_id AND model_name=t.model_name ORDER BY created_at DESC,id DESC LIMIT 1000) bounded GROUP BY batch_id ORDER BY min(created_at) DESC,batch_id DESC LIMIT 20) b
 LEFT JOIN connection_health_question_answer_schedule_execution_targets et ON et.id=b.batch_id AND et.target_id=t.target_id LEFT JOIN connection_health_question_answer_schedule_executions e ON e.id=et.execution_id AND e.user_id=$1) rounds ON true
 WHERE t.user_id=$1 AND t.admin_account_id=$2 AND t.target_id=ANY($3::text[])`, user, workspace, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var targetJSON, ruleJSON, roundJSON []byte
		if err := rows.Scan(&targetJSON, &ruleJSON, &roundJSON); err != nil {
			return nil, err
		}
		var stored struct {
			TargetID       string               `json:"target_id"`
			ModelName      string               `json:"model_name"`
			ClosedEntries  map[string]string    `json:"closed_entries"`
			Pending        *modelControlPending `json:"pending"`
			ConflictReason string               `json:"conflict_reason"`
			State          string               `json:"observed_state"`
			Schedulable    *bool                `json:"observed_account_schedulable"`
			LastAttempt    *modelControlAttempt `json:"last_attempt"`
		}
		var sr struct {
			ModelName          string `json:"model_name"`
			MinAccuracyPercent int    `json:"min_accuracy_percent"`
			MinJudgedAnswers   int    `json:"min_judged_answers"`
			IncludeManual      bool   `json:"include_manual"`
			IncludeScheduled   bool   `json:"include_scheduled"`
			Version            int64  `json:"version"`
		}
		rounds := []modelControlRound{}
		if err := json.Unmarshal(targetJSON, &stored); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(ruleJSON, &sr); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(roundJSON, &rounds); err != nil {
			return nil, err
		}
		rule := ModelControlRule{ModelName: sr.ModelName, MinAccuracyPercent: sr.MinAccuracyPercent, MinJudgedAnswers: sr.MinJudgedAnswers, IncludeManual: sr.IncludeManual, IncludeScheduled: sr.IncludeScheduled, Version: sr.Version}
		target := modelControlTarget{TargetID: stored.TargetID, ModelName: stored.ModelName, ClosedEntries: stored.ClosedEntries, Pending: stored.Pending, ConflictReason: stored.ConflictReason, LastAttempt: stored.LastAttempt, Observation: modelControlObservation{State: stored.State, AccountSchedulable: stored.Schedulable}}
		summary := result[target.TargetID]
		if summary == nil {
			summary = &ModelControlAccountSummary{}
			result[target.TargetID] = summary
		}
		if len(target.ClosedEntries) > 0 {
			summary.Closed++
		}
		if modelControlNeedsAttention(buildModelControlItem(target, rule, rounds, nil, now)) {
			summary.Attention++
		}
	}
	return result, rows.Err()
}

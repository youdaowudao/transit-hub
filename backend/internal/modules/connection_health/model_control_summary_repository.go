package connection_health

import (
	"context"
	"encoding/json"
	"time"
)

// Admin-groups and recent summaries read workspace settings once before the
// local summary query. C1 remains the only evidence source, and each
// account/model scan stays bounded to 1,000 records.
func (r *Repository) queryModelControlAccountSummaries(ctx context.Context, user, workspace string, targetIDs []string) (map[string]*ModelControlAccountSummary, error) {
	result := map[string]*ModelControlAccountSummary{}
	if len(targetIDs) == 0 {
		return result, nil
	}
	settings, err := r.GetModelControlSettings(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT to_jsonb(t),COALESCE(rounds.items,'[]'::jsonb)
 FROM connection_health_model_control_targets t
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
		var targetJSON, roundJSON []byte
		if err := rows.Scan(&targetJSON, &roundJSON); err != nil {
			return nil, err
		}
		var stored struct {
			TargetID       string               `json:"target_id"`
			ModelName      string               `json:"model_name"`
			ClosedEntries  map[string]string    `json:"closed_entries"`
			Pending        *modelControlPending `json:"pending"`
			ConflictReason string               `json:"conflict_reason"`
			Reason         string               `json:"observed_reason"`
			ObservedAt     *time.Time           `json:"observed_at"`
			State          string               `json:"observed_state"`
			Schedulable    *bool                `json:"observed_account_schedulable"`
			LastAttempt    *modelControlAttempt `json:"last_attempt"`
		}
		rounds := []modelControlRound{}
		if err := json.Unmarshal(targetJSON, &stored); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(roundJSON, &rounds); err != nil {
			return nil, err
		}
		rule := modelControlWorkspaceRule(settings, stored.ModelName)
		target := modelControlTarget{TargetID: stored.TargetID, ModelName: stored.ModelName, ClosedEntries: stored.ClosedEntries, Pending: stored.Pending, ConflictReason: stored.ConflictReason, LastAttempt: stored.LastAttempt, Observation: modelControlObservation{State: stored.State, ReasonKey: stored.Reason, CheckedAt: stored.ObservedAt, AccountSchedulable: stored.Schedulable}}
		summary := result[target.TargetID]
		if summary == nil {
			summary = &ModelControlAccountSummary{}
			result[target.TargetID] = summary
		}
		appendModelControlSummary(summary, target, buildModelControlItem(target, rule, rounds, nil, now))
	}
	for _, summary := range result {
		sortModelControlSummary(summary)
	}
	return result, rows.Err()
}

package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"sort"
	"time"
)

type modelControlTxGuard func(context.Context, pgx.Tx, time.Time) error

type modelControlPair struct{ TargetID, ModelName string }
type modelControlRepository interface {
	GetModelControlSettings(context.Context, string, string) (ModelControlSettings, error)
	SaveModelControlSettings(context.Context, string, string, ModelControlSettings, int64) (ModelControlSettings, error)
	ListModelControlTargets(context.Context, string, string) ([]modelControlTarget, error)
	InsertModelControlTarget(context.Context, string, string, string, string) (modelControlTarget, error)
	ListModelControlRounds(context.Context, string, []modelControlPair) (map[modelControlPair][]modelControlRound, error)
	ListRecentQuestionAnswerModels(context.Context, string, string) ([]string, error)
	ListModelControlCoverage(context.Context, string, string, []modelControlPair) (map[modelControlPair][]modelControlScheduleCoverage, error)
	ListModelControlEvents(context.Context, string, string, string, string, int) ([]ModelControlEvent, int, error)
	InsertModelControlEvent(context.Context, ModelControlEvent) error
	mutateModelControlOwnership(context.Context, string, string, string, bool, string, func([]*modelControlTarget, time.Time) ([]ModelControlEvent, error), ...modelControlTxGuard) error
	observeModelControlTarget(context.Context, modelControlTarget, time.Time, bool, string) (modelControlTarget, error)
	recordModelControlReceipt(context.Context, string, string, string, string, string) error
}

const modelControlTargetColumns = `id,user_id,admin_account_id,target_id,model_name,account_name,closed_entries,closed_at,closed_by,closed_batch_id,closed_accuracy_percent,conflict_reason,pending,last_pending_id,unconfirmed_close,observed_state,observed_reason,observed_sources,observed_account_status,observed_account_schedulable,observed_at,last_attempt,attempt_at,version,created_at,updated_at`

func scanModelControlTarget(row pgx.Row) (modelControlTarget, error) {
	var t modelControlTarget
	err := row.Scan(&t.ID, &t.UserID, &t.AdminAccountID, &t.TargetID, &t.ModelName, &t.AccountName, &t.ClosedEntries, &t.ClosedAt, &t.ClosedBy, &t.ClosedBatchID, &t.ClosedAccuracyPercent, &t.ConflictReason, &t.Pending, &t.LastPendingID, &t.UnconfirmedClose, &t.Observation.State, &t.Observation.ReasonKey, &t.Observation.Sources, &t.Observation.AccountStatus, &t.Observation.AccountSchedulable, &t.Observation.CheckedAt, &t.LastAttempt, &t.AttemptAt, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}
func scanModelControlSettings(row pgx.Row) (ModelControlSettings, error) {
	var settings ModelControlSettings
	err := row.Scan(&settings.MinAccuracyPercent, &settings.MinJudgedAnswers, &settings.Version)
	return settings, err
}
func (r *Repository) GetModelControlSettings(ctx context.Context, user, workspace string) (ModelControlSettings, error) {
	settings, err := scanModelControlSettings(r.db.QueryRow(ctx, `SELECT min_accuracy_percent,min_judged_answers,version FROM connection_health_model_control_settings WHERE user_id=$1 AND admin_account_id=$2`, user, workspace))
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelControlSettings{50, 3, 0}, nil
	}
	return settings, err
}
func (r *Repository) SaveModelControlSettings(ctx context.Context, user, workspace string, input ModelControlSettings, expected int64) (ModelControlSettings, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return input, err
	}
	defer tx.Rollback(ctx)
	var saved ModelControlSettings
	if expected == 0 {
		saved, err = scanModelControlSettings(tx.QueryRow(ctx, `INSERT INTO connection_health_model_control_settings(user_id,admin_account_id,min_accuracy_percent,min_judged_answers) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING RETURNING min_accuracy_percent,min_judged_answers,version`, user, workspace, input.MinAccuracyPercent, input.MinJudgedAnswers))
	} else {
		saved, err = scanModelControlSettings(tx.QueryRow(ctx, `UPDATE connection_health_model_control_settings SET min_accuracy_percent=$3,min_judged_answers=$4,version=version+1,updated_at=now() WHERE user_id=$1 AND admin_account_id=$2 AND version=$5 RETURNING min_accuracy_percent,min_judged_answers,version`, user, workspace, input.MinAccuracyPercent, input.MinJudgedAnswers, expected))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		current, readErr := scanModelControlSettings(tx.QueryRow(ctx, `SELECT min_accuracy_percent,min_judged_answers,version FROM connection_health_model_control_settings WHERE user_id=$1 AND admin_account_id=$2`, user, workspace))
		if errors.Is(readErr, pgx.ErrNoRows) {
			current, readErr = ModelControlSettings{50, 3, 0}, nil
		}
		if readErr != nil {
			return input, readErr
		}
		return input, modelControlConflict("VersionConflict", current)
	}
	if err != nil {
		return input, err
	}
	err = insertModelControlEventTx(ctx, tx, modelControlNewEvent(user, workspace, "", "", "settings_saved", nil, map[string]any{"minAccuracyPercent": saved.MinAccuracyPercent, "minJudgedAnswers": saved.MinJudgedAnswers}))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return saved, err
}
func (r *Repository) ListModelControlTargets(ctx context.Context, user, workspace string) ([]modelControlTarget, error) {
	rows, err := r.db.Query(ctx, `SELECT `+modelControlTargetColumns+` FROM connection_health_model_control_targets WHERE user_id=$1 AND admin_account_id=$2 ORDER BY target_id,model_name,id`, user, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []modelControlTarget{}
	for rows.Next() {
		target, err := scanModelControlTarget(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, rows.Err()
}
func (r *Repository) InsertModelControlTarget(ctx context.Context, user, workspace, targetID, model string) (modelControlTarget, error) {
	var result modelControlTarget
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	ruleID, err := newID()
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO connection_health_model_control_rules(id,user_id,admin_account_id,model_name) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,admin_account_id,model_name) DO NOTHING`, ruleID, user, workspace, model)
	if err != nil {
		return result, err
	}
	id, err := newID()
	if err != nil {
		return result, err
	}
	result, err = scanModelControlTarget(tx.QueryRow(ctx, `INSERT INTO connection_health_model_control_targets(id,user_id,admin_account_id,target_id,model_name) VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,admin_account_id,target_id,model_name) DO NOTHING RETURNING `+modelControlTargetColumns, id, user, workspace, targetID, model))
	if errors.Is(err, pgx.ErrNoRows) {
		result, err = scanModelControlTarget(tx.QueryRow(ctx, `SELECT `+modelControlTargetColumns+` FROM connection_health_model_control_targets WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3 AND model_name=$4`, user, workspace, targetID, model))
	} else if err == nil {
		err = insertModelControlEventTx(ctx, tx, modelControlNewEvent(user, workspace, targetID, model, "managed_added", nil, map[string]any{}))
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23503" {
		return result, modelControlConflict("VersionConflict", nil)
	}
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (r *Repository) ListModelControlRounds(ctx context.Context, user string, pairs []modelControlPair) (map[modelControlPair][]modelControlRound, error) {
	result := map[modelControlPair][]modelControlRound{}
	if len(pairs) == 0 {
		return result, nil
	}
	if len(pairs) > 50 {
		return nil, requestError(ErrorRequest)
	}
	targets, models := []string{}, []string{}
	for _, p := range pairs {
		targets = append(targets, p.TargetID)
		models = append(models, p.ModelName)
	}
	rows, err := r.db.Query(ctx, `WITH requested AS(SELECT * FROM unnest($2::text[],$3::text[]) AS p(target_id,model_name)),rounds AS(
 SELECT p.target_id,p.model_name,b.* FROM requested p CROSS JOIN LATERAL(
 SELECT batch_id,min(created_at) AS created_at,max(completed_at) AS completed_at,bool_or(status IN ('pending','running')) AS running,
 count(*) FILTER(WHERE status='succeeded' AND answer_judgment='correct') AS correct,count(*) FILTER(WHERE status='succeeded' AND answer_judgment='incorrect') AS incorrect,
 count(*) FILTER(WHERE status='succeeded' AND (answer_judgment IS NULL OR answer_judgment NOT IN ('correct','incorrect'))) AS unreviewed,count(*) FILTER(WHERE status='failed') AS failed,count(*) FILTER(WHERE status='cancelled') AS cancelled
 FROM(SELECT batch_id,created_at,completed_at,status,answer_judgment FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=p.target_id AND model_name=p.model_name ORDER BY created_at DESC,id DESC LIMIT 1000) bounded GROUP BY batch_id ORDER BY min(created_at) DESC,batch_id DESC LIMIT 20) b)
 SELECT r.target_id,r.model_name,r.batch_id,r.created_at,r.completed_at,r.running,r.correct,r.incorrect,r.unreviewed,r.failed,r.cancelled,COALESCE(e.trigger,'manual'),COALESCE(e.config_snapshot->'requested'->'schedule'->>'name',s.name,'')
 FROM rounds r LEFT JOIN connection_health_question_answer_schedule_execution_targets t ON t.id=r.batch_id AND t.target_id=r.target_id
 LEFT JOIN connection_health_question_answer_schedule_executions e ON e.id=t.execution_id AND e.user_id=$1 LEFT JOIN connection_health_question_answer_schedules s ON s.id=e.schedule_id ORDER BY r.target_id,r.model_name,r.created_at DESC,r.batch_id DESC`, user, targets, models)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p modelControlPair
		var round modelControlRound
		if err := rows.Scan(&p.TargetID, &p.ModelName, &round.BatchID, &round.CreatedAt, &round.CompletedAt, &round.Running, &round.Correct, &round.Incorrect, &round.Unreviewed, &round.Failed, &round.Cancelled, &round.Source, &round.ScheduleName); err != nil {
			return nil, err
		}
		if n := round.Correct + round.Incorrect; n > 0 {
			v := float64(round.Correct) * 100 / float64(n)
			round.AccuracyPercent = &v
		}
		result[p] = append(result[p], round)
	}
	return result, rows.Err()
}
func (r *Repository) ListRecentQuestionAnswerModels(ctx context.Context, user, target string) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT DISTINCT model_name FROM(SELECT model_name FROM connection_health_question_answer_records WHERE user_id=$1 AND target_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1000) b ORDER BY model_name`, user, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		result = append(result, model)
	}
	return result, rows.Err()
}
func (r *Repository) ListModelControlCoverage(ctx context.Context, user, workspace string, pairs []modelControlPair) (map[modelControlPair][]modelControlScheduleCoverage, error) {
	result := map[modelControlPair][]modelControlScheduleCoverage{}
	if len(pairs) == 0 {
		return result, nil
	}
	targets, models := []string{}, []string{}
	for _, p := range pairs {
		targets = append(targets, p.TargetID)
		models = append(models, p.ModelName)
	}
	rows, err := r.db.Query(ctx, `SELECT p.target_id,p.model_name,s.id,s.name FROM unnest($3::text[],$4::text[]) p(target_id,model_name) JOIN connection_health_question_answer_schedules s ON p.model_name=ANY(s.model_names) WHERE s.user_id=$1 AND s.admin_account_id=$2 AND s.enabled AND s.deleted_at IS NULL AND s.blocked_reason='' AND ((s.target_mode='accounts' AND p.target_id=ANY(s.selected_account_target_ids)) OR (s.target_mode='groups' AND EXISTS(SELECT 1 FROM connection_health_question_answer_schedule_execution_targets t WHERE t.target_id=p.target_id AND t.execution_id=(SELECT e.id FROM connection_health_question_answer_schedule_executions e WHERE e.schedule_id=s.id ORDER BY e.created_at DESC,e.id DESC LIMIT 1)))) ORDER BY s.name,s.id`, user, workspace, targets, models)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p modelControlPair
		var c modelControlScheduleCoverage
		if err := rows.Scan(&p.TargetID, &p.ModelName, &c.ID, &c.Name); err != nil {
			return nil, err
		}
		result[p] = append(result[p], c)
	}
	return result, rows.Err()
}
func modelControlNewEvent(user, workspace, target, model, event string, basis, detail any) ModelControlEvent {
	id, _ := newID()
	if basis == nil {
		basis = map[string]any{}
	}
	if detail == nil {
		detail = map[string]any{}
	}
	return ModelControlEvent{ID: id, UserID: user, AdminAccountID: workspace, TargetID: target, ModelName: model, EventType: event, ActorUserID: user, Basis: basis, Detail: detail, CreatedAt: time.Now().UTC()}
}
func insertModelControlEventTx(ctx context.Context, tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, e ModelControlEvent) error {
	_, err := tx.Exec(ctx, `INSERT INTO connection_health_model_control_events(id,user_id,admin_account_id,target_id,model_name,event_type,actor_user_id,basis,detail,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, e.ID, e.UserID, e.AdminAccountID, e.TargetID, e.ModelName, e.EventType, e.ActorUserID, e.Basis, e.Detail, e.CreatedAt)
	return err
}
func (r *Repository) InsertModelControlEvent(ctx context.Context, e ModelControlEvent) error {
	return insertModelControlEventTx(ctx, r.db, e)
}
func (r *Repository) ListModelControlEvents(ctx context.Context, user, workspace, target, model string, page int) ([]ModelControlEvent, int, error) {
	var total int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM connection_health_model_control_events WHERE user_id=$1 AND admin_account_id=$2 AND ($3='' OR target_id=$3) AND ($4='' OR model_name=$4)`, user, workspace, target, model).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(ctx, `SELECT id,user_id,admin_account_id,target_id,model_name,event_type,actor_user_id,basis,detail,created_at FROM connection_health_model_control_events WHERE user_id=$1 AND admin_account_id=$2 AND ($3='' OR target_id=$3) AND ($4='' OR model_name=$4) ORDER BY created_at DESC,id DESC LIMIT 50 OFFSET $5`, user, workspace, target, model, (page-1)*50)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := []ModelControlEvent{}
	for rows.Next() {
		var e ModelControlEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.AdminAccountID, &e.TargetID, &e.ModelName, &e.EventType, &e.ActorUserID, &e.Basis, &e.Detail, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	return events, (total + 49) / 50, rows.Err()
}
func (r *Repository) setModelControlCommitter(commit func(context.Context, pgx.Tx, string) error) {
	r.modelControlCommitter = commit
}
func (r *Repository) commitModelControl(ctx context.Context, tx pgx.Tx, stage string) error {
	if r.modelControlCommitter != nil {
		return r.modelControlCommitter(ctx, tx, stage)
	}
	return tx.Commit(ctx)
}

type modelControlCommitError struct {
	error
	proof []modelControlCommitTarget
}

func modelControlHandleValid(h *RuntimeLeaseHandle) bool {
	if h == nil || h.Context == nil || h.Context.Err() != nil {
		return false
	}
	select {
	case <-h.Lost:
		return false
	default:
		return true
	}
}
func modelControlLeasesValid(ctx context.Context, mutation bool) bool {
	return modelControlHandleValid(actionLeaseFromContext(ctx)) && (!mutation || modelControlHandleValid(mutationLeaseFromContext(ctx)))
}

// Lock order: workspace row, account lease, mutation lease, all account objects,
// database clock, live handles. Observations are intentionally absent from writes.
func (r *Repository) mutateModelControlOwnership(ctx context.Context, user, workspace, targetID string, mutation bool, stage string, fn func([]*modelControlTarget, time.Time) ([]ModelControlEvent, error), guards ...modelControlTxGuard) error {
	if !modelControlLeasesValid(ctx, mutation) {
		return ErrRemoteActionLeaseLost
	}
	tx, err := r.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	handles := []*RuntimeLeaseHandle{actionLeaseFromContext(ctx)}
	if mutation {
		handles = append(handles, mutationLeaseFromContext(ctx))
	}
	expiry := []time.Time{}
	for _, h := range handles {
		var owner string
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT owner_id,expires_at FROM connection_health_runtime_leases WHERE lease_key=$1 FOR UPDATE`, h.Key).Scan(&owner, &expires); err != nil || owner != h.OwnerID {
			return ErrRemoteActionLeaseLost
		}
		expiry = append(expiry, expires)
	}
	rows, err := tx.Query(ctx, `SELECT `+modelControlTargetColumns+` FROM connection_health_model_control_targets WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3 ORDER BY id FOR UPDATE`, user, workspace, targetID)
	if err != nil {
		return err
	}
	targets := []*modelControlTarget{}
	for rows.Next() {
		t, err := scanModelControlTarget(rows)
		if err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, &t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	for _, expires := range expiry {
		if !expires.After(now) {
			return ErrRemoteActionLeaseLost
		}
	}
	if !modelControlLeasesValid(ctx, mutation) {
		return ErrRemoteActionLeaseLost
	}
	for _, guard := range guards {
		if err = guard(ctx, tx, now); err != nil {
			return err
		}
	}
	original := map[string]modelControlTarget{}
	before := map[string]string{}
	for _, t := range targets {
		original[t.ID] = *t
		b, _ := json.Marshal(modelControlOwnershipSnapshot(*t))
		before[t.ID] = string(b)
	}
	events, err := fn(targets, now)
	if err != nil {
		return err
	}
	proof := make([]modelControlCommitTarget, 0, len(targets))
	for _, t := range targets {
		if t.deleted {
			proof = append(proof, modelControlCommitTarget{ID: t.ID, Deleted: true})
			if _, err = tx.Exec(ctx, `DELETE FROM connection_health_model_control_targets WHERE id=$1 AND version=$2`, t.ID, t.Version); err != nil {
				return err
			}
			continue
		}
		if t.ID == "" {
			continue
		}
		b, _ := json.Marshal(modelControlOwnershipSnapshot(*t))
		expected := modelControlCommitTarget{ID: t.ID, Version: t.Version, Ownership: string(b)}
		if before[t.ID] != expected.Ownership {
			expected.Version++
		}
		proof = append(proof, expected)
		if t.observationAt != nil {
			current := original[t.ID]
			merged := mergeModelControlObservation(current, *t, *t.observationAt, t.updateAttempt)
			if err = writeModelControlObservationTx(ctx, tx, merged); err != nil {
				return err
			}
		}
		if before[t.ID] == string(b) {
			continue
		}
		_, err = tx.Exec(ctx, `UPDATE connection_health_model_control_targets SET closed_entries=$2,closed_at=$3,closed_by=$4,closed_batch_id=$5,closed_accuracy_percent=$6,pending=$7,last_pending_id=$8,unconfirmed_close=$9,version=version+1,updated_at=$10 WHERE id=$1 AND version=$11`, t.ID, t.ClosedEntries, t.ClosedAt, t.ClosedBy, t.ClosedBatchID, t.ClosedAccuracyPercent, t.Pending, t.LastPendingID, t.UnconfirmedClose, now, t.Version)
		if err != nil {
			return err
		}
	}
	for _, e := range events {
		if err = insertModelControlEventTx(ctx, tx, e); err != nil {
			return err
		}
	}
	if err = r.commitModelControl(ctx, tx, stage); err != nil {
		return &modelControlCommitError{error: err, proof: proof}
	}
	return nil
}
func modelControlOwnershipSnapshot(t modelControlTarget) any {
	return struct {
		ClosedEntries           map[string]string
		ClosedAt                *time.Time
		ClosedBy, ClosedBatchID *string
		ClosedAccuracyPercent   *float64
		Pending                 *modelControlPending
		LastPendingID           string
		UnconfirmedClose        *modelControlUnconfirmed
	}{t.ClosedEntries, t.ClosedAt, t.ClosedBy, t.ClosedBatchID, t.ClosedAccuracyPercent, t.Pending, t.LastPendingID, t.UnconfirmedClose}
}
func modelControlSameBlock(a, b *modelControlAttempt) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Operation != b.Operation || a.ReasonKey != b.ReasonKey {
		return false
	}
	groupKeys := func(groups []modelSourceCount) []string {
		keys := []string{}
		for _, g := range groups {
			keys = append(keys, g.GroupID+"\x00"+g.Key)
		}
		sort.Strings(keys)
		return keys
	}
	return reflect.DeepEqual(groupKeys(a.Groups), groupKeys(b.Groups))
}
func (r *Repository) observeModelControlTarget(ctx context.Context, incoming modelControlTarget, at time.Time, setAttempt bool, event string) (modelControlTarget, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, incoming.UserID, incoming.AdminAccountID)
	if err != nil {
		return incoming, err
	}
	defer tx.Rollback(ctx)
	current, err := scanModelControlTarget(tx.QueryRow(ctx, `SELECT `+modelControlTargetColumns+` FROM connection_health_model_control_targets WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 FOR UPDATE`, incoming.ID, incoming.UserID, incoming.AdminAccountID))
	if err != nil {
		return incoming, err
	}
	updated := false
	writeEvent := false
	if current.Observation.CheckedAt == nil || !at.Before(*current.Observation.CheckedAt) {
		current.Observation = incoming.Observation
		current.Observation.CheckedAt = &at
		current.AccountName = incoming.AccountName
		current.ConflictReason = incoming.ConflictReason
		updated = true
	}
	if setAttempt && (current.AttemptAt == nil || !at.Before(*current.AttemptAt)) {
		writeEvent = event != "" && !modelControlSameBlock(current.LastAttempt, incoming.LastAttempt)

		current.LastAttempt = incoming.LastAttempt
		current.AttemptAt = &at
		updated = true
	}
	if updated {
		_, err = tx.Exec(ctx, `UPDATE connection_health_model_control_targets SET account_name=$2,conflict_reason=$3,observed_state=$4,observed_reason=$5,observed_sources=$6,observed_account_status=$7,observed_account_schedulable=$8,observed_at=$9,last_attempt=$10,attempt_at=$11 WHERE id=$1`, current.ID, current.AccountName, current.ConflictReason, current.Observation.State, current.Observation.ReasonKey, current.Observation.Sources, current.Observation.AccountStatus, current.Observation.AccountSchedulable, current.Observation.CheckedAt, current.LastAttempt, current.AttemptAt)
		if err != nil {
			return current, err
		}
	}
	if writeEvent {
		detail := any(incoming.LastAttempt)
		if event == "conflict_detected" {
			detail = map[string]any{"reasonKey": incoming.ConflictReason}
		}
		if err = insertModelControlEventTx(ctx, tx, modelControlNewEvent(current.UserID, current.AdminAccountID, current.TargetID, current.ModelName, event, nil, detail)); err != nil {
			return current, err
		}
	}
	return current, tx.Commit(ctx)
}
func (r *Repository) recordModelControlReceipt(ctx context.Context, user, workspace, target, pendingID, receipt string) error {
	_, err := r.db.Exec(ctx, `UPDATE connection_health_model_control_targets SET pending=jsonb_set(pending,'{receipt}',to_jsonb($5::text)) WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3 AND pending->>'id'=$4 AND pending->>'phase'='sending'`, user, workspace, target, pendingID, receipt)
	return err
}

func mergeModelControlObservation(current, incoming modelControlTarget, at time.Time, setAttempt bool) modelControlTarget {
	if current.Observation.CheckedAt == nil || !at.Before(*current.Observation.CheckedAt) {
		current.Observation = incoming.Observation
		current.Observation.CheckedAt = &at
		current.AccountName = incoming.AccountName
		current.ConflictReason = incoming.ConflictReason
	}
	if setAttempt && (current.AttemptAt == nil || !at.Before(*current.AttemptAt)) {
		current.LastAttempt = incoming.LastAttempt
		current.AttemptAt = &at
	}
	return current
}
func writeModelControlObservationTx(ctx context.Context, tx pgx.Tx, t modelControlTarget) error {
	_, err := tx.Exec(ctx, `UPDATE connection_health_model_control_targets SET account_name=$2,conflict_reason=$3,observed_state=$4,observed_reason=$5,observed_sources=$6,observed_account_status=$7,observed_account_schedulable=$8,observed_at=$9,last_attempt=$10,attempt_at=$11 WHERE id=$1`, t.ID, t.AccountName, t.ConflictReason, t.Observation.State, t.Observation.ReasonKey, t.Observation.Sources, t.Observation.AccountStatus, t.Observation.AccountSchedulable, t.Observation.CheckedAt, t.LastAttempt, t.AttemptAt)
	return err
}

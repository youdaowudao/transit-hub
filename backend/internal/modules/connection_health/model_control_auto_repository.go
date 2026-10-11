package connection_health

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

type ModelControlAutoSettings struct {
	AutoCloseEnabled          bool       `json:"autoCloseEnabled"`
	AutoCloseEnabledAt        *time.Time `json:"autoCloseEnabledAt"`
	AutoRestoreEnabled        bool       `json:"autoRestoreEnabled"`
	AutoRestoreEnabledAt      *time.Time `json:"autoRestoreEnabledAt"`
	HealthCoordinationEnabled bool       `json:"healthCoordinationEnabled"`
	VerifyIntervalMinutes     int        `json:"verifyIntervalMinutes"`
	RetryIntervalMinutes      int        `json:"retryIntervalMinutes"`
	Version                   int64      `json:"version"`
	LastScanStartedAt         *time.Time `json:"lastScanStartedAt"`
	LastScanFinishedAt        *time.Time `json:"lastScanFinishedAt"`
	LastScanErrorKey          string     `json:"lastScanErrorKey"`
}

func (a ModelControlAutoSettings) enabled() bool {
	return a.AutoCloseEnabled || a.AutoRestoreEnabled || a.HealthCoordinationEnabled
}
func defaultModelControlAutoSettings() ModelControlAutoSettings {
	return ModelControlAutoSettings{VerifyIntervalMinutes: 10, RetryIntervalMinutes: 5}
}

type modelControlAutoWorkspace struct{ UserID, AdminAccountID string }
type modelControlAutoRepository interface {
	GetModelControlAutoSettings(context.Context, string, string) (ModelControlAutoSettings, error)
	SaveModelControlAutoSettings(context.Context, string, string, ModelControlAutoSettings, int64) (ModelControlAutoSettings, error)
	GetModelControlRuleSavedAt(context.Context, string, string) (*time.Time, error)
	ListModelControlAutoWorkspaces(context.Context) ([]modelControlAutoWorkspace, error)
	MarkModelControlAutoScan(context.Context, string, string, *time.Time, *time.Time, string) error
	SaveModelControlAutoState(context.Context, string, string, string, *modelControlAutoAttempt) error
	ListModelControlVerifyDue(context.Context, string, string, int, time.Time, int) ([]string, error)
	MarkModelControlAutoVerify(context.Context, string, string, string, *time.Time) error
	ListModelControlFullyClosed(context.Context, string, string) (map[string]map[string]struct{}, error)
}

const modelControlAutoColumns = `auto_close_enabled,auto_close_enabled_at,auto_restore_enabled,auto_restore_enabled_at,health_coordination_enabled,verify_interval_minutes,retry_interval_minutes,version,last_scan_started_at,last_scan_finished_at,last_scan_error_key`

func scanModelControlAutoSettings(row pgx.Row) (ModelControlAutoSettings, error) {
	var a ModelControlAutoSettings
	err := row.Scan(&a.AutoCloseEnabled, &a.AutoCloseEnabledAt, &a.AutoRestoreEnabled, &a.AutoRestoreEnabledAt, &a.HealthCoordinationEnabled, &a.VerifyIntervalMinutes, &a.RetryIntervalMinutes, &a.Version, &a.LastScanStartedAt, &a.LastScanFinishedAt, &a.LastScanErrorKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultModelControlAutoSettings(), nil
	}
	return a, err
}
func (r *Repository) GetModelControlAutoSettings(ctx context.Context, user, ws string) (ModelControlAutoSettings, error) {
	return scanModelControlAutoSettings(r.db.QueryRow(ctx, `SELECT `+modelControlAutoColumns+` FROM connection_health_model_control_auto WHERE user_id=$1 AND admin_account_id=$2`, user, ws))
}
func (r *Repository) SaveModelControlAutoSettings(ctx context.Context, user, ws string, in ModelControlAutoSettings, expected int64) (ModelControlAutoSettings, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, user, ws)
	if err != nil {
		return in, err
	}
	defer tx.Rollback(ctx)
	var row pgx.Row
	if expected == 0 {
		row = tx.QueryRow(ctx, `INSERT INTO connection_health_model_control_auto(user_id,admin_account_id,auto_close_enabled,auto_restore_enabled,health_coordination_enabled,auto_close_enabled_at,auto_restore_enabled_at,verify_interval_minutes,retry_interval_minutes) VALUES($1,$2,$3,$4,$5,CASE WHEN $3 THEN now() END,CASE WHEN $4 THEN now() END,$6,$7) ON CONFLICT DO NOTHING RETURNING `+modelControlAutoColumns, user, ws, in.AutoCloseEnabled, in.AutoRestoreEnabled, in.HealthCoordinationEnabled, in.VerifyIntervalMinutes, in.RetryIntervalMinutes)
	} else {
		row = tx.QueryRow(ctx, `UPDATE connection_health_model_control_auto SET auto_close_enabled=$3,auto_restore_enabled=$4,health_coordination_enabled=$5,auto_close_enabled_at=CASE WHEN NOT $3 THEN NULL WHEN NOT auto_close_enabled THEN now() ELSE auto_close_enabled_at END,auto_restore_enabled_at=CASE WHEN NOT $4 THEN NULL WHEN NOT auto_restore_enabled THEN now() ELSE auto_restore_enabled_at END,verify_interval_minutes=$6,retry_interval_minutes=$7,version=version+1,updated_at=now() WHERE user_id=$1 AND admin_account_id=$2 AND version=$8 RETURNING `+modelControlAutoColumns, user, ws, in.AutoCloseEnabled, in.AutoRestoreEnabled, in.HealthCoordinationEnabled, in.VerifyIntervalMinutes, in.RetryIntervalMinutes, expected)
	}
	saved, err := scanModelControlAutoSettings(row)
	if err != nil {
		return in, err
	}
	if saved.Version == 0 {
		current, e := scanModelControlAutoSettings(tx.QueryRow(ctx, `SELECT `+modelControlAutoColumns+` FROM connection_health_model_control_auto WHERE user_id=$1 AND admin_account_id=$2`, user, ws))
		if e != nil {
			return in, e
		}
		return in, modelControlConflict("VersionConflict", current)
	}
	err = insertModelControlEventTx(ctx, tx, modelControlNewEvent(user, ws, "", "", "auto_settings_saved", nil, map[string]any{"autoCloseEnabled": saved.AutoCloseEnabled, "autoRestoreEnabled": saved.AutoRestoreEnabled, "healthCoordinationEnabled": saved.HealthCoordinationEnabled, "verifyIntervalMinutes": saved.VerifyIntervalMinutes, "retryIntervalMinutes": saved.RetryIntervalMinutes}))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return saved, err
}
func (r *Repository) GetModelControlRuleSavedAt(ctx context.Context, user, ws string) (*time.Time, error) {
	var at *time.Time
	err := r.db.QueryRow(ctx, `SELECT updated_at FROM connection_health_model_control_settings WHERE user_id=$1 AND admin_account_id=$2`, user, ws).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return at, err
}
func (r *Repository) ListModelControlAutoWorkspaces(ctx context.Context) ([]modelControlAutoWorkspace, error) {
	rows, err := r.db.Query(ctx, `SELECT user_id,admin_account_id FROM connection_health_model_control_auto WHERE auto_close_enabled OR auto_restore_enabled OR health_coordination_enabled ORDER BY user_id,admin_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []modelControlAutoWorkspace{}
	for rows.Next() {
		var w modelControlAutoWorkspace
		if err := rows.Scan(&w.UserID, &w.AdminAccountID); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
func (r *Repository) MarkModelControlAutoScan(ctx context.Context, user, ws string, started, finished *time.Time, key string) error {
	_, err := r.db.Exec(ctx, `UPDATE connection_health_model_control_auto SET last_scan_started_at=COALESCE($3,last_scan_started_at),last_scan_finished_at=COALESCE($4,last_scan_finished_at),last_scan_error_key=$5 WHERE user_id=$1 AND admin_account_id=$2`, user, ws, started, finished, key)
	return err
}
func (r *Repository) SaveModelControlAutoState(ctx context.Context, user, ws, id string, state *modelControlAutoAttempt) error {
	tag, err := r.db.Exec(ctx, `UPDATE connection_health_model_control_targets SET auto_state=$4 WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, user, ws, id, state)
	if err == nil && tag.RowsAffected() != 1 {
		return requestError(ErrorProbeTargetNotFound)
	}
	return err
}
func (r *Repository) MarkModelControlAutoVerify(ctx context.Context, user, ws, target string, at *time.Time) error {
	_, err := r.db.Exec(ctx, `UPDATE connection_health_model_control_targets SET auto_verify_at=$4 WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3`, user, ws, target, at)
	return err
}
func (r *Repository) ListModelControlVerifyDue(ctx context.Context, user, ws string, minutes int, now time.Time, limit int) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT target_id FROM connection_health_model_control_targets WHERE user_id=$1 AND admin_account_id=$2 GROUP BY target_id HAVING bool_or(closed_entries<>'{}'::jsonb OR unconfirmed_close IS NOT NULL) AND NOT bool_or(pending IS NOT NULL) AND (bool_or(auto_verify_at IS NULL) OR min(auto_verify_at)<$3) ORDER BY CASE WHEN bool_or(auto_verify_at IS NULL) THEN NULL ELSE min(auto_verify_at) END NULLS FIRST,target_id LIMIT $4`, user, ws, now.Add(-time.Duration(minutes)*time.Minute), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *Repository) ListModelControlFullyClosed(ctx context.Context, user, ws string) (map[string]map[string]struct{}, error) {
	rows, err := r.db.Query(ctx, `SELECT target_id,model_name FROM connection_health_model_control_targets WHERE user_id=$1 AND admin_account_id=$2 AND closed_entries<>'{}'::jsonb AND observed_state='closed' AND conflict_reason='' AND pending IS NULL`, user, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var target, model string
		if err := rows.Scan(&target, &model); err != nil {
			return nil, err
		}
		if out[target] == nil {
			out[target] = map[string]struct{}{}
		}
		out[target][model] = struct{}{}
	}
	return out, rows.Err()
}

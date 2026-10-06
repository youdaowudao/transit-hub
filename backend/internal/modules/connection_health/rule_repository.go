package connection_health

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const presetColumns = `id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,delay_line_ms,observation_seconds,recovery_step_percent,created_at,updated_at`
const settingsColumns = `user_id,admin_account_id,rule_version,config_generation,probe_concurrency,probe_concurrency_version,rule_switched_at,updated_at`

func builtinPresetID(kind, userID, workspace string) string {
	sum := md5.Sum([]byte(strconv.Itoa(len(userID)) + ":" + userID + workspace))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// Every write calls this inside the existing workspace transaction. Reads never initialize.
func ensureWorkspaceHealthSettings(ctx context.Context, tx pgx.Tx, userID, workspace string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO connection_health_workspace_settings(user_id,admin_account_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, workspace); err != nil {
		return err
	}
	for _, kind := range []string{PresetRecommended, PresetLegacyDefault} {
		p := DefaultRulePreset()
		p.UserID = userID
		p.AdminAccountID = workspace
		p.Kind = kind
		p.ID = builtinPresetID(kind, userID, workspace)
		if kind == PresetLegacyDefault {
			p.Name = "旧规则（默认值）"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO connection_health_rule_presets (`+presetColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now(),now()) ON CONFLICT DO NOTHING`, p.ID, p.UserID, p.AdminAccountID, p.Name, p.Kind, p.FailureThreshold, p.SuccessThreshold, p.CooldownSeconds, p.FailedRetryIntervalSeconds, p.LongFailureAfterSeconds, p.LongFailureIntervalSeconds, p.DelayLineMs, p.ObservationSeconds, p.RecoveryStepPercent); err != nil {
			return err
		}
	}
	return nil
}
func bumpHealthConfigGenerationTx(ctx context.Context, tx pgx.Tx, userID, workspace string) error {
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE connection_health_workspace_settings SET config_generation=config_generation+1,updated_at=clock_timestamp() WHERE user_id=$1 AND admin_account_id=$2`, userID, workspace)
	return err
}
func getWorkspaceHealthSettings(ctx context.Context, q stateQuerier, userID, workspace string) (WorkspaceHealthSettings, error) {
	s := defaultWorkspaceHealthSettings(userID, workspace)
	err := q.QueryRow(ctx, `SELECT `+settingsColumns+` FROM connection_health_workspace_settings WHERE user_id=$1 AND admin_account_id=$2`, userID, workspace).Scan(&s.UserID, &s.AdminAccountID, &s.RuleVersion, &s.ConfigGeneration, &s.ProbeConcurrency, &s.ProbeConcurrencyVersion, &s.RuleSwitchedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}
func (r *Repository) GetWorkspaceHealthSettings(ctx context.Context, userID, workspace string) (WorkspaceHealthSettings, error) {
	return getWorkspaceHealthSettings(ctx, r.db, userID, workspace)
}
func (r *Repository) ListWorkspaceHealthSettings(ctx context.Context) ([]WorkspaceHealthSettings, error) {
	rows, err := r.db.Query(ctx, `SELECT `+settingsColumns+` FROM connection_health_workspace_settings ORDER BY user_id,admin_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkspaceHealthSettings{}
	for rows.Next() {
		var s WorkspaceHealthSettings
		if err := rows.Scan(&s.UserID, &s.AdminAccountID, &s.RuleVersion, &s.ConfigGeneration, &s.ProbeConcurrency, &s.ProbeConcurrencyVersion, &s.RuleSwitchedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func scanRulePreset(row rowScanner) (RulePreset, error) {
	var p RulePreset
	var lines []byte
	err := row.Scan(&p.ID, &p.UserID, &p.AdminAccountID, &p.Name, &p.Kind, &p.FailureThreshold, &p.SuccessThreshold, &p.CooldownSeconds, &p.FailedRetryIntervalSeconds, &p.LongFailureAfterSeconds, &p.LongFailureIntervalSeconds, &lines, &p.ObservationSeconds, &p.RecoveryStepPercent, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(lines, &p.DelayLineMs)
	p.Policies = []RulePresetPolicy{}
	return p, err
}
func (r *Repository) ListRulePresets(ctx context.Context, userID, workspace string) ([]RulePreset, error) {
	rows, err := r.db.Query(ctx, `SELECT `+presetColumns+` FROM connection_health_rule_presets WHERE user_id=$1 AND admin_account_id=$2 ORDER BY created_at,id`, userID, workspace)
	if err != nil {
		return nil, err
	}
	out := []RulePreset{}
	for rows.Next() {
		p, err := scanRulePreset(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		for _, kind := range []string{PresetRecommended, PresetLegacyDefault} {
			p := DefaultRulePreset()
			p.ID = builtinPresetID(kind, userID, workspace)
			p.UserID = userID
			p.AdminAccountID = workspace
			p.Kind = kind
			if kind == PresetLegacyDefault {
				p.Name = "旧规则（默认值）"
			}
			out = append(out, p)
		}
	}
	refs, err := r.db.Query(ctx, `SELECT id,name,rule_preset_id,legacy_preset_id FROM connection_health_policies WHERE user_id=$1 AND admin_account_id=$2 ORDER BY created_at,id`, userID, workspace)
	if err != nil {
		return nil, err
	}
	defer refs.Close()
	for refs.Next() {
		var id, name, current, legacy string
		if err := refs.Scan(&id, &name, &current, &legacy); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].ID == current || out[i].ID == legacy {
				out[i].Policies = append(out[i].Policies, RulePresetPolicy{id, name})
			}
		}
	}
	return out, refs.Err()
}
func getRulePresetTx(ctx context.Context, tx pgx.Tx, userID, workspace, id string) (RulePreset, error) {
	p, err := scanRulePreset(tx.QueryRow(ctx, `SELECT `+presetColumns+` FROM connection_health_rule_presets WHERE user_id=$1 AND admin_account_id=$2 AND id=$3`, userID, workspace, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, requestError(ErrorNotFound)
	}
	return p, err
}
func (r *Repository) SaveRulePreset(ctx context.Context, p RulePreset) (RulePreset, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.DelayLineMs == nil {
		p.DelayLineMs = map[string]int{}
	}
	tx, err := r.beginWorkspaceTransaction(ctx, p.UserID, p.AdminAccountID)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, p.UserID, p.AdminAccountID); err != nil {
		return p, err
	}
	if p.ID != "" {
		old, err := getRulePresetTx(ctx, tx, p.UserID, p.AdminAccountID, p.ID)
		if err != nil {
			return p, err
		}
		if old.Kind == PresetLegacyDefault || old.Kind == PresetLegacySnapshot {
			return p, requestError(ErrorPresetReadOnly)
		}
		p.Kind = old.Kind
	} else {
		p.ID, err = newID()
		if err != nil {
			return p, err
		}
		p.Kind = PresetCustom
	}
	if !validRulePreset(p) {
		return p, requestError(ErrorRequest)
	}
	rows, err := tx.Query(ctx, `SELECT name FROM connection_health_rule_presets WHERE user_id=$1 AND admin_account_id=$2 AND id<>$3`, p.UserID, p.AdminAccountID, p.ID)
	if err != nil {
		return p, err
	}
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return p, err
		}
		names[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	p.Name = uniquePresetName(p.Name, names)
	_, err = tx.Exec(ctx, `INSERT INTO connection_health_rule_presets (`+presetColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now(),now()) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,failure_threshold=EXCLUDED.failure_threshold,success_threshold=EXCLUDED.success_threshold,cooldown_seconds=EXCLUDED.cooldown_seconds,failed_retry_interval_seconds=EXCLUDED.failed_retry_interval_seconds,long_failure_after_seconds=EXCLUDED.long_failure_after_seconds,long_failure_interval_seconds=EXCLUDED.long_failure_interval_seconds,delay_line_ms=EXCLUDED.delay_line_ms,observation_seconds=EXCLUDED.observation_seconds,recovery_step_percent=EXCLUDED.recovery_step_percent,updated_at=clock_timestamp()`, p.ID, p.UserID, p.AdminAccountID, p.Name, p.Kind, p.FailureThreshold, p.SuccessThreshold, p.CooldownSeconds, p.FailedRetryIntervalSeconds, p.LongFailureAfterSeconds, p.LongFailureIntervalSeconds, p.DelayLineMs, p.ObservationSeconds, p.RecoveryStepPercent)
	if err != nil {
		return p, err
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, p.UserID, p.AdminAccountID); err != nil {
		return p, err
	}
	p, err = getRulePresetTx(ctx, tx, p.UserID, p.AdminAccountID, p.ID)
	if err != nil {
		return p, err
	}
	refs, err := tx.Query(ctx, `SELECT id,name FROM connection_health_policies WHERE user_id=$1 AND admin_account_id=$2 AND (rule_preset_id=$3 OR legacy_preset_id=$3) ORDER BY created_at,id`, p.UserID, p.AdminAccountID, p.ID)
	if err != nil {
		return p, err
	}
	for refs.Next() {
		var ref RulePresetPolicy
		if err := refs.Scan(&ref.ID, &ref.Name); err != nil {
			refs.Close()
			return p, err
		}
		p.Policies = append(p.Policies, ref)
	}
	err = refs.Err()
	refs.Close()
	if err != nil {
		return p, err
	}
	if err := tx.Commit(ctx); err != nil {
		return p, err
	}
	return p, nil
}
func (r *Repository) DeleteRulePreset(ctx context.Context, userID, workspace, id string) error {
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return err
	}
	p, err := getRulePresetTx(ctx, tx, userID, workspace, id)
	if err != nil {
		return err
	}
	if p.Kind != PresetCustom {
		return requestError(ErrorPresetBuiltIn)
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_policies WHERE user_id=$1 AND admin_account_id=$2 AND (rule_preset_id=$3 OR legacy_preset_id=$3))`, userID, workspace, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return requestError(ErrorPresetInUse)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM connection_health_rule_presets WHERE id=$1 AND user_id=$2 AND admin_account_id=$3`, id, userID, workspace); err != nil {
		return err
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, userID, workspace); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) ApplyRulePresetToAll(ctx context.Context, userID, workspace, id string) (WorkspaceHealthSettings, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if _, err := getRulePresetTx(ctx, tx, userID, workspace, id); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_health_policies SET rule_preset_id=$3,updated_at=clock_timestamp() WHERE user_id=$1 AND admin_account_id=$2`, userID, workspace, id); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, userID, workspace); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	s, err := getWorkspaceHealthSettings(ctx, tx, userID, workspace)
	if err != nil {
		return s, err
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) SwitchWorkspaceRule(ctx context.Context, userID, workspace, rule string) (WorkspaceHealthSettings, error) {
	if rule != RuleVersionV2 && rule != RuleVersionLegacy {
		return WorkspaceHealthSettings{}, requestError(ErrorRequest)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if rule == RuleVersionLegacy {
		if _, err := tx.Exec(ctx, `UPDATE connection_health_policies SET rule_preset_id=CASE WHEN legacy_preset_id='' THEN $3 ELSE legacy_preset_id END WHERE user_id=$1 AND admin_account_id=$2`, userID, workspace, builtinPresetID(PresetLegacyDefault, userID, workspace)); err != nil {
			return WorkspaceHealthSettings{}, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT connection_health_convert_states($1,$2,$3)`, userID, workspace, rule); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_health_workspace_settings SET rule_version=$3,rule_switched_at=clock_timestamp(),updated_at=clock_timestamp(),config_generation=config_generation+1 WHERE user_id=$1 AND admin_account_id=$2`, userID, workspace, rule); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	s, err := getWorkspaceHealthSettings(ctx, tx, userID, workspace)
	if err != nil {
		return s, err
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) SaveWorkspaceProbeConcurrency(ctx context.Context, userID, workspace string, cap int, version int64) (WorkspaceHealthSettings, error) {
	if cap < 1 || cap > 10 {
		return WorkspaceHealthSettings{}, requestError(ErrorRequest)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return WorkspaceHealthSettings{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE connection_health_workspace_settings SET probe_concurrency=$3,probe_concurrency_version=probe_concurrency_version+1,updated_at=clock_timestamp() WHERE user_id=$1 AND admin_account_id=$2 AND probe_concurrency_version=$4`, userID, workspace, cap, version)
	if err != nil {
		return WorkspaceHealthSettings{}, err
	}
	if tag.RowsAffected() != 1 {
		return WorkspaceHealthSettings{}, requestError(ErrorSettingsConflict)
	}
	s, err := getWorkspaceHealthSettings(ctx, tx, userID, workspace)
	if err != nil {
		return s, err
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) attachPolicyRules(ctx context.Context, policies []Policy, settings map[string]WorkspaceHealthSettings) error {
	presets := map[string][]RulePreset{}
	for i := range policies {
		p := &policies[i]
		key := p.UserID + "\x00" + p.AdminAccountID
		s, ok := settings[key]
		if !ok {
			s = defaultWorkspaceHealthSettings(p.UserID, p.AdminAccountID)
		}
		p.RuleVersion = s.RuleVersion
		p.ConfigGeneration = s.ConfigGeneration
		if _, ok := presets[key]; !ok {
			rows, err := r.ListRulePresets(ctx, p.UserID, p.AdminAccountID)
			if err != nil {
				return err
			}
			presets[key] = rows
		}
		preset := DefaultRulePreset()
		for _, candidate := range presets[key] {
			if candidate.ID == p.RulePresetID {
				preset = candidate
				break
			}
		}
		p.RulePreset = &preset
	}
	return nil
}

func preparePolicyPreset(ctx context.Context, q policyExecutor, p *Policy) error {
	reader, ok := q.(stateQuerier)
	if !ok {
		return requestError(ErrorRequest)
	}
	if p.RulePresetID == "" {
		err := reader.QueryRow(ctx, `SELECT rule_preset_id FROM connection_health_policies WHERE id=$1 AND user_id=$2 AND admin_account_id=$3`, p.ID, p.UserID, p.AdminAccountID).Scan(&p.RulePresetID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if p.RulePresetID == "" {
			p.RulePresetID = builtinPresetID(PresetRecommended, p.UserID, p.AdminAccountID)
		}
	}
	var valid bool
	if err := reader.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connection_health_rule_presets WHERE id=$1 AND user_id=$2 AND admin_account_id=$3)`, p.RulePresetID, p.UserID, p.AdminAccountID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return requestError(ErrorNotFound)
	}
	return nil
}

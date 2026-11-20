package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/shared/workspacescope"
)

var ErrDailyReportInvalid = errors.New("经营日报设置不正确；请检查每日时间和一个有效Telegram目标后保存。")
var ErrDailyReportWritesDisabled = errors.New("当前实例不提供这项操作，请在正常服务执行。")
var ErrDailyReportUnavailable = errors.New("经营日报设置的本地存储当前不可用；请检查本站数据库连接和启动日志后重试。")

type DailyReportConfigError struct{ Reason string }

func (e *DailyReportConfigError) Error() string        { return e.Reason }
func (e *DailyReportConfigError) Is(target error) bool { return target == ErrDailyReportInvalid }

func reportConfigError(reason string) error {
	return &DailyReportConfigError{Reason: reason}
}

type DailyReportConfig struct {
	Enabled bool     `json:"enabled"`
	Time    string   `json:"time"`
	BotIDs  []string `json:"botIds"`
}

type DailyReportPatch struct {
	Enabled json.RawMessage
	Time    json.RawMessage
	BotIDs  json.RawMessage
}

func (p DailyReportPatch) Present() bool {
	return len(p.Enabled) > 0 || len(p.Time) > 0 || len(p.BotIDs) > 0
}

func reportNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func DailyReportBotIDs(strategy StrategySettings) ([]string, error) {
	result := []string{}
	if len(strategy.DailyReportBotIDs) > 0 {
		if reportNull(strategy.DailyReportBotIDs) || json.Unmarshal(strategy.DailyReportBotIDs, &result) != nil {
			return nil, reportConfigError("已保存的日报机器人列表不是有效字符串数组；请重新选择一个机器人，或关闭后明确清空列表再保存。")
		}
	}
	if len(result) > 1 {
		return nil, reportConfigError("本轮日报只允许一个Telegram机器人；请只保留一个目标后保存。")
	}
	if len(result) == 1 && (result[0] == "" || utf8.RuneCountInString(result[0]) > 128) {
		return nil, reportConfigError("日报机器人编号为空或超过128字；请重新选择已保存的可用机器人。")
	}
	return result, nil
}

func ParseDailyReportConfig(strategy StrategySettings) (DailyReportConfig, error) {
	result := DailyReportConfig{Time: "08:00", BotIDs: []string{}}
	if len(strategy.DailyReportEnabled) > 0 {
		if reportNull(strategy.DailyReportEnabled) || json.Unmarshal(strategy.DailyReportEnabled, &result.Enabled) != nil {
			return result, reportConfigError("已保存的日报开关不是有效布尔值；请明确选择开启或关闭后保存。")
		}
	}
	if len(strategy.DailyReportTime) > 0 {
		if reportNull(strategy.DailyReportTime) || json.Unmarshal(strategy.DailyReportTime, &result.Time) != nil {
			return result, reportConfigError("已保存的日报时间不是有效文本；请填写00:00至23:59的每日业务时间后保存。")
		}
	}
	if len(result.Time) != 5 || result.Time[2] != ':' {
		return result, reportConfigError("已保存的日报时间不是HH:mm格式；请填写00:00至23:59后保存，不会自动改为08:00。")
	}
	if _, err := time.Parse("15:04", result.Time); err != nil {
		return result, reportConfigError("已保存的日报时间超出00:00至23:59；请修正后保存。")
	}
	ids, err := DailyReportBotIDs(strategy)
	if err != nil {
		return result, err
	}
	result.BotIDs = ids
	return result, nil
}

// DailyReportStrategy reads only the existing row. Locked callers own their transaction.
// It never creates a default enabled row, projects recipients or generates IDs.
func DailyReportStrategy(ctx context.Context, reader telegramTargetReader, userID, workspaceID string, lock bool) (StrategySettings, bool, error) {
	statement := `SELECT settings FROM strategy_settings WHERE user_id=$1 AND admin_account_id=$2`
	if lock {
		statement += " FOR SHARE"
	}
	var raw []byte
	cfg := DefaultStrategySettings()
	err := reader.QueryRow(ctx, statement, userID, workspaceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, true, err
	}
	return cfg, true, nil
}

// Legacy saves merge only the original known fields and preserve report and unknown JSON.
type preservingStrategyRepository interface {
	SaveStrategyPreservingReport(context.Context, string, string, StrategySettings) (StrategySettings, error)
}

var legacyStrategyKeys = []string{
	"balanceNotifyRecipientsInvalid", "multiplierNotifyRecipientsInvalid", "enableRefreshInterval", "refreshInterval",
	"enableBalanceWarning", "defaultBalanceThreshold", "balanceNotifyBotIds", "balanceTemplate", "balanceTemplateFormat",
	"enableMultiplierAlert", "multiplierNotifyBotIds", "multiplierTemplate", "multiplierTemplateFormat", "enableAutoChangeMultiplier",
}

func mergeStrategyJSON(original []byte, legacy StrategySettings, patch DailyReportPatch) ([]byte, StrategySettings, error) {
	encoded, err := json.Marshal(legacy)
	if err != nil {
		return nil, StrategySettings{}, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, StrategySettings{}, err
	}
	// Pure legacy forms still replace every original field, including omitted false warning flags.
	fields := make(map[string]json.RawMessage, len(legacyStrategyKeys))
	for _, key := range legacyStrategyKeys {
		fields[key] = values[key]
	}
	return mergeStrategyFieldsJSON(original, fields, patch)
}

func mergeStrategyFieldsJSON(original []byte, fields map[string]json.RawMessage, patch DailyReportPatch) ([]byte, StrategySettings, error) {
	root := map[string]json.RawMessage{}
	if len(original) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return nil, StrategySettings{}, err
		}
		if root == nil {
			root = map[string]json.RawMessage{}
		}
	}
	for _, key := range legacyStrategyKeys {
		if value, present := fields[key]; present {
			if len(value) == 0 {
				delete(root, key)
			} else {
				root[key] = value
			}
		}
	}
	if len(patch.Enabled) > 0 {
		root["dailyReportEnabled"] = patch.Enabled
	}
	if len(patch.Time) > 0 {
		root["dailyReportTime"] = patch.Time
	}
	if len(patch.BotIDs) > 0 {
		root["dailyReportBotIds"] = patch.BotIDs
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, StrategySettings{}, err
	}
	merged := DefaultStrategySettings()
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, StrategySettings{}, err
	}
	return encoded, merged, nil
}

// Only submitted old fields are legalized. Recipient changes retain their original
// derived warning/disable behavior; unrelated old JSON is never rewritten.
func normalizeLegacyStrategyPatch(original []byte, fields map[string]json.RawMessage, targets []TelegramTarget) (map[string]json.RawMessage, error) {
	if len(fields) == 0 {
		return fields, nil
	}
	_, candidate, err := mergeStrategyFieldsJSON(original, fields, DailyReportPatch{})
	if err != nil {
		return nil, err
	}
	// Explicit legacy edits retain the original save-time invalid-recipient calculation.
	candidate.BalanceNotifyRecipientsInvalid = false
	candidate.MultiplierNotifyRecipientsInvalid = false
	projected := projectStrategyWithTargets(normalizeStrategySettings(candidate), targets)
	raw, err := json.Marshal(projected)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	normalized := make(map[string]json.RawMessage, len(fields))
	for _, key := range legacyStrategyKeys {
		if _, present := fields[key]; present {
			normalized[key] = values[key]
		}
	}
	if _, present := fields["balanceNotifyBotIds"]; present {
		normalized["balanceNotifyRecipientsInvalid"] = values["balanceNotifyRecipientsInvalid"]
		if !projected.EnableBalanceWarning {
			normalized["enableBalanceWarning"] = values["enableBalanceWarning"]
		}
	}
	if _, present := fields["multiplierNotifyBotIds"]; present {
		normalized["multiplierNotifyRecipientsInvalid"] = values["multiplierNotifyRecipientsInvalid"]
		if !projected.EnableMultiplierAlert {
			normalized["enableMultiplierAlert"] = values["enableMultiplierAlert"]
		}
	}
	return normalized, nil
}

func readStrategyForUpdate(ctx context.Context, tx pgx.Tx, userID, workspaceID string) ([]byte, error) {
	// A save already creates a row. Create it inside this transaction before locking so
	// concurrent first saves cannot overwrite report fields inserted after a no-row read.
	if _, err := tx.Exec(ctx, `INSERT INTO strategy_settings(user_id,admin_account_id,settings,updated_at)
        VALUES($1,$2,'{}'::jsonb,now()) ON CONFLICT(user_id,admin_account_id) DO NOTHING`, userID, workspaceID); err != nil {
		return nil, err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT settings FROM strategy_settings WHERE user_id=$1 AND admin_account_id=$2 FOR UPDATE`, userID, workspaceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return []byte("{}"), nil
	}
	return raw, err
}

func writeStrategyJSON(ctx context.Context, tx pgx.Tx, userID, workspaceID string, raw []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO strategy_settings(user_id,admin_account_id,settings,updated_at)
        VALUES($1,$2,$3::jsonb,now()) ON CONFLICT(user_id,admin_account_id)
        DO UPDATE SET settings=excluded.settings,updated_at=excluded.updated_at`, userID, workspaceID, string(raw))
	return err
}

func (r *Repository) SaveStrategyPreservingReport(ctx context.Context, userID, workspaceID string, legacy StrategySettings) (StrategySettings, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return StrategySettings{}, err
	}
	defer tx.Rollback(ctx)
	original, err := readStrategyForUpdate(ctx, tx, userID, workspaceID)
	if err != nil {
		return StrategySettings{}, err
	}
	raw, merged, err := mergeStrategyJSON(original, legacy, DailyReportPatch{})
	if err != nil {
		return StrategySettings{}, err
	}
	if err := writeStrategyJSON(ctx, tx, userID, workspaceID, raw); err != nil {
		return StrategySettings{}, err
	}
	if err := workspacescope.Commit(ctx, tx); err != nil {
		return StrategySettings{}, err
	}
	return merged, nil
}

// projectStrategyWithTargets preserves the original pure recipient/template projection.
// Only IDs are needed; disabled bots remain valid references for legacy alerts.
func projectStrategyWithTargets(strategy StrategySettings, targets []TelegramTarget) StrategySettings {
	channels := NotificationChannelSettings{Telegram: []TelegramChannelSettings{}}
	for _, target := range targets {
		channels.Telegram = append(channels.Telegram, TelegramChannelSettings{ID: strings.TrimSpace(target.ID)})
	}
	strategy.BalanceTemplate = projectDefaultBalanceTemplate(strategy.BalanceTemplate)
	if len(strategy.BalanceNotifyBotIDs) > 0 {
		original := strategy.BalanceNotifyBotIDs
		strategy.BalanceNotifyBotIDs = projectRecipientIDs(original, channels)
		strategy.BalanceNotifyRecipientsInvalid = hasInvalidRecipientIDs(original, channels)
		if strategy.BalanceNotifyRecipientsInvalid && len(strategy.BalanceNotifyBotIDs) == 0 {
			strategy.EnableBalanceWarning = false
		}
	}
	if len(strategy.MultiplierNotifyBotIDs) > 0 {
		original := strategy.MultiplierNotifyBotIDs
		strategy.MultiplierNotifyBotIDs = projectRecipientIDs(original, channels)
		strategy.MultiplierNotifyRecipientsInvalid = hasInvalidRecipientIDs(original, channels)
		if strategy.MultiplierNotifyRecipientsInvalid && len(strategy.MultiplierNotifyBotIDs) == 0 {
			strategy.EnableMultiplierAlert = false
		}
	}
	return strategy
}

func (s *Service) SaveStrategyWithDailyReport(ctx context.Context, scope workspacescope.Scope, legacyPatch map[string]json.RawMessage, patch DailyReportPatch) (StrategySettings, error) {
	if s.dailyReportWritesDisabled {
		return StrategySettings{}, ErrDailyReportWritesDisabled
	}
	if s.repository == nil || s.repository.db == nil {
		return StrategySettings{}, ErrDailyReportUnavailable
	}
	var merged, runtimeStrategy StrategySettings
	refreshChanged := false
	err := workspacescope.Transaction(ctx, s.repository.db, scope, true, func(tx pgx.Tx) error {
		targets, err := s.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, true)
		if err != nil {
			return err
		}
		original, err := readStrategyForUpdate(ctx, tx, scope.UserID, scope.AdminAccountID)
		if err != nil {
			return err
		}
		before := DefaultStrategySettings()
		if err := json.Unmarshal(original, &before); err != nil {
			return err
		}
		before = normalizeStrategySettings(before)
		fields, err := normalizeLegacyStrategyPatch(original, legacyPatch, targets)
		if err != nil {
			return err
		}
		raw, value, err := mergeStrategyFieldsJSON(original, fields, patch)
		if err != nil {
			return err
		}
		cfg, err := ParseDailyReportConfig(value)
		if err != nil {
			return err
		}
		if cfg.Enabled {
			if _, err := SelectTelegramTarget(targets, cfg.BotIDs); err != nil {
				return err
			}
		}
		if err := writeStrategyJSON(ctx, tx, scope.UserID, scope.AdminAccountID, raw); err != nil {
			return err
		}
		runtimeStrategy = normalizeStrategySettings(value)
		refreshChanged = before.EnableRefreshInterval != runtimeStrategy.EnableRefreshInterval || before.RefreshInterval != runtimeStrategy.RefreshInterval
		// Omitted reminder fields use the same read projection as GET without rewriting JSON.
		merged = projectStrategyWithTargets(value, targets)
		// Explicit recipient edits keep their existing immediate save-time warning.
		if _, present := legacyPatch["balanceNotifyBotIds"]; present {
			merged.BalanceNotifyRecipientsInvalid = value.BalanceNotifyRecipientsInvalid
		}
		if _, present := legacyPatch["multiplierNotifyBotIds"]; present {
			merged.MultiplierNotifyRecipientsInvalid = value.MultiplierNotifyRecipientsInvalid
		}
		return nil
	})
	if err != nil {
		return StrategySettings{}, err
	}
	if refreshChanged && s.OnStrategyChanged != nil {
		s.OnStrategyChanged(scope.UserID, scope.AdminAccountID, runtimeStrategy)
	}
	return merged, nil
}

func (s *Service) SetDailyReportWritesDisabled(disabled bool) {
	s.dailyReportWritesDisabled = disabled
}

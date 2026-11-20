package vip

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrInvalidSettings = errors.New("大户关注设置超出允许范围；请检查推荐人数、提醒阈值、单个机器人及两段500字以内的话术。")
var ErrNotificationUnavailable = errors.New("通知服务当前不可用，本次没有保存；请检查服务启动日志后重试。")

type Service struct {
	repo     *Repository
	notifier *settings.Service
	apiOnly  bool
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
}

func NewService(repo *Repository, notifier *settings.Service, apiOnly bool) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, notifier: notifier, apiOnly: apiOnly, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func shift(day string, days int) string {
	start, _, _ := businesstime.Bounds(day)
	return start.AddDate(0, 0, days).Format("2006-01-02")
}

func covers(state syncSnapshot, from, to string) bool {
	return state.From != "" && state.To != "" && state.From <= from && state.To >= to
}

func classify(row *Row, cfg Settings, state syncSnapshot, today string) {
	if covers(state, shift(today, -7), shift(today, -1)) {
		average := row.Cost7 / 7
		row.DailyAverage = &average
		if row.Balance != nil && average > 0 {
			days := math.Max(*row.Balance, 0) / average
			if !math.IsNaN(days) && !math.IsInf(days, 0) {
				row.DaysLeft = &days
			}
		}
	}
	if covers(state, shift(today, -14), shift(today, -8)) {
		previous := row.CostPrevious7 / 7
		row.PreviousAverage = &previous
	}
	if row.DailyAverage != nil && row.PreviousAverage != nil && *row.PreviousAverage > 0 {
		trend := *row.DailyAverage / *row.PreviousAverage - 1
		if !math.IsNaN(trend) && !math.IsInf(trend, 0) {
			row.Trend = &trend
		}
	}
	row.Status = "normal"
	switch {
	case row.UserStatus == "disabled":
		row.Status = "disabled"
	case row.Balance != nil && *row.Balance <= 0:
		row.Status = "empty"
	case row.Requests7 > 0 &&
		((cfg.AlertBalance > 0 && row.Balance != nil && *row.Balance < cfg.AlertBalance) ||
			(cfg.AlertDays > 0 && row.DaysLeft != nil && *row.DaysLeft <= cfg.AlertDays)):
		row.Status = "low"
	case row.DailyAverage != nil && row.PreviousAverage != nil && *row.PreviousAverage >= 1 && *row.DailyAverage < *row.PreviousAverage*.5:
		row.Status = "declining"
	}
}

func syncPauseReason(state syncSnapshot, today string, now time.Time) string {
	reason := ""
	switch {
	case state.Status == "running":
		reason = "用户分析仍在同步，尚不能确认本轮完整资料"
	case state.Status == "failed":
		reason = "最近一次用户分析同步失败"
	case state.Status != "success":
		reason = "用户分析尚未完整同步成功"
	case state.StartedAt == nil || state.SyncedAt == nil:
		reason = "用户分析缺少本轮开始时间或完整成功时间"
	case state.SyncedAt.Before(*state.StartedAt):
		reason = "用户分析的完整成功时间早于本轮开始时间"
	case state.SyncedAt.After(now):
		reason = "用户分析记录的成功时间晚于当前时间，请核查服务时钟"
	case now.Sub(*state.SyncedAt) > 2*time.Hour:
		reason = "用户分析资料已超过2小时"
	case !covers(state, shift(today, -14), shift(today, -1)):
		reason = "尚无连续14个完整日的已核实用量"
	}
	if reason == "" {
		return ""
	}
	return "提醒已暂停：" + reason + "；请到用户分析核查并重新同步，名单仍显示已知资料。"
}

func coherent(state syncSnapshot, row Row, today string, now time.Time) bool {
	return syncPauseReason(state, today, now) == "" && row.ProfilePresent && row.ObservedAt != nil &&
		row.ObservedAt.Equal(*state.StartedAt) && row.UserStatus == "active" && row.Role == "user"
}

func (s *Service) readSummary(ctx context.Context, scope Scope) (Summary, syncSnapshot, error) {
	tx, err := s.repo.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE user_id=$1 AND id=$2`, scope.UserID, scope.AdminAccountID).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && created.Format(time.RFC3339Nano) != scope.CreatedAt) {
		return Summary{}, syncSnapshot{}, workspacescope.ErrChanged
	}
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	cfg, _, err := readSettings(ctx, tx, scope, false)
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	state, err := readSync(ctx, tx, scope)
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	targets := []settings.TelegramTarget{}
	if s.notifier != nil {
		targets, err = s.notifier.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, false)
		if err != nil {
			return Summary{}, syncSnapshot{}, err
		}
	}
	today := businesstime.Today()
	rows, err := readRows(ctx, tx, scope, cfg, state, today)
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	var dismissed int
	err = tx.QueryRow(ctx, `SELECT count(*)::int FROM vip_watch WHERE user_id=$1 AND admin_account_id=$2 AND kind='dismissed'`, scope.UserID, scope.AdminAccountID).Scan(&dismissed)
	if err != nil {
		return Summary{}, syncSnapshot{}, err
	}
	summary := Summary{
		Settings: cfg, Items: rows, LastSyncedAt: state.SyncedAt, DismissedCount: dismissed, Bots: []BotOption{},
		Coverage: Coverage{From: state.From, To: state.To, SevenDays: covers(state, shift(today, -7), shift(today, -1)), FourteenDays: covers(state, shift(today, -14), shift(today, -1)), Warnings: []string{}},
	}
	if state.Warning != "" {
		summary.Coverage.Warnings = strings.Split(state.Warning, "\n")
	}
	for _, target := range targets {
		if target.ID == "" {
			continue
		}
		summary.Bots = append(summary.Bots, BotOption{target.ID, target.Name, target.Enabled})
	}
	summary.Counts.All = len(rows)
	for _, row := range rows {
		switch row.Status {
		case "low":
			summary.Counts.Low++
		case "empty":
			summary.Counts.Empty++
		case "declining":
			summary.Counts.Declining++
		case "disabled":
			summary.Counts.Disabled++
		default:
			summary.Counts.Normal++
		}
	}
	if cfg.AlertsEnabled {
		if _, err := settings.SelectTelegramTarget(targets, cfg.BotIDs); err != nil {
			summary.PauseReason = err.Error()
		} else {
			summary.PauseReason = syncPauseReason(state, today, time.Now())
		}
	}
	return summary, state, nil
}

func (s *Service) Summary(ctx context.Context, scope Scope) (Summary, error) {
	result, _, err := s.readSummary(ctx, scope)
	return result, err
}

type settingsValidationError struct{ reason string }

func (e *settingsValidationError) Error() string        { return e.reason }
func (e *settingsValidationError) Is(target error) bool { return target == ErrInvalidSettings }
func validateSettings(cfg Settings) error {
	reason := ""
	switch {
	case cfg.DailyTop < 0 || cfg.DailyTop > 100:
		reason = "近7日日均推荐人数必须是0至100的整数；请修正后保存。"
	case cfg.LifetimeTop < 0 || cfg.LifetimeTop > 100:
		reason = "已记录累计推荐人数必须是0至100的整数；请修正后保存。"
	case math.IsNaN(cfg.AlertDays) || math.IsInf(cfg.AlertDays, 0) || cfg.AlertDays < 0 || cfg.AlertDays > 30:
		reason = "预计天数阈值必须是0至30的有限数值；请修正后保存。"
	case math.IsNaN(cfg.AlertBalance) || math.IsInf(cfg.AlertBalance, 0) || cfg.AlertBalance < 0 || cfg.AlertBalance > 1000000:
		reason = "余额阈值必须是0至1000000的有限数值；请修正后保存。"
	case len(cfg.BotIDs) > 1:
		reason = "本轮提醒只允许一个Telegram机器人；请只保留一个目标再保存。"
	case len(cfg.BotIDs) == 1 && (cfg.BotIDs[0] == "" || utf8.RuneCountInString(cfg.BotIDs[0]) > 128):
		reason = "机器人编号为空或超过128字；请重新选择可用机器人后保存。"
	case utf8.RuneCountInString(cfg.ReminderText) > 500:
		reason = "快用完时复制的话术超过500字；请缩短后保存。"
	case utf8.RuneCountInString(cfg.EmptyReminderText) > 500:
		reason = "已用完时复制的话术超过500字；请缩短后保存。"
	}
	if reason != "" {
		return &settingsValidationError{reason: reason}
	}
	return nil
}

func (s *Service) SaveSettings(ctx context.Context, scope Scope, cfg Settings) error {
	if err := validateSettings(cfg); err != nil {
		return err
	}
	return workspacescope.Transaction(ctx, s.repo.db, scope, true, func(tx pgx.Tx) error {
		if s.notifier == nil {
			return ErrNotificationUnavailable
		}
		targets, err := s.notifier.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, true)
		if err != nil {
			return err
		}
		if cfg.AlertsEnabled {
			if _, err := settings.SelectTelegramTarget(targets, cfg.BotIDs); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
            INSERT INTO vip_settings(user_id,admin_account_id,daily_top,lifetime_top,alert_days,alert_balance,
                                     alerts_enabled,bot_ids,reminder_text,empty_reminder_text)
            VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
            ON CONFLICT(user_id,admin_account_id) DO UPDATE SET
                daily_top=excluded.daily_top,lifetime_top=excluded.lifetime_top,
                alert_days=excluded.alert_days,alert_balance=excluded.alert_balance,
                alerts_enabled=excluded.alerts_enabled,bot_ids=excluded.bot_ids,
                reminder_text=excluded.reminder_text,empty_reminder_text=excluded.empty_reminder_text
        `, scope.UserID, scope.AdminAccountID, cfg.DailyTop, cfg.LifetimeTop, cfg.AlertDays, cfg.AlertBalance,
			cfg.AlertsEnabled, cfg.BotIDs, cfg.ReminderText, cfg.EmptyReminderText)
		return err
	})
}

func (s *Service) Watch(ctx context.Context, scope Scope, id, note string, dismiss bool) error {
	return workspacescope.Transaction(ctx, s.repo.db, scope, true, func(tx pgx.Tx) error {
		var known bool
		err := tx.QueryRow(ctx, `
            SELECT EXISTS(SELECT 1 FROM user_activity_profiles WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3)
                OR EXISTS(SELECT 1 FROM vip_watch WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3)
        `, scope.UserID, scope.AdminAccountID, id).Scan(&known)
		if err != nil {
			return err
		}
		if !known {
			return ErrNotFound
		}
		if dismiss {
			_, err = tx.Exec(ctx, `
                INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind,note)
                VALUES($1,$2,$3,'dismissed','')
                ON CONFLICT(user_id,admin_account_id,site_user_id) DO UPDATE SET kind='dismissed'
            `, scope.UserID, scope.AdminAccountID, id)
		} else {
			_, err = tx.Exec(ctx, `
                INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind,note)
                VALUES($1,$2,$3,'manual',$4)
                ON CONFLICT(user_id,admin_account_id,site_user_id) DO UPDATE SET kind='manual',note=excluded.note
            `, scope.UserID, scope.AdminAccountID, id, note)
		}
		return err
	})
}

func (s *Service) Restore(ctx context.Context, scope Scope) (int, error) {
	restored := 0
	err := workspacescope.Transaction(ctx, s.repo.db, scope, true, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `DELETE FROM vip_watch WHERE user_id=$1 AND admin_account_id=$2 AND kind='dismissed'`, scope.UserID, scope.AdminAccountID)
		if err == nil {
			restored = int(result.RowsAffected())
		}
		return err
	})
	return restored, err
}

func displayName(row Row) string {
	if row.Email != "" {
		return row.Email
	}
	if row.Username != "" {
		return row.Username
	}
	return "#" + row.SiteUserID
}

func amount(value *float64) string {
	if value == nil {
		return "未核实"
	}
	return fmt.Sprintf("%.2f", *value)
}

func Reminder(row Row, cfg Settings) string {
	text := cfg.ReminderText
	if text == "" {
		text = "您好，您的余额约为{balance}。根据最近使用情况，可用时间为{days}；请按需补充余额。"
	}
	if row.Status == "empty" {
		text = cfg.EmptyReminderText
		if text == "" {
			text = "您好，您的余额已经用完，请检查账户并按需补充余额。"
		}
	}
	days := "暂无法估计"
	if row.DaysLeft != nil {
		days = fmt.Sprintf("%.1f天", *row.DaysLeft)
	}
	return strings.NewReplacer("{username}", displayName(row), "{email}", row.Email, "{balance}", amount(row.Balance), "{days}", days).Replace(text)
}

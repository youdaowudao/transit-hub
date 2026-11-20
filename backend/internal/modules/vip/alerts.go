package vip

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

type attempt struct {
	Scope      Scope
	SiteUserID string
	State      string
	At         time.Time
	Previous   *alertState
	Target     settings.TelegramTarget
	Text       string
}

// A state is not repeated just because another day elapsed. A new episode needs recovery.
func shouldNotify(state string, previous *alertState, now time.Time) bool {
	if state != "low" && state != "empty" {
		return false
	}
	if previous == nil {
		return true
	}
	if now.Before(previous.At) {
		return false
	}
	if previous.Recovered {
		return now.Sub(previous.At) >= 24*time.Hour
	}
	return previous.Delivery == settings.DeliverySent && previous.State == "low" && state == "empty"
}

func concise(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func alertText(row Row) string {
	state := "正常"
	switch row.Status {
	case "low":
		state = "快用完"
	case "empty":
		state = "余额已用完"
	case "declining":
		state = "消费下降"
	case "disabled":
		state = "停用"
	}
	days := "未核实"
	if row.DaysLeft != nil {
		days = fmt.Sprintf("%.1f天", *row.DaysLeft)
	}
	observed := "未核实"
	if row.ObservedAt != nil {
		observed = row.ObservedAt.In(businesstime.Location()).Format("2006-01-02 15:04:05")
	}
	lines := []string{
		"大户余额提醒（仅发给站长）",
		"客户：" + concise(displayName(row), 120),
		"状态：" + state,
		"已保存余额：" + amount(row.Balance),
		"近7个完整日日均：" + amount(row.DailyAverage),
		"预计可用时间：" + days,
		"资料观察时间：" + observed + "（" + businesstime.Timezone + "）",
	}
	if row.Source.Note != "" {
		lines = append(lines, "备注："+concise(row.Source.Note, 200))
	}
	lines = append(lines, "只依据本站已同步资料，不会联系客户或修改主站。")
	return strings.Join(lines, "\n")
}

func (s *Service) prepare(ctx context.Context, conn *pgxpool.Conn, scope Scope, draft Row, text string) (*attempt, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := workspacescope.Lock(ctx, tx, scope, false); err != nil {
		return nil, err
	}
	if s.notifier == nil {
		return nil, ErrNotificationUnavailable
	}
	// Keep the existing cleanup lock order: parent, channels, then VIP settings.
	targets, err := s.notifier.TelegramTargets(ctx, tx, scope.UserID, scope.AdminAccountID, true)
	if err != nil {
		return nil, err
	}
	cfg, exists, err := readSettings(ctx, tx, scope, true)
	if err != nil {
		return nil, err
	}
	if !exists || !cfg.AlertsEnabled {
		return nil, nil
	}
	target, err := settings.SelectTelegramTarget(targets, cfg.BotIDs)
	if err != nil {
		return nil, err
	}
	state, err := readSync(ctx, tx, scope)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	today := businesstime.DateAt(now)
	rows, err := readRows(ctx, tx, scope, cfg, state, today)
	if err != nil {
		return nil, err
	}
	var fresh *Row
	for i := range rows {
		if rows[i].SiteUserID == draft.SiteUserID {
			fresh = &rows[i]
			break
		}
	}
	if fresh == nil || !coherent(state, *fresh, today, now) || alertText(*fresh) != text {
		return nil, nil
	}
	previous, err := readAlert(ctx, tx, scope, fresh.SiteUserID)
	if err != nil {
		return nil, err
	}
	urgent := fresh.Status == "low" || fresh.Status == "empty"
	if !urgent {
		if previous != nil && !previous.Recovered {
			_, err = tx.Exec(ctx, `
                UPDATE vip_alert_states SET recovered=true
                WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3
                  AND notified_at=$4 AND state=$5 AND delivery_status=$6
            `, scope.UserID, scope.AdminAccountID, fresh.SiteUserID, previous.At, previous.State, previous.Delivery)
			if err != nil {
				return nil, err
			}
			if err := workspacescope.Commit(ctx, tx); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if fresh.Requests7 <= 0 || !shouldNotify(fresh.Status, previous, now) {
		return nil, nil
	}
	// PostgreSQL timestamps retain microsecond precision; this is also the conditional completion key.
	at := now.Truncate(time.Microsecond)
	_, err = tx.Exec(ctx, `
        INSERT INTO vip_alert_states(user_id,admin_account_id,site_user_id,state,notified_at,recovered,delivery_status)
        VALUES($1,$2,$3,$4,$5,false,'sending')
        ON CONFLICT(user_id,admin_account_id,site_user_id) DO UPDATE SET
            state=excluded.state,notified_at=excluded.notified_at,recovered=false,delivery_status='sending'
    `, scope.UserID, scope.AdminAccountID, fresh.SiteUserID, fresh.Status, at)
	if err != nil {
		return nil, err
	}
	if err := workspacescope.Commit(ctx, tx); err != nil {
		return nil, err
	}
	return &attempt{Scope: scope, SiteUserID: fresh.SiteUserID, State: fresh.Status, At: at, Previous: previous, Target: target, Text: text}, nil
}

func finish(ctx context.Context, db workspacescope.Beginner, item attempt, result settings.DeliveryResult) error {
	return workspacescope.Transaction(ctx, db, item.Scope, false, func(tx pgx.Tx) error {
		const condition = ` WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3
                              AND state=$4 AND notified_at=$5 AND delivery_status='sending'`
		args := []any{item.Scope.UserID, item.Scope.AdminAccountID, item.SiteUserID, item.State, item.At}
		var statement string
		switch result.Status {
		case settings.DeliverySent:
			statement = `UPDATE vip_alert_states SET delivery_status='sent'` + condition
		case settings.DeliveryFailed:
			if item.Previous == nil {
				statement = `DELETE FROM vip_alert_states` + condition
			} else {
				statement = `UPDATE vip_alert_states SET state=$6,notified_at=$7,recovered=$8,delivery_status=$9` + condition
				args = append(args, item.Previous.State, item.Previous.At, item.Previous.Recovered, item.Previous.Delivery)
			}
		default:
			statement = `UPDATE vip_alert_states SET delivery_status='unknown'` + condition
		}
		_, err := tx.Exec(ctx, statement, args...)
		return err
	})
}

func releaseWorkspaceLock(conn *pgxpool.Conn, scope Scope) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtext($1),hashtext($2))`, "vip-alert:"+scope.UserID, scope.AdminAccountID).Scan(&released)
	if err != nil || !released {
		_ = conn.Hijack().Close(ctx)
		return
	}
	conn.Release()
}

func (s *Service) alertWorkspace(ctx context.Context, scope Scope) error {
	summary, state, err := s.readSummary(ctx, scope)
	if err != nil {
		return err
	}
	if !summary.Settings.AlertsEnabled || summary.PauseReason != "" {
		return nil
	}
	type draft struct {
		row  Row
		text string
	}
	drafts := []draft{}
	today := businesstime.Today()
	now := time.Now()
	for _, row := range summary.Items {
		if coherent(state, row, today, now) {
			drafts = append(drafts, draft{row, alertText(row)})
		}
	}
	if len(drafts) == 0 {
		return nil
	}
	conn, err := s.repo.db.Acquire(ctx)
	if err != nil {
		return err
	}
	var locked bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1),hashtext($2))`, "vip-alert:"+scope.UserID, scope.AdminAccountID).Scan(&locked)
	if err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(closeCtx)
		return err
	}
	if !locked {
		conn.Release()
		return nil
	}
	defer releaseWorkspaceLock(conn, scope)
	for _, candidate := range drafts {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, err := s.prepare(ctx, conn, scope, candidate.row, candidate.text)
		if err != nil {
			return err
		}
		if item == nil {
			continue
		}
		result := s.notifier.SendFrozenTelegramWithResult(ctx, item.Target, item.Text, settings.NotificationTemplateFormatText)
		resultCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = finish(resultCtx, conn, *item, result)
		cancel()
		if result.Status != settings.DeliverySent {
			log.Printf("[vip] 站长提醒未确认送达 user_id=%s workspace_id=%s customer_id=%s reason=%s", scope.UserID, scope.AdminAccountID, item.SiteUserID, result.Reason)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Start() {
	if s.apiOnly || s.repo.db == nil {
		return
	}
	s.started = true
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				scopes, err := s.repo.AlertWorkspaces(s.ctx)
				if err != nil {
					log.Printf("[vip] 读取站长提醒工作区失败：%s", publicReason(err))
					continue
				}
				for _, scope := range scopes {
					if s.ctx.Err() != nil {
						return
					}
					if err := s.alertWorkspace(s.ctx, scope); err != nil && !errors.Is(err, workspacescope.ErrChanged) {
						log.Printf("[vip] 站长提醒检查未完成 user_id=%s workspace_id=%s reason=%s", scope.UserID, scope.AdminAccountID, publicReason(err))
					}
				}
			}
		}
	}()
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.cancel()
	if !s.started {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

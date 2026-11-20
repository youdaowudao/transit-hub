package user_activity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrUnavailable = errors.New("用户分析资料表尚未初始化，请先用正常服务完成升级；原功能可继续使用。")
var ErrBusy = errors.New("另一个工作区正在读取资料，本次没有开始；请稍后重新同步。")
var ErrNotFound = errors.New("当前工作区没有这位用户的本地资料；请检查编号或重新同步后再查看。")

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db} }
func (r *Repository) EnsureSchema(ctx context.Context) error {
	return migrations.EnsureUserActivitySchema(ctx, r.db)
}
func (r *Repository) Ready(ctx context.Context) error {
	if r.db == nil {
		return ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRow(ctx, `SELECT to_regclass('user_activity_profiles') IS NOT NULL
        AND to_regclass('user_activity_daily') IS NOT NULL
        AND to_regclass('user_activity_recharge_daily') IS NOT NULL
        AND to_regclass('user_activity_site_daily') IS NOT NULL
        AND to_regclass('user_activity_sync_states') IS NOT NULL`).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return ErrUnavailable
	}
	return nil
}

type rowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readSync(ctx context.Context, db rowReader, s Scope) (SyncState, error) {
	st := SyncState{Status: "idle", Warnings: []string{}}
	var warning string
	e := db.QueryRow(ctx, `SELECT status,
        error_detail,
        warning_detail,
        COALESCE(usage_from::text,
        ''),
        COALESCE(usage_to::text,
        ''),
        COALESCE(recharge_from::text,
        ''),
        COALESCE(recharge_to::text,
        ''),
        user_count,
        last_started_at,
        last_synced_at,
        today_usage_observed_at
        FROM user_activity_sync_states
        WHERE user_id=$1
        AND admin_account_id=$2`, s.UserID, s.AdminAccountID).Scan(&st.Status, &st.Error, &warning, &st.UsageFrom, &st.UsageTo, &st.RechargeFrom, &st.RechargeTo, &st.UserCount, &st.StartedAt, &st.LastSyncedAt, &st.TodayUsageObservedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return st, nil
	}
	if warning != "" {
		st.Warnings = strings.Split(warning, "\n")
	}
	return st, e
}
func (r *Repository) Sync(ctx context.Context, s Scope) (SyncState, error) {
	return readSync(ctx, r.db, s)
}
func nullableDate(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func writeSync(ctx context.Context, tx pgx.Tx, s Scope, st SyncState) error {
	_, e := tx.Exec(ctx, `INSERT INTO user_activity_sync_states(user_id,
        admin_account_id,
        status,
        error_detail,
        warning_detail,
        usage_from,
        usage_to,
        recharge_from,
        recharge_to,
        user_count,
        last_started_at,
        last_synced_at,
        today_usage_observed_at)
        VALUES($1,
        $2,
        $3,
        $4,
        $5,
        $6::date,
        $7::date,
        $8::date,
        $9::date,
        $10,
        $11,
        $12,
        $13)
        ON CONFLICT(user_id,
        admin_account_id)
        DO UPDATE SET status=excluded.status,
        error_detail=excluded.error_detail,
        warning_detail=excluded.warning_detail,
        usage_from=excluded.usage_from,
        usage_to=excluded.usage_to,
        recharge_from=excluded.recharge_from,
        recharge_to=excluded.recharge_to,
        user_count=excluded.user_count,
        last_started_at=excluded.last_started_at,
        last_synced_at=excluded.last_synced_at,
        today_usage_observed_at=excluded.today_usage_observed_at`, s.UserID, s.AdminAccountID, st.Status, st.Error, strings.Join(st.Warnings, "\n"), nullableDate(st.UsageFrom), nullableDate(st.UsageTo), nullableDate(st.RechargeFrom), nullableDate(st.RechargeTo), st.UserCount, st.StartedAt, st.LastSyncedAt, st.TodayUsageObservedAt)
	return e
}
func publish(ctx context.Context, db workspacescope.Beginner, s Scope, fn func(pgx.Tx) error) error {
	return workspacescope.Transaction(ctx, db, s, false, fn)
}
func publishInventory(ctx context.Context, db workspacescope.Beginner, s Scope, users []upstream.Sub2APIAdminUser, started time.Time) error {
	return publish(ctx, db, s, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(users))
		var balance float64
		complete := true
		for _, u := range users {
			ids = append(ids, u.ID)
			if u.Balance == nil {
				complete = false
			} else {
				balance += *u.Balance
			}
			_, e := tx.Exec(ctx, `INSERT INTO user_activity_profiles(user_id,
        admin_account_id,
        site_user_id,
        email,
        username,
        role,
        status,
        balance,
        site_created_at,
        last_used_at,
        first_observed_at,
        updated_at)
        VALUES($1,
        $2,
        $3,
        $4,
        $5,
        $6,
        $7,
        $8,
        $9,
        $10,
        $11,
        $11)
        ON CONFLICT(user_id,
        admin_account_id,
        site_user_id)
        DO UPDATE SET email=excluded.email,
        username=excluded.username,
        role=excluded.role,
        status=excluded.status,
        balance=excluded.balance,
        site_created_at=excluded.site_created_at,
        last_used_at=excluded.last_used_at,
        updated_at=excluded.updated_at`, s.UserID, s.AdminAccountID, u.ID, u.Email, u.Username, u.Role, u.Status, u.Balance, u.CreatedAt, u.LastUsedAt, started)
			if e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, `DELETE
        FROM user_activity_profiles
        WHERE user_id=$1
        AND admin_account_id=$2
        AND NOT(site_user_id=ANY($3::text[]))`, s.UserID, s.AdminAccountID, ids); e != nil {
			return e
		}
		var total any
		if complete {
			total = balance
		}
		_, e := tx.Exec(ctx, `INSERT INTO user_activity_site_daily(user_id,
        admin_account_id,
        day,
        total_balance,
        user_count,
        observed_at)
        VALUES($1,
        $2,
        $3::date,
        $4,
        $5,
        $6)
        ON CONFLICT(user_id,
        admin_account_id,
        day)
        DO UPDATE SET total_balance=excluded.total_balance,
        user_count=excluded.user_count,
        observed_at=excluded.observed_at`, s.UserID, s.AdminAccountID, businesstime.DateAt(started), total, len(users), started)
		return e
	})
}
func publishUsage(ctx context.Context, db workspacescope.Beginner, s Scope, day string, usage []DailyUsage, st SyncState) error {
	return publish(ctx, db, s, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `DELETE
        FROM user_activity_daily
        WHERE user_id=$1
        AND admin_account_id=$2
        AND day=$3::date`, s.UserID, s.AdminAccountID, day); e != nil {
			return e
		}
		for _, u := range usage {
			if _, e := tx.Exec(ctx, `INSERT INTO user_activity_daily(user_id,
        admin_account_id,
        site_user_id,
        day,
        requests,
        tokens,
        actual_cost)
        VALUES($1,
        $2,
        $3,
        $4::date,
        $5,
        $6,
        $7)`, s.UserID, s.AdminAccountID, u.SiteUserID, day, u.Requests, u.Tokens, u.Cost); e != nil {
				return e
			}
		}
		return writeSync(ctx, tx, s, st)
	})
}
func retainHistory(ctx context.Context, db workspacescope.Beginner, s Scope, today string, st SyncState) error {
	return publish(ctx, db, s, func(tx pgx.Tx) error {
		for _, table := range []string{"user_activity_daily", "user_activity_recharge_daily", "user_activity_site_daily"} {
			if _, e := tx.Exec(ctx, `DELETE
        FROM `+table+` WHERE user_id=$1 AND admin_account_id=$2 AND day<$3::date-399`, s.UserID, s.AdminAccountID, today); e != nil {
				return e
			}
		}
		return writeSync(ctx, tx, s, st)
	})
}
func (r *Repository) Due(ctx context.Context, runningOnly bool) ([]Scope, error) {
	rows, e := r.db.Query(ctx, `SELECT s.user_id,
        s.admin_account_id,
        a.created_at
        FROM user_activity_sync_states s JOIN admin_accounts a ON a.user_id=s.user_id
        AND a.id=s.admin_account_id
        WHERE ($1
        AND s.status='running') OR (NOT $1
        AND (s.last_started_at IS NULL OR s.last_started_at<=now()-interval '1 hour'))
        ORDER BY s.last_started_at NULLS FIRST,
        s.admin_account_id,
        s.user_id`, runningOnly)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Scope{}
	for rows.Next() {
		var s Scope
		var t time.Time
		if e := rows.Scan(&s.UserID, &s.AdminAccountID, &t); e != nil {
			return nil, e
		}
		s.CreatedAt = t.Format(time.RFC3339Nano)
		out = append(out, s)
	}
	return out, rows.Err()
}

package reports

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/shared/workspacescope"
)

var ErrUnavailable = errors.New("经营日报所需的用户分析或销售资料表尚未初始化，请先用正常服务完成升级；原功能可继续使用。")
var ErrStopped = errors.New("日报服务正在退出，本次没有开始发送；请在服务恢复后重新读取已保存配置。")

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	var ready bool
	err := r.db.QueryRow(ctx, `
        SELECT to_regclass('user_activity_profiles') IS NOT NULL
           AND to_regclass('user_activity_daily') IS NOT NULL
           AND to_regclass('user_activity_recharge_daily') IS NOT NULL
           AND to_regclass('user_activity_site_daily') IS NOT NULL
           AND to_regclass('user_activity_sync_states') IS NOT NULL
           AND to_regclass('sales_excluded_entries') IS NOT NULL
           AND to_regclass('sales_settings') IS NOT NULL
           AND to_regclass('dashboard_daily_stats') IS NOT NULL
           AND to_regclass('strategy_settings') IS NOT NULL
           AND to_regclass('notification_channel_settings') IS NOT NULL
    `).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return ErrUnavailable
	}
	return nil
}

func verifyParent(ctx context.Context, reader workspacescope.Reader, scope Scope) error {
	var created time.Time
	err := reader.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE user_id=$1 AND id=$2`, scope.UserID, scope.AdminAccountID).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && created.Format(time.RFC3339Nano) != scope.CreatedAt) {
		return workspacescope.ErrChanged
	}
	return err
}

func (r *Repository) ScheduledWorkspaces(ctx context.Context) ([]Scope, error) {
	rows, err := r.db.Query(ctx, `
        SELECT s.user_id,s.admin_account_id,a.created_at
        FROM strategy_settings s JOIN admin_accounts a ON a.user_id=s.user_id AND a.id=s.admin_account_id
        WHERE s.settings->'dailyReportEnabled'='true'::jsonb
        ORDER BY s.user_id,s.admin_account_id
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Scope{}
	for rows.Next() {
		var scope Scope
		var created time.Time
		if err := rows.Scan(&scope.UserID, &scope.AdminAccountID, &created); err != nil {
			return nil, err
		}
		scope.CreatedAt = created.Format(time.RFC3339Nano)
		result = append(result, scope)
	}
	return result, rows.Err()
}

type localActivity struct {
	UsageFrom       string
	UsageTo         string
	LastSyncedAt    *time.Time
	ObservedAt      *time.Time
	ProfileCount    int64
	OrdinaryUsers   int64
	UnknownBalances int64
	KnownBalance    float64
	UsageRows       int64
	Requests        int64
	Tokens          int64
	Cost            float64
	ActiveYesterday int64
	Cost7           float64
	CostPrevious7   float64
	Active7         int64
	Declining       int64
}

func (r *Repository) activity(ctx context.Context, scope Scope, day, today string) (localActivity, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return localActivity{}, err
	}
	defer tx.Rollback(ctx)
	if err := verifyParent(ctx, tx, scope); err != nil {
		return localActivity{}, err
	}
	var result localActivity
	err = tx.QueryRow(ctx, `SELECT COALESCE(usage_from::text,''),COALESCE(usage_to::text,''),last_synced_at
        FROM user_activity_sync_states WHERE user_id=$1 AND admin_account_id=$2`, scope.UserID, scope.AdminAccountID).Scan(
		&result.UsageFrom, &result.UsageTo, &result.LastSyncedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*),
        count(*) FILTER(WHERE role='user' AND status<>'disabled'),
        count(*) FILTER(WHERE role='user' AND status<>'disabled' AND balance IS NULL),
        COALESCE(sum(balance) FILTER(WHERE role='user' AND status<>'disabled'),0),
        max(updated_at) FROM user_activity_profiles WHERE user_id=$1 AND admin_account_id=$2`, scope.UserID, scope.AdminAccountID).Scan(
		&result.ProfileCount, &result.OrdinaryUsers, &result.UnknownBalances, &result.KnownBalance, &result.ObservedAt)
	if err != nil {
		return result, err
	}
	if result.ObservedAt == nil {
		// A completed empty inventory still has its original independent observation.
		err = tx.QueryRow(ctx, `SELECT observed_at FROM user_activity_site_daily
            WHERE user_id=$1 AND admin_account_id=$2 ORDER BY day DESC LIMIT 1`, scope.UserID, scope.AdminAccountID).Scan(&result.ObservedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
	}
	err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(requests),0)::bigint,COALESCE(sum(tokens),0)::bigint,
        COALESCE(sum(actual_cost),0)::float8,count(*) FILTER(WHERE requests>0)
        FROM user_activity_daily WHERE user_id=$1 AND admin_account_id=$2 AND day=$3::date`, scope.UserID, scope.AdminAccountID, day).Scan(
		&result.UsageRows, &result.Requests, &result.Tokens, &result.Cost, &result.ActiveYesterday)
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `
        WITH per_user AS (
            SELECT site_user_id,
                COALESCE(sum(actual_cost) FILTER(WHERE day>=$3::date-7 AND day<$3::date),0)::float8 AS cost_7,
                COALESCE(sum(actual_cost) FILTER(WHERE day>=$3::date-14 AND day<$3::date-7),0)::float8 AS previous_7,
                COALESCE(sum(requests) FILTER(WHERE day>=$3::date-7 AND day<$3::date),0)::bigint AS requests_7
            FROM user_activity_daily WHERE user_id=$1 AND admin_account_id=$2
              AND day>=$3::date-14 AND day<$3::date GROUP BY site_user_id
        )
        SELECT COALESCE(sum(d.cost_7),0)::float8,COALESCE(sum(d.previous_7),0)::float8,
               count(*) FILTER(WHERE d.requests_7>0),
               count(*) FILTER(WHERE d.previous_7>0 AND d.cost_7<=d.previous_7*0.5)
        FROM user_activity_profiles p LEFT JOIN per_user d ON d.site_user_id=p.site_user_id
        WHERE p.user_id=$1 AND p.admin_account_id=$2 AND p.role='user' AND p.status<>'disabled'
    `, scope.UserID, scope.AdminAccountID, today).Scan(&result.Cost7, &result.CostPrevious7, &result.Active7, &result.Declining)
	return result, err
}

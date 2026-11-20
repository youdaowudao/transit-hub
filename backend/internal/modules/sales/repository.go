package sales

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrUnavailable = errors.New("销售入账分析或用户分析资料表尚未初始化，请先用正常服务完成升级；原功能可继续使用。")

type Repository struct{ db *pgxpool.Pool }

type reader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) EnsureSchema(ctx context.Context) error {
	return migrations.EnsureSalesSchema(ctx, r.db)
}

func (r *Repository) Ready(ctx context.Context) error {
	if r.db == nil {
		return ErrUnavailable
	}
	var ready bool
	err := r.db.QueryRow(ctx, `
        SELECT to_regclass('sales_excluded_entries') IS NOT NULL
           AND to_regclass('sales_settings') IS NOT NULL
           AND to_regclass('user_activity_recharge_daily') IS NOT NULL
           AND to_regclass('user_activity_profiles') IS NOT NULL
           AND to_regclass('user_activity_site_daily') IS NOT NULL
           AND to_regclass('user_activity_sync_states') IS NOT NULL
    `).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return ErrUnavailable
	}
	return nil
}

// rechargeSource is the single filtering rule for summaries, management rows and reports.
// $1/$2 are always the authenticated user and captured workspace. Negative amounts keep
// their original sign; channel exclusion takes precedence over individual snapshots.
func rechargeSource() string {
	return `
        SELECT r.site_user_id,r.day,r.channel,r.amount AS raw_amount,r.events,
               CASE WHEN r.channel=ANY(COALESCE(s.excluded_channels,'{}'::text[])) THEN 0
                    WHEN r.amount>0 THEN GREATEST(r.amount-LEAST(r.amount,COALESCE(e.amount,0)),0)
                    ELSE r.amount END AS effective_amount,
               CASE WHEN r.amount<=0 THEN 0
                    WHEN r.channel=ANY(COALESCE(s.excluded_channels,'{}'::text[])) THEN r.amount
                    ELSE LEAST(r.amount,COALESCE(e.amount,0)) END AS excluded_amount,
               CASE WHEN r.channel=ANY(COALESCE(s.excluded_channels,'{}'::text[])) THEN r.events
                    WHEN r.amount<=0 THEN 0
                    ELSE LEAST(r.events,COALESCE(e.events,0)) END AS excluded_events,
               e.site_user_id IS NOT NULL AS has_exclusion,
               r.channel=ANY(COALESCE(s.excluded_channels,'{}'::text[])) AS channel_excluded
        FROM user_activity_recharge_daily r
        LEFT JOIN sales_excluded_entries e
          ON e.user_id=r.user_id AND e.admin_account_id=r.admin_account_id
         AND e.site_user_id=r.site_user_id AND e.day=r.day AND e.channel=r.channel
        LEFT JOIN sales_settings s ON s.user_id=r.user_id AND s.admin_account_id=r.admin_account_id
        WHERE r.user_id=$1 AND r.admin_account_id=$2
    `
}

func readSettings(ctx context.Context, db reader, scope Scope) (Settings, error) {
	cfg := Settings{ExcludedChannels: []string{}}
	err := db.QueryRow(ctx, `SELECT excluded_channels FROM sales_settings WHERE user_id=$1 AND admin_account_id=$2`, scope.UserID, scope.AdminAccountID).Scan(&cfg.ExcludedChannels)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{ExcludedChannels: []string{}}, nil
	}
	if cfg.ExcludedChannels == nil {
		cfg.ExcludedChannels = []string{}
	}
	return cfg, err
}

func readSync(ctx context.Context, db reader, scope Scope) (syncState, error) {
	var state syncState
	err := db.QueryRow(ctx, `
        SELECT COALESCE(recharge_from::text,''),COALESCE(recharge_to::text,''),last_synced_at,warning_detail
        FROM user_activity_sync_states WHERE user_id=$1 AND admin_account_id=$2
    `, scope.UserID, scope.AdminAccountID).Scan(&state.From, &state.To, &state.LastSyncedAt, &state.Warning)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	return state, err
}

func (r *Repository) snapshot(ctx context.Context, scope Scope, fn func(pgx.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE user_id=$1 AND id=$2`, scope.UserID, scope.AdminAccountID).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && created.Format(time.RFC3339Nano) != scope.CreatedAt) {
		return workspacescope.ErrChanged
	}
	if err != nil {
		return err
	}
	return fn(tx)
}

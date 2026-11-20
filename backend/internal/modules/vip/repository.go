package vip

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrUnavailable = errors.New("大户关注或用户分析资料表尚未初始化，请先用正常服务完成升级；原功能可继续使用。")
var ErrNotFound = errors.New("当前工作区没有这位客户的本地资料或关注记录；请重新搜索客户后操作，不会向主站补查。")

type Repository struct{ db *pgxpool.Pool }

type reader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) EnsureSchema(ctx context.Context) error {
	return migrations.EnsureVIPSchema(ctx, r.db)
}

func (r *Repository) Ready(ctx context.Context) error {
	if r.db == nil {
		return ErrUnavailable
	}
	var ready bool
	err := r.db.QueryRow(ctx, `
        SELECT to_regclass('vip_watch') IS NOT NULL
           AND to_regclass('vip_settings') IS NOT NULL
           AND to_regclass('vip_alert_states') IS NOT NULL
           AND to_regclass('user_activity_profiles') IS NOT NULL
           AND to_regclass('user_activity_daily') IS NOT NULL
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

func readSettings(ctx context.Context, db reader, scope Scope, lock bool) (Settings, bool, error) {
	cfg := DefaultSettings()
	statement := `
        SELECT daily_top, lifetime_top, alert_days, alert_balance,
               alerts_enabled, bot_ids, reminder_text, empty_reminder_text
        FROM vip_settings WHERE user_id=$1 AND admin_account_id=$2
    `
	if lock {
		statement += " FOR SHARE"
	}
	err := db.QueryRow(ctx, statement, scope.UserID, scope.AdminAccountID).Scan(
		&cfg.DailyTop, &cfg.LifetimeTop, &cfg.AlertDays, &cfg.AlertBalance,
		&cfg.AlertsEnabled, &cfg.BotIDs, &cfg.ReminderText, &cfg.EmptyReminderText,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), false, nil
	}
	if cfg.BotIDs == nil {
		cfg.BotIDs = []string{}
	}
	return cfg, err == nil, err
}

func readSync(ctx context.Context, db reader, scope Scope) (syncSnapshot, error) {
	var state syncSnapshot
	err := db.QueryRow(ctx, `
        SELECT status, COALESCE(usage_from::text,''), COALESCE(usage_to::text,''),
               last_started_at, last_synced_at, warning_detail
        FROM user_activity_sync_states WHERE user_id=$1 AND admin_account_id=$2
    `, scope.UserID, scope.AdminAccountID).Scan(
		&state.Status, &state.From, &state.To, &state.StartedAt, &state.SyncedAt, &state.Warning,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	return state, err
}

// readRows ranks only eligible ordinary active users. Manual rows survive a missing profile.
func readRows(ctx context.Context, db reader, scope Scope, cfg Settings, state syncSnapshot, today string) ([]Row, error) {
	known7 := covers(state, shift(today, -7), shift(today, -1))
	rows, err := db.Query(ctx, `
        WITH usage AS (
            SELECT site_user_id,
                   COALESCE(sum(actual_cost) FILTER(WHERE day >= $3::date-7 AND day < $3::date),0)::float8 AS cost_7,
                   COALESCE(sum(actual_cost) FILTER(WHERE day >= $3::date-14 AND day < $3::date-7),0)::float8 AS cost_previous,
                   COALESCE(sum(requests) FILTER(WHERE day >= $3::date-7 AND day < $3::date),0)::bigint AS requests_7,
                   sum(actual_cost)::float8 AS recorded_cost,
                   min(day)::text AS recorded_from
            FROM user_activity_daily
            WHERE user_id=$1 AND admin_account_id=$2
            GROUP BY site_user_id
        ), profiles AS (
            SELECT * FROM user_activity_profiles WHERE user_id=$1 AND admin_account_id=$2
        ), watched AS (
            SELECT * FROM vip_watch WHERE user_id=$1 AND admin_account_id=$2
        ), base AS (
            SELECT COALESCE(p.site_user_id,w.site_user_id) AS site_user_id,
                   COALESCE(p.email,'') AS email, COALESCE(p.username,'') AS username,
                   COALESCE(p.role,'') AS role, COALESCE(p.status,'') AS user_status,
                   p.balance, p.last_used_at, p.updated_at,
                   p.site_user_id IS NOT NULL AS profile_present,
                   COALESCE(w.kind,'') AS kind, COALESCE(w.note,'') AS note,
                   COALESCE(u.cost_7,0)::float8 AS cost_7,
                   COALESCE(u.cost_previous,0)::float8 AS cost_previous,
                   COALESCE(u.requests_7,0)::bigint AS requests_7,
                   u.recorded_cost, COALESCE(u.recorded_from,'') AS recorded_from,
                   COALESCE(p.status='active' AND p.role='user' AND w.kind IS DISTINCT FROM 'dismissed',false) AS eligible
            FROM profiles p FULL JOIN watched w ON w.site_user_id=p.site_user_id
            LEFT JOIN usage u ON u.site_user_id=COALESCE(p.site_user_id,w.site_user_id)
        ), ranked AS (
            SELECT *,
                CASE WHEN eligible AND $4 AND cost_7>0 THEN
                    row_number() OVER(PARTITION BY eligible AND $4 AND cost_7>0 ORDER BY cost_7 DESC,site_user_id)
                END AS daily_rank,
                CASE WHEN eligible AND recorded_cost>0 THEN
                    row_number() OVER(PARTITION BY eligible AND recorded_cost>0 ORDER BY recorded_cost DESC,site_user_id)
                END AS lifetime_rank
            FROM base
        )
        SELECT r.site_user_id,r.email,r.username,r.role,r.user_status,r.balance,r.last_used_at,r.updated_at,
               r.profile_present,r.kind,r.note,r.cost_7,r.cost_previous,r.requests_7,r.recorded_cost,r.recorded_from,
               CASE WHEN r.daily_rank <= $5 THEN r.daily_rank ELSE 0 END,
               CASE WHEN r.lifetime_rank <= $6 THEN r.lifetime_rank ELSE 0 END,
               a.state,a.notified_at,a.recovered,a.delivery_status
        FROM ranked r
        LEFT JOIN vip_alert_states a ON a.user_id=$1 AND a.admin_account_id=$2 AND a.site_user_id=r.site_user_id
        WHERE r.kind='manual' OR r.daily_rank <= $5 OR r.lifetime_rank <= $6
        ORDER BY r.cost_7 DESC,r.recorded_cost DESC NULLS LAST,r.site_user_id
    `, scope.UserID, scope.AdminAccountID, today, known7, cfg.DailyTop, cfg.LifetimeTop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Row{}
	for rows.Next() {
		var row Row
		var kind string
		var dailyRank, lifetimeRank int64
		var previousState, delivery *string
		var previousAt *time.Time
		var recovered *bool
		err := rows.Scan(
			&row.SiteUserID, &row.Email, &row.Username, &row.Role, &row.UserStatus, &row.Balance,
			&row.LastUsedAt, &row.ObservedAt, &row.ProfilePresent, &kind, &row.Source.Note,
			&row.Cost7, &row.CostPrevious7, &row.Requests7, &row.RecordedConsumption, &row.RecordedFrom,
			&dailyRank, &lifetimeRank, &previousState, &previousAt, &recovered, &delivery,
		)
		if err != nil {
			return nil, err
		}
		row.Source.Manual = kind == "manual"
		row.Source.DailyRank, row.Source.LifetimeRank = int(dailyRank), int(lifetimeRank)
		if previousState != nil {
			row.LastAlertState = *previousState
		}
		row.LastAlertAt = previousAt
		if recovered != nil {
			row.LastAlertRecovered = *recovered
		}
		if delivery != nil {
			row.LastAlertStatus = *delivery
			if row.LastAlertStatus == "sending" {
				row.LastAlertStatus = "unknown"
			}
		}
		classify(&row, cfg, state, today)
		row.Reminder = Reminder(row, cfg)
		result = append(result, row)
	}
	return result, rows.Err()
}

func readAlert(ctx context.Context, tx pgx.Tx, scope Scope, id string) (*alertState, error) {
	var state alertState
	err := tx.QueryRow(ctx, `
        SELECT state,notified_at,recovered,delivery_status
        FROM vip_alert_states
        WHERE user_id=$1 AND admin_account_id=$2 AND site_user_id=$3
        FOR UPDATE
    `, scope.UserID, scope.AdminAccountID, id).Scan(&state.State, &state.At, &state.Recovered, &state.Delivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &state, err
}

func (r *Repository) Search(ctx context.Context, scope Scope, query string) ([]Candidate, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var created time.Time
	err = tx.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE user_id=$1 AND id=$2`, scope.UserID, scope.AdminAccountID).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && created.Format(time.RFC3339Nano) != scope.CreatedAt) {
		return nil, workspacescope.ErrChanged
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
        SELECT p.site_user_id,p.email,p.username,p.balance,
               COALESCE(w.kind='manual',false),COALESCE(w.kind='dismissed',false)
        FROM user_activity_profiles p
        LEFT JOIN vip_watch w ON w.user_id=p.user_id AND w.admin_account_id=p.admin_account_id AND w.site_user_id=p.site_user_id
        WHERE p.user_id=$1 AND p.admin_account_id=$2
          AND position(lower($3) in lower(p.site_user_id || ' ' || p.email || ' ' || p.username))>0
        ORDER BY p.email,p.site_user_id LIMIT 20
    `, scope.UserID, scope.AdminAccountID, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Candidate{}
	for rows.Next() {
		var row Candidate
		if err := rows.Scan(&row.SiteUserID, &row.Email, &row.Username, &row.Balance, &row.Watched, &row.Dismissed); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *Repository) AlertWorkspaces(ctx context.Context) ([]Scope, error) {
	rows, err := r.db.Query(ctx, `
        SELECT s.user_id,s.admin_account_id,a.created_at
        FROM vip_settings s
        JOIN admin_accounts a ON a.user_id=s.user_id AND a.id=s.admin_account_id
        WHERE s.alerts_enabled
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

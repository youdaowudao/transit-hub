package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type metricsDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MetricsRepository 负责 dashboard_daily_stats 表的持久化操作。
// 与 Redis 的 SessionStore 独立，专门用于存储每日统计快照。
type MetricsRepository struct {
	db metricsDB
}

func NewMetricsRepository(db *pgxpool.Pool) *MetricsRepository {
	return newMetricsRepository(db)
}

func newMetricsRepository(db metricsDB) *MetricsRepository {
	return &MetricsRepository{db: db}
}

// EnsureSchema 在服务启动时创建 dashboard_daily_stats、dashboard_balance_filter、
// upstream_site_daily_costs 表及索引，并将旧数据迁移到 workspace 维度。
//
// 幂等追加策略：通过 DO $$ BEGIN...EXCEPTION WHEN duplicate_column THEN NULL; END $$;
// 模式追加新列，失败只记日志，不影响原有表结构。
func (r *MetricsRepository) EnsureSchema(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS dashboard_daily_stats (
			id               text PRIMARY KEY,
			user_id          text NOT NULL,
			admin_account_id text NOT NULL DEFAULT '',
			date             date NOT NULL,
			today_profit     double precision NOT NULL DEFAULT 0,
			site_balance     double precision NOT NULL DEFAULT 0,
			today_purchase   double precision NOT NULL DEFAULT 0,
			net_profit       double precision NOT NULL DEFAULT 0,
			upstream_balance double precision NOT NULL DEFAULT 0,
			created_at       timestamptz NOT NULL DEFAULT now()
		);

		-- 新增 admin_account_id 列（旧表迁移，IF NOT EXISTS 语义通过 DO NOTHING 实现）。
		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN admin_account_id text NOT NULL DEFAULT '';
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		-- 删除旧的 (user_id, date) 唯一索引，避免与新索引冲突。
		DROP INDEX IF EXISTS idx_dashboard_daily_stats_user_date;

		-- 创建新的 (user_id, admin_account_id, date) 唯一索引。
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_daily_stats_user_account_date
			ON dashboard_daily_stats (user_id, admin_account_id, date);
		CREATE INDEX IF NOT EXISTS idx_dashboard_daily_stats_user_date_desc
			ON dashboard_daily_stats (user_id, admin_account_id, date DESC);

		-- V0.1.16：追加结算状态与来源字段。
		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN settlement_status text NOT NULL DEFAULT 'provisional';
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN snapshot_source text NOT NULL DEFAULT 'live_cache';
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN observed_at timestamptz;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN finalized_at timestamptz;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN cost_expected_count integer;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN cost_collected_count integer;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		DO $$ BEGIN
			ALTER TABLE dashboard_daily_stats ADD COLUMN balance_observed_at timestamptz;
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS additional_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS recharge_fee double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS recharge_fee_rate double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS promotion_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS fixed_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS adjustment_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS additional_cost_records jsonb;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS operating_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS adjusted_net_profit double precision;

		-- V0.1.16：金额字段改为允许 NULL，区分”真零消费”(0.0)与”无数据”(NULL)。
		-- 只在字段仍为 NOT NULL 时执行，已经是 nullable 的字段直接跳过；
		-- 不使用 EXCEPTION WHEN others 以避免静默吞掉意外错误。
		DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='dashboard_daily_stats' AND column_name='today_profit' AND is_nullable='NO'
			) THEN ALTER TABLE dashboard_daily_stats ALTER COLUMN today_profit DROP NOT NULL; END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='dashboard_daily_stats' AND column_name='today_purchase' AND is_nullable='NO'
			) THEN ALTER TABLE dashboard_daily_stats ALTER COLUMN today_purchase DROP NOT NULL; END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='dashboard_daily_stats' AND column_name='net_profit' AND is_nullable='NO'
			) THEN ALTER TABLE dashboard_daily_stats ALTER COLUMN net_profit DROP NOT NULL; END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='dashboard_daily_stats' AND column_name='site_balance' AND is_nullable='NO'
			) THEN ALTER TABLE dashboard_daily_stats ALTER COLUMN site_balance DROP NOT NULL; END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='dashboard_daily_stats' AND column_name='upstream_balance' AND is_nullable='NO'
			) THEN ALTER TABLE dashboard_daily_stats ALTER COLUMN upstream_balance DROP NOT NULL; END IF;
		END $$;

		CREATE TABLE IF NOT EXISTS dashboard_balance_filter (
			user_id          text NOT NULL,
			admin_account_id text NOT NULL DEFAULT '',
			exclude_admin    boolean NOT NULL DEFAULT true,
			exclude_balances jsonb NOT NULL DEFAULT '[]',
			updated_at       timestamptz NOT NULL DEFAULT now()
		);

		-- 新增 admin_account_id 列（旧表迁移）。
		DO $$ BEGIN
			ALTER TABLE dashboard_balance_filter ADD COLUMN admin_account_id text NOT NULL DEFAULT '';
		EXCEPTION WHEN duplicate_column THEN NULL;
		END $$;

		-- 删除旧的 user_id 主键约束，改为复合唯一索引。
		ALTER TABLE dashboard_balance_filter DROP CONSTRAINT IF EXISTS dashboard_balance_filter_pkey;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_balance_filter_user_account
			ON dashboard_balance_filter (user_id, admin_account_id);

		-- V0.1.16：逐站点日成本明细表。
		CREATE TABLE IF NOT EXISTS upstream_site_daily_costs (
			id               text PRIMARY KEY,
			user_id          text NOT NULL,
			admin_account_id text NOT NULL,
			date             date NOT NULL,
			site_id          text NOT NULL,
			site_name        text NOT NULL,
			platform         text NOT NULL,
			raw_cost         numeric,
			recharge_rate    numeric NOT NULL,
			adjusted_cost    numeric,
			status           text NOT NULL,
			source           text NOT NULL,
			error_reason     text,
			observed_at      timestamptz NOT NULL,
			UNIQUE (user_id, admin_account_id, date, site_id)
		);
		CREATE INDEX IF NOT EXISTS idx_upstream_site_daily_costs_date
			ON upstream_site_daily_costs (user_id, admin_account_id, date);

		CREATE TABLE IF NOT EXISTS dashboard_recharge_fee_rates (
			id               text PRIMARY KEY,
			user_id          text NOT NULL,
			admin_account_id text NOT NULL,
			effective_date   date NOT NULL,
			rate             numeric NOT NULL,
			created_at       timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, effective_date)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_recharge_fee_rates_date
			ON dashboard_recharge_fee_rates (user_id, admin_account_id, effective_date DESC);

		CREATE TABLE IF NOT EXISTS dashboard_additional_costs (
			id               text PRIMARY KEY,
			user_id          text NOT NULL,
			admin_account_id text NOT NULL,
			type             text NOT NULL,
			name             text NOT NULL,
			business_date    date NOT NULL,
			amount_cents     bigint NOT NULL,
			original_amount  numeric NOT NULL DEFAULT 0,
			rate             numeric NOT NULL DEFAULT 0,
			usage_rate       numeric NOT NULL DEFAULT 0,
			days             integer NOT NULL DEFAULT 0,
			source_id        text NOT NULL DEFAULT '',
			note             text NOT NULL DEFAULT '',
			estimated        boolean NOT NULL DEFAULT false,
			created_at       timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_additional_costs_date
			ON dashboard_additional_costs (user_id, admin_account_id, business_date);
	`)
	return err
}

func (r *MetricsRepository) GetRechargeFeeRate(ctx context.Context, userID, adminAccountID, date string) (RechargeFeeRate, error) {
	rate := RechargeFeeRate{UserID: userID, AdminAccountID: adminAccountID, Rate: defaultRechargeFeeRate, EffectiveDate: date}
	err := r.db.QueryRow(ctx, `
		SELECT id, effective_date::text, rate, created_at
		FROM dashboard_recharge_fee_rates
		WHERE user_id = $1 AND admin_account_id = $2 AND effective_date <= $3::date
		ORDER BY effective_date DESC, created_at DESC, id DESC LIMIT 1
	`, userID, adminAccountID, date).Scan(&rate.ID, &rate.EffectiveDate, &rate.Rate, &rate.CreatedAt)
	if err == pgx.ErrNoRows {
		return rate, nil
	}
	return rate, err
}

func (r *MetricsRepository) SaveRechargeFeeRate(ctx context.Context, rate RechargeFeeRate) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO dashboard_recharge_fee_rates (id, user_id, admin_account_id, effective_date, rate)
		VALUES ($1, $2, $3, $4::date, $5)
		ON CONFLICT (user_id, admin_account_id, effective_date) DO UPDATE SET
			id = EXCLUDED.id, rate = EXCLUDED.rate, created_at = now()
	`, rate.ID, rate.UserID, rate.AdminAccountID, rate.EffectiveDate, rate.Rate)
	return err
}

func (r *MetricsRepository) InsertAdditionalCosts(ctx context.Context, records []AdditionalCostRecord) error {
	if len(records) == 0 {
		return nil
	}
	values := make([]string, 0, len(records))
	args := make([]any, 0, len(records)*14)
	for i, record := range records {
		base := i * 14
		values = append(values, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d::date, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12, base+13, base+14,
		))
		args = append(args,
			record.ID, record.UserID, record.AdminAccountID, record.Type, record.Name, record.BusinessDate,
			record.AmountCents, record.OriginalAmount, record.Rate, record.UsageRate, record.Days,
			record.SourceID, record.Note, record.Estimated,
		)
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO dashboard_additional_costs (
			id, user_id, admin_account_id, type, name, business_date, amount_cents,
			original_amount, rate, usage_rate, days, source_id, note, estimated
		) VALUES `+strings.Join(values, ", "), args...)
	return err
}

func (r *MetricsRepository) ListAdditionalCosts(ctx context.Context, userID, adminAccountID, from, to string) ([]AdditionalCostRecord, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, type, name, business_date::text, amount_cents, original_amount, rate, usage_rate,
		       days, source_id, note, estimated, created_at
		FROM dashboard_additional_costs
		WHERE user_id = $1 AND admin_account_id = $2 AND business_date >= $3::date AND business_date <= $4::date
		ORDER BY business_date ASC, created_at ASC, id ASC
	`, userID, adminAccountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdditionalCostRecord, 0)
	for rows.Next() {
		var item AdditionalCostRecord
		if err := rows.Scan(&item.ID, &item.Type, &item.Name, &item.BusinessDate, &item.AmountCents,
			&item.OriginalAmount, &item.Rate, &item.UsageRate, &item.Days, &item.SourceID, &item.Note,
			&item.Estimated, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.UserID = userID
		item.AdminAccountID = adminAccountID
		item.Amount = float64(item.AmountCents) / 100
		items = append(items, item)
	}
	return items, rows.Err()
}

// Upsert 插入或更新指定用户指定工作区指定日期的快照行。
// final 保护：settlement_status = 'final' 的行不允许被 snapshot_source = 'live_cache' 的写入覆盖。
// 条件：目标行不是 final，或者来源不是 live_cache（dated_query/backfill 允许覆盖 provisional/partial）。
func (r *MetricsRepository) Upsert(ctx context.Context, snapshot DailySnapshot) error {
	recordsJSON, err := json.Marshal(snapshot.AdditionalCostRecords)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO dashboard_daily_stats (
			id, user_id, admin_account_id, date,
			today_profit, site_balance, today_purchase, net_profit, upstream_balance,
			created_at, settlement_status, snapshot_source, observed_at,
			finalized_at, cost_expected_count, cost_collected_count, balance_observed_at,
			additional_cost, recharge_fee, recharge_fee_rate, promotion_cost, fixed_cost,
			adjustment_cost, additional_cost_records, operating_cost, adjusted_net_profit
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24::jsonb, $25, $26
		WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = $2 AND id = $3)
		ON CONFLICT (user_id, admin_account_id, date) DO UPDATE SET
			today_profit        = EXCLUDED.today_profit,
			-- 余额字段是时点观测值，dated_query/backfill 不覆盖历史余额，用 COALESCE 保留旧值。
			site_balance        = COALESCE(EXCLUDED.site_balance,     dashboard_daily_stats.site_balance),
			today_purchase      = EXCLUDED.today_purchase,
			net_profit          = EXCLUDED.net_profit,
			upstream_balance    = COALESCE(EXCLUDED.upstream_balance, dashboard_daily_stats.upstream_balance),
			created_at          = EXCLUDED.created_at,
			settlement_status   = EXCLUDED.settlement_status,
			snapshot_source     = EXCLUDED.snapshot_source,
			observed_at         = EXCLUDED.observed_at,
			finalized_at        = EXCLUDED.finalized_at,
			cost_expected_count = EXCLUDED.cost_expected_count,
			cost_collected_count = EXCLUDED.cost_collected_count,
			balance_observed_at = COALESCE(EXCLUDED.balance_observed_at, dashboard_daily_stats.balance_observed_at),
			additional_cost    = EXCLUDED.additional_cost,
			recharge_fee       = EXCLUDED.recharge_fee,
			recharge_fee_rate  = EXCLUDED.recharge_fee_rate,
			promotion_cost     = EXCLUDED.promotion_cost,
			fixed_cost         = EXCLUDED.fixed_cost,
			adjustment_cost    = EXCLUDED.adjustment_cost,
			additional_cost_records = EXCLUDED.additional_cost_records,
			operating_cost     = EXCLUDED.operating_cost,
			adjusted_net_profit = EXCLUDED.adjusted_net_profit
		WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = EXCLUDED.user_id AND id = EXCLUDED.admin_account_id)
		  AND (dashboard_daily_stats.settlement_status != 'final' OR EXCLUDED.snapshot_source != 'live_cache')
	`, snapshot.ID, snapshot.UserID, snapshot.AdminAccountID, snapshot.Date,
		snapshot.TodayProfit, snapshot.SiteBalance, snapshot.TodayPurchase,
		snapshot.NetProfit, snapshot.UpstreamBalance, snapshot.CreatedAt,
		snapshot.SettlementStatus, snapshot.SnapshotSource, snapshot.ObservedAt,
		snapshot.FinalizedAt, snapshot.CostExpectedCount, snapshot.CostCollectedCount,
		snapshot.BalanceObservedAt, snapshot.AdditionalCost, snapshot.RechargeFee, snapshot.RechargeFeeRate,
		snapshot.PromotionCost, snapshot.FixedCost, snapshot.AdjustmentCost, recordsJSON,
		snapshot.OperatingCost, snapshot.AdjustedNetProfit)
	return err
}

// ListRange 查询指定用户指定工作区相对固定上海业务日最近 days 天的快照记录，按日期升序返回。
// 包含结算状态，供前端判断环比是否可信。
func (r *MetricsRepository) ListRange(ctx context.Context, userID, adminAccountID string, days int, businessDate string) ([]DailySnapshot, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, admin_account_id, date, today_profit, site_balance, today_purchase, net_profit, upstream_balance, created_at, settlement_status, additional_cost, recharge_fee, recharge_fee_rate, promotion_cost, fixed_cost, adjustment_cost, operating_cost, adjusted_net_profit
		FROM dashboard_daily_stats
		WHERE user_id = $1 AND admin_account_id = $2 AND date >= ($3::date - $4::int) AND date < $3::date
		ORDER BY date ASC
	`, userID, adminAccountID, businessDate, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshots := make([]DailySnapshot, 0)
	for rows.Next() {
		var s DailySnapshot
		if err := rows.Scan(&s.ID, &s.UserID, &s.AdminAccountID, &s.Date, &s.TodayProfit, &s.SiteBalance,
			&s.TodayPurchase, &s.NetProfit, &s.UpstreamBalance, &s.CreatedAt, &s.SettlementStatus,
			&s.AdditionalCost, &s.RechargeFee, &s.RechargeFeeRate, &s.PromotionCost, &s.FixedCost,
			&s.AdjustmentCost, &s.OperatingCost, &s.AdjustedNetProfit); err != nil {
			return nil, err
		}
		if s.SnapshotSource == "" {
			s.SnapshotSource = SnapshotSourceLiveCache
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// Exists 检查指定用户指定工作区指定日期是否已有快照行。
func (r *MetricsRepository) Exists(ctx context.Context, userID, adminAccountID string, date time.Time) (bool, error) {
	var count int
	err := r.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM dashboard_daily_stats WHERE user_id = $1 AND admin_account_id = $2 AND date = $3
	`, userID, adminAccountID, date).Scan(&count)
	return count > 0, err
}

// GetBalanceFilter 读取指定用户指定工作区的余额筛选配置。
// 若用户尚未配置，返回默认值（排除 admin、不排除任何余额值）。
func (r *MetricsRepository) GetBalanceFilter(ctx context.Context, userID, adminAccountID string) (BalanceFilterConfig, error) {
	config := BalanceFilterConfig{
		UserID:          userID,
		AdminAccountID:  adminAccountID,
		ExcludeAdmin:    true,
		ExcludeBalances: []float64{},
	}
	var balancesJSON []byte
	err := r.db.QueryRow(ctx, `
		SELECT exclude_admin, exclude_balances FROM dashboard_balance_filter WHERE user_id = $1 AND admin_account_id = $2
	`, userID, adminAccountID).Scan(&config.ExcludeAdmin, &balancesJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return config, nil
		}
		return config, err
	}
	if len(balancesJSON) > 0 {
		if err := json.Unmarshal(balancesJSON, &config.ExcludeBalances); err != nil {
			return config, err
		}
	}
	return config, nil
}

// SaveBalanceFilter 保存或更新指定用户指定工作区的余额筛选配置。
// 使用 upsert 确保幂等写入，用户首次配置和后续修改都走同一路径。
func (r *MetricsRepository) SaveBalanceFilter(ctx context.Context, config BalanceFilterConfig) error {
	balancesJSON, err := json.Marshal(config.ExcludeBalances)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO dashboard_balance_filter (user_id, admin_account_id, exclude_admin, exclude_balances, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, admin_account_id) DO UPDATE SET
			exclude_admin    = EXCLUDED.exclude_admin,
			exclude_balances = EXCLUDED.exclude_balances,
			updated_at       = now()
	`, config.UserID, config.AdminAccountID, config.ExcludeAdmin, balancesJSON)
	return err
}

// UpsertSiteCost 插入或更新单个上游站点的日成本明细。
// 保护规则：status='ok' 的行不允许被失败结果覆盖，避免重试时覆盖已成功的历史数据。
func (r *MetricsRepository) UpsertSiteCost(ctx context.Context, cost SiteDailyCost) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO upstream_site_daily_costs (
			id, user_id, admin_account_id, date,
			site_id, site_name, platform,
			raw_cost, recharge_rate, adjusted_cost,
			status, source, error_reason, observed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (user_id, admin_account_id, date, site_id) DO UPDATE SET
			site_name     = EXCLUDED.site_name,
			platform      = EXCLUDED.platform,
			raw_cost      = EXCLUDED.raw_cost,
			recharge_rate = EXCLUDED.recharge_rate,
			adjusted_cost = EXCLUDED.adjusted_cost,
			status        = EXCLUDED.status,
			source        = EXCLUDED.source,
			error_reason  = EXCLUDED.error_reason,
			observed_at   = EXCLUDED.observed_at
		WHERE upstream_site_daily_costs.status != 'ok'
	`, cost.ID, cost.UserID, cost.AdminAccountID, cost.Date,
		cost.SiteID, cost.SiteName, cost.Platform,
		cost.RawCost, cost.RechargeRate, cost.AdjustedCost,
		cost.Status, cost.Source, cost.ErrorReason, cost.ObservedAt)
	return err
}

// ListSiteCosts 查询指定日期的所有站点成本明细，按 site_id 排序。
func (r *MetricsRepository) ListSiteCosts(ctx context.Context, userID, adminAccountID string, date string) ([]SiteDailyCost, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, admin_account_id, date, site_id, site_name, platform,
		       raw_cost, recharge_rate, adjusted_cost, status, source, error_reason, observed_at
		FROM upstream_site_daily_costs
		WHERE user_id = $1 AND admin_account_id = $2 AND date = $3::date
		ORDER BY site_id ASC
	`, userID, adminAccountID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	costs := make([]SiteDailyCost, 0)
	for rows.Next() {
		var c SiteDailyCost
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.AdminAccountID, &c.Date,
			&c.SiteID, &c.SiteName, &c.Platform,
			&c.RawCost, &c.RechargeRate, &c.AdjustedCost,
			&c.Status, &c.Source, &c.ErrorReason, &c.ObservedAt,
		); err != nil {
			return nil, err
		}
		costs = append(costs, c)
	}
	return costs, rows.Err()
}

// ListDailyStats 查询指定日期范围内的快照记录，按日期升序返回。
// 范围为闭区间 [from, to]，上限最多 90 天。
func (r *MetricsRepository) ListDailyStats(ctx context.Context, userID, adminAccountID string, from, to string) ([]DailySnapshot, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, admin_account_id, date,
		       today_profit, site_balance, today_purchase, net_profit, upstream_balance,
		       created_at, settlement_status, snapshot_source, observed_at,
		       finalized_at, cost_expected_count, cost_collected_count, balance_observed_at,
		       additional_cost, recharge_fee, recharge_fee_rate, promotion_cost, fixed_cost,
		       adjustment_cost, additional_cost_records, operating_cost, adjusted_net_profit
		FROM dashboard_daily_stats
		WHERE user_id = $1 AND admin_account_id = $2
		  AND date >= $3::date AND date <= $4::date
		ORDER BY date ASC
	`, userID, adminAccountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshots := make([]DailySnapshot, 0)
	for rows.Next() {
		var s DailySnapshot
		var recordsJSON []byte
		if err := rows.Scan(
			&s.ID, &s.UserID, &s.AdminAccountID, &s.Date,
			&s.TodayProfit, &s.SiteBalance, &s.TodayPurchase, &s.NetProfit, &s.UpstreamBalance,
			&s.CreatedAt, &s.SettlementStatus, &s.SnapshotSource, &s.ObservedAt,
			&s.FinalizedAt, &s.CostExpectedCount, &s.CostCollectedCount, &s.BalanceObservedAt,
			&s.AdditionalCost, &s.RechargeFee, &s.RechargeFeeRate, &s.PromotionCost, &s.FixedCost,
			&s.AdjustmentCost, &recordsJSON, &s.OperatingCost, &s.AdjustedNetProfit,
		); err != nil {
			return nil, err
		}
		if len(recordsJSON) > 0 && string(recordsJSON) != "null" {
			if err := json.Unmarshal(recordsJSON, &s.AdditionalCostRecords); err != nil {
				return nil, err
			}
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

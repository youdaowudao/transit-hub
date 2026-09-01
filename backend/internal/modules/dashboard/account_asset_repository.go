package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/shared/businesstime"
)

func (r *MetricsRepository) ensureAccountAssetSchema(ctx context.Context) error {
	_, err := r.db.Exec(ctx, `
		ALTER TABLE dashboard_additional_costs ADD COLUMN IF NOT EXISTS batch_id text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_additional_costs ADD COLUMN IF NOT EXISTS account_asset_id text NOT NULL DEFAULT '';
		CREATE INDEX IF NOT EXISTS idx_dashboard_additional_costs_asset_date
			ON dashboard_additional_costs (user_id, admin_account_id, account_asset_id, business_date);

		CREATE TABLE IF NOT EXISTS dashboard_account_batches (
			id text PRIMARY KEY,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			idempotency_key text NOT NULL,
			batch_name text NOT NULL,
			platform text NOT NULL,
			channel text NOT NULL,
			account_type text NOT NULL,
			purchase_date date NOT NULL,
			purchase_url text NOT NULL DEFAULT '',
			default_upstream_reference_url text NOT NULL DEFAULT '',
			quantity integer NOT NULL CHECK (quantity > 0),
			total_amount_cents bigint NOT NULL CHECK (total_amount_cents >= 0),
			accounting_mode text NOT NULL,
			recognition_mode text NOT NULL,
			recognition_start_date date NOT NULL,
			recognition_days integer NOT NULL DEFAULT 0,
			stats_mode text NOT NULL,
			note text NOT NULL DEFAULT '',
			created_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, idempotency_key)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_account_batches_workspace_date
			ON dashboard_account_batches (user_id, admin_account_id, purchase_date DESC, created_at DESC);

		CREATE TABLE IF NOT EXISTS dashboard_account_assets (
			id text PRIMARY KEY,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			batch_id text NOT NULL REFERENCES dashboard_account_batches(id) ON DELETE CASCADE,
			identifier text NOT NULL,
			platform text NOT NULL,
			channel text NOT NULL,
			account_type text NOT NULL,
			initial_identifier text NOT NULL,
			initial_platform text NOT NULL,
			initial_channel text NOT NULL,
			initial_account_type text NOT NULL,
			initial_upstream_reference_url text NOT NULL DEFAULT '',
			purchase_cost_cents bigint NOT NULL CHECK (purchase_cost_cents >= 0),
			quota_total_micros bigint,
			accounting_mode text NOT NULL,
			recognition_mode text NOT NULL,
			recognition_start_date date NOT NULL,
			recognition_days integer NOT NULL DEFAULT 0,
			stats_mode text NOT NULL,
			current_status text NOT NULL,
			upstream_reference_url text,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, batch_id, identifier)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_account_assets_workspace_status
			ON dashboard_account_assets (user_id, admin_account_id, current_status, updated_at DESC);
		ALTER TABLE dashboard_account_assets ADD COLUMN IF NOT EXISTS initial_identifier text;
		ALTER TABLE dashboard_account_assets ADD COLUMN IF NOT EXISTS initial_platform text;
		ALTER TABLE dashboard_account_assets ADD COLUMN IF NOT EXISTS initial_channel text;
		ALTER TABLE dashboard_account_assets ADD COLUMN IF NOT EXISTS initial_account_type text;
		ALTER TABLE dashboard_account_assets ADD COLUMN IF NOT EXISTS initial_upstream_reference_url text;
		UPDATE dashboard_account_assets SET
			initial_identifier=identifier,
			initial_platform=platform,
			initial_channel=channel,
			initial_account_type=account_type,
			initial_upstream_reference_url=upstream_reference_url
		WHERE initial_identifier IS NULL OR initial_platform IS NULL OR initial_channel IS NULL
		   OR initial_account_type IS NULL OR initial_upstream_reference_url IS NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_identifier SET NOT NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_platform SET NOT NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_channel SET NOT NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_account_type SET NOT NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_upstream_reference_url SET NOT NULL;
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_identifier SET DEFAULT '';
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_platform SET DEFAULT '';
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_channel SET DEFAULT '';
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_account_type SET DEFAULT '';
		ALTER TABLE dashboard_account_assets ALTER COLUMN initial_upstream_reference_url SET DEFAULT '';

		CREATE TABLE IF NOT EXISTS dashboard_account_links (
			id text PRIMARY KEY,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			account_asset_id text NOT NULL REFERENCES dashboard_account_assets(id) ON DELETE CASCADE,
			connection_id text NOT NULL,
			upstream_site_id text NOT NULL,
			upstream_key_id text NOT NULL,
			scope_admin_account_id text NOT NULL,
			own_group_id text NOT NULL,
			connection_name text NOT NULL DEFAULT '',
			site_name text NOT NULL DEFAULT '',
			key_name text NOT NULL DEFAULT '',
			own_group_name text NOT NULL DEFAULT '',
			upstream_reference_url text NOT NULL DEFAULT '',
			effective_from date NOT NULL,
			effective_to date,
			manual_same_day_split boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_account_links_active_asset
			ON dashboard_account_links (user_id, admin_account_id, account_asset_id) WHERE effective_to IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_account_links_active_connection
			ON dashboard_account_links (user_id, admin_account_id, connection_id) WHERE effective_to IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_account_links_active_key
			ON dashboard_account_links (user_id, admin_account_id, upstream_site_id, upstream_key_id) WHERE effective_to IS NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboard_account_links_active_scope
			ON dashboard_account_links (user_id, admin_account_id, scope_admin_account_id, own_group_id) WHERE effective_to IS NULL;
		ALTER TABLE dashboard_account_links ADD COLUMN IF NOT EXISTS upstream_reference_url text NOT NULL DEFAULT '';

		CREATE TABLE IF NOT EXISTS dashboard_account_events (
			id text PRIMARY KEY,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			account_asset_id text NOT NULL REFERENCES dashboard_account_assets(id) ON DELETE CASCADE,
			event_type text NOT NULL,
			effective_date date NOT NULL,
			status text NOT NULL DEFAULT '',
			quota_used_micros bigint,
			revenue_cents bigint,
			refund_cents bigint,
			upstream_cost_cents bigint,
			stats_mode text NOT NULL DEFAULT '',
			identifier text NOT NULL DEFAULT '',
			platform text NOT NULL DEFAULT '',
			channel text NOT NULL DEFAULT '',
			account_type text NOT NULL DEFAULT '',
			purchase_url text,
			upstream_reference_url text NOT NULL DEFAULT '',
			note text NOT NULL DEFAULT '',
			idempotency_key text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, account_asset_id, idempotency_key)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_account_events_asset_date
			ON dashboard_account_events (user_id, admin_account_id, account_asset_id, effective_date, created_at);
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS stats_mode text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS identifier text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS platform text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS channel text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS account_type text NOT NULL DEFAULT '';
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS purchase_url text;
		ALTER TABLE dashboard_account_events ADD COLUMN IF NOT EXISTS upstream_reference_url text;
		ALTER TABLE dashboard_account_events ALTER COLUMN upstream_reference_url DROP NOT NULL;
		ALTER TABLE dashboard_account_events ALTER COLUMN upstream_reference_url DROP DEFAULT;

		CREATE TABLE IF NOT EXISTS dashboard_upstream_key_cost_runs (
			id text PRIMARY KEY,
			snapshot_run_id text NOT NULL,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			business_date date NOT NULL,
			site_id text NOT NULL,
			expected_key_count integer NOT NULL,
			collected_key_count integer NOT NULL,
			site_total_cents bigint NOT NULL,
			key_total_cents bigint NOT NULL,
			complete boolean NOT NULL,
			quality text NOT NULL,
			observed_at timestamptz NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, business_date, site_id, id)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_key_cost_runs_workspace_date
			ON dashboard_upstream_key_cost_runs (user_id, admin_account_id, business_date, site_id, created_at DESC);
		ALTER TABLE dashboard_upstream_key_cost_runs ADD COLUMN IF NOT EXISTS snapshot_run_id text NOT NULL DEFAULT '';

		CREATE TABLE IF NOT EXISTS dashboard_upstream_key_daily_costs (
			run_id text NOT NULL REFERENCES dashboard_upstream_key_cost_runs(id) ON DELETE CASCADE,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			business_date date NOT NULL,
			site_id text NOT NULL,
			key_id text NOT NULL,
			key_name text NOT NULL DEFAULT '',
			raw_amount_micros bigint NOT NULL,
			adjusted_cost_cents bigint NOT NULL,
			status text NOT NULL,
			observed_at timestamptz NOT NULL,
			PRIMARY KEY (run_id, key_id)
		);

		CREATE TABLE IF NOT EXISTS dashboard_account_daily_stats (
			id text PRIMARY KEY,
			user_id text NOT NULL,
			admin_account_id text NOT NULL,
			account_asset_id text NOT NULL REFERENCES dashboard_account_assets(id) ON DELETE CASCADE,
			business_date date NOT NULL,
			source text NOT NULL,
			quality text NOT NULL,
			key_cost_run_id text NOT NULL DEFAULT '',
			raw_quota_used_micros bigint,
			revenue_cents bigint,
			upstream_cost_cents bigint,
			recognized_cost_cents bigint NOT NULL DEFAULT 0,
			replacement_deduction_cents bigint,
			observed_at timestamptz,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (user_id, admin_account_id, account_asset_id, business_date)
		);
		CREATE INDEX IF NOT EXISTS idx_dashboard_account_daily_stats_workspace_date
			ON dashboard_account_daily_stats (user_id, admin_account_id, business_date, account_asset_id);

		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS account_snapshot_run_id text;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS account_expected_count integer;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS account_completed_count integer;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS account_stats_quality text NOT NULL DEFAULT 'missing';
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS account_purchase_cost double precision;
		ALTER TABLE dashboard_daily_stats ADD COLUMN IF NOT EXISTS replacement_deduction double precision;
	`)
	return err
}

func (r *MetricsRepository) CreateAccountBatch(ctx context.Context, batch AccountBatch, assets []AccountAsset, links []AccountLink, costs []AdditionalCostRecord) (AccountBatchResult, error) {
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return AccountBatchResult{}, errors.New("account asset repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return AccountBatchResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if existing, found, err := loadAccountBatchByIdempotency(ctx, tx, batch.UserID, batch.AdminAccountID, batch.IdempotencyKey); err != nil {
		return AccountBatchResult{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return AccountBatchResult{}, err
		}
		return existing, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dashboard_account_batches (
			id, user_id, admin_account_id, idempotency_key, batch_name, platform, channel, account_type,
			purchase_date, purchase_url, default_upstream_reference_url, quantity, total_amount_cents,
			accounting_mode, recognition_mode, recognition_start_date, recognition_days, stats_mode, note, created_at
		)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10,$11,$12,$13,$14,$15,$16::date,$17,$18,$19,$20
		WHERE EXISTS (SELECT 1 FROM admin_accounts WHERE user_id = $2 AND id = $3)
	`, batch.ID, batch.UserID, batch.AdminAccountID, batch.IdempotencyKey, batch.BatchName, batch.Platform, batch.Channel,
		batch.AccountType, batch.PurchaseDate, batch.PurchaseURL, batch.DefaultUpstreamReferenceURL, batch.Quantity,
		batch.TotalAmountCents, batch.AccountingMode, batch.RecognitionMode, batch.RecognitionStartDate,
		batch.RecognitionDays, batch.StatsMode, batch.Note, batch.CreatedAt); err != nil {
		return AccountBatchResult{}, err
	}
	for _, asset := range assets {
		if asset.UserID != batch.UserID || asset.AdminAccountID != batch.AdminAccountID || asset.BatchID != batch.ID {
			return AccountBatchResult{}, errors.New("account asset workspace mismatch")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboard_account_assets (
				id,user_id,admin_account_id,batch_id,identifier,platform,channel,account_type,purchase_cost_cents,
				quota_total_micros,accounting_mode,recognition_mode,recognition_start_date,recognition_days,stats_mode,
				current_status,upstream_reference_url,created_at,updated_at,initial_identifier,initial_platform,
				initial_channel,initial_account_type,initial_upstream_reference_url
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::date,$14,$15,$16,$17,$18,$19,$5,$6,$7,$8,$17)
		`, asset.ID, asset.UserID, asset.AdminAccountID, asset.BatchID, asset.Identifier, asset.Platform, asset.Channel,
			asset.AccountType, asset.PurchaseCostCents, asset.QuotaTotalMicros, asset.AccountingMode, asset.RecognitionMode,
			asset.RecognitionStartDate, asset.RecognitionDays, asset.StatsMode, asset.CurrentStatus,
			asset.UpstreamReferenceURL, asset.CreatedAt, asset.UpdatedAt); err != nil {
			return AccountBatchResult{}, err
		}
	}
	for _, link := range links {
		if link.UserID != batch.UserID || link.AdminAccountID != batch.AdminAccountID {
			return AccountBatchResult{}, errors.New("account link workspace mismatch")
		}
		if err := r.insertAccountLink(ctx, tx, link); err != nil {
			return AccountBatchResult{}, err
		}
	}
	for _, cost := range costs {
		if cost.UserID != batch.UserID || cost.AdminAccountID != batch.AdminAccountID || cost.BatchID != batch.ID {
			return AccountBatchResult{}, errors.New("account cost workspace mismatch")
		}
	}
	if err := r.insertAdditionalCosts(ctx, tx, costs); err != nil {
		return AccountBatchResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountBatchResult{}, err
	}
	return AccountBatchResult{Batch: batch, Assets: assets}, nil
}

func (r *MetricsRepository) insertAccountLink(ctx context.Context, db metricsDB, link AccountLink) error {
	var overlaps int
	if err := db.QueryRow(ctx, `
		SELECT count(*)::int FROM dashboard_account_links existing
		WHERE existing.user_id=$1 AND existing.admin_account_id=$2 AND existing.id<>$3
		  AND existing.effective_from <= COALESCE($5::date,'infinity'::date)
		  AND $4::date <= COALESCE(existing.effective_to,'infinity'::date)
		  AND (existing.account_asset_id=$6 OR existing.connection_id=$7
		    OR (existing.upstream_site_id=$8 AND existing.upstream_key_id=$9)
		    OR (existing.scope_admin_account_id=$10 AND existing.own_group_id=$11))
		  AND NOT ($12 AND existing.effective_to=$4::date)
	`, link.UserID, link.AdminAccountID, link.ID, link.EffectiveFrom, link.EffectiveTo,
		link.AccountAssetID, link.ConnectionID, link.UpstreamSiteID, link.UpstreamKeyID,
		link.ScopeAdminAccountID, link.OwnGroupID, link.ManualSameDaySplit).Scan(&overlaps); err != nil {
		return err
	}
	if overlaps > 0 {
		return errInvalidAccountBatch
	}
	_, err := db.Exec(ctx, `
		INSERT INTO dashboard_account_links (
			id,user_id,admin_account_id,account_asset_id,connection_id,upstream_site_id,upstream_key_id,
			scope_admin_account_id,own_group_id,connection_name,site_name,key_name,own_group_name,
			upstream_reference_url,effective_from,effective_to,manual_same_day_split,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::date,$16::date,$17,$18)
	`, link.ID, link.UserID, link.AdminAccountID, link.AccountAssetID, link.ConnectionID,
		link.UpstreamSiteID, link.UpstreamKeyID, link.ScopeAdminAccountID, link.OwnGroupID,
		link.ConnectionName, link.SiteName, link.KeyName, link.OwnGroupName,
		link.UpstreamReferenceURL, link.EffectiveFrom, link.EffectiveTo, link.ManualSameDaySplit, link.CreatedAt)
	return err
}

func loadAccountBatchByIdempotency(ctx context.Context, db metricsDB, userID, adminAccountID, key string) (AccountBatchResult, bool, error) {
	var batch AccountBatch
	err := db.QueryRow(ctx, `
		SELECT id,user_id,admin_account_id,idempotency_key,batch_name,platform,channel,account_type,
		       purchase_date::text,purchase_url,default_upstream_reference_url,quantity,total_amount_cents,
		       accounting_mode,recognition_mode,recognition_start_date::text,recognition_days,stats_mode,note,created_at
		FROM dashboard_account_batches
		WHERE user_id=$1 AND admin_account_id=$2 AND idempotency_key=$3
	`, userID, adminAccountID, key).Scan(
		&batch.ID, &batch.UserID, &batch.AdminAccountID, &batch.IdempotencyKey, &batch.BatchName, &batch.Platform,
		&batch.Channel, &batch.AccountType, &batch.PurchaseDate, &batch.PurchaseURL, &batch.DefaultUpstreamReferenceURL,
		&batch.Quantity, &batch.TotalAmountCents, &batch.AccountingMode, &batch.RecognitionMode,
		&batch.RecognitionStartDate, &batch.RecognitionDays, &batch.StatsMode, &batch.Note, &batch.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountBatchResult{}, false, nil
	}
	if err != nil {
		return AccountBatchResult{}, false, err
	}
	rows, err := db.Query(ctx, `
		SELECT id,user_id,admin_account_id,batch_id,identifier,platform,channel,account_type,purchase_cost_cents,
		       quota_total_micros,accounting_mode,recognition_mode,recognition_start_date::text,recognition_days,
		       stats_mode,current_status,upstream_reference_url,created_at,updated_at
		FROM dashboard_account_assets
		WHERE user_id=$1 AND admin_account_id=$2 AND batch_id=$3 ORDER BY created_at,id
	`, userID, adminAccountID, batch.ID)
	if err != nil {
		return AccountBatchResult{}, false, err
	}
	defer rows.Close()
	assets := make([]AccountAsset, 0, batch.Quantity)
	for rows.Next() {
		var asset AccountAsset
		if err := rows.Scan(&asset.ID, &asset.UserID, &asset.AdminAccountID, &asset.BatchID, &asset.Identifier,
			&asset.Platform, &asset.Channel, &asset.AccountType, &asset.PurchaseCostCents, &asset.QuotaTotalMicros,
			&asset.AccountingMode, &asset.RecognitionMode, &asset.RecognitionStartDate, &asset.RecognitionDays,
			&asset.StatsMode, &asset.CurrentStatus, &asset.UpstreamReferenceURL, &asset.CreatedAt, &asset.UpdatedAt); err != nil {
			return AccountBatchResult{}, false, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return AccountBatchResult{}, false, err
	}
	return AccountBatchResult{Batch: batch, Assets: assets}, true, nil
}

func accountAssetQueryError(kind string, err error) error {
	return fmt.Errorf("dashboard account asset %s: %w", kind, err)
}

func (r *MetricsRepository) ListAccountAssets(ctx context.Context, userID, adminAccountID string, filter AccountAssetFilter) (AccountAssetPage, error) {
	filter.Platform = strings.TrimSpace(filter.Platform)
	filter.Channel = strings.TrimSpace(filter.Channel)
	filter.AccountType = strings.TrimSpace(filter.AccountType)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Search = strings.TrimSpace(filter.Search)
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 200 {
		filter.PageSize = 50
	}
	rows, err := r.db.Query(ctx, `
		SELECT asset.id,asset.user_id,asset.admin_account_id,asset.batch_id,asset.identifier,asset.platform,
		       asset.channel,asset.account_type,asset.purchase_cost_cents,asset.quota_total_micros,
		       asset.accounting_mode,asset.recognition_mode,asset.recognition_start_date::text,asset.recognition_days,
		       asset.stats_mode,asset.current_status,asset.upstream_reference_url,asset.created_at,asset.updated_at,
		       stats.raw_quota_used_micros,stats.revenue_cents,stats.upstream_cost_cents,stats.quality,
		       COALESCE(refunds.refund_cents,0),
		       EXISTS (SELECT 1 FROM dashboard_account_links link WHERE link.user_id=asset.user_id
		         AND link.admin_account_id=asset.admin_account_id AND link.account_asset_id=asset.id
		         AND link.effective_from <= $12::date AND (link.effective_to IS NULL OR link.effective_to >= $12::date))
		FROM dashboard_account_assets asset
		LEFT JOIN LATERAL (
			SELECT sum(raw_quota_used_micros)::bigint AS raw_quota_used_micros,
			       sum(revenue_cents)::bigint AS revenue_cents,sum(upstream_cost_cents)::bigint AS upstream_cost_cents,
			       CASE WHEN count(*)=0 THEN 'missing'
			            WHEN bool_and(quality=$10) THEN $10 ELSE 'partial' END AS quality
			FROM dashboard_account_daily_stats stat
			WHERE stat.user_id=asset.user_id AND stat.admin_account_id=asset.admin_account_id AND stat.account_asset_id=asset.id
		) stats ON true
		LEFT JOIN LATERAL (
			SELECT sum(refund_cents)::bigint AS refund_cents FROM dashboard_account_events event
			WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
			  AND event.account_asset_id=asset.id AND event.event_type=$11
		) refunds ON true
		WHERE asset.user_id=$1 AND asset.admin_account_id=$2
		  AND ($3='' OR COALESCE((
		    SELECT NULLIF(event.platform,'') FROM dashboard_account_events event
		    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
		      AND event.account_asset_id=asset.id AND event.event_type=$16
		      AND event.effective_date <= $12::date AND event.platform<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		  ),asset.initial_platform)=$3)
		  AND ($4='' OR COALESCE((
		    SELECT NULLIF(event.channel,'') FROM dashboard_account_events event
		    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
		      AND event.account_asset_id=asset.id AND event.event_type=$16
		      AND event.effective_date <= $12::date AND event.channel<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		  ),asset.initial_channel)=$4)
		  AND ($5='' OR COALESCE((
		    SELECT NULLIF(event.account_type,'') FROM dashboard_account_events event
		    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
		      AND event.account_asset_id=asset.id AND event.event_type=$16
		      AND event.effective_date <= $12::date AND event.account_type<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		  ),asset.initial_account_type)=$5)
		  AND ($6='' OR COALESCE((
		    SELECT event.status FROM dashboard_account_events event
		    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
		      AND event.account_asset_id=asset.id AND event.effective_date <= $12::date AND event.status<>''
		      AND event.event_type IN ($13,$14,$11)
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		  ),$15)=$6)
		  AND ($7='' OR COALESCE((
		    SELECT NULLIF(event.identifier,'') FROM dashboard_account_events event
		    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
		      AND event.account_asset_id=asset.id AND event.event_type=$16
		      AND event.effective_date <= $12::date AND event.identifier<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		  ),asset.initial_identifier) ILIKE '%' || $7 || '%')
		ORDER BY asset.updated_at DESC,asset.id ASC LIMIT $8 OFFSET $9
	`, userID, adminAccountID, filter.Platform, filter.Channel, filter.AccountType, filter.Status, filter.Search,
		filter.PageSize+1, (filter.Page-1)*filter.PageSize, KeyCostQualityComplete, AccountEventRefund,
		businesstime.Today(), AccountEventStatus, AccountEventRestore, AccountStatusUnactivated, AccountEventMetadataCorrection)
	if err != nil {
		return AccountAssetPage{}, err
	}
	defer rows.Close()
	items := make([]AccountAsset, 0)
	for rows.Next() {
		var item AccountAsset
		var revenueCents, upstreamCostCents *int64
		var refundCents int64
		if err := rows.Scan(&item.ID, &item.UserID, &item.AdminAccountID, &item.BatchID, &item.Identifier,
			&item.Platform, &item.Channel, &item.AccountType, &item.PurchaseCostCents, &item.QuotaTotalMicros,
			&item.AccountingMode, &item.RecognitionMode, &item.RecognitionStartDate, &item.RecognitionDays,
			&item.StatsMode, &item.CurrentStatus, &item.UpstreamReferenceURL, &item.CreatedAt, &item.UpdatedAt,
			&item.QuotaUsedMicros, &revenueCents, &upstreamCostCents, &item.StatsQuality, &refundCents,
			&item.HasActiveLink); err != nil {
			return AccountAssetPage{}, err
		}
		projected, err := projectAccountMetadataAtDate(ctx, r.db, item, businesstime.Today())
		if err != nil {
			return AccountAssetPage{}, err
		}
		item.Identifier, item.Platform, item.Channel = projected.Identifier, projected.Platform, projected.Channel
		item.AccountType, item.UpstreamReferenceURL = projected.AccountType, projected.UpstreamReferenceURL
		projectedStatus, projectedStatsMode, err := accountAssetStateAtDate(ctx, r.db, item, businesstime.Today())
		if err != nil {
			return AccountAssetPage{}, err
		}
		item.CurrentStatus, item.StatsMode = projectedStatus, projectedStatsMode
		if revenueCents != nil || item.QuotaUsedMicros != nil || upstreamCostCents != nil || refundCents > 0 {
			input := AccountPerformanceInput{
				Status: item.CurrentStatus, AccountingMode: item.AccountingMode, PurchaseCostCents: item.PurchaseCostCents,
				RefundCents: refundCents, HasIncompleteDailyStats: item.StatsQuality != KeyCostQualityComplete,
			}
			if revenueCents != nil {
				input.RevenueCents = *revenueCents
				input.HasRevenue = true
			}
			if item.QuotaUsedMicros != nil {
				input.RawQuotaUsedMicros = *item.QuotaUsedMicros
				input.HasRawQuotaUsed = true
			}
			if upstreamCostCents != nil {
				input.AdditiveUpstreamCostCents = *upstreamCostCents
				input.HasAdditiveUpstreamCost = true
			}
			performance := calculateAccountPerformance(input)
			item.Performance = &performance
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AccountAssetPage{}, err
	}
	hasMore := len(items) > filter.PageSize
	if hasMore {
		items = items[:filter.PageSize]
	}
	return AccountAssetPage{Items: items, HasMore: hasMore}, nil
}

func (r *MetricsRepository) ListAccountCostLedger(ctx context.Context, userID, adminAccountID string, filter AccountCostLedgerFilter) (AccountCostLedgerPage, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 500 {
		filter.PageSize = 100
	}
	rows, err := r.db.Query(ctx, `
		WITH matching AS (
			SELECT cost.*
			FROM dashboard_additional_costs cost
			LEFT JOIN dashboard_account_assets asset
			  ON asset.user_id=cost.user_id AND asset.admin_account_id=cost.admin_account_id AND asset.id=cost.account_asset_id
			WHERE cost.user_id=$1 AND cost.admin_account_id=$2
			  AND ($3='' OR cost.business_date >= $3::date) AND ($4='' OR cost.business_date <= $4::date)
			  AND ($5='' OR cost.type=$5)
			  AND ($6='' OR COALESCE((
			    SELECT NULLIF(event.platform,'') FROM dashboard_account_events event
			    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
			      AND event.account_asset_id=asset.id AND event.event_type=$12
			      AND event.effective_date <= cost.business_date AND event.platform<>''
			    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
			  ),asset.initial_platform)=$6)
			  AND ($7='' OR COALESCE((
			    SELECT NULLIF(event.channel,'') FROM dashboard_account_events event
			    WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
			      AND event.account_asset_id=asset.id AND event.event_type=$12
			      AND event.effective_date <= cost.business_date AND event.channel<>''
			    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
			  ),asset.initial_channel)=$7)
			  AND ($8='' OR cost.batch_id=$8) AND ($9='' OR cost.account_asset_id=$9)
		), selected_groups AS (
			SELECT type,batch_id,account_asset_id,name,COALESCE(NULLIF(source_id,''),id) AS entry_key,max(business_date) AS latest_date,
			       max(created_at) AS latest_created,count(*) OVER() AS total_groups
			FROM matching
			GROUP BY type,batch_id,account_asset_id,name,COALESCE(NULLIF(source_id,''),id)
			ORDER BY latest_date DESC,latest_created DESC,type,batch_id,account_asset_id,name
			LIMIT $10 OFFSET $11
		)
		SELECT cost.id,cost.type,cost.name,cost.business_date::text,
		       cost.amount_cents,cost.original_amount,cost.rate,cost.usage_rate,cost.days,cost.source_id,
		       cost.batch_id,cost.account_asset_id,cost.note,cost.estimated,cost.created_at,selected.total_groups
		FROM matching cost
		JOIN selected_groups selected ON selected.type=cost.type AND selected.batch_id=cost.batch_id
			 AND selected.account_asset_id=cost.account_asset_id AND selected.name=cost.name
			 AND selected.entry_key=COALESCE(NULLIF(cost.source_id,''),cost.id)
		ORDER BY selected.latest_date DESC,selected.latest_created DESC,cost.business_date DESC,cost.created_at DESC,cost.id DESC
	`, userID, adminAccountID, strings.TrimSpace(filter.From), strings.TrimSpace(filter.To), strings.TrimSpace(filter.Type),
		strings.TrimSpace(filter.Platform), strings.TrimSpace(filter.Channel), strings.TrimSpace(filter.BatchID),
		strings.TrimSpace(filter.AccountAssetID), filter.PageSize, (filter.Page-1)*filter.PageSize,
		AccountEventMetadataCorrection)
	if err != nil {
		return AccountCostLedgerPage{}, err
	}
	defer rows.Close()
	items := make([]AdditionalCostRecord, 0)
	var totalGroups int
	for rows.Next() {
		var item AdditionalCostRecord
		if err := rows.Scan(&item.ID, &item.Type, &item.Name, &item.BusinessDate,
			&item.AmountCents, &item.OriginalAmount, &item.Rate, &item.UsageRate, &item.Days,
			&item.SourceID, &item.BatchID, &item.AccountAssetID, &item.Note, &item.Estimated, &item.CreatedAt, &totalGroups); err != nil {
			return AccountCostLedgerPage{}, err
		}
		item.UserID = userID
		item.AdminAccountID = adminAccountID
		item.Amount = float64(item.AmountCents) / 100
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AccountCostLedgerPage{}, err
	}
	return AccountCostLedgerPage{Items: items, HasMore: filter.Page*filter.PageSize < totalGroups}, nil
}

func (r *MetricsRepository) ListAutomaticAccountTargets(ctx context.Context, userID, adminAccountID, date string) ([]AutomaticAccountTarget, error) {
	rows, err := r.db.Query(ctx, `
		SELECT asset.id,asset.accounting_mode,asset.recognition_mode,asset.quota_total_micros,
		       COALESCE(link.id,''),COALESCE(link.account_asset_id,''),COALESCE(link.connection_id,''),
		       COALESCE(link.upstream_site_id,''),COALESCE(link.upstream_key_id,''),
		       COALESCE(link.scope_admin_account_id,''),COALESCE(link.own_group_id,''),
		       COALESCE(link.connection_name,''),COALESCE(link.site_name,''),COALESCE(link.key_name,''),
		       COALESCE(link.own_group_name,''),COALESCE(link.effective_from::text,''),link.effective_to::text,
		       COALESCE(link.manual_same_day_split,false),COALESCE(link.created_at,'epoch'::timestamptz)
		FROM dashboard_account_assets asset
		JOIN dashboard_account_batches batch
		  ON batch.user_id=asset.user_id AND batch.admin_account_id=asset.admin_account_id AND batch.id=asset.batch_id
		LEFT JOIN LATERAL (
			SELECT candidate.* FROM dashboard_account_links candidate
			JOIN real_connections live
			  ON live.id=candidate.connection_id AND live.user_id=candidate.user_id
			 AND live.workspace_admin_account_id=candidate.admin_account_id
			 AND live.upstream_site_id=candidate.upstream_site_id AND live.upstream_key_id=candidate.upstream_key_id
			 AND live.admin_account_id=candidate.scope_admin_account_id
			 AND live.status='active' AND jsonb_array_length(live.own_group_ids)=1
			 AND live.own_group_ids->>0=candidate.own_group_id
			WHERE candidate.user_id=asset.user_id AND candidate.admin_account_id=asset.admin_account_id
			  AND candidate.account_asset_id=asset.id
			  AND candidate.effective_from <= $6::date AND (candidate.effective_to IS NULL OR candidate.effective_to >= $6::date)
			  AND NOT EXISTS (
				SELECT 1 FROM real_connections other_live
				WHERE other_live.user_id=live.user_id AND other_live.workspace_admin_account_id=live.workspace_admin_account_id
				  AND other_live.id<>live.id AND other_live.status='active' AND jsonb_array_length(other_live.own_group_ids)=1
				  AND ((other_live.upstream_site_id=live.upstream_site_id AND other_live.upstream_key_id=live.upstream_key_id)
				    OR (other_live.admin_account_id=live.admin_account_id AND other_live.own_group_ids->>0=live.own_group_ids->>0))
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM dashboard_account_links other_link
				WHERE other_link.user_id=candidate.user_id AND other_link.admin_account_id=candidate.admin_account_id
				  AND other_link.id<>candidate.id
				  AND other_link.effective_from <= $6::date AND (other_link.effective_to IS NULL OR other_link.effective_to >= $6::date)
				  AND (other_link.connection_id=candidate.connection_id
				    OR (other_link.upstream_site_id=candidate.upstream_site_id AND other_link.upstream_key_id=candidate.upstream_key_id)
				    OR (other_link.scope_admin_account_id=candidate.scope_admin_account_id AND other_link.own_group_id=candidate.own_group_id))
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM dashboard_account_links split
				WHERE split.user_id=asset.user_id AND split.admin_account_id=asset.admin_account_id
				  AND split.account_asset_id=asset.id AND split.manual_same_day_split=true
				  AND (split.effective_from = $6::date OR split.effective_to = $6::date)
			  )
			ORDER BY candidate.created_at DESC,candidate.id DESC LIMIT 1
		) link ON true
		LEFT JOIN LATERAL (
			SELECT event.stats_mode FROM dashboard_account_events event
			WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
			  AND event.account_asset_id=asset.id AND event.event_type=$7 AND event.effective_date <= $6::date
			ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		) projected_mode ON true
		LEFT JOIN LATERAL (
			SELECT event.status FROM dashboard_account_events event
			WHERE event.user_id=asset.user_id AND event.admin_account_id=asset.admin_account_id
			  AND event.account_asset_id=asset.id AND event.status <> '' AND event.effective_date <= $6::date
			  AND event.event_type IN ($8,$9,$10)
			ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1
		) projected_status ON true
		WHERE asset.user_id=$1 AND asset.admin_account_id=$2 AND COALESCE(projected_mode.stats_mode,batch.stats_mode)=$3
		  AND COALESCE(projected_status.status,'unactivated') IN ($4,$5)
		  AND NOT EXISTS (
			SELECT 1 FROM dashboard_account_links manual_split
			WHERE manual_split.user_id=asset.user_id AND manual_split.admin_account_id=asset.admin_account_id
			  AND manual_split.account_asset_id=asset.id AND manual_split.manual_same_day_split=true
			  AND (manual_split.effective_from = $6::date OR manual_split.effective_to = $6::date)
		  )
		ORDER BY asset.id
	`, userID, adminAccountID, StatsModeAutomatic, AccountStatusUnactivated, AccountStatusActive, date,
		AccountEventStatsModeChange, AccountEventStatus, AccountEventRestore, AccountEventRefund)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AutomaticAccountTarget, 0)
	for rows.Next() {
		var target AutomaticAccountTarget
		var effectiveTo *string
		if err := rows.Scan(&target.Asset.ID, &target.Asset.AccountingMode, &target.Asset.RecognitionMode, &target.Asset.QuotaTotalMicros,
			&target.Link.ID, &target.Link.AccountAssetID, &target.Link.ConnectionID, &target.Link.UpstreamSiteID, &target.Link.UpstreamKeyID,
			&target.Link.ScopeAdminAccountID, &target.Link.OwnGroupID, &target.Link.ConnectionName, &target.Link.SiteName,
			&target.Link.KeyName, &target.Link.OwnGroupName, &target.Link.EffectiveFrom, &effectiveTo,
			&target.Link.ManualSameDaySplit, &target.Link.CreatedAt); err != nil {
			return nil, err
		}
		target.Asset.UserID, target.Asset.AdminAccountID = userID, adminAccountID
		target.Link.UserID, target.Link.AdminAccountID = userID, adminAccountID
		target.Link.EffectiveTo = effectiveTo
		items = append(items, target)
	}
	return items, rows.Err()
}

func (r *MetricsRepository) SaveAutomaticAccountDailyStats(ctx context.Context, stats []AccountDailyStat) error {
	if len(stats) == 0 {
		return nil
	}
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return errors.New("account stats repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.saveAutomaticAccountDailyStats(ctx, tx, stats); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *MetricsRepository) saveAutomaticAccountDailyStats(ctx context.Context, db metricsDB, stats []AccountDailyStat) error {
	type quotaRebuild struct {
		asset     AccountAsset
		createdAt time.Time
	}
	affectedAssets := make(map[string]quotaRebuild)
	quotaAssets := make(map[string]quotaRebuild)
	for _, stat := range stats {
		asset := AccountAsset{ID: stat.AccountAssetID, UserID: stat.UserID, AdminAccountID: stat.AdminAccountID}
		if err := db.QueryRow(ctx, `
			SELECT purchase_cost_cents,quota_total_micros,recognition_mode,identifier,batch_id,accounting_mode
			FROM dashboard_account_assets
			WHERE user_id=$1 AND admin_account_id=$2 AND id=$3 FOR UPDATE
		`, stat.UserID, stat.AdminAccountID, stat.AccountAssetID).Scan(
			&asset.PurchaseCostCents, &asset.QuotaTotalMicros, &asset.RecognitionMode, &asset.Identifier,
			&asset.BatchID, &asset.AccountingMode,
		); err != nil {
			return err
		}
		_, statsMode, err := accountAssetStateAtDate(ctx, db, asset, stat.BusinessDate)
		if err != nil {
			return err
		}
		if statsMode != StatsModeAutomatic || stat.Source != StatsModeAutomatic {
			return errInvalidAccountBatch
		}
		if stat.Quality == KeyCostQualityComplete && (stat.RawQuotaUsedMicros == nil || stat.RevenueCents == nil ||
			asset.AccountingMode == AccountingModeAdditive && stat.UpstreamCostCents == nil) {
			return errInvalidAccountBatch
		}
		if asset.RecognitionMode == RecognitionModeQuota {
			if asset.QuotaTotalMicros == nil || stat.RawQuotaUsedMicros == nil {
				return errInvalidAccountBatch
			}
			stat.RecognizedCostCents = 0
			quotaAssets[asset.ID] = quotaRebuild{asset: asset, createdAt: stat.UpdatedAt}
		}
		affectedAssets[asset.ID] = quotaRebuild{asset: asset, createdAt: stat.UpdatedAt}
		commandTag, err := db.Exec(ctx, `
			INSERT INTO dashboard_account_daily_stats (
				id,user_id,admin_account_id,account_asset_id,business_date,source,quality,key_cost_run_id,
				raw_quota_used_micros,revenue_cents,upstream_cost_cents,recognized_cost_cents,
				replacement_deduction_cents,observed_at,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT (user_id,admin_account_id,account_asset_id,business_date) DO UPDATE SET
				source=EXCLUDED.source,quality=EXCLUDED.quality,key_cost_run_id=EXCLUDED.key_cost_run_id,
				raw_quota_used_micros=EXCLUDED.raw_quota_used_micros,revenue_cents=EXCLUDED.revenue_cents,
				upstream_cost_cents=EXCLUDED.upstream_cost_cents,recognized_cost_cents=EXCLUDED.recognized_cost_cents,
				replacement_deduction_cents=EXCLUDED.replacement_deduction_cents,
				observed_at=EXCLUDED.observed_at,updated_at=EXCLUDED.updated_at
			WHERE dashboard_account_daily_stats.source=$6
		`, stat.ID, stat.UserID, stat.AdminAccountID, stat.AccountAssetID, stat.BusinessDate, stat.Source,
			stat.Quality, stat.KeyCostRunID, stat.RawQuotaUsedMicros, stat.RevenueCents, stat.UpstreamCostCents,
			stat.RecognizedCostCents, stat.ReplacementDeductionCents, stat.ObservedAt, stat.CreatedAt, stat.UpdatedAt)
		if err != nil {
			return err
		}
		if commandTag.RowsAffected() != 1 {
			return errors.New("account daily stat source conflict")
		}
	}
	for _, rebuild := range affectedAssets {
		if err := r.rebuildManualAccountDailyStats(ctx, db, rebuild.asset); err != nil {
			return err
		}
	}
	for _, rebuild := range quotaAssets {
		if err := r.rebuildQuotaRecognitionCosts(ctx, db, rebuild.asset, rebuild.createdAt); err != nil {
			return err
		}
	}
	return nil
}

func (r *MetricsRepository) GetAccountAsset(ctx context.Context, userID, adminAccountID, assetID string) (AccountAsset, error) {
	item, err := scanAccountAsset(r.db.QueryRow(ctx, `
		SELECT id,user_id,admin_account_id,batch_id,identifier,platform,channel,account_type,purchase_cost_cents,
		       quota_total_micros,accounting_mode,recognition_mode,recognition_start_date::text,recognition_days,
		       stats_mode,current_status,upstream_reference_url,created_at,updated_at
		FROM dashboard_account_assets WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, userID, adminAccountID, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountAsset{}, ErrAccountAssetNotFound
	}
	if err != nil {
		return AccountAsset{}, err
	}
	item, err = projectAccountMetadataAtDate(ctx, r.db, item, businesstime.Today())
	if err != nil {
		return AccountAsset{}, err
	}
	item.CurrentStatus, item.StatsMode, err = accountAssetStateAtDate(ctx, r.db, item, businesstime.Today())
	return item, err
}

type accountAssetScanner interface {
	Scan(dest ...any) error
}

func scanAccountAsset(scanner accountAssetScanner) (AccountAsset, error) {
	var item AccountAsset
	err := scanner.Scan(&item.ID, &item.UserID, &item.AdminAccountID, &item.BatchID, &item.Identifier,
		&item.Platform, &item.Channel, &item.AccountType, &item.PurchaseCostCents, &item.QuotaTotalMicros,
		&item.AccountingMode, &item.RecognitionMode, &item.RecognitionStartDate, &item.RecognitionDays,
		&item.StatsMode, &item.CurrentStatus, &item.UpstreamReferenceURL, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (r *MetricsRepository) AppendAccountEvent(ctx context.Context, event AccountEvent) (AccountEventResult, error) {
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return AccountEventResult{}, errors.New("account asset repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return AccountEventResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	asset, err := scanAccountAsset(tx.QueryRow(ctx, `
		SELECT id,user_id,admin_account_id,batch_id,identifier,platform,channel,account_type,purchase_cost_cents,
		       quota_total_micros,accounting_mode,recognition_mode,recognition_start_date::text,recognition_days,
		       stats_mode,current_status,upstream_reference_url,created_at,updated_at
		FROM dashboard_account_assets WHERE user_id=$1 AND admin_account_id=$2 AND id=$3 FOR UPDATE
	`, event.UserID, event.AdminAccountID, event.AccountAssetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountEventResult{}, ErrAccountAssetNotFound
	}
	if err != nil {
		return AccountEventResult{}, err
	}
	if existing, found, err := loadAccountEventByIdempotency(ctx, tx, event.UserID, event.AdminAccountID, event.AccountAssetID, event.IdempotencyKey); err != nil {
		return AccountEventResult{}, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return AccountEventResult{}, err
		}
		return AccountEventResult{Event: existing, Asset: asset}, nil
	}
	if event.ID == "" || strings.TrimSpace(event.IdempotencyKey) == "" {
		return AccountEventResult{}, errInvalidAccountBatch
	}
	if _, err := time.ParseInLocation("2006-01-02", event.EffectiveDate, businesstime.Location()); err != nil {
		return AccountEventResult{}, errInvalidAccountBatch
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	var purchaseDate string
	if err := tx.QueryRow(ctx, `
		SELECT purchase_date::text FROM dashboard_account_batches
		WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, event.UserID, event.AdminAccountID, asset.BatchID).Scan(&purchaseDate); err != nil {
		return AccountEventResult{}, err
	}
	if event.EffectiveDate < purchaseDate {
		return AccountEventResult{}, errInvalidAccountBatch
	}
	statusAtEffectiveDate, statsModeAtEffectiveDate, err := accountAssetStateAtDate(ctx, tx, asset, event.EffectiveDate)
	if err != nil {
		return AccountEventResult{}, err
	}

	costRecords := make([]AdditionalCostRecord, 0)
	terminalTransition := false
	switch event.EventType {
	case AccountEventStatus:
		if event.Status != AccountStatusActive && !isTerminalAccountStatus(event.Status) {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		if isTerminalAccountStatus(statusAtEffectiveDate) && event.Status == AccountStatusActive {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		terminalTransition = isTerminalAccountStatus(event.Status) && !isTerminalAccountStatus(statusAtEffectiveDate)
		if terminalTransition {
			existing, err := listAccountPurchaseAmounts(ctx, tx, event.UserID, event.AdminAccountID, event.AccountAssetID)
			if err != nil {
				return AccountEventResult{}, err
			}
			adjustments, err := terminalCostAdjustments(asset.PurchaseCostCents, event.EffectiveDate, existing)
			if err != nil {
				return AccountEventResult{}, err
			}
			for _, adjustment := range adjustments {
				costRecords = append(costRecords, accountEventCostRecord(event, asset, AdditionalCostAccountPurchase, adjustment.BusinessDate, adjustment.AmountCents, "terminal_rebalance"))
			}
		}
	case AccountEventRestore:
		if !isTerminalAccountStatus(statusAtEffectiveDate) || event.Status != AccountStatusActive {
			return AccountEventResult{}, errInvalidAccountBatch
		}
	case AccountEventQuotaObservation:
		if asset.RecognitionMode != RecognitionModeQuota || asset.QuotaTotalMicros == nil || statsModeAtEffectiveDate != StatsModeManual || event.QuotaUsedMicros == nil || *event.QuotaUsedMicros < 0 {
			return AccountEventResult{}, errInvalidAccountBatch
		}
	case AccountEventManualObservation:
		if statsModeAtEffectiveDate != StatsModeManual || event.QuotaUsedMicros == nil && event.RevenueCents == nil && event.UpstreamCostCents == nil {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		for _, value := range []*int64{event.QuotaUsedMicros, event.RevenueCents, event.UpstreamCostCents} {
			if value != nil && *value < 0 {
				return AccountEventResult{}, errInvalidAccountBatch
			}
		}
		var existingSource string
		err := tx.QueryRow(ctx, `
			SELECT source FROM dashboard_account_daily_stats
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date=$4::date
		`, event.UserID, event.AdminAccountID, event.AccountAssetID, event.EffectiveDate).Scan(&existingSource)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return AccountEventResult{}, err
		}
		if err == nil && existingSource != StatsModeManual {
			return AccountEventResult{}, errInvalidAccountBatch
		}
	case AccountEventStatsModeChange:
		if event.StatsMode != StatsModeAutomatic && event.StatsMode != StatsModeManual || event.StatsMode == statsModeAtEffectiveDate {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		if event.StatsMode == StatsModeAutomatic {
			var hasActiveLink bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM dashboard_account_links
				WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
				  AND effective_from <= $4::date AND (effective_to IS NULL OR effective_to >= $4::date))
			`, event.UserID, event.AdminAccountID, event.AccountAssetID, event.EffectiveDate).Scan(&hasActiveLink); err != nil {
				return AccountEventResult{}, err
			}
			if !hasActiveLink {
				return AccountEventResult{}, errInvalidAccountBatch
			}
		}
	case AccountEventMetadataCorrection:
		if event.Identifier == "" && event.Platform == "" && event.Channel == "" && event.AccountType == "" && event.PurchaseURL == nil && event.UpstreamReferenceURL == nil {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		if event.PurchaseURL != nil {
			*event.PurchaseURL = strings.TrimSpace(*event.PurchaseURL)
			if err := validateAccountReferenceURL(*event.PurchaseURL); err != nil {
				return AccountEventResult{}, err
			}
		}
		if event.UpstreamReferenceURL != nil {
			if err := validateAccountReferenceURL(*event.UpstreamReferenceURL); err != nil {
				return AccountEventResult{}, err
			}
		}
	case AccountEventRefund:
		if event.RefundCents == nil || *event.RefundCents <= 0 {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		var previousRefund int64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(refund_cents),0) FROM dashboard_account_events
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND event_type=$4
		`, event.UserID, event.AdminAccountID, event.AccountAssetID, AccountEventRefund).Scan(&previousRefund); err != nil {
			return AccountEventResult{}, err
		}
		if previousRefund+*event.RefundCents > asset.PurchaseCostCents {
			return AccountEventResult{}, errInvalidAccountBatch
		}
		costRecords = append(costRecords, accountEventCostRecord(event, asset, AdditionalCostAccountRefund, event.EffectiveDate, -*event.RefundCents, "refund"))
		if event.Status != "" {
			if event.Status != AccountStatusClosed {
				return AccountEventResult{}, errInvalidAccountBatch
			}
			terminalTransition = !isTerminalAccountStatus(statusAtEffectiveDate)
			if terminalTransition {
				existing, err := listAccountPurchaseAmounts(ctx, tx, event.UserID, event.AdminAccountID, event.AccountAssetID)
				if err != nil {
					return AccountEventResult{}, err
				}
				adjustments, err := terminalCostAdjustments(asset.PurchaseCostCents, event.EffectiveDate, existing)
				if err != nil {
					return AccountEventResult{}, err
				}
				for _, adjustment := range adjustments {
					costRecords = append(costRecords, accountEventCostRecord(event, asset, AdditionalCostAccountPurchase, adjustment.BusinessDate, adjustment.AmountCents, "terminal_rebalance"))
				}
			}
		}
	default:
		return AccountEventResult{}, errInvalidAccountBatch
	}
	if event.EventType == AccountEventQuotaObservation || event.EventType == AccountEventManualObservation {
		observations, err := listAccountCumulativeObservations(ctx, tx, event.UserID, event.AdminAccountID, event.AccountAssetID)
		if err != nil {
			return AccountEventResult{}, err
		}
		if err := validateAccountCumulativeObservations(append(observations, event)); err != nil {
			return AccountEventResult{}, err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO dashboard_account_events (
			id,user_id,admin_account_id,account_asset_id,event_type,effective_date,status,quota_used_micros,
			revenue_cents,refund_cents,upstream_cost_cents,stats_mode,identifier,platform,channel,account_type,
			purchase_url,upstream_reference_url,note,idempotency_key,created_at
		) VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
	`, event.ID, event.UserID, event.AdminAccountID, event.AccountAssetID, event.EventType, event.EffectiveDate,
		event.Status, event.QuotaUsedMicros, event.RevenueCents, event.RefundCents, event.UpstreamCostCents,
		event.StatsMode, event.Identifier, event.Platform, event.Channel, event.AccountType,
		event.PurchaseURL, event.UpstreamReferenceURL, event.Note, event.IdempotencyKey, event.CreatedAt); err != nil {
		return AccountEventResult{}, err
	}
	if terminalTransition {
		if _, err := tx.Exec(ctx, `
			UPDATE dashboard_account_links SET effective_to=$4::date
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
			  AND effective_from <= $4::date AND (effective_to IS NULL OR effective_to > $4::date)
		`, event.UserID, event.AdminAccountID, event.AccountAssetID, event.EffectiveDate); err != nil {
			return AccountEventResult{}, err
		}
	}
	projectedStatus, projectedStatsMode, err := accountAssetStateAtDate(ctx, tx, asset, businesstime.Today())
	if err != nil {
		return AccountEventResult{}, err
	}
	if projectedStatus != asset.CurrentStatus || projectedStatsMode != asset.StatsMode {
		if _, err := tx.Exec(ctx, `
			UPDATE dashboard_account_assets SET current_status=$4,stats_mode=$5,updated_at=$6
			WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
		`, event.UserID, event.AdminAccountID, event.AccountAssetID, projectedStatus, projectedStatsMode, event.CreatedAt); err != nil {
			return AccountEventResult{}, err
		}
		asset.CurrentStatus = projectedStatus
		asset.StatsMode = projectedStatsMode
		asset.UpdatedAt = event.CreatedAt
	}
	projectedMetadata, err := projectAccountMetadataAtDate(ctx, tx, asset, businesstime.Today())
	if err != nil {
		return AccountEventResult{}, err
	}
	if projectedMetadata.Identifier != asset.Identifier || projectedMetadata.Platform != asset.Platform ||
		projectedMetadata.Channel != asset.Channel || projectedMetadata.AccountType != asset.AccountType ||
		projectedMetadata.UpstreamReferenceURL != asset.UpstreamReferenceURL {
		if _, err := tx.Exec(ctx, `
			UPDATE dashboard_account_assets SET identifier=$4,platform=$5,channel=$6,account_type=$7,
			  upstream_reference_url=$8,updated_at=$9
			WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
		`, event.UserID, event.AdminAccountID, event.AccountAssetID, projectedMetadata.Identifier,
			projectedMetadata.Platform, projectedMetadata.Channel, projectedMetadata.AccountType,
			projectedMetadata.UpstreamReferenceURL, event.CreatedAt); err != nil {
			return AccountEventResult{}, err
		}
		asset.Identifier, asset.Platform, asset.Channel = projectedMetadata.Identifier, projectedMetadata.Platform, projectedMetadata.Channel
		asset.AccountType, asset.UpstreamReferenceURL = projectedMetadata.AccountType, projectedMetadata.UpstreamReferenceURL
		asset.UpdatedAt = event.CreatedAt
	}
	if err := r.insertAdditionalCosts(ctx, tx, costRecords); err != nil {
		return AccountEventResult{}, err
	}
	if event.EventType == AccountEventQuotaObservation || event.EventType == AccountEventManualObservation {
		if err := r.rebuildManualAccountDailyStats(ctx, tx, asset); err != nil {
			return AccountEventResult{}, err
		}
		if asset.RecognitionMode == RecognitionModeQuota {
			if err := r.rebuildQuotaRecognitionCosts(ctx, tx, asset, event.CreatedAt); err != nil {
				return AccountEventResult{}, err
			}
		}
	}
	if asset.RecognitionMode == RecognitionModeQuota && isTerminalAccountStatus(event.Status) &&
		event.EventType != AccountEventQuotaObservation && event.EventType != AccountEventManualObservation {
		if err := r.rebuildQuotaRecognitionCosts(ctx, tx, asset, event.CreatedAt); err != nil {
			return AccountEventResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return AccountEventResult{}, err
	}
	return AccountEventResult{Event: event, Asset: asset, CostRecords: costRecords}, nil
}

func listAccountCumulativeObservations(ctx context.Context, db metricsDB, userID, adminAccountID, assetID string) ([]AccountEvent, error) {
	rows, err := db.Query(ctx, `
		SELECT id,effective_date::text,quota_used_micros,revenue_cents,upstream_cost_cents,created_at
		FROM dashboard_account_events
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND event_type IN ($4,$5)
		ORDER BY effective_date,created_at,id
	`, userID, adminAccountID, assetID, AccountEventQuotaObservation, AccountEventManualObservation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountEvent, 0)
	for rows.Next() {
		var item AccountEvent
		item.UserID, item.AdminAccountID, item.AccountAssetID = userID, adminAccountID, assetID
		if err := rows.Scan(&item.ID, &item.EffectiveDate, &item.QuotaUsedMicros, &item.RevenueCents, &item.UpstreamCostCents, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func validateAccountCumulativeObservations(events []AccountEvent) error {
	sort.Slice(events, func(i, j int) bool {
		if events[i].EffectiveDate != events[j].EffectiveDate {
			return events[i].EffectiveDate < events[j].EffectiveDate
		}
		if !events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].CreatedAt.Before(events[j].CreatedAt)
		}
		return events[i].ID < events[j].ID
	})
	var previousQuota, previousRevenue, previousUpstream *int64
	for _, event := range events {
		for _, pair := range []struct {
			current  *int64
			previous **int64
		}{{event.QuotaUsedMicros, &previousQuota}, {event.RevenueCents, &previousRevenue}, {event.UpstreamCostCents, &previousUpstream}} {
			if pair.current == nil {
				continue
			}
			if *pair.current < 0 || *pair.previous != nil && *pair.current < **pair.previous {
				return errInvalidAccountBatch
			}
			value := *pair.current
			*pair.previous = &value
		}
	}
	return nil
}

type cumulativeAccountObservation struct {
	date                           string
	quota, revenue, upstream       *int64
	observedAt                     time.Time
	previousQuota, previousRevenue int64
	previousUpstream               int64
	hasPreviousQuota               bool
	hasPreviousRevenue             bool
	hasPreviousUpstream            bool
}

func (r *MetricsRepository) rebuildManualAccountDailyStats(ctx context.Context, db metricsDB, asset AccountAsset) error {
	events, err := listAccountCumulativeObservations(ctx, db, asset.UserID, asset.AdminAccountID, asset.ID)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
		DELETE FROM dashboard_account_daily_stats
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND source=$4
	`, asset.UserID, asset.AdminAccountID, asset.ID, StatsModeManual); err != nil {
		return err
	}
	byDate := make(map[string]cumulativeAccountObservation)
	var quota, revenue, upstreamCost *int64
	for _, event := range events {
		if event.QuotaUsedMicros != nil {
			value := *event.QuotaUsedMicros
			quota = &value
		}
		if event.RevenueCents != nil {
			value := *event.RevenueCents
			revenue = &value
		}
		if event.UpstreamCostCents != nil {
			value := *event.UpstreamCostCents
			upstreamCost = &value
		}
		byDate[event.EffectiveDate] = cumulativeAccountObservation{
			date: event.EffectiveDate, quota: quota, revenue: revenue, upstream: upstreamCost, observedAt: event.CreatedAt,
		}
	}
	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	for _, date := range dates {
		observation := byDate[date]
		var previousQuota, previousRevenue, previousUpstream int64
		if err := db.QueryRow(ctx, `
			SELECT COALESCE(sum(raw_quota_used_micros),0),COALESCE(sum(revenue_cents),0),
			       COALESCE(sum(upstream_cost_cents),0)
			FROM dashboard_account_daily_stats
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
			  AND business_date < $4::date
		`, asset.UserID, asset.AdminAccountID, asset.ID, date).Scan(
			&previousQuota, &previousRevenue, &previousUpstream,
		); err != nil {
			return err
		}
		var quotaDelta, revenueDelta, upstreamDelta *int64
		if observation.quota != nil {
			value := *observation.quota - previousQuota
			if value < 0 {
				return errInvalidAccountBatch
			}
			quotaDelta = &value
		}
		if observation.revenue != nil {
			value := *observation.revenue - previousRevenue
			if value < 0 {
				return errInvalidAccountBatch
			}
			revenueDelta = &value
		}
		if observation.upstream != nil {
			value := *observation.upstream - previousUpstream
			if value < 0 {
				return errInvalidAccountBatch
			}
			upstreamDelta = &value
		}
		quality := KeyCostQualityMissing
		if observation.quota != nil && observation.revenue != nil && (asset.AccountingMode != AccountingModeAdditive || observation.upstream != nil) {
			quality = KeyCostQualityComplete
		}
		commandTag, err := db.Exec(ctx, `
			INSERT INTO dashboard_account_daily_stats (
				id,user_id,admin_account_id,account_asset_id,business_date,source,quality,
				raw_quota_used_micros,revenue_cents,upstream_cost_cents,observed_at,created_at,updated_at
			) VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$11,$11)
			ON CONFLICT (user_id,admin_account_id,account_asset_id,business_date) DO UPDATE SET
				quality=EXCLUDED.quality,raw_quota_used_micros=EXCLUDED.raw_quota_used_micros,
				revenue_cents=EXCLUDED.revenue_cents,upstream_cost_cents=EXCLUDED.upstream_cost_cents,
				observed_at=EXCLUDED.observed_at,updated_at=EXCLUDED.updated_at
			WHERE dashboard_account_daily_stats.source=EXCLUDED.source
		`, mustMetricsID(), asset.UserID, asset.AdminAccountID, asset.ID, date, StatsModeManual, quality,
			quotaDelta, revenueDelta, upstreamDelta, observation.observedAt)
		if err != nil {
			return err
		}
		if commandTag.RowsAffected() != 1 {
			return errors.New("account daily stat source conflict")
		}
	}
	return nil
}

func (r *MetricsRepository) rebuildQuotaRecognitionCosts(ctx context.Context, db metricsDB, asset AccountAsset, createdAt time.Time) error {
	if asset.QuotaTotalMicros == nil || *asset.QuotaTotalMicros <= 0 {
		return errInvalidAccountBatch
	}
	rows, err := db.Query(ctx, `
		SELECT business_date::text,COALESCE(sum(raw_quota_used_micros),0)::bigint
		FROM dashboard_account_daily_stats
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		GROUP BY business_date ORDER BY business_date
	`, asset.UserID, asset.AdminAccountID, asset.ID)
	if err != nil {
		return err
	}
	dailyQuota := make(map[string]int64)
	for rows.Next() {
		var date string
		var amount int64
		if err := rows.Scan(&date, &amount); err != nil {
			rows.Close()
			return err
		}
		dailyQuota[date] = amount
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var terminalDate *string
	var terminal string
	err = db.QueryRow(ctx, `
		SELECT effective_date::text FROM dashboard_account_events
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND status IN ($4,$5,$6)
		ORDER BY effective_date,created_at,id LIMIT 1
	`, asset.UserID, asset.AdminAccountID, asset.ID, AccountStatusExhausted, AccountStatusDead, AccountStatusClosed).Scan(&terminal)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		terminalDate = &terminal
		dailyQuota[terminal] += 0
	}
	dates := make([]string, 0, len(dailyQuota))
	for date := range dailyQuota {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	desired := make(map[string]int64, len(dates))
	var cumulativeQuota, previousDue int64
	for _, date := range dates {
		cumulativeQuota += dailyQuota[date]
		terminal := terminalDate != nil && date >= *terminalDate
		delta, err := recognizeQuotaCost(asset.PurchaseCostCents, *asset.QuotaTotalMicros, cumulativeQuota, previousDue, terminal)
		if err != nil {
			return err
		}
		desired[date] = delta
		previousDue += delta
	}
	for date, recognized := range desired {
		if _, err := db.Exec(ctx, `
			UPDATE dashboard_account_daily_stats SET recognized_cost_cents=$5,updated_at=$6
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date=$4::date
		`, asset.UserID, asset.AdminAccountID, asset.ID, date, recognized, createdAt); err != nil {
			return err
		}
	}
	rows, err = db.Query(ctx, `
		SELECT business_date::text,COALESCE(sum(amount_cents),0)::bigint
		FROM dashboard_additional_costs
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND type=$4
		GROUP BY business_date
	`, asset.UserID, asset.AdminAccountID, asset.ID, AdditionalCostAccountPurchase)
	if err != nil {
		return err
	}
	actual := make(map[string]int64)
	for rows.Next() {
		var date string
		var amount int64
		if err := rows.Scan(&date, &amount); err != nil {
			rows.Close()
			return err
		}
		actual[date] = amount
		if _, exists := desired[date]; !exists {
			desired[date] = 0
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	records := make([]AdditionalCostRecord, 0)
	for date, want := range desired {
		if delta := want - actual[date]; delta != 0 {
			records = append(records, AdditionalCostRecord{
				ID: mustMetricsID(), UserID: asset.UserID, AdminAccountID: asset.AdminAccountID,
				Type: AdditionalCostAccountPurchase, Name: asset.Identifier, BusinessDate: date,
				AmountCents: delta, Amount: float64(delta) / 100, OriginalAmount: float64(asset.PurchaseCostCents) / 100,
				SourceID: asset.ID, BatchID: asset.BatchID, AccountAssetID: asset.ID,
				Note: "quota_reprojection", CreatedAt: createdAt,
			})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].BusinessDate < records[j].BusinessDate })
	return r.insertAdditionalCosts(ctx, db, records)
}

func accountAssetStateAtDate(ctx context.Context, db metricsDB, asset AccountAsset, date string) (string, string, error) {
	status := AccountStatusUnactivated
	err := db.QueryRow(ctx, `
		SELECT status FROM dashboard_account_events
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		  AND effective_date <= $4::date AND status <> ''
		  AND event_type IN ($5,$6,$7)
		ORDER BY effective_date DESC,created_at DESC,id DESC LIMIT 1
	`, asset.UserID, asset.AdminAccountID, asset.ID, date, AccountEventStatus, AccountEventRestore, AccountEventRefund).Scan(&status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	statsMode := StatsModeManual
	if err := db.QueryRow(ctx, `
		SELECT stats_mode FROM dashboard_account_batches
		WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, asset.UserID, asset.AdminAccountID, asset.BatchID).Scan(&statsMode); err != nil {
		return "", "", err
	}
	err = db.QueryRow(ctx, `
		SELECT stats_mode FROM dashboard_account_events
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		  AND effective_date <= $4::date AND event_type=$5
		ORDER BY effective_date DESC,created_at DESC,id DESC LIMIT 1
	`, asset.UserID, asset.AdminAccountID, asset.ID, date, AccountEventStatsModeChange).Scan(&statsMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	return status, statsMode, nil
}

func projectAccountMetadataAtDate(ctx context.Context, db metricsDB, asset AccountAsset, date string) (AccountAsset, error) {
	var initialIdentifier, initialPlatform, initialChannel, initialAccountType, initialUpstreamReferenceURL string
	if err := db.QueryRow(ctx, `
		SELECT initial_identifier,initial_platform,initial_channel,initial_account_type,initial_upstream_reference_url
		FROM dashboard_account_assets
		WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, asset.UserID, asset.AdminAccountID, asset.ID).Scan(
		&initialIdentifier, &initialPlatform, &initialChannel, &initialAccountType, &initialUpstreamReferenceURL,
	); err != nil {
		return AccountAsset{}, err
	}
	err := db.QueryRow(ctx, `
		SELECT
		  COALESCE((SELECT NULLIF(identifier,'') FROM dashboard_account_events event
		    WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		      AND event.event_type=$4 AND event.effective_date <= $5::date AND identifier<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$6),
		  COALESCE((SELECT NULLIF(platform,'') FROM dashboard_account_events event
		    WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		      AND event.event_type=$4 AND event.effective_date <= $5::date AND platform<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$7),
		  COALESCE((SELECT NULLIF(channel,'') FROM dashboard_account_events event
		    WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		      AND event.event_type=$4 AND event.effective_date <= $5::date AND channel<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$8),
		  COALESCE((SELECT NULLIF(account_type,'') FROM dashboard_account_events event
		    WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		      AND event.event_type=$4 AND event.effective_date <= $5::date AND account_type<>''
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$9),
		  COALESCE((SELECT upstream_reference_url FROM dashboard_account_events event
		    WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		      AND event.event_type IN ($4,$11) AND event.effective_date <= $5::date
		      AND event.upstream_reference_url IS NOT NULL
		    ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$10)
	`, asset.UserID, asset.AdminAccountID, asset.ID, AccountEventMetadataCorrection, date,
		initialIdentifier, initialPlatform, initialChannel, initialAccountType, initialUpstreamReferenceURL,
		AccountEventLinkChange).Scan(
		&asset.Identifier, &asset.Platform, &asset.Channel, &asset.AccountType, &asset.UpstreamReferenceURL,
	)
	return asset, err
}

func projectAccountPurchaseURLAtDate(ctx context.Context, db metricsDB, asset AccountAsset, initialURL, date string) (string, error) {
	var purchaseURL string
	err := db.QueryRow(ctx, `
		SELECT COALESCE((SELECT purchase_url FROM dashboard_account_events event
		  WHERE event.user_id=$1 AND event.admin_account_id=$2 AND event.account_asset_id=$3
		    AND event.event_type=$4 AND event.effective_date <= $5::date AND event.purchase_url IS NOT NULL
		  ORDER BY event.effective_date DESC,event.created_at DESC,event.id DESC LIMIT 1),$6)
	`, asset.UserID, asset.AdminAccountID, asset.ID, AccountEventMetadataCorrection, date, initialURL).Scan(&purchaseURL)
	return purchaseURL, err
}

func (r *MetricsRepository) ReplaceAccountLink(ctx context.Context, event AccountEvent, link *AccountLink, upstreamReferenceURL string) error {
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return errors.New("account asset repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var replacementAccountingMode, purchaseDate, replacementBatchID string
	if err := tx.QueryRow(ctx, `
		SELECT asset.accounting_mode,batch.purchase_date::text,asset.batch_id
		FROM dashboard_account_assets asset
		JOIN dashboard_account_batches batch ON batch.user_id=asset.user_id
		 AND batch.admin_account_id=asset.admin_account_id AND batch.id=asset.batch_id
		WHERE asset.user_id=$1 AND asset.admin_account_id=$2 AND asset.id=$3 FOR UPDATE OF asset
	`, event.UserID, event.AdminAccountID, event.AccountAssetID).Scan(&replacementAccountingMode, &purchaseDate, &replacementBatchID); errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountAssetNotFound
	} else if err != nil {
		return err
	}
	if _, found, err := loadAccountEventByIdempotency(ctx, tx, event.UserID, event.AdminAccountID, event.AccountAssetID, event.IdempotencyKey); err != nil {
		return err
	} else if found {
		return tx.Commit(ctx)
	}
	if event.EventType != AccountEventLinkChange || event.ID == "" || strings.TrimSpace(event.IdempotencyKey) == "" {
		return errInvalidAccountBatch
	}
	effectiveAt, err := time.ParseInLocation("2006-01-02", event.EffectiveDate, businesstime.Location())
	if err != nil {
		return errInvalidAccountBatch
	}
	if event.EffectiveDate < purchaseDate {
		return errInvalidAccountBatch
	}
	projectedStatus, _, err := accountAssetStateAtDate(ctx, tx, AccountAsset{
		ID: event.AccountAssetID, UserID: event.UserID, AdminAccountID: event.AdminAccountID, BatchID: replacementBatchID,
	}, event.EffectiveDate)
	if err != nil {
		return err
	}
	if isTerminalAccountStatus(projectedStatus) {
		return errInvalidAccountBatch
	}

	if link != nil {
		var currentFrom string
		err := tx.QueryRow(ctx, `
			SELECT effective_from::text FROM dashboard_account_links
			WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND effective_to IS NULL
			FOR UPDATE
		`, event.UserID, event.AdminAccountID, event.AccountAssetID).Scan(&currentFrom)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var ownerLinkID, ownerAssetID, ownerFrom string
		ownerErr := tx.QueryRow(ctx, `
			SELECT id,account_asset_id,effective_from::text FROM dashboard_account_links
			WHERE user_id=$1 AND admin_account_id=$2 AND connection_id=$3
			  AND account_asset_id<>$4 AND effective_to IS NULL
			FOR UPDATE
		`, event.UserID, event.AdminAccountID, link.ConnectionID, event.AccountAssetID).Scan(
			&ownerLinkID, &ownerAssetID, &ownerFrom,
		)
		if ownerErr != nil && !errors.Is(ownerErr, pgx.ErrNoRows) {
			return ownerErr
		}
		if link.ManualSameDaySplit {
			if errors.Is(ownerErr, pgx.ErrNoRows) || link.PreviousQuotaUsedMicros == nil || link.PreviousRevenueCents == nil ||
				link.ReplacementQuotaUsedMicros == nil || link.ReplacementRevenueCents == nil {
				return errInvalidAccountBatch
			}
			var source, quality, keyCostRunID, previousAccountingMode string
			var confirmedQuota, confirmedRevenue, confirmedUpstreamCost *int64
			if err := tx.QueryRow(ctx, `
				SELECT stat.source,stat.quality,stat.key_cost_run_id,stat.raw_quota_used_micros,
				       stat.revenue_cents,stat.upstream_cost_cents,asset.accounting_mode
				FROM dashboard_account_daily_stats stat
				JOIN dashboard_account_assets asset ON asset.id=stat.account_asset_id
				  AND asset.user_id=stat.user_id AND asset.admin_account_id=stat.admin_account_id
				WHERE stat.user_id=$1 AND stat.admin_account_id=$2 AND stat.account_asset_id=$3 AND stat.business_date=$4::date
				FOR UPDATE
			`, event.UserID, event.AdminAccountID, ownerAssetID, event.EffectiveDate).Scan(
				&source, &quality, &keyCostRunID, &confirmedQuota, &confirmedRevenue, &confirmedUpstreamCost, &previousAccountingMode,
			); err != nil {
				return errInvalidAccountBatch
			}
			if source != StatsModeAutomatic || quality != KeyCostQualityComplete || confirmedQuota == nil || confirmedRevenue == nil || confirmedUpstreamCost == nil ||
				*link.PreviousQuotaUsedMicros < 0 || *link.PreviousRevenueCents < 0 ||
				*link.ReplacementQuotaUsedMicros < 0 || *link.ReplacementRevenueCents < 0 ||
				*link.PreviousQuotaUsedMicros+*link.ReplacementQuotaUsedMicros > *confirmedQuota ||
				*link.PreviousRevenueCents+*link.ReplacementRevenueCents > *confirmedRevenue {
				return errInvalidAccountBatch
			}
			previousUpstreamCost, err := prorateCents(*confirmedUpstreamCost, *link.PreviousQuotaUsedMicros, *confirmedQuota)
			if err != nil {
				return errInvalidAccountBatch
			}
			replacementUpstreamCost, err := prorateCents(*confirmedUpstreamCost, *link.ReplacementQuotaUsedMicros, *confirmedQuota)
			if err != nil {
				return errInvalidAccountBatch
			}
			if *link.PreviousQuotaUsedMicros+*link.ReplacementQuotaUsedMicros == *confirmedQuota {
				replacementUpstreamCost = *confirmedUpstreamCost - previousUpstreamCost
			}
			var previousDeduction, replacementDeduction *int64
			if previousAccountingMode == AccountingModeReplace {
				previousDeductionValue := previousUpstreamCost
				previousDeduction = &previousDeductionValue
			}
			if replacementAccountingMode == AccountingModeReplace {
				replacementDeductionValue := replacementUpstreamCost
				replacementDeduction = &replacementDeductionValue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE dashboard_account_daily_stats SET source=$5,quality=$6,raw_quota_used_micros=$7,
				  revenue_cents=$8,upstream_cost_cents=$9,replacement_deduction_cents=$10,updated_at=$11
				WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date=$4::date
			`, event.UserID, event.AdminAccountID, ownerAssetID, event.EffectiveDate, StatsModeManual,
				KeyCostQualityComplete, link.PreviousQuotaUsedMicros, link.PreviousRevenueCents,
				previousUpstreamCost, previousDeduction, event.CreatedAt); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO dashboard_account_daily_stats (
					id,user_id,admin_account_id,account_asset_id,business_date,source,quality,key_cost_run_id,
					raw_quota_used_micros,revenue_cents,upstream_cost_cents,replacement_deduction_cents,
					observed_at,created_at,updated_at
				) VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12,$13,$13,$13)
			`, mustMetricsID(), event.UserID, event.AdminAccountID, event.AccountAssetID, event.EffectiveDate,
				StatsModeManual, KeyCostQualityComplete, keyCostRunID, link.ReplacementQuotaUsedMicros,
				link.ReplacementRevenueCents, replacementUpstreamCost, replacementDeduction, event.CreatedAt); err != nil {
				return errInvalidAccountBatch
			}
			for _, baseline := range []struct {
				assetID      string
				quota        int64
				revenue      int64
				upstreamCost int64
			}{
				{ownerAssetID, *link.PreviousQuotaUsedMicros, *link.PreviousRevenueCents, previousUpstreamCost},
				{event.AccountAssetID, *link.ReplacementQuotaUsedMicros, *link.ReplacementRevenueCents, replacementUpstreamCost},
			} {
				var priorQuota, priorRevenue, priorUpstream int64
				if err := tx.QueryRow(ctx, `
					SELECT COALESCE(sum(raw_quota_used_micros),0),COALESCE(sum(revenue_cents),0),
					       COALESCE(sum(upstream_cost_cents),0)
					FROM dashboard_account_daily_stats
					WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND business_date < $4::date
				`, event.UserID, event.AdminAccountID, baseline.assetID, event.EffectiveDate).Scan(
					&priorQuota, &priorRevenue, &priorUpstream,
				); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO dashboard_account_events (
						id,user_id,admin_account_id,account_asset_id,event_type,effective_date,
						quota_used_micros,revenue_cents,upstream_cost_cents,note,idempotency_key,created_at
					) VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10,$11,$12)
				`, mustMetricsID(), event.UserID, event.AdminAccountID, baseline.assetID, AccountEventManualObservation,
					event.EffectiveDate, priorQuota+baseline.quota, priorRevenue+baseline.revenue,
					priorUpstream+baseline.upstreamCost, "same_day_split_baseline",
					event.IdempotencyKey+":split-baseline", event.CreatedAt); err != nil {
					return err
				}
			}
		}
		if err == nil {
			closeDate := effectiveAt.AddDate(0, 0, -1).Format("2006-01-02")
			if link.ManualSameDaySplit {
				closeDate = event.EffectiveDate
			}
			if closeDate < currentFrom {
				return errInvalidAccountBatch
			}
			if _, err := tx.Exec(ctx, `
				UPDATE dashboard_account_links SET effective_to=$4::date
				WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND effective_to IS NULL
			`, event.UserID, event.AdminAccountID, event.AccountAssetID, closeDate); err != nil {
				return err
			}
		}
		if ownerErr == nil {
			closeDate := effectiveAt.AddDate(0, 0, -1).Format("2006-01-02")
			if link.ManualSameDaySplit {
				closeDate = event.EffectiveDate
			}
			if closeDate < ownerFrom {
				return errInvalidAccountBatch
			}
			if _, err := tx.Exec(ctx, `
				UPDATE dashboard_account_links SET effective_to=$4::date,manual_same_day_split=$5
				WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
			`, event.UserID, event.AdminAccountID, ownerLinkID, closeDate, link.ManualSameDaySplit); err != nil {
				return err
			}
		}
		if err := r.insertAccountLink(ctx, tx, *link); err != nil {
			return err
		}
		if link.ManualSameDaySplit {
			for _, assetID := range []string{ownerAssetID, event.AccountAssetID} {
				var asset AccountAsset
				asset.ID, asset.UserID, asset.AdminAccountID = assetID, event.UserID, event.AdminAccountID
				if err := tx.QueryRow(ctx, `
					SELECT batch_id,identifier,purchase_cost_cents,quota_total_micros,recognition_mode
					FROM dashboard_account_assets
					WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
				`, event.UserID, event.AdminAccountID, assetID).Scan(
					&asset.BatchID, &asset.Identifier, &asset.PurchaseCostCents, &asset.QuotaTotalMicros, &asset.RecognitionMode,
				); err != nil {
					return err
				}
				if asset.RecognitionMode == RecognitionModeQuota {
					if err := r.rebuildQuotaRecognitionCosts(ctx, tx, asset, event.CreatedAt); err != nil {
						return err
					}
				}
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE dashboard_account_assets SET upstream_reference_url=$4,updated_at=$5
		WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, event.UserID, event.AdminAccountID, event.AccountAssetID, upstreamReferenceURL, event.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dashboard_account_events (
			id,user_id,admin_account_id,account_asset_id,event_type,effective_date,
			upstream_reference_url,note,idempotency_key,created_at
		) VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8,$9,$10)
	`, event.ID, event.UserID, event.AdminAccountID, event.AccountAssetID, event.EventType,
		event.EffectiveDate, upstreamReferenceURL, event.Note, event.IdempotencyKey, event.CreatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func prorateCents(total, part, whole int64) (int64, error) {
	if total < 0 || part < 0 || whole <= 0 || part > whole {
		return 0, errInvalidAccountBatch
	}
	value := new(big.Int).Mul(big.NewInt(total), big.NewInt(part))
	value.Quo(value, big.NewInt(whole))
	if !value.IsInt64() {
		return 0, errInvalidAccountBatch
	}
	return value.Int64(), nil
}

func listAccountPurchaseAmounts(ctx context.Context, db metricsDB, userID, adminAccountID, assetID string) ([]DatedAmount, error) {
	rows, err := db.Query(ctx, `
		SELECT business_date::text,amount_cents FROM dashboard_additional_costs
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND type=$4
		ORDER BY business_date,created_at,id
	`, userID, adminAccountID, assetID, AdditionalCostAccountPurchase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DatedAmount, 0)
	for rows.Next() {
		var item DatedAmount
		if err := rows.Scan(&item.BusinessDate, &item.AmountCents); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func accountEventCostRecord(event AccountEvent, asset AccountAsset, costType, date string, amount int64, note string) AdditionalCostRecord {
	return AdditionalCostRecord{
		ID: mustMetricsID(), UserID: event.UserID, AdminAccountID: event.AdminAccountID,
		Type: costType, Name: asset.Identifier, BusinessDate: date, AmountCents: amount,
		Amount: float64(amount) / 100, OriginalAmount: float64(absInt64(amount)) / 100,
		SourceID: asset.ID, BatchID: asset.BatchID, AccountAssetID: asset.ID,
		Note: note, CreatedAt: event.CreatedAt,
	}
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func loadAccountEventByIdempotency(ctx context.Context, db metricsDB, userID, adminAccountID, assetID, key string) (AccountEvent, bool, error) {
	var event AccountEvent
	err := db.QueryRow(ctx, `
		SELECT id,user_id,admin_account_id,account_asset_id,event_type,effective_date::text,status,
		       quota_used_micros,revenue_cents,refund_cents,upstream_cost_cents,stats_mode,
		       identifier,platform,channel,account_type,purchase_url,upstream_reference_url,note,idempotency_key,created_at
		FROM dashboard_account_events
		WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3 AND idempotency_key=$4
	`, userID, adminAccountID, assetID, key).Scan(&event.ID, &event.UserID, &event.AdminAccountID,
		&event.AccountAssetID, &event.EventType, &event.EffectiveDate, &event.Status, &event.QuotaUsedMicros,
		&event.RevenueCents, &event.RefundCents, &event.UpstreamCostCents, &event.StatsMode,
		&event.Identifier, &event.Platform, &event.Channel, &event.AccountType, &event.PurchaseURL, &event.UpstreamReferenceURL, &event.Note,
		&event.IdempotencyKey, &event.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountEvent{}, false, nil
	}
	return event, err == nil, err
}

func (r *MetricsRepository) GetAccountAssetDetail(ctx context.Context, userID, adminAccountID, assetID string) (AccountAssetDetail, error) {
	asset, err := r.GetAccountAsset(ctx, userID, adminAccountID, assetID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	batch, err := scanAccountBatch(r.db.QueryRow(ctx, `
		SELECT id,user_id,admin_account_id,idempotency_key,batch_name,platform,channel,account_type,
		       purchase_date::text,purchase_url,default_upstream_reference_url,quantity,total_amount_cents,
		       accounting_mode,recognition_mode,recognition_start_date::text,recognition_days,stats_mode,note,created_at
		FROM dashboard_account_batches WHERE user_id=$1 AND admin_account_id=$2 AND id=$3
	`, userID, adminAccountID, asset.BatchID))
	if err != nil {
		return AccountAssetDetail{}, err
	}
	today := businesstime.Today()
	batch.PurchaseURL, err = projectAccountPurchaseURLAtDate(ctx, r.db, asset, batch.PurchaseURL, today)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	links, err := r.listAccountLinks(ctx, userID, adminAccountID, assetID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	for _, link := range links {
		if link.EffectiveFrom <= today && (link.EffectiveTo == nil || *link.EffectiveTo >= today) {
			asset.HasActiveLink = true
			break
		}
	}
	events, err := r.listAccountEvents(ctx, userID, adminAccountID, assetID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	stats, err := r.listAccountDailyStats(ctx, userID, adminAccountID, assetID)
	if err != nil {
		return AccountAssetDetail{}, err
	}
	var revenueCents, rawQuotaMicros, upstreamCostCents, refundCents int64
	var hasRevenue, hasRawQuota, hasUpstreamCost bool
	hasIncompleteDailyStats := false
	for _, stat := range stats {
		if stat.Quality != KeyCostQualityComplete {
			hasIncompleteDailyStats = true
		}
		if stat.RevenueCents != nil {
			revenueCents += *stat.RevenueCents
			hasRevenue = true
		}
		if stat.RawQuotaUsedMicros != nil {
			rawQuotaMicros += *stat.RawQuotaUsedMicros
			hasRawQuota = true
		}
		if stat.UpstreamCostCents != nil {
			upstreamCostCents += *stat.UpstreamCostCents
			hasUpstreamCost = true
		}
	}
	for _, event := range events {
		if event.EventType == AccountEventRefund && event.RefundCents != nil {
			refundCents += *event.RefundCents
		}
	}
	return AccountAssetDetail{
		Asset: asset, Batch: batch, Links: links, Events: events, DailyStats: stats,
		Performance: calculateAccountPerformance(AccountPerformanceInput{
			Status: asset.CurrentStatus, AccountingMode: asset.AccountingMode, PurchaseCostCents: asset.PurchaseCostCents,
			AdditiveUpstreamCostCents: upstreamCostCents, RevenueCents: revenueCents,
			RefundCents: refundCents, RawQuotaUsedMicros: rawQuotaMicros,
			HasAdditiveUpstreamCost: hasUpstreamCost, HasRevenue: hasRevenue, HasRawQuotaUsed: hasRawQuota,
			HasIncompleteDailyStats: hasIncompleteDailyStats,
		}),
	}, nil
}

func scanAccountBatch(scanner accountAssetScanner) (AccountBatch, error) {
	var batch AccountBatch
	err := scanner.Scan(&batch.ID, &batch.UserID, &batch.AdminAccountID, &batch.IdempotencyKey, &batch.BatchName,
		&batch.Platform, &batch.Channel, &batch.AccountType, &batch.PurchaseDate, &batch.PurchaseURL,
		&batch.DefaultUpstreamReferenceURL, &batch.Quantity, &batch.TotalAmountCents, &batch.AccountingMode,
		&batch.RecognitionMode, &batch.RecognitionStartDate, &batch.RecognitionDays, &batch.StatsMode,
		&batch.Note, &batch.CreatedAt)
	return batch, err
}

func (r *MetricsRepository) listAccountLinks(ctx context.Context, userID, adminAccountID, assetID string) ([]AccountLink, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id,user_id,admin_account_id,account_asset_id,connection_id,upstream_site_id,upstream_key_id,
		       scope_admin_account_id,own_group_id,connection_name,site_name,key_name,own_group_name,
		       upstream_reference_url,effective_from::text,effective_to::text,manual_same_day_split,created_at
		FROM dashboard_account_links WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		ORDER BY effective_from,created_at,id
	`, userID, adminAccountID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountLink, 0)
	for rows.Next() {
		var item AccountLink
		if err := rows.Scan(&item.ID, &item.UserID, &item.AdminAccountID, &item.AccountAssetID, &item.ConnectionID,
			&item.UpstreamSiteID, &item.UpstreamKeyID, &item.ScopeAdminAccountID, &item.OwnGroupID,
			&item.ConnectionName, &item.SiteName, &item.KeyName, &item.OwnGroupName,
			&item.UpstreamReferenceURL, &item.EffectiveFrom, &item.EffectiveTo, &item.ManualSameDaySplit, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *MetricsRepository) listAccountEvents(ctx context.Context, userID, adminAccountID, assetID string) ([]AccountEvent, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id,user_id,admin_account_id,account_asset_id,event_type,effective_date::text,status,
		       quota_used_micros,revenue_cents,refund_cents,upstream_cost_cents,stats_mode,
		       identifier,platform,channel,account_type,purchase_url,upstream_reference_url,note,idempotency_key,created_at
		FROM dashboard_account_events WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		ORDER BY effective_date,created_at,id
	`, userID, adminAccountID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountEvent, 0)
	for rows.Next() {
		var event AccountEvent
		if err := rows.Scan(&event.ID, &event.UserID, &event.AdminAccountID, &event.AccountAssetID,
			&event.EventType, &event.EffectiveDate, &event.Status, &event.QuotaUsedMicros, &event.RevenueCents,
			&event.RefundCents, &event.UpstreamCostCents, &event.StatsMode,
			&event.Identifier, &event.Platform, &event.Channel, &event.AccountType, &event.PurchaseURL, &event.UpstreamReferenceURL,
			&event.Note, &event.IdempotencyKey, &event.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}

func (r *MetricsRepository) listAccountDailyStats(ctx context.Context, userID, adminAccountID, assetID string) ([]AccountDailyStat, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id,business_date::text,source,quality,key_cost_run_id,raw_quota_used_micros,revenue_cents,
		       upstream_cost_cents,recognized_cost_cents,replacement_deduction_cents,observed_at,created_at,updated_at
		FROM dashboard_account_daily_stats WHERE user_id=$1 AND admin_account_id=$2 AND account_asset_id=$3
		ORDER BY business_date,created_at,id
	`, userID, adminAccountID, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccountDailyStat, 0)
	for rows.Next() {
		var item AccountDailyStat
		if err := rows.Scan(&item.ID, &item.BusinessDate, &item.Source, &item.Quality, &item.KeyCostRunID,
			&item.RawQuotaUsedMicros, &item.RevenueCents, &item.UpstreamCostCents, &item.RecognizedCostCents,
			&item.ReplacementDeductionCents, &item.ObservedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

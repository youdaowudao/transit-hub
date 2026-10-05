package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

const (
	KeyCostQualityComplete = "complete"
	KeyCostQualityMismatch = "mismatch"
	KeyCostQualityMissing  = "missing"
)

type UpstreamKeyDailyCost struct {
	RunID             string
	UserID            string
	AdminAccountID    string
	BusinessDate      string
	SiteID            string
	KeyID             string
	KeyName           string
	RawAmountMicros   int64
	AdjustedCostCents int64
	Status            string
	ObservedAt        time.Time
}

type UpstreamKeyCostRun struct {
	ID                string
	SnapshotRunID     string
	UserID            string
	AdminAccountID    string
	BusinessDate      string
	SiteID            string
	ExpectedKeyCount  int
	CollectedKeyCount int
	SiteTotalCents    int64
	KeyTotalCents     int64
	Complete          bool
	Quality           string
	ObservedAt        time.Time
	Items             []UpstreamKeyDailyCost
}

func completeAccountKeySnapshotRunID(runs []UpstreamKeyCostRun) string {
	if len(runs) == 0 || runs[0].SnapshotRunID == "" {
		return ""
	}
	runID := runs[0].SnapshotRunID
	for _, run := range runs {
		if !run.Complete || run.SnapshotRunID != runID {
			return ""
		}
	}
	return runID
}

func buildAccountKeyCostRuns(userID, adminAccountID, runPrefix, date string, siteTotals []upstream.SiteCostForDateResult, keys upstream.KeyUsageForDateResult, observedAt time.Time) []UpstreamKeyCostRun {
	keySites := make(map[string]upstream.KeyUsageSiteResult, len(keys.Sites))
	for _, site := range keys.Sites {
		keySites[site.SiteID] = site
	}
	runs := make([]UpstreamKeyCostRun, 0, len(siteTotals))
	for index, siteTotal := range siteTotals {
		run := UpstreamKeyCostRun{
			ID: fmt.Sprintf("%s-%03d", runPrefix, index+1), SnapshotRunID: runPrefix, UserID: userID, AdminAccountID: adminAccountID,
			BusinessDate: date, SiteID: siteTotal.SiteID, Quality: KeyCostQualityMissing, ObservedAt: observedAt,
		}
		if !siteTotal.Meta.ObservedAt.IsZero() {
			run.ObservedAt = siteTotal.Meta.ObservedAt
		}
		if siteTotal.Err == nil {
			run.SiteTotalCents = cents(siteTotal.RawCost * siteTotal.RechargeRate)
		}
		keySite, exists := keySites[siteTotal.SiteID]
		if siteTotal.Err != nil || !exists || !keySite.Complete {
			runs = append(runs, run)
			continue
		}
		run.ExpectedKeyCount = len(keySite.Items)
		run.CollectedKeyCount = len(keySite.Items)
		run.Items = make([]UpstreamKeyDailyCost, 0, len(keySite.Items))
		nonzeroKeys := 0
		for _, item := range keySite.Items {
			status := "ok"
			if item.Merged {
				status = "merged"
			}
			adjustedCents := cents(item.TodayAmount)
			if item.RawAmount != 0 || item.TodayAmount != 0 {
				nonzeroKeys++
			}
			run.KeyTotalCents += adjustedCents
			run.Items = append(run.Items, UpstreamKeyDailyCost{
				RunID: run.ID, UserID: userID, AdminAccountID: adminAccountID, BusinessDate: date,
				SiteID: item.SiteID, KeyID: item.KeyID, KeyName: item.KeyName,
				RawAmountMicros: int64(item.RawAmount * 1_000_000), AdjustedCostCents: adjustedCents,
				Status: status, ObservedAt: run.ObservedAt,
			})
		}
		tolerance := int64((nonzeroKeys + 1) / 2)
		lower, upper := run.SiteTotalCents-tolerance, run.SiteTotalCents+tolerance
		dateOK := keySite.ConsumeDate == "" || keySite.ConsumeDate == date
		if keySite.CollectedAt != nil && businesstime.DateAt(*keySite.CollectedAt) != date {
			dateOK = false
		}
		if keySite.SyncedRawCost != nil && keySite.CollectedRawCost != nil {
			lower = cents(*keySite.SyncedRawCost*siteTotal.RechargeRate) - tolerance
			upper = cents(*keySite.CollectedRawCost*siteTotal.RechargeRate) + tolerance
		}
		if dateOK && run.KeyTotalCents >= lower && run.KeyTotalCents <= upper {
			run.Complete = true
			run.Quality = KeyCostQualityComplete
		} else {
			run.Quality = KeyCostQualityMismatch
		}
		runs = append(runs, run)
	}
	return runs
}

type AccountCostComponents struct {
	AccountPurchaseCostCents          int64
	ReplacementDeductionCents         *int64
	RequiresReplacementDeduction      bool
	ReconciledUpstreamDirectCostCents *int64
	ReconciledSiteCount               int
	LiveKeySnapshot                   bool
	SnapshotRunID                     string
}

func (r *MetricsRepository) SaveUpstreamKeyCostRuns(ctx context.Context, runs []UpstreamKeyCostRun) error {
	if len(runs) == 0 {
		return nil
	}
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return errors.New("key cost repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.saveUpstreamKeyCostRuns(ctx, tx, runs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *MetricsRepository) PublishAccountStatsRefresh(ctx context.Context, runs []UpstreamKeyCostRun, stats []AccountDailyStat) error {
	if len(runs) == 0 && len(stats) == 0 {
		return nil
	}
	starter, ok := r.db.(metricsTxStarter)
	if !ok {
		return errors.New("account stats refresh repository does not support transactions")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.saveUpstreamKeyCostRuns(ctx, tx, runs); err != nil {
		return err
	}
	if err := r.saveAutomaticAccountDailyStats(ctx, tx, stats); err != nil {
		return err
	}
	if err := r.bindAccountStatsRefreshSnapshot(ctx, tx, runs, stats); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *MetricsRepository) bindAccountStatsRefreshSnapshot(ctx context.Context, tx pgx.Tx, runs []UpstreamKeyCostRun, stats []AccountDailyStat) error {
	if len(runs) == 0 {
		return nil
	}
	first := runs[0]
	for _, run := range runs {
		if run.UserID != first.UserID || run.AdminAccountID != first.AdminAccountID || run.BusinessDate != first.BusinessDate || run.SnapshotRunID != first.SnapshotRunID || !run.Complete {
			return errors.New("account stats refresh contains inconsistent key runs")
		}
	}
	var accountPurchaseCents int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date AND type=$4`, first.UserID, first.AdminAccountID, first.BusinessDate, AdditionalCostAccountPurchase).Scan(&accountPurchaseCents); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE dashboard_daily_stats SET account_snapshot_run_id=$4,account_expected_count=$5,account_completed_count=$5,account_stats_quality=$6,account_purchase_cost=$7 WHERE user_id=$1 AND admin_account_id=$2 AND date=$3::date`, first.UserID, first.AdminAccountID, first.BusinessDate, first.SnapshotRunID, len(stats), KeyCostQualityComplete, float64(accountPurchaseCents)/100)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("dashboard snapshot is required for account stats refresh")
	}
	return nil
}

func (r *MetricsRepository) GetPublishedAccountStatsRefresh(ctx context.Context, userID, adminAccountID, runID, date string) (AccountStatsRefreshResponse, bool, error) {
	response := AccountStatsRefreshResponse{Date: date, SnapshotRunID: runID}
	var expectedAccounts, completedAccounts sql.NullInt64
	var accountQuality string
	if err := r.db.QueryRow(ctx, `
		SELECT account_expected_count,account_completed_count,COALESCE(account_stats_quality,'missing')
		FROM dashboard_daily_stats
		WHERE user_id=$1 AND admin_account_id=$2 AND date=$3::date AND account_snapshot_run_id=$4
	`, userID, adminAccountID, date, runID).Scan(&expectedAccounts, &completedAccounts, &accountQuality); errors.Is(err, pgx.ErrNoRows) {
		return AccountStatsRefreshResponse{}, false, nil
	} else if err != nil {
		return AccountStatsRefreshResponse{}, false, err
	}
	if !expectedAccounts.Valid || !completedAccounts.Valid || accountQuality != KeyCostQualityComplete ||
		expectedAccounts.Int64 != completedAccounts.Int64 {
		return AccountStatsRefreshResponse{}, false, nil
	}
	response.ExpectedAccounts = int(expectedAccounts.Int64)
	response.CompletedAccounts = int(completedAccounts.Int64)
	var allComplete bool
	if err := r.db.QueryRow(ctx, `
		SELECT count(*)::int,count(*) FILTER (WHERE complete)::int,COALESCE(bool_and(complete),false)
		FROM dashboard_upstream_key_cost_runs
		WHERE user_id=$1 AND admin_account_id=$2 AND snapshot_run_id=$3 AND business_date=$4::date
	`, userID, adminAccountID, runID, date).Scan(&response.ExpectedSites, &response.CompletedSites, &allComplete); err != nil {
		return AccountStatsRefreshResponse{}, false, err
	}
	if response.ExpectedSites == 0 || !allComplete || response.CompletedSites != response.ExpectedSites {
		return AccountStatsRefreshResponse{}, false, nil
	}
	var storedCompletedAccounts int
	if err := r.db.QueryRow(ctx, `
		SELECT count(*)::int FROM dashboard_account_daily_stats
		WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date AND key_cost_run_id=$4 AND quality=$5
	`, userID, adminAccountID, date, runID, KeyCostQualityComplete).Scan(&storedCompletedAccounts); err != nil {
		return AccountStatsRefreshResponse{}, false, err
	}
	if storedCompletedAccounts != response.CompletedAccounts {
		return AccountStatsRefreshResponse{}, false, nil
	}
	response.Quality = KeyCostQualityComplete
	return response, true, nil
}

func (r *MetricsRepository) LatestCompleteAccountKeyCostRuns(ctx context.Context, userID, adminAccountID, date string) (string, []UpstreamKeyCostRun, error) {
	var snapshotRunID string
	err := r.db.QueryRow(ctx, `
		SELECT snapshot_run_id FROM dashboard_upstream_key_cost_runs
		WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date
		GROUP BY snapshot_run_id HAVING bool_and(complete)
		ORDER BY max(observed_at) DESC,max(created_at) DESC,snapshot_run_id DESC LIMIT 1
	`, userID, adminAccountID, date).Scan(&snapshotRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	runs, err := r.AccountKeyCostRunsForSnapshot(ctx, userID, adminAccountID, date, snapshotRunID)
	return snapshotRunID, runs, err
}

func (r *MetricsRepository) AccountKeyCostRunsForSnapshot(ctx context.Context, userID, adminAccountID, date, snapshotRunID string) ([]UpstreamKeyCostRun, error) {
	if snapshotRunID == "" {
		return nil, nil
	}
	rows, err := r.db.Query(ctx, `
		SELECT id,snapshot_run_id,user_id,admin_account_id,business_date::text,site_id,
		       expected_key_count,collected_key_count,site_total_cents,key_total_cents,complete,quality,observed_at
		FROM dashboard_upstream_key_cost_runs
		WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date AND snapshot_run_id=$4
		ORDER BY site_id,id
	`, userID, adminAccountID, date, snapshotRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]UpstreamKeyCostRun, 0)
	for rows.Next() {
		var run UpstreamKeyCostRun
		if err := rows.Scan(&run.ID, &run.SnapshotRunID, &run.UserID, &run.AdminAccountID, &run.BusinessDate,
			&run.SiteID, &run.ExpectedKeyCount, &run.CollectedKeyCount, &run.SiteTotalCents, &run.KeyTotalCents,
			&run.Complete, &run.Quality, &run.ObservedAt); err != nil {
			return nil, err
		}
		itemRows, err := r.db.Query(ctx, `
			SELECT run_id,user_id,admin_account_id,business_date::text,site_id,key_id,key_name,
			       raw_amount_micros,adjusted_cost_cents,status,observed_at
			FROM dashboard_upstream_key_daily_costs
			WHERE run_id=$1 AND user_id=$2 AND admin_account_id=$3 ORDER BY key_id
		`, run.ID, userID, adminAccountID)
		if err != nil {
			return nil, err
		}
		for itemRows.Next() {
			var item UpstreamKeyDailyCost
			if err := itemRows.Scan(&item.RunID, &item.UserID, &item.AdminAccountID, &item.BusinessDate, &item.SiteID,
				&item.KeyID, &item.KeyName, &item.RawAmountMicros, &item.AdjustedCostCents, &item.Status, &item.ObservedAt); err != nil {
				itemRows.Close()
				return nil, err
			}
			run.Items = append(run.Items, item)
		}
		if err := itemRows.Err(); err != nil {
			itemRows.Close()
			return nil, err
		}
		itemRows.Close()
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	for _, run := range runs {
		if !run.Complete || run.Quality != KeyCostQualityComplete {
			return nil, nil
		}
	}
	return runs, nil
}

func (r *MetricsRepository) saveUpstreamKeyCostRuns(ctx context.Context, db metricsDB, runs []UpstreamKeyCostRun) error {
	for _, run := range runs {
		if _, err := db.Exec(ctx, `
			INSERT INTO dashboard_upstream_key_cost_runs (
				id,snapshot_run_id,user_id,admin_account_id,business_date,site_id,expected_key_count,
				collected_key_count,site_total_cents,key_total_cents,complete,quality,observed_at
			) VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (id) DO NOTHING
		`, run.ID, run.SnapshotRunID, run.UserID, run.AdminAccountID, run.BusinessDate, run.SiteID,
			run.ExpectedKeyCount, run.CollectedKeyCount, run.SiteTotalCents, run.KeyTotalCents,
			run.Complete, run.Quality, run.ObservedAt); err != nil {
			return err
		}
		for _, item := range run.Items {
			if item.UserID != run.UserID || item.AdminAccountID != run.AdminAccountID || item.RunID != run.ID || item.SiteID != run.SiteID {
				return errors.New("key cost item workspace mismatch")
			}
			if _, err := db.Exec(ctx, `
				INSERT INTO dashboard_upstream_key_daily_costs (
					run_id,user_id,admin_account_id,business_date,site_id,key_id,key_name,
					raw_amount_micros,adjusted_cost_cents,status,observed_at
				) VALUES ($1,$2,$3,$4::date,$5,$6,$7,$8,$9,$10,$11)
				ON CONFLICT (run_id,key_id) DO NOTHING
			`, item.RunID, item.UserID, item.AdminAccountID, item.BusinessDate, item.SiteID,
				item.KeyID, item.KeyName, item.RawAmountMicros, item.AdjustedCostCents, item.Status, item.ObservedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *MetricsRepository) AccountCostComponentsForDate(ctx context.Context, userID, adminAccountID, date string) (AccountCostComponents, error) {
	return r.accountCostComponentsForDate(ctx, r.db, userID, adminAccountID, date, "", true)
}

func (r *MetricsRepository) AccountCostComponentsForSnapshotRun(ctx context.Context, userID, adminAccountID, date, snapshotRunID string) (AccountCostComponents, error) {
	return r.accountCostComponentsForDate(ctx, r.db, userID, adminAccountID, date, snapshotRunID, false)
}

func (r *MetricsRepository) AccountCostComponentsFromKeySnapshot(ctx context.Context, userID, adminAccountID, date string, keys upstream.KeyUsageForDateResult) (AccountCostComponents, error) {
	components := AccountCostComponents{LiveKeySnapshot: true}
	if err := r.db.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date AND type=$4`, userID, adminAccountID, date, AdditionalCostAccountPurchase).Scan(&components.AccountPurchaseCostCents); err != nil {
		return components, err
	}
	var replacementLinks int
	if err := r.db.QueryRow(ctx, `SELECT count(DISTINCT(link.upstream_site_id,link.upstream_key_id)) FROM dashboard_account_links link JOIN dashboard_account_assets asset ON asset.id=link.account_asset_id AND asset.user_id=link.user_id AND asset.admin_account_id=link.admin_account_id WHERE link.user_id=$1 AND link.admin_account_id=$2 AND asset.accounting_mode=$4 AND link.effective_from<=$3::date AND (link.effective_to IS NULL OR link.effective_to>=$3::date)`, userID, adminAccountID, date, AccountingModeReplace).Scan(&replacementLinks); err != nil {
		return components, err
	}
	components.RequiresReplacementDeduction = replacementLinks > 0
	if !components.RequiresReplacementDeduction || keys.BusinessDate != date {
		return components, nil
	}
	costByKey := make(map[string]int64)
	for _, site := range keys.Sites {
		if !site.Complete || site.Status == "missing" {
			continue
		}
		for _, item := range site.Items {
			if item.Merged {
				continue
			}
			costByKey[directProfitKey(site.SiteID, item.KeyID)] = cents(item.TodayAmount)
		}
	}
	rows, err := r.db.Query(ctx, `
 SELECT DISTINCT link.upstream_site_id,link.upstream_key_id
 FROM dashboard_account_links link
 JOIN dashboard_account_assets asset ON asset.id=link.account_asset_id AND asset.user_id=link.user_id AND asset.admin_account_id=link.admin_account_id
 JOIN real_connections live ON live.id=link.connection_id AND live.user_id=link.user_id AND live.workspace_admin_account_id=link.admin_account_id
  AND live.upstream_site_id=link.upstream_site_id AND live.upstream_key_id=link.upstream_key_id AND live.admin_account_id=link.scope_admin_account_id
  AND live.status='active' AND jsonb_array_length(live.own_group_ids)=1 AND live.own_group_ids->>0=link.own_group_id
 WHERE link.user_id=$1 AND link.admin_account_id=$2 AND asset.accounting_mode=$4
  AND link.effective_from<=$3::date AND (link.effective_to IS NULL OR link.effective_to>=$3::date)
  AND NOT EXISTS(SELECT 1 FROM real_connections other
   WHERE other.user_id=live.user_id AND other.workspace_admin_account_id=live.workspace_admin_account_id AND other.id<>live.id AND other.status='active' AND jsonb_array_length(other.own_group_ids)=1
    AND ((other.upstream_site_id=live.upstream_site_id AND other.upstream_key_id=live.upstream_key_id) OR (other.admin_account_id=live.admin_account_id AND other.own_group_ids->>0=live.own_group_ids->>0)))`, userID, adminAccountID, date, AccountingModeReplace)
	if err != nil {
		return components, err
	}
	defer rows.Close()
	var deduction int64
	matched := 0
	available := true
	for rows.Next() {
		var siteID, keyID string
		if err := rows.Scan(&siteID, &keyID); err != nil {
			return components, err
		}
		matched++
		amount, ok := costByKey[directProfitKey(siteID, keyID)]
		if !ok {
			available = false
			continue
		}
		deduction += amount
	}
	if err := rows.Err(); err != nil {
		return components, err
	}
	if matched == replacementLinks && available {
		components.ReplacementDeductionCents = &deduction
	}
	return components, nil
}

func (r *MetricsRepository) accountCostComponentsForDate(ctx context.Context, db metricsDB, userID, adminAccountID, date, requiredSnapshotRunID string, allowLatest bool) (AccountCostComponents, error) {
	var components AccountCostComponents
	if err := db.QueryRow(ctx, `
		SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs
		WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date AND type=$4
	`, userID, adminAccountID, date, AdditionalCostAccountPurchase).Scan(&components.AccountPurchaseCostCents); err != nil {
		return AccountCostComponents{}, err
	}
	var replacementLinks int
	if err := db.QueryRow(ctx, `
		SELECT count(DISTINCT (link.upstream_site_id,link.upstream_key_id)) FROM dashboard_account_links link
		JOIN dashboard_account_assets asset
		  ON asset.id=link.account_asset_id AND asset.user_id=link.user_id AND asset.admin_account_id=link.admin_account_id
		WHERE link.user_id=$1 AND link.admin_account_id=$2 AND asset.accounting_mode=$4
		  AND link.effective_from <= $3::date AND (link.effective_to IS NULL OR link.effective_to >= $3::date)
	`, userID, adminAccountID, date, AccountingModeReplace).Scan(&replacementLinks); err != nil {
		return AccountCostComponents{}, err
	}
	components.RequiresReplacementDeduction = replacementLinks > 0
	if !components.RequiresReplacementDeduction {
		return components, nil
	}
	var reconciled int64
	err := db.QueryRow(ctx, `
		SELECT snapshot_run_id,COALESCE(sum(site_total_cents),0),count(*)::int
		FROM dashboard_upstream_key_cost_runs
		WHERE user_id=$1 AND admin_account_id=$2 AND business_date=$3::date
		  AND ($5 OR snapshot_run_id=$4)
		GROUP BY snapshot_run_id
		HAVING bool_and(complete)
		ORDER BY max(observed_at) DESC,max(created_at) DESC,snapshot_run_id DESC LIMIT 1
	`, userID, adminAccountID, date, requiredSnapshotRunID, allowLatest).Scan(&components.SnapshotRunID, &reconciled, &components.ReconciledSiteCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return components, nil
	}
	if err != nil {
		return AccountCostComponents{}, err
	}
	components.ReconciledUpstreamDirectCostCents = &reconciled
	rows, err := db.Query(ctx, `
		SELECT matched.adjusted_cost_cents FROM (
		SELECT DISTINCT ON (link.upstream_site_id,link.upstream_key_id)
		       link.upstream_site_id,link.upstream_key_id,latest.adjusted_cost_cents
		FROM dashboard_account_links link
		JOIN dashboard_account_assets asset
		  ON asset.id=link.account_asset_id AND asset.user_id=link.user_id AND asset.admin_account_id=link.admin_account_id
		JOIN real_connections live
		  ON live.id=link.connection_id AND live.user_id=link.user_id
		 AND live.workspace_admin_account_id=link.admin_account_id
		 AND live.upstream_site_id=link.upstream_site_id AND live.upstream_key_id=link.upstream_key_id
		 AND live.admin_account_id=link.scope_admin_account_id
		 AND live.status='active' AND jsonb_array_length(live.own_group_ids)=1
		 AND live.own_group_ids->>0=link.own_group_id
		LEFT JOIN LATERAL (
			SELECT key_cost.adjusted_cost_cents
			FROM dashboard_upstream_key_cost_runs run
			JOIN dashboard_upstream_key_daily_costs key_cost
			  ON key_cost.run_id=run.id AND key_cost.user_id=run.user_id AND key_cost.admin_account_id=run.admin_account_id
			WHERE run.user_id=link.user_id AND run.admin_account_id=link.admin_account_id
			  AND run.business_date=$3::date AND run.site_id=link.upstream_site_id AND run.complete=true
			  AND run.snapshot_run_id=$5
			  AND key_cost.key_id=link.upstream_key_id
			  AND key_cost.status IN ('ok','')
			ORDER BY run.observed_at DESC,run.created_at DESC,run.id DESC LIMIT 1
		) latest ON true
		WHERE link.user_id=$1 AND link.admin_account_id=$2 AND asset.accounting_mode=$4
		  AND link.effective_from <= $3::date AND (link.effective_to IS NULL OR link.effective_to >= $3::date)
		  AND NOT EXISTS (
			SELECT 1 FROM real_connections other
			WHERE other.user_id=live.user_id AND other.workspace_admin_account_id=live.workspace_admin_account_id
			  AND other.id<>live.id AND other.status='active' AND jsonb_array_length(other.own_group_ids)=1
			  AND ((other.upstream_site_id=live.upstream_site_id AND other.upstream_key_id=live.upstream_key_id)
			    OR (other.admin_account_id=live.admin_account_id AND other.own_group_ids->>0=live.own_group_ids->>0))
		  )
		ORDER BY link.upstream_site_id,link.upstream_key_id,link.id
		) matched
	`, userID, adminAccountID, date, AccountingModeReplace, components.SnapshotRunID)
	if err != nil {
		return AccountCostComponents{}, err
	}
	defer rows.Close()
	var deduction int64
	matchedLinks := 0
	available := true
	for rows.Next() {
		matchedLinks++
		var amount *int64
		if err := rows.Scan(&amount); err != nil {
			return AccountCostComponents{}, err
		}
		if amount == nil {
			available = false
			continue
		}
		deduction += *amount
	}
	if err := rows.Err(); err != nil {
		return AccountCostComponents{}, err
	}
	if matchedLinks != replacementLinks {
		available = false
	}
	if components.RequiresReplacementDeduction && available {
		components.ReplacementDeductionCents = &deduction
	}
	return components, nil
}

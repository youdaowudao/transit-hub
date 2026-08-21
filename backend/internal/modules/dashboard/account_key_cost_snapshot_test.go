package dashboard

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestCompleteAccountKeySnapshotRunIDRequiresOneCompleteRun(t *testing.T) {
	complete := []UpstreamKeyCostRun{
		{SnapshotRunID: "run-1", Complete: true},
		{SnapshotRunID: "run-1", Complete: true},
	}
	if got := completeAccountKeySnapshotRunID(complete); got != "run-1" {
		t.Fatalf("complete run ID = %q, want run-1", got)
	}
	for name, runs := range map[string][]UpstreamKeyCostRun{
		"empty":      nil,
		"mixed runs": {{SnapshotRunID: "run-1", Complete: true}, {SnapshotRunID: "run-2", Complete: true}},
		"incomplete": {{SnapshotRunID: "run-1", Complete: true}, {SnapshotRunID: "run-1", Complete: false}},
		"blank ID":   {{SnapshotRunID: "", Complete: true}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := completeAccountKeySnapshotRunID(runs); got != "" {
				t.Fatalf("run ID = %q, want blank", got)
			}
		})
	}
}

func TestAccountKeyCostRunsRequireCompleteSitesAndExactCentReconciliation(t *testing.T) {
	observedAt := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	siteTotals := []upstream.SiteCostForDateResult{
		{SiteID: "site-exact", SiteName: "exact", RechargeRate: 2, RawCost: 5, Meta: upstream.CostFetchMeta{ObservedAt: observedAt}},
		{SiteID: "site-mismatch", SiteName: "mismatch", RechargeRate: 1, RawCost: 10.01, Meta: upstream.CostFetchMeta{ObservedAt: observedAt}},
		{SiteID: "site-failed", SiteName: "failed", RechargeRate: 1, RawCost: 8, Meta: upstream.CostFetchMeta{ObservedAt: observedAt}},
	}
	keys := upstream.KeyUsageForDateResult{
		BusinessDate: "2026-08-22", ExpectedSites: 3, CompletedSites: 2,
		Sites: []upstream.KeyUsageSiteResult{
			{SiteID: "site-exact", Complete: true, Items: []upstream.KeyUsageTodayItem{
				{SiteID: "site-exact", KeyID: "key-a", KeyName: "A", RawAmount: 2, TodayAmount: 4},
				{SiteID: "site-exact", KeyID: "key-b", KeyName: "B", RawAmount: 3, TodayAmount: 6},
			}},
			{SiteID: "site-mismatch", Complete: true, Items: []upstream.KeyUsageTodayItem{
				{SiteID: "site-mismatch", KeyID: "key-c", KeyName: "C", RawAmount: 10, TodayAmount: 10},
			}},
			{SiteID: "site-failed", Complete: false, Error: upstream.ErrorRequest},
		},
	}

	runs := buildAccountKeyCostRuns("user-1", "workspace-1", "run-1", "2026-08-22", siteTotals, keys, observedAt)
	if len(runs) != 3 {
		t.Fatalf("runs = %#v", runs)
	}
	bySite := map[string]UpstreamKeyCostRun{}
	for _, run := range runs {
		bySite[run.SiteID] = run
	}
	if !bySite["site-exact"].Complete || bySite["site-exact"].SiteTotalCents != 1000 || bySite["site-exact"].KeyTotalCents != 1000 || len(bySite["site-exact"].Items) != 2 {
		t.Fatalf("exact run = %#v", bySite["site-exact"])
	}
	if bySite["site-mismatch"].Complete || bySite["site-mismatch"].Quality != KeyCostQualityMismatch || bySite["site-mismatch"].SiteTotalCents != 1001 || bySite["site-mismatch"].KeyTotalCents != 1000 {
		t.Fatalf("mismatch run = %#v", bySite["site-mismatch"])
	}
	if bySite["site-failed"].Complete || bySite["site-failed"].Quality != KeyCostQualityMissing {
		t.Fatalf("failed run = %#v", bySite["site-failed"])
	}
}

func TestAccountKeyCostRepositoryKeepsLastCompleteRunAndRequiresEveryReplacementLink(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE real_connections (
			id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,
			upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,
			own_group_ids jsonb NOT NULL,status text NOT NULL
		);
		INSERT INTO real_connections (
			id,user_id,workspace_admin_account_id,upstream_site_id,upstream_key_id,admin_account_id,own_group_ids,status
		) VALUES ('connection-1','user-1','workspace-1','site-1','key-1','admin-1','["group-1"]'::jsonb,'active');
	`); err != nil {
		t.Fatalf("create live connection: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-key", "idem-key", now)
	asset := repositoryTestAsset("asset-key", batch.ID, "linked", 1000, now)
	link := AccountLink{
		ID: "link-key", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-1", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "admin-1", OwnGroupID: "group-1", EffectiveFrom: "2026-08-22", CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, []AdditionalCostRecord{
		repositoryTestCost("cost-key", batch.ID, asset.ID, 1000, now),
	}); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	complete := UpstreamKeyCostRun{
		ID: "site-run-complete", SnapshotRunID: "snapshot-complete", UserID: "user-1", AdminAccountID: "workspace-1",
		BusinessDate: "2026-08-22", SiteID: "site-1", ExpectedKeyCount: 1, CollectedKeyCount: 1,
		SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now,
		Items: []UpstreamKeyDailyCost{{
			RunID: "site-run-complete", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: "2026-08-22",
			SiteID: "site-1", KeyID: "key-1", KeyName: "one", RawAmountMicros: 300_000_000,
			AdjustedCostCents: 300, Status: "ok", ObservedAt: now,
		}},
	}
	if err := repo.SaveUpstreamKeyCostRuns(ctx, []UpstreamKeyCostRun{complete}); err != nil {
		t.Fatalf("SaveUpstreamKeyCostRuns(complete): %v", err)
	}
	components, err := repo.AccountCostComponentsForDate(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil {
		t.Fatalf("AccountCostComponentsForDate() error: %v", err)
	}
	if components.AccountPurchaseCostCents != 1000 || components.ReplacementDeductionCents == nil || *components.ReplacementDeductionCents != 300 || !components.RequiresReplacementDeduction || components.ReconciledUpstreamDirectCostCents == nil || *components.ReconciledUpstreamDirectCostCents != 300 || components.SnapshotRunID != "snapshot-complete" {
		t.Fatalf("complete components = %#v", components)
	}
	if _, err := pool.Exec(ctx, `UPDATE dashboard_account_links SET effective_to='2026-08-22'::date WHERE id=$1`, link.ID); err != nil {
		t.Fatalf("close first same-day link: %v", err)
	}
	secondLink := link
	secondLink.ID, secondLink.ConnectionID, secondLink.ManualSameDaySplit = "link-key-same-day", "connection-2", true
	if err := repo.insertAccountLink(ctx, pool, secondLink); err != nil {
		t.Fatalf("insert second same-day link: %v", err)
	}
	components, err = repo.AccountCostComponentsForDate(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil || components.ReplacementDeductionCents == nil || *components.ReplacementDeductionCents != 300 {
		t.Fatalf("same key was deducted more than once: components=%#v err=%v", components, err)
	}

	incomplete := complete
	incomplete.ID = "site-run-incomplete"
	incomplete.SnapshotRunID = "snapshot-incomplete"
	incomplete.Complete = false
	incomplete.Quality = KeyCostQualityMismatch
	incomplete.KeyTotalCents = 299
	incomplete.ObservedAt = now.Add(time.Minute)
	incomplete.Items = nil
	if err := repo.SaveUpstreamKeyCostRuns(ctx, []UpstreamKeyCostRun{incomplete}); err != nil {
		t.Fatalf("SaveUpstreamKeyCostRuns(incomplete): %v", err)
	}
	exactComponents, err := repo.AccountCostComponentsForSnapshotRun(ctx, "user-1", "workspace-1", "2026-08-22", "snapshot-incomplete")
	if err != nil {
		t.Fatalf("AccountCostComponentsForSnapshotRun(incomplete) error: %v", err)
	}
	if exactComponents.ReplacementDeductionCents != nil || exactComponents.ReconciledUpstreamDirectCostCents != nil || !exactComponents.RequiresReplacementDeduction {
		t.Fatalf("incomplete requested run fell back to older complete run: %#v", exactComponents)
	}
	components, err = repo.AccountCostComponentsForDate(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil || components.ReplacementDeductionCents == nil || *components.ReplacementDeductionCents != 300 {
		t.Fatalf("incomplete run overwrote confirmed deduction: components=%#v err=%v", components, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE dashboard_account_links SET upstream_key_id='missing-key' WHERE account_asset_id=$1`, asset.ID); err != nil {
		t.Fatalf("replace link key: %v", err)
	}
	components, err = repo.AccountCostComponentsForDate(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil {
		t.Fatalf("missing key components error: %v", err)
	}
	if components.ReplacementDeductionCents != nil || !components.RequiresReplacementDeduction {
		t.Fatalf("missing replacement key became zero: %#v", components)
	}
	if _, err := pool.Exec(ctx, `UPDATE real_connections SET status='degraded' WHERE id='connection-1'`); err != nil {
		t.Fatalf("degrade live connection: %v", err)
	}
	components, err = repo.AccountCostComponentsForDate(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil {
		t.Fatalf("degraded connection components error: %v", err)
	}
	if !components.RequiresReplacementDeduction || components.ReplacementDeductionCents != nil {
		t.Fatalf("degraded live connection did not make required deduction unavailable: %#v", components)
	}
}

func TestPublishAccountStatsRefreshRollsBackKeyRunsWhenAccountStatsFail(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	run := UpstreamKeyCostRun{
		ID: "atomic-site-run", SnapshotRunID: "atomic-refresh-run", UserID: "user-1", AdminAccountID: "workspace-1",
		BusinessDate: "2026-08-22", SiteID: "site-1", ExpectedKeyCount: 1, CollectedKeyCount: 1,
		SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now,
		Items: []UpstreamKeyDailyCost{{
			RunID: "atomic-site-run", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: "2026-08-22",
			SiteID: "site-1", KeyID: "key-1", KeyName: "one", RawAmountMicros: 300_000_000,
			AdjustedCostCents: 300, Status: "ok", ObservedAt: now,
		}},
	}
	missingAssetStat := AccountDailyStat{
		ID: "atomic-stat", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: "missing-asset",
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: run.SnapshotRunID, CreatedAt: now, UpdatedAt: now,
	}

	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, []AccountDailyStat{missingAssetStat}); err == nil {
		t.Fatal("PublishAccountStatsRefresh() expected the account stat failure")
	}
	var runCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_upstream_key_cost_runs WHERE snapshot_run_id=$1`, run.SnapshotRunID).Scan(&runCount); err != nil {
		t.Fatalf("count rolled back key runs: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("failed refresh left %d key cost runs", runCount)
	}
}

func TestPublishAccountStatsRefreshRequiresAnExistingDashboardSnapshot(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-requires-snapshot", "idem-requires-snapshot", now)
	asset := repositoryTestAsset("asset-requires-snapshot", batch.ID, "account", 1000, now)
	asset.StatsMode = StatsModeAutomatic
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	run := UpstreamKeyCostRun{
		ID: "requires-snapshot-site", SnapshotRunID: "requires-snapshot-run", UserID: "user-1", AdminAccountID: "workspace-1",
		BusinessDate: "2026-08-22", SiteID: "site-1", ExpectedKeyCount: 1, CollectedKeyCount: 1,
		SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now,
	}
	rawQuota, revenue := int64(10_000_000), int64(100)
	stat := AccountDailyStat{
		ID: "requires-snapshot-stat", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: run.SnapshotRunID, RawQuotaUsedMicros: &rawQuota, RevenueCents: &revenue, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, []AccountDailyStat{stat}); err == nil {
		t.Fatal("PublishAccountStatsRefresh() accepted a refresh without a dashboard snapshot")
	}
	var runCount, statCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_upstream_key_cost_runs WHERE snapshot_run_id=$1`, run.SnapshotRunID).Scan(&runCount); err != nil {
		t.Fatalf("count key runs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_daily_stats WHERE key_cost_run_id=$1`, run.SnapshotRunID).Scan(&statCount); err != nil {
		t.Fatalf("count account stats: %v", err)
	}
	if runCount != 0 || statCount != 0 {
		t.Fatalf("unbound refresh left run=%d stats=%d", runCount, statCount)
	}
}

func TestPublishAccountStatsRefreshAtomicallyBindsCurrentDashboardSnapshot(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	date, _ := time.Parse("2006-01-02", "2026-08-22")
	if err := repo.Upsert(ctx, DailySnapshot{
		ID: "snapshot-before-refresh", UserID: "user-1", AdminAccountID: "workspace-1", Date: date,
		TodayProfit: ptrF64(100), TodayPurchase: ptrF64(1), NetProfit: ptrF64(99), CreatedAt: now,
		SettlementStatus: SettlementStatusProvisional, SnapshotSource: SnapshotSourceLiveCache,
	}); err != nil {
		t.Fatalf("seed dashboard snapshot: %v", err)
	}
	run := UpstreamKeyCostRun{
		ID: "bound-site-run", SnapshotRunID: "bound-refresh-run", UserID: "user-1", AdminAccountID: "workspace-1",
		BusinessDate: "2026-08-22", SiteID: "site-1", ExpectedKeyCount: 1, CollectedKeyCount: 1,
		SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now,
	}
	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, nil); err != nil {
		t.Fatalf("PublishAccountStatsRefresh() error: %v", err)
	}
	updated, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil || updated == nil {
		t.Fatalf("LatestDashboardSnapshot() = %#v, %v", updated, err)
	}
	if updated.AccountSnapshotRunID != run.SnapshotRunID || updated.TodayPurchase == nil || *updated.TodayPurchase != 3 ||
		updated.NetProfit == nil || *updated.NetProfit != 97 || updated.AccountStatsQuality != KeyCostQualityComplete {
		t.Fatalf("refresh was not atomically bound to dashboard snapshot: %#v", updated)
	}
	published, found, err := repo.GetPublishedAccountStatsRefresh(ctx, "user-1", "workspace-1", run.SnapshotRunID, "2026-08-22")
	if err != nil || !found || published.ExpectedSites != 1 || published.CompletedSites != 1 || published.Quality != KeyCostQualityComplete {
		t.Fatalf("bound refresh was not readable as published: response=%#v found=%v err=%v", published, found, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dashboard_daily_stats SET account_snapshot_run_id=''
		WHERE user_id='user-1' AND admin_account_id='workspace-1' AND date='2026-08-22'::date
	`); err != nil {
		t.Fatalf("unbind dashboard snapshot: %v", err)
	}
	if published, found, err := repo.GetPublishedAccountStatsRefresh(ctx, "user-1", "workspace-1", run.SnapshotRunID, "2026-08-22"); err != nil || found {
		t.Fatalf("unbound refresh still appeared published: response=%#v found=%v err=%v", published, found, err)
	}
}

func TestPublishAccountStatsRefreshRollsBackWhenAutomaticStatsConflictWithManualDay(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	quota := int64(300_000_000)
	batch := repositoryTestBatch("batch-source-conflict", "idem-source-conflict", now)
	batch.RecognitionMode, batch.StatsMode = RecognitionModeQuota, StatsModeAutomatic
	asset := repositoryTestAsset("asset-source-conflict", batch.ID, "source-conflict", 1000, now)
	asset.RecognitionMode, asset.StatsMode, asset.QuotaTotalMicros = RecognitionModeQuota, StatsModeAutomatic, &quota
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_account_daily_stats (
			id,user_id,admin_account_id,account_asset_id,business_date,source,quality,raw_quota_used_micros,created_at,updated_at
		) VALUES ('manual-day','user-1','workspace-1',$1,'2026-08-22','manual','complete',100000000,$2,$2)
	`, asset.ID, now); err != nil {
		t.Fatalf("insert manual day: %v", err)
	}
	run := UpstreamKeyCostRun{
		ID: "conflict-site-run", SnapshotRunID: "conflict-refresh-run", UserID: "user-1", AdminAccountID: "workspace-1",
		BusinessDate: "2026-08-22", SiteID: "site-1", ExpectedKeyCount: 1, CollectedKeyCount: 1,
		SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now,
	}
	rawQuota := int64(150_000_000)
	revenue := int64(100)
	stat := AccountDailyStat{
		ID: "automatic-day", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: run.SnapshotRunID, RawQuotaUsedMicros: &rawQuota, RevenueCents: &revenue, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, []AccountDailyStat{stat}); err == nil {
		t.Fatal("PublishAccountStatsRefresh() accepted a manual/automatic source conflict")
	}
	var runCount, purchaseCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_upstream_key_cost_runs WHERE snapshot_run_id=$1`, run.SnapshotRunID).Scan(&runCount); err != nil {
		t.Fatalf("count rolled back run: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_additional_costs WHERE account_asset_id=$1`, asset.ID).Scan(&purchaseCount); err != nil {
		t.Fatalf("count rolled back costs: %v", err)
	}
	if runCount != 0 || purchaseCount != 0 {
		t.Fatalf("source conflict left run=%d costs=%d", runCount, purchaseCount)
	}
}

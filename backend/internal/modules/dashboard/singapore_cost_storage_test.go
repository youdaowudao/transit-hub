package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/shared/businesstime"
)

func singaporeAccountRepository(t *testing.T) (*MetricsRepository, *pgxpool.Pool, AccountAsset, AccountLink, time.Time) {
	t.Helper()
	pool := accountAssetTestPool(t)
	repo := NewMetricsRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);INSERT INTO admin_accounts VALUES('workspace-1','user-1');CREATE TABLE real_connections(id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,own_group_ids jsonb NOT NULL,status text NOT NULL);INSERT INTO real_connections VALUES('connection','user-1','workspace-1','site','key','scope','["group"]','active')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch", "idem", now)
	batch.PurchaseDate = "2031-02-02"
	batch.RecognitionStartDate = "2031-02-02"
	batch.StatsMode = StatsModeAutomatic
	asset := repositoryTestAsset("asset", batch.ID, "验收-账号", 1000, now)
	asset.StatsMode = StatsModeAutomatic
	asset.RecognitionStartDate = "2031-02-02"
	link := AccountLink{ID: "link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID, ConnectionID: "connection", UpstreamSiteID: "site", UpstreamKeyID: "key", ScopeAdminAccountID: "scope", OwnGroupID: "group", EffectiveFrom: "2031-02-02", CreatedAt: now}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatal(err)
	}
	return repo, pool, asset, link, now
}
func singaporeAccountRun(now time.Time, date, tz, id string) UpstreamKeyCostRun {
	run := UpstreamKeyCostRun{ID: id, SnapshotRunID: id + "-batch", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: date, SiteID: "site", UsageTimezone: tz, ExpectedKeyCount: 1, CollectedKeyCount: 1, SiteTotalCents: 300, KeyTotalCents: 300, Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now}
	run.Items = []UpstreamKeyDailyCost{{RunID: id, UserID: run.UserID, AdminAccountID: run.AdminAccountID, BusinessDate: date, SiteID: "site", KeyID: "key", AdjustedCostCents: 300, Status: "ok", ObservedAt: now}}
	return run
}
func TestSingaporeCostPGRunProofAndPrivateDeductionScope(t *testing.T) {
	repo, _, _, _, now := singaporeAccountRepository(t)
	date := businesstime.DateAt(now)
	old := singaporeAccountRun(now, date, "", "old")
	if err := repo.SaveUpstreamKeyCostRuns(t.Context(), []UpstreamKeyCostRun{old}); err != nil {
		t.Fatal(err)
	}
	historical, err := repo.AccountKeyCostRunsForSnapshot(t.Context(), "user-1", "workspace-1", date, old.SnapshotRunID, "2031-02-04")
	if err != nil || len(historical) != 1 || historical[0].UsageTimezone != "" {
		t.Fatal("historical record was altered or hidden")
	}
	current, err := repo.AccountKeyCostRunsForSnapshot(t.Context(), "user-1", "workspace-1", date, old.SnapshotRunID, date)
	if err != nil || len(current) != 0 {
		t.Fatal("current legacy run remained certified")
	}
	components, err := repo.AccountCostComponentsForDate(t.Context(), "user-1", "workspace-1", date, date)
	if err != nil || components.ReconciledUpstreamDirectCostCents != nil || components.ReplacementDeductionCents != nil {
		t.Fatal("private aggregate or Key deduction accepted legacy current run")
	}
	good := singaporeAccountRun(now.Add(time.Minute), date, businesstime.Timezone, "good")
	if err := repo.SaveUpstreamKeyCostRuns(t.Context(), []UpstreamKeyCostRun{good}); err != nil {
		t.Fatal(err)
	}
	runs, err := repo.AccountKeyCostRunsForSnapshot(t.Context(), "user-1", "workspace-1", date, good.SnapshotRunID, date)
	if err != nil || len(runs) != 1 || runs[0].UsageTimezone != businesstime.Timezone {
		t.Fatal("usage_timezone failed PG round trip")
	}
	components, err = repo.AccountCostComponentsForSnapshotRun(t.Context(), "user-1", "workspace-1", date, good.SnapshotRunID, date)
	if err != nil || components.ReplacementDeductionCents == nil || *components.ReplacementDeductionCents != 300 {
		t.Fatal("certified parent run lost its Key deduction")
	}
	old.UsageTimezone = businesstime.Timezone
	if err := repo.SaveUpstreamKeyCostRuns(t.Context(), []UpstreamKeyCostRun{old}); err == nil {
		t.Fatal("same-ID legacy row was silently certified")
	}
}
func TestSingaporeCostPGCurrentAssetMaskingPreservesHistoricalAndManualRows(t *testing.T) {
	repo, pool, asset, _, now := singaporeAccountRepository(t)
	date := businesstime.DateAt(now)
	for _, day := range []string{"2031-02-02", date} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO dashboard_account_daily_stats(id,user_id,admin_account_id,account_asset_id,business_date,source,quality,key_cost_run_id,raw_quota_used_micros,revenue_cents,upstream_cost_cents,replacement_deduction_cents) VALUES($1,'user-1','workspace-1',$2,$3::date,'automatic','complete','legacy',5,10,15,15)`, "stat-"+day, asset.ID, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.ListAccountAssets(t.Context(), "user-1", "workspace-1", AccountAssetFilter{}); err == nil {
		t.Fatal("missing internal date silently bypassed scope")
	}
	detail, err := repo.GetAccountAssetDetail(t.Context(), "user-1", "workspace-1", asset.ID, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.DailyStats) != 2 || detail.DailyStats[0].RawQuotaUsedMicros == nil || detail.DailyStats[1].RawQuotaUsedMicros != nil || detail.DailyStats[1].UpstreamCostCents != nil || detail.DailyStats[1].ReplacementDeductionCents != nil || detail.DailyStats[1].Quality != KeyCostQualityMissing {
		t.Fatal("current legacy mask changed historical values or exposed current money")
	}
	page, err := repo.ListAccountAssets(t.Context(), "user-1", "workspace-1", AccountAssetFilter{CurrentDate: date})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].QuotaUsedMicros == nil || *page.Items[0].QuotaUsedMicros != 5 || page.Items[0].StatsQuality != "partial" {
		t.Fatal("asset cumulative total included unconfirmed current usage")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE dashboard_account_daily_stats SET source='manual' WHERE business_date=$1::date`, date); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.GetAccountAssetDetail(t.Context(), "user-1", "workspace-1", asset.ID, date)
	if err != nil || detail.DailyStats[1].RawQuotaUsedMicros == nil || *detail.DailyStats[1].RawQuotaUsedMicros != 5 {
		t.Fatal("manual row was masked")
	}
}
func TestSingaporeCostPGPublishRollsBackHistoricalBusinessMutation(t *testing.T) {
	repo, pool, asset, _, now := singaporeAccountRepository(t)
	date := businesstime.DateAt(now)
	ctx := withAccountCurrentDate(t.Context(), date)
	if _, err := pool.Exec(ctx, `INSERT INTO dashboard_account_daily_stats(id,user_id,admin_account_id,account_asset_id,business_date,source,quality,key_cost_run_id,raw_quota_used_micros,revenue_cents,upstream_cost_cents) VALUES('history','user-1','workspace-1',$1,'2031-02-02','automatic','complete','history',5,10,15)`, asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION corrupt_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.business_date='2031-02-03'::date THEN UPDATE dashboard_account_daily_stats SET raw_quota_used_micros=6 WHERE id='history'; END IF; RETURN NEW; END $$;CREATE TRIGGER corrupt_history_after_insert AFTER INSERT ON dashboard_account_daily_stats FOR EACH ROW EXECUTE FUNCTION corrupt_history()`); err != nil {
		t.Fatal(err)
	}
	day, _ := time.Parse("2006-01-02", date)
	if err := repo.Upsert(ctx, DailySnapshot{ID: "snapshot", UserID: "user-1", AdminAccountID: "workspace-1", Date: day, TodayProfit: ptrF64(20), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := singaporeAccountRun(now, date, businesstime.Timezone, "new")
	amount := int64(5)
	stat := AccountDailyStat{ID: "new-stat", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID, BusinessDate: date, Source: StatsModeAutomatic, Quality: KeyCostQualityComplete, KeyCostRunID: run.SnapshotRunID, RawQuotaUsedMicros: &amount, RevenueCents: &amount, UpstreamCostCents: &amount, CreatedAt: now, UpdatedAt: now}
	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, []AccountDailyStat{stat}); err == nil {
		t.Fatal("publication committed a historical business change")
	}
	var quota int64
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT raw_quota_used_micros FROM dashboard_account_daily_stats WHERE id='history'`).Scan(&quota); err != nil || quota != 5 {
		t.Fatal("historical change escaped rollback")
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM dashboard_upstream_key_cost_runs WHERE id='new'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed transaction left a Key run")
	}
}

func TestSingaporeCostPGLedgerKeepsAmountsAndChecksCurrentQuotaNotes(t *testing.T) {
	repo, pool, asset, _, now := singaporeAccountRepository(t)
	date := businesstime.DateAt(now)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `UPDATE dashboard_account_assets SET recognition_mode='quota',quota_total_micros=100 WHERE id=$1`, asset.ID); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2031-02-02", date} {
		if _, err := pool.Exec(ctx, `INSERT INTO dashboard_additional_costs(id,user_id,admin_account_id,type,name,business_date,amount_cents,source_id,batch_id,account_asset_id,note) VALUES($1,'user-1','workspace-1','account_purchase','验收-额度',$2::date,123,'quota','batch',$3,'原保存说明')`, "ledger-"+day, day, asset.ID); err != nil {
			t.Fatal(err)
		}
	}
	read := func(wantUnconfirmed bool) {
		t.Helper()
		page, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{CurrentDate: date})
		if err != nil || len(page.Items) != 2 {
			t.Fatalf("ledger read: %v, rows=%d", err, len(page.Items))
		}
		for _, item := range page.Items {
			if item.AmountCents != 123 || item.Amount != 1.23 || !strings.HasPrefix(item.Note, "原保存说明") {
				t.Fatal("read projection changed saved amount or note")
			}
			got := strings.Contains(item.Note, "尚未按新加坡日期重新确认")
			if got != (wantUnconfirmed && item.BusinessDate == date) {
				t.Fatal("current quota note disagreed with certification or changed historical note")
			}
		}
	}
	read(true)
	run := singaporeAccountRun(now, date, businesstime.Timezone, "ledger-certified")
	if err := repo.SaveUpstreamKeyCostRuns(ctx, []UpstreamKeyCostRun{run}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO dashboard_account_daily_stats(id,user_id,admin_account_id,account_asset_id,business_date,source,quality,key_cost_run_id,raw_quota_used_micros) VALUES('ledger-stat','user-1','workspace-1',$1,$2::date,'automatic','complete',$3,5)`, asset.ID, date, run.SnapshotRunID); err != nil {
		t.Fatal(err)
	}
	read(false)
	if _, err := pool.Exec(ctx, `UPDATE dashboard_account_daily_stats SET source='manual',key_cost_run_id='' WHERE id='ledger-stat'`); err != nil {
		t.Fatal(err)
	}
	read(false)
	var note string
	if err := pool.QueryRow(ctx, `SELECT note FROM dashboard_additional_costs WHERE id=$1`, "ledger-"+date).Scan(&note); err != nil || note != "原保存说明" {
		t.Fatal("ledger explanation was written back to saved records")
	}
}

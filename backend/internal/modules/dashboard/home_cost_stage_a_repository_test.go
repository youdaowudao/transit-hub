package dashboard

import (
	"context"
	"reflect"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostStageAPublishPreservesEveryHomepageCostColumnAndLivePreservesAccountBinding(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('workspace-1','user-1')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := now.UTC().Truncate(24 * time.Hour)
	dateText := date.Format("2006-01-02")
	before := DailySnapshot{ID: "before", UserID: "user-1", AdminAccountID: "workspace-1", Date: date, CreatedAt: now, TodayProfit: ptrF64(20), TodayPurchase: ptrF64(4), NetProfit: ptrF64(16), CostExpectedCount: intPtr(3), CostCollectedCount: intPtr(2), CostFreshCount: intPtr(1), CostRetainedCount: intPtr(1), CostMissingCount: intPtr(1), CostQualityMode: "partial", OperatingCost: ptrF64(5), AdjustedNetProfit: ptrF64(15), ReplacementDeduction: ptrF64(2), SettlementStatus: SettlementStatusPartial, SnapshotSource: SnapshotSourceLiveCache}
	if err := repo.Upsert(ctx, before); err != nil {
		t.Fatal(err)
	}
	run := UpstreamKeyCostRun{ID: "site-run", SnapshotRunID: "refresh-run", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: dateText, SiteID: "site", Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now, SiteTotalCents: 900, KeyTotalCents: 900}
	if err := repo.PublishAccountStatsRefresh(ctx, []UpstreamKeyCostRun{run}, nil); err != nil {
		t.Fatal(err)
	}
	after, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", dateText)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"TodayPurchase", "NetProfit", "CostExpectedCount", "CostCollectedCount", "CostFreshCount", "CostRetainedCount", "CostMissingCount", "CostQualityMode", "OperatingCost", "AdjustedNetProfit", "ReplacementDeduction"} {
		want := reflect.ValueOf(before).FieldByName(field).Interface()
		got := reflect.ValueOf(*after).FieldByName(field).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("published %s=%v; want unchanged %v", field, got, want)
		}
	}
	if after.AccountSnapshotRunID != "refresh-run" || after.AccountStatsQuality != KeyCostQualityComplete {
		t.Fatalf("account result was not bound: %#v", after)
	}
	live := before
	live.TodayPurchase = ptrF64(7)
	live.AccountSnapshotRunID = "wrong-live-run"
	live.AccountExpectedCount = intPtr(19)
	live.AccountCompletedCount = intPtr(17)
	live.AccountStatsQuality = KeyCostQualityMissing
	if err := repo.Upsert(ctx, live); err != nil {
		t.Fatal(err)
	}
	after, err = repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", dateText)
	if err != nil {
		t.Fatal(err)
	}
	if after.AccountSnapshotRunID != "refresh-run" || after.AccountExpectedCount == nil || *after.AccountExpectedCount != 0 || after.AccountCompletedCount == nil || *after.AccountCompletedCount != 0 || after.AccountStatsQuality != KeyCostQualityComplete {
		t.Fatalf("live cache changed account columns: %#v", after)
	}
	if _, found, err := repo.GetPublishedAccountStatsRefresh(ctx, "user-1", "workspace-1", "refresh-run", dateText); err != nil || !found {
		t.Fatalf("live cache broke idempotency: found=%v err=%v", found, err)
	}
}

func TestHomeCostStageALiveReplacementReadsKeysAndPreservesMatchingRules(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);INSERT INTO admin_accounts VALUES('workspace-1','user-1');CREATE TABLE real_connections(id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,own_group_ids jsonb NOT NULL,status text NOT NULL);INSERT INTO real_connections VALUES('connection','user-1','workspace-1','site','key','admin','["group"]'::jsonb,'active')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	batch := repositoryTestBatch("batch", "idem", now)
	batch.PurchaseDate = date
	batch.RecognitionStartDate = date
	asset := repositoryTestAsset("asset", batch.ID, "account", 1000, now)
	asset.RecognitionStartDate = date
	link := AccountLink{ID: "link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID, ConnectionID: "connection", UpstreamSiteID: "site", UpstreamKeyID: "key", ScopeAdminAccountID: "admin", OwnGroupID: "group", EffectiveFrom: date, CreatedAt: now}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatal(err)
	}
	keys := upstream.KeyUsageForDateResult{BusinessDate: date, Sites: []upstream.KeyUsageSiteResult{{SiteID: "site", Complete: true, Status: "retained", Items: []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "key", RawAmount: 3.5, RechargeRate: 2, TodayAmount: 7}}}}}
	assertDeduction := func(want *int64) {
		t.Helper()
		components, err := repo.AccountCostComponentsFromKeySnapshot(ctx, "user-1", "workspace-1", date, keys)
		if err != nil {
			t.Fatal(err)
		}
		if !components.RequiresReplacementDeduction || !reflect.DeepEqual(components.ReplacementDeductionCents, want) {
			t.Fatalf("snapshot deduction=%#v want=%v", components, want)
		}
	}
	amount := int64(700)
	assertDeduction(&amount)
	keys.Sites[0].Complete = false
	keys.Sites[0].Status = "missing"
	assertDeduction(nil)
	keys.Sites[0].Complete = true
	keys.Sites[0].Status = "ok"
	keys.Sites[0].Items[0].Merged = true
	keys.Sites[0].Items[0].KeyIDs = []string{"key", "same-name-key"}
	assertDeduction(nil)
	keys.Sites[0].Items[0].Merged = false
	for _, update := range []string{
		`UPDATE real_connections SET own_group_ids='["group","other"]'::jsonb WHERE id='connection'`,
		`UPDATE real_connections SET own_group_ids='["other"]'::jsonb WHERE id='connection'`,
		`UPDATE real_connections SET own_group_ids='["group"]'::jsonb,admin_account_id='other-admin' WHERE id='connection'`,
	} {
		if _, err := pool.Exec(ctx, update); err != nil {
			t.Fatal(err)
		}
		assertDeduction(nil)
	}
	if _, err := pool.Exec(ctx, `UPDATE real_connections SET admin_account_id='admin' WHERE id='connection'`); err != nil {
		t.Fatal(err)
	}
	assertDeduction(&amount)
	if _, err := pool.Exec(ctx, `INSERT INTO real_connections VALUES('key-conflict','user-1','workspace-1','site','key','another-admin','["another-group"]'::jsonb,'active')`); err != nil {
		t.Fatal(err)
	}
	assertDeduction(nil)
	if _, err := pool.Exec(ctx, `DELETE FROM real_connections WHERE id='key-conflict'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE real_connections SET status='degraded' WHERE id='connection'`); err != nil {
		t.Fatal(err)
	}
	assertDeduction(nil)
	if _, err := pool.Exec(ctx, `UPDATE real_connections SET status='active' WHERE id='connection';INSERT INTO real_connections VALUES('conflict','user-1','workspace-1','site','other-key','admin','["group"]'::jsonb,'active')`); err != nil {
		t.Fatal(err)
	}
	assertDeduction(nil)
}

package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
)

func mergedRepositoryFixture(t *testing.T) (*MetricsRepository, *pgxpool.Pool, time.Time, upstream.KeyUsageForDateResult, []upstream.SiteCostForDateResult) {
	t.Helper()
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);INSERT INTO admin_accounts VALUES('workspace-1','user-1');CREATE TABLE real_connections(id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,own_group_ids jsonb NOT NULL,status text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	keys, totals, _ := mergedAccountFixture(now)
	date := keys.BusinessDate
	batch := repositoryTestBatch("merged-batch", "merged-idem", now)
	batch.PurchaseDate = date
	batch.RecognitionStartDate = date
	batch.StatsMode = StatsModeAutomatic
	var assets []AccountAsset
	var links []AccountLink
	for _, id := range []string{"merged-main", "merged-other", "single"} {
		asset := repositoryTestAsset(id, batch.ID, id, 1000, now)
		asset.StatsMode = StatsModeAutomatic
		asset.RecognitionStartDate = date
		assets = append(assets, asset)
		link := AccountLink{ID: "link-" + id, UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: id, ConnectionID: "connection-" + id, UpstreamSiteID: "site", UpstreamKeyID: id, ScopeAdminAccountID: "scope", OwnGroupID: id, EffectiveFrom: date, CreatedAt: now}
		links = append(links, link)
		if _, err := pool.Exec(ctx, `INSERT INTO real_connections VALUES($1,'user-1','workspace-1','site',$2,'scope',jsonb_build_array($2::text),'active')`, link.ConnectionID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, assets, links, nil); err != nil {
		t.Fatal(err)
	}
	return repo, pool, now, keys, totals
}

func TestHomeCostMergedPersistencePreservesUnsplitStatusAndReadbackRejectsBothIDs(t *testing.T) {
	repo, _, now, keys, totals := mergedRepositoryFixture(t)
	ctx := context.Background()
	runs := buildAccountKeyCostRuns("user-1", "workspace-1", "persisted-merged", keys.BusinessDate, totals, keys, now)
	if err := repo.SaveUpstreamKeyCostRuns(ctx, runs); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.AccountKeyCostRunsForSnapshot(ctx, "user-1", "workspace-1", keys.BusinessDate, "persisted-merged")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || !stored[0].Complete || stored[0].KeyTotalCents != 300 || len(stored[0].Items) != 2 {
		t.Fatalf("site aggregate changed in persistence: %#v", stored)
	}
	_, _, targets := mergedAccountFixture(now)
	for _, item := range stored[0].Items {
		if item.KeyID == "merged-main" && item.Status != "merged" {
			t.Errorf("merged status did not survive persistence: %#v", item)
		}
	}
	for _, target := range targets[:2] {
		if _, _, ok := findAutomaticAccountKey(target, stored); ok {
			t.Errorf("readback confirmed aggregate for %s", target.Asset.ID)
		}
	}
	if _, item, ok := findAutomaticAccountKey(targets[2], stored); !ok || item.AdjustedCostCents != 200 {
		t.Fatal("readback lost the independent key amount")
	}
}

func TestHomeCostMergedFinalizationKeepsAccountSubstateIncomplete(t *testing.T) {
	repo, pool, now, keys, totals := mergedRepositoryFixture(t)
	ctx := context.Background()
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{siteCostResults: totals}, result: keys}
	store := newFakeSessionStore()
	store.set("user-1", "workspace-1", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(store, &fakePlatformClient{usageStats: 10, scopeUsage: map[string]upstream.AdminUsageStats{"scope|single": {TotalActualCost: 4}}}, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.now = func() time.Time { return now }
	if err := service.finalizeBusinessDate(ctx, ActiveSessionRef{UserID: "user-1", AdminAccountID: "workspace-1"}, keys.BusinessDate, SnapshotSourceDatedQuery); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", keys.BusinessDate)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.SettlementStatus != SettlementStatusFinal || snapshot.TodayPurchase == nil || *snapshot.TodayPurchase != 3 {
		t.Fatalf("base site settlement changed: %#v", snapshot)
	}
	if snapshot.AccountExpectedCount == nil || *snapshot.AccountExpectedCount != 3 || snapshot.AccountCompletedCount == nil || *snapshot.AccountCompletedCount != 1 || snapshot.AccountStatsQuality != KeyCostQualityMissing {
		t.Fatalf("finalized account substate incorrectly complete: %#v", snapshot)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_daily_stats WHERE user_id='user-1' AND admin_account_id='workspace-1' AND business_date=$1::date AND account_asset_id IN ('merged-main','merged-other')`, keys.BusinessDate).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("merged accounts retained %d confirmed monetary rows", count)
	}
	var amount, deduction int64
	var observedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT upstream_cost_cents,replacement_deduction_cents,observed_at FROM dashboard_account_daily_stats WHERE user_id='user-1' AND admin_account_id='workspace-1' AND business_date=$1::date AND account_asset_id='single'`, keys.BusinessDate).Scan(&amount, &deduction, &observedAt); err != nil {
		t.Fatal(err)
	}
	if amount != 200 || deduction != 200 || !observedAt.Equal(now) {
		t.Fatalf("independent account amount=%d deduction=%d observed=%v", amount, deduction, observedAt)
	}
}

func TestHomeCostMergedDayEndDeductionRejectsPrimaryOnlyBinding(t *testing.T) {
	repo, pool, now, keys, totals := mergedRepositoryFixture(t)
	ctx := context.Background()
	runs := buildAccountKeyCostRuns("user-1", "workspace-1", "primary-only", keys.BusinessDate, totals, keys, now)
	if err := repo.SaveUpstreamKeyCostRuns(ctx, runs); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_account_links WHERE user_id='user-1' AND admin_account_id='workspace-1' AND id IN ('link-merged-other','link-single')`); err != nil {
		t.Fatal(err)
	}
	components, err := repo.AccountCostComponentsForSnapshotRun(ctx, "user-1", "workspace-1", keys.BusinessDate, "primary-only")
	if err != nil {
		t.Fatal(err)
	}
	if !components.RequiresReplacementDeduction || components.ReplacementDeductionCents != nil {
		t.Fatalf("day-end deduction attributed aggregate to primary ID: %#v", components)
	}
}

func TestHomeCostMergedUnknownStatusCannotProduceConfirmedAccountAmount(t *testing.T) {
	target := AutomaticAccountTarget{Link: AccountLink{UpstreamSiteID: "site", UpstreamKeyID: "key"}}
	for _, status := range []string{"merged", "missing", "unknown"} {
		runs := []UpstreamKeyCostRun{{SiteID: "site", Complete: true, Items: []UpstreamKeyDailyCost{{KeyID: "key", Status: status, AdjustedCostCents: 100}}}}
		if _, _, ok := findAutomaticAccountKey(target, runs); ok {
			t.Errorf("status %q produced a confirmed amount", status)
		}
	}
	for _, status := range []string{"", "ok"} {
		runs := []UpstreamKeyCostRun{{SiteID: "site", Complete: true, Items: []UpstreamKeyDailyCost{{KeyID: "key", Status: status, AdjustedCostCents: 100}}}}
		if _, _, ok := findAutomaticAccountKey(target, runs); !ok {
			t.Errorf("compatible status %q lost a confirmed amount", status)
		}
	}
}

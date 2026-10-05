package dashboard

import (
	"context"
	"testing"
	"time"
)

// A primary ID remains identifiable, but a name-based aggregate cannot confirm
// that ID's monetary amount, even if no other merged ID has an account binding.
func TestHomeCostMergedPrimaryOnlyPersistedDeductionIsUnavailable(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);
		INSERT INTO admin_accounts VALUES('workspace-1','user-1');
		CREATE TABLE real_connections(id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,own_group_ids jsonb NOT NULL,status text NOT NULL);
		INSERT INTO real_connections VALUES('connection','user-1','workspace-1','site','merged-main','admin','["group"]'::jsonb,'active')`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := "2031-02-03"
	batch := repositoryTestBatch("batch", "idem", now)
	batch.PurchaseDate, batch.RecognitionStartDate = date, date
	asset := repositoryTestAsset("asset", batch.ID, "account", 1000, now)
	asset.RecognitionStartDate = date
	link := AccountLink{ID: "link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID, ConnectionID: "connection", UpstreamSiteID: "site", UpstreamKeyID: "merged-main", ScopeAdminAccountID: "admin", OwnGroupID: "group", EffectiveFrom: date, CreatedAt: now}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatal(err)
	}
	run := UpstreamKeyCostRun{ID: "site-run", SnapshotRunID: "snapshot-run", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: date, SiteID: "site", Complete: true, Quality: KeyCostQualityComplete, ObservedAt: now, SiteTotalCents: 100, KeyTotalCents: 100, Items: []UpstreamKeyDailyCost{{RunID: "site-run", UserID: "user-1", AdminAccountID: "workspace-1", BusinessDate: date, SiteID: "site", KeyID: "merged-main", Status: "merged", RawAmountMicros: 1000000, AdjustedCostCents: 100, ObservedAt: now}}}
	if err := repo.SaveUpstreamKeyCostRuns(ctx, []UpstreamKeyCostRun{run}); err != nil {
		t.Fatal(err)
	}
	components, err := repo.AccountCostComponentsForSnapshotRun(ctx, "user-1", "workspace-1", date, "snapshot-run")
	if err != nil {
		t.Fatal(err)
	}
	if !components.RequiresReplacementDeduction || components.ReplacementDeductionCents != nil {
		t.Errorf("merged primary-only binding confirmed a deduction: %#v", components)
	}
	if components.ReconciledUpstreamDirectCostCents == nil || *components.ReconciledUpstreamDirectCostCents != 100 {
		t.Errorf("site aggregate was lost: %#v", components)
	}
	if _, err := pool.Exec(ctx, `UPDATE dashboard_upstream_key_daily_costs SET status='ok' WHERE run_id='site-run' AND key_id='merged-main'`); err != nil {
		t.Fatal(err)
	}
	components, err = repo.AccountCostComponentsForSnapshotRun(ctx, "user-1", "workspace-1", date, "snapshot-run")
	if err != nil {
		t.Fatal(err)
	}
	if components.ReplacementDeductionCents == nil || *components.ReplacementDeductionCents != 100 {
		t.Errorf("independent confirmed key lost its deduction: %#v", components)
	}
}

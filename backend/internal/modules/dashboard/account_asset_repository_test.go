package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/shared/businesstime"
)

func TestAccountAssetRepositoryCreatesBatchAtomicallyAndDeduplicatesIdempotency(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id, user_id) VALUES ('workspace-1', 'user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}

	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-1", "idem-1", now)
	assets := []AccountAsset{
		repositoryTestAsset("asset-1", batch.ID, "account-a", 500, now),
		repositoryTestAsset("asset-2", batch.ID, "account-b", 500, now),
	}
	costs := []AdditionalCostRecord{
		repositoryTestCost("cost-1", batch.ID, assets[0].ID, 500, now),
		repositoryTestCost("cost-2", batch.ID, assets[1].ID, 500, now),
	}
	created, err := repo.CreateAccountBatch(ctx, batch, assets, nil, costs)
	if err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if created.Batch.ID != batch.ID || len(created.Assets) != 2 {
		t.Fatalf("created batch = %#v", created)
	}

	duplicate := repositoryTestBatch("batch-duplicate", batch.IdempotencyKey, now.Add(time.Minute))
	deduplicated, err := repo.CreateAccountBatch(ctx, duplicate, []AccountAsset{
		repositoryTestAsset("asset-duplicate", duplicate.ID, "other", 1000, now),
	}, nil, nil)
	if err != nil {
		t.Fatalf("idempotent CreateAccountBatch() error: %v", err)
	}
	if deduplicated.Batch.ID != batch.ID || len(deduplicated.Assets) != 2 {
		t.Fatalf("idempotent result = %#v, want original batch", deduplicated)
	}
	assertTableCount(t, pool, "dashboard_account_batches", 1)
	assertTableCount(t, pool, "dashboard_account_assets", 2)
	assertTableCount(t, pool, "dashboard_additional_costs", 2)
	ledger, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{
		From: "2026-08-22", To: "2026-08-22", Platform: "Claude", Channel: "A", BatchID: batch.ID, AccountAssetID: assets[0].ID,
	})
	if err != nil {
		t.Fatalf("ListAccountCostLedger() error: %v", err)
	}
	if len(ledger.Items) != 1 || ledger.Items[0].ID != "cost-1" {
		t.Fatalf("filtered ledger = %#v", ledger)
	}

	broken := repositoryTestBatch("batch-broken", "idem-broken", now)
	duplicateAsset := repositoryTestAsset("asset-conflict", broken.ID, "broken", 500, now)
	if _, err := repo.CreateAccountBatch(ctx, broken, []AccountAsset{duplicateAsset, duplicateAsset}, nil, []AdditionalCostRecord{
		repositoryTestCost("cost-broken", broken.ID, duplicateAsset.ID, 500, now),
	}); err == nil {
		t.Fatal("CreateAccountBatch() expected a duplicate asset error")
	}
	var brokenBatches int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_batches WHERE id = 'batch-broken'`).Scan(&brokenBatches); err != nil {
		t.Fatalf("count broken batch: %v", err)
	}
	if brokenBatches != 0 {
		t.Fatalf("broken transaction left %d batch rows", brokenBatches)
	}
}

func TestAccountCostLedgerPaginatesWholeSourceGroups(t *testing.T) {
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
	batch := repositoryTestBatch("batch-ledger-pages", "idem-ledger-pages", now)
	first := repositoryTestAsset("asset-ledger-first", batch.ID, "first", 300, now)
	second := repositoryTestAsset("asset-ledger-second", batch.ID, "second", 100, now)
	costs := []AdditionalCostRecord{
		repositoryTestDatedCost("first-1", batch.ID, first.ID, "2026-08-20", 100, now),
		repositoryTestDatedCost("first-2", batch.ID, first.ID, "2026-08-21", 100, now),
		repositoryTestDatedCost("first-3", batch.ID, first.ID, "2026-08-22", 100, now),
		repositoryTestDatedCost("second-1", batch.ID, second.ID, "2026-08-19", 100, now),
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{first, second}, nil, costs); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	pageOne, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListAccountCostLedger(page one) error: %v", err)
	}
	if len(pageOne.Items) != 3 || !pageOne.HasMore {
		t.Fatalf("first source group was split by row pagination: %#v", pageOne)
	}
	for _, record := range pageOne.Items {
		if record.AccountAssetID != first.ID {
			t.Fatalf("page one mixed source groups: %#v", pageOne)
		}
	}
	pageTwo, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{Page: 2, PageSize: 1})
	if err != nil || len(pageTwo.Items) != 1 || pageTwo.Items[0].AccountAssetID != second.ID || pageTwo.HasMore {
		t.Fatalf("page two = %#v, err=%v", pageTwo, err)
	}
}

func TestAccountAssetRepositoryListAndGetStayInsideWorkspace(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id, user_id) VALUES ('workspace-1','user-1'),('workspace-2','user-1')`); err != nil {
		t.Fatalf("insert admin accounts: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	for _, workspace := range []string{"workspace-1", "workspace-2"} {
		batch := repositoryTestBatch("batch-"+workspace, "idem-"+workspace, now)
		batch.AdminAccountID = workspace
		asset := repositoryTestAsset("asset-"+workspace, batch.ID, "same-name", 1000, now)
		asset.AdminAccountID = workspace
		if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
			t.Fatalf("create %s batch: %v", workspace, err)
		}
	}

	items, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{Platform: "Claude", Search: "same"})
	if err != nil {
		t.Fatalf("ListAccountAssets() error: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].ID != "asset-workspace-1" {
		t.Fatalf("workspace-1 list = %#v", items)
	}
	if _, err := repo.GetAccountAsset(ctx, "user-1", "workspace-2", "asset-workspace-1"); !errors.Is(err, ErrAccountAssetNotFound) {
		t.Fatalf("cross-workspace GetAccountAsset() error = %v, want not found", err)
	}
}

func TestAccountAssetRepositoryLifecycleEventsAreAppendOnlyIdempotentAndCostConserving(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id, user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	today, err := time.ParseInLocation("2006-01-02", businesstime.Today(), businesstime.Location())
	if err != nil {
		t.Fatalf("parse business date: %v", err)
	}
	startDate := today.AddDate(0, 0, -4).Format("2006-01-02")
	deadDate := today.AddDate(0, 0, -3).Format("2006-01-02")
	restoreDate := today.AddDate(0, 0, -2).Format("2006-01-02")
	refundDate := today.AddDate(0, 0, -1).Format("2006-01-02")
	excessiveRefundDate := today.Format("2006-01-02")
	now := today.Add(10 * time.Hour)
	batch := repositoryTestBatch("batch-life", "idem-life", now)
	batch.PurchaseDate = startDate
	batch.RecognitionStartDate = startDate
	batch.RecognitionMode = RecognitionModeDaily
	batch.RecognitionDays = 3
	asset := repositoryTestAsset("asset-life", batch.ID, "life-account", 1000, now)
	asset.RecognitionStartDate = startDate
	asset.RecognitionMode = RecognitionModeDaily
	asset.RecognitionDays = 3
	link := AccountLink{
		ID: "link-life", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-life", UpstreamSiteID: "site-life", UpstreamKeyID: "key-life",
		ScopeAdminAccountID: "scope-life", OwnGroupID: "group-life", EffectiveFrom: startDate, CreatedAt: now,
	}
	costs := []AdditionalCostRecord{
		repositoryTestDatedCost("cost-life-1", batch.ID, asset.ID, startDate, 333, now),
		repositoryTestDatedCost("cost-life-2", batch.ID, asset.ID, deadDate, 333, now),
		repositoryTestDatedCost("cost-life-3", batch.ID, asset.ID, restoreDate, 334, now),
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, costs); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-active", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: startDate, Status: AccountStatusActive,
		IdempotencyKey: "active", CreatedAt: now,
	}); err != nil {
		t.Fatalf("activate event: %v", err)
	}
	dead := AccountEvent{
		ID: "event-dead", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: deadDate, Status: AccountStatusDead,
		IdempotencyKey: "dead", CreatedAt: now.Add(time.Minute),
	}
	if _, err := repo.AppendAccountEvent(ctx, dead); err != nil {
		t.Fatalf("dead event: %v", err)
	}
	assertAccountLinkEffectiveTo(t, pool, link.ID, deadDate)
	if _, err := repo.AppendAccountEvent(ctx, dead); err != nil {
		t.Fatalf("idempotent dead event: %v", err)
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 1000)
	assertAccountDateCost(t, pool, asset.ID, restoreDate, 0)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-restore", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventRestore, EffectiveDate: restoreDate, Status: AccountStatusActive,
		IdempotencyKey: "restore", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("restore event: %v", err)
	}
	assertAccountLinkEffectiveTo(t, pool, link.ID, deadDate)
	assertAccountPurchaseCost(t, pool, asset.ID, 1000)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-refund", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventRefund, EffectiveDate: refundDate, RefundCents: int64Pointer(400),
		IdempotencyKey: "refund", CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("partial refund event: %v", err)
	}
	current, err := repo.GetAccountAsset(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil || current.CurrentStatus != AccountStatusActive {
		t.Fatalf("partial refund changed status: asset=%#v err=%v", current, err)
	}
	assertAccountRefundCost(t, pool, asset.ID, -400)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-refund-too-much", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventRefund, EffectiveDate: excessiveRefundDate, RefundCents: int64Pointer(601),
		IdempotencyKey: "refund-too-much", CreatedAt: now.Add(4 * time.Minute),
	}); err == nil {
		t.Fatal("cumulative refund above purchase cost was accepted")
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_events WHERE account_asset_id=$1`, asset.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 4 {
		t.Fatalf("event count = %d, want 4 immutable events", eventCount)
	}
}

func TestAccountAssetRepositoryProjectsCurrentStateByEffectiveDateInsteadOfEntryOrder(t *testing.T) {
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
	now := time.Now().UTC()
	today, err := time.ParseInLocation("2006-01-02", businesstime.Today(), businesstime.Location())
	if err != nil {
		t.Fatalf("parse today: %v", err)
	}
	date := func(days int) string { return today.AddDate(0, 0, days).Format("2006-01-02") }
	batch := repositoryTestBatch("batch-projection", "idem-projection", now)
	batch.PurchaseDate, batch.RecognitionStartDate = date(-2), date(-2)
	asset := repositoryTestAsset("asset-projection", batch.ID, "projection-account", 1000, now)
	asset.RecognitionStartDate = date(-2)
	link := AccountLink{
		ID: "link-projection", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-projection", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: date(-2), CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "future-active", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: date(2), Status: AccountStatusActive,
		IdempotencyKey: "future-active", CreatedAt: now,
	}); err != nil {
		t.Fatalf("future active event: %v", err)
	}
	assertAccountProjectedState(t, repo, asset.ID, AccountStatusUnactivated, StatsModeManual)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "backfilled-active", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: date(-1), Status: AccountStatusActive,
		IdempotencyKey: "backfilled-active", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("backfilled active event: %v", err)
	}
	assertAccountProjectedState(t, repo, asset.ID, AccountStatusActive, StatsModeManual)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "future-automatic", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: date(3), StatsMode: StatsModeAutomatic,
		IdempotencyKey: "future-automatic", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("future stats mode event: %v", err)
	}
	assertAccountProjectedState(t, repo, asset.ID, AccountStatusActive, StatsModeManual)

	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "current-automatic", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: date(0), StatsMode: StatsModeAutomatic,
		IdempotencyKey: "current-automatic", CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("current stats mode event: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dashboard_account_assets SET current_status=$2,stats_mode=$3 WHERE id=$1`,
		asset.ID, AccountStatusUnactivated, StatsModeManual); err != nil {
		t.Fatalf("simulate overnight stale projection: %v", err)
	}
	assertAccountProjectedState(t, repo, asset.ID, AccountStatusActive, StatsModeAutomatic)
}

func TestReplaceAccountLinkUsesLifecycleStateAtEffectiveDate(t *testing.T) {
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
	today, err := time.ParseInLocation("2006-01-02", businesstime.Today(), businesstime.Location())
	if err != nil {
		t.Fatalf("parse today: %v", err)
	}
	date := func(days int) string { return today.AddDate(0, 0, days).Format("2006-01-02") }
	now := time.Now().UTC()
	batch := repositoryTestBatch("batch-link-state", "idem-link-state", now)
	batch.PurchaseDate, batch.RecognitionStartDate = date(-1), date(-1)
	asset := repositoryTestAsset("asset-link-state", batch.ID, "link-state", 1000, now)
	asset.RecognitionStartDate = date(-1)
	initialLink := AccountLink{
		ID: "link-state-initial", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-initial", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: date(-1), CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{initialLink}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	for index, event := range []AccountEvent{
		{ID: "link-state-active", EventType: AccountEventStatus, EffectiveDate: date(0), Status: AccountStatusActive},
		{ID: "link-state-dead", EventType: AccountEventStatus, EffectiveDate: date(1), Status: AccountStatusDead},
		{ID: "link-state-restored", EventType: AccountEventRestore, EffectiveDate: date(3), Status: AccountStatusActive},
	} {
		event.UserID, event.AdminAccountID, event.AccountAssetID = "user-1", "workspace-1", asset.ID
		event.IdempotencyKey, event.CreatedAt = event.ID, now.Add(time.Duration(index)*time.Minute)
		if _, err := repo.AppendAccountEvent(ctx, event); err != nil {
			t.Fatalf("AppendAccountEvent(%s) error: %v", event.ID, err)
		}
	}
	deadLink := AccountLink{
		ID: "link-state-dead-replacement", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-dead", UpstreamSiteID: "site-2", UpstreamKeyID: "key-2",
		ScopeAdminAccountID: "scope-2", OwnGroupID: "group-2", EffectiveFrom: date(2), CreatedAt: now,
	}
	if err := repo.ReplaceAccountLink(ctx, AccountEvent{
		ID: "link-at-dead-date", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: date(2), IdempotencyKey: "link-at-dead-date", CreatedAt: now,
	}, &deadLink, ""); err == nil {
		t.Fatal("ReplaceAccountLink() accepted a link while the projected account state was dead")
	}
	restoredLink := deadLink
	restoredLink.ID, restoredLink.ConnectionID, restoredLink.EffectiveFrom = "link-state-restored-replacement", "connection-restored", date(3)
	if err := repo.ReplaceAccountLink(ctx, AccountEvent{
		ID: "link-at-restored-date", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: date(3), IdempotencyKey: "link-at-restored-date", CreatedAt: now,
	}, &restoredLink, ""); err != nil {
		t.Fatalf("ReplaceAccountLink() rejected a link after the projected restore: %v", err)
	}
}

func TestAccountAssetRepositoryDoesNotExposeFutureLinkAsActive(t *testing.T) {
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
	now := time.Now().UTC()
	tomorrow := time.Now().In(businesstime.Location()).AddDate(0, 0, 1).Format("2006-01-02")
	batch := repositoryTestBatch("batch-future-link", "idem-future-link", now)
	asset := repositoryTestAsset("asset-future-link", batch.ID, "future-link", 1000, now)
	link := AccountLink{
		ID: "link-future", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-future", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: tomorrow, CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	items, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{PageSize: 20})
	if err != nil {
		t.Fatalf("ListAccountAssets() error: %v", err)
	}
	detail, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil {
		t.Fatalf("GetAccountAssetDetail() error: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].HasActiveLink || detail.Asset.HasActiveLink {
		t.Fatalf("future link exposed as active: items=%#v detail=%#v", items, detail.Asset)
	}
}

func TestAccountAssetRepositoryRefundAndCloseSettlesRemainingPurchaseCost(t *testing.T) {
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
	batch := repositoryTestBatch("batch-refund-close", "idem-refund-close", now)
	batch.RecognitionMode, batch.RecognitionDays = RecognitionModeDaily, 3
	asset := repositoryTestAsset("asset-refund-close", batch.ID, "refund-close", 1000, now)
	asset.RecognitionMode, asset.RecognitionDays, asset.CurrentStatus = RecognitionModeDaily, 3, AccountStatusActive
	costs := []AdditionalCostRecord{
		repositoryTestDatedCost("refund-close-1", batch.ID, asset.ID, "2026-08-22", 333, now),
		repositoryTestDatedCost("refund-close-2", batch.ID, asset.ID, "2026-08-23", 333, now),
		repositoryTestDatedCost("refund-close-3", batch.ID, asset.ID, "2026-08-24", 334, now),
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, costs); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "refund-close", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventRefund, EffectiveDate: "2026-08-23", RefundCents: int64Pointer(100), Status: AccountStatusClosed,
		IdempotencyKey: "refund-close", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("refund and close event: %v", err)
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 1000)
	assertAccountDateCost(t, pool, asset.ID, "2026-08-24", 0)
	assertAccountRefundCost(t, pool, asset.ID, -100)
}

func TestAccountAssetRepositoryReplacesLinkWithEffectiveHistoryAndIdempotency(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1'),('workspace-2','user-1')`); err != nil {
		t.Fatalf("insert admin accounts: %v", err)
	}
	today, err := time.ParseInLocation("2006-01-02", businesstime.Today(), businesstime.Location())
	if err != nil {
		t.Fatalf("parse business date: %v", err)
	}
	oldEffectiveDate := today.AddDate(0, 0, -1).Format("2006-01-02")
	currentEffectiveDate := today.Format("2006-01-02")
	newEffectiveDate := today.AddDate(0, 0, 1).Format("2006-01-02")
	now := today.Add(10 * time.Hour)
	batch := repositoryTestBatch("batch-link", "batch-link-idem", now)
	batch.PurchaseDate = oldEffectiveDate
	batch.RecognitionStartDate = oldEffectiveDate
	asset := repositoryTestAsset("asset-link", batch.ID, "linked-account", 1000, now)
	asset.RecognitionStartDate = oldEffectiveDate
	asset.UpstreamReferenceURL = "https://supplier.example/old"
	oldLink := AccountLink{
		ID: "link-old", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-old", UpstreamSiteID: "site-old", UpstreamKeyID: "key-old",
		ScopeAdminAccountID: "scope-old", OwnGroupID: "group-old", EffectiveFrom: oldEffectiveDate,
		UpstreamReferenceURL: "https://supplier.example/old", CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{oldLink}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	newLink := AccountLink{
		ID: "link-new", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-new", UpstreamSiteID: "site-new", UpstreamKeyID: "key-new",
		ScopeAdminAccountID: "scope-new", OwnGroupID: "group-new", EffectiveFrom: newEffectiveDate,
		UpstreamReferenceURL: "https://supplier.example/new", CreatedAt: now.Add(time.Minute),
	}
	event := AccountEvent{
		ID: "event-link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: newEffectiveDate, Note: "换号",
		IdempotencyKey: "link-change", CreatedAt: now.Add(time.Minute),
	}
	if err := repo.ReplaceAccountLink(ctx, event, &newLink, newLink.UpstreamReferenceURL); err != nil {
		t.Fatalf("ReplaceAccountLink() error: %v", err)
	}
	if err := repo.ReplaceAccountLink(ctx, event, &newLink, newLink.UpstreamReferenceURL); err != nil {
		t.Fatalf("idempotent ReplaceAccountLink() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-stats-mode", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: currentEffectiveDate, StatsMode: StatsModeAutomatic,
		IdempotencyKey: "stats-mode", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("automatic stats mode event: %v", err)
	}
	detail, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil {
		t.Fatalf("GetAccountAssetDetail() error: %v", err)
	}
	if len(detail.Links) != 2 || detail.Links[0].EffectiveTo == nil || *detail.Links[0].EffectiveTo != currentEffectiveDate || detail.Links[1].EffectiveTo != nil {
		t.Fatalf("link history = %#v", detail.Links)
	}
	if detail.Asset.UpstreamReferenceURL != oldLink.UpstreamReferenceURL || detail.Links[1].UpstreamReferenceURL != newLink.UpstreamReferenceURL {
		t.Fatalf("reference history asset=%#v links=%#v", detail.Asset, detail.Links)
	}
	projectedLink, err := projectAccountMetadataAtDate(ctx, pool, detail.Asset, newEffectiveDate)
	if err != nil || projectedLink.UpstreamReferenceURL != newLink.UpstreamReferenceURL {
		t.Fatalf("future link reference projection = %#v, err=%v", projectedLink, err)
	}
	if detail.Asset.StatsMode != StatsModeAutomatic || len(detail.Events) != 2 ||
		detail.Events[0].EventType != AccountEventStatsModeChange || detail.Events[1].EventType != AccountEventLinkChange {
		t.Fatalf("link events = %#v", detail.Events)
	}
	crossWorkspace := event
	crossWorkspace.ID, crossWorkspace.AdminAccountID, crossWorkspace.IdempotencyKey = "event-cross", "workspace-2", "cross"
	if err := repo.ReplaceAccountLink(ctx, crossWorkspace, nil, "https://supplier.example/cross"); !errors.Is(err, ErrAccountAssetNotFound) {
		t.Fatalf("cross-workspace ReplaceAccountLink() error = %v, want not found", err)
	}
}

func assertAccountLinkEffectiveTo(t *testing.T, pool *pgxpool.Pool, linkID, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(context.Background(), `SELECT effective_to::text FROM dashboard_account_links WHERE id=$1`, linkID).Scan(&got); err != nil {
		t.Fatalf("query account link effective_to: %v", err)
	}
	if got != want {
		t.Fatalf("account link effective_to = %q, want %q", got, want)
	}
}

func TestAccountAssetRepositoryQuotaEventsRecognizeCumulativeDifferenceAndRejectRegression(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-quota", "idem-quota", now)
	batch.RecognitionMode = RecognitionModeQuota
	asset := repositoryTestAsset("asset-quota", batch.ID, "quota-account", 1001, now)
	asset.RecognitionMode = RecognitionModeQuota
	asset.QuotaTotalMicros = int64Pointer(300_000_000)
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	for index, observation := range []struct {
		date string
		used int64
	}{{"2026-08-22", 100_000_000}, {"2026-08-24", 200_000_000}, {"2026-08-23", 150_000_000}} {
		if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
			ID: fmt.Sprintf("event-quota-%d", index), UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
			EventType: AccountEventQuotaObservation, EffectiveDate: observation.date,
			QuotaUsedMicros: int64Pointer(observation.used), IdempotencyKey: fmt.Sprintf("quota-%d", index), CreatedAt: now.Add(time.Duration(index) * time.Minute),
		}); err != nil {
			t.Fatalf("quota event %d: %v", index, err)
		}
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 667)
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-quota-regression", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventQuotaObservation, EffectiveDate: "2026-08-25", QuotaUsedMicros: int64Pointer(150_000_000),
		IdempotencyKey: "quota-regression", CreatedAt: now.Add(3 * time.Minute),
	}); err == nil {
		t.Fatal("quota regression was accepted")
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 667)
	assertAccountDailyQuota(t, pool, asset.ID, "2026-08-22", 100_000_000)
	assertAccountDailyQuota(t, pool, asset.ID, "2026-08-23", 50_000_000)
	assertAccountDailyQuota(t, pool, asset.ID, "2026-08-24", 50_000_000)
	assertAccountDailyRecognizedCost(t, pool, asset.ID, "2026-08-22", 333)
	assertAccountDailyRecognizedCost(t, pool, asset.ID, "2026-08-23", 167)
	assertAccountDailyRecognizedCost(t, pool, asset.ID, "2026-08-24", 167)
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-exhausted", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: "2026-08-24", Status: AccountStatusExhausted,
		IdempotencyKey: "exhausted", CreatedAt: now.Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("exhausted event: %v", err)
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 1001)
	assertAccountDailyRecognizedCost(t, pool, asset.ID, "2026-08-24", 501)
}

func TestAccountAssetRepositoryManualDailyStatsBuildLifecyclePerformanceFromTotals(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY, user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-performance", "idem-performance", now)
	batch.AccountingMode = AccountingModeAdditive
	asset := repositoryTestAsset("asset-performance", batch.ID, "performance-account", 10_000, now)
	asset.AccountingMode = AccountingModeAdditive
	asset.CurrentStatus = AccountStatusActive
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, []AdditionalCostRecord{
		repositoryTestCost("cost-performance", batch.ID, asset.ID, 10_000, now),
	}); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	observations := []struct {
		date         string
		rawMicros    int64
		revenueCents int64
		upstreamCost int64
	}{
		{date: "2026-08-22", rawMicros: 40_000_000, revenueCents: 8_000, upstreamCost: 1_500},
		{date: "2026-08-23", rawMicros: 50_000_000, revenueCents: 10_000, upstreamCost: 2_000},
	}
	for index, observation := range observations {
		if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
			ID: fmt.Sprintf("event-manual-%d", index), UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
			EventType: AccountEventManualObservation, EffectiveDate: observation.date,
			QuotaUsedMicros: int64Pointer(observation.rawMicros), RevenueCents: int64Pointer(observation.revenueCents),
			UpstreamCostCents: int64Pointer(observation.upstreamCost), IdempotencyKey: fmt.Sprintf("manual-%d", index),
			CreatedAt: now.Add(time.Duration(index) * time.Minute),
		}); err != nil {
			t.Fatalf("manual observation %d: %v", index, err)
		}
	}

	detail, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil {
		t.Fatalf("GetAccountAssetDetail() error: %v", err)
	}
	if len(detail.DailyStats) != 2 || len(detail.Events) != 2 {
		t.Fatalf("detail history = %#v", detail)
	}
	assertFloatPointer(t, "detail average multiplier", detail.Performance.AverageSaleMultiplier, 2)
	assertFloatPointer(t, "detail recovery multiple", detail.Performance.CostRecoveryMultiple, float64(10_000)/float64(12_000))
	if detail.Performance.BreakevenDifferenceCents == nil || *detail.Performance.BreakevenDifferenceCents != -2_000 || detail.Performance.FinalProfitCents != nil {
		t.Fatalf("active performance = %#v", detail.Performance)
	}
	items, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{PageSize: 20})
	if err != nil {
		t.Fatalf("ListAccountAssets() error: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].QuotaUsedMicros == nil || *items.Items[0].QuotaUsedMicros != 50_000_000 || items.Items[0].Performance == nil {
		t.Fatalf("asset summary = %#v", items)
	}
	assertFloatPointer(t, "list average multiplier", items.Items[0].Performance.AverageSaleMultiplier, 2)

	if _, err := pool.Exec(ctx, `
		UPDATE dashboard_account_daily_stats SET quality='missing'
		WHERE user_id='user-1' AND admin_account_id='workspace-1' AND account_asset_id=$1 AND business_date='2026-08-23'::date
	`, asset.ID); err != nil {
		t.Fatalf("mark one daily stat incomplete: %v", err)
	}
	detail, err = repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil {
		t.Fatalf("GetAccountAssetDetail(incomplete) error: %v", err)
	}
	if detail.Performance.AverageSaleMultiplier != nil || detail.Performance.CostRecoveryMultiple != nil ||
		detail.Performance.BreakevenDifferenceCents != nil || !slices.Contains(detail.Performance.MissingFields, "dailyStats") {
		t.Fatalf("incomplete detail still exposed derived performance: %#v", detail.Performance)
	}
	items, err = repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{PageSize: 20})
	if err != nil {
		t.Fatalf("ListAccountAssets(incomplete) error: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].Performance == nil || items.Items[0].Performance.AverageSaleMultiplier != nil ||
		items.Items[0].Performance.BreakevenDifferenceCents != nil || !slices.Contains(items.Items[0].Performance.MissingFields, "dailyStats") {
		t.Fatalf("incomplete list still exposed derived performance: %#v", items)
	}
}

func TestAccountLinkSameDayTransferSplitsConfirmedConnectionTotalsAcrossBothAccounts(t *testing.T) {
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
		) VALUES ('connection-1','user-1','workspace-1','site-1','key-1','scope-1','["group-1"]'::jsonb,'active');
	`); err != nil {
		t.Fatalf("create live connection: %v", err)
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	batch := repositoryTestBatch("batch-same-day-transfer", "idem-same-day-transfer", now)
	batch.StatsMode, batch.AccountingMode = StatsModeAutomatic, AccountingModeAdditive
	oldAsset := repositoryTestAsset("asset-old", batch.ID, "old-account", 1000, now)
	newAsset := repositoryTestAsset("asset-new", batch.ID, "new-account", 1000, now)
	oldAsset.StatsMode, newAsset.StatsMode = StatsModeAutomatic, StatsModeAutomatic
	oldAsset.AccountingMode, newAsset.AccountingMode = AccountingModeAdditive, AccountingModeAdditive
	oldLink := AccountLink{
		ID: "link-old", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: oldAsset.ID,
		ConnectionID: "connection-1", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: "2026-08-22", CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{oldAsset, newAsset}, []AccountLink{oldLink}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_account_daily_stats (
			id,user_id,admin_account_id,account_asset_id,business_date,source,quality,
			raw_quota_used_micros,revenue_cents,upstream_cost_cents,created_at,updated_at
		) VALUES ('confirmed-total','user-1','workspace-1',$1,'2026-08-22','automatic','complete',100000000,20000,333,$2,$2)
	`, oldAsset.ID, now); err != nil {
		t.Fatalf("insert confirmed connection totals: %v", err)
	}
	invalidOldQuota, invalidOldRevenue := int64(60_000_000), int64(12_000)
	invalidNewQuota, invalidNewRevenue := int64(50_000_000), int64(9000)
	invalidLink := oldLink
	invalidLink.ID, invalidLink.AccountAssetID, invalidLink.ManualSameDaySplit = "link-invalid", newAsset.ID, true
	invalidLink.PreviousQuotaUsedMicros, invalidLink.PreviousRevenueCents = &invalidOldQuota, &invalidOldRevenue
	invalidLink.ReplacementQuotaUsedMicros, invalidLink.ReplacementRevenueCents = &invalidNewQuota, &invalidNewRevenue
	if err := repo.ReplaceAccountLink(ctx, AccountEvent{
		ID: "event-invalid-split", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: newAsset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: "2026-08-22", IdempotencyKey: "invalid-split", CreatedAt: now,
	}, &invalidLink, ""); err == nil {
		t.Fatal("ReplaceAccountLink() accepted split totals above the confirmed connection totals")
	}
	var openOwner string
	if err := pool.QueryRow(ctx, `SELECT account_asset_id FROM dashboard_account_links WHERE connection_id='connection-1' AND effective_to IS NULL`).Scan(&openOwner); err != nil || openOwner != oldAsset.ID {
		t.Fatalf("failed split changed connection owner: owner=%q err=%v", openOwner, err)
	}

	oldQuota, oldRevenue := int64(40_000_000), int64(8000)
	newQuota, newRevenue := int64(60_000_000), int64(12_000)
	validLink := invalidLink
	validLink.ID = "link-new"
	validLink.PreviousQuotaUsedMicros, validLink.PreviousRevenueCents = &oldQuota, &oldRevenue
	validLink.ReplacementQuotaUsedMicros, validLink.ReplacementRevenueCents = &newQuota, &newRevenue
	if err := repo.ReplaceAccountLink(ctx, AccountEvent{
		ID: "event-valid-split", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: newAsset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: "2026-08-22", IdempotencyKey: "valid-split", CreatedAt: now.Add(time.Minute),
	}, &validLink, ""); err != nil {
		t.Fatalf("ReplaceAccountLink(valid split) error: %v", err)
	}
	var oldEffectiveTo *string
	var oldManual, newManual bool
	if err := pool.QueryRow(ctx, `SELECT effective_to::text,manual_same_day_split FROM dashboard_account_links WHERE id='link-old'`).Scan(&oldEffectiveTo, &oldManual); err != nil {
		t.Fatalf("read old link: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT manual_same_day_split FROM dashboard_account_links WHERE id='link-new' AND effective_to IS NULL`).Scan(&newManual); err != nil {
		t.Fatalf("read new link: %v", err)
	}
	if oldEffectiveTo == nil || *oldEffectiveTo != "2026-08-22" || !oldManual || !newManual {
		t.Fatalf("same-day link periods were not preserved: oldTo=%v oldManual=%v newManual=%v", oldEffectiveTo, oldManual, newManual)
	}
	for _, expected := range []struct {
		assetID  string
		quota    int64
		revenue  int64
		upstream int64
	}{{oldAsset.ID, oldQuota, oldRevenue, 133}, {newAsset.ID, newQuota, newRevenue, 200}} {
		var source, quality string
		var quota, revenue, upstream int64
		if err := pool.QueryRow(ctx, `
			SELECT source,quality,raw_quota_used_micros,revenue_cents,upstream_cost_cents FROM dashboard_account_daily_stats
			WHERE account_asset_id=$1 AND business_date='2026-08-22'::date
		`, expected.assetID).Scan(&source, &quality, &quota, &revenue, &upstream); err != nil {
			t.Fatalf("read split stat for %s: %v", expected.assetID, err)
		}
		if source != StatsModeManual || quality != KeyCostQualityComplete || quota != expected.quota ||
			revenue != expected.revenue || upstream != expected.upstream {
			t.Fatalf("split stat for %s = source %s quality %s quota %d revenue %d upstream %d", expected.assetID, source, quality, quota, revenue, upstream)
		}
	}
	targets, err := repo.ListAutomaticAccountTargets(ctx, "user-1", "workspace-1", "2026-08-22")
	if err != nil || len(targets) != 0 {
		t.Fatalf("same-day split still entered automatic refresh: targets=%#v err=%v", targets, err)
	}
	targets, err = repo.ListAutomaticAccountTargets(ctx, "user-1", "workspace-1", "2026-08-23")
	if err != nil || len(targets) != 2 || targets[0].Asset.ID != newAsset.ID || targets[0].Link.ID != "link-new" ||
		targets[1].Asset.ID != oldAsset.ID || targets[1].Link.ID != "" {
		t.Fatalf("replacement did not resume while the unlinked old account remained explicitly missing: targets=%#v err=%v", targets, err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "new-account-manual", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: newAsset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: "2026-08-23", StatsMode: StatsModeManual,
		IdempotencyKey: "new-account-manual", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("switch replacement account to manual: %v", err)
	}
	cumulativeQuota, cumulativeRevenue, cumulativeUpstream := int64(70_000_000), int64(14_000), int64(233)
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "new-account-cumulative", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: newAsset.ID,
		EventType: AccountEventManualObservation, EffectiveDate: "2026-08-23",
		QuotaUsedMicros: &cumulativeQuota, RevenueCents: &cumulativeRevenue, UpstreamCostCents: &cumulativeUpstream,
		IdempotencyKey: "new-account-cumulative", CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("append cumulative observation after split: %v", err)
	}
	var dayOneQuota, dayOneRevenue, dayOneUpstream, dayTwoQuota, dayTwoRevenue, dayTwoUpstream int64
	if err := pool.QueryRow(ctx, `
		SELECT raw_quota_used_micros,revenue_cents,upstream_cost_cents
		FROM dashboard_account_daily_stats WHERE account_asset_id=$1 AND business_date='2026-08-22'::date
	`, newAsset.ID).Scan(&dayOneQuota, &dayOneRevenue, &dayOneUpstream); err != nil {
		t.Fatalf("read split baseline after cumulative rebuild: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT raw_quota_used_micros,revenue_cents,upstream_cost_cents
		FROM dashboard_account_daily_stats WHERE account_asset_id=$1 AND business_date='2026-08-23'::date
	`, newAsset.ID).Scan(&dayTwoQuota, &dayTwoRevenue, &dayTwoUpstream); err != nil {
		t.Fatalf("read post-split daily delta: %v", err)
	}
	if dayOneQuota != 60_000_000 || dayOneRevenue != 12_000 || dayOneUpstream != 200 ||
		dayTwoQuota != 10_000_000 || dayTwoRevenue != 2_000 || dayTwoUpstream != 33 {
		t.Fatalf("post-split cumulative values were double counted: day1=%d/%d/%d day2=%d/%d/%d",
			dayOneQuota, dayOneRevenue, dayOneUpstream, dayTwoQuota, dayTwoRevenue, dayTwoUpstream)
	}
}

func TestAccountLinkRejectsOverlappingHistoricalOwnershipIntervals(t *testing.T) {
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
	batch := repositoryTestBatch("batch-overlap", "idem-overlap", now)
	first := repositoryTestAsset("asset-overlap-a", batch.ID, "overlap-a", 1000, now)
	second := repositoryTestAsset("asset-overlap-b", batch.ID, "overlap-b", 1000, now)
	to := "2026-08-25"
	links := []AccountLink{
		{ID: "link-overlap-a", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: first.ID,
			ConnectionID: "connection-1", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1",
			EffectiveFrom: "2026-08-22", EffectiveTo: &to, CreatedAt: now},
		{ID: "link-overlap-b", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: second.ID,
			ConnectionID: "connection-1", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1",
			EffectiveFrom: "2026-08-24", CreatedAt: now},
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{first, second}, links, nil); err == nil {
		t.Fatal("CreateAccountBatch() accepted overlapping historical connection/key/group ownership")
	}
	var batchCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_batches WHERE id=$1`, batch.ID).Scan(&batchCount); err != nil {
		t.Fatalf("count rolled back overlap batch: %v", err)
	}
	if batchCount != 0 {
		t.Fatalf("overlap validation left batch row behind: %d", batchCount)
	}
}

func TestAccountEventsAndLinkChangesRejectDatesBeforePurchase(t *testing.T) {
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
	batch := repositoryTestBatch("batch-date-floor", "idem-date-floor", now)
	asset := repositoryTestAsset("asset-date-floor", batch.ID, "date-floor", 1000, now)
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "event-before-purchase", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatus, EffectiveDate: "2026-08-21", Status: AccountStatusDead,
		IdempotencyKey: "event-before-purchase", CreatedAt: now,
	}); err == nil {
		t.Fatal("AppendAccountEvent() accepted an effective date before purchase")
	}
	if err := repo.ReplaceAccountLink(ctx, AccountEvent{
		ID: "link-before-purchase", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventLinkChange, EffectiveDate: "2026-08-21",
		IdempotencyKey: "link-before-purchase", CreatedAt: now,
	}, nil, ""); err == nil {
		t.Fatal("ReplaceAccountLink() accepted an effective date before purchase")
	}
	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_account_events WHERE account_asset_id=$1`, asset.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("invalid dates left %d events", eventCount)
	}
}

func TestAccountMetadataCorrectionsAreAppendOnlyAndProjectedByEffectiveDate(t *testing.T) {
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
	now := time.Now().UTC()
	batch := repositoryTestBatch("batch-metadata", "idem-metadata", now)
	yesterday := time.Now().In(businesstime.Location()).AddDate(0, 0, -1).Format("2006-01-02")
	batch.PurchaseDate, batch.RecognitionStartDate = yesterday, yesterday
	batch.PurchaseURL = "https://supplier.example/orders/original"
	asset := repositoryTestAsset("asset-metadata", batch.ID, "old-name", 1000, now)
	asset.Platform, asset.Channel, asset.AccountType = "OldPlatform", "OldChannel", "OldType"
	asset.RecognitionStartDate = yesterday
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	tomorrow := time.Now().In(businesstime.Location()).AddDate(0, 0, 1).Format("2006-01-02")
	futureUpstreamURL := "https://supplier.example/future"
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "metadata-future", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventMetadataCorrection, EffectiveDate: tomorrow, Identifier: "future-name",
		Platform: "FuturePlatform", Channel: "FutureChannel", AccountType: "FutureType",
		UpstreamReferenceURL: &futureUpstreamURL, IdempotencyKey: "metadata-future", CreatedAt: now,
	}); err != nil {
		t.Fatalf("future metadata correction: %v", err)
	}
	current, err := repo.GetAccountAsset(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil || current.Identifier != "old-name" || current.Platform != "OldPlatform" {
		t.Fatalf("future correction changed current asset: asset=%#v err=%v", current, err)
	}
	today := businesstime.Today()
	correctPurchaseURL := "https://supplier.example/orders/correct"
	correctUpstreamURL := "https://supplier.example/correct"
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "metadata-current", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventMetadataCorrection, EffectiveDate: today, Identifier: "correct-name",
		Platform: "CorrectPlatform", Channel: "CorrectChannel", AccountType: "CorrectType",
		PurchaseURL: &correctPurchaseURL, UpstreamReferenceURL: &correctUpstreamURL,
		IdempotencyKey: "metadata-current", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("current metadata correction: %v", err)
	}
	detail, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil {
		t.Fatalf("GetAccountAssetDetail() error: %v", err)
	}
	if detail.Asset.Identifier != "correct-name" || detail.Asset.Platform != "CorrectPlatform" ||
		detail.Asset.Channel != "CorrectChannel" || detail.Asset.AccountType != "CorrectType" ||
		detail.Asset.UpstreamReferenceURL != "https://supplier.example/correct" ||
		detail.Batch.PurchaseURL != correctPurchaseURL || len(detail.Events) != 2 {
		t.Fatalf("metadata projection/history = %#v", detail)
	}
	if detail.Events[0].Identifier == "" || detail.Events[1].Identifier == "" {
		t.Fatalf("metadata correction history lost values: %#v", detail.Events)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "metadata-platform-only", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventMetadataCorrection, EffectiveDate: today, Identifier: "filtered-name",
		Platform: "FilteredPlatform", Channel: "FilteredChannel", AccountType: "FilteredType",
		IdempotencyKey: "metadata-platform-only", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("metadata correction without links: %v", err)
	}
	linkPreserved, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil || linkPreserved.Asset.UpstreamReferenceURL != "https://supplier.example/correct" {
		t.Fatalf("metadata correction without link cleared existing value: asset=%#v err=%v", linkPreserved.Asset, err)
	}
	if err := repo.insertAdditionalCosts(ctx, pool, []AdditionalCostRecord{{
		ID: "metadata-filter-cost", UserID: "user-1", AdminAccountID: "workspace-1",
		Type: AdditionalCostAccountPurchase, Name: asset.Identifier, BusinessDate: today,
		AmountCents: 100, Amount: 1, OriginalAmount: 1, SourceID: asset.ID, BatchID: batch.ID,
		AccountAssetID: asset.ID, CreatedAt: now.Add(2 * time.Minute),
	}}); err != nil {
		t.Fatalf("insert account cost for metadata filter: %v", err)
	}
	if err := repo.insertAdditionalCosts(ctx, pool, []AdditionalCostRecord{{
		ID: "metadata-historical-filter-cost", UserID: "user-1", AdminAccountID: "workspace-1",
		Type: AdditionalCostAccountPurchase, Name: asset.Identifier, BusinessDate: yesterday,
		AmountCents: 200, Amount: 2, OriginalAmount: 2, SourceID: asset.ID, BatchID: batch.ID,
		AccountAssetID: asset.ID, CreatedAt: now.Add(2 * time.Minute),
	}}); err != nil {
		t.Fatalf("insert historical account cost for metadata filter: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dashboard_account_assets SET identifier='stale-name',platform='StalePlatform',
		  channel='StaleChannel',account_type='StaleType' WHERE id=$1
	`, asset.ID); err != nil {
		t.Fatalf("simulate stale materialized metadata: %v", err)
	}
	filteredAssets, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{
		Platform: "FilteredPlatform", Channel: "FilteredChannel", AccountType: "FilteredType", Search: "filtered-name",
	})
	if err != nil || len(filteredAssets.Items) != 1 {
		t.Fatalf("filter by projected account metadata = %#v, err=%v", filteredAssets, err)
	}
	filteredLedger, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{
		Platform: "FilteredPlatform", Channel: "FilteredChannel",
	})
	if err != nil || len(filteredLedger.Items) != 1 {
		t.Fatalf("filter ledger by projected account metadata = %#v, err=%v", filteredLedger, err)
	}
	historicalLedger, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{
		Platform: "OldPlatform", Channel: "OldChannel",
	})
	if err != nil || len(historicalLedger.Items) != 1 || historicalLedger.Items[0].BusinessDate != yesterday {
		t.Fatalf("filter ledger by business-date metadata = %#v, err=%v", historicalLedger, err)
	}
	emptyPurchaseURL := ""
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "metadata-clear-link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventMetadataCorrection, EffectiveDate: today, Identifier: "filtered-name",
		Platform: "FilteredPlatform", Channel: "FilteredChannel", AccountType: "FilteredType",
		PurchaseURL: &emptyPurchaseURL, UpstreamReferenceURL: &emptyPurchaseURL, IdempotencyKey: "metadata-clear-link", CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("clear upstream reference link: %v", err)
	}
	cleared, err := repo.GetAccountAssetDetail(ctx, "user-1", "workspace-1", asset.ID)
	if err != nil || cleared.Asset.UpstreamReferenceURL != "" || cleared.Batch.PurchaseURL != "" {
		t.Fatalf("cleared reference links = upstream %q purchase %q, err=%v", cleared.Asset.UpstreamReferenceURL, cleared.Batch.PurchaseURL, err)
	}
	historical, err := projectAccountMetadataAtDate(ctx, pool, detail.Asset, yesterday)
	if err != nil {
		t.Fatalf("projectAccountMetadataAtDate(before correction) error: %v", err)
	}
	if historical.Identifier != "old-name" || historical.Platform != "OldPlatform" || historical.Channel != "OldChannel" ||
		historical.AccountType != "OldType" || historical.UpstreamReferenceURL != "" {
		t.Fatalf("metadata before first correction was overwritten: %#v", historical)
	}
}

func TestListAccountAssetsReturnsExplicitHasMore(t *testing.T) {
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
	now := time.Now().UTC()
	batch := repositoryTestBatch("batch-asset-pages", "idem-asset-pages", now)
	batch.Quantity = 51
	assets := make([]AccountAsset, 51)
	for i := range assets {
		assets[i] = repositoryTestAsset(fmt.Sprintf("asset-page-%02d", i), batch.ID, fmt.Sprintf("account-%02d", i), 100, now.Add(time.Duration(i)*time.Second))
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, assets, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	first, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{Page: 1, PageSize: 50})
	if err != nil || len(first.Items) != 50 || !first.HasMore {
		t.Fatalf("first account asset page = %#v, err=%v", first, err)
	}
	second, err := repo.ListAccountAssets(ctx, "user-1", "workspace-1", AccountAssetFilter{Page: 2, PageSize: 50})
	if err != nil || len(second.Items) != 1 || second.HasMore {
		t.Fatalf("second account asset page = %#v, err=%v", second, err)
	}
}

func TestSaveAutomaticAccountDailyStatsRecognizesQuotaCostWithoutDuplicateRefresh(t *testing.T) {
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
	batch := repositoryTestBatch("batch-auto-quota", "idem-auto-quota", now)
	batch.RecognitionMode, batch.StatsMode = RecognitionModeQuota, StatsModeAutomatic
	asset := repositoryTestAsset("asset-auto-quota", batch.ID, "auto-quota", 1001, now)
	asset.RecognitionMode = RecognitionModeQuota
	asset.StatsMode = StatsModeAutomatic
	asset.QuotaTotalMicros = int64Pointer(300_000_000)
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	dayOneRaw := int64(100_000_000)
	dayOneRevenue := int64(1000)
	dayOne := AccountDailyStat{
		ID: "stat-auto-1", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: "run-1", RawQuotaUsedMicros: &dayOneRaw, RevenueCents: &dayOneRevenue, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{dayOne}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(day one) error: %v", err)
	}
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{dayOne}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(repeated day one) error: %v", err)
	}
	assertAccountDateCost(t, pool, asset.ID, "2026-08-22", 333)

	dayTwoRaw := int64(200_000_000)
	dayTwoRevenue := int64(2000)
	dayTwo := dayOne
	dayTwo.ID, dayTwo.BusinessDate, dayTwo.KeyCostRunID, dayTwo.RawQuotaUsedMicros, dayTwo.RevenueCents = "stat-auto-2", "2026-08-23", "run-2", &dayTwoRaw, &dayTwoRevenue
	dayTwo.CreatedAt, dayTwo.UpdatedAt = now.Add(24*time.Hour), now.Add(24*time.Hour)
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{dayTwo}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(day two) error: %v", err)
	}
	assertAccountPurchaseCost(t, pool, asset.ID, 1001)
	assertAccountDateCost(t, pool, asset.ID, "2026-08-23", 668)
}

func TestSaveAutomaticAccountDailyStatsRebuildsQuotaCostWhenOlderDayArrivesLater(t *testing.T) {
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
	batch := repositoryTestBatch("batch-auto-quota-order", "idem-auto-quota-order", now)
	batch.PurchaseDate, batch.RecognitionStartDate = "2026-08-21", "2026-08-21"
	batch.RecognitionMode, batch.StatsMode = RecognitionModeQuota, StatsModeAutomatic
	asset := repositoryTestAsset("asset-auto-quota-order", batch.ID, "auto-quota-order", 1001, now)
	asset.RecognitionMode, asset.RecognitionStartDate, asset.StatsMode = RecognitionModeQuota, "2026-08-21", StatsModeAutomatic
	asset.QuotaTotalMicros = int64Pointer(300_000_000)
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	dayTwoRaw := int64(200_000_000)
	dayTwoRevenue := int64(2000)
	dayTwo := AccountDailyStat{
		ID: "stat-order-2", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: "run-order-2", RawQuotaUsedMicros: &dayTwoRaw, RevenueCents: &dayTwoRevenue, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{dayTwo}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(later day first) error: %v", err)
	}
	dayOneRaw := int64(100_000_000)
	dayOneRevenue := int64(1000)
	dayOne := dayTwo
	dayOne.ID, dayOne.BusinessDate, dayOne.KeyCostRunID, dayOne.RawQuotaUsedMicros, dayOne.RevenueCents = "stat-order-1", "2026-08-21", "run-order-1", &dayOneRaw, &dayOneRevenue
	dayOne.CreatedAt, dayOne.UpdatedAt = now.Add(time.Minute), now.Add(time.Minute)
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{dayOne}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(older day later) error: %v", err)
	}
	assertAccountDateCost(t, pool, asset.ID, "2026-08-21", 333)
	assertAccountDateCost(t, pool, asset.ID, "2026-08-22", 668)
	assertAccountPurchaseCost(t, pool, asset.ID, 1001)
}

func TestSaveAutomaticAccountDailyStatsUsesModeProjectedForHistoricalDate(t *testing.T) {
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
	batch := repositoryTestBatch("batch-historical-mode", "idem-historical-mode", now)
	batch.PurchaseDate, batch.RecognitionStartDate, batch.StatsMode = "2026-08-21", "2026-08-21", StatsModeAutomatic
	asset := repositoryTestAsset("asset-historical-mode", batch.ID, "historical-mode", 1000, now)
	asset.RecognitionStartDate, asset.StatsMode = "2026-08-21", StatsModeAutomatic
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "mode-manual-today", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: "2026-08-22", StatsMode: StatsModeManual,
		IdempotencyKey: "mode-manual-today", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendAccountEvent(stats mode) error: %v", err)
	}
	rawQuota := int64(10_000_000)
	revenue := int64(100)
	stat := AccountDailyStat{
		ID: "historical-auto-stat", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-21", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: "historical-run", RawQuotaUsedMicros: &rawQuota, RevenueCents: &revenue, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{stat}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(historical automatic date) error: %v", err)
	}
}

func TestManualCumulativeObservationsIncludeAutomaticIntervalsAcrossModeSwitches(t *testing.T) {
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
	batch := repositoryTestBatch("batch-mode-switch-totals", "idem-mode-switch-totals", now)
	batch.AccountingMode, batch.StatsMode = AccountingModeAdditive, StatsModeAutomatic
	asset := repositoryTestAsset("asset-mode-switch-totals", batch.ID, "mode-switch-totals", 1000, now)
	asset.AccountingMode, asset.StatsMode = AccountingModeAdditive, StatsModeAutomatic
	link := AccountLink{
		ID: "link-mode-switch-totals", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-mode-switch", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: "2026-08-22", CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	saveAutomatic := func(id, date string, value int64) {
		t.Helper()
		if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{{
			ID: id, UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
			BusinessDate: date, Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
			KeyCostRunID: "run-" + date, RawQuotaUsedMicros: int64Pointer(value), RevenueCents: int64Pointer(value),
			UpstreamCostCents: int64Pointer(value), CreatedAt: now, UpdatedAt: now,
		}}); err != nil {
			t.Fatalf("SaveAutomaticAccountDailyStats(%s) error: %v", date, err)
		}
	}
	appendMode := func(id, date, mode string, minute int) {
		t.Helper()
		if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
			ID: id, UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
			EventType: AccountEventStatsModeChange, EffectiveDate: date, StatsMode: mode,
			IdempotencyKey: id, CreatedAt: now.Add(time.Duration(minute) * time.Minute),
		}); err != nil {
			t.Fatalf("AppendAccountEvent(%s) error: %v", id, err)
		}
	}
	appendManual := func(id, date string, value int64, minute int) error {
		t.Helper()
		_, err := repo.AppendAccountEvent(ctx, AccountEvent{
			ID: id, UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
			EventType: AccountEventManualObservation, EffectiveDate: date,
			QuotaUsedMicros: int64Pointer(value), RevenueCents: int64Pointer(value), UpstreamCostCents: int64Pointer(value),
			IdempotencyKey: id, CreatedAt: now.Add(time.Duration(minute) * time.Minute),
		})
		return err
	}

	saveAutomatic("auto-day-1", "2026-08-22", 100)
	appendMode("manual-day-2", "2026-08-23", StatsModeManual, 1)
	if err := appendManual("manual-total-150", "2026-08-23", 150, 2); err != nil {
		t.Fatalf("first manual observation: %v", err)
	}
	appendMode("automatic-day-3", "2026-08-24", StatsModeAutomatic, 3)
	saveAutomatic("auto-day-3", "2026-08-24", 50)
	appendMode("manual-day-4", "2026-08-25", StatsModeManual, 4)
	if err := appendManual("manual-total-220", "2026-08-25", 220, 5); err != nil {
		t.Fatalf("second manual observation: %v", err)
	}
	var quotaTotal, revenueTotal, upstreamTotal int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(sum(raw_quota_used_micros),0),COALESCE(sum(revenue_cents),0),COALESCE(sum(upstream_cost_cents),0)
		FROM dashboard_account_daily_stats WHERE account_asset_id=$1
	`, asset.ID).Scan(&quotaTotal, &revenueTotal, &upstreamTotal); err != nil {
		t.Fatalf("sum account daily stats: %v", err)
	}
	if quotaTotal != 220 || revenueTotal != 220 || upstreamTotal != 220 {
		t.Fatalf("lifecycle totals = %d/%d/%d, want 220/220/220", quotaTotal, revenueTotal, upstreamTotal)
	}
	if err := appendManual("manual-regression", "2026-08-26", 219, 6); err == nil {
		t.Fatal("manual cumulative observation below confirmed lifecycle totals was accepted")
	}
}

func TestSaveAutomaticAccountDailyStatsRebuildsLaterManualTotalsAfterHistoricalBackfill(t *testing.T) {
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
	batch := repositoryTestBatch("batch-late-auto", "idem-late-auto", now)
	batch.AccountingMode, batch.StatsMode = AccountingModeAdditive, StatsModeAutomatic
	asset := repositoryTestAsset("asset-late-auto", batch.ID, "late-auto", 1000, now)
	asset.AccountingMode, asset.StatsMode = AccountingModeAdditive, StatsModeAutomatic
	link := AccountLink{
		ID: "link-late-auto", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-late-auto", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: "2026-08-22", CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "manual-mode-late-auto", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventStatsModeChange, EffectiveDate: "2026-08-23", StatsMode: StatsModeManual,
		IdempotencyKey: "manual-mode-late-auto", CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("switch to manual: %v", err)
	}
	if _, err := repo.AppendAccountEvent(ctx, AccountEvent{
		ID: "manual-total-late-auto", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		EventType: AccountEventManualObservation, EffectiveDate: "2026-08-23",
		QuotaUsedMicros: int64Pointer(220), RevenueCents: int64Pointer(220), UpstreamCostCents: int64Pointer(220),
		IdempotencyKey: "manual-total-late-auto", CreatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("append manual cumulative total: %v", err)
	}
	if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{{
		ID: "automatic-backfill", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		BusinessDate: "2026-08-22", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete,
		KeyCostRunID: "run-2026-08-22", RawQuotaUsedMicros: int64Pointer(50), RevenueCents: int64Pointer(50),
		UpstreamCostCents: int64Pointer(50), CreatedAt: now.Add(3 * time.Minute), UpdatedAt: now.Add(3 * time.Minute),
	}}); err != nil {
		t.Fatalf("SaveAutomaticAccountDailyStats(historical backfill) error: %v", err)
	}
	var quotaTotal, revenueTotal, upstreamTotal, manualQuota int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(sum(raw_quota_used_micros),0),COALESCE(sum(revenue_cents),0),
		       COALESCE(sum(upstream_cost_cents),0),
		       COALESCE(sum(raw_quota_used_micros) FILTER (WHERE source='manual'),0)
		FROM dashboard_account_daily_stats WHERE account_asset_id=$1
	`, asset.ID).Scan(&quotaTotal, &revenueTotal, &upstreamTotal, &manualQuota); err != nil {
		t.Fatalf("sum account daily stats: %v", err)
	}
	if quotaTotal != 220 || revenueTotal != 220 || upstreamTotal != 220 || manualQuota != 170 {
		t.Fatalf("totals after historical backfill = %d/%d/%d, manual=%d; want 220/220/220, manual=170",
			quotaTotal, revenueTotal, upstreamTotal, manualQuota)
	}
}

func TestListAutomaticAccountTargetsRevalidatesLiveConnectionEligibility(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts (id text PRIMARY KEY,user_id text NOT NULL)`); err != nil {
		t.Fatalf("create admin_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE real_connections (
			id text PRIMARY KEY,user_id text NOT NULL,workspace_admin_account_id text NOT NULL,
			upstream_site_id text NOT NULL,upstream_key_id text NOT NULL,admin_account_id text NOT NULL,
			own_group_ids jsonb NOT NULL,status text NOT NULL
		)
	`); err != nil {
		t.Fatalf("create real_connections: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO admin_accounts (id,user_id) VALUES ('workspace-1','user-1')`); err != nil {
		t.Fatalf("insert admin account: %v", err)
	}
	now := time.Now().UTC()
	batch := repositoryTestBatch("batch-live-link", "idem-live-link", now)
	batch.StatsMode = StatsModeAutomatic
	asset := repositoryTestAsset("asset-live-link", batch.ID, "live-link", 1000, now)
	asset.StatsMode, asset.CurrentStatus = StatsModeAutomatic, AccountStatusActive
	link := AccountLink{
		ID: "account-link", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID,
		ConnectionID: "connection-1", UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
		ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1", EffectiveFrom: businesstime.Today(), CreatedAt: now,
	}
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, []AccountLink{link}, nil); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO real_connections (id,user_id,workspace_admin_account_id,upstream_site_id,upstream_key_id,admin_account_id,own_group_ids,status)
		VALUES ('connection-1','user-1','workspace-1','site-1','key-1','scope-1','["group-1"]','active')
	`); err != nil {
		t.Fatalf("insert eligible connection: %v", err)
	}
	targets, err := repo.ListAutomaticAccountTargets(ctx, "user-1", "workspace-1", businesstime.Today())
	if err != nil || len(targets) != 1 {
		t.Fatalf("eligible targets=%#v err=%v", targets, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE real_connections SET status='degraded' WHERE id='connection-1'`); err != nil {
		t.Fatalf("degrade connection: %v", err)
	}
	targets, err = repo.ListAutomaticAccountTargets(ctx, "user-1", "workspace-1", businesstime.Today())
	if err != nil || len(targets) != 1 || targets[0].Asset.ID != asset.ID || targets[0].Link.ID != "" {
		t.Fatalf("inactive connection disappeared from expected accounts or remained eligible: targets=%#v err=%v", targets, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE real_connections SET status='active' WHERE id='connection-1';
		INSERT INTO real_connections (id,user_id,workspace_admin_account_id,upstream_site_id,upstream_key_id,admin_account_id,own_group_ids,status)
		VALUES ('connection-2','user-1','workspace-1','site-1','key-1','scope-2','["group-2"]','active')
	`); err != nil {
		t.Fatalf("create key ambiguity: %v", err)
	}
	targets, err = repo.ListAutomaticAccountTargets(ctx, "user-1", "workspace-1", businesstime.Today())
	if err != nil || len(targets) != 1 || targets[0].Asset.ID != asset.ID || targets[0].Link.ID != "" {
		t.Fatalf("ambiguous key disappeared from expected accounts or remained eligible: targets=%#v err=%v", targets, err)
	}
}

func TestAccountLedgerWritesReprojectExistingDailySnapshotWithoutChangingBaseAmounts(t *testing.T) {
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
	now := time.Now().UTC()
	revenue, upstreamCost, netProfit := 100.0, 20.0, 80.0
	date, _ := time.ParseInLocation("2006-01-02", businesstime.Today(), businesstime.Location())
	if err := repo.Upsert(ctx, DailySnapshot{
		ID: "snapshot-ledger-reprojection", UserID: "user-1", AdminAccountID: "workspace-1", Date: date,
		TodayProfit: &revenue, TodayPurchase: &upstreamCost, NetProfit: &netProfit, CreatedAt: now,
		SettlementStatus: SettlementStatusFinal, SnapshotSource: SnapshotSourceDatedQuery,
	}); err != nil {
		t.Fatalf("Upsert(base snapshot): %v", err)
	}
	batch := repositoryTestBatch("batch-ledger-reprojection", "idem-ledger-reprojection", now)
	batch.AccountingMode, batch.PurchaseDate, batch.RecognitionStartDate = AccountingModeAdditive, businesstime.Today(), businesstime.Today()
	asset := repositoryTestAsset("asset-ledger-reprojection", batch.ID, "ledger-reprojection", 1000, now)
	asset.AccountingMode, asset.RecognitionStartDate = AccountingModeAdditive, businesstime.Today()
	if _, err := repo.CreateAccountBatch(ctx, batch, []AccountAsset{asset}, nil, []AdditionalCostRecord{
		repositoryTestDatedCost("cost-ledger-reprojection", batch.ID, asset.ID, businesstime.Today(), 1000, now),
	}); err != nil {
		t.Fatalf("CreateAccountBatch() error: %v", err)
	}
	snapshot, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", businesstime.Today())
	if err != nil {
		t.Fatalf("LatestDashboardSnapshot() error: %v", err)
	}
	if snapshot == nil || snapshot.TodayProfit == nil || *snapshot.TodayProfit != revenue || snapshot.TodayPurchase == nil || *snapshot.TodayPurchase != upstreamCost {
		t.Fatalf("ledger projection changed base amounts: %#v", snapshot)
	}
	if snapshot.AccountPurchaseCost == nil || *snapshot.AccountPurchaseCost != 10 || snapshot.OperatingCost == nil || *snapshot.OperatingCost != 31.6 || snapshot.AdjustedNetProfit == nil || *snapshot.AdjustedNetProfit != 68.4 {
		t.Fatalf("ledger projection did not update derived costs: %#v", snapshot)
	}
}

func accountAssetTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository tests")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	schema := fmt.Sprintf("dashboard_account_asset_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse test database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("connect test schema: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		adminPool.Close()
		t.Fatalf("ping test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}

func repositoryTestBatch(id, idempotencyKey string, now time.Time) AccountBatch {
	return AccountBatch{
		ID: id, UserID: "user-1", AdminAccountID: "workspace-1", IdempotencyKey: idempotencyKey,
		BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseDate: "2026-08-22", Quantity: 2, TotalAmountCents: 1000,
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeImmediate,
		RecognitionStartDate: "2026-08-22", StatsMode: StatsModeManual, CreatedAt: now,
	}
}

func repositoryTestAsset(id, batchID, identifier string, cost int64, now time.Time) AccountAsset {
	return AccountAsset{
		ID: id, UserID: "user-1", AdminAccountID: "workspace-1", BatchID: batchID,
		Identifier: identifier, Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseCostCents: cost, AccountingMode: AccountingModeReplace,
		RecognitionMode: RecognitionModeImmediate, RecognitionStartDate: "2026-08-22",
		StatsMode: StatsModeManual, CurrentStatus: AccountStatusUnactivated, CreatedAt: now, UpdatedAt: now,
	}
}

func repositoryTestCost(id, batchID, assetID string, amount int64, now time.Time) AdditionalCostRecord {
	return repositoryTestDatedCost(id, batchID, assetID, "2026-08-22", amount, now)
}

func repositoryTestDatedCost(id, batchID, assetID, date string, amount int64, now time.Time) AdditionalCostRecord {
	return AdditionalCostRecord{
		ID: id, UserID: "user-1", AdminAccountID: "workspace-1", Type: AdditionalCostAccountPurchase,
		Name: assetID, BusinessDate: date, AmountCents: amount, Amount: float64(amount) / 100,
		OriginalAmount: float64(amount) / 100, SourceID: assetID, BatchID: batchID, AccountAssetID: assetID, CreatedAt: now,
	}
}

func int64Pointer(value int64) *int64 { return &value }

func assertAccountProjectedState(t *testing.T, repo *MetricsRepository, assetID, wantStatus, wantStatsMode string) {
	t.Helper()
	asset, err := repo.GetAccountAsset(context.Background(), "user-1", "workspace-1", assetID)
	if err != nil {
		t.Fatalf("GetAccountAsset(%s): %v", assetID, err)
	}
	if asset.CurrentStatus != wantStatus || asset.StatsMode != wantStatsMode {
		t.Fatalf("projected asset state = status %q stats %q, want %q/%q", asset.CurrentStatus, asset.StatsMode, wantStatus, wantStatsMode)
	}
}

func assertAccountDailyQuota(t *testing.T, pool *pgxpool.Pool, assetID, date string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(raw_quota_used_micros,0) FROM dashboard_account_daily_stats
		WHERE account_asset_id=$1 AND business_date=$2::date
	`, assetID, date).Scan(&got); err != nil {
		t.Fatalf("query account daily quota %s: %v", date, err)
	}
	if got != want {
		t.Fatalf("account daily quota %s = %d, want %d", date, got, want)
	}
}

func assertAccountDailyRecognizedCost(t *testing.T, pool *pgxpool.Pool, assetID, date string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `
		SELECT recognized_cost_cents FROM dashboard_account_daily_stats
		WHERE account_asset_id=$1 AND business_date=$2::date
	`, assetID, date).Scan(&got); err != nil {
		t.Fatalf("read recognized cost for %s: %v", date, err)
	}
	if got != want {
		t.Fatalf("recognized cost for %s = %d, want %d", date, got, want)
	}
}

func assertAccountPurchaseCost(t *testing.T, pool *pgxpool.Pool, assetID string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs WHERE account_asset_id=$1 AND type=$2`, assetID, AdditionalCostAccountPurchase).Scan(&got); err != nil {
		t.Fatalf("sum account purchase costs: %v", err)
	}
	if got != want {
		t.Fatalf("account purchase cost = %d, want %d", got, want)
	}
}

func assertAccountDateCost(t *testing.T, pool *pgxpool.Pool, assetID, date string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs WHERE account_asset_id=$1 AND type=$2 AND business_date=$3::date`, assetID, AdditionalCostAccountPurchase, date).Scan(&got); err != nil {
		t.Fatalf("sum account date costs: %v", err)
	}
	if got != want {
		t.Fatalf("account date cost %s = %d, want %d", date, got, want)
	}
}

func assertAccountRefundCost(t *testing.T, pool *pgxpool.Pool, assetID string, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(sum(amount_cents),0) FROM dashboard_additional_costs WHERE account_asset_id=$1 AND type=$2`, assetID, AdditionalCostAccountRefund).Scan(&got); err != nil {
		t.Fatalf("sum account refund costs: %v", err)
	}
	if got != want {
		t.Fatalf("account refund cost = %d, want %d", got, want)
	}
}

func assertTableCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s row count = %d, want %d", table, got, want)
	}
}

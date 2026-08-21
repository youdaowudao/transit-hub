package dashboard

import (
	"context"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/shared/businesstime"
)

func TestAccountAssetBatchAllocationConservesEveryCent(t *testing.T) {
	tests := []struct {
		name    string
		total   int64
		count   int
		want    []int64
		wantErr bool
	}{
		{name: "divisible", total: 1200, count: 3, want: []int64{400, 400, 400}},
		{name: "positive remainder on final account", total: 1000, count: 3, want: []int64{333, 333, 334}},
		{name: "negative remainder on final adjustment", total: -1000, count: 3, want: []int64{-333, -333, -334}},
		{name: "zero total remains explicit", total: 0, count: 2, want: []int64{0, 0}},
		{name: "empty batch rejected", total: 1000, count: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := allocateCents(tt.total, tt.count)
			if tt.wantErr {
				if err == nil {
					t.Fatal("allocateCents() expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("allocateCents() error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("allocateCents() = %v, want %v", got, tt.want)
			}
			var sum int64
			for _, amount := range got {
				sum += amount
			}
			if sum != tt.total {
				t.Fatalf("allocated sum = %d, want %d", sum, tt.total)
			}
		})
	}
}

type fakeAccountAssetRepository struct {
	createCalls  int
	batch        AccountBatch
	assets       []AccountAsset
	links        []AccountLink
	costs        []AdditionalCostRecord
	events       []AccountEvent
	replacedLink *AccountLink
	linkEvent    AccountEvent
	linkURL      string
	detail       AccountAssetDetail
}

func (f *fakeAccountAssetRepository) CreateAccountBatch(_ context.Context, batch AccountBatch, assets []AccountAsset, links []AccountLink, costs []AdditionalCostRecord) (AccountBatchResult, error) {
	f.createCalls++
	f.batch = batch
	f.assets = append([]AccountAsset(nil), assets...)
	f.links = append([]AccountLink(nil), links...)
	f.costs = append([]AdditionalCostRecord(nil), costs...)
	return AccountBatchResult{Batch: batch, Assets: assets}, nil
}

func TestAccountAssetBatchAppliesPerAccountQuotaLinkAndReferenceOverrides(t *testing.T) {
	connection := my_sites.RealConnection{
		ID: "connection-1", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
		UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", UpstreamGroupName: "供应商组",
		AdminAccountID: "upstream-admin-1", OwnGroupIDs: []string{"group-1"}, OwnGroupNames: []string{"自有组"},
		Status: my_sites.ConnectionStatusActive,
	}
	connectionTwo := connection
	connectionTwo.ID = "connection-2"
	connectionTwo.UpstreamSiteID = "site-2"
	connectionTwo.UpstreamKeyID = "key-2"
	connectionTwo.AdminAccountID = "upstream-admin-2"
	connectionTwo.OwnGroupIDs = []string{"group-2"}
	connectionTwo.OwnGroupNames = []string{"自有组二"}
	repo := &fakeAccountAssetRepository{detail: AccountAssetDetail{Asset: AccountAsset{ID: "asset-1"}}}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, fakeRealConnectionReader{connections: []my_sites.RealConnection{connection, connectionTwo}})

	quota := int64(250_000_000)
	result, err := service.CreateBatch(context.Background(), "user-1", AccountBatchInput{
		IdempotencyKey: "overrides", BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseDate: "2026-08-22", Quantity: 2, TotalAmountCents: 2000,
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeQuota,
		RecognitionStartDate: "2026-08-22", StatsMode: StatsModeAutomatic,
		Accounts: []AccountAssetInput{
			{Identifier: "account-a", QuotaTotalMicros: &quota, ConnectionID: connection.ID, UpstreamReferenceURL: "https://supplier.example/a"},
			{Identifier: "account-b", QuotaTotalMicros: &quota, ConnectionID: connectionTwo.ID, UpstreamReferenceURL: "https://supplier.example/b"},
		},
	})
	if err != nil {
		t.Fatalf("CreateBatch() error: %v", err)
	}
	if result.Assets[0].QuotaTotalMicros == nil || *result.Assets[0].QuotaTotalMicros != quota || result.Assets[0].UpstreamReferenceURL != "https://supplier.example/a" {
		t.Fatalf("first asset overrides = %#v", result.Assets[0])
	}
	if len(repo.links) != 2 {
		t.Fatalf("links = %#v, want two", repo.links)
	}
	link := repo.links[0]
	if link.ConnectionID != connection.ID || link.UpstreamSiteID != "site-1" || link.UpstreamKeyID != "key-1" || link.OwnGroupID != "group-1" || link.EffectiveFrom != "2026-08-23" {
		t.Fatalf("link = %#v", link)
	}

	repo.createCalls = 0
	_, err = service.CreateBatch(context.Background(), "user-1", AccountBatchInput{
		IdempotencyKey: "duplicate-link", BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseDate: "2026-08-22", Quantity: 2, TotalAmountCents: 2000,
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeQuota,
		RecognitionStartDate: "2026-08-22", StatsMode: StatsModeAutomatic,
		Accounts: []AccountAssetInput{{Identifier: "a", QuotaTotalMicros: &quota, ConnectionID: connection.ID}, {Identifier: "b", QuotaTotalMicros: &quota, ConnectionID: connection.ID}},
	})
	if err == nil {
		t.Fatal("CreateBatch() accepted one connection for two accounts")
	}
	if repo.createCalls != 0 {
		t.Fatalf("duplicate connection reached repository %d times", repo.createCalls)
	}
}

func TestAccountAssetBatchRejectsAutomaticAccountsWithoutConnectionsAndQuotaRecognitionWithoutTotals(t *testing.T) {
	repo := &fakeAccountAssetRepository{}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, nil)
	base := AccountBatchInput{
		IdempotencyKey: "required-configuration", BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseDate: "2026-08-22", Quantity: 1, TotalAmountCents: 1000,
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeImmediate,
		RecognitionStartDate: "2026-08-22", StatsMode: StatsModeAutomatic,
		Accounts: []AccountAssetInput{{Identifier: "account-a"}},
	}
	if _, err := service.CreateBatch(context.Background(), "user-1", base); err == nil {
		t.Fatal("CreateBatch() accepted an automatic account without a connection")
	}
	base.StatsMode = StatsModeManual
	base.RecognitionMode = RecognitionModeQuota
	if _, err := service.CreateBatch(context.Background(), "user-1", base); err == nil {
		t.Fatal("CreateBatch() accepted quota recognition without a per-account quota total")
	}
	if repo.createCalls != 0 {
		t.Fatalf("invalid batches reached repository %d times", repo.createCalls)
	}
}

func TestAccountAssetBatchRejectsRecognitionBeforePurchase(t *testing.T) {
	repo := &fakeAccountAssetRepository{}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, nil)
	_, err := service.CreateBatch(context.Background(), "user-1", AccountBatchInput{
		IdempotencyKey: "recognition-before-purchase", BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
		PurchaseDate: "2026-08-22", Quantity: 1, TotalAmountCents: 1000,
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeImmediate,
		RecognitionStartDate: "2026-08-21", StatsMode: StatsModeManual,
		Accounts: []AccountAssetInput{{Identifier: "account-a"}},
	})
	if err == nil {
		t.Fatal("CreateBatch() accepted recognition before purchase")
	}
	if repo.createCalls != 0 {
		t.Fatalf("invalid recognition reached repository %d times", repo.createCalls)
	}
}

func (f *fakeAccountAssetRepository) ListAccountAssets(context.Context, string, string, AccountAssetFilter) (AccountAssetPage, error) {
	return AccountAssetPage{Items: append([]AccountAsset(nil), f.assets...)}, nil
}

func (f *fakeAccountAssetRepository) GetAccountAsset(_ context.Context, _, _, assetID string) (AccountAsset, error) {
	for _, asset := range f.assets {
		if asset.ID == assetID {
			return asset, nil
		}
	}
	return AccountAsset{}, ErrAccountAssetNotFound
}

func (f *fakeAccountAssetRepository) AppendAccountEvent(_ context.Context, event AccountEvent) (AccountEventResult, error) {
	f.events = append(f.events, event)
	return AccountEventResult{Event: event}, nil
}

func (f *fakeAccountAssetRepository) ReplaceAccountLink(_ context.Context, event AccountEvent, link *AccountLink, upstreamReferenceURL string) error {
	f.linkEvent = event
	f.linkURL = upstreamReferenceURL
	if link != nil {
		copy := *link
		f.replacedLink = &copy
	}
	return nil
}

func (f *fakeAccountAssetRepository) GetAccountAssetDetail(context.Context, string, string, string) (AccountAssetDetail, error) {
	return f.detail, nil
}

func (f *fakeAccountAssetRepository) ListAccountCostLedger(context.Context, string, string, AccountCostLedgerFilter) (AccountCostLedgerPage, error) {
	return AccountCostLedgerPage{Items: append([]AdditionalCostRecord(nil), f.costs...)}, nil
}

func TestAccountAssetReadAndEventMethodsUseCurrentWorkspace(t *testing.T) {
	repo := &fakeAccountAssetRepository{assets: []AccountAsset{{ID: "asset-1"}}, detail: AccountAssetDetail{Asset: AccountAsset{ID: "asset-1"}}}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, nil)
	service.newID = func() string { return "event-1" }
	service.now = func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC) }

	items, err := service.ListAssets(context.Background(), "user-1", AccountAssetFilter{Status: AccountStatusActive})
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("ListAssets() = %#v, %v", items, err)
	}
	detail, err := service.GetAssetDetail(context.Background(), "user-1", "asset-1")
	if err != nil || detail.Asset.ID != "asset-1" {
		t.Fatalf("GetAssetDetail() = %#v, %v", detail, err)
	}
	result, err := service.AppendEvent(context.Background(), "user-1", "asset-1", "request-1", AccountEvent{
		EventType: AccountEventStatus, EffectiveDate: "2026-08-22", Status: AccountStatusDead,
	})
	if err != nil {
		t.Fatalf("AppendEvent() error: %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].UserID != "user-1" || repo.events[0].AdminAccountID != "workspace-1" || repo.events[0].AccountAssetID != "asset-1" || repo.events[0].IdempotencyKey != "request-1" || result.Event.ID != "event-1" {
		t.Fatalf("scoped event = %#v", repo.events)
	}
}

func TestAccountAssetReplaceLinkUsesEligibleConnectionAndDefaultsToNextBusinessDay(t *testing.T) {
	connection := my_sites.RealConnection{
		ID: "connection-2", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
		UpstreamSiteID: "site-2", UpstreamKeyID: "key-2", UpstreamGroupName: "供应商组",
		AdminAccountID: "upstream-admin-2", OwnGroupIDs: []string{"group-2"}, OwnGroupNames: []string{"自有组"},
		Status: my_sites.ConnectionStatusActive,
	}
	repo := &fakeAccountAssetRepository{detail: AccountAssetDetail{Asset: AccountAsset{ID: "asset-1"}}}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, fakeRealConnectionReader{connections: []my_sites.RealConnection{connection}})
	ids := []string{"event-link", "link-2", "event-rejected"}
	service.newID = func() string { value := ids[0]; ids = ids[1:]; return value }
	service.now = func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, businesstime.Location()) }

	detail, err := service.ReplaceLink(context.Background(), "user-1", "asset-1", "link-idem", AccountLinkInput{
		ConnectionID: connection.ID, UpstreamReferenceURL: "https://supplier.example/account/2", Note: "换号",
	})
	if err != nil {
		t.Fatalf("ReplaceLink() error: %v", err)
	}
	if detail.Asset.ID != "asset-1" || repo.replacedLink == nil {
		t.Fatalf("ReplaceLink() detail=%#v link=%#v", detail, repo.replacedLink)
	}
	if repo.replacedLink.EffectiveFrom != "2026-08-23" || repo.replacedLink.ConnectionID != connection.ID || repo.replacedLink.UpstreamReferenceURL != "https://supplier.example/account/2" {
		t.Fatalf("replacement link = %#v", repo.replacedLink)
	}
	if repo.linkEvent.EventType != AccountEventLinkChange || repo.linkEvent.AdminAccountID != "workspace-1" || repo.linkEvent.IdempotencyKey != "link-idem" {
		t.Fatalf("link event = %#v", repo.linkEvent)
	}

	if _, err := service.ReplaceLink(context.Background(), "user-1", "asset-1", "unsafe", AccountLinkInput{UpstreamReferenceURL: "https://user:secret@supplier.example/account"}); err == nil {
		t.Fatal("ReplaceLink() accepted URL user info")
	}
	if _, err := service.ReplaceLink(context.Background(), "user-1", "asset-1", "missing", AccountLinkInput{ConnectionID: "missing"}); err == nil {
		t.Fatal("ReplaceLink() accepted an ineligible connection")
	}
}

func TestAccountAssetSameDayReplacementRequiresCompleteManualSplit(t *testing.T) {
	connection := my_sites.RealConnection{
		ID: "connection-split", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
		UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", UpstreamGroupName: "供应商组",
		AdminAccountID: "upstream-admin-1", OwnGroupIDs: []string{"group-1"}, OwnGroupNames: []string{"自有组"},
		Status: my_sites.ConnectionStatusActive,
	}
	repo := &fakeAccountAssetRepository{detail: AccountAssetDetail{Asset: AccountAsset{ID: "asset-new"}}}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, fakeRealConnectionReader{connections: []my_sites.RealConnection{connection}})
	service.now = func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, businesstime.Location()) }
	service.newID = func() string { return "same-day-id" }

	base := AccountLinkInput{ConnectionID: connection.ID, EffectiveFrom: "2026-08-22"}
	if _, err := service.ReplaceLink(context.Background(), "user-1", "asset-new", "same-day-no-split", base); err == nil {
		t.Fatal("ReplaceLink() accepted an explicit same-day transfer without manual split")
	}
	base.ManualSameDaySplit = true
	if _, err := service.ReplaceLink(context.Background(), "user-1", "asset-new", "same-day-missing-values", base); err == nil {
		t.Fatal("ReplaceLink() accepted a manual split without both account values")
	}
	oldQuota, oldRevenue := int64(40_000_000), int64(8000)
	newQuota, newRevenue := int64(60_000_000), int64(12_000)
	base.PreviousQuotaUsedMicros, base.PreviousRevenueCents = &oldQuota, &oldRevenue
	base.ReplacementQuotaUsedMicros, base.ReplacementRevenueCents = &newQuota, &newRevenue
	if _, err := service.ReplaceLink(context.Background(), "user-1", "asset-new", "same-day-complete", base); err != nil {
		t.Fatalf("ReplaceLink(complete manual split) error: %v", err)
	}
	if repo.replacedLink == nil || repo.replacedLink.PreviousQuotaUsedMicros == nil || *repo.replacedLink.PreviousQuotaUsedMicros != oldQuota ||
		repo.replacedLink.ReplacementRevenueCents == nil || *repo.replacedLink.ReplacementRevenueCents != newRevenue {
		t.Fatalf("manual split values were not forwarded: %#v", repo.replacedLink)
	}
}

func TestAccountAssetBatchCreationBuildsOneAtomicBatchWithPerAccountSchedules(t *testing.T) {
	repo := &fakeAccountAssetRepository{}
	service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, nil)
	service.now = func() time.Time { return time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC) }
	sequence := 0
	service.newID = func() string {
		sequence++
		return "id-" + string(rune('0'+sequence))
	}

	result, err := service.CreateBatch(context.Background(), "user-1", AccountBatchInput{
		IdempotencyKey: "create-batch-1",
		BatchName:      "8 月 Claude",
		Platform:       "  Claude  ", Channel: "  渠道 A ", AccountType: "  Team ",
		PurchaseDate: "2026-08-22", PurchaseURL: "https://supplier.example/orders/1",
		DefaultUpstreamReferenceURL: "https://supplier.example/accounts",
		Quantity:                    3, TotalAmountCents: 1000, Identifiers: []string{"claude-a", "claude-b"},
		AccountingMode: AccountingModeReplace, RecognitionMode: RecognitionModeDaily,
		RecognitionStartDate: "2026-08-22", RecognitionDays: 2, StatsMode: StatsModeManual,
	})
	if err != nil {
		t.Fatalf("CreateBatch() error: %v", err)
	}
	if repo.createCalls != 1 {
		t.Fatalf("repository create calls = %d, want 1", repo.createCalls)
	}
	if result.Batch.Platform != "Claude" || result.Batch.Channel != "渠道 A" || result.Batch.AccountType != "Team" {
		t.Fatalf("normalized batch = %#v", result.Batch)
	}
	if got := []int64{result.Assets[0].PurchaseCostCents, result.Assets[1].PurchaseCostCents, result.Assets[2].PurchaseCostCents}; !reflect.DeepEqual(got, []int64{333, 333, 334}) {
		t.Fatalf("asset costs = %v, want [333 333 334]", got)
	}
	if got := []string{result.Assets[0].Identifier, result.Assets[1].Identifier, result.Assets[2].Identifier}; !reflect.DeepEqual(got, []string{"claude-a", "claude-b", "8 月 Claude-003"}) {
		t.Fatalf("identifiers = %v", got)
	}
	if len(repo.costs) != 6 {
		t.Fatalf("cost records = %d, want 6", len(repo.costs))
	}
	var total int64
	for _, record := range repo.costs {
		total += record.AmountCents
		if record.Type != AdditionalCostAccountPurchase || record.BatchID != result.Batch.ID || record.AccountAssetID == "" {
			t.Fatalf("cost record lost its source: %#v", record)
		}
	}
	if total != 1000 {
		t.Fatalf("cost record sum = %d, want 1000", total)
	}
}

func TestAccountAssetBatchRejectsUnsafeSupplierLinksBeforeWriting(t *testing.T) {
	tests := []struct {
		name        string
		purchaseURL string
		upstreamURL string
	}{
		{name: "script protocol", purchaseURL: "javascript:alert(1)"},
		{name: "URL user info", purchaseURL: "https://user:password@supplier.example/order"},
		{name: "token query", purchaseURL: "https://supplier.example/order?token=secret"},
		{name: "password query", upstreamURL: "https://supplier.example/account?password=secret"},
		{name: "authorization query", upstreamURL: "https://supplier.example/account?Authorization=Bearer-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeAccountAssetRepository{}
			service := NewAccountAssetService(repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, nil)
			_, err := service.CreateBatch(context.Background(), "user-1", AccountBatchInput{
				IdempotencyKey: "unsafe-link", BatchName: "batch", Platform: "Claude", Channel: "A", AccountType: "Team",
				PurchaseDate: "2026-08-22", PurchaseURL: tt.purchaseURL, DefaultUpstreamReferenceURL: tt.upstreamURL,
				Quantity: 1, TotalAmountCents: 100, AccountingMode: AccountingModeReplace,
				RecognitionMode: RecognitionModeImmediate, RecognitionStartDate: "2026-08-22", StatsMode: StatsModeManual,
			})
			if err == nil {
				t.Fatal("CreateBatch() accepted an unsafe URL")
			}
			if repo.createCalls != 0 {
				t.Fatalf("repository create calls = %d, want 0", repo.createCalls)
			}
		})
	}
}

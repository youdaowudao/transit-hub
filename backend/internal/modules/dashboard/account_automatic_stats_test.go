package dashboard

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type fakeAutomaticAccountStatsRepository struct {
	targets []AutomaticAccountTarget
	stats   []AccountDailyStat
}

func (f *fakeAutomaticAccountStatsRepository) ListAutomaticAccountTargets(context.Context, string, string, string) ([]AutomaticAccountTarget, error) {
	return append([]AutomaticAccountTarget(nil), f.targets...), nil
}

func (f *fakeAutomaticAccountStatsRepository) SaveAutomaticAccountDailyStats(_ context.Context, stats []AccountDailyStat) error {
	f.stats = append(f.stats, stats...)
	return nil
}

func TestBuildAutomaticAccountDailyStatUsesMatchedKeyAndScopedRevenue(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	target := AutomaticAccountTarget{
		Asset: AccountAsset{ID: "asset-1", AccountingMode: AccountingModeReplace},
		Link:  AccountLink{UpstreamSiteID: "site-1", UpstreamKeyID: "key-1"},
	}
	run := UpstreamKeyCostRun{ID: "run-1", SnapshotRunID: "snapshot-1", SiteID: "site-1", Complete: true}
	item := UpstreamKeyDailyCost{KeyID: "key-1", RawAmountMicros: 50_000_000, AdjustedCostCents: 300}

	stat := buildAutomaticAccountDailyStat(target, run, item, "2026-08-22", 8_000, now)
	if stat.AccountAssetID != "asset-1" || stat.Source != StatsModeAutomatic || stat.Quality != KeyCostQualityComplete || stat.KeyCostRunID != "snapshot-1" {
		t.Fatalf("stat identity = %#v", stat)
	}
	if stat.RawQuotaUsedMicros == nil || *stat.RawQuotaUsedMicros != 50_000_000 || stat.RevenueCents == nil || *stat.RevenueCents != 8_000 || stat.UpstreamCostCents == nil || *stat.UpstreamCostCents != 300 || stat.ReplacementDeductionCents == nil || *stat.ReplacementDeductionCents != 300 {
		t.Fatalf("stat values = %#v", stat)
	}
}

func TestFindAutomaticAccountKeyRejectsIncompleteOrWrongRun(t *testing.T) {
	target := AutomaticAccountTarget{Link: AccountLink{UpstreamSiteID: "site-1", UpstreamKeyID: "key-1"}}
	runs := []UpstreamKeyCostRun{{ID: "run-1", SiteID: "site-1", Complete: false, Items: []UpstreamKeyDailyCost{{KeyID: "key-1"}}}}
	if _, _, ok := findAutomaticAccountKey(target, runs); ok {
		t.Fatal("incomplete key run was accepted")
	}
	runs[0].Complete = true
	runs[0].Items[0].KeyID = "other"
	if _, _, ok := findAutomaticAccountKey(target, runs); ok {
		t.Fatal("wrong key was accepted")
	}
}

func TestSaveAutomaticAccountStatsForRunPersistsCompleteMatchedAccounts(t *testing.T) {
	repository := &fakeAutomaticAccountStatsRepository{targets: []AutomaticAccountTarget{{
		Asset: AccountAsset{ID: "asset-1", AccountingMode: AccountingModeReplace},
		Link:  AccountLink{UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1"},
	}}}
	service := &MetricsService{accountStats: repository, platform: &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{
		"scope-1|group-1": {TotalActualCost: 20},
	}}}
	runs := []UpstreamKeyCostRun{{
		ID: "run-1", SnapshotRunID: "snapshot-1", SiteID: "site-1", Complete: true,
		Items: []UpstreamKeyDailyCost{{KeyID: "key-1", RawAmountMicros: 50_000_000, AdjustedCostCents: 300}},
	}}

	expected, completed, quality, err := service.saveAutomaticAccountStatsForRun(
		context.Background(), "user-1", "workspace-1", "2026-08-22", runs, upstream.Session{},
	)
	if err != nil {
		t.Fatalf("saveAutomaticAccountStatsForRun() error: %v", err)
	}
	if expected != 1 || completed != 1 || quality != KeyCostQualityComplete || len(repository.stats) != 1 {
		t.Fatalf("result expected=%d completed=%d quality=%q stats=%#v", expected, completed, quality, repository.stats)
	}
	if repository.stats[0].RevenueCents == nil || *repository.stats[0].RevenueCents != 2_000 {
		t.Fatalf("scoped revenue = %#v", repository.stats[0].RevenueCents)
	}
}

package dashboard

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func mergedAccountFixture(now time.Time) (upstream.KeyUsageForDateResult, []upstream.SiteCostForDateResult, []AutomaticAccountTarget) {
	amount := 3.0
	date := businesstime.DateAt(now)
	keys := upstream.KeyUsageForDateResult{BusinessDate: date, ExpectedSites: 1, CompletedSites: 1, Sites: []upstream.KeyUsageSiteResult{{
		SiteID: "site", RechargeRate: 1, Complete: true, Status: "ok", StartedAt: now.Add(time.Second), ConsumeDate: date,
		SyncedRawCost: &amount, CollectedRawCost: &amount,
		Items: []upstream.KeyUsageTodayItem{
			{SiteID: "site", KeyID: "merged-main", KeyIDs: []string{"merged-main", "merged-other"}, KeyName: "same", Merged: true, RawAmount: 1, TodayAmount: 1},
			{SiteID: "site", KeyID: "single", KeyIDs: []string{"single"}, KeyName: "single", RawAmount: 2, TodayAmount: 2},
		},
	}}}
	totals := []upstream.SiteCostForDateResult{{SiteID: "site", RechargeRate: 1, RawCost: amount}}
	var targets []AutomaticAccountTarget
	for _, id := range []string{"merged-main", "merged-other", "single"} {
		targets = append(targets, AutomaticAccountTarget{Asset: AccountAsset{ID: id, AccountingMode: AccountingModeReplace}, Link: AccountLink{UpstreamSiteID: "site", UpstreamKeyID: id, ScopeAdminAccountID: "scope", OwnGroupID: id}})
	}
	return keys, totals, targets
}

func TestHomeCostMergedAccountNeverConfirmsEitherMergedID(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	keys, totals, targets := mergedAccountFixture(now)
	runs := buildAccountKeyCostRuns("user", "workspace", "run", keys.BusinessDate, totals, keys, now)
	if len(runs) != 1 || !runs[0].Complete || runs[0].KeyTotalCents != 300 {
		t.Fatalf("merged consumption must reconcile once at site level: %#v", runs)
	}
	for _, target := range targets[:2] {
		if _, _, ok := findAutomaticAccountKey(target, runs); ok {
			t.Errorf("aggregate consumption was accepted as one account's amount for %s", target.Asset.ID)
		}
	}
	if _, item, ok := findAutomaticAccountKey(targets[2], runs); !ok || item.AdjustedCostCents != 200 {
		t.Fatal("independent Key lost its confirmable account amount")
	}
}

func TestHomeCostMergedAccountDayEndReportsIncompleteWithoutZeroOrAllocation(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	keys, totals, targets := mergedAccountFixture(now)
	runs := buildAccountKeyCostRuns("user", "workspace", "run", keys.BusinessDate, totals, keys, now)
	repo := &fakeAutomaticAccountStatsRepository{targets: targets}
	service := &MetricsService{accountStats: repo, platform: &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{"scope|single": {TotalActualCost: 4}}}, now: func() time.Time { return now }}
	expected, completed, quality, err := service.saveAutomaticAccountStatsForRun(context.Background(), "user", "workspace", keys.BusinessDate, runs, upstream.Session{})
	if err != nil || expected != 3 || completed != 1 || quality != KeyCostQualityMissing || len(repo.stats) != 1 {
		t.Fatalf("merged account substate: expected=%d completed=%d quality=%s saved=%d err=%v", expected, completed, quality, len(repo.stats), err)
	}
	stat := repo.stats[0]
	if stat.AccountAssetID != "single" || stat.UpstreamCostCents == nil || *stat.UpstreamCostCents != 200 || stat.ReplacementDeductionCents == nil || *stat.ReplacementDeductionCents != 200 {
		t.Fatal("merged account was assigned a total, allocation, or confirmed zero")
	}
}

func TestHomeCostMergedAccountRefreshDoesNotPublishIncompleteAccounts(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	keys, _, targets := mergedAccountFixture(now)
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: keys}
	store := newFakeSessionStore()
	store.set("user", "workspace", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(store, &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{"scope|single": {TotalActualCost: 4}}}, upstreams, nil, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	repo := &fakeAccountKeyRunRepository{}
	service.keyCostRuns = repo
	service.accountStats = &fakeAutomaticAccountStatsRepository{targets: targets}
	service.now = func() time.Time { return now }
	result, err := service.RefreshAccountStats(context.Background(), "user", keys.BusinessDate, "merged-accounts")
	if err != nil || result.Quality != KeyCostQualityMissing || result.ExpectedAccounts != 3 || result.CompletedAccounts != 1 || repo.published {
		t.Fatalf("merged refresh: quality=%s expected=%d completed=%d published=%t err=%v", result.Quality, result.ExpectedAccounts, result.CompletedAccounts, repo.published, err)
	}
}

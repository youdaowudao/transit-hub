package dashboard

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type fakeAccountStatsUpstreams struct {
	*fakeUpstreamLister
	keyResult upstream.KeyUsageForDateResult
	keyErr    error
	keyCalls  int
}

func (f *fakeAccountStatsUpstreams) KeyUsageForDate(context.Context, string, string, string) (upstream.KeyUsageForDateResult, error) {
	f.keyCalls++
	return f.keyResult, f.keyErr
}

type fakeAccountKeyRunRepository struct {
	runs      []UpstreamKeyCostRun
	published bool
}

func (f *fakeAccountKeyRunRepository) SaveUpstreamKeyCostRuns(_ context.Context, runs []UpstreamKeyCostRun) error {
	f.runs = append(f.runs, runs...)
	return nil
}

func (f *fakeAccountKeyRunRepository) PublishAccountStatsRefresh(_ context.Context, runs []UpstreamKeyCostRun, _ []AccountDailyStat) error {
	f.runs = append(f.runs, runs...)
	f.published = true
	return nil
}

func (f *fakeAccountKeyRunRepository) GetPublishedAccountStatsRefresh(_ context.Context, _, _, _, date string) (AccountStatsRefreshResponse, bool, error) {
	if !f.published {
		return AccountStatsRefreshResponse{}, false, nil
	}
	return AccountStatsRefreshResponse{
		Date: date, SnapshotRunID: f.runs[0].SnapshotRunID,
		ExpectedSites: len(f.runs), CompletedSites: len(f.runs), Quality: KeyCostQualityComplete,
	}, true, nil
}

func TestRefreshAccountStatsPublishesOnlyACompleteCentReconciledRun(t *testing.T) {
	today := businesstime.Today()
	observedDate, err := time.ParseInLocation("2006-01-02", today, businesstime.Location())
	if err != nil {
		t.Fatalf("parse business date: %v", err)
	}
	observed := observedDate.Add(12 * time.Hour)
	upstreams := &fakeAccountStatsUpstreams{
		fakeUpstreamLister: &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{
			SiteID: "site-1", RechargeRate: 2, RawCost: 5, Meta: upstream.CostFetchMeta{ObservedAt: observed},
		}}},
		keyResult: upstream.KeyUsageForDateResult{BusinessDate: today, ExpectedSites: 1, CompletedSites: 1, Sites: []upstream.KeyUsageSiteResult{{
			SiteID: "site-1", Complete: true, Items: []upstream.KeyUsageTodayItem{{SiteID: "site-1", KeyID: "key-1", RawAmount: 5, TodayAmount: 10}},
		}}},
	}
	repository := &fakeAccountKeyRunRepository{}
	service := &MetricsService{
		upstreams: upstreams, accounts: &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}},
		keyUsageForDate: upstreams, keyCostRuns: repository,
	}

	result, err := service.RefreshAccountStats(context.Background(), "user-1", today, "refresh-1")
	if err != nil {
		t.Fatalf("RefreshAccountStats() error: %v", err)
	}
	if result.Quality != KeyCostQualityComplete || result.CompletedSites != 1 || len(repository.runs) != 1 || !repository.runs[0].Complete {
		t.Fatalf("refresh result=%#v runs=%#v", result, repository.runs)
	}
	if repository.runs[0].SnapshotRunID == "refresh-1" || repository.runs[0].SnapshotRunID == "" {
		t.Fatalf("run id must be scoped and opaque: %q", repository.runs[0].SnapshotRunID)
	}
}

func TestRefreshAccountStatsRejectsHistoricalDatesBeforeExternalQueries(t *testing.T) {
	upstreams := &fakeAccountStatsUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}}
	repository := &fakeAccountKeyRunRepository{}
	service := &MetricsService{
		upstreams: upstreams, accounts: &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}},
		keyUsageForDate: upstreams, keyCostRuns: repository,
	}
	yesterday := time.Now().In(businesstime.Location()).AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := service.RefreshAccountStats(context.Background(), "user-1", yesterday, "historical"); err == nil {
		t.Fatal("RefreshAccountStats() accepted a historical date")
	}
	if upstreams.keyCalls != 0 {
		t.Fatalf("historical refresh made %d key usage calls", upstreams.keyCalls)
	}
}

func TestRefreshAccountStatsReturnsPublishedIdempotentResultWithoutRefetching(t *testing.T) {
	upstreams := &fakeAccountStatsUpstreams{
		fakeUpstreamLister: &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{SiteID: "site-1", RechargeRate: 1, RawCost: 1}}},
		keyResult: upstream.KeyUsageForDateResult{ExpectedSites: 1, CompletedSites: 1, Sites: []upstream.KeyUsageSiteResult{{
			SiteID: "site-1", Complete: true, Items: []upstream.KeyUsageTodayItem{{SiteID: "site-1", KeyID: "key-1", TodayAmount: 1}},
		}}},
	}
	repository := &fakeAccountKeyRunRepository{}
	service := &MetricsService{
		upstreams: upstreams, accounts: &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}},
		keyUsageForDate: upstreams, keyCostRuns: repository,
	}
	today := businesstime.Today()
	first, err := service.RefreshAccountStats(context.Background(), "user-1", today, "repeat")
	if err != nil {
		t.Fatalf("first RefreshAccountStats() error: %v", err)
	}
	second, err := service.RefreshAccountStats(context.Background(), "user-1", today, "repeat")
	if err != nil {
		t.Fatalf("second RefreshAccountStats() error: %v", err)
	}
	if upstreams.keyCalls != 1 || first.SnapshotRunID != second.SnapshotRunID {
		t.Fatalf("idempotent refresh calls=%d first=%#v second=%#v", upstreams.keyCalls, first, second)
	}
}

func TestRefreshAccountStatsKeepsMismatchExplicitlyIncomplete(t *testing.T) {
	today := businesstime.Today()
	upstreams := &fakeAccountStatsUpstreams{
		fakeUpstreamLister: &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{
			{SiteID: "site-1", RechargeRate: 1, RawCost: 10.01},
		}},
		keyResult: upstream.KeyUsageForDateResult{BusinessDate: today, ExpectedSites: 1, CompletedSites: 1, Sites: []upstream.KeyUsageSiteResult{{
			SiteID: "site-1", Complete: true, Items: []upstream.KeyUsageTodayItem{{SiteID: "site-1", KeyID: "key-1", TodayAmount: 10}},
		}}},
	}
	repository := &fakeAccountKeyRunRepository{}
	service := &MetricsService{upstreams: upstreams, accounts: &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}, keyUsageForDate: upstreams, keyCostRuns: repository}

	result, err := service.RefreshAccountStats(context.Background(), "user-1", today, "refresh-mismatch")
	if err != nil {
		t.Fatalf("RefreshAccountStats() error: %v", err)
	}
	if result.Quality != KeyCostQualityMismatch || result.CompletedSites != 0 || len(repository.runs) != 0 {
		t.Fatalf("mismatch result=%#v runs=%#v", result, repository.runs)
	}
}

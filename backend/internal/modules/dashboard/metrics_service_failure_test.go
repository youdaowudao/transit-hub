package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// todayCachedMetrics 返回一个带有今日日期标记的有效 Metrics，用于测试成本路径。
func todayCachedMetrics(cost float64) upstream.Metrics {
	now := time.Now()
	return upstream.Metrics{
		TodayConsume:     upstream.MetricValue{Value: ptrFloat64(cost)},
		TodayConsumeDate: businesstime.Today(),
		TodayConsumeAt:   &now,
	}
}

type fakeMetricsRepository struct {
	snapshots            []DailySnapshot
	upsertErr            error
	listRangeSnapshots   []DailySnapshot
	listRangeErr         error
	listRangeBusinessDay string
	balanceFilter        BalanceFilterConfig
	balanceFilterErr     error
	saveBalanceFilterErr error
	latestSiteCosts      []SiteDailyCost
	latestSiteCostsErr   error
	groupMetricCache     []GroupMetricCacheItem
	groupMetricCacheErr  error
	latestSnapshot       *DailySnapshot
	latestSnapshotErr    error
	dailyStatsSnapshots  []DailySnapshot
	dailyStatsCalls      int
	dailyStatsFrom       string
	dailyStatsTo         string
}

type liveMetricsAccountingRepository struct {
	*fakeMetricsRepository
	*fakeAdditionalCostRepository
	components        AccountCostComponents
	componentErr      error
	requestedSnapshot string
}

type fakeAccountSubstateRecoveryRepository struct {
	*fakeMetricsRepository
	*fakeAutomaticAccountStatsRepository
	runID          string
	runs           []UpstreamKeyCostRun
	requestedRunID string
}

func (f *fakeAccountSubstateRecoveryRepository) LatestCompleteAccountKeyCostRuns(context.Context, string, string, string) (string, []UpstreamKeyCostRun, error) {
	return f.runID, append([]UpstreamKeyCostRun(nil), f.runs...), nil
}

func (f *fakeAccountSubstateRecoveryRepository) AccountKeyCostRunsForSnapshot(_ context.Context, _, _, _, snapshotRunID string) ([]UpstreamKeyCostRun, error) {
	f.requestedRunID = snapshotRunID
	if snapshotRunID != f.runID {
		return nil, nil
	}
	return append([]UpstreamKeyCostRun(nil), f.runs...), nil
}

func (r *liveMetricsAccountingRepository) AccountCostComponentsForDate(context.Context, string, string, string) (AccountCostComponents, error) {
	return r.components, r.componentErr
}

func (r *liveMetricsAccountingRepository) AccountCostComponentsForSnapshotRun(_ context.Context, _, _, _, snapshotRunID string) (AccountCostComponents, error) {
	r.requestedSnapshot = snapshotRunID
	return r.components, r.componentErr
}

func (r *liveMetricsAccountingRepository) LatestCompleteAccountKeyCostRuns(context.Context, string, string, string) (string, []UpstreamKeyCostRun, error) {
	if r.components.SnapshotRunID == "" {
		return "", nil, nil
	}
	return r.components.SnapshotRunID, []UpstreamKeyCostRun{{SnapshotRunID: r.components.SnapshotRunID, Complete: true}}, nil
}

func (f *fakeMetricsRepository) Upsert(ctx context.Context, snapshot DailySnapshot) error {
	f.snapshots = append(f.snapshots, snapshot)
	return f.upsertErr
}

func (f *fakeMetricsRepository) ListRange(ctx context.Context, userID, adminAccountID string, days int, businessDate string) ([]DailySnapshot, error) {
	f.listRangeBusinessDay = businessDate
	return f.listRangeSnapshots, f.listRangeErr
}

func (f *fakeMetricsRepository) GetBalanceFilter(ctx context.Context, userID, adminAccountID string) (BalanceFilterConfig, error) {
	return f.balanceFilter, f.balanceFilterErr
}

func (f *fakeMetricsRepository) SaveBalanceFilter(ctx context.Context, config BalanceFilterConfig) error {
	return f.saveBalanceFilterErr
}

func (f *fakeMetricsRepository) UpsertSiteCost(ctx context.Context, cost SiteDailyCost) error {
	return nil
}

func (f *fakeMetricsRepository) ListSiteCosts(ctx context.Context, userID, adminAccountID string, date string) ([]SiteDailyCost, error) {
	return nil, nil
}

func (f *fakeMetricsRepository) ListLatestSiteCosts(ctx context.Context, userID, adminAccountID, date string) ([]SiteDailyCost, error) {
	return f.latestSiteCosts, f.latestSiteCostsErr
}

func (f *fakeMetricsRepository) LatestDashboardSnapshot(ctx context.Context, userID, adminAccountID, date string) (*DailySnapshot, error) {
	return f.latestSnapshot, f.latestSnapshotErr
}

func (f *fakeMetricsRepository) SaveGroupMetricCache(ctx context.Context, userID, adminAccountID string, items []GroupMetricCacheItem) error {
	f.groupMetricCache = append(f.groupMetricCache, items...)
	return f.groupMetricCacheErr
}

func (f *fakeMetricsRepository) ListGroupMetricCache(ctx context.Context, userID, adminAccountID, metricType string) ([]GroupMetricCacheItem, error) {
	items := make([]GroupMetricCacheItem, 0)
	for _, item := range f.groupMetricCache {
		if item.MetricType == metricType {
			items = append(items, item)
		}
	}
	return items, f.groupMetricCacheErr
}

func (f *fakeMetricsRepository) ListDailyStats(ctx context.Context, userID, adminAccountID string, from, to string) ([]DailySnapshot, error) {
	f.dailyStatsCalls++
	f.dailyStatsFrom = from
	f.dailyStatsTo = to
	return f.dailyStatsSnapshots, nil
}

func newLiveMetricsTestService(platform *fakePlatformClient, upstreams *fakeUpstreamLister, metricsRepo *fakeMetricsRepository) *MetricsService {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	accounts := &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}}
	return NewMetricsService(store, platform, upstreams, metricsRepo, accounts)
}

func metricsResponseJSON(t *testing.T, response MetricsResponse) map[string]any {
	t.Helper()
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}
	return decoded
}

func ptrFloat64(value float64) *float64 {
	return &value
}

func parseBusinessDateForTest(t *testing.T, date string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02", date, businesstime.Location())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func snapshotBusinessDates(snapshots []DailySnapshot) []string {
	dates := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		dates = append(dates, snapshot.Date.Format("2006-01-02"))
	}
	return dates
}

func TestLiveMetricsCostFailureStillPersistsRevenue(t *testing.T) {
	errorKey := upstream.ErrorAuth
	staleCost := 10.0
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(
		&fakePlatformClient{usageStats: 30},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusError, ErrorKey: &errorKey, RechargeRate: 2,
			Metrics: upstream.Metrics{TodayConsume: upstream.MetricValue{Value: &staleCost}},
		}}},
		repo,
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error = %v, want partial response", err)
	}
	decoded := metricsResponseJSON(t, response)
	if decoded["todayProfit"] != 30.0 {
		t.Fatalf("todayProfit = %#v, want 30", decoded["todayProfit"])
	}
	// 上游失败且没有同日确认值时，不得把无日期缓存当作今日成本。
	if decoded["todayPurchase"] != nil || decoded["netProfit"] != nil {
		t.Fatalf("cost failure must remain unavailable: todayPurchase=%#v netProfit=%#v", decoded["todayPurchase"], decoded["netProfit"])
	}
	// 新实现：成本质量通过 costQuality 字段暴露，不再写入 metricErrors
	if cq, hasCq := decoded["costQuality"].(map[string]any); !hasCq || cq["complete"] != false {
		t.Fatalf("costQuality.complete should be false, got %#v", decoded["costQuality"])
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayProfit == nil || *repo.snapshots[0].TodayProfit != 30 || repo.snapshots[0].TodayPurchase != nil {
		t.Fatalf("unavailable cost snapshot = %+v, want revenue 30 and unknown cost", repo.snapshots)
	}
}

func TestStartupRecoveryScansUnknownCostQualityModeWithoutDroppingSnapshot(t *testing.T) {
	loc := businesstime.Location()
	yesterday := businesstime.DateAt(time.Now().In(loc).AddDate(0, 0, -1))
	t.Setenv("SETTLEMENT_BASELINE_DATE", yesterday)
	date, err := time.ParseInLocation("2006-01-02", yesterday, loc)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		Date: date, SettlementStatus: SettlementStatusFinal, CostQualityMode: CostQualityModeUnknown,
	}}}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	service := NewMetricsService(store, &fakePlatformClient{}, &fakeUpstreamLister{}, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	if repo.dailyStatsCalls != 1 {
		t.Fatalf("startup recovery ListDailyStats calls = %d, want 1", repo.dailyStatsCalls)
	}
	if repo.dailyStatsSnapshots[0].CostQualityMode != CostQualityModeUnknown {
		t.Fatalf("startup recovery changed cost quality mode = %q", repo.dailyStatsSnapshots[0].CostQualityMode)
	}
}

func TestStartupRecoveryRetriesOnlyMissingAccountSubstateForFinalSnapshot(t *testing.T) {
	loc := businesstime.Location()
	yesterday := businesstime.DateAt(time.Now().In(loc).AddDate(0, 0, -1))
	t.Setenv("SETTLEMENT_BASELINE_DATE", yesterday)
	date := parseBusinessDateForTest(t, yesterday)
	revenue, siteCost := 80.0, 20.0
	base := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		ID: "snapshot-final", UserID: "user-1", AdminAccountID: "account-1", Date: date,
		TodayProfit: &revenue, TodayPurchase: &siteCost, SettlementStatus: SettlementStatusFinal,
		AccountStatsQuality: KeyCostQualityMissing, AccountSnapshotRunID: "account-run-1",
	}}}
	automatic := &fakeAutomaticAccountStatsRepository{targets: []AutomaticAccountTarget{{
		Asset: AccountAsset{ID: "asset-1", AccountingMode: AccountingModeReplace},
		Link:  AccountLink{UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1"},
	}}}
	repository := &fakeAccountSubstateRecoveryRepository{
		fakeMetricsRepository: base, fakeAutomaticAccountStatsRepository: automatic, runID: "account-run-1",
		runs: []UpstreamKeyCostRun{{
			ID: "site-run-1", SnapshotRunID: "account-run-1", SiteID: "site-1", Complete: true,
			Items: []UpstreamKeyDailyCost{{KeyID: "key-1", RawAmountMicros: 10_000_000, AdjustedCostCents: 200}},
		}},
	}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	upstreams := &fakeUpstreamLister{siteCostErr: errors.New("base costs must not be fetched")}
	platform := &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{"scope-1|group-1": {TotalActualCost: 30}}}
	service := NewMetricsService(store, platform, upstreams, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	if len(automatic.stats) != 1 || len(base.snapshots) != 1 {
		t.Fatalf("account retry stats=%#v snapshots=%#v", automatic.stats, base.snapshots)
	}
	updated := base.snapshots[0]
	if updated.TodayProfit == nil || *updated.TodayProfit != revenue || updated.TodayPurchase == nil || *updated.TodayPurchase != siteCost {
		t.Fatalf("account-only retry rewrote base amounts: %#v", updated)
	}
	if updated.AccountStatsQuality != KeyCostQualityComplete || updated.AccountSnapshotRunID != "account-run-1" {
		t.Fatalf("account substate was not completed: %#v", updated)
	}
	if repository.requestedRunID != "account-run-1" {
		t.Fatalf("account retry requested run %q, want frozen account-run-1", repository.requestedRunID)
	}
}

func TestBackfillRetriesOnlyMissingAccountSubstateForProtectedFinalSnapshot(t *testing.T) {
	dateText := time.Now().In(businesstime.Location()).AddDate(0, 0, -1).Format("2006-01-02")
	date := parseBusinessDateForTest(t, dateText)
	revenue, siteCost := 80.0, 20.0
	base := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		ID: "snapshot-final", UserID: "user-1", AdminAccountID: "account-1", Date: date,
		TodayProfit: &revenue, TodayPurchase: &siteCost, SettlementStatus: SettlementStatusFinal,
		SnapshotSource: SnapshotSourceDatedQuery, AccountStatsQuality: KeyCostQualityMissing,
		AccountSnapshotRunID: "account-run-1",
	}}}
	automatic := &fakeAutomaticAccountStatsRepository{targets: []AutomaticAccountTarget{{
		Asset: AccountAsset{ID: "asset-1", AccountingMode: AccountingModeReplace},
		Link:  AccountLink{UpstreamSiteID: "site-1", UpstreamKeyID: "key-1", ScopeAdminAccountID: "scope-1", OwnGroupID: "group-1"},
	}}}
	repository := &fakeAccountSubstateRecoveryRepository{
		fakeMetricsRepository: base, fakeAutomaticAccountStatsRepository: automatic, runID: "account-run-1",
		runs: []UpstreamKeyCostRun{{
			ID: "site-run-1", SnapshotRunID: "account-run-1", SiteID: "site-1", Complete: true,
			Items: []UpstreamKeyDailyCost{{KeyID: "key-1", RawAmountMicros: 10_000_000, AdjustedCostCents: 200}},
		}},
	}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	upstreams := &fakeUpstreamLister{siteCostErr: errors.New("base costs must not be fetched")}
	platform := &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{"scope-1|group-1": {TotalActualCost: 30}}}
	service := NewMetricsService(store, platform, upstreams, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	response, err := service.Backfill(context.Background(), "user-1", BackfillRequest{From: dateText, To: dateText})
	if err != nil {
		t.Fatalf("Backfill() error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Status != "updated" || len(automatic.stats) != 1 || len(base.snapshots) != 1 {
		t.Fatalf("account-only backfill response=%#v stats=%#v snapshots=%#v", response, automatic.stats, base.snapshots)
	}
	updated := base.snapshots[0]
	if updated.TodayProfit == nil || *updated.TodayProfit != revenue || updated.TodayPurchase == nil || *updated.TodayPurchase != siteCost {
		t.Fatalf("account-only backfill rewrote base amounts: %#v", updated)
	}
}

func TestStartupRecoveryRetriesRecentNonFinalSnapshotsWithoutBaseline(t *testing.T) {
	t.Setenv("SETTLEMENT_BASELINE_DATE", "")
	loc := businesstime.Location()
	stuckDate := businesstime.DateAt(time.Now().In(loc).AddDate(0, 0, -2))
	parsedStuckDate, err := time.ParseInLocation("2006-01-02", stuckDate, loc)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		Date: parsedStuckDate, SettlementStatus: SettlementStatusPartialHigh,
	}}}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	upstreams := &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{
		SiteID: "site-1", SiteName: "site one", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5,
		Meta: upstream.CostFetchMeta{Source: "account_level"},
	}}}
	service := NewMetricsService(store, &fakePlatformClient{usageStats: 20}, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	for _, snapshot := range repo.snapshots {
		if snapshot.Date.Format("2006-01-02") == stuckDate {
			return
		}
	}
	t.Fatalf("startup recovery snapshots = %+v, want retry for recent non-final date %s", repo.snapshots, stuckDate)
}

func TestStartupRecoveryWithoutBaselineUsesRecentWindowAndLimitedMissingBackfill(t *testing.T) {
	t.Setenv("SETTLEMENT_BASELINE_DATE", "")
	loc := businesstime.Location()
	yesterdayTime := time.Now().In(loc).AddDate(0, 0, -1)
	yesterday := businesstime.DateAt(yesterdayTime)
	windowStart := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -(startupRecoveryRecentDays - 1)))
	finalDate := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -5))
	nonFinalDate := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -2))

	repo := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{
		{Date: parseBusinessDateForTest(t, finalDate), SettlementStatus: SettlementStatusFinal},
		{Date: parseBusinessDateForTest(t, nonFinalDate), SettlementStatus: SettlementStatusPartialHigh},
	}}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	upstreams := &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{
		SiteID: "site-1", SiteName: "site one", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5,
		Meta: upstream.CostFetchMeta{Source: "account_level"},
	}}}
	service := NewMetricsService(store, &fakePlatformClient{usageStats: 20}, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	if repo.dailyStatsFrom != windowStart || repo.dailyStatsTo != yesterday {
		t.Fatalf("ListDailyStats range = %s..%s, want %s..%s", repo.dailyStatsFrom, repo.dailyStatsTo, windowStart, yesterday)
	}
	wantDates := []string{nonFinalDate, yesterday}
	if gotDates := snapshotBusinessDates(repo.snapshots); !reflect.DeepEqual(gotDates, wantDates) {
		t.Fatalf("startup recovery finalized dates = %v, want %v", gotDates, wantDates)
	}
}

func TestStartupRecoveryExplicitBaselinePreservesRangeGapBackfill(t *testing.T) {
	loc := businesstime.Location()
	yesterdayTime := time.Now().In(loc).AddDate(0, 0, -1)
	yesterday := businesstime.DateAt(yesterdayTime)
	baseline := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -3))
	finalDate := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -2))
	missingDate := businesstime.DateAt(yesterdayTime.AddDate(0, 0, -1))
	t.Setenv("SETTLEMENT_BASELINE_DATE", baseline)

	repo := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{
		{Date: parseBusinessDateForTest(t, finalDate), SettlementStatus: SettlementStatusFinal},
	}}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	upstreams := &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{
		SiteID: "site-1", SiteName: "site one", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5,
		Meta: upstream.CostFetchMeta{Source: "account_level"},
	}}}
	service := NewMetricsService(store, &fakePlatformClient{usageStats: 20}, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	if repo.dailyStatsFrom != baseline || repo.dailyStatsTo != yesterday {
		t.Fatalf("ListDailyStats range = %s..%s, want %s..%s", repo.dailyStatsFrom, repo.dailyStatsTo, baseline, yesterday)
	}
	wantDates := []string{baseline, missingDate, yesterday}
	if gotDates := snapshotBusinessDates(repo.snapshots); !reflect.DeepEqual(gotDates, wantDates) {
		t.Fatalf("startup recovery finalized dates = %v, want %v", gotDates, wantDates)
	}
}

func TestStartupRecoveryRejectsBlankExplicitBaseline(t *testing.T) {
	t.Setenv("SETTLEMENT_BASELINE_DATE", "   ")
	repo := &fakeMetricsRepository{}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	upstreams := &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{
		SiteID: "site-1", SiteName: "site one", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5,
		Meta: upstream.CostFetchMeta{Source: "account_level"},
	}}}
	service := NewMetricsService(store, &fakePlatformClient{usageStats: 20}, upstreams, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	service.startupRecovery(context.Background())

	if repo.dailyStatsCalls != 0 {
		t.Fatalf("blank explicit baseline should be rejected before ListDailyStats, calls = %d", repo.dailyStatsCalls)
	}
	if len(repo.snapshots) != 0 {
		t.Fatalf("blank explicit baseline should not finalize snapshots, got %v", snapshotBusinessDates(repo.snapshots))
	}
}

func TestLiveMetricsUsesCachedCostsAndKeepsPartialSuccess(t *testing.T) {
	failedKey := upstream.ErrorAuth
	successCost := 3.0
	upstreams := &fakeUpstreamLister{cachedSites: []upstream.Response{
		{
			ID: "site-success", Status: upstream.StatusConnected, RechargeRate: 2,
			Metrics: upstream.Metrics{TodayConsume: upstream.MetricValue{Value: &successCost}, TodayConsumeDate: businesstime.Today()},
		},
		{
			ID: "site-failure", Status: upstream.StatusError, ErrorKey: &failedKey, RechargeRate: 2,
			Metrics: upstream.Metrics{},
		},
	}}
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, upstreams, repo)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error = %v, want partial response", err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 6 || response.NetProfit == nil || *response.NetProfit != 24 {
		t.Fatalf("partial cached cost: todayPurchase=%v netProfit=%v, want 6 and 24", response.TodayPurchase, response.NetProfit)
	}
	if response.ConfirmedCost == nil || *response.ConfirmedCost != 6 {
		t.Fatalf("confirmed cost = %v, want 6.00", response.ConfirmedCost)
	}
	if response.NetProfitCeiling == nil || *response.NetProfitCeiling != 24 {
		t.Fatalf("net profit ceiling = %v, want 24.00", response.NetProfitCeiling)
	}
	if response.SettlementStatus != SettlementStatusPartial {
		t.Fatalf("settlement status = %q, want partial", response.SettlementStatus)
	}
	if upstreams.keyUsageCalls != 0 {
		t.Fatalf("LiveMetrics() called active upstream cost query %d time(s)", upstreams.keyUsageCalls)
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayProfit == nil || *repo.snapshots[0].TodayProfit != 30 {
		t.Fatalf("partial cost did not preserve revenue snapshot: %+v", repo.snapshots)
	}
}

func TestLiveMetricsUsesLatestPersistedSiteCostWhenCurrentValueMissing(t *testing.T) {
	failedKey := upstream.ErrorNetwork
	historicalCost := 8.0
	observedAt := time.Now().Add(-4 * time.Hour)
	repo := &fakeMetricsRepository{latestSiteCosts: []SiteDailyCost{{
		SiteID: "site-failure", AdjustedCost: &historicalCost, ObservedAt: &observedAt,
	}}}
	service := newLiveMetricsTestService(
		&fakePlatformClient{usageStats: 30},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			ID: "site-failure", Name: "上游一", Status: upstream.StatusError, ErrorKey: &failedKey, RechargeRate: 2,
		}}},
		repo,
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 8 || response.NetProfit == nil || *response.NetProfit != 22 {
		t.Fatalf("historical fallback amounts: cost=%v net=%v", response.TodayPurchase, response.NetProfit)
	}
	if response.CostQuality == nil || response.CostQuality.Mode != "retained" || response.CostQuality.RetainedSites != 1 || response.CostQuality.FallbackAt == nil || !response.CostQuality.FallbackAt.Equal(observedAt) || !response.CostQuality.Complete {
		t.Fatalf("historical fallback quality = %+v", response.CostQuality)
	}
}

func TestLiveMetricsAllCachedCostsUnavailableReturnsZeroAndError(t *testing.T) {
	errorKey := upstream.ErrorAuth
	upstreams := &fakeUpstreamLister{cachedSites: []upstream.Response{
		{
			ID: "site-failure", Status: upstream.StatusError, ErrorKey: &errorKey, RechargeRate: 2,
			Metrics: upstream.Metrics{},
		},
	}}
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, upstreams, repo)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error = %v, want partial response", err)
	}
	decoded := metricsResponseJSON(t, response)
	// 全部成本不可用：todayPurchase 和 netProfit 为 null
	if decoded["todayPurchase"] != nil || decoded["netProfit"] != nil {
		t.Fatalf("all cached costs unavailable: todayPurchase=%#v netProfit=%#v", decoded["todayPurchase"], decoded["netProfit"])
	}
	// costQuality 标记不完整
	if cq, hasCq := decoded["costQuality"].(map[string]any); !hasCq || cq["complete"] != false {
		t.Fatalf("costQuality.complete should be false, got %#v", decoded["costQuality"])
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayProfit == nil || *repo.snapshots[0].TodayProfit != 30 {
		t.Fatalf("unavailable cost did not preserve revenue snapshot: %+v", repo.snapshots)
	}
}

func TestLiveMetricsDoesNotUseErroredCacheAsFallback(t *testing.T) {
	errorKey := upstream.ErrorNetwork
	metrics := todayCachedMetrics(10)
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(
		&fakePlatformClient{usageStats: 30},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			ID: "site-fallback", Status: upstream.StatusError, ErrorKey: &errorKey, RechargeRate: 2, Metrics: metrics,
		}}},
		repo,
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	if response.TodayPurchase != nil || response.NetProfit != nil {
		t.Fatalf("errored cache must not become confirmed cost: cost=%v net=%v", response.TodayPurchase, response.NetProfit)
	}
	if response.CostQuality == nil || response.CostQuality.Mode != "unavailable" || response.CostQuality.MissingSites != 1 || response.CostQuality.Complete {
		t.Fatalf("unavailable quality = %+v", response.CostQuality)
	}
	if response.SettlementStatus != SettlementStatusPartial {
		t.Fatalf("settlement status = %q, want partial", response.SettlementStatus)
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayPurchase != nil {
		t.Fatalf("unknown-cost snapshot = %+v", repo.snapshots)
	}
}

func TestCachedCostRejectsStaleSameDayValueWithoutConfirmation(t *testing.T) {
	observedAt := time.Now().Add(-3 * time.Hour)
	cost := 10.0
	_, quality := summarizeCachedUpstreamCostsWithQuality([]upstream.Response{{
		Name: "stale-site", Status: upstream.StatusConnected, RechargeRate: 1,
		Metrics: upstream.Metrics{
			TodayConsume:     upstream.MetricValue{Value: &cost},
			TodayConsumeDate: businesstime.Today(),
			TodayConsumeAt:   &observedAt,
		},
	}}, businesstime.Today(), 2*time.Hour)

	if quality.Mode != "unavailable" || quality.CollectedSites != 0 || quality.MissingSites != 1 {
		t.Fatalf("stale same-day quality = %+v", quality)
	}
}

func TestCachedCostRejectsPreviousBusinessDay(t *testing.T) {
	errorKey := upstream.ErrorNetwork
	observedAt := time.Now().Add(-time.Hour)
	cost := 10.0
	total, quality := summarizeCachedUpstreamCostsWithQuality([]upstream.Response{{
		Name: "old-site", Status: upstream.StatusError, ErrorKey: &errorKey, RechargeRate: 1,
		Metrics: upstream.Metrics{
			TodayConsume:     upstream.MetricValue{Value: &cost},
			TodayConsumeDate: "2026-08-07",
			TodayConsumeAt:   &observedAt,
		},
	}}, "2026-08-08", 2*time.Hour)

	if total != 0 || quality.Mode != "unavailable" || quality.CollectedSites != 0 || quality.MissingSites != 1 || len(quality.Failures) != 1 {
		t.Fatalf("previous-day quality = %+v", quality)
	}
}

func TestCachedCostExcludesDisabledSites(t *testing.T) {
	disabled := false
	cost := 99.0
	_, quality := summarizeCachedUpstreamCostsWithQuality([]upstream.Response{{
		Name: "disabled-site", Enabled: &disabled, Status: upstream.StatusError, RechargeRate: 1,
		Metrics: upstream.Metrics{TodayConsume: upstream.MetricValue{Value: &cost}},
	}}, businesstime.Today(), 2*time.Hour)

	if quality.Mode != "exact" || quality.ExpectedSites != 0 || quality.ConfirmedCost != 0 {
		t.Fatalf("disabled site quality = %+v", quality)
	}
}

func TestLiveMetricsRevenueFailureDoesNotPersistSnapshot(t *testing.T) {
	revenueErr := errors.New("admin usage unavailable")
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(
		&fakePlatformClient{usageStatsErr: revenueErr},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusConnected, RechargeRate: 1,
			Metrics: todayCachedMetrics(2.5),
		}}},
		repo,
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error = %v, want partial response", err)
	}
	decoded := metricsResponseJSON(t, response)
	if decoded["todayPurchase"] != 2.5 {
		t.Fatalf("todayPurchase = %#v, want 2.5", decoded["todayPurchase"])
	}
	// 营收失败：todayProfit=nil，netProfit=nil；成本完整时 todayPurchase 有值
	if decoded["todayProfit"] != nil || decoded["netProfit"] != nil {
		t.Fatalf("revenue failure fallback amounts: todayProfit=%#v netProfit=%#v", decoded["todayProfit"], decoded["netProfit"])
	}
	metricErrors, ok := decoded["metricErrors"].(map[string]any)
	if !ok || metricErrors["todayProfit"] != revenueErr.Error() {
		t.Fatalf("metricErrors = %#v, want revenue reason on todayProfit", decoded["metricErrors"])
	}
	if len(repo.snapshots) != 0 {
		t.Fatalf("revenue failure persisted %d snapshot(s): %+v", len(repo.snapshots), repo.snapshots)
	}
}

func TestLiveMetricsRevenueFailureUsesLatestSnapshotWithoutOverwritingIt(t *testing.T) {
	revenueErr := errors.New("admin usage unavailable")
	previousRevenue := 30.0
	repo := &fakeMetricsRepository{latestSnapshot: &DailySnapshot{TodayProfit: &previousRevenue}}
	service := newLiveMetricsTestService(
		&fakePlatformClient{usageStatsErr: revenueErr},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusConnected, RechargeRate: 1, Metrics: todayCachedMetrics(2.5),
		}}},
		repo,
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	if response.TodayProfit == nil || *response.TodayProfit != 30 || response.NetProfit == nil || *response.NetProfit != 27.5 {
		t.Fatalf("snapshot fallback amounts: revenue=%v net=%v", response.TodayProfit, response.NetProfit)
	}
	if len(repo.snapshots) != 0 {
		t.Fatalf("fallback revenue overwrote snapshot: %+v", repo.snapshots)
	}
}

func TestLiveMetricsSuccessPersistsSameDayAmounts(t *testing.T) {
	repo := &fakeMetricsRepository{}
	upstreams := &fakeUpstreamLister{cachedSites: []upstream.Response{{
		Status: upstream.StatusConnected, RechargeRate: 1,
		Metrics: todayCachedMetrics(2.5),
	}}}
	platform := &fakePlatformClient{usageStats: 30}
	service := newLiveMetricsTestService(platform, upstreams, repo)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	decoded := metricsResponseJSON(t, response)
	if decoded["todayProfit"] != 30.0 || decoded["todayPurchase"] != 2.5 || decoded["netProfit"] != 27.5 {
		t.Fatalf("unexpected response: %#v", decoded)
	}
	if _, exists := decoded["metricErrors"]; exists {
		t.Fatalf("successful response contains metricErrors: %#v", decoded["metricErrors"])
	}
	if len(repo.snapshots) != 1 {
		t.Fatalf("persisted snapshots = %d, want 1", len(repo.snapshots))
	}
	if platform.capturedUsageStart != response.Date || platform.capturedUsageEnd != response.Date || upstreams.keyUsageCalls != 0 {
		t.Fatalf("date/query mismatch: response=%q revenue=%q..%q keyUsageCalls=%d", response.Date, platform.capturedUsageStart, platform.capturedUsageEnd, upstreams.keyUsageCalls)
	}
	persisted := repo.snapshots[0]
	if persisted.TodayProfit == nil || *persisted.TodayProfit != 30 ||
		persisted.TodayPurchase == nil || *persisted.TodayPurchase != 2.5 ||
		persisted.NetProfit == nil || *persisted.NetProfit != 27.5 {
		t.Fatalf("unexpected persisted snapshot: profit=%v purchase=%v net=%v",
			persisted.TodayProfit, persisted.TodayPurchase, persisted.NetProfit)
	}
}

func TestLiveMetricsProjectsReplacementAccountCostIntoPrimaryOperatingMetrics(t *testing.T) {
	deduction := int64(3000)
	reconciled := int64(10_000)
	publishedCost := 100.0
	expectedAccounts, completedAccounts := 1, 1
	repository := &liveMetricsAccountingRepository{
		fakeMetricsRepository: &fakeMetricsRepository{latestSnapshot: &DailySnapshot{
			TodayPurchase: &publishedCost, AccountSnapshotRunID: "account-run-live",
			AccountExpectedCount: &expectedAccounts, AccountCompletedCount: &completedAccounts,
			AccountStatsQuality: KeyCostQualityComplete,
			CostExpectedCount:   intPtr(1), CostCollectedCount: intPtr(1), CostFreshCount: intPtr(1),
			CostRetainedCount: intPtr(0), CostMissingCount: intPtr(0), CostQualityMode: "exact",
		}},
		fakeAdditionalCostRepository: &fakeAdditionalCostRepository{
			rate: RechargeFeeRate{Rate: 0},
			items: []AdditionalCostRecord{
				{Type: AdditionalCostAccountPurchase, Amount: 40},
				{Type: AdditionalCostFixed, Amount: 20},
			},
		},
		components: AccountCostComponents{
			AccountPurchaseCostCents:          4000,
			ReplacementDeductionCents:         &deduction,
			RequiresReplacementDeduction:      true,
			ReconciledUpstreamDirectCostCents: &reconciled,
			SnapshotRunID:                     "account-run-live",
		},
	}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(
		store,
		&fakePlatformClient{usageStats: 200},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusConnected, RechargeRate: 1, Metrics: todayCachedMetrics(100),
		}}},
		repository,
		&fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}},
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 100 {
		t.Fatalf("direct cost = %#v, want 100", response.TodayPurchase)
	}
	if response.OperatingCost == nil || *response.OperatingCost != 130 {
		t.Fatalf("operating cost = %#v, want 100 - 30 + 40 + 20 = 130", response.OperatingCost)
	}
	if response.AdjustedNetProfit == nil || *response.AdjustedNetProfit != 70 {
		t.Fatalf("adjusted net profit = %#v, want 70", response.AdjustedNetProfit)
	}
	if response.AdjustedProfitMargin == nil || *response.AdjustedProfitMargin != 35 {
		t.Fatalf("adjusted profit margin = %#v, want 35", response.AdjustedProfitMargin)
	}
	if repository.requestedSnapshot != "account-run-live" {
		t.Fatalf("account cost requested snapshot = %q, want account-run-live", repository.requestedSnapshot)
	}
	if len(repository.snapshots) != 1 || repository.snapshots[0].AccountSnapshotRunID != "account-run-live" ||
		repository.snapshots[0].ReplacementDeduction == nil || *repository.snapshots[0].ReplacementDeduction != 30 {
		t.Fatalf("published live snapshot did not preserve account run binding: %#v", repository.snapshots)
	}
}

func TestLiveMetricsKeepsAtomicallyPublishedAccountRunAndDirectCostTogether(t *testing.T) {
	deduction := int64(3000)
	reconciled := int64(10_000)
	publishedCost := 100.0
	expectedAccounts, completedAccounts := 1, 1
	repository := &liveMetricsAccountingRepository{
		fakeMetricsRepository: &fakeMetricsRepository{latestSnapshot: &DailySnapshot{
			TodayPurchase: &publishedCost, AccountSnapshotRunID: "published-run",
			AccountExpectedCount: &expectedAccounts, AccountCompletedCount: &completedAccounts,
			AccountStatsQuality: KeyCostQualityComplete,
			CostExpectedCount:   intPtr(1), CostCollectedCount: intPtr(1), CostFreshCount: intPtr(1),
			CostRetainedCount: intPtr(0), CostMissingCount: intPtr(0), CostQualityMode: "exact",
		}},
		fakeAdditionalCostRepository: &fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0}},
		components: AccountCostComponents{
			ReplacementDeductionCents: &deduction, RequiresReplacementDeduction: true,
			ReconciledUpstreamDirectCostCents: &reconciled, SnapshotRunID: "published-run",
		},
	}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(
		store,
		&fakePlatformClient{usageStats: 200},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusConnected, RechargeRate: 1, Metrics: todayCachedMetrics(120),
		}}},
		repository,
		&fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}},
	)

	response, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("LiveMetrics() error: %v", err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 100 || response.AccountSnapshotRunID != "published-run" ||
		response.AccountExpectedCount == nil || *response.AccountExpectedCount != 1 ||
		response.AccountCompletedCount == nil || *response.AccountCompletedCount != 1 {
		t.Fatalf("published run/cost group was not preserved: %#v", response)
	}
	if repository.requestedSnapshot != "published-run" {
		t.Fatalf("account components requested run %q, want published-run", repository.requestedSnapshot)
	}
	if len(repository.snapshots) != 1 || repository.snapshots[0].TodayPurchase == nil || *repository.snapshots[0].TodayPurchase != 100 ||
		repository.snapshots[0].AccountSnapshotRunID != "published-run" ||
		repository.snapshots[0].AccountExpectedCount == nil || *repository.snapshots[0].AccountExpectedCount != 1 {
		t.Fatalf("live upsert broke the published group: %#v", repository.snapshots)
	}
}

func TestSnapshotAllRevenueFailureKeepsExistingSnapshotUntouched(t *testing.T) {
	revenueErr := errors.New("admin usage unavailable")
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	store.activeSessions = []ActiveSessionRef{{UserID: "user-1", AdminAccountID: "account-1"}}
	repo := &fakeMetricsRepository{}
	service := NewMetricsService(
		store,
		&fakePlatformClient{usageStatsErr: revenueErr},
		&fakeUpstreamLister{cachedSites: []upstream.Response{{
			Status: upstream.StatusConnected, RechargeRate: 1,
			Metrics: upstream.Metrics{TodayConsume: upstream.MetricValue{Value: ptrFloat64(2.5)}},
		}}},
		repo,
		nil,
	)

	service.snapshotAll(context.Background())

	if len(repo.snapshots) != 0 {
		t.Fatalf("revenue failure overwrote snapshot with %+v", repo.snapshots)
	}
}

func TestTrendsPassesExplicitBusinessDateToRepository(t *testing.T) {
	repo := &fakeMetricsRepository{}
	service := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	if _, err := service.Trends(context.Background(), "user-1", 7); err != nil {
		t.Fatalf("Trends() error: %v", err)
	}
	if repo.listRangeBusinessDay == "" {
		t.Fatal("Trends() did not pass an explicit business date")
	}
}

func TestTrendsPreservesFallbackAmountsAndStatus(t *testing.T) {
	revenue := 30.0
	cost := 20.0
	profit := 10.0
	repo := &fakeMetricsRepository{listRangeSnapshots: []DailySnapshot{{
		Date:             time.Date(2026, 8, 7, 0, 0, 0, 0, businesstime.Location()),
		TodayProfit:      &revenue,
		TodayPurchase:    &cost,
		NetProfit:        &profit,
		SettlementStatus: SettlementStatusFallback,
	}}}
	service := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})

	response, err := service.Trends(context.Background(), "user-1", 7)
	if err != nil {
		t.Fatalf("Trends() error: %v", err)
	}
	if len(response.Points) != 1 || response.Points[0].SettlementStatus != SettlementStatusFallback ||
		response.Points[0].TodayPurchase == nil || *response.Points[0].TodayPurchase != cost ||
		response.Points[0].NetProfit == nil || *response.Points[0].NetProfit != profit {
		t.Fatalf("fallback trend point = %+v", response.Points)
	}
}

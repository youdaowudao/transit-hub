package dashboard

import (
	"context"
	"encoding/json"
	"errors"
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
	return nil, nil
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
	// 上游本轮失败但仍保留最后值：成本和利润继续显示，并标记 fallback。
	if decoded["todayPurchase"] != 20.0 || decoded["netProfit"] != 10.0 {
		t.Fatalf("cost failure fallback amounts: todayPurchase=%#v netProfit=%#v", decoded["todayPurchase"], decoded["netProfit"])
	}
	// 新实现：成本质量通过 costQuality 字段暴露，不再写入 metricErrors
	if cq, hasCq := decoded["costQuality"].(map[string]any); !hasCq || cq["complete"] != false {
		t.Fatalf("costQuality.complete should be false, got %#v", decoded["costQuality"])
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayProfit == nil || *repo.snapshots[0].TodayProfit != 30 || repo.snapshots[0].TodayPurchase == nil || *repo.snapshots[0].TodayPurchase != 20 {
		t.Fatalf("fallback snapshot = %+v, want revenue 30 and cost 20", repo.snapshots)
	}
}

func TestLiveMetricsUsesCachedCostsAndKeepsPartialSuccess(t *testing.T) {
	failedKey := upstream.ErrorAuth
	successCost := 3.0
	upstreams := &fakeUpstreamLister{cachedSites: []upstream.Response{
		{
			ID: "site-success", Status: upstream.StatusConnected, RechargeRate: 2,
			Metrics: upstream.Metrics{TodayConsume: upstream.MetricValue{Value: &successCost}},
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
		SiteID: "site-failure", AdjustedCost: &historicalCost, ObservedAt: observedAt,
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
	if response.CostQuality == nil || response.CostQuality.Mode != "fallback" || response.CostQuality.FallbackSites != 1 || response.CostQuality.FallbackAt == nil || !response.CostQuality.FallbackAt.Equal(observedAt) {
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

func TestLiveMetricsUsesSameDayCachedCostAsFallback(t *testing.T) {
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
	if response.TodayPurchase == nil || *response.TodayPurchase != 20 || response.NetProfit == nil || *response.NetProfit != 10 {
		t.Fatalf("fallback amounts: cost=%v net=%v", response.TodayPurchase, response.NetProfit)
	}
	if response.CostQuality == nil || response.CostQuality.Mode != "fallback" || response.CostQuality.FallbackSites != 1 || response.CostQuality.FallbackAt == nil || response.CostQuality.Complete {
		t.Fatalf("fallback quality = %+v", response.CostQuality)
	}
	if response.SettlementStatus != SettlementStatusFallback {
		t.Fatalf("fallback settlement status = %q, want fallback", response.SettlementStatus)
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].TodayPurchase == nil || *repo.snapshots[0].TodayPurchase != 20 || repo.snapshots[0].SettlementStatus != SettlementStatusFallback {
		t.Fatalf("fallback snapshot = %+v", repo.snapshots)
	}
}

func TestCachedCostUsesStaleSameDayValueAsFallback(t *testing.T) {
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

	if quality.Mode != "fallback" || quality.FallbackSites != 1 || quality.FallbackAt == nil || !quality.FallbackAt.Equal(observedAt) {
		t.Fatalf("stale same-day quality = %+v", quality)
	}
}

func TestCachedCostFallbackAcceptsPreviousBusinessDay(t *testing.T) {
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

	if total != 10 || quality.Mode != "fallback" || quality.CollectedSites != 1 || quality.FallbackSites != 1 || len(quality.Failures) != 0 {
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

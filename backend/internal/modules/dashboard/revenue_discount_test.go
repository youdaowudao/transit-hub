package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
)

type discountAdditionalCostRepository struct {
	fakeAdditionalCostRepository
	rule      *RevenueDiscountRate
	ruleErr   error
	ruleDates []string
	listErr   error
}

func (r *discountAdditionalCostRepository) ListAdditionalCosts(ctx context.Context, userID, adminAccountID, from, to string) ([]AdditionalCostRecord, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.fakeAdditionalCostRepository.ListAdditionalCosts(ctx, userID, adminAccountID, from, to)
}

func (r *discountAdditionalCostRepository) GetRevenueDiscountRate(_ context.Context, _, _, date string) (*RevenueDiscountRate, error) {
	r.ruleDates = append(r.ruleDates, date)
	return r.rule, r.ruleErr
}

func (r *discountAdditionalCostRepository) ListRevenueDiscountRates(context.Context, string, string) ([]RevenueDiscountRate, error) {
	return nil, nil
}

func (r *discountAdditionalCostRepository) SaveRevenueDiscountRate(context.Context, RevenueDiscountRate) error {
	return nil
}

func (r *discountAdditionalCostRepository) DeleteRevenueDiscountRate(context.Context, string, string, string, string) error {
	return ErrRevenueDiscountNotFound
}

// 签名漂移时编译失败，避免假仓储悄悄不再提供折扣规则。
var _ revenueDiscountRateRepository = (*discountAdditionalCostRepository)(nil)

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAdditionalCostSummaryDeductsRevenueDiscountBeforeFee(t *testing.T) {
	revenue := 100.0
	repository := &discountAdditionalCostRepository{
		fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}, items: []AdditionalCostRecord{{Type: AdditionalCostFixed, Amount: 5}}},
		rule:                         &RevenueDiscountRate{EffectiveDate: "2026-11-14", GiftRate: 0.03, NewUserRate: 0.02, ReferralRate: 0.01, TotalRate: 0.06},
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if summary == nil || summary.RevenueDiscount == nil || *summary.RevenueDiscount != 6 || summary.NetRevenue == nil || *summary.NetRevenue != 94 ||
		summary.RevenueDiscountRate == nil || *summary.RevenueDiscountRate != 0.06 || summary.RevenueDiscountRule == nil {
		t.Fatalf("summary = %+v, want discount 6 and net revenue 94", summary)
	}
	if summary.RechargeFee == nil || !approxEqual(*summary.RechargeFee, 1.88) {
		t.Fatalf("fee = %v, want 2%% of net revenue 94 = 1.88", summary.RechargeFee)
	}
	if summary.Total == nil || !approxEqual(*summary.Total, 6.88) {
		t.Fatalf("total = %v, want fixed 5 + fee 1.88 without the revenue discount", summary.Total)
	}
	if len(repository.ruleDates) != 1 || repository.ruleDates[0] != "2026-11-14" {
		t.Fatalf("discount rule looked up for %v, want the summary date", repository.ruleDates)
	}
}

func TestAdditionalCostSummaryWithoutDiscountRuleKeepsOldNumbers(t *testing.T) {
	revenue := 100.0
	repository := &discountAdditionalCostRepository{
		fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}, items: []AdditionalCostRecord{{Type: AdditionalCostFixed, Amount: 5}}},
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if summary == nil || summary.RevenueDiscount != nil || summary.NetRevenue != nil || summary.RevenueDiscountRule != nil {
		t.Fatalf("summary without rule carries discount: %+v", summary)
	}
	if summary.RechargeFee == nil || *summary.RechargeFee != 2 || summary.Total == nil || *summary.Total != 7 {
		t.Fatalf("summary = %+v, want the old 2%% fee on full revenue and total 7", summary)
	}
}

func TestAdditionalCostSummaryDiscountReadFailureHidesCosts(t *testing.T) {
	revenue := 100.0
	repository := &discountAdditionalCostRepository{
		fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}},
		ruleErr:                      errors.New("database down"),
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if summary == nil || summary.Available || summary.Total != nil || summary.RechargeFee != nil || summary.UnavailableReason != revenueDiscountUnavailableReason {
		t.Fatalf("summary = %+v, want unavailable costs with a plain reason", summary)
	}
	operating, profit, margin := projectOperatingCost(ptrF64(20), &revenue, summary, AccountCostComponents{}, nil)
	if operating != nil || profit != nil || margin != nil {
		t.Fatalf("profit shown while discount unknown: %v %v %v", operating, profit, margin)
	}
}

func TestProjectOperatingCostUsesNetRevenueForProfitAndMargin(t *testing.T) {
	revenue := 100.0
	total := 6.88
	net := 94.0
	summary := &AdditionalCostSummary{Available: true, Total: &total, NetRevenue: &net}
	operating, profit, margin := projectOperatingCost(ptrF64(50), &revenue, summary, AccountCostComponents{}, nil)
	if operating == nil || !approxEqual(*operating, 56.88) {
		t.Fatalf("operating = %v, want 56.88 (discount is not a cost)", operating)
	}
	if profit == nil || !approxEqual(*profit, 37.12) {
		t.Fatalf("profit = %v, want net revenue 94 - 56.88", profit)
	}
	if margin == nil || !approxEqual(*margin, 37.12/94*100) {
		t.Fatalf("margin = %v, want profit / net revenue", margin)
	}
	summary.NetRevenue = nil
	_, oldProfit, oldMargin := projectOperatingCost(ptrF64(50), &revenue, summary, AccountCostComponents{}, nil)
	if oldProfit == nil || !approxEqual(*oldProfit, 43.12) || oldMargin == nil || !approxEqual(*oldMargin, 43.12) {
		t.Fatalf("without discount profit/margin = %v/%v, want old 43.12/43.12", oldProfit, oldMargin)
	}
}

func TestApplySnapshotRevenueDiscountUsesStoredAmountOnly(t *testing.T) {
	revenue, discount, rate := 200.0, 12.0, 0.06
	summary := &AdditionalCostSummary{}
	applySnapshotRevenueDiscount(summary, DailySnapshot{TodayProfit: &revenue, RevenueDiscount: &discount, RevenueDiscountRate: &rate})
	if summary.NetRevenue == nil || *summary.NetRevenue != 188 || summary.RevenueDiscount == nil || *summary.RevenueDiscount != 12 || summary.RevenueDiscountRate == nil || *summary.RevenueDiscountRate != 0.06 {
		t.Fatalf("stored discount not applied: %+v", summary)
	}
	applySnapshotRevenueDiscount(summary, DailySnapshot{TodayProfit: &revenue})
	if summary.NetRevenue != nil || summary.RevenueDiscount != nil || summary.RevenueDiscountRate != nil {
		t.Fatalf("snapshot without discount kept stale discount: %+v", summary)
	}
}

func TestNormalizeRevenueDiscountInputRejectsPastDatesAndBadRates(t *testing.T) {
	today, tomorrow := "2026-11-14", "2026-11-15"
	valid := RevenueDiscountRateInput{EffectiveDate: " 2026-11-15 ", GiftRate: 0.03, NewUserRate: 0.02, ReferralRate: 0.01}
	got, err := normalizeRevenueDiscountInput(valid, today)
	if err != nil || got.EffectiveDate != tomorrow {
		t.Fatalf("valid input = %+v err=%v", got, err)
	}
	cases := []struct {
		name  string
		input RevenueDiscountRateInput
		want  error
	}{
		{"past date", RevenueDiscountRateInput{EffectiveDate: "2026-11-13"}, ErrRevenueDiscountPastDate},
		{"today", RevenueDiscountRateInput{EffectiveDate: today}, ErrRevenueDiscountPastDate},
		{"bad date", RevenueDiscountRateInput{EffectiveDate: "2026/11/14"}, ErrRevenueDiscountInvalidDate},
		{"negative", RevenueDiscountRateInput{EffectiveDate: tomorrow, GiftRate: -0.01}, ErrRevenueDiscountInvalidRate},
		{"over 100%", RevenueDiscountRateInput{EffectiveDate: tomorrow, ReferralRate: 1.01}, ErrRevenueDiscountInvalidRate},
		{"NaN", RevenueDiscountRateInput{EffectiveDate: tomorrow, NewUserRate: math.NaN()}, ErrRevenueDiscountInvalidRate},
		{"total 100%", RevenueDiscountRateInput{EffectiveDate: tomorrow, GiftRate: 0.5, NewUserRate: 0.3, ReferralRate: 0.2}, ErrRevenueDiscountInvalidRate},
	}
	for _, tc := range cases {
		if _, err := normalizeRevenueDiscountInput(tc.input, today); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestUpsertSnapshotStoresRevenueDiscount(t *testing.T) {
	repository := &fakeMetricsRepository{}
	service := &MetricsService{metricsRepo: repository}
	revenue, discount, rate, net := 100.0, 6.0, 0.06, 94.0
	service.upsertSnapshot(context.Background(), "user-1", "workspace-1", "2026-11-14", MetricsResponse{
		TodayProfit:     &revenue,
		AdditionalCosts: &AdditionalCostSummary{Available: true, RevenueDiscount: &discount, RevenueDiscountRate: &rate, NetRevenue: &net},
	})
	if len(repository.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(repository.snapshots))
	}
	snapshot := repository.snapshots[0]
	if snapshot.RevenueDiscount == nil || *snapshot.RevenueDiscount != 6 || snapshot.RevenueDiscountRate == nil || *snapshot.RevenueDiscountRate != 0.06 || *snapshot.TodayProfit != 100 {
		t.Fatalf("snapshot = %+v, want stored discount 6 at 6%% and main-site revenue 100", snapshot)
	}
}

// 折扣率读取失败时折扣未知：实时刷新、日结、账号补结都不写快照，保留已存的消费与折扣。
func TestRevenueDiscountReadFailureSkipsEverySnapshotWrite(t *testing.T) {
	failing := func() *discountAdditionalCostRepository {
		return &discountAdditionalCostRepository{
			fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}},
			ruleErr:                      errors.New("database down"),
		}
	}
	revenue := 100.0

	// 实时刷新：汇总带失败标记，upsertSnapshot 跳过。
	live := &fakeMetricsRepository{}
	liveService := &MetricsService{metricsRepo: live, additionalCosts: failing()}
	summary := liveService.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if !revenueDiscountReadFailed(summary) {
		t.Fatalf("summary = %+v, want the discount failure marked", summary)
	}
	liveService.upsertSnapshot(context.Background(), "user-1", "workspace-1", "2026-11-14", MetricsResponse{TodayProfit: &revenue, AdditionalCosts: summary})
	if len(live.snapshots) != 0 {
		t.Fatalf("live snapshot written with unknown discount: %+v", live.snapshots)
	}
	// 反向：确认没有规则时照常写入，折扣为空。
	liveService.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}}
	noRule := liveService.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	liveService.upsertSnapshot(context.Background(), "user-1", "workspace-1", "2026-11-14", MetricsResponse{TodayProfit: &revenue, AdditionalCosts: noRule})
	if len(live.snapshots) != 1 || live.snapshots[0].RevenueDiscount != nil {
		t.Fatalf("no-rule snapshot = %+v, want one write without discount", live.snapshots)
	}

	// 日结：返回错误等待重试，不写快照。
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	finalizeRepo := &fakeMetricsRepository{}
	finalizeService := NewMetricsService(store, &fakePlatformClient{usageStats: 20},
		&fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{SiteID: "site-1", SiteName: "test", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5, Meta: upstream.CostFetchMeta{Source: "account_level"}}}},
		finalizeRepo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	finalizeService.additionalCosts = failing()
	err := finalizeService.finalizeBusinessDate(context.Background(), ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}, "2026-08-01", SnapshotSourceDatedQuery)
	if !errors.Is(err, errRevenueDiscountUnavailable) || len(finalizeRepo.snapshots) != 0 {
		t.Fatalf("finalize err=%v snapshots=%d, want discount error and no snapshot", err, len(finalizeRepo.snapshots))
	}

	// 账号补结：不覆盖已结算快照。
	backfillRepo := &fakeMetricsRepository{}
	backfillService := &MetricsService{metricsRepo: backfillRepo, additionalCosts: failing()}
	date, _ := time.ParseInLocation("2006-01-02", "2026-11-13", businesstime.Location())
	discount := 6.0
	settled := DailySnapshot{ID: "settled", UserID: "user-1", AdminAccountID: "workspace-1", Date: date, TodayProfit: &revenue, RevenueDiscount: &discount, SettlementStatus: SettlementStatusFinal}
	if err := backfillService.finalizeAutomaticAccountSubstate(context.Background(), settled, "run-1", "2026-11-13", nil, upstream.Session{}); !errors.Is(err, errRevenueDiscountUnavailable) || len(backfillRepo.snapshots) != 0 {
		t.Fatalf("account backfill err=%v snapshots=%d, want discount error and no overwrite", err, len(backfillRepo.snapshots))
	}
}

// 每日明细直接带出快照已存折扣，即使当天费用不可用（与趋势同一口径）。
func TestDailyStatsCarryStoredDiscountWithoutCostSummary(t *testing.T) {
	date, _ := time.ParseInLocation("2006-01-02", "2026-11-12", businesstime.Location())
	revenue, discount, rate := 100.0, 6.0, 0.06
	repository := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		Date: date, TodayProfit: &revenue, RevenueDiscount: &discount, RevenueDiscountRate: &rate, SettlementStatus: SettlementStatusFinal,
	}}}
	service := NewMetricsService(nil, nil, nil, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, businesstime.Location()) }
	items, err := service.DailyStats(context.Background(), "user-1", "2026-11-12", "2026-11-12", 1, 31, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	if items[0].AdditionalCosts != nil || items[0].RevenueDiscount == nil || *items[0].RevenueDiscount != 6 || items[0].RevenueDiscountRate == nil || *items[0].RevenueDiscountRate != 0.06 {
		t.Fatalf("item = %+v, want stored discount 6 without a cost summary", items[0])
	}
}

// 费用列表读取失败时也先确定折扣：能读到规则就照扣，规则也读不到就标记未知。
func TestAdditionalCostSummaryListFailureStillDeterminesDiscount(t *testing.T) {
	revenue := 100.0
	repository := &discountAdditionalCostRepository{
		rule:    &RevenueDiscountRate{EffectiveDate: "2026-11-14", TotalRate: 0.06},
		listErr: errors.New("list down"),
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if summary.Available || summary.RevenueDiscount == nil || *summary.RevenueDiscount != 6 || revenueDiscountReadFailed(summary) {
		t.Fatalf("summary = %+v, want costs unavailable but discount 6 known", summary)
	}
	repository.ruleErr = errors.New("rule down")
	summary = service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{})
	if !revenueDiscountReadFailed(summary) || summary.RevenueDiscount != nil {
		t.Fatalf("summary = %+v, want discount marked unknown", summary)
	}
	live := &fakeMetricsRepository{}
	(&MetricsService{metricsRepo: live}).upsertSnapshot(context.Background(), "user-1", "workspace-1", "2026-11-14", MetricsResponse{TodayProfit: &revenue, AdditionalCosts: summary})
	if len(live.snapshots) != 0 {
		t.Fatalf("snapshot written while discount unknown after list failure: %+v", live.snapshots)
	}
}

// 折扣未知时首页营收显示不可用并给出白话原因，不按主站消费原值显示，也不写快照。
func TestLiveMetricsShowsRevenueUnavailableWhenDiscountUnknown(t *testing.T) {
	repository := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 20}, &fakeUpstreamLister{}, repository)
	service.additionalCosts = &discountAdditionalCostRepository{ruleErr: errors.New("rule down")}
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.TodayProfit != nil || result.MetricErrors["todayProfit"] != revenueDiscountUnavailableReason || result.AdjustedNetProfit != nil {
		t.Fatalf("result revenue=%v errors=%v profit=%v, want unavailable revenue with the plain reason", result.TodayProfit, result.MetricErrors, result.AdjustedNetProfit)
	}
	if len(repository.snapshots) != 0 {
		t.Fatalf("snapshot written while discount unknown: %+v", repository.snapshots)
	}
	// 反向：没有规则时营收照常显示主站消费并写快照。
	service.additionalCosts = &discountAdditionalCostRepository{}
	result, err = service.LiveMetrics(context.Background(), "user-1")
	if err != nil || result.TodayProfit == nil || *result.TodayProfit != 20 || result.MetricErrors["todayProfit"] != "" || len(repository.snapshots) != 1 {
		t.Fatalf("no-rule result revenue=%v errors=%v snapshots=%d err=%v", result.TodayProfit, result.MetricErrors, len(repository.snapshots), err)
	}
}

// 每日明细的今天一行：折扣未知时营收显示不可用，不按主站消费原值。
func TestDailyStatsTodayRowHidesRevenueWhenDiscountUnknown(t *testing.T) {
	date, _ := time.ParseInLocation("2006-01-02", "2026-11-14", businesstime.Location())
	revenue := 100.0
	repository := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{
		Date: date, TodayProfit: &revenue, SettlementStatus: SettlementStatusProvisional, SnapshotSource: SnapshotSourceLiveCache,
	}}}
	service := NewMetricsService(nil, nil, &fakeUpstreamLister{}, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, businesstime.Location()) }
	service.additionalCosts = &discountAdditionalCostRepository{ruleErr: errors.New("rule down")}
	items, err := service.DailyStats(context.Background(), "user-1", "2026-11-14", "2026-11-14", 1, 31, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	if items[0].TodayProfit != nil || items[0].RevenueDiscount != nil || items[0].AdditionalCosts == nil || items[0].AdditionalCosts.UnavailableReason != revenueDiscountUnavailableReason {
		t.Fatalf("today row = %+v, want revenue hidden with the discount reason", items[0])
	}
	// 反向：没有规则时今天一行照常显示主站消费。
	service.additionalCosts = &discountAdditionalCostRepository{}
	items, err = service.DailyStats(context.Background(), "user-1", "2026-11-14", "2026-11-14", 1, 31, false)
	if err != nil || len(items) != 1 || items[0].TodayProfit == nil || *items[0].TodayProfit != 100 {
		t.Fatalf("no-rule today row = %+v err=%v", items, err)
	}
}

func TestTrendsCarryStoredRevenueDiscount(t *testing.T) {
	date, _ := time.ParseInLocation("2006-01-02", "2026-11-13", businesstime.Location())
	revenue, discount := 100.0, 6.0
	repository := &fakeMetricsRepository{listRangeSnapshots: []DailySnapshot{
		{Date: date, TodayProfit: &revenue, RevenueDiscount: &discount, SettlementStatus: SettlementStatusFinal},
		{Date: date.AddDate(0, 0, -1), TodayProfit: &revenue, SettlementStatus: SettlementStatusFinal},
	}}
	service := NewMetricsService(nil, nil, nil, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	trends, err := service.Trends(context.Background(), "user-1", 7)
	if err != nil || len(trends.Points) != 2 {
		t.Fatalf("trends = %+v err=%v", trends, err)
	}
	if trends.Points[0].RevenueDiscount == nil || *trends.Points[0].RevenueDiscount != 6 || trends.Points[1].RevenueDiscount != nil {
		t.Fatalf("trend discounts = %v / %v, want 6 and none", trends.Points[0].RevenueDiscount, trends.Points[1].RevenueDiscount)
	}
}

func TestPromotionCostCannotBeCreatedButExistingCanBeEdited(t *testing.T) {
	accounts := &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}}
	repository := &fakeAdditionalCostRepository{}
	service := NewMetricsService(nil, nil, nil, nil, accounts)
	service.additionalCosts = repository
	input := AdditionalCostInput{Type: AdditionalCostPromotion, Name: "活动", BusinessDate: "2026-11-14", Amount: 100, UsageRate: 0.8, Days: 1}
	if _, err := service.CreateAdditionalCost(context.Background(), "user-1", input); !errors.Is(err, ErrAdditionalCostPromotionRetired) {
		t.Fatalf("create promotion err = %v, want retired", err)
	}
	fixed := input
	fixed.Type = AdditionalCostFixed
	if _, err := service.CreateAdditionalCost(context.Background(), "user-1", fixed); err != nil {
		t.Fatalf("create fixed err = %v, want ok", err)
	}

	repository.items = []AdditionalCostRecord{{ID: "old-0", SourceID: "old", Type: AdditionalCostPromotion}}
	if _, err := service.UpdateAdditionalCost(context.Background(), "user-1", "old", input); err != nil || repository.updatedSource != "old" {
		t.Fatalf("edit existing promotion err=%v source=%q, want allowed", err, repository.updatedSource)
	}
	repository.updatedSource = ""
	repository.items = []AdditionalCostRecord{{ID: "fixed-0", SourceID: "fixed", Type: AdditionalCostFixed}}
	if _, err := service.UpdateAdditionalCost(context.Background(), "user-1", "fixed", input); !errors.Is(err, ErrAdditionalCostPromotionRetired) || repository.updatedSource != "" {
		t.Fatalf("switch fixed to promotion err=%v source=%q, want rejected before writing", err, repository.updatedSource)
	}

	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, service)
	request := httptest.NewRequest(http.MethodPost, "/api/dashboard/additional-costs", strings.NewReader(`{"type":"promotion","name":"活动","businessDate":"2026-11-14","amount":100,"usageRate":0.8,"days":1}`))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-1"))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "promotionRetired") {
		t.Fatalf("POST promotion = %d %s, want 400 promotionRetired", recorder.Code, recorder.Body.String())
	}
}

func discountRequest(mux *http.ServeMux, method, path, body, user string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		request = request.WithContext(authctx.WithUserID(request.Context(), user))
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestRevenueDiscountRoutesRequireLogin(t *testing.T) {
	service := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	service.additionalCosts = &discountAdditionalCostRepository{}
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, service)
	for _, call := range [][2]string{
		{http.MethodGet, "/api/dashboard/revenue-discount-rates"},
		{http.MethodPut, "/api/dashboard/revenue-discount-rate"},
		{http.MethodDelete, "/api/dashboard/revenue-discount-rates/rate-1"},
		{http.MethodDelete, "/api/dashboard/recharge-fee-rates/rate-1"},
	} {
		if w := discountRequest(mux, call[0], call[1], "{}", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without login = %d, want 401", call[0], call[1], w.Code)
		}
	}
}

func TestRevenueDiscountPostgresRulesSnapshotsAndReprojection(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('workspace-1','user-1'),('workspace-2','user-2')`); err != nil {
		t.Fatal(err)
	}
	service := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1", "user-2": "workspace-2"}})
	service.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, businesstime.Location()) }
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, service)

	// 规则：早于今天被拒；同日再次保存覆盖；工作区隔离。
	for _, day := range []string{"2026-11-13", "2026-11-14"} {
		if w := discountRequest(mux, http.MethodPut, "/api/dashboard/revenue-discount-rate", `{"effectiveDate":"`+day+`","giftRate":0.03,"newUserRate":0.02,"referralRate":0.01}`, "user-1"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "pastDate") {
			t.Fatalf("PUT for %s = %d %s, want 400 pastDate (earliest is tomorrow)", day, w.Code, w.Body.String())
		}
	}
	if w := discountRequest(mux, http.MethodPut, "/api/dashboard/revenue-discount-rate", `{"effectiveDate":"2026-11-15","giftRate":0.01,"newUserRate":0.01,"referralRate":0.01}`, "user-1"); w.Code != http.StatusOK {
		t.Fatalf("first PUT = %d %s", w.Code, w.Body.String())
	}
	w := discountRequest(mux, http.MethodPut, "/api/dashboard/revenue-discount-rate", `{"effectiveDate":"2026-11-15","giftRate":0.03,"newUserRate":0.02,"referralRate":0.01}`, "user-1")
	if w.Code != http.StatusOK {
		t.Fatalf("overwrite PUT = %d %s", w.Code, w.Body.String())
	}
	var saved RevenueDiscountRate
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.TotalRate != 0.06 {
		t.Fatalf("saved = %+v err=%v, want total 0.06", saved, err)
	}
	var listed struct{ Items []RevenueDiscountRate }
	if err := json.Unmarshal(discountRequest(mux, http.MethodGet, "/api/dashboard/revenue-discount-rates", "", "user-1").Body.Bytes(), &listed); err != nil ||
		len(listed.Items) != 1 || listed.Items[0].GiftRate != 0.03 || listed.Items[0].NewUserRate != 0.02 || listed.Items[0].ReferralRate != 0.01 || listed.Items[0].TotalRate != 0.06 {
		t.Fatalf("listed = %+v err=%v, want one overwritten rule", listed, err)
	}
	var otherList struct{ Items []RevenueDiscountRate }
	if err := json.Unmarshal(discountRequest(mux, http.MethodGet, "/api/dashboard/revenue-discount-rates", "", "user-2").Body.Bytes(), &otherList); err != nil || otherList.Items == nil || len(otherList.Items) != 0 {
		t.Fatalf("other workspace list = %+v err=%v, want empty array", otherList, err)
	}

	// 生效日按规则扣折扣；生效日之前（含今天）没有折扣。
	revenue := 100.0
	effective := service.additionalCostSummary(ctx, "user-1", "workspace-1", "2026-11-15", &revenue, revenueDiscountSource{})
	if effective.RevenueDiscount == nil || *effective.RevenueDiscount != 6 || effective.RechargeFee == nil || !approxEqual(*effective.RechargeFee, 1.5) {
		t.Fatalf("effective-day summary = %+v, want discount 6 and default 1.6%% fee on 94", effective)
	}
	if before := service.additionalCostSummary(ctx, "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{}); before.RevenueDiscount != nil {
		t.Fatalf("today (before the rule) got discount %+v", before)
	}

	// 快照两列读写；修改手工成本触发重算时沿用已存折扣，不查规则。
	upsert := func(day string, discount *float64, rate *float64) {
		t.Helper()
		date, _ := time.ParseInLocation("2006-01-02", day, businesstime.Location())
		value, direct, feeRate := 100.0, 20.0, 0.02
		if err := repo.Upsert(ctx, DailySnapshot{ID: day, UserID: "user-1", AdminAccountID: "workspace-1", Date: date,
			TodayProfit: &value, TodayPurchase: &direct, RechargeFeeRate: &feeRate, RevenueDiscount: discount, RevenueDiscountRate: rate,
			SettlementStatus: SettlementStatusFinal, SnapshotSource: SnapshotSourceDatedQuery, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	discount, rate := 6.0, 0.06
	upsert("2026-11-14", &discount, &rate)
	upsert("2026-11-10", nil, nil)
	// 直接写一条覆盖 11-10 的旧规则，证明重算不会给已结算日期补扣。
	if _, err := pool.Exec(ctx, `INSERT INTO dashboard_revenue_discount_rates(id,user_id,admin_account_id,effective_date,gift_rate,new_user_rate,referral_rate) VALUES('legacy','user-1','workspace-1','2026-11-01',0.5,0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-11-14", "2026-11-10"} {
		records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{Type: AdditionalCostFixed, Name: "服务器", Amount: 10, Days: 1, BusinessDate: day}, "fixed-"+day)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.InsertAdditionalCosts(ctx, records); err != nil {
			t.Fatal(err)
		}
	}
	snapshots, err := repo.ListDailyStats(ctx, "user-1", "workspace-1", "2026-11-10", "2026-11-14")
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots = %+v err=%v", snapshots, err)
	}
	withDiscount, withoutDiscount := snapshots[1], snapshots[0]
	if withDiscount.RevenueDiscount == nil || *withDiscount.RevenueDiscount != 6 || withDiscount.RevenueDiscountRate == nil || *withDiscount.RevenueDiscountRate != 0.06 ||
		withDiscount.RechargeFee == nil || !approxEqual(*withDiscount.RechargeFee, 1.88) ||
		withDiscount.OperatingCost == nil || !approxEqual(*withDiscount.OperatingCost, 31.88) ||
		withDiscount.AdjustedNetProfit == nil || !approxEqual(*withDiscount.AdjustedNetProfit, 62.12) {
		t.Fatalf("reprojected discounted day = discount %v fee %v operating %v profit %v, want 6 / 1.88 / 31.88 / 62.12",
			withDiscount.RevenueDiscount, withDiscount.RechargeFee, withDiscount.OperatingCost, withDiscount.AdjustedNetProfit)
	}
	if withoutDiscount.RevenueDiscount != nil || withoutDiscount.RechargeFee == nil || *withoutDiscount.RechargeFee != 2 ||
		withoutDiscount.AdjustedNetProfit == nil || !approxEqual(*withoutDiscount.AdjustedNetProfit, 68) {
		t.Fatalf("settled day without discount changed: discount %v fee %v profit %v, want none / 2 / 68",
			withoutDiscount.RevenueDiscount, withoutDiscount.RechargeFee, withoutDiscount.AdjustedNetProfit)
	}
	ranged, err := repo.ListRange(ctx, "user-1", "workspace-1", 7, "2026-11-15")
	if err != nil || len(ranged) != 2 || ranged[1].RevenueDiscount == nil || *ranged[1].RevenueDiscount != 6 {
		t.Fatalf("ListRange = %+v err=%v, want stored discount on 11-14", ranged, err)
	}
	if latest, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", "2026-11-14"); err != nil || latest == nil || latest.RevenueDiscount == nil || *latest.RevenueDiscount != 6 {
		t.Fatalf("LatestDashboardSnapshot = %+v err=%v", latest, err)
	}

	// 删除：本工作区可删，重复删除或其他工作区返回 404。
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/revenue-discount-rates/"+saved.ID, "", "user-2"); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace DELETE = %d, want 404", w.Code)
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/revenue-discount-rates/"+saved.ID, "", "user-1"); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d %s, want 204", w.Code, w.Body.String())
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/revenue-discount-rates/"+saved.ID, "", "user-1"); w.Code != http.StatusNotFound {
		t.Fatalf("repeat DELETE = %d, want 404", w.Code)
	}
	if after := service.additionalCostSummary(ctx, "user-1", "workspace-1", "2026-11-15", &revenue, revenueDiscountSource{}); after.RevenueDiscount == nil || *after.RevenueDiscount != 50 {
		t.Fatalf("after delete summary = %+v, want the earlier rule (50%%) to apply again", after)
	}
	// 已经生效的规则（过去 11-01、今天 11-14）都不能删除：删除会让今天或过去日期回落到更早的规则。
	if _, err := pool.Exec(ctx, `INSERT INTO dashboard_revenue_discount_rates(id,user_id,admin_account_id,effective_date,gift_rate,new_user_rate,referral_rate) VALUES('today-rule','user-1','workspace-1','2026-11-14',0.1,0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy", "today-rule"} {
		if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/revenue-discount-rates/"+id, "", "user-1"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "locked") {
			t.Fatalf("DELETE effective rule %s = %d %s, want 409 locked", id, w.Code, w.Body.String())
		}
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/revenue-discount-rates/legacy", "", "user-2"); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace DELETE past rule = %d, want 404", w.Code)
	}
	if still := service.additionalCostSummary(ctx, "user-1", "workspace-1", "2026-11-10", &revenue, revenueDiscountSource{}); still.RevenueDiscount == nil || *still.RevenueDiscount != 50 {
		t.Fatalf("past-effective rule removed: %+v", still)
	}
	if still := service.additionalCostSummary(ctx, "user-1", "workspace-1", "2026-11-14", &revenue, revenueDiscountSource{}); still.RevenueDiscount == nil || *still.RevenueDiscount != 10 {
		t.Fatalf("today's rule removed: %+v", still)
	}

	// 手续费率删除。
	if _, err := service.SaveRechargeFeeRate(ctx, "user-1", RechargeFeeRateInput{EffectiveDate: "2026-11-01", Rate: 0.02}); err != nil {
		t.Fatal(err)
	}
	fees, err := service.ListRechargeFeeRates(ctx, "user-1")
	if err != nil || len(fees) != 1 {
		t.Fatalf("fee rates = %+v err=%v", fees, err)
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/recharge-fee-rates/"+fees[0].ID, "", "user-2"); w.Code != http.StatusNotFound {
		t.Fatalf("other workspace fee DELETE = %d, want 404", w.Code)
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/recharge-fee-rates/"+fees[0].ID, "", "user-1"); w.Code != http.StatusNoContent {
		t.Fatalf("fee DELETE = %d %s, want 204", w.Code, w.Body.String())
	}
	if rate, err := service.GetRechargeFeeRate(ctx, "user-1", "2026-11-14"); err != nil || rate.Rate != defaultRechargeFeeRate {
		t.Fatalf("after fee delete rate = %+v err=%v, want default", rate, err)
	}
	if w := discountRequest(mux, http.MethodDelete, "/api/dashboard/recharge-fee-rates/"+fees[0].ID, "", "user-1"); w.Code != http.StatusNotFound {
		t.Fatalf("repeat fee DELETE = %d, want 404", w.Code)
	}
}

// 回填只接受昨天及以前：今天和以后的日期一旦被回填成最终结果，实时刷新不再更新它，
// 跨零点保存的折扣率也会与已存的最终折扣不一致。
func TestBackfillRejectsTodayAndFutureDates(t *testing.T) {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	repository := &fakeMetricsRepository{}
	service := NewMetricsService(store, &fakePlatformClient{}, &fakeUpstreamLister{}, repository, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.now = func() time.Time { return time.Date(2026, 8, 22, 0, 0, 1, 0, businesstime.Location()) }

	for _, request := range []BackfillRequest{
		{From: "2026-08-22", To: "2026-08-22"},
		{From: "2026-08-21", To: "2026-08-22"},
		{From: "2026-08-23", To: "2026-08-23"},
	} {
		response, err := service.Backfill(context.Background(), "user-1", request)
		if !errors.Is(err, requestError(ErrorBackfillPastOnly)) {
			t.Fatalf("Backfill(%+v) err=%v response=%#v, want %s", request, err, response, ErrorBackfillPastOnly)
		}
	}
	if len(repository.snapshots) != 0 {
		t.Fatalf("rejected backfill wrote snapshots: %#v", repository.snapshots)
	}
	w := httptest.NewRecorder()
	writeError(w, requestError(ErrorBackfillPastOnly))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), ErrorBackfillPastOnly) {
		t.Fatalf("writeError(backfillPastOnly) = %d %s, want 400 with key", w.Code, w.Body.String())
	}
}

package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// ---- 测试替身 ----

type giftLedgerTestRepo struct {
	*fakeMetricsRepository
	mu        sync.Mutex
	records   []GiftLedgerRecord
	usage     map[int64]map[string]int64
	saves     []string
	listErr   error
	saveErr   error
	usageErr  error
	recordErr error
}

func newGiftLedgerTestRepo(records ...GiftLedgerRecord) *giftLedgerTestRepo {
	return &giftLedgerTestRepo{fakeMetricsRepository: &fakeMetricsRepository{}, records: records, usage: map[int64]map[string]int64{}}
}

func (r *giftLedgerTestRepo) ListGiftLedgerRecords(context.Context, string, string) ([]GiftLedgerRecord, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]GiftLedgerRecord(nil), r.records...), nil
}

func (r *giftLedgerTestRepo) GetGiftLedgerRecord(_ context.Context, _, _, id string) (GiftLedgerRecord, error) {
	for _, record := range r.records {
		if record.ID == id {
			return record, nil
		}
	}
	return GiftLedgerRecord{}, ErrGiftLedgerNotFound
}

func (r *giftLedgerTestRepo) CreateGiftLedgerRecord(_ context.Context, record GiftLedgerRecord) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	r.records = append(r.records, record)
	return nil
}

func (r *giftLedgerTestRepo) UpdateGiftLedgerRecord(_ context.Context, record GiftLedgerRecord) error {
	for i := range r.records {
		if r.records[i].ID == record.ID {
			r.records[i].RechargeDate, r.records[i].PaidCents, r.records[i].GiftCents, r.records[i].Note = record.RechargeDate, record.PaidCents, record.GiftCents, record.Note
			return nil
		}
	}
	return ErrGiftLedgerNotFound
}

func (r *giftLedgerTestRepo) DeleteGiftLedgerRecord(_ context.Context, _, _, id string) error {
	for i := range r.records {
		if r.records[i].ID == id {
			r.records = append(r.records[:i], r.records[i+1:]...)
			return nil
		}
	}
	return ErrGiftLedgerNotFound
}

func (r *giftLedgerTestRepo) ListGiftCustomerUsage(_ context.Context, _, _ string, ids []int64, from, to string) (map[int64]map[string]int64, error) {
	if r.usageErr != nil {
		return nil, r.usageErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := map[int64]map[string]int64{}
	for _, id := range ids {
		for date, value := range r.usage[id] {
			if date >= from && date <= to {
				if result[id] == nil {
					result[id] = map[string]int64{}
				}
				result[id][date] = value
			}
		}
	}
	return result, nil
}

func (r *giftLedgerTestRepo) SaveGiftCustomerUsage(_ context.Context, _, _ string, id int64, date string, micros int64, _ time.Time) (int64, error) {
	if r.saveErr != nil {
		return 0, r.saveErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, fmt.Sprintf("%d|%s", id, date))
	if r.usage[id] == nil {
		r.usage[id] = map[string]int64{}
	}
	if existing, ok := r.usage[id][date]; ok {
		return existing, nil
	}
	r.usage[id][date] = micros
	return micros, nil
}

// ListDailyStats 按日期范围过滤（基础替身不过滤）。
func (r *giftLedgerTestRepo) ListDailyStats(_ context.Context, _, _ string, from, to string) ([]DailySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dailyStatsCalls++
	items := make([]DailySnapshot, 0)
	for _, snapshot := range r.dailyStatsSnapshots {
		date := snapshotDate(snapshot)
		if date >= from && date <= to {
			items = append(items, snapshot)
		}
	}
	return items, nil
}

type giftLedgerTestPlatform struct {
	*fakePlatformClient
	mu      sync.Mutex
	costs   map[string]float64
	seq     map[string][]float64 // 同一天依次返回的值（主站补记）；用完后用 costs
	errs    map[string]error
	calls   []string
	users   map[string]upstream.Sub2APIAdminUser
	userErr error
	// block 在返回前调用（不持锁），用于让某次查询停住。
	block func(key string, call int)
}

func newGiftLedgerTestPlatform(revenue float64) *giftLedgerTestPlatform {
	return &giftLedgerTestPlatform{fakePlatformClient: &fakePlatformClient{usageStats: revenue}, costs: map[string]float64{}, errs: map[string]error{}, users: map[string]upstream.Sub2APIAdminUser{}}
}

func (p *giftLedgerTestPlatform) FetchSub2APIAdminUserUsageCost(_ upstream.Session, id, start, end string) (float64, error) {
	p.mu.Lock()
	key := id + "|" + start
	p.calls = append(p.calls, key)
	call := len(p.calls)
	if start != end {
		p.mu.Unlock()
		return 0, errors.New("range query not expected")
	}
	if err := p.errs[key]; err != nil {
		p.mu.Unlock()
		return 0, err
	}
	value := p.costs[key]
	if queue := p.seq[key]; len(queue) > 0 {
		value, p.seq[key] = queue[0], queue[1:]
	}
	block := p.block
	p.mu.Unlock()
	if block != nil {
		block(key, call)
	}
	return value, nil
}

func (p *giftLedgerTestPlatform) callsFor(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, call := range p.calls {
		if call == key {
			count++
		}
	}
	return count
}

func (p *giftLedgerTestPlatform) FetchSub2APIAdminUser(_ upstream.Session, id string) (upstream.Sub2APIAdminUser, error) {
	if p.userErr != nil {
		return upstream.Sub2APIAdminUser{}, p.userErr
	}
	user, ok := p.users[id]
	if !ok {
		return upstream.Sub2APIAdminUser{}, &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: http.StatusNotFound, Reason: upstream.ErrorNotFound}
	}
	return user, nil
}

func (p *giftLedgerTestPlatform) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// 业务日期：2026-11-14 12:00 新加坡。
var giftTestNow = time.Date(2026, 11, 14, 12, 0, 0, 0, businesstime.Location())

func giftTestDate(offset int) string {
	return giftTestNow.AddDate(0, 0, offset).Format("2006-01-02")
}

func giftTestRecord(id string, mainUserID int64, offset int, paid, gift int64) GiftLedgerRecord {
	return GiftLedgerRecord{
		ID: id, UserID: "user-1", AdminAccountID: "account-1", MainUserID: mainUserID, MainUserEmail: fmt.Sprintf("u%d@example.com", mainUserID),
		RechargeDate: giftTestDate(offset), PaidCents: paid * 100, GiftCents: gift * 100,
		CreatedAt: giftTestNow.Add(time.Duration(len(id)) * time.Second),
	}
}

func newGiftLedgerTestService(repo *giftLedgerTestRepo, platform *giftLedgerTestPlatform) *MetricsService {
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(store, platform, &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{SiteID: "site-1", SiteName: "test", Platform: upstream.PlatformSub2API, RechargeRate: 1, RawCost: 5, Meta: upstream.CostFetchMeta{Source: "account_level"}}}},
		repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}}
	service.now = func() time.Time { return giftTestNow }
	return service
}

func giftSettledSnapshot(offset int, revenue float64, customers []RevenueDiscountCustomer) DailySnapshot {
	date, _ := time.ParseInLocation("2006-01-02", giftTestDate(offset), businesstime.Location())
	observed := date.AddDate(0, 0, 1).Add(5 * time.Minute)
	snapshot := DailySnapshot{ID: "snap-" + giftTestDate(offset), UserID: "user-1", AdminAccountID: "account-1", Date: date, TodayProfit: &revenue,
		SettlementStatus: SettlementStatusPartial, SnapshotSource: SnapshotSourceDatedQuery, ObservedAt: &observed}
	if customers != nil {
		raw, _ := json.Marshal(customers)
		detail := RevenueDiscountDetail{Version: revenueDiscountDetailVersion, CustomersRaw: raw}
		snapshot.RevenueDiscountDetail, _ = json.Marshal(detail)
	}
	return snapshot
}

func session() upstream.Session { return authenticatedSession() }

// ---- giftLedgerDay ----

func TestGiftLedgerDayNoRecordsMakesNoMainSiteCalls(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("future", 1, 0, 100, 20))
	platform := newGiftLedgerTestPlatform(100)
	service := newGiftLedgerTestService(repo, platform)
	result, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-1), giftTestDate(0), true)
	if result != nil || err != nil || platform.callCount() != 0 || repo.dailyStatsCalls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d snapshotReads=%d", result, err, platform.callCount(), repo.dailyStatsCalls)
	}
}

func TestGiftLedgerDayConsumptionSourceOrder(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -5, 100, 20))
	// -5：已保存 10（明细里写 99，保存值优先）；-4：已日结明细 20，未保存；-3：没有任何来源（前天以前，查主站并保存）；
	// -1：昨天未日结，现查不保存；0：今天现查不保存。
	repo.usage[1] = map[string]int64{giftTestDate(-5): 10e6}
	repo.dailyStatsSnapshots = []DailySnapshot{
		giftSettledSnapshot(-5, 100, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 99e6, UncoveredMicros: 99e6}}),
		giftSettledSnapshot(-4, 100, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 20e6, UncoveredMicros: 20e6}}),
	}
	platform := newGiftLedgerTestPlatform(100)
	platform.costs["1|"+giftTestDate(-3)] = 30
	platform.costs["1|"+giftTestDate(-2)] = 0
	platform.costs["1|"+giftTestDate(-1)] = 15
	platform.costs["1|"+giftTestDate(0)] = 5
	service := newGiftLedgerTestService(repo, platform)
	result, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(0), giftTestDate(0), false)
	if err != nil {
		t.Fatal(err)
	}
	// 位置：10 + 20 + 30 + 0 + 15 = 75，今天 5 → 80；赠送 G(80)−G(75) = 13.33 − 12.50 = 0.83
	if result.GiftCents != 83 || result.CoveredMicros != 5e6 {
		t.Fatalf("result = %+v", result)
	}
	sort.Strings(platform.calls)
	wantCalls := []string{"1|" + giftTestDate(-1), "1|" + giftTestDate(-2), "1|" + giftTestDate(-3), "1|" + giftTestDate(0)}
	sort.Strings(wantCalls)
	if strings.Join(platform.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", platform.calls, wantCalls)
	}
	sort.Strings(repo.saves)
	wantSaves := []string{"1|" + giftTestDate(-2), "1|" + giftTestDate(-3), "1|" + giftTestDate(-4)}
	sort.Strings(wantSaves)
	if strings.Join(repo.saves, ",") != strings.Join(wantSaves, ",") {
		t.Fatalf("saves = %v, want %v (yesterday and today must not be saved)", repo.saves, wantSaves)
	}
	if repo.usage[1][giftTestDate(-4)] != 20e6 {
		t.Fatalf("settled detail value not saved: %v", repo.usage[1])
	}
}

func TestGiftLedgerDaySettlingSavesTheSettledDay(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 20))
	platform := newGiftLedgerTestPlatform(100)
	platform.costs["1|"+giftTestDate(-1)] = 60
	service := newGiftLedgerTestService(repo, platform)
	result, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-1), giftTestDate(0), true)
	if err != nil || result.GiftCents != 1000 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(repo.saves) != 1 || repo.saves[0] != "1|"+giftTestDate(-1) {
		t.Fatalf("saves = %v, want the settled day saved", repo.saves)
	}
	// 首页计算昨天（未日结）时不保存。
	repo2 := newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 20))
	service2 := newGiftLedgerTestService(repo2, platform)
	if _, err := service2.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(0), giftTestDate(0), false); err != nil {
		t.Fatal(err)
	}
	if len(repo2.saves) != 0 {
		t.Fatalf("live computation saved %v", repo2.saves)
	}
}

func TestGiftLedgerDayLateUsageAfterSettlement(t *testing.T) {
	// S12：第1天日结保存 60（扣 10），之后主站对第1天返回 90；第2、3天各花 30，分别扣 5、5。
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -3, 100, 20))
	repo.usage[1] = map[string]int64{giftTestDate(-3): 60e6}
	platform := newGiftLedgerTestPlatform(100)
	platform.costs["1|"+giftTestDate(-3)] = 90
	platform.costs["1|"+giftTestDate(-2)] = 30
	platform.costs["1|"+giftTestDate(-1)] = 30
	service := newGiftLedgerTestService(repo, platform)
	day2, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-2), giftTestDate(-1), true)
	if err != nil || day2.GiftCents != 500 {
		t.Fatalf("day2=%+v err=%v", day2, err)
	}
	day3, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-1), giftTestDate(0), true)
	if err != nil || day3.GiftCents != 500 {
		t.Fatalf("day3=%+v err=%v", day3, err)
	}
	for _, call := range platform.calls {
		if call == "1|"+giftTestDate(-3) {
			t.Fatalf("settled day re-queried: %v", platform.calls)
		}
	}
}

func TestGiftLedgerDayRetentionGuard(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -100, 100, 20))
	platform := newGiftLedgerTestPlatform(100)
	// 除了距今天 85 天的那一天，其余每天都有保存值。
	repo.usage[1] = map[string]int64{}
	for offset := -100; offset <= -2; offset++ {
		if offset != -85 {
			repo.usage[1][giftTestDate(offset)] = 0
		}
	}
	service := newGiftLedgerTestService(repo, platform)
	_, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(0), giftTestDate(0), false)
	var computeErr giftLedgerComputeError
	if !errors.As(err, &computeErr) || !strings.Contains(computeErr.Reason, "超过本站 80 天安全补查范围") || !strings.Contains(computeErr.Reason, giftTestDate(-85)) || !strings.Contains(computeErr.Reason, "u1@example.com") {
		t.Fatalf("err = %v", err)
	}
	if platform.callCount() != 0 {
		t.Fatalf("main site called %v despite the retention guard", platform.calls)
	}
	// S21：补上第 85 天后，100 天的记录照常计算，主站只查昨天和今天。
	repo.usage[1][giftTestDate(-85)] = 0
	if _, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(0), giftTestDate(0), false); err != nil {
		t.Fatal(err)
	}
	if platform.callCount() != 2 {
		t.Fatalf("calls = %v, want only yesterday and today", platform.calls)
	}
}

func TestGiftLedgerDayLateProtectionFromLaterSettledDay(t *testing.T) {
	// S13：第2天未结算，第3天已结算且明细含该客户；第2天日结时不扣赠送。
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -4, 100, 20))
	repo.usage[1] = map[string]int64{giftTestDate(-4): 30e6}
	repo.dailyStatsSnapshots = []DailySnapshot{giftSettledSnapshot(-2, 100, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 30e6, CoveredMicros: 30e6, GiftCents: 500}})}
	platform := newGiftLedgerTestPlatform(100)
	platform.costs["1|"+giftTestDate(-3)] = 30
	service := newGiftLedgerTestService(repo, platform)
	late, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-3), giftTestDate(0), true)
	if err != nil || late.GiftCents != 0 || late.CoveredMicros != 0 || !late.Customers[0].LateProtected || late.Customers[0].UncoveredMicros != 30e6 {
		t.Fatalf("late = %+v err=%v", late, err)
	}
	// 首页（非日结）不做晚结保护。
	live, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(-3), giftTestDate(0), false)
	if err != nil || live.GiftCents != 500 {
		t.Fatalf("live = %+v err=%v", live, err)
	}
}

func TestGiftLedgerDayFreezeRequiresObservedAfterDayEnd(t *testing.T) {
	snapshot := giftSettledSnapshot(0, 100, nil)
	early := giftTestNow
	snapshot.ObservedAt = &early
	if giftLedgerSettled(snapshot) {
		t.Fatal("today's dated_query row observed before day end must not count as settled")
	}
	snapshot.ObservedAt = nil
	if giftLedgerSettled(snapshot) {
		t.Fatal("row without observed_at must not count as settled")
	}
	settled := giftSettledSnapshot(-1, 100, nil)
	if !giftLedgerSettled(settled) {
		t.Fatal("yesterday's dated_query row observed after day end must count as settled")
	}
	settled.SnapshotSource = SnapshotSourceLiveCache
	if giftLedgerSettled(settled) {
		t.Fatal("live rows never count as settled")
	}
}

func TestGiftLedgerDayFailureReasons(t *testing.T) {
	cases := map[string]struct {
		err  error
		want []string
		not  string
	}{
		"forbidden": {&upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 403, UpstreamMessage: "admin only"}, []string{"HTTP 403", "admin only", "u1@example.com", giftTestDate(0)}, "没有收到主站响应"},
		"missing":   {&upstream.RequestError{MessageKey: upstream.ErrorInvalidResponse, StatusCode: 200, Reason: upstream.GiftUsageMissingCost}, []string{"HTTP 200", "缺少消费金额"}, "没有收到主站响应"},
		"negative":  {&upstream.RequestError{MessageKey: upstream.ErrorInvalidResponse, StatusCode: 200, Reason: upstream.GiftUsageNegativeCost}, []string{"HTTP 200", "消费金额为负数"}, "没有收到主站响应"},
		"timeout":   {&upstream.RequestError{MessageKey: upstream.ErrorNetwork, Timeout: true}, []string{"没有收到主站响应（超时）"}, ""},
		"auth":      {&upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401, UpstreamMessage: "token expired"}, []string{"主站登录已失效", "HTTP 401", "token expired", "重新登录"}, "没有收到主站响应"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
			platform := newGiftLedgerTestPlatform(100)
			platform.errs["1|"+giftTestDate(0)] = tc.err
			service := newGiftLedgerTestService(repo, platform)
			_, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", giftTestDate(0), giftTestDate(0), false)
			for _, want := range tc.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want %q", err, want)
				}
			}
			if tc.not != "" && strings.Contains(err.Error(), tc.not) {
				t.Fatalf("err = %v must not contain %q", err, tc.not)
			}
		})
	}
}

// ---- 营收折扣接入 ----

func TestLiveMetricsRevenueDiscountWithGiftLedger(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(0)] = 60
	service := newGiftLedgerTestService(repo, platform)
	service.additionalCosts = &discountAdditionalCostRepository{
		fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}},
		rule:                         &RevenueDiscountRate{EffectiveDate: giftTestDate(-10), TotalRate: 0.05},
	}
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	// 整站：(200 − 60) × 5% = 7.00；单独客户：60 × 20/120 = 10.00；合计 17.00；实收 183；手续费 183 × 2% = 3.66
	summary := result.AdditionalCosts
	if summary == nil || summary.RevenueDiscount == nil || !approxEqual(*summary.RevenueDiscount, 17) || summary.NetRevenue == nil || !approxEqual(*summary.NetRevenue, 183) {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.RechargeFee == nil || !approxEqual(*summary.RechargeFee, 3.66) {
		t.Fatalf("fee = %v", summary.RechargeFee)
	}
	detail := summary.RevenueDiscountDetail
	if detail == nil || !approxEqual(detail.GeneralDiscount, 7) || !approxEqual(detail.LedgerGift, 10) || !approxEqual(detail.LedgerCovered, 60) {
		t.Fatalf("detail = %+v", detail)
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].RevenueDiscount == nil || !approxEqual(*repo.snapshots[0].RevenueDiscount, 17) || len(repo.snapshots[0].RevenueDiscountDetail) == 0 {
		t.Fatalf("snapshots = %+v", repo.snapshots)
	}
}

// S9：两位客户的覆盖和赠送都要累加，明细保留两位客户。
func TestLiveMetricsGiftLedgerSumsMultipleCustomers(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20), giftTestRecord("s", 2, 0, 50, 10))
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(0)] = 60
	platform.costs["2|"+giftTestDate(0)] = 30
	service := newGiftLedgerTestService(repo, platform)
	service.additionalCosts = &discountAdditionalCostRepository{
		fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}},
		rule:                         &RevenueDiscountRate{EffectiveDate: giftTestDate(-10), TotalRate: 0.05},
	}
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	// 整站 (200 − 60 − 30) × 5% = 5.50；单独客户 60×20/120 + 30×10/60 = 10 + 5；合计 20.50
	summary := result.AdditionalCosts
	if summary == nil || summary.RevenueDiscount == nil || !approxEqual(*summary.RevenueDiscount, 20.5) {
		t.Fatalf("summary = %+v", summary)
	}
	detail := summary.RevenueDiscountDetail
	if detail == nil || !approxEqual(detail.GeneralDiscount, 5.5) || !approxEqual(detail.LedgerGift, 15) || !approxEqual(detail.LedgerCovered, 90) || len(detail.Customers) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	gifts := map[string]int64{}
	for _, c := range detail.Customers {
		gifts[c.MainUserID] = c.GiftCents
	}
	if gifts["1"] != 1000 || gifts["2"] != 500 {
		t.Fatalf("customers = %+v", detail.Customers)
	}
}

func TestGiftLedgerDetailRejectsUnsafeCustomers(t *testing.T) {
	wrap := func(customers string) []byte { return []byte(`{"version":1,"customers":` + customers + `}`) }
	good := `{"mainUserId":"1","consumptionMicros":60000000,"coveredMicros":60000000,"uncoveredMicros":0,"giftCents":1000,"lateProtected":false}`
	late := `{"mainUserId":"2","consumptionMicros":5000000,"coveredMicros":0,"uncoveredMicros":5000000,"giftCents":0,"lateProtected":true}`
	detail, err := parseRevenueDiscountDetail(wrap("[" + good + "," + late + "]"))
	if err != nil || detail == nil || len(detail.Customers) != 2 || detail.Customers[0].GiftCents != 1000 || !detail.Customers[1].LateProtected {
		t.Fatalf("valid detail = %+v err=%v", detail, err)
	}
	replace := func(old, new string) string { return "[" + strings.Replace(good, old, new, 1) + "]" }
	bad := map[string]string{
		"gift null":            replace(`"giftCents":1000`, `"giftCents":null`),
		"covered null":         replace(`"coveredMicros":60000000`, `"coveredMicros":null`),
		"id null":              replace(`"mainUserId":"1"`, `"mainUserId":null`),
		"late null":            replace(`"lateProtected":false`, `"lateProtected":null`),
		"gift missing":         replace(`,"giftCents":1000`, ``),
		"gift string":          replace(`"giftCents":1000`, `"giftCents":"1000"`),
		"negative gift":        replace(`"giftCents":1000`, `"giftCents":-1`),
		"negative uncovered":   replace(`"uncoveredMicros":0`, `"uncoveredMicros":-1`),
		"split mismatch":       replace(`"uncoveredMicros":0`, `"uncoveredMicros":1`),
		"late with gift":       replace(`"lateProtected":false`, `"lateProtected":true`),
		"bad id":               replace(`"mainUserId":"1"`, `"mainUserId":"abc"`),
		"zero id":              replace(`"mainUserId":"1"`, `"mainUserId":"0"`),
		"duplicate customer":   "[" + good + "," + good + "]",
		"leading zero id":      replace(`"mainUserId":"1"`, `"mainUserId":"01"`),
		"padded id":            replace(`"mainUserId":"1"`, `"mainUserId":" 1"`),
		"same id two ways":     "[" + good + "," + strings.Replace(good, `"mainUserId":"1"`, `"mainUserId":"01"`, 1) + "]",
		"id beyond json range": replace(`"mainUserId":"1"`, `"mainUserId":"9007199254740992"`),
		"empty customers":      "[]",
		"customers not a list": `{}`,
	}
	for name, customers := range bad {
		if detail, err := parseRevenueDiscountDetail(wrap(customers)); !errors.Is(err, errRevenueDiscountDetailBroken) || detail != nil {
			t.Fatalf("%s accepted: %+v err=%v", name, detail, err)
		}
	}
}

func TestLiveMetricsGiftLedgerFailureHidesRevenue(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	platform.errs["1|"+giftTestDate(0)] = &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 403, UpstreamMessage: "denied"}
	service := newGiftLedgerTestService(repo, platform)
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	reason := result.MetricErrors["todayProfit"]
	if result.TodayProfit != nil || !strings.Contains(reason, "单独客户赠送计算失败") || !strings.Contains(reason, "u1@example.com") || !strings.Contains(reason, giftTestDate(0)) || !strings.Contains(reason, "HTTP 403") {
		t.Fatalf("revenue=%v reason=%q", result.TodayProfit, reason)
	}
	if len(repo.snapshots) != 0 {
		t.Fatalf("snapshot written while gift unknown: %+v", repo.snapshots)
	}
}

func TestLiveMetricsRevenueFallbackUsesSnapshotDiscount(t *testing.T) {
	today, _ := time.ParseInLocation("2006-01-02", giftTestDate(0), businesstime.Location())
	revenue, discount := 150.0, 12.0
	withDetail := DailySnapshot{Date: today, TodayProfit: &revenue, RevenueDiscount: &discount, SnapshotSource: SnapshotSourceLiveCache}
	withDetail.RevenueDiscountDetail, _ = json.Marshal(RevenueDiscountDetail{Version: 1, CustomersRaw: json.RawMessage(`[{"mainUserId":"1","consumptionMicros":1,"coveredMicros":1,"uncoveredMicros":0,"giftCents":0,"lateProtected":false}]`)})

	t.Run("snapshot with detail", func(t *testing.T) {
		repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
		repo.latestSnapshot = &withDetail
		platform := newGiftLedgerTestPlatform(0)
		platform.usageStatsErr = errors.New("main site down")
		service := newGiftLedgerTestService(repo, platform)
		result, err := service.LiveMetrics(context.Background(), "user-1")
		if err != nil {
			t.Fatal(err)
		}
		if platform.callCount() != 0 || result.AdditionalCosts == nil || result.AdditionalCosts.RevenueDiscount == nil || *result.AdditionalCosts.RevenueDiscount != 12 {
			t.Fatalf("calls=%d summary=%+v", platform.callCount(), result.AdditionalCosts)
		}
	})
	t.Run("no detail and no records keeps V3.1.6", func(t *testing.T) {
		repo := newGiftLedgerTestRepo()
		plain := withDetail
		plain.RevenueDiscountDetail = nil
		repo.latestSnapshot = &plain
		platform := newGiftLedgerTestPlatform(0)
		platform.usageStatsErr = errors.New("main site down")
		service := newGiftLedgerTestService(repo, platform)
		service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, rule: &RevenueDiscountRate{TotalRate: 0.1}}
		result, err := service.LiveMetrics(context.Background(), "user-1")
		if err != nil {
			t.Fatal(err)
		}
		if platform.callCount() != 0 || result.AdditionalCosts.RevenueDiscount == nil || *result.AdditionalCosts.RevenueDiscount != 15 {
			t.Fatalf("summary=%+v", result.AdditionalCosts)
		}
	})
	t.Run("no detail but records", func(t *testing.T) {
		repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
		plain := withDetail
		plain.RevenueDiscountDetail = nil
		repo.latestSnapshot = &plain
		platform := newGiftLedgerTestPlatform(0)
		platform.usageStatsErr = errors.New("main site down")
		service := newGiftLedgerTestService(repo, platform)
		result, err := service.LiveMetrics(context.Background(), "user-1")
		if err != nil {
			t.Fatal(err)
		}
		if platform.callCount() != 0 || result.TodayProfit != nil || !strings.Contains(result.MetricErrors["todayProfit"], "今天还没有保存过单独客户结果") {
			t.Fatalf("calls=%d revenue=%v errors=%v", platform.callCount(), result.TodayProfit, result.MetricErrors)
		}
	})
}

func TestLiveMetricsTodayClampsGeneralBase(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))
	platform := newGiftLedgerTestPlatform(50)
	platform.costs["1|"+giftTestDate(0)] = 60
	service := newGiftLedgerTestService(repo, platform)
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, rule: &RevenueDiscountRate{TotalRate: 0.05}}
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	detail := result.AdditionalCosts.RevenueDiscountDetail
	if detail == nil || detail.GeneralBase != 0 || detail.GeneralDiscount != 0 || !approxEqual(*result.AdditionalCosts.RevenueDiscount, 10) {
		t.Fatalf("summary = %+v detail=%+v", result.AdditionalCosts, detail)
	}
}

func TestLiveMetricsGiftLedgerReasonSurvivesAccountCheck(t *testing.T) {
	repo := &giftLedgerUnconfirmedRepo{giftLedgerTestRepo: newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))}
	platform := newGiftLedgerTestPlatform(200)
	platform.errs["1|"+giftTestDate(0)] = &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 500}
	store := newFakeSessionStore()
	store.set("user-1", "account-1", AdminSession{Session: authenticatedSession()})
	service := NewMetricsService(store, platform, &fakeUpstreamLister{}, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}}
	service.now = func() time.Time { return giftTestNow }
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.MetricErrors["todayProfit"], "u1@example.com") {
		t.Fatalf("reason overwritten: %v", result.MetricErrors)
	}
}

type giftLedgerUnconfirmedRepo struct{ *giftLedgerTestRepo }

func (r *giftLedgerUnconfirmedRepo) UnconfirmedQuotaRecognitionAssets(context.Context, string, string, string, string) (map[string]bool, error) {
	return nil, errors.New("storage down")
}

func TestFinalizeGiftLedgerFrozenAfterFirstSettlement(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(-1)] = 60
	service := newGiftLedgerTestService(repo, platform)
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}
	if err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery); err != nil {
		t.Fatal(err)
	}
	first := repo.snapshots[len(repo.snapshots)-1]
	if first.RevenueDiscount == nil || !approxEqual(*first.RevenueDiscount, 10) || len(first.RevenueDiscountDetail) == 0 {
		t.Fatalf("first settlement = %+v", first)
	}
	// 修改记录为 100送30，营收变成 220，再次日结：单独客户部分不变，整站部分按新营收（无规则时为 0）。
	repo.records[0].GiftCents = 3000
	repo.dailyStatsSnapshots = []DailySnapshot{first}
	platform.usageStats = 220
	callsBefore := platform.callCount()
	if err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceBackfill); err != nil {
		t.Fatal(err)
	}
	second := repo.snapshots[len(repo.snapshots)-1]
	firstDetail, _ := parseRevenueDiscountDetail(first.RevenueDiscountDetail)
	secondDetail, _ := parseRevenueDiscountDetail(second.RevenueDiscountDetail)
	if second.RevenueDiscount == nil || !approxEqual(*second.RevenueDiscount, 10) || string(firstDetail.CustomersRaw) != string(secondDetail.CustomersRaw) {
		t.Fatalf("second settlement = %+v detail %s vs %s", second, secondDetail.CustomersRaw, firstDetail.CustomersRaw)
	}
	if platform.callCount() != callsBefore {
		t.Fatalf("frozen re-run queried the main site: %v", platform.calls)
	}
	// 已有行是 live_cache（未日结）时照常现算：赠送按新记录 60 × 30/130 = 13.85。
	live := first
	live.SnapshotSource = SnapshotSourceLiveCache
	repo.dailyStatsSnapshots = []DailySnapshot{live}
	if err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery); err != nil {
		t.Fatal(err)
	}
	third := repo.snapshots[len(repo.snapshots)-1]
	if third.RevenueDiscount == nil || !approxEqual(*third.RevenueDiscount, 13.85) {
		t.Fatalf("unsettled row recomputed = %v", third.RevenueDiscount)
	}
}

func TestFinalizeGiftLedgerConsistencyCheck(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -10, 100, 20))
	// S18：已冻结明细里客户当天消费 50、覆盖 0；主站返回总消费 0。
	repo.dailyStatsSnapshots = []DailySnapshot{giftSettledSnapshot(-1, 80, []RevenueDiscountCustomer{{MainUserID: "1", Email: "u1@example.com", ConsumptionMicros: 50e6, UncoveredMicros: 50e6}})}
	platform := newGiftLedgerTestPlatform(0)
	service := newGiftLedgerTestService(repo, platform)
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}
	err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery)
	if !errors.Is(err, errRevenueDiscountUnavailable) || !strings.Contains(err.Error(), "小于单独客户这一天的消费合计") || len(repo.snapshots) != 0 {
		t.Fatalf("err=%v snapshots=%d", err, len(repo.snapshots))
	}
	// 等于主站总消费 + 0.01 时通过；S18b：基数取 0，不出现负折扣。
	repo.dailyStatsSnapshots = []DailySnapshot{giftSettledSnapshot(-1, 80, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 10000, CoveredMicros: 10000}})}
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, rule: &RevenueDiscountRate{TotalRate: 0.9}}
	if err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery); err != nil {
		t.Fatal(err)
	}
	written := repo.snapshots[len(repo.snapshots)-1]
	if written.RevenueDiscount == nil || *written.RevenueDiscount != 0 {
		t.Fatalf("S18b discount = %v, want 0", written.RevenueDiscount)
	}
}

func TestFinalizeGiftLedgerFailureSkipsSnapshot(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	platform.errs["1|"+giftTestDate(-1)] = &upstream.RequestError{MessageKey: upstream.ErrorNetwork, Timeout: true}
	service := newGiftLedgerTestService(repo, platform)
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}
	err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery)
	if !errors.Is(err, errRevenueDiscountUnavailable) || !strings.Contains(err.Error(), "u1@example.com") || !strings.Contains(err.Error(), "超时") || len(repo.snapshots) != 0 {
		t.Fatalf("err=%v snapshots=%d", err, len(repo.snapshots))
	}
	// 明细损坏：报错不写。
	broken := giftSettledSnapshot(-1, 200, nil)
	broken.RevenueDiscountDetail = []byte(`{"version":2}`)
	repo.dailyStatsSnapshots = []DailySnapshot{broken}
	err = service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery)
	if !errors.Is(err, errRevenueDiscountUnavailable) || !strings.Contains(err.Error(), "数据格式不对") || len(repo.snapshots) != 0 {
		t.Fatalf("broken detail err=%v snapshots=%d", err, len(repo.snapshots))
	}
	// 金额为 null（Go 会解成 0）：同样报错不写，不能把已结算的赠送改成 0。
	nullGift := giftSettledSnapshot(-1, 200, nil)
	nullGift.RevenueDiscountDetail = []byte(`{"version":1,"customers":[{"mainUserId":"1","consumptionMicros":60000000,"coveredMicros":60000000,"uncoveredMicros":0,"giftCents":null,"lateProtected":false}]}`)
	repo.dailyStatsSnapshots = []DailySnapshot{nullGift}
	err = service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery)
	if !errors.Is(err, errRevenueDiscountUnavailable) || !strings.Contains(err.Error(), "数据格式不对") || len(repo.snapshots) != 0 {
		t.Fatalf("null gift detail err=%v snapshots=%d", err, len(repo.snapshots))
	}
}

func TestBackfillShowsGiftLedgerReason(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -2, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	platform.errs["1|"+giftTestDate(-2)] = &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 502}
	service := newGiftLedgerTestService(repo, platform)
	response, err := service.Backfill(context.Background(), "user-1", BackfillRequest{From: giftTestDate(-2), To: giftTestDate(-2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Status != "failed" || !strings.Contains(response.Results[0].Reason, "u1@example.com") || !strings.Contains(response.Results[0].Reason, "HTTP 502") {
		t.Fatalf("results = %+v", response.Results)
	}
}

func TestAccountSubstateAndDailyStatsUseStoredDiscount(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -3, 100, 20))
	platform := newGiftLedgerTestPlatform(200)
	service := newGiftLedgerTestService(repo, platform)
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, ruleErr: errors.New("rule down")}
	settled := giftSettledSnapshot(-1, 200, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 60e6, CoveredMicros: 60e6, GiftCents: 1000}})
	discount := 10.0
	settled.RevenueDiscount = &discount
	if err := service.finalizeAutomaticAccountSubstate(context.Background(), settled, "run-1", giftTestDate(-1), nil, upstream.Session{}); err != nil {
		t.Fatal(err)
	}
	written := repo.snapshots[len(repo.snapshots)-1]
	if written.RevenueDiscount == nil || *written.RevenueDiscount != 10 || string(written.RevenueDiscountDetail) != string(settled.RevenueDiscountDetail) {
		t.Fatalf("account substate = %+v", written)
	}
	// 每日明细今天行：快照有明细时用快照已存值，不查规则（规则读取失败也照常显示）。
	todayRow := giftSettledSnapshot(0, 200, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 60e6, CoveredMicros: 60e6, GiftCents: 1000}})
	todayRow.SnapshotSource = SnapshotSourceLiveCache
	todayRow.RevenueDiscount = &discount
	repo.dailyStatsSnapshots = []DailySnapshot{todayRow}
	items, err := service.DailyStats(context.Background(), "user-1", giftTestDate(0), giftTestDate(0), 1, 31, false)
	if err != nil || len(items) != 1 || items[0].TodayProfit == nil || items[0].RevenueDiscount == nil || *items[0].RevenueDiscount != 10 || items[0].RevenueDiscountDetail == nil {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	if platform.callCount() != 0 {
		t.Fatalf("stored discount paths queried the main site: %v", platform.calls)
	}
}

func TestNoGiftLedgerKeepsV316Numbers(t *testing.T) {
	repo := newGiftLedgerTestRepo()
	platform := newGiftLedgerTestPlatform(200)
	service := newGiftLedgerTestService(repo, platform)
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, rule: &RevenueDiscountRate{TotalRate: 0.05}}
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.AdditionalCosts.RevenueDiscount == nil || *result.AdditionalCosts.RevenueDiscount != 10 || result.AdditionalCosts.RevenueDiscountDetail != nil || result.AdditionalCosts.RevenueDiscountRule == nil {
		t.Fatalf("summary = %+v", result.AdditionalCosts)
	}
	if len(repo.snapshots) != 1 || repo.snapshots[0].RevenueDiscountDetail != nil {
		t.Fatalf("snapshot detail written without records: %+v", repo.snapshots)
	}
	// 失败表现：规则读取失败时，今天行营收为空；账号补结返回原错误；首页原因仍是原固定文字。
	service.additionalCosts = &discountAdditionalCostRepository{fakeAdditionalCostRepository: fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0.02}}, ruleErr: errors.New("rule down")}
	result, err = service.LiveMetrics(context.Background(), "user-1")
	if err != nil || result.MetricErrors["todayProfit"] != revenueDiscountUnavailableReason {
		t.Fatalf("live errors = %v err=%v", result.MetricErrors, err)
	}
	plain := giftSettledSnapshot(-1, 200, nil)
	if err := service.finalizeAutomaticAccountSubstate(context.Background(), plain, "run-1", giftTestDate(-1), nil, upstream.Session{}); err != errRevenueDiscountUnavailable {
		t.Fatalf("account substate err = %v, want the bare V3.1.6 error", err)
	}
	todayRow := giftSettledSnapshot(0, 200, nil)
	todayRow.SnapshotSource = SnapshotSourceLiveCache
	repo.dailyStatsSnapshots = []DailySnapshot{todayRow}
	items, err := service.DailyStats(context.Background(), "user-1", giftTestDate(0), giftTestDate(0), 1, 31, false)
	if err != nil || len(items) != 1 || items[0].TodayProfit != nil {
		t.Fatalf("today row = %+v err=%v", items, err)
	}
	if platform.callCount() != 0 {
		t.Fatalf("no-record paths queried per customer: %v", platform.calls)
	}
}

// ---- 日结排队与时间 ----

// giftLedgerFinalizerRepo 模拟 PostgreSQL 日结：写入的快照同时成为 ListDailyStats 能读到的行。
type giftLedgerFinalizerRepo struct{ *giftLedgerTestRepo }

func (r *giftLedgerFinalizerRepo) FinalizeDailySnapshotWithKeyRuns(_ context.Context, snapshot DailySnapshot, _ []SiteDailyCost, _ []UpstreamKeyCostRun) (DailySnapshot, error) {
	r.store(snapshot)
	return snapshot, nil
}

func (r *giftLedgerFinalizerRepo) Upsert(_ context.Context, snapshot DailySnapshot) error {
	r.store(snapshot)
	return nil
}

func (r *giftLedgerFinalizerRepo) store(snapshot DailySnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snapshot)
	for i := range r.dailyStatsSnapshots {
		if snapshotDate(r.dailyStatsSnapshots[i]) == snapshotDate(snapshot) {
			r.dailyStatsSnapshots[i] = snapshot
			return
		}
	}
	r.dailyStatsSnapshots = append(r.dailyStatsSnapshots, snapshot)
}

func (r *giftLedgerFinalizerRepo) latestFor(date string) DailySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.snapshots) - 1; i >= 0; i-- {
		if snapshotDate(r.snapshots[i]) == date {
			return r.snapshots[i]
		}
	}
	return DailySnapshot{}
}

func giftDetailCustomers(t *testing.T, snapshot DailySnapshot) []RevenueDiscountCustomer {
	t.Helper()
	detail, err := parseRevenueDiscountDetail(snapshot.RevenueDiscountDetail)
	if err != nil || detail == nil {
		t.Fatalf("detail = %s err=%v", snapshot.RevenueDiscountDetail, err)
	}
	return detail.Customers
}

func waitGiftLedgerLockWaiters(t *testing.T, service *MetricsService, ref ActiveSessionRef, want int) {
	t.Helper()
	key := costFinalizationKey{costRecoveryWorkspace: costRecoveryWorkspace{userID: ref.UserID, adminAccountID: ref.AdminAccountID}, date: giftLedgerSettlementLockDate}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		service.finalizationMu.Lock()
		lock := service.finalizationLocks[key]
		refs := 0
		if lock != nil {
			refs = lock.refs
		}
		service.finalizationMu.Unlock()
		if refs >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gift ledger lock never reached %d holders/waiters", want)
}

// S22：两个日期的日结并发，主站对第12天先后返回 60 和 90。单独客户部分排队；第12天只保存一个值，所有计算都用它；r2 合计扣 15。
func TestFinalizeGiftLedgerSerializedAcrossDates(t *testing.T) {
	r1 := giftTestRecord("r1", 1, -2, 90, 0)
	r2 := giftTestRecord("r2-later", 1, -2, 15, 15)
	repo := &giftLedgerFinalizerRepo{giftLedgerTestRepo: newGiftLedgerTestRepo(r1, r2)}
	platform := newGiftLedgerTestPlatform(500)
	day12, day13, day14 := giftTestDate(-2), giftTestDate(-1), giftTestDate(0)
	platform.seq = map[string][]float64{"1|" + day12: {60, 90}}
	platform.costs["1|"+day12] = 90
	platform.costs["1|"+day13] = 30
	platform.costs["1|"+day14] = 30
	started, release := make(chan struct{}), make(chan struct{})
	platform.block = func(key string, call int) {
		if key == "1|"+day12 && call == 1 {
			close(started)
			<-release
		}
	}
	service := newGiftLedgerTestService(repo.giftLedgerTestRepo, platform)
	service.metricsRepo = repo
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[0] = service.finalizeBusinessDate(context.Background(), ref, day12, SnapshotSourceBackfill)
	}()
	<-started
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[1] = service.finalizeBusinessDate(context.Background(), ref, day13, SnapshotSourceDatedQuery)
	}()
	waitGiftLedgerLockWaiters(t, service, ref, 2)
	time.Sleep(50 * time.Millisecond)
	if platform.callCount() != 1 {
		t.Fatalf("second settlement queried before the first finished: %v", platform.calls)
	}
	// 首页实时不排队：此时查到第12天 90 并先保存。
	live, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", day14, day14, false)
	if err != nil || live.GiftCents != 0 {
		t.Fatalf("live = %+v err=%v", live, err)
	}
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs = %v", errs)
	}
	if repo.usage[1][day12] != 90e6 {
		t.Fatalf("stored day12 = %d, want the first saved value 90", repo.usage[1][day12])
	}
	d12 := giftDetailCustomers(t, repo.latestFor(day12))[0]
	d13 := giftDetailCustomers(t, repo.latestFor(day13))[0]
	if d12.ConsumptionMicros != 90e6 {
		t.Fatalf("day12 used %d, want the stored value instead of its own fetch", d12.ConsumptionMicros)
	}
	final, err := service.giftLedgerDay(context.Background(), session(), "user-1", "account-1", day14, day14, false)
	if err != nil {
		t.Fatal(err)
	}
	if total := d12.GiftCents + d13.GiftCents + final.GiftCents; total != 1500 {
		t.Fatalf("r2 gift total = %d (d12 %d, d13 %d, d14 %d), want 1500", total, d12.GiftCents, d13.GiftCents, final.GiftCents)
	}
}

// 日结开始在日末前、单独客户部分开始在日末后：单独客户部分按日末后的“今天”计算（这一天算日结，保存消费）；
// 写入时间早于日末，写入的行不算已日结。
func TestFinalizeLedgerTodayFixedAfterObservedAt(t *testing.T) {
	repo := &giftLedgerFinalizerRepo{giftLedgerTestRepo: newGiftLedgerTestRepo(giftTestRecord("r", 1, 0, 100, 20))}
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(0)] = 60
	service := newGiftLedgerTestService(repo.giftLedgerTestRepo, platform)
	service.metricsRepo = repo
	beforeEnd := time.Date(2026, 11, 14, 23, 59, 59, 0, businesstime.Location())
	afterEnd := time.Date(2026, 11, 15, 0, 0, 1, 0, businesstime.Location())
	var clock atomic.Value
	clock.Store(beforeEnd)
	service.now = func() time.Time { return clock.Load().(time.Time) }
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}
	hold, err := service.acquireCostFinalization(context.Background(), ref, giftLedgerSettlementLockDate)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- service.finalizeBusinessDate(context.Background(), ref, giftTestDate(0), SnapshotSourceDatedQuery)
	}()
	waitGiftLedgerLockWaiters(t, service, ref, 2)
	clock.Store(afterEnd)
	hold()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	written := repo.latestFor(giftTestDate(0))
	if written.ObservedAt == nil || !written.ObservedAt.Equal(beforeEnd.UTC()) {
		t.Fatalf("observed_at = %v, want the time taken before the queue", written.ObservedAt)
	}
	if giftLedgerSettled(written) {
		t.Fatal("row written before day end must not count as settled")
	}
	if len(repo.saves) != 1 || repo.saves[0] != "1|"+giftTestDate(0) {
		t.Fatalf("saves = %v, want the day saved because the ledger part ran after day end", repo.saves)
	}
}

// S20：今天被成本恢复提前写成 dated_query（写入时间早于日末）的行不算已日结，零点后日结照常现算。
func TestFinalizeGiftLedgerEarlyDatedQueryRowIsRecomputed(t *testing.T) {
	repo := newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 30))
	early := giftSettledSnapshot(-1, 200, []RevenueDiscountCustomer{{MainUserID: "1", ConsumptionMicros: 60e6, CoveredMicros: 60e6, GiftCents: 1000}})
	observed := time.Date(2026, 11, 13, 23, 0, 0, 0, businesstime.Location())
	early.ObservedAt = &observed
	repo.dailyStatsSnapshots = []DailySnapshot{early}
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(-1)] = 60
	service := newGiftLedgerTestService(repo, platform)
	if err := service.finalizeBusinessDate(context.Background(), ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}, giftTestDate(-1), SnapshotSourceDatedQuery); err != nil {
		t.Fatal(err)
	}
	written := repo.snapshots[len(repo.snapshots)-1]
	if written.RevenueDiscount == nil || !approxEqual(*written.RevenueDiscount, 13.85) || platform.callCount() != 1 {
		t.Fatalf("discount = %v calls=%v, want recomputed 13.85", written.RevenueDiscount, platform.calls)
	}
}

type failingAccountTargets struct{}

func (failingAccountTargets) ListAutomaticAccountTargets(context.Context, string, string, string) ([]AutomaticAccountTarget, error) {
	return nil, errors.New("account targets down")
}

func (failingAccountTargets) SaveAutomaticAccountDailyStats(context.Context, []AccountDailyStat) error {
	return nil
}

// S24：日结写入快照后账号补结失败、日结返回错误；重试时这一天已是已日结日，沿用已保存的单独客户部分，不再按客户查询。
func TestFinalizeRetryAfterAccountSubstateFailure(t *testing.T) {
	repo := &giftLedgerFinalizerRepo{giftLedgerTestRepo: newGiftLedgerTestRepo(giftTestRecord("r", 1, -1, 100, 20))}
	platform := newGiftLedgerTestPlatform(200)
	platform.costs["1|"+giftTestDate(-1)] = 60
	service := newGiftLedgerTestService(repo.giftLedgerTestRepo, platform)
	service.metricsRepo = repo
	service.accountStats = failingAccountTargets{}
	ref := ActiveSessionRef{UserID: "user-1", AdminAccountID: "account-1"}
	if err := service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery); err == nil || !strings.Contains(err.Error(), "account targets down") {
		t.Fatalf("err = %v, want the account substate failure", err)
	}
	first := repo.latestFor(giftTestDate(-1))
	if first.RevenueDiscount == nil || *first.RevenueDiscount != 10 || len(first.RevenueDiscountDetail) == 0 {
		t.Fatalf("first write = %+v", first)
	}
	repo.records[0].GiftCents = 3000
	calls := platform.callCount()
	_ = service.finalizeBusinessDate(context.Background(), ref, giftTestDate(-1), SnapshotSourceDatedQuery)
	retried := repo.latestFor(giftTestDate(-1))
	if platform.callCount() != calls || retried.RevenueDiscount == nil || *retried.RevenueDiscount != 10 {
		t.Fatalf("retry calls=%v discount=%v, want the frozen gift 10", platform.calls, retried.RevenueDiscount)
	}
	if got, want := giftDetailCustomers(t, retried), giftDetailCustomers(t, first); got[0] != want[0] {
		t.Fatalf("retry customers = %+v, want %+v", got, want)
	}
}

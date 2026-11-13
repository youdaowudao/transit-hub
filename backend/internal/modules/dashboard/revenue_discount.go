package dashboard

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

// 营收折扣：用户花掉的充值赠送、新用户福利、邀请返利不是实际收到的钱。
// 按比例从主站消费中扣除得到实收营收；折扣不计入成本。
var (
	ErrRevenueDiscountInvalidRate = errors.New("dashboard.revenueDiscount.errors.invalidRate")
	ErrRevenueDiscountInvalidDate = errors.New("dashboard.revenueDiscount.errors.invalidDate")
	ErrRevenueDiscountPastDate    = errors.New("dashboard.revenueDiscount.errors.pastDate")
	ErrRevenueDiscountNotFound    = errors.New("dashboard.revenueDiscount.errors.notFound")
	// 今天及以前生效的折扣率不能删除：删除会让今天或过去日期回落到更早的规则，补结时改写已结算的数字。
	ErrRevenueDiscountLocked = errors.New("dashboard.revenueDiscount.errors.locked")
	// 折扣率读取失败按营收读取失败处理：不写快照，等下一轮重试。
	errRevenueDiscountUnavailable = errors.New("dashboard.revenueDiscount.errors.unavailable")
	ErrRechargeFeeRateNotFound    = errors.New("dashboard.additionalCost.errors.feeRateNotFound")
)

const revenueDiscountUnavailableReason = "营收折扣率读取失败，暂不计算成本和利润；请稍后刷新，仍失败请检查本站数据库连接。不会修改主站。"

// RevenueDiscountRate 是一条按生效日期保存的营收折扣率，三项都是占主站消费的比例（小数）。
type RevenueDiscountRate struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	AdminAccountID string    `json:"-"`
	EffectiveDate  string    `json:"effectiveDate"`
	GiftRate       float64   `json:"giftRate"`
	NewUserRate    float64   `json:"newUserRate"`
	ReferralRate   float64   `json:"referralRate"`
	TotalRate      float64   `json:"totalRate"`
	CreatedAt      time.Time `json:"createdAt"`
}

type RevenueDiscountRateInput struct {
	EffectiveDate string  `json:"effectiveDate"`
	GiftRate      float64 `json:"giftRate"`
	NewUserRate   float64 `json:"newUserRate"`
	ReferralRate  float64 `json:"referralRate"`
}

// revenueDiscountRateRepository 由 PostgreSQL 仓储实现；未实现时视为没有折扣规则。
type revenueDiscountRateRepository interface {
	GetRevenueDiscountRate(ctx context.Context, userID, adminAccountID, date string) (*RevenueDiscountRate, error)
	ListRevenueDiscountRates(ctx context.Context, userID, adminAccountID string) ([]RevenueDiscountRate, error)
	SaveRevenueDiscountRate(ctx context.Context, rate RevenueDiscountRate) error
	// DeleteRevenueDiscountRate 只删除 effective_date > today（明天起）的规则；已生效的返回 ErrRevenueDiscountLocked。
	DeleteRevenueDiscountRate(ctx context.Context, userID, adminAccountID, id, today string) error
}

type rechargeFeeRateDeleteRepository interface {
	DeleteRechargeFeeRate(ctx context.Context, userID, adminAccountID, id string) error
}

func revenueDiscountTotal(gift, newUser, referral float64) float64 {
	return math.Round((gift+newUser+referral)*1e6) / 1e6
}

func validRevenueDiscountPart(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// normalizeRevenueDiscountInput 校验三项比率和生效日期；生效日期最早为明天（晚于 today），
// 保存和删除都只动明天起的规则：已结算的日期（昨天及以前）查到的规则不会再变；跨零点的慢请求要晚到一天以上
// 才可能碰到已结算日期。零点前提交、零点后才落库的请求可能改动刚生效的“今天”规则一次，今天尚未结算，实时数字随之更新。
func normalizeRevenueDiscountInput(input RevenueDiscountRateInput, today string) (RevenueDiscountRateInput, error) {
	input.EffectiveDate = strings.TrimSpace(input.EffectiveDate)
	if _, err := time.ParseInLocation("2006-01-02", input.EffectiveDate, businesstime.Location()); err != nil {
		return input, ErrRevenueDiscountInvalidDate
	}
	if input.EffectiveDate <= today {
		return input, ErrRevenueDiscountPastDate
	}
	if !validRevenueDiscountPart(input.GiftRate) || !validRevenueDiscountPart(input.NewUserRate) || !validRevenueDiscountPart(input.ReferralRate) {
		return input, ErrRevenueDiscountInvalidRate
	}
	if revenueDiscountTotal(input.GiftRate, input.NewUserRate, input.ReferralRate) >= 1 {
		return input, ErrRevenueDiscountInvalidRate
	}
	return input, nil
}

// applyRevenueDiscount 按总折扣率写入营收折扣和实收营收。revenue 或 rate 为空时不扣折扣，
// 汇总保持旧口径（netRevenue 为空，利润按原营收计算）。
func applyRevenueDiscount(summary *AdditionalCostSummary, revenue *float64, rate *float64) {
	if summary == nil {
		return
	}
	summary.RevenueDiscount, summary.RevenueDiscountRate, summary.NetRevenue = nil, nil, nil
	if revenue == nil || rate == nil {
		return
	}
	discount := float64(cents(*revenue**rate)) / 100
	net := *revenue - discount
	rateValue := *rate
	summary.RevenueDiscount = &discount
	summary.RevenueDiscountRate = &rateValue
	summary.NetRevenue = &net
}

// applySnapshotRevenueDiscount 使用快照已存的折扣，不查当前规则，避免已结算日期被新规则改写。
func applySnapshotRevenueDiscount(summary *AdditionalCostSummary, snapshot DailySnapshot) {
	if summary == nil {
		return
	}
	summary.RevenueDiscount, summary.RevenueDiscountRate, summary.NetRevenue = nil, nil, nil
	if snapshot.RevenueDiscount == nil || snapshot.TodayProfit == nil {
		return
	}
	discount := *snapshot.RevenueDiscount
	net := *snapshot.TodayProfit - discount
	summary.RevenueDiscount = &discount
	summary.NetRevenue = &net
	if snapshot.RevenueDiscountRate != nil {
		rate := *snapshot.RevenueDiscountRate
		summary.RevenueDiscountRate = &rate
	}
}

// feeRevenueBase 是手续费的计算基数：有营收折扣时用实收营收。
func feeRevenueBase(summary *AdditionalCostSummary, revenue float64) float64 {
	if summary != nil && summary.NetRevenue != nil {
		return *summary.NetRevenue
	}
	return revenue
}

// revenueDiscountReadFailed 表示这份汇总的折扣率读取失败、折扣未知；写快照的路径遇到它必须跳过本轮。
func revenueDiscountReadFailed(summary *AdditionalCostSummary) bool {
	return summary != nil && summary.revenueDiscountReadFailed
}

func revenueDiscountSummary(summary *AdditionalCostSummary) (*float64, *float64) {
	if summary == nil {
		return nil, nil
	}
	return summary.RevenueDiscount, summary.RevenueDiscountRate
}

func (s *MetricsService) revenueDiscountRepository() (revenueDiscountRateRepository, bool) {
	repository, ok := s.additionalCosts.(revenueDiscountRateRepository)
	return repository, ok
}

// revenueDiscountRule 返回 date 当天生效的折扣规则；没有规则或仓储不支持时返回 nil。
func (s *MetricsService) revenueDiscountRule(ctx context.Context, userID, adminAccountID, date string) (*RevenueDiscountRate, error) {
	repository, ok := s.revenueDiscountRepository()
	if !ok {
		return nil, nil
	}
	return repository.GetRevenueDiscountRate(ctx, userID, adminAccountID, date)
}

func (s *MetricsService) ListRevenueDiscountRates(ctx context.Context, userID string) ([]RevenueDiscountRate, error) {
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	repository, ok := s.revenueDiscountRepository()
	if !ok {
		return []RevenueDiscountRate{}, nil
	}
	items, err := repository.ListRevenueDiscountRates(ctx, userID, adminAccountID)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]RevenueDiscountRate, 0)
	}
	return items, nil
}

func (s *MetricsService) SaveRevenueDiscountRate(ctx context.Context, userID string, input RevenueDiscountRateInput) (RevenueDiscountRate, error) {
	repository, ok := s.revenueDiscountRepository()
	if !ok {
		return RevenueDiscountRate{}, errors.New("dashboard.additionalCost.errors.unavailable")
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return RevenueDiscountRate{}, err
	}
	input, err = normalizeRevenueDiscountInput(input, businesstime.DateAt(s.currentTime()))
	if err != nil {
		return RevenueDiscountRate{}, err
	}
	rate := RevenueDiscountRate{
		ID: mustMetricsID(), UserID: userID, AdminAccountID: adminAccountID, EffectiveDate: input.EffectiveDate,
		GiftRate: input.GiftRate, NewUserRate: input.NewUserRate, ReferralRate: input.ReferralRate,
		TotalRate: revenueDiscountTotal(input.GiftRate, input.NewUserRate, input.ReferralRate), CreatedAt: time.Now(),
	}
	if err := repository.SaveRevenueDiscountRate(ctx, rate); err != nil {
		return RevenueDiscountRate{}, err
	}
	return rate, nil
}

func (s *MetricsService) DeleteRevenueDiscountRate(ctx context.Context, userID, id string) error {
	repository, ok := s.revenueDiscountRepository()
	if !ok {
		return errors.New("dashboard.additionalCost.errors.unavailable")
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return ErrRevenueDiscountNotFound
	}
	return repository.DeleteRevenueDiscountRate(ctx, userID, adminAccountID, id, businesstime.DateAt(s.currentTime()))
}

func (s *MetricsService) DeleteRechargeFeeRate(ctx context.Context, userID, id string) error {
	repository, ok := s.additionalCosts.(rechargeFeeRateDeleteRepository)
	if !ok {
		return errors.New("dashboard.additionalCost.errors.unavailable")
	}
	adminAccountID, err := s.requireCurrentAdminAccount(ctx, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return ErrRechargeFeeRateNotFound
	}
	return repository.DeleteRechargeFeeRate(ctx, userID, adminAccountID, id)
}

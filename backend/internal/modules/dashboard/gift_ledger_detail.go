package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

const revenueDiscountDetailVersion = 1

// RevenueDiscountCustomer 是快照明细里一位单独客户当天的结果；整数字段用于重放和冻结，不能改成元。
type RevenueDiscountCustomer struct {
	MainUserID        string `json:"mainUserId"`
	Email             string `json:"email"`
	ConsumptionMicros int64  `json:"consumptionMicros"`
	CoveredMicros     int64  `json:"coveredMicros"`
	UncoveredMicros   int64  `json:"uncoveredMicros"`
	GiftCents         int64  `json:"giftCents"`
	LateProtected     bool   `json:"lateProtected"`
}

// RevenueDiscountDetail 保存在 dashboard_daily_stats.revenue_discount_detail。
// Customers 保留读出的原始 JSON：冻结时原样写回，写入保护按 customers 相等比较。
type RevenueDiscountDetail struct {
	Version         int                       `json:"version"`
	GeneralRate     *float64                  `json:"generalRate"`
	GeneralBase     float64                   `json:"generalBase"`
	GeneralDiscount float64                   `json:"generalDiscount"`
	LedgerCovered   float64                   `json:"ledgerCovered"`
	LedgerGift      float64                   `json:"ledgerGift"`
	CustomersRaw    json.RawMessage           `json:"customers"`
	Customers       []RevenueDiscountCustomer `json:"-"`
}

var errRevenueDiscountDetailBroken = errors.New("revenue discount detail broken")

func parseRevenueDiscountDetail(raw []byte) (*RevenueDiscountDetail, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var detail RevenueDiscountDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, errRevenueDiscountDetailBroken
	}
	if detail.Version != revenueDiscountDetailVersion || len(detail.CustomersRaw) == 0 {
		return nil, errRevenueDiscountDetailBroken
	}
	// 必需字段缺失或为 null（Go 会把 null 解成 0）、金额为负、拆分对不上，都按损坏处理：
	// 冻结会原样沿用这些数字，宁可报错不写，也不能把已结算金额改成别的数。
	var rows []struct {
		MainUserID        *string `json:"mainUserId"`
		ConsumptionMicros *int64  `json:"consumptionMicros"`
		CoveredMicros     *int64  `json:"coveredMicros"`
		UncoveredMicros   *int64  `json:"uncoveredMicros"`
		GiftCents         *int64  `json:"giftCents"`
		LateProtected     *bool   `json:"lateProtected"`
	}
	if err := json.Unmarshal(detail.CustomersRaw, &rows); err != nil || len(rows) == 0 {
		return nil, errRevenueDiscountDetailBroken
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.MainUserID == nil || row.ConsumptionMicros == nil || row.CoveredMicros == nil || row.UncoveredMicros == nil || row.GiftCents == nil || row.LateProtected == nil {
			return nil, errRevenueDiscountDetailBroken
		}
		// 只接受本系统写出的规范写法（无前导 0、无空格），保证“同一客户”只有一种写法，去重才可靠。
		id, err := parseGiftLedgerMainUserID(*row.MainUserID)
		if err != nil || *row.MainUserID != strconv.FormatInt(id, 10) || seen[*row.MainUserID] {
			return nil, errRevenueDiscountDetailBroken
		}
		seen[*row.MainUserID] = true
		consumption, covered, uncovered, gift := *row.ConsumptionMicros, *row.CoveredMicros, *row.UncoveredMicros, *row.GiftCents
		if consumption < 0 || covered < 0 || uncovered < 0 || gift < 0 || covered > consumption || covered+uncovered != consumption {
			return nil, errRevenueDiscountDetailBroken
		}
		if *row.LateProtected && (covered != 0 || gift != 0) {
			return nil, errRevenueDiscountDetailBroken
		}
	}
	if err := json.Unmarshal(detail.CustomersRaw, &detail.Customers); err != nil {
		return nil, errRevenueDiscountDetailBroken
	}
	return &detail, nil
}

func (d *RevenueDiscountDetail) marshal() ([]byte, error) {
	if d == nil {
		return nil, nil
	}
	if len(d.CustomersRaw) == 0 {
		raw, err := json.Marshal(d.Customers)
		if err != nil {
			return nil, err
		}
		d.CustomersRaw = raw
	}
	return json.Marshal(d)
}

type giftLedgerDayResult struct {
	Customers         []RevenueDiscountCustomer
	CustomersRaw      json.RawMessage // 冻结时来自已存明细，原样写回
	CoveredMicros     int64
	GiftCents         int64
	ConsumptionMicros int64
	// 现算时才有：每位客户的逐笔结果和排好序的记录，供状态页显示。
	customerResults map[int64]giftLedgerCustomerResult
	records         map[int64][]GiftLedgerRecord
}

func giftLedgerResultFromDetail(detail *RevenueDiscountDetail) *giftLedgerDayResult {
	if detail == nil {
		return nil
	}
	result := &giftLedgerDayResult{Customers: detail.Customers, CustomersRaw: detail.CustomersRaw}
	for _, c := range detail.Customers {
		result.CoveredMicros += c.CoveredMicros
		result.GiftCents += c.GiftCents
		result.ConsumptionMicros += c.ConsumptionMicros
	}
	return result
}

func microsToAmount(m int64) float64 { return float64(m) / 1e6 }

// applyRevenueDiscountWithLedger 按 SPEC 6.4 计算：整站折扣 = round((R − 覆盖) × 整站率)，所有日期基数下限 0；
// strict（过去日期）时客户消费合计超过 R + 0.01 返回原因。
func applyRevenueDiscountWithLedger(summary *AdditionalCostSummary, revenue *float64, rule *RevenueDiscountRate, ledger *giftLedgerDayResult, strict bool, date string) (reason string) {
	summary.RevenueDiscount, summary.RevenueDiscountRate, summary.NetRevenue, summary.RevenueDiscountDetail = nil, nil, nil, nil
	if revenue == nil || ledger == nil {
		return ""
	}
	r := *revenue
	consumption := microsToAmount(ledger.ConsumptionMicros)
	if strict && consumption > r+0.01+1e-9 {
		return fmt.Sprintf("%s 主站总消费 %.2f 小于单独客户这一天的消费合计 %.2f，数据不一致（可能主站已不保留这一天的明细），没有保存这一天的数字", date, r, consumption)
	}
	covered := microsToAmount(ledger.CoveredMicros)
	base := math.Max(r-covered, 0)
	var generalCents int64
	var rate *float64
	if rule != nil {
		total := rule.TotalRate
		rate = &total
		generalCents = cents(base * total)
	}
	discount := float64(generalCents+ledger.GiftCents) / 100
	net := r - discount
	summary.RevenueDiscount = &discount
	summary.RevenueDiscountRate = rate
	summary.NetRevenue = &net
	summary.RevenueDiscountRule = rule
	summary.RevenueDiscountDetail = &RevenueDiscountDetail{
		Version: revenueDiscountDetailVersion, GeneralRate: rate,
		GeneralBase: math.Round(base*1e6) / 1e6, GeneralDiscount: float64(generalCents) / 100,
		LedgerCovered: covered, LedgerGift: float64(ledger.GiftCents) / 100,
		CustomersRaw: ledger.CustomersRaw, Customers: ledger.Customers,
	}
	return ""
}

// nullableJSON 把空明细写成 NULL，其余原样写入 jsonb。
func nullableJSON(raw []byte) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

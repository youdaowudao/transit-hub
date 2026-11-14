package dashboard

import (
	"math"
	"math/big"
	"sort"
	"time"
)

// 单独客户赠送记录的纯计算：不读数据库、不调主站。
// 位置用整数微单位（1/1,000,000）：消费 = round(元 × 1e6)，到账 = 分 × 1e4。

const giftLedgerMicrosPerCent = 10000

// GiftLedgerRecord 是一位客户的一笔充值记录（到账 = 实付 + 赠送）。
type GiftLedgerRecord struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	AdminAccountID string    `json:"-"`
	MainUserID     int64     `json:"mainUserId,string"`
	MainUserEmail  string    `json:"email"`
	RechargeDate   string    `json:"rechargeDate"`
	PaidCents      int64     `json:"-"`
	GiftCents      int64     `json:"-"`
	Note           string    `json:"note"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (r GiftLedgerRecord) creditMicros() int64 {
	return (r.PaidCents + r.GiftCents) * giftLedgerMicrosPerCent
}

// sortGiftLedgerRecords 按先进先出顺序排列：充值日期 → 录入时间 → 编号。
func sortGiftLedgerRecords(records []GiftLedgerRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if a.RechargeDate != b.RechargeDate {
			return a.RechargeDate < b.RechargeDate
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

func giftLedgerMicros(amount float64) int64 {
	return int64(math.Round(amount * 1e6))
}

type giftLedgerDayInput struct {
	Date              string
	ConsumptionMicros int64
}

const (
	giftLedgerStateUnused = "unused"
	giftLedgerStateUsing  = "using"
	giftLedgerStateUsedUp = "used_up"
)

type giftLedgerRecordUse struct {
	RecordID   string
	UsedMicros int64
	State      string
}

type giftLedgerCustomerResult struct {
	MainUserID        int64
	Email             string
	ConsumptionMicros int64
	CoveredMicros     int64
	UncoveredMicros   int64
	GiftCents         int64
	LateProtected     bool
	CreditedMicros    int64
	Records           []giftLedgerRecordUse
}

// giftLedgerFlow 把 amount 按顺序流入充值日期不晚于 day 的记录，每笔最多流到到账；返回流不进的部分。
func giftLedgerFlow(amount int64, records []GiftLedgerRecord, used []int64, day string) int64 {
	for i := range records {
		if amount <= 0 {
			break
		}
		if records[i].RechargeDate > day {
			continue
		}
		room := records[i].creditMicros() - used[i]
		if room <= 0 {
			continue
		}
		take := room
		if amount < take {
			take = amount
		}
		used[i] += take
		amount -= take
	}
	return amount
}

// giftLedgerRecordGiftCents = round_half_up(used × 赠送分 ÷ 到账微单位)，单位为分。
func giftLedgerRecordGiftCents(usedMicros int64, record GiftLedgerRecord) int64 {
	credit := record.creditMicros()
	if usedMicros <= 0 || record.GiftCents <= 0 || credit <= 0 {
		return 0
	}
	num := new(big.Int).Mul(big.NewInt(usedMicros), big.NewInt(record.GiftCents))
	num.Mul(num, big.NewInt(2))
	den := big.NewInt(credit)
	num.Add(num, den)
	den2 := new(big.Int).Mul(den, big.NewInt(2))
	return new(big.Int).Quo(num, den2).Int64()
}

// giftLedgerCustomerDay 计算一位客户在 target 这一天的结果。
// records 必须已按先进先出排好，且只含充值日期 ≤ target.Date 的记录；
// history 为最早充值日期到 target 前一天的每一天（消费为 0 的天可以省略）。
func giftLedgerCustomerDay(records []GiftLedgerRecord, history []giftLedgerDayInput, target giftLedgerDayInput, lateProtected bool) giftLedgerCustomerResult {
	used := make([]int64, len(records))
	for _, day := range history {
		giftLedgerFlow(day.ConsumptionMicros, records, used, day.Date)
	}
	before := append([]int64(nil), used...)
	left := giftLedgerFlow(target.ConsumptionMicros, records, used, target.Date)
	result := giftLedgerCustomerResult{
		ConsumptionMicros: target.ConsumptionMicros,
		CoveredMicros:     target.ConsumptionMicros - left,
		LateProtected:     lateProtected,
		Records:           make([]giftLedgerRecordUse, len(records)),
	}
	if len(records) > 0 {
		result.MainUserID = records[0].MainUserID
		result.Email = records[0].MainUserEmail
	}
	for i, record := range records {
		result.CreditedMicros += record.creditMicros()
		if !lateProtected {
			result.GiftCents += giftLedgerRecordGiftCents(used[i], record) - giftLedgerRecordGiftCents(before[i], record)
		}
		state := giftLedgerStateUsing
		switch {
		case used[i] <= 0:
			state = giftLedgerStateUnused
		case used[i] >= record.creditMicros():
			state = giftLedgerStateUsedUp
		}
		result.Records[i] = giftLedgerRecordUse{RecordID: record.ID, UsedMicros: used[i], State: state}
	}
	if lateProtected {
		result.CoveredMicros = 0
	}
	result.UncoveredMicros = result.ConsumptionMicros - result.CoveredMicros
	return result
}

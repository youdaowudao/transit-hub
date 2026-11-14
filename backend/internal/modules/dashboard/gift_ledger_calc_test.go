package dashboard

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func giftDay(n int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n).Format("2006-01-02")
}

func giftRecord(id string, day int, paid, gift int64, seq int) GiftLedgerRecord {
	return GiftLedgerRecord{
		ID: id, MainUserID: 42, MainUserEmail: "a@example.com", RechargeDate: giftDay(day),
		PaidCents: paid * 100, GiftCents: gift * 100,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, seq, 0, time.UTC),
	}
}

// giftRun 逐日计算 days 中每一天（按顺序）的结果；spend[day] 为当天消费（元）。
func giftRun(records []GiftLedgerRecord, spend map[int]float64, target int) giftLedgerCustomerResult {
	sortGiftLedgerRecords(records)
	eligible := make([]GiftLedgerRecord, 0, len(records))
	for _, r := range records {
		if r.RechargeDate <= giftDay(target) {
			eligible = append(eligible, r)
		}
	}
	history := []giftLedgerDayInput{}
	if len(eligible) > 0 {
		for d := 0; d < target; d++ {
			if giftDay(d) < eligible[0].RechargeDate {
				continue
			}
			history = append(history, giftLedgerDayInput{Date: giftDay(d), ConsumptionMicros: giftLedgerMicros(spend[d])})
		}
	}
	return giftLedgerCustomerDay(eligible, history, giftLedgerDayInput{Date: giftDay(target), ConsumptionMicros: giftLedgerMicros(spend[target])}, false)
}

func TestGiftLedgerCustomerDayScenarios(t *testing.T) {
	t.Run("S1", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r1", 1, 1000, 200, 1), giftRecord("r2", 10, 500, 50, 2)}, map[int]float64{5: 1150, 20: 100}, 20)
		if got.GiftCents != 1288 || got.CoveredMicros != 100e6 || got.UncoveredMicros != 0 {
			t.Fatalf("S1 got %+v", got)
		}
	})
	t.Run("S2", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r1", 1, 1000, 200, 1), giftRecord("r2", 10, 500, 50, 2)}, map[int]float64{5: 1150, 15: 550, 20: 100}, 20)
		if got.GiftCents != 455 || got.CoveredMicros != 50e6 || got.UncoveredMicros != 50e6 {
			t.Fatalf("S2 got %+v", got)
		}
	})
	t.Run("S3", func(t *testing.T) {
		records := []GiftLedgerRecord{giftRecord("r1", 1, 100, 20, 1), giftRecord("r2", 6, 100, 20, 2)}
		spend := map[int]float64{}
		for d := 1; d <= 8; d++ {
			spend[d] = 50
		}
		want := []int64{833, 834, 333, 0, 0, 833, 834, 333}
		for d := 1; d <= 8; d++ {
			got := giftRun(append([]GiftLedgerRecord(nil), records...), spend, d)
			if got.GiftCents != want[d-1] {
				t.Fatalf("S3 day %d gift %d want %d", d, got.GiftCents, want[d-1])
			}
		}
		if got := giftRun(append([]GiftLedgerRecord(nil), records...), spend, 3); got.UncoveredMicros != 30e6 {
			t.Fatalf("S3 day 3 uncovered %d", got.UncoveredMicros)
		}
	})
	t.Run("S4", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r", 0, 100, 20, 1)}, map[int]float64{3: 80, 25: 30}, 25)
		if got.GiftCents != 500 || got.CoveredMicros != 30e6 {
			t.Fatalf("S4 got %+v", got)
		}
	})
	t.Run("S10", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r1", 1, 100, 25, 1), giftRecord("r2", 2, 100, 20, 2)}, map[int]float64{1: 120, 2: 120, 3: 5}, 3)
		if got.GiftCents != 83 {
			t.Fatalf("S10 got %+v", got)
		}
	})
	t.Run("S11", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r2", 2, 100, 20, 2)}, map[int]float64{2: 120, 3: 50}, 3)
		if got.GiftCents != 0 || got.CoveredMicros != 0 || got.UncoveredMicros != 50e6 {
			t.Fatalf("S11 got %+v", got)
		}
	})
}

func TestGiftLedgerCustomerDayMoreScenarios(t *testing.T) {
	t.Run("S5 backdated record already used up", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r", 0, 100, 20, 1)}, map[int]float64{2: 300, 5: 200, 9: 40}, 9)
		if got.CoveredMicros != 0 || got.UncoveredMicros != 40e6 || got.GiftCents != 0 || got.Records[0].State != giftLedgerStateUsedUp {
			t.Fatalf("S5 got %+v", got)
		}
	})
	t.Run("S6 same day uses the earlier entry first", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("later", 1, 100, 0, 2), giftRecord("earlier", 1, 100, 100, 1)}, map[int]float64{1: 50}, 1)
		// earlier 先用：50 × 100/200 = 25.00
		if got.GiftCents != 2500 || got.Records[0].RecordID != "earlier" || got.Records[0].UsedMicros != 50e6 || got.Records[1].UsedMicros != 0 {
			t.Fatalf("S6 got %+v", got)
		}
	})
	t.Run("S7 gift only", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r", 1, 0, 30, 1)}, map[int]float64{1: 30}, 1)
		if got.GiftCents != 3000 || got.CoveredMicros != 30e6 {
			t.Fatalf("S7 got %+v", got)
		}
	})
	t.Run("S8 paid only still counts as covered", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("r", 1, 50, 0, 1)}, map[int]float64{1: 50}, 1)
		if got.GiftCents != 0 || got.CoveredMicros != 50e6 || got.UncoveredMicros != 0 {
			t.Fatalf("S8 got %+v", got)
		}
	})
	t.Run("S14 moving a record later replays by the new date", func(t *testing.T) {
		moved := giftRun([]GiftLedgerRecord{giftRecord("r1", 1, 1000, 200, 1), giftRecord("r2", 25, 500, 50, 2)}, map[int]float64{5: 1150, 20: 100, 26: 100}, 26)
		// 第20天的 100 只能用 r1 剩余 50，另 50 未覆盖；r2 从第25天起可用：第26天 100 → r2 0→100
		if moved.GiftCents != 909 || moved.CoveredMicros != 100e6 {
			t.Fatalf("S14 got %+v", moved)
		}
	})
	t.Run("record states", func(t *testing.T) {
		got := giftRun([]GiftLedgerRecord{giftRecord("a", 1, 100, 20, 1), giftRecord("b", 1, 100, 20, 2), giftRecord("c", 1, 100, 20, 3)}, map[int]float64{1: 150}, 1)
		if got.Records[0].State != giftLedgerStateUsedUp || got.Records[1].State != giftLedgerStateUsing || got.Records[2].State != giftLedgerStateUnused {
			t.Fatalf("states got %+v", got.Records)
		}
	})
}

func TestGiftLedgerLateProtection(t *testing.T) {
	records := []GiftLedgerRecord{giftRecord("r", 1, 100, 20, 1)}
	history := []giftLedgerDayInput{{Date: giftDay(1), ConsumptionMicros: 30e6}}
	normal := giftLedgerCustomerDay(records, history, giftLedgerDayInput{Date: giftDay(2), ConsumptionMicros: 30e6}, false)
	late := giftLedgerCustomerDay(records, history, giftLedgerDayInput{Date: giftDay(2), ConsumptionMicros: 30e6}, true)
	if normal.GiftCents != 500 || normal.CoveredMicros != 30e6 {
		t.Fatalf("normal got %+v", normal)
	}
	if late.GiftCents != 0 || late.CoveredMicros != 0 || late.UncoveredMicros != 30e6 || !late.LateProtected {
		t.Fatalf("late got %+v", late)
	}
	// 晚结保护照常占用额度：第二天的开始与不保护时相同。
	if late.Records[0].UsedMicros != normal.Records[0].UsedMicros {
		t.Fatalf("late used %d, normal used %d", late.Records[0].UsedMicros, normal.Records[0].UsedMicros)
	}
	// S13：第2天晚结保护，第1、3、4天各扣 5，共 15。
	spend := []giftLedgerDayInput{{giftDay(1), 30e6}, {giftDay(2), 30e6}, {giftDay(3), 30e6}, {giftDay(4), 30e6}}
	var total int64
	for i := range spend {
		total += giftLedgerCustomerDay(records, spend[:i], spend[i], i == 1).GiftCents
	}
	if total != 1500 {
		t.Fatalf("S13 total %d, want 1500", total)
	}
}

func TestGiftLedgerRecordGiftRounding(t *testing.T) {
	huge := GiftLedgerRecord{PaidCents: 0, GiftCents: 1_000_000_000}
	if got := giftLedgerRecordGiftCents(huge.creditMicros(), huge); got != 1_000_000_000 {
		t.Fatalf("huge used up gift = %d", got)
	}
	if got := giftLedgerRecordGiftCents(huge.creditMicros()-1, huge); got != 1_000_000_000 {
		t.Fatalf("huge almost used gift = %d", got)
	}
	half := GiftLedgerRecord{PaidCents: 100, GiftCents: 1} // 到账 1.01，赠送 0.01
	// used 0.505 → 0.505 × 1 / 1.01 = 0.5 分，向上取整为 1 分
	if got := giftLedgerRecordGiftCents(505000, half); got != 1 {
		t.Fatalf("half cent rounds up: %d", got)
	}
	noGift := GiftLedgerRecord{PaidCents: 100}
	if got := giftLedgerRecordGiftCents(1e6, noGift); got != 0 {
		t.Fatalf("no gift = %d", got)
	}
}

func TestGiftLedgerRandomTelescopes(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	records := []GiftLedgerRecord{}
	for i := 0; i < 4; i++ {
		records = append(records, giftRecord(fmt.Sprintf("r%d", i), rng.Intn(30), int64(50+rng.Intn(500)), int64(rng.Intn(100)), i))
	}
	spend := map[int]float64{}
	for d := 0; d < 40; d++ {
		spend[d] = float64(rng.Intn(20000)) / 100
	}
	var sum int64
	var last giftLedgerCustomerResult
	for d := 0; d < 40; d++ {
		last = giftRun(append([]GiftLedgerRecord(nil), records...), spend, d)
		sum += last.GiftCents
	}
	sortGiftLedgerRecords(records)
	var want, giftTotal int64
	for i, r := range records {
		want += giftLedgerRecordGiftCents(last.Records[i].UsedMicros, r)
		giftTotal += r.GiftCents
	}
	if sum != want || sum > giftTotal {
		t.Fatalf("sum %d want %d total %d", sum, want, giftTotal)
	}
}

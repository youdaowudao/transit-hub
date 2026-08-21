package dashboard

import (
	"math"
	"testing"
)

func TestAccountPerformanceKeepsAverageMultiplierRecoveryAndFinalProfitDistinct(t *testing.T) {
	active := calculateAccountPerformance(AccountPerformanceInput{
		Status: AccountStatusActive, AccountingMode: AccountingModeAdditive,
		PurchaseCostCents: 10_000, AdditiveUpstreamCostCents: 2_000,
		RevenueCents: 8_000, RefundCents: 1_000, RawQuotaUsedMicros: 40_000_000,
		HasAdditiveUpstreamCost: true, HasRevenue: true, HasRawQuotaUsed: true,
	})
	assertFloatPointer(t, "active average multiplier", active.AverageSaleMultiplier, 2)
	assertFloatPointer(t, "active recovery multiple", active.CostRecoveryMultiple, 0.75)
	if active.BreakevenDifferenceCents == nil || *active.BreakevenDifferenceCents != -3_000 {
		t.Fatalf("active breakeven difference = %v, want -3000", active.BreakevenDifferenceCents)
	}
	if active.FinalProfitCents != nil || active.ROI != nil {
		t.Fatalf("active account must not expose final result: %#v", active)
	}

	terminal := calculateAccountPerformance(AccountPerformanceInput{
		Status: AccountStatusDead, AccountingMode: AccountingModeReplace,
		PurchaseCostCents: 10_000, AdditiveUpstreamCostCents: 9_999,
		RevenueCents: 13_000, RawQuotaUsedMicros: 5_000_000,
		HasRevenue: true, HasRawQuotaUsed: true,
	})
	assertFloatPointer(t, "terminal recovery multiple", terminal.CostRecoveryMultiple, 1.3)
	if terminal.BreakevenDifferenceCents == nil || *terminal.BreakevenDifferenceCents != 3_000 || terminal.FinalProfitCents == nil || *terminal.FinalProfitCents != 3_000 {
		t.Fatalf("terminal result = %#v, want final profit 3000", terminal)
	}
	assertFloatPointer(t, "terminal ROI", terminal.ROI, 0.3)
}

func TestAccountPerformanceRefundSignIsAppliedOnceAndMissingDenominatorsStayUnavailable(t *testing.T) {
	result := calculateAccountPerformance(AccountPerformanceInput{
		Status: AccountStatusClosed, AccountingMode: AccountingModeReplace,
		PurchaseCostCents: 10_000, RevenueCents: 8_000, RefundCents: 3_000,
		HasRevenue: true,
	})
	assertFloatPointer(t, "refund recovery multiple", result.CostRecoveryMultiple, 1.1)
	if result.FinalProfitCents == nil || *result.FinalProfitCents != 1_000 {
		t.Fatalf("final profit = %v, want 1000", result.FinalProfitCents)
	}
	assertFloatPointer(t, "refund ROI", result.ROI, float64(1_000)/float64(7_000))
	if result.AverageSaleMultiplier != nil {
		t.Fatalf("missing raw quota must not become 0x: %#v", result.AverageSaleMultiplier)
	}
	if len(result.MissingFields) != 1 || result.MissingFields[0] != "quotaUsed" {
		t.Fatalf("missing fields = %#v, want quotaUsed", result.MissingFields)
	}

	zeroInvestment := calculateAccountPerformance(AccountPerformanceInput{
		Status: AccountStatusClosed, AccountingMode: AccountingModeReplace,
		PurchaseCostCents: 1_000, RefundCents: 1_000,
	})
	if zeroInvestment.ROI != nil {
		t.Fatalf("zero net investment must make ROI unavailable: %#v", zeroInvestment.ROI)
	}
	if zeroInvestment.BreakevenDifferenceCents != nil || zeroInvestment.FinalProfitCents != nil {
		t.Fatalf("missing revenue produced a false zero result: %#v", zeroInvestment)
	}
}

func assertFloatPointer(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

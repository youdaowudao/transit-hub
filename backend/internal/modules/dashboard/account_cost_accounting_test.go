package dashboard

import (
	"reflect"
	"testing"
)

func TestAccountCostDailyAllocationUsesBusinessDatesAndConservesCents(t *testing.T) {
	got, err := allocateDailyCost(1000, "2026-08-22", 3)
	if err != nil {
		t.Fatalf("allocateDailyCost() error: %v", err)
	}
	want := []DatedAmount{
		{BusinessDate: "2026-08-22", AmountCents: 333},
		{BusinessDate: "2026-08-23", AmountCents: 333},
		{BusinessDate: "2026-08-24", AmountCents: 334},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allocateDailyCost() = %#v, want %#v", got, want)
	}

	for _, tt := range []struct {
		name string
		date string
		days int
	}{
		{name: "invalid date", date: "2026-02-30", days: 3},
		{name: "zero days", date: "2026-08-22", days: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := allocateDailyCost(1000, tt.date, tt.days); err == nil {
				t.Fatal("allocateDailyCost() expected an error")
			}
		})
	}
}

func TestAccountCostQuotaRecognitionUsesCumulativeDifferenceAndFinalRemainder(t *testing.T) {
	tests := []struct {
		name               string
		purchaseCents      int64
		totalQuotaMicros   int64
		cumulativeUsed     int64
		previousRecognized int64
		terminal           bool
		want               int64
		wantErr            bool
	}{
		{name: "first third", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 1_000_000, want: 333},
		{name: "second third uses cumulative difference", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 2_000_000, previousRecognized: 333, want: 334},
		{name: "overuse is capped", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 4_000_000, previousRecognized: 667, want: 334},
		{name: "terminal writes off remainder", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 2_500_000, previousRecognized: 667, terminal: true, want: 334},
		{name: "missing total quota rejected", purchaseCents: 1001, cumulativeUsed: 1, wantErr: true},
		{name: "recognized exceeds purchase rejected", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 2_000_000, previousRecognized: 1002, wantErr: true},
		{name: "cumulative use cannot imply less than already recognized", purchaseCents: 1001, totalQuotaMicros: 3_000_000, cumulativeUsed: 1_000_000, previousRecognized: 667, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recognizeQuotaCost(tt.purchaseCents, tt.totalQuotaMicros, tt.cumulativeUsed, tt.previousRecognized, tt.terminal)
			if tt.wantErr {
				if err == nil {
					t.Fatal("recognizeQuotaCost() expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("recognizeQuotaCost() error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("recognizeQuotaCost() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAccountCostTerminalAdjustmentsCancelFutureScheduleAndWriteOffRemainder(t *testing.T) {
	daily, err := terminalCostAdjustments(1000, "2026-08-23", []DatedAmount{
		{BusinessDate: "2026-08-22", AmountCents: 333},
		{BusinessDate: "2026-08-23", AmountCents: 333},
		{BusinessDate: "2026-08-24", AmountCents: 334},
	})
	if err != nil {
		t.Fatalf("daily terminalCostAdjustments() error: %v", err)
	}
	wantDaily := []DatedAmount{
		{BusinessDate: "2026-08-24", AmountCents: -334},
		{BusinessDate: "2026-08-23", AmountCents: 334},
	}
	if !reflect.DeepEqual(daily, wantDaily) {
		t.Fatalf("daily adjustments = %#v, want %#v", daily, wantDaily)
	}

	quota, err := terminalCostAdjustments(1001, "2026-08-23", []DatedAmount{
		{BusinessDate: "2026-08-22", AmountCents: 333},
		{BusinessDate: "2026-08-23", AmountCents: 334},
	})
	if err != nil {
		t.Fatalf("quota terminalCostAdjustments() error: %v", err)
	}
	wantQuota := []DatedAmount{{BusinessDate: "2026-08-23", AmountCents: 334}}
	if !reflect.DeepEqual(quota, wantQuota) {
		t.Fatalf("quota adjustments = %#v, want %#v", quota, wantQuota)
	}
}

func TestOperatingCostSeparatesReplacementAndAdditiveAccountCosts(t *testing.T) {
	tests := []struct {
		name      string
		input     OperatingCostInput
		want      int64
		available bool
	}{
		{
			name:  "no account assets preserves old formula",
			input: OperatingCostInput{UpstreamDirectCostCents: 10_000, OtherCostCents: 1_500},
			want:  11_500, available: true,
		},
		{
			name:  "additive keeps direct upstream and purchase cost",
			input: OperatingCostInput{UpstreamDirectCostCents: 10_000, AccountPurchaseCostCents: 4_000, OtherCostCents: 1_500},
			want:  15_500, available: true,
		},
		{
			name:  "replacement deducts only reconciled linked upstream cost",
			input: OperatingCostInput{UpstreamDirectCostCents: 10_000, ReplacementDeductionCents: int64Pointer(3_000), AccountPurchaseCostCents: 4_000, OtherCostCents: 1_500},
			want:  12_500, available: true,
		},
		{
			name:      "required replacement deduction cannot silently become zero",
			input:     OperatingCostInput{UpstreamDirectCostCents: 10_000, RequiresReplacementDeduction: true, AccountPurchaseCostCents: 4_000, OtherCostCents: 1_500},
			available: false,
		},
		{
			name:      "deduction above upstream direct cost is rejected",
			input:     OperatingCostInput{UpstreamDirectCostCents: 10_000, ReplacementDeductionCents: int64Pointer(10_001), AccountPurchaseCostCents: 4_000},
			available: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, available := calculateOperatingCost(tt.input)
			if available != tt.available || available && got != tt.want {
				t.Fatalf("calculateOperatingCost() = (%d,%v), want (%d,%v)", got, available, tt.want, tt.available)
			}
		})
	}
}

func TestOperatingCostProjectionUsesLedgerTotalWithoutCountingAccountPurchaseTwice(t *testing.T) {
	additionalTotal := 55.0
	summary := &AdditionalCostSummary{
		AccountPurchase: 40, AccountRefund: -5, Fixed: 20, Total: &additionalTotal, Available: true,
	}
	deduction := int64(3000)
	reconciled := int64(10_000)
	operating, profit, margin := projectOperatingCost(
		ptrF64(100), ptrF64(200), summary,
		AccountCostComponents{
			AccountPurchaseCostCents: 4000, ReplacementDeductionCents: &deduction,
			RequiresReplacementDeduction: true, ReconciledUpstreamDirectCostCents: &reconciled,
		}, nil,
	)
	assertFloatPointer(t, "operating cost", operating, 125)
	assertFloatPointer(t, "adjusted profit", profit, 75)
	assertFloatPointer(t, "adjusted margin", margin, 37.5)
	if summary.ReplacementDeduction == nil || *summary.ReplacementDeduction != 30 || summary.AccountQuality != KeyCostQualityComplete {
		t.Fatalf("summary quality = %#v", summary)
	}

	unavailableSummary := &AdditionalCostSummary{AccountPurchase: 40, Total: &additionalTotal, Available: true}
	operating, profit, margin = projectOperatingCost(ptrF64(100), ptrF64(200), unavailableSummary, AccountCostComponents{
		AccountPurchaseCostCents: 4000, RequiresReplacementDeduction: true,
	}, nil)
	if operating != nil || profit != nil || margin != nil || unavailableSummary.AccountQuality != KeyCostQualityMissing {
		t.Fatalf("missing deduction projection = operating %v profit %v margin %v summary %#v", operating, profit, margin, unavailableSummary)
	}
}

func TestOperatingCostProjectionRejectsDirectCostFromAnotherSnapshotRun(t *testing.T) {
	deduction := int64(3000)
	reconciled := int64(10_000)
	summary := &AdditionalCostSummary{Total: ptrF64(60), AccountPurchase: 40}

	operating, profit, margin := projectOperatingCost(
		ptrF64(101), ptrF64(200), summary,
		AccountCostComponents{
			AccountPurchaseCostCents:          4000,
			ReplacementDeductionCents:         &deduction,
			RequiresReplacementDeduction:      true,
			ReconciledUpstreamDirectCostCents: &reconciled,
			SnapshotRunID:                     "snapshot-1",
		}, nil,
	)
	if operating != nil || profit != nil || margin != nil || summary.AccountQuality != KeyCostQualityMissing {
		t.Fatalf("mixed-run costs must be unavailable: operating=%v profit=%v margin=%v summary=%#v", operating, profit, margin, summary)
	}
}

package dashboard

import (
	"context"
	"errors"
	"testing"
)

type fakeAdditionalCostRepository struct {
	rate          RechargeFeeRate
	items         []AdditionalCostRecord
	updatedSource string
	updatedUser   string
	updatedAdmin  string
	updatedInput  AdditionalCostInput
	getErr        error
	updateErr     error
}

func (f *fakeAdditionalCostRepository) GetRechargeFeeRate(context.Context, string, string, string) (RechargeFeeRate, error) {
	return f.rate, nil
}

func (f *fakeAdditionalCostRepository) ListAdditionalCosts(context.Context, string, string, string, string) ([]AdditionalCostRecord, error) {
	return f.items, nil
}

func (f *fakeAdditionalCostRepository) GetAdditionalCost(context.Context, string, string, string) ([]AdditionalCostRecord, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, item := range f.items {
		if err := validateEditableAdditionalCostRecord(item); err != nil {
			return nil, err
		}
	}
	return f.items, nil
}

func (f *fakeAdditionalCostRepository) SaveRechargeFeeRate(context.Context, RechargeFeeRate) error {
	return nil
}

func (f *fakeAdditionalCostRepository) InsertAdditionalCosts(context.Context, []AdditionalCostRecord) error {
	return nil
}

func (f *fakeAdditionalCostRepository) ReplaceAdditionalCost(_ context.Context, userID, adminAccountID, sourceID string, input AdditionalCostInput) ([]AdditionalCostRecord, error) {
	f.updatedUser, f.updatedAdmin, f.updatedSource, f.updatedInput = userID, adminAccountID, sourceID, input
	return f.items, f.updateErr
}

func TestBuildPromotionAdditionalCostUsesConfiguredUsageRateAndPeriod(t *testing.T) {
	records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{
		Type: AdditionalCostPromotion, Name: "活动赠送", BusinessDate: "2026-08-13", Amount: 900, UsageRate: 0.8, Days: 10,
	}, "promotion-1")
	if err != nil {
		t.Fatalf("buildAdditionalCostRecords() error: %v", err)
	}
	if len(records) != 10 {
		t.Fatalf("record count = %d, want 10", len(records))
	}
	var total int64
	for _, record := range records {
		if record.AmountCents != 7200 {
			t.Fatalf("daily cents = %d, want 7200", record.AmountCents)
		}
		total += record.AmountCents
	}
	if total != 72000 {
		t.Fatalf("total cents = %d, want 72000", total)
	}
}

func TestBuildFixedAdditionalCostPutsRoundingRemainderOnFinalDay(t *testing.T) {
	records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{
		Type: AdditionalCostFixed, Name: "服务器", BusinessDate: "2026-08-13", Amount: 100, Days: 3,
	}, "fixed-1")
	if err != nil {
		t.Fatalf("buildAdditionalCostRecords() error: %v", err)
	}
	if records[0].AmountCents != 3333 || records[1].AmountCents != 3333 || records[2].AmountCents != 3334 {
		t.Fatalf("rounding records = %+v", records)
	}
}

func TestBuildAdjustmentAllowsNegativeAmountWithoutRewritingExistingRecord(t *testing.T) {
	records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{
		Type: AdditionalCostAdjustment, Name: "服务器退款", BusinessDate: "2026-08-13", Amount: -42.35,
	}, "adjustment-1")
	if err != nil {
		t.Fatalf("buildAdditionalCostRecords() error: %v", err)
	}
	if len(records) != 1 || records[0].AmountCents != -4235 || records[0].Amount != -42.35 || records[0].SourceID != "adjustment-1" {
		t.Fatalf("adjustment = %+v", records)
	}
}

func TestValidateEditableAdditionalCostRecordProtectsSystemSources(t *testing.T) {
	for name, item := range map[string]AdditionalCostRecord{
		"account purchase": {Type: AdditionalCostAccountPurchase},
		"account refund":   {Type: AdditionalCostAccountRefund},
		"batch-linked":     {Type: AdditionalCostFixed, BatchID: "batch-1"},
		"asset-linked":     {Type: AdditionalCostFixed, AccountAssetID: "asset-1"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateEditableAdditionalCostRecord(item); !errors.Is(err, ErrAdditionalCostProtected) {
				t.Fatalf("validateEditableAdditionalCostRecord() = %v, want ErrAdditionalCostProtected", err)
			}
		})
	}
	if err := validateEditableAdditionalCostRecord(AdditionalCostRecord{Type: AdditionalCostFixed}); err != nil {
		t.Fatalf("manual cost unexpectedly protected: %v", err)
	}
}

func TestAdditionalCostSummaryUsesConfiguredRechargeFeeRate(t *testing.T) {
	revenue := 100.0
	repository := &fakeAdditionalCostRepository{
		rate:  RechargeFeeRate{Rate: 0.02},
		items: []AdditionalCostRecord{{Type: AdditionalCostFixed, Amount: 5}},
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-08-13", &revenue)
	if summary == nil || summary.RechargeFee == nil || *summary.RechargeFee != 2 || summary.FeeRate == nil || *summary.FeeRate != 0.02 || summary.Total == nil || *summary.Total != 7 {
		t.Fatalf("summary = %+v, want 2%% fee and total 7", summary)
	}
}

func TestAdditionalCostSummaryKeepsTotalUnavailableWithoutRevenue(t *testing.T) {
	repository := &fakeAdditionalCostRepository{
		rate:  RechargeFeeRate{Rate: 0.02},
		items: []AdditionalCostRecord{{Type: AdditionalCostFixed, Amount: 5}},
	}
	service := &MetricsService{additionalCosts: repository}
	summary := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-08-13", nil)
	if summary == nil || summary.Available || summary.Total != nil || summary.UnavailableReason != "revenue_unavailable" {
		t.Fatalf("summary = %+v, want unavailable without total", summary)
	}
}

func TestAdditionalCostSummaryIncludesAccountPurchaseAndRefundWithLedgerSigns(t *testing.T) {
	summary := summarizeAdditionalCostRecords([]AdditionalCostRecord{
		{Type: AdditionalCostFixed, Amount: 5},
		{Type: AdditionalCostAccountPurchase, Amount: 10},
		{Type: AdditionalCostAccountRefund, Amount: -3},
	})
	if summary.AccountPurchase != 10 || summary.AccountRefund != -3 {
		t.Fatalf("account cost summary = %#v", summary)
	}
	if summary.Promotion != 0 || summary.Fixed != 5 || summary.Adjustment != 0 {
		t.Fatalf("other cost summary changed = %#v", summary)
	}
	revenue := 100.0
	repository := &fakeAdditionalCostRepository{
		rate:  RechargeFeeRate{Rate: 0.02},
		items: summary.Records,
	}
	service := &MetricsService{additionalCosts: repository}
	withFee := service.additionalCostSummary(context.Background(), "user-1", "workspace-1", "2026-08-13", &revenue)
	if withFee.Total == nil || *withFee.Total != 14 {
		t.Fatalf("ledger total = %#v, want fixed 5 + purchase 10 - refund 3 + fee 2 = 14", withFee.Total)
	}
}

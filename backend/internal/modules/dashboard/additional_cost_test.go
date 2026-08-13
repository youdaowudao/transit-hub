package dashboard

import (
	"context"
	"testing"
)

type fakeAdditionalCostRepository struct {
	rate  RechargeFeeRate
	items []AdditionalCostRecord
}

func (f *fakeAdditionalCostRepository) GetRechargeFeeRate(context.Context, string, string, string) (RechargeFeeRate, error) {
	return f.rate, nil
}

func (f *fakeAdditionalCostRepository) ListAdditionalCosts(context.Context, string, string, string, string) ([]AdditionalCostRecord, error) {
	return f.items, nil
}

func (f *fakeAdditionalCostRepository) SaveRechargeFeeRate(context.Context, RechargeFeeRate) error {
	return nil
}

func (f *fakeAdditionalCostRepository) InsertAdditionalCosts(context.Context, []AdditionalCostRecord) error {
	return nil
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
	if len(records) != 1 || records[0].AmountCents != -4235 || records[0].Amount != -42.35 {
		t.Fatalf("adjustment = %+v", records)
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

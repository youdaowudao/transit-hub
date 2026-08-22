package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const defaultRechargeFeeRate = 0.016

const (
	AdditionalCostRechargeFee     = "recharge_fee"
	AdditionalCostPromotion       = "promotion"
	AdditionalCostFixed           = "fixed"
	AdditionalCostAdjustment      = "adjustment"
	AdditionalCostAccountPurchase = "account_purchase"
	AdditionalCostAccountRefund   = "account_refund"
)

var (
	ErrAdditionalCostInvalidType   = errors.New("dashboard.additionalCost.errors.invalidType")
	ErrAdditionalCostInvalidAmount = errors.New("dashboard.additionalCost.errors.invalidAmount")
	ErrAdditionalCostInvalidDate   = errors.New("dashboard.additionalCost.errors.invalidDate")
	ErrAdditionalCostInvalidDays   = errors.New("dashboard.additionalCost.errors.invalidDays")
	ErrAdditionalCostInvalidRate   = errors.New("dashboard.additionalCost.errors.invalidRate")
)

type RechargeFeeRate struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	AdminAccountID string    `json:"-"`
	EffectiveDate  string    `json:"effectiveDate"`
	Rate           float64   `json:"rate"`
	CreatedAt      time.Time `json:"createdAt"`
}

type AdditionalCostRecord struct {
	ID             string    `json:"id"`
	UserID         string    `json:"-"`
	AdminAccountID string    `json:"-"`
	Type           string    `json:"type"`
	Name           string    `json:"name"`
	BusinessDate   string    `json:"businessDate"`
	AmountCents    int64     `json:"amountCents"`
	Amount         float64   `json:"amount"`
	OriginalAmount float64   `json:"originalAmount,omitempty"`
	Rate           float64   `json:"rate,omitempty"`
	UsageRate      float64   `json:"usageRate,omitempty"`
	Days           int       `json:"days,omitempty"`
	SourceID       string    `json:"sourceId,omitempty"`
	BatchID        string    `json:"batchId,omitempty"`
	AccountAssetID string    `json:"accountAssetId,omitempty"`
	Note           string    `json:"note,omitempty"`
	Estimated      bool      `json:"estimated"`
	CreatedAt      time.Time `json:"createdAt"`
}

type AdditionalCostSummary struct {
	RechargeFee          *float64               `json:"rechargeFee"`
	AccountPurchase      float64                `json:"accountPurchase"`
	AccountRefund        float64                `json:"accountRefund"`
	ReplacementDeduction *float64               `json:"replacementDeduction,omitempty"`
	AccountQuality       string                 `json:"accountQuality,omitempty"`
	Promotion            float64                `json:"promotion"`
	Fixed                float64                `json:"fixed"`
	Adjustment           float64                `json:"adjustment"`
	Total                *float64               `json:"total"`
	Records              []AdditionalCostRecord `json:"records,omitempty"`
	FeeRate              *float64               `json:"feeRate,omitempty"`
	Available            bool                   `json:"available"`
	UnavailableReason    string                 `json:"unavailableReason,omitempty"`
}

type AdditionalCostInput struct {
	Type         string  `json:"type"`
	Name         string  `json:"name"`
	BusinessDate string  `json:"businessDate"`
	Amount       float64 `json:"amount"`
	UsageRate    float64 `json:"usageRate"`
	Days         int     `json:"days"`
	Note         string  `json:"note"`
}

type RechargeFeeRateInput struct {
	EffectiveDate string  `json:"effectiveDate"`
	Rate          float64 `json:"rate"`
}

func mustMetricsID() string {
	id, err := metricsRandomID()
	if err == nil {
		return id
	}
	return fmt.Sprintf("manual-%d", time.Now().UnixNano())
}

type AdditionalCostRepository interface {
	GetRechargeFeeRate(ctx context.Context, userID, adminAccountID, date string) (RechargeFeeRate, error)
	ListAdditionalCosts(ctx context.Context, userID, adminAccountID, from, to string) ([]AdditionalCostRecord, error)
	SaveRechargeFeeRate(ctx context.Context, rate RechargeFeeRate) error
	InsertAdditionalCosts(ctx context.Context, records []AdditionalCostRecord) error
}

type rechargeFeeRateHistoryRepository interface {
	ListRechargeFeeRates(ctx context.Context, userID, adminAccountID string) ([]RechargeFeeRate, error)
}

func normalizeAdditionalCostInput(input AdditionalCostInput) (AdditionalCostInput, error) {
	input.Type = strings.TrimSpace(input.Type)
	input.Name = strings.TrimSpace(input.Name)
	input.BusinessDate = strings.TrimSpace(input.BusinessDate)
	input.Note = strings.TrimSpace(input.Note)
	if input.Type != AdditionalCostPromotion && input.Type != AdditionalCostFixed && input.Type != AdditionalCostAdjustment {
		return input, ErrAdditionalCostInvalidType
	}
	if math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) || math.Abs(input.Amount) > 1e12 {
		return input, ErrAdditionalCostInvalidAmount
	}
	if _, err := time.ParseInLocation("2006-01-02", input.BusinessDate, businesstime.Location()); err != nil {
		return input, ErrAdditionalCostInvalidDate
	}
	if input.Type == AdditionalCostPromotion {
		if input.Amount < 0 || input.UsageRate < 0 || input.UsageRate > 1 || input.Days <= 0 {
			return input, ErrAdditionalCostInvalidRate
		}
	} else if input.Type == AdditionalCostFixed {
		if input.Amount < 0 || input.Days <= 0 {
			return input, ErrAdditionalCostInvalidDays
		}
	}
	if input.Name == "" {
		input.Name = input.Type
	}
	return input, nil
}

func cents(value float64) int64 { return int64(math.Round(value * 100)) }

func buildAdditionalCostRecords(userID, adminAccountID string, input AdditionalCostInput, idPrefix string) ([]AdditionalCostRecord, error) {
	input, err := normalizeAdditionalCostInput(input)
	if err != nil {
		return nil, err
	}
	total := input.Amount
	if input.Type == AdditionalCostPromotion {
		total = input.Amount * input.UsageRate
	}
	totalCents := cents(total)
	if input.Type == AdditionalCostAdjustment {
		return []AdditionalCostRecord{{ID: idPrefix, UserID: userID, AdminAccountID: adminAccountID, Type: input.Type, Name: input.Name, BusinessDate: input.BusinessDate, AmountCents: cents(input.Amount), Amount: float64(cents(input.Amount)) / 100, OriginalAmount: input.Amount, Note: input.Note, CreatedAt: time.Now()}}, nil
	}
	if input.Days <= 0 {
		return nil, ErrAdditionalCostInvalidDays
	}
	start, _ := time.ParseInLocation("2006-01-02", input.BusinessDate, businesstime.Location())
	base := totalCents / int64(input.Days)
	remainder := totalCents - base*int64(input.Days)
	records := make([]AdditionalCostRecord, 0, input.Days)
	for index := 0; index < input.Days; index++ {
		amountCents := base
		if index == input.Days-1 {
			amountCents += remainder
		}
		date := start.AddDate(0, 0, index).Format("2006-01-02")
		records = append(records, AdditionalCostRecord{
			ID: idPrefix + fmt.Sprintf("-%d", index), UserID: userID, AdminAccountID: adminAccountID,
			Type: input.Type, Name: input.Name, BusinessDate: date, AmountCents: amountCents,
			Amount: float64(amountCents) / 100, OriginalAmount: input.Amount, UsageRate: input.UsageRate,
			Days: input.Days, SourceID: idPrefix, Note: input.Note,
			Estimated: input.Type == AdditionalCostPromotion, CreatedAt: time.Now(),
		})
	}
	return records, nil
}

func summarizeAdditionalCostRecords(items []AdditionalCostRecord) AdditionalCostSummary {
	summary := AdditionalCostSummary{Available: true, Records: items}
	for _, item := range items {
		switch item.Type {
		case AdditionalCostPromotion:
			summary.Promotion += item.Amount
		case AdditionalCostFixed:
			summary.Fixed += item.Amount
		case AdditionalCostAdjustment:
			summary.Adjustment += item.Amount
		case AdditionalCostAccountPurchase:
			summary.AccountPurchase += item.Amount
		case AdditionalCostAccountRefund:
			summary.AccountRefund += item.Amount
		}
	}
	return summary
}

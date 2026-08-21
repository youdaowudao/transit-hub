package dashboard

import (
	"errors"
	"math/big"
	"sort"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

var errInvalidCostSchedule = errors.New("dashboard.accountAsset.errors.invalidCostSchedule")

type DatedAmount struct {
	BusinessDate string
	AmountCents  int64
}

type OperatingCostInput struct {
	UpstreamDirectCostCents      int64
	ReplacementDeductionCents    *int64
	RequiresReplacementDeduction bool
	AccountPurchaseCostCents     int64
	OtherCostCents               int64
}

func allocateDailyCost(totalCents int64, startDate string, days int) ([]DatedAmount, error) {
	start, err := time.ParseInLocation("2006-01-02", startDate, businesstime.Location())
	if err != nil || days <= 0 {
		return nil, errInvalidCostSchedule
	}
	amounts, err := allocateCents(totalCents, days)
	if err != nil {
		return nil, err
	}
	result := make([]DatedAmount, 0, days)
	for index, amount := range amounts {
		result = append(result, DatedAmount{
			BusinessDate: start.AddDate(0, 0, index).Format("2006-01-02"),
			AmountCents:  amount,
		})
	}
	return result, nil
}

func recognizeQuotaCost(purchaseCents, totalQuotaMicros, cumulativeUsed, previousRecognized int64, terminal bool) (int64, error) {
	if purchaseCents < 0 || totalQuotaMicros <= 0 || cumulativeUsed < 0 || previousRecognized < 0 || previousRecognized > purchaseCents {
		return 0, errInvalidCostSchedule
	}
	if terminal || cumulativeUsed >= totalQuotaMicros {
		return purchaseCents - previousRecognized, nil
	}
	product := new(big.Int).Mul(big.NewInt(purchaseCents), big.NewInt(cumulativeUsed))
	due := new(big.Int).Quo(product, big.NewInt(totalQuotaMicros))
	if !due.IsInt64() || due.Int64() < previousRecognized {
		return 0, errInvalidCostSchedule
	}
	return due.Int64() - previousRecognized, nil
}

func terminalCostAdjustments(purchaseCents int64, effectiveDate string, existing []DatedAmount) ([]DatedAmount, error) {
	if purchaseCents < 0 {
		return nil, errInvalidCostSchedule
	}
	effective, err := time.ParseInLocation("2006-01-02", effectiveDate, businesstime.Location())
	if err != nil {
		return nil, errInvalidCostSchedule
	}
	var recognized int64
	future := make(map[string]int64)
	for _, item := range existing {
		date, err := time.ParseInLocation("2006-01-02", item.BusinessDate, businesstime.Location())
		if err != nil {
			return nil, errInvalidCostSchedule
		}
		if date.After(effective) {
			future[item.BusinessDate] += item.AmountCents
		} else {
			recognized += item.AmountCents
		}
	}
	if recognized < 0 || recognized > purchaseCents {
		return nil, errInvalidCostSchedule
	}
	dates := make([]string, 0, len(future))
	for date, amount := range future {
		if amount != 0 {
			dates = append(dates, date)
		}
	}
	sort.Strings(dates)
	result := make([]DatedAmount, 0, len(dates)+1)
	for _, date := range dates {
		result = append(result, DatedAmount{BusinessDate: date, AmountCents: -future[date]})
	}
	if remaining := purchaseCents - recognized; remaining > 0 {
		result = append(result, DatedAmount{BusinessDate: effectiveDate, AmountCents: remaining})
	}
	return result, nil
}

func calculateOperatingCost(input OperatingCostInput) (int64, bool) {
	if input.UpstreamDirectCostCents < 0 || input.AccountPurchaseCostCents < 0 {
		return 0, false
	}
	deduction := int64(0)
	if input.ReplacementDeductionCents != nil {
		deduction = *input.ReplacementDeductionCents
		if deduction < 0 || deduction > input.UpstreamDirectCostCents {
			return 0, false
		}
	} else if input.RequiresReplacementDeduction {
		return 0, false
	}
	return input.UpstreamDirectCostCents - deduction + input.AccountPurchaseCostCents + input.OtherCostCents, true
}

func projectOperatingCost(upstreamDirect, revenue *float64, summary *AdditionalCostSummary, components AccountCostComponents, componentErr error) (*float64, *float64, *float64) {
	if summary == nil || summary.Total == nil || upstreamDirect == nil || componentErr != nil {
		if summary != nil {
			summary.AccountQuality = KeyCostQualityMissing
		}
		return nil, nil, nil
	}
	if components.RequiresReplacementDeduction {
		if components.ReplacementDeductionCents == nil || components.ReconciledUpstreamDirectCostCents == nil {
			summary.AccountQuality = KeyCostQualityMissing
			return nil, nil, nil
		}
		if cents(*upstreamDirect) != *components.ReconciledUpstreamDirectCostCents {
			summary.AccountQuality = KeyCostQualityMissing
			return nil, nil, nil
		}
		deduction := float64(*components.ReplacementDeductionCents) / 100
		summary.ReplacementDeduction = &deduction
	}
	summary.AccountQuality = KeyCostQualityComplete
	upstreamDirectCents := cents(*upstreamDirect)
	totalCents, available := calculateOperatingCost(OperatingCostInput{
		UpstreamDirectCostCents:      upstreamDirectCents,
		ReplacementDeductionCents:    components.ReplacementDeductionCents,
		RequiresReplacementDeduction: components.RequiresReplacementDeduction,
		AccountPurchaseCostCents:     components.AccountPurchaseCostCents,
		OtherCostCents:               cents(*summary.Total) - components.AccountPurchaseCostCents,
	})
	if !available {
		summary.AccountQuality = KeyCostQualityMissing
		return nil, nil, nil
	}
	operatingValue := float64(totalCents) / 100
	operating := &operatingValue
	if revenue == nil {
		return operating, nil, nil
	}
	profitValue := *revenue - operatingValue
	profit := &profitValue
	if *revenue <= 0 {
		return operating, profit, nil
	}
	marginValue := profitValue / *revenue * 100
	return operating, profit, &marginValue
}

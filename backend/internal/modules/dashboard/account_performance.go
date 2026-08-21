package dashboard

func calculateAccountPerformance(input AccountPerformanceInput) AccountPerformance {
	if input.HasIncompleteDailyStats {
		return AccountPerformance{MissingFields: []string{"dailyStats"}}
	}
	costCents := input.PurchaseCostCents
	if input.AccountingMode == AccountingModeAdditive {
		costCents += input.AdditiveUpstreamCostCents
	}
	result := AccountPerformance{}
	if !input.HasRevenue {
		result.MissingFields = append(result.MissingFields, "revenue")
	}
	if !input.HasRawQuotaUsed {
		result.MissingFields = append(result.MissingFields, "quotaUsed")
	}
	if input.AccountingMode == AccountingModeAdditive && !input.HasAdditiveUpstreamCost {
		result.MissingFields = append(result.MissingFields, "upstreamCost")
	}
	if input.HasRevenue && input.HasRawQuotaUsed && input.RawQuotaUsedMicros > 0 {
		value := float64(input.RevenueCents) * 10_000 / float64(input.RawQuotaUsedMicros)
		result.AverageSaleMultiplier = &value
	}
	financialInputsComplete := input.HasRevenue && (input.AccountingMode != AccountingModeAdditive || input.HasAdditiveUpstreamCost)
	if financialInputsComplete && costCents > 0 {
		value := float64(input.RevenueCents+input.RefundCents) / float64(costCents)
		result.CostRecoveryMultiple = &value
	}
	if financialInputsComplete {
		breakeven := input.RevenueCents + input.RefundCents - costCents
		result.BreakevenDifferenceCents = &breakeven
	}
	if isTerminalAccountStatus(input.Status) && result.BreakevenDifferenceCents != nil {
		profit := *result.BreakevenDifferenceCents
		result.FinalProfitCents = &profit
		netInvestment := costCents - input.RefundCents
		if netInvestment > 0 {
			value := float64(profit) / float64(netInvestment)
			result.ROI = &value
		}
	}
	return result
}

func isTerminalAccountStatus(status string) bool {
	return status == AccountStatusExhausted || status == AccountStatusDead || status == AccountStatusClosed
}

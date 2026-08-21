package dashboard

import (
	"context"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type AutomaticAccountTarget struct {
	Asset AccountAsset
	Link  AccountLink
}

func (s *MetricsService) saveAutomaticAccountStatsForRun(
	ctx context.Context,
	userID, adminAccountID, date string,
	runs []UpstreamKeyCostRun,
	session upstream.Session,
) (int, int, string, error) {
	if s == nil || s.accountStats == nil || s.platform == nil {
		return 0, 0, KeyCostQualityMissing, nil
	}
	targets, err := s.accountStats.ListAutomaticAccountTargets(ctx, userID, adminAccountID, date)
	if err != nil {
		return 0, 0, KeyCostQualityMissing, err
	}
	stats, quality := s.buildAutomaticAccountStatsForTargets(ctx, userID, adminAccountID, date, runs, session, targets)
	if err := s.accountStats.SaveAutomaticAccountDailyStats(ctx, stats); err != nil {
		return len(targets), 0, KeyCostQualityMissing, err
	}
	return len(targets), len(stats), quality, nil
}

func (s *MetricsService) buildAutomaticAccountStatsForTargets(
	ctx context.Context,
	userID, adminAccountID, date string,
	runs []UpstreamKeyCostRun,
	session upstream.Session,
	targets []AutomaticAccountTarget,
) ([]AccountDailyStat, string) {
	if s == nil || s.platform == nil {
		return nil, KeyCostQualityMissing
	}
	stats := make([]AccountDailyStat, 0, len(targets))
	for _, target := range targets {
		run, item, ok := findAutomaticAccountKey(target, runs)
		if !ok {
			continue
		}
		usage, err := s.platform.FetchAdminUsageStatsForScope(
			session, target.Link.ScopeAdminAccountID, target.Link.OwnGroupID, date, date,
		)
		if err != nil {
			continue
		}
		stat := buildAutomaticAccountDailyStat(target, run, item, date, cents(usage.TotalActualCost), time.Now().UTC())
		stat.UserID, stat.AdminAccountID = userID, adminAccountID
		stats = append(stats, stat)
	}
	quality := KeyCostQualityComplete
	if len(stats) != len(targets) {
		quality = KeyCostQualityMissing
	}
	return stats, quality
}

func (s *MetricsService) finalizeAutomaticAccountSubstate(
	ctx context.Context,
	snapshot DailySnapshot,
	runID, date string,
	runs []UpstreamKeyCostRun,
	session upstream.Session,
) error {
	expected, completed, quality, accountErr := s.saveAutomaticAccountStatsForRun(
		ctx, snapshot.UserID, snapshot.AdminAccountID, date, runs, session,
	)
	snapshot.AccountSnapshotRunID = runID
	snapshot.AccountExpectedCount = intPtr(expected)
	snapshot.AccountCompletedCount = intPtr(completed)
	snapshot.AccountStatsQuality = quality
	if accountErr != nil {
		snapshot.AccountStatsQuality = KeyCostQualityMissing
	}

	additionalCosts := s.additionalCostSummary(ctx, snapshot.UserID, snapshot.AdminAccountID, date, snapshot.TodayProfit)
	if additionalCosts != nil {
		snapshot.AdditionalCost = additionalCostTotal(additionalCosts)
		snapshot.RechargeFee, snapshot.RechargeFeeRate = rechargeFeeSummary(additionalCosts)
		snapshot.PromotionCost, snapshot.FixedCost, snapshot.AdjustmentCost = additionalCostCategorySummary(additionalCosts)
		snapshot.AdditionalCostRecords = additionalCostRecords(additionalCosts)
		accountPurchase := additionalCosts.AccountPurchase
		snapshot.AccountPurchaseCost = &accountPurchase
		if s.accountCosts != nil {
			components, componentErr := s.accountCosts.AccountCostComponentsForSnapshotRun(ctx, snapshot.UserID, snapshot.AdminAccountID, date, runID)
			snapshot.OperatingCost, snapshot.AdjustedNetProfit, _ = projectOperatingCost(
				snapshot.TodayPurchase, snapshot.TodayProfit, additionalCosts, components, componentErr,
			)
			snapshot.ReplacementDeduction = additionalCosts.ReplacementDeduction
		}
	}
	if err := s.metricsRepo.Upsert(ctx, snapshot); err != nil {
		return err
	}
	return accountErr
}

type automaticAccountStatsRepository interface {
	ListAutomaticAccountTargets(ctx context.Context, userID, adminAccountID, date string) ([]AutomaticAccountTarget, error)
	SaveAutomaticAccountDailyStats(ctx context.Context, stats []AccountDailyStat) error
}

func findAutomaticAccountKey(target AutomaticAccountTarget, runs []UpstreamKeyCostRun) (UpstreamKeyCostRun, UpstreamKeyDailyCost, bool) {
	for _, run := range runs {
		if !run.Complete || run.SiteID != target.Link.UpstreamSiteID {
			continue
		}
		for _, item := range run.Items {
			if item.KeyID == target.Link.UpstreamKeyID {
				return run, item, true
			}
		}
	}
	return UpstreamKeyCostRun{}, UpstreamKeyDailyCost{}, false
}

func buildAutomaticAccountDailyStat(target AutomaticAccountTarget, run UpstreamKeyCostRun, item UpstreamKeyDailyCost, date string, revenueCents int64, observedAt time.Time) AccountDailyStat {
	rawQuota, upstreamCost, revenue := item.RawAmountMicros, item.AdjustedCostCents, revenueCents
	stat := AccountDailyStat{
		ID: mustMetricsID(), AccountAssetID: target.Asset.ID, BusinessDate: date,
		Source: StatsModeAutomatic, Quality: KeyCostQualityComplete, KeyCostRunID: run.SnapshotRunID,
		RawQuotaUsedMicros: &rawQuota, RevenueCents: &revenue, UpstreamCostCents: &upstreamCost,
		ObservedAt: &observedAt, CreatedAt: observedAt, UpdatedAt: observedAt,
	}
	if target.Asset.AccountingMode == AccountingModeReplace {
		stat.ReplacementDeductionCents = &upstreamCost
	}
	return stat
}

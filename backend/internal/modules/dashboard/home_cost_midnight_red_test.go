package dashboard

import (
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostCollectionCrossesMidnightCannotReconcileZeroAmounts(t *testing.T) {
	startedAt := time.Date(2031, 2, 3, 15, 59, 59, 0, time.UTC)
	collectedAt := startedAt.Add(2 * time.Second)
	date := businesstime.DateAt(startedAt)
	zero := 0.0
	keys := upstream.KeyUsageForDateResult{BusinessDate: date, Sites: []upstream.KeyUsageSiteResult{{SiteID: "site", Complete: true, Status: "ok", ConsumeDate: date, StartedAt: startedAt, CollectedAt: &collectedAt, SyncedRawCost: &zero, CollectedRawCost: &zero, Items: []upstream.KeyUsageTodayItem{{KeyID: "key", RawAmount: 0, TodayAmount: 0}}}}}
	runs := buildAccountKeyCostRuns("user", "workspace", "run", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 0, RechargeRate: 1}}, keys, collectedAt)
	if len(runs) != 1 || runs[0].Complete || runs[0].Quality != KeyCostQualityMismatch {
		t.Fatalf("cross-midnight collection confirmed zero cost: %#v", runs)
	}
	keys.Sites[0].CollectedAt = &startedAt
	runs = buildAccountKeyCostRuns("user", "workspace", "same-day", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 0, RechargeRate: 1}}, keys, startedAt)
	if len(runs) != 1 || !runs[0].Complete {
		t.Fatalf("same-day confirmed zero became unavailable: %#v", runs)
	}
}

package dashboard

import (
	"context"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// 主代理固定 D3 的业务边界，不可放宽真实差额，也不能把零金额计入容差。
func TestHomeCostStageAReconciliationRoundingBoundaries(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, tc := range []struct {
		name     string
		amounts  []float64
		total    float64
		complete bool
	}{
		{"three_nonzero_two_cents", []float64{1, 1, 1}, 3.02, true},
		{"three_nonzero_three_cents", []float64{1, 1, 1}, 3.03, false},
		{"one_nonzero_two_cents", append(make([]float64, 100), 1), 1.02, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]upstream.KeyUsageTodayItem, len(tc.amounts))
			for i, amount := range tc.amounts {
				items[i] = upstream.KeyUsageTodayItem{SiteID: "site", TodayAmount: amount, RawAmount: amount}
			}
			runs := buildAccountKeyCostRuns("user", "workspace", "run", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: tc.total, RechargeRate: 1}}, upstream.KeyUsageForDateResult{BusinessDate: date, ExpectedSites: 1, CompletedSites: 1, Sites: []upstream.KeyUsageSiteResult{{SiteID: "site", Complete: true, Items: items}}}, now)
			if len(runs) != 1 || runs[0].Complete != tc.complete {
				t.Fatalf("reconciliation = %#v; want complete=%v", runs, tc.complete)
			}
		})
	}
}

func TestHomeCostStageADrilldownNeverCallsLiveKeyUsage(t *testing.T) {
	upstreams := &fakeUpstreamLister{cachedSites: []upstream.Response{{ID: "site", RechargeRate: 1}}, keyUsageItems: []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "one", TodayAmount: 9}}}
	svc := NewMetricsService(nil, nil, upstreams, nil, nil)
	_, _ = svc.UpstreamKeyUsageToday(context.Background(), "user")
	if upstreams.keyUsageCalls != 0 {
		t.Fatalf("drilldown called live key usage %d times", upstreams.keyUsageCalls)
	}
}

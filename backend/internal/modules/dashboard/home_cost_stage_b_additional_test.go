package dashboard

import (
	"encoding/json"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func stageBCostMetrics(t *testing.T, date string, observedAt time.Time, status string, value *float64) upstream.Metrics {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"todayConsume":     map[string]any{"value": value},
		"todayConsumeDate": date, "todayConsumeAt": observedAt, "todayConsumeStatus": status,
	})
	if err != nil {
		t.Fatal(err)
	}
	var metrics upstream.Metrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		t.Fatal(err)
	}
	return metrics
}

func TestHomeCostStageBLiveUnreadableCostRetainsAmountAndQuality(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	observedAt := now.Add(-3 * time.Hour)
	repo := &fakeMetricsRepository{}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, &fakeUpstreamLister{
		cachedSites: []upstream.Response{{
			ID: "unreadable", Name: "验收-成本不可读", Status: upstream.StatusConnected, RechargeRate: 2,
			Metrics: stageBCostMetrics(t, businesstime.DateAt(now), observedAt, "unreadable", ptrFloat64(10)),
		}},
	}, repo)
	service.now = func() time.Time { return now }
	response, err := service.LiveMetrics(t.Context(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 20 || response.NetProfit == nil || *response.NetProfit != 10 {
		t.Fatalf("same-day retained amounts: purchase=%v net=%v", response.TodayPurchase, response.NetProfit)
	}
	quality := response.CostQuality
	if quality == nil || quality.Mode != "retained" || quality.FreshSites != 0 || quality.RetainedSites != 1 || quality.MissingSites != 0 || !quality.Complete {
		t.Fatalf("unreadable cost must remain confirmed but retained: %+v", quality)
	}
	if quality.FallbackAt == nil || !quality.FallbackAt.Equal(observedAt) || quality.ObservedAt == nil || !quality.ObservedAt.Equal(now) {
		t.Fatalf("retained observation times: %+v", quality)
	}
	if response.SettlementStatus != SettlementStatusFallback || len(repo.snapshots) != 1 || repo.snapshots[0].CostQualityMode != "retained" || repo.snapshots[0].CostRetainedCount == nil || *repo.snapshots[0].CostRetainedCount != 1 {
		t.Fatalf("retained quality did not reach live snapshot: status=%s snapshots=%+v", response.SettlementStatus, repo.snapshots)
	}
}

func TestHomeCostStageBLiveCostClockAndUnreadableBoundaries(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, item := range []struct {
		name         string
		status       string
		date         string
		observedAt   time.Time
		value        *float64
		wantMode     string
		wantPurchase *float64
	}{
		{"cross-day", "unreadable", businesstime.DateAt(now.Add(-24 * time.Hour)), now.Add(-24 * time.Hour), ptrFloat64(10), "unavailable", nil},
		{"missing", "unreadable", businesstime.DateAt(now), now, nil, "unavailable", nil},
		{"confirmed-zero", "unreadable", businesstime.DateAt(now), now, ptrFloat64(0), "retained", ptrFloat64(0)},
		{"fresh-old-data", "", businesstime.DateAt(now), now, ptrFloat64(10), "exact", ptrFloat64(20)},
		{"fresh-ok", "ok", businesstime.DateAt(now), now, ptrFloat64(10), "exact", ptrFloat64(20)},
		{"stale-ok", "ok", businesstime.DateAt(now), now.Add(-3 * time.Hour), ptrFloat64(10), "unavailable", nil},
	} {
		t.Run(item.name, func(t *testing.T) {
			repo := &fakeMetricsRepository{}
			service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, &fakeUpstreamLister{
				cachedSites: []upstream.Response{{
					ID: "fixture", Name: "验收-日期边界", Status: upstream.StatusConnected, RechargeRate: 2,
					Metrics: stageBCostMetrics(t, item.date, item.observedAt, item.status, item.value),
				}},
			}, repo)
			service.now = func() time.Time { return now }
			response, err := service.LiveMetrics(t.Context(), "user-1")
			if err != nil {
				t.Fatal(err)
			}
			if response.CostQuality == nil || response.CostQuality.Mode != item.wantMode {
				t.Fatalf("quality=%+v want %s", response.CostQuality, item.wantMode)
			}
			if item.wantPurchase == nil {
				if response.TodayPurchase != nil || response.NetProfit != nil || len(repo.snapshots) != 1 || repo.snapshots[0].TodayPurchase != nil {
					t.Fatalf("unconfirmed amount must remain unknown: purchase=%v net=%v snapshots=%+v", response.TodayPurchase, response.NetProfit, repo.snapshots)
				}
			} else if response.TodayPurchase == nil || *response.TodayPurchase != *item.wantPurchase {
				t.Fatalf("purchase=%v want %v", response.TodayPurchase, *item.wantPurchase)
			}
		})
	}
}

func TestHomeCostStageBUnreadableStillUsesExistingHistoryFallback(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	previousAt := now.Add(-time.Hour)
	repo := &fakeMetricsRepository{latestSiteCosts: []SiteDailyCost{{
		SiteID: "fixture", AdjustedCost: ptrFloat64(9), ObservedAt: &previousAt,
	}}}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, &fakeUpstreamLister{
		cachedSites: []upstream.Response{{
			ID: "fixture", Name: "验收-保留历史回退", Status: upstream.StatusConnected, RechargeRate: 2,
			Metrics: stageBCostMetrics(t, businesstime.DateAt(now), now, "unreadable", ptrFloat64(3)),
		}},
	}, repo)
	service.now = func() time.Time { return now }
	response, err := service.LiveMetrics(t.Context(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if response.TodayPurchase == nil || *response.TodayPurchase != 9 || response.CostQuality == nil || response.CostQuality.Mode != "retained" || response.CostQuality.FallbackAt == nil || !response.CostQuality.FallbackAt.Equal(previousAt) {
		t.Fatalf("existing history fallback changed: purchase=%v quality=%+v", response.TodayPurchase, response.CostQuality)
	}
}

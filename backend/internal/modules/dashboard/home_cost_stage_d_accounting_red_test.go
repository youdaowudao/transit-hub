package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func TestHomeCostStageDGroupFallbackOnlyUsesRequestedBusinessDay(t *testing.T) {
	now := time.Date(2031, 2, 3, 16, 0, 1, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, age := range []time.Duration{0, -2 * time.Second} {
		t.Run(age.String(), func(t *testing.T) {
			at := now.Add(age)
			amount := 12.0
			repo := &fakeMetricsRepository{groupMetricCache: []GroupMetricCacheItem{
				{MetricType: "revenue", GroupID: "1", TodayRevenue: &amount, ObservedAt: at},
				{MetricType: "profit", GroupID: "1", TodayProfit: &amount, ObservedAt: at},
			}}
			s := NewMetricsService(nil, nil, nil, repo, nil)
			s.now = func() time.Time { return now }
			cause := errors.New("fixture unavailable")
			revenue, err := s.cachedGroupRevenue(t.Context(), "user", "ws", date, cause)
			profit, profitErr := s.cachedGroupProfit(t.Context(), "user", "ws", date, cause)
			merged, mergeErr := s.mergeGroupProfitFallback(t.Context(), "user", "ws", date, nil, map[string]struct{}{"1": {}})
			if mergeErr != nil {
				t.Fatal(mergeErr)
			}
			if age == 0 {
				if err != nil || profitErr != nil || revenue.TotalRevenue != 12 || profit.TotalProfit != 12 || merged.FallbackGroups != 1 {
					t.Error("same-day successful cache must remain available")
				}
			} else {
				if err == nil || profitErr == nil || len(revenue.Groups) != 0 || len(profit.Groups) != 0 {
					t.Error("prior-day revenue/profit was returned as current fallback")
				}
				if len(merged.Groups) != 0 || merged.FallbackGroups != 0 || merged.UnavailableGroups != 1 {
					t.Errorf("prior-day merged profit fallback=%+v", merged)
				}
			}
		})
	}
}

func TestHomeCostStageDAdjustedProfitMarginQuality(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, tc := range []struct {
		count int
		want  string
	}{{10, "exact"}, {9, "exact"}, {8, "ceiling"}, {0, "unavailable"}} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"total_actual_cost": 30, "role": "admin"}})
			}))
			defer server.Close()
			store := newFakeSessionStore()
			store.set("user-1", "account-1", AdminSession{Session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}})
			sites := make([]upstream.Response, 10)
			for i := range sites {
				sites[i] = upstream.Response{ID: fmt.Sprint(i), Status: upstream.StatusConnected, RechargeRate: 1}
				if i >= tc.count {
					sites[i].Status = upstream.StatusError
				}
				if i < tc.count {
					amount := 1.0
					sites[i].Metrics = upstream.Metrics{TodayConsume: upstream.MetricValue{Value: &amount}, TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &now, TodayConsumeStatus: "ok"}
				}
			}
			repo := &liveMetricsAccountingRepository{fakeMetricsRepository: &fakeMetricsRepository{}, fakeAdditionalCostRepository: &fakeAdditionalCostRepository{rate: RechargeFeeRate{Rate: 0}, items: []AdditionalCostRecord{{Type: AdditionalCostFixed, Amount: 5}}}}
			s := NewMetricsService(store, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), &fakeUpstreamLister{cachedSites: sites}, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
			s.now = func() time.Time { return now }
			response, err := s.LiveMetrics(t.Context(), "user-1")
			if err != nil {
				t.Fatal(err)
			}
			payload := metricsResponseJSON(t, response)
			if payload["adjustedProfitMarginQuality"] != tc.want {
				t.Errorf("margin quality=%v want=%s", payload["adjustedProfitMarginQuality"], tc.want)
			}
			if response.AdjustedProfitMargin == nil {
				t.Fatal("fixture should provide adjusted margin")
			}
		})
	}
}

func TestHomeCostStageDPartialTrendsUseOperatingAmountsOrNull(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, status := range []string{SettlementStatusPartial, SettlementStatusProvisional, SettlementStatusPartialHigh, SettlementStatusFinal} {
		for _, haveOperating := range []bool{true, false} {
			t.Run(status+fmt.Sprint(haveOperating), func(t *testing.T) {
				cost, profit, operating, adjusted := 4.0, 16.0, 7.0, 13.0
				snap := DailySnapshot{Date: now.Add(-24 * time.Hour), TodayPurchase: &cost, NetProfit: &profit, SettlementStatus: status}
				if haveOperating {
					snap.OperatingCost = &operating
					snap.AdjustedNetProfit = &adjusted
				}
				repo := &fakeMetricsRepository{listRangeSnapshots: []DailySnapshot{snap}}
				s := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "account-1"}})
				s.now = func() time.Time { return now }
				response, err := s.Trends(t.Context(), "user-1", 7)
				if err != nil {
					t.Fatal(err)
				}
				point := response.Points[0]
				if status == SettlementStatusPartial || status == SettlementStatusProvisional {
					if point.TodayPurchase != nil || point.NetProfit != nil {
						t.Error("partial points must retain their provisional fields")
					}
					if haveOperating {
						if point.ConfirmedCost == nil || *point.ConfirmedCost != operating || point.NetProfitCeiling == nil || *point.NetProfitCeiling != adjusted {
							t.Errorf("partial trend uses direct amounts: %+v", point)
						}
					} else if point.ConfirmedCost != nil || point.NetProfitCeiling != nil {
						t.Error("absent operating amounts must remain null")
					}
				} else if status == SettlementStatusPartialHigh {
					if point.ConfirmedCost == nil || *point.ConfirmedCost != cost || point.NetProfitCeiling == nil || *point.NetProfitCeiling != profit {
						t.Error("partial_high behavior changed")
					}
				} else if point.TodayPurchase == nil || *point.TodayPurchase != cost || point.NetProfit == nil || *point.NetProfit != profit {
					t.Error("final behavior changed")
				}
			})
		}
	}
}

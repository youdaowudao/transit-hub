package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestAccountConsumptionUsesExistingSyncResponses(t *testing.T) {
	for _, platform := range []Platform{PlatformSub2API, PlatformNewAPI} {
		for _, amount := range []any{123.75, float64(0), nil} {
			t.Run(string(platform)+"/"+stringMustJSON(t, amount), func(t *testing.T) {
				calls := map[string]int{}
				p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
					calls[r.URL.Path]++
					switch r.URL.Path {
					case "/api/v1/auth/me":
						return 200, map[string]any{"data": map[string]any{"balance": 10, "total_recharged": 999}}
					case "/api/v1/usage/dashboard/stats":
						return 200, map[string]any{"data": map[string]any{"total_actual_cost": amount}}
					case "/api/v1/usage/stats":
						return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
					case "/api/v1/groups/available":
						return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": "a", "rate_multiplier": 1}}}
					case "/api/v1/groups/rates":
						return 200, map[string]any{"data": map[string]any{}}
					case "/api/user/self":
						var used any
						if amount != nil {
							used = amount.(float64) * 500000
						}
						return 200, map[string]any{"data": map[string]any{"quota": 5000000, "used_quota": used, "group": "a"}}
					case "/api/log/self/stat":
						return 200, map[string]any{"data": map[string]any{"quota": 3500000}}
					case "/api/user/self/groups":
						return 200, map[string]any{"data": map[string]any{"a": 1}}
					case "/api/pricing":
						return 200, map[string]any{"data": []any{}}
					default:
						t.Errorf("unexpected amount request: %s", r.URL.Path)
						return 500, map[string]any{}
					}
				})
				metrics, err := p.FetchMetricsForDate(Session{Platform: platform, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only", Cookie: "fixture-only", UserID: "42", QuotaPerUnit: 500000}, "2031-01-01")
				if err != nil {
					t.Fatal(err)
				}
				// Check the persisted JSON shape, including unknown values and round-trip.
				raw, _ := json.Marshal(metrics)
				var stored Metrics
				if err = json.Unmarshal(raw, &stored); err != nil {
					t.Fatal(err)
				}
				var payload map[string]any
				raw, _ = json.Marshal(stored)
				if err = json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				total, ok := payload["totalConsume"].(map[string]any)
				if !ok || !reflect.DeepEqual(total["value"], amount) {
					t.Fatalf("saved cumulative amount want=%v got=%v", amount, payload["totalConsume"])
				}
				if metrics.TodayConsume.Value == nil || *metrics.TodayConsume.Value != 7 {
					t.Fatal("today amount changed")
				}
				expected := map[string]int{"/api/v1/auth/me": 1, "/api/v1/usage/dashboard/stats": 1, "/api/v1/usage/stats": 1, "/api/v1/groups/available": 1, "/api/v1/groups/rates": 1}
				if platform == PlatformNewAPI {
					expected = map[string]int{"/api/user/self": 1, "/api/log/self/stat": 1, "/api/user/self/groups": 1, "/api/pricing": 1}
				}
				if !reflect.DeepEqual(calls, expected) {
					t.Fatalf("sync added requests: %v", calls)
				}
			})
		}
	}
}

func stringMustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAccountConsumptionOptionsReadSavedWorkspaceOnly(t *testing.T) {
	var metrics Metrics
	if err := json.Unmarshal([]byte(`{"balance":{"value":11,"display":"11"},"todayConsume":{"value":7,"display":"7"},"totalConsume":{"value":123.75,"display":"123.75"},"todayConsumeDate":"2031-01-01","todayConsumeTimezone":"Asia/Singapore","todayConsumeStatus":"unreadable","todayConsumeErrorDetail":"上游今日消费暂不可读，请稍后同步账号"}`), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2031-01-01", "2031-01-02"} {
		t.Run(date, func(t *testing.T) {
			cache := newSyncTestCache(&Site{ID: "a", UserID: "user", AdminAccountID: "ws", Name: "账号甲", RechargeRate: 2, Metrics: metrics}, &Site{ID: "b", UserID: "user", AdminAccountID: "other", Name: "其他工作区"}, &Site{ID: "c", UserID: "other", AdminAccountID: "ws", Name: "其他用户"})
			svc := NewService(nil, nil, nil, cache)
			now, err := time.Parse("2006-01-02", date)
			if err != nil {
				t.Fatal(err)
			}
			svc.now = func() time.Time { return now }
			result, err := svc.ListOptions(context.Background(), "user", "ws")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(result)
			var body struct {
				Items []struct {
					ID          string `json:"id"`
					Consumption struct {
                        Balance MetricValue `json:"balance"`
                        RechargeRate float64 `json:"rechargeRate"`
					} `json:"consumption"`
				} `json:"items"`
			}
			if err = json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Items) != 1 || body.Items[0].ID != "a" {
				t.Fatalf("foreign account leaked: %s", raw)
			}
			c := body.Items[0].Consumption
            if c.Balance.Value == nil || *c.Balance.Value != 11 || c.RechargeRate != 2 {
                t.Fatalf("saved balance summary missing: %s", raw)
            }
            var fields map[string]any
            encoded, _ := json.Marshal(result.Items[0].Consumption)
            if err := json.Unmarshal(encoded, &fields); err != nil {t.Fatal(err)}
            if len(fields)!=2 || fields["balance"]==nil || fields["rechargeRate"]==nil {t.Fatalf("unexpected summary fields: %s",encoded)}
		})
	}
}

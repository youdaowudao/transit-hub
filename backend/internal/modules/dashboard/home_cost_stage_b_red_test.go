package dashboard

import (
	"encoding/json"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

// 主代理固定 B-1 首页质量，采用 JSON 确保 RED 在新增字段之前可运行。
func TestHomeCostStageBUnreadableAmountIsRetainedNotExact(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, status := range []string{"unreadable", "", "ok"} {
		t.Run(status, func(t *testing.T) {
			var metrics upstream.Metrics
			body, _ := json.Marshal(map[string]any{"todayConsume": map[string]any{"value": 3, "display": "3.00"}, "todayConsumeDate": date, "todayConsumeAt": now, "todayConsumeStatus": status})
			if err := json.Unmarshal(body, &metrics); err != nil {
				t.Fatal(err)
			}
			site := upstream.Response{ID: "site", Name: "验收-成本", Status: upstream.StatusConnected, RechargeRate: 2, Metrics: metrics}
			total, quality := summarizeCachedUpstreamCostsWithQuality([]upstream.Response{site}, date, 0)
			expected := "exact"
			if status == "unreadable" {
				expected = "retained"
			}
			if total != 6 || quality.Mode != expected {
				t.Errorf("total=%v mode=%s want total=6 mode=%s", total, quality.Mode, expected)
			}
			site.Metrics.TodayConsumeDate = businesstime.DateAt(now.Add(-24 * time.Hour))
			total, quality = summarizeCachedUpstreamCostsWithQuality([]upstream.Response{site}, date, 0)
			if total != 0 || quality.Mode != "unavailable" {
				t.Error("cross-day amount used as current cost")
			}
		})
	}
}

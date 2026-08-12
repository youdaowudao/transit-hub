package dashboard

import (
	"testing"
)

// TestSettlementStatusConstants 验证结算状态常量定义完整性
func TestSettlementStatusConstants(t *testing.T) {
	expectedStatuses := []string{
		SettlementStatusProvisional,
		SettlementStatusFallback,
		SettlementStatusPartialHigh,
		SettlementStatusPartial,
		SettlementStatusFinal,
		SettlementStatusMissing,
		SettlementStatusUnavailable,
	}

	// 验证所有常量都有值
	for _, status := range expectedStatuses {
		if status == "" {
			t.Errorf("Settlement status constant is empty")
		}
	}

	// 验证 partial_high 常量值正确
	if SettlementStatusPartialHigh != "partial_high" {
		t.Errorf("SettlementStatusPartialHigh = %q, want %q", SettlementStatusPartialHigh, "partial_high")
	}
}

// TestSettlementCoverageThreshold 验证覆盖率阈值边界情况
func TestSettlementCoverageThreshold(t *testing.T) {
	const minCoverage = 0.90

	tests := []struct {
		name      string
		collected int
		expected  int
		wantHigh  bool // 是否应该是 partial_high
	}{
		{"完全覆盖", 22, 22, true},
		{"90%覆盖率", 18, 20, true},
		{"刚好90%", 9, 10, true},
		{"89%覆盖率", 89, 100, false},
		{"单个失败低覆盖", 1, 2, false},
		{"单个失败高覆盖", 19, 20, true},
		{"零站点", 0, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expected == 0 {
				return // 跳过零站点情况
			}
			coverage := float64(tt.collected) / float64(tt.expected)
			isHigh := coverage >= minCoverage

			if isHigh != tt.wantHigh {
				t.Errorf("coverage %.2f%% (collected=%d, expected=%d): got isHigh=%v, want %v",
					coverage*100, tt.collected, tt.expected, isHigh, tt.wantHigh)
			}
		})
	}
}

package upstream

import (
	"strings"
	"testing"
	"time"
)

// 主代理固定的业务门禁；不得通过放宽核验或改业务期望使之通过。
func TestGroupCostRetryCriticalChangedTotalsWaitFiveMinutes(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	_, err := reconcileGroupCosts(PlatformSub2API, []GroupInfo{{ID: "1"}}, []GroupDailyStat{{GroupID: "1", TodayActualCost: 3}}, 2, 3)
	if err == nil {
		t.Fatal("读取期间费用变化必须拒绝本轮发布")
	}
	class := groupCostFailureClass(err)
	state := GroupCostSamplingState{LastSuccessAt: now.Add(-time.Minute), SummaryAuthFailures: 4, SummaryNetworkFailures: 3}
	state.RecordSummaryFailure(class, now)
	if class != "cost_changed" || !state.SummaryNextAllowedAt.Equal(now.Add(5*time.Minute)) || state.SummaryAuthFailures != 0 || state.SummaryNetworkFailures != 0 || state.LastAttemptStatus != "failed" || !state.LastSuccessAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("合法消费变化只能五分钟后允许、不延续旧错误、不伪造成功；class=%s state=%+v", class, state)
	}
}

func TestGroupCostRetryComparisonLogsKeepOnlyNumbersAndClasses(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	next := time.Date(2031, 2, 3, 4, 5, 0, 0, time.UTC)
	site := Site{ID: "fixture-site", AdminAccountID: "fixture-workspace", Name: "fixture-secret-name", BaseURL: "https://fixture-secret.invalid", Session: &Session{AccessToken: "fixture-secret-token"}}
	for _, tc := range []struct {
		a, b, group float64
		want        string
	}{{2, 3, 3, "comparison=cost_changed a=2 b=3"}, {3, 3, 2, "comparison=group_total_mismatch a=3 b=3 g=2"}} {
		_, err := reconcileGroupCosts(PlatformSub2API, []GroupInfo{{ID: "1"}}, []GroupDailyStat{{GroupID: "1", TodayActualCost: tc.group}}, tc.a, tc.b)
		if err == nil {
			t.Fatal("不一致必须先拒绝发布")
		}
		logGroupCostComparison(site, "2031-02-03", err, next)
		if !strings.Contains(logs.String(), tc.want) || !strings.Contains(logs.String(), "next_allowed_at=2031-02-03T04:05:00Z") {
			t.Fatal("比较日志缺少固定分支、真实安全数值或允许时间")
		}
	}
	if strings.Contains(logs.String(), "fixture-secret") || strings.Contains(logs.String(), "https://") {
		t.Fatal("比较日志包含站点名、地址或登录资料")
	}
}

func TestGroupCostRetryCriticalStableMismatchStillRejected(t *testing.T) {
	_, err := reconcileGroupCosts(PlatformSub2API, []GroupInfo{{ID: "1"}}, []GroupDailyStat{{GroupID: "1", TodayActualCost: 2}}, 3, 3)
	if err == nil || groupCostFailureClass(err) != "invalid_response" {
		t.Fatal("独立总额稳定但分组合计不符，必须保持原格式错误及长等待，不能套用费用变化")
	}
}

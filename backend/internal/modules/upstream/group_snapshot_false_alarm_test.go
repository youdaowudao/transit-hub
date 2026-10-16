package upstream

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 20261016/4：生产 9 站的分组倍率检查误判。下面的样本都是上游真实会返回的正常数据。

func falseAlarmJSON(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func falseAlarmSub2APIMetrics(t *testing.T, available string) Metrics {
	t.Helper()
	payload := falseAlarmJSON(t, available)
	p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"balance": 10}}
		case "/api/v1/usage/dashboard/stats", "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
		case "/api/v1/groups/available":
			return 200, payload
		case "/api/v1/groups/rates":
			return 200, map[string]any{"data": map[string]any{}}
		default:
			t.Errorf("unexpected fixture request %s", r.URL.Path)
			return 500, map[string]any{}
		}
	})
	metrics, err := p.fetchSub2APIMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, "2031-01-01")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	return metrics
}

func falseAlarmNewAPIMetrics(t *testing.T, groups, pricing string) Metrics {
	t.Helper()
	groupPayload, pricePayload := falseAlarmJSON(t, groups), falseAlarmJSON(t, pricing)
	p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		switch r.URL.Path {
		case "/api/user/self":
			return 200, map[string]any{"data": map[string]any{"quota": 500000, "used_quota": 500000, "group": "a"}}
		case "/api/log/self/stat":
			return 200, map[string]any{"data": map[string]any{"quota": 3500000}}
		case "/api/user/self/groups":
			return 200, groupPayload
		case "/api/pricing":
			return 200, pricePayload
		default:
			t.Errorf("unexpected fixture request %s", r.URL.Path)
			return 500, map[string]any{}
		}
	})
	metrics, err := p.fetchNewAPIMetrics(Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", Cookie: "fixture-only", UserID: "42", QuotaPerUnit: 500000}, nil, "2031-01-01")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	return metrics
}

// A1：有编号的分组名末尾多一个空格时检查通过，显示用分组保留原名。
func TestGroupSnapshotFalseAlarmSub2APITrailingSpaceWithIDKeepsRawName(t *testing.T) {
	metrics := falseAlarmSub2APIMetrics(t, `{"data":[{"id":9,"name":"Claude-Max ","rate_multiplier":1}]}`)
	audit := metrics.GroupSnapshotAudit
	if audit == nil || !audit.AllowUpdate || !audit.AllowCleanup {
		t.Fatalf("trailing space on a group with id must not block the site: %+v", audit)
	}
	if len(metrics.Groups) != 1 || metrics.Groups[0].ID != "9" || metrics.Groups[0].Name != "Claude-Max " {
		t.Fatalf("group name must be kept as returned by upstream: %+v", metrics.Groups)
	}
}

// A2：仍要拒绝的名字问题，原因写明是哪个分组（SPEC P7—P9）。
func TestGroupSnapshotFalseAlarmSub2APINameReasonsNameTheGroup(t *testing.T) {
	missingName := auditSub2APIGroups(falseAlarmJSON(t, `{"data":[{"id":1,"rate_multiplier":1}]}`), falseAlarmJSON(t, `{"data":{}}`), nil, nil)
	if missingName == nil || missingName.AllowUpdate || missingName.Issue != "读取可用分组失败：编号 1 的分组没有名称" {
		t.Fatalf("P7 mismatch: %+v", missingName)
	}
	noNameNoID := auditSub2APIGroups(falseAlarmJSON(t, `{"data":[{"id":1,"name":"a","rate_multiplier":1},{"rate_multiplier":1}]}`), falseAlarmJSON(t, `{"data":{}}`), nil, nil)
	if noNameNoID == nil || noNameNoID.AllowUpdate || noNameNoID.Issue != "读取可用分组失败：列表第 2 个分组没有名称和编号" {
		t.Fatalf("P8 mismatch: %+v", noNameNoID)
	}
	edgeNoID := auditSub2APIGroups(falseAlarmJSON(t, `{"data":[{"name":" lonely","rate_multiplier":1}]}`), falseAlarmJSON(t, `{"data":{}}`), nil, nil)
	if edgeNoID == nil || edgeNoID.AllowUpdate ||
		!strings.HasPrefix(edgeNoID.Issue, "读取可用分组失败：没有编号的分组“") ||
		!strings.HasSuffix(edgeNoID.Issue, "”首尾带空白字符，无法按名称核对") ||
		!strings.Contains(edgeNoID.Issue, "lonely") {
		t.Fatalf("P9 mismatch: %+v", edgeNoID)
	}
}

// A3：NewAPI 组目录倍率无效时，原因带分组名（SPEC P10）。
func TestGroupSnapshotFalseAlarmNewAPIInvalidRatioNamesTheGroup(t *testing.T) {
	_, _, err := parseNewAPIGroupDirectory(falseAlarmJSON(t, `{"data":{"a":{"ratio":"broken"}}}`))
	if err == nil || err.Error() != "分组“a”的倍率无效：固定倍率缺失或不是有效的非负数值" {
		t.Fatalf("directory reason must name the group: %v", err)
	}
	audit := auditNewAPIGroups(falseAlarmJSON(t, `{"data":{"a":{"ratio":"broken"}}}`), falseAlarmJSON(t, `{"group_ratio":{},"usable_group":{"a":"a"}}`), nil, nil, nil)
	if audit == nil || audit.AllowUpdate || !strings.HasPrefix(audit.Issue, "核查组目录失败：分组“a”的倍率无效：") {
		t.Fatalf("P10 mismatch: %+v", audit)
	}
}

// A4：auto 在价格目录也有数字倍率时，显示和记录都按这个数字作为固定倍率。
// 旧代码的显示结果本来就是这样，本用例守住放行后记录与显示一致。
func TestGroupSnapshotFalseAlarmNewAPIPricedAutoStaysFixed(t *testing.T) {
	metrics := falseAlarmNewAPIMetrics(t, `{"data":{"a":{"ratio":2},"auto":{"ratio":"auto"}}}`, `{"group_ratio":{"a":2,"auto":1},"usable_group":{"a":"a","auto":"x"}}`)
	var found bool
	for _, group := range snapshotGroups(metrics.Groups) {
		if group.Name != "auto" {
			continue
		}
		found = true
		if group.MultiplierMode != "fixed" || group.Multiplier == nil || *group.Multiplier != 1 {
			t.Fatalf("priced auto must be a fixed ratio of 1: %+v", group)
		}
	}
	for _, group := range metrics.Groups {
		if group.Name == "auto" && (group.MultiplierMode != "fixed" || group.Multiplier == nil || *group.Multiplier != 1) {
			t.Fatalf("displayed priced auto must be a fixed ratio of 1: %+v", group)
		}
	}
	if !found {
		t.Fatal("priced auto group missing")
	}
}

// A5：价格目录可用分组没有倍率时只跳过该组，其余照常确认；清理资格仍只看目录是否完整。
func TestGroupSnapshotFalseAlarmNewAPIUsableGroupWithoutRatio(t *testing.T) {
	now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, groups string
		cleanup      bool
	}{
		{"complete directory", `{"data":{"a":{"ratio":2}}}`, true},
		{"partial directory", `{"data":{"a":{"ratio":2}},"has_more":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := falseAlarmNewAPIMetrics(t, tc.groups, `{"group_ratio":{"a":2},"usable_group":{"a":"a","vip":"User group"}}`)
			if len(metrics.Groups) != 1 || metrics.Groups[0].Name != "a" {
				t.Fatalf("group without a ratio must not be recorded: %+v", metrics.Groups)
			}
			if observation := observationFromGroupAudit(metrics.GroupSnapshotAudit, now); !observation.Confirmed {
				t.Fatalf("other groups must still be confirmed: %+v", observation)
			}
			if input := currentRateNoticeInput(metrics, nil, now); input.AllowCleanup != tc.cleanup {
				t.Fatalf("notice cleanup eligibility want=%v got=%v", tc.cleanup, input.AllowCleanup)
			}
		})
	}
}

// N1：真实数据库。有编号、名字带尾空格的分组能建立调价比较记录，并在调价时提醒。
func TestGroupSnapshotFalseAlarmTrailingSpaceRateNoticePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "Claude-Max "}
	svc, _ := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 0)
	sample.rate = 2
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
	var confirmed bool
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE((metrics->'rateNoticeObservation'->>'confirmed')::boolean,false) FROM upstream_sites WHERE id='site'`).Scan(&confirmed); err != nil || !confirmed {
		t.Fatalf("trailing-space group with id must be confirmed: confirmed=%v err=%v", confirmed, err)
	}
}

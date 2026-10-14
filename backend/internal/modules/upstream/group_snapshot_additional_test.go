package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGroupSnapshotAdditionalSuccessfulHTTPDamagedRatesPreservesOldWriterEligibility(t *testing.T) {
	for _, tc := range []struct{ name, rates string }{{"business refusal", `{"success":false,"message":"专属倍率读取暂时禁止"}`}, {"broken JSON", `{"data":`}} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"data":{"total_actual_cost":7,"balance":10}}`
				switch r.URL.Path {
				case "/api/v1/groups/available":
					body = `{"data":[{"id":1,"name":"a","rate_multiplier":2}]}`
				case "/api/v1/groups/rates":
					body = tc.rates
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			platform := NewPlatformService(NewHTTPClient(client))
			metrics, err := platform.fetchSub2APIMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, "2031-01-01")
			if err != nil || len(metrics.Groups) != 1 || metrics.Groups[0].Multiplier == nil || *metrics.Groups[0].Multiplier != 2 || metrics.TodayConsume.Value == nil || *metrics.TodayConsume.Value != 7 {
				t.Fatal("new audit changed shared default fallback or independent cost")
			}
			audit := metrics.GroupSnapshotAudit
			if audit == nil || audit.AllowUpdate || audit.AllowCleanup || audit.Issue == "" {
				t.Fatalf("successful HTTP damage must preserve old PG rates, got %+v", audit)
			}
			svc := NewService(nil, nil, &additionalSnapshotWriter{}, newSyncTestCache())
			defer svc.Close()
			warning := svc.saveSnapshot(context.Background(), groupGuardSite(t), audit, "fixture-version")
			if warning == nil || !strings.Contains(warning.Detail, "旧倍率和历史仍保留") {
				t.Fatal("successful HTTP damage lacked rate-preservation explanation")
			}
		})
	}
}

func TestGroupSnapshotAdditionalBusinessHintIsRedactedAndRetained(t *testing.T) {
	client := &http.Client{Transport: keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"total_actual_cost":7,"balance":10}}`
		switch r.URL.Path {
		case "/api/v1/groups/available":
			body = `{"code":9,"message":"网站维护中，令牌fixture-only请勿重试","data":[]}`
		case "/api/v1/groups/rates":
			body = `{"data":{}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	platform := NewPlatformService(NewHTTPClient(client))
	metrics, err := platform.fetchSub2APIMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, "2031-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.GroupSnapshotAudit == nil || metrics.GroupSnapshotAudit.AllowUpdate || !strings.Contains(metrics.GroupSnapshotAudit.Issue, "网站维护中") || strings.Contains(metrics.GroupSnapshotAudit.Issue, "fixture-only") {
		t.Fatal("direct upstream business hint was lost or credential was exposed")
	}
}

func TestGroupSnapshotAdditionalNestedPaginationOnlyReducesCleanup(t *testing.T) {
	groups := map[string]any{"data": map[string]any{"a": map[string]any{"ratio": 2.0}, "total": "models", "pagination": map[string]any{"page": "unknown"}}}
	pricing := map[string]any{"group_ratio": map[string]any{"a": 3.0}, "usable_group": map[string]any{"a": "a"}}
	normalized := newAPIGroups(groups, pricing)
	audit := auditNewAPIGroups(groups, pricing, nil, nil, normalized)
	if audit == nil || !audit.AllowUpdate || audit.AllowCleanup {
		t.Fatalf("valid groups with nested pagination must update without cleanup, got %+v", audit)
	}
}

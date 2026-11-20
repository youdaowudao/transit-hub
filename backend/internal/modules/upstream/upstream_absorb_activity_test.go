package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAbsorbActivityStrictUsagePayload(t *testing.T) {
	valid := `{"user_id":1,"date":"2026-11-19","requests":3,"tokens":0,"actual_cost":0}`
	for _, tc := range []struct {
		name, body string
		limit      int
		good       bool
	}{
		{"free requests", `{"data":{"trend":[` + valid + `]}}`, 100, true},
		{"empty is explicit", `{"data":{"trend":[]}}`, 100, true},
		{"missing array", `{"data":{}}`, 100, false}, {"null array", `{"data":{"trend":null}}`, 100, false},
		{"string money", `{"data":{"trend":[` + strings.Replace(valid, `"actual_cost":0`, `"actual_cost":"0"`, 1) + `]}}`, 100, false},
		{"missing requests", `{"data":{"trend":[` + strings.Replace(valid, `"requests":3,`, "", 1) + `]}}`, 100, false},
		{"fractional requests", `{"data":{"trend":[` + strings.Replace(valid, `"requests":3`, `"requests":1.5`, 1) + `]}}`, 100, false},
		{"unsafe integer", `{"data":{"trend":[` + strings.Replace(valid, `"requests":3`, `"requests":9007199254740992`, 1) + `]}}`, 100, false},
		{"conflicting alias", `{"data":{"trend":[` + strings.Replace(valid, `"actual_cost":0`, `"actual_cost":0,"actualCost":1`, 1) + `]}}`, 100, false},
		{"wrong date", `{"data":{"trend":[` + strings.Replace(valid, "2026-11-19", "2026-11-20", 1) + `]}}`, 100, false},
		{"duplicate", `{"data":{"trend":[` + valid + `,` + valid + `]}}`, 100, false},
		{"at limit", `{"data":{"trend":[` + valid + `]}}`, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: absorbProbeTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/admin/dashboard/users-trend" || r.URL.Query().Get("timezone") != "Asia/Singapore" || r.URL.Query().Get("metric") != "actual_cost" {
					t.Fatal("new read changed query contract")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}, Request: r}, nil
			})}))
			rows, err := service.FetchSub2APIUserUsageTrendContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}, "2026-11-19", "2026-11-19", tc.limit)
			if (err == nil) != tc.good {
				t.Fatal("strict payload validation changed")
			}
			if tc.good && tc.name == "free requests" && (len(rows) != 1 || rows[0].Requests != 3 || rows[0].ActualCost != 0) {
				t.Fatal("zero-cost requests were discarded")
			}
		})
	}
}

func TestAbsorbActivityStrictIncomePayload(t *testing.T) {
	valid := `{"id":1,"code":"PAY-fixture","type":"balance","used_by":2,"used_at":"2026-11-19T00:00:00Z","value":0}`
	for _, tc := range []struct {
		name, body string
		good       bool
	}{
		{"explicit zero", `{"data":{"items":[` + valid + `],"total":1}}`, true}, {"explicit empty", `{"data":{"items":[],"total":0}}`, true},
		{"missing items", `{"data":{"total":0}}`, false}, {"null items", `{"data":{"items":null}}`, false},
		{"missing money", `{"items":[` + strings.Replace(valid, `,"value":0`, "", 1) + `]}`, false},
		{"wrong type", `{"items":[` + strings.Replace(valid, `"type":"balance"`, `"type":"admin_balance"`, 1) + `]}`, false},
		{"invalid time", `{"items":[` + strings.Replace(valid, "2026-11-19T00:00:00Z", "yesterday", 1) + `]}`, false},
		{"invalid id", `{"items":[` + strings.Replace(valid, `"id":1`, `"id":1.5`, 1) + `]}`, false},
		{"conflicting arrays", `{"items":[],"data":{"items":[` + valid + `]}}`, false},
		{"bad total", `{"items":[],"total":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := NewPlatformService(NewHTTPClient(&http.Client{Transport: absorbProbeTransport(func(r *http.Request) (*http.Response, error) {
				q := r.URL.Query()
				if r.Method != "GET" || q.Get("type") != "balance" || q.Get("status") != "used" || q.Get("sort_by") != "used_at" || q.Get("sort_order") != "desc" {
					t.Fatal("new income reader changed its stable query")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}, Request: r}, nil
			})}))
			_, err := service.FetchSub2APIUsedRedeemRecordsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}, "balance", 1, 500)
			if (err == nil) != tc.good {
				t.Fatal("strict income validation changed")
			}
		})
	}
}

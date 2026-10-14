package upstream

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Primary-agent expectations: raw group safety only gates the rate writer;
// the existing metrics/groups/cost interpretation stays independently useful.
func TestGroupSnapshotCriticalSub2APIRawEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, available, rates string
		status                 int
		update, cleanup        bool
	}{
		{"fixed", "{\"data\":[{\"id\":1,\"name\":\"a\",\"rate_multiplier\":2}]}", `{"data":{"1":3}}`, 200, true, true},
		{"zero", `{"data":[{"id":1,"name":"a","rate_multiplier":0}]}`, `{"data":{}}`, 200, true, true},
		{"complete empty", `{"data":[]}`, `{"data":{}}`, 200, true, true},
		{"missing id unique name", `{"data":[{"name":"a","rate_multiplier":1}]}`, `{"data":{}}`, 200, true, true},
		{"optional 404", `{"data":[{"id":1,"name":"a","rate_multiplier":2}]}`, `{"message":"not available"}`, 404, true, true},
		{"unknown override does not add", `{"data":[{"id":1,"name":"a","rate_multiplier":2}]}`, `{"data":{"99":7}}`, 200, true, true},
		{"null not empty", `{"data":null}`, `{"data":{}}`, 200, false, false},
		{"unknown not empty", `{"unknown":[]}`, `{"data":{}}`, 200, false, false},
		{"object not empty", `{"data":{}}`, `{"data":{}}`, 200, false, false},
		{"missing rate", `{"data":[{"id":1,"name":"a"}]}`, `{"data":{}}`, 200, false, false},
		{"negative rate", `{"data":[{"id":1,"name":"a","rate_multiplier":-1}]}`, `{"data":{}}`, 200, false, false},
		{"missing name", `{"data":[{"id":1,"rate_multiplier":1}]}`, `{"data":{}}`, 200, false, false},
		{"fractional id", `{"data":[{"id":1.5,"name":"a","rate_multiplier":1}]}`, `{"data":{}}`, 200, false, false},
		{"unsafe numeric id", `{"data":[{"id":9007199254740992,"name":"a","rate_multiplier":1}]}`, `{"data":{}}`, 200, false, false},
		{"duplicate id", `{"data":[{"id":1,"name":"a","rate_multiplier":1},{"id":1,"name":"b","rate_multiplier":1}]}`, `{"data":{}}`, 200, false, false},
		{"duplicate blank identity", `{"data":[{"name":"a","rate_multiplier":1},{"name":"a","rate_multiplier":2}]}`, `{"data":{}}`, 200, false, false},
		{"remaining page", `{"data":[{"id":1,"name":"a","rate_multiplier":1}],"has_more":true}`, `{"data":{}}`, 200, false, false},
		{"count mismatch", `{"data":[{"id":1,"name":"a","rate_multiplier":1}],"total":2}`, `{"data":{}}`, 200, false, false},
		{"conflicting arrays", `{"data":[],"items":[{"id":1,"name":"a","rate_multiplier":1}]}`, `{"data":{}}`, 200, false, false},
		{"successful damaged override", `{"data":[{"id":1,"name":"a","rate_multiplier":1}]}`, `{"data":{"1":"broken"}}`, 200, false, false},
		{"HTTP200 rejected override", `{"data":[{"id":1,"name":"a","rate_multiplier":1}]}`, `{"success":false,"message":"倍率覆盖读取被拒绝"}`, 200, false, false},
		{"HTTP200 nonzero override code", `{"data":[{"id":1,"name":"a","rate_multiplier":1}]}`, `{"code":5,"message":"倍率覆盖资料暂不可读"}`, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := map[string]int{}
			p := singaporeCostPlatform(t, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
				calls[r.URL.Path]++
				switch r.URL.Path {
				case "/api/v1/auth/me":
					return 200, map[string]any{"data": map[string]any{"balance": 10}}
				case "/api/v1/usage/dashboard/stats", "/api/v1/usage/stats":
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
				case "/api/v1/groups/available":
					var v any
					if err := json.Unmarshal([]byte(tc.available), &v); err != nil {
						t.Fatal(err)
					}
					return 200, v
				case "/api/v1/groups/rates":
					var v any
					if err := json.Unmarshal([]byte(tc.rates), &v); err != nil {
						t.Fatal(err)
					}
					return tc.status, v
				default:
					t.Errorf("unexpected raw fixture request %s", r.URL.Path)
					return 500, map[string]any{}
				}
			})
			metrics, err := p.fetchSub2APIMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"}, "2031-01-01")
			if err != nil {
				t.Fatalf("newly recognized group damage must not block independent metrics: %v", err)
			}
			a := metrics.GroupSnapshotAudit
			if a == nil || a.AllowUpdate != tc.update || a.AllowCleanup != tc.cleanup {
				t.Fatalf("raw eligibility update/cleanup want=%v/%v actual=%+v", tc.update, tc.cleanup, a)
			}
			if !tc.update && a.Issue == "" {
				t.Fatal("damaged group missing direct safe reason")
			}
			if metrics.TodayConsume.Value == nil || *metrics.TodayConsume.Value != 7 {
				t.Fatal("group audit damaged independent cost")
			}
			if calls["/api/v1/groups/available"] != 1 || calls["/api/v1/groups/rates"] != 1 {
				t.Fatal("audit added upstream group requests")
			}
			raw, _ := json.Marshal(metrics)
			if strings.Contains(string(raw), "SnapshotAudit") || strings.Contains(string(raw), "AllowCleanup") {
				t.Fatal("one-request audit leaked into persistent DTO")
			}
		})
	}
}

func TestGroupSnapshotCriticalSub2APIMalformedHTTP200Override(t *testing.T) {
	p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeJSON(w, map[string]any{"data": map[string]any{"balance": 10}})
		case "/api/v1/usage/dashboard/stats", "/api/v1/usage/stats":
			writeJSON(w, map[string]any{"data": map[string]any{"total_actual_cost": 7}})
		case "/api/v1/groups/available":
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": 1, "name": "a", "rate_multiplier": 2}}})
		case "/api/v1/groups/rates":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"data":broken`)
		default:
			t.Errorf("unexpected raw path %s", r.URL.Path)
		}
	})
	metrics, err := p.fetchSub2APIMetrics(Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	if err != nil {
		t.Fatalf("optional groups fallback/independent cost changed: %v", err)
	}
	if metrics.GroupSnapshotAudit == nil || metrics.GroupSnapshotAudit.AllowUpdate || metrics.GroupSnapshotAudit.AllowCleanup {
		t.Fatalf("malformed successful override authorized rate persistence: %+v", metrics.GroupSnapshotAudit)
	}
	if len(metrics.Groups) != 1 || metrics.Groups[0].Multiplier == nil || *metrics.Groups[0].Multiplier != 2 || metrics.TodayConsume.Value == nil || *metrics.TodayConsume.Value != 7 {
		t.Fatal("new audit changed original default fallback or independent cost")
	}
}

func TestGroupSnapshotCriticalNewAPIRawEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, groups, pricing string
		update, cleanup       bool
	}{
		{"complete fixed", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"},"data":[]}`, true, true},
		{"zero and numeric string", `{"data":{"a":{"rate":"0"}}}`, `{"group_ratio":{"a":"0"},"usable_group":{"a":"a"}}`, true, true},
		{"complete empty object", `{"data":{}}`, `{"group_ratio":{},"usable_group":{},"data":[]}`, true, true},
		{"complete empty array", `{"data":[]}`, `{"data":{"group_ratio":{},"usable_group":{}}}`, true, true},
		{"model-only empty not a group empty", `{"data":{}}`, `{"data":[]}`, true, false},
		{"old object current ratio", `{"data":{"a":{"multiplier":"2"}}}`, `{"data":[]}`, true, false},
		{"legacy simple conversion loss", `{"data":{"a":2}}`, `{"data":[]}`, false, false},
		{"bare empty unknown", `{}`, `{"group_ratio":{},"usable_group":{}}`, false, false},
		{"bare nonempty object", `{"a":{"ratio":2}}`, `{"data":[]}`, true, false},
		{"only one price directory", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":3},"data":[]}`, true, false},
		{"legal auto", `{"data":{"auto":{}}}`, `{"group_ratio":{},"usable_group":{"auto":"auto"}}`, true, true},
		{"bad fixed is not auto", `{"data":{"a":{"ratio":"broken"}}}`, `{"group_ratio":{},"usable_group":{"a":"a"}}`, false, false},
		{"bad explicit auto rate", `{"data":{"auto":{"ratio":"broken"}}}`, `{"group_ratio":{},"usable_group":{"auto":"auto"}}`, false, false},
		{"trailing whitespace conversion loss", `{"data":{"a ":{"ratio":2}}}`, `{"group_ratio":{},"usable_group":{}}`, false, false},
		{"negative override", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":-3},"usable_group":{"a":"a"}}`, false, false},
		{"invalid usable value", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":3},"usable_group":{"a":1}}`, false, false},
		{"partial first page", `{"data":{"a":{"ratio":2}},"total":2,"has_more":true}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"}}`, true, false},
		{"declared last page still partial", `{"data":{"a":{"ratio":2}},"has_more":false}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"}}`, true, false},
		{"one model two groups total one", `{"data":{"a":{"ratio":2},"b":{"ratio":4}}}`, `{"group_ratio":{"a":3,"b":5},"usable_group":{"a":"a","b":"b"},"data":[{"owner_by":"openai","enable_groups":["a","b"]}],"total":1,"has_more":false}`, true, false},
		{"unknown pagination types", `{"data":{"a":{"ratio":2}},"pagination":"unknown"}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"},"total":"models"}`, true, false},
		{"conflicting nested directory", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"},"data":{"group_ratio":{"a":4},"usable_group":{"a":"a"}}}`, false, false},
		{"matching nested directory", `{"data":{"a":{"ratio":2}}}`, `{"group_ratio":{"a":3},"usable_group":{"a":"a"},"data":{"group_ratio":{"a":3},"usable_group":{"a":"a"}}}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var groupPayload, pricePayload any
			if err := json.Unmarshal([]byte(tc.groups), &groupPayload); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.pricing), &pricePayload); err != nil {
				t.Fatal(err)
			}
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
					t.Errorf("unexpected raw fixture request %s", r.URL.Path)
					return 500, map[string]any{}
				}
			})
			metrics, err := p.fetchNewAPIMetrics(Session{Platform: PlatformNewAPI, BaseURL: "http://fixture.invalid", Cookie: "fixture-only", UserID: "42", QuotaPerUnit: 500000}, nil, "2031-01-01")
			if err != nil {
				t.Fatalf("new audit must preserve original metrics result: %v", err)
			}
			a := metrics.GroupSnapshotAudit
			if a == nil || a.AllowUpdate != tc.update || a.AllowCleanup != tc.cleanup {
				t.Fatalf("raw eligibility update/cleanup want=%v/%v actual=%+v", tc.update, tc.cleanup, a)
			}
			if (!tc.update || !tc.cleanup) && a.Issue == "" {
				t.Fatal("unconfirmed directory missing safe explanation")
			}
			if metrics.TodayConsume.Value == nil || *metrics.TodayConsume.Value != 7 {
				t.Fatal("group eligibility changed independent cost")
			}
			got := append([]GroupInfo{}, metrics.Groups...)
			want := newAPIGroups(groupPayload, pricePayload)
			sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })
			sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
			if !reflect.DeepEqual(got, want) {
				t.Fatal("audit changed shared Groups/auto/model platform interpretation")
			}
		})
	}
}

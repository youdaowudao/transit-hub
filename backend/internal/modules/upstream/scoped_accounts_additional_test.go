package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type scopedOptionCountingCache struct {
	*syncTestCache
	GroupCostStore
	lists, costs int
	listErr      error
}

func (c *scopedOptionCountingCache) ListByUser(ctx context.Context, user string) ([]*Site, error) {
	c.lists++
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.syncTestCache.ListByUser(ctx, user)
}
func (c *scopedOptionCountingCache) GetGroupCostSamplingState(context.Context, string) (GroupCostSamplingState, error) {
	c.costs++
	return GroupCostSamplingState{}, nil
}
func (c *scopedOptionCountingCache) ListGroupCostSamples(context.Context, string, string, string) ([]GroupCostSample, error) {
	c.costs++
	return nil, nil
}

type scopedOptionsTransport struct{ calls int }

func (t *scopedOptionsTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("options must not contact a remote website")
}

func TestScopedSiteOptionsCountsReadsAndPropagatesCacheFailure(t *testing.T) {
	enabled, disabled := true, false
	cache := &scopedOptionCountingCache{syncTestCache: newSyncTestCache(&Site{ID: "enabled", UserID: "u", AdminAccountID: "w", Name: "Enabled", Enabled: &enabled, Metrics: Metrics{Groups: []GroupInfo{{ID: "1", Name: "Group"}}}}, &Site{ID: "disabled", UserID: "u", AdminAccountID: "w", Name: "Disabled", Enabled: &disabled})}
	transport := &scopedOptionsTransport{}
	svc := NewService(NewPlatformService(NewHTTPClient(&http.Client{Transport: transport})), nil, nil, cache)
	v, err := svc.ListOptions(context.Background(), "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 2 || cache.lists != 1 || cache.costs != 0 || transport.calls != 0 {
		t.Fatalf("options reads: items=%d cache=%d cost=%d remote=%d", len(v.Items), cache.lists, cache.costs, transport.calls)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, raw := range payload["items"].([]any) {
		item := raw.(map[string]any)
		// The approved account-consumption summary is the only additional field.
		if len(item) != 3 || item["id"] == nil || item["name"] == nil {
			t.Fatal("options added fields outside the approved account summary")
		}
		summary, ok := item["consumption"].(map[string]any)
		if !ok || summary["balance"] == nil || summary["rechargeRate"] == nil {
			t.Fatal("options missing the saved account summary")
		}
		allowed := map[string]bool{"balance": true, "rechargeRate": true}
		for field := range summary {
			if !allowed[field] {
				t.Fatalf("options leaked an unrelated account field: %s", field)
			}
		}
	}
	sentinel := errors.New("fixture cache unavailable")
	cache.listErr = sentinel
	if _, err := svc.ListOptions(context.Background(), "u", "w"); !errors.Is(err, sentinel) {
		t.Fatal("cache failure was swallowed into empty success")
	}
	if cache.costs != 0 || transport.calls != 0 {
		t.Fatal("failed options fell back to cost or remote reads")
	}
}

func TestScopedAccountExistenceAdditional(t *testing.T) {
	cases := []struct {
		name, payload string
		status        int
		found         bool
	}{
		{"disabled remains present", `{"code":0,"data":{"id":101,"status":"disabled"}}`, 200, true},
		{"no unrelated fields required", `{"code":0,"success":true,"data":{"id":"101"}}`, 200, true},
		{"wrong ID", `{"code":0,"data":{"id":102}}`, 200, false},
		{"fractional ID", `{"code":0,"data":{"id":101.5}}`, 200, false},
		{"business failure", `{"code":9,"message":"账号读取维护中","data":{"id":101}}`, 200, false},
		{"missing code", `{"data":{"id":101}}`, 200, false},
		{"string code", `{"code":"0","data":{"id":101}}`, 200, false},
		{"invalid success", `{"code":0,"success":"true","data":{"id":101}}`, 200, false},
		{"not JSON", `Account gone`, 200, false},
		{"404 does not prove deletion", `{"message":"账号详情未找到"}`, 404, false},
		{"403 reason preserved", `{"message":"该账号详情仅允许维护员读取 token=fixture-secret"}`, 403, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/admin/accounts/101" {
					t.Errorf("wrong detail path")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			s := NewPlatformService(NewHTTPClient(server.Client()))
			found, err := s.ReadSub2APIAdminAccountExistenceContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-secret"}, "101")
			if found != tc.found || (err == nil) != tc.found {
				t.Fatalf("found=%t err=%v", found, err)
			}
			if err != nil {
				encoded, _ := json.Marshal(err)
				if strings.Contains(string(encoded), "fixture-secret") {
					t.Fatal("sensitive upstream hint escaped")
				}
			}
		})
	}
}
func TestScopedStrictInventoryAdditional(t *testing.T) {
	cases := []struct {
		name, payload string
		valid         bool
	}{
		{"empty complete", `{"code":0,"data":{"items":[],"total":0}}`, true},
		{"valid exact IDs", `{"code":0,"success":true,"data":{"items":[{"id":101},{"id":"102"}],"total":2}}`, true},
		{"code rejects HTTP200", `{"code":3,"data":{"items":[],"total":0}}`, false},
		{"missing code", `{"data":{"items":[],"total":0}}`, false},
		{"fractional code", `{"code":0.5,"data":{"items":[],"total":0}}`, false},
		{"false success", `{"code":0,"success":false,"data":{"items":[],"total":0}}`, false},
		{"string success", `{"code":0,"success":"true","data":{"items":[],"total":0}}`, false},
		{"fractional ID", `{"code":0,"data":{"items":[{"id":101.1}],"total":1}}`, false},
		{"unsafe number", `{"code":0,"data":{"items":[{"id":9007199254740992}],"total":1}}`, false},
		{"overflow ID", `{"code":0,"data":{"items":[{"id":"18446744073709551616"}],"total":1}}`, false},
		{"leading zero", `{"code":0,"data":{"items":[{"id":"0101"}],"total":1}}`, false},
		{"duplicate IDs", `{"code":0,"data":{"items":[{"id":101},{"id":"101"}],"total":2}}`, false},
		{"truncated page", `{"code":0,"data":{"items":[{"id":101}],"total":2}}`, false},
		{"total above limit", `{"code":0,"data":{"items":[],"total":10001}}`, false},
		{"contradictory totals", `{"code":0,"total":1,"data":{"items":[],"total":0}}`, false},
		{"contradictory containers", `{"code":0,"items":[{"id":101}],"data":{"items":[],"total":0}}`, false},
		{"missing total", `{"code":0,"data":{"items":[]}}`, false},
		{"Review1 terminal says more", `{"code":0,"data":{"items":[],"total":0,"has_more":true}}`, false},
		{"Review1 wrong page", `{"code":0,"data":{"items":[],"total":0,"page":2}}`, false},
		{"Review1 wrong page size", `{"code":0,"data":{"items":[],"total":0,"page_size":20}}`, false},
		{"Review1 contradictory nested pagination", `{"code":0,"data":{"items":[],"total":0,"has_more":false,"pagination":{"has_more":true}}}`, false},
		{"Review1 terminal next page", `{"code":0,"data":{"items":[],"total":0,"next_page":2}}`, false},
		{"Review1 terminal total pages", `{"code":0,"data":{"items":[],"total":0,"total_pages":2}}`, false},
		{"Review1 valid explicit terminal", `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"has_more":false}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page_size") != "100" || r.URL.Query().Get("page") != "1" {
					t.Error("inventory request shape changed")
				}
				fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			s := NewPlatformService(NewHTTPClient(server.Client()))
			ids, err := s.ListSub2APIAdminAccountIDsStrictContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
			if (err == nil) != tc.valid {
				t.Fatalf("inventory validity=%t err=%v", tc.valid, err)
			}
			if !tc.valid && ids != nil {
				t.Fatal("partial ID evidence returned on failure")
			}
		})
	}
	t.Run("later-page total change returns no IDs", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			items := []map[string]any{}
			total := 101
			if calls == 1 {
				for i := 1; i <= 100; i++ {
					items = append(items, map[string]any{"id": i})
				}
			} else {
				items = append(items, map[string]any{"id": 101})
				total = 102
			}
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": items, "total": total}})
		}))
		defer server.Close()
		s := NewPlatformService(NewHTTPClient(server.Client()))
		ids, err := s.ListSub2APIAdminAccountIDsStrictContext(context.Background(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture"})
		if err == nil || ids != nil || calls != 2 {
			t.Fatalf("changing inventory accepted calls=%d", calls)
		}
	})
}
func TestScopedSiteOptionsAdditional(t *testing.T) {
	disabled := false
	cache := newSyncTestCache(&Site{ID: "a", UserID: "user", AdminAccountID: "workspace", Name: "A", Enabled: &disabled}, &Site{ID: "b", UserID: "user", AdminAccountID: "other", Name: "B"})
	svc := NewService(nil, nil, nil, cache)
	result, err := svc.ListOptions(context.Background(), "user", "workspace")
	if err != nil || len(result.Items) != 1 || result.Items[0].ID != "a" {
		t.Fatalf("options=%#v err=%v", result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "metrics") || strings.Contains(string(raw), "platform") || strings.Contains(string(raw), "session") {
		t.Fatal("options leaked full site response")
	}
}

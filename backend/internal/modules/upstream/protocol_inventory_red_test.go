package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type protocolInventoryTransport func(*http.Request) (*http.Response, error)

func (f protocolInventoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// T05: HTTP 200 is insufficient evidence of a complete inventory. These tests
// exercise the existing HTTP boundary without opening a port or external service.
func TestProtocolContractGroupInventory(t *testing.T) {
	cases := []struct {
		name, body string
		valid      bool
		count      int
	}{
		{"unknown_container", `{"data":{"unexpected":[]}}`, false, 0},
		{"partial_total", `{"data":{"items":[{"id":1,"name":"a"}],"total":2}}`, false, 0},
		{"empty_but_has_more", `{"data":{"items":[],"total":0,"has_more":true}}`, false, 0},
		{"wrong_page", `{"data":{"items":[{"id":1,"name":"a"}],"total":1,"page":2}}`, false, 0},
		{"fractional_id", `{"data":[{"id":1.5,"name":"a"}]}`, false, 0},
		{"duplicate_id", `{"data":[{"id":1,"name":"a"},{"id":"1","name":"b"}]}`, false, 0},
		{"missing_name_keeps_membership", `{"data":[{"id":1}]}`, true, 1},
		{"complete_empty", `{"data":[]}`, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/api/v1/admin/groups" {
					t.Errorf("unexpected query route: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			service := NewPlatformService(NewHTTPClient(client))
			groups, err := service.fetchSub2APIAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"})
			if (err == nil) != tc.valid {
				t.Errorf("complete=%v, want %v; groups=%d err=%v", err == nil, tc.valid, len(groups), err)
			}
			if tc.valid && len(groups) != tc.count {
				t.Errorf("retained groups=%d, want %d", len(groups), tc.count)
			}
			if calls != 1 {
				t.Errorf("group query count=%d, want original single read", calls)
			}
		})
	}
}

func TestProtocolContractMemberInventory(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty_but_has_more", `{"data":{"items":[],"total":0,"has_more":true}}`},
		{"wrong_page", `{"data":{"items":[{"id":1}],"total":1,"page":2,"page_size":100}}`},
		{"wrong_page_size", `{"data":{"items":[{"id":1}],"total":1,"page":1,"page_size":10}}`},
		{"fractional_id", `{"data":{"items":[{"id":1.5}],"total":1}}`},
		{"negative_id", `{"data":{"items":[{"id":-1}],"total":1}}`},
		{"short_page_with_next", `{"data":{"items":[{"id":1}],"has_more":true}}`},
		{"total_reached_with_next", `{"data":{"items":[{"id":1}],"total":1,"next":2}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("group") != "1" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
					t.Errorf("changed query range: %s", r.URL.RawQuery)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			service := NewPlatformService(NewHTTPClient(client))
			accounts, err := service.ListAdminGroupAccountsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}, AdminGroupInfo{ID: "1"})
			if err == nil || accounts != nil {
				t.Errorf("contradictory inventory accepted: accounts=%v err=%v", accounts, err)
			}
			if calls != 1 {
				t.Errorf("contradictory metadata must stop this round, calls=%d", calls)
			}
		})
	}
}

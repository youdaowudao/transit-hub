package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func groupInventoryPage(page, pageSize, total int) map[string]any {
	items := make([]map[string]any, 0)
	for id := (page-1)*pageSize + 1; id <= page*pageSize && id <= total; id++ {
		items = append(items, map[string]any{"id": id, "name": "group-" + strconv.Itoa(id), "platform": "openai"})
	}
	return map[string]any{"data": map[string]any{"items": items, "total": total, "page": page, "page_size": pageSize, "pages": (total + pageSize - 1) / pageSize}}
}

func groupInventoryResponse(t *testing.T, r *http.Request, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}
}

func TestSub2APIGroupInventoryProduction23GroupsAreOneCompleteList(t *testing.T) {
	entries := []struct {
		name string
		read func(*PlatformService, Session) ([]AdminGroupInfo, error)
	}{
		{"health_context", func(s *PlatformService, session Session) ([]AdminGroupInfo, error) {
			return s.FetchAdminAllGroupsContext(t.Context(), session)
		}},
		{"shared_groups", func(s *PlatformService, session Session) ([]AdminGroupInfo, error) {
			return s.FetchAdminAllGroups(session)
		}},
		{"sub2api_groups", func(s *PlatformService, session Session) ([]AdminGroupInfo, error) {
			return s.FetchSub2APIAdminAllGroups(session)
		}},
	}
	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Host != "fixture.invalid" || r.URL.Path != "/api/v1/admin/groups" {
					t.Fatalf("changed address or query route: %s %s", r.Method, r.URL)
				}
				page, size := 1, 20 // Actual production defaults: 20 items, total 23, pages 2.
				if raw := r.URL.Query().Get("page"); raw != "" {
					page, _ = strconv.Atoi(raw)
				}
				if raw := r.URL.Query().Get("page_size"); raw != "" {
					size, _ = strconv.Atoi(raw)
				}
				if page < 1 || size < 1 {
					t.Fatal("invalid page request")
				}
				return groupInventoryResponse(t, r, groupInventoryPage(page, size, 23)), nil
			})}
			groups, err := entry.read(NewPlatformService(NewHTTPClient(client)), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
			if err != nil || len(groups) != 23 {
				t.Fatalf("production inventory must return all 23 groups in one list: count=%d err=%v", len(groups), err)
			}
			for i, group := range groups {
				if group.ID != strconv.Itoa(i+1) || group.Platform != "openai" {
					t.Fatalf("group lost, duplicated, or provider changed at index %d: %#v", i, group)
				}
			}
			if calls != 1 {
				t.Fatalf("23 groups should fit one explicit 100-item read, calls=%d", calls)
			}
		})
	}
}

func TestSub2APIGroupInventoryMergesEveryPageWithoutLosingGroups(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("page") != strconv.Itoa(calls) || r.URL.Query().Get("page_size") != "100" {
			t.Errorf("unexpected page request: %s", r.URL.RawQuery)
		}
		return groupInventoryResponse(t, r, groupInventoryPage(calls, 100, 103)), nil
	})}
	groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
	if err != nil || len(groups) != 103 || calls != 2 {
		t.Fatalf("all pages must merge into one list: count=%d calls=%d err=%v", len(groups), calls, err)
	}
	for i, group := range groups {
		if group.ID != strconv.Itoa(i+1) {
			t.Fatalf("group identity changed across pages: index=%d id=%s", i, group.ID)
		}
	}
}

func TestSub2APIGroupInventoryRejectsLaterPageProblemsWithoutPartialResults(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"total_changes", func(d map[string]any) { d["total"] = 102 }},
		{"total_disappears", func(d map[string]any) { delete(d, "total") }},
		{"wrong_page", func(d map[string]any) { d["page"] = 1 }},
		{"wrong_page_size", func(d map[string]any) { d["page_size"] = 20 }},
		{"duplicate_across_pages", func(d map[string]any) { d["items"] = []map[string]any{{"id": 100}} }},
		{"fractional_id", func(d map[string]any) { d["items"] = []map[string]any{{"id": 101.5}} }},
		{"empty_before_total", func(d map[string]any) { d["items"] = []any{} }},
		{"terminal_with_next", func(d map[string]any) { d["has_more"] = true }},
		{"terminal_pages_says_more", func(d map[string]any) { d["pages"] = 3 }},
		{"unknown_container", func(d map[string]any) { delete(d, "items"); d["unexpected"] = []any{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				payload := groupInventoryPage(calls, 100, 101)
				if calls == 2 {
					tc.mutate(payload["data"].(map[string]any))
				}
				return groupInventoryResponse(t, r, payload), nil
			})}
			groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
			if err == nil || groups != nil || calls != 2 {
				t.Fatalf("bad second page must discard the entire list: count=%d calls=%d err=%v", len(groups), calls, err)
			}
		})
	}
}

func TestSub2APIGroupInventoryPreservesLegacyWholeArray(t *testing.T) {
	for _, total := range []int{0, 200} {
		for _, metadata := range []struct {
			name   string
			fields map[string]any
		}{
			{"plain", nil},
			{"total_only", map[string]any{"total": total}},
			{"count_only", map[string]any{"count": total}},
			{"terminal_flag", map[string]any{"has_more": false}},
			{"terminal_page", map[string]any{"total": total, "page": 1, "pages": 1}},
			{"terminal_next", map[string]any{"total": total, "next": nil}},
		} {
			t.Run(strconv.Itoa(total)+"/"+metadata.name, func(t *testing.T) {
				calls := 0
				client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					items := groupInventoryPage(1, 200, total)["data"].(map[string]any)["items"]
					payload := map[string]any{"data": items}
					for key, value := range metadata.fields {
						payload[key] = value
					}
					return groupInventoryResponse(t, r, payload), nil
				})}
				groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
				if err != nil || len(groups) != total || calls != 1 {
					t.Fatalf("legacy whole-array contract must remain one complete read: count=%d calls=%d err=%v", len(groups), calls, err)
				}
			})
		}
	}
}

func TestSub2APIGroupInventoryStopsOnCancellationAndPageLimit(t *testing.T) {
	t.Run("cancel_between_pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			response := groupInventoryResponse(t, r, groupInventoryPage(calls, 100, 101))
			cancel()
			return response, nil
		})}
		groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(ctx, Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
		if err == nil || groups != nil || calls != 1 {
			t.Fatalf("cancelled inventory must stop before the next page: count=%d calls=%d err=%v", len(groups), calls, err)
		}
	})
	t.Run("page_limit_is_not_completion", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return groupInventoryResponse(t, r, groupInventoryPage(calls, 100, 10001)), nil
		})}
		groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
		if err == nil || groups != nil || calls != 100 {
			t.Fatalf("unclosed page limit must fail with zero results: count=%d calls=%d err=%v", len(groups), calls, err)
		}
	})
	t.Run("second_page_transport_failure", func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: protocolInventoryTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 2 {
				return nil, errors.New("fixture transport failure")
			}
			return groupInventoryResponse(t, r, groupInventoryPage(calls, 100, 101)), nil
		})}
		groups, err := NewPlatformService(NewHTTPClient(client)).FetchAdminAllGroupsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AdminAPIKey: "fixture"})
		if err == nil || groups != nil || calls != 2 {
			t.Fatalf("failed later request must discard the entire list: count=%d calls=%d err=%v", len(groups), calls, err)
		}
	})
}

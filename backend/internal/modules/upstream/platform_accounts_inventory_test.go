package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListSub2APIAdminAccountsRejectsBlankDuplicateAndChangingTotal(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "blank id",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, map[string]any{"data": []map[string]any{{"id": " "}}, "total": 1})
			},
		},
		{
			name: "duplicate id",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, map[string]any{"data": []map[string]any{{"id": 1}, {"id": 1}}, "total": 2})
			},
		},
		{
			name: "changing total",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					writeJSON(w, map[string]any{"data": sub2APIPaginationItems(100), "total": 101})
					return
				}
				writeJSON(w, map[string]any{"data": []map[string]any{{"id": 101}}, "total": 102})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			service := NewPlatformService(NewHTTPClient(server.Client()))
			accounts, err := service.ListSub2APIAdminAccountsContext(t.Context(), Session{
				Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
			})
			if err == nil || accounts != nil {
				t.Fatalf("invalid inventory was accepted: accounts=%#v err=%v", accounts, err)
			}
		})
	}
}

func TestListSub2APIAdminAccountsFailsClosedOnLaterPageAndCancellation(t *testing.T) {
	t.Run("later page failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "1" {
				writeJSON(w, map[string]any{"data": sub2APIPaginationItems(100), "total": 101})
				return
			}
			http.Error(w, "unavailable", http.StatusBadGateway)
		}))
		defer server.Close()
		service := NewPlatformService(NewHTTPClient(server.Client()))
		accounts, err := service.ListSub2APIAdminAccountsContext(t.Context(), Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"})
		if err == nil || accounts != nil {
			t.Fatalf("partial inventory leaked: accounts=%#v err=%v", accounts, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		service := NewPlatformService(NewHTTPClient(http.DefaultClient))
		accounts, err := service.ListSub2APIAdminAccountsContext(ctx, Session{Platform: PlatformSub2API, BaseURL: "http://127.0.0.1:1", AccessToken: "token"})
		if err == nil || accounts != nil {
			t.Fatalf("cancelled inventory leaked: accounts=%#v err=%v", accounts, err)
		}
	})
}

func TestListSub2APIAdminAccountsRejectsNonSub2APIMainSite(t *testing.T) {
	service := NewPlatformService(NewHTTPClient(http.DefaultClient))
	accounts, err := service.ListSub2APIAdminAccountsContext(t.Context(), Session{Platform: PlatformNewAPI, BaseURL: "http://127.0.0.1:1", Cookie: "session=1"})
	if err == nil || accounts != nil {
		t.Fatalf("non-Sub2API inventory was accepted: accounts=%#v err=%v", accounts, err)
	}
}

func TestListSub2APIAdminAccountsRejectsAmbiguousContainersAndPagination(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "different data and items containers",
			payload: map[string]any{
				"data":  []map[string]any{{"id": "account-data"}},
				"items": []map[string]any{{"id": "account-items"}},
				"total": 1,
			},
		},
		{
			name: "different total and count",
			payload: map[string]any{
				"data":  []map[string]any{{"id": "account-1"}},
				"total": 1,
				"count": 2,
			},
		},
		{
			name: "different nested and top-level total",
			payload: map[string]any{
				"data": map[string]any{
					"items": []map[string]any{{"id": "account-1"}},
					"total": 1,
				},
				"total": 2,
			},
		},
		{
			name: "different nested item containers",
			payload: map[string]any{
				"data": map[string]any{
					"items":   []map[string]any{{"id": "account-items"}},
					"list":    []map[string]any{{"id": "account-list"}},
					"records": []map[string]any{{"id": "account-items"}},
					"total":   1,
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, test.payload)
			}))
			defer server.Close()

			service := NewPlatformService(NewHTTPClient(server.Client()))
			accounts, err := service.ListSub2APIAdminAccountsContext(t.Context(), Session{
				Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
			})
			if err == nil || accounts != nil {
				t.Fatalf("ambiguous inventory was accepted: accounts=%#v err=%v", accounts, err)
			}
		})
	}
}

func TestListSub2APIAdminAccountsAcceptsIdenticalCompatibilityCopies(t *testing.T) {
	items := []map[string]any{{"id": "account-1", "status": "active"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"data": map[string]any{
				"items":   items,
				"list":    items,
				"records": items,
				"total":   1,
				"count":   1,
			},
			"items": items,
			"total": 1,
			"count": 1,
		})
	}))
	defer server.Close()

	service := NewPlatformService(NewHTTPClient(server.Client()))
	accounts, err := service.ListSub2APIAdminAccountsContext(t.Context(), Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	})
	if err != nil {
		t.Fatalf("identical compatibility copies were rejected: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "account-1" {
		t.Fatalf("unexpected compatible inventory: %#v", accounts)
	}
}

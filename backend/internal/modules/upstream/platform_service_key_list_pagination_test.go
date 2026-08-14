package upstream

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestListSub2APIKeysContext_ReadsAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/keys" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("page_size") != "100" || r.URL.Query().Get("sort_by") != "created_at" || r.URL.Query().Get("sort_order") != "desc" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		writeJSON(w, map[string]any{"data": keyListPage(page, true), "total": 150})
	}))
	defer server.Close()

	keys, err := NewPlatformService(NewHTTPClient(server.Client())).ListSub2APIKeys(Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token", TokenType: "Bearer",
	})
	if err != nil {
		t.Fatalf("ListSub2APIKeys() error: %v", err)
	}
	if len(keys) != 150 || keys[149].ID != "150" || keys[149].GroupID != "15" || keys[149].GroupName != "group-15" {
		t.Fatalf("expected page-2 key metadata, got len=%d last=%+v", len(keys), keys[len(keys)-1])
	}
}

func TestListNewAPITokensContext_ReadsAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/token/" || r.URL.Query().Get("page_size") != "100" {
			t.Fatalf("unexpected request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		writeJSON(w, map[string]any{"data": map[string]any{"items": keyListPage(page, false), "total": 150}})
	}))
	defer server.Close()

	tokens, err := NewPlatformService(NewHTTPClient(server.Client())).ListNewAPITokens(Session{
		Platform: PlatformNewAPI, BaseURL: server.URL, Cookie: "session=abc", UserID: "1",
	})
	if err != nil {
		t.Fatalf("ListNewAPITokens() error: %v", err)
	}
	if len(tokens) != 150 || tokens[149].ID != "150" || tokens[149].GroupName != "group-15" {
		t.Fatalf("expected page-2 token metadata, got len=%d last=%+v", len(tokens), tokens[len(tokens)-1])
	}
}

func TestListSub2APIKeysContext_DoesNotReturnPartialPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 2 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"data": keyListPage(page, true), "total": 150})
	}))
	defer server.Close()

	keys, err := NewPlatformService(NewHTTPClient(server.Client())).ListSub2APIKeys(Session{
		Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token",
	})
	if err == nil || keys != nil {
		t.Fatalf("partial key list must fail as a whole, keys=%d err=%v", len(keys), err)
	}
}

func keyListPage(page int, sub2API bool) []map[string]any {
	start := (page - 1) * 100
	if start >= 150 {
		return []map[string]any{}
	}
	end := start + 100
	if end > 150 {
		end = 150
	}
	items := make([]map[string]any, 0, end-start)
	for index := start; index < end; index++ {
		id := index + 1
		groupNumber := (index / 10) + 1
		record := map[string]any{
			"id": id, "name": fmt.Sprintf("key-%d", id), "status": 1,
		}
		if sub2API {
			record["group_id"] = groupNumber
			record["group"] = map[string]any{"name": fmt.Sprintf("group-%d", groupNumber)}
		} else {
			record["group"] = fmt.Sprintf("group-%d", groupNumber)
		}
		items = append(items, record)
	}
	return items
}

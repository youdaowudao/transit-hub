package upstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchSub2APIAdminRedeemCodesPageUsesFixedSelfRechargeQuery(t *testing.T) {
	var gotPath, gotAPIKey string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotQuery = r.URL.Query()
		writeJSON(w, map[string]any{"data": map[string]any{
			"items": []map[string]any{{
				"id": 7, "type": "balance", "value": 12.5, "status": "used", "used_by": 42,
				"used_at": "2026-08-13T04:05:06Z", "user": map[string]any{"id": 42, "email": "user@example.com", "username": "ignored"},
			}},
			"total": 1, "page": 1, "page_size": 100, "pages": 1,
		}})
	}))
	defer server.Close()

	service := NewPlatformService(NewHTTPClient(server.Client()))
	session := Session{Platform: PlatformSub2API, BaseURL: server.URL, AdminAPIKey: "admin-key"}
	page, err := service.FetchSub2APIAdminRedeemCodesPage(context.Background(), session, Sub2APIAdminRedeemCodesQuery{
		Page: -1, PageSize: 500, Type: "admin_balance", Status: "unused", SortBy: "created_at", SortOrder: "asc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/admin/redeem-codes" || gotAPIKey != "admin-key" {
		t.Fatalf("unexpected request path/auth: path=%q apiKey=%q", gotPath, gotAPIKey)
	}
	for key, want := range map[string]string{
		"page": "1", "page_size": "100", "type": "balance", "status": "used", "sort_by": "used_at", "sort_order": "desc",
	} {
		assertQueryValue(t, gotQuery, key, want)
	}
	if !page.TotalKnown || !page.PagesKnown || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("unexpected pagination: %+v", page)
	}
	item := page.Items[0]
	if item.ID != "7" || item.UsedBy != "42" || item.Value != 12.5 || item.User.Email != "user@example.com" || item.User.Username != "ignored" {
		t.Fatalf("unexpected parsed item: %+v", item)
	}
	wantTime := time.Date(2026, 8, 13, 4, 5, 6, 0, time.UTC)
	if item.UsedAt == nil || !item.UsedAt.Equal(wantTime) {
		t.Fatalf("unexpected used_at: %+v", item.UsedAt)
	}
}

func TestFetchSub2APIAdminRedeemCodesPageCancellationClosesRequest(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(released)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	service := NewPlatformService(NewHTTPClient(server.Client()))
	go func() {
		_, err := service.FetchSub2APIAdminRedeemCodesPage(ctx, Session{Platform: PlatformSub2API, BaseURL: server.URL, AccessToken: "token"}, Sub2APIAdminRedeemCodesQuery{})
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not return")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("upstream handler did not observe request cancellation")
	}
}

func TestRequestJSONWithContextLimitRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("x", 64) + `"}`))
	}))
	defer server.Close()

	_, err := NewHTTPClient(server.Client()).requestJSONWithContextLimit(context.Background(), server.URL, requestOptions{}, 32)
	requestErr, ok := err.(*RequestError)
	if !ok || requestErr.MessageKey != ErrorInvalidResponse {
		t.Fatalf("error = %#v, want invalid response", err)
	}
}

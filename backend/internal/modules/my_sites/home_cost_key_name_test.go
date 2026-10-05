package my_sites

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestHomeCostManagedKeyNameIncludesCreationMonthDay(t *testing.T) {
	clockCases := []struct {
		name string
		now  time.Time
		date string
	}{
		{"october_fifth", time.Date(2026, time.October, 5, 3, 0, 0, 0, time.UTC), "1005"},
		{"zero_padded_month_and_day", time.Date(2026, time.January, 2, 3, 0, 0, 0, time.UTC), "0102"},
		{"before_singapore_midnight", time.Date(2026, time.October, 4, 15, 59, 59, 0, time.UTC), "1004"},
		{"at_singapore_midnight", time.Date(2026, time.October, 4, 16, 0, 0, 0, time.UTC), "1005"},
	}
	for _, platform := range []upstream.Platform{upstream.PlatformSub2API, upstream.PlatformNewAPI} {
		for _, clock := range clockCases {
			t.Run(string(platform)+"/"+clock.name, func(t *testing.T) {
				var mu sync.Mutex
				var keyName, accountName string
				var keyCreates, accountCreates, tokenLists int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
						writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
					case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/groups":
						writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 7, "name": "vip", "platform": "openai", "status": "active"}}})
					case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/keys" || r.URL.Path == "/api/token/"):
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error("invalid key creation fixture request")
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						keyName, _ = body["name"].(string)
						keyCreates++
						if platform == upstream.PlatformSub2API {
							writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"id": 11, "key": "synthetic-test-key"}})
						} else {
							writeConnectionTestJSON(w, map[string]any{"success": true})
						}
					case r.Method == http.MethodGet && r.URL.Path == "/api/token/":
						tokenLists++
						writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 33, "name": keyName}}})
					case r.Method == http.MethodPost && r.URL.Path == "/api/token/33/key":
						writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"key": "synthetic-test-key"}})
					case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts":
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error("invalid main account creation fixture request")
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						accountName, _ = body["name"].(string)
						accountCreates++
						writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"id": 22}})
					default:
						t.Errorf("unexpected fixture request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				t.Cleanup(server.Close)
				admin := platformTestSession(upstream.PlatformSub2API, server.URL)
				source := platformTestSession(platform, server.URL)
				stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: admin}}
				connRepo := &testConnRepo{stateRepo: stateRepo}
				lookup := testUpstreamLookup{sites: map[string]*upstream.Site{
					"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "admin-1", Name: "验收-source", BaseURL: server.URL, Platform: platform, Session: &source, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "7", Name: "vip", Platform: stringPointer("openai")}}}},
				}}
				service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), lookup)
				service.now = func() time.Time { return clock.now }
				service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
				service.connRepository = connRepo
				addToPricing := false
				result, err := service.RealConnect(t.Context(), "user-1", RealConnectRequest{UpstreamSiteID: "site-1", UpstreamGroupID: "7", UpstreamGroupName: "vip", GroupType: "openai", OwnGroupIDs: []string{"7"}, AddToPricingMapping: &addToPricing})
				if err != nil {
					t.Fatalf("managed create failed: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				if !strings.HasSuffix(keyName, "-验收-source-vip") {
					t.Fatalf("site or group part changed: %q", keyName)
				}
				prefix := strings.TrimSuffix(keyName, "-验收-source-vip")
				if !strings.HasSuffix(prefix, clock.date) {
					t.Fatalf("creation name %q lacks month/day %s immediately after the English word", keyName, clock.date)
				}
				word := strings.TrimSuffix(prefix, clock.date)
				foundWord := false
				for _, existingWord := range keyPrefixes {
					foundWord = foundWord || word == existingWord
				}
				if !foundWord {
					t.Fatalf("creation name changed the original English prefix: %q", word)
				}
				wantTokenLists := 0
				if platform == upstream.PlatformNewAPI {
					wantTokenLists = 1
				}
				if keyCreates != 1 || accountCreates != 1 || tokenLists != wantTokenLists {
					t.Fatalf("unexpected extra creation or name queries: keys=%d accounts=%d tokenLists=%d", keyCreates, accountCreates, tokenLists)
				}
				if accountName != "A-【验收-source】-vip" || result.Connection.AdminAccountName != accountName || connRepo.connection == nil {
					t.Fatal("main account name or local persistence changed")
				}
			})
		}
	}
}

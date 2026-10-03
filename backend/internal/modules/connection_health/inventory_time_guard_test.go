package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestStageAWorkspaceClockGuardIncludesEmptyAndLaterPages(t *testing.T) {
	for _, sample := range []struct {
		name, badPage           string
		groupPages, memberPages bool
		wantKnown               bool
	}{
		{"trusted", "", false, false, true},
		{"empty-member-page", "empty", false, false, false},
		{"last-group-page", "groups-last", true, false, false},
		{"later-member-page", "members-last", false, true, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				page, _ := strconv.Atoi(req.URL.Query().Get("page"))
				header := http.Header{"Date": []string{time.Now().UTC().Format(http.TimeFormat)}}
				items := make([]any, 0)
				total := 0
				if req.URL.Path == "/api/v1/admin/groups" {
					total = 2
					if sample.groupPages {
						total = 101
					}
					start, end := (page-1)*100+1, page*100
					if end > total {
						end = total
					}
					for id := start; id <= end; id++ {
						items = append(items, map[string]any{"id": id, "name": "group"})
					}
					if sample.badPage == "groups-last" && page == 2 {
						header.Del("Date")
					}
				} else if req.URL.Path == "/api/v1/admin/accounts" {
					groupID := req.URL.Query().Get("group")
					if groupID == "1" {
						total = 1
						items = append(items, map[string]any{"id": 1, "status": "active", "schedulable": true, "temp_unschedulable_until": "2027-01-01T00:00:00Z", "rate_limit_reset_at": nil, "overload_until": "2027-01-01T00:00:00Z", "expires_at": float64(1800000000), "extra": map[string]any{"model_rate_limits": map[string]any{"model": map[string]any{"rate_limit_reset_at": "2027-01-01T00:00:00Z"}}}})
					}
					if groupID == "2" && sample.memberPages {
						total = 101
						start, end := (page-1)*100+2, page*100+1
						if end > 102 {
							end = 102
						}
						for id := start; id <= end; id++ {
							items = append(items, map[string]any{"id": id, "temp_unschedulable_until": nil})
						}
					}
					if groupID == "2" && ((sample.badPage == "empty") || (sample.badPage == "members-last" && page == 2)) {
						header.Del("Date")
					}
				} else {
					t.Fatalf("unexpected query %s", req.URL.Path)
				}
				payload, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"items": items, "total": total, "page": page, "page_size": 100}})
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(string(payload))), Request: req}, nil
			})
			service := upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport}))
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://127.0.0.1:8080", AccessToken: "synthetic-test-token"}
			groups, err := service.FetchAdminAllGroupsContext(context.Background(), session)
			if err != nil {
				t.Fatalf("groups: %v", err)
			}
			inventory := adminWorkspaceInventory{session: session, groupsComplete: true}
			for _, group := range groups {
				accounts, err := service.ListAdminGroupAccountsContext(context.Background(), session, group)
				if err != nil {
					t.Fatalf("accounts: %v", err)
				}
				inventory.groups = append(inventory.groups, adminInventoryGroup{group: group, accounts: accounts})
			}
			guardAdminInventoryTimes(&inventory)
			account := inventory.groups[0].accounts[0]
			if account.TempUnschedulableKnown != sample.wantKnown || account.OverloadKnown != sample.wantKnown || account.ExpiresAtKnown != sample.wantKnown || account.ModelRateLimits[0].Known != sample.wantKnown {
				t.Fatal("later or empty response did not guard all earlier deadlines")
			}
			if !account.RateLimitKnown || account.RateLimitResetAt != nil {
				t.Fatal("explicit null must stay unlimited")
			}
		})
	}
}

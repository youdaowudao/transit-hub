package my_sites

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type globalizationPricingTransport func(*http.Request) (*http.Response, error)

func (f globalizationPricingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGlobalizationManualPricingCompletesWhenNotificationLookupFails(t *testing.T) {
	repo := &testStateRepo{state: &State{
		UserID: "user", AdminAccountID: "workspace",
		Session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://primary.fixture.invalid", AdminAPIKey: "fixture"},
		Mappings: []GroupMapping{{OwnGroup: "vip", EnableAutoPricing: true, AutoPricingSource: "average_upstream", AutoPricingStrategy: "fixed", FixedIncrease: 0.5, AdjustThresholdPercent: 10,
			UpstreamTargets:         []UpstreamGroupRef{{SiteID: "one", GroupName: "a"}, {SiteID: "two", GroupName: "b"}},
			EnableAutoPricingNotify: true, AutoPricingNotifyBotIDs: []string{"original"}}},
		OwnGroups: []GroupOption{{Name: "vip", Multiplier: 1.1}},
	}}
	writes := 0
	client := &http.Client{Transport: globalizationPricingTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"id":1,"name":"vip","rate_multiplier":1.1,"platform":"openai","status":"active"}}`
		if r.URL.Path == "/api/v1/admin/groups" {
			body = `{"code":0,"data":[{"id":1,"name":"vip","rate_multiplier":1.1,"platform":"openai","status":"active"}]}`
		}
		if r.Method == http.MethodPut {
			var payload struct {
				Rate float64 `json:"rate_multiplier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Rate != 2.5 {
				t.Fatal("pricing request changed the expected numeric result")
			}
			writes++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})}
	lookup := testUpstreamLookup{sites: map[string]*upstream.Site{
		"one": {ID: "one", UserID: "user", AdminAccountID: "workspace", Status: upstream.StatusConnected, LastSyncedAt: int64Ptr(1), Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{Name: "a", Multiplier: floatPtr(1)}}}},
		"two": {ID: "two", UserID: "user", AdminAccountID: "workspace", Status: upstream.StatusConnected, LastSyncedAt: int64Ptr(1), Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{Name: "b", Multiplier: floatPtr(3)}}}},
	}}
	svc := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(client)), lookup)
	svc.SetAdminAccountResolver(testAdminResolver{currentID: "workspace"})
	notifier := &notificationProjectionFixture{fail: true}
	svc.botNotifier = notifier
	response, err := svc.RunAutoPricingNow(context.Background(), "user", AutoPricingRunRequest{OwnGroup: "vip"})
	if err != nil || response.Result.Status != "applied" || writes != 1 {
		t.Fatalf("notification lookup must not stop pricing: status=%q writes=%d err=%v", response.Result.Status, writes, err)
	}
	if repo.state.OwnGroups[0].Multiplier != 2.5 || !repo.state.Mappings[0].EnableAutoPricingNotify || !reflect.DeepEqual(repo.state.Mappings[0].AutoPricingNotifyBotIDs, []string{"original"}) {
		t.Fatal("pricing must persist its result without rewriting notification configuration")
	}
	if !response.Mapping.EnableAutoPricingNotify || !reflect.DeepEqual(response.Mapping.AutoPricingNotifyBotIDs, []string{"original"}) || len(notifier.sentIDs) != 0 {
		t.Fatal("temporary lookup failure must retain references and send no message")
	}
	raw, _ := json.Marshal(response.Mapping)
	var mapping map[string]json.RawMessage
	_ = json.Unmarshal(raw, &mapping)
	if string(mapping["autoPricingNotifyRecipientsUnavailable"]) != "true" {
		t.Fatal("manual success must expose the temporary notification failure separately")
	}
}

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

type cleanupPricingTransport struct {
	ratios map[string]float64
	writes int
}

func (m *cleanupPricingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var data any
	switch request.URL.Path {
	case "/api/user/self":
		data = map[string]any{"role": 10}
	case "/api/group/":
		data = []string{"vip"}
	case "/api/user/self/groups", "/api/pricing":
		data = map[string]any{"group_ratio": m.ratios}
	case "/api/option/":
		if request.Method == http.MethodPut {
			var payload struct{ Key, Value string }
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(payload.Value), &m.ratios); err != nil {
				return nil, err
			}
			m.writes++
		}
		value, _ := json.Marshal(m.ratios)
		data = map[string]any{"GroupRatio": string(value)}
	default:
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":false}`))}, nil
	}
	body, _ := json.Marshal(map[string]any{"success": true, "data": data})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

type cleanupPricingNotifier struct{ messages []string }

func (n *cleanupPricingNotifier) SendToBots(_ context.Context, _ string, _ []string, message string) {
	n.messages = append(n.messages, message)
}

func cleanupPricingService(t *testing.T, source string, aGroups []upstream.GroupInfo) (*Service, *testStateRepo, *cleanupPricingTransport, *cleanupPricingNotifier, testUpstreamLookup) {
	t.Helper()
	transport := &cleanupPricingTransport{ratios: map[string]float64{"vip": 1.1}}
	notifier := &cleanupPricingNotifier{}
	repo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "admin-1", Session: testSession("https://cleanup-pricing.invalid"),
		Mappings: []GroupMapping{{OwnGroup: "vip", EnableAutoPricing: true, AutoPricingSource: source,
			PrimaryUpstreamSiteID: "site-a", PrimaryUpstreamGroupName: "a", AutoPricingStrategy: "fixed", FixedIncrease: 0.5,
			AdjustThresholdPercent: 100, EnableAutoPricingNotify: true, AutoPricingNotifyBotIDs: []string{"fixture-bot"},
			UpstreamTargets: []UpstreamGroupRef{{SiteID: "site-a", GroupName: "a"}, {SiteID: "site-b", GroupName: "b"}}}},
		OwnGroups: []GroupOption{{Name: "vip", Multiplier: 1.1}}}}
	lookup := testUpstreamLookup{sites: map[string]*upstream.Site{
		"site-a": {ID: "site-a", UserID: "user-1", AdminAccountID: "admin-1", Status: upstream.StatusConnected, LastSyncedAt: int64Ptr(1), Metrics: upstream.Metrics{Groups: aGroups}},
		"site-b": {ID: "site-b", UserID: "user-1", AdminAccountID: "admin-1", Status: upstream.StatusConnected, LastSyncedAt: int64Ptr(1), Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "b", Name: "b", Multiplier: floatPtr(4)}}}},
	}}
	service := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(&http.Client{Transport: transport})), lookup)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "admin-1"})
	service.SetBotNotifier(notifier)
	return service, repo, transport, notifier, lookup
}

func TestCleanupPricingDisappearedAndAutoDoNotWriteOrNotify(t *testing.T) {
	old := upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "a", Name: "a", Multiplier: floatPtr(2)}}}
	for _, groups := range [][]upstream.GroupInfo{{}, {{ID: "a", Name: "a", MultiplierMode: "auto"}}} {
		service, repo, transport, notifier, _ := cleanupPricingService(t, "primary_upstream", groups)
		before, _ := json.Marshal(repo.state)
		service.ApplyAutoPricingAfterSync(context.Background(), "user-1", "admin-1", "site-a", "source A", old, upstream.Metrics{Groups: groups})
		after, _ := json.Marshal(repo.state)
		if transport.writes != 0 || len(notifier.messages) != 0 || string(before) != string(after) {
			t.Fatal("disappeared/auto group manufactured pricing, notification or status change")
		}
	}
}

func TestCleanupPricingMissingReferenceSkipsWithoutWriteOrNotify(t *testing.T) {
	for _, source := range []string{"primary_upstream", "average_upstream", "lowest_upstream", "highest_upstream"} {
		for _, groups := range [][]upstream.GroupInfo{{}, {{ID: "a", Name: "a", MultiplierMode: "auto"}}} {
			service, repo, transport, notifier, _ := cleanupPricingService(t, source, groups)
			response, err := service.RunAutoPricingNow(context.Background(), "user-1", AutoPricingRunRequest{OwnGroup: "vip"})
			if err != nil || response.Result.Status != "skipped" || response.Result.Reason != "missing_reference_multiplier" {
				t.Fatalf("missing %s source must skip specifically: %#v %v", source, response.Result, err)
			}
			if transport.writes != 0 || len(notifier.messages) != 0 || repo.state.OwnGroups[0].Multiplier != 1.1 || !reflect.DeepEqual(repo.state.Mappings[0].UpstreamTargets, []UpstreamGroupRef{{SiteID: "site-a", GroupName: "a"}, {SiteID: "site-b", GroupName: "b"}}) {
				t.Fatal("missing reference changed remote rate, notification or mapping")
			}
		}
	}
	// A missing other source must also block a genuine B price change.
	service, repo, transport, notifier, _ := cleanupPricingService(t, "average_upstream", nil)
	service.ApplyAutoPricingAfterSync(context.Background(), "user-1", "admin-1", "site-b", "source B",
		upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "b", Name: "b", Multiplier: floatPtr(3)}}},
		upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "b", Name: "b", Multiplier: floatPtr(4)}}})
	status := repo.state.Mappings[0].LastAutoPricingRun
	if status == nil || status.Status != "skipped" || status.Reason != "missing_reference_multiplier" || transport.writes != 0 || len(notifier.messages) != 0 {
		t.Fatal("automatic aggregate pricing ignored missing source")
	}
}

func TestCleanupPricingSharedGroupsRemainUsableAfterAuditRejection(t *testing.T) {
	groups := []upstream.GroupInfo{{ID: "a", Name: "a", Multiplier: floatPtr(2)}}
	service, repo, transport, notifier, lookup := cleanupPricingService(t, "average_upstream", groups)
	// Audit rejects only local snapshot writing. Existing shared valid groups
	// and the after-sync contract must remain available to aggregate pricing.
	lookup.sites["site-a"].Metrics.GroupSnapshotAudit = &upstream.GroupSnapshotAudit{AllowUpdate: false, AllowCleanup: false, Issue: "invalid optional raw field"}
	service.ApplyAutoPricingAfterSync(context.Background(), "user-1", "admin-1", "site-a", "source A", upstream.Metrics{Groups: groups}, lookup.sites["site-a"].Metrics)
	if transport.writes != 0 || len(notifier.messages) != 0 {
		t.Fatal("unchanged source A manufactured a price change")
	}
	service.ApplyAutoPricingAfterSync(context.Background(), "user-1", "admin-1", "site-b", "source B",
		upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "b", Name: "b", Multiplier: floatPtr(3)}}}, lookup.sites["site-b"].Metrics)
	status := repo.state.Mappings[0].LastAutoPricingRun
	if transport.writes != 1 || len(notifier.messages) != 1 || status == nil || status.NewReference == nil || *status.NewReference != 3 || repo.state.OwnGroups[0].Multiplier != 3.5 {
		t.Fatal("B aggregate pricing lost valid shared A source after audit rejection")
	}
	response, err := service.RunAutoPricingNow(context.Background(), "user-1", AutoPricingRunRequest{OwnGroup: "vip"})
	if err != nil || response.Result.Status != "skipped" || response.Result.Reason != "target_unchanged" || response.Result.NewReference == nil || *response.Result.NewReference != 3 || transport.writes != 1 || len(notifier.messages) != 1 {
		t.Fatalf("manual pricing failed to reuse the same shared source without duplicate mutation: %#v %v", response.Result, err)
	}
}

func TestCleanupPricingRiseAndFallKeepActualNotifications(t *testing.T) {
	for _, next := range []float64{1, 3} {
		groups := []upstream.GroupInfo{{ID: "a", Name: "a", Multiplier: floatPtr(next)}}
		service, repo, transport, notifier, _ := cleanupPricingService(t, "primary_upstream", groups)
		service.ApplyAutoPricingAfterSync(context.Background(), "user-1", "admin-1", "site-a", "source A", upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "a", Name: "a", Multiplier: floatPtr(2)}}}, upstream.Metrics{Groups: groups})
		status := repo.state.Mappings[0].LastAutoPricingRun
		if transport.writes != 1 || len(notifier.messages) != 1 || status == nil || status.Status != "applied" || status.NewReference == nil || *status.NewReference != next || repo.state.OwnGroups[0].Multiplier != next+0.5 {
			t.Fatal("genuine rise/fall lost original pricing or notification")
		}
	}
}

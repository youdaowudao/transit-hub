package dashboard

import (
	"errors"
	"testing"

	"transithub/backend/internal/modules/my_sites"
)

func TestFinalizeRealConnectionProfitAggregatesSameGroupAfterAllConnections(t *testing.T) {
	connections := []my_sites.RealConnection{
		{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}},
		{ID: "conn-2", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-2", AdminAccountID: "account-2", OwnGroupIDs: []string{"group-1"}},
	}
	states := newProfitConnectionStates(connections)
	setConnectionRevenue(t, states, "conn-1", 60)
	setConnectionRevenue(t, states, "conn-2", 40)

	result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 100}, map[string]float64{
		"site-a\x00key-1": 30,
		"site-a\x00key-2": 20,
	})

	group := result.Groups["group-1"]
	if group.Status != ProfitAllocationExact || group.Cost == nil || *group.Cost != 50 || group.Profit == nil || *group.Profit != 50 {
		t.Fatalf("group allocation = %+v, want exact cost=50 profit=50", group)
	}
	if connection := result.Connections["conn-1"]; connection.Profit == nil || *connection.Profit != 30 {
		t.Fatalf("conn-1 allocation = %+v, want profit=30", connection)
	}
	if connection := result.Connections["conn-2"]; connection.Profit == nil || *connection.Profit != 20 {
		t.Fatalf("conn-2 allocation = %+v, want profit=20", connection)
	}
	if result.ResolvedConnections != 2 || result.FailedConnections != 0 || result.UnallocatableConnections != 0 {
		t.Fatalf("unexpected quality counts: %+v", result)
	}
}

func TestFinalizeRealConnectionProfitDoesNotPublishPartialGroup(t *testing.T) {
	connections := []my_sites.RealConnection{
		{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}},
		{ID: "conn-2", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-2", AdminAccountID: "account-2", OwnGroupIDs: []string{"group-1"}},
	}
	states := newProfitConnectionStates(connections)
	setConnectionRevenue(t, states, "conn-1", 60)
	setConnectionRevenue(t, states, "conn-2", 40)

	result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 100}, map[string]float64{"site-a\x00key-1": 30})

	group := result.Groups["group-1"]
	if group.Status != ProfitAllocationUnavailable || group.Cost != nil || group.Profit != nil {
		t.Fatalf("partial group produced formal amounts: %+v", group)
	}
	if result.ResolvedConnections != 1 || result.FailedConnections != 1 || result.UnallocatableConnections != 0 {
		t.Fatalf("unexpected quality counts: %+v", result)
	}
	if !hasConnectionProfitIssue(result.Issues, ProfitIssueKeyMissing, "conn-2") {
		t.Fatalf("missing connection-scoped key issue: %+v", result.Issues)
	}
}

func TestFinalizeRealConnectionProfitRejectsRevenueMismatch(t *testing.T) {
	connections := []my_sites.RealConnection{
		{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}},
		{ID: "conn-2", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-2", AdminAccountID: "account-2", OwnGroupIDs: []string{"group-1"}},
	}
	states := newProfitConnectionStates(connections)
	setConnectionRevenue(t, states, "conn-1", 60)
	setConnectionRevenue(t, states, "conn-2", 39)

	result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 100}, map[string]float64{
		"site-a\x00key-1": 30,
		"site-a\x00key-2": 20,
	})

	group := result.Groups["group-1"]
	if group.Status != ProfitAllocationUnavailable || group.Cost != nil || group.Profit != nil {
		t.Fatalf("mismatched group produced formal amounts: %+v", group)
	}
	if result.ResolvedConnections != 0 || result.FailedConnections != 2 {
		t.Fatalf("mismatch did not close every connection as failed: %+v", result)
	}
	for _, connectionID := range []string{"conn-1", "conn-2"} {
		if !hasConnectionProfitIssue(result.Issues, ProfitIssueGroupRevenueMismatch, connectionID) {
			t.Fatalf("missing mismatch issue for %s: %+v", connectionID, result.Issues)
		}
	}
}

func TestNewProfitConnectionStatesClosesAmbiguousBindingsPerConnection(t *testing.T) {
	connections := []my_sites.RealConnection{
		{ID: "multi", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-0", AdminAccountID: "account-0", OwnGroupIDs: []string{"group-1", "group-2"}},
		{ID: "dup-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}},
		{ID: "dup-2", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-2", OwnGroupIDs: []string{"group-2"}},
	}
	states := newProfitConnectionStates(connections)
	result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 60, "group-2": 40}, map[string]float64{"site-a\x00key-0": 10, "site-a\x00key-1": 20})

	if result.ResolvedConnections != 0 || result.FailedConnections != 0 || result.UnallocatableConnections != 3 {
		t.Fatalf("unexpected quality counts: %+v", result)
	}
	if !hasConnectionProfitIssue(result.Issues, ProfitIssueMultiGroup, "multi") ||
		!hasConnectionProfitIssue(result.Issues, ProfitIssueDuplicateBinding, "dup-1") ||
		!hasConnectionProfitIssue(result.Issues, ProfitIssueDuplicateBinding, "dup-2") {
		t.Fatalf("ambiguous bindings were not reported per connection: %+v", result.Issues)
	}
}

func TestNewProfitConnectionStatesDuplicateCheckIncludesMultiGroupConnections(t *testing.T) {
	connections := []my_sites.RealConnection{
		{ID: "multi", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "shared-key", AdminAccountID: "shared-account", OwnGroupIDs: []string{"group-1", "group-2"}},
		{ID: "single", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "shared-key", AdminAccountID: "shared-account", OwnGroupIDs: []string{"group-1"}},
	}

	result := finalizeRealConnectionProfit(
		newProfitConnectionStates(connections),
		map[string]float64{"group-1": 60, "group-2": 40},
		map[string]float64{"site-a\x00shared-key": 30},
	)

	if result.ResolvedConnections != 0 || result.UnallocatableConnections != 2 || result.FailedConnections != 0 {
		t.Fatalf("duplicate relation did not close every affected connection: %+v", result)
	}
	for _, connectionID := range []string{"multi", "single"} {
		if !hasConnectionProfitIssue(result.Issues, ProfitIssueDuplicateBinding, connectionID) {
			t.Fatalf("missing duplicate issue for %s: %+v", connectionID, result.Issues)
		}
	}
}

func TestFinalizeRealConnectionProfitAllowsZeroCostAndNegativeProfit(t *testing.T) {
	t.Run("zero cost is known", func(t *testing.T) {
		states := newProfitConnectionStates([]my_sites.RealConnection{{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}}})
		setConnectionRevenue(t, states, "conn-1", 50)
		result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 50}, map[string]float64{"site-a\x00key-1": 0})
		group := result.Groups["group-1"]
		if group.Status != ProfitAllocationExact || group.Cost == nil || *group.Cost != 0 || group.Profit == nil || *group.Profit != 50 {
			t.Fatalf("zero cost was treated as missing: %+v", group)
		}
	})

	t.Run("negative profit remains visible", func(t *testing.T) {
		states := newProfitConnectionStates([]my_sites.RealConnection{{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}}})
		setConnectionRevenue(t, states, "conn-1", 20)
		result := finalizeRealConnectionProfit(states, map[string]float64{"group-1": 20}, map[string]float64{"site-a\x00key-1": 30})
		group := result.Groups["group-1"]
		if group.Status != ProfitAllocationExact || group.Profit == nil || *group.Profit != -10 {
			t.Fatalf("negative profit was hidden: %+v", group)
		}
	})
}

func TestFinalizeRealConnectionProfitIsIndependentOfConnectionOrder(t *testing.T) {
	forward := []my_sites.RealConnection{
		{ID: "conn-1", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-1", AdminAccountID: "account-1", OwnGroupIDs: []string{"group-1"}},
		{ID: "conn-2", Status: "active", UpstreamSiteID: "site-a", UpstreamKeyID: "key-2", AdminAccountID: "account-2", OwnGroupIDs: []string{"group-1"}},
	}
	reverse := []my_sites.RealConnection{forward[1], forward[0]}

	calculate := func(connections []my_sites.RealConnection) profitAllocationGroup {
		states := newProfitConnectionStates(connections)
		setConnectionRevenue(t, states, "conn-1", 60)
		setConnectionRevenue(t, states, "conn-2", 40)
		return finalizeRealConnectionProfit(states, map[string]float64{"group-1": 100}, map[string]float64{
			"site-a\x00key-1": 30,
			"site-a\x00key-2": 20,
		}).Groups["group-1"]
	}

	left := calculate(forward)
	right := calculate(reverse)
	if left.Status != right.Status || left.Cost == nil || right.Cost == nil || *left.Cost != *right.Cost || left.Profit == nil || right.Profit == nil || *left.Profit != *right.Profit {
		t.Fatalf("connection order changed result: forward=%+v reverse=%+v", left, right)
	}
}

func TestSafeProfitErrorDoesNotExposeRawErrorText(t *testing.T) {
	if got := safeProfitError(errors.New("Authorization: Bearer secret-token")); got != "request_failed" {
		t.Fatalf("safe error = %q, want request_failed", got)
	}
}

func setConnectionRevenue(t *testing.T, states []*profitConnectionState, connectionID string, revenue float64) {
	t.Helper()
	for _, state := range states {
		if state.Connection.ID == connectionID {
			state.Revenue = floatPtr(revenue)
			return
		}
	}
	t.Fatalf("connection %s not found", connectionID)
}

func hasConnectionProfitIssue(issues []ProfitIssue, code, connectionID string) bool {
	for _, issue := range issues {
		if issue.Code == code && issue.ConnectionID == connectionID {
			return true
		}
	}
	return false
}

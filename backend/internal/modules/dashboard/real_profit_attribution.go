package dashboard

import (
	"math"
	"sort"
	"strings"

	"transithub/backend/internal/modules/my_sites"
)

const (
	ProfitAllocationExact         = "exact"
	ProfitAllocationFailed        = "failed"
	ProfitAllocationPending       = "pending"
	ProfitAllocationUnallocatable = "unallocatable"
	ProfitAllocationUnavailable   = "unavailable"

	ProfitIssueConnectionIncomplete = "real_connection_incomplete"
	ProfitIssueDuplicateBinding     = "duplicate_binding"
	ProfitIssueGroupRevenueMismatch = "group_revenue_mismatch"
	ProfitIssueMultiGroup           = "multi_group_unallocatable"
	ProfitIssueKeyMissing           = "upstream_key_missing"

	profitAmountTolerance = 0.000001
)

// ProfitIssue is a sanitized attribution failure returned to the dashboard.
type ProfitIssue struct {
	Code         string `json:"code"`
	Source       string `json:"source,omitempty"`
	Stage        string `json:"stage,omitempty"`
	ConnectionID string `json:"connectionId,omitempty"`
	SiteID       string `json:"siteId,omitempty"`
	KeyID        string `json:"keyId,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	GroupID      string `json:"groupId,omitempty"`
	HTTPStatus   int    `json:"httpStatus,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
	Detail       string `json:"detail,omitempty"`
	ObservedAt   string `json:"observedAt,omitempty"`
	RunID        string `json:"runId,omitempty"`
}

type profitAllocationGroup struct {
	Cost   *float64
	Profit *float64
	Status string
}

type profitConnectionState struct {
	Connection my_sites.RealConnection
	GroupIDs   []string
	GroupID    string
	Status     string
	Revenue    *float64
	Cost       *float64
	Profit     *float64
	Issues     []ProfitIssue
}

type profitAllocationResult struct {
	Groups                   map[string]profitAllocationGroup
	Connections              map[string]profitAllocationGroup
	ConnectionRevenue        map[string]*float64
	Issues                   []ProfitIssue
	UnboundCost              float64
	ResolvedConnections      int
	UnallocatableConnections int
	FailedConnections        int
}

func newProfitConnectionStates(connections []my_sites.RealConnection) []*profitConnectionState {
	states := make([]*profitConnectionState, 0, len(connections))
	keyOwners := make(map[string][]*profitConnectionState)
	accountOwners := make(map[string][]*profitConnectionState)

	for _, connection := range connections {
		state := &profitConnectionState{
			Connection: connection,
			GroupIDs:   normalizedIDs(connection.OwnGroupIDs),
			Status:     ProfitAllocationPending,
		}
		states = append(states, state)
		if strings.TrimSpace(connection.UpstreamSiteID) != "" && strings.TrimSpace(connection.UpstreamKeyID) != "" {
			keyOwners[profitKey(connection)] = append(keyOwners[profitKey(connection)], state)
		}
		if accountID := strings.TrimSpace(connection.AdminAccountID); accountID != "" {
			accountOwners[accountID] = append(accountOwners[accountID], state)
		}
		if len(state.GroupIDs) != 1 {
			markProfitConnection(state, ProfitAllocationUnallocatable, connectionProfitIssue(connection, ProfitIssueMultiGroup, "real_connections", "binding", strings.Join(state.GroupIDs, ",")))
			continue
		}
		state.GroupID = state.GroupIDs[0]
		if strings.TrimSpace(connection.UpstreamSiteID) == "" || strings.TrimSpace(connection.UpstreamKeyID) == "" || strings.TrimSpace(connection.AdminAccountID) == "" {
			markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(connection, ProfitIssueConnectionIncomplete, "real_connections", "binding", state.GroupID))
			continue
		}
	}

	for _, state := range states {
		if len(keyOwners[profitKey(state.Connection)]) > 1 {
			issue := connectionProfitIssue(state.Connection, ProfitIssueDuplicateBinding, "real_connections", "binding", state.GroupID)
			issue.Detail = "duplicate_key"
			markProfitConnection(state, ProfitAllocationUnallocatable, issue)
		}
		if len(accountOwners[strings.TrimSpace(state.Connection.AdminAccountID)]) > 1 {
			issue := connectionProfitIssue(state.Connection, ProfitIssueDuplicateBinding, "real_connections", "binding", state.GroupID)
			issue.Detail = "duplicate_account"
			markProfitConnection(state, ProfitAllocationUnallocatable, issue)
		}
	}
	return states
}

func finalizeRealConnectionProfit(states []*profitConnectionState, displayRevenueByGroup, costByKey map[string]float64) profitAllocationResult {
	result := profitAllocationResult{
		Groups:            make(map[string]profitAllocationGroup, len(displayRevenueByGroup)),
		Connections:       make(map[string]profitAllocationGroup, len(states)),
		ConnectionRevenue: make(map[string]*float64, len(states)),
	}
	for groupID := range displayRevenueByGroup {
		result.Groups[groupID] = profitAllocationGroup{Status: ProfitAllocationUnavailable}
	}

	for _, state := range states {
		if state.Status != ProfitAllocationPending {
			continue
		}
		cost, ok := costByKey[profitKey(state.Connection)]
		if !ok {
			markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(state.Connection, ProfitIssueKeyMissing, "upstream", "key_cost", state.GroupID))
			continue
		}
		if state.Revenue == nil {
			markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(state.Connection, "usage_stats_missing", "main_admin", "revenue", state.GroupID))
			continue
		}
		state.Status = ProfitAllocationExact
		state.Cost = floatPtr(cost)
		state.Profit = floatPtr(*state.Revenue - cost)
	}

	statesByGroup := make(map[string][]*profitConnectionState)
	for _, state := range states {
		for _, groupID := range state.GroupIDs {
			statesByGroup[groupID] = append(statesByGroup[groupID], state)
		}
	}
	groupIDs := make([]string, 0, len(statesByGroup))
	for groupID := range statesByGroup {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		groupStates := statesByGroup[groupID]
		allExact := len(groupStates) > 0
		hasUnallocatable := false
		var connectionRevenueTotal float64
		var costTotal float64
		for _, state := range groupStates {
			if state.Status != ProfitAllocationExact || state.GroupID != groupID {
				allExact = false
			}
			if state.Status == ProfitAllocationUnallocatable {
				hasUnallocatable = true
			}
			if state.Status == ProfitAllocationExact {
				connectionRevenueTotal += *state.Revenue
				costTotal += *state.Cost
			}
		}
		displayRevenue, hasDisplayRevenue := displayRevenueByGroup[groupID]
		if allExact && (!hasDisplayRevenue || !profitAmountsEqual(connectionRevenueTotal, displayRevenue)) {
			for _, state := range groupStates {
				issue := connectionProfitIssue(state.Connection, ProfitIssueGroupRevenueMismatch, "main_admin", "reconciliation", groupID)
				issue.Detail = "connection_revenue_total_mismatch"
				markProfitConnection(state, ProfitAllocationFailed, issue)
			}
			allExact = false
		}
		if allExact {
			result.Groups[groupID] = profitAllocationGroup{
				Cost:   floatPtr(costTotal),
				Profit: floatPtr(displayRevenue - costTotal),
				Status: ProfitAllocationExact,
			}
		} else if hasUnallocatable {
			result.Groups[groupID] = profitAllocationGroup{Status: ProfitAllocationUnallocatable}
		} else {
			result.Groups[groupID] = profitAllocationGroup{Status: ProfitAllocationUnavailable}
		}
	}

	usedCosts := make(map[string]struct{})
	for _, state := range states {
		allocation := profitAllocationGroup{Status: state.Status}
		if state.Revenue != nil {
			result.ConnectionRevenue[state.Connection.ID] = floatPtr(*state.Revenue)
		}
		if state.Status == ProfitAllocationExact {
			allocation.Cost = floatPtr(*state.Cost)
			allocation.Profit = floatPtr(*state.Profit)
			usedCosts[profitKey(state.Connection)] = struct{}{}
			result.ResolvedConnections++
		} else if state.Status == ProfitAllocationUnallocatable {
			result.UnallocatableConnections++
		} else {
			result.FailedConnections++
		}
		result.Connections[state.Connection.ID] = allocation
		result.Issues = append(result.Issues, state.Issues...)
	}
	costKeys := make([]string, 0, len(costByKey))
	for key := range costByKey {
		costKeys = append(costKeys, key)
	}
	sort.Strings(costKeys)
	for _, key := range costKeys {
		cost := costByKey[key]
		if _, used := usedCosts[key]; !used {
			result.UnboundCost += cost
		}
	}
	return result
}

func markProfitConnection(state *profitConnectionState, status string, issue ProfitIssue) {
	if state.Status == ProfitAllocationExact && status != ProfitAllocationExact {
		state.Cost = nil
		state.Profit = nil
	}
	if state.Status == ProfitAllocationPending || state.Status == ProfitAllocationExact {
		state.Status = status
	}
	state.Issues = append(state.Issues, issue)
}

func connectionProfitIssue(connection my_sites.RealConnection, code, source, stage, groupID string) ProfitIssue {
	return ProfitIssue{
		Code:         code,
		Source:       source,
		Stage:        stage,
		ConnectionID: connection.ID,
		SiteID:       strings.TrimSpace(connection.UpstreamSiteID),
		KeyID:        strings.TrimSpace(connection.UpstreamKeyID),
		AccountID:    strings.TrimSpace(connection.AdminAccountID),
		GroupID:      groupID,
	}
}

func profitKey(connection my_sites.RealConnection) string {
	return strings.TrimSpace(connection.UpstreamSiteID) + "\x00" + strings.TrimSpace(connection.UpstreamKeyID)
}

func profitAmountsEqual(left, right float64) bool {
	return math.Abs(left-right) <= profitAmountTolerance
}

func normalizedIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hasProfitIssue(issues []ProfitIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func floatPtr(value float64) *float64 { return &value }

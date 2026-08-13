package dashboard

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func (s *MetricsService) realGroupUsageToday(ctx context.Context, userID, adminAccountID string, session upstream.Session, date string) (GroupUsageTodayResponse, error) {
	runID, runErr := metricsRandomID()
	if runErr != nil {
		return GroupUsageTodayResponse{}, runErr
	}
	quality := &GroupProfitQuality{Status: ProfitAllocationUnavailable, BusinessDate: date, ObservedAt: time.Now().Format(time.RFC3339), RunID: runID}
	response := GroupUsageTodayResponse{Date: date, Quality: quality}

	groups, err := s.platform.FetchAdminAllGroups(session)
	if err != nil {
		status, retryable := profitErrorMeta(err)
		response.Issues = append(response.Issues, ProfitIssue{Code: "main_groups_failed", Source: "main_admin", Stage: "groups", HTTPStatus: status, Retryable: retryable, Detail: safeProfitError(err)})
		response.ProfitUnavailableReason = "main_groups_failed"
		annotateProfitIssues(&response, runID, quality.ObservedAt)
		return response, nil
	}

	groupByID := make(map[string]upstream.AdminGroupInfo, len(groups))
	currentGroupIDs := make(map[string]struct{}, len(groups))
	groupOrder := make([]string, 0, len(groups))
	for _, group := range groups {
		groupID := strings.TrimSpace(group.ID)
		if groupID == "" {
			continue
		}
		if _, exists := groupByID[groupID]; !exists {
			groupOrder = append(groupOrder, groupID)
		}
		groupByID[groupID] = group
		currentGroupIDs[groupID] = struct{}{}
	}

	displayStats, err := s.platform.FetchSub2APIAdminGroupDailyStatsByIDForDate(session, date)
	if err != nil {
		status, retryable := profitErrorMeta(err)
		response.Issues = append(response.Issues, ProfitIssue{Code: "main_group_revenue_failed", Source: "main_admin", Stage: "revenue", HTTPStatus: status, Retryable: retryable, Detail: safeProfitError(err)})
		response.ProfitUnavailableReason = "main_group_revenue_failed"
		annotateProfitIssues(&response, runID, quality.ObservedAt)
		return response, nil
	}
	displayRevenueByGroup := make(map[string]float64, len(groupOrder)+len(displayStats))
	for _, groupID := range groupOrder {
		displayRevenueByGroup[groupID] = 0
	}
	for _, stat := range displayStats {
		groupID := strings.TrimSpace(stat.GroupID)
		if groupID == "" {
			continue
		}
		if _, exists := groupByID[groupID]; !exists {
			groupByID[groupID] = upstream.AdminGroupInfo{ID: groupID, Name: strings.TrimSpace(stat.GroupName)}
			groupOrder = append(groupOrder, groupID)
		}
		displayRevenueByGroup[groupID] += stat.TodayActualCost
	}

	connections, err := s.realConnections.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		status, retryable := profitErrorMeta(err)
		response.Issues = append(response.Issues, ProfitIssue{Code: "real_connections_failed", Source: "real_connections", Stage: "binding", HTTPStatus: status, Retryable: retryable, Detail: safeProfitError(err)})
		response.ProfitUnavailableReason = "real_connections_failed"
		response.Groups = realGroupRevenueItems(groupOrder, groupByID, displayRevenueByGroup, nil, nil, nil)
		response.TotalRevenue = sumGroupRevenue(response.Groups)
		response.Total = response.TotalRevenue
		annotateProfitIssues(&response, runID, quality.ObservedAt)
		return response, nil
	}

	disabledSiteIDs := make(map[string]struct{})
	if s.upstreams != nil {
		for _, site := range s.upstreams.List(ctx, userID) {
			if !site.IsEnabled() {
				disabledSiteIDs[site.ID] = struct{}{}
			}
		}
	}
	initialActive := make([]my_sites.RealConnection, 0, len(connections))
	for _, connection := range connections {
		if _, disabled := disabledSiteIDs[strings.TrimSpace(connection.UpstreamSiteID)]; disabled {
			continue
		}
		if strings.TrimSpace(connection.Status) == "active" {
			initialActive = append(initialActive, connection)
			continue
		}
		// 自动核对已经退出的本地绑定不再作为首页问题；保留它们只用于历史
		// 记录时，不能让一条明确停用的记录阻塞其他分组的正式利润。
		if s.realReconciler == nil {
			response.Issues = append(response.Issues, connectionProfitIssue(connection, "real_connection_inactive", "real_connections", "binding", strings.Join(normalizedIDs(connection.OwnGroupIDs), ",")))
		}
	}

	costByKey := make(map[string]float64)
	costsComplete := s.upstreams != nil
	if s.upstreams == nil {
		if s.realReconciler == nil {
			// The non-reconciling test/compatibility path reports this below.
		}
	} else {
		costResponse, costErr := s.upstreamKeyUsageTodayForDate(ctx, userID, date, true)
		for _, item := range costResponse.Keys {
			key := strings.TrimSpace(item.SiteID) + "\x00" + strings.TrimSpace(item.KeyID)
			costByKey[key] = item.TodayAmount
		}
		if costErr != nil {
			costsComplete = false
			status, retryable := profitErrorMeta(costErr)
			response.Issues = append(response.Issues, ProfitIssue{Code: "upstream_cost_failed", Source: "upstream", Stage: "cost", HTTPStatus: status, Retryable: retryable, Detail: safeProfitError(costErr)})
		}
		if costResponse.FailedSites > 0 {
			costsComplete = false
		}
	}

	active := initialActive
	membershipFailures := map[string]ProfitIssue{}
	if s.realReconciler != nil {
		active, membershipFailures = s.reconcileActiveConnections(ctx, &response, initialActive, groupByID, currentGroupIDs, session, costByKey, costsComplete)
	}
	quality.ExpectedConnections = len(active)
	states := newProfitConnectionStates(active)
	for _, state := range states {
		if issue, failed := membershipFailures[state.Connection.ID]; failed {
			markProfitConnection(state, ProfitAllocationFailed, issue)
		}
	}
	if s.realReconciler == nil {
		// Compatibility path for lightweight readers used by existing tests. The
		// production server injects a reconciler and resolves membership changes
		// before states are built.
		validateRealConnectionMembership(states, &response, groupByID, currentGroupIDs, session, s.platform)
	}

	revenueStates := pendingProfitStatesByScope(states)
	scopeKeys := make([]string, 0, len(revenueStates))
	for scopeKey := range revenueStates {
		scopeKeys = append(scopeKeys, scopeKey)
	}
	sort.Strings(scopeKeys)
	for _, scopeKey := range scopeKeys {
		pendingStates := revenueStates[scopeKey]
		if len(pendingStates) == 0 {
			continue
		}
		accountID := strings.TrimSpace(pendingStates[0].Connection.AdminAccountID)
		groupID := pendingStates[0].GroupID
		stats, statsErr := s.platform.FetchAdminUsageStatsForScope(session, accountID, groupID, date, date)
		if statsErr != nil {
			status, retryable := profitErrorMeta(statsErr)
			for _, state := range pendingStates {
				issue := connectionProfitIssue(state.Connection, "usage_stats_failed", "main_admin", "revenue", groupID)
				issue.HTTPStatus = status
				issue.Retryable = retryable
				issue.Detail = safeProfitError(statsErr)
				markProfitConnection(state, ProfitAllocationFailed, issue)
			}
			continue
		}
		for _, state := range pendingStates {
			state.Revenue = floatPtr(stats.TotalActualCost)
		}
	}

	if s.upstreams == nil {
		for _, state := range states {
			if state.Status == ProfitAllocationPending {
				markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(state.Connection, "upstream_cost_unavailable", "upstream", "cost", state.GroupID))
			}
		}
	}

	allocation := finalizeRealConnectionProfit(states, displayRevenueByGroup, costByKey)
	response.Issues = append(response.Issues, allocation.Issues...)
	if allocation.UnboundCost > profitAmountTolerance {
		response.UnboundUpstreamCost = floatPtr(allocation.UnboundCost)
	}

	response.Groups = realGroupRevenueItems(groupOrder, groupByID, displayRevenueByGroup, allocation.Groups, &allocation, states)
	if allocation.UnboundCost > profitAmountTolerance {
		response.Groups = append(response.Groups, unboundUpstreamCostContribution(allocation.UnboundCost))
	}
	response.TotalRevenue = sumGroupRevenue(response.Groups)
	response.Total = response.TotalRevenue
	quality.ResolvedConnections = allocation.ResolvedConnections
	quality.UnallocatableConnections = allocation.UnallocatableConnections
	quality.FailedConnections = allocation.FailedConnections

	closedConnections := quality.ResolvedConnections + quality.UnallocatableConnections + quality.FailedConnections
	if closedConnections != quality.ExpectedConnections {
		quality.Status = ProfitAllocationUnavailable
		response.ProfitUnavailableReason = "real_connection_accounting_incomplete"
		response.Issues = append(response.Issues, ProfitIssue{Code: "real_connection_accounting_incomplete", Source: "real_connections", Stage: "reconciliation"})
	} else if quality.ExpectedConnections > 0 && quality.ResolvedConnections == quality.ExpectedConnections {
		quality.Status = ProfitAllocationExact
		response.ProfitAvailable = true
		var totalCost float64
		var totalProfit float64
		for _, group := range response.Groups {
			if group.Status != ProfitAllocationExact || group.TodayCost == nil || group.TodayProfit == nil {
				continue
			}
			totalCost += *group.TodayCost
			totalProfit += *group.TodayProfit
		}
		response.TotalCost = floatPtr(totalCost)
		response.TotalProfit = floatPtr(totalProfit)
	} else if quality.ResolvedConnections > 0 {
		quality.Status = "partial"
		response.ProfitUnavailableReason = "partial_real_connections"
	} else if quality.ExpectedConnections > 0 {
		quality.Status = ProfitAllocationUnavailable
		response.ProfitUnavailableReason = "real_connection_profit_unavailable"
	} else {
		response.ProfitUnavailableReason = "no_active_real_connections"
	}
	annotateProfitIssues(&response, runID, quality.ObservedAt)
	for index := range response.Groups {
		groupID := response.Groups[index].GroupID
		for _, issue := range response.Issues {
			if issue.GroupID == groupID || strings.Contains(","+issue.GroupID+",", ","+groupID+",") {
				response.Groups[index].Issues = append(response.Groups[index].Issues, issue)
			}
		}
	}
	log.Printf("dashboard group profit run_id=%s date=%s status=%s expected=%d resolved=%d unallocatable=%d failed=%d issues=%d", runID, date, quality.Status, quality.ExpectedConnections, quality.ResolvedConnections, quality.UnallocatableConnections, quality.FailedConnections, len(response.Issues))
	return response, nil
}

// reconcileActiveConnections resolves local bindings against the complete
// current admin-group membership snapshot. It is deliberately conservative:
// an incomplete membership/cost snapshot never causes a destructive action.
func (s *MetricsService) reconcileActiveConnections(ctx context.Context, response *GroupUsageTodayResponse, connections []my_sites.RealConnection, groupByID map[string]upstream.AdminGroupInfo, currentGroupIDs map[string]struct{}, session upstream.Session, costByKey map[string]float64, costsComplete bool) ([]my_sites.RealConnection, map[string]ProfitIssue) {
	type member struct {
		GroupID string
		Status  string
	}
	memberships := make(map[string][]member)
	membershipErrors := make(map[string]error)
	membershipFailures := make(map[string]ProfitIssue)
	groupIDs := make([]string, 0, len(currentGroupIDs))
	for groupID := range currentGroupIDs {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		group := groupByID[groupID]
		if status := strings.TrimSpace(group.Status); status != "" && !strings.EqualFold(status, "active") {
			continue
		}
		accounts, err := s.platform.ListAdminGroupAccounts(session, group)
		if err != nil {
			membershipErrors[groupID] = err
			continue
		}
		for _, account := range accounts {
			accountID := strings.TrimSpace(account.ID)
			if accountID == "" {
				continue
			}
			memberships[accountID] = append(memberships[accountID], member{GroupID: groupID, Status: strings.TrimSpace(account.Status)})
		}
	}

	keyOwners := make(map[string]int)
	accountOwners := make(map[string]int)
	for _, conn := range connections {
		if key := profitKey(conn); strings.Trim(conn.UpstreamSiteID, " ") != "" && strings.Trim(conn.UpstreamKeyID, " ") != "" {
			keyOwners[key]++
		}
		if accountID := strings.TrimSpace(conn.AdminAccountID); accountID != "" {
			accountOwners[accountID]++
		}
	}

	active := make([]my_sites.RealConnection, 0, len(connections))
	firstMembershipErrorGroupID := ""
	for _, groupID := range groupIDs {
		if _, failed := membershipErrors[groupID]; failed {
			firstMembershipErrorGroupID = groupID
			break
		}
	}
	for _, conn := range connections {
		accountID := strings.TrimSpace(conn.AdminAccountID)
		key := profitKey(conn)
		retire := func(reason string) bool {
			if err := s.realReconciler.RetireRealConnection(ctx, conn); err != nil {
				issue := connectionProfitIssue(conn, "real_connection_reconcile_failed", "real_connections", "reconciliation", strings.Join(normalizedIDs(conn.OwnGroupIDs), ","))
				issue.Detail = safeProfitError(err)
				response.Issues = append(response.Issues, issue)
				active = append(active, conn)
				return false
			}
			log.Printf("dashboard group profit retired stale real connection id=%s reason=%s", conn.ID, reason)
			return true
		}
		if accountID == "" || strings.TrimSpace(conn.UpstreamSiteID) == "" || strings.TrimSpace(conn.UpstreamKeyID) == "" {
			retire("incomplete_binding")
			continue
		}
		if keyOwners[key] > 1 || accountOwners[accountID] > 1 {
			retire("duplicate_binding")
			continue
		}
		if firstMembershipErrorGroupID != "" {
			issue := connectionProfitIssue(conn, "main_group_membership_failed", "main_admin", "membership", strings.Join(normalizedIDs(conn.OwnGroupIDs), ","))
			status, retryable := profitErrorMeta(membershipErrors[firstMembershipErrorGroupID])
			issue.HTTPStatus = status
			issue.Retryable = retryable
			issue.Detail = safeProfitError(membershipErrors[firstMembershipErrorGroupID])
			membershipFailures[conn.ID] = issue
			active = append(active, conn)
			continue
		}
		if !costsComplete {
			// Do not infer a transfer/drop while either authoritative snapshot is
			// incomplete. The normal attribution path will return the precise
			// retryable issue for this run.
			active = append(active, conn)
			continue
		}
		members := memberships[accountID]
		if len(members) != 1 {
			if len(members) == 0 {
				retire("account_not_in_any_active_group")
			} else {
				retire("account_in_multiple_groups")
			}
			continue
		}
		current := members[0]
		if _, exists := costByKey[key]; !exists {
			retire("upstream_key_missing")
			continue
		}
		if _, exists := currentGroupIDs[current.GroupID]; !exists {
			retire("group_missing")
			continue
		}
		group := groupByID[current.GroupID]
		wantedNames := []string{strings.TrimSpace(group.Name)}
		wantedIDs := []string{current.GroupID}
		if !sameStringSet(normalizedIDs(conn.OwnGroupIDs), wantedIDs) || !sameStringSet(normalizedNames(conn.OwnGroupNames), wantedNames) {
			if err := s.realReconciler.ReassignRealConnectionGroups(ctx, conn, wantedIDs, wantedNames); err != nil {
				issue := connectionProfitIssue(conn, "real_connection_reconcile_failed", "real_connections", "reconciliation", current.GroupID)
				issue.Detail = safeProfitError(err)
				response.Issues = append(response.Issues, issue)
				active = append(active, conn)
				continue
			}
			conn.OwnGroupIDs = wantedIDs
			conn.OwnGroupNames = wantedNames
			log.Printf("dashboard group profit reassigned real connection id=%s group=%s", conn.ID, current.GroupID)
		}
		active = append(active, conn)
	}
	return active, membershipFailures
}

func validateRealConnectionMembership(states []*profitConnectionState, response *GroupUsageTodayResponse, groupByID map[string]upstream.AdminGroupInfo, currentGroupIDs map[string]struct{}, session upstream.Session, platform PlatformClient) {
	statesByGroup := pendingProfitStatesByGroup(states)
	groupIDs := make([]string, 0, len(statesByGroup))
	for groupID := range statesByGroup {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		pendingStates := statesByGroup[groupID]
		group, known := groupByID[groupID]
		_, current := currentGroupIDs[groupID]
		if !known || !current {
			for _, state := range pendingStates {
				markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(state.Connection, "main_group_missing", "main_admin", "membership", groupID))
			}
			continue
		}
		if status := strings.TrimSpace(group.Status); status != "" && !strings.EqualFold(status, "active") {
			for _, state := range pendingStates {
				issue := connectionProfitIssue(state.Connection, "main_group_inactive", "main_admin", "membership", groupID)
				issue.Detail = status
				markProfitConnection(state, ProfitAllocationFailed, issue)
			}
			continue
		}
		accounts, accountErr := platform.ListAdminGroupAccounts(session, group)
		if accountErr != nil {
			status, retryable := profitErrorMeta(accountErr)
			for _, state := range pendingStates {
				issue := connectionProfitIssue(state.Connection, "main_group_membership_failed", "main_admin", "membership", groupID)
				issue.HTTPStatus = status
				issue.Retryable = retryable
				issue.Detail = safeProfitError(accountErr)
				markProfitConnection(state, ProfitAllocationFailed, issue)
			}
			continue
		}
		membership := make(map[string]struct{}, len(accounts))
		for _, account := range accounts {
			if accountID := strings.TrimSpace(account.ID); accountID != "" {
				membership[accountID] = struct{}{}
			}
		}
		for _, state := range pendingStates {
			if _, exists := membership[strings.TrimSpace(state.Connection.AdminAccountID)]; !exists {
				markProfitConnection(state, ProfitAllocationFailed, connectionProfitIssue(state.Connection, "main_group_membership_changed", "main_admin", "membership", groupID))
			}
		}
	}
	_ = response
}

func normalizedNames(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStringSet(left, right []string) bool {
	left = normalizedNames(left)
	right = normalizedNames(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func pendingProfitStatesByGroup(states []*profitConnectionState) map[string][]*profitConnectionState {
	result := make(map[string][]*profitConnectionState)
	for _, state := range states {
		if state.Status == ProfitAllocationPending && state.GroupID != "" {
			result[state.GroupID] = append(result[state.GroupID], state)
		}
	}
	return result
}

func pendingProfitStatesByScope(states []*profitConnectionState) map[string][]*profitConnectionState {
	result := make(map[string][]*profitConnectionState)
	for _, state := range states {
		if state.Status != ProfitAllocationPending || state.GroupID == "" {
			continue
		}
		key := strings.TrimSpace(state.Connection.AdminAccountID) + "\x00" + state.GroupID
		result[key] = append(result[key], state)
	}
	return result
}

func realGroupRevenueItems(groupOrder []string, groupByID map[string]upstream.AdminGroupInfo, displayRevenueByGroup map[string]float64, allocations map[string]profitAllocationGroup, allocation *profitAllocationResult, states []*profitConnectionState) []GroupUsageTodayItem {
	items := make([]GroupUsageTodayItem, 0, len(groupOrder))
	for _, groupID := range groupOrder {
		group := groupByID[groupID]
		revenue := displayRevenueByGroup[groupID]
		item := GroupUsageTodayItem{
			GroupID:      groupID,
			GroupName:    strings.TrimSpace(group.Name),
			TodayAmount:  revenue,
			TodayRevenue: revenue,
			Status:       ProfitAllocationUnavailable,
		}
		if allocated, exists := allocations[groupID]; exists {
			item.Status = allocated.Status
			item.TodayCost = allocated.Cost
			item.TodayProfit = allocated.Profit
		}
		if allocation != nil {
			for _, state := range states {
				if !containsString(state.GroupIDs, groupID) {
					continue
				}
				connection := state.Connection
				connectionItem := GroupProfitConnection{
					ConnectionID: connection.ID,
					AccountID:    strings.TrimSpace(connection.AdminAccountID),
					GroupID:      groupID,
					SiteID:       strings.TrimSpace(connection.UpstreamSiteID),
					KeyID:        strings.TrimSpace(connection.UpstreamKeyID),
					Status:       state.Status,
				}
				if revenue := allocation.ConnectionRevenue[connection.ID]; revenue != nil {
					connectionItem.Revenue = floatPtr(*revenue)
				}
				if allocated, exists := allocation.Connections[connection.ID]; exists {
					connectionItem.Status = allocated.Status
					connectionItem.Cost = allocated.Cost
					connectionItem.Profit = allocated.Profit
				}
				item.Connections = append(item.Connections, connectionItem)
			}
		}
		items = append(items, item)
	}
	return items
}

func unboundUpstreamCostContribution(cost float64) GroupUsageTodayItem {
	profit := -cost
	return GroupUsageTodayItem{
		GroupID:          "__unbound_upstream_cost__",
		GroupName:        "未分组上游成本",
		ContributionKind: "unbound_upstream_cost",
		TodayCost:        floatPtr(cost),
		TodayProfit:      floatPtr(profit),
		Status:           ProfitAllocationExact,
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sumGroupRevenue(groups []GroupUsageTodayItem) float64 {
	var total float64
	for _, group := range groups {
		total += group.TodayRevenue
	}
	return total
}

func annotateProfitIssues(response *GroupUsageTodayResponse, runID, observedAt string) {
	for index := range response.Issues {
		response.Issues[index].RunID = runID
		response.Issues[index].ObservedAt = observedAt
	}
}

func safeProfitError(err error) string {
	if err == nil {
		return ""
	}
	var requestErr *upstream.RequestError
	if errors.As(err, &requestErr) {
		return requestErr.MessageKey
	}
	return "request_failed"
}

func profitErrorMeta(err error) (status int, retryable bool) {
	var requestErr *upstream.RequestError
	if !errors.As(err, &requestErr) {
		return 0, true
	}
	status = requestErr.StatusCode
	return status, requestErr.MessageKey == upstream.ErrorRequest && (status == 0 || status >= http.StatusInternalServerError)
}

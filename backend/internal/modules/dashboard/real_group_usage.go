package dashboard

import (
	"context"
	"sort"
	"strings"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// realGroupUsageToday provides the main platform's authoritative group revenue.
// Direct profit is optional: only a unique existing connection with both a
// scoped account revenue and a matching upstream Key cost contributes. It does
// not need to cover every account in the group and never changes connections.
func (s *MetricsService) realGroupUsageToday(ctx context.Context, userID, adminAccountID string, session upstream.Session, date string) (GroupUsageTodayResponse, error) {
	groups, err := s.platform.FetchAdminAllGroups(session)
	if err != nil {
		return GroupUsageTodayResponse{}, err
	}

	groupByID := make(map[string]upstream.AdminGroupInfo, len(groups))
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
	}

	stats, err := s.platform.FetchSub2APIAdminGroupDailyStatsByIDForDate(session, date)
	if err != nil {
		return GroupUsageTodayResponse{}, err
	}
	revenueByGroup := make(map[string]float64, len(groupOrder)+len(stats))
	for _, groupID := range groupOrder {
		revenueByGroup[groupID] = 0
	}
	for _, stat := range stats {
		groupID := strings.TrimSpace(stat.GroupID)
		if groupID == "" {
			continue
		}
		if _, exists := groupByID[groupID]; !exists {
			groupByID[groupID] = upstream.AdminGroupInfo{ID: groupID, Name: strings.TrimSpace(stat.GroupName)}
			groupOrder = append(groupOrder, groupID)
		}
		revenueByGroup[groupID] += stat.TodayActualCost
	}

	items := realGroupRevenueItems(groupOrder, groupByID, revenueByGroup)
	s.attachDirectGroupProfit(ctx, userID, adminAccountID, session, date, items)
	totalRevenue := sumGroupRevenue(items)
	return GroupUsageTodayResponse{
		Date:         date,
		Total:        totalRevenue,
		TotalRevenue: totalRevenue,
		Groups:       items,
	}, nil
}

func (s *MetricsService) attachDirectGroupProfit(ctx context.Context, userID, adminAccountID string, session upstream.Session, date string, groups []GroupUsageTodayItem) {
	if s.realConnections == nil || s.upstreams == nil {
		return
	}
	connections, err := s.realConnections.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return
	}
	costResponse, _ := s.upstreamKeyUsageTodayForDate(ctx, userID, date, true)
	costByKey := make(map[string]float64, len(costResponse.Keys))
	for _, item := range costResponse.Keys {
		costByKey[directProfitKey(item.SiteID, item.KeyID)] = item.TodayAmount
	}

	eligible := uniqueDirectProfitConnections(connections, costByKey)
	if len(eligible) == 0 {
		return
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	byGroup := make(map[string]*directGroupTotals)
	for _, connection := range eligible {
		stats, err := s.platform.FetchAdminUsageStatsForScope(session, connection.AdminAccountID, connection.OwnGroupIDs[0], date, date)
		if err != nil {
			continue
		}
		cost := costByKey[directProfitKey(connection.UpstreamSiteID, connection.UpstreamKeyID)]
		totals := byGroup[connection.OwnGroupIDs[0]]
		if totals == nil {
			totals = &directGroupTotals{}
			byGroup[connection.OwnGroupIDs[0]] = totals
		}
		totals.revenue += stats.TotalActualCost
		totals.cost += cost
	}
	for index := range groups {
		totals := byGroup[groups[index].GroupID]
		if totals == nil {
			continue
		}
		revenue, cost := totals.revenue, totals.cost
		profit := revenue - cost
		groups[index].DirectRevenue = &revenue
		groups[index].DirectCost = &cost
		groups[index].TodayProfit = &profit
	}
}

type directGroupTotals struct {
	revenue float64
	cost    float64
}

func uniqueDirectProfitConnections(connections []my_sites.RealConnection, costByKey map[string]float64) []my_sites.RealConnection {
	keyCounts := make(map[string]int)
	scopeCounts := make(map[string]int)
	for _, connection := range connections {
		if strings.TrimSpace(connection.Status) != "active" || len(normalizedDirectGroupIDs(connection.OwnGroupIDs)) != 1 {
			continue
		}
		key := directProfitKey(connection.UpstreamSiteID, connection.UpstreamKeyID)
		scope := strings.TrimSpace(connection.AdminAccountID) + "\x00" + normalizedDirectGroupIDs(connection.OwnGroupIDs)[0]
		if key == "\x00" || strings.TrimSpace(connection.AdminAccountID) == "" {
			continue
		}
		keyCounts[key]++
		scopeCounts[scope]++
	}
	result := make([]my_sites.RealConnection, 0, len(connections))
	for _, connection := range connections {
		groupIDs := normalizedDirectGroupIDs(connection.OwnGroupIDs)
		if strings.TrimSpace(connection.Status) != "active" || len(groupIDs) != 1 || strings.TrimSpace(connection.AdminAccountID) == "" {
			continue
		}
		key := directProfitKey(connection.UpstreamSiteID, connection.UpstreamKeyID)
		scope := strings.TrimSpace(connection.AdminAccountID) + "\x00" + groupIDs[0]
		if keyCounts[key] != 1 || scopeCounts[scope] != 1 {
			continue
		}
		if _, ok := costByKey[key]; !ok {
			continue
		}
		connection.OwnGroupIDs = groupIDs
		result = append(result, connection)
	}
	return result
}

func normalizedDirectGroupIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
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
	return result
}

func directProfitKey(siteID, keyID string) string {
	return strings.TrimSpace(siteID) + "\x00" + strings.TrimSpace(keyID)
}

func realGroupRevenueItems(groupOrder []string, groupByID map[string]upstream.AdminGroupInfo, revenueByGroup map[string]float64) []GroupUsageTodayItem {
	items := make([]GroupUsageTodayItem, 0, len(groupOrder))
	for _, groupID := range groupOrder {
		group := groupByID[groupID]
		revenue := revenueByGroup[groupID]
		items = append(items, GroupUsageTodayItem{
			GroupID:      groupID,
			GroupName:    strings.TrimSpace(group.Name),
			TodayAmount:  revenue,
			TodayRevenue: revenue,
		})
	}
	return items
}

func sumGroupRevenue(groups []GroupUsageTodayItem) float64 {
	var total float64
	for _, group := range groups {
		total += group.TodayRevenue
	}
	return total
}

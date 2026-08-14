package dashboard

import (
	"context"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// realGroupUsageToday only reads the main platform's authoritative group
// revenue. It never waits for connection or upstream-key profit reads.
func (s *MetricsService) realGroupUsageToday(ctx context.Context, userID, adminAccountID string, session upstream.Session, date string) (GroupUsageTodayResponse, error) {
	groups, err := s.platform.FetchAdminAllGroups(session)
	if err != nil {
		return s.cachedGroupRevenue(ctx, userID, adminAccountID, date, err)
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
		return s.cachedGroupRevenue(ctx, userID, adminAccountID, date, err)
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
	totalRevenue := sumGroupRevenue(items)
	s.saveGroupRevenueCache(ctx, userID, adminAccountID, items)
	return GroupUsageTodayResponse{
		Date:         date,
		Total:        totalRevenue,
		TotalRevenue: totalRevenue,
		Groups:       items,
	}, nil
}

func (s *MetricsService) cachedGroupRevenue(ctx context.Context, userID, adminAccountID, date string, cause error) (GroupUsageTodayResponse, error) {
	if s.metricsRepo == nil {
		return GroupUsageTodayResponse{}, cause
	}
	cached, err := s.metricsRepo.ListGroupMetricCache(ctx, userID, adminAccountID, "revenue")
	if err != nil || len(cached) == 0 {
		return GroupUsageTodayResponse{}, cause
	}
	items := make([]GroupUsageTodayItem, 0, len(cached))
	var fallbackAt *time.Time
	for _, item := range cached {
		if item.TodayRevenue == nil {
			continue
		}
		revenue := *item.TodayRevenue
		items = append(items, GroupUsageTodayItem{
			GroupID: item.GroupID, GroupName: item.GroupName,
			TodayAmount: revenue, TodayRevenue: revenue,
		})
		if fallbackAt == nil || item.ObservedAt.Before(*fallbackAt) {
			observedAt := item.ObservedAt
			fallbackAt = &observedAt
		}
	}
	if len(items) == 0 {
		return GroupUsageTodayResponse{}, cause
	}
	totalRevenue := sumGroupRevenue(items)
	return GroupUsageTodayResponse{
		Date: date, Total: totalRevenue, TotalRevenue: totalRevenue,
		Groups: items, Fallback: true, FallbackAt: fallbackAt,
	}, nil
}

func (s *MetricsService) saveGroupRevenueCache(ctx context.Context, userID, adminAccountID string, groups []GroupUsageTodayItem) {
	if s.metricsRepo == nil {
		return
	}
	now := time.Now()
	items := make([]GroupMetricCacheItem, 0, len(groups))
	for _, group := range groups {
		revenue := group.TodayRevenue
		items = append(items, GroupMetricCacheItem{
			MetricType: "revenue", GroupID: group.GroupID, GroupName: group.GroupName,
			TodayRevenue: &revenue, ObservedAt: now,
		})
	}
	_ = s.metricsRepo.SaveGroupMetricCache(ctx, userID, adminAccountID, items)
}

// realGroupProfitToday calculates only the safely attributable direct profit.
// Failed groups keep their latest successful profit and never block revenue.
func (s *MetricsService) realGroupProfitToday(ctx context.Context, userID, adminAccountID string, session upstream.Session, date string) (GroupProfitTodayResponse, error) {
	if s.realConnections == nil || s.upstreams == nil {
		return s.cachedGroupProfit(ctx, userID, adminAccountID, date, nil)
	}
	connections, err := s.realConnections.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return s.cachedGroupProfit(ctx, userID, adminAccountID, date, err)
	}
	eligible := uniqueDirectProfitConnections(connections)
	if len(eligible) == 0 {
		return GroupProfitTodayResponse{Date: date, Groups: []GroupUsageTodayItem{}}, nil
	}
	failedGroups := make(map[string]struct{})
	for _, connection := range eligible {
		failedGroups[connection.OwnGroupIDs[0]] = struct{}{}
	}
	costResponse, costErr := s.upstreamKeyUsageTodayForDate(ctx, userID, date, true)
	if costErr != nil {
		response, mergeErr := s.mergeGroupProfitFallback(ctx, userID, adminAccountID, date, nil, failedGroups)
		if mergeErr != nil || len(response.Groups) == 0 {
			return GroupProfitTodayResponse{}, costErr
		}
		return response, nil
	}
	costByKey := make(map[string]float64, len(costResponse.Keys))
	for _, item := range costResponse.Keys {
		costByKey[directProfitKey(item.SiteID, item.KeyID)] = item.TodayAmount
	}

	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	byGroup := make(map[string]*directGroupTotals)
	groupNames := make(map[string]string)
	clear(failedGroups)
	for _, connection := range eligible {
		groupID := connection.OwnGroupIDs[0]
		if len(connection.OwnGroupNames) > 0 {
			groupNames[groupID] = strings.TrimSpace(connection.OwnGroupNames[0])
		}
		cost, costOK := costByKey[directProfitKey(connection.UpstreamSiteID, connection.UpstreamKeyID)]
		if !costOK {
			failedGroups[groupID] = struct{}{}
			continue
		}
		stats, err := s.platform.FetchAdminUsageStatsForScope(session, connection.AdminAccountID, groupID, date, date)
		if err != nil {
			failedGroups[groupID] = struct{}{}
			continue
		}
		totals := byGroup[groupID]
		if totals == nil {
			totals = &directGroupTotals{}
			byGroup[groupID] = totals
		}
		totals.revenue += stats.TotalActualCost
		totals.cost += cost
	}
	current := make([]GroupUsageTodayItem, 0, len(byGroup))
	cacheItems := make([]GroupMetricCacheItem, 0, len(byGroup))
	now := time.Now()
	for groupID, totals := range byGroup {
		if _, failed := failedGroups[groupID]; failed {
			continue
		}
		revenue, cost := totals.revenue, totals.cost
		profit := revenue - cost
		name := groupNames[groupID]
		if name == "" {
			name = groupID
		}
		current = append(current, GroupUsageTodayItem{
			GroupID: groupID, GroupName: name,
			DirectRevenue: &revenue, DirectCost: &cost, TodayProfit: &profit,
		})
		cacheItems = append(cacheItems, GroupMetricCacheItem{
			MetricType: "profit", GroupID: groupID, GroupName: name,
			DirectRevenue: &revenue, DirectCost: &cost, TodayProfit: &profit, ObservedAt: now,
		})
	}
	if s.metricsRepo != nil && len(cacheItems) > 0 {
		_ = s.metricsRepo.SaveGroupMetricCache(ctx, userID, adminAccountID, cacheItems)
	}
	return s.mergeGroupProfitFallback(ctx, userID, adminAccountID, date, current, failedGroups)
}

func (s *MetricsService) cachedGroupProfit(ctx context.Context, userID, adminAccountID, date string, cause error) (GroupProfitTodayResponse, error) {
	if s.metricsRepo == nil {
		if cause != nil {
			return GroupProfitTodayResponse{}, cause
		}
		return GroupProfitTodayResponse{Date: date, Groups: []GroupUsageTodayItem{}}, nil
	}
	cached, err := s.metricsRepo.ListGroupMetricCache(ctx, userID, adminAccountID, "profit")
	if err != nil || len(cached) == 0 {
		if cause != nil {
			return GroupProfitTodayResponse{}, cause
		}
		return GroupProfitTodayResponse{Date: date, Groups: []GroupUsageTodayItem{}}, nil
	}
	return groupProfitResponseFromCache(date, cached), nil
}

func (s *MetricsService) mergeGroupProfitFallback(ctx context.Context, userID, adminAccountID, date string, current []GroupUsageTodayItem, failedGroups map[string]struct{}) (GroupProfitTodayResponse, error) {
	currentIDs := make(map[string]struct{}, len(current))
	for _, item := range current {
		currentIDs[item.GroupID] = struct{}{}
	}
	var fallbackAt *time.Time
	fallbackGroups := 0
	if s.metricsRepo != nil {
		cached, _ := s.metricsRepo.ListGroupMetricCache(ctx, userID, adminAccountID, "profit")
		for _, item := range cached {
			if _, exists := currentIDs[item.GroupID]; exists || item.TodayProfit == nil {
				continue
			}
			if _, failed := failedGroups[item.GroupID]; !failed {
				continue
			}
			current = append(current, GroupUsageTodayItem{
				GroupID: item.GroupID, GroupName: item.GroupName,
				DirectRevenue: item.DirectRevenue, DirectCost: item.DirectCost, TodayProfit: item.TodayProfit,
			})
			fallbackGroups++
			if fallbackAt == nil || item.ObservedAt.Before(*fallbackAt) {
				observedAt := item.ObservedAt
				fallbackAt = &observedAt
			}
		}
	}
	unavailableGroups := len(failedGroups) - fallbackGroups
	if unavailableGroups < 0 {
		unavailableGroups = 0
	}
	sort.Slice(current, func(i, j int) bool { return current[i].GroupID < current[j].GroupID })
	var total float64
	for _, item := range current {
		if item.TodayProfit != nil {
			total += *item.TodayProfit
		}
	}
	return GroupProfitTodayResponse{
		Date: date, TotalProfit: total, Groups: current,
		FallbackGroups: fallbackGroups, UnavailableGroups: unavailableGroups, FallbackAt: fallbackAt,
	}, nil
}

func groupProfitResponseFromCache(date string, cached []GroupMetricCacheItem) GroupProfitTodayResponse {
	items := make([]GroupUsageTodayItem, 0, len(cached))
	var total float64
	var fallbackAt *time.Time
	for _, item := range cached {
		if item.TodayProfit == nil {
			continue
		}
		items = append(items, GroupUsageTodayItem{
			GroupID: item.GroupID, GroupName: item.GroupName,
			DirectRevenue: item.DirectRevenue, DirectCost: item.DirectCost, TodayProfit: item.TodayProfit,
		})
		total += *item.TodayProfit
		if fallbackAt == nil || item.ObservedAt.Before(*fallbackAt) {
			observedAt := item.ObservedAt
			fallbackAt = &observedAt
		}
	}
	return GroupProfitTodayResponse{
		Date: date, TotalProfit: total, Groups: items,
		FallbackGroups: len(items), FallbackAt: fallbackAt,
	}
}

type directGroupTotals struct {
	revenue float64
	cost    float64
}

func uniqueDirectProfitConnections(connections []my_sites.RealConnection) []my_sites.RealConnection {
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

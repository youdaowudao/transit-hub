package connection_health

import (
	"context"
	"log"
	"sort"
	"strings"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func (s *Service) refreshRelatedUpstreamSites(ctx context.Context, userID string, adminAccountID string, force bool) ([]upstream.SyncSiteResult, []my_sites.RealConnection, bool, string) {
	if s.upstreamSync == nil || s.mySites == nil {
		return nil, nil, false, ""
	}
	connections, err := s.mySites.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		log.Printf("[connection-health] relevant site refresh skipped workspace=%s err=%v", adminAccountID, err)
		return nil, nil, false, "site_sync_connections"
	}
	return s.upstreamSync.SyncSites(ctx, userID, adminAccountID, relatedUpstreamSiteIDs(connections), force), connections, true, ""
}

func relatedUpstreamSiteIDs(connections []my_sites.RealConnection) []string {
	seen := make(map[string]struct{})
	for _, connection := range connections {
		siteID := strings.TrimSpace(connection.UpstreamSiteID)
		if siteID == "" {
			continue
		}
		seen[siteID] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for siteID := range seen {
		ids = append(ids, siteID)
	}
	sort.Strings(ids)
	return ids
}

func mergeAdminGroupsRefreshSummary(syncSites []upstream.SyncSiteResult, multiplier AdminGroupsRefreshSummary, syncErrorKey string) AdminGroupsRefreshSummary {
	merged := make(map[string]AdminGroupsRefreshSite, len(syncSites)+len(multiplier.Sites))
	for _, site := range multiplier.Sites {
		site.ErrorKey = multiplierRefreshErrorKey(site.ErrorKey)
		merged[site.SiteID] = site
	}
	for _, site := range syncSites {
		if site.SiteID == "" {
			continue
		}
		result := AdminGroupsRefreshSite{SiteID: site.SiteID, Status: site.Status, ErrorKey: site.ErrorKey}
		if site.Status == "success" {
			if _, exists := merged[site.SiteID]; !exists {
				merged[site.SiteID] = result
			}
			continue
		}
		merged[site.SiteID] = result
	}
	sites := make([]AdminGroupsRefreshSite, 0, len(merged))
	for _, site := range merged {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].SiteID < sites[j].SiteID })
	state := refreshSummaryState(sites)
	errorKey := multiplier.ErrorKey
	if syncErrorKey != "" {
		errorKey = syncErrorKey
		if state == "success" {
			state = "failure"
		}
	}
	return AdminGroupsRefreshSummary{State: state, ErrorKey: errorKey, Sites: sites}
}

func multiplierRefreshErrorKey(errorKey string) string {
	if errorKey == "" {
		return ""
	}
	return "multiplier_" + errorKey
}

func refreshSummaryState(sites []AdminGroupsRefreshSite) string {
	anySuccess := false
	anyFailure := false
	anyTimeout := false
	for _, site := range sites {
		if site.Status == "success" {
			anySuccess = true
			continue
		}
		anyFailure = true
		if site.Status == "timeout" || strings.HasSuffix(site.ErrorKey, "_timeout") {
			anyTimeout = true
		}
	}
	switch {
	case anyTimeout:
		return "timeout"
	case anyFailure && anySuccess:
		return "partial"
	case anyFailure:
		return "failure"
	default:
		return "success"
	}
}

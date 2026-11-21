package connection_health

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

const (
	targetMultiplierWorkspaceChanged = requestError("workspace_changed")
	targetMultiplierUnsupported      = requestError("unsupported_platform")
	targetMultiplierTargetNotFound   = requestError("target_not_found")
	targetMultiplierSessionReadError = requestError("admin.connectionHealth.targetMultiplierRefresh.sessionReadFailed")
	targetMultiplierLoginRequired    = requestError("admin.connectionHealth.targetMultiplierRefresh.sessionLoginRequired")
	targetMultiplierAdminRequired    = requestError("admin.connectionHealth.targetMultiplierRefresh.sessionAdminRequired")
	targetMultiplierConnectionsError = requestError("admin.connectionHealth.boundSiteRateStatus.failedConnections")
	targetMultiplierSyncLimit        = 2 * time.Minute
)

type TargetMultiplierRefreshResult struct {
	NoBinding bool                          `json:"noBinding"`
	Sites     []targetMultiplierRefreshSite `json:"sites"`
}

type targetMultiplierRefreshSite struct {
	upstream.SyncSiteResult
	SiteName string `json:"siteName,omitempty"`
}

func (h *Handler) refreshTargetMultiplier(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		WorkspaceID *string `json:"workspaceId"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.WorkspaceID == nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.RefreshTargetMultiplier(r.Context(), userID, *input.WorkspaceID, r.PathValue("id"))
	switch err {
	case nil:
		httpjson.Write(w, http.StatusOK, result)
	case targetMultiplierWorkspaceChanged, targetMultiplierLoginRequired, targetMultiplierAdminRequired:
		httpjson.WriteError(w, http.StatusConflict, err.Error())
	case targetMultiplierTargetNotFound:
		httpjson.WriteError(w, http.StatusNotFound, err.Error())
	case targetMultiplierSessionReadError, targetMultiplierConnectionsError:
		httpjson.WriteError(w, http.StatusInternalServerError, err.Error())
	default:
		writeError(w, err)
	}
}

func (s *Service) RefreshTargetMultiplier(ctx context.Context, userID, workspaceID, targetID string) (TargetMultiplierRefreshResult, error) {
	var result TargetMultiplierRefreshResult
	currentWorkspace, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		if err.Error() == ErrorNoCurrentAccount {
			return result, requestError(ErrorNoCurrentAccount)
		}
		return result, err
	}
	if currentWorkspace != workspaceID {
		return result, targetMultiplierWorkspaceChanged
	}
	if s.mySites == nil {
		return result, targetMultiplierSessionReadError
	}
	session, err := s.mySites.RequireSession(ctx, userID, workspaceID)
	if err != nil {
		switch err.Error() {
		case my_sites.ErrorAuthRequired:
			return result, targetMultiplierLoginRequired
		case my_sites.ErrorAdminOnly:
			return result, targetMultiplierAdminRequired
		default:
			return result, targetMultiplierSessionReadError
		}
	}
	if session.Platform != upstream.PlatformSub2API {
		return result, targetMultiplierUnsupported
	}
	accountID, valid := scopedActionAccountID(targetID, workspaceID)
	if !valid {
		return result, targetMultiplierTargetNotFound
	}
	connections, err := s.mySites.ListRealConnectionsForWorkspace(ctx, userID, workspaceID)
	if err != nil {
		return result, targetMultiplierConnectionsError
	}

	boundSiteIDs := make(map[string]struct{})
	for _, connection := range connections {
		if strings.TrimSpace(connection.AdminAccountID) != accountID || (connection.Status != "" && connection.Status != my_sites.ConnectionStatusActive) {
			continue
		}
		if platform := strings.TrimSpace(connection.AdminPlatform); platform != "" && !strings.EqualFold(platform, string(upstream.PlatformSub2API)) {
			continue
		}
		if siteID := strings.TrimSpace(connection.UpstreamSiteID); siteID != "" {
			boundSiteIDs[siteID] = struct{}{}
		}
	}

	result.Sites = make([]targetMultiplierRefreshSite, 0, len(boundSiteIDs))
	if len(boundSiteIDs) > 0 && s.sites == nil {
		return result, errors.New("upstream site lookup is unavailable")
	}
	siteIDs := make([]string, 0, len(boundSiteIDs))
	siteNames := make(map[string]string, len(boundSiteIDs))
	for _, siteID := range sortedKeys(boundSiteIDs) {
		site, err := s.sites.GetSite(ctx, siteID)
		if err != nil || (site != nil && (site.UserID != userID || site.AdminAccountID != workspaceID)) {
			result.Sites = append(result.Sites, targetMultiplierRefreshSite{SyncSiteResult: upstream.SyncSiteResult{SiteID: siteID, Status: "site_read_failed"}})
			continue
		}
		if !site.IsEnabled() {
			continue
		}
		siteIDs = append(siteIDs, siteID)
		siteNames[siteID] = site.Name
	}
	if len(siteIDs) == 0 {
		result.NoBinding = len(result.Sites) == 0
		return result, nil
	}
	if s.upstreamSync == nil {
		return result, errors.New("upstream site sync is unavailable")
	}

	s.initializeAdminGroupsRefreshRuntime()
	syncCtx, cancel := context.WithTimeout(s.refreshRootCtx, targetMultiplierSyncLimit)
	defer cancel()
	for _, syncResult := range s.upstreamSync.SyncSites(syncCtx, userID, workspaceID, siteIDs, true) {
		result.Sites = append(result.Sites, targetMultiplierRefreshSite{SyncSiteResult: syncResult, SiteName: siteNames[syncResult.SiteID]})
	}
	s.expireTargetMultiplierSnapshots(userID, workspaceID, siteIDs)
	return result, nil
}

func (s *Service) expireTargetMultiplierSnapshots(userID, workspaceID string, siteIDs []string) {
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	for _, siteID := range siteIDs {
		entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, workspaceID, siteID)]
		if entry == nil || entry.inFlight {
			continue
		}
		clearMultiplierManualMarker(entry)
		entry.confirmedDeletedKeys = nil
		entry.expiresAt = time.Time{}
		entry.nextRetryAt = time.Time{}
	}
}

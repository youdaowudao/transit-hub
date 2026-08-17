package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type recordingUpstreamSyncCoordinator struct {
	results []upstream.SyncSiteResult
	calls   []struct {
		userID         string
		adminAccountID string
		siteIDs        []string
		force          bool
	}
}

type countingPipelineMetadataReader struct {
	*snapshotMetadataReader
	listCalls int
}

type failingPipelineConnectionsReader struct {
	*snapshotMetadataReader
	err error
}

func (r *failingPipelineConnectionsReader) ListRealConnectionsForWorkspace(context.Context, string, string) ([]my_sites.RealConnection, error) {
	return nil, r.err
}

func (r *countingPipelineMetadataReader) ListRealConnectionsForWorkspace(ctx context.Context, userID string, adminAccountID string) ([]my_sites.RealConnection, error) {
	r.listCalls++
	return r.snapshotMetadataReader.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
}

func (c *recordingUpstreamSyncCoordinator) SyncSites(_ context.Context, userID string, adminAccountID string, siteIDs []string, force bool) []upstream.SyncSiteResult {
	c.calls = append(c.calls, struct {
		userID         string
		adminAccountID string
		siteIDs        []string
		force          bool
	}{userID: userID, adminAccountID: adminAccountID, siteIDs: append([]string(nil), siteIDs...), force: force})
	if c.results != nil {
		return append([]upstream.SyncSiteResult(nil), c.results...)
	}
	return []upstream.SyncSiteResult{{SiteID: "site-1", Status: "success"}}
}

func TestAdminGroupsFreshRefreshPipelineIncludesSiteSyncFailureInTerminalSummary(t *testing.T) {
	metadataReader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{
			connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
			session:     upstream.Session{Platform: upstream.PlatformSub2API},
		},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
		},
	}
	service := newAdminGroupsService(
		fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}},
		metadataReader,
		newFakeRepository(),
	)
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{
		results: []upstream.SyncSiteResult{{SiteID: "site-1", Status: "auth_failed", ErrorKey: "site_sync_auth"}},
	})

	result, err := service.AdminGroupsFreshResult(context.Background(), "user1")
	if err != nil {
		t.Fatalf("AdminGroupsFreshResult() error = %v", err)
	}
	if result.Refresh.State != "failure" {
		t.Fatalf("refresh state = %q, want failure", result.Refresh.State)
	}
	if len(result.Refresh.Sites) != 1 || result.Refresh.Sites[0].Status != "auth_failed" || result.Refresh.Sites[0].ErrorKey != "site_sync_auth" {
		t.Fatalf("refresh sites = %+v, want site_sync auth failure", result.Refresh.Sites)
	}
}

func TestAdminGroupsFreshRefreshPipelineReadsWorkspaceConnectionsOnce(t *testing.T) {
	metadataReader := &countingPipelineMetadataReader{snapshotMetadataReader: &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{
			connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
			session:     upstream.Session{Platform: upstream.PlatformSub2API},
		},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
		},
	}}
	service := newAdminGroupsService(
		fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}},
		metadataReader,
		newFakeRepository(),
	)
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{})

	if _, err := service.AdminGroupsFreshResult(context.Background(), "user1"); err != nil {
		t.Fatalf("AdminGroupsFreshResult() error = %v", err)
	}
	if metadataReader.listCalls != 1 {
		t.Fatalf("workspace connection reads = %d, want 1", metadataReader.listCalls)
	}
}

func TestAdminGroupsFreshRefreshPipelineSyncsOnlyRelevantSites(t *testing.T) {
	metadataReader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{
			connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
			session:     upstream.Session{Platform: upstream.PlatformSub2API},
		},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
		},
	}
	service := newAdminGroupsService(
		fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}},
		metadataReader,
		newFakeRepository(),
	)
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
	syncCoordinator := &recordingUpstreamSyncCoordinator{}
	service.SetUpstreamSyncCoordinator(syncCoordinator)

	if _, err := service.AdminGroupsFreshResult(context.Background(), "user1"); err != nil {
		t.Fatalf("AdminGroupsFreshResult() error = %v", err)
	}
	if len(syncCoordinator.calls) != 1 {
		t.Fatalf("relevant site sync calls = %d, want 1", len(syncCoordinator.calls))
	}
	call := syncCoordinator.calls[0]
	if call.userID != "user1" || call.adminAccountID != "ws1" || !call.force {
		t.Fatalf("sync call = %+v, want user1/ws1 forced refresh", call)
	}
	if len(call.siteIDs) != 1 || call.siteIDs[0] != "site-1" {
		t.Fatalf("sync site IDs = %v, want [site-1]", call.siteIDs)
	}
}

func TestAdminGroupsFreshRefreshPipelineReportsConnectionDiscoveryFailure(t *testing.T) {
	metadataReader := &failingPipelineConnectionsReader{
		snapshotMetadataReader: &snapshotMetadataReader{
			fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		},
		err: errors.New("connection repository unavailable"),
	}
	service := newAdminGroupsService(
		fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}},
		metadataReader,
		newFakeRepository(),
	)
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{})

	result, err := service.AdminGroupsFreshResult(context.Background(), "user1")
	if err != nil {
		t.Fatalf("AdminGroupsFreshResult() error = %v", err)
	}
	if result.Refresh.State != "failure" || result.Refresh.ErrorKey != "site_sync_connections" {
		t.Fatalf("refresh summary = %+v, want safe connection-discovery failure", result.Refresh)
	}
}

func TestMultiplierRefreshSummaryReportsGenericFailureStage(t *testing.T) {
	service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{
		multiplierSnapshotKey("user1", "ws1", "site-1"): {
			siteID: "site-1", status: "unavailable", lastOutcome: "unavailable",
		},
	}}

	result := service.multiplierRefreshSummary("user1", "ws1")
	if result.State != "failure" || len(result.Sites) != 1 || result.Sites[0].ErrorKey != "unavailable" {
		t.Fatalf("multiplier summary = %+v, want unavailable error key", result)
	}
}

func TestRefreshAdminGroupsHandlerUsesAutomaticGetAndManualPost(t *testing.T) {
	for _, testCase := range []struct {
		method string
		force  bool
	}{
		{method: http.MethodGet, force: false},
		{method: http.MethodPost, force: true},
	} {
		metadataReader := &snapshotMetadataReader{
			fakeMySitesReader: fakeMySitesReader{
				connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
				session:     upstream.Session{Platform: upstream.PlatformSub2API},
			},
			directItems: map[string]upstream.Sub2APIKeyItem{
				"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
			},
		}
		service := newAdminGroupsService(
			fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "group-1", Name: "group"}}},
			metadataReader,
			newFakeRepository(),
		)
		service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
		syncCoordinator := &recordingUpstreamSyncCoordinator{}
		service.SetUpstreamSyncCoordinator(syncCoordinator)
		mux := http.NewServeMux()
		RegisterRoutes(mux, service)

		request := httptest.NewRequest(testCase.method, "/api/connection-health/admin-groups/refresh", nil)
		request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", testCase.method, response.Code, response.Body.String())
		}
		var result AdminGroupsFreshResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("%s response decode error: %v", testCase.method, err)
		}
		if result.Groups == nil || result.Refresh.Sites == nil {
			t.Fatalf("%s response = %+v, want groups and refresh sites arrays", testCase.method, result)
		}
		if len(syncCoordinator.calls) != 1 || syncCoordinator.calls[0].force != testCase.force {
			t.Fatalf("%s sync call = %+v, want force=%t", testCase.method, syncCoordinator.calls, testCase.force)
		}
	}
}

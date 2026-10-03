package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type statusCheckConnectionRepository struct {
	items          []RealConnection
	reconcileCalls int
}

func (r *statusCheckConnectionRepository) SaveRealConnection(context.Context, RealConnection) error {
	return nil
}

func (r *statusCheckConnectionRepository) ListRealConnections(_ context.Context, userID string, adminAccountID string) ([]RealConnection, error) {
	var result []RealConnection
	for _, item := range r.items {
		if item.UserID == userID && item.WorkspaceAdminAccountID == adminAccountID {
			result = append(result, item)
		}
	}
	return result, nil
}

func (r *statusCheckConnectionRepository) GetRealConnection(_ context.Context, id string, userID string, adminAccountID string) (*RealConnection, error) {
	for _, item := range r.items {
		if item.ID == id && item.UserID == userID && item.WorkspaceAdminAccountID == adminAccountID {
			copy := item
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *statusCheckConnectionRepository) DeleteRealConnection(context.Context, string, string, string) error {
	return nil
}

func (r *statusCheckConnectionRepository) ReconcileRealConnectionStatuses(_ context.Context, userID string, adminAccountID string, mainAccountIDs []string) (RealConnectionCheckResponse, error) {
	r.reconcileCalls++
	response := RealConnectionCheckResponse{}
	for index := range r.items {
		item := &r.items[index]
		if item.UserID != userID || item.WorkspaceAdminAccountID != adminAccountID || item.AdminAccountID == "" || (item.AdminPlatform != "" && item.AdminPlatform != string(upstream.PlatformSub2API)) {
			continue
		}
		response.Checked++
		if slices.Contains(mainAccountIDs, item.AdminAccountID) {
			item.Status = ConnectionStatusActive
			response.Active++
		} else {
			item.Status = ConnectionStatusMissing
			response.Missing++
		}
	}
	return response, nil
}

func TestCheckRealConnectionsMarksMissingAndRestoresFromCompleteInventory(t *testing.T) {
	accountIDs := []string{"account-existing", "account-inactive", "account-error"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
		case "/api/v1/admin/accounts":
			items := make([]map[string]any, 0, len(accountIDs))
			for _, id := range accountIDs {
				status := "active"
				if id == "account-inactive" {
					status = "inactive"
				}
				if id == "account-error" {
					status = "error"
				}
				items = append(items, map[string]any{"id": id, "status": status, "group_ids": []any{}})
			}
			writeConnectionTestJSON(w, map[string]any{
				"data": map[string]any{
					"items":   items,
					"list":    items,
					"records": items,
					"total":   len(items),
					"count":   len(items),
				},
				"items": items,
				"total": len(items),
				"count": len(items),
			})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()

	session := platformTestSession(upstream.PlatformSub2API, server.URL)
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "workspace-1", Session: session}}
	connRepo := &statusCheckConnectionRepository{items: []RealConnection{
		{ID: "existing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-existing", Status: ConnectionStatusMissing},
		{ID: "deleted", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-deleted", Status: ConnectionStatusActive},
		{ID: "inactive", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-inactive", Status: ConnectionStatusActive},
		{ID: "error", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-error", Status: ConnectionStatusActive},
		{ID: "blank", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "", Status: ConnectionStatusActive},
		{ID: "other-workspace", UserID: "user-1", WorkspaceAdminAccountID: "workspace-2", AdminAccountID: "account-deleted", Status: ConnectionStatusActive},
	}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	service.connRepository = connRepo

	response, err := service.CheckRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CheckRealConnections: %v", err)
	}
	if response != (RealConnectionCheckResponse{Checked: 4, Active: 3, Missing: 1}) {
		t.Fatalf("unexpected response %#v", response)
	}
	if connRepo.items[0].Status != ConnectionStatusActive || connRepo.items[1].Status != ConnectionStatusMissing {
		t.Fatalf("unexpected reconciled statuses %#v", connRepo.items[:2])
	}
	if connRepo.items[4].Status != ConnectionStatusActive || connRepo.items[5].Status != ConnectionStatusActive {
		t.Fatalf("blank ID or other workspace changed: %#v", connRepo.items[4:])
	}

	accountIDs = append(accountIDs, "account-deleted")
	response, err = service.CheckRealConnections(context.Background(), "user-1")
	if err != nil || response.Missing != 0 || connRepo.items[1].Status != ConnectionStatusActive {
		t.Fatalf("restored account did not reactivate: response=%#v status=%q err=%v", response, connRepo.items[1].Status, err)
	}
}

func TestCheckRealConnectionsInventoryFailureDoesNotWriteStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"role": "admin"}})
			return
		}
		http.Error(w, "inventory unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	session := platformTestSession(upstream.PlatformSub2API, server.URL)
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "workspace-1", Session: session}}
	connRepo := &statusCheckConnectionRepository{items: []RealConnection{{
		ID: "connection-1", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-1", Status: ConnectionStatusActive,
	}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	service.connRepository = connRepo

	if _, err := service.CheckRealConnections(context.Background(), "user-1"); err == nil {
		t.Fatal("expected inventory failure")
	}
	if connRepo.reconcileCalls != 0 || connRepo.items[0].Status != ConnectionStatusActive {
		t.Fatalf("failed inventory wrote statuses: calls=%d item=%#v", connRepo.reconcileCalls, connRepo.items[0])
	}
}

func TestCheckRealConnectionsAmbiguousInventoryDoesNotWriteStatuses(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "different data and items",
			payload: map[string]any{
				"data":  []map[string]any{{"id": "account-1"}},
				"items": []map[string]any{{"id": "account-other"}},
				"total": 1,
			},
		},
		{
			name: "different total and count",
			payload: map[string]any{
				"data":  []map[string]any{{"id": "account-1"}},
				"total": 1,
				"count": 2,
			},
		},
		{
			name: "different nested and top-level total",
			payload: map[string]any{
				"data": map[string]any{
					"items": []map[string]any{{"id": "account-1"}},
					"total": 1,
				},
				"total": 2,
			},
		},
		{
			name: "different nested item containers",
			payload: map[string]any{
				"data": map[string]any{
					"items":   []map[string]any{{"id": "account-1"}},
					"list":    []map[string]any{{"id": "account-other"}},
					"records": []map[string]any{{"id": "account-1"}},
					"total":   1,
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/auth/me" {
					writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
					return
				}
				writeConnectionTestJSON(w, test.payload)
			}))
			defer server.Close()

			session := platformTestSession(upstream.PlatformSub2API, server.URL)
			stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "workspace-1", Session: session}}
			connRepo := &statusCheckConnectionRepository{items: []RealConnection{{
				ID: "connection-1", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-1", Status: ConnectionStatusActive,
			}}}
			service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
			service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
			service.connRepository = connRepo

			if _, err := service.CheckRealConnections(context.Background(), "user-1"); err == nil {
				t.Fatal("expected ambiguous inventory failure")
			}
			if connRepo.reconcileCalls != 0 || connRepo.items[0].Status != ConnectionStatusActive {
				t.Fatalf("ambiguous inventory wrote statuses: calls=%d item=%#v", connRepo.reconcileCalls, connRepo.items[0])
			}
		})
	}
}

func TestCheckRealConnectionsHandlerUsesAuthenticatedWorkspaceAndReturnsCountsOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
		case "/api/v1/admin/accounts":
			writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": "account-1"}}, "total": 1})
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()
	session := platformTestSession(upstream.PlatformSub2API, server.URL)
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "workspace-1", Session: session}}
	connRepo := &statusCheckConnectionRepository{items: []RealConnection{{
		ID: "connection-1", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "account-1", Status: ConnectionStatusMissing,
	}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	service.connRepository = connRepo
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connections/check", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connections/check", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user-1"))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["checked"] != float64(1) || response["active"] != float64(1) || response["missing"] != float64(0) || len(response) != 3 {
		t.Fatalf("unsafe or incorrect response %#v", response)
	}
}

type failingRuntimeCleaner struct {
	calls int
	err   error
}

func (c *failingRuntimeCleaner) CleanupRealConnectionRuntime(context.Context, string, string, string) error {
	c.calls++
	return c.err
}

func TestMissingConnectionRejectsFullDeleteWithoutRemoteCalls(t *testing.T) {
	connRepo := &testConnRepo{connection: &RealConnection{
		ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", AdminAccountID: "remote-1",
		ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusMissing,
	}}
	service := &Service{connRepository: connRepo}
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})

	err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{ConnectionID: "missing", Mode: "full"})
	if !errors.Is(err, requestError(ErrorManagedDeleteOnly)) || connRepo.deleteCalls != 0 || connRepo.connection == nil {
		t.Fatalf("missing full delete was not rejected safely: err=%v deletes=%d connection=%#v", err, connRepo.deleteCalls, connRepo.connection)
	}
}

func TestMissingConnectionRuntimeCleanupFailureKeepsConnection(t *testing.T) {
	connRepo := &testConnRepo{connection: &RealConnection{
		ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", Status: ConnectionStatusMissing,
	}}
	cleaner := &failingRuntimeCleaner{err: errors.New("cleanup failed")}
	service := &Service{connRepository: connRepo, runtimeCleaner: cleaner}
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})

	err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{ConnectionID: "missing", Mode: "unlink"})
	if err == nil || cleaner.calls != 1 || connRepo.deleteCalls != 0 || connRepo.connection == nil {
		t.Fatalf("cleanup failure did not preserve retry row: err=%v cleaner=%d deletes=%d", err, cleaner.calls, connRepo.deleteCalls)
	}
}

func TestMissingConnectionUnlinkCleansRuntimeThenDeletesLocalRow(t *testing.T) {
	connRepo := &testConnRepo{connection: &RealConnection{
		ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
		Status: ConnectionStatusMissing, PricingMappingEnabled: true,
	}}
	cleaner := &failingRuntimeCleaner{}
	service := &Service{connRepository: connRepo, runtimeCleaner: cleaner}
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	removePricing := false

	err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
		ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
	})
	if err != nil || cleaner.calls != 1 || connRepo.deleteCalls != 1 || connRepo.connection != nil {
		t.Fatalf("missing unlink did not clean and delete locally: err=%v cleaner=%d deletes=%d connection=%#v", err, cleaner.calls, connRepo.deleteCalls, connRepo.connection)
	}
}

func TestActiveManagedConnectionStillAllowsFullRemoteDelete(t *testing.T) {
	adminDeletes := 0
	adminServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/admin/accounts/22":
			adminDeletes++
			writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"message": "Account deleted successfully"}})
		default:
			t.Fatalf("unexpected admin request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer adminServer.Close()

	keyDeletes := 0
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/keys/upstream-key-1" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		keyDeletes++
		writeConnectionTestJSON(w, map[string]any{"data": map[string]any{}})
	}))
	defer upstreamServer.Close()

	adminSession := platformTestSession(upstream.PlatformSub2API, adminServer.URL)
	upstreamSession := platformTestSession(upstream.PlatformSub2API, upstreamServer.URL)
	stateRepo := &testStateRepo{state: &State{UserID: "user-1", AdminAccountID: "workspace-1", Session: adminSession}}
	connRepo := &testConnRepo{connection: &RealConnection{
		ID: "active", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
		UpstreamSiteID: "site-1", UpstreamKeyID: "upstream-key-1", AdminAccountID: "22",
		ProvisioningMode: ProvisioningModeManaged, Status: ConnectionStatusActive,
		UpstreamPlatform: string(upstream.PlatformSub2API), AdminPlatform: string(upstream.PlatformSub2API),
	}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(adminServer.Client())), testUpstreamLookup{sites: map[string]*upstream.Site{
		"site-1": {ID: "site-1", UserID: "user-1", AdminAccountID: "workspace-1", Session: &upstreamSession},
	}})
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	service.connRepository = connRepo
	service.SetSafeAdminAccountDeletion(&stageATestSafeDeletion{apply: func(ctx context.Context, user, workspace string, session upstream.Session, id, source string) error {
		if source != "manual_delete" || id != "22" {
			t.Fatal("wrong protected full delete")
		}
		return service.platformService.DeleteSub2APIAdminAccountContext(ctx, session, id)
	}})

	err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{ConnectionID: "active", Mode: "full"})
	if err != nil || adminDeletes != 1 || keyDeletes != 1 || connRepo.deleteCalls != 1 || connRepo.connection != nil {
		t.Fatalf("active full delete regressed: err=%v admin=%d key=%d local=%d connection=%#v", err, adminDeletes, keyDeletes, connRepo.deleteCalls, connRepo.connection)
	}
}

func TestAutoPricingTargetFilteringHandlesMissingActiveAndManualMappings(t *testing.T) {
	target := UpstreamGroupRef{SiteID: "site-1", GroupName: "group-1"}
	tests := []struct {
		name        string
		connections []RealConnection
		wantReason  string
		wantTargets int
	}{
		{name: "priced missing only skips", connections: []RealConnection{{UpstreamSiteID: target.SiteID, UpstreamGroupName: target.GroupName, Status: ConnectionStatusMissing, PricingMappingEnabled: true}}, wantReason: "main_account_missing", wantTargets: 0},
		{name: "unpriced missing does not block manual mapping", connections: []RealConnection{{UpstreamSiteID: target.SiteID, UpstreamGroupName: target.GroupName, Status: ConnectionStatusMissing, PricingMappingEnabled: false}}, wantTargets: 1},
		{name: "active wins", connections: []RealConnection{{UpstreamSiteID: target.SiteID, UpstreamGroupName: target.GroupName, Status: ConnectionStatusMissing, PricingMappingEnabled: true}, {UpstreamSiteID: target.SiteID, UpstreamGroupName: target.GroupName, Status: ConnectionStatusActive}}, wantTargets: 1},
		{name: "manual mapping continues", wantTargets: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for index := range test.connections {
				test.connections[index].UserID = "user-1"
				test.connections[index].WorkspaceAdminAccountID = "workspace-1"
			}
			repo := &statusCheckConnectionRepository{items: test.connections}
			service := &Service{connRepository: repo}
			mapping, reason, err := service.withoutMissingConnectionTargets(context.Background(), "user-1", "workspace-1", GroupMapping{
				OwnGroup: "own", AutoPricingSource: "lowest_upstream", UpstreamTargets: []UpstreamGroupRef{target},
			})
			if err != nil || reason != test.wantReason || len(mapping.UpstreamTargets) != test.wantTargets {
				t.Fatalf("filter result mapping=%#v reason=%q err=%v", mapping, reason, err)
			}
		})
	}
}

func TestManualAutoPricingMissingOnlyPersistsSkipWithoutRemoteWrite(t *testing.T) {
	remoteWrites := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me" {
			writeConnectionTestJSON(w, map[string]any{"data": map[string]any{"role": "admin"}})
			return
		}
		if r.Method != http.MethodGet {
			remoteWrites++
		}
		t.Fatalf("missing-only manual pricing made unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	session := platformTestSession(upstream.PlatformSub2API, server.URL)
	stateRepo := &testStateRepo{state: &State{
		UserID: "user-1", AdminAccountID: "workspace-1", Session: session,
		Mappings: []GroupMapping{{
			OwnGroup: "own", EnableAutoPricing: true, AutoPricingSource: "lowest_upstream", AutoPricingStrategy: "fixed",
			UpstreamTargets: []UpstreamGroupRef{{SiteID: "site-1", GroupName: "group-1"}},
		}},
	}}
	connRepo := &statusCheckConnectionRepository{items: []RealConnection{{
		ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-1", UpstreamGroupName: "group-1", Status: ConnectionStatusMissing, PricingMappingEnabled: true,
	}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	service.connRepository = connRepo

	response, err := service.RunAutoPricingNow(context.Background(), "user-1", AutoPricingRunRequest{OwnGroup: "own"})
	if err != nil {
		t.Fatalf("RunAutoPricingNow: %v", err)
	}
	if response.Result.Status != "skipped" || response.Result.Reason != "main_account_missing" || response.Result.Trigger != "manual" {
		t.Fatalf("unexpected missing-only result %#v", response.Result)
	}
	if remoteWrites != 0 {
		t.Fatalf("missing-only manual pricing wrote remotely %d times", remoteWrites)
	}
}

func TestAutomaticPricingMissingOnlyPersistsSkipWithoutRemoteWrite(t *testing.T) {
	remoteWrites := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/groups" {
			writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 1, "name": "own", "rate_multiplier": 1.0}}})
			return
		}
		if r.Method != http.MethodGet {
			remoteWrites++
		}
		t.Fatalf("missing-only automatic pricing made unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	session := platformTestSession(upstream.PlatformSub2API, server.URL)
	stateRepo := &testStateRepo{state: &State{
		UserID: "user-1", AdminAccountID: "workspace-1", Session: session,
		Mappings: []GroupMapping{{
			OwnGroup: "own", EnableAutoPricing: true, AutoPricingSource: "lowest_upstream", AutoPricingStrategy: "fixed",
			UpstreamTargets: []UpstreamGroupRef{{SiteID: "site-1", GroupName: "group-1"}},
		}},
	}}
	connRepo := &statusCheckConnectionRepository{items: []RealConnection{{
		ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-1", UpstreamGroupName: "group-1", Status: ConnectionStatusMissing, PricingMappingEnabled: true,
	}}}
	service := NewService(stateRepo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	service.connRepository = connRepo
	service.ApplyAutoPricingAfterSync(
		context.Background(), "user-1", "workspace-1", "site-1", "source",
		upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "group-1", Name: "group-1", Multiplier: floatPtr(1)}}},
		upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "group-1", Name: "group-1", Multiplier: floatPtr(2)}}},
	)

	status := stateRepo.state.Mappings[0].LastAutoPricingRun
	if status == nil || status.Status != "skipped" || status.Reason != "main_account_missing" || status.Trigger != "after_sync" {
		t.Fatalf("unexpected automatic missing-only status %#v", status)
	}
	if remoteWrites != 0 {
		t.Fatalf("missing-only automatic pricing wrote remotely %d times", remoteWrites)
	}
}

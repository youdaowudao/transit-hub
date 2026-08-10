package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type noCurrentPrioritySyncAccountResolver struct{}

func (noCurrentPrioritySyncAccountResolver) RequireCurrentID(context.Context, string) (string, error) {
	return "", requestError(ErrorNoCurrentAccount)
}

// failingPrioritySyncUpstreamReader makes any accidental upstream read fail the
// test. The priority-sync endpoint must resolve the local workspace and read
// exactly one persisted workspace row.
type failingPrioritySyncUpstreamReader struct{}

func (failingPrioritySyncUpstreamReader) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	panic("priority-sync must not fetch upstream groups")
}

func (failingPrioritySyncUpstreamReader) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	panic("priority-sync must not fetch upstream accounts")
}

func (failingPrioritySyncUpstreamReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	panic("priority-sync must not resolve upstream credentials")
}

func TestPrioritySyncEndpointReadsOnlyPersistedWorkspaceState(t *testing.T) {
	repo := newFakeRepository()
	repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", PendingTargetCount: 2, LastWriteRoundTargetCount: 7, WritebackSpreadSeconds: 5,
	}
	service := &Service{
		repo:           repo,
		accounts:       fakeAdminAccountResolver{id: "ws1"},
		platformGroups: failingPrioritySyncUpstreamReader{},
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := httptest.NewRequest(http.MethodGet, "/api/connection-health/priority-sync", nil)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got PriorityWorkspaceSyncState
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got.LastWriteRoundTargetCount != 7 || got.PendingTargetCount != 2 || got.WritebackSpreadSeconds != 5 {
		t.Fatalf("unexpected state: %+v", got)
	}
}

func TestPrioritySyncEndpointReturnsNullWithoutStateAndPreservesAuthContract(t *testing.T) {
	service := &Service{repo: newFakeRepository(), accounts: fakeAdminAccountResolver{id: "ws1"}}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)

	unauthenticated := httptest.NewRecorder()
	mux.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/connection-health/priority-sync", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d, want 401", unauthenticated.Code)
	}

	noStateRequest := httptest.NewRequest(http.MethodGet, "/api/connection-health/priority-sync", nil)
	noStateRequest = noStateRequest.WithContext(authctx.WithUserID(noStateRequest.Context(), "user1"))
	noState := httptest.NewRecorder()
	mux.ServeHTTP(noState, noStateRequest)
	if noState.Code != http.StatusOK || noState.Body.String() != "null\n" {
		t.Fatalf("no-state response=%d body=%q, want 200 JSON null", noState.Code, noState.Body.String())
	}

	noWorkspace := &Service{repo: newFakeRepository(), accounts: noCurrentPrioritySyncAccountResolver{}}
	noWorkspaceMux := http.NewServeMux()
	RegisterRoutes(noWorkspaceMux, noWorkspace)
	noWorkspaceRequest := httptest.NewRequest(http.MethodGet, "/api/connection-health/priority-sync", nil)
	noWorkspaceRequest = noWorkspaceRequest.WithContext(authctx.WithUserID(noWorkspaceRequest.Context(), "user1"))
	noWorkspaceResponse := httptest.NewRecorder()
	noWorkspaceMux.ServeHTTP(noWorkspaceResponse, noWorkspaceRequest)
	if noWorkspaceResponse.Code != http.StatusConflict {
		t.Fatalf("no-workspace status=%d, want 409", noWorkspaceResponse.Code)
	}
}

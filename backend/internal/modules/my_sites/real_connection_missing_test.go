package my_sites

import (
	"context"
	"testing"
)

type missingConnectionRepository struct {
	items []RealConnection
}

func (r *missingConnectionRepository) SaveRealConnection(context.Context, RealConnection) error {
	return nil
}
func (r *missingConnectionRepository) ListRealConnections(_ context.Context, userID, workspaceID string) ([]RealConnection, error) {
	result := make([]RealConnection, 0, len(r.items))
	for _, item := range r.items {
		if item.UserID == userID && item.WorkspaceAdminAccountID == workspaceID {
			result = append(result, item)
		}
	}
	return result, nil
}
func (r *missingConnectionRepository) GetRealConnection(context.Context, string, string, string) (*RealConnection, error) {
	return nil, nil
}
func (r *missingConnectionRepository) DeleteRealConnection(context.Context, string, string, string) error {
	return nil
}

func TestOperationalConnectionReadExcludesMissingButManagementReadKeepsIt(t *testing.T) {
	repository := &missingConnectionRepository{items: []RealConnection{
		{ID: "active", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", Status: "active"},
		{ID: "legacy", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", Status: ""},
		{ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", Status: "missing"},
	}}
	service := NewService(nil, nil, nil)
	service.connRepository = repository
	service.accounts = testAdminResolver{currentID: "workspace-1"}

	management, err := service.ListRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("management read: %v", err)
	}
	if len(management) != 3 {
		t.Fatalf("management read must keep missing visible, got %#v", management)
	}

	operational, err := service.ListRealConnectionsForWorkspace(context.Background(), "user-1", "workspace-1")
	if err != nil {
		t.Fatalf("operational read: %v", err)
	}
	if len(operational) != 2 || operational[0].ID != "active" || operational[1].ID != "legacy" {
		t.Fatalf("operational read must exclude missing, got %#v", operational)
	}
}

func TestMissingConnectionDoesNotBackfillPricingMapping(t *testing.T) {
	state := &State{}
	applyMappingsFromRealConnections(state, map[string]string{"group-1": "主站组"}, []RealConnection{{
		ID: "missing", Status: "missing", OwnGroupIDs: []string{"group-1"},
		UpstreamSiteID: "site-1", UpstreamGroupName: "上游组", PricingMappingEnabled: true,
	}})
	if len(state.Mappings) != 0 {
		t.Fatalf("missing connection must not recreate pricing mapping, got %#v", state.Mappings)
	}
}

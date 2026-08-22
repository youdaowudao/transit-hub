package my_sites

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type realConnectionDisplayRepository struct {
	itemsByWorkspace map[string][]RealConnection
}

func (r *realConnectionDisplayRepository) SaveRealConnection(context.Context, RealConnection) error {
	return nil
}

func (r *realConnectionDisplayRepository) ListRealConnections(_ context.Context, userID, adminAccountID string) ([]RealConnection, error) {
	items := r.itemsByWorkspace[adminAccountID]
	result := make([]RealConnection, 0, len(items))
	for _, item := range items {
		if item.UserID == userID && item.WorkspaceAdminAccountID == adminAccountID {
			result = append(result, item)
		}
	}
	return result, nil
}

func (r *realConnectionDisplayRepository) GetRealConnection(context.Context, string, string, string) (*RealConnection, error) {
	return nil, nil
}

func (r *realConnectionDisplayRepository) DeleteRealConnection(context.Context, string, string, string) error {
	return nil
}

type realConnectionDisplayAccounts struct{ current string }

func (a *realConnectionDisplayAccounts) RequireCurrentID(context.Context, string) (string, error) {
	return a.current, nil
}

type realConnectionDisplaySites struct{ sites map[string]*upstream.Site }

func (s realConnectionDisplaySites) GetSite(_ context.Context, siteID string) (*upstream.Site, error) {
	return s.sites[siteID], nil
}

func TestListRealConnectionsReturnsCompleteSafeDisplayNamesForCurrentWorkspace(t *testing.T) {
	const secretKey = "sk-live-r10-secret-7890"
	repository := &realConnectionDisplayRepository{itemsByWorkspace: map[string][]RealConnection{
		"workspace-a": {{
			ID: "connection-r10", UserID: "user-1", WorkspaceAdminAccountID: "workspace-a",
			UpstreamSiteID: "site-r10", UpstreamGroupID: "upstream-group-r10", UpstreamGroupName: "供应商分组",
			UpstreamKeyID: "key-id-r10", UpstreamKey: secretKey,
			AdminAccountID: "forward-admin-r10", AdminAccountName: "转发连接 A",
			OwnGroupIDs: []string{"main-group-r10"}, OwnGroupNames: []string{"主站高级组"},
			GroupType: "vip", Status: "active", UpstreamPlatform: "sub2api",
		}},
		"workspace-b": {{
			ID: "connection-b", UserID: "user-1", WorkspaceAdminAccountID: "workspace-b",
			UpstreamSiteID: "site-b", UpstreamKeyID: "key-b", UpstreamKey: "sk-workspace-b-secret",
			AdminAccountID: "forward-b", AdminAccountName: "工作区 B 连接", OwnGroupIDs: []string{"group-b"}, OwnGroupNames: []string{"B 组"},
		}},
	}}
	accounts := &realConnectionDisplayAccounts{current: "workspace-a"}
	service := NewService(nil, nil, realConnectionDisplaySites{sites: map[string]*upstream.Site{
		"site-r10": {ID: "site-r10", UserID: "user-1", AdminAccountID: "workspace-a", Name: "上海供应站"},
	}})
	service.connRepository = repository
	service.accounts = accounts

	connections, err := service.ListRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListRealConnections() error: %v", err)
	}
	if len(connections) != 1 || connections[0].ID != "connection-r10" {
		t.Fatalf("workspace-a connections = %#v, want only connection-r10", connections)
	}

	body, err := json.Marshal(connections)
	if err != nil {
		t.Fatalf("marshal connections: %v", err)
	}
	if strings.Contains(string(body), secretKey) || strings.Contains(string(body), "sk-workspace-b-secret") {
		t.Fatalf("real credential leaked in response: %s", body)
	}
	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode public connections: %v", err)
	}
	item := payload[0]
	if item["siteName"] != "上海供应站" {
		t.Fatalf("siteName = %#v, want 上海供应站", item["siteName"])
	}
	if item["connectionName"] != "转发连接 A" {
		t.Fatalf("connectionName = %#v, want 转发连接 A", item["connectionName"])
	}
	keyName, _ := item["keyName"].(string)
	if keyName == "" || keyName == "key-id-r10" || keyName == secretKey || !strings.Contains(keyName, "7890") {
		t.Fatalf("keyName = %q, want a distinguishable safe mask ending in 7890", keyName)
	}
	if item["ownGroupName"] != "主站高级组" {
		t.Fatalf("ownGroupName = %#v, want 主站高级组", item["ownGroupName"])
	}
	if item["upstreamKey"] != "" {
		t.Fatalf("upstreamKey = %#v, want empty", item["upstreamKey"])
	}

	accounts.current = "workspace-b"
	connections, err = service.ListRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListRealConnections(workspace-b) error: %v", err)
	}
	if len(connections) != 1 || connections[0].ID != "connection-b" {
		t.Fatalf("workspace-b connections = %#v, want only connection-b", connections)
	}
}

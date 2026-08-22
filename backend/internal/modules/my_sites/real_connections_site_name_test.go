package my_sites

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type siteNameRepository struct {
	connection RealConnection
}

var _ realConnectionSiteNameProvider = (*Repository)(nil)

func (r siteNameRepository) SaveRealConnection(context.Context, RealConnection) error { return nil }
func (r siteNameRepository) ListRealConnections(context.Context, string, string) ([]RealConnection, error) {
	return []RealConnection{r.connection}, nil
}
func (r siteNameRepository) GetRealConnection(context.Context, string, string, string) (*RealConnection, error) {
	return nil, nil
}
func (r siteNameRepository) DeleteRealConnection(context.Context, string, string, string) error {
	return nil
}

type siteNameCapableRepository struct {
	siteNameRepository
}

func (siteNameCapableRepository) realConnectionSiteNamesProvided() {}

type siteNameAccounts struct{}

func (siteNameAccounts) RequireCurrentID(context.Context, string) (string, error) {
	return "workspace-a", nil
}

type siteNameLookupSpy struct {
	calls int
}

func (s *siteNameLookupSpy) GetSite(context.Context, string) (*upstream.Site, error) {
	s.calls++
	return &upstream.Site{ID: "site-a", UserID: "user-1", AdminAccountID: "workspace-a", Name: "缓存站点名"}, nil
}

func TestListRealConnectionsUsesRepositorySiteNameWithoutLookup(t *testing.T) {
	lookup := &siteNameLookupSpy{}
	service := NewService(nil, nil, lookup)
	service.accounts = siteNameAccounts{}
	service.connRepository = siteNameRepository{connection: RealConnection{
		ID: "connection-a", UserID: "user-1", WorkspaceAdminAccountID: "workspace-a",
		UpstreamSiteID: "site-a", SiteName: "数据库站点名", UpstreamKey: "sk-secret-1234",
		AdminAccountName: "转发连接", OwnGroupNames: []string{"主站分组"},
	}}

	connections, err := service.ListRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListRealConnections() error: %v", err)
	}
	if lookup.calls != 0 {
		t.Fatalf("upstream lookup calls = %d, want 0 when repository supplied SiteName", lookup.calls)
	}
	if len(connections) != 1 || connections[0].SiteName != "数据库站点名" {
		t.Fatalf("connections = %#v, want repository site name", connections)
	}
	if connections[0].UpstreamKey != "" {
		t.Fatalf("public connection leaked upstream key: %#v", connections[0])
	}
}

func TestListRealConnectionsDoesNotLookupEmptySiteNameWhenRepositoryProvidesSiteNames(t *testing.T) {
	lookup := &siteNameLookupSpy{}
	service := NewService(nil, nil, lookup)
	service.accounts = siteNameAccounts{}
	service.connRepository = siteNameCapableRepository{siteNameRepository{connection: RealConnection{
		ID: "connection-empty-site-name", UserID: "user-1", WorkspaceAdminAccountID: "workspace-a",
		UpstreamSiteID: "site-a", UpstreamKey: "sk-secret-5678",
	}}}

	connections, err := service.ListRealConnections(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListRealConnections() error: %v", err)
	}
	if lookup.calls != 0 {
		t.Fatalf("upstream lookup calls = %d, want 0 for a SiteName-capable repository", lookup.calls)
	}
	if len(connections) != 1 || connections[0].SiteName != "" {
		t.Fatalf("connections = %#v, want an empty site name without fallback", connections)
	}
	if connections[0].UpstreamKey != "" {
		t.Fatalf("public connection leaked upstream key: %#v", connections[0])
	}
}

func TestListRealConnectionsQueryScopesJoinedSiteNameToUserAndWorkspace(t *testing.T) {
	createdAt := time.Date(2026, 8, 22, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	rows := &siteNameQueryRows{values: [][]any{{
		"connection-a", "user-1", "workspace-a", "site-a", "group-a", "供应商分组",
		"key-a", "sk-secret", "forward-a", "转发连接", []byte(`["main-a"]`), []byte(`["主站分组"]`),
		"vip", "managed", "active", "sub2api", "sub2api", true, "operation-a", createdAt, "数据库站点名",
	}}}
	queryCalls := 0
	var queryText string
	var queryArgs []any
	connections, err := listRealConnections(context.Background(), "user-1", "workspace-a", func(_ context.Context, query string, args ...any) (realConnectionRows, error) {
		queryCalls++
		queryText = query
		queryArgs = append([]any(nil), args...)
		return rows, nil
	})
	if err != nil {
		t.Fatalf("listRealConnections() error: %v", err)
	}
	if queryCalls != 1 {
		t.Fatalf("query calls = %d, want 1", queryCalls)
	}
	if !reflect.DeepEqual(queryArgs, []any{"user-1", "workspace-a"}) {
		t.Fatalf("query args = %#v, want user and workspace", queryArgs)
	}
	if len(connections) != 1 || connections[0].SiteName != "数据库站点名" {
		t.Fatalf("connections = %#v, want joined site name", connections)
	}
	queryContract := []string{
		"LEFT JOIN upstream_sites",
		"site.id = connection_row.upstream_site_id",
		"site.user_id = connection_row.user_id",
		"site.admin_account_id = connection_row.workspace_admin_account_id",
		"connection_row.user_id = $1",
		"connection_row.workspace_admin_account_id = $2",
		"COALESCE(site.name, '')",
	}
	for _, expected := range queryContract {
		if !strings.Contains(queryText, expected) {
			t.Errorf("ListRealConnections production query missing %q", expected)
		}
	}
}

type siteNameQueryRows struct {
	values [][]any
	index  int
}

func (r *siteNameQueryRows) Close()     {}
func (r *siteNameQueryRows) Err() error { return nil }
func (r *siteNameQueryRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}
func (r *siteNameQueryRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.values) {
		return fmt.Errorf("rows not positioned")
	}
	values := r.values[r.index-1]
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, values = %d", len(dest), len(values))
	}
	for index, target := range dest {
		destination := reflect.ValueOf(target)
		source := reflect.ValueOf(values[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() || !source.Type().AssignableTo(destination.Elem().Type()) {
			return fmt.Errorf("column %d cannot assign %T to %T", index, values[index], target)
		}
		destination.Elem().Set(source)
	}
	return nil
}

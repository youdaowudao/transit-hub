package my_sites

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
)

type cleanupRuntimeRecorder struct {
	calls int
	err   error
}

func (r *cleanupRuntimeRecorder) CleanupRealConnectionRuntime(context.Context, string, string, string) error {
	r.calls++
	return r.err
}

func TestMissingConnectionUnlinkWithPricingRemovalIsLocalScopedAndAtomic(t *testing.T) {
	t.Run("removes its target and keeps unrelated target without remote calls", func(t *testing.T) {
		pool, repository := openMySitesCleanupPostgres(t)
		remoteRequests := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			remoteRequests++
		}))
		defer server.Close()
		prepareCleanupState(t, repository, []UpstreamGroupRef{
			{SiteID: "site-missing", GroupName: "group-missing"},
			{SiteID: "site-kept", GroupName: "group-kept"},
		})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-missing", UpstreamGroupName: "group-missing",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusMissing,
			PricingMappingEnabled: true,
		})
		cleaner := &cleanupRuntimeRecorder{}
		service := NewService(repository, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
		service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
		service.SetConnectionRuntimeCleaner(cleaner)
		if err := service.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema: %v", err)
		}
		removePricing := true

		if err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
			ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
		}); err != nil {
			t.Fatalf("RealDisconnect: %v", err)
		}

		assertCleanupConnectionCount(t, pool, "missing", 0)
		state, err := repository.Get(context.Background(), "user-1", "workspace-1")
		if err != nil || state == nil || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 || state.Mappings[0].UpstreamTargets[0].SiteID != "site-kept" {
			t.Fatalf("unexpected retained mapping state=%#v err=%v", state, err)
		}
		if cleaner.calls != 1 || remoteRequests != 0 {
			t.Fatalf("unexpected cleanup calls=%d remote requests=%d", cleaner.calls, remoteRequests)
		}
	})

	t.Run("keeps a target shared by another pricing connection", func(t *testing.T) {
		pool, repository := openMySitesCleanupPostgres(t)
		prepareCleanupState(t, repository, []UpstreamGroupRef{{SiteID: "site-shared", GroupName: "group-shared"}})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-shared", UpstreamGroupName: "group-shared",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusMissing, PricingMappingEnabled: true,
		})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "active", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-shared", UpstreamGroupName: "group-shared",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusActive, PricingMappingEnabled: true,
		})
		cleaner := &cleanupRuntimeRecorder{}
		service := NewService(repository, nil, nil)
		service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
		service.SetConnectionRuntimeCleaner(cleaner)
		if err := service.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema: %v", err)
		}
		removePricing := true

		if err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
			ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
		}); err != nil {
			t.Fatalf("RealDisconnect: %v", err)
		}

		assertCleanupConnectionCount(t, pool, "missing", 0)
		assertCleanupConnectionCount(t, pool, "active", 1)
		state, err := repository.Get(context.Background(), "user-1", "workspace-1")
		if err != nil || state == nil || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 {
			t.Fatalf("shared mapping was removed state=%#v err=%v", state, err)
		}
	})

	t.Run("runtime cleanup failure keeps connection and mapping", func(t *testing.T) {
		pool, repository := openMySitesCleanupPostgres(t)
		prepareCleanupState(t, repository, []UpstreamGroupRef{{SiteID: "site-missing", GroupName: "group-missing"}})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-missing", UpstreamGroupName: "group-missing",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusMissing, PricingMappingEnabled: true,
		})
		cleaner := &cleanupRuntimeRecorder{err: fmt.Errorf("injected runtime cleanup failure")}
		service := NewService(repository, nil, nil)
		service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
		service.SetConnectionRuntimeCleaner(cleaner)
		if err := service.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema: %v", err)
		}
		removePricing := true

		if err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
			ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
		}); err == nil {
			t.Fatal("expected runtime cleanup failure")
		}

		assertCleanupConnectionCount(t, pool, "missing", 1)
		state, err := repository.Get(context.Background(), "user-1", "workspace-1")
		if err != nil || state == nil || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 {
			t.Fatalf("cleanup failure changed mapping state=%#v err=%v", state, err)
		}
	})

	t.Run("removePricingMapping false keeps mapping while deleting connection", func(t *testing.T) {
		pool, repository := openMySitesCleanupPostgres(t)
		prepareCleanupState(t, repository, []UpstreamGroupRef{{SiteID: "site-missing", GroupName: "group-missing"}})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-missing", UpstreamGroupName: "group-missing",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusMissing, PricingMappingEnabled: true,
		})
		cleaner := &cleanupRuntimeRecorder{}
		service := NewService(repository, nil, nil)
		service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
		service.SetConnectionRuntimeCleaner(cleaner)
		if err := service.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema: %v", err)
		}
		removePricing := false

		if err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
			ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
		}); err != nil {
			t.Fatalf("RealDisconnect: %v", err)
		}

		assertCleanupConnectionCount(t, pool, "missing", 0)
		state, err := repository.Get(context.Background(), "user-1", "workspace-1")
		if err != nil || state == nil || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 || state.Mappings[0].UpstreamTargets[0].SiteID != "site-missing" {
			t.Fatalf("removePricingMapping=false changed mapping state=%#v err=%v", state, err)
		}
		if cleaner.calls != 1 {
			t.Fatalf("unexpected runtime cleanup calls=%d", cleaner.calls)
		}
	})

	t.Run("connection delete failure rolls back mapping update", func(t *testing.T) {
		pool, repository := openMySitesCleanupPostgres(t)
		prepareCleanupState(t, repository, []UpstreamGroupRef{{SiteID: "site-missing", GroupName: "group-missing"}})
		insertCleanupConnection(t, repository, RealConnection{
			ID: "missing", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1",
			UpstreamSiteID: "site-missing", UpstreamGroupName: "group-missing",
			OwnGroupNames: []string{"own-a"}, Status: ConnectionStatusMissing, PricingMappingEnabled: true,
		})
		if _, err := pool.Exec(context.Background(), `
			CREATE FUNCTION reject_missing_connection_delete() RETURNS trigger
			LANGUAGE plpgsql AS $$
			BEGIN
				IF OLD.id = 'missing' THEN
					RAISE EXCEPTION 'injected connection delete failure';
				END IF;
				RETURN OLD;
			END;
			$$
		`); err != nil {
			t.Fatalf("create delete rejection function: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `
			CREATE TRIGGER reject_missing_connection_delete
			BEFORE DELETE ON real_connections
			FOR EACH ROW EXECUTE FUNCTION reject_missing_connection_delete()
		`); err != nil {
			t.Fatalf("create delete rejection trigger: %v", err)
		}

		cleaner := &cleanupRuntimeRecorder{}
		service := NewService(repository, nil, nil)
		service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
		service.SetConnectionRuntimeCleaner(cleaner)
		if err := service.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema: %v", err)
		}
		removePricing := true

		if err := service.RealDisconnect(context.Background(), "user-1", RealDisconnectRequest{
			ConnectionID: "missing", Mode: "unlink", RemovePricingMapping: &removePricing,
		}); err == nil {
			t.Fatal("expected injected connection delete failure")
		}

		assertCleanupConnectionCount(t, pool, "missing", 1)
		state, err := repository.Get(context.Background(), "user-1", "workspace-1")
		if err != nil || state == nil || len(state.Mappings) != 1 || len(state.Mappings[0].UpstreamTargets) != 1 || state.Mappings[0].UpstreamTargets[0].SiteID != "site-missing" {
			t.Fatalf("delete failure did not roll back mapping state=%#v err=%v", state, err)
		}
		if cleaner.calls != 1 {
			t.Fatalf("unexpected runtime cleanup calls=%d", cleaner.calls)
		}
	})
}

func openMySitesCleanupPostgres(t *testing.T) (*pgxpool.Pool, *Repository) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("my_sites_cleanup_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse PostgreSQL config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("connect test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dropCancel()
		if _, err := adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		adminPool.Close()
	})
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return pool, repository
}

func prepareCleanupState(t *testing.T, repository *Repository, targets []UpstreamGroupRef) {
	t.Helper()
	if err := repository.Save(context.Background(), State{
		UserID: "user-1", AdminAccountID: "workspace-1",
		Mappings: []GroupMapping{{OwnGroup: "own-a", UpstreamTargets: targets}},
	}); err != nil {
		t.Fatalf("save cleanup state: %v", err)
	}
}

func insertCleanupConnection(t *testing.T, repository *Repository, connection RealConnection) {
	t.Helper()
	connection.UpstreamGroupID = connection.UpstreamGroupName
	connection.UpstreamKeyID = connection.ID + "-key"
	connection.AdminAccountID = connection.ID + "-account"
	connection.OwnGroupIDs = []string{"own-a-id"}
	connection.GroupType = "openai"
	connection.ProvisioningMode = ProvisioningModeManaged
	connection.UpstreamPlatform = string(upstream.PlatformSub2API)
	connection.AdminPlatform = string(upstream.PlatformSub2API)
	connection.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := repository.SaveRealConnection(context.Background(), connection); err != nil {
		t.Fatalf("save cleanup connection: %v", err)
	}
}

func assertCleanupConnectionCount(t *testing.T, pool *pgxpool.Pool, connectionID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM real_connections WHERE id=$1`, connectionID).Scan(&count); err != nil {
		t.Fatalf("count connection %s: %v", connectionID, err)
	}
	if count != want {
		t.Fatalf("connection %s count=%d want=%d", connectionID, count, want)
	}
}

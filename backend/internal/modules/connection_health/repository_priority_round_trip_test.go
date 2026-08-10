package connection_health

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const priorityRepositoryPostgresTimeout = 15 * time.Second

func TestPriorityWorkspaceSyncStateRepositoryRoundTrip(t *testing.T) {
	pool := openPriorityRepositoryPostgresTestPool(t)
	repository := NewRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), priorityRepositoryPostgresTimeout)
	defer cancel()
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure connection-health schema: %v", err)
	}

	state := PriorityWorkspaceSyncState{
		UserID:                    "round-trip-user",
		AdminAccountID:            "round-trip-workspace",
		AppliedSignature:          "applied",
		PendingSignature:          "pending",
		LastDecision:              "pending",
		LastActionSource:          priorityActionWriteback,
		InventoryStatus:           "unknown",
		WritebackSpreadSeconds:    5,
		PendingTargetCount:        4,
		LastWriteRoundTargetCount: 37,
		EvaluationCount:           2,
		WriteAttemptCount:         1,
		WriteSuccessCount:         1,
		WriteFailureCount:         0,
	}
	if err := repository.UpsertPriorityWorkspaceSyncState(ctx, state); err != nil {
		t.Fatalf("insert priority workspace state: %v", err)
	}
	got, err := repository.GetPriorityWorkspaceSyncState(ctx, state.UserID, state.AdminAccountID)
	if err != nil {
		t.Fatalf("select priority workspace state: %v", err)
	}
	if got == nil || got.LastWriteRoundTargetCount != 37 || got.PendingTargetCount != 4 || got.WritebackSpreadSeconds != 5 {
		t.Fatalf("round-trip state=%+v, want round=37 pending=4 spread=5", got)
	}

	state.LastWriteRoundTargetCount = 11
	state.PendingTargetCount = 1
	if err := repository.UpsertPriorityWorkspaceSyncState(ctx, state); err != nil {
		t.Fatalf("upsert priority workspace state: %v", err)
	}
	got, err = repository.GetPriorityWorkspaceSyncState(ctx, state.UserID, state.AdminAccountID)
	if err != nil {
		t.Fatalf("select updated priority workspace state: %v", err)
	}
	if got == nil || got.LastWriteRoundTargetCount != 11 || got.PendingTargetCount != 1 {
		t.Fatalf("updated round-trip state=%+v, want round=11 pending=1", got)
	}
}

func openPriorityRepositoryPostgresTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL repository tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), priorityRepositoryPostgresTimeout)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	schema := fmt.Sprintf("connection_health_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create test schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse PostgreSQL config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		adminPool.Close()
		t.Fatalf("connect test schema: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		adminPool.Close()
		t.Fatalf("ping test schema: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), priorityRepositoryPostgresTimeout)
		defer dropCancel()
		if _, err := adminPool.Exec(dropCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}

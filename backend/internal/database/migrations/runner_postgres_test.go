package migrations

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

func TestRunInitializesFreshDatabaseAndPreservesStateOnRestart(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := context.Background()
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("migrate fresh database: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_priority_sync_states (
			user_id, admin_account_id, target_id, original_priority, last_applied_priority,
			pending_mutation_generation, pending_source, pending_epoch, pending_action_key
		) VALUES ('user-1', 'workspace-1', 'target-1', 17, 23, 31, 'probe', 41, 'action-1')
	`); err != nil {
		t.Fatalf("save priority state after fresh migrations: %v", err)
	}
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}

	assertPriorityMigrationState(t, pool, 17, 23, 31, "probe", 41, "action-1")
	entries, err := migrationFiles.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	wantVersions := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			wantVersions++
		}
	}
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != wantVersions {
		t.Fatalf("applied versions=%d, want %d", versions, wantVersions)
	}
}

func TestSafetyGateMigrationPreservesExistingPriorityState(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE connection_health_priority_sync_states (
			user_id text NOT NULL,
			admin_account_id text NOT NULL DEFAULT '',
			target_id text NOT NULL,
			original_priority integer NOT NULL DEFAULT 0,
			last_applied_priority integer NOT NULL DEFAULT 0,
			effective_multiplier double precision NOT NULL DEFAULT 0,
			conflict boolean NOT NULL DEFAULT false,
			last_conflict_priority integer NULL,
			updated_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, admin_account_id, target_id)
		);
		CREATE TABLE connection_health_target_action_states (
			user_id text NOT NULL,
			admin_account_id text NOT NULL DEFAULT '',
			target_id text NOT NULL,
			PRIMARY KEY (user_id, admin_account_id, target_id)
		);
		INSERT INTO connection_health_priority_sync_states (
			user_id, admin_account_id, target_id, original_priority, last_applied_priority
		) VALUES ('user-1', 'workspace-1', 'target-1', 17, 23);
	`); err != nil {
		t.Fatalf("prepare existing priority state: %v", err)
	}
	sql, err := migrationFiles.ReadFile("000021_connection_health_safety_gate.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("migrate existing priority state: %v", err)
	}
	assertPriorityMigrationState(t, pool, 17, 23, 0, "", 0, "")
}

func assertPriorityMigrationState(t *testing.T, pool *pgxpool.Pool, wantOriginal, wantApplied int, wantGeneration int64, wantSource string, wantEpoch int64, wantAction string) {
	t.Helper()
	var original, applied int
	var generation, epoch int64
	var source, action string
	if err := pool.QueryRow(context.Background(), `
		SELECT original_priority, last_applied_priority, pending_mutation_generation,
			pending_source, pending_epoch, pending_action_key
		FROM connection_health_priority_sync_states
		WHERE user_id = 'user-1' AND admin_account_id = 'workspace-1' AND target_id = 'target-1'
	`).Scan(&original, &applied, &generation, &source, &epoch, &action); err != nil {
		t.Fatalf("read migrated priority state: %v", err)
	}
	if original != wantOriginal || applied != wantApplied || generation != wantGeneration || source != wantSource || epoch != wantEpoch || action != wantAction {
		t.Fatalf("migrated state=(%d,%d,%d,%q,%d,%q), want (%d,%d,%d,%q,%d,%q)", original, applied, generation, source, epoch, action, wantOriginal, wantApplied, wantGeneration, wantSource, wantEpoch, wantAction)
	}
}

func openMigrationPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL migration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("cannot parse TEST_DATABASE_URL")
	}
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open PostgreSQL migration test pool")
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated migration test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("remove migration test schema: %v", err)
		}
	})
	config = config.Copy()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("cannot open isolated migration test pool")
	}
	t.Cleanup(pool.Close)
	return pool
}

package connection_health

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestAccountTierRepositoryMigrationIsIdempotentAndPreservesLegacyData(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	// Reproduce the deployed pre-tier table, including historical values and timestamps.
	_, err := pool.Exec(ctx, `CREATE TABLE connection_health_account_configs (
		user_id text NOT NULL, admin_account_id text NOT NULL DEFAULT '', target_id text NOT NULL,
		intelligence_weight integer NULL CHECK (intelligence_weight IS NULL OR intelligence_weight BETWEEN 0 AND 100),
		created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (user_id, admin_account_id, target_id));
		INSERT INTO connection_health_account_configs (user_id, admin_account_id, target_id, intelligence_weight)
		VALUES ('user1','ws1','sub2api:ws1:shared',87), ('user1','ws1','sub2api:ws1:null',NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	legacy := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(target_id,intelligence_weight,created_at) ORDER BY target_id)::text FROM connection_health_account_configs WHERE user_id='user1' AND admin_account_id='ws1'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := legacy()
	repository := NewRepository(pool)
	for run := 0; run < 2; run++ {
		if err := repository.EnsureSchema(ctx); err != nil {
			t.Fatalf("schema pass %d: %v", run, err)
		}
	}
	var defaultTier int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(account_tier,2) FROM connection_health_account_configs WHERE target_id='sub2api:ws1:shared'`).Scan(&defaultTier); err != nil {
		t.Fatal(err)
	}
	if defaultTier != 2 {
		t.Fatalf("legacy account tier=%d want 2", defaultTier)
	}
	service, _, _ := accountTierFixture()
	service.repo = repository
	workspaceMigration, err := os.ReadFile("../../database/migrations/000019_connection_health_priority_sync_b.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(workspaceMigration)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_priority_workspace_sync_states (user_id, admin_account_id, pending_signature, last_decision, last_error) VALUES ('user1','ws1','protected-pending','blocked','protected-safety-state')`); err != nil {
		t.Fatal(err)
	}
	for _, state := range []PrioritySyncState{
		{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:shared", Conflict: true, OriginalPriority: 7, LastAppliedPriority: 4, LastConflictPriority: intPointer(7)},
		{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:multiplier-only", OriginalPriority: 50, LastAppliedPriority: 4, EffectiveMultiplier: 0.5, PendingPriority: intPointer(3)},
	} {
		if state.PendingPriority != nil {
			// Seed a historical pending checkpoint directly; normal writes must not
			// create an unclaimed Sub2API action under the current safety contract.
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_priority_sync_states
				(user_id, admin_account_id, target_id, original_priority, last_applied_priority, pending_priority, effective_multiplier)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, state.UserID, state.AdminAccountID, state.TargetID,
				state.OriginalPriority, state.LastAppliedPriority, state.PendingPriority, state.EffectiveMultiplier); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := repository.UpsertPrioritySyncState(ctx, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.UpsertTargetActionState(ctx, TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:shared", OriginalStatus: "active", LastAppliedStatus: "disabled", Conflict: true}); err != nil {
		t.Fatal(err)
	}
	protected := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
			(SELECT jsonb_agg(to_jsonb(p) ORDER BY target_id) FROM connection_health_priority_sync_states p),
			(SELECT jsonb_agg(to_jsonb(a) ORDER BY target_id) FROM connection_health_target_action_states a),
			(SELECT jsonb_agg(to_jsonb(w)) FROM connection_health_priority_workspace_sync_states w))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	protectedBefore := protected()
	for _, tier := range []int{1, 2, 1} {
		requireTierResponse(t, tierRequest(service, "PUT", "user1", "sub2api:ws1:shared", fmt.Sprintf(`{"accountTier":%d}`, tier)), "sub2api:ws1:shared", tier)
		if err := NewRepository(pool).EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
		// A new service/repository represents a process reload, not an in-memory echo.
		reloaded, _, _ := accountTierFixture()
		reloaded.repo = NewRepository(pool)
		requireTierResponse(t, tierRequest(reloaded, "GET", "user1", "sub2api:ws1:shared", ""), "sub2api:ws1:shared", tier)
		if got := legacy(); got != before {
			t.Fatalf("legacy data changed\nbefore=%s\nafter=%s", before, got)
		}
		if got := protected(); got != protectedBefore {
			t.Fatalf("persisted checkpoint/conflict/action/pending changed\nbefore=%s\nafter=%s", protectedBefore, got)
		}
	}
	for _, invalid := range []int{-1, 0, 3, 100} {
		if _, err := pool.Exec(ctx, `UPDATE connection_health_account_configs SET account_tier=$1 WHERE target_id='sub2api:ws1:shared'`, invalid); err == nil {
			t.Fatalf("database accepted invalid tier %d", invalid)
		}
	}
	requireTierResponse(t, tierRequest(service, "GET", "user1", "sub2api:ws1:shared", ""), "sub2api:ws1:shared", 1)
}

func TestAccountTierRepositoryHTTPPersistenceAndIsolation(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	svc, _, actions := accountTierFixture()
	svc.repo = repo
	for _, tc := range []struct {
		user, ws, target string
		tier             int
	}{
		{"user1", "ws1", "sub2api:ws1:shared", 1}, {"user2", "ws1", "sub2api:ws1:shared", 2}, {"user1", "ws2", "sub2api:ws2:shared", 2},
	} {
		svc.accounts = fakeAdminAccountResolver{id: tc.ws}
		requireTierResponse(t, tierRequest(svc, "GET", tc.user, tc.target, ""), tc.target, 2)
		requireTierResponse(t, tierRequest(svc, "PUT", tc.user, tc.target, fmt.Sprintf(`{"accountTier":%d}`, tc.tier)), tc.target, tc.tier)
	}
	svc.accounts = fakeAdminAccountResolver{id: "ws1"}
	for _, body := range []string{`{"accountTier":3}`, `{"accountTier":null}`, `{}`} {
		if response := tierRequest(svc, "PUT", "user1", "sub2api:ws1:shared", body); response.Code != 400 {
			t.Fatalf("invalid status %d", response.Code)
		}
	}
	if response := tierRequest(svc, "PUT", "user1", "sub2api:ws2:shared", `{"accountTier":1}`); response.Code != 400 {
		t.Fatalf("foreign write status %d", response.Code)
	}
	requireTierResponse(t, tierRequest(svc, "GET", "user1", "sub2api:ws1:shared", ""), "sub2api:ws1:shared", 1)
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d want 3", rows)
	}
	if len(actions.calls) != 0 {
		t.Fatal("persistent save called Priority writer")
	}
}

package connection_health

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Database assertions own the business invariant, rather than inspecting a
// particular SQL statement or trusting the in-memory repository substitute.
func taskBEnsurePriorityWorkspaceTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	for _, name := range []string{"000019_connection_health_priority_sync_b.sql", "000020_connection_health_frequency_split_c.sql", "000023_connection_health_priority_writeback_spread_e.sql"} {
		sql, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../database/migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
	}
}

func taskBWorkspaceSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var result string
	if err := pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
	(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY target_id),'[]') FROM connection_health_account_configs c),
	(SELECT coalesce(jsonb_agg(to_jsonb(s)),'[]') FROM connection_health_workspace_settings s),
	(SELECT coalesce(jsonb_agg(to_jsonb(p)),'[]') FROM connection_health_priority_workspace_sync_states p))::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPriorityTaskBPostgresTierSaveAtomicGenerationAndRollback(t *testing.T) {
	pool := stageAPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	taskBEnsurePriorityWorkspaceTable(t, pool)
	settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SaveAccountTier(ctx, "u", "w", "sub2api:w:1", 1); err != nil {
		t.Fatal(err)
	}
	next, err := NewRepository(pool).GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if next.ConfigGeneration != settings.ConfigGeneration+1 {
		t.Errorf("changed tier did not invalidate old decisions: before=%d after=%d", settings.ConfigGeneration, next.ConfigGeneration)
	}
	sync, err := r.GetPriorityWorkspaceSyncState(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if sync == nil || sync.PendingSignature == "" {
		t.Error("changed tier did not durably request Priority synchronization")
	}
	if err := r.SaveAccountTier(ctx, "u", "w", "sub2api:w:1", 1); err != nil {
		t.Fatal(err)
	}
	// A same-value write can keep its normal account updated_at behavior; the
	// generation and durable request identity must stay exactly unchanged.
	same, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.GetPriorityWorkspaceSyncState(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if same.ConfigGeneration != next.ConfigGeneration || (sync != nil && (again == nil || again.PendingSignature != sync.PendingSignature)) {
		t.Errorf("same tier created a new generation/request: %+v %+v", same, again)
	}
	if err := r.SaveAccountTier(ctx, "u", "w", "sub2api:w:1", 2); err != nil {
		t.Fatal(err)
	}
	changed, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConfigGeneration != same.ConfigGeneration+1 {
		t.Errorf("reverse tier did not invalidate generation: %d -> %d", same.ConfigGeneration, changed.ConfigGeneration)
	}
	before := taskBWorkspaceSnapshot(t, pool)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION task_b_reject_generation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'FAKE TEST DATA generation rejection'; END $$;
	CREATE TRIGGER task_b_reject_generation BEFORE UPDATE ON connection_health_workspace_settings FOR EACH ROW EXECUTE FUNCTION task_b_reject_generation();`); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveAccountTier(ctx, "u", "w", "sub2api:w:1", 1); err == nil {
		t.Error("tier save reported success after generation failed")
	}
	if after := taskBWorkspaceSnapshot(t, pool); after != before {
		t.Error("tier/generation/request did not roll back together")
	}
}

type taskBPostgresPolicyChangesAfterRead struct {
	*Repository
	pool    *pgxpool.Pool
	changed bool
}

func (r *taskBPostgresPolicyChangesAfterRead) ListPolicies(ctx context.Context, user, workspace string) ([]Policy, error) {
	p, err := r.Repository.ListPolicies(ctx, user, workspace)
	if err != nil || r.changed {
		return p, err
	}
	r.changed = true
	// An independent configuration transaction wins after the request captured
	// its old settings and policy. The old decision cannot borrow its generation.
	tx, err := r.Repository.beginWorkspaceTransaction(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE connection_health_policies SET auto_degrade_enabled=false WHERE user_id=$1 AND admin_account_id=$2`, user, workspace); err != nil {
		return nil, err
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, user, workspace); err != nil {
		return nil, err
	}
	return p, tx.Commit(ctx)
}

func TestPriorityTaskBPostgresPageCannotBorrowNewConfigurationGeneration(t *testing.T) {
	pool := stageAPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	taskBEnsurePriorityWorkspaceTable(t, pool)
	s, fake, f := taskBAccountAPIFixture(5, 1)
	p := fake.policies[0]
	if err := r.SavePolicyWithTargets(ctx, p, p.ModelTargets); err != nil {
		t.Fatal(err)
	}
	if err := r.ReplacePolicyAssignments(ctx, "user1", "ws1", "sub2api:ws1:a", []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveAccountTier(ctx, "user1", "ws1", "sub2api:ws1:a", 1); err != nil {
		t.Fatal(err)
	}
	s.repo = &taskBPostgresPolicyChangesAfterRead{Repository: r, pool: pool}
	rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`)
	if rec.Code != 409 {
		t.Fatalf("old eligibility borrowed a new generation: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.priorityWrites) != 0 || f.priority != 5 || f.credentials != 0 {
		t.Fatalf("stale page decision sent HTTP/probe: %+v", f)
	}
	rows, err := NewRepository(pool).ListPrioritySyncStates(ctx, "user1", "ws1")
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale page decision claimed a checkpoint: %v %v", rows, err)
	}
}

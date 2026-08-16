package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEventRetentionRepositoryPostgresContract(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), questionAnswerPostgresTimeout)
	defer cancel()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := repository.EnsureEventRetentionIndex(ctx); err != nil {
		t.Fatalf("EnsureEventRetentionIndex first run: %v", err)
	}
	if err := repository.EnsureEventRetentionIndex(ctx); err != nil {
		t.Fatalf("EnsureEventRetentionIndex second run: %v", err)
	}
	var indexValid bool
	if err := pool.QueryRow(ctx, `
		SELECT index.indisvalid
		FROM pg_class relation
		JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		JOIN pg_index index ON index.indexrelid = relation.oid
		WHERE namespace.nspname = current_schema() AND relation.relname = $1
	`, eventRetentionIndexName).Scan(&indexValid); err != nil || !indexValid {
		t.Fatalf("retention index valid=%v err=%v", indexValid, err)
	}

	cutoff := time.Now().UTC().Truncate(time.Second)
	insertRetentionEvent(t, ctx, repository, "old-only", "target-old", string(ResultServerError), "", cutoff.Add(-26*time.Hour))
	insertRetentionEvent(t, ctx, repository, "old-shadowed", "target-recent", string(ResultAuth), "", cutoff.Add(-25*time.Hour))
	insertRetentionEvent(t, ctx, repository, "recent-failure", "target-recent", string(ResultServerError), "", cutoff.Add(time.Minute))
	insertRetentionEvent(t, ctx, repository, "old-action", "target-action-old", SchedulableActionFailed, ActionSourceUser, cutoff.Add(-25*time.Hour))
	insertRetentionEvent(t, ctx, repository, "recent-action", "target-action", SchedulableActionFailed, ActionSourceUser, cutoff.Add(time.Minute))
	insertRetentionEvent(t, ctx, repository, "recent-action-success", "target-action", SchedulableActionSucceeded, ActionSourceUser, cutoff.Add(2*time.Minute))
	insertRetentionEvent(t, ctx, repository, "boundary", "target-boundary", string(ResultServerError), "", cutoff)

	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_states (connection_id, user_id, upstream_site_id, upstream_group_name, state)
		VALUES ('state-target', 'user-1', 'site-1', 'group-1', 'healthy');
		INSERT INTO connection_health_policies (id, user_id, name) VALUES ('policy-1', 'user-1', 'policy');
		INSERT INTO connection_health_target_action_states (user_id, target_id) VALUES ('user-1', 'action-target');
		INSERT INTO connection_health_probe_budget_usage (user_id, policy_id, day_start, used)
		VALUES ('user-1', 'policy-1', date_trunc('day', now()), 3);
	`); err != nil {
		t.Fatalf("insert invariant rows: %v", err)
	}

	failures, err := repository.ListLatestProbeFailureEventsByWorkspace(ctx, "user-1", "", cutoff)
	if err != nil {
		t.Fatalf("ListLatestProbeFailureEventsByWorkspace: %v", err)
	}
	assertEventIDs(t, failures, "recent-failure", "boundary")
	actions, err := repository.ListLatestSchedulableActionEventsByWorkspace(ctx, "user-1", "", cutoff)
	if err != nil {
		t.Fatalf("ListLatestSchedulableActionEventsByWorkspace: %v", err)
	}
	assertEventIDs(t, actions, "recent-action-success")
	successfulActions, err := repository.ListLatestSuccessfulSchedulableActionEventsByWorkspace(ctx, "user-1", "", cutoff)
	if err != nil {
		t.Fatalf("ListLatestSuccessfulSchedulableActionEventsByWorkspace: %v", err)
	}
	assertEventIDs(t, successfulActions, "recent-action-success")
	connectionEvents, err := repository.ListEventsByConnection(ctx, "target-recent", "user-1", "", 100)
	if err != nil {
		t.Fatalf("ListEventsByConnection: %v", err)
	}
	assertEventIDs(t, connectionEvents, "recent-failure")
	recentWorkspaceEvents, err := repository.ListRecentEventsByWorkspace(ctx, "user-1", "", 100)
	if err != nil {
		t.Fatalf("ListRecentEventsByWorkspace: %v", err)
	}
	assertEventIDs(t, recentWorkspaceEvents, "recent-failure", "recent-action", "recent-action-success", "boundary")

	if deleted, err := repository.DeleteEventsBefore(ctx, cutoff, 0); err == nil || deleted != 0 {
		t.Fatalf("invalid zero batch deleted=%d err=%v", deleted, err)
	}
	if deleted, err := repository.DeleteEventsBefore(ctx, cutoff, maxEventDeleteBatchSize+1); err == nil || deleted != 0 {
		t.Fatalf("oversized batch deleted=%d err=%v", deleted, err)
	}
	cancelledCtx, cancelDelete := context.WithCancel(ctx)
	cancelDelete()
	if deleted, err := repository.DeleteEventsBefore(cancelledCtx, cutoff, 100); err == nil || deleted != 0 {
		t.Fatalf("cancelled delete deleted=%d err=%v", deleted, err)
	}

	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	defer lockTx.Rollback(context.Background())
	var lockedID string
	if err := lockTx.QueryRow(ctx, `SELECT id FROM connection_health_events WHERE id = 'old-only' FOR UPDATE`).Scan(&lockedID); err != nil {
		t.Fatalf("lock old event: %v", err)
	}
	deleted, err := repository.DeleteEventsBefore(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("delete unlocked expired events: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted unlocked=%d want 2", deleted)
	}
	if countEvent(t, ctx, pool, "old-only") != 1 {
		t.Fatal("locked expired event must remain")
	}
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback lock transaction: %v", err)
	}
	deleted, err = repository.DeleteEventsBefore(ctx, cutoff, 100)
	if err != nil || deleted != 1 {
		t.Fatalf("delete formerly locked event deleted=%d err=%v", deleted, err)
	}
	deleted, err = repository.DeleteEventsBefore(ctx, cutoff, 100)
	if err != nil || deleted != 0 {
		t.Fatalf("idempotent delete deleted=%d err=%v", deleted, err)
	}

	assertEventIDsFromDatabase(t, ctx, pool, "boundary", "recent-action", "recent-action-success", "recent-failure")
	for table, want := range map[string]int{
		"connection_health_states":               1,
		"connection_health_policies":             1,
		"connection_health_target_action_states": 1,
		"connection_health_probe_budget_usage":   1,
	} {
		var got int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("count %s=%d want %d", table, got, want)
		}
	}
}

func insertRetentionEvent(t *testing.T, ctx context.Context, repository *Repository, id string, connectionID string, result string, actionSource string, createdAt time.Time) {
	t.Helper()
	if _, err := repository.db.Exec(ctx, `
		INSERT INTO connection_health_events (
			id, connection_id, model_name, user_id, admin_account_id, result, action_source, created_at
		) VALUES ($1, $2, '*', 'user-1', '', $3, $4, $5)
	`, id, connectionID, result, actionSource, createdAt); err != nil {
		t.Fatalf("insert event %s: %v", id, err)
	}
}

func assertEventIDs(t *testing.T, events []ConnectionHealthEvent, want ...string) {
	t.Helper()
	got := make(map[string]struct{}, len(events))
	for _, event := range events {
		got[event.ID] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("event ids=%v want=%v", got, want)
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("event ids=%v missing=%s", got, id)
		}
	}
}

func assertEventIDsFromDatabase(t *testing.T, ctx context.Context, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, want ...string) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT id FROM connection_health_events ORDER BY id`)
	if err != nil {
		t.Fatalf("list remaining events: %v", err)
	}
	defer rows.Close()
	events := make([]ConnectionHealthEvent, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan remaining event: %v", err)
		}
		events = append(events, ConnectionHealthEvent{ID: id})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("remaining event rows: %v", err)
	}
	assertEventIDs(t, events, want...)
}

func countEvent(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events WHERE id = $1`, id).Scan(&count); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("count event %s: %v", id, err)
	}
	return count
}

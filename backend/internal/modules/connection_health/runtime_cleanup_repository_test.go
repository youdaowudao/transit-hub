package connection_health

import (
	"context"
	"testing"
	"time"
)

func TestDeleteRuntimeByConnectionIsScopedAndRetainsHistory(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO connection_health_states (
			connection_id, model_name, user_id, admin_account_id, upstream_site_id, upstream_group_name, state
		) VALUES
			('connection-1', 'model-a', 'user-1', 'workspace-1', 'site-1', 'group-1', 'healthy'),
			('connection-1', 'model-b', 'user-1', 'workspace-2', 'site-1', 'group-1', 'healthy');
		INSERT INTO connection_health_events (
			id, connection_id, user_id, admin_account_id, result
		) VALUES ('event-1', 'connection-1', 'user-1', 'workspace-1', 'success');
	`); err != nil {
		t.Fatalf("insert runtime fixture: %v", err)
	}

	if err := repository.DeleteRuntimeByConnection(context.Background(), "user-1", "workspace-1", "connection-1"); err != nil {
		t.Fatalf("DeleteRuntimeByConnection: %v", err)
	}
	var currentWorkspaceStates, otherWorkspaceStates, historyEvents int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_states WHERE user_id='user-1' AND admin_account_id='workspace-1' AND connection_id='connection-1'`).Scan(&currentWorkspaceStates); err != nil {
		t.Fatalf("count current workspace states: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_states WHERE user_id='user-1' AND admin_account_id='workspace-2' AND connection_id='connection-1'`).Scan(&otherWorkspaceStates); err != nil {
		t.Fatalf("count other workspace states: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_events WHERE id='event-1'`).Scan(&historyEvents); err != nil {
		t.Fatalf("count history events: %v", err)
	}
	if currentWorkspaceStates != 0 || otherWorkspaceStates != 1 || historyEvents != 1 {
		t.Fatalf("unexpected cleanup scope current=%d other=%d history=%d", currentWorkspaceStates, otherWorkspaceStates, historyEvents)
	}
}

func TestCountFailureEventsSinceExcludesMissingConnectionHistory(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO connection_health_events (
			id, connection_id, user_id, admin_account_id, result, created_at
		) VALUES
			('active-failure', 'active', 'user-1', 'workspace-1', 'server_error', $1),
			('missing-failure', 'missing', 'user-1', 'workspace-1', 'auth', $1),
			('other-workspace-failure', 'missing', 'user-1', 'workspace-2', 'auth', $1),
			('old-failure', 'active', 'user-1', 'workspace-1', 'auth', $2)
	`, now, now.Add(-25*time.Hour)); err != nil {
		t.Fatalf("insert event fixtures: %v", err)
	}

	count, err := repository.CountFailureEventsSince(context.Background(), "user-1", "workspace-1", now.Add(-24*time.Hour), []string{"active"})
	if err != nil {
		t.Fatalf("CountFailureEventsSince: %v", err)
	}
	if count != 1 {
		t.Fatalf("failure count=%d want=1", count)
	}
	var retained int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_events WHERE id='missing-failure'`).Scan(&retained); err != nil {
		t.Fatalf("count retained missing history: %v", err)
	}
	if retained != 1 {
		t.Fatalf("missing history retained=%d want=1", retained)
	}
}

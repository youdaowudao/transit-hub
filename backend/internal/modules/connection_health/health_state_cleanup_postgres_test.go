package connection_health

import (
	"context"
	"testing"
)

func TestAbandonConflictedTargetActionPostgresEventRollback(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	scope := RemoteActionScope{"u", "w", "sub2api:w:atomic"}
	checkpoint := TargetActionState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
	if err := r.UpsertTargetActionState(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, scope.UserID, scope.AdminAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_abandon_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture event rejection'; END $$;
CREATE TRIGGER reject_abandon_event BEFORE INSERT ON connection_health_events FOR EACH ROW EXECUTE FUNCTION reject_abandon_event()`); err != nil {
		t.Fatal(err)
	}
	event := ConnectionHealthEvent{ID: "abandon-atomic", ModelName: "*", Source: EventSourceScheduled}
	if changed, err := r.AbandonConflictedTargetAction(ctx, scope, settings.ConfigGeneration, event); err == nil || changed {
		t.Fatal("event rejection must roll back the preceding checkpoint deletion")
	}
	if stored, err := r.GetTargetActionState(ctx, scope.UserID, scope.AdminAccountID, scope.TargetID); err != nil || stored == nil || !stored.Conflict {
		t.Fatalf("failed event left a partial deletion: checkpoint=%v err=%v", stored, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed transaction left an audit row: count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_abandon_event ON connection_health_events`); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.AbandonConflictedTargetAction(ctx, scope, settings.ConfigGeneration, event); err != nil || !changed {
		t.Fatalf("retry did not commit both deletion and event: %t %v", changed, err)
	}
	if stored, err := r.GetTargetActionState(ctx, scope.UserID, scope.AdminAccountID, scope.TargetID); err != nil || stored != nil {
		t.Fatalf("committed abandonment retained its checkpoint: %v %v", stored, err)
	}
	events, err := r.ListEventsByConnection(ctx, scope.TargetID, scope.UserID, scope.AdminAccountID, 100)
	if err != nil || len(events) != 1 || events[0].Result != "policy_unmanaged_abandoned" || events[0].Source != EventSourceScheduled || events[0].RemoteAction != "" {
		t.Fatalf("atomic event scope/result = %+v err=%v", events, err)
	}
	event.ID = "abandon-duplicate-attempt"
	if changed, err := r.AbandonConflictedTargetAction(ctx, scope, settings.ConfigGeneration, event); err != nil || changed {
		t.Fatalf("repeated cleanup created another event: %t %v", changed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("abandonment event repeated: %d %v", count, err)
	}
}

func TestAbandonConflictedTargetActionPostgresGenerationAndPending(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"generation", "target-status", "target-phase", "priority-value", "priority-phase", "not-conflicted"} {
		t.Run(kind, func(t *testing.T) {
			scope := RemoteActionScope{"u", "w", "sub2api:w:" + kind}
			checkpoint := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
			if err := r.UpsertTargetActionState(ctx, checkpoint); err != nil {
				t.Fatal(err)
			}
			settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
			if err != nil {
				t.Fatal(err)
			}
			expected := settings.ConfigGeneration
			switch kind {
			case "generation":
				expected--
			case "target-status":
				_, err = pool.Exec(ctx, `UPDATE connection_health_target_action_states SET pending_status='active' WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, scope.TargetID)
			case "target-phase":
				_, err = pool.Exec(ctx, `UPDATE connection_health_target_action_states SET pending_dispatch_phase='sending',pending_dispatch_id='fixture',pending_owner_id='fixture' WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, scope.TargetID)
			case "priority-value":
				_, err = pool.Exec(ctx, `INSERT INTO connection_health_priority_sync_states(user_id,admin_account_id,target_id,pending_priority) VALUES('u','w',$1,30)`, scope.TargetID)
			case "priority-phase":
				_, err = pool.Exec(ctx, `INSERT INTO connection_health_priority_sync_states(user_id,admin_account_id,target_id,pending_dispatch_phase,pending_dispatch_id,pending_owner_id) VALUES('u','w',$1,'sending','fixture','fixture')`, scope.TargetID)
			case "not-conflicted":
				_, err = pool.Exec(ctx, `UPDATE connection_health_target_action_states SET conflict=false WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, scope.TargetID)
			}
			if err != nil {
				t.Fatal(err)
			}
			event := ConnectionHealthEvent{ID: "abandon-" + kind, ModelName: "*", Source: EventSourceScheduled}
			if changed, err := r.AbandonConflictedTargetAction(ctx, scope, expected, event); err != nil || changed {
				t.Fatalf("%s guard did not retain checkpoint: changed=%t err=%v", kind, changed, err)
			}
			if stored, err := r.GetTargetActionState(ctx, "u", "w", scope.TargetID); err != nil || stored == nil {
				t.Fatalf("%s guard lost checkpoint: %v %v", kind, stored, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE connection_health_target_action_states SET conflict=true,pending_status='',pending_dispatch_phase='',pending_dispatch_id='',pending_owner_id='' WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, scope.TargetID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM connection_health_priority_sync_states WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, scope.TargetID); err != nil {
				t.Fatal(err)
			}
			if changed, err := r.AbandonConflictedTargetAction(ctx, scope, settings.ConfigGeneration, event); err != nil || !changed {
				t.Fatalf("resolved %s guard did not allow next stable cleanup: %t %v", kind, changed, err)
			}
		})
	}
}

func TestDeleteUnmonitoredStatesPostgresCheckpointsAndScope(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, state := range []ConnectionHealthState{
		{ConnectionID: "sub2api:w:a", UserID: "u", AdminAccountID: "w", ModelName: "m1", State: StateSuspended},
		{ConnectionID: "sub2api:w:a", UserID: "u", AdminAccountID: "w", ModelName: "old-model", State: StateDegraded},
		{ConnectionID: "sub2api:w2:a", UserID: "u", AdminAccountID: "w2", ModelName: "m1", State: StateSuspended},
		{ConnectionID: "sub2api:w:other", UserID: "other", AdminAccountID: "w", ModelName: "m1", State: StateSuspended},
		{ConnectionID: "newapi:w:a", UserID: "u", AdminAccountID: "w", ModelName: "m1", State: StateSuspended},
	} {
		if err := r.UpsertState(ctx, state); err != nil {
			t.Fatal(err)
		}
	}
	workspaces, err := r.ListSub2APIHealthWorkspaces(ctx)
	if err != nil || len(workspaces) != 3 {
		t.Fatalf("health workspace discovery = %v %v", workspaces, err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := r.DeleteUnmonitoredStates(ctx, "u", "w", "sub2api:w:a", settings.ConfigGeneration+1); err != nil || deleted != 0 {
		t.Fatalf("changed generation deleted health: %d %v", deleted, err)
	}
	if err := r.UpsertTargetActionState(ctx, TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:a", Conflict: true}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := r.DeleteUnmonitoredStates(ctx, "u", "w", "sub2api:w:a", settings.ConfigGeneration); err != nil || deleted != 0 {
		t.Fatalf("target checkpoint did not protect health: %d %v", deleted, err)
	}
	if err := r.DeleteTargetActionState(ctx, "u", "w", "sub2api:w:a"); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertPrioritySyncState(ctx, PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:a", OriginalPriority: 20, LastAppliedPriority: 20}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := r.DeleteUnmonitoredStates(ctx, "u", "w", "sub2api:w:a", settings.ConfigGeneration); err != nil || deleted != 0 {
		t.Fatalf("Priority checkpoint did not protect health: %d %v", deleted, err)
	}
	if err := r.DeletePrioritySyncState(ctx, "u", "w", "sub2api:w:a"); err != nil {
		t.Fatal(err)
	}
	if err := r.InsertEvent(ctx, ConnectionHealthEvent{ID: "retained-history", UserID: "u", AdminAccountID: "w", ConnectionID: "sub2api:w:a", ModelName: "m1", Result: string(ResultServerError)}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ user, workspace, target string }{{"other", "w", "sub2api:w:a"}, {"u", "w2", "sub2api:w:a"}, {"u", "w", "newapi:w:a"}} {
		if deleted, err := r.DeleteUnmonitoredStates(ctx, invalid.user, invalid.workspace, invalid.target, settings.ConfigGeneration); err != nil || deleted != 0 {
			t.Fatalf("wrong scope/platform deleted health: %d %v", deleted, err)
		}
	}
	if deleted, err := r.DeleteUnmonitoredStates(ctx, "u", "w", "sub2api:w:a", settings.ConfigGeneration); err != nil || deleted != 2 {
		t.Fatalf("whole-account cleanup deleted %d rows, want two: %v", deleted, err)
	}
	for _, target := range []string{"sub2api:w2:a", "sub2api:w:other", "newapi:w:a"} {
		if states, err := r.ListStatesByConnection(ctx, target); err != nil || len(states) != 1 {
			t.Fatalf("cleanup changed another account/workspace/platform: target=%s rows=%d err=%v", target, len(states), err)
		}
	}
	if events, err := r.ListEventsByConnection(ctx, "sub2api:w:a", "u", "w", 100); err != nil || len(events) != 1 {
		t.Fatalf("health cleanup deleted event history: %d %v", len(events), err)
	}
}

func TestDeleteUnmonitoredStatesPostgresWaitsForGenerationTransaction(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertState(ctx, ConnectionHealthState{ConnectionID: "sub2api:w:a", UserID: "u", AdminAccountID: "w", ModelName: "m", State: StateSuspended}); err != nil {
		t.Fatal(err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	workerPool := protocolWorkerPool(t, pool, "health_cleanup_generation_worker")
	worker := NewRepository(workerPool)
	type result struct {
		deleted int64
		err     error
	}
	done := make(chan result, 1)
	go func() {
		deleted, err := worker.DeleteUnmonitoredStates(ctx, "u", "w", "sub2api:w:a", settings.ConfigGeneration)
		done <- result{deleted, err}
	}()
	protocolWaitForDatabaseLock(t, pool, "health_cleanup_generation_worker")
	if _, err := tx.Exec(ctx, `INSERT INTO connection_health_workspace_settings(user_id,admin_account_id,config_generation) VALUES('u','w',$1) ON CONFLICT(user_id,admin_account_id) DO UPDATE SET config_generation=EXCLUDED.config_generation`, settings.ConfigGeneration+1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || got.deleted != 0 {
		t.Fatalf("cleanup ignored generation committed during its lock wait: %+v", got)
	}
	if states, err := r.ListStatesByConnection(ctx, "sub2api:w:a"); err != nil || len(states) != 1 {
		t.Fatalf("new configuration lost old health before its scope could be checked: %d %v", len(states), err)
	}
}

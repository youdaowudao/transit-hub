package connection_health

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func taskBReadPriorityCheckpoint(ctx context.Context, r *Repository, target string) (*PrioritySyncState, error) {
	rows, err := r.ListPrioritySyncStates(ctx, "u", "w")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.TargetID == target {
			return &row, nil
		}
	}
	return nil, nil
}

// Explicitly enabled through the existing approved local connection helper.
// Every test creates and cleans its own FAKE TEST DATA schema. Reflection keeps
// the new lease contract runnable on the pre-B production types for RED.
func taskBPostgresClaim(t *testing.T, pool *pgxpool.Pool) RemoteActionClaim {
	t.Helper()
	claim, _ := protocolClaims()
	claim.MutationLeaseKey, claim.MutationOwnerID = "write-lease", "write-owner"
	taskASet(&claim, "WorkspaceLeaseKey", "workspace-lease")
	taskASet(&claim, "WorkspaceOwnerID", "workspace-owner")
	for _, row := range [][2]string{{claim.LeaseKey, claim.OwnerID}, {claim.MutationLeaseKey, claim.MutationOwnerID}, {"workspace-lease", "workspace-owner"}} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	return claim
}

func TestPriorityTaskBPostgresThreeLeaseClaimAndPermit(t *testing.T) {
	for _, stage := range []string{"claim", "permit"} {
		for _, key := range []string{"workspace-lease", "p-lease", "write-lease"} {
			for _, failure := range []string{"expired", "owner-replaced", "missing"} {
				t.Run(stage+"/"+key+"/"+failure, func(t *testing.T) {
					pool := stageAPostgresPool(t)
					r := NewRepository(pool)
					if err := r.EnsureSchema(t.Context()); err != nil {
						t.Fatal(err)
					}
					claim := taskBPostgresClaim(t, pool)
					if stage == "permit" {
						if ok, err := r.ClaimRemoteAction(t.Context(), claim); !ok || err != nil {
							t.Fatal(ok, err)
						}
					}
					query := `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1`
					if failure == "owner-replaced" {
						query = `UPDATE connection_health_runtime_leases SET owner_id='new-owner' WHERE lease_key=$1`
					}
					if failure == "missing" {
						query = `DELETE FROM connection_health_runtime_leases WHERE lease_key=$1`
					}
					if _, err := pool.Exec(t.Context(), query, key); err != nil {
						t.Fatal(err)
					}
					allowed := false
					var err error
					if stage == "claim" {
						allowed, err = r.ClaimRemoteAction(t.Context(), claim)
					} else {
						allowed, err = r.PermitRemoteAction(t.Context(), claim)
					}
					if allowed || err == nil {
						t.Errorf("%s accepted invalid %s/%s; must send zero HTTP: allowed=%v err=%v", stage, key, failure, allowed, err)
					}
					var sending int
					if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM connection_health_priority_sync_states WHERE pending_dispatch_phase='sending'`).Scan(&sending); err != nil {
						t.Fatal(err)
					}
					if sending != 0 {
						t.Error("invalid actual database lease admitted a send")
					}
				})
			}
		}
	}
}

func TestPriorityTaskBPostgresAllLocksThenDatabaseTime(t *testing.T) {
	for _, stage := range []string{"claim", "permit"} {
		t.Run(stage, func(t *testing.T) {
			pool := stageAPostgresPool(t)
			r := NewRepository(pool)
			if err := r.EnsureSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			claim := taskBPostgresClaim(t, pool)
			if stage == "permit" {
				if ok, err := r.ClaimRemoteAction(t.Context(), claim); !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			block, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(context.Background())
			var owner string
			if err := block.QueryRow(t.Context(), `SELECT owner_id FROM connection_health_runtime_leases WHERE lease_key='write-lease' FOR UPDATE`).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			// A two-row old implementation also waits on this row. Expire the
			// workspace row inside the same blocker transaction: it is visible
			// only after the actual waiter can resume and must then reject.
			if _, err := block.Exec(t.Context(), `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key='workspace-lease'`); err != nil {
				t.Fatal(err)
			}
			name := "task-b-lease-wait-" + stage
			worker := NewRepository(protocolWorkerPool(t, pool, name))
			type result struct {
				allowed bool
				err     error
			}
			done := make(chan result, 1)
			go func() {
				if stage == "claim" {
					ok, e := worker.ClaimRemoteAction(t.Context(), claim)
					done <- result{ok, e}
				} else {
					ok, e := worker.PermitRemoteAction(t.Context(), claim)
					done <- result{ok, e}
				}
			}()
			protocolWaitForDatabaseLock(t, pool, name)
			if err := block.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.allowed || got.err == nil {
				t.Errorf("%s used a permit from before row-lock wait: %+v", stage, got)
			}
		})
	}
}

func TestActionCheckpointTaskBPostgresReleaseTextIDAndAtomicConfirmation(t *testing.T) {
	for _, kind := range []string{"manual-same-value", "restore-to-manual"} {
		t.Run(kind, func(t *testing.T) {
			pool := stageAPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			claim := taskBPostgresClaim(t, pool)
			claim.DispatchID = "priority-release:" + kind
			claim.Priority.OriginalPriority, claim.Priority.LastAppliedPriority = 50, 1
			claim.Priority.PendingPriority = intPointer(1)
			target := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: claim.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
			if err := r.UpsertTargetActionState(ctx, target); err != nil {
				t.Fatal(err)
			}
			before, err := r.GetTargetActionState(ctx, "u", "w", claim.TargetID)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := r.ClaimRemoteAction(ctx, claim); !ok || err != nil {
				t.Fatal(ok, err)
			}
			var id string
			if err := pool.QueryRow(ctx, `SELECT pending_dispatch_id FROM connection_health_priority_sync_states`).Scan(&id); err != nil || id != claim.DispatchID {
				t.Fatalf("text ID lost without migration: %q %v", id, err)
			}
			if ok, err := r.PermitRemoteAction(ctx, claim); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if err := r.RecordRemoteActionReceipt(ctx, claim, DispatchConfirmedApplied); err != nil {
				t.Fatal(err)
			}
			// A crash/restart reconstructs only Repository and durable rows.
			restarted := NewRepository(pool)
			observation := RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, Visible: true, InventoryComplete: true, SnapshotStartedAt: time.Now(), Priority: intPointer(1)}
			pair, err := restarted.ReconcileRemoteAction(ctx, observation)
			if err != nil {
				t.Fatal(err)
			}
			if pair.Priority != nil {
				t.Errorf("confirmed release kept ambiguous Priority baseline: %+v", pair.Priority)
			}
			var remaining int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_priority_sync_states`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Errorf("atomic confirmation did not delete Priority: %d", remaining)
			}
			after, err := r.GetTargetActionState(ctx, "u", "w", claim.TargetID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Error("Priority transaction changed independent Target checkpoint")
			}
			if err := r.RecordRemoteActionReceipt(ctx, claim, DispatchConfirmedApplied); err == nil {
				t.Error("late receipt rebuilt released checkpoint")
			}
		})
	}
}

func TestActionCheckpointTaskBPostgresReleaseDeleteFailureRollsBack(t *testing.T) {
	pool := stageAPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	claim := taskBPostgresClaim(t, pool)
	claim.DispatchID = "priority-release:rollback"
	claim.Priority.PendingPriority = intPointer(1)
	if ok, err := r.ClaimRemoteAction(ctx, claim); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := r.PermitRemoteAction(ctx, claim); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := r.RecordRemoteActionReceipt(ctx, claim, DispatchConfirmedApplied); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION task_b_reject_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'task B fixture delete rejection'; END $$; CREATE TRIGGER task_b_reject_delete BEFORE DELETE ON connection_health_priority_sync_states FOR EACH ROW EXECUTE FUNCTION task_b_reject_delete()`); err != nil {
		t.Fatal(err)
	}
	obs := RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, Visible: true, InventoryComplete: true, SnapshotStartedAt: time.Now(), Priority: intPointer(1)}
	if _, err := r.ReconcileRemoteAction(ctx, obs); err == nil {
		t.Error("confirmation reported success despite atomic delete rejection")
	}
	state, err := taskBReadPriorityCheckpoint(ctx, r, claim.TargetID)
	if err != nil || state == nil || state.PendingDispatchID != claim.DispatchID || state.PendingDispatchPhase != DispatchConfirmedApplied || state.LastAppliedPriority != 1000 {
		t.Fatalf("failed atomic transaction released pending or changed last confirmed: %+v %v", state, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER task_b_reject_delete ON connection_health_priority_sync_states`); err != nil {
		t.Fatal(err)
	}
	if pair, err := r.ReconcileRemoteAction(ctx, obs); err != nil || pair.Priority != nil {
		t.Errorf("retry confirmed transaction must finish release: %+v %v", pair, err)
	}
}

func TestActionCheckpointTaskBPostgresFirstNoEffectSurvivesRestart(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchNotSent, DispatchConfirmedRejected, DispatchPrepared} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			pool := stageAPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			claim := taskBPostgresClaim(t, pool)
			claim.Priority.OriginalPriority, claim.Priority.LastAppliedPriority = 50, 0
			if ok, err := r.ClaimRemoteAction(ctx, claim); !ok || err != nil {
				t.Fatal(ok, err)
			}
			if phase == DispatchPrepared {
				if _, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE owner_id=$1`, claim.OwnerID); err != nil {
					t.Fatal(err)
				}
			} else if err := r.RecordRemoteActionReceipt(ctx, claim, phase); err != nil {
				t.Fatal(err)
			}
			restarted := NewRepository(pool)
			pair, err := restarted.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
			if err != nil || pair.Priority != nil || pair.pendingCount() != 0 {
				t.Fatalf("restart left false unwritten baseline after %s: %+v %v", phase, pair, err)
			}
			if state, err := taskBReadPriorityCheckpoint(ctx, r, claim.TargetID); err != nil || state != nil {
				t.Fatal("zero-write deletion not durably committed")
			}
		})
	}
}

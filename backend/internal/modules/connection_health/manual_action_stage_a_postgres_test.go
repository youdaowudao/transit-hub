package connection_health

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"transithub/backend/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Explicit local acceptance only; defaults never connect or start a service.
// Config.Load is the existing application loader. No connection string or
// credential is logged, and fixtures use the established isolated-schema helper.
func stageAPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("P1_STAGE_A_LOCAL_DB") == "1" {
		t.Chdir("../../..")
		cfg := config.Load()
		parsed, err := url.Parse(cfg.DatabaseURL)
		if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() != "15432" || parsed.Path != "/transithub" {
			t.Fatal("stage A local database target does not match approved workspace")
		}
		t.Setenv("TEST_DATABASE_URL", cfg.DatabaseURL)
	}
	return openQuestionAnswerPostgresPool(t)
}

func TestStageAPostgresManualTemporaryReceiptsPersistAndDelete(t *testing.T) {
	for _, kind := range []string{TargetMutationStatus, TargetMutationSchedulable, TargetMutationDelete} {
		t.Run(kind, func(t *testing.T) {
			pool := stageAPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			handle, _, err := r.AcquireActionLease(ctx, "account", true)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Release()
			mutation, _, err := r.AcquireActionLease(ctx, "write", true)
			if err != nil {
				t.Fatal(err)
			}
			defer mutation.Release()
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingActionKind: kind, PendingSource: ActionSourceManual, PendingStatus: "inactive", PendingGroupIDs: []string{"1", "2"}}
			if kind == TargetMutationSchedulable {
				state.PendingStatus = ""
				state.PendingSchedulable = boolPointer(false)
			}
			claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", state.TargetID}, Kind: ActionKindTarget, DispatchID: "exact-dispatch", OwnerID: handle.OwnerID, LeaseKey: handle.Key, MutationLeaseKey: mutation.Key, MutationOwnerID: mutation.OwnerID, Target: &state}
			if ok, err := r.ClaimRemoteAction(ctx, claim); err != nil || !ok {
				t.Fatalf("claim failed: %v", err)
			}
			persisted, err := r.GetTargetActionState(ctx, "u", "w", state.TargetID)
			if err != nil || persisted.PendingActionKind != kind || persisted.PendingSource != ActionSourceManual || len(persisted.PendingGroupIDs) != 2 {
				t.Fatal("manual claim fields not persisted")
			}
			if ok, err := r.PermitRemoteAction(ctx, claim); err != nil || !ok {
				t.Fatalf("permit failed: %v", err)
			}
			if err := r.RecordRemoteActionReceipt(ctx, claim, DispatchNotSent); err != nil {
				t.Fatal(err)
			}
			pair, err := r.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
			if err != nil || pair.pendingCount() != 0 || pair.Target != nil {
				t.Fatal("not_sent temporary row was not deleted without inventory")
			}
			if !pair.reconciledWithoutRemoteEffect {
				t.Fatal("committed no-side-effect settlement did not return capacity-release evidence")
			}
			again, err := r.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
			if err != nil || again.reconciledWithoutRemoteEffect {
				t.Fatal("no-side-effect evidence leaked into a later empty transaction")
			}
			rows, err := r.ListTargetActionStates(ctx, "u", "w")
			if err != nil || len(rows) != 0 {
				t.Fatal("temporary row remains in database")
			}
		})
	}
}

func TestStageAPostgresPermitRejectsLostMutationLease(t *testing.T) {
	pool := stageAPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	handle, _, err := r.AcquireActionLease(ctx, "account", true)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	mutation, _, err := r.AcquireActionLease(ctx, "write", true)
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.Release()
	state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", PendingSource: ActionSourceManual, PendingStatus: "inactive", PendingActionKind: TargetMutationStatus}
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", state.TargetID}, Kind: ActionKindTarget, DispatchID: "exact-dispatch", OwnerID: handle.OwnerID, LeaseKey: handle.Key, MutationLeaseKey: mutation.Key, MutationOwnerID: mutation.OwnerID, Target: &state}
	if ok, err := r.ClaimRemoteAction(ctx, claim); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1`, mutation.Key); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.PermitRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionLeaseLost) {
		t.Fatalf("expired write lease permitted: %v", err)
	}
	persisted, err := r.GetTargetActionState(ctx, "u", "w", state.TargetID)
	if err != nil || persisted.PendingDispatchPhase != DispatchPrepared {
		t.Fatal("rejected permit changed prepared record")
	}
}

func TestStageAPostgresManualClosePreservesConflictAndOriginalBaseline(t *testing.T) {
	pool := stageAPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	original := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", LastAppliedStatus: "inactive", OriginalWeight: intPointer(78), LastAppliedWeight: intPointer(23), Conflict: true}
	if err := r.UpsertTargetActionState(ctx, original); err != nil {
		t.Fatal(err)
	}
	handle, _, err := r.AcquireActionLease(ctx, "account", true)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Release()
	state := original
	state.PendingStatus = "inactive"
	state.PendingActionKind = TargetMutationStatus
	state.PendingSource = ActionSourceManual
	state.PendingHadAutomaticBaseline = true
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", state.TargetID}, Kind: ActionKindTarget, DispatchID: "manual-close", OwnerID: handle.OwnerID, LeaseKey: handle.Key, Target: &state}
	if ok, err := r.ClaimRemoteAction(ctx, claim); err != nil || !ok {
		t.Fatal("existing conflict blocked explicit manual intent")
	}
	if ok, err := r.PermitRemoteAction(ctx, claim); err != nil || !ok {
		t.Fatal(err)
	}
	if err := r.RecordRemoteActionReceipt(ctx, claim, DispatchConfirmedApplied); err != nil {
		t.Fatal(err)
	}
	pair, err := r.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: time.Now(), Status: "inactive"})
	if err != nil || pair.pendingCount() != 0 || pair.Target == nil || !pair.Target.Conflict || pair.Target.OriginalStatus != "active" || pair.Target.LastAppliedStatus != "inactive" || *pair.Target.OriginalWeight != 78 || *pair.Target.LastAppliedWeight != 23 {
		t.Fatal("manual close overwrote automatic baseline")
	}
}

// This is an acceptance rehearsal of the approved-per-record SQL workflow,
// deliberately test-only. It is not a force API or an automatic maintenance job.
func stageARehearseMaintenance(ctx context.Context, r *Repository, endedEvidence bool, dispatch, phase, target string, auditMustFail bool) error {
	if !endedEvidence {
		return errors.New("old request termination evidence required")
	}
	tx, err := r.beginWorkspaceTransaction(ctx, "u", "w")
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT target_id FROM connection_health_target_action_states WHERE user_id='u' AND admin_account_id='w' AND pending_dispatch_id=$1 AND pending_dispatch_phase=$2 AND pending_owner_id='old-owner' AND ($3='' OR target_id=$3) FOR UPDATE`, dispatch, phase, target)
	if err != nil {
		return err
	}
	matched := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		matched = append(matched, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(matched) != 1 {
		return errors.New("approved maintenance must match exactly one row")
	}
	tag, err := tx.Exec(ctx, `UPDATE connection_health_target_action_states SET last_applied_status=pending_status,pending_status='',pending_weight=NULL,pending_dispatch_id='',pending_owner_id='',pending_dispatch_phase='',pending_action_kind='',pending_schedulable=NULL,pending_source='',pending_group_ids='{}',pending_had_automatic_baseline=false,updated_at=clock_timestamp() WHERE user_id='u' AND admin_account_id='w' AND target_id=$3 AND pending_dispatch_id=$1 AND pending_dispatch_phase=$2 AND pending_owner_id='old-owner'`, dispatch, phase, matched[0])
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("maintenance changed row count")
	}
	auditID := "approved-maintenance"
	if auditMustFail {
		auditID = "existing-audit"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connection_health_events(id,connection_id,user_id,admin_account_id,result,error_detail,action_source,source) VALUES($1,$2,'u','w','maintenance_confirmed_applied',$3,'approved_maintenance','manual')`, auditID, matched[0], dispatch+":"+phase); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestStageAPostgresMaintenanceRequiresExactApprovalAndAtomicAudit(t *testing.T) {
	for _, sample := range []struct {
		name                            string
		ended                           bool
		dispatch, phase, target         string
		multiple, auditFailure, success bool
	}{
		{"no-termination-evidence", false, "exact", "uncertain", "sub2api:w:1", false, false, false},
		{"changed-dispatch", true, "different", "uncertain", "sub2api:w:1", false, false, false},
		{"changed-phase", true, "exact", "sending", "sub2api:w:1", false, false, false},
		{"multiple-matches", true, "exact", "uncertain", "", true, false, false},
		{"audit-error-rollback", true, "exact", "uncertain", "sub2api:w:1", false, true, false},
		{"exact-one-and-audit", true, "exact", "uncertain", "sub2api:w:1", false, false, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			pool := stageAPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive", PendingDispatchID: "exact", PendingOwnerID: "old-owner", PendingDispatchPhase: DispatchUncertain}
			if err := writeStageAUncertainFixture(ctx, pool, state); err != nil {
				t.Fatal(err)
			}
			if sample.multiple {
				state.TargetID = "sub2api:w:2"
				if err := writeStageAUncertainFixture(ctx, pool, state); err != nil {
					t.Fatal(err)
				}
			}
			if sample.auditFailure {
				if _, err := pool.Exec(ctx, `INSERT INTO connection_health_events(id,connection_id,user_id,admin_account_id,result) VALUES('existing-audit','sentinel','u','w','sentinel')`); err != nil {
					t.Fatal(err)
				}
			}
			err := stageARehearseMaintenance(ctx, r, sample.ended, sample.dispatch, sample.phase, sample.target, sample.auditFailure)
			if (err == nil) != sample.success {
				t.Fatalf("maintenance result mismatch success=%v", sample.success)
			}
			after, err := r.GetTargetActionState(ctx, "u", "w", "sub2api:w:1")
			if err != nil {
				t.Fatal(err)
			}
			var audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events WHERE result='maintenance_confirmed_applied' AND connection_id='sub2api:w:1'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if sample.success {
				if targetActionPending(after) || after.OriginalStatus != "active" || after.LastAppliedStatus != "inactive" || audits != 1 {
					t.Fatal("precise maintenance did not settle and audit atomically")
				}
			} else {
				if !targetActionPending(after) || after.PendingDispatchID != "exact" || after.PendingDispatchPhase != DispatchUncertain || after.LastAppliedStatus != "active" || audits != 0 {
					t.Fatal("rejected maintenance changed protected row or audit")
				}
			}
		})
	}
}
func writeStageAUncertainFixture(ctx context.Context, pool *pgxpool.Pool, state TargetActionState) error {
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_target_action_states(user_id,admin_account_id,target_id,original_status,last_applied_status,pending_status,pending_dispatch_id,pending_owner_id,pending_dispatch_phase) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, state.UserID, state.AdminAccountID, state.TargetID, state.OriginalStatus, state.LastAppliedStatus, state.PendingStatus, state.PendingDispatchID, state.PendingOwnerID, state.PendingDispatchPhase)
	return err
}

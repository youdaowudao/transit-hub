package connection_health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func protocolWorkerPool(t *testing.T, pool *pgxpool.Pool, name string) *pgxpool.Pool {
	t.Helper()
	config := pool.Config().Copy()
	config.ConnConfig.RuntimeParams["application_name"] = name
	worker, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("cannot create isolated worker pool")
	}
	t.Cleanup(worker.Close)
	return worker
}

// Observe the real database lock waiter before advancing the race. A timeout
// fails the test rather than pretending that elapsed time proves contention.
func protocolWaitForDatabaseLock(t *testing.T, pool *pgxpool.Pool, application string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, application).Scan(&waiting)
		if err != nil {
			t.Fatalf("observe real lock waiter: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker did not reach database lock")
		case <-tick.C:
		}
	}
}

func TestProtocolPostgresCrossTableRaceBothDirections(t *testing.T) {
	for _, firstKind := range []string{ActionKindPriority, ActionKindTarget} {
		t.Run(firstKind, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			p, target := protocolClaims()
			for _, claim := range []RemoteActionClaim{p, target} {
				_, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, claim.LeaseKey, claim.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
			}
			first, second := p, target
			if firstKind == ActionKindTarget {
				first, second = target, p
			}
			tx, err := r.beginWorkspaceTransaction(ctx, "u", "w")
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			firstName := "protocol-first-" + firstKind
			secondName := "protocol-second-" + firstKind
			firstRepo := NewRepository(protocolWorkerPool(t, pool, firstName))
			secondRepo := NewRepository(protocolWorkerPool(t, pool, secondName))
			type result struct {
				allowed bool
				err     error
			}
			a, b := make(chan result, 1), make(chan result, 1)
			go func() { ok, err := firstRepo.ClaimRemoteAction(ctx, first); a <- result{ok, err} }()
			protocolWaitForDatabaseLock(t, pool, firstName)
			go func() { ok, err := secondRepo.ClaimRemoteAction(ctx, second); b <- result{ok, err} }()
			protocolWaitForDatabaseLock(t, pool, secondName)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			winner, loser := <-a, <-b
			if !winner.allowed || winner.err != nil || loser.allowed {
				t.Fatalf("cross-table race winner=%+v loser=%+v", winner, loser)
			}
			counts := map[string]int{}
			client := &http.Client{Transport: protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				counts[req.URL.Path]++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
			})}
			for _, claim := range []RemoteActionClaim{first, second, first} {
				allowed, err := r.PermitRemoteAction(ctx, claim)
				if err != nil || !allowed {
					continue
				}
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/"+claim.Kind, strings.NewReader(`{}`))
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
			}
			if counts["/"+first.Kind] != 1 || counts["/"+second.Kind] != 0 {
				t.Errorf("actual HTTP winner=%d loser=%d", counts["/"+first.Kind], counts["/"+second.Kind])
			}
			var pending int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_priority_sync_states WHERE pending_dispatch_id<>'')+(SELECT count(*) FROM connection_health_target_action_states WHERE pending_dispatch_id<>'')`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 1 {
				t.Errorf("persisted pending claims=%d, want 1", pending)
			}
		})
	}
}

func TestProtocolPostgresPausedPreparedOwnerCannotSend(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	p, target := protocolClaims()
	for _, claim := range []RemoteActionClaim{p, target} {
		if _, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, claim.LeaseKey, claim.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.ClaimRemoteAction(ctx, p); err != nil || !ok {
		t.Fatalf("prepare A: %v %v", ok, err)
	}
	// Simulate process suspension without relying on its Lost goroutine.
	if _, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1`, p.LeaseKey); err != nil {
		t.Fatal(err)
	}
	current := 1000
	obs := RemoteActionObservation{RemoteActionScope: p.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: time.Now(), Priority: &current, Status: "active"}
	if _, err := r.ReconcileRemoteAction(ctx, obs); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ClaimRemoteAction(ctx, target); err != nil || !ok {
		t.Fatalf("new owner B: %v %v", ok, err)
	}
	if ok, _ := r.PermitRemoteAction(ctx, p); ok {
		t.Fatal("resumed A obtained sending permission after revocation")
	}
	if ok, err := r.PermitRemoteAction(ctx, target); err != nil || !ok {
		t.Fatalf("B permit: %v %v", ok, err)
	}
}

func TestProtocolPostgresSendingBlocksRecoveryAndCheckpointBypasses(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	p, target := protocolClaims()
	for _, claim := range []RemoteActionClaim{p, target} {
		if _, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, claim.LeaseKey, claim.OwnerID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.ClaimRemoteAction(ctx, p); err != nil || !ok {
		t.Fatal("claim A", err)
	}
	if ok, err := r.PermitRemoteAction(ctx, p); err != nil || !ok {
		t.Fatal("permit A", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1`, p.LeaseKey); err != nil {
		t.Fatal(err)
	}
	for _, visible := range []bool{false, true} {
		current := 1000
		_, err := r.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: p.RemoteActionScope, InventoryComplete: true, Visible: visible, SnapshotStartedAt: time.Now(), Priority: &current, Status: "active"})
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := r.ClaimRemoteAction(ctx, target); ok {
			t.Fatalf("sending disappeared on visible=%v", visible)
		}
	}
	// Existing Upsert/Delete methods must share the same guard as dispatch.
	if err := r.UpsertTargetActionState(ctx, *target.Target); err == nil {
		t.Error("plain target Upsert bypassed Priority sending claim")
	}
	if err := r.DeletePrioritySyncState(ctx, "u", "w", p.TargetID); err == nil {
		t.Error("plain Priority Delete erased an unresolved sending claim")
	}
	for _, phase := range []RemoteDispatchPhase{DispatchUncertain, DispatchConfirmedApplied} {
		if err := r.RecordRemoteActionReceipt(ctx, p, phase); err != nil {
			t.Fatalf("late original-owner receipt %s: %v", phase, err)
		}
		if ok, _ := r.ClaimRemoteAction(ctx, target); ok {
			t.Fatalf("receipt %s prematurely released target", phase)
		}
	}
	var receiptAt time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM connection_health_priority_sync_states WHERE user_id='u' AND admin_account_id='w' AND target_id=$1`, p.TargetID).Scan(&receiptAt); err != nil {
		t.Fatal(err)
	}
	value := 1001
	_, err := r.ReconcileRemoteAction(ctx, RemoteActionObservation{RemoteActionScope: p.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: receiptAt.Add(time.Millisecond), Priority: &value, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ClaimRemoteAction(ctx, target); err != nil || !ok {
		t.Fatal(fmt.Sprintf("fresh terminal reconciliation did not release: %v %v", ok, err))
	}
}

func TestProtocolPostgresExpiredLeaseRenewalSignalsLostAndNeverRevives(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	handle, acquired, err := r.AcquireActionLease(ctx, "protocol-expiry", false)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v %v", acquired, err)
	}
	defer handle.Release()
	if _, err := pool.Exec(ctx, `UPDATE connection_health_runtime_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE lease_key=$1 AND owner_id=$2`, handle.Key, handle.OwnerID); err != nil {
		t.Fatal(err)
	}
	if err := renewActionLease(ctx, pool, handle); !errors.Is(err, ErrRemoteActionLeaseLost) {
		t.Fatalf("expired renewal must fail: %v", err)
	}
	select {
	case <-handle.Lost:
	default:
		t.Fatal("zero-row renewal did not notify Lost")
	}
	if handle.Context.Err() == nil {
		t.Fatal("lease work context remains live after loss")
	}
	var expired bool
	if err := pool.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM connection_health_runtime_leases WHERE lease_key=$1`, handle.Key).Scan(&expired); err != nil || !expired {
		t.Fatalf("expired owner was revived: expired=%v err=%v", expired, err)
	}
	replacement, ok, err := r.AcquireActionLease(ctx, handle.Key, false)
	if err != nil || !ok {
		t.Fatalf("replacement: %v %v", ok, err)
	}
	defer replacement.Release()
	if err := renewActionLease(ctx, pool, handle); !errors.Is(err, ErrRemoteActionLeaseLost) {
		t.Fatalf("old handle unexpectedly renewed: %v", err)
	}
	handle.Release()
	var owner string
	if err := pool.QueryRow(ctx, `SELECT owner_id FROM connection_health_runtime_leases WHERE lease_key=$1`, replacement.Key).Scan(&owner); err != nil || owner != replacement.OwnerID {
		t.Fatalf("old release deleted replacement: owner=%q err=%v", owner, err)
	}
}

func TestProtocolPostgresConfigurationAndClaimRespectWorkspaceOrder(t *testing.T) {
	for _, saveFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("save_first_%v", saveFirst), func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			r := NewRepository(pool)
			ctx := t.Context()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			claim, _ := protocolClaims()
			state := ConnectionHealthState{ConnectionID: claim.TargetID, ModelName: "m", UserID: "u", AdminAccountID: "w", State: StateHealthy, HealthEvidenceStatus: HealthEvidenceLegacy}
			if err := r.UpsertState(ctx, state); err != nil {
				t.Fatal(err)
			}
			claim.Guard = RemoteActionHealthGuard{Required: true, InventoryComplete: true, Memberships: []TestConfigurationSource{{AdminGroupID: "g"}}, Configuration: defaultTestConfiguration(), Models: []string{"m"}, ExpectedStates: []ConnectionHealthState{state}}
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, claim.LeaseKey, claim.OwnerID); err != nil {
				t.Fatal(err)
			}
			save := func() {
				t.Helper()
				if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "g", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
					t.Fatal(err)
				}
			}
			if saveFirst {
				save()
			}
			ok, err := r.ClaimRemoteAction(ctx, claim)
			if saveFirst {
				if ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
					t.Fatalf("old configuration obtained claim after save: %v %v", ok, err)
				}
				var claims int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_priority_sync_states`).Scan(&claims); err != nil || claims != 0 {
					t.Fatalf("denied claim left prepared row: %d %v", claims, err)
				}
				return
			}
			if err != nil || !ok {
				t.Fatalf("earlier claim denied: %v %v", ok, err)
			}
			save()
			// Claim admission precedes the save, but final sending permission must recheck configuration.
			if ok, err := r.PermitRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatalf("configuration change reached final sending permit: %v %v", ok, err)
			}
			if ok, _ := r.PermitRemoteAction(ctx, claim); ok {
				t.Fatal("stale admitted action obtained sending permit")
			}
		})
	}
}

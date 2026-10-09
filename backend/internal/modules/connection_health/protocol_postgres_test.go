package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// These tests require an explicitly approved TEST_DATABASE_URL. The existing
// helper uses an isolated schema; retention is explicit, and default tests never connect.
func TestProtocolPostgresConfigurationIsolationAndIdempotence(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		user, workspace string
		protocol        TestProtocol
		timeout         int
	}{
		{"u1", "w1", TestProtocolResponses, 30}, {"u2", "w1", TestProtocolChatCompletions, 10}, {"u1", "w2", TestProtocolResponses, 120},
	} {
		if err := r.SaveGroupTestConfiguration(ctx, scope.user, scope.workspace, "1", &GroupTestConfiguration{scope.protocol, scope.timeout}); err != nil {
			t.Fatal(err)
		}
	}
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM connection_health_group_test_configs WHERE user_id='u1' AND admin_account_id='w1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u1", "w1", "1", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM connection_health_group_test_configs WHERE user_id='u1' AND admin_account_id='w1'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Error("identical save must not rewrite configuration")
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u1", "w1", "1", nil); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		user, workspace string
		count           int
	}{{"u1", "w1", 0}, {"u2", "w1", 1}, {"u1", "w2", 1}} {
		rows, err := r.ListGroupTestConfigurations(ctx, scope.user, scope.workspace)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != scope.count {
			t.Errorf("scope %s/%s rows=%d, want %d", scope.user, scope.workspace, len(rows), scope.count)
		}
	}
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatalf("repeat schema: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_group_test_configs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("repeat migration changed saved configurations: %d", count)
	}
	for _, value := range []struct {
		protocol string
		timeout  int
	}{{"messages", 30}, {"responses", 4}, {"responses", 121}} {
		_, err := pool.Exec(ctx, `INSERT INTO connection_health_group_test_configs(user_id,admin_account_id,admin_group_id,protocol,probe_timeout_seconds) VALUES ('bad','bad','1',$1,$2)`, value.protocol, value.timeout)
		if err == nil {
			t.Errorf("database accepted illegal configuration %s/%d", value.protocol, value.timeout)
		}
	}
}

func TestProtocolPostgresWorkspaceLockProtectsAbsentRowAndRollsBack(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	result := make(chan error, 1)
	go func() {
		result <- r.SaveGroupTestConfiguration(ctx, "u", "w", "new-group", &GroupTestConfiguration{TestProtocolResponses, 30})
	}()
	// This waits for PostgreSQL's configured lock_timeout, not an assumed
	// goroutine schedule. A missing-row row-lock-only design would insert here.
	select {
	case err := <-result:
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("expected actual PostgreSQL lock timeout, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("workspace lock waiter did not terminate")
	}
	rows, err := r.ListGroupTestConfigurations(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Error("timed-out transaction partially saved first configuration")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "new-group", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatalf("explicit retry after rollback: %v", err)
	}
	rows, err = r.ListGroupTestConfigurations(ctx, "u", "w")
	if err != nil || len(rows) != 1 {
		t.Fatalf("failed lock attempt leaked lock or mutation: rows=%d err=%v", len(rows), err)
	}
}

func TestProtocolPostgresWorkspaceLockOrderAndLocalTimeouts(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var lock, statement, idle string
	if err := tx.QueryRow(ctx, `SELECT current_setting('lock_timeout'),current_setting('statement_timeout'),current_setting('idle_in_transaction_session_timeout')`).Scan(&lock, &statement, &idle); err != nil {
		t.Fatal(err)
	}
	if lock == "0" || statement == "0" || idle == "0" {
		t.Fatalf("unbounded workspace transaction: lock=%s statement=%s idle=%s", lock, statement, idle)
	}
	// An unrelated workspace can proceed while this W remains held.
	other, err := r.beginWorkspaceTransaction(ctx, "u", "other")
	if err != nil {
		t.Fatalf("unrelated workspace blocked: %v", err)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolPostgresWorkspaceIdleTimeoutRollsBackAndReleasesLock(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, "idle-user", "idle-workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `INSERT INTO connection_health_group_test_configs(user_id,admin_account_id,admin_group_id,protocol,probe_timeout_seconds) VALUES ('idle-user','idle-workspace','uncommitted','responses',30)`); err != nil {
		t.Fatal(err)
	}
	// Wait for the server's actual idle timeout at the production value. There
	// is no elapsed-time assumption, test override, or query keeping tx alive.
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	message, err := tx.Conn().PgConn().ReceiveMessage(waitCtx)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code != "25P03" {
			t.Fatalf("unexpected server termination: %s", pgErr.Code)
		}
	} else if response, ok := message.(*pgproto3.ErrorResponse); !ok || response.Code != "25P03" {
		t.Fatalf("expected server idle-in-transaction timeout 25P03, got %T and %v", message, err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("terminated idle transaction unexpectedly committed")
	}
	if err := r.SaveGroupTestConfiguration(ctx, "idle-user", "idle-workspace", "retry", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatalf("idle timeout did not release the same workspace lock: %v", err)
	}
	rows, err := r.ListGroupTestConfigurations(ctx, "idle-user", "idle-workspace")
	if err != nil || len(rows) != 1 || rows[0].AdminGroupID != "retry" || rows[0].Protocol != TestProtocolResponses {
		t.Fatalf("idle rollback retained uncommitted data or blocked retry: rows=%+v err=%v", rows, err)
	}
}

func TestProtocolPostgresCredentialPreparationPreservesConcurrentHealth(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := protocolEvidenceState(StateDegraded, 60, now)
	old.UpdatedAt = now
	old.LastRemoteAction = RemoteActionSub2APIStatusInactive
	if err := r.UpsertState(ctx, old); err != nil {
		t.Fatal(err)
	}
	// A newer accepted failure commits while credential preparation still owns
	// an old snapshot. The diagnostic write must not restore that snapshot.
	target := AdminProbeTarget{TargetID: old.ConnectionID, Platform: "sub2api", AccountID: "shared", InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
	newerAt := now.Add(time.Second)
	accepted, err := r.CommitTargetProbe(ctx, TargetProbeCommit{UserID: old.UserID, AdminAccountID: old.AdminAccountID, Target: target, ModelName: old.ModelName,
		Policy: Policy{AutoDegradeEnabled: true, FailureThreshold: 10}, Outcome: ProbeOutcome{Result: ResultRateLimited, Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, Detail: "newer accepted failure"},
		Event: ConnectionHealthEvent{ID: "credential-newer-failure", UserID: old.UserID, AdminAccountID: old.AdminAccountID, ConnectionID: old.ConnectionID, ModelName: old.ModelName}, Now: newerAt})
	if err != nil || accepted.Disposition != "applied" {
		t.Fatalf("newer failure did not commit: %+v err=%v", accepted, err)
	}
	newer := accepted.State
	credentialAt := now.Add(2 * time.Second)
	stored, err := r.RecordTargetCredentialFailure(ctx, old, "credential_unavailable", credentialAt)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConsecutiveFailures != newer.ConsecutiveFailures || stored.LastErrorDetail != newer.LastErrorDetail || stored.LastRemoteAction != newer.LastRemoteAction || !stored.LastAppliedProbeAt.Equal(newerAt) {
		t.Fatalf("credential preparation restored stale health snapshot: %+v", stored)
	}
	if _, err := r.RecordTargetCredentialFailure(ctx, old, "export_unavailable", now); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM connection_health_events`); err != nil {
		t.Fatal(err)
	}
	persisted, err := r.GetState(ctx, old.ConnectionID, old.ModelName)
	if err != nil || persisted == nil {
		t.Fatal("read credential diagnostic", err)
	}
	if persisted.LastCredentialFailureAt == nil || !persisted.LastCredentialFailureAt.Equal(credentialAt) || persisted.LastCredentialFailureReason != "credential_unavailable" {
		t.Fatalf("older diagnostic or repeat schema replaced latest credential metadata: %+v", persisted)
	}
	current := projectCurrentHealth(*persisted, defaultTestConfiguration())
	if current.Status != "failure" || current.ErrorDetail != newer.LastErrorDetail || current.At == nil || !current.At.Equal(newerAt) {
		t.Fatalf("event retention lost current failure: %+v", current)
	}
	commit, err := r.CommitTargetProbe(ctx, TargetProbeCommit{UserID: old.UserID, AdminAccountID: old.AdminAccountID, Target: target, ModelName: old.ModelName,
		Policy: Policy{AutoDegradeEnabled: true, SuccessThreshold: 1, RecoveryStepPercent: 100}, Outcome: ProbeOutcome{Result: ResultOK, Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10},
		Event: ConnectionHealthEvent{ID: "credential-following-success", UserID: old.UserID, AdminAccountID: old.AdminAccountID, ConnectionID: old.ConnectionID, ModelName: old.ModelName}, Now: now.Add(3 * time.Second)})
	if err != nil || commit.Disposition != "applied" {
		t.Fatalf("following real probe not applied: %+v err=%v", commit, err)
	}
	if reason, _ := currentCredentialFailure(commit.State); reason != "" {
		t.Fatalf("successful newer model request kept obsolete credential block: %s", reason)
	}
}

func TestProtocolPostgresMigrationLegacyInvalidAndRepeat(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the pre-upgrade column boundary inside this isolated schema.
	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_states DROP COLUMN health_evidence_status CASCADE`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO connection_health_states(connection_id,model_name,user_id,admin_account_id,upstream_site_id,upstream_group_name,state) VALUES($1,'m','u','w','','','healthy')`
	if _, err := pool.Exec(ctx, insert, "old"); err != nil {
		t.Fatal(err)
	}
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000029_connection_health_test_protocol.sql")
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var old string
	if err := pool.QueryRow(ctx, `SELECT health_evidence_status FROM connection_health_states WHERE connection_id='old'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != "legacy" {
		t.Errorf("pre-upgrade evidence=%s, want legacy", old)
	}
	if _, err := pool.Exec(ctx, insert, "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_states SET health_evidence_status='invalid',health_evidence_protocol=NULL WHERE connection_id='old'`); err != nil {
		t.Fatal(err)
	}
	applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000029_connection_health_test_protocol.sql")
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_states WHERE health_evidence_status='invalid'`).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 2 {
		t.Errorf("new or invalid evidence revived on repeat migration: invalid=%d", invalid)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_states SET health_evidence_status='valid' WHERE connection_id='new'`); err == nil {
		t.Error("valid evidence accepted without a source")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_states SET health_evidence_protocol='responses' WHERE connection_id='new'`); err == nil {
		t.Error("invalid evidence retained a reusable protocol tag")
	}
}

func TestProtocolPostgresLegacyMigrationsPreserveExistingSchemaBehavior(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := t.Context()
	r := NewRepository(pool)
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_policies(id,user_id,name,priority_mode,auto_degrade_enabled)
		VALUES ('without-models','fixture-user','Fake multiplier policy','multiplier',false),
		       ('with-models','fixture-user','Fake probe policy','multiplier',false);
		INSERT INTO connection_health_model_targets(id,policy_id,user_id,model_name)
		VALUES ('fixture-model','with-models','fixture-user','fixture-model');
		INSERT INTO connection_health_events(id,connection_id,user_id,result)
		VALUES ('fixture-event','fixture-target','fixture-user','ok');
	`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000016_connection_health_target_actions.sql")
		applyQuestionAnswerMigrationForTest(t, ctx, pool, "../../database/migrations/000018_connection_health_strategy_mode.sql")
	}
	for id, want := range map[string]string{"without-models": StrategyModeMultiplierOnly, "with-models": StrategyModeHealthProbe} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT strategy_mode FROM connection_health_policies WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("policy %s mode=%s, want %s", id, got, want)
		}
	}
	var eventCount, modelCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_events), (SELECT count(*) FROM connection_health_model_targets)`).Scan(&eventCount, &modelCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || modelCount != 1 {
		t.Errorf("legacy migrations changed existing fixture history/models: events=%d models=%d", eventCount, modelCount)
	}
}

func TestProtocolPostgresAppliedInvalidStaleAndRetentionProjection(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "1", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatal(err)
	}
	// This regression describes legacy verdict/projection behavior. Capture the new workspace generation at request start.
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); err != nil {
		t.Fatal(err)
	}
	captured, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	preset := DefaultRulePreset()
	preset.SuccessThreshold = 1
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	target := AdminProbeTarget{TargetID: "sub2api:w:1", Platform: "sub2api", AccountID: "1", InventoryComplete: true, TestMemberships: []TestConfigurationSource{{AdminGroupID: "1"}}, TestConfiguration: EffectiveTestConfiguration{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Status: "inherited"}}
	input := TargetProbeCommit{ConfigGeneration: captured.ConfigGeneration, UserID: "u", AdminAccountID: "w", Target: target, ModelName: "m", Policy: Policy{RuleVersion: RuleVersionLegacy, RulePreset: &preset, AutoDegradeEnabled: true, FailureThreshold: 3, SuccessThreshold: 1}, Outcome: ProbeOutcome{Result: ResultNetworkFluctuation, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Detail: "accepted failure"}, DecisionKey: "d1", Event: ConnectionHealthEvent{ID: "probe-1", UserID: "u", AdminAccountID: "w", ConnectionID: target.TargetID, ModelName: "m"}, Now: now}
	first, err := r.CommitTargetProbe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Disposition != "applied" || projectCurrentHealth(first.State, target.TestConfiguration).Status != "failure" {
		t.Fatalf("first effective failure absent: %+v", first)
	}
	input.Event.ID = "probe-2"
	input.Now = now.Add(time.Minute)
	input.Outcome.Result = ResultInvalidResponse
	input.Outcome.Detail = "incomplete output"
	if _, err := r.CommitTargetProbe(ctx, input); err != nil {
		t.Fatal(err)
	}
	state, err := r.GetState(ctx, target.TargetID, "m")
	if err != nil || state == nil {
		t.Fatal("read current state", err)
	}
	current := projectCurrentHealth(*state, target.TestConfiguration)
	if current.Status != "failure" || current.ErrorDetail != "accepted failure" || current.At == nil || !current.At.Equal(now) {
		t.Errorf("invalid attempt changed effective failure: %+v", current)
	}
	if state.LastProbeAt == nil || !state.LastProbeAt.Equal(input.Now) {
		t.Error("invalid attempt not recorded separately")
	}
	// Same-protocol timeout changes still make the captured old request stale.
	if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "1", &GroupTestConfiguration{TestProtocolResponses, 31}); err != nil {
		t.Fatal(err)
	}
	input.Event.ID = "probe-3"
	input.Now = now.Add(2 * time.Minute)
	input.Outcome.Result = ResultServerError
	input.Outcome.Detail = "obsolete failure"
	stale, err := r.CommitTargetProbe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Disposition != "stale" {
		t.Errorf("timeout change accepted old result: %s", stale.Disposition)
	}
	state, err = r.GetState(ctx, target.TargetID, "m")
	if err != nil || state == nil {
		t.Fatal(err)
	}
	if state.ConsecutiveFailures != 1 || state.LastErrorDetail != "accepted failure" || !state.LastProbeAt.Equal(now.Add(time.Minute)) {
		t.Errorf("stale result changed state: %+v", state)
	}
	latest, err := r.ListLatestProbeFailureEventsByWorkspace(ctx, "u", "w", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].ID != "probe-1" {
		t.Errorf("current failure query admitted invalid/stale: %+v", latest)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM connection_health_events`); err != nil {
		t.Fatal(err)
	}
	state, err = r.GetState(ctx, target.TargetID, "m")
	if err != nil || state == nil {
		t.Fatal(err)
	}
	if result := projectCurrentHealth(*state, target.TestConfiguration); result.Status != "failure" || result.ErrorDetail != "accepted failure" {
		t.Errorf("event retention erased durable last-applied failure: %+v", result)
	}
	captured, err = r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	input.ConfigGeneration = captured.ConfigGeneration
	input.Target.TestConfiguration.ProbeTimeoutSeconds = 31
	input.Outcome.ProbeTimeoutSeconds = 31
	input.Outcome.Result = ResultOK
	input.Outcome.Detail = ""
	input.Event.ID = "probe-4"
	input.Now = now.Add(3 * time.Minute)
	success, err := r.CommitTargetProbe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if result := projectCurrentHealth(success.State, input.Target.TestConfiguration); result.Status != "success" || result.ErrorDetail != "" {
		t.Errorf("valid success failed to replace current failure: %+v", result)
	}
}

func TestProtocolPostgresEventFailureRollsBackHealthAtomically(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// The trigger exists only in this test's isolated schema and simulates a
	// database-side event failure after the state statement has executed.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_protocol_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected event failure'; END $$;
CREATE TRIGGER protocol_event_failure BEFORE INSERT ON connection_health_events FOR EACH ROW EXECUTE FUNCTION reject_protocol_event()`); err != nil {
		t.Fatal(err)
	}
	target := AdminProbeTarget{TargetID: "sub2api:w:atomic", Platform: "sub2api", AccountID: "atomic", InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
	input := TargetProbeCommit{UserID: "u", AdminAccountID: "w", Target: target, ModelName: "m", Policy: Policy{AutoDegradeEnabled: true, FailureThreshold: 3}, Outcome: ProbeOutcome{Result: ResultServerError, Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10}, Event: ConnectionHealthEvent{ID: "atomic-event", UserID: "u", AdminAccountID: "w", ConnectionID: target.TargetID, ModelName: "m"}, Now: time.Now()}
	if _, err := r.CommitTargetProbe(ctx, input); err == nil {
		t.Fatal("injected event failure was ignored")
	}
	state, err := r.GetState(ctx, target.TargetID, "m")
	if err != nil || state != nil {
		t.Fatalf("event failure left partial health write: state=%+v err=%v", state, err)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_events`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("event transaction left rows: %d %v", events, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER protocol_event_failure ON connection_health_events`); err != nil {
		t.Fatal(err)
	}
	result, err := r.CommitTargetProbe(ctx, input)
	if err != nil || result.Disposition != "applied" {
		t.Fatalf("rollback leaked W or prevented explicit applied retry: %+v %v", result, err)
	}
	if state, err := r.GetState(ctx, target.TargetID, "m"); err != nil || state == nil {
		t.Fatalf("retry did not actually write state: %+v %v", state, err)
	}
}

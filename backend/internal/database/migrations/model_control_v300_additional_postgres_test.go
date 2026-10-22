package migrations

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func v300MigrationOriginalSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openMigrationPostgresPool(t)
	ctx := t.Context()
	if err := ensureMigrationsTable(ctx, pool); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") || name > "000039_connection_health_question_answer_protocol_messages.sql" {
			continue
		}
		data, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(data)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, strings.TrimSuffix(name, ".sql")); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func TestModelControlV300MigrationPreservesHistoryAndTargetsBackfill(t *testing.T) {
	pool := v300MigrationOriginalSchema(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_model_control_rules(id,user_id,admin_account_id,model_name) VALUES('old-rule','v300-history','missing-target-rule','A');
 INSERT INTO connection_health_model_control_targets(id,user_id,admin_account_id,target_id,model_name,closed_entries,closed_accuracy_percent,closed_batch_id,closed_at,observed_state)
 VALUES('old-target','v300-history','missing-target-rule','sub2api:missing-target-rule:1','A','{"old-alias":"A"}',62.5,'old-history-batch',now()-interval '1 hour','closed');
 INSERT INTO connection_health_model_control_events(id,user_id,admin_account_id,target_id,model_name,event_type,actor_user_id,basis,detail)
 VALUES('old-event','v300-history','missing-target-rule','sub2api:missing-target-rule:1','A','close_succeeded','v300-history','{"accuracyPercent":62.5}','{"entries":[{"key":"old-alias","value":"A"}]}');
 INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at,updated_at)
 VALUES('old-answer','v300-history','sub2api:missing-target-rule:1','old-history-batch','A','old-question','old question','unchanged snapshot','succeeded','correct',now()-interval '1 hour',now()-interval '1 hour',now()-interval '1 hour');
 INSERT INTO connection_health_model_control_auto(user_id,admin_account_id,auto_close_enabled,auto_restore_enabled,health_coordination_enabled,auto_close_enabled_at,auto_restore_enabled_at)
 VALUES('v300-history','missing-target-rule',true,true,true,now()-interval '1 hour',now()-interval '1 hour');`)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT to_jsonb(t)::text FROM connection_health_model_control_targets t WHERE id='old-target'`,
		`SELECT to_jsonb(t)::text FROM connection_health_model_control_events t WHERE id='old-event'`,
		// Compare every historical column; the new nullable diagnostics column is
		// checked separately after the full migration chain.
		`SELECT (to_jsonb(t) - 'request_diagnostics')::text FROM connection_health_question_answer_records t WHERE id='old-answer'`,
		`SELECT to_jsonb(t)::text FROM connection_health_model_control_auto t WHERE user_id='v300-history'`,
	}
	before := make([]string, len(queries))
	for i, q := range queries {
		if err = pool.QueryRow(ctx, q).Scan(&before[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err = Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i, q := range queries {
		var after string
		if err = pool.QueryRow(ctx, q).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before[i] {
			t.Errorf("migration rewrote historical row %d before=%s after=%s", i, before[i], after)
		}
	}
	var diagnosticsNull bool
	if err = pool.QueryRow(ctx, `SELECT request_diagnostics IS NULL FROM connection_health_question_answer_records WHERE id='old-answer'`).Scan(&diagnosticsNull); err != nil {
		t.Fatal(err)
	}
	if !diagnosticsNull {
		t.Fatal("new diagnostics column must remain NULL for the historical answer")
	}
	var accuracy, min int
	var version int64
	var oldNewBarrier bool
	if err = pool.QueryRow(ctx, `SELECT s.min_accuracy_percent,s.min_judged_answers,s.version,s.updated_at>r.updated_at FROM connection_health_model_control_settings s CROSS JOIN connection_health_question_answer_records r WHERE s.user_id='v300-history' AND s.admin_account_id='missing-target-rule' AND r.id='old-answer'`).Scan(&accuracy, &min, &version, &oldNewBarrier); err != nil {
		t.Fatal(err)
	}
	if accuracy != 50 || min != 3 || version != 2 || !oldNewBarrier {
		t.Fatal("missing target settings must use original defaults with a new time barrier", accuracy, min, version, oldNewBarrier)
	}
}

func TestModelControlV300MigrationAtomicRollbackAndRetry(t *testing.T) {
	pool := v300MigrationOriginalSchema(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_model_control_settings(user_id,admin_account_id,min_judged_answers,version) VALUES('v300-rollback','rollback',1,4);
 INSERT INTO connection_health_model_control_auto(user_id,admin_account_id) VALUES('v300-rollback','missing');
 CREATE FUNCTION v300_reject_conversion() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'isolated fixture rejects conversion'; END$$;
 CREATE TRIGGER v300_reject_conversion BEFORE UPDATE ON connection_health_model_control_settings FOR EACH ROW EXECUTE FUNCTION v300_reject_conversion();`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Run(ctx, pool); err == nil {
		t.Fatal("forced transaction failure must stop the migration")
	}
	var min, version, missing, applied int
	if err = pool.QueryRow(ctx, `SELECT min_judged_answers,version,(SELECT count(*) FROM connection_health_model_control_settings WHERE user_id='v300-rollback' AND admin_account_id='missing'),(SELECT count(*) FROM schema_migrations WHERE version LIKE '000040%') FROM connection_health_model_control_settings WHERE user_id='v300-rollback' AND admin_account_id='rollback'`).Scan(&min, &version, &missing, &applied); err != nil {
		t.Fatal(err)
	}
	if min != 1 || version != 4 || missing != 0 || applied != 0 {
		t.Fatal("failed migration must roll back values, missing-row insert and application record", min, version, missing, applied)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER v300_reject_conversion ON connection_health_model_control_settings; DROP FUNCTION v300_reject_conversion();`); err != nil {
		t.Fatal(err)
	}
	if err = Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT min_judged_answers,version,(SELECT count(*) FROM schema_migrations WHERE version LIKE '000040%') FROM connection_health_model_control_settings WHERE user_id='v300-rollback' AND admin_account_id='rollback'`).Scan(&min, &version, &applied); err != nil {
		t.Fatal(err)
	}
	if min != 3 || version != 5 || applied != 1 {
		t.Fatal("explicit retry must apply exactly one successful conversion", min, version, applied)
	}
}

func TestModelControlV300UpgradeRunningQuestionPreflight(t *testing.T) {
	pool := v300MigrationOriginalSchema(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment)
 SELECT 'v300-preflight-'||n,'v300-preflight','target','old-batch','A','q','fixture question','fixture body',CASE WHEN n=4 THEN 'running' ELSE 'succeeded' END,CASE WHEN n=4 THEN NULL ELSE 'correct' END FROM generate_series(1,4)n;`)
	if err != nil {
		t.Fatal(err)
	}
	var active, judged int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN('pending','running')),count(*) FILTER(WHERE status='succeeded' AND answer_judgment IN('correct','incorrect')) FROM connection_health_question_answer_records`).Scan(&active, &judged); err != nil {
		t.Fatal(err)
	}
	if active != 1 || judged != 3 {
		t.Fatal("three judged answers cannot mask a still-running old request before the migration", active, judged)
	}
}

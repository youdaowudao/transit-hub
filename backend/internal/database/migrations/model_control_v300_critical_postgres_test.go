package migrations

import (
	"strings"
	"testing"
	"time"
)

// The primary agent's migration expectation is fixed before implementation.
func TestModelControlV300CriticalMigrationPreservesHighSettingsAndBlocksOldEvidence(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := t.Context()
	if err := ensureMigrationsTable(ctx, pool); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name > "000039_connection_health_question_answer_protocol_messages.sql" {
			continue
		}
		sql, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", strings.TrimSuffix(name, ".sql")); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_model_control_settings(user_id,admin_account_id,min_accuracy_percent,min_judged_answers,version,updated_at) VALUES
 ('v300','one',67,1,4,now()-interval '1 hour'),('v300','two',55,2,5,now()-interval '1 hour'),('v300','three',50,3,6,now()-interval '1 hour'),('v300','high',80,11,7,now()-interval '1 hour'),('v300','max',90,50,8,now()-interval '1 hour');
 INSERT INTO connection_health_model_control_auto(user_id,admin_account_id,auto_close_enabled,auto_close_enabled_at) VALUES('v300','missing',true,now()-interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Millisecond)
	if err = Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	type state struct {
		minimum, accuracy int
		version           int64
		at                time.Time
	}
	before := map[string]state{}
	for _, tc := range []struct {
		workspace         string
		minimum, accuracy int
		version           int64
	}{{"one", 3, 67, 5}, {"two", 3, 55, 6}, {"three", 3, 50, 7}, {"high", 11, 80, 8}, {"max", 50, 90, 9}, {"missing", 3, 50, 2}} {
		var s state
		err = pool.QueryRow(ctx, `SELECT min_judged_answers,min_accuracy_percent,version,updated_at FROM connection_health_model_control_settings WHERE user_id='v300' AND admin_account_id=$1`, tc.workspace).Scan(&s.minimum, &s.accuracy, &s.version, &s.at)
		if err != nil {
			t.Errorf("workspace=%s read=%v", tc.workspace, err)
			continue
		}
		if s.minimum != tc.minimum || s.accuracy != tc.accuracy || s.version != tc.version || s.at.Before(start) {
			t.Errorf("workspace=%s got=%+v want min=%d accuracy=%d version=%d migration barrier", tc.workspace, s, tc.minimum, tc.accuracy, tc.version)
		}
		before[tc.workspace] = s
	}
	if err = Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for ws, want := range before {
		var got state
		if err = pool.QueryRow(ctx, `SELECT min_judged_answers,min_accuracy_percent,version,updated_at FROM connection_health_model_control_settings WHERE user_id='v300' AND admin_account_id=$1`, ws).Scan(&got.minimum, &got.accuracy, &got.version, &got.at); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("restart changed migrated settings %s: got=%+v want=%+v", ws, got, want)
		}
	}
}

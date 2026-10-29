package migrations

import (
	"strings"
	"testing"

	"transithub/backend/internal/modules/notifications"
)

func TestSignupRiskMigrationRuntimeSchemaAndWorkspaceCascadePostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY); INSERT INTO admin_accounts VALUES('workspace')`); err != nil {
		t.Fatal("cannot prepare signup-risk schema parent")
	}
	raw, err := migrationFiles.ReadFile("000044_signup_risk_notifications.sql")
	if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(notifications.SignupRiskSchemaSQL) {
		t.Fatal("signup-risk migration diverged from runtime schema")
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal("signup-risk schema cannot repeat safely")
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO signup_risk_settings(user_id,admin_account_id,enabled) VALUES('user','workspace',true); INSERT INTO signup_risk_processed_users VALUES('user','workspace',repeat('a',64),'42')`); err != nil {
		t.Fatal("cannot save processed baseline without a notification")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM admin_accounts WHERE id='workspace'`); err != nil {
		t.Fatal("cannot delete risk fixture workspace")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM signup_risk_settings)+(SELECT count(*) FROM signup_risk_processed_users)`).Scan(&n); err != nil || n != 0 {
		t.Fatal("workspace deletion left risk settings or processed identities")
	}
}

func TestSignupRiskHoursMigrationPreservesOldConfigurationAndRepeatsPostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, notifications.SignupRiskSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO signup_risk_settings(user_id,admin_account_id,enabled,initialized,updated_at) VALUES('user','workspace',true,true,'2031-01-01T00:00:00Z'); INSERT INTO signup_risk_processed_users VALUES('user','workspace',repeat('a',64),'42')`); err != nil {
		t.Fatal("cannot prepare previous persisted configuration")
	}
	raw, err := migrationFiles.ReadFile("000045_signup_risk_work_hours.sql")
	if err != nil || strings.TrimSpace(string(raw)) != strings.TrimSpace(notifications.SignupRiskHoursSchemaSQL) {
		t.Fatal("working hours runtime schema differs from migration")
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal("working hours migration cannot repeat")
		}
	}
	var enabled, initialized bool
	var start, end, zone, updated string
	var processed int
	if err := pool.QueryRow(ctx, `SELECT enabled,initialized,work_start,work_end,time_zone,to_char(updated_at AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS'),(SELECT count(*) FROM signup_risk_processed_users) FROM signup_risk_settings`).Scan(&enabled, &initialized, &start, &end, &zone, &updated, &processed); err != nil {
		t.Fatal(err)
	}
	if !enabled || !initialized || start != "09:00" || end != "18:00" || zone != "Asia/Singapore" || updated != "2031-01-01 00:00:00" || processed != 1 {
		t.Fatal("migration changed old switch, initialization, timestamp or processed identities")
	}
	for _, sql := range []string{`UPDATE signup_risk_settings SET work_start='24:00'`, `UPDATE signup_risk_settings SET work_start=work_end`, `UPDATE signup_risk_settings SET time_zone='Local'`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("database accepted invalid working hours")
		}
	}
}

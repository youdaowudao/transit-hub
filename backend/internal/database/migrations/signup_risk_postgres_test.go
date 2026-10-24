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

package reports

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
	"transithub/backend/internal/database/migrations"
)

func absorbStore(t *testing.T, options ...func(*pgxpool.Config)) (*pgxpool.Pool, Scope) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit isolated PostgreSQL verification requires TEST_DATABASE_URL")
	}
	ctx := t.Context()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("cannot open explicit database")
	}
	schema := fmt.Sprintf("absorb_reports_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal("cannot create isolated schema")
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := base.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot clean exact isolated schema")
		}
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse explicit database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	for _, option := range options {
		option(cfg)
	}
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot connect isolated schema")
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE users(id text PRIMARY KEY,current_admin_account_id text NOT NULL);CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL,created_at timestamptz NOT NULL);
 INSERT INTO users VALUES('fixture-user','fixture-workspace');INSERT INTO admin_accounts VALUES('fixture-workspace','fixture-user',clock_timestamp());
 CREATE TABLE notification_channel_settings(user_id text,admin_account_id text,settings jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,admin_account_id));
 CREATE TABLE strategy_settings(user_id text,admin_account_id text,settings jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,admin_account_id));`); err != nil {
		t.Fatal("cannot initialize isolated workspace fixture")
	}
	if err = migrations.EnsureUserActivitySchema(ctx, pool); err != nil {
		t.Fatal("cannot initialize analytics fixture schema")
	}
	var created time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE id='fixture-workspace'`).Scan(&created); err != nil {
		t.Fatal("cannot read fixture account creation timestamp")
	}
	return pool, Scope{UserID: "fixture-user", AdminAccountID: "fixture-workspace", CreatedAt: created.Format(time.RFC3339Nano)}
}

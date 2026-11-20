package admin_accounts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/modules/user_activity"
	"transithub/backend/internal/shared/workspacescope"
)

var absorbAnalysisTableNames = []string{
	"user_activity_profiles", "user_activity_daily", "user_activity_recharge_daily", "user_activity_site_daily", "user_activity_sync_states",
	"vip_watch", "vip_settings", "vip_alert_states", "sales_excluded_entries", "sales_settings",
}

func absorbDeleteStore(t *testing.T, analysis bool) (*pgxpool.Pool, *Repository) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit TEST_DATABASE_URL is required for isolated workspace deletion verification")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal("cannot open explicit test database")
	}
	schema := fmt.Sprintf("absorb_workspace_delete_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal("cannot create isolated deletion schema")
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot clean exact isolated workspace deletion schema")
		}
		var exists bool
		if err := base.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil || exists {
			t.Error("isolated workspace deletion schema was not fully removed")
		}
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse explicit test database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("cannot open isolated deletion schema")
	}
	if _, err := pool.Exec(t.Context(), `CREATE TABLE users(id text PRIMARY KEY,current_admin_account_id text NOT NULL);
CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL,created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE workspace_cleanup_jobs(id text PRIMARY KEY,user_id text,admin_account_id text,attachment_paths jsonb,upstream_site_ids jsonb,attempts integer,next_attempt_at timestamptz,last_error text,created_at timestamptz,updated_at timestamptz);
CREATE TABLE preserved_original_fact(id text PRIMARY KEY,payload text NOT NULL);
INSERT INTO preserved_original_fact VALUES('unrelated','original-unchanged');
INSERT INTO users VALUES('fixture-user','workspace-a'),('foreign-user','foreign-workspace');
INSERT INTO admin_accounts(id,user_id,created_at) VALUES('workspace-a','fixture-user','2026-11-20T00:00:00.123456Z'),('workspace-b','fixture-user','2026-11-20T00:00:00.234567Z'),('foreign-workspace','foreign-user','2026-11-20T00:00:00.345678Z')`); err != nil {
		t.Fatal("cannot prepare isolated workspace deletion parents")
	}
	// Preserve the original deletion inventory without initializing unrelated module services.
	for _, table := range workspaceDeleteStatements {
		if _, err := pool.Exec(t.Context(), `CREATE TABLE `+table.Name+`(user_id text,admin_account_id text,workspace_admin_account_id text,id text,storage_path text,payload text)`); err != nil {
			t.Fatalf("cannot prepare original deletion dependency %s", table.Name)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO `+table.Name+` VALUES('fixture-user','workspace-a','workspace-a','deleted','deleted-file','original-a'),('fixture-user','workspace-b','workspace-b','preserved-b','preserved-b-file','original-b'),('foreign-user','workspace-a','workspace-a','preserved-user','preserved-user-file','original-user')`); err != nil {
			t.Fatalf("cannot seed original deletion dependency %s", table.Name)
		}
	}
	if analysis {
		for _, ensure := range []func(context.Context, *pgxpool.Pool) error{migrations.EnsureUserActivitySchema, migrations.EnsureVIPSchema, migrations.EnsureSalesSchema} {
			if err := ensure(t.Context(), pool); err != nil {
				t.Fatal("cannot initialize isolated new analysis tables")
			}
		}
		for _, scope := range [][2]string{{"fixture-user", "workspace-a"}, {"fixture-user", "workspace-b"}, {"foreign-user", "workspace-a"}} {
			for _, statement := range []string{
				`INSERT INTO user_activity_profiles VALUES($1,$2,'customer','isolated@example.invalid','isolated','user','active',25,NULL,NULL,now(),now())`,
				`INSERT INTO user_activity_daily VALUES($1,$2,'customer','2026-11-19',1,2,3)`,
				`INSERT INTO user_activity_recharge_daily VALUES($1,$2,'customer','2026-11-19','payment',10,1)`,
				`INSERT INTO user_activity_site_daily VALUES($1,$2,'2026-11-19',25,1,now())`,
				`INSERT INTO user_activity_sync_states(user_id,admin_account_id,status) VALUES($1,$2,'running')`,
				`INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind,note) VALUES($1,$2,'customer','manual','原备注')`,
				`INSERT INTO vip_settings(user_id,admin_account_id) VALUES($1,$2)`,
				`INSERT INTO vip_alert_states VALUES($1,$2,'customer','low',now(),false,'unknown')`,
				`INSERT INTO sales_excluded_entries VALUES($1,$2,'customer','2026-11-19','payment',5,1)`,
				`INSERT INTO sales_settings(user_id,admin_account_id) VALUES($1,$2)`,
			} {
				if _, err := pool.Exec(t.Context(), statement, scope[0], scope[1]); err != nil {
					t.Fatal("cannot seed isolated analysis deletion fixture")
				}
			}
		}
	}
	return pool, NewRepository(pool)
}

func absorbPreservedRows(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY user_id,admin_account_id)::text,'[]') FROM `+table+` t WHERE NOT(user_id='fixture-user' AND admin_account_id='workspace-a')`).Scan(&snapshot); err != nil {
		t.Fatalf("cannot snapshot untouched rows in %s", table)
	}
	return snapshot
}

func TestAbsorbDeleteWorkspaceClearsTenTablesAndPreservesOtherDataPostgres(t *testing.T) {
	for _, analysis := range []bool{false, true} {
		t.Run(fmt.Sprintf("newTablesExist=%t", analysis), func(t *testing.T) {
			pool, repo := absorbDeleteStore(t, analysis)
			tables := []string{"upstream_sites", "strategy_settings", "dashboard_daily_stats"}
			if analysis {
				tables = append(tables, absorbAnalysisTableNames...)
			}
			before := map[string]string{}
			for _, table := range tables {
				before[table] = absorbPreservedRows(t, pool, table)
			}
			result, err := repo.DeleteWorkspace(t.Context(), "fixture-user", "workspace-a")
			if err != nil || result == nil || result.DeletedID != "workspace-a" || !result.WasCurrent || result.CurrentAdminAccountID != "workspace-b" {
				t.Fatalf("workspace deletion failed or changed original fallback: result=%+v err=%v", result, err)
			}
			for _, table := range tables {
				var count int
				if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE user_id='fixture-user' AND admin_account_id='workspace-a'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("deleted workspace retains %s rows", table)
				}
				if before[table] != absorbPreservedRows(t, pool, table) {
					t.Fatalf("deletion changed another user/workspace in %s", table)
				}
			}
			var accounts, jobs, facts int
			if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM admin_accounts),(SELECT count(*) FROM workspace_cleanup_jobs),(SELECT count(*) FROM preserved_original_fact WHERE payload='original-unchanged')`).Scan(&accounts, &jobs, &facts); err != nil || accounts != 2 || jobs != 1 || facts != 1 {
				t.Fatal("deletion changed unrelated original data or lost the original cleanup job")
			}
		})
	}
}

func TestAbsorbDeleteWorkspaceRollsBackOriginalAndAllNewRowsTogetherPostgres(t *testing.T) {
	pool, repo := absorbDeleteStore(t, true)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_analysis_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated final-table deletion failure'; END $$;
CREATE TRIGGER reject_analysis_delete BEFORE DELETE ON sales_settings FOR EACH ROW EXECUTE FUNCTION reject_analysis_delete()`); err != nil {
		t.Fatal("cannot prepare isolated deletion rollback trigger")
	}
	result, err := repo.DeleteWorkspace(t.Context(), "fixture-user", "workspace-a")
	if err == nil || result != nil {
		t.Fatal("last analysis deletion failure must abort the whole original workspace deletion")
	}
	for _, table := range append([]string{"upstream_sites", "strategy_settings"}, absorbAnalysisTableNames...) {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE user_id='fixture-user' AND admin_account_id='workspace-a'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("failed deletion did not roll back %s", table)
		}
	}
	var current string
	var accounts, jobs int
	if err := pool.QueryRow(t.Context(), `SELECT current_admin_account_id,(SELECT count(*) FROM admin_accounts),(SELECT count(*) FROM workspace_cleanup_jobs) FROM users WHERE id='fixture-user'`).Scan(&current, &accounts, &jobs); err != nil || current != "workspace-a" || accounts != 3 || jobs != 0 {
		t.Fatal("failed deletion changed original parent selection/account/cleanup job")
	}
}

func TestAbsorbDeletedAndRecreatedWorkspaceRejectsLateAnalysisPublicationPostgres(t *testing.T) {
	for _, recreated := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreated=%t", recreated), func(t *testing.T) {
			pool, repo := absorbDeleteStore(t, true)
			var created time.Time
			if err := pool.QueryRow(t.Context(), `SELECT created_at FROM admin_accounts WHERE id='workspace-a'`).Scan(&created); err != nil {
				t.Fatal("cannot read original workspace creation string")
			}
			scope := workspacescope.Scope{UserID: "fixture-user", AdminAccountID: "workspace-a", CreatedAt: created.Format(time.RFC3339Nano)}
			sessions := &absorbCanceledSession{entered: make(chan struct{})}
			service := user_activity.NewService(user_activity.NewRepository(pool), sessions, nil, false)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := service.Shutdown(ctx); err != nil {
					t.Error("isolated analysis task did not stop")
				}
			})
			if state, err := service.StartSync(t.Context(), scope, true); err != nil || state.Status != "running" {
				t.Fatalf("cannot start explicit isolated old-workspace analysis task: status=%s err=%v", state.Status, err)
			}
			select {
			case <-sessions.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("isolated analysis task did not reach cancelable session read")
			}
			if _, err := repo.DeleteWorkspace(t.Context(), scope.UserID, scope.AdminAccountID); err != nil {
				t.Fatal("cannot delete isolated old workspace")
			}
			if recreated {
				if _, err := pool.Exec(t.Context(), `INSERT INTO admin_accounts(id,user_id,created_at) VALUES('workspace-a','fixture-user','2026-11-20T01:00:00Z')`); err != nil {
					t.Fatal("cannot recreate isolated same-ID workspace")
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := service.Shutdown(ctx); err != nil {
				t.Fatal("shutdown did not cancel and wait for the old task")
			}
			called := false
			err := workspacescope.Transaction(t.Context(), pool, scope, false, func(tx pgx.Tx) error {
				called = true
				_, err := tx.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status) VALUES($1,$2,'failed')`, scope.UserID, scope.AdminAccountID)
				return err
			})
			if !errors.Is(err, workspacescope.ErrChanged) || called {
				t.Fatal("old task publication reached persistence after workspace deletion/recreation")
			}
			var count int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM user_activity_sync_states WHERE user_id='fixture-user' AND admin_account_id='workspace-a'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("late canceled task publication resurrected deleted analysis state")
			}
		})
	}
}

type absorbCanceledSession struct{ entered chan struct{} }

func (s *absorbCanceledSession) StoredSession(ctx context.Context, user, workspace string) (upstream.Session, bool, error) {
	close(s.entered)
	<-ctx.Done()
	return upstream.Session{}, false, ctx.Err()
}

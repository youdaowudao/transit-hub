package migrations

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
)

func TestNotificationMigrationAndEnsureSchemaConstraintsPostgres(t *testing.T) {
	for _, entry := range []string{"migration", "EnsureSchema"} {
		t.Run(entry, func(t *testing.T) {
			pool := openMigrationPostgresPool(t)
			ctx := t.Context()
			if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); CREATE TABLE upstream_sites(id text PRIMARY KEY); INSERT INTO admin_accounts(id,user_id) VALUES ('workspace','user'); INSERT INTO upstream_sites(id) VALUES ('site')`); err != nil {
				t.Fatal("cannot prepare notification migration parent fixtures")
			}
			apply := func() {
				t.Helper()
				if entry == "EnsureSchema" {
					if err := notifications.NewRepository(pool).EnsureSchema(ctx); err != nil {
						t.Fatal("cannot apply notification EnsureSchema")
					}
					return
				}
				raw, err := migrationFiles.ReadFile("000041_site_notifications.sql")
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(raw)) != strings.TrimSpace(notifications.SchemaSQL) {
					t.Fatal("notification migration and runtime schema definitions diverged")
				}
				if _, err := pool.Exec(ctx, string(raw)); err != nil {
					t.Fatal("cannot apply notification migration")
				}
			}
			apply()
			service := notifications.NewService(notifications.NewRepository(pool))
			id, inserted, err := service.Publish(ctx, notifications.NoticeInput{
				UserID: "user", AdminAccountID: "workspace", Kind: "fixture.fact", EventKey: "migration-fact", SubjectType: "site", SubjectID: "site", Level: notifications.LevelUrgent,
				DiscoveredAt: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), Content: notifications.Content{Title: "当时的网站名称", Summary: "保留这条提醒的事实"},
			})
			if err != nil || !inserted || id == "" {
				t.Fatalf("cannot publish migrated notice: inserted=%t err=%v", inserted, err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO site_notification_preferences(user_id,admin_account_id) VALUES ('user','workspace'); INSERT INTO upstream_rate_notice_baselines(user_id,admin_account_id,site_id,group_key,group_id,group_name,multiplier,observed_at) VALUES ('user','workspace','site','id:1','1','原分组',0,now())`); err != nil {
				t.Fatal("zero multiplier and default notification preference must be supported")
			}
			apply()
			var defaultSeconds, factCount, baselineCount int
			if err := pool.QueryRow(ctx, `SELECT poll_interval_seconds,(SELECT count(*) FROM site_notifications),(SELECT count(*) FROM upstream_rate_notice_baselines) FROM site_notification_preferences WHERE user_id='user' AND admin_account_id='workspace'`).Scan(&defaultSeconds, &factCount, &baselineCount); err != nil || defaultSeconds != 300 || factCount != 1 || baselineCount != 1 {
				t.Fatal("schema repeat did not preserve records and default 300-second interval")
			}
			for _, level := range []int{0, 5} {
				if _, err := pool.Exec(ctx, `INSERT INTO site_notifications(id,user_id,admin_account_id,kind,event_key,subject_type,subject_id,level,discovered_at,content) VALUES ($1,'user','workspace','fixture.invalid',$1,'site','site',$2,now(),'{"title":"x"}')`, fmt.Sprintf("invalid-level-%d", level), level); err == nil {
					t.Fatalf("database allowed invalid notification level %d", level)
				}
			}
			for _, interval := range []int{0, 59, 61, 3601} {
				if _, err := pool.Exec(ctx, `UPDATE site_notification_preferences SET poll_interval_seconds=$1 WHERE user_id='user' AND admin_account_id='workspace'`, interval); err == nil {
					t.Fatalf("database allowed invalid notification interval %d", interval)
				}
			}
			for _, value := range []string{"-1", "NaN", "Infinity", "-Infinity"} {
				if _, err := pool.Exec(ctx, `UPDATE upstream_rate_notice_baselines SET multiplier=$1::double precision WHERE site_id='site'`, value); err == nil {
					t.Fatalf("database allowed nonfinite or negative baseline %q", value)
				}
			}
			if _, err := pool.Exec(ctx, `DELETE FROM upstream_sites WHERE id='site'`); err != nil {
				t.Fatal("cannot delete isolated website fixture")
			}
			var title string
			if err := pool.QueryRow(ctx, `SELECT content->>'title',(SELECT count(*) FROM upstream_rate_notice_baselines) FROM site_notifications WHERE id=$1`, id).Scan(&title, &baselineCount); err != nil || title != "当时的网站名称" || baselineCount != 0 {
				t.Fatal("website deletion must remove its baseline and retain notification facts")
			}
			if _, err := pool.Exec(ctx, `DELETE FROM admin_accounts WHERE id='workspace'`); err != nil {
				t.Fatal("cannot delete isolated workspace fixture")
			}
			var total int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM site_notifications)+(SELECT count(*) FROM site_notification_preferences)+(SELECT count(*) FROM upstream_rate_notice_baselines)`).Scan(&total); err != nil || total != 0 {
				t.Fatal("workspace deletion must leave no notification facts, preferences or baselines")
			}
		})
	}
}

func TestNotificationRepositoryBindExistingSchemaDoesNotCreateTablesPostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	_ = notifications.NewService(notifications.NewRepository(pool))
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('site_notifications','site_notification_preferences','upstream_rate_notice_baselines')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("binding notification repository/service must not run DDL")
	}
}

func TestNotificationFreshMigrationAddsForeignKeysWhenParentsBecomeAvailablePostgres(t *testing.T) {
	pool := openMigrationPostgresPool(t)
	ctx := t.Context()
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("fresh migration before runtime parent tables: %v", err)
	}
	var tables, foreignKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('site_notifications','site_notification_preferences','upstream_rate_notice_baselines')`).Scan(&tables); err != nil || tables != 3 {
		t.Fatal("fresh migration must create all three notification tables before runtime parents exist")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f' AND connamespace=current_schema()::regnamespace AND conrelid IN ('site_notifications'::regclass,'site_notification_preferences'::regclass,'upstream_rate_notice_baselines'::regclass)`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatal("fresh schema unexpectedly has unavailable runtime parent references")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); CREATE TABLE upstream_sites(id text PRIMARY KEY)`); err != nil {
		t.Fatal("cannot create runtime parent table fixtures")
	}
	if err := notifications.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal("runtime notification schema must attach available parent references")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f' AND confdeltype='c' AND connamespace=current_schema()::regnamespace AND conrelid IN ('site_notifications'::regclass,'site_notification_preferences'::regclass,'upstream_rate_notice_baselines'::regclass) AND confrelid IN ('admin_accounts'::regclass,'upstream_sites'::regclass)`).Scan(&foreignKeys); err != nil || foreignKeys != 4 {
		t.Fatal("runtime schema must attach all four cascading notification parent references")
	}
	if err := notifications.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal("runtime parent attachment must be repeatable")
	}
}

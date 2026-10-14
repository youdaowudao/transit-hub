package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/shared/businesstime"
)

func singaporeCostPGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for isolated PostgreSQL acceptance")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("test database connection failed")
	}
	schema := pgx.Identifier{fmt.Sprintf("singapore_cost_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal("isolated schema creation failed")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("test database configuration failed")
	}
	config.ConnConfig.RuntimeParams["search_path"] = strings.Trim(schema, "\"")
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("isolated database connection failed")
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("isolated schema cleanup failed")
		}
		admin.Close()
	})
	if _, err = pool.Exec(ctx, `CREATE TABLE users(id text PRIMARY KEY);CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL);INSERT INTO users VALUES('user');INSERT INTO admin_accounts VALUES('workspace','user')`); err != nil {
		t.Fatal(err)
	}
	if err = NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// Site creation and sync now join the real notification transaction. Keep the
	// old business expectations while providing their newly required local tables.
	if _, err := pool.Exec(ctx, `
ALTER TABLE admin_accounts ADD COLUMN IF NOT EXISTS platform text NOT NULL DEFAULT 'sub2api';
CREATE TABLE IF NOT EXISTS real_connections (
 id text PRIMARY KEY, user_id text NOT NULL, workspace_admin_account_id text NOT NULL,
 upstream_site_id text NOT NULL, admin_account_id text NOT NULL, admin_account_name text NOT NULL,
 upstream_key_id text NOT NULL, status text NOT NULL DEFAULT 'active', admin_platform text NOT NULL DEFAULT 'sub2api'
);`); err != nil {
		t.Fatal("isolated site notification dependencies failed")
	}
	if err := notifications.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal("isolated site notification schema failed")
	}
	return pool
}
func TestSingaporeCostPGConditionalSaveMergesOwnedFieldsAndNeverResurrects(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	site.Metrics = Metrics{TodayConsume: metric(float64Pointer(1)), TodayConsumeDate: businesstime.DateAt(now), TodayConsumeTimezone: businesstime.Timezone, TodayConsumeAt: &now, TodayConsumeErrorDetail: "旧提示"}
	if err := repo.SaveSite(ctx, *site); err != nil {
		t.Fatal(err)
	}
	// PG has a newer verified amount, while the settings request reads old cache.
	if _, err := pool.Exec(ctx, `UPDATE upstream_sites SET metrics=jsonb_set(metrics,'{todayConsume,value}','9') WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	expected := cloneSite(site)
	candidate := cloneSite(site)
	candidate.Settings.BalanceThreshold = float64Pointer(7)
	saved, err := repo.SaveExistingSiteIfUnchanged(ctx, expected, candidate)
	if err != nil || !saved {
		t.Fatalf("save result=%v err=%v", saved, err)
	}
	rows, err := repo.ListSites(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal("site round trip failed")
	}
	if rows[0].Metrics.TodayConsume.Value == nil || *rows[0].Metrics.TodayConsume.Value != 9 || rows[0].Metrics.TodayConsumeTimezone != businesstime.Timezone || rows[0].Metrics.TodayConsumeErrorDetail != "旧提示" || rows[0].Settings.BalanceThreshold == nil || *rows[0].Settings.BalanceThreshold != 7 {
		t.Fatal("settings overwrote PG costs or JSONB proof/detail was lost")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM upstream_sites WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	if saved, err := repo.SaveExistingSiteIfUnchanged(ctx, candidate, candidate); err != nil || saved {
		t.Fatal("deleted site was recreated")
	}
}
func TestSingaporeCostPGConditionalGuardLocksAndDecodesDefaults(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://fixture.invalid", AccessToken: "fixture-only"})
	if err := repo.SaveSite(ctx, *site); err != nil {
		t.Fatal(err)
	}
	// Missing zero-valued JSON keys compare as decoded defaults, not bytes.
	if _, err := pool.Exec(ctx, `UPDATE upstream_sites SET session=jsonb_build_object('Platform','sub2api','BaseURL','http://fixture.invalid','AccessToken','fixture-only') WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	if saved, err := repo.SaveExistingSiteIfUnchanged(ctx, *site, *site); err != nil || !saved {
		t.Fatal("default JSON fields must not cause a false conflict")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM upstream_sites WHERE id='site' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() { saved, _ := repo.SaveExistingSiteIfUnchanged(ctx, *site, *site); result <- saved }()
	select {
	case <-result:
		t.Fatal("conditional write bypassed the locked row")
	case <-time.After(25 * time.Millisecond):
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sites SET session=jsonb_set(session,'{AccessToken}','"fixture-new"') WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case saved := <-result:
		if saved {
			t.Fatal("old session overwrote the newer PG login")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("conditional writer did not finish")
	}
	if _, err = pool.Exec(ctx, `UPDATE upstream_sites SET session='"malformed"'::jsonb WHERE id='site'`); err != nil {
		t.Fatal(err)
	}
	if saved, err := repo.SaveExistingSiteIfUnchanged(ctx, *site, *site); err == nil || saved {
		t.Fatal("malformed Session JSON must refuse the save")
	}
}
func TestSingaporeCostRedisProofRetentionAndSnapshotOrder(t *testing.T) {
	cache, _, _ := homeCostLocalRedis(t)
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	site := newTestSite("验收-singapore", "验收-user", "workspace", 1, &Session{Platform: PlatformSub2API, AccessToken: "fixture-only"})
	site.Metrics = Metrics{TodayConsume: metric(float64Pointer(3)), TodayConsumeDate: businesstime.DateAt(now), TodayConsumeTimezone: businesstime.Timezone, TodayConsumeErrorDetail: "本站总额读取失败：缺少实际费用字段；本轮没有更新费用，可同步重试。"}
	if err := cache.Set(t.Context(), site); err != nil {
		t.Fatal(err)
	}
	read, err := cache.Get(t.Context(), site.ID)
	if err != nil || read == nil || !reflect.DeepEqual(read.Metrics, site.Metrics) {
		t.Fatal("Redis lost metrics proof or safe detail")
	}
	old := fixtureKeySnapshot(now, 9)
	old.Timezone = ""
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, old); err != nil {
		t.Fatal(err)
	}
	failure := KeyUsageSnapshot{BusinessDate: old.BusinessDate, AttemptStartedAt: now.Add(time.Minute), FailureReason: "独立总额读取失败", Items: []KeyUsageTodayStat{}}
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, failure); err != nil {
		t.Fatal(err)
	}
	snapshot, err := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	if err != nil || snapshot == nil || snapshot.Complete {
		t.Fatal("legacy snapshot was retained as certified after a new failure")
	}
	valid := fixtureKeySnapshot(now.Add(2*time.Minute), 3)
	valid.Timezone = businesstime.Timezone
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, valid); err != nil {
		t.Fatal(err)
	}
	failure.AttemptStartedAt = now.Add(3 * time.Minute)
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, failure); err != nil {
		t.Fatal(err)
	}
	snapshot, err = cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	if err != nil || snapshot == nil || !snapshot.Complete || snapshot.Timezone != businesstime.Timezone || snapshot.Items[0].TodayAmount != 3 {
		t.Fatal("certified same-day snapshot was not retained")
	}
	if err := cache.SaveKeyUsageSnapshot(t.Context(), site.ID, old); err != nil {
		t.Fatal(err)
	}
	after, _ := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	a, _ := json.Marshal(after)
	b, _ := json.Marshal(snapshot)
	if string(a) != string(b) {
		t.Fatal("older WATCH attempt replaced newer certified result")
	}
}

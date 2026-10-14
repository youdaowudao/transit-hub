package group_rates

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/upstream"
)

func additionalSnapshotSource(t *testing.T, pool *pgxpool.Pool, groups []SnapshotGroup) (int64, string) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Groups []SnapshotGroup `json:"groups"`
	}{groups})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UnixMilli()
	var version string
	if err = pool.QueryRow(t.Context(), `UPDATE upstream_sites SET metrics=$1::jsonb,last_synced_at=$2 WHERE id='site-a' RETURNING xmin::text`, string(raw), at).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return at, version
}
func TestCleanupAdditionalPartialAutoOnlyClearsConfirmedIdentityPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	for _, v := range []struct {
		id, group string
		deleted   bool
	}{{"auto-old", "auto", false}, {"auto-new", "auto", true}, {"absent-old", "absent", false}, {"absent-new", "absent", false}} {
		cleanupSeed(t, pool, v.id, "site-a", v.group, v.group, "custom", 2, -1, v.deleted)
	}
	groups := []SnapshotGroup{{ID: "auto", Name: "auto", MultiplierMode: "auto"}}
	at, version := additionalSnapshotSource(t, pool, groups)
	if err := cleanupApply(t.Context(), s, groups, at, version, false); err != nil {
		t.Fatal(err)
	}
	if got := cleanupIDs(t, pool, "site-a"); got != "absent-new,absent-old" {
		t.Fatalf("positive auto must only clear returned identity: %s", got)
	}
	groups = []SnapshotGroup{cleanupGroup("auto", "auto", 3)}
	at, version = additionalSnapshotSource(t, pool, groups)
	if err := cleanupApply(t.Context(), s, groups, at, version, false); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(t.Context(), "user-a", "workspace-a", "site-a", "auto", "")
	if err != nil || len(history) != 1 || history[0].Delta != nil {
		t.Fatalf("fixed after auto must start first sample: %+v %v", history, err)
	}
}
func TestCleanupAdditionalLegacyIdentityAndZeroPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "legacy-old", "site-a", "", "a", "", 1, -2, false)
	cleanupSeed(t, pool, "legacy-latest", "site-a", "", "a", "", 2, -1, true)
	groups := []SnapshotGroup{cleanupGroup("1", "a", 0)}
	at, version := additionalSnapshotSource(t, pool, groups)
	if err := cleanupApply(t.Context(), s, groups, at, version, true); err != nil {
		t.Fatal(err)
	}
	history, err := s.History(t.Context(), "user-a", "workspace-a", "site-a", "1", "")
	if err != nil || len(history) != 3 || history[0].Multiplier != 0 || history[0].Delta == nil || *history[0].Delta != -2 {
		t.Fatalf("zero/legacy history %+v %v", history, err)
	}
	for _, h := range history {
		if h.GroupID != "1" || h.Type != "" {
			t.Fatal("legacy history or cleared classification was not preserved")
		}
	}
}
func TestCleanupAdditionalLegacyAmbiguityRollsBackSitePostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "legacy", "site-a", "", "same", "custom", 1, -2, false)
	groups := []SnapshotGroup{cleanupGroup("1", "same", 1), cleanupGroup("2", "same", 2)}
	at, version := additionalSnapshotSource(t, pool, groups)
	if err := cleanupApply(t.Context(), s, groups, at, version, true); err == nil {
		t.Fatal("legacy ambiguity must reject entire site")
	}
	if cleanupIDs(t, pool, "site-a") != "legacy" {
		t.Fatal("ambiguous identities modified history")
	}
}
func TestCleanupAdditionalSameNameReplacementKeepsDistinctIDPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "old-id", "site-a", "1", "same", "old-user-type", 2, -1, false)
	groups := []SnapshotGroup{cleanupGroup("2", "same", 7)}
	at, version := additionalSnapshotSource(t, pool, groups)
	if err := cleanupApply(t.Context(), s, groups, at, version, true); err != nil {
		t.Fatal(err)
	}
	old, err := s.History(t.Context(), "user-a", "workspace-a", "site-a", "1", "")
	if err != nil || len(old) != 0 {
		t.Fatal("different ID old history remains")
	}
	newHistory, err := s.History(t.Context(), "user-a", "workspace-a", "site-a", "2", "")
	if err != nil || len(newHistory) != 1 || newHistory[0].Type == "old-user-type" || newHistory[0].Delta != nil {
		t.Fatal("new ID borrowed old history/type/change")
	}
}
func TestCleanupAdditionalDeleteSiteAndWriterBothLockOrdersPostgres(t *testing.T) {
	for _, first := range []string{"writer", "delete"} {
		t.Run(first, func(t *testing.T) {
			pool, s := cleanupFixture(t)
			cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 1, -1, false)
			groups := []SnapshotGroup{cleanupGroup("1", "a", 2)}
			at, version := additionalSnapshotSource(t, pool, groups)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(ctx, `SELECT id FROM upstream_sites WHERE id='site-a' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			writerDone, deleteDone := make(chan error, 1), make(chan error, 1)
			writer := func() { writerDone <- cleanupApply(ctx, s, groups, at, version, true) }
			deleteSite := func() { deleteDone <- upstream.NewRepository(pool).DeleteSite(ctx, "user-a", "site-a") }
			if first == "writer" {
				go writer()
			} else {
				go deleteSite()
			}
			waitSnapshotLock(t, pool, 1)
			if first == "writer" {
				go deleteSite()
			} else {
				go writer()
			}
			waitSnapshotLock(t, pool, 2)
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-deleteDone; err != nil {
				t.Fatalf("site deletion failed: %v", err)
			}
			_ = <-writerDone // writer may safely reject a site that deletion removed first.
			var count int
			if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM upstream_sites WHERE id='site-a')+(SELECT count(*) FROM group_rate_snapshots WHERE site_id='site-a')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("deleted site/history reappeared count=%d err=%v", count, err)
			}
		})
	}
}
func waitSnapshotLock(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity AS activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FROM upstream_sites WHERE%' AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=activity.pid AND relation='upstream_sites'::regclass)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("expected %d site-lock waiters", want))
}
func TestCleanupAdditionalWorkspaceDeletionBlocksWriterBeforeSitePostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 1, -1, false)
	groups := []SnapshotGroup{cleanupGroup("1", "a", 2)}
	at, version := additionalSnapshotSource(t, pool, groups)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT id FROM admin_accounts WHERE user_id='user-a' AND id='workspace-a' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cleanupApply(ctx, s, groups, at, version, true) }()
	// The parent lock must be acquired before a writer can acquire the site row.
	var siteLocked bool
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity AS activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM admin_accounts WHERE%' AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=activity.pid AND relation='admin_accounts'::regclass)`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			siteLocked = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !siteLocked {
		t.Fatal("writer did not block on workspace parent")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM group_rate_snapshots WHERE user_id='user-a' AND admin_account_id='workspace-a'; DELETE FROM upstream_sites WHERE user_id='user-a' AND admin_account_id='workspace-a'; DELETE FROM admin_accounts WHERE id='workspace-a'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("deleted workspace authorized snapshot write")
	}
	if cleanupIDs(t, pool, "site-a") != "" {
		t.Fatal("deleted workspace history reappeared")
	}
}

func TestCleanupAdditionalCommitErrorIsUnconfirmedPostgres(t *testing.T) {
	pool, s := cleanupFixture(t)
	cleanupSeed(t, pool, "old", "site-a", "old", "old", "", 1, -1, false)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_snapshot_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit rejection'; END $$; CREATE CONSTRAINT TRIGGER reject_snapshot_commit AFTER INSERT ON group_rate_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_snapshot_commit()`); err != nil {
		t.Fatal(err)
	}
	groups := []SnapshotGroup{cleanupGroup("1", "a", 2)}
	at, version := additionalSnapshotSource(t, pool, groups)
	err := cleanupApply(t.Context(), s, groups, at, version, true)
	outcome, ok := err.(*SnapshotSaveError)
	if !ok || !outcome.Uncertain {
		t.Fatalf("commit error did not retain unconfirmed outcome: %v", err)
	}
	if cleanupIDs(t, pool, "site-a") != "old" {
		t.Fatal("failed commit left partial history")
	}
}

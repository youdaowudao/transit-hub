package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openingFixture(t *testing.T) (string, *sql.DB, string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opening.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(t.Context(), testOwner)
	if err == nil {
		settings.Concurrency = 17
		_, err = s.SaveSettings(t.Context(), testOwner, settings)
	}
	closeErr := s.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	var mode string
	if err = db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil || mode != "delete" {
		t.Fatal("cannot prepare an exclusive read lock", mode, err)
	}
	var raw string
	var revision int
	if err = db.QueryRow("SELECT settings_json,revision FROM hub_settings").Scan(&raw, &revision); err != nil {
		t.Fatal(err)
	}
	return path, db, raw, revision
}

func holdOpeningLock(t *testing.T, db *sql.DB, duration time.Duration) {
	t.Helper()
	if _, err := db.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	timer := time.AfterFunc(duration, func() {
		_, err := db.Exec("ROLLBACK")
		released <- err
	})
	t.Cleanup(func() {
		if timer.Stop() {
			if _, err := db.Exec("ROLLBACK"); err != nil {
				t.Error(err)
			}
		} else if err := <-released; err != nil {
			t.Error(err)
		}
	})
}

func checkOpeningContents(t *testing.T, db *sql.DB, want string, wantRevision int) {
	t.Helper()
	var got string
	var revision int
	if err := db.QueryRow("SELECT settings_json,revision FROM hub_settings").Scan(&got, &revision); err != nil || got != want || revision != wantRevision {
		t.Fatalf("opening changed existing settings: revision=%d error=%v", revision, err)
	}
}

func TestOpeningWaitsForShortReadLockAndPreservesSettings(t *testing.T) {
	path, locker, raw, revision := openingFixture(t)
	holdOpeningLock(t, locker, 150*time.Millisecond)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("short-lived database lock prevented opening: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	checkOpeningContents(t, s.db, raw, revision)
}

func TestOpeningPersistentReadLockFailsWithinExistingWindow(t *testing.T) {
	path, locker, raw, revision := openingFixture(t)
	// The fallback releases a broken unbounded wait so the test leaves no worker.
	holdOpeningLock(t, locker, 8*time.Second)
	started := time.Now()
	s, err := Open(path)
	elapsed := time.Since(started)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("persistent lock did not return a lock error: %v", err)
	}
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("opening did not respect the existing 5-second window: %s", elapsed)
	}
	checkOpeningContents(t, locker, raw, revision)
}

func TestOpeningFutureVersionDoesNotChangeJournalOrContents(t *testing.T) {
	path, db, raw, revision := openingFixture(t)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if s != nil {
		s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "更新版本") {
		t.Fatalf("future version was not refused: %v", err)
	}
	var version int
	var mode string
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion+1 {
		t.Fatal("future database version changed", version, err)
	}
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "delete" {
		t.Fatal("future database journal changed", mode, err)
	}
	checkOpeningContents(t, db, raw, revision)
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("refused opening changed database bytes", err)
	}
}

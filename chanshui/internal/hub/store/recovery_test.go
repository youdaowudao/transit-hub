package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockedWriteEndsWithinDeadlineAndKeepsOriginalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original, err := s.Settings(context.Background(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	lockDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer lockDB.Close()
	if _, err = lockDB.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer lockDB.Exec("ROLLBACK")
	if _, err = s.db.Exec("PRAGMA busy_timeout=20"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	draft := original
	draft.MemoryLimitMiB = 128
	started := time.Now()
	if _, err = s.SaveSettings(ctx, testOwner, draft); err == nil {
		t.Fatal("locked write unexpectedly succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("locked write exceeded its bounded retry window")
	}
	if _, err = lockDB.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(context.Background(), testOwner)
	if err != nil || got != original {
		t.Fatalf("failed write replaced original: %+v %v", got, err)
	}
}

func TestConsistentSQLiteBackupPreservesDeletedPresetsSettingsAndReport(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "live.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	v, err := s.Settings(ctx, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	v.MemoryLimitMiB = 128
	saved, err := s.SaveSettings(ctx, testOwner, v)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Question(ctx, testOwner, "Q2")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("e", 32)
	_, err = s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Target: Target{AccountID: "fixture", AccountName: "备份隔离验收", BaseURL: "https://fixture.invalid", Protocol: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishInterrupted(ctx, testOwner, id, map[string]any{"message": "隔离数据"}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteQuestion(ctx, testOwner, q.ID, q.Revision); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.sqlite")
	// VACUUM INTO produces a transactionally consistent copy, including live WAL.
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	got, err := restored.Settings(ctx, testOwner)
	if err != nil || got != saved {
		t.Fatal(got, err)
	}
	if _, err = restored.Question(ctx, testOwner, "Q2"); err == nil {
		t.Fatal("deleted preset reappeared after consistent restore")
	}
	r, snapshot, err := restored.LoadRun(ctx, testOwner, id)
	if err != nil || r.Status != "interrupted" || len(snapshot.Questions) != 1 || snapshot.Questions[0].Definition.ID != "Q2" {
		t.Fatal(r, snapshot, err)
	}
}

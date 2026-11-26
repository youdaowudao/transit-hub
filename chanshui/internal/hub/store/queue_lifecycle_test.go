package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionTwoMigrationUpdatesOnlyOldDefaultsAndKeepsSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Settings(ctx, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	old.Concurrency, old.TotalTimeoutSeconds, old.MemoryLimitMiB = 2, 600, 128
	old, err = s.SaveSettings(ctx, testOwner, old)
	if err != nil {
		t.Fatal(err)
	}
	customOwner := Owner{"other", "custom"}
	custom, err := s.Settings(ctx, customOwner)
	if err != nil {
		t.Fatal(err)
	}
	custom.Concurrency, custom.TotalTimeoutSeconds = 1, 7200
	custom, err = s.SaveSettings(ctx, customOwner, custom)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if _, err = s.CreateReport(ctx, testOwner, createRequest(id)); err != nil {
		t.Fatal(err)
	}
	stripV8ForMigrationFixture(t, s)
	if _, err = s.db.Exec(`DROP INDEX hub_reports_one_active; CREATE UNIQUE INDEX hub_reports_one_active ON hub_reports((1)) WHERE status IN('queued','running'); PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Settings(ctx, testOwner)
	if err != nil || got.Concurrency != 32 || got.TotalTimeoutSeconds != 3600 || got.MemoryLimitMiB != 128 || got.Revision != old.Revision+1 {
		t.Fatalf("old settings migration: %+v %v", got, err)
	}
	gotCustom, err := s.Settings(ctx, customOwner)
	if err != nil || gotCustom != custom {
		t.Fatalf("custom settings changed: %+v != %+v %v", gotCustom, custom, err)
	}
	report, snap, err := s.LoadRun(ctx, testOwner, id)
	if err != nil || report.Status != "queued" || snap.Settings.Concurrency != 2 || snap.Settings.TotalTimeoutSeconds != 600 {
		t.Fatalf("old snapshot changed: %+v %v", snap.Settings, err)
	}
	if _, err = s.CreateReport(ctx, testOwner, createRequest(strings.Repeat("b", 32))); err != nil {
		t.Fatal("v2 still rejects queued reports", err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	twice, err := s.Settings(ctx, testOwner)
	if err != nil || twice != got {
		t.Fatal("v2 migration reapplied defaults", twice, err)
	}
}

func TestQueuedCancelOwnershipCASAndFailedWriteKeepResults(t *testing.T) {
	ctx := context.Background()
	s := memoryStore(t)
	id := strings.Repeat("a", 32)
	if _, err := s.CreateReport(ctx, testOwner, createRequest(id)); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelQueued(ctx, Owner{"foreign", "foreign"}, id); err == nil {
		t.Fatal("foreign queued report cancelled")
	}
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_cancel BEFORE UPDATE OF status ON hub_reports WHEN NEW.status='cancelled' BEGIN SELECT RAISE(ABORT,'cancel fixture unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelQueued(ctx, testOwner, id); err == nil {
		t.Fatal("failed queued cancellation acknowledged")
	}
	r, err := s.Report(ctx, testOwner, id)
	if err != nil || r.Status != "queued" {
		t.Fatal("failed cancellation removed waiting report", r, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER refuse_cancel`); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelQueued(ctx, testOwner, id); err != nil {
		t.Fatal(err)
	}
	r, err = s.Report(ctx, testOwner, id)
	if err != nil || r.Status != "cancelled" || r.StartedAtMs != 0 {
		t.Fatal(r, err)
	}
	if err = s.CancelQueued(ctx, testOwner, id); err != nil {
		t.Fatal("repeated cancel not idempotent", err)
	}
	if err = s.BeginRun(ctx, testOwner, id); err == nil {
		t.Fatal("cancelled waiting report started")
	}
	next := strings.Repeat("b", 32)
	if _, err = s.CreateReport(ctx, testOwner, createRequest(next)); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginRun(ctx, testOwner, next); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelQueued(ctx, testOwner, next); err == nil {
		t.Fatal("queued cancellation touched running report")
	}
	if err = s.Finish(ctx, testOwner, next, "done", map[string]any{"totalScore": 0}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelQueued(ctx, testOwner, next); err != nil {
		t.Fatal(err)
	}
	r, err = s.Report(ctx, testOwner, next)
	if err != nil || r.Status != "done" || !strings.Contains(string(r.Summary.Summary), `"totalScore":0`) {
		t.Fatal("completed report was overwritten", r, err)
	}
}

func TestHigherTotalTimeoutRejectsOverflowWithoutReplacingSavedSettings(t *testing.T) {
	ctx := context.Background()
	s := memoryStore(t)
	settings, err := s.Settings(ctx, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	settings.TotalTimeoutSeconds = MaxTotalTimeoutSeconds
	settings, err = s.SaveSettings(ctx, testOwner, settings)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(settings.TotalTimeoutSeconds+10)*time.Second <= 0 {
		t.Fatal("accepted timeout overflows outer duration")
	}
	bad := settings
	bad.TotalTimeoutSeconds = MaxTotalTimeoutSeconds + 1
	if _, err = s.SaveSettings(ctx, testOwner, bad); err == nil {
		t.Fatal("overflowing timeout accepted")
	}
	after, err := s.Settings(ctx, testOwner)
	if err != nil || after != settings {
		t.Fatal("bad timeout changed settings", after, err)
	}
}

func TestStartedTimeUsesFirstExecutedItemAndKeepsWaitingOutOfTiming(t *testing.T) {
	ctx := context.Background()
	s := memoryStore(t)
	id := strings.Repeat("a", 32)
	if _, err := s.CreateReport(ctx, testOwner, createRequest(id)); err != nil {
		t.Fatal(err)
	}
	before, err := s.Report(ctx, testOwner, id)
	if err != nil || before.StartedAtMs != 0 {
		t.Fatal(before, err)
	}
	if err = s.BeginRun(ctx, testOwner, id); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginItem(ctx, testOwner, id, "Q2"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE hub_items SET started_at_ms=? WHERE report_id=?`, before.CreatedAtMs+50000, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.Report(ctx, testOwner, id)
	if err != nil || got.StartedAtMs != before.CreatedAtMs+50000 {
		t.Fatal("queue time counted as execution", got, err)
	}
	raw, _ := json.Marshal(ReportView(got))
	if !strings.Contains(string(raw), `"startedAtMs"`) {
		t.Fatal("derived started time missing from report API")
	}
}

// A failed migration must not leave the running-only index installed without
// the settings upgrade and schema version advancing in the same transaction.
func TestVersionTwoMigrationRollsBackOnSettingsWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Settings(context.Background(), testOwner); err != nil {
		t.Fatal(err)
	}
	stripV8ForMigrationFixture(t, s)
	if _, err = s.db.Exec(`UPDATE hub_settings SET settings_json=json_set(settings_json,'$.concurrency',2); DROP INDEX hub_reports_one_active; CREATE UNIQUE INDEX hub_reports_one_active ON hub_reports((1)) WHERE status IN('queued','running'); CREATE TRIGGER refuse_migration BEFORE UPDATE ON hub_settings BEGIN SELECT RAISE(ABORT,'migration fixture unavailable'); END; PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if reopened, e := Open(path); e == nil {
		reopened.Close()
		t.Fatal("failed migration opened store")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var index string
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatal("failed migration advanced version", version, err)
	}
	if err = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='hub_reports_one_active'`).Scan(&index); err != nil || !strings.Contains(index, "'queued','running'") {
		t.Fatal("failed migration replaced original index", index, err)
	}
}

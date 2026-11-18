package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginalDefaultsMigrationRollsBackSchemaAndSettingsTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Settings(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE hub_settings SET settings_json=json_set(settings_json,'$.timeoutSeconds',120)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE runtime_state`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER isolated_migration_failure BEFORE UPDATE ON hub_settings BEGIN SELECT RAISE(ABORT,'isolated migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("failed migration was accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatal("failed migration changed schema version", version, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='runtime_state'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed migration left a new table", count, err)
	}
}

func TestOriginalDefaultsMigrationFromV2IsTransactionalAndPreservesReports(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	old := settings
	old.TimeoutSeconds = 120
	old.MaxOutputTokens = 4096
	old.Retries = 0
	old.MaxAttempts = 128
	old.Protocol = ""
	if _, err = s.SaveSettings(t.Context(), testOwner, old); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if _, err = s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = s.db.QueryRow(`SELECT snapshot_json FROM hub_reports WHERE report_id=?`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Settings(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeoutSeconds != 360 || got.Retries != 3 || got.MaxOutputTokens != 8192 || got.MaxAttempts != 0 || got.Protocol != "auto" || got.Concurrency != 32 || got.TotalTimeoutSeconds != 3600 {
		t.Fatal("original defaults not restored", got)
	}
	var after string
	if err = s.db.QueryRow(`SELECT snapshot_json FROM hub_reports WHERE report_id=?`, id).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote a paid report snapshot", err)
	}
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	if _, err = s.db.Exec(`INSERT INTO runtime_state(name,value) VALUES('fixture',?)`, []byte(`{"original":true}`)); err != nil {
		t.Fatal("original bank state cannot use existing DB", err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	again, err := s.Settings(t.Context(), testOwner)
	if err != nil || again != got {
		t.Fatal("migration ran again", again, got, err)
	}
}

func TestRetryKeepsOriginalDetectionProtocolAfterSettingsChange(t *testing.T) {
	for _, protocol := range []string{"auto", "", "chat"} {
		t.Run("original-"+protocol, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			settings, err := s.Settings(t.Context(), testOwner)
			if err != nil {
				t.Fatal(err)
			}
			settings.Protocol = protocol
			if _, err = s.SaveSettings(t.Context(), testOwner, settings); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 32)
			if _, err = s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal(err)
			}
			if err = s.BeginRun(t.Context(), testOwner, id); err != nil {
				t.Fatal(err)
			}
			if err = s.BeginItem(t.Context(), testOwner, id, "Q2"); err != nil {
				t.Fatal(err)
			}
			if err = s.SaveItem(t.Context(), testOwner, id, "Q2", "error", map[string]any{"error": "isolated unavailable", "judgment": "unknown"}, 1); err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(t.Context(), testOwner, id, "partial", map[string]any{}, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			settings, err = s.Settings(t.Context(), testOwner)
			if err != nil {
				t.Fatal(err)
			}
			settings.Protocol = "anthropic"
			if _, err = s.SaveSettings(t.Context(), testOwner, settings); err != nil {
				t.Fatal(err)
			}
			if _, err = s.RetryReport(t.Context(), testOwner, RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q2"}}); err != nil {
				t.Fatal(err)
			}
			r, snap, err := s.LoadRun(t.Context(), testOwner, id)
			want := protocol
			if want == "" {
				want = "chat"
			}
			if err != nil || snap.Settings.Protocol != want || r.Protocol != "chat" {
				t.Fatal("retry changed original protocol/binding", snap.Settings.Protocol, r.Protocol, err)
			}
			var original map[string]any
			if err = json.Unmarshal(r.Request, &original); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOriginalCacheCheckboxExecutesUntouchedDefaultAndRespectsExplicitDisable(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	create := func(id string, sections []string) Snapshot {
		t.Helper()
		if _, err := s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedSectionIDs: sections, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
			t.Fatal(err)
		}
		_, snap, err := s.LoadRun(t.Context(), testOwner, id)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	hasCache := func(snap Snapshot) bool {
		for _, q := range snap.Questions {
			if q.Definition.ID == "cache" {
				return true
			}
		}
		return false
	}
	if hasCache(create(strings.Repeat("a", 32), nil)) {
		t.Fatal("cache no longer defaults off")
	}
	if !hasCache(create(strings.Repeat("b", 32), []string{"iq", "cache"})) {
		t.Fatal("original homepage cache checkbox is ineffective")
	}
	q, err := s.Question(t.Context(), testOwner, "cache")
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = false
	if _, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, false); err != nil {
		t.Fatal(err)
	}
	if hasCache(create(strings.Repeat("c", 32), []string{"iq", "cache"})) {
		t.Fatal("explicitly disabled cache was re-enabled")
	}
	q, err = s.Question(t.Context(), testOwner, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteQuestion(t.Context(), testOwner, "cache", q.Revision); err != nil {
		t.Fatal(err)
	}
	if hasCache(create(strings.Repeat("d", 32), []string{"iq", "cache"})) {
		t.Fatal("deleted cache was restored")
	}
}

func TestReviewV2CustomConcurrencyAndTimeoutAreNotMigratedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2-custom.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	settings.Concurrency = 2
	settings.TotalTimeoutSeconds = 600
	if _, err = s.SaveSettings(t.Context(), testOwner, settings); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=2`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Settings(t.Context(), testOwner)
	if err != nil || got.Concurrency != 2 || got.TotalTimeoutSeconds != 600 {
		t.Fatal("schema2 explicit settings were overwritten", got, err)
	}
}

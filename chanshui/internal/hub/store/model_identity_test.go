package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func TestModelIdentityUpgradeAddsOnceAndPreservesFrozenReportsAndFullCatalogue(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identity.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.EnsureScope(t.Context(), testOwner); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec("DELETE FROM hub_questions WHERE question_id='Q10'"); err != nil {
				t.Fatal(err)
			}
			if full {
				for i := 0; i < 23; i++ {
					q := questions.Definition{ID: fmt.Sprintf("custom:%032x", i), Kind: "iq", Title: "已存在自定义题", Enabled: true, Order: 1000 + i, Template: "原题", Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual"}}
					b, _ := json.Marshal(q)
					if _, err = s.db.Exec("INSERT INTO hub_questions VALUES(?,?,?,?,1)", testOwner.UserID, testOwner.WorkspaceID, q.ID, string(b)); err != nil {
						t.Fatal(err)
					}
				}
			}
			id := strings.Repeat("a", 32)
			if _, err = s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.2-sol", SelectedQuestionIDs: []string{"Q2"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal(err)
			}
			var snapshot, settings string
			if err = s.db.QueryRow("SELECT snapshot_json FROM hub_reports WHERE report_id=?", id).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			if err = s.db.QueryRow("SELECT settings_json FROM hub_settings").Scan(&settings); err != nil {
				t.Fatal(err)
			}
			before := map[string]string{}
			rows, err := s.db.Query("SELECT question_id,definition_json FROM hub_questions")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id, raw string
				if err = rows.Scan(&id, &raw); err != nil {
					t.Fatal(err)
				}
				before[id] = raw
			}
			rows.Close()
			if _, err = s.db.Exec("PRAGMA user_version=4"); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			added, err := s.Question(t.Context(), testOwner, "Q10")
			if err != nil {
				t.Fatal("old scope did not get new question", err)
			}
			if added.Enabled == full || added.MaxScore != 5 {
				t.Fatal("full catalogue not respected", added)
			}
			for id, want := range before {
				var got string
				if err = s.db.QueryRow("SELECT definition_json FROM hub_questions WHERE question_id=?", id).Scan(&got); err != nil || got != want {
					t.Fatal("existing definition changed", id, err)
				}
			}
			var got string
			if err = s.db.QueryRow("SELECT snapshot_json FROM hub_reports WHERE report_id=?", id).Scan(&got); err != nil || got != snapshot {
				t.Fatal("old snapshot changed", err)
			}
			if err = s.db.QueryRow("SELECT settings_json FROM hub_settings").Scan(&got); err != nil || got != settings {
				t.Fatal("settings changed", err)
			}
			q, err := s.Question(t.Context(), testOwner, "Q2")
			if err != nil {
				t.Fatal(err)
			}
			q.Title = "仍可编辑"
			if _, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, false); err != nil {
				t.Fatal("old question became uneditable", err)
			}
			if _, err = s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: strings.Repeat("b", 32), TargetID: "1", Model: "gpt-6.2-sol", SelectedSectionIDs: []string{"iq"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal("catalogue became unrunnable", err)
			}
			if err = s.DeleteQuestion(t.Context(), testOwner, "Q10", added.Revision); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Question(t.Context(), testOwner, "Q10"); err == nil {
				t.Fatal("deleted question resurrected")
			}
			s.Close()
		})
	}
}

func TestModelIdentityUpgradeFailureRollsBackVersionAndAddedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureScope(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DELETE FROM hub_questions WHERE question_id='Q10'", "PRAGMA user_version=4", `CREATE TRIGGER reject_identity BEFORE INSERT ON hub_questions WHEN NEW.question_id='Q10' BEGIN SELECT RAISE(ABORT,'isolated migration failure'); END`} {
		if _, err = s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("failed identity migration was accepted")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("failed upgrade changed version", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM hub_questions WHERE question_id='Q10'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed upgrade left rows", count, err)
	}
}
func TestModelIdentityConcurrentUpgradeAndDisabledQuestionStayStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureScope(t.Context(), testOwner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM hub_questions WHERE question_id='Q10'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			opened, e := Open(path)
			if e == nil {
				e = opened.Close()
			}
			errs <- e
		}()
	}
	for i := 0; i < 2; i++ {
		if err = <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Question(t.Context(), testOwner, "Q10")
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = false
	if _, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	q, err = s.Question(t.Context(), testOwner, "Q10")
	if err != nil || q.Enabled {
		t.Fatal("disabled question re-enabled", q, err)
	}
}

func TestCacheRestoringDefaultStillRequiresExplicitEnable(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q, err := s.Question(t.Context(), testOwner, "cache")
	if err != nil {
		t.Fatal(err)
	}
	q.Enabled = true
	q, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, true)
	if err != nil || q.Enabled {
		t.Fatal("restore no longer returns disabled definition", q, err)
	}
	create := func(id string) bool {
		t.Helper()
		if _, err := s.CreateReport(t.Context(), testOwner, CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.2-sol", SelectedSectionIDs: []string{"iq", "cache"}}); err != nil {
			t.Fatal(err)
		}
		_, snap, err := s.LoadRun(t.Context(), testOwner, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range snap.Questions {
			if g.Definition.Kind == "cache" {
				return true
			}
		}
		return false
	}
	if create(strings.Repeat("d", 32)) {
		t.Fatal("homepage checkbox bypassed explicit restored disable")
	}
	q.Enabled = true
	if _, err = s.SaveQuestion(t.Context(), testOwner, q, q.Revision, false); err != nil {
		t.Fatal(err)
	}
	if !create(strings.Repeat("e", 32)) {
		t.Fatal("explicit enable did not restore cache execution")
	}
}

package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func seedLegacyKnowledge(t *testing.T, s *Store, owner Owner) {
	t.Helper()
	if err := s.EnsureScope(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM hub_questions WHERE user_id=? AND workspace_id=? AND json_extract(definition_json,'$.kind')='knowledge'`, owner.UserID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	for _, p := range knowledge.Anchors {
		q, ok := questions.Preset("knowledge:" + p.ID)
		if !ok {
			t.Fatal("legacy preset missing", p.ID)
		}
		raw, err := json.Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("INSERT INTO hub_questions VALUES(?,?,?,?,1)", owner.UserID, owner.WorkspaceID, q.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventRotationPreservesCustomizationAndFrozenQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotation.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	edited := Owner{"user-A", "edited"}
	disabled := Owner{"user-A", "disabled"}
	deleted := Owner{"user-A", "deleted"}
	added := Owner{"user-A", "added"}
	for _, owner := range []Owner{testOwner, edited, disabled, deleted, added} {
		seedLegacyKnowledge(t, s, owner)
	}
	iq, err := s.Question(t.Context(), testOwner, "Q2")
	if err != nil {
		t.Fatal(err)
	}
	iq.Title = "非知识题的自定义标题"
	if _, err = s.SaveQuestion(t.Context(), testOwner, iq, iq.Revision, false); err != nil {
		t.Fatal(err)
	}
	const changeID = "knowledge:mamdani"
	for _, owner := range []Owner{edited, disabled} {
		q, err := s.Question(t.Context(), owner, changeID)
		if err != nil {
			t.Fatal(err)
		}
		if owner == edited {
			q.Title = "已编辑的旧事件"
		} else {
			q.Enabled = false
		}
		if _, err = s.SaveQuestion(t.Context(), owner, q, q.Revision, false); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DeleteQuestion(t.Context(), deleted, changeID, 1); err != nil {
		t.Fatal(err)
	}
	q, _ := questions.Preset(changeID)
	q.ID = "custom:" + strings.Repeat("a", 32)
	if _, err = s.SaveQuestion(t.Context(), added, q, 0, false); err != nil {
		t.Fatal(err)
	}
	reportID := strings.Repeat("1", 32)
	if _, err = s.CreateReport(t.Context(), testOwner, CreateRequest{
		ReportID: reportID, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{changeID},
		Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"},
	}); err != nil {
		t.Fatal(err)
	}
	var originalSnapshot string
	if err = s.db.QueryRow("SELECT snapshot_json FROM hub_reports WHERE report_id=?", reportID).Scan(&originalSnapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	openErrors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app, openErr := Open(path)
			if openErr == nil {
				app.Close()
			}
			openErrors <- openErr
		}()
	}
	wg.Wait()
	close(openErrors)
	for openErr := range openErrors {
		if openErr != nil {
			t.Fatal("concurrent opening failed", openErr)
		}
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for owner, want := range map[Owner]int{testOwner: 24, edited: 16, disabled: 16, deleted: 15, added: 17} {
		page, err := upgraded.ListQuestions(t.Context(), owner, Filters{Kind: "knowledge", Limit: 32})
		if err != nil || len(page.Items) != want {
			t.Fatalf("owner %v has %d events, want %d: %v", owner, len(page.Items), want, err)
		}
	}
	var savedSnapshot string
	if err = upgraded.db.QueryRow("SELECT snapshot_json FROM hub_reports WHERE report_id=?", reportID).Scan(&savedSnapshot); err != nil || savedSnapshot != originalSnapshot {
		t.Fatal("queued report snapshot changed", err)
	}
	if q, err := upgraded.Question(t.Context(), edited, changeID); err != nil || q.Title != "已编辑的旧事件" {
		t.Fatal("edited knowledge definition changed", err)
	}
	if q, err := upgraded.Question(t.Context(), testOwner, "Q2"); err != nil || q.Title != iq.Title || q.Revision != 2 {
		t.Fatal("non-knowledge change was rewritten", err)
	}
	if _, err = upgraded.Question(t.Context(), testOwner, changeID); err == nil {
		t.Fatal("retired question still available in default scope")
	}
	q, err = upgraded.Question(t.Context(), edited, changeID)
	if err != nil {
		t.Fatal(err)
	}
	if q, err = upgraded.SaveQuestion(t.Context(), edited, q, q.Revision, true); err != nil || q.Title != "马姆达尼当选纽约市长" {
		t.Fatal("retained question could not restore its own baseline", err)
	}
}

func TestEventRotationRollsBackPartialWritesAndCanRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyKnowledge(t, s, testOwner)
	_, err = s.db.Exec(`PRAGMA user_version=3;
		CREATE TRIGGER reject_second_event BEFORE INSERT ON hub_questions
		WHEN NEW.question_id='knowledge:icc_2025_india'
		BEGIN SELECT RAISE(ABORT,'isolated migration failure'); END`)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("failed upgrade was accepted")
	} else if !strings.Contains(err.Error(), "isolated migration failure") {
		t.Fatal("reported wrong cause", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal("failed upgrade advanced SQLite version", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM hub_questions WHERE question_id LIKE 'knowledge:%'").Scan(&count); err != nil || count != 16 {
		t.Fatal("failed upgrade changed the old bank", count, err)
	}
	if _, err = db.Exec("DROP TRIGGER reject_second_event"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatal("upgrade did not succeed on retry", version, err)
	}
}

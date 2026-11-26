package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestKnowledgeV6RotatesOnlyUntouchedDefaults(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "knowledge-defaults-v6.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const oldID = "knowledge:tariffs_2025_april"
	const newID = "knowledge:us_2025_tariff_pause"
	old, ok := questions.Preset(oldID)
	if !ok {
		t.Fatal("missing original preset", oldID)
	}
	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	edited := Owner{testOwner.UserID, "edited"}
	disabled := Owner{testOwner.UserID, "disabled"}
	deleted := Owner{testOwner.UserID, "deleted"}
	duplicate := Owner{testOwner.UserID, "duplicate"}
	for _, owner := range []Owner{testOwner, edited, disabled, deleted, duplicate} {
		if err := s.EnsureScope(ctx, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM hub_questions
			WHERE user_id=? AND workspace_id=? AND question_id IN (?,?)`, owner.UserID, owner.WorkspaceID, oldID, newID); err != nil {
			t.Fatal(err)
		}
		if owner != deleted {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO hub_questions VALUES(?,?,?,?,1)`, owner.UserID, owner.WorkspaceID, oldID, string(oldRaw)); err != nil {
				t.Fatal(err)
			}
		}
	}
	const customTitle = "保留我的事件题"
	if _, err := s.db.ExecContext(ctx, `UPDATE hub_questions SET revision=2, definition_json=json_set(definition_json,'$.title',?)
		WHERE user_id=? AND workspace_id=? AND question_id=?`, customTitle, edited.UserID, edited.WorkspaceID, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE hub_questions SET revision=2, definition_json=json_set(definition_json,'$.enabled',json('false'))
		WHERE user_id=? AND workspace_id=? AND question_id=?`, disabled.UserID, disabled.WorkspaceID, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO hub_questions VALUES(?,?,?,json_set(?,'$.id',?,'$.title',?),4)`,
		duplicate.UserID, duplicate.WorkspaceID, newID, string(oldRaw), newID, customTitle); err != nil {
		t.Fatal(err)
	}
	reportID := strings.Repeat("e", 32)
	if _, err := s.CreateReport(ctx, testOwner, CreateRequest{
		ReportID: reportID, TargetID: "fixture", Model: "fixture", SelectedQuestionIDs: []string{oldID},
		Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"},
	}); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	if err := s.db.QueryRowContext(ctx, "SELECT snapshot_json FROM hub_reports WHERE report_id=?", reportID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	stripV8ForMigrationFixture(t, s)
	if _, err := s.db.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if q, err := upgraded.Question(ctx, testOwner, newID); err != nil || !q.Enabled || q.Order != old.Order || q.Revision != 1 {
		t.Fatalf("v5 default was not replaced within its slot: %+v (%v)", q, err)
	}
	for _, owner := range []Owner{testOwner, deleted} {
		if _, err := upgraded.Question(ctx, owner, oldID); err == nil {
			t.Fatal("retired or deleted ID still exists", owner)
		}
	}
	if _, err := upgraded.Question(ctx, deleted, newID); err == nil {
		t.Fatal("migration resurrected a deleted default")
	}
	if q, err := upgraded.Question(ctx, edited, oldID); err != nil || q.Title != customTitle || q.Revision != 2 {
		t.Fatalf("edited default changed: %+v (%v)", q, err)
	}
	if q, err := upgraded.Question(ctx, disabled, oldID); err != nil || q.Enabled || q.Revision != 2 {
		t.Fatalf("disabled default changed: %+v (%v)", q, err)
	}
	if q, err := upgraded.Question(ctx, duplicate, newID); err != nil || q.Title != customTitle || q.Revision != 4 {
		t.Fatalf("existing destination changed: %+v (%v)", q, err)
	}
	if _, err := upgraded.Question(ctx, duplicate, oldID); err != nil {
		t.Fatal("migration removed the old question after a destination collision", err)
	}
	var after string
	if err := upgraded.db.QueryRowContext(ctx, "SELECT snapshot_json FROM hub_reports WHERE report_id=?", reportID).Scan(&after); err != nil || after != snapshot {
		t.Fatal("report snapshot changed", err)
	}
}

func TestKnowledgeV6SupplementsPreserveWorkspacesAndUseFortyQuestionLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge-v6.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const january = "knowledge:minnesota_2026_pretti"
	const february = "knowledge:wbd_2026_bid"
	const march = "knowledge:iea_2026_reserves"
	const oldID = "knowledge:ao_2026_women"
	old, _ := questions.Preset(oldID)
	oldRaw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, owner := range []Owner{testOwner, {"user-A", "another-workspace"}} {
		if err := s.EnsureScope(ctx, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO hub_questions VALUES(?,?,?,?,1)`,
			owner.UserID, owner.WorkspaceID, oldID, string(oldRaw)); err != nil {
			t.Fatal(err)
		}
	}
	stripV8ForMigrationFixture(t, s)
	for _, statement := range []string{
		`DELETE FROM hub_questions WHERE question_id IN ('` + january + `','` + february + `','` + march + `','Q10','knowledge:ao_2025_zverev','knowledge:afcon_2026_goal')`,
		`UPDATE hub_questions SET definition_json=json_set(definition_json,'$.title','用户改过的澳网题'),revision=2 WHERE question_id='knowledge:ao_2026_women'`,
		`INSERT INTO hub_questions SELECT user_id,workspace_id,'` + february + `',json_set(definition_json,'$.id','` + february + `','$.enabled',json('false'),'$.title','已编辑的同编号题'),3 FROM hub_questions WHERE question_id='knowledge:ao_2026_women'`,
		`PRAGMA user_version=5`,
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("expected current version: %d (%v)", version, err)
	}
	for _, owner := range []Owner{testOwner, {"user-A", "another-workspace"}} {
		for _, id := range []string{january, march} {
			want, _ := questions.Preset(id)
			got, err := upgraded.Question(ctx, owner, id)
			if err != nil || !got.Enabled || got.Revision != 1 || got.Order != want.Order {
				t.Fatalf("missing default %s in %v: %+v (%v)", id, owner, got, err)
			}
		}
		if q, err := upgraded.Question(ctx, owner, february); err != nil || q.Title != "已编辑的同编号题" || q.Enabled || q.Revision != 3 {
			t.Fatalf("edited same-ID question changed: %+v (%v)", q, err)
		}
		if q, err := upgraded.Question(ctx, owner, "knowledge:ao_2026_women"); err != nil || q.Title != "用户改过的澳网题" || q.Revision != 2 {
			t.Fatalf("edited old question changed: %+v (%v)", q, err)
		}
		for _, id := range []string{"knowledge:ao_2025_zverev", "Q10"} {
			if _, err := upgraded.Question(ctx, owner, id); err == nil {
				t.Fatal("migration resurrected a deleted question", id)
			}
		}
	}
	page, err := upgraded.ListQuestions(ctx, testOwner, Filters{Kind: "knowledge", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, row := range page.Items {
		if row.Enabled && !questions.IsModelAddition(row.ID) {
			enabled++
		}
	}
	q, _ := questions.Preset(january)
	for i := enabled; i <= 40; i++ {
		q.ID = fmt.Sprintf("custom:%032x", i)
		_, err := upgraded.SaveQuestion(ctx, testOwner, q, 0, false)
		if (err == nil) != (i < 40) {
			t.Fatalf("adding question %d: %v", i+1, err)
		}
	}
	id := strings.Repeat("a", 32)
	if _, err := upgraded.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedSectionIDs: []string{"knowledge"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
		t.Fatalf("40 enabled knowledge questions could not create a new report: %v", err)
	}
	if err := upgraded.DeleteQuestion(ctx, testOwner, january, 1); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Question(ctx, testOwner, january); err == nil {
		t.Fatal("reopen put a deleted supplement back")
	}
	if q, err := reopened.Question(ctx, testOwner, march); err != nil || q.Revision != 1 {
		t.Fatalf("reopen reinserted the saved supplement: %+v (%v)", q, err)
	}
}

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
	stripV8ForMigrationFixture(t, s)
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
	for owner, want := range map[Owner]int{testOwner: 27 + len(questions.ModelAdditionIDs()), edited: 19 + len(questions.ModelAdditionIDs()), disabled: 19 + len(questions.ModelAdditionIDs()), deleted: 18 + len(questions.ModelAdditionIDs()), added: 20 + len(questions.ModelAdditionIDs())} {
		page, err := upgraded.ListQuestions(t.Context(), owner, Filters{Kind: "knowledge", Limit: 50})
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
	stripV8ForMigrationFixture(t, s)
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

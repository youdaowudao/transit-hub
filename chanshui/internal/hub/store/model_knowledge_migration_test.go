package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestV7AddsModelEventsWithoutChangingExistingData(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v7.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureScope(ctx, testOwner); err != nil {
		t.Fatal(err)
	}
	reportID := strings.Repeat("7", 32)
	if _, err = s.CreateReport(ctx, testOwner, CreateRequest{ReportID: reportID, TargetID: "fixture", Model: "fixture", SelectedQuestionIDs: []string{"knowledge:superbowl_2026_score"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
		t.Fatal(err)
	}
	var snapshot, summary string
	if err = s.db.QueryRow(`SELECT snapshot_json,summary_json FROM hub_reports WHERE report_id=?`, reportID).Scan(&snapshot, &summary); err != nil {
		t.Fatal(err)
	}
	const edited = "knowledge:bp_2026_chair_ouster"
	const disabled = "knowledge:anthropic_2026_spacex_compute"
	const oldDeleted = "knowledge:superbowl_2026_score"
	const newlyAdded = "knowledge:milano_2026_men_skate"
	if _, err = s.db.Exec(`UPDATE hub_questions SET revision=2,definition_json=json_set(definition_json,'$.title','用户修改') WHERE question_id=?`, edited); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE hub_questions SET revision=2,definition_json=json_set(definition_json,'$.enabled',json('false')) WHERE question_id=?`, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM hub_questions WHERE question_id IN (?,?)`, oldDeleted, newlyAdded); err != nil {
		t.Fatal(err)
	}
	stripV8ForMigrationFixture(t, s)
	if _, err = s.db.Exec(`PRAGMA user_version=6`); err != nil {
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
	if q, err := s.Question(ctx, testOwner, edited); err != nil || q.Title != "用户修改" || q.Revision != 2 {
		t.Fatalf("已编辑题被更改：%+v %v", q, err)
	}
	if q, err := s.Question(ctx, testOwner, disabled); err != nil || q.Enabled || q.Revision != 2 {
		t.Fatalf("已停用题被更改：%+v %v", q, err)
	}
	if _, err = s.Question(ctx, testOwner, oldDeleted); err == nil {
		t.Fatal("v6 已删除题被恢复")
	}
	if _, err = s.Question(ctx, testOwner, newlyAdded); err != nil {
		t.Fatal("明确要求补入的冬奥花滑题缺失", err)
	}
	var afterSnapshot, afterSummary string
	if err = s.db.QueryRow(`SELECT snapshot_json,summary_json FROM hub_reports WHERE report_id=?`, reportID).Scan(&afterSnapshot, &afterSummary); err != nil {
		t.Fatal(err)
	}
	if afterSnapshot != snapshot || afterSummary != summary {
		t.Fatal("升级改变了旧报告或快照")
	}
}

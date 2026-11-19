package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func memoryStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var testOwner = Owner{"user-A", "workspace-A"}

func TestEveryPresetCanBeReadDeletedAndNeverReseededAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = s.EnsureScope(ctx, testOwner); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, preset := range questions.Presets() {
		q, e := s.Question(ctx, testOwner, preset.ID)
		if e != nil {
			t.Fatalf("preset missing %s: %v", preset.ID, e)
		}
		if q.Template != preset.Template {
			t.Fatal("hidden or changed preset")
		}
		if e = s.DeleteQuestion(ctx, testOwner, q.ID, q.Revision); e != nil {
			t.Fatal(e)
		}
		count++
	}
	if count != len(questions.Presets()) {
		t.Fatal("not all presets deleted")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.EnsureScope(ctx, testOwner); e != nil {
		t.Fatal(e)
	}
	page, e := s.ListQuestions(ctx, testOwner, Filters{})
	if e != nil || len(page.Items) != 0 {
		t.Fatalf("deleted presets reappeared: %+v %v", page, e)
	}
}
func customQuestion(n int) questions.Definition {
	return questions.Definition{ID: fmt.Sprintf("custom:%032x", n), Title: fmt.Sprintf("自定义%d", n), Kind: "iq", Enabled: false, Order: n, Template: strings.Repeat("长题正文", 650), MaxScore: 0, Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual", ReferenceAnswer: "供人工查看"}}
}
func TestQuestionSummaryPaginationDoesNotLoadLongDisabledDefinitions(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	for n := 1; n <= 300; n++ {
		if _, e := s.SaveQuestion(ctx, testOwner, customQuestion(n), 0, false); e != nil {
			t.Fatal(e)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, e := s.ListQuestions(ctx, testOwner, Filters{Kind: "iq", Enabled: boolPtr(false), Cursor: cursor, Limit: 20})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > 64<<10 || strings.Contains(string(raw), "长题正文") {
			t.Fatal("summary contains bodies or exceeds cap")
		}
		for _, q := range page.Items {
			if seen[q.ID] {
				t.Fatal("duplicate pagination")
			}
			seen[q.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 300 {
		t.Fatalf("lost disabled questions %d", len(seen))
	}
	q, e := s.Question(ctx, testOwner, customQuestion(90).ID)
	if e != nil || q.Template != customQuestion(90).Template {
		t.Fatal("detail missing")
	}
	q.Title = "已修改"
	saved, e := s.SaveQuestion(ctx, testOwner, q, q.Revision, false)
	if e != nil || saved.Revision != 2 {
		t.Fatal(e)
	}
	if _, e = s.SaveQuestion(ctx, testOwner, q, q.Revision, false); e == nil {
		t.Fatal("stale revision overwrote newer edit")
	}
	if e = s.DeleteQuestion(ctx, testOwner, q.ID, 1); e == nil {
		t.Fatal("stale delete succeeded")
	}
	if e = s.DeleteQuestion(ctx, testOwner, q.ID, 2); e != nil {
		t.Fatal(e)
	}
}
func boolPtr(v bool) *bool { return &v }
func createRequest(id string) CreateRequest {
	return CreateRequest{ReportID: id, TargetID: "account:1", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Target: Target{AccountID: "1", AccountName: "测试账号", GroupName: "分组A", BaseURL: "https://fixture.invalid", Protocol: "chat"}}
}
func TestReportSnapshotOwnerIdempotenceAndSingleActivity(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	req := createRequest(strings.Repeat("a", 32))
	r, e := s.CreateReport(ctx, testOwner, req)
	if e != nil {
		t.Fatal(e)
	}
	if r.MemoryLimitMiB != 256 || r.Status != "queued" {
		t.Fatal(r)
	}
	again, e := s.CreateReport(ctx, testOwner, req)
	if e != nil || again.ReportID != r.ReportID {
		t.Fatal("idempotent replay failed", e)
	}
	changed := req
	changed.Model = "other"
	if _, e = s.CreateReport(ctx, testOwner, changed); e == nil {
		t.Fatal("same ID changed request accepted")
	}
	other := Owner{"other", testOwner.WorkspaceID}
	if _, e = s.CreateReport(ctx, other, req); e == nil {
		t.Fatal("cross owner accepted")
	}
	if _, e = s.Report(ctx, other, r.ReportID); e == nil {
		t.Fatal("cross owner read")
	}
	if _, e = s.Image(ctx, other, r.ReportID, "vision"); e == nil {
		t.Fatal("cross owner image")
	}
	q, e := s.Question(ctx, testOwner, "Q2")
	if e != nil {
		t.Fatal(e)
	}
	q.Template = "修改后的题"
	q.Judge.Type = "manual"
	q.MaxScore = 0
	if _, e = s.SaveQuestion(ctx, testOwner, q, q.Revision, false); e != nil {
		t.Fatal(e)
	}
	_, snap, e := s.LoadRun(ctx, testOwner, r.ReportID)
	if e != nil || strings.Contains(snap.Questions[0].Prompt, "修改后的题") {
		t.Fatal("running snapshot changed", e)
	}
	if e = s.DeleteQuestion(ctx, testOwner, "Q2", 2); e != nil {
		t.Fatal(e)
	}
	items, e := s.ListItems(ctx, testOwner, r.ReportID, Filters{})
	if e != nil || len(items.Items) != 1 || items.Items[0].Definition.Title != "洗车路径" {
		t.Fatal("deletion damaged old snapshot", e)
	}
	next := createRequest(strings.Repeat("b", 32))
	next.SelectedQuestionIDs = []string{"Q1"}
	if _, e = s.CreateReport(ctx, testOwner, next); e != nil {
		t.Fatal("second waiting report rejected", e)
	}
	if e = s.BeginRun(ctx, testOwner, r.ReportID); e != nil {
		t.Fatal(e)
	}
	if e = s.BeginRun(ctx, testOwner, next.ReportID); e == nil {
		t.Fatal("second running report allowed")
	}
}
func TestConcurrentDifferentIDsCanWaitButOnlyOneRunsAndTerminalCASPreserved(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	if e := s.EnsureScope(ctx, testOwner); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := []string{}
	for n := 1; n <= 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r, e := s.CreateReport(ctx, testOwner, createRequest(fmt.Sprintf("%032x", n)))
			if e == nil {
				mu.Lock()
				ids = append(ids, r.ReportID)
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if len(ids) != 2 {
		t.Fatalf("accepted %d tasks", len(ids))
	}
	id := ids[0]
	if e := s.BeginRun(ctx, testOwner, id); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginRun(ctx, testOwner, id); e == nil {
		t.Fatal("replayed running task")
	}
	if e := s.BeginRun(ctx, testOwner, ids[1]); e == nil {
		t.Fatal("another running report accepted")
	}
	if e := s.Finish(ctx, testOwner, id, "done", map[string]any{"iqScore": 0}, map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishInterrupted(ctx, testOwner, id, map[string]any{"message": "迟到取消"}); e != nil {
		t.Fatal(e)
	}
	r, e := s.Report(ctx, testOwner, id)
	if e != nil || r.Status != "done" {
		t.Fatal("terminal overwritten", e)
	}
}
func TestHistoryTwoNaturalDaysKeepsZeroFailuresAndArchive(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	fixed := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return fixed }
	defer func() { Now = oldNow }()
	ids := []string{}
	for n := 0; n < 3; n++ {
		req := createRequest(fmt.Sprintf("%032x", n+1))
		r, e := s.CreateReport(ctx, testOwner, req)
		if e != nil {
			t.Fatal(e)
		}
		status := []string{"done", "partial", "interrupted"}[n]
		if e = s.Finish(ctx, testOwner, r.ReportID, status, map[string]any{"iqScore": 0}, map[string]any{}); e != nil {
			t.Fatal(e)
		}
		when := fixed.AddDate(0, 0, -n).UnixMilli()
		if _, e = s.db.ExecContext(ctx, "UPDATE hub_reports SET created_at_ms=? WHERE report_id=?", when, r.ReportID); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, r.ReportID)
	}
	page, e := s.ListReports(ctx, testOwner, Filters{Scope: "sidebar"})
	if e != nil || len(page.Items) != 2 {
		t.Fatalf("sidebar %+v %v", page, e)
	}
	for _, r := range page.Items {
		if r.ReportID == ids[2] {
			t.Fatal("old report in sidebar")
		}
	}
	archive, e := s.ListReports(ctx, testOwner, Filters{Scope: "archive"})
	if e != nil || len(archive.Items) != 3 {
		t.Fatal("archive lost old report", e)
	}
	if _, e = s.Report(ctx, testOwner, ids[2]); e != nil {
		t.Fatal(e)
	}
}
func TestSettingsCASAndHigherSchemaRefusesMigration(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	v, e := s.Settings(ctx, testOwner)
	if e != nil {
		t.Fatal(e)
	}
	v.MemoryLimitMiB = 128
	saved, e := s.SaveSettings(ctx, testOwner, v)
	if e != nil || saved.Revision != 2 {
		t.Fatal(e)
	}
	if _, e = s.SaveSettings(ctx, testOwner, v); e == nil {
		t.Fatal("stale settings accepted")
	}
	saved.MemoryLimitMiB = 385
	if _, e = s.SaveSettings(ctx, testOwner, saved); e == nil {
		t.Fatal("oversized memory accepted")
	}
	if _, e = s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1)); e != nil {
		t.Fatal(e)
	}
	if e = s.migrate(ctx); e == nil {
		t.Fatal("newer schema opened for write")
	}
}

func TestDeletedPresetCannotBeRestoredWithOldRevisionOrRecreatedAtSameID(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	q, e := s.Question(ctx, testOwner, "Q2")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteQuestion(ctx, testOwner, q.ID, q.Revision); e != nil {
		t.Fatal(e)
	}
	for _, restore := range []bool{false, true} {
		for _, rev := range []int{0, 1} {
			if _, e = s.SaveQuestion(ctx, testOwner, q, rev, restore); e == nil {
				t.Fatal("deleted preset ID resurrected and revision ABA possible", restore, rev)
			}
		}
	}
	copy := questions.Clone(q)
	copy.ID = "custom:" + strings.Repeat("e", 32)
	copy.Judge.Type = "manual"
	copy.MaxScore = 0
	if _, e = s.SaveQuestion(ctx, testOwner, copy, 0, false); e != nil {
		t.Fatal("new copy should be allowed", e)
	}
}
func TestReportListsProjectLargeDetailsAndPaginateWithin64KiBWithoutLoss(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	expected := map[string]bool{}
	for i := 1; i <= 40; i++ {
		req := createRequest(fmt.Sprintf("%032x", i))
		req.Target.AccountName = strings.Repeat("长名字", 300)
		r, e := s.CreateReport(ctx, testOwner, req)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Finish(ctx, testOwner, r.ReportID, "done", map[string]any{"iqScore": 0, "totalScore": 0, "complete": true, "largeEvidence": strings.Repeat("x", 40000)}, map[string]any{"message": strings.Repeat("具体错误", 20000), "step": "large-fixture"}); e != nil {
			t.Fatal(e)
		}
		expected[r.ReportID] = true
	}
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		page, e := s.ListReports(ctx, testOwner, Filters{Scope: "archive", Cursor: cursor, Limit: 50})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > 64<<10 || strings.Contains(string(raw), "largeEvidence") {
			t.Fatal("unbounded history projection", len(raw))
		}
		for _, r := range page.Items {
			if seen[r.ReportID] {
				t.Fatal("duplicate page")
			}
			seen[r.ReportID] = true
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 40 {
			t.Fatal("pagination loop")
		}
	}
	if len(seen) != len(expected) || pages < 2 {
		t.Fatal("history records lost or missing byte pagination", len(seen), pages)
	}
}
func TestLargeRequestComparedInsideHelperNeverReturnedInReportView(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	req := createRequest(strings.Repeat("f", 32))
	large := map[string]any{"selectedQuestionIds": strings.Repeat("Q2", 50000), "targetId": req.TargetID, "model": req.Model}
	req.Request, _ = json.Marshal(large)
	r, e := s.CreateReport(ctx, testOwner, req)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckRequest(ctx, testOwner, r.ReportID, req.Request); e != nil {
		t.Fatal(e)
	}
	view := ReportView(r)
	raw, _ := json.Marshal(view)
	if len(raw) > 64<<10 || strings.Contains(string(raw), "request") || strings.Contains(string(raw), strings.Repeat("Q2", 100)) {
		t.Fatal("large original request returned", len(raw))
	}
	large["model"] = "changed"
	changed, _ := json.Marshal(large)
	if e = s.CheckRequest(ctx, testOwner, r.ReportID, changed); e == nil {
		t.Fatal("changed request accepted")
	}
	if e = s.CheckRequest(ctx, Owner{"other", testOwner.WorkspaceID}, r.ReportID, req.Request); e == nil {
		t.Fatal("cross owner request compared as readable")
	}
}

func requireCurrentRevision(t *testing.T, err error, want int) {
	t.Helper()
	var detail *Error
	if !errors.As(err, &detail) || detail.Status != 409 || detail.CurrentRevision == nil || *detail.CurrentRevision != want {
		t.Fatalf("conflict must include actual revision %d: %+v", want, err)
	}
	raw, e := json.Marshal(detail)
	if e != nil || !strings.Contains(string(raw), fmt.Sprintf(`"currentRevision":%d`, want)) {
		t.Fatalf("revision missing from serialized conflict: %s %v", raw, e)
	}
}
func TestCASConflictsCarryActualCurrentRevisionWithoutChangingValues(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	settings, e := s.Settings(ctx, testOwner)
	if e != nil {
		t.Fatal(e)
	}
	settings.MemoryLimitMiB = 128
	saved, e := s.SaveSettings(ctx, testOwner, settings)
	if e != nil {
		t.Fatal(e)
	}
	stale := settings
	stale.MemoryLimitMiB = 384
	_, e = s.SaveSettings(ctx, testOwner, stale)
	requireCurrentRevision(t, e, saved.Revision)
	actual, e := s.Settings(ctx, testOwner)
	if e != nil || actual.MemoryLimitMiB != 128 || actual.Revision != saved.Revision {
		t.Fatal("stale settings changed original value", actual, e)
	}
	q, e := s.Question(ctx, testOwner, "Q2")
	if e != nil {
		t.Fatal(e)
	}
	updated := questions.Clone(q)
	updated.Title = "保存后的题名"
	newer, e := s.SaveQuestion(ctx, testOwner, updated, q.Revision, false)
	if e != nil {
		t.Fatal(e)
	}
	q.Title = "迟到编辑不能覆盖"
	_, e = s.SaveQuestion(ctx, testOwner, q, q.Revision, false)
	requireCurrentRevision(t, e, newer.Revision)
	e = s.DeleteQuestion(ctx, testOwner, q.ID, q.Revision)
	requireCurrentRevision(t, e, newer.Revision)
	actualQuestion, e := s.Question(ctx, testOwner, q.ID)
	if e != nil || actualQuestion.Title != "保存后的题名" || actualQuestion.Revision != newer.Revision {
		t.Fatal("stale edit or delete changed current question", actualQuestion, e)
	}
	if e = s.DeleteQuestion(ctx, testOwner, q.ID, newer.Revision); e != nil {
		t.Fatal(e)
	}
	_, e = s.SaveQuestion(ctx, testOwner, q, q.Revision, false)
	requireCurrentRevision(t, e, 0)
	if !strings.Contains(e.Error(), "已被删除") {
		t.Fatal("missing deleted explanation", e)
	}
	e = s.DeleteQuestion(ctx, testOwner, q.ID, newer.Revision)
	requireCurrentRevision(t, e, 0)
	if _, e = s.Question(ctx, testOwner, q.ID); e == nil {
		t.Fatal("deleted question resurrected")
	}
	invalid := Fail(422, "输入不正确", "question")
	raw, _ := json.Marshal(invalid)
	if invalid.CurrentRevision != nil || strings.Contains(string(raw), "currentRevision") {
		t.Fatal("unconfirmed revision included for non-conflict", string(raw))
	}
}

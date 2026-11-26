package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func batchFixture(t *testing.T, s *Store, n int) (BatchRequest, []BatchTarget) {
	t.Helper()
	settings, err := s.Settings(t.Context(), testOwner)
	if err != nil {
		t.Fatal(err)
	}
	r := BatchRequest{BatchID: strings.Repeat("f", 32), SettingsRevision: settings.Revision,
		SelectedSectionIDs: []string{"iq"}, SelectedQuestionIDs: []string{"Q2"}}
	var targets []BatchTarget
	for i := 0; i < n; i++ {
		targetID := fmt.Sprintf("sub2api:1:%d", i)
		r.Tasks = append(r.Tasks, BatchTask{ReportID: fmt.Sprintf("%032x", i+1), TargetID: targetID, GroupID: "g1", Model: "Model",
			ExpectedContext: BatchExpectedContext{testOwner.WorkspaceID, targetID, "g1", "https://fixture.invalid", "chat"}})
		targets = append(targets, BatchTarget{targetID, "g1", Target{fmt.Sprint(i), strings.Repeat("账", 100), strings.Repeat("组", 100), "https://fixture.invalid", "chat"}})
	}
	return r, targets
}

func TestBatchProjectionNoDetailAndFrozenOrder(t *testing.T) {
	s := memoryStore(t)
	r, targets := batchFixture(t, s, 3)
	targets[2].Target.Protocol, r.Tasks[2].ExpectedContext.Protocol = "anthropic", "anthropic"
	accepted, err := s.CreateBatch(t.Context(), testOwner, r, targets)
	if err != nil {
		t.Fatal(err)
	}
	id := accepted.ReportIDs[0]
	raw := map[string]any{
		"complete": true, "iqScore": nil, "iqMax": 2, "totalScore": nil,
		"total": map[string]any{"complete": true, "parts": []any{map[string]any{
			"key": "cache", "applicable": true, "known": false, "points": 0, "max_points": 3, "contribution": 0,
			"evidence": "excluded-evidence",
		}}},
		"knowledge": map[string]any{"status": "unknown", "label": "待确认", "correct": 0, "total": 1, "rows": []string{"excluded-knowledge-row"}},
		"verdict":   map[string]any{"timing": map[string]any{"started_at": "2026-11-26T01:02:03Z", "finished_at": "2026-11-26T01:02:05Z", "total_ms": 2000, "last_run_ms": 2000, "runs": 1}, "injection": "excluded-injection"},
		"prefix":    "excluded-prefix",
	}
	if err = s.Finish(t.Context(), testOwner, id, "done", raw, map[string]any{"message": strings.Repeat("详", 300)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE hub_items SET result_json=? WHERE report_id=?", `{"answer":"excluded-answer `+strings.Repeat("x", 1<<20)+`"}`, id); err != nil {
		t.Fatal(err)
	}
	detail, err := s.ReadBatch(t.Context(), testOwner, BatchFilters{BatchID: r.BatchID, View: "results", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.MemberStates) != 3 || len(detail.Items) != 1 || detail.NextOffset == nil || *detail.NextOffset != 1 {
		t.Fatalf("unexpected page: %+v", detail)
	}
	if len(detail.Sections) != 1 || detail.Sections[0].ID != "iq" || detail.Sections[0].Count != 1 || detail.Items[0].ReportID != id {
		t.Fatal("frozen scope/order changed")
	}
	states := detail.MemberStates
	if !states[0].SummaryComplete || states[0].Timing == nil || states[0].Timing.LastRunMs != 2000 ||
		states[0].EtaKey == "" || states[0].EtaKey != states[1].EtaKey || states[0].EtaKey == states[2].EtaKey {
		t.Fatalf("timing or configuration grouping changed: %+v", states)
	}
	b, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"excluded-", "https://fixture.invalid", "expectedContext", "definition", "answer", "snapshot", "questionIds", strings.Repeat("账", 97), strings.Repeat("详", 257)} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("batch projection contains excluded field: %s", forbidden)
		}
	}
	var compact struct {
		IQScore    *float64 `json:"iqScore"`
		TotalScore *float64 `json:"totalScore"`
		Total      struct {
			Complete bool `json:"complete"`
			Parts    []struct {
				Key        string `json:"key"`
				Applicable bool   `json:"applicable"`
				Known      *bool  `json:"known"`
			} `json:"parts"`
		} `json:"total"`
	}
	if err = json.Unmarshal(detail.Items[0].Summary, &compact); err != nil {
		t.Fatal(err)
	}
	if compact.IQScore != nil || compact.TotalScore != nil || !compact.Total.Complete ||
		len(compact.Total.Parts) != 1 || !compact.Total.Parts[0].Applicable ||
		compact.Total.Parts[0].Known == nil || *compact.Total.Parts[0].Known {
		t.Fatalf("projection changed score meaning: %s", detail.Items[0].Summary)
	}
	hints := map[string]BatchStateHint{accepted.ReportIDs[1]: {Phase: "queued"}}
	detail, err = s.ReadBatch(t.Context(), testOwner, BatchFilters{BatchID: r.BatchID, View: "progress", Offset: 1, Status: "queued", StateHints: hints})
	if err != nil || len(detail.MemberStates) != 3 || len(detail.Items) != 1 || detail.Items[0].ReportID != accepted.ReportIDs[1] ||
		detail.NextOffset != nil || string(detail.Items[0].Summary) != "{}" {
		t.Fatalf("derived filter/page not independent of full membership: %+v %v", detail, err)
	}
	hints[accepted.ReportIDs[1]] = BatchStateHint{RetryID: strings.Repeat("a", 32), Phase: "queued"}
	detail, err = s.ReadBatch(t.Context(), testOwner, BatchFilters{BatchID: r.BatchID, Status: "unknown", StateHints: hints})
	if err != nil || len(detail.Items) != 2 {
		t.Fatalf("orphan/mismatched waiting records treated as confirmed: %+v %v", detail, err)
	}
	// A whole-batch save intent holds even a SQL-terminal member until
	// the executor confirms the relevant state write.
	detail, err = s.ReadBatch(t.Context(), testOwner, BatchFilters{BatchID: r.BatchID, Status: "paused",
		StateHints: map[string]BatchStateHint{"*": {Phase: "paused"}}})
	if err != nil || len(detail.Items) != 3 {
		t.Fatalf("batch-wide unresolved recovery was omitted: %+v %v", detail, err)
	}
	if _, err = s.ReadBatch(t.Context(), Owner{"other", testOwner.WorkspaceID}, BatchFilters{BatchID: r.BatchID}); err == nil {
		t.Fatal("batch leaked across owners")
	}
	list, err := s.ListBatches(t.Context(), testOwner, Filters{Scope: "sidebar"})
	if err != nil || len(list.Items) != 1 || len(list.Items[0].MemberStates) != 3 || list.Items[0].MemberStates[0].Timing != nil {
		t.Fatalf("lifecycle-only list failed: %+v %v", list, err)
	}
	single, err := s.ListReports(t.Context(), testOwner, Filters{Scope: "sidebar"})
	if err != nil || len(single.Items) != 0 {
		t.Fatalf("batch reports leak to sidebar: %+v %v", single, err)
	}
	all, err := s.ListReports(t.Context(), testOwner, Filters{})
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("default legacy history changed: %+v %v", all, err)
	}
}

func TestBatchFinalizeAndRetryGateAfterConfirmedExit(t *testing.T) {
	s := memoryStore(t)
	ctx := t.Context()
	r, targets := batchFixture(t, s, 2)
	a, err := s.CreateBatch(ctx, testOwner, r, targets)
	if err != nil {
		t.Fatal(err)
	}
	first, second := a.ReportIDs[0], a.ReportIDs[1]
	if err = s.BeginRun(ctx, testOwner, first); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, testOwner, first, "partial", map[string]any{"complete": false}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelQueued(ctx, testOwner, second, false); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(ctx, testOwner, first)
	if err != nil || report.BatchID != r.BatchID || report.BatchFirstFinishedAtMs != 0 {
		t.Fatalf("premature first completion: %+v %v", report, err)
	}
	retry := RetryRequest{ReportID: second, RetryID: strings.Repeat("a", 32), ItemIDs: []string{"Q2"}}
	if _, err = s.RetryReport(ctx, testOwner, retry); err == nil {
		t.Fatal("retry accepted before initial batch exit")
	}
	if err = s.FinishInterrupted(ctx, testOwner, first, map[string]any{"message": "do not replace summary"}, true); err != nil {
		t.Fatal(err)
	}
	report, err = s.Report(ctx, testOwner, first)
	if err != nil || report.Status != "partial" || report.BatchFirstFinishedAtMs <= 0 {
		t.Fatalf("first completion not recorded after real exit: %+v %v", report, err)
	}
	finished := report.BatchFirstFinishedAtMs
	if _, err = s.RetryReport(ctx, testOwner, retry); err != nil {
		t.Fatal("retry after first completion blocked:", err)
	}
	if err = s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	report, err = s.Report(ctx, testOwner, second)
	if err != nil || report.Status != "interrupted" || report.BatchFirstFinishedAtMs != finished {
		t.Fatalf("restart changed first batch completion or started retry: %+v %v", report, err)
	}
}

func TestBatchUnregisteredRecoveryAtomicAndIdempotent(t *testing.T) {
	s := memoryStore(t)
	r, targets := batchFixture(t, s, 2)
	a, err := s.CreateBatch(t.Context(), testOwner, r, targets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER fail_batch_finalization BEFORE UPDATE ON hub_batches BEGIN SELECT RAISE(ABORT,'fixture finalization failed'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InterruptBatchUnstarted(t.Context(), testOwner, r.BatchID, true); err == nil {
		t.Fatal("batch recovery acknowledged despite failed completion write")
	}
	for _, id := range a.ReportIDs {
		report, err := s.Report(t.Context(), testOwner, id)
		if err != nil || report.Status != "queued" || report.BatchFirstFinishedAtMs != 0 {
			t.Fatalf("partial recovery write leaked: %+v %v", report, err)
		}
	}
	if _, err = s.db.Exec("DROP TRIGGER fail_batch_finalization"); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.InterruptBatchUnstarted(t.Context(), testOwner, r.BatchID, true)
	if err != nil || recovered.FirstFinishedAtMs <= 0 || len(recovered.MemberStates) != 2 {
		t.Fatalf("unregistered recovery did not finish: %+v %v", recovered, err)
	}
	for _, member := range recovered.MemberStates {
		if member.Status != "interrupted" {
			t.Fatalf("unregistered member not interrupted: %+v", member)
		}
	}
	replayed, err := s.InterruptBatchUnstarted(t.Context(), testOwner, r.BatchID, true)
	if err != nil || replayed.FirstFinishedAtMs != recovered.FirstFinishedAtMs {
		t.Fatalf("repeat after unknown response changed completion: %+v %v", replayed, err)
	}
	settings, _ := s.Settings(t.Context(), testOwner)
	settings.Concurrency--
	if _, err = s.SaveSettings(t.Context(), testOwner, settings); err != nil {
		t.Fatal(err)
	}
	r.SelectedSectionIDs = []string{"iq", "iq"}
	r.SelectedQuestionIDs = []string{"Q2", "Q2"}
	r.Tasks[0].Model = " Model "
	replayed, err = s.CreateBatch(t.Context(), testOwner, r, nil)
	if err != nil || replayed.FirstFinishedAtMs != recovered.FirstFinishedAtMs || len(replayed.ReportIDs) != 2 {
		t.Fatalf("normalized same-ID replay rejected or recreated: %+v %v", replayed, err)
	}
}

func TestBatchV7MigrationRollbackAndConcurrentOpen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "batch-migration.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 32)
	if _, err = s.CreateReport(ctx, testOwner, createRequest(id)); err != nil {
		t.Fatal(err)
	}
	var snapshot string
	if err = s.db.QueryRow("SELECT snapshot_json FROM hub_reports WHERE report_id=?", id).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	stripV8ForMigrationFixture(t, s)
	if _, err = s.db.Exec("PRAGMA user_version=7; CREATE INDEX hub_reports_owner_batch ON hub_reports(report_id)"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if upgraded, err := Open(path); err == nil {
		upgraded.Close()
		t.Fatal("migration claimed success after index conflict")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, tables, columns int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("rollback did not keep version 7: %d %v", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='hub_batches'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("rollback leaked a new table: %d %v", tables, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('hub_reports') WHERE name='batch_id'").Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("rollback leaked batch_id: %d %v", columns, err)
	}
	if _, err = db.Exec("DROP INDEX hub_reports_owner_batch"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			upgraded, err := Open(path)
			if err == nil {
				var got int
				err = upgraded.db.QueryRow("PRAGMA user_version").Scan(&got)
				if err == nil && got != 8 {
					err = fmt.Errorf("concurrent migration opened version %d", got)
				}
				upgraded.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var after string
	var parent sql.NullString
	if err = upgraded.db.QueryRow("SELECT snapshot_json,batch_id FROM hub_reports WHERE report_id=?", id).Scan(&after, &parent); err != nil || parent.Valid || snapshot != after {
		t.Fatal("migration changed original report", err)
	}
}

func TestBatchPageByteLimitPreservesFrozenOffsets(t *testing.T) {
	s := memoryStore(t)
	r, targets := batchFixture(t, s, 32)
	for i := range r.Tasks {
		targetID := fmt.Sprintf("sub2api:%s:%d", strings.Repeat("x", 480), i)
		groupID := strings.Repeat("g", 180)
		r.Tasks[i].TargetID, r.Tasks[i].GroupID, r.Tasks[i].Model = targetID, groupID, strings.Repeat("模", 80)
		r.Tasks[i].ExpectedContext.TargetID, r.Tasks[i].ExpectedContext.GroupID = targetID, groupID
		targets[i].TargetID, targets[i].GroupID = targetID, groupID
		targets[i].Target.AccountID = strings.Repeat("a", 180)
	}
	accepted, err := s.CreateBatch(t.Context(), testOwner, r, targets)
	if err != nil {
		t.Fatal(err)
	}
	createJSON, _ := json.Marshal(accepted)
	if len(createJSON) > 8<<10 {
		t.Fatal("create response beyond 8KiB")
	}
	seen := []string{}
	pages := 0
	for offset := 0; ; {
		detail, err := s.ReadBatch(t.Context(), testOwner, BatchFilters{BatchID: r.BatchID, Offset: offset, View: "results", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(detail.MemberStates) != 32 || len(detail.Items) == 0 {
			t.Fatal("byte trimming lost all-member states or produced empty page")
		}
		base := detail
		base.Items = nil
		baseJSON, _ := json.Marshal(base)
		rowsJSON, _ := json.Marshal(detail.Items)
		raw, _ := json.Marshal(detail)
		if len(baseJSON) > 24<<10 || len(rowsJSON) > 36<<10 || len(raw)+4096 > 64<<10 {
			t.Fatalf("page exceeds bytes: base=%d rows=%d full=%d", len(baseJSON), len(rowsJSON), len(raw))
		}
		for _, row := range detail.Items {
			seen = append(seen, row.ReportID)
			if row.Model != strings.Repeat("模", 80) || row.TargetID != r.Tasks[len(seen)-1].TargetID || len([]rune(row.AccountName)) != 96 {
				t.Fatal("display truncation affected identity or model fields")
			}
		}
		if detail.NextOffset == nil {
			break
		}
		if *detail.NextOffset <= offset {
			t.Fatal("pagination did not advance")
		}
		offset = *detail.NextOffset
	}
	if pages < 2 || strings.Join(seen, ",") != strings.Join(accepted.ReportIDs, ",") {
		t.Fatalf("byte pages lost order or did not shrink: pages=%d seen=%d", pages, len(seen))
	}
}

func TestBatchRejectsMissingScopeAndReportCollisionWithoutChanges(t *testing.T) {
	s := memoryStore(t)
	r, targets := batchFixture(t, s, 1)
	foreign := Owner{"unread", "unread"}
	if _, err := s.CreateBatch(t.Context(), foreign, r, targets); err == nil {
		t.Fatal("batch created without saved settings revision")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM hub_settings WHERE user_id='unread'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed batch seeded workspace", count, err)
	}
	stale := r
	stale.SettingsRevision++
	if _, err := s.CreateBatch(t.Context(), testOwner, stale, targets); err == nil {
		t.Fatal("stale revision accepted")
	}
	badQuestion := r
	badQuestion.SelectedQuestionIDs = []string{"Q2", "nonexistent-question"}
	if _, err := s.CreateBatch(t.Context(), testOwner, badQuestion, targets); err == nil {
		t.Fatal("unknown selected question silently ignored")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM hub_batches").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected range or revision wrote a batch", count, err)
	}
	request := createRequest(r.Tasks[0].ReportID)
	if _, err := s.CreateReport(t.Context(), testOwner, request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBatch(t.Context(), testOwner, r, targets); err == nil {
		t.Fatal("existing single report reused as batch member")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM hub_batches").Scan(&count); err != nil || count != 0 {
		t.Fatal("report collision left a batch behind", count, err)
	}
	report, err := s.Report(t.Context(), testOwner, request.ReportID)
	if err != nil || report.BatchID != "" {
		t.Fatal("existing single report changed by collision", report, err)
	}
}

func TestBatchListByteLimitAndStartupRecovery(t *testing.T) {
	s := memoryStore(t)
	ctx := t.Context()
	r, targets := batchFixture(t, s, 32)
	// A bounded list cursor must work for the max task count in many batches.
	const batches = 28
	for i := 1; i <= batches; i++ {
		r.BatchID = fmt.Sprintf("%032x", 10000+i)
		for j := range r.Tasks {
			r.Tasks[j].ReportID = fmt.Sprintf("%032x", i*100+j)
		}
		if _, err := s.CreateBatch(ctx, testOwner, r, targets); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BeginRun(ctx, testOwner, r.Tasks[0].ReportID); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	pages := 0
	for cursor := ""; ; {
		list, err := s.ListBatches(ctx, testOwner, Filters{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		raw, _ := json.Marshal(list)
		if len(raw)+2048 > 64<<10 || len(list.Items) == 0 {
			t.Fatalf("list page beyond limit or did not advance: %d", len(raw))
		}
		for _, b := range list.Items {
			if seen[b.BatchID] || b.FirstFinishedAtMs <= 0 || len(b.MemberStates) != 32 {
				t.Fatalf("batch duplicated, startup did not finish batch, or status truncated: %+v", b)
			}
			seen[b.BatchID] = true
			for _, member := range b.MemberStates {
				if member.Status != "interrupted" {
					t.Fatalf("startup restored a nonterminal task: %+v", member)
				}
			}
		}
		if list.NextCursor == "" {
			break
		}
		if cursor == list.NextCursor {
			t.Fatal("list cursor did not advance")
		}
		cursor = list.NextCursor
	}
	if len(seen) != batches || pages < 2 {
		t.Fatalf("list lost records or failed byte limit: batches=%d pages=%d", len(seen), pages)
	}
}

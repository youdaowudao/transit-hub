package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
)

func insertCrossTestReport(t *testing.T, s *Store, owner Owner, id, account, model, status string, at int64, summary string) {
	t.Helper()
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO hub_reports
(report_id,user_id,workspace_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,request_json,snapshot_json,summary_json)
VALUES (?,?,?,?,?,?,'','','https://example.invalid','chat',?,?,? ,?,'{}','{}',?)`, id, owner.UserID, owner.WorkspaceID, account, account, account, model, at, at, status, summary)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCrossCheckReadsReportItemsAndNearestOtherAccounts(t *testing.T) {
	s := newCrossTestStore(t, ":memory:")
	const source = "88888888888888888888888888888888"
	const at = 1_800_000_000_000
	insertCrossTestReport(t, s, testOwner, source, "source", "gpt-6.2-sol", "done", at, `{}`)
	for i := 0; i < 24; i++ {
		id, account := fmt.Sprintf("%032x", i+20), fmt.Sprintf("account-%d", i)
		insertCrossTestReport(t, s, testOwner, id, account, "GPT-6.2-SOL", "partial", at+int64(i/2+1), `{}`)
	}
	window := prefixcheck.PeerWindow.Milliseconds()
	for i, tc := range []struct {
		owner           Owner
		account, model  string
		status, summary string
		delta           int64
	}{
		{Owner{UserID: "other", WorkspaceID: testOwner.WorkspaceID}, "another", "gpt-6.2-sol", "done", `{}`, 0},
		{Owner{UserID: testOwner.UserID, WorkspaceID: "other"}, "another", "gpt-6.2-sol", "done", `{}`, 0},
		{testOwner, "source", "gpt-6.2-sol", "done", `{}`, 0},
		{testOwner, "another", " gpt-6.2-sol", "done", `{}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "queued", `{}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "running", `{}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "cancelled", `{}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "interrupted", `{}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "done", `{"singleAsk":true}`, 0},
		{testOwner, "another", "gpt-6.2-sol", "done", `{}`, window + 1},
	} {
		insertCrossTestReport(t, s, tc.owner, fmt.Sprintf("f%031x", i), tc.account, tc.model, tc.status, at+tc.delta, tc.summary)
	}
	for i, item := range []struct{ id, section, question, result string }{
		{"a", "injection", `{"definition":{"template":"{{sample}}"}}`, `{"evidence":{"reliable":false,"points":[{"n":1,"total":8,"input":3}]},"padding":"` + strings.Repeat("x", 4<<20) + `"}`},
		{"model", "iq", `{"definition":{"judge":{"type":"model_identity"}}}`, `{"judgment":{"status":"known","earned":0,"max":5}}`},
		{"later", "iq", `{"definition":{"judge":{"type":"model_identity"}}}`, `{"judgment":{"status":"known","earned":5,"max":5}}`},
	} {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO hub_items(report_id,item_id,section_id,order_index,status,question_json,result_json)
VALUES(?,?,?,?,'done',?,?)`, source, item.id, item.section, i*10, item.question, item.result)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.CrossCheckData(t.Context(), testOwner, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Injections) != 1 || string(got.Injections[0].Points) != `[{"n":1,"total":8,"input":3}]` ||
		got.Injections[0].Reliable == nil || *got.Injections[0].Reliable || got.Identity == nil || got.Identity.ItemID != "model" {
		t.Fatalf("correct injection and earliest self-report not projected")
	}
	if len(got.Items) != 3 || got.Items[2].OrderIndex != 20 || len(got.Peers) != 20 {
		t.Fatalf("ordering or peer limit incorrect: %d items, %d peers", len(got.Items), len(got.Peers))
	}
	for i, p := range got.Peers {
		if p.ReportID != fmt.Sprintf("%032x", i+20) {
			t.Fatalf("wrong candidate or order at index %d: %s", i, p.ReportID)
		}
	}
	page, err := s.ListItems(t.Context(), testOwner, source, Filters{Limit: 5})
	if err != nil || len(page.Items) != 1 || page.Items[0].OrderIndex != 0 || page.NextCursor == "" {
		t.Fatalf("large first item changed normal pagination: %v", err)
	}
	page, err = s.ListItems(t.Context(), testOwner, source, Filters{Limit: 5, Cursor: page.NextCursor})
	if err != nil || len(page.Items) != 2 || page.Items[1].OrderIndex != 20 {
		t.Fatalf("remaining items lost stored order index: %v", err)
	}
	_, err = s.CrossCheckData(t.Context(), Owner{UserID: "other", WorkspaceID: testOwner.WorkspaceID}, source)
	var detail *Error
	if !errors.As(err, &detail) || detail.Status != 404 || detail.Message != "报告不存在或不属于当前工作区" {
		t.Fatalf("other owner: %v", err)
	}
}

func newCrossTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCrossCheckUsesOneSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	reader, writer := newCrossTestStore(t, path), newCrossTestStore(t, path)
	const id = "99999999999999999999999999999999"
	insertCrossTestReport(t, reader, testOwner, id, "account", "fixture", "done", 2000, `{"knowledge":{"rows":[{"id":"old","kind":"know","happened_at":"2025-01-01"}]}}`)
	got, err := reader.crossCheckData(t.Context(), testOwner, id, func() {
		_, err := writer.db.ExecContext(t.Context(), `UPDATE hub_reports SET status='partial', updated_at_ms=3000,
summary_json='{"knowledge":{"rows":[{"id":"new","kind":"know","happened_at":"2025-01-01"}]}}' WHERE report_id=?`, id)
		if err != nil {
			t.Fatal(err)
		}
	})
	if err != nil || got.Report.Status != "done" || got.Report.UpdatedAtMs != 2000 ||
		len(got.Report.KnowledgeRows) != 1 || got.Report.KnowledgeRows[0].ID != "old" {
		t.Fatalf("cross-check combined different snapshots: %+v, %v", got.Report, err)
	}
	next, err := reader.CrossCheckData(t.Context(), testOwner, id)
	if err != nil || next.Report.Status != "partial" || next.Report.UpdatedAtMs != 3000 ||
		len(next.Report.KnowledgeRows) != 1 || next.Report.KnowledgeRows[0].ID != "new" {
		t.Fatalf("next cross-check missed committed change: %+v, %v", next.Report, err)
	}
}

func TestListItemsDefaultsOnlyNonTextInjectionTemplate(t *testing.T) {
	s := newCrossTestStore(t, ":memory:")
	const id = "99999999999999999999999999999999"
	insertCrossTestReport(t, s, testOwner, id, "account", "fixture", "done", 2000, `{}`)
	const result = `{"evidence":{"points":[{"n":1,"input":7,"total":7}]}}`
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO hub_items
	(report_id,item_id,section_id,order_index,status,question_json,result_json)
	VALUES(?,'injection','injection',0,'done','{}',?)`, id, result)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{`5`, `true`, `{}`, `[]`, `null`} {
		question := `{"definition":{"kind":"injection","template":` + literal + `}}`
		if _, err := s.db.ExecContext(t.Context(), `UPDATE hub_items SET question_json=? WHERE report_id=?`, question, id); err != nil {
			t.Fatal(err)
		}
		page, err := s.ListItems(t.Context(), testOwner, id, Filters{})
		if err != nil || len(page.Items) != 1 || page.Items[0].Definition.Template != "" {
			t.Fatalf("literal %s did not use the default template: %+v, %v", literal, page, err)
		}
		item := page.Items[0]
		_, injection, _ := prefixcheck.Extract(prefixcheck.StoredItem{Kind: item.Definition.Kind, Template: item.Definition.Template, Result: item.Result})
		if injection == nil || len(injection.Points) != 1 {
			t.Fatalf("point A was not usable with default template for %s", literal)
		}
		var saved string
		if err := s.db.QueryRowContext(t.Context(), `SELECT question_json FROM hub_items WHERE report_id=?`, id).Scan(&saved); err != nil || saved != question {
			t.Fatalf("read changed stored template: %v", err)
		}
	}
	for _, data := range []struct{ section, question string }{
		{"injection", `{"definition":{"kind":"injection","template":5,"order":"bad"}}`},
		{"iq", `{"definition":{"kind":"iq","template":5}}`},
	} {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE hub_items SET section_id=?, question_json=? WHERE report_id=?`, data.section, data.question, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListItems(t.Context(), testOwner, id, Filters{}); err == nil {
			t.Fatalf("unrelated malformed question was accepted: %s", data.question)
		}
	}
}

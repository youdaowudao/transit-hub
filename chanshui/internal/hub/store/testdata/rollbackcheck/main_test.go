package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func setupRollback(t *testing.T) (*store.Store, string, store.Owner) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollback.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path, store.Owner{UserID: "rollback-check", WorkspaceID: "test-only"}
}

func insertRollbackRow(t *testing.T, s *store.Store, owner store.Owner, id, status string, updated int64, summary string) {
	t.Helper()
	if _, err := s.RuntimeDB().ExecContext(t.Context(), `INSERT INTO hub_reports
(report_id,user_id,workspace_id,target_id,account_id,account_name,group_id,group_name,base_url,protocol,model,created_at_ms,updated_at_ms,status,request_json,snapshot_json,summary_json)
VALUES (?,?,?,'account','account','fixture','','','https://example.invalid','chat','fixture',100,?,?,'{}','{}',?)`,
		id, owner.UserID, owner.WorkspaceID, updated, status, summary); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackOrdinaryAndOversizedReports(t *testing.T) {
	s, path, owner := setupRollback(t)
	const small, large = "10000000000000000000000000000000", "20000000000000000000000000000000"
	insertRollbackRow(t, s, owner, small, "done", 100, `{"prefix":{"version":1}}`)
	var out bytes.Buffer
	code, err := run(t.Context(), path, 200, &out)
	if err != nil || code != 0 || !strings.Contains(out.String(), "over=false") || !strings.Contains(out.String(), "stopped=0 reports=1 measured=1 failed=0 over=0") {
		t.Fatalf("normal report check code=%d err=%v: %s", code, err, out.String())
	}
	summary := `{"detail":"` + strings.Repeat("x", 63<<10-1000) + `","prefix":{"detail":"` + strings.Repeat("x", 4000) + `"}}`
	insertRollbackRow(t, s, owner, large, "done", 100, summary)
	out.Reset()
	code, err = run(t.Context(), path, 200, &out)
	if err != nil || code != 1 || !strings.Contains(out.String(), "over=true") || !strings.Contains(out.String(), "stopped=0 reports=2 measured=2 failed=0 over=1") {
		t.Fatalf("over-limit report check code=%d err=%v: %s", code, err, out.String())
	}
	r, err := s.Report(t.Context(), owner, large)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(store.ReportView(r))
	if err != nil || len(body) > oldLimit {
		t.Fatalf("new read view = %d bytes: %v", len(body), err)
	}
}

func TestRollbackReportsStoppedAndReadFailure(t *testing.T) {
	s, path, owner := setupRollback(t)
	for _, tc := range []struct {
		id, status string
		at         int64
	}{
		{"30000000000000000000000000000000", "cancelled", 210},
		{"40000000000000000000000000000000", "interrupted", 220},
		{"50000000000000000000000000000000", "queued", 120},
		{"60000000000000000000000000000000", "cancelled", 199},
	} {
		insertRollbackRow(t, s, owner, tc.id, tc.status, tc.at, `{}`)
	}
	var out bytes.Buffer
	code, err := run(t.Context(), path, 200, &out)
	if err != nil || code != 1 || strings.Count(out.String(), "ROLLBACK_CHECK_STOPPED") != 3 ||
		strings.Contains(out.String(), "60000000000000000000000000000000") || !strings.Contains(out.String(), "stopped=3 reports=0 measured=0 failed=0 over=0") {
		t.Fatalf("interruption check code=%d err=%v: %s", code, err, out.String())
	}
	const missing = "70000000000000000000000000000000"
	result := check(t.Context(), s, []Target{{ID: missing, Owner: owner}})
	if result.Failed != 1 || result.Measured != 0 || result.safe() || len(result.Lines) != 1 ||
		!strings.Contains(result.Lines[0], "report="+missing+" error=") {
		t.Fatalf("missing data should block rollback: %+v", result)
	}
}

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBatchCreateAtomicIdempotentAndScoped(t *testing.T) {
	s := memoryStore(t)
	ctx := context.Background()
	settings, err := s.Settings(ctx, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	// Marshal through the frozen wire shape rather than relying on implementation-specific field names.
	raw := map[string]any{
		"batchId":             strings.Repeat("b", 32),
		"settingsRevision":    settings.Revision,
		"selectedSectionIds":  []string{"iq"},
		"selectedQuestionIds": []string{"Q2"},
		"tasks": []any{
			map[string]any{"reportId": strings.Repeat("c", 32), "targetId": "account:1", "groupId": "g1", "model": "model-A", "expectedContext": map[string]any{}},
			map[string]any{"reportId": strings.Repeat("d", 32), "targetId": "account:1", "groupId": "g1", "model": "model-B", "expectedContext": map[string]any{}},
		},
	}
	requestJSON, _ := json.Marshal(raw)
	var req BatchRequest
	if err = json.Unmarshal(requestJSON, &req); err != nil {
		t.Fatal(err)
	}
	targets := []BatchTarget{{TargetID: "account:1", GroupID: "g1", Target: Target{AccountID: "1", AccountName: "fixture", GroupName: "Group", BaseURL: "https://fixture.invalid", Protocol: "chat"}}}
	if _, err = s.db.Exec("CREATE TRIGGER reject_second_batch_report BEFORE INSERT ON hub_reports WHEN NEW.report_id='" + strings.Repeat("d", 32) + "' BEGIN SELECT RAISE(ABORT,'test write failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateBatch(ctx, testOwner, req, targets); err == nil {
		t.Fatal("partial create acknowledged")
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := s.db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"hub_batches", "hub_reports", "hub_items"} {
		if n := count("SELECT count(*) FROM " + table); n != 0 {
			t.Fatalf("rollback leaked %s rows: %d", table, n)
		}
	}
	if _, err = s.db.Exec("DROP TRIGGER reject_second_batch_report"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateBatch(ctx, testOwner, req, targets); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateBatch(ctx, testOwner, req, targets); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if count("SELECT count(*) FROM hub_batches") != 1 || count("SELECT count(*) FROM hub_reports") != 2 || count("SELECT count(*) FROM hub_items") != 2 {
		t.Fatal("replay changed frozen report count")
	}
	tasks := raw["tasks"].([]any)
	tasks[1].(map[string]any)["model"] = "different"
	requestJSON, _ = json.Marshal(raw)
	if err = json.Unmarshal(requestJSON, &req); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateBatch(ctx, testOwner, req, targets); err == nil {
		t.Fatal("same id accepted different request")
	}
	if _, err = s.CreateBatch(ctx, Owner{"foreign", "foreign"}, req, targets); err == nil {
		t.Fatal("cross owner id reused")
	}
}

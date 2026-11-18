package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func retryFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s := memoryStore(t)
	ctx := t.Context()
	id := strings.Repeat("a", 32)
	if _, e := s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}}); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginRun(ctx, testOwner, id); e != nil {
		t.Fatal(e)
	}
	if e := s.BeginItem(ctx, testOwner, id, "Q2"); e != nil {
		t.Fatal(e)
	}
	if e := s.RecordAttempt(ctx, testOwner, id, "Q2"); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveItem(ctx, testOwner, id, "Q2", "error", map[string]any{"error": "fixture", "judgment": map[string]any{"status": "unknown"}}, 1); e != nil {
		t.Fatal(e)
	}
	if e := s.Finish(ctx, testOwner, id, "partial", map[string]any{"knowledge": map[string]any{"note": "keep-observations"}, "attempts": 1}, nil); e != nil {
		t.Fatal(e)
	}
	return s, id
}
func TestRetryRejectsWrongOwnerStaleOperationAndInvalidSelectionAtomically(t *testing.T) {
	s, id := retryFixture(t)
	ctx := t.Context()
	first := RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q2"}}
	for _, ids := range [][]string{nil, {""}, {"missing"}} {
		r := first
		r.ItemIDs = ids
		if _, e := s.RetryReport(ctx, testOwner, r); e == nil {
			t.Fatal("invalid retry accepted", ids)
		}
	}
	if _, e := s.RetryReport(ctx, Owner{"other", testOwner.WorkspaceID}, first); e == nil {
		t.Fatal("foreign retry accepted")
	}
	before, _ := s.Report(ctx, testOwner, id)
	if before.Status != "partial" {
		t.Fatal("rejection changed report")
	}
	if _, e := s.RetryReport(ctx, testOwner, first); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RetryReport(ctx, testOwner, first); e != nil {
		t.Fatal("same operation rejected while queued", e)
	}
	changed := first
	changed.ItemIDs = []string{"missing"}
	if _, e := s.RetryReport(ctx, testOwner, changed); e == nil {
		t.Fatal("same id different body accepted")
	}
	if e := s.CancelQueued(ctx, testOwner, id); e != nil {
		t.Fatal(e)
	}
	second := RetryRequest{ReportID: id, RetryID: strings.Repeat("c", 32), ExpectedRetryID: first.RetryID, ItemIDs: []string{"Q2"}}
	r, e := s.RetryReport(ctx, testOwner, second)
	if e != nil {
		t.Fatal(e)
	}
	if r.Retry.AttemptsBefore != 1 || r.StartedAtMs != 0 {
		t.Fatal("cancel lost cumulative count/time", r)
	}
	if _, e = s.RetryReport(ctx, testOwner, first); e == nil {
		t.Fatal("old retry replay accepted after new round")
	}
	if e = s.FinishInterrupted(ctx, testOwner, id, map[string]any{"message": "fixture interruption"}); e != nil {
		t.Fatal(e)
	}
	third := RetryRequest{ReportID: id, RetryID: strings.Repeat("d", 32), ExpectedRetryID: second.RetryID, ItemIDs: []string{"Q2"}}
	r, e = s.RetryReport(ctx, testOwner, third)
	if e != nil {
		t.Fatal(e)
	}
	var summary map[string]any
	_ = json.Unmarshal(r.Summary.Summary, &summary)
	if summary["knowledge"].(map[string]any)["note"] != "keep-observations" || summary["attempts"] != float64(1) {
		t.Fatal("cancel/interruption discarded summary", summary)
	}
}

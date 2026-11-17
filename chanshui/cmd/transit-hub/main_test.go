package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/hub/wire"
)

func TestReadReportComparesLargeRequestButAlwaysReturnsBoundedSummary(t *testing.T) {
	s, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	o := store.Owner{UserID: "u", WorkspaceID: "w"}
	raw, _ := json.Marshal(map[string]any{"targetId": "1", "model": "fixture", "selectedQuestionIds": strings.Repeat("Q2", 50000)})
	id := strings.Repeat("a", 32)
	_, e = s.CreateReport(context.Background(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}, Request: raw})
	if e != nil {
		t.Fatal(e)
	}
	for _, check := range []bool{false, true} {
		args := map[string]any{"reportId": id}
		if check {
			args["request"] = json.RawMessage(raw)
		}
		a, _ := json.Marshal(args)
		value, _, e := query(context.Background(), s, wire.Envelope{Operation: "read-report", Owner: o, Args: a}, wire.Continue{})
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(value)
		if len(b) > 64<<10 || strings.Contains(string(b), `"request"`) || strings.Contains(string(b), strings.Repeat("Q2", 100)) {
			t.Fatal("full request returned to Hub", len(b))
		}
	}
	args, _ := json.Marshal(map[string]any{"reportId": id, "request": map[string]any{"model": "changed"}})
	_, _, e = query(context.Background(), s, wire.Envelope{Operation: "read-report", Owner: o, Args: args}, wire.Continue{})
	if e == nil {
		t.Fatal("changed request accepted")
	}
	var detail *store.Error
	if !errors.As(e, &detail) || detail.Status != 409 {
		t.Fatal("expected precise conflict", e)
	}
	_, _, e = query(context.Background(), s, wire.Envelope{Operation: "read-report", Owner: store.Owner{UserID: "other", WorkspaceID: "w"}, Args: args}, wire.Continue{})
	if !errors.As(e, &detail) || detail.Status != 404 {
		t.Fatal("cross-owner report exposed", e)
	}
}

func TestCancelQueuedOperationIsStrictOwnerScopedAndDoesNotStartRun(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner := store.Owner{UserID: "queue-cli", WorkspaceID: "isolated"}
	id := strings.Repeat("a", 32)
	if _, err = s.CreateReport(t.Context(), owner, store.CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture", SelectedQuestionIDs: []string{"Q2"}}); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"reportId": id})
	other := store.Owner{UserID: "other", WorkspaceID: "isolated"}
	if _, _, err = query(t.Context(), s, wire.Envelope{Operation: "cancel-queued", Owner: other, Args: args}, wire.Continue{}); err == nil {
		t.Fatal("cancel exposed foreign report")
	}
	value, _, err := query(t.Context(), s, wire.Envelope{Operation: "cancel-queued", Owner: owner, Args: args}, wire.Continue{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !strings.Contains(string(raw), `"status":"cancelled"`) || strings.Contains(string(raw), `"request"`) {
		t.Fatal("invalid cancelled report response", string(raw))
	}
	if err = s.BeginRun(t.Context(), owner, id); err == nil {
		t.Fatal("cancelled report could start")
	}
	extra, _ := json.Marshal(map[string]any{"reportId": id, "credential": "not-an-argument"})
	if _, _, err = query(t.Context(), s, wire.Envelope{Operation: "cancel-queued", Owner: owner, Args: extra}, wire.Continue{}); err == nil {
		t.Fatal("cancel accepted arbitrary wire fields")
	}
}

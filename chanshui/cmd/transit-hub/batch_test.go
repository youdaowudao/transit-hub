package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/hub/wire"
)

func TestBatchCLIWireAndFinishConfirmation(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o := store.Owner{UserID: "batch-cli", WorkspaceID: "fixture"}
	settings, err := s.Settings(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	batchID, reportID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	request := map[string]any{
		"batchId": batchID, "settingsRevision": settings.Revision, "selectedSectionIds": []string{"iq"}, "selectedQuestionIds": []string{"Q2"},
		"tasks": []any{map[string]any{"reportId": reportID, "targetId": "sub2api:1:1", "groupId": "1", "model": "model", "expectedContext": map[string]any{}}},
	}
	targets := []any{map[string]any{"targetId": "sub2api:1:1", "groupId": "1", "target": map[string]any{
		"accountId": "1", "accountName": "fixture", "groupName": "fixture", "baseURL": "https://fixture.invalid", "protocol": "chat"}}}
	call := func(op string, args any, owner store.Owner) (any, error) {
		raw, _ := json.Marshal(args)
		v, _, err := query(ctx, s, wire.Envelope{Operation: op, Owner: owner, Args: raw}, wire.Continue{})
		return v, err
	}
	request["unexpected"] = true
	if _, err := call("create-batch", map[string]any{"request": request, "targets": targets}, o); err == nil {
		t.Fatal("batch accepted unknown wire field")
	}
	delete(request, "unexpected")
	created, err := call("create-batch", map[string]any{"request": request, "targets": targets}, o)
	if err != nil {
		t.Fatal(err)
	}
	create, ok := created.(store.BatchAccepted)
	if !ok || len(create.ReportIDs) != 1 || create.ReportIDs[0] != reportID || len(create.MemberStates) != 0 {
		t.Fatalf("invalid batch accepted contract: %+v", created)
	}
	checked, err := call("read-batch", map[string]any{"batchId": batchID, "request": request}, o)
	if err != nil {
		t.Fatal(err)
	}
	check := checked.(store.BatchAccepted)
	if len(check.MemberStates) != 1 || check.MemberStates[0].Status != "queued" {
		t.Fatal("idempotent read did not project saved member")
	}
	var detail *store.Error
	if _, err := call("read-batch", map[string]any{"batchId": batchID}, store.Owner{UserID: "foreign", WorkspaceID: o.WorkspaceID}); !errors.As(err, &detail) || detail.Status != 404 {
		t.Fatal("batch CLI did not preserve owner privacy", err)
	}
	if _, err := call("cancel-queued", map[string]any{"reportId": reportID, "mayFinalizeBatch": true}, o); err != nil {
		t.Fatal(err)
	}
	value, err := call("read-batch", map[string]any{"batchId": batchID, "view": "results"}, o)
	if err != nil {
		t.Fatal(err)
	}
	batch := value.(store.BatchDetail)
	if batch.FirstFinishedAtMs <= 0 || len(batch.Items) != 1 || batch.Items[0].Status != "cancelled" {
		t.Fatalf("CLI finalization was not applied: %+v", batch)
	}
}

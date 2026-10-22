package connection_health

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Opt-in only. The existing helper labels and removes its isolated schema;
// application tables, schedulable flags and account 395 are never touched.
func TestProbeDiagnosticsPostgresCommitAndRollback(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := t.Context()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "probe-fixture-user", "probe-fixture-workspace", "probe-fixture-group", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatal(err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, "probe-fixture-user", "probe-fixture-workspace")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-time.Hour)
	target := AdminProbeTarget{TargetID: "sub2api:probe-fixture-workspace:probe-fixture-account", Platform: "sub2api", AccountID: "probe-fixture-account", InventoryComplete: true, TestMemberships: []TestConfigurationSource{{AdminGroupID: "probe-fixture-group"}}, TestConfiguration: EffectiveTestConfiguration{Status: "inherited", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}}
	before := defaultTargetState("probe-fixture-user", "probe-fixture-workspace", target, "fixture-model")
	before.State = StateSuspended
	before.ConsecutiveFailures = 30
	before.ConsecutiveSuccesses = 1
	before.CounterProtocol = protocolPointer(TestProtocolResponses)
	before.LastProbeAt = &old
	before.LastSuccessAt = &old
	before.LastSuccessProtocol = protocolPointer(TestProtocolResponses)
	before.LastFirstTokenMs = intPtr(60)
	before.LastFirstEventMs = intPtr(40)
	before.LastSuccessLatencyMs = intPtr(80)
	before.LastErrorDetail = "old service error"
	before.LastErrorKey = "server_error"
	before.HealthEvidenceStatus = HealthEvidenceValid
	before.HealthEvidenceProtocol = protocolPointer(TestProtocolResponses)
	if err := r.UpsertState(ctx, before); err != nil {
		t.Fatal(err)
	}
	out := runProbeDiagnostic(200, "text/event-stream", "data: {\"type\":\"error\",\"code\":\"unsupported_parameter\",\"message\":\"parameter rejected fixture-secret\"}\n\n")
	input := TargetProbeCommit{ConfigGeneration: settings.ConfigGeneration, UserID: before.UserID, AdminAccountID: before.AdminAccountID, Target: target, ModelName: before.ModelName, Policy: taskAV2Policy(), Outcome: out, Now: now, Event: ConnectionHealthEvent{ID: "probe-fixture-invalid", UserID: before.UserID, AdminAccountID: before.AdminAccountID, ConnectionID: target.TargetID, ModelName: before.ModelName, Source: EventSourceManual}}
	committed, err := r.CommitTargetProbe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.GetState(ctx, target.TargetID, before.ModelName)
	if err != nil || state == nil {
		t.Fatal("state read", err)
	}
	if committed.Disposition != "invalid" || state.State != StateSuspended || state.ConsecutiveFailures != 30 || state.ConsecutiveSuccesses != 1 || state.LastErrorDetail != before.LastErrorDetail || !reflect.DeepEqual(state.LastFirstTokenMs, before.LastFirstTokenMs) {
		t.Fatalf("compatibility overwrote persistent health: %+v", state)
	}
	events, err := r.ListEventsByConnection(ctx, target.TargetID, before.UserID, before.AdminAccountID, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("event read: %v %d", err, len(events))
	}
	if events[0].ErrorDetail != out.Detail || events[0].FirstTokenMs != nil || events[0].FirstEventMs == nil || strings.Contains(events[0].ErrorDetail, "fixture-secret") {
		t.Fatalf("event diagnostics/measurements changed: %+v", events[0])
	}
	// A duplicate event ID fails inside the same transaction after the state upsert.
	// The failed commit must restore the exact original row and accepted event.
	saved := *state
	input.Now = now.Add(time.Minute)
	input.Outcome = runProbeDiagnostic(503, "application/json", `{"error":{"type":"api_error","message":"Service temporarily unavailable"}}`)
	if _, err = r.CommitTargetProbe(ctx, input); err == nil {
		t.Fatal("expected duplicate-event transaction failure")
	}
	state, err = r.GetState(ctx, target.TargetID, before.ModelName)
	if err != nil || state == nil || !reflect.DeepEqual(*state, saved) {
		t.Fatalf("failed commit did not roll back: %v", err)
	}
	input.Event.ID = "probe-fixture-http-error"
	if _, err = r.CommitTargetProbe(ctx, input); err != nil {
		t.Fatal(err)
	}
	state, err = r.GetState(ctx, target.TargetID, before.ModelName)
	if err != nil || state == nil || !strings.Contains(state.LastErrorDetail, "HTTP 503") || !reflect.DeepEqual(state.LastFirstTokenMs, before.LastFirstTokenMs) {
		t.Fatal("HTTP diagnostics or historical measurements lost", err)
	}
	events, err = r.ListEventsByConnection(ctx, target.TargetID, before.UserID, before.AdminAccountID, 10)
	if err != nil || len(events) != 2 {
		t.Fatal("accepted event count", err, len(events))
	}
	for _, event := range events {
		if event.Result == string(ResultServerError) && (event.FirstTokenMs != nil || event.FirstEventMs != nil || strings.Contains(event.ErrorDetail, "上游未返回流式")) {
			t.Fatal("HTTP event stored false stream/text measurements")
		}
	}
}

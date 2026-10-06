package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Reflection lets these business assertions run against the pre-upgrade types.
// The old code ignores the new metadata and fails on behavior, not compilation.
func taskASet(value any, name string, field any) {
	v := reflect.ValueOf(value).Elem().FieldByName(name)
	if v.IsValid() && v.CanSet() {
		s := reflect.ValueOf(field)
		if s.Type().ConvertibleTo(v.Type()) {
			v.Set(s.Convert(v.Type()))
		}
	}
}

func taskABool(value any, name string) bool {
	v := reflect.ValueOf(value).FieldByName(name)
	return v.IsValid() && v.Kind() == reflect.Bool && v.Bool()
}

func taskAV2Policy() Policy {
	p := Policy{AutoDegradeEnabled: true, AutoRemoteActionEnabled: true, FailureThreshold: 3, SuccessThreshold: 2, CooldownSeconds: 300, RecoveryStepPercent: 25}
	taskASet(&p, "RuleVersion", "v2")
	return p
}

func TestTaskAFirstFailureRetainsActiveHealthyBand(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	for _, failure := range []ResultKey{ResultNetworkFluctuation, ResultServerError, ResultAuth, ResultRateLimited, ResultModelNotFound} {
		t.Run(string(failure), func(t *testing.T) {
			next, action := applyProbeOutcome(ConnectionHealthState{State: StateHealthy, CurrentWeight: 100}, ProbeOutcome{Result: failure, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}, taskAV2Policy(), now)
			if next.State != State("suspect") || next.CurrentWeight != 100 || next.ConsecutiveFailures != 1 || !taskABool(next, "RecheckPending") || action.TriggerRemoteDegrade || action.TriggerRemoteRestore {
				t.Fatalf("first failure must retain active in healthy band and request one recheck: state=%s weight=%d failures=%d recheck=%v remote=%v/%v", next.State, next.CurrentWeight, next.ConsecutiveFailures, taskABool(next, "RecheckPending"), action.TriggerRemoteDegrade, action.TriggerRemoteRestore)
			}
		})
	}
}

func TestTaskARecoveryAndRepeatedFailure(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	cooldown := now.Add(300 * time.Second)
	for _, state := range []State{State("suspect"), StateDegraded} {
		next, action := applyProbeOutcome(ConnectionHealthState{State: state, CurrentWeight: 0, ConsecutiveFailures: 2}, ProbeOutcome{Result: ResultSlowResponse, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, LatencyMs: 15000}, taskAV2Policy(), now)
		if next.State != StateHealthy || next.CurrentWeight != 100 || next.ConsecutiveFailures != 0 || next.ConsecutiveSuccesses != 0 || action.TriggerRemoteDegrade {
			t.Errorf("completed slow success must immediately recover %s: %+v", state, next)
		}
	}
	current := ConnectionHealthState{State: StateSuspended, CurrentWeight: 0, ConsecutiveFailures: 3, CooldownUntil: &cooldown}
	for i, result := range []ResultKey{ResultOK, ResultServerError, ResultSlowResponse, ResultOK} {
		next, action := applyProbeOutcome(current, ProbeOutcome{Result: result, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}, taskAV2Policy(), now.Add(time.Duration(i+1)*time.Minute))
		if i < 3 && (next.State != StateSuspended || action.TriggerRemoteRestore || action.TriggerRemoteDegrade) {
			t.Fatalf("success/failure/success must remain suspended without repeated action: step=%d state=%s action=%+v", i, next.State, action)
		}
		if i == 1 && (next.CooldownUntil == nil || !next.CooldownUntil.Equal(cooldown)) {
			t.Fatal("repeat failure rewrote cooldown")
		}
		if i == 3 && (next.State != StateHealthy || next.CurrentWeight != 100 || !action.TriggerRemoteRestore) {
			t.Fatal("M completed successes did not restore active")
		}
		current = next
	}
}

func TestTaskAStateTable(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	cooldown := now.Add(-time.Minute)
	cases := []struct {
		name                                string
		state                               State
		failures, successes                 int
		result                              ResultKey
		want                                State
		weight, wantFailures, wantSuccesses int
		recheck, degrade, restore           bool
	}{
		{"healthy success", StateHealthy, 0, 0, ResultOK, StateHealthy, 100, 0, 0, false, false, false},
		{"healthy failure", StateHealthy, 0, 0, ResultAuth, State("suspect"), 100, 1, 0, true, false, false},
		{"suspect success", State("suspect"), 1, 0, ResultOK, StateHealthy, 100, 0, 0, false, false, false},
		{"suspect second failure", State("suspect"), 1, 0, ResultAuth, StateDegraded, 75, 2, 0, false, false, false},
		{"degraded success", StateDegraded, 2, 0, ResultOK, StateHealthy, 100, 0, 0, false, false, false},
		{"degraded third failure", StateDegraded, 2, 0, ResultAuth, StateSuspended, 0, 3, 0, false, true, false},
		{"converted degraded zero counter", StateDegraded, 0, 0, ResultNetworkFluctuation, StateDegraded, 75, 1, 0, false, false, false},
		{"suspended first success", StateSuspended, 3, 0, ResultOK, StateSuspended, 0, 0, 1, false, false, false},
		{"suspended second success", StateSuspended, 0, 1, ResultSlowResponse, StateHealthy, 100, 0, 0, false, false, true},
		{"suspended repeat failure", StateSuspended, 3, 1, ResultAuth, StateSuspended, 0, 4, 0, false, false, false},
		{"suspect invalid", State("suspect"), 1, 0, ResultInvalidResponse, State("suspect"), 100, 1, 0, false, false, false},
		{"degraded invalid", StateDegraded, 2, 0, ResultInvalidResponse, StateDegraded, 75, 2, 0, false, false, false},
		{"disabled failure", StateDisabled, 3, 0, ResultAuth, StateDisabled, 0, 3, 0, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			initialWeight := map[State]int{StateHealthy: 100, State("suspect"): 100, StateDegraded: 75, StateSuspended: 0, StateDisabled: 0}[c.state]
			current := ConnectionHealthState{State: c.state, CurrentWeight: initialWeight, ConsecutiveFailures: c.failures, ConsecutiveSuccesses: c.successes, CounterProtocol: protocolPointer(TestProtocolResponses), CooldownUntil: &cooldown}
			taskASet(&current, "RecheckPending", c.state == State("suspect"))
			next, action := applyProbeOutcome(current, ProbeOutcome{Result: c.result, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}, taskAV2Policy(), now)
			if next.State != c.want || next.CurrentWeight != c.weight || next.ConsecutiveFailures != c.wantFailures || next.ConsecutiveSuccesses != c.wantSuccesses || taskABool(next, "RecheckPending") != c.recheck || action.TriggerRemoteDegrade != c.degrade || action.TriggerRemoteRestore != c.restore {
				t.Fatalf("state table violated: next=%s weight=%d cf=%d cs=%d recheck=%v remote=%v/%v", next.State, next.CurrentWeight, next.ConsecutiveFailures, next.ConsecutiveSuccesses, taskABool(next, "RecheckPending"), action.TriggerRemoteDegrade, action.TriggerRemoteRestore)
			}
			if c.degrade && (next.CooldownUntil == nil || !next.CooldownUntil.Equal(now.Add(300*time.Second))) {
				t.Fatal("first suspension must set cooling deadline")
			}
			if c.name == "suspended repeat failure" && (next.CooldownUntil == nil || !next.CooldownUntil.Equal(cooldown)) {
				t.Fatal("repeated failure must preserve cooling deadline")
			}
		})
	}
	p := taskAV2Policy()
	p.FailureThreshold = 2
	// Setting the new preset through JSON permits running on pre-upgrade types.
	data := []byte(`{"rulePreset":{"failureThreshold":2,"successThreshold":2,"cooldownSeconds":300,"failedRetryIntervalSeconds":600,"longFailureAfterSeconds":86400,"longFailureIntervalSeconds":3600,"observationSeconds":300,"recoveryStepPercent":25}}`)
	field := reflect.ValueOf(&p).Elem().FieldByName("RulePreset")
	if field.IsValid() && field.Kind() == reflect.Pointer {
		preset := reflect.New(field.Type().Elem())
		var wrapper map[string]json.RawMessage
		_ = json.Unmarshal(data, &wrapper)
		_ = json.Unmarshal(wrapper["rulePreset"], preset.Interface())
		field.Set(preset)
	}
	next, action := applyProbeOutcome(ConnectionHealthState{State: State("suspect"), CurrentWeight: 100, ConsecutiveFailures: 1, CounterProtocol: protocolPointer(TestProtocolResponses)}, ProbeOutcome{Result: ResultNetworkFluctuation, Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}, p, now)
	if next.State != StateSuspended || !action.TriggerRemoteDegrade {
		t.Fatal("N=2 must suspend on second failure")
	}
}

func TestTaskACadenceTable(t *testing.T) {
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	last := now.Add(-30 * time.Second)
	cooling := last.Add(300 * time.Second)
	oldCooling := last.Add(-time.Second)
	longAgo := now.Add(-25 * time.Hour)
	cases := []struct {
		name              string
		state             State
		pending           bool
		successes         int
		cooldown, failing *time.Time
		schedulable       bool
		want              time.Time
	}{
		{"suspect recheck", State("suspect"), true, 0, nil, nil, true, now},
		{"suspect consumed", State("suspect"), false, 0, nil, nil, true, last.Add(time.Minute)},
		{"healthy", StateHealthy, false, 0, nil, nil, true, last.Add(time.Minute)},
		{"degraded no backoff", StateDegraded, false, 0, nil, nil, true, last.Add(time.Minute)},
		{"new suspension", StateSuspended, false, 0, &cooling, nil, true, cooling},
		{"suspended accumulating success", StateSuspended, false, 1, &oldCooling, nil, true, last.Add(time.Minute)},
		{"suspended repeat failure", StateSuspended, false, 0, &oldCooling, nil, true, last.Add(600 * time.Second)},
		{"long failure", StateSuspended, false, 0, &oldCooling, &longAgo, true, last.Add(time.Hour)},
		{"main scheduling disabled", State("suspect"), true, 0, nil, nil, false, last.Add(time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := taskAV2Policy()
			p.Enabled = true
			p.ProbeIntervalSeconds = 60
			p.ContinueProbeWhenUnschedulable = true
			p.UnschedulableProbeIntervalMinutes = 60
			s := ConnectionHealthState{State: c.state, ConsecutiveFailures: 2, ConsecutiveSuccesses: c.successes, LastProbeAt: &last, CooldownUntil: c.cooldown}
			taskASet(&s, "RuleVersion", "v2")
			taskASet(&s, "RecheckPending", c.pending)
			if c.failing != nil {
				taskASet(&s, "FailingSince", c.failing)
			}
			decision := calculateEffectiveProbeDecision([]Policy{p}, &c.schedulable, &s, now)
			if decision.NextProbeAt == nil || !decision.NextProbeAt.Equal(c.want) {
				t.Fatalf("cadence must respect state and preset: got=%v want=%s", decision.NextProbeAt, c.want)
			}
		})
	}
}

type taskATransport func(*http.Request) (*http.Response, error)

func (f taskATransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTaskAStreamingRequestsAndCompletion(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
		t.Run(string(protocol), func(t *testing.T) {
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"
			if protocol == TestProtocolResponses {
				body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"
			}
			runner := &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["stream"] != true {
					t.Errorf("health probe must request stream=true, got %v", payload["stream"])
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			out := runner.Probe(context.Background(), ProbeRequest{Protocol: protocol, ProbeTimeoutSeconds: 30, BaseURL: "https://probe.invalid", ModelName: "fixture-model", MaxTokens: 1})
			if out.Result != ResultOK {
				t.Fatalf("completed valid stream must be healthy, got %s", out.Result)
			}
		})
	}
}

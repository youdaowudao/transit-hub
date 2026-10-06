package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type protocolContractTransport func(*http.Request) (*http.Response, error)

func (f protocolContractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// JSON input lets the baseline execute its existing path: RED must be a wrong
// model request/deadline, not a compiler failure for newly introduced fields.
func TestProtocolContractRequestAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		timeout  int
		path     string
		budget   int
	}{
		{"chat_completions", 10, "/v1/chat/completions", 1},
		{"responses", 30, "/v1/responses", 128},
		{"responses", 120, "/v1/responses", 128},
	} {
		t.Run(tc.protocol+time.Duration(tc.timeout).String(), func(t *testing.T) {
			var req ProbeRequest
			payload, _ := json.Marshal(map[string]any{"BaseURL": "https://fixture.invalid", "UpstreamKey": "fixture", "ModelName": "model-a", "MaxTokens": 1, "protocol": tc.protocol, "probeTimeoutSeconds": tc.timeout})
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Fatal(err)
			}
			runner := NewRealProbeRunner()
			calls := 0
			runner.client.Transport = protocolContractTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != tc.path {
					t.Errorf("request path=%s, want %s", r.URL.Path, tc.path)
				}
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > time.Duration(tc.timeout)*time.Second || time.Until(deadline) < time.Duration(tc.timeout-1)*time.Second {
					t.Errorf("request deadline remaining=%v exists=%v, want %ds (including body)", time.Until(deadline), ok, tc.timeout)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				response := `{"choices":[{"message":{"content":"ok"}}]}`
				if tc.protocol == "responses" {
					if body["max_output_tokens"] != float64(tc.budget) || body["stream"] != true || body["store"] != false || body["input"] == nil || body["messages"] != nil || body["max_tokens"] != nil || body["reasoning"] != nil {
						t.Errorf("wrong Responses request: %#v", body)
					}
					response = `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
			})
			out := runner.Probe(context.Background(), req)
			if out.Result != ResultOK {
				t.Errorf("complete protocol response rejected: %+v", out)
			}
			if calls != 1 {
				t.Errorf("request count=%d, want exactly 1, no fallback", calls)
			}
		})
	}
}

type protocolBlockingBody struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
	closed  bool
}

func (b *protocolBlockingBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *protocolBlockingBody) Close() error { b.closed = true; return nil }

func TestProtocolContractCompleteResponseTwelveSecondsIsSlow(t *testing.T) {
	runner := NewRealProbeRunner()
	start := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	clockCalls := 0
	runner.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return start
		}
		return start.Add(12 * time.Second)
	}
	runner.client.Transport = protocolContractTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) < 29*time.Second {
			t.Error("12s response would be cut by legacy10 deadline")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}]}`)), Request: r}, nil
	})
	// Request classification leaves the delay label to the selected rule preset.
	outcome := applyOutcomeDelay(runner.Probe(context.Background(), ProbeRequest{BaseURL: "https://fixture.invalid", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, ModelName: "m"}), Policy{RuleVersion: RuleVersionLegacy})
	if outcome.Result != ResultSlowResponse || outcome.LatencyMs != 12000 {
		t.Errorf("12s complete response must retain slow threshold: %+v", outcome)
	}
}

func TestProtocolContractBodyHonorsParentCancellationAndCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &protocolBlockingBody{started: make(chan struct{})}
	runner := NewRealProbeRunner()
	runner.client.Transport = protocolContractTransport(func(r *http.Request) (*http.Response, error) {
		body.ctx = r.Context()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
	})
	result := make(chan ProbeOutcome, 1)
	go func() {
		result <- runner.Probe(ctx, ProbeRequest{BaseURL: "https://fixture.invalid", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 120, ModelName: "m"})
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("body read did not begin")
	}
	deadline, ok := body.ctx.Deadline()
	if !ok || time.Until(deadline) < 119*time.Second {
		t.Error("configured deadline missing from body read")
	}
	cancel()
	select {
	case outcome := <-result:
		if outcome.Result != ResultNetworkFluctuation || outcome.RequestPhase != "reading_body" || !body.closed {
			t.Errorf("body cancellation did not close/classify request: outcome=%+v closed=%v", outcome, body.closed)
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not interrupt body")
	}
}

func TestProtocolContractInvalidPreservesCurrentFailure(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	previous := now.Add(-time.Minute)
	current := ConnectionHealthState{ConnectionID: "sub2api:ws1:1", ModelName: "m", State: StateDegraded, CurrentWeight: 50, ConsecutiveFailures: 2, LastFailureAt: &previous, LastErrorKey: string(ResultNetworkFluctuation), LastErrorDetail: "effective failure"}
	var outcome ProbeOutcome
	if err := json.Unmarshal([]byte(`{"Result":"invalid_response","Detail":"response incomplete","protocol":"responses","probeTimeoutSeconds":30}`), &outcome); err != nil {
		t.Fatal(err)
	}
	next, _ := applyProbeOutcome(current, outcome, Policy{AutoDegradeEnabled: true, FailureThreshold: 3}, now)
	if next.State != current.State || next.ConsecutiveFailures != 2 || next.LastFailureAt == nil || !next.LastFailureAt.Equal(previous) || next.LastErrorDetail != "effective failure" {
		t.Errorf("invalid attempt polluted accepted health failure: %+v", next)
	}
	if next.LastProbeAt == nil || !next.LastProbeAt.Equal(now) {
		t.Error("invalid attempt must still advance last attempt")
	}
}

func TestProtocolContractStaleEventCannotReplaceFailure(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for _, disposition := range []string{"stale", "invalid"} {
		t.Run(disposition, func(t *testing.T) {
			models := []ModelHealth{{ModelName: "m", LastFailureAt: &now, LastErrorKey: "network_fluctuation", LastErrorDetail: "accepted failure"}}
			old := ConnectionHealthEvent{ConnectionID: "sub2api:ws1:1", ModelName: "m", CreatedAt: now, Result: "network_fluctuation", ErrorDetail: "accepted failure"}
			var late ConnectionHealthEvent
			payload, _ := json.Marshal(map[string]any{"connectionId": old.ConnectionID, "modelName": "m", "createdAt": now.Add(time.Minute), "result": "server_error", "errorDetail": "obsolete attempt", "probeDisposition": disposition, "requestProtocol": "chat_completions", "requestTimeoutSeconds": 10})
			if err := json.Unmarshal(payload, &late); err != nil {
				t.Fatal(err)
			}
			latest := latestProbeFailureEventsByTargetModel([]ConnectionHealthEvent{old, late})
			applyLatestProbeFailureDetails(models, latest[old.ConnectionID])
			if models[0].LastErrorDetail != "accepted failure" {
				t.Errorf("%s event replaced current DTO error: %+v", disposition, models[0])
			}
		})
	}
}

func TestProtocolContractProtocolSwitchResetsCountersBeforeTransition(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	var state ConnectionHealthState
	if err := json.Unmarshal([]byte(`{"ConnectionID":"sub2api:ws1:1","ModelName":"m","State":"degraded","CurrentWeight":50,"ConsecutiveFailures":2,"CounterProtocol":"chat_completions","HealthEvidenceStatus":"valid","HealthEvidenceProtocol":"chat_completions"}`), &state); err != nil {
		t.Fatal(err)
	}
	var outcome ProbeOutcome
	if err := json.Unmarshal([]byte(`{"Result":"network_fluctuation","protocol":"responses","probeTimeoutSeconds":30}`), &outcome); err != nil {
		t.Fatal(err)
	}
	policy := Policy{AutoDegradeEnabled: true, FailureThreshold: 3}
	next, _ := applyProbeOutcome(state, outcome, policy, now)
	if next.ConsecutiveFailures != 1 || next.State != StateDegraded {
		t.Errorf("old Chat failures drove new Responses transition: count=%d state=%s", next.ConsecutiveFailures, next.State)
	}
	next, _ = applyProbeOutcome(next, outcome, policy, now.Add(time.Minute))
	if next.ConsecutiveFailures != 2 {
		t.Errorf("second same-protocol failure reset or inherited count=%d", next.ConsecutiveFailures)
	}
}

func TestProtocolContractRoundTripCannotReviveEvidence(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for _, origin := range []string{"valid", "legacy"} {
		t.Run(origin, func(t *testing.T) {
			var state ConnectionHealthState
			payload, _ := json.Marshal(map[string]any{"ConnectionID": "sub2api:ws1:1", "ModelName": "m", "State": "suspended", "CurrentWeight": 0, "ConsecutiveFailures": 3, "HealthEvidenceStatus": origin, "HealthEvidenceProtocol": "chat_completions"})
			if err := json.Unmarshal(payload, &state); err != nil {
				t.Fatal(err)
			}
			var outcome ProbeOutcome
			if err := json.Unmarshal([]byte(`{"Result":"ok","protocol":"responses","probeTimeoutSeconds":30}`), &outcome); err != nil {
				t.Fatal(err)
			}
			next, _ := applyProbeOutcome(state, outcome, Policy{AutoDegradeEnabled: true, ObservationSeconds: 60, SuccessThreshold: 2}, now)
			if next.State != StateObserving {
				t.Fatalf("control transition changed: %s", next.State)
			}
			encoded, _ := json.Marshal(next)
			var evidence map[string]any
			_ = json.Unmarshal(encoded, &evidence)
			if evidence["HealthEvidenceStatus"] != "invalid" || (evidence["HealthEvidenceProtocol"] != "" && evidence["HealthEvidenceProtocol"] != nil) {
				t.Errorf("new protocol Observing must invalidate old Chat evidence before switch-back/clear: %s", encoded)
			}
		})
	}
}

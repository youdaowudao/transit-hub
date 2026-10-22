package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// In-memory HTTP transport and deterministic readings; no listener or upstream IO.
func probeDiagnosticFixture(status int, contentType, body string) (*RealProbeRunner, ProbeRequest) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	runner := &RealProbeRunner{now: func() time.Time { now = now.Add(18 * time.Millisecond); return now }, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}
	return runner, ProbeRequest{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, BaseURL: "https://probe-diagnostic.invalid", ModelName: "fixture-model", AnthropicAuth: "x_api_key", UpstreamKey: "fixture-secret"}
}

func runProbeDiagnostic(status int, contentType, body string) ProbeOutcome {
	runner, req := probeDiagnosticFixture(status, contentType, body)
	return probeOutcomeWithAuditMetadata(runner.Probe(context.Background(), req))
}

func TestProbeErrorClassificationExactCompatibilityCodes(t *testing.T) {
	for _, code := range []string{"unsupported_parameter", "unsupported_endpoint", "endpoint_not_supported", "unsupported_protocol"} {
		for _, body := range []string{
			fmt.Sprintf(`{"type":"error","code":%q,"message":"not accepted"}`, code),
			fmt.Sprintf(`{"type":"error","error":{"code":%q,"type":"invalid_request_error","message":"not accepted"}}`, code),
			fmt.Sprintf(`{"type":"response.failed","response":{"status":"failed","error":{"code":%q,"message":"not accepted"}}}`, code),
			fmt.Sprintf(`{"error":{"code":%q},"status_code":400}`, code),
			fmt.Sprintf(`{"type":"","error":{"code":%q},"status":404}`, code),
			fmt.Sprintf(`{"type":null,"error":{"code":%q},"status":null}`, code),
		} {
			t.Run(code+body, func(t *testing.T) {
				for _, transport := range []string{"http", "http404", "sse", "event-name"} {
					status, contentType, payload := 400, "application/json", body
					if transport == "http404" {
						status = 404
					}
					if transport == "sse" || transport == "event-name" {
						status, contentType, payload = 200, "text/event-stream", "data: "+body+"\n\n"
					}
					if transport == "event-name" {
						payload = "event: error\n" + payload
					}
					got := runProbeDiagnostic(status, contentType, payload)
					if got.Result != ResultInvalidResponse {
						t.Errorf("%s: result=%s, want invalid_response", transport, got.Result)
					}
				}
			})
		}
	}
}

func TestProbeErrorClassificationConservativeEvidence(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{"type":"invalid_request_error","message":"unsupported parameter"}}`,
		`{"type":"error","code":"UNSUPPORTED_PARAMETER"}`, `{"type":"error","code":"unknown"}`, `{"type":"error","code":""}`,
		`{"type":"error","code":42}`, `{"type":"error","code":null}`, `{"type":"error","error":"unsupported_parameter"}`,
		`{"type":"error","code":"unsupported_parameter","error":{"code":"unsupported_parameter"}}`,
		`{"type":"error","code":"unsupported_parameter","error":{"code":42}}`,
		`{"type":"error","code":"unsupported_parameter","error":"failure"}`,
		`{"type":"error","code":"unsupported_parameter","error":[]}`,
		`{"type":"error","code":"unsupported_parameter","error":true}`,
		`{"type":"error","code":"unsupported_parameter","error":{"type":null}}`,
		`{"type":"error","code":"unsupported_parameter","error":{"type":42}}`,
		`{"type":"error","code":"unsupported_parameter","response":{"error":"failure"}}`,
		`{"type":"error","code":"unsupported_parameter","error":{"type":"api_error"}}`,
		`{"type":"error","error":{"code":"model_not_found"}}`, `{"type":"error","error":{"code":"invalid_api_key"}}`,
		`{"type":"error","error":{"type":"api_error","message":"Service temporarily unavailable"}}`,
		`{"type":"error","code":"unsupported_parameter","response":{"error":{"type":"server_error"}}}`,
		`{"type":"response.failed","code":"model_not_found","response":{"error":{"code":"unsupported_parameter"}}}`,
		`{"type":"response.failed","code":"unsupported_parameter","response":{"error":{"code":"unsupported_parameter"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			if out := runProbeDiagnostic(200, "text/event-stream", "data: "+body+"\n\n"); out.Result != ResultServerError {
				t.Errorf("conflicting/unknown evidence excluded failure: %s", out.Result)
			}
		})
	}
	for _, typ := range []string{"api_error", "server_error", "future_error", "invalid_request_error"} {
		for _, body := range []string{fmt.Sprintf(`{"type":%q,"error":{"code":"unsupported_parameter"}}`, typ), `{"error":{"code":"unsupported_parameter"}}`} {
			out := runProbeDiagnostic(200, "text/event-stream", "event: "+typ+"\ndata: "+body+"\n\n")
			if out.Result != ResultServerError {
				t.Errorf("type=%s body=%s result=%s", typ, body, out.Result)
			}
		}
	}
	for _, location := range []string{"top", "error", "response", "response.error"} {
		for _, field := range []string{"status", "status_code"} {
			for _, value := range []string{"401", "403", "429", "500", `"400"`, `"unknown"`, "true"} {
				body := map[string]any{"type": "error", "code": "unsupported_parameter"}
				var object map[string]any
				switch location {
				case "top":
					object = body
				case "error":
					object = map[string]any{}
					body["error"] = object
				case "response":
					object = map[string]any{}
					body["response"] = object
				default:
					object = map[string]any{}
					body["response"] = map[string]any{"error": object}
				}
				var v any
				_ = json.Unmarshal([]byte(value), &v)
				object[field] = v
				encoded, _ := json.Marshal(body)
				raw := string(encoded)
				out := runProbeDiagnostic(200, "text/event-stream", "data: "+raw+"\n\n")
				want := streamErrorResult([]byte(raw))
				if out.Result != want {
					t.Errorf("%s %s=%s got=%s want=%s", location, field, value, out.Result, want)
				}
			}
		}
	}
	for _, body := range []string{`{"type":"error","code":"unsupported_parameter","extra":{"code":"rate_limit_exceeded"}}`, `{"type":"error","error":{"type":"overloaded_error","code":"unsupported_parameter"}}`} {
		runner, req := probeDiagnosticFixture(200, "text/event-stream", "data: "+body+"\n\n")
		req.Protocol = TestProtocolMessages
		if out := runner.Probe(context.Background(), req); out.Result != ResultRateLimited {
			t.Errorf("rate limit lost: %+v", out)
		}
	}
	// The shared classifier used by question-answer is deliberately unchanged.
	if streamErrorResult([]byte(`{"type":"error","code":"unsupported_parameter"}`)) != ResultServerError {
		t.Fatal("shared question-answer classifier changed")
	}
}

func TestProbeErrorStateCompatibilityKeepsExistingHealth(t *testing.T) {
	for _, state := range []State{StateHealthy, StateSuspect, StateDegraded, StateSuspended} {
		for _, code := range []string{"unsupported_parameter", "unsupported_endpoint", "endpoint_not_supported", "unsupported_protocol"} {
			t.Run(string(state)+"/"+code, func(t *testing.T) {
				now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
				previous := now.Add(-time.Hour)
				before := ConnectionHealthState{CounterProtocol: protocolPointer(TestProtocolResponses), State: state, CurrentWeight: 17, ConsecutiveFailures: 30, ConsecutiveSuccesses: 2, LastSuccessAt: &previous, LastSuccessLatencyMs: intPtr(90), LastFirstTokenMs: intPtr(70), LastFirstEventMs: intPtr(50), LastErrorKey: "server_error", LastErrorDetail: "old failure", HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolResponses), LastSuccessProtocol: protocolPointer(TestProtocolResponses), LastRemoteAction: RemoteActionSub2APIStatusInactive}
				out := runProbeDiagnostic(200, "text/event-stream", fmt.Sprintf("data: {\"type\":\"error\",\"code\":%q}\n\n", code))
				after, transition := applyProbeOutcome(before, out, taskAV2Policy(), now)
				if after.State != before.State || after.CurrentWeight != before.CurrentWeight || after.ConsecutiveFailures != before.ConsecutiveFailures || after.ConsecutiveSuccesses != before.ConsecutiveSuccesses || !reflect.DeepEqual(after.LastSuccessAt, before.LastSuccessAt) || !reflect.DeepEqual(after.LastFirstTokenMs, before.LastFirstTokenMs) || !reflect.DeepEqual(after.LastFirstEventMs, before.LastFirstEventMs) || after.LastRemoteAction != before.LastRemoteAction || after.LastErrorDetail != before.LastErrorDetail || transition.NextState != state || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
					t.Fatalf("compatibility error changed retained health: before=%+v after=%+v transition=%+v", before, after, transition)
				}
			})
		}
	}
}

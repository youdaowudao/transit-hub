package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

// These primary-agent REDs use the existing dispatch/HTTP interfaces, so the
// baseline fails on missing business evidence rather than missing new symbols.
func diagnosticsREDRecord(t *testing.T, protocol TestProtocol, transport http.RoundTripper) map[string]any {
	t.Helper()
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, repo)
	s.questionAnswerTTL = 1100 * time.Millisecond
	s.questionAnswerMessagesTTL = 1700 * time.Millisecond
	run := panicRegressionStart(t, s, repo, "diagnostics-red")
	run.cred.AnthropicAuth = "x_api_key"
	run.records[0].RequestProtocol = &protocol
	s.questionAnswerHTTP.client.Transport = transport
	panicRegressionDispatch(t, s)
	records, err := repo.ListQuestionAnswerBatch(context.Background(), "user1", run.targetID, run.batchID)
	if err != nil || len(records) != 1 {
		t.Fatalf("record read: count=%d err=%v", len(records), err)
	}
	data, err := json.Marshal(records[0])
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func diagnosticsREDEvidence(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	diagnostics, ok := record["diagnostics"].(map[string]any)
	if !ok {
		t.Fatalf("completed record lost the request evidence: status=%v diagnostics=%v", record["status"], record["diagnostics"])
	}
	return diagnostics
}

func TestQuestionAnswerDiagnosticsREDActualBudgetAndObservedStages(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses, TestProtocolMessages} {
		t.Run(string(protocol), func(t *testing.T) {
			body := `{"choices":[{"message":{"content":"ok"}}]}`
			if protocol == TestProtocolResponses {
				body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`
			}
			if protocol == TestProtocolMessages {
				body = `{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`
			}
			record := diagnosticsREDRecord(t, protocol, c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(request.Context())
				if trace != nil {
					if trace.WroteRequest != nil {
						trace.WroteRequest(httptrace.WroteRequestInfo{})
					}
					if trace.GotFirstResponseByte != nil {
						trace.GotFirstResponseByte()
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if record["status"] != "succeeded" || record["answerBody"] != "ok" {
				t.Fatalf("complete answer was not preserved: %v", record["status"])
			}
			d := diagnosticsREDEvidence(t, record)
			budget := float64(1100)
			if protocol == TestProtocolMessages {
				budget = 1700
			}
			if d["version"] != float64(1) || d["timeoutMs"] != budget {
				t.Fatalf("actual dispatch budget not retained: version=%v timeoutMs=%v", d["version"], d["timeoutMs"])
			}
			for _, field := range []string{"selectedAt", "requestStartedAt", "requestWrittenAt", "firstResponseByteAt", "responseHeadersAt", "lastReceivedAt", "requestFinishedAt", "requestElapsedMs"} {
				if d[field] == nil {
					t.Errorf("observed %s is missing", field)
				}
			}
			if d["receivedBytes"] != float64(len(body)) {
				t.Errorf("receivedBytes=%v want %d", d["receivedBytes"], len(body))
			}
		})
	}
}

type diagnosticsREDReadFailure struct{}

func (diagnosticsREDReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestQuestionAnswerDiagnosticsREDDirectCauseAndNoInventedWrite(t *testing.T) {
	for _, test := range []struct {
		name, step, cause string
		transport         http.RoundTripper
	}{
		{"dns", "request", "no such host", c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "unit.invalid"}
		})},
		{"read", "receive", "unexpected EOF", c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(diagnosticsREDReadFailure{})}, nil
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := diagnosticsREDRecord(t, TestProtocolChatCompletions, test.transport)
			if r["status"] != "failed" || r["answerJudgment"] != nil {
				t.Fatal("network failure became a judged answer")
			}
			d := diagnosticsREDEvidence(t, r)
			if d["failureStep"] != test.step || !strings.Contains(d["localError"].(string), test.cause) {
				t.Fatalf("direct cause/step lost: step=%v cause=%v", d["failureStep"], d["localError"])
			}
			if d["requestWrittenAt"] != nil {
				t.Fatal("request was falsely described as written")
			}
		})
	}
}

type diagnosticsREDReadRepository struct{ *fakeQuestionAnswerRepository }

func (r *diagnosticsREDReadRepository) LatestQuestionAnswerBatch(context.Context, string, string) ([]QuestionAnswerRecord, error) {
	return nil, errors.New("context deadline exceeded")
}

func TestQuestionAnswerDiagnosticsREDOperationKeepsKeyAndExplainsCause(t *testing.T) {
	s := panicPathsScheduleService(t, nil)
	s.questionAnswers = &diagnosticsREDReadRepository{newFakeQuestionAnswerRepository()}
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	response := internalHTTPQuestionAnswerRequest(mux, http.MethodGet, "/latest", "")
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 500 {
		t.Fatalf("status=%d", response.Code)
	}
	if body["message"] != ErrorUnknown {
		t.Fatalf("legacy key changed: %v", body["message"])
	}
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "deadline exceeded") || !strings.Contains(detail, "读取") || body["step"] == nil {
		t.Fatalf("read failure lost its direct cause and retry guidance: detail=%q step=%v", detail, body["step"])
	}
}

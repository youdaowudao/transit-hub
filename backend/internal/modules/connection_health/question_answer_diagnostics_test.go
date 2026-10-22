package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func diagnosticsTestAsk(t *testing.T, protocol TestProtocol, status int, body io.Reader, contentTypes ...string) QuestionAnswerAskResult {
	t.Helper()
	p := newQuestionAnswerProgress(time.Now())
	p.startRequest(time.Second, time.Now())
	runner := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		headers := make(http.Header)
		if len(contentTypes) > 0 {
			headers.Set("Content-Type", contentTypes[0])
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(body)}, nil
	})}}
	return runner.AskDetailedWithProgress(context.Background(), upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "private-key-diagnostics", AnthropicAuth: "x_api_key"}, "model", "question", QuestionAnswerReasoningEffortMedium, p, protocol)
}

func TestQuestionAnswerDiagnosticsMessagesScannerDirectCause(t *testing.T) {
	result := diagnosticsTestAsk(t, TestProtocolMessages, 200, strings.NewReader(strings.Repeat("x", maxProbeResponseBytes+3)), "text/event-stream")
	if result.ErrorType != QuestionAnswerErrorResponseTooLarge || result.Answer != "" || result.Diagnostics == nil || result.Diagnostics.FailureStep != "receive" || !strings.Contains(result.Diagnostics.LocalError, "token too long") {
		t.Fatal("Messages Scanner limit lost its direct cause or became success")
	}
}

func TestQuestionAnswerDiagnosticsDirectParseReadAndLimitCauses(t *testing.T) {
	for _, test := range []struct {
		name, body, errorType, step, cause string
		protocol                           TestProtocol
	}{
		{"chat-json", "{", QuestionAnswerErrorInvalidResponse, "parse", "unexpected end", TestProtocolChatCompletions},
		{"messages-json", "{", QuestionAnswerErrorInvalidResponse, "parse", "unexpected end", TestProtocolMessages},
		{"responses-json", "{", QuestionAnswerErrorInvalidResponse, "parse", "unexpected end", TestProtocolResponses},
		{"responses-event-json", "data: {broken}\n\n", QuestionAnswerErrorInvalidResponse, "parse", "invalid character", TestProtocolResponses},
		{"messages-event-json", "data: {broken}\n\n", QuestionAnswerErrorInvalidResponse, "parse", "invalid character", TestProtocolMessages},
		{"chat-size", strings.Repeat("x", maxProbeResponseBytes+1), QuestionAnswerErrorResponseTooLarge, "receive", "1MiB", TestProtocolChatCompletions},
		{"responses-size", strings.Repeat("x", maxProbeResponseBytes+1), QuestionAnswerErrorResponseTooLarge, "receive", "1MiB", TestProtocolResponses},
		{"messages-output-limit", `{"type":"message","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`, QuestionAnswerErrorResponseIncomplete, "parse", "max_tokens", TestProtocolMessages},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := diagnosticsTestAsk(t, test.protocol, 200, strings.NewReader(test.body))
			if result.ErrorType != test.errorType || result.Answer != "" || result.Diagnostics == nil {
				t.Fatalf("failure became success or lost diagnostics: %+v", result)
			}
			d := result.Diagnostics
			if d.FailureStep != test.step || !strings.Contains(d.LocalError, test.cause) {
				t.Fatalf("direct cause lost: step=%s cause=%s", d.FailureStep, d.LocalError)
			}
			if d.RequestWrittenAt != nil || d.FirstResponseByteAt != nil {
				t.Fatal("unobserved trace callback was invented")
			}
		})
	}
}

type diagnosticsHeartbeatReader struct {
	ctx  context.Context
	sent bool
}

func (r *diagnosticsHeartbeatReader) Read(buffer []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(buffer, ": heartbeat\n\n"), nil
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestQuestionAnswerDiagnosticsHeartbeatDoesNotRenewBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p := newQuestionAnswerProgress(time.Now().Add(-time.Minute))
	p.startRequest(20*time.Millisecond, time.Now())
	runner := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{Err: io.ErrUnexpectedEOF})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&diagnosticsHeartbeatReader{ctx: request.Context()})}, nil
	})}}
	result := runner.AskDetailedWithProgress(ctx, upstream.ProbeCredential{BaseURL: "https://unit.invalid"}, "model", "q", QuestionAnswerReasoningEffortMedium, p, TestProtocolResponses)
	if result.ErrorType != QuestionAnswerErrorTimeout || result.Answer != "" || ctx.Err() != context.DeadlineExceeded {
		t.Fatal("heartbeat changed hard deadline or became a complete answer")
	}
	d := result.Diagnostics
	if d.RequestWrittenAt != nil || d.LastReceivedAt == nil || d.ReceivedBytes != int64(len(": heartbeat\n\n")) || d.TimeoutMs == nil || *d.TimeoutMs != 20 {
		t.Fatalf("heartbeat/write evidence inaccurate: %+v", d)
	}
	if d.RequestElapsedMs == nil || *d.RequestElapsedMs > 1000 {
		t.Fatal("waiting before HTTP was charged to the HTTP duration")
	}
}

func TestQuestionAnswerDiagnosticsRemoteFailureIsNotLocalDeadline(t *testing.T) {
	for _, status := range []int{200, 403, 429, 502, 524} {
		result := diagnosticsTestAsk(t, TestProtocolResponses, status, strings.NewReader(`{"status":"failed","error":{"message":"upstream timeout"}}`))
		if result.ErrorType == "" || result.ErrorType == QuestionAnswerErrorTimeout || result.Answer != "" || !strings.Contains(result.UpstreamExcerpt, "upstream timeout") {
			t.Fatalf("HTTP %d was mistaken for local deadline: %+v", status, result)
		}
	}
}

func TestQuestionAnswerDiagnosticsSafeCauseRemovesCredentialsAndQueryText(t *testing.T) {
	key := "private-key-diagnostics"
	for _, err := range []error{
		&url.Error{Op: "Post", URL: "https://user:password@unit.invalid/path?token=secret", Err: errors.New("connection reset by peer")},
		errors.New(`TLS handshake failed https://user:password@unit.invalid/path?token=secret Bearer short-secret token=secret private-key-diagnostics`),
		errors.New(`password=secret host=unit.invalid SQL=SELECT password FROM users`),
		&pgconn.PgError{Code: "22000", Message: `invalid input value "private-key-diagnostics"`, Detail: "secret SQL parameter", InternalQuery: "SELECT password FROM users"},
		errors.New(`JSON failure private\u002dkey\u002ddiagnostics`),
	} {
		cause := questionAnswerSafeCause(err, key)
		for _, forbidden := range []string{key, "user:password", "token=secret", "short-secret", "SQL=SELECT", "secret SQL parameter", `private\u002dkey`} {
			if strings.Contains(cause, forbidden) {
				t.Fatalf("cause leaks %q: %q", forbidden, cause)
			}
		}
		if len([]rune(cause)) > 500 {
			t.Fatal("cause exceeded 500 characters")
		}
	}
	if cause := questionAnswerSafeCause(errors.New(strings.Repeat("原", 600))); len([]rune(cause)) != 500 {
		t.Fatalf("cause bound=%d", len([]rune(cause)))
	}
}

func TestQuestionAnswerDiagnosticsCodecAndClone(t *testing.T) {
	progress := newQuestionAnswerProgress(time.Now())
	progress.startRequest(time.Second, time.Now())
	original := progress.snapshot("record").Diagnostics
	encoded, err := encodeQuestionAnswerDiagnostics(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, unavailable := decodeQuestionAnswerDiagnostics(encoded)
	if unavailable || decoded == nil || decoded.TimeoutMs == nil || *decoded.TimeoutMs != 1000 {
		t.Fatal("valid diagnostic lost")
	}
	cloned := cloneQuestionAnswerRecords([]QuestionAnswerRecord{{Diagnostics: decoded}})
	*cloned[0].Diagnostics.TimeoutMs = 7
	*cloned[0].Diagnostics.SelectedAt = time.Time{}
	if *decoded.TimeoutMs != 1000 || decoded.SelectedAt.IsZero() {
		t.Fatal("diagnostic copy aliases the original")
	}
	for _, data := range []string{`[]`, `{bad`, `{"version":2}`, `{"version":1,"receivedBytes":-1}`, `{"version":1,"failureStep":"unknown"}`, `{"version":1,"localError":"` + strings.Repeat("x", questionAnswerDiagnosticsLimit) + `"}`} {
		if d, bad := decodeQuestionAnswerDiagnostics([]byte(data)); !bad || d != nil {
			t.Fatalf("corruption was not isolated: %q", data[:min(len(data), 50)])
		}
	}
	if d, bad := decodeQuestionAnswerDiagnostics(nil); bad || d != nil {
		t.Fatal("old NULL marked corrupt")
	}
	if strings.Contains(questionAnswerRecordSummarySelect(), "upstream_excerpt, request_diagnostics") || !strings.Contains(questionAnswerRecordSummarySelect(), "NULL AS request_diagnostics") || !strings.Contains(questionAnswerRecordSummarySelect(), "'' AS answer_body") {
		t.Fatal("summary loads answers or diagnostics")
	}
}

func TestQuestionAnswerOperationJoinedCauseKeepsKeyWithoutDisplayingIt(t *testing.T) {
	for _, step := range []string{"creating", "reading", "history", "cancelling", "judging"} {
		err := errors.Join(requestError(ErrorQuestionAnswerStorage), errors.New("context deadline exceeded"))
		wrapped := questionAnswerOperationError(err, step, "unknown", "batch-id")
		if !errors.Is(wrapped, err) {
			t.Fatal("original error no longer unwraps")
		}
		response := httptest.NewRecorder()
		writeQuestionAnswerError(response, wrapped, step, "unknown", "batch-id")
		var payload map[string]any
		if json.Unmarshal(response.Body.Bytes(), &payload) != nil || response.Code != 500 || payload["message"] != ErrorQuestionAnswerStorage {
			t.Fatal("legacy status/key changed")
		}
		detail, _ := payload["detail"].(string)
		if strings.Contains(detail, "admin.") || !strings.Contains(detail, "deadline exceeded") {
			t.Fatalf("joined cause lost or leaks key: %s", detail)
		}
		if step == "judging" && !strings.Contains(detail, "先重新读取") {
			t.Fatal("uncertain judgment claims success")
		}
	}
}

func TestQuestionAnswerDiagnosticsActualCancelRetainsObservedProgress(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, repo, "diagnostic-cancel")
	started := make(chan struct{})
	dispatchDone := make(chan struct{})
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(request.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		close(started)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&diagnosticsHeartbeatReader{ctx: request.Context()})}, nil
	})
	go func() { defer close(dispatchDone); panicRegressionDispatch(t, s) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.stopQuestionAnswerBatchCore(ctx, run.userID, run.targetID, run.batchID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatchDone:
	case <-ctx.Done():
		t.Fatal("dispatch did not finish")
	}
	records, err := repo.ListQuestionAnswerBatch(ctx, run.userID, run.targetID, run.batchID)
	if err != nil || len(records) != 1 {
		t.Fatal("cancelled record missing")
	}
	r := records[0]
	if r.Status != QuestionAnswerCancelled || r.AnswerBody != "" || r.AnswerJudgment != nil || r.Diagnostics == nil || r.Diagnostics.RequestStartedAt == nil || r.Diagnostics.RequestWrittenAt == nil || r.Diagnostics.RequestFinishedAt == nil {
		t.Fatal("cancel rewrote the result or lost actual request progress")
	}
}

func TestQuestionAnswerOperationKnownValidationReasonIsNotSwallowed(t *testing.T) {
	for _, test := range []struct{ key, reason string }{
		{ErrorQuestionAnswerSelection, "先选择模型和题目"},
		{ErrorTestQuestionDisabled, "题目已停用"},
		{ErrorQuestionAnswerActive, "已有正在进行"},
		{ErrorQuestionAnswerJudgmentConflict, "已被其他操作修改"},
	} {
		response := httptest.NewRecorder()
		writeQuestionAnswerError(response, requestError(test.key), "preparing", "not_started", "")
		var payload map[string]any
		if json.Unmarshal(response.Body.Bytes(), &payload) != nil {
			t.Fatal("invalid response")
		}
		detail, _ := payload["detail"].(string)
		if payload["message"] != test.key || !strings.Contains(detail, test.reason) || strings.Contains(detail, "admin.") || strings.Contains(detail, "接口未提供更具体原因") {
			t.Fatalf("known validation explanation lost: %q", detail)
		}
	}
}

func TestQuestionAnswerOperationHTTPPreflightRejectionsKeepKeyAndSpecificDetail(t *testing.T) {
	repo := newFakeQuestionAnswerRepository()
	mux := http.NewServeMux()
	RegisterRoutes(mux, &Service{questionAnswers: repo})
	base := "/api/connection-health/targets/sub2api:ws1:a/question-answers"
	for _, test := range []struct{ name, method, path, body, key, step, reason string }{
		{"start-json", http.MethodPost, "/batches", "{", ErrorRequest, "preparing", "EOF"},
		{"selection", http.MethodPost, "/batches", `{}`, ErrorQuestionAnswerSelection, "preparing", "选择模型和题目"},
		{"history-zero", http.MethodGet, "/history?page=0", "", ErrorQuestionAnswerHistoryPage, "history", "页码"},
		{"history-duplicate", http.MethodGet, "/history?page=1&page=2", "", ErrorQuestionAnswerHistoryPage, "history", "页码"},
		{"history-scope", http.MethodGet, "/history?scope=invalid", "", ErrorQuestionAnswerHistoryScope, "history", "今日或全部"},
		{"judgment-json", http.MethodPut, "/records/r/judgment", "{", ErrorRequest, "judging", "EOF"},
		{"judgment-value", http.MethodPut, "/records/r/judgment", `{"judgment":"bad","expectedUpdatedAt":"2026-10-22T00:00:00Z"}`, ErrorRequest, "judging", "必须选择正确或错误"},
		{"judgment-time", http.MethodPut, "/records/r/judgment", `{"judgment":"correct"}`, ErrorRequest, "judging", "更新时间"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, base+test.path, strings.NewReader(test.body))
			r = r.WithContext(authctx.WithUserID(r.Context(), "user1"))
			r.Header.Set(questionAnswerContractHeaderName, questionAnswerContractVersion)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, r)
			var payload map[string]any
			if json.Unmarshal(response.Body.Bytes(), &payload) != nil || response.Code != http.StatusBadRequest || payload["message"] != test.key || payload["step"] != test.step || payload["operationResult"] != "not_started" {
				t.Fatalf("rejection contract changed: %d %s", response.Code, response.Body.String())
			}
			detail, _ := payload["detail"].(string)
			if !strings.Contains(detail, test.reason) || strings.Contains(detail, "admin.") || strings.Contains(detail, "是否已保存") || strings.Contains(detail, "无法确认后台是否已接收") {
				t.Fatalf("preflight result was vague or unconfirmed: %s", detail)
			}
			if test.step == "judging" && !strings.Contains(detail, "本次没有保存或修改人工判断") {
				t.Fatal("judgment rejection pretends a new batch was created")
			}
			if test.step == "history" && !strings.Contains(detail, "可以重新读取") {
				t.Fatal("read rejection lacks read-only retry guidance")
			}
		})
	}
	if calls, _, records := repo.createMetrics(); calls != 0 || records != 0 {
		t.Fatal("preflight rejection created records")
	}
}

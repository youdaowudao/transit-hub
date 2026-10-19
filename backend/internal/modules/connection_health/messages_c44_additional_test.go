package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const c44PrivateThought = "不应保存的思考"
const c44Body = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":" answer "}}`

func c44Stop(reason string) string {
	b, _ := json.Marshal(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": reason}})
	return c44Frame(string(b))
}
func c44Complete(reason string) string {
	return c44Frame(c44Body) + c44Stop(reason) + c44Frame(`{"type":"message_stop"}`)
}
func c44Ask(t *testing.T, body string, status int, contentType string, protocol TestProtocol) QuestionAnswerAskResult {
	t.Helper()
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		var request map[string]any
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if protocol == TestProtocolMessages {
			if request["stream"] != true || request["max_tokens"] != float64(64000) {
				t.Error("Messages streaming budget missing")
			}
		} else {
			if request["stream"] == true {
				t.Error("other protocol gained stream=true")
			}
			if _, ok := request["max_tokens"]; ok {
				t.Error("other protocol gained token budget")
			}
		}
		out := protocolServiceResponse(req, status, body)
		out.Header.Set("Content-Type", contentType)
		return out, nil
	})
	return runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}, "m", "q", QuestionAnswerReasoningEffortHigh, protocol)
}
func c44AssertResult(t *testing.T, out QuestionAnswerAskResult, answer, errorType, excerpt string) {
	t.Helper()
	if out.Answer != answer || out.ErrorType != errorType || out.UpstreamExcerpt != excerpt {
		t.Fatalf("got answer=%q error=%q excerpt=%q; want %q/%q/%q", out.Answer, out.ErrorType, out.UpstreamExcerpt, answer, errorType, excerpt)
	}
	if errorType != "" && out.UpstreamStatus != 200 {
		t.Fatalf("lost response status: %d", out.UpstreamStatus)
	}
	if strings.Contains(out.Answer, c44PrivateThought) || strings.Contains(out.UpstreamExcerpt, c44PrivateThought) {
		t.Fatal("thinking entered answer or evidence")
	}
}
func TestMessagesC44QuestionAnswerStreamFramingAndCompletion(t *testing.T) {
	cases := []struct{ name, body, answer, errorType, excerpt string }{
		{"event_name", "event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\n" + c44Stop("end_turn") + "event: message_stop\ndata: {}\n\n", "answer", "", ""},
		{"done", c44Frame(c44Body) + c44Stop("end_turn") + c44Frame("[DONE]"), "answer", "", ""},
		{"crlf", strings.ReplaceAll(c44Complete("end_turn"), "\n", "\r\n"), "answer", "", ""},
		{"multiline_data", "data: {\"type\":\"content_block_start\",\n" + "data:\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"answer\"}}\n\n" + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "answer", "", ""},
		{"final_stop_incomplete", c44Stop("max_tokens") + `data: {"type":"message_stop","thinking":"不应保存的思考"}`, "", "response_incomplete", "max_tokens"},
		{"final_stop_success", c44Frame(c44Body) + c44Stop("end_turn") + `data: {"type":"message_stop"}`, "answer", "", ""},
		{"final_error", `data: {"type":"error","error":{"message":"具体说明"}}`, "", "server_error", "具体说明"},
		{"final_response_failed", `data: {"type":"response.failed","response":{"error":{"message":"响应具体说明"}}}`, "", "server_error", "响应具体说明"},
		{"empty_frames", ": keepalive\n\n\n\nevent: ping\n\n" + c44Frame(c44Body) + ": keepalive\n\n\n" + c44Frame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" next"}}`) + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "answer  next", "", ""},
		{"empty_frame_clears_event", "event: error\n\ndata: {}\n\n" + c44Complete("end_turn"), "answer", "", ""},
		{"invalid_json", c44Frame(`{"type":"content_block_delta","delta":{"thinking":"不应保存的思考"}`), "", "invalid_response", ""},
		{"stop_sequence", c44Complete("stop_sequence"), "answer", "", ""},
		{"thinking_only", c44Frame(`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"不应保存的思考"}}`) + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "", "invalid_response", "end_turn"},
		{"blank_body", c44Frame(`{"type":"content_block_start","content_block":{"type":"text","text":" \t "}}`) + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "", "invalid_response", "end_turn"},
		{"ordered_blocks", c44Frame(`{"type":"content_block_start","index":4,"content_block":{"type":"text","text":" second "}}`) + c44Frame(c44Body) + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "answer\nsecond", "", ""},
		{"last_stop_incomplete", c44Frame(c44Body) + c44Stop("end_turn") + c44Stop("max_tokens") + c44Frame(`{"type":"message_stop"}`), "", "response_incomplete", "max_tokens"},
		{"last_stop_success", c44Frame(c44Body) + c44Stop("max_tokens") + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`), "answer", "", ""},
		{"null_error", c44Frame(`{"type":"ping","error":null}`) + c44Complete("end_turn"), "answer", "", ""},
		{"done_without_reason", c44Frame("[DONE]"), "", "invalid_response", ""},
		{"stop_without_reason", c44Frame(`{"type":"message_stop","thinking":"不应保存的思考"}`), "", "invalid_response", ""},
	}
	for _, reason := range []string{"refusal", "tool_use", "pause_turn", "unknown", ""} {
		cases = append(cases, struct{ name, body, answer, errorType, excerpt string }{"reason_" + reason, c44Complete(reason), "", "invalid_response", reason})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c44AssertResult(t, c44Ask(t, tc.body, 200, "Text/Event-Stream; charset=utf-8", TestProtocolMessages), tc.answer, tc.errorType, tc.excerpt)
		})
	}
}

func TestMessagesC44QuestionAnswerFixedExcerpts(t *testing.T) {
	cases := []struct{ name, event, excerpt string }{
		{"string_error", `{"type":"error","error":"该模型最多允许输出 32768 个 token"}`, "该模型最多允许输出 32768 个 token"},
		{"explanation_before_code", `{"type":"error","error":"invalid_request_error","message":"具体限制"}`, "具体限制"},
		{"nested_order", `{"type":"error","error":{"message":""},"response":{"error":{"message":"具体限制"}},"message":"兜底说明"}`, "具体限制"},
		{"whitespace", `{"type":"error","error":{"message":" \t "},"message":"具体限制"}`, "具体限制"},
		{"nul", `{"type":"error","error":{"message":"\u0000"},"message":"具体限制"}`, "具体限制"},
		{"all_clean_empty", `{"type":"error","error":{"message":"\u0000"},"response":{"error":{"message":" \t "}},"message":""}`, ""},
		{"nested_thinking", `{"type":"error","error":{"message":"具体限制","thinking":"不应保存的思考"}}`, "具体限制"},
		{"array_message", `{"type":"error","error":{"message":["不应保存的思考"]},"message":"具体限制"}`, "具体限制"},
		{"object_message", `{"type":"error","error":{"message":{"thinking":"不应保存的思考"}}}`, ""},
		{"response_nested", `{"type":"response.failed","response":{"error":{"message":"具体限制"}}}`, "具体限制"},
		{"response_string", `{"type":"response.failed","response":{"error":"具体限制"}}`, "具体限制"},
		{"top_error_first", `{"type":"response.failed","error":{"message":"首选说明"},"response":{"error":{"message":"具体限制"}}}`, "首选说明"},
		{"event_error_message", "event: error\ndata: {\"message\":\"该模型当前不可用\"}\n\n", "该模型当前不可用"},
		{"event_error_empty", "event: error\ndata: {}\n\n", ""},
		{"thinking_with_error", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"不应保存的思考"},"error":{"message":"具体限制"}}`, "具体限制"},
		{"redacted", `{"type":"error","error":{"message":"具体限制 fixture-key"}}`, "具体限制 ***"},
		{"truncate", `{"type":"error","message":"` + strings.Repeat("界", 510) + `"}`, strings.Repeat("界", 500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.event
			if !strings.HasPrefix(body, "event:") {
				body = c44Frame(body)
			}
			c44AssertResult(t, c44Ask(t, body, 200, "text/event-stream", TestProtocolMessages), "", "server_error", tc.excerpt)
		})
	}
	for _, reason := range []string{" \t ", "\x00", " \x00unknown fixture-key \x00 "} {
		t.Run("stop_clean_"+reason, func(t *testing.T) {
			want := ""
			if strings.Contains(reason, "unknown") {
				want = "unknown ***"
			}
			c44AssertResult(t, c44Ask(t, c44Complete(reason), 200, "text/event-stream", TestProtocolMessages), "", "invalid_response", want)
		})
	}
	c44AssertResult(t, c44Ask(t, c44Frame(`{"type":"message_delta","delta":{"stop_reason":"max_tokens","thinking":"不应保存的思考"},"usage":{"thinking":"不应保存的思考"}}`)+c44Frame(`{"type":"message_stop"}`), 200, "text/event-stream", TestProtocolMessages), "", "response_incomplete", "max_tokens")
}

// Limit the physical read so an attempt to consume anything after the terminal
// frame is observable, regardless of Scanner's normal read-ahead behavior.
type c44TerminalReader struct {
	text string
	t    *testing.T
}

func (r *c44TerminalReader) Read(p []byte) (int, error) {
	if r.text == "" {
		r.t.Error("read after terminal frame")
		return 0, errors.New("unexpected read")
	}
	p[0] = r.text[0]
	r.text = r.text[1:]
	return 1, nil
}
func TestMessagesC44QuestionAnswerStopsReadingAtTerminalFrame(t *testing.T) {
	body := &c44TerminalReader{text: c44Complete("end_turn"), t: t}
	c44AssertResult(t, readMessagesQuestionAnswerStream(context.Background(), body, "", QuestionAnswerAskResult{UpstreamStatus: 200}), "answer", "", "")
}

func TestMessagesC44QuestionAnswerSizeBoundaries(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", ""} {
		for _, extra := range []int{0, 1} {
			t.Run("line_"+strings.ReplaceAll(ending, "\n", "LF")+string(rune('0'+extra)), func(t *testing.T) {
				prefix, suffix := `data: {"type":"message_stop","padding":"`, `"}`
				line := prefix + strings.Repeat("x", (1<<20)-len(prefix)-len(suffix)+extra) + suffix + ending
				body := c44Frame(c44Body) + c44Stop("end_turn") + line
				want, answer := "", "answer"
				if extra == 1 {
					want, answer = "response_too_large", ""
				}
				c44AssertResult(t, c44Ask(t, body, 200, "text/event-stream", TestProtocolMessages), answer, want, "")
			})
		}
	}
	prefix := c44Frame(c44Body) + c44Stop("end_turn")
	terminal := c44Frame(`{"type":"message_stop"}`)
	padding := func(n int) string {
		var b strings.Builder
		for n > 0 {
			size := n
			if size > 8192 {
				size = 8192
			}
			if size == 1 {
				b.WriteByte('\n')
			} else {
				b.WriteString(":" + strings.Repeat("x", size-2) + "\n")
			}
			n -= size
		}
		return b.String()
	}
	for _, last := range []string{terminal, c44Frame(`{"type":"error","error":{"message":"should not save"}}`), c44Frame(`{"type":"ping"}`)} {
		for _, extra := range []int{0, 1} {
			if extra == 0 && last != terminal {
				continue
			}
			t.Run("total_"+last+string(rune('0'+extra)), func(t *testing.T) {
				body := prefix + padding((16<<20)-len(prefix)-len(last)+extra) + last
				want := ""
				answer := "answer"
				if extra == 1 {
					want = "response_too_large"
					answer = ""
				}
				c44AssertResult(t, c44Ask(t, body, 200, "text/event-stream", TestProtocolMessages), answer, want, "")
			})
		}
	}
}

func TestMessagesC44QuestionAnswerFallbackAndOtherProtocols(t *testing.T) {
	for _, tc := range []struct{ body, errorType, answer string }{{`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`, "", "answer"}, {`{"type":"message","stop_reason":"max_tokens","content":[]}`, "response_incomplete", ""}} {
		out := c44Ask(t, tc.body, 200, "application/json", TestProtocolMessages)
		if out.Answer != tc.answer || out.ErrorType != tc.errorType {
			t.Fatalf("fallback changed: %+v", out)
		}
		if tc.errorType != "" && out.UpstreamExcerpt != tc.body {
			t.Fatal("JSON fallback evidence changed")
		}
	}
	out := c44Ask(t, `{"error":{"type":"rate_limit_error","message":"rate limit"}}`, 429, "text/event-stream", TestProtocolMessages)
	if out.ErrorType != "rate_limited" || out.UpstreamStatus != 429 || !strings.Contains(out.UpstreamExcerpt, "rate limit") {
		t.Fatalf("non2xx must keep existing path: %+v", out)
	}
	for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
		body := `{"choices":[{"message":{"content":"answer"}}]}`
		if protocol == TestProtocolResponses {
			body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}]}`
		}
		out := c44Ask(t, body, 200, "application/json", protocol)
		if out.ErrorType != "" || out.Answer != "answer" {
			t.Fatalf("other protocol changed: %+v", out)
		}
	}
}

type c44ContextBody struct {
	ctx     context.Context
	started chan struct{}
	final   string
}

func (b *c44ContextBody) Read(p []byte) (int, error) {
	if b.started != nil {
		close(b.started)
		b.started = nil
	}
	<-b.ctx.Done()
	if b.final != "" {
		n := copy(p, b.final)
		b.final = b.final[n:]
		return n, nil
	}
	return 0, b.ctx.Err()
}
func (b *c44ContextBody) Close() error { return nil }
func TestMessagesC44QuestionAnswerDeadlineAndCancellation(t *testing.T) {
	for _, afterResult := range []bool{false, true} {
		t.Run(map[bool]string{false: "reading_deadline", true: "dispatcher_deadline_after_error"}[afterResult], func(t *testing.T) {
			s, _ := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				final := ""
				if afterResult {
					final = c44Frame(`{"type":"error","error":{"message":"具体限制"}}`)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &c44ContextBody{ctx: req.Context(), final: final}, Request: req}, nil
			}))
			s.questionAnswerMessagesTTL = 20 * time.Millisecond
			record := c44Start(t, s)
			want := ""
			if afterResult {
				want = "具体限制"
			}
			if record.ErrorType != "timeout" || record.UpstreamExcerpt != want || record.AnswerJudgment != nil || record.UpstreamStatus == nil || *record.UpstreamStatus != 200 {
				t.Fatalf("deadline result wrong: %+v", record)
			}
		})
	}
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual_cancel", true: "shutdown"}[shutdown], func(t *testing.T) {
			started := make(chan struct{})
			s, _ := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &c44ContextBody{ctx: req.Context(), started: started}, Request: req}, nil
			}))
			batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("stream not started")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if shutdown {
				err = s.ShutdownQuestionAnswers(ctx)
			} else {
				_, err = s.StopQuestionAnswerBatch(ctx, "user1", "sub2api:ws1:acc-a", batch.BatchID)
			}
			if err != nil {
				t.Fatal(err)
			}
			waitQuestionAnswerRunReleased(t, s)
			saved, err := s.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
			if err != nil || len(saved.Records) != 1 {
				t.Fatalf("records: %v", err)
			}
			r := saved.Records[0]
			if r.Status == QuestionAnswerSucceeded || r.AnswerBody != "" || r.AnswerJudgment != nil {
				t.Fatalf("cancelled stream judged: %+v", r)
			}
			wantStatus := QuestionAnswerCancelled
			wantError := ""
			if shutdown {
				wantStatus = QuestionAnswerFailed
				wantError = QuestionAnswerErrorServiceShutdown
			}
			if r.Status != wantStatus || r.ErrorType != wantError {
				t.Fatalf("existing terminal behavior changed: %+v", r)
			}
		})
	}
}

var _ io.ReadCloser = (*c44ContextBody)(nil)

func TestMessagesC44QuestionAnswerScheduledStreamBudgetDeadlineAndJudgment(t *testing.T) {
	execution := c2ScheduleExecutionFixture("accounts")
	execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "question", Name: "old name", Body: "old body", Keywords: []string{"old answer"}}}}
	execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", ExecutionID: execution.ID, TargetID: "sub2api:ws1:a", Status: "pending", AvailableModels: []string{"old-model"}, PlannedRequestCount: 1, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "ws1", InventoryComplete: true}, Protocol: TestProtocolMessages}}}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "new-group"}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture", AnthropicAuth: "x_api_key"}}}, allAccounts: []upstream.AdminGroupAccountInfo{{ID: "a", Models: "new-model"}}}
	qa := newFakeQuestionAnswerRepository()
	repo := &c2ScheduleRecoveryRepository{c2SchedulePreparationRepository: &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "new body", Keywords: []string{"new answer"}, Enabled: true}}, configs: []GroupTestConfig{{AdminGroupID: "new-group", Protocol: TestProtocolChatCompletions}}}, execution: execution, qa: qa}
	service, models := c2SchedulePreparationFixture(t, reader, repo.c2SchedulePreparationRepository)
	t.Cleanup(func() {
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service.questionAnswerSchedules = repo
	service.questionAnswers = qa
	service.accounts = fakeAdminAccountResolver{id: "ws2"}
	workspaces := make(chan string, 2)
	service.mySites = c2ScheduleRecoveryMySitesReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, workspaces: workspaces}
	type requestFact struct{ path, body string }
	requests := make(chan requestFact, 2)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil || len(payload) != 5 || payload["max_tokens"] != float64(64000) || payload["stream"] != true {
			t.Errorf("scheduled Messages budget or stream changed: %s", body)
		}
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) < 19*time.Minute || time.Until(deadline) > 20*time.Minute {
			t.Error("scheduled Messages did not get 20 minute deadline")
		}
		requests <- requestFact{request.URL.Path, string(body)}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(c44Frame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"old answer"}}`) + c44Stop("end_turn") + c44Frame(`{"type":"message_stop"}`))), Request: request}, nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.buildDone:
	case <-time.After(time.Second):
		t.Fatal("recovery did not finish building from frozen facts")
	}
	deadline := time.Now().Add(time.Second)
	var batch QuestionAnswerBatch
	for time.Now().Before(deadline) {
		var err error
		batch, err = service.getQuestionAnswerBatchCore(context.Background(), execution.UserID, execution.Targets[0].TargetID, "exact")
		if err == nil && !batch.Active {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if batch.Active || len(batch.Records) != 1 || batch.Records[0].RequestProtocol == nil || *batch.Records[0].RequestProtocol != TestProtocolMessages || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].QuestionBody != "old body" || batch.Records[0].ModelName != "old-model" || batch.Records[0].AnswerJudgment == nil || *batch.Records[0].AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("recovery lost frozen request/automatic judgment: %+v", batch)
	}
	if repo.reads.Load() != 0 || models.Load() != 0 || repo.creates.Load() != 1 || reader.credentialCalls.Load() != 1 || len(workspaces) != 1 || <-workspaces != "ws1" {
		t.Fatal("recovery reread current facts, rediscovered models, or used switched workspace")
	}
	select {
	case fact := <-requests:
		var payload struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal([]byte(fact.body), &payload); err != nil || payload.OutputConfig.Effort != "medium" {
			t.Fatalf("nil record effort did not use existing default: %s", fact.body)
		}
		if fact.path != "/v1/messages" || !strings.Contains(fact.body, "old body") || !strings.Contains(fact.body, "old-model") || strings.Contains(fact.body, "new body") || strings.Contains(fact.body, "new-model") || len(requests) != 0 {
			t.Fatalf("recovery request violated frozen protocol/model/question: %+v", fact)
		}
	default:
		t.Fatal("recovered request was not dispatched through C1")
	}
}

type c44BrokenReader struct{}

func (c44BrokenReader) Read([]byte) (int, error) { return 0, errors.New("fixture connection reset") }
func TestMessagesC44QuestionAnswerReadErrorDiscardsPartialTextAndEvidence(t *testing.T) {
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(c44Frame(c44Body)), c44BrokenReader{})), Request: req}, nil
	})
	result := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}, "m", "q", QuestionAnswerReasoningEffortHigh, TestProtocolMessages)
	c44AssertResult(t, result, "", "network", "")
}
func TestMessagesC44QuestionAnswerResponsesRetainsTenMinuteDeadline(t *testing.T) {
	s, qa := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) < 9*time.Minute || time.Until(deadline) > 10*time.Minute {
			t.Error("Responses deadline changed")
		}
		return protocolServiceResponse(req, 200, `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first second"}]}]}`), nil
	}))
	if err := s.repo.(*fakeRepository).SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolResponses, 20}); err != nil {
		t.Fatal(err)
	}
	qa.mu.Lock()
	qa.configurations[0].Protocol = TestProtocolResponses
	qa.mu.Unlock()
	record := c44Start(t, s)
	if record.Status != QuestionAnswerSucceeded {
		t.Fatalf("Responses regression: %+v", record)
	}
}

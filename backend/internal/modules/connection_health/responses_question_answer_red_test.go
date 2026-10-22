package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

// Critical business expectations owned by the primary agent. Implementation must
// not change these expectations to accommodate the parser.
const responsesQAComplete = `{"id":"resp_final","status":"completed","output":[{"type":"reasoning"},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first answer"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"second answer"}]}]}`

func responsesQAFrame(event string) string { return "data: " + event + "\n\n" }

func responsesQAAsk(t *testing.T, ctx context.Context, status int, contentType string, reader io.Reader, key string) QuestionAnswerAskResult {
	t.Helper()
	body := &trackingBody{Reader: reader}
	calls := 0
	runner := NewQuestionAnswerRunner()
	runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}, "X-Private": []string{"do-not-save-response-headers"}}, Body: body, Request: req}, nil
	})
	result := runner.AskDetailed(ctx, upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: key}, "model", "question", QuestionAnswerReasoningEffortMedium, TestProtocolResponses)
	if calls != 1 || !body.closed {
		t.Fatalf("must make exactly one request and close body: calls=%d closed=%v", calls, body.closed)
	}
	return result
}

func TestResponsesQuestionAnswerT01StreamingRequest(t *testing.T) {
	runner := NewQuestionAnswerRunner()
	calls := 0
	runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		reasoning, _ := payload["reasoning"].(map[string]any)
		input, _ := json.Marshal(payload["input"])
		if req.URL.Path != "/v1/responses" || payload["stream"] != true || payload["store"] != false || payload["model"] != "model" || reasoning["effort"] != "medium" || string(input) != `[{"content":"question","role":"user"}]` {
			t.Errorf("Responses question request must retain snapshot and enable streaming: %v", payload)
		}
		for _, field := range []string{"max_output_tokens", "max_tokens", "messages", "previous_response_id"} {
			if _, exists := payload[field]; exists {
				t.Errorf("unexpected field %s", field)
			}
		}
		return protocolServiceResponse(req, 200, responsesQAComplete), nil
	})
	r := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key"}, "model", "question", QuestionAnswerReasoningEffortMedium, TestProtocolResponses)
	if r.ErrorType != "" || r.Answer != "first answer\nsecond answer" || calls != 1 {
		t.Fatalf("result=%+v calls=%d", r, calls)
	}
}

func TestResponsesQuestionAnswerT02CompleteSnapshotOnly(t *testing.T) {
	prefix := ": keepalive\n\n" + responsesQAFrame(`{"type":"response.created","response":{"id":"resp_initial"}}`) +
		responsesQAFrame(`{"type":"response.reasoning_text.delta","delta":"private thinking"}`) + responsesQAFrame(`{"type":"response.output_text.delta","delta":"partial answer"}`)
	complete := responsesQAFrame(`{"type":"response.completed","response":` + responsesQAComplete + `}`)
	r := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(prefix+complete), "fixture-key")
	if r.ErrorType != "" || r.Answer != "first answer\nsecond answer" || r.UpstreamExcerpt != "" || r.UpstreamStatus != 0 {
		t.Fatalf("must use only complete snapshot: %+v", r)
	}
	for _, end := range []string{"", "data: [DONE]\n\n", responsesQAFrame(`{"type":"response.output_text.done","text":"partial answer"}`)} {
		r = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(prefix+end), "fixture-key")
		if r.ErrorType != QuestionAnswerErrorNetwork || r.Answer != "" || !strings.Contains(r.UpstreamExcerpt, "resp_initial") || strings.Contains(r.UpstreamExcerpt, "private thinking") || strings.Contains(r.UpstreamExcerpt, "partial answer") {
			t.Fatalf("incomplete stream must retain safe reference without answer: %+v", r)
		}
	}
}

func TestResponsesQuestionAnswerT06ErrorAfterLongThinking(t *testing.T) {
	thought := responsesQAFrame(`{"type":"response.reasoning_text.delta","delta":"` + strings.Repeat("x", 8192) + `"}`)
	body := responsesQAFrame(`{"type":"response.created","response":{"id":"resp_initial"}}`) + strings.Repeat(thought, 384) +
		responsesQAFrame(`{"type":"response.failed","response":{"id":"resp_error","status":"failed","error":{"code":"upstream_error","message":"specific tail failure"}}}`)
	r := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(body), "fixture-key")
	if r.ErrorType != QuestionAnswerErrorServer || r.UpstreamStatus != 200 || r.Answer != "" {
		t.Fatalf("actual stream error after 3MiB reasoning must be retained: %+v", r)
	}
	for _, want := range []string{"specific tail failure", "resp_error", "response.failed", "本题不判对错"} {
		if !strings.Contains(r.UpstreamExcerpt, want) {
			t.Errorf("missing %q in %q", want, r.UpstreamExcerpt)
		}
	}
	if strings.Contains(r.UpstreamExcerpt, "resp_initial") || strings.Contains(r.UpstreamExcerpt, strings.Repeat("x", 16)) || len([]rune(r.UpstreamExcerpt)) > 500 {
		t.Fatalf("unsafe or misleading excerpt: %q", r.UpstreamExcerpt)
	}
}

func TestResponsesQuestionAnswerT07ExplicitOutputLimitOnly(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "content_filter", "unknown_reason", ""} {
		for _, stream := range []bool{false, true} {
			response := `{"id":"resp_incomplete","status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"output":[]}`
			body := response
			if stream {
				body = responsesQAFrame(`{"type":"response.incomplete","response":` + response + `}`)
			}
			r := responsesQAAsk(t, context.Background(), 200, "application/json", strings.NewReader(body), "fixture-key")
			want := QuestionAnswerErrorInvalidResponse
			if reason == "max_output_tokens" {
				want = QuestionAnswerErrorResponseIncomplete
			}
			if r.ErrorType != want || r.Answer != "" {
				t.Errorf("stream=%v reason=%s result=%+v want=%s", stream, reason, r, want)
			}
			if reason != "" && !strings.Contains(r.UpstreamExcerpt, reason) {
				t.Errorf("reason must survive: %q", r.UpstreamExcerpt)
			}
			if reason != "max_output_tokens" && strings.Contains(r.UpstreamExcerpt, "输出上限") {
				t.Errorf("must not invent token exhaustion: %q", r.UpstreamExcerpt)
			}
		}
	}
}

func TestResponsesQuestionAnswerT09UpstreamTimeoutIsNotLocalDeadline(t *testing.T) {
	for _, message := range []string{"upstream_error: upstream timeout", "upstream_error: socket hang up"} {
		r := responsesQAAsk(t, context.Background(), 502, "application/json", strings.NewReader(`{"error":{"message":"`+message+`"}}`), "fixture-key")
		if r.ErrorType != QuestionAnswerErrorServer || r.UpstreamStatus != 502 || r.Answer != "" || !strings.Contains(r.UpstreamExcerpt, message) || !strings.Contains(r.UpstreamExcerpt, "本题不判对错") {
			t.Fatalf("upstream failure must preserve actual reason: %+v", r)
		}
		if strings.Contains(r.UpstreamExcerpt, "本地已等满") || strings.Contains(r.UpstreamExcerpt, "输出上限") {
			t.Fatalf("invented cause: %q", r.UpstreamExcerpt)
		}
	}
}

func TestResponsesQuestionAnswerT09Non2xxSSEPreservesActualError(t *testing.T) {
	body := responsesQAFrame(`{"type":"response.created","response":{"id":"resp_before_http_failure"}}`) +
		responsesQAFrame(`{"type":"response.reasoning_text.delta","delta":"must never persist this thinking"}`) +
		responsesQAFrame(`{"type":"response.failed","response":{"id":"resp_http_failure","error":{"message":"upstream_error: upstream timeout"}}}`)
	r := responsesQAAsk(t, context.Background(), 502, "text/event-stream", strings.NewReader(body), "fixture-key")
	if r.ErrorType != QuestionAnswerErrorServer || r.UpstreamStatus != 502 || r.Answer != "" || !strings.Contains(r.UpstreamExcerpt, "upstream_error: upstream timeout") || !strings.Contains(r.UpstreamExcerpt, "resp_http_failure") {
		t.Fatalf("HTTP failure must still extract its actual structured SSE error: %+v", r)
	}
	if strings.Contains(r.UpstreamExcerpt, "must never persist this thinking") || strings.Contains(r.UpstreamExcerpt, "resp_before_http_failure") {
		t.Fatalf("HTTP failure must not fall back to the beginning of the SSE body: %+v", r)
	}
}

func TestResponsesQuestionAnswerReviewJSONNestedError(t *testing.T) {
	for _, status := range []int{200, 502} {
		for _, value := range []any{map[string]any{"code": "upstream_error", "message": "upstream_error: socket hang up"}, "upstream_error: socket hang up"} {
			payload, _ := json.Marshal(map[string]any{"id": "resp_json_root", "status": "failed", "response": map[string]any{"id": "resp_nested_not_metadata", "error": value}})
			got := responsesQAAsk(t, context.Background(), status, "application/json", strings.NewReader(string(payload)), "fixture-key")
			want := QuestionAnswerErrorInvalidResponse
			if status == 502 {
				want = QuestionAnswerErrorServer
			}
			if got.ErrorType != want || got.Answer != "" || !strings.Contains(got.UpstreamExcerpt, "upstream_error: socket hang up") || !strings.Contains(got.UpstreamExcerpt, "resp_json_root") || strings.Contains(got.UpstreamExcerpt, "resp_nested_not_metadata") || strings.Contains(got.UpstreamExcerpt, "没有说明具体失败原因") {
				t.Errorf("JSON metadata must stay at root while actual nested error survives: %+v", got)
			}
			if _, object := value.(map[string]any); object && !strings.Contains(got.UpstreamExcerpt, "错误码：upstream_error") {
				t.Errorf("nested code lost: %q", got.UpstreamExcerpt)
			}
		}
	}
	payload := `{"id":"resp_priority","error":{"message":"outer first","code":"outer_code"},"response":{"error":{"message":"nested second","code":"nested_code"}},"message":"top third"}`
	got := responsesQAAsk(t, context.Background(), 502, "application/json", strings.NewReader(payload), "fixture-key")
	if !strings.Contains(got.UpstreamExcerpt, "outer first") || !strings.Contains(got.UpstreamExcerpt, "outer_code") || strings.Contains(got.UpstreamExcerpt, "nested second") || strings.Contains(got.UpstreamExcerpt, "top third") || strings.Contains(got.UpstreamExcerpt, "nested_code") {
		t.Fatalf("fixed JSON error priority changed: %+v", got)
	}
}

func TestResponsesQuestionAnswerReviewDeadlineKeepsCompleteReference(t *testing.T) {
	protocol := TestProtocolResponses
	id := "resp_" + strings.Repeat("z", 75)
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"id": id, "status": "failed", "error": map[string]any{"code": "upstream_error", "message": strings.Repeat("长", 900)}}})
	got := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(string(payload))), "fixture-key")
	if got.ErrorType != QuestionAnswerErrorServer || !strings.Contains(got.UpstreamExcerpt, id) || !strings.Contains(got.UpstreamExcerpt, "已省略后文") {
		t.Fatalf("invalid real parser setup: %+v", got)
	}
	completion := questionAnswerCompletionFromAsk(got, true, QuestionAnswerRecord{RequestProtocol: &protocol}, "fixture-key")
	if completion.Status != QuestionAnswerFailed || completion.ErrorType != QuestionAnswerErrorTimeout || completion.AnswerBody != "" || completion.AnswerJudgment != nil || completion.UpstreamStatus == nil || *completion.UpstreamStatus != 200 || len([]rune(completion.UpstreamExcerpt)) > 500 || !strings.HasPrefix(completion.UpstreamExcerpt, "本地等待到期；以下为此前收到的信息") || !strings.Contains(completion.UpstreamExcerpt, "已省略后文") || !strings.HasSuffix(completion.UpstreamExcerpt, "响应编号："+id) {
		t.Fatalf("deadline override must not truncate a validated response ID or keep a partial answer: %+v", completion)
	}
}

package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"
)

func responsesQACompletedFrame() string {
	return responsesQAFrame(`{"type":"response.completed","response":` + responsesQAComplete + `}`)
}

func TestResponsesQuestionAnswerT03BodyDetection(t *testing.T) {
	for _, body := range []string{responsesQAComplete, "\xef\xbb\xbf \r\n\t" + responsesQAComplete, responsesQACompletedFrame(), "\xef\xbb\xbf \r\n\t" + responsesQACompletedFrame()} {
		for _, contentType := range []string{"application/json", "text/event-stream", "text/plain"} {
			result := responsesQAAsk(t, context.Background(), 200, contentType, strings.NewReader(body), "account-key")
			if result.ErrorType != "" || result.Answer != "first answer\nsecond answer" || result.UpstreamStatus != 0 || result.UpstreamExcerpt != "" {
				t.Fatalf("content type %s must follow actual body: %+v", contentType, result)
			}
		}
	}
	for _, body := range []string{"not an answer", "[]", "null", "datax: unsupported\n\n"} {
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(body), "account-key")
		if result.ErrorType != QuestionAnswerErrorInvalidResponse || result.Answer != "" || strings.Contains(result.UpstreamExcerpt, body) {
			t.Fatalf("unknown body must fail without persisting arbitrary body: %+v", result)
		}
	}
}

func TestResponsesQuestionAnswerT04SSEFramingAndInvalidEvents(t *testing.T) {
	body := ": comment\r\n\r\nid: ignored\r\nretry: 1000\r\n\r\nevent: extension\r\ndata: {\"type\":\"extension\",\r\ndata: \"ignored\":true}\r\n\r\n" +
		"event: response.completed\r\ndata: {\"response\":" + responsesQAComplete + "}\r\n\r\n"
	result := responsesQAAsk(t, context.Background(), 200, "application/json", iotest.OneByteReader(strings.NewReader(body)), "account-key")
	if result.ErrorType != "" || result.Answer != "first answer\nsecond answer" {
		t.Fatalf("CRLF/multidata/fragmented event must complete: %+v", result)
	}
	for _, body := range []string{
		"event: response.failed\ndata: {\"type\":\"response.completed\",\"response\":" + responsesQAComplete + "}\n\n",
		"data: broken JSON\n\n", "data: []\n\n", "data: null\n\n", "data: 1\n\n", "data: {\"type\":1}\n\n",
	} {
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(body+responsesQACompletedFrame()), "account-key")
		if result.ErrorType != QuestionAnswerErrorInvalidResponse || result.Answer != "" {
			t.Fatalf("invalid event must stop before a later completion: %+v", result)
		}
	}
}

type responsesQAErrorTail struct{ body string }

func (r *responsesQAErrorTail) Read(data []byte) (int, error) {
	n := copy(data, r.body)
	r.body = r.body[n:]
	if len(r.body) == 0 {
		return n, errors.New("fixture connection interrupted")
	}
	return n, nil
}

func TestResponsesQuestionAnswerT05OnlyValidNormalCompletion(t *testing.T) {
	for _, snapshot := range []string{
		`{"status":"completed","output":[]}`,
		`{"status":"completed","output":[{"type":"message","role":"assistant","status":"in_progress","content":[{"type":"output_text","text":"unfinished"}]}]}`,
		`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","text":"refused"}]}]}`,
	} {
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.completed","response":`+snapshot+`}`)), "account-key")
		if result.ErrorType != QuestionAnswerErrorInvalidResponse || result.Answer != "" {
			t.Fatalf("invalid completion snapshot must never become an answer: %+v", result)
		}
	}
	result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(strings.TrimSuffix(responsesQACompletedFrame(), "\n\n")), "account-key")
	if result.ErrorType != "" || result.Answer != "first answer\nsecond answer" {
		t.Fatalf("normal EOF may finish its last frame: %+v", result)
	}
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", &responsesQAErrorTail{body: strings.TrimSuffix(responsesQACompletedFrame(), "\n\n")}, "account-key")
	if result.ErrorType != QuestionAnswerErrorNetwork || result.Answer != "" {
		t.Fatalf("connection error must discard unterminated final frame: %+v", result)
	}
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.completed","error":{"message":"terminal error"},"response":`+responsesQAComplete+`}`)), "account-key")
	if result.ErrorType != QuestionAnswerErrorServer || result.Answer != "" || !strings.Contains(result.UpstreamExcerpt, "terminal error") {
		t.Fatalf("an explicit error prevents completion: %+v", result)
	}
}

func TestResponsesQuestionAnswerT06T08FixedErrorsAndHTTPClassification(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"message":"first explanation","code":"failure_code"},"message":"unused fallback"}`,
		`{"type":"response.failed","response":{"error":{"message":"first explanation","type":"failure_code"}}}`,
		`{"type":"error","message":"first explanation","code":"failure_code"}`,
		`{"type":"error","error":"first explanation","code":"failure_code"}`,
		`{"type":"response.failed","response":{"error":"first explanation"},"code":"failure_code"}`,
	} {
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(payload)), "account-key")
		if result.ErrorType != QuestionAnswerErrorServer || result.Answer != "" || result.UpstreamStatus != 200 || !strings.Contains(result.UpstreamExcerpt, "first explanation") || !strings.Contains(result.UpstreamExcerpt, "failure_code") || strings.Contains(result.UpstreamExcerpt, "unused fallback") {
			t.Fatalf("fixed field priority must retain actual error: %+v", result)
		}
	}
	for _, test := range []struct{ payload, want string }{
		{`{"type":"error","code":"rate_limit_exceeded","message":"limited"}`, QuestionAnswerErrorRateLimited},
		{`{"type":"error","error":{"type":"rate_limit_error"}}`, QuestionAnswerErrorRateLimited},
		{`{"type":"response.failed","response":{"error":{"code":"too_many_requests"}}}`, QuestionAnswerErrorRateLimited},
		{`{"type":"error","error":{"status":429}}`, QuestionAnswerErrorRateLimited},
		{`{"type":"error","error":{"code":"unknown_error","message":"rate_limit_exceeded in ordinary text"}}`, QuestionAnswerErrorServer},
		{`{"type":"error","error":{"code":"overloaded"}}`, QuestionAnswerErrorServer},
	} {
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(test.payload)), "account-key")
		if result.ErrorType != test.want || result.Answer != "" {
			t.Fatalf("classification must use only explicit structured errors: %+v want %s", result, test.want)
		}
	}
	for _, test := range []struct {
		status int
		want   string
	}{{400, QuestionAnswerErrorInvalidResponse}, {404, QuestionAnswerErrorInvalidResponse}, {401, QuestionAnswerErrorAuth}, {403, QuestionAnswerErrorAuth}, {429, QuestionAnswerErrorRateLimited}, {502, QuestionAnswerErrorServer}} {
		result := responsesQAAsk(t, context.Background(), test.status, "application/json", strings.NewReader(responsesQAComplete), "account-key")
		if result.ErrorType != test.want || result.Answer != "" || result.UpstreamStatus != test.status {
			t.Fatalf("non-2xx body cannot turn HTTP failure into success: %+v", result)
		}
	}
	result := responsesQAAsk(t, context.Background(), 200, "application/json", strings.NewReader(`{"error":{"code":"rate_limit_exceeded","message":"limited"}}`), "account-key")
	if result.ErrorType != QuestionAnswerErrorInvalidResponse {
		t.Fatalf("complete JSON must retain old classification: %+v", result)
	}
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.reasoning_text.delta","delta":"rate_limit_exceeded","message":"private thought"}`)+responsesQACompletedFrame()), "account-key")
	if result.ErrorType != "" {
		t.Fatalf("ordinary reasoning cannot classify a rate limit: %+v", result)
	}
	result = responsesQAAsk(t, context.Background(), 502, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.reasoning_text.delta","delta":"private thought"}`)+responsesQACompletedFrame()), "account-key")
	if result.ErrorType != QuestionAnswerErrorServer || result.Answer != "" || strings.Contains(result.UpstreamExcerpt, "private thought") || strings.Contains(result.UpstreamExcerpt, "first answer") {
		t.Fatalf("non-2xx SSE cannot preserve reasoning or answer fragments: %+v", result)
	}
}

func TestResponsesQuestionAnswerT08Non2xxSSECannotSucceedOrOverrideHTTPClass(t *testing.T) {
	for _, body := range []string{responsesQACompletedFrame(), "\xef\xbb\xbf \r\n" + responsesQACompletedFrame()} {
		result := responsesQAAsk(t, context.Background(), 502, "text/event-stream", strings.NewReader(body), "account-key")
		if result.ErrorType != QuestionAnswerErrorServer || result.UpstreamStatus != 502 || result.Answer != "" || strings.Contains(result.UpstreamExcerpt, "first answer") || strings.Contains(result.UpstreamExcerpt, "second answer") || !strings.Contains(result.UpstreamExcerpt, "resp_final") {
			t.Fatalf("non-2xx completion cannot return or retain an answer: %+v", result)
		}
	}
	body := responsesQAFrame(`{"type":"error","error":{"code":"rate_limit_exceeded","message":"explicit stream limit"},"response":{"id":"resp_http_auth"}}`)
	result := responsesQAAsk(t, context.Background(), 403, "text/event-stream", strings.NewReader(body), "account-key")
	if result.ErrorType != QuestionAnswerErrorAuth || result.UpstreamStatus != 403 || result.Answer != "" || !strings.Contains(result.UpstreamExcerpt, "explicit stream limit") || !strings.Contains(result.UpstreamExcerpt, "resp_http_auth") {
		t.Fatalf("HTTP classification must retain precedence over stream classification: %+v", result)
	}
}

func responsesQACommentPadding(size int) string {
	var padding strings.Builder
	for size > 0 {
		n := size
		if n > 32768 {
			n = 32768
		}
		if n == 1 {
			padding.WriteByte('\n')
		} else {
			padding.WriteByte(':')
			padding.WriteString(strings.Repeat(" ", n-2))
			padding.WriteByte('\n')
		}
		size -= n
	}
	return padding.String()
}

func TestResponsesQuestionAnswerT10ExactSizeBoundaries(t *testing.T) {
	complete := responsesQACompletedFrame()
	frameBase := "data: {\"type\":\"extension\",\ndata: \"delta\":\""
	// The SSE data field values join with one LF; framing bytes do not count
	// toward the frame budget, but do count toward the total stream budget.
	frameDataBase := len(`{"type":"extension",` + "\n" + `"delta":""}`)
	for _, extra := range []int{0, 1} {
		for _, test := range []struct{ name, body string }{
			{"line", ":" + strings.Repeat("x", maxProbeResponseBytes-1+extra) + "\r\n" + complete},
			{"frame", frameBase + strings.Repeat("x", maxProbeResponseBytes-frameDataBase+extra) + "\"}\n\n" + complete},
			{"total", responsesQACommentPadding(responsesQuestionAnswerStreamLimit-len(complete)+extra) + complete},
			{"json", "\xef\xbb\xbf" + strings.Repeat(" ", maxProbeResponseBytes-len(responsesQAComplete)-3+extra) + responsesQAComplete},
		} {
			t.Run(fmt.Sprintf("%s-extra-%d", test.name, extra), func(t *testing.T) {
				result := responsesQAAsk(t, context.Background(), 200, "application/json", strings.NewReader(test.body), "account-key")
				if extra == 0 {
					if result.ErrorType != "" || result.Answer != "first answer\nsecond answer" {
						t.Fatalf("exact limit must allow a complete answer: %+v", result)
					}
				} else if result.ErrorType != QuestionAnswerErrorResponseTooLarge || result.Answer != "" {
					t.Fatalf("one byte over limit must fail before completion: %+v", result)
				}
			})
		}
		for _, prefix := range []string{"", "\xef\xbb\xbf"} {
			body := prefix + strings.Repeat(" ", maxProbeResponseBytes-len(prefix)+extra)
			result := responsesQAAsk(t, context.Background(), 200, "application/json", strings.NewReader(body), "account-key")
			want := QuestionAnswerErrorInvalidResponse
			if extra > 0 {
				want = QuestionAnswerErrorResponseTooLarge
			}
			if result.ErrorType != want || result.Answer != "" {
				t.Fatalf("blank prefix length %d must respect non-SSE limit: %+v", len(body), result)
			}
		}
	}
	// Size checks apply to failure frames before interpreting their error.
	result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQACommentPadding(responsesQuestionAnswerStreamLimit)+responsesQAFrame(`{"type":"error","message":"too late"}`)), "account-key")
	if result.ErrorType != QuestionAnswerErrorResponseTooLarge || strings.Contains(result.UpstreamExcerpt, "too late") {
		t.Fatalf("error beyond total budget cannot bypass size limit: %+v", result)
	}
	// Whitespace before format detection still belongs to the first SSE line.
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(strings.Repeat(" ", maxProbeResponseBytes)+":"+"\n"+complete), "account-key")
	if result.ErrorType != QuestionAnswerErrorResponseTooLarge || result.Answer != "" {
		t.Fatalf("leading whitespace cannot bypass the line budget: %+v", result)
	}
}

func TestResponsesQuestionAnswerT11ReferencesAndMessageBudget(t *testing.T) {
	for _, id := range []string{"resp_last", "resp_" + strings.Repeat("x", 75), "resp_" + strings.Repeat("x", 76), "resp_bad.id", "other_id"} {
		payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"id": id, "status": "failed", "error": map[string]any{"message": strings.Repeat("长", 900), "code": "failure_code"}}})
		body := responsesQAFrame(`{"type":"response.created","response":{"id":"resp_previous"}}`) + responsesQAFrame(string(payload))
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(body), "account-key")
		for _, text := range []string{"failure_code", "response.failed", "failed", "说明较长，已省略后文", "本题不判对错"} {
			if !strings.Contains(result.UpstreamExcerpt, text) {
				t.Fatalf("lost reserved metadata %s: %q", text, result.UpstreamExcerpt)
			}
		}
		valid := id == "resp_last" || len(id) == 80
		if valid && (!strings.Contains(result.UpstreamExcerpt, id) || strings.Contains(result.UpstreamExcerpt, "resp_previous")) {
			t.Fatalf("terminal valid ID must win: %q", result.UpstreamExcerpt)
		}
		if !valid && (!strings.Contains(result.UpstreamExcerpt, "resp_previous") || strings.Contains(result.UpstreamExcerpt, id)) {
			t.Fatalf("invalid ID cannot be shortened/persisted: %q", result.UpstreamExcerpt)
		}
		if utf8.RuneCountInString(result.UpstreamExcerpt) > 500 {
			t.Fatal("message budget exceeded")
		}
	}
	result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"unexpected_private_name","response":{"status":"private_status"}}`)), "account-key")
	if strings.Contains(result.UpstreamExcerpt, "unexpected_private_name") || strings.Contains(result.UpstreamExcerpt, "private_status") || !strings.Contains(result.UpstreamExcerpt, "无法识别的事件") {
		t.Fatalf("unrecognized metadata must not be copied: %+v", result)
	}
	payload, _ := json.Marshal(map[string]any{"type": "response.incomplete", "message": strings.Repeat("长", 900), "response": map[string]any{"id": "resp_reason", "status": "incomplete", "incomplete_details": map[string]any{"reason": "content_filter"}}})
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(string(payload))), "account-key")
	if !strings.Contains(result.UpstreamExcerpt, "content_filter") || !strings.Contains(result.UpstreamExcerpt, "resp_reason") || !strings.Contains(result.UpstreamExcerpt, "已省略后文") || utf8.RuneCountInString(result.UpstreamExcerpt) > 500 {
		t.Fatalf("long message must not displace actual incomplete reason: %+v", result)
	}
}

func TestResponsesQuestionAnswerT12SafeFieldsAndComposition(t *testing.T) {
	for _, text := range []string{"literal fixture-secret", `\u0066ixture-secret`, strings.Repeat(`\`, 512) + "fixture-secret", "fixture-\x00secret", "fixture-\xffsecret", "Bearer abcdefghijklmnop", "sk-abcdefghijklmnop"} {
		encoded, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"message": text, "code": "fixture-secret"}, "response": map[string]any{"id": "resp_fixture-secret"}})
		result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(string(encoded))), "fixture-secret")
		if result.ErrorType != QuestionAnswerErrorServer || strings.Contains(result.UpstreamExcerpt, "fixture-secret") || strings.Contains(result.UpstreamExcerpt, "abcdefghijklmnop") || !utf8.ValidString(result.UpstreamExcerpt) || strings.ContainsRune(result.UpstreamExcerpt, 0) || utf8.RuneCountInString(result.UpstreamExcerpt) > 500 {
			t.Fatalf("unsafe failure details: %q", result.UpstreamExcerpt)
		}
		decoded := result.UpstreamExcerpt
		for layer := 0; layer < 8; layer++ {
			decoded = questionAnswerDecodeJSONEscapes(decoded)
			if strings.Contains(decoded, "fixture-secret") {
				t.Fatal("encoded key escaped safety checks")
			}
		}
	}
	// Field-level redaction can recreate the exact key; reject that field.
	result := responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"error","message":"testtesttest***"}`)), "test***")
	if strings.Contains(result.UpstreamExcerpt, "test***") || strings.Contains(result.UpstreamExcerpt, "testtest") {
		t.Fatalf("unsafe field survived: %q", result.UpstreamExcerpt)
	}
	// The composed text itself must also pass the existing exact-key checker.
	result = responsesQuestionAnswerFailure(context.Background(), "test***", QuestionAnswerAskResult{UpstreamStatus: 200}, QuestionAnswerErrorServer, responsesQuestionAnswerEvidence{}, "testtesttest***")
	if result.UpstreamExcerpt != "" {
		t.Fatalf("unsafe composition must be discarded: %q", result.UpstreamExcerpt)
	}
}

func TestResponsesQuestionAnswerT13T14DeadlineAndLocalCauses(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	body := io.MultiReader(strings.NewReader(responsesQAFrame(`{"type":"response.created","response":{"id":"resp_started"}}`)), &responsesQAErrorTail{body: "data: unfinished"})
	result := readResponsesQuestionAnswer(ctx, body, "account-key", QuestionAnswerAskResult{UpstreamStatus: 200})
	if result.Answer != "" || result.ErrorType != QuestionAnswerErrorTimeout || !strings.Contains(result.UpstreamExcerpt, "本地已等满") || !strings.Contains(result.UpstreamExcerpt, "resp_started") {
		t.Fatalf("local deadline must retain known reference without an answer: %+v", result)
	}
	protocol := TestProtocolResponses
	record := QuestionAnswerRecord{RequestProtocol: &protocol}
	got := questionAnswerCompletionFromAsk(QuestionAnswerAskResult{Answer: "partial", ErrorType: QuestionAnswerErrorServer, UpstreamStatus: 502, UpstreamExcerpt: "earlier upstream explanation"}, true, record, "account-key")
	if got.Status != QuestionAnswerFailed || got.ErrorType != QuestionAnswerErrorTimeout || got.AnswerBody != "" || got.AnswerJudgment != nil || !strings.HasPrefix(got.UpstreamExcerpt, "本地等待到期；以下为此前收到的信息") || !strings.Contains(got.UpstreamExcerpt, "earlier upstream explanation") {
		t.Fatalf("deadline must retain precedence and label older evidence: %+v", got)
	}
	protocol = TestProtocolChatCompletions
	got = questionAnswerCompletionFromAsk(QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorServer, UpstreamExcerpt: "unchanged chat evidence"}, true, record, "account-key")
	if got.UpstreamExcerpt != "unchanged chat evidence" {
		t.Fatalf("Chat evidence changed: %+v", got)
	}
	result = responsesQAAsk(t, context.Background(), 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.failed","response":{"id":"resp_no_reason"}}`)), "account-key")
	if result.ErrorType != QuestionAnswerErrorServer || !strings.Contains(result.UpstreamExcerpt, "没有说明具体失败原因") || strings.Contains(result.UpstreamExcerpt, "输出上限") {
		t.Fatalf("unknown failure must not invent cause: %+v", result)
	}
}

func TestResponsesQuestionAnswerT14PostgresTerminalProtection(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,request_protocol) VALUES ('responses-f','responses-user','responses-target','responses-batch','model','q','Q','Body','running','responses'),('responses-c','responses-user','responses-target','responses-batch','model','q','Q','Body','running','responses'),('responses-s','responses-user','responses-target','responses-batch','model','q','Q','Body','running','responses')`)
	if err != nil {
		t.Fatal(err)
	}
	failed := responsesQAAsk(t, ctx, 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.failed","response":{"id":"resp_pg_failure","error":{"message":"persisted concrete error"}}}`)), "account-key")
	protocol := TestProtocolResponses
	record := QuestionAnswerRecord{RequestProtocol: &protocol}
	failedCompletion := questionAnswerCompletionFromAsk(failed, false, record, "account-key")
	if ok, err := repository.CompleteQuestionAnswer(ctx, "responses-user", "responses-batch", "responses-f", failedCompletion); err != nil || !ok {
		t.Fatalf("failure saved=%v/%v", ok, err)
	}
	succeeded := responsesQAAsk(t, ctx, 200, "text/event-stream", strings.NewReader(responsesQACompletedFrame()), "account-key")
	if ok, err := repository.CompleteQuestionAnswer(ctx, "responses-user", "responses-batch", "responses-s", questionAnswerCompletionFromAsk(succeeded, false, record, "account-key")); err != nil || !ok {
		t.Fatalf("success saved=%v/%v", ok, err)
	}
	if _, err := repository.FinalizeQuestionAnswerBatch(ctx, "responses-user", "responses-target", "responses-batch", QuestionAnswerCancelled, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"responses-f", "responses-c"} {
		if ok, err := repository.CompleteQuestionAnswer(ctx, "responses-user", "responses-batch", id, failedCompletion); err != nil || ok {
			t.Fatalf("terminal %s replaced=%v/%v", id, ok, err)
		}
	}
	records, err := repository.ListQuestionAnswerBatch(ctx, "responses-user", "responses-target", "responses-batch")
	if err != nil || len(records) != 3 {
		t.Fatalf("read records=%d/%v", len(records), err)
	}
	for _, saved := range records {
		switch saved.ID {
		case "responses-f":
			if saved.Status != QuestionAnswerFailed || saved.AnswerBody != "" || saved.AnswerJudgment != nil || saved.UpstreamStatus == nil || *saved.UpstreamStatus != 200 || saved.UpstreamExcerpt != failed.UpstreamExcerpt {
				t.Fatalf("failure changed after finalization: %+v", saved)
			}
		case "responses-c":
			if saved.Status != QuestionAnswerCancelled || saved.AnswerBody != "" || saved.AnswerJudgment != nil || saved.UpstreamStatus != nil || saved.UpstreamExcerpt != "" {
				t.Fatalf("cancellation retained failure details: %+v", saved)
			}
		case "responses-s":
			if saved.Status != QuestionAnswerSucceeded || saved.AnswerBody != "first answer\nsecond answer" || saved.UpstreamStatus != nil || saved.UpstreamExcerpt != "" {
				t.Fatalf("success retained failure details: %+v", saved)
			}
		}
	}
}

func TestResponsesQuestionAnswerT14FailurePersistsThroughOriginalBatchAPI(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='running' WHERE id=$1`, fixture.failedRecordID); err != nil {
		t.Fatal(err)
	}
	result := responsesQAAsk(t, ctx, 200, "text/event-stream", strings.NewReader(responsesQAFrame(`{"type":"response.failed","response":{"id":"resp_persisted","error":{"message":"persisted tail failure"}}}`)), "account-key")
	protocol := TestProtocolResponses
	completion := questionAnswerCompletionFromAsk(result, false, QuestionAnswerRecord{RequestProtocol: &protocol}, "account-key")
	repository := NewRepository(fixture.pool)
	if completed, err := repository.CompleteQuestionAnswer(ctx, "handler-user", fixture.failedBatchID, fixture.failedRecordID, completion); err != nil || !completed {
		t.Fatalf("complete=%v/%v", completed, err)
	}
	path := fmt.Sprintf("/api/connection-health/targets/%s/question-answers/batches/%s", fixture.targetID, fixture.failedBatchID)
	response := fixture.request(t, http.MethodGet, path, "", "3")
	if response.Code != 200 {
		t.Fatalf("API status=%d", response.Code)
	}
	var batch struct{ Records []QuestionAnswerRecord }
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 {
		t.Fatalf("records=%d", len(batch.Records))
	}
	saved := batch.Records[0]
	if saved.UpstreamExcerpt != result.UpstreamExcerpt || saved.UpstreamStatus == nil || *saved.UpstreamStatus != 200 || saved.Status != QuestionAnswerFailed || saved.AnswerBody != "" || saved.AnswerJudgment != nil || !strings.Contains(saved.UpstreamExcerpt, "resp_persisted") {
		t.Fatalf("API lost persisted failure evidence: %+v", saved)
	}
	if updated, err := repository.CompleteQuestionAnswer(ctx, "handler-user", fixture.failedBatchID, fixture.failedRecordID, questionAnswerTestCompletion(QuestionAnswerSucceeded, "late answer", "")); err != nil || updated {
		t.Fatalf("late completion must not replace failure: %v/%v", updated, err)
	}
}

var _ io.Reader = (*responsesQAErrorTail)(nil)

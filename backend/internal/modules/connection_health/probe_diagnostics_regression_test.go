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
)

func TestProbeDiagnosticsHTTPFailuresHaveRealStatusAndNoFirstText(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 429, 500, 502, 503, 529} {
		for _, contentType := range []string{"application/json", "text/event-stream"} {
			t.Run(fmt.Sprint(status)+contentType, func(t *testing.T) {
				body := `{"error":{"type":"api_error","message":"Service temporarily unavailable fixture-secret"}}`
				out := runProbeDiagnostic(status, contentType, body)
				if out.Result != classifyTestHTTPResponse(TestProtocolResponses, status, []byte(body), "fixture-secret", 18).Result {
					t.Fatalf("HTTP classification changed: %s", out.Result)
				}
				for _, want := range []string{fmt.Sprintf("HTTP %d", status), "等待回答时收到接口错误", "Service temporarily unavailable", "一次性测试"} {
					if !strings.Contains(out.Detail, want) {
						t.Errorf("missing %q: %s", want, out.Detail)
					}
				}
				if out.FirstTokenMs != nil || out.FirstEventMs != nil || out.NonStreaming || out.LatencyMs <= 0 {
					t.Errorf("failure fabricated measurements: %+v", out)
				}
				for _, absent := range []string{"上游未返回流式", "fixture-secret", "余额", "防护"} {
					if strings.Contains(out.Detail, absent) {
						t.Errorf("unsafe/misleading detail contains %q", absent)
					}
				}
			})
		}
	}
	for _, status := range []int{500, 503} {
		out := runProbeDiagnostic(status, "application/json", `{"error":{"code":"unsupported_parameter","message":"rejected"}}`)
		if out.Result != ResultServerError || strings.Contains(out.Detail, "不增加健康失败次数") {
			t.Fatalf("HTTP service failure was excluded: %+v", out)
		}
	}
}

func TestProbeDiagnosticsLocalValidationKeepsConfirmedCause(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		protocol                 TestProtocol
		contentType, body, cause string
	}{
		{"malformed", TestProtocolResponses, "text/event-stream", "data: {broken\n\n", "流式事件不是合法 JSON"},
		{"messages stop", TestProtocolMessages, "text/event-stream", "data: {\"type\":\"message_stop\"}\n\n", "回答结束但没有有效正文"},
		{"chat done", TestProtocolChatCompletions, "text/event-stream", "data: [DONE]\n\n", "回答结束但没有有效正文"},
		{"chat finish", TestProtocolChatCompletions, "text/event-stream", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", "回答结束但没有有效正文"},
		{"incomplete", TestProtocolResponses, "text/event-stream", "data: {\"type\":\"response.incomplete\"}\n\n", "响应未完成"},
		{"completed invalid", TestProtocolResponses, "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{}}\n\n", "回答已结束，但回复格式或正文无效"},
		{"malformed json body", TestProtocolResponses, "application/json", `{broken`, "回复格式或正文无效，未能识别有效回答"},
		{"invalid json body", TestProtocolResponses, "application/json", `{"message":"body unavailable"}`, "回复格式或正文无效，未能识别有效回答"},
		{"oversized json", TestProtocolResponses, "application/json", strings.Repeat("x", maxProbeResponseBytes+10), "回复超过 1 MiB，无法继续校验"},
		{"oversized frame", TestProtocolResponses, "text/event-stream", "data: " + strings.Repeat("x", maxProbeResponseBytes+10), "回复超过 1 MiB，无法继续校验"},
		{"total oversized", TestProtocolResponses, "text/event-stream", strings.Repeat(": keepalive "+strings.Repeat("x", 1000)+"\n\n", 1100), "回复超过 1 MiB，无法继续校验"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, req := probeDiagnosticFixture(201, tc.contentType, tc.body)
			req.Protocol = tc.protocol
			out := runner.Probe(context.Background(), req)
			for _, want := range []string{"HTTP 201", "未能确认有效回答", "已知原因：" + tc.cause} {
				if !strings.Contains(out.Detail, want) {
					t.Errorf("missing %q: %s", want, out.Detail)
				}
			}
			if out.Result != ResultInvalidResponse || out.FirstTokenMs != nil {
				t.Errorf("validation behavior changed: %+v", out)
			}
			for _, absent := range []string{"开始接收回答时收到错误", "上游没有提供可安全显示的具体原因", "{broken", "上游未返回流式"} {
				if strings.Contains(out.Detail, absent) {
					t.Errorf("confirmed cause replaced by %q: %s", absent, out.Detail)
				}
			}
			if tc.name == "invalid json body" && !strings.Contains(out.Detail, "上游提示：body unavailable") {
				t.Error("safe upstream reason lost")
			}
		})
	}
}

func TestProbeDiagnosticsReadingFailureAndEOF(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/event-stream"} {
		runner, req := probeDiagnosticFixture(200, contentType, "")
		runner.client.Transport = taskATransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(io.MultiReader(strings.NewReader("data: {unfinished"), probeDiagnosticErrorReader{})), Request: r}, nil
		})
		out := runner.Probe(context.Background(), req)
		if out.Result != ResultNetworkFluctuation || out.RequestPhase != "reading_body" || !strings.Contains(out.Detail, "读取接口回复时中断（HTTP 200）") || !strings.Contains(out.Detail, "fixture read reset") || strings.Contains(out.Detail, "fixture-secret") || strings.Contains(out.Detail, "unfinished") {
			t.Errorf("read failure lost actual cause or exposed partial body: %+v", out)
		}
	}
	out := runProbeDiagnostic(200, "text/event-stream", ": keepalive\n\n")
	if out.Result != ResultNetworkFluctuation || !strings.Contains(out.Detail, "流式回答中途断开或未结束") || !strings.Contains(out.Detail, "HTTP 200") {
		t.Errorf("EOF diagnostics=%+v", out)
	}
	runner, req := probeDiagnosticFixture(200, "", "")
	runner.client.Transport = taskATransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture transport failure") })
	if out = runner.Probe(context.Background(), req); strings.Contains(out.Detail, "HTTP ") || out.RequestPhase != "waiting_headers" {
		t.Errorf("invented HTTP status before response: %+v", out)
	}
}

type probeDiagnosticErrorReader struct{}

func (probeDiagnosticErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("fixture read reset fixture-secret")
}

func TestProbeDiagnosticsSafeExcerptsAndFullFrameExtraction(t *testing.T) {
	// A complete JSON error can also reach local body validation on HTTP 200.
	// Keep the known validation reason before the exact compatibility guidance.
	jsonCompatibility := runProbeDiagnostic(200, "application/json", `{"error":{"code":"unsupported_parameter","message":"parameter rejected"}}`)
	for _, want := range []string{"未能确认有效回答", "回复格式或正文无效，未能识别有效回答", "上游提示：parameter rejected", "本次不增加健康失败次数"} {
		if !strings.Contains(jsonCompatibility.Detail, want) {
			t.Errorf("complete JSON compatibility diagnostic lost %q: %s", want, jsonCompatibility.Detail)
		}
	}
	if jsonCompatibility.Result != ResultInvalidResponse || jsonCompatibility.FirstTokenMs != nil {
		t.Fatal("local JSON failure classification or measurements changed")
	}
	for _, body := range []string{
		`{"request":{"secret":"do-not-display"}}`, `[{"message":"do-not-display"}]`, `true`, `123`,
	} {
		out := runProbeDiagnostic(503, "application/json", body)
		if !strings.Contains(out.Detail, "上游没有提供可安全显示的具体原因") || strings.Contains(out.Detail, "do-not-display") {
			t.Errorf("unsafe JSON excerpt: %s", out.Detail)
		}
	}
	for _, body := range []string{`{"error":{"message":"explicit reason"},"message":"other"}`, `{"response":{"error":{"message":"explicit reason"}}}`, `{"message":"explicit reason"}`, `{"error":"explicit reason"}`, `{"response":{"error":"explicit reason"}}`, `"explicit reason"`, `explicit reason`} {
		if out := runProbeDiagnostic(503, "application/json", body); !strings.Contains(out.Detail, "explicit reason") {
			t.Errorf("reason lost: %s", out.Detail)
		}
	}
	key := "fixture-\"escaped\\key"
	message := "已知原因 " + key + "\x00 Bearer abcdefghijklmnopqrs " + strings.Repeat("中文", 400)
	encoded, _ := json.Marshal(map[string]any{"padding": strings.Repeat("x", 700), "type": "error", "error": map[string]any{"code": "unsupported_parameter", "message": message}})
	runner, req := probeDiagnosticFixture(200, "text/event-stream", "data: "+string(encoded)+"\n\n")
	req.UpstreamKey = key
	out := runner.Probe(context.Background(), req)
	if !strings.Contains(out.Detail, "在开始接收回答时收到错误（HTTP 200）") || strings.Contains(out.Detail, "等待回答时收到接口错误") {
		t.Error("SSE error described as rejected HTTP request")
	}
	if !strings.Contains(out.Detail, "已知原因") || !strings.Contains(out.Detail, "不增加健康失败次数") || out.FirstEventMs == nil || out.FirstTokenMs != nil {
		t.Errorf("full-frame reason or SSE event timing lost: %+v", out)
	}
	for _, absent := range []string{key, "abcdefghijklmnopqrs", "\x00", "padding", "�"} {
		if strings.Contains(out.Detail, absent) {
			t.Errorf("unsafe excerpt contains %q", absent)
		}
	}
	if len([]rune(out.Detail)) > 850 {
		t.Errorf("Unicode excerpt not bounded: %d", len([]rune(out.Detail)))
	}
	if got := probeOutcomeWithAuditMetadata(probeOutcomeWithAuditMetadata(out)); got.Detail != out.Detail {
		t.Error("audit formatting is not idempotent")
	}
}

func TestProbeDiagnosticsNormalJSONAndLegacyMeasurements(t *testing.T) {
	body := `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}]}`
	out := runProbeDiagnostic(200, "application/json", body)
	if out.Result != ResultOK || out.FirstTokenMs == nil || out.FirstEventMs == nil || !out.NonStreaming || strings.Count(probeOutcomeWithAuditMetadata(out).Detail, "上游未返回流式") != 1 {
		t.Errorf("normal JSON changed: %+v", out)
	}
	runner, req := probeDiagnosticFixture(503, "application/json", `{"message":"old legacy detail"}`)
	req.LegacyCompatibility = true
	got := runner.Probe(context.Background(), req)
	want := classifyHTTPResponse(503, []byte(`{"message":"old legacy detail"}`), req.UpstreamKey, got.LatencyMs)
	if got.Detail != want.Detail || got.Result != want.Result || got.NonStreaming || got.FirstTokenMs != nil {
		t.Errorf("legacy changed: got=%+v want=%+v", got, want)
	}
	// An old externally constructed failure must never receive the success-only audit note.
	if out = probeOutcomeWithAuditMetadata(ProbeOutcome{Result: ResultServerError, NonStreaming: true, Detail: "known failure"}); out.Detail != "known failure" {
		t.Errorf("audit still labels errors as non-streaming: %s", out.Detail)
	}
}

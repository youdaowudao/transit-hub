package connection_health

import "testing"

func TestResponsesCompletionRequiresEntireUsableOutput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"reasoning_before_messages", `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"second"}]}]}`, true},
		{"top_error", `{"status":"completed","error":{"code":"bad"},"output":[]}`, false},
		{"incomplete", `{"status":"incomplete","output":[]}`, false},
		{"queued", `{"status":"queued","output":[]}`, false},
		{"in_progress", `{"status":"in_progress","output":[]}`, false},
		{"mixed_refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no"}]}]}`, false},
		{"mixed_incomplete", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]},{"type":"message","role":"assistant","status":"incomplete","content":[]}]}`, false},
		{"blank", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"  "}]}]}`, false},
		{"tool_only", `{"status":"completed","output":[{"type":"function_call","name":"ok"}]}`, false},
		{"chat_shape", `{"choices":[{"message":{"content":"ok"}}]}`, false},
		{"sse", "data: {\"status\":\"completed\"}\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, qa := range []bool{false, true} {
				text, ok := decodeTestResponse(TestProtocolResponses, []byte(tc.body), qa)
				if ok != tc.valid {
					t.Fatalf("qa=%v valid=%v want=%v", qa, ok, tc.valid)
				}
				if tc.valid && text != "first\nsecond" {
					t.Fatalf("all assistant messages required, got %q", text)
				}
			}
		})
	}
}

func TestProtocolHTTPFailureClassificationPreservesLegacy(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   ResultKey
	}{
		{404, `{"error":{"code":"model_not_found"}}`, ResultModelNotFound},
		{404, `{"error":{"code":"unsupported_endpoint"}}`, ResultInvalidResponse},
		{404, `missing`, ResultInvalidResponse},
		{400, `{"error":{"code":"unsupported_parameter"}}`, ResultInvalidResponse},
		{401, `bad`, ResultAuth}, {403, `bad`, ResultAuth}, {429, `bad`, ResultRateLimited}, {503, `bad`, ResultServerError},
	} {
		out := classifyTestHTTPResponse(TestProtocolResponses, tc.status, []byte(tc.body), "secret", 10)
		if out.Result != tc.want {
			t.Fatalf("status=%d body=%s result=%s want=%s", tc.status, tc.body, out.Result, tc.want)
		}
	}
	if out := classifyHTTPResponse(404, []byte("legacy missing"), "", 10); out.Result != ResultModelNotFound {
		t.Fatalf("legacy classification changed: %+v", out)
	}
}

func TestChatProbeAndQuestionAnswerKeepDifferentAcceptance(t *testing.T) {
	for _, body := range []string{`{"choices":[{"message":{"content":""}}]}`, `{"choices":[{"message":{"reasoning_content":"reasoning"}}]}`} {
		if _, ok := decodeTestResponse(TestProtocolChatCompletions, []byte(body), false); !ok {
			t.Fatal("old probe acceptance was removed")
		}
		if _, ok := decodeTestResponse(TestProtocolChatCompletions, []byte(body), true); ok {
			t.Fatal("QA accepted a non-evaluable answer")
		}
	}
}

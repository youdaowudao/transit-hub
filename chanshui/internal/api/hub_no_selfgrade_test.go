package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/iq"
)

type hubGradeTransport func(*http.Request) (*http.Response, error)

func (f hubGradeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The upstream lies when asked to grade itself. A wrong answer must stay wrong,
// and neither its canonical answer nor a grading request may reach that upstream.
func TestHubWrongAnswerNeverAsksTestedModelToGradeItself(t *testing.T) {
	calls, leaked := 0, false
	g := gateway.Client{Mode: "anthropic", BaseURL: "https://fixture.invalid", NoRetry: true,
		HTTP: &http.Client{Transport: hubGradeTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			answer := "最终答案: 走路去"
			if strings.Contains(string(body), "标准答案") || strings.Contains(string(body), "给自己的上一轮回答打分") {
				leaked = true
				answer = "5"
			}
			start, _ := json.Marshal(map[string]any{"type": "message_start", "message": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": answer}},
				"usage":   map[string]int{"input_tokens": 10, "output_tokens": 10}, "stop_reason": "end_turn",
			}})
			sse := "data: " + string(start) + "\n\ndata: {\"type\":\"message_stop\"}\n\n"
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)), Request: r}, nil
		})}}
	result, err := (&Server{}).askQuestion(context.Background(), g, Request{Model: "fixture", Timeout: 10}, iq.GenCarWash())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || leaked || result.Earned != 0 {
		t.Fatalf("wrong answer must remain zero without self-grading: calls=%d leaked=%v earned=%d", calls, leaked, result.Earned)
	}
}

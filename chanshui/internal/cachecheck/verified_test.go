package cachecheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
)

type cacheTransport func(*http.Request) (*http.Response, error)

func (f cacheTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cacheReply(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func cacheJSON(protocol, usage string) string {
	if protocol == "messages" {
		return `{"content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":` + usage + `}`
	}
	return `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":` + usage + `}`
}

func measureUsage(t *testing.T, protocol string, usage ...string) Result {
	t.Helper()
	calls := 0
	c := gateway.Client{BaseURL: "https://fixture.invalid", Mode: protocol, RetryDelay: time.Millisecond,
		HTTP: &http.Client{Transport: cacheTransport(func(r *http.Request) (*http.Response, error) {
			if calls >= len(usage) {
				t.Fatalf("unexpected request %d", calls+1)
			}
			body := cacheJSON(protocol, usage[calls])
			calls++
			return cacheReply(200, "application/json", body), nil
		})}}
	result, err := MeasureVerified(t.Context(), c, "fixture-model", 1, nil)
	if err != nil || calls != cacheTurns {
		t.Fatalf("six requests should complete without added retry, calls %d, error %v", calls, err)
	}
	return result
}

func chatUsage(read, write, total int) string {
	return fmt.Sprintf(`{"prompt_tokens":%d,"cached_tokens":%d,"cache_write_tokens":%d}`, total, read, write)
}

func TestVerifiedCacheChainEvidence(t *testing.T) {
	first := chatUsage(0, 1800, 2000)
	warm := chatUsage(2000, 27, 2060)
	last := chatUsage(2057, 0, 2060)
	unknown := `{"prompt_tokens":2060,"cached_tokens":2000,"cache_write_tokens":null}`
	r := measureUsage(t, "chat", first, warm, warm, warm, warm, last)
	if r.Pass == nil || !*r.Pass || r.Expected != 2027 || r.Actual != 2057 || r.Delta != 30 {
		t.Fatalf("valid chain did not pass with expected transition: %+v", r)
	}
	if ratioText(r.Samples[5].HitRate) != "99.85" || ratioText(r.ThirdReadRatio) != "101.48" {
		t.Fatalf("hit rate %s, crossing ratio %s", ratioText(r.Samples[5].HitRate), ratioText(r.ThirdReadRatio))
	}
	for _, tc := range []struct {
		name  string
		usage []string
		known bool
	}{
		{"cannot exclude middle rewrite", []string{first, warm, unknown, warm, warm, last}, false},
		{"missing only possible growth", []string{first, chatUsage(1, 0, 2000), unknown, chatUsage(1, 0, 2000), chatUsage(1, 0, 2000), chatUsage(1, 0, 2000)}, false},
		{"missing last read", []string{first, warm, warm, warm, warm, `{"prompt_tokens":2060,"cached_tokens":null,"cache_write_tokens":0}`}, false},
		{"confirmed middle rewrite with missing data elsewhere", []string{first, chatUsage(0, 2000, 2060), unknown, warm, warm, last}, true},
		{"confirmed last failure with missing data elsewhere", []string{first, warm, unknown, warm, warm, chatUsage(1800, 0, 2060)}, true},
		{"fully observed without growth", []string{chatUsage(2000, 27, 2060), warm, warm, warm, warm, last}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := measureUsage(t, "chat", tc.usage...)
			if (r.Pass != nil) != tc.known || (r.Pass != nil && *r.Pass) {
				t.Fatalf("unexpected judgment %+v", r)
			}
		})
	}
}

func TestVerifiedCacheReadOnlyAndInputConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, cold, hot, end string
		known, pass, input, read       bool
	}{
		{"chat cold start", "chat", `{"prompt_tokens":1909}`, `{"prompt_tokens":2050,"cached_tokens":1792}`, `{"prompt_tokens":2039,"cached_tokens":1792}`, true, true, true, true},
		{"messages cold start", "messages", `{"input_tokens":1909}`, `{"input_tokens":258,"cache_read_input_tokens":1792}`, `{"input_tokens":247,"cache_read_input_tokens":1792}`, true, true, true, true},
		{"read-only zero", "chat", `{"prompt_tokens":200}`, `{"prompt_tokens":200,"cached_tokens":50}`, `{"prompt_tokens":200,"cached_tokens":0}`, true, false, true, true},
		{"inconsistent read", "chat", `{"prompt_tokens":10}`, `{"prompt_tokens":10,"cached_tokens":7}`, `{"prompt_tokens":10,"cached_tokens":20}`, false, false, true, false},
		{"missing final read", "chat", `{"prompt_tokens":10}`, `{"prompt_tokens":10,"cached_tokens":7}`, `{"prompt_tokens":10}`, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := measureUsage(t, tc.protocol, tc.cold, tc.hot, tc.hot, tc.hot, tc.hot, tc.end)
			s := r.Samples[5]
			if (r.Pass != nil) != tc.known || (r.Pass != nil && *r.Pass != tc.pass) ||
				s.InputReported != tc.input || s.ReadReported != tc.read || (s.HitRate != nil) != tc.read {
				t.Fatalf("judgment %v, last sample %+v, reason %s", r.Pass, s, r.Reason)
			}
			if tc.pass && (r.Samples[0].HitRate != nil || ratioText(s.HitRate) != "87.89") {
				t.Fatalf("cold and last hit rates: %s, %s", ratioText(r.Samples[0].HitRate), ratioText(s.HitRate))
			}
		})
	}
	invalid := chatUsage(100, 200, 200)
	r := measureUsage(t, "chat", chatUsage(0, 0, 200), invalid, invalid, invalid, invalid, chatUsage(300, 0, 400))
	if r.Pass != nil || !r.Samples[1].ReadReported || r.Samples[1].WriteReported || ratioText(r.Samples[1].HitRate) != "50.00" {
		t.Fatalf("impossible write participated or removed valid read evidence: %+v", r)
	}
	read := `{"prompt_tokens":200,"cached_tokens":100}`
	missing := `{"prompt_tokens":200}`
	r = measureUsage(t, "chat", missing, read, missing, read, read, read)
	if r.Pass != nil || r.Samples[2].HitRate != nil || ratioText(r.Samples[5].HitRate) != "50.00" {
		t.Fatalf("read-only gap participated in scoring: %+v", r)
	}
}

func TestVerifiedCacheOverflowEvidence(t *testing.T) {
	limit := gateway.CacheCountLimit
	warm := chatUsage(limit-100, 27, limit)
	r := measureUsage(t, "chat", chatUsage(0, 0, 1000), warm, warm, warm, warm, chatUsage(limit-73, 0, limit))
	if r.Pass == nil || !*r.Pass || r.Samples[1].Read != limit-100 || r.ThirdReadRatio == nil {
		t.Fatalf("aggregate read overflow hid valid individual links: %+v", r)
	}
	var stored map[string]json.RawMessage
	data, err := json.Marshal(r.ReportEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if _, has := stored["total_read"]; has || stored["samples"] == nil || stored["total_output"] == nil {
		t.Fatalf("unexpected evidence keys: %s", data)
	}
	readWrite := func(input, read, write int) string {
		return fmt.Sprintf(`{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}`, input, read, write)
	}
	baselineOverflow := readWrite(1, limit-100, 101)
	denominatorOverflow := readWrite(10, limit-100, 100)
	r = measureUsage(t, "messages", readWrite(10, 0, 100), denominatorOverflow, denominatorOverflow, denominatorOverflow, baselineOverflow, readWrite(0, limit, 0))
	if r.Pass != nil || r.Transitions[4].Pass != nil || r.ThirdReadRatio != nil || r.Samples[1].InputReported || r.Samples[1].HitRate != nil {
		t.Fatalf("unsafe addition used as evidence: %+v", r)
	}
	if !r.Samples[1].ReadReported || !r.Samples[1].WriteReported || r.Samples[5].Total != limit {
		t.Fatalf("overflow removed other valid evidence: %+v", r.Samples)
	}
}

func TestVerifiedCacheKeepsOriginalDecodeRetries(t *testing.T) {
	good := cacheJSON("messages", `{"input_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`)
	badUsage := `{"input_tokens":100,"cache_read_input_tokens":"bad"}`
	stop := "\n\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct {
		name, bad string
		stream    bool
	}{
		{"messages JSON", cacheJSON("messages", badUsage), false},
		{"stream start", `data: {"type":"message_start","message":` + cacheJSON("messages", badUsage) + `}` + stop, true},
		{"stream delta", `data: {"type":"message_start","message":` + good + "}\n\n" + `data: {"type":"message_delta","usage":` + badUsage + `}` + stop, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, retries := 0, 2
			c := gateway.Client{BaseURL: "https://fixture.invalid", Mode: "messages", RetryLimit: &retries, RetryDelay: time.Millisecond,
				HTTP: &http.Client{Transport: cacheTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return cacheReply(200, "application/json", good), nil
					}
					if tc.stream && calls == 2 {
						return cacheReply(400, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"stream must be true"}}`), nil
					}
					if tc.stream {
						return cacheReply(200, "text/event-stream", tc.bad), nil
					}
					return cacheReply(200, "application/json", tc.bad), nil
				})}}
			r, err := MeasureVerified(t.Context(), c, "fixture-model", 1, nil)
			if err == nil || calls != 4 || r.Pass != nil || len(r.Samples) != 2 || !r.RetriesObserved {
				t.Fatalf("retry or stopping behavior changed: calls %d, error %v, report %+v", calls, err, r)
			}
			if !r.Samples[0].ReadReported || r.Samples[0].Error != "" || ratioText(r.Samples[0].HitRate) != "0.00" ||
				r.Samples[1].Attempts != 3 || r.Samples[1].Error == "" || r.Samples[1].ReadReported || r.Samples[1].HitRate != nil {
				t.Fatalf("error sample contains verified data or discarded last successful sample: %+v", r.Samples)
			}
		})
	}
}

func TestVerifiedCacheCustomTemplateUsesOnlyFinalSuccessfulAttempt(t *testing.T) {
	calls := 0
	good := cacheJSON("chat", chatUsage(0, 0, 200))
	templates := &HubTemplates{Heading: "Custom heading", Line: "Custom block {{index}}", Turn: "Custom turn {{turn}}"}
	c := gateway.Client{BaseURL: "https://fixture.invalid", Mode: "chat", RetryDelay: time.Millisecond,
		HTTP: &http.Client{Transport: cacheTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			var body struct {
				Messages []struct{ Role, Content string }
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			turn := calls
			if calls >= 3 {
				turn--
			}
			if len(body.Messages) != 2*turn || body.Messages[0].Role != "system" ||
				!strings.HasPrefix(body.Messages[0].Content, "Custom heading\nCustom block 001\n") ||
				!strings.Contains(body.Messages[0].Content, "Custom block 170\n") ||
				body.Messages[len(body.Messages)-1].Content != fmt.Sprintf("Custom turn %d", turn) {
				t.Fatalf("request %d lost the custom template or earlier turns: %+v", calls, body.Messages)
			}
			if calls == 2 {
				bad := strings.Replace(cacheJSON("chat", chatUsage(100, 90, 200)), `"OK"`, `""`, 1)
				return cacheReply(200, "application/json", bad), nil
			}
			return cacheReply(200, "application/json", good), nil
		})}}
	r, err := MeasureVerified(t.Context(), c, "fixture-model", 1, templates)
	if err != nil || calls != 7 || r.Pass == nil || *r.Pass || !r.RetriesObserved || len(r.Samples) != 6 {
		t.Fatalf("expected the six final zero samples after one retry, calls %d, err %v, evidence %+v", calls, err, r)
	}
	if r.Second == nil || r.Second.Attempts != 2 || r.Second.Read != 0 || r.Second.Write != 0 || !r.Second.ReadReported || !r.Second.WriteReported ||
		r.Second.Error != "" || r.Second.RequestHash == "" || ratioText(r.Second.HitRate) != "0.00" || r.ThirdReadRatio != nil {
		t.Fatalf("invalid first attempt escaped into saved evidence: %+v", r.Second)
	}
}

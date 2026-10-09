package connection_health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A body controlled entirely in memory: no listener, external service or worker.
// After its first chunk, a blocking tail only exits when the HTTP context ends.
type firstTextProbeBody struct {
	chunk  string
	tail   string
	ctx    context.Context
	err    error
	block  bool
	cancel context.CancelFunc
	reads  int
	closed bool
	now    *time.Time
}

func (b *firstTextProbeBody) Read(p []byte) (int, error) {
	b.reads++
	*b.now = b.now.Add(25 * time.Millisecond)
	if b.chunk != "" {
		n := copy(p, b.chunk)
		b.chunk = b.chunk[n:]
		return n, nil
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.block {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	if b.err != nil {
		return 0, b.err
	}
	if b.tail != "" {
		n := copy(p, b.tail)
		b.tail = b.tail[n:]
		return n, nil
	}
	return 0, io.EOF
}
func (b *firstTextProbeBody) Close() error { b.closed = true; return nil }

func firstTextFrame(protocol TestProtocol) string {
	if protocol == TestProtocolResponses {
		return "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	}
	return "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
}

func firstTextRunner(b *firstTextProbeBody) *RealProbeRunner {
	return &RealProbeRunner{now: func() time.Time { return *b.now }, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
		b.ctx = req.Context()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: b}, nil
	})}}
}

func firstTextRequest(protocol TestProtocol) ProbeRequest {
	return ProbeRequest{Protocol: protocol, ProbeTimeoutSeconds: 20, BaseURL: "https://first-text.invalid", ModelName: "fixture-model", MaxTokens: 256}
}

func TestTaskAFirstTextReturnsBeforeBlockedTailAndReleasesRequest(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		t.Run(string(protocol), func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
			body := &firstTextProbeBody{chunk: firstTextFrame(protocol), block: true, now: &now}
			out := firstTextRunner(body).Probe(parent, firstTextRequest(protocol))
			if out.Result != ResultOK || body.reads != 1 || out.FirstTokenMs == nil || *out.FirstTokenMs != 25 || out.LatencyMs != 25 {
				t.Errorf("first text must immediately succeed without reading blocked tail: outcome=%+v reads=%d", out, body.reads)
			}
			if !body.closed || body.ctx.Err() != context.Canceled || parent.Err() != nil {
				t.Errorf("body and child must be released while parent stays valid: closed=%v child=%v parent=%v", body.closed, body.ctx.Err(), parent.Err())
			}
		})
	}
}

func TestTaskAFirstTextIgnoresAllLaterFailures(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		for _, tail := range []string{"data: {\"error\":{\"code\":\"rate_limit_exceeded\"}}\n\n", "data: not-json\n\n", ""} {
			t.Run(string(protocol)+"/"+tail, func(t *testing.T) {
				now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
				body := &firstTextProbeBody{chunk: firstTextFrame(protocol), tail: tail, now: &now}
				out := firstTextRunner(body).Probe(context.Background(), firstTextRequest(protocol))
				if out.Result != ResultOK || body.reads != 1 || !body.closed {
					t.Fatalf("later failures cannot change first-text success: %+v reads=%d closed=%v", out, body.reads, body.closed)
				}
			})
		}
	}
}

func TestTaskAFirstTextDeadlineWithoutTextHasNoFirstToken(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		t.Run(string(protocol), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
			body := &firstTextProbeBody{chunk: ": keepalive\n\n", block: true, now: &now}
			out := firstTextRunner(body).Probe(ctx, firstTextRequest(protocol))
			if out.Result != ResultNetworkFluctuation || out.FirstTokenMs != nil || out.FirstEventMs != nil || out.RequestPhase != "reading_body" || !body.closed {
				t.Fatalf("no text must not fabricate a first-token timing: %+v", out)
			}
		})
	}
}

func TestTaskAFirstTextMalformedFrameStopsBeforeLaterText(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		t.Run(string(protocol), func(t *testing.T) {
			now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
			body := &firstTextProbeBody{chunk: "data: broken\n\n", tail: firstTextFrame(protocol), now: &now}
			out := firstTextRunner(body).Probe(context.Background(), firstTextRequest(protocol))
			if out.Result != ResultInvalidResponse || body.reads != 1 || out.FirstTokenMs != nil || !strings.Contains(out.Detail, "JSON") {
				t.Fatalf("malformed frame must stop immediately and cannot be washed by later text: %+v reads=%d", out, body.reads)
			}
		})
	}
}

func TestTaskAFirstTextResidualFrameRequiresCleanEOF(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		for _, stop := range []string{"deadline", "cancel", "connection-error", "eof", "completed-eof"} {
			t.Run(string(protocol)+"/"+stop, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				defer cancel()
				now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
				body := &firstTextProbeBody{chunk: strings.TrimSuffix(firstTextFrame(protocol), "\n"), now: &now}
				switch stop {
				case "deadline":
					body.block = true
				case "cancel":
					body.block, body.cancel = true, cancel
				case "connection-error":
					body.err = errors.New("fixture connection reset")
				case "completed-eof":
					if protocol == TestProtocolResponses {
						body.chunk = strings.TrimSuffix(taskAResponseComplete, "\n")
					} else {
						body.chunk = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n"
					}
				}
				out := firstTextRunner(body).Probe(ctx, firstTextRequest(protocol))
				if stop == "eof" || stop == "completed-eof" {
					if out.Result != ResultOK || out.FirstTokenMs == nil || *out.FirstTokenMs != 50 {
						t.Fatalf("clean EOF preserves residual-frame compatibility: %+v", out)
					}
				} else if out.Result != ResultNetworkFluctuation || out.FirstTokenMs != nil || out.FirstEventMs != nil || out.RequestPhase != "reading_body" {
					t.Fatalf("read error must discard unfinished frame and timings: %+v", out)
				}
			})
		}
	}
}

func TestTaskAFirstTextPresetDefaultAndSixSecondBoundary(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy, ""} {
		for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
			p := Policy{RuleVersion: version}
			want := defaultProtocolDelayLineMs(protocol)
			if version == RuleVersionV2 {
				want = 6000
			}
			if got := RulePresetForPolicy(p).DelayLine(protocol); got != want {
				t.Errorf("version=%q protocol=%s: delay=%d want=%d", version, protocol, got, want)
			}
		}
	}
	for _, kind := range []string{PresetRecommended, PresetCustom, PresetLegacySnapshot, PresetLegacyDefault} {
		for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
			p := DefaultRulePreset()
			p.Kind = kind
			p.DelayLineMs = map[string]int{}
			want := 6000
			if kind == PresetLegacySnapshot || kind == PresetLegacyDefault {
				want = defaultProtocolDelayLineMs(protocol)
			}
			if got := p.DelayLine(protocol); got != want {
				t.Errorf("kind=%s protocol=%s: delay=%d want=%d", kind, protocol, got, want)
			}
		}
	}
	for _, protocol := range []TestProtocol{TestProtocolResponses, TestProtocolChatCompletions} {
		for _, first := range []int{6000, 6001} {
			out := applyOutcomeDelay(ProbeOutcome{Result: ResultOK, Protocol: protocol, FirstTokenMs: intPtr(first), LatencyMs: first}, Policy{RuleVersion: RuleVersionV2})
			want := ResultOK
			if first == 6001 {
				want = ResultSlowResponse
			}
			if out.Result != want {
				t.Errorf("%s first=%d: got=%s want=%s", protocol, first, out.Result, want)
			}
		}
		preset := DefaultRulePreset()
		preset.DelayLineMs[string(protocol)] = 8000
		for _, first := range []int{7000, 9000} {
			out := applyOutcomeDelay(ProbeOutcome{Result: ResultOK, Protocol: protocol, FirstTokenMs: intPtr(first)}, Policy{RuleVersion: RuleVersionV2, RulePreset: &preset})
			want := ResultOK
			if first == 9000 {
				want = ResultSlowResponse
			}
			if out.Result != want {
				t.Errorf("saved 8-second line ignored: %+v", out)
			}
		}
	}
}

func TestTaskAFirstTextSub2APIDefaultIsTwentySeconds(t *testing.T) {
	got := defaultTestConfiguration()
	if got.Protocol != TestProtocolChatCompletions || got.ProbeTimeoutSeconds != 20 || got.Status != "default" || len(got.SourceGroups) != 0 {
		t.Fatalf("unconfigured main-site must default to Chat/20: %+v", got)
	}
}

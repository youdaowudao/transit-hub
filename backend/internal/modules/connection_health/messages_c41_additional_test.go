package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func TestMessagesC41NonStreamingBodyTextAndTiming(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       ResultKey
	}{
		{"text", `{"content":[{"type":"text","text":" body "}]}`, ResultOK},
		{"text after thinking", `{"content":[{"type":"thinking","thinking":"thought"},{"type":"text","text":"body"}]}`, ResultOK},
		{"thinking only", `{"content":[{"type":"thinking","thinking":"thought"}]}`, ResultInvalidResponse},
		{"empty", `{"content":[{"type":"text","text":""}]}`, ResultInvalidResponse},
		{"blank", `{"content":[{"type":"text","text":" \n "}]}`, ResultInvalidResponse},
		{"other type", `{"content":[{"type":"tool_use","text":"body"}]}`, ResultInvalidResponse},
		{"malformed", `{"content":`, ResultInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
			r := NewRealProbeRunner()
			r.now = func() time.Time { return now }
			r.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
				now = now.Add(7300 * time.Millisecond)
				return protocolServiceResponse(req, 200, tc.body), nil
			})
			out := r.Probe(context.Background(), c41Request("x_api_key"))
			if out.Result != tc.want || !out.NonStreaming || out.LatencyMs != 7300 || out.FirstTokenMs == nil || *out.FirstTokenMs != 7300 {
				t.Fatalf("body and timing=%+v want=%s at 7300ms", out, tc.want)
			}
		})
	}
}

func TestMessagesC41DelayLineFallbackAndBoundary(t *testing.T) {
	for _, kind := range []string{PresetRecommended, PresetCustom, PresetLegacyDefault, PresetLegacySnapshot} {
		t.Run(kind, func(t *testing.T) {
			p := DefaultRulePreset()
			p.Kind = kind
			if p.DelayLine(TestProtocolMessages) != 6000 {
				t.Fatalf("fallback for %s=%d", kind, p.DelayLine(TestProtocolMessages))
			}
			policy := Policy{RuleVersion: RuleVersionV2, RulePreset: &p}
			for _, tc := range []struct {
				millis int
				want   ResultKey
			}{{6000, ResultOK}, {6001, ResultSlowResponse}} {
				out := applyOutcomeDelay(ProbeOutcome{Protocol: TestProtocolMessages, Result: ResultOK, LatencyMs: 10000, FirstTokenMs: intPtr(tc.millis)}, policy)
				if out.Result != tc.want {
					t.Errorf("default at %dms=%s want=%s", tc.millis, out.Result, tc.want)
				}
			}
			p.DelayLineMs["messages"] = 8000
			if p.DelayLine(TestProtocolMessages) != 8000 {
				t.Fatal("saved Messages line ignored")
			}
			for _, tc := range []struct {
				millis int
				want   ResultKey
			}{{8000, ResultOK}, {8001, ResultSlowResponse}} {
				out := applyOutcomeDelay(ProbeOutcome{Protocol: TestProtocolMessages, Result: ResultOK, FirstTokenMs: intPtr(tc.millis)}, policy)
				if out.Result != tc.want {
					t.Errorf("saved line at %dms=%s want=%s", tc.millis, out.Result, tc.want)
				}
			}
		})
	}
	p := DefaultRulePreset()
	p.DelayLineMs["messages"] = 8000
	if !validRulePreset(p) {
		t.Fatal("Messages preset rejected")
	}
	p.DelayLineMs["bogus"] = 8000
	if validRulePreset(p) {
		t.Fatal("unknown protocol preset accepted")
	}
}

func TestMessagesC41SavedConfigFlowsToEveryProbeEntryAndDeadline(t *testing.T) {
	for _, entry := range []string{"automatic", "formal-json", "formal-sse", "group-quick", "once"} {
		for _, seconds := range []int{12, 20, 30} {
			t.Run(entry+"/"+strconv.Itoa(seconds), func(t *testing.T) {
				s, repo := protocolServiceFixture(t)
				reader := s.platformGroups.(fakePlatformGroupReader)
				reader.groups[0].Platform = "anthropic"
				cred := reader.credByAccount["normal"]
				cred.AnthropicAuth = "x_api_key"
				reader.credByAccount["normal"] = cred
				s.platformGroups = reader
				if _, err := s.SetAdminGroupTestConfiguration(context.Background(), "user1", "g1", &GroupTestConfiguration{TestProtocolMessages, seconds}); err != nil {
					t.Fatal(err)
				}
				read, err := s.GetAdminGroupTestConfiguration(context.Background(), "user1", "g1")
				if err != nil || read.Configuration == nil || read.Configuration.Protocol != TestProtocolMessages || read.Configuration.ProbeTimeoutSeconds != seconds {
					t.Fatalf("saved configuration=%+v err=%v", read, err)
				}
				calls := 0
				s.probeRunner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
					calls++
					deadline, ok := req.Context().Deadline()
					left := time.Until(deadline)
					if !ok || left < time.Duration(seconds-1)*time.Second || left > time.Duration(seconds)*time.Second {
						t.Errorf("request deadline=%v want=%ds", left, seconds)
					}
					if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" || req.Header.Get("x-api-key") != "fixture" {
						t.Errorf("inherited Messages request=%s", req.URL.RequestURI())
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(c41Frame("content_block_delta", `,"delta":{"type":"text_delta","text":"body"}`)))}, nil
				})
				const target = "sub2api:ws1:normal"
				switch entry {
				case "automatic":
					found := false
					for _, job := range s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments) {
						if job.target.TargetID == target {
							found = true
							runProtocolServiceJob(s, job)
						}
					}
					if !found {
						t.Fatal("automatic target not found")
					}
				case "once":
					out, err := s.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
					if err != nil || len(out) != 1 || !out[0].Healthy || out[0].Protocol != TestProtocolMessages || out[0].ProbeTimeoutSeconds != seconds {
						t.Fatalf("once=%+v err=%v", out, err)
					}
				case "formal-sse", "group-quick":
					events := protocolServiceSSE(t, s, target)
					if len(events) == 0 || events[len(events)-1].Type == "error" {
						t.Fatalf("stream=%+v", events)
					}
				case "formal-json":
					mux := http.NewServeMux()
					RegisterRoutes(mux, s)
					req := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/"+target+"/probe", strings.NewReader(`{"models":["gpt-4o"]}`))
					req = req.WithContext(authctx.WithUserID(req.Context(), "user1"))
					out := httptest.NewRecorder()
					mux.ServeHTTP(out, req)
					if out.Code != http.StatusOK {
						t.Fatalf("formal status=%d", out.Code)
					}
				}
				if calls != 1 {
					t.Fatalf("entry=%s requests=%d", entry, calls)
				}
				if entry != "once" {
					if len(repo.events) != 1 || repo.events[0].RequestProtocol == nil || *repo.events[0].RequestProtocol != TestProtocolMessages || repo.events[0].RequestTimeoutSeconds == nil || *repo.events[0].RequestTimeoutSeconds != seconds {
						t.Fatalf("recorded protocol/seconds=%+v", repo.events)
					}
				}
			})
		}
	}
}

func TestMessagesC41StreamEndMalformedAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		want        ResultKey
		detail      string
		firstEvent  bool
	}{
		{"DONE", "data: [DONE]\n\n", ResultInvalidResponse, "回答结束但没有正文", true},
		{"message stop", c41Frame("message_stop", ""), ResultInvalidResponse, "回答结束但没有正文", true},
		{"malformed", "data: {oops}\n\n", ResultInvalidResponse, "流式事件不是合法 JSON", false},
		{"metadata", c41Frame("message_start", "") + c41Frame("ping", ""), ResultNetworkFluctuation, "流式回答中途断开或未结束", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := c41Stream(t, []taskATimedFrame{{time.Second, tc.frame}})
			if out.Result != tc.want || out.Detail != tc.detail || out.FirstTokenMs != nil || (out.FirstEventMs != nil) != tc.firstEvent {
				t.Fatalf("end/metadata=%+v", out)
			}
			if out.FirstEventMs != nil && *out.FirstEventMs != 1000 {
				t.Fatalf("stop timestamp=%+v", out)
			}
		})
	}
	// Preserve the existing event-name fallback for servers that omit payload.type.
	out := c41Stream(t, []taskATimedFrame{{time.Second, "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"body\"}}\n\n"}})
	if out.Result != ResultOK || out.FirstTokenMs == nil || *out.FirstTokenMs != 1000 {
		t.Fatalf("event-name fallback=%+v", out)
	}
}

func TestMessagesC41QuestionAnswerGuardAndEphemeralAuth(t *testing.T) {
	req, err := buildTestRequest(context.Background(), testRequestInput{Protocol: TestProtocolMessages, BaseURL: "https://fixture.invalid", Key: "fixture", AnthropicAuth: "x_api_key", QuestionAnswer: true})
	if err == nil || req != nil {
		t.Fatal("question-answer Messages request was constructed")
	}
	if _, ok := decodeTestResponse(TestProtocolMessages, []byte(`{"content":[{"type":"text","text":"body"}]}`), true); ok {
		t.Fatal("Messages decoded as a question answer")
	}
	req, err = buildTestRequest(context.Background(), testRequestInput{Protocol: TestProtocolMessages, BaseURL: "https://fixture.invalid", Key: "fixture"})
	if req != nil || !errors.Is(err, errMessagesAccountTypeUnsupported) || err.Error() != c41UnsupportedAccount {
		t.Fatalf("unsupported typed error=%v", err)
	}
	raw, err := json.Marshal(ProbeRequest{Protocol: TestProtocolMessages, AnthropicAuth: "authorization_bearer"})
	if err != nil || strings.Contains(string(raw), "AnthropicAuth") || strings.Contains(string(raw), "authorization_bearer") {
		t.Fatalf("ephemeral auth exposed: %s err=%v", raw, err)
	}
}

func TestMessagesC41ProtocolSwitchMatchesExistingScenarios(t *testing.T) {
	now := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for _, other := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
		for _, pair := range [][2]TestProtocol{{other, TestProtocolMessages}, {TestProtocolMessages, other}} {
			t.Run(string(pair[0])+" to "+string(pair[1]), func(t *testing.T) {
				state := ConnectionHealthState{ConnectionID: "sub2api:ws1:1", ModelName: "m", State: StateDegraded, CurrentWeight: 50, ConsecutiveFailures: 2, CounterProtocol: protocolPointer(pair[0]), HealthEvidenceStatus: "valid", HealthEvidenceProtocol: protocolPointer(pair[0])}
				outcome := ProbeOutcome{Result: ResultNetworkFluctuation, Protocol: pair[1], ProbeTimeoutSeconds: 30}
				policy := Policy{AutoDegradeEnabled: true, FailureThreshold: 3}
				next, _ := applyProbeOutcome(state, outcome, policy, now)
				if next.ConsecutiveFailures != 1 || next.State != StateDegraded {
					t.Errorf("old protocol failures drove transition: count=%d state=%s", next.ConsecutiveFailures, next.State)
				}
				next, _ = applyProbeOutcome(next, outcome, policy, now.Add(time.Minute))
				if next.ConsecutiveFailures != 2 {
					t.Errorf("second same-protocol failure reset/inherited=%d", next.ConsecutiveFailures)
				}
				for _, origin := range []string{"valid", "legacy"} {
					state := ConnectionHealthState{ConnectionID: "sub2api:ws1:1", ModelName: "m", State: StateSuspended, CurrentWeight: 0, ConsecutiveFailures: 3, HealthEvidenceStatus: origin, HealthEvidenceProtocol: protocolPointer(pair[0])}
					outcome := ProbeOutcome{Result: ResultOK, Protocol: pair[1], ProbeTimeoutSeconds: 30}
					next, _ := applyProbeOutcome(state, outcome, Policy{AutoDegradeEnabled: true, ObservationSeconds: 60, SuccessThreshold: 2}, now)
					if next.State != StateObserving {
						t.Fatalf("control transition changed: %s", next.State)
					}
					if next.HealthEvidenceStatus != "invalid" || next.HealthEvidenceProtocol != nil {
						t.Errorf("new protocol observing revived %s evidence: %+v", origin, next)
					}
				}
			})
		}
	}
}

func TestMessagesC41InheritanceAgreesAndConflicts(t *testing.T) {
	memberships := []TestConfigurationSource{{AdminGroupID: "one"}, {AdminGroupID: "two"}}
	configs := []GroupTestConfig{{AdminGroupID: "one", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 30}, {AdminGroupID: "two", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 30}}
	got := ResolveGroupTestConfiguration("sub2api", memberships, true, configs)
	if !got.usable() || got.Protocol != TestProtocolMessages || len(got.SourceGroups) != 2 {
		t.Fatalf("matching Messages groups=%+v", got)
	}
	configs[1].Protocol = TestProtocolChatCompletions
	got = ResolveGroupTestConfiguration("sub2api", memberships, true, configs)
	if got.usable() || got.Status != "conflict" || got.Protocol != "" {
		t.Fatalf("Messages/Chat conflict=%+v", got)
	}
}

// The upgrade must only replace a single-column protocol value check.
func TestMessagesC41MigrationPreservesOtherAndCompoundChecks(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `CREATE TABLE connection_health_group_test_configs (
 user_id text NOT NULL,
 protocol text NOT NULL CHECK(protocol IN ('chat_completions','responses')),
 CONSTRAINT c41_compound CHECK (protocol <> 'chat_completions' OR user_id <> ''),
 CONSTRAINT c41_nonempty CHECK (length(protocol)>0)
 )`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../database/migrations/000038_connection_health_test_protocol_messages.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	var retained int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='connection_health_group_test_configs'::regclass AND conname IN ('c41_compound','c41_nonempty')`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Fatalf("unrelated or compound checks were removed: retained=%d", retained)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_group_test_configs(user_id,protocol) VALUES('fixture','messages')`); err != nil {
		t.Fatalf("messages rejected: %v", err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_group_test_configs(user_id,protocol) VALUES('fixture','bogus')`); err == nil {
		t.Fatal("bogus accepted")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_group_test_configs(user_id,protocol) VALUES('','chat_completions')`); err == nil {
		t.Fatal("compound check no longer enforced")
	}
}

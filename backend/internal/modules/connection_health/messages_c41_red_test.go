package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const c41Messages TestProtocol = "messages"
const c41UnsupportedAccount = "Messages 探活只支持 API Key 类型的账号，这个账号没有发出请求。请把它所在分组的测试请求改回 Chat Completions，或在主站换成 API Key 类型的账号。"

// Reflection keeps the same business assertions compilable on the pre-C4.1
// baseline, which has no ephemeral AnthropicAuth field yet.
func c41SetAuth(value any, auth string) {
	field := reflect.ValueOf(value).Elem().FieldByName("AnthropicAuth")
	if field.IsValid() {
		field.SetString(auth)
	}
}

func c41Request(auth string) ProbeRequest {
	r := ProbeRequest{Protocol: c41Messages, ProbeTimeoutSeconds: 20, BaseURL: "https://messages.invalid/", UpstreamKey: "fixture-key", ModelName: "fixture-model", MaxTokens: 1, ProbePrompt: "fixture prompt"}
	c41SetAuth(&r, auth)
	return r
}

func c41Stream(t *testing.T, frames []taskATimedFrame) ProbeOutcome {
	t.Helper()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	runner := &RealProbeRunner{now: func() time.Time { return now }, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &taskATimedReader{frames: frames, now: &now}}, nil
	})}}
	return runner.Probe(context.Background(), c41Request("x_api_key"))
}

func c41Frame(kind, delta string) string {
	return "data: {\"type\":\"" + kind + "\"" + delta + "}\n\n"
}

func TestMessagesC41K1RequestMatchesMainSite(t *testing.T) {
	for _, auth := range []string{"x_api_key", "authorization_bearer"} {
		for _, budget := range []int{1, 256} {
			t.Run(auth+"/"+strconv.Itoa(budget), func(t *testing.T) {
				calls := 0
				r := NewRealProbeRunner()
				r.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" {
						t.Errorf("path/query=%s", req.URL.RequestURI())
					}
					if req.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("missing Anthropic version")
					}
					wantKey, wantBearer := "fixture-key", ""
					if auth == "authorization_bearer" {
						wantKey, wantBearer = "", "Bearer fixture-key"
					}
					if req.Header.Get("x-api-key") != wantKey || req.Header.Get("Authorization") != wantBearer {
						t.Error("wrong authentication scheme or both headers sent")
					}
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					wantBudget := budget
					if wantBudget < 128 {
						wantBudget = 128
					}
					if len(body) != 4 || body["model"] != "fixture-model" || body["max_tokens"] != float64(wantBudget) || body["stream"] != true {
						t.Errorf("minimal request or retained budget violated: %v", body)
					}
					messages, _ := body["messages"].([]any)
					if len(messages) != 1 {
						t.Fatalf("messages=%v", messages)
					}
					message, _ := messages[0].(map[string]any)
					if len(message) != 2 || message["role"] != "user" || message["content"] != "fixture prompt" {
						t.Errorf("message=%v", message)
					}
					return protocolServiceResponse(req, 200, `{"content":[{"type":"text","text":"ok"}]}`), nil
				})
				request := c41Request(auth)
				request.MaxTokens = budget
				out := r.Probe(context.Background(), request)
				if calls != 1 || out.Result != ResultOK {
					t.Fatalf("requests=%d result=%+v", calls, out)
				}
			})
		}
	}
}

func TestMessagesC41K2OnlyBodyTextSucceedsAndReleasesRequest(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	chunk := c41Frame("message_start", "") + c41Frame("content_block_delta", `,"delta":{"type":"thinking_delta","thinking":"thought"}`) + c41Frame("content_block_delta", `,"delta":{"type":"text_delta","text":""}`) + c41Frame("ping", "") + c41Frame("content_block_delta", `,"delta":{"type":"text_delta","text":"hello"}`)
	body := &firstTextProbeBody{chunk: chunk, tail: c41Frame("error", `,"error":{"type":"overloaded_error"}`), block: true, now: &now}
	out := firstTextRunner(body).Probe(parent, c41Request("x_api_key"))
	if out.Result != ResultOK || out.FirstTokenMs == nil || *out.FirstTokenMs != 25 || body.reads != 1 || !body.closed || body.ctx.Err() != context.Canceled || parent.Err() != nil {
		t.Fatalf("first body text must succeed before tail and release request: result=%+v reads=%d closed=%v", out, body.reads, body.closed)
	}
	// Every frame advances an injected clock; metadata, thought and empty text
	// cannot supply the first body text's timestamp.
	frames := []taskATimedFrame{{time.Second, c41Frame("message_start", "")}, {time.Second, c41Frame("content_block_delta", `,"delta":{"type":"thinking_delta","thinking":"thought"}`)}, {time.Second, c41Frame("content_block_delta", `,"delta":{"type":"text_delta","text":""}`)}, {time.Second, c41Frame("ping", "")}, {time.Second, c41Frame("content_block_delta", `,"delta":{"type":"text_delta","text":"hello"}`)}}
	out = c41Stream(t, frames)
	if out.Result != ResultOK || out.FirstTokenMs == nil || *out.FirstTokenMs != 5000 || out.FirstEventMs == nil || *out.FirstEventMs != 2000 {
		t.Fatalf("body/event timestamp=%+v", out)
	}
}

func TestMessagesC41K3NoTextEndIsInvalidAndDisconnectIsNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		err        error
		result     ResultKey
	}{
		{"end", c41Frame("message_stop", ""), nil, ResultInvalidResponse},
		{"disconnect", "", errors.New("fixture reset"), ResultNetworkFluctuation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
			body := &firstTextProbeBody{chunk: c41Frame("content_block_delta", `,"delta":{"type":"thinking_delta","thinking":"thought"}`), tail: tc.tail, err: tc.err, now: &now}
			out := firstTextRunner(body).Probe(context.Background(), c41Request("x_api_key"))
			if out.Result != tc.result || out.FirstTokenMs != nil {
				t.Fatalf("no-text result=%+v want %s", out, tc.result)
			}
			if tc.name == "end" {
				if !strings.Contains(out.Detail, "已收到回复（HTTP 200），但未能确认有效回答。已知原因：回答结束但没有有效正文。") {
					t.Errorf("end reason=%q", out.Detail)
				}
				state := ConnectionHealthState{State: StateHealthy, CurrentWeight: 100, CounterProtocol: protocolPointer(c41Messages), ConsecutiveFailures: 2, ConsecutiveSuccesses: 1}
				next, transition := applyProbeOutcome(state, out, Policy{AutoDegradeEnabled: true, FailureThreshold: 3}, now)
				if next.State != state.State || next.ConsecutiveFailures != 2 || next.ConsecutiveSuccesses != 1 || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
					t.Fatalf("invalid result changed health: %+v %+v", next, transition)
				}
			}
		})
	}
}

func TestMessagesC41K4ErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   ResultKey
	}{
		{401, `{"error":{"type":"authentication_error"}}`, ResultAuth},
		{403, `{}`, ResultAuth},
		{404, `{"type":"error","error":{"type":"not_found_error","message":"  model: fixture  "}}`, ResultModelNotFound},
		{404, `{"error":{"type":"not_found_error","message":"route missing"}}`, ResultInvalidResponse},
		{404, `{"error":{"type":"model_not_found"}}`, ResultModelNotFound},
		{404, `{"error":{"code":"model_not_found"}}`, ResultModelNotFound},
		{404, `{}`, ResultInvalidResponse}, {429, `{}`, ResultRateLimited}, {529, `{}`, ResultRateLimited}, {500, `{}`, ResultServerError}, {503, `{}`, ResultServerError}, {400, `{}`, ResultInvalidResponse}, {422, `{}`, ResultInvalidResponse},
	} {
		out := classifyTestHTTPResponse(c41Messages, tc.status, []byte(tc.body), "fixture", 10)
		if out.Result != tc.want {
			t.Errorf("HTTP %d body=%s got=%s want=%s", tc.status, tc.body, out.Result, tc.want)
		}
	}
	for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
		if out := classifyTestHTTPResponse(protocol, 529, []byte(`{}`), "fixture", 10); out.Result != ResultServerError {
			t.Errorf("%s 529 changed to %s", protocol, out.Result)
		}
	}
	for _, tc := range []struct {
		kind string
		want ResultKey
	}{{"rate_limit_error", ResultRateLimited}, {"overloaded_error", ResultRateLimited}, {"api_error", ResultServerError}} {
		out := c41Stream(t, []taskATimedFrame{{time.Second, c41Frame("error", `,"error":{"type":"`+tc.kind+`"}`)}})
		if out.Result != tc.want {
			t.Errorf("stream %s got=%s want=%s", tc.kind, out.Result, tc.want)
		}
	}
}

func TestMessagesC41K5GroupPlatformSaveAndInheritance(t *testing.T) {
	for _, platform := range []string{"anthropic", " Anthropic ", "openai"} {
		t.Run(platform, func(t *testing.T) {
			s, repo := protocolServiceFixture(t)
			reader := s.platformGroups.(fakePlatformGroupReader)
			reader.groups[0].Platform = platform
			s.platformGroups = reader
			// A rejected save must retain an existing Chat configuration as well.
			saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolChatCompletions, 20})
			got, err := s.SetAdminGroupTestConfiguration(context.Background(), "user1", "g1", &GroupTestConfiguration{c41Messages, 30})
			configs, _ := repo.ListGroupTestConfigurations(context.Background(), "user1", "ws1")
			if platform == "openai" {
				if !errors.Is(err, requestError("admin.connectionHealth.errors.testProtocolNotAllowed")) || len(configs) != 1 || configs[0].Protocol != TestProtocolChatCompletions {
					t.Fatalf("non-Anthropic rejection not preserving config: err=%v configs=%+v", err, configs)
				}
				return
			}
			if err != nil || got.Configuration == nil || got.Configuration.Protocol != c41Messages || len(configs) != 1 {
				t.Fatalf("Messages save=%+v configs=%+v err=%v", got, configs, err)
			}
			for _, account := range got.Accounts {
				if account.TestConfiguration.Protocol != c41Messages || account.TestConfiguration.ProbeTimeoutSeconds != 30 {
					t.Errorf("account did not inherit: %+v", account.TestConfiguration)
				}
			}
			if _, err = s.SetAdminGroupTestConfiguration(context.Background(), "user1", "g1", nil); err != nil {
				t.Fatal(err)
			}
			configs, _ = repo.ListGroupTestConfigurations(context.Background(), "user1", "ws1")
			if len(configs) != 0 {
				t.Fatal("clear did not restore default")
			}
		})
	}
}

func TestMessagesC41K8UnsupportedTypeKeepsHealthAndDoesNotSend(t *testing.T) {
	for _, origin := range []TestProtocol{c41Messages, TestProtocolChatCompletions} {
		for _, entry := range []string{"automatic", "formal", "formal-sse", "group-quick", "once"} {
			t.Run(string(origin)+"/"+entry, func(t *testing.T) {
				s, repo := protocolServiceFixture(t)
				// Persisted current rules carry a version; unmarked legacy callers have a different preserved counter contract.
				repo.policies[0].RuleVersion = RuleVersionV2
				saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{c41Messages, 20})
				const target = "sub2api:ws1:normal"
				state := ConnectionHealthState{RuleVersion: RuleVersionV2, ConnectionID: target, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy, CurrentWeight: 100, CounterProtocol: protocolPointer(origin), ConsecutiveFailures: 2, ConsecutiveSuccesses: 1}
				repo.states[target] = map[string]ConnectionHealthState{"gpt-4o": state}
				calls := 0
				s.probeRunner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
					calls++
					return protocolServiceResponse(req, 200, `{"content":[{"type":"text","text":"ok"}]}`), nil
				})
				switch entry {
				case "automatic":
					jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
					found := false
					for _, job := range jobs {
						if job.target.TargetID == target {
							found = true
							runProtocolServiceJob(s, job)
						}
					}
					if !found {
						t.Error("usable OAuth credentials should still enter request assembly")
					}
				case "once":
					out, err := s.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
					if err != nil || len(out) != 1 || out[0].Healthy || out[0].ErrorDetail != c41UnsupportedAccount {
						t.Errorf("once invalid reason=%+v err=%v", out, err)
					}
				case "formal-sse", "group-quick":
					protocolServiceSSE(t, s, target)
				default:
					if _, err := s.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"}); err != nil {
						t.Error(err)
					}
				}
				if calls != 0 {
					t.Errorf("unsupported type sent %d upstream requests", calls)
				}
				next := repo.states[target]["gpt-4o"]
				wantFailures, wantSuccesses := 2, 1
				if entry != "once" && origin != c41Messages {
					wantFailures, wantSuccesses = 0, 0
				}
				if next.State != state.State || next.ConsecutiveFailures != wantFailures || next.ConsecutiveSuccesses != wantSuccesses || len(repo.targetActionStates) != 0 || len(repo.priorityStates) != 0 {
					t.Errorf("invalid changed health/actions: %+v", next)
				}
				if entry != "once" {
					if len(repo.events) != 1 || repo.events[0].ErrorDetail != c41UnsupportedAccount || repo.events[0].ProbeDisposition != "invalid" {
						t.Errorf("missing unsupported audit reason: %+v", repo.events)
					}
				} else if len(repo.events) != 0 {
					t.Error("once persisted event")
				}
				// Same account becomes API Key: unchanged configured protocol now sends.
				reader := s.platformGroups.(fakePlatformGroupReader)
				cred := reader.credByAccount["normal"]
				c41SetAuth(&cred, "x_api_key")
				reader.credByAccount["normal"] = cred
				s.platformGroups = reader
				out, err := s.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
				if err != nil || len(out) != 1 || !out[0].Healthy || calls != 1 {
					t.Errorf("API Key account failed to send: healthy=%v err=%v calls=%d", out, err, calls)
				}
			})
		}
	}
}

func TestMessagesC41K8MissingAddressKeepsCredentialFailurePath(t *testing.T) {
	for _, entry := range []string{"automatic", "formal", "formal-sse", "group-quick", "once"} {
		t.Run(entry, func(t *testing.T) {
			s, repo := protocolServiceFixture(t)
			saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{c41Messages, 20})
			reader := s.platformGroups.(fakePlatformGroupReader)
			reader.credErr = map[string]error{"normal": &upstream.ProbeCredentialError{Reason: upstream.ReasonBaseURLUnavailable}}
			s.platformGroups = reader
			calls := 0
			s.probeRunner.client.Transport = taskATransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected request") })
			const target = "sub2api:ws1:normal"
			switch entry {
			case "automatic":
				for _, job := range s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments) {
					if job.target.TargetID == target {
						runProtocolServiceJob(s, job)
					}
				}
				claims := 0
				for _, count := range repo.budgetClaims {
					claims += count
				}
				if claims != 0 || len(repo.events) != 1 || repo.events[0].ErrorKey != upstream.ReasonBaseURLUnavailable || repo.events[0].ErrorDetail == c41UnsupportedAccount {
					t.Errorf("credential failure used type rejection/budget path: events=%+v claims=%d", repo.events, claims)
				}
			case "once":
				if _, err := s.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"}); err == nil || strings.Contains(err.Error(), c41UnsupportedAccount) {
					t.Errorf("missing address should return credential error: %v", err)
				}
			case "formal-sse", "group-quick":
				events := protocolServiceSSE(t, s, target)
				if len(events) == 0 || events[len(events)-1].Type != "error" {
					t.Errorf("missing address stream=%+v", events)
				}
			default:
				if _, err := s.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"}); err == nil || strings.Contains(err.Error(), c41UnsupportedAccount) {
					t.Errorf("missing address formal error=%v", err)
				}
			}
			if calls != 0 {
				t.Errorf("missing address sent %d requests", calls)
			}
		})
	}
}

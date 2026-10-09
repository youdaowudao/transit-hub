package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"net/http/httptest"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func TestProtocolFirstTextPlatformDefaultsAndExplicitValues(t *testing.T) {
	for _, platform := range []string{"sub2api", "newapi", ""} {
		want := 10
		if platform == "sub2api" {
			want = 20
		}
		got := ResolveGroupTestConfiguration(platform, nil, true, nil)
		if !got.usable() || got.Protocol != TestProtocolChatCompletions || got.ProbeTimeoutSeconds != want {
			t.Fatalf("platform=%q default=%+v", platform, got)
		}
		for _, seconds := range []int{5, 12, 20, 30, 120} {
			got = ResolveGroupTestConfiguration(platform, []TestConfigurationSource{{AdminGroupID: "g"}}, true, []GroupTestConfig{{AdminGroupID: "g", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: seconds}})
			if !got.usable() || got.Protocol != TestProtocolResponses || got.ProbeTimeoutSeconds != seconds {
				t.Fatalf("explicit value overwritten: platform=%q seconds=%d got=%+v", platform, seconds, got)
			}
		}
		if got := ResolveGroupTestConfiguration(platform, nil, false, nil); got.usable() || got.Status != "unavailable" {
			t.Fatalf("unknown inventory accepted: %+v", got)
		}
	}
}

func TestProtocolFirstTextConfigFlowsToEveryProbeEntryAndDeadline(t *testing.T) {
	for _, entry := range []string{"automatic", "formal-json", "formal-sse", "group-quick", "once"} {
		for _, seconds := range []int{20, 12, 30} {
			t.Run(entry+"/"+strconv.Itoa(seconds), func(t *testing.T) {
				s, repo := protocolServiceFixture(t)
				protocol := TestProtocolChatCompletions
				if seconds != 20 {
					protocol = TestProtocolResponses
					if _, err := s.SetAdminGroupTestConfiguration(context.Background(), "user1", "g1", &GroupTestConfiguration{Protocol: protocol, ProbeTimeoutSeconds: seconds}); err != nil {
						t.Fatal(err)
					}
					read, err := s.GetAdminGroupTestConfiguration(context.Background(), "user1", "g1")
					if err != nil || read.Configuration == nil || read.Configuration.ProbeTimeoutSeconds != seconds {
						t.Fatalf("save/read mismatch: %+v %v", read, err)
					}
				}
				calls := 0
				s.probeRunner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
					calls++
					deadline, ok := req.Context().Deadline()
					left := time.Until(deadline)
					if !ok || left < time.Duration(seconds)*time.Second-time.Second || left > time.Duration(seconds)*time.Second {
						t.Errorf("actual request deadline=%v want=%ds", left, seconds)
					}
					wantPath := "/v1/chat/completions"
					if protocol == TestProtocolResponses {
						wantPath = "/v1/responses"
					}
					if req.URL.Path != wantPath {
						t.Errorf("protocol not inherited: %s", req.URL.Path)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(firstTextFrame(protocol)))}, nil
				})
				const target = "sub2api:ws1:normal"
				switch entry {
				case "automatic":
					jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
					for _, job := range jobs {
						if job.target.TargetID == target {
							runProtocolServiceJob(s, job)
						}
					}
				case "once":
					out, err := s.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
					if err != nil || len(out) != 1 || !out[0].Healthy || out[0].ProbeTimeoutSeconds != seconds {
						t.Fatalf("once result=%+v err=%v", out, err)
					}
				case "formal-sse", "group-quick":
					events := protocolServiceSSE(t, s, target)
					if len(events) == 0 {
						t.Fatal("empty SSE result")
					}
				case "formal-json":
					mux := http.NewServeMux()
					RegisterRoutes(mux, s)
					req := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/"+target+"/probe", strings.NewReader(`{"models":["gpt-4o"]}`))
					req = req.WithContext(authctx.WithUserID(req.Context(), "user1"))
					out := httptest.NewRecorder()
					mux.ServeHTTP(out, req)
					if out.Code != 200 {
						t.Fatalf("formal JSON status=%d", out.Code)
					}
				}
				if calls != 1 {
					t.Fatalf("entry=%s requests=%d want=1", entry, calls)
				}
			})
		}
	}
}

func TestProtocolFirstTextNewAPIDefaultStaysConsistentAcrossListScheduleAndExecution(t *testing.T) {
	repo := newFakeRepository()
	p := Policy{ID: "p", UserID: "user1", AdminAccountID: "ws1", Enabled: true, ProbeIntervalSeconds: 600, DailyProbeBudget: 100, ModelTargets: []ModelTarget{{ModelName: "gpt-4o", Enabled: true}}}
	repo.policies = []Policy{p}
	assignPolicyToTarget(repo, p, "newapi:ws1:100")
	reader := schedulerReader("100")
	reader.credByAccount = map[string]upstream.ProbeCredential{"100": {BaseURL: "https://fixture.invalid", Key: "fake"}}
	s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}, repo)
	groups, err := s.AdminGroups(context.Background(), "user1")
	if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 || groups[0].Accounts[0].TestConfiguration.ProbeTimeoutSeconds != 10 {
		t.Fatalf("NewAPI list config=%+v err=%v", groups, err)
	}
	jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
	if len(jobs) != 1 || jobs[0].target.TestConfiguration.ProbeTimeoutSeconds != 10 {
		t.Fatalf("NewAPI schedule config=%+v", jobs)
	}
	job := jobs[0]
	last := time.Now().Add(-time.Second)
	repo.states[job.target.TargetID] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: job.target.TargetID, ModelName: "gpt-4o", State: StateHealthy, LastProbeAt: &last, LastProbeDecisionKey: probeDecisionKey(job.target, job.dueSpecs[0])}}
	if jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments); len(jobs) != 0 {
		t.Fatalf("NewAPI default change must not advance existing interval: %+v", jobs)
	}
	last = time.Now().Add(-time.Hour)
	if jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments); len(jobs) != 1 {
		t.Fatalf("NewAPI due interval must still be collected: %+v", jobs)
	}
	calls := 0
	s.probeRunner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) < 9*time.Second || time.Until(deadline) > 10*time.Second {
			t.Error("NewAPI execution changed ten-second timeout")
		}
		var payload map[string]any
		_ = json.NewDecoder(req.Body).Decode(&payload)
		if payload["stream"] == true || req.URL.Path != "/v1/chat/completions" {
			t.Error("NewAPI request compatibility changed")
		}
		return protocolServiceSuccess(req), nil
	})
	out, err := s.ProbeTarget(context.Background(), "user1", job.target.TargetID, []string{"gpt-4o"})
	if err != nil || calls != 1 || len(out) != 1 {
		t.Fatalf("NewAPI execution failed: %+v %v calls=%d", out, err, calls)
	}
}

func TestProtocolFirstTextDefaultFingerprintChangeRetainsScheduleGuards(t *testing.T) {
	for _, condition := range []string{"changed", "cooldown", "empty-key", "budget"} {
		t.Run(condition, func(t *testing.T) {
			s, repo := protocolServiceFixture(t)
			initial := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
			for _, job := range initial {
				if job.target.TestConfiguration.ProbeTimeoutSeconds != 20 {
					t.Fatal("Sub2API collector default differs")
				}
				oldTarget := job.target
				oldTarget.TestConfiguration.ProbeTimeoutSeconds = 10
				last := time.Now().Add(-time.Second)
				cooldown := time.Now().Add(time.Hour)
				state := ConnectionHealthState{ConnectionID: job.target.TargetID, ModelName: "gpt-4o", State: StateHealthy, LastProbeAt: &last, CounterProtocol: protocolPointer(TestProtocolChatCompletions), LastProbeDecisionKey: probeDecisionKey(oldTarget, job.dueSpecs[0])}
				if condition == "cooldown" {
					state.State, state.CooldownUntil = StateSuspended, &cooldown
				}
				if condition == "empty-key" {
					state.LastProbeDecisionKey = ""
				}
				repo.states[job.target.TargetID] = map[string]ConnectionHealthState{"gpt-4o": state}
			}
			if condition == "budget" {
				repo.policies[0].DailyProbeBudget = 1
				repo.events = []ConnectionHealthEvent{{UserID: "user1", AdminAccountID: "ws1", PolicyID: repo.policies[0].ID, Result: string(ResultOK), CreatedAt: time.Now()}}
			}
			jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
			if condition != "changed" {
				if len(jobs) != 0 {
					t.Fatalf("%s guard bypassed by default change: %+v", condition, jobs)
				}
				return
			}
			if len(jobs) != 2 {
				t.Fatalf("changed nonempty key should make both targets eligible: %+v", jobs)
			}
			s.probeRunner.client.Transport = taskATransport(func(req *http.Request) (*http.Response, error) { return protocolServiceSuccess(req), nil })
			for _, job := range jobs {
				runProtocolServiceJob(s, job)
			}
			if next := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments); len(next) != 0 {
				t.Fatalf("new committed fingerprint did not restore normal interval: %+v", next)
			}
		})
	}
}

func TestTaskAFirstTextVisibilityAndErrorPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol TestProtocol
		frame    string
		result   ResultKey
		token    bool
	}{
		{"chat role", TestProtocolChatCompletions, `{"choices":[{"delta":{"role":"assistant"}}]}`, ResultNetworkFluctuation, false},
		{"chat tool", TestProtocolChatCompletions, `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"abc"}}]}}]}`, ResultNetworkFluctuation, false},
		{"chat usage", TestProtocolChatCompletions, `{"usage":{"completion_tokens":1}}`, ResultNetworkFluctuation, false},
		{"chat choice two", TestProtocolChatCompletions, `{"choices":[{"delta":{}},{"delta":{"content":"hello"}}]}`, ResultNetworkFluctuation, false},
		{"chat blank", TestProtocolChatCompletions, `{"choices":[{"delta":{"content":" "}}]}`, ResultOK, true},
		{"chat empty", TestProtocolChatCompletions, `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`, ResultOK, false},
		{"chat array no text", TestProtocolChatCompletions, `{"choices":[{"delta":{"content":[{"type":"image"}]}}]}`, ResultNetworkFluctuation, false},
		{"chat same frame error", TestProtocolChatCompletions, `{"error":{"code":"rate_limit_exceeded"},"choices":[{"delta":{"content":"hi"}}]}`, ResultRateLimited, false},
		{"responses created", TestProtocolResponses, `{"type":"response.created"}`, ResultNetworkFluctuation, false},
		{"responses progress", TestProtocolResponses, `{"type":"response.in_progress"}`, ResultNetworkFluctuation, false},
		{"responses keepalive", TestProtocolResponses, `{"type":"keepalive"}`, ResultNetworkFluctuation, false},
		{"responses reasoning", TestProtocolResponses, `{"type":"response.reasoning_text.delta","delta":"thinking"}`, ResultNetworkFluctuation, false},
		{"responses tool", TestProtocolResponses, `{"type":"response.function_call_arguments.delta","delta":"abc"}`, ResultNetworkFluctuation, false},
		{"responses empty", TestProtocolResponses, `{"type":"response.output_text.delta","delta":""}`, ResultNetworkFluctuation, false},
		{"responses blank", TestProtocolResponses, `{"type":"response.output_text.delta","delta":" "}`, ResultOK, true},
		{"responses same frame error", TestProtocolResponses, `{"type":"response.output_text.delta","delta":"hi","error":{"code":"internal_error"}}`, ResultServerError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := taskAStream(t, tc.protocol, []taskATimedFrame{{time.Second, "data: " + tc.frame + "\n\n"}})
			if out.Result != tc.result || (out.FirstTokenMs != nil) != tc.token {
				t.Fatalf("visibility/error contract: %+v", out)
			}
		})
	}
}

func TestTaskAFirstTextNewAndPersistedPresetsPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	ctx := t.Context()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	check := func(presets []RulePreset) {
		for _, p := range presets {
			if p.Kind == PresetRecommended && (p.DelayLine(TestProtocolResponses) != 6000 || p.DelayLine(TestProtocolChatCompletions) != 6000) {
				t.Fatalf("new recommended not 6/6: %+v", p)
			}
			if p.Kind == PresetLegacyDefault && (p.DelayLine(TestProtocolResponses) != 10000 || p.DelayLine(TestProtocolChatCompletions) != 5000) {
				t.Fatalf("legacy changed: %+v", p)
			}
		}
	}
	before, err := repo.ListRulePresets(ctx, "u", "w")
	if err != nil || len(before) != 2 {
		t.Fatal(before, err)
	}
	check(before)
	p := sub2APIProbePolicy(false)
	p.UserID, p.AdminAccountID = "u", "w"
	if err := repo.SavePolicyWithTargets(ctx, p, p.ModelTargets); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.ListRulePresets(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	check(stored)
	for _, p := range stored {
		if p.Kind == PresetRecommended {
			p.DelayLineMs = map[string]int{"responses": 10000, "chat_completions": 5000}
			if _, err := repo.SaveRulePreset(ctx, p); err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.ListRulePresets(ctx, "u", "w")
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range loaded {
				if saved.ID == p.ID && (saved.DelayLine(TestProtocolResponses) != 10000 || saved.DelayLine(TestProtocolChatCompletions) != 5000) {
					t.Fatal("existing saved line migrated without authorization")
				}
			}
		}
	}
}

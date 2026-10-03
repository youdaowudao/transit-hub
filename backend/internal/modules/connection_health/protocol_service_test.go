package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// Both targets have independent policy assignments. Only the shared target is
// also a member of g2; protocol inheritance must not depend on policy ownership.
func protocolServiceFixture(t *testing.T) (*Service, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	for _, id := range []string{"shared", "normal"} {
		assignPolicyToTarget(repo, policy, "sub2api:ws1:"+id)
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "one"}, {ID: "g2", Name: "two"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "shared", Name: "shared", Models: "gpt-4o"}, {ID: "normal", Name: "normal", Models: "gpt-4o"}},
			"g2": {{ID: "shared", Name: "shared", Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{
			"shared": {BaseURL: "https://fixture.invalid", Key: "fixture"},
			"normal": {BaseURL: "https://fixture.invalid", Key: "fixture"},
		},
	}
	return newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo), repo
}

func saveProtocolServiceConfig(t *testing.T, repo *fakeRepository, group string, config *GroupTestConfiguration) {
	t.Helper()
	if err := repo.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", group, config); err != nil {
		t.Fatal(err)
	}
}

func protocolServiceResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func protocolServiceSuccess(req *http.Request) *http.Response {
	body := `{"choices":[{"message":{"content":"ok"}}]}`
	if req.URL.Path == "/v1/responses" {
		body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}`
	}
	return protocolServiceResponse(req, http.StatusOK, body)
}

func protocolServiceSSE(t *testing.T, service *Service, target string) []probeTargetStreamEvent {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/"+target+"/probe-stream", strings.NewReader(`{"models":["gpt-4o"]}`))
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE status=%d type=%s", response.Code, response.Header().Get("Content-Type"))
	}
	var events []probeTargetStreamEvent
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event probeTargetStreamEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
		}
	}
	return events
}

func runProtocolServiceJob(service *Service, job adminProbeJob) {
	var done sync.WaitGroup
	done.Add(1)
	service.runAdminProbeJob(context.Background(), job, func() {}, &done)
	done.Wait()
}

func TestProtocolServiceConflictsBlockEachEntryAndKeepOtherTargets(t *testing.T) {
	for _, conflict := range []GroupTestConfiguration{{TestProtocolChatCompletions, 30}, {TestProtocolResponses, 20}} {
		for _, entry := range []string{"temporary", "formal", "sse", "automatic"} {
			t.Run(string(conflict.Protocol)+"/"+entry, func(t *testing.T) {
				service, repo := protocolServiceFixture(t)
				saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
				saveProtocolServiceConfig(t, repo, "g2", &conflict)
				var sends atomic.Int32
				service.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
					sends.Add(1)
					if req.URL.Path != "/v1/responses" {
						t.Errorf("normal target lost inheritance: %s", req.URL.Path)
					}
					return protocolServiceSuccess(req), nil
				})
				invoke := func(target string) bool {
					switch entry {
					case "temporary":
						_, err := service.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
						return err != nil
					case "formal":
						_, err := service.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
						return err != nil
					case "sse":
						events := protocolServiceSSE(t, service, target)
						if len(events) == 0 {
							t.Fatal("missing terminal SSE event")
						}
						return events[len(events)-1].Type == "error"
					default:
						jobs := service.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
						for _, job := range jobs {
							if job.target.TargetID == target {
								runProtocolServiceJob(service, job)
								return false
							}
						}
						return true
					}
				}
				if !invoke("sub2api:ws1:shared") {
					t.Fatal("conflicting target was not blocked")
				}
				if sends.Load() != 0 || len(repo.states) != 0 || len(repo.events) != 0 || len(repo.budgetClaims) != 0 || len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0 {
					t.Fatal("conflicting entry caused model, budget, health or action effects")
				}
				if invoke("sub2api:ws1:normal") || sends.Load() != 1 {
					t.Fatalf("unaffected target did not continue: calls=%d", sends.Load())
				}
				if entry == "temporary" && (len(repo.states) != 0 || len(repo.events) != 0 || len(repo.budgetClaims) != 0 || len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0) {
					t.Fatal("temporary probe persisted production data")
				}
				if entry == "automatic" {
					claims := 0
					for _, n := range repo.budgetClaims {
						claims += n
					}
					if claims != 1 || len(repo.events) != 1 || repo.events[0].ConnectionID != "sub2api:ws1:normal" {
						t.Fatalf("automatic accounting included blocked target: claims=%d events=%+v", claims, repo.events)
					}
				}
			})
		}
	}
}

func TestProtocolServiceInheritedTimeoutAndSSEActualRequest(t *testing.T) {
	for _, entry := range []string{"temporary", "formal", "sse", "automatic"} {
		t.Run(entry, func(t *testing.T) {
			service, repo := protocolServiceFixture(t)
			saveProtocolServiceConfig(t, repo, "g2", &GroupTestConfiguration{TestProtocolResponses, 30})
			calls := 0
			service.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := req.Context().Deadline()
				if req.URL.Path != "/v1/responses" || !ok || time.Until(deadline) < 29*time.Second || time.Until(deadline) > 30*time.Second {
					t.Errorf("inheritance path=%s deadline=%v", req.URL.Path, deadline)
				}
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["stream"] != false || body["store"] != false {
					t.Errorf("model request must remain nonstream: %v err=%v", body, err)
				}
				return protocolServiceSuccess(req), nil
			})
			const target = "sub2api:ws1:shared"
			switch entry {
			case "temporary":
				results, err := service.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
				if err != nil || len(results) != 1 || results[0].Protocol != TestProtocolResponses || results[0].ProbeTimeoutSeconds != 30 || !results[0].Healthy {
					t.Fatalf("temporary results=%+v err=%v", results, err)
				}
			case "formal":
				results, err := service.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
				if err != nil || len(results) != 1 || results[0].RequestProtocol == nil || *results[0].RequestProtocol != TestProtocolResponses {
					t.Fatalf("formal results=%+v err=%v", results, err)
				}
			case "sse":
				events := protocolServiceSSE(t, service, target)
				if len(events) != 2 || events[0].Type != "phase" || events[0].Phase != "running" || events[1].Type != "result" || len(events[1].Results) != 1 {
					t.Fatalf("progress stream=%+v", events)
				}
				result := events[1].Results[0]
				if result.RequestProtocol == nil || *result.RequestProtocol != TestProtocolResponses || result.CurrentHealthResult == nil || result.CurrentHealthResult.Status != "success" {
					t.Fatalf("SSE lost current protocol result: %+v", result)
				}
			case "automatic":
				for _, job := range service.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments) {
					if job.target.TargetID == target {
						runProtocolServiceJob(service, job)
					}
				}
			}
			if calls != 1 {
				t.Fatalf("model calls=%d", calls)
			}
			if entry != "temporary" && (len(repo.events) != 1 || repo.events[0].RequestProtocol == nil || *repo.events[0].RequestProtocol != TestProtocolResponses || repo.events[0].RequestTimeoutSeconds == nil || *repo.events[0].RequestTimeoutSeconds != 30 || repo.events[0].ProbeDisposition != "applied") {
				t.Fatalf("event lost inherited snapshot: %+v", repo.events)
			}
		})
	}
}

func TestProtocolServiceInFlightConfigurationChangesKeepStateAndDTO(t *testing.T) {
	for _, entry := range []string{"formal", "sse"} {
		for _, change := range []string{"protocol", "timeout", "clear", "empty_group", "same_value"} {
			t.Run(entry+"/"+change, func(t *testing.T) {
				service, repo := protocolServiceFixture(t)
				saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
				calls := 0
				service.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls == 2 {
						switch change {
						case "protocol":
							saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolChatCompletions, 30})
						case "timeout":
							saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 20})
						case "clear":
							saveProtocolServiceConfig(t, repo, "g1", nil)
						case "empty_group":
							saveProtocolServiceConfig(t, repo, "g2", &GroupTestConfiguration{TestProtocolChatCompletions, 30})
						case "same_value":
							saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
						}
						return protocolServiceResponse(req, 500, `{"error":{"message":"later failure"}}`), nil
					}
					return protocolServiceResponse(req, 500, `{"error":{"message":"original failure"}}`), nil
				})
				const target = "sub2api:ws1:shared"
				if _, err := service.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"}); err != nil {
					t.Fatal(err)
				}
				before := repo.states[target]["gpt-4o"]
				var results []ModelHealth
				if entry == "formal" {
					var err error
					results, err = service.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
					if err != nil {
						t.Fatal(err)
					}
				} else {
					events := protocolServiceSSE(t, service, target)
					if len(events) == 0 || events[len(events)-1].Type != "result" {
						t.Fatalf("missing SSE result: %+v", events)
					}
					results = events[len(events)-1].Results
				}
				wantDisposition := "stale"
				if change == "same_value" {
					wantDisposition = "applied"
				}
				if calls != 2 || len(results) != 1 || results[0].ProbeDisposition != wantDisposition || len(repo.events) != 2 || repo.events[1].ProbeDisposition != wantDisposition {
					t.Fatalf("calls=%d results=%+v events=%+v", calls, results, repo.events)
				}
				if wantDisposition == "stale" && !reflect.DeepEqual(before, repo.states[target]["gpt-4o"]) {
					t.Fatal("stale completion changed persistent health, attempt, counters or source")
				}
				result := results[0]
				if result.RequestProtocol == nil || *result.RequestProtocol != TestProtocolResponses || result.RequestTimeoutSeconds == nil || *result.RequestTimeoutSeconds != 30 || !strings.Contains(result.RequestErrorDetail, "later failure") || result.RequestAt == nil || result.RequestLatencyMs == nil {
					t.Fatalf("actual old request result missing: %+v", result)
				}
				if change == "timeout" && (result.CurrentHealthResult == nil || result.CurrentHealthResult.Status != "failure" || !strings.Contains(result.CurrentHealthResult.ErrorDetail, "original failure")) {
					t.Fatalf("timeout-only stale hid valid same-protocol failure: %+v", result.CurrentHealthResult)
				}
				if wantDisposition == "stale" && change != "timeout" && (result.CurrentHealthResult == nil || result.CurrentHealthResult.Status != "unverified") {
					t.Fatalf("different/blocked protocol reused old health: %+v", result.CurrentHealthResult)
				}
				if change == "same_value" && repo.states[target]["gpt-4o"].ConsecutiveFailures != before.ConsecutiveFailures+1 {
					t.Fatal("same effective configuration incorrectly invalidated completion")
				}
				groups, err := service.AdminGroups(context.Background(), "user1")
				if err != nil {
					t.Fatal(err)
				}
				projected := 0
				for _, group := range groups {
					for _, account := range group.Accounts {
						if account.TargetID != target {
							continue
						}
						projected++
						if len(account.ModelHealth) != 1 || !reflect.DeepEqual(account.ModelHealth[0].CurrentHealthResult, result.CurrentHealthResult) {
							t.Fatalf("refresh differs from immediate current-health DTO: %+v vs %+v", account.ModelHealth, result.CurrentHealthResult)
						}
						if wantDisposition == "stale" && (account.ModelHealth[0].LastAttempt == nil || account.ModelHealth[0].LastAttempt.At == nil || !account.ModelHealth[0].LastAttempt.At.Equal(*before.LastProbeAt)) {
							t.Fatal("refresh treated stale request as current last attempt")
						}
					}
				}
				if projected != 2 {
					t.Fatalf("shared target projections=%d", projected)
				}
				failures, err := repo.CountFailureEventsSince(context.Background(), "user1", "ws1", time.Time{}, []string{target})
				wantFailures := 1
				if change == "same_value" {
					wantFailures = 2
				}
				if err != nil || failures != wantFailures {
					t.Fatalf("failure statistics included obsolete result: %d err=%v", failures, err)
				}
				if len(repo.budgetClaims) != 0 || len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0 {
					t.Fatal("manual configuration transition caused automatic accounting/action")
				}
			})
		}
	}
}

func TestProtocolServiceQueuedProbeRechecksConfigurationBeforeDispatch(t *testing.T) {
	for _, entry := range []string{"formal", "automatic"} {
		for _, conflict := range []bool{false, true} {
			name := "changed"
			if conflict {
				name = "conflict"
			}
			t.Run(entry+"/"+name, func(t *testing.T) {
				service, repo := protocolServiceFixture(t)
				var calls atomic.Int32
				service.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					if req.URL.Path != "/v1/responses" {
						t.Errorf("queued request kept old protocol: %s", req.URL.Path)
					}
					return protocolServiceSuccess(req), nil
				})
				change := func() {
					saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
					if conflict {
						saveProtocolServiceConfig(t, repo, "g2", &GroupTestConfiguration{TestProtocolResponses, 20})
					}
				}
				const target = "sub2api:ws1:shared"
				if entry == "automatic" {
					jobs := service.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
					change()
					for _, job := range jobs {
						if job.target.TargetID == target {
							runProtocolServiceJob(service, job)
						}
					}
				} else {
					service.probeLimiter = newProbeConcurrencyLimiter(1, 1)
					release, ok := service.probeLimiter.acquireAutomatic(context.Background(), "user1|ws1")
					if !ok {
						t.Fatal("cannot hold queue slot")
					}
					defer release()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					finished := make(chan error, 1)
					go func() {
						_, err := service.ProbeTarget(ctx, "user1", target, []string{"gpt-4o"})
						finished <- err
					}()
					waitForProbeManualWaiters(t, service.probeLimiter, "user1|ws1", 1)
					change()
					release()
					select {
					case err := <-finished:
						if (err != nil) != conflict {
							t.Fatalf("conflict=%v err=%v", conflict, err)
						}
					case <-time.After(time.Second):
						t.Fatal("queued probe did not finish")
					}
				}
				wantCalls := int32(1)
				if conflict {
					wantCalls = 0
				}
				if calls.Load() != wantCalls {
					t.Fatalf("model calls=%d want=%d", calls.Load(), wantCalls)
				}
				if conflict && (len(repo.states) != 0 || len(repo.events) != 0 || len(repo.budgetClaims) != 0) {
					t.Fatal("queued conflict mutated health or consumed budget")
				}
			})
		}
	}
}

type protocolServiceDeadlineBody struct{ closed bool }

func (*protocolServiceDeadlineBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (b *protocolServiceDeadlineBody) Close() error           { b.closed = true; return nil }

func TestProtocolServiceDeadlinePhaseSurvivesDTOAndHistory(t *testing.T) {
	for _, entry := range []string{"temporary", "formal", "sse"} {
		for _, phase := range []string{"waiting_headers", "reading_body"} {
			t.Run(entry+"/"+phase, func(t *testing.T) {
				service, repo := protocolServiceFixture(t)
				saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
				body := &protocolServiceDeadlineBody{}
				service.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
					if phase == "waiting_headers" {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
				})
				const target = "sub2api:ws1:shared"
				actualPhase, detail := "", ""
				if entry == "temporary" {
					results, err := service.ManualProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
					if err != nil || len(results) != 1 {
						t.Fatalf("results=%+v err=%v", results, err)
					}
					actualPhase, detail = results[0].RequestPhase, results[0].ErrorDetail
				} else {
					var results []ModelHealth
					if entry == "formal" {
						var err error
						results, err = service.ProbeTarget(context.Background(), "user1", target, []string{"gpt-4o"})
						if err != nil {
							t.Fatal(err)
						}
					} else {
						events := protocolServiceSSE(t, service, target)
						if len(events) > 0 {
							results = events[len(events)-1].Results
						}
					}
					if len(results) != 1 {
						t.Fatalf("missing result: %+v", results)
					}
					actualPhase, detail = results[0].RequestPhase, results[0].RequestErrorDetail
					if len(repo.events) != 1 || !strings.Contains(repo.events[0].ErrorDetail, phase) || !strings.Contains(repo.events[0].ErrorDetail, "30") {
						t.Fatalf("history lost configured deadline/stage: %+v", repo.events)
					}
				}
				if actualPhase != phase || !strings.Contains(detail, phase) || !strings.Contains(detail, "30") || (phase == "reading_body" && !body.closed) {
					t.Fatalf("phase=%s detail=%s closed=%v", actualPhase, detail, body.closed)
				}
			})
		}
	}
}

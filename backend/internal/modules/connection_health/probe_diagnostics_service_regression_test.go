package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func TestProbeDiagnosticsServiceAllEntries(t *testing.T) {
	for _, sample := range []struct {
		name              string
		status            int
		contentType, body string
		result            ResultKey
	}{
		{"service", 503, "application/json", `{"error":{"type":"api_error","message":"Service temporarily unavailable fixture"}}`, ResultServerError},
		{"http compatibility", 400, "application/json", `{"error":{"type":"invalid_request_error","code":"unsupported_parameter","message":"parameter rejected fixture"}}`, ResultInvalidResponse},
		{"sse compatibility", 200, "text/event-stream", "data: {\"type\":\"error\",\"error\":{\"code\":\"unsupported_parameter\",\"message\":\"parameter rejected fixture\"}}\n\n", ResultInvalidResponse},
	} {
		for _, entry := range []string{"temporary", "formal-json", "formal-progress", "group-quick", "automatic"} {
			t.Run(sample.name+"/"+entry, func(t *testing.T) {
				service, repo := protocolServiceFixture(t)
				// Formal/automatic tests isolate the independent existing Priority cycle.
				// Once keeps it enabled: any accidental trigger would break its isolation.
				service.backgroundTasksDisabled = entry != "temporary"
				platform := &fakePlatformActioner{}
				service.dispatcher = newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform)
				scheduling := &fakeTargetSchedulableActioner{}
				service.schedulableActions = scheduling
				policy := repo.policies[0]
				policy.RuleVersion = RuleVersionV2
				policy.AutoRemoteActionEnabled = true
				policy.ContinueProbeWhenUnschedulable = true
				policy.UnschedulableProbeIntervalMinutes = 60
				repo.policies[0] = policy
				saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
				const target = "sub2api:ws1:normal"
				old := time.Now().Add(-25 * time.Hour)
				before := ConnectionHealthState{UserID: "user1", AdminAccountID: "ws1", ConnectionID: target, ModelName: "gpt-4o", State: StateSuspended, RuleVersion: RuleVersionV2, CurrentWeight: 0, ConsecutiveFailures: 30, ConsecutiveSuccesses: 1, CounterProtocol: protocolPointer(TestProtocolResponses), LastSuccessAt: &old, LastSuccessLatencyMs: intPtr(80), LastFirstTokenMs: intPtr(60), LastFirstEventMs: intPtr(40), LastProbeAt: &old, LastErrorKey: "server_error", LastErrorDetail: "old failure", HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolResponses), LastSuccessProtocol: protocolPointer(TestProtocolResponses), LastRemoteAction: RemoteActionSub2APIStatusInactive}
				repo.states[target] = map[string]ConnectionHealthState{"gpt-4o": before}
				reader := service.platformGroups.(fakePlatformGroupReader)
				account := reader.accountsByGrp["g1"][1]
				account.Status = "inactive"
				account.Schedulable = boolPointer(false)
				reader.accountsByGrp["g1"][1] = account
				service.platformGroups = reader
				priority := &fakeTargetPriorityActioner{}
				service.priorityActions = priority
				calls := 0
				service.probeRunner.client.Transport = taskATransport(func(r *http.Request) (*http.Response, error) {
					calls++
					resp := protocolServiceResponse(r, sample.status, sample.body)
					resp.Header.Set("Content-Type", sample.contentType)
					return resp, nil
				})
				var detail string
				var firstToken, firstEvent *int
				if entry == "automatic" {
					jobs := service.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
					found := false
					for _, job := range jobs {
						if job.target.TargetID == target {
							found = true
							runProtocolServiceJob(service, job)
						}
					}
					if !found {
						t.Fatal("automatic fixture did not schedule")
					}
				} else if entry == "temporary" || entry == "formal-json" {
					mux := http.NewServeMux()
					RegisterRoutes(mux, service)
					route := "probe"
					if entry == "temporary" {
						route = "manual-probe"
					}
					req := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/"+target+"/"+route, strings.NewReader(`{"models":["gpt-4o"]}`))
					req = req.WithContext(authctx.WithUserID(req.Context(), "user1"))
					resp := httptest.NewRecorder()
					mux.ServeHTTP(resp, req)
					if resp.Code != 200 {
						t.Fatalf("handler status=%d", resp.Code)
					}
					if entry == "temporary" {
						var results []ManualProbeResult
						if err := json.Unmarshal(resp.Body.Bytes(), &results); err != nil || len(results) != 1 {
							t.Fatalf("once DTO: %s", resp.Body.String())
						}
						detail, firstToken, firstEvent = results[0].ErrorDetail, results[0].FirstTokenMs, results[0].FirstEventMs
						if results[0].Result != string(sample.result) {
							t.Fatal("once classification mismatch")
						}
					} else {
						var results []ModelHealth
						if err := json.Unmarshal(resp.Body.Bytes(), &results); err != nil || len(results) != 1 {
							t.Fatalf("formal DTO: %s", resp.Body.String())
						}
						detail, firstToken, firstEvent = results[0].RequestErrorDetail, results[0].RequestFirstTokenMs, results[0].RequestFirstEventMs
						if results[0].ProbeResult != string(sample.result) {
							t.Fatal("formal classification mismatch")
						}
					}
				} else {
					events := protocolServiceSSE(t, service, target)
					if len(events) == 0 || len(events[len(events)-1].Results) != 1 {
						t.Fatalf("no terminal results: %+v", events)
					}
					result := events[len(events)-1].Results[0]
					detail, firstToken, firstEvent = result.RequestErrorDetail, result.RequestFirstTokenMs, result.RequestFirstEventMs
					if result.ProbeResult != string(sample.result) {
						t.Fatal("progress classification mismatch")
					}
				}
				if calls != 1 {
					t.Fatalf("requests=%d want1", calls)
				}
				if entry == "temporary" {
					service.priorityTriggerMu.Lock()
					triggeredPriority := service.priorityHealthRunning != nil || service.priorityHealthPending != nil || service.priorityTriggerRunning != nil || service.priorityTriggerPending != nil
					service.priorityTriggerMu.Unlock()
					if triggeredPriority {
						t.Fatal("once initialized a Priority task")
					}
					if !reflect.DeepEqual(repo.states[target]["gpt-4o"], before) || len(repo.events) != 0 || len(repo.budgetClaims) != 0 || len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0 || len(priority.calls) != 0 {
						t.Fatal("once leaked into health/budget/action/Priority")
					}
				} else {
					if len(repo.events) != 1 {
						t.Fatalf("events=%d", len(repo.events))
					}
					event := repo.events[0]
					if event.Result != string(sample.result) {
						t.Fatalf("event classification=%s", event.Result)
					}
					if entry == "automatic" {
						detail, firstToken, firstEvent = event.ErrorDetail, event.FirstTokenMs, event.FirstEventMs
					} else if event.ErrorDetail != detail {
						t.Fatal("DTO and durable diagnostics differ")
					}
					if sample.result == ResultInvalidResponse {
						after := repo.states[target]["gpt-4o"]
						if after.State != StateSuspended || after.ConsecutiveFailures != 30 || after.ConsecutiveSuccesses != 1 || after.LastErrorDetail != before.LastErrorDetail || !reflect.DeepEqual(after.LastFirstTokenMs, before.LastFirstTokenMs) || event.ProbeDisposition != "invalid" || len(repo.targetActionStates) != 0 || len(priority.calls) != 0 {
							t.Fatalf("invalid result changed health/action: %+v", after)
						}
					}
					if entry != "automatic" && len(repo.budgetClaims) != 0 {
						t.Fatal("formal consumed automatic budget")
					}
				}
				if detail == "" || strings.Contains(detail, "上游未返回流式") || strings.Contains(detail, " fixture") || firstToken != nil {
					t.Fatalf("unsafe/lost diagnostics or fake token: %s", detail)
				}
				if (firstEvent != nil) != (sample.contentType == "text/event-stream") {
					t.Fatal("first event did not follow actual SSE")
				}
				currentAccount := service.platformGroups.(fakePlatformGroupReader).accountsByGrp["g1"][1]
				if currentAccount.Status != "inactive" || *currentAccount.Schedulable || scheduling.calls != 0 || len(platform.sub2APICalls) != 0 {
					t.Fatal("manual scheduling changed")
				}
			})
		}
	}
}

func TestProbeDiagnosticsServiceFailedCommitAndStalePreserveOriginal(t *testing.T) {
	for _, failure := range []string{"storage", "cancel", "stale"} {
		t.Run(failure, func(t *testing.T) {
			service, repo := protocolServiceFixture(t)
			service.backgroundTasksDisabled = true
			saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
			const target = "sub2api:ws1:normal"
			before := ConnectionHealthState{UserID: "user1", AdminAccountID: "ws1", ConnectionID: target, ModelName: "gpt-4o", State: StateSuspended, ConsecutiveFailures: 30, LastErrorDetail: "original"}
			repo.states[target] = map[string]ConnectionHealthState{"gpt-4o": before}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "storage" {
				repo.insertEventErr = errors.New("fixture atomic commit failure")
			}
			service.probeRunner.client.Transport = taskATransport(func(r *http.Request) (*http.Response, error) {
				if failure == "cancel" {
					cancel()
					return nil, context.Canceled
				}
				if failure == "stale" {
					saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolChatCompletions, 30})
				}
				resp := protocolServiceResponse(r, 200, "data: {\"type\":\"error\",\"code\":\"unsupported_parameter\"}\n\n")
				resp.Header.Set("Content-Type", "text/event-stream")
				return resp, nil
			})
			_, err := service.ProbeTarget(ctx, "user1", target, []string{"gpt-4o"})
			if failure == "storage" && err == nil {
				t.Fatal("storage error hidden")
			}
			if !reflect.DeepEqual(repo.states[target]["gpt-4o"], before) {
				t.Fatal("unaccepted result overwrote original")
			}
			if failure != "stale" && len(repo.events) != 0 {
				t.Fatal("uncommitted result wrote event")
			}
			if failure == "stale" && (len(repo.events) != 1 || repo.events[0].ProbeDisposition != "stale") {
				t.Fatalf("stale event missing: %+v", repo.events)
			}
			if len(repo.targetActionStates) != 0 || len(repo.priorityStates) != 0 {
				t.Fatal("unaccepted result caused action")
			}
		})
	}
}

package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

// The policy read observes the disabled generation; the user then enables it.
// A remote restoration must retain the generation captured before that read.
type taskAReviewGenerationRepository struct {
	*fakeRepository
	generation     int64
	nextGeneration int64
	guards         []int64
}

func (r *taskAReviewGenerationRepository) GetWorkspaceHealthSettings(_ context.Context, user, workspace string) (WorkspaceHealthSettings, error) {
	s := defaultWorkspaceHealthSettings(user, workspace)
	s.ConfigGeneration = r.generation
	return s, nil
}
func (r *taskAReviewGenerationRepository) ListWorkspaceHealthSettings(ctx context.Context) ([]WorkspaceHealthSettings, error) {
	s, _ := r.GetWorkspaceHealthSettings(ctx, "user1", "ws1")
	return []WorkspaceHealthSettings{s}, nil
}
func (r *taskAReviewGenerationRepository) ListEnabledPolicies(context.Context) ([]Policy, error) {
	r.generation = r.nextGeneration
	return nil, nil
}
func (r *taskAReviewGenerationRepository) ClaimRemoteAction(ctx context.Context, c RemoteActionClaim) (bool, error) {
	if c.Guard.ConfigGeneration != nil {
		r.guards = append(r.guards, *c.Guard.ConfigGeneration)
		if *c.Guard.ConfigGeneration != r.generation {
			return false, ErrRemoteActionEvidenceChanged
		}
	}
	return r.fakeRepository.ClaimRemoteAction(ctx, c)
}
func TestTaskAReviewUnmanagedRestoreRetainsPrePolicyGeneration(t *testing.T) {
	for _, generation := range []int64{2, 3} {
		t.Run(string(rune('0'+generation)), func(t *testing.T) {
			base := newFakeRepository()
			repo := &taskAReviewGenerationRepository{fakeRepository: base, generation: 1, nextGeneration: generation}
			id := "sub2api:ws1:acc-1"
			base.targetActionStates["user1|ws1|"+id] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalStatus: "active", LastAppliedStatus: "inactive"}
			platform := &fakePlatformActioner{}
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Status: "inactive", Models: "gpt-4o"}}}}
			s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, base)
			s.repo = repo
			s.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
			s.runSchedulerTick(context.Background())
			if len(platform.sub2APICalls) != 0 {
				t.Fatalf("disabled snapshot restored after config change: calls=%v guards=%v current=%d", platform.sub2APICalls, repo.guards, repo.generation)
			}
			for _, guard := range repo.guards {
				if guard != 1 {
					t.Fatalf("restoration replaced initial generation 1 with %d", guard)
				}
			}
		})
	}
}
func TestTaskAReviewInvalidProtocolChangeClearsCountersAndOrigin(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		for _, auto := range []bool{true, false} {
			p := taskAV2Policy()
			p.RuleVersion = version
			p.AutoDegradeEnabled = auto
			current := ConnectionHealthState{State: StateDegraded, CurrentWeight: 75, ConsecutiveFailures: 2, ConsecutiveSuccesses: 1, FailingSince: &old, CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), RecheckPending: true}
			next, transition := applyProbeOutcome(current, ProbeOutcome{Protocol: TestProtocolResponses, Result: ResultInvalidResponse}, p, now)
			if next.FailingSince != nil || next.ConsecutiveFailures != 0 || next.ConsecutiveSuccesses != 0 || next.CounterProtocol == nil || *next.CounterProtocol != TestProtocolResponses || next.RecheckPending {
				t.Errorf("%s auto=%v protocol reset missing: %+v", version, auto, next)
			}
			if next.State != current.State || next.CurrentWeight != current.CurrentWeight || next.HealthEvidenceStatus != current.HealthEvidenceStatus || *next.HealthEvidenceProtocol != *current.HealthEvidenceProtocol || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
				t.Errorf("invalid response changed health evidence/state: %+v %+v", next, transition)
			}
		}
	}
}
func TestTaskAReviewOldProtocolOriginDoesNotDelayNewProtocol(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-25 * time.Hour)
	last := now.Add(-time.Minute)
	p := taskAV2Policy()
	p.ProbeIntervalSeconds = 60
	p.Enabled = true
	target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: "sub2api", Schedulable: boolPointer(true), TestConfiguration: defaultTestConfiguration()}
	target.TestConfiguration.Protocol = TestProtocolResponses
	spec := probeModelSpec{modelName: "m", policy: p, policies: []Policy{p}, maxProbeTokens: 1}
	repo := newFakeRepository()
	repo.states[target.TargetID] = map[string]ConnectionHealthState{"m": {State: StateHealthy, RuleVersion: RuleVersionV2, CurrentWeight: 100, LastProbeAt: &last, FailingSince: &old, CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions)}}
	s := &Service{repo: repo}
	d, ok := s.effectiveProbeDecisionForSpec(context.Background(), target, spec, []Policy{p}, now, nil)
	if !ok || d.NextProbeAt == nil || d.NextProbeAt.After(now) {
		t.Fatalf("new protocol inherited old 24h origin: %+v", d)
	}
	st := repo.states[target.TargetID]["m"]
	st.CounterProtocol = protocolPointer(TestProtocolResponses)
	st.HealthEvidenceProtocol = protocolPointer(TestProtocolResponses)
	st.LastProbeDecisionKey = probeDecisionKey(target, spec)
	repo.states[target.TargetID]["m"] = st
	d, ok = s.effectiveProbeDecisionForSpec(context.Background(), target, spec, []Policy{p}, now, nil)
	if !ok || d.NextProbeAt == nil || !d.NextProbeAt.Equal(last.Add(time.Hour)) {
		t.Fatalf("same-protocol long failure floor lost: %+v", d)
	}
}
func TestTaskAReviewPendingWinsLastEstimatedBudget(t *testing.T) {
	for _, crossAccount := range []bool{false, true} {
		repo := newFakeRepository()
		p := sub2APIProbePolicy(false)
		p.RuleVersion = RuleVersionV2
		p.DailyProbeBudget = 1
		p.ProbeIntervalSeconds = 60
		p.ModelTargets = []ModelTarget{{ModelName: "normal", Enabled: true, MaxProbeTokens: 1}, {ModelName: "pending", Enabled: true, MaxProbeTokens: 1}}
		repo.policies = []Policy{p}
		accounts := []upstream.AdminGroupAccountInfo{{ID: "a", Models: "normal,pending", Status: "active", Schedulable: boolPointer(true)}}
		pendingID := "sub2api:ws1:a"
		if crossAccount {
			accounts = []upstream.AdminGroupAccountInfo{{ID: "a", Models: "normal", Status: "active", Schedulable: boolPointer(true)}, {ID: "b", Models: "pending", Status: "active", Schedulable: boolPointer(true)}}
			pendingID = "sub2api:ws1:b"
		}
		for _, a := range accounts {
			assignPolicyToTarget(repo, p, "sub2api:ws1:"+a.ID)
		}
		repo.states[pendingID] = map[string]ConnectionHealthState{"pending": {State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100, RecheckPending: true}}
		reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g": accounts}}
		s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
		jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
		if len(jobs) != 1 || len(jobs[0].dueSpecs) != 1 || jobs[0].dueSpecs[0].modelName != "pending" || jobs[0].dueSpecs[0].budgetPolicy.ID != p.ID {
			t.Errorf("cross-account=%v last budget did not select pending task: %+v", crossAccount, jobs)
		}
		if len(repo.budgetClaims) != 0 {
			t.Fatal("collection claimed actual request budget")
		}
	}
}
func TestTaskAReviewFormalResponseUsesCurrentMeasurements(t *testing.T) {
	for _, entry := range []string{"http", "sse"} {
		for _, success := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/success=%v", entry, success), func(t *testing.T) {
				s, repo := protocolServiceFixture(t)
				now := time.Now().UTC()
				first, event := 1000, 900
				last := now.Add(-time.Minute)
				repo.states["sub2api:ws1:normal"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:normal", ModelName: "gpt-4o", State: StateHealthy, CurrentWeight: 100, LastLatencyMs: intPtr(2000), LastFirstTokenMs: &first, LastFirstEventMs: &event, LastSuccessAt: &last, LastSuccessLatencyMs: intPtr(2000), LastSuccessProtocol: protocolPointer(TestProtocolChatCompletions), LastProbeAt: &last, HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), CounterProtocol: protocolPointer(TestProtocolChatCompletions)}}
				s.probeRunner = &RealProbeRunner{now: func() time.Time { return now }, client: &http.Client{Transport: taskATransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &taskATimedReader{now: &now, frames: []taskATimedFrame{{7 * time.Second, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"}, {time.Second, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"}, {time.Second, ""}}}}, nil
				})}}
				if !success {
					s.probeRunner.client.Transport = taskATransport(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &taskATimedReader{now: &now, frames: []taskATimedFrame{{7 * time.Second, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"}}}}, nil
					})
				}
				var result any
				if entry == "http" {
					out, err := s.ProbeTarget(context.Background(), "user1", "sub2api:ws1:normal", []string{"gpt-4o"})
					if err != nil || len(out) != 1 {
						t.Fatal(out, err)
					}
					result = out[0]
				} else {
					events := protocolServiceSSE(t, s, "sub2api:ws1:normal")
					for _, e := range events {
						if len(e.Results) == 1 {
							result = e.Results[0]
						}
					}
					if result == nil {
						t.Fatalf("no SSE result: %+v", events)
					}
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err = json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				wantToken, wantHistoryToken, wantHistoryEvent := any(nil), float64(1000), float64(900)
				if success {
					wantToken, wantHistoryToken, wantHistoryEvent = float64(8000), float64(8000), float64(7000)
				}
				if fields["requestFirstTokenMs"] != wantToken || fields["requestFirstEventMs"] != float64(7000) {
					t.Fatalf("formal result missing current 8000/7000 measurements: %s", data)
				}
				if fields["firstTokenMs"] != wantHistoryToken || fields["firstEventMs"] != wantHistoryEvent {
					t.Fatalf("current failure overwrote historical success: %s", data)
				}
			})
		}
	}
}

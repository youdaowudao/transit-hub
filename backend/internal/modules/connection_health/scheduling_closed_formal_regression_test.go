package connection_health

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestManualFormalProbeRestoresWhileUnschedulable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		thresholds     []int
		alreadyHealthy bool
	}{
		{"two_successes", []int{2}, false},
		{"three_successes", []int{3}, false},
		{"models_two_and_three", []int{2, 3}, false},
		{"already_healthy_one_new_result", []int{3}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepository()
			targetID, key := "sub2api:ws1:a", "user1|ws1|sub2api:ws1:a"
			models := make([]string, len(tc.thresholds))
			repo.states[targetID] = make(map[string]ConnectionHealthState)
			last, origin := time.Now().Add(-time.Hour), time.Now().Add(-25*time.Hour)
			maximum := 1
			for i, threshold := range tc.thresholds {
				model := fmt.Sprintf("model-%d", i+1)
				models[i] = model
				preset := recommendedRulePreset()
				preset.SuccessThreshold = threshold
				p := sub2APIProbePolicy(true)
				p.ID, p.RuleVersion, p.RulePreset = fmt.Sprintf("policy-%d", i+1), RuleVersionV2, &preset
				p.SuccessThreshold, p.PriorityMode = threshold, PriorityModeNone
				p.ContinueProbeWhenUnschedulable, p.UnschedulableProbeIntervalMinutes = false, 60
				p.ModelTargets = []ModelTarget{{PolicyID: p.ID, ModelName: model, ProviderFamily: ProviderOpenAI, Enabled: true, MaxProbeTokens: 1}}
				repo.policies = append(repo.policies, p)
				assignPolicyToTarget(repo, p, targetID)
				result := string(ResultAuth)
				state := ConnectionHealthState{ConnectionID: targetID, UserID: "user1", AdminAccountID: "ws1", ModelName: model, RuleVersion: RuleVersionV2, State: StateSuspended, CurrentWeight: 0, ConsecutiveFailures: 3, LastProbeAt: &last, LastAppliedProbeAt: &last, LastAppliedProbeResult: &result, LastAppliedProbeProtocol: protocolPointer(TestProtocolChatCompletions), CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), FailingSince: &origin}
				if tc.alreadyHealthy {
					result = string(ResultOK)
					state.State, state.CurrentWeight, state.ConsecutiveFailures, state.FailingSince = StateHealthy, 100, 0, nil
					state.LastSuccessAt, state.LastSuccessProtocol = &last, protocolPointer(TestProtocolChatCompletions)
				} else if threshold > maximum {
					maximum = threshold
				}
				repo.states[targetID][model] = state
			}
			repo.targetActionStates[key] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "isolated"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Name: "isolated", Platform: "openai", Type: "apikey", Models: strings.Join(models, ","), Status: "inactive", Priority: intPointer(100), Schedulable: boolPointer(false), TempUnschedulableKnown: true}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}
			platform := &fakePlatformActioner{afterSub2APIWrite: func(_ string, status string) { reader.accountsByGrp["g1"][0].Status = status }}
			s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			s.dispatcher, s.schedulableActions = newRemoteActionDispatcher(nil, nil, platform), forbiddenAutomaticSchedulingWriter{t}
			s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")), Request: request}, nil
			})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.Shutdown(ctx); err != nil {
					t.Errorf("formal fixture shutdown: %v", err)
				}
			})
			for round := 1; round <= maximum; round++ {
				results, err := s.ProbeTarget(context.Background(), "user1", targetID, models)
				if err != nil || len(results) != len(models) {
					t.Fatalf("formal round %d did not complete: count=%d err=%v", round, len(results), err)
				}
				for i, model := range models {
					want := StateSuspended
					if tc.alreadyHealthy || round >= tc.thresholds[i] {
						want = StateHealthy
					}
					if got := repo.states[targetID][model].State; got != want {
						t.Errorf("round %d model %s state=%s want=%s; keep its own preset threshold", round, model, got, want)
					}
				}
				wantWrites, wantStatus := 0, "inactive"
				if round == maximum {
					wantWrites, wantStatus = 1, "active"
				}
				account := reader.accountsByGrp["g1"][0]
				if len(platform.sub2APICalls) != wantWrites || account.Status != wantStatus || *account.Schedulable {
					t.Errorf("round %d status=%s writes=%d scheduling=%t want status=%s writes=%d scheduling=false", round, account.Status, len(platform.sub2APICalls), *account.Schedulable, wantStatus, wantWrites)
				}
			}
			if _, err := s.ProbeTarget(context.Background(), "user1", targetID, models); err != nil {
				t.Fatal(err)
			}
			if _, exists := repo.targetActionStates[key]; exists || len(platform.sub2APICalls) != 1 {
				t.Errorf("confirmed restored checkpoint retained or status rewritten: checkpoint=%t writes=%d", exists, len(platform.sub2APICalls))
			}
		})
	}
}

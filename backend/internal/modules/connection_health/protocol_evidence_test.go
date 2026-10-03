package connection_health

import (
	"context"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func protocolEvidenceState(state State, weight int, now time.Time) ConnectionHealthState {
	last := now.Add(-time.Hour)
	failure := string(ResultRateLimited)
	return ConnectionHealthState{
		ConnectionID: "sub2api:ws1:shared", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o",
		State: state, CurrentWeight: weight, ConsecutiveFailures: 2, ConsecutiveSuccesses: 7,
		CounterProtocol:      protocolPointer(TestProtocolChatCompletions),
		HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions),
		LastProbeAt: &last, LastAppliedProbeAt: &last, LastAppliedProbeResult: &failure,
		LastAppliedProbeProtocol: protocolPointer(TestProtocolChatCompletions),
		LastFailureAt:            &last, LastErrorKey: failure, LastErrorDetail: "original failure",
	}
}

func TestProtocolEvidenceUnverifiedProjectionPreservesSchedulerBlock(t *testing.T) {
	for _, reason := range []string{"", "budget_exhausted", "cooldown", "disabled"} {
		t.Run(reason, func(t *testing.T) {
			state := protocolEvidenceState(StateHealthy, 100, time.Now())
			model := ModelHealth{BlockedReason: reason}
			applyCurrentHealthProjection(&model, state, EffectiveTestConfiguration{Status: "inherited", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30})
			if model.CurrentHealthResult == nil || model.CurrentHealthResult.Status != "unverified" {
				t.Fatalf("new protocol must remain unverified: %+v", model.CurrentHealthResult)
			}
			if model.BlockedReason != reason {
				t.Fatalf("health projection replaced scheduler block %q with %q", reason, model.BlockedReason)
			}
		})
	}
}

func TestProtocolEvidenceCredentialPreparationKeepsAppliedFailureAfterEventRetention(t *testing.T) {
	repo := newFakeRepository()
	svc := &Service{repo: repo}
	now := time.Now()
	state := protocolEvidenceState(StateDegraded, 60, now)
	state.LastRemoteAction = RemoteActionSub2APIStatusInactive
	state.LastProbeProtocol = protocolPointer(TestProtocolChatCompletions)
	state.LastProbeTimeoutSeconds = intPointer(10)
	repo.states[state.ConnectionID] = map[string]ConnectionHealthState{state.ModelName: state}
	policy := Policy{ID: "credential-regression", Enabled: true, ProbeIntervalSeconds: 60}
	spec := probeModelSpec{modelName: state.ModelName, policy: policy, policies: []Policy{policy}}
	target := AdminProbeTarget{TargetID: state.ConnectionID, Platform: "sub2api", AccountID: "shared", InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
	svc.recordTargetCredentialUnavailable(context.Background(), state.UserID, state.AdminAccountID, target, []probeModelSpec{spec}, upstream.ReasonCredentialUnavailable)
	stored := repo.states[state.ConnectionID][state.ModelName]
	if stored.LastErrorKey != state.LastErrorKey || stored.LastErrorDetail != state.LastErrorDetail {
		t.Errorf("credential preparation replaced applied failure: key=%q detail=%q", stored.LastErrorKey, stored.LastErrorDetail)
	}
	if !reflect.DeepEqual(stored.LastProbeAt, state.LastProbeAt) || !reflect.DeepEqual(stored.LastAppliedProbeAt, state.LastAppliedProbeAt) || stored.ConsecutiveFailures != state.ConsecutiveFailures || stored.State != state.State || stored.LastRemoteAction != state.LastRemoteAction {
		t.Error("credential preparation changed request metadata, health counters, or remote-action ownership")
	}
	if len(repo.events) != 1 || repo.events[0].ErrorKey != upstream.ReasonCredentialUnavailable {
		t.Fatalf("credential preparation must remain visible in its own audit event: %+v", repo.events)
	}
	// The current result must come from durable state even after event retention.
	repo.events = nil
	models, _ := modelHealthForSpecs(map[string]ConnectionHealthState{stored.ModelName: stored}, []probeModelSpec{spec}, target, now, nil, true)
	if len(models) != 1 || models[0].CurrentHealthResult == nil {
		t.Fatal("current result missing")
	}
	current := models[0].CurrentHealthResult
	if current.Status != "failure" || current.ErrorKey != state.LastErrorKey || current.ErrorDetail != state.LastErrorDetail || current.At == nil || !current.At.Equal(*state.LastAppliedProbeAt) {
		t.Errorf("retained current health falsely borrows preparation failure: %+v", current)
	}
	if got := latestCredentialUnavailableReason(models); got != upstream.ReasonCredentialUnavailable {
		t.Errorf("credential-unavailable presentation disappeared: %q", got)
	}
	decision := calculateEffectiveProbeDecision([]Policy{policy}, boolPointer(true), &stored, stored.UpdatedAt)
	wantInterval := max(time.Minute, ProbeBackoff(state.ConsecutiveFailures))
	if decision.NextProbeAt == nil || !decision.NextProbeAt.Equal(stored.UpdatedAt.Add(wantInterval)) {
		t.Errorf("credential preparation must keep the same retry interval: %+v", decision)
	}
	if len(repo.budgetClaims) != 0 {
		t.Errorf("credential preparation consumed model request budget: %+v", repo.budgetClaims)
	}
	if models[0].LastAttempt == nil || models[0].LastAttempt.ErrorKey != state.LastErrorKey {
		t.Errorf("last actual request borrows the credential preparation error: %+v", models[0].LastAttempt)
	}
}

func TestProtocolEvidenceSourceTransferTable(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	for _, example := range []struct {
		name       string
		state      State
		weight     int
		result     ResultKey
		wantState  State
		wantWeight int
		valid      bool
	}{
		{"hard_from_healthy", StateHealthy, 100, ResultAuth, StateSuspended, 0, true},
		{"hard_from_suspended", StateSuspended, 0, ResultServerError, StateSuspended, 0, true},
		{"soft_to_degraded", StateHealthy, 100, ResultRateLimited, StateDegraded, 75, true},
		{"soft_still_suspended_below_new_threshold", StateSuspended, 0, ResultNetworkFluctuation, StateSuspended, 0, false},
		{"ok_to_healthy", StateHealthy, 100, ResultOK, StateHealthy, 100, true},
		{"ok_to_recovering", StateRecovering, 25, ResultOK, StateRecovering, 50, true},
		{"ok_still_degraded", StateDegraded, 25, ResultOK, StateDegraded, 50, false},
		{"ok_to_observing", StateSuspended, 0, ResultOK, StateObserving, 0, false},
		{"ok_still_observing", StateObserving, 0, ResultOK, StateObserving, 0, false},
		{"slow_from_healthy", StateHealthy, 100, ResultSlowResponse, StateDegraded, 100, true},
		{"slow_from_recovering", StateRecovering, 25, ResultSlowResponse, StateDegraded, 25, true},
		{"slow_still_suspended", StateSuspended, 0, ResultSlowResponse, StateSuspended, 0, false},
		{"slow_still_observing", StateObserving, 0, ResultSlowResponse, StateObserving, 0, false},
	} {
		t.Run(example.name, func(t *testing.T) {
			state := protocolEvidenceState(example.state, example.weight, now)
			until := now.Add(time.Minute)
			if example.state == StateObserving {
				state.ObservingUntil = &until
			}
			outcome := ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: example.result, LatencyMs: 20}
			next, _ := applyProbeOutcome(state, outcome, sub2APIProbePolicy(false), now)
			if next.State != example.wantState || next.CurrentWeight != example.wantWeight {
				t.Fatalf("old control transition changed: state=%s weight=%d", next.State, next.CurrentWeight)
			}
			if healthEvidenceMatches(next, TestProtocolResponses) != example.valid || healthEvidenceMatches(next, TestProtocolChatCompletions) {
				t.Fatalf("source transfer=%s/%v want valid=%v", next.HealthEvidenceStatus, next.HealthEvidenceProtocol, example.valid)
			}
			if !example.valid && (next.HealthEvidenceStatus != HealthEvidenceInvalid || next.HealthEvidenceProtocol != nil) {
				t.Fatal("unproven output retained old source label")
			}
			if next.CounterProtocol == nil || *next.CounterProtocol != TestProtocolResponses || next.LastAppliedProbeProtocol == nil || *next.LastAppliedProbeProtocol != TestProtocolResponses {
				t.Fatal("actual result/counter source missing")
			}
		})
	}
}

func TestProtocolEvidenceClosedGatesNeverTransferSourceOrCounterProtocol(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	for _, gate := range []string{"auto_degrade_off", "disabled"} {
		for _, result := range []ResultKey{ResultOK, ResultSlowResponse, ResultRateLimited, ResultServerError, ResultInvalidResponse} {
			t.Run(gate+"/"+string(result), func(t *testing.T) {
				state := protocolEvidenceState(StateDegraded, 25, now)
				policy := sub2APIProbePolicy(false)
				if gate == "disabled" {
					state.State, state.CurrentWeight = StateDisabled, 0
				} else {
					policy.AutoDegradeEnabled = false
				}
				next, transition := applyProbeOutcome(state, ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: result, LatencyMs: 12000}, policy, now)
				if next.State != state.State || next.CurrentWeight != state.CurrentWeight || !reflect.DeepEqual(next.CounterProtocol, state.CounterProtocol) || next.HealthEvidenceStatus != state.HealthEvidenceStatus || !reflect.DeepEqual(next.HealthEvidenceProtocol, state.HealthEvidenceProtocol) {
					t.Fatalf("closed gate changed control/source: %+v", next)
				}
				if healthEvidenceMatches(next, TestProtocolResponses) || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
					t.Fatal("closed gate authorized current-protocol health or remote action")
				}
				if gate == "auto_degrade_off" && (next.ConsecutiveFailures != state.ConsecutiveFailures || next.ConsecutiveSuccesses != state.ConsecutiveSuccesses) {
					t.Fatal("auto-degrade-off changed counters")
				}
				if result == ResultInvalidResponse && (!reflect.DeepEqual(next.LastAppliedProbeAt, state.LastAppliedProbeAt) || next.LastErrorDetail != state.LastErrorDetail || next.ConsecutiveSuccesses != state.ConsecutiveSuccesses || next.ConsecutiveFailures != state.ConsecutiveFailures) {
					t.Fatal("invalid attempt bypassed the closed gate")
				}
			})
		}
	}
}

func TestProtocolEvidenceSlowSequenceKeepsRecoveryGates(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	for _, original := range []State{StateHealthy, StateSuspended, StateObserving} {
		t.Run(string(original), func(t *testing.T) {
			weight := 0
			if original == StateHealthy {
				weight = 100
			}
			state := protocolEvidenceState(original, weight, now)
			until := now.Add(time.Minute)
			if original == StateObserving {
				state.ObservingUntil = &until
			}
			for index := 0; index < 4; index++ {
				var transition TransitionOutput
				state, transition = applyProbeOutcome(state, ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: ResultSlowResponse, LatencyMs: 12000}, sub2APIProbePolicy(false), now.Add(time.Duration(index)*time.Minute))
				want := original
				if original == StateHealthy {
					want = StateDegraded
				}
				if state.State != want || state.CurrentWeight != weight || state.ConsecutiveFailures != 0 || state.ConsecutiveSuccesses != 0 || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
					t.Fatalf("slow-only sequence manufactured recovery: %+v", state)
				}
				if healthEvidenceMatches(state, TestProtocolResponses) != (original == StateHealthy) {
					t.Fatal("slow response granted suspended/observing health evidence")
				}
				if successLatencyForProtocol(state, TestProtocolResponses) != nil && original != StateHealthy {
					t.Fatal("unverified slow response became usable sorting latency")
				}
				if original == StateObserving && (state.ObservingUntil == nil || !state.ObservingUntil.Equal(until)) {
					t.Fatal("slow sequence changed observation window")
				}
			}
		})
	}
}

func TestProtocolEvidenceObservingAndDegradedRequireOriginalRecoveryThresholds(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	policy := sub2APIProbePolicy(false)
	observing := protocolEvidenceState(StateObserving, 0, now)
	until := now.Add(time.Minute)
	observing.ObservingUntil = &until
	outcome := ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: ResultOK}
	observing, transition := applyProbeOutcome(observing, outcome, policy, now)
	if observing.State != StateObserving || observing.ConsecutiveSuccesses != 1 || healthEvidenceMatches(observing, TestProtocolResponses) || transition.TriggerRemoteRestore {
		t.Fatal("old Chat successes bypassed new-protocol observation")
	}
	observing, transition = applyProbeOutcome(observing, outcome, policy, until)
	if observing.State != StateRecovering || observing.ConsecutiveSuccesses != 2 || !healthEvidenceMatches(observing, TestProtocolResponses) || !transition.TriggerRemoteRestore {
		t.Fatal("current protocol could not recover after both original gates")
	}
	degraded := protocolEvidenceState(StateDegraded, 25, now)
	for index, weight := range []int{50, 75, 100} {
		degraded, _ = applyProbeOutcome(degraded, outcome, policy, now.Add(time.Duration(index)*time.Minute))
		if degraded.CurrentWeight != weight || healthEvidenceMatches(degraded, TestProtocolResponses) != (weight == 100) || degraded.ConsecutiveSuccesses != index+1 {
			t.Fatalf("degraded recovery skipped original weight step: %+v", degraded)
		}
	}
}

func TestProtocolEvidenceLegacyChatCompatibilityAndConfigurationOnlyRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	for _, timeout := range []int{10, 30, 120} {
		state := protocolEvidenceState(StateSuspended, 0, now)
		state.HealthEvidenceStatus, state.HealthEvidenceProtocol, state.CounterProtocol = HealthEvidenceLegacy, nil, nil
		state.LastAppliedProbeAt, state.LastAppliedProbeResult, state.LastAppliedProbeProtocol = nil, nil, nil
		state.LastSuccessLatencyMs = intPtr(40)
		configuration := EffectiveTestConfiguration{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: timeout, Status: "inherited"}
		if !healthEvidenceMatches(state, configuration.Protocol) || projectCurrentHealth(state, configuration).Status != "failure" || successLatencyForProtocol(state, configuration.Protocol) == nil {
			t.Fatalf("legacy Chat/%d lost upgrade compatibility", timeout)
		}
		next, _ := applyProbeOutcome(state, ProbeOutcome{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: timeout, Result: ResultRateLimited}, sub2APIProbePolicy(false), now)
		if next.ConsecutiveFailures != state.ConsecutiveFailures+1 || !healthEvidenceMatches(next, TestProtocolChatCompletions) || next.CounterProtocol == nil || *next.CounterProtocol != TestProtocolChatCompletions {
			t.Fatalf("explicit Chat reset legacy counters: %+v", next)
		}
	}
	for _, converted := range []bool{false, true} {
		repo := newFakeRepository()
		state := protocolEvidenceState(StateSuspended, 0, now)
		memberships := []TestConfigurationSource{{AdminGroupID: "g1"}}
		saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolResponses, 30})
		if converted {
			state, _ = applyProbeOutcome(state, ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: ResultOK}, sub2APIProbePolicy(false), now)
		}
		saveProtocolServiceConfig(t, repo, "g1", nil)
		configs, err := repo.ListGroupTestConfigurations(context.Background(), "user1", "ws1")
		if err != nil {
			t.Fatal(err)
		}
		configuration := ResolveGroupTestConfiguration(memberships, true, configs)
		if configuration.Protocol != TestProtocolChatCompletions || healthEvidenceMatches(state, configuration.Protocol) == converted {
			t.Fatalf("configuration-only vs converted clear conflated: converted=%v state=%+v", converted, state)
		}
		if converted && projectCurrentHealth(state, configuration).Status != "unverified" {
			t.Fatal("clear revived invalid Chat evidence")
		}
	}
}

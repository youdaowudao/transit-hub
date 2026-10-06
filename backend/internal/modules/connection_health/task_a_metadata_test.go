package connection_health

import (
	"context"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestTaskACredentialPreparationRuleAndLongFailureMetadata(t *testing.T) {
	for _, sample := range []struct {
		name, platform, rule string
		existing             bool
		long                 bool
	}{
		{"new v2", string(upstream.PlatformSub2API), RuleVersionV2, false, false},
		{"new legacy", string(upstream.PlatformSub2API), RuleVersionLegacy, false, false},
		{"new NewAPI", string(upstream.PlatformNewAPI), RuleVersionLegacy, false, false},
		{"existing long v2", string(upstream.PlatformSub2API), RuleVersionV2, true, true},
		{"existing short legacy", string(upstream.PlatformSub2API), RuleVersionLegacy, true, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			repo := newFakeRepository()
			service := &Service{repo: repo}
			target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: sample.platform, AccountID: "a"}
			if sample.platform != string(upstream.PlatformSub2API) {
				target.TargetID = "newapi:ws1:a"
			}
			policy := taskAV2Policy()
			if sample.platform == string(upstream.PlatformSub2API) {
				policy.RuleVersion = sample.rule
			}
			policy.ID = "p"
			spec := probeModelSpec{modelName: "m", policy: policy}
			before := defaultTargetState("user1", "ws1", target, "m")
			if sample.existing {
				since := time.Now().Add(-time.Hour)
				if sample.long {
					since = time.Now().Add(-25 * time.Hour)
				}
				before.RuleVersion = sample.rule
				before.State = StateSuspended
				before.CurrentWeight = 0
				before.ConsecutiveFailures = 4
				before.FailingSince = &since
				before.RecheckPending = true
				before.HealthEvidenceStatus = HealthEvidenceValid
				before.HealthEvidenceProtocol = protocolPointer(TestProtocolResponses)
				before.LastRemoteAction = RemoteActionSub2APIStatusInactive
				repo.states[target.TargetID] = map[string]ConnectionHealthState{"m": before}
			}
			service.recordTargetCredentialUnavailable(context.Background(), "user1", "ws1", target, []probeModelSpec{spec}, upstream.ReasonCredentialUnavailable)
			state := repo.states[target.TargetID]["m"]
			if state.RuleVersion != sample.rule || state.RecheckPending || state.State != before.State || state.CurrentWeight != before.CurrentWeight || state.ConsecutiveFailures != before.ConsecutiveFailures || state.ConsecutiveSuccesses != before.ConsecutiveSuccesses || !reflect.DeepEqual(state.FailingSince, before.FailingSince) || state.LastProbeAt != nil || state.LastAppliedProbeAt != nil || state.HealthEvidenceStatus != before.HealthEvidenceStatus || !reflect.DeepEqual(state.HealthEvidenceProtocol, before.HealthEvidenceProtocol) || state.LastRemoteAction != before.LastRemoteAction {
				t.Fatalf("credential metadata changed probe/health contract before=%+v after=%+v", before, state)
			}
			if len(repo.events) != 1 {
				t.Fatalf("events=%d", len(repo.events))
			}
			event := repo.events[0]
			if event.RuleVersion != sample.rule || event.LongFailure == nil || *event.LongFailure != sample.long || event.Result != string(ResultUnsupported) || event.FirstTokenMs != nil || event.FirstEventMs != nil || event.RemoteAction != "" || event.Source != EventSourceScheduled {
				t.Fatalf("credential event metadata %+v", event)
			}
		})
	}
}

func TestTaskAFirstTokenProjectionFollowsCurrentSuccessProtocol(t *testing.T) {
	state := ConnectionHealthState{State: StateHealthy, HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), LastSuccessProtocol: protocolPointer(TestProtocolChatCompletions), LastSuccessLatencyMs: intPtr(8000), LastFirstTokenMs: intPtr(3000), LastFirstEventMs: intPtr(1000)}
	original := state
	for _, sample := range []struct {
		name            string
		protocol        TestProtocol
		evidence        string
		successProtocol TestProtocol
		visible         bool
	}{
		{"same protocol", TestProtocolChatCompletions, HealthEvidenceValid, TestProtocolChatCompletions, true},
		{"changed protocol", TestProtocolResponses, HealthEvidenceValid, TestProtocolChatCompletions, false},
		{"current failure with previous protocol success", TestProtocolResponses, HealthEvidenceValid, TestProtocolChatCompletions, false},
		{"invalid current evidence", TestProtocolChatCompletions, HealthEvidenceInvalid, TestProtocolChatCompletions, false},
		{"new current success", TestProtocolResponses, HealthEvidenceValid, TestProtocolResponses, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			candidate := state
			candidate.HealthEvidenceStatus = sample.evidence
			if sample.name != "changed protocol" {
				candidate.HealthEvidenceProtocol = protocolPointer(sample.protocol)
			}
			if sample.evidence == HealthEvidenceInvalid {
				candidate.HealthEvidenceProtocol = nil
			}
			candidate.LastSuccessProtocol = protocolPointer(sample.successProtocol)
			before := candidate
			model := toModelHealth("m", candidate)
			applyCurrentHealthProjection(&model, candidate, EffectiveTestConfiguration{Status: "inherited", Protocol: sample.protocol, ProbeTimeoutSeconds: 30})
			if sample.visible {
				if model.FirstTokenMs == nil || *model.FirstTokenMs != 3000 || model.FirstEventMs == nil || *model.FirstEventMs != 1000 || model.LastSuccessLatencyMs == nil {
					t.Fatalf("current success measurements hidden %+v", model)
				}
			} else if model.FirstTokenMs != nil || model.FirstEventMs != nil || model.LastSuccessLatencyMs != nil {
				t.Fatalf("old protocol measurements displayed %+v", model)
			}
			if !reflect.DeepEqual(candidate, before) {
				t.Fatal("projection mutated persisted success evidence")
			}
		})
	}
	if !reflect.DeepEqual(state, original) {
		t.Fatal("projection changed source state")
	}
}

func TestTaskAManualCredentialFailuresPreservePendingAndState(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		t.Run(map[bool]string{false: "formal", true: "one shot"}[oneShot], func(t *testing.T) {
			repo := newFakeRepository()
			policy := sub2APIProbePolicy(false)
			policy.RuleVersion = RuleVersionV2
			repo.policies = []Policy{policy}
			targetID := "sub2api:ws1:a"
			assignPolicyToTarget(repo, policy, targetID)
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g": {{ID: "a", Status: "active", Schedulable: boolPointer(true), Models: "gpt-4o"}}}, credErr: map[string]error{"a": requestError(ErrorRequest)}}
			service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			since := time.Now().Add(-time.Hour)
			state := ConnectionHealthState{ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100, ConsecutiveFailures: 1, FailingSince: &since, RecheckPending: true, HealthEvidenceStatus: HealthEvidenceLegacy}
			repo.states[targetID] = map[string]ConnectionHealthState{"gpt-4o": state}
			var err error
			if oneShot {
				_, err = service.ManualProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
			} else {
				_, err = service.ProbeTarget(context.Background(), "user1", targetID, nil)
			}
			if err == nil {
				t.Fatal("credential failure fixture unexpectedly succeeded")
			}
			if !reflect.DeepEqual(repo.states[targetID]["gpt-4o"], state) || len(repo.events) != 0 || len(repo.budgetClaims) != 0 {
				t.Fatal("manual credential failure changed pending/state/events/budget")
			}
		})
	}
}

func TestTaskACredentialNewRowUsesCurrentWorkspaceRulePostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	target := AdminProbeTarget{TargetID: "sub2api:w:new", Platform: string(upstream.PlatformSub2API)}
	initial := defaultTargetState("u", "w", target, "m")
	initial.RuleVersion = RuleVersionLegacy
	state, err := r.RecordTargetCredentialFailure(ctx, initial, upstream.ReasonCredentialUnavailable, time.Now())
	if err != nil || state.RuleVersion != RuleVersionV2 || state.LastProbeAt != nil || state.LastAppliedProbeAt != nil || state.FailingSince != nil || state.HealthEvidenceStatus != HealthEvidenceInvalid || state.RecheckPending {
		t.Fatalf("new credential row %+v %v", state, err)
	}
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); err != nil {
		t.Fatal(err)
	}
	initial.ConnectionID = "sub2api:w:after-switch"
	initial.RuleVersion = RuleVersionV2
	// Credential lookup captured v2, but the switch happened before this short write.
	after, err := r.RecordTargetCredentialFailure(ctx, initial, upstream.ReasonCredentialUnavailable, time.Now())
	if err != nil || after.RuleVersion != RuleVersionLegacy || after.LastProbeAt != nil || after.LastAppliedProbeAt != nil || after.FailingSince != nil || after.HealthEvidenceStatus != HealthEvidenceInvalid || after.RecheckPending {
		t.Fatalf("credential wrote old captured rule after switch %+v %v", after, err)
	}
}

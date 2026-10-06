package connection_health

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/modules/upstream"
)

func taskAReviewThresholdOnePolicy(version string) Policy {
	p := taskAV2Policy()
	p.RuleVersion = version
	preset := DefaultRulePreset()
	preset.Kind = PresetLegacySnapshot
	preset.FailureThreshold = 1
	preset.SuccessThreshold = 1
	preset.CooldownSeconds = 30
	p.RulePreset = &preset
	return p
}
func TestTaskAReviewLegacyThresholdOneStillStartsSuspect(t *testing.T) {
	for _, tc := range []struct {
		result       ResultKey
		legacyState  State
		legacyWeight int
		legacyRemote bool
	}{
		{ResultServerError, StateSuspended, 0, true},
		{ResultAuth, StateSuspended, 0, true},
		{ResultModelNotFound, StateSuspended, 0, true},
		{ResultNetworkFluctuation, StateDegraded, 75, false},
		{ResultRateLimited, StateDegraded, 75, false},
	} {
		result := tc.result
		t.Run(string(result), func(t *testing.T) {
			now := time.Now().UTC()
			p := taskAReviewThresholdOnePolicy(RuleVersionV2)
			current := ConnectionHealthState{State: StateHealthy, CurrentWeight: 100, RuleVersion: RuleVersionV2, CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions)}
			next, transition := applyProbeOutcome(current, ProbeOutcome{Result: result, Protocol: TestProtocolChatCompletions}, p, now)
			if next.State != StateSuspect || next.CurrentWeight != 100 || next.ConsecutiveFailures != 1 || !next.RecheckPending || next.CooldownUntil != nil || transition.TriggerRemoteDegrade || transition.TriggerRemoteRestore {
				t.Fatalf("snapshot threshold1 must not bypass first suspect: state=%+v transition=%+v", next, transition)
			}
			if p.RulePreset.FailureThreshold != 1 {
				t.Fatal("stored snapshot value was clamped")
			}
			second, action := applyProbeOutcome(next, ProbeOutcome{Result: result, Protocol: TestProtocolChatCompletions}, p, now.Add(time.Second))
			if second.State != StateSuspended || second.CurrentWeight != 0 || second.RecheckPending || !action.TriggerRemoteDegrade {
				t.Fatalf("repeat failure must still enforce legacy snapshot threshold: %+v %+v", second, action)
			}
			healthy, _ := applyProbeOutcome(next, ProbeOutcome{Result: ResultSlowResponse, Protocol: TestProtocolChatCompletions}, p, now.Add(time.Second))
			if healthy.State != StateHealthy || healthy.ConsecutiveFailures != 0 || healthy.RecheckPending {
				t.Fatalf("completed slow success must recover snapshot suspect: %+v", healthy)
			}
			p.RuleVersion = RuleVersionLegacy
			legacy, legacyAction := applyProbeOutcome(current, ProbeOutcome{Result: result, Protocol: TestProtocolChatCompletions}, p, now)
			if legacy.State != tc.legacyState || legacy.CurrentWeight != tc.legacyWeight || legacyAction.TriggerRemoteDegrade != tc.legacyRemote || legacy.RecheckPending {
				t.Fatalf("legacy threshold1 behavior changed: %+v %+v", legacy, legacyAction)
			}
		})
	}
}
func TestTaskAReviewThresholdOneRuleSwitchFirstFailureNoRemotePostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := migrations.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	p := sub2APIProbePolicy(true)
	if err := r.SavePolicyWithTargets(ctx, p, p.ModelTargets); err != nil {
		t.Fatal(err)
	}
	// Isolated pre-upgrade fixture preserves the originally legitimate numbers.
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_rule_presets(id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,observation_seconds,recovery_step_percent) VALUES('before','user1','ws1','升级前阈值一','legacy_snapshot',1,1,30,600,86400,3600,300,25);
 UPDATE connection_health_policies SET legacy_preset_id='before',failure_threshold=1,success_threshold=1,cooldown_seconds=30 WHERE id='policy-1';`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{RuleVersionLegacy, RuleVersionV2} {
		if _, err := r.SwitchWorkspaceRule(ctx, "user1", "ws1", version); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := r.GetPolicy(ctx, p.ID, "user1", "ws1")
	if err != nil || loaded == nil || loaded.RulePresetID != "before" || loaded.RuleVersion != RuleVersionV2 || loaded.RulePreset.FailureThreshold != 1 || loaded.FailureThreshold != 1 {
		t.Fatalf("legacy→v2 did not preserve snapshot: %+v %v", loaded, err)
	}
	id := "sub2api:ws1:acc-1"
	peer := "sub2api:ws1:acc-2"
	for _, target := range []string{id, peer} {
		if err := r.ReplacePolicyAssignments(ctx, "user1", "ws1", target, []string{p.ID}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-2 * time.Minute)
	ok := string(ResultOK)
	for _, target := range []string{id, peer} {
		state := ConnectionHealthState{ConnectionID: target, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", RuleVersion: RuleVersionV2, State: StateHealthy, CurrentWeight: 100, CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), LastProbeAt: &now, LastProbeProtocol: protocolPointer(TestProtocolChatCompletions), LastAppliedProbeAt: &now, LastAppliedProbeResult: &ok, LastAppliedProbeProtocol: protocolPointer(TestProtocolChatCompletions), LastSuccessAt: &now, LastSuccessProtocol: protocolPointer(TestProtocolChatCompletions)}
		if err := r.UpsertState(ctx, state); err != nil {
			t.Fatal(err)
		}
	}
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Status: "active", Models: "gpt-4o", Schedulable: boolPointer(true)}, {ID: "acc-2", Status: "active", Models: "gpt-4o", Schedulable: boolPointer(true)}}}, credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: "https://probe.invalid", Key: "fixture"}}}
	s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	s.repo = r
	s.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
	s.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"fixture failure"}}`))}, nil
	})}}
	policies, err := r.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := r.ListAllPolicyAssignments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jobs := s.collectAdminProbeJobs(ctx, policies, assignments)
	found := false
	for _, job := range jobs {
		if job.target.TargetID == id {
			job.floorGuard = newWorkspaceFloorGuard()
			runProtocolServiceJob(s, job)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("automatic job missing after actual legacy→v2 switches")
	}

	state, err := r.GetState(ctx, id, "gpt-4o")
	if err != nil || state == nil {
		t.Fatal(err)
	}
	if state.State != StateSuspect || state.CurrentWeight != 100 || !state.RecheckPending || state.ConsecutiveFailures != 1 || state.CooldownUntil != nil || len(platform.sub2APICalls) != 0 {
		t.Fatalf("threshold1 after real rule switches bypassed suspect: state=%+v remoteCalls=%+v", state, platform.sub2APICalls)
	}
	var actions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_target_action_states WHERE target_id=$1`, id).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if actions != 0 {
		t.Fatalf("first suspect claimed remote action checkpoint: %d", actions)
	}
}

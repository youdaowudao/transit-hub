package connection_health

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestRecommendedPresetLongFailureIntervalIsDaily(t *testing.T) {
	if p := recommendedRulePreset(); p.LongFailureIntervalSeconds != 86400 {
		t.Fatalf("recommended retry seconds=%d want=86400", p.LongFailureIntervalSeconds)
	}
	if p := DefaultRulePreset(); p.LongFailureIntervalSeconds != 3600 {
		t.Fatalf("legacy default changed to %d", p.LongFailureIntervalSeconds)
	}
}
func TestLongFailureBlockedReason(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	failing := now.Add(-25 * time.Hour)
	last := now.Add(-time.Hour)
	policy := Policy{ID: "long", RuleVersion: RuleVersionV2}
	preset := DefaultRulePreset()
	preset.LongFailureIntervalSeconds = 86400
	policy.RulePreset = &preset
	next, reason := applyLongFailureCadence(&ConnectionHealthState{State: StateSuspended, FailingSince: &failing, LastProbeAt: &last}, policy, now, now, ProbeBlockedFailureBackoff)
	if reason != "long_failure" || !next.Equal(last.Add(24*time.Hour)) {
		t.Fatalf("long retry next=%s reason=%q", next, reason)
	}
	recent := now.Add(-time.Hour)
	_, reason = applyLongFailureCadence(&ConnectionHealthState{State: StateSuspended, FailingSince: &recent, LastProbeAt: &last}, policy, now, now, ProbeBlockedFailureBackoff)
	if reason != ProbeBlockedFailureBackoff {
		t.Fatalf("ordinary backoff changed: %q", reason)
	}
}

func TestLongFailureViewUsesJudgmentPreset(t *testing.T) {
	for _, kind := range []string{"suspended", "disabled", "changed-protocol", "newapi", "no-state", "config-conflict"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			failing := now.Add(-25 * time.Hour)
			last := now.Add(-time.Hour)
			protocol := TestProtocolChatCompletions
			platform := upstream.PlatformSub2API
			if kind == "newapi" {
				platform = upstream.PlatformNewAPI
			}
			targetID := buildTargetID(string(platform), "ws1", "acc-1")
			repo := newFakeRepository()
			state := ConnectionHealthState{ConnectionID: targetID, UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", State: StateSuspended, RuleVersion: RuleVersionV2, FailingSince: &failing, LastProbeAt: &last, LastFailureAt: &last, ConsecutiveFailures: 5, CounterProtocol: &protocol, LastAppliedProbeProtocol: &protocol, LastProbeProtocol: &protocol}
			if kind == "disabled" {
				state.State = StateDisabled
			}
			if kind != "no-state" {
				repo.states[targetID] = map[string]ConnectionHealthState{"gpt-4o": state}
			}
			judgment := schedulableProbePolicy("judgment", true, 300)
			judgment.RuleVersion = RuleVersionV2
			judgment.AutoDegradeEnabled = true
			judgment.AutoRemoteActionEnabled = false
			judgment.ProbeIntervalSeconds = 300
			judgmentPreset := recommendedRulePreset()
			judgmentPreset.LongFailureAfterSeconds = 7200
			judgmentPreset.LongFailureIntervalSeconds = 43200
			judgment.RulePreset = &judgmentPreset
			budget := judgment
			budget.ID = "budget"
			budget.ProbeIntervalSeconds = 60
			budget.AutoRemoteActionEnabled = true
			budgetPreset := judgmentPreset
			budgetPreset.LongFailureAfterSeconds = 14400
			budgetPreset.LongFailureIntervalSeconds = 64800
			budget.RulePreset = &budgetPreset
			repo.policies = []Policy{budget, judgment}
			selected := candidateModelSpecs(nil, repo.policies, "", probeModelSource{}, true)
			if len(selected) != 1 || selected[0].policy.ID != judgment.ID {
				t.Fatal("fixture must select the non-remote-action judgment policy independently of the budget owner")
			}
			repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: budget.ID}, {UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: judgment.ID}}
			account := upstream.AdminGroupAccountInfo{ID: "acc-1", Name: "隔离长期失败账号", Status: "active", Models: "gpt-4o", BaseURL: "https://fixture.invalid", Schedulable: boolPointer(true)}
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "fixture"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}}}
			if kind == "changed-protocol" || kind == "config-conflict" {
				repo.testConfigurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}}
			}
			if kind == "config-conflict" {
				reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g2"})
				reader.accountsByGrp["g2"] = []upstream.AdminGroupAccountInfo{account}
				repo.testConfigurations = append(repo.testConfigurations, GroupTestConfig{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10})
			}
			s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: platform}}, repo)
			groups, err := s.AdminGroups(context.Background(), "user1")
			if err != nil {
				t.Fatal(err)
			}
			if len(groups) == 0 || len(groups[0].Accounts) == 0 {
				t.Fatalf("no account response: %+v", groups)
			}
			models := groups[0].Accounts[0].ModelHealth
			if kind == "no-state" {
				if len(models) != 0 {
					t.Fatalf("invented health for unprobed model: %+v", models)
				}
				return
			}
			if len(models) != 1 {
				t.Fatalf("model response count=%d", len(models))
			}
			raw, _ := json.Marshal(models[0])
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			positive := kind == "suspended" || kind == "config-conflict"
			if positive {
				if models[0].BudgetPolicyID != budget.ID {
					t.Fatalf("fixture did not retain the separate budget owner: %q", models[0].BudgetPolicyID)
				}
				retry := "continuing"
				if kind == "config-conflict" {
					retry = "config_blocked"
				}
				if fields["longFailure"] != true || fields["longFailureRetry"] != retry || fields["longFailureAfterSeconds"] != float64(7200) || fields["longFailureIntervalSeconds"] != float64(43200) {
					t.Fatalf("response must use judgment preset rather than budget preset: %s", raw)
				}
			} else if fields["longFailure"] == true || fields["longFailureRetry"] != nil || fields["longFailureAfterSeconds"] != nil || fields["longFailureIntervalSeconds"] != nil {
				t.Fatalf("ineligible model advertised long-failure retry: %s", raw)
			}
		})
	}
}

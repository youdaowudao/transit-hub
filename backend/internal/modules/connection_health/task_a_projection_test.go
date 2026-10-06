package connection_health

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// Use the public read paths, including a target shared by two groups, so the
// summaries cannot be made correct only in a standalone state counter.
func taskAProjectionService() (*Service, time.Time) {
	repo := newFakeRepository()
	last := time.Now().UTC().Add(-20 * time.Second)
	preset := DefaultRulePreset()
	preset.FailedRetryIntervalSeconds = 420
	preset.LongFailureAfterSeconds = 3600
	preset.LongFailureIntervalSeconds = 1200
	policy := Policy{ID: "p", UserID: "user1", AdminAccountID: "ws1", Enabled: true, RuleVersion: RuleVersionV2, RulePreset: &preset, ProbeIntervalSeconds: 600, FailureThreshold: 9, CooldownSeconds: 9999}
	targetID := "sub2api:ws1:1"
	repo.states[targetID] = map[string]ConnectionHealthState{}
	for _, name := range []string{"healthy", "suspect", "suspended", "long", "unverified", "other_protocol"} {
		policy.ModelTargets = append(policy.ModelTargets, ModelTarget{ModelName: name, Enabled: true})
		state := ConnectionHealthState{ConnectionID: targetID, UserID: "user1", AdminAccountID: "ws1", ModelName: name, RuleVersion: RuleVersionV2, State: StateHealthy, CurrentWeight: 100, LastProbeAt: &last, LastAppliedProbeAt: &last, LastProbeProtocol: protocolPointer(TestProtocolChatCompletions), LastAppliedProbeProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), UpdatedAt: last}
		success := string(ResultOK)
		failure := string(ResultNetworkFluctuation)
		if name == "healthy" {
			state.LastSuccessAt, state.LastSuccessProtocol = &last, protocolPointer(TestProtocolChatCompletions)
			state.LastAppliedProbeResult = &success
			state.LastSuccessLatencyMs, state.LastFirstTokenMs, state.LastFirstEventMs = intPointer(9000), intPointer(6500), intPointer(6000)
		} else {
			state.State, state.RecheckPending = StateSuspect, true
			state.ConsecutiveFailures = 1
			state.LastAppliedProbeResult, state.LastFailureAt, state.FailingSince = &failure, &last, &last
			if name == "suspended" || name == "long" {
				state.State, state.CurrentWeight, state.RecheckPending = StateSuspended, 0, false
				state.ConsecutiveFailures = 3
			}
			if name == "long" {
				old := last.Add(-2 * time.Hour)
				state.FailingSince = &old
			}
			if name == "unverified" {
				state.HealthEvidenceStatus, state.HealthEvidenceProtocol = HealthEvidenceInvalid, nil
			}
			if name == "other_protocol" {
				state.HealthEvidenceProtocol = protocolPointer(TestProtocolResponses)
			}
		}
		repo.states[targetID][name] = state
	}
	repo.policies = []Policy{policy}
	repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: "p"}, {UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", PolicyID: "p"}}
	account := upstream.AdminGroupAccountInfo{ID: "1", BaseURL: "https://probe.invalid", Status: "active", Schedulable: boolPointer(true)}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}, "g2": {account}}}
	return newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo), last
}

func taskAJSONCount(t *testing.T, value any, field string, want int) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result[field] != float64(want) {
		t.Errorf("API field %s=%v, want %d; response=%s", field, result[field], want, data)
	}
}

func TestTaskAAdminGroupAPISuspectSummaryAndPresetProjection(t *testing.T) {
	service, last := taskAProjectionService()
	readStarted := time.Now().UTC()
	groups, err := service.AdminGroups(context.Background(), "user1")
	readEnded := time.Now().UTC()
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	for _, group := range groups {
		taskAJSONCount(t, group.HealthSummary, "suspectModels", 1)
		if group.HealthSummary.HealthyModels != 2 || group.HealthSummary.SuspendedModels != 2 || group.HealthSummary.UnconfiguredModels != 2 {
			t.Errorf("suspect stays in healthy band; unverified/other-protocol models must not count as suspect: %+v", group.HealthSummary)
		}
		models := map[string]ModelHealth{}
		for _, model := range group.Accounts[0].ModelHealth {
			models[model.ModelName] = model
		}
		pending := models["suspect"]
		if pending.State != StateSuspect || !pending.RecheckPending || pending.RuleVersion != RuleVersionV2 || pending.NextProbeAt == nil || pending.NextProbeAt.Before(readStarted) || pending.NextProbeAt.After(readEnded) || pending.EffectiveIntervalSeconds != 600 {
			t.Errorf("pending v2 recheck must be due at read time while preserving normal policy interval: %+v", pending)
		}
		for name, seconds := range map[string]int{"healthy": 600, "suspended": 420, "long": 1200} {
			model := models[name]
			if model.NextProbeAt == nil || !model.NextProbeAt.Equal(last.Add(time.Duration(seconds)*time.Second)) {
				t.Errorf("%s projected cadence must use selected preset and policy, got=%v want=%v", name, model.NextProbeAt, last.Add(time.Duration(seconds)*time.Second))
			}
		}
		healthy := models["healthy"]
		if healthy.FirstTokenMs == nil || *healthy.FirstTokenMs != 6500 || healthy.FirstEventMs == nil || *healthy.FirstEventMs != 6000 {
			t.Errorf("current successful metrics missing from model projection: %+v", healthy)
		}
	}
}

func TestTaskASuspectSummaryExcludesUnverifiedAndCredentialFailures(t *testing.T) {
	summary := AdminGroupHealthSummary{}
	accumulateSummary(&summary, []ModelHealth{
		{State: StateSuspect, Configured: true, CurrentHealthResult: &CurrentHealthResult{Status: "failure"}},
		{State: StateSuspect, Configured: true, CurrentHealthResult: &CurrentHealthResult{Status: "unverified"}},
		{State: StateSuspect, Configured: false, CredentialUnavailableReason: upstream.ReasonCredentialUnavailable},
	})
	taskAJSONCount(t, summary, "suspectModels", 1)
	if summary.HealthyModels != 1 || summary.UnconfiguredModels != 2 || summary.DegradedModels != 0 || summary.SuspendedModels != 0 {
		t.Fatalf("non-health diagnostics must not create suspect/healthy counters: %+v", summary)
	}
}

func TestTaskAOverviewAPISuspectSubsetDeduplicatesSharedTarget(t *testing.T) {
	service, _ := taskAProjectionService()
	view, err := service.Overview(context.Background(), "user1")
	if err != nil {
		t.Fatal(err)
	}
	taskAJSONCount(t, view, "suspect", 1)
	if view.TotalConnections != 1 || view.Healthy != 2 || view.Suspended != 2 || view.Unconfigured != 2 || view.Degraded != 0 {
		t.Fatalf("shared models must be counted once; suspect is a healthy subset: %+v", view)
	}
}

func TestTaskALegacyGroupsAndOverviewPreserveSuspectProjection(t *testing.T) {
	repo := newFakeRepository()
	repo.states["conn"] = map[string]ConnectionHealthState{"model": {ConnectionID: "conn", UserID: "user1", AdminAccountID: "ws1", ModelName: "model", State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100, RecheckPending: true}}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}, mySites: fakeMySitesReader{connections: []my_sites.RealConnection{{ID: "conn", OwnGroupIDs: []string{"g"}}}, ownGroups: []my_sites.MappingOwnGroupOption{{ID: "g"}}}}
	groups, err := service.Groups(context.Background(), "user1")
	if err != nil || len(groups) != 1 || len(groups[0].Connections) != 1 || len(groups[0].Connections[0].Models) != 1 {
		t.Fatalf("legacy groups=%+v err=%v", groups, err)
	}
	model := groups[0].Connections[0].Models[0]
	if model.State != StateSuspect || !model.RecheckPending || model.RuleVersion != RuleVersionV2 || model.CurrentWeight != 100 {
		t.Fatalf("legacy group view must preserve current state projection: %+v", model)
	}
	view, err := service.Overview(context.Background(), "user1")
	if err != nil {
		t.Fatal(err)
	}
	taskAJSONCount(t, view, "suspect", 1)
	if view.Healthy != 1 || view.Degraded != 0 || view.Suspended != 0 {
		t.Fatalf("legacy overview must preserve healthy-band semantics: %+v", view)
	}
}

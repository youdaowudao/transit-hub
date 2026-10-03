package connection_health

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestActionCheckpointManagedPriorityRetainedWhenProtocolEvidenceUnknown(t *testing.T) {
	for _, scenario := range []string{"conflict", "unavailable", "invalid", "partial_models", "missing_model"} {
		t.Run(scenario, func(t *testing.T) {
			repo := newFakeRepository()
			actions := &fakeTargetPriorityActioner{}
			service := &Service{repo: repo, priorityActions: actions}
			policy := probePolicy()
			policy.PriorityMode = PriorityModeMultiplier
			policy.StrategyMode = StrategyModeHealthProbe
			policy.AutoDegradeEnabled = true
			policy.ModelTargets = append(policy.ModelTargets, ModelTarget{ID: "t2", PolicyID: policy.ID, ModelName: "gpt-4.1", ProviderFamily: ProviderOpenAI, Enabled: true, MaxProbeTokens: 1})
			targetID := "sub2api:ws1:100"
			stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalPriority: 7, LastAppliedPriority: 50, EffectiveMultiplier: 0.1}
			repo.priorityStates["user1|ws1|"+targetID] = stored
			multiplier := 0.1
			item := &priorityTargetInventory{
				target:   AdminProbeTarget{TargetID: targetID, AccountID: "100", Platform: string(upstream.PlatformSub2API), Models: []string{"gpt-4o", "gpt-4.1"}, TestMemberships: []TestConfigurationSource{{AdminGroupID: "g1"}, {AdminGroupID: "g2"}}},
				policies: []Policy{policy}, currentPriority: 50, priorityPresent: true, snapshotStartedAt: time.Now(),
				upstreamMultiplier: upstreamMultiplierResolution{status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: &multiplier}},
			}
			states := []ConnectionHealthState{
				{ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy, HealthEvidenceStatus: HealthEvidenceLegacy},
				{ConnectionID: targetID, ModelName: "gpt-4.1", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy, HealthEvidenceStatus: HealthEvidenceLegacy},
			}
			switch scenario {
			case "conflict":
				repo.testConfigurations = []GroupTestConfig{
					{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10},
					{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10},
				}
			case "unavailable":
				repo.testConfigurationErr = errors.New("configuration read unavailable")
			case "invalid":
				states[0].HealthEvidenceStatus, states[1].HealthEvidenceStatus = HealthEvidenceInvalid, HealthEvidenceInvalid
			case "partial_models":
				states[1].HealthEvidenceStatus = HealthEvidenceInvalid
			case "missing_model":
				states = states[:1]
			}
			repo.states[targetID] = make(map[string]ConnectionHealthState)
			for _, state := range states {
				repo.states[targetID][state.ModelName] = state
			}

			service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", map[string]*priorityTargetInventory{targetID: item}, true, states, []PrioritySyncState{stored})

			if len(actions.calls) != 0 {
				t.Fatalf("unknown evidence must neither assign health Priority nor restore original Priority: %+v", actions.calls)
			}
			if got, exists := repo.priorityStates["user1|ws1|"+targetID]; !exists || !reflect.DeepEqual(got, stored) {
				t.Fatalf("still-managed target lost its original checkpoint: exists=%v got=%+v want=%+v", exists, got, stored)
			}
			if got := len(activeHealthPriorityModels(item)); got != 2 {
				t.Fatalf("expected model set shrank to available evidence: got=%d", got)
			}
		})
	}
}

func TestActionCheckpointMultiplierOnlyStillWritesWithProtocolConflict(t *testing.T) {
	repo := newFakeRepository()
	repo.testConfigurations = []GroupTestConfig{
		{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10},
		{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10},
	}
	actions := &fakeTargetPriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions}
	policy := probePolicy()
	policy.PriorityMode, policy.StrategyMode = PriorityModeMultiplier, StrategyModeMultiplierOnly
	targetID := "sub2api:ws1:100"
	item := &priorityTargetInventory{
		target:   AdminProbeTarget{TargetID: targetID, AccountID: "100", Platform: string(upstream.PlatformSub2API), TestMemberships: []TestConfigurationSource{{AdminGroupID: "g1"}, {AdminGroupID: "g2"}}},
		policies: []Policy{policy}, currentPriority: 50, priorityPresent: true, snapshotStartedAt: time.Now(), multipliers: []float64{0.1},
	}
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", map[string]*priorityTargetInventory{targetID: item}, true, nil, nil)
	if item.target.TestConfiguration.Status != "conflict" || len(actions.calls) != 1 || actions.calls[0].targetID != "100" || actions.calls[0].priority != 1 {
		t.Fatalf("multiplier-only write unexpectedly depended on protocol evidence: config=%+v calls=%+v", item.target.TestConfiguration, actions.calls)
	}
	stored := repo.priorityStates["user1|ws1|"+targetID]
	if stored.PendingPriority == nil || *stored.PendingPriority != 1 || stored.PendingDispatchPhase != DispatchConfirmedApplied {
		t.Fatalf("multiplier-only write bypassed shared action checkpoint: %+v", stored)
	}
}

func TestActionCheckpointQueuedHealthPolicyWithoutEvidenceDoesNotWrite(t *testing.T) {
	repo := newFakeRepository()
	policy := probePolicy()
	policy.PriorityMode, policy.StrategyMode = PriorityModeMultiplier, StrategyModeHealthProbe
	policy.AutoDegradeEnabled = false
	repo.policies = []Policy{policy}
	currentPriority := 11
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "100", Name: "cheap", Priority: &currentPriority, Models: "gpt-4o"}}},
	}
	actions := &fakeTargetPriorityActioner{}
	service := newAdminGroupsService(reader, fakeAdminGroupKeyReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}, repo)
	service.sites, service.priorityActions = fakeSiteLookup{}, actions
	fallback := 0.08
	configuration, err := service.SetAdminGroupPolicyConfiguration(context.Background(), "user1", "g1", AdminGroupPolicyConfigurationInput{PolicyIDs: []string{policy.ID}, ProbeSortFallbackMultiplier: &fallback})
	if err != nil || configuration.PrioritySyncStatus != "pending" {
		t.Fatalf("save must queue synchronization: configuration=%+v err=%v", configuration, err)
	}
	waitForPriorityAsyncIdle(t)
	if len(actions.calls) != 0 || len(repo.priorityStates) != 0 {
		t.Fatalf("health policy without usable evidence wrote Priority: calls=%+v checkpoints=%+v", actions.calls, repo.priorityStates)
	}
}

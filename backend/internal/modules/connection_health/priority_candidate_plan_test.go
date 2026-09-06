package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func candidateFloat64Pointer(value float64) *float64 {
	return &value
}

func candidateAccount(targetID string, tier int, state State, multiplier float64, latency int, priority int) (AdminGroupAccount, upstream.AdminGroupAccountInfo, PrioritySyncState) {
	schedulable := true
	account := AdminGroupAccount{
		ID: targetID, Name: targetID, Status: "active", Schedulable: &schedulable,
		Priority: intPointer(priority), TargetID: targetID, AccountTier: tier,
		ModelHealth: []ModelHealth{{
			ModelName: "gpt-test", Configured: true, State: state,
			LastSuccessLatencyMs: intPointer(latency),
		}},
		EffectivePolicies: []AssignedPolicySummary{{
			PolicyID: "health", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeHealthProbe,
		}},
		HasEnabledProbePolicy: true, ProbeModelsConfigured: true,
		PriorityManaged: true, EffectiveMultiplier: candidateFloat64Pointer(multiplier),
		MultiplierResolutionStatus: MultiplierResolutionResolved, MultiplierSource: MultiplierSourceUpstreamKey,
	}
	observation := upstream.AdminGroupAccountInfo{
		ID: targetID, Name: targetID, Status: "active", Schedulable: &schedulable, Priority: intPointer(priority),
	}
	syncState := PrioritySyncState{TargetID: targetID, LastAppliedPriority: priority, EffectiveMultiplier: multiplier}
	return account, observation, syncState
}

func candidatePlan(
	accounts []AdminGroupAccount,
	states []PrioritySyncState,
	observations map[string][]upstream.AdminGroupAccountInfo,
	inventoryComplete bool,
) (PriorityCandidateSummary, map[string]PriorityCandidateProjection) {
	return candidatePlanForGroups(
		[]AdminGroupHealth{{ID: "g1", Accounts: accounts}},
		states,
		observations,
		inventoryComplete,
	)
}

func candidatePlanForGroups(
	groups []AdminGroupHealth,
	states []PrioritySyncState,
	observations map[string][]upstream.AdminGroupAccountInfo,
	inventoryComplete bool,
) (PriorityCandidateSummary, map[string]PriorityCandidateProjection) {
	evidence := make(map[string]priorityCandidateSortEvidence)
	for _, group := range groups {
		for _, account := range group.Accounts {
			if _, exists := evidence[account.TargetID]; exists {
				continue
			}
			activeModels := make(map[string]struct{})
			activeStates := make([]ConnectionHealthState, 0, len(account.ModelHealth))
			for _, model := range account.ModelHealth {
				activeModels[model.ModelName] = struct{}{}
				activeStates = append(activeStates, ConnectionHealthState{
					ConnectionID:         account.TargetID,
					ModelName:            model.ModelName,
					State:                model.State,
					LastSuccessLatencyMs: cloneIntPointer(model.LastSuccessLatencyMs),
				})
			}
			for _, model := range account.UnprobedModels {
				activeModels[model.ModelName] = struct{}{}
			}
			multiplier := 0.0
			multiplierAvailable := false
			if account.MultiplierResolutionStatus == MultiplierResolutionResolved && account.EffectiveMultiplier != nil {
				multiplier = *account.EffectiveMultiplier
				multiplierAvailable = true
			} else if (account.MultiplierResolutionStatus == MultiplierResolutionUnassociated || account.MultiplierResolutionStatus == MultiplierResolutionConflict) && account.LocalFallbackMultiplier != nil {
				multiplier = *account.LocalFallbackMultiplier
				multiplierAvailable = true
			}
			evidence[account.TargetID] = priorityCandidateSortEvidence{
				activeModels: activeModels, states: activeStates,
				healthBand:       priorityHealthBand(activeStates, len(activeModels)),
				successLatencyMs: completeTargetSuccessLatency(activeStates, activeModels),
				multiplier:       multiplier, multiplierAvailable: multiplierAvailable,
			}
		}
	}
	return buildSub2APIPriorityCandidatePlan(groups, states, observations, inventoryComplete, evidence)
}

func candidatePointersByTarget(candidates map[string]PriorityCandidateProjection, targetIDs ...string) []*int {
	result := make([]*int, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		result = append(result, candidates[targetID].Priority)
	}
	return result
}

// Regression mutation guarded: sorting multiplier before health or tier would let a cheap standby target outrank primary health.
func TestBuildSub2APIPriorityCandidatePlanUsesFrozenStrictOrderingAndDisjointRegions(t *testing.T) {
	type fixture struct {
		targetID   string
		tier       int
		state      State
		multiplier float64
		latency    int
	}
	fixtures := []fixture{
		{targetID: "t1-healthy-expensive", tier: 1, state: StateHealthy, multiplier: 9, latency: 900},
		{targetID: "t1-recovering-cheap", tier: 1, state: StateRecovering, multiplier: 0.1, latency: 1},
		{targetID: "t1-degraded-cheap", tier: 1, state: StateDegraded, multiplier: 0.01, latency: 1},
		{targetID: "t1-z-multiplier", tier: 1, state: StateHealthy, multiplier: 1, latency: 50},
		{targetID: "t1-y-latency", tier: 1, state: StateHealthy, multiplier: 1, latency: 20},
		{targetID: "t1-x-target", tier: 1, state: StateHealthy, multiplier: 1, latency: 20},
		{targetID: "t1-low-multiplier", tier: 1, state: StateHealthy, multiplier: 0.5, latency: 990},
		{targetID: "t2-perfect", tier: 2, state: StateHealthy, multiplier: 0.001, latency: 1},
	}
	accounts := make([]AdminGroupAccount, 0, len(fixtures))
	states := make([]PrioritySyncState, 0, len(fixtures))
	observations := make(map[string][]upstream.AdminGroupAccountInfo, len(fixtures))
	for index, fixture := range fixtures {
		account, observation, state := candidateAccount(fixture.targetID, fixture.tier, fixture.state, fixture.multiplier, fixture.latency, 20+index)
		accounts = append(accounts, account)
		states = append(states, state)
		observations[fixture.targetID] = []upstream.AdminGroupAccountInfo{observation}
	}

	summary, candidates := candidatePlan(accounts, states, observations, true)
	if summary.Mode != "first_active" || !summary.CandidatePriorityReady || summary.CandidateCount != len(fixtures) {
		t.Fatalf("unexpected first-layer plan summary: %+v", summary)
	}
	wantOrder := []string{
		"t1-low-multiplier", "t1-x-target", "t1-y-latency", "t1-z-multiplier", "t1-healthy-expensive",
		"t1-recovering-cheap", "t1-degraded-cheap", "t2-perfect",
	}
	seenPriorities := make(map[int]string)
	for index, targetID := range wantOrder {
		candidate := candidates[targetID]
		if candidate.Rank == nil || *candidate.Rank != index+1 || candidate.Priority == nil {
			t.Fatalf("candidate %s = %+v, want rank=%d with priority", targetID, candidate, index+1)
		}
		if previous, duplicate := seenPriorities[*candidate.Priority]; duplicate {
			t.Fatalf("targets %s and %s share candidate Priority %d", previous, targetID, *candidate.Priority)
		}
		seenPriorities[*candidate.Priority] = targetID
	}
	if got := *candidates["t1-low-multiplier"].Priority; got != 10 {
		t.Fatalf("first healthy normal priority=%d want 10", got)
	}
	if got := *candidates["t1-recovering-cheap"].Priority; got != 100 {
		t.Fatalf("first recovering normal priority=%d want 100", got)
	}
	if got := *candidates["t1-degraded-cheap"].Priority; got != 1000 {
		t.Fatalf("first degraded normal priority=%d want 1000", got)
	}
	if got := candidates["t2-perfect"]; got.Region != "hot_standby" || got.Priority == nil || *got.Priority != 10000 {
		t.Fatalf("second layer must remain behind every first-layer health band: %+v", got)
	}
	encoded, err := json.Marshal(candidates["t1-low-multiplier"])
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if got, ok := projected["multiplier"].(float64); !ok || got != 0.5 {
		t.Fatalf("candidate projection multiplier=%v want the actual sort multiplier 0.5", projected["multiplier"])
	}
}

// Regression mutation guarded: treating unknown first-layer evidence as unavailable would falsely announce second-layer takeover.
func TestBuildSub2APIPriorityCandidatePlanSwitchesOnlyAfterExplicitUnavailabilityAndReturnsToStandby(t *testing.T) {
	first, firstObservation, firstState := candidateAccount("first", 1, StateDisabled, 1, 10, 20)
	firstObservation.Status = "inactive"
	firstObservation.Schedulable = boolPointer(false)
	second, secondObservation, secondState := candidateAccount("second", 2, StateHealthy, 0.1, 5, 21)
	groups := []AdminGroupHealth{{ID: "g1", Accounts: []AdminGroupAccount{first, second}}}
	states := []PrioritySyncState{firstState, secondState}
	observations := map[string][]upstream.AdminGroupAccountInfo{"first": {firstObservation}, "second": {secondObservation}}

	summary, candidates := candidatePlanForGroups(groups, states, observations, true)
	if summary.Mode != "second_active" || !summary.CandidatePriorityReady {
		t.Fatalf("explicitly unavailable first layer must promote second layer: %+v", summary)
	}
	if got := candidates["second"]; got.Region != "normal" || got.Priority == nil || *got.Priority != 10 {
		t.Fatalf("promoted second-layer candidate=%+v want normal priority 10", got)
	}
	if got := candidates["first"]; got.State != "unavailable" || got.Priority != nil {
		t.Fatalf("unavailable first-layer target must remain outside candidate priorities: %+v", got)
	}

	first.ModelHealth = nil
	first.UnprobedModels = []AdminGroupUnprobedModel{{ModelName: "gpt-test"}}
	firstObservation.Status = "active"
	firstObservation.Schedulable = boolPointer(true)
	groups[0].Accounts[0] = first
	observations["first"] = []upstream.AdminGroupAccountInfo{firstObservation}
	summary, candidates = candidatePlanForGroups(groups, states, observations, true)
	if summary.Mode != "safety_lock" || summary.CandidatePriorityReady || summary.SafetyReason != "health_incomplete" {
		t.Fatalf("unknown first layer must safety lock: %+v", summary)
	}
	if candidates["second"].Priority != nil {
		t.Fatalf("safety lock must not emit a writable second-layer priority: %+v", candidates["second"])
	}

	first, firstObservation, firstState = candidateAccount("first", 1, StateHealthy, 2, 30, 20)
	groups[0].Accounts[0] = first
	states[0] = firstState
	observations["first"] = []upstream.AdminGroupAccountInfo{firstObservation}
	summary, candidates = candidatePlanForGroups(groups, states, observations, true)
	if summary.Mode != "first_active" || !summary.CandidatePriorityReady {
		t.Fatalf("recovered first layer must become active again: %+v", summary)
	}
	if got := candidates["second"]; got.Region != "hot_standby" || got.Priority == nil || *got.Priority != 10000 {
		t.Fatalf("second layer must return to hot standby after recovery: %+v", got)
	}
}

// Regression mutation guarded: evaluating each group projection alone would duplicate targets or drop a still-effective policy source.
func TestBuildSub2APIPriorityCandidatePlanMergesEffectiveSourcesAndDeduplicatesTargets(t *testing.T) {
	direct, directObservation, directState := candidateAccount("direct", 1, StateHealthy, 1, 10, 20)
	direct.ExcludedFromGroupPolicy = true
	singleExcluded, singleObservation, singleState := candidateAccount("single-excluded", 2, StateHealthy, 1, 10, 21)
	singleExcluded.EffectivePolicies = nil
	singleExcluded.HasEnabledProbePolicy = false
	partialEmpty, partialObservation, partialState := candidateAccount("partial", 2, StateHealthy, 1, 10, 22)
	partialEmpty.EffectivePolicies = nil
	partialEmpty.HasEnabledProbePolicy = false
	partialEmpty.ExcludedFromGroupPolicy = true
	partialEffective := partialEmpty
	partialEffective.EffectivePolicies = direct.EffectivePolicies
	partialEffective.HasEnabledProbePolicy = true
	mixedHealth, mixedObservation, mixedState := candidateAccount("mixed", 2, StateHealthy, 1, 10, 5)
	mixedLegacy := mixedHealth
	mixedLegacy.EffectivePolicies = []AssignedPolicySummary{{
		PolicyID: "legacy", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly,
	}}
	sameIDHealth, sameIDObservation, sameIDState := candidateAccount("same-id-mixed", 2, StateHealthy, 1, 10, 23)
	sameIDLegacy := sameIDHealth
	sameIDLegacy.EffectivePolicies = []AssignedPolicySummary{{
		PolicyID: "health", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly,
	}}
	unmanaged, unmanagedObservation, _ := candidateAccount("unmanaged", 2, StateHealthy, 1, 10, 30)
	unmanaged.PriorityManaged = false

	groups := []AdminGroupHealth{
		{ID: "g1", Accounts: []AdminGroupAccount{direct, singleExcluded, partialEmpty, mixedHealth, sameIDLegacy, unmanaged}},
		{ID: "g2", Accounts: []AdminGroupAccount{partialEffective, mixedLegacy, sameIDHealth}},
	}
	states := []PrioritySyncState{directState, singleState, partialState, mixedState, sameIDState}
	observations := map[string][]upstream.AdminGroupAccountInfo{
		"direct":          {directObservation},
		"single-excluded": {singleObservation},
		"partial":         {partialObservation, partialObservation},
		"mixed":           {mixedObservation, mixedObservation},
		"same-id-mixed":   {sameIDObservation, sameIDObservation},
		"unmanaged":       {unmanagedObservation},
	}

	summary, candidates := candidatePlanForGroups(groups, states, observations, true)
	if summary.CandidateCount != 2 {
		t.Fatalf("only direct and partially inherited targets should be candidates: %+v", summary)
	}
	if candidates["direct"].State != "candidate" || candidates["partial"].State != "candidate" {
		t.Fatalf("direct assignment and remaining group inheritance must survive exclusion: %+v", candidates)
	}
	if candidates["partial"].Rank == nil {
		t.Fatal("deduplicated partial target must have one global rank")
	}
	if got := candidates["single-excluded"]; got.State != "out_of_scope" || got.Reason != "no_effective_health_policy" {
		t.Fatalf("single-group exclusion classification=%+v", got)
	}
	if got := candidates["mixed"]; got.State != "out_of_scope" || got.Reason != "multiplier_only" {
		t.Fatalf("any effective multiplier_only source must keep the whole target on the old path: %+v", got)
	}
	if got := candidates["same-id-mixed"]; got.State != "out_of_scope" || got.Reason != "multiplier_only" {
		t.Fatalf("a multiplier_only source must survive a same-ID health projection instead of being overwritten: %+v", got)
	}
	if got := candidates["unmanaged"]; got.State != "out_of_scope" || got.Reason != "unmanaged" {
		t.Fatalf("unmanaged target must not be captured by the new ranking: %+v", got)
	}
}

// Regression mutation guarded: trusting 1-9 without its checkpoint evidence would overwrite protected manual priorities.
func TestBuildSub2APIPriorityCandidatePlanClassifiesPriorityEvidenceWithoutMutatingIt(t *testing.T) {
	type fixture struct {
		name         string
		priority     int
		legacy       bool
		state        *PrioritySyncState
		wantState    string
		wantReason   string
		wantEvidence string
	}
	fixtures := []fixture{
		{name: "manual-unknown-1-9", priority: 7, wantState: "out_of_scope", wantReason: "protected_priority_1_9", wantEvidence: "unknown"},
		{name: "managed-unknown-1-9", priority: 7, state: &PrioritySyncState{LastAppliedPriority: 4}, wantState: "out_of_scope", wantReason: "protected_priority_1_9", wantEvidence: "unknown"},
		{name: "legacy-last-applied", priority: 4, legacy: true, state: &PrioritySyncState{LastAppliedPriority: 4}, wantState: "out_of_scope", wantReason: "multiplier_only", wantEvidence: "last_applied"},
		{name: "legacy-confirmed-pending", priority: 3, legacy: true, state: &PrioritySyncState{LastAppliedPriority: 4, PendingPriority: intPointer(3)}, wantState: "out_of_scope", wantReason: "multiplier_only", wantEvidence: "confirmed_pending"},
		{name: "stale-pending-current-last-applied", priority: 4, state: &PrioritySyncState{LastAppliedPriority: 4, PendingPriority: intPointer(3)}, wantState: "out_of_scope", wantReason: "stale_pending", wantEvidence: "stale_pending"},
		{name: "stale-pending", priority: 6, legacy: true, state: &PrioritySyncState{LastAppliedPriority: 4, PendingPriority: intPointer(3)}, wantState: "out_of_scope", wantReason: "stale_pending", wantEvidence: "stale_pending"},
		{name: "stale-pending-outside-reserved", priority: 20, state: &PrioritySyncState{LastAppliedPriority: 10, PendingPriority: intPointer(15)}, wantState: "out_of_scope", wantReason: "stale_pending", wantEvidence: "stale_pending"},
		{name: "checkpoint-mismatch", priority: 20, state: &PrioritySyncState{LastAppliedPriority: 10}, wantState: "out_of_scope", wantReason: "priority_unknown", wantEvidence: "unknown"},
		{name: "explicit-conflict", priority: 20, state: &PrioritySyncState{LastAppliedPriority: 10, Conflict: true}, wantState: "out_of_scope", wantReason: "priority_conflict", wantEvidence: "conflict"},
		{name: "health-last-applied", priority: 20, state: &PrioritySyncState{LastAppliedPriority: 20}, wantState: "candidate", wantEvidence: "last_applied"},
		{name: "unmanaged-outside-reserved", priority: 20, wantState: "out_of_scope", wantReason: "unmanaged", wantEvidence: "unknown"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			account, observation, _ := candidateAccount(fixture.name, 1, StateHealthy, 1, 10, fixture.priority)
			if fixture.legacy {
				account.EffectivePolicies = []AssignedPolicySummary{{PolicyID: "legacy", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly}}
				account.HasEnabledProbePolicy = false
			}
			account.PriorityManaged = fixture.state != nil
			states := []PrioritySyncState{}
			if fixture.state != nil {
				state := *fixture.state
				state.TargetID = fixture.name
				states = append(states, state)
			}
			_, candidates := candidatePlan(
				[]AdminGroupAccount{account}, states,
				map[string][]upstream.AdminGroupAccountInfo{fixture.name: {observation}}, true,
			)
			got := candidates[fixture.name]
			if got.State != fixture.wantState || got.Reason != fixture.wantReason || got.PriorityEvidence != fixture.wantEvidence {
				t.Fatalf("classification=%+v want state=%s reason=%s evidence=%s", got, fixture.wantState, fixture.wantReason, fixture.wantEvidence)
			}
		})
	}
}

// Regression mutation guarded: wrapping an overflowing region or sorting incomplete evidence would create duplicate or unsafe priorities.
func TestBuildSub2APIPriorityCandidatePlanFailsClosedOnCapacityAndEvidence(t *testing.T) {
	accounts := make([]AdminGroupAccount, 0, 91)
	states := make([]PrioritySyncState, 0, 91)
	observations := make(map[string][]upstream.AdminGroupAccountInfo, 91)
	for index := 0; index < 91; index++ {
		targetID := fmt.Sprintf("healthy-%03d", index)
		account, observation, state := candidateAccount(targetID, 1, StateHealthy, float64(index+1), index+1, 20+index)
		accounts = append(accounts, account)
		states = append(states, state)
		observations[targetID] = []upstream.AdminGroupAccountInfo{observation}
	}
	summary, candidates := candidatePlan(accounts, states, observations, true)
	if summary.Mode != "safety_lock" || summary.CandidatePriorityReady || summary.SafetyReason != "capacity_overflow" {
		t.Fatalf("overflow must close the whole writable plan: %+v", summary)
	}
	for targetID, priority := range candidatePointersByTarget(candidates, "healthy-000", "healthy-090") {
		if priority != nil {
			t.Fatalf("overflow target index=%d received priority=%d", targetID, *priority)
		}
	}
	var healthyNormal *PriorityCandidateCapacity
	for index := range summary.Capacities {
		capacity := &summary.Capacities[index]
		if capacity.Region == "normal" && capacity.HealthBand == "healthy" {
			healthyNormal = capacity
		}
	}
	if healthyNormal == nil || healthyNormal.Start != 10 || healthyNormal.End != 99 || healthyNormal.Capacity != 90 || healthyNormal.Actual != 91 || healthyNormal.Remaining != 0 || !healthyNormal.Overflow {
		t.Fatalf("healthy normal capacity must expose exact overflow: %+v", healthyNormal)
	}

	base, baseObservation, baseState := candidateAccount("first", 1, StateHealthy, 1, 10, 20)
	tests := []struct {
		name              string
		inventoryComplete bool
		mutate            func(*AdminGroupAccount, *[]upstream.AdminGroupAccountInfo)
		wantReason        string
	}{
		{name: "inventory incomplete", inventoryComplete: false, mutate: func(*AdminGroupAccount, *[]upstream.AdminGroupAccountInfo) {}, wantReason: "inventory_incomplete"},
		{name: "health incomplete", inventoryComplete: true, mutate: func(account *AdminGroupAccount, _ *[]upstream.AdminGroupAccountInfo) {
			account.ModelHealth = nil
			account.UnprobedModels = []AdminGroupUnprobedModel{{ModelName: "gpt-test"}}
		}, wantReason: "health_incomplete"},
		{name: "schedulable unknown", inventoryComplete: true, mutate: func(_ *AdminGroupAccount, rows *[]upstream.AdminGroupAccountInfo) { (*rows)[0].Schedulable = nil }, wantReason: "schedulable_unknown"},
		{name: "cross group conflict", inventoryComplete: true, mutate: func(_ *AdminGroupAccount, rows *[]upstream.AdminGroupAccountInfo) {
			copy := (*rows)[0]
			copy.Status = "inactive"
			*rows = append(*rows, copy)
		}, wantReason: "observation_conflict"},
		{name: "cross group priority conflict", inventoryComplete: true, mutate: func(_ *AdminGroupAccount, rows *[]upstream.AdminGroupAccountInfo) {
			copy := (*rows)[0]
			copy.Priority = intPointer(21)
			*rows = append(*rows, copy)
		}, wantReason: "priority_observation_conflict"},
		{name: "current priority missing", inventoryComplete: true, mutate: func(account *AdminGroupAccount, rows *[]upstream.AdminGroupAccountInfo) {
			account.Priority = nil
			(*rows)[0].Priority = nil
		}, wantReason: "priority_unknown"},
		{name: "multiplier unavailable", inventoryComplete: true, mutate: func(account *AdminGroupAccount, _ *[]upstream.AdminGroupAccountInfo) {
			account.EffectiveMultiplier = nil
			account.MultiplierSource = MultiplierSourceNone
		}, wantReason: "multiplier_unavailable"},
		{name: "stale multiplier with display value", inventoryComplete: true, mutate: func(account *AdminGroupAccount, _ *[]upstream.AdminGroupAccountInfo) {
			account.MultiplierResolutionStatus = MultiplierResolutionStale
			account.EffectiveMultiplier = candidateFloat64Pointer(0.25)
			account.MultiplierSource = MultiplierSourceUpstreamKey
		}, wantReason: "multiplier_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := base
			account.ModelHealth = append([]ModelHealth(nil), base.ModelHealth...)
			rows := []upstream.AdminGroupAccountInfo{baseObservation}
			test.mutate(&account, &rows)
			summary, candidates := candidatePlan(
				[]AdminGroupAccount{account}, []PrioritySyncState{baseState},
				map[string][]upstream.AdminGroupAccountInfo{"first": rows}, test.inventoryComplete,
			)
			if summary.Mode != "safety_lock" || summary.CandidatePriorityReady || summary.SafetyReason != test.wantReason || candidates["first"].Priority != nil {
				t.Fatalf("unsafe evidence must fail closed: summary=%+v candidate=%+v", summary, candidates["first"])
			}
		})
	}
}

// Regression mutation guarded: treating every protected account as a blocker would reject a second-layer candidate
// even when the protected account's current Priority sorts strictly after the proposed promoted range.
func TestBuildSub2APIPriorityCandidatePlanBlocksTakeoverOnlyWhenProtectedPriorityMaySortFirst(t *testing.T) {
	first, firstObservation, firstState := candidateAccount("first-unavailable", 1, StateDisabled, 1, 10, 20)
	firstObservation.Status = "inactive"
	firstObservation.Schedulable = boolPointer(false)
	second, secondObservation, secondState := candidateAccount("second", 2, StateHealthy, 1, 10, 21)
	secondRecovering, secondRecoveringObservation, secondRecoveringState := candidateAccount("second-recovering", 2, StateRecovering, 1, 10, 22)
	protected, protectedObservation, _ := candidateAccount("protected", 2, StateHealthy, 1, 10, 100000)
	protected.PriorityManaged = false
	groups := []AdminGroupHealth{{ID: "g1", Accounts: []AdminGroupAccount{first, second, secondRecovering, protected}}}
	states := []PrioritySyncState{firstState, secondState, secondRecoveringState}
	observations := map[string][]upstream.AdminGroupAccountInfo{
		"first-unavailable": {firstObservation},
		"second":            {secondObservation},
		"second-recovering": {secondRecoveringObservation},
		"protected":         {protectedObservation},
	}

	summary, candidates := candidatePlanForGroups(groups, states, observations, true)
	if summary.Mode != "second_active" || !summary.CandidatePriorityReady {
		t.Fatalf("protected Priority after promoted candidates must not block takeover: %+v", summary)
	}
	if got := candidates["second"]; got.Priority == nil || *got.Priority != 10 {
		t.Fatalf("second layer should receive the first normal candidate Priority: %+v", got)
	}
	if got := candidates["second-recovering"]; got.Priority == nil || *got.Priority != 100 {
		t.Fatalf("recovering second layer should use its normal health band: %+v", got)
	}
	if candidates["protected"].BlocksTakeover {
		t.Fatalf("protected Priority 100000 cannot sort before any promoted candidate: %+v", candidates["protected"])
	}

	protected.Priority = intPointer(50)
	protectedObservation.Priority = intPointer(50)
	groups[0].Accounts[3] = protected
	observations["protected"] = []upstream.AdminGroupAccountInfo{protectedObservation}
	summary, candidates = candidatePlanForGroups(groups, states, observations, true)
	if summary.Mode != "safety_lock" || summary.CandidatePriorityReady || summary.SafetyReason != "protected_account_blocker" {
		t.Fatalf("protected Priority before proposed second layer must safety lock: %+v", summary)
	}
	if !candidates["protected"].BlocksTakeover || candidates["second"].Priority != nil {
		t.Fatalf("blocked takeover must expose the blocker and no writable candidate Priority: protected=%+v second=%+v", candidates["protected"], candidates["second"])
	}
}

// Regression mutation guarded: checkpoint provenance decides scope, but a known current Priority that sorts after all
// proposed second-layer candidates must not become an unconditional takeover blocker.
func TestBuildSub2APIPriorityCandidatePlanUsesKnownCurrentPriorityForProtectedEvidenceBlockers(t *testing.T) {
	tests := []struct {
		name         string
		state        PrioritySyncState
		wantReason   string
		wantEvidence string
	}{
		{name: "stale pending", state: PrioritySyncState{LastAppliedPriority: 10, PendingPriority: intPointer(15)}, wantReason: "stale_pending", wantEvidence: "stale_pending"},
		{name: "checkpoint mismatch", state: PrioritySyncState{LastAppliedPriority: 10}, wantReason: "priority_unknown", wantEvidence: "unknown"},
		{name: "recorded conflict", state: PrioritySyncState{LastAppliedPriority: 10, Conflict: true}, wantReason: "priority_conflict", wantEvidence: "conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first, firstObservation, firstState := candidateAccount("first-unavailable", 1, StateDisabled, 1, 10, 20)
			firstObservation.Status = "inactive"
			firstObservation.Schedulable = boolPointer(false)
			second, secondObservation, secondState := candidateAccount("second", 2, StateHealthy, 1, 10, 21)
			protected, protectedObservation, _ := candidateAccount("protected", 2, StateHealthy, 1, 10, 100000)
			test.state.TargetID = "protected"
			summary, candidates := candidatePlan(
				[]AdminGroupAccount{first, second, protected},
				[]PrioritySyncState{firstState, secondState, test.state},
				map[string][]upstream.AdminGroupAccountInfo{
					"first-unavailable": {firstObservation},
					"second":            {secondObservation},
					"protected":         {protectedObservation},
				},
				true,
			)
			if summary.Mode != "second_active" || !summary.CandidatePriorityReady {
				t.Fatalf("known protected Priority after promoted candidates must not globally lock: %+v", summary)
			}
			got := candidates["protected"]
			if got.State != "out_of_scope" || got.Reason != test.wantReason || got.PriorityEvidence != test.wantEvidence || got.BlocksTakeover {
				t.Fatalf("protected classification=%+v want reason=%s evidence=%s without blocker", got, test.wantReason, test.wantEvidence)
			}
		})
	}
}

// Regression mutation guarded: a protected account that is explicitly unavailable cannot carry traffic and therefore
// must not block a promoted second layer merely because its current Priority is missing.
func TestBuildSub2APIPriorityCandidatePlanDoesNotBlockOnExplicitlyUnavailableProtectedAccount(t *testing.T) {
	first, firstObservation, firstState := candidateAccount("first-unavailable", 1, StateDisabled, 1, 10, 20)
	firstObservation.Status = "inactive"
	firstObservation.Schedulable = boolPointer(false)
	second, secondObservation, secondState := candidateAccount("second", 2, StateHealthy, 1, 10, 21)
	protected, protectedObservation, protectedState := candidateAccount("protected-unavailable", 2, StateHealthy, 1, 10, 22)
	protected.Priority = nil
	protectedObservation.Priority = nil
	protectedObservation.Status = "inactive"
	protectedObservation.Schedulable = boolPointer(false)

	summary, candidates := candidatePlan(
		[]AdminGroupAccount{first, second, protected},
		[]PrioritySyncState{firstState, secondState, protectedState},
		map[string][]upstream.AdminGroupAccountInfo{
			"first-unavailable":     {firstObservation},
			"second":                {secondObservation},
			"protected-unavailable": {protectedObservation},
		},
		true,
	)
	if summary.Mode != "second_active" || !summary.CandidatePriorityReady {
		t.Fatalf("explicitly unavailable protected account must not block the second layer: %+v", summary)
	}
	if got := candidates["protected-unavailable"]; got.State != "out_of_scope" || got.Reason != "priority_unknown" || got.BlocksTakeover {
		t.Fatalf("unavailable protected account classification=%+v want out_of_scope without blocker", got)
	}
}

// Regression mutation guarded: a read-only candidate projection must never reuse reconciliation and touch remote or durable state.
func TestAdminGroupsPriorityCandidateProjectionHasZeroWritesAndPreservesAllState(t *testing.T) {
	repo := newFakeRepository()
	targetID := "sub2api:ws1:protected"
	priority := 7
	pending := 3
	schedulable := true
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "one", Platform: string(upstream.PlatformSub2API)}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "protected", Name: "protected", Status: "active", Schedulable: &schedulable, Priority: &priority}},
		},
	}
	repo.priorityStates["user1|ws1|"+targetID] = PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 50, LastAppliedPriority: 4, PendingPriority: &pending, EffectiveMultiplier: 0.5,
	}
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", AppliedSignature: "applied", PendingSignature: "pending", LastDecision: "protected",
	}
	repo.states[targetID] = map[string]ConnectionHealthState{"gpt-test": {
		UserID: "user1", AdminAccountID: "ws1", ConnectionID: targetID, ModelName: "gpt-test", State: StateHealthy,
	}}
	actions := &fakeTargetPriorityActioner{}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	service.priorityActions = actions
	snapshot := func() string {
		t.Helper()
		payload, err := json.Marshal([]any{repo.priorityStates, repo.priorityWorkspaces, repo.states, repo.events, repo.accountTiers, reader.accountsByGrp})
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}
	before := snapshot()

	groups, err := service.AdminGroups(context.Background(), "user1")
	if err != nil {
		t.Fatalf("AdminGroups() error = %v", err)
	}
	if len(groups) != 1 || groups[0].PriorityCandidateSummary == nil {
		t.Fatalf("candidate summary missing from read response: %+v", groups)
	}
	if after := snapshot(); after != before {
		t.Fatalf("read-only candidate call changed protected state\nbefore=%s\nafter=%s", before, after)
	}
	if len(actions.calls) != 0 {
		t.Fatalf("read-only candidate call invoked Priority actioner: %+v", actions.calls)
	}
}

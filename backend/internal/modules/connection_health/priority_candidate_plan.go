package connection_health

import (
	"math"
	"sort"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

type PriorityCandidateSummary struct {
	Mode                   string                      `json:"mode"`
	CandidatePriorityReady bool                        `json:"candidatePriorityReady"`
	SafetyReason           string                      `json:"safetyReason,omitempty"`
	CandidateCount         int                         `json:"candidateCount"`
	OutOfScopeCount        int                         `json:"outOfScopeCount"`
	BlockerCount           int                         `json:"blockerCount"`
	Capacities             []PriorityCandidateCapacity `json:"capacities"`
}

type PriorityCandidateCapacity struct {
	Region     string `json:"region"`
	HealthBand string `json:"healthBand"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	Capacity   int    `json:"capacity"`
	Actual     int    `json:"actual"`
	Remaining  int    `json:"remaining"`
	Overflow   bool   `json:"overflow"`
}

type PriorityCandidateProjection struct {
	State            string   `json:"state"`
	Reason           string   `json:"reason,omitempty"`
	Rank             *int     `json:"rank,omitempty"`
	Priority         *int     `json:"priority,omitempty"`
	Region           string   `json:"region,omitempty"`
	HealthBand       string   `json:"healthBand,omitempty"`
	SuccessLatencyMs *int     `json:"successLatencyMs,omitempty"`
	Multiplier       *float64 `json:"multiplier,omitempty"`
	PriorityEvidence string   `json:"priorityEvidence"`
	BlocksTakeover   bool     `json:"blocksTakeover"`
}

type priorityCandidateSortEvidence struct {
	activeModels        map[string]struct{}
	states              []ConnectionHealthState
	healthBand          int
	successLatencyMs    *int
	multiplier          float64
	multiplierAvailable bool
}

func buildPriorityCandidateSortEvidence(item *priorityTargetInventory, states []ConnectionHealthState) priorityCandidateSortEvidence {
	activeModels := activeHealthPriorityModels(item)
	activeStates := activeHealthPriorityStates(states, activeModels)
	multiplier, multiplierAvailable := effectiveHealthSortMultiplier(item)
	return priorityCandidateSortEvidence{
		activeModels:        activeModels,
		states:              activeStates,
		healthBand:          priorityHealthBand(activeStates, len(activeModels)),
		successLatencyMs:    completeTargetSuccessLatency(activeStates, activeModels),
		multiplier:          multiplier,
		multiplierAvailable: multiplierAvailable,
	}
}

type priorityCandidateTarget struct {
	targetID         string
	name             string
	tier             int
	tierConflict     bool
	priority         *int
	priorityConflict bool
	priorityManaged  bool
	policies         map[string]AssignedPolicySummary
	projection       PriorityCandidateProjection
	healthBand       int
	multiplier       float64
	latency          int
}

type priorityCandidateRegion struct {
	name       string
	healthBand string
	start      int
	end        int
}

var sub2APIPriorityCandidateRegions = []priorityCandidateRegion{
	{name: "normal", healthBand: "healthy", start: 10, end: 99},
	{name: "normal", healthBand: "recovering", start: 100, end: 999},
	{name: "normal", healthBand: "degraded", start: 1000, end: 9999},
	{name: "hot_standby", healthBand: "healthy", start: 10000, end: 39999},
	{name: "hot_standby", healthBand: "recovering", start: 40000, end: 69999},
	{name: "hot_standby", healthBand: "degraded", start: 70000, end: 99999},
}

func buildSub2APIPriorityCandidatePlan(
	groups []AdminGroupHealth,
	priorityStates []PrioritySyncState,
	observations map[string][]upstream.AdminGroupAccountInfo,
	inventoryComplete bool,
	sortEvidence map[string]priorityCandidateSortEvidence,
) (PriorityCandidateSummary, map[string]PriorityCandidateProjection) {
	targets := collectPriorityCandidateTargets(groups)
	stateByTarget := make(map[string]PrioritySyncState, len(priorityStates))
	for _, state := range priorityStates {
		stateByTarget[state.TargetID] = state
	}

	targetIDs := make([]string, 0, len(targets))
	for targetID := range targets {
		targetIDs = append(targetIDs, targetID)
	}
	sort.Strings(targetIDs)

	firstManaged := 0
	firstAvailable := 0
	firstUnavailable := 0
	protectedTakeoverBlocker := false
	safetyReason := ""
	fallbackSafetyReason := ""
	candidates := make([]*priorityCandidateTarget, 0, len(targets))
	for _, targetID := range targetIDs {
		target := targets[targetID]
		state, stateExists := stateByTarget[targetID]
		target.projection.PriorityEvidence = priorityCandidateEvidence(target.priority, state, stateExists)
		if observationConflictReason := priorityCandidateObservationConflictReason(observations[targetID]); observationConflictReason != "" {
			target.projection.State = "safety_lock"
			target.projection.Reason = observationConflictReason
			if safetyReason == "" {
				safetyReason = target.projection.Reason
			}
			continue
		}
		classifyPriorityCandidateScope(target, state, stateExists)
		if target.projection.State != "" {
			if target.tier == 1 && target.projection.Reason == "priority_unknown" && target.priority == nil && fallbackSafetyReason == "" {
				fallbackSafetyReason = target.projection.Reason
			}
			continue
		}

		if target.tier == 1 {
			firstManaged++
		}
		evidence, evidenceExists := sortEvidence[targetID]
		availability, reason := classifyPriorityCandidateAvailability(target, observations[targetID], evidence, evidenceExists)
		switch availability {
		case "unavailable":
			target.projection.State = "unavailable"
			target.projection.Reason = reason
			if target.tier == 1 {
				firstUnavailable++
			}
		case "safety_lock":
			target.projection.State = "safety_lock"
			target.projection.Reason = reason
			if safetyReason == "" {
				safetyReason = reason
			}
		case "candidate":
			target.projection.State = "candidate"
			if target.tier == 1 {
				firstAvailable++
			}
			candidates = append(candidates, target)
		}
	}

	mode := "safety_lock"
	if firstAvailable > 0 {
		mode = "first_active"
	} else if firstManaged > 0 && firstUnavailable == firstManaged {
		mode = "second_active"
	} else if safetyReason == "" {
		if fallbackSafetyReason != "" {
			safetyReason = fallbackSafetyReason
		} else {
			safetyReason = "first_layer_unavailable"
		}
	}
	if !inventoryComplete {
		mode = "safety_lock"
		safetyReason = "inventory_incomplete"
	} else if safetyReason != "" {
		mode = "safety_lock"
	}

	for _, target := range candidates {
		if mode == "second_active" {
			target.projection.Region = "normal"
		} else if target.tier == 1 {
			target.projection.Region = "normal"
		} else {
			target.projection.Region = "hot_standby"
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.projection.Region != right.projection.Region {
			return left.projection.Region == "normal"
		}
		if left.healthBand != right.healthBand {
			return left.healthBand < right.healthBand
		}
		if left.multiplier != right.multiplier {
			return left.multiplier < right.multiplier
		}
		if left.latency != right.latency {
			return left.latency < right.latency
		}
		return left.targetID < right.targetID
	})

	for index, target := range candidates {
		rank := index + 1
		target.projection.Rank = &rank
	}
	capacities := priorityCandidateCapacities(candidates)
	capacityOverflow := false
	for _, capacity := range capacities {
		if capacity.Overflow {
			capacityOverflow = true
			break
		}
	}
	if capacityOverflow {
		mode = "safety_lock"
		safetyReason = "capacity_overflow"
	}
	ready := (mode == "first_active" || mode == "second_active") && !capacityOverflow && len(candidates) > 0
	if ready {
		assignPriorityCandidateValues(candidates)
	}
	promotedSecondPriority := promotedSecondCandidatePriority(candidates)
	for _, targetID := range targetIDs {
		target := targets[targetID]
		if target.projection.State != "out_of_scope" {
			continue
		}
		evidence, evidenceExists := sortEvidence[targetID]
		target.projection.BlocksTakeover = priorityCandidateProtectedBlocker(
			target, observations[targetID], promotedSecondPriority, evidence, evidenceExists,
		)
		if target.projection.BlocksTakeover {
			protectedTakeoverBlocker = true
		}
	}
	if mode == "second_active" && protectedTakeoverBlocker {
		mode = "safety_lock"
		safetyReason = "protected_account_blocker"
		ready = false
		for _, candidate := range candidates {
			candidate.projection.Priority = nil
		}
	}

	projections := make(map[string]PriorityCandidateProjection, len(targets))
	outOfScopeCount := 0
	blockerCount := 0
	for _, targetID := range targetIDs {
		projection := targets[targetID].projection
		if projection.State == "out_of_scope" {
			outOfScopeCount++
		}
		if projection.BlocksTakeover {
			blockerCount++
		}
		projections[targetID] = projection
	}
	return PriorityCandidateSummary{
		Mode: mode, CandidatePriorityReady: ready, SafetyReason: safetyReason,
		CandidateCount: len(candidates), OutOfScopeCount: outOfScopeCount, BlockerCount: blockerCount,
		Capacities: capacities,
	}, projections
}

func collectPriorityCandidateTargets(groups []AdminGroupHealth) map[string]*priorityCandidateTarget {
	targets := make(map[string]*priorityCandidateTarget)
	for _, group := range groups {
		for _, account := range group.Accounts {
			target := targets[account.TargetID]
			if target == nil {
				target = &priorityCandidateTarget{
					targetID: account.TargetID, name: account.Name, tier: effectiveAccountTier(account.AccountTier),
					priority: cloneIntPointer(account.Priority), policies: make(map[string]AssignedPolicySummary),
				}
				targets[account.TargetID] = target
			} else if target.tier != effectiveAccountTier(account.AccountTier) {
				target.tierConflict = true
			}
			target.priorityManaged = target.priorityManaged || account.PriorityManaged
			target.priorityConflict = target.priorityConflict || account.PriorityConflict
			for _, policy := range account.EffectivePolicies {
				key := policy.PolicyID + "\x00" + policy.PriorityMode + "\x00" + policy.StrategyMode
				if policy.PolicyID == "" {
					key = policy.PolicyName + "\x00" + policy.PriorityMode + "\x00" + policy.StrategyMode
				}
				target.policies[key] = policy
			}
		}
	}
	return targets
}

func classifyPriorityCandidateScope(target *priorityCandidateTarget, state PrioritySyncState, stateExists bool) {
	hasPriorityPolicy := false
	hasMultiplierOnly := false
	for _, policy := range target.policies {
		if !policy.Enabled || normalizePriorityMode(policy.PriorityMode) != PriorityModeMultiplier {
			continue
		}
		hasPriorityPolicy = true
		if normalizeStrategyMode(policy.StrategyMode) == StrategyModeMultiplierOnly {
			hasMultiplierOnly = true
		}
	}
	evidence := target.projection.PriorityEvidence
	if target.priorityConflict || (stateExists && state.Conflict) {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "priority_conflict"
		target.projection.PriorityEvidence = "conflict"
		target.projection.BlocksTakeover = true
		return
	}
	if evidence == "stale_pending" {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "stale_pending"
		return
	}
	if target.priority != nil && *target.priority >= 1 && *target.priority <= 9 {
		if hasMultiplierOnly && (evidence == "last_applied" || evidence == "confirmed_pending") {
			target.projection.State = "out_of_scope"
			target.projection.Reason = "multiplier_only"
		} else if evidence == "stale_pending" {
			target.projection.State = "out_of_scope"
			target.projection.Reason = "stale_pending"
		} else {
			target.projection.State = "out_of_scope"
			target.projection.Reason = "protected_priority_1_9"
		}
		target.projection.BlocksTakeover = true
		return
	}
	if target.priority == nil || (stateExists && target.priorityManaged && evidence == "unknown") {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "priority_unknown"
		return
	}
	if !target.priorityManaged || !stateExists {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "unmanaged"
		target.projection.BlocksTakeover = true
		return
	}
	if hasMultiplierOnly {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "multiplier_only"
		target.projection.BlocksTakeover = true
		return
	}
	if !hasPriorityPolicy {
		target.projection.State = "out_of_scope"
		target.projection.Reason = "no_effective_health_policy"
		target.projection.BlocksTakeover = true
	}
}

func classifyPriorityCandidateAvailability(
	target *priorityCandidateTarget,
	observations []upstream.AdminGroupAccountInfo,
	evidence priorityCandidateSortEvidence,
	evidenceExists bool,
) (string, string) {
	if target.tierConflict {
		return "safety_lock", "tier_conflict"
	}
	localUnavailable := evidenceExists && evidence.healthBand == 4
	upstreamUnavailable, unsafeReason := priorityCandidateObservationState(observations)
	if localUnavailable || upstreamUnavailable {
		return "unavailable", "explicitly_unavailable"
	}
	if unsafeReason != "" {
		return "safety_lock", unsafeReason
	}
	if !evidenceExists || len(evidence.activeModels) == 0 || evidence.healthBand == 3 {
		return "safety_lock", "health_incomplete"
	}
	for _, state := range evidence.states {
		switch state.State {
		case StateHealthy, StateRecovering, StateDegraded, StateObserving:
		case StateDisabled, StateSuspended:
			return "unavailable", "explicitly_unavailable"
		default:
			return "safety_lock", "health_unknown"
		}
	}
	if evidence.successLatencyMs == nil {
		return "safety_lock", "health_incomplete"
	}
	if !evidence.multiplierAvailable || !validPriorityCandidateMultiplier(evidence.multiplier) {
		return "safety_lock", "multiplier_unavailable"
	}
	target.healthBand = evidence.healthBand
	target.multiplier = evidence.multiplier
	target.latency = *evidence.successLatencyMs
	target.projection.HealthBand = priorityCandidateHealthBandName(evidence.healthBand)
	target.projection.SuccessLatencyMs = cloneIntPointer(evidence.successLatencyMs)
	target.projection.Multiplier = priorityCandidateFloatPointer(evidence.multiplier)
	return "candidate", ""
}

func priorityCandidateProtectedBlocker(
	target *priorityCandidateTarget,
	observations []upstream.AdminGroupAccountInfo,
	promotedPriority *int,
	evidence priorityCandidateSortEvidence,
	evidenceExists bool,
) bool {
	if evidenceExists && evidence.healthBand == 4 {
		return false
	}
	unavailable, unsafeReason := priorityCandidateObservationState(observations)
	if unavailable {
		return false
	}
	if unsafeReason != "" || target.priority == nil || promotedPriority == nil {
		return true
	}
	return *target.priority <= *promotedPriority
}

func priorityCandidateFloatPointer(value float64) *float64 {
	return &value
}

func priorityCandidateObservationState(observations []upstream.AdminGroupAccountInfo) (bool, string) {
	if len(observations) == 0 {
		return false, "observation_unknown"
	}
	status := ""
	var schedulable *bool
	for _, observation := range observations {
		currentStatus := strictSub2APIAccountStatus(observation.Status)
		if currentStatus == "" {
			return false, "status_unknown"
		}
		if status != "" && status != currentStatus {
			return false, "observation_conflict"
		}
		status = currentStatus
		if observation.Schedulable == nil {
			return false, "schedulable_unknown"
		}
		if schedulable != nil && *schedulable != *observation.Schedulable {
			return false, "observation_conflict"
		}
		value := *observation.Schedulable
		schedulable = &value
	}
	return status == "inactive" || schedulable == nil || !*schedulable, ""
}

func priorityCandidateObservationConflictReason(observations []upstream.AdminGroupAccountInfo) string {
	if len(observations) < 2 {
		return ""
	}
	firstPriority := observations[0].Priority
	firstModels := priorityCandidateModelSetKey(observations[0].Models)
	for _, observation := range observations[1:] {
		if !sameOptionalInt(firstPriority, observation.Priority) {
			return "priority_observation_conflict"
		}
		if priorityCandidateModelSetKey(observation.Models) != firstModels {
			return "observation_conflict"
		}
	}
	return ""
}

func priorityCandidateModelSetKey(raw string) string {
	seen := make(map[string]struct{})
	for _, model := range splitModelList(raw) {
		seen[model] = struct{}{}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return strings.Join(models, "\x00")
}

func strictSub2APIAccountStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "enabled", "1":
		return "active"
	case "inactive", "disabled", "2":
		return "inactive"
	default:
		return ""
	}
}

func priorityCandidateEvidence(priority *int, state PrioritySyncState, exists bool) string {
	if !exists || priority == nil {
		return "unknown"
	}
	if state.Conflict {
		return "conflict"
	}
	if state.PendingPriority != nil {
		if *priority == *state.PendingPriority {
			return "confirmed_pending"
		}
		return "stale_pending"
	}
	if *priority == state.LastAppliedPriority {
		return "last_applied"
	}
	return "unknown"
}

func priorityCandidateCapacities(candidates []*priorityCandidateTarget) []PriorityCandidateCapacity {
	actual := make(map[string]int)
	for _, candidate := range candidates {
		key := candidate.projection.Region + "\x00" + candidate.projection.HealthBand
		actual[key]++
	}
	capacities := make([]PriorityCandidateCapacity, 0, len(sub2APIPriorityCandidateRegions))
	for _, region := range sub2APIPriorityCandidateRegions {
		capacity := region.end - region.start + 1
		count := actual[region.name+"\x00"+region.healthBand]
		remaining := capacity - count
		if remaining < 0 {
			remaining = 0
		}
		capacities = append(capacities, PriorityCandidateCapacity{
			Region: region.name, HealthBand: region.healthBand, Start: region.start, End: region.end,
			Capacity: capacity, Actual: count, Remaining: remaining, Overflow: count > capacity,
		})
	}
	return capacities
}

func assignPriorityCandidateValues(candidates []*priorityCandidateTarget) {
	next := make(map[string]int)
	for _, region := range sub2APIPriorityCandidateRegions {
		next[region.name+"\x00"+region.healthBand] = region.start
	}
	for _, candidate := range candidates {
		key := candidate.projection.Region + "\x00" + candidate.projection.HealthBand
		priority := next[key]
		candidate.projection.Priority = &priority
		next[key]++
	}
}

func promotedSecondCandidatePriority(candidates []*priorityCandidateTarget) *int {
	nextByBand := map[int]int{0: 10, 1: 100, 2: 1000}
	var greatest *int
	for _, candidate := range candidates {
		if candidate.tier != 2 {
			continue
		}
		value := nextByBand[candidate.healthBand]
		nextByBand[candidate.healthBand] = value + 1
		if greatest == nil || value > *greatest {
			greatest = priorityCandidateIntPointer(value)
		}
	}
	return greatest
}

func priorityCandidateHealthBandName(band int) string {
	switch band {
	case 1:
		return "recovering"
	case 2:
		return "degraded"
	default:
		return "healthy"
	}
}

func validPriorityCandidateMultiplier(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func sameOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func priorityCandidateIntPointer(value int) *int {
	return &value
}

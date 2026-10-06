package connection_health

import (
	"sort"
	"time"
)

const (
	ProbeBlockedInterval                   = "probe_interval"
	ProbeBlockedCooldown                   = "cooldown"
	ProbeBlockedFailureBackoff             = "failure_backoff"
	ProbeBlockedHealthDisabled             = "health_disabled"
	ProbeBlockedUpstreamSchedulingDisabled = "upstream_scheduling_disabled"
	ProbeBlockedDailyBudget                = "daily_probe_budget_exhausted"
	ProbeBlockedBudgetUnavailable          = "daily_probe_budget_unavailable"
)

type EffectiveProbePolicySource struct {
	PolicyID             string `json:"policyId"`
	PolicyName           string `json:"policyName"`
	ContinueAutoProbe    bool   `json:"continueAutoProbe"`
	EffectiveIntervalSec int    `json:"effectiveIntervalSeconds"`
}

type EffectiveProbeDecision struct {
	ContinueAutoProbe        bool                         `json:"continueAutoProbe"`
	EffectiveIntervalSeconds int                          `json:"effectiveIntervalSeconds"`
	SourcePolicies           []EffectiveProbePolicySource `json:"sourcePolicies"`
	NextProbeAt              *time.Time                   `json:"nextProbeAt"`
	BlockedReason            string                       `json:"blockedReason"`
	BudgetPolicyID           string                       `json:"budgetPolicyId"`
}

func calculateEffectiveProbeDecision(policies []Policy, schedulable *bool, state *ConnectionHealthState, now time.Time) EffectiveProbeDecision {
	return calculateEffectiveProbeDecisionWithBudgets(policies, schedulable, state, now, nil)
}

type effectiveProbeCandidate struct {
	policy          Policy
	intervalSec     int
	nextProbeAt     time.Time
	blockedReason   string
	continueProbing bool
}

func calculateEffectiveProbeDecisionWithBudgets(policies []Policy, schedulable *bool, state *ConnectionHealthState, now time.Time, budgetUsage map[string]int) EffectiveProbeDecision {
	return calculateEffectiveProbeDecisionWithBudgetAndReuse(policies, schedulable, state, now, budgetUsage, true)
}

func calculateEffectiveProbeDecisionWithBudgetAndReuse(policies []Policy, schedulable *bool, state *ConnectionHealthState, now time.Time, budgetUsage map[string]int, reuseProbeInterval bool) EffectiveProbeDecision {
	decision := EffectiveProbeDecision{SourcePolicies: make([]EffectiveProbePolicySource, 0, len(policies))}
	candidates := make([]effectiveProbeCandidate, 0, len(policies))
	var judgment Policy
	for index, policy := range policies {
		if index == 0 || preferProbePolicy(policy, judgment) {
			judgment = policy
		}
	}
	for _, policy := range policies {
		continueAutoProbe, intervalSeconds := policyProbeCadence(policy, schedulable)
		decision.SourcePolicies = append(decision.SourcePolicies, EffectiveProbePolicySource{
			PolicyID: policy.ID, PolicyName: policy.Name, ContinueAutoProbe: continueAutoProbe,
			EffectiveIntervalSec: intervalSeconds,
		})
		if continueAutoProbe {
			candidate := effectiveProbeCandidate{policy: policy, intervalSec: intervalSeconds, continueProbing: true}
			candidate.nextProbeAt, candidate.blockedReason = policyNextProbeAtForRule(state, defaultInt(policy.ProbeIntervalSeconds, 60), judgment, now, reuseProbeInterval)
			if schedulable != nil && !*schedulable {
				if last := lastProbeAttemptAt(state); last != nil {
					floor := last.Add(time.Duration(intervalSeconds) * time.Second)
					if floor.After(candidate.nextProbeAt) {
						candidate.nextProbeAt, candidate.blockedReason = floor, ProbeBlockedInterval
					}
				}
			}
			if budgetUsage != nil && budgetUsage[policy.ID] >= probeBudgetLimit(policy) {
				budgetReset := probeBudgetDayStart(now).Add(24 * time.Hour).UTC()
				if budgetReset.After(candidate.nextProbeAt) {
					candidate.nextProbeAt = budgetReset
					candidate.blockedReason = ProbeBlockedDailyBudget
				}
			}
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(decision.SourcePolicies, func(i, j int) bool {
		return decision.SourcePolicies[i].PolicyID < decision.SourcePolicies[j].PolicyID
	})
	if len(candidates) == 0 {
		decision.BlockedReason = ProbeBlockedUpstreamSchedulingDisabled
		return decision
	}

	decision.ContinueAutoProbe = true
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.nextProbeAt.Before(selected.nextProbeAt) ||
			(candidate.nextProbeAt.Equal(selected.nextProbeAt) && candidate.policy.ID < selected.policy.ID) {
			selected = candidate
		}
	}
	decision.EffectiveIntervalSeconds = selected.intervalSec
	decision.BudgetPolicyID = selected.policy.ID
	if state != nil && state.State == StateDisabled {
		decision.NextProbeAt = nil
		decision.BlockedReason = ProbeBlockedHealthDisabled
		return decision
	}
	next := selected.nextProbeAt.UTC()
	decision.NextProbeAt = &next
	if now.Before(next) {
		decision.BlockedReason = selected.blockedReason
	}
	return decision
}

func policyProbeCadence(policy Policy, schedulable *bool) (bool, int) {
	intervalSeconds := defaultInt(policy.ProbeIntervalSeconds, 60)
	continueAutoProbe := true
	if schedulable != nil && !*schedulable {
		continueAutoProbe = policy.ContinueProbeWhenUnschedulable
		// Zero only occurs in legacy in-memory values; persisted rows have a database default.
		if policy.UnschedulableProbeIntervalMinutes <= 0 {
			continueAutoProbe = true
		}
		intervalSeconds = defaultInt(policy.UnschedulableProbeIntervalMinutes, 60) * 60
	}
	return continueAutoProbe, intervalSeconds
}

func policyNextProbeAt(state *ConnectionHealthState, intervalSeconds int, now time.Time) (time.Time, string) {
	return policyNextProbeAtForDecision(state, intervalSeconds, now, true)
}

func policyNextProbeAtForDecision(state *ConnectionHealthState, intervalSeconds int, now time.Time, reuseProbeInterval bool) (time.Time, string) {
	next := now
	blockedReason := ""
	var lastAttemptAt *time.Time
	if state != nil {
		lastAttemptAt = state.LastProbeAt
		if _, credentialAt := currentCredentialFailure(*state); credentialAt != nil && (lastAttemptAt == nil || credentialAt.After(*lastAttemptAt)) {
			lastAttemptAt = credentialAt
		}
	}
	if lastAttemptAt != nil {
		if reuseProbeInterval {
			interval := time.Duration(intervalSeconds) * time.Second
			blockedReason = ProbeBlockedInterval
			if backoff := ProbeBackoff(state.ConsecutiveFailures); backoff > interval {
				interval = backoff
				blockedReason = ProbeBlockedFailureBackoff
			}
			next = lastAttemptAt.Add(interval)
		} else if backoff := ProbeBackoff(state.ConsecutiveFailures); backoff > 0 {
			backoffUntil := lastAttemptAt.Add(backoff)
			if backoffUntil.After(next) {
				next = backoffUntil
				blockedReason = ProbeBlockedFailureBackoff
			}
		}
	}
	if state != nil && state.CooldownUntil != nil && state.CooldownUntil.After(next) {
		next = *state.CooldownUntil
		blockedReason = ProbeBlockedCooldown
	}
	return next.UTC(), blockedReason
}

func lastProbeAttemptAt(state *ConnectionHealthState) *time.Time {
	if state == nil {
		return nil
	}
	last := state.LastProbeAt
	if _, at := currentCredentialFailure(*state); at != nil && (last == nil || at.After(*last)) {
		last = at
	}
	return last
}

// Read-side scheduling must not borrow a failure origin from another protocol.
// Keep durable state intact; the eventual result commit performs the reset.
func probeDecisionStateForProtocol(state *ConnectionHealthState, protocol TestProtocol) *ConnectionHealthState {
	if state == nil || !validTestProtocol(protocol) {
		return state
	}
	counterProtocol := TestProtocolChatCompletions
	if state.CounterProtocol != nil {
		counterProtocol = *state.CounterProtocol
	}
	if counterProtocol == protocol {
		return state
	}
	current := *state
	current.FailingSince = nil
	current.ConsecutiveFailures, current.ConsecutiveSuccesses = 0, 0
	return &current
}

func policyNextProbeAtForRule(state *ConnectionHealthState, intervalSeconds int, judgment Policy, now time.Time, reuseProbeInterval bool) (time.Time, string) {
	if judgment.RuleVersion != RuleVersionV2 {
		next, reason := policyNextProbeAtForDecision(state, intervalSeconds, now, reuseProbeInterval)
		return applyLongFailureCadence(state, judgment, now, next, reason)
	}
	next, reason := now, ""
	last := lastProbeAttemptAt(state)
	if state != nil && state.State == StateSuspect && state.RecheckPending {
		next, reason = now, ""
	} else if last != nil {
		interval := time.Duration(intervalSeconds) * time.Second
		reason = ProbeBlockedInterval
		if state.State == StateSuspended {
			if state.CooldownUntil != nil && state.CooldownUntil.After(*last) {
				next, reason = *state.CooldownUntil, ProbeBlockedCooldown
				return applyLongFailureCadence(state, judgment, now, next, reason)
			}
			if state.ConsecutiveSuccesses == 0 {
				interval = time.Duration(RulePresetForPolicy(judgment).FailedRetryIntervalSeconds) * time.Second
				reason = ProbeBlockedFailureBackoff
			}
		}
		if reuseProbeInterval || state.State == StateSuspended {
			next = last.Add(interval)
		}
	}
	return applyLongFailureCadence(state, judgment, now, next, reason)
}

func applyLongFailureCadence(state *ConnectionHealthState, judgment Policy, now, next time.Time, reason string) (time.Time, string) {
	if state != nil && stateHasLongFailure(*state, judgment, now) {
		if last := lastProbeAttemptAt(state); last != nil {
			floor := last.Add(time.Duration(RulePresetForPolicy(judgment).LongFailureIntervalSeconds) * time.Second)
			if floor.After(next) {
				return floor.UTC(), ProbeBlockedFailureBackoff
			}
		}
	}
	return next.UTC(), reason
}

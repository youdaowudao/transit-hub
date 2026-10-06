package connection_health

import "time"

// TransitionInput 是状态机做一次决策所需的全部输入：探活前的状态快照 + 本次探活结果 + 所属策略阈值。
// 不依赖任何 IO，纯函数，便于单测覆盖全部分支。
type TransitionInput struct {
	Current              State
	CurrentWeight        int
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	CooldownUntil        *time.Time
	ObservingUntil       *time.Time
	Now                  time.Time
	Result               ResultKey
	Policy               Policy
}

// TransitionOutput 是状态机决策的结果：新状态 + 新权重 + 计数器 + 是否需要触发远端降级/恢复动作。
type TransitionOutput struct {
	NextState            State
	Weight               int
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	CooldownUntil        *time.Time
	ObservingUntil       *time.Time
	TriggerRemoteDegrade bool
	TriggerRemoteRestore bool
}

// isHardFailure 分类：5xx、认证失败、模型不存在，无需累计失败次数即可直接暂停。
func isHardFailure(result ResultKey) bool {
	switch result {
	case ResultServerError, ResultAuth, ResultModelNotFound:
		return true
	default:
		return false
	}
}

// isSoftFailure 分类：网络波动、限流、响应无法解析，先降级观察，达到阈值才暂停。
func isSoftFailure(result ResultKey) bool {
	switch result {
	case ResultNetworkFluctuation, ResultRateLimited, ResultInvalidResponse:
		return true
	default:
		return false
	}
}

// Transition 是健康状态机的核心决策函数。disabled 只能人工进出，探活结果不会自动改变它。
func Transition(in TransitionInput) TransitionOutput {
	if in.Current == StateDisabled {
		if in.Result == ResultSlowResponse {
			return transitionOnSlowResponse(in)
		}
		return TransitionOutput{
			NextState:            StateDisabled,
			Weight:               0,
			ConsecutiveFailures:  in.ConsecutiveFailures,
			ConsecutiveSuccesses: in.ConsecutiveSuccesses,
			ObservingUntil:       in.ObservingUntil,
		}
	}

	step := stepPercent(in.Policy)

	switch {
	case in.Result == ResultOK:
		return transitionOnSuccess(in, step)
	case in.Result == ResultSlowResponse:
		return transitionOnSlowResponse(in)
	case isHardFailure(in.Result):
		return transitionOnHardFailure(in)
	case isSoftFailure(in.Result):
		return transitionOnSoftFailure(in, step)
	default:
		// unsupported 等非探活结果不驱动状态机，原样保持。
		return TransitionOutput{
			NextState:            in.Current,
			Weight:               in.CurrentWeight,
			ConsecutiveFailures:  in.ConsecutiveFailures,
			ConsecutiveSuccesses: in.ConsecutiveSuccesses,
			ObservingUntil:       in.ObservingUntil,
		}
	}
}

func transitionOnSlowResponse(in TransitionInput) TransitionOutput {
	out := TransitionOutput{
		NextState:            in.Current,
		Weight:               in.CurrentWeight,
		ConsecutiveFailures:  in.ConsecutiveFailures,
		ConsecutiveSuccesses: 0,
		CooldownUntil:        in.CooldownUntil,
		ObservingUntil:       in.ObservingUntil,
	}
	switch in.Current {
	case StateHealthy, StateRecovering:
		out.NextState = StateDegraded
	case StateDisabled:
		out.Weight = 0
	}
	return out
}

// applyOutcomeDelay is the sole place where a successful request is labelled
// slow. v2 measures visible first text; legacy keeps its whole-response metric.
func applyOutcomeDelay(outcome ProbeOutcome, policy Policy) ProbeOutcome {
	if outcome.Result != ResultOK && outcome.Result != ResultSlowResponse {
		return outcome
	}
	if outcome.Result == ResultSlowResponse && outcome.FirstTokenMs == nil {
		return outcome
	}
	metric := outcome.LatencyMs
	if policy.RuleVersion == RuleVersionV2 && !outcome.LegacyCompatibility && outcome.FirstTokenMs != nil {
		metric = *outcome.FirstTokenMs
	}
	if metric > RulePresetForPolicy(policy).DelayLine(outcome.Protocol) {
		outcome.Result = ResultSlowResponse
	} else {
		outcome.Result = ResultOK
	}
	return outcome
}

func unchangedTransition(current ConnectionHealthState) TransitionOutput {
	return TransitionOutput{NextState: current.State, Weight: current.CurrentWeight, ConsecutiveFailures: current.ConsecutiveFailures, ConsecutiveSuccesses: current.ConsecutiveSuccesses, CooldownUntil: current.CooldownUntil, ObservingUntil: current.ObservingUntil}
}

func transitionV2(in TransitionInput) TransitionOutput {
	out := TransitionOutput{NextState: in.Current, Weight: in.CurrentWeight, ConsecutiveFailures: in.ConsecutiveFailures, ConsecutiveSuccesses: in.ConsecutiveSuccesses, CooldownUntil: in.CooldownUntil, ObservingUntil: in.ObservingUntil}
	if in.Current == StateDisabled {
		return out
	}
	if in.Result == ResultOK || in.Result == ResultSlowResponse {
		out.ConsecutiveFailures = 0
		if in.Current == StateSuspended {
			out.ConsecutiveSuccesses++
			if out.ConsecutiveSuccesses < successThreshold(in.Policy) {
				out.Weight = 0
				return out
			}
			out.TriggerRemoteRestore = true
		}
		out.NextState, out.Weight = StateHealthy, 100
		out.ConsecutiveSuccesses = 0
		out.CooldownUntil, out.ObservingUntil = nil, nil
		return out
	}
	if !isHardFailure(in.Result) && in.Result != ResultNetworkFluctuation && in.Result != ResultRateLimited {
		return out
	}
	if in.Current == StateHealthy {
		out.NextState, out.Weight = StateSuspect, 100
		out.ConsecutiveFailures, out.ConsecutiveSuccesses = 1, 0
		out.CooldownUntil, out.ObservingUntil = nil, nil
		return out
	}
	out.ConsecutiveFailures++
	out.ConsecutiveSuccesses = 0
	out.ObservingUntil = nil
	if in.Current == StateSuspended {
		out.Weight = 0
		return out
	}
	if out.ConsecutiveFailures >= failureThreshold(in.Policy) {
		until := in.Now.Add(cooldownWindow(in.Policy))
		out.NextState, out.Weight, out.CooldownUntil = StateSuspended, 0, &until
		out.TriggerRemoteDegrade = true
	} else {
		out.NextState, out.Weight = StateDegraded, 75
	}
	return out
}

func applyProbeOutcome(current ConnectionHealthState, outcome ProbeOutcome, policy Policy, now time.Time) (ConnectionHealthState, TransitionOutput) {
	protocolAware := validTestProtocol(outcome.Protocol) && !outcome.LegacyCompatibility
	v2 := protocolAware && policy.RuleVersion == RuleVersionV2
	policy = effectivePolicyFromPreset(policy)
	outcome = applyOutcomeDelay(outcome, policy)
	next := current
	if protocolAware {
		next.RuleVersion = RuleVersionLegacy
		if v2 {
			next.RuleVersion = RuleVersionV2
		}
		next.RecheckPending = false
		next.LastProbeProtocol = protocolPointer(outcome.Protocol)
		next.LastProbeTimeoutSeconds = intPtr(outcome.ProbeTimeoutSeconds)
	}
	next.LastProbeAt = &now
	next.LastLatencyMs = intPtr(outcome.LatencyMs)
	// Protocol changes invalidate counters and their origin even when the new
	// response is invalid. Preserve unmarked legacy callers' old invalid contract.
	if protocolAware && policy.RuleVersion != "" {
		counterProtocol := TestProtocolChatCompletions
		if current.CounterProtocol != nil {
			counterProtocol = *current.CounterProtocol
		}
		if counterProtocol != outcome.Protocol {
			next.FailingSince = nil
			next.ConsecutiveFailures, next.ConsecutiveSuccesses = 0, 0
		}
		next.CounterProtocol = protocolPointer(outcome.Protocol)
	}
	if protocolAware && outcome.Result == ResultInvalidResponse {
		return next, unchangedTransition(next)
	}
	counted := outcome.Result == ResultOK || outcome.Result == ResultSlowResponse || isHardFailure(outcome.Result) || outcome.Result == ResultNetworkFluctuation || outcome.Result == ResultRateLimited
	if protocolAware && !counted {
		return next, unchangedTransition(next)
	}
	eligible := protocolAware && policy.AutoDegradeEnabled && current.State != StateDisabled
	matched := healthEvidenceMatches(current, outcome.Protocol)
	if protocolAware {
		counterProtocol := TestProtocolChatCompletions
		if current.CounterProtocol != nil {
			counterProtocol = *current.CounterProtocol
		}
		if counterProtocol != outcome.Protocol {
			next.FailingSince = nil
			if eligible || policy.RuleVersion != "" {
				next.ConsecutiveFailures, next.ConsecutiveSuccesses = 0, 0
			}
		}
		if eligible || policy.RuleVersion != "" {
			next.CounterProtocol = protocolPointer(outcome.Protocol)
		}
		if eligible && !matched {
			next.HealthEvidenceStatus = HealthEvidenceInvalid
			next.HealthEvidenceProtocol = nil
		}
		if outcome.Result == ResultOK || outcome.Result == ResultSlowResponse {
			next.FailingSince = nil
		} else if next.FailingSince == nil {
			next.FailingSince = &now
		}
	}
	input := TransitionInput{Current: next.State, CurrentWeight: next.CurrentWeight, ConsecutiveFailures: next.ConsecutiveFailures, ConsecutiveSuccesses: next.ConsecutiveSuccesses, CooldownUntil: next.CooldownUntil, ObservingUntil: next.ObservingUntil, Now: now, Result: outcome.Result, Policy: policy}
	transitionOut := Transition(input)
	if v2 {
		transitionOut = transitionV2(input)
	}
	if !policy.AutoDegradeEnabled {
		transitionOut = unchangedTransition(next)
	}
	next.State, next.CurrentWeight = transitionOut.NextState, transitionOut.Weight
	next.ConsecutiveFailures, next.ConsecutiveSuccesses = transitionOut.ConsecutiveFailures, transitionOut.ConsecutiveSuccesses
	next.CooldownUntil, next.ObservingUntil = transitionOut.CooldownUntil, transitionOut.ObservingUntil
	if v2 && policy.AutoDegradeEnabled && current.State == StateHealthy && next.State == StateSuspect {
		next.RecheckPending = true
	}
	if outcome.Result == ResultOK || outcome.Result == ResultSlowResponse {
		next.LastSuccessAt, next.LastSuccessLatencyMs = &now, intPtr(outcome.LatencyMs)
		next.LastFirstTokenMs, next.LastFirstEventMs = outcome.FirstTokenMs, outcome.FirstEventMs
		next.LastErrorKey, next.LastErrorDetail = "", ""
	} else {
		next.LastFailureAt = &now
		next.LastErrorKey, next.LastErrorDetail = string(outcome.Result), outcome.Detail
	}
	if protocolAware {
		next.LastAppliedProbeAt = &now
		result := string(outcome.Result)
		next.LastAppliedProbeResult = &result
		next.LastAppliedProbeProtocol = protocolPointer(outcome.Protocol)
		if outcome.Result == ResultOK || outcome.Result == ResultSlowResponse {
			next.LastSuccessProtocol = protocolPointer(outcome.Protocol)
		}
		if eligible {
			valid := matched || (isHardFailure(outcome.Result) && next.State == StateSuspended) ||
				(isSoftFailure(outcome.Result) && (next.State == StateDegraded || (next.State == StateSuspended && next.ConsecutiveFailures >= failureThreshold(policy)))) ||
				(outcome.Result == ResultOK && (next.State == StateHealthy || next.State == StateRecovering)) ||
				(outcome.Result == ResultSlowResponse && next.State == StateDegraded)
			if v2 {
				valid = true
			}
			if valid {
				next.HealthEvidenceStatus = HealthEvidenceValid
				next.HealthEvidenceProtocol = protocolPointer(outcome.Protocol)
			}
		}
	}
	return next, transitionOut
}

func stateHasLongFailure(state ConnectionHealthState, policy Policy, now time.Time) bool {
	return state.FailingSince != nil && !now.Before(state.FailingSince.Add(time.Duration(RulePresetForPolicy(policy).LongFailureAfterSeconds)*time.Second))
}

func stepPercent(p Policy) int {
	if p.RecoveryStepPercent <= 0 {
		return 25
	}
	return p.RecoveryStepPercent
}

func successThreshold(p Policy) int {
	if p.SuccessThreshold <= 0 {
		return 2
	}
	return p.SuccessThreshold
}

func failureThreshold(p Policy) int {
	if p.FailureThreshold <= 0 {
		return 3
	}
	return p.FailureThreshold
}

func transitionOnSuccess(in TransitionInput, step int) TransitionOutput {
	out := TransitionOutput{
		ConsecutiveFailures:  0,
		ConsecutiveSuccesses: in.ConsecutiveSuccesses + 1,
	}

	switch in.Current {
	case StateHealthy:
		out.NextState = StateHealthy
		out.Weight = 100

	case StateDegraded:
		weight := minInt(100, in.CurrentWeight+step)
		if weight >= 100 {
			out.NextState = StateHealthy
			out.Weight = 100
		} else {
			out.NextState = StateDegraded
			out.Weight = weight
		}

	case StateSuspended:
		// 冷却后探活成功：进入 observing，权重从 0 起步观察，不立即恢复调用。
		observingUntil := in.Now.Add(observationWindow(in.Policy))
		out.NextState = StateObserving
		out.Weight = 0
		out.ObservingUntil = &observingUntil
		out.ConsecutiveSuccesses = 1

	case StateObserving:
		out.ObservingUntil = in.ObservingUntil
		// 观察期和连续成功阈值必须同时满足。旧数据可能没有 observing_until，
		// 此时只按成功阈值判断，保持升级前已进入 observing 的状态可继续恢复。
		observationFinished := in.ObservingUntil == nil || !in.Now.Before(*in.ObservingUntil)
		if observationFinished && out.ConsecutiveSuccesses >= successThreshold(in.Policy) {
			out.NextState = StateRecovering
			out.Weight = minInt(100, step)
			out.TriggerRemoteRestore = true
		} else {
			out.NextState = StateObserving
			out.Weight = in.CurrentWeight
		}

	case StateRecovering:
		weight := minInt(100, in.CurrentWeight+step)
		if weight >= 100 {
			out.NextState = StateHealthy
			out.Weight = 100
		} else {
			out.NextState = StateRecovering
			out.Weight = weight
		}
		out.TriggerRemoteRestore = true

	default:
		out.NextState = StateHealthy
		out.Weight = 100
	}

	return out
}

func transitionOnSoftFailure(in TransitionInput, step int) TransitionOutput {
	out := TransitionOutput{
		ConsecutiveSuccesses: 0,
		ConsecutiveFailures:  in.ConsecutiveFailures + 1,
	}

	switch in.Current {
	case StateHealthy:
		out.NextState = StateDegraded
		out.Weight = maxInt(0, 100-step)

	case StateDegraded, StateObserving, StateRecovering:
		if out.ConsecutiveFailures >= failureThreshold(in.Policy) {
			cooldownUntil := in.Now.Add(cooldownWindow(in.Policy))
			out.NextState = StateSuspended
			out.Weight = 0
			out.CooldownUntil = &cooldownUntil
			out.TriggerRemoteDegrade = true
		} else {
			out.NextState = StateDegraded
			out.Weight = maxInt(0, in.CurrentWeight-step)
		}

	case StateSuspended:
		cooldownUntil := in.Now.Add(cooldownWindow(in.Policy))
		out.NextState = StateSuspended
		out.Weight = 0
		out.CooldownUntil = &cooldownUntil

	default:
		out.NextState = StateDegraded
		out.Weight = maxInt(0, 100-step)
	}

	return out
}

func transitionOnHardFailure(in TransitionInput) TransitionOutput {
	cooldownUntil := in.Now.Add(cooldownWindow(in.Policy))
	return TransitionOutput{
		NextState:            StateSuspended,
		Weight:               0,
		ConsecutiveFailures:  in.ConsecutiveFailures + 1,
		ConsecutiveSuccesses: 0,
		CooldownUntil:        &cooldownUntil,
		TriggerRemoteDegrade: in.Current != StateSuspended,
	}
}

func observationWindow(p Policy) time.Duration {
	if p.ObservationSeconds <= 0 {
		return 300 * time.Second
	}
	return time.Duration(p.ObservationSeconds) * time.Second
}

func cooldownWindow(p Policy) time.Duration {
	if p.CooldownSeconds <= 0 {
		return 300 * time.Second
	}
	return time.Duration(p.CooldownSeconds) * time.Second
}

// ProbeBackoff 按连续失败次数返回下一次探活前的退避时长：2、5、10 分钟，超过后维持 10 分钟。
func ProbeBackoff(consecutiveFailures int) time.Duration {
	switch {
	case consecutiveFailures <= 0:
		return 0
	case consecutiveFailures == 1:
		return 2 * time.Minute
	case consecutiveFailures == 2:
		return 5 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

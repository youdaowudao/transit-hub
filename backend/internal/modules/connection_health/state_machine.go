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

// isHardFailure 分类：5xx、认证失败、模型不存在需要进入异常确认，
// 但单次观测不能直接暂停账号或触发远程写入。
func isHardFailure(result ResultKey) bool {
	switch result {
	case ResultServerError, ResultAuth, ResultModelNotFound:
		return true
	default:
		return false
	}
}

// isSoftFailure 分类：网络波动、响应无法解析会降低本地健康权重。
// 429 是调度信号，在 Transition 中单独处理，不归入失败累计。
func isSoftFailure(result ResultKey) bool {
	switch result {
	case ResultNetworkFluctuation, ResultInvalidResponse:
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
	case in.Result == ResultRateLimited:
		return transitionOnRateLimited(in)
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

// Rate limiting is a scheduling signal, not evidence that the account is dead.
// Keep the health state and failure counter unchanged so repeated 429 responses
// cannot walk the legacy soft-failure path into suspension or remote disable.
func transitionOnRateLimited(in TransitionInput) TransitionOutput {
	return TransitionOutput{
		NextState:            in.Current,
		Weight:               in.CurrentWeight,
		ConsecutiveFailures:  in.ConsecutiveFailures,
		ConsecutiveSuccesses: 0,
		CooldownUntil:        in.CooldownUntil,
		ObservingUntil:       in.ObservingUntil,
	}
}

func applyProbeOutcome(current ConnectionHealthState, outcome ProbeOutcome, policy Policy, now time.Time) (ConnectionHealthState, TransitionOutput) {
	return applyProbeOutcomeWithConfirmation(current, outcome, policy, now, true)
}

func applyProbeOutcomeWithConfirmation(current ConnectionHealthState, outcome ProbeOutcome, policy Policy, now time.Time, requireConfirmation bool) (ConnectionHealthState, TransitionOutput) {
	transitionInput := TransitionInput{
		Current: current.State, CurrentWeight: current.CurrentWeight,
		ConsecutiveFailures: current.ConsecutiveFailures, ConsecutiveSuccesses: current.ConsecutiveSuccesses,
		CooldownUntil: current.CooldownUntil, ObservingUntil: current.ObservingUntil,
		Now: now, Result: outcome.Result, Policy: policy,
	}
	transitionOut := Transition(transitionInput)
	if requireConfirmation && (isHardFailure(outcome.Result) || isSoftFailure(outcome.Result)) {
		transitionOut = transitionOnHardFailure(transitionInput)
	} else if !requireConfirmation {
		transitionOut = legacyTargetTransition(transitionInput)
	}
	if !policy.AutoDegradeEnabled {
		transitionOut = TransitionOutput{
			NextState: current.State, Weight: current.CurrentWeight,
			ConsecutiveFailures: current.ConsecutiveFailures, ConsecutiveSuccesses: current.ConsecutiveSuccesses,
			CooldownUntil: current.CooldownUntil, ObservingUntil: current.ObservingUntil,
		}
	}

	next := current
	next.State = transitionOut.NextState
	next.CurrentWeight = transitionOut.Weight
	next.ConsecutiveFailures = transitionOut.ConsecutiveFailures
	next.ConsecutiveSuccesses = transitionOut.ConsecutiveSuccesses
	next.CooldownUntil = transitionOut.CooldownUntil
	next.ObservingUntil = transitionOut.ObservingUntil
	if outcome.Result == ResultRateLimited {
		retryDelay := 30 * time.Second
		if outcome.RetryAfterSeconds > 0 {
			retryDelay = time.Duration(outcome.RetryAfterSeconds) * time.Second
		}
		retryAt := now.Add(retryDelay)
		if next.CooldownUntil == nil || next.CooldownUntil.Before(retryAt) {
			next.CooldownUntil = &retryAt
		}
	}
	next.LastProbeAt = &now
	latencyMs := outcome.LatencyMs
	next.LastLatencyMs = &latencyMs

	if outcome.Result == ResultOK || outcome.Result == ResultSlowResponse {
		next.LastSuccessAt = &now
		next.LastSuccessLatencyMs = &latencyMs
		next.LastErrorKey = ""
		next.LastErrorDetail = ""
	} else {
		next.LastFailureAt = &now
		next.LastErrorKey = string(outcome.Result)
		next.LastErrorDetail = outcome.Detail
	}
	return next, transitionOut
}

func legacyTargetTransition(in TransitionInput) TransitionOutput {
	if isHardFailure(in.Result) {
		cooldownUntil := in.Now.Add(cooldownWindow(in.Policy))
		return TransitionOutput{
			NextState: StateSuspended, Weight: 0,
			ConsecutiveFailures: in.ConsecutiveFailures + 1, ConsecutiveSuccesses: 0,
			CooldownUntil: &cooldownUntil, TriggerRemoteDegrade: in.Current != StateSuspended,
		}
	}
	if isSoftFailure(in.Result) && in.Current != StateHealthy && in.Current != StateSuspended {
		failures := in.ConsecutiveFailures + 1
		if failures >= failureThreshold(in.Policy) {
			cooldownUntil := in.Now.Add(cooldownWindow(in.Policy))
			return TransitionOutput{
				NextState: StateSuspended, Weight: 0,
				ConsecutiveFailures: failures, ConsecutiveSuccesses: 0,
				CooldownUntil: &cooldownUntil, TriggerRemoteDegrade: true,
			}
		}
	}
	return Transition(in)
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
		// 普通传输波动可以降低本地健康权重，破坏性动作则由
		// 持久化确认、熔断和存活底线共同裁决，本地阈值不得触发远程写。
		out.NextState = StateDegraded
		out.Weight = maxInt(1, in.CurrentWeight-step)

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
	// Hard failures are observations until the persistent confirmation worker
	// completes floor reservation, remote mutation, and readback. Keeping the
	// state/weight unchanged also prevents the normal priority loop from turning
	// a single unconfirmed response into an upstream priority mutation.
	return TransitionOutput{
		NextState:            in.Current,
		Weight:               in.CurrentWeight,
		ConsecutiveFailures:  in.ConsecutiveFailures + 1,
		ConsecutiveSuccesses: 0,
		CooldownUntil:        in.CooldownUntil,
		ObservingUntil:       in.ObservingUntil,
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

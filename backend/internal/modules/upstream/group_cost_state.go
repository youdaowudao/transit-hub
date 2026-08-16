package upstream

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	GroupCostModeExact    = "exact"
	GroupCostModeRetained = "retained"
	GroupCostModeUnknown  = "unknown"

	GroupCostSourceSummary     = "group_summary"
	GroupCostSourceKeyFallback = "key_fallback"
)

// GroupCostSamplingState 保留采样失败原因和退避窗口。它只影响成本观测请求，
// 不参与上游站点健康状态、Priority 或 schedulable 判断。
type GroupCostSamplingState struct {
	LastAttemptAt           time.Time `json:"lastAttemptAt,omitempty"`
	LastSuccessAt           time.Time `json:"lastSuccessAt,omitempty"`
	LastAttemptStatus       string    `json:"lastAttemptStatus,omitempty"`
	LastReason              string    `json:"lastReason,omitempty"`
	SummaryNextAllowedAt    time.Time `json:"summaryNextAllowedAt,omitempty"`
	FallbackNextAllowedAt   time.Time `json:"fallbackNextAllowedAt,omitempty"`
	SummaryAuthFailures     int       `json:"summaryAuthFailures,omitempty"`
	SummaryNetworkFailures  int       `json:"summaryNetworkFailures,omitempty"`
	FallbackAuthFailures    int       `json:"fallbackAuthFailures,omitempty"`
	FallbackNetworkFailures int       `json:"fallbackNetworkFailures,omitempty"`
}

type groupCostCooldownError struct{}

func (groupCostCooldownError) Error() string { return "group cost sampling is in backoff" }

func isGroupCostCooldown(err error) bool {
	var cooldown groupCostCooldownError
	return errors.As(err, &cooldown)
}

// groupCostFetchError 标识一次失败发生在哪条采样链路。汇总失败后，Key 回退
// 可以成功；这时调用方仍需保留汇总链路的独立退避窗口。
type groupCostFetchError struct {
	err                 error
	path                string
	summaryFailureClass string
}

func (e *groupCostFetchError) Error() string { return e.err.Error() }
func (e *groupCostFetchError) Unwrap() error { return e.err }

func groupCostFetchFailure(err error) (path, summaryFailureClass string) {
	var fetchErr *groupCostFetchError
	if errors.As(err, &fetchErr) {
		return fetchErr.path, fetchErr.summaryFailureClass
	}
	return "summary", ""
}

func (s GroupCostSamplingState) summaryAllowed(now time.Time) bool {
	return s.SummaryNextAllowedAt.IsZero() || !now.Before(s.SummaryNextAllowedAt)
}

func (s GroupCostSamplingState) fallbackAllowed(now time.Time) bool {
	return s.FallbackNextAllowedAt.IsZero() || !now.Before(s.FallbackNextAllowedAt)
}

func (s *GroupCostSamplingState) recordSuccess(now time.Time) {
	s.LastSuccessAt = now
	s.LastAttemptStatus = "ok"
	s.LastReason = ""
}

func (s *GroupCostSamplingState) RecordSummarySuccess(now time.Time) {
	s.recordSuccess(now)
	s.SummaryAuthFailures = 0
	s.SummaryNetworkFailures = 0
	s.SummaryNextAllowedAt = now.Add(groupCostSampleInterval)
}

func (s *GroupCostSamplingState) RecordFallbackSuccess(now time.Time) {
	s.recordSuccess(now)
	s.FallbackAuthFailures = 0
	s.FallbackNetworkFailures = 0
	s.FallbackNextAllowedAt = now.Add(groupCostFallbackInterval)
}

func (s *GroupCostSamplingState) recordFailure(class string, now time.Time, authFailures, networkFailures *int, nextAllowedAt *time.Time) {
	s.LastAttemptStatus = "failed"
	s.LastReason = class
	backoff := groupCostBackoff(class, *authFailures, *networkFailures)
	*nextAllowedAt = now.Add(backoff)
	if groupCostPersistentFailure(class) {
		(*authFailures)++
		return
	}
	if class == "network" {
		(*networkFailures)++
	}
}

func (s *GroupCostSamplingState) RecordSummaryFailure(class string, now time.Time) {
	s.recordFailure(class, now, &s.SummaryAuthFailures, &s.SummaryNetworkFailures, &s.SummaryNextAllowedAt)
}

func (s *GroupCostSamplingState) RecordFallbackFailure(class string, now time.Time) {
	s.recordFailure(class, now, &s.FallbackAuthFailures, &s.FallbackNetworkFailures, &s.FallbackNextAllowedAt)
}

func groupCostBackoff(class string, authFailures, networkFailures int) time.Duration {
	base := 5 * time.Minute
	max := 30 * time.Minute
	count := networkFailures
	if groupCostPersistentFailure(class) {
		base = 30 * time.Minute
		max = 6 * time.Hour
		count = authFailures
	}
	if count < 0 {
		count = 0
	}
	backoff := base
	for i := 0; i < count && backoff < max; i++ {
		backoff *= 2
		if backoff > max {
			return max
		}
	}
	return backoff
}

func groupCostPersistentFailure(class string) bool {
	return class == "auth_401" || class == "auth_403" || class == "invalid_response"
}

func groupCostFailureClass(err error) string {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		switch requestErr.StatusCode {
		case http.StatusUnauthorized:
			return "auth_401"
		case http.StatusForbidden:
			return "auth_403"
		}
		switch requestErr.MessageKey {
		case ErrorAuth:
			return "auth"
		case ErrorNetwork:
			return "network"
		case ErrorInvalidResponse:
			return "invalid_response"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "network"
	}
	return "request_failed"
}

func groupCostSnapshotMode(observedAt, now time.Time) string {
	if now.Sub(observedAt) <= groupCostMaxAge {
		return GroupCostModeExact
	}
	return GroupCostModeRetained
}

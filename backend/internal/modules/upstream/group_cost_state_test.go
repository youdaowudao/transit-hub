package upstream

import (
	"testing"
	"time"
)

func TestGroupCostSamplingStateKeepsSummaryBackoffAfterFallbackSuccess(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var state GroupCostSamplingState
	state.RecordSummaryFailure("auth_403", now)
	summaryBackoff := state.SummaryNextAllowedAt
	state.RecordFallbackSuccess(now.Add(time.Minute))

	if !state.SummaryNextAllowedAt.Equal(summaryBackoff) || state.SummaryAuthFailures != 1 {
		t.Fatalf("fallback success must not clear summary backoff: %+v", state)
	}
	if state.FallbackNextAllowedAt.Before(now.Add(time.Minute + groupCostFallbackInterval)) {
		t.Fatalf("fallback success must throttle only fallback path: %+v", state)
	}
}

func TestGroupCostSamplingStateUsesLongBackoffForInvalidFallbackResponse(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var state GroupCostSamplingState
	state.RecordFallbackFailure("invalid_response", now)

	if got := state.FallbackNextAllowedAt.Sub(now); got != 30*time.Minute || state.FallbackAuthFailures != 1 {
		t.Fatalf("invalid fallback response must use persistent backoff: delay=%s state=%+v", got, state)
	}
}

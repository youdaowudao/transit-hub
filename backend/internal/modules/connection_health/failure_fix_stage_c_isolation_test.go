package connection_health

import (
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestFailureFixStageCFailureHistoryIsLocalAndIgnoresObsoleteCompletions(t *testing.T) {
	first := &multiplierSnapshotEntry{workspaceKey: "fixture-one", generation: 1}
	second := &multiplierSnapshotEntry{workspaceKey: "fixture-two", generation: 1}
	service := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{first.workspaceKey: first, second.workspaceKey: second}}
	failed := &upstream.RequestError{MessageKey: upstream.ErrorAuth, StatusCode: 401}
	for range 2 {
		captured := *first
		service.finishMultiplierSnapshot(first, &captured, nil, nil, multiplierSiteMetadata{}, failed)
	}
	failureFixRetryInterval(t, first, 15*time.Minute)
	capturedSecond := *second
	service.finishMultiplierSnapshot(second, &capturedSecond, nil, nil, multiplierSiteMetadata{}, failed)
	failureFixRetryInterval(t, second, 5*time.Minute)
	// An old job finishing after the entry replacement must not advance the new
	// binding's failure history or leave a retry deadline on it.
	obsolete := *first
	replacement := &multiplierSnapshotEntry{workspaceKey: first.workspaceKey, generation: 2}
	service.multiplierSnapshots[first.workspaceKey] = replacement
	service.finishMultiplierSnapshot(first, &obsolete, nil, nil, multiplierSiteMetadata{}, failed)
	if !replacement.nextRetryAt.IsZero() {
		t.Error("obsolete completion contaminated the replacement retry state")
	}
	capturedReplacement := *replacement
	service.finishMultiplierSnapshot(replacement, &capturedReplacement, nil, nil, multiplierSiteMetadata{}, failed)
	failureFixRetryInterval(t, replacement, 5*time.Minute)
}

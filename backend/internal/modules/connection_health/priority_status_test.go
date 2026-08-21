package connection_health

import (
	"context"
	"testing"
	"time"
)

func TestPrioritySyncStatus_HealthFailureWithoutPendingSignatureIsFailed(t *testing.T) {
	repo := newFakeRepository()
	failureAt := time.Now().Add(-time.Minute)
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", PendingSignature: "",
		LastDecision: "failed", LastError: ErrorPriorityMetadataUnavailable,
		PendingTargetCount: 3, LastReconcileFailureAt: &failureAt,
	}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}}

	status, err := service.PrioritySyncStatus(context.Background(), "user1")
	if err != nil {
		t.Fatalf("priority status failed: %v", err)
	}
	if status.Status != "failed" || status.ErrorKey != ErrorPriorityMetadataUnavailable || status.FailedCount != 3 || status.LastFailureAt == nil || !status.LastFailureAt.Equal(failureAt) {
		t.Fatalf("health failure must remain visible without a pending signature: %+v", status)
	}
}

func TestPrioritySyncStatus_PartialGenerationRemainsVisibleAndRetryable(t *testing.T) {
	repo := newFakeRepository()
	failureAt := time.Now().Add(-time.Minute)
	retryAt := time.Now().Add(time.Minute)
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", PendingSignature: "generation-1",
		LastDecision: "partial", LastError: ErrorPriorityMetadataUnavailable,
		PendingTargetCount: 2, LastReconcileFailureAt: &failureAt, NextReconcileAt: &retryAt,
	}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}}

	status, err := service.PrioritySyncStatus(context.Background(), "user1")
	if err != nil {
		t.Fatalf("priority status failed: %v", err)
	}
	if status.Status != "partial" || status.ErrorKey != ErrorPriorityMetadataUnavailable || status.FailedCount != 2 ||
		status.LastFailureAt == nil || !status.LastFailureAt.Equal(failureAt) {
		t.Fatalf("partial generation must remain visible with its blocker count: %+v", status)
	}
}

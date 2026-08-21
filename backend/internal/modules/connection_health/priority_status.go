package connection_health

import (
	"context"
	"time"
)

type PrioritySyncStatusView struct {
	WorkspaceID   string     `json:"workspaceId"`
	Status        string     `json:"status"`
	ErrorKey      string     `json:"errorKey,omitempty"`
	PendingSince  *time.Time `json:"pendingSince,omitempty"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastFailureAt *time.Time `json:"lastFailureAt,omitempty"`
	FailedCount   int        `json:"failedCount"`
}

func (s *Service) PrioritySyncStatus(ctx context.Context, userID string) (PrioritySyncStatusView, error) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return PrioritySyncStatusView{}, err
	}
	view := PrioritySyncStatusView{WorkspaceID: adminAccountID, Status: "idle"}
	state, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
	if err != nil || state == nil {
		return view, err
	}
	view.PendingSince = state.PendingSince
	view.LastAttemptAt = state.LastReconcileAttemptAt
	view.LastFailureAt = state.LastReconcileFailureAt
	view.FailedCount = state.PendingTargetCount
	if state.PendingSignature == "" {
		switch state.LastDecision {
		case "failed":
			view.Status = "failed"
			view.ErrorKey = state.LastError
		case "partial":
			view.Status = "partial"
			view.ErrorKey = state.LastError
		case "success":
			view.Status = "success"
		}
		return view, nil
	}
	switch state.LastDecision {
	case "failed":
		view.Status = "failed"
		view.ErrorKey = state.LastError
	case "partial":
		view.Status = "partial"
		view.ErrorKey = state.LastError
	case "running":
		view.Status = "running"
	default:
		view.Status = "pending"
	}
	return view, nil
}

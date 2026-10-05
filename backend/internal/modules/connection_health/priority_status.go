package connection_health

import (
	"context"
	"sort"
	"time"
)

type PrioritySyncStatusView struct {
	FailedTargets     []PriorityFailedTarget   `json:"failedTargets,omitempty"`
	ActionDiagnostics []RemoteActionDiagnostic `json:"actionDiagnostics,omitempty"`
	WorkspaceID       string                   `json:"workspaceId"`
	Status            string                   `json:"status"`
	ErrorKey          string                   `json:"errorKey,omitempty"`
	PendingSince      *time.Time               `json:"pendingSince,omitempty"`
	LastAttemptAt     *time.Time               `json:"lastAttemptAt,omitempty"`
	LastFailureAt     *time.Time               `json:"lastFailureAt,omitempty"`
	FailedCount       int                      `json:"failedCount"`
}

type PriorityFailedTarget struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName,omitempty"`
	Reason      string `json:"reason"`
}

func (s *Service) PrioritySyncStatus(ctx context.Context, userID string) (PrioritySyncStatusView, error) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return PrioritySyncStatusView{}, err
	}
	view := PrioritySyncStatusView{WorkspaceID: adminAccountID, Status: "idle"}
	priorityStates, err := s.repo.ListPrioritySyncStates(ctx, userID, adminAccountID)
	if err != nil {
		return view, err
	}
	targetStates, err := s.repo.ListTargetActionStates(ctx, userID, adminAccountID)
	if err != nil {
		return view, err
	}
	checkpoints := s.actionCheckpointsByAccount(userID, adminAccountID, priorityStates, targetStates)
	inventory := s.completeActionInventory(userID, adminAccountID)
	view.ActionDiagnostics = s.actionAccountDiagnostics(userID, adminAccountID, checkpoints, inventory, s.actionTime(), false)
	sort.Slice(view.ActionDiagnostics, func(i, j int) bool {
		left, right := view.ActionDiagnostics[i], view.ActionDiagnostics[j]
		if left.TargetID != right.TargetID {
			return left.TargetID < right.TargetID
		}
		if left.Action != right.Action {
			return left.Action < right.Action
		}
		return left.Reason < right.Reason
	})
	state, failedTargets, err := func() (*PriorityWorkspaceSyncState, []priorityTargetFailure, error) {
		lock := s.priorityStatusLock(userID, adminAccountID)
		lock.Lock()
		defer lock.Unlock()
		state, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
		var targets []priorityTargetFailure
		if stored, exists := s.priorityFailureTargets.Load(priorityRuntimeLeaseKey(userID, adminAccountID)); exists {
			targets = stored.([]priorityTargetFailure)
		}
		return state, targets, err
	}()
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
		s.fillPriorityFailedTargets(&view, failedTargets, inventory, s.actionSweepViewFor(userID, adminAccountID))
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
	s.fillPriorityFailedTargets(&view, failedTargets, inventory, s.actionSweepViewFor(userID, adminAccountID))
	return view, nil
}

func (s *Service) fillPriorityFailedTargets(view *PrioritySyncStatusView, targets []priorityTargetFailure, inventory *actionInventoryView, sweep *actionSweepView) {
	if view.Status != "failed" || len(targets) == 0 {
		return
	}
	if len(targets) > 10 {
		targets = targets[:10]
	}
	view.FailedTargets = make([]PriorityFailedTarget, 0, len(targets))
	for _, target := range targets {
		name := ""
		if inventory != nil {
			name = inventory.names[target.accountID]
		}
		if name == "" && sweep != nil {
			name = sweep.conclusions[target.accountID].accountName
		}
		view.FailedTargets = append(view.FailedTargets, PriorityFailedTarget{AccountID: target.accountID, AccountName: name, Reason: target.reason})
	}
}

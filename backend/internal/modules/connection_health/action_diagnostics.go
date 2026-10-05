package connection_health

import (
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type RemoteActionPendingView struct {
	Action     string              `json:"action"`
	DispatchID string              `json:"dispatchId,omitempty"`
	Phase      RemoteDispatchPhase `json:"phase"`
	Reason     string              `json:"reason"`
	Source     string              `json:"source,omitempty"`
}

type RemoteActionDiagnostic struct {
	AccountID   string     `json:"accountId"`
	AccountName string     `json:"accountName,omitempty"`
	ObservedAt  *time.Time `json:"observedAt,omitempty"`
	TargetID    string     `json:"targetId"`
	RemoteActionPendingView
}

// Every persisted alias participates in the account's display decision.
// These slices are read-only projections, never dispatch or Priority evidence.
type actionAccountCheckpoints struct {
	priorities []*PrioritySyncState
	targets    []*TargetActionState
}

func actionPendingView(priority *PrioritySyncState, target *TargetActionState, now time.Time) *RemoteActionPendingView {
	return actionAccountPendingView(actionAccountCheckpoints{
		priorities: []*PrioritySyncState{priority}, targets: []*TargetActionState{target},
	}, now)
}

func actionAccountPendingView(checkpoints actionAccountCheckpoints, now time.Time) *RemoteActionPendingView {
	type pendingRecord struct {
		view      RemoteActionPendingView
		conflict  bool
		legacy    bool
		updatedAt time.Time
	}
	pending := make([]pendingRecord, 0, len(checkpoints.priorities)+len(checkpoints.targets))
	for _, state := range checkpoints.priorities {
		if !priorityActionPending(state) {
			continue
		}
		pending = append(pending, pendingRecord{
			view:     RemoteActionPendingView{Action: ActionKindPriority, DispatchID: state.PendingDispatchID, Phase: state.PendingDispatchPhase},
			conflict: state.Conflict, legacy: state.PendingDispatchID == "" || state.PendingOwnerID == "" || state.PendingDispatchPhase == "", updatedAt: state.UpdatedAt,
		})
	}
	for _, state := range checkpoints.targets {
		if !targetActionPending(state) {
			continue
		}
		action := state.PendingActionKind
		if action == "" {
			action = ActionKindTarget
		}
		pending = append(pending, pendingRecord{
			view:     RemoteActionPendingView{Action: action, DispatchID: state.PendingDispatchID, Phase: state.PendingDispatchPhase, Source: state.PendingSource},
			conflict: state.Conflict, legacy: state.PendingDispatchID == "" || state.PendingOwnerID == "" || state.PendingDispatchPhase == "", updatedAt: state.UpdatedAt,
		})
	}
	viewFor := func(record pendingRecord, reason string) *RemoteActionPendingView {
		view := record.view
		view.Reason = reason
		return &view
	}
	// Apply the specified precedence across all rows, including older aliases.
	for _, record := range pending {
		if record.conflict {
			return viewFor(record, "conflict")
		}
	}
	for _, record := range pending {
		if record.view.Phase == DispatchUncertain {
			return viewFor(record, "uncertain")
		}
	}
	for _, record := range pending {
		if record.legacy {
			return viewFor(record, "legacy")
		}
	}
	if len(pending) > 1 {
		return &RemoteActionPendingView{Action: "priority_and_target", Phase: DispatchUncertain, Reason: "dual_claim"}
	}
	for _, record := range pending {
		switch record.view.Phase {
		case DispatchPrepared, DispatchSending, DispatchConfirmedApplied, DispatchNotSent, DispatchConfirmedRejected:
			if now.Sub(record.updatedAt) > actionConfirmationWindow {
				return viewFor(record, "overdue")
			}
		}
	}
	return nil
}

func (s *Service) actionCheckpointsByAccount(userID, workspace string, priorities []PrioritySyncState, targets []TargetActionState) map[string]actionAccountCheckpoints {
	accounts := make(map[string]actionAccountCheckpoints)
	accountID := func(stateUser, stateWorkspace, targetID string) (string, bool) {
		if stateUser != userID || stateWorkspace != workspace || !isSub2APIActionTarget(targetID) {
			return "", false
		}
		id, valid := scopedActionAccountID(targetID, workspace)
		if !valid {
			s.logInvisibleAction(RemoteActionScope{userID, workspace, targetID})
		}
		return id, valid
	}
	for _, state := range priorities {
		if id, valid := accountID(state.UserID, state.AdminAccountID, state.TargetID); valid {
			checkpoints := accounts[id]
			copy := state
			checkpoints.priorities = append(checkpoints.priorities, &copy)
			accounts[id] = checkpoints
		}
	}
	for _, state := range targets {
		if id, valid := accountID(state.UserID, state.AdminAccountID, state.TargetID); valid {
			checkpoints := accounts[id]
			copy := state
			checkpoints.targets = append(checkpoints.targets, &copy)
			accounts[id] = checkpoints
		}
	}
	return accounts
}

type actionInventoryView struct {
	complete   bool
	visible    map[string]bool
	names      map[string]string
	observedAt time.Time
}

func (s *Service) completeActionInventory(userID, workspace string) *actionInventoryView {
	stored, exists := s.actionInventoryViews.Load(priorityRuntimeLeaseKey(userID, workspace))
	if !exists {
		return nil
	}
	return stored.(*actionInventoryView)
}

// This immutable projection remembers only successful complete grouped reads.
// It is never used as dispatch evidence or to request another grouped read.
func (s *Service) rememberActionInventory(userID, workspace string, inventory *adminWorkspaceInventory) {
	if inventory == nil || inventory.session.Platform != upstream.PlatformSub2API || !adminInventoryComplete(*inventory) {
		return
	}
	view := actionInventoryView{complete: true, visible: make(map[string]bool), names: make(map[string]string), observedAt: inventory.snapshotStartedAt}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			id := strings.TrimSpace(account.ID)
			view.visible[buildTargetID(string(upstream.PlatformSub2API), workspace, id)] = true
			view.names[id] = account.Name
		}
	}
	key := priorityRuntimeLeaseKey(userID, workspace)
	for {
		previous, loaded := s.actionInventoryViews.LoadOrStore(key, &view)
		if !loaded {
			break
		}
		old := previous.(*actionInventoryView)
		if !view.observedAt.After(old.observedAt) {
			return
		}
		if s.actionInventoryViews.CompareAndSwap(key, old, &view) {
			break
		}
	}
	// Reset even when a pending action would make restoration return early.
	s.actionInvisibleLogs.Range(func(key, _ any) bool {
		scope := key.(RemoteActionScope)
		if scope.UserID == userID && scope.AdminAccountID == workspace {
			if id, valid := scopedActionAccountID(scope.TargetID, workspace); valid {
				if _, visible := view.names[id]; visible {
					s.actionInvisibleLogs.Delete(scope)
				}
			}
		}
		return true
	})
	s.removeVisibleActionConclusions(userID, workspace, view.names)
}

func (s *Service) invisibleActionDiagnostics(userID, workspace string, pairs map[string]RemoteActionCheckpoints) []RemoteActionDiagnostic {
	return s.invisibleActionDiagnosticsForInventory(userID, workspace, pairs, s.completeActionInventory(userID, workspace), s.actionTime())
}

func (s *Service) invisibleActionDiagnosticsForInventory(userID, workspace string, pairs map[string]RemoteActionCheckpoints, view *actionInventoryView, now time.Time) []RemoteActionDiagnostic {
	accounts := make(map[string]actionAccountCheckpoints)
	for targetID, pair := range pairs {
		id, valid := scopedActionAccountID(targetID, workspace)
		if !valid {
			s.logInvisibleAction(RemoteActionScope{userID, workspace, targetID})
			continue
		}
		checkpoints := accounts[id]
		if pair.Priority != nil {
			checkpoints.priorities = append(checkpoints.priorities, pair.Priority)
		}
		if pair.Target != nil {
			checkpoints.targets = append(checkpoints.targets, pair.Target)
		}
		accounts[id] = checkpoints
	}
	return s.actionAccountDiagnostics(userID, workspace, accounts, view, now, true)
}

func (s *Service) actionAccountDiagnostics(userID, workspace string, accounts map[string]actionAccountCheckpoints, inventory *actionInventoryView, now time.Time, onlyInvisible bool) []RemoteActionDiagnostic {
	sweep := s.actionSweepViewFor(userID, workspace)
	out := []RemoteActionDiagnostic{}
	for id, checkpoints := range accounts {
		visible, name := true, ""
		if inventory != nil {
			name, visible = inventory.names[id]
		}
		pending := actionAccountPendingView(checkpoints, now)
		diagnostic := RemoteActionDiagnostic{TargetID: buildTargetID("sub2api", workspace, id), AccountID: id}
		if visible {
			if onlyInvisible || pending == nil {
				continue
			}
			diagnostic.AccountName, diagnostic.RemoteActionPendingView = name, *pending
			out = append(out, diagnostic)
			continue
		}
		diagnostic.ObservedAt = &inventory.observedAt
		if sweep != nil && sweep.departed[id] {
			if conclusion, confirmed := sweep.conclusions[id]; confirmed {
				if conclusion.deleted {
					continue
				}
				diagnostic.Reason, diagnostic.AccountName = "target_not_visible", conclusion.accountName
			}
		}
		if diagnostic.Reason == "" {
			if pending != nil {
				diagnostic.RemoteActionPendingView = *pending
				diagnostic.Reason = "target_unverified"
			} else if sweep != nil && (sweep.failures >= 3 || sweep.unavailable) {
				diagnostic.Reason = "inventory_unavailable"
			} else {
				continue
			}
		}
		out = append(out, diagnostic)
	}
	return out
}

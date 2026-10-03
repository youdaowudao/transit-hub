package connection_health

import (
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
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	TargetID   string     `json:"targetId"`
	RemoteActionPendingView
}

func actionPendingView(priority *PrioritySyncState, target *TargetActionState) *RemoteActionPendingView {
	pair := RemoteActionCheckpoints{Priority: priority, Target: target}
	if pair.pendingCount() == 0 {
		return nil
	}
	if pair.pendingCount() > 1 {
		return &RemoteActionPendingView{Action: "priority_and_target", Phase: DispatchUncertain, Reason: "dual_claim"}
	}
	kind := ActionKindPriority
	if targetActionPending(target) {
		kind = ActionKindTarget
	}
	id, owner, phase, _ := pair.dispatch(kind)
	reason := "pending"
	if id == "" || owner == "" || phase == "" {
		phase = DispatchUncertain
		reason = "legacy"
	}
	view := &RemoteActionPendingView{Action: kind, DispatchID: id, Phase: phase, Reason: reason}
	if kind == ActionKindTarget && target != nil {
		if target.PendingActionKind != "" {
			view.Action = target.PendingActionKind
		}
		view.Source = target.PendingSource
	}
	return view
}

type actionInventoryView struct {
	complete   bool
	visible    map[string]bool
	observedAt time.Time
}

// This is a read-only projection of the existing refresh, never a source for
// dispatch or a reason to start another inventory request.
func (s *Service) rememberActionInventory(userID, workspace string, inventory *adminWorkspaceInventory) {
	if inventory == nil || inventory.session.Platform != upstream.PlatformSub2API {
		return
	}
	view := actionInventoryView{complete: adminInventoryComplete(*inventory), visible: make(map[string]bool), observedAt: inventory.snapshotStartedAt}
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			view.visible[buildTargetID(string(upstream.PlatformSub2API), workspace, account.ID)] = true
		}
	}
	key := priorityRuntimeLeaseKey(userID, workspace)
	for {
		previous, loaded := s.actionInventoryViews.LoadOrStore(key, &view)
		if !loaded {
			return
		}
		old := previous.(*actionInventoryView)
		if !view.observedAt.After(old.observedAt) {
			return
		}
		if s.actionInventoryViews.CompareAndSwap(key, old, &view) {
			return
		}
	}
}

func (s *Service) invisibleActionDiagnostics(userID, workspace string, pairs map[string]RemoteActionCheckpoints) []RemoteActionDiagnostic {
	stored, exists := s.actionInventoryViews.Load(priorityRuntimeLeaseKey(userID, workspace))
	if !exists {
		return nil
	}
	view := stored.(*actionInventoryView)
	if !view.complete {
		return nil
	}
	out := []RemoteActionDiagnostic{}
	for targetID, pair := range pairs {
		if view.visible[targetID] {
			continue
		}
		for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
			if (kind == ActionKindPriority && pair.Priority == nil) || (kind == ActionKindTarget && pair.Target == nil) {
				continue
			}
			id, _, phase, _ := pair.dispatch(kind)
			out = append(out, RemoteActionDiagnostic{TargetID: targetID, RemoteActionPendingView: RemoteActionPendingView{Action: kind, DispatchID: id, Phase: phase, Reason: "target_not_visible"}, ObservedAt: &view.observedAt})
		}
	}
	return out
}

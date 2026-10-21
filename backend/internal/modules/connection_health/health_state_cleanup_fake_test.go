package connection_health

import (
	"context"
	"sort"
)

func (f *fakeRepository) ListSub2APIHealthWorkspaces(ctx context.Context) ([]healthStateWorkspace, error) {
	f.dataMu.Lock()
	defer f.dataMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seen := make(map[healthStateWorkspace]bool)
	for targetID, models := range f.states {
		parsed, valid := parseTargetID(targetID)
		if !valid || parsed.platform != "sub2api" {
			continue
		}
		for _, state := range models {
			seen[healthStateWorkspace{state.UserID, state.AdminAccountID}] = true
		}
	}
	workspaces := make([]healthStateWorkspace, 0, len(seen))
	for workspace := range seen {
		workspaces = append(workspaces, workspace)
	}
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].userID != workspaces[j].userID {
			return workspaces[i].userID < workspaces[j].userID
		}
		return workspaces[i].adminAccountID < workspaces[j].adminAccountID
	})
	return workspaces, nil
}

func (f *fakeRepository) AbandonConflictedTargetAction(ctx context.Context, scope RemoteActionScope, expectedGeneration int64, event ConnectionHealthEvent) (bool, error) {
	parsed, valid := parseTargetID(scope.TargetID)
	if !valid || parsed.platform != "sub2api" || parsed.adminAccountID != scope.AdminAccountID {
		return false, nil
	}
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	f.dataMu.Lock()
	defer f.dataMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f.fakeWorkspaceHealthSettings(scope.UserID, scope.AdminAccountID).ConfigGeneration != expectedGeneration {
		return false, nil
	}
	key := scope.UserID + "|" + scope.AdminAccountID + "|" + scope.TargetID
	stored, exists := f.targetActionStates[key]
	priority := f.priorityStates[key]
	if !exists || !stored.Conflict || stored.PendingDispatchPhase != "" || stored.PendingStatus != "" || priority.PendingDispatchPhase != "" || priority.PendingPriority != nil {
		return false, nil
	}
	if f.insertEventErr != nil {
		return false, f.insertEventErr
	}
	event.UserID, event.AdminAccountID, event.ConnectionID = scope.UserID, scope.AdminAccountID, scope.TargetID
	event.Result = "policy_unmanaged_abandoned"
	f.events = append(f.events, event)
	delete(f.targetActionStates, key)
	return true, nil
}

func (f *fakeRepository) DeleteUnmonitoredStates(ctx context.Context, userID, adminAccountID, targetID string, expectedGeneration int64) (int64, error) {
	parsed, valid := parseTargetID(targetID)
	if !valid || parsed.platform != "sub2api" || parsed.adminAccountID != adminAccountID {
		return 0, nil
	}
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	f.dataMu.Lock()
	defer f.dataMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if f.fakeWorkspaceHealthSettings(userID, adminAccountID).ConfigGeneration != expectedGeneration {
		return 0, nil
	}
	key := userID + "|" + adminAccountID + "|" + targetID
	if _, exists := f.targetActionStates[key]; exists {
		return 0, nil
	}
	if _, exists := f.priorityStates[key]; exists {
		return 0, nil
	}
	var deleted int64
	for model, state := range f.states[targetID] {
		if state.UserID == userID && state.AdminAccountID == adminAccountID {
			delete(f.states[targetID], model)
			deleted++
		}
	}
	if len(f.states[targetID]) == 0 {
		delete(f.states, targetID)
	}
	return deleted, nil
}

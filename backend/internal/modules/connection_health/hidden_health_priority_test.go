package connection_health

import (
	"errors"
	"reflect"
	"testing"
)

func TestHiddenHealthUnmanagedPriorityRestore(t *testing.T) {
	reader := &mutableSub2APISchedulerReader{priority: 30}
	service, repo := hiddenHealthFixture(t, reader)
	service.priorityActions = reader
	state := PrioritySyncState{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, TargetID: hiddenHealthTarget,
		OriginalPriority: 20, LastAppliedPriority: 30}
	repo.priorityStates[hiddenHealthUser+"|"+hiddenHealthWorkspace+"|"+hiddenHealthTarget] = state
	saveHiddenHealthTarget(t, repo, true)
	service.syncMultiplierPriorities(t.Context(), repo.policies, repo.assignments, repo.groupAssignments, nil, []PrioritySyncState{state})
	observed, writes, _ := reader.snapshot()
	if observed != 20 || writes != 1 {
		t.Fatalf("screening must restore original Priority 20 once: priority=%d writes=%d", observed, writes)
	}
}

func TestHiddenHealthPriorityReadFailureKeepsCheckpoints(t *testing.T) {
	for _, tc := range []struct {
		name            string
		independent     bool
		workspaceStatus bool
	}{
		{name: "scheduled with workspace status", workspaceStatus: true},
		{name: "independent with only target checkpoint", independent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &mutableSub2APISchedulerReader{priority: 30}
			service, repo := hiddenHealthFixture(t, reader)
			service.priorityActions = reader
			repo.listErr = errors.New("database unavailable in fixture")
			workspaceKey := hiddenHealthUser + "|" + hiddenHealthWorkspace
			targetKey := workspaceKey + "|" + hiddenHealthTarget
			state := PrioritySyncState{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, TargetID: hiddenHealthTarget, OriginalPriority: 20, LastAppliedPriority: 30}
			repo.priorityStates[targetKey] = state
			if tc.workspaceStatus {
				repo.priorityWorkspaces[workspaceKey] = PriorityWorkspaceSyncState{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, LastDecision: "success"}
			}
			if tc.independent {
				if err := service.syncCurrentWorkspacePrioritiesWithResult(t.Context(), hiddenHealthUser, hiddenHealthWorkspace); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, _ := service.beginSchedulerRound(t.Context())
				service.syncMultiplierPriorities(ctx, repo.policies, repo.assignments, repo.groupAssignments, nil, []PrioritySyncState{state})
			}
			view, err := service.PrioritySyncStatus(t.Context(), hiddenHealthUser)
			if err != nil {
				t.Fatal(err)
			}
			workspace, present := repo.priorityWorkspaces[workspaceKey]
			_, writes, _ := reader.snapshot()
			if writes != 0 || !reflect.DeepEqual(repo.priorityStates[targetKey], state) || present != tc.workspaceStatus {
				t.Fatalf("failed screening read changed data: writes=%d state=%+v workspace=%+v", writes, repo.priorityStates[targetKey], workspace)
			}
			if tc.workspaceStatus {
				if view.Status != "failed" || view.ErrorKey != ErrorHiddenAccountsRead || workspace.NextReconcileAt == nil {
					t.Fatalf("existing status must show a timed read failure: %+v", view)
				}
			} else if view.Status != "idle" || view.ErrorKey != "" {
				t.Fatalf("checkpoint-only workspace must retain idle status: %+v", view)
			}
		})
	}
}

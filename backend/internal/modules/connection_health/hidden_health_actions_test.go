package connection_health

import (
	"errors"
	"reflect"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func TestHiddenHealthSchedulerKeepsRestorableStatusOnListFailure(t *testing.T) {
	service, base, state, actions := cleanupPathsFixture(t, true)
	repo := &hiddenFakeRepository{fakeRepository: base, listErr: errors.New("fixture hidden list unavailable")}
	service.repo = repo
	reader := service.platformGroups.(fakePlatformGroupReader)
	reader.accountsByGrp["g1"][0].Status = "inactive"
	// The active peer protects the group without creating another probe job.
	reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "peer", Status: "active", Priority: intPointer(5)})
	state.Conflict = false
	base.targetActionStates[state.UserID+"|"+state.AdminAccountID+"|"+state.TargetID] = state
	base.groupExclusions = []GroupTargetExclusion{
		{UserID: state.UserID, AdminAccountID: state.AdminAccountID, AdminGroupID: "g1", TargetID: state.TargetID},
	}

	service.runSchedulerTick(t.Context())
	checkpoint, err := base.GetTargetActionState(t.Context(), state.UserID, state.AdminAccountID, state.TargetID)
	if err != nil || !reflect.DeepEqual(checkpoint, &state) || len(actions.sub2APICalls) != 0 {
		t.Fatalf("unreadable list must keep the restore checkpoint and prevent status writes: checkpoint=%+v writes=%v err=%v", checkpoint, actions.sub2APICalls, err)
	}

	repo.listErr = nil
	service.runSchedulerTick(t.Context())
	if len(actions.sub2APICalls) != 1 || actions.sub2APICalls[0].status != "active" {
		t.Fatalf("readable list must allow the same excluded target to restore its original status: %v", actions.sub2APICalls)
	}
}

func TestHiddenHealthUnmanagedStatusRestore(t *testing.T) {
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "manual", Status: "inactive", Models: "gpt-4o"}}},
	}
	service, repo := hiddenHealthFixture(t, reader)
	actions := &fakePlatformActioner{}
	service.dispatcher = newRemoteActionDispatcher(nil, nil, actions)
	state := TargetActionState{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, TargetID: hiddenHealthTarget,
		OriginalStatus: "active", LastAppliedStatus: "inactive"}
	key := hiddenHealthUser + "|" + hiddenHealthWorkspace + "|" + hiddenHealthTarget
	repo.targetActionStates[key] = state
	run := func(state TargetActionState) {
		service.restoreUnmanagedTargetActions(t.Context(), repo.policies, repo.assignments, repo.groupAssignments, nil, []TargetActionState{state}, make(adminInventoryCache))
	}
	run(state)
	if len(actions.sub2APICalls) != 0 {
		t.Fatalf("active policy must keep standby status: %v", actions.sub2APICalls)
	}
	saveHiddenHealthTarget(t, repo, true)
	run(state)
	if len(actions.sub2APICalls) != 1 || actions.sub2APICalls[0].status != "active" {
		t.Fatalf("screened target must restore original status: %v", actions.sub2APICalls)
	}
	reader.accountsByGrp["g1"][0].Status = "active"
	run(repo.targetActionStates[key])
	if _, exists := repo.targetActionStates[key]; exists || len(actions.sub2APICalls) != 1 {
		t.Fatalf("confirmed restore must remove the checkpoint without resending: %v", actions.sub2APICalls)
	}
}

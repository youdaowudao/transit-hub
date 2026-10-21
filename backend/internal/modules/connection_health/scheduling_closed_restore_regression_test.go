package connection_health

import (
	"context"
	"testing"
	"transithub/backend/internal/modules/upstream"
)

func TestRestoreWhileUnschedulableRestoresStatusOnly(t *testing.T) {
	repo := newFakeRepository()
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "isolated"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Status: "inactive", Models: "model-a", Schedulable: boolPointer(false), TempUnschedulableKnown: true}}}}
	platform := &fakePlatformActioner{afterSub2APIWrite: func(_ string, status string) { reader.accountsByGrp["g1"][0].Status = status }}
	s := &Service{repo: repo, platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	targetID := "sub2api:ws1:acc-1"
	key := "user1|ws1|" + targetID
	repo.states[targetID] = map[string]ConnectionHealthState{"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100}}
	repo.targetActionStates[key] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	// Read a complete snapshot for each observation, as the public formal-probe path does.
	// A nil inventory cannot prove that confirmation happened after the status write.
	reconcile := func() (string, error) {
		session := upstream.Session{Platform: upstream.PlatformSub2API}
		refresh, err := s.refreshAdminTarget(context.Background(), session, "ws1", "acc-1")
		if err != nil {
			return "", err
		}
		refresh.target.TestConfiguration = defaultTestConfiguration()
		result, err := s.reconcileTargetRemoteActionWithFloor(context.Background(), "user1", "ws1", session, refresh.target, []probeModelSpec{sub2APIActionTestSpec()}, nil, &refresh.inventory, adminMonitoringScope{})
		return result.remoteAction, err
	}
	action, err := reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if action != RemoteActionSub2APIStatusActive || len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("healthy system-stopped target was not restored: action=%q calls=%+v", action, platform.sub2APICalls)
	}
	action, err = reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.targetActionStates[key]; exists {
		t.Fatal("restored target checkpoint was not removed on confirmation")
	}
	if len(platform.sub2APICalls) != 1 || *reader.accountsByGrp["g1"][0].Schedulable {
		t.Fatal("confirmation resent status or changed scheduling")
	}
}

func TestUnschedulableEqualStateCheckpointDeleted(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	s := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	targetID := "sub2api:ws1:acc-1"
	key := "user1|ws1|" + targetID
	repo.states[targetID] = map[string]ConnectionHealthState{"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100}}
	repo.targetActionStates[key] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "active"}
	target := AdminProbeTarget{TargetID: targetID, Platform: "sub2api", AccountID: "acc-1", AccountStatus: "active", Schedulable: boolPointer(false), InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
	_, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{sub2APIActionTestSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.targetActionStates[key]; exists {
		t.Fatal("equal original/current state remained when scheduling was closed")
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatal("equal state generated an unnecessary status write")
	}
}

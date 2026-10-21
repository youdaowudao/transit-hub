package connection_health

import (
	"context"
	"net/http"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func schedulingClosedPathsFixture(t *testing.T) (*Service, *fakeRepository, *fakePlatformActioner, AdminProbeTarget) {
	t.Helper()
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	s := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: "sub2api", AccountID: "a", AccountStatus: "inactive", Schedulable: boolPointer(false), InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
	repo.targetActionStates["user1|ws1|"+target.TargetID] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: target.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	repo.states[target.TargetID] = map[string]ConnectionHealthState{"model-a": {ConnectionID: target.TargetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100}}
	return s, repo, platform, target
}

func TestDisableWhileUnschedulableStillSkipped(t *testing.T) {
	s, repo, platform, target := schedulingClosedPathsFixture(t)
	target.AccountStatus = "active"
	delete(repo.targetActionStates, "user1|ws1|"+target.TargetID)
	repo.states[target.TargetID]["model-a"] = ConnectionHealthState{ConnectionID: target.TargetID, ModelName: "model-a", State: StateSuspended, CurrentWeight: 0}
	action, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{sub2APIActionTestSpec()})
	if err != nil || action != RemoteActionSkippedUpstreamScheduling || len(platform.sub2APICalls) != 0 || len(repo.targetActionStates) != 0 {
		t.Fatalf("closed scheduling must not shut down or create a checkpoint: action=%s err=%v", action, err)
	}
}

func TestRestoreWhileUnschedulableRequiresAllHealthy(t *testing.T) {
	for _, kind := range []string{"unhealthy-model", "missing-model"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, platform, target := schedulingClosedPathsFixture(t)
			if kind == "unhealthy-model" {
				repo.states[target.TargetID]["model-b"] = ConnectionHealthState{ConnectionID: target.TargetID, ModelName: "model-b", State: StateSuspended, CurrentWeight: 0}
			}
			other := sub2APIActionTestSpec()
			other.modelName = "model-b"
			if _, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{sub2APIActionTestSpec(), other}); err != nil {
				t.Fatal(err)
			}
			if len(platform.sub2APICalls) != 0 || len(repo.targetActionStates) != 1 || *target.Schedulable {
				t.Fatal("partial or missing model health must preserve the shutdown and scheduling switch")
			}
		})
	}
}

func TestRestoreWhileUnschedulableBlockedByConflict(t *testing.T) {
	s, repo, platform, target := schedulingClosedPathsFixture(t)
	key := "user1|ws1|" + target.TargetID
	checkpoint := repo.targetActionStates[key]
	checkpoint.Conflict = true
	repo.targetActionStates[key] = checkpoint
	action, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{sub2APIActionTestSpec()})
	if err != nil || action != RemoteActionSkippedUpstreamScheduling || len(platform.sub2APICalls) != 0 || !repo.targetActionStates[key].Conflict {
		t.Fatalf("manual conflict must prevent restoration: action=%s err=%v", action, err)
	}
}

func TestUnschedulableWithoutSystemCheckpointDoesNotRestore(t *testing.T) {
	s, repo, platform, target := schedulingClosedPathsFixture(t)
	delete(repo.targetActionStates, "user1|ws1|"+target.TargetID)
	if _, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{sub2APIActionTestSpec()}); err != nil {
		t.Fatal(err)
	}
	if len(platform.sub2APICalls) != 0 || len(repo.targetActionStates) != 0 {
		t.Fatal("user-disabled account without a system checkpoint must stay disabled")
	}
}

func TestNewAPIUnschedulableRestoreStillSkipped(t *testing.T) {
	s, repo, platform, target := schedulingClosedPathsFixture(t)
	delete(repo.targetActionStates, "user1|ws1|"+target.TargetID)
	delete(repo.states, target.TargetID)
	target.TargetID, target.Platform, target.AccountStatus, target.AccountWeight = "newapi:ws1:a", "newapi", "2", intPointer(0)
	repo.states[target.TargetID] = map[string]ConnectionHealthState{"model-a": {ConnectionID: target.TargetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100}}
	repo.targetActionStates["user1|ws1|"+target.TargetID] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: target.TargetID, OriginalStatus: "1", OriginalWeight: intPointer(100), LastAppliedStatus: "2", LastAppliedWeight: intPointer(0)}
	action, err := s.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformNewAPI}, target, []probeModelSpec{sub2APIActionTestSpec()})
	if err != nil || action != RemoteActionSkippedUpstreamScheduling || len(platform.calls) != 0 || len(repo.targetActionStates) != 1 {
		t.Fatalf("NewAPI must retain its scheduling barrier: action=%s err=%v", action, err)
	}
}

func TestScheduledProbeRestoresWhileUnschedulable(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	repo.policies[0].AutoDegradeEnabled = true
	repo.policies[0].AutoRemoteActionEnabled = true
	repo.policies[0].ContinueProbeWhenUnschedulable = true
	repo.policies[0].UnschedulableProbeIntervalMinutes = 60
	base := s.platformGroups.(fakePlatformGroupReader)
	base.accountsByGrp["g1"][0].Status = "inactive"
	base.accountsByGrp["g1"][0].Schedulable = boolPointer(false)
	base.accountsByGrp["g1"] = append(base.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "b", Status: "active", Models: "gpt-4o", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
	s.platformGroups = base
	id := "sub2api:ws1:a"
	last := time.Now().Add(-2 * time.Hour)
	repo.states[id] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: id, UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", State: StateHealthy, RuleVersion: RuleVersionV2, CurrentWeight: 100, LastProbeAt: &last}}
	now := time.Now()
	repo.states["sub2api:ws1:b"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:b", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", State: StateHealthy, RuleVersion: RuleVersionV2, CurrentWeight: 100, LastProbeAt: &now}}
	repo.targetActionStates["user1|ws1|"+id] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	platform := &fakePlatformActioner{afterSub2APIWrite: func(account, status string) {
		for i := range base.accountsByGrp["g1"] {
			if base.accountsByGrp["g1"][i].ID == account {
				base.accountsByGrp["g1"][i].Status = status
			}
		}
	}}
	s.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
	probes := 0
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if base.accountsByGrp["g1"][0].Status != "inactive" || len(platform.sub2APICalls) != 0 {
			t.Error("fixture restored through another workflow before the new probe result")
		}
		probes++
		return schedulerPathsResponse(request, http.StatusOK), nil
	})
	s.runSchedulerTick(context.Background())
	waitSchedulerPathsIdle(t, s)
	if probes != 1 || len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" || base.accountsByGrp["g1"][0].Status != "active" || *base.accountsByGrp["g1"][0].Schedulable {
		t.Fatalf("fresh automatic success must restore only status: probes=%d writes=%d status=%s", probes, len(platform.sub2APICalls), base.accountsByGrp["g1"][0].Status)
	}
}

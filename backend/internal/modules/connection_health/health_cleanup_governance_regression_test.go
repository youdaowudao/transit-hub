package connection_health

import (
	"context"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

func TestUnmanagedStoredConflictCheckpointAbandoned(t *testing.T) {
	for _, visible := range []bool{true, false} {
		t.Run(map[bool]string{true: "all-monitored-memberships-excluded", false: "no-memberships"}[visible], func(t *testing.T) {
			repo := newFakeRepository()
			platform := &fakePlatformActioner{}
			targetID := "sub2api:ws1:acc-1"
			stored := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
			repo.targetActionStates["user1|ws1|"+targetID] = stored
			accounts := []upstream.AdminGroupAccountInfo{}
			if visible {
				accounts = append(accounts, upstream.AdminGroupAccountInfo{ID: "acc-1", Status: "active", Models: "model-a", Schedulable: boolPointer(true)})
			}
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": accounts}}
			s := &Service{repo: repo, platformGroups: reader, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
			s.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, adminInventoryCache{})
			if _, exists := repo.targetActionStates["user1|ws1|"+targetID]; exists {
				t.Error("stored manual conflict remained after the account left monitoring")
			}
			if len(platform.sub2APICalls) != 0 {
				t.Errorf("abandonment wrote main-site status: %+v", platform.sub2APICalls)
			}
			if len(repo.events) != 1 || repo.events[0].Result != "policy_unmanaged_abandoned" {
				t.Fatalf("abandonment audit=%+v", repo.events)
			}
		})
	}
}

func TestFirstConflictDetectionThenAbandon(t *testing.T) {
	repo := newFakeRepository()
	targetID := "sub2api:ws1:acc-1"
	key := "user1|ws1|" + targetID
	stored := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	repo.targetActionStates[key] = stored
	platform := &fakePlatformActioner{}
	s := &Service{repo: repo, platformGroups: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Status: "active", Models: "model-a"}}}}, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	s.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, adminInventoryCache{})
	stored, exists := repo.targetActionStates[key]
	if !exists || !stored.Conflict {
		t.Fatalf("first detection must retain conflict: %+v", stored)
	}
	s.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, adminInventoryCache{})
	if _, exists = repo.targetActionStates[key]; exists {
		t.Fatal("second tick did not abandon the previously stored conflict")
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatal("main-site status was changed")
	}
}

func TestStatesDeletedWhenWorkspaceHasNoMonitoring(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-assignment-removed", true: "last-policy-disabled"}[disabled], func(t *testing.T) {
			repo := newFakeRepository()
			targetID := "sub2api:ws1:acc-1"
			old := time.Now().Add(-30 * 24 * time.Hour)
			repo.states[targetID] = map[string]ConnectionHealthState{"model-a": {ConnectionID: targetID, UserID: "user1", AdminAccountID: "ws1", ModelName: "model-a", State: StateSuspended, LastProbeAt: &old}}
			if disabled {
				repo.policies = []Policy{{ID: "off", UserID: "user1", AdminAccountID: "ws1", Enabled: false, ModelTargets: []ModelTarget{{ModelName: "model-a", Enabled: true}}}}
				repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: "off"}}
			}
			s := &Service{repo: repo, platformGroups: fakePlatformGroupReader{}, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
			s.runSchedulerTick(context.Background())
			if len(repo.states[targetID]) != 0 {
				t.Fatalf("early-return tick retained inactive-workspace health records: %+v", repo.states[targetID])
			}
		})
	}
}

package connection_health

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func stageASetRestriction(account *upstream.AdminGroupAccountInfo, until *time.Time, known bool) {
	// The old code has no restriction fields. Its resulting admission is the RED.
	v := reflect.ValueOf(account).Elem()
	if f := v.FieldByName("TempUnschedulableKnown"); f.IsValid() {
		f.SetBool(known)
	}
	if f := v.FieldByName("TempUnschedulableUntil"); f.IsValid() {
		f.Set(reflect.ValueOf(until))
	}
}

func TestStageAFloorExcludesTemporaryOrUnknownReserve(t *testing.T) {
	for _, mode := range []string{"temporary", "unknown", "expired", "unlimited", "unknown-with-normal-reserve"} {
		t.Run(mode, func(t *testing.T) {
			inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: []upstream.AdminGroupAccountInfo{
				{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
				{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
			}})
			stageASetRestriction(&inventory.groups[0].accounts[0], nil, true)
			future := time.Now().Add(time.Hour)
			expired := time.Now().Add(-time.Minute)
			switch mode {
			case "temporary":
				stageASetRestriction(&inventory.groups[0].accounts[1], &future, true)
			case "unknown", "unknown-with-normal-reserve":
				stageASetRestriction(&inventory.groups[0].accounts[1], nil, false)
			case "expired":
				stageASetRestriction(&inventory.groups[0].accounts[1], &expired, true)
			case "unlimited":
				stageASetRestriction(&inventory.groups[0].accounts[1], nil, true)
			}
			if mode == "unknown-with-normal-reserve" {
				reserve := upstream.AdminGroupAccountInfo{ID: "1717", Status: "active", Schedulable: boolPointer(true)}
				stageASetRestriction(&reserve, nil, true)
				inventory.groups[0].accounts = append(inventory.groups[0].accounts, reserve)
			}
			target := AdminProbeTarget{TargetID: "sub2api:ws1:1515", Platform: string(upstream.PlatformSub2API), AccountID: "1515"}
			result := newWorkspaceFloorGuard().reserveSub2APIInactive(target, *inventory, fullFloorTestMonitoringScope(*inventory))
			blocked := mode == "temporary" || mode == "unknown"
			if blocked && result.remoteAction == "" {
				t.Fatal("closing last usable target admitted with temporary/unknown reserve")
			}
			if !blocked && result.remoteAction != "" {
				t.Fatalf("known normal reserve must allow close: %+v", result)
			}
			if !*inventory.groups[0].accounts[1].Schedulable {
				t.Fatal("reserve scheduling switch changed")
			}
		})
	}
}

func TestStageAManualSwitchBlockedByPriorityPending(t *testing.T) {
	for _, desired := range []bool{true, false} {
		actioner := &fakeTargetSchedulableActioner{}
		service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
		priority := 20
		key := "user1|ws1|sub2api:ws1:1515"
		repo.priorityStates[key] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1515", PendingPriority: &priority, PendingDispatchID: "priority-pending", PendingOwnerID: "old-owner", PendingDispatchPhase: DispatchUncertain}
		_, err := service.SetTargetSchedulable(context.Background(), "user1", "sub2api:ws1:1515", desired)
		if err == nil || actioner.calls != 0 {
			t.Fatalf("pending Priority allowed manual switch=%v: calls=%d err=%v", desired, actioner.calls, err)
		}
		if repo.priorityStates[key].PendingDispatchID != "priority-pending" {
			t.Fatal("manual switch cleared other action")
		}
	}
}

func TestStageANotSentAndRejectedReconcileWithoutInventory(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchNotSent, DispatchConfirmedRejected} {
		pair := RemoteActionCheckpoints{Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive", PendingDispatchID: "d", PendingOwnerID: "o", PendingDispatchPhase: phase}}
		changed := reconcileRemoteAction(&pair, RemoteActionObservation{}, func(string) bool { return true })
		if !changed || targetActionPending(pair.Target) {
			t.Fatalf("known no-side-effect receipt %s still requires inventory: %+v", phase, pair.Target)
		}
		if pair.Target == nil || pair.Target.LastAppliedStatus != "active" || pair.Target.OriginalStatus != "active" {
			t.Fatal("automatic baseline changed while clearing no-send receipt")
		}
	}
}

func TestStageAPermitRequiresTenSecondBudget(t *testing.T) {
	repo := newFakeRepository()
	service := &Service{repo: repo}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	claim := RemoteActionClaim{RemoteActionScope: RemoteActionScope{"u", "w", "sub2api:w:1"}, Kind: ActionKindTarget, Target: &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive"}}
	calls := 0
	_, err := service.dispatchRemoteAction(ctx, claim, func(context.Context) (string, error) { calls++; return "applied", nil })
	if err == nil || calls != 0 {
		t.Fatalf("two-second preparation authorized send: calls=%d err=%v", calls, err)
	}
	pair := repo.actionPair(claim.RemoteActionScope)
	if pair.Target == nil || pair.Target.PendingDispatchPhase != DispatchNotSent {
		t.Fatal("short preparation budget did not persist not_sent receipt")
	}
}

func TestStageALegacyDisableMustNotFallBackToRecentCache(t *testing.T) {
	repo := newFakeRepository()
	policy := monitoringScopeTestPolicy("direct-policy")
	repo.policies = []Policy{policy}
	for _, id := range []string{"1515", "1616"} {
		repo.assignments = append(repo.assignments, PolicyAssignment{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:" + id, PolicyID: policy.ID})
	}
	platform := &fakePlatformActioner{}
	service := legacyDisableMonitoringScopeService(repo, platform, []upstream.AdminGroupAccountInfo{
		{ID: "1515", Status: "active", Schedulable: boolPointer(true)},
		{ID: "1616", Status: "active", Schedulable: boolPointer(true)},
	})
	service.platformGroups = fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, errByGrp: map[string]error{"g1": errors.New("fresh inventory unavailable")}}
	err := service.DisableConnection(context.Background(), "user1", "conn-1")
	if err == nil || len(platform.sub2APICalls) != 0 {
		t.Fatalf("failed fresh read fell back to recent cache: calls=%d err=%v", len(platform.sub2APICalls), err)
	}
	if len(repo.states["conn-1"]) != 0 {
		t.Fatal("rejected compatibility close changed local states")
	}
}

func TestStageALegacyRestorePendingPreservesLocalState(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := legacyDisableMonitoringScopeService(repo, platform, nil)
	before := ConnectionHealthState{ConnectionID: "conn-1", ModelName: "model-a", UserID: "user1", AdminAccountID: "ws1", State: StateDisabled, CurrentWeight: 0, ConsecutiveFailures: 4}
	repo.states["conn-1"] = map[string]ConnectionHealthState{"model-a": before}
	priority := 20
	repo.priorityStates["user1|ws1|sub2api:ws1:1515"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1515", PendingPriority: &priority, PendingDispatchID: "pending-priority", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
	err := service.RestoreConnection(context.Background(), "user1", "conn-1")
	if err == nil || len(platform.sub2APICalls) != 0 {
		t.Fatalf("pending compatibility restore called remote: calls=%d err=%v", len(platform.sub2APICalls), err)
	}
	if !reflect.DeepEqual(before, repo.states["conn-1"]["model-a"]) {
		t.Fatal("pending compatibility restore prewrote local state")
	}
}

func TestStageALongUncertainCannotSelfClearFromEqualOrInvisibleInventory(t *testing.T) {
	for _, visible := range []bool{true, false} {
		state := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:1", OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive", PendingDispatchID: "exact-id", PendingOwnerID: "lost-owner", PendingDispatchPhase: DispatchUncertain}
		pair := RemoteActionCheckpoints{Target: &state}
		changed := reconcileRemoteAction(&pair, RemoteActionObservation{InventoryComplete: true, Visible: visible, SnapshotStartedAt: time.Now().Add(time.Minute), Status: "inactive"}, func(string) bool { return false })
		if changed || !targetActionPending(pair.Target) || pair.Target.PendingDispatchID != "exact-id" || pair.Target.PendingDispatchPhase != DispatchUncertain {
			t.Fatal("uncertain action self-cleared without approved terminal evidence")
		}
	}
}

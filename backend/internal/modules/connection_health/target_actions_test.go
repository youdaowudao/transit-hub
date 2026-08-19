package connection_health

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func sub2APIActionTestSpec() probeModelSpec {
	return probeModelSpec{
		modelName: "model-a",
		policy:    Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true},
	}
}

func sub2APISuspendedTargetFixture(repo *fakeRepository, accountID string) AdminProbeTarget {
	targetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID)
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateSuspended, CurrentWeight: 0},
	}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "active",
	}
	return AdminProbeTarget{
		TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: accountID, AccountStatus: "active", Schedulable: boolPointer(true),
	}
}

func sub2APITestInventory(groups ...adminInventoryGroup) *adminWorkspaceInventory {
	knownGroups := make([]adminInventoryGroup, len(groups))
	for i, group := range groups {
		knownGroups[i] = group
		knownGroups[i].accounts = append([]upstream.AdminGroupAccountInfo(nil), group.accounts...)
		for accountIndex := range knownGroups[i].accounts {
			account := &knownGroups[i].accounts[accountIndex]
			if account.Schedulable == nil && targetStatusEnabled(string(upstream.PlatformSub2API), normalizeTargetStatus(string(upstream.PlatformSub2API), account.Status)) {
				account.Schedulable = boolPointer(true)
			}
		}
	}
	return &adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups:  knownGroups,
	}
}

func floorTestMonitoringScope(fingerprint string, groups map[string][]string) adminMonitoringScope {
	monitoredByGroup := make(map[string]map[string]struct{}, len(groups))
	for groupID, accountIDs := range groups {
		monitoredByGroup[groupID] = make(map[string]struct{}, len(accountIDs))
		for _, accountID := range accountIDs {
			targetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID)
			monitoredByGroup[groupID][targetID] = struct{}{}
		}
	}
	return adminMonitoringScope{monitoredByGroup: monitoredByGroup, complete: true, fingerprint: fingerprint}
}

func fullFloorTestMonitoringScope(inventory adminWorkspaceInventory) adminMonitoringScope {
	groups := make(map[string][]string, len(inventory.groups))
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			groups[group.group.ID] = append(groups[group.group.ID], account.ID)
		}
	}
	return floorTestMonitoringScope("all:"+adminInventoryFingerprint(inventory), groups)
}

func TestWorkspaceFloorGuard_IgnoresUnmonitoredUsableAccounts(t *testing.T) {
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"},
			{ID: "acc-2", Status: "active"},
			{ID: "acc-3", Status: "active"},
		},
	})
	scope := floorTestMonitoringScope("only-acc-1", map[string][]string{"g1": {"acc-1"}})
	target := sub2APISuspendedTargetFixture(newFakeRepository(), "acc-1")

	result := newWorkspaceFloorGuard().reserveSub2APIInactive(target, *inventory, scope)
	if result.remoteAction != RemoteActionSkippedSub2APILastActive || result.adminGroupID != "g1" {
		t.Fatalf("unmonitored usable accounts released the monitored floor: %+v", result)
	}
}

func TestWorkspaceFloorGuard_CountsOnlyMonitoredMembersAcrossTwoClosures(t *testing.T) {
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"},
			{ID: "acc-2", Status: "active"},
			{ID: "acc-3", Status: "active"},
		},
	})
	scope := floorTestMonitoringScope("acc-1-and-acc-2", map[string][]string{"g1": {"acc-1", "acc-2"}})
	guard := newWorkspaceFloorGuard()

	first := guard.reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), "acc-1"), *inventory, scope)
	if first.remoteAction != "" {
		t.Fatalf("first monitored closure should consume one slot: %+v", first)
	}
	second := guard.reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), "acc-2"), *inventory, scope)
	if second.remoteAction != RemoteActionSkippedSub2APILastActive || second.adminGroupID != "g1" {
		t.Fatalf("second monitored closure was released by an unmonitored peer: %+v", second)
	}
}

func TestWorkspaceFloorGuard_KeepsReservationWhenMonitoringScopeShrinksOnSameInventory(t *testing.T) {
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"},
			{ID: "acc-2", Status: "active"},
			{ID: "acc-3", Status: "active"},
		},
	})
	guard := newWorkspaceFloorGuard()
	initial := floorTestMonitoringScope("three-members", map[string][]string{"g1": {"acc-1", "acc-2", "acc-3"}})
	changed := floorTestMonitoringScope("two-members", map[string][]string{"g1": {"acc-1", "acc-2"}})

	if result := guard.reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), "acc-1"), *inventory, initial); result.remoteAction != "" {
		t.Fatalf("initial reservation should succeed: %+v", result)
	}
	if result := guard.reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), "acc-2"), *inventory, changed); result.remoteAction != RemoteActionSkippedSub2APILastActive {
		t.Fatalf("scope shrink on stale inventory must keep the first closure reserved: %+v", result)
	}
}

func TestWorkspaceFloorGuard_ConcurrentClosuresUseOnlyMonitoredMembers(t *testing.T) {
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"},
			{ID: "acc-2", Status: "active"},
			{ID: "acc-3", Status: "active"},
		},
	})
	scope := floorTestMonitoringScope("two-monitored", map[string][]string{"g1": {"acc-1", "acc-2"}})
	guard := newWorkspaceFloorGuard()
	results := make(chan targetRemoteActionResult, 2)
	var wg sync.WaitGroup
	for _, accountID := range []string{"acc-1", "acc-2"} {
		accountID := accountID
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- guard.reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), accountID), *inventory, scope)
		}()
	}
	wg.Wait()
	close(results)
	allowed := 0
	protected := 0
	for result := range results {
		switch result.remoteAction {
		case "":
			allowed++
		case RemoteActionSkippedSub2APILastActive:
			protected++
		default:
			t.Fatalf("unexpected concurrent floor result: %+v", result)
		}
	}
	if allowed != 1 || protected != 1 {
		t.Fatalf("monitored concurrent closures allowed=%d protected=%d", allowed, protected)
	}
}

func TestReconcileTargetRemoteAction_LastActiveSkipsWithoutPendingCheckpoint(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	stored := repo.targetActionStates["user1|ws1|"+target.TargetID]
	stored.PendingStatus = "inactive"
	repo.targetActionStates["user1|ws1|"+target.TargetID] = stored
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "only"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"},
		},
	})

	result, err := service.reconcileTargetRemoteActionWithFloor(
		context.Background(), "user1", "ws1", inventory.session, target,
		[]probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.remoteAction != RemoteActionSkippedSub2APILastActive || result.adminGroupID != "g1" {
		t.Fatalf("last active decision = %+v", result)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("last active target must not be disabled: %+v", platform.sub2APICalls)
	}
	stored = repo.targetActionStates["user1|ws1|"+target.TargetID]
	if stored.PendingStatus != "" || stored.LastAppliedStatus != "active" || stored.Conflict {
		t.Fatalf("skip must clear stale pending inactive without claiming a write: %+v", stored)
	}
}

func TestReconcileTargetRemoteAction_LastUsableSkipsWhenRemainingActiveIsUnschedulable(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	target.Schedulable = boolPointer(true)
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "only-usable"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active", Schedulable: boolPointer(true)},
			{ID: "acc-2", Status: "active", Schedulable: boolPointer(false)},
		},
	})

	result, err := service.reconcileTargetRemoteActionWithFloor(
		context.Background(), "user1", "ws1", inventory.session, target,
		[]probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.remoteAction != RemoteActionSkippedSub2APILastActive || result.adminGroupID != "g1" {
		t.Fatalf("last usable decision = %+v", result)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("last usable target must not be disabled: %+v", platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_LastUsableFailsClosedWhenSurvivorSchedulableUnknown(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	target.Schedulable = boolPointer(true)
	inventory := &adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups: []adminInventoryGroup{{
			group: upstream.AdminGroupInfo{ID: "g1", Name: "unknown-survivor"},
			accounts: []upstream.AdminGroupAccountInfo{
				{ID: "acc-1", Status: "active", Schedulable: boolPointer(true)},
				{ID: "acc-2", Status: "active"},
			},
		}},
	}

	result, err := service.reconcileTargetRemoteActionWithFloor(
		context.Background(), "user1", "ws1", inventory.session, target,
		[]probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.remoteAction != RemoteActionSkippedSub2APIInventory || result.adminGroupID != "g1" {
		t.Fatalf("unknown survivor decision = %+v", result)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("unknown survivor must not permit a destructive write: %+v", platform.sub2APICalls)
	}
}

func TestWorkspaceFloorGuard_RejectsUnknownSub2APIStatus(t *testing.T) {
	cases := []struct {
		name              string
		targetStatus      string
		survivorStatus    string
		wantBlockingGroup string
	}{
		{name: "unknown survivor", targetStatus: "active", survivorStatus: "pending", wantBlockingGroup: "g1"},
		{name: "unknown target", targetStatus: "pending", survivorStatus: "active", wantBlockingGroup: "g1"},
		{name: "empty survivor", targetStatus: "active", survivorStatus: "", wantBlockingGroup: "g1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			inventory := sub2APITestInventory(adminInventoryGroup{
				group: upstream.AdminGroupInfo{ID: "g1", Name: "unknown-status"},
				accounts: []upstream.AdminGroupAccountInfo{
					{ID: "acc-1", Status: tt.targetStatus, Schedulable: boolPointer(true)},
					{ID: "acc-2", Status: tt.survivorStatus, Schedulable: boolPointer(true)},
				},
			})
			result := newWorkspaceFloorGuard().reserveSub2APIInactive(sub2APISuspendedTargetFixture(newFakeRepository(), "acc-1"), *inventory, fullFloorTestMonitoringScope(*inventory))
			if result.remoteAction != RemoteActionSkippedSub2APIInventory || result.adminGroupID != tt.wantBlockingGroup {
				t.Fatalf("unknown status must fail closed: %+v", result)
			}
		})
	}
}

func TestInventoryTargetAlreadyUnavailable_RejectsConflictingUnavailableObservations(t *testing.T) {
	inventory := sub2APITestInventory(
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "inactive"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "inactive", Schedulable: boolPointer(true)}},
		},
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g2", Name: "unschedulable"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active", Schedulable: boolPointer(false)}},
		},
	)
	if inventoryTargetAlreadyUnavailable(*inventory, "sub2api:ws1:acc-1") {
		t.Fatal("conflicting unavailable observations must not permit an idempotent bypass")
	}
}

func TestReconcileTargetRemoteAction_WaitsForWorkspaceMutationLease(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active", Schedulable: boolPointer(true)},
			{ID: "acc-2", Status: "active", Schedulable: boolPointer(true)},
		},
	})
	rawHeldRelease, err := repo.AcquireSub2APIMutationLease(context.Background(), "user1", "ws1")
	if err != nil {
		t.Fatalf("acquire held mutation lease: %v", err)
	}
	var releaseOnce sync.Once
	heldRelease := func() { releaseOnce.Do(rawHeldRelease) }
	defer heldRelease()

	done := make(chan struct{})
	go func() {
		_, _ = service.reconcileTargetRemoteActionWithFloor(
			context.Background(), "user1", "ws1", inventory.session, target,
			[]probeModelSpec{sub2APIActionTestSpec()}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
		)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("automatic destructive action bypassed the workspace mutation lease")
	case <-time.After(30 * time.Millisecond):
	}
	heldRelease()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("automatic action did not continue after workspace mutation lease release")
	}
}

func TestFinishTargetProbeBatch_LastActiveSkipAuditsBlockingGroup(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	state := repo.states[target.TargetID]["model-a"]
	state.LastRemoteAction = RemoteActionSub2APIStatusActive
	repo.states[target.TargetID]["model-a"] = state
	spec := sub2APIActionTestSpec()
	inventory := sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "only"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}},
	})

	err := service.finishTargetProbeBatchWithFloor(
		context.Background(), "user1", "ws1", inventory.session, target,
		[]probeModelSpec{spec}, []targetProbeResult{{state: &state, previousState: StateDegraded, outcome: ProbeOutcome{Result: ResultAuth}, spec: spec}},
		EventSourceScheduled, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err != nil {
		t.Fatalf("finish scheduled batch failed: %v", err)
	}
	if len(repo.events) != 1 || repo.events[0].RemoteAction != RemoteActionSkippedSub2APILastActive || repo.events[0].AdminGroupID != "g1" || repo.events[0].OwnGroupName != "only" {
		t.Fatalf("last-active skip must audit the blocking group: %+v", repo.events)
	}
	if stored := repo.states[target.TargetID]["model-a"]; stored.LastRemoteAction != RemoteActionSub2APIStatusActive {
		t.Fatalf("audit-only skip must preserve the previous real action: %+v", stored)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("last active skip wrote remote status: %+v", platform.sub2APICalls)
	}
}

func TestFinishTargetProbeBatch_UsesCurrentMonitoringScopeForInactive(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	state := repo.states[target.TargetID]["model-a"]
	state.LastRemoteAction = RemoteActionSub2APIStatusActive
	repo.states[target.TargetID]["model-a"] = state
	spec := sub2APIActionTestSpec()
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active", Models: "model-a"},
			{ID: "acc-2", Status: "active", Models: "model-a"},
		},
	})
	scope := floorTestMonitoringScope("only-acc-1", map[string][]string{"g1": {"acc-1"}})

	err := service.finishTargetProbeBatchWithFloor(
		context.Background(), "user1", "ws1", inventory.session, target,
		[]probeModelSpec{spec}, []targetProbeResult{{state: &state, previousState: StateDegraded, outcome: ProbeOutcome{Result: ResultAuth}, spec: spec}},
		EventSourceScheduled, newWorkspaceFloorGuard(), inventory, scope,
	)
	if err != nil {
		t.Fatalf("finish scheduled batch failed: %v", err)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("unmonitored usable peer released automatic inactive: %+v", platform.sub2APICalls)
	}
	if len(repo.events) != 1 || repo.events[0].RemoteAction != RemoteActionSkippedSub2APILastActive || repo.events[0].AdminGroupID != "g1" {
		t.Fatalf("automatic monitored floor audit = %+v", repo.events)
	}
	stored := repo.states[target.TargetID]["model-a"]
	if stored.State != StateSuspended || stored.LastRemoteAction != RemoteActionSub2APIStatusActive {
		t.Fatalf("blocked automatic action corrupted health or prior action state: %+v", stored)
	}
}

func TestFinishTargetProbeBatch_LastActiveIsReevaluatedEveryBatch(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	spec := sub2APIActionTestSpec()
	inventory := sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "only"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}},
	})

	for batch := 0; batch < 3; batch++ {
		state := repo.states[target.TargetID]["model-a"]
		if err := service.finishTargetProbeBatchWithFloor(
			context.Background(), "user1", "ws1", inventory.session, target,
			[]probeModelSpec{spec}, []targetProbeResult{{state: &state, previousState: StateSuspended, outcome: ProbeOutcome{Result: ResultAuth}, spec: spec}},
			EventSourceScheduled, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory),
		); err != nil {
			t.Fatalf("batch %d failed: %v", batch+1, err)
		}
	}
	if len(platform.sub2APICalls) != 0 || len(repo.events) != 3 {
		t.Fatalf("each batch must re-evaluate without disabling or stopping events: calls=%+v events=%+v", platform.sub2APICalls, repo.events)
	}
	for _, event := range repo.events {
		if event.RemoteAction != RemoteActionSkippedSub2APILastActive {
			t.Fatalf("every batch must independently protect the last active target: %+v", repo.events)
		}
	}
}

func TestReconcileTargetRemoteAction_FailedInactiveKeepsCurrentTickReservation(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream timeout")}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	first := sub2APISuspendedTargetFixture(repo, "acc-1")
	second := sub2APISuspendedTargetFixture(repo, "acc-2")
	inventory := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"},
		},
	})
	guard := newWorkspaceFloorGuard()

	result, err := service.reconcileTargetRemoteActionWithFloor(
		context.Background(), "user1", "ws1", inventory.session, first,
		[]probeModelSpec{sub2APIActionTestSpec()}, guard, inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err == nil || result.remoteAction != RemoteActionSub2APIStatusInactiveFailed {
		t.Fatalf("first inactive should fail after reserving: result=%+v err=%v", result, err)
	}
	firstState := repo.targetActionStates["user1|ws1|"+first.TargetID]
	if firstState.PendingStatus != "inactive" || firstState.LastAppliedStatus != "active" {
		t.Fatalf("failed write must keep its pending checkpoint: %+v", firstState)
	}
	platform.sub2APIErr = nil
	result, err = service.reconcileTargetRemoteActionWithFloor(
		context.Background(), "user1", "ws1", inventory.session, second,
		[]probeModelSpec{sub2APIActionTestSpec()}, guard, inventory, fullFloorTestMonitoringScope(*inventory),
	)
	if err != nil || result.remoteAction != RemoteActionSkippedSub2APILastActive {
		t.Fatalf("second candidate must be protected in the same tick: result=%+v err=%v", result, err)
	}
	if len(platform.sub2APICalls) != 1 {
		t.Fatalf("same tick must not attempt the second inactive: %+v", platform.sub2APICalls)
	}
}

func TestWorkspaceFloorGuard_ConcurrentCandidatesNeverReachZero(t *testing.T) {
	for _, accountIDs := range [][]string{{"acc-1", "acc-2"}, {"acc-1", "acc-2", "acc-3"}} {
		t.Run(string(rune('0'+len(accountIDs)))+"_active", func(t *testing.T) {
			accounts := make([]upstream.AdminGroupAccountInfo, 0, len(accountIDs))
			for _, accountID := range accountIDs {
				accounts = append(accounts, upstream.AdminGroupAccountInfo{ID: accountID, Status: "active"})
			}
			inventory := sub2APITestInventory(adminInventoryGroup{
				group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"}, accounts: accounts,
			})
			guard := newWorkspaceFloorGuard()
			results := make(chan targetRemoteActionResult, len(accountIDs))
			var wg sync.WaitGroup
			for _, accountID := range accountIDs {
				accountID := accountID
				wg.Add(1)
				go func() {
					defer wg.Done()
					results <- guard.reserveSub2APIInactive(AdminProbeTarget{
						TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID),
						Platform: string(upstream.PlatformSub2API), AccountID: accountID, AccountStatus: "active",
					}, *inventory, fullFloorTestMonitoringScope(*inventory))
				}()
			}
			wg.Wait()
			close(results)
			allowed := 0
			protected := 0
			for result := range results {
				switch result.remoteAction {
				case "":
					allowed++
				case RemoteActionSkippedSub2APILastActive:
					protected++
				default:
					t.Fatalf("unexpected floor result: %+v", result)
				}
			}
			if allowed != len(accountIDs)-1 || protected != 1 {
				t.Fatalf("allowed=%d protected=%d for %d active targets", allowed, protected, len(accountIDs))
			}
		})
	}
}

func TestWorkspaceFloorGuard_CrossGroupAndIncompleteInventoryFailClosed(t *testing.T) {
	target := AdminProbeTarget{
		TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", "acc-1"),
		Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "active",
	}
	crossGroup := sub2APITestInventory(
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "single"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}},
		},
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g2", Name: "many"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}},
		},
	)
	result := newWorkspaceFloorGuard().reserveSub2APIInactive(target, *crossGroup, fullFloorTestMonitoringScope(*crossGroup))
	if result.remoteAction != RemoteActionSkippedSub2APILastActive || result.adminGroupID != "g1" {
		t.Fatalf("cross-group target must be protected by its single-member group: %+v", result)
	}

	incomplete := sub2APITestInventory(
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "known"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}},
		},
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g2", Name: "failed"}, err: errors.New("temporary read error")},
	)
	result = newWorkspaceFloorGuard().reserveSub2APIInactive(target, *incomplete, fullFloorTestMonitoringScope(*incomplete))
	if result.remoteAction != RemoteActionSkippedSub2APIInventory || result.adminGroupID != "g2" {
		t.Fatalf("incomplete inventory must fail closed with the failed group: %+v", result)
	}
}

func TestWorkspaceFloorGuard_FailedWriteKeepsReservationOnlyForCurrentTick(t *testing.T) {
	inventory := sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}},
	})
	target := func(accountID string) AdminProbeTarget {
		return AdminProbeTarget{
			TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID),
			Platform: string(upstream.PlatformSub2API), AccountID: accountID, AccountStatus: "active",
		}
	}
	guard := newWorkspaceFloorGuard()
	if result := guard.reserveSub2APIInactive(target("acc-1"), *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != "" {
		t.Fatalf("first candidate should reserve the available slot: %+v", result)
	}
	if result := guard.reserveSub2APIInactive(target("acc-2"), *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != RemoteActionSkippedSub2APILastActive {
		t.Fatalf("same tick must keep the failed/unknown reservation: %+v", result)
	}
	if result := newWorkspaceFloorGuard().reserveSub2APIInactive(target("acc-2"), *inventory, fullFloorTestMonitoringScope(*inventory)); result.remoteAction != "" {
		t.Fatalf("next tick must rebuild reservations and reconsider: %+v", result)
	}
}

func TestWorkspaceFloorGuard_NewInventoryClearsPersistentReservation(t *testing.T) {
	initial := *sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}},
	})
	target := func(accountID string) AdminProbeTarget {
		return AdminProbeTarget{
			TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID),
			Platform: string(upstream.PlatformSub2API), AccountID: accountID, AccountStatus: "active",
		}
	}
	guard := newWorkspaceFloorGuard()
	if result := guard.reserveSub2APIInactive(target("acc-1"), initial, fullFloorTestMonitoringScope(initial)); result.remoteAction != "" {
		t.Fatalf("first reservation should be accepted: %+v", result)
	}

	afterClose := *sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "inactive"}, {ID: "acc-2", Status: "active"}},
	})
	guard.rememberInventory(afterClose)
	guard.rememberInventory(initial)
	if result := guard.reserveSub2APIInactive(target("acc-2"), initial, fullFloorTestMonitoringScope(initial)); result.remoteAction != "" {
		t.Fatalf("fresh inventory after restore must clear the old reservation: %+v", result)
	}
}

func TestWorkspaceFloorGuard_IncompleteInventoryKeepsPersistentReservation(t *testing.T) {
	inventory := *sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"}},
	})
	target := func(accountID string) AdminProbeTarget {
		return AdminProbeTarget{
			TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID),
			Platform: string(upstream.PlatformSub2API), AccountID: accountID, AccountStatus: "active",
		}
	}
	guard := newWorkspaceFloorGuard()
	if result := guard.reserveSub2APIInactive(target("acc-1"), inventory, fullFloorTestMonitoringScope(inventory)); result.remoteAction != "" {
		t.Fatalf("first reservation should be accepted: %+v", result)
	}
	guard.rememberInventory(*sub2APITestInventory(
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"}, accounts: inventory.groups[0].accounts},
		adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g2", Name: "unreadable"}, err: errors.New("accounts unavailable")},
	))
	if result := guard.reserveSub2APIInactive(target("acc-2"), inventory, fullFloorTestMonitoringScope(inventory)); result.remoteAction != RemoteActionSkippedSub2APILastActive {
		t.Fatalf("incomplete snapshot must not discard an unresolved reservation: %+v", result)
	}
}

func TestWorkspaceFloorGuard_ReconsidersAfterPeerBecomesActive(t *testing.T) {
	target := AdminProbeTarget{
		TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", "acc-1"),
		Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "active",
	}
	single := sub2APITestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active"}},
	})
	if result := newWorkspaceFloorGuard().reserveSub2APIInactive(target, *single, fullFloorTestMonitoringScope(*single)); result.remoteAction != RemoteActionSkippedSub2APILastActive {
		t.Fatalf("single active target must be protected: %+v", result)
	}
	withRecoveredPeer := sub2APITestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "shared"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "acc-1", Status: "active"}, {ID: "acc-2", Status: "active"},
		},
	})
	if result := newWorkspaceFloorGuard().reserveSub2APIInactive(target, *withRecoveredPeer, fullFloorTestMonitoringScope(*withRecoveredPeer)); result.remoteAction != "" {
		t.Fatalf("next tick must allow inactive after a peer becomes active: %+v", result)
	}
}

func TestFinishTargetProbeBatch_ManualProbeNeverWritesInactive(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	target := sub2APISuspendedTargetFixture(repo, "acc-1")
	state := repo.states[target.TargetID]["model-a"]
	spec := sub2APIActionTestSpec()

	err := service.finishTargetProbeBatch(
		context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target,
		[]probeModelSpec{spec}, []targetProbeResult{{state: &state, previousState: StateDegraded, outcome: ProbeOutcome{Result: ResultAuth}, spec: spec}},
		EventSourceManual,
	)
	if err != nil {
		t.Fatalf("manual batch failed: %v", err)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("formal manual probe must not write inactive: %+v", platform.sub2APICalls)
	}
	if len(repo.events) != 1 || repo.events[0].Source != EventSourceManual || repo.events[0].RemoteAction != "" {
		t.Fatalf("manual probe must keep state/event only: %+v", repo.events)
	}
}

func TestRestoreEmptySub2APIGroup_OnlyRestoresSystemOwnedAccount(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	now := time.Now().UTC()
	systemTargetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", "system")
	repo.targetActionStates["user1|ws1|"+systemTargetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: systemTargetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive", UpdatedAt: now,
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "zero"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "manual", Status: "inactive"}, {ID: "system", Status: "inactive"}},
		},
	}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}

	service.restoreEmptySub2APIGroups(context.Background(), []TargetActionState{repo.targetActionStates["user1|ws1|"+systemTargetID]}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "system" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("only the system-owned account may be restored: %+v", platform.sub2APICalls)
	}
	stored := repo.targetActionStates["user1|ws1|"+systemTargetID]
	if stored.LastAppliedStatus != "active" || stored.PendingStatus != "" || stored.Conflict {
		t.Fatalf("restored checkpoint not confirmed: %+v", stored)
	}
	if len(repo.events) != 1 || repo.events[0].Result != "group_zero_restore" || repo.events[0].AdminGroupID != "g1" {
		t.Fatalf("zero-group restore must be auditable: %+v", repo.events)
	}
}

func TestRestoreEmptySub2APIGroup_DoesNotRestoreManualInactiveAccounts(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "manual-only"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "manual", Status: "inactive"}}},
	}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	state := TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: buildTargetID(string(upstream.PlatformSub2API), "ws1", "manual"),
		OriginalStatus: "inactive", LastAppliedStatus: "inactive",
	}
	service.restoreEmptySub2APIGroups(context.Background(), []TargetActionState{state}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 0 || len(repo.events) != 0 {
		t.Fatalf("manual inactive account must stay untouched: calls=%+v events=%+v", platform.sub2APICalls, repo.events)
	}
}

type countingAdminInventoryReader struct {
	inner      fakePlatformGroupReader
	fetchCalls int
	listCalls  int
}

func (r *countingAdminInventoryReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	r.fetchCalls++
	return r.inner.FetchAdminAllGroups(session)
}

func (r *countingAdminInventoryReader) ListAdminGroupAccounts(session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.listCalls++
	return r.inner.ListAdminGroupAccounts(session, group)
}

func (r *countingAdminInventoryReader) ResolveProbeCredential(session upstream.Session, account upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	return r.inner.ResolveProbeCredential(session, account)
}

func TestAdminTargetRefresh_FloorGuardReusesInventoryWithoutExtraReads(t *testing.T) {
	reader := &countingAdminInventoryReader{inner: fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "first"}, {ID: "g2", Name: "second"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Status: "active", Schedulable: boolPointer(true)}, {ID: "acc-2", Status: "active", Schedulable: boolPointer(true)}},
			"g2": {{ID: "acc-1", Status: "active", Schedulable: boolPointer(true)}, {ID: "acc-3", Status: "active", Schedulable: boolPointer(true)}},
		},
	}}
	service := &Service{platformGroups: reader}
	refresh, err := service.refreshAdminTarget(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "ws1", "acc-1")
	if err != nil || !refresh.found || refresh.accountsReadError {
		t.Fatalf("refresh failed: refresh=%+v err=%v", refresh, err)
	}
	if reader.fetchCalls != 1 || reader.listCalls != 2 {
		t.Fatalf("execution refresh calls = fetch:%d list:%d", reader.fetchCalls, reader.listCalls)
	}
	result := newWorkspaceFloorGuard().reserveSub2APIInactive(refresh.target, refresh.inventory, fullFloorTestMonitoringScope(refresh.inventory))
	if result.remoteAction != "" {
		t.Fatalf("two active targets per membership should allow reservation: %+v", result)
	}
	if reader.fetchCalls != 1 || reader.listCalls != 2 {
		t.Fatalf("floor guard added upstream reads: fetch:%d list:%d", reader.fetchCalls, reader.listCalls)
	}
}

func TestReconcileTargetRemoteAction_SuspendedSiblingBlocksRestore(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateSuspended, CurrentWeight: 0},
		"model-b": {ConnectionID: targetID, ModelName: "model-b", State: StateRecovering, CurrentWeight: 25},
	}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	specs := []probeModelSpec{{modelName: "model-a", policy: policy}, {modelName: "model-b", policy: policy}}
	target := AdminProbeTarget{TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "inactive"}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "" || len(platform.sub2APICalls) != 0 {
		t.Fatalf("suspended sibling must keep account inactive, action=%q calls=%+v", action, platform.sub2APICalls)
	}

	repo.states[targetID]["model-a"] = ConnectionHealthState{ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100}
	action, err = service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, specs)
	if err != nil {
		t.Fatalf("unexpected restore error: %v", err)
	}
	if action != RemoteActionSub2APIStatusActive || len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("account should restore only after every model is safe, action=%q calls=%+v", action, platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_RestoresOriginalNewAPIWeight(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "newapi:ws1:100"
	originalWeight, appliedWeight, currentWeight := 37, 25, 25
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100},
	}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "1", OriginalWeight: &originalWeight, LastAppliedStatus: "1", LastAppliedWeight: &appliedWeight,
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	target := AdminProbeTarget{
		TargetID: targetID, Platform: string(upstream.PlatformNewAPI), AccountID: "100",
		AccountStatus: "1", AccountWeight: &currentWeight,
	}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformNewAPI}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "newapi_channel_weight_37" || len(platform.calls) != 1 || platform.calls[0].weight != 37 || platform.calls[0].status != 1 {
		t.Fatalf("expected exact original weight restore, action=%q calls=%+v", action, platform.calls)
	}
	if _, exists := repo.targetActionStates["user1|ws1|"+targetID]; exists {
		t.Fatal("action snapshot should be removed after the original state is restored")
	}
}

func TestReconcileTargetRemoteAction_ScalesNewAPIWeightFromOriginal(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "newapi:ws1:100"
	originalWeight, currentWeight := 37, 37
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateDegraded, CurrentWeight: 75},
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	target := AdminProbeTarget{
		TargetID: targetID, Platform: string(upstream.PlatformNewAPI), AccountID: "100",
		AccountStatus: "1", AccountWeight: &currentWeight,
	}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformNewAPI}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "" || len(platform.calls) != 0 {
		t.Fatalf("ordinary degraded state should not take over an unmanaged target: action=%q calls=%+v", action, platform.calls)
	}

	appliedWeight := 0
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "1", OriginalWeight: &originalWeight, LastAppliedStatus: "2", LastAppliedWeight: &appliedWeight,
	}
	target.AccountStatus = "2"
	target.AccountWeight = &appliedWeight
	action, err = service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformNewAPI}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected managed recovery error: %v", err)
	}
	if action != "newapi_channel_weight_28" || len(platform.calls) != 1 || platform.calls[0].weight != 28 {
		t.Fatalf("75%% recovery of original weight 37 must write 28, action=%q calls=%+v", action, platform.calls)
	}
}

func TestReconcileTargetRemoteAction_DoesNotRestoreWithUnprobedControlledModel(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100},
	}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	specs := []probeModelSpec{{modelName: "model-a", policy: policy}, {modelName: "model-b", policy: policy}}
	target := AdminProbeTarget{TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "inactive"}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "" || len(platform.sub2APICalls) != 0 {
		t.Fatalf("missing model state must keep the managed target inactive: action=%q calls=%+v", action, platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_DoesNotEnableInitiallyDisabledTarget(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateRecovering, CurrentWeight: 25},
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	target := AdminProbeTarget{TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "inactive"}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSkippedTargetInitiallyDisabled || len(platform.sub2APICalls) != 0 {
		t.Fatalf("initially disabled target must not be enabled, action=%q calls=%+v", action, platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_SkipsWhenUpstreamSchedulingDisabled(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateSuspended, CurrentWeight: 0},
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	schedulable := false
	target := AdminProbeTarget{
		TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1",
		AccountStatus: "active", Schedulable: &schedulable,
	}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != RemoteActionSkippedUpstreamScheduling || len(platform.sub2APICalls) != 0 {
		t.Fatalf("upstream scheduling disabled must skip automatic remote action, action=%q calls=%+v", action, platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_DoesNotReportSchedulingSkipWithoutRequestedAction(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateHealthy, CurrentWeight: 100},
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	schedulable := false
	target := AdminProbeTarget{
		TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1",
		AccountStatus: "active", Schedulable: &schedulable,
	}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != "" || len(platform.sub2APICalls) != 0 {
		t.Fatalf("ordinary healthy probe must not invent a skipped remote action, action=%q calls=%+v", action, platform.sub2APICalls)
	}
}

func TestReconcileTargetRemoteAction_ConfirmsPendingSystemWrite(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
	targetID := "sub2api:ws1:acc-1"
	repo.states[targetID] = map[string]ConnectionHealthState{
		"model-a": {ConnectionID: targetID, ModelName: "model-a", State: StateSuspended, CurrentWeight: 0},
	}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "active", PendingStatus: "inactive",
	}
	policy := Policy{ID: "p1", Enabled: true, AutoDegradeEnabled: true, AutoRemoteActionEnabled: true}
	target := AdminProbeTarget{TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "acc-1", AccountStatus: "inactive"}

	action, err := service.reconcileTargetRemoteAction(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, []probeModelSpec{{modelName: "model-a", policy: policy}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored := repo.targetActionStates["user1|ws1|"+targetID]
	if action != "" || stored.Conflict || stored.PendingStatus != "" || stored.LastAppliedStatus != "inactive" {
		t.Fatalf("pending system write should be confirmed without conflict: action=%q stored=%+v", action, stored)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("already-applied pending action must not be repeated: %+v", platform.sub2APICalls)
	}
}

func TestRestoreUnmanagedTargetActions_RestoresAfterPolicyUnbound(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Status: "inactive", Models: "gpt-4o"}},
		},
	}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	targetID := "sub2api:ws1:acc-1"
	stored := TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	repo.targetActionStates["user1|ws1|"+targetID] = stored

	service.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("unbound policy should restore the original upstream state: %+v", platform.sub2APICalls)
	}
	if _, exists := repo.targetActionStates["user1|ws1|"+targetID]; exists {
		t.Fatal("restored unmanaged target must release its action snapshot")
	}
	if len(repo.events) != 1 || repo.events[0].Result != "policy_unmanaged_restore" {
		t.Fatalf("restore should be traceable in events: %+v", repo.events)
	}
}

func TestRestoreUnmanagedTargetActions_RestoresWhenAutoDegradeDisabled(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Status: "inactive", Models: "gpt-4o"}},
		},
	}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	targetID := "sub2api:ws1:acc-1"
	stored := TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	repo.targetActionStates["user1|ws1|"+targetID] = stored
	policy := Policy{
		ID: "p1", UserID: "user1", AdminAccountID: "ws1", Enabled: true,
		AutoDegradeEnabled: false, AutoRemoteActionEnabled: true,
		ModelTargets: []ModelTarget{{ModelName: "gpt-4o", Enabled: true}},
	}
	assignment := GroupPolicyAssignment{
		UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID,
	}

	service.restoreUnmanagedTargetActions(
		context.Background(), []Policy{policy}, nil, []GroupPolicyAssignment{assignment}, nil,
		[]TargetActionState{stored}, make(adminInventoryCache),
	)
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("turning off auto degrade must release the captured upstream state: %+v", platform.sub2APICalls)
	}
	if _, exists := repo.targetActionStates["user1|ws1|"+targetID]; exists {
		t.Fatal("restored target must release its action snapshot")
	}
}

func TestRestoreUnmanagedTargetActions_IgnoresInheritedRemoteActionWhenTargetPolicyIsEnabled(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Status: "inactive", Models: "direct-model,group-model"}},
		},
	}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	targetID := "sub2api:ws1:acc-1"
	stored := TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	repo.targetActionStates["user1|ws1|"+targetID] = stored
	direct := Policy{
		ID: "direct", UserID: "user1", AdminAccountID: "ws1", Enabled: true,
		AutoDegradeEnabled: true, AutoRemoteActionEnabled: false,
		ModelTargets: []ModelTarget{{ModelName: "direct-model", Enabled: true}},
	}
	group := Policy{
		ID: "group", UserID: "user1", AdminAccountID: "ws1", Enabled: true,
		AutoDegradeEnabled: true, AutoRemoteActionEnabled: true,
		ModelTargets: []ModelTarget{{ModelName: "group-model", Enabled: true}},
	}
	targetAssignment := PolicyAssignment{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, PolicyID: direct.ID}
	groupAssignment := GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: group.ID}
	if !hasRemoteActionModel(candidateModelSpecs([]string{"direct-model", "group-model"}, []Policy{direct, group})) {
		t.Fatal("test setup must include a remote-action group model")
	}
	current := mergePoliciesByID([]Policy{direct}, []Policy{group})
	if !hasRemoteActionModel(candidateModelSpecs([]string{"direct-model", "group-model"}, current)) {
		t.Fatal("merged target and group policies must retain the remote-action model")
	}

	service.restoreUnmanagedTargetActions(
		context.Background(), []Policy{direct, group}, []PolicyAssignment{targetAssignment},
		[]GroupPolicyAssignment{groupAssignment}, nil, []TargetActionState{stored}, make(adminInventoryCache),
	)
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("inherited remote action must not survive enabled target policy override: %+v", platform.sub2APICalls)
	}
}

func TestRestoreUnmanagedTargetActions_RestoresTargetRemovedFromAllGroups(t *testing.T) {
	repo := newFakeRepository()
	platform := &fakePlatformActioner{}
	service := &Service{
		repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: fakePlatformGroupReader{}, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	targetID := "sub2api:ws1:acc-1"
	stored := TargetActionState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalStatus: "active", LastAppliedStatus: "inactive",
	}
	repo.targetActionStates["user1|ws1|"+targetID] = stored

	service.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("target removed from every group should still restore by stable target id: %+v", platform.sub2APICalls)
	}
	if _, exists := repo.targetActionStates["user1|ws1|"+targetID]; exists {
		t.Fatal("restored missing target must release its action snapshot")
	}
}

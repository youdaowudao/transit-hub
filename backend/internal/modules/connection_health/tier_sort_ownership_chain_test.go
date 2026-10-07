package connection_health

import (
	"fmt"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func fullChainOwnershipFixture(mode string, priority int) (*Service, *fakeRepository, *fakeTargetPriorityActioner, *fakePlatformActioner) {
	r := newFakeRepository()
	p := monitoringScopeTestPolicy("health")
	p.RuleVersion, p.PriorityMode = RuleVersionV2, PriorityModeMultiplier
	p.AutoDegradeEnabled, p.AutoRemoteActionEnabled = true, true
	r.policies = []Policy{p}
	r.accountTiers = map[string]int{"user1|ws1|sub2api:ws1:a": 1}
	r.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: p.ID}}
	r.groupSortSettings["user1|ws1|g1"] = GroupProbeSortSetting{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", FallbackMultiplier: float64Ptr(.1)}
	switch mode {
	case "direct", "direct-survives-exclusion":
		assignPolicyToTarget(r, p, "sub2api:ws1:a")
		assignPolicyToTarget(r, p, "sub2api:ws1:b")
		if mode == "direct-survives-exclusion" {
			r.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: "sub2api:ws1:a"}}
		}
	case "excluded":
		r.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: "sub2api:ws1:a"}}
	case "shared-ineffective-sibling":
		invalid := p
		invalid.ID, invalid.Enabled = "disabled", false
		r.policies = append(r.policies, invalid)
		r.groupAssignments = append(r.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", PolicyID: invalid.ID})
	case "direct-invalid-model-overrides-group":
		invalid := p
		invalid.ID, invalid.ModelTargets = "direct-invalid", []ModelTarget{{ModelName: "not-applicable", Enabled: true}}
		r.policies = append(r.policies, invalid)
		assignPolicyToTarget(r, invalid, "sub2api:ws1:a")
	case "remote-off":
		r.policies[0].AutoRemoteActionEnabled = false
	case "degrade-off":
		r.policies[0].AutoDegradeEnabled = false
	case "sort-off":
		r.policies[0].PriorityMode = PriorityModeNone
	case "disabled":
		r.policies[0].Enabled = false
	case "unassigned":
		r.groupAssignments = nil
	case "only":
		r.policies[0].StrategyMode, r.policies[0].ModelTargets = StrategyModeMultiplierOnly, nil
	case "mixed":
		only := p
		only.ID, only.StrategyMode, only.ModelTargets = "only", StrategyModeMultiplierOnly, nil
		r.policies = append(r.policies, only)
		r.groupAssignments = append(r.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: only.ID})
	}
	accounts := []upstream.AdminGroupAccountInfo{{ID: "a", Status: "active", Schedulable: boolPointer(true), Priority: intPointer(priority), Models: "gpt-4o", TempUnschedulableKnown: true}, {ID: "b", Status: "active", Schedulable: boolPointer(true), Priority: intPointer(51), Models: "gpt-4o", TempUnschedulableKnown: true}}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Multiplier: float64Ptr(.1)}, {ID: "g2", Multiplier: float64Ptr(.001)}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": accounts, "g2": accounts}}
	priorityWriter, statusWriter := &fakeTargetPriorityActioner{}, &fakePlatformActioner{}
	s := newAdminGroupsService(reader, fakeAdminGroupKeyReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}, r)
	s.sites, s.priorityActions, s.dispatcher = fakeSiteLookup{}, priorityWriter, newRemoteActionDispatcher(nil, nil, statusWriter)
	fullChainHealthState(r, RuleVersionV2, StateHealthy)
	r.states["sub2api:ws1:b"] = map[string]ConnectionHealthState{"gpt-4o": {UserID: "user1", AdminAccountID: "ws1", ConnectionID: "sub2api:ws1:b", ModelName: "gpt-4o", State: StateHealthy, RuleVersion: RuleVersionV2, CurrentWeight: 100, HealthEvidenceStatus: HealthEvidenceLegacy}}
	return s, r, priorityWriter, statusWriter
}

func TestTierSortFullChainEffectiveOwnershipMatrixControlsActualWrites(t *testing.T) {
	for _, mode := range []string{"direct", "direct-survives-exclusion", "excluded", "shared-ineffective-sibling", "direct-invalid-model-overrides-group", "remote-off", "degrade-off", "sort-off", "disabled", "unassigned", "only", "mixed"} {
		for _, priority := range []int{5, 50} {
			t.Run(fmt.Sprintf("%s/priority-%d", mode, priority), func(t *testing.T) {
				wantPriority, wantStatus := priority > 9, priority > 9
				wantValue := 10
				switch mode {
				case "excluded", "direct-invalid-model-overrides-group", "disabled", "unassigned":
					wantPriority, wantStatus = false, false
				case "remote-off":
					wantStatus = false
				case "degrade-off":
					wantPriority, wantStatus = false, false
				case "sort-off":
					wantPriority = false
				case "only":
					wantPriority, wantStatus, wantValue = true, false, 1
				case "mixed":
					wantPriority, wantValue = true, 1
				}
				// Each entry point starts from the same actual account state. A
				// priority mutation must not redefine the initial status scenario.
				s, r, priorities, statuses := fullChainOwnershipFixture(mode, priority)
				fullChainSync(t, s, r)
				count := 0
				for _, call := range priorities.calls {
					if call.targetID == "a" {
						count++
						if call.priority != wantValue {
							t.Fatalf("actual Priority=%d want=%d", call.priority, wantValue)
						}
					}
				}
				if (count == 1) != wantPriority || count > 1 || len(statuses.sub2APICalls) != 0 {
					t.Fatalf("effective policies admitted wrong Priority side effects: priorities=%+v statuses=%+v", priorities.calls, statuses.sub2APICalls)
				}
				s, r, priorities, statuses = fullChainOwnershipFixture(mode, priority)
				fullChainHealthState(r, RuleVersionV2, StateSuspended)
				inventory, err := s.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
				if err != nil {
					t.Fatal(err)
				}
				refresh, err := s.refreshAdminTarget(t.Context(), inventory.session, "ws1", "a")
				if err != nil {
					t.Fatal(err)
				}
				target := refresh.target
				if err := s.configureTestTarget(t.Context(), "user1", "ws1", &target, refresh.memberships, true); err != nil {
					t.Fatal(err)
				}
				specs, _, valid := s.currentScheduledProbeSpecs(t.Context(), "user1", "ws1", target, refresh.memberships, nil)
				if !valid {
					t.Fatal("complete effective-policy reads unexpectedly failed")
				}
				scope, err := s.loadAdminMonitoringScope(t.Context(), "user1", "ws1", *inventory)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", inventory.session, target, specs, newWorkspaceFloorGuard(), inventory, scope); err != nil {
					t.Fatal(err)
				}
				if (len(statuses.sub2APICalls) == 1) != wantStatus || len(statuses.sub2APICalls) > 1 || len(priorities.calls) != 0 {
					t.Fatalf("effective policies admitted wrong status side effects: want=%v priorities=%+v statuses=%+v", wantStatus, priorities.calls, statuses.sub2APICalls)
				}
				if wantStatus && (statuses.sub2APICalls[0].accountID != "a" || statuses.sub2APICalls[0].status != "inactive") {
					t.Fatalf("status write targeted a different account or direction: %+v", statuses.sub2APICalls)
				}
			})
		}
	}
}

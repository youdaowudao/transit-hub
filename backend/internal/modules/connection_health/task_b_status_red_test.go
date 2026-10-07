package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

// Primary-owned expectations: restoration must run without any fresh probe,
// and a legacy primary must never be disabled by the health action path.
func taskBStatusRestoreFixture(version string, tier, priority int) (*Service, *fakeRepository, *fakePlatformActioner, fakePlatformGroupReader, Policy, TargetActionState) {
	repo := newFakeRepository()
	repo.accountTiers = map[string]int{"user1|ws1|sub2api:ws1:a": tier}
	actions := &fakePlatformActioner{}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Status: "inactive", Priority: intPointer(priority), Models: "gpt-4o", TempUnschedulableKnown: true}}},
	}
	p := monitoringScopeTestPolicy("p")
	p.RuleVersion, p.AutoDegradeEnabled, p.AutoRemoteActionEnabled = version, true, true
	repo.policies = []Policy{p}
	st := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalStatus: "active", LastAppliedStatus: "inactive"}
	repo.targetActionStates["user1|ws1|"+st.TargetID] = st
	s := &Service{repo: repo, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, actions)}
	return s, repo, actions, reader, p, st
}

func taskBRestoreStatus(s *Service, p Policy, st TargetActionState) {
	s.restoreUnmanagedTargetActions(context.Background(), []Policy{p}, []PolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", TargetID: st.TargetID, PolicyID: p.ID}}, nil, nil, []TargetActionState{st}, make(adminInventoryCache))
}

func TestPriorityTaskBUnmanagedStatusRestoresWithoutProbe(t *testing.T) {
	for _, tc := range []struct {
		name, version  string
		tier, priority int
		wantRestore    bool
	}{
		{"legacy-primary", RuleVersionLegacy, 1, 20, true},
		{"ordinary-manual", RuleVersionV2, 2, 5, true},
		{"legacy-backup-stays-managed", RuleVersionLegacy, 2, 20, false},
		{"v2-primary-stays-managed", RuleVersionV2, 1, 20, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, actions, reader, p, st := taskBStatusRestoreFixture(tc.version, tc.tier, tc.priority)
			// No health state or probe outcome exists: budget/cooldown/credentials
			// cannot be prerequisites for restoring a no-longer-managed account.
			taskBRestoreStatus(s, p, st)
			if !tc.wantRestore {
				if len(actions.sub2APICalls) != 0 {
					t.Fatalf("still-managed target restored: %+v", actions.sub2APICalls)
				}
				return
			}
			if len(actions.sub2APICalls) != 1 || actions.sub2APICalls[0].status != "active" {
				t.Fatalf("missing original-status restoration without probe: %+v", actions.sub2APICalls)
			}
			if got := repo.targetActionStates["user1|ws1|"+st.TargetID]; got.PendingStatus != "active" || got.LastAppliedStatus != "inactive" {
				t.Fatalf("receipt guessed confirmation: %+v", got)
			}
			reader.accountsByGrp["g1"][0].Status = "active"
			taskBRestoreStatus(s, p, repo.targetActionStates["user1|ws1|"+st.TargetID])
			if _, ok := repo.targetActionStates["user1|ws1|"+st.TargetID]; ok || len(actions.sub2APICalls) != 1 {
				t.Fatalf("restored original must release once, without resending: exists=%v calls=%v", ok, actions.sub2APICalls)
			}
			if len(repo.states) != 0 || len(repo.events) != 1 || repo.events[0].Result != "policy_unmanaged_restore" {
				t.Fatalf("restoration fabricated health evidence: states=%v events=%v", repo.states, repo.events)
			}
		})
	}
}

func TestPriorityTaskBUnmanagedStatusKeepsConflictPendingAndIncomplete(t *testing.T) {
	for _, reason := range []string{"conflict", "priority-pending", "target-pending", "incomplete"} {
		t.Run(reason, func(t *testing.T) {
			s, repo, actions, reader, p, st := taskBStatusRestoreFixture(RuleVersionLegacy, 1, 5)
			switch reason {
			case "conflict":
				st.Conflict = true
				repo.targetActionStates["user1|ws1|"+st.TargetID] = st
			case "target-pending":
				st.PendingStatus = "active"
				repo.targetActionStates["user1|ws1|"+st.TargetID] = st
			case "priority-pending":
				repo.priorityStates["user1|ws1|"+st.TargetID] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: st.TargetID, OriginalPriority: 50, LastAppliedPriority: 20, PendingPriority: intPointer(5)}
			case "incomplete":
				reader.errByGrp = map[string]error{"g1": errors.New("fixture inventory incomplete")}
				s.platformGroups = reader
			}
			taskBRestoreStatus(s, p, st)
			if len(actions.sub2APICalls) != 0 {
				t.Fatalf("%s admitted restoration: %+v", reason, actions.sub2APICalls)
			}
			if _, ok := repo.targetActionStates["user1|ws1|"+st.TargetID]; !ok {
				t.Fatal("unconfirmed checkpoint deleted")
			}
		})
	}
}

func TestPriorityTaskBLegacyPrimaryCannotDisableButV2StillUsesFloor(t *testing.T) {
	for _, version := range []string{RuleVersionLegacy, RuleVersionV2} {
		for _, survivor := range []bool{false, true} {
			t.Run(version+"/survivor-"+map[bool]string{false: "no", true: "yes"}[survivor], func(t *testing.T) {
				repo := newFakeRepository()
				repo.accountTiers = map[string]int{"user1|ws1|sub2api:ws1:acc-1": 1}
				actions := &fakePlatformActioner{}
				s := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, actions)}
				target := sub2APISuspendedTargetFixture(repo, "acc-1")
				accounts := []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active", Priority: intPointer(20), Models: "model-a"}}
				if survivor {
					accounts = append(accounts, upstream.AdminGroupAccountInfo{ID: "acc-2", Status: "active", Priority: intPointer(21), Models: "model-a"})
				}
				inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: accounts})
				stageAFloorService(s, inventory)
				p := s.repo.(*fakeRepository).policies[0]
				p.AutoDegradeEnabled, p.AutoRemoteActionEnabled, p.RuleVersion = true, true, version
				p.ModelTargets = []ModelTarget{{ModelName: "model-a", Enabled: true}}
				repo.policies = []Policy{p}
				spec := sub2APIActionTestSpec()
				spec.policy = p
				_, err := s.reconcileTargetRemoteActionWithFloor(context.Background(), "user1", "ws1", inventory.session, target, []probeModelSpec{spec}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory))
				if err != nil {
					t.Fatal(err)
				}
				wantDisable := version == RuleVersionV2 && survivor
				if (len(actions.sub2APICalls) == 1) != wantDisable {
					t.Fatalf("version=%s survivor=%v calls=%+v", version, survivor, actions.sub2APICalls)
				}
				if wantDisable && actions.sub2APICalls[0].status != "inactive" {
					t.Fatalf("new-rule failure did not disable: %v", actions.sub2APICalls)
				}
			})
		}
	}
}

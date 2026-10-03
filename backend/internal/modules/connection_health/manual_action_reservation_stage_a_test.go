package connection_health

import (
	"testing"

	"transithub/backend/internal/modules/upstream"
)

// A reliable no-side-effect receipt must restore capacity even when the next
// complete inventory is byte-for-byte unchanged and the scheduler has no work.
func TestStageANoSideEffectReleasesReservationAcrossManualEntrypoints(t *testing.T) {
	for _, entry := range []string{"schedulable", "compatibility", ActionSourceManualDelete, ActionSourceCompensateDelete} {
		for _, outcome := range []string{upstream.MutationNotSent, upstream.MutationConfirmedRejected} {
			for _, next := range []string{"1515", "1616"} {
				t.Run(entry+"/"+string(outcome)+"/next-"+next, func(t *testing.T) {
					sendErr := &upstream.RequestError{MessageKey: upstream.ErrorNetwork, MutationOutcome: outcome}
					actioner := &fakeTargetSchedulableActioner{err: sendErr, afterWrite: func(bool) {}}
					service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
					reader := &stageADeleteInventoryReader{fakePlatformGroupReader: service.platformGroups.(fakePlatformGroupReader), sendErr: sendErr}
					service.platformGroups = reader
					platform := &fakePlatformActioner{sub2APIErr: sendErr}
					service.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
					session := upstream.Session{Platform: upstream.PlatformSub2API}
					var err error
					switch entry {
					case "schedulable":
						_, err = service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1515", false)
					case "compatibility":
						_, err = service.runCompatibilityStatusAction(t.Context(), "user1", "ws1", session, "1515", "inactive")
					default:
						err = service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", entry)
					}
					if err == nil || actioner.calls+len(platform.sub2APICalls)+reader.calls != 1 {
						t.Fatalf("first attempt must fail after one classified request: %v", err)
					}
					if pair := repo.actionPair(RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"}); pair.pendingCount() != 0 {
						t.Fatalf("reliable no-side-effect receipt did not reconcile: %+v", pair)
					}
					guard := service.sub2APIFloorGuardFor("user1", "ws1")
					// A tick with no enabled policies cannot replace this workspace guard.
					policies := repo.policies
					repo.policies = nil
					service.runSchedulerTick(t.Context())
					repo.policies = policies
					if service.sub2APIFloorGuardFor("user1", "ws1") != guard {
						t.Fatal("fixture unexpectedly refreshed the floor guard")
					}
					actioner.err = nil
					actioner.afterWriteForAccount = func(id string, value bool) {
						for i := range reader.accountsByGrp["g1"] {
							if reader.accountsByGrp["g1"][i].ID == id {
								reader.accountsByGrp["g1"][i].Schedulable = boolPointer(value)
							}
						}
					}
					calls := actioner.calls
					if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:"+next, false); err != nil || actioner.calls != calls+1 {
						t.Fatalf("no-side-effect attempt left usable capacity reserved: calls=%d err=%v", actioner.calls, err)
					}
					last := "1515"
					if next == last {
						last = "1616"
					}
					if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:"+last, false); err == nil || actioner.calls != calls+1 {
						t.Fatal("releasing stale capacity bypassed the last usable account safeguard")
					}
				})
			}
		}
	}
}

func TestStageAPriorityNoSideEffectReleasesOverlayReservation(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchNotSent, DispatchConfirmedRejected} {
		t.Run(string(phase), func(t *testing.T) {
			actioner := &fakeTargetSchedulableActioner{afterWrite: func(bool) {}}
			service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), actioner)
			priority := 30
			scope := RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"}
			repo.priorityStates["user1|ws1|"+scope.TargetID] = PrioritySyncState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, PendingPriority: &priority, PendingDispatchID: "old-priority", PendingDispatchPhase: phase}
			inventory, err := service.loadAdminInventory(t.Context(), "user1", "ws1", adminInventoryCache{})
			if err != nil {
				t.Fatal(err)
			}
			guard := service.sub2APIFloorGuardFor("user1", "ws1")
			guard.rememberInventory(*inventory)
			if err := service.overlayPersistentPending(t.Context(), "user1", "ws1", guard, *inventory); err != nil {
				t.Fatal(err)
			}
			pair, err := service.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: scope})
			if err != nil || pair.pendingCount() != 0 {
				t.Fatalf("Priority receipt did not reconcile: %v", err)
			}
			actioner.afterWriteForAccount = func(id string, value bool) {
				reader := service.platformGroups.(fakePlatformGroupReader)
				for i := range reader.accountsByGrp["g1"] {
					if reader.accountsByGrp["g1"][i].ID == id {
						reader.accountsByGrp["g1"][i].Schedulable = boolPointer(value)
					}
				}
			}
			if _, err := service.SetTargetSchedulable(t.Context(), "user1", "sub2api:ws1:1616", false); err != nil || actioner.calls != 1 {
				t.Fatalf("reconciled Priority still consumed the peer's reserve: %v", err)
			}
		})
	}
}

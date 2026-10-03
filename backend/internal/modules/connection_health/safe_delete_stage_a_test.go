package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type stageADeleteInventoryReader struct {
	fakePlatformGroupReader
	calls       int
	afterDelete func()
	sendErr     error
}

func (r *stageADeleteInventoryReader) DeleteSub2APIAdminAccountContext(ctx context.Context, session upstream.Session, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.calls++
	if r.afterDelete != nil {
		r.afterDelete()
	}
	return r.sendErr
}

func stageASafeDeleteService(t *testing.T) (*Service, *fakeRepository, *stageADeleteInventoryReader, upstream.Session) {
	t.Helper()
	service, repo := schedulableActionServiceWithSurvivor(boolPointer(true), &fakeTargetSchedulableActioner{})
	reader := &stageADeleteInventoryReader{fakePlatformGroupReader: service.platformGroups.(fakePlatformGroupReader)}
	service.platformGroups = reader
	session, _ := service.mySites.RequireSession(t.Context(), "user1", "ws1")
	return service, repo, reader, session
}

func TestStageASafeDeleteRejectsLastReserveAndSameAccountPending(t *testing.T) {
	for _, mode := range []string{"last-reserve", "same-pending"} {
		t.Run(mode, func(t *testing.T) {
			service, repo, reader, session := stageASafeDeleteService(t)
			if mode == "last-reserve" {
				reader.accountsByGrp["g1"] = reader.accountsByGrp["g1"][:1]
			} else {
				value := 30
				repo.priorityStates["user1|ws1|sub2api:ws1:1515"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1515", PendingPriority: &value, PendingDispatchID: "priority-pending", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
			}
			err := service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", ActionSourceManualDelete)
			if err == nil || reader.calls != 0 || len(reader.accountsByGrp["g1"]) == 0 {
				t.Fatal("protected delete called endpoint or dropped source data")
			}
		})
	}
}

func TestStageASafeDeleteRequiresConfirmedWriteAndFreshCompleteAbsence(t *testing.T) {
	for _, mode := range []string{"confirmed-absent", "still-visible", "read-failed", "timeout-unknown"} {
		t.Run(mode, func(t *testing.T) {
			service, repo, reader, session := stageASafeDeleteService(t)
			initial := reader.accountsByGrp["g1"]
			switch mode {
			case "confirmed-absent":
				reader.afterDelete = func() { reader.accountsByGrp["g1"] = initial[1:] }
			case "read-failed":
				reader.afterDelete = func() { reader.errByGrp = map[string]error{"g1": errors.New("fresh inventory unavailable")} }
			case "timeout-unknown":
				reader.sendErr = context.DeadlineExceeded
				reader.afterDelete = func() { reader.accountsByGrp["g1"] = initial[1:] }
			}
			err := service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", ActionSourceManualDelete)
			pair := repo.actionPair(RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"})
			if reader.calls != 1 {
				t.Fatal("delete did not make exactly one permitted request")
			}
			if mode == "confirmed-absent" {
				if err != nil || pair.pendingCount() != 0 || pair.Target != nil {
					t.Fatal("confirmed delete with fresh full absence did not permit resource cleanup")
				}
				return
			}
			if err == nil || pair.pendingCount() != 1 || pair.Target.PendingActionKind != TargetMutationDelete {
				t.Fatal("unconfirmed/visible/incomplete delete allowed cleanup")
			}
			expected := DispatchConfirmedApplied
			if mode == "timeout-unknown" {
				expected = DispatchUncertain
			}
			if pair.Target.PendingDispatchPhase != expected {
				t.Fatal("wrong protected delete receipt")
			}
		})
	}
}

func TestStageADeleteClosedAccountStillRequiresCompleteProtectedInventory(t *testing.T) {
	for _, source := range []string{ActionSourceManualDelete, ActionSourceCompensateDelete} {
		for _, closedBy := range []string{"inactive", "schedulable-false"} {
			for _, mode := range []string{"incomplete", "target-pending-peer", "priority-pending-peer", "complete-confirmed"} {
				t.Run(source+"/"+closedBy+"/"+mode, func(t *testing.T) {
					service, repo, reader, session := stageASafeDeleteService(t)
					if closedBy == "inactive" {
						reader.accountsByGrp["g1"][0].Status = "inactive"
					} else {
						reader.accountsByGrp["g1"][0].Schedulable = boolPointer(false)
					}
					peerKey := "user1|ws1|sub2api:ws1:1616"
					switch mode {
					case "incomplete":
						reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g2", Name: "unavailable-group"})
						reader.errByGrp = map[string]error{"g2": errors.New("inventory unavailable")}
					case "target-pending-peer":
						repo.targetActionStates[peerKey] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1616", PendingActionKind: TargetMutationSchedulable, PendingSchedulable: boolPointer(false), PendingSource: ActionSourceManual, PendingGroupIDs: []string{"g1"}, PendingDispatchID: "peer-target-pending", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
					case "priority-pending-peer":
						priority := 20
						repo.priorityStates[peerKey] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:1616", PendingPriority: &priority, PendingDispatchID: "peer-priority-pending", PendingOwnerID: "owner", PendingDispatchPhase: DispatchUncertain}
					case "complete-confirmed":
						initial := reader.accountsByGrp["g1"]
						reader.afterDelete = func() { reader.accountsByGrp["g1"] = initial[1:] }
					}
					err := service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", source)
					pair := repo.actionPair(RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"})
					if mode == "complete-confirmed" {
						if err != nil || reader.calls != 1 || pair.pendingCount() != 0 || pair.Target != nil {
							t.Fatalf("safe closed-account delete did not complete once: calls=%d err=%v pair=%+v", reader.calls, err, pair)
						}
						return
					}
					if err == nil || reader.calls != 0 || len(reader.accountsByGrp["g1"]) != 2 || pair.Target != nil {
						t.Fatalf("closed account bypassed deletion protection: calls=%d err=%v pair=%+v", reader.calls, err, pair)
					}
					if mode == "incomplete" && err.Error() != ErrorSub2APIInventoryIncomplete {
						t.Fatalf("incomplete inventory lost its reason: %v", err)
					}
					if mode != "incomplete" && !errors.Is(err, ErrRemoteActionPending) {
						t.Fatalf("pending group lost its reason: %v", err)
					}
					if mode == "target-pending-peer" && repo.targetActionStates[peerKey].PendingDispatchID != "peer-target-pending" {
						t.Fatal("delete cleared another account's Target pending")
					}
					if mode == "priority-pending-peer" && repo.priorityStates[peerKey].PendingDispatchID != "peer-priority-pending" {
						t.Fatal("delete cleared another account's Priority pending")
					}
				})
			}
		}
	}
}

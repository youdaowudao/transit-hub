package connection_health

import (
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type stageASentDeleteInventoryReader struct {
	*stageADeleteInventoryReader
	groupErr error
}

func (r *stageASentDeleteInventoryReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	if r.groupErr != nil {
		return nil, r.groupErr
	}
	return r.stageADeleteInventoryReader.FetchAdminAllGroups(session)
}

func TestStageASentDeleteConfirmationErrorsPreserveAppliedEvidence(t *testing.T) {
	for _, source := range []string{ActionSourceManualDelete, ActionSourceCompensateDelete} {
		for _, failure := range []string{"still-visible", "inventory-incomplete", "inventory-load", "settlement", "receipt"} {
			t.Run(source+"/"+failure, func(t *testing.T) {
				service, repo, reader, session := stageASafeDeleteService(t)
				wrapper := &stageAReservationFaultRepository{fakeRepository: repo}
				service.repo = wrapper
				inventoryReader := &stageASentDeleteInventoryReader{stageADeleteInventoryReader: reader}
				service.platformGroups = inventoryReader
				fault := errors.New("synthetic confirmation failure")
				reader.afterDelete = func() {
					switch failure {
					case "inventory-incomplete":
						reader.errByGrp = map[string]error{"g1": fault}
					case "inventory-load":
						inventoryReader.groupErr = fault
					case "settlement":
						wrapper.reconcileErr = fault
					}
				}
				if failure == "receipt" {
					wrapper.receiptErr = fault
				}
				err := service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", source)
				if err == nil || reader.calls != 1 {
					t.Fatal("confirmed delete must be sent once and stay unconfirmed locally")
				}
				pair := repo.actionPair(RemoteActionScope{"user1", "ws1", "sub2api:ws1:1515"})
				phase := DispatchConfirmedApplied
				if failure == "receipt" {
					phase = DispatchSending
				}
				if pair.pendingCount() != 1 || pair.Target.PendingDispatchPhase != phase {
					t.Fatal("confirmation error lost persistent delete protection")
				}
				if errors.Is(err, ErrRemoteActionPending) || upstream.RemoteMutationOutcome(err) != upstream.MutationConfirmedApplied {
					t.Fatalf("confirmed DELETE was reported like a pre-send pending block: %v", err)
				}
				if failure == "inventory-load" || failure == "settlement" || failure == "receipt" {
					if !errors.Is(err, fault) {
						t.Fatal("sent confirmation error discarded its original cause")
					}
				}
			})
		}
	}
}

func TestStageADeleteInitialSettlementFailureDoesNotInventPending(t *testing.T) {
	service, repo, reader, session := stageASafeDeleteService(t)
	fault := errors.New("synthetic initial checkpoint read failure")
	service.repo = &stageAReservationFaultRepository{fakeRepository: repo, reconcileErr: fault}
	err := service.DeleteManagedSub2APIAccount(t.Context(), "user1", "ws1", session, "1515", ActionSourceManualDelete)
	if !errors.Is(err, fault) || errors.Is(err, ErrRemoteActionPending) || reader.calls != 0 {
		t.Fatalf("initial database failure falsely claimed pending or sent DELETE: %v", err)
	}
}

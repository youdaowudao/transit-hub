package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type generationChangingPriorityActioner struct {
	beforePrepared func() error
	afterPrepared  chan struct{}
	releaseWrite   chan struct{}
	afterWrite     func()
	writes         int
}

func (a *generationChangingPriorityActioner) UpdateAdminTargetPriority(upstream.Session, string, int) error {
	a.writes++
	return nil
}

func (a *generationChangingPriorityActioner) UpdateAdminTargetPriorityPreparedContext(
	_ context.Context,
	_ upstream.Session,
	_ string,
	_ int,
	beforeWrite func() error,
) error {
	if a.beforePrepared != nil {
		if err := a.beforePrepared(); err != nil {
			return err
		}
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			return err
		}
	}
	if a.afterPrepared != nil {
		close(a.afterPrepared)
	}
	if a.releaseWrite != nil {
		<-a.releaseWrite
	}
	a.writes++
	if a.afterWrite != nil {
		a.afterWrite()
	}
	return nil
}

func TestAutomaticSub2APIPriorityRejectsFreshManualPriorityChange(t *testing.T) {
	actualPriority := 55
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Priority: &actualPriority, Models: "gpt-4o"}},
		},
	}
	actioner := &generationChangingPriorityActioner{}
	safetyRepo := newMutationSafetyRepository()
	service := &Service{
		repo: newFakeRepository(), safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: reader, priorityActions: actioner,
	}
	err := service.updateAutomaticTargetPriority(context.Background(), "user1", "ws1",
		upstream.Session{Platform: upstream.PlatformSub2API}, "sub2api:ws1:1515", "1515", 10, 0, 10)
	var mismatch *upstream.PriorityCompareAndSetError
	if !errors.Is(err, upstream.ErrPriorityCompareAndSetMismatch) || !errors.As(err, &mismatch) || mismatch.ActualPriority == nil || *mismatch.ActualPriority != 55 {
		t.Fatalf("fresh manual priority was not returned as a typed conflict: err=%v mismatch=%+v", err, mismatch)
	}
	if actioner.writes != 0 {
		t.Fatalf("fresh manual priority must prevent the remote write, writes=%d", actioner.writes)
	}
}

func TestAutomaticSub2APIPriorityDoesNotAdoptManualDesiredValueAsApplied(t *testing.T) {
	manualPriority := 1000
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Priority: &manualPriority, Models: "gpt-4o"}},
		},
	}
	policy := priorityGatePolicy()
	inventory := sub2APIPriorityGateInventory(policy, map[string]int{"1515": 10})
	targetID := "sub2api:ws1:1515"
	latency := 100
	healthStates := []ConnectionHealthState{{ConnectionID: targetID, ModelName: "gpt-4o", State: StateDegraded, LastSuccessLatencyMs: &latency}}
	repo := newFakeRepository()
	safetyRepo := newMutationSafetyRepository()
	actioner := &generationChangingPriorityActioner{}
	service := &Service{
		repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: reader, priorityActions: actioner,
	}
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
		prioritySyncRunMode{source: priorityActionCombined, reconcile: false, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	checkpoint := repo.priorityStates["user1|ws1|"+targetID]
	if actioner.writes != 0 || !checkpoint.Conflict || checkpoint.LastConflictPriority == nil || *checkpoint.LastConflictPriority != manualPriority || checkpoint.LastAppliedPriority != 10 {
		t.Fatalf("manual value equal to desired was incorrectly adopted as applied: writes=%d checkpoint=%+v", actioner.writes, checkpoint)
	}
}

func TestAutomaticSub2APIPriorityFencesGenerationImmediatelyBeforeWrite(t *testing.T) {
	actualPriority := 10
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Priority: &actualPriority, Models: "gpt-4o"}},
		},
	}
	safetyRepo := newMutationSafetyRepository()
	actioner := &generationChangingPriorityActioner{beforePrepared: func() error {
		_, err := safetyRepo.BumpMutationGeneration(context.Background(), "user1", "ws1", "1515")
		return err
	}}
	service := &Service{
		repo: newFakeRepository(), safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: reader, priorityActions: actioner,
	}
	err := service.updateAutomaticTargetPriority(context.Background(), "user1", "ws1",
		upstream.Session{Platform: upstream.PlatformSub2API}, "sub2api:ws1:1515", "1515", 1000, 0, 10)
	if !errors.Is(err, errStaleMutation) {
		t.Fatalf("generation change before write was not fenced: %v", err)
	}
	if actioner.writes != 0 {
		t.Fatalf("stale automatic mutation crossed the final generation fence, writes=%d", actioner.writes)
	}
}

func TestAutomaticSub2APIPriorityCompletesBeforeWaitingManualAdvancesGeneration(t *testing.T) {
	actualPriority := 10
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "1515", Priority: &actualPriority, Models: "gpt-4o"}},
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: accounts,
	}
	safetyRepo := newMutationSafetyRepository()
	coordinator := NewMutationCoordinator(safetyRepo)
	afterPrepared := make(chan struct{})
	releaseWrite := make(chan struct{})
	actioner := &generationChangingPriorityActioner{
		afterPrepared: afterPrepared,
		releaseWrite:  releaseWrite,
		afterWrite: func() {
			actualPriority = 1000
			updated := accounts["g1"]
			updated[0].Priority = &actualPriority
			accounts["g1"] = updated
		},
	}
	service := &Service{
		repo: newFakeRepository(), safetyRepo: safetyRepo, mutationCoordinator: coordinator,
		platformGroups: reader, priorityActions: actioner,
	}
	automaticErr := make(chan error, 1)
	go func() {
		automaticErr <- service.updateAutomaticTargetPriority(context.Background(), "user1", "ws1",
			upstream.Session{Platform: upstream.PlatformSub2API}, "sub2api:ws1:1515", "1515", 1000, 0, 10)
	}()

	select {
	case <-afterPrepared:
	case <-time.After(time.Second):
		t.Fatal("automatic mutation did not reach the boundary after final validation")
	}
	manualResult := make(chan *MutationSession, 1)
	manualError := make(chan error, 1)
	go func() {
		manual, err := coordinator.BeginManual(context.Background(), MutationKey{
			UserID: "user1", WorkspaceID: "ws1", AccountID: "1515",
		})
		if err != nil {
			manualError <- err
			return
		}
		manualResult <- manual
	}()
	select {
	case manual := <-manualResult:
		manual.Release()
		t.Fatal("manual generation advanced before the admitted automatic write completed")
	case err := <-manualError:
		t.Fatalf("manual mutation failed while waiting: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if generation, err := safetyRepo.MutationGeneration(context.Background(), "user1", "ws1", "1515"); err != nil || generation != 0 {
		t.Fatalf("waiting manual changed generation: generation=%d err=%v", generation, err)
	}

	close(releaseWrite)
	if err := <-automaticErr; err != nil {
		t.Fatalf("automatic mutation failed before manual acquired the lease: %v", err)
	}
	select {
	case err := <-manualError:
		t.Fatalf("manual mutation failed after automatic completion: %v", err)
	case manual := <-manualResult:
		defer manual.Release()
		if manual.Generation != 1 || manual.Validate(context.Background()) != nil {
			t.Fatalf("manual mutation did not atomically advance generation after acquiring the lease: %+v", manual)
		}
	case <-time.After(time.Second):
		t.Fatal("manual mutation did not acquire the lease after automatic completion")
	}
}

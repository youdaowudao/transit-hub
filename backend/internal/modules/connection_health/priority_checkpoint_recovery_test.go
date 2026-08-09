package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type failingPriorityCheckpointRepository struct {
	*fakeRepository
	failTargetConfirmation bool
	failWorkspaceFinal     bool
	workspaceWrites        int
}

type contextRejectingPriorityCheckpointRepository struct {
	*fakeRepository
}

func (r *contextRejectingPriorityCheckpointRepository) UpsertPrioritySyncState(ctx context.Context, state PrioritySyncState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.fakeRepository.UpsertPrioritySyncState(ctx, state)
}

func (r *contextRejectingPriorityCheckpointRepository) UpsertPriorityWorkspaceSyncState(ctx context.Context, state PriorityWorkspaceSyncState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.fakeRepository.UpsertPriorityWorkspaceSyncState(ctx, state)
}

type cancelingPriorityActioner struct {
	gatePriorityActioner
	cancel func()
}

func (a *cancelingPriorityActioner) UpdateAdminTargetPriority(session upstream.Session, targetID string, priority int) error {
	err := a.gatePriorityActioner.UpdateAdminTargetPriority(session, targetID, priority)
	if a.cancel != nil {
		a.cancel()
	}
	return err
}

func (r *failingPriorityCheckpointRepository) UpsertPrioritySyncState(ctx context.Context, state PrioritySyncState) error {
	if r.failTargetConfirmation && state.PendingPriority == nil {
		return errors.New("target checkpoint unavailable")
	}
	return r.fakeRepository.UpsertPrioritySyncState(ctx, state)
}

func (r *failingPriorityCheckpointRepository) UpsertPriorityWorkspaceSyncState(ctx context.Context, state PriorityWorkspaceSyncState) error {
	r.workspaceWrites++
	if r.failWorkspaceFinal && r.workspaceWrites >= 2 {
		return errors.New("workspace checkpoint unavailable")
	}
	return r.fakeRepository.UpsertPriorityWorkspaceSyncState(ctx, state)
}

func TestPriorityCheckpointFailureForcesFreshRecoveryWithoutDriftConflict(t *testing.T) {
	for _, test := range []struct {
		name                   string
		failTargetConfirmation bool
		failWorkspaceFinal     bool
	}{
		{name: "target checkpoint", failTargetConfirmation: true},
		{name: "workspace checkpoint", failWorkspaceFinal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseRepo := newFakeRepository()
			repo := &failingPriorityCheckpointRepository{
				fakeRepository: baseRepo, failTargetConfirmation: test.failTargetConfirmation, failWorkspaceFinal: test.failWorkspaceFinal,
			}
			now := time.Date(2026, time.August, 9, 3, 0, 0, 0, time.UTC)
			policy := priorityGatePolicy()
			latency := 100
			targetID := "sub2api:ws1:a"
			inventory := map[string]*priorityTargetInventory{
				targetID: {
					target:          AdminProbeTarget{TargetID: targetID, AccountID: "a", Models: []string{"gpt-4o"}},
					currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
				},
			}
			healthStates := []ConnectionHealthState{{
				ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
			}}
			actions := &gatePriorityActioner{}
			service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
			service.putAdminInventorySnapshot(context.Background(), "user1", "ws1", &adminWorkspaceInventory{complete: true}, now, time.Minute)

			service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
				"user1", "ws1", inventory, true, healthStates, nil,
				prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
			if len(actions.calls) != 1 {
				t.Fatalf("test did not reach the remote mutation: %+v", actions.calls)
			}
			if _, valid := service.getAdminInventorySnapshot("user1", "ws1", now); !valid {
				t.Fatal("old snapshot must stay cached until both checkpoints are durable")
			}
			uncertain := priorityWorkspaceState(t, baseRepo)
			if uncertain.InventoryStatus != "unknown" || uncertain.AppliedSignature != "" || uncertain.NextReconcileAt == nil {
				t.Fatalf("checkpoint uncertainty was not kept pending for a fresh reconcile: %+v", uncertain)
			}

			inventory[targetID].currentPriority = actions.calls[0].priority
			repo.failTargetConfirmation = false
			repo.failWorkspaceFinal = false
			states, err := baseRepo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
			if err != nil {
				t.Fatalf("list checkpoints for recovery: %v", err)
			}
			service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
				"user1", "ws1", inventory, true, healthStates, states,
				prioritySyncRunMode{source: priorityActionCombined, reconcile: true, write: true, persistenceContext: context.Background()})
			checkpoint := baseRepo.priorityStates["user1|ws1|"+targetID]
			if checkpoint.Conflict || priorityWorkspaceState(t, baseRepo).LastDecision == "drift_alert" {
				t.Fatalf("fresh authoritative value was misclassified as drift: workspace=%+v checkpoint=%+v", priorityWorkspaceState(t, baseRepo), checkpoint)
			}

			now = now.Add(31 * time.Second)
			states, err = baseRepo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
			if err != nil {
				t.Fatalf("list confirmed checkpoints: %v", err)
			}
			service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
				"user1", "ws1", inventory, true, healthStates, states,
				prioritySyncRunMode{source: priorityActionCombined, reconcile: true, write: true, persistenceContext: context.Background()})
			recovered := priorityWorkspaceState(t, baseRepo)
			if recovered.LastDecision == "drift_alert" || recovered.AppliedSignature == "" || recovered.PendingSignature != "" {
				t.Fatalf("workspace checkpoint did not close after the existing B window: workspace=%+v checkpoint=%+v", recovered, checkpoint)
			}
			if len(actions.calls) != 1 {
				t.Fatalf("checkpoint recovery repeated the confirmed remote mutation: %+v", actions.calls)
			}
		})
	}
}

func TestPriorityWritePersistsCheckpointAfterOperationContextCancels(t *testing.T) {
	baseRepo := newFakeRepository()
	repo := &contextRejectingPriorityCheckpointRepository{fakeRepository: baseRepo}
	now := time.Date(2026, time.August, 9, 4, 15, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	latency := 100
	targetID := "sub2api:ws1:a"
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target:          AdminProbeTarget{TargetID: targetID, AccountID: "a", Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		},
	}
	healthStates := []ConnectionHealthState{{
		ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
	}}
	operationCtx, cancelOperation := context.WithCancel(context.Background())
	defer cancelOperation()
	actions := &cancelingPriorityActioner{cancel: cancelOperation}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	service.syncWorkspacePrioritiesRunMode(operationCtx, upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	workspace := priorityWorkspaceState(t, baseRepo)
	checkpoint, exists := baseRepo.priorityStates["user1|ws1|"+targetID]
	if len(actions.calls) != 1 || !exists || checkpoint.PendingPriority != nil || checkpoint.LastAppliedPriority != actions.calls[0].priority || workspace.AppliedSignature == "" {
		t.Fatalf("canceled operation context prevented durable checkpoint state: calls=%+v checkpoint=%+v workspace=%+v", actions.calls, checkpoint, workspace)
	}
}

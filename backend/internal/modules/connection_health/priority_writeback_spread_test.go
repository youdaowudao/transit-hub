package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type priorityHTTPEvent struct {
	at       time.Time
	method   string
	targetID string
}

type selectivelyFailingPriorityActioner struct {
	calls      []priorityUpdateCall
	failTarget string
}

func (a *selectivelyFailingPriorityActioner) UpdateAdminTargetPriority(_ upstream.Session, targetID string, priority int) error {
	a.calls = append(a.calls, priorityUpdateCall{targetID: targetID, priority: priority})
	if targetID == a.failTarget {
		return fmt.Errorf("upstream write failed for %s", targetID)
	}
	return nil
}

func TestPriorityWriteBatchSplitsTargetsAcrossConfiguredSeconds(t *testing.T) {
	for _, test := range []struct {
		targetCount int
		spread      int
		want        []int
	}{
		{targetCount: 30, spread: 1, want: []int{30}},
		{targetCount: 30, spread: 2, want: []int{15, 15}},
		{targetCount: 30, spread: 3, want: []int{10, 10, 10}},
		{targetCount: 30, spread: 5, want: []int{6, 6, 6, 6, 6}},
		{targetCount: 7, spread: 5, want: []int{2, 2, 2, 1}},
	} {
		t.Run(fmt.Sprintf("targets_%d_spread_%d", test.targetCount, test.spread), func(t *testing.T) {
			service := &Service{}
			targetIDs := make([]string, test.targetCount)
			for index := range targetIDs {
				targetIDs[index] = fmt.Sprintf("target-%02d", index)
			}
			now := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
			got := make([]int, 0, test.spread)
			for slotIndex := range test.want {
				slot, waiting, refresh := service.preparePriorityWriteBatch("ws", "signature", 7, test.spread, targetIDs, now)
				if waiting || refresh {
					t.Fatalf("slot %d unexpectedly blocked: waiting=%v refresh=%v", slotIndex, waiting, refresh)
				}
				got = append(got, len(slot.targetIDs))
				if slot.targetCount != test.targetCount {
					t.Fatalf("slot target count=%d, want full batch target count %d", slot.targetCount, test.targetCount)
				}
				if !service.commitPriorityWriteBatchSlot("ws", slot, now) {
					t.Fatalf("slot %d did not commit", slotIndex)
				}
				if !slot.final {
					if _, waiting, _ := service.preparePriorityWriteBatch("ws", "signature", 7, test.spread, targetIDs, now); !waiting {
						t.Fatalf("slot %d did not enforce the next one-second boundary", slotIndex)
					}
					now = now.Add(time.Second)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("batch sizes=%v, want %v", got, test.want)
			}
		})
	}
}

func TestPriorityWriteBatchSkipsEmptySlotsAndDoesNotRetainZeroTargetPlan(t *testing.T) {
	service := &Service{}
	now := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	slot, waiting, refresh := service.preparePriorityWriteBatch("ws", "signature", 1, 5, nil, now)
	if waiting || refresh || !slot.final || len(slot.targetIDs) != 0 || len(service.priorityBatches) != 0 {
		t.Fatalf("zero-target plan retained an empty batch: slot=%+v waiting=%v refresh=%v batches=%+v", slot, waiting, refresh, service.priorityBatches)
	}
}

func TestPriorityWriteBatchLatestPlanReplacesUnsentSlotsAfterFreshSnapshot(t *testing.T) {
	service := &Service{}
	now := time.Date(2026, time.August, 9, 0, 30, 0, 0, time.UTC)
	targetIDs := []string{"a", "b", "c", "d", "e", "f"}
	first, waiting, refresh := service.preparePriorityWriteBatch("ws", "plan-a", 1, 3, targetIDs, now)
	if waiting || refresh || len(first.targetIDs) != 2 || !service.commitPriorityWriteBatchSlot("ws", first, now) {
		t.Fatalf("first plan did not start: slot=%+v waiting=%v refresh=%v", first, waiting, refresh)
	}
	if _, waiting, refresh = service.preparePriorityWriteBatch("ws", "plan-b", 1, 3, targetIDs, now.Add(time.Second)); waiting || !refresh {
		t.Fatalf("changed plan on the same snapshot must request refresh: waiting=%v refresh=%v", waiting, refresh)
	}
	replacement, waiting, refresh := service.preparePriorityWriteBatch("ws", "plan-b", 2, 3, targetIDs, now.Add(time.Second))
	if waiting || refresh || len(replacement.targetIDs) != 2 || replacement.signature != "plan-b" {
		t.Fatalf("fresh replacement plan did not start from its first slot: %+v waiting=%v refresh=%v", replacement, waiting, refresh)
	}
}

func TestPriorityWritebackSpreadProcessesThirtyTargetsInThreeSlots(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 1, 0, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 30)
	healthStates := make([]ConnectionHealthState, 0, 30)
	for index := 0; index < 30; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{
			ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
		})
	}
	actions := &gatePriorityActioner{}
	nextReconcileAt := now.Add(30 * time.Second)
	repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", InventoryStatus: "ready", NextReconcileAt: &nextReconcileAt,
	}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}

	for slot := 1; slot <= 3; slot++ {
		states, err := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
		if err != nil {
			t.Fatalf("list checkpoints before slot %d: %v", slot, err)
		}
		service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
			"user1", "ws1", inventory, true, healthStates, states,
			prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
		if got, want := len(actions.calls), slot*10; got != want {
			t.Fatalf("after slot %d writes=%d, want %d", slot, got, want)
		}
		workspace := priorityWorkspaceState(t, repo)
		if workspace.PendingTargetCount != 30-slot*10 {
			t.Fatalf("after slot %d pending=%d, want %d", slot, workspace.PendingTargetCount, 30-slot*10)
		}
		if workspace.LastWriteRoundTargetCount != 30 {
			t.Fatalf("after slot %d last round target count=%d, want 30", slot, workspace.LastWriteRoundTargetCount)
		}
		if slot < 3 && (workspace.LastDecision != "pending" || workspace.LastSuppressionReason != "writeback_spread") {
			t.Fatalf("intermediate slot %d state=%+v", slot, workspace)
		}
		if slot < 3 && (workspace.NextReconcileAt == nil || !workspace.NextReconcileAt.Equal(nextReconcileAt)) {
			t.Fatalf("intermediate slot %d changed the existing reconcile schedule: %+v", slot, workspace)
		}
		now = now.Add(time.Second)
	}
	workspace := priorityWorkspaceState(t, repo)
	if workspace.LastDecision != "applied" || workspace.PendingSignature != "" || workspace.AppliedSignature == "" {
		t.Fatalf("final slot did not close the workspace plan: %+v", workspace)
	}
}

type failingFirstPriorityWorkspaceRepository struct {
	*fakeRepository
}

func (r *failingFirstPriorityWorkspaceRepository) UpsertPriorityWorkspaceSyncState(ctx context.Context, state PriorityWorkspaceSyncState) error {
	return fmt.Errorf("workspace checkpoint unavailable")
}

func TestPriorityWritebackRecordsFullBatchCountForNonDivisibleSpread(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 1, 7, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 5
	inventory := make(map[string]*priorityTargetInventory, 7)
	healthStates := make([]ConnectionHealthState, 0, 7)
	for index := 0; index < 7; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency})
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: &failingFirstPriorityWorkspaceRepository{fakeRepository: repo}, priorityActions: actions, now: func() time.Time { return now }}

	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	if len(actions.calls) != 0 {
		t.Fatalf("failed first checkpoint must send zero upstream writes: %+v", actions.calls)
	}
	if _, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1"); err != nil {
		t.Fatalf("read unchanged workspace state: %v", err)
	}

	service.repo = repo
	for slot := 0; slot < 4; slot++ {
		states, err := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
		if err != nil {
			t.Fatalf("list checkpoints before slot %d: %v", slot, err)
		}
		service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
			"user1", "ws1", inventory, true, healthStates, states,
			prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 2})
		now = now.Add(time.Second)
	}
	workspace := priorityWorkspaceState(t, repo)
	if workspace.LastWriteRoundTargetCount != 7 || workspace.PendingTargetCount != 0 {
		t.Fatalf("non-divisible batch count was not preserved: %+v", workspace)
	}
}

func TestPriorityWritebackCheckpointFailureLeavesExistingRoundCountUnchanged(t *testing.T) {
	baseRepo := newFakeRepository()
	baseRepo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastWriteRoundTargetCount: 11,
	}
	policy := priorityGatePolicy()
	targetID := "sub2api:ws1:a"
	latency := 100
	inventory := map[string]*priorityTargetInventory{targetID: {
		target:          AdminProbeTarget{TargetID: targetID, AccountID: "a", Models: []string{"gpt-4o"}},
		currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
	}}
	healthStates := []ConnectionHealthState{{ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency}}
	actions := &gatePriorityActioner{}
	service := &Service{repo: &failingFirstPriorityWorkspaceRepository{fakeRepository: baseRepo}, priorityActions: actions}
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	workspace := priorityWorkspaceState(t, baseRepo)
	if len(actions.calls) != 0 || workspace.LastWriteRoundTargetCount != 11 {
		t.Fatalf("checkpoint failure changed history or sent a write: calls=%+v workspace=%+v", actions.calls, workspace)
	}
}

func TestPriorityWritebackSpreadFailureKeepsFailedTargetInPendingCount(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 1, 15, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 30)
	healthStates := make([]ConnectionHealthState, 0, 30)
	for index := 0; index < 30; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{
			ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
		})
	}
	actions := &selectivelyFailingPriorityActioner{failTarget: "00"}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	workspace := priorityWorkspaceState(t, repo)
	if len(actions.calls) != 10 || actions.calls[0].targetID != "00" || workspace.PendingTargetCount != 21 || workspace.AppliedSignature != "" || workspace.LastDecision != "failed" {
		t.Fatalf("failed slot did not preserve the real pending total: calls=%+v workspace=%+v", actions.calls, workspace)
	}
	if workspace.LastWriteRoundTargetCount != 30 {
		t.Fatalf("failed slot changed the full round target count: workspace=%+v", workspace)
	}
	for _, call := range actions.calls[1:] {
		if call.targetID == "00" {
			t.Fatalf("failed target retried inside its original slot: %+v", actions.calls)
		}
	}

	// A failed write must retain the workspace retry interval even when the failed
	// target crosses priority bands. The one-second writeback tick cannot retry it.
	now = now.Add(time.Second)
	states, err := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	if err != nil {
		t.Fatalf("list checkpoints before retry: %v", err)
	}
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 2})
	if len(actions.calls) != 10 {
		t.Fatalf("failed write retried before the workspace interval: %+v", actions.calls)
	}
	workspace = priorityWorkspaceState(t, repo)
	if workspace.LastDecision != "pending" || workspace.LastSuppressionReason != "min_write_interval" {
		t.Fatalf("failed write did not enter the existing retry protection: %+v", workspace)
	}

	// After the interval, the failed checkpoint may retry without changing the
	// original round total.
	now = now.Add(29 * time.Second)
	states, err = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	if err != nil {
		t.Fatalf("list checkpoints after retry interval: %v", err)
	}
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 3})
	workspace = priorityWorkspaceState(t, repo)
	if workspace.LastWriteRoundTargetCount != 30 {
		t.Fatalf("retry changed the full round target count: workspace=%+v", workspace)
	}
}

func TestPriorityWritebackSpreadDistributesRealSub2APIMutationsWithoutExtraWrites(t *testing.T) {
	now := time.Date(2026, time.August, 9, 1, 30, 0, 0, time.UTC)
	remotePriorities := make(map[string]int, 30)
	events := make([]priorityHTTPEvent, 0, 30)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts/bulk-update" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode Sub2API priority bulk update: %v", err)
			}
			accountIDs, ok := body["account_ids"].([]any)
			if !ok || len(accountIDs) != 1 {
				t.Fatalf("unexpected Sub2API account_ids: %+v", body)
			}
			targetID := strconv.Itoa(int(accountIDs[0].(float64)))
			remotePriorities[targetID] = int(body["priority"].(float64))
			events = append(events, priorityHTTPEvent{at: now, method: r.Method, targetID: targetID})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	repo := newFakeRepository()
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 30)
	healthStates := make([]ConnectionHealthState, 0, 30)
	for index := 0; index < 30; index++ {
		accountID := strconv.Itoa(100 + index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		remotePriorities[accountID] = 1
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{
			ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
		})
	}
	platform := upstream.NewPlatformService(upstream.NewHTTPClient(server.Client()))
	service := &Service{repo: repo, priorityActions: platform, now: func() time.Time { return now }}
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "token-1", TokenType: "Bearer"}
	startedAt := now
	for slot := 0; slot < 3; slot++ {
		states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
		service.syncWorkspacePrioritiesRunMode(context.Background(), session, "user1", "ws1", inventory, true, healthStates, states,
			prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
		now = now.Add(time.Second)
	}

	postsBySecond := map[int]int{}
	for _, event := range events {
		postsBySecond[int(event.at.Sub(startedAt)/time.Second)]++
	}
	if len(events) != 30 {
		t.Fatalf("real Sub2API mutation total changed: POST=%d", len(events))
	}
	for second := 0; second < 3; second++ {
		if postsBySecond[second] != 10 {
			t.Fatalf("POST distribution=%v, want 10 writes in second %d", postsBySecond, second)
		}
	}
}

func TestPriorityWorkspacePlanSignatureTracksDesiredPriorityNotRawLatency(t *testing.T) {
	policy := priorityGatePolicy()
	targetID := "sub2api:ws1:a"
	latency := 100
	inventory := sub2APIPriorityGateInventory(policy, map[string]int{"a": 10})
	health := []ConnectionHealthState{{ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency}}
	healthyPlan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, health)

	slowerLatency := 9000
	health[0].LastSuccessLatencyMs = &slowerLatency
	slowerPlan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, health)
	if slowerPlan.signature != healthyPlan.signature || slowerPlan.desiredPriorityByTarget[targetID] != 10 {
		t.Fatalf("raw latency without a desired change altered the plan: before=%+v after=%+v", healthyPlan, slowerPlan)
	}

	health[0].State = StateDegraded
	degradedPlan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, health)
	if degradedPlan.signature == healthyPlan.signature || degradedPlan.desiredPriorityByTarget[targetID] != 1000 {
		t.Fatalf("health band change did not alter desired signature: healthy=%+v degraded=%+v", healthyPlan, degradedPlan)
	}
}

func TestPriorityWritebackExactUnprobedAndSuspendedValuesRecoverImmediately(t *testing.T) {
	for _, current := range []int{10000, 100000} {
		t.Run(fmt.Sprintf("from_%d", current), func(t *testing.T) {
			repo := newFakeRepository()
			now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
			policy := priorityGatePolicy()
			targetID := "sub2api:ws1:a"
			inventory := sub2APIPriorityGateInventory(policy, map[string]int{"a": current})
			repo.priorityStates["user1|ws1|"+targetID] = PrioritySyncState{
				UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
				OriginalPriority: current, LastAppliedPriority: current,
			}
			recentWrite := now
			repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
				UserID: "user1", AdminAccountID: "ws1", AppliedSignature: "legacy-owner-order-signature",
				LastWriteAttemptAt: &recentWrite, LastWriteSuccessAt: &recentWrite,
			}
			actions := &gatePriorityActioner{}
			service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
			now = now.Add(time.Second)
			runPriorityWriteGateForPlatform(service, repo, inventory, []ConnectionHealthState{{
				ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy,
			}}, upstream.PlatformSub2API)
			if len(actions.calls) != 1 || actions.calls[0].priority != 10 {
				t.Fatalf("%d -> 10 did not bypass the 30-second merge window: calls=%+v state=%+v", current, actions.calls, priorityWorkspaceState(t, repo))
			}
		})
	}
}

func TestPriorityWritebackSignatureRevertReplacesTargetPendingWithLatestPlan(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 2, 30, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	targetID := "sub2api:ws1:a"
	latency := 100
	inventory := sub2APIPriorityGateInventory(policy, map[string]int{"a": 1000})
	healthStates := []ConnectionHealthState{{
		ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
	}}
	healthyPlan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, healthStates)
	degraded := healthStates[0]
	degraded.State = StateDegraded
	degradedPlan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, []ConnectionHealthState{degraded})
	pending := degradedPlan.desiredPriorityByTarget[targetID]
	repo.priorityStates["user1|ws1|"+targetID] = PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 10, LastAppliedPriority: 10, PendingPriority: &pending, PendingSource: "normal",
	}
	repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", AppliedSignature: healthyPlan.signature,
		PendingSignature: degradedPlan.signature, PendingSince: &now,
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	workspace := priorityWorkspaceState(t, repo)
	checkpoint := repo.priorityStates["user1|ws1|"+targetID]
	if len(actions.calls) != 1 || actions.calls[0].priority != 10 || workspace.AppliedSignature != healthyPlan.signature || workspace.PendingSignature != "" || checkpoint.PendingPriority != nil {
		t.Fatalf("reverted plan did not replace stale target pending: calls=%+v workspace=%+v checkpoint=%+v", actions.calls, workspace, checkpoint)
	}
}

func TestPriorityWritebackIncidentOwnershipDoesNotMarkNormalPlanApplied(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 2, 45, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	targetID := "sub2api:ws1:a"
	inventory := sub2APIPriorityGateInventory(policy, map[string]int{"a": 10000})
	healthStates := []ConnectionHealthState{{ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy}}
	incidentPriority := 100000
	repo.priorityStates["user1|ws1|"+targetID] = PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 10000, LastAppliedPriority: 10000,
		PendingPriority: &incidentPriority, PendingSource: SafetySourceHealthIncident, PendingEpoch: 7,
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	held := priorityWorkspaceState(t, repo)
	if len(actions.calls) != 0 || held.AppliedSignature != "" || held.PendingSignature == "" || repo.priorityStates["user1|ws1|"+targetID].PendingSource != SafetySourceHealthIncident {
		t.Fatalf("normal write consumed or applied incident-owned work: calls=%+v workspace=%+v checkpoint=%+v", actions.calls, held, repo.priorityStates["user1|ws1|"+targetID])
	}
	if held.LastWriteRoundTargetCount != 0 {
		t.Fatalf("incident-held work must not create a normal write round: %+v", held)
	}

	checkpoint := repo.priorityStates["user1|ws1|"+targetID]
	clearPriorityPending(&checkpoint)
	repo.priorityStates["user1|ws1|"+targetID] = checkpoint
	now = now.Add(time.Second)
	states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	recovered := priorityWorkspaceState(t, repo)
	if len(actions.calls) != 1 || actions.calls[0].priority != 10 || recovered.AppliedSignature == "" || recovered.PendingSignature != "" {
		t.Fatalf("normal plan did not resume after incident release: calls=%+v workspace=%+v", actions.calls, recovered)
	}
	if recovered.LastWriteRoundTargetCount != 1 {
		t.Fatalf("released normal target must start a one-account round: %+v", recovered)
	}
}

func TestPriorityWritebackRecentSuccessStartsCompleteBatchWithCrossBandFirst(t *testing.T) {
	for _, test := range []struct {
		spreadSeconds int
		wantCalls     []int
	}{
		{spreadSeconds: 1, wantCalls: []int{10}},
		{spreadSeconds: 3, wantCalls: []int{4, 8, 10}},
		{spreadSeconds: 5, wantCalls: []int{2, 4, 6, 8, 10}},
	} {
		spreadSeconds := test.spreadSeconds
		t.Run(fmt.Sprintf("spread_%d", spreadSeconds), func(t *testing.T) {
			repo := newFakeRepository()
			now := time.Date(2026, time.August, 9, 3, 15, 0, 0, time.UTC)
			policy := priorityGatePolicy()
			policy.PrioritySyncPreset.WritebackSpreadSeconds = spreadSeconds
			priorities := make(map[string]int, 10)
			healthStates := make([]ConnectionHealthState, 0, 10)
			for index := 0; index < 10; index++ {
				accountID := string(rune('a' + index))
				priorities[accountID] = 10 + index
				latency := 100 + index
				state := StateHealthy
				if index == 0 {
					state = StateDegraded
				}
				healthStates = append(healthStates, ConnectionHealthState{
					ConnectionID: "sub2api:ws1:" + accountID, ModelName: "gpt-4o", State: state, LastSuccessLatencyMs: &latency,
				})
			}
			inventory := sub2APIPriorityGateInventory(policy, priorities)
			recent := now
			repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
				UserID: "user1", AdminAccountID: "ws1", LastWriteAttemptAt: &recent, LastWriteSuccessAt: &recent,
			}
			actions := &gatePriorityActioner{}
			service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}

			for slotIndex, wantCalls := range test.wantCalls {
				states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
				service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
					"user1", "ws1", inventory, true, healthStates, states,
					prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
				if len(actions.calls) != wantCalls {
					t.Fatalf("after slot %d writes=%d, want %d: %+v", slotIndex+1, len(actions.calls), wantCalls, actions.calls)
				}
				workspace := priorityWorkspaceState(t, repo)
				if workspace.LastWriteRoundTargetCount != 10 || workspace.PendingTargetCount != 10-wantCalls {
					t.Fatalf("slot %d did not retain the complete ten-account round: %+v", slotIndex+1, workspace)
				}
				now = now.Add(time.Second)
			}
			if actions.calls[0].targetID != "a" || actions.calls[0].priority != 1000 {
				t.Fatalf("cross-band target did not stay first: %+v", actions.calls)
			}
			if final := priorityWorkspaceState(t, repo); final.LastDecision != "applied" || final.PendingTargetCount != 0 {
				t.Fatalf("complete normal batch did not converge: %+v", final)
			}
		})
	}
}

func TestPriorityWritebackHistoricalSuccessfulAttemptDoesNotHideNewRoundCount(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 3, 25, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 1
	latency := 100
	inventory := sub2APIPriorityGateInventory(policy, map[string]int{"a": 11, "b": 12})
	healthStates := []ConnectionHealthState{
		{ConnectionID: "sub2api:ws1:a", ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency},
		{ConnectionID: "sub2api:ws1:b", ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency},
	}
	plan := buildPriorityWorkspacePlan(upstream.Session{Platform: upstream.PlatformSub2API}, inventory, true, healthStates)
	recent := now.Add(-time.Second)
	pendingSince := now.Add(-2 * time.Second)
	repo.priorityWorkspaceStates["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", PendingSignature: plan.signature, PendingSince: &pendingSince,
		LastDecision: "pending", LastSuppressionReason: "writeback_queued", LastWriteRoundTargetCount: 9,
		LastWriteAttemptAt: &recent, LastWriteSuccessAt: &recent,
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}

	states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	workspace := priorityWorkspaceState(t, repo)
	if len(actions.calls) != 2 || workspace.LastWriteRoundTargetCount != 2 || workspace.PendingTargetCount != 0 {
		t.Fatalf("historical successful attempt hid the new batch total: calls=%+v workspace=%+v", actions.calls, workspace)
	}
}

func TestPriorityWritebackIncidentDoesNotInterruptThreeNormalSlots(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 3, 30, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 31)
	healthStates := make([]ConnectionHealthState, 0, 31)
	for index := 0; index < 31; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{
			ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
		})
	}
	incidentPriority := 100000
	incidentTargetID := "sub2api:ws1:30"
	repo.priorityStates["user1|ws1|"+incidentTargetID] = PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: incidentTargetID,
		OriginalPriority: 1, LastAppliedPriority: 1, PendingPriority: &incidentPriority, PendingSource: SafetySourceHealthIncident,
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	for slot := 1; slot <= 3; slot++ {
		states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
		service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
			"user1", "ws1", inventory, true, healthStates, states,
			prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
		if got, want := len(actions.calls), slot*10; got != want {
			t.Fatalf("slot %d writes=%d, want %d: %+v", slot, got, want, actions.calls)
		}
		now = now.Add(time.Second)
	}
	workspace := priorityWorkspaceState(t, repo)
	if workspace.AppliedSignature != "" || workspace.PendingSignature == "" || workspace.PendingTargetCount != 1 || workspace.LastDecision != "pending" {
		t.Fatalf("incident-held target incorrectly closed the normal plan: %+v", workspace)
	}
	if workspace.LastWriteRoundTargetCount != 30 {
		t.Fatalf("incident-held target must stay outside the 30-account normal batch: %+v", workspace)
	}
}

func TestPriorityWritebackIncidentReleaseReplacesUnsentBatchSlots(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 3, 45, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 31)
	healthStates := make([]ConnectionHealthState, 0, 31)
	for index := 0; index < 31; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{
			ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency,
		})
	}
	incidentTargetID := "sub2api:ws1:00"
	incidentPriority := 100000
	repo.priorityStates["user1|ws1|"+incidentTargetID] = PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: incidentTargetID,
		OriginalPriority: 1, LastAppliedPriority: 1, PendingPriority: &incidentPriority, PendingSource: SafetySourceHealthIncident,
	}
	actions := &gatePriorityActioner{}
	service := &Service{repo: repo, priorityActions: actions, now: func() time.Time { return now }}
	states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	if len(actions.calls) != 10 {
		t.Fatalf("first batch slot wrote %d targets, want 10", len(actions.calls))
	}
	applySub2APIPriorityActionCalls(t, inventory, actions.calls)
	checkpoint := repo.priorityStates["user1|ws1|"+incidentTargetID]
	clearPriorityPending(&checkpoint)
	repo.priorityStates["user1|ws1|"+incidentTargetID] = checkpoint
	now = now.Add(time.Second)
	states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	if len(actions.calls) != 10 {
		t.Fatalf("released incident was incorrectly dropped into an old unsent slot: %+v", actions.calls)
	}
	if workspace := priorityWorkspaceState(t, repo); workspace.LastInventoryError != "priority_plan_replaced" {
		t.Fatalf("released incident did not force a fresh plan: %+v", workspace)
	}

	states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	service.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 2})
	if len(actions.calls) != 17 || actions.calls[10].targetID != "00" {
		t.Fatalf("fresh replacement did not include the released incident target: %+v", actions.calls)
	}
	if workspace := priorityWorkspaceState(t, repo); workspace.LastWriteRoundTargetCount != 21 {
		t.Fatalf("fresh replacement did not record its own full batch count: %+v", workspace)
	}
}

func TestPriorityPendingTargetsSkipCompletedRestoreAndPrioritizeCrossBandRestore(t *testing.T) {
	completedTargetID := "sub2api:ws1:completed"
	completedInventory := map[string]*priorityTargetInventory{
		completedTargetID: {target: AdminProbeTarget{TargetID: completedTargetID, AccountID: "completed"}, currentPriority: 10, priorityPresent: true},
	}
	ids, pending, urgent := priorityPendingTargetIDs(upstream.PlatformSub2API, priorityWorkspacePlan{
		managed: map[string]*priorityTargetInventory{}, missingSortTargets: map[string]struct{}{}, missingPriorityTargets: map[string]struct{}{},
	}, completedInventory, []PrioritySyncState{{TargetID: completedTargetID, OriginalPriority: 10, LastAppliedPriority: 10}})
	if len(ids) != 0 || pending != 0 || urgent != 0 {
		t.Fatalf("already restored target must not consume a pending slot: ids=%v pending=%d urgent=%d", ids, pending, urgent)
	}

	crossTargetID := "sub2api:ws1:cross"
	sameTargetID := "sub2api:ws1:same"
	restoreInventory := map[string]*priorityTargetInventory{
		crossTargetID: {target: AdminProbeTarget{TargetID: crossTargetID, AccountID: "cross"}, currentPriority: 100000, priorityPresent: true},
		sameTargetID:  {target: AdminProbeTarget{TargetID: sameTargetID, AccountID: "same"}, currentPriority: 11, priorityPresent: true},
	}
	ids, pending, urgent = priorityPendingTargetIDs(upstream.PlatformSub2API, priorityWorkspacePlan{
		managed: map[string]*priorityTargetInventory{}, missingSortTargets: map[string]struct{}{}, missingPriorityTargets: map[string]struct{}{},
	}, restoreInventory, []PrioritySyncState{
		{TargetID: crossTargetID, OriginalPriority: 10, LastAppliedPriority: 100000},
		{TargetID: sameTargetID, OriginalPriority: 10, LastAppliedPriority: 11},
	})
	if fmt.Sprint(ids) != fmt.Sprint([]string{crossTargetID, sameTargetID}) || pending != 2 || urgent != 1 {
		t.Fatalf("cross-band restore must precede same-band restore: ids=%v pending=%d urgent=%d", ids, pending, urgent)
	}
}

func TestPriorityWritebackSecondServiceSkipsStaleBatchSnapshotWithoutDrift(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, time.August, 9, 4, 0, 0, 0, time.UTC)
	policy := priorityGatePolicy()
	policy.PrioritySyncPreset.WritebackSpreadSeconds = 3
	inventory := make(map[string]*priorityTargetInventory, 6)
	healthStates := make([]ConnectionHealthState, 0, 6)
	for index := 0; index < 6; index++ {
		accountID := fmt.Sprintf("%02d", index)
		targetID := "sub2api:ws1:" + accountID
		latency := 100 + index
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: 1, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
		healthStates = append(healthStates, ConnectionHealthState{ConnectionID: targetID, ModelName: "gpt-4o", State: StateHealthy, LastSuccessLatencyMs: &latency})
	}
	firstActions := &gatePriorityActioner{}
	secondActions := &gatePriorityActioner{}
	first := &Service{repo: repo, priorityActions: firstActions, now: func() time.Time { return now }}
	second := &Service{repo: repo, priorityActions: secondActions, now: func() time.Time { return now }}
	states, _ := repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	first.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	if len(firstActions.calls) != 2 {
		t.Fatalf("first service did not complete its first slot: %+v", firstActions.calls)
	}
	states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	second.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
	if len(secondActions.calls) != 0 {
		t.Fatalf("second service consumed another service's cached batch: %+v", secondActions.calls)
	}
	for _, state := range repo.priorityStates {
		if state.Conflict {
			t.Fatalf("stale batch snapshot became an artificial manual conflict: %+v", state)
		}
	}

	applySub2APIPriorityActionCalls(t, inventory, firstActions.calls)
	now = now.Add(time.Second)
	for slot := 0; slot < 2; slot++ {
		states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
		first.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
			"user1", "ws1", inventory, true, healthStates, states,
			prioritySyncRunMode{source: priorityActionWriteback, write: true, persistenceContext: context.Background(), snapshotGeneration: 1})
		applySub2APIPriorityActionCalls(t, inventory, firstActions.calls)
		now = now.Add(time.Second)
	}
	states, _ = repo.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	second.syncWorkspacePrioritiesRunMode(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, states,
		prioritySyncRunMode{source: priorityActionCombined, reconcile: true, persistenceContext: context.Background(), snapshotGeneration: 2})
	for _, state := range repo.priorityStates {
		if state.Conflict {
			t.Fatalf("fresh second-service reconcile became an artificial manual conflict: %+v", state)
		}
	}
}

func sub2APIPriorityGateInventory(policy Policy, priorities map[string]int) map[string]*priorityTargetInventory {
	inventory := make(map[string]*priorityTargetInventory, len(priorities))
	for accountID, priority := range priorities {
		targetID := "sub2api:ws1:" + accountID
		inventory[targetID] = &priorityTargetInventory{
			target:          AdminProbeTarget{TargetID: targetID, AccountID: accountID, Models: []string{"gpt-4o"}},
			currentPriority: priority, priorityPresent: true, policies: []Policy{policy}, fallbackMultipliers: []float64{0.4},
		}
	}
	return inventory
}

func applySub2APIPriorityActionCalls(t *testing.T, inventory map[string]*priorityTargetInventory, calls []priorityUpdateCall) {
	t.Helper()
	for _, call := range calls {
		targetID := "sub2api:ws1:" + call.targetID
		item := inventory[targetID]
		if item == nil {
			t.Fatalf("Sub2API action target missing from inventory: target_id=%s", targetID)
		}
		item.currentPriority = call.priority
	}
}

package connection_health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type mutableSub2APISchedulerReader struct {
	mu              sync.Mutex
	priority        int
	priorityWrites  []priorityUpdateCall
	credentialReads int
	credential      upstream.ProbeCredential
}

type failingPriorityConfirmationRepository struct {
	*fakeRepository
	err error
}

func (r *failingPriorityConfirmationRepository) UpsertPrioritySyncState(context.Context, PrioritySyncState) error {
	return r.err
}

func (r *mutableSub2APISchedulerReader) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{{ID: "g1", Name: "default", Platform: string(upstream.PlatformSub2API)}}, nil
}

func (r *mutableSub2APISchedulerReader) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	priority := r.priority
	return []upstream.AdminGroupAccountInfo{{
		ID: "manual", Name: "manual", Status: "active", Models: "gpt-4o",
		Priority: &priority, Schedulable: boolPointer(true),
	}}, nil
}

func (r *mutableSub2APISchedulerReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentialReads++
	return r.credential, nil
}

func (r *mutableSub2APISchedulerReader) UpdateAdminTargetPriority(_ upstream.Session, targetID string, priority int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.priorityWrites = append(r.priorityWrites, priorityUpdateCall{targetID: targetID, priority: priority})
	r.priority = priority
	return nil
}

func (r *mutableSub2APISchedulerReader) snapshot() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.priority, len(r.priorityWrites), r.credentialReads
}

func TestHealthPrioritySync_Sub2APIManualPriorityNeverEntersManagedBand(t *testing.T) {
	for _, test := range []struct {
		name              string
		currentPriority   int
		wantPriorityWrite bool
	}{
		{name: "priority 1 is manual", currentPriority: 1},
		{name: "priority 9 is manual", currentPriority: 9},
		{name: "priority 10 is managed", currentPriority: 10, wantPriorityWrite: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeRepository()
			actions := &fakeTargetPriorityActioner{}
			policy := sub2APIProbePolicy(false)
			policy.PriorityMode = PriorityModeMultiplier
			policy.StrategyMode = StrategyModeHealthProbe
			multiplier := 0.4
			targetID := "sub2api:ws1:manual"
			priority := test.currentPriority
			inventory := map[string]*priorityTargetInventory{
				targetID: {
					target: AdminProbeTarget{
						TargetID: targetID, Platform: string(upstream.PlatformSub2API),
						AccountID: "manual", Models: []string{"gpt-4o"},
					},
					account: upstream.AdminGroupAccountInfo{
						ID: "manual", Priority: &priority, Models: "gpt-4o",
					},
					policies: []Policy{policy},
					upstreamMultiplier: upstreamMultiplierResolution{
						status: MultiplierResolutionResolved,
						info:   upstreamKeyGroupInfo{effectiveMultiplier: &multiplier},
					},
					currentPriority: priority,
					priorityPresent: true,
				},
			}
			service := &Service{repo: repo, priorityActions: actions}

			service.syncWorkspacePriorities(
				context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
				"user1", "ws1", inventory, true, nil, nil,
			)

			if test.wantPriorityWrite {
				if len(actions.calls) != 1 || actions.calls[0].priority < 10 {
					t.Fatalf("managed Priority %d must retain health synchronization: writes=%+v", test.currentPriority, actions.calls)
				}
				if _, exists := repo.priorityStates["user1|ws1|"+targetID]; !exists {
					t.Fatalf("managed Priority %d must retain its sync checkpoint", test.currentPriority)
				}
				return
			}
			if len(actions.calls) != 0 {
				t.Fatalf("manual Priority %d must not be moved into a health band: writes=%+v", test.currentPriority, actions.calls)
			}
			if _, exists := repo.priorityStates["user1|ws1|"+targetID]; exists {
				t.Fatalf("manual Priority %d must not create a health sync checkpoint: %+v", test.currentPriority, repo.priorityStates)
			}
		})
	}
}

func TestHealthPrioritySync_Sub2APIExistingManualChangeRecordsConflict(t *testing.T) {
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	multiplier := 0.4
	manualPriority := 1
	targetID := "sub2api:ws1:manual"
	stored := PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 50, LastAppliedPriority: 10,
	}
	repo.priorityStates["user1|ws1|"+targetID] = stored
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target: AdminProbeTarget{
				TargetID: targetID, Platform: string(upstream.PlatformSub2API),
				AccountID: "manual", Models: []string{"gpt-4o"},
			},
			account: upstream.AdminGroupAccountInfo{
				ID: "manual", Priority: &manualPriority, Models: "gpt-4o",
			},
			policies: []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{
				status: MultiplierResolutionResolved,
				info:   upstreamKeyGroupInfo{effectiveMultiplier: &multiplier},
			},
			currentPriority: manualPriority,
			priorityPresent: true,
		},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, nil, []PrioritySyncState{stored},
	)

	if len(actions.calls) != 0 {
		t.Fatalf("existing manual Priority change must not be overwritten: %+v", actions.calls)
	}
	got := repo.priorityStates["user1|ws1|"+targetID]
	if !got.Conflict || got.LastConflictPriority == nil || *got.LastConflictPriority != manualPriority {
		t.Fatalf("existing manual Priority change must preserve conflict evidence: %+v", got)
	}
}

func TestHealthPrioritySync_Sub2APIManualPriorityDoesNotConsumeManagedRank(t *testing.T) {
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	manualPriority := 1
	managedPriority := 50
	manualMultiplier := 0.1
	managedMultiplier := 0.2
	inventory := map[string]*priorityTargetInventory{
		"sub2api:ws1:manual": {
			target: AdminProbeTarget{
				TargetID: "sub2api:ws1:manual", Platform: string(upstream.PlatformSub2API),
				AccountID: "manual", Models: []string{"gpt-4o"},
			},
			account:  upstream.AdminGroupAccountInfo{ID: "manual", Priority: &manualPriority, Models: "gpt-4o"},
			policies: []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{
				status: MultiplierResolutionResolved,
				info:   upstreamKeyGroupInfo{effectiveMultiplier: &manualMultiplier},
			},
			currentPriority: manualPriority,
			priorityPresent: true,
		},
		"sub2api:ws1:managed": {
			target: AdminProbeTarget{
				TargetID: "sub2api:ws1:managed", Platform: string(upstream.PlatformSub2API),
				AccountID: "managed", Models: []string{"gpt-4o"},
			},
			account:  upstream.AdminGroupAccountInfo{ID: "managed", Priority: &managedPriority, Models: "gpt-4o"},
			policies: []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{
				status: MultiplierResolutionResolved,
				info:   upstreamKeyGroupInfo{effectiveMultiplier: &managedMultiplier},
			},
			currentPriority: managedPriority,
			priorityPresent: true,
		},
	}
	healthStates := []ConnectionHealthState{
		{ConnectionID: "sub2api:ws1:manual", ModelName: "gpt-4o", State: StateHealthy},
		{ConnectionID: "sub2api:ws1:managed", ModelName: "gpt-4o", State: StateHealthy},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
	)

	if len(actions.calls) != 1 || actions.calls[0].targetID != "managed" || actions.calls[0].priority != 10 {
		t.Fatalf("manual Priority must not consume the first managed health rank: %+v", actions.calls)
	}
}

func TestHealthPrioritySync_Sub2APIManualMultiplierFailureDoesNotFailWorkspace(t *testing.T) {
	repo := newFakeRepository()
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastDecision: "failed", LastError: ErrorPriorityMetadataUnavailable,
	}
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	manualPriority := 1
	managedPriority := 10
	managedMultiplier := 0.2
	inventory := map[string]*priorityTargetInventory{
		"sub2api:ws1:manual": {
			target: AdminProbeTarget{
				TargetID: "sub2api:ws1:manual", Platform: string(upstream.PlatformSub2API),
				AccountID: "manual", Models: []string{"gpt-4o"},
			},
			account:            upstream.AdminGroupAccountInfo{ID: "manual", Priority: &manualPriority, Models: "gpt-4o"},
			policies:           []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{status: MultiplierResolutionUnavailable},
			currentPriority:    manualPriority,
			priorityPresent:    true,
		},
		"sub2api:ws1:managed": {
			target: AdminProbeTarget{
				TargetID: "sub2api:ws1:managed", Platform: string(upstream.PlatformSub2API),
				AccountID: "managed", Models: []string{"gpt-4o"},
			},
			account:  upstream.AdminGroupAccountInfo{ID: "managed", Priority: &managedPriority, Models: "gpt-4o"},
			policies: []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{
				status: MultiplierResolutionResolved,
				info:   upstreamKeyGroupInfo{effectiveMultiplier: &managedMultiplier},
			},
			currentPriority: managedPriority,
			priorityPresent: true,
		},
	}
	healthStates := []ConnectionHealthState{
		{ConnectionID: "sub2api:ws1:manual", ModelName: "gpt-4o", State: StateHealthy},
		{ConnectionID: "sub2api:ws1:managed", ModelName: "gpt-4o", State: StateHealthy},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, healthStates, nil,
	)

	workspaceState := repo.priorityWorkspaces["user1|ws1"]
	if workspaceState.LastDecision != "success" || workspaceState.LastError != "" || workspaceState.PendingTargetCount != 0 {
		t.Fatalf("manual target multiplier metadata must not fail workspace health sync: %+v", workspaceState)
	}
}

func TestHealthPrioritySync_Sub2APIHardExcludedPendingConfirmationPersists(t *testing.T) {
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	currentPriority := 1
	pendingPriority := 1
	targetID := "sub2api:ws1:manual"
	stored := PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 50, LastAppliedPriority: 50, PendingPriority: &pendingPriority,
	}
	repo.priorityStates["user1|ws1|"+targetID] = stored
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target: AdminProbeTarget{
				TargetID: targetID, Platform: string(upstream.PlatformSub2API),
				AccountID: "manual", Models: []string{"gpt-4o"},
			},
			account:         upstream.AdminGroupAccountInfo{ID: "manual", Priority: &currentPriority, Models: "gpt-4o"},
			policies:        []Policy{policy},
			currentPriority: currentPriority,
			priorityPresent: true,
		},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, nil, []PrioritySyncState{stored},
	)

	got := repo.priorityStates["user1|ws1|"+targetID]
	if got.PendingPriority != nil || got.LastAppliedPriority != currentPriority {
		t.Fatalf("confirmed pending Priority must be persisted before hard exclusion: %+v", got)
	}
	if len(actions.calls) != 0 {
		t.Fatalf("hard-excluded pending confirmation must not trigger another write: %+v", actions.calls)
	}
}

func TestHealthPrioritySync_Sub2APIHardExcludedPendingConfirmationFailureMarksWorkspace(t *testing.T) {
	baseRepo := newFakeRepository()
	baseRepo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastDecision: "success",
	}
	repo := &failingPriorityConfirmationRepository{fakeRepository: baseRepo, err: errors.New("checkpoint unavailable")}
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	currentPriority := 1
	pendingPriority := 1
	targetID := "sub2api:ws1:manual"
	stored := PrioritySyncState{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID,
		OriginalPriority: 50, LastAppliedPriority: 50, PendingPriority: &pendingPriority,
	}
	baseRepo.priorityStates["user1|ws1|"+targetID] = stored
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target: AdminProbeTarget{
				TargetID: targetID, Platform: string(upstream.PlatformSub2API),
				AccountID: "manual", Models: []string{"gpt-4o"},
			},
			account:         upstream.AdminGroupAccountInfo{ID: "manual", Priority: &currentPriority, Models: "gpt-4o"},
			policies:        []Policy{policy},
			currentPriority: currentPriority,
			priorityPresent: true,
		},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, nil, []PrioritySyncState{stored},
	)

	workspaceState := baseRepo.priorityWorkspaces["user1|ws1"]
	if workspaceState.LastDecision != "failed" || workspaceState.PendingTargetCount != 1 {
		t.Fatalf("checkpoint persistence failure must fail workspace sync: %+v", workspaceState)
	}
	if len(actions.calls) != 0 {
		t.Fatalf("checkpoint persistence failure must not trigger a Priority write: %+v", actions.calls)
	}
}

func TestMultiplierPrioritySync_Sub2APIMultiplierOnlyStillUsesOneToNine(t *testing.T) {
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeMultiplierOnly
	currentPriority := 50
	targetID := "sub2api:ws1:price-only"
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target: AdminProbeTarget{
				TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "price-only",
			},
			account:         upstream.AdminGroupAccountInfo{ID: "price-only", Priority: &currentPriority},
			policies:        []Policy{policy},
			multipliers:     []float64{0.4},
			currentPriority: currentPriority,
			priorityPresent: true,
		},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, nil, nil,
	)

	if len(actions.calls) != 1 || actions.calls[0].priority != 1 {
		t.Fatalf("multiplier_only must retain the Sub2API 1-9 range: %+v", actions.calls)
	}
	stored := repo.priorityStates["user1|ws1|"+targetID]
	if stored.LastAppliedPriority != 1 || stored.OriginalPriority != currentPriority || stored.Conflict {
		t.Fatalf("unexpected multiplier_only sync checkpoint: %+v", stored)
	}
}

func TestMultiplierPrioritySync_Sub2APIMixedHealthAndMultiplierOnlyStillUsesOneToNine(t *testing.T) {
	repo := newFakeRepository()
	actions := &fakeTargetPriorityActioner{}
	healthPolicy := sub2APIProbePolicy(false)
	healthPolicy.ID = "health"
	healthPolicy.PriorityMode = PriorityModeMultiplier
	healthPolicy.StrategyMode = StrategyModeHealthProbe
	pricePolicy := sub2APIProbePolicy(false)
	pricePolicy.ID = "price-only"
	pricePolicy.PriorityMode = PriorityModeMultiplier
	pricePolicy.StrategyMode = StrategyModeMultiplierOnly
	currentPriority := 50
	targetID := "sub2api:ws1:mixed"
	inventory := map[string]*priorityTargetInventory{
		targetID: {
			target: AdminProbeTarget{
				TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "mixed", Models: []string{"gpt-4o"},
			},
			account:         upstream.AdminGroupAccountInfo{ID: "mixed", Priority: &currentPriority, Models: "gpt-4o"},
			policies:        []Policy{healthPolicy, pricePolicy},
			multipliers:     []float64{0.4},
			currentPriority: currentPriority,
			priorityPresent: true,
		},
	}
	service := &Service{repo: repo, priorityActions: actions}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, true, nil, nil,
	)

	if len(actions.calls) != 1 || actions.calls[0].priority != 1 {
		t.Fatalf("mixed health and multiplier_only policies must retain the Sub2API 1-9 range: %+v", actions.calls)
	}
}

func TestRunSchedulerTick_Sub2APIManualPriorityStaysExcludedAcrossTicks(t *testing.T) {
	var httpRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	policy.ProbeIntervalSeconds = 60
	repo.policies = []Policy{policy}
	repo.groupAssignments = []GroupPolicyAssignment{{
		UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", AdminGroupName: "default", PolicyID: policy.ID,
	}}
	fallback := 0.4
	repo.groupSortSettings["user1|ws1|g1"] = GroupProbeSortSetting{
		UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", FallbackMultiplier: &fallback,
	}
	reader := &mutableSub2APISchedulerReader{
		priority:   1,
		credential: upstream.ProbeCredential{BaseURL: server.URL, Key: "test-key"},
	}
	service := &Service{
		repo:            repo,
		mySites:         fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups:  reader,
		priorityActions: reader,
		dispatcher:      noopRemoteActionRunner{},
		probeRunner:     NewRealProbeRunner(),
	}

	service.runSchedulerTick(context.Background())
	service.runSchedulerTick(context.Background())

	priority, priorityWrites, credentialReads := reader.snapshot()
	if priority != 1 || priorityWrites != 0 || credentialReads != 0 || httpRequests.Load() != 0 || len(repo.states) != 0 || len(repo.events) != 0 {
		t.Fatalf(
			"manual account entered automatic monitoring across ticks: priority=%d priority_writes=%d credential_reads=%d http_requests=%d states=%+v events=%+v",
			priority, priorityWrites, credentialReads, httpRequests.Load(), repo.states, repo.events,
		)
	}
}

func TestRunAdminProbeJob_Sub2APIPriorityBecomesManualBeforeExecution(t *testing.T) {
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	policy.ProbeIntervalSeconds = 60
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:manual"
	repo.assignments = []PolicyAssignment{{
		UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, PolicyID: policy.ID,
	}}
	reader := &mutableSub2APISchedulerReader{priority: 1}
	service := &Service{
		repo:           repo,
		mySites:        fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		platformGroups: reader,
		probeRunner:    NewRealProbeRunner(),
	}
	queuedPriority := 10
	dueSpec := probeModelSpec{modelName: "gpt-4o", policy: policy, policies: []Policy{policy}}
	job := adminProbeJob{
		userID: "user1", adminAccountID: "ws1", session: upstream.Session{Platform: upstream.PlatformSub2API},
		target: AdminProbeTarget{
			TargetID: targetID, Platform: string(upstream.PlatformSub2API), AccountID: "manual",
			AccountStatus: "active", Schedulable: boolPointer(true), Models: []string{"gpt-4o"},
		},
		account: upstream.AdminGroupAccountInfo{
			ID: "manual", Priority: &queuedPriority, Status: "active", Models: "gpt-4o",
		},
		dueSpecs: []probeModelSpec{dueSpec}, floorGuard: newWorkspaceFloorGuard(),
	}
	var wg sync.WaitGroup
	wg.Add(1)

	service.runAdminProbeJob(context.Background(), job, func() {}, &wg)
	wg.Wait()

	_, _, credentialReads := reader.snapshot()
	if credentialReads != 0 || len(repo.states) != 0 || len(repo.events) != 0 {
		t.Fatalf("fresh manual Priority must stop before credentials and probing: credential_reads=%d states=%+v events=%+v", credentialReads, repo.states, repo.events)
	}
}

func TestOverview_Sub2APIManualPriorityIsNotCountedAsMonitored(t *testing.T) {
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	repo.groupAssignments = []GroupPolicyAssignment{{
		UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", AdminGroupName: "default", PolicyID: policy.ID,
	}}
	manualPriority := 1
	managedPriority := 10
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "default", Platform: string(upstream.PlatformSub2API)}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {
				{ID: "manual", Name: "manual", Models: "gpt-4o", Priority: &manualPriority},
				{ID: "managed", Name: "managed", Models: "gpt-4o", Priority: &managedPriority},
			},
		},
	}
	service := newAdminGroupsService(
		reader,
		fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		repo,
	)

	overview, err := service.Overview(context.Background(), "user1")
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if overview.TotalConnections != 1 || overview.Unconfigured != 1 {
		t.Fatalf("overview must count only the Priority 10 monitored account: %+v", overview)
	}
}

func TestOverview_SharedHealthTargetAndMultiplierOnlyPeerAreCountedByProbePolicy(t *testing.T) {
	repo := newFakeRepository()
	healthPolicy := sub2APIProbePolicy(false)
	healthPolicy.ID = "health"
	pricePolicy := sub2APIProbePolicy(false)
	pricePolicy.ID = "price-only"
	pricePolicy.StrategyMode = StrategyModeMultiplierOnly
	pricePolicy.PriorityMode = PriorityModeMultiplier
	pricePolicy.ModelTargets = nil
	repo.policies = []Policy{healthPolicy, pricePolicy}
	repo.groupAssignments = []GroupPolicyAssignment{
		{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "health-group", PolicyID: healthPolicy.ID},
		{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "price-group", PolicyID: pricePolicy.ID},
	}
	priority := 10
	shared := upstream.AdminGroupAccountInfo{ID: "shared", Name: "shared", Models: "gpt-4o", Priority: &priority}
	priceOnly := upstream.AdminGroupAccountInfo{ID: "price-only", Name: "price-only", Priority: &priority}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{
			{ID: "health-group", Name: "health", Platform: string(upstream.PlatformSub2API)},
			{ID: "price-group", Name: "price", Platform: string(upstream.PlatformSub2API)},
		},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"health-group": {shared},
			"price-group":  {shared, priceOnly},
		},
	}
	service := newAdminGroupsService(
		reader,
		fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		repo,
	)

	overview, err := service.Overview(context.Background(), "user1")
	if err != nil {
		t.Fatalf("Overview() error = %v", err)
	}
	if overview.TotalConnections != 1 || overview.Unconfigured != 1 {
		t.Fatalf("overview must count the shared health target once and exclude the multiplier_only peer: %+v", overview)
	}
}

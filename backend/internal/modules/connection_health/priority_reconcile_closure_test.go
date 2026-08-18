package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type eventCancelRepository struct {
	*fakeRepository
	cancelAfterEvent func()
}

func (r *eventCancelRepository) InsertEvent(ctx context.Context, event ConnectionHealthEvent) error {
	if err := r.fakeRepository.InsertEvent(ctx, event); err != nil {
		return err
	}
	if r.cancelAfterEvent != nil {
		r.cancelAfterEvent()
	}
	return nil
}

type contextAwarePriorityReader struct {
	fakePlatformGroupReader
}

type blockingPriorityActioner struct {
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	releaseOnce sync.Once
}

type sequencedPriorityActioner struct {
	mu           sync.Mutex
	calls        []priorityUpdateCall
	firstStart   chan struct{}
	firstRelease chan struct{}
	secondStart  chan struct{}
}

func (a *sequencedPriorityActioner) UpdateAdminTargetPriority(session upstream.Session, targetID string, priority int) error {
	a.mu.Lock()
	a.calls = append(a.calls, priorityUpdateCall{targetID: targetID, priority: priority})
	callNumber := len(a.calls)
	a.mu.Unlock()
	switch callNumber {
	case 1:
		close(a.firstStart)
		<-a.firstRelease
	case 2:
		close(a.secondStart)
	}
	return nil
}

func (a *blockingPriorityActioner) UpdateAdminTargetPriority(session upstream.Session, targetID string, priority int) error {
	close(a.started)
	<-a.release
	close(a.finished)
	return nil
}

func (a *blockingPriorityActioner) Release() {
	a.releaseOnce.Do(func() { close(a.release) })
}

type eventFailureRepository struct {
	*fakeRepository
}

type partialUpsertFailureRepository struct {
	*fakeRepository
	upsertCalls int
}

type countingPriorityRepository struct {
	*fakeRepository
	mu                   sync.Mutex
	listCheckpointCalls  int
	secondListCheckPoint chan struct{}
}

type blockingHealthFailureRepository struct {
	*fakeRepository
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	releaseOnce sync.Once
}

type panickingHealthFailureRepository struct {
	*fakeRepository
}

func (r *panickingHealthFailureRepository) GetPriorityWorkspaceSyncState(ctx context.Context, userID string, adminAccountID string) (*PriorityWorkspaceSyncState, error) {
	panic("health failure state read panic")
}

func (r *blockingHealthFailureRepository) MarkPriorityWorkspaceHealthSyncFailed(ctx context.Context, userID string, adminAccountID string, errorDetail string, failedCount int) (bool, error) {
	close(r.started)
	<-r.release
	marked, err := r.fakeRepository.MarkPriorityWorkspaceHealthSyncFailed(ctx, userID, adminAccountID, errorDetail, failedCount)
	close(r.finished)
	return marked, err
}

func (r *blockingHealthFailureRepository) Release() {
	r.releaseOnce.Do(func() { close(r.release) })
}

func (r *countingPriorityRepository) ListPrioritySyncStates(ctx context.Context, userID string, adminAccountID string) ([]PrioritySyncState, error) {
	r.mu.Lock()
	r.listCheckpointCalls++
	if r.listCheckpointCalls == 2 {
		close(r.secondListCheckPoint)
	}
	r.mu.Unlock()
	return r.fakeRepository.ListPrioritySyncStates(ctx, userID, adminAccountID)
}

func waitForPriorityAsyncIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		priorityAsyncDispatcher.mu.Lock()
		workers := priorityAsyncDispatcher.workers
		queueLength := len(priorityAsyncDispatcher.queue)
		priorityAsyncDispatcher.mu.Unlock()
		if workers == 0 && queueLength == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	priorityAsyncDispatcher.mu.Lock()
	workers := priorityAsyncDispatcher.workers
	queueLength := len(priorityAsyncDispatcher.queue)
	priorityAsyncDispatcher.mu.Unlock()
	t.Fatalf("priority async dispatcher did not become idle: workers=%d queue=%d", workers, queueLength)
}

func (r *partialUpsertFailureRepository) UpsertState(ctx context.Context, state ConnectionHealthState) error {
	r.upsertCalls++
	if r.upsertCalls == 2 {
		return errors.New("second health state write failed")
	}
	return r.fakeRepository.UpsertState(ctx, state)
}

func (r *eventFailureRepository) InsertEvent(ctx context.Context, event ConnectionHealthEvent) error {
	return errors.New("event insert failed after health state commit")
}

func (r contextAwarePriorityReader) FetchAdminAllGroupsContext(ctx context.Context, session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.fakePlatformGroupReader.FetchAdminAllGroups(session)
}

func (r contextAwarePriorityReader) ListAdminGroupAccountsContext(ctx context.Context, session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.fakePlatformGroupReader.ListAdminGroupAccounts(session, group)
}

func newPostCommitProbeService(baseRepo *fakeRepository, repo healthRepository, priorityActions TargetPriorityActioner, serverURL string) *Service {
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	baseRepo.policies = []Policy{policy}
	assignPolicyToTarget(baseRepo, policy, "sub2api:ws1:acc-1")
	accountPriority := 1000
	reader := contextAwarePriorityReader{fakePlatformGroupReader: fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{
				ID: "acc-1", Name: "acc", Models: "gpt-4o",
				Priority: &accountPriority,
			}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: serverURL, Key: "k"}},
	}}
	return &Service{
		repo:            repo,
		mySites:         fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		accounts:        fakeAdminAccountResolver{id: "ws1"},
		dispatcher:      noopRemoteActionRunner{},
		probeRunner:     NewRealProbeRunner(),
		platformGroups:  reader,
		priorityActions: priorityActions,
	}
}

func TestProbeTarget_PostCommitPrioritySyncDoesNotInheritCanceledRequestContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	baseRepo := newFakeRepository()
	requestCtx, cancel := context.WithCancel(context.Background())
	repo := &eventCancelRepository{fakeRepository: baseRepo, cancelAfterEvent: cancel}
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	baseRepo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(baseRepo, policy, targetID)
	accountPriority := 1000
	reader := contextAwarePriorityReader{fakePlatformGroupReader: fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{
				ID: "acc-1", Name: "acc", Models: "gpt-4o",
				Priority: &accountPriority,
			}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}}
	priorityActions := &fakeTargetPriorityActioner{called: make(chan struct{}, 1)}
	service := &Service{
		repo:            repo,
		mySites:         fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}},
		accounts:        fakeAdminAccountResolver{id: "ws1"},
		dispatcher:      noopRemoteActionRunner{},
		probeRunner:     NewRealProbeRunner(),
		platformGroups:  reader,
		priorityActions: priorityActions,
	}

	results, err := service.ProbeTarget(requestCtx, "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("formal probe itself must finish before post-commit cancellation affects priority sync: %v", err)
	}
	if len(results) != 1 || results[0].State != StateHealthy {
		t.Fatalf("formal probe should commit a healthy result, got %+v", results)
	}
	if len(baseRepo.events) != 1 {
		t.Fatalf("health event must be committed before priority reconciliation: %+v", baseRepo.events)
	}
	select {
	case <-priorityActions.called:
	case <-time.After(time.Second):
		t.Fatalf("post-commit priority reconciliation must not inherit the canceled request context: calls=%+v", priorityActions.calls)
	}
	if len(priorityActions.calls) != 1 {
		t.Fatalf("post-commit priority reconciliation must write once, got %+v", priorityActions.calls)
	}
	if priorityActions.calls[0].priority < 10 || priorityActions.calls[0].priority > 99 {
		t.Fatalf("healthy target must be restored to the Sub2API healthy band, got %+v", priorityActions.calls[0])
	}
}

func TestProbeTarget_PostCommitPrioritySyncDoesNotBlockRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	priorityActions := &blockingPriorityActioner{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	defer priorityActions.Release()
	service := newPostCommitProbeService(repo, repo, priorityActions, server.URL)
	done := make(chan error, 1)
	go func() {
		_, err := service.ProbeTarget(context.Background(), "user1", "sub2api:ws1:acc-1", []string{"gpt-4o"})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("formal probe failed before asynchronous priority write started: %v", err)
		}
	case <-priorityActions.started:
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("formal probe returned an error while priority write was queued: %v", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("formal probe remained blocked by the post-commit Priority write")
		}
	case <-time.After(time.Second):
		t.Fatal("post-commit Priority write did not start")
	}
	priorityActions.Release()
	select {
	case <-priorityActions.finished:
	case <-time.After(time.Second):
		t.Fatal("asynchronous Priority write did not finish after release")
	}
}

func TestProbeTarget_EventFailureStillQueuesPostCommitPrioritySync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	baseRepo := newFakeRepository()
	repo := &eventFailureRepository{fakeRepository: baseRepo}
	priorityActions := &fakeTargetPriorityActioner{called: make(chan struct{}, 1)}
	service := newPostCommitProbeService(baseRepo, repo, priorityActions, server.URL)

	_, err := service.ProbeTarget(context.Background(), "user1", "sub2api:ws1:acc-1", []string{"gpt-4o"})
	if err == nil {
		t.Fatal("event persistence failure must still be returned to the caller")
	}
	select {
	case <-priorityActions.called:
	case <-time.After(time.Second):
		t.Fatal("health state committed before event failure must still queue Priority reconciliation")
	}
}

func TestProbeTarget_PartialMultiModelCommitStillQueuesPostCommitPrioritySync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Model == "gpt-4o-mini" {
			http.Error(w, "upstream failure", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	baseRepo := newFakeRepository()
	repo := &partialUpsertFailureRepository{fakeRepository: baseRepo}
	priorityActions := &fakeTargetPriorityActioner{called: make(chan struct{}, 1)}
	service := newPostCommitProbeService(baseRepo, repo, priorityActions, server.URL)
	servicePolicy := baseRepo.policies[0]
	servicePolicy.ModelTargets = append(servicePolicy.ModelTargets, ModelTarget{
		ID: "t2", PolicyID: servicePolicy.ID, ModelName: "gpt-4o-mini", ProviderFamily: ProviderOpenAI, Enabled: true, MaxProbeTokens: 1,
	})
	baseRepo.policies[0] = servicePolicy
	reader := service.platformGroups.(contextAwarePriorityReader)
	reader.accountsByGrp["g1"][0].Models = "gpt-4o,gpt-4o-mini"
	service.platformGroups = reader

	_, err := service.ProbeTarget(context.Background(), "user1", "sub2api:ws1:acc-1", []string{"gpt-4o", "gpt-4o-mini"})
	if err == nil {
		t.Fatal("the second model failure must be returned")
	}
	if len(baseRepo.states) == 0 {
		t.Fatal("the first successful model must commit health state before the second model fails")
	}
	select {
	case <-priorityActions.called:
	case <-time.After(time.Second):
		t.Fatal("partial multi-model health commit must still queue Priority reconciliation")
	}
}

func TestHealthPrioritySyncSecondTriggerWhileRunningQueuesFollowUp(t *testing.T) {
	baseRepo := newFakeRepository()
	repo := &countingPriorityRepository{
		fakeRepository:       baseRepo,
		secondListCheckPoint: make(chan struct{}),
	}
	priorityActions := &sequencedPriorityActioner{
		firstStart:   make(chan struct{}),
		firstRelease: make(chan struct{}),
		secondStart:  make(chan struct{}),
	}
	service := newPostCommitProbeService(baseRepo, repo, priorityActions, "http://unused")

	service.triggerHealthPrioritySyncAfterCommit("user1", "ws1")
	select {
	case <-priorityActions.firstStart:
	case <-time.After(time.Second):
		t.Fatal("first health Priority reconciliation did not start")
	}
	baseRepo.states["sub2api:ws1:acc-1"] = map[string]ConnectionHealthState{
		"gpt-4o": {
			ConnectionID: "sub2api:ws1:acc-1", ModelName: "gpt-4o",
			UserID: "user1", AdminAccountID: "ws1", State: StateSuspended,
		},
	}
	service.triggerHealthPrioritySyncAfterCommit("user1", "ws1")
	close(priorityActions.firstRelease)
	select {
	case <-repo.secondListCheckPoint:
	case <-time.After(time.Second):
		t.Fatal("second health commit was dropped instead of scheduling a follow-up reconciliation")
	}
	waitForPriorityAsyncIdle(t)
}

func TestPrioritySyncGenerationReadFailurePersistsHealthFailure(t *testing.T) {
	repo := newFakeRepository()
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastDecision: "success", InventoryStatus: "complete",
	}
	repo.priorityWorkspaceStateErr = errors.New("workspace state read failed")
	service := &Service{
		repo: repo, platformGroups: fakePlatformGroupReader{}, priorityActions: &fakeTargetPriorityActioner{},
	}

	service.syncCurrentWorkspacePriorities(context.Background(), "user1", "ws1")

	repo.priorityWorkspaceStateErr = nil
	state, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || state == nil {
		t.Fatalf("workspace state err=%v state=%+v", err, state)
	}
	if state.LastDecision != "failed" || state.InventoryStatus != "failed" || state.NextReconcileAt == nil {
		t.Fatalf("generation read failure must persist a retryable failure: %+v", state)
	}
}

func TestHealthPrioritySyncQueueFullDoesNotBlockCaller(t *testing.T) {
	waitForPriorityAsyncIdle(t)
	priorityAsyncDispatcher.mu.Lock()
	oldQueue := priorityAsyncDispatcher.queue
	oldWorkers := priorityAsyncDispatcher.workers
	priorityAsyncDispatcher.queue = make(chan priorityAsyncJob, 1)
	priorityAsyncDispatcher.queue <- priorityAsyncJob{}
	priorityAsyncDispatcher.workers = priorityAsyncWorkerCount
	priorityAsyncDispatcher.mu.Unlock()
	restored := false
	restoreDispatcher := func() {
		if restored {
			return
		}
		priorityAsyncDispatcher.mu.Lock()
		priorityAsyncDispatcher.queue = oldQueue
		priorityAsyncDispatcher.workers = oldWorkers
		priorityAsyncDispatcher.mu.Unlock()
		restored = true
	}
	defer restoreDispatcher()

	baseRepo := newFakeRepository()
	baseRepo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastDecision: "success", InventoryStatus: "complete",
	}
	repo := &blockingHealthFailureRepository{
		fakeRepository: baseRepo,
		started:        make(chan struct{}),
		release:        make(chan struct{}),
		finished:       make(chan struct{}),
	}
	service := &Service{
		repo: repo, platformGroups: fakePlatformGroupReader{}, priorityActions: &fakeTargetPriorityActioner{},
	}
	done := make(chan struct{})
	go func() {
		service.triggerHealthPrioritySyncAfterCommit("user1", "ws1")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		repo.Release()
		<-done
		t.Fatal("queue-full health Priority trigger blocked the caller")
	}
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		repo.Release()
		t.Fatal("queue-full health Priority failure was not written asynchronously")
	}
	repo.Release()
	select {
	case <-repo.finished:
	case <-time.After(time.Second):
		t.Fatal("queue-full health Priority failure write did not finish")
	}
	restoreDispatcher()
	waitForPriorityAsyncIdle(t)
}

func TestHealthPrioritySyncQueueFullFailureCannotOverwriteFollowUpSuccess(t *testing.T) {
	waitForPriorityAsyncIdle(t)
	priorityAsyncDispatcher.mu.Lock()
	oldQueue := priorityAsyncDispatcher.queue
	oldWorkers := priorityAsyncDispatcher.workers
	priorityAsyncDispatcher.queue = make(chan priorityAsyncJob, 1)
	priorityAsyncDispatcher.queue <- priorityAsyncJob{}
	priorityAsyncDispatcher.workers = priorityAsyncWorkerCount
	priorityAsyncDispatcher.mu.Unlock()
	restored := false
	restoreDispatcher := func() {
		if restored {
			return
		}
		priorityAsyncDispatcher.mu.Lock()
		priorityAsyncDispatcher.queue = oldQueue
		priorityAsyncDispatcher.workers = oldWorkers
		priorityAsyncDispatcher.mu.Unlock()
		restored = true
	}
	defer restoreDispatcher()

	baseRepo := newFakeRepository()
	baseRepo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID: "user1", AdminAccountID: "ws1", LastDecision: "success", InventoryStatus: "complete",
	}
	repo := &blockingHealthFailureRepository{
		fakeRepository: baseRepo,
		started:        make(chan struct{}),
		release:        make(chan struct{}),
		finished:       make(chan struct{}),
	}
	priorityActions := &fakeTargetPriorityActioner{called: make(chan struct{}, 1)}
	service := newPostCommitProbeService(baseRepo, repo, priorityActions, "http://unused")
	service.mySites = fakeAdminGroupKeyReader{
		fakeMySitesReader: fakeMySitesReader{
			session: upstream.Session{Platform: upstream.PlatformSub2API},
			connections: []my_sites.RealConnection{{
				UserID: "user1", WorkspaceAdminAccountID: "ws1", AdminAccountID: "acc-1",
				AdminPlatform: string(upstream.PlatformSub2API), UpstreamSiteID: "site-1", UpstreamKeyID: "key-1",
				UpstreamGroupID: "g1", UpstreamGroupName: "vip",
			}},
		},
		keysBySite: map[string][]upstream.Sub2APIKeyItem{
			"site-1": {{ID: "key-1", GroupID: "g1", GroupName: "vip"}},
		},
	}
	service.sites = multiplierSiteLookup{}

	service.triggerHealthPrioritySyncAfterCommit("user1", "ws1")
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("queue-full failure fallback did not start")
	}
	restoreDispatcher()
	service.triggerHealthPrioritySyncAfterCommit("user1", "ws1")
	repo.Release()

	select {
	case <-repo.finished:
	case <-time.After(time.Second):
		t.Fatal("queue-full failure fallback did not finish")
	}
	select {
	case <-priorityActions.called:
	case <-time.After(time.Second):
		t.Fatal("follow-up health reconciliation did not run after the failure fallback")
	}
	waitForPriorityAsyncIdle(t)

	state, err := baseRepo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || state == nil {
		t.Fatalf("workspace state err=%v state=%+v", err, state)
	}
	if state.LastDecision != "success" || state.InventoryStatus != "complete" || state.NextReconcileAt != nil {
		priorityActionsCalled := append([]priorityUpdateCall(nil), priorityActions.calls...)
		priorityAsyncDispatcher.mu.Lock()
		workers := priorityAsyncDispatcher.workers
		queueLength := len(priorityAsyncDispatcher.queue)
		priorityAsyncDispatcher.mu.Unlock()
		t.Fatalf("stale queue-full failure overwrote follow-up success: state=%+v calls=%+v workers=%d queue=%d", state, priorityActionsCalled, workers, queueLength)
	}
}

func TestHealthPriorityFailureFallbackRecoversRepositoryPanic(t *testing.T) {
	repo := &panickingHealthFailureRepository{fakeRepository: newFakeRepository()}
	service := &Service{
		repo:                  repo,
		priorityHealthRunning: map[string]bool{"user1\x00ws1": true},
	}

	service.runHealthPriorityFailureFallback("user1\x00ws1", "user1", "ws1")

	service.priorityTriggerMu.Lock()
	running := service.priorityHealthRunning["user1\x00ws1"]
	pending := service.priorityHealthPending["user1\x00ws1"]
	service.priorityTriggerMu.Unlock()
	if running || pending {
		t.Fatalf("repository panic left health failure fallback reserved: running=%v pending=%v", running, pending)
	}
}

func TestHealthPrioritySyncPanicReleasesReservation(t *testing.T) {
	repo := &panickingPriorityRepository{fakeRepository: newFakeRepository()}
	service := &Service{
		repo: repo, platformGroups: fakePlatformGroupReader{}, priorityActions: &fakeTargetPriorityActioner{},
		priorityHealthRunning: map[string]bool{"user1\x00ws1": true},
	}

	service.runQueuedHealthPrioritySync("user1", "ws1")

	service.priorityTriggerMu.Lock()
	running := service.priorityHealthRunning["user1\x00ws1"]
	pending := service.priorityHealthPending["user1\x00ws1"]
	service.priorityTriggerMu.Unlock()
	if running || pending {
		t.Fatalf("health Priority panic left workspace reserved: running=%v pending=%v", running, pending)
	}
}

func TestPrioritySyncIncompleteGroupInventoryDoesNotMarkWorkspaceSuccess(t *testing.T) {
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
		UserID:           "user1",
		AdminAccountID:   "ws1",
		AppliedSignature: "generation-done",
		LastDecision:     "success",
		InventoryStatus:  "complete",
	}
	multiplier := 0.05
	accountPriority := 10
	inventory := map[string]*priorityTargetInventory{
		"sub2api:ws1:acc-a": {
			target: AdminProbeTarget{
				TargetID: "sub2api:ws1:acc-a", Platform: string(upstream.PlatformSub2API),
				AccountID: "acc-a", Models: []string{"gpt-4o"},
			},
			account:  upstream.AdminGroupAccountInfo{ID: "acc-a", Priority: &accountPriority, Models: "gpt-4o"},
			policies: []Policy{policy},
			upstreamMultiplier: upstreamMultiplierResolution{
				status: MultiplierResolutionResolved,
				info:   upstreamKeyGroupInfo{effectiveMultiplier: &multiplier},
			},
			currentPriority: accountPriority,
			priorityPresent: true,
		},
	}
	omittedTargetID := "sub2api:ws1:acc-b"
	syncStates := []PrioritySyncState{{
		UserID: "user1", AdminAccountID: "ws1", TargetID: omittedTargetID,
		OriginalPriority: 100000, LastAppliedPriority: 1000,
	}}
	repo.priorityStates["user1|ws1|"+omittedTargetID] = syncStates[0]
	healthStates := []ConnectionHealthState{
		{ConnectionID: "sub2api:ws1:acc-a", ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy},
		{ConnectionID: omittedTargetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy},
	}
	service := &Service{
		repo:            repo,
		priorityActions: &fakeTargetPriorityActioner{},
	}

	service.syncWorkspacePriorities(
		context.Background(), upstream.Session{Platform: upstream.PlatformSub2API},
		"user1", "ws1", inventory, false, healthStates, syncStates,
	)

	workspaceState, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || workspaceState == nil {
		t.Fatalf("workspace state err=%v state=%+v", err, workspaceState)
	}
	if workspaceState.LastDecision == "success" || workspaceState.InventoryStatus == "complete" {
		t.Fatalf("incomplete inventory must not be recorded as workspace success: %+v", workspaceState)
	}
	if workspaceState.NextReconcileAt == nil {
		t.Fatalf("incomplete inventory must leave a retry time: %+v", workspaceState)
	}
	if _, ok := repo.priorityStates["user1|ws1|"+omittedTargetID]; !ok {
		t.Fatalf("omitted target checkpoint must remain retryable")
	}
}

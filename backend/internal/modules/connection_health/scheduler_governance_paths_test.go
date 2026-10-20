package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func schedulerPathsService(t *testing.T, models string) (*Service, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	policy := schedulableProbePolicy("paths", true, 60)
	policy.AutoRemoteActionEnabled = false
	policy.PriorityMode = PriorityModeMultiplier
	policy.StrategyMode = StrategyModeHealthProbe
	policy.RuleVersion = RuleVersionV2
	preset := recommendedRulePreset()
	policy.RulePreset = &preset
	if models != "gpt-4o" {
		policy.ModelTargets = nil
		for _, name := range strings.Split(models, ",") {
			policy.ModelTargets = append(policy.ModelTargets, ModelTarget{ModelName: name, Enabled: true, MaxProbeTokens: 1})
		}
	}
	repo.policies = []Policy{policy}
	repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "paths"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: models}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://unit.invalid", Key: "fixture"}}}
	s := &Service{repo: repo, platformGroups: reader, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, dispatcher: noopRemoteActionRunner{}, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	s.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: request}, nil
	})}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("scheduler fixture shutdown: %v", err)
		}
		schedulerStatsByService.Delete(s)
	})
	return s, repo
}

func waitSchedulerPathsIdle(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if waiter, ok := any(s).(interface{ waitScheduledProbes(context.Context) error }); ok {
		if err := waiter.waitScheduledProbes(ctx); err != nil {
			t.Fatalf("scheduled jobs did not exit: %v", err)
		}
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s.priorityTriggerMu.Lock()
		busy := len(s.priorityHealthRunning) != 0 || len(s.priorityHealthPending) != 0
		s.priorityTriggerMu.Unlock()
		if !busy {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("health priority synchronization did not exit")
		}
	}
}

func schedulerPathsTick(s *Service, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() { s.runSchedulerTick(ctx); close(done) }()
	return done
}

func schedulerPathsResponse(request *http.Request, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: request}
}

func schedulerPathsSkipped(s *Service, reason string) int {
	value, exists := schedulerStatsByService.Load(s)
	if !exists {
		return 0
	}
	stats := value.(*schedulerRoundStats)
	stats.mu.Lock()
	defer stats.mu.Unlock()
	// Keep the same test runnable against the old statistics type, which had
	// no skip counters. The workflow assertion still supplies its business RED.
	counts := reflect.ValueOf(stats).Elem().FieldByName("skipped")
	if !counts.IsValid() {
		return 0
	}
	count := counts.MapIndex(reflect.ValueOf(reason))
	if !count.IsValid() {
		return 0
	}
	return int(count.Int())
}

func TestInFlightTargetsDoNotConsumeTaskWindow(t *testing.T) {
	models := make([]string, 100)
	for i := range models {
		models[i] = fmt.Sprintf("slow-%03d", i)
	}
	s, repo := schedulerPathsService(t, strings.Join(models, ","))
	s.repo = &governanceCanceledRepository{repo}
	other := schedulableProbePolicy("other", true, 60)
	other.ModelTargets[0].ModelName = "other-model"
	repo.policies = append(repo.policies, other)
	repo.assignments = []PolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:b", PolicyID: other.ID}}
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "b", Models: "other-model", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
	reader.credByAccount["b"] = upstream.ProbeCredential{BaseURL: "https://other.invalid", Key: "fixture"}
	s.platformGroups = reader
	started := make(chan struct{}, len(models)+1)
	otherStarted := make(chan struct{}, 1)
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "other.invalid" {
			otherStarted <- struct{}{}
			return schedulerPathsResponse(request, http.StatusOK), nil
		}
		started <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := schedulerPathsTick(s, ctx)
	defer func() { cancel(); <-done; waitSchedulerPathsIdle(t, s) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow account did not start")
	}
	jobs := s.collectAdminProbeJobsWithGroups(ctx, repo.policies, repo.assignments, repo.groupAssignments, nil)
	if len(jobs) != 1 || jobs[0].target.AccountID != "b" || len(jobs[0].dueSpecs) != 1 {
		t.Fatalf("100 in-flight model tasks hid the other due account: jobs=%d", len(jobs))
	}
	second := schedulerPathsTick(s, ctx)
	select {
	case <-second:
	case <-time.After(time.Second):
		cancel()
		<-second
		t.Fatal("dispatch of the other account waited for the 100-model account")
	}
	select {
	case <-otherStarted:
	case <-time.After(time.Second):
		t.Fatal("the other account entered the candidate window but was not dispatched")
	}
}

func TestInFlightAndSlotReleasedAfterJobPanic(t *testing.T) {
	testScheduledPathsPanicRelease(t, false)
}

type schedulerPathsReleasePanicRepository struct {
	*fakeRepository
	panicOnce atomic.Bool
}

func (r *schedulerPathsReleasePanicRepository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	handle, acquired, err := r.fakeRepository.AcquireActionLease(ctx, key, wait)
	if acquired && strings.HasPrefix(key, "connection-health:target:") {
		release := handle.release
		handle.release = func() {
			release()
			if r.panicOnce.CompareAndSwap(true, false) {
				panic("injected target release panic")
			}
		}
	}
	return handle, acquired, err
}

func TestScheduledJobCleanupPanicReleasesResources(t *testing.T) {
	testScheduledPathsPanicRelease(t, true)
}

func testScheduledPathsPanicRelease(t *testing.T, cleanupPanic bool) {
	t.Helper()
	s, repo := schedulerPathsService(t, "gpt-4o")
	var cleanupStorage *schedulerPathsReleasePanicRepository
	if cleanupPanic {
		cleanupStorage = &schedulerPathsReleasePanicRepository{fakeRepository: repo}
		s.repo = cleanupStorage
	}
	var calls atomic.Int64
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			if cleanupStorage != nil {
				cleanupStorage.panicOnce.Store(true)
			}
			panic("injected scheduled probe panic")
		}
		return schedulerPathsResponse(request, http.StatusOK), nil
	})
	s.runSchedulerTick(context.Background())
	waitSchedulerPathsIdle(t, s)
	limiter := s.sharedProbeLimiter()
	limiter.mu.Lock()
	active := limiter.globalActive
	limiter.mu.Unlock()
	if active != 0 {
		t.Fatalf("panic retained %d concurrency slots", active)
	}
	if flights, ok := any(s).(interface{ scheduledProbeInFlight(string, string) bool }); ok && flights.scheduledProbeInFlight("user1", "sub2api:ws1:a") {
		t.Fatal("panic retained the in-flight target")
	}
	s.runSchedulerTick(context.Background())
	waitSchedulerPathsIdle(t, s)
	state, _ := repo.GetState(context.Background(), "sub2api:ws1:a", "gpt-4o")
	if calls.Load() != 2 || state == nil || state.State != StateHealthy {
		t.Fatalf("target could not run after panic: calls=%d state=%+v", calls.Load(), state)
	}
}

func TestSchedulerRecheckGetsSlotFirst(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "b", Models: "gpt-4o", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
	reader.credByAccount["b"] = upstream.ProbeCredential{BaseURL: "https://recheck.invalid", Key: "fixture"}
	s.platformGroups = reader
	now, protocol := time.Now(), TestProtocolChatCompletions
	repo.states["sub2api:ws1:b"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:b", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", RuleVersion: RuleVersionV2, State: StateSuspect, RecheckPending: true, LastProbeAt: &now, LastAppliedProbeProtocol: &protocol}}
	s.sharedProbeLimiter().SetWorkspaceCap("user1|ws1", 1, 1)
	first := make(chan string, 2)
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		first <- request.URL.Host
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := schedulerPathsTick(s, ctx)
	select {
	case host := <-first:
		if host != "recheck.invalid" {
			t.Errorf("first admitted account=%s, wanted the immediate recheck", host)
		}
	case <-time.After(time.Second):
		t.Error("no scheduled probe was admitted")
	}
	cancel()
	<-done
	waitSchedulerPathsIdle(t, s)
}

func TestBudgetOneAllowsOneProbeAcrossTicks(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	repo.policies[0].DailyProbeBudget = 1
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "b", Models: "gpt-4o", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
	reader.credByAccount["b"] = upstream.ProbeCredential{BaseURL: "https://other.invalid", Key: "fixture"}
	s.platformGroups = reader
	started, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int64
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return schedulerPathsResponse(request, http.StatusOK), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	first := schedulerPathsTick(s, context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("budgeted probe did not start")
	}
	second := schedulerPathsTick(s, context.Background())
	select {
	case <-second:
	case <-time.After(time.Second):
		unblock()
		<-second
		t.Fatal("scan waited instead of observing the already-claimed budget")
	}
	unblock()
	<-first
	waitSchedulerPathsIdle(t, s)
	count, _ := repo.CountProbesToday(context.Background(), "user1", "ws1", repo.policies[0].ID, probeBudgetDayStart(time.Now()))
	if calls.Load() != 1 || count != 1 {
		t.Fatalf("overlapping scans exceeded one probe: HTTP=%d budget=%d", calls.Load(), count)
	}
}

func TestRecheckWaitsForOwnAccountBatch(t *testing.T) {
	s, repo := schedulerPathsService(t, "first,slow")
	repo.policies[0].AutoDegradeEnabled = true
	s.repo = &governanceCanceledRepository{repo}
	slowStarted, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var firstCalls atomic.Int64
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body.Model == "first" {
			if firstCalls.Add(1) == 1 {
				return schedulerPathsResponse(request, http.StatusInternalServerError), nil
			}
			return schedulerPathsResponse(request, http.StatusOK), nil
		}
		slowStarted <- struct{}{}
		select {
		case <-release:
			return schedulerPathsResponse(request, http.StatusOK), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstTick := schedulerPathsTick(s, ctx)
	defer func() { cancel(); unblock(); <-firstTick; waitSchedulerPathsIdle(t, s) }()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("the account's second model did not start")
	}
	state, _ := repo.GetState(ctx, "sub2api:ws1:a", "first")
	if state == nil || !state.RecheckPending {
		t.Fatalf("first model did not enter an immediate recheck: %+v", state)
	}
	secondTick := schedulerPathsTick(s, ctx)
	select {
	case <-secondTick:
	case <-time.After(time.Second):
		cancel()
		unblock()
		<-secondTick
		t.Fatal("the next scan waited for a second task on the same account")
	}
	if firstCalls.Load() != 1 {
		t.Fatalf("the first model rechecked while its account was still running: %d", firstCalls.Load())
	}
	unblock()
	<-firstTick
	waitSchedulerPathsIdle(t, s)
	s.runSchedulerTick(ctx)
	waitSchedulerPathsIdle(t, s)
	if firstCalls.Load() != 2 {
		t.Fatalf("the next available scan did not dispatch the pending recheck: %d", firstCalls.Load())
	}
}

type schedulerPathsWorkspaceReader struct{ fakeAdminGroupKeyReader }

func (r schedulerPathsWorkspaceReader) RequireSession(_ context.Context, _, workspace string) (upstream.Session, error) {
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: workspace}, nil
}

type schedulerPathsPriorityReader struct {
	fakePlatformGroupReader
	mu            sync.Mutex
	priorities    map[string]int
	priorityReads map[string]int
	started       chan string
	writes        int
}

func (r *schedulerPathsPriorityReader) FetchAdminAllGroupsContext(ctx context.Context, session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	if handle := actionLeaseFromContext(ctx); handle != nil && strings.HasPrefix(handle.Key, "connection-health:priority-sync:") {
		r.mu.Lock()
		r.priorityReads[session.BaseURL]++
		r.mu.Unlock()
		if r.started != nil {
			r.started <- session.BaseURL
		}
	}
	return r.fakePlatformGroupReader.FetchAdminAllGroups(session)
}

func (r *schedulerPathsPriorityReader) ListAdminGroupAccountsContext(_ context.Context, session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	accounts, err := r.fakePlatformGroupReader.ListAdminGroupAccounts(session, group)
	accounts = append([]upstream.AdminGroupAccountInfo(nil), accounts...)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range accounts {
		priority := r.priorities[session.BaseURL]
		accounts[i].Priority = &priority
	}
	return accounts, err
}

func (r *schedulerPathsPriorityReader) UpdateAdminTargetPriority(session upstream.Session, _ string, priority int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.priorities[session.BaseURL] = priority
	r.writes++
	return nil
}

func schedulerPathsEnablePriority(s *Service, repo *fakeRepository) *schedulerPathsPriorityReader {
	r := &schedulerPathsPriorityReader{fakePlatformGroupReader: s.platformGroups.(fakePlatformGroupReader), priorities: map[string]int{"ws1": 1000, "ws2": 1000}, priorityReads: map[string]int{}, started: make(chan string, 16)}
	s.platformGroups, s.priorityActions = r, r
	s.mySites = schedulerPathsWorkspaceReader{}
	s.sites = fakeSiteLookup{site: &upstream.Site{}}
	fallback := 0.4
	repo.groupSortSettings["user1|ws1|g1"] = GroupProbeSortSetting{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", FallbackMultiplier: &fallback}
	repo.groupSortSettings["user1|ws2|g1"] = GroupProbeSortSetting{UserID: "user1", AdminAccountID: "ws2", AdminGroupID: "g1", FallbackMultiplier: &fallback}
	return r
}

func TestTickPrioritySyncSkipsWorkspaceWithInFlightProbes(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	repo.policies[0].AutoDegradeEnabled = true
	other := repo.policies[0]
	other.ID, other.AdminAccountID, other.StrategyMode = "other", "ws2", StrategyModeMultiplierOnly
	repo.policies = append(repo.policies, other)
	repo.groupAssignments = append(repo.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws2", AdminGroupID: "g1", PolicyID: other.ID})
	r := schedulerPathsEnablePriority(s, repo)
	old, protocol := time.Now().Add(-time.Hour), TestProtocolChatCompletions
	repo.states["sub2api:ws1:a"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:a", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", RuleVersion: RuleVersionV2, State: StateDegraded, LastProbeAt: &old, LastAppliedProbeProtocol: &protocol, HealthEvidenceStatus: "valid", HealthEvidenceProtocol: &protocol}}
	started, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		select {
		case <-release:
			return schedulerPathsResponse(request, http.StatusOK), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := schedulerPathsTick(s, ctx)
	defer func() { cancel(); unblock(); <-first; waitSchedulerPathsIdle(t, s) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not begin")
	}
	r.mu.Lock()
	readsBefore, otherBefore, writesBefore := r.priorityReads["ws1"], r.priorityReads["ws2"], r.writes
	r.mu.Unlock()
	before, _ := repo.GetPriorityWorkspaceSyncState(ctx, "user1", "ws1")
	repo.priorityLeaseMu.Lock()
	leasesBefore := repo.priorityLeaseCount["user1|ws1"]
	repo.priorityLeaseMu.Unlock()
	second := schedulerPathsTick(s, ctx)
	select {
	case <-second:
	case <-time.After(time.Second):
		cancel()
		unblock()
		<-second
		t.Fatal("tick priority sync waited for an in-flight workspace")
	}
	after, _ := repo.GetPriorityWorkspaceSyncState(ctx, "user1", "ws1")
	r.mu.Lock()
	readsAfter, otherAfter, writesAfter := r.priorityReads["ws1"], r.priorityReads["ws2"], r.writes
	r.mu.Unlock()
	repo.priorityLeaseMu.Lock()
	leasesAfter := repo.priorityLeaseCount["user1|ws1"]
	repo.priorityLeaseMu.Unlock()
	if readsAfter != readsBefore || leasesAfter != leasesBefore || writesAfter != writesBefore || otherAfter != otherBefore+1 {
		t.Fatalf("skipped workspace changed or other workspace stalled: reads=%d/%d locks=%d/%d writes=%d/%d other=%d/%d", readsBefore, readsAfter, leasesBefore, leasesAfter, writesBefore, writesAfter, otherBefore, otherAfter)
	}
	if count := schedulerPathsSkipped(s, "priority_workspace"); count != 1 {
		t.Fatalf("skipped priority workspace count=%d, want 1", count)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("skipped workspace status changed: before=%+v after=%+v", before, after)
	}
	unblock()
	<-first
	waitSchedulerPathsIdle(t, s)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.priorityReads["ws1"] <= readsAfter || r.writes <= writesAfter {
		t.Fatalf("completed probe did not trigger its normal priority write: reads=%v writes=%d", r.priorityReads, r.writes)
	}
}

func TestTickPrioritySyncWithPendingSignatureStillRuns(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	r := schedulerPathsEnablePriority(s, repo)
	started, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		select {
		case <-release:
			return schedulerPathsResponse(request, http.StatusOK), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := schedulerPathsTick(s, ctx)
	defer func() { cancel(); unblock(); <-first; waitSchedulerPathsIdle(t, s) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	<-r.started
	repo.priorityWorkspaceMu.Lock()
	state := repo.priorityWorkspaces["user1|ws1"]
	state.UserID, state.AdminAccountID, state.PendingSignature = "user1", "ws1", "pending-fixture"
	repo.priorityWorkspaces["user1|ws1"] = state
	repo.priorityWorkspaceMu.Unlock()
	second := schedulerPathsTick(s, ctx)
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("pending configuration did not enter priority reconciliation")
	}
	select {
	case <-second:
		t.Fatal("pending configuration skipped its occupied account lock")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("pending configuration did not finish after the account lock was released")
	}
	<-first
	waitSchedulerPathsIdle(t, s)
	current, _ := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if current == nil || current.PendingSignature != "" {
		t.Fatalf("configuration did not finish applying: %+v", current)
	}
}

func TestManualProbeLeaseKeepsTickPriorityWaiting(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	r := schedulerPathsEnablePriority(s, repo)
	_, unlock, acquired, err := s.acquireActionTargetLease(context.Background(), "sub2api:ws1:a", false)
	if err != nil || !acquired {
		t.Fatalf("manual fixture lock: acquired=%v err=%v", acquired, err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	var requests atomic.Int64
	normal := s.probeRunner.client.Transport
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) { requests.Add(1); return normal.RoundTrip(request) })
	done := schedulerPathsTick(s, context.Background())
	select {
	case <-r.started:
	case <-time.After(time.Second):
		t.Fatal("priority sync did not start")
	}
	select {
	case <-done:
		t.Fatal("manual formal probe was mistaken for an in-flight scheduled probe")
	case <-time.After(30 * time.Millisecond):
	}
	if requests.Load() != 0 {
		t.Fatal("scheduled probe started while the manual account lock was occupied")
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("priority reconciliation did not resume after the manual probe released its lock")
	}
	waitSchedulerPathsIdle(t, s)
	if requests.Load() != 1 {
		t.Fatalf("due probe was not dispatched after the manual probe: %d", requests.Load())
	}
}

type schedulerPathsHealthSyncRepository struct {
	*fakeRepository
	block   atomic.Bool
	calls   atomic.Int64
	entered chan struct{}
	release <-chan struct{}
}

func (r *schedulerPathsHealthSyncRepository) ListPolicies(ctx context.Context, user, workspace string) ([]Policy, error) {
	if r.block.Load() {
		r.calls.Add(1)
		r.entered <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.fakeRepository.ListPolicies(ctx, user, workspace)
}

func TestScheduledCommitTriggersHealthPrioritySync(t *testing.T) {
	s, base := schedulerPathsService(t, "gpt-4o")
	r := schedulerPathsEnablePriority(s, base)
	for _, id := range []string{"b", "c"} {
		r.accountsByGrp["g1"] = append(r.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: id, Models: "gpt-4o", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
		r.credByAccount[id] = upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "fixture"}
	}
	probeRelease, syncRelease := make(chan struct{}), make(chan struct{})
	var probeOnce, syncOnce sync.Once
	unblockProbes := func() { probeOnce.Do(func() { close(probeRelease) }) }
	unblockSync := func() { syncOnce.Do(func() { close(syncRelease) }) }
	defer unblockProbes()
	defer unblockSync()
	repo := &schedulerPathsHealthSyncRepository{fakeRepository: base, entered: make(chan struct{}, 4), release: syncRelease}
	s.repo = repo
	started := make(chan struct{}, 3)
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		select {
		case <-probeRelease:
			return schedulerPathsResponse(request, http.StatusOK), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	done := schedulerPathsTick(s, context.Background())
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("scheduled jobs did not overlap")
		}
	}
	repo.block.Store(true)
	unblockProbes()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("committed jobs did not trigger health priority synchronization")
	}
	if waiter, ok := any(s).(interface{ waitScheduledProbes(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := waiter.waitScheduledProbes(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s.priorityTriggerMu.Lock()
	running, pending := s.priorityHealthRunning["user1\x00ws1"], s.priorityHealthPending["user1\x00ws1"]
	s.priorityTriggerMu.Unlock()
	if !running || !pending || repo.calls.Load() != 1 {
		t.Errorf("expected one health sync plus one pending rerun: running=%v pending=%v executions=%d", running, pending, repo.calls.Load())
	}
	unblockSync()
	<-done
	waitSchedulerPathsIdle(t, s)
	if repo.calls.Load() != 2 {
		t.Fatalf("health syncs were not coalesced into two executions: %d", repo.calls.Load())
	}
}

func TestFloorKeepsOneAccountAcrossOverlappingJobs(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	repo.policies[0].AutoDegradeEnabled, repo.policies[0].AutoRemoteActionEnabled = true, true
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.accountsByGrp["g1"] = append(reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: "b", Models: "gpt-4o", Status: "active", Schedulable: boolPointer(true), TempUnschedulableKnown: true})
	reader.credByAccount["b"] = upstream.ProbeCredential{BaseURL: "https://other.invalid", Key: "fixture"}
	s.platformGroups = reader
	now, protocol := time.Now(), TestProtocolChatCompletions
	for _, id := range []string{"a", "b"} {
		last := now.Add(-time.Hour)
		if id == "b" {
			last = now
		}
		repo.states["sub2api:ws1:"+id] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:" + id, UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", RuleVersion: RuleVersionV2, State: StateSuspended, ConsecutiveFailures: 3, LastProbeAt: &last, LastAppliedProbeProtocol: &protocol, HealthEvidenceStatus: "valid", HealthEvidenceProtocol: &protocol}}
	}
	oldInventory := sub2APITestInventory(adminInventoryGroup{group: reader.groups[0], accounts: reader.accountsByGrp["g1"]})
	firstWrite, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var writes atomic.Int64
	platform := &fakePlatformActioner{afterSub2APIWrite: func(_, status string) {
		if status == "inactive" && writes.Add(1) == 1 {
			close(firstWrite)
			<-release
		}
	}}
	s.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
	secondProbe := make(chan struct{}, 1)
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "other.invalid" {
			secondProbe <- struct{}{}
		}
		return schedulerPathsResponse(request, http.StatusInternalServerError), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := schedulerPathsTick(s, ctx)
	defer func() { unblock(); <-first; waitSchedulerPathsIdle(t, s) }()
	select {
	case <-firstWrite:
	case <-time.After(time.Second):
		t.Fatal("first job did not reach its permitted account shutdown")
	}
	b, _ := repo.GetState(ctx, "sub2api:ws1:b", "gpt-4o")
	old := now.Add(-time.Hour)
	b.LastProbeAt = &old
	_ = repo.UpsertState(ctx, *b)
	second := schedulerPathsTick(s, ctx)
	select {
	case <-second:
	case <-time.After(time.Second):
		unblock()
		<-second
		t.Fatal("second scan waited for the first account's remote action")
	}
	select {
	case <-secondProbe:
	case <-time.After(time.Second):
		t.Fatal("second account did not probe while the first job remained active")
	}
	s.sub2APIFloorGuardFor("user1", "ws1").rememberInventory(*oldInventory)
	unblock()
	<-first
	waitSchedulerPathsIdle(t, s)
	if writes.Load() != 1 {
		t.Fatalf("overlapping jobs shut down %d of two usable accounts", writes.Load())
	}
}

func TestScheduledJobSkipsBusyTargetLease(t *testing.T) {
	s, _ := schedulerPathsService(t, "gpt-4o")
	jobs := s.collectAdminProbeJobsWithGroups(context.Background(), s.repo.(*fakeRepository).policies, nil, s.repo.(*fakeRepository).groupAssignments, nil)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%+v", jobs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, unlock, acquired, err := s.acquireActionTargetLease(ctx, jobs[0].target.TargetID, false)
	if err != nil || !acquired {
		t.Fatalf("fixture target lock: acquired=%v err=%v", acquired, err)
	}
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	var calls, released atomic.Int64
	s.probeRunner.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	})
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() { s.runAdminProbeJob(ctx, jobs[0], func() { released.Add(1) }, &wg); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		cancel()
		release()
		<-done
		t.Fatal("scheduled job waited for an occupied target lock")
	}
	wg.Wait()
	if calls.Load() != 0 || released.Load() != 1 {
		t.Fatalf("busy job calls=%d released slots=%d", calls.Load(), released.Load())
	}
	if count := schedulerPathsSkipped(s, "lease_busy"); count != 1 {
		t.Fatalf("busy target skip count=%d, want 1", count)
	}
}

func TestRestoreFlowsSkipBusyTargetLease(t *testing.T) {
	for _, mode := range []string{"unmanaged", "empty_group"} {
		t.Run(mode, func(t *testing.T) {
			repo := newFakeRepository()
			platform := &fakePlatformActioner{}
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "paths"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Status: "inactive", Models: "gpt-4o"}}}}
			s := &Service{repo: repo, platformGroups: reader, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, dispatcher: newRemoteActionDispatcher(nil, nil, platform), schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
			stored := TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalStatus: "active", LastAppliedStatus: "inactive"}
			repo.targetActionStates["user1|ws1|"+stored.TargetID] = stored
			run := func() {
				if mode == "unmanaged" {
					s.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, []TargetActionState{stored}, make(adminInventoryCache))
				} else {
					s.restoreEmptySub2APIGroups(context.Background(), []TargetActionState{stored}, make(adminInventoryCache))
				}
			}
			_, unlock, acquired, err := s.acquireActionTargetLease(context.Background(), stored.TargetID, false)
			if err != nil || !acquired {
				t.Fatalf("fixture target lock: acquired=%v err=%v", acquired, err)
			}
			var once sync.Once
			release := func() { once.Do(unlock) }
			defer release()
			done := make(chan struct{})
			go func() { run(); close(done) }()
			select {
			case <-done:
			case <-time.After(100 * time.Millisecond):
				release()
				<-done
				t.Fatal("restore flow waited for an occupied target lock")
			}
			if len(platform.sub2APICalls) != 0 {
				t.Fatalf("busy target was restored: %+v", platform.sub2APICalls)
			}
			release()
			run()
			if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].status != "active" {
				t.Fatalf("next available scan did not restore: %+v", platform.sub2APICalls)
			}
		})
	}
}

func TestTryAcquireAutomaticKeepsManualReservation(t *testing.T) {
	for _, mode := range []string{"workspace", "global"} {
		t.Run(mode, func(t *testing.T) {
			limiter := newProbeConcurrencyLimiter(12, 6)
			limiter.mu.Lock()
			usage := limiter.usageLocked("workspace")
			if mode == "workspace" {
				usage.active, usage.manualWaiters, limiter.globalActive, limiter.globalManualWaiters = 5, 1, 5, 1
			} else {
				limiter.globalActive, limiter.globalManualWaiters = 11, 1
			}
			limiter.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			// The old automatic path supplies a business RED without depending on a
			// helper that did not yet exist in that implementation.
			var release func()
			var acquired bool
			if immediate, ok := any(limiter).(interface{ tryAcquireAutomatic(string) (func(), bool) }); ok {
				release, acquired = immediate.tryAcquireAutomatic("workspace")
			} else {
				release, acquired = limiter.acquireAutomatic(ctx, "workspace")
			}
			if acquired {
				release()
				t.Fatal("automatic job used the last slot reserved for a waiting manual request")
			}
			if ctx.Err() != nil {
				t.Fatal("automatic admission waited instead of immediately preserving manual capacity")
			}
		})
	}
}

func TestCanceledProbeIsNotCommitted(t *testing.T) {
	s, repo := schedulerPathsService(t, "gpt-4o")
	s.repo = &governanceCanceledRepository{repo}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)), Request: request}, nil
	})
	target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: string(upstream.PlatformSub2API), AccountID: "a", InventoryComplete: true, TestMemberships: []TestConfigurationSource{{AdminGroupID: "g1"}}, TestConfiguration: defaultTestConfiguration()}
	spec := probeModelSpec{modelName: "gpt-4o", policy: repo.policies[0]}
	result, err := s.probeTargetOnce(ctx, "user1", "ws1", target, upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "fixture"}, spec, false)
	if err == nil || result != nil || len(repo.states) != 0 || len(repo.events) != 0 {
		t.Fatalf("cancelled probe was committed: result=%+v err=%v states=%+v events=%+v", result, err, repo.states, repo.events)
	}
}

type schedulerPathsCanceledCommitRepository struct {
	*governanceCanceledRepository
	calls atomic.Int64
}

func (r *schedulerPathsCanceledCommitRepository) CommitTargetProbe(ctx context.Context, input TargetProbeCommit) (TargetProbeCommitResult, error) {
	r.calls.Add(1)
	return r.governanceCanceledRepository.CommitTargetProbe(ctx, input)
}

func TestCanceledProbeStopsBeforeCommitCall(t *testing.T) {
	s, base := schedulerPathsService(t, "gpt-4o")
	repo := &schedulerPathsCanceledCommitRepository{governanceCanceledRepository: &governanceCanceledRepository{base}}
	s.repo = repo
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		cancel()
		return schedulerPathsResponse(request, http.StatusOK), nil
	})
	target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: string(upstream.PlatformSub2API), AccountID: "a", InventoryComplete: true, TestMemberships: []TestConfigurationSource{{AdminGroupID: "g1"}}, TestConfiguration: defaultTestConfiguration()}
	_, _ = s.probeTargetOnce(ctx, "user1", "ws1", target, upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "fixture"}, probeModelSpec{modelName: "gpt-4o", policy: base.policies[0]}, false)
	if repo.calls.Load() != 0 {
		t.Fatalf("cancelled HTTP result reached %d storage commit calls", repo.calls.Load())
	}
}

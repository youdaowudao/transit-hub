package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type panicOnceQuestionAnswerContext struct {
	context.Context
	panicked atomic.Bool
}

func (c *panicOnceQuestionAnswerContext) Err() error {
	if c.panicked.CompareAndSwap(false, true) {
		panic("injected question selection panic")
	}
	return c.Context.Err()
}

func TestQuestionAnswerDispatcherLoopSurvivesPanic(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, repo, "selection-panic")
	run.ctx = &panicOnceQuestionAnswerContext{Context: run.ctx}
	s.questionAnswerDispatcherDone = make(chan struct{})
	asks := make(chan struct{}, 1)
	normal := s.questionAnswerHTTP.client.Transport
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		asks <- struct{}{}
		return normal.RoundTrip(request)
	})
	// The start wake remains buffered, so a failed selection gets another explicit
	// wake without timers or a real network request.
	go s.runQuestionAnswerDispatcher()
	select {
	case <-asks:
	case <-time.After(time.Second):
		t.Fatal("dispatcher stopped after a question-selection panic")
	}
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("dispatch after a question-selection panic did not complete")
	}
	s.questionAnswerStop()
	select {
	case <-s.questionAnswerDispatcherDone:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not exit")
	}
	s.questionAnswerWG.Wait()
}

func panicPathsScheduleService(t *testing.T, repo questionAnswerScheduleRepository) *Service {
	t.Helper()
	s := panicRegressionService(t, newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"}))
	s.repo = newFakeRepository()
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	s.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	s.platformGroups = &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "selected", Name: "Selected"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "a", Name: "A", Models: "available"}, {ID: "b", Name: "B", Models: "available"}, {ID: "c", Name: "C", Models: "available"}}},
		credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://unit.invalid", Key: "fixture"}, "b": {BaseURL: "https://unit.invalid", Key: "fixture"}, "c": {BaseURL: "https://unit.invalid", Key: "fixture"}},
	}, allAccounts: []upstream.AdminGroupAccountInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	s.questionAnswerSchedules = repo
	s.questionAnswerScheduleWake = make(chan struct{}, 1)
	s.questionAnswerScheduleNow = time.Now
	s.modelDiscovery = &ModelDiscoveryRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"available"},{"id":"model-a"}]}`)), Request: request}, nil
	})}}
	return s
}

func panicPathsScheduleHandle(t *testing.T, s *Service, execution QuestionAnswerScheduleExecution) *questionAnswerScheduleHandle {
	t.Helper()
	ctx, cancel := context.WithCancel(s.questionAnswerCtx)
	t.Cleanup(cancel)
	return &questionAnswerScheduleHandle{execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
}

func TestSchedulePreparationPanicReleasesReservations(t *testing.T) {
	repo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Enabled: true, Body: "question"}}}
	s := panicPathsScheduleService(t, repo)
	var discoveries atomic.Int64
	var reservedAtPanic atomic.Int64
	normal := s.modelDiscovery.client.Transport
	s.modelDiscovery.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if discoveries.Add(1) == 2 {
			s.questionAnswerMu.Lock()
			reservedAtPanic.Store(int64(len(s.questionAnswerStarts)))
			s.questionAnswerMu.Unlock()
			panic("injected preparation panic with two reservations")
		}
		return normal.RoundTrip(request)
	})
	handle := panicPathsScheduleHandle(t, s, c2ScheduleExecutionFixture("groups"))
	s.launchQuestionAnswerSchedulePreparation(handle)
	select {
	case <-handle.prepareDone:
	case <-time.After(time.Second):
		t.Fatal("preparation panic did not settle")
	}
	if reservedAtPanic.Load() != 2 || handle.preparation.outcome.Status != "failed" || handle.preparation.outcome.Reason != "internal_error" {
		t.Fatalf("reserved=%d outcome=%+v", reservedAtPanic.Load(), handle.preparation.outcome)
	}
	s.questionAnswerMu.Lock()
	remaining := len(s.questionAnswerStarts)
	s.questionAnswerMu.Unlock()
	if remaining != 0 {
		t.Fatalf("preparation leaked %d reservations", remaining)
	}
	// The coordinator can release the already-settled preparation again safely.
	s.releaseQuestionAnswerScheduleReservations(handle)
	s.questionAnswerStartWG.Wait()
}

type panicPathsBuildRepository struct {
	*Repository
	qa          *fakeQuestionAnswerRepository
	mu          sync.Mutex
	failures    map[string]string
	panicTarget string
	panicked    atomic.Bool
}

func (r *panicPathsBuildRepository) CreateScheduledQuestionAnswerBatch(ctx context.Context, user, _ string, _, target, batch string) ([]QuestionAnswerRecord, bool, error) {
	records, err := r.qa.CreateQuestionAnswerBatch(ctx, user, target, batch, []string{"available"}, []string{"question"}, QuestionAnswerReasoningEffortMedium, 1)
	if target == r.panicTarget && r.panicked.CompareAndSwap(false, true) {
		panic("injected scheduled create-result panic")
	}
	return records, true, err
}

func (r *panicPathsBuildRepository) FailQuestionAnswerScheduleTarget(_ context.Context, _, _, _, target, _, reason string, _ func() time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[target] = reason
	return nil
}

type panicPathsBuildSessionReader struct {
	fakeMySitesReader
	calls atomic.Int64
}

func (r *panicPathsBuildSessionReader) RequireSession(ctx context.Context, user, workspace string) (upstream.Session, error) {
	if r.calls.Add(1) == 2 {
		panic("injected second-target recovery panic")
	}
	return r.fakeMySitesReader.RequireSession(ctx, user, workspace)
}

func panicPathsBuildExecution() QuestionAnswerScheduleExecution {
	execution := c2ScheduleExecutionFixture("groups")
	for _, account := range []string{"a", "b", "c"} {
		execution.Targets = append(execution.Targets, QuestionAnswerScheduleExecutionTarget{ID: "batch-" + account, TargetID: "sub2api:ws1:" + account, Status: "pending"})
	}
	return execution
}

func TestScheduleBuildPanicBeforeCoreFailsOnlyThatTarget(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"})
	repo := &panicPathsBuildRepository{qa: base, failures: map[string]string{}}
	s := panicPathsScheduleService(t, repo)
	s.questionAnswers = base
	s.mySites = &panicPathsBuildSessionReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	execution := panicPathsBuildExecution()
	handle := panicPathsScheduleHandle(t, s, execution)
	s.launchQuestionAnswerScheduleBuild(handle, execution)
	select {
	case <-handle.buildDone:
	case <-time.After(time.Second):
		t.Fatal("build panic did not settle")
	}
	if repo.failures["sub2api:ws1:b"] != "internal_error" || len(repo.failures) != 1 {
		t.Fatalf("target failures=%+v", repo.failures)
	}
	for _, account := range []string{"a", "c"} {
		panicRegressionDispatch(t, s)
		records, _ := base.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:"+account, "batch-"+account)
		if len(records) != 1 || records[0].Status != QuestionAnswerSucceeded {
			t.Fatalf("unaffected target %s did not complete: %+v", account, records)
		}
	}
	s.questionAnswerMu.Lock()
	remaining := len(s.questionAnswerStarts)
	s.questionAnswerMu.Unlock()
	if remaining != 0 {
		t.Fatalf("build leaked %d reservations", remaining)
	}
	s.questionAnswerStartWG.Wait()
}

type panicPathsCoordinatorRepository struct {
	*Repository
	calls   atomic.Int64
	retried chan struct{}
	cancel  context.CancelFunc
}

func (r *panicPathsCoordinatorRepository) ListNonterminalQuestionAnswerScheduleExecutions(context.Context) ([]QuestionAnswerScheduleExecution, error) {
	if r.calls.Add(1) == 1 {
		panic("injected coordinator reconciliation panic")
	}
	close(r.retried)
	r.cancel()
	return nil, nil
}

func (*panicPathsCoordinatorRepository) ListDueQuestionAnswerSchedules(context.Context, time.Time) ([]QuestionAnswerSchedule, error) {
	return nil, nil
}

func TestScheduleCoordinatorSurvivesPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := &panicPathsCoordinatorRepository{retried: make(chan struct{}), cancel: cancel}
	s := &Service{questionAnswerSchedules: repo, questionAnswerScheduleCtx: ctx, questionAnswerScheduleDone: make(chan struct{}), questionAnswerScheduleWake: make(chan struct{}, 1), questionAnswerScheduleHandles: map[string]*questionAnswerScheduleHandle{}, questionAnswerScheduleNow: time.Now, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	s.questionAnswerScheduleWake <- struct{}{}
	go s.runQuestionAnswerScheduleCoordinator()
	select {
	case <-repo.retried:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not reconcile after a panic")
	}
	select {
	case <-s.questionAnswerScheduleDone:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not stop after cancellation")
	}
}

type panicPathsLockedCoordinatorRepository struct {
	*c2ScheduleLifecycleRepository
	claims atomic.Int64
	stop   context.CancelFunc
}

func (r *panicPathsLockedCoordinatorRepository) ListNonterminalQuestionAnswerScheduleExecutions(context.Context) ([]QuestionAnswerScheduleExecution, error) {
	return []QuestionAnswerScheduleExecution{{UserID: "user1", AdminAccountID: "ws1", Status: "preparing"}}, nil
}

func (r *panicPathsLockedCoordinatorRepository) ClaimNextQuestionAnswerScheduleExecution(context.Context, string, string, func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	if r.claims.Add(1) == 1 {
		panic("injected coordinator panic while holding workspace lock")
	}
	r.stop()
	return nil, nil
}

func TestScheduleCoordinatorPanicReleasesWorkspaceLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	repo := &panicPathsLockedCoordinatorRepository{c2ScheduleLifecycleRepository: &c2ScheduleLifecycleRepository{}, stop: cancel}
	s := &Service{questionAnswerSchedules: repo, questionAnswerScheduleCtx: ctx, questionAnswerScheduleDone: make(chan struct{}), questionAnswerScheduleWake: make(chan struct{}, 1), questionAnswerScheduleHandles: map[string]*questionAnswerScheduleHandle{}, schedulableActions: forbiddenAutomaticSchedulingWriter{t}}
	s.questionAnswerScheduleWake <- struct{}{}
	go s.runQuestionAnswerScheduleCoordinator()
	select {
	case <-s.questionAnswerScheduleDone:
	case <-time.After(time.Second):
		t.Fatal("coordinator could not reacquire its workspace lock after panic")
	}
	if repo.claims.Load() != 2 {
		t.Fatalf("claims=%d, wanted a successful retry", repo.claims.Load())
	}
	lock := s.questionAnswerScheduleWorkspaceLock("user1", "ws1")
	if !lock.TryLock() {
		t.Fatal("coordinator retained its workspace lock")
	}
	lock.Unlock()
}

func TestQuestionAnswerShutdownPanicThroughPublicEntry(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, base)
	panicRegressionStart(t, s, base, "public-shutdown-panic")
	s.questionAnswers = &panicShutdownRepository{base}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.ShutdownQuestionAnswers(ctx)
	if err == nil || !strings.Contains(err.Error(), "internal") || ctx.Err() != nil {
		t.Fatalf("public shutdown failed to return its readable internal error: %v", err)
	}
	s.questionAnswerMu.Lock()
	closed := questionAnswerChannelClosed(s.questionAnswerShutdown.done)
	s.questionAnswerMu.Unlock()
	if !closed {
		t.Fatal("public shutdown returned before its attempt settled")
	}
}

func TestQuestionAnswerStartPanicCleanupDoesNotEscape(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, &panicShutdownRepository{base})
	const target = "sub2api:ws1:acc-1"
	reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", target, "cleanup-panic")
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseQuestionAnswerTargetStart(reservation)
	panicRegressionCall(t, func() {
		_, startErr := s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
			_, _ = base.CreateQuestionAnswerBatch(ctx, "user1", target, "cleanup-panic", []string{"model-a"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1)
			panic("injected start panic followed by stop-storage panic")
		})
		if startErr == nil || !strings.Contains(startErr.Error(), "internal") {
			t.Errorf("start error=%v", startErr)
		}
	})
	s.questionAnswerMu.Lock()
	remaining := s.questionAnswerRuns[questionAnswerRunKey("user1", target)]
	s.questionAnswerMu.Unlock()
	records, _ := base.ListQuestionAnswerBatch(context.Background(), "user1", target, "cleanup-panic")
	if remaining == nil || len(records) != 1 || records[0].Status != QuestionAnswerPending || remaining.finalErr == nil {
		t.Fatalf("failed finalizer lost retry ownership or prematurely ended record: remaining=%v records=%+v", remaining != nil, records)
	}
	// The final storage panic is now the real finalizer failure. An explicit
	// recovery after storage returns must still end the same row without Ask.
	s.questionAnswers = base
	if _, err := s.stopQuestionAnswerBatchCore(context.Background(), "user1", target, "cleanup-panic"); err != nil {
		t.Fatalf("explicit cleanup retry: %v", err)
	}
	records, _ = base.ListQuestionAnswerBatch(context.Background(), "user1", target, "cleanup-panic")
	s.questionAnswerMu.Lock()
	remaining = s.questionAnswerRuns[questionAnswerRunKey("user1", target)]
	s.questionAnswerMu.Unlock()
	if remaining != nil || len(records) != 1 || records[0].Status != QuestionAnswerFailed || records[0].ErrorType != "internal_error" {
		t.Fatalf("explicit cleanup did not settle original record: remaining=%v records=%+v", remaining != nil, records)
	}
}

type panicPathsRefreshReader struct{ fakePlatformGroupReader }

func (panicPathsRefreshReader) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	panic("injected admin-groups refresh panic")
}

func TestAdminGroupsRefreshPipelinePanicTerminal(t *testing.T) {
	s := newAdminGroupsService(panicPathsRefreshReader{}, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	s.schedulableActions = forbiddenAutomaticSchedulingWriter{t}
	run := newUnitRefreshRun(t)
	t.Cleanup(run.cancel)
	subscription, _ := run.subscribe()
	s.refreshRunWG.Add(1)
	go s.executeAdminGroupsRefreshRun(run, "user1")
	for {
		select {
		case _, open := <-subscription.Signals:
			latest := run.latest()
			if latest.Terminal != nil {
				if latest.Terminal.Status != "failed" || latest.Terminal.ErrorKey != "refresh_internal_error" {
					t.Fatalf("refresh terminal=%+v", latest.Terminal)
				}
				s.refreshRunWG.Wait()
				return
			}
			if !open {
				t.Fatal("refresh subscriber closed without a terminal")
			}
		case <-time.After(time.Second):
			t.Fatal("refresh panic did not publish its terminal to the subscriber")
		}
	}
}

type panicPathsManualRepository struct {
	*fakeQuestionAnswerRepository
	panicked atomic.Bool
}

func (r *panicPathsManualRepository) CreateQuestionAnswerBatch(ctx context.Context, user, target, batch string, models, questions []string, effort QuestionAnswerReasoningEffort, repeat int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error) {
	records, err := r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, user, target, batch, models, questions, effort, repeat, snapshots...)
	if r.panicked.CompareAndSwap(false, true) {
		panic("injected manual entry create-result panic")
	}
	return records, err
}

func TestQuestionAnswerStartPanicThroughPublicManualEntry(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"})
	repo := &panicPathsManualRepository{fakeQuestionAnswerRepository: base}
	s := panicPathsScheduleService(t, nil)
	s.questionAnswers = repo
	input := QuestionAnswerStartInput{Models: []string{"available"}, QuestionIDs: []string{"question"}, RepeatCount: json.RawMessage(`1`)}
	panicRegressionCall(t, func() {
		_, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", input)
		if err == nil || !strings.Contains(err.Error(), "internal") {
			t.Errorf("manual start error=%v", err)
		}
	})
	base.mu.Lock()
	if len(base.records) != 1 || base.records[0].Status != QuestionAnswerFailed || base.records[0].ErrorType != "internal_error" {
		t.Errorf("manual uncertain records=%+v", base.records)
	}
	base.mu.Unlock()
	batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", input)
	if err != nil {
		t.Fatalf("manual entry still busy: %v", err)
	}
	panicRegressionDispatch(t, s)
	records, _ := base.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", batch.BatchID)
	if len(records) != 1 || records[0].Status != QuestionAnswerSucceeded {
		t.Fatalf("retry through manual entry did not complete: %+v", records)
	}
}

func TestQuestionAnswerStartPanicThroughScheduledEntry(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"})
	repo := &panicPathsBuildRepository{qa: base, failures: map[string]string{}, panicTarget: "sub2api:ws1:a"}
	s := panicPathsScheduleService(t, repo)
	s.questionAnswers = base
	execution := panicPathsBuildExecution()
	execution.Targets = execution.Targets[:1]
	handle := panicPathsScheduleHandle(t, s, execution)
	s.launchQuestionAnswerScheduleBuild(handle, execution)
	select {
	case <-handle.buildDone:
	case <-time.After(time.Second):
		t.Fatal("scheduled start panic did not settle")
	}
	records, _ := base.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", "batch-a")
	if len(records) != 1 || records[0].Status != QuestionAnswerFailed || records[0].ErrorType != "internal_error" || repo.failures["sub2api:ws1:a"] != "internal_error" {
		t.Fatalf("scheduled records=%+v failures=%+v", records, repo.failures)
	}
	reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", "sub2api:ws1:a", "retry-a")
	if err != nil {
		t.Fatalf("scheduled panic retained target: %v", err)
	}
	s.releaseQuestionAnswerTargetStart(reservation)
	s.questionAnswerStartWG.Wait()
}

package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type c2ScheduleInventoryReader struct {
	fakePlatformGroupReader
	allAccounts     []upstream.AdminGroupAccountInfo
	allErr          error
	credentialCalls atomic.Int64
}

func (r *c2ScheduleInventoryReader) ListSub2APIAdminAccountsContext(context.Context, upstream.Session) ([]upstream.AdminGroupAccountInfo, error) {
	return r.allAccounts, r.allErr
}
func (r *c2ScheduleInventoryReader) ResolveProbeCredential(session upstream.Session, account upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	r.credentialCalls.Add(1)
	return r.fakePlatformGroupReader.ResolveProbeCredential(session, account)
}

type c2SchedulePreparationRepository struct {
	*Repository
	questions    []TestQuestion
	configs      []GroupTestConfig
	reads        atomic.Int64
	finishCalls  atomic.Int64
	outcome      QuestionAnswerSchedulePreparationOutcome
	finishHook   func()
	finishResult *QuestionAnswerScheduleExecution
}

func (r *c2SchedulePreparationRepository) ReadQuestionAnswerSchedulePreparationConfiguration(context.Context, string, string, []string) ([]TestQuestion, []GroupTestConfig, error) {
	r.reads.Add(1)
	return r.questions, r.configs, nil
}
func (r *c2SchedulePreparationRepository) FinishQuestionAnswerSchedulePreparation(_ context.Context, _, _, _ string, outcome QuestionAnswerSchedulePreparationOutcome, _ func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	r.finishCalls.Add(1)
	r.outcome = outcome
	if r.finishHook != nil {
		r.finishHook()
	}
	return r.finishResult, nil
}
func c2ScheduleExecutionFixture(mode string) QuestionAnswerScheduleExecution {
	now := time.Now().UTC()
	return QuestionAnswerScheduleExecution{ID: "execution", UserID: "user1", AdminAccountID: "ws1", ScheduleID: "schedule", Status: "active", Trigger: "run_now", CreatedAt: now, ScheduledFor: now, ConfigSnapshot: QuestionAnswerScheduleSnapshot{Requested: QuestionAnswerScheduleRequested{Schedule: QuestionAnswerScheduleConfig{Name: "fixture", TargetMode: mode, SelectedGroupIDs: []string{"selected"}, Models: []string{"available", "missing"}, QuestionIDs: []string{"question"}, ReasoningEffort: "medium", RepeatCount: 2}, Limits: defaultQuestionAnswerScheduleLimits(), Timezone: "Asia/Singapore"}}}
}
func c2SchedulePreparationFixture(t *testing.T, reader *c2ScheduleInventoryReader, repo *c2SchedulePreparationRepository) (*Service, *atomic.Int64) {
	t.Helper()
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, newFakeRepository())
	service.questionAnswers = newFakeQuestionAnswerRepository()
	service.questionAnswerSchedules = repo
	calls := &atomic.Int64{}
	service.modelDiscovery = &ModelDiscoveryRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"available"}]}`)), Request: request}, nil
	})}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.ShutdownQuestionAnswers(ctx); err != nil {
			t.Errorf("C1 fixture cleanup: %v", err)
		}
	})
	return service, calls
}

func TestC2QuestionAnswerSchedulePreparationInventoryDeletionAndCompleteness(t *testing.T) {
	for _, test := range []struct {
		name                   string
		accounts               []upstream.AdminGroupAccountInfo
		inventoryErr           bool
		groupErr               bool
		wantStatus, wantReason string
		wantModels             int64
	}{
		{"all missing", nil, false, false, "skipped", "all_accounts_missing", 0},
		{"partial deletion", []upstream.AdminGroupAccountInfo{{ID: "a", Name: "A"}}, false, false, "", "", 1},
		{"account inventory failed", nil, true, false, "failed", "test_configuration_unavailable", 0},
		{"unselected group failed", []upstream.AdminGroupAccountInfo{{ID: "a"}}, false, true, "failed", "test_configuration_unavailable", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected"}, {ID: "unselected"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "a"}}, "unselected": {{ID: "a"}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}, allAccounts: test.accounts}
			if test.inventoryErr {
				reader.allErr = errors.New("inventory unavailable")
			}
			if test.groupErr {
				reader.errByGrp = map[string]error{"unselected": errors.New("incomplete membership")}
			}
			repo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Name: "Q", Body: "frozen", Keywords: []string{"answer"}, Enabled: true}}}
			service, calls := c2SchedulePreparationFixture(t, reader, repo)
			execution := c2ScheduleExecutionFixture("accounts")
			execution.ConfigSnapshot.Requested.Schedule.SelectedGroupIDs = nil
			execution.ConfigSnapshot.Requested.Schedule.SelectedAccountTargetIDs = []string{"sub2api:ws1:a", "sub2api:ws1:b"}
			prep := service.resolveQuestionAnswerScheduleExecution(context.Background(), execution)
			for _, reservation := range prep.reservations {
				service.releaseQuestionAnswerTargetStart(reservation)
			}
			if prep.outcome.Status != test.wantStatus || prep.outcome.Reason != test.wantReason {
				t.Fatalf("outcome status/reason=%s/%s", prep.outcome.Status, prep.outcome.Reason)
			}
			if calls.Load() != test.wantModels {
				t.Fatalf("models calls=%d want=%d", calls.Load(), test.wantModels)
			}
			if test.name == "all missing" {
				if repo.reads.Load() != 0 || reader.credentialCalls.Load() != 0 || len(prep.outcome.Targets) != 2 {
					t.Fatalf("all-missing accessed request configuration/credential or lost evidence")
				}
				for _, target := range prep.outcome.Targets {
					if target.Status != "failed" || target.StatusReason != "account_not_found" || target.PlannedRequestCount != 0 || len(target.AvailableModels) > 0 || len(target.UnavailableModels) > 0 {
						t.Fatalf("missing target fabricated request fact: %+v", target)
					}
					wire, _ := json.Marshal(target.TestConfigurationSnapshot)
					if string(wire) != "{}" {
						t.Fatalf("missing target configuration=%s", wire)
					}
				}
			}
			if test.name == "partial deletion" {
				if prep.outcome.Targets[1].StatusReason != "account_not_found" || prep.outcome.Targets[0].PlannedRequestCount != 2 || len(prep.outcome.Targets[0].UnavailableModels) != 1 {
					t.Fatalf("partial deletion did not preserve complete matrix evidence: %+v", prep.outcome.Targets)
				}
			}
		})
	}
}

func TestC2QuestionAnswerSchedulePreparationUsesCompleteMembershipsWithoutMonitoringFilters(t *testing.T) {
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected", Name: "selected"}, {ID: "outside", Name: "outside"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "a", Priority: intPointer(1), Schedulable: boolPointer(false)}, {ID: "b"}}, "outside": {{ID: "a"}, {ID: "c"}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}, "b": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}}
	repo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "Q", Enabled: true}}, configs: []GroupTestConfig{{AdminGroupID: "selected", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10}, {AdminGroupID: "outside", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10}}}
	service, calls := c2SchedulePreparationFixture(t, reader, repo)
	prep := service.resolveQuestionAnswerScheduleExecution(context.Background(), c2ScheduleExecutionFixture("groups"))
	for _, reservation := range prep.reservations {
		service.releaseQuestionAnswerTargetStart(reservation)
	}
	if len(prep.outcome.Targets) != 2 {
		t.Fatalf("selected range included outside-only account or filtered selected account: %+v", prep.outcome.Targets)
	}
	a, b := prep.outcome.Targets[0], prep.outcome.Targets[1]
	if a.TargetID != "sub2api:ws1:a" || a.StatusReason != "test_configuration_conflict" || len(a.TestConfigurationSnapshot.Memberships) != 2 || len(a.MatchedGroupsSnapshot) != 1 {
		t.Fatalf("outside membership conflict not preserved: %+v", a)
	}
	if b.TargetID != "sub2api:ws1:b" || b.Status != "pending" || calls.Load() != 1 {
		t.Fatalf("unconflicted selected target did not continue: %+v", b)
	}
}

func TestC2QuestionAnswerSchedulePreparationFreezesAllQuestionsAndProtocolsBeforeDiscovery(t *testing.T) {
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "selected"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"selected": {{ID: "a"}, {ID: "b"}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}, "b": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}}
	repo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "old body", Keywords: []string{"old keyword"}, Enabled: true}}, configs: []GroupTestConfig{{AdminGroupID: "selected", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10}}}
	service, _ := c2SchedulePreparationFixture(t, reader, repo)
	calls := 0
	service.modelDiscovery.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		repo.questions[0].Body = "new body"
		repo.questions[0].Keywords[0] = "new keyword"
		repo.configs[0].Protocol = TestProtocolChatCompletions
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"available"}]}`)), Request: request}, nil
	})
	prep := service.resolveQuestionAnswerScheduleExecution(context.Background(), c2ScheduleExecutionFixture("groups"))
	for _, reservation := range prep.reservations {
		service.releaseQuestionAnswerTargetStart(reservation)
	}
	if repo.reads.Load() != 1 || calls != 2 {
		t.Fatalf("configuration reread or incomplete model freeze reads=%d calls=%d", repo.reads.Load(), calls)
	}
	if got := prep.outcome.Resolved.Questions[0]; got.Body != "old body" || !reflect.DeepEqual(got.Keywords, []string{"old keyword"}) {
		t.Fatalf("question snapshot changed after discovery began: %+v", got)
	}
	for _, target := range prep.outcome.Targets {
		if target.TestConfigurationSnapshot.Protocol != TestProtocolResponses {
			t.Fatalf("mixed current protocols: %+v", target)
		}
	}
}

func TestC2QuestionAnswerSchedulePreparationCannotFinishBeforeWorkerExitAndReservationRelease(t *testing.T) {
	service := &Service{questionAnswerRuns: map[string]*activeQuestionAnswerBatch{}, questionAnswerSchedules: &c2SchedulePreparationRepository{}}
	service.initializeQuestionAnswerRuntime()
	defer service.ShutdownQuestionAnswers(context.Background())
	reservation, err := service.reserveQuestionAnswerTargetStart(nil, "user1", "sub2api:ws1:a", "batch")
	if err != nil {
		t.Fatal(err)
	}
	execution := c2ScheduleExecutionFixture("accounts")
	handleCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{UserID: execution.UserID, AdminAccountID: execution.AdminAccountID, ScheduleID: execution.ScheduleID, ExecutionID: execution.ID}, execution: execution, ctx: handleCtx, cancel: cancel, prepareDone: make(chan struct{}), preparation: questionAnswerSchedulePreparation{reservations: map[string]*questionAnswerTargetStart{"sub2api:ws1:a": reservation}, outcome: QuestionAnswerSchedulePreparationOutcome{Status: "skipped", Reason: "empty_scope"}}}
	repo := service.questionAnswerSchedules.(*c2SchedulePreparationRepository)
	repo.finishResult = &QuestionAnswerScheduleExecution{ID: execution.ID, Status: "skipped"}
	repo.finishHook = func() {
		service.questionAnswerMu.Lock()
		defer service.questionAnswerMu.Unlock()
		if len(service.questionAnswerStarts) != 0 {
			t.Error("terminal transaction preceded reservation release")
		}
	}
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
		t.Fatal(err)
	}
	if repo.finishCalls.Load() != 0 || reservation.released {
		t.Fatal("worker still running but execution/reservation was released")
	}
	close(handle.prepareDone)
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
		t.Fatal(err)
	}
	if repo.finishCalls.Load() != 1 || !reservation.released {
		t.Fatal("exited preparation did not release then commit terminal result")
	}
}

type c2ScheduleLifecycleRepository struct {
	*c2SchedulePreparationRepository
	current       QuestionAnswerScheduleExecution
	finished      atomic.Int64
	unsettled     atomic.Int64
	parentErr     error
	parentPresent bool
}

func (r *c2ScheduleLifecycleRepository) FinishQuestionAnswerScheduleExecution(_ context.Context, _, _, _ string, settled bool, reason string, _ func() time.Time) (*QuestionAnswerScheduleExecution, error) {
	if !settled {
		r.unsettled.Add(1)
		r.current.StatusReason = reason
		return &r.current, nil
	}
	r.finished.Add(1)
	r.current.Status, r.current.StatusReason = summarizeQuestionAnswerScheduleExecution(r.current, r.current.Targets)
	return &r.current, nil
}
func (r *c2ScheduleLifecycleRepository) DecideQuestionAnswerScheduleTermination(context.Context, string, string, string, string, *int64, func() time.Time) (QuestionAnswerScheduleTerminationResult, error) {
	return QuestionAnswerScheduleTerminationResult{Execution: r.current, Conflict: true}, nil
}
func (r *c2ScheduleLifecycleRepository) GetQuestionAnswerScheduleExecution(context.Context, string, string, string) (*QuestionAnswerScheduleExecution, error) {
	return &r.current, nil
}
func (r *c2ScheduleLifecycleRepository) ListNonterminalQuestionAnswerScheduleExecutions(context.Context) ([]QuestionAnswerScheduleExecution, error) {
	return nil, nil
}
func (r *c2ScheduleLifecycleRepository) ListDueQuestionAnswerSchedules(context.Context, time.Time) ([]QuestionAnswerSchedule, error) {
	return nil, nil
}
func (r *c2ScheduleLifecycleRepository) ListQuestionAnswerScheduleHandleParents(_ context.Context, identities []QuestionAnswerScheduleHandleIdentity) (map[string]bool, error) {
	result := map[string]bool{}
	for _, identity := range identities {
		result[identity.ExecutionID] = r.parentPresent
	}
	return result, r.parentErr
}

type c2ScheduleFinalizerRepository struct {
	*fakeQuestionAnswerRepository
	fail atomic.Bool
}

func (r *c2ScheduleFinalizerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	if r.fail.Load() {
		return true, errQuestionAnswerTestFinalize
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}

func TestC2QuestionAnswerScheduleTerminationWaitsForBlockedPreparationExit(t *testing.T) {
	for _, cause := range []string{"user_cancel", "execution_timeout"} {
		t.Run(cause, func(t *testing.T) {
			execution := c2ScheduleExecutionFixture("accounts")
			execution.TerminationCause = cause
			execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{}}
			repo := &c2ScheduleLifecycleRepository{current: execution}
			service := &Service{questionAnswerSchedules: repo}
			service.initializeQuestionAnswerRuntime()
			defer service.ShutdownQuestionAnswers(context.Background())
			reservation, err := service.reserveQuestionAnswerTargetStart(nil, "user1", "sub2api:ws1:a", "batch")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, prepareDone: make(chan struct{}), preparation: questionAnswerSchedulePreparation{reservations: map[string]*questionAnswerTargetStart{"sub2api:ws1:a": reservation}}, batchIDs: map[string]string{}}
			if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
				t.Fatal(err)
			}
			if repo.finished.Load() != 0 || reservation.released || ctx.Err() == nil {
				t.Fatal("termination did not cancel preparation while preserving its resources")
			}
			close(handle.prepareDone)
			if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if cause == "execution_timeout" {
				want = "skipped"
			}
			if repo.finished.Load() != 1 || repo.current.Status != want || !reservation.released || repo.current.TerminationCause != cause {
				t.Fatalf("termination result=%+v released=%v", repo.current, reservation.released)
			}
		})
	}
}

func TestC2QuestionAnswerScheduleFinalizerFailureKeepsExecutionAndExactReservation(t *testing.T) {
	execution := c2ScheduleExecutionFixture("accounts")
	execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{}}
	execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", TargetID: "sub2api:ws1:a", Status: "batch_created", BatchAvailable: true, Stats: QuestionAnswerStats{Requests: QuestionAnswerRequestStats{Submitted: 1, Succeeded: 1}}}}
	repo := &c2ScheduleLifecycleRepository{current: execution}
	qa := &c2ScheduleFinalizerRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository()}
	qa.fail.Store(true)
	now := time.Now().UTC()
	qa.records = []QuestionAnswerRecord{{ID: "record", BatchID: "exact", TargetID: "sub2api:ws1:a", Status: QuestionAnswerSucceeded, CreatedAt: now, UpdatedAt: now, CompletedAt: &now}}
	service := &Service{questionAnswerSchedules: repo, questionAnswers: qa}
	service.initializeQuestionAnswerRuntime()
	defer func() { qa.fail.Store(false); service.ShutdownQuestionAnswers(context.Background()) }()
	runCtx, runCancel := context.WithCancel(context.Background())
	settled := make(chan struct{})
	close(settled)
	run := &activeQuestionAnswerBatch{userID: "user1", targetID: "sub2api:ws1:a", batchID: "exact", ctx: runCtx, cancel: runCancel, done: make(chan struct{}), finalizeSettled: settled, finalizeGeneration: 1, finalizeResults: map[uint64]error{1: errQuestionAnswerTestFinalize}, finalErr: errQuestionAnswerTestFinalize, stopReason: QuestionAnswerErrorStorage}
	key := questionAnswerRunKey("user1", "sub2api:ws1:a")
	service.questionAnswerMu.Lock()
	service.questionAnswerRuns[key] = run
	service.questionAnswerOrder = append(service.questionAnswerOrder, key)
	service.questionAnswerMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buildDone := make(chan struct{})
	close(buildDone)
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, buildDone: buildDone, batchIDs: map[string]string{"sub2api:ws1:a": "exact"}}
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err == nil {
		t.Fatal("failed C1 finalizer was hidden")
	}
	if repo.finished.Load() != 0 || repo.current.Status != "active" || repo.current.StatusReason != "c1_finalization_failed" || questionAnswerChannelClosed(run.done) {
		t.Fatalf("execution or C1 reservation released on failed finalizer: %+v", repo.current)
	}
	qa.fail.Store(false)
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, repo.current); err != nil {
		t.Fatal(err)
	}
	if repo.finished.Load() != 1 || repo.current.Status != "completed" || !questionAnswerChannelClosed(run.done) {
		t.Fatalf("accurate finalizer retry did not close execution: %+v", repo.current)
	}
}

func TestC2QuestionAnswerScheduleCancelCompletedBusinessReturnsAccurateRuntimeCurrent(t *testing.T) {
	for _, state := range []string{"exited", "preparation still exiting", "exact run still exiting", "newer batch"} {
		t.Run(state, func(t *testing.T) {
			execution := c2ScheduleExecutionFixture("accounts")
			execution.StatusReason = "c1_finalization_pending"
			execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{}}
			execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", TargetID: "sub2api:ws1:a", Status: "batch_created", BatchAvailable: true, Stats: QuestionAnswerStats{Requests: QuestionAnswerRequestStats{Submitted: 1, Succeeded: 1}}}}
			repo := &c2ScheduleLifecycleRepository{current: execution}
			service := &Service{questionAnswerSchedules: repo, questionAnswers: newFakeQuestionAnswerRepository(), accounts: fakeAdminAccountResolver{id: "ws1"}}
			service.initializeQuestionAnswerRuntime()
			defer service.ShutdownQuestionAnswers(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if state == "preparation still exiting" {
				done := make(chan struct{})
				defer close(done)
				service.questionAnswerScheduleHandles = map[string]*questionAnswerScheduleHandle{execution.ID: {identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, ctx: ctx, cancel: cancel, prepareDone: done, batchIDs: map[string]string{"sub2api:ws1:a": "exact"}}}
			}
			if state == "exact run still exiting" || state == "newer batch" {
				batchID := "exact"
				if state == "newer batch" {
					batchID = "newer"
				}
				key := questionAnswerRunKey("user1", "sub2api:ws1:a")
				service.questionAnswerMu.Lock()
				service.questionAnswerRuns[key] = &activeQuestionAnswerBatch{userID: "user1", targetID: "sub2api:ws1:a", batchID: batchID, ctx: ctx, cancel: cancel, done: make(chan struct{}), finalizeResults: map[uint64]error{}}
				service.questionAnswerMu.Unlock()
			}
			result, err := service.CancelQuestionAnswerScheduleExecution(context.Background(), "user1", execution.ID, execution.Version)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Conflict || result.Accepted || result.Execution.TerminationCause != "" || result.Execution.CancelRequestedAt != nil {
				t.Fatalf("normal business completion accepted or fabricated cancellation: %+v", result)
			}
			if state == "exited" || state == "newer batch" {
				if result.Execution.Status != "completed" || repo.finished.Load() != 1 {
					t.Fatalf("exited exact batch did not return authoritative completed current: %+v", result.Execution)
				}
			} else if result.Execution.Status != "active" || result.Execution.StatusReason != "c1_finalization_pending" || repo.finished.Load() != 0 || ctx.Err() != nil {
				t.Fatalf("pending runtime was cancelled or prematurely finalized: %+v", result.Execution)
			}
		})
	}
}

type c2ScheduleRecoveryRepository struct {
	*c2SchedulePreparationRepository
	execution QuestionAnswerScheduleExecution
	qa        *fakeQuestionAnswerRepository
	creates   atomic.Int64
}

func (r *c2ScheduleRecoveryRepository) CreateScheduledQuestionAnswerBatch(_ context.Context, user, workspace, executionID, targetID, batchID string) ([]QuestionAnswerRecord, bool, error) {
	r.creates.Add(1)
	if user != r.execution.UserID || workspace != r.execution.AdminAccountID || executionID != r.execution.ID || targetID != r.execution.Targets[0].TargetID || batchID != r.execution.Targets[0].ID {
		return nil, false, errors.New("recovery changed accepted ownership or exact batch")
	}
	question := r.execution.ConfigSnapshot.Resolved.Questions[0]
	protocol := r.execution.Targets[0].TestConfigurationSnapshot.Protocol
	now := time.Now().UTC()
	record := QuestionAnswerRecord{ID: "recovered-record", TargetID: targetID, BatchID: batchID, ModelName: r.execution.Targets[0].AvailableModels[0], RequestProtocol: &protocol, QuestionID: question.ID, QuestionName: question.Name, QuestionBody: question.Body, QuestionKeywordSnapshot: question.Keywords, Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now}
	r.qa.mu.Lock()
	r.qa.records = []QuestionAnswerRecord{record}
	r.qa.mu.Unlock()
	return []QuestionAnswerRecord{record}, true, nil
}

func (r *c2ScheduleRecoveryRepository) FailQuestionAnswerScheduleTarget(context.Context, string, string, string, string, string, string, func() time.Time) error {
	return nil
}

type c2ScheduleRecoveryMySitesReader struct {
	fakeMySitesReader
	workspaces chan string
}

func (r c2ScheduleRecoveryMySitesReader) RequireSession(ctx context.Context, user, workspace string) (upstream.Session, error) {
	r.workspaces <- workspace
	return r.fakeMySitesReader.RequireSession(ctx, user, workspace)
}

func TestC2QuestionAnswerScheduleRecoveryUsesFrozenFactsAndAcceptedWorkspace(t *testing.T) {
	execution := c2ScheduleExecutionFixture("accounts")
	execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "question", Name: "old name", Body: "old body", Keywords: []string{"old answer"}}}}
	execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", ExecutionID: execution.ID, TargetID: "sub2api:ws1:a", Status: "pending", AvailableModels: []string{"old-model"}, PlannedRequestCount: 1, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "ws1", InventoryComplete: true}, Protocol: TestProtocolResponses}}}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "new-group"}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}, allAccounts: []upstream.AdminGroupAccountInfo{{ID: "a", Models: "new-model"}}}
	qa := newFakeQuestionAnswerRepository()
	repo := &c2ScheduleRecoveryRepository{c2SchedulePreparationRepository: &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "new body", Keywords: []string{"new answer"}, Enabled: true}}, configs: []GroupTestConfig{{AdminGroupID: "new-group", Protocol: TestProtocolChatCompletions}}}, execution: execution, qa: qa}
	service, models := c2SchedulePreparationFixture(t, reader, repo.c2SchedulePreparationRepository)
	service.questionAnswerSchedules = repo
	service.questionAnswers = qa
	service.accounts = fakeAdminAccountResolver{id: "ws2"}
	workspaces := make(chan string, 2)
	service.mySites = c2ScheduleRecoveryMySitesReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, workspaces: workspaces}
	type requestFact struct{ path, body string }
	requests := make(chan requestFact, 2)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requests <- requestFact{request.URL.Path, string(body)}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"old answer"}]}]}`)), Request: request}, nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.buildDone:
	case <-time.After(time.Second):
		t.Fatal("recovery did not finish building from frozen facts")
	}
	deadline := time.Now().Add(time.Second)
	var batch QuestionAnswerBatch
	for time.Now().Before(deadline) {
		var err error
		batch, err = service.getQuestionAnswerBatchCore(context.Background(), execution.UserID, execution.Targets[0].TargetID, "exact")
		if err == nil && !batch.Active {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if batch.Active || len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].QuestionBody != "old body" || batch.Records[0].ModelName != "old-model" || batch.Records[0].AnswerJudgment == nil || *batch.Records[0].AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("recovery lost frozen request/automatic judgment: %+v", batch)
	}
	if repo.reads.Load() != 0 || models.Load() != 0 || repo.creates.Load() != 1 || reader.credentialCalls.Load() != 1 || len(workspaces) != 1 || <-workspaces != "ws1" {
		t.Fatal("recovery reread current facts, rediscovered models, or used switched workspace")
	}
	select {
	case fact := <-requests:
		if fact.path != "/v1/responses" || !strings.Contains(fact.body, "old body") || !strings.Contains(fact.body, "old-model") || strings.Contains(fact.body, "new body") || strings.Contains(fact.body, "new-model") || len(requests) != 0 {
			t.Fatalf("recovery request violated frozen protocol/model/question: %+v", fact)
		}
	default:
		t.Fatal("recovered request was not dispatched through C1")
	}
}

func TestC2QuestionAnswerScheduleHardDeleteRetainsHandlesOnParentSQLFailureAndCleansOnlyExactBatch(t *testing.T) {
	execution := c2ScheduleExecutionFixture("accounts")
	qa := newFakeQuestionAnswerRepository()
	now := time.Now().UTC()
	qa.records = []QuestionAnswerRecord{{ID: "own", BatchID: "exact", TargetID: "sub2api:ws1:a", Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now}, {ID: "manual", BatchID: "manual", TargetID: "sub2api:ws1:b", Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now}, {ID: "other", BatchID: "other", TargetID: "sub2api:ws2:a", Status: QuestionAnswerPending, CreatedAt: now, UpdatedAt: now}}
	repo := &c2ScheduleLifecycleRepository{parentErr: errors.New("temporary SQL failure")}
	service := &Service{questionAnswerSchedules: repo, questionAnswers: qa, questionAnswerConcurrencyLimit: 15}
	service.initializeQuestionAnswerRuntime()
	defer service.ShutdownQuestionAnswers(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{UserID: execution.UserID, AdminAccountID: execution.AdminAccountID, ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{"sub2api:ws1:a": "exact"}}
	service.questionAnswerScheduleHandles = map[string]*questionAnswerScheduleHandle{execution.ID: handle}
	if err := service.reconcileQuestionAnswerScheduleExecutions(context.Background()); err == nil {
		t.Fatal("parent SQL failure was hidden")
	}
	if len(service.questionAnswerScheduleHandles) != 1 || ctx.Err() != nil {
		t.Fatal("parent query failure was treated as deletion")
	}
	repo.parentErr = nil
	if err := service.reconcileQuestionAnswerScheduleExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(service.questionAnswerScheduleHandles) != 0 || ctx.Err() == nil || service.questionAnswerConcurrencyLimit != 15 {
		t.Fatal("confirmed hard deletion did not clean retained handle or changed singleton")
	}
	qa.mu.Lock()
	defer qa.mu.Unlock()
	if qa.records[0].Status != QuestionAnswerCancelled || qa.records[1].Status != QuestionAnswerPending || qa.records[2].Status != QuestionAnswerPending {
		t.Fatalf("hard deletion affected manual/other workspace records: %+v", qa.records)
	}
}

func TestC2QuestionAnswerScheduleShutdownWaitsForCallsBeforeReleasingPreparation(t *testing.T) {
	repo := &c2ScheduleLifecycleRepository{parentPresent: true}
	service := &Service{questionAnswerSchedules: repo}
	service.initializeQuestionAnswerRuntime()
	defer service.ShutdownQuestionAnswers(context.Background())
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	execution := c2ScheduleExecutionFixture("accounts")
	ctx, cancel := context.WithCancel(rootCtx)
	reservation, err := service.reserveQuestionAnswerTargetStart(ctx, "user1", "sub2api:ws1:a", "batch")
	if err != nil {
		t.Fatal(err)
	}
	workerExit := make(chan struct{})
	prepareDone := make(chan struct{})
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, prepareDone: prepareDone, preparation: questionAnswerSchedulePreparation{reservations: map[string]*questionAnswerTargetStart{"sub2api:ws1:a": reservation}}}
	service.questionAnswerScheduleCtx = rootCtx
	service.questionAnswerScheduleCancel = rootCancel
	service.questionAnswerScheduleDone = make(chan struct{})
	service.questionAnswerScheduleWake = make(chan struct{}, 1)
	service.questionAnswerScheduleHandles = map[string]*questionAnswerScheduleHandle{execution.ID: handle}
	go func() { <-ctx.Done(); <-workerExit; close(prepareDone) }()
	go service.runQuestionAnswerScheduleCoordinator()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = service.ShutdownQuestionAnswerSchedules(shutdownCtx)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || reservation.released {
		t.Fatalf("shutdown finished or released before call exit: err=%v released=%v", err, reservation.released)
	}
	close(workerExit)
	shutdownCtx, stop = context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = service.ShutdownQuestionAnswerSchedules(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if !reservation.released {
		t.Fatal("shutdown did not release exited preparation")
	}
}

package connection_health

import (
	"context"
	"errors"
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

type completionConfirmationRepository struct {
	*fakeQuestionAnswerRepository
	service       *Service
	calls         []string
	deadlines     []time.Time
	failRecord    string
	failFinal     bool
	waitRecord    string
	finalizeCalls int
	earlyStops    atomic.Int32
}

func (r *completionConfirmationRepository) ConfirmOrSaveQuestionAnswerCompletion(ctx context.Context, user, target, batch, id string, completion QuestionAnswerCompletion, allow bool) (bool, error) {
	if !r.service.questionAnswerMu.TryLock() {
		return false, errors.New("confirmation attempted while service lock was held")
	}
	r.service.questionAnswerMu.Unlock()
	r.calls = append(r.calls, id)
	deadline, ok := ctx.Deadline()
	if !ok {
		return false, errors.New("confirmation has no storage deadline")
	}
	r.deadlines = append(r.deadlines, deadline)
	if id == r.waitRecord {
		<-ctx.Done()
		return false, ctx.Err()
	}
	if id == r.failRecord {
		return false, errors.New("confirmation interrupted for " + id)
	}
	return r.fakeQuestionAnswerRepository.ConfirmOrSaveQuestionAnswerCompletion(ctx, user, target, batch, id, completion, allow)
}
func (r *completionConfirmationRepository) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	if !r.service.questionAnswerMu.TryLock() {
		return false, errors.New("batch ending attempted while service lock was held")
	}
	r.service.questionAnswerMu.Unlock()
	r.finalizeCalls++
	deadline, ok := ctx.Deadline()
	if !ok {
		return false, errors.New("batch ending has no storage deadline")
	}
	r.deadlines = append(r.deadlines, deadline)
	if r.failFinal {
		return false, errors.New("batch ending interrupted after answers were saved")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}
func (r *completionConfirmationRepository) StopPendingQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	r.earlyStops.Add(1)
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}

// These records represent completed HTTP requests whose first writes were not
// acknowledged. The production finalizer must save the original answers in
// selection order and retain every occupied position until batch ending succeeds.
func storageConfirmationRun(t *testing.T, count int) (*Service, *activeQuestionAnswerBatch, *completionConfirmationRepository) {
	t.Helper()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "question", Enabled: true})
	repo := &completionConfirmationRepository{fakeQuestionAnswerRepository: base}
	s := panicRegressionService(t, repo)
	repo.service = s
	run := panicRegressionStart(t, s, base, "confirmation-run")
	base.mu.Lock()
	seed := base.records[0]
	base.records = nil
	for i := 0; i < count; i++ {
		record := seed
		record.ID = fmt.Sprintf("confirmation-%02d", i)
		record.Status = QuestionAnswerRunning
		base.records = append(base.records, record)
	}
	run.records = append([]QuestionAnswerRecord(nil), base.records...)
	base.mu.Unlock()
	run.next = count
	run.heldSlots = count
	s.questionAnswerInFlight = count
	run.progress = map[string]*QuestionAnswerProgress{}
	for _, record := range run.records {
		progress := newQuestionAnswerProgress(time.Now())
		progress.startRequest(time.Minute, time.Now())
		run.progress[record.ID] = progress
		completion := questionAnswerTestCompletion(QuestionAnswerSucceeded, "answer "+record.ID, "")
		completion.Diagnostics = progress.snapshot(record.ID).Diagnostics
		if _, ok := s.freezeQuestionAnswerCompletionLocked(run, record, completion); !ok {
			t.Fatal("could not freeze selected result")
		}
	}
	run.stopReason = QuestionAnswerErrorStorage
	run.cancel()
	return s, run, repo
}
func storageRunFinalizationAttempt(t *testing.T, s *Service, run *activeQuestionAnswerBatch) error {
	t.Helper()
	key := questionAnswerRunKey(run.userID, run.targetID)
	s.questionAnswerMu.Lock()
	if run.finalErr != nil {
		s.resetQuestionAnswerFinalizationLocked(run)
	}
	if !s.beginQuestionAnswerRunFinalizationLocked(key, run) {
		s.questionAnswerMu.Unlock()
		t.Fatal("finalization did not start")
	}
	s.questionAnswerMu.Unlock()
	return s.finalizeQuestionAnswerRun(key, run)
}
func TestQuestionAnswerStorageConfirmationOrderRetainsAllHeldUntilBatchEnds(t *testing.T) {
	s, run, repo := storageConfirmationRun(t, 15)
	last := run.records[14].ID
	repo.failRecord = last
	if err := storageRunFinalizationAttempt(t, s, run); err == nil {
		t.Fatal("unconfirmed last record was treated as saved")
	}
	if len(run.pendingCompletions) != 1 || len(run.progress) != 1 || run.heldSlots != 15 || s.questionAnswerInFlight != 15 || repo.finalizeCalls != 0 {
		t.Fatalf("14 confirmed state: pending=%d progress=%d held=%d occupied=%d finalizes=%d", len(run.pendingCompletions), len(run.progress), run.heldSlots, s.questionAnswerInFlight, repo.finalizeCalls)
	}
	for i, record := range run.records {
		if repo.calls[i] != record.ID {
			t.Fatal("confirmation reordered records")
		}
	}
	repo.failRecord = ""
	repo.failFinal = true
	if err := storageRunFinalizationAttempt(t, s, run); err == nil {
		t.Fatal("last batch ending failure was ignored")
	}
	if len(repo.calls) != 16 || repo.calls[15] != last || len(run.pendingCompletions) != 0 || len(run.progress) != 0 || run.heldSlots != 15 || s.questionAnswerInFlight != 15 {
		t.Fatal("confirmed results were retried or held positions released before batch ending")
	}
	projection, current := s.projectQuestionAnswerFinalization(run.userID, run.targetID, run, true)
	if !current || projection == nil || projection.HeldCount != 15 || projection.Recovery != "cancel" || !strings.Contains(projection.Message, "batch ending interrupted") {
		t.Fatalf("all saved ending failure lost its location: %+v", projection)
	}
	repo.failFinal = false
	if err := storageRunFinalizationAttempt(t, s, run); err != nil {
		t.Fatal(err)
	}
	if len(repo.calls) != 16 || repo.finalizeCalls != 2 || run.heldSlots != 0 || s.questionAnswerInFlight != 0 || len(s.questionAnswerRuns) != 0 || repo.earlyStops.Load() != 0 {
		t.Fatal("retry rewrote confirmed rows or retained finished ownership")
	}
	records, _ := repo.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
	for _, record := range records {
		if record.Status != QuestionAnswerSucceeded || record.AnswerBody != "answer "+record.ID {
			t.Fatalf("saved answer was lost: %+v", record)
		}
	}
}
func TestQuestionAnswerStorageFinalizationSharesSingleBudget(t *testing.T) {
	s, run, repo := storageConfirmationRun(t, 3)
	s.questionAnswerStorageTimeout = 45 * time.Millisecond
	repo.waitRecord = run.records[1].ID
	started := time.Now()
	err := storageRunFinalizationAttempt(t, s, run)
	if !errors.Is(err, context.DeadlineExceeded) || len(repo.calls) != 2 || len(run.pendingCompletions) != 2 || repo.finalizeCalls != 0 {
		t.Fatalf("attempt did not share a single deadline: err=%v elapsed=%v calls=%d remaining=%d", err, time.Since(started), len(repo.calls), len(run.pendingCompletions))
	}
	if len(repo.deadlines) != 2 || !repo.deadlines[0].Equal(repo.deadlines[1]) {
		t.Fatal("each record received a fresh storage budget")
	}
	repo.waitRecord = ""
	if err := storageRunFinalizationAttempt(t, s, run); err != nil {
		t.Fatal(err)
	}
	if len(repo.calls) != 4 {
		t.Fatal("a confirmed result was retried")
	}
}
func TestQuestionAnswerStorageAllowPendingRequiresAllCurrentSelectionFacts(t *testing.T) {
	for _, broken := range []string{"none", "selected-time", "failure-step", "http-started", "successful", "not-selected", "other-run", "target", "batch", "record"} {
		t.Run(broken, func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			s := panicRegressionService(t, base)
			run := panicRegressionStart(t, s, base, "pending-evidence")
			_, _, record, ok := s.nextQuestionAnswerDispatchLocked()
			if !ok {
				t.Fatal("selection failed")
			}
			completion := questionAnswerTestCompletion(QuestionAnswerFailed, "", QuestionAnswerErrorStorage)
			completion.Diagnostics = run.progress[record.ID].snapshot(record.ID).Diagnostics
			completion.Diagnostics.FailureStep = "starting"
			switch broken {
			case "selected-time":
				completion.Diagnostics.SelectedAt = nil
			case "failure-step":
				completion.Diagnostics.FailureStep = "request"
			case "http-started":
				now := time.Now()
				completion.Diagnostics.RequestStartedAt = &now
			case "successful":
				completion = questionAnswerTestCompletion(QuestionAnswerSucceeded, "answer", "")
				completion.Diagnostics = run.progress[record.ID].snapshot(record.ID).Diagnostics
				completion.Diagnostics.FailureStep = "starting"
			case "not-selected":
				run.next = 0
			case "other-run":
				s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)] = &activeQuestionAnswerBatch{}
			case "target":
				record.TargetID = "sub2api:other:a"
			case "batch":
				record.BatchID = "another"
			case "record":
				record.ID = "another"
			}
			got := s.allowPendingQuestionAnswerCompletionLocked(run, record, completion)
			if got != (broken == "none") {
				t.Fatalf("allowPending=%v after breaking %s", got, broken)
			}
		})
	}
}
func TestQuestionAnswerStorageFrozenCompletionIsImmutableAcrossConcurrentStop(t *testing.T) {
	for _, stopFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(stopFirst), func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			s := panicRegressionService(t, base)
			run := panicRegressionStart(t, s, base, "freeze-stop")
			_, _, record, _ := s.nextQuestionAnswerDispatchLocked()
			progress := run.progress[record.ID]
			progress.startRequest(time.Minute, time.Now())
			completion := questionAnswerTestCompletion(QuestionAnswerSucceeded, "complete answer", "")
			completion.Diagnostics = progress.snapshot(record.ID).Diagnostics
			stop := func() {
				s.questionAnswerMu.Lock()
				run.stopReason = string(QuestionAnswerCancelled)
				s.questionAnswerMu.Unlock()
			}
			if stopFirst {
				stop()
			}
			s.questionAnswerMu.Lock()
			sealed, ok := s.freezeQuestionAnswerCompletionLocked(run, record, completion)
			s.questionAnswerMu.Unlock()
			if !ok {
				t.Fatal("no sealed result")
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); stop() }()
			wg.Wait()
			completion.AnswerBody = "replacement"
			completion.Diagnostics.FailureStep = "parse"
			sealed.Diagnostics.FailureStep = "receive"
			s.questionAnswerMu.Lock()
			again, _ := s.freezeQuestionAnswerCompletionLocked(run, record, completion)
			s.questionAnswerMu.Unlock()
			if again.Diagnostics.FailureStep != "" {
				t.Fatal("caller mutated immutable diagnostics")
			}
			if stopFirst {
				if again.Status != QuestionAnswerCancelled || again.AnswerBody != "" || again.AnswerJudgment != nil {
					t.Fatal("stop-first gained an answer")
				}
			} else if again.Status != QuestionAnswerSucceeded || again.AnswerBody != "complete answer" {
				t.Fatal("later stop changed an already sealed answer")
			}
		})
	}
}
func TestQuestionAnswerStorageCancellationCauseDoesNotInventStopSource(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			s := panicRegressionService(t, base)
			run := panicRegressionStart(t, s, base, "save-cancel")
			_, _, record, _ := s.nextQuestionAnswerDispatchLocked()
			if stopped {
				run.stopReason = string(QuestionAnswerCancelled)
				run.cancel()
			}
			s.recordQuestionAnswerStorageFailure(run, record, context.Canceled, false)
			got := run.progress[record.ID].snapshot(record.ID).StorageError
			if stopped && !strings.Contains(got, "被停止操作中断") {
				t.Fatal(got)
			}
			if !stopped && (!strings.Contains(got, "取消来源尚未确认") || strings.Contains(got, "被停止操作中断")) {
				t.Fatal(got)
			}
		})
	}
}

type storageFalseMarkRepository struct{ *fakeQuestionAnswerRepository }

func (r *storageFalseMarkRepository) MarkQuestionAnswerRunning(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func TestQuestionAnswerStorageMarkFalseOnlyReleasesExactTerminal(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			repo := &storageFalseMarkRepository{base}
			s := panicRegressionService(t, repo)
			run := panicRegressionStart(t, s, base, "mark-terminal")
			original := QuestionAnswerRecord{}
			base.mu.Lock()
			base.records[0].Status = QuestionAnswerSucceeded
			base.records[0].AnswerBody = "already saved"
			original = base.records[0]
			if !terminal {
				base.records[0].TargetID = "sub2api:ws1:another"
			}
			base.mu.Unlock()
			var asks atomic.Int32
			s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) { asks.Add(1); return nil, errors.New("must not send") })
			panicRegressionDispatch(t, s)
			if asks.Load() != 0 {
				t.Fatal("Mark false sent HTTP")
			}
			if terminal {
				if len(s.questionAnswerRuns) != 0 || s.questionAnswerInFlight != 0 {
					t.Fatal("exact terminal not released")
				}
				base.mu.Lock()
				unchanged := reflect.DeepEqual(original, base.records[0])
				base.mu.Unlock()
				if !unchanged {
					t.Fatal("terminal answer changed")
				}
			} else {
				storageREDSettled(t, s, run)
				if run.heldSlots != 1 || s.questionAnswerInFlight != 1 || run.finalErr == nil {
					t.Fatal("another target's terminal record released this position")
				}
			}
		})
	}
}

type storageSlowMarkRepository struct {
	*fakeQuestionAnswerRepository
	finished time.Time
}

func (r *storageSlowMarkRepository) MarkQuestionAnswerRunning(ctx context.Context, user, batch, id string) (bool, error) {
	select {
	case <-time.After(25 * time.Millisecond):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	ok, err := r.fakeQuestionAnswerRepository.MarkQuestionAnswerRunning(ctx, user, batch, id)
	r.finished = time.Now()
	return ok, err
}
func TestQuestionAnswerStorageStartWriteDoesNotConsumeHTTPBudget(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &storageSlowMarkRepository{fakeQuestionAnswerRepository: base}
	s := panicRegressionService(t, repo)
	s.questionAnswerTTL = time.Minute
	s.questionAnswerStorageTimeout = 60 * time.Millisecond
	run := panicRegressionStart(t, s, base, "independent-budget")
	var requestDeadline time.Time
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, _ := request.Context().Deadline()
		requestDeadline = deadline
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"answer"}}]}`))}, nil
	})
	panicRegressionDispatch(t, s)
	if requestDeadline.IsZero() || requestDeadline.Add(-s.questionAnswerTTL).Before(repo.finished) {
		t.Fatalf("HTTP budget started before the start write ended: request=%v write=%v", requestDeadline.Add(-s.questionAnswerTTL), repo.finished)
	}
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("normal answer did not finish")
	}
}

type storageNormalDeadlineRepository struct {
	*fakeQuestionAnswerRepository
	blockMark                    bool
	markContext, completeContext context.Context
}

func (r *storageNormalDeadlineRepository) MarkQuestionAnswerRunning(ctx context.Context, user, batch, id string) (bool, error) {
	r.markContext = ctx
	if r.blockMark {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return r.fakeQuestionAnswerRepository.MarkQuestionAnswerRunning(ctx, user, batch, id)
}
func (r *storageNormalDeadlineRepository) CompleteQuestionAnswer(ctx context.Context, user, batch, id string, c QuestionAnswerCompletion) (bool, error) {
	r.completeContext = ctx
	<-ctx.Done()
	return false, ctx.Err()
}
func TestQuestionAnswerStorageNormalWritesActuallyReachTheirOwnDeadline(t *testing.T) {
	for _, mark := range []bool{true, false} {
		t.Run(fmt.Sprint(mark), func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			repo := &storageNormalDeadlineRepository{fakeQuestionAnswerRepository: base, blockMark: mark}
			s := panicRegressionService(t, repo)
			s.questionAnswerStorageTimeout = 15 * time.Millisecond
			run := panicRegressionStart(t, s, base, "bounded-normal")
			var asks atomic.Int32
			normal := s.questionAnswerHTTP.client.Transport
			s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) { asks.Add(1); return normal.RoundTrip(request) })
			panicRegressionDispatch(t, s)
			select {
			case <-run.done:
			case <-time.After(time.Second):
				t.Fatal("normal write did not finish its bounded confirmation")
			}
			if repo.markContext == nil || repo.markContext == run.ctx {
				t.Fatal("Mark reused unbounded run context")
			}
			if _, has := repo.markContext.Deadline(); !has {
				t.Fatal("Mark has no deadline")
			}
			records, _ := base.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
			if mark {
				if asks.Load() != 0 || records[0].Status != QuestionAnswerFailed || records[0].Diagnostics == nil || records[0].Diagnostics.RequestStartedAt != nil {
					t.Fatal("start timeout sent HTTP or lost starting failure")
				}
			} else {
				if repo.completeContext == nil || repo.completeContext == run.ctx {
					t.Fatal("Complete reused unbounded run context")
				}
				if _, has := repo.completeContext.Deadline(); !has {
					t.Fatal("Complete has no deadline")
				}
				if asks.Load() != 1 || records[0].Status != QuestionAnswerSucceeded || records[0].AnswerBody != "ok" {
					t.Fatal("save timeout lost sealed answer or resent HTTP")
				}
			}
		})
	}
}

type storageEarlyStopRepository struct {
	*fakeQuestionAnswerRepository
	stops        atomic.Int32
	failComplete bool
}

func (r *storageEarlyStopRepository) CompleteQuestionAnswer(ctx context.Context, user, batch, id string, c QuestionAnswerCompletion) (bool, error) {
	if r.failComplete {
		return false, errors.New("ordinary result save interrupted")
	}
	return r.fakeQuestionAnswerRepository.CompleteQuestionAnswer(ctx, user, batch, id, c)
}
func (r *storageEarlyStopRepository) StopPendingQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	r.stops.Add(1)
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}
func TestQuestionAnswerStorageAllActiveStopPathsSkipEarlyPendingWrite(t *testing.T) {
	for _, path := range []string{"storage", "manual", "internal", "failed-start", "shutdown"} {
		t.Run(path, func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			repo := &storageEarlyStopRepository{fakeQuestionAnswerRepository: base, failComplete: path == "storage"}
			s := panicRegressionService(t, repo)
			var asks atomic.Int32
			normal := s.questionAnswerHTTP.client.Transport
			s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
				asks.Add(1)
				if path == "internal" {
					panic("injected response panic")
				}
				return normal.RoundTrip(request)
			})
			const target = "sub2api:ws1:acc-1"
			var run *activeQuestionAnswerBatch
			if path == "failed-start" {
				reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", target, "start-cleanup")
				if err != nil {
					t.Fatal(err)
				}
				defer s.releaseQuestionAnswerTargetStart(reservation)
				_, err = s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
					_, err := base.CreateQuestionAnswerBatch(ctx, "user1", target, "start-cleanup", []string{"m"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1)
					if err != nil {
						return nil, true, err
					}
					return nil, true, &uncertainQuestionAnswerCreateError{cause: errors.New("commit result unknown")}
				})
				if err == nil {
					t.Fatal("failed start became successful")
				}
			} else {
				run = panicRegressionStart(t, s, base, "active-stop")
				switch path {
				case "storage", "internal":
					panicRegressionDispatch(t, s)
				case "manual":
					if _, err := s.stopQuestionAnswerBatchCore(context.Background(), "user1", target, run.batchID); err != nil {
						t.Fatal(err)
					}
				case "shutdown":
					if err := s.ShutdownQuestionAnswers(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case <-run.done:
				case <-time.After(time.Second):
					t.Fatal("active path did not end")
				}
			}
			if repo.stops.Load() != 0 {
				t.Fatalf("%s called early StopPending %d times", path, repo.stops.Load())
			}
			if path == "storage" || path == "internal" {
				if asks.Load() != 1 {
					t.Fatal("selected request was not exactly once")
				}
			} else if asks.Load() != 0 {
				t.Fatal("stopped/unconfirmed creation sent HTTP")
			}
		})
	}
}
func TestQuestionAnswerStorageInactiveCancelKeepsOriginalPendingStop(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &storageEarlyStopRepository{fakeQuestionAnswerRepository: base}
	s := panicRegressionService(t, repo)
	const target = "sub2api:ws1:acc-1"
	if _, err := base.CreateQuestionAnswerBatch(context.Background(), "user1", target, "inactive", []string{"m"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1); err != nil {
		t.Fatal(err)
	}
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), "user1", target, "inactive")
	if err != nil || repo.stops.Load() != 1 || len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("inactive cancel changed: stops=%d batch=%+v err=%v", repo.stops.Load(), batch, err)
	}
}

type storagePanicCompletionRepository struct {
	*fakeQuestionAnswerRepository
	confirmationPanic atomic.Bool
}

func (r *storagePanicCompletionRepository) CompleteQuestionAnswer(context.Context, string, string, string, QuestionAnswerCompletion) (bool, error) {
	panic("injected completion panic after answer sealing")
}
func (r *storagePanicCompletionRepository) ConfirmOrSaveQuestionAnswerCompletion(ctx context.Context, user, target, batch, id string, c QuestionAnswerCompletion, allow bool) (bool, error) {
	if r.confirmationPanic.CompareAndSwap(true, false) {
		panic("injected confirmation panic")
	}
	return r.fakeQuestionAnswerRepository.ConfirmOrSaveQuestionAnswerCompletion(ctx, user, target, batch, id, c, allow)
}
func TestQuestionAnswerStoragePanicWhileSavingKeepsSealedResultForExplicitRetry(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &storagePanicCompletionRepository{fakeQuestionAnswerRepository: base}
	repo.confirmationPanic.Store(true)
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, base, "sealed-save-panic")
	var asks atomic.Int32
	normal := s.questionAnswerHTTP.client.Transport
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) { asks.Add(1); return normal.RoundTrip(request) })
	panicRegressionDispatch(t, s)
	storageREDSettled(t, s, run)
	s.questionAnswerMu.Lock()
	held, pending, failed := run.heldSlots, len(run.pendingCompletions), run.finalErr
	s.questionAnswerMu.Unlock()
	if held != 1 || pending != 1 || failed == nil || asks.Load() != 1 {
		t.Fatalf("panic lost sealed result or held position: held=%d pending=%d error=%v asks=%d", held, pending, failed, asks.Load())
	}
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || batch.Active || len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].AnswerBody != "ok" || batch.Records[0].Diagnostics == nil || asks.Load() != 1 {
		t.Fatalf("explicit recovery after panic changed sealed answer: %+v err=%v asks=%d", batch, err, asks.Load())
	}
}

type storageCancelReadRepository struct {
	*fakeQuestionAnswerRepository
	readErr      error
	reads        atomic.Int32
	finalizes    atomic.Int32
	firstContext context.Context
	readScope    [3]string
}

func (r *storageCancelReadRepository) ListQuestionAnswerBatch(ctx context.Context, user, target, batch string) ([]QuestionAnswerRecord, error) {
	if r.reads.Add(1) == 1 {
		r.firstContext = ctx
		r.readScope = [3]string{user, target, batch}
	}
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.fakeQuestionAnswerRepository.ListQuestionAnswerBatch(ctx, user, target, batch)
}
func (r *storageCancelReadRepository) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	r.finalizes.Add(1)
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}
func storageCancelReadFixture(t *testing.T, withRecord bool) (*Service, *activeQuestionAnswerBatch, *storageCancelReadRepository, *atomic.Int32) {
	t.Helper()
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &storageCancelReadRepository{fakeQuestionAnswerRepository: base}
	s := panicRegressionService(t, repo)
	s.questionAnswerStorageTimeout = 40 * time.Millisecond
	const target = "sub2api:ws1:acc-1"
	const batch = "unknown-commit-cancel"
	if withRecord {
		if _, err := base.CreateQuestionAnswerBatch(context.Background(), "user1", target, batch, []string{"m"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(s.questionAnswerCtx)
	run := &activeQuestionAnswerBatch{userID: "user1", targetID: target, batchID: batch, ctx: ctx, cancel: cancel, stopReason: QuestionAnswerErrorStorage, finalErr: errors.New("previous final ending was not confirmed"), done: make(chan struct{})}
	s.questionAnswerMu.Lock()
	key := questionAnswerRunKey(run.userID, run.targetID)
	s.questionAnswerRuns[key] = run
	s.questionAnswerOrder = append(s.questionAnswerOrder, key)
	s.ensureQuestionAnswerFinalizationLocked(run)
	run.finalizeResults[run.finalizeGeneration] = run.finalErr
	close(run.finalizeSettled)
	s.questionAnswerMu.Unlock()
	asks := &atomic.Int32{}
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		asks.Add(1)
		return nil, errors.New("cancel must not send HTTP")
	})
	return s, run, repo, asks
}
func assertStorageCancelReadBounded(t *testing.T, repo *storageCancelReadRepository, run *activeQuestionAnswerBatch) {
	t.Helper()
	if repo.firstContext == nil || repo.readScope != [3]string{run.userID, run.targetID, run.batchID} {
		t.Fatal("cancel did not read exact owned batch")
	}
	if _, ok := repo.firstContext.Deadline(); !ok {
		t.Fatal("cancel presence read has no storage deadline")
	}
	select {
	case <-repo.firstContext.Done():
	default:
		t.Fatal("cancel presence read context was not released")
	}
}
func TestQuestionAnswerStorageCancelMissingRecordsKeepsShutdownOnlyReservation(t *testing.T) {
	s, run, repo, asks := storageCancelReadFixture(t, false)
	originalErr, generation := run.finalErr, run.finalizeGeneration
	_, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if !errors.Is(err, requestError(ErrorQuestionAnswerBatchNotFound)) {
		t.Fatalf("missing cancel error=%v", err)
	}
	assertStorageCancelReadBounded(t, repo, run)
	s.questionAnswerMu.Lock()
	retained := s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)]
	unchanged := run.finalErr == originalErr && run.finalizeGeneration == generation
	s.questionAnswerMu.Unlock()
	if retained != run || !unchanged || repo.finalizes.Load() != 0 || asks.Load() != 0 || repo.reads.Load() != 1 {
		t.Fatal("missing cancel reset/finalized/released its reservation or sent a request")
	}
	projection, current := s.projectQuestionAnswerFinalization(run.userID, run.targetID, run, false)
	if !current || projection == nil || projection.Recovery != "service_shutdown" {
		t.Fatal("missing record gained cancel recovery")
	}
}
func TestQuestionAnswerStorageCancelReadFailureDoesNotTreatReservationAsAbsent(t *testing.T) {
	s, run, repo, asks := storageCancelReadFixture(t, true)
	cause := errors.New("fixture cancellation presence read unavailable")
	repo.readErr = cause
	originalErr, generation := run.finalErr, run.finalizeGeneration
	_, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if !errors.Is(err, cause) || errors.Is(err, requestError(ErrorQuestionAnswerBatchNotFound)) {
		t.Fatalf("read failure became absence: %v", err)
	}
	assertStorageCancelReadBounded(t, repo, run)
	s.questionAnswerMu.Lock()
	retained := s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)]
	unchanged := run.finalErr == originalErr && run.finalizeGeneration == generation
	s.questionAnswerMu.Unlock()
	if retained != run || !unchanged || repo.finalizes.Load() != 0 || asks.Load() != 0 || repo.reads.Load() != 1 {
		t.Fatal("failed presence read changed retry ownership or sent/finalized a request")
	}
	records, _ := repo.fakeQuestionAnswerRepository.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
	if len(records) != 1 || records[0].Status != QuestionAnswerPending {
		t.Fatal("read failure ended an unconfirmed record")
	}
}
func TestQuestionAnswerStorageCancelUsesActualRecordsWhenLocalRunIsEmpty(t *testing.T) {
	s, run, repo, asks := storageCancelReadFixture(t, true)
	if len(run.records) != 0 {
		t.Fatal("fixture must model an unknown create reply")
	}
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || batch.Active || batch.BatchID != run.batchID || len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerFailed || batch.Records[0].ErrorType != QuestionAnswerErrorStorage {
		t.Fatalf("actual committed row could not recover via cancel: batch=%+v err=%v", batch, err)
	}
	assertStorageCancelReadBounded(t, repo, run)
	s.questionAnswerMu.Lock()
	retained := s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)]
	s.questionAnswerMu.Unlock()
	if retained != nil || repo.finalizes.Load() != 1 || asks.Load() != 0 || repo.reads.Load() != 2 {
		t.Fatal("actual committed batch was not ended once or cancel sent a request")
	}
	creates, _, _ := repo.createMetrics()
	if creates != 1 {
		t.Fatal("recovery recreated the batch")
	}
}

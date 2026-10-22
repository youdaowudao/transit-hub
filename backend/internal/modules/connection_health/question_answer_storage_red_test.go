package connection_health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type storageREDRepository struct {
	*fakeQuestionAnswerRepository
	writable         atomic.Bool
	markDeadline     atomic.Bool
	completeDeadline atomic.Bool
	earlyStops       atomic.Int32
	markFalse        bool
	markErr          error
	commitButError   bool
	commitLostOnce   atomic.Bool
	completeEntered  chan struct{}
	releaseComplete  chan struct{}
	completeOnce     atomic.Bool
}

func (r *storageREDRepository) MarkQuestionAnswerRunning(ctx context.Context, u, b, id string) (bool, error) {
	deadline, has := ctx.Deadline()
	r.markDeadline.Store(has && time.Until(deadline) > 0 && time.Until(deadline) <= 45*time.Millisecond)
	if r.markErr != nil {
		return false, r.markErr
	}
	if r.markFalse {
		return false, nil
	}
	return r.fakeQuestionAnswerRepository.MarkQuestionAnswerRunning(ctx, u, b, id)
}
func (r *storageREDRepository) CompleteQuestionAnswer(ctx context.Context, u, b, id string, c QuestionAnswerCompletion) (bool, error) {
	deadline, has := ctx.Deadline()
	r.completeDeadline.Store(has && time.Until(deadline) > 0 && time.Until(deadline) <= 45*time.Millisecond)
	if r.completeEntered != nil && r.completeOnce.CompareAndSwap(false, true) {
		close(r.completeEntered)
		<-r.releaseComplete
		return false, context.Canceled
	}
	if r.commitButError && r.commitLostOnce.CompareAndSwap(false, true) {
		_, err := r.fakeQuestionAnswerRepository.CompleteQuestionAnswer(ctx, u, b, id, c)
		if err != nil {
			return false, err
		}
		r.writable.Store(false)
		return false, errors.New("fixture saved but response lost")
	}
	if !r.writable.Load() {
		return false, errors.New("fixture storage write interrupted")
	}
	return r.fakeQuestionAnswerRepository.CompleteQuestionAnswer(ctx, u, b, id, c)
}
func (r *storageREDRepository) ConfirmOrSaveQuestionAnswerCompletion(ctx context.Context, u, target, b, id string, c QuestionAnswerCompletion, allow bool) (bool, error) {
	if !r.writable.Load() {
		return false, errors.New("fixture storage confirmation interrupted")
	}
	records, _ := r.fakeQuestionAnswerRepository.ListQuestionAnswerBatch(ctx, u, target, b)
	for _, record := range records {
		if record.ID == id && record.Status != QuestionAnswerPending && record.Status != QuestionAnswerRunning {
			return true, nil
		}
		if record.ID == id && record.Status == QuestionAnswerPending && allow && (c.Status == QuestionAnswerFailed || c.Status == QuestionAnswerCancelled) {
			f := r.fakeQuestionAnswerRepository
			f.mu.Lock()
			defer f.mu.Unlock()
			for i := range f.records {
				row := &f.records[i]
				if row.ID == id && row.TargetID == target && row.BatchID == b && row.Status == QuestionAnswerPending {
					now := time.Now()
					row.Status, row.AnswerBody, row.ErrorType = c.Status, c.AnswerBody, c.ErrorType
					row.AnswerJudgment, row.JudgmentSource = c.AnswerJudgment, c.JudgmentSource
					row.UpstreamStatus, row.UpstreamExcerpt = c.UpstreamStatus, c.UpstreamExcerpt
					row.Diagnostics = cloneQuestionAnswerDiagnostics(c.Diagnostics)
					row.CompletedAt, row.UpdatedAt = &now, now
					return true, nil
				}
			}
			return false, errors.New("fixture pending record not found")
		}
	}
	return r.fakeQuestionAnswerRepository.CompleteQuestionAnswer(ctx, u, b, id, c)
}
func (r *storageREDRepository) StopPendingQuestionAnswerBatch(ctx context.Context, u, target, b string, status QuestionAnswerStatus, reason string) (bool, error) {
	r.earlyStops.Add(1)
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, u, target, b, status, reason)
}
func (r *storageREDRepository) FinalizeQuestionAnswerBatch(ctx context.Context, u, target, b string, status QuestionAnswerStatus, reason string) (bool, error) {
	if !r.writable.Load() {
		return false, errors.New("fixture batch end unconfirmed")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, u, target, b, status, reason)
}
func storageREDStart(t *testing.T, r *storageREDRepository) (*Service, *activeQuestionAnswerBatch, *atomic.Int32) {
	t.Helper()
	s := panicRegressionService(t, r)
	s.questionAnswerStorageTimeout = 40 * time.Millisecond
	calls := &atomic.Int32{}
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"preserved complete answer"}}]}`))}, nil
	})
	run := panicRegressionStart(t, s, r.fakeQuestionAnswerRepository, "storage-red")
	// Keep the synthetic credential distinct from the injected error text so
	// credential masking does not hide the direct-cause assertion's fixture word.
	s.questionAnswerMu.Lock()
	run.cred.Key = "storage-red-synthetic-private-material"
	s.questionAnswerMu.Unlock()
	return s, run, calls
}
func storageREDSettled(t *testing.T, s *Service, run *activeQuestionAnswerBatch) {
	t.Helper()
	s.questionAnswerMu.Lock()
	settled := run.finalizeSettled
	s.questionAnswerMu.Unlock()
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("storage failure did not settle within its bounded attempt")
	}
}
func TestQuestionAnswerStorageREDBoundedNormalWritesAndNoEarlyBatchStop(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"})}
	s, run, calls := storageREDStart(t, repo)
	panicRegressionDispatch(t, s)
	storageREDSettled(t, s, run)
	if !repo.markDeadline.Load() || !repo.completeDeadline.Load() {
		t.Errorf("ordinary writes are unbounded: mark=%v complete=%v", repo.markDeadline.Load(), repo.completeDeadline.Load())
	}
	if repo.earlyStops.Load() != 0 {
		t.Errorf("selected records can be overwritten by an early batch stop: calls=%d", repo.earlyStops.Load())
	}
	if calls.Load() != 1 {
		t.Fatalf("Ask count=%d want1", calls.Load())
	}
}
func TestQuestionAnswerStorageREDPreservesAnswerOnRetryWithoutSendingAgain(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q", Keywords: []string{"complete"}})}
	s, run, calls := storageREDStart(t, repo)
	panicRegressionDispatch(t, s)
	storageREDSettled(t, s, run)
	repo.writable.Store(true)
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].AnswerBody != "preserved complete answer" || batch.Records[0].AnswerJudgment == nil || *batch.Records[0].AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("recovery lost the sealed answer and automatic judgment: %+v", batch.Records)
	}
	if calls.Load() != 1 {
		t.Fatalf("recovery sent the question again: Ask=%d", calls.Load())
	}
}
func TestQuestionAnswerStorageREDMarkFalseDoesNotReleaseUnconfirmedRecord(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"}), markFalse: true}
	s, run, calls := storageREDStart(t, repo)
	panicRegressionDispatch(t, s)
	s.questionAnswerMu.Lock()
	retained := s.questionAnswerRuns[questionAnswerRunKey(run.userID, run.targetID)] == run
	occupied := s.questionAnswerInFlight
	s.questionAnswerMu.Unlock()
	if !retained || occupied != 1 {
		t.Fatalf("unconfirmed pending record lost its execution reservation: retained=%v occupied=%d", retained, occupied)
	}
	if calls.Load() != 0 {
		t.Fatal("Mark false sent an HTTP request")
	}
	storageREDSettled(t, s, run)
}
func storageREDClose(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}
func storageREDWorkerCleanup(t *testing.T, release, done chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		storageREDClose(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("controlled test worker did not exit after cleanup released its barrier")
		}
	})
}

func TestQuestionAnswerStorageREDSealedResultWinsLaterCancel(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"}), completeEntered: make(chan struct{}), releaseComplete: make(chan struct{})}
	s, run, calls := storageREDStart(t, repo)
	done := make(chan struct{})
	storageREDWorkerCleanup(t, repo.releaseComplete, done)
	go func() { defer close(done); panicRegressionDispatch(t, s) }()
	select {
	case <-repo.completeEntered:
	case <-time.After(time.Second):
		t.Fatal("complete did not start")
	}
	// Ordinary save has been entered, so the single decision point must already
	// have sealed the HTTP result. A later cancellation may not rewrite it.
	s.questionAnswerMu.Lock()
	run.stopReason = string(QuestionAnswerCancelled)
	s.questionAnswerMu.Unlock()
	run.cancel()
	storageREDClose(repo.releaseComplete)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not stop")
	}
	storageREDSettled(t, s, run)
	repo.writable.Store(true)
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].AnswerBody != "preserved complete answer" {
		t.Fatalf("later cancellation overwrote an already sealed answer: %+v", batch.Records)
	}
	if calls.Load() != 1 {
		t.Fatal("cancel recovery issued another question")
	}
}

func TestQuestionAnswerStorageREDStopBeforeResultKeepsCancelled(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"})}
	repo.writable.Store(true)
	s, run, calls := storageREDStart(t, repo)
	entered, release := make(chan struct{}), make(chan struct{})
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"late complete answer"}}]}`))}, nil
	})
	done := make(chan struct{})
	storageREDWorkerCleanup(t, release, done)
	go func() { defer close(done); panicRegressionDispatch(t, s) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("HTTP did not start")
	}
	s.questionAnswerMu.Lock()
	run.stopReason = string(QuestionAnswerCancelled)
	s.questionAnswerMu.Unlock()
	run.cancel()
	storageREDClose(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not finish")
	}
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled batch did not settle")
	}
	records, err := repo.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerCancelled || records[0].AnswerBody != "" || records[0].AnswerJudgment != nil {
		t.Fatalf("stop-first was rewritten by a late answer: %+v err=%v", records, err)
	}
	if calls.Load() != 1 {
		t.Fatal("stop-first caused another HTTP request")
	}
}

func TestQuestionAnswerStorageREDStartingFailureRetainsDirectCauseWithoutHTTP(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"}), markErr: errors.New("fixture statement timeout marking start")}
	s, run, calls := storageREDStart(t, repo)
	panicRegressionDispatch(t, s)
	storageREDSettled(t, s, run)
	if calls.Load() != 0 {
		t.Fatal("a failed start save called the answer API")
	}
	repo.writable.Store(true)
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 {
		t.Fatal("start failure record disappeared")
	}
	r := batch.Records[0]
	if r.Status != QuestionAnswerFailed || r.ErrorType != QuestionAnswerErrorStorage || r.Diagnostics == nil || r.Diagnostics.FailureStep != "starting" || !strings.Contains(r.Diagnostics.LocalError, "fixture statement timeout marking start") || r.Diagnostics.RequestStartedAt != nil || r.AnswerBody != "" || r.AnswerJudgment != nil {
		t.Fatalf("start failure lost its actual cause or gained an HTTP result: %+v", r)
	}
	if repo.earlyStops.Load() != 0 {
		t.Fatal("start failure used an early batch stop")
	}
}

func TestQuestionAnswerStorageREDCommittedResultAndManualJudgmentRemainUntouched(t *testing.T) {
	repo := &storageREDRepository{fakeQuestionAnswerRepository: newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "q"}), commitButError: true}
	s, run, calls := storageREDStart(t, repo)
	panicRegressionDispatch(t, s)
	storageREDSettled(t, s, run)
	rs, err := repo.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || len(rs) != 1 || rs[0].Status != QuestionAnswerSucceeded {
		t.Fatalf("fixture result did not commit: records=%+v err=%v", rs, err)
	}
	manual, err := repo.SetQuestionAnswerJudgment(context.Background(), run.userID, run.targetID, rs[0].ID, QuestionAnswerIncorrect, rs[0].UpdatedAt)
	if err != nil || manual == nil {
		t.Fatalf("manual judgment fixture failed: %v", err)
	}
	repo.writable.Store(true)
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Records) != 1 || !reflect.DeepEqual(batch.Records[0], *manual) {
		t.Fatal("confirming an already committed result changed answer, diagnostics, manual judgment or timestamps")
	}
	if calls.Load() != 1 {
		t.Fatal("confirming an already committed result called Ask again")
	}
}

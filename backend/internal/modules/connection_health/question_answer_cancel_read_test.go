package connection_health

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuestionAnswerCancelReadFailureLeavesInFlightWorkerInControl(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &storageCancelReadRepository{fakeQuestionAnswerRepository: base}
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, base, "cancel-read-in-flight")
	transport := newCancellationHoldingRoundTripper(1)
	var asks atomic.Int32
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) { asks.Add(1); return transport.RoundTrip(request) })
	s.questionAnswerMu.Lock()
	key, selected, record, ok := s.nextQuestionAnswerDispatchLocked()
	s.questionAnswerMu.Unlock()
	if !ok || selected != run {
		t.Fatal("fixture did not select its worker")
	}
	finished := make(chan struct{})
	go func() { defer close(finished); s.executeQuestionAnswerDispatch(key, run, record) }()
	var release sync.Once
	allowCancelled := func() { release.Do(func() { transport.allowCancelled <- struct{}{} }) }
	defer func() {
		run.cancel()
		allowCancelled()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("cancelled worker was not collected")
		}
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cause := errors.New("cancel presence read interrupted during HTTP")
	repo.readErr = cause
	_, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "读取批次记录时失败") {
		t.Fatalf("read error lost its step or direct cause: %v", err)
	}
	s.questionAnswerMu.Lock()
	untouched := run.finalErr == nil && !run.finalizing && run.inFlight == 1 && !questionAnswerChannelClosed(run.finalizeSettled) && len(run.finalizeResults) == 0
	s.questionAnswerMu.Unlock()
	if !untouched || repo.finalizes.Load() != 0 {
		t.Fatal("presence read failure settled an unfinished worker's attempt")
	}
	select {
	case <-transport.cancellationObserved:
	case <-time.After(time.Second):
		t.Fatal("read failure delayed HTTP cancellation")
	}
	repo.readErr = nil
	allowCancelled()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("worker's finalizer did not finish")
	}
	s.questionAnswerMu.Lock()
	finalErr := run.finalErr
	released := s.questionAnswerRuns[key] == nil
	s.questionAnswerMu.Unlock()
	records, _ := base.ListQuestionAnswerBatch(context.Background(), run.userID, run.targetID, run.batchID)
	if finalErr != nil || !released || repo.finalizes.Load() != 1 || asks.Load() != 1 || len(records) != 1 || records[0].Status != QuestionAnswerCancelled {
		t.Fatalf("worker lost its actual cancelled result: error=%v released=%v records=%+v", finalErr, released, records)
	}
}

type cancelReadFinalizerRepository struct {
	*storageCancelReadRepository
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	endingErr error
}

func (r *cancelReadFinalizerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	if r.endingErr != nil {
		r.finalizes.Add(1)
		return false, r.endingErr
	}
	return r.storageCancelReadRepository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}
func TestQuestionAnswerCancelReadFailureDoesNotSettleExistingFinalizer(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	original := errors.New("actual batch ending failed")
	repo := &cancelReadFinalizerRepository{storageCancelReadRepository: &storageCancelReadRepository{fakeQuestionAnswerRepository: base}, entered: make(chan struct{}), release: make(chan struct{}), endingErr: original}
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, base, "cancel-read-finalizing")
	var asks atomic.Int32
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) { asks.Add(1); return nil, errors.New("must not send") })
	s.questionAnswerMu.Lock()
	key := questionAnswerRunKey(run.userID, run.targetID)
	run.stopReason = string(QuestionAnswerCancelled)
	run.cancel()
	started := s.beginQuestionAnswerRunFinalizationLocked(key, run)
	generation, settled := run.finalizeGeneration, run.finalizeSettled
	s.questionAnswerMu.Unlock()
	if !started {
		t.Fatal("fixture did not start finalizer")
	}
	result := make(chan error, 1)
	go func() { result <- s.finalizeQuestionAnswerRun(key, run) }()
	var release sync.Once
	unblock := func() { release.Do(func() { close(repo.release) }) }
	defer unblock()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("finalizer did not enter storage")
	}
	cause := errors.New("presence read failed while batch ending was active")
	repo.readErr = cause
	_, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if !errors.Is(err, cause) {
		t.Fatalf("cancel read cause lost: %v", err)
	}
	s.questionAnswerMu.Lock()
	untouched := run.finalizing && run.finalErr == nil && run.finalizeGeneration == generation && run.finalizeSettled == settled && !questionAnswerChannelClosed(settled) && len(run.finalizeResults) == 0
	s.questionAnswerMu.Unlock()
	if !untouched {
		t.Fatal("presence read failure stole the existing finalizer's result")
	}
	unblock()
	select {
	case err := <-result:
		if !errors.Is(err, original) {
			t.Fatalf("actual finalizer result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("actual finalizer did not settle")
	}
	s.questionAnswerMu.Lock()
	actual := run.finalErr
	stored := run.finalizeResults[generation]
	s.questionAnswerMu.Unlock()
	if !errors.Is(actual, original) || !errors.Is(stored, original) || errors.Is(actual, cause) {
		t.Fatal("cancel read replaced the actual batch-ending failure")
	}
	repo.readErr = nil
	repo.endingErr = nil
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || batch.Active || asks.Load() != 0 || repo.finalizes.Load() != 2 {
		t.Fatalf("actual finalizer failure could not recover once: active=%v err=%v endings=%d", batch.Active, err, repo.finalizes.Load())
	}
}
func TestQuestionAnswerCancelReadFailurePreservesPreviouslySettledError(t *testing.T) {
	s, run, repo, asks := storageCancelReadFixture(t, true)
	original, generation, settled := run.finalErr, run.finalizeGeneration, run.finalizeSettled
	cause := errors.New("later cancellation read failed")
	repo.readErr = cause
	_, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "读取批次记录时失败") {
		t.Fatalf("cancel read returned wrong cause: %v", err)
	}
	s.questionAnswerMu.Lock()
	unchanged := run.finalErr == original && run.finalizeResults[generation] == original && run.finalizeGeneration == generation && run.finalizeSettled == settled && questionAnswerChannelClosed(settled)
	s.questionAnswerMu.Unlock()
	if !unchanged || repo.finalizes.Load() != 0 || asks.Load() != 0 {
		t.Fatal("cancel read replaced or retried an already settled result")
	}
	repo.readErr = nil
	batch, err := s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
	if err != nil || batch.Active || repo.finalizes.Load() != 1 || asks.Load() != 0 {
		t.Fatalf("original failure was not available for explicit recovery: %+v err=%v", batch, err)
	}
}

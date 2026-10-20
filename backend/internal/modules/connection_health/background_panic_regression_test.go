package connection_health

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// This fixture deliberately leaves the dispatcher stopped. Each dispatch is
// explicit and all HTTP runs through an in-memory RoundTripper.
func panicRegressionService(t *testing.T, repo questionAnswerRepository) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Service{questionAnswers: repo, questionAnswerCtx: ctx, questionAnswerStop: cancel, schedulableActions: forbiddenAutomaticSchedulingWriter{t},
		questionAnswerRuns: map[string]*activeQuestionAnswerBatch{}, questionAnswerStarts: map[string]*questionAnswerTargetStart{},
		questionAnswerWake: make(chan struct{}, 1), questionAnswerTTL: time.Second, questionAnswerStorageTimeout: time.Second,
		questionAnswerHTTP: &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
		})}},
	}
}

func panicRegressionCall(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() != nil {
			t.Error("panic escaped the workflow instead of becoming its terminal failure")
		}
	}()
	fn()
}

func panicRegressionStart(t *testing.T, s *Service, repo *fakeQuestionAnswerRepository, batch string) *activeQuestionAnswerBatch {
	t.Helper()
	target := "sub2api:ws1:acc-1"
	reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", target, batch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseQuestionAnswerTargetStart(reservation)
	_, err = s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "fixture"}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
		records, err := repo.CreateQuestionAnswerBatch(ctx, "user1", target, batch, []string{"model-a"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1)
		return records, true, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.questionAnswerRuns[questionAnswerRunKey("user1", target)]
}

func panicRegressionDispatch(t *testing.T, s *Service) {
	t.Helper()
	s.questionAnswerMu.Lock()
	key, run, record, ok := s.nextQuestionAnswerDispatchLocked()
	s.questionAnswerMu.Unlock()
	if !ok {
		t.Fatal("next batch could not dispatch")
	}
	panicRegressionCall(t, func() { s.executeQuestionAnswerDispatch(key, run, record) })
}

func TestQuestionAnswerDispatchPanicStopsBatch(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, repo)
	var calls atomic.Int32
	normal := s.questionAnswerHTTP.client.Transport
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			panic("injected HTTP panic")
		}
		return normal.RoundTrip(r)
	})
	run := panicRegressionStart(t, s, repo, "panic-batch")
	panicRegressionDispatch(t, s)
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("batch did not stop after panic")
	}
	records, _ := repo.ListQuestionAnswerBatch(context.Background(), "user1", run.targetID, run.batchID)
	if len(records) != 1 || records[0].Status != QuestionAnswerFailed || records[0].ErrorType != "internal_error" {
		t.Fatalf("terminal records=%+v", records)
	}
	s.questionAnswerMu.Lock()
	count := s.questionAnswerInFlight
	s.questionAnswerMu.Unlock()
	if count != 0 {
		t.Fatalf("inFlight=%d", count)
	}
	next := panicRegressionStart(t, s, repo, "after-panic")
	panicRegressionDispatch(t, s)
	select {
	case <-next.done:
	case <-time.After(time.Second):
		t.Fatal("account remained busy after panic")
	}
	records, _ = repo.ListQuestionAnswerBatch(context.Background(), "user1", next.targetID, next.batchID)
	if len(records) != 1 || records[0].Status != QuestionAnswerSucceeded {
		t.Fatalf("new batch did not succeed: %+v", records)
	}
}

type panicFinalizeRepository struct {
	*fakeQuestionAnswerRepository
	panicOnce atomic.Bool
}

func (r *panicFinalizeRepository) FinalizeQuestionAnswerBatch(ctx context.Context, u, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	if r.panicOnce.CompareAndSwap(true, false) {
		panic("injected finalizer panic")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, u, target, batch, status, reason)
}
func TestQuestionAnswerFinalizePanicRecoversViaCancel(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	repo := &panicFinalizeRepository{fakeQuestionAnswerRepository: base}
	repo.panicOnce.Store(true)
	s := panicRegressionService(t, repo)
	run := panicRegressionStart(t, s, base, "finalize-panic")
	key := questionAnswerRunKey("user1", run.targetID)
	s.questionAnswerMu.Lock()
	run.stopReason = QuestionAnswerErrorStorage
	s.beginQuestionAnswerRunFinalizationLocked(key, run)
	s.questionAnswerMu.Unlock()
	panicRegressionCall(t, func() {
		if err := s.finalizeQuestionAnswerRun(key, run); err == nil {
			t.Error("finalizer panic returned no error")
		}
	})
	select {
	case <-run.finalizeSettled:
	case <-time.After(time.Second):
		t.Fatal("finalization never settled")
	}
	projection, current := s.projectQuestionAnswerFinalization("user1", run.targetID, run, true)
	if !current || projection == nil || projection.State != "failed" || projection.Recovery != "cancel" {
		t.Fatalf("projection=%+v current=%v", projection, current)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch, err := s.stopQuestionAnswerBatchCore(ctx, "user1", run.targetID, run.batchID)
	if err != nil || batch.Active {
		t.Fatalf("cancel retry batch=%+v err=%v", batch, err)
	}
	next := panicRegressionStart(t, s, base, "after-finalize-panic")
	panicRegressionDispatch(t, s)
	select {
	case <-next.done:
	case <-ctx.Done():
		t.Fatal("account busy after successful finalizer retry")
	}
}

func TestQuestionAnswerStartCorePanicReleasesAccount(t *testing.T) {
	for _, entry := range []string{"manual", "scheduled"} {
		t.Run(entry, func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			s := panicRegressionService(t, base)
			target := "sub2api:ws1:acc-1"
			reservation, err := s.reserveQuestionAnswerTargetStart(nil, "user1", target, "start-panic")
			if err != nil {
				t.Fatal(err)
			}
			defer s.releaseQuestionAnswerTargetStart(reservation)
			panicRegressionCall(t, func() {
				_, err = s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
					_, _ = base.CreateQuestionAnswerBatch(ctx, "user1", target, "start-panic", []string{"model-a"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, 1)
					panic("injected create-result uncertainty")
				})
				if err == nil || !strings.Contains(err.Error(), "internal") {
					t.Errorf("start error=%v", err)
				}
			})
			records, _ := base.ListQuestionAnswerBatch(context.Background(), "user1", target, "start-panic")
			if len(records) != 1 || records[0].Status != QuestionAnswerFailed || records[0].ErrorType != "internal_error" {
				t.Fatalf("uncertain start records=%+v", records)
			}
			if s.questionAnswerRuns[questionAnswerRunKey("user1", target)] != nil {
				t.Fatal("panicked start still owns the account")
			}
			next := panicRegressionStart(t, s, base, "after-start-panic")
			panicRegressionDispatch(t, s)
			select {
			case <-next.done:
			case <-time.After(time.Second):
				t.Fatal("next batch did not complete")
			}
		})
	}
}

type panicShutdownRepository struct{ *fakeQuestionAnswerRepository }

func (r *panicShutdownRepository) StopPendingQuestionAnswerBatch(context.Context, string, string, string, QuestionAnswerStatus, string) (bool, error) {
	panic("injected shutdown storage panic")
}
func TestQuestionAnswerShutdownPanicCompletes(t *testing.T) {
	base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
	s := panicRegressionService(t, base)
	run := panicRegressionStart(t, s, base, "shutdown-panic")
	s.questionAnswers = &panicShutdownRepository{base}
	attempt := &questionAnswerShutdownAttempt{done: make(chan struct{})}
	panicRegressionCall(t, func() {
		s.runQuestionAnswerShutdown(attempt, []questionAnswerShutdownRun{{key: questionAnswerRunKey("user1", run.targetID), run: run}}, nil)
	})
	select {
	case <-attempt.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown attempt never closed")
	}
	if attempt.err == nil {
		t.Fatal("shutdown panic lost its error")
	}
}

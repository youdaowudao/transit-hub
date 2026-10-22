package connection_health

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuestionAnswerCancelReadREDFirstQueuedCancelReadFailureRemainsRecoverable(t *testing.T) {
	for _, example := range []struct {
		name  string
		cause error
	}{
		{"temporary_read_failure", errors.New("batch lookup temporarily unavailable")},
		{"caller_deadline", context.DeadlineExceeded},
		{"caller_cancelled", context.Canceled},
	} {
		t.Run(example.name, func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Enabled: true, Body: "question"})
			repo := &storageCancelReadRepository{fakeQuestionAnswerRepository: base}
			s := panicRegressionService(t, repo)
			s.questionAnswerStorageTimeout = 40 * time.Millisecond
			// Other accounts hold every position. This batch has not selected a
			// record and therefore has no worker that can finish its first stop.
			s.questionAnswerInFlight = questionAnswerConcurrency
			run := panicRegressionStart(t, s, base, "first-queued-cancel-read")
			var asks atomic.Int32
			s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
				asks.Add(1)
				return nil, errors.New("cancel recovery must not send a question")
			})
			if _, _, _, ok := s.nextQuestionAnswerDispatch(); ok || run.next != 0 || run.inFlight != 0 || run.finalErr != nil {
				t.Fatal("fixture must be an unselected batch before its first stop")
			}
			repo.readErr = example.cause
			caller, cancel := context.WithCancel(context.Background())
			if errors.Is(example.cause, context.Canceled) {
				cancel()
			}
			if errors.Is(example.cause, context.DeadlineExceeded) {
				cancel()
				caller, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			_, err := s.stopQuestionAnswerBatchCore(caller, run.userID, run.targetID, run.batchID)
			cancel()
			if !errors.Is(err, example.cause) {
				t.Fatalf("cancel lost its known read failure: %v", err)
			}
			assertStorageCancelReadBounded(t, repo, run)
			repo.readErr = nil
			s.questionAnswerMu.Lock()
			s.questionAnswerInFlight = 0
			s.questionAnswerMu.Unlock()
			if _, _, _, ok := s.nextQuestionAnswerDispatch(); ok || asks.Load() != 0 {
				t.Fatal("a failed cancellation resumed dispatch")
			}
			batch, err := s.getQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
			if err != nil || batch.Finalization == nil || batch.Finalization.State != "failed" || batch.Finalization.Recovery != "cancel" {
				t.Fatalf("first cancel read failure stranded the batch without page recovery: finalization=%+v err=%v", batch.Finalization, err)
			}
			if len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerPending || repo.finalizes.Load() != 0 {
				t.Fatal("failed presence read must not end unconfirmed records")
			}
			batch, err = s.stopQuestionAnswerBatchCore(context.Background(), run.userID, run.targetID, run.batchID)
			if err != nil || batch.Active || batch.Finalization != nil || len(batch.Records) != 1 || batch.Records[0].Status != QuestionAnswerCancelled {
				t.Fatalf("explicit retry did not end the original batch: batch=%+v err=%v", batch, err)
			}
			creates, _, _ := base.createMetrics()
			if asks.Load() != 0 || creates != 1 || repo.finalizes.Load() != 1 {
				t.Fatal("cancel recovery repeated a question, recreated a batch, or ended it more than once")
			}
		})
	}
}

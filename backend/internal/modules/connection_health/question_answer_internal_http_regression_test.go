package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

// Exercise the public response after both uncertain-create outcomes. The
// sentinel must remain available to scheduled callers while HTTP explains it.
type internalHTTPQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository
	persistBeforePanic bool
	panicked           atomic.Bool
	finalizeFailure    string
	finalizeFails      atomic.Bool
}

func (r *internalHTTPQuestionAnswerRepository) CreateQuestionAnswerBatch(ctx context.Context, user, target, batch string, models, questions []string, effort QuestionAnswerReasoningEffort, repeat int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error) {
	if r.panicked.CompareAndSwap(false, true) {
		if r.persistBeforePanic {
			if _, err := r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, user, target, batch, models, questions, effort, repeat, snapshots...); err != nil {
				return nil, err
			}
		}
		panic("injected internal create failure must not reach the client")
	}
	return r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, user, target, batch, models, questions, effort, repeat, snapshots...)
}

func (r *internalHTTPQuestionAnswerRepository) FinalizeQuestionAnswerBatch(ctx context.Context, user, target, batch string, status QuestionAnswerStatus, reason string) (bool, error) {
	if r.finalizeFails.CompareAndSwap(true, false) {
		if r.finalizeFailure == "panic" {
			panic("injected finalization panic")
		}
		return false, errors.New("isolated finalization storage failure")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, user, target, batch, status, reason)
}

func internalHTTPQuestionAnswerRequest(mux *http.ServeMux, method, suffix, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/connection-health/targets/sub2api:ws1:a/question-answers/batches"+suffix, strings.NewReader(body))
	r = r.WithContext(authctx.WithUserID(r.Context(), "user1"))
	r.Header.Set(questionAnswerContractHeader, "3")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestQuestionAnswerInternalPanicHTTPExplainsAndAllowsRetry(t *testing.T) {
	for _, persisted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-records", true: "after-records"}[persisted], func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"})
			s := panicPathsScheduleService(t, nil)
			s.questionAnswers = &internalHTTPQuestionAnswerRepository{fakeQuestionAnswerRepository: base, persistBeforePanic: persisted}
			mux := http.NewServeMux()
			RegisterRoutes(mux, s)
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/connection-health/targets/sub2api:ws1:a/question-answers/batches", strings.NewReader(`{"models":["available"],"questionIds":["question"],"repeatCount":1}`))
				r = r.WithContext(authctx.WithUserID(r.Context(), "user1"))
				r.Header.Set(questionAnswerContractHeader, "3")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			failed := request()
			if failed.Code != http.StatusInternalServerError {
				t.Errorf("internal failure status=%d body=%s", failed.Code, failed.Body.String())
			}
			var failure struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(failed.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Message != "admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.internal_error" {
				t.Errorf("known stopped-batch reason was lost at HTTP boundary: message=%q", failure.Message)
			}
			if strings.Contains(failed.Body.String(), "injected") || strings.Contains(failed.Body.String(), ErrorUnknown) {
				t.Error("HTTP response leaked panic text or replaced the cause with generic data-read failure")
			}
			base.mu.Lock()
			wantRecords := 0
			if persisted {
				wantRecords = 1
			}
			if len(base.records) != wantRecords {
				t.Errorf("uncertain-create records=%d want=%d", len(base.records), wantRecords)
			}
			for _, record := range base.records {
				if record.Status != QuestionAnswerFailed || record.ErrorType != QuestionAnswerErrorInternal {
					t.Errorf("created record did not stop: %+v", record)
				}
			}
			base.mu.Unlock()
			s.questionAnswerMu.Lock()
			if len(s.questionAnswerRuns) != 0 || len(s.questionAnswerStarts) != 0 || s.questionAnswerInFlight != 0 {
				t.Error("internal HTTP failure retained account admission or in-flight state")
			}
			s.questionAnswerMu.Unlock()
			retry := request()
			if retry.Code != http.StatusAccepted {
				t.Fatalf("same account cannot retry: status=%d body=%s", retry.Code, retry.Body.String())
			}
			var batch QuestionAnswerBatch
			if err := json.Unmarshal(retry.Body.Bytes(), &batch); err != nil {
				t.Fatal(err)
			}
			panicRegressionDispatch(t, s)
			records, err := base.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", batch.BatchID)
			if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerSucceeded {
				t.Fatalf("HTTP retry did not complete: records=%+v err=%v", records, err)
			}
		})
	}
}

func TestQuestionAnswerInternalPanicHTTPKeepsFailedFinalizationRecovery(t *testing.T) {
	for _, failure := range []string{"error", "panic"} {
		t.Run(failure, func(t *testing.T) {
			base := newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Enabled: true, Body: "question"})
			repo := &internalHTTPQuestionAnswerRepository{fakeQuestionAnswerRepository: base, persistBeforePanic: true, finalizeFailure: failure}
			repo.finalizeFails.Store(true)
			s := panicPathsScheduleService(t, nil)
			s.questionAnswers = repo
			mux := http.NewServeMux()
			RegisterRoutes(mux, s)
			body := `{"models":["available"],"questionIds":["question"],"repeatCount":1}`
			failed := internalHTTPQuestionAnswerRequest(mux, http.MethodPost, "", body)
			if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), "manualProbeDialog.questionAnswer.errorTypes.internal_error") {
				t.Errorf("unfinished finalization must not promise a completed batch or immediate new start: status=%d body=%s", failed.Code, failed.Body.String())
			}
			latest := internalHTTPQuestionAnswerRequest(mux, http.MethodGet, "/latest", "")
			var batch QuestionAnswerBatch
			if err := json.Unmarshal(latest.Body.Bytes(), &batch); err != nil {
				t.Fatal(err)
			}
			if latest.Code != http.StatusOK || batch.Finalization == nil || batch.Finalization.State != "failed" || batch.Finalization.Recovery != "cancel" {
				t.Fatalf("existing failed/cancel recovery was lost: status=%d batch=%+v", latest.Code, batch)
			}
			cancelled := internalHTTPQuestionAnswerRequest(mux, http.MethodPost, "/"+batch.BatchID+"/cancel", "")
			if cancelled.Code != http.StatusOK {
				t.Fatalf("existing recovery cannot retry finalization: status=%d body=%s", cancelled.Code, cancelled.Body.String())
			}
			retry := internalHTTPQuestionAnswerRequest(mux, http.MethodPost, "", body)
			if retry.Code != http.StatusAccepted {
				t.Fatalf("completed recovery retained admission: status=%d body=%s", retry.Code, retry.Body.String())
			}
			if err := json.Unmarshal(retry.Body.Bytes(), &batch); err != nil {
				t.Fatal(err)
			}
			panicRegressionDispatch(t, s)
			records, err := base.ListQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:a", batch.BatchID)
			if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerSucceeded {
				t.Fatalf("start after recovery did not finish: records=%+v err=%v", records, err)
			}
		})
	}
}

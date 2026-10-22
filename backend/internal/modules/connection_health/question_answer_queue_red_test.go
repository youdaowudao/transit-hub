package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func queueREDStart(t *testing.T, s *Service, repo *fakeQuestionAnswerRepository, user, target, batch string, count int) *activeQuestionAnswerBatch {
	t.Helper()
	reservation, err := s.reserveQuestionAnswerTargetStart(nil, user, target, batch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseQuestionAnswerTargetStart(reservation)
	_, err = s.startQuestionAnswerBatchCore(reservation, upstream.ProbeCredential{BaseURL: "https://unit.invalid", Key: "synthetic-fixture"}, func(ctx context.Context) ([]QuestionAnswerRecord, bool, error) {
		rs, e := repo.CreateQuestionAnswerBatch(ctx, user, target, batch, []string{"model-a"}, []string{"q"}, QuestionAnswerReasoningEffortMedium, count)
		return rs, true, e
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.questionAnswerRuns[questionAnswerRunKey(user, target)]
}
func queueREDRead(t *testing.T, mux *http.ServeMux, workspace string) (int, map[string]any, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/connection-health/question-answer-queue?workspace="+workspace, nil)
	r = r.WithContext(authctx.WithUserID(r.Context(), "user1"))
	r.Header.Set(questionAnswerContractHeader, "3")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	result := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	return w.Code, result, w.Body.String()
}
func TestQuestionAnswerQueueREDSnapshotMatchesRealSelectionAndKeepsIdentityPrivate(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "q", Enabled: true})
	s := panicRegressionService(t, repo)
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	a := queueREDStart(t, s, repo, "user1", "sub2api:ws1:a", "queue-a", 3)
	b := queueREDStart(t, s, repo, "user1", "sub2api:ws1:b", "queue-b", 3)
	queueREDStart(t, s, repo, "foreign-user", "sub2api:ws1:foreign", "DO-NOT-EXPOSE-USER", 2)
	queueREDStart(t, s, repo, "user1", "sub2api:ws2:foreign", "DO-NOT-EXPOSE-WORKSPACE", 2)
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	code, snapshot, raw := queueREDRead(t, mux, "ws1")
	if code != 200 {
		t.Fatalf("real queue is unavailable: HTTP%d", code)
	}
	if strings.Contains(raw, "DO-NOT-EXPOSE") || strings.Contains(raw, "synthetic-fixture") {
		t.Fatal("queue leaked another scope or credentials")
	}
	queued, _ := snapshot["queued"].([]any)
	got := []string{}
	for _, entry := range queued {
		row := entry.(map[string]any)
		got = append(got, row["recordId"].(string))
	}
	if a.next != 0 || b.next != 0 || s.questionAnswerInFlight != 0 {
		t.Fatal("preview changed live dispatch cursors")
	}
	want := []string{}
	for {
		s.questionAnswerMu.Lock()
		_, run, record, ok := s.nextQuestionAnswerDispatchLocked()
		s.questionAnswerMu.Unlock()
		if !ok {
			break
		}
		if run.userID == "user1" && strings.HasPrefix(run.targetID, "sub2api:ws1:") {
			want = append(want, record.ID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("predicted choice differs from actual account rotation: got=%v want=%v", got, want)
	}
}
func TestQuestionAnswerQueueREDSelectedBeforeMarkIsProcessingAndDefaultCapacityIs15(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "q", Enabled: true})
	s := panicRegressionService(t, repo)
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:a", "queue-a", 10)
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:b", "queue-b", 10)
	for i := 0; i < 15; i++ {
		s.questionAnswerMu.Lock()
		_, _, _, ok := s.nextQuestionAnswerDispatchLocked()
		s.questionAnswerMu.Unlock()
		if !ok {
			t.Fatalf("default dispatcher stopped at %d/15", i)
		}
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	code, snapshot, _ := queueREDRead(t, mux, "ws1")
	if code != 200 {
		t.Fatalf("queue HTTP%d", code)
	}
	queued, _ := snapshot["queued"].([]any)
	processing, _ := snapshot["processing"].([]any)
	if snapshot["capacity"] != float64(15) || snapshot["availableSlots"] != float64(0) || len(queued) != 5 || len(processing) != 15 {
		t.Fatalf("selected pending rows are still queued or capacity changed: capacity=%v available=%v queued=%d processing=%d", snapshot["capacity"], snapshot["availableSlots"], len(queued), len(processing))
	}
}
func TestQuestionAnswerQueueREDRejectsChangedWorkspace(t *testing.T) {
	s := panicRegressionService(t, newFakeQuestionAnswerRepository())
	s.accounts = fakeAdminAccountResolver{id: "ws1"}
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	code, body, _ := queueREDRead(t, mux, "ws2")
	if code != 409 || body["detail"] == nil {
		t.Fatalf("workspace changed without a useful409 response: status=%d detail=%v", code, body["detail"])
	}
}

func TestQuestionAnswerQueueREDDefaultWindowRefillsBeforeOtherPositionsFinish(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "q", Enabled: true})
	s := panicRegressionService(t, repo)
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:a", "rolling-a", 10)
	queueREDStart(t, s, repo, "user1", "sub2api:ws1:b", "rolling-b", 10)
	type selected struct {
		key    string
		run    *activeQuestionAnswerBatch
		record QuestionAnswerRecord
	}
	choices := []selected{}
	for i := 0; i < 15; i++ {
		s.questionAnswerMu.Lock()
		key, run, record, ok := s.nextQuestionAnswerDispatchLocked()
		s.questionAnswerMu.Unlock()
		if !ok {
			t.Fatalf("window stopped at%d", i)
		}
		choices = append(choices, selected{key, run, record})
	}
	asks := 0
	s.questionAnswerHTTP.client.Transport = c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		asks++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"rolling answer"}}]}`))}, nil
	})
	first := choices[0]
	s.executeQuestionAnswerDispatch(first.key, first.run, first.record)
	s.questionAnswerMu.Lock()
	occupied := s.questionAnswerInFlight
	key, run, record, ok := s.nextQuestionAnswerDispatchLocked()
	s.questionAnswerMu.Unlock()
	if occupied != 14 || !ok {
		t.Fatalf("first completion did not make exactly one slot available while14 remain: occupied=%d next=%v", occupied, ok)
	}
	for _, old := range choices {
		if old.record.ID == record.ID {
			t.Fatal("refill reselected an already occupied record")
		}
	}
	s.executeQuestionAnswerDispatch(key, run, record)
	if asks != 2 {
		t.Fatalf("refill did not send the next question: Ask=%d", asks)
	}
	s.questionAnswerMu.Lock()
	remaining := s.questionAnswerInFlight
	s.questionAnswerMu.Unlock()
	if remaining != 14 {
		t.Fatalf("other14 positions were not preserved: %d", remaining)
	}
}

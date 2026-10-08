package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

func TestC2QuestionAnswerSameTargetReservesBeforeModelDiscovery(t *testing.T) {
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "frozen question", Enabled: true})
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", repo, newFakeRepository())
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.ShutdownQuestionAnswers(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"choices":[{"message":{"content":"answer"}}]}`
		if req.URL.Path == "/v1/models" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			body = `{"data":[{"id":"model-a"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	service.modelDiscovery.client.Transport = transport
	service.questionAnswerHTTP.client.Transport = transport
	const target = "sub2api:ws1:acc-a"
	input := QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}}
	first := make(chan error, 1)
	go func() {
		_, err := service.StartQuestionAnswerBatch(context.Background(), "user1", target, input)
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first discovery did not start")
	}
	second := make(chan error, 1)
	go func() {
		_, err := service.StartQuestionAnswerBatch(context.Background(), "user1", target, input)
		second <- err
	}()
	var secondErr error
	var secondReceived bool
	select {
	case secondErr = <-second:
		secondReceived = true
	case <-entered:
		t.Error("competing same-target start reached models before the accepted preparation exited")
	case <-time.After(2 * time.Second):
		t.Error("competing start must return active without waiting for model discovery")
	}
	close(release)
	closed = true
	if !secondReceived {
		select {
		case secondErr = <-second:
		case <-time.After(2 * time.Second):
			t.Fatal("second start leaked")
		}
	}
	if !errors.Is(secondErr, requestError(ErrorQuestionAnswerActive)) {
		t.Errorf("competing start got=%v want active", secondErr)
	}
	select {
	case err := <-first:
		if err != nil {
			t.Errorf("accepted start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accepted start leaked")
	}
}

// These feature-boundary assertions are authored before C2 implementation.
// They use the actual dispatcher and HTTP routes, and require no test listener.
func TestC2QuestionAnswerDefaultSharedSlotsAdmitFifteenThenWait(t *testing.T) {
	ctx := context.Background()
	run := &activeQuestionAnswerBatch{ctx: ctx, records: make([]QuestionAnswerRecord, 16)}
	service := &Service{questionAnswerCtx: ctx, questionAnswerRuns: map[string]*activeQuestionAnswerBatch{"one": run}, questionAnswerOrder: []string{"one"}}
	for i := 0; i < 15; i++ {
		service.questionAnswerMu.Lock()
		_, _, _, admitted := service.nextQuestionAnswerDispatchLocked()
		service.questionAnswerMu.Unlock()
		if !admitted {
			t.Fatalf("request %d must enter the default 15 shared slots; admitted=%d", i+1, service.questionAnswerInFlight)
		}
	}
	service.questionAnswerMu.Lock()
	_, _, _, admitted := service.nextQuestionAnswerDispatchLocked()
	service.questionAnswerMu.Unlock()
	if admitted || service.questionAnswerInFlight != 15 || run.next != 15 {
		t.Fatalf("sixteenth must wait: admitted=%v inFlight=%d next=%d", admitted, service.questionAnswerInFlight, run.next)
	}
}

func TestC2QuestionAnswerAPIOnlyRejectsEveryWriteBeforeStorage(t *testing.T) {
	service := &Service{backgroundTasksDisabled: true}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	for _, route := range []struct{ method, path string }{
		{"PUT", "question-answer-runtime-settings"},
		{"PUT", "question-answer-schedule-limits"},
		{"POST", "question-answer-schedules"},
		{"POST", "question-answer-schedules/preview"},
		{"PUT", "question-answer-schedules/plan"},
		{"POST", "question-answer-schedules/plan/enable"},
		{"POST", "question-answer-schedules/plan/disable"},
		{"POST", "question-answer-schedules/plan/revalidate"},
		{"DELETE", "question-answer-schedules/plan?expectedVersion=1"},
		{"POST", "question-answer-schedules/plan/run"},
		{"POST", "question-answer-schedule-executions/execution/cancel"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, "/api/connection-health/"+route.path, strings.NewReader(`{}`))
			req = req.WithContext(authctx.WithUserID(req.Context(), "c2-user"))
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), ErrorQuestionAnswerServiceStopped) {
				t.Fatalf("APIOnly must reject before nil storage/session: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestC2QuestionAnswerRecentUsesNewestTerminalEvenUnjudgedAndPreservesActive(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	const target = "sub2api:ws1:c2-account"
	_, err := pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_records
		(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at) VALUES
		('c2-old','c2-user',$1,'old','model','q','Q','Body','succeeded','correct','2026-10-01 00:00:00Z','2026-10-01 00:00:01Z'),
		('c2-new','c2-user',$1,'new','model','q','Q','Body','succeeded','unreviewed','2026-10-02 00:00:00Z','2026-10-02 00:00:01Z'),
		('c2-active','c2-user',$1,'active','model','q','Q','Body','pending',NULL,'2026-10-03 00:00:00Z',NULL)`, target)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithBackgroundTasks(repo, nil, nil, nil, false)
	service.accounts = fakeAdminAccountResolver{id: "ws1"}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	req := httptest.NewRequest("GET", "/api/connection-health/question-answer-recent-summaries?targetId="+target+"&targetId="+target+"&targetId=sub2api:ws1:empty", nil)
	req = req.WithContext(authctx.WithUserID(req.Context(), "c2-user"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("pure-local APIOnly recent GET: status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []struct {
			TargetID string `json:"targetId"`
			Recent   *struct {
				BatchID string                    `json:"batchId"`
				Source  string                    `json:"source"`
				Reviews QuestionAnswerReviewStats `json:"reviews"`
			} `json:"recentQuestionAnswer"`
			Active bool `json:"activeNewerBatch"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].TargetID != target || body.Items[0].Recent == nil {
		t.Fatalf("must preserve complete deduplicated input order: %s", response.Body.String())
	}
	item := body.Items[0]
	if item.Recent.BatchID != "new" || item.Recent.Source != "manual" || item.Recent.Reviews.Correct != 0 || item.Recent.Reviews.Incorrect != 0 || item.Recent.Reviews.Unreviewed != 1 || !item.Active {
		t.Fatalf("must not fall back to old 100%% or replace terminal with active: %s", response.Body.String())
	}
	if body.Items[1].Recent != nil || body.Items[1].Active {
		t.Fatalf("empty target must remain null/false: %s", response.Body.String())
	}
}

package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/shared/authctx"
)

const questionAnswerContractHeader = "X-TransitHub-Question-Answer-Contract"

type questionAnswerHandlerFixture struct {
	mux               *http.ServeMux
	pool              *pgxpool.Pool
	targetID          string
	succeededBatchID  string
	succeededRecordID string
	failedBatchID     string
	failedRecordID    string
	priorityActions   *fakeTargetPriorityActioner
}

func newQuestionAnswerHandlerFixture(t *testing.T) questionAnswerHandlerFixture {
	t.Helper()
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	repository := NewRepository(pool)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	question, err := repository.CreateTestQuestion(ctx, "handler-user", "Handler question", "Handler body")
	if err != nil {
		t.Fatalf("create handler question: %v", err)
	}
	targetID := "sub2api:ws1:handler-account"
	succeededBatchID := "handler-succeeded"
	succeeded, err := repository.CreateQuestionAnswerBatch(ctx, "handler-user", targetID, succeededBatchID, []string{"model-a"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(succeeded) != 1 {
		t.Fatalf("create succeeded handler record: records=%+v err=%v", succeeded, err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "handler-user", succeededBatchID, succeeded[0].ID); err != nil || !running {
		t.Fatalf("mark succeeded handler record running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "handler-user", succeededBatchID, succeeded[0].ID, QuestionAnswerSucceeded, "answer", ""); err != nil || !completed {
		t.Fatalf("complete succeeded handler record=%v err=%v", completed, err)
	}

	failedBatchID := "handler-failed"
	failed, err := repository.CreateQuestionAnswerBatch(ctx, "handler-user", targetID, failedBatchID, []string{"model-b"}, []string{question.ID}, QuestionAnswerReasoningEffortMedium)
	if err != nil || len(failed) != 1 {
		t.Fatalf("create failed handler record: records=%+v err=%v", failed, err)
	}
	if running, err := repository.MarkQuestionAnswerRunning(ctx, "handler-user", failedBatchID, failed[0].ID); err != nil || !running {
		t.Fatalf("mark failed handler record running=%v err=%v", running, err)
	}
	if completed, err := repository.CompleteQuestionAnswer(ctx, "handler-user", failedBatchID, failed[0].ID, QuestionAnswerFailed, "", QuestionAnswerErrorNetwork); err != nil || !completed {
		t.Fatalf("complete failed handler record=%v err=%v", completed, err)
	}

	priorityActions := &fakeTargetPriorityActioner{}
	service := &Service{
		questionAnswers: repository,
		accounts:        fakeAdminAccountResolver{id: "ws1"},
		priorityActions: priorityActions,
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	return questionAnswerHandlerFixture{
		mux: mux, pool: pool, targetID: targetID,
		succeededBatchID: succeededBatchID, succeededRecordID: succeeded[0].ID,
		failedBatchID: failedBatchID, failedRecordID: failed[0].ID,
		priorityActions: priorityActions,
	}
}

func (f questionAnswerHandlerFixture) request(t *testing.T, method string, path string, body string, contract string) *httptest.ResponseRecorder {
	t.Helper()
	return f.requestAs(t, "handler-user", method, path, body, contract)
}

func (f questionAnswerHandlerFixture) requestAs(t *testing.T, userID string, method string, path string, body string, contract string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request = request.WithContext(authctx.WithUserID(request.Context(), userID))
	if contract != "" {
		request.Header.Set(questionAnswerContractHeader, contract)
	}
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, request)
	return response
}

func TestQuestionAnswerHandlersRejectMissingOrWrongContractBeforeBusinessLogic(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	paths := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "start", method: http.MethodPost, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches", body: `{}`},
		{name: "latest", method: http.MethodGet, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/latest"},
		{name: "batch", method: http.MethodGet, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/" + fixture.succeededBatchID},
		{name: "cancel", method: http.MethodPost, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/" + fixture.failedBatchID + "/cancel"},
		{name: "history", method: http.MethodGet, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/history"},
		{name: "judgment", method: http.MethodPut, path: "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment", body: `{"judgment":"correct"}`},
	}
	for _, requestCase := range paths {
		for _, contract := range []string{"", "1"} {
			t.Run(requestCase.name+"/contract="+contract, func(t *testing.T) {
				response := fixture.request(t, requestCase.method, requestCase.path, requestCase.body, contract)
				if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "admin.connectionHealth.errors.questionAnswerContractMismatch") {
					t.Fatalf("status=%d body=%s, want 409 contract mismatch", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestQuestionAnswerOldManualErrorEndpointIsReadOnlyCompatibilityRejection(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	path := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/manual-error"
	response := fixture.request(t, http.MethodPut, path, `{"manualError":true}`, "2")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "admin.connectionHealth.errors.questionAnswerContractMismatch") {
		t.Fatalf("status=%d body=%s, want 409 contract mismatch", response.Code, response.Body.String())
	}
	var manualError bool
	if err := fixture.pool.QueryRow(context.Background(), `SELECT manual_error FROM connection_health_question_answer_records WHERE id = $1`, fixture.succeededRecordID).Scan(&manualError); err != nil {
		t.Fatalf("read old mirror after compatibility rejection: %v", err)
	}
	if manualError {
		t.Fatal("old manual-error endpoint changed the compatibility mirror")
	}
}

func TestQuestionAnswerJudgmentHandlerSavesAuthoritativeStateWithoutPriority(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	path := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment"
	for attempt := 0; attempt < 100; attempt++ {
		judgment := "correct"
		if attempt > 1 && attempt%2 == 1 {
			judgment = "incorrect"
		}
		response := fixture.request(t, http.MethodPut, path, `{"judgment":"`+judgment+`"}`, "2")
		if response.Code != http.StatusOK {
			t.Fatalf("save %s status=%d body=%s", judgment, response.Code, response.Body.String())
		}
		var record map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &record); err != nil {
			t.Fatalf("decode %s judgment response: %v", judgment, err)
		}
		if record["answerJudgment"] != judgment {
			t.Fatalf("response answerJudgment=%#v, want %s", record["answerJudgment"], judgment)
		}
		wantManualError := judgment == "incorrect"
		if record["manualError"] != wantManualError {
			t.Fatalf("response manualError=%#v, want %v for %s", record["manualError"], wantManualError, judgment)
		}
	}
	var storedJudgment string
	var storedManualError bool
	if err := fixture.pool.QueryRow(context.Background(), `SELECT answer_judgment, manual_error FROM connection_health_question_answer_records WHERE id = $1`, fixture.succeededRecordID).Scan(&storedJudgment, &storedManualError); err != nil {
		t.Fatalf("read stored judgment: %v", err)
	}
	if storedJudgment != "incorrect" || !storedManualError {
		t.Fatalf("stored judgment=%s manualError=%v", storedJudgment, storedManualError)
	}
	if len(fixture.priorityActions.calls) != 0 {
		t.Fatalf("judgment save triggered Priority calls: %+v", fixture.priorityActions.calls)
	}
}

func TestQuestionAnswerJudgmentHandlerRejectsUnauthenticatedRequest(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	path := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment"
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"judgment":"incorrect"}`))
	request.Header.Set(questionAnswerContractHeader, "2")
	response := httptest.NewRecorder()

	fixture.mux.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "auth.errors.unauthorized") {
		t.Fatalf("status=%d body=%s, want unauthenticated rejection", response.Code, response.Body.String())
	}
	var judgment string
	var manualError bool
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT answer_judgment, manual_error
		FROM connection_health_question_answer_records
		WHERE id = $1
	`, fixture.succeededRecordID).Scan(&judgment, &manualError); err != nil {
		t.Fatalf("read judgment after unauthenticated rejection: %v", err)
	}
	if judgment != "unreviewed" || manualError {
		t.Fatalf("unauthenticated request changed judgment=%s manualError=%v", judgment, manualError)
	}
	if len(fixture.priorityActions.calls) != 0 {
		t.Fatalf("unauthenticated rejection triggered Priority calls: %+v", fixture.priorityActions.calls)
	}
}

func TestQuestionAnswerJudgmentHandlerRejectsInvalidAndNonSucceededRecords(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	invalidPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment"
	for _, body := range []string{`{"judgment":"unknown"}`, `{"judgment":`} {
		invalid := fixture.request(t, http.MethodPut, invalidPath, body, "2")
		if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), ErrorRequest) {
			t.Fatalf("invalid judgment body=%q status=%d response=%s", body, invalid.Code, invalid.Body.String())
		}
	}

	failedPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.failedRecordID + "/judgment"
	failed := fixture.request(t, http.MethodPut, failedPath, `{"judgment":"incorrect"}`, "2")
	if failed.Code != http.StatusConflict || !strings.Contains(failed.Body.String(), "admin.connectionHealth.errors.questionAnswerJudgmentForbidden") {
		t.Fatalf("failed-record judgment status=%d body=%s", failed.Code, failed.Body.String())
	}
	var judgment *string
	if err := fixture.pool.QueryRow(context.Background(), `SELECT answer_judgment FROM connection_health_question_answer_records WHERE id = $1`, fixture.failedRecordID).Scan(&judgment); err != nil {
		t.Fatalf("read failed record judgment: %v", err)
	}
	if judgment != nil {
		t.Fatalf("failed request received judgment %v", judgment)
	}
	if len(fixture.priorityActions.calls) != 0 {
		t.Fatalf("rejected judgment triggered Priority calls: %+v", fixture.priorityActions.calls)
	}
}

func TestQuestionAnswerJudgmentHandlerScopesWritesByUserAndTarget(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	originalPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment"
	wrongTargetPath := "/api/connection-health/targets/sub2api:ws1:other-account/question-answers/records/" + fixture.succeededRecordID + "/judgment"

	for _, requestCase := range []struct {
		name   string
		userID string
		path   string
	}{
		{name: "other user", userID: "other-handler-user", path: originalPath},
		{name: "other target", userID: "handler-user", path: wrongTargetPath},
	} {
		t.Run(requestCase.name, func(t *testing.T) {
			response := fixture.requestAs(t, requestCase.userID, http.MethodPut, requestCase.path, `{"judgment":"incorrect"}`, "2")
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "admin.connectionHealth.errors.questionAnswerJudgmentForbidden") {
				t.Fatalf("status=%d body=%s, want scoped write rejection", response.Code, response.Body.String())
			}
		})
	}

	var judgment string
	var manualError bool
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT answer_judgment, manual_error
		FROM connection_health_question_answer_records
		WHERE id = $1
	`, fixture.succeededRecordID).Scan(&judgment, &manualError); err != nil {
		t.Fatalf("read judgment after scoped rejections: %v", err)
	}
	if judgment != "unreviewed" || manualError {
		t.Fatalf("scoped rejection changed judgment=%s manualError=%v", judgment, manualError)
	}
	if len(fixture.priorityActions.calls) != 0 {
		t.Fatalf("scoped rejection triggered Priority calls: %+v", fixture.priorityActions.calls)
	}
}

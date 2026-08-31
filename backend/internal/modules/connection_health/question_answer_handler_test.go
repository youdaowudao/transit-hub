package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/shared/authctx"
)

const questionAnswerContractHeader = "X-TransitHub-Question-Answer-Contract"

type questionAnswerHandlerFixture struct {
	mux               *http.ServeMux
	pool              *pgxpool.Pool
	targetID          string
	questionID        string
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
	question, err := repository.CreateTestQuestion(ctx, "handler-user", "Handler question", "Handler body", []string{"Error", "错误码"})
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
		mux: mux, pool: pool, targetID: targetID, questionID: question.ID,
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

func TestQuestionAnswerKeywordHandlersPreserveOldClientsAndRejectInvalidInput(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	questionsPath := "/api/connection-health/test-questions"

	missingCreate := fixture.request(t, http.MethodPost, questionsPath, `{"name":"Missing keywords","body":"Missing body"}`, "")
	if missingCreate.Code != http.StatusCreated {
		t.Fatalf("missing create status=%d body=%s", missingCreate.Code, missingCreate.Body.String())
	}
	var missingQuestion map[string]any
	if err := json.Unmarshal(missingCreate.Body.Bytes(), &missingQuestion); err != nil {
		t.Fatalf("decode missing create: %v", err)
	}
	if keywords, ok := missingQuestion["keywords"].([]any); !ok || len(keywords) != 0 {
		t.Fatalf("missing create keywords=%#v, want []", missingQuestion["keywords"])
	}

	nullCreate := fixture.request(t, http.MethodPost, questionsPath, `{"name":"Null keywords","body":"Null body","keywords":null}`, "")
	if nullCreate.Code != http.StatusCreated {
		t.Fatalf("null create status=%d body=%s", nullCreate.Code, nullCreate.Body.String())
	}
	var nullQuestion map[string]any
	if err := json.Unmarshal(nullCreate.Body.Bytes(), &nullQuestion); err != nil {
		t.Fatalf("decode null create: %v", err)
	}
	if keywords, ok := nullQuestion["keywords"].([]any); !ok || len(keywords) != 0 {
		t.Fatalf("null create keywords=%#v, want []", nullQuestion["keywords"])
	}

	updatePath := questionsPath + "/" + fixture.questionID
	for _, requestCase := range []struct {
		name string
		body string
	}{
		{name: "missing", body: `{"name":"Handler changed","body":"Handler body changed"}`},
		{name: "null", body: `{"name":"Handler changed again","body":"Handler body changed again","keywords":null}`},
	} {
		t.Run("update "+requestCase.name+" preserves keywords", func(t *testing.T) {
			response := fixture.request(t, http.MethodPut, updatePath, requestCase.body, "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var question map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &question); err != nil {
				t.Fatalf("decode update: %v", err)
			}
			keywords, ok := question["keywords"].([]any)
			if !ok || len(keywords) != 2 || keywords[0] != "Error" || keywords[1] != "错误码" {
				t.Fatalf("preserved keywords=%#v", question["keywords"])
			}
		})
	}

	clearResponse := fixture.request(t, http.MethodPut, updatePath, `{"name":"Handler cleared","body":"Handler body cleared","keywords":[]}`, "")
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clearResponse.Code, clearResponse.Body.String())
	}
	var cleared map[string]any
	if err := json.Unmarshal(clearResponse.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if keywords, ok := cleared["keywords"].([]any); !ok || len(keywords) != 0 {
		t.Fatalf("cleared keywords=%#v, want []", cleared["keywords"])
	}

	for _, endpoint := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{name: "list", method: http.MethodGet, path: questionsPath, status: http.StatusOK},
		{name: "enabled", method: http.MethodPost, path: updatePath + "/enabled", body: `{"enabled":true}`, status: http.StatusOK},
		{name: "default", method: http.MethodPost, path: updatePath + "/default", status: http.StatusOK},
	} {
		t.Run(endpoint.name+" never returns null keywords", func(t *testing.T) {
			response := fixture.request(t, endpoint.method, endpoint.path, endpoint.body, "")
			if response.Code != endpoint.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			items, ok := payload.([]any)
			if !ok {
				items = []any{payload}
			}
			for _, item := range items {
				question := item.(map[string]any)
				if _, ok := question["keywords"].([]any); !ok {
					t.Fatalf("keywords=%#v, want JSON array", question["keywords"])
				}
			}
		})
	}

	countBefore := 0
	if err := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_test_questions WHERE user_id = 'handler-user'`).Scan(&countBefore); err != nil {
		t.Fatalf("count before invalid requests: %v", err)
	}
	countKeywords := make([]string, TestQuestionKeywordCountLimit+1)
	for i := range countKeywords {
		countKeywords[i] = fmt.Sprintf("keyword-%02d", i)
	}
	byteKeywords := make([]string, 0, 9)
	for _, value := range []string{"😀", "😁", "😂", "😃", "😄", "😅", "😆", "😉"} {
		byteKeywords = append(byteKeywords, strings.Repeat(value, TestQuestionKeywordRuneLimit))
	}
	byteKeywords = append(byteKeywords, "x")
	for _, invalid := range []struct {
		name     string
		keywords []string
		errorKey string
	}{
		{name: "blank", keywords: []string{" "}, errorKey: ErrorTestQuestionKeywordBlank},
		{name: "count", keywords: countKeywords, errorKey: ErrorTestQuestionKeywordCount},
		{name: "length", keywords: []string{strings.Repeat("界", TestQuestionKeywordRuneLimit+1)}, errorKey: ErrorTestQuestionKeywordLength},
		{name: "bytes", keywords: byteKeywords, errorKey: ErrorTestQuestionKeywordBytes},
	} {
		t.Run("reject "+invalid.name+" without partial write", func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"name": "Invalid " + invalid.name, "body": "Invalid body", "keywords": invalid.keywords})
			if err != nil {
				t.Fatal(err)
			}
			response := fixture.request(t, http.MethodPost, questionsPath, string(body), "")
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), invalid.errorKey) {
				t.Fatalf("status=%d body=%s, want 400 %s", response.Code, response.Body.String(), invalid.errorKey)
			}
		})
	}
	countAfter := 0
	if err := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM connection_health_test_questions WHERE user_id = 'handler-user'`).Scan(&countAfter); err != nil {
		t.Fatalf("count after invalid requests: %v", err)
	}
	if countAfter != countBefore {
		t.Fatalf("invalid keyword requests changed row count %d -> %d", countBefore, countAfter)
	}
}

func TestQuestionAnswerKeywordSnapshotAppearsAcrossReadAndJudgmentHandlers(t *testing.T) {
	fixture := newQuestionAnswerHandlerFixture(t)
	contract := "2"
	assertSnapshot := func(t *testing.T, record map[string]any, want []string, wantNull bool) {
		t.Helper()
		value, exists := record["questionKeywordSnapshot"]
		if !exists {
			t.Fatal("questionKeywordSnapshot field is missing")
		}
		if wantNull {
			if value != nil {
				t.Fatalf("snapshot=%#v, want null", value)
			}
			return
		}
		items, ok := value.([]any)
		if !ok || len(items) != len(want) {
			t.Fatalf("snapshot=%#v, want %#v", value, want)
		}
		for i := range want {
			if items[i] != want[i] {
				t.Fatalf("snapshot=%#v, want %#v", value, want)
			}
		}
	}
	decodeBatchRecords := func(t *testing.T, response *httptest.ResponseRecorder) []any {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var batch map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		records, ok := batch["records"].([]any)
		if !ok {
			t.Fatalf("batch records=%#v", batch["records"])
		}
		return records
	}

	configuredPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/" + fixture.succeededBatchID
	configuredRecords := decodeBatchRecords(t, fixture.request(t, http.MethodGet, configuredPath, "", contract))
	if len(configuredRecords) != 1 {
		t.Fatalf("configured records=%d", len(configuredRecords))
	}
	assertSnapshot(t, configuredRecords[0].(map[string]any), []string{"Error", "错误码"}, false)

	repository := NewRepository(fixture.pool)
	emptyQuestion, err := repository.CreateTestQuestion(context.Background(), "handler-user", "Empty snapshot", "Empty snapshot body", []string{})
	if err != nil {
		t.Fatalf("create empty snapshot question: %v", err)
	}
	emptyBatchID := "handler-empty-snapshot"
	emptyRecords, err := repository.CreateQuestionAnswerBatch(
		context.Background(), "handler-user", fixture.targetID, emptyBatchID,
		[]string{"model-empty"}, []string{emptyQuestion.ID}, QuestionAnswerReasoningEffortMedium,
	)
	if err != nil || len(emptyRecords) != 1 {
		t.Fatalf("create empty snapshot batch=%+v err=%v", emptyRecords, err)
	}
	emptyPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/" + emptyBatchID
	emptyReadRecords := decodeBatchRecords(t, fixture.request(t, http.MethodGet, emptyPath, "", contract))
	assertSnapshot(t, emptyReadRecords[0].(map[string]any), []string{}, false)
	cancelPath := emptyPath + "/cancel"
	emptyCancelRecords := decodeBatchRecords(t, fixture.request(t, http.MethodPost, cancelPath, "", contract))
	assertSnapshot(t, emptyCancelRecords[0].(map[string]any), []string{}, false)

	if _, err := fixture.pool.Exec(context.Background(), `
		INSERT INTO connection_health_question_answer_records (
			id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
			reasoning_effort, answer_body, status, answer_judgment
		) VALUES (
			'handler-legacy-snapshot-record', 'handler-user', $1, 'handler-legacy-snapshot',
			'model-legacy', $2, 'Legacy snapshot', 'Legacy snapshot body',
			'medium', 'legacy answer', 'succeeded', 'unreviewed'
		)
	`, fixture.targetID, fixture.questionID); err != nil {
		t.Fatalf("insert legacy snapshot record: %v", err)
	}
	legacyPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/handler-legacy-snapshot"
	legacyRecords := decodeBatchRecords(t, fixture.request(t, http.MethodGet, legacyPath, "", contract))
	assertSnapshot(t, legacyRecords[0].(map[string]any), nil, true)

	latestPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/batches/latest"
	latestRecords := decodeBatchRecords(t, fixture.request(t, http.MethodGet, latestPath, "", contract))
	if len(latestRecords) == 0 {
		t.Fatal("latest response has no records")
	}
	if _, exists := latestRecords[0].(map[string]any)["questionKeywordSnapshot"]; !exists {
		t.Fatal("latest response omits questionKeywordSnapshot")
	}

	historyPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/history?page=1"
	historyResponse := fixture.request(t, http.MethodGet, historyPath, "", contract)
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("history status=%d body=%s", historyResponse.Code, historyResponse.Body.String())
	}
	var history map[string]any
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	for _, item := range history["records"].([]any) {
		if _, exists := item.(map[string]any)["questionKeywordSnapshot"]; !exists {
			t.Fatal("history response omits questionKeywordSnapshot")
		}
	}

	judgmentPath := "/api/connection-health/targets/" + fixture.targetID + "/question-answers/records/" + fixture.succeededRecordID + "/judgment"
	judgmentResponse := fixture.request(t, http.MethodPut, judgmentPath, `{"judgment":"correct"}`, contract)
	if judgmentResponse.Code != http.StatusOK {
		t.Fatalf("judgment status=%d body=%s", judgmentResponse.Code, judgmentResponse.Body.String())
	}
	var judgmentRecord map[string]any
	if err := json.Unmarshal(judgmentResponse.Body.Bytes(), &judgmentRecord); err != nil {
		t.Fatalf("decode judgment: %v", err)
	}
	assertSnapshot(t, judgmentRecord, []string{"Error", "错误码"}, false)
}

func TestQuestionAnswerKeywordSnapshotAppearsInStartHandlerResponse(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		http.NotFound(w, request)
	}))
	t.Cleanup(upstreamServer.Close)

	repository := newFakeQuestionAnswerRepository(TestQuestion{
		ID: "question-start-snapshot", Name: "Start snapshot", Body: "Start snapshot body",
		Keywords: []string{"Error", "错误码"}, Enabled: true,
	})
	service := newQuestionAnswerService(upstreamServer.URL, repository, newFakeRepository())
	started := make(chan struct{})
	cancelled := make(chan struct{})
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: &contextBlockingRoundTripper{
		started: started, cancelled: cancelled,
	}}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.ShutdownQuestionAnswers(ctx); err != nil {
			t.Errorf("shutdown question answers: %v", err)
		}
	})

	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/connection-health/targets/sub2api:ws1:acc-1/question-answers/batches",
		strings.NewReader(`{"models":["model-a"],"questionIds":["question-start-snapshot"],"reasoningEffort":"medium"}`),
	)
	request = request.WithContext(authctx.WithUserID(request.Context(), "user1"))
	request.Header.Set(questionAnswerContractHeader, "2")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var batch map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	records, ok := batch["records"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("start records=%#v", batch["records"])
	}
	record := records[0].(map[string]any)
	snapshot, ok := record["questionKeywordSnapshot"].([]any)
	if !ok || len(snapshot) != 2 || snapshot[0] != "Error" || snapshot[1] != "错误码" {
		t.Fatalf("start snapshot=%#v, want [Error 错误码]", record["questionKeywordSnapshot"])
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("question answer worker did not start")
	}
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

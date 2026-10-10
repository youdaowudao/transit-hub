package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

type questionAnswerEvidenceReader struct{}

func (questionAnswerEvidenceReader) Read([]byte) (int, error) {
	return 0, errors.New("fake read failure")
}

func questionAnswerEvidenceAsk(status int, body, key string) QuestionAnswerAskResult {
	runner := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"X-Fake-Secret": []string{"HEADER-ONLY-MARKER"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	return runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "http://fake.invalid", Key: key}, "model", "question", QuestionAnswerReasoningEffortMedium, TestProtocolResponses)
}

func TestQuestionAnswerDetailedFailureBranches(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		body, errorType string
		excerpt         bool
	}{
		{"400", 400, `{"error":{"message":"bad request"}}`, QuestionAnswerErrorInvalidResponse, true},
		{"404 ambiguous", 404, `{"error":{"message":"missing route"}}`, QuestionAnswerErrorInvalidResponse, true},
		{"429", 429, `{"error":{"message":"rate limit"}}`, QuestionAnswerErrorRateLimited, true},
		{"500", 500, `{"error":{"message":"server error"}}`, QuestionAnswerErrorServer, true},
		{"incomplete", 200, `{"status":"incomplete","output":[]}`, QuestionAnswerErrorInvalidResponse, true},
		{"oversized", 200, strings.Repeat("文", (1<<20)/3+2), QuestionAnswerErrorResponseTooLarge, true},
		{"600", 600, "fake failure", QuestionAnswerErrorServer, true},
		{"999", 999, "fake failure", QuestionAnswerErrorServer, true},
		{"42", 42, "fake failure", QuestionAnswerErrorInvalidResponse, true},
		{"empty", 500, " \n\t", QuestionAnswerErrorServer, false},
		{"success", 200, `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"safe answer"}]}]}`, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := questionAnswerEvidenceAsk(c.status, c.body, "fake-key")
			if result.ErrorType != c.errorType {
				t.Fatalf("error type=%q want %q", result.ErrorType, c.errorType)
			}
			expectedStatus := c.status
			if expectedStatus < 100 || expectedStatus > 999 || c.errorType == "" {
				expectedStatus = 0
			}
			if result.UpstreamStatus != expectedStatus || (result.UpstreamExcerpt != "") != c.excerpt {
				t.Fatalf("evidence status=%d excerpt-present=%v want %d/%v", result.UpstreamStatus, result.UpstreamExcerpt != "", expectedStatus, c.excerpt)
			}
			if strings.Contains(result.UpstreamExcerpt, "HEADER-ONLY-MARKER") {
				t.Fatal("response header persisted")
			}
			if c.errorType == "" && result.Answer != "safe answer" {
				t.Fatalf("answer=%q", result.Answer)
			}
		})
	}
	for _, c := range []struct {
		name                 string
		baseURL              string
		readError, sendError bool
		errorType            string
		status               int
	}{
		{"build failure", "://bad", false, false, QuestionAnswerErrorInvalidResponse, 0},
		{"send failure", "http://fake.invalid", false, true, QuestionAnswerErrorNetwork, 0},
		{"body read failure", "http://fake.invalid", true, false, QuestionAnswerErrorNetwork, 429},
	} {
		t.Run(c.name, func(t *testing.T) {
			runner := &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(*http.Request) (*http.Response, error) {
				if c.sendError {
					return nil, errors.New("fake send failure")
				}
				return &http.Response{StatusCode: c.status, Body: io.NopCloser(questionAnswerEvidenceReader{}), Header: make(http.Header)}, nil
			})}}
			result := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: c.baseURL, Key: "fake-key"}, "model", "question", QuestionAnswerReasoningEffortMedium)
			if result.ErrorType != c.errorType || result.UpstreamStatus != c.status || result.UpstreamExcerpt != "" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestQuestionAnswerEvidenceSecretProtection(t *testing.T) {
	quote := func(s string) string { data, _ := json.Marshal(s); return string(data[1 : len(data)-1]) }
	nested := func(s string, n int) string {
		for i := 0; i < n; i++ {
			s = quote(s)
		}
		return s
	}
	cases := []struct {
		name, key, body string
		retained        bool
		contains        string
	}{
		{"literal", "fake-secret", "error fake-secret tail", true, "error *** tail"},
		{"standard escaped", `fake"secret`, `error ` + quote(`fake"secret`), true, "***"},
		{"nul joins key", "fake-secret", "fake-\x00secret", true, "***"},
		{"invalid utf8 joins key", "fake-secret", "fake-\xffsecret", true, "***"},
		{"sk token", "account-key", `{"error":"sk-abcdefghijklmnop","next":"keep"}`, true, `"next":"keep"`},
		{"Bearer token", "account-key", `{"error":"bEaReR abcdefghijklmnop","next":"keep"}`, true, `bEaReR ***","next":"keep"`},
		{"ordinary identifiers", "account-key", "task-abcdefghijklmnop NotBearer abcdefghijklmnop", true, "task-abcdefghijklmnop NotBearer abcdefghijklmnop"},
		{"ordinary nested", "account-key", nested(`plain \"error\"`, 3), true, "plain"},
		{"escaped slash", "a/b/c", `a\/b/c`, false, ""},
		{"unicode escape", "abc123", `\u0061bc123`, false, ""},
		{"unicode surrogate", "a😀b", `a\ud83d\ude00b`, false, ""},
		{"two layers", "abc123", nested(`\u0061bc123`, 1), false, ""},
		{"three layers", "abc123", nested(`\u0061bc123`, 2), false, ""},
		{"four layers", "abc123", strings.Repeat(`\`, 8) + "u0061bc123", false, ""},
		{"still changing after eight", "fake-secret", strings.Repeat(`\`, 512) + "safe", false, ""},
		{"replacement recreates key", "test***", "testtesttest***", false, ""},
		{"full secret crosses cutoff", "abc123", strings.Repeat("文", 498) + `\u0061bc123`, false, ""},
		// Full text decodes to safex, but the cut keeps only the trailing escape
		// prefix: the actual 500-character excerpt decodes to the complete key.
		{"truncation creates key", `safe\`, strings.Repeat("文", 490) + `\u0073afe\u0078`, false, ""},
		{"empty key", "", `plain error`, true, "plain error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := questionAnswerEvidenceAsk(500, c.body, c.key)
			if result.UpstreamStatus != 500 || (result.UpstreamExcerpt != "") != c.retained {
				t.Fatalf("status=%d retained=%v want 500/%v", result.UpstreamStatus, result.UpstreamExcerpt != "", c.retained)
			}
			if c.contains != "" && !strings.Contains(result.UpstreamExcerpt, c.contains) {
				t.Fatalf("excerpt=%q missing %q", result.UpstreamExcerpt, c.contains)
			}
			if c.key != "" && strings.Contains(result.UpstreamExcerpt, c.key) {
				t.Fatal("complete fake key persisted")
			}
			if c.name == "sk token" && strings.Contains(result.UpstreamExcerpt, "sk-abcdefghijklmnop") {
				t.Fatal("sk token persisted")
			}
			if c.name == "Bearer token" && strings.Contains(result.UpstreamExcerpt, "abcdefghijklmnop") {
				t.Fatal("Bearer token persisted")
			}
		})
	}
	result := questionAnswerEvidenceAsk(500, strings.Repeat("中", 600), "fake-key")
	if !utf8.ValidString(result.UpstreamExcerpt) || utf8.RuneCountInString(result.UpstreamExcerpt) != 500 {
		t.Fatalf("UTF8/count=%v/%d", utf8.ValidString(result.UpstreamExcerpt), utf8.RuneCountInString(result.UpstreamExcerpt))
	}
	result = questionAnswerEvidenceAsk(500, "\x00\xff 正文 \x00", "fake-key")
	if result.UpstreamExcerpt != "正文" {
		t.Fatalf("clean excerpt=%q", result.UpstreamExcerpt)
	}
}

func TestQuestionAnswerEvidenceCompletionRules(t *testing.T) {
	for _, c := range []struct {
		name    string
		result  QuestionAnswerAskResult
		timeout bool
		status  QuestionAnswerStatus
		saved   bool
	}{
		{"request failure", QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorInvalidResponse, UpstreamStatus: 400, UpstreamExcerpt: "failure"}, false, QuestionAnswerFailed, true},
		{"failure then timeout", QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorInvalidResponse, UpstreamStatus: 400, UpstreamExcerpt: "failure"}, true, QuestionAnswerFailed, true},
		{"success then timeout", QuestionAnswerAskResult{Answer: "answer"}, true, QuestionAnswerFailed, false},
		{"success", QuestionAnswerAskResult{Answer: "answer", UpstreamStatus: 200, UpstreamExcerpt: "raw"}, false, QuestionAnswerSucceeded, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := questionAnswerCompletionFromAsk(c.result, c.timeout, QuestionAnswerRecord{})
			if got.Status != c.status || (got.UpstreamStatus != nil) != c.saved || (got.UpstreamExcerpt != "") != c.saved {
				t.Fatalf("completion=%+v", got)
			}
			if c.timeout && (got.ErrorType != QuestionAnswerErrorTimeout || got.AnswerBody != "") {
				t.Fatalf("timeout=%+v", got)
			}
		})
	}
	out := questionAnswerCompletionFromAsk(QuestionAnswerAskResult{ErrorType: QuestionAnswerErrorInvalidResponse, UpstreamStatus: 42, UpstreamExcerpt: "failure"}, false, QuestionAnswerRecord{})
	if out.UpstreamStatus != nil || out.UpstreamExcerpt != "failure" {
		t.Fatalf("out-of-range completion=%+v", out)
	}
}

func TestQuestionAnswerEvidenceBatchChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
			return
		}
		var input struct{ Model string }
		json.NewDecoder(r.Body).Decode(&input)
		if input.Model == "model-a" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"fake bad request"}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"safe answer"}}]}`)
	}))
	defer server.Close()
	repo := newFakeQuestionAnswerRepository(TestQuestion{ID: "q1", Name: "Q1", Body: "Question", Enabled: true})
	service := newQuestionAnswerService(server.URL, repo, newFakeRepository())
	defer service.ShutdownQuestionAnswers(context.Background())
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-1", QuestionAnswerStartInput{Models: []string{"model-a", "model-b"}, QuestionIDs: []string{"q1"}})
	if err != nil {
		t.Fatal(err)
	}
	completed := waitQuestionAnswerBatch(t, service, batch.BatchID, false)
	for _, r := range completed.Records {
		if r.ModelName == "model-a" {
			if r.Status != QuestionAnswerFailed || r.ErrorType != QuestionAnswerErrorInvalidResponse || r.UpstreamStatus == nil || *r.UpstreamStatus != 400 || !strings.Contains(r.UpstreamExcerpt, "fake bad request") {
				t.Fatalf("failure=%+v", r)
			}
		} else if r.Status != QuestionAnswerSucceeded || r.UpstreamStatus != nil || r.UpstreamExcerpt != "" {
			t.Fatalf("success=%+v", r)
		}
	}
}

func TestQuestionAnswerEvidencePostgresPreservesReadAndFinalization(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status) VALUES ('evidence-f','evidence-user','evidence-target','evidence-batch','model','q','Q','Body','running'),('evidence-c','evidence-user','evidence-target','evidence-batch','model','q','Q','Body','running'),('evidence-s','evidence-user','evidence-target','evidence-batch','model','q','Q','Body','running'),('evidence-status-only','evidence-user','evidence-target','evidence-batch','model','q','Q','Body','running'),('evidence-excerpt-only','evidence-user','evidence-target','evidence-batch','model','q','Q','Body','running')`)
	if err != nil {
		t.Fatal(err)
	}
	code := 400
	completion := QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorInvalidResponse, UpstreamStatus: &code, UpstreamExcerpt: questionAnswerEvidenceAsk(500, "\x00\xff 正文 \x00", "fake-key").UpstreamExcerpt}
	ok, err := repo.CompleteQuestionAnswer(ctx, "evidence-user", "evidence-batch", "evidence-f", completion)
	if err != nil || !ok {
		t.Fatalf("complete=%v/%v", ok, err)
	}
	if ok, err := repo.CompleteQuestionAnswer(ctx, "evidence-user", "evidence-batch", "evidence-s", questionAnswerTestCompletion(QuestionAnswerSucceeded, "answer", "")); err != nil || !ok {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id         string
		completion QuestionAnswerCompletion
	}{
		{"evidence-status-only", QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorServer, UpstreamStatus: &code}},
		{"evidence-excerpt-only", QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorServer, UpstreamExcerpt: "independent excerpt"}},
	} {
		if ok, err := repo.CompleteQuestionAnswer(ctx, "evidence-user", "evidence-batch", item.id, item.completion); err != nil || !ok {
			t.Fatalf("independent write=%v/%v", ok, err)
		}
	}
	before, err := repo.ListQuestionAnswerBatch(ctx, "evidence-user", "evidence-target", "evidence-batch")
	if err != nil {
		t.Fatal(err)
	}
	assertEvidence := func(records []QuestionAnswerRecord) {
		t.Helper()
		for _, r := range records {
			if r.ID == "evidence-f" && (r.UpstreamStatus == nil || *r.UpstreamStatus != 400 || r.UpstreamExcerpt != "正文") {
				t.Fatalf("read lost evidence=%+v", r)
			}
		}
	}
	assertEvidence(before)
	for _, r := range before {
		switch r.ID {
		case "evidence-status-only":
			if r.UpstreamStatus == nil || *r.UpstreamStatus != 400 || r.UpstreamExcerpt != "" {
				t.Fatalf("status-only=%+v", r)
			}
		case "evidence-excerpt-only":
			if r.UpstreamStatus != nil || r.UpstreamExcerpt != "independent excerpt" {
				t.Fatalf("excerpt-only=%+v", r)
			}
		}
	}

	for _, status := range []QuestionAnswerStatus{QuestionAnswerSucceeded, QuestionAnswerCancelled} {
		for _, field := range []string{"status", "excerpt"} {
			bad := questionAnswerTestCompletion(status, "", "")
			if field == "status" {
				bad.UpstreamStatus = &code
			} else {
				bad.UpstreamExcerpt = "must reject"
			}
			if _, err := repo.CompleteQuestionAnswer(ctx, "evidence-user", "evidence-batch", "evidence-c", bad); err == nil {
				t.Fatalf("accepted %s with %s", status, field)
			}
		}
	}
	if _, err := repo.FinalizeQuestionAnswerBatch(ctx, "evidence-user", "evidence-target", "evidence-batch", QuestionAnswerCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FailAbandonedQuestionAnswers(ctx, QuestionAnswerErrorServiceShutdown); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"evidence-f", "evidence-c"} {
		late := completion
		late.UpstreamExcerpt = "late overwrite"
		if ok, err := repo.CompleteQuestionAnswer(ctx, "evidence-user", "evidence-batch", id, late); err != nil || ok {
			t.Fatalf("late %s=%v/%v", id, ok, err)
		}
	}
	after, err := repo.ListQuestionAnswerBatch(ctx, "evidence-user", "evidence-target", "evidence-batch")
	if err != nil {
		t.Fatal(err)
	}
	assertEvidence(after)
	for _, r := range after {
		if (r.ID == "evidence-c" || r.ID == "evidence-s") && (r.UpstreamStatus != nil || r.UpstreamExcerpt != "") {
			t.Fatalf("nonfailure=%+v", r)
		}
	}
	// Compare statistics with identical request/judgment states and no evidence.
	cleared := cloneQuestionAnswerRecords(after)
	for i := range cleared {
		cleared[i].UpstreamStatus = nil
		cleared[i].UpstreamExcerpt = ""
	}
	if !reflect.DeepEqual(aggregateQuestionAnswerStats(after), aggregateQuestionAnswerStats(cleared)) {
		t.Fatal("evidence changed stats")
	}
}

func TestQuestionAnswerEvidenceHTTPFieldsAndOwnership(t *testing.T) {
	f := newQuestionAnswerHandlerFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET status='running' WHERE id=$1`, f.failedRecordID); err != nil {
		t.Fatal(err)
	}
	code := 400
	if _, err := NewRepository(f.pool).CompleteQuestionAnswer(ctx, "handler-user", f.failedBatchID, f.failedRecordID, QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorInvalidResponse, UpstreamStatus: &code, UpstreamExcerpt: "fake upstream failure"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		batch   string
		status  any
		excerpt string
	}{{f.failedBatchID, float64(400), "fake upstream failure"}, {f.succeededBatchID, nil, ""}} {
		path := fmt.Sprintf("/api/connection-health/targets/%s/question-answers/batches/%s", f.targetID, c.batch)
		response := f.request(t, http.MethodGet, path, "", "3")
		if response.Code != 200 {
			t.Fatalf("HTTP=%d %s", response.Code, response.Body.String())
		}
		var batch struct{ Records []map[string]any }
		if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Records) != 1 {
			t.Fatalf("records=%v", batch.Records)
		}
		r := batch.Records[0]
		status, exists := r["upstreamStatus"]
		if !exists || status != c.status || r["upstreamExcerpt"] != c.excerpt {
			t.Fatalf("fields=%v", r)
		}
		foreign := f.requestAs(t, "other-user", http.MethodGet, path, "", "3")
		if strings.Contains(foreign.Body.String(), f.failedRecordID) || strings.Contains(foreign.Body.String(), "fake upstream failure") {
			t.Fatal("foreign user read record")
		}
	}
}

func TestQuestionAnswerEvidenceMigrationOldRecordsAndRepeat(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='connection_health_question_answer_records' AND column_name IN ('upstream_status','upstream_excerpt')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("schema evidence columns=%d want 2", count)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_question_answer_records DROP COLUMN upstream_status, DROP COLUMN upstream_excerpt; INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status) VALUES ('legacy-evidence','legacy-user','legacy-target','legacy-batch','model','q','Q','Body','failed')`); err != nil {
		t.Fatal(err)
	}
	migration := "../../database/migrations/000035_connection_health_question_answer_failure_evidence.sql"
	applyQuestionAnswerMigrationForTest(t, ctx, pool, migration)
	applyQuestionAnswerMigrationForTest(t, ctx, pool, migration)
	for i := 0; i < 2; i++ {
		if err := repo.EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	records, err := repo.ListQuestionAnswerBatch(ctx, "legacy-user", "legacy-target", "legacy-batch")
	if err != nil || len(records) != 1 {
		t.Fatalf("old read=%+v/%v", records, err)
	}
	if records[0].UpstreamStatus != nil || records[0].UpstreamExcerpt != "" {
		t.Fatal("old record has evidence")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET upstream_status=42 WHERE id='legacy-evidence'`); err == nil {
		t.Fatal("invalid code accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_question_answer_records SET upstream_status=999,upstream_excerpt=$1 WHERE id='legacy-evidence'`, "正常正文"); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	applyQuestionAnswerMigrationForTest(t, ctx, pool, migration)
	records, err = repo.ListQuestionAnswerBatch(ctx, "legacy-user", "legacy-target", "legacy-batch")
	if err != nil || records[0].UpstreamStatus == nil || *records[0].UpstreamStatus != 999 || records[0].UpstreamExcerpt != "正常正文" {
		t.Fatalf("rerun erased evidence=%+v/%v", records, err)
	}
}

func TestQuestionAnswerEvidenceScheduledBatchRead(t *testing.T) {
	repo, _, ctx := c2ScheduleRepository(t)
	now := time.Now().UTC()
	execution, target := c2StoragePreparedExecution(t, repo, now)
	records, _, err := repo.CreateScheduledQuestionAnswerBatch(ctx, execution.UserID, execution.AdminAccountID, execution.ID, target.TargetID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	code := 500
	for _, r := range records {
		if _, err := repo.MarkQuestionAnswerRunning(ctx, execution.UserID, target.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CompleteQuestionAnswer(ctx, execution.UserID, target.ID, r.ID, QuestionAnswerCompletion{Status: QuestionAnswerFailed, ErrorType: QuestionAnswerErrorServer, UpstreamStatus: &code, UpstreamExcerpt: "fake scheduled failure"}); err != nil {
			t.Fatal(err)
		}
	}
	read, err := repo.ListQuestionAnswerBatch(ctx, execution.UserID, target.TargetID, target.ID)
	if err != nil || len(read) != len(records) {
		t.Fatalf("scheduled read=%+v/%v", read, err)
	}
	for _, r := range read {
		if r.UpstreamStatus == nil || *r.UpstreamStatus != 500 || r.UpstreamExcerpt != "fake scheduled failure" {
			t.Fatalf("scheduled lost evidence=%+v", r)
		}
	}
}

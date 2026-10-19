package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestMessagesC42QuestionAnswerResponseRules(t *testing.T) {
	for _, example := range []struct{ name, body, answer, errorType string }{
		{"trim_and_join_text_around_thinking", `{"type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"  first \n"},{"type":"text","text":" \t "},{"type":"thinking","thinking":"secret"},{"type":"text","text":" second  "},{"type":"thinking","thinking":"secret"}]}`, "first\nsecond", ""},
		{"stop_sequence", `{"type":"message","stop_reason":"stop_sequence","content":[{"type":"text","text":" answer "}]}`, "answer", ""},
		{"max_tokens_with_body", `{"type":"message","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`, "", QuestionAnswerErrorResponseIncomplete},
		{"max_tokens_without_body", `{"type":"message","stop_reason":"max_tokens"}`, "", QuestionAnswerErrorResponseIncomplete},
		{"not_json", `broken`, "", QuestionAnswerErrorInvalidResponse},
		{"wrong_type", `{"type":"error","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"missing_type", `{"stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"refusal", `{"type":"message","stop_reason":"refusal","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"tool_use", `{"type":"message","stop_reason":"tool_use","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"pause_turn", `{"type":"message","stop_reason":"pause_turn","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"other_stop", `{"type":"message","stop_reason":"unknown","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"missing_stop", `{"type":"message","content":[{"type":"text","text":"answer"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"thinking_only", `{"type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"secret"}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"empty_text", `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"  "}]}`, "", QuestionAnswerErrorInvalidResponse},
		{"missing_content", `{"type":"message","stop_reason":"end_turn"}`, "", QuestionAnswerErrorInvalidResponse},
		{"stop_sequence_without_content", `{"type":"message","stop_reason":"stop_sequence"}`, "", QuestionAnswerErrorInvalidResponse},
	} {
		t.Run(example.name, func(t *testing.T) {
			answer, errorType := extractMessagesQuestionAnswer([]byte(example.body))
			if answer != example.answer || errorType != example.errorType {
				t.Fatalf("extract answer=%q error=%q", answer, errorType)
			}
			calls := 0
			body := &trackingBody{Reader: strings.NewReader(example.body)}
			runner := NewQuestionAnswerRunner()
			runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: req}, nil
			})
			result := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture", AnthropicAuth: "x_api_key"}, "claude-opus-5-5", "question", QuestionAnswerReasoningEffortMedium, TestProtocolMessages)
			if result.Answer != example.answer || result.ErrorType != example.errorType || calls != 1 || !body.closed {
				t.Fatalf("result=%+v calls=%d closed=%v", result, calls, body.closed)
			}
			if example.errorType != "" && (result.UpstreamStatus != 200 || result.UpstreamExcerpt == "") {
				t.Fatalf("failure lost upstream evidence: %+v", result)
			}
			if strings.Contains(result.Answer, "secret") {
				t.Fatal("thinking included in answer")
			}
		})
	}
}

func TestMessagesC42QuestionAnswerEffortAndBearerAuth(t *testing.T) {
	for _, effort := range []QuestionAnswerReasoningEffort{QuestionAnswerReasoningEffortLow, QuestionAnswerReasoningEffortMedium, QuestionAnswerReasoningEffortHigh, QuestionAnswerReasoningEffortXHigh} {
		t.Run(string(effort), func(t *testing.T) {
			calls := 0
			runner := NewQuestionAnswerRunner()
			runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" || req.Header.Get("Authorization") != "Bearer fixture" || req.Header.Get("x-api-key") != "" || req.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("request path or headers incorrect")
				}
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				output, ok := payload["output_config"].(map[string]any)
				if len(payload) != 5 || payload["max_tokens"] != float64(64000) || payload["stream"] != true || !ok || len(output) != 1 || output["effort"] != string(effort) {
					t.Errorf("question body=%v", payload)
				}
				return protocolServiceResponse(req, 200, `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`), nil
			})
			result := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid/", Key: "fixture", AnthropicAuth: "authorization_bearer"}, "claude-sonnet-5-5", "question", effort, TestProtocolMessages)
			if result.Answer != "answer" || result.ErrorType != "" || calls != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
		})
	}
}

func TestMessagesC42QuestionAnswerOtherRequestErrorsStayInvalid(t *testing.T) {
	for _, example := range []struct {
		protocol TestProtocol
		baseURL  string
	}{{TestProtocol("bogus"), "https://fixture.invalid"}, {TestProtocolMessages, ":invalid"}} {
		runner := NewQuestionAnswerRunner()
		calls := 0
		runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			return protocolServiceResponse(req, 200, `{}`), nil
		})
		result := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: example.baseURL, Key: "fixture", AnthropicAuth: "x_api_key"}, "model", "question", QuestionAnswerReasoningEffortMedium, example.protocol)
		if result.ErrorType != QuestionAnswerErrorInvalidResponse || result.Answer != "" || result.UpstreamStatus != 0 || result.UpstreamExcerpt != "" || calls != 0 {
			t.Fatalf("result=%+v calls=%d", result, calls)
		}
	}
}

func TestMessagesC42QuestionAnswerScheduleRecoveryUsesMessagesSnapshotAndDefaultEffort(t *testing.T) {
	execution := c2ScheduleExecutionFixture("accounts")
	execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "question", Name: "old name", Body: "old body", Keywords: []string{"old answer"}}}}
	execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", ExecutionID: execution.ID, TargetID: "sub2api:ws1:a", Status: "pending", AvailableModels: []string{"old-model"}, PlannedRequestCount: 1, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "ws1", InventoryComplete: true}, Protocol: TestProtocolMessages}}}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "new-group"}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture", AnthropicAuth: "x_api_key"}}}, allAccounts: []upstream.AdminGroupAccountInfo{{ID: "a", Models: "new-model"}}}
	qa := newFakeQuestionAnswerRepository()
	repo := &c2ScheduleRecoveryRepository{c2SchedulePreparationRepository: &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "new body", Keywords: []string{"new answer"}, Enabled: true}}, configs: []GroupTestConfig{{AdminGroupID: "new-group", Protocol: TestProtocolChatCompletions}}}, execution: execution, qa: qa}
	service, models := c2SchedulePreparationFixture(t, reader, repo.c2SchedulePreparationRepository)
	t.Cleanup(func() {
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service.questionAnswerSchedules = repo
	service.questionAnswers = qa
	service.accounts = fakeAdminAccountResolver{id: "ws2"}
	workspaces := make(chan string, 2)
	service.mySites = c2ScheduleRecoveryMySitesReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, workspaces: workspaces}
	type requestFact struct{ path, body string }
	requests := make(chan requestFact, 2)
	service.questionAnswerHTTP = &QuestionAnswerRunner{client: &http.Client{Transport: c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		requests <- requestFact{request.URL.Path, string(body)}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"old answer"}]}`)), Request: request}, nil
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
	if err := service.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handle.buildDone:
	case <-time.After(time.Second):
		t.Fatal("recovery did not finish building from frozen facts")
	}
	deadline := time.Now().Add(time.Second)
	var batch QuestionAnswerBatch
	for time.Now().Before(deadline) {
		var err error
		batch, err = service.getQuestionAnswerBatchCore(context.Background(), execution.UserID, execution.Targets[0].TargetID, "exact")
		if err == nil && !batch.Active {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if batch.Active || len(batch.Records) != 1 || batch.Records[0].RequestProtocol == nil || *batch.Records[0].RequestProtocol != TestProtocolMessages || batch.Records[0].Status != QuestionAnswerSucceeded || batch.Records[0].QuestionBody != "old body" || batch.Records[0].ModelName != "old-model" || batch.Records[0].AnswerJudgment == nil || *batch.Records[0].AnswerJudgment != QuestionAnswerCorrect {
		t.Fatalf("recovery lost frozen request/automatic judgment: %+v", batch)
	}
	if repo.reads.Load() != 0 || models.Load() != 0 || repo.creates.Load() != 1 || reader.credentialCalls.Load() != 1 || len(workspaces) != 1 || <-workspaces != "ws1" {
		t.Fatal("recovery reread current facts, rediscovered models, or used switched workspace")
	}
	select {
	case fact := <-requests:
		var payload struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal([]byte(fact.body), &payload); err != nil || payload.OutputConfig.Effort != "medium" {
			t.Fatalf("nil record effort did not use existing default: %s", fact.body)
		}
		if fact.path != "/v1/messages" || !strings.Contains(fact.body, "old body") || !strings.Contains(fact.body, "old-model") || strings.Contains(fact.body, "new body") || strings.Contains(fact.body, "new-model") || len(requests) != 0 {
			t.Fatalf("recovery request violated frozen protocol/model/question: %+v", fact)
		}
	default:
		t.Fatal("recovered request was not dispatched through C1")
	}
}

func TestMessagesC42QuestionAnswerBatchKeepsMessagesSnapshotAfterChatSave(t *testing.T) {
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
	health := newFakeRepository()
	configuration := GroupTestConfig{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 30}
	qa.configurations = []GroupTestConfig{configuration}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	reader := service.platformGroups.(fakePlatformGroupReader)
	credential := reader.credByAccount["acc-a"]
	credential.AnthropicAuth = "x_api_key"
	reader.credByAccount["acc-a"] = credential
	service.platformGroups = reader
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"model-a"}]}`
		if req.URL.Path != "/v1/models" {
			sends.Add(1)
			if req.URL.Path != "/v1/messages" {
				t.Errorf("batch protocol changed: %s", req.URL.Path)
			}
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) < 9*time.Minute {
				t.Error("probe timeout replaced ten-minute question cutoff")
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			body = `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	service.modelDiscovery.client.Transport = transport
	service.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := service.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, RepeatCount: json.RawMessage("6")})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("five slots were not dispatched")
		}
	}
	service.questionAnswerMu.Lock()
	run := service.questionAnswerRuns[questionAnswerRunKey("user1", "sub2api:ws1:acc-a")]
	service.questionAnswerMu.Unlock()
	if run == nil {
		t.Fatal("missing active batch")
	}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10}); err != nil {
		t.Fatal(err)
	}
	qa.mu.Lock()
	qa.configurations[0].Protocol = TestProtocolChatCompletions
	qa.mu.Unlock()
	close(release)
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("batch did not finish")
	}
	persisted, err := service.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 6 || persisted.Active || persisted.Stats.Requests.Succeeded != 6 {
		t.Fatalf("sends=%d batch=%+v", sends.Load(), persisted)
	}
	for _, record := range persisted.Records {
		if record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolMessages {
			t.Fatal("stored snapshot changed after save")
		}
	}
}

func TestMessagesC42QuestionAnswerMigrationPreservesOtherAndCompoundChecks(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	ctx := context.Background()
	migration, err := os.ReadFile("../../database/migrations/000039_connection_health_question_answer_protocol_messages.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A migration before the table exists is harmless and must not create it.
	for i := 0; i < 2; i++ {
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	var absent bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('connection_health_question_answer_records') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatalf("missing table was created: absent=%v err=%v", absent, err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE connection_health_question_answer_records (
 user_id text NOT NULL,
 request_protocol text NULL CHECK(request_protocol IS NULL OR request_protocol IN ('chat_completions','responses')),
 CONSTRAINT c42_compound CHECK (request_protocol <> 'chat_completions' OR user_id <> ''),
 CONSTRAINT c42_nonempty CHECK (request_protocol IS NULL OR length(request_protocol)>0)
 )`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(user_id,request_protocol) VALUES ('legacy','chat_completions'),('legacy','responses'),('legacy',NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	var retained, values, rows int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='connection_health_question_answer_records'::regclass AND conname IN ('c42_compound','c42_nonempty')`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='connection_health_question_answer_records'::regclass AND conname='connection_health_qa_records_request_protocol_values'`).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_question_answer_records WHERE user_id='legacy' AND (request_protocol IN ('chat_completions','responses') OR request_protocol IS NULL)`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if retained != 2 || values != 1 || rows != 3 {
		t.Fatalf("unrelated checks or existing rows changed: retained=%d values=%d rows=%d", retained, values, rows)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO connection_health_question_answer_records(user_id,request_protocol) VALUES('fixture','messages')`); err != nil {
		t.Fatalf("messages rejected: %v", err)
	}
	for _, query := range []string{`INSERT INTO connection_health_question_answer_records(user_id,request_protocol) VALUES('fixture','bogus')`, `INSERT INTO connection_health_question_answer_records(user_id,request_protocol) VALUES('','chat_completions')`, `INSERT INTO connection_health_question_answer_records(user_id,request_protocol) VALUES('fixture','')`} {
		if _, err = pool.Exec(ctx, query); err == nil {
			t.Fatalf("constraint no longer enforced: %s", query)
		}
	}
}

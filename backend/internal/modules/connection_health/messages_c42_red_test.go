package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const c42CompleteBody = `{"type":"message","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private thought"},{"type":"text","text":"  first answer  "},{"type":"text","text":" second answer \n"}]}`

func c42AssertRequest(t *testing.T, req *http.Request, model, prompt, effort string) {
	t.Helper()
	if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" {
		t.Errorf("Messages path/query=%s", req.URL.RequestURI())
	}
	if req.Header.Get("anthropic-version") != "2023-06-01" || req.Header.Get("x-api-key") != "fixture-key" || req.Header.Get("Authorization") != "" {
		t.Error("Messages headers differ from API Key health probe")
	}
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 5 || body["model"] != model || body["max_tokens"] != float64(64000) || body["stream"] != true {
		t.Errorf("five-field streaming request=%v", body)
	}
	config, ok := body["output_config"].(map[string]any)
	if !ok || len(config) != 1 || config["effort"] != effort {
		t.Errorf("output_config=%v want effort=%s", config, effort)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages=%v", body["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok || len(message) != 2 || message["role"] != "user" || message["content"] != prompt {
		t.Errorf("single user message=%v", message)
	}
}

func TestMessagesC42M2QuestionAnswerJudgesAllBodyTextWithoutThinking(t *testing.T) {
	c42RunPersistedAnswer(t, c42CompleteBody, "", "first answer\nsecond answer")
}

func TestMessagesC42M3QuestionAnswerIncompleteIsFailureWithoutJudgment(t *testing.T) {
	c42RunPersistedAnswer(t, `{"type":"message","stop_reason":"max_tokens","content":[{"type":"text","text":"partial answer"}]}`, "response_incomplete", "")
}

func c42RunPersistedAnswer(t *testing.T, body, wantError, wantAnswer string) {
	t.Helper()
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "C42 fake", Body: "fixture", Keywords: []string{"first", "second"}, Enabled: true})
	health := newFakeRepository()
	qa.configurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20}}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolMessages, 20}); err != nil {
		t.Fatal(err)
	}
	s := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.credByAccount["acc-a"] = upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}
	s.platformGroups = reader
	var sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		response := `{"data":[{"id":"model-a"}]}`
		if req.URL.Path != "/v1/models" {
			sends.Add(1)
			c42AssertRequest(t, req, "model-a", "fixture", "high")
			response = body
		}
		return protocolServiceResponse(req, 200, response), nil
	})
	s.modelDiscovery.client.Transport = transport
	s.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		if err := s.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, ReasoningEffort: string(QuestionAnswerReasoningEffortHigh)})
	if err != nil {
		t.Fatal(err)
	}
	waitQuestionAnswerRunReleased(t, s)
	persisted, err := s.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
	if err != nil || len(persisted.Records) != 1 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	record := persisted.Records[0]
	if sends.Load() != 1 || record.ErrorType != wantError || record.AnswerBody != wantAnswer {
		t.Fatalf("requests=%d record=%+v wantError=%s answer=%q", sends.Load(), record, wantError, wantAnswer)
	}
	if wantError != "" {
		if record.Status != QuestionAnswerFailed || record.AnswerJudgment != nil || record.JudgmentSource != nil || record.UpstreamStatus == nil || *record.UpstreamStatus != 200 || !strings.Contains(record.UpstreamExcerpt, "max_tokens") {
			t.Fatalf("failure leaked answer/judgment or lost upstream evidence: %+v", record)
		}
	} else if record.Status != QuestionAnswerSucceeded || record.AnswerJudgment == nil || *record.AnswerJudgment != QuestionAnswerCorrect || record.JudgmentSource == nil || *record.JudgmentSource != QuestionAnswerJudgmentAutomatic || strings.Contains(record.AnswerBody, "private thought") {
		t.Fatalf("body answer not judged correctly: %+v", record)
	}
}

func TestMessagesC42M5QuestionAnswerHTTPClassificationIsProtocolSpecific(t *testing.T) {
	for _, tc := range []struct {
		name       string
		protocol   TestProtocol
		status     int
		body, want string
	}{
		{"messages529", TestProtocolMessages, 529, `{"type":"error","error":{"type":"overloaded_error"}}`, "rate_limited"},
		{"messages404", TestProtocolMessages, 404, `{"type":"error","error":{"type":"not_found_error","message":"model: absent"}}`, "model_not_found"},
		{"chat529", TestProtocolChatCompletions, 529, `{}`, "server_error"},
		{"responses529", TestProtocolResponses, 529, `{}`, "server_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			runner := NewQuestionAnswerRunner()
			runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				return protocolServiceResponse(req, tc.status, tc.body), nil
			})
			out := runner.AskDetailed(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}, "m", "q", QuestionAnswerReasoningEffortHigh, tc.protocol)
			if calls != 1 || out.ErrorType != tc.want || out.Answer != "" || out.UpstreamStatus != tc.status || out.UpstreamExcerpt == "" {
				t.Fatalf("calls=%d outcome=%+v want=%s", calls, out, tc.want)
			}
		})
	}
}

func TestMessagesC42M6OAuthQuestionAnswerDoesNotSendAndFailsEachRecord(t *testing.T) {
	exports := 0
	client := &http.Client{Transport: protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		exports++
		if req.URL.Path != "/api/v1/admin/accounts/data" {
			t.Errorf("credential path=%s", req.URL.Path)
		}
		return protocolServiceResponse(req, 200, `{"code":0,"data":{"accounts":[{"id":1,"type":"oauth","credentials":{"access_token":"fixture-key","base_url":"https://fixture.invalid"}}]}}`), nil
	})}
	platform := upstream.NewPlatformService(upstream.NewHTTPClient(client))
	cred, err := platform.ResolveProbeCredentialContext(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "https://main.invalid", AccessToken: "fixture-token"}, upstream.AdminGroupAccountInfo{ID: "1"})
	if err != nil || exports != 1 || cred.Key == "" || cred.BaseURL == "" || cred.AnthropicAuth != "" {
		t.Fatalf("usable OAuth credential prerequisite failed: err=%v exports=%d", err, exports)
	}
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "C42 fake OAuth", Body: "fixture", Enabled: true})
	health := newFakeRepository()
	qa.configurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20}}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolMessages, 20}); err != nil {
		t.Fatal(err)
	}
	s := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.credByAccount["acc-a"] = cred
	s.platformGroups = reader
	var discoveries, sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/models" {
			discoveries.Add(1)
			return protocolServiceResponse(req, 200, `{"data":[{"id":"model-a"}]}`), nil
		}
		sends.Add(1)
		return protocolServiceResponse(req, 200, c42CompleteBody), nil
	})
	s.modelDiscovery.client.Transport = transport
	s.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		if err := s.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, RepeatCount: json.RawMessage("2")})
	if err != nil {
		t.Fatal(err)
	}
	waitQuestionAnswerRunReleased(t, s)
	persisted, err := s.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
	if err != nil || discoveries.Load() != 1 || sends.Load() != 0 || qa.createCalls != 1 || len(persisted.Records) != 2 {
		t.Fatalf("prerequisites/dispatch discovery=%d sends=%d records=%d err=%v", discoveries.Load(), sends.Load(), len(persisted.Records), err)
	}
	for _, record := range persisted.Records {
		if record.Status != QuestionAnswerFailed || record.ErrorType != "account_type_unsupported" || record.AnswerBody != "" || record.AnswerJudgment != nil || record.UpstreamStatus != nil || record.UpstreamExcerpt != "" {
			t.Errorf("unsupported account record=%+v", record)
		}
	}
}

// E2 retains the original six-item, five-slot test's business assertions, with
// only initial/saved protocols changed to Chat/Messages.
func TestMessagesC42E2QuestionAnswerBatchKeepsChatSnapshotAfterMessagesSave(t *testing.T) {
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
	health := newFakeRepository()
	qa.configurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 30}}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolChatCompletions, 30}); err != nil {
		t.Fatal(err)
	}
	s := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"model-a"}]}`
		if req.URL.Path != "/v1/models" {
			sends.Add(1)
			if req.URL.Path != "/v1/chat/completions" {
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
			body = `{"choices":[{"message":{"content":"answer"}}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	s.modelDiscovery.client.Transport = transport
	s.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if err := s.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, RepeatCount: json.RawMessage("6")})
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
	s.questionAnswerMu.Lock()
	run := s.questionAnswerRuns[questionAnswerRunKey("user1", "sub2api:ws1:acc-a")]
	s.questionAnswerMu.Unlock()
	if run == nil {
		t.Fatal("missing active batch")
	}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolMessages, 10}); err != nil {
		t.Fatal(err)
	}
	qa.mu.Lock()
	qa.configurations[0].Protocol = TestProtocolMessages
	qa.mu.Unlock()
	close(release)
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("batch did not finish")
	}
	persisted, err := s.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
	if err != nil || sends.Load() != 6 || persisted.Active || persisted.Stats.Requests.Succeeded != 6 {
		t.Fatalf("sends=%d batch=%+v err=%v", sends.Load(), persisted, err)
	}
	for _, record := range persisted.Records {
		if record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolChatCompletions {
			t.Fatal("stored snapshot changed after save")
		}
	}
}

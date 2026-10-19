package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// These business expectations are owned by the primary agent (PLAN 5.1).
func c44Frame(event string) string { return "data: " + event + "\n\n" }

func c44TextStream() string {
	return c44Frame(`{"type":"message_start","message":{"usage":{"output_tokens":1}}}`) +
		c44Frame(`{"type":"ping"}`) +
		c44Frame(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"不应保存的思考"}}`) +
		c44Frame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"不应保存的思考"}}`) +
		c44Frame(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"private-signature"}}`) +
		c44Frame(`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":" first answer "}}`) +
		c44Frame(`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`) +
		c44Frame(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":" second "}}`) +
		c44Frame(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"answer \n"}}`) +
		c44Frame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
		c44Frame(`{"type":"message_stop"}`)
}

func c44Service(t *testing.T, transport http.RoundTripper) (*Service, *fakeQuestionAnswerRepository) {
	t.Helper()
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "C44 fake", Body: "fixture", Keywords: []string{"first", "second"}, Enabled: true})
	health := newFakeRepository()
	qa.configurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolMessages, ProbeTimeoutSeconds: 20}}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolMessages, 20}); err != nil {
		t.Fatal(err)
	}
	s := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	reader := s.platformGroups.(fakePlatformGroupReader)
	reader.credByAccount["acc-a"] = upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture-key", AnthropicAuth: "x_api_key"}
	s.platformGroups = reader
	s.modelDiscovery.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		return protocolServiceResponse(req, 200, `{"data":[{"id":"model-a"}]}`), nil
	})
	s.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		if err := s.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s, qa
}

func c44Start(t *testing.T, s *Service) QuestionAnswerRecord {
	t.Helper()
	batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	waitQuestionAnswerRunReleased(t, s)
	persisted, err := s.GetQuestionAnswerBatch(context.Background(), "user1", "sub2api:ws1:acc-a", batch.BatchID)
	if err != nil || len(persisted.Records) != 1 {
		t.Fatalf("persisted records=%d err=%v", len(persisted.Records), err)
	}
	return persisted.Records[0]
}

func TestMessagesC44QuestionAnswerT1FiveFieldStreamingRequest(t *testing.T) {
	var sends atomic.Int32
	s, _ := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		sends.Add(1)
		if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" || req.Header.Get("anthropic-version") != "2023-06-01" || req.Header.Get("x-api-key") != "fixture-key" || req.Header.Get("Authorization") != "" {
			t.Error("Messages address or authentication changed")
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 5 || body["model"] != "model-a" || body["max_tokens"] != float64(64000) || body["stream"] != true {
			t.Errorf("request must have exactly five fields, 64000 tokens and stream=true: %v", body)
		}
		config, ok := body["output_config"].(map[string]any)
		if !ok || len(config) != 1 || config["effort"] != "high" {
			t.Errorf("output_config=%v", body["output_config"])
		}
		messages, _ := json.Marshal(body["messages"])
		if string(messages) != `[{"content":"fixture","role":"user"}]` {
			t.Errorf("user message=%s", messages)
		}
		return protocolServiceResponse(req, 200, c42CompleteBody), nil
	}))
	if record := c44Start(t, s); sends.Load() != 1 || record.Status != QuestionAnswerSucceeded {
		t.Fatalf("sends=%d status=%s", sends.Load(), record.Status)
	}
}

func TestMessagesC44QuestionAnswerT2ThroughT6PersistStreamResults(t *testing.T) {
	thought := c44Frame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"不应保存的思考"}}`)
	cases := []struct{ name, body, wantError, wantAnswer, wantExcerpt string }{
		{"T2_complete", c44TextStream(), "", "first answer\nsecond answer", ""},
		{"T3_incomplete_usage_does_not_erase_stop", thought + c44Frame(`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":64000}}`) + c44Frame(`{"type":"message_delta","delta":{},"usage":{"output_tokens":64001}}`) + c44Frame(`{"type":"message_stop"}`), "response_incomplete", "", "max_tokens"},
		{"T5_disconnected", c44Frame(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"partial answer"}}`), "network", "", ""},
		{"T6_long_thinking", strings.Repeat(c44Frame(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"`+strings.Repeat("x", 8192)+`"}}`), 384) + c44TextStream(), "", "first answer\nsecond answer", ""},
	}
	for _, kind := range []string{"overloaded_error", "rate_limit_error", "api_error"} {
		want := "rate_limited"
		if kind == "api_error" {
			want = "server_error"
		}
		cases = append(cases, struct{ name, body, wantError, wantAnswer, wantExcerpt string }{"T4_" + kind, thought + c44Frame(`{"type":"error","error":{"type":"`+kind+`","message":"具体上游说明"}}`), want, "", "具体上游说明"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sends atomic.Int32
			s, qa := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				sends.Add(1)
				response := protocolServiceResponse(req, 200, tc.body)
				response.Header.Set("Content-Type", "text/event-stream")
				return response, nil
			}))
			record := c44Start(t, s)
			if sends.Load() != 1 || record.ErrorType != tc.wantError || record.AnswerBody != tc.wantAnswer || record.UpstreamExcerpt != tc.wantExcerpt {
				t.Errorf("sends=%d status=%s error=%s answer=%q excerpt=%q; want error=%s answer=%q excerpt=%q", sends.Load(), record.Status, record.ErrorType, record.AnswerBody, record.UpstreamExcerpt, tc.wantError, tc.wantAnswer, tc.wantExcerpt)
			}
			if record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolMessages {
				t.Error("record lost Messages snapshot")
			}
			if tc.wantError != "" {
				if record.Status != QuestionAnswerFailed || record.AnswerJudgment != nil || record.JudgmentSource != nil || record.UpstreamStatus == nil || *record.UpstreamStatus != 200 {
					t.Errorf("failure must retain 200 with no answer judgment: status=%s code=%v judgment=%v", record.Status, record.UpstreamStatus, record.AnswerJudgment)
				}
			} else if record.Status != QuestionAnswerSucceeded || record.AnswerJudgment == nil || *record.AnswerJudgment != QuestionAnswerCorrect || record.JudgmentSource == nil || *record.JudgmentSource != QuestionAnswerJudgmentAutomatic {
				t.Errorf("complete body must be judged correct: status=%s judgment=%v source=%v", record.Status, record.AnswerJudgment, record.JudgmentSource)
			}
			qa.mu.Lock()
			stored, err := json.Marshal(qa.records)
			qa.mu.Unlock()
			if err != nil || strings.Contains(string(stored), "不应保存的思考") || strings.Contains(string(stored), "private-signature") {
				t.Error("thinking or signature persisted")
			}
		})
	}
}

func TestMessagesC44QuestionAnswerT7ProtocolSpecificDeadlines(t *testing.T) {
	deadlines := make(chan time.Duration, 2)
	s, qa := c44Service(t, protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Error("missing per-question deadline")
		}
		deadlines <- time.Until(deadline)
		if req.URL.Path == "/v1/messages" {
			return protocolServiceResponse(req, 200, c42CompleteBody), nil
		}
		return protocolServiceResponse(req, 200, `{"choices":[{"message":{"content":"first answer second answer"}}]}`), nil
	}))
	if r := c44Start(t, s); r.Status != QuestionAnswerSucceeded {
		t.Fatal("Messages prerequisite did not complete")
	}
	messagesRemaining := <-deadlines
	health := s.repo.(*fakeRepository)
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{TestProtocolChatCompletions, 20}); err != nil {
		t.Fatal(err)
	}
	qa.mu.Lock()
	qa.configurations[0].Protocol = TestProtocolChatCompletions
	qa.mu.Unlock()
	if r := c44Start(t, s); r.Status != QuestionAnswerSucceeded {
		t.Fatal("Chat prerequisite did not complete")
	}
	chatRemaining := <-deadlines
	if messagesRemaining < 19*time.Minute || messagesRemaining > 20*time.Minute {
		t.Errorf("Messages remaining=%s want 19–20 minutes", messagesRemaining)
	}
	if chatRemaining < 9*time.Minute || chatRemaining > 10*time.Minute {
		t.Errorf("Chat remaining=%s want 9–10 minutes", chatRemaining)
	}
}

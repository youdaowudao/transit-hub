package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestQuestionAnswerProtocolResponsesUsesFixedSnapshotAndStrictCompletion(t *testing.T) {
	for _, example := range []struct {
		name, body, want string
		status           int
	}{
		{"complete", `{"status":"completed","output":[{"type":"reasoning"},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"complete answer"}]}]}`, "", 200},
		{"refusal", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"partial"},{"type":"refusal"}]}]}`, QuestionAnswerErrorInvalidResponse, 200},
		{"incomplete", `{"status":"incomplete","output":[]}`, QuestionAnswerErrorInvalidResponse, 200},
		{"ambiguous404", `{"error":{"message":"missing"}}`, QuestionAnswerErrorInvalidResponse, 404},
		{"model404", `{"error":{"code":"model_not_found"}}`, QuestionAnswerErrorModelNotFound, 404},
	} {
		t.Run(example.name, func(t *testing.T) {
			calls := 0
			body := &trackingBody{Reader: strings.NewReader(example.body)}
			runner := NewQuestionAnswerRunner()
			runner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/v1/responses" {
					t.Errorf("path=%s", req.URL.Path)
				}
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["stream"] != false || payload["store"] != false {
					t.Errorf("nonstream/store fields=%v", payload)
				}
				reasoning, ok := payload["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != "xhigh" {
					t.Errorf("effort=%v", payload["reasoning"])
				}
				for _, key := range []string{"messages", "reasoning_effort", "max_tokens", "max_output_tokens", "previous_response_id"} {
					if _, exists := payload[key]; exists {
						t.Errorf("unexpected %s", key)
					}
				}
				return &http.Response{StatusCode: example.status, Body: body, Header: make(http.Header), Request: req}, nil
			})
			answer, errorType := runner.Ask(context.Background(), upstream.ProbeCredential{BaseURL: "https://fixture.invalid", Key: "fixture"}, "model", "question", QuestionAnswerReasoningEffortXHigh, TestProtocolResponses)
			if errorType != example.want {
				t.Fatalf("error=%s want %s", errorType, example.want)
			}
			if example.want == "" && answer != "complete answer" {
				t.Fatalf("answer=%q", answer)
			}
			if example.want != "" && answer != "" {
				t.Fatal("invalid response leaked into reviewable answer")
			}
			if calls != 1 || !body.closed {
				t.Fatalf("calls=%d closed=%v", calls, body.closed)
			}
		})
	}
}

type projectionQuestionAnswerRepository struct {
	*fakeQuestionAnswerRepository
	requested []string
	readErr   error
}

func (r *projectionQuestionAnswerRepository) ListQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string) ([]QuestionAnswerRecord, error) {
	r.requested = append(r.requested, batchID)
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.fakeQuestionAnswerRepository.ListQuestionAnswerBatch(ctx, userID, targetID, batchID)
}

func TestQuestionAnswerFinalizationNoRecordsRequiresShutdownAndReadFailureIsNotAbsence(t *testing.T) {
	base := newFakeQuestionAnswerRepository()
	repo := &projectionQuestionAnswerRepository{fakeQuestionAnswerRepository: base}
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", repo, newFakeRepository())
	service.initializeQuestionAnswerRuntime()
	const target = "sub2api:ws1:acc-a"
	ctx, cancel := context.WithCancel(context.Background())
	run := &activeQuestionAnswerBatch{userID: "user1", targetID: target, batchID: "unknown-commit", ctx: ctx, cancel: cancel, stopReason: QuestionAnswerErrorStorage, finalErr: errors.New("fixture"), done: make(chan struct{})}
	service.questionAnswerMu.Lock()
	service.questionAnswerRuns[questionAnswerRunKey("user1", target)] = run
	service.ensureQuestionAnswerFinalizationLocked(run)
	run.finalizeResults[run.finalizeGeneration] = run.finalErr
	close(run.finalizeSettled)
	service.questionAnswerMu.Unlock()
	t.Cleanup(func() {
		repo.readErr = nil
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := service.LatestQuestionAnswerBatch(context.Background(), "user1", target)
	if err != nil {
		t.Fatal(err)
	}
	if batch.BatchID != "" || batch.Active || len(batch.Records) != 0 {
		t.Fatal("fabricated batch for absent records")
	}
	if batch.Finalization == nil || batch.Finalization.Recovery != "service_shutdown" || batch.Finalization.BatchID != "unknown-commit" {
		t.Fatalf("projection=%+v", batch.Finalization)
	}
	if len(repo.requested) != 1 || repo.requested[0] != "unknown-commit" {
		t.Fatalf("did not read retained ID: %v", repo.requested)
	}
	repo.readErr = errors.New("fixture read unavailable")
	if _, err := service.LatestQuestionAnswerBatch(context.Background(), "user1", target); err == nil {
		t.Fatal("read failure treated as no records")
	}
	repo.readErr = nil
	if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, "unknown-commit"); err == nil {
		t.Fatal("no-record reservation incorrectly exposed via cancel")
	}
	service.questionAnswerMu.Lock()
	retained := service.questionAnswerRuns[questionAnswerRunKey("user1", target)]
	service.questionAnswerMu.Unlock()
	if retained == nil {
		t.Fatal("NotFound cancel released reservation")
	}
	if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.questionAnswerMu.Lock()
	remaining := len(service.questionAnswerRuns)
	service.questionAnswerMu.Unlock()
	if remaining != 0 {
		t.Fatal("shutdown did not release verified absent batch")
	}
}

func TestQuestionAnswerFinalizationLatestFindsCrossDayRunAndDoesNotAttachToHistory(t *testing.T) {
	base := newFakeQuestionAnswerRepository()
	now := time.Now()
	base.records = []QuestionAnswerRecord{
		{ID: "r1", TargetID: "sub2api:ws1:acc-a", BatchID: "yesterday", ModelName: "m", QuestionID: "q", Status: QuestionAnswerFailed, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "r2", TargetID: "sub2api:ws1:acc-a", BatchID: "other-history", ModelName: "m", QuestionID: "q", Status: QuestionAnswerFailed, CreatedAt: now},
	}
	repo := &projectionQuestionAnswerRepository{fakeQuestionAnswerRepository: base}
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", repo, newFakeRepository())
	service.initializeQuestionAnswerRuntime()
	const target = "sub2api:ws1:acc-a"
	ctx, cancel := context.WithCancel(context.Background())
	run := &activeQuestionAnswerBatch{userID: "user1", targetID: target, batchID: "yesterday", ctx: ctx, cancel: cancel, stopReason: QuestionAnswerErrorStorage, finalErr: errors.New("fixture"), done: make(chan struct{})}
	service.questionAnswerMu.Lock()
	service.questionAnswerRuns[questionAnswerRunKey("user1", target)] = run
	service.ensureQuestionAnswerFinalizationLocked(run)
	run.finalizeResults[run.finalizeGeneration] = run.finalErr
	close(run.finalizeSettled)
	service.questionAnswerMu.Unlock()
	t.Cleanup(func() {
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	batch, err := service.LatestQuestionAnswerBatch(context.Background(), "user1", target)
	if err != nil {
		t.Fatal(err)
	}
	if batch.BatchID != "yesterday" || batch.Finalization == nil || batch.Finalization.Recovery != "cancel" {
		t.Fatalf("lost cross-day run: %+v", batch)
	}
	history, err := service.GetQuestionAnswerBatch(context.Background(), "user1", target, "other-history")
	if err != nil {
		t.Fatal(err)
	}
	if history.Finalization != nil {
		t.Fatal("retained cleanup attached to another history batch")
	}
}

func TestQuestionAnswerProtocolBatchKeepsSnapshotAfterConfigurationSave(t *testing.T) {
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
	health := newFakeRepository()
	configuration := GroupTestConfig{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}
	qa.configurations = []GroupTestConfig{configuration}
	if err := health.SaveGroupTestConfiguration(context.Background(), "user1", "ws1", "g1", &GroupTestConfiguration{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	var sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"model-a"}]}`
		if req.URL.Path != "/v1/models" {
			sends.Add(1)
			if req.URL.Path != "/v1/responses" {
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
			body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}]}`
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
		if record.RequestProtocol == nil || *record.RequestProtocol != TestProtocolResponses {
			t.Fatal("stored snapshot changed after save")
		}
	}
}

func TestQuestionAnswerProtocolConflictBlocksBeforeDiscoveryAndNormalTargetContinues(t *testing.T) {
	ctx := context.Background()
	qa := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
	health := newFakeRepository()
	for group, configuration := range map[string]GroupTestConfiguration{
		"g1": {Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30},
		"g2": {Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10},
	} {
		if err := health.SaveGroupTestConfiguration(ctx, "user1", "ws1", group, &configuration); err != nil {
			t.Fatal(err)
		}
		qa.configurations = append(qa.configurations, GroupTestConfig{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: group, Protocol: configuration.Protocol, ProbeTimeoutSeconds: configuration.ProbeTimeoutSeconds})
	}
	service := newMultiTargetQuestionAnswerService("https://fixture.invalid", qa, health)
	reader := service.platformGroups.(fakePlatformGroupReader)
	reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g2", Name: "timeout-conflict"})
	reader.accountsByGrp["g2"] = []upstream.AdminGroupAccountInfo{{ID: "acc-a", Name: "account-a", Models: "model-a"}}
	service.platformGroups = reader
	var discoveries, sends atomic.Int32
	transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"model-a"}]}`
		if req.URL.Path == "/v1/models" {
			discoveries.Add(1)
		} else {
			sends.Add(1)
			if req.URL.Path != "/v1/responses" {
				t.Errorf("normal target did not inherit Responses: %s", req.URL.Path)
			}
			body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	service.modelDiscovery.client.Transport = transport
	service.questionAnswerHTTP.client.Transport = transport
	t.Cleanup(func() {
		if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
			t.Error(err)
		}
	})
	input := QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}}
	if _, err := service.StartQuestionAnswerBatch(ctx, "user1", "sub2api:ws1:acc-a", input); err == nil || !strings.Contains(err.Error(), ErrorTestConfigurationConflict) {
		t.Fatalf("expected timeout-only configuration conflict, got %v", err)
	}
	service.questionAnswerMu.Lock()
	reserved := len(service.questionAnswerRuns)
	service.questionAnswerMu.Unlock()
	if discoveries.Load() != 0 || sends.Load() != 0 || qa.createCalls != 0 || len(qa.records) != 0 || reserved != 0 || len(health.budgetClaims) != 0 {
		t.Fatalf("conflict had effects: discovery=%d sends=%d creates=%d records=%d reservations=%d budget=%v", discoveries.Load(), sends.Load(), qa.createCalls, len(qa.records), reserved, health.budgetClaims)
	}
	batch, err := service.StartQuestionAnswerBatch(ctx, "user1", "sub2api:ws1:acc-b", input)
	if err != nil {
		t.Fatal(err)
	}
	waitQuestionAnswerRunReleased(t, service)
	persisted, err := service.GetQuestionAnswerBatch(ctx, "user1", "sub2api:ws1:acc-b", batch.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if discoveries.Load() != 1 || sends.Load() != 1 || persisted.Stats.Requests.Succeeded != 1 || len(health.budgetClaims) != 0 || len(health.events) != 0 || len(health.states) != 0 {
		t.Fatalf("normal QA target did not remain isolated: discovery=%d sends=%d batch=%+v budget=%v events=%d states=%d", discoveries.Load(), sends.Load(), persisted, health.budgetClaims, len(health.events), len(health.states))
	}
}

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
)

type protocolFinalizationRepository struct {
	*fakeQuestionAnswerRepository
	finalizeFails atomic.Bool
}

func (r *protocolFinalizationRepository) CreateQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string, models, questionIDs []string, effort QuestionAnswerReasoningEffort, repeat int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error) {
	_, err := r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, userID, targetID, batchID, models, questionIDs, effort, repeat, snapshots...)
	if err != nil {
		return nil, err
	}
	return nil, &uncertainQuestionAnswerCreateError{cause: errors.New("fixture commit response lost")}
}
func (r *protocolFinalizationRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	if r.finalizeFails.Load() {
		return true, errors.New("fixture Finalize failure")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

// T23 checks actual records, stopped dispatch and retained retry ownership.
// Active runs now have only the final batch-ending operation: there is no
// earlier StopPending write whose success can make rows terminal first.
func TestProtocolContractQuestionAnswerFinalizationProjection(t *testing.T) {
	for _, entry := range []string{"start", "cancel"} {
		for _, fail := range []bool{false, true} {
			name := "finalize_succeeds"
			if fail {
				name = "finalize_fails"
			}
			t.Run(entry+"/"+name, func(t *testing.T) {
				base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
				repo := &protocolFinalizationRepository{fakeQuestionAnswerRepository: base}
				// A cancel needs an owned failed start to retry; its actual final
				// attempt is configured only after the uncertain create returns.
				repo.finalizeFails.Store(fail || entry == "cancel")
				service := newMultiTargetQuestionAnswerService("https://fixture.invalid", repo, newFakeRepository())
				var sends atomic.Int32
				transport := protocolContractTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path != "/v1/models" {
						sends.Add(1)
						return nil, errors.New("unexpected model HTTP")
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-a"}]}`)), Request: r}, nil
				})
				service.modelDiscovery.client.Transport = transport
				service.questionAnswerHTTP.client.Transport = transport
				t.Cleanup(func() {
					repo.finalizeFails.Store(false)
					if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
						t.Errorf("shutdown cleanup: %v", err)
					}
				})
				const target = "sub2api:ws1:acc-a"
				_, startErr := service.StartQuestionAnswerBatch(context.Background(), "user1", target, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}})
				var uncertain *uncertainQuestionAnswerCreateError
				if !errors.As(startErr, &uncertain) || !strings.Contains(startErr.Error(), "fixture commit response lost") {
					t.Fatalf("uncertain commit cause must remain visible: %v", startErr)
				}
				base.mu.Lock()
				batchID := base.records[0].BatchID
				base.mu.Unlock()
				if entry == "cancel" {
					repo.finalizeFails.Store(fail)
					_, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batchID)
					if (err != nil) != fail {
						t.Fatalf("cancel finalization error=%v fail=%v", err, fail)
					}
				}
				latest, err := service.LatestQuestionAnswerBatch(context.Background(), "user1", target)
				if err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(latest)
				var projected struct {
					Finalization *struct {
						BatchID  string `json:"batchId"`
						Recovery string `json:"recovery"`
					} `json:"finalization"`
				}
				if err := json.Unmarshal(payload, &projected); err != nil {
					t.Fatal(err)
				}
				service.questionAnswerMu.Lock()
				run := service.questionAnswerRuns[questionAnswerRunKey("user1", target)]
				service.questionAnswerMu.Unlock()
				if fail {
					if run == nil || projected.Finalization == nil || projected.Finalization.BatchID != batchID || projected.Finalization.Recovery != "cancel" {
						t.Fatalf("failed ending lost visible same-batch retry: run=%v finalization=%+v", run != nil, projected.Finalization)
					}
					if len(latest.Records) != 1 || latest.Records[0].Status != QuestionAnswerPending {
						t.Fatalf("failed final ending must retain unselected pending row: %+v", latest.Records)
					}
					repo.finalizeFails.Store(false)
					if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batchID); err != nil {
						t.Fatalf("explicit recovery: %v", err)
					}
				} else if run != nil || projected.Finalization != nil {
					t.Error("successful final ending must release reservation")
				}
				records, err := base.ListQuestionAnswerBatch(context.Background(), "user1", target, batchID)
				if err != nil || len(records) != 1 || records[0].Status != QuestionAnswerFailed {
					t.Fatalf("recovery did not end the original record: %+v err=%v", records, err)
				}
				if sends.Load() != 0 {
					t.Errorf("uncertain create or recovery dispatched model HTTP=%d", sends.Load())
				}
				service.questionAnswerMu.Lock()
				remaining := len(service.questionAnswerRuns)
				service.questionAnswerMu.Unlock()
				if remaining != 0 {
					t.Error("successful finalization must release reservation")
				}
			})
		}
	}
}

// T03/T04: the RED baseline used the existing hardcoded request builder.
// This adapter now calls the production resolver; contract assertions below
// remain unchanged. Full service/permission/persistence coverage is separate.
func protocolContractResolveBaseline(t *testing.T, memberships []string, configs map[string]struct {
	Protocol string
	Timeout  int
}) (string, int, string) {
	t.Helper()
	groups := make([]TestConfigurationSource, 0, len(memberships))
	for _, id := range memberships {
		groups = append(groups, TestConfigurationSource{AdminGroupID: id, AdminGroupName: "display-" + id})
	}
	rules := make([]GroupTestConfig, 0, len(configs))
	for id, c := range configs {
		rules = append(rules, GroupTestConfig{AdminGroupID: id, Protocol: TestProtocol(c.Protocol), ProbeTimeoutSeconds: c.Timeout})
	}
	result := ResolveGroupTestConfiguration("sub2api", groups, true, rules)
	return string(result.Protocol), result.ProbeTimeoutSeconds, result.Status
}

func TestProtocolContractDynamicGroupInheritance(t *testing.T) {
	config := map[string]struct {
		Protocol string
		Timeout  int
	}{"g2": {"responses", 30}}
	for _, name := range []string{"existing_account", "new_account_after_refresh", "renamed_group"} {
		t.Run(name, func(t *testing.T) {
			p, timeout, status := protocolContractResolveBaseline(t, []string{"g1", "g2"}, config)
			if p != "responses" || timeout != 30 || status != "inherited" {
				t.Errorf("runtime group inheritance=%s/%d/%s, want responses/30/inherited", p, timeout, status)
			}
		})
	}
	config["g1"] = struct {
		Protocol string
		Timeout  int
	}{"responses", 20}
	_, _, status := protocolContractResolveBaseline(t, []string{"g1", "g2"}, config)
	if status != "conflict" {
		t.Errorf("timeout-only conflict fell through: %s", status)
	}
	delete(config, "g1")
	delete(config, "g2")
	p, timeout, status := protocolContractResolveBaseline(t, []string{"g1", "g2"}, config)
	if p != "chat_completions" || timeout != 20 || status != "default" {
		t.Error("clear all explicit configurations must restore main-site default")
	}
}

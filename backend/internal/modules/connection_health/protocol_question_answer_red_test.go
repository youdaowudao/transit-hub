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
	stopFails     atomic.Bool
	finalizeFails atomic.Bool
}

func (r *protocolFinalizationRepository) CreateQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string, models, questionIDs []string, effort QuestionAnswerReasoningEffort, repeat int, snapshots ...QuestionAnswerConfigurationSnapshot) ([]QuestionAnswerRecord, error) {
	_, err := r.fakeQuestionAnswerRepository.CreateQuestionAnswerBatch(ctx, userID, targetID, batchID, models, questionIDs, effort, repeat, snapshots...)
	if err != nil {
		return nil, err
	}
	return nil, &uncertainQuestionAnswerCreateError{cause: errors.New("fixture commit response lost")}
}
func (r *protocolFinalizationRepository) StopPendingQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	if r.stopFails.Load() {
		return true, errors.New("fixture StopPending failure")
	}
	return r.fakeQuestionAnswerRepository.StopPendingQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}
func (r *protocolFinalizationRepository) FinalizeQuestionAnswerBatch(ctx context.Context, userID, targetID, batchID string, status QuestionAnswerStatus, errorType string) (bool, error) {
	if r.finalizeFails.Load() {
		return true, errors.New("fixture Finalize failure")
	}
	return r.fakeQuestionAnswerRepository.FinalizeQuestionAnswerBatch(ctx, userID, targetID, batchID, status, errorType)
}

// T23 checks actual repository records and retained run, not the HTTP error.
// The same three error combinations apply to failed starts and explicit cancels.
func TestProtocolContractQuestionAnswerFinalizationProjection(t *testing.T) {
	for _, entry := range []string{"start", "cancel"} {
		for _, combo := range []struct {
			name        string
			stop, final bool
		}{
			{"both_fail", true, true}, {"only_finalize_fails", false, true}, {"only_stop_fails", true, false},
		} {
			t.Run(entry+"/"+combo.name, func(t *testing.T) {
				base := newFakeQuestionAnswerRepository(TestQuestion{ID: "q", Name: "Q", Body: "fixture", Enabled: true})
				repo := &protocolFinalizationRepository{fakeQuestionAnswerRepository: base}
				repo.stopFails.Store(combo.stop)
				repo.finalizeFails.Store(combo.final)
				if entry == "cancel" {
					repo.stopFails.Store(true)
					repo.finalizeFails.Store(true)
				}
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
					repo.stopFails.Store(false)
					repo.finalizeFails.Store(false)
					if err := service.ShutdownQuestionAnswers(context.Background()); err != nil {
						t.Errorf("shutdown cleanup: %v", err)
					}
				})
				const target = "sub2api:ws1:acc-a"
				_, startErr := service.StartQuestionAnswerBatch(context.Background(), "user1", target, QuestionAnswerStartInput{Models: []string{"model-a"}, QuestionIDs: []string{"q"}})
				if startErr == nil {
					t.Fatal("uncertain commit must not publish a successful start")
				}
				base.mu.Lock()
				batchID := base.records[0].BatchID
				base.mu.Unlock()
				if entry == "cancel" {
					repo.stopFails.Store(combo.stop)
					repo.finalizeFails.Store(combo.final)
					if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batchID); err == nil {
						t.Error("injected cancel error must remain visible")
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
				if combo.final {
					if run == nil {
						t.Error("failed finalization released duplicate-prevention reservation")
					}
					if projected.Finalization == nil || projected.Finalization.BatchID != batchID || projected.Finalization.Recovery != "cancel" {
						t.Errorf("reopened page cannot find actual cleanup retry: finalization=%+v active=%v", projected.Finalization, latest.Active)
					}
					if !combo.stop && latest.Active {
						t.Error("StopPending succeeded: records must be terminal even while reservation remains")
					}
					repo.stopFails.Store(false)
					repo.finalizeFails.Store(false)
					if _, err := service.StopQuestionAnswerBatch(context.Background(), "user1", target, batchID); err != nil {
						t.Errorf("explicit recovery: %v", err)
					}
				} else if run != nil || projected.Finalization != nil {
					t.Error("Finalize succeeded: merged error must not imply a retained reservation")
				}
				if sends.Load() != 0 {
					t.Errorf("uncertain commit dispatched model HTTP=%d", sends.Load())
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

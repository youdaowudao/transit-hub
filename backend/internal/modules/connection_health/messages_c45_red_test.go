package connection_health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const c45Target = "sub2api:ws1:normal"

// This complete inventory is shared by the default, negative and frozen tests.
// No HTTP listener, external service or background scheduler is started.
func c45Fixture(t *testing.T) (*Service, *fakeRepository, *c2ScheduleInventoryReader) {
	t.Helper()
	s, repo := protocolServiceFixture(t)
	on := true
	account := upstream.AdminGroupAccountInfo{ID: "normal", Name: "C45 account", Platform: "anthropic", Type: "apikey", Models: "gpt-4o", Status: "active", Schedulable: &on, ModelMappingKnown: true}
	reader := &c2ScheduleInventoryReader{fakePlatformGroupReader: fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "C45 group", Platform: "anthropic"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}},
		credByAccount: map[string]upstream.ProbeCredential{"normal": {BaseURL: "https://fixture.invalid", Key: "fixture", AnthropicAuth: "x_api_key"}},
	}, allAccounts: []upstream.AdminGroupAccountInfo{account}}
	s.platformGroups = reader
	s.questionAnswers = newFakeQuestionAnswerRepository(TestQuestion{ID: "question", Name: "C45", Body: "fixture question", Keywords: []string{"ok"}, Enabled: true})
	s.questionAnswerHTTP = NewQuestionAnswerRunner()
	s.modelDiscovery = NewModelDiscoveryRunner()
	s.questionAnswerTTL = QuestionAnswerRequestTimeout
	s.initializeQuestionAnswerRuntime()
	s.modelDiscovery.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
		return protocolServiceResponse(req, 200, `{"data":[{"id":"gpt-4o"}]}`), nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.ShutdownQuestionAnswers(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, repo, reader
}

func c45Configuration(t *testing.T, s *Service, group string) EffectiveTestConfiguration {
	t.Helper()
	read, err := s.GetAdminGroupTestConfiguration(context.Background(), "user1", group)
	if err != nil || !read.InventoryComplete || len(read.Accounts) != 1 {
		t.Fatalf("configuration read prerequisite: %+v err=%v", read, err)
	}
	return read.Accounts[0].TestConfiguration
}

func c45AssertConfiguration(t *testing.T, got EffectiveTestConfiguration, protocol TestProtocol, seconds int, status string) {
	t.Helper()
	if got.Protocol != protocol || got.ProbeTimeoutSeconds != seconds || got.Status != status || got.BlockedReason != "" {
		t.Errorf("configuration=%+v want=%s/%d/%s", got, protocol, seconds, status)
	}
}

func c45Job(t *testing.T, s *Service, repo *fakeRepository) adminProbeJob {
	t.Helper()
	for _, job := range s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments) {
		if job.target.TargetID == c45Target {
			return job
		}
	}
	t.Fatal("eligible target was not scheduled")
	return adminProbeJob{}
}

func TestMessagesC45K1DefaultAgreesAcrossSixConfigurationEntries(t *testing.T) {
	for _, entry := range []string{"configuration-read", "group-health", "automatic", "single-refresh", "priority", "schedule-preparation"} {
		t.Run(entry, func(t *testing.T) {
			s, repo, reader := c45Fixture(t)
			var got EffectiveTestConfiguration
			switch entry {
			case "configuration-read":
				got = c45Configuration(t, s, "g1")
			case "group-health":
				groups, err := s.AdminGroups(context.Background(), "user1")
				if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
					t.Fatalf("group inventory prerequisite: %+v %v", groups, err)
				}
				got = groups[0].Accounts[0].TestConfiguration
			case "automatic":
				got = c45Job(t, s, repo).target.TestConfiguration
			case "single-refresh":
				_, target, _, _, err := s.resolveManualTarget(context.Background(), "user1", c45Target)
				if err != nil {
					t.Fatal(err)
				}
				got = target.TestConfiguration
			case "priority":
				snapshot, err := s.loadAdminInventory(context.Background(), "user1", "ws1", make(adminInventoryCache))
				if err != nil {
					t.Fatal(err)
				}
				inventory, complete, err := s.priorityInventoryForSnapshot(snapshot, "ws1", map[string][]Policy{c45Target: repo.policies}, nil, nil, nil, upstreamMultiplierLookup{})
				if err != nil || !complete || inventory[c45Target] == nil {
					t.Fatalf("priority inventory prerequisite: complete=%v err=%v", complete, err)
				}
				s.syncWorkspacePriorities(context.Background(), snapshot.session, "user1", "ws1", inventory, complete, nil, nil)
				got = inventory[c45Target].target.TestConfiguration
			case "schedule-preparation":
				prepRepo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "fixture question", Enabled: true}}}
				service, _ := c2SchedulePreparationFixture(t, reader, prepRepo)
				execution := c2ScheduleExecutionFixture("groups")
				execution.ConfigSnapshot.Requested.Schedule.SelectedGroupIDs = []string{"g1"}
				execution.ConfigSnapshot.Requested.Schedule.Models = []string{"available"}
				prep := service.resolveQuestionAnswerScheduleExecution(context.Background(), execution)
				defer func() {
					for _, reservation := range prep.reservations {
						service.releaseQuestionAnswerTargetStart(reservation)
					}
				}()
				if len(prep.outcome.Targets) != 1 || prep.outcome.Targets[0].Status != "pending" || prep.outcome.Resolved == nil {
					t.Fatalf("schedule preparation prerequisite: %+v", prep.outcome)
				}
				snapshot := prep.outcome.Targets[0].TestConfigurationSnapshot
				got = ResolveGroupTestConfiguration("sub2api", snapshot.Memberships, snapshot.InventoryComplete, nil)
				if snapshot.Protocol != TestProtocolMessages {
					t.Errorf("frozen schedule protocol=%s want Messages", snapshot.Protocol)
				}
			}
			c45AssertConfiguration(t, got, TestProtocolMessages, 20, "default")
		})
	}
}

func c45ProbeResponse(req *http.Request) *http.Response {
	body := `{"choices":[{"message":{"content":"ok"}}]}`
	if req.URL.Path == "/v1/messages" {
		body = `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`
	}
	return protocolServiceResponse(req, 200, body)
}

func TestMessagesC45K2DefaultSendsMessagesAndCreatesMessagesRecords(t *testing.T) {
	for _, entry := range []string{"automatic", "once", "manual-question-answer"} {
		t.Run(entry, func(t *testing.T) {
			s, repo, _ := c45Fixture(t)
			var sends atomic.Int32
			transport := protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				sends.Add(1)
				if req.URL.Path != "/v1/messages" || req.URL.RawQuery != "beta=true" {
					t.Errorf("request=%s want=/v1/messages?beta=true", req.URL.RequestURI())
				}
				return c45ProbeResponse(req), nil
			})
			s.probeRunner.client.Transport = transport
			s.questionAnswerHTTP.client.Transport = transport
			switch entry {
			case "automatic":
				runProtocolServiceJob(s, c45Job(t, s, repo))
			case "once":
				out, err := s.ManualProbeTarget(context.Background(), "user1", c45Target, []string{"gpt-4o"})
				if err != nil || len(out) != 1 || !out[0].Healthy {
					t.Fatalf("once result=%+v err=%v", out, err)
				}
			case "manual-question-answer":
				batch, err := s.StartQuestionAnswerBatch(context.Background(), "user1", c45Target, QuestionAnswerStartInput{Models: []string{"gpt-4o"}, QuestionIDs: []string{"question"}, RepeatCount: json.RawMessage(`1`)})
				if err != nil {
					t.Fatal(err)
				}
				waitQuestionAnswerRunReleased(t, s)
				read, err := s.GetQuestionAnswerBatch(context.Background(), "user1", c45Target, batch.BatchID)
				if err != nil || len(read.Records) != 1 || read.Records[0].Status != QuestionAnswerSucceeded {
					t.Fatalf("persisted question answer=%+v err=%v", read, err)
				}
				if read.Records[0].RequestProtocol == nil || *read.Records[0].RequestProtocol != TestProtocolMessages {
					t.Errorf("record protocol=%v want Messages", read.Records[0].RequestProtocol)
				}
			}
			if sends.Load() != 1 {
				t.Errorf("requests=%d want exactly 1", sends.Load())
			}
		})
	}
}

func TestMessagesC45K3IneligibleAccountsKeepChatConfigurationAndRequests(t *testing.T) {
	for _, reason := range []string{"oauth", "account-openai", "account-empty", "mixed-membership", "group-openai"} {
		t.Run(reason, func(t *testing.T) {
			s, repo, reader := c45Fixture(t)
			account := reader.accountsByGrp["g1"][0]
			switch reason {
			case "oauth":
				account.Type = "oauth"
			case "account-openai":
				account.Platform = "openai"
			case "account-empty":
				account.Platform = ""
			case "mixed-membership":
				reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g2", Name: "other", Platform: "openai"})
				reader.accountsByGrp["g2"] = []upstream.AdminGroupAccountInfo{account}
			case "group-openai":
				reader.groups[0].Platform = "openai"
			}
			reader.accountsByGrp["g1"][0] = account
			c45AssertConfiguration(t, c45Configuration(t, s, "g1"), TestProtocolChatCompletions, 20, "default")
			job := c45Job(t, s, repo)
			c45AssertConfiguration(t, job.target.TestConfiguration, TestProtocolChatCompletions, 20, "default")
			calls := 0
			s.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/v1/chat/completions" {
					t.Errorf("negative case sent=%s", req.URL.Path)
				}
				return c45ProbeResponse(req), nil
			})
			runProtocolServiceJob(s, job)
			if calls != 1 {
				t.Fatalf("requests=%d want=1", calls)
			}
		})
	}
}

func TestMessagesC45K4ExplicitInheritanceAndConflictsArePreserved(t *testing.T) {
	for _, example := range []struct {
		name                 string
		protocol             TestProtocol
		two, conflict, oauth bool
	}{
		{"chat", TestProtocolChatCompletions, false, false, false},
		{"responses", TestProtocolResponses, false, false, false},
		{"configured-and-default", TestProtocolChatCompletions, true, false, false},
		{"conflict", TestProtocolChatCompletions, true, true, false},
		{"oauth-explicit-messages", TestProtocolMessages, false, false, true},
	} {
		t.Run(example.name, func(t *testing.T) {
			s, repo, reader := c45Fixture(t)
			if example.oauth {
				reader.accountsByGrp["g1"][0].Type = "oauth"
			}
			if example.two {
				reader.groups = append(reader.groups, upstream.AdminGroupInfo{ID: "g2", Platform: "anthropic"})
				reader.accountsByGrp["g2"] = append([]upstream.AdminGroupAccountInfo{}, reader.accountsByGrp["g1"]...)
			}
			saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{example.protocol, 20})
			if example.conflict {
				saveProtocolServiceConfig(t, repo, "g2", &GroupTestConfiguration{TestProtocolResponses, 20})
			}
			got := c45Configuration(t, s, "g1")
			if example.conflict {
				if got.Status != "conflict" || got.Protocol != "" || got.ProbeTimeoutSeconds != 0 || got.BlockedReason != ErrorTestConfigurationConflict {
					t.Fatalf("conflict=%+v", got)
				}
			} else {
				c45AssertConfiguration(t, got, example.protocol, 20, "inherited")
			}
		})
	}
}

func TestMessagesC45K5OldMembershipJSONAndOppositeFrozenProtocols(t *testing.T) {
	t.Run("old-membership-json", func(t *testing.T) {
		_, _, reader := c45Fixture(t)
		if reader.groups[0].Platform != "anthropic" || reader.accountsByGrp["g1"][0].Platform != "anthropic" || reader.accountsByGrp["g1"][0].Type != "apikey" {
			t.Fatal("eligible live account prerequisite")
		}
		var memberships []TestConfigurationSource
		if err := json.Unmarshal([]byte(`[{"adminGroupId":"g1","adminGroupName":"C45 group"}]`), &memberships); err != nil {
			t.Fatal(err)
		}
		c45AssertConfiguration(t, ResolveGroupTestConfiguration("sub2api", memberships, true, nil), TestProtocolChatCompletions, 20, "default")
	})
	for _, frozen := range []TestProtocol{TestProtocolChatCompletions, TestProtocolMessages} {
		t.Run("frozen-"+string(frozen), func(t *testing.T) {
			_, _, reader := c45Fixture(t)
			qa := newFakeQuestionAnswerRepository()
			configs := []GroupTestConfig{}
			if frozen == TestProtocolMessages {
				configs = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 20}}
			}
			// K1 establishes Messages for this same live default inventory after
			// the change. Frozen behavior itself must also pass on the baseline.
			execution := c2ScheduleExecutionFixture("accounts")
			execution.ConfigSnapshot.Resolved = &QuestionAnswerScheduleResolved{Questions: []TestQuestion{{ID: "question", Body: "frozen body", Keywords: []string{"ok"}}}}
			execution.Targets = []QuestionAnswerScheduleExecutionTarget{{ID: "exact", ExecutionID: execution.ID, TargetID: c45Target, Status: "pending", AvailableModels: []string{"frozen-model"}, PlannedRequestCount: 1, TestConfigurationSnapshot: QuestionAnswerScheduleTargetConfiguration{QuestionAnswerConfigurationSnapshot: QuestionAnswerConfigurationSnapshot{AdminAccountID: "ws1", InventoryComplete: true}, Protocol: frozen}}}
			prepRepo := &c2SchedulePreparationRepository{questions: []TestQuestion{{ID: "question", Body: "new body", Enabled: true}}, configs: configs}
			repo := &c2ScheduleRecoveryRepository{c2SchedulePreparationRepository: prepRepo, execution: execution, qa: qa}
			s, models := c2SchedulePreparationFixture(t, reader, prepRepo)
			s.questionAnswerSchedules, s.questionAnswers = repo, qa
			s.questionAnswerHTTP = NewQuestionAnswerRunner()
			var sends atomic.Int32
			s.questionAnswerHTTP.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
				sends.Add(1)
				want := "/v1/chat/completions"
				if frozen == TestProtocolMessages {
					want = "/v1/messages"
				}
				if req.URL.Path != want {
					t.Errorf("frozen request=%s want=%s", req.URL.Path, want)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil || !strings.Contains(string(body), "frozen body") || !strings.Contains(string(body), "frozen-model") || strings.Contains(string(body), "new body") {
					t.Error("recovery used current question/model")
				}
				return c45ProbeResponse(req), nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handle := &questionAnswerScheduleHandle{identity: QuestionAnswerScheduleHandleIdentity{ExecutionID: execution.ID}, execution: execution, ctx: ctx, cancel: cancel, batchIDs: map[string]string{}, targetFailures: map[string]questionAnswerScheduleTargetFailure{}}
			if err := s.reconcileQuestionAnswerScheduleHandle(context.Background(), handle, execution); err != nil {
				t.Fatal(err)
			}
			select {
			case <-handle.buildDone:
			case <-time.After(time.Second):
				t.Fatal("recovery did not finish building")
			}
			waitQuestionAnswerRunReleased(t, s)
			batch, err := s.getQuestionAnswerBatchCore(context.Background(), "user1", c45Target, "exact")
			if err != nil || batch.Active || len(batch.Records) != 1 || batch.Records[0].RequestProtocol == nil || *batch.Records[0].RequestProtocol != frozen || batch.Records[0].Status != QuestionAnswerSucceeded {
				t.Fatalf("frozen record=%+v err=%v", batch, err)
			}
			if sends.Load() != 1 || repo.creates.Load() != 1 || repo.reads.Load() != 0 || models.Load() != 0 {
				t.Fatalf("sends=%d creates=%d current-config-reads=%d models=%d", sends.Load(), repo.creates.Load(), repo.reads.Load(), models.Load())
			}
		})
	}
}

func TestMessagesC45K6NewAPIDefaultStaysChatTenSeconds(t *testing.T) {
	s, repo, reader := c45Fixture(t)
	// NewAPI availability uses the account URL rather than exported credentials.
	reader.accountsByGrp["g1"][0].BaseURL = "https://fixture.invalid"
	s.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	for i := range repo.assignments {
		repo.assignments[i].TargetID = "newapi:ws1:normal"
	}
	groups, err := s.AdminGroups(context.Background(), "user1")
	if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
		t.Fatalf("NewAPI inventory=%+v err=%v", groups, err)
	}
	c45AssertConfiguration(t, groups[0].Accounts[0].TestConfiguration, TestProtocolChatCompletions, 10, "default")
	jobs := s.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
	if len(jobs) != 1 {
		t.Fatalf("NewAPI jobs=%d", len(jobs))
	}
	c45AssertConfiguration(t, jobs[0].target.TestConfiguration, TestProtocolChatCompletions, 10, "default")
}

func TestMessagesC45K7DefaultAndExplicitMessagesHaveSameProbeAndHealth(t *testing.T) {
	type requestFact struct {
		URL    string
		Header http.Header
		Body   map[string]any
	}
	type healthFact struct {
		State                                                   State
		Weight, Failures, Successes                             int
		Evidence                                                string
		Counter, EvidenceProtocol, LastSuccess, AppliedProtocol TestProtocol
		AppliedResult, Disposition                              string
	}
	var requests []requestFact
	var states []healthFact
	for _, explicit := range []bool{true, false} {
		s, repo, _ := c45Fixture(t)
		repo.policies[0].RuleVersion = RuleVersionV2
		if !repo.policies[0].AutoDegradeEnabled {
			t.Fatal("comparison requires automatic degradation policy")
		}
		if explicit {
			saveProtocolServiceConfig(t, repo, "g1", &GroupTestConfiguration{TestProtocolMessages, 20})
		}
		repo.states[c45Target] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: c45Target, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", RuleVersion: RuleVersionV2, State: StateHealthy, CurrentWeight: 100, ConsecutiveFailures: 2, ConsecutiveSuccesses: 1, CounterProtocol: protocolPointer(TestProtocolChatCompletions), HealthEvidenceStatus: "valid", HealthEvidenceProtocol: protocolPointer(TestProtocolChatCompletions), LastSuccessProtocol: protocolPointer(TestProtocolChatCompletions)}}
		calls := 0
		s.probeRunner.client.Transport = protocolContractTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, requestFact{req.URL.String(), req.Header.Clone(), body})
			return c45ProbeResponse(req), nil
		})
		runProtocolServiceJob(s, c45Job(t, s, repo))
		if calls != 1 || len(repo.events) != 1 {
			t.Fatalf("explicit=%v requests=%d events=%d want each=1", explicit, calls, len(repo.events))
		}
		state := repo.states[c45Target]["gpt-4o"]
		value := func(p *TestProtocol) TestProtocol {
			if p == nil {
				return ""
			}
			return *p
		}
		result := ""
		if state.LastAppliedProbeResult != nil {
			result = *state.LastAppliedProbeResult
		}
		states = append(states, healthFact{state.State, state.CurrentWeight, state.ConsecutiveFailures, state.ConsecutiveSuccesses, state.HealthEvidenceStatus, value(state.CounterProtocol), value(state.HealthEvidenceProtocol), value(state.LastSuccessProtocol), value(state.LastAppliedProbeProtocol), result, repo.events[0].ProbeDisposition})
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Errorf("default request differs from explicit Messages: %+v", requests)
	}
	if !reflect.DeepEqual(states[0], states[1]) {
		t.Errorf("default health differs from explicit Messages: %+v", states)
	}
	if states[0].Counter != TestProtocolMessages || states[0].EvidenceProtocol != TestProtocolMessages || states[0].Evidence != "valid" || states[0].Disposition != "applied" {
		t.Errorf("explicit Messages did not establish effective health evidence: %+v", states[0])
	}
}

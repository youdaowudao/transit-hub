package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// newAdminTargetsRemoteActionService 构造一个用真实 remoteActionDispatcher（而不是
// noopRemoteActionRunner）驱动的 Service，供测试区分手动探活与后台调度的远端动作边界。
func newAdminTargetsRemoteActionService(reader PlatformGroupReader, mySites MySitesReader, repo *fakeRepository, platform *fakePlatformActioner) *Service {
	if fixture, ok := reader.(fakePlatformGroupReader); ok {
		if siteFixture, ok := mySites.(fakeMySitesReader); ok {
			for _, accounts := range fixture.accountsByGrp {
				for _, account := range accounts {
					for _, policy := range repo.policies {
						assignPolicyToTarget(repo, policy, buildTargetID(string(siteFixture.session.Platform), "ws1", account.ID))
					}
				}
			}
		}
	}
	return &Service{
		repo:           repo,
		mySites:        mySites,
		accounts:       fakeAdminAccountResolver{id: "ws1"},
		dispatcher:     newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, platform),
		probeRunner:    NewRealProbeRunner(),
		platformGroups: reader,
	}
}

// sub2APIProbePolicy 返回一条启用策略：自动降级开启，自动远端动作按参数控制。
func sub2APIProbePolicy(autoRemoteAction bool) Policy {
	return Policy{
		ID: "policy-1", UserID: "user1", AdminAccountID: "ws1", Name: "p", Enabled: true, DailyProbeBudget: 1000,
		AutoDegradeEnabled: true, AutoRemoteActionEnabled: autoRemoteAction,
		FailureThreshold: 3, SuccessThreshold: 2, CooldownSeconds: 300, ObservationSeconds: 300, RecoveryStepPercent: 25,
		ModelTargets: []ModelTarget{{ID: "t1", PolicyID: "policy-1", ModelName: "gpt-4o", ProviderFamily: ProviderOpenAI, Enabled: true, MaxProbeTokens: 1}},
	}
}

func assignPolicyToTarget(repo *fakeRepository, policy Policy, targetID string) {
	repo.assignments = append(repo.assignments, PolicyAssignment{
		UserID: policy.UserID, AdminAccountID: policy.AdminAccountID, TargetID: targetID, PolicyID: policy.ID,
	})
}

func TestProbeTarget_FormalProbeUsesEffectiveStrategyWithoutConsumingBudget(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	policy.ModelTargets[0].ProbePrompt = "formal prompt"
	policy.ModelTargets[0].MaxProbeTokens = 7
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	svc.priorityActions = &fakeTargetPriorityActioner{}

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("formal probe failed: %v", err)
	}
	if len(results) != 1 || results[0].State != StateHealthy {
		t.Fatalf("unexpected formal result: %+v", results)
	}
	if requestBody["max_tokens"] != float64(7) {
		t.Fatalf("max_tokens = %#v, want 7", requestBody["max_tokens"])
	}
	messages, _ := requestBody["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "formal prompt" {
		t.Fatalf("formal probe did not use strategy prompt: %#v", requestBody)
	}
	if len(repo.budgetClaims) != 0 {
		t.Fatalf("formal probe must not consume automatic budget: %+v", repo.budgetClaims)
	}
	if len(repo.events) != 1 || repo.events[0].Source != EventSourceManual {
		t.Fatalf("formal probe event source = %+v, want manual", repo.events)
	}
	stored := repo.states[targetID]["gpt-4o"]
	if stored.LastProbeDecisionKey == "" {
		t.Fatal("formal probe must persist its effective decision key")
	}
	count, err := repo.CountProbesToday(context.Background(), "user1", "ws1", policy.ID, probeBudgetDayStart(time.Now()))
	if err != nil || count != 0 {
		t.Fatalf("manual event must not count against automatic budget: count=%d err=%v", count, err)
	}
	allowed, err := repo.TryConsumeProbeBudget(context.Background(), "user1", "ws1", policy.ID, probeBudgetDayStart(time.Now()), 1)
	if err != nil || !allowed {
		t.Fatalf("automatic probe must retain its full budget after formal manual probe: allowed=%v err=%v", allowed, err)
	}
	waitForPriorityLeaseCount(t, repo, "user1|ws1", 1)
}

func waitForPriorityLeaseCount(t *testing.T, repo *fakeRepository, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		repo.priorityLeaseMu.Lock()
		got := repo.priorityLeaseCount[key]
		repo.priorityLeaseMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	repo.priorityLeaseMu.Lock()
	got := repo.priorityLeaseCount[key]
	repo.priorityLeaseMu.Unlock()
	t.Fatalf("priority refresh lease count = %d, want %d", got, want)
}

func TestProbeTarget_QueuedManualProbeStopsWhenRequestIsCancelled(t *testing.T) {
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}},
		},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	svc.probeLimiter = newProbeConcurrencyLimiter(1, 1)
	releaseCurrent, ok := svc.probeLimiter.acquireAutomatic(context.Background(), "user1|ws1")
	if !ok {
		t.Fatal("failed to occupy the probe slot")
	}
	defer releaseCurrent()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := svc.ProbeTarget(ctx, "user1", targetID, []string{"gpt-4o"})
		resultCh <- err
	}()
	waitForProbeManualWaiters(t, svc.probeLimiter, "user1|ws1", 1)
	cancel()

	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled queued probe error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled queued probe did not return")
	}
	if len(repo.events) != 0 || len(repo.states) != 0 {
		t.Fatalf("cancelled queued probe changed health data: events=%+v states=%+v", repo.events, repo.states)
	}
}

func TestProbeTarget_FormalProbeReturnsEventPersistenceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	repo := newFakeRepository()
	repo.insertEventErr = errors.New("audit unavailable")
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err == nil || len(results) != 0 {
		t.Fatalf("formal probe must report missing manual audit event, results=%+v err=%v", results, err)
	}
}

func TestProbeTarget_FormalProbeReturnsStatePersistenceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	repo := newFakeRepository()
	repo.upsertStateErr = errors.New("state unavailable")
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err == nil || len(results) != 0 || len(repo.events) != 0 {
		t.Fatalf("formal probe must report missing state persistence without an event, results=%+v events=%+v err=%v", results, repo.events, err)
	}
}

func TestProbeTarget_TargetLeaseConflictHasZeroSideEffects(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	repo := newFakeRepository()
	repo.targetLeaseBlocked = true
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	_, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err == nil || err.Error() != ErrorProbeTargetLeaseBusy {
		t.Fatalf("lease conflict error = %v, want %s", err, ErrorProbeTargetLeaseBusy)
	}
	if hits != 0 || len(repo.states) != 0 || len(repo.events) != 0 || len(repo.budgetClaims) != 0 {
		t.Fatalf("lease conflict caused side effects: hits=%d states=%+v events=%+v budget=%+v", hits, repo.states, repo.events, repo.budgetClaims)
	}
}

func TestProbeTarget_FormalProbeBlockedStateHasZeroSideEffects(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	for _, test := range []struct {
		name      string
		state     ConnectionHealthState
		wantError string
	}{
		{name: "disabled", state: ConnectionHealthState{State: StateDisabled}, wantError: "admin.connectionHealth.errors.probeBlockedHealthDisabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeRepository()
			policy := sub2APIProbePolicy(false)
			repo.policies = []Policy{policy}
			targetID := "sub2api:ws1:acc-1"
			assignPolicyToTarget(repo, policy, targetID)
			test.state.ConnectionID = targetID
			test.state.ModelName = "gpt-4o"
			test.state.UserID = "user1"
			test.state.AdminAccountID = "ws1"
			repo.states[targetID] = map[string]ConnectionHealthState{"gpt-4o": test.state}
			before := repo.states[targetID]["gpt-4o"]
			reader := fakePlatformGroupReader{
				groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
				accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
				credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
			}
			svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			_, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("error = %v, want %s", err, test.wantError)
			}
			if got := repo.states[targetID]["gpt-4o"]; got != before {
				t.Fatalf("blocked probe changed state: before=%+v after=%+v", before, got)
			}
			if len(repo.events) != 0 || len(repo.budgetClaims) != 0 {
				t.Fatalf("blocked probe wrote side effects: events=%+v budget=%+v", repo.events, repo.budgetClaims)
			}
		})
	}
	if hits != 0 {
		t.Fatalf("blocked formal probes issued %d HTTP requests", hits)
	}
}

func TestProbeTarget_FormalProbeBypassesCooldownAndRejoinsObservation(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	lastProbe := time.Now()
	cooldown := lastProbe.Add(time.Hour)
	repo.states[targetID] = map[string]ConnectionHealthState{
		"gpt-4o": {
			ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1",
			State: StateSuspended, CurrentWeight: 0, ConsecutiveFailures: 3,
			LastProbeAt: &lastProbe, CooldownUntil: &cooldown,
		},
	}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("formal probe during cooldown failed: %v", err)
	}
	if hits != 1 || len(results) != 1 || results[0].State != StateObserving {
		t.Fatalf("formal probe must execute and enter observing: hits=%d results=%+v", hits, results)
	}
	stored := repo.states[targetID]["gpt-4o"]
	if stored.State != StateObserving || stored.CooldownUntil != nil || stored.ConsecutiveFailures != 0 {
		t.Fatalf("successful formal probe must clear suspension timing: %+v", stored)
	}
	if len(repo.events) != 1 || repo.events[0].Source != EventSourceManual {
		t.Fatalf("formal recovery event source = %+v, want manual", repo.events)
	}
}

func TestProbeTarget_FormalProbeBypassesFailureBackoff(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	targetID := "sub2api:ws1:acc-1"
	assignPolicyToTarget(repo, policy, targetID)
	lastProbe := time.Now()
	repo.states[targetID] = map[string]ConnectionHealthState{
		"gpt-4o": {
			ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1",
			State: StateDegraded, CurrentWeight: 50, ConsecutiveFailures: 2, LastProbeAt: &lastProbe,
		},
	}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil || hits != 1 || len(results) != 1 {
		t.Fatalf("formal probe must bypass failure backoff: hits=%d results=%+v err=%v", hits, results, err)
	}
	if stored := repo.states[targetID]["gpt-4o"]; stored.ConsecutiveFailures != 0 || stored.LastProbeAt == nil || !stored.LastProbeAt.After(lastProbe) {
		t.Fatalf("successful formal probe must replace the backed-off result: %+v", stored)
	}
}

func TestProbeTarget_FormalProbeRejectsUnassignedModel(t *testing.T) {
	repo := newFakeRepository()
	policy := sub2APIProbePolicy(false)
	repo.policies = []Policy{policy}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)

	_, err := svc.ProbeTarget(context.Background(), "user1", "sub2api:ws1:acc-1", []string{"gpt-4o"})
	if err == nil || err.Error() != ErrorNoMatchingModels {
		t.Fatalf("unassigned formal model error = %v, want %s", err, ErrorNoMatchingModels)
	}
}

// TestProbeTarget_FormalFailureDefersRemoteDegradeToScheduler 验证正式手动探活只更新健康状态
// 与事件；即使策略允许自动远端动作，也不能在请求线程直接关闭 Sub2API 账号。
func TestProbeTarget_FormalFailureDefersRemoteDegradeToScheduler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(true)}
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminTargetsRemoteActionService(reader, mySites, repo, platform)

	targetID := "sub2api:ws1:acc-1"
	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateSuspended {
		t.Fatalf("expected hard failure to suspend immediately, got %+v", results)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("formal probe must defer remote inactive to the scheduler, got %+v", platform.sub2APICalls)
	}
	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != "" {
		t.Fatalf("formal probe must not claim a remote action, got %q", st.LastRemoteAction)
	}
	if len(repo.events) != 1 || repo.events[0].Source != EventSourceManual || repo.events[0].RemoteAction != "" {
		t.Fatalf("formal probe must record only a manual health event, got %+v", repo.events)
	}
	if len(repo.targetActionStates) != 0 {
		t.Fatalf("blocked manual inactive must not create an ownership checkpoint: %+v", repo.targetActionStates)
	}
}

// TestProbeTargetOnce_Sub2APIAutoRemoteRestoreUpdatesActive 验证手动探活恢复时保留
// 既有 active 写回；最后账号保底只限制 inactive 方向。
func TestProbeTargetOnce_Sub2APIAutoRemoteRestoreUpdatesActive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(true)}
	targetID := "sub2api:ws1:acc-1"
	observingUntil := time.Now().Add(-1 * time.Minute)
	repo.states[targetID] = map[string]ConnectionHealthState{
		"gpt-4o": {
			ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1",
			State: StateObserving, ConsecutiveSuccesses: 1, ObservingUntil: &observingUntil, CurrentWeight: 0,
			LastRemoteAction: RemoteActionSub2APIStatusInactive,
		},
	}
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Status: "inactive", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminTargetsRemoteActionService(reader, mySites, repo, platform)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateRecovering {
		t.Fatalf("expected transition to recovering after success threshold, got %+v", results)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "acc-1" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("expected one call accountID=acc-1 status=active, got %+v", platform.sub2APICalls)
	}
	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != RemoteActionSub2APIStatusActive {
		t.Fatalf("expected state.LastRemoteAction=%s, got %q", RemoteActionSub2APIStatusActive, st.LastRemoteAction)
	}
}

// TestProbeTarget_FormalFailureNeverInvokesFailingDispatcher 验证即使远端动作配置为失败，正式
// 手动探活也不会触达 dispatcher，更不能把一个未执行动作记录成失败。
func TestProbeTarget_FormalFailureNeverInvokesFailingDispatcher(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(true)}
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream 500")}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminTargetsRemoteActionService(reader, mySites, repo, platform)

	targetID := "sub2api:ws1:acc-1"
	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateSuspended {
		t.Fatalf("expected hard failure to suspend, got %+v", results)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("formal probe must not invoke the failing dispatcher: %+v", platform.sub2APICalls)
	}
	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != "" {
		t.Fatalf("unattempted remote action must not be recorded as failed, got %q", st.LastRemoteAction)
	}
	if len(repo.events) != 1 || repo.events[0].RemoteAction != "" {
		t.Fatalf("unattempted remote action must stay empty in the event: %+v", repo.events)
	}
}

// TestProbeTargetOnce_Sub2APIAutoRemoteRestoreFailureRecordsFailedAction 验证手动恢复写回
// 失败时仍记录 active_failed，不能因 inactive 保底丢失既有错误证据。
func TestProbeTargetOnce_Sub2APIAutoRemoteRestoreFailureRecordsFailedAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(true)}
	targetID := "sub2api:ws1:acc-1"
	observingUntil := time.Now().Add(-1 * time.Minute)
	repo.states[targetID] = map[string]ConnectionHealthState{
		"gpt-4o": {
			ConnectionID: targetID, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1",
			State: StateObserving, ConsecutiveSuccesses: 1, ObservingUntil: &observingUntil, CurrentWeight: 0,
			LastRemoteAction: RemoteActionSub2APIStatusInactive,
		},
	}
	platform := &fakePlatformActioner{sub2APIErr: errors.New("upstream 500")}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Status: "inactive", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminTargetsRemoteActionService(reader, mySites, repo, platform)

	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateRecovering {
		t.Fatalf("expected transition to recovering, got %+v", results)
	}
	if len(platform.sub2APICalls) != 1 || platform.sub2APICalls[0].accountID != "acc-1" || platform.sub2APICalls[0].status != "active" {
		t.Fatalf("expected one failed call accountID=acc-1 status=active, got %+v", platform.sub2APICalls)
	}
	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != RemoteActionSub2APIStatusActiveFailed {
		t.Fatalf("expected state.LastRemoteAction=%s, got %q", RemoteActionSub2APIStatusActiveFailed, st.LastRemoteAction)
	}
}

// TestProbeTargetOnce_Sub2APIRealPlatformServiceComboDegradeSucceeds 是覆盖「PlatformService
// 单测通过、dispatcher fake 单测通过，但真实组合路径失败」这类盲区的端到端测试：
// 用同一个 httptest.Server 同时模拟探活端点和 sub2api admin accounts 的字段级批量更新。
// 手动探活先只落 suspended；随后模拟 scheduled 收口，复用完整 inventory 做保底裁决并通过真实
// *upstream.PlatformService 把指定账号写成 inactive。
func TestProbeTargetOnce_Sub2APIRealPlatformServiceComboDegradeSucceeds(t *testing.T) {
	var bulkBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/accounts/bulk-update":
			if err := json.NewDecoder(r.Body).Decode(&bulkBody); err != nil {
				t.Fatalf("failed to decode bulk update body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	realPlatform := upstream.NewPlatformService(upstream.NewHTTPClient(server.Client()))
	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(true)}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "token-1", TokenType: "Bearer"}}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "acc", Status: "active", Schedulable: boolPointer(true), Models: "gpt-4o"}, {ID: "1516", Name: "reserve", Status: "active", Schedulable: boolPointer(true), Models: "gpt-4o"}},
		},
		credByAccount: map[string]upstream.ProbeCredential{"1515": {BaseURL: server.URL, Key: "probe-key"}},
	}
	svc := &Service{
		repo: repo, mySites: mySites, accounts: fakeAdminAccountResolver{id: "ws1"},
		dispatcher:     newRemoteActionDispatcher(fakeSiteLookup{}, fakeSessionProvider{}, realPlatform),
		probeRunner:    NewRealProbeRunner(),
		platformGroups: reader,
	}

	targetID := "sub2api:ws1:1515"
	assignPolicyToTarget(repo, repo.policies[0], targetID)
	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateSuspended {
		t.Fatalf("expected hard failure to suspend, got %+v", results)
	}
	if bulkBody != nil {
		t.Fatalf("formal probe must not write Sub2API before scheduled reconciliation: %+v", bulkBody)
	}
	refresh, err := svc.refreshAdminTarget(context.Background(), mySites.session, "ws1", "1515")
	if err != nil || !refresh.found || refresh.accountsReadError {
		t.Fatalf("scheduled refresh failed: refresh=%+v err=%v", refresh, err)
	}
	state := repo.states[targetID]["gpt-4o"]
	spec := probeModelSpec{modelName: "gpt-4o", policy: repo.policies[0]}
	if err := svc.finishTargetProbeBatchWithFloor(
		context.Background(), "user1", "ws1", mySites.session, refresh.target,
		[]probeModelSpec{spec}, []targetProbeResult{{state: &state, previousState: StateHealthy, outcome: ProbeOutcome{Result: ResultServerError}, spec: spec}},
		EventSourceScheduled, newWorkspaceFloorGuard(), &refresh.inventory,
	); err != nil {
		t.Fatalf("scheduled target action failed: %v", err)
	}

	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != RemoteActionSub2APIStatusInactive {
		t.Fatalf("expected state.LastRemoteAction=%s, got %q", RemoteActionSub2APIStatusInactive, st.LastRemoteAction)
	}
	if len(repo.events) != 2 || repo.events[1].RemoteAction != RemoteActionSub2APIStatusInactive || repo.events[1].Source != EventSourceScheduled {
		t.Fatalf("expected event.RemoteAction=%s, got %+v", RemoteActionSub2APIStatusInactive, repo.events)
	}
	if bulkBody == nil {
		t.Fatalf("expected a real bulk update request to the sub2api admin accounts API")
	}
	if len(bulkBody) != 2 || bulkBody["status"] != "inactive" {
		t.Fatalf("expected field-only status update, got %+v", bulkBody)
	}
	accountIDs, ok := bulkBody["account_ids"].([]any)
	if !ok || len(accountIDs) != 1 || accountIDs[0] != float64(1515) {
		t.Fatalf("expected account_ids=[1515], got %+v", bulkBody["account_ids"])
	}
}

// TestProbeTargetOnce_Sub2APIRemoteActionDisabledSkipsUpstream 验证 AutoRemoteActionEnabled=false
// 时，即使状态机触发远端动作，也绝不调用 sub2api PUT 接口，只记录 skipped_independent_probe。
func TestProbeTargetOnce_Sub2APIRemoteActionDisabledSkipsUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	repo := newFakeRepository()
	repo.policies = []Policy{sub2APIProbePolicy(false)}
	platform := &fakePlatformActioner{}
	mySites := fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "vip"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "acc-1", Name: "acc", Models: "gpt-4o"}}},
		credByAccount: map[string]upstream.ProbeCredential{"acc-1": {BaseURL: server.URL, Key: "k"}},
	}
	svc := newAdminTargetsRemoteActionService(reader, mySites, repo, platform)

	targetID := "sub2api:ws1:acc-1"
	results, err := svc.ProbeTarget(context.Background(), "user1", targetID, []string{"gpt-4o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].State != StateSuspended {
		t.Fatalf("expected hard failure to suspend, got %+v", results)
	}
	if len(platform.sub2APICalls) != 0 {
		t.Fatalf("expected no upstream call when AutoRemoteActionEnabled=false, got %+v", platform.sub2APICalls)
	}
	st := repo.states[targetID]["gpt-4o"]
	if st.LastRemoteAction != RemoteActionSkippedIndependentProbe {
		t.Fatalf("expected LastRemoteAction=%s, got %q", RemoteActionSkippedIndependentProbe, st.LastRemoteAction)
	}
}

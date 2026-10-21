package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

const noticePathsConfigBlocked = "因为分组测试配置冲突或无法确认，系统目前无法探活，也不会自动恢复。请到分组设置把这个账号所在各分组的测试协议和超时统一，未被禁止探活的模型才会继续自动探活。若模型显示“健康已禁用”，请联系管理员核查该模型的禁用记录；正式探活不能解除禁用。"
const noticePathsSchedulingBlocked = "因为主站调度已关闭、且策略设为调度关闭时不继续探活，系统目前不会自动探活。请在分组健康检查模型状态：若有模型显示“健康已禁用”，请先联系管理员核查禁用记录，正式探活不能解除禁用；否则可点“一键正式探活”验证。仍处于关停状态的模型要按各自预设要求连续成功，全部受控模型恢复健康后，账号才会改回原状态；调度开关仍关闭，要接收请求请在主站手动打开。"

type noticePathsPublisher struct {
	mu     sync.Mutex
	inputs []notifications.NoticeInput
	err    error
}

func (p *noticePathsPublisher) Publish(_ context.Context, input notifications.NoticeInput) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = append(p.inputs, input)
	return "fixture-notice", p.err == nil, p.err
}

func (p *noticePathsPublisher) snapshot() []notifications.NoticeInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]notifications.NoticeInput(nil), p.inputs...)
}

// Missing producer injection on the old implementation leaves the real tick
// unchanged; positive assertions still demand its actual business notification.
func noticePathsInject(s *Service, publisher any) {
	method := reflect.ValueOf(s).MethodByName("SetNoticePublisher")
	if method.IsValid() {
		method.Call([]reflect.Value{reflect.ValueOf(publisher)})
	}
}

type noticePathsFixture struct {
	s       *Service
	repo    *fakeRepository
	reader  fakePlatformGroupReader
	pub     *noticePathsPublisher
	probes  atomic.Int32
	actions *fakePlatformActioner
}

func newNoticePathsFixture(t *testing.T) *noticePathsFixture {
	t.Helper()
	f := &noticePathsFixture{repo: newFakeRepository(), pub: &noticePathsPublisher{}}
	p := schedulableProbePolicy("notice", true, 60)
	p.AutoDegradeEnabled, p.AutoRemoteActionEnabled = true, false
	p.PriorityMode, p.StrategyMode, p.RuleVersion = PriorityModeNone, StrategyModeHealthProbe, RuleVersionV2
	preset := recommendedRulePreset()
	preset.LongFailureIntervalSeconds = 86400
	p.RulePreset = &preset
	f.repo.policies = []Policy{p}
	f.repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: p.ID}}
	now := time.Now().UTC()
	origin, last := now.Add(-25*time.Hour), now.Add(-time.Hour)
	protocol, outcome := TestProtocolChatCompletions, string(ResultAuth)
	f.repo.states["sub2api:ws1:a"] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: "sub2api:ws1:a", UserID: "user1", AdminAccountID: "ws1", ModelName: "gpt-4o", State: StateSuspended, RuleVersion: RuleVersionV2, CurrentWeight: 0, ConsecutiveFailures: 5, FailingSince: &origin, LastProbeAt: &last, LastFailureAt: &last, LastAppliedProbeAt: &last, LastAppliedProbeResult: &outcome, LastAppliedProbeProtocol: &protocol, LastProbeProtocol: &protocol, CounterProtocol: &protocol, HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: &protocol, LastErrorKey: string(ResultAuth), LastErrorDetail: "账号套餐尚未开通，联系上游处理"}}
	f.reader = fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "隔离提醒组"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Name: "隔离失败账号", Status: "active", Models: "gpt-4o", Priority: intPointer(100), Schedulable: boolPointer(true), TempUnschedulableKnown: true}, {ID: "guard", Name: "隔离保留账号", Status: "active", Models: "gpt-4o", Priority: intPointer(5), Schedulable: boolPointer(true), TempUnschedulableKnown: true}}}, credByAccount: map[string]upstream.ProbeCredential{"a": {BaseURL: "https://fixture.invalid", Key: "fixture"}}}
	f.actions = &fakePlatformActioner{afterSub2APIWrite: func(id, status string) {
		for groupID, accounts := range f.reader.accountsByGrp {
			for i := range accounts {
				if accounts[i].ID == id {
					accounts[i].Status = status
				}
			}
			f.reader.accountsByGrp[groupID] = accounts
		}
	}}
	f.s = newAdminGroupsService(f.reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, f.repo)
	f.s.dispatcher, f.s.schedulableActions = newRemoteActionDispatcher(nil, nil, f.actions), forbiddenAutomaticSchedulingWriter{t}
	f.s.probeRunner.client.Transport = c1RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		f.probes.Add(1)
		return schedulerPathsResponse(request, http.StatusInternalServerError), nil
	})
	noticePathsInject(f.s, f.pub)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := f.s.Shutdown(ctx); err != nil {
			t.Errorf("notice fixture shutdown: %v", err)
		}
		schedulerStatsByService.Delete(f.s)
	})
	return f
}

func (f *noticePathsFixture) tick(t *testing.T) {
	t.Helper()
	f.s.runSchedulerTick(context.Background())
	waitSchedulerPathsIdle(t, f.s)
}

func (f *noticePathsFixture) conflictConfiguration() {
	f.reader.groups = append(f.reader.groups, upstream.AdminGroupInfo{ID: "g2", Name: "隔离冲突组"})
	f.reader.accountsByGrp["g2"] = append([]upstream.AdminGroupAccountInfo(nil), f.reader.accountsByGrp["g1"]...)
	f.repo.testConfigurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10}, {UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30}}
	f.s.platformGroups = f.reader
}

func (f *noticePathsFixture) systemStopped() {
	f.repo.policies[0].AutoRemoteActionEnabled = true
	for groupID, accounts := range f.reader.accountsByGrp {
		for i := range accounts {
			if accounts[i].ID == "a" {
				accounts[i].Status = "inactive"
			}
		}
		f.reader.accountsByGrp[groupID] = accounts
	}
	f.s.platformGroups = f.reader
	f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalStatus: "active", LastAppliedStatus: "inactive", UpdatedAt: time.Now().Add(-26 * time.Hour)}
}

func noticePathsDetail(input notifications.NoticeInput, label string) string {
	for _, detail := range input.Content.Details {
		if detail.Label == label {
			return detail.Value
		}
	}
	return ""
}

func TestLongFailureNoticePublishedOncePerEpisode(t *testing.T) {
	f := newNoticePathsFixture(t)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	f.tick(t)
	inputs := f.pub.snapshot()
	if len(inputs) != 1 {
		t.Fatalf("no-due-job tick must publish its collected long failure: got %d", len(inputs))
	}
	input := inputs[0]
	if input.Kind != "connection_health.long_failure" || input.EventKey != "long_failure|sub2api:ws1:a|gpt-4o|"+state.FailingSince.UTC().Format(time.RFC3339Nano) || input.SubjectType != "connection_health_target" || input.SubjectID != state.ConnectionID || input.UserID != "user1" || input.AdminAccountID != "ws1" || input.Level != notifications.LevelImportant || !input.DiscoveredAt.Equal(state.FailingSince.Add(24*time.Hour)) || input.Content.Action == nil || input.Content.Action.Kind != "connection_health" {
		t.Fatalf("notification identity/level/discovery/navigation mismatch: %+v", input)
	}
	if input.Content.Title != "账号长期失败，需要人工处理" || !strings.Contains(input.Content.Summary, "鉴权失败") || !strings.Contains(input.Content.Summary, "账号套餐尚未开通") || !strings.Contains(input.Content.Summary, "系统已改为每 24 小时 自动试一次") || !strings.HasSuffix(input.Content.Summary, "查看这条提醒不会修改主站。") || len(input.Content.Details) != 7 || noticePathsDetail(input, "连续失败开始") != state.FailingSince.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04") {
		t.Fatalf("notification lost its concrete reason or required Chinese explanation: %+v", input.Content)
	}
	f.tick(t)
	if len(f.pub.snapshot()) != 1 || f.probes.Load() != 0 || len(f.actions.sub2APICalls) != 0 {
		t.Fatal("stable no-due ticks repeated a notice or ran a probe/main-site write")
	}
	_ = f.s.collectAdminProbeJobs(context.Background(), f.repo.policies, f.repo.assignments)
	_ = f.s.collectAdminProbeJobsWithGroups(context.Background(), f.repo.policies, f.repo.assignments, f.repo.groupAssignments, nil)
	if len(f.pub.snapshot()) != 1 {
		t.Fatal("non-scheduler collection wrapper must not publish")
	}
}

func TestLongFailureNoticeNewEpisode(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.tick(t)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	state.State, state.FailingSince, state.ConsecutiveFailures = StateHealthy, nil, 0
	now := time.Now()
	state.LastProbeAt = &now
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	origin, last := time.Now().Add(-26*time.Hour), time.Now().Add(-time.Hour)
	state.State, state.FailingSince, state.LastProbeAt = StateSuspended, &origin, &last
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	inputs := f.pub.snapshot()
	if len(inputs) != 2 || inputs[0].EventKey == inputs[1].EventKey || !inputs[1].DiscoveredAt.Equal(origin.Add(24*time.Hour)) {
		t.Fatalf("new failure episode did not create its own notification: count=%d", len(inputs))
	}
}

func TestLongFailureNoticeRetriesAfterPublishError(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.pub.err = errors.New("fixture notification storage unavailable")
	f.repo.policies[0].RulePreset.LongFailureIntervalSeconds = 600
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	last := time.Now().Add(-2 * time.Hour)
	state.LastProbeAt, state.LastAppliedProbeAt = &last, &last
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	if got, _ := f.repo.GetState(context.Background(), state.ConnectionID, state.ModelName); f.probes.Load() != 1 || got.LastProbeAt == nil || !got.LastProbeAt.After(last) {
		t.Fatal("notification storage failure prevented actual probe state persistence")
	}
	if len(f.pub.snapshot()) != 1 {
		t.Fatal("real due-job tick did not attempt the required notification")
	}
	f.pub.mu.Lock()
	f.pub.err = nil
	f.pub.mu.Unlock()
	f.tick(t)
	f.tick(t)
	if inputs := f.pub.snapshot(); len(inputs) != 2 || inputs[0].EventKey != inputs[1].EventKey {
		t.Fatalf("failed publish must retry its original event exactly once: attempts=%d", len(inputs))
	}
}

func TestLongFailureNoticeRestartConflictIsRemembered(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.tick(t)
	f.pub.err = notifications.ErrEventConflict
	next := newAdminGroupsService(f.reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, f.repo)
	next.schedulableActions = forbiddenAutomaticSchedulingWriter{t}
	noticePathsInject(next, f.pub)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = next.Shutdown(ctx)
		schedulerStatsByService.Delete(next)
	})
	next.runSchedulerTick(context.Background())
	next.runSchedulerTick(context.Background())
	if inputs := f.pub.snapshot(); len(inputs) != 2 || inputs[0].EventKey != inputs[1].EventKey {
		t.Fatalf("restart conflict must count as sent rather than retry every tick: attempts=%d", len(inputs))
	}
}

func TestLongFailureNoticePublishCapPerTick(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("publish-error-%t", fail), func(t *testing.T) {
			f := newNoticePathsFixture(t)
			if fail {
				f.pub.err = errors.New("fixture publish error")
			}
			seed := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			delete(f.repo.states, seed.ConnectionID)
			f.reader.accountsByGrp["g1"] = nil
			for i := 0; i < 7; i++ {
				id := fmt.Sprintf("a%d", i)
				state := seed
				state.ConnectionID = "sub2api:ws1:" + id
				origin := time.Now().Add(time.Duration(-31+i) * time.Hour)
				state.FailingSince = &origin
				f.repo.states[state.ConnectionID] = map[string]ConnectionHealthState{state.ModelName: state}
				f.reader.accountsByGrp["g1"] = append(f.reader.accountsByGrp["g1"], upstream.AdminGroupAccountInfo{ID: id, Name: id, Models: "gpt-4o", Status: "active", Priority: intPointer(100), Schedulable: boolPointer(true)})
			}
			f.s.platformGroups = f.reader
			f.tick(t)
			inputs := f.pub.snapshot()
			if len(inputs) != 5 {
				t.Fatalf("tick attempted %d notifications, want five including failures", len(inputs))
			}
			for i := 0; i < 5; i++ {
				if inputs[i].SubjectID != fmt.Sprintf("sub2api:ws1:a%d", i) {
					t.Fatal("notifications were not attempted oldest first")
				}
			}
			if !fail {
				f.tick(t)
				f.tick(t)
				if inputs = f.pub.snapshot(); len(inputs) != 7 || inputs[5].SubjectID != "sub2api:ws1:a5" || inputs[6].SubjectID != "sub2api:ws1:a6" {
					t.Fatal("remaining candidates were lost or repeated after the five-notice cap")
				}
			}
		})
	}
}

func TestDisabledUnverifiedNotice(t *testing.T) {
	for _, kind := range []string{"config-conflict", "success-then-config-conflict", "scheduling-blocked", "never-probed", "disabled-with-config-conflict", "disabled-with-scheduling-blocked"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			f.systemStopped()
			state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			origin := state.FailingSince
			last := time.Now().Add(-25 * time.Hour)
			state.LastProbeAt, state.LastAppliedProbeAt, state.FailingSince = &last, &last, nil
			state.State = StateHealthy
			message := noticePathsConfigBlocked
			if kind == "scheduling-blocked" || kind == "disabled-with-scheduling-blocked" {
				f.repo.policies[0].ContinueProbeWhenUnschedulable = false
				f.reader.accountsByGrp["g1"][0].Schedulable = boolPointer(false)
				message = noticePathsSchedulingBlocked
			} else {
				f.conflictConfiguration()
			}
			if kind == "never-probed" {
				state.LastProbeAt, state.LastAppliedProbeAt = nil, nil
			}
			if kind == "success-then-config-conflict" {
				ok := string(ResultOK)
				state.State, state.ConsecutiveFailures, state.ConsecutiveSuccesses = StateSuspended, 0, 1
				state.LastAppliedProbeResult, state.LastSuccessAt = &ok, &last
			}
			if strings.HasPrefix(kind, "disabled-with-") {
				state.State, state.FailingSince = StateDisabled, origin
			}
			f.repo.states[state.ConnectionID][state.ModelName] = state
			f.tick(t)
			inputs := f.pub.snapshot()
			if len(inputs) != 1 || inputs[0].Kind != "connection_health.disabled_unverified" || inputs[0].Content.Title != "系统关停的账号无法继续验证" || !strings.Contains(inputs[0].Content.Summary, message) || !strings.Contains(inputs[0].Content.Summary, "正式探活不能解除禁用") || len(inputs[0].Content.Details) != 4 {
				t.Fatalf("system shutdown without possible verification must publish U with exact guidance: notices=%+v", inputs)
			}
			key, discovered := last.UTC().Format(time.RFC3339Nano), last.Add(24*time.Hour)
			if kind == "never-probed" {
				key = "never"
				discovered = f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"].UpdatedAt.Add(24 * time.Hour)
			}
			if inputs[0].EventKey != "unverifiable|sub2api:ws1:a|"+key || !inputs[0].DiscoveredAt.Equal(discovered) {
				t.Fatal("U episode key or discovery timestamp did not use latest probe / never checkpoint time")
			}
			f.tick(t)
			if len(f.pub.snapshot()) != 1 || f.probes.Load() != 0 {
				t.Fatal("U notice repeated or created a forbidden automatic probe")
			}
		})
	}
}

func TestDisabledUnverifiedUsesMinimumModelPreset(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.systemStopped()
	f.conflictConfiguration()
	f.repo.policies[0].RulePreset.LongFailureAfterSeconds = 7200
	other := f.repo.policies[0]
	other.ID, other.ModelTargets = "other", []ModelTarget{{ModelName: "other-model", Enabled: true}}
	preset := *other.RulePreset
	preset.LongFailureAfterSeconds = 14400
	other.RulePreset = &preset
	f.repo.policies = append(f.repo.policies, other)
	f.repo.groupAssignments = append(f.repo.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: other.ID})
	last := time.Now().Add(-3 * time.Hour)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	state.State, state.FailingSince, state.LastProbeAt = StateHealthy, nil, &last
	f.repo.states[state.ConnectionID][state.ModelName] = state
	state.ModelName = "other-model"
	f.repo.states[state.ConnectionID][state.ModelName] = state
	for groupID, accounts := range f.reader.accountsByGrp {
		accounts[0].Models = "gpt-4o,other-model"
		f.reader.accountsByGrp[groupID] = accounts
	}
	f.tick(t)
	inputs := f.pub.snapshot()
	if len(inputs) != 1 || inputs[0].Kind != "connection_health.disabled_unverified" || !inputs[0].DiscoveredAt.Equal(last.Add(2*time.Hour)) || !strings.Contains(inputs[0].Content.Summary, "超过 2 小时") {
		t.Fatalf("multi-model U must use its smallest judgment duration: %+v", inputs)
	}
}

func TestDisabledUnverifiedPartialModelStopDoesNotNotify(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.systemStopped()
	f.repo.policies[0].ContinueProbeWhenUnschedulable = false
	f.repo.policies[0].DailyProbeBudget = 1
	other := f.repo.policies[0]
	other.ID, other.ContinueProbeWhenUnschedulable, other.ModelTargets = "other", true, []ModelTarget{{ModelName: "other-model", Enabled: true}}
	f.repo.policies = append(f.repo.policies, other)
	f.repo.groupAssignments = append(f.repo.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: other.ID})
	f.reader.accountsByGrp["g1"][0].Models, f.reader.accountsByGrp["g1"][0].Schedulable = "gpt-4o,other-model", boolPointer(false)
	last := time.Now().Add(-25 * time.Hour)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	state.State, state.FailingSince, state.LastProbeAt, state.LastAppliedProbeAt = StateHealthy, nil, &last, &last
	f.repo.states[state.ConnectionID][state.ModelName] = state
	state.ModelName = "other-model"
	f.repo.states[state.ConnectionID][state.ModelName] = state
	for _, p := range f.repo.policies {
		f.repo.budgetClaims["user1|ws1|"+p.ID+"|"+probeBudgetDayStart(time.Now()).Format(time.RFC3339)] = 1
	}
	f.tick(t)
	if len(f.pub.snapshot()) != 0 || f.probes.Load() != 0 {
		t.Fatal("one model still permits automatic retry, so this account must not be classified unverifiable")
	}
}

func TestLongFailureNoticeUsesJudgmentPreset(t *testing.T) {
	f := newNoticePathsFixture(t)
	judgment := f.repo.policies[0]
	judgment.ID, judgment.ProbeIntervalSeconds = "judgment", 300
	preset := *judgment.RulePreset
	preset.LongFailureAfterSeconds, preset.LongFailureIntervalSeconds = 7200, 43200
	judgment.RulePreset = &preset
	budget := judgment
	budget.ID, budget.ProbeIntervalSeconds, budget.AutoRemoteActionEnabled = "budget", 60, true
	budgetPreset := preset
	budgetPreset.LongFailureAfterSeconds, budgetPreset.LongFailureIntervalSeconds = 14400, 64800
	budget.RulePreset = &budgetPreset
	f.repo.policies = []Policy{budget, judgment}
	f.repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: budget.ID}, {UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: judgment.ID}}
	f.tick(t)
	inputs := f.pub.snapshot()
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	if len(inputs) != 1 || !inputs[0].DiscoveredAt.Equal(state.FailingSince.Add(2*time.Hour)) || !strings.Contains(inputs[0].Content.Summary, "超过 2 小时") || !strings.Contains(inputs[0].Content.Summary, "每 12 小时 自动试一次") {
		t.Fatalf("notice used budget policy instead of the model's judgment policy: %+v", inputs)
	}
}

func TestLongFailureNoticeSanitizesAndBoundsConcreteError(t *testing.T) {
	f := newNoticePathsFixture(t)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	state.LastErrorDetail = `{"error":{"message":"套餐未开通；token=fixture-sensitive；Authorization: Bearer fixture-secret；<b>请联系上游开通套餐</b>；redis://fixture-password@localhost；` + strings.Repeat("具体原因", 100) + `"}}`
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	inputs := f.pub.snapshot()
	if len(inputs) != 1 {
		t.Fatal("sanitized concrete upstream cause did not generate a notification")
	}
	errorText := noticePathsDetail(inputs[0], "最近错误")
	if !strings.Contains(errorText, "套餐未开通") || !strings.Contains(errorText, "请联系上游开通套餐") || len([]rune(errorText)) > 300 {
		t.Fatal("notification dropped the known cause or exceeded the 300-character error boundary")
	}
	all := inputs[0].Content.Summary + errorText
	for _, forbidden := range []string{"fixture-sensitive", "fixture-secret", "fixture-password", "token=", "Authorization:", "<b>", "redis://"} {
		if strings.Contains(all, forbidden) {
			t.Fatal("notification exposed an unsafe upstream field")
		}
	}
}

func TestDisabledUnverifiedNoticeNegativeGuards(t *testing.T) {
	for _, kind := range []string{"no-system-record", "manual-conflict", "target-pending", "too-recent", "credential-unavailable", "empty-group-restored"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			f.systemStopped()
			state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			last := time.Now().Add(-25 * time.Hour)
			state.State, state.FailingSince, state.LastProbeAt, state.LastAppliedProbeAt = StateHealthy, nil, &last, &last
			if kind != "credential-unavailable" {
				f.conflictConfiguration()
			}
			switch kind {
			case "no-system-record":
				delete(f.repo.targetActionStates, "user1|ws1|sub2api:ws1:a")
			case "manual-conflict":
				checkpoint := f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"]
				checkpoint.Conflict = true
				f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"] = checkpoint
			case "target-pending":
				checkpoint := f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"]
				checkpoint.PendingStatus, checkpoint.PendingDispatchID, checkpoint.PendingOwnerID, checkpoint.PendingDispatchPhase = "active", "fixture", "fixture", DispatchSending
				f.repo.targetActionStates["user1|ws1|sub2api:ws1:a"] = checkpoint
			case "too-recent":
				recent := time.Now().Add(-time.Hour)
				state.LastProbeAt, state.LastAppliedProbeAt = &recent, &recent
			case "credential-unavailable":
				f.reader.credErr = map[string]error{"a": errors.New("fixture credentials temporarily unavailable")}
				f.s.platformGroups = f.reader
			case "empty-group-restored":
				for groupID, accounts := range f.reader.accountsByGrp {
					f.reader.accountsByGrp[groupID] = accounts[:1]
				}
				f.s.platformGroups = f.reader
			}
			f.repo.states[state.ConnectionID][state.ModelName] = state
			f.tick(t)
			if len(f.pub.snapshot()) != 0 {
				t.Fatalf("%s generated U without all publishing conditions", kind)
			}
			if kind == "credential-unavailable" {
				if got, _ := f.repo.GetState(context.Background(), state.ConnectionID, state.ModelName); got.LastCredentialFailureAt == nil {
					t.Fatal("credential protection did not exercise the real failed preparation")
				}
			}
			if kind == "empty-group-restored" && (len(f.actions.sub2APICalls) != 1 || f.reader.accountsByGrp["g1"][0].Status != "active") {
				t.Fatal("empty-group guard did not exercise the actual earlier restoration")
			}
		})
	}
}

func TestDisabledUnverifiedPublishRechecksStateAndCachedStatus(t *testing.T) {
	for _, kind := range []string{"removed", "restored", "conflict", "pending-phase", "cached-account-restored"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			f.systemStopped()
			f.conflictConfiguration()
			targetID := "sub2api:ws1:a"
			key := "user1|ws1|" + targetID
			state := f.repo.states[targetID]["gpt-4o"]
			last := time.Now().Add(-25 * time.Hour)
			state.State, state.FailingSince, state.LastProbeAt = StateHealthy, nil, &last
			cache := make(adminInventoryCache)
			inventory, err := f.s.loadAdminInventory(context.Background(), "user1", "ws1", cache)
			if err != nil {
				t.Fatal(err)
			}
			target, found := findActionInventoryTarget(targetID, *inventory)
			if !found {
				t.Fatal("real full inventory did not contain fixture account")
			}
			target.TestMemberships = inventoryTestMemberships(*inventory, "a")
			target.TestConfiguration = ResolveGroupTestConfiguration(target.Platform, target.TestMemberships, true, f.repo.testConfigurations)
			specs := candidateModelSpecsForPlatform([]string{"gpt-4o"}, f.repo.policies, "sub2api", probeModelSource{}, true)
			scan := &schedulerScan{inventoryCache: cache, targetActionStates: []TargetActionState{f.repo.targetActionStates[key]}}
			f.s.collectLongFailureNotices(scan, "user1", "ws1", target, specs, []*ConnectionHealthState{&state}, []EffectiveProbeDecision{{}}, []bool{true}, time.Now())
			if len(scan.notices) != 1 || !scan.notices[0].unverifiable {
				t.Fatal("protection did not begin with a collected U fact")
			}
			checkpoint := f.repo.targetActionStates[key]
			switch kind {
			case "removed":
				delete(f.repo.targetActionStates, key)
			case "restored":
				checkpoint.LastAppliedStatus = "active"
				f.repo.targetActionStates[key] = checkpoint
			case "conflict":
				checkpoint.Conflict = true
				f.repo.targetActionStates[key] = checkpoint
			case "pending-phase":
				checkpoint.PendingDispatchPhase = DispatchSending
				f.repo.targetActionStates[key] = checkpoint
			case "cached-account-restored":
				for groupIndex := range inventory.groups {
					for index := range inventory.groups[groupIndex].accounts {
						if inventory.groups[groupIndex].accounts[index].ID == "a" {
							inventory.groups[groupIndex].accounts[index].Status = "active"
						}
					}
				}
			}
			f.s.publishLongFailureNotices(context.Background(), scan)
			if len(f.pub.snapshot()) != 0 || f.probes.Load() != 0 || len(f.actions.sub2APICalls) != 0 {
				t.Fatal("publication trusted a stale collected fact or changed the main site")
			}
		})
	}
}

func TestLongFailureAndUnverifiableNoticesIndependent(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.systemStopped()
	f.tick(t)
	f.conflictConfiguration()
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	last := time.Now().Add(-25 * time.Hour)
	state.LastProbeAt, state.LastAppliedProbeAt = &last, &last
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	inputs := f.pub.snapshot()
	if len(inputs) != 2 || inputs[0].Kind != "connection_health.long_failure" || inputs[1].Kind != "connection_health.disabled_unverified" {
		t.Fatalf("one account must retain L history and later receive its independent U: %+v", inputs)
	}
}

func TestLongFailureNoticeMainSiteStatusText(t *testing.T) {
	for _, tc := range []struct {
		status     string
		scheduling *bool
		want       string
	}{{"active", boolPointer(true), "仍是启用且可调度，可能还在接收请求"}, {"inactive", boolPointer(true), "已停用，目前不接收请求"}, {"active", boolPointer(false), "调度已关闭，目前不接收请求"}, {"", nil, "状态未能确认"}} {
		t.Run(tc.want, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			f.reader.accountsByGrp["g1"][0].Status, f.reader.accountsByGrp["g1"][0].Schedulable = tc.status, tc.scheduling
			f.tick(t)
			inputs := f.pub.snapshot()
			if len(inputs) != 1 || noticePathsDetail(inputs[0], "主站状态") != tc.want || !strings.Contains(inputs[0].Content.Summary, tc.want) {
				t.Fatalf("main-site status text mismatch: %+v", inputs)
			}
		})
	}
}

func TestAutoProbeRetryKind(t *testing.T) {
	for _, kind := range []string{"continuing", "config_blocked", "scheduling_blocked", "credential-retry"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			p := f.repo.policies[0]
			target := AdminProbeTarget{Platform: "sub2api", Schedulable: boolPointer(true), TestConfiguration: defaultTestConfiguration()}
			if kind == "config_blocked" {
				target.TestConfiguration.Status = "conflict"
			}
			if kind == "scheduling_blocked" {
				target.Schedulable, p.ContinueProbeWhenUnschedulable = boolPointer(false), false
			}
			if kind == "credential-retry" {
				now := time.Now()
				state.LastCredentialFailureAt = &now
			}
			models, _ := modelHealthForSpecs(map[string]ConnectionHealthState{"gpt-4o": state}, []probeModelSpec{{modelName: "gpt-4o", policy: p, policies: []Policy{p}}}, target, time.Now(), nil, true)
			raw, _ := json.Marshal(models[0])
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			want := kind
			if kind == "credential-retry" {
				want = "continuing"
			}
			if fields["longFailureRetry"] != want {
				t.Fatalf("response classification=%v want=%s", fields["longFailureRetry"], want)
			}
		})
	}
}

func TestLongFailureUsesProtocolProjection(t *testing.T) {
	f := newNoticePathsFixture(t)
	f.repo.testConfigurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10}}
	f.repo.policies[0].DailyProbeBudget = 1
	f.repo.budgetClaims["user1|ws1|notice|"+probeBudgetDayStart(time.Now()).Format(time.RFC3339)] = 1
	f.tick(t)
	if len(f.pub.snapshot()) != 0 {
		t.Fatal("old protocol's failure generated a new protocol notice")
	}
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	state.CounterProtocol, state.LastAppliedProbeProtocol, state.HealthEvidenceProtocol = protocolPointer(TestProtocolResponses), protocolPointer(TestProtocolResponses), protocolPointer(TestProtocolResponses)
	f.repo.states[state.ConnectionID][state.ModelName] = state
	f.tick(t)
	if len(f.pub.snapshot()) != 1 {
		t.Fatal("new protocol's own long failure did not generate a notice")
	}
}

func TestLongFailureNoticeOnlyForMonitoredCurrentModels(t *testing.T) {
	for _, kind := range []string{"unmonitored", "old-model", "in-flight"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			switch kind {
			case "unmonitored":
				f.repo.groupAssignments = nil
			case "old-model":
				p := f.repo.policies[0]
				p.ModelTargets = []ModelTarget{{ModelName: "new-model", Enabled: true}}
				f.repo.policies[0] = p
				f.reader.accountsByGrp["g1"][0].Models = "new-model"
				f.repo.budgetClaims["user1|ws1|notice|"+probeBudgetDayStart(time.Now()).Format(time.RFC3339)] = probeBudgetLimit(p)
			case "in-flight":
				f.s.schedulerMu.Lock()
				f.s.probeInFlight = map[string]string{"user1|sub2api:ws1:a": "user1|ws1"}
				f.s.schedulerMu.Unlock()
			}
			f.tick(t)
			if len(f.pub.snapshot()) != 0 {
				t.Fatalf("%s old state entered notification candidates", kind)
			}
		})
	}
}

func TestLongFailureNoticeCrossGroupExclusions(t *testing.T) {
	for _, retainMonitoring := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain-monitoring-%t", retainMonitoring), func(t *testing.T) {
			f := newNoticePathsFixture(t)
			f.repo.policies[0].AutoRemoteActionEnabled = true
			targetID := "sub2api:ws1:a"
			for _, groupID := range []string{"g2", "unmonitored"} {
				f.reader.groups = append(f.reader.groups, upstream.AdminGroupInfo{ID: groupID, Name: groupID})
				f.reader.accountsByGrp[groupID] = append([]upstream.AdminGroupAccountInfo(nil), f.reader.accountsByGrp["g1"]...)
			}
			f.s.platformGroups = f.reader
			f.repo.groupAssignments = append(f.repo.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", PolicyID: "notice"})
			f.repo.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: targetID}}
			if !retainMonitoring {
				f.repo.groupExclusions = append(f.repo.groupExclusions, GroupTargetExclusion{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", TargetID: targetID})
			}
			checkpointKey := "user1|ws1|" + targetID
			f.repo.targetActionStates[checkpointKey] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
			f.tick(t)
			_, remains := f.repo.targetActionStates[checkpointKey]
			if remains != retainMonitoring {
				t.Fatal("cross-group scope did not retain/abandon its conflict checkpoint correctly")
			}
			if len(f.actions.sub2APICalls) != 0 || f.probes.Load() != 0 {
				t.Fatal("scope change wrote main-site status or started an unnecessary probe")
			}
			for _, accounts := range f.reader.accountsByGrp {
				for _, account := range accounts {
					if account.ID == "a" && (account.Status != "active" || account.Schedulable == nil || !*account.Schedulable) {
						t.Fatal("scope change altered original main-site status/scheduling")
					}
				}
			}
			events, err := f.s.Events(context.Background(), "user1", targetID, 20)
			if err != nil {
				t.Fatal(err)
			}
			abandoned := 0
			for _, event := range events {
				if event.Result == "policy_unmanaged_abandoned" {
					abandoned++
				}
			}
			if retainMonitoring {
				inputs := f.pub.snapshot()
				if abandoned != 0 || len(inputs) != 1 || inputs[0].Kind != "connection_health.long_failure" {
					t.Fatal("one remaining monitored membership lost its independent long-failure notice")
				}
			} else if abandoned != 1 || len(f.pub.snapshot()) != 0 {
				t.Fatal("fully excluded target must expose its atomic abandonment audit without L/U notices")
			}
		})
	}
}

func TestNewAPINoLongFailureNotice(t *testing.T) {
	f := newNoticePathsFixture(t)
	state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
	delete(f.repo.states, state.ConnectionID)
	state.ConnectionID = "newapi:ws1:a"
	f.repo.states[state.ConnectionID] = map[string]ConnectionHealthState{state.ModelName: state}
	f.reader.accountsByGrp["g1"][0].BaseURL = "https://fixture.invalid"
	f.s.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	f.tick(t)
	if len(f.pub.snapshot()) != 0 || f.probes.Load() != 0 {
		t.Fatal("NewAPI must keep its cadence without Sub2API notices")
	}
}

func TestHealthDisabledKeepsProbeAndNoticeBlocked(t *testing.T) {
	for _, kind := range []string{"matching-on", "matching-off", "changed-on", "changed-off", "config-on", "config-off"} {
		t.Run(kind, func(t *testing.T) {
			f := newNoticePathsFixture(t)
			state := f.repo.states["sub2api:ws1:a"]["gpt-4o"]
			state.State = StateDisabled
			f.repo.states[state.ConnectionID][state.ModelName] = state
			if strings.HasSuffix(kind, "off") {
				f.reader.accountsByGrp["g1"][0].Schedulable = boolPointer(false)
				f.repo.policies[0].ContinueProbeWhenUnschedulable = false
			}
			if strings.HasPrefix(kind, "changed") {
				f.repo.testConfigurations = []GroupTestConfig{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 10}}
			}
			if strings.HasPrefix(kind, "config") {
				f.conflictConfiguration()
			}
			f.tick(t)
			groups, err := f.s.AdminGroups(context.Background(), "user1")
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range groups {
				for _, account := range group.Accounts {
					if account.ID == "a" {
						for _, model := range account.ModelHealth {
							raw, _ := json.Marshal(model)
							var fields map[string]any
							_ = json.Unmarshal(raw, &fields)
							if fields["longFailure"] == true || fields["longFailureRetry"] != nil {
								t.Fatal("raw disabled model was projected into a long-failure instruction")
							}
						}
					}
				}
			}
			if _, err := f.s.ProbeTarget(context.Background(), "user1", state.ConnectionID, []string{state.ModelName}); err == nil {
				t.Fatal("formal probe lifted a pre-existing health-disabled restriction")
			}
			current, err := f.repo.GetState(context.Background(), state.ConnectionID, state.ModelName)
			if err != nil || current == nil || current.State != StateDisabled || current.FailingSince == nil || !current.FailingSince.Equal(*state.FailingSince) || f.probes.Load() != 0 || len(f.pub.snapshot()) != 0 {
				t.Fatal("disabled probe/notice protection changed the state or failure origin")
			}
		})
	}
}

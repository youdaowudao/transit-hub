package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type taskATimedFrame struct {
	delay time.Duration
	text  string
}
type taskATimedReader struct {
	frames  []taskATimedFrame
	now     *time.Time
	pending string
}

func (r *taskATimedReader) Read(p []byte) (int, error) {
	if r.pending == "" {
		if len(r.frames) == 0 {
			return 0, io.EOF
		}
		frame := r.frames[0]
		r.frames = r.frames[1:]
		*r.now = r.now.Add(frame.delay)
		r.pending = frame.text
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func (r *taskATimedReader) Close() error { return nil }
func taskAStream(t *testing.T, protocol TestProtocol, frames []taskATimedFrame) ProbeOutcome {
	t.Helper()
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	runner := &RealProbeRunner{now: func() time.Time { return now }, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &taskATimedReader{frames: frames, now: &now}}, nil
	})}}
	return runner.Probe(context.Background(), ProbeRequest{Protocol: protocol, ProbeTimeoutSeconds: 30, BaseURL: "https://probe.invalid", ModelName: "m", MaxTokens: 256})
}

const taskAResponseComplete = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}]}}\n\n"

func TestTaskAStreamVisibleFirstTextAndSemanticEvent(t *testing.T) {
	out := taskAStream(t, TestProtocolResponses, []taskATimedFrame{
		{time.Second, "data: {\"type\":\"response.created\"}\n\n"},
		{time.Second, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"private function input\"}\n\n"},
		{time.Second, "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"hidden reasoning\"}\n\n"},
		{2 * time.Second, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"},
		{10 * time.Second, taskAResponseComplete},
	})
	if out.Result != ResultOK || out.FirstTokenMs == nil || *out.FirstTokenMs != 5000 || out.FirstEventMs == nil || *out.FirstEventMs != 2000 || out.LatencyMs != 5000 {
		t.Fatalf("first visible text/event/probe latency: %+v", out)
	}
	p := taskAV2Policy()
	if got := applyOutcomeDelay(out, p); got.Result != ResultOK {
		t.Fatalf("delay line must only see first visible text: %+v", got)
	}
	p.RuleVersion = RuleVersionLegacy
	if got := applyOutcomeDelay(out, p); got.Result != ResultOK {
		t.Fatal("legacy delay must use probe completion at first text")
	}
	out = taskAStream(t, TestProtocolResponses, []taskATimedFrame{{time.Second, "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"input\"}\n\n"}, {2 * time.Second, taskAResponseComplete}})
	if out.FirstTokenMs == nil || *out.FirstTokenMs != 3000 {
		t.Fatalf("no visible delta must fall back to full latency: %+v", out)
	}
}

func TestTaskAStreamingClassificationAndContent(t *testing.T) {
	cases := []struct {
		name     string
		protocol TestProtocol
		body     string
		want     ResultKey
	}{
		{"chat empty string", TestProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: [DONE]\n\n", ResultOK},
		{"chat reasoning", TestProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":\"stop\"}]}\n\n", ResultOK},
		{"chat array", TestProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}]}\n\ndata: [DONE]\n\n", ResultOK},
		{"chat empty array", TestProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"content\":[]}}]}\n\ndata: [DONE]\n\n", ResultInvalidResponse},
		{"chat no complete", TestProtocolChatCompletions, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", ResultOK},
		{"chat rejects malformed before done", TestProtocolChatCompletions, "data: broken\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n", ResultInvalidResponse},
		{"response incomplete", TestProtocolResponses, "data: {\"type\":\"response.incomplete\"}\n\n", ResultInvalidResponse},
		{"response refused", TestProtocolResponses, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"refusal\"}]}}\n\n", ResultInvalidResponse},
		{"response rate limit", TestProtocolResponses, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\"}}}\n\n", ResultRateLimited},
		{"response failure", TestProtocolResponses, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"internal_error\"}}}\n\n", ResultServerError},
		{"chat error", TestProtocolChatCompletions, "data: {\"error\":{\"code\":\"rate_limit_exceeded\"}}\n\n", ResultRateLimited},
		{"response lost completion", TestProtocolResponses, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n", ResultOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := taskAStream(t, c.protocol, []taskATimedFrame{{time.Second, c.body}})
			if out.Result != c.want {
				t.Fatalf("want %s got %+v", c.want, out)
			}
		})
	}
}

func TestTaskAProbePayloadFallbackAndOneDeadline(t *testing.T) {
	for _, protocol := range []TestProtocol{TestProtocolChatCompletions, TestProtocolResponses} {
		runner := &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
			var payload map[string]any
			_ = json.NewDecoder(req.Body).Decode(&payload)
			if payload["stream"] != true {
				t.Error("probe must stream")
			}
			tokenField := "max_tokens"
			if protocol == TestProtocolResponses {
				tokenField = "max_output_tokens"
			}
			if payload[tokenField] != float64(256) {
				t.Errorf("custom token count lost: %v", payload)
			}
			if _, ok := payload["reasoning"]; ok {
				t.Error("probe changed reasoning")
			}
			if _, ok := payload["reasoning_effort"]; ok {
				t.Error("probe changed reasoning")
			}
			deadline, ok := req.Context().Deadline()
			if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) < 29*time.Second {
				t.Error("single configured deadline missing")
			}
			body := `{"choices":[{"message":{"content":"hi"}}]}`
			if protocol == TestProtocolResponses {
				body = `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi"}]}]}`
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
		out := runner.Probe(context.Background(), ProbeRequest{Protocol: protocol, ProbeTimeoutSeconds: 30, BaseURL: "https://probe.invalid", MaxTokens: 256})
		if out.Result != ResultOK || !out.NonStreaming || out.FirstTokenMs == nil || out.FirstEventMs == nil || *out.FirstTokenMs != out.LatencyMs || *out.FirstEventMs != out.LatencyMs {
			t.Fatalf("nonstream fallback: %+v", out)
		}
		request, err := buildTestRequest(context.Background(), testRequestInput{Protocol: protocol, QuestionAnswer: true, BaseURL: "https://probe.invalid", ReasoningEffort: QuestionAnswerReasoningEffort("medium")})
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		_ = json.NewDecoder(request.Body).Decode(&payload)
		if payload["stream"] == true {
			t.Error("question answer must remain nonstream")
		}
	}
}

func TestTaskAFailingSinceResetAndNoAutoDegrade(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	p := taskAV2Policy()
	p.AutoDegradeEnabled = false
	current := ConnectionHealthState{State: StateHealthy, CurrentWeight: 100, CounterProtocol: protocolPointer(TestProtocolResponses), FailingSince: &old}
	next, _ := applyProbeOutcome(current, ProbeOutcome{Result: ResultServerError, Protocol: TestProtocolResponses}, p, now)
	if next.State != StateHealthy || next.FailingSince == nil || !next.FailingSince.Equal(old) {
		t.Fatalf("monitor-only failure must maintain first failure without state change: %+v", next)
	}
	next, _ = applyProbeOutcome(next, ProbeOutcome{Result: ResultSlowResponse, Protocol: TestProtocolResponses}, p, now.Add(time.Minute))
	if next.FailingSince != nil || next.State != StateHealthy {
		t.Fatal("any completed success must clear failure origin")
	}
	current.CounterProtocol = protocolPointer(TestProtocolChatCompletions)
	next, _ = applyProbeOutcome(current, ProbeOutcome{Result: ResultAuth, Protocol: TestProtocolResponses}, p, now)
	if next.FailingSince == nil || !next.FailingSince.Equal(now) {
		t.Fatal("protocol change must discard old failure origin")
	}
	current.RecheckPending = true
	next, _ = applyProbeOutcome(current, ProbeOutcome{Result: ResultInvalidResponse, Protocol: TestProtocolResponses}, p, now)
	if next.RecheckPending || next.FailingSince != nil || next.CounterProtocol == nil || *next.CounterProtocol != TestProtocolResponses || next.ConsecutiveFailures != 0 || next.ConsecutiveSuccesses != 0 {
		t.Fatal("invalid result on protocol change must consume recheck and clear old counters/origin")
	}
}

func TestTaskAModelTaskTruncation(t *testing.T) {
	ordinary := []probeModelSpec{}
	for i := 0; i < 100; i++ {
		ordinary = append(ordinary, probeModelSpec{modelName: fmt.Sprintf("normal-%d", i)})
	}
	ordinary = append(ordinary, probeModelSpec{modelName: "recheck", recheckPending: true})
	jobs := prioritizeAndLimitProbeJobs([]adminProbeJob{{target: AdminProbeTarget{TargetID: "one"}, models: ordinary, dueSpecs: ordinary}}, 100)
	if len(jobs) != 1 || len(jobs[0].dueSpecs) != 100 || jobs[0].dueSpecs[0].modelName != "recheck" || len(jobs[0].models) != 101 {
		t.Fatalf("recheck must survive model cap without losing all-model decision: %+v", jobs)
	}
	pending := []adminProbeJob{}
	for i := 0; i < 101; i++ {
		pending = append(pending, adminProbeJob{target: AdminProbeTarget{TargetID: fmt.Sprint(i)}, dueSpecs: []probeModelSpec{{modelName: "m", recheckPending: true}}})
	}
	jobs = prioritizeAndLimitProbeJobs(pending, 100)
	if len(jobs) != 100 || jobs[99].target.TargetID != "99" || !pending[100].dueSpecs[0].recheckPending {
		t.Fatal("stable rechecks over cap must remain for the next round")
	}
}

func TestTaskAV2PriorityIgnoresLatencyAndPreservesLegacy(t *testing.T) {
	makeCandidate := func(id string, multiplier float64, latency int, state State, version string) healthPriorityCandidate {
		return healthPriorityCandidate{targetID: id, multiplier: multiplier, healthBand: priorityHealthBand([]ConnectionHealthState{{State: state}}, 1), latencyMs: intPtr(latency), states: []ConnectionHealthState{{State: state, RuleVersion: version}}, ruleVersion: version}
	}
	a := makeCandidate("a", .13, 20000, StateSuspect, RuleVersionV2)
	b := makeCandidate("b", .13, 1000, StateHealthy, RuleVersionV2)
	c := makeCandidate("c", .14, 1, StateHealthy, RuleVersionV2)
	for _, input := range [][]healthPriorityCandidate{{a, b, c}, {c, b, a}, {b, c, a}} {
		sortHealthPriorityCandidates(input)
		if input[0].targetID != "a" || input[1].targetID != "b" || input[2].targetID != "c" {
			t.Fatal("v2 sorting depended on latency or input order")
		}
	}
	a.ruleVersion = RuleVersionLegacy
	a.states[0].RuleVersion = RuleVersionLegacy
	b.ruleVersion = RuleVersionLegacy
	b.states[0].RuleVersion = RuleVersionLegacy
	if compareHealthPriorityCandidates(a, b) <= 0 {
		t.Fatal("legacy comparator must retain latency ordering")
	}
	_, blocked, _ := aggregateTargetStates([]ConnectionHealthState{{State: StateDegraded, RuleVersion: RuleVersionV2, CurrentWeight: 0}, {State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100}})
	if blocked {
		t.Fatal("v2 zero display weight must not disable a usable multi-model account")
	}
	_, blocked, _ = aggregateTargetStates([]ConnectionHealthState{{State: StateDegraded, RuleVersion: RuleVersionV2, CurrentWeight: 0}, {State: StateSuspended, RuleVersion: RuleVersionV2}})
	if !blocked {
		t.Fatal("any suspended model must block account")
	}
}

func TestTaskALimiterVersionAndWorkspaceCaps(t *testing.T) {
	l := newProbeConcurrencyLimiter(12, 6)
	l.SetWorkspaceCap("a", 1, 1)
	l.SetWorkspaceCap("b", 3, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, ok := l.acquireAutomatic(ctx, "a")
	if !ok {
		t.Fatal("first a slot")
	}
	defer a()
	started := make(chan func(), 1)
	go func() {
		release, ok := l.acquireAutomatic(ctx, "a")
		if ok {
			started <- release
		}
	}()
	select {
	case release := <-started:
		release()
		t.Fatal("workspace cap not applied")
	case <-time.After(20 * time.Millisecond):
	}
	l.SetWorkspaceCap("a", 2, 3)
	select {
	case release := <-started:
		defer release()
	case <-ctx.Done():
		t.Fatal("queued task was not woken on cap increase")
	}
	l.SetWorkspaceCap("a", 1, 2)
	l.SetWorkspaceCap("a", 1, 1)
	l.mu.Lock()
	if l.workspaces["a"].cap != 2 || l.workspaces["a"].active != 2 || l.workspaces["b"].cap != 3 {
		t.Error("late settings overwrite or workspace leakage")
	}
	l.mu.Unlock()
	l.SetWorkspaceCap("a", 1, 4)
	l.mu.Lock()
	if l.workspaces["a"].active != 2 {
		t.Error("lowering cap canceled in-flight work")
	}
	l.mu.Unlock()
}

func TestTaskARoundEvidencePrecedesRestoreAndUnknownIsSeparate(t *testing.T) {
	s := &Service{}
	ctx, round := s.beginSchedulerRound(context.Background())
	p := taskAV2Policy()
	p.ID = "p"
	p.UserID = "u"
	p.AdminAccountID = "w"
	p.Enabled = true
	p.ModelTargets = []ModelTarget{{ModelName: "m", Enabled: true}}
	round.policies = []Policy{p}
	round.groups = []GroupPolicyAssignment{{UserID: "u", AdminAccountID: "w", AdminGroupID: "g", PolicyID: "p"}}
	inventory := adminWorkspaceInventory{groupsComplete: true, session: upstream.Session{Platform: upstream.PlatformSub2API}, groups: []adminInventoryGroup{{group: upstream.AdminGroupInfo{ID: "g"}, accounts: []upstream.AdminGroupAccountInfo{{ID: "a", Status: "inactive", Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: "m"}}}}}
	recordSchedulerInventory(ctx, "u", "w", inventory)
	updateAdminInventoryTargetState(&inventory, "a", "active", nil)
	recordSchedulerInventory(ctx, "u", "w", inventory)
	if round.zero["u|w|g"] != 1 || len(round.unknown) != 0 {
		t.Fatalf("restore erased prior zero evidence: %+v", round)
	}
	ctx, round = s.beginSchedulerRound(context.Background())
	round.policies = []Policy{p}
	round.groups = []GroupPolicyAssignment{{UserID: "u", AdminAccountID: "w", AdminGroupID: "g", PolicyID: "p"}}
	inventory.groups[0].accounts[0].TempUnschedulableKnown = false
	recordSchedulerInventory(ctx, "u", "w", inventory)
	if len(round.zero) != 0 || round.unknown["u|w|g"] != 1 {
		t.Fatal("unknown temporary state was counted as zero")
	}
	inventory.groups[0].err = errors.New("partial")
	ctx, round = s.beginSchedulerRound(context.Background())
	round.policies = []Policy{p}
	round.groups = []GroupPolicyAssignment{{UserID: "u", AdminAccountID: "w", AdminGroupID: "g", PolicyID: "p"}}
	recordSchedulerInventory(ctx, "u", "w", inventory)
	if len(round.zero) != 0 || round.unknown["u|w|g"] != 1 {
		t.Fatal("partial inventory was counted as zero")
	}
	s.finishSchedulerRound(round)
	value, _ := schedulerStatsByService.Load(s)
	stats := value.(*schedulerRoundStats)
	if stats.lastEnded.Before(round.started) || stats.total != 1 {
		t.Fatal("round end was not recorded")
	}
	schedulerStatsByService.Delete(s)
}

// taskASchedulerNextStart models StartScheduler's ticker created before its
// immediate round: ticks stay anchored to startup, and a slow round leaves one
// due tick to consume as soon as that synchronous round finishes.
func taskASchedulerNextStart(ended time.Time, nextTick *time.Time) time.Time {
	if ended.Before(*nextTick) {
		started := *nextTick
		*nextTick = nextTick.Add(schedulerTickInterval)
		return started
	}
	for !nextTick.After(ended) {
		*nextTick = nextTick.Add(schedulerTickInterval)
	}
	return ended
}

func TestTaskASchedulerColdStartConsumesDueTick(t *testing.T) {
	start := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	nextTick := start.Add(schedulerTickInterval)
	firstEnd := start.Add(150 * time.Second)
	secondStart := taskASchedulerNextStart(firstEnd, &nextTick)
	if !secondStart.Equal(firstEnd) {
		t.Fatalf("first 150s round leaves a due tick: second start=%s, expected=%s", secondStart, firstEnd)
	}
	secondEnd := secondStart.Add(time.Second)
	thirdStart := taskASchedulerNextStart(secondEnd, &nextTick)
	if !thirdStart.Equal(start.Add(180*time.Second)) || thirdStart.Before(secondEnd) {
		t.Fatalf("following round must wait for the next anchored tick without overlapping: start=%s end=%s", thirdStart, secondEnd)
	}
	t.Logf("scheduler startup clock: first round 0–150s, due round starts %s, following round starts %s", secondStart.Sub(start), thirdStart.Sub(start))
}

// The simulation uses production cadence, model sorting and grouping with the
// same startup clock as StartScheduler. Each request holds one of six slots
// until its configured end; rounds wait for all requests before taking a tick.
func TestTaskASchedulerSimulationSeventeenAndLongFailures(t *testing.T) {
	start := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	p := taskAV2Policy()
	p.Enabled = true
	p.ProbeIntervalSeconds = 60
	p.ContinueProbeWhenUnschedulable = true
	states := make([]ConnectionHealthState, 28)
	starts := make([][]time.Time, 28)
	old := start.Add(-25 * time.Hour)
	lastLong := start.Add(-time.Hour)
	for i := range states {
		states[i] = ConnectionHealthState{State: StateHealthy, CurrentWeight: 100, RuleVersion: RuleVersionV2, CounterProtocol: protocolPointer(TestProtocolResponses)}
		if i >= 17 {
			states[i].State = StateSuspended
			states[i].FailingSince = &old
			states[i].LastProbeAt = &lastLong
		}
	}
	now := start
	nextTick := start.Add(schedulerTickInterval)
	roundNumber := 0
	for now.Before(start.Add(70 * time.Minute)) {
		jobs := []adminProbeJob{}
		for i, state := range states {
			decision := calculateEffectiveProbeDecision([]Policy{p}, nil, &state, now)
			if decision.NextProbeAt != nil && !now.Before(*decision.NextProbeAt) {
				jobs = append(jobs, adminProbeJob{target: AdminProbeTarget{TargetID: fmt.Sprint(i)}, dueSpecs: []probeModelSpec{{modelName: "m", recheckPending: state.RecheckPending}}})
			}
		}
		jobs = prioritizeAndLimitProbeJobs(jobs, 100)
		available := [6]time.Time{}
		for i := range available {
			available[i] = now
		}
		roundEnd := now
		for _, job := range jobs {
			var index int
			_, _ = fmt.Sscan(job.target.TargetID, &index)
			slot := 0
			for i := 1; i < 6; i++ {
				if available[i].Before(available[slot]) {
					slot = i
				}
			}
			requestStart := available[slot]
			starts[index] = append(starts[index], requestStart)
			latency := time.Second
			result := ResultOK
			if index >= 17 || requestStart.Before(start.Add(3*time.Minute)) {
				latency = 30 * time.Second
				result = ResultNetworkFluctuation
			}
			ended := requestStart.Add(latency)
			states[index], _ = applyProbeOutcome(states[index], ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: result, LatencyMs: int(latency.Milliseconds())}, p, ended)
			available[slot] = ended
			if ended.After(roundEnd) {
				roundEnd = ended
			}
		}
		nextStart := taskASchedulerNextStart(roundEnd, &nextTick)
		if roundNumber == 0 && (!roundEnd.Equal(start.Add(150*time.Second)) || !nextStart.Equal(roundEnd)) {
			t.Fatalf("cold startup 28 due accounts must end at 150s and immediately consume the due tick: end=%s next=%s", roundEnd.Sub(start), nextStart.Sub(start))
		}
		roundNumber++
		now = nextStart
	}
	longest := time.Duration(0)
	for i := 0; i < 17; i++ {
		if len(starts[i]) < 40 {
			t.Fatalf("normal account %d missed ordinary cadence: %v", i, starts[i])
		}
		for j := 1; j < len(starts[i]); j++ {
			gap := starts[i][j].Sub(starts[i][j-1])
			if gap > longest {
				longest = gap
			}
			if gap > 150*time.Second {
				t.Fatalf("normal account %d interval %s exceeds 2.5 minutes", i, gap)
			}
		}
	}
	for i := 17; i < 28; i++ {
		if len(starts[i]) != 2 {
			t.Fatalf("long failed account %d missed its hourly turns: %v", i, starts[i])
		}
		if starts[i][1].Sub(starts[i][0]) < time.Hour {
			t.Fatalf("long failed account %d overprobed: %v", i, starts[i])
		}
	}
	t.Logf("28 accounts (17 normal + 11 long failed), concurrency 6, outage 3m: longest interval=%s", longest)
}

func TestTaskAV2PriorityWritesSameMultiplierSameValue(t *testing.T) {
	for _, order := range [][]string{{"a", "b", "c", "d"}, {"d", "c", "b", "a"}, {"b", "d", "a", "c"}} {
		repo := newFakeRepository()
		actions := &fakeTargetPriorityActioner{}
		service := &Service{repo: repo, priorityActions: actions}
		policy := taskAV2Policy()
		policy.ID = "p"
		policy.UserID = "user1"
		policy.AdminAccountID = "ws1"
		policy.Enabled = true
		policy.PriorityMode = PriorityModeMultiplier
		policy.ModelTargets = []ModelTarget{{ModelName: "m", Enabled: true}}
		inventory := map[string]*priorityTargetInventory{}
		states := []ConnectionHealthState{}
		for index, id := range order {
			multiplier := .13
			state := StateHealthy
			if id == "c" {
				multiplier = .14
			}
			if id == "d" {
				state = StateDegraded
			}
			if id == "b" {
				state = StateSuspect
			}
			targetID := "sub2api:ws1:" + id
			item := &priorityTargetInventory{target: AdminProbeTarget{TargetID: targetID, AccountID: id, Platform: string(upstream.PlatformSub2API), AccountStatus: "active", InventoryComplete: true, Models: []string{"m"}}, policies: []Policy{policy}, currentPriority: 50, priorityPresent: true, snapshotStartedAt: time.Now(), upstreamMultiplier: upstreamMultiplierResolution{status: MultiplierResolutionResolved, info: upstreamKeyGroupInfo{effectiveMultiplier: &multiplier}}}
			inventory[targetID] = item
			st := ConnectionHealthState{ConnectionID: targetID, ModelName: "m", UserID: "user1", AdminAccountID: "ws1", State: state, RuleVersion: RuleVersionV2, CurrentWeight: 100, LastSuccessLatencyMs: intPtr(1000 * (index + 1)), HealthEvidenceStatus: HealthEvidenceLegacy}
			states = append(states, st)
			repo.states[targetID] = map[string]ConnectionHealthState{"m": st}
		}
		service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, states, nil)
		got := map[string]int{}
		for _, call := range actions.calls {
			got[call.targetID] = call.priority
		}
		if got["a"] != 10 || got["b"] != 10 || got["c"] != 11 || got["d"] != 1000 {
			t.Fatalf("same multiplier same value regardless of input order: %v", got)
		}
	}
}

type taskABatchGateRepository struct {
	*fakeRepository
	generation int64
	captured   []int64
}

func (r *taskABatchGateRepository) ValidateTargetProbeBatch(_ context.Context, user, workspace, target string, models []string, generation int64) (bool, error) {
	r.captured = append(r.captured, generation)
	if generation == r.generation {
		return true, nil
	}
	for _, model := range models {
		if state, exists := r.states[target][model]; exists {
			state.RecheckPending = false
			r.states[target][model] = state
		}
	}
	return false, nil
}
func TestTaskACompletedBatchRejectsChangedGenerationAndABA(t *testing.T) {
	for _, generation := range []int64{2, 3} {
		repo := &taskABatchGateRepository{fakeRepository: newFakeRepository(), generation: generation}
		platform := &fakePlatformActioner{}
		service := &Service{repo: repo, dispatcher: newRemoteActionDispatcher(nil, nil, platform)}
		target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: string(upstream.PlatformSub2API), AccountID: "a", ConfigGeneration: 1, ConfigGenerationKnown: true, AccountStatus: "inactive", InventoryComplete: true, TestConfiguration: defaultTestConfiguration()}
		p := taskAV2Policy()
		p.ID = "p"
		p.Enabled = true
		p.ConfigGeneration = 1
		specs := []probeModelSpec{{modelName: "m", policy: p}, {modelName: "n", policy: p}}
		since := time.Now().Add(-time.Hour)
		state := ConnectionHealthState{ConnectionID: target.TargetID, ModelName: "m", UserID: "user1", AdminAccountID: "ws1", State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100, RecheckPending: true, ConsecutiveFailures: 1, FailingSince: &since, HealthEvidenceStatus: HealthEvidenceLegacy}
		repo.states[target.TargetID] = map[string]ConnectionHealthState{"m": state, "n": state}
		resultState := state
		resultState.State = StateHealthy
		results := []targetProbeResult{{state: &resultState, spec: specs[0], disposition: "applied", triggeredRemote: true, eventID: "event", outcome: ProbeOutcome{Result: ResultOK}}}
		err := service.finishTargetProbeBatch(context.Background(), "user1", "ws1", upstream.Session{Platform: upstream.PlatformSub2API}, target, specs, results, EventSourceManual)
		if err != nil || len(repo.captured) != 1 || repo.captured[0] != 1 || len(platform.sub2APICalls) != 0 || len(repo.events) != 0 {
			t.Fatalf("batch failed captured-generation gate: generation=%d err=%v captured=%v actions=%v", generation, err, repo.captured, platform.sub2APICalls)
		}
		for _, stored := range repo.states[target.TargetID] {
			if stored.RecheckPending || stored.State != StateSuspect || stored.ConsecutiveFailures != 1 || stored.FailingSince != &since || stored.HealthEvidenceStatus != HealthEvidenceLegacy {
				t.Fatal("changed batch replaced state beyond recheck clearing")
			}
		}
	}
}

func TestTaskAStreamingResponseLimitsAndFirstChoiceAcceptance(t *testing.T) {
	for _, body := range []string{"data: " + strings.Repeat("x", maxProbeResponseBytes+100) + "\n\n", strings.Repeat("data: {\"type\":\"keepalive\",\"extra\":\""+strings.Repeat("x", 1024)+"\"}\n\n", 1024)} {
		out := taskAStream(t, TestProtocolResponses, []taskATimedFrame{{time.Second, body}})
		if out.Result != ResultInvalidResponse {
			t.Fatalf("oversized SSE must be invalid, not a counted transport failure: %+v", out)
		}
	}
	out := taskAStream(t, TestProtocolChatCompletions, []taskATimedFrame{{time.Second, "data: {\"choices\":[{\"delta\":{}},{\"delta\":{\"content\":\"second choice\"}}]}\n\ndata: [DONE]\n\n"}})
	if out.Result != ResultInvalidResponse {
		t.Fatal("streaming acceptance differs from first-choice nonstream validation")
	}
}

type taskASettingsReaderRepository struct {
	*fakeRepository
	settings WorkspaceHealthSettings
	reads    int
}

func (r *taskASettingsReaderRepository) GetWorkspaceHealthSettings(_ context.Context, user, workspace string) (WorkspaceHealthSettings, error) {
	r.reads++
	settings := r.settings
	settings.UserID = user
	settings.AdminAccountID = workspace
	return settings, nil
}
func TestTaskAFirstManualProbeReadsWorkspaceCapAndRejectsOlderRead(t *testing.T) {
	base := newFakeRepository()
	p := sub2APIProbePolicy(false)
	p.RuleVersion = RuleVersionV2
	base.policies = []Policy{p}
	targetID := "sub2api:ws1:a"
	assignPolicyToTarget(base, p, targetID)
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g": {{ID: "a", Status: "active", Schedulable: boolPointer(true), Models: "gpt-4o"}}}, credErr: map[string]error{"a": errors.New("credential unavailable")}}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, base)
	repo := &taskASettingsReaderRepository{fakeRepository: base, settings: WorkspaceHealthSettings{RuleVersion: RuleVersionV2, ProbeConcurrency: 3, ProbeConcurrencyVersion: 7}}
	service.repo = repo
	service.SetProbeGlobalConcurrency(11)
	if _, err := service.ProbeTarget(context.Background(), "user1", targetID, nil); err == nil {
		t.Fatal("fixture credential failure was not returned")
	}
	limiter := service.sharedProbeLimiter()
	limiter.mu.Lock()
	usage := limiter.workspaces["user1|ws1"]
	if repo.reads != 1 || usage == nil || usage.cap != 3 || usage.capVersion != 7 || usage.active != 0 || limiter.globalCap != 11 {
		t.Error("first manual request failed to load workspace and global caps")
	}
	limiter.mu.Unlock()
	service.applyWorkspaceProbeSettings(WorkspaceHealthSettings{UserID: "user1", AdminAccountID: "ws1", ProbeConcurrency: 4, ProbeConcurrencyVersion: 9})
	repo.settings.ProbeConcurrency, repo.settings.ProbeConcurrencyVersion = 1, 8
	if _, err := service.ProbeTarget(context.Background(), "user1", targetID, nil); err == nil {
		t.Fatal("fixture credential failure was not returned")
	}
	limiter.mu.Lock()
	if usage.cap != 4 || usage.capVersion != 9 {
		t.Fatal("older delayed read replaced a newer save")
	}
	limiter.mu.Unlock()
}

func TestTaskACollectedBudgetIsEstimateAndConsumedAfterCredentialSuccess(t *testing.T) {
	repo := newFakeRepository()
	first := sub2APIProbePolicy(false)
	first.ID = "a"
	first.RuleVersion = RuleVersionV2
	first.DailyProbeBudget = 1
	first.ProbeIntervalSeconds = 60
	first.ModelTargets = []ModelTarget{{ModelName: "m", Enabled: true, MaxProbeTokens: 1}, {ModelName: "n", Enabled: true, MaxProbeTokens: 1}}
	second := first
	second.ID = "b"
	second.DailyProbeBudget = 10
	repo.policies = []Policy{first, second}
	targetID := "sub2api:ws1:a"
	assignPolicyToTarget(repo, first, targetID)
	assignPolicyToTarget(repo, second, targetID)
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g": {{ID: "a", Status: "active", Schedulable: boolPointer(true), Models: "m,n"}}}, credErr: map[string]error{"a": errors.New("credential unavailable")}}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	jobs := service.collectAdminProbeJobs(context.Background(), repo.policies, repo.assignments)
	if len(jobs) != 1 || len(jobs[0].dueSpecs) != 2 || len(repo.budgetClaims) != 0 {
		t.Fatal("collection consumed budget or lost model tasks")
	}
	if jobs[0].dueSpecs[0].budgetPolicy.ID != "a" || jobs[0].dueSpecs[1].budgetPolicy.ID != "b" {
		t.Fatal("multiple budget sources were not retained")
	}
	j := jobs[0]
	j.floorGuard = newWorkspaceFloorGuard()
	var wg sync.WaitGroup
	wg.Add(1)
	service.runAdminProbeJob(context.Background(), j, func() {}, &wg)
	wg.Wait()
	if len(repo.budgetClaims) != 0 {
		t.Fatal("credential failure consumed probe budget")
	}
	hits := 0
	service.probeRunner = &RealProbeRunner{now: time.Now, client: &http.Client{Transport: taskATransport(func(req *http.Request) (*http.Response, error) {
		hits++
		claimed := 0
		for _, count := range repo.budgetClaims {
			claimed += count
		}
		if claimed != hits {
			t.Errorf("request %d sent before exactly one budget reservation: claims=%d", hits, claimed)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})}}
	for _, spec := range j.dueSpecs {
		if _, err := service.probeTargetOnce(context.Background(), "user1", "ws1", j.target, upstream.ProbeCredential{BaseURL: "https://probe.invalid", Key: "fixture"}, spec, true); err != nil {
			t.Fatal(err)
		}
	}
	claims := 0
	for _, count := range repo.budgetClaims {
		claims += count
	}
	if hits != 2 || claims != 2 {
		t.Fatalf("model tasks must each consume once: hits=%d claims=%d", hits, claims)
	}
}

type taskARuleServiceRepository struct {
	*fakeRepository
	switches []string
}

func (r *taskARuleServiceRepository) GetWorkspaceHealthSettings(_ context.Context, user, workspace string) (WorkspaceHealthSettings, error) {
	return defaultWorkspaceHealthSettings(user, workspace), nil
}
func (r *taskARuleServiceRepository) ListRulePresets(context.Context, string, string) ([]RulePreset, error) {
	return []RulePreset{}, nil
}
func (r *taskARuleServiceRepository) SaveRulePreset(_ context.Context, p RulePreset) (RulePreset, error) {
	return p, nil
}
func (r *taskARuleServiceRepository) DeleteRulePreset(context.Context, string, string, string) error {
	return nil
}
func (r *taskARuleServiceRepository) ApplyRulePresetToAll(_ context.Context, user, workspace, id string) (WorkspaceHealthSettings, error) {
	return defaultWorkspaceHealthSettings(user, workspace), nil
}
func (r *taskARuleServiceRepository) SwitchWorkspaceRule(_ context.Context, user, workspace, rule string) (WorkspaceHealthSettings, error) {
	r.switches = append(r.switches, rule)
	settings := defaultWorkspaceHealthSettings(user, workspace)
	settings.RuleVersion = rule
	return settings, nil
}
func (r *taskARuleServiceRepository) SaveWorkspaceProbeConcurrency(_ context.Context, user, workspace string, cap int, version int64) (WorkspaceHealthSettings, error) {
	return defaultWorkspaceHealthSettings(user, workspace), nil
}
func TestTaskARuleSwitchDoesNotAdmitImmediateRemoteSync(t *testing.T) {
	base := newFakeRepository()
	repo := &taskARuleServiceRepository{fakeRepository: base}
	service := newAdminGroupsService(fakePlatformGroupReader{}, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, base)
	service.repo = repo
	actions := &fakeTargetPriorityActioner{}
	service.priorityActions = actions
	for _, rule := range []string{RuleVersionLegacy, RuleVersionV2} {
		result, err := service.SwitchWorkspaceRule(context.Background(), "user1", rule)
		if err != nil || result.RuleVersion != rule {
			t.Fatalf("switch failed: %+v %v", result, err)
		}
	}
	service.priorityTriggerMu.Lock()
	admitted := service.priorityHealthRunning != nil || service.priorityHealthPending != nil
	service.priorityTriggerMu.Unlock()
	if admitted || len(actions.calls) != 0 || len(base.events) != 0 || len(repo.switches) != 2 {
		t.Fatal("rule conversion admitted immediate priority/remote synchronization")
	}
}

package connection_health

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestManualSettingsFullChainAuditIsSeparateFromHealth(t *testing.T) {
	for _, kind := range []string{"priority-owner", "concurrency"} {
		for _, outcome := range []string{"applied", upstream.MutationNotSent, upstream.MutationConfirmedRejected, upstream.MutationUncertain, "read-failed"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				s, r, f := taskBAccountAPIFixture(20, 1)
				generation := r.fakeWorkspaceHealthSettings("user1", "ws1").ConfigGeneration
				if outcome != "applied" && outcome != "read-failed" {
					f.writeErr = &upstream.RequestError{MessageKey: "fixture.write", MutationOutcome: outcome}
				}
				f.readFailAfterWrite = outcome == "read-failed"
				body := `{"mode":"manual","priority":5}`
				if kind == "concurrency" {
					body = `{"concurrency":12}`
				}
				want := AccountEditPending
				if outcome == "applied" {
					want = AccountEditSuccess
				} else if outcome == upstream.MutationNotSent || outcome == upstream.MutationConfirmedRejected {
					want = AccountEditNotSent
				}
				taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", kind, body), want)
				waitForPriorityAsyncIdle(t)
				if len(r.states) != 0 || len(r.budgetClaims) != 0 || f.credentials != 0 || len(r.events) != 1 {
					t.Fatalf("account edit entered health path: events=%v states=%v budget=%v credentials=%d", r.events, r.states, r.budgetClaims, f.credentials)
				}
				e := r.events[0]
				action := PriorityManualSetAction
				if kind == "concurrency" {
					action = ConcurrencySetAction
				}
				if want != AccountEditSuccess {
					if kind == "concurrency" {
						action = ConcurrencySetFailedAction
					} else {
						action = PriorityManualSetFailedAction
					}
				}
				if e.Result != "account_edit_"+want || e.RemoteAction != action || e.Source != EventSourceManual || e.ActionSource != ActionSourceUser || e.ModelName != "*" || e.FromState != "" || e.ToState != "" || e.ConnectionID != "sub2api:ws1:a" {
					t.Fatalf("incorrect audit or fabricated health transition: %+v", e)
				}
				delta := int64(0)
				if kind == "priority-owner" {
					delta = 1
				}
				if got := r.fakeWorkspaceHealthSettings("user1", "ws1").ConfigGeneration; got != generation+delta {
					t.Fatalf("config generation=%d want=%d", got, generation+delta)
				}
			})
		}
	}
	// An already automatic value is a read-only result, not a user write audit.
	s, r, f := taskBAccountAPIFixture(20, 1)
	before := r.fakeWorkspaceHealthSettings("user1", "ws1").ConfigGeneration
	taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`), AccountEditNoop)
	if len(r.events) != 0 || len(f.priorityWrites) != 0 || r.fakeWorkspaceHealthSettings("user1", "ws1").ConfigGeneration != before {
		t.Fatal("noop registered a write or invalidated the configuration")
	}
}

func TestManualSettingsFullChainReceiptStoreFailureBlocksOnlyTransition(t *testing.T) {
	for _, entry := range []string{"health", "page"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed-%v", entry, mixed), func(t *testing.T) {
				s, r, f := fullChainSettingsFixture(50, 1)
				s.repo = &stageAReservationFaultRepository{fakeRepository: r, receiptErr: errors.New("fixture uncommitted receipt")}
				wantActual, wantHealthRows := 5, 0
				if entry == "health" {
					fullChainHealthState(r, RuleVersionV2, StateHealthy)
					fullChainSync(t, s, r)
					wantActual, wantHealthRows = 10, 1
				} else {
					taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), AccountEditPending)
				}
				before := r.priorityStates["user1|ws1|sub2api:ws1:a"]
				if before.LastAppliedPriority != 0 || before.OriginalPriority != 50 || before.PendingDispatchPhase != DispatchSending || (entry == "page" && !strings.HasPrefix(before.PendingDispatchID, "priority-release:")) {
					t.Fatalf("uncommitted receipt lost durable uncertainty: %+v", before)
				}
				p := r.policies[0]
				only := p
				only.ID, only.StrategyMode = "only", StrategyModeMultiplierOnly
				r.policies, r.assignments = []Policy{only}, nil
				assignPolicyToTarget(r, only, "sub2api:ws1:a")
				if mixed {
					r.policies = append(r.policies, p)
					assignPolicyToTarget(r, p, "sub2api:ws1:a")
				}
				r.bumpFakeConfigGeneration("user1", "ws1")
				fullChainSync(t, s, r)
				fullChainSync(t, s, r)
				if !reflect.DeepEqual(r.priorityStates["user1|ws1|sub2api:ws1:a"], before) || len(f.priorityWrites) != 1 || f.priority != wantActual || len(r.states) != wantHealthRows {
					t.Fatalf("only transition erased/replayed uncommitted receipt: state=%+v calls=%v", r.priorityStates["user1|ws1|sub2api:ws1:a"], f.priorityWrites)
				}
			})
		}
	}
}

func TestManualSettingsFullChainAutoQualificationUsesChosenModelPolicy(t *testing.T) {
	for _, variant := range []string{"monitor-only-wins", "disabled-model-ignored", "different-model-ignored", "mixed-only-rejected"} {
		t.Run(variant, func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(5, 1)
			sorter := r.policies[0]
			sorter.ID, sorter.AutoRemoteActionEnabled = "z-sorter", true
			peer := sorter
			peer.ID, peer.AutoDegradeEnabled, peer.AutoRemoteActionEnabled = "a-monitor", false, false
			want := 400
			switch variant {
			case "disabled-model-ignored":
				peer.ModelTargets = []ModelTarget{{ModelName: "gpt-4o", Enabled: false}}
				want = 200
			case "different-model-ignored":
				peer.ModelTargets = []ModelTarget{{ModelName: "not-applicable", Enabled: true}}
				want = 200
			case "mixed-only-rejected":
				peer.StrategyMode = StrategyModeMultiplierOnly
			}
			r.policies, r.assignments = []Policy{sorter, peer}, nil
			assignPolicyToTarget(r, sorter, "sub2api:ws1:a")
			assignPolicyToTarget(r, peer, "sub2api:ws1:a")
			rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`)
			waitForPriorityAsyncIdle(t)
			if rec.Code != want || f.credentials != 0 || len(r.states) != 0 || f.concurrencyWrites != 0 {
				t.Fatalf("merged-model eligibility status=%d want=%d body=%s actual=%+v", rec.Code, want, rec.Body.String(), f)
			}
			if want == 200 {
				taskBRequireAccountResult(t, rec, AccountEditSuccess)
				if !reflect.DeepEqual(f.priorityWrites, []int{10}) || f.priority != 10 {
					t.Fatalf("eligible merged model did not write only temporary primary value: %+v", f)
				}
			} else if len(f.priorityWrites) != 0 || len(r.priorityStates) != 0 || len(r.events) != 0 {
				t.Fatalf("ineligible chosen model wrote or audited success: %+v events=%v", f, r.events)
			}
		})
	}
}

func TestManualSettingsFullChainMalformedAndBoundaries(t *testing.T) {
	for _, tc := range []struct{ kind, body string }{
		{"priority-owner", `not-json`}, {"priority-owner", `{"mode":"manual","priority":null}`}, {"priority-owner", `{"mode":"manual","priority":1.5}`}, {"priority-owner", `{"mode":"manual","priority":"5"}`}, {"priority-owner", `{"mode":"manual","priority":0}`}, {"priority-owner", `{"mode":"auto","priority":5}`},
		{"concurrency", `not-json`}, {"concurrency", `{"concurrency":null}`}, {"concurrency", `{"concurrency":1.5}`}, {"concurrency", `{"concurrency":"5"}`}, {"concurrency", `{"concurrency":0}`},
	} {
		t.Run(tc.kind+"/"+tc.body, func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(20, 1)
			rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", tc.kind, tc.body)
			if rec.Code != 400 || len(f.priorityWrites) != 0 || f.concurrencyWrites != 0 || f.credentials != 0 || len(r.events) != 0 || len(r.states) != 0 {
				t.Fatalf("malformed input admitted side effects: status=%d body=%s actual=%+v", rec.Code, rec.Body.String(), f)
			}
		})
	}
	for _, tc := range []struct {
		kind, body string
		value      int
	}{{"priority-owner", `{"mode":"manual","priority":1}`, 1}, {"priority-owner", `{"mode":"manual","priority":9}`, 9}, {"concurrency", `{"concurrency":1}`, 1}, {"concurrency", `{"concurrency":1000}`, 1000}} {
		t.Run(fmt.Sprintf("%s/boundary-%d", tc.kind, tc.value), func(t *testing.T) {
			s, r, f := taskBAccountAPIFixture(20, 1)
			taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", tc.kind, tc.body), AccountEditSuccess)
			waitForPriorityAsyncIdle(t)
			if tc.kind == "priority-owner" && (f.priority != tc.value || !reflect.DeepEqual(f.priorityWrites, []int{tc.value}) || f.concurrencyWrites != 0) {
				t.Fatalf("manual boundary not written exactly: %+v", f)
			}
			if tc.kind == "concurrency" && (f.concurrency != tc.value || f.load == nil || *f.load != tc.value || f.priority != 20 || len(f.priorityWrites) != 0) {
				t.Fatalf("concurrency boundary or related load not confirmed: %+v", f)
			}
			if f.credentials != 0 || len(r.states) != 0 || len(r.priorityStates) != 0 {
				t.Fatal("confirmed boundary action retained an unrelated health record")
			}
		})
	}
}

func TestManualSettingsFullChainUncertainHealthAndPageNeverReplayAfterOnlySwitch(t *testing.T) {
	for _, entry := range []string{"health", "page"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed-%v", entry, mixed), func(t *testing.T) {
				s, r, f := fullChainSettingsFixture(50, 1)
				f.writeErr = &upstream.RequestError{MessageKey: "fixture.uncertain", MutationOutcome: upstream.MutationUncertain}
				if entry == "health" {
					fullChainHealthState(r, RuleVersionV2, StateHealthy)
					fullChainSync(t, s, r)
				} else {
					taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), AccountEditPending)
				}
				before := r.priorityStates["user1|ws1|sub2api:ws1:a"]
				if before.PendingDispatchPhase != DispatchUncertain || before.LastAppliedPriority != 0 || before.OriginalPriority != 50 || len(f.priorityWrites) != 1 {
					t.Fatalf("first uncertain write lost real origin: %+v calls=%v", before, f.priorityWrites)
				}
				p := r.policies[0]
				only := p
				only.ID, only.StrategyMode = "only", StrategyModeMultiplierOnly
				r.policies, r.assignments = []Policy{only}, nil
				assignPolicyToTarget(r, only, "sub2api:ws1:a")
				if mixed {
					r.policies = append(r.policies, p)
					assignPolicyToTarget(r, p, "sub2api:ws1:a")
				}
				r.bumpFakeConfigGeneration("user1", "ws1")
				f.writeErr = nil
				// A matching later value is not a substitute for a success receipt.
				f.priority = *before.PendingPriority
				fullChainSync(t, s, r)
				fullChainSync(t, s, r)
				if !reflect.DeepEqual(r.priorityStates["user1|ws1|sub2api:ws1:a"], before) || len(f.priorityWrites) != 1 {
					t.Fatalf("uncertain switch was erased or resent: before=%+v after=%+v calls=%v", before, r.priorityStates["user1|ws1|sub2api:ws1:a"], f.priorityWrites)
				}
			})
		}
	}
}

type fullChainLeaseObserver struct {
	*fakeRepository
	mu               sync.Mutex
	busy             bool
	keys             []string
	workspaceBudget  time.Duration
	workspaceExpired bool
	secondTarget     chan struct{}
	targetAttempts   int
}

func (r *fullChainLeaseObserver) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	r.mu.Lock()
	r.keys = append(r.keys, key)
	if strings.HasPrefix(key, "connection-health:target:") {
		r.targetAttempts++
		if r.targetAttempts == 2 && r.secondTarget != nil {
			close(r.secondTarget)
		}
	}
	busy := r.busy && strings.HasPrefix(key, "connection-health:priority-sync:")
	if busy {
		deadline, _ := ctx.Deadline()
		r.workspaceBudget = time.Until(deadline)
	}
	r.mu.Unlock()
	if busy {
		<-ctx.Done()
		r.mu.Lock()
		r.workspaceExpired = errors.Is(ctx.Err(), context.DeadlineExceeded)
		r.mu.Unlock()
		return nil, false, ctx.Err()
	}
	return r.fakeRepository.AcquireActionLease(ctx, key, wait)
}

func TestManualSettingsFullChainWorkspaceBusyNeverStartsAccountPreparation(t *testing.T) {
	s, r, f := taskBAccountAPIFixture(50, 1)
	observer := &fullChainLeaseObserver{fakeRepository: r, busy: true}
	s.repo = observer
	rec := taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), string(ErrorPrioritySyncBusy)) {
		t.Fatalf("busy workspace response=%d %s", rec.Code, rec.Body.String())
	}
	if len(observer.keys) != 1 || observer.targetAttempts != 0 || !observer.workspaceExpired || observer.workspaceBudget < 4*time.Second || observer.workspaceBudget > 5*time.Second || len(r.actionLeases) != 0 || len(r.priorityStates) != 0 || len(r.events) != 0 || len(f.priorityWrites) != 0 || f.credentials != 0 {
		t.Fatalf("workspace contention consumed account preparation: keys=%v budget=%v actual=%+v", observer.keys, observer.workspaceBudget, f)
	}
}

type fullChainBlockedConcurrency struct {
	*taskBAccountSettingsPlatform
	muCalls sync.Mutex
	calls   []int
	loads   []*int
	first   chan struct{}
	release chan struct{}
}

func (f *fullChainBlockedConcurrency) UpdateSub2APIAdminAccountConcurrencyContext(ctx context.Context, session upstream.Session, id string, n int, load *int) error {
	f.muCalls.Lock()
	f.calls = append(f.calls, n)
	f.loads = append(f.loads, cloneIntPointer(load))
	first := len(f.calls) == 1
	f.muCalls.Unlock()
	if first {
		close(f.first)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.release:
		}
	}
	return f.taskBAccountSettingsPlatform.UpdateSub2APIAdminAccountConcurrencyContext(ctx, session, id, n, load)
}

func TestManualSettingsFullChainConcurrencySerializesSameAccountAndKeepsPriority(t *testing.T) {
	s, r, f := taskBAccountAPIFixture(20, 1)
	observer := &fullChainLeaseObserver{fakeRepository: r, secondTarget: make(chan struct{})}
	s.repo = observer
	writer := &fullChainBlockedConcurrency{taskBAccountSettingsPlatform: f, first: make(chan struct{}), release: make(chan struct{})}
	s.concurrencyActions = writer
	done := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(writer.release) }) }
	defer func() { release(); workers.Wait() }()
	workers.Add(1)
	go func() {
		defer workers.Done()
		done <- taskBAccountRequest(s, "user1", "sub2api:ws1:a", "concurrency", `{"concurrency":12}`)
	}()
	fullChainAwait(t, writer.first)
	workers.Add(1)
	go func() {
		defer workers.Done()
		done <- taskBAccountRequest(s, "user1", "sub2api:ws1:a", "concurrency", `{"concurrency":22}`)
	}()
	fullChainAwait(t, observer.secondTarget)
	writer.muCalls.Lock()
	blockedCalls := append([]int(nil), writer.calls...)
	writer.muCalls.Unlock()
	release()
	for range 2 {
		taskBRequireAccountResult(t, <-done, AccountEditSuccess)
	}
	if !reflect.DeepEqual(blockedCalls, []int{12}) || !reflect.DeepEqual(writer.calls, []int{12, 22}) || len(writer.loads) != 2 || writer.loads[0] == nil || *writer.loads[0] != 12 || writer.loads[1] == nil || *writer.loads[1] != 22 {
		t.Fatalf("same-account requests overlapped or lost narrow load intent: blocked=%v calls=%v loads=%v", blockedCalls, writer.calls, writer.loads)
	}
	if f.concurrency != 22 || f.load == nil || *f.load != 22 || f.priority != 20 || len(f.priorityWrites) != 0 || f.credentials != 0 || len(r.states) != 0 || len(r.priorityStates) != 0 || len(r.targetActionStates) != 0 || len(r.events) != 2 || len(r.actionLeases) != 0 {
		t.Fatalf("serial edits changed unrelated state or leaked authority: actual=%+v states=%v audits=%v leases=%v", f, r.states, r.events, r.actionLeases)
	}
	for _, event := range r.events {
		if event.Result != "account_edit_success" || event.RemoteAction != ConcurrencySetAction || event.FromState != "" || event.ToState != "" {
			t.Fatalf("concurrency produced health transition: %+v", event)
		}
	}
}

func fullChainAwait(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
		t.Fatal("controlled fixture barrier was not reached")
	}
}

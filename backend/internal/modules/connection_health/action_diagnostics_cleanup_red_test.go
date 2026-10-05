package connection_health

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func actionCleanupNow() time.Time     { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
func actionCleanupInt(value int) *int { return &value }

func actionCleanupPair(id string, now time.Time) RemoteActionCheckpoints {
	return RemoteActionCheckpoints{
		Priority: &PrioritySyncState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:" + id, OriginalPriority: 10, LastAppliedPriority: 20, UpdatedAt: now.Add(-35 * time.Minute)},
		Target:   &TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: "sub2api:w:" + id, OriginalStatus: "active", LastAppliedStatus: "inactive", UpdatedAt: now.Add(-35 * time.Minute)},
	}
}

func actionCleanupPending(pair *RemoteActionCheckpoints, kind string, phase RemoteDispatchPhase, updated time.Time) {
	if kind == ActionKindPriority {
		pair.Priority.PendingPriority = actionCleanupInt(30)
		pair.Priority.PendingDispatchID, pair.Priority.PendingOwnerID, pair.Priority.PendingDispatchPhase = "red-priority-dispatch", "owner", phase
		pair.Priority.UpdatedAt = updated
	} else {
		pair.Target.PendingStatus = "active"
		pair.Target.PendingDispatchID, pair.Target.PendingOwnerID, pair.Target.PendingDispatchPhase = "red-target-dispatch", "owner", phase
		pair.Target.UpdatedAt = updated
	}
}

func TestActionDiagnosticsCleanupRED1DisplayRules(t *testing.T) {
	now := actionCleanupNow()
	for _, phase := range []RemoteDispatchPhase{DispatchPrepared, DispatchSending, DispatchConfirmedApplied, DispatchNotSent, DispatchConfirmedRejected} {
		for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
			for _, age := range []time.Duration{0, 5 * time.Minute, 5*time.Minute + time.Nanosecond} {
				t.Run(string(phase)+"/"+kind+"/"+age.String(), func(t *testing.T) {
					pair := actionCleanupPair("1", now)
					actionCleanupPending(&pair, kind, phase, now.Add(-age))
					got := actionPendingView(pair.Priority, pair.Target, now)
					if age <= 5*time.Minute {
						if got != nil {
							t.Fatalf("normal action must be hidden: %+v", got)
						}
					} else if got == nil || got.Reason != "overdue" {
						t.Fatalf("overdue action=%+v", got)
					}
				})
			}
		}
	}
	for _, tc := range []struct {
		name, reason string
		setup        func(*RemoteActionCheckpoints)
	}{
		{"conflict_confirmed", "conflict", func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchConfirmedApplied, now)
			p.Priority.Conflict = true
		}},
		{"uncertain", "uncertain", func(p *RemoteActionCheckpoints) { actionCleanupPending(p, ActionKindTarget, DispatchUncertain, now) }},
		{"legacy", "legacy", func(p *RemoteActionCheckpoints) { p.Target.PendingStatus = "active" }},
		{"dual", "dual_claim", func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchSending, now)
			actionCleanupPending(p, ActionKindTarget, DispatchSending, now)
		}},
		{"uncertain_before_dual", "uncertain", func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchSending, now)
			actionCleanupPending(p, ActionKindTarget, DispatchUncertain, now)
		}},
		{"legacy_before_dual", "legacy", func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchSending, now)
			p.Target.PendingStatus = "active"
		}},
		{"conflict_before_uncertain", "conflict", func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindTarget, DispatchUncertain, now)
			p.Target.Conflict = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := actionCleanupPair("1", now)
			tc.setup(&p)
			got := actionPendingView(p.Priority, p.Target, now)
			if got == nil || got.Reason != tc.reason {
				t.Fatalf("want %s got %+v", tc.reason, got)
			}
		})
	}
}

func actionCleanupRemember(s *Service, now time.Time, complete bool, accounts ...upstream.AdminGroupAccountInfo) {
	i := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: complete, snapshotStartedAt: now, groups: []adminInventoryGroup{{accounts: accounts}}}
	s.rememberActionInventory("u", "w", &i)
}

func actionCleanupStore(repo *fakeRepository, pair RemoteActionCheckpoints) {
	if pair.Priority != nil {
		repo.priorityStates[pair.Priority.UserID+"|"+pair.Priority.AdminAccountID+"|"+pair.Priority.TargetID] = *pair.Priority
	}
	if pair.Target != nil {
		repo.targetActionStates[pair.Target.UserID+"|"+pair.Target.AdminAccountID+"|"+pair.Target.TargetID] = *pair.Target
	}
}

func TestActionDiagnosticsCleanupRED2OneRowAndInventory(t *testing.T) {
	now := actionCleanupNow()
	for _, tc := range []struct {
		name, reason, accountName  string
		complete, missing, pending bool
		conclusion                 *actionAccountConclusion
		failures                   int
		unavailable                bool
	}{
		{"visible", "uncertain", "分组账号", true, false, true, nil, 0, false},
		{"visible_normal_hidden", "", "", true, false, false, nil, 0, false},
		{"unknown_departed_pending", "target_unverified", "", true, true, true, nil, 0, false},
		{"unknown_departed_idle", "", "", true, true, false, nil, 0, false},
		{"deleted", "", "", true, true, true, &actionAccountConclusion{deleted: true}, 0, false},
		{"exists", "target_not_visible", "离组账号", true, true, true, &actionAccountConclusion{accountName: "离组账号"}, 0, false},
		{"three_failures", "inventory_unavailable", "", true, true, false, nil, 3, false},
		{"unavailable", "inventory_unavailable", "", true, true, false, nil, 0, true},
		{"pending_before_unavailable", "target_unverified", "", true, true, true, nil, 3, true},
		{"never_complete", "uncertain", "", false, true, true, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepository()
			p := actionCleanupPair("1", now)
			if tc.pending {
				actionCleanupPending(&p, ActionKindPriority, DispatchUncertain, now.Add(-time.Minute))
				actionCleanupPending(&p, ActionKindTarget, DispatchConfirmedApplied, now.Add(-time.Minute))
			}
			actionCleanupStore(repo, p)
			s := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, actionNow: func() time.Time { return now }}
			if tc.complete {
				if tc.missing {
					actionCleanupRemember(s, now.Add(-time.Minute), true)
				} else {
					actionCleanupRemember(s, now.Add(-time.Minute), true, upstream.AdminGroupAccountInfo{ID: "1", Name: "分组账号"})
				}
				// A newer incomplete read must not destroy the last complete read.
				actionCleanupRemember(s, now, false)
			}
			v := &actionSweepView{failures: tc.failures, unavailable: tc.unavailable, departed: map[string]bool{}, conclusions: map[string]actionAccountConclusion{}}
			if tc.conclusion != nil {
				v.departed["1"] = true
				v.conclusions["1"] = *tc.conclusion
			}
			s.actionSweepViews.Store(priorityRuntimeLeaseKey("u", "w"), v)
			before := cloneActionPair(repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:1"}))
			got, err := s.PrioritySyncStatus(context.Background(), "u")
			if err != nil {
				t.Fatal(err)
			}
			if tc.reason == "" {
				if len(got.ActionDiagnostics) != 0 {
					t.Fatalf("must be hidden: %+v", got.ActionDiagnostics)
				}
			} else {
				if len(got.ActionDiagnostics) != 1 {
					t.Fatalf("want one row: %+v", got.ActionDiagnostics)
				}
				d := got.ActionDiagnostics[0]
				if d.Reason != tc.reason || d.AccountID != "1" || d.AccountName != tc.accountName {
					t.Fatalf("diagnostic=%+v", d)
				}
			}
			if !reflect.DeepEqual(before, repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:1"})) {
				t.Fatal("status changed checkpoints")
			}
		})
	}
	// Invalid IDs and targets belonging to another workspace never leak diagnostics.
	repo := newFakeRepository()
	for _, id := range []string{"sub2api:w:", "sub2api:other:1", "newapi:w:1"} {
		p := actionCleanupPair("1", now)
		p.Priority.TargetID = id
		actionCleanupPending(&p, ActionKindPriority, DispatchUncertain, now)
		p.Target = nil
		actionCleanupStore(repo, p)
	}
	s := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, actionNow: func() time.Time { return now }}
	got, err := s.PrioritySyncStatus(context.Background(), "u")
	if err != nil || len(got.ActionDiagnostics) != 0 {
		t.Fatalf("invalid scope diagnostics=%+v err=%v", got, err)
	}
}

func TestActionCheckpointCleanupRED3AtomicDecision(t *testing.T) {
	now := actionCleanupNow()
	scope := RemoteActionScope{"u", "w", "sub2api:w:1"}
	for _, tc := range []struct {
		name  string
		clear bool
		setup func(*RemoteActionCheckpoints)
	}{
		{"idle", true, func(*RemoteActionCheckpoints) {}},
		{"idle_conflict", true, func(p *RemoteActionCheckpoints) { p.Priority.Conflict = true; p.Target.Conflict = true }},
		{"uncertain", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchUncertain, now.Add(-time.Minute))
		}},
		{"confirmed_conflict", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindTarget, DispatchConfirmedApplied, now.Add(-time.Minute))
			p.Target.Conflict = true
		}},
		{"dual_terminal", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchConfirmedApplied, now.Add(-time.Minute))
			actionCleanupPending(p, ActionKindTarget, DispatchConfirmedRejected, now.Add(-time.Minute))
		}},
		{"not_sent", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchNotSent, now.Add(-time.Minute))
		}},
		{"prepared_recent", false, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchPrepared, now.Add(-time.Minute))
		}},
		{"sending_recent_blocks_both", false, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindTarget, DispatchSending, now.Add(-time.Minute))
		}},
		{"prepared_exact_five", false, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindTarget, DispatchPrepared, now.Add(-5*time.Minute))
		}},
		{"sending_overdue", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindTarget, DispatchSending, now.Add(-5*time.Minute-time.Nanosecond))
		}},
		{"prepared_overdue", true, func(p *RemoteActionCheckpoints) {
			actionCleanupPending(p, ActionKindPriority, DispatchPrepared, now.Add(-6*time.Minute))
		}},
		{"priority_updated_equal", false, func(p *RemoteActionCheckpoints) { p.Priority.UpdatedAt = now }},
		{"target_updated_after", false, func(p *RemoteActionCheckpoints) { p.Target.UpdatedAt = now.Add(time.Second) }},
		{"wrong_user", false, func(p *RemoteActionCheckpoints) { p.Priority.UserID = "another" }},
		{"wrong_workspace", false, func(p *RemoteActionCheckpoints) { p.Target.AdminAccountID = "another" }},
		{"foreign_target", false, func(p *RemoteActionCheckpoints) { p.Priority.TargetID = "newapi:w:1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := actionCleanupPair("1", now)
			tc.setup(&p)
			before := cloneActionPair(p)
			got := clearDeletedAccountCheckpoints(&p, scope, now, now)
			if got != tc.clear {
				t.Fatalf("clear=%t want %t pair=%+v", got, tc.clear, p)
			}
			if tc.clear {
				if p.Priority != nil || p.Target != nil {
					t.Fatal("must delete both")
				}
			} else if !reflect.DeepEqual(before, p) {
				t.Fatal("skipped pair changed")
			}
			if tc.name == "wrong_user" || tc.name == "wrong_workspace" || tc.name == "foreign_target" {
				return
			}
			repo := newFakeRepository()
			actionCleanupStore(repo, before)
			got, err := repo.ClearDeletedAccountCheckpoint(context.Background(), scope, now, now)
			if err != nil || got != tc.clear {
				t.Fatalf("repository clear=%t err=%v", got, err)
			}
			after := repo.actionPair(scope)
			if tc.clear {
				if after.Priority != nil || after.Target != nil {
					t.Fatal("repository retained rows")
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatal("repository changed skipped original values")
			}
		})
	}
}

type actionCleanupLister struct {
	fakePlatformGroupReader
	calls    atomic.Int32
	mu       sync.Mutex
	accounts []upstream.AdminGroupAccountInfo
	err      error
	entered  chan struct{}
	release  chan struct{}
	deadline time.Duration
}

func (r *actionCleanupLister) ListSub2APIAdminAccountsContext(ctx context.Context, session upstream.Session) ([]upstream.AdminGroupAccountInfo, error) {
	r.calls.Add(1)
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := ctx.Deadline(); ok {
		r.deadline = time.Until(d)
	}
	return append([]upstream.AdminGroupAccountInfo(nil), r.accounts...), r.err
}

func actionCleanupHarness() (*Service, *fakeRepository, *actionCleanupLister, *time.Time, actionSweepInventory) {
	now := actionCleanupNow()
	repo := newFakeRepository()
	lister := &actionCleanupLister{accounts: []upstream.AdminGroupAccountInfo{{ID: "1", Name: "仍存在"}, {ID: "sentinel", Name: "保留"}}}
	s := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "w"}, platformGroups: lister, priorityActions: &fakeTargetPriorityActioner{}, mySites: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, actionNow: func() time.Time { return now }}
	actionCleanupRemember(s, now.Add(-time.Minute), true)
	return s, repo, lister, &now, actionSweepInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, snapshotStartedAt: now.Add(-time.Minute), visible: map[string]string{}}
}

func actionCleanupView(t *testing.T, s *Service) *actionSweepView {
	t.Helper()
	v, ok := s.actionSweepViews.Load(priorityRuntimeLeaseKey("u", "w"))
	if !ok {
		t.Fatal("no sweep read evidence")
	}
	return v.(*actionSweepView)
}

func TestActionCheckpointCleanupRED4ReadIntervalsAndNewAccounts(t *testing.T) {
	s, repo, r, now, i := actionCleanupHarness()
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 0 {
		t.Fatal("empty workspace read accounts")
	}
	actionCleanupStore(repo, actionCleanupPair("1", *now))
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 {
		t.Fatal("departed account did not trigger read")
	}
	if c := actionCleanupView(t, s).conclusions["1"]; c.deleted || c.accountName != "仍存在" {
		t.Fatalf("existing account conclusion=%+v", c)
	}
	*now = now.Add(29 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 {
		t.Fatal("existing account read too soon")
	}
	*now = now.Add(time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 2 {
		t.Fatal("30 minute read missing")
	}
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	*now = now.Add(4 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 2 {
		t.Fatal("new account read before five minutes")
	}
	*now = now.Add(time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 3 {
		t.Fatal("new account missing five minute read")
	}
	if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority != nil {
		t.Fatal("deleted new account retained")
	}
	if r.deadline <= 0 || r.deadline > 60*time.Second {
		t.Fatalf("unbounded list read deadline=%s", r.deadline)
	}
}

func TestActionCheckpointCleanupRED4FailureKeepsSuccessAndNewSet(t *testing.T) {
	for _, mode := range []string{"error", "empty", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, r, now, i := actionCleanupHarness()
			actionCleanupStore(repo, actionCleanupPair("1", *now))
			s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
			before := actionCleanupView(t, s)
			actionCleanupStore(repo, actionCleanupPair("2", *now))
			*now = now.Add(5 * time.Minute)
			ctx := context.Background()
			var cancel context.CancelFunc
			switch mode {
			case "error":
				r.err = errors.New("list failed")
			case "empty":
				r.accounts = nil
			case "timeout":
				r.release = make(chan struct{})
				ctx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
			}
			s.sweepDeletedAccountCheckpoints(ctx, "u", "w", i)
			after := actionCleanupView(t, s)
			if r.calls.Load() != 2 || after.failures != 1 || after.lastReadAt != *now {
				t.Fatalf("failure evidence=%+v calls=%d", after, r.calls.Load())
			}
			if !reflect.DeepEqual(before.conclusions, after.conclusions) || !reflect.DeepEqual(before.departed, after.departed) || before.snapshotStartedAt != after.snapshotStartedAt {
				t.Fatal("failed read overwrote successful conclusion")
			}
			if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority == nil {
				t.Fatal("failed read deleted checkpoint")
			}
			r.release = nil
			r.err = nil
			r.accounts = []upstream.AdminGroupAccountInfo{{ID: "1", Name: "仍存在"}}
			*now = now.Add(30 * time.Second)
			s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
			if r.calls.Load() != 2 {
				t.Fatal("failure retried next tick")
			}
			*now = now.Add(4*time.Minute + 30*time.Second)
			s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
			if r.calls.Load() != 3 {
				t.Fatal("failed new departure ceased to be new")
			}
			if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority != nil {
				t.Fatal("retry failed to remove deleted account")
			}
		})
	}
}

func TestActionCheckpointCleanupRED4EmptyAndReentryPreserveReadRecord(t *testing.T) {
	s, repo, r, now, i := actionCleanupHarness()
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 {
		t.Fatal("initial read missing")
	}
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	v := actionCleanupView(t, s)
	if len(v.departed) != 0 || len(v.conclusions) != 0 || v.lastReadAt != *now {
		t.Fatalf("empty reset=%+v", v)
	}
	*now = now.Add(time.Minute)
	actionCleanupStore(repo, actionCleanupPair("3", *now))
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 {
		t.Fatal("empty reset erased throttle")
	}
	*now = now.Add(4 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 2 {
		t.Fatal("new departure missing read")
	}
	actionCleanupStore(repo, actionCleanupPair("1", *now))
	*now = now.Add(5 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	i.visible["1"] = "回组"
	actionCleanupRemember(s, now.Add(time.Second), true, upstream.AdminGroupAccountInfo{ID: "1", Name: "回组"})
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	v = actionCleanupView(t, s)
	if v.departed["1"] {
		t.Fatal("reentered account still departed")
	}
	if _, ok := v.conclusions["1"]; ok {
		t.Fatal("reentered account retains conclusion")
	}
	delete(i.visible, "1")
	actionCleanupRemember(s, now.Add(2*time.Second), true)
	*now = now.Add(time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 3 {
		t.Fatal("redeparture read before interval")
	}
	*now = now.Add(4 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 4 {
		t.Fatal("redeparture no longer treated as new")
	}
}

func TestActionCheckpointCleanupRED4DeletedInFlightAndScope(t *testing.T) {
	s, repo, r, now, i := actionCleanupHarness()
	p := actionCleanupPair("2", *now)
	actionCleanupPending(&p, ActionKindTarget, DispatchSending, now.Add(-time.Minute))
	actionCleanupStore(repo, p)
	for _, other := range []RemoteActionScope{{"other", "w", "sub2api:w:2"}, {"u", "other", "sub2api:other:2"}, {"u", "w", "newapi:w:2"}} {
		p := actionCleanupPair("2", *now)
		p.Priority.UserID = other.UserID
		p.Priority.AdminAccountID = other.AdminAccountID
		p.Priority.TargetID = other.TargetID
		p.Target = nil
		actionCleanupStore(repo, p)
	}
	beforeOther := len(repo.priorityStates)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 || !actionCleanupView(t, s).conclusions["2"].deleted {
		t.Fatal("deleted sending evidence absent")
	}
	if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority == nil {
		t.Fatal("sending did not protect both")
	}
	*now = now.Add(4 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 1 {
		t.Fatal("deleted in-flight read too soon")
	}
	*now = now.Add(time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if r.calls.Load() != 2 || len(repo.priorityStates) != beforeOther-1 || len(repo.targetActionStates) != 0 {
		t.Fatalf("cleanup escaped scope or retained expired send: priorities=%+v targets=%+v", repo.priorityStates, repo.targetActionStates)
	}
	// IDs are compared after whitespace normalization on both sides.
	p = actionCleanupPair(" 1 ", *now)
	actionCleanupStore(repo, p)
	r.accounts = []upstream.AdminGroupAccountInfo{{ID: " 1 ", Name: "仍存在"}}
	*now = now.Add(5 * time.Minute)
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w: 1 "}).Priority == nil {
		t.Fatal("whitespace ID falsely deleted")
	}
}

func TestActionDiagnosticsCleanupRED4FailureThresholdRecovery(t *testing.T) {
	s, repo, r, now, i := actionCleanupHarness()
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	r.err = errors.New("read failed")
	for n := 1; n <= 3; n++ {
		s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
		status, err := s.PrioritySyncStatus(context.Background(), "u")
		if err != nil {
			t.Fatal(err)
		}
		if n < 3 && len(status.ActionDiagnostics) != 0 {
			t.Fatal("premature unavailable diagnostic")
		}
		if n == 3 && (len(status.ActionDiagnostics) != 1 || status.ActionDiagnostics[0].Reason != "inventory_unavailable") {
			t.Fatalf("failure threshold diagnostics=%+v", status.ActionDiagnostics)
		}
		*now = now.Add(5 * time.Minute)
	}
	r.err = nil
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
	if actionCleanupView(t, s).failures != 0 {
		t.Fatal("success did not reset failures")
	}
	status, err := s.PrioritySyncStatus(context.Background(), "u")
	if err != nil || len(status.ActionDiagnostics) != 0 {
		t.Fatalf("successful deletion diagnostic persists: %+v %v", status, err)
	}
}

func TestActionDiagnosticsCleanupRED4InvalidFullListUnavailable(t *testing.T) {
	s, repo, r, now, inventory := actionCleanupHarness()
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	r.err = &upstream.RequestError{MessageKey: upstream.ErrorInvalidResponse, Platform: upstream.PlatformSub2API}
	s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", inventory)
	status, err := s.PrioritySyncStatus(context.Background(), "u")
	if err != nil || len(status.ActionDiagnostics) != 1 || status.ActionDiagnostics[0].Reason != "inventory_unavailable" {
		t.Fatalf("incomplete/over-limit inventory must be unavailable: %+v %v", status, err)
	}
	if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority == nil {
		t.Fatal("invalid inventory deleted checkpoint")
	}
}

type actionCleanupUnsupportedRepo struct{ healthRepository }
type actionCleanupErrorRepo struct{ *fakeRepository }

func (r actionCleanupErrorRepo) ClearDeletedAccountCheckpoint(context.Context, RemoteActionScope, time.Time, time.Time) (bool, error) {
	return false, errors.New("transaction rolled back")
}

func TestActionCheckpointCleanupRED4UnsupportedErrorAndReappeared(t *testing.T) {
	for _, mode := range []string{"lister_missing", "repo_missing", "transaction_error", "reappeared"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, r, now, i := actionCleanupHarness()
			actionCleanupStore(repo, actionCleanupPair("2", *now))
			switch mode {
			case "lister_missing":
				s.platformGroups = fakePlatformGroupReader{}
			case "repo_missing":
				s.repo = actionCleanupUnsupportedRepo{repo}
			case "transaction_error":
				s.repo = actionCleanupErrorRepo{repo}
			case "reappeared":
				actionCleanupRemember(s, now.Add(time.Second), true, upstream.AdminGroupAccountInfo{ID: "2", Name: "回组"})
			}
			s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", i)
			if repo.actionPair(RemoteActionScope{"u", "w", "sub2api:w:2"}).Priority == nil {
				t.Fatal("unsupported/error/reentered checkpoint removed")
			}
			if (mode == "lister_missing" || mode == "repo_missing") && r.calls.Load() != 0 {
				t.Fatal("unsupported capability caused list read")
			}
			if mode == "lister_missing" {
				status, err := s.PrioritySyncStatus(context.Background(), "u")
				if err != nil || len(status.ActionDiagnostics) != 1 || status.ActionDiagnostics[0].Reason != "inventory_unavailable" {
					t.Fatalf("missing interface diagnostic=%+v %v", status, err)
				}
			}
		})
	}
}

func actionCleanupWait(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sweep did not enter list read")
	}
}

func TestActionCheckpointCleanupRED4SchedulerNoPoliciesAndSingleFlight(t *testing.T) {
	s, repo, r, now, _ := actionCleanupHarness()
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	r.entered = make(chan struct{}, 4)
	r.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { close(r.release); s.actionDispatchWG.Wait() }()
	s.runSchedulerTick(ctx)
	actionCleanupWait(t, r.entered)
	s.runSchedulerTick(ctx)
	if r.calls.Load() != 1 {
		t.Fatal("same workspace started second sweep")
	}
}

func TestActionCheckpointCleanupRED4CacheCompleteness(t *testing.T) {
	for _, mode := range []string{"absent", "partial", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s, repo, r, now, _ := actionCleanupHarness()
			actionCleanupStore(repo, actionCleanupPair("2", *now))
			cache := adminInventoryCache{}
			ctx := context.Background()
			if mode == "partial" {
				cache["u|w"] = adminInventoryCacheEntry{inventory: &adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, snapshotStartedAt: *now}}
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			s.startDeletedAccountSweeps(ctx, cache)
			if r.calls.Load() != 0 {
				t.Fatal("sweep fetched outside complete tick cache")
			}
		})
	}
}

// Status reads and the scheduler may overlap. Lock only the fake repository's
// read methods here; production already reads through independent transactions.
type actionCleanupConcurrentRepo struct{ *fakeRepository }

func (r actionCleanupConcurrentRepo) ListPrioritySyncStates(ctx context.Context, u, w string) ([]PrioritySyncState, error) {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()
	return r.fakeRepository.ListPrioritySyncStates(ctx, u, w)
}
func (r actionCleanupConcurrentRepo) ListTargetActionStates(ctx context.Context, u, w string) ([]TargetActionState, error) {
	r.actionMu.Lock()
	defer r.actionMu.Unlock()
	return r.fakeRepository.ListTargetActionStates(ctx, u, w)
}

func TestActionCheckpointCleanupRED4ConcurrentInventoryCopy(t *testing.T) {
	s, repo, r, now, _ := actionCleanupHarness()
	s.repo = actionCleanupConcurrentRepo{repo}
	actionCleanupStore(repo, actionCleanupPair("2", *now))
	r.entered = make(chan struct{}, 1)
	r.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { close(r.release); s.actionDispatchWG.Wait() }()
	i := &adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, snapshotStartedAt: *now, groups: []adminInventoryGroup{{accounts: []upstream.AdminGroupAccountInfo{{ID: "1", Name: "原名", Status: "inactive"}}}}}
	s.startDeletedAccountSweeps(ctx, adminInventoryCache{"u|w": {inventory: i}})
	actionCleanupWait(t, r.entered)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			_, err := s.PrioritySyncStatus(ctx, "u")
			if err != nil {
				t.Error(err)
			}
		}
	}()
	for n := 0; n < 100; n++ {
		i.groups[0].accounts[0].Name = "变动"
		i.groups[0].accounts[0].Status = "active"
		actionCleanupRemember(s, now.Add(time.Duration(n+1)*time.Second), true, upstream.AdminGroupAccountInfo{ID: "1", Name: "最新名"})
	}
	wg.Wait()
	if r.calls.Load() != 1 {
		t.Fatal("concurrent status started list reads")
	}
}

func TestActionCheckpointCleanupRED4bPriorityFailureAndWorkspaceFreeze(t *testing.T) {
	for _, deleted := range []bool{true, false} {
		t.Run(map[bool]string{true: "deleted", false: "still_exists"}[deleted], func(t *testing.T) {
			s, repo, r, now, _ := actionCleanupHarness()
			p := actionCleanupPair("2", *now)
			p.Target = nil
			actionCleanupPending(&p, ActionKindPriority, DispatchUncertain, now.Add(-35*time.Minute))
			actionCleanupStore(repo, p)
			repo.priorityWorkspaces["u|w"] = PriorityWorkspaceSyncState{UserID: "u", AdminAccountID: "w"}
			policies := []Policy{{UserID: "u", AdminAccountID: "w"}}
			if !deleted {
				r.accounts = append(r.accounts, upstream.AdminGroupAccountInfo{ID: "2", Name: "仍在主站"})
			}
			cache := adminInventoryCache{}
			states, _ := repo.ListAllPrioritySyncStates(context.Background())
			s.syncMultiplierPrioritiesWithCache(context.Background(), policies, nil, nil, nil, states, cache)
			before, _ := repo.GetPriorityWorkspaceSyncState(context.Background(), "u", "w")
			if before == nil || before.PendingTargetCount < 1 {
				t.Fatal("fixture did not cause old Priority failure")
			}
			guard := newWorkspaceFloorGuard()
			inventory := adminWorkspaceInventory{session: upstream.Session{Platform: upstream.PlatformSub2API}, groupsComplete: true, snapshotStartedAt: *now}
			if err := s.overlayPersistentPending(context.Background(), "u", "w", guard, inventory); err != nil || !guard.freezeWorkspace {
				t.Fatalf("fixture did not freeze workspace: %v", err)
			}
			s.sweepDeletedAccountCheckpoints(context.Background(), "u", "w", actionSweepInventory{session: inventory.session, snapshotStartedAt: *now, visible: map[string]string{}})
			states, _ = repo.ListAllPrioritySyncStates(context.Background())
			s.syncMultiplierPrioritiesWithCache(context.Background(), policies, nil, nil, nil, states, make(adminInventoryCache))
			after, _ := repo.GetPriorityWorkspaceSyncState(context.Background(), "u", "w")
			guard = newWorkspaceFloorGuard()
			if err := s.overlayPersistentPending(context.Background(), "u", "w", guard, inventory); err != nil {
				t.Fatal(err)
			}
			if deleted {
				if guard.freezeWorkspace || len(states) != 0 {
					t.Fatal("deleted pending still freezes workspace")
				}
				if after == nil || after.PendingTargetCount != 0 || after.LastDecision != "success" {
					t.Fatal("deleted target still counted as Priority failure")
				}
			} else {
				if !guard.freezeWorkspace || len(states) != 1 || after == nil || after.PendingTargetCount < 1 {
					t.Fatal("still-existing departed account lost protection/failure")
				}
			}
		})
	}
}

func TestActionDiagnosticsCleanupRED5InvisibleLogRateLimit(t *testing.T) {
	s, repo, r, now, _ := actionCleanupHarness()
	p := actionCleanupPair("2", *now)
	p.Priority = nil
	actionCleanupStore(repo, p)
	var output bytes.Buffer
	oldWriter, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	defer func() { log.SetOutput(oldWriter); log.SetFlags(oldFlags) }()
	restore := func() {
		states, _ := repo.ListAllTargetActionStates(context.Background())
		s.restoreUnmanagedTargetActions(context.Background(), nil, nil, nil, nil, states, make(adminInventoryCache))
	}
	restore()
	*now = now.Add(30 * time.Second)
	restore()
	if n := strings.Count(output.String(), RemoteActionTargetNotVisible); n != 1 {
		t.Fatalf("one hour logged %d times", n)
	}
	*now = now.Add(time.Hour)
	restore()
	if n := strings.Count(output.String(), RemoteActionTargetNotVisible); n != 2 {
		t.Fatalf("next hour logs=%d", n)
	}
	r.groups = []upstream.AdminGroupInfo{{ID: "g"}}
	r.accountsByGrp = map[string][]upstream.AdminGroupAccountInfo{"g": {{ID: "2", Status: "active"}}}
	restore()
	actionCleanupStore(repo, p)
	r.groups = nil
	r.accountsByGrp = nil
	restore()
	if n := strings.Count(output.String(), RemoteActionTargetNotVisible); n != 3 {
		t.Fatalf("reentered account did not reset rate limit: %d", n)
	}
}

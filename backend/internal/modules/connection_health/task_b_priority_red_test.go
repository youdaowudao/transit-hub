package connection_health

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// These business expectations are owned by the primary agent and come from
// task B SPEC 3.1–3.4 and PLAN 3–4. They must not be weakened by implementation.
type taskBPriorityCase struct {
	id         string
	tier       int
	state      State
	multiplier *float64
	current    int
	status     string
	noEvidence bool
	latency    int
}

func taskBPriorityFixture(version string, cases []taskBPriorityCase) (*Service, *fakeRepository, *fakeTargetPriorityActioner, map[string]*priorityTargetInventory, []ConnectionHealthState) {
	repo := newFakeRepository()
	repo.accountTiers = make(map[string]int)
	actions := &fakeTargetPriorityActioner{}
	s := &Service{repo: repo, priorityActions: actions}
	p := taskAV2Policy()
	p.ID, p.UserID, p.AdminAccountID, p.RuleVersion = "p", "user1", "ws1", version
	p.Enabled, p.PriorityMode = true, PriorityModeMultiplier
	p.ModelTargets = []ModelTarget{{ModelName: "m", Enabled: true}}
	repo.policies = []Policy{p}
	items := make(map[string]*priorityTargetInventory)
	states := []ConnectionHealthState{}
	for _, c := range cases {
		id := "sub2api:ws1:" + c.id
		current := c.current
		if current == 0 {
			current = 50
		}
		status := c.status
		if status == "" {
			status = MultiplierResolutionResolved
		}
		items[id] = &priorityTargetInventory{
			target:   AdminProbeTarget{TargetID: id, AccountID: c.id, Platform: "sub2api", AccountStatus: "active", InventoryComplete: true, Models: []string{"m"}},
			account:  upstream.AdminGroupAccountInfo{ID: c.id, Priority: intPointer(current)},
			policies: []Policy{p}, currentPriority: current, priorityPresent: true, snapshotStartedAt: time.Now(),
			upstreamMultiplier: upstreamMultiplierResolution{status: status, info: upstreamKeyGroupInfo{effectiveMultiplier: c.multiplier}},
		}
		if c.tier == 1 {
			repo.accountTiers["user1|ws1|"+id] = 1
		}
		if !c.noEvidence {
			st := ConnectionHealthState{ConnectionID: id, ModelName: "m", UserID: "user1", AdminAccountID: "ws1", State: c.state, RuleVersion: version, CurrentWeight: 100, LastSuccessLatencyMs: intPointer(c.latency), HealthEvidenceStatus: HealthEvidenceLegacy}
			repo.states[id] = map[string]ConnectionHealthState{"m": st}
			states = append(states, st)
		}
	}
	return s, repo, actions, items, states
}

func taskBWrittenPriorities(actions *fakeTargetPriorityActioner) map[string]int {
	got := map[string]int{}
	for _, call := range actions.calls {
		got[call.targetID] = call.priority
	}
	return got
}

func TestPriorityTaskBTwoPrimaryTwoBackupStopAndRecoverIntegration(t *testing.T) {
	cases := []taskBPriorityCase{{id: "main-a", tier: 1, state: StateHealthy, multiplier: float64Ptr(.2)}, {id: "main-b", tier: 1, state: StateHealthy, multiplier: float64Ptr(.3)}, {id: "backup-a", state: StateHealthy, multiplier: float64Ptr(.01)}, {id: "backup-b", state: StateHealthy, multiplier: float64Ptr(.02)}}
	s, repo, actions, items, states := taskBPriorityFixture(RuleVersionV2, cases)
	for _, stage := range []struct {
		name  string
		state State
		want  map[string]int
	}{
		{"initial", StateHealthy, map[string]int{"main-a": 10, "main-b": 11, "backup-a": 12, "backup-b": 13}},
		{"primary-stopped", StateSuspended, map[string]int{"main-a": 100000, "main-b": 100000, "backup-a": 10, "backup-b": 11}},
		{"primary-recovered", StateHealthy, map[string]int{"main-a": 10, "main-b": 11, "backup-a": 12, "backup-b": 13}},
	} {
		for i := range states {
			if strings.HasSuffix(states[i].ConnectionID, "main-a") || strings.HasSuffix(states[i].ConnectionID, "main-b") {
				states[i].State = stage.state
				repo.states[states[i].ConnectionID]["m"] = states[i]
			}
		}
		actions.calls = nil
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
		for _, call := range actions.calls {
			id := "sub2api:ws1:" + call.targetID
			items[id].currentPriority = call.priority
			items[id].account.Priority = intPointer(call.priority)
		}
		got := map[string]int{}
		for id, item := range items {
			item.snapshotStartedAt = time.Now()
			got[strings.TrimPrefix(id, "sub2api:ws1:")] = item.currentPriority
		}
		if !reflect.DeepEqual(got, stage.want) {
			t.Fatalf("%s actual=%v want=%v", stage.name, got, stage.want)
		}
		t.Logf("%s confirmed platform values: %v", stage.name, got)
		actions.calls = nil
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
		if len(actions.calls) != 0 {
			t.Fatalf("%s repeated confirmed write: %+v", stage.name, actions.calls)
		}
	}
}

func TestPriorityTaskBSevenSegmentsAndInputOrder(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		cases := []taskBPriorityCase{
			{id: "main-normal", tier: 1, state: StateHealthy, multiplier: float64Ptr(.3)},
			{id: "main-suspect", tier: 1, state: StateSuspect, multiplier: float64Ptr(.3)},
			{id: "main-degraded", tier: 1, state: StateDegraded, multiplier: float64Ptr(.5)},
			{id: "backup-normal", state: StateHealthy, multiplier: float64Ptr(.01)},
			{id: "backup-degraded", state: StateDegraded, multiplier: float64Ptr(.01)},
			{id: "main-stopped", tier: 1, state: StateSuspended, multiplier: float64Ptr(.01)},
		}
		want := map[string]int{"main-normal": 10, "main-suspect": 10, "main-degraded": 11, "backup-normal": 12, "backup-degraded": 1000, "main-stopped": 100000}
		if version == RuleVersionLegacy {
			cases = append(cases, taskBPriorityCase{id: "main-recovering", tier: 1, state: StateRecovering, multiplier: float64Ptr(.2)}, taskBPriorityCase{id: "backup-recovering", state: StateRecovering, multiplier: float64Ptr(.01)})
			want["main-suspect"], want["main-recovering"], want["main-degraded"], want["backup-normal"], want["backup-recovering"] = 11, 12, 13, 14, 100
		}
		for rotation := 0; rotation < len(cases); rotation++ {
			ordered := append(append([]taskBPriorityCase{}, cases[rotation:]...), cases[:rotation]...)
			s, _, a, items, states := taskBPriorityFixture(version, ordered)
			s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
			if got := taskBWrittenPriorities(a); !reflect.DeepEqual(got, want) {
				t.Errorf("%s rotation %d: got %v want %v", version, rotation, got, want)
			}
		}
	}
}

func TestPriorityTaskBUnknownMultiplierStaysInMainSegment(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		cases := []taskBPriorityCase{{id: "known", tier: 1, state: StateHealthy, multiplier: float64Ptr(.2)}, {id: "unknown-a", tier: 1, state: StateHealthy, latency: 100}, {id: "unknown-b", tier: 1, state: StateHealthy, latency: 200}, {id: "backup", state: StateHealthy, multiplier: float64Ptr(.01)}}
		s, _, a, items, states := taskBPriorityFixture(version, cases)
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
		want := map[string]int{"known": 10, "unknown-a": 11, "unknown-b": 11, "backup": 12}
		if version == RuleVersionLegacy {
			want["unknown-b"], want["backup"] = 12, 13
		}
		if got := taskBWrittenPriorities(a); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s got %v want %v; unknown main must never fall behind backup", version, got, want)
		}
	}
}

func TestPriorityTaskBOverflowNeverTiesMainWithBackup(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		t.Run(version, func(t *testing.T) {
			cases := []taskBPriorityCase{}
			for i := 0; i < 100; i++ {
				cases = append(cases, taskBPriorityCase{id: fmt.Sprintf("main-%03d", i), tier: 1, state: StateHealthy, multiplier: float64Ptr(float64(i+1) / 1000)})
			}
			cases = append(cases, taskBPriorityCase{id: "backup-a", state: StateHealthy, multiplier: float64Ptr(.0001)}, taskBPriorityCase{id: "backup-b", state: StateHealthy, multiplier: float64Ptr(.0002)})
			s, _, a, items, states := taskBPriorityFixture(version, cases)
			s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
			got := taskBWrittenPriorities(a)
			if got["main-000"] != 10 || got["main-088"] != 98 || got["main-099"] != 98 || got["backup-a"] != 99 || got["backup-b"] != 99 {
				t.Fatalf("overflow broke boundary: %v", got)
			}
		})
	}
}

// Baseline golden outputs are independently fixed to V2.9.3; these assertions
// should pass on both baseline and candidate, including unknown evidence/gates.
func TestPriorityTaskBNoMainGoldenPreservesBothRules(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		cases := []taskBPriorityCase{{id: "a", state: StateHealthy, multiplier: float64Ptr(.13), latency: 10}, {id: "b", state: StateHealthy, multiplier: float64Ptr(.13), latency: 20}, {id: "c", state: StateHealthy, multiplier: float64Ptr(.14)}, {id: "degraded", state: StateDegraded, multiplier: float64Ptr(.1)}, {id: "stopped", state: StateSuspended, multiplier: float64Ptr(.1)}, {id: "unknown-multiplier", state: StateHealthy}, {id: "no-health", state: StateHealthy, multiplier: float64Ptr(.1), noEvidence: true}, {id: "manual", state: StateHealthy, multiplier: float64Ptr(.1), current: 5}, {id: "blocked", state: StateHealthy, multiplier: float64Ptr(.01), status: MultiplierResolutionStale}}
		want := map[string]int{"a": 10, "b": 10, "c": 11, "degraded": 1000, "stopped": 100000, "unknown-multiplier": 99}
		if version == RuleVersionLegacy {
			want["b"], want["c"] = 11, 12
		}
		s, _, a, items, states := taskBPriorityFixture(version, cases)
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
		if got := taskBWrittenPriorities(a); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s baseline golden got %v want %v", version, got, want)
		}
	}
}

func TestPriorityTaskBIdleOwnershipAndConfirmedWriteBaseline(t *testing.T) {
	for _, c := range []struct {
		name                       string
		current                    int
		noEvidence, exit, conflict bool
		wantWrite                  bool
		wantRecord                 bool
	}{
		{"external-machine", 60, false, false, false, true, true},
		{"external-machine-conflict", 60, false, false, true, true, true},
		{"unknown-evidence", 60, true, false, false, false, true},
		{"exit-external-machine", 60, true, true, false, false, false},
		{"manual", 5, false, false, false, false, false},
		{"manual-exit", 5, true, true, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r, a, items, states := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", state: StateHealthy, multiplier: float64Ptr(.13), current: c.current, noEvidence: c.noEvidence}})
			id := "sub2api:ws1:a"
			key := "user1|ws1|" + id
			stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 50, LastAppliedPriority: 10, Conflict: c.conflict}
			r.priorityStates[key] = stored
			if c.exit {
				items[id].policies = nil
			}
			s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, []PrioritySyncState{stored})
			if (len(a.calls) > 0) != c.wantWrite {
				t.Errorf("writes %v wantWrite %v", a.calls, c.wantWrite)
			}
			got, exists := r.priorityStates[key]
			if exists != c.wantRecord {
				t.Fatalf("record exists %v want %v: %+v", exists, c.wantRecord, got)
			}
			if exists && got.OriginalPriority != 50 {
				t.Error("original restore value changed")
			}
			if exists && got.LastAppliedPriority != 10 {
				t.Error("observed external value was promoted to confirmed write")
			}
			if exists && !c.conflict && got.Conflict {
				t.Error("idle external value created conflict")
			}
		})
	}
}

func TestPriorityTaskBLegacyExitAlreadyOriginalEndsWithoutWrite(t *testing.T) {
	for _, c := range []struct {
		name         string
		current      int
		conflict     bool
		wantRecord   bool
		wantConflict bool
	}{
		{"already-original", 50, false, false, false},
		{"existing-conflict-kept", 50, true, true, true},
		{"external-other-value-kept", 60, false, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r, actions, items, states := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", state: StateHealthy, multiplier: float64Ptr(.13), current: c.current}})
			id := "sub2api:ws1:a"
			key := "user1|ws1|" + id
			stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 50, LastAppliedPriority: 1, Conflict: c.conflict}
			r.priorityStates[key] = stored
			items[id].policies = nil
			s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, []PrioritySyncState{stored})
			if len(actions.calls) != 0 {
				t.Fatalf("legacy already restored/external value must not send: %+v", actions.calls)
			}
			got, exists := r.priorityStates[key]
			if exists != c.wantRecord {
				t.Fatalf("record exists=%v want=%v: %+v", exists, c.wantRecord, got)
			}
			if exists && (got.Conflict != c.wantConflict || got.OriginalPriority != 50 || got.LastAppliedPriority != 1) {
				t.Fatalf("retained legacy baseline changed: %+v", got)
			}
		})
	}
}

func TestActionCheckpointTaskBFirstNoEffectDeletesOnlyNewZeroRecord(t *testing.T) {
	for _, phase := range []RemoteDispatchPhase{DispatchNotSent, DispatchConfirmedRejected, DispatchPrepared} {
		for _, original := range []int{0, 50} {
			t.Run(fmt.Sprintf("%s-original-%d", phase, original), func(t *testing.T) {
				r := newFakeRepository()
				claim := actionClaimFixture(t, r, ActionKindPriority, "new")
				claim.Priority.OriginalPriority, claim.Priority.LastAppliedPriority = original, 0
				if ok, err := r.ClaimRemoteAction(t.Context(), claim); !ok || err != nil {
					t.Fatal(ok, err)
				}
				if phase == DispatchPrepared {
					r.expireActionLeaseForTest(claim.OwnerID, false)
				} else if err := r.RecordRemoteActionReceipt(t.Context(), claim, phase); err != nil {
					t.Fatal(err)
				}
				pair, err := r.ReconcileRemoteAction(t.Context(), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope})
				if err != nil || pair.pendingCount() != 0 {
					t.Fatal(pair, err)
				}
				if original == 50 && pair.Priority != nil {
					t.Fatal("first unconfirmed write survived no-effect reconciliation", pair.Priority)
				}
				if original == 0 && pair.Priority == nil {
					t.Fatal("legacy original=0 semantics changed")
				}
			})
		}
	}
}

func TestActionCheckpointTaskBReleaseIDEndsPriorityAndPreservesTarget(t *testing.T) {
	for _, id := range []string{"priority-release:manual", "priority-release:restore", "old-unmarked-id"} {
		r := newFakeRepository()
		claim := actionClaimFixture(t, r, ActionKindPriority, id)
		claim.Priority.OriginalPriority, claim.Priority.LastAppliedPriority = 50, 1
		claim.Priority.PendingPriority = intPointer(1)
		target := TargetActionState{UserID: "u", AdminAccountID: "w", TargetID: claim.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive"}
		r.targetActionStates["u|w|"+claim.TargetID] = target
		if ok, err := r.ClaimRemoteAction(t.Context(), claim); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if ok, err := r.PermitRemoteAction(t.Context(), claim); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if err := r.RecordRemoteActionReceipt(t.Context(), claim, DispatchConfirmedApplied); err != nil {
			t.Fatal(err)
		}
		pair, err := r.ReconcileRemoteAction(t.Context(), RemoteActionObservation{RemoteActionScope: claim.RemoteActionScope, InventoryComplete: true, Visible: true, SnapshotStartedAt: time.Now(), Priority: intPointer(1)})
		if err != nil || pair.pendingCount() != 0 {
			t.Fatal(pair, err)
		}
		if id != "old-unmarked-id" && pair.Priority != nil {
			t.Errorf("%s left ambiguous idle 50/1 record: %+v", id, pair.Priority)
		}
		if id == "old-unmarked-id" && (pair.Priority == nil || pair.Priority.LastAppliedPriority != 1) {
			t.Error("old unmarked semantics changed")
		}
		if pair.Target == nil || !reflect.DeepEqual(*pair.Target, target) {
			t.Error("priority release changed target checkpoint")
		}
		if err := r.RecordRemoteActionReceipt(context.Background(), claim, DispatchConfirmedApplied); err == nil {
			t.Error("late receipt reclaimed closed action")
		}
	}
}

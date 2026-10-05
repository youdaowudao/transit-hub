package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestPriorityFailureReasonClassification(t *testing.T) {
	request := &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 502}
	for _, tc := range []struct {
		name    string
		err     error
		visible bool
		want    string
	}{
		{"write_visible", request, true, "write_failed"}, {"write_absent", request, false, "write_failed"},
		{"joined_wait_write", errors.Join(request, ErrRemoteActionPending), true, "write_failed"},
		{"joined_absent_write", errors.Join(ErrRemoteActionLeaseLost, request), false, "write_failed"},
		{"absent_evidence", ErrRemoteActionEvidenceChanged, false, "not_visible"},
		{"absent_local", errors.New("record unavailable"), false, "not_visible"},
		{"visible_evidence", ErrRemoteActionEvidenceChanged, true, "waiting"},
		{"visible_pending", ErrRemoteActionPending, true, "waiting"},
		{"visible_lease", ErrRemoteActionLeaseLost, true, "waiting"},
		{"wrapped_wait", errors.Join(errors.New("context"), ErrRemoteActionEvidenceChanged), true, "waiting"},
		{"visible_local", errors.New("record unavailable"), true, "other"},
		{"visible_timeout", context.DeadlineExceeded, true, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPriorityTargetFailure(tc.err, tc.visible); got != tc.want {
				t.Fatalf("classification=%q want=%q visible=%v", got, tc.want, tc.visible)
			}
		})
	}
}

type priorityFailureREDRepository struct {
	*fakeRepository
	waiting  map[string]error
	obsolete bool
}

func (r *priorityFailureREDRepository) ReconcileRemoteAction(ctx context.Context, o RemoteActionObservation) (RemoteActionCheckpoints, error) {
	if err := r.waiting[o.TargetID]; err != nil {
		return RemoteActionCheckpoints{}, err
	}
	return r.fakeRepository.ReconcileRemoteAction(ctx, o)
}
func (r *priorityFailureREDRepository) IsPriorityWorkspaceGenerationCurrent(ctx context.Context, u, w, g string) (bool, error) {
	if r.obsolete {
		return false, nil
	}
	return r.fakeRepository.IsPriorityWorkspaceGenerationCurrent(ctx, u, w, g)
}
func priorityFailureREDSetup(t *testing.T) (*Service, *priorityFailureREDRepository, *fakeTargetPriorityActioner, map[string]*priorityTargetInventory) {
	t.Helper()
	r := &priorityFailureREDRepository{fakeRepository: newFakeRepository(), waiting: map[string]error{}}
	r.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success", InventoryStatus: "complete"}
	a := &fakeTargetPriorityActioner{}
	s := &Service{repo: r, priorityActions: a, accounts: fakeAdminAccountResolver{id: "ws1"}}
	p := probePolicy()
	p.PriorityMode = PriorityModeMultiplier
	p.StrategyMode = StrategyModeMultiplierOnly
	inv := map[string]*priorityTargetInventory{}
	for _, id := range []string{"100", "101"} {
		target := "sub2api:ws1:" + id
		inv[target] = &priorityTargetInventory{target: AdminProbeTarget{TargetID: target, AccountID: id, Platform: string(upstream.PlatformSub2API)}, policies: []Policy{p}, currentPriority: 50, priorityPresent: true, multipliers: []float64{0.1}}
	}
	return s, r, a, inv
}
func priorityFailureREDRound(t *testing.T, s *Service, r *priorityFailureREDRepository, inv map[string]*priorityTargetInventory, g string) *PriorityWorkspaceSyncState {
	t.Helper()
	states, err := r.ListPrioritySyncStates(context.Background(), "user1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	s.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inv, true, nil, states, g)
	state, err := r.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || state == nil {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	return state
}
func TestPrioritySyncWaitingTargetDoesNotFailRoundAndRetries(t *testing.T) {
	for _, pending := range []string{"", "saved-generation"} {
		t.Run("generation="+pending, func(t *testing.T) {
			s, r, a, inv := priorityFailureREDSetup(t)
			state := r.priorityWorkspaces["user1|ws1"]
			state.PendingSignature = pending
			r.priorityWorkspaces["user1|ws1"] = state
			r.waiting["sub2api:ws1:100"] = ErrRemoteActionEvidenceChanged
			got := priorityFailureREDRound(t, s, r, inv, pending)
			if got.LastDecision != "success" || got.PendingTargetCount != 0 || got.NextReconcileAt != nil || got.PendingSignature != "" {
				t.Fatalf("waiting must finish success without failure/backoff and clear pending: %+v", got)
			}
			if len(a.calls) != 1 || a.calls[0].targetID != "101" {
				t.Fatalf("healthy sibling must still write: %+v", a.calls)
			}
			inv["sub2api:ws1:101"].currentPriority = a.calls[0].priority
			delete(r.waiting, "sub2api:ws1:100")
			got = priorityFailureREDRound(t, s, r, inv, "")
			if got.LastDecision != "success" || len(a.calls) != 2 || a.calls[1].targetID != "100" {
				t.Fatalf("next ordinary round must retry deferred target: state=%+v writes=%+v", got, a.calls)
			}
		})
	}
}
func TestPriorityWaitingTimeoutAfterFiveMinutes(t *testing.T) {
	for _, scenario := range []string{"continuous", "reset", "aborted_round"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, actions, inv := priorityFailureREDSetup(t)
			start := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
			now := start
			s.actionNow = func() time.Time { return now }
			r.waiting["sub2api:ws1:100"] = ErrRemoteActionPending
			// Stored target reconciliation supplies the waiting outcome; it remains managed.
			r.priorityStates["user1|ws1|sub2api:ws1:100"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:100", OriginalPriority: 50, LastAppliedPriority: 50}
			run := func(offset time.Duration, want string) {
				now = start.Add(offset)
				got := priorityFailureREDRound(t, s, r, inv, "")
				for _, call := range actions.calls {
					inv["sub2api:ws1:"+call.targetID].currentPriority = call.priority
				}
				if got.LastDecision != want {
					t.Errorf("at %s decision=%s want=%s key=%s", offset, got.LastDecision, want, got.LastError)
				}
				if want == "failed" && got.LastError != "admin.connectionHealth.errors.priorityWaitingTimeout" {
					t.Errorf("timeout key=%q", got.LastError)
				}
			}
			run(0, "success")
			switch scenario {
			case "continuous":
				run(5*time.Minute-time.Nanosecond, "success")
				run(5*time.Minute, "failed")
			case "reset":
				delete(r.waiting, "sub2api:ws1:100")
				run(4*time.Minute, "success")
				r.waiting["sub2api:ws1:100"] = ErrRemoteActionPending
				run(4*time.Minute+time.Second, "success")
				run(5*time.Minute, "success")
				run(9*time.Minute+time.Second, "failed")
			case "aborted_round":
				delete(r.waiting, "sub2api:ws1:100")
				now = start.Add(4 * time.Minute)
				st := r.priorityWorkspaces["user1|ws1"]
				st.PendingSignature = "obsolete"
				r.priorityWorkspaces["user1|ws1"] = st
				r.obsolete = true
				priorityFailureREDRound(t, s, r, inv, "obsolete")
				st = r.priorityWorkspaces["user1|ws1"]
				st.PendingSignature = ""
				r.priorityWorkspaces["user1|ws1"] = st
				r.obsolete = false
				r.waiting["sub2api:ws1:100"] = ErrRemoteActionPending
				run(5*time.Minute, "failed")
			}
		})
	}
}
func TestPrioritySyncNotVisibleRestoreRecordsSpecificReason(t *testing.T) {
	s, r, a, _ := priorityFailureREDSetup(t)
	missing := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:242", OriginalPriority: 7, LastAppliedPriority: 50}
	r.priorityStates["user1|ws1|"+missing.TargetID] = missing
	got := priorityFailureREDRound(t, s, r, map[string]*priorityTargetInventory{}, "")
	if got.LastDecision != "failed" || got.LastError != "admin.connectionHealth.errors.priorityTargetNotVisible" || got.PendingTargetCount != 1 {
		t.Fatalf("absent restore must give specific failure: %+v", got)
	}
	view, err := s.PrioritySyncStatus(context.Background(), "user1")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(view)
	var decoded struct {
		FailedTargets []struct {
			AccountID string `json:"accountId"`
			Reason    string `json:"reason"`
		} `json:"failedTargets"`
	}
	_ = json.Unmarshal(payload, &decoded)
	if len(decoded.FailedTargets) != 1 || decoded.FailedTargets[0].AccountID != "242" || decoded.FailedTargets[0].Reason != "not_visible" {
		t.Fatalf("absent restore must expose account/reason: %s", payload)
	}
	if len(a.calls) != 0 {
		t.Fatalf("missing account must not be written: %+v", a.calls)
	}
}

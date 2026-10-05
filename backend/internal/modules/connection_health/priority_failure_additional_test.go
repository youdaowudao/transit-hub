package connection_health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestPrioritySyncWriteFailureRecordsWriteFailed(t *testing.T) {
	repo := newFakeRepository()
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
	actions := &failingTargetPriorityActioner{err: &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 503}}
	service := &Service{repo: repo, priorityActions: actions}
	policy := Policy{ID: "p1", UserID: "user1", AdminAccountID: "ws1", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly}
	targetID := "sub2api:ws1:100"
	inventory := map[string]*priorityTargetInventory{targetID: {
		target: AdminProbeTarget{TargetID: targetID, AccountID: "100"}, policies: []Policy{policy},
		multipliers: []float64{0.1}, currentPriority: 50, priorityPresent: true,
	}}
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, nil, nil)
	state, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || state == nil || state.LastDecision != "failed" || state.LastError != "admin.connectionHealth.errors.priorityWriteFailed" {
		t.Fatalf("main-site write failure must record write_failed, state=%+v err=%v", state, err)
	}
}

type prioritySequenceFailureRepository struct {
	*fakeRepository
	calls  map[string]int
	failAt int
	err    error
}

func (r *prioritySequenceFailureRepository) ReconcileRemoteAction(ctx context.Context, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	r.calls[observation.TargetID]++
	if r.calls[observation.TargetID] == r.failAt {
		return RemoteActionCheckpoints{}, r.err
	}
	return r.fakeRepository.ReconcileRemoteAction(ctx, observation)
}

func TestPrioritySyncFailureClassificationAtEveryTargetEntry(t *testing.T) {
	for _, entry := range []string{"reconcile", "managed", "restore"} {
		for _, category := range []struct {
			name string
			err  error
			key  string
		}{
			{"write", errors.Join(&upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 503}, ErrRemoteActionPending), ErrorPriorityWriteFailed},
			{"other", errors.New("本地记录读取失败"), ErrorPriorityTargetFailed},
			{"waiting", ErrRemoteActionEvidenceChanged, ""},
		} {
			t.Run(entry+"/"+category.name, func(t *testing.T) {
				base := newFakeRepository()
				base.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
				repo := &prioritySequenceFailureRepository{fakeRepository: base, calls: map[string]int{}, failAt: 1, err: category.err}
				targetID := "sub2api:ws1:100"
				stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalPriority: 7, LastAppliedPriority: 50}
				item := &priorityTargetInventory{target: AdminProbeTarget{TargetID: targetID, AccountID: "100"}, currentPriority: 50, priorityPresent: true}
				var states []PrioritySyncState
				if entry == "managed" {
					item.policies = []Policy{{ID: "p1", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly}}
					item.multipliers = []float64{0.1}
				} else {
					base.priorityStates["user1|ws1|"+targetID] = stored
					states = []PrioritySyncState{stored}
					if entry == "restore" {
						repo.failAt = 2
					}
				}
				service := &Service{repo: repo, priorityActions: &fakeTargetPriorityActioner{}}
				service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", map[string]*priorityTargetInventory{targetID: item}, true, nil, states)
				state, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
				if err != nil || state == nil || state.LastError != category.key {
					t.Fatalf("entry=%s failure=%s state=%+v err=%v", entry, category.name, state, err)
				}
				if category.name == "waiting" && (state.LastDecision != "success" || state.PendingTargetCount != 0 || state.NextReconcileAt != nil) {
					t.Fatalf("visible EvidenceChanged must defer without failure: %+v", state)
				}
			})
		}
	}
}

func TestPrioritySyncJoinedWriteErrorKeepsWriteReason(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%v", restore), func(t *testing.T) {
			repo := newFakeRepository()
			repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
			targetID := "sub2api:ws1:100"
			item := &priorityTargetInventory{target: AdminProbeTarget{TargetID: targetID, AccountID: "100"}, currentPriority: 50, priorityPresent: true}
			var states []PrioritySyncState
			if restore {
				stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalPriority: 7, LastAppliedPriority: 50}
				repo.priorityStates["user1|ws1|"+targetID] = stored
				states = []PrioritySyncState{stored}
			} else {
				item.policies = []Policy{{ID: "p1", Enabled: true, PriorityMode: PriorityModeMultiplier, StrategyMode: StrategyModeMultiplierOnly}}
				item.multipliers = []float64{0.1}
			}
			joined := errors.Join(&upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 502}, ErrRemoteActionPending)
			actions := &failingTargetPriorityActioner{err: joined}
			service := &Service{repo: repo, priorityActions: actions}
			service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", map[string]*priorityTargetInventory{targetID: item}, true, nil, states)
			state, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
			if err != nil || state == nil || state.LastError != ErrorPriorityWriteFailed || state.PendingTargetCount != 1 || len(actions.calls) != 1 {
				t.Fatalf("joined actual write must stay write_failed: state=%+v actions=%+v err=%v", state, actions.calls, err)
			}
		})
	}
}

func TestPrioritySyncMarksKeepTargetsWhenGenerationNotMarked(t *testing.T) {
	for _, kind := range []string{"failed", "health_failed", "health_failed_direct", "partial", "health_partial", "success", "health_success"} {
		t.Run(kind, func(t *testing.T) {
			repo := newFakeRepository()
			repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", PendingSignature: "current", LastDecision: "failed", LastError: ErrorPriorityTargetNotVisible, PendingTargetCount: 1}
			service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}}
			service.priorityFailureTargets.Store(priorityRuntimeLeaseKey("user1", "ws1"), []priorityTargetFailure{{accountID: "100", reason: "not_visible"}})
			switch kind {
			case "failed":
				service.markPriorityWorkspaceSyncFailed("user1", "ws1", "stale", requestError(ErrorPriorityWriteFailed), 1, nil)
			case "health_failed":
				service.markPriorityWorkspaceHealthSyncFailed("user1", "ws1", requestError(ErrorUnknown), 1, nil)
			case "health_failed_direct":
				service.markPriorityWorkspaceHealthSyncFailedDirect("user1", "ws1", requestError(ErrorUnknown), 1, nil)
			case "partial":
				service.markPriorityWorkspaceSyncPartial("user1", "ws1", "stale", 1, nil)
			case "health_partial":
				service.markPriorityWorkspaceHealthSyncPartial("user1", "ws1", 1, nil)
			case "success":
				service.markPriorityWorkspaceSyncSucceeded("user1", "ws1", "stale", nil)
			case "health_success":
				service.markPriorityWorkspaceHealthSyncSucceeded("user1", "ws1", nil)
			}
			status, err := service.PrioritySyncStatus(context.Background(), "user1")
			if err != nil || status.ErrorKey != ErrorPriorityTargetNotVisible || len(status.FailedTargets) != 1 || status.FailedTargets[0].AccountID != "100" {
				t.Fatalf("unmarked transition must keep the previous row and target snapshot: %+v err=%v", status, err)
			}
		})
	}
}

type blockingPriorityStatusSnapshotRepository struct {
	*fakeRepository
	marked  chan struct{}
	release chan struct{}
	listed  chan struct{}
}

func (r *blockingPriorityStatusSnapshotRepository) MarkPriorityWorkspaceHealthSyncFailed(ctx context.Context, userID, workspace, key string, count int) (bool, error) {
	marked, err := r.fakeRepository.MarkPriorityWorkspaceHealthSyncFailed(ctx, userID, workspace, key, count)
	close(r.marked)
	<-r.release
	return marked, err
}

func (r *blockingPriorityStatusSnapshotRepository) ListTargetActionStates(ctx context.Context, userID, workspace string) ([]TargetActionState, error) {
	close(r.listed)
	return r.fakeRepository.ListTargetActionStates(ctx, userID, workspace)
}

func TestPrioritySyncStatusLocksStateAndFailedTargetsTogether(t *testing.T) {
	base := newFakeRepository()
	base.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "failed", LastError: ErrorPriorityTargetNotVisible, PendingTargetCount: 1}
	repo := &blockingPriorityStatusSnapshotRepository{fakeRepository: base, marked: make(chan struct{}), release: make(chan struct{}), listed: make(chan struct{})}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}}
	service.priorityFailureTargets.Store(priorityRuntimeLeaseKey("user1", "ws1"), []priorityTargetFailure{{accountID: "100", reason: "not_visible"}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.markPriorityWorkspaceHealthSyncFailedDirect("user1", "ws1", requestError(ErrorPriorityWriteFailed), 1, []priorityTargetFailure{{accountID: "101", reason: "write_failed"}})
	}()
	<-repo.marked
	type result struct {
		status PrioritySyncStatusView
		err    error
	}
	read := make(chan result, 1)
	go func() {
		status, err := service.PrioritySyncStatus(context.Background(), "user1")
		read <- result{status, err}
	}()
	<-repo.listed
	select {
	case got := <-read:
		close(repo.release)
		<-done
		t.Fatalf("status read escaped atomic state/list replacement: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.release)
	<-done
	got := <-read
	if got.err != nil || got.status.ErrorKey != ErrorPriorityWriteFailed || len(got.status.FailedTargets) != 1 || got.status.FailedTargets[0].AccountID != "101" || got.status.FailedTargets[0].Reason != "write_failed" {
		t.Fatalf("status must read the new row and matching account snapshot: %+v", got)
	}
}

func TestPrioritySyncRoundFailureReasonPrecedence(t *testing.T) {
	for _, scenario := range []struct {
		reasons []string
		key     string
	}{
		{[]string{"other", "waiting_timeout", "write_failed", "not_visible"}, ErrorPriorityTargetNotVisible},
		{[]string{"other", "waiting_timeout", "write_failed"}, ErrorPriorityWriteFailed},
		{[]string{"other", "waiting_timeout"}, ErrorPriorityWaitingTimeout},
		{[]string{"other"}, ErrorPriorityTargetFailed},
	} {
		t.Run(scenario.key, func(t *testing.T) {
			now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
			base := newFakeRepository()
			base.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
			repo := &priorityFailureREDRepository{fakeRepository: base, waiting: map[string]error{}}
			service := &Service{repo: repo, actionNow: func() time.Time { return now }, accounts: fakeAdminAccountResolver{id: "ws1"}}
			inventory := map[string]*priorityTargetInventory{}
			waitingSince := map[string]time.Time{}
			var states []PrioritySyncState
			for index, reason := range scenario.reasons {
				id := fmt.Sprintf("%02d", index)
				targetID := "sub2api:ws1:" + id
				states = append(states, PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID})
				if reason != "not_visible" {
					inventory[targetID] = &priorityTargetInventory{target: AdminProbeTarget{TargetID: targetID, AccountID: id}, priorityPresent: true}
				}
				switch reason {
				case "other":
					repo.waiting[targetID] = errors.New("priority row unavailable")
				case "write_failed":
					repo.waiting[targetID] = errors.Join(&upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 503}, ErrRemoteActionPending)
				case "waiting_timeout":
					repo.waiting[targetID] = ErrRemoteActionLeaseLost
					waitingSince[targetID] = now.Add(-5 * time.Minute)
				case "not_visible":
					repo.waiting[targetID] = ErrRemoteActionEvidenceChanged
				}
			}
			service.priorityWaitingSince.Store(priorityRuntimeLeaseKey("user1", "ws1"), waitingSince)
			service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, nil, states)
			status, err := service.PrioritySyncStatus(context.Background(), "user1")
			if err != nil || status.ErrorKey != scenario.key || status.FailedCount != len(scenario.reasons) {
				t.Fatalf("round reason priority mismatch: %+v err=%v", status, err)
			}
		})
	}
}

func TestPrioritySyncRoundTruncatesDisplayButTracksFullLogSignature(t *testing.T) {
	base := newFakeRepository()
	base.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
	waitingID := "sub2api:ws1:00"
	repo := &priorityFailureREDRepository{fakeRepository: base, waiting: map[string]error{waitingID: ErrRemoteActionPending}}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}, actionNow: func() time.Time { return now }}
	inventory := map[string]*priorityTargetInventory{waitingID: {target: AdminProbeTarget{TargetID: waitingID, AccountID: "00"}, priorityPresent: true}}
	states := []PrioritySyncState{{UserID: "user1", AdminAccountID: "ws1", TargetID: waitingID}}
	for index := 1; index <= 25; index++ {
		states = append(states, PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: fmt.Sprintf("sub2api:ws1:%02d", index)})
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	run := func() {
		service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, nil, states)
	}
	run()
	status, err := service.PrioritySyncStatus(context.Background(), "user1")
	stored, _ := service.priorityFailureTargets.Load(priorityRuntimeLeaseKey("user1", "ws1"))
	if err != nil || status.FailedCount != 25 || len(status.FailedTargets) != 10 || status.FailedTargets[0].AccountID != "01" || len(stored.([]priorityTargetFailure)) != 20 {
		t.Fatalf("memory/display/count truncation mismatch: %+v err=%v", status, err)
	}
	line := output.String()
	start, end := strings.Index(line, "targets="), strings.Index(line, " other_err=")
	if start < 0 || end < start || !strings.Contains(line, "failed=25 deferred=1") {
		t.Fatalf("missing full counts in summary: %q", line)
	}
	loggedTargets := strings.Split(line[start+len("targets="):end], ",")
	if len(loggedTargets) != 20 || loggedTargets[0] != "00:waiting" || loggedTargets[19] != "19:not_visible" {
		t.Fatalf("log must cap sorted account list at twenty: %+v", loggedTargets)
	}
	states[len(states)-1].TargetID = "sub2api:ws1:26"
	run()
	if strings.Count(output.String(), "priority sync round incomplete") != 2 {
		t.Fatalf("changed hidden account must change full signature: %q", output.String())
	}
}

func priorityStatusJSON(t *testing.T, service *Service) map[string]json.RawMessage {
	t.Helper()
	status, err := service.PrioritySyncStatus(context.Background(), "user1")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPrioritySyncStatusReturnsFailedTargetsWithNames(t *testing.T) {
	repo := newFakeRepository()
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
	service := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}, priorityActions: &fakeTargetPriorityActioner{}}
	states := make([]PrioritySyncState, 0, 12)
	for i := 1; i <= 12; i++ {
		states = append(states, PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: fmt.Sprintf("sub2api:ws1:%02d", i), OriginalPriority: 7, LastAppliedPriority: 1})
	}
	service.actionInventoryViews.Store(priorityRuntimeLeaseKey("user1", "ws1"), &actionInventoryView{complete: true, names: map[string]string{"01": "库存名字"}})
	service.updateActionSweepView("user1", "ws1", func(view *actionSweepView) {
		view.conclusions["02"] = actionAccountConclusion{accountName: "清扫名字"}
	})
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", nil, true, nil, states)
	var targets []struct {
		AccountID   string `json:"accountId"`
		AccountName string `json:"accountName"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(priorityStatusJSON(t, service)["failedTargets"], &targets); err != nil {
		t.Fatalf("failed status must return targets: %v", err)
	}
	if len(targets) != 10 || targets[0].AccountID != "01" || targets[0].AccountName != "库存名字" || targets[1].AccountName != "清扫名字" || targets[2].AccountName != "" || targets[0].Reason != "not_visible" {
		t.Fatalf("failed targets must be sorted, capped, and named from inventory then sweep: %+v", targets)
	}
	restarted := &Service{repo: repo, accounts: fakeAdminAccountResolver{id: "ws1"}}
	if _, exists := priorityStatusJSON(t, restarted)["failedTargets"]; exists {
		t.Fatal("a restarted process must retain the error but have no in-memory failure list")
	}
	for _, decision := range []string{"partial", "running", "pending"} {
		state := repo.priorityWorkspaces["user1|ws1"]
		state.PendingSignature, state.LastDecision = "generation", decision
		repo.priorityWorkspaces["user1|ws1"] = state
		if _, exists := priorityStatusJSON(t, service)["failedTargets"]; exists {
			t.Fatalf("%s status must not expose old failed targets", decision)
		}
	}
	state := repo.priorityWorkspaces["user1|ws1"]
	state.PendingSignature = ""
	repo.priorityWorkspaces["user1|ws1"] = state
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", nil, true, nil, nil)
	if _, exists := priorityStatusJSON(t, service)["failedTargets"]; exists {
		t.Fatal("successful round must not expose old failed targets")
	}
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", nil, true, nil, states)
	service.markPriorityWorkspaceHealthSyncFailedDirect("user1", "ws1", requestError(ErrorUnknown), 1, nil)
	if _, exists := priorityStatusJSON(t, service)["failedTargets"]; exists {
		t.Fatal("whole-round failure must clear old failed targets")
	}
	// NewAPI keeps the old unknown reason and does not collect account failures.
	service.priorityActions = &failingTargetPriorityActioner{err: &upstream.RequestError{MessageKey: upstream.ErrorRequest, StatusCode: 503}}
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformNewAPI}, "user1", "ws1", nil, true, nil, []PrioritySyncState{{UserID: "user1", AdminAccountID: "ws1", TargetID: "newapi:ws1:100", OriginalPriority: 7, LastAppliedPriority: 1}})
	newAPIState, err := repo.GetPriorityWorkspaceSyncState(context.Background(), "user1", "ws1")
	if err != nil || newAPIState == nil || newAPIState.LastDecision != "failed" || newAPIState.LastError != ErrorUnknown {
		t.Fatalf("NewAPI must keep the unknown error: state=%+v err=%v", newAPIState, err)
	}
	if _, exists := priorityStatusJSON(t, service)["failedTargets"]; exists {
		t.Fatal("NewAPI must not expose Sub2API failure names")
	}
}

func TestPrioritySyncOtherFailureLogsOriginalErrorAndDeferredCount(t *testing.T) {
	base := newFakeRepository()
	base.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
	repo := &priorityFailureREDRepository{fakeRepository: base, waiting: map[string]error{
		"sub2api:ws1:100": errors.New("priority row read: local database unavailable"),
		"sub2api:ws1:101": ErrRemoteActionPending,
	}}
	service := &Service{repo: repo}
	var states []PrioritySyncState
	inventory := map[string]*priorityTargetInventory{}
	for _, id := range []string{"100", "101"} {
		targetID := "sub2api:ws1:" + id
		states = append(states, PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID})
		inventory[targetID] = &priorityTargetInventory{target: AdminProbeTarget{TargetID: targetID, AccountID: id}, priorityPresent: true}
	}
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", inventory, true, nil, states)
	line := output.String()
	if !strings.Contains(line, "failed=1 deferred=1") || !strings.Contains(line, "targets=100:other,101:waiting") || !strings.Contains(line, "other_err=priority row read: local database unavailable") {
		t.Fatalf("summary must retain the local failure and separate waiting count: %q", line)
	}
}

func TestPrioritySyncRoundLogThrottle(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	repo := newFakeRepository()
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", LastDecision: "success"}
	service := &Service{repo: repo, actionNow: func() time.Time { return now }}
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	run := func(id string) {
		states := []PrioritySyncState(nil)
		if id != "" {
			states = []PrioritySyncState{{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:" + id, OriginalPriority: 7, LastAppliedPriority: 1}}
		}
		service.syncWorkspacePriorities(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", nil, true, nil, states)
	}
	count := func() int {
		return strings.Count(output.String(), "[connection-health] priority sync round incomplete")
	}
	run("100")
	if count() != 1 || !strings.Contains(output.String(), "100:not_visible") || !strings.Contains(output.String(), "failed=1 deferred=0") {
		t.Fatalf("first incomplete round must log an account summary: %q", output.String())
	}
	now = now.Add(9 * time.Minute)
	run("100")
	if count() != 1 {
		t.Fatal("same signature logged before ten minutes")
	}
	run("101")
	if count() != 2 {
		t.Fatal("changed account signature must log immediately")
	}
	now = now.Add(10 * time.Minute)
	run("101")
	if count() != 3 {
		t.Fatal("same signature must log at ten-minute boundary")
	}
	run("")
	run("101")
	if count() != 4 {
		t.Fatal("successful round must clear log throttling")
	}
}

package chanshui

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/shared/authctx"
)

// A distinct pending stop owns the original 15 second confirmation window.
// The fake synchronously stalls like a management call and deliberately does
// not acknowledge process exit; repeated callers cannot dispatch another stop.
func TestExecutionSingleStopClaimAndManualReauthorization(t *testing.T) {
	s, _, _ := fixture(t)
	o := Owner{"user-a", "workspace-a"}
	id := queueInput("a").ReportID
	entered, allow := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a := &activeTask{Owner: o, ReportID: id, Job: &Job{Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} }, stop: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-allow
		}
		return nil
	}}}
	s.active = a
	first := make(chan error, 1)
	go func() { _, err := s.Cancel(t.Context(), o, id); first <- err }()
	<-entered
	s.mu.Lock()
	deadline := a.StopConfirmDeadlineMs
	s.mu.Unlock()
	// Fast-forward the observable confirmation window, not an actual timer.
	s.mu.Lock()
	a.StopConfirmDeadlineMs = time.Now().Add(-time.Millisecond).UnixMilli()
	expired := a.StopConfirmDeadlineMs
	s.mu.Unlock()
	if _, err := s.Cancel(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if calls.Load() != 1 || !a.StopCallPending || a.StopConfirmDeadlineMs != expired || deadline <= time.Now().UnixMilli() {
		s.mu.Unlock()
		t.Fatal("duplicate stop renewed or stole original confirmation window")
	}
	s.mu.Unlock()
	view := s.runtime(t.Context(), o)["active"].(map[string]any)
	if view["executionPhase"] != "unknown" || view["stopCallPending"] != true || view["finishConfirmed"] != false {
		t.Fatal(view)
	}
	close(allow)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if calls.Load() != 2 || a.StopCallPending || a.StopConfirmDeadlineMs <= time.Now().UnixMilli() {
		t.Fatal("explicit new stop after returned/expired call did not receive one new claim")
	}
}

// The Job published after a cancelled preparation must consume the initial
// cancellation window, not renew it or call Stop twice through another entry.
func TestExecutionCompensationConsumesOriginalStopWindow(t *testing.T) {
	s, _, _ := fixture(t)
	o := Owner{"user-a", "workspace-a"}
	id := queueInput("a").ReportID
	var cancelled, calls atomic.Int32
	a := &activeTask{Owner: o, ReportID: id, Preparing: true, CancelPrepare: func() { cancelled.Add(1) }}
	s.active = a
	if _, err := s.Cancel(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if a.StopCallPending || cancelled.Load() != 1 {
		t.Fatal("prepare cancellation pretended to dispatch Job.Stop")
	}
	deadline := a.StopConfirmDeadlineMs
	job := &Job{Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} }, stop: func(context.Context) error { calls.Add(1); return nil }}
	a.Job = job
	a.Preparing = false
	claimed := s.claimActiveStopLocked(a, false)
	if claimed != job || a.StopConfirmDeadlineMs != deadline {
		t.Fatal("compensation renewed cancellation window")
	}
	s.mu.Unlock()
	if _, err := s.Cancel(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	if err := s.callClaimedStop(t.Context(), a, claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("compensation/manual overlap", calls.Load())
	}
}

// While an older exhausted save remains blocked, a newly finished report or
// accepted cancellation must have its own recovery cycle, not replay the old
// budget. Foreign owner cannot reset the exhausted intent.
func TestExecutionRecoveryDoesNotRenewOldEventForNewCancellation(t *testing.T) {
	s, f, _ := queueFixture(t)
	owner := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), owner, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), owner, b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.finishFailure = a.ReportID
	f.mu.Unlock()
	f.release(a.ReportID, false)
	waitRecovery(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && s.active.RecoveryAttempted && !s.recoveryRunning
	})
	s.mu.Lock()
	oldDeadline := s.active.recoveryDeadlineMs
	s.mu.Unlock()
	if _, err := s.RecoverRuntime(t.Context(), Owner{"other", "workspace-a"}); err == nil {
		t.Fatal("foreign owner reset recovery")
	}
	if _, err := s.Cancel(t.Context(), owner, b.ReportID); err != nil {
		t.Fatal(err)
	}
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.recoveryRunning && len(s.blocked) == 0 })
	s.mu.Lock()
	if s.active == nil || s.active.ReportID != a.ReportID || s.active.recoveryDeadlineMs != oldDeadline {
		s.mu.Unlock()
		t.Fatal("new cancellation renewed or lost exhausted recovery")
	}
	s.mu.Unlock()
	f.mu.Lock()
	if f.statuses[b.ReportID] != "cancelled" || len(f.order) != 1 {
		f.mu.Unlock()
		t.Fatal("new cancellation event was lost or models replayed")
	}
	f.finishFailure = ""
	f.mu.Unlock()
	if _, err := s.RecoverRuntime(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil && !s.recoveryRunning })
}

// The original query slot is observable before a process has even been
// created. Its retained state cannot prevent independent owner-scoped Stop.
func TestExecutionRetainedQueryDoesNotBlockOwnerStopOrLeakDetails(t *testing.T) {
	r := NewRunner(RunnerConfig{})
	owner := Owner{"user-a", "workspace-a"}
	entered, allow := make(chan struct{}), make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := r.query(t.Context(), Envelope{Operation: "create-batch", Owner: owner}, nil, false, func(context.Context, string, Envelope, uint64, int) (*process, error) {
			close(entered)
			<-allow
			return nil, problem(503, "startup", "隔离连接失败")
		})
		returned <- err
	}()
	<-entered
	state := r.QueryStatus(owner)
	if state.State != "busy" || state.DeadlineMs <= time.Now().UnixMilli() {
		t.Fatal("prelaunch query not observable", state)
	}
	r.queryMu.Lock()
	r.queryRef.cleanupDeadline = time.Now().Add(-time.Second)
	r.queryRef.reason = "当前Owner的具体保存原因"
	r.queryMu.Unlock()
	foreign := r.QueryStatus(Owner{"other", "workspace-a"})
	if foreign.State != "retained" || strings.Contains(foreign.Reason, "具体保存原因") {
		t.Fatal("retained state leaked owner detail", foreign)
	}
	s, _, _ := fixture(t)
	s.initialized = false
	s.runner = r
	id := queueInput("a").ReportID
	var stopped atomic.Int32
	s.active = &activeTask{Owner: owner, ReportID: id, Job: &Job{Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} }, stop: func(context.Context) error { stopped.Add(1); return nil }}}
	view := s.runtime(t.Context(), owner)
	if view["queryState"] != "retained" || view["active"] == nil {
		t.Fatal("runtime attempted initialization instead of in-memory view", view)
	}
	req := httptest.NewRequest("POST", "/api/chanshui/reports/"+id+"/cancel", strings.NewReader("{}"))
	req = req.WithContext(authctx.WithUserID(req.Context(), owner.UserID))
	req.Header.Set(WorkspaceHeader, owner.WorkspaceID)
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, req)
	if recorder.Code != 200 || stopped.Load() != 1 {
		t.Fatal("known active Stop was lost behind retained query", recorder.Code, recorder.Body.String())
	}
	close(allow)
	if err := <-returned; err == nil {
		t.Fatal("isolated connection unexpectedly succeeded")
	}
	if r.QueryStatus(owner).State != "" {
		t.Fatal("late confirmed no-process return did not release original slot")
	}
}

// The second cancellation path claims the exact active while admission is
// held, then invokes Stop outside admission. Even a full queue/round advance
// between the claim and its invocation cannot lose the response or stop the
// newly reserved task.
func TestExecutionCancelClaimSurvivesQueueAdvanceWithoutStoppingNewRound(t *testing.T) {
	s, _, _ := fixture(t)
	owner := Owner{"user-a", "workspace-a"}
	id := queueInput("a").ReportID
	oldRound, newRound := queueInput("b").ReportID, queueInput("c").ReportID
	var oldStops, newStops atomic.Int32
	old := &activeTask{Owner: owner, ReportID: id, Input: StartInput{RetryID: oldRound}, Job: &Job{
		Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} },
		stop: func(context.Context) error { oldStops.Add(1); return nil },
	}}
	s.admission.Lock()
	s.mu.Lock()
	s.active = old
	s.mu.Unlock()
	action, known, err := s.claimKnownActiveCancel(owner, id, oldRound)
	if err != nil || !known || action == nil {
		s.admission.Unlock()
		t.Fatal("known original round was not claimed", known, err)
	}
	s.admission.Unlock()
	// A completion worker can advance immediately after admission is released.
	s.mu.Lock()
	s.active = &activeTask{Owner: owner, ReportID: id, Input: StartInput{RetryID: newRound}, Job: &Job{
		Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} },
		stop: func(context.Context) error { newStops.Add(1); return nil },
	}}
	s.mu.Unlock()
	result, err := action(t.Context())
	if err != nil || result == nil || result["reportId"] != id || retryID(result) != oldRound || result["finishConfirmed"] != false || result["executionPhase"] != "unknown" {
		t.Fatal("accepted original-round response lost or falsely confirmed", result, err)
	}
	if oldStops.Load() != 1 || newStops.Load() != 0 {
		t.Fatal("claimed stop changed task identity", oldStops.Load(), newStops.Load())
	}
}

func TestExecutionOrphanRunningStatusDoesNotClaimProcessExit(t *testing.T) {
	owner := Owner{"user-a", "workspace-a"}
	s, _, _ := fixture(t)
	id := queueInput("a").ReportID
	for _, tt := range []struct {
		status           string
		exited, finished bool
		phase            string
	}{
		{"running", false, false, "unknown"},
		{"queued", true, false, "unknown"},
		{"done", true, true, "ended"},
	} {
		report := map[string]any{"reportId": id, "status": tt.status}
		s.decorateReport(owner, id, report)
		if report["processExited"] != tt.exited || report["finishConfirmed"] != tt.finished || report["executionPhase"] != tt.phase {
			t.Fatal(tt.status, report)
		}
	}
}

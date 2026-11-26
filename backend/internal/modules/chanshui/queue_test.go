package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/connection_health"
)

type queuedFixtureTargets struct {
	fakeTargets
	prepareEntered chan struct{}
	prepareOnce    sync.Once
	change         atomic.Bool
	mu             sync.Mutex
	workspaces     []string
}

func (f *queuedFixtureTargets) ResolveAuditContextInWorkspace(ctx context.Context, user, workspace, target, group string, need bool) (connection_health.AuditContext, error) {
	f.mu.Lock()
	f.workspaces = append(f.workspaces, workspace)
	f.mu.Unlock()
	if f.prepareEntered != nil {
		f.prepareOnce.Do(func() { close(f.prepareEntered) })
		<-ctx.Done()
		return connection_health.AuditContext{}, ctx.Err()
	}
	if f.change.Load() {
		return connection_health.AuditContext{}, fmt.Errorf("账号已经删除")
	}
	return f.fakeTargets.ResolveAuditContext(ctx, user, target, group, need)
}

type heldQueueJob struct {
	done   chan struct{}
	exited atomic.Bool
	once   sync.Once
}
type queueFixtureExecutor struct {
	fakeExecutor
	mu                                       sync.Mutex
	statuses                                 map[string]string
	owners                                   map[string]Owner
	requests                                 map[string]StartInput
	jobs                                     map[string]*heldQueueJob
	order                                    []string
	started                                  chan string
	maxRunning                               int
	cancelFailure, finishFailure, lostCreate string
}

func (f *queueFixtureExecutor) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, _ := e.Args.(map[string]any)
	id, _ := a["reportId"].(string)
	view := func() map[string]any {
		return map[string]any{"reportId": id, "status": f.statuses[id], "memoryLimitMiB": 128, "totalTimeoutSeconds": 60}
	}
	switch e.Operation {
	case "read-report":
		if f.statuses[id] == "" || f.owners[id] != e.Owner {
			return nil, problem(404, "report", "找不到报告")
		}
		if request, ok := a["request"].(StartInput); ok && mustJSON(request) != mustJSON(f.requests[id]) {
			return nil, problem(409, "idempotency", "不同请求")
		}
	case "create-report":
		f.statuses[id], f.owners[id] = "queued", e.Owner
		f.requests[id] = a["request"].(StartInput)
		if f.lostCreate == id {
			return nil, problem(503, "query", "模拟提交后响应丢失")
		}
	case "cancel-queued":
		if f.statuses[id] == "" || f.owners[id] != e.Owner {
			return nil, problem(404, "report", "找不到报告")
		}
		if f.cancelFailure == id {
			return nil, problem(503, "storage", "模拟取消写失败")
		}
		if f.statuses[id] == "queued" {
			f.statuses[id] = "cancelled"
		} else if f.statuses[id] == "running" {
			return nil, problem(409, "cancel", "已经运行")
		}
	case "finish-interrupted":
		if f.statuses[id] == "" || f.owners[id] != e.Owner {
			return nil, problem(404, "report", "找不到报告")
		}
		if f.finishFailure == id {
			return nil, problem(503, "storage", "模拟中断保存失败")
		}
		if f.statuses[id] == "queued" || f.statuses[id] == "running" {
			f.statuses[id] = "interrupted"
		}
	case "recover":
		for id, status := range f.statuses {
			if status == "queued" || status == "running" {
				f.statuses[id] = "interrupted"
			}
		}
	default:
		return nil, fmt.Errorf("unexpected queue fixture operation %s", e.Operation)
	}
	return fakeResponse(view()), nil
}
func (f *queueFixtureExecutor) Start(_ context.Context, e Envelope, _ Credential, _, _ int) (*Job, error) {
	id := e.Args.(map[string]any)["reportId"].(string)
	f.mu.Lock()
	j := &heldQueueJob{done: make(chan struct{})}
	f.jobs[id] = j
	f.order = append(f.order, id)
	f.statuses[id] = "running"
	running := 0
	for _, job := range f.jobs {
		if !job.exited.Load() {
			running++
		}
	}
	if running > f.maxRunning {
		f.maxRunning = running
	}
	f.mu.Unlock()
	f.started <- id
	return &Job{Done: j.done, info: func() ExitInfo { return ExitInfo{Exited: j.exited.Load()} }, stop: func(context.Context) error { f.release(id, false); return nil }}, nil
}
func (f *queueFixtureExecutor) Shutdown(context.Context) error {
	f.mu.Lock()
	ids := make([]string, 0, len(f.jobs))
	for id := range f.jobs {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.release(id, false)
	}
	return nil
}
func (f *queueFixtureExecutor) release(id string, success bool) {
	f.mu.Lock()
	j := f.jobs[id]
	if j != nil && success {
		f.statuses[id] = "done"
	}
	f.mu.Unlock()
	if j != nil {
		j.once.Do(func() { j.exited.Store(true); close(j.done) })
	}
}
func (f *queueFixtureExecutor) status(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses[id]
}
func queueFixture(t *testing.T) (*Service, *queueFixtureExecutor, *queuedFixtureTargets) {
	t.Helper()
	f := &queueFixtureExecutor{statuses: map[string]string{}, owners: map[string]Owner{}, requests: map[string]StartInput{}, jobs: map[string]*heldQueueJob{}, started: make(chan string, 130)}
	targets := &queuedFixtureTargets{}
	s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, targets: targets, runner: f, initialized: true}
	t.Cleanup(func() {
		f.mu.Lock()
		f.cancelFailure = ""
		f.finishFailure = ""
		f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("queue fixture cleanup: %v", err)
		}
	})
	return s, f, targets
}
func queueInput(letter string) StartInput {
	v := startInput()
	v.ReportID = strings.Repeat(letter, 32)
	return v
}
func waitQueue(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue condition timed out")
}
func waitRecovery(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("recovery condition timed out")
}
func nextStarted(t *testing.T, f *queueFixtureExecutor, want string) {
	t.Helper()
	select {
	case got := <-f.started:
		if got != want {
			t.Fatalf("start order %s want %s", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("report %s did not start", want)
	}
}

func TestQueueFIFORefillsOnlyAfterPreviousProcessExited(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b, c := queueInput("a"), queueInput("b"), queueInput("c")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	for _, v := range []StartInput{b, c, b} {
		result, err := s.Start(t.Context(), o, v)
		if err != nil || result.(map[string]any)["status"] != "queued" {
			t.Fatalf("enqueue %+v %v", result, err)
		}
	}
	f.mu.Lock()
	if len(f.order) != 1 {
		t.Fatal("waiting report started while first process alive")
	}
	f.mu.Unlock()
	f.release(a.ReportID, true)
	nextStarted(t, f, b.ReportID)
	f.release(b.ReportID, true)
	nextStarted(t, f, c.ReportID)
	f.release(c.ReportID, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxRunning != 1 || len(f.order) != 3 {
		t.Fatalf("overlap/duplicate: max=%d order=%v", f.maxRunning, f.order)
	}
}

func TestQueueLimitAllows128WaitingPlusActiveAndRejectsNextWithoutCreate(t *testing.T) {
	s, f, targets := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	active := queueInput("a")
	if _, err := s.Start(t.Context(), o, active); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, active.ReportID)
	var replay StartInput
	for i := 1; i <= maxWaitingReports; i++ {
		input := startInput()
		input.ReportID = fmt.Sprintf("%032x", i)
		value, err := s.Start(t.Context(), o, input)
		if err != nil || value.(map[string]any)["status"] != "queued" {
			t.Fatalf("waiting report %d rejected: value=%v error=%v", i, value, err)
		}
		if i == 1 {
			replay = input
		}
	}
	resolved := targets.calls.Load()
	rejected := startInput()
	rejected.ReportID = fmt.Sprintf("%032x", maxWaitingReports+1)
	if _, err := s.Start(t.Context(), o, rejected); err == nil || asProblem(err).Status != 429 {
		t.Fatalf("report beyond full waiting queue was accepted: %v", err)
	}
	if targets.calls.Load() != resolved {
		t.Fatal("full-queue rejection resolved upstream account or credentials")
	}
	f.mu.Lock()
	if f.statuses[rejected.ReportID] != "" || len(f.statuses) != maxWaitingReports+1 || len(f.order) != 1 {
		t.Fatalf("rejection created a report or started models: states=%d starts=%v", len(f.statuses), f.order)
	}
	f.mu.Unlock()
	value, err := s.Start(t.Context(), o, replay)
	if err != nil || value.(map[string]any)["status"] != "queued" || value.(map[string]any)["queuePosition"] != 1 {
		t.Fatalf("same-ID replay must remain readable at queue limit: %v %v", value, err)
	}
	s.mu.Lock()
	waiting := len(s.waiting)
	s.mu.Unlock()
	if waiting != maxWaitingReports || targets.calls.Load() != resolved {
		t.Fatal("same-ID replay changed full queue or resolved credentials again")
	}
}

func TestQueueCancelWriteFailureKeepsPositionAndBlocksAdvance(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.cancelFailure = b.ReportID
	f.mu.Unlock()
	result, err := s.Cancel(t.Context(), o, b.ReportID)
	if err != nil || result.(map[string]any)["executionPhase"] != "saving_cancel" || result.(map[string]any)["finishConfirmed"] != false {
		t.Fatalf("cancel acceptance falsely reported completed: %v %v", result, err)
	}
	waitQueue(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.blocked[reportKey{o, b.ReportID}] != nil && strings.Contains(s.blocked[reportKey{o, b.ReportID}].RecoveryReason, "模拟取消写失败")
	})
	s.mu.Lock()
	kept := len(s.waiting) == 1 && s.waiting[0].Input.ReportID == b.ReportID
	s.mu.Unlock()
	if !kept {
		t.Fatal("failed cancellation dropped queued report")
	}
	f.release(a.ReportID, true)
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil && !s.recoveryRunning })
	f.mu.Lock()
	if len(f.order) != 1 {
		t.Fatal("blocked queue advanced")
	}
	f.cancelFailure = ""
	f.mu.Unlock()
	if _, err := s.RecoverRuntime(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.blocked) == 0 && !s.recoveryRunning })
	if f.status(b.ReportID) != "cancelled" {
		t.Fatal("cancellation not saved")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiting) != 0 || len(s.blocked) != 0 {
		t.Fatal("saved cancellation did not clear queue blocker")
	}
}

func TestQueuePreparationCancelHasNoModelStartAndSavedCancelled(t *testing.T) {
	s, f, targets := queueFixture(t)
	targets.prepareEntered = make(chan struct{})
	o := Owner{"user-a", "workspace-a"}
	a := queueInput("a")
	finished := make(chan error, 1)
	go func() { _, err := s.Start(t.Context(), o, a); finished <- err }()
	select {
	case <-targets.prepareEntered:
	case <-time.After(time.Second):
		t.Fatal("preparation not entered")
	}
	if _, err := s.Cancel(t.Context(), o, a.ReportID); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	waitQueue(t, func() bool { return f.status(a.ReportID) == "cancelled" })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 0 {
		t.Fatal("cancelled preparation launched model job")
	}
}

func TestQueueChangedAccountIsInterruptedAndOriginalWorkspacePreserved(t *testing.T) {
	s, f, targets := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	s.workspace = &currentWorkspace{ID: "another-workspace"}
	targets.change.Store(true)
	f.release(a.ReportID, true)
	waitQueue(t, func() bool { return f.status(b.ReportID) == "interrupted" })
	f.mu.Lock()
	if len(f.order) != 1 {
		t.Fatal("changed account received a model job")
	}
	f.mu.Unlock()
	targets.mu.Lock()
	defer targets.mu.Unlock()
	for _, ws := range targets.workspaces {
		if ws != "workspace-a" {
			t.Fatal("queue followed current workspace")
		}
	}
}

func TestQueueCommittedCreateResponseLostNeverLeavesOrReplaysQueuedOrphan(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	f.mu.Lock()
	f.lostCreate = b.ReportID
	f.finishFailure = b.ReportID
	f.mu.Unlock()
	if _, err := s.Start(t.Context(), o, b); err == nil {
		t.Fatal("lost create response was reported accepted")
	}
	if f.status(b.ReportID) != "queued" {
		t.Fatal("fixture did not commit creation before failure")
	}
	f.release(a.ReportID, true)
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil && !s.recoveryRunning })
	if _, err := s.Start(t.Context(), o, queueInput("c")); err == nil {
		t.Fatal("unconfirmed queue was allowed to advance")
	}
	if _, err := s.Start(t.Context(), o, b); err == nil {
		t.Fatal("failed recovery was hidden on replay")
	}
	f.mu.Lock()
	f.finishFailure = ""
	f.mu.Unlock()
	if _, err := s.RecoverRuntime(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.blocked) == 0 && !s.recoveryRunning })
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 1 || f.statuses[b.ReportID] != "interrupted" {
		t.Fatal("orphan was replayed or left queued")
	}
}

func TestQueueUnknownQueuedReplayIsInterruptedWithoutPaidRun(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a := queueInput("a")
	f.mu.Lock()
	f.statuses[a.ReportID] = "queued"
	f.owners[a.ReportID] = o
	f.requests[a.ReportID] = a
	f.mu.Unlock()
	if _, err := s.Start(t.Context(), o, a); err == nil || asProblem(err).Status != 503 {
		t.Fatalf("underegistered creation not reported: %v", err)
	}
	waitRecovery(t, func() bool { return f.status(a.ReportID) == "interrupted" })
	result, err := s.Start(t.Context(), o, a)
	if err != nil || result.(map[string]any)["status"] != "interrupted" {
		t.Fatal(result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 0 {
		t.Fatal("unregistered report was automatically executed")
	}
}

func TestQueueShutdownInterruptsWaitingAndCannotStartNext(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 1 || f.statuses[a.ReportID] != "interrupted" || f.statuses[b.ReportID] != "interrupted" {
		t.Fatalf("shutdown replayed or lost queue: order=%v states=%v", f.order, f.statuses)
	}
}

func TestQueueMetadataDoesNotExposeOtherOwnersAndForeignStopCannotCancel(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), Owner{"other-user", "workspace-a"}, a.ReportID); err == nil {
		t.Fatal("foreign owner stopped active job")
	}
	if _, err := s.Cancel(t.Context(), Owner{"other-user", "workspace-a"}, b.ReportID); err == nil {
		t.Fatal("foreign owner cancelled waiting report")
	}
	result := map[string]any{"reportId": b.ReportID}
	s.decorateReport(o, b.ReportID, result)
	if result["queuePosition"] != 1 || result["queuedCount"] != 1 {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), a.ReportID) || strings.Contains(string(raw), "server-only-secret") {
		t.Fatal("queue metadata exposed another report or credential")
	}
}

func TestRunnerAcceptsLongExecutionLimitAndRejectsOverflowBeforeLaunch(t *testing.T) {
	r := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: filepath.Join(t.TempDir(), "not-installed"), DataDir: t.TempDir()})
	for _, seconds := range []int{3600, 7200, maxTaskSeconds} {
		_, err := r.Start(t.Context(), Envelope{}, Credential{}, 128, seconds)
		if err == nil || asProblem(err).Step == "settings" {
			t.Fatalf("valid long limit %d rejected by settings: %v", seconds, err)
		}
	}
	for _, seconds := range []int{59, maxTaskSeconds + 1} {
		_, err := r.Start(t.Context(), Envelope{}, Credential{}, 128, seconds)
		if err == nil || asProblem(err).Step != "settings" {
			t.Fatalf("unsafe duration %d reached launch: %v", seconds, err)
		}
	}
}

func TestQueuePreparationCancellationSaveFailureCannotReleaseNextReport(t *testing.T) {
	s, f, targets := queueFixture(t)
	targets.prepareEntered = make(chan struct{})
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	f.mu.Lock()
	f.cancelFailure = a.ReportID
	f.mu.Unlock()
	finished := make(chan error, 1)
	go func() { _, err := s.Start(t.Context(), o, a); finished <- err }()
	select {
	case <-targets.prepareEntered:
	case <-time.After(time.Second):
		t.Fatal("preparation not entered")
	}
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(t.Context(), o, a.ReportID); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	waitQueue(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && !s.active.Preparing && strings.Contains(s.active.RecoveryReason, "模拟取消写失败")
	})
	s.mu.Lock()
	kept := s.active != nil && s.active.ReportID == a.ReportID && !s.active.Preparing && len(s.waiting) == 1
	s.mu.Unlock()
	if !kept || f.status(a.ReportID) != "queued" {
		t.Fatal("failed cancellation released preparation or lost waiting report")
	}
	f.mu.Lock()
	if len(f.order) != 0 {
		t.Fatal("model started after failed preparation cancellation")
	}
	f.cancelFailure = ""
	f.mu.Unlock()
	waitRecovery(t, func() bool { return f.status(a.ReportID) == "cancelled" })
	// The next report may enter preparation only after the prior cancellation saved.
	waitRecovery(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && s.active.ReportID == b.ReportID
	})
}

func TestQueueSavedTerminalAloneDoesNotOverlapStillAliveProcess(t *testing.T) {
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), o, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	f.mu.Lock()
	f.statuses[a.ReportID] = "done"
	f.mu.Unlock()
	if _, err := s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.order) != 1 {
		t.Fatal("terminal database report released a living process")
	}
	f.mu.Unlock()
	f.release(a.ReportID, false)
	nextStarted(t, f, b.ReportID)
	f.release(b.ReportID, true)
}

func TestSystemdLongDurationKeepsCleanupAllowanceAndRejectsUnsafeValues(t *testing.T) {
	m, f := managerFixture(t)
	dev, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	if err = m.start(t.Context(), "duration-test.service", "run", RunnerConfig{DisableBankSync: true, BinaryPath: "/project/probe", ProjectDir: "/project", DataDir: "/project/data", Manager: "user"}, 128<<20, maxTaskSeconds, dev, dev, dev); err != nil {
		t.Fatal(err)
	}
	calls := callsNamed(f, "StartTransientUnit")
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	found := false
	for _, p := range calls[0].args[2].([]unitProperty) {
		if p.Name == "RuntimeMaxUSec" {
			found = true
			if p.Value.Value() != uint64(maxTaskSeconds+10)*1000000 {
				t.Fatal("systemd duration overflowed or lost cleanup allowance")
			}
		}
	}
	if !found {
		t.Fatal("systemd runtime limit missing")
	}
	if err = m.start(t.Context(), "unsafe-duration.service", "run", RunnerConfig{}, 128<<20, maxTaskSeconds+1, dev, dev, dev); err == nil {
		t.Fatal("unsafe systemd duration launched")
	}
	if len(callsNamed(f, "StartTransientUnit")) != 1 {
		t.Fatal("unsafe duration reached systemd")
	}
}

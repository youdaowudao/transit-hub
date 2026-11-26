package chanshui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
)

// Execute the real fixture transaction first, then fail its completed response
// cleanup. The stored rows and first finish are already terminal while the
// parent still has an unresolved whole-batch acceptance-recovery intent.
type lostBatchRecoveryReply struct {
	*batchContractExecutor
	loseExit atomic.Bool
}
type lostBatchRecoveryClose struct{ io.ReadCloser }

func (b lostBatchRecoveryClose) Close() error {
	_ = b.ReadCloser.Close()
	return errors.New("隔离测试：整批保存成功但返回确认丢失")
}
func (f *lostBatchRecoveryReply) Query(ctx context.Context, env Envelope, cred *Credential, internal bool) (*QueryResponse, error) {
	response, err := f.batchContractExecutor.Query(ctx, env, cred, internal)
	if err != nil {
		return nil, err
	}
	if env.Operation == "interrupt-batch-unstarted" && f.loseExit.Load() {
		response.Body = lostBatchRecoveryClose{response.Body}
	}
	if env.Operation != "read-report" {
		return response, nil
	}
	var report map[string]any
	if err = json.NewDecoder(response.Body).Decode(&report); err != nil {
		return nil, err
	}
	if err = response.Body.Close(); err != nil {
		return nil, err
	}
	f.batchMu.Lock()
	defer f.batchMu.Unlock()
	if f.request != nil && f.owner == env.Owner {
		f.mu.Lock()
		defer f.mu.Unlock()
		allTerminal := true
		member := false
		for _, task := range f.request.Tasks {
			allTerminal = allTerminal && isTerminal(f.statuses[task.ReportID])
			member = member || task.ReportID == stringField(report, "reportId")
		}
		if member {
			report["batchId"] = f.request.BatchID
			if allTerminal {
				report["batchFirstFinishedAtMs"] = 123456
			}
		}
	}
	return fakeResponse(report), nil
}
func TestBatchLostRecoveryReplyKeepsDirectReportAndFirstFinishUnconfirmed(t *testing.T) {
	s, base, _ := queueFixture(t)
	f := &lostBatchRecoveryReply{batchContractExecutor: &batchContractExecutor{queueFixtureExecutor: base, lostResponse: true}}
	f.loseExit.Store(true)
	s.runner = f
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	batch := BatchInput{BatchID: queueInput("c").ReportID, SettingsRevision: 1, SelectedSectionIDs: []string{"iq"}, Tasks: []BatchTaskInput{
		{ReportID: a.ReportID, TargetID: a.TargetID, GroupID: a.GroupID, Model: "model-A", ExpectedContext: a.ExpectedContext},
		{ReportID: b.ReportID, TargetID: b.TargetID, GroupID: b.GroupID, Model: "model-B", ExpectedContext: b.ExpectedContext},
	}}
	if _, err := s.StartBatch(t.Context(), o, batch); err == nil {
		t.Fatal("initial lost creation response was confirmed")
	}
	waitQueue(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		block := s.blocked[reportKey{o, "batch:" + batch.BatchID}]
		return block != nil && strings.Contains(block.RecoveryReason, "返回确认丢失")
	})
	if base.status(a.ReportID) != "interrupted" || base.status(b.ReportID) != "interrupted" {
		t.Fatal("fixture did not commit the end before dropping response")
	}
	get := func(path string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/chanshui/"+path, nil)
		r = r.WithContext(authctx.WithUserID(r.Context(), o.UserID))
		r.Header.Set(WorkspaceHeader, o.WorkspaceID)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	report := get("reports/" + a.ReportID)
	if report["status"] != "interrupted" || report["finishConfirmed"] != false || report["executionPhase"] == "ended" || numberField(report, "batchFirstFinishedAtMs") != 0 {
		t.Fatal("direct report opened retry or claimed first finish before parent recovery confirmation", report)
	}
	detail := get("batches/" + batch.BatchID)
	if detail["state"] == "ended" || numberField(detail, "firstFinishedAtMs") != 0 {
		t.Fatal("batch first finish was displayed as confirmed", detail)
	}
	for _, m := range mapsField(detail, "memberStates") {
		if m["finishConfirmed"] != false {
			t.Fatal("batch member falsely confirmed", m)
		}
	}
	if _, err := s.Retry(t.Context(), o, a.ReportID, RetryInput{RetryID: queueInput("d").ReportID, ItemIDs: []string{"Q2"}}); err == nil || asProblem(err).Status != 409 {
		t.Fatal("direct retry did not reject unconfirmed batch", err)
	}
	before := s.captureRuntime(o)
	f.loseExit.Store(false)
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.blocked) == 0 && !s.recoveryRunning })
	// The earlier SQL data arrived during a related blocker transition; this
	// individual read still cannot claim either end. A subsequent stable GET can.
	crossing := map[string]any{"reportId": a.ReportID, "batchId": batch.BatchID, "status": "interrupted", "batchFirstFinishedAtMs": 123456}
	s.decorateReadReport(o, a.ReportID, crossing, before)
	if crossing["executionPhase"] != "unknown" || crossing["finishConfirmed"] != false || numberField(crossing, "batchFirstFinishedAtMs") != 0 {
		t.Fatal("read spanning recovery transition was prematurely confirmed", crossing)
	}
	report = get("reports/" + a.ReportID)
	detail = get("batches/" + batch.BatchID)
	if report["finishConfirmed"] != true || numberField(report, "batchFirstFinishedAtMs") != 123456 || detail["state"] != "ended" || numberField(detail, "firstFinishedAtMs") != 123456 {
		t.Fatal("confirmed recovery did not restore original first finish", report, detail)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.order) != 0 {
		t.Fatal("unknown acceptance/confirmation dispatched model work", base.order)
	}
}

func TestLateConfirmedManualStopReclaimsOriginalResourcesAndStartsNext(t *testing.T) {
	p, _, _ := handoffPipe(t)
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { errRead.Close(); errWrite.Close() })
	p.stderr = errRead
	p.name = "late-stop-owned.service"
	p.done = make(chan struct{})
	p.monitored = true
	manager, connection := managerFixture(t)
	p.manager = manager
	s, f, _ := queueFixture(t)
	o := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	active := &activeTask{Owner: o, ReportID: a.ReportID, Input: a, OperationResult: "unknown", Job: &Job{Done: p.done, stop: p.stop, info: p.info}}
	s.active = active
	f.mu.Lock()
	f.statuses[a.ReportID] = "running"
	f.owners[a.ReportID] = o
	f.requests[a.ReportID] = a
	f.mu.Unlock()
	if _, err = s.Start(t.Context(), o, b); err != nil {
		t.Fatal(err)
	}
	// The old monitor has returned without a confirmed exit. Its original
	// cleanup deadline is now long expired, and a reader requested cleanup.
	p.cleanupDeadline = time.Now().Add(-time.Minute)
	p.cleanupMaximum = p.cleanupDeadline
	close(p.done)
	released := 0
	release := func() { released++ }
	if err = p.closeAfterConfirmedExit(release); err == nil || released != 0 || connection.closed != 0 {
		t.Fatal("unconfirmed old exit reclaimed resources", err, released, connection.closed)
	}
	s.finishReserved(active)
	view := s.runtime(t.Context(), o)["active"].(map[string]any)
	if view["executionPhase"] != "unknown" || view["processExited"] != false {
		t.Fatal(view)
	}
	// A fresh, explicitly accepted targeted Stop now sees the original unit
	// genuinely absent. The old monitor cannot produce a second Done event.
	connection.exists = false
	if _, err = s.Cancel(t.Context(), o, a.ReportID); err != nil {
		t.Fatal(err)
	}
	if !p.info().Exited || connection.closed != 1 || released != 1 {
		t.Fatal("confirmed late exit did not reclaim original resources", p.info(), connection.closed, released)
	}
	var unitUnref bool
	for _, call := range callsNamed(connection, "UnrefUnit") {
		if call.args[0] == p.name {
			unitUnref = true
		}
	}
	if !unitUnref {
		t.Fatal("late confirmation attempted release with stale expired context")
	}
	for _, pipe := range []*os.File{p.input, p.output, p.stderr} {
		if _, e := pipe.Stat(); !errors.Is(e, os.ErrClosed) {
			t.Fatal("original pipe not closed", e)
		}
	}
	if err = p.closeAfterConfirmedExit(release); err != nil || connection.closed != 1 || released != 1 {
		t.Fatal("duplicate cleanup", err, connection.closed, released)
	}
	nextStarted(t, f, b.ReportID)
	if f.status(a.ReportID) != "interrupted" {
		t.Fatal("next started before prior confirmed status was saved")
	}
	f.release(b.ReportID, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
}

func TestOnlyCurrentStoredExecutionTimingCanAssertBudgetExpired(t *testing.T) {
	s, _, _ := fixture(t)
	o := Owner{"user-a", "workspace-a"}
	id := queueInput("a").ReportID
	now := time.Now()
	// The outer process started before persisted RunOne timing; its nominal
	// external deadline must not claim the actual current-round budget is gone.
	s.active = &activeTask{Owner: o, ReportID: id, Job: &Job{Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{} }, ExitConfirmDeadline: now.Add(15 * time.Second)}}
	runtime := s.runtime(t.Context(), o)["active"].(map[string]any)
	if strings.Contains(stringField(runtime, "recoveryReason"), "本轮时限已到") {
		t.Fatal("outer timer invented elapsed execution budget", runtime)
	}
	timing := map[string]any{"started_at": now.Add(-55 * time.Second).Format(time.RFC3339Nano)}
	report := map[string]any{"reportId": id, "status": "running", "createdAtMs": now.Add(-70 * time.Second).UnixMilli(), "totalTimeoutSeconds": 60, "summary": map[string]any{"verdict": map[string]any{"timing": timing}}}
	s.decorateReport(o, id, report)
	if strings.Contains(stringField(report, "recoveryReason"), "本轮时限已到") {
		t.Fatal("reliable current budget still has time", report)
	}
	timing["started_at"] = now.Add(-61 * time.Second).Format(time.RFC3339Nano)
	s.decorateReport(o, id, report)
	if stringField(report, "recoveryReason") != currentBudgetExpiredReason {
		t.Fatal("reliable expired current budget not displayed", report)
	}
	retry := queueInput("b").ReportID
	s.mu.Lock()
	s.active.Input.RetryID = retry
	s.mu.Unlock()
	report["retry"] = map[string]any{"id": retry, "acceptedAtMs": now.Add(-10 * time.Second).UnixMilli()}
	delete(report, "recoveryReason")
	s.decorateReport(o, id, report)
	if stringField(report, "recoveryReason") == currentBudgetExpiredReason {
		t.Fatal("prior-round timing expired a new retry", report)
	}
	// Batch rows consume the same existing compact timing and current-round
	// accepted time, never the earlier Runner deadline or stale retry timing.
	view := s.captureRuntime(o)
	state := map[string]any{"reportId": id, "status": "running", "retryId": retry, "retryAcceptedAtMs": now.Add(-10 * time.Second).UnixMilli(), "totalTimeoutSeconds": 60, "timing": timing}
	row := map[string]any{"reportId": id, "createdAtMs": now.Add(-120 * time.Second).UnixMilli()}
	detail := map[string]any{"batchId": "batch", "memberStates": []map[string]any{state}, "items": []map[string]any{row}}
	projectBatch(detail, view, view, true)
	if stringField(row, "recoveryReason") == currentBudgetExpiredReason {
		t.Fatal("batch row expired a new retry from old timing", row)
	}
	state["retryAcceptedAtMs"] = now.Add(-90 * time.Second).UnixMilli()
	projectBatch(detail, view, view, true)
	if stringField(row, "recoveryReason") != currentBudgetExpiredReason {
		t.Fatal("batch row did not display reliable expired retry timing", row)
	}
}

// The service's real final-job/queued-cancel metadata paths invoke the same
// transaction contract as Store: mayFinalizeBatch must be true and every
// frozen member terminal before the first timestamp is committed. Fail the
// actual returned body close *after* that fixture transaction commits.
type lostLastMemberRecoveryReply struct {
	*batchAdmissionExecutor
	lastID, lastOp string
	loseExit       atomic.Bool
	firstFinished  int64 // protected by batchMu
}

func (f *lostLastMemberRecoveryReply) Query(ctx context.Context, env Envelope, cred *Credential, internal bool) (*QueryResponse, error) {
	args, _ := env.Args.(map[string]any)
	if env.Operation == "list-batches" || env.Operation == "read-batch" && args["request"] == nil {
		f.batchMu.Lock()
		defer f.batchMu.Unlock()
		if f.request == nil || env.Owner != f.owner || env.Operation == "read-batch" && args["batchId"] != f.request.BatchID {
			return nil, problem(404, "batch", "找不到批次")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		states, rows := []map[string]any{}, []map[string]any{}
		for _, task := range f.request.Tasks {
			state := f.statuses[task.ReportID]
			states = append(states, map[string]any{"reportId": task.ReportID, "status": state, "summaryComplete": state == "done"})
			rows = append(rows, map[string]any{"reportId": task.ReportID, "status": state})
		}
		batch := map[string]any{"batchId": f.request.BatchID, "createdAtMs": 123000, "firstFinishedAtMs": f.firstFinished, "memberStates": states}
		if env.Operation == "list-batches" {
			return fakeResponse(map[string]any{"items": []map[string]any{batch}}), nil
		}
		batch["items"] = rows
		return fakeResponse(batch), nil
	}
	response, err := f.batchAdmissionExecutor.Query(ctx, env, cred, internal)
	if err != nil {
		return nil, err
	}
	switch env.Operation {
	case "finish-interrupted", "cancel-queued":
		f.batchMu.Lock()
		defer f.batchMu.Unlock()
		if f.request != nil && env.Owner == f.owner && args["mayFinalizeBatch"] == true {
			f.mu.Lock()
			id := stringField(args, "reportId")
			allTerminal, member := true, false
			for _, task := range f.request.Tasks {
				allTerminal = allTerminal && isTerminal(f.statuses[task.ReportID])
				member = member || id == task.ReportID
			}
			f.mu.Unlock()
			if member && allTerminal {
				if f.firstFinished == 0 {
					f.firstFinished = 123456
				}
				if id == f.lastID && env.Operation == f.lastOp && f.loseExit.Load() {
					response.Body = lostBatchRecoveryClose{response.Body}
				}
			}
		}
	case "read-report":
		var row map[string]any
		if err = json.NewDecoder(response.Body).Decode(&row); err != nil {
			return nil, err
		}
		if err = response.Body.Close(); err != nil {
			return nil, err
		}
		f.batchMu.Lock()
		defer f.batchMu.Unlock()
		if f.request != nil && env.Owner == f.owner {
			for _, task := range f.request.Tasks {
				if task.ReportID == stringField(row, "reportId") {
					row["batchId"] = f.request.BatchID
					row["batchFirstFinishedAtMs"] = f.firstFinished
				}
			}
		}
		response = fakeResponse(row)
	}
	return response, nil
}

func TestBatchLastMemberRecoveryReplyLossMasksOnlyFirstFinishUntilConfirmed(t *testing.T) {
	for _, name := range []string{"last-running-job", "last-waiting-cancel"} {
		t.Run(name, func(t *testing.T) {
			s, base, _ := queueFixture(t)
			o := Owner{"user-a", "workspace-a"}
			a, b, outer := queueInput("a"), queueInput("b"), queueInput("d")
			f := &lostLastMemberRecoveryReply{
				batchAdmissionExecutor: &batchAdmissionExecutor{queueFixtureExecutor: base},
				lastID:                 b.ReportID,
				lastOp:                 "finish-interrupted",
			}
			f.loseExit.Store(true)
			s.runner = f
			if name == "last-waiting-cancel" {
				f.lastOp = "cancel-queued"
				// Another batch/single may remain active while this entire batch
				// waits. This makes the last queued cancellation itself final.
				if _, err := s.Start(t.Context(), o, outer); err != nil {
					t.Fatal(err)
				}
				nextStarted(t, base, outer.ReportID)
			}
			batch := BatchInput{BatchID: queueInput("c").ReportID, SettingsRevision: 1, SelectedSectionIDs: []string{"iq"}, Tasks: []BatchTaskInput{
				{ReportID: a.ReportID, TargetID: a.TargetID, GroupID: a.GroupID, Model: "model-A", ExpectedContext: a.ExpectedContext},
				{ReportID: b.ReportID, TargetID: b.TargetID, GroupID: b.GroupID, Model: "model-B", ExpectedContext: b.ExpectedContext},
			}}
			if _, err := s.StartBatch(t.Context(), o, batch); err != nil {
				t.Fatal(err)
			}
			wantStatus := "done"
			if name == "last-running-job" {
				nextStarted(t, base, a.ReportID)
				base.release(a.ReportID, true)
				nextStarted(t, base, b.ReportID)
				base.release(b.ReportID, true)
				waitQueue(t, func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					return s.active != nil && s.active.ReportID == b.ReportID && strings.Contains(s.active.RecoveryReason, "返回确认丢失")
				})
			} else {
				wantStatus = "cancelled"
				if _, err := s.Cancel(t.Context(), o, a.ReportID); err != nil {
					t.Fatal(err)
				}
				waitRecovery(t, func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					return !s.recoveryRunning && len(s.blocked) == 0 && base.status(a.ReportID) == wantStatus
				})
				if _, err := s.Cancel(t.Context(), o, b.ReportID); err != nil {
					t.Fatal(err)
				}
				waitQueue(t, func() bool {
					s.mu.Lock()
					defer s.mu.Unlock()
					block := s.blocked[reportKey{o, b.ReportID}]
					return block != nil && strings.Contains(block.RecoveryReason, "返回确认丢失")
				})
			}
			f.batchMu.Lock()
			savedFirst := f.firstFinished
			f.batchMu.Unlock()
			if savedFirst <= 0 || base.status(a.ReportID) != wantStatus || base.status(b.ReportID) != wantStatus {
				t.Fatal("fixture did not commit terminal states and first anchor before losing response", savedFirst)
			}
			get := func(path string) map[string]any {
				t.Helper()
				request := httptest.NewRequest("GET", "/api/chanshui/"+path, nil)
				request = request.WithContext(authctx.WithUserID(request.Context(), o.UserID))
				request.Header.Set(WorkspaceHeader, o.WorkspaceID)
				w := httptest.NewRecorder()
				s.ServeHTTP(w, request)
				if w.Code != 200 {
					t.Fatal(path, w.Code, w.Body.String())
				}
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			assertProjection := func(first int64, lastConfirmed bool) {
				t.Helper()
				prior := get("reports/" + a.ReportID)
				last := get("reports/" + b.ReportID)
				if prior["finishConfirmed"] != true || prior["executionPhase"] != "ended" || numberField(prior, "batchFirstFinishedAtMs") != first {
					t.Fatal("unresolved last member changed another member's individual confirmation or exposed the first anchor", prior)
				}
				if last["finishConfirmed"] != lastConfirmed || numberField(last, "batchFirstFinishedAtMs") != first {
					t.Fatal("last member projection does not match returned confirmation", last)
				}
				detail := get("batches/" + batch.BatchID)
				list := mapsField(get("batches"), "items")
				if len(list) != 1 || numberField(detail, "firstFinishedAtMs") != first || numberField(list[0], "firstFinishedAtMs") != first {
					t.Fatal("detail and list do not agree on first-run confirmation", detail, list)
				}
				if (detail["state"] == "ended") != lastConfirmed || (list[0]["state"] == "ended") != lastConfirmed {
					t.Fatal("detail/list falsely reported end", detail, list)
				}
				members := mapsField(detail, "memberStates")
				if len(members) != 2 || members[0]["finishConfirmed"] != true || members[1]["finishConfirmed"] != lastConfirmed {
					t.Fatal("member-level confirmation lost", members)
				}
			}
			assertProjection(0, false)
			if _, err := s.Retry(t.Context(), o, a.ReportID, RetryInput{RetryID: queueInput("e").ReportID, ItemIDs: []string{"Q2"}}); err == nil || asProblem(err).Status != 409 {
				t.Fatal("confirmed member was allowed to retry before first batch completion confirmation", err)
			}
			before := s.captureRuntime(o)
			f.loseExit.Store(false)
			waitRecovery(t, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return !s.recoveryRunning && len(s.blocked) == 0 && (s.active == nil || s.active.ReportID != b.ReportID)
			})
			// SQL may have already exposed the persisted anchor at the start
			// of this read; a crossed recovery still masks only the batch end.
			crossing := map[string]any{"reportId": a.ReportID, "batchId": batch.BatchID, "status": wantStatus, "batchFirstFinishedAtMs": savedFirst}
			s.decorateReadReport(o, a.ReportID, crossing, before)
			if crossing["finishConfirmed"] != true || numberField(crossing, "batchFirstFinishedAtMs") != 0 {
				t.Fatal("crossed last-member read exposed an unconfirmed anchor or hid another member's result", crossing)
			}
			members := []map[string]any{{"reportId": a.ReportID, "status": wantStatus}, {"reportId": b.ReportID, "status": wantStatus}}
			for _, detail := range []bool{true, false} {
				crossBatch := map[string]any{"batchId": batch.BatchID, "memberStates": members, "firstFinishedAtMs": savedFirst}
				projectBatch(crossBatch, before, s.captureRuntime(o), detail)
				if numberField(crossBatch, "firstFinishedAtMs") != 0 || crossBatch["state"] == "ended" {
					t.Fatal("crossed batch/detail read exposed unconfirmed first finish", crossBatch)
				}
			}
			assertProjection(savedFirst, true)
			if name == "last-waiting-cancel" {
				base.release(outer.ReportID, true)
				waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
			}
			base.mu.Lock()
			defer base.mu.Unlock()
			wantStarts := 2
			if name == "last-waiting-cancel" {
				wantStarts = 1
			}
			if len(base.order) != wantStarts {
				t.Fatal("recovering first completion replayed model work", base.order)
			}
		})
	}
}

func TestBatchConfirmedFirstFinishSurvivesLaterRetryAndUnrelatedOwner(t *testing.T) {
	s, _, _ := fixture(t)
	o := Owner{"user-a", "workspace-a"}
	other := Owner{"other", "workspace-a"}
	a, b, c := queueInput("a"), queueInput("b"), queueInput("c")
	batchID, retry := queueInput("d").ReportID, queueInput("e").ReportID
	s.active = &activeTask{Owner: o, ReportID: b.ReportID, BatchID: batchID, Input: StartInput{RetryID: retry}, Job: &Job{Done: make(chan struct{}), info: func() ExitInfo { return ExitInfo{Exited: true} }}}
	s.waiting = []*waitingTask{{Owner: o, BatchID: batchID, Input: StartInput{ReportID: c.ReportID, RetryID: retry}}, {Owner: o, BatchID: "other-batch", Input: StartInput{ReportID: "other-report"}}}
	s.blocked = map[reportKey]*blockedReport{
		{o, c.ReportID}:     {Owner: o, ID: c.ReportID, BatchID: batchID, RetryID: retry},
		{other, a.ReportID}: {Owner: other, ID: a.ReportID, BatchID: batchID},
	}
	before := s.captureRuntime(o)
	if before.batchFirstFinishUnconfirmed(batchID) {
		t.Fatal("later retry or another owner altered historical first finish")
	}
	report := map[string]any{"reportId": a.ReportID, "batchId": batchID, "status": "done", "batchFirstFinishedAtMs": 123456}
	s.decorateReadReport(o, a.ReportID, report, before)
	if report["finishConfirmed"] != true || numberField(report, "batchFirstFinishedAtMs") != 123456 {
		t.Fatal("confirmed first anchor changed during retry", report)
	}
	members := []map[string]any{{"reportId": a.ReportID, "status": "done"}, {"reportId": b.ReportID, "status": "partial", "retryId": retry}, {"reportId": c.ReportID, "status": "queued", "retryId": retry}}
	for _, detail := range []bool{true, false} {
		value := map[string]any{"batchId": batchID, "memberStates": members, "firstFinishedAtMs": 123456}
		projectBatch(value, before, s.captureRuntime(o), detail)
		if numberField(value, "firstFinishedAtMs") != 123456 {
			t.Fatal("batch retry changed historical anchor", value)
		}
	}
}

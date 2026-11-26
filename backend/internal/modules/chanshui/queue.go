package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"transithub/backend/internal/modules/connection_health"
)

const maxWaitingReports = 128
const maxTaskSeconds = 9223371976

type reportKey struct {
	Owner Owner
	ID    string
}
type waitingTask struct {
	Owner                               Owner
	Input                               StartInput
	AccountName, BatchID                string
	MemoryLimitMiB, TotalTimeoutSeconds int
}
type blockedReport struct {
	Owner                         Owner
	ID, Message, BatchID, RetryID string
	RecoveryReason                string
	RecoveryAttempted             bool
	recoveryDeadlineMs            int64
	BatchUnstarted                bool
	CancelQueued                  bool
}
type workspaceTargetReader interface {
	ResolveAuditContextInWorkspace(context.Context, string, string, string, string, bool) (connection_health.AuditContext, error)
}

func copyStartInput(input StartInput) StartInput {
	input.SingleAsk = append(json.RawMessage(nil), input.SingleAsk...)
	input.SelectedSectionIDs = append([]string(nil), input.SelectedSectionIDs...)
	input.SelectedQuestionIDs = append([]string(nil), input.SelectedQuestionIDs...)
	return input
}

func (s *Service) Start(ctx context.Context, owner Owner, input StartInput) (any, error) {
	ctx, cancel := admissionContext(ctx)
	defer cancel()
	if !reportIDPattern.MatchString(input.ReportID) || strings.TrimSpace(input.TargetID) == "" || len(input.Model) < 1 || len(input.Model) > 256 {
		return nil, problem(422, "input", "报告编号、账号或模型名称无效，请重新打开账号并选择模型后重试")
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, s.withQueryStatus(owner, err)
	}
	input = copyStartInput(input)
	if err := s.lockAdmission(ctx); err != nil {
		return nil, err
	}
	defer s.admission.Unlock()
	if err := s.checkAdmissionLocked(); err != nil {
		return nil, err
	}
	before := s.captureRuntime(owner)
	previous, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, true)
	if err == nil {
		result, e := decodeReport(previous, input.ReportID)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		known := s.containsWaitingLocked(owner, input.ReportID) || s.active != nil && s.active.Owner == owner && s.active.ReportID == input.ReportID
		blocked := s.blocked[reportKey{owner, input.ReportID}]
		if result["status"] == "queued" && !known && blocked == nil {
			if batchID := stringField(result, "batchId"); batchID != "" {
				s.registerBatchBlockedLocked(owner, batchID, "批次已保存但执行登记尚未确认，正在核查整批状态，不会自动执行")
			} else {
				s.registerBlockedLocked(owner, input.ReportID, stringField(result, "batchId"), retryID(result), "报告已保存，但未能确认加入等待队列；本报告没有自动开始，请查看结果后手动新建", false)
			}
		}
		s.mu.Unlock()
		if blocked != nil || result["status"] == "queued" && !known {
			p := problem(503, "recovery", "报告已保存，但执行登记尚未确认；后续检测正在核查或已暂停，请查看原报告并核查保存，不会重复发出检测请求")
			p.ReportID = input.ReportID
			return nil, p
		}
		s.decorateReadReport(owner, input.ReportID, result, before)
		result["accepted"] = true
		return result, nil
	}
	if asProblem(err).Status != 404 {
		return nil, err
	}
	if err := s.checkCapacityLocked(owner, 1); err != nil {
		return nil, err
	}
	audit, err := s.targets.ResolveAuditContext(ctx, owner.UserID, input.TargetID, input.GroupID, true)
	if err != nil {
		return nil, problem(409, "account", err.Error())
	}
	if err = validateTaskContext(owner, input, audit); err != nil {
		return nil, err
	}
	if _, err = checkedCredential(audit); err != nil {
		return nil, err
	}
	created, err := s.queryJSON(ctx, "create-report", owner, map[string]any{
		"reportId": input.ReportID, "targetId": input.TargetID, "groupId": input.GroupID, "model": input.Model, "selectedSectionIds": input.SelectedSectionIDs, "selectedQuestionIds": input.SelectedQuestionIDs, "singleAsk": input.SingleAsk, "expectedContext": input.ExpectedContext, "request": input,
		"target": map[string]any{"accountId": audit.AccountID, "accountName": audit.AccountName, "groupName": audit.GroupName, "baseURL": audit.BaseURL, "protocol": audit.Protocol},
	}, nil, false)
	if err != nil {
		issue := asProblem(err)
		issue.ReportID = input.ReportID
		if issue.Status >= 500 {
			s.mu.Lock()
			s.registerBlockedLocked(owner, input.ReportID, "", "", "创建报告的返回结果中断，未能确认已加入等待队列；本报告不会自动发出检测请求，请按报告编号查看后手动新建", false)
			s.mu.Unlock()
		}
		return nil, issue
	}
	var report struct {
		MemoryLimitMiB      int    `json:"memoryLimitMiB"`
		TotalTimeoutSeconds int    `json:"totalTimeoutSeconds"`
		Status              string `json:"status"`
	}
	if json.Unmarshal(created, &report) != nil || !validLimits(report.MemoryLimitMiB, report.TotalTimeoutSeconds) || report.Status != "queued" {
		issue := problem(503, "storage", "已创建报告，但运行设置或等待状态无法确认；未开始检测，请按原编号查看，不要重复新建")
		issue.ReportID = input.ReportID
		s.mu.Lock()
		s.registerBlockedLocked(owner, input.ReportID, "", "", issue.Message, false)
		s.mu.Unlock()
		return nil, issue
	}
	entry := &waitingTask{Owner: owner, Input: input, AccountName: audit.AccountName, MemoryLimitMiB: report.MemoryLimitMiB, TotalTimeoutSeconds: report.TotalTimeoutSeconds}
	s.mu.Lock()
	s.waiting = append(s.waiting, entry)
	s.mu.Unlock()
	next := s.reserveNextLocked()
	result := map[string]any{"reportId": input.ReportID, "status": "queued", "accepted": true}
	s.decorateReport(owner, input.ReportID, result)
	if next != nil {
		go s.startReserved(next)
	}
	return result, nil
}
func validLimits(memory, seconds int) bool {
	return memory >= 128 && memory <= 384 && seconds >= 60 && seconds <= maxTaskSeconds
}
func (s *Service) checkCapacityLocked(owner Owner, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	phase := ""
	if s.active != nil {
		phase = activePhase(s.active, time.Now().UnixMilli(), s.recoveryRunning, unixMilli(s.recoveryDeadline))
	}
	if len(s.blocked) > 0 || phase == "unknown" || phase == "paused" {
		reason := "有其他检测的结束状态尚未确认，后续检测已暂停"
		if s.active != nil && s.active.Owner == owner {
			if phase == "unknown" {
				reason = exitUnknownReason
			} else if phase == "paused" && s.active.RecoveryReason != "" {
				reason = s.active.RecoveryReason
			}
		}
		for _, b := range s.blocked {
			if b.Owner == owner {
				reason = b.Message
				if b.RecoveryReason != "" {
					reason = b.RecoveryReason
				}
				break
			}
		}
		return problem(503, "recovery", reason+"；本次未创建报告或发送请求")
	}

	if len(s.waiting)+n > maxWaitingReports {
		return problem(429, "queue", fmt.Sprintf("当前还可等待%d份，本次需要%d份；请减少组合或等待后重试，本次没有创建报告或发送检测请求", maxWaitingReports-len(s.waiting), n))
	}
	return nil
}

func validateTaskContext(owner Owner, input StartInput, audit connection_health.AuditContext) error {
	if !audit.CanRun {
		return problem(422, "account", audit.Reason)
	}
	if expected(audit) != input.ExpectedContext || audit.WorkspaceID != owner.WorkspaceID {
		return problem(409, "context", "账号、分组、地址或协议已变化，请刷新账号信息后重新确认；尚未发出检测请求")
	}
	return nil
}

func decodeReport(raw json.RawMessage, id string) (map[string]any, error) {
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result == nil || result["reportId"] != id {
		return nil, problem(503, "storage", "报告摘要无法读取或编号不一致，请检查检测程序版本；不会重复执行模型请求")
	}
	return result, nil
}

func confirmTerminal(raw json.RawMessage) error {
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &result) == nil {
		switch result.Status {
		case "done", "partial", "cancelled", "interrupted":
			return nil
		}
	}
	return problem(503, "storage", "报告结束状态尚未确认，等待队列已暂停；请在原报告点击停止检测重试保存，不会再次调用模型")
}

// admission is held for every queue mutation and active reservation. No worker
// spends its model-request lifetime holding that lock.
func (s *Service) reserveNextLocked() *activeTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.active != nil || len(s.blocked) > 0 || len(s.waiting) == 0 {
		return nil
	}
	entry := s.waiting[0]
	s.waiting = s.waiting[1:]
	prepareCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	active := &activeTask{Owner: entry.Owner, ReportID: entry.Input.ReportID, AccountName: entry.AccountName, BatchID: entry.BatchID, Input: entry.Input, Preparing: true, CancelPrepare: cancel, PreparationDone: make(chan struct{}), OperationResult: "not_started"}
	active.prepareContext = prepareCtx
	active.memoryMiB, active.totalSeconds = entry.MemoryLimitMiB, entry.TotalTimeoutSeconds
	s.active = active
	return active
}

func (s *Service) startReserved(active *activeTask) (any, error) {
	ctx := active.prepareContext
	defer active.CancelPrepare()
	defer close(active.PreparationDone)
	var audit connection_health.AuditContext
	var err error
	if targets, ok := s.targets.(workspaceTargetReader); ok {
		audit, err = targets.ResolveAuditContextInWorkspace(ctx, active.Owner.UserID, active.Owner.WorkspaceID, active.Input.TargetID, active.Input.GroupID, true)
	} else {
		workspace, e := s.workspace.RequireCurrentID(ctx, active.Owner.UserID)
		if e != nil || workspace != active.Owner.WorkspaceID {
			err = fmt.Errorf("无法核实排队报告的原工作区权限，未开始检测")
		} else {
			audit, err = s.targets.ResolveAuditContext(ctx, active.Owner.UserID, active.Input.TargetID, active.Input.GroupID, true)
		}
	}
	if err == nil {
		err = validateTaskContext(active.Owner, active.Input, audit)
	}
	var cred Credential
	if err == nil {
		cred, err = checkedCredential(audit)
	}
	s.mu.Lock()
	cancelled := active.CancelRequested || s.closing
	s.mu.Unlock()
	if cancelled || ctx.Err() != nil || err != nil {
		message := "准备检测未完成，尚未发送模型请求，请查看账号和工作区后手动新建"
		if err != nil {
			message = "排队报告开始前核查失败：" + err.Error() + "；本轮未发送检测请求，请处理后手动新建"
		}
		if cancelled {
			message = "已取消准备，本轮没有发送检测请求"
		}
		s.mu.Lock()
		active.Preparing = false
		active.Diagnostic = message
		s.mu.Unlock()
		s.finishReserved(active)
		if cancelled {
			return map[string]any{"reportId": active.ReportID, "status": "stopping", "processExited": false}, nil
		}
		issue := problem(409, "account", message)
		issue.ReportID = active.ReportID
		return nil, issue
	}
	job, startErr := s.runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: active.Owner, Args: map[string]any{"reportId": active.ReportID}}, cred, active.memoryMiB, active.totalSeconds)
	s.mu.Lock()
	active.Job = job
	active.Preparing = false
	active.OperationResult = "unknown"
	if startErr != nil {
		active.Diagnostic = asProblem(startErr).Message
		active.OperationResult = asProblem(startErr).OperationResult
	}
	cancelled = active.CancelRequested || s.closing
	var stop *Job
	if job != nil {
		go func() { <-job.Done; s.finishReserved(active) }()
		if active.CancelRequested {
			stop = s.claimActiveStopLocked(active, false)
		}
	}
	s.mu.Unlock()
	if job == nil {
		s.finishReserved(active)
	}
	if stop != nil {
		_ = s.callClaimedStop(context.Background(), active, stop)
	}

	if startErr != nil {
		issue := asProblem(startErr)
		issue.ReportID = active.ReportID
		return nil, issue
	}
	status := "running"
	if cancelled {
		status = "stopping"
	}
	return map[string]any{"reportId": active.ReportID, "status": status, "accepted": true, "processExited": false}, nil
}

func (s *Service) finishReserved(active *activeTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == active {
		s.scheduleRecoveryLocked()
	}
}
func (s *Service) containsWaitingLocked(owner Owner, id string) bool {
	for _, item := range s.waiting {
		if item.Owner == owner && item.Input.ReportID == id {
			return true
		}
	}
	return false
}

func (s *Service) checkAdmissionLocked() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return problem(503, "shutdown", "检测服务正在结束旧任务，请稍后重新打开；本次未创建报告或发送请求")
	}
	return nil
}

func (s *Service) retryBlockedLocked(ctx context.Context, entry *blockedReport) error {
	s.mu.Lock()
	if s.blocked[reportKey{entry.Owner, entry.ID}] != entry {
		s.mu.Unlock()
		return nil
	}
	if entry.BatchUnstarted && (s.active != nil && s.active.Owner == entry.Owner && s.active.BatchID == entry.BatchID || s.containsBatchWaitingLocked(entry.Owner, entry.BatchID)) {
		s.mu.Unlock()
		return problem(503, "recovery", "批次已登记其他任务，无法确认整批未开始，请核查原批次")
	}
	mayFinalize := s.mayFinalizeBatchLocked(entry.Owner, entry.BatchID, nil, entry)
	message := entry.Message
	s.mu.Unlock()
	var err error
	if entry.BatchUnstarted {
		var raw json.RawMessage
		raw, err = s.queryJSON(ctx, "interrupt-batch-unstarted", entry.Owner, map[string]any{"batchId": entry.BatchID, "mayFinalizeBatch": mayFinalize}, nil, true)
		if err != nil && asProblem(err).Status == 404 {
			err = nil
		} else if err == nil {
			var a batchAccepted
			if json.Unmarshal(raw, &a) != nil || a.BatchID != entry.BatchID || len(a.MemberStates) == 0 || len(a.MemberStates) != len(a.ReportIDs) || a.FirstFinishedAtMs <= 0 {
				err = problem(503, "recovery", "批次已保存但首次结束时间或全部成员状态尚未确认，请核查原批次")
			} else {
				for i, m := range a.MemberStates {
					if m.ReportID != a.ReportIDs[i] || !isTerminal(m.Status) {
						err = problem(503, "recovery", "批次成员尚未全部结束，请核查原批次")
						break
					}
				}
			}
		}
	} else if entry.CancelQueued {
		args := map[string]any{"reportId": entry.ID}
		if entry.BatchID != "" {
			args["mayFinalizeBatch"] = mayFinalize
		}
		var saved json.RawMessage
		saved, err = s.queryJSON(ctx, "cancel-queued", entry.Owner, args, nil, true)
		if err == nil {
			err = confirmTerminal(saved)
		}
	} else {
		var raw json.RawMessage
		raw, err = s.queryJSON(ctx, "read-report", entry.Owner, map[string]any{"reportId": entry.ID}, nil, true)
		if err == nil {
			var result map[string]any
			result, err = decodeReport(raw, entry.ID)
			if err == nil && retryID(result) != entry.RetryID && !isTerminal(stringField(result, "status")) {
				err = staleStop()
			}
			if err == nil && (result["status"] == "queued" || (entry.BatchID != "" && mayFinalize && isTerminal(stringField(result, "status")) && retryID(result) == entry.RetryID)) {
				args := map[string]any{"reportId": entry.ID, "error": map[string]any{"message": message, "step": "create", "operationResult": "not_started"}}
				if entry.BatchID != "" {
					args["mayFinalizeBatch"] = mayFinalize
				}
				var saved json.RawMessage
				saved, err = s.queryJSON(ctx, "finish-interrupted", entry.Owner, args, nil, true)
				if err == nil {
					err = confirmTerminal(saved)
				}
			} else if err == nil {
				err = confirmTerminal(raw)
			}
		} else if asProblem(err).Status == 404 {
			err = nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked[reportKey{entry.Owner, entry.ID}] != entry {
		return nil
	}
	if err != nil {
		// The specific cause from the latest attempted save stays visible.
		entry.RecoveryReason = "后续检测已暂停：保存报告结束或取消状态未完成：" + asProblem(err).Message + "；请检查检测运行环境后核查并重试保存，不会再次调用模型"
		return err
	}
	if entry.CancelQueued {
		for i, w := range s.waiting {
			if w.Owner == entry.Owner && w.Input.ReportID == entry.ID && w.Input.RetryID == entry.RetryID {
				s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
				break
			}
		}
	}
	delete(s.blocked, reportKey{entry.Owner, entry.ID})
	return nil
}

func (s *Service) Cancel(ctx context.Context, owner Owner, id string) (any, error) {
	return s.CancelRound(ctx, owner, id, "")
}

// Stop claim and publication share the same lock. No caller can dispatch a
// second Stop while an earlier one is pending or its confirmation window lives.
func (s *Service) claimActiveStopLocked(active *activeTask, manual bool) *Job {
	if s.active != active || active.Job == nil || active.Job.Info().Exited || active.StopCallPending {
		return nil
	}
	now := time.Now().UnixMilli()
	if manual {
		if active.CancelRequested && now < active.StopConfirmDeadlineMs {
			return nil
		}
		active.CancelRequested = true
		active.StopConfirmDeadlineMs = now + 15000
	}
	if !active.CancelRequested {
		return nil
	}
	active.StopCallPending = true
	return active.Job
}
func (s *Service) callClaimedStop(ctx context.Context, active *activeTask, job *Job) error {
	control, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := job.Stop(control)
	exited := job.Info().Exited
	s.mu.Lock()
	if s.active == active && active.Job == job {
		active.StopCallPending = false
		if activeExited(active) {
			s.scheduleRecoveryLocked()
		}
		if err != nil {
			if exited {
				active.Diagnostic = "检测程序已确认退出，但资源清理未完成：" + err.Error() + "；请核查检测运行环境"
			} else {
				active.Diagnostic = "停止请求未完成：" + err.Error() + "；检测可能仍在执行，请核查运行环境"
			}
		}
	}
	s.mu.Unlock()
	if err != nil {
		if exited {
			return problem(503, "cancel", "检测程序已确认退出，但原进程资源清理未完成："+err.Error()+"；请核查检测运行环境，再读取原报告")
		}
		return problem(503, "cancel", "停止检测的请求未完成："+err.Error()+"；请核查运行状态，不能确认已经停止")
	}
	return nil
}

// Claim under the same memory lock used for active publication. The
// returned action performs the already-authorized control call only after
// the caller has released admission, so recovery can proceed even if Stop
// does not return.
func (s *Service) claimKnownActiveCancel(owner Owner, id, expectedRetryID string) (func(context.Context) (map[string]any, error), bool, error) {
	s.mu.Lock()
	a := s.active
	if a == nil || a.Owner != owner || a.ReportID != id {
		s.mu.Unlock()
		return nil, false, nil
	}
	if a.Input.RetryID != expectedRetryID {
		s.mu.Unlock()
		return nil, true, staleStop()
	}
	var stop *Job
	var cancel context.CancelFunc
	if !activeExited(a) {
		if a.Job == nil {
			if !a.CancelRequested {
				a.CancelRequested = true
				a.StopConfirmDeadlineMs = time.Now().UnixMilli() + 15000
				cancel = a.CancelPrepare
			}
		} else {
			stop = s.claimActiveStopLocked(a, true)
		}
	}
	result := map[string]any{"reportId": id, "status": "stopping", "accepted": true, "retry": map[string]any{"id": expectedRetryID}}
	s.mu.Unlock()
	return func(ctx context.Context) (map[string]any, error) {
		if cancel != nil {
			cancel()
		}
		if stop != nil {
			if err := s.callClaimedStop(ctx, a, stop); err != nil {
				return nil, err
			}
		}
		s.decorateReport(owner, id, result)
		return result, nil
	}, true, nil
}
func (s *Service) CancelRound(ctx context.Context, owner Owner, id, expectedRetryID string) (any, error) {
	ctx, cancel := admissionContext(ctx)
	defer cancel()
	if !reportIDPattern.MatchString(id) {
		return nil, problem(422, "cancel", "报告编号无效，请重新读取原报告后停止；没有停止任何检测")
	}
	if action, known, err := s.claimKnownActiveCancel(owner, id, expectedRetryID); known {
		if err != nil {
			return nil, err
		}
		return action(ctx)
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, s.withQueryStatus(owner, err)
	}
	if err := s.lockAdmission(ctx); err != nil {
		return nil, err
	}
	if action, known, err := s.claimKnownActiveCancel(owner, id, expectedRetryID); known {
		s.admission.Unlock()
		if err != nil {
			return nil, err
		}
		return action(ctx)
	}
	defer s.admission.Unlock()
	if status := s.queryStatus(owner); status.State == "retained" {
		p := status.problem()
		p.Status = 503
		return nil, p
	}
	s.mu.Lock()
	var waiting *waitingTask
	for _, w := range s.waiting {
		if w.Owner == owner && w.Input.ReportID == id {
			waiting = w
			break
		}
	}
	blocked := s.blocked[reportKey{owner, id}]
	if blocked != nil {
		if blocked.RetryID != expectedRetryID {
			s.mu.Unlock()
			return nil, staleStop()
		}
		s.mu.Unlock()
		result := map[string]any{"reportId": id, "status": "queued", "accepted": true, "retry": map[string]any{"id": expectedRetryID}}
		s.decorateReport(owner, id, result)
		return result, nil
	}
	if waiting != nil {
		if waiting.Input.RetryID != expectedRetryID {
			s.mu.Unlock()
			return nil, staleStop()
		}
		s.registerBlockedLocked(owner, id, waiting.BatchID, expectedRetryID, "正在保存这一份等待任务的取消状态，其他检测继续", true)
		s.mu.Unlock()
		result := map[string]any{"reportId": id, "status": "queued", "accepted": true, "retry": map[string]any{"id": expectedRetryID}}
		s.decorateReport(owner, id, result)
		return result, nil
	}
	s.mu.Unlock()
	raw, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, true)
	if err != nil {
		return nil, err
	}
	report, err := decodeReport(raw, id)
	if err != nil {
		return nil, err
	}
	if retryID(report) != expectedRetryID {
		return nil, staleStop()
	}
	if report["status"] == "queued" {
		return nil, problem(409, "cancel", "这份报告未确认进入等待队列，请先核查原报告的受理结果；不会停止其他任务")
	}
	s.decorateReport(owner, id, report)
	return report, nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	if !s.cfg.ChanshuiEnabled {
		return nil
	}
	s.mu.Lock()
	s.closing = true
	if s.recoveryCancel != nil {
		s.recoveryCancel()
	}
	active := s.active
	var cancel context.CancelFunc
	var prepared <-chan struct{}
	if active != nil {
		cancel = active.CancelPrepare
		if active.Preparing {
			prepared = active.PreparationDone
		}
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if prepared != nil {
		select {
		case <-prepared:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := s.lockAdmission(ctx); err != nil {
		return err
	}
	defer s.admission.Unlock()
	if err := s.runner.Shutdown(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	active = s.active
	s.mu.Unlock()
	if active != nil && active.Job != nil {
		select {
		case <-active.Job.Done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if active != nil && active.Job != nil && !active.Job.Info().Exited {
		return problem(503, "exit", "仍无法确认检测程序退出，请检查systemd运行状态")
	}
	if _, err := s.queryJSON(ctx, "recover", Owner{}, map[string]any{}, nil, true); err != nil {
		return err
	}
	s.mu.Lock()
	s.active = nil
	s.waiting = nil
	s.blocked = nil
	s.mu.Unlock()
	return nil
}

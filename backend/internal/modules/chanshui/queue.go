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
	AccountName                         string
	MemoryLimitMiB, TotalTimeoutSeconds int
}
type blockedReport struct {
	Owner        Owner
	ID, Message  string
	CancelQueued bool
}
type workspaceTargetReader interface {
	ResolveAuditContextInWorkspace(context.Context, string, string, string, string, bool) (connection_health.AuditContext, error)
}

func copyStartInput(input StartInput) StartInput {
	input.SelectedSectionIDs = append([]string(nil), input.SelectedSectionIDs...)
	input.SelectedQuestionIDs = append([]string(nil), input.SelectedQuestionIDs...)
	return input
}

func (s *Service) Start(ctx context.Context, owner Owner, input StartInput) (any, error) {
	if !reportIDPattern.MatchString(input.ReportID) || strings.TrimSpace(input.TargetID) == "" || len(input.Model) < 1 || len(input.Model) > 256 {
		return nil, problem(422, "input", "报告编号、账号或模型名称无效，请重新打开账号并选择模型后重试")
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	input = copyStartInput(input)
	s.admission.Lock()
	if err := s.checkAdmissionLocked(); err != nil {
		s.admission.Unlock()
		return nil, err
	}
	previous, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, true)
	if err == nil {
		result, e := decodeReport(previous, input.ReportID)
		if e != nil {
			s.admission.Unlock()
			return nil, e
		}
		key := reportKey{owner, input.ReportID}
		s.mu.Lock()
		active := s.active
		needsRecovery := active != nil && !active.Preparing && (active.Job == nil || active.Job.Info().Exited)
		known := s.containsWaitingLocked(owner, input.ReportID) || active != nil && active.Owner == owner && active.ReportID == input.ReportID
		blocked := s.blocked[key]
		s.mu.Unlock()
		if blocked != nil {
			if err = s.retryBlockedLocked(ctx, blocked); err != nil {
				s.admission.Unlock()
				return nil, err
			}
			previous, err = s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, true)
			if err == nil {
				result, err = decodeReport(previous, input.ReportID)
			}
		} else if result["status"] == "queued" && !known {
			err = s.interruptOrphanLocked(ctx, owner, input.ReportID, "报告已保存，但未能确认加入等待队列；本报告没有自动开始，请查看结果后手动新建")
			if err == nil {
				previous, err = s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, true)
				if err == nil {
					result, err = decodeReport(previous, input.ReportID)
				}
			}
		} else if needsRecovery && active.Owner == owner && active.ReportID == input.ReportID {
			err = s.recoverExited(ctx, active)
			if err == nil {
				previous, err = s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": input.ReportID, "request": input}, nil, true)
				if err == nil {
					result, err = decodeReport(previous, input.ReportID)
				}
			}
		}
		if err == nil {
			s.decorateReport(owner, input.ReportID, result)
			result["accepted"] = true
		}
		next := s.reserveNextLocked()
		s.admission.Unlock()
		if next != nil {
			go s.startReserved(next)
		}
		return result, err
	}
	if asProblem(err).Status != 404 {
		s.admission.Unlock()
		return nil, err
	}
	s.mu.Lock()
	active := s.active
	needsRecovery := active != nil && !active.Preparing && (active.Job == nil || active.Job.Info().Exited)
	s.mu.Unlock()
	if needsRecovery {
		if err = s.recoverExited(ctx, active); err != nil {
			s.admission.Unlock()
			return nil, err
		}
	}
	s.mu.Lock()
	full, blocked := len(s.waiting) >= maxWaitingReports, len(s.blocked) > 0
	s.mu.Unlock()
	if blocked {
		s.admission.Unlock()
		return nil, problem(503, "recovery", "有报告状态尚未保存，等待队列已暂停；请在原报告点击停止检测重试保存，本次未创建报告或发送请求")
	}
	if full {
		s.admission.Unlock()
		return nil, problem(429, "queue", "已有128份报告等待执行，请等待部分任务结束后再开始；本次未创建报告，也未发送检测请求")
	}
	audit, err := s.targets.ResolveAuditContext(ctx, owner.UserID, input.TargetID, input.GroupID, true)
	if err != nil {
		s.admission.Unlock()
		return nil, problem(409, "account", err.Error())
	}
	if err = validateTaskContext(owner, input, audit); err != nil {
		s.admission.Unlock()
		return nil, err
	}
	if _, err = checkedCredential(audit); err != nil {
		s.admission.Unlock()
		return nil, err
	}
	created, err := s.queryJSON(ctx, "create-report", owner, map[string]any{
		"reportId": input.ReportID, "targetId": input.TargetID, "groupId": input.GroupID, "model": input.Model, "selectedSectionIds": input.SelectedSectionIDs, "selectedQuestionIds": input.SelectedQuestionIDs, "expectedContext": input.ExpectedContext, "request": input,
		"target": map[string]any{"accountId": audit.AccountID, "accountName": audit.AccountName, "groupName": audit.GroupName, "baseURL": audit.BaseURL, "protocol": audit.Protocol},
	}, nil, false)
	if err != nil {
		issue := asProblem(err)
		issue.ReportID = input.ReportID
		issue.OperationResult = "not_started"
		_ = s.interruptOrphanLocked(context.WithoutCancel(ctx), owner, input.ReportID, "创建报告的返回结果中断，未能确认已加入等待队列；本报告不会自动发出检测请求，请按报告编号查看后手动新建")
		s.admission.Unlock()
		return nil, issue
	}
	var report struct {
		MemoryLimitMiB      int    `json:"memoryLimitMiB"`
		TotalTimeoutSeconds int    `json:"totalTimeoutSeconds"`
		Status              string `json:"status"`
	}
	if json.Unmarshal(created, &report) != nil || report.MemoryLimitMiB < 128 || report.MemoryLimitMiB > 384 || report.TotalTimeoutSeconds < 60 || report.TotalTimeoutSeconds > maxTaskSeconds || report.Status != "queued" {
		issue := problem(503, "storage", "已创建报告，但运行设置或等待状态无法确认；未开始检测，请按原编号查看，不要重复新建")
		issue.ReportID = input.ReportID
		_ = s.interruptOrphanLocked(context.WithoutCancel(ctx), owner, input.ReportID, issue.Message)
		s.admission.Unlock()
		return nil, issue
	}
	entry := &waitingTask{Owner: owner, Input: input, AccountName: audit.AccountName, MemoryLimitMiB: report.MemoryLimitMiB, TotalTimeoutSeconds: report.TotalTimeoutSeconds}
	s.mu.Lock()
	s.waiting = append(s.waiting, entry)
	s.mu.Unlock()
	next := s.reserveNextLocked()
	result := map[string]any{"reportId": input.ReportID, "status": "queued", "accepted": true}
	s.decorateReport(owner, input.ReportID, result)
	s.admission.Unlock()
	if next != nil {
		if next.ReportID == input.ReportID && next.Owner == owner {
			return s.startReserved(next)
		}
		go s.startReserved(next)
	}
	return result, nil
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
	active := &activeTask{Owner: entry.Owner, ReportID: entry.Input.ReportID, AccountName: entry.AccountName, Input: entry.Input, Preparing: true, CancelPrepare: cancel, PreparationDone: make(chan struct{}), OperationResult: "not_started"}
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
			message = "排队报告开始前核查失败：" + err.Error() + "；本报告未发送检测请求，请处理后手动新建"
		}
		if cancelled {
			message = "已取消准备，本报告没有发送检测请求"
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
	s.mu.Unlock()
	if job != nil {
		if cancelled {
			_ = job.Stop(context.Background())
		}
		go func() { <-job.Done; s.finishReserved(active) }()
	} else {
		s.finishReserved(active)
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
	s.admission.Lock()
	_ = s.recoverExited(context.Background(), active)
	next := s.reserveNextLocked()
	s.admission.Unlock()
	if next != nil {
		go s.startReserved(next)
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

func (s *Service) setBlocked(owner Owner, id, message string, cancelQueued bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked == nil {
		s.blocked = map[reportKey]*blockedReport{}
	}
	s.blocked[reportKey{owner, id}] = &blockedReport{Owner: owner, ID: id, Message: message, CancelQueued: cancelQueued}
}

func (s *Service) interruptOrphanLocked(ctx context.Context, owner Owner, id, message string) error {
	raw, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, true)
	if err != nil && asProblem(err).Status == 404 {
		s.clearBlocked(owner, id)
		return nil
	}
	if err == nil {
		var result map[string]any
		result, err = decodeReport(raw, id)
		if err == nil && result["status"] == "queued" {
			var saved json.RawMessage
			saved, err = s.queryJSON(ctx, "finish-interrupted", owner, map[string]any{"reportId": id, "error": map[string]any{"message": message, "step": "create", "operationResult": "not_started"}}, nil, true)
			if err == nil {
				err = confirmTerminal(saved)
			}
		} else if err == nil {
			err = confirmTerminal(raw)
		}
	}
	if err != nil {
		s.setBlocked(owner, id, message+"；报告状态无法确认或保存："+asProblem(err).Message+"；等待队列已暂停，请在原报告点击停止检测重试保存", false)
		return err
	}
	s.clearBlocked(owner, id)
	return nil
}

func (s *Service) clearBlocked(owner Owner, id string) {
	s.mu.Lock()
	delete(s.blocked, reportKey{owner, id})
	s.mu.Unlock()
}

func (s *Service) retryBlockedLocked(ctx context.Context, entry *blockedReport) error {
	if entry.CancelQueued {
		_, err := s.cancelWaitingLocked(ctx, entry.Owner, entry.ID)
		return err
	}
	return s.interruptOrphanLocked(ctx, entry.Owner, entry.ID, entry.Message)
}

func (s *Service) cancelWaitingLocked(ctx context.Context, owner Owner, id string) (any, error) {
	raw, err := s.queryJSON(ctx, "cancel-queued", owner, map[string]any{"reportId": id}, nil, true)
	if err != nil {
		s.setBlocked(owner, id, "取消等待的结果尚未保存："+asProblem(err).Message+"；等待项仍保留、队列已暂停，请再次点击取消等待重试保存", true)
		return nil, err
	}
	result, err := decodeReport(raw, id)
	if err != nil || result["status"] == "queued" || result["status"] == "running" {
		if err == nil {
			err = problem(503, "cancel", "取消等待后状态尚未确认，等待项仍保留；请再次点击取消等待")
		}
		s.setBlocked(owner, id, asProblem(err).Message, true)
		return nil, err
	}
	s.mu.Lock()
	for i, entry := range s.waiting {
		if entry.Owner == owner && entry.Input.ReportID == id {
			s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
			break
		}
	}
	delete(s.blocked, reportKey{owner, id})
	s.mu.Unlock()
	result["processExited"] = true
	return result, nil
}

func (s *Service) Cancel(ctx context.Context, owner Owner, id string) (any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	// A known running owner can stop immediately; a busy read helper is irrelevant.
	s.mu.Lock()
	active := s.active
	if active != nil && active.Owner == owner && active.ReportID == id {
		active.CancelRequested = true
		cancel, job, preparing := active.CancelPrepare, active.Job, active.Preparing
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if preparing {
			return map[string]any{"reportId": id, "status": "stopping", "processExited": false}, nil
		}
		if job != nil && !job.Info().Exited {
			if err := job.Stop(ctx); err != nil {
				return nil, problem(503, "cancel", "停止检测的请求未完成："+err.Error()+"；请核查运行状态后重试，不能确认已经停止")
			}
			return map[string]any{"reportId": id, "status": "stopping", "processExited": false}, nil
		}
	} else {
		s.mu.Unlock()
	}
	s.admission.Lock()
	s.mu.Lock()
	active = s.active
	stillPreparing := active != nil && (active.Preparing || active.Job != nil && !active.Job.Info().Exited)
	waiting := s.containsWaitingLocked(owner, id)
	blocked := s.blocked[reportKey{owner, id}]
	s.mu.Unlock()
	var result any
	var err error
	if active != nil && active.Owner == owner && active.ReportID == id {
		if stillPreparing {
			s.admission.Unlock()
			return s.Cancel(ctx, owner, id)
		}
		err = s.recoverExited(ctx, active)
	} else if waiting {
		result, err = s.cancelWaitingLocked(ctx, owner, id)
	} else if blocked != nil {
		err = s.retryBlockedLocked(ctx, blocked)
	}
	if err == nil && result == nil {
		var raw json.RawMessage
		raw, err = s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, true)
		if err == nil {
			var report map[string]any
			report, err = decodeReport(raw, id)
			if err == nil && report["status"] == "queued" {
				result, err = s.cancelWaitingLocked(ctx, owner, id)
			} else if err == nil {
				s.decorateReport(owner, id, report)
				result = report
			}
		}
	}
	next := s.reserveNextLocked()
	s.admission.Unlock()
	if next != nil {
		go s.startReserved(next)
	}
	return result, err
}

func (s *Service) decorateReport(owner Owner, id string, result map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result["processExited"] = true
	result["queuedCount"] = len(s.waiting)
	for i, item := range s.waiting {
		if item.Owner == owner && item.Input.ReportID == id {
			position := i
			if s.active != nil {
				position++
			}
			result["queuePosition"] = position
		}
	}
	if blocked := s.blocked[reportKey{owner, id}]; blocked != nil {
		result["diagnostic"] = blocked.Message
	}
	if active := s.active; active != nil && active.Owner == owner && active.ReportID == id {
		result["processExited"] = active.Job != nil && active.Job.Info().Exited
		if active.CancelRequested {
			result["stopRequested"] = true
		}
		if active.Diagnostic != "" {
			result["diagnostic"] = active.Diagnostic
		}
		if active.Job != nil {
			info := active.Job.Info()
			result["memoryPeakBytes"] = info.MemoryPeak
			result["taskMemoryMaxBytes"] = info.TaskMemoryMaxBytes
			result["sliceMemoryMaxBytes"] = info.SliceMemoryMaxBytes
			result["swapMaxBytes"] = info.SwapMaxBytes
		}
	}
}

func (s *Service) Shutdown(ctx context.Context) error {
	if !s.cfg.ChanshuiEnabled {
		return nil
	}
	s.mu.Lock()
	s.closing = true
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
	s.admission.Lock()
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
	if active != nil {
		if err := s.recoverExited(ctx, active); err != nil {
			return err
		}
	}
	if _, err := s.queryJSON(ctx, "recover", Owner{}, map[string]any{}, nil, true); err != nil {
		return err
	}
	s.mu.Lock()
	s.waiting = nil
	s.blocked = nil
	s.mu.Unlock()
	return nil
}

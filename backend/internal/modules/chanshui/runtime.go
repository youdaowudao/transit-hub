package chanshui

import (
	"reflect"
	"time"
)

const exitUnknownReason = "无法确认检测程序已退出，已保留位置并暂停后续检测。请检查本实例systemd与原进程后按审核重新启动本调度站后端；已保存回答保留，原等待任务不自动重跑"

type memberSnapshot struct {
	active                                                   *activeTask
	job                                                      *Job
	waiting                                                  *waitingTask
	blocked                                                  *blockedReport
	batchID, retryID, phase, reason, account, model          string
	preparing, stopRequested, stopPending, exited, attempted bool
	queuePosition                                            int
	stopDeadline, executionDeadline, recoveryDeadline        int64
	info                                                     ExitInfo
}
type runtimeSnapshot struct {
	members          map[string]memberSnapshot
	active           *memberSnapshot
	activeID         string
	otherActive      bool
	queueCount       int
	queueBlocked     bool
	recoveryRunning  bool
	recoveryDeadline int64
	reason           string
	now              int64
	query            QueryState
	enabled          bool
	recoverable      bool
}

func executionDeadline(a *activeTask) int64 {
	if a.Job != nil {
		return unixMilli(a.Job.ExitConfirmDeadline)
	}
	if a.prepareContext != nil {
		d, ok := a.prepareContext.Deadline()
		if ok {
			return d.Add(10 * time.Second).UnixMilli()
		}
	}
	return 0
}

type observedActive struct {
	info         ExitInfo
	exited, done bool
}

func observeActive(a *activeTask) observedActive {
	var o observedActive
	if a.Job != nil {
		o.info = a.Job.Info()
	}
	o.exited = !a.Preparing && (a.Job == nil || o.info.Exited)
	o.done = o.exited
	if !o.done && !a.Preparing && a.Job != nil {
		select {
		case <-a.Job.Done:
			o.done = true
		default:
		}
	}
	return o
}
func activePhase(a *activeTask, now int64, recoveryRunning bool, recoveryDeadline int64) string {
	return phaseFromObserved(a, observeActive(a), now, recoveryRunning, recoveryDeadline)
}
func phaseFromObserved(a *activeTask, o observedActive, now int64, recoveryRunning bool, recoveryDeadline int64) string {
	if o.exited {
		if a.RecoveryAttempted && (!recoveryRunning || a.recoveryDeadlineMs != recoveryDeadline || now >= recoveryDeadline) {
			return "paused"
		}
		return "saving_end"
	}
	if o.done || (a.CancelRequested && a.StopConfirmDeadlineMs > 0 && now >= a.StopConfirmDeadlineMs) || (executionDeadline(a) > 0 && now >= executionDeadline(a)) {
		return "unknown"
	}
	if a.CancelRequested {
		return "stopping"
	}
	if a.Preparing {
		return "preparing"
	}
	return "running"
}
func (s *Service) captureRuntime(owner Owner) runtimeSnapshot {
	query := s.queryStatus(owner)
	s.mu.Lock()
	defer s.mu.Unlock()
	v := runtimeSnapshot{members: map[string]memberSnapshot{}, queueCount: len(s.waiting), queueBlocked: len(s.blocked) > 0, recoveryRunning: s.recoveryRunning, recoveryDeadline: unixMilli(s.recoveryDeadline), now: time.Now().UnixMilli(), query: query, enabled: s.cfg.ChanshuiEnabled}
	for i, w := range s.waiting {
		if w.Owner != owner {
			continue
		}
		pos := i
		if s.active != nil {
			pos++
		}
		v.members[w.Input.ReportID] = memberSnapshot{waiting: w, batchID: w.BatchID, retryID: w.Input.RetryID, phase: "queued", exited: true, queuePosition: pos, account: w.AccountName, model: w.Input.Model}
	}
	for _, b := range s.blocked {
		if b.Owner != owner {
			continue
		}
		v.reason = b.Message
		if b.RecoveryReason != "" {
			v.reason = b.RecoveryReason
		}
		m := v.members[b.ID]
		m.blocked = b
		m.batchID = b.BatchID
		m.retryID = b.RetryID
		m.attempted = b.RecoveryAttempted
		m.reason = v.reason
		m.phase = "unknown"
		m.exited = true
		if b.CancelQueued {
			m.phase = "saving_cancel"
		} else {
			m.phase = "saving_end"
		}
		if query.State == "retained" && !b.CancelQueued {
			m.phase = "unknown"
			m.reason = query.Reason
		}
		if b.RecoveryAttempted && (!v.recoveryRunning || b.recoveryDeadlineMs != v.recoveryDeadline || v.now >= v.recoveryDeadline) {
			m.phase = "paused"
		}
		if v.recoveryRunning && (!b.RecoveryAttempted || b.recoveryDeadlineMs == v.recoveryDeadline) {
			m.recoveryDeadline = v.recoveryDeadline
		}
		if m.phase == "paused" {
			v.recoverable = true
		}
		v.members[b.ID] = m
	}
	if v.queueBlocked && v.reason == "" {
		v.reason = "有其他检测的结束状态尚未保存，后续检测已暂停"
	}
	if a := s.active; a != nil {
		observed := observeActive(a)
		phase := phaseFromObserved(a, observed, v.now, v.recoveryRunning, v.recoveryDeadline)
		if phase == "paused" || phase == "unknown" {
			v.queueBlocked = true
		}
		if a.Owner == owner {
			m := memberSnapshot{active: a, job: a.Job, batchID: a.BatchID, retryID: a.Input.RetryID, phase: phase, reason: a.Diagnostic, account: a.AccountName, model: a.Input.Model, preparing: a.Preparing, stopRequested: a.CancelRequested, stopPending: a.StopCallPending, exited: observed.exited, attempted: a.RecoveryAttempted, stopDeadline: a.StopConfirmDeadlineMs, executionDeadline: executionDeadline(a)}
			m.info = observed.info
			if m.phase == "running" && a.Job != nil && m.executionDeadline > 0 && v.now >= m.executionDeadline-20000 {
				m.reason = "检测程序尚未确认退出，正在核查运行状态；本轮执行时间请以原报告记录为准"
			}
			if m.phase == "unknown" {
				m.reason = exitUnknownReason
				if a.CancelRequested && !observed.done {
					m.reason = "停止结果尚未确认，检测可能仍在继续。请检查运行环境后核查原报告；本次不会自动重复停止。"
					if a.StopCallPending {
						m.reason += "上次停止调用尚未返回，不能再次发送停止。"
					}
				} else if a.Preparing {
					m.reason = "检测准备超过确认期限，尚不能确认启动程序退出。" + exitUnknownReason
				}
				if a.Diagnostic != "" && a.Diagnostic != a.RecoveryReason {
					m.reason += " 原因：" + a.Diagnostic
				}
			}
			if m.phase == "saving_end" && v.recoveryRunning {
				m.recoveryDeadline = v.recoveryDeadline
			}
			if m.phase == "saving_end" && m.reason == "" {
				m.reason = "检测程序已停止，正在保存结束状态；只重试保存，不会再次发送检测题目"
			}
			if m.phase == "paused" && m.reason == "" {
				m.reason = "结束状态保存未确认，后续检测已暂停；请核查并重试保存"
			}
			if a.RecoveryReason != "" && m.phase != "unknown" {
				m.reason = a.RecoveryReason
			}
			if m.phase == "paused" && m.exited {
				v.recoverable = true
			}
			if v.reason == "" && (m.phase == "paused" || m.phase == "unknown") {
				v.reason = m.reason
			}
			v.members[a.ReportID] = m
			v.active = &m
			v.activeID = a.ReportID
		} else {
			v.otherActive = true
			if v.reason == "" {
				if phase == "paused" || phase == "unknown" {
					v.reason = "有其他检测的结束状态尚未确认，后续检测已暂停"
				} else {
					v.reason = "当前有其他检测正在执行"
				}
			}
		}
	}
	if query.State != "" {
		if v.reason == "" {
			v.reason = query.Reason
		}
		v.recoverable = false
	}
	if query.State == "retained" {
		v.queueBlocked = true
		v.recoverable = false
		v.reason = query.Reason
	}
	return v
}
func (s *Service) decorateReadReport(owner Owner, id string, result map[string]any, before runtimeSnapshot) {
	after := s.captureRuntime(owner)
	after.decorate(id, result)
	result["runtime"] = compactBatchRuntime(after, owner)
	batchID := stringField(result, "batchId")
	beforeBatch, _ := before.batchBlock(batchID)
	afterBatch, _ := after.batchBlock(batchID)
	// A committed first-finish value whose recovery response is still
	// unresolved is not yet a confirmed first finish for this view.
	if before.batchFirstFinishUnconfirmed(batchID) || after.batchFirstFinishUnconfirmed(batchID) {
		result["batchFirstFinishedAtMs"] = int64(0)
	}
	if !sameMember(before.members[id], after.members[id]) || !sameMember(beforeBatch, afterBatch) {
		result["executionPhase"] = "unknown"
		result["finishConfirmed"] = false
		result["recoveryReason"] = "读取期间检测状态已变化，请重新核查原报告"
	}
}
func (v runtimeSnapshot) batchBlock(batchID string) (memberSnapshot, bool) {
	if batchID == "" {
		return memberSnapshot{}, false
	}
	m, ok := v.members["batch:"+batchID]
	return m, ok
}

// A stored first-finish value is not confirmed until the matching first-run
// reservations and save intents have settled. The snapshot is already owner
// scoped; a later retry (which preserves the historical anchor) is excluded.
func (v runtimeSnapshot) batchFirstFinishUnconfirmed(batchID string) bool {
	if batchID == "" {
		return false
	}
	for _, m := range v.members {
		if m.batchID == batchID && m.retryID == "" && (m.active != nil || m.blocked != nil || m.waiting != nil) {
			return true
		}
	}
	return false
}
func (s *Service) decorateReport(owner Owner, id string, result map[string]any) {
	s.captureRuntime(owner).decorate(id, result)
}
func sameMember(a, b memberSnapshot) bool {
	// Pointers, round, lifecycle and the related deadline must agree. Resource
	// peaks and diagnostic text are observations, not identity changes.
	a.info = ExitInfo{}
	b.info = ExitInfo{}
	a.reason = ""
	b.reason = ""
	return reflect.DeepEqual(a, b)
}
func isTerminal(status string) bool {
	return status == "done" || status == "partial" || status == "cancelled" || status == "interrupted"
}
func (v runtimeSnapshot) decorate(id string, result map[string]any) {
	m, known := v.members[id]
	status := stringField(result, "status")
	phase, confirmed := "unknown", false
	batchID := stringField(result, "batchId")
	if v.batchFirstFinishUnconfirmed(batchID) {
		result["batchFirstFinishedAtMs"] = int64(0)
	}
	if block, blocked := v.batchBlock(batchID); blocked {
		m, known = block, true
		phase = block.phase
	} else if known {
		phase = m.phase
		if m.retryID != retryID(result) {
			phase = "unknown"
		}
		if m.waiting != nil && m.blocked == nil && status != "queued" {
			phase = "unknown"
		}
	} else if isTerminal(status) {
		phase = "ended"
		confirmed = true
	}
	result["executionPhase"] = phase
	result["finishConfirmed"] = confirmed
	// Absence of a reservation is normal for a queued or genuinely terminal
	// row, but a stranded SQL running row cannot prove that its process exited.
	result["processExited"] = (known && m.exited) || (!known && (status == "queued" || isTerminal(status)))
	result["queuedCount"] = v.queueCount
	result["serverNow"] = v.now
	if m.waiting != nil {
		result["queuePosition"] = m.queuePosition
	}
	if m.reason != "" {
		result["recoveryReason"] = m.reason
		result["diagnostic"] = m.reason
	}
	if m.stopRequested {
		result["stopRequested"] = true
	}
	result["stopConfirmDeadlineMs"] = m.stopDeadline
	result["stopCallPending"] = m.stopPending
	result["executionConfirmDeadlineMs"] = m.executionDeadline
	result["recoveryDeadlineMs"] = m.recoveryDeadline
	if known && m.active != nil {
		result["memoryPeakBytes"] = m.info.MemoryPeak
		result["taskMemoryMaxBytes"] = m.info.TaskMemoryMaxBytes
		result["sliceMemoryMaxBytes"] = m.info.SliceMemoryMaxBytes
		result["swapMaxBytes"] = m.info.SwapMaxBytes
	}
	if phase == "running" {
		summary, _ := result["summary"].(map[string]any)
		verdict, _ := summary["verdict"].(map[string]any)
		timing, _ := verdict["timing"].(map[string]any)
		accepted := numberField(result, "createdAtMs")
		if retryID(result) != "" {
			accepted = numberField(retryMeta(result), "acceptedAtMs")
		}
		if currentBudgetExpired(timing, accepted, v.now, numberField(result, "totalTimeoutSeconds")) {
			result["recoveryReason"] = currentBudgetExpiredReason
			result["diagnostic"] = currentBudgetExpiredReason
		}
	}
	if phase == "unknown" && stringField(result, "recoveryReason") == "" {
		result["recoveryReason"] = "报告的当前执行状态尚未确认，请核查原报告和运行环境；不会自动重发检测题目"
	}
}

const currentBudgetExpiredReason = "本轮时限已到，正在确认程序结束；尚不能确认已停止。超过确认时间仍不明时，请按提示检查运行环境"

// The external Runner starts before RunOne writes its timing. Only that
// stored, current-round start can establish that an execution budget expired.
func currentBudgetExpired(timing map[string]any, accepted, now, seconds int64) bool {
	start := timingStamp(timing, "started_at")
	if accepted <= 0 || start < accepted || start > now || seconds < 60 || seconds > maxTaskSeconds || stringField(timing, "finished_at") != "" {
		return false
	}
	return now-start >= seconds*1000
}
func (v runtimeSnapshot) runtime(owner Owner) map[string]any {
	result := map[string]any{"enabled": v.enabled, "available": v.query.State != "retained", "memoryCeilingBytes": MemoryCeiling, "queuedCount": v.queueCount, "queueBlocked": v.queueBlocked, "recoveryRunning": v.recoveryRunning, "recoveryDeadlineMs": v.recoveryDeadline, "serverNow": v.now, "otherActive": v.otherActive, "recoverable": v.recoverable}
	if v.query.State != "" {
		result["queryState"] = v.query.State
		result["retryAfterMs"] = v.query.RetryAfterMs
		if v.query.DeadlineMs > 0 && v.recoveryDeadline == 0 {
			result["recoveryDeadlineMs"] = v.query.DeadlineMs
		}
	}
	if v.reason != "" {
		result["reason"] = v.reason
	}
	if v.active != nil {
		a := v.active
		status := "running"
		if a.preparing {
			status = "queued"
		}
		if a.stopRequested {
			status = "stopping"
		}
		item := map[string]any{"reportId": v.activeID, "batchId": a.batchID, "accountName": a.account, "model": a.model, "status": status, "retryId": a.retryID, "retry": map[string]any{"id": a.retryID}}
		v.decorate(v.activeID, item)
		result["active"] = item
	}
	return result
}
func stringField(m map[string]any, key string) string { v, _ := m[key].(string); return v }
func numberField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

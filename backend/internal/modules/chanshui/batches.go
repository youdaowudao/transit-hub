package chanshui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/modules/connection_health"
)

type BatchTaskInput struct {
	ReportID        string          `json:"reportId"`
	TargetID        string          `json:"targetId"`
	GroupID         string          `json:"groupId"`
	Model           string          `json:"model"`
	ExpectedContext ExpectedContext `json:"expectedContext"`
}
type BatchInput struct {
	BatchID             string           `json:"batchId"`
	SettingsRevision    int              `json:"settingsRevision"`
	SelectedSectionIDs  []string         `json:"selectedSectionIds"`
	SelectedQuestionIDs []string         `json:"selectedQuestionIds"`
	Tasks               []BatchTaskInput `json:"tasks"`
}
type batchAccepted struct {
	BatchID             string                                       `json:"batchId"`
	ReportIDs           []string                                     `json:"reportIds"`
	SettingsRevision    int                                          `json:"settingsRevision"`
	MemoryLimitMiB      int                                          `json:"memoryLimitMiB"`
	TotalTimeoutSeconds int                                          `json:"totalTimeoutSeconds"`
	FirstFinishedAtMs   int64                                        `json:"firstFinishedAtMs"`
	MemberStates        []struct{ ReportID, Status, RetryID string } `json:"memberStates"`
}
type batchTarget struct {
	TargetID string            `json:"targetId"`
	GroupID  string            `json:"groupId"`
	Target   map[string]string `json:"target"`
}

func normalizeBatchInput(input BatchInput) (BatchInput, error) {
	fail := func(msg string) (BatchInput, error) {
		return input, problem(422, "batch", msg+"；请重新确认整批清单，本次没有创建报告或发送检测请求")
	}
	if !reportIDPattern.MatchString(input.BatchID) || input.SettingsRevision < 1 {
		return fail("批次编号或设置版本无效")
	}
	if len(input.Tasks) < 1 || len(input.Tasks) > 32 {
		return fail("本批最多32份，请减少组合或分开提交")
	}
	unique := func(ids []string) []string {
		out := []string{}
		seen := map[string]bool{}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		sort.Strings(out)
		return out
	}
	input.SelectedSectionIDs = unique(input.SelectedSectionIDs)
	input.SelectedQuestionIDs = unique(input.SelectedQuestionIDs)
	if len(input.SelectedSectionIDs) == 0 && len(input.SelectedQuestionIDs) == 0 {
		return fail("请至少选择一个检测分类或题目")
	}
	input.Tasks = append([]BatchTaskInput(nil), input.Tasks...)
	seen := map[string]bool{}
	combinations := map[[2]string]string{}
	for i := range input.Tasks {
		task := &input.Tasks[i]
		task.Model = strings.TrimSpace(task.Model)
		if !reportIDPattern.MatchString(task.ReportID) || seen[task.ReportID] || task.TargetID == "" || task.GroupID == "" || task.Model == "" || len(task.Model) > 256 {
			return fail("报告编号、账号、分组或模型无效或重复")
		}
		if group, exists := combinations[[2]string{task.TargetID, task.Model}]; exists {
			if group != task.GroupID {
				return fail("相同账号和模型请选择一个分组")
			}
			return fail("相同账号和模型组合重复")
		}
		combinations[[2]string{task.TargetID, task.Model}] = task.GroupID
		seen[task.ReportID] = true
	}
	return input, nil
}
func (t BatchTaskInput) start(input BatchInput) StartInput {
	return StartInput{ReportID: t.ReportID, TargetID: t.TargetID, GroupID: t.GroupID, Model: t.Model, ExpectedContext: t.ExpectedContext, SelectedSectionIDs: input.SelectedSectionIDs, SelectedQuestionIDs: input.SelectedQuestionIDs}
}
func (s *Service) containsBatchWaitingLocked(owner Owner, batchID string) bool {
	for _, w := range s.waiting {
		if w.Owner == owner && w.BatchID == batchID {
			return true
		}
	}
	return false
}
func (s *Service) registerBatchBlockedLocked(owner Owner, batchID, message string) {
	key := reportKey{owner, "batch:" + batchID}
	if s.blocked[key] != nil {
		return
	}
	if s.blocked == nil {
		s.blocked = map[reportKey]*blockedReport{}
	}
	s.blocked[key] = &blockedReport{Owner: owner, ID: key.ID, BatchID: batchID, BatchUnstarted: true, Message: message}
	s.scheduleRecoveryLocked()
}
func batchProblem(id, msg string) *Problem {
	p := problem(503, "batch", "本批受理结果尚未确认："+msg+"。请查询原批次"+id+"，不要另建一批；已发请求是否发生以查询结果为准")
	p.OperationResult = "unknown"
	return p
}
func (s *Service) StartBatch(ctx context.Context, owner Owner, input BatchInput) (any, error) {
	ctx, cancel := admissionContext(ctx)
	defer cancel()
	input, err := normalizeBatchInput(input)
	if err != nil {
		return nil, err
	}
	if err = s.ensureInitialized(ctx); err != nil {
		return nil, s.withQueryStatus(owner, err)
	}
	if err = s.lockAdmission(ctx); err != nil {
		return nil, err
	}
	defer s.admission.Unlock()
	if err = s.checkAdmissionLocked(); err != nil {
		return nil, err
	}
	args := map[string]any{"batchId": input.BatchID, "request": input}
	prior, err := s.queryJSON(ctx, "read-batch", owner, args, nil, true)
	if err == nil {
		return s.confirmAcceptedBatch(owner, input, prior)
	}
	if asProblem(err).Status != 404 {
		return nil, err
	}
	// An earlier unconfirmed create can never be replayed until its own recovery
	// proves the query ended and the batch does not exist.
	if err = s.checkCapacityLocked(owner, len(input.Tasks)); err != nil {
		return nil, err
	}
	targets := []batchTarget{}
	audits := map[[2]string]connection_health.AuditContext{}
	for _, task := range input.Tasks {
		key := [2]string{task.TargetID, task.GroupID}
		audit, ok := audits[key]
		if !ok {
			audit, err = s.targets.ResolveAuditContext(ctx, owner.UserID, task.TargetID, task.GroupID, true)
			if err != nil {
				return nil, problem(409, "account", err.Error())
			}
			if _, err = checkedCredential(audit); err != nil {
				return nil, err
			}
			audits[key] = audit
			targets = append(targets, batchTarget{TargetID: task.TargetID, GroupID: task.GroupID, Target: map[string]string{"accountId": audit.AccountID, "accountName": audit.AccountName, "groupName": audit.GroupName, "baseURL": audit.BaseURL, "protocol": audit.Protocol}})
		}
		if err = validateTaskContext(owner, task.start(input), audit); err != nil {
			return nil, err
		}
	}
	createArgs := map[string]any{"request": input, "targets": targets}
	// Include the complete internal wire envelope in the 128KiB check, not
	// merely the browser selection (trusted server metadata also takes space).
	marshal, err := json.Marshal(Envelope{V: 1, Operation: "create-batch", Owner: owner, Args: createArgs, Deployment: Deployment{DisableBankSync: true, AllowPrivateTargets: true}})
	if err != nil || len(marshal) > 128*1024 {
		return nil, problem(422, "batch", "完整批次清单与账号资料超过128KiB，请减少组合或分开提交；本次整批未创建")
	}
	raw, err := s.queryJSON(ctx, "create-batch", owner, createArgs, nil, false)
	if err != nil {
		issue := asProblem(err)
		if issue.Status >= 500 {
			s.mu.Lock()
			s.registerBatchBlockedLocked(owner, input.BatchID, "创建批次返回中断，正在核查是否保存；不会自动开始检测")
			s.mu.Unlock()
			p := batchProblem(input.BatchID, issue.Message)
			p.QueryState = issue.QueryState
			p.RecoveryDeadlineMs = issue.RecoveryDeadlineMs
			p.RetryAfterMs = issue.RetryAfterMs
			return nil, p
		}
		return nil, err
	}
	var accepted batchAccepted
	if json.Unmarshal(raw, &accepted) != nil || !validBatchAccepted(input, accepted) {
		s.mu.Lock()
		s.registerBatchBlockedLocked(owner, input.BatchID, "已创建批次但完整受理结果无法确认；不会自动开始检测")
		s.mu.Unlock()
		return nil, batchProblem(input.BatchID, "批次受理内容无法完整核对")
	}
	if len(accepted.MemberStates) > 0 {
		return s.confirmAcceptedBatch(owner, input, raw)
	}
	s.mu.Lock()
	for _, task := range input.Tasks {
		audit := audits[[2]string{task.TargetID, task.GroupID}]
		s.waiting = append(s.waiting, &waitingTask{Owner: owner, BatchID: input.BatchID, Input: task.start(input), AccountName: audit.AccountName, MemoryLimitMiB: accepted.MemoryLimitMiB, TotalTimeoutSeconds: accepted.TotalTimeoutSeconds})
	}
	s.mu.Unlock()
	next := s.reserveNextLocked()
	result := acceptedResult(accepted, "queued")
	snapshot := s.captureRuntime(owner)
	if snapshot.active != nil && snapshot.active.batchID == input.BatchID {
		result["state"] = "running"
		result["executionPhase"] = snapshot.active.phase
	} else {
		result["state"] = "queued"
	}
	if next != nil {
		go s.startReserved(next)
	}
	return result, nil
}
func validBatchAccepted(input BatchInput, a batchAccepted) bool {
	if a.BatchID != input.BatchID || len(a.ReportIDs) != len(input.Tasks) || a.SettingsRevision != input.SettingsRevision || !validLimits(a.MemoryLimitMiB, a.TotalTimeoutSeconds) {
		return false
	}
	for i, task := range input.Tasks {
		if a.ReportIDs[i] != task.ReportID {
			return false
		}
	}
	return true
}
func acceptedResult(a batchAccepted, status string) map[string]any {
	return map[string]any{"batchId": a.BatchID, "reportIds": a.ReportIDs, "settingsRevision": a.SettingsRevision, "memoryLimitMiB": a.MemoryLimitMiB, "totalTimeoutSeconds": a.TotalTimeoutSeconds, "accepted": true, "status": status}
}
func (s *Service) confirmAcceptedBatch(owner Owner, input BatchInput, raw json.RawMessage) (any, error) {
	var a batchAccepted
	if json.Unmarshal(raw, &a) != nil || !validBatchAccepted(input, a) || len(a.MemberStates) != len(input.Tasks) {
		return nil, batchProblem(input.BatchID, "已有批次的保存清单无法完整核对")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	registered := s.containsBatchWaitingLocked(owner, input.BatchID) || s.active != nil && s.active.Owner == owner && s.active.BatchID == input.BatchID
	blocked := s.blocked[reportKey{owner, "batch:" + input.BatchID}]
	ended := true
	for i, m := range a.MemberStates {
		if m.ReportID != a.ReportIDs[i] || !isTerminal(m.Status) {
			ended = false
			break
		}
	}
	if blocked != nil {
		message := blocked.Message
		if blocked.RecoveryReason != "" {
			message = blocked.RecoveryReason
		}
		return nil, batchProblem(input.BatchID, message)
	}
	if !registered && a.FirstFinishedAtMs <= 0 {
		s.registerBatchBlockedLocked(owner, input.BatchID, "本批已保存但未登记执行，正在中断未开始项；不会自动重新发题")
		return nil, batchProblem(input.BatchID, "已保存但未开始，可查看报告，首次结束确认后补测")
	}
	status := "queued"
	phase := ""
	relatedBlocked := false
	for _, b := range s.blocked {
		if b.Owner == owner && b.BatchID == input.BatchID {
			relatedBlocked = true
			break
		}
	}
	if s.active != nil && s.active.Owner == owner && s.active.BatchID == input.BatchID {
		phase = activePhase(s.active, time.Now().UnixMilli(), s.recoveryRunning, unixMilli(s.recoveryDeadline))
		switch phase {
		case "unknown", "paused", "saving_end":
			status = phase
		default:
			status = "running"
		}
	} else if relatedBlocked {
		if s.recoveryRunning {
			status = "saving_end"
		} else {
			status = "paused"
		}
	} else if ended && !registered {
		status = "ended"
	} else if !registered {
		status = "unknown"
	}
	result := acceptedResult(a, status)
	result["state"] = status
	if phase != "" {
		result["executionPhase"] = phase
	}
	return result, nil
}

type phaseHint struct {
	RetryID string `json:"retryId"`
	Phase   string `json:"phase"`
}
type batchCounts struct {
	Total       int `json:"total"`
	Active      int `json:"active"`
	Waiting     int `json:"waiting"`
	Ended       int `json:"ended"`
	Unconfirmed int `json:"unconfirmed"`
	Complete    int `json:"complete"`
	Partial     int `json:"partial"`
	Abnormal    int `json:"abnormal"`
	Cancelled   int `json:"cancelled"`
	Retrying    int `json:"retrying"`
}

func mapsField(m map[string]any, key string) []map[string]any {
	if items, ok := m[key].([]map[string]any); ok {
		return items
	}
	items, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		if item, ok := v.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}
func batchMemberPhase(id, status, retry string, before, after runtimeSnapshot, batchID string) (string, memberSnapshot, bool) {
	m, known := after.members[id]
	beforeBlock, _ := before.batchBlock(batchID)
	afterBlock, blocked := after.batchBlock(batchID)
	if !sameMember(before.members[id], m) || !sameMember(beforeBlock, afterBlock) {
		return "unknown", m, false
	}
	if blocked {
		return afterBlock.phase, afterBlock, false
	}
	if known {
		if m.retryID != retry || status == "missing" || m.waiting != nil && m.blocked == nil && status != "queued" {
			return "unknown", m, false
		}
		return m.phase, m, false
	}
	if isTerminal(status) {
		return "ended", m, true
	}
	return "unknown", m, false
}
func projectBatch(value map[string]any, before, after runtimeSnapshot, detail bool) {
	id := stringField(value, "batchId")
	if before.batchFirstFinishUnconfirmed(id) || after.batchFirstFinishUnconfirmed(id) {
		value["firstFinishedAtMs"] = int64(0)
	}
	counts := batchCounts{}
	members := mapsField(value, "memberStates")
	counts.Total = len(members)
	phases := map[string]string{}
	known := map[string]memberSnapshot{}
	confirmed := map[string]bool{}
	stateByID := map[string]map[string]any{}
	healthyActive, unknownActive, savingActive, paused := false, false, false, false
	for _, row := range members {
		rid, status, retry := stringField(row, "reportId"), stringField(row, "status"), stringField(row, "retryId")
		phase, m, ended := batchMemberPhase(rid, status, retry, before, after, id)
		phases[rid] = phase
		stateByID[rid] = row
		known[rid] = m
		confirmed[rid] = ended
		if retry != "" && !ended {
			counts.Retrying++
		}
		if m.active != nil && sameMember(before.members[rid], after.members[rid]) && m.retryID == retry && status != "missing" {
			counts.Active++
			healthyActive = healthyActive || phase == "preparing" || phase == "running"
			savingActive = savingActive || phase == "saving_end"
		} else if phase == "queued" && m.waiting != nil {
			counts.Waiting++
		} else if ended {
			counts.Ended++
			switch status {
			case "cancelled":
				counts.Cancelled++
			case "interrupted":
				counts.Abnormal++
			case "done":
				if row["summaryComplete"] == true {
					counts.Complete++
				} else {
					counts.Partial++
				}
			default:
				counts.Partial++
			}
		} else {
			counts.Unconfirmed++
		}
		if m.active != nil && phase == "unknown" {
			unknownActive = true
		}
		if phase == "saving_cancel" || phase == "saving_end" {
			savingActive = true
		}
		if phase == "paused" {
			paused = true
		}
		if detail {
			row["phase"] = phase
			row["finishConfirmed"] = ended
		}
	}
	state := "unknown"
	switch {
	case counts.Total > 0 && counts.Ended == counts.Total:
		state = "ended"
	case healthyActive:
		state = "running"
	case unknownActive:
		state = "unknown"
	case savingActive && after.recoveryRunning:
		state = "saving_end"
	case paused:
		state = "paused"
	case counts.Unconfirmed > 0:
		state = "unknown"
	case counts.Active > 0:
		state = "running"
	case counts.Waiting > 0:
		state = "queued"
	}
	value["state"] = state
	value["counts"] = counts
	value["serverNow"] = after.now
	value["observedAt"] = after.now
	if !detail {
		return
	}
	for _, row := range mapsField(value, "items") {
		rid := stringField(row, "reportId")
		phase := phases[rid]
		if phase == "" {
			phase = "unknown"
		}
		row["phase"] = phase
		row["finishConfirmed"] = confirmed[rid]
		m := known[rid]
		if m.waiting != nil {
			row["queuePosition"] = m.queuePosition
		}
		if m.reason != "" {
			row["recoveryReason"] = batchText(m.reason, 256)
		}
		if phase == "running" {
			state := stateByID[rid]
			timing, _ := state["timing"].(map[string]any)
			accepted := numberField(row, "createdAtMs")
			if stringField(state, "retryId") != "" {
				accepted = numberField(state, "retryAcceptedAtMs")
			}
			if currentBudgetExpired(timing, accepted, after.now, numberField(state, "totalTimeoutSeconds")) {
				row["recoveryReason"] = currentBudgetExpiredReason
			}
		}
		if m.stopDeadline > 0 {
			row["stopConfirmDeadlineMs"] = m.stopDeadline
		}
		if m.stopPending {
			row["stopCallPending"] = true
		}
		if m.executionDeadline > 0 {
			row["executionConfirmDeadlineMs"] = m.executionDeadline
		}
		if m.recoveryDeadline > 0 {
			row["recoveryDeadlineMs"] = m.recoveryDeadline
		}
	}
	value["eta"], value["etaReason"] = batchETA(id, members, counts, state, before, after)
}
func timingStamp(timing map[string]any, key string) int64 {
	value, ok := timing[key].(string)
	if !ok {
		return 0
	}
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return stamp.UnixMilli()
}
func batchText(value string, limit int) string {
	chars := []rune(value)
	if len(chars) > limit {
		return string(chars[:limit])
	}
	return value
}
func compactBatchRuntime(v runtimeSnapshot, owner Owner) map[string]any {
	runtime := v.runtime(owner)
	if name := stringField(runtime, "reason"); name != "" {
		runtime["reason"] = batchText(name, 256)
	}
	if a, ok := runtime["active"].(map[string]any); ok {
		for _, k := range []string{"diagnostic", "recoveryReason"} {
			if text := stringField(a, k); text != "" {
				a[k] = batchText(text, 256)
			}
		}
		if text := stringField(a, "accountName"); text != "" {
			a["accountName"] = batchText(text, 96)
		}
	}
	return runtime
}
func batchETA(id string, members []map[string]any, counts batchCounts, state string, before, after runtimeSnapshot) (any, string) {
	if counts.Ended == counts.Total {
		return map[string]any{"lowMs": 0, "highMs": 0}, ""
	}
	if counts.Retrying > 0 {
		return nil, "正在补测"
	}
	if state == "unknown" || state == "paused" || counts.Unconfirmed > 0 || after.queueBlocked || after.query.State == "retained" {
		return nil, "状态待核查或队列异常"
	}
	if after.otherActive || after.active != nil && after.active.batchID != id {
		return nil, "前方还有其他检测"
	}
	// The original FIFO can include other-owner members invisible in this view.
	firstPosition := math.MaxInt
	for _, m := range after.members {
		if m.batchID == id && m.waiting != nil && m.queuePosition < firstPosition {
			firstPosition = m.queuePosition
		}
	}
	allowedFirst := 0
	if after.active != nil && after.active.batchID == id {
		allowedFirst = 1
	}
	if firstPosition != math.MaxInt && firstPosition != allowedFirst {
		return nil, "前方还有其他检测"
	}
	type sample struct {
		n        int
		min, max int64
	}
	samples := map[string]sample{}
	for _, m := range members {
		timing, _ := m["timing"].(map[string]any)
		key := stringField(m, "etaKey")
		if key == "" || m["phase"] != "ended" || m["status"] != "done" || m["summaryComplete"] != true || stringField(m, "retryId") != "" || numberField(timing, "runs") != 1 || numberField(timing, "last_run_ms") <= 0 {
			continue
		}
		start, finish := timingStamp(timing, "started_at"), timingStamp(timing, "finished_at")
		if start <= 0 || finish < start || finish > after.now {
			continue
		}
		n := numberField(timing, "last_run_ms")
		v := samples[key]
		v.n++
		if v.min == 0 || n < v.min {
			v.min = n
		}
		if n > v.max {
			v.max = n
		}
		samples[key] = v
	}
	var low, high int64
	for _, m := range members {
		phase := stringField(m, "phase")
		if phase == "ended" {
			continue
		}
		key := stringField(m, "etaKey")
		sample := samples[key]
		if sample.n < 3 || key == "" {
			return nil, "样本不足"
		}
		lo, hi := sample.min/2, sample.max*2
		if sample.max > math.MaxInt64/2-70000 {
			return nil, "样本时长无法可靠比较"
		}
		if phase == "queued" {
			hi += 70000
		} else {
			if phase != "running" {
				return nil, "状态待核查"
			}
			timing, _ := m["timing"].(map[string]any)
			started := timingStamp(timing, "started_at")
			if started <= 0 || started > after.now {
				return nil, "本轮起点待确认"
			}
			elapsed := after.now - started
			lo = max(0, lo-elapsed)
			hi = max(0, hi-elapsed)
			hi = min(hi, max(0, numberField(m, "totalTimeoutSeconds")*1000-elapsed)+40000)
			lo = min(lo, hi)
		}
		if low > math.MaxInt64-lo || high > math.MaxInt64-hi {
			return nil, "样本时长无法可靠比较"
		}
		low += lo
		high += hi
	}
	return map[string]any{"lowMs": low, "highMs": high}, ""
}
func validateBatchState(value map[string]any) error {
	members := mapsField(value, "memberStates")
	if len(members) < 1 || len(members) > 32 {
		return problem(503, "batch", "批次保存的任务总数无法核对，请检查原批次及检测程序版本")
	}
	seen := map[string]bool{}
	for _, m := range members {
		id := stringField(m, "reportId")
		if !reportIDPattern.MatchString(id) || seen[id] {
			return problem(503, "batch", "批次保存的报告编号重复或无法核对，请按原批次编号检查")
		}
		seen[id] = true
	}
	return nil
}
func (s *Service) ReadBatch(ctx context.Context, owner Owner, id string, args map[string]any) (any, error) {
	if !reportIDPattern.MatchString(id) {
		return nil, problem(404, "batch", "找不到这批检测，请核对批次编号")
	}
	args["batchId"] = id
	before := s.captureRuntime(owner)
	hints := map[string]phaseHint{}
	for rid, m := range before.members {
		if m.batchID == id {
			if strings.HasPrefix(rid, "batch:") {
				hints["*"] = phaseHint{Phase: m.phase}
			} else {
				hints[rid] = phaseHint{RetryID: m.retryID, Phase: m.phase}
			}
		}
	}
	args["stateHints"] = hints
	raw, err := s.queryJSON(ctx, "read-batch", owner, args, nil, false)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || stringField(value, "batchId") != id {
		return nil, problem(503, "batch", "批次摘要无法读取或编号不一致，请核查检测程序版本后重试")
	}
	if err := validateBatchState(value); err != nil {
		return nil, err
	}
	after := s.captureRuntime(owner)
	projectBatch(value, before, after, true)
	value["runtime"] = compactBatchRuntime(after, owner)
	if err := boundBatchResponse(value); err != nil {
		return nil, err
	}
	return value, nil
}
func boundBatchResponse(value map[string]any) error {
	fail := func(reason string) error {
		return problem(503, "batch", reason+"，请按报告编号查看已保存结果；已有结果未删除")
	}
	rows, members := mapsField(value, "items"), mapsField(value, "memberStates")
	indexes := map[string]int{}
	for i, m := range members {
		compact, _ := json.Marshal(m)
		if len(compact) > 512 {
			return fail("批次中一份报告状态摘要超过512字节")
		}
		indexes[stringField(m, "reportId")] = i
	}
	// Keep the envelope, base and current page disjoint to enforce the separate
	// limits after Service adds all lifecycle metadata (including UTF-8 bytes).
	base := map[string]any{}
	for k, v := range value {
		if k != "items" && k != "runtime" {
			base[k] = v
		}
	}
	rawBase, _ := json.Marshal(base)
	header, _ := json.Marshal(map[string]any{"runtime": value["runtime"]})
	if len(rawBase) > 24*1024 {
		return fail("批次基础摘要超过24KiB")
	}
	if len(header) > 4*1024 {
		return fail("批次运行状态摘要超过4KiB")
	}
	for {
		page, _ := json.Marshal(rows)
		raw, _ := json.Marshal(value)
		if len(page) <= 36*1024 && len(raw)+1 <= 64*1024 {
			return nil
		}
		if len(rows) <= 1 {
			return fail("批次单页或单行摘要超过读取上限")
		}
		rows = rows[:len(rows)-1]
		last := rows[len(rows)-1]
		value["nextOffset"] = indexes[stringField(last, "reportId")] + 1
		value["items"] = rows
	}
}
func batchCursor(created int64, id string) string {
	raw, _ := json.Marshal(map[string]any{"n": created, "id": id})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func (s *Service) ListBatches(ctx context.Context, owner Owner, args map[string]any) (any, error) {
	before := s.captureRuntime(owner)
	raw, err := s.queryJSON(ctx, "list-batches", owner, args, nil, false)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, problem(503, "batch", "批次历史摘要无法读取，请核查检测程序版本后重试")
	}
	after := s.captureRuntime(owner)
	items := mapsField(value, "items")
	for _, batch := range items {
		if err := validateBatchState(batch); err != nil {
			return nil, err
		}
		projectBatch(batch, before, after, false)
		delete(batch, "memberStates")
	}
	value["runtime"] = compactBatchRuntime(after, owner)
	value["serverNow"] = after.now
	value["observedAt"] = after.now
	value["items"] = items
	for {
		raw, _ := json.Marshal(value)
		if len(raw)+1 <= 64*1024 {
			break
		}
		if len(items) <= 1 {
			return nil, problem(503, "batch", "批次历史摘要超过64KiB，请按批次编号查看")
		}
		items = items[:len(items)-1]
		last := items[len(items)-1]
		value["nextCursor"] = batchCursor(numberField(last, "createdAtMs"), stringField(last, "batchId"))
		value["items"] = items
	}
	return value, nil
}

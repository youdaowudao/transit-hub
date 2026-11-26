package chanshui

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

type RetryInput struct {
	RetryID         string   `json:"retryId"`
	ExpectedRetryID string   `json:"expectedRetryId"`
	ItemIDs         []string `json:"itemIds"`
}

func normalizeRetry(input RetryInput) (RetryInput, error) {
	if !reportIDPattern.MatchString(input.RetryID) || len(input.ItemIDs) == 0 || len(input.ItemIDs) > 128 {
		return input, problem(422, "retry", "请选择失败项目并重新读取报告后补测；没有发送模型请求")
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range input.ItemIDs {
		if strings.TrimSpace(id) == "" || len(id) > 200 {
			return input, problem(422, "retry", "补测题目编号无效，请重新读取报告")
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	sort.Strings(ids)
	input.ItemIDs = ids
	return input, nil
}
func retryMeta(report map[string]any) map[string]any {
	v, _ := report["retry"].(map[string]any)
	return v
}
func retryID(report map[string]any) string { v, _ := retryMeta(report)["id"].(string); return v }
func retryMatches(report map[string]any, input RetryInput) bool {
	meta := retryMeta(report)
	raw, _ := json.Marshal(meta["itemIds"])
	ids := []string{}
	_ = json.Unmarshal(raw, &ids)
	return meta["expectedRetryId"] == input.ExpectedRetryID && reflect.DeepEqual(ids, input.ItemIDs)
}
func staleStop() error {
	return problem(409, "cancel", "报告已经进入另一轮检测，请重新读取后再停止；没有停止当前这轮任务")
}
func (s *Service) Retry(ctx context.Context, owner Owner, id string, input RetryInput) (any, error) {
	ctx, cancel := admissionContext(ctx)
	defer cancel()
	if !reportIDPattern.MatchString(id) {
		return nil, problem(422, "retry", "原报告编号无效，请重新读取报告")
	}
	var err error
	input, err = normalizeRetry(input)
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
	before := s.captureRuntime(owner)
	if err = s.checkAdmissionLocked(); err != nil {
		return nil, err
	}
	raw, err := s.queryJSON(ctx, "read-report", owner, map[string]any{"reportId": id}, nil, true)
	if err != nil {
		return nil, err
	}
	report, err := decodeReport(raw, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	active := s.active
	known := s.containsWaitingLocked(owner, id) || active != nil && active.Owner == owner && active.ReportID == id
	blocked := s.blocked[reportKey{owner, id}]
	s.mu.Unlock()
	if retryID(report) == input.RetryID {
		if !retryMatches(report, input) {
			return nil, problem(409, "retry", "同一补测编号的题目已变化，请重新读取报告；没有重复检测")
		}
		if report["status"] == "queued" && !known && blocked == nil {
			s.mu.Lock()
			s.registerBlockedLocked(owner, id, stringField(report, "batchId"), input.RetryID, "本轮补测已保存，但未能确认加入等待队列；没有自动重新执行，成功回答保留，请读取后重新点击补测", false)
			s.mu.Unlock()
		}
		if blocked != nil || report["status"] == "queued" && !known {
			return nil, problem(503, "recovery", "本轮补测已保存但执行登记尚未确认，正在核查或已暂停，请查看原报告并核查保存，不会重复发出检测请求")
		}
		s.decorateReadReport(owner, id, report, before)
		report["accepted"] = true
		return report, nil
	}
	if retryID(report) != input.ExpectedRetryID {
		return nil, problem(409, "retry", "报告已有更新一轮补测，请重新读取再选择；没有再次发送请求")
	}
	s.decorateReadReport(owner, id, report, before)
	if report["finishConfirmed"] != true {
		return nil, problem(409, "retry", "本报告结束尚未确认，请等待结束或核查并重试保存；没有重复补测")
	}
	if stringField(report, "batchId") != "" && numberField(report, "batchFirstFinishedAtMs") == 0 {
		return nil, problem(409, "retry", "请先等本批首次执行结束，再补测这份报告的失败或未完成项；已保存回答保留")
	}
	if err = s.checkCapacityLocked(owner, 1); err != nil {
		return nil, err
	}
	get := func(key string) string { v, _ := report[key].(string); return v }
	targetID, groupID := get("targetId"), get("groupId")
	audit, err := s.targets.ResolveAuditContext(ctx, owner.UserID, targetID, groupID, true)
	if err != nil {
		return nil, problem(409, "account", err.Error())
	}
	if !audit.CanRun {
		return nil, problem(422, "account", audit.Reason)
	}
	if audit.WorkspaceID != owner.WorkspaceID || audit.TargetID != targetID || audit.GroupID != groupID || audit.AccountID != get("accountId") || audit.BaseURL != get("baseURL") || audit.Protocol != get("protocol") {
		return nil, problem(409, "context", "原报告账号、分组、地址或协议已变化，请新建检测；原报告没有修改")
	}
	if _, err = checkedCredential(audit); err != nil {
		return nil, err
	}
	changed, err := s.queryJSON(ctx, "retry-report", owner, map[string]any{"reportId": id, "retryId": input.RetryID, "expectedRetryId": input.ExpectedRetryID, "itemIds": input.ItemIDs, "target": map[string]any{"accountId": audit.AccountID, "baseURL": audit.BaseURL, "protocol": audit.Protocol}}, nil, false)
	if err != nil {
		// The write may have committed before the helper response disappeared. Never
		// enqueue without confirmation, and never replay paid work automatically.
		if asProblem(err).Status >= 500 {
			s.mu.Lock()
			s.registerBlockedLocked(owner, id, stringField(report, "batchId"), input.RetryID, "补测受理结果中断，未开始自动执行；原成功结果保留，请读取后重新补测", false)
			s.mu.Unlock()
		}
		return nil, err
	}
	batchID := stringField(report, "batchId")
	report, err = decodeReport(changed, id)
	if err != nil {
		s.mu.Lock()
		s.registerBlockedLocked(owner, id, batchID, input.RetryID, "补测返回内容无法确认；原成功结果保留，请读取原报告", false)
		s.mu.Unlock()
		return nil, err
	}
	var limits struct {
		Memory  int `json:"memoryLimitMiB"`
		Seconds int `json:"totalTimeoutSeconds"`
	}
	_ = json.Unmarshal(changed, &limits)
	if report["status"] != "queued" || retryID(report) != input.RetryID || limits.Memory < 128 || limits.Memory > 384 || limits.Seconds < 60 || limits.Seconds > maxTaskSeconds {
		s.mu.Lock()
		s.registerBlockedLocked(owner, id, batchID, input.RetryID, "补测等待状态或运行设置未确认；没有开始自动执行，请读取原报告", false)
		s.mu.Unlock()
		return nil, problem(503, "retry", "补测已保存但执行设置未确认，请读取原报告；没有自动重复请求")
	}
	entry := &waitingTask{Owner: owner, BatchID: batchID, Input: StartInput{ReportID: id, TargetID: targetID, GroupID: groupID, Model: get("model"), ExpectedContext: expected(audit), RetryID: input.RetryID}, AccountName: audit.AccountName, MemoryLimitMiB: limits.Memory, TotalTimeoutSeconds: limits.Seconds}
	s.mu.Lock()
	s.waiting = append(s.waiting, entry)
	s.mu.Unlock()
	next := s.reserveNextLocked()
	s.decorateReport(owner, id, report)
	report["accepted"] = true
	if next != nil {
		go s.startReserved(next)
	}
	return report, nil
}

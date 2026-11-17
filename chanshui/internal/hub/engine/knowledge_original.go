package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

// The complete original catalogue uses Check itself. The observers only attach
// item ownership, attempts and storage; they never select or classify differently.
func originalKnowledge(ctx context.Context, s *store.Store, o store.Owner, id string, defs []questions.Generated, allDefs []questions.Generated, previous map[string]any, g gateway.Client, timeout int, model string, retry *store.Retry, execute func(questions.Generated) (itemResult, error), results map[string]itemResult, mu *sync.Mutex, fail func(error), notify func(string, map[string]any)) (map[string]any, error) {
	byPrompt := map[string]questions.Generated{}
	full := retry == nil && len(defs) == len(knowledge.Anchors)
	for _, q := range defs {
		if p, ok := questions.OriginalKnowledge(q.Definition); ok {
			byPrompt[p.Prompt()] = q
		} else {
			full = false
		}
	}
	if len(byPrompt) != len(knowledge.Anchors) {
		full = false
	}
	var verdict knowledge.Verdict
	var executionErr error
	if full {
		seen := map[string]bool{}
		observed := g
		observed.BeforeCall = func(callCtx context.Context, request gateway.CallRequest) (context.Context, error) {
			q, ok := byPrompt[request.Prompt]
			if !ok {
				err := fmt.Errorf("原知识题没有对应的报告快照，未发送此请求")
				fail(err)
				return callCtx, err
			}
			item := &itemAttempts{ID: q.Definition.ID}
			callCtx = context.WithValue(callCtx, itemAttemptContextKey{}, item)
			if err := s.BeginItem(callCtx, o, id, item.ID); err != nil {
				fail(err)
				return callCtx, err
			}
			seen[item.ID] = true
			return callCtx, nil
		}
		observed.AfterCall = func(callCtx context.Context, request gateway.CallRequest, m gateway.Metrics, err error) error {
			q := byPrompt[request.Prompt]
			p, _ := questions.OriginalKnowledge(q.Definition)
			failure := m.Err
			if err != nil {
				failure = err.Error()
			}
			row := knowledge.Observe(p, m.Answer, failure)
			item := callCtx.Value(itemAttemptContextKey{}).(*itemAttempts)
			result := itemResult{Answer: m.Answer, ThinkingText: m.ThinkingText, Error: failure, Attempts: int(item.Count.Load()), Ms: m.Ms, Metrics: trimMetrics(&m), Evidence: map[string]any{"row": row, "prompt": p.Prompt()}, Judgment: questions.Judgment{Status: "reference"}}
			// An invalid original knowledge measurement is informational, not an HTTP failure.
			status := "done"
			if failure != "" {
				status = "error"
			}
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 11*time.Second)
			defer cancel()
			if err := s.SaveItem(saveCtx, o, id, item.ID, status, clean(result, g.Key), result.Attempts); err != nil {
				fail(err)
				return err
			}
			mu.Lock()
			results[item.ID] = compactResult("knowledge", result)
			mu.Unlock()
			notify("item-saved", map[string]any{"reportId": id, "itemId": item.ID})
			return nil
		}
		var err error
		verdict, err = knowledge.Check(ctx, observed, model, timeout)
		// Probe failure is represented by original rows/verdict and report partial,
		// whereas storage failure cancels the round through fail.
		if err != nil && ctx.Err() != nil {
			return asMap(verdict), err
		}
		if ctx.Err() == nil {
			for _, q := range defs {
				if seen[q.Definition.ID] {
					continue
				}
				if err := s.SaveItem(ctx, o, id, q.Definition.ID, "done", map[string]any{"judgment": map[string]any{"status": "not_asked", "reason": "原版知识探针按已取得的回答继续选题，本轮没有选到此题；不作为答错"}}, 0); err != nil {
					return nil, err
				}
			}
		}
	} else {
		// Explicit subsets/custom definitions cannot let Check ask disabled items.
		// This is the existing user-editable path, never a replacement binary search.
		customObservations := []map[string]any{}
		for _, q := range defs {
			if ctx.Err() != nil {
				executionErr = ctx.Err()
				break
			}
			if questions.Text(q.Definition.Parameters, "date") > time.Now().UTC().Format("2006-01-02") {
				if err := s.SaveItem(ctx, o, id, q.Definition.ID, "done", map[string]any{"judgment": map[string]any{"status": "not_asked", "reason": "此事件日期晚于今天，本轮不提问"}}, 0); err != nil {
					return nil, err
				}
				continue
			}
			result, err := execute(q)
			if err != nil {
				executionErr = err
				break
			}
			kind, reason := questions.GradeKnowledge(q.Definition, result.Answer)
			if result.Error != "" {
				kind, reason = "error", result.Error
			}
			customObservations = append(customObservations, map[string]any{"id": q.Definition.ID, "date": questions.Text(q.Definition.Parameters, "date"), "kind": kind, "reason": reason})
		}
		rows := []knowledge.Row{}
		attempts := 0
		custom := false
		mu.Lock()
		for itemID, result := range results {
			if p, ok := knowledge.Anchor(trimKnowledgePrefix(itemID)); ok {
				if result.Judgment.Status == "not_asked" || result.Judgment.Status == "not_applicable" {
					continue
				}
				ev := asMap(result.Evidence)
				var row knowledge.Row
				raw, _ := json.Marshal(ev["row"])
				_ = json.Unmarshal(raw, &row)
				if row.ID == "" {
					row = knowledge.Observe(p, result.Answer, result.Error)
				}
				rows = append(rows, row)
				attempts += result.Attempts
			}
		}
		mu.Unlock()
		for _, q := range allDefs {
			if q.Definition.Kind != "knowledge" {
				continue
			}
			if _, ok := questions.OriginalKnowledge(q.Definition); !ok {
				custom = true
			}
		}
		if custom {
			customObservations = nil
			mu.Lock()
			for _, q := range allDefs {
				if q.Definition.Kind != "knowledge" {
					continue
				}
				r, ok := results[q.Definition.ID]
				if !ok || r.Judgment.Status == "not_asked" || r.Judgment.Status == "not_applicable" {
					continue
				}
				kind, reason := questions.GradeKnowledge(q.Definition, r.Answer)
				if r.Error != "" {
					kind, reason = "error", r.Error
				}
				customObservations = append(customObservations, map[string]any{"id": q.Definition.ID, "date": questions.Text(q.Definition.Parameters, "date"), "kind": kind, "reason": reason})
			}
			mu.Unlock()
			if len(customObservations) == 0 {
				if executionErr != nil {
					return map[string]any{"status": "error", "label": "知识检测已停止", "reason": "本轮已停止，尚无完成的事件回答；已保存记录保留，可补测未完成项。", "rows": rows, "observations": customObservations, "total": 0}, executionErr
				}
				return map[string]any{"status": "not_applicable", "label": "本轮没有适用事件", "reason": "所选事件日期晚于今天，本轮不提问。", "rows": rows, "total": 0}, nil
			}
			return map[string]any{"observations": customObservations, "status": "custom", "label": "自定义知识事件", "reason": "本次使用自定义或修改过的知识题，逐题按所选规则检测；请展开查看实际题目和判定。", "rows": rows, "total": len(customObservations), "protocol": g.ProtocolFor(model)}, executionErr
		}
		// Preserve the original saved row order; retry replaces only the chosen row.
		orderKnowledgeRows(rows, previous)
		verdict = knowledge.FromRows(rows, g.ProtocolFor(model), attempts)
	}
	return asMap(verdict), executionErr
}
func asMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func trimKnowledgePrefix(id string) string { return strings.TrimPrefix(id, "knowledge:") }
func orderKnowledgeRows(rows []knowledge.Row, previous map[string]any) {
	index := map[string]int{}
	for i, p := range knowledge.Anchors {
		index[p.ID] = i + len(knowledge.Anchors)
	}
	if oldRows, ok := previous["rows"].([]any); ok {
		for i, value := range oldRows {
			if id, ok := asMap(value)["id"].(string); ok {
				index[id] = i
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return index[rows[i].ID] < index[rows[j].ID] })
}

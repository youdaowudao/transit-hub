package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

// Complete built-in catalogues use the original serial request loop. The
// observers only attach item ownership, attempts and storage.
func originalKnowledge(ctx context.Context, s *store.Store, o store.Owner, id string, defs []questions.Generated, allDefs []questions.Generated, previous map[string]any, crossYear, previousCrossYear bool, g gateway.Client, timeout int, model string, retry *store.Retry, execute func(questions.Generated) (itemResult, error), results map[string]itemResult, mu *sync.Mutex, fail func(error), notify func(string, map[string]any)) (map[string]any, error) {
	if asMap(previous["boundary_review"])["method"] == knowledge.ModelGroupMethod {
		return modelKnowledge(ctx, s, o, id, defs, allDefs, model, asMap(previous["boundary_review"]), retry, g.ProtocolFor(model), execute, results, mu)
	}
	byPrompt := map[string]questions.Generated{}
	byID := map[string]bool{}
	for _, q := range defs {
		if p, ok := questions.OriginalKnowledge(q.Definition); ok {
			prompt := p.Prompt()
			byPrompt[prompt] = q
			byID[p.ID] = true
		}
	}
	var verdict knowledge.Verdict
	var executionErr error
	fromCrossYearRows := knowledge.CrossYearFromRows
	if previousCrossYear {
		fromCrossYearRows = knowledge.PreviousCrossYearFromRows
	}
	complete := retry == nil && len(byPrompt) == len(defs)
	// Earlier releases stored the same old definitions with a different prompt format.
	current := complete && (crossYear || questions.MatchesPreviousKnowledge(defs))
	if (!hasCrossYearMethod(previous) || crossYear) && (current || (complete && matchesKnowledgeBank(byID, knowledge.Anchors))) {
		check := knowledge.Check
		skipReason := "原版知识探针按已取得的回答继续选题，本轮没有选到此题；不作为答错"
		reviewed := crossYear || (current && knowledge.ReviewModel(model))
		if current {
			check = knowledge.CheckCurrent
			if reviewed {
				check = knowledge.CheckCurrentReviewed
			}
			skipReason = "串行二分按已经取得的回答继续选题，本轮未选到这道题；不算答错。"
		}
		if crossYear {
			check = knowledge.CheckCrossYear
			if previousCrossYear {
				check = knowledge.CheckPreviousCrossYear
			}
			skipReason = "本轮根据已取得的回答选择了其他题目。这道题未提问，不算答错。"
		}
		roundDate := time.Now().UTC().Format("2006-01-02")
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
				if crossYear && callCtx.Err() != nil {
					return callCtx, callCtx.Err()
				}
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
			if reviewed {
				failure = knowledge.ReviewResponseError(m, err)
			}
			row := knowledge.Observe(p, m.Answer, failure)
			item := callCtx.Value(itemAttemptContextKey{}).(*itemAttempts)
			result := itemResult{Answer: m.Answer, ThinkingText: m.ThinkingText, Error: failure, Attempts: int(item.Count.Load()), Ms: m.Ms, Metrics: &m, Evidence: map[string]any{"row": row, "prompt": p.Prompt()}, Judgment: questions.Judgment{Status: "reference"}}
			result.Metrics = trimMetrics(result.Metrics)
			// An invalid original knowledge measurement is informational, not an HTTP failure.
			status := "done"
			if failure != "" {
				status = "error"
			}
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 11*time.Second)
			defer cancel()
			cleaned := clean(result, g.Key)
			// Keep the observation if the raw reply cannot fit in a stored item.
			if raw, _ := json.Marshal(cleaned); len(raw) > store.MaxItemBytes {
				result.Answer, result.ThinkingText = "", ""
				result.Judgment.Reason = "模型原始回答超过单题保存上限，已保留判定摘录。"
				cleaned = clean(result, g.Key)
			}
			if err := s.SaveItem(saveCtx, o, id, item.ID, status, cleaned, result.Attempts); err != nil {
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
		verdict, err = check(ctx, observed, model, timeout)
		// Probe failure is represented by original rows/verdict and report partial,
		// whereas storage failure cancels the round through fail.
		if crossYear && ctx.Err() != nil {
			// A cancellation during BeforeCall never reaches AfterCall; the
			// search can contain an error for a question that was never saved.
			var saved []knowledge.Row
			mu.Lock()
			for _, row := range verdict.Rows {
				if _, ok := results["knowledge:"+row.ID]; ok {
					saved = append(saved, row)
				}
			}
			mu.Unlock()
			verdict = knowledge.InterruptCrossYear(fromCrossYearRows(saved, verdict.Protocol, verdict.Attempts))
			return asMap(verdict), ctx.Err()
		}
		if err != nil && ctx.Err() != nil {
			return asMap(verdict), err
		}
		if ctx.Err() == nil {
			for _, q := range defs {
				if seen[q.Definition.ID] {
					continue
				}
				if crossYear && questions.MatchesModelKnowledge(allDefs) && questions.IsModelAddition(q.Definition.ID) {
					continue
				}
				reason := skipReason
				if crossYear && questions.Text(q.Definition.Parameters, "date") > roundDate {
					reason = "此事件日期晚于今天，本轮不提问"
				}
				if err := s.SaveItem(ctx, o, id, q.Definition.ID, "done", map[string]any{"judgment": map[string]any{"status": "not_asked", "reason": reason}}, 0); err != nil {
					if crossYear && ctx.Err() != nil {
						return asMap(knowledge.InterruptCrossYear(verdict)), err
					}
					return nil, err
				}
			}
		}
	} else {
		// Explicit subsets/custom definitions cannot let Check ask disabled items.
		// This is the existing user-editable path, never a replacement binary search.
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
			_, err := execute(q)
			if err != nil {
				executionErr = err
				break
			}
		}
		rows := []knowledge.Row{}
		attempts := 0
		custom := false
		customObservations := []map[string]any{}
		mu.Lock()
		for _, q := range allDefs {
			def := q.Definition
			if def.Kind != "knowledge" {
				continue
			}
			p, original := questions.OriginalKnowledge(def)
			custom = custom || !original
			result, ok := results[def.ID]
			if !ok || result.Judgment.Status == "not_asked" || result.Judgment.Status == "not_applicable" {
				continue
			}
			ev := asMap(result.Evidence)
			if previous["boundary_review"] != nil && result.Judgment.Status == "unknown" && result.Error == "" && (!crossYear || ev["row"] == nil) {
				continue
			}
			var kind, reason string
			switch {
			case original:
				var row knowledge.Row
				raw, _ := json.Marshal(ev["row"])
				_ = json.Unmarshal(raw, &row)
				if row.ID == "" {
					row = knowledge.Observe(p, result.Answer, result.Error)
				}
				rows = append(rows, row)
				attempts += result.Attempts
				kind, reason = row.Kind, row.Answer
				if row.Error != "" {
					reason = row.Error
				}
			case result.Error != "":
				kind, reason = "error", result.Error
			default:
				kind, _ = ev["kind"].(string)
				reason, _ = ev["reason"].(string)
				if kind == "" {
					kind, reason = questions.GradeKnowledge(def, result.Answer)
				}
			}
			customObservations = append(customObservations, map[string]any{"id": def.ID, "date": questions.Text(def.Parameters, "date"), "kind": kind, "reason": reason})
		}
		mu.Unlock()
		if custom {
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
		if crossYear {
			verdict = fromCrossYearRows(rows, g.ProtocolFor(model), attempts)
		} else {
			verdict = knowledge.FromRows(rows, g.ProtocolFor(model), attempts)
			if !hasCrossYearMethod(previous) {
				var old knowledge.Verdict
				raw, _ := json.Marshal(previous)
				_ = json.Unmarshal(raw, &old)
				verdict = knowledge.ReviewAfterRetry(verdict, old.BoundaryReview)
			}
		}
	}
	return asMap(verdict), executionErr
}

func modelKnowledge(ctx context.Context, s *store.Store, o store.Owner, reportID string, defs, allDefs []questions.Generated, model string, review map[string]any, retry *store.Retry, protocol string, execute func(questions.Generated) (itemResult, error), results map[string]itemResult, mu *sync.Mutex) (map[string]any, error) {
	// Use the group saved when the report was created; fall back for reports saved before groups were recorded.
	group, ok := knowledge.SavedGroup(review)
	if !ok {
		group, ok = knowledge.GroupForModel(model)
	}
	if !ok {
		return nil, fmt.Errorf("本报告的型号题组无法识别，未发送知识题请求")
	}
	selected := map[string]bool{}
	for _, id := range append(append([]string{}, group.Know...), group.Newer...) {
		selected["knowledge:"+id] = true
	}
	var runErr error
	for _, q := range defs {
		if !selected[q.Definition.ID] {
			if retry == nil {
				continue
			}
			judgment := questions.Judgment{Status: "not_asked", Reason: "本题不在本型号题组中，本轮不提问。"}
			if err := s.SaveItem(ctx, o, reportID, q.Definition.ID, "done", map[string]any{"judgment": judgment}, 0); err != nil {
				runErr = err
				break
			}
			mu.Lock()
			results[q.Definition.ID] = itemResult{Judgment: judgment, Attempts: results[q.Definition.ID].Attempts}
			mu.Unlock()
			continue
		}
		if _, err := execute(q); err != nil {
			runErr = err
			break
		}
	}
	var rows []knowledge.Row
	attempts := 0
	mu.Lock()
	for _, q := range allDefs {
		if !selected[q.Definition.ID] {
			continue
		}
		result, exists := results[q.Definition.ID]
		if !exists || result.Judgment.Status == "not_asked" || result.Judgment.Status == "not_applicable" {
			continue
		}
		p, ok := questions.OriginalKnowledge(q.Definition)
		if !ok {
			continue
		}
		var row knowledge.Row
		raw, _ := json.Marshal(asMap(result.Evidence)["row"])
		_ = json.Unmarshal(raw, &row)
		if row.ID == "" {
			row = knowledge.Observe(p, result.Answer, result.Error)
		}
		rows = append(rows, row)
		attempts += result.Attempts
	}
	mu.Unlock()
	return asMap(knowledge.ModelGroupVerdict(group, rows, protocol, attempts)), runErr
}

func hasCrossYearMethod(summary map[string]any) bool {
	meta, _ := summary["boundary_review"].(map[string]any)
	return meta["method"] == knowledge.CrossYearMethod
}

func matchesKnowledgeBank(ids map[string]bool, anchors []knowledge.Probe) bool {
	if len(ids) != len(anchors) {
		return false
	}
	for _, p := range anchors {
		if !ids[p.ID] {
			return false
		}
	}
	return true
}

func asMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func orderKnowledgeRows(rows []knowledge.Row, previous map[string]any) {
	index := map[string]int{}
	oldRows, _ := previous["rows"].([]any)
	next := len(oldRows)
	// A saved subset can include retired defaults without matching the complete v5 bank.
	anchors := append(knowledge.PreviousAnchors(), knowledge.CurrentAnchors()...)
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].HappenedAt != anchors[j].HappenedAt {
			return anchors[i].HappenedAt < anchors[j].HappenedAt
		}
		return anchors[i].ID < anchors[j].ID
	})
	for _, bank := range [][]knowledge.Probe{knowledge.Anchors, anchors} {
		for _, p := range bank {
			if _, exists := index[p.ID]; !exists {
				index[p.ID] = next
				next++
			}
		}
	}
	for i, value := range oldRows {
		if id, ok := asMap(value)["id"].(string); ok {
			index[id] = i
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, leftFound := index[rows[i].ID]
		right, rightFound := index[rows[j].ID]
		if leftFound != rightFound {
			return leftFound
		}
		return left < right
	})
}

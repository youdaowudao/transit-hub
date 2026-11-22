package engine

import (
	"encoding/json"
	"fmt"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/integrity"
	"github.com/kiss-kedaya/chanshui/internal/iq"
)

// RescoreSummary uses only the report's saved questions and item results.
func RescoreSummary(defs []questions.Generated, items map[string]store.Item, model, scoringVersion string) (map[string]any, error) {
	if scoringVersion == "" {
		scoringVersion = api.DefaultScoringVersion
	}
	results := make(map[string]itemResult, len(items))
	for _, q := range defs {
		id := q.Definition.ID
		item, ok := items[id]
		if !ok {
			return nil, fmt.Errorf("报告缺少题目 %s 的记录，请核查原报告数据", id)
		}
		r, err := savedResult(item.Result)
		if err != nil {
			return nil, fmt.Errorf("题目 %s 的判分记录无法读取：%w", id, err)
		}
		if item.Status != "done" {
			r.Judgment.Status = "unknown"
		}
		results[id] = compactResult(q.Definition.Kind, r)
	}
	return finishedScores(defs, results, model, scoringVersion), nil
}

func finishedScores(defs []questions.Generated, results map[string]itemResult, model, scoringVersion string) map[string]any {
	summary := summarizeForVersion(defs, results, model, scoringVersion)
	total, _ := summary["total"].(map[string]any)
	complete := scoringVersion != api.HubScoringVersion || total["complete"] != false
	for _, result := range results {
		if result.Error != "" || result.Judgment.Status == "unknown" {
			complete = false
			break
		}
	}
	summary["complete"] = complete
	return summary
}

func boolJudgment(pass *bool, reason string) questions.Judgment {
	j := questions.Judgment{Status: "unknown", Max: 1, Reason: reason}
	if pass != nil {
		n := 0
		if *pass {
			n = 1
		}
		j.Status = "known"
		j.Earned = &n
	}
	return j
}

// Keep public score identifiers at the three service-generated paths when
// a short credential matches part of an identifier. Other text stays redacted.
func restoreScorePartKeys(source, cleaned map[string]any) {
	sourceTotal, _ := source["total"].(map[string]any)
	cleanTotal, _ := cleaned["total"].(map[string]any)
	sourceVerdict, _ := source["verdict"].(map[string]any)
	cleanVerdict, _ := cleaned["verdict"].(map[string]any)
	sourceVerdictTotal, _ := sourceVerdict["total"].(map[string]any)
	cleanVerdictTotal, _ := cleanVerdict["total"].(map[string]any)
	for _, pair := range [][2]map[string]any{{source, cleaned}, {sourceTotal, cleanTotal}, {sourceVerdictTotal, cleanVerdictTotal}} {
		original, _ := pair[0]["parts"].([]any)
		redacted, _ := pair[1]["parts"].([]any)
		if len(original) != len(redacted) {
			continue
		}
		for i, raw := range original {
			before, _ := raw.(map[string]any)
			after, ok := redacted[i].(map[string]any)
			if !ok {
				continue
			}
			switch key, _ := before["key"].(string); key {
			case "fingerprint", "iq", "vision", "injection", "tools", "web", "thinking", "cache":
				after["key"] = key
			}
		}
	}
}

// summarize delegates IQ, integrity and total calculation to the original API.
func summarize(defs []questions.Generated, results map[string]itemResult, models ...string) map[string]any {
	model := "fixture"
	if len(models) > 0 {
		model = models[0]
	}
	return summarizeForVersion(defs, results, model, api.DefaultScoringVersion)
}

func summarizeForVersion(defs []questions.Generated, results map[string]itemResult, model, scoringVersion string) map[string]any {
	v := api.Verdict{ScoringVersion: scoringVersion}
	verifiedCache := scoringVersion == api.HubScoringVersion
	selected := map[string]bool{}
	paper := []iq.Question{}
	answers := map[string]api.QuestionResult{}
	identityPending := false
	cachePending := false
	for _, q := range defs {
		kind := q.Definition.Kind
		r := results[q.Definition.ID]
		if q.Definition.Judge.Type == questions.ModelIdentityJudge {
			if !questions.ModelIdentityApplies(model) {
				continue
			}
			identityPending = identityPending || r.Error != "" || r.Judgment.Status != "known" || r.Judgment.Earned == nil
		}
		if kind == "fingerprint_behavior" || kind == "knowledge" {
			continue
		}
		if (kind == "iq" || kind == "vision") && q.Definition.Judge.Type == "manual" {
			continue
		}
		if r.Judgment.Status == "not_applicable" && kind != "vision" {
			continue
		}
		key := kind
		if key == "fingerprint_numeric" {
			key = "fingerprint"
		}
		selected[key] = true
		ev := asMap(r.Evidence)
		if ev == nil {
			ev = map[string]any{}
		}
		if r.Error != "" {
			ev["error"] = r.Error
		}
		switch key {
		case "iq":
			item := q.IQ()
			paper = append(paper, item)
			var answer api.QuestionResult
			raw, _ := json.Marshal(ev)
			_ = json.Unmarshal(raw, &answer)
			if answer.ID == "" {
				answer = api.QuestionResult{ID: item.ID, Title: item.Title, Max: item.MaxScore, Error: r.Error, Half: r.Judgment.Half, Tags: r.Judgment.Tags}
				if r.Judgment.Earned != nil {
					answer.Earned = *r.Judgment.Earned
				}
				if r.Judgment.Status != "known" && answer.Error == "" {
					answer.Error = "请求未完成"
					if q.Definition.Judge.Type == questions.ModelIdentityJudge && r.Judgment.Reason != "" {
						answer.Error = r.Judgment.Reason
					}
				}
			}
			answers[item.ID] = answer
		case "vision":
			if len(ev) == 0 {
				item := q.IQ()
				answer := api.QuestionResult{ID: item.ID, Max: item.MaxScore, Error: r.Error}
				if r.Judgment.Earned != nil {
					answer.Earned = *r.Judgment.Earned
				}
				answer.Answer = ""
				if r.Recognized {
					answer.Answer = "0"
				}
				ev = api.VisionResult(item, answer, nil)
			}
			v.Vision = ev
		case "fingerprint":
			v.Fingerprint = ev
		case "injection":
			v.Injection = ev
		case "tools":
			v.Tools = ev
		case "web":
			v.Web = ev
		case "cache":
			if verifiedCache && r.ManualJudgment && r.Error == "" && r.Judgment.Status == "known" && r.Judgment.Earned != nil {
				ev["pass"] = *r.Judgment.Earned > 0
			}
			_, hasPass := ev["pass"].(bool)
			evidenceError, _ := ev["error"].(string)
			cachePending = cachePending || (verifiedCache && (r.Judgment.Status != "known" || !hasPass || evidenceError != ""))
			v.Cache = ev
		case "thinking":
			v.Thinking = ev
		}
	}
	if cachePending {
		v.Cache = map[string]any{"pass": nil}
	}
	v = api.AssembleVerdict(v, paper, answers, model, selected)
	if identityPending {
		if m, ok := v.IQ.(map[string]any); ok {
			m["score"], m["complete"], m["note"] = nil, false, questions.ModelIdentityPending
		}
		composed := v.Total.(integrity.Total)
		for i := range composed.Parts {
			if composed.Parts[i].Key == "iq" {
				composed.Parts[i].Known, composed.Parts[i].Points = false, 0
			}
		}
		composed = integrity.Compose(composed.Parts)
		composed.Reason = questions.ModelIdentityPending
		v.Total = composed
	}
	// Original question bodies live once in each item, then the frontend joins
	// those DTOs into per_question. This summary stays below the 64KiB channel.
	if iqMap, ok := v.IQ.(map[string]any); ok {
		delete(iqMap, "per_question")
	}
	verdict := asMap(v)
	for _, key := range []string{"fingerprint", "vision", "injection", "tools", "web", "cache", "thinking"} {
		verdict[key] = nil
	}
	iqMap := asMap(v.IQ)
	total := asMap(v.Total)
	if cachePending {
		total["complete"] = false
		if identityPending {
			total["score"] = nil
		} else {
			total["reason"] = api.HubCacheUnconfirmedReason
		}
		verdict["total"] = total
	}
	var totalScore any = total["score"]
	if identityPending {
		totalScore = nil
	}
	return map[string]any{"verdict": verdict, "total": total, "totalScore": totalScore, "parts": total["parts"], "iqMax": iqMap["paper_max"], "iqScore": iqMap["score"]}
}

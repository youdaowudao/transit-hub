package engine

import (
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/integrity"
	"github.com/kiss-kedaya/chanshui/internal/iq"
)

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

// summarize reuses upstream IQ half-credit aggregation and section weights.
// Compose's partial zero score is never presented as a completed Hub result.
func summarize(defs []questions.Generated, results map[string]itemResult) map[string]any {
	parts := []integrity.Part{{Key: "fingerprint", Label: "模型指纹归因", MaxPoints: 10}, {Key: "iq", Label: "推理表现", MaxPoints: 100}, {Key: "vision", Label: "识图能力", MaxPoints: 100}, {Key: "injection", Label: "注入大小", MaxPoints: 10}, {Key: "tools", Label: "工具调用", MaxPoints: 1}, {Key: "web", Label: "联网检测", MaxPoints: 1}, {Key: "thinking", Label: "思维摘要回传", MaxPoints: 5}, {Key: "cache", Label: "缓存读写", MaxPoints: 1}}
	index := map[string]int{}
	for i, p := range parts {
		index[p.Key] = i
	}
	paper := []iq.QItem{}
	iqKnown := true
	counts := map[string]int{}
	for _, q := range defs {
		kind := q.Definition.Kind
		if kind == "knowledge" || kind == "fingerprint_behavior" {
			continue
		}
		if (kind == "iq" || kind == "vision") && q.Definition.Judge.Type == "manual" {
			continue
		}
		key := kind
		if kind == "fingerprint_numeric" {
			key = "fingerprint"
		}
		i, ok := index[key]
		if !ok {
			continue
		}
		res, exists := results[q.Definition.ID]
		if exists && res.Judgment.Status == "not_applicable" {
			continue
		}
		part := &parts[i]
		if counts[key] == 0 {
			part.Known = true
			part.Points = 0
			if kind != "iq" {
				part.MaxPoints = 0
			}
		}
		counts[key]++
		part.Applicable = true
		known := exists && res.Error == "" && res.Judgment.Status == "known" && res.Judgment.Earned != nil
		if !known {
			part.Known = false
		}
		if kind == "iq" {
			item := iq.QItem{ID: q.Definition.ID, Title: q.Definition.Title, Max: q.Definition.MaxScore}
			if known {
				item.Earned = *res.Judgment.Earned
				item.Half = res.Judgment.Half
			} else {
				iqKnown = false
			}
			paper = append(paper, item)
			continue
		}
		if kind == "vision" {
			part.MaxPoints += 100
			if known {
				seen := 0
				if res.Recognized {
					seen = 50
				}
				if q.Definition.MaxScore > 0 {
					part.Points += seen + (*res.Judgment.Earned*50+q.Definition.MaxScore/2)/q.Definition.MaxScore
				}
			}
			continue
		}
		max := res.Judgment.Max
		if max < 1 {
			max = map[string]int{"fingerprint": 10, "injection": 10, "thinking": 5, "tools": 1, "web": 1, "cache": 1}[key]
		}
		part.MaxPoints += max
		if known {
			part.Points += *res.Judgment.Earned
		}
	}
	iqResult := iq.Score(paper)
	parts[index["iq"]].Points = iqResult.Score
	parts[index["iq"]].Known = iqKnown && len(paper) > 0
	total := integrity.Compose(parts)
	applicable, complete := 0, true
	for _, p := range total.Parts {
		if p.Applicable {
			applicable++
			if !p.Known {
				complete = false
			}
		}
	}
	var score any
	if complete && applicable > 0 {
		score = total.Score
	}
	reason := "仅对本次选中的适用板块按原权重计算；人工题、知识题、行为参考和不适用项不计总分。"
	if !complete {
		reason += "仍有项目未确认，总分尚未完成，请查看逐项结果。"
	} else if applicable == 0 {
		reason += "本次没有自动计分项，请人工查看答案。"
	} else {
		reason += "这些板块已完成；不同题库配置不直接比较总分。"
	}
	out := map[string]any{"total": map[string]any{"score": score, "max": 100, "parts": total.Parts, "complete": complete, "reason": reason}, "totalScore": score, "parts": total.Parts, "iqMax": iqResult.Max, "iqScore": nil}
	if iqKnown && len(paper) > 0 {
		out["iqScore"] = iqResult.Score
	}
	return out
}

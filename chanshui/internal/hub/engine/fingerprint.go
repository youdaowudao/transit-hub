package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/integrity"
)

func numeric(ctx context.Context, g gateway.Client, model string, timeout int, q questions.Generated) (itemResult, error) {
	out := itemResult{Judgment: questions.Judgment{Status: "reference", Max: 0}}
	bank, e := fingerprint.LoadBank()
	if e != nil {
		return out, e
	}
	r := rand.New(rand.NewSource(q.Seed))
	counts := r.Perm(41)
	outputs := []fingerprint.Output{}
	evidence := []map[string]any{}
	compatible := questions.Compatible(q.Definition)
	for i := 0; i < 10 && ctx.Err() == nil; i++ {
		count := 292 + counts[i]
		prompt := questions.NumericPrompt(q.Definition, count, r)
		m, e := g.CallMessages(ctx, gateway.CallRequest{Model: model, Prompt: prompt, MaxTokens: 16384, Stream: g.ProtocolFor(model) != "messages", TimeoutSeconds: timeout, SuppressDefaultEffort: true})
		entry := map[string]any{"prompt": prompt, "answer": m.Answer, "expectedCount": count, "parsedCount": len(fingerprint.ParseNumbers(m.Answer)), "attempts": m.Attempts, "protocol": m.Protocol, "ms": m.Ms}
		if e != nil {
			entry["error"] = e.Error()
		}
		if m.Err != "" {
			entry["error"] = m.Err
		}
		evidence = append(evidence, entry)
		if e != nil || m.Err != "" {
			out.Evidence = map[string]any{"compatible": compatible, "samples": evidence}
			if e != nil {
				return out, e
			}
			return out, fmt.Errorf("%s", m.Err)
		}
		if m.StopReason == "max_tokens" || m.StopReason == "length" {
			entry["usable"] = false
			continue
		}
		usable := fingerprint.Usable(m.Answer, count, bank.Numeric.MinimumValidNumbers)
		entry["usable"] = usable
		if usable {
			outputs = append(outputs, fingerprint.Output{Text: m.Answer, ExpectedCount: count})
		}
		if compatible && len(outputs) >= 3 {
			attr := fingerprint.Attribute(outputs, bank)
			if attr.TopProb >= .90 {
				break
			}
		}
	}
	ev := map[string]any{"compatible": compatible, "samples": evidence, "usedOutputs": len(outputs), "source": bank.Source, "sampleMin": 3, "sampleLimit": 10}
	out.Evidence = ev
	if !compatible {
		out.Judgment.Status = "not_applicable"
		ev["note"] = "提示模板已修改，原参考样本不再适用；只采样，不判断模型身份"
		return out, nil
	}
	if len(outputs) < 3 {
		return out, fmt.Errorf("数字指纹有效样本不足3个，本项未确认，不把它当假模型")
	}
	attr := fingerprint.Attribute(outputs, bank)
	ev["attribution"] = attr
	ev["note"] = "概率仅在参考库候选内比较，不能单独证明模型身份"
	points := 10
	if !attr.LowConfidence && integrity.Confirmed("numeric", attr.TopProb) && integrity.FingerprintMismatch(model, attr.Family, attr.TopModel) {
		points = 0
	}
	out.Judgment = questions.Judgment{Status: "known", Max: 10, Earned: &points}
	return out, nil
}
func addBehaviorSummary(summary map[string]any, defs []questions.Generated, results map[string]itemResult) {
	responses := make([]string, 3)
	compatible := true
	slots := map[int]bool{}
	count := 0
	for _, q := range defs {
		if q.Definition.Kind != "fingerprint_behavior" {
			continue
		}
		count++
		slot := questions.Int(q.Definition.Parameters, "slot")
		if slot < 0 || slot > 2 || slots[slot] {
			compatible = false
			continue
		}
		slots[slot] = true
		res, ok := results[q.Definition.ID]
		if !ok || res.Error != "" || strings.TrimSpace(res.Answer) == "" || !questions.Compatible(q.Definition) {
			compatible = false
			continue
		}
		responses[slot] = res.Answer
	}
	compatible = compatible && count == 3 && len(slots) == 3
	if !compatible {
		summary["behavior"] = map[string]any{"compatible": false, "note": "行为探针未全部选择、未完成或模板已修改；仅保留逐项样本，不作旧库归因"}
		return
	}
	bank, e := fingerprint.LoadBank()
	if e != nil {
		summary["behavior"] = map[string]any{"note": "行为参考库读取失败，不影响数字板块"}
		return
	}
	summary["behavior"] = fingerprint.AnalyzeBehaviorResponses(responses, bank, fingerprint.Attribution{LowConfidence: true})
}

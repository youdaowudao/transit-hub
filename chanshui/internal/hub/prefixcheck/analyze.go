package prefixcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTextBytes       = 16 * 1024
	maxSafeInteger     = 1<<53 - 1
	defaultTemplate    = "{{sample}}"
	redactedPromptText = "[密钥已隐藏]"
	noInjection        = "本报告没有注入检测结果"
	multipleInjections = "本报告有多道注入题，无法确定按哪一道计算"
	noInjectionUsage   = "注入检测的请求 A 没有可用的用量记录"
	oldCustomTemplate  = "这份旧报告的注入模板被修改过，无法计算"
)

type StoredItem struct {
	ItemID, Kind, JudgeType, Template, Prompt string
	OrderIndex                                int
	ImageAvailable                            bool
	Result                                    json.RawMessage
}

type Candidate struct {
	OrderIndex int
	Text       string
	HasSystem  bool
	Total      int64
}

type Point struct{ N, Input, Total int64 }

type InjectionItem struct {
	ItemID     string
	OrderIndex int
	Template   string
	Points     []Point
	Reliable   *bool
	Error      string
}

type Reference struct {
	ItemID        string `json:"itemId"`
	OrderIndex    int    `json:"orderIndex"`
	ATotal        int64  `json:"aTotal"`
	APromptTokens int    `json:"aPromptTokens"`
	Overhead      int64  `json:"overhead"`
}

type Analysis struct {
	Version         int        `json:"version"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason"`
	Tokenizer       string     `json:"tokenizer"`
	ComputedAtMs    int64      `json:"computedAtMs"`
	Reference       *Reference `json:"reference"`
	ReferenceReason string     `json:"referenceReason"`
	Overheads       [][2]int64 `json:"overheads"`
	Checked         int        `json:"checked"`
	Skipped         int        `json:"skipped"`
}

func object(raw json.RawMessage, dest any) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(dest) == nil
}

func integer(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := number.Int64()
	return n, err == nil && n >= -maxSafeInteger && n <= maxSafeInteger
}

func textValue(raw json.RawMessage) (string, bool) {
	var value string
	returnString := len(raw) != 0 && raw[0] == '"' && json.Unmarshal(raw, &value) == nil
	return value, returnString
}

func usageTotal(raw json.RawMessage) (int64, bool) {
	var usage struct {
		UsagePresent                     any
		Status, InputTokens, CacheRead   any
		CacheCreation5m, CacheCreation1h any
	}
	if !object(raw, &usage) || usage.UsagePresent != true {
		return 0, false
	}
	if status, ok := integer(usage.Status); !ok || status != 200 {
		return 0, false
	}
	var total int64
	for _, value := range []any{usage.InputTokens, usage.CacheRead, usage.CacheCreation5m, usage.CacheCreation1h} {
		if value == nil {
			continue
		}
		n, ok := integer(value)
		if !ok || n < 0 || n > maxSafeInteger-total {
			return 0, false
		}
		total += n
	}
	return total, true
}

func Extract(it StoredItem) (cand *Candidate, inj *InjectionItem, skipped bool) {
	if it.Kind == "injection" {
		return nil, extractInjection(it), false
	}
	switch it.Kind {
	case "vision", "cache", "tools", "web", "thinking", "fingerprint_numeric":
		return nil, nil, true
	}
	if it.ImageAvailable {
		return nil, nil, true
	}
	var result struct{ Evidence, Metrics json.RawMessage }
	if !object(it.Result, &result) {
		return nil, nil, true
	}
	var evidence struct{ Prompt, Metrics json.RawMessage }
	object(result.Evidence, &evidence)
	text, hasText := textValue(evidence.Prompt)
	withSystem := hasText && len(evidence.Metrics) != 0 && evidence.Metrics[0] == '{'
	metrics := result.Metrics
	if withSystem {
		metrics = evidence.Metrics
	}
	if !hasText {
		text = it.Prompt
	}
	total, valid := usageTotal(metrics)
	if !valid || text == "" || len(text) > MaxTextBytes || strings.Contains(text, redactedPromptText) {
		return nil, nil, true
	}
	return &Candidate{OrderIndex: it.OrderIndex, Text: text, HasSystem: withSystem, Total: total}, nil, false
}

func injectionTemplate(template string) string {
	if template == "" {
		return defaultTemplate
	}
	return template
}

func extractInjection(it StoredItem) *InjectionItem {
	inj := &InjectionItem{ItemID: it.ItemID, OrderIndex: it.OrderIndex, Template: it.Template}
	var result, evidence map[string]json.RawMessage
	_ = json.Unmarshal(it.Result, &result)
	_ = json.Unmarshal(result["evidence"], &evidence)
	var hasError bool
	inj.Error, hasError = textValue(result["error"])
	if !hasError {
		inj.Error, _ = textValue(evidence["error"])
	}
	var reliable bool
	switch string(evidence["reliable"]) {
	case "true", "false":
		reliable = string(evidence["reliable"]) == "true"
		inj.Reliable = &reliable
	}
	var points []json.RawMessage
	if json.Unmarshal(evidence["points"], &points) != nil {
		return inj
	}
	template := injectionTemplate(it.Template)
	samples := strings.Count(template, defaultTemplate)
	templateBytes := len(template) - len(defaultTemplate)*samples
	for i, chars := range []int{1, 1599} {
		if i >= len(points) {
			break
		}
		var raw map[string]any
		if !object(points[i], &raw) {
			break
		}
		n, nOK := integer(raw["n"])
		input, inputOK := integer(raw["input"])
		total, totalOK := integer(raw["total"])
		if !nOK || !inputOK || !totalOK || n != int64(templateBytes+samples*chars) || input < 0 || total < input {
			break
		}
		inj.Points = append(inj.Points, Point{N: n, Input: input, Total: total})
	}
	return inj
}

func referenceProblem(injections []InjectionItem) string {
	switch {
	case len(injections) == 0:
		return noInjection
	case len(injections) > 1:
		return multipleInjections
	case len(injections[0].Points) == 0:
		return noInjectionUsage
	default:
		return ""
	}
}

func newReference(inj InjectionItem, count int) *Reference {
	return &Reference{
		ItemID: inj.ItemID, OrderIndex: inj.OrderIndex, ATotal: inj.Points[0].Total,
		APromptTokens: count, Overhead: inj.Points[0].Total - int64(count),
	}
}

func Analyze(ctx context.Context, cands []Candidate, injections []InjectionItem, skipped int, systemPrompt string, count CountFunc) (Analysis, error) {
	countText := func(text string) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return count(text)
	}
	out := Analysis{
		Version: 1, Status: "ok", Tokenizer: TokenizerName,
		Overheads: make([][2]int64, 0, len(cands)), Skipped: skipped,
	}
	for _, cand := range cands {
		own, err := countText(cand.Text)
		if err != nil {
			return Analysis{}, err
		}
		if cand.HasSystem {
			systemCount, err := countText(systemPrompt)
			if err != nil {
				return Analysis{}, err
			}
			own += systemCount
		}
		out.Overheads = append(out.Overheads, [2]int64{int64(cand.OrderIndex), cand.Total - int64(own)})
	}
	out.ReferenceReason = referenceProblem(injections)
	if out.ReferenceReason == "" {
		injection := injections[0]
		n, err := countText(strings.ReplaceAll(injectionTemplate(injection.Template), defaultTemplate, "x"))
		if err != nil {
			return Analysis{}, err
		}
		out.Reference = newReference(injection, n)
	}
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}
	sort.Slice(out.Overheads, func(i, j int) bool { return out.Overheads[i][0] < out.Overheads[j][0] })
	out.Checked = len(out.Overheads)
	out.ComputedAtMs = time.Now().UnixMilli()
	return out, nil
}

func Unavailable(reason string) Analysis {
	return Analysis{Version: 1, Status: "unavailable", Reason: reason, Tokenizer: TokenizerName, ComputedAtMs: time.Now().UnixMilli(), Overheads: [][2]int64{}}
}

func LimitReason(reason string) string {
	const limit, suffix = 512, "…"
	reason = strings.ToValidUTF8(reason, "�")
	if len(reason) <= limit {
		return reason
	}
	end := limit - len(suffix)
	for !utf8.RuneStart(reason[end]) {
		end--
	}
	return reason[:end] + suffix
}

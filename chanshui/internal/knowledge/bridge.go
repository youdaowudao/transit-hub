package knowledge

import (
	"encoding/json"
	"io"
	"strings"
)

// Prompt and Classify expose the original implementation for persistence and
// explicit per-question retries.
func (p Probe) Prompt() string                          { return p.prompt() }
func (p Probe) Classify(answer string) (string, string) { return classify(p, answer) }
func Anchor(id string) (Probe, bool) {
	for _, p := range Anchors {
		if p.ID == id {
			return p, true
		}
	}
	e, ok := eventByID(id)
	return e.Probe, ok
}
func Observe(p Probe, answer, failure string) Row {
	s := newSearch()
	kind, focus := classify(p, answer)
	if failure != "" {
		kind, focus = kindError, failure
	}
	s.apply(p, kind, focus)
	return s.rows[0]
}

// ObserveModel 只把可识别的选项视为答对或答错，避免无关内容被误判。
func ObserveModel(p Probe, answer, failure string, webUsed bool) Row {
	row := Observe(p, answer, failure)
	if failure != "" {
		return row
	}
	if webUsed {
		row.Kind, row.OK, row.Unsure = kindInvalid, false, true
		row.Answer = "上游返回了工具或联网记录，本题未确认，请查看回答后补测。"
		return row
	}
	if row.Kind == kindInvalid {
		return row
	}
	text := stripCodeFence(strings.TrimSpace(answer))
	selected, ok := singleModelChoice(p, text)
	if !ok && !strings.ContainsAny(text, "{}") {
		selected, ok = p.lookupChoice(text)
		if !ok {
			plain := strings.ToLower(strings.TrimRight(text, "。.!！ "))
			switch plain {
			case "不知道", "我不知道", "不确定", "我不确定", "不清楚", "我不清楚", "无法确定", "unknown", "i don't know", "i do not know":
				selected, ok = p.lookupLetter("D")
			}
		}
		if !ok {
			if matches := finalRE.FindStringSubmatch(text); len(matches) > 1 && strings.TrimSpace(matches[0]) == text {
				selected, ok = p.lookupChoice(strings.TrimSpace(matches[1]))
			}
		}
	}
	if ok {
		return Observe(p, `{"choice":"`+selected.Letter+`"}`, "")
	}
	row.Kind, row.OK, row.Unsure = kindInvalid, false, true
	return row
}

// stripCodeFence removes one surrounding Markdown fence such as ```json ... ```.
func stripCodeFence(text string) string {
	if !strings.HasPrefix(text, "```") || !strings.HasSuffix(text, "```") || len(text) < 6 {
		return text
	}
	body := strings.TrimSuffix(text[3:], "```")
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		if lang := strings.TrimSpace(body[:i]); lang == "" || lang == "json" || lang == "JSON" {
			body = body[i+1:]
		}
	}
	return strings.TrimSpace(body)
}

func singleModelChoice(p Probe, answer string) (Choice, bool) {
	decoder := json.NewDecoder(strings.NewReader(answer))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Choice{}, false
	}
	var selected Choice
	count := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return Choice{}, false
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return Choice{}, false
		}
		if key == "choice" {
			count++
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return Choice{}, false
			}
			var found bool
			selected, found = p.lookupChoice(value)
			if !found {
				return Choice{}, false
			}
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || count != 1 {
		return Choice{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Choice{}, false
	}
	return selected, true
}

// FromRows reuses the same boundary semantics after an explicitly requested
// retry. Callers provide only saved actual observations, never guessed rows.
func FromRows(rows []Row, protocol string, attempts int) Verdict {
	s := newSearch()
	for _, row := range rows {
		if p, ok := Anchor(row.ID); ok {
			text := row.Answer
			if row.Error != "" {
				text = row.Error
			}
			s.apply(p, row.Kind, text)
		}
	}
	v := s.verdict(nowFn())
	v.Protocol = protocol
	v.Attempts = attempts
	return v
}

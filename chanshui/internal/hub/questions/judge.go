package questions

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/iq"
)

func encodeImage(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

var answerMarker = regexp.MustCompile(`(?i)(最终答案|最后答案|本题答案|答案[:：]|final answer|the answer is)`)

func ExtractCandidate(raw string) string {
	locs := answerMarker.FindAllStringIndex(raw, -1)
	candidate := ""
	if len(locs) > 0 {
		candidate = raw[locs[len(locs)-1][1]:]
		if n := strings.IndexByte(candidate, '\n'); n >= 0 {
			candidate = candidate[:n]
		}
		candidate = strings.TrimLeft(candidate, " \t\r:：")
	}
	if strings.TrimSpace(candidate) == "" {
		lines := strings.Split(raw, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			v := strings.TrimSpace(lines[i])
			if v != "" && !strings.HasPrefix(v, "```") {
				candidate = v
				break
			}
		}
	}
	candidate = strings.TrimSpace(candidate)
	for {
		changed := false
		for _, mark := range []string{"**", "__", "*", "_", "`"} {
			if len(candidate) > 2*len(mark) && strings.HasPrefix(candidate, mark) && strings.HasSuffix(candidate, mark) {
				candidate = strings.TrimSpace(candidate[len(mark) : len(candidate)-len(mark)])
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return candidate
}
func Grade(g Generated, answer string, allowQ4Equivalent ...bool) Judgment {
	q := g.Definition
	if q.Judge.Type == ModelIdentityJudge {
		return gradeModelIdentity(g, answer)
	}
	j := Judgment{Status: "known", Max: q.MaxScore, Tags: []string{}}
	if q.Judge.Type == "manual" {
		j.Status = "unreviewed"
		j.Max = 0
		return j
	}
	if q.Judge.Type == "legacy" {
		answerBody := ""
		if len(allowQ4Equivalent) > 0 && allowQ4Equivalent[0] {
			answerBody = answer
		}
		n, half, tags := iq.JudgeWithAnswer(g.IQ(), answer, answerBody)
		j.Earned = &n
		j.Half = half
		j.Tags = tags
		return j
	}
	v := ExtractCandidate(answer)
	if v == "" {
		j.Status = "unknown"
		j.Reason = "未找到最终答案，请人工查看原文"
		return j
	}
	pass := false
	switch q.Judge.Type {
	case "text":
		pass = v == strings.TrimSpace(g.Expected)
	case "number":
		if !numText.MatchString(v) {
			j.Status = "unknown"
			j.Reason = "最终答案不是完整数值，请人工查看"
			return j
		}
		n, e := strconv.ParseFloat(v, 64)
		want, e2 := strconv.ParseFloat(g.Expected, 64)
		if e != nil || e2 != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			j.Status = "unknown"
			return j
		}
		pass = math.Abs(n-want) <= 1e-9
	case "keywords":
		s := strings.ToLower(v)
		hits := 0
		for _, k := range q.Judge.Required {
			if strings.Contains(s, strings.ToLower(strings.TrimSpace(k))) {
				hits++
			}
		}
		pass = hits > 0
		if q.Judge.Match == "all" {
			pass = hits == len(q.Judge.Required)
		}
		for _, k := range q.Judge.Forbidden {
			if strings.Contains(s, strings.ToLower(strings.TrimSpace(k))) {
				pass = false
			}
		}
		j.Reason = "仅按配置字词匹配，请人工查看原文"
	}
	n := 0
	if pass {
		n = q.MaxScore
	}
	j.Earned = &n
	return j
}
func GradeKnowledge(q Definition, answer string) (string, string) {
	v := strings.ToLower(strings.TrimSpace(ExtractCandidate(answer)))
	if v == "" {
		return "invalid", "未找到明确答案"
	}
	kinds := map[string]bool{}
	for i, c := range objects(q.Parameters["choices"]) {
		if v == strings.ToLower(string(rune('A'+i))) || v == strings.ToLower(strings.TrimSpace(Text(c, "text"))) {
			kinds[Text(c, "kind")] = true
		}
	}
	for _, entry := range []struct{ key, kind string }{{"knowAliases", "know"}, {"wrongAliases", "wrong"}, {"unknownAliases", "unknown"}} {
		for _, a := range Strings(q.Parameters[entry.key]) {
			if v == strings.ToLower(strings.TrimSpace(a)) {
				kinds[entry.kind] = true
			}
		}
	}
	if len(kinds) != 1 {
		return "invalid", "答案未匹配唯一选项或类别"
	}
	for k := range kinds {
		return k, fmt.Sprintf("最终答案明确匹配%s类别", k)
	}
	return "invalid", "答案无效"
}

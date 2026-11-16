package questions

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/kiss-kedaya/chanshui/internal/iq"
)

func Expand(t string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(t, func(s string) string { k := strings.TrimSuffix(strings.TrimPrefix(s, "{{"), "}}"); return values[k] })
}
func (g Generated) Preview() map[string]any {
	out := map[string]any{"definition": g.Definition, "seed": g.Seed, "prompt": g.Prompt, "expected": g.Expected, "tags": g.Tags}
	if len(g.Image) > 0 {
		out["image"] = "data:image/png;base64," + encodeImage(g.Image)
	}
	return out
}
func Generate(q Definition, seed int64) (Generated, error) {
	if e := Validate(q); e != nil {
		return Generated{}, e
	}
	g := Generated{Definition: Clone(q), Seed: seed, Prompt: q.Template, Expected: q.Judge.ExpectedText}
	if q.Judge.Type == "manual" {
		g.Expected = q.Judge.ReferenceAnswer
	}
	r := rand.New(rand.NewSource(seed))
	values := map[string]string{}
	generatedExpected := ""
	switch Text(q.Parameters, "generator") {
	case "color_blind_v1":
		pairs := objects(q.Parameters["pairs"])
		p := pairs[r.Intn(len(pairs))]
		for _, k := range []string{"father", "daughter", "remark"} {
			values[k] = Text(p, k)
		}
		generatedExpected = "非亲生"
	case "candy_classic_v1", "candy_variant_v1":
		circ, star := [3]int{}, [3]int{}
		keys := []string{"ca", "ct", "cm", "sa", "st", "sm"}
		found := false
		for attempt := 0; attempt < 50; attempt++ {
			for i, k := range keys {
				n := Int(q.Parameters, k)
				if Text(q.Parameters, "generator") == "candy_variant_v1" {
					n = Int(q.Parameters, "min") + r.Intn(Int(q.Parameters, "max")-Int(q.Parameters, "min")+1)
				}
				values[k] = strconv.Itoa(n)
				if i < 3 {
					circ[i] = n
				} else {
					star[i-3] = n
				}
			}
			ans := iq.SolveCandy(circ, star)
			if Text(q.Parameters, "generator") == "candy_classic_v1" || ans != 21 {
				generatedExpected = strconv.Itoa(ans)
				found = true
				break
			}
		}
		if !found {
			return Generated{}, fmt.Errorf("糖果参数在50次以内无法生成有效变体，请扩大库存范围")
		}
		g.Tags = []string{fmt.Sprintf("库存:%d,%d,%d:%d,%d,%d", circ[0], circ[1], circ[2], star[0], star[1], star[2])}
	case "socks_v1":
		found := false
		for attempt := 0; attempt < 50; attempt++ {
			n := Int(q.Parameters, "colorsMin") + r.Intn(Int(q.Parameters, "colorsMax")-Int(q.Parameters, "colorsMin")+1)
			if attempt == 49 {
				n = Int(q.Parameters, "colorsMin")
			}
			counts := make([]int, n)
			pairs := 0
			for i := range counts {
				counts[i] = Int(q.Parameters, "stockMin") + r.Intn(Int(q.Parameters, "stockMax")-Int(q.Parameters, "stockMin")+1)
				if attempt == 49 {
					counts[i] = Int(q.Parameters, "stockMin")
				}
				pairs += counts[i] / 2
			}
			p := Int(q.Parameters, "pairsMin") + r.Intn(Int(q.Parameters, "pairsMax")-Int(q.Parameters, "pairsMin")+1)
			if attempt == 49 {
				p = Int(q.Parameters, "pairsMin")
			}
			if pairs < p {
				continue
			}
			worst := iq.SolveSocks(counts, p)
			if worst == 2*p+n-1 {
				continue
			}
			colors := append([]string{}, Strings(q.Parameters["colors"])...)
			r.Shuffle(len(colors), func(i, j int) { colors[i], colors[j] = colors[j], colors[i] })
			parts, ints := []string{}, []string{}
			for i, c := range counts {
				parts = append(parts, fmt.Sprintf("%s色袜子 %d 只", colors[i], c))
				ints = append(ints, strconv.Itoa(c))
			}
			values["color_count"] = strconv.Itoa(n)
			values["stocks"] = strings.Join(parts, "、")
			values["pair_count"] = strconv.Itoa(p)
			generatedExpected = strconv.Itoa(worst)
			g.Tags = []string{fmt.Sprintf("实例:%s:%d", strings.Join(ints, ","), p)}
			found = true
			break
		}
		if !found {
			return Generated{}, fmt.Errorf("袜子参数在50次以内无法生成可行且可区分的题目，请扩大范围")
		}
	case "chart_v1":
		lines := Strings(q.Parameters["lines"])
		expanded := make([]string, 6)
		for i, line := range lines {
			expanded[i] = Expand(line, map[string]string{"bar_count": "40", "last_bar_id": "D40"})
		}
		q2, e := iq.HubChart(seed, expanded)
		if e != nil {
			return Generated{}, e
		}
		g.Prompt = ""
		g.Image = q2.ImagePNG
		generatedExpected = q2.Answer
		g.Tags = q2.Tags
	case "fingerprint_numeric_v1":
		g.Prompt = NumericPrompt(q, 292+r.Intn(41), r)
	case "event_v1":
		var b strings.Builder
		b.WriteString(q.Template)
		b.WriteString("\n")
		for i, c := range objects(q.Parameters["choices"]) {
			fmt.Fprintf(&b, "%c. %s\n", 'A'+i, Text(c, "text"))
			if Text(c, "kind") == "know" {
				g.Expected = string(rune('A' + i))
			}
		}
		b.WriteString("仅凭已有知识回答，禁止联网或工具；最后单独一行以“最终答案: ”给出选项字母。无法确认请选择不知道的选项。")
		g.Prompt = b.String()
	}
	if q.Kind == "iq" {
		g.Prompt = Expand(q.Template, values) + "\n" + AnswerTail
	}
	// Generators always compute their own solution for reproducible parameters,
	// but only the selected local judge decides which answer belongs to this
	// report. A hidden/stale generator checkbox must not override manual mode.
	if q.Kind == "iq" || q.Kind == "vision" {
		switch q.Judge.Type {
		case "manual":
			g.Expected = q.Judge.ReferenceAnswer
		case "text":
			g.Expected = q.Judge.ExpectedText
			if q.Judge.ExpectedFrom == "generator" {
				g.Expected = generatedExpected
			}
		case "number":
			g.Expected = strconv.FormatFloat(q.Judge.ExpectedNumber, 'g', -1, 64)
			if q.Judge.ExpectedFrom == "generator" {
				g.Expected = generatedExpected
			}
		case "legacy":
			if generatedExpected != "" {
				g.Expected = generatedExpected
			}
		}
	}
	return g, nil
}
func Compatible(q Definition) bool {
	for _, p := range Presets() {
		if p.Kind != q.Kind || Text(p.Parameters, "generator") != Text(q.Parameters, "generator") {
			continue
		}
		if q.Kind == "fingerprint_behavior" && Int(p.Parameters, "slot") != Int(q.Parameters, "slot") {
			continue
		}
		a, _ := json.Marshal(struct {
			Template   string
			Parameters map[string]any
		}{q.Template, q.Parameters})
		b, _ := json.Marshal(struct {
			Template   string
			Parameters map[string]any
		}{p.Template, p.Parameters})
		if string(a) == string(b) {
			return true
		}
	}
	return false
}

func NumericPrompt(q Definition, count int, r *rand.Rand) string {
	values := map[string]string{"count": strconv.Itoa(count), "min": "1", "max": "355"}
	for _, pair := range [][2]string{{"opening", "openings"}, {"action", "actions"}, {"ending", "endings"}, {"separatorHint", "separatorHints"}} {
		a := Strings(q.Parameters[pair[1]])
		values[pair[0]] = a[r.Intn(len(a))]
	}
	return Expand(q.Template, values)
}

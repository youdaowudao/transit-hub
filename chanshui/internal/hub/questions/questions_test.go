package questions

import (
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestEveryUpstreamPresetIsEditableDefinitionAndGenerates(t *testing.T) {
	defs := Presets()
	want := 10 + 1 + len(knowledge.ModelAnchors()) + 5 + 1 + len(fingerprint.BehaviorProbes)
	if len(defs) != want {
		t.Fatalf("preset coverage %d want %d", len(defs), want)
	}
	seen := map[string]bool{}
	for _, q := range defs {
		if seen[q.ID] {
			t.Fatalf("duplicate %s", q.ID)
		}
		seen[q.ID] = true
		t.Run(q.ID, func(t *testing.T) {
			if e := Validate(q); e != nil {
				t.Fatal(e)
			}
			g, e := Generate(q, 73)
			if e != nil {
				t.Fatal(e)
			}
			if q.Kind != "vision" && g.Prompt == "" && q.Kind != "cache" {
				t.Fatal("missing actual prompt")
			}
			if q.Kind == "vision" && len(g.Image) == 0 {
				t.Fatal("missing real image")
			}
			if q.Parameters == nil {
				t.Fatal("hidden parameters")
			}
		})
	}
	for _, id := range []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8", "Q9", "vision", "tools", "cache", "injection", "web", "thinking", "fingerprint:numeric"} {
		if !seen[id] {
			t.Errorf("missing %s", id)
		}
	}
}
func TestLegacyMaximumCannotBecome600Percent(t *testing.T) {
	q, _ := Preset("Q1")
	q.MaxScore = 1
	if e := Validate(q); e == nil {
		t.Fatal("legacy adjustable maximum accepted")
	}
	q.MaxScore = 6
	q.Template += "新的题意"
	if e := Validate(q); e == nil {
		t.Fatal("changed body still legacy")
	}
	q.Judge.Type = "manual"
	q.MaxScore = 0
	if e := Validate(q); e != nil {
		t.Fatal(e)
	}
}
func custom(kind string) Definition {
	return Definition{ID: "custom:" + strings.Repeat("a", 32), Title: "新增题", Kind: "iq", Enabled: true, Order: 1, MaxScore: 7, Template: "只输出答案", Parameters: map[string]any{"generator": "static"}, Judge: Judge{Type: kind, ExpectedText: "a-b!", ExpectedNumber: 2.5}}
}
func TestGenericGradesOnlyFinalCandidateAndKeepsInternalPunctuation(t *testing.T) {
	q := custom("text")
	g, e := Generate(q, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		answer string
		want   int
	}{{"推理：其他\n最终答案: **a-b!**", 7}, {"a-b", 0}, {"a-b!\n最终答案: 错误", 0}} {
		j := Grade(g, v.answer)
		if j.Earned == nil || *j.Earned != v.want {
			t.Errorf("%q: %+v", v.answer, j)
		}
	}
	q = custom("number")
	g, _ = Generate(q, 1)
	for _, v := range []struct {
		answer string
		known  bool
		points int
	}{{"最终答案: 2.5e0", true, 7}, {"推理得到2.5\n最终答案: 3", true, 0}, {"最终答案: 2.5个", false, 0}, {"最终答案: NaN", false, 0}} {
		j := Grade(g, v.answer)
		if (j.Earned != nil) != v.known || j.Earned != nil && *j.Earned != v.points {
			t.Errorf("%q: %+v", v.answer, j)
		}
	}
}
func TestKeywordsForbiddenWinsAndManualNeverGetsAutomaticPoints(t *testing.T) {
	q := custom("keywords")
	q.Judge.Required = []string{"Yes"}
	q.Judge.Forbidden = []string{"No"}
	q.Judge.Match = "any"
	g, e := Generate(q, 1)
	if e != nil {
		t.Fatal(e)
	}
	j := Grade(g, "最终答案: YES NO")
	if j.Earned == nil || *j.Earned != 0 {
		t.Fatal(j)
	}
	q.Judge.Type = "manual"
	q.Judge.ReferenceAnswer = "人工参考"
	q.MaxScore = 0
	g, e = Generate(q, 1)
	if e != nil {
		t.Fatal(e)
	}
	if g.Expected != "人工参考" || Grade(g, "人工参考").Earned != nil {
		t.Fatal("manual reference lost or auto-graded")
	}
}
func TestTypedParametersRejectUnknownMissingAndImpossibleWithoutLoop(t *testing.T) {
	for _, q := range Presets() {
		q = Clone(q)
		q.Parameters["unknownSetting"] = true
		if Validate(q) == nil {
			t.Errorf("unknown param allowed %s", q.ID)
		}
	}
	q, _ := Preset("Q8")
	q.Parameters["min"] = 10
	q.Parameters["max"] = 4
	if Validate(q) == nil {
		t.Fatal("reversed range accepted")
	}
	q, _ = Preset("Q7")
	q.Parameters["colorsMin"] = 3
	q.Parameters["colorsMax"] = 3
	q.Parameters["stockMin"] = 2
	q.Parameters["stockMax"] = 2
	q.Parameters["pairsMin"] = 4
	q.Parameters["pairsMax"] = 4
	if _, e := Generate(q, 1); e == nil {
		t.Fatal("impossible instance should fail bounded generation")
	}
	q, _ = Preset("tools")
	q.Template = "只说调用了工具"
	if Validate(q) == nil {
		t.Fatal("required tool tokens missing accepted")
	}
}
func TestChartRejectsExistingGlyphsOutsidePanelAndMissingGlyphs(t *testing.T) {
	q, _ := Preset("vision")
	q = Clone(q)
	q.Judge.Type = "number"
	q.Judge.ExpectedFrom = "generator"
	lines := Strings(q.Parameters["lines"])
	lines[0] = strings.Repeat("ONE INTEGER ", 200)
	q.Parameters["lines"] = lines
	if _, e := Generate(q, 1); e == nil {
		t.Fatal("overwide known glyphs accepted")
	}
	lines[0] = "ONE\nINTEGER"
	if _, e := Generate(q, 1); e == nil {
		t.Fatal("newline accepted")
	}
	lines[0] = "龘"
	if _, e := Generate(q, 1); e == nil {
		t.Fatal("missing glyph accepted")
	}
	q, _ = Preset("vision")
	g, e := Generate(q, 1)
	if e != nil {
		t.Fatal(e)
	}
	q.Parameters["lines"] = Strings(q.Parameters["lines"])
	got, e := Generate(q, 1)
	if e != nil || g.Expected != got.Expected || string(g.Image) != string(got.Image) {
		t.Fatal("seed did not reproduce chart")
	}
}
func TestKnowledgeExactMatchingAndIndependentFingerprintCompatibility(t *testing.T) {
	var q Definition
	for _, d := range Presets() {
		if d.Kind == "knowledge" {
			q = d
			break
		}
	}
	g, e := Generate(q, 1)
	if e != nil {
		t.Fatal(e)
	}
	kind, _ := GradeKnowledge(q, "最终答案: "+g.Expected)
	if kind != "know" {
		t.Fatal(kind)
	}
	kind, _ = GradeKnowledge(q, "我也许知道 "+g.Expected)
	if kind != "invalid" {
		t.Fatal("semantic guessing accepted")
	}
	n, _ := Preset("fingerprint:numeric")
	b, _ := Preset("fingerprint:behavior:1")
	if !Compatible(n) || !Compatible(b) {
		t.Fatal("default incompatible")
	}
	b.Template += "修改"
	if Compatible(b) || !Compatible(n) {
		t.Fatal("behavior edit contaminated numeric signature")
	}
}
func TestParameterizedLocalAnswersMatchSolver(t *testing.T) {
	for i := int64(1); i < 10; i++ {
		for _, id := range []string{"Q6", "Q7", "Q8"} {
			q, _ := Preset(id)
			g, e := Generate(q, i)
			if e != nil {
				t.Fatal(e)
			}
			j := Grade(g, "最终答案: "+g.Expected)
			if j.Earned == nil || *j.Earned != 16 {
				t.Fatalf("%s seed%d expected %s: %+v", id, i, g.Expected, j)
			}
		}
	}
}

func TestOriginalOlympicsTwoKnownChoicesRetainedButNewMultipleKnownRejected(t *testing.T) {
	q, _ := Preset("knowledge:olympics")
	q.Title = "改标题不改选项"
	if _, e := Generate(q, 1); e != nil {
		t.Fatal(e)
	}
	for _, answer := range []string{"A", "B"} {
		kind, _ := GradeKnowledge(q, "最终答案: "+answer)
		if kind != "know" {
			t.Fatal("original known choice rejected", answer, kind)
		}
	}
	q = Clone(q)
	choices := q.Parameters["choices"].([]any)
	choices[0].(map[string]any)["text"] = "新的选项"
	if Validate(q) == nil {
		t.Fatal("changed multiple-known choices accepted")
	}
}

func TestNestedParameterUnknownFieldsAreRejected(t *testing.T) {
	for _, id := range []string{"Q4", "knowledge:olympics"} {
		q, _ := Preset(id)
		q = Clone(q)
		key := "pairs"
		if q.Kind == "knowledge" {
			key = "choices"
		}
		items := q.Parameters[key].([]any)
		items[0].(map[string]any)["unregistered_field"] = "must not silently ignore"
		if Validate(q) == nil {
			t.Fatal("nested unknown accepted", id)
		}
	}
}
func TestFingerprintCompatibilityUsesTemplatesAndParametersRatherThanOldID(t *testing.T) {
	for _, id := range []string{"fingerprint:numeric", "fingerprint:behavior:1", "fingerprint:behavior:2", "fingerprint:behavior:3"} {
		q, _ := Preset(id)
		q.ID = "custom:" + strings.Repeat("f", 32)
		if !Compatible(q) {
			t.Fatal("unmodified new copy incorrectly incompatible", id)
		}
		q.Template += "改动"
		if Compatible(q) {
			t.Fatal("modified prompt remained compatible", id)
		}
	}
}

func TestDefaultHubChartPreservesOriginalRenderedImage(t *testing.T) {
	q, _ := Preset("vision")
	g, e := Generate(q, 73)
	if e != nil {
		t.Fatal(e)
	}
	old := g.IQ()
	old.ImagePNG = nil
	old = iq.WithChartImage(old)
	if string(old.ImagePNG) != string(g.Image) {
		t.Fatal("default image rendering drifted from frozen upstream")
	}
}

func TestNestedParameterFieldsKeepDeclaredTypes(t *testing.T) {
	cases := []struct {
		id, key string
		value   any
	}{{"cache", "prefixHeading", 12}, {"web", "deepSeekQuery", false}, {"fingerprint:behavior:1", "slot", "zero"}}
	for _, tc := range cases {
		q, _ := Preset(tc.id)
		q = Clone(q)
		q.Parameters[tc.key] = tc.value
		if Validate(q) == nil {
			t.Fatal("wrong parameter type accepted", tc.id, tc.key)
		}
	}
	q, _ := Preset("Q4")
	q = Clone(q)
	q.Parameters["pairs"].([]any)[0].(map[string]any)["remark"] = 42
	if Validate(q) == nil {
		t.Fatal("non-text remark accepted")
	}
}

func TestAllParameterGeneratorsRespectExplicitJudgeAnswerSource(t *testing.T) {
	for _, id := range []string{"Q4", "Q6", "Q7", "Q8", "vision"} {
		t.Run(id, func(t *testing.T) {
			base, _ := Preset(id)
			baseline, e := Generate(base, 73)
			if e != nil {
				t.Fatal(e)
			}
			cases := []struct {
				name     string
				judge    Judge
				max      int
				expected string
				grade    bool
			}{
				{"manual-reference", Judge{Type: "manual", ReferenceAnswer: "用户填写的人工参考", ExpectedFrom: "generator"}, 0, "用户填写的人工参考", false},
				{"manual-empty", Judge{Type: "manual"}, 0, "", false},
				{"explicit-text", Judge{Type: "text", ExpectedText: "用户明确指定的文本答案"}, 7, "用户明确指定的文本答案", true},
				{"explicit-number", Judge{Type: "number", ExpectedNumber: 987654.25}, 7, "987654.25", true},
				{"explicit-zero", Judge{Type: "number", ExpectedNumber: 0}, 7, "0", true},
				{"generated-text", Judge{Type: "text", ExpectedText: "不应覆盖选中的生成器答案", ExpectedFrom: "generator"}, 7, baseline.Expected, true},
			}
			if id != "Q4" {
				cases = append(cases, struct {
					name     string
					judge    Judge
					max      int
					expected string
					grade    bool
				}{"generated-number", Judge{Type: "number", ExpectedNumber: 987654.25, ExpectedFrom: "generator"}, 7, baseline.Expected, true})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					q := Clone(base)
					q.Judge = tc.judge
					q.MaxScore = tc.max
					g, e := Generate(q, 73)
					if e != nil {
						t.Fatal(e)
					}
					if g.Expected != tc.expected {
						t.Fatalf("configured answer overwritten: got %q want %q", g.Expected, tc.expected)
					}
					j := Grade(g, "最终答案: "+tc.expected)
					if tc.grade {
						if j.Earned == nil || *j.Earned != tc.max {
							t.Fatalf("configured answer was not graded: %+v", j)
						}
					} else if j.Earned != nil || j.Status != "unreviewed" {
						t.Fatalf("manual answer was automatically graded: %+v", j)
					}
				})
			}
		})
	}
}

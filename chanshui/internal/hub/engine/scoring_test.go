package engine

import (
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/iq"
	"reflect"
	"testing"
)

func points(n int) *int { return &n }
func TestSelectedTotalMatchesOriginalAPIIncludingPartialResults(t *testing.T) {
	q, _ := questions.Preset("Q2")
	tools, _ := questions.Preset("tools")
	defs := []questions.Generated{{Definition: q}, {Definition: tools}}
	results := map[string]itemResult{q.ID: {Judgment: questions.Judgment{Status: "known", Max: 5, Earned: points(5)}}, tools.ID: {Evidence: map[string]any{"pass": false, "status": "fail"}, Judgment: questions.Judgment{Status: "known", Max: 1, Earned: points(0)}}}
	for _, failure := range []bool{false, true} {
		tool := map[string]any{"pass": false, "status": "fail"}
		if failure {
			tool = map[string]any{"error": "isolated upstream failure"}
			results[tools.ID] = itemResult{Error: "isolated upstream failure", Evidence: tool, Judgment: questions.Judgment{Status: "unknown"}}
		}
		original := api.AssembleVerdict(api.Verdict{Tools: tool}, []iq.Question{defs[0].IQ()}, map[string]api.QuestionResult{q.ID: {ID: q.ID, Title: q.Title, Max: 5, Earned: 5}}, "fixture", map[string]bool{"iq": true, "tools": true})
		summary := summarize(defs, results)
		if !reflect.DeepEqual(summary["total"], asMap(original.Total)) || summary["iqScore"] != float64(100) {
			t.Fatalf("original partial scoring diverged: got %v original %v", summary, original.Total)
		}
	}
}
func TestManualOnlyNeverCreatesAutomaticApplicableParts(t *testing.T) {
	q, _ := questions.Preset("Q2")
	q.Judge.Type = "manual"
	q.MaxScore = 0
	summary := summarize([]questions.Generated{{Definition: q}}, map[string]itemResult{q.ID: {Judgment: questions.Judgment{Status: "unreviewed"}}})
	for _, p := range summary["parts"].([]any) {
		if p.(map[string]any)["applicable"] == true {
			t.Fatal("manual question acquired an automatic score", summary)
		}
	}
	if summary["iqScore"] != nil {
		t.Fatal("manual-only question received IQ score", summary)
	}
}
func TestVisionKeepsOriginalRecognitionAndAnswerHalves(t *testing.T) {
	q, _ := questions.Preset("vision")
	defs := []questions.Generated{{Definition: q}}
	for _, failure := range []bool{false, true} {
		question := defs[0].IQ()
		answer := api.QuestionResult{ID: "vision", Max: 20, Answer: "0"}
		result := itemResult{Recognized: true, Judgment: questions.Judgment{Status: "known", Max: 20, Earned: points(0)}}
		if failure {
			answer.Error = "请求未完成"
			result.Error = answer.Error
			result.Judgment.Status = "unknown"
		}
		evidence := api.VisionResult(question, answer, nil)
		result.Evidence = evidence
		original := api.AssembleVerdict(api.Verdict{Vision: evidence}, nil, nil, "fixture", map[string]bool{"vision": true})
		got := summarize(defs, map[string]itemResult{q.ID: result})
		if !reflect.DeepEqual(got["total"], asMap(original.Total)) {
			t.Fatal("vision score differs from original", got, original.Total)
		}
	}
}

func TestBehaviorSummaryUsesThreeCompatibleUniqueSlotsRegardlessOfIDs(t *testing.T) {
	defs := []questions.Generated{}
	results := map[string]itemResult{}
	for i := 1; i <= 3; i++ {
		q, _ := questions.Preset(fmt.Sprintf("fingerprint:behavior:%d", i))
		q.ID = fmt.Sprintf("custom:%032x", i)
		defs = append(defs, questions.Generated{Definition: q})
		results[q.ID] = itemResult{Answer: "I cannot promise legal outcomes.\n- First\n- Second"}
	}
	summary := map[string]any{}
	addBehaviorSummary(summary, defs, results)
	if _, ok := summary["behavior"].(*fingerprint.BehaviorResult); !ok {
		t.Fatal("new IDs blocked matching templates", summary)
	}
	dup := append([]questions.Generated{}, defs...)
	dup[2].Definition = questions.Clone(dup[0].Definition)
	dup[2].Definition.ID = "custom:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	results[dup[2].Definition.ID] = itemResult{Answer: "sample"}
	summary = map[string]any{}
	addBehaviorSummary(summary, dup, results)
	if summary["behavior"].(map[string]any)["compatible"] != false {
		t.Fatal("duplicate slot attributed")
	}
	summary = map[string]any{}
	addBehaviorSummary(summary, defs[:2], results)
	if summary["behavior"].(map[string]any)["compatible"] != false {
		t.Fatal("missing slot attributed")
	}
	defs[0].Definition.Template += "改题"
	summary = map[string]any{}
	addBehaviorSummary(summary, defs, results)
	if summary["behavior"].(map[string]any)["compatible"] != false {
		t.Fatal("changed template attributed")
	}
	n, _ := questions.Preset("fingerprint:numeric")
	if !questions.Compatible(n) {
		t.Fatal("behavior state invalidated numeric")
	}
}

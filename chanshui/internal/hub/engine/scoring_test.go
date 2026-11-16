package engine

import (
	"fmt"
	"github.com/kiss-kedaya/chanshui/internal/fingerprint"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/integrity"
	"testing"
)

func points(n int) *int { return &n }
func TestSelectedTotalReusesComposeAndHidesUnknownRatherThanZero(t *testing.T) {
	iq, _ := questions.Preset("Q2")
	tools, _ := questions.Preset("tools")
	manual := questions.Clone(iq)
	manual.ID = "custom:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manual.Judge.Type = "manual"
	manual.MaxScore = 0
	numeric, _ := questions.Preset("fingerprint:numeric")
	defs := []questions.Generated{{Definition: iq}, {Definition: tools}, {Definition: manual}, {Definition: numeric}}
	results := map[string]itemResult{iq.ID: {Judgment: questions.Judgment{Status: "known", Max: 5, Earned: points(5)}}, tools.ID: {Judgment: questions.Judgment{Status: "known", Max: 1, Earned: points(0)}}, manual.ID: {Judgment: questions.Judgment{Status: "unreviewed"}}, numeric.ID: {Judgment: questions.Judgment{Status: "not_applicable"}}}
	summary := summarize(defs, results)
	total := summary["total"].(map[string]any)
	want := integrity.Compose([]integrity.Part{{Key: "iq", Applicable: true, Known: true, Points: 100, MaxPoints: 100}, {Key: "tools", Applicable: true, Known: true, Points: 0, MaxPoints: 1}})
	if total["score"] != want.Score || summary["iqScore"] != 100 {
		t.Fatal("weights/score diverged", total, want)
	}
	results[tools.ID] = itemResult{Judgment: questions.Judgment{Status: "unknown", Max: 1}}
	summary = summarize(defs, results)
	total = summary["total"].(map[string]any)
	if total["score"] != nil || summary["totalScore"] != nil || total["complete"] != false || summary["iqScore"] != 100 {
		t.Fatal("unknown became zero or hid completed IQ", summary)
	}
}
func TestManualKnowledgeBehaviorNeverCreateAutomaticFullScore(t *testing.T) {
	q, _ := questions.Preset("Q2")
	q.Judge.Type = "manual"
	q.MaxScore = 0
	summary := summarize([]questions.Generated{{Definition: q}}, map[string]itemResult{q.ID: {Judgment: questions.Judgment{Status: "unreviewed"}}})
	total := summary["total"].(map[string]any)
	if total["score"] != nil {
		t.Fatal("manual-only report given score", total)
	}
}
func TestVisionKeepsOriginalRecognitionAndAnswerHalves(t *testing.T) {
	q, _ := questions.Preset("vision")
	defs := []questions.Generated{{Definition: q}}
	summary := summarize(defs, map[string]itemResult{q.ID: {Judgment: questions.Judgment{Status: "known", Max: 20, Earned: points(0)}, Recognized: true}})
	total := summary["total"].(map[string]any)
	if total["score"] != 50 {
		t.Fatal("vision recognition half changed", total)
	}
	summary = summarize(defs, map[string]itemResult{q.ID: {Judgment: questions.Judgment{Status: "unknown", Max: 20}}})
	if summary["total"].(map[string]any)["score"] != nil {
		t.Fatal("unfinished vision scored")
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

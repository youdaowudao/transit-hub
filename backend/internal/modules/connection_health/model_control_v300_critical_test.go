package connection_health

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

// Authored by the primary agent before implementation. The JSON contract lets
// the unchanged implementation run and fail on business results, not compilation.
func v300CriticalRound(t *testing.T, questions []map[string]any) modelControlRound {
	t.Helper()
	value := map[string]any{"batchId": "v300-critical", "source": "manual", "createdAt": time.Now(), "questions": questions, "duplicateQuestionIds": []string{}}
	for _, key := range []string{"correct", "incorrect", "unreviewed", "failed", "cancelled"} {
		total := 0
		for _, q := range questions {
			if n, ok := q[key].(int); ok {
				total += n
			}
		}
		value[key] = total
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var round modelControlRound
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatal(err)
	}
	return round
}

func v300CriticalQuestion(id string, correct, incorrect, unreviewed, failed, cancelled int) map[string]any {
	return map[string]any{"questionId": id, "questionName": id, "questionBody": "same content", "correct": correct, "incorrect": incorrect, "unreviewed": unreviewed, "failed": failed, "cancelled": cancelled}
}

func TestModelControlV300CriticalPerQuestionBusinessResults(t *testing.T) {
	q := v300CriticalQuestion
	for _, tc := range []struct {
		name      string
		questions []map[string]any
		threshold int
		decision  string
		score     *float64
	}{
		{"one-question-three-answers", []map[string]any{q("A", 3, 0, 0, 0, 0)}, 50, "usable", floatPtrV300(100)},
		{"two-questions-three-each", []map[string]any{q("A", 3, 0, 0, 0, 0), q("B", 3, 0, 0, 0, 0)}, 50, "usable", floatPtrV300(100)},
		{"cannot-pool-nine-and-one", []map[string]any{q("A", 9, 0, 0, 0, 0), q("B", 0, 1, 0, 0, 0)}, 50, "insufficient", floatPtrV300(50)},
		{"equal-question-weights", []map[string]any{q("A", 10, 0, 0, 0, 0), q("B", 0, 3, 0, 7, 0), q("C", 0, 3, 0, 7, 0)}, 50, "close_recommended", floatPtrV300(100.0 / 3)},
		{"zero-sample-is-not-ignored", []map[string]any{q("A", 3, 0, 0, 0, 0), q("B", 0, 0, 0, 3, 0)}, 50, "insufficient", nil},
		{"failed-cancelled-not-quality", []map[string]any{q("A", 2, 0, 0, 1, 1)}, 50, "insufficient", floatPtrV300(100)},
		{"unreviewed-before-insufficient", []map[string]any{q("A", 2, 0, 1, 0, 0)}, 50, "awaiting_review", floatPtrV300(100)},
		{"exact-equality-usable", []map[string]any{q("A", 3, 0, 0, 0, 0), q("B", 0, 3, 0, 0, 0)}, 50, "usable", floatPtrV300(50)},
		{"per-question-not-per-question-pass-line", []map[string]any{q("A", 3, 0, 0, 0, 0), q("B", 1, 2, 0, 0, 0)}, 50, "usable", floatPtrV300(200.0 / 3)},
		{"same-body-distinct-ids-remain-distinct", []map[string]any{q("A", 3, 0, 0, 0, 0), q("B", 0, 1, 0, 0, 0)}, 50, "insufficient", floatPtrV300(50)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			round := v300CriticalRound(t, tc.questions)
			got := evaluateModelControlDecision(&round, ModelControlRule{MinJudgedAnswers: 3, MinAccuracyPercent: tc.threshold})
			if got.Decision != tc.decision {
				t.Errorf("decision=%s want=%s", got.Decision, tc.decision)
			}
			if tc.score == nil {
				if got.AccuracyPercent != nil {
					t.Errorf("zero-sample score=%v must be unavailable", *got.AccuracyPercent)
				}
			} else if got.AccuracyPercent == nil || math.Abs(*got.AccuracyPercent-*tc.score) > 1e-10 {
				t.Errorf("score=%v want=%v", got.AccuracyPercent, *tc.score)
			}
		})
	}
	legacy := v300CriticalRound(t, []map[string]any{q("A", 10, 0, 0, 0, 0)})
	if got := evaluateModelControlDecision(&legacy, ModelControlRule{MinJudgedAnswers: 11, MinAccuracyPercent: 50}); got.Decision != "insufficient" {
		t.Fatal("legacy >10 must remain insufficient", got)
	}
}
func floatPtrV300(v float64) *float64 { return &v }

func TestModelControlV300CriticalManualCloseAndAddWithoutEvidence(t *testing.T) {
	for _, op := range []string{"close", "add"} {
		t.Run(op, func(t *testing.T) {
			f := newC3REDFixture(t)
			model := "A"
			if op == "add" {
				model = "NEW"
			}
			f.manage("1", model)
			f.verify("1")
			item := f.item("1", model)
			if item["decision"] != "no_evidence" {
				t.Fatal("fixture must have no quality evidence", item)
			}
			preview := f.expect("POST", "model-control/preview", map[string]any{"targetId": c3REDTarget("1"), "modelName": model, "operation": op, "basis": item["basis"]}, 200)
			if preview["blockReasonKey"] != "" && preview["blockReasonKey"] != nil {
				t.Fatal("manual operation must permit preview without quality", preview)
			}
			body := map[string]any{"targetId": c3REDTarget("1"), "modelName": model, "basis": preview["item"].(map[string]any)["basis"], "planFingerprint": preview["planFingerprint"]}
			code, _ := f.request("POST", "model-control/"+op, body)
			if code == 200 || f.writeCount() != 0 {
				t.Fatal("missing second confirmation wrote main site", code)
			}
			body["confirmWithoutEvidence"] = true
			code, result := f.request("POST", "model-control/"+op, body)
			if code != 200 || result["outcome"] != map[string]string{"close": "closed", "add": "added"}[op] || f.writeCount() != 1 {
				t.Fatal("confirmed manual action must write exactly once", code, result, f.writeCount())
			}
		})
	}
}

func TestModelControlV300CriticalOldEvidenceCannotTriggerAuto(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Minute)
	migrated := now.Add(-time.Second)
	item := ModelControlItem{Decision: "close_recommended", Control: modelControlControl{Observation: modelControlObservation{State: "serving"}}}
	in := modelControlAutoStatusInput{Now: now, Settings: ModelControlAutoSettings{AutoCloseEnabled: true, AutoCloseEnabledAt: &old}, RuleSavedAt: &migrated, Item: item, Object: modelControlTarget{UpdatedAt: old}, EvidenceAt: &old}
	if got := modelControlAutoStatus(in); got.State != "waiting" {
		t.Fatal("migration must block old evidence", got)
	}
	fresh := now
	in.EvidenceAt = &fresh
	if got := modelControlAutoStatus(in); got.State != "eligible" {
		t.Fatal("new evidence must pass original time barrier", got)
	}
}

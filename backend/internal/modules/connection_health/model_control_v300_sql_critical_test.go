package connection_health

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestModelControlV300CriticalBothSQLPathsAndScopeIsolation(t *testing.T) {
	f := newC3REDFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, model := range []string{"A", "B", "C", "D"} {
		f.manage("1", model)
	}
	// Each logical set uses the same question IDs; neither another model, target,
	// user nor older batch is allowed to repair a current question's sample gap.
	sets := []struct {
		user, target, model, batch, q, body, judgment, status string
		n                                                     int
		at                                                    time.Time
	}{
		{c3REDUser, c3REDTarget("1"), "A", "new-A", "one", " body\r\nline ", "correct", "succeeded", 9, now},
		{c3REDUser, c3REDTarget("1"), "A", "new-A", "two", "body\nline", "incorrect", "succeeded", 1, now},
		{c3REDUser, c3REDTarget("1"), "A", "old-A", "two", "old snapshot", "correct", "succeeded", 3, now.Add(-time.Minute)},
		{c3REDUser, c3REDTarget("1"), "B", "new-B", "one", "unique", "correct", "succeeded", 10, now},
		{c3REDUser, c3REDTarget("1"), "B", "new-B", "two", "different", "incorrect", "succeeded", 3, now},
		{c3REDUser, c3REDTarget("1"), "B", "new-B", "three", "third", "incorrect", "succeeded", 3, now},
		{c3REDUser, c3REDTarget("1"), "C", "new-C", "one", "only", "correct", "succeeded", 3, now},
		{c3REDUser, c3REDTarget("1"), "D", "new-D", "one", "some", "correct", "succeeded", 3, now},
		{c3REDUser, c3REDTarget("1"), "D", "new-D", "two", "failed", "", "failed", 3, now},
		{"another-user", c3REDTarget("1"), "A", "new-A", "two", "other user", "correct", "succeeded", 10, now},
		{c3REDUser, c3REDTarget("2"), "A", "new-A", "two", "other target", "correct", "succeeded", 10, now},
	}
	for si, s := range sets {
		for n := 0; n < s.n; n++ {
			_, err := f.pool.Exec(t.Context(), `INSERT INTO connection_health_question_answer_records(id,user_id,target_id,batch_id,model_name,question_id,question_name,question_body,status,answer_judgment,created_at,completed_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,NULLIF($9,''),$10,$10,$10)`, fmt.Sprintf("v300-%d-%d", si, n), s.user, s.target, s.batch, s.model, s.q, s.body, s.status, s.judgment, s.at)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	repo := f.service.modelControls.(*Repository)
	pairs := []modelControlPair{}
	for _, model := range []string{"A", "B", "C", "D"} {
		pairs = append(pairs, modelControlPair{TargetID: c3REDTarget("1"), ModelName: model})
	}
	rounds, err := repo.ListModelControlRounds(t.Context(), c3REDUser, pairs)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model, decision string
		questions       int
		score           *float64
	}{{"A", "insufficient", 2, floatPtrV300(50)}, {"B", "close_recommended", 3, floatPtrV300(100.0 / 3)}, {"C", "usable", 1, floatPtrV300(100)}, {"D", "insufficient", 2, nil}} {
		rs := rounds[modelControlPair{TargetID: c3REDTarget("1"), ModelName: tc.model}]
		if len(rs) == 0 {
			t.Fatal("round missing", tc.model)
		}
		decision := evaluateModelControlDecision(&rs[0], ModelControlRule{MinAccuracyPercent: 50, MinJudgedAnswers: 3})
		if decision.Decision != tc.decision {
			t.Errorf("model=%s decision=%s want=%s", tc.model, decision.Decision, tc.decision)
		}
		encoded, _ := json.Marshal(rs[0])
		var raw map[string]any
		_ = json.Unmarshal(encoded, &raw)
		questions, ok := raw["questions"].([]any)
		if !ok || len(questions) != tc.questions {
			t.Errorf("model=%s per-question contract=%v want count=%d", tc.model, raw["questions"], tc.questions)
		}
		if tc.model == "A" {
			ids, ok := raw["duplicateQuestionIds"].([]any)
			if !ok || len(ids) != 2 {
				t.Errorf("same snapshot bodies must warn for both IDs: %v", raw["duplicateQuestionIds"])
			}
		}
		if tc.score == nil {
			if rs[0].AccuracyPercent != nil {
				t.Error("zero sample SQL score must be null")
			}
		} else if rs[0].AccuracyPercent == nil || *rs[0].AccuracyPercent != *tc.score {
			t.Errorf("SQL score for %s =%v expected %v", tc.model, rs[0].AccuracyPercent, *tc.score)
		}
	}
	sqlSummary, err := repo.queryModelControlAccountSummaries(t.Context(), c3REDUser, c3REDWorkspace, []string{c3REDTarget("1")})
	if err != nil {
		t.Fatal(err)
	}
	serviceSummary, err := listModelControlAccountSummaries(t.Context(), repo, c3REDUser, c3REDWorkspace, []string{c3REDTarget("1")})
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range serviceSummary {
		for n := range summary.Models {
			if at := summary.Models[n].CheckedAt; at != nil {
				utc := at.UTC()
				summary.Models[n].CheckedAt = &utc
			}
		}
	}
	a, _ := json.Marshal(sqlSummary)
	b, _ := json.Marshal(serviceSummary)
	if string(a) != string(b) {
		t.Fatalf("quality projections disagree SQL=%s service=%s", a, b)
	}
	want := map[string]string{"A": "insufficient", "B": "close_recommended", "C": "usable", "D": "insufficient"}
	for _, m := range sqlSummary[c3REDTarget("1")].Models {
		if m.Decision != want[m.ModelName] {
			t.Errorf("summary %s decision=%s want=%s", m.ModelName, m.Decision, want[m.ModelName])
		}
	}
}

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
)

func TestJudgeItemRejectsIneligibleReportsAndRollsBackOnWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name, status, item, model, answer, judgment string
		want                                        int
	}{
		{"queued", "queued", "Q10", "gpt-6.2-sol", "原回答", "unknown", 409},
		{"running", "running", "Q10", "gpt-6.2-sol", "原回答", "unknown", 409},
		{"auto zero", "partial", "Q10", "gpt-6.2-sol", "我是 GPT-5", "known", 422},
		{"ordinary question", "partial", "Q2", "gpt-6.2-sol", "原回答", "unknown", 422},
		{"no answer", "partial", "Q10", "gpt-6.2-sol", " ", "unknown", 422},
		{"truncated answer", "partial", "Q10", "gpt-6.2-sol", "被截断的回答", "unknown", 422},
		{"other model", "partial", "Q10", "claude-sonnet", "原回答", "unknown", 422},
		{"summary write fails", "partial", "Q10", "gpt-6.2-sol", "原回答", "unknown", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, id := t.Context(), strings.Repeat("a", 32)
			if _, err := s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: tc.model,
				SelectedQuestionIDs: []string{tc.item}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal(err)
			}
			before := fmt.Sprintf(`{"answer":%q,"attempts":3,"metrics":{"input":5},"judgment":{"status":%q,"max":5,"earned":0,"reason":"原判原因"}}`, tc.answer, tc.judgment)
			if _, err := s.db.Exec(`UPDATE hub_items SET result_json=?,status='done',attempts=3 WHERE report_id=?`, before, id); err != nil {
				t.Fatal(err)
			}
			if tc.name == "truncated answer" {
				before = strings.TrimSuffix(before, "}") + `,"error":"上游截断了回答"}`
				if _, err := s.db.Exec(`UPDATE hub_items SET result_json=?,status='error' WHERE report_id=?`, before, id); err != nil {
					t.Fatal(err)
				}
			}
			const beforeSummary = `{"complete":false,"attempts":3,"marker":"原汇总"}`
			if _, err := s.db.Exec(`UPDATE hub_reports SET status=?,summary_json=? WHERE report_id=?`, tc.status, beforeSummary, id); err != nil {
				t.Fatal(err)
			}
			if tc.want == 500 {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_summary BEFORE UPDATE OF summary_json ON hub_reports BEGIN SELECT RAISE(ABORT,'isolated test write failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			rescored := false
			_, err = s.JudgeItem(ctx, testOwner, JudgeRequest{ReportID: id, ItemID: tc.item, Verdict: "pass"}, func(_ []questions.Generated, items map[string]Item, _, _ string) (map[string]any, error) {
				rescored = true
				var value struct{ Judgment questions.Judgment }
				if err := json.Unmarshal(items[tc.item].Result, &value); err != nil || value.Judgment.Earned == nil || *value.Judgment.Earned != 5 {
					t.Fatalf("scorer did not see the updated item: %+v (%v)", value, err)
				}
				return map[string]any{"complete": true, "verdict": map[string]any{"iq": nil}}, nil
			})
			var fault *Error
			if !errors.As(err, &fault) || fault.Status != tc.want || (tc.want == 500) != rescored {
				t.Fatalf("wanted %d, got %v; rescore=%v", tc.want, err, rescored)
			}
			var after, status, summary string
			if err := s.db.QueryRow(`SELECT i.result_json,r.status,r.summary_json FROM hub_items i JOIN hub_reports r USING(report_id) WHERE report_id=?`, id).Scan(&after, &status, &summary); err != nil {
				t.Fatal(err)
			}
			if after != before || status != tc.status || summary != beforeSummary {
				t.Fatalf("failed judgment changed saved data: %s %s %s", after, status, summary)
			}
		})
	}
}

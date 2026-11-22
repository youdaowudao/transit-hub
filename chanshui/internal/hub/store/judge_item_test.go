package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/api"
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

func TestJudgeCacheItemEligibilityAndState(t *testing.T) {
	for _, tc := range []struct {
		name, version, reportStatus, itemStatus, judgment, requestError string
		want                                                            int
	}{
		{"v7 unknown", api.HubScoringVersion, "partial", "done", "unknown", "", 200},
		{"v6 unknown", api.DefaultScoringVersion, "partial", "done", "unknown", "", 422},
		{"automatic pass", api.HubScoringVersion, "done", "done", "known", "", 422},
		{"request failed", api.HubScoringVersion, "partial", "error", "unknown", "隔离缓存请求失败", 422},
		{"another item running", api.HubScoringVersion, "running", "done", "unknown", "", 409},
		{"cancelled", api.HubScoringVersion, "cancelled", "done", "unknown", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, id := t.Context(), strings.Repeat("d", 32)
			if _, err := s.CreateReport(ctx, testOwner, CreateRequest{ReportID: id, TargetID: "fixture", Model: "fixture",
				SelectedQuestionIDs: []string{"cache", "Q5"}, Target: Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal(err)
			}
			beforeItem := fmt.Sprintf(`{"error":%q,"judgment":{"status":%q,"max":0,"earned":1,"reason":"原判原因"},"evidence":{"reason":"原检测记录"}}`, tc.requestError, tc.judgment)
			if _, err := s.db.Exec(`UPDATE hub_items SET result_json=?,status=?,attempts=6 WHERE report_id=? AND item_id='cache'`, beforeItem, tc.itemStatus, id); err != nil {
				t.Fatal(err)
			}
			beforeSummary := fmt.Sprintf(`{"complete":false,"verdict":{"scoring_version":%q}}`, tc.version)
			if _, err := s.db.Exec(`UPDATE hub_reports SET status=?,summary_json=? WHERE report_id=?`, tc.reportStatus, beforeSummary, id); err != nil {
				t.Fatal(err)
			}
			rescored := false
			report, err := s.JudgeItem(ctx, testOwner, JudgeRequest{ReportID: id, ItemID: "cache", Verdict: "pass"}, func(_ []questions.Generated, items map[string]Item, _, version string) (map[string]any, error) {
				rescored = true
				var value struct{ Judgment manualJudgment }
				if err := json.Unmarshal(items["cache"].Result, &value); err != nil || version != tc.version || value.Judgment.Earned == nil || *value.Judgment.Earned != 1 || value.Judgment.Max != 1 || value.Judgment.Source != "manual" {
					t.Errorf("manual cache judgment was not saved correctly: %+v (%v)", value, err)
				}
				return map[string]any{"complete": true}, nil
			})
			if tc.want == 200 {
				if err != nil || !rescored {
					t.Fatalf("eligible cache should save, got %v; rescore=%t", err, rescored)
				}
				wantStatus := "done"
				if tc.reportStatus == "cancelled" {
					wantStatus = "cancelled"
					var summary struct{ Complete bool }
					if err := json.Unmarshal(report.Summary.Summary, &summary); err != nil || summary.Complete {
						t.Errorf("cancelled report completion changed: %s (%v)", report.Summary.Summary, err)
					}
				}
				if report.Status != wantStatus {
					t.Errorf("report status=%s, want %s", report.Status, wantStatus)
				}
				return
			}
			var fault *Error
			if !errors.As(err, &fault) || fault.Status != tc.want || rescored {
				t.Fatalf("wanted %d, got %v; rescore=%t", tc.want, err, rescored)
			}
			var result, status, summary string
			if err := s.db.QueryRow(`SELECT i.result_json,r.status,r.summary_json FROM hub_items i JOIN hub_reports r USING(report_id) WHERE report_id=? AND item_id='cache'`, id).Scan(&result, &status, &summary); err != nil {
				t.Fatal(err)
			}
			if result != beforeItem || status != tc.reportStatus || summary != beforeSummary {
				t.Errorf("rejected judgment changed stored item or report: %s %s %s", result, status, summary)
			}
		})
	}
}

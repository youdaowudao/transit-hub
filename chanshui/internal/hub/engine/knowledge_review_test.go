package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestReviewedKnowledgeUsesOriginalRequestsAndPersistsFailure(t *testing.T) {
	for _, blank := range []bool{false, true} {
		t.Run(fmt.Sprint(blank), func(t *testing.T) {
			var defs []questions.Definition
			for _, q := range questions.Presets() {
				if q.Kind == "knowledge" {
					defs = append(defs, q)
				}
			}
			s, o, id, cred := configuredReport(t, defs, "gpt-6-astra")
			_, snap, e := s.LoadRun(t.Context(), o, id)
			if e != nil {
				t.Fatal(e)
			}
			prompts := map[string]questions.Generated{}
			for _, q := range snap.Questions {
				prompts[q.Prompt] = q
			}
			n := 0
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				q, ok := prompts[prompt]
				if !ok {
					t.Fatal("changed frozen prompt")
				}
				n++
				if blank && n == 1 {
					return fixtureResponse(r, ""), nil
				}
				if questions.Text(q.Definition.Parameters, "date") <= "2026-05-18" {
					return fixtureResponse(r, fmt.Sprintf("{\"choice\":%q}", q.Expected)), nil
				}
				return fixtureResponse(r, "不知道"), nil
			})
			if e = RunOne(t.Context(), s, o, id, cred, Config{DisableBankSync: true, Transport: transport}); e != nil {
				t.Fatal(e)
			}
			report, _ := s.Report(t.Context(), o, id)
			var summary struct {
				Knowledge knowledge.Verdict
				Verdict   struct{ Knowledge knowledge.Verdict }
			}
			if e = json.Unmarshal(report.Summary.Summary, &summary); e != nil {
				t.Fatal(e)
			}
			k := summary.Knowledge
			if k.BoundaryReview == nil || summary.Verdict.Knowledge.BoundaryReview == nil {
				t.Fatal("lost full-run provenance")
			}
			if n > 10 || len(k.Rows) != n {
				t.Fatal("request budget/evidence differs", n, k)
			}
			if blank {
				if k.Rows[0].Kind != "error" || report.Status != "partial" {
					t.Fatal("blank answer counted as unknown", report, k.Rows[0])
				}
				var item store.Item
				cursor := ""
				for {
					page, err := s.ListItems(t.Context(), o, id, store.Filters{Limit: 5, Cursor: cursor})
					if err != nil {
						t.Fatal(err)
					}
					for _, candidate := range page.Items {
						if candidate.ItemID == "knowledge:"+k.Rows[0].ID {
							item = candidate
						}
					}
					if page.NextCursor == "" {
						break
					}
					cursor = page.NextCursor
				}
				if item.Status != "error" {
					t.Fatal("cannot retry failed response", item)
				}
				retry, e := s.RetryReport(t.Context(), o, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{item.ItemID}})
				if e != nil {
					t.Fatal(e)
				}
				var pending map[string]any
				json.Unmarshal(retry.Summary.Summary, &pending)
				a := asMap(pending["knowledge"])
				b := asMap(asMap(pending["verdict"])["knowledge"])
				if asMap(a["boundary_review"])["reason"] != "retry_pending" || asMap(b["boundary_review"])["reason"] != "retry_pending" {
					t.Fatal("stale conclusion", pending)
				}
				if e = s.FinishInterrupted(t.Context(), o, id, map[string]any{"message": "fixture final save failed"}); e != nil {
					t.Fatal(e)
				}
				ended, _ := s.Report(t.Context(), o, id)
				if !strings.Contains(string(ended.Summary.Summary), "retry_pending") {
					t.Fatal("interruption restored old boundary")
				}
			} else if k.BoundaryReview.Status != "two_unknown" {
				t.Fatal("two independent events missing", k)
			}
		})
	}
}

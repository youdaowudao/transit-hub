package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func eventDefinitions(dates []string) []questions.Definition {
	defs := []questions.Definition{}
	for i, date := range dates {
		defs = append(defs, questions.Definition{ID: fmt.Sprintf("custom:%032x", 100+i), Title: fmt.Sprintf("事件%d", i), Kind: "knowledge", Enabled: true, Order: i, MaxScore: 0, Template: fmt.Sprintf("EVENT-%s-INDEX_%d", date, i), Parameters: map[string]any{"generator": "event_v1", "date": date, "event": fmt.Sprintf("事件%d", i), "choices": []any{map[string]any{"text": "确认发生", "kind": "know"}, map[string]any{"text": "不知道", "kind": "unknown"}, map[string]any{"text": "错误说法", "kind": "wrong"}}, "knowAliases": []string{}, "wrongAliases": []string{}, "unknownAliases": []string{}}, Judge: questions.Judge{Type: "manual"}})
	}
	return defs
}
func TestExplicitCustomKnowledgeUsesSelectedItemsWithoutInventingOriginalBoundaries(t *testing.T) {
	base := []string{"2020-01-01", "2021-01-01", "2022-01-01", "2023-01-01", "2100-01-01"}
	cases := []struct {
		name                 string
		dates                []string
		answer               func(questions.Definition) string
		failure              bool
		lower, upper, status string
	}{
		{"mixed-boundary", base, func(q questions.Definition) string {
			if questions.Text(q.Parameters, "date") < "2022-01-01" {
				return "A"
			}
			return "B"
		}, false, "2021-01-01", "2022-01-01", "range"},
		{"all-known-no-fabricated-upper", base, func(questions.Definition) string { return "A" }, false, "2023-01-01", "", "range"},
		{"all-unknown-no-fabricated-lower", base, func(questions.Definition) string { return "B" }, false, "", "2020-01-01", "range"},
		{"request-failed-is-not-unknown-answer", base, func(questions.Definition) string { return "A" }, true, "", "", "unknown"},
		{"unparseable-stops-boundary", base, func(questions.Definition) string { return "maybe" }, false, "", "", "unknown"},
		{"same-date-contradiction", []string{"2020-01-01", "2020-01-01"}, func(q questions.Definition) string {
			if strings.HasSuffix(q.Template, "INDEX_0") {
				return "A"
			}
			return "B"
		}, false, "2020-01-01", "2020-01-01", "unknown"},
		{"future-only-no-call", []string{"2100-01-01"}, func(questions.Definition) string { return "A" }, false, "", "", "not_applicable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defs := eventDefinitions(tc.dates)
			s, o, id, c := configuredReport(t, defs, "fixture")
			calls := 0
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				var chosen *questions.Definition
				for i := range defs {
					if strings.Contains(string(raw), defs[i].Template) {
						chosen = &defs[i]
						break
					}
				}
				if chosen == nil {
					t.Fatal("unconfigured knowledge question sent")
				}
				if questions.Text(chosen.Parameters, "date") == "2100-01-01" {
					t.Fatal("future event was sent")
				}
				if tc.failure {
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated knowledge unavailable"}}`)), Request: r}, nil
				}
				return fixtureResponse(r, "最终答案: "+tc.answer(*chosen)), nil
			})
			if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
				t.Fatal(e)
			}
			r, e := s.Report(context.Background(), o, id)
			if e != nil {
				t.Fatal(e)
			}
			var summary map[string]any
			json.Unmarshal(r.Summary.Summary, &summary)
			k := summary["knowledge"].(map[string]any)
			eligible := 0
			for _, q := range defs {
				if questions.Text(q.Parameters, "date") <= "2099-01-01" {
					eligible++
				}
			}
			if calls != eligible {
				t.Fatalf("selected custom items were skipped or repeated: got %d want %d", calls, eligible)
			}
			if k["lower"] != nil || k["upper"] != nil {
				t.Fatal("custom collection fabricated original boundaries", k)
			}
			if eligible == 0 {
				if k["status"] != "not_applicable" {
					t.Fatal(k)
				}
			} else {
				if k["status"] != "custom" || len(k["observations"].([]any)) != eligible {
					t.Fatal("custom observations missing", k)
				}
			}
			items, e := s.ListItems(context.Background(), o, id, store.Filters{Limit: 5})
			if e != nil || len(items.Items) != len(defs) {
				t.Fatal("answer list lost configured events", e, len(items.Items))
			}
			for _, part := range summary["parts"].([]any) {
				if part.(map[string]any)["applicable"] == true {
					t.Fatal("knowledge was added to total score")
				}
			}
		})
	}
}

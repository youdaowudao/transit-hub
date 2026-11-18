package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestRotatedKnowledgeUsesSerial2026Midpoints(t *testing.T) {
	ids := []string{}
	for _, q := range questions.Presets() {
		if q.Kind == "knowledge" {
			ids = append(ids, q.ID)
		}
	}
	if len(ids) != 24 {
		t.Fatalf("default bank has %d events, want 24", len(ids))
	}
	s, o, id, cred := setup(t, ids, "chat")
	_, snap, err := s.LoadRun(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := map[string]questions.Generated{}
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	var requested []string
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q, ok := byPrompt[prompt]
		if !ok {
			t.Fatal("sent a prompt outside the frozen report", prompt)
		}
		date := questions.Text(q.Definition.Parameters, "date")
		requested = append(requested, date)
		if date <= "2026-05-18" {
			return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, q.Expected)), nil
		}
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err = RunOne(t.Context(), s, o, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	want := []string{"2025-11-04", "2026-02-22", "2026-07-19", "2026-07-04", "2026-04-26", "2026-05-30", "2026-05-16", "2026-05-20"}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("sent %v, want serial path %v", requested, want)
	}
	report, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct{ Knowledge knowledge.Verdict }
	if err = json.Unmarshal(report.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Knowledge.Rows) != len(want) {
		t.Fatalf("saved %d rows for %d real requests", len(summary.Knowledge.Rows), len(want))
	}
	skipped := 0
	cursor := ""
	for {
		page, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			var result itemResult
			if err = json.Unmarshal(item.Result, &result); err != nil {
				t.Fatal(err)
			}
			if result.Judgment.Status == "not_asked" {
				skipped++
				if item.Attempts != 0 || result.Judgment.Reason != "串行二分按已经取得的回答继续选题，本轮未选到这道题；不算答错。" {
					t.Fatalf("incorrect skipped result: %+v", item)
				}
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if skipped != 16 {
		t.Fatalf("saved %d skipped questions, want 16", skipped)
	}
}

func TestEditedKnowledgeAndOriginalJSONUseTheirFrozenJudgments(t *testing.T) {
	edited, _ := questions.Preset("knowledge:ao_2026_women")
	edited.Parameters["date"] = "2026-02-01"
	options := edited.Parameters["choices"].([]any)
	options[0].(map[string]any)["kind"] = "wrong"
	options[1].(map[string]any)["kind"] = "know"
	builtIn, _ := questions.Preset("knowledge:milano_2026_men_skate")
	s, owner, id, credential := configuredReport(t, []questions.Definition{edited, builtIn}, "fixture")
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		if strings.Contains(prompt, edited.Template) {
			return fixtureResponse(r, "最终答案: B"), nil
		}
		if !strings.Contains(prompt, builtIn.Template) {
			t.Fatalf("unexpected prompt: %s", prompt)
		}
		return fixtureResponse(r, `{"choice":"C"}`), nil
	})
	if err := RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Knowledge struct {
			Status       string
			Rows         []knowledge.Row
			Observations []struct{ ID, Kind, Date string }
		}
	}
	if err = json.Unmarshal(report.Summary.Summary, &saved); err != nil {
		t.Fatal(err)
	}
	got := saved.Knowledge
	if got.Status != "custom" || len(got.Rows) != 1 || got.Rows[0].ID != "milano_2026_men_skate" || got.Rows[0].Kind != "know" {
		t.Errorf("edited question received a false built-in judgement: %+v", got)
	}
	if len(got.Observations) != 2 || got.Observations[0].ID != edited.ID || got.Observations[0].Date != "2026-02-01" || got.Observations[0].Kind != "know" ||
		got.Observations[1].ID != builtIn.ID || got.Observations[1].Kind != "know" {
		t.Errorf("summary diverges from report snapshots: %+v", got.Observations)
	}
}

package engine

import (
	"encoding/json"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRetryExecutesOnlyFailureAndRebuildsFullOriginalScore(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2", "Q3"}, "chat")
	ctx := t.Context()
	_, snap, e := s.LoadRun(ctx, o, id)
	if e != nil {
		t.Fatal(e)
	}
	prompts := map[string]string{}
	answers := map[string]string{}
	for _, q := range snap.Questions {
		prompts[q.Definition.ID] = q.Prompt
		answers[q.Prompt] = q.Expected
	}
	var calls atomic.Int32
	var retry atomic.Bool
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		if len(body.Messages) == 0 {
			t.Fatal("missing prompt")
		}
		prompt := body.Messages[len(body.Messages)-1].Content
		if prompt == prompts["Q3"] && !retry.Load() {
			return fixtureResponse(r, ""), nil
		}
		if retry.Load() && prompt != prompts["Q3"] {
			t.Error("successful question was charged again")
		}
		return fixtureResponse(r, "最终答案: "+answers[prompt]), nil
	})
	if e = RunOne(ctx, s, o, id, c, Config{Transport: transport}); e != nil {
		t.Fatal(e)
	}
	before, _ := s.ListItems(ctx, o, id, store.Filters{Limit: 5})
	r, _ := s.Report(ctx, o, id)
	if r.Status != "partial" || calls.Load() != 2 {
		t.Fatal(r.Status, calls.Load())
	}
	request := store.RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q3"}}
	queued, e := s.RetryReport(ctx, o, request)
	if e != nil {
		t.Fatal(e)
	}
	if queued.StartedAtMs != 0 {
		t.Fatal("old execution time leaked into queued retry")
	}
	retry.Store(true)
	if e = RunOne(ctx, s, o, id, c, Config{Transport: transport}); e != nil {
		t.Fatal(e)
	}
	after, _ := s.ListItems(ctx, o, id, store.Filters{Limit: 5})
	r, _ = s.Report(ctx, o, id)
	if len(after.Items) != 2 || string(before.Items[0].Result) != string(after.Items[0].Result) || after.Items[0].Attempts != 1 || after.Items[1].Attempts != 2 || calls.Load() != 3 {
		t.Fatal("changed success/repeated calls/lost cumulative usage", after, calls.Load())
	}
	var summary map[string]any
	_ = json.Unmarshal(r.Summary.Summary, &summary)
	total := summary["total"].(map[string]any)
	if r.Status != "done" || summary["iqScore"] != float64(100) || total["score"] != float64(100) || summary["attempts"] != float64(3) {
		t.Fatal("retry did not rebuild full report", string(r.Summary.Summary))
	}
	if _, e = s.RetryReport(ctx, o, request); e != nil {
		t.Fatal("same operation must only read result", e)
	}
	if e = RunOne(ctx, s, o, id, c, Config{Transport: transport}); e == nil || calls.Load() != 3 {
		t.Fatal("same retry replayed paid calls")
	}
	page, _ := s.ListReports(ctx, o, store.Filters{})
	if len(page.Items) != 1 {
		t.Fatal("retry created more reports")
	}
}

func TestSavedResultRetainsLegacyJudgmentAndCurrentPoints(t *testing.T) {
	for _, raw := range []string{`{"answer":"kept","judgment":"unknown"}`, `{"answer":"kept","judgment":{"status":"unknown"}}`} {
		r, e := savedResult(json.RawMessage(raw))
		if e != nil || r.Answer != "kept" || r.Judgment.Status != "unknown" {
			t.Fatal("legacy saved result lost", r, e)
		}
	}
	r, e := savedResult(json.RawMessage(`{"judgment":{"status":"known","earned":6,"max":6},"recognized":true}`))
	if e != nil || r.Judgment.Earned == nil || *r.Judgment.Earned != 6 || !r.Recognized {
		t.Fatal("saved points changed", r, e)
	}
}

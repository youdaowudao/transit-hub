package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func TestSingleAskUsesOriginalRequestAndOnePrivateRecordWithoutEditingBank(t *testing.T) {
	for _, thinking := range []*gateway.ThinkingConfig{nil, {Type: "adaptive", Display: "updates"}, {Type: "enabled", BudgetTokens: 8192, Display: "summarized"}, {Type: "disabled"}} {
		t.Run("thinking-"+func() string {
			if thinking == nil {
				return "omitted"
			}
			return thinking.Type + thinking.Display
		}(), func(t *testing.T) {
			s, o, _, c := setup(t, []string{"Q2"}, "anthropic")
			before, err := s.ListQuestions(t.Context(), o, store.Filters{Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			input := api.SingleAskInput{Prompt: "原样问题，不添加IQ题面或判分提示", Thinking: thinking, Effort: "low"}
			traces := [2][]map[string]any{}
			transport := func(index int) fixtureTransport {
				return func(r *http.Request) (*http.Response, error) {
					_, body := requestPrompt(t, r)
					traces[index] = append(traces[index], body)
					return fixtureResponse(r, "原单问答复"), nil
				}
			}
			g := gateway.Client{Mode: "anthropic", BaseURL: c.BaseURL, Key: c.Key, DefaultEffort: "medium", NoRetry: true, HTTP: &http.Client{Transport: transport(0)}}
			if _, err = api.SingleAsk(t.Context(), g, api.Request{Model: "claude-opus-5-5", Timeout: 360}, input); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("b", 32)
			if _, err = s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "claude-opus-5-5", SelectedQuestionIDs: []string{"Q2"}, SingleAsk: &input, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
				t.Fatal(err)
			}
			if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: transport(1)}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(traces[0], traces[1]) {
				t.Fatal("single-ask parameters differ from original handler", traces)
			}
			_, snap, err := s.LoadRun(t.Context(), o, id)
			if err != nil || snap.SingleAsk == nil || len(snap.Questions) != 1 || snap.Questions[0].Prompt != input.Prompt {
				t.Fatal("single question was changed or probes were added", snap, err)
			}
			after, err := s.ListQuestions(t.Context(), o, store.Filters{Limit: 50})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("one-off question modified the shared question bank", err)
			}
			r, err := s.Report(t.Context(), o, id)
			if err != nil {
				t.Fatal(err)
			}
			var summary map[string]any
			_ = json.Unmarshal(r.Summary.Summary, &summary)
			if summary["singleAsk"] != true || summary["totalScore"] != nil || summary["iqScore"] != nil || asMap(summary["verdict"])["total"] != nil || asMap(summary["verdict"])["iq"] != nil {
				t.Fatal("single ask incorrectly acquired a grade", summary)
			}
			page, err := s.ListItems(t.Context(), o, id, store.Filters{})
			if err != nil || len(page.Items) != 1 || page.Items[0].Attempts != 1 {
				t.Fatal("single question was repeated", page, err)
			}
			var result itemResult
			_ = json.Unmarshal(page.Items[0].Result, &result)
			if asMap(asMap(result.Evidence)["single_ask"])["Answer"] != "原单问答复" {
				t.Fatal("original raw metrics/answer omitted", result)
			}
		})
	}
}

func TestSingleAskFailureRetriesInSameRecordWithOriginalThinking(t *testing.T) {
	s, o, _, c := setup(t, []string{"Q2"}, "anthropic")
	id := strings.Repeat("b", 32)
	input := api.SingleAskInput{Prompt: "只发送本题", Thinking: &gateway.ThinkingConfig{Type: "enabled", BudgetTokens: 4096}, Effort: "high"}
	if _, err := s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "claude-opus-5-5", SingleAsk: &input, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/messages" {
			t.Fatal("retry changed original protocol")
		}
		prompt, body := requestPrompt(t, r)
		if prompt != input.Prompt || body["max_tokens"] != float64(6144) {
			t.Fatal("retry changed original question/budget", body)
		}
		if calls == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated failure"}}`)), Request: r}, nil
		}
		return fixtureResponse(r, "补测原答复"), nil
	})
	if err := RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Protocol = "chat"
	if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryReport(t.Context(), o, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("c", 32), ItemIDs: []string{"custom:" + id}}); err != nil {
		t.Fatal(err)
	}
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Attempts != 2 || calls != 2 {
		t.Fatal("single ask retry duplicated/lost cumulative calls", page, err, calls)
	}
	r, err := s.Report(t.Context(), o, id)
	if err != nil || r.Status != "done" {
		t.Fatal(r, err)
	}
}

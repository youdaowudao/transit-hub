package engine

import (
	"context"
	"encoding/json"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func identityDefinition() questions.Generated {
	return questions.Generated{Definition: questions.Definition{ID: "Q10", Kind: "iq", MaxScore: 5, Judge: questions.Judge{Type: "model_identity"}}}
}
func TestModelIdentityScoringNormalizes105AndNeverConfirmsMissingAnswer(t *testing.T) {
	defs := []questions.Generated{}
	results := map[string]itemResult{}
	for _, q := range questions.Presets() {
		if q.Kind == "iq" && q.Judge.Type == "legacy" {
			defs = append(defs, questions.Generated{Definition: q})
			results[q.ID] = itemResult{Judgment: questions.Judgment{Status: "known", Max: q.MaxScore, Earned: points(q.MaxScore)}}
		}
	}
	defs = append(defs, identityDefinition())
	results["Q10"] = itemResult{Judgment: questions.Judgment{Status: "known", Max: 5, Earned: points(0)}}
	got := summarize(defs, results, "gpt-6.2-sol")
	if got["iqMax"] != float64(105) || got["iqScore"] != float64(95) {
		t.Fatal(got)
	}
	results["Q10"] = itemResult{Judgment: questions.Judgment{Status: "known", Max: 5, Earned: points(5)}}
	if got = summarize(defs, results, "gpt-6.2-sol"); got["iqScore"] != float64(100) {
		t.Fatal(got)
	}
	delete(results, "Q10")
	got = summarize(defs, results, "gpt-6.2-sol")
	if got["iqScore"] != nil || got["totalScore"] != nil {
		t.Fatal("missing identity became confirmed score", got)
	}
	got = summarize(defs, results, "claude-opus-5-5")
	if got["iqMax"] != float64(100) || got["iqScore"] != float64(100) {
		t.Fatal("unstarted inapplicable question cost points", got)
	}
}

func TestModelIdentityRunOneUsesOneRequestNoToolsNoSelfGradeAndRetriesSameReport(t *testing.T) {
	for _, answer := range []string{"我是基于GPT-5.5的模型。", "我是GPT-6.2。", "不知道", "我是一个AI助手。"} {
		t.Run(answer, func(t *testing.T) {
			s, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			o := store.Owner{UserID: "identity-user", WorkspaceID: "identity-scope"}
			settings, err := s.Settings(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			settings.Protocol = "chat"
			settings.Retries = 0
			if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 32)
			_, err = s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.2-sol", SelectedQuestionIDs: []string{"Q2", "Q10"}, Target: store.Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}})
			if err != nil {
				t.Fatal(err)
			}
			var calls, identityCalls atomic.Int32
			currentAnswer := answer
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), questions.ModelIdentityPrompt) {
					identityCalls.Add(1)
					if strings.Contains(string(raw), "tools") || strings.Contains(string(raw), "最终答案") || strings.Contains(string(raw), questions.ModelIdentityReference) {
						t.Fatal("identity request changed or leaks grading", string(raw))
					}
					return fixtureResponse(r, currentAnswer), nil
				}
				return fixtureResponse(r, "最终答案: 开车去"), nil
			})
			cfg := Config{DisableBankSync: true, Transport: transport}
			credential := Credential{BaseURL: "https://fixture.invalid", Key: "isolated-test", Protocol: "chat"}
			if err = RunOne(t.Context(), s, o, id, credential, cfg); err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), o, id)
			if err != nil {
				t.Fatal(err)
			}
			var summary map[string]any
			if err = json.Unmarshal(report.Summary.Summary, &summary); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 || identityCalls.Load() != 1 {
				t.Fatal("unexpected requests", calls.Load(), identityCalls.Load())
			}
			if answer == "我是一个AI助手。" {
				if report.Status != "partial" || summary["iqScore"] != nil || summary["totalScore"] != nil {
					t.Fatal("unknown published final scores", report.Status, summary)
				}
				history, historyErr := s.ListReports(t.Context(), o, store.Filters{})
				if historyErr != nil || len(history.Items) != 1 {
					t.Fatal("history report changed", historyErr)
				}
				var historySummary map[string]any
				if err = json.Unmarshal(history.Items[0].Summary, &historySummary); err != nil || historySummary["iqScore"] != nil || historySummary["totalScore"] != nil {
					t.Fatal("history leaked final score", historySummary, err)
				}
				if _, err = s.RetryReport(t.Context(), o, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q10"}}); err != nil {
					t.Fatal(err)
				}
				currentAnswer = "我是GPT-6.2。"
				if err = RunOne(t.Context(), s, o, id, credential, cfg); err != nil {
					t.Fatal(err)
				}
				report, err = s.Report(t.Context(), o, id)
				if err != nil {
					t.Fatal(err)
				}
				json.Unmarshal(report.Summary.Summary, &summary)
				if calls.Load() != 3 || identityCalls.Load() != 2 || report.Status != "done" || summary["iqScore"] != float64(100) {
					t.Fatal("retry did not replace in original report", calls.Load(), report.Status, summary)
				}
			} else if report.Status != "done" || summary["iqScore"] == nil {
				t.Fatal("known identity did not count", report.Status, summary)
			}
		})
	}
}
func TestModelIdentityOtherModelsDoNotSendRequests(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "gpt-5.6-sol", "gpt-60", "unknown"} {
		t.Run(model, func(t *testing.T) {
			s, err := store.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			o := store.Owner{UserID: "a", WorkspaceID: "a"}
			id := strings.Repeat("c", 32)
			if _, err = s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: model, SelectedQuestionIDs: []string{"Q10"}, Target: store.Target{BaseURL: "https://fixture.invalid", Protocol: "chat"}}); err != nil {
				t.Fatal(err)
			}
			if err = RunOne(t.Context(), s, o, id, Credential{BaseURL: "https://fixture.invalid", Key: "isolated-test", Protocol: "chat"}, Config{DisableBankSync: true, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
				t.Fatal("inapplicable self report sent request")
				return nil, nil
			})}); err != nil {
				t.Fatal(err)
			}
			page, err := s.ListItems(t.Context(), o, id, store.Filters{})
			if err != nil {
				t.Fatal(err)
			}
			var result itemResult
			json.Unmarshal(page.Items[0].Result, &result)
			if result.Judgment.Status != "not_applicable" {
				t.Fatal(result)
			}
		})
	}
}

func TestModelIdentityIncompleteResponsesPersistWithoutFinalScores(t *testing.T) {
	for _, mode := range []string{"empty", "truncated", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			q, _ := questions.Preset("Q10")
			s, o, id, credential := configuredReport(t, []questions.Definition{q}, "gpt-6.2-sol")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				if mode == "cancelled" {
					cancel()
					return nil, context.Canceled
				}
				answer := ""
				if mode == "truncated" {
					answer = "我是GPT-6。"
				}
				response := fixtureResponse(r, answer)
				if mode == "truncated" {
					raw, _ := io.ReadAll(response.Body)
					response.Body.Close()
					response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), `"finish_reason":"stop"`, `"finish_reason":"length"`)))
				}
				return response, nil
			})
			if err := RunOne(ctx, s, o, id, credential, Config{DisableBankSync: true, Transport: transport}); err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), o, id)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" && report.Status != "cancelled" || mode != "cancelled" && report.Status != "partial" {
				t.Fatal("wrong terminal status", report.Status)
			}
			var summary map[string]any
			if err = json.Unmarshal(report.Summary.Summary, &summary); err != nil || summary["iqScore"] != nil || summary["totalScore"] != nil {
				t.Fatal("incomplete response published scores", summary, err)
			}
			page, err := s.ListItems(t.Context(), o, id, store.Filters{})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(err)
			}
			var result itemResult
			if err = json.Unmarshal(page.Items[0].Result, &result); err != nil || result.Error == "" || result.Judgment.Status != "unknown" || result.Judgment.Earned != nil {
				t.Fatal("incomplete response graded", result, err)
			}
			if mode == "truncated" && !strings.Contains(result.Error, "截断") {
				t.Fatal("lost detailed failure", result)
			}
			if mode == "empty" && !strings.Contains(result.Error, "正文答案为空") {
				t.Fatal("lost detailed failure", result)
			}
			if _, err = s.RetryReport(t.Context(), o, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("f", 32), ItemIDs: []string{"Q10"}}); err != nil {
				t.Fatal("cannot retry incomplete response", err)
			}
		})
	}
}

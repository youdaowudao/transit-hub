package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/gateway"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func requestPrompt(t *testing.T, r *http.Request) (string, map[string]any) {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		last, _ := messages[len(messages)-1].(map[string]any)
		if text, ok := last["content"].(string); ok {
			return text, body
		}
		if blocks, ok := last["content"].([]any); ok {
			for _, b := range blocks {
				if text, ok := b.(map[string]any)["text"].(string); ok {
					return text, body
				}
			}
		}
	}
	return "", body
}

func TestOriginalKnowledgeExecutionMatchesOriginalCheck(t *testing.T) {
	ids := []string{}
	for _, q := range questions.Presets() {
		if q.Kind == "knowledge" {
			ids = append(ids, q.ID)
		}
	}
	s, o, id, c := setup(t, ids, "chat")
	traces := [2][]string{}
	makeTransport := func(index int) fixtureTransport {
		return func(r *http.Request) (*http.Response, error) {
			prompt, _ := requestPrompt(t, r)
			traces[index] = append(traces[index], prompt)
			return fixtureResponse(r, "不知道"), nil
		}
	}
	oracle, err := knowledge.Check(t.Context(), gateway.Client{BaseURL: c.BaseURL, Mode: "chat", Key: c.Key, DefaultEffort: "medium", NoRetry: true, HTTP: &http.Client{Transport: makeTransport(0)}}, "fixture", 360)
	if err != nil {
		t.Fatal(err)
	}
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: makeTransport(1)}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(traces[0], traces[1]) {
		t.Fatalf("knowledge no longer uses the original question trace: original=%d migrated=%d; first original=%q migrated=%q", len(traces[0]), len(traces[1]), traces[0][0], traces[1][0])
	}
	r, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]json.RawMessage
	if err = json.Unmarshal(r.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	var got knowledge.Verdict
	if err = json.Unmarshal(summary["knowledge"], &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oracle, got) {
		t.Fatalf("original knowledge fields changed:\noriginal=%+v\nmigrated=%+v", oracle, got)
	}
}

func TestOriginalQuestionBudgetSystemEffortAndEmptyAnswerFallback(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2"}, "chat")
	calls := 0
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		_, body := requestPrompt(t, r)
		if body["max_tokens"] != float64(8192) {
			t.Errorf("original 8192 budget was replaced: %v", body["max_tokens"])
		}
		messages, _ := body["messages"].([]any)
		if len(messages) < 2 || messages[0].(map[string]any)["role"] != "system" {
			t.Error("original system instruction missing")
		}
		if calls == 1 {
			return fixtureResponse(r, ""), nil
		}
		return fixtureResponse(r, "最终答案: 开车去"), nil
	})
	if err := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 || page.Items[0].Status != "done" {
		t.Fatalf("original empty answer recovery missing: calls=%d status=%s result=%s", calls, page.Items[0].Status, page.Items[0].Result)
	}
	if !strings.Contains(string(page.Items[0].Result), `"evidence"`) {
		t.Fatal("original question DTO was not saved")
	}
}

func TestOriginalDefaultsPreserveAuthorizedConcurrencyAndDuration(t *testing.T) {
	s := store.DefaultSettings()
	if s.TimeoutSeconds != 360 || s.Retries != 3 || s.MaxOutputTokens != 8192 || s.MaxAttempts != 0 || s.Concurrency != 32 || s.TotalTimeoutSeconds != 3600 {
		t.Fatalf("changed original defaults or authorized limits: %+v", s)
	}
}

func TestOriginalAutoCompatibilityDoesNotChangeAccountProtocol(t *testing.T) {
	s, o, _, c := setup(t, []string{"Q2"}, "chat")
	settings, err := s.Settings(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Protocol = "auto"
	settings.Retries = 3
	if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("b", 32)
	if _, err = s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.1-sol", SelectedQuestionIDs: []string{"Q2"}, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/responses") {
			return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated missing endpoint"}}`)), Request: r}, nil
		}
		return fixtureResponse(r, "最终答案: 开车去"), nil
	})
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/v1/responses", "/v1/chat/completions"}) {
		t.Fatal("original auto fallback changed", paths)
	}
	r, snap, err := s.LoadRun(t.Context(), o, id)
	if err != nil || r.Protocol != "chat" || snap.Settings.Protocol != "auto" {
		t.Fatal("account binding was overwritten by detection protocol", r.Protocol, snap.Settings.Protocol, err)
	}
}

func TestOriginalNumericBatchKeepsSuccessfulSamplesAfterOneRequestFails(t *testing.T) {
	s, o, _, c := setup(t, []string{"fingerprint:numeric"}, "chat")
	id := strings.Repeat("b", 32)
	if _, err := s.CreateReport(t.Context(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "gpt-6.1-sol", SelectedQuestionIDs: []string{"fingerprint:numeric"}, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); err != nil {
		t.Fatal(err)
	}
	var calls, active, peak atomic.Int32
	gate := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		if !strings.Contains(prompt, "整数") {
			return fixtureResponse(r, "I cannot guarantee outcomes.\n- first\n- second"), nil
		}
		n := calls.Add(1)
		a := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); a > old; old = peak.Load() {
			if peak.CompareAndSwap(old, a) {
				break
			}
		}
		if n <= 3 {
			if n == 3 {
				once.Do(func() { close(gate) })
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		if n == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated transient"}}`)), Request: r}, nil
		}
		return fixtureResponse(r, strings.Repeat("1 2 3 4 5 6 7 8 9 10 ", 34)), nil
	})
	if err := RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 3 || calls.Load() < 4 || calls.Load() > 10 {
		t.Fatalf("original numeric batch/continuation changed: peak=%d calls=%d", peak.Load(), calls.Load())
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	var result itemResult
	if err = json.Unmarshal(page.Items[0].Result, &result); err != nil {
		t.Fatal(err)
	}
	evidence := asMap(result.Evidence)
	if evidence["basis"] != "numeric" || evidence["used_outputs"].(float64) < 3 || len(evidence["evidence"].([]any)) < 4 {
		t.Fatal("successful samples/diagnostics were lost", evidence)
	}
}

func TestOriginalLargeQuestionDTOIsSavedAndReadWithoutDuplicateBody(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2", "Q3"}, "chat")
	large := strings.Repeat("x", 7<<20) + "\n最终答案: 开车去"
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		if strings.Contains(prompt, "洗车店") {
			return fixtureResponse(r, large), nil
		}
		return fixtureResponse(r, "最终答案: 0.05"), nil
	})
	if err := RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ListItems(t.Context(), o, id, store.Filters{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" || first.Items[0].Status != "done" {
		t.Fatal("large first item cannot advance", len(first.Items), first.NextCursor, first.Items[0].Status)
	}
	var result itemResult
	if err = json.Unmarshal(first.Items[0].Result, &result); err != nil {
		t.Fatal(err)
	}
	if asMap(result.Evidence)["answer"] != large || result.Answer != "" {
		t.Fatal("full original body lost or duplicated")
	}
	second, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: first.NextCursor, Limit: 5})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatal("large item caused empty/repeating page", err, len(second.Items), second.NextCursor)
	}
}

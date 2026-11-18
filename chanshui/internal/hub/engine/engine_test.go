package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureResponse(r *http.Request, answer string) *http.Response {
	body := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}}
	if strings.HasSuffix(r.URL.Path, "/messages") {
		body = map[string]any{"content": []any{map[string]any{"type": "text", "text": answer}}, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
	}
	if strings.HasSuffix(r.URL.Path, "/responses") {
		body = map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}}
	}
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}
}
func setup(t *testing.T, selected []string, protocol string) (*store.Store, store.Owner, string, Credential) {
	t.Helper()
	s, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	o := store.Owner{UserID: "test-user", WorkspaceID: "test-workspace"}
	settings, err := s.Settings(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Protocol = protocol
	settings.Retries = 0
	if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "test-secret-do-not-persist", Protocol: protocol, AnthropicAuth: "bearer"}
	_, e = s.CreateReport(context.Background(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: selected, Target: store.Target{AccountID: "1", AccountName: "隔离测试账号", BaseURL: c.BaseURL, Protocol: protocol}})
	if e != nil {
		t.Fatal(e)
	}
	return s, o, id, c
}
func TestOriginalLowScoreUsesSelfGradeAndKeepsCredentialsPrivate(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2"}, "chat")
	var calls atomic.Int32
	events := []string{}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "标准答案") || strings.Contains(string(b), "给自己的上一轮回答打分") {
			return fixtureResponse(r, "0"), nil
		}
		return fixtureResponse(r, "最终答案: 走路去"), nil
	})
	e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake, Notify: func(event string, _ map[string]any) { events = append(events, event) }})
	if e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected paid calls %d", calls.Load())
	}
	r, e := s.Report(context.Background(), o, id)
	if e != nil || r.Status != "done" {
		t.Fatal(r, e)
	}
	items, e := s.ListItems(context.Background(), o, id, store.Filters{})
	if e != nil || len(items.Items) != 1 {
		t.Fatal(e)
	}
	var result itemResult
	if e = json.Unmarshal(items.Items[0].Result, &result); e != nil {
		t.Fatal(e)
	}
	if result.Judgment.Earned == nil || *result.Judgment.Earned != 0 {
		t.Fatal("wrong answer was upgraded", result)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), c.Key) {
		t.Fatal("credential persisted")
	}
	if strings.Join(events, ",") != "accepted,item-saved,persisted" {
		t.Fatal(events)
	}
	if e = RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e == nil || calls.Load() != 2 {
		t.Fatal("same report executed twice")
	}
}
func TestAllExplicitProtocolsAndAnthropicBearerPreserved(t *testing.T) {
	for _, protocol := range []string{"chat", "openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			s, o, id, c := setup(t, []string{"Q2"}, protocol)
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				want := map[string]string{"chat": "/v1/chat/completions", "openai": "/v1/responses", "anthropic": "/v1/messages"}[protocol]
				if r.URL.Path != want {
					t.Fatalf("protocol fallback %s want%s", r.URL.Path, want)
				}
				if r.Header.Get("Authorization") != "Bearer "+c.Key {
					t.Fatal("bearer auth lost")
				}
				if protocol == "anthropic" && r.Header.Get("x-api-key") != "" {
					t.Fatal("unexpected other credential header")
				}
				return fixtureResponse(r, "最终答案: 开车去"), nil
			})
			if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestOversizedResponseNeverGradesTruncatedAnswerAndRedactsErrors(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2"}, "chat")
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return fixtureResponse(r, "最终答案: 开车去"+strings.Repeat("x", MaxResponseBytes+1)), nil
	})
	if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	items, e := s.ListItems(context.Background(), o, id, store.Filters{})
	if e != nil {
		t.Fatal(e)
	}
	var v itemResult
	json.Unmarshal(items.Items[0].Result, &v)
	if v.Error == "" || v.Judgment.Earned != nil || v.Judgment.Status != "unknown" {
		t.Fatal("truncated result graded", v)
	}
	r, _ := s.Report(context.Background(), o, id)
	if r.Status != "partial" {
		t.Fatal(r.Status)
	}
}
func TestEveryHTTPAttemptCountedAndGlobalBudgetStopsWithoutSending(t *testing.T) {
	tport, _ := NewTransport(1, 2, false, fixtureTransport(func(r *http.Request) (*http.Response, error) { return fixtureResponse(r, "ok"), nil }))
	before := 0
	tport.BeforeAttempt = func(context.Context) error { before++; return nil }
	for i := 0; i < 3; i++ {
		r, _ := http.NewRequest("POST", "https://fixture.invalid", strings.NewReader("{}"))
		res, e := tport.RoundTrip(r)
		if i < 2 {
			if e != nil {
				t.Fatal(e)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		} else if e == nil {
			t.Fatal("attempt budget exceeded silently")
		}
	}
	if before != 2 {
		t.Fatal("unissued attempt recorded", before)
	}
}
func TestModelsPreserveProviderOrderAndUseOneAttempt(t *testing.T) {
	calls := 0
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"z-first"},{"id":"a-second"}]}`)), Request: r}, nil
	})
	ids, e := Models(context.Background(), Credential{BaseURL: "https://fixture.invalid", Key: "test-key", Protocol: "chat"}, Deployment{}, fake)
	if e != nil || strings.Join(ids, ",") != "z-first,a-second" || calls != 1 {
		t.Fatal(ids, e, calls)
	}
}
func TestCancellationSavesTerminalAndDoesNotWaitForBrowser(t *testing.T) {
	s, o, id, c := setup(t, []string{"Q2"}, "chat")
	ctx, cancel := context.WithCancel(context.Background())
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) { cancel(); return nil, context.Canceled })
	start := time.Now()
	if e := RunOne(ctx, s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("job waited for browser or ticker")
	}
	r, e := s.Report(context.Background(), o, id)
	if e != nil || r.Status != "cancelled" {
		t.Fatal(r, e)
	}
}
func TestAddedQuestionIsExecutedAndOldSnapshotUnaffected(t *testing.T) {
	s, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	o := store.Owner{UserID: "u", WorkspaceID: "w"}
	q := questions.Definition{ID: "custom:" + strings.Repeat("c", 32), Title: "新增业务题", Kind: "iq", Enabled: true, Order: 10, Template: "只返回alpha", Parameters: map[string]any{"generator": "static"}, MaxScore: 5, Judge: questions.Judge{Type: "text", ExpectedText: "alpha"}}
	saved, e := s.SaveQuestion(context.Background(), o, q, 0, false)
	if e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("b", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "unused-fixture", Protocol: "chat"}
	_, e = s.CreateReport(context.Background(), o, store.CreateRequest{ReportID: id, TargetID: "1", Model: "fixture", SelectedQuestionIDs: []string{q.ID}, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}})
	if e != nil {
		t.Fatal(e)
	}
	saved.Template = "不应执行的新题"
	saved.Judge.ExpectedText = "beta"
	if _, e = s.SaveQuestion(context.Background(), o, saved, saved.Revision, false); e != nil {
		t.Fatal(e)
	}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "只返回alpha") || strings.Contains(string(b), "不应执行") {
			t.Fatal("snapshot changed")
		}
		return fixtureResponse(r, "最终答案: alpha"), nil
	})
	if e = RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	items, e := s.ListItems(context.Background(), o, id, store.Filters{})
	if e != nil || len(items.Items) != 1 {
		t.Fatal(e)
	}
	var result itemResult
	json.Unmarshal(items.Items[0].Result, &result)
	if result.Judgment.Earned == nil || *result.Judgment.Earned != 5 {
		t.Fatal("added question missing from results", fmt.Sprint(result))
	}
}

func TestModelsKeepsSpecificUpstreamReasonWithoutGuessingAndRedactsKey(t *testing.T) {
	key := "a-specific-fixture-secret"
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"gateway_rule","message":"tenant route suspended by operator, reference R-77; echoed ` + key + `"}}`)), Request: r}, nil
	})
	_, e := Models(context.Background(), Credential{BaseURL: "https://fixture.invalid", Key: key, Protocol: "chat"}, Deployment{}, fake)
	if e == nil {
		t.Fatal("expected model-list failure")
	}
	msg := e.Error()
	if !strings.Contains(msg, "R-77") || !strings.Contains(msg, "tenant route suspended") || strings.Contains(msg, key) || strings.Contains(msg, "余额不足") || strings.Contains(msg, "拒绝鉴权") {
		t.Fatalf("lost detail or guessed cause: %s", msg)
	}
}

func configuredReport(t *testing.T, defs []questions.Definition, model string) (*store.Store, store.Owner, string, Credential) {
	t.Helper()
	s, e := store.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	o := store.Owner{UserID: "configured-user", WorkspaceID: "configured-workspace"}
	settings, err := s.Settings(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	settings.Protocol = "chat"
	settings.Retries = 0
	if _, err = s.SaveSettings(t.Context(), o, settings); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, q := range defs {
		q.Enabled = true
		revision := 0
		if old, e := s.Question(context.Background(), o, q.ID); e == nil {
			revision = old.Revision
		}
		if _, e = s.SaveQuestion(context.Background(), o, q, revision, false); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, q.ID)
	}
	id := strings.Repeat("d", 32)
	c := Credential{BaseURL: "https://fixture.invalid", Key: "configured-key-fixture", Protocol: "chat"}
	if _, e = s.CreateReport(context.Background(), o, store.CreateRequest{ReportID: id, TargetID: "target-1", Model: model, SelectedQuestionIDs: ids, Target: store.Target{BaseURL: c.BaseURL, Protocol: c.Protocol}}); e != nil {
		t.Fatal(e)
	}
	return s, o, id, c
}
func TestConfiguredToolsTemplateAndOperandsActuallyReachToolRoundTrip(t *testing.T) {
	q, _ := questions.Preset("tools")
	q.Template = "CHANGED call {{tool_name}} nonce {{nonce}} using lhs {{lhs}} and rhs {{rhs}}; return only the confirmation from tool result."
	q.Parameters["lhs"] = 5
	q.Parameters["rhs"] = 9
	s, o, id, c := configuredReport(t, []questions.Definition{q}, "fixture")
	calls := 0
	nonce := ""
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		if !strings.Contains(body, "CHANGED call audit_echo nonce ") || !strings.Contains(body, "lhs 5 and rhs 9") {
			t.Fatalf("configured tools text not sent %s", body)
		}
		if calls == 1 {
			if strings.Contains(body, "tool-confirm-") {
				t.Fatal("confirmation leaked before result")
			}
			at := strings.Index(body, "nonce ") + 6
			nonce = body[at : at+16]
			args, _ := json.Marshal(map[string]any{"nonce": nonce, "sum": 14})
			response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "call-fixture", "type": "function", "function": map[string]any{"name": "audit_echo", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5}}
			encoded, _ := json.Marshal(response)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
		}
		at := strings.Index(body, "tool-confirm-")
		if at < 0 {
			t.Fatal("missing actual tool result")
		}
		token := body[at : at+len("tool-confirm-")+24]
		return fixtureResponse(r, token), nil
	})
	if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("wrong tool attempt count", calls)
	}
	items, _ := s.ListItems(context.Background(), o, id, store.Filters{})
	var result itemResult
	json.Unmarshal(items.Items[0].Result, &result)
	if result.Judgment.Status != "known" || result.Judgment.Earned == nil || *result.Judgment.Earned != 1 {
		t.Fatal("tool roundtrip not verified", result)
	}
}
func TestConfiguredCachePrefixAndTurnTemplatesActuallyUsedAcrossSixTurns(t *testing.T) {
	q, _ := questions.Preset("cache")
	q.Parameters["prefixHeading"] = "CUSTOM PREFIX HEADER"
	q.Parameters["prefixLineTemplate"] = "CUSTOM LINE {{index}} must stay identical."
	q.Parameters["turnTemplate"] = "CUSTOM TURN {{turn}}"
	s, o, id, c := configuredReport(t, []questions.Definition{q}, "fixture")
	calls := 0
	prefix := ""
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string
				Content string
			}
		}
		if e := json.Unmarshal(raw, &body); e != nil {
			t.Fatal(e)
		}
		if len(body.Messages) == 0 || body.Messages[0].Role != "system" {
			t.Fatal("system prefix absent")
		}
		current := body.Messages[0].Content
		if !strings.Contains(current, "CUSTOM PREFIX HEADER") || !strings.Contains(current, "CUSTOM LINE 001") || !strings.Contains(current, "CUSTOM LINE 170") {
			t.Fatal("configured cache prefix missing")
		}
		if calls == 1 {
			prefix = current
		} else if prefix != current {
			t.Fatal("cache prefix changed across turns")
		}
		if body.Messages[len(body.Messages)-1].Content != fmt.Sprintf("CUSTOM TURN %d", calls) {
			t.Fatal("configured turn missing")
		}
		return fixtureResponse(r, fmt.Sprintf("noted-%d", calls)), nil
	})
	if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	if calls != 6 {
		t.Fatal("cache chain did not execute six turns", calls)
	}
	items, _ := s.ListItems(context.Background(), o, id, store.Filters{})
	var result itemResult
	json.Unmarshal(items.Items[0].Result, &result)
	if result.Judgment.Status != "unknown" || result.Judgment.Earned != nil {
		t.Fatal("missing cache evidence became 0/pass", result)
	}
	r, _ := s.Report(context.Background(), o, id)
	var summary map[string]any
	json.Unmarshal(r.Summary.Summary, &summary)
	if summary["totalScore"] != float64(0) {
		t.Fatal("unknown cache produced full total")
	}
}
func TestDeepSeekUnknownKeepsOriginalNetworkApplicability(t *testing.T) {
	q, _ := questions.Preset("web")
	s, o, id, c := configuredReport(t, []questions.Definition{q}, "deepseek-test")
	fake := fixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("isolated search network unavailable")
	})
	if e := RunOne(context.Background(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); e != nil {
		t.Fatal(e)
	}
	items, _ := s.ListItems(context.Background(), o, id, store.Filters{})
	var result itemResult
	json.Unmarshal(items.Items[0].Result, &result)
	if result.Judgment.Status != "unknown" {
		t.Fatal("network error hidden as not-applicable", result)
	}
	r, _ := s.Report(context.Background(), o, id)
	var summary map[string]any
	json.Unmarshal(r.Summary.Summary, &summary)
	total := summary["total"].(map[string]any)
	if total["score"] != float64(0) {
		t.Fatal("network error yielded full score", total)
	}
	for _, part := range total["parts"].([]any) {
		p := part.(map[string]any)
		if p["key"] == "web" && p["applicable"] != false {
			t.Fatal("changed original unknown web applicability", p)
		}
	}
}

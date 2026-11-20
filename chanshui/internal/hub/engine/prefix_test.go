package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/hub/prefixcheck"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func readPrefix(t *testing.T, s *store.Store, owner store.Owner, id string) (store.Report, prefixcheck.Analysis) {
	t.Helper()
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Prefix *prefixcheck.Analysis
	}
	if err := json.Unmarshal(report.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Prefix == nil {
		t.Fatal("completed report has no saved prefix analysis")
	}
	return report, *summary.Prefix
}

func prefixFixtureTransport(r *http.Request) (*http.Response, error) {
	return fixtureResponse(r, "最终答案: 0.05"), nil
}

func TestReportSavesPrefixForRealOrderIndex(t *testing.T) {
	s, owner, id, credential := setup(t, []string{"Q2", "Q3", "injection"}, "chat")
	if _, err := s.RuntimeDB().ExecContext(t.Context(), "UPDATE hub_items SET order_index=26 WHERE report_id=? AND item_id='Q3'", id); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DisableBankSync: true, Transport: fixtureTransport(prefixFixtureTransport)}
	if err := RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, p := readPrefix(t, s, owner, id)
	if (report.Status != "done" && report.Status != "partial") || p.Status != "ok" || p.Checked != 2 || len(p.Overheads) != 2 || p.Overheads[1][0] != 26 ||
		p.Reference == nil || p.Reference.ItemID != "injection" {
		t.Fatalf("report %s finished with %+v", report.Status, p)
	}
}

func setPrefixCounter(t *testing.T, counter prefixcheck.CountFunc) {
	t.Helper()
	old := prefixCount
	prefixCount = counter
	t.Cleanup(func() { prefixCount = old })
}

func TestReportPersistsUnavailablePrefix(t *testing.T) {
	for _, tc := range []struct {
		name, note        string
		cancelBeforeCount bool
		cancelDuringCount bool
		readFailure       bool
		countDelay        time.Duration
		countFailure      bool
	}{
		{name: "stopped before analysis", note: "检测被停止或已达到总时长，未做逐题核对", cancelBeforeCount: true},
		{name: "stopped during analysis", note: "检测被停止，逐题核对已放弃", cancelDuringCount: true},
		{name: "analysis deadline", note: "逐题核对超过 3 秒，已放弃，检测结果照常保存", countDelay: 3200 * time.Millisecond},
		{name: "read failed", note: "读取已保存题目失败，未做逐题核对：", readFailure: true},
		{name: "counter failed", note: "分词器加载失败，未做逐题核对：", countFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, owner, id, credential := setup(t, []string{"Q2", "Q3"}, "chat")
			credential.Key = "a"
			runCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			setPrefixCounter(t, func(text string) (int, error) {
				calls++
				if tc.cancelDuringCount && calls == 3 {
					cancel()
				}
				if tc.countDelay > 0 && calls == 2 {
					time.Sleep(tc.countDelay)
				}
				if tc.countFailure {
					return 0, errors.New("模拟分词错误")
				}
				return prefixcheck.Count(text)
			})
			saved := 0
			cfg := Config{DisableBankSync: true, Transport: fixtureTransport(prefixFixtureTransport), Notify: func(event string, _ map[string]any) {
				if event != "item-saved" {
					return
				}
				saved++
				if saved != 2 {
					return
				}
				if tc.cancelBeforeCount {
					cancel()
				}
				if tc.readFailure {
					// The report and item indexes remain readable while reading the stored answer fails.
					db := s.RuntimeDB()
					if _, e := db.ExecContext(t.Context(), "ALTER TABLE hub_items RENAME TO original_hub_items"); e != nil {
						t.Error(e)
						return
					}
					query := "CREATE VIEW hub_items AS SELECT report_id,item_id,section_id,order_index,status,attempts,started_at_ms,completed_at_ms,question_json,image," +
						"json_extract('{}','" + strings.Repeat("a", 512) + "') AS result_json FROM original_hub_items"
					if _, e := db.ExecContext(t.Context(), query); e != nil {
						t.Error(e)
					}
				}
			}}
			if err := RunOne(runCtx, s, owner, id, credential, cfg); err != nil {
				t.Fatal(err)
			}
			r, p := readPrefix(t, s, owner, id)
			stopped := tc.cancelBeforeCount || tc.cancelDuringCount
			if (r.Status == "cancelled") != stopped || (!stopped && r.Status != "done" && r.Status != "partial") {
				t.Fatalf("unexpected report status after analysis: %s", r.Status)
			}
			if p.Status != "unavailable" || !strings.HasPrefix(p.Reason, tc.note) || p.Checked != 0 || p.Skipped != 0 || p.Reference != nil || p.Overheads == nil || len(p.Overheads) != 0 {
				t.Fatalf("analysis should have been discarded: %+v", p)
			}
			if tc.readFailure {
				raw, _ := json.Marshal(p)
				if !strings.Contains(p.Reason, "[密钥已隐藏]") || !strings.HasSuffix(p.Reason, "…") || len(p.Reason) > 512 ||
					strings.Contains(p.Reason, credential.Key) || !utf8.ValidString(p.Reason) || len(raw) > 4096 {
					t.Fatalf("incorrect reason redaction and limit: bytes=%d", len(p.Reason))
				}
			}
			var summary map[string]any
			if err := json.Unmarshal(r.Summary.Summary, &summary); err != nil {
				t.Fatal(err)
			}
			if stopped && summary["complete"] != false {
				t.Fatal("a cancelled run was marked complete")
			}
		})
	}
}

func TestPrefixLeavesShortKeysOutOfFixedFields(t *testing.T) {
	for _, tc := range []struct {
		key      string
		selected []string
	}{
		{"ok", []string{"injection", "Q2"}},
		{"o200k", []string{"injection", "Q2"}},
		{"injection", []string{"injection", "Q2"}},
		{"本报告", []string{"Q2"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			s, owner, id, credential := setup(t, tc.selected, "chat")
			credential.Key = tc.key
			if err := RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: fixtureTransport(prefixFixtureTransport)}); err != nil {
				t.Fatal(err)
			}
			r, p := readPrefix(t, s, owner, id)
			got := crossSavedPrefix(t, s, owner, r, p)
			if !got.PerItemChecked || p.Status != "ok" || p.Tokenizer != prefixcheck.TokenizerName {
				t.Fatalf("short fixture key altered fixed values: %+v", p)
			}
			if tc.key == "本报告" {
				if !strings.HasPrefix(p.ReferenceReason, "本报告") || p.Reference != nil {
					t.Fatal("fixed missing-reference reason changed")
				}
			} else if p.Reference == nil || p.Reference.ItemID != "injection" {
				t.Fatal("fixed injection identifier changed")
			}
		})
	}
}

func crossSavedPrefix(t *testing.T, s *store.Store, owner store.Owner, report store.Report, p prefixcheck.Analysis) prefixcheck.Result {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	in := prefixcheck.CrossInput{Report: prefixcheck.ReportInput{
		ReportID: report.ReportID, Model: report.Model, Status: report.Status, Prefix: raw, UpdatedAtMs: report.UpdatedAtMs,
	}}
	for cursor := ""; ; {
		page, err := s.ListItems(t.Context(), owner, report.ReportID, store.Filters{Limit: 5, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			in.Items = append(in.Items, prefixcheck.ItemIndex{ItemID: item.ItemID, OrderIndex: item.OrderIndex, CompletedAtMs: item.CompletedAtMs})
			if _, injection, _ := prefixcheck.Extract(toStored(item)); injection != nil {
				in.Injections = append(in.Injections, *injection)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return prefixcheck.CrossCheck(in)
}

func TestPrefixRecomputedOrPreservedAcrossRetry(t *testing.T) {
	for _, action := range []string{"run", "cancelled", "interrupted"} {
		t.Run(action, func(t *testing.T) {
			s, owner, id, credential := setup(t, []string{"Q2", "Q3", "injection"}, "chat")
			_, snap, err := s.LoadRun(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			failedPrompt := ""
			for _, q := range snap.Questions {
				if q.Definition.ID == "Q3" {
					failedPrompt = q.Prompt
				}
			}
			var retry atomic.Bool
			cfg := Config{DisableBankSync: true, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				if prompt == failedPrompt && !retry.Load() {
					resp := fixtureResponse(r, "")
					resp.StatusCode = http.StatusServiceUnavailable
					return resp, nil
				}
				return prefixFixtureTransport(r)
			})}
			if err := RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
				t.Fatal(err)
			}
			r1, p1 := readPrefix(t, s, owner, id)
			if r1.Status != "partial" || p1.Checked != 1 {
				t.Fatalf("first run = %s, %+v", r1.Status, p1)
			}
			before := crossSavedPrefix(t, s, owner, r1, p1)
			if _, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("b", 32), ItemIDs: []string{"Q3"}}); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "run":
				retry.Store(true)
				err = RunOne(t.Context(), s, owner, id, credential, cfg)
			case "cancelled":
				err = s.CancelQueued(t.Context(), owner, id)
			case "interrupted":
				err = s.FinishInterrupted(t.Context(), owner, id, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			r2, p2 := readPrefix(t, s, owner, id)
			got := crossSavedPrefix(t, s, owner, r2, p2)
			if !got.PerItemChecked || r2.UpdatedAtMs <= r1.UpdatedAtMs {
				t.Fatalf("retry %s returned %+v", action, got)
			}
			if action == "run" {
				if p2.Checked != 2 || p2.ComputedAtMs < p1.ComputedAtMs || len(p2.Overheads) != 2 {
					t.Fatalf("successful retry should analyze all stored answers: %+v", p2)
				}
			} else if r2.Status != action || !reflect.DeepEqual(p1, p2) || !reflect.DeepEqual(got.Reference, before.Reference) ||
				!reflect.DeepEqual(got.Groups, before.Groups) || !reflect.DeepEqual(got.Codes, before.Codes) || got.Grade != before.Grade {
				t.Fatalf("queued %s changed saved analysis", action)
			}
		})
	}
}

func TestPrefixHandlesLargeItemsAndSkipsSingleAsk(t *testing.T) {
	var ids []string
	for i := 0; i < 10; i++ {
		ids = append(ids, fmt.Sprintf("custom:%032x", i+1))
	}
	s, owner, id, credential := setupReport(t, ids, "chat", func(db *store.Store, o store.Owner) {
		for i, q := range ids {
			_, err := db.SaveQuestion(t.Context(), o, questions.Definition{ID: q, Title: "隔离大题", Kind: "iq", Enabled: true, Order: i,
				Template: "资源核对题", Parameters: map[string]any{"generator": "static"}, Judge: questions.Judge{Type: "manual"}}, 0, false)
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	large := strings.Repeat("x", 2<<20) + "最终答案: 1"
	cfg := Config{DisableBankSync: true, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return fixtureResponse(r, large), nil
	})}
	if err := RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	_, p := readPrefix(t, s, owner, id)
	if p.Status != "ok" || p.Checked != 10 {
		t.Fatalf("large result pagination lost data: %+v", p)
	}
	single := strings.Repeat("e", 32)
	ask := api.SingleAskInput{Prompt: "只发一道题"}
	_, err := s.CreateReport(t.Context(), owner, store.CreateRequest{ReportID: single, TargetID: "1", Model: "fixture", SingleAsk: &ask,
		Target: store.Target{AccountID: "1", BaseURL: credential.BaseURL, Protocol: credential.Protocol}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Transport = fixtureTransport(prefixFixtureTransport)
	if err := RunOne(t.Context(), s, owner, single, credential, cfg); err != nil {
		t.Fatal(err)
	}
	r, err := s.Report(t.Context(), owner, single)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(r.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if _, found := summary["prefix"]; found || summary["singleAsk"] != true || summary["total"] != nil || summary["totalScore"] != nil {
		t.Fatal("single ask received the report-only analysis or scores")
	}
}

func TestPrefixSkipsInvalidStoredUsageAndShortRemainingTime(t *testing.T) {
	short, cancel := context.WithTimeout(t.Context(), 14*time.Second)
	defer cancel()
	if got := prefixSummary(short, nil, store.Owner{}, ""); got.Reason != "检测接近总时长，为保证结果能保存，未做逐题核对" {
		t.Fatalf("near deadline should skip all stored reads: %+v", got)
	}
	s, owner, id, credential := setup(t, []string{"Q2", "Q3", "injection"}, "chat")
	saved := 0
	cfg := Config{DisableBankSync: true, Transport: fixtureTransport(prefixFixtureTransport), Notify: func(event string, _ map[string]any) {
		if event != "item-saved" {
			return
		}
		saved++
		if saved != 3 {
			return
		}
		_, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_items SET result_json=CASE item_id
WHEN 'Q2' THEN json_set(result_json,'$.evidence.metrics.InputTokens',9007199254740992)
WHEN 'Q3' THEN json_set(result_json,'$.evidence.metrics.CacheRead',-1) END
WHERE report_id=? AND item_id IN('Q2','Q3')`, id)
		if err != nil {
			t.Error(err)
		}
	}}
	if err := RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	_, p := readPrefix(t, s, owner, id)
	if p.Status != "ok" || p.Checked != 0 || p.Skipped != 2 || p.Reference == nil {
		t.Fatalf("invalid numeric usage survived: %+v", p)
	}
}

func TestPrefixDoesNotInferRoutesFromRedactedPrompts(t *testing.T) {
	s, owner, id, credential := setup(t, []string{"Q2", "Q3"}, "chat")
	credential.Key = "1"
	_, snapshot, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	answers := make(map[string]string, len(snapshot.Questions))
	for _, q := range snapshot.Questions {
		answers[q.Prompt] = q.Expected
	}
	const routeOverhead = 100
	system, err := prefixcheck.Count(api.IQSystemPrompt)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DisableBankSync: true, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		count, err := prefixcheck.Count(prompt)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "最终答案: " + answers[prompt]}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": count + system + routeOverhead, "completion_tokens": 5},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(payload))), Request: r}, nil
	})}
	if err := RunOne(t.Context(), s, owner, id, credential, cfg); err != nil {
		t.Fatal(err)
	}
	report, p := readPrefix(t, s, owner, id)
	if p.Checked != 1 || p.Skipped != 1 || len(p.Overheads) != 1 || p.Overheads[0][1] != routeOverhead {
		t.Fatalf("redacted prompt changed route observations: %+v", p)
	}
	result := crossSavedPrefix(t, s, owner, report, p)
	if !result.PerItemChecked || len(result.Groups) != 1 || slices.Contains(result.Codes, "mixed_routes") {
		t.Fatalf("one route was reported as multiple routes: %+v", result)
	}
}

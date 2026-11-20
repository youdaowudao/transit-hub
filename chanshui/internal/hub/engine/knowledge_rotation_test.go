package engine

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func loadKnowledgeProjection(t *testing.T, report store.Report) knowledge.Verdict {
	t.Helper()
	var saved struct {
		Knowledge knowledge.Verdict
		Verdict   struct{ Knowledge knowledge.Verdict }
	}
	if err := json.Unmarshal(report.Summary.Summary, &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Knowledge, saved.Verdict.Knowledge) {
		t.Fatal("the two report projections diverged")
	}
	return saved.Knowledge
}

func TestKnowledgeAsksAcrossYearsBeforeNarrowingAfterUnknown(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "fixture"} {
		t.Run(model, func(t *testing.T) {
			s, owner, id, credential := configuredReport(t, questions.CurrentKnowledge(), model)
			_, snapshot, err := s.LoadRun(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			byPrompt := make(map[string]questions.Generated)
			for _, q := range snapshot.Questions {
				byPrompt[q.Prompt] = q
			}
			var asked []string
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				q, found := byPrompt[prompt]
				if !found {
					t.Fatal("asked a question outside the report snapshot")
				}
				date := questions.Text(q.Definition.Parameters, "date")
				asked = append(asked, date)
				answer := "D"
				if date < "2025-11-04" {
					answer = q.Expected
				}
				return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
			})
			if err = RunOne(t.Context(), s, owner, id, credential, Config{DisableBankSync: true, Transport: transport}); err != nil {
				t.Fatal(err)
			}
			want := []string{"2025-11-04", "2026-01-31", "2026-04-12"}
			if len(asked) < len(want) || len(asked) > 10 || !reflect.DeepEqual(asked[:len(want)], want) {
				t.Fatalf("asked %v, want to start with %v within the 10-question budget", asked, want)
			}
			report, err := s.Report(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			var summary struct {
				Knowledge knowledge.Verdict
				Verdict   struct{ Knowledge knowledge.Verdict }
			}
			if err = json.Unmarshal(report.Summary.Summary, &summary); err != nil {
				t.Fatal(err)
			}
			k := summary.Knowledge
			if !reflect.DeepEqual(k, summary.Verdict.Knowledge) || k.Label != "知识截止日期未确认" || k.Cutoff != "" ||
				k.Status != "unknown" || k.BoundaryReview == nil || k.BoundaryReview.From != "" || k.BoundaryReview.To != "" || len(k.Rows) != len(asked) {
				t.Fatalf("unsupported date range or divergent saved observations: %+v", summary)
			}
		})
	}
}

func TestRotatedKnowledgeUsesSerial2026Midpoints(t *testing.T) {
	t.Run("previously queued report", func(t *testing.T) { testRotatedKnowledgeSerial(t, true, false) })
	t.Run("new report", func(t *testing.T) { testRotatedKnowledgeSerial(t, false, false) })
	t.Run("oversized answer preserves other events", func(t *testing.T) { testRotatedKnowledgeSerial(t, false, true) })
}

func testRotatedKnowledgeSerial(t *testing.T, legacy, oversized bool) {
	t.Helper()
	ids := []string{}
	for _, q := range questions.Presets() {
		if q.Kind == "knowledge" {
			ids = append(ids, q.ID)
		}
	}
	if len(ids) != 24 {
		t.Fatalf("default bank has %d events, want 24", len(ids))
	}
	const editedID = "knowledge:ao_2026_women"
	const title = "只修改标题的澳网题"
	const order = 99800
	s, o, id, cred := setupReport(t, ids, "chat", func(s *store.Store, owner store.Owner) {
		if legacy {
			return
		}
		q, err := s.Question(t.Context(), owner, editedID)
		if err != nil {
			t.Fatal(err)
		}
		q.Title, q.Order = title, order
		if _, err = s.SaveQuestion(t.Context(), owner, q, q.Revision, false); err != nil {
			t.Fatal(err)
		}
	})
	if legacy {
		// A report created before the change has a frozen bank but no method marker.
		if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_reports SET summary_json='{}' WHERE report_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_, snap, err := s.LoadRun(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := map[string]questions.Generated{}
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	var requested []string
	var oversizedItem string
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q, ok := byPrompt[prompt]
		if !ok {
			t.Fatal("sent a prompt outside the frozen report", prompt)
		}
		date := questions.Text(q.Definition.Parameters, "date")
		requested = append(requested, date)
		if oversized && len(requested) == 1 {
			oversizedItem = q.Definition.ID
			answer := strings.Repeat("<", store.MaxItemBytes/6+1) + fmt.Sprintf(`{"choice":%q}`, q.Expected)
			body := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, answer)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		if date <= "2026-05-18" {
			return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, q.Expected)), nil
		}
		return fixtureResponse(r, `{"choice":"D"}`), nil
	})
	if err = RunOne(t.Context(), s, o, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	want := []string{"2025-11-04", "2026-02-22", "2026-07-19", "2026-07-04", "2026-04-26", "2026-05-30", "2026-05-16", "2026-05-20"}
	skipReason := "串行二分按已经取得的回答继续选题，本轮未选到这道题；不算答错。"
	if !legacy {
		want = []string{"2025-11-04", "2026-01-31", "2026-04-12", "2026-05-30", "2026-06-17", "2026-04-26", "2026-05-16", "2026-05-20"}
		skipReason = "本轮根据已取得的回答选择了其他题目。这道题未提问，不算答错。"
	}
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
	if !legacy {
		review := summary.Knowledge.BoundaryReview
		if review == nil || review.Method != knowledge.CrossYearMethod || review.Status != "two_unknown" ||
			review.From != "2026-05-16" || review.To != "2026-05-20" || review.CheckID != "ucl_2026_final" {
			t.Fatalf("range was not based on the two later responses: %+v", review)
		}
	}
	skipped := 0
	cursor := ""
	for {
		page, err := s.ListItems(t.Context(), o, id, store.Filters{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if !legacy && item.ItemID == editedID && (item.Definition.Title != title || item.Definition.Order != order || item.Status != "done") {
				t.Fatalf("did not retain display edits with the completed question: %+v", item)
			}
			var result itemResult
			if err = json.Unmarshal(item.Result, &result); err != nil {
				t.Fatal(err)
			}
			if item.ItemID == oversizedItem {
				row := asMap(asMap(result.Evidence)["row"])
				if result.Answer != "" || item.Status != "done" || row["kind"] != "know" ||
					!strings.Contains(result.Judgment.Reason, "超过单题保存上限") {
					t.Fatalf("oversized answer lost its assessment or reported full text: %s %s %v", item.Status, result.Judgment.Reason, row)
				}
			} else if result.Judgment.Status == "reference" {
				wantAnswer := fmt.Sprintf(`{"choice":%q}`, item.Expected)
				if questions.Text(item.Definition.Parameters, "date") > "2026-05-18" {
					wantAnswer = `{"choice":"D"}`
				}
				if result.Answer != wantAnswer {
					t.Fatalf("knowledge question %s saved answer %q, want %q", item.ItemID, result.Answer, wantAnswer)
				}
				if result.Metrics == nil || result.Metrics.Answer != "" {
					t.Fatalf("knowledge question %s duplicated the answer in metrics", item.ItemID)
				}
			}
			if result.Judgment.Status == "not_asked" {
				skipped++
				if item.Attempts != 0 || result.Judgment.Reason != skipReason {
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

var knowledgeReadyFunctionNumber atomic.Int64

func registerKnowledgeTestCallback(t *testing.T, callback func()) string {
	t.Helper()
	name := fmt.Sprintf("cross_year_ready_%d", knowledgeReadyFunctionNumber.Add(1))
	var once sync.Once
	err := sqlite.RegisterScalarFunction(name, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		once.Do(callback)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestCrossYearEstimateIsPublishedOnlyAtFinish(t *testing.T) {
	for _, mode := range []string{"partial", "cancelled", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			other, ok := questions.Preset("Q2")
			if !ok {
				t.Fatal("missing concurrent question")
			}
			var ready chan struct{}
			var readyFunction string
			if mode == "cancelled" {
				ready = make(chan struct{})
				readyFunction = registerKnowledgeTestCallback(t, func() { close(ready) })
			}
			s, owner, id, cred := configuredReport(t, append(questions.CurrentKnowledge(), other), "fixture")
			_, snap, err := s.LoadRun(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			byPrompt := make(map[string]questions.Generated)
			for _, q := range snap.Questions {
				if q.Definition.Kind == "knowledge" {
					byPrompt[q.Prompt] = q
				}
			}
			// Enforce the rule on every progress write, including writes by other sections.
			_, err = s.RuntimeDB().ExecContext(t.Context(), `CREATE TRIGGER exclude_knowledge_range_from_progress
				BEFORE UPDATE OF summary_json ON hub_reports WHEN NEW.status = 'running' AND
				(json_extract(NEW.summary_json,'$.knowledge.cutoff') IS NOT NULL OR
				json_extract(NEW.summary_json,'$.verdict.knowledge.cutoff') IS NOT NULL)
				BEGIN SELECT RAISE(ABORT, 'range leaked into progress'); END`)
			if err != nil {
				t.Fatal(err)
			}
			if ready != nil {
				// That progress write happens after check computed the candidate and
				// originalKnowledge saved every unasked item. Keep Q2 open until then.
				_, err = s.RuntimeDB().ExecContext(t.Context(), `CREATE TRIGGER announce_completed_knowledge
					AFTER UPDATE OF summary_json ON hub_reports WHEN NEW.status = 'running' AND NOT EXISTS
					(SELECT 1 FROM hub_items WHERE report_id=NEW.report_id AND section_id='knowledge' AND status IN ('queued','running'))
					BEGIN SELECT `+readyFunction+`(); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "write failure" {
				_, err = s.RuntimeDB().ExecContext(t.Context(), `CREATE TRIGGER block_final_summary
					BEFORE UPDATE OF status ON hub_reports WHEN NEW.status IN ('done', 'partial')
					BEGIN SELECT RAISE(ABORT, 'fixture rejected final summary'); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				q, found := byPrompt[prompt]
				if !found {
					if ready != nil {
						select {
						case <-ready:
							cancel()
							return nil, context.Canceled
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return nil, fmt.Errorf("fixture non-knowledge request failed")
				}
				answer := q.Expected
				if questions.Text(q.Definition.Parameters, "date") > "2026-05-18" {
					answer = "D"
				}
				return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
			})
			err = RunOne(ctx, s, owner, id, cred, Config{DisableBankSync: true, Transport: fake})
			if ready != nil {
				select {
				case <-ready:
				default:
					t.Fatal("other section stopped before a complete knowledge candidate was available")
				}
			}
			wantStatus := mode
			if mode == "write failure" {
				if err == nil || !strings.Contains(err.Error(), "fixture rejected final summary") {
					t.Fatalf("expected final write failure, got %v", err)
				}
				if err = s.FinishInterrupted(t.Context(), owner, id, nil); err != nil {
					t.Fatal(err)
				}
				wantStatus = "interrupted"
			} else if err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), owner, id)
			if err != nil || report.Status != wantStatus {
				t.Fatalf("report status %q, error %v; want %q", report.Status, err, wantStatus)
			}
			got := loadKnowledgeProjection(t, report)
			if got.BoundaryReview == nil || got.BoundaryReview.Method != knowledge.CrossYearMethod {
				t.Fatal("lost initial report method")
			}
			if mode == "partial" {
				if got.BoundaryReview.To != "2026-05-20" || got.Status != "estimated" {
					t.Fatalf("unrelated failure suppressed valid evidence: %+v", got)
				}
			} else {
				if got.Cutoff != "" || got.BoundaryReview.To != "" || got.Status == "estimated" {
					t.Fatalf("stopped report published an unsupported interval: %+v", got)
				}
				var count int
				err = s.RuntimeDB().QueryRowContext(t.Context(), `SELECT count(*) FROM hub_items
					WHERE report_id=? AND section_id='knowledge' AND json_extract(result_json,'$.evidence.row.id') IS NOT NULL`, id).Scan(&count)
				if err != nil || count != 8 {
					t.Fatalf("already saved observations=%d, error %v; want 8", count, err)
				}
			}
		})
	}
}

func TestCrossYearStopPreservesOnlySavedStatistics(t *testing.T) {
	for _, tc := range []struct {
		when string
		want int
	}{
		{"before the knowledge worker starts", 0},
		{"before beginning the next question", 1},
		{"while saving skipped items", 8},
	} {
		t.Run(tc.when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var callback string
			if tc.want > 0 {
				callback = registerKnowledgeTestCallback(t, cancel)
			}
			ids := []string{"Q2"}
			for _, q := range questions.CurrentKnowledge() {
				ids = append(ids, q.ID)
			}
			s, owner, id, cred := setupReport(t, ids, "chat", func(s *store.Store, owner store.Owner) {
				settings, err := s.Settings(t.Context(), owner)
				if err != nil {
					t.Fatal(err)
				}
				settings.Concurrency = 1
				if _, err := s.SaveSettings(t.Context(), owner, settings); err != nil {
					t.Fatal(err)
				}
			})
			_, snap, err := s.LoadRun(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			byPrompt := make(map[string]questions.Generated)
			for _, q := range snap.Questions {
				if q.Definition.Kind == "knowledge" {
					byPrompt[q.Prompt] = q
				}
			}
			if callback != "" {
				trigger := `AFTER UPDATE OF result_json ON hub_items WHEN json_extract(NEW.result_json,'$.judgment.status')='not_asked'
					BEGIN SELECT ` + callback + `(); END`
				if tc.want == 1 {
					trigger = `BEFORE UPDATE OF status ON hub_items WHEN NEW.item_id='knowledge:ao_2026_women' AND NEW.status='running'
						BEGIN SELECT ` + callback + `(); SELECT RAISE(ABORT, 'fixture cancelled before starting the next item'); END`
				}
				_, err = s.RuntimeDB().ExecContext(t.Context(), `CREATE TRIGGER cancel_knowledge `+trigger)
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				q, found := byPrompt[prompt]
				if !found {
					if tc.want == 0 {
						cancel()
						return nil, context.Canceled
					}
					return nil, fmt.Errorf("fixture other section failed")
				}
				calls++
				answer := q.Expected
				if questions.Text(q.Definition.Parameters, "date") > "2026-05-18" {
					answer = "D"
				}
				return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
			})
			if err = RunOne(ctx, s, owner, id, cred, Config{DisableBankSync: true, Transport: fake}); err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			v := loadKnowledgeProjection(t, report)
			if report.Status != "cancelled" || v.BoundaryReview.Reason != "interrupted" || v.Cutoff != "" || v.BoundaryReview.To != "" ||
				strings.Contains(v.Reason, "知识结论尚未发布") {
				t.Fatalf("stopped report retained a range or pending instructions: %+v", v)
			}
			if calls != tc.want || v.Total != tc.want || len(v.Rows) != tc.want {
				t.Fatalf("sent %d knowledge requests, want %d saved and displayed: %+v", calls, tc.want, v)
			}
			if tc.want == 0 && v.Reason != "" || tc.want > 0 && !strings.Contains(v.Reason, fmt.Sprintf("本轮记录了 %d 道事件题结果", tc.want)) {
				t.Fatalf("wrong number of observations in the stopped report: %+v", v)
			}
		})
	}
}

func TestCrossYearSavesTenQuestionBudgetAndTruncatedOrInvalidReplies(t *testing.T) {
	for _, mode := range []string{"wrong", "truncated", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, id, cred := configuredReport(t, questions.CurrentKnowledge(), "fixture")
			_, snap, err := s.LoadRun(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			byPrompt := make(map[string]questions.Generated)
			for _, q := range snap.Questions {
				byPrompt[q.Prompt] = q
			}
			asked := make(map[string]bool)
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				prompt, _ := requestPrompt(t, r)
				q := byPrompt[prompt]
				if q.Definition.ID == "" || asked[q.Definition.ID] {
					t.Fatal("outside the snapshot or repeated prompt")
				}
				asked[q.Definition.ID] = true
				if len(asked) == 1 {
					switch mode {
					case "invalid":
						return fixtureResponse(r, "我刚刚联网"), nil
					case "truncated":
						response := fixtureResponse(r, "")
						response.Body.Close()
						content := fmt.Sprintf(`{"choice":%q}`, q.Expected)
						payload := fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"length"}]}`, content)
						response.Body = io.NopCloser(strings.NewReader(payload))
						return response, nil
					}
				}
				wrong := "A"
				if wrong == q.Expected {
					wrong = "B"
				}
				return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, wrong)), nil
			})
			if err = RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: fake}); err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			got := loadKnowledgeProjection(t, report)
			limit := 10
			if mode == "invalid" {
				limit = 1
			}
			if len(asked) != limit || got.Total != limit || len(got.Rows) != limit || got.Cutoff != "" || got.BoundaryReview.To != "" {
				t.Fatalf("mode %s, sent %d requests, saved %+v", mode, len(asked), got)
			}
			switch mode {
			case "wrong":
				if got.BoundaryReview.Reason != "no_anchor" || got.Correct != 0 {
					t.Fatalf("wrong answers supplied a boundary: %+v", got)
				}
			case "truncated":
				if report.Status != "partial" || got.BoundaryReview.Reason != "main_error" ||
					got.Rows[0].Kind != "error" || !strings.Contains(got.Rows[0].Error, "截断") {
					t.Fatalf("truncated response did not preserve its failure: %+v", got)
				}
			case "invalid":
				if got.Status != "invalid" || got.BoundaryReview.Reason != "invalid" {
					t.Fatalf("invalid response did not immediately stop the search: %+v", got)
				}
			}
		})
	}
}

func TestCrossYearRetriesOnlyTheFailedObservationAndRecomputesTheRange(t *testing.T) {
	s, owner, id, cred := configuredReport(t, questions.CurrentKnowledge(), "gpt-6.1-sol")
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := make(map[string]questions.Generated)
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	const failedID = "knowledge:ao_2026_women"
	var retried []string
	retry := false
	retryFails := true
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q := byPrompt[prompt]
		if retry {
			retried = append(retried, q.Definition.ID)
			if retryFails {
				return nil, fmt.Errorf("fixture retry upstream connection reset")
			}
		} else if q.Definition.ID == failedID {
			return fixtureResponse(r, ""), nil
		}
		answer := q.Expected
		if questions.Text(q.Definition.Parameters, "date") > "2026-05-18" {
			answer = "D"
		}
		return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, answer)), nil
	})
	cfg := Config{DisableBankSync: true, Transport: fake}
	if err = RunOne(t.Context(), s, owner, id, cred, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	first := loadKnowledgeProjection(t, before)
	if before.Status != "partial" || first.BoundaryReview.Reason != "main_error" || first.Rows[1].Kind != "error" || first.Cutoff != "" {
		t.Fatalf("empty 2026 answer was not retained as a failure: %+v", first)
	}
	accepted, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("e", 32), ItemIDs: []string{failedID}})
	if err != nil {
		t.Fatal(err)
	}
	pending := loadKnowledgeProjection(t, accepted)
	if pending.Cutoff != "" || pending.BoundaryReview.Method != knowledge.CrossYearMethod || pending.BoundaryReview.Reason != "retry_pending" {
		t.Fatalf("invalid retry projection: %+v", pending)
	}
	retry = true
	if err = RunOne(t.Context(), s, owner, id, cred, cfg); err != nil {
		t.Fatal(err)
	}
	failedAgain, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	failed := loadKnowledgeProjection(t, failedAgain)
	if failedAgain.Status != "partial" || !reflect.DeepEqual(retried, []string{failedID}) ||
		failed.BoundaryReview.Reason != "main_error" || failed.Cutoff != "" || failed.BoundaryReview.To != "" ||
		!strings.Contains(failed.Rows[1].Error, "fixture retry upstream connection reset") {
		t.Fatalf("failed retry lost its specific failure or exposed a range: %+v", failed)
	}
	if _, err = s.RetryReport(t.Context(), owner, store.RetryRequest{
		ReportID: id, RetryID: strings.Repeat("f", 32), ExpectedRetryID: strings.Repeat("e", 32), ItemIDs: []string{failedID},
	}); err != nil {
		t.Fatal(err)
	}
	retryFails = false
	retried = nil
	if err = RunOne(t.Context(), s, owner, id, cred, cfg); err != nil {
		t.Fatal(err)
	}
	after, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, after)
	if after.Status != "done" || !reflect.DeepEqual(retried, []string{failedID}) ||
		got.Rows[1].Kind != "know" || len(got.Rows) != len(first.Rows) || got.BoundaryReview.To != "2026-05-20" {
		t.Fatalf("retry=%v, report=%s, observations=%+v", retried, after.Status, got)
	}
}

func TestCrossYearRetryDoesNotReuseQueuedOldAnswers(t *testing.T) {
	s, owner, id, cred := configuredReport(t, questions.CurrentKnowledge(), "fixture")
	if err := s.CancelQueued(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	_, snap, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	byPrompt := make(map[string]questions.Generated)
	for _, q := range snap.Questions {
		byPrompt[q.Prompt] = q
	}
	const invalidID = "knowledge:ao_2026_women"
	const failedID = "knowledge:virginia_2025_governor"
	const otherID = "knowledge:hungary_2026_vote"
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		prompt, _ := requestPrompt(t, r)
		q := byPrompt[prompt]
		switch q.Definition.ID {
		case invalidID:
			return fixtureResponse(r, "我刚刚联网"), nil
		case failedID:
			return nil, fmt.Errorf("fixture upstream connection reset")
		case otherID:
			return fixtureResponse(r, fmt.Sprintf(`{"choice":%q}`, q.Expected)), nil
		default:
			t.Fatalf("explicit retry selected %s without authorization", q.Definition.ID)
			return nil, nil
		}
	})
	cfg := Config{DisableBankSync: true, Transport: fake}
	enqueue := func(key, previous string, ids ...string) {
		t.Helper()
		_, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat(key, 32), ExpectedRetryID: previous, ItemIDs: ids})
		if err != nil {
			t.Fatal(err)
		}
	}
	enqueue("b", "", invalidID, failedID)
	if err = RunOne(t.Context(), s, owner, id, cred, cfg); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got := loadKnowledgeProjection(t, report)
	if got.BoundaryReview.Method != knowledge.CrossYearMethod || got.BoundaryReview.Reason != "invalid" || len(got.Rows) != 2 {
		t.Fatalf("new-method marker or recorded invalid reply lost after cancel-before-first-run: %+v", got)
	}

	enqueue("c", strings.Repeat("b", 32), invalidID)
	if err = s.CancelQueued(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	enqueue("e", strings.Repeat("c", 32), otherID)
	if err = RunOne(t.Context(), s, owner, id, cred, cfg); err != nil {
		t.Fatal(err)
	}
	report, err = s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	got = loadKnowledgeProjection(t, report)
	if got.BoundaryReview.Reason != "main_error" || len(got.Rows) != 2 ||
		got.Rows[0].Kind != "error" || got.Rows[1].Kind != "know" || got.Cutoff != "" {
		t.Fatalf("stale queued row was included, or genuine error was lost: %+v", got)
	}
}

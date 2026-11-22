package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
	"github.com/kiss-kedaya/chanshui/internal/knowledge"
)

func TestReviewSummaryAndReadJSONRedactProviderKeyInEverySavedUpdate(t *testing.T) {
	ids := []string{}
	for _, q := range questions.Presets() {
		if q.Kind == "knowledge" {
			ids = append(ids, q.ID)
		}
	}
	s, o, id, c := setup(t, ids, "anthropic")
	c.Key = "fixture-secret-not-real-867"
	_, err := s.RuntimeDB().Exec(`CREATE TABLE isolated_summary_updates(value TEXT); CREATE TRIGGER isolated_summary_capture AFTER UPDATE OF summary_json ON hub_reports BEGIN INSERT INTO isolated_summary_updates VALUES(NEW.summary_json); END`)
	if err != nil {
		t.Fatal(err)
	}
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"isolated-reason ` + c.Key + `"}}`)), Request: r}, nil
	})
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fake}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.RuntimeDB().Query(`SELECT value FROM isolated_summary_updates`)
	if err != nil {
		t.Fatal(err)
	}
	captured := 0
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		captured++
		if strings.Contains(raw, c.Key) {
			t.Error("provider key leaked in saved summary update")
		}
	}
	rows.Close()
	if captured < 2 {
		t.Fatal("progress/terminal writes not captured", captured)
	}
	report, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(report)
	if err != nil || strings.Contains(string(body), c.Key) || !strings.Contains(string(body), "isolated-reason") {
		t.Fatal("report response did not safely retain the actual reason", err)
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(page)
	if err != nil || strings.Contains(string(body), c.Key) {
		t.Fatal("item response contains provider key", err)
	}
}

func TestReviewKnowledgeCancellationKeepsObservedVerdict(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-first-request", true: "custom-after-one-answer"}[custom], func(t *testing.T) {
			var s *store.Store
			var o store.Owner
			var id string
			var c Credential
			if custom {
				s, o, id, c = configuredReport(t, eventDefinitions([]string{"2025-01-01", "2026-01-01"}), "fixture")
			} else {
				ids := []string{}
				for _, q := range questions.Presets() {
					if q.Kind == "knowledge" {
						ids = append(ids, q.ID)
					}
				}
				s, o, id, c = setup(t, ids, "chat")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				if !custom {
					cancel()
					return nil, context.Canceled
				}
				return fixtureResponse(r, "最终答案: A"), nil
			})
			cfg := Config{DisableBankSync: true, Transport: fake, Notify: func(event string, _ map[string]any) {
				if custom && event == "item-saved" {
					cancel()
				}
			}}
			if err := RunOne(ctx, s, o, id, c, cfg); err != nil {
				t.Fatal(err)
			}
			report, err := s.Report(t.Context(), o, id)
			if err != nil || report.Status != "cancelled" {
				t.Fatal("stop did not remain cancelled", report, err)
			}
			var summary map[string]any
			_ = json.Unmarshal(report.Summary.Summary, &summary)
			k := asMap(summary["knowledge"])
			if k["status"] == "not_selected" || k["status"] == nil {
				t.Fatal("stop lost the observed knowledge summary", k)
			}
			key := "rows"
			if custom {
				key = "observations"
			}
			if len(k[key].([]any)) != 1 {
				t.Fatal("saved observation missing", k)
			}
		})
	}
}

func TestReviewKnowledgeRetryUsesCumulativePersistedAttempts(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		s, o, id, c := setupLegacyKnowledgeReport(t, []string{"knowledge:mamdani", "knowledge:bulgaria"})
		assertCumulativeKnowledgeRetry(t, s, o, id, c)
	})
	t.Run("current", func(t *testing.T) {
		s, o, id, c := setup(t, []string{"knowledge:virginia_2025_governor", "knowledge:afcon_2026_goal"}, "chat")
		assertCumulativeKnowledgeRetry(t, s, o, id, c)
	})
}

func assertCumulativeKnowledgeRetry(t *testing.T, s *store.Store, o store.Owner, id string, c Credential) {
	t.Helper()
	_, snap, err := s.LoadRun(t.Context(), o, id)
	if err != nil || len(snap.Questions) != 2 {
		t.Fatal("test needs two original anchors", err, len(snap.Questions))
	}
	if err = s.BeginRun(t.Context(), o, id); err != nil {
		t.Fatal(err)
	}
	rows := []knowledge.Row{}
	for i, q := range snap.Questions {
		p, _ := questions.OriginalKnowledge(q.Definition)
		failure := ""
		status := "done"
		attempts := 1
		if i == 0 {
			failure = "isolated earlier failure"
			status = "error"
			attempts = 2
		}
		row := knowledge.Observe(p, "不知道", failure)
		rows = append(rows, row)
		if err = s.BeginItem(t.Context(), o, id, q.Definition.ID); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveItem(t.Context(), o, id, q.Definition.ID, status, map[string]any{"answer": "不知道", "error": failure, "evidence": map[string]any{"row": row}, "judgment": map[string]any{"status": "reference"}}, attempts); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Finish(t.Context(), o, id, "partial", map[string]any{"attempts": 3, "knowledge": knowledge.FromRows(rows, "chat", 3)}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryReport(t.Context(), o, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("c", 32), ItemIDs: []string{snap.Questions[0].Definition.ID}}); err != nil {
		t.Fatal(err)
	}
	if err = RunOne(t.Context(), s, o, id, c, Config{DisableBankSync: true, Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) { return fixtureResponse(r, "不知道"), nil })}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), o, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	_ = json.Unmarshal(report.Summary.Summary, &summary)
	if summary["attempts"] != float64(4) || asMap(summary["knowledge"])["attempts"] != float64(4) {
		t.Fatal("knowledge/report attempt totals diverge", summary["attempts"], asMap(summary["knowledge"])["attempts"])
	}
	page, err := s.ListItems(t.Context(), o, id, store.Filters{Limit: 5})
	if err != nil || page.Items[0].Attempts+page.Items[1].Attempts != 4 {
		t.Fatal("persisted item attempt total changed", page, err)
	}
}

func TestReviewModelsRestoreOriginalDefaultRetry(t *testing.T) {
	calls := 0
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated temporary list failure"}}`)), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"restored-model"}]}`)), Request: r}, nil
	})
	ids, err := Models(t.Context(), Credential{BaseURL: "https://fixture.invalid", Protocol: "chat", Key: "placeholder"}, Deployment{}, fake)
	if err != nil || calls != 2 || len(ids) != 1 || ids[0] != "restored-model" {
		t.Fatal("model listing lost original retry", ids, err, calls)
	}
}

func TestReviewModelsExplicitZeroRetryStillUsesOneRequest(t *testing.T) {
	settings := store.DefaultSettings()
	settings.Retries = 0
	calls := 0
	fake := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"isolated temporary failure"}}`)), Request: r}, nil
	})
	_, err := Models(t.Context(), Credential{BaseURL: "https://fixture.invalid", Protocol: "chat", Key: "placeholder"}, Deployment{}, fake, settings)
	if err == nil || calls != 1 {
		t.Fatal("explicit no retry ignored", err, calls)
	}
}

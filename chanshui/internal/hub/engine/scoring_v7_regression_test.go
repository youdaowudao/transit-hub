package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

func scoringReport(t *testing.T, ids ...string) (*store.Store, store.Owner, store.Report, Credential) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	owner := store.Owner{UserID: "scoring-user", WorkspaceID: "scoring-scope"}
	settings, err := s.Settings(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	settings.Protocol, settings.Retries, settings.Concurrency = "chat", 0, 1
	if _, err = s.SaveSettings(t.Context(), owner, settings); err != nil {
		t.Fatal(err)
	}
	cred := Credential{BaseURL: "https://fixture.invalid", Protocol: "chat", Key: "isolated-key"}
	report, err := s.CreateReport(t.Context(), owner, store.CreateRequest{
		ReportID: strings.Repeat("b", 32), TargetID: "1", Model: "fixture", SelectedQuestionIDs: ids,
		Target: store.Target{BaseURL: cred.BaseURL, Protocol: cred.Protocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, report, cred
}

func readScoringReport(t *testing.T, s *store.Store, owner store.Owner, id string) (store.Report, map[string]any) {
	t.Helper()
	report, err := s.Report(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(report.Summary.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	return report, summary
}

func TestReportV7ConfirmedCacheZeroAndMissingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, usage, reportStatus string
		known                     bool
	}{
		{"explicit-zero", `{"prompt_tokens":200,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}`, "done", true},
		{"null-is-unknown", `{"prompt_tokens":200,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":null,"cache_write_tokens":0}}`, "partial", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, owner, report, cred := scoringReport(t, "cache")
			var calls atomic.Int32
			transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				body := fmt.Sprintf(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":%s}`, tc.usage)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			id := report.Summary.ReportID
			if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
				t.Fatal(err)
			}
			finished, summary := readScoringReport(t, s, owner, id)
			if calls.Load() != 6 || finished.Status != tc.reportStatus {
				t.Errorf("expected six calls and status %s, got %d and %s", tc.reportStatus, calls.Load(), finished.Status)
			}
			savedVerdict := asMap(summary["verdict"])
			for _, projection := range []map[string]any{asMap(summary["total"]), asMap(savedVerdict["total"]), summary} {
				found := false
				for _, row := range projection["parts"].([]any) {
					part := asMap(row)
					if part["key"] == "cache" {
						found = true
						if part["known"] != tc.known || part["coefficient"] != float64(100) {
							t.Errorf("cache score is inconsistent: %+v", part)
						}
					}
				}
				if !found {
					t.Error("cache part missing from a report projection")
				}
			}
			var score any
			if tc.known {
				score = float64(0)
			}
			if summary["totalScore"] != score || asMap(summary["total"])["score"] != score || asMap(savedVerdict["total"])["score"] != score {
				t.Errorf("expected all official totals to be %v, got %v, %v, %v", score, summary["totalScore"], asMap(summary["total"])["score"], asMap(savedVerdict["total"])["score"])
			}
		})
	}
}

func TestReportV7PublicCacheFieldsSurviveShortKey(t *testing.T) {
	for _, key := range []string{"cache", "known"} {
		t.Run(key, func(t *testing.T) {
			s, owner, report, cred := scoringReport(t, "cache")
			id := report.Summary.ReportID
			cred.Key = key
			transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
				const body = `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
				t.Fatal(err)
			}
			_, summary := readScoringReport(t, s, owner, id)
			for _, projection := range []map[string]any{asMap(summary["total"]), asMap(asMap(summary["verdict"])["total"]), summary} {
				confirmed := false
				for _, row := range projection["parts"].([]any) {
					part := asMap(row)
					confirmed = confirmed || part["key"] == "cache" && part["known"] == true
				}
				if !confirmed {
					t.Error("a public cache part identifier was lost")
				}
			}
			page, err := s.ListItems(t.Context(), owner, id, store.Filters{})
			if err != nil || len(page.Items) != 1 {
				t.Fatal("unable to read saved cache item", err)
			}
			var saved itemResult
			if err = json.Unmarshal(page.Items[0].Result, &saved); err != nil || saved.Judgment.Status != "known" {
				t.Fatal("saved cache judgment lost its public status", saved.Judgment.Status, err)
			}
		})
	}
}

func TestReportV7CountsEarnedHalfAndGradesQ4Locally(t *testing.T) {
	for _, body := range []string{"女儿非该父亲亲生", "最终答案：在题设条件下，女儿并非这位父亲的亲生女儿。"} {
		t.Run(body, func(t *testing.T) {
			s, owner, report, cred := scoringReport(t, "Q4", "Q5")
			var selfGrades atomic.Int32
			transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
				bodyRaw, _ := io.ReadAll(req.Body)
				switch {
				case strings.Contains(string(bodyRaw), "评分开始"):
					selfGrades.Add(1)
					return fixtureResponse(req, "14"), nil
				case strings.Contains(string(bodyRaw), "色盲"):
					return fixtureResponse(req, body), nil
				default:
					return fixtureResponse(req, "最终答案: 下降。"), nil
				}
			})
			id := report.Summary.ReportID
			if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
				t.Fatal(err)
			}
			_, summary := readScoringReport(t, s, owner, id)
			if selfGrades.Load() != 0 {
				t.Errorf("Q4 used %d extra self-grading requests", selfGrades.Load())
			}
			if summary["iqMax"] != float64(24) || summary["iqScore"] != float64(79) {
				t.Errorf("14 + 5 earned must normalize to 79 with paper max 24, got %v / %v", summary["iqScore"], summary["iqMax"])
			}
			if asMap(summary["verdict"])["scoring_version"] != "2026-11-19-v7-hub" {
				t.Error("new report did not carry the new scoring version")
			}
		})
	}
}

func TestReportV7CacheRetryKeepsOriginalIQAndLegacyVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		iqScore       float64
		totalUnknown  bool
	}{
		{"new", "2026-11-19-v7-hub", 50, true},
		{"old", "2026-10-02-v6-total", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, owner, report, cred := scoringReport(t, "cache", "Q5")
			id := report.Summary.ReportID
			if tc.name == "old" {
				if _, err := s.RuntimeDB().ExecContext(t.Context(), `UPDATE hub_reports SET summary_json='{}' WHERE report_id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			var iqCalls, cacheCalls atomic.Int32
			read := "null"
			transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
				bodyRaw, _ := io.ReadAll(req.Body)
				if strings.Contains(string(bodyRaw), "最终答案") {
					iqCalls.Add(1)
					return fixtureResponse(req, "最终答案: 下降。"), nil
				}
				cacheCalls.Add(1)
				body := fmt.Sprintf(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":200,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":%s,"cache_write_tokens":0}}}`, read)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			run := func() (store.Report, map[string]any) {
				t.Helper()
				if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
					t.Fatal(err)
				}
				return readScoringReport(t, s, owner, id)
			}
			_, before := run()
			if before["iqScore"] != tc.iqScore || (before["totalScore"] == nil) != tc.totalUnknown {
				t.Fatalf("unexpected initial IQ=%v total=%v, calls iq=%d cache=%d", before["iqScore"], before["totalScore"], iqCalls.Load(), cacheCalls.Load())
			}
			if _, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("c", 32), ItemIDs: []string{"cache"}}); err != nil {
				t.Fatal(err)
			}
			queued, carry := readScoringReport(t, s, owner, id)
			if queued.Status != "queued" || asMap(carry["verdict"])["scoring_version"] != tc.version || carry["iqScore"] != tc.iqScore {
				t.Fatalf("cache retry changed original score or rule: %v", carry)
			}
			if tc.name == "new" && asMap(asMap(carry["verdict"])["total"])["score"] != nil {
				t.Fatal("queued retry showed a formal total", carry)
			}
			read = "0"
			finished, after := run()
			if iqCalls.Load() != 1 || cacheCalls.Load() != 12 || asMap(after["verdict"])["scoring_version"] != tc.version || after["iqScore"] != tc.iqScore {
				t.Fatalf("cache-only retry modified the previous IQ: status=%s calls=%d/%d summary=%v", finished.Status, iqCalls.Load(), cacheCalls.Load(), after)
			}
			if tc.name == "new" && (finished.Status != "done" || after["totalScore"] == nil) {
				t.Fatal("complete cache result did not restore the total", after)
			}
		})
	}
}

func TestReportV7KeepsRuleWhenCredentialOverlapsMarker(t *testing.T) {
	s, owner, report, cred := scoringReport(t, "Q2")
	cred.Key = "v7"
	id := report.Summary.ReportID
	checkVersion := func(stage string) {
		current, err := s.Report(t.Context(), owner, id)
		if err != nil {
			t.Errorf("%s: %v", stage, err)
			return
		}
		if version := asMap(asMap(current.Summary.Summary)["verdict"])["scoring_version"]; version != "2026-11-19-v7-hub" {
			t.Errorf("%s scoring version changed: %v", stage, version)
		}
	}
	transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
		checkVersion("initial run")
		return fixtureResponse(req, "最终答案: 开车。v7"), nil
	})
	if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport, Notify: func(event string, _ map[string]any) {
		if event == "item-saved" {
			checkVersion("progress")
		}
	}}); err != nil {
		t.Fatal(err)
	}
	checkVersion("finished")
	items, err := s.ListItems(t.Context(), owner, id, store.Filters{Limit: 5})
	if err != nil || len(items.Items) != 1 {
		t.Fatalf("read saved answer: %v, %d items", err, len(items.Items))
	}
	answer, _ := asMap(asMap(items.Items[0].Result)["evidence"])["answer"].(string)
	if strings.Contains(answer, cred.Key) || !strings.Contains(answer, "[密钥已隐藏]") {
		t.Error("expected the returned credential text to be redacted in the saved answer")
	}
}

package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kiss-kedaya/chanshui/internal/api"
	"github.com/kiss-kedaya/chanshui/internal/hub/questions"
	"github.com/kiss-kedaya/chanshui/internal/hub/store"
)

const cacheAutoReason = "原缓存读数未确认"

func cacheScorePart(t *testing.T, summary map[string]any) map[string]any {
	t.Helper()
	parts, _ := asMap(summary["total"])["parts"].([]any)
	for _, raw := range parts {
		if part := asMap(raw); part["key"] == "cache" {
			return part
		}
	}
	t.Fatal("report is missing the cache score")
	return nil
}

func savedCacheJudgment(t *testing.T, s *store.Store, owner store.Owner, id string) (store.Item, json.RawMessage, map[string]any) {
	t.Helper()
	page, err := s.ListItems(t.Context(), owner, id, store.Filters{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.ItemID == "cache" {
			var saved struct {
				Evidence json.RawMessage
				Judgment map[string]any
			}
			if err := json.Unmarshal(item.Result, &saved); err != nil {
				t.Fatal(err)
			}
			return item, saved.Evidence, saved.Judgment
		}
	}
	t.Fatal("report is missing the saved cache item")
	return store.Item{}, nil, nil
}

func cacheJudgmentFixture(t *testing.T, model string, defs []questions.Definition, results map[string]itemResult, states map[string]string) (*store.Store, store.Owner, string, Credential) {
	t.Helper()
	s, owner, id, cred := configuredReport(t, defs, model)
	if err := s.BeginRun(t.Context(), owner, id); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := s.LoadRun(t.Context(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	for key, result := range results {
		if err := s.SaveItem(t.Context(), owner, id, key, states[key], result, 6); err != nil {
			t.Fatal(err)
		}
	}
	initial := finishedScores(snapshot.Questions, results, model, api.HubScoringVersion)
	if err := s.Finish(t.Context(), owner, id, "partial", initial, nil); err != nil {
		t.Fatal(err)
	}
	return s, owner, id, cred
}

func TestCacheManualJudgmentRescoresTotal(t *testing.T) {
	cache, _ := questions.Preset("cache")
	iq, _ := questions.Preset("Q5")
	results := map[string]itemResult{
		"cache": {Judgment: boolJudgment(nil, cacheAutoReason), Evidence: map[string]any{"pass": nil, "reason": cacheAutoReason, "samples": []int{0, 1, 0, 1, 0, 1}}},
		"Q5":    {Judgment: questions.Judgment{Status: "known", Earned: points(iq.MaxScore), Max: iq.MaxScore}},
	}
	s, owner, id, _ := cacheJudgmentFixture(t, "fixture", []questions.Definition{cache, iq}, results, map[string]string{"cache": "done", "Q5": "done"})
	_, initial := readScoringReport(t, s, owner, id)
	before, numeric := initial["totalScore"].(float64)
	part := cacheScorePart(t, initial)
	if !numeric || part["known"] != false || part["contribution"] != float64(0) {
		t.Errorf("expected a numeric total with no cache contribution: total=%v cache=%v", initial["totalScore"], part)
	}
	_, evidenceBefore, _ := savedCacheJudgment(t, s, owner, id)
	for _, verdict := range []string{"pass", "fail"} {
		_, err := s.JudgeItem(t.Context(), owner, store.JudgeRequest{ReportID: id, ItemID: "cache", Verdict: verdict}, RescoreSummary)
		if err != nil {
			t.Fatalf("manual cache %s: %v", verdict, err)
		}
		report, summary := readScoringReport(t, s, owner, id)
		part := cacheScorePart(t, summary)
		wantPoints, wantContribution := float64(0), float64(0)
		if verdict == "pass" {
			wantPoints = 1
			wantContribution = part["coefficient"].(float64)
		}
		if report.Status != "done" || part["known"] != true || part["points"] != wantPoints || part["contribution"] != wantContribution ||
			summary["totalScore"] != before+wantContribution || asMap(summary["total"])["score"] != summary["totalScore"] || asMap(asMap(summary["verdict"])["total"])["score"] != summary["totalScore"] {
			t.Errorf("manual cache %s should rescore the existing report: status=%s total=%v cache=%v", verdict, report.Status, summary["totalScore"], part)
		}
		item, evidence, judgment := savedCacheJudgment(t, s, owner, id)
		if string(evidence) != string(evidenceBefore) || item.Attempts != 6 || item.Status != "done" || judgment["autoReason"] != cacheAutoReason ||
			judgment["source"] != "manual" || judgment["earned"] != wantPoints || judgment["max"] != float64(1) {
			t.Errorf("manual cache %s changed automatic evidence or saved the wrong judgment: evidence=%s judgment=%v", verdict, evidence, judgment)
		}
	}
}

func TestCacheManualJudgmentSurvivesRetryOfOtherItem(t *testing.T) {
	cache, _ := questions.Preset("cache")
	iq, _ := questions.Preset("Q5")
	results := map[string]itemResult{
		"cache": {Judgment: boolJudgment(nil, cacheAutoReason), Evidence: map[string]any{"pass": nil, "reason": cacheAutoReason}},
		"Q5":    {Judgment: questions.Judgment{Status: "unknown", Max: iq.MaxScore}, Error: "隔离推理请求失败"},
	}
	s, owner, id, cred := cacheJudgmentFixture(t, "fixture", []questions.Definition{cache, iq}, results, map[string]string{"cache": "done", "Q5": "error"})
	if _, err := s.JudgeItem(t.Context(), owner, store.JudgeRequest{ReportID: id, ItemID: "cache", Verdict: "pass"}, RescoreSummary); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryReport(t.Context(), owner, store.RetryRequest{ReportID: id, RetryID: strings.Repeat("e", 32), ItemIDs: []string{"Q5"}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	transport := fixtureTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse(req, "最终答案: 下降。"), nil
	})
	if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	report, summary := readScoringReport(t, s, owner, id)
	item, _, judgment := savedCacheJudgment(t, s, owner, id)
	part := cacheScorePart(t, summary)
	if calls != 1 || report.Status != "done" || part["known"] != true || part["points"] != float64(1) || part["contribution"] != part["coefficient"] || item.Attempts != 6 || judgment["source"] != "manual" {
		t.Errorf("retry did not retain the manual cache pass: calls=%d status=%s cache=%v judgment=%v", calls, report.Status, part, judgment)
	}
}

func TestCacheAndModelIdentityUnconfirmedKeepAllTotalsEmpty(t *testing.T) {
	cache, _ := questions.Preset("cache")
	defs := []questions.Generated{{Definition: cache}, identityDefinition()}
	results := map[string]itemResult{
		"cache": {Judgment: boolJudgment(nil, cacheAutoReason)},
		"Q10":   {Judgment: questions.Judgment{Status: "unknown", Max: 5}},
	}
	check := func(t *testing.T, summary map[string]any) {
		t.Helper()
		total := asMap(summary["total"])
		verdictTotal := asMap(asMap(summary["verdict"])["total"])
		if summary["totalScore"] != nil || total["score"] != nil || verdictTotal["score"] != nil || total["reason"] != questions.ModelIdentityPending || verdictTotal["reason"] != questions.ModelIdentityPending {
			t.Fatalf("pending model identity should hide all totals: %v", summary)
		}
	}
	t.Run("finished", func(t *testing.T) {
		check(t, finishedScores(defs, results, "gpt-6.2-sol", api.HubScoringVersion))
	})
	t.Run("rescored", func(t *testing.T) {
		items := map[string]store.Item{}
		for key, result := range results {
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			items[key] = store.Item{ItemID: key, Status: "done", Result: raw}
		}
		summary, err := RescoreSummary(defs, items, "gpt-6.2-sol", api.HubScoringVersion)
		if err != nil {
			t.Fatal(err)
		}
		check(t, summary)
	})
}

func TestFailedCacheStillPublishesPartialTotal(t *testing.T) {
	s, owner, created, cred := scoringReport(t, "cache")
	id := created.Summary.ReportID
	transport := fixtureTransport(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("隔离缓存请求失败")
	})
	if err := RunOne(t.Context(), s, owner, id, cred, Config{DisableBankSync: true, Transport: transport}); err != nil {
		t.Fatal(err)
	}
	report, summary := readScoringReport(t, s, owner, id)
	item, _, _ := savedCacheJudgment(t, s, owner, id)
	part := cacheScorePart(t, summary)
	if report.Status != "partial" || item.Status != "error" || summary["totalScore"] != float64(0) || part["known"] != false || part["contribution"] != float64(0) {
		t.Fatalf("failed cache should publish zero with a partial status, got %s/%s total=%v cache=%v", report.Status, item.Status, summary["totalScore"], part)
	}
}

func TestSavedAutomaticCacheGradeWithoutPassRemainsUnconfirmed(t *testing.T) {
	cache, _ := questions.Preset("cache")
	items := map[string]store.Item{"cache": {ItemID: "cache", Status: "done", Result: json.RawMessage(`{"judgment":{"status":"known","earned":1,"max":1},"evidence":{}}`)}}
	summary, err := RescoreSummary([]questions.Generated{{Definition: cache}}, items, "fixture", api.HubScoringVersion)
	if err != nil {
		t.Fatal(err)
	}
	part := cacheScorePart(t, summary)
	if summary["complete"] != false || part["known"] != false || part["contribution"] != float64(0) {
		t.Fatalf("automatic judgment without a pass became a cache score: %v", summary)
	}
}

func TestMultipleCacheItemsUseLastApplicableResultAfterManualJudgment(t *testing.T) {
	cache, _ := questions.Preset("cache")
	for _, manualLast := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_last_%t", manualLast), func(t *testing.T) {
			other := cache
			other.ID, other.Title = "custom:0123456789abcdef0123456789abcdef", "隔离缓存副本"
			if manualLast {
				other.Order = cache.Order - 1
			} else {
				other.Order = cache.Order + 1
			}
			failed := false
			results := map[string]itemResult{
				"cache":  {Judgment: boolJudgment(nil, cacheAutoReason), Evidence: map[string]any{"pass": nil}},
				other.ID: {Judgment: boolJudgment(&failed, "自动未通过"), Evidence: map[string]any{"pass": false}},
			}
			s, owner, id, _ := cacheJudgmentFixture(t, "fixture", []questions.Definition{cache, other}, results, map[string]string{"cache": "done", other.ID: "done"})
			_, before := readScoringReport(t, s, owner, id)
			if cacheScorePart(t, before)["known"] != false {
				t.Fatal("one unconfirmed item must leave the category unconfirmed")
			}
			if _, err := s.JudgeItem(t.Context(), owner, store.JudgeRequest{ReportID: id, ItemID: "cache", Verdict: "pass"}, RescoreSummary); err != nil {
				t.Fatal(err)
			}
			report, summary := readScoringReport(t, s, owner, id)
			part := cacheScorePart(t, summary)
			want := float64(0)
			if manualLast {
				want = 100
			}
			if report.Status != "done" || part["known"] != true || part["contribution"] != want || summary["totalScore"] != want {
				t.Errorf("cache category did not score the last applicable item: status=%s total=%v cache=%v", report.Status, summary["totalScore"], part)
			}
		})
	}
}

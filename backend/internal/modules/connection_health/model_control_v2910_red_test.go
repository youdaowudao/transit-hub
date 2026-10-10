package connection_health

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Business expectations authored before V2.9.11 implementation. PostgreSQL
// cases explicitly opt in via the existing isolated-schema fixture.
func TestModelControlV2911CompletedEvidenceOnlyUsesBusinessToday(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 1, 0, 0, questionAnswerScheduleLocation)
	rule := ModelControlRule{MinAccuracyPercent: 50, MinJudgedAnswers: 3, IncludeManual: true, IncludeScheduled: true}
	target := modelControlTarget{Observation: modelControlObservation{State: "serving"}}
	round := modelControlRound{BatchID: "yesterday", Source: "manual", CreatedAt: now.Add(-2 * time.Minute), Incorrect: 3}
	item := buildModelControlItem(target, rule, []modelControlRound{round}, nil, now)
	if item.Decision != "no_evidence" || item.DecisionReason != "not_today" || modelControlNeedsAttention(item) {
		t.Fatalf("old evidence suggested a closure: decision=%s reason=%s attention=%v", item.Decision, item.DecisionReason, modelControlNeedsAttention(item))
	}
	if item.Round == nil || item.Round.BatchID != "yesterday" {
		t.Fatal("last round must remain available for explanation")
	}
	target.ConflictReason = modelControlError("RestoreMappingEmpty")
	if !modelControlNeedsAttention(buildModelControlItem(target, rule, []modelControlRound{round}, nil, now)) {
		t.Fatal("date filtering hid a safety conflict")
	}
	round.CreatedAt = now
	if item = buildModelControlItem(target, rule, []modelControlRound{round}, nil, now); item.Decision != "close_recommended" {
		t.Fatal("today's failed round must still suggest closure")
	}
}

func TestModelControlV2911RunningYesterdayStillBlocksRestore(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 1, 0, 0, questionAnswerScheduleLocation)
	rule := ModelControlRule{MinAccuracyPercent: 50, MinJudgedAnswers: 3, IncludeManual: true, IncludeScheduled: true}
	target := modelControlTarget{ClosedEntries: map[string]string{"alias": "M"}}
	round := modelControlRound{BatchID: "running", Source: "manual", CreatedAt: now.Add(-24 * time.Hour), Running: true}
	item := buildModelControlItem(target, rule, []modelControlRound{round}, nil, now)
	if item.Decision != "testing" || modelControlAdmission(item, "restore") == nil {
		t.Fatal("a running round must remain protected across midnight")
	}
}

func TestModelControlV2911UnchangedDecisionAcrossMidnightInvalidatesBasis(t *testing.T) {
	var before, after modelControlBasis
	if err := json.Unmarshal([]byte(`{"batchId":"old","ruleVersion":1,"decision":"no_evidence","businessDay":"2026-10-10"}`), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"batchId":"old","ruleVersion":1,"decision":"no_evidence","businessDay":"2026-10-11"}`), &after); err != nil {
		t.Fatal(err)
	}
	if modelControlBasisMatches(before, after) {
		t.Fatal("unchanged decision allowed a preview from the previous business day")
	}
}

func TestModelControlV2911SettingsAndAddRoutes(t *testing.T) {
	f := newC3REDFixture(t)
	f.expect("GET", "model-control/settings", nil, 200)
	if code, _ := f.request("GET", "model-control/rules", nil); code != 404 {
		t.Fatalf("retired rule route status=%d", code)
	}
	f.expect("PUT", "model-control/settings", map[string]any{"minAccuracyPercent": 60, "minJudgedAnswers": 3, "expectedVersion": 0}, 200)
	f.mapping("1", map[string]string{"b": "B"})
	f.round("1", "A", 3, 0, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "add")
	code, result := f.execute("1", "A", "add", p)
	if code != 200 || result["outcome"] != "added" || f.writeCount() != 1 {
		t.Fatalf("add result=%d %v writes=%d", code, result, f.writeCount())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mapping := f.accounts["1"]["credentials"].(map[string]any)["model_mapping"].(map[string]any)
	if mapping["A"] != "A" || mapping["b"] != "B" || len(mapping) != 2 {
		t.Fatal("add changed an existing entry")
	}
}

func TestModelControlV2911RuleChangesDuringReadNeverWrite(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "close")
	f.beforeDetail = func() {
		f.mu.Lock()
		f.beforeDetail = nil
		f.mu.Unlock()
		basis := p["item"].(map[string]any)["basis"].(map[string]any)
		f.expect("PUT", "model-control/settings", map[string]any{"minAccuracyPercent": 60, "minJudgedAnswers": 3, "expectedVersion": basis["ruleVersion"]}, 200)
	}
	code, result := f.execute("1", "A", "close", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "BasisChanged") || f.writeCount() != 0 {
		t.Fatalf("changed rules wrote main: status=%d result=%v writes=%d", code, result, f.writeCount())
	}
	if f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
		t.Fatal("conflict created a new pending operation")
	}
}

func TestModelControlV2911CountsIgnoreAttentionAndModelFilter(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.round("1", "B", 3, 0, 0)
	f.manage("1", "A")
	f.manage("1", "B")
	result := f.expect("GET", "model-control/items?view=attention&modelName=A&pageSize=1", nil, 200)
	counts, ok := result["counts"].(map[string]any)
	if !ok || counts["total"] != float64(2) || counts["open"] != float64(2) || counts["attention"] != float64(1) {
		t.Fatalf("filtered global counts=%v", result["counts"])
	}
}

func TestModelControlV2911BusyIsNotAnUnfinishedWrite(t *testing.T) {
	f := newC3REDFixture(t)
	f.round("1", "A", 1, 2, 0)
	f.manage("1", "A")
	_, release, acquired, err := f.service.acquireActionTargetLease(t.Context(), c3REDTarget("1"), false)
	if err != nil || !acquired {
		t.Fatal("fixture could not hold the account lock")
	}
	defer release()
	errors := f.verify("1")["errors"].([]any)
	if len(errors) != 1 || errors[0].(map[string]any)["reasonKey"] != modelControlError("Busy") {
		t.Fatalf("busy read mislabeled as write: %v", errors)
	}
	if f.writeCount() != 0 {
		t.Fatal("read-only verification wrote main")
	}
}

func TestModelControlV2911RestoreFromPreviousBusinessDayNeverWrites(t *testing.T) {
	f := newC3REDFixture(t)
	f.manage("1", "A")
	f.mapping("1", map[string]string{"b": "B"})
	if _, err := f.pool.Exec(t.Context(), `UPDATE connection_health_model_control_targets SET closed_entries='{"a":"A"}' WHERE target_id=$1`, c3REDTarget("1")); err != nil {
		t.Fatal(err)
	}
	p := f.preview("1", "A", "restore")
	p["item"].(map[string]any)["basis"].(map[string]any)["businessDay"] = time.Now().In(questionAnswerScheduleLocation).AddDate(0, 0, -1).Format("2006-01-02")
	code, result := f.execute("1", "A", "restore", p)
	if code != 409 || !strings.Contains(fmt.Sprint(result), "BasisChanged") || f.writeCount() != 0 {
		t.Fatalf("expired restore basis sent a write: code=%d writes=%d result=%v", code, f.writeCount(), result)
	}
	if f.item("1", "A")["control"].(map[string]any)["pending"] != nil {
		t.Fatal("old day created a pending operation")
	}
}

func TestModelControlV2911AddPreviewIsSupported(t *testing.T) {
	f := newC3REDFixture(t)
	f.mapping("1", map[string]string{"b": "B"})
	f.round("1", "A", 3, 0, 0)
	f.manage("1", "A")
	p := f.preview("1", "A", "add")
	code, result := f.execute("1", "A", "add", p)
	if code != 200 || result["outcome"] != "added" || f.writeCount() != 1 {
		t.Fatalf("add unavailable: code=%d result=%v", code, result)
	}
}

package chanshui

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestBatchETAFromCompleteComparableFirstRunSamples(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	samples := []map[string]any{}
	for _, ms := range []int64{20000, 30000, 40000} {
		samples = append(samples, map[string]any{"phase": "ended", "status": "done", "etaKey": "same", "summaryComplete": true, "timing": map[string]any{"runs": 1, "last_run_ms": ms, "started_at": stamp(-time.Minute), "finished_at": stamp(-time.Second)}})
	}
	rows := append(samples, map[string]any{"phase": "running", "etaKey": "same", "totalTimeoutSeconds": 60, "timing": map[string]any{"started_at": stamp(-10 * time.Second)}}, map[string]any{"phase": "queued", "etaKey": "same"})
	counts := batchCounts{Total: 5, Ended: 3, Active: 1, Waiting: 1}
	view := runtimeSnapshot{now: now.UnixMilli(), members: map[string]memberSnapshot{"current": {batchID: "batch", phase: "running"}, "next": {batchID: "batch", phase: "queued", waiting: &waitingTask{}, queuePosition: 1}}, active: &memberSnapshot{batchID: "batch"}}
	result, reason := batchETA("batch", rows, counts, "running", view, view)
	if result == nil || reason != "" || result.(map[string]any)["lowMs"] != int64(10000) || result.(map[string]any)["highMs"] != int64(220000) {
		t.Fatal(result, reason)
	}
	changed := counts
	changed.Retrying = 1
	if v, _ := batchETA("batch", rows, changed, "running", view, view); v != nil {
		t.Fatal("retry produced comparable ETA")
	}
	blocked := view
	blocked.queueBlocked = true
	if v, _ := batchETA("batch", rows, counts, "running", view, blocked); v != nil {
		t.Fatal("blocked queue produced ETA")
	}
	other := view
	other.otherActive = true
	if v, _ := batchETA("batch", rows, counts, "running", view, other); v != nil {
		t.Fatal("other active task produced ETA")
	}
	rows[0]["summaryComplete"] = false
	if v, _ := batchETA("batch", rows, counts, "running", view, view); v != nil {
		t.Fatal("two confirmed samples produced ETA")
	}
	rows[0]["summaryComplete"] = true
	for _, r := range samples {
		r["timing"].(map[string]any)["last_run_ms"] = int64(math.MaxInt64/2) - 75000
	}
	if v, _ := batchETA("batch", rows, counts, "running", view, view); v != nil {
		t.Fatal("overflow yielded ETA")
	}
}
func TestBatchResponseBudgetsAfterLifecycleProjection(t *testing.T) {
	members := []map[string]any{}
	rows := []map[string]any{}
	for i := 0; i < 4; i++ {
		id := strings.Repeat(string(rune('a'+i)), 32)
		members = append(members, map[string]any{"reportId": id, "phase": "running"})
		rows = append(rows, map[string]any{"reportId": id, "recoveryReason": strings.Repeat("错", 4000)})
	}
	value := map[string]any{"batchId": strings.Repeat("x", 32), "memberStates": members, "items": rows, "runtime": map[string]any{}}
	if err := boundBatchResponse(value); err != nil {
		t.Fatal(err)
	}
	if len(mapsField(value, "items")) != 3 || value["nextOffset"] != 3 {
		t.Fatal("page did not consume exact retained member indexes", len(mapsField(value, "items")), value["nextOffset"])
	}
	members[0]["tooLong"] = strings.Repeat("错", 200)
	if err := boundBatchResponse(value); err == nil {
		t.Fatal("oversized member state accepted")
	}
	delete(members[0], "tooLong")
	value["tooLong"] = strings.Repeat("错", 9000)
	if err := boundBatchResponse(value); err == nil {
		t.Fatal("oversized base accepted")
	}
	delete(value, "tooLong")
	value["runtime"] = map[string]any{"tooLong": strings.Repeat("错", 1500)}
	if err := boundBatchResponse(value); err == nil {
		t.Fatal("oversized runtime accepted")
	}
}

func TestBatchProjectionCountsStableActiveAndRejectsChangedMember(t *testing.T) {
	rows := []any{
		map[string]any{"reportId": "done", "status": "done", "summaryComplete": true},
		map[string]any{"reportId": "partial", "status": "partial", "summaryComplete": false},
		map[string]any{"reportId": "active", "status": "running"},
		map[string]any{"reportId": "waiting", "status": "queued"},
		map[string]any{"reportId": "cancel", "status": "cancelled"},
		map[string]any{"reportId": "missing", "status": "missing"},
	}
	active := &activeTask{}
	waiting := &waitingTask{}
	blocked := &blockedReport{}
	view := runtimeSnapshot{members: map[string]memberSnapshot{
		"active":  {active: active, batchID: "batch", phase: "unknown"},
		"waiting": {waiting: waiting, batchID: "batch", phase: "queued"},
		"cancel":  {blocked: blocked, batchID: "batch", phase: "saving_cancel"},
	}, recoveryRunning: true}
	detail := map[string]any{"batchId": "batch", "memberStates": rows, "items": []any{}}
	projectBatch(detail, view, view, true)
	counts := detail["counts"].(batchCounts)
	if counts.Total != 6 || counts.Ended != 2 || counts.Active != 1 || counts.Waiting != 1 || counts.Unconfirmed != 2 || counts.Complete != 1 || counts.Partial != 1 || detail["state"] != "unknown" {
		t.Fatal(counts, detail["state"])
	}
	changed := view
	changed.members = map[string]memberSnapshot{
		"active":  {active: &activeTask{}, batchID: "batch", phase: "running"},
		"waiting": view.members["waiting"], "cancel": view.members["cancel"],
	}
	projectBatch(detail, view, changed, true)
	counts = detail["counts"].(batchCounts)
	if counts.Active != 0 || counts.Unconfirmed != 3 || detail["eta"] != nil {
		t.Fatal("changed member looked confirmed", counts, detail["eta"])
	}
	healthy := changed
	projectBatch(detail, healthy, healthy, true)
	if detail["state"] != "running" {
		t.Fatal("healthy active was hidden by another waiting cancellation", detail["state"])
	}
	old := map[string]any{"batchId": "old", "memberStates": []any{map[string]any{"reportId": "done", "status": "done", "summaryComplete": true}}}
	healthy.queueBlocked = true
	projectBatch(old, healthy, healthy, false)
	if old["state"] != "ended" {
		t.Fatal("unrelated queue failure changed fully ended batch")
	}
}

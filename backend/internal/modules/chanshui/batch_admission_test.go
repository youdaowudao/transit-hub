package chanshui

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// Successful creation has the original compact 5-field response; comparing an
// existing frozen request includes member statuses. All operations below are
// in-memory, never start real processes or query an upstream.
type batchAdmissionExecutor struct {
	*queueFixtureExecutor
	batchMu     sync.Mutex
	request     *BatchInput
	owner       Owner
	createCount int
}

func (f *batchAdmissionExecutor) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	if e.Operation != "create-batch" && e.Operation != "read-batch" {
		return f.queueFixtureExecutor.Query(ctx, e, c, internal)
	}
	f.batchMu.Lock()
	defer f.batchMu.Unlock()
	var args struct {
		BatchID string        `json:"batchId"`
		Request *BatchInput   `json:"request"`
		Targets []batchTarget `json:"targets"`
	}
	raw, _ := json.Marshal(e.Args)
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if e.Operation == "read-batch" {
		if f.request == nil || e.Owner != f.owner || args.BatchID != f.request.BatchID {
			return nil, problem(404, "batch", "找不到批次")
		}
		if mustJSON(args.Request) != mustJSON(f.request) {
			return nil, problem(409, "batch", "同号请求不同")
		}
	} else {
		if f.request != nil || args.Request == nil || len(args.Targets) != 1 {
			return nil, problem(503, "fixture", "批次参数重复或目标资料未复用")
		}
		f.createCount++
		f.request = args.Request
		f.owner = e.Owner
		f.mu.Lock()
		for _, task := range f.request.Tasks {
			f.statuses[task.ReportID] = "queued"
			f.owners[task.ReportID] = e.Owner
			f.requests[task.ReportID] = task.start(*f.request)
		}
		f.mu.Unlock()
	}
	ids := []string{}
	states := []map[string]any{}
	f.mu.Lock()
	for _, task := range f.request.Tasks {
		ids = append(ids, task.ReportID)
		states = append(states, map[string]any{"reportId": task.ReportID, "status": f.statuses[task.ReportID]})
	}
	f.mu.Unlock()
	accepted := map[string]any{"batchId": f.request.BatchID, "reportIds": ids, "settingsRevision": f.request.SettingsRevision, "memoryLimitMiB": 128, "totalTimeoutSeconds": 60}
	if e.Operation == "read-batch" {
		accepted["memberStates"] = states
	}
	return fakeResponse(accepted), nil
}
func TestBatchAdmissionReusesAccountContextAndOriginalFIFO(t *testing.T) {
	s, base, targets := queueFixture(t)
	f := &batchAdmissionExecutor{queueFixtureExecutor: base}
	s.runner = f
	owner := Owner{"user-a", "workspace-a"}
	a, b, c := queueInput("a"), queueInput("b"), queueInput("c")
	if _, err := s.Start(t.Context(), owner, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, base, a.ReportID)
	before := targets.calls.Load()
	input := BatchInput{BatchID: queueInput("d").ReportID, SettingsRevision: 1, SelectedSectionIDs: []string{"iq", "iq"}, Tasks: []BatchTaskInput{
		{ReportID: b.ReportID, TargetID: b.TargetID, GroupID: b.GroupID, Model: "model-B", ExpectedContext: b.ExpectedContext},
		{ReportID: c.ReportID, TargetID: c.TargetID, GroupID: c.GroupID, Model: "model-C", ExpectedContext: c.ExpectedContext},
	}}
	accepted, err := s.StartBatch(t.Context(), owner, input)
	if err != nil || accepted.(map[string]any)["accepted"] != true {
		t.Fatal(accepted, err)
	}
	if targets.calls.Load() != before+1 {
		t.Fatal("same account/group was resolved more than once at admission")
	}
	if _, err = s.StartBatch(t.Context(), owner, input); err != nil {
		t.Fatal(err)
	}
	if targets.calls.Load() != before+1 {
		t.Fatal("same-ID confirmation resolved another credential")
	}
	f.mu.Lock()
	if len(f.order) != 1 {
		f.mu.Unlock()
		t.Fatal("batch ran concurrently with earlier single report")
	}
	f.mu.Unlock()
	base.release(a.ReportID, true)
	nextStarted(t, base, b.ReportID)
	base.release(b.ReportID, true)
	nextStarted(t, base, c.ReportID)
	base.release(c.ReportID, true)
	waitRecovery(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil && !s.recoveryRunning })
	f.batchMu.Lock()
	created := f.createCount
	f.batchMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if created != 1 || len(f.order) != 3 || f.maxRunning != 1 {
		t.Fatal("batch was duplicated or overlapped", f.order, created)
	}
}

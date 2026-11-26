package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/config"
)

// A simulated lost response does not mean CreateBatch failed: persistence has
// committed before the error is returned. Keep the original IDs and assert
// that no model dispatch occurs even upon an explicit same-ID confirmation.
type batchContractExecutor struct {
	*queueFixtureExecutor
	batchMu                     sync.Mutex
	request                     *BatchInput
	owner                       Owner
	createCalls, interruptCalls int
	lostResponse                bool
}

func (f *batchContractExecutor) Query(ctx context.Context, e Envelope, c *Credential, internal bool) (*QueryResponse, error) {
	if e.Operation != "read-batch" && e.Operation != "create-batch" && e.Operation != "interrupt-batch-unstarted" {
		return f.queueFixtureExecutor.Query(ctx, e, c, internal)
	}
	f.batchMu.Lock()
	defer f.batchMu.Unlock()
	raw, _ := json.Marshal(e.Args)
	var args struct {
		BatchID string      `json:"batchId"`
		Request *BatchInput `json:"request"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	switch e.Operation {
	case "create-batch":
		f.createCalls++
		if args.Request == nil {
			return nil, fmt.Errorf("missing frozen request")
		}
		if f.request != nil {
			return nil, fmt.Errorf("duplicate creation attempt")
		}
		f.request = args.Request
		f.owner = e.Owner
		f.mu.Lock()
		for _, task := range f.request.Tasks {
			f.statuses[task.ReportID] = "queued"
			f.owners[task.ReportID] = e.Owner
		}
		f.mu.Unlock()
		if f.lostResponse {
			return nil, problem(503, "query", "模拟整批保存成功后返回结果中断")
		}
	case "read-batch":
		if f.request == nil || f.owner != e.Owner || args.BatchID != f.request.BatchID {
			return nil, problem(404, "batch", "没有找到批次")
		}
		if args.Request != nil && mustJSON(args.Request) != mustJSON(f.request) {
			return nil, problem(409, "idempotency", "同号请求不同")
		}
	case "interrupt-batch-unstarted":
		f.interruptCalls++
		if f.request == nil {
			return nil, problem(404, "batch", "没有找到批次")
		}
		f.mu.Lock()
		for _, task := range f.request.Tasks {
			if f.statuses[task.ReportID] == "queued" {
				f.statuses[task.ReportID] = "interrupted"
			}
		}
		f.mu.Unlock()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []string{}
	states := []map[string]any{}
	allTerminal := true
	for _, task := range f.request.Tasks {
		status := f.statuses[task.ReportID]
		if status == "queued" || status == "running" {
			allTerminal = false
		}
		ids = append(ids, task.ReportID)
		states = append(states, map[string]any{"reportId": task.ReportID, "status": status, "summaryComplete": false})
	}
	firstFinished := int64(0)
	if allTerminal {
		firstFinished = 123456
	}
	return fakeResponse(map[string]any{"batchId": f.request.BatchID, "reportIds": ids, "settingsRevision": f.request.SettingsRevision, "memoryLimitMiB": 128, "totalTimeoutSeconds": 60, "memberStates": states, "firstFinishedAtMs": firstFinished}), nil
}

func TestBatchContractLostCreateResponseNeverDispatchesOrReplays(t *testing.T) {
	f := &batchContractExecutor{queueFixtureExecutor: &queueFixtureExecutor{statuses: map[string]string{}, owners: map[string]Owner{}, requests: map[string]StartInput{}, jobs: map[string]*heldQueueJob{}, started: make(chan string, 32)}, lostResponse: true}
	targets := &queuedFixtureTargets{}
	s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, targets: targets, runner: f, initialized: true}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	owner := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	input := BatchInput{BatchID: queueInput("c").ReportID, SettingsRevision: 1, SelectedSectionIDs: []string{"iq"}, SelectedQuestionIDs: []string{"Q2"}, Tasks: []BatchTaskInput{
		{ReportID: a.ReportID, TargetID: a.TargetID, GroupID: a.GroupID, Model: "fixture-A", ExpectedContext: a.ExpectedContext},
		{ReportID: b.ReportID, TargetID: b.TargetID, GroupID: b.GroupID, Model: "fixture-B", ExpectedContext: b.ExpectedContext},
	}}
	if _, err := s.StartBatch(t.Context(), owner, input); err == nil {
		t.Fatal("lost response falsely reported complete acceptance")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(f.status(a.ReportID) == "interrupted" && f.status(b.ReportID) == "interrupted") {
		time.Sleep(10 * time.Millisecond)
	}
	if f.status(a.ReportID) != "interrupted" || f.status(b.ReportID) != "interrupted" {
		t.Fatal("saved but unregistered batch not recovered by original IDs")
	}
	if _, err := s.StartBatch(t.Context(), owner, input); err != nil {
		t.Fatalf("saved original batch cannot be confirmed: %v", err)
	}
	input.Tasks[0].Model = "different"
	if _, err := s.StartBatch(t.Context(), owner, input); err == nil {
		t.Fatal("changed request accepted on original batch ID")
	}
	f.batchMu.Lock()
	if f.createCalls != 1 || f.interruptCalls != 1 {
		t.Errorf("unexpected repeated persistence: create %d finish %d", f.createCalls, f.interruptCalls)
	}
	f.batchMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 0 {
		t.Fatalf("unknown creation dispatched/replayed a model: %v", f.order)
	}
}

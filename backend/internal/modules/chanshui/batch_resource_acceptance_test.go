package chanshui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/connection_health"
	"transithub/backend/internal/modules/upstream"
)

// Explicitly opted in, real query slot and SQLite with an isolated fixture.
// No model credentials are delivered and no upstream server is contacted.
func TestChanshuiActualBatchFullBankAndLargeSnapshotResourceBudget(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit actual batch resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := actualSlice(t)
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-batch-resource-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	if staged := os.Getenv("TRANSITHUB_CHANSHUI_BATCH_STAGED_BINARY"); staged != "" {
		if !within(filepath.Join(root, ".cache"), staged) {
			t.Fatal("staged resource candidate must be in project .cache")
		}
		binary = staged
	}
	runner := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: binary, DataDir: dir, ProjectDir: root, Manager: "user"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := runner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		assertActualEmpty(t, conn)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	owner := Owner{"batch-resource-fixture", "batch-isolated-workspace"}
	var maxPeak uint64
	query := func(t *testing.T, op string, args any) map[string]any {
		t.Helper()
		res, err := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, nil, true)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		raw, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if b, ok := res.Body.(*queryBody); ok {
			info := b.p.info()
			if info.MemoryPeak > maxPeak {
				maxPeak = info.MemoryPeak
			}
			if info.OOM || info.MemoryPeak > 64<<20 || !info.Exited {
				t.Fatalf("%s peak/exit contract: %+v", op, info)
			}
		}
		if readErr != nil || closeErr != nil {
			t.Fatalf("%s read %v close %v", op, readErr, closeErr)
		}
		if len(raw) > 64<<10 {
			t.Fatalf("%s returned %d bytes", op, len(raw))
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("%s response %v", op, err)
		}
		return result
	}
	query(t, "init", map[string]any{})
	settings := query(t, "read-settings", map[string]any{})
	// 32 large text items at the existing allowed IQ limit. The exact
	// snapshot includes template, prompt, definitions and numeric seeds.
	// 7168-byte ASCII prompt gives > 448KiB before metadata.
	// Also exercise the complete actual default bank, including the original
	// vision image. PNGs are not included in the 512KiB JSON limit.
	t.Run("near_snapshot_limit", func(t *testing.T) {
		ids := make([]string, 32)
		// Use an isolated owner with the preset bank disabled for custom IQ:
		// remove presets from the selection only; disable existing IQ to make
		// all 32 custom items legal without replacing preset definitions.
		page := query(t, "list-question-summaries", map[string]any{"kind": "iq", "limit": 50})
		for _, v := range page["items"].([]any) {
			row := v.(map[string]any)
			if row["enabled"] == true {
				definition := query(t, "read-question", map[string]any{"id": row["id"]})
				definition["enabled"] = false
				query(t, "save-question", map[string]any{"definition": definition, "revision": row["revision"]})
			}
		}
		for i := range ids {
			ids[i] = fmt.Sprintf("custom:%032x", i+1)
			query(t, "save-question", map[string]any{"definition": map[string]any{"id": ids[i], "title": fmt.Sprintf("批次资源题%d", i), "kind": "iq", "enabled": true, "order": i, "maxScore": 0, "template": strings.Repeat("A", 7168), "parameters": map[string]any{"generator": "static"}, "judge": map[string]any{"type": "manual"}}, "revision": 0})
		}
		testBatchResourceCreate(t, query, settings, ids, 1)
	})
	t.Run("complete_original_bank", func(t *testing.T) {
		// Use the real preset bank in a fresh isolated scope, not artificial
		// duplicates of the original chart.
		owner = Owner{"batch-full-bank-fixture", "batch-isolated-workspace"}
		settings := query(t, "read-settings", map[string]any{})
		ids := []string{}
		cursor := ""
		visionCount := 0
		for {
			page := query(t, "list-question-summaries", map[string]any{"limit": 50, "cursor": cursor})
			for _, v := range page["items"].([]any) {
				row := v.(map[string]any)
				if row["enabled"] == true || row["id"] == "cache" && row["revision"] == float64(1) {
					ids = append(ids, row["id"].(string))
					if row["kind"] == "vision" {
						visionCount++
					}
				}
			}
			cursor, _ = page["nextCursor"].(string)
			if cursor == "" {
				break
			}
		}
		if len(ids) < 30 || visionCount < 1 {
			t.Fatalf("real original bank incomplete: %d items, %d vision", len(ids), visionCount)
		}
		t.Logf("actual full preset bank: %d selected items, %d vision; 32 reports", len(ids), visionCount)
		testBatchResourceCreate(t, query, settings, ids, 2)
	})
	t.Logf("original 64MiB query limit: maximum observed peak %d bytes", maxPeak)
}

func testBatchResourceCreate(t *testing.T, query func(*testing.T, string, any) map[string]any, settings map[string]any, ids []string, seq int) {
	t.Helper()
	batchID := fmt.Sprintf("%032x", seq)
	tasks := make([]map[string]any, 32)
	targets := make([]map[string]any, 32)
	for i := range tasks {
		targetID := fmt.Sprintf("sub2api:batch-isolated-workspace:fixture-%d", i)
		tasks[i] = map[string]any{"reportId": fmt.Sprintf("%032x", 10000*seq+i), "targetId": targetID, "groupId": "fixture-group", "model": fmt.Sprintf("%032d", i) + strings.Repeat("m", 224), "expectedContext": map[string]any{"workspaceId": "batch-isolated-workspace", "targetId": targetID, "groupId": "fixture-group", "protocol": "chat", "baseURL": "https://fixture.invalid"}}
		targets[i] = map[string]any{"targetId": targetID, "groupId": "fixture-group", "target": map[string]any{"accountId": fmt.Sprintf("fixture-%d", i), "accountName": strings.Repeat("测", 96), "groupName": strings.Repeat("组", 96), "baseURL": "https://fixture.invalid", "protocol": "chat"}}
	}
	request := map[string]any{"batchId": batchID, "settingsRevision": settings["revision"], "selectedSectionIds": []string{}, "selectedQuestionIds": ids, "tasks": tasks}
	started := time.Now()
	created := query(t, "create-batch", map[string]any{"request": request, "targets": targets})
	raw, _ := json.Marshal(created)
	if created["batchId"] != batchID || len(created["reportIds"].([]any)) != 32 || len(raw) > 8<<10 {
		t.Fatalf("incomplete or oversized batch response: %d bytes", len(raw))
	}
	t.Logf("create 32 reports with %d selected items in %v, response %d bytes", len(ids), time.Since(started), len(raw))
	seen := map[string]bool{}
	offset := 0
	for {
		detail := query(t, "read-batch", map[string]any{"batchId": batchID, "offset": offset, "limit": 50, "view": "results"})
		members, ok := detail["memberStates"].([]any)
		if !ok || len(members) != 32 {
			t.Fatal("full member count is missing")
		}
		for _, v := range detail["items"].([]any) {
			row := v.(map[string]any)
			id := row["reportId"].(string)
			if seen[id] {
				t.Fatal("duplicate result row")
			}
			seen[id] = true
		}
		if detail["nextOffset"] == nil {
			break
		}
		next := int(detail["nextOffset"].(float64))
		if next <= offset {
			t.Fatal("no paging progress")
		}
		offset = next
	}
	if len(seen) != 32 {
		t.Fatalf("missing rows: %d", len(seen))
	}
}

// Exercise the actual Service, CLI and systemd while a single mock-model
// request remains active. Stop the first report and observe the next two
// genuinely dispatch, not just transition to a queued status.
func TestChanshuiActualBatchReadWhileRunning(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit actual batch resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := actualSlice(t)
	assertActualEmpty(t, conn)
	binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	if staged := os.Getenv("TRANSITHUB_CHANSHUI_BATCH_STAGED_BINARY"); staged != "" {
		if !within(filepath.Join(root, ".cache"), staged) {
			t.Fatal("staged resource candidate must be in project .cache")
		}
		binary = staged
	}
	started := make(chan string, 8)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		t.Logf("mock request model=%s path=%s stream=%v", body.Model, r.URL.Path, body.Stream)
		started <- body.Model
		if body.Model == "fixture-A" {
			<-r.Context().Done()
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"最终答案: 开车去"},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}

data: [DONE]

`)
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"最终答案: 开车去"},"finish_reason":"stop"}]}`)
		}
	}))
	t.Cleanup(up.Close)
	audit := connection_health.AuditContext{WorkspaceID: "workspace-a", TargetID: "fixture-account", AccountID: "fixture-account", AccountName: "批次隔离验收", GroupID: "fixture-group", GroupName: "Fixture", BaseURL: up.URL, Protocol: "chat", CanRun: true, Credential: upstream.ProbeCredential{BaseURL: up.URL, Key: "isolated-batch-fixture", AnthropicAuth: "x_api_key"}}
	runner := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: binary, DataDir: t.TempDir(), ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
	s := &Service{cfg: config.Config{ChanshuiEnabled: true}, workspace: fakeWorkspace{}, targets: actualServiceTargets{audit}, runner: runner, initialized: true}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		assertActualEmpty(t, conn)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner := Owner{"batch-service-fixture", "workspace-a"}
	raw, err := s.queryJSON(ctx, "read-settings", owner, map[string]any{}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["retries"] = 0
	settings["protocol"] = "chat"
	raw, err = s.queryJSON(ctx, "save-settings", owner, settings, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	req := BatchInput{BatchID: fmt.Sprintf("%032x", 1), SettingsRevision: int(settings["revision"].(float64)), SelectedQuestionIDs: []string{"Q2"}}
	for i, model := range []string{"fixture-A", "fixture-B", "fixture-C"} {
		req.Tasks = append(req.Tasks, BatchTaskInput{ReportID: fmt.Sprintf("%032x", i+10), TargetID: audit.TargetID, GroupID: audit.GroupID, Model: model, ExpectedContext: expected(audit)})
	}
	if _, err := s.StartBatch(ctx, owner, req); err != nil {
		t.Fatal(err)
	}
	next := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("got actual request %s expected %s", got, want)
			}
		case <-time.After(12 * time.Second):
			t.Fatalf("actual request %s did not start", want)
		}
	}
	next("fixture-A")
	var job *Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if s.active != nil {
			job = s.active.Job
		}
		s.mu.Unlock()
		if job != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("no real active job")
	}
	manager, err := connectManager(ctx, runner.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.release("")
	_, taskGroup := resourceProcessLocation(t, manager, job.ID)
	group := filepath.Dir(taskGroup)
	view, err := s.ReadBatch(ctx, owner, req.BatchID, map[string]any{"view": "results", "offset": 0, "limit": 32})
	if err != nil {
		t.Fatal(err)
	}
	value := view.(map[string]any)
	encoded, _ := json.Marshal(value)
	var summary struct {
		Counts batchCounts `json:"counts"`
	}
	if err = json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Counts.Active != 1 || summary.Counts.Waiting != 2 || summary.Counts.Ended != 0 || len(encoded) > 64<<10 || calls.Load() != 1 {
		t.Fatalf("unexpected live batch counts %+v calls=%d bytes=%d", summary.Counts, calls.Load(), len(encoded))
	}
	peak, err := resourceReadUint(filepath.Join(group, "memory.peak"))
	if err != nil {
		t.Fatal(err)
	}
	maxMem, err := resourceReadUint(filepath.Join(group, "memory.max"))
	if err != nil {
		t.Fatal(err)
	}
	swap, err := resourceReadUint(filepath.Join(group, "memory.swap.max"))
	if err != nil {
		t.Fatal(err)
	}
	pids, err := resourceReadUint(filepath.Join(group, "pids.max"))
	if err != nil {
		t.Fatal(err)
	}
	if peak > MemoryCeiling || (maxMem > MemoryCeiling || maxMem < MemoryCeiling-uint64(os.Getpagesize())) || swap != 0 || pids > 64 {
		t.Fatalf("resource boundary peak=%d max=%d swap=%d tasks=%d", peak, maxMem, swap, pids)
	}
	if _, err := s.CancelRound(ctx, owner, req.Tasks[0].ReportID, ""); err != nil {
		t.Fatal(err)
	}
	next("fixture-B")
	next("fixture-C")
	var finished map[string]any
	until := time.Now().Add(12 * time.Second)
	for time.Now().Before(until) {
		v, e := s.ReadBatch(ctx, owner, req.BatchID, map[string]any{"view": "progress"})
		if e == nil {
			finished = v.(map[string]any)
			if finished["state"] == "ended" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finished["state"] != "ended" || numberField(finished, "firstFinishedAtMs") <= 0 || calls.Load() != 3 || !job.Info().Exited {
		t.Fatalf("batch did not safely advance: state=%v calls=%d first confirmed=%d original exited=%v", finished["state"], calls.Load(), numberField(finished, "firstFinishedAtMs"), job.Info().Exited)
	}
	t.Logf("real held request + batch read: %d bytes, slice peak=%d, cap=%d, swap=%d, tasks=%d; stopped first, next two dispatched, 3 total mock requests", len(encoded), peak, maxMem, swap, pids)
}

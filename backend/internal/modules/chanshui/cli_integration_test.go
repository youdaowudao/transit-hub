package chanshui

import (
	"context"
	"encoding/json"
	"errors"
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
)

// Unlike the systemd fixture gate, this executes the shipped SQLite/engine CLI.
// It is explicitly opted in; it uses only an isolated loopback fake upstream.
func TestChanshuiActualCLIAllPresetsAndMemoryRelease(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("actual CLI acceptance requires explicit resource test opt-in")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	if _, err = os.Stat(binary); err != nil {
		t.Fatalf("build the actual CLI before resource acceptance: %v", err)
	}
	var manifest struct {
		IDs []string `json:"preset_ids"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "chanshui/.artifacts/bank-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil || len(manifest.IDs) < 30 {
		t.Fatalf("missing full build-time preset inventory: count=%d error=%v", len(manifest.IDs), err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-cli-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	conn := actualSlice(t)
	runner := NewRunner(RunnerConfig{BinaryPath: binary, DataDir: dir, ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := runner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	owner := Owner{UserID: "cli-acceptance", WorkspaceID: "cli-isolated-workspace"}
	var paid, models, leaks atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			models.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"other-first"},{"id":"gpt-6.1-sol"},{"id":"claude-opus-5-5"}]}`)
			return
		}
		paid.Add(1)
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 128*1024))
		body := string(raw)
		if strings.Contains(body, "标准答案是") || strings.Contains(body, "给自己的上一轮回答打分") {
			leaks.Add(1)
		}
		answer := "最终答案: 4"
		if strings.Contains(body, "洗车店") {
			answer = "最终答案: 开车去"
		}
		if strings.Contains(body, "large-response") {
			answer = strings.Repeat("a", 2*1024*1024)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		one, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20}}\n\ndata: [DONE]\n\n", one)
	}))
	defer up.Close()
	cred := Credential{BaseURL: up.URL, Key: "isolated-cli-key-must-not-be-persisted", Protocol: "chat"}
	query := func(op string, args any, credential *Credential) json.RawMessage {
		t.Helper()
		res, err := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, credential, false)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		data, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("%s read=%v close=%v", op, readErr, closeErr)
		}
		if strings.Contains(string(data), cred.Key) {
			t.Fatalf("%s leaked key into a returned result", op)
		}
		return data
	}
	query("init", map[string]any{}, nil)
	modelRaw := query("models", map[string]any{}, &cred)
	if paid.Load() != 0 || models.Load() != 1 || !strings.Contains(string(modelRaw), "gpt-6.1-sol") {
		t.Fatal("listing models must return this account's models without starting detection")
	}
	var settings map[string]any
	if err = json.Unmarshal(query("read-settings", map[string]any{}, nil), &settings); err != nil {
		t.Fatal(err)
	}
	settings["memoryLimitMiB"] = 128
	query("save-settings", settings, nil)
	all := map[string]map[string]any{}
	cursor := ""
	for {
		var page struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"nextCursor"`
		}
		if err = json.Unmarshal(query("list-question-summaries", map[string]any{"cursor": cursor, "limit": 20}, nil), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			all[item["id"].(string)] = item
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(all) != len(manifest.IDs) {
		t.Fatalf("preset settings inventory mismatch: listed=%d build=%d", len(all), len(manifest.IDs))
	}
	for _, id := range manifest.IDs {
		if all[id] == nil {
			t.Fatalf("preset missing from paginated settings: %s", id)
		}
		var detail map[string]any
		if err = json.Unmarshal(query("read-question", map[string]any{"id": id}, nil), &detail); err != nil {
			t.Fatal(err)
		}
		if detail["id"] != id || detail["title"] == "" || detail["parameters"] == nil {
			t.Fatalf("preset is not editable/readable: %s", id)
		}
	}
	t.Logf("all %d upstream preset definitions are individually readable in settings", len(all))
	run := func(reportID string, ids []string, sections []string, model string) (json.RawMessage, *Job) {
		t.Helper()
		query("create-report", map[string]any{"reportId": reportID, "targetId": "isolated-account", "groupId": "test-group", "model": model, "selectedQuestionIds": ids, "selectedSectionIds": sections, "target": map[string]any{"accountId": "acceptance-account", "accountName": "隔离检测验收", "groupName": "GPT验收组", "baseURL": up.URL, "protocol": "chat"}}, nil)
		job, err := runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: owner, Args: map[string]any{"reportId": reportID}}, cred, 128, 60)
		if err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 35*time.Second)
		assertActualEmpty(t, conn)
		return query("read-report", map[string]any{"reportId": reportID}, nil), job
	}
	firstID := "10000000000000000000000000000001"
	first, firstJob := run(firstID, []string{"Q2"}, []string{"iq"}, "gpt-6.1-sol")
	if !strings.Contains(string(first), `"status":"done"`) {
		t.Fatalf("actual Q2 report not completed: %s", first)
	}
	if paid.Load() != 1 || leaks.Load() != 0 {
		t.Fatalf("unexpected model requests/self-grade: paid=%d leaks=%d", paid.Load(), leaks.Load())
	}
	for _, id := range manifest.IDs {
		query("delete-question", map[string]any{"id": id, "revision": all[id]["revision"]}, nil)
	}
	query("init", map[string]any{}, nil)
	var empty struct {
		Items []any `json:"items"`
	}
	if err = json.Unmarshal(query("list-question-summaries", map[string]any{}, nil), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 {
		t.Fatal("deleted presets were re-seeded by a later process")
	}
	old := query("list-items", map[string]any{"reportId": firstID, "limit": 5}, nil)
	if !strings.Contains(string(old), "开车去") || !strings.Contains(string(old), "洗车店") {
		t.Fatal("deleting preset changed the saved report snapshot")
	}
	definition := map[string]any{"id": "custom:20000000000000000000000000000001", "title": "自定义加法验收", "kind": "iq", "enabled": true, "order": 100, "maxScore": 7, "template": "加法2+2等于几？", "parameters": map[string]any{"generator": "static"}, "judge": map[string]any{"type": "number", "expectedNumber": 4}}
	var saved struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(query("save-question", map[string]any{"definition": definition}, nil), &saved); err != nil || saved.ID == "" {
		t.Fatalf("add custom question: %v", err)
	}
	second, secondJob := run("10000000000000000000000000000002", []string{saved.ID}, []string{"iq"}, "gpt-6.1-sol")
	if !strings.Contains(string(second), `"status":"done"`) {
		t.Fatalf("new custom question did not run: %s", second)
	}
	newItems := query("list-items", map[string]any{"reportId": "10000000000000000000000000000002", "limit": 5}, nil)
	if !strings.Contains(string(newItems), "自定义加法验收") || !strings.Contains(string(newItems), `"earned":7`) {
		t.Fatalf("new question missing from saved answer list: %s", newItems)
	}
	large, largeJob := run("10000000000000000000000000000003", []string{saved.ID}, []string{"iq"}, "large-response")
	if !strings.Contains(string(large), `"status":"partial"`) {
		t.Fatalf("oversized upstream response must remain incomplete: %s", large)
	}
	if leaks.Load() != 0 || paid.Load() != 3 {
		t.Fatalf("unexpected paid retries or self-grading: requests=%d leaks=%d", paid.Load(), leaks.Load())
	}
	assertActualEmpty(t, conn)
	t.Logf("actual CLI peaks bytes: preset=%d custom=%d oversized=%d; each used run128MiB/query64MiB; all presets deleted without re-seed, old report intact, custom result visible, no model/self-grade retry", firstJob.Info().MemoryPeak, secondJob.Info().MemoryPeak, largeJob.Info().MemoryPeak)
}

func TestChanshuiActualCLIFullPresetResourceBudget(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit real-resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		IDs []string `json:"preset_ids"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "chanshui/.artifacts/bank-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-full-resource-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	conn := actualSlice(t)
	runner := NewRunner(RunnerConfig{BinaryPath: filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui"), DataDir: dir, ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := runner.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	var calls, leaks atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 128*1024))
		if strings.Contains(string(raw), "标准答案是") || strings.Contains(string(raw), "给自己的上一轮回答打分") {
			leaks.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"最终答案: 4\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner := Owner{UserID: "full-resource-user", WorkspaceID: "full-isolated-workspace"}
	credential := Credential{BaseURL: up.URL, Key: "isolated-full-resource-key", Protocol: "chat"}
	query := func(op string, args any) json.RawMessage {
		t.Helper()
		res, e := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, nil, false)
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		data, e := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if e != nil || closeErr != nil {
			t.Fatalf("%s read=%v close=%v", op, e, closeErr)
		}
		if strings.Contains(string(data), credential.Key) {
			t.Fatal("known credential escaped into report")
		}
		return data
	}
	query("init", map[string]any{})
	var cache map[string]any
	if err = json.Unmarshal(query("read-question", map[string]any{"id": "cache"}), &cache); err != nil {
		t.Fatal(err)
	}
	cache["enabled"] = true
	query("save-question", map[string]any{"definition": cache, "revision": cache["revision"]})
	reportID := "30000000000000000000000000000001"
	created := query("create-report", map[string]any{"reportId": reportID, "targetId": "full-resource-account", "model": "gpt-6.1-sol", "target": map[string]any{"accountId": "full-resource-account", "accountName": "全预设资源验收", "baseURL": up.URL, "protocol": "chat"}})
	if !strings.Contains(string(created), `"memoryLimitMiB":256`) {
		t.Fatalf("full test must use default task limit: %s", created)
	}
	job, err := runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: owner, Args: map[string]any{"reportId": reportID}}, credential, 256, 60)
	if err != nil {
		t.Fatal(err)
	}
	waitActualJob(t, job, 40*time.Second)
	assertActualEmpty(t, conn)
	var report struct {
		Status  string `json:"status"`
		Summary struct {
			Total struct {
				Score    *int `json:"score"`
				Complete bool `json:"complete"`
			} `json:"total"`
		} `json:"summary"`
	}
	if err = json.Unmarshal(query("read-report", map[string]any{"reportId": reportID}), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "partial" || report.Summary.Total.Score != nil || report.Summary.Total.Complete {
		t.Fatalf("unknown fake-upstream capabilities must not produce a completed numeric score: %+v", report)
	}
	seen := map[string]bool{}
	cursor := ""
	images := 0
	for {
		var page struct {
			Items []struct {
				ID    string `json:"itemId"`
				Image bool   `json:"imageAvailable"`
			} `json:"items"`
			Next string `json:"nextCursor"`
		}
		if err = json.Unmarshal(query("list-items", map[string]any{"reportId": reportID, "cursor": cursor, "limit": 5}), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate saved item %s", item.ID)
			}
			seen[item.ID] = true
			if item.Image {
				images++
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	for _, id := range manifest.IDs {
		if !seen[id] {
			t.Fatalf("preset not represented in full report: %s", id)
		}
	}
	if len(seen) != 35 || images != 1 {
		t.Fatalf("full saved inventory count=%d images=%d", len(seen), images)
	}
	image, err := runner.Query(ctx, Envelope{V: 1, Operation: "read-image", Owner: owner, Args: map[string]any{"reportId": reportID, "itemId": "vision"}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	bytes, readErr := io.ReadAll(image.Body)
	closeErr := image.Body.Close()
	if readErr != nil || closeErr != nil || !strings.HasPrefix(string(bytes), "\x89PNG\r\n\x1a\n") {
		t.Fatalf("saved chart unreadable: read=%v close=%v bytes=%d", readErr, closeErr, len(bytes))
	}
	if calls.Load() > 128 || calls.Load() < 20 || leaks.Load() != 0 {
		t.Fatalf("full run request budget/leak failed: requests=%d selfgrade=%d", calls.Load(), leaks.Load())
	}
	if !job.Info().Exited || job.Info().OOM {
		t.Fatalf("full default preset process did not exit normally: %+v", job.Info())
	}
	assertActualEmpty(t, conn)
	t.Logf("full 35 presets including cache+chart: requests=%d peak=%d bytes, task max=%d slice max=%d, image=%d bytes; unknown total withheld; all processes and idle slice reclaimed", calls.Load(), job.Info().MemoryPeak, job.Info().TaskMemoryMaxBytes, job.Info().SliceMemoryMaxBytes, len(bytes))
}

func TestChanshuiActualCLICurrentRevisionProjection(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("explicit real IPC acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-cas-resource-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	conn := actualSlice(t)
	runner := NewRunner(RunnerConfig{BinaryPath: filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui"), DataDir: dir, ProjectDir: root, Manager: "user"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner := Owner{UserID: "cas-resource", WorkspaceID: "cas-isolated"}
	t.Cleanup(func() {
		end, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := runner.Shutdown(end); err != nil {
			t.Error(err)
		}
	})
	call := func(op string, args any) (json.RawMessage, error) {
		res, e := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, nil, false)
		if e != nil {
			return nil, e
		}
		raw, e := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if e == nil {
			e = closeErr
		}
		return raw, e
	}
	must := func(op string, args any) json.RawMessage {
		t.Helper()
		raw, e := call(op, args)
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		return raw
	}
	must("init", map[string]any{})
	var settings map[string]any
	if err = json.Unmarshal(must("read-settings", map[string]any{}), &settings); err != nil {
		t.Fatal(err)
	}
	settings["memoryLimitMiB"] = 128
	must("save-settings", settings)
	_, err = call("save-settings", settings)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 409 || p.CurrentRevision == nil || *p.CurrentRevision != 2 {
		t.Fatalf("actual CLI conflict version lost: %#v %v", p, err)
	}
	var current map[string]any
	if err = json.Unmarshal(must("read-settings", map[string]any{}), &current); err != nil {
		t.Fatal(err)
	}
	if current["memoryLimitMiB"] != float64(128) || current["revision"] != float64(2) {
		t.Fatal("stale write changed actual stored settings")
	}
	var q map[string]any
	if err = json.Unmarshal(must("read-question", map[string]any{"id": "Q1"}), &q); err != nil {
		t.Fatal(err)
	}
	must("delete-question", map[string]any{"id": "Q1", "revision": q["revision"]})
	_, err = call("delete-question", map[string]any{"id": "Q1", "revision": q["revision"]})
	p = nil
	if !errors.As(err, &p) || p.Status != 409 || p.CurrentRevision == nil || *p.CurrentRevision != 0 {
		t.Fatalf("deleted revision zero lost in actual pipe: %#v %v", p, err)
	}
	assertActualEmpty(t, conn)
	t.Log("actual CLI -> bounded error header -> Hub projection keeps current revision 2 and deleted revision 0 without changing stored values")
}

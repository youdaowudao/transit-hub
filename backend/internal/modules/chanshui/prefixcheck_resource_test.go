package chanshui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChanshuiActualPrefixCheckMemory(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_CONCURRENCY_MEMORY_TEST") != "1" {
		t.Skip("explicit local prefix resource acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_BINARY")
	if binary == "" {
		binary = filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	}
	runner := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: binary, DataDir: dir, ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
	manager, err := connectManager(ctx, runner.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.release("")
	owner := Owner{UserID: "resource-prefix", WorkspaceID: "isolated-resource-only"}
	read := func(op string, args any, dest any) uint64 {
		t.Helper()
		res, err := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, nil, true)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil || res.Status != http.StatusOK {
			t.Fatalf("%s: status=%d read=%v close=%v body=%s", op, res.Status, readErr, closeErr, body)
		}
		info := res.Body.(*queryBody).p.info()
		if info.OOM {
			t.Fatalf("%s exceeded the 64MiB query limit: %+v", op, info)
		}
		if dest != nil {
			if err := json.Unmarshal(body, dest); err != nil {
				t.Fatalf("%s: %v", op, err)
			}
		}
		return info.MemoryPeak
	}
	read("init", map[string]any{}, nil)
	var bank struct {
		Items []struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		}
		Next string `json:"nextCursor"`
	}
	for cursor := ""; ; cursor = bank.Next {
		read("list-question-summaries", map[string]any{"limit": 50, "cursor": cursor}, &bank)
		for _, q := range bank.Items {
			if q.ID != "injection" {
				read("delete-question", map[string]any{"id": q.ID, "revision": q.Revision}, nil)
			}
		}
		if bank.Next == "" {
			break
		}
	}
	ids := []string{"injection"}
	for i := 1; i <= 30; i++ {
		id, title := fmt.Sprintf("custom:%032x", i), fmt.Sprintf("资源核对题%d", i)
		ids = append(ids, id)
		read("save-question", map[string]any{
			"revision": 0,
			"definition": map[string]any{
				"id": id, "title": title, "kind": "iq", "enabled": true, "order": i, "maxScore": 0, "template": title,
				"parameters": map[string]any{"generator": "static"}, "judge": map[string]any{"type": "manual"},
			},
		}, nil)
	}
	requested, release := make(chan struct{}), make(chan struct{})
	var firstRequest, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	const usage = `"usage":{"prompt_tokens":5430,"completion_tokens":5,"total_tokens":5435}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "malformed resource fixture request", http.StatusBadRequest)
			return
		}
		firstRequest.Do(func() { close(requested) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"最终答案: 1\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],%s}\n\ndata: [DONE]\n\n", usage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"最终答案: 1"}}],%s}`, usage)
	}))
	defer func() {
		unblock()
		up.Close()
	}()
	var settings map[string]any
	read("read-settings", map[string]any{}, &settings)
	settings["concurrency"], settings["totalTimeoutSeconds"], settings["timeoutSeconds"] = 16, 60, 10
	settings["protocol"], settings["retries"] = "chat", 0
	read("save-settings", settings, nil)
	const id = "0000000000000000000000000000cafe"
	reportArgs := map[string]any{"reportId": id}
	read("create-report", map[string]any{
		"reportId": id, "targetId": "isolated", "model": "gpt-6.2-sol", "selectedQuestionIds": ids,
		"target": map[string]any{"accountId": "isolated", "accountName": "判读资源隔离验收", "baseURL": up.URL, "protocol": "chat"},
	}, nil)
	job, err := runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: owner, Args: reportArgs}, Credential{
		BaseURL: up.URL, Protocol: "chat", Key: "isolated-resource-placeholder",
	}, 128, 60)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		unblock()
		if !job.Info().Exited {
			cleanup, done := context.WithTimeout(context.Background(), 8*time.Second)
			defer done()
			if err := job.Stop(cleanup); err != nil {
				t.Errorf("stop resource worker: %v", err)
			}
			select {
			case <-job.Done:
			case <-cleanup.Done():
				t.Error("resource worker did not exit")
			}
		}
	}()
	if limit := job.Info().TaskMemoryMaxBytes; limit != 128<<20 {
		t.Fatalf("actual task limit = %d bytes; want 128MiB", limit)
	}
	select {
	case <-requested:
	case <-job.Done:
		t.Fatalf("worker exited before requesting a question: %+v", job.Info())
	case <-time.After(8 * time.Second):
		t.Fatal("no resource-test question reached the isolated upstream")
	}
	pid, _ := resourceProcessLocation(t, manager, job.ID)
	rssPeak, _ := resourceProcessUsage(pid)
	unblock()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for running := true; running; {
		select {
		case <-job.Done:
			running = false
		case <-ticker.C:
			rss, _ := resourceProcessUsage(pid)
			rssPeak = max(rssPeak, rss)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	info := job.Info()
	if !info.Exited || info.OOM || info.MemoryPeak == 0 || info.MemoryPeak > info.TaskMemoryMaxBytes {
		t.Fatalf("invalid resource worker exit: %+v", info)
	}
	var report struct{ Status string }
	read("read-report", reportArgs, &report)
	if report.Status != "done" && report.Status != "partial" {
		t.Fatalf("resource report status = %q; want done or partial", report.Status)
	}
	type metrics struct{ UsagePresent bool }
	var page struct {
		Next  string `json:"nextCursor"`
		Items []struct {
			Result struct {
				Metrics  metrics
				Evidence struct{ Metrics metrics }
			}
		}
	}
	usagePresent := false
	for cursor := ""; ; cursor = page.Next {
		read("list-items", map[string]any{"reportId": id, "limit": 5, "cursor": cursor}, &page)
		for _, item := range page.Items {
			usagePresent = usagePresent || item.Result.Metrics.UsagePresent || item.Result.Evidence.Metrics.UsagePresent
		}
		if usagePresent || page.Next == "" {
			break
		}
	}
	if !usagePresent {
		t.Fatal("no question saved a positive UsagePresent flag")
	}

	var cross struct {
		PerItemChecked bool
		Checked        int
		Peers          []json.RawMessage
	}
	var queryPeakCross, queryPeakPrompt uint64
	if os.Getenv("TRANSITHUB_CHANSHUI_PREFIX_BASELINE") != "1" {
		pressure := exec.CommandContext(ctx, "go", "-C", filepath.Join(root, "chanshui"), "run", "./internal/hub/prefixcheck/testdata/pressure",
			"-db", filepath.Join(dir, "hub.sqlite"), "-report", id, "-user", owner.UserID, "-workspace", owner.WorkspaceID)
		if out, err := pressure.CombinedOutput(); err != nil {
			t.Fatalf("prepare isolated query pressure: %v\n%s", err, out)
		}
		queryPeakCross = read("cross-check", reportArgs, &cross)
		queryPeakPrompt = read("read-prefix-prompt", map[string]any{"promptId": "codex-gpt-6.2-sol"}, nil)
		if !cross.PerItemChecked || cross.Checked != 30 || len(cross.Peers) != 20 {
			t.Errorf("pressure result: perItemChecked=%t checked=%d peers=%d; want true, 30, 20", cross.PerItemChecked, cross.Checked, len(cross.Peers))
		}
		const queryPeakLimit = 48 << 20
		if queryPeakCross == 0 || queryPeakPrompt == 0 || queryPeakCross >= queryPeakLimit || queryPeakPrompt >= queryPeakLimit {
			t.Errorf("query peaks must be measured below 48MiB: cross-check=%d read-prefix-prompt=%d", queryPeakCross, queryPeakPrompt)
		}
		if info.MemoryPeak > 115<<20 {
			t.Errorf("resource worker peak = %d bytes; exceeds 115MiB", info.MemoryPeak)
		}
	}
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	commit, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PREFIX_RESOURCE commit=%s binarySha256=%x taskLimit=%d rssPeakBytes=%d taskMemoryPeakBytes=%d perItemChecked=%t checked=%d queryPeakCross=%d queryPeakPrompt=%d",
		strings.TrimSpace(string(commit)), hash.Sum(nil)[:8], info.TaskMemoryMaxBytes, rssPeak, info.MemoryPeak, cross.PerItemChecked, cross.Checked, queryPeakCross, queryPeakPrompt)
}

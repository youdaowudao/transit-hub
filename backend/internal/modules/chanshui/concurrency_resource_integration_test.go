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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Explicit local resource acceptance. CI discovers this test but never starts
// an upstream listener or a systemd unit without this separate opt-in.
func TestChanshuiActualConcurrencyMemory(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_CONCURRENCY_MEMORY_TEST") != "1" {
		t.Skip("explicit local concurrency resource acceptance only")
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
	runner := NewRunner(RunnerConfig{BinaryPath: binary, DataDir: dir, ProjectDir: root, Manager: "user", AllowPrivateTargets: true})
	manager, err := connectManager(ctx, runner.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.release("")
	owner := Owner{UserID: "resource-concurrency", WorkspaceID: "isolated-resource-only"}
	query := func(op string, args any) json.RawMessage {
		t.Helper()
		res, err := runner.Query(ctx, Envelope{V: 1, Operation: op, Owner: owner, Args: args}, nil, true)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("%s read=%v close=%v", op, readErr, closeErr)
		}
		return body
	}
	query("init", map[string]any{})
	var page struct {
		Items []struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		} `json:"items"`
		Next string `json:"nextCursor"`
	}
	// The isolated queue has only the exact controlled questions, not the presets.
	for {
		if err = json.Unmarshal(query("list-question-summaries", map[string]any{"limit": 50}), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			break
		}
		for _, q := range page.Items {
			query("delete-question", map[string]any{"id": q.ID, "revision": q.Revision})
		}
	}
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = fmt.Sprintf("custom:%032x", i+1)
		kind, generator := "iq", "static"
		params := map[string]any{"generator": generator}
		if i >= 32 {
			kind = "fingerprint_behavior"
			params = map[string]any{"generator": "fingerprint_behavior_v1", "slot": 0, "probeKind": "refusal-x2"}
		}
		query("save-question", map[string]any{"definition": map[string]any{"id": ids[i], "title": fmt.Sprintf("资源隔离题%d", i), "kind": kind, "enabled": true, "order": i, "maxScore": 0, "template": fmt.Sprintf("resource-request-%d", i), "parameters": params, "judge": map[string]any{"type": "manual"}}, "revision": 0})
	}
	cases := []struct {
		Limit, Bytes       int
		SSE, Retry, Cancel bool
	}{
		{32, 4096, false, false, false}, {32, 900 * 1024, true, false, false},
		{64, 4096, false, false, false}, {64, 900 * 1024, true, false, false},
		{64, 4096, true, true, false}, {64, 4096, true, false, true},
	}
	for caseIndex, scenario := range cases {
		t.Run(fmt.Sprintf("c%d_bytes%d_sse%v_retry%v_cancel%v", scenario.Limit, scenario.Bytes, scenario.SSE, scenario.Retry, scenario.Cancel), func(t *testing.T) {
			var active, peak, calls atomic.Int32
			var retryMu sync.Mutex
			retried := map[string]bool{}
			saturated, release := make(chan struct{}), make(chan struct{})
			var saturationOnce, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			answer := strings.Repeat("a", scenario.Bytes)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for previous := peak.Load(); n > previous; previous = peak.Load() {
					if peak.CompareAndSwap(previous, n) {
						break
					}
				}
				if n == int32(scenario.Limit) {
					saturationOnce.Do(func() { close(saturated) })
				}
				body, _ := io.ReadAll(io.LimitReader(r.Body, 128*1024))
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				if scenario.Retry {
					retryMu.Lock()
					first := !retried[string(body)]
					retried[string(body)] = true
					retryMu.Unlock()
					if first {
						w.WriteHeader(http.StatusServiceUnavailable)
						fmt.Fprint(w, `{"error":{"message":"isolated retry fixture"}}`)
						return
					}
				}
				if scenario.SSE {
					w.Header().Set("Content-Type", "text/event-stream")
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": nil}}})
					fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", chunk)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}, "finish_reason": "stop"}}})
				}
			}))
			defer func() { releaseOnce.Do(func() { close(release) }); up.Close() }()
			var settings map[string]any
			if err := json.Unmarshal(query("read-settings", map[string]any{}), &settings); err != nil {
				t.Fatal(err)
			}
			settings["concurrency"], settings["totalTimeoutSeconds"], settings["timeoutSeconds"], settings["maxAttempts"] = scenario.Limit, 60, 10, 256
			settings["retries"] = 0
			if scenario.Retry {
				settings["retries"] = 1
			}
			query("save-settings", settings)
			id := fmt.Sprintf("%032x", 1000+caseIndex)
			query("create-report", map[string]any{"reportId": id, "targetId": "isolated", "model": "fixture", "selectedQuestionIds": ids[:scenario.Limit], "target": map[string]any{"accountId": "isolated", "accountName": "并发内存隔离验收", "baseURL": up.URL, "protocol": "chat"}})
			job, err := runner.Start(ctx, Envelope{V: 1, Operation: "run", Owner: owner, Args: map[string]any{"reportId": id}}, Credential{BaseURL: up.URL, Protocol: "chat", Key: "isolated-resource-placeholder"}, 256, 60)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				if !job.Info().Exited {
					cleanup, done := context.WithTimeout(context.Background(), 8*time.Second)
					defer done()
					_ = job.Stop(cleanup)
					select {
					case <-job.Done:
					case <-cleanup.Done():
						t.Error("own resource job did not exit")
					}
				}
			})
			select {
			case <-saturated:
			case <-job.Done:
				t.Fatalf("worker exited before requested concurrency: %+v", job.Info())
			case <-time.After(8 * time.Second):
				t.Fatalf("did not reach %d requests; peak=%d", scenario.Limit, peak.Load())
			}
			var rssPeak, slicePeak, cpuTicks uint64
			pid, group := resourceProcessLocation(t, manager, job.ID)
			sample := func() {
				rss, cpu := resourceProcessUsage(pid)
				if rss > rssPeak {
					rssPeak = rss
				}
				if cpu > cpuTicks {
					cpuTicks = cpu
				}
				if group != "" {
					n, _ := resourceReadUint(filepath.Join(filepath.Dir(group), "memory.peak"))
					if n > slicePeak {
						slicePeak = n
					}
				}
			}
			sample()
			if scenario.Cancel {
				if err := job.Stop(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for running := true; running; {
				select {
				case <-job.Done:
					running = false
				case <-ticker.C:
					sample()
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			sample()
			info := job.Info()
			if !info.Exited || info.OOM || info.MemoryPeak > info.TaskMemoryMaxBytes || slicePeak > MemoryCeiling {
				t.Fatalf("resource boundary exceeded: exit=%+v slicePeak=%d", info, slicePeak)
			}
			if peak.Load() != int32(scenario.Limit) {
				t.Fatalf("actual concurrency=%d configured=%d", peak.Load(), scenario.Limit)
			}
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); !os.IsNotExist(err) {
				t.Errorf("resource worker pid %d remains: %v", pid, err)
			}
			t.Logf("RESOURCE concurrency=%d actualPeak=%d responseBytes=%d sse=%v retry=%v cancel=%v calls=%d rssPeakBytes=%d taskMemoryPeakBytes=%d sliceMemoryPeakBytes=%d taskLimitBytes=%d sliceLimitBytes=%d swapLimitBytes=%d cpuTicks=%d exited=%v oom=%v", scenario.Limit, peak.Load(), scenario.Bytes, scenario.SSE, scenario.Retry, scenario.Cancel, calls.Load(), rssPeak, info.MemoryPeak, slicePeak, info.TaskMemoryMaxBytes, info.SliceMemoryMaxBytes, info.SwapMaxBytes, cpuTicks, info.Exited, info.OOM)
			var report struct {
				Status  string `json:"status"`
				Summary struct {
					Attempts int `json:"attempts"`
				} `json:"summary"`
			}
			if err := json.Unmarshal(query("read-report", map[string]any{"reportId": id}), &report); err != nil {
				t.Fatal(err)
			}
			if scenario.Cancel {
				if report.Status != "cancelled" {
					t.Errorf("cancel result=%s", report.Status)
				}
			} else if report.Summary.Attempts != int(calls.Load()) {
				t.Errorf("attempts=%d actualCalls=%d", report.Summary.Attempts, calls.Load())
			}
		})
		if t.Failed() {
			break
		} // Recover this round before deciding a lower setting.
	}
}

func resourceReadUint(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
}
func resourceProcessUsage(pid int) (rss, cpu uint64) {
	raw, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			rss, _ = strconv.ParseUint(fields[1], 10, 64)
			rss *= 1024
		}
	}
	raw, _ = os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	parts := strings.SplitN(string(raw), ") ", 2)
	if len(parts) == 2 {
		fields := strings.Fields(parts[1])
		if len(fields) > 12 {
			user, _ := strconv.ParseUint(fields[11], 10, 64)
			system, _ := strconv.ParseUint(fields[12], 10, 64)
			cpu = user + system
		}
	}
	return
}
func resourceProcessLocation(t *testing.T, manager *systemManager, name string) (int, string) {
	t.Helper()
	path, err := manager.path(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	props, err := manager.props(t.Context(), path, "Service")
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := props["MainPID"].Value().(uint32)
	if pid == 0 {
		t.Fatal("resource unit has no worker pid")
	}
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(pid)), "cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			return int(pid), filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line[3:], "/"))
		}
	}
	t.Fatal("resource unit is not in cgroup v2")
	return 0, ""
}

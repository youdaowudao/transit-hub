package chanshui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Deterministic cancellation of an actual systemd worker before it emits ready.
// The fixture can emit ready only after cancellation has already happened.
func TestChanshuiActualPreparationCancelBeforeReady(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_PREPARATION_CANCEL_TEST") != "1" {
		t.Skip("explicit local handshake cancellation acceptance only")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "handshake-cancel-probe")
	command := exec.Command("/usr/local/go/bin/go", "build", "-p=1", "-o", helper, "./internal/modules/chanshui/testdata/memory_probe.go")
	command.Dir = filepath.Join(root, "backend")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build isolated fixture: %v: %s", err, out)
	}
	runner := NewRunner(RunnerConfig{BinaryPath: helper, DataDir: dir, ProjectDir: root, Manager: "user"})
	ctx, done := context.WithTimeout(t.Context(), 20*time.Second)
	defer done()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	waiting, allow, continued := filepath.Join(dir, "waiting"), filepath.Join(dir, "allow-ready"), filepath.Join(dir, "continued")
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	type outcome struct {
		job *Job
		err error
	}
	result := make(chan outcome, 1)
	go func() {
		job, err := runner.Start(startCtx, Envelope{V: 1, Operation: "delayed-ready-run", Owner: Owner{UserID: "cancel-fixture", WorkspaceID: "isolated"}, Args: map[string]any{"waitingFile": waiting, "allowReadyFile": allow, "continuedFile": continued, "modelURL": upstream.URL}}, Credential{BaseURL: upstream.URL, Key: "isolated-never-delivered", Protocol: "chat"}, 128, 60)
		result <- outcome{job, err}
	}()
	var pid int
	until := time.Now().Add(7 * time.Second)
	for time.Now().Before(until) {
		if raw, err := os.ReadFile(waiting); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		cancelStart()
		t.Fatal("fixture never reached the controlled pre-ready handshake boundary")
	}
	cancelStart()
	if err := os.WriteFile(allow, []byte("ready may now be sent"), 0600); err != nil {
		t.Fatal(err)
	}
	var value outcome
	select {
	case value = <-result:
	case <-ctx.Done():
		t.Fatal("cancelled startup was not returned and reclaimed")
	}
	// A cancelled startup must return only after the process group is reclaimed.
	_, stillPresent := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	returnedBeforeExit := stillPresent == nil
	if value.job != nil {
		cleanup, cleanupDone := context.WithTimeout(context.Background(), 8*time.Second)
		_ = value.job.Stop(cleanup)
		select {
		case <-value.job.Done:
		case <-cleanup.Done():
			t.Error("own test worker did not exit")
		}
		cleanupDone()
	}
	if value.err == nil || asProblem(value.err).OperationResult != "not_started" {
		t.Errorf("pre-ready cancel should be confirmed unstarted: job=%v err=%v", value.job != nil, value.err)
	}
	if _, err := os.Stat(continued); !os.IsNotExist(err) {
		t.Errorf("credentials were delivered after preparation cancellation: marker=%v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("cancelled preparation issued %d model requests", calls.Load())
	}
	if returnedBeforeExit {
		t.Error("cancelled preparation returned before the worker exited; queue capacity must not be released yet")
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); !os.IsNotExist(err) {
		t.Errorf("resource worker pid %d remains", pid)
	}
	t.Logf("PREPARATION_CANCEL modelCalls=%d noContinuation=%v returnedBeforeExit=%v", calls.Load(), func() bool { _, e := os.Stat(continued); return os.IsNotExist(e) }(), returnedBeforeExit)
}

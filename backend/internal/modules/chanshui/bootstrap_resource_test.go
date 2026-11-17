package chanshui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// Unlike actualSlice, this deliberately does not install or pre-create a slice.
func TestChanshuiActualBootstrapWithoutManualSlice(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("requires explicit resource test opt-in")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-bootstrap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	helper := filepath.Join(dir, "memory-probe")
	build := exec.Command("/usr/local/go/bin/go", "build", "-p=1", "-o", helper, "./internal/modules/chanshui/testdata/memory_probe.go")
	build.Dir = filepath.Join(root, "backend")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build explicit resource probe: %v: %s", err, output)
	}
	conn, err := dbus.Dial("unix:path=/run/user/" + strconv.Itoa(os.Getuid()) + "/bus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		t.Fatal(err)
	}
	var existing dbus.ObjectPath
	err = conn.Object("org.freedesktop.systemd1", managerPath).Call(managerInterface+".GetUnit", 0, sliceName).Store(&existing)
	if err == nil {
		t.Fatal("本项必须在没有预装/预建检测slice的隔离现场执行，当前存在已加载单元，未运行探针")
	}
	if e, ok := err.(dbus.Error); !ok || e.Name != "org.freedesktop.systemd1.NoSuchUnit" {
		t.Fatalf("cannot confirm absent slice: %v", err)
	}
	runner := NewRunner(RunnerConfig{DisableBankSync: true, BinaryPath: helper, DataDir: dir, ProjectDir: root, Manager: "user"})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if err := runner.Available(ctx); err != nil {
		t.Fatalf("没有人工安装slice时，程序必须自动建立并核验硬限制：%v", err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 12*time.Second)
		defer done()
		if err := runner.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
		assertActualEmpty(t, conn)
	})
	assertActualEmpty(t, conn)
	for i := 0; i < 20; i++ {
		response, err := runner.Query(ctx, Envelope{V: 1, Operation: "normal-query", Owner: Owner{UserID: "bootstrap-resource", WorkspaceID: "isolated-bootstrap"}}, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			PID   int    `json:"pid"`
			Max   uint64 `json:"memoryMax"`
			Slice uint64 `json:"sliceMax"`
			Swap  uint64 `json:"swapMax"`
		}
		err = json.NewDecoder(response.Body).Decode(&body)
		_, drainErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if err != nil || drainErr != nil || closeErr != nil {
			t.Fatalf("query/cleanup: %v / %v / %v", err, drainErr, closeErr)
		}
		if body.Max != 64*1024*1024 || body.Slice > MemoryCeiling || MemoryCeiling-body.Slice >= uint64(os.Getpagesize()) || body.Swap != 0 {
			t.Fatalf("query %d did not enforce actual limits: %+v", i, body)
		}
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(body.PID))); !os.IsNotExist(err) {
			t.Fatalf("query %d process remains", i)
		}
		assertActualEmpty(t, conn)
	}
	if err := runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("absent slice bootstrapped without manual configuration; 20 queries enforced limits and exited")
}

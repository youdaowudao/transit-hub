package chanshui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// This gate explicitly starts real units. Normal make test / CI never starts it.
func TestChanshuiActualResourceLifecycle(t *testing.T) {
	if os.Getenv("TRANSITHUB_CHANSHUI_RESOURCE_TEST") != "1" {
		t.Skip("real systemd resource test requires explicit TRANSITHUB_CHANSHUI_RESOURCE_TEST=1")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".cache"), "chanshui-resource-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	helper := filepath.Join(dir, "memory-probe")
	cmd := exec.Command("/usr/local/go/bin/go", "build", "-p=1", "-o", helper, "./internal/modules/chanshui/testdata/memory_probe.go")
	cmd.Dir = filepath.Join(root, "backend")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build explicit resource fixture: %v: %s", err, output)
	}
	conn := actualSlice(t)
	runner := NewRunner(RunnerConfig{BinaryPath: helper, DataDir: dir, ProjectDir: root, Manager: "user"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 12*time.Second)
		defer done()
		if err := runner.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	})
	if err := runner.Available(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Owner{UserID: "resource-fixture", WorkspaceID: "isolated-resource-fixture"}
	request := func(op string, args map[string]any) Envelope {
		return Envelope{V: 1, Operation: op, Owner: owner, Args: args}
	}
	credential := Credential{BaseURL: "https://unused.invalid", Key: "isolated-resource-test-placeholder", Protocol: "chat"}
	t.Run("twenty queries release every process", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			res, err := runner.Query(ctx, request("normal-query", nil), nil, false)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				PID   int    `json:"pid"`
				Max   uint64 `json:"memoryMax"`
				Slice uint64 `json:"sliceMax"`
				Swap  uint64 `json:"swapMax"`
			}
			if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
				res.Body.Close()
				t.Fatal(err)
			}
			if err = res.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if body.Max != 64*1024*1024 || (body.Slice > MemoryCeiling || MemoryCeiling-body.Slice >= uint64(os.Getpagesize())) || body.Swap != 0 {
				t.Fatalf("actual cgroup limits: %+v", body)
			}
			if _, err = os.Stat(filepath.Join("/proc", strconv.Itoa(body.PID))); !os.IsNotExist(err) {
				t.Fatalf("query %d still has process %d: %v", i, body.PID, err)
			}
		}
		assertActualEmpty(t, conn)
	})
	t.Run("run exits without page close", func(t *testing.T) {
		job, err := runner.Start(ctx, request("normal-run", nil), credential, 128, 60)
		if err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 10*time.Second)
		if info := job.Info(); !info.Exited || info.OOM {
			t.Fatalf("normal task did not exit cleanly: %+v", info)
		}
		assertActualEmpty(t, conn)
	})
	t.Run("persisted task that lingers is stopped", func(t *testing.T) {
		job, err := runner.Start(ctx, request("linger", nil), credential, 128, 60)
		if err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 12*time.Second)
		assertActualEmpty(t, conn)
	})
	t.Run("query exit keeps another run alive", func(t *testing.T) {
		job, err := runner.Start(ctx, request("hold-run", nil), credential, 384, 60)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if !job.Info().Exited {
				_ = job.Stop(ctx)
				waitActualJob(t, job, 10*time.Second)
			}
		}()
		response, err := runner.Query(ctx, request("normal-query", nil), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, response.Body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if job.Info().Exited {
			t.Fatal("closing a query stopped the still active run")
		}
		var unit dbus.ObjectPath
		if err = conn.Object("org.freedesktop.systemd1", managerPath).Call(managerInterface+".GetUnit", 0, job.ID).Store(&unit); err != nil {
			t.Fatal(err)
		}
		var props map[string]dbus.Variant
		if err = conn.Object("org.freedesktop.systemd1", unit).Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.systemd1.Service").Store(&props); err != nil {
			t.Fatal(err)
		}
		if number(props["MainPID"]) == 0 || number(props["MemoryMax"]) != 384*1024*1024 {
			t.Fatal("actual concurrent run exited or lost its hard limit")
		}
		if err = job.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 10*time.Second)
		assertActualEmpty(t, conn)
	})
	t.Run("stop clears child and descendant", func(t *testing.T) {
		pidFile := filepath.Join(dir, "stop-descendant.pid")
		job, err := runner.Start(ctx, request("hold-run", map[string]any{"pidFile": pidFile}), credential, 128, 60)
		if err != nil {
			t.Fatal(err)
		}
		if err = job.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 10*time.Second)
		assertDescendantGone(t, pidFile)
		assertActualEmpty(t, conn)
	})
	t.Run("OOM is confirmed and descendants are gone", func(t *testing.T) {
		pidFile := filepath.Join(dir, "oom-descendant.pid")
		job, err := runner.Start(ctx, request("oom", map[string]any{"pidFile": pidFile}), credential, 128, 60)
		if err != nil {
			t.Fatal(err)
		}
		waitActualJob(t, job, 20*time.Second)
		info := job.Info()
		if !info.Exited || !info.OOM || info.Result != "oom-kill" {
			t.Fatalf("must identify actual OOM rather than guess from signal: %+v", info)
		}
		t.Logf("actual run hard limit=134217728, peak=%d, result=%s", info.MemoryPeak, info.Result)
		assertDescendantGone(t, pidFile)
		assertActualEmpty(t, conn)
	})
	t.Run("bad handshake never receives continue", func(t *testing.T) {
		marker := filepath.Join(dir, "unexpected-continue")
		_, err := runner.Query(ctx, request("bad-limits", map[string]any{"continuedFile": marker}), nil, false)
		if err == nil {
			t.Fatal("invalid hard limit accepted")
		}
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("sent continue before validating limits")
		}
		assertActualEmpty(t, conn)
	})
	t.Run("cancelled query is reaped", func(t *testing.T) {
		short, done := context.WithTimeout(ctx, 350*time.Millisecond)
		defer done()
		res, err := runner.Query(short, request("hold-query", nil), nil, false)
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			t.Fatal("cancelled hanging query unexpectedly succeeded")
		}
		assertActualEmpty(t, conn)
	})
	if !t.Failed() {
		t.Log("20 sequential real queries, normal completion, persisted linger, explicit stop, OOM, invalid limits and cancellation left no model detection processes")
	}
}

func waitActualJob(t *testing.T, job *Job, limit time.Duration) {
	t.Helper()
	select {
	case <-job.Done:
	case <-time.After(limit):
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = job.Stop(ctx)
		t.Fatal("actual detection process did not finish before deadline")
	}
	if !job.Info().Exited {
		t.Fatalf("Done without empty cgroup: %+v", job.Info())
	}
}
func assertDescendantGone(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid := strings.TrimSpace(string(raw))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/proc", pid)); os.IsNotExist(err) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("descendant %s survived task shutdown", pid)
}

func actualSlice(t *testing.T) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Dial("unix:path=/run/user/" + strconv.Itoa(os.Getuid()) + "/bus")
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	obj := conn.Object("org.freedesktop.systemd1", managerPath)
	var path dbus.ObjectPath
	err = obj.Call(managerInterface+".GetUnit", 0, sliceName).Store(&path)
	created := err != nil
	if !created {
		var props map[string]dbus.Variant
		if err = conn.Object("org.freedesktop.systemd1", path).Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.systemd1.Unit").Store(&props); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		state, _ := props["ActiveState"].Value().(string)
		fragment, _ := props["FragmentPath"].Value().(string)
		transient, _ := props["Transient"].Value().(bool)
		// An inactive automatically generated slice is not an installed budget.
		// Recreate this test's private transient definition after a prior cleanup.
		created = state == "inactive" && fragment == "" && !transient
	}
	if created {
		props := []unitProperty{{"Description", dbus.MakeVariant("TransitHub explicit local resource acceptance")}, {"StopWhenUnneeded", dbus.MakeVariant(true)}, {"MemoryAccounting", dbus.MakeVariant(true)}, {"MemoryMax", dbus.MakeVariant(MemoryCeiling)}, {"MemoryHigh", dbus.MakeVariant(uint64(450000000))}, {"MemorySwapMax", dbus.MakeVariant(uint64(0))}, {"TasksMax", dbus.MakeVariant(uint64(64))}}
		if err = obj.Call(managerInterface+".StartTransientUnit", 0, sliceName, "fail", props, []unitAux{}).Err; err != nil {
			conn.Close()
			t.Fatal(err)
		}
	}
	if err = obj.Call(managerInterface+".RefUnit", 0, sliceName).Err; err != nil {
		conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer conn.Close()
		assertActualEmpty(t, conn)
		if created {
			if err := obj.Call(managerInterface+".StopUnit", 0, sliceName, "replace").Err; err != nil {
				t.Error(err)
			}
		}
		if err := obj.Call(managerInterface+".UnrefUnit", 0, sliceName).Err; err != nil {
			t.Error(err)
		}
		if created {
			// Do not let the following test pin the old unit while its stop job
			// is removing the transient configuration and replacing it with an
			// automatically generated, unlimited slice.
			end := time.Now().Add(3 * time.Second)
			for {
				var unused dbus.ObjectPath
				if obj.Call(managerInterface+".GetUnit", 0, sliceName).Store(&unused) != nil {
					break
				}
				if time.Now().After(end) {
					t.Error("isolated transient slice was not unloaded before the next test")
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	})
	return conn
}
func assertActualEmpty(t *testing.T, conn *dbus.Conn) {
	t.Helper()
	end := time.Now().Add(7 * time.Second)
	var remaining []listedUnit
	var charged string
	for time.Now().Before(end) {
		var units []listedUnit
		err := conn.Object("org.freedesktop.systemd1", managerPath).Call(managerInterface+".ListUnitsByPatterns", 0, []string{}, []string{"transithub-chanshui-run-*.service", "transithub-chanshui-query-*.service"}).Store(&units)
		if err != nil {
			t.Fatal(err)
		}
		remaining = units
		if len(units) == 0 {
			var path dbus.ObjectPath
			if err = conn.Object("org.freedesktop.systemd1", managerPath).Call(managerInterface+".GetUnit", 0, sliceName).Store(&path); err != nil {
				return
			}
			var props map[string]dbus.Variant
			if err = conn.Object("org.freedesktop.systemd1", path).Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.systemd1.Slice").Store(&props); err != nil {
				t.Fatal(err)
			}
			cg, _ := props["ControlGroup"].Value().(string)
			if cg == "" {
				return
			}
			raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", cg, "memory.current"))
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			n, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			charged = strings.TrimSpace(string(raw))
			if n == 0 {
				t.Log("empty detection slice memory.current=0 bytes")
				return
			}
		}
		time.Sleep(75 * time.Millisecond)
	}
	t.Fatalf("detection service units or charged memory remain after task cleanup: units=%+v memory.current=%s", remaining, charged)
}

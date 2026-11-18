package chanshui

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

const peakUnit = "transithub-chanshui-run-test.service"

func writeCgroupFile(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

// useCgroupFixture points inspect at a temporary cgroup tree and returns the
// unit directory "<root>/unit" (ControlGroup "/unit").
func useCgroupFixture(t *testing.T, populated bool, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
	dir := filepath.Join(root, "unit")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	events := "populated 0\n"
	if populated {
		events = "populated 1\n"
	}
	writeCgroupFile(t, dir, "cgroup.events", events)
	for name, value := range files {
		writeCgroupFile(t, dir, name, value)
	}
	return dir
}

// serviceProps models the Service properties; peak == nil models systemd
// below 255, which has no MemoryPeak property at all.
func serviceProps(group string, peak *uint64) map[string]dbus.Variant {
	props := map[string]dbus.Variant{
		"Slice":        dbus.MakeVariant(sliceName),
		"ExecStart":    dbus.MakeVariant([]unitExec{{"/project/probe", []string{"/project/probe", "run"}, false}}),
		"Result":       dbus.MakeVariant("success"),
		"MainPID":      dbus.MakeVariant(uint32(0)),
		"ControlGroup": dbus.MakeVariant(group),
	}
	if peak != nil {
		props["MemoryPeak"] = dbus.MakeVariant(*peak)
	}
	return props
}

func inspectWith(t *testing.T, group string, peak *uint64) ExitInfo {
	t.Helper()
	m, f := managerFixture(t)
	f.exists = true
	f.unit = serviceProps(group, peak)
	info, err := m.inspect(t.Context(), peakUnit)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func peakOf(n uint64) *uint64 { return &n }

// systemd 255+ answers UINT64_MAX when it cannot read the peak; that is
// "no value", not 16 EiB of memory.
func TestChanshuiInspectDropsUnreadableSystemdPeak(t *testing.T) {
	if got := inspectWith(t, "", peakOf(math.MaxUint64)).MemoryPeak; got != 0 {
		t.Fatalf("unreadable systemd peak became %d", got)
	}
	if got := inspectWith(t, "", peakOf(123456789)).MemoryPeak; got != 123456789 {
		t.Fatalf("readable systemd peak became %d", got)
	}
	if got := inspectWith(t, "", nil).MemoryPeak; got != 0 {
		t.Fatalf("no peak source became %d", got)
	}
}

// systemd below 255 has no MemoryPeak, so inspect must take the peak from the
// unit's own cgroup, and use whichever source is higher when both exist.
func TestChanshuiInspectTakesPeakFromUnitCgroup(t *testing.T) {
	useCgroupFixture(t, false, map[string]string{"memory.peak": "50000000\n", "memory.current": "1000\n"})
	info := inspectWith(t, "/unit", nil)
	if info.MemoryPeak != 50000000 || !info.Exited {
		t.Fatalf("systemd below 255 lost the cgroup peak or exit state: %+v", info)
	}
	if got := inspectWith(t, "/unit", peakOf(80000000)).MemoryPeak; got != 80000000 {
		t.Fatalf("higher systemd peak was not kept: %d", got)
	}
	if got := inspectWith(t, "/unit", peakOf(30000000)).MemoryPeak; got != 50000000 {
		t.Fatalf("higher cgroup peak was not kept: %d", got)
	}
	if got := inspectWith(t, "/unit", peakOf(math.MaxUint64)).MemoryPeak; got != 50000000 {
		t.Fatalf("unreadable systemd peak hid the cgroup peak: %d", got)
	}
}

// Missing or unreadable memory files must neither fail inspect nor change the
// running/exited decision taken from cgroup.events.
func TestChanshuiInspectPeakReadFailureKeepsExitDecision(t *testing.T) {
	useCgroupFixture(t, true, nil)
	info := inspectWith(t, "/unit", nil)
	if info.Exited || info.MemoryPeak != 0 {
		t.Fatalf("missing memory files changed inspect: %+v", info)
	}
	dir := useCgroupFixture(t, false, map[string]string{"memory.current": "42000000\n"})
	if err := os.Mkdir(filepath.Join(dir, "memory.peak"), 0700); err != nil {
		t.Fatal(err) // reading a directory fails like an unreadable file
	}
	info = inspectWith(t, "/unit", nil)
	if !info.Exited || info.MemoryPeak != 42000000 {
		t.Fatalf("unreadable memory.peak did not fall back to memory.current: %+v", info)
	}
}

// memory.peak is the exact kernel peak (Linux 5.19+); older kernels fall back
// to the current usage sampled on every check.
func TestChanshuiCgroupPeakPrefersKernelPeakAndFallsBackToCurrent(t *testing.T) {
	both := t.TempDir()
	writeCgroupFile(t, both, "memory.peak", "50000000\n")
	writeCgroupFile(t, both, "memory.current", "30000000\n")
	if got := cgroupMemoryPeak(both); got != 50000000 {
		t.Fatalf("kernel peak not used: %d", got)
	}
	oldKernel := t.TempDir()
	writeCgroupFile(t, oldKernel, "memory.current", "30000000\n")
	if got := cgroupMemoryPeak(oldKernel); got != 30000000 {
		t.Fatalf("current usage fallback not used: %d", got)
	}
	if got := cgroupMemoryPeak(t.TempDir()); got != 0 {
		t.Fatalf("missing cgroup files became %d", got)
	}
	bad := t.TempDir()
	writeCgroupFile(t, bad, "memory.peak", "max\n")
	writeCgroupFile(t, bad, "memory.current", "30000000\n")
	if got := cgroupMemoryPeak(bad); got != 0 {
		t.Fatalf("unparsable peak became %d", got)
	}
}

// The real kernel files of this test process's own cgroup must parse to a
// non-zero usage (no fixture formatting assumptions).
func TestChanshuiCgroupPeakReadsRealKernelFiles(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	own := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			own = filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(line, "0::"))
		}
	}
	if _, err := os.Stat(filepath.Join(own, "memory.current")); own == "" || err != nil {
		t.Skip("cgroup v2 memory files are not visible here")
	}
	if got := cgroupMemoryPeak(own); got == 0 {
		t.Fatalf("real cgroup %s gave no memory value", own)
	}
}

// Each check may see a lower sample, and the cgroup disappears on exit; the
// highest value seen during the run must survive both.
func TestChanshuiProcessKeepsHighestPeakAfterExit(t *testing.T) {
	p := &process{}
	p.setExit(ExitInfo{MemoryPeak: 50000000}, nil)
	p.setExit(ExitInfo{MemoryPeak: 30000000}, nil)
	p.setExit(ExitInfo{Exited: true}, nil)
	if info := p.info(); !info.Exited || info.MemoryPeak != 50000000 {
		t.Fatalf("peak was not kept through exit: %+v", info)
	}
}

// A monitor sample may arrive after stopAndWait already recorded the exit; its
// higher peak counts, but its running state must not undo the exit.
func TestChanshuiLateSampleRaisesPeakWithoutUndoingExit(t *testing.T) {
	p := &process{}
	exitErr := errors.New("exit diagnostic")
	p.setExit(ExitInfo{MemoryPeak: 30000000}, nil)
	p.setExit(ExitInfo{Exited: true, Result: "success"}, exitErr)
	p.setExit(ExitInfo{MemoryPeak: 80000000, Result: "running"}, nil)
	info := p.info()
	if !info.Exited || info.Result != "success" || info.MemoryPeak != 80000000 || p.exitErr != exitErr {
		t.Fatalf("late sample: %+v err=%v", info, p.exitErr)
	}
}

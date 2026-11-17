package chanshui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"transithub/backend/internal/config"

	"github.com/godbus/dbus/v5"
)

type managerCall struct {
	method string
	args   []any
}
type fakeManagerConnection struct {
	t                                                      *testing.T
	exists                                                 bool
	calls                                                  []managerCall
	props, unit                                            map[string]dbus.Variant
	units                                                  []listedUnit
	startErr, refErr, propsErr, stopErr, listErr, closeErr error
	closed                                                 int
	expireUnpinned                                         bool
}
type fakeManagerObject struct {
	dbus.BusObject
	conn *fakeManagerConnection
}

func (f *fakeManagerConnection) Object(string, dbus.ObjectPath) dbus.BusObject {
	return fakeManagerObject{conn: f}
}
func (f *fakeManagerConnection) Close() error { f.closed++; return f.closeErr }
func (o fakeManagerObject) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	f := o.conn
	f.calls = append(f.calls, managerCall{method, args})
	result := &dbus.Call{}
	switch strings.TrimPrefix(method, managerInterface+".") {
	case "GetUnit":
		if !f.exists {
			result.Err = dbus.Error{Name: "org.freedesktop.systemd1.NoSuchUnit"}
		} else {
			result.Body = []any{dbus.ObjectPath("/unit/slice")}
		}
	case "LoadUnit":
		f.exists = true
		result.Body = []any{dbus.ObjectPath("/unit/slice")}
	case "StartTransientUnit":
		if f.startErr != nil {
			result.Err = f.startErr
		} else {
			f.exists = true
			pinned := false
			for _, property := range args[2].([]unitProperty) {
				if property.Name == "AddRef" {
					pinned, _ = property.Value.Value().(bool)
				}
			}
			if f.expireUnpinned && !pinned {
				// Reproduce automatic collection before a separate RefUnit call.
				f.unit["StopWhenUnneeded"] = dbus.MakeVariant(false)
				f.props["MemoryMax"] = dbus.MakeVariant(^uint64(0))
			}
		}
	case "RefUnit":
		result.Err = f.refErr
	case "UnrefUnit", "ResetFailedUnit":
	case "ListUnitsByPatterns":
		result.Err = f.listErr
		result.Body = []any{f.units}
	case "StopUnit":
		result.Err = f.stopErr
		if f.stopErr == nil {
			f.unit["ActiveState"] = dbus.MakeVariant("inactive")
		}
	case "org.freedesktop.DBus.Properties.GetAll":
		if strings.HasSuffix(args[0].(string), ".Slice") {
			result.Body = []any{f.props}
			result.Err = f.propsErr
			f.propsErr = nil // A transient read failure must permit cleanup to recheck.
		} else {
			result.Body = []any{f.unit}
		}
	default:
		f.t.Errorf("unexpected manager operation %s", method)
		result.Err = errors.New("unexpected manager operation")
	}
	return result
}
func managerFixture(t *testing.T) (*systemManager, *fakeManagerConnection) {
	f := &fakeManagerConnection{t: t, props: map[string]dbus.Variant{"MemoryMax": dbus.MakeVariant(MemoryCeiling), "MemorySwapMax": dbus.MakeVariant(uint64(0)), "TasksMax": dbus.MakeVariant(uint64(64)), "ControlGroup": dbus.MakeVariant("")}, unit: map[string]dbus.Variant{"StopWhenUnneeded": dbus.MakeVariant(true), "ActiveState": dbus.MakeVariant("active")}}
	return &systemManager{conn: f, binary: "/project/probe"}, f
}
func callsNamed(f *fakeManagerConnection, name string) []managerCall {
	var found []managerCall
	for _, c := range f.calls {
		if c.method == managerInterface+"."+name {
			found = append(found, c)
		}
	}
	return found
}
func requireNoRuntimeCreation(t *testing.T, f *fakeManagerConnection) {
	t.Helper()
	for _, name := range []string{"StartTransientUnit", "LoadUnit", "SetUnitProperties"} {
		if len(callsNamed(f, name)) != 0 {
			t.Fatalf("existing configuration was changed: %s", name)
		}
	}
}

func TestChanshuiManagerUsesHubIdentityAndExplicitChoice(t *testing.T) {
	expected := "user"
	if os.Geteuid() == 0 {
		expected = "system"
	}
	for _, choice := range []string{"", "user", "system", "invalid-explicit"} {
		s := NewService(config.Config{ChanshuiEnabled: true, ChanshuiSystemdManager: choice}, nil, nil)
		want := choice
		if want == "" {
			want = expected
		}
		if got := s.runner.(*Runner).cfg.Manager; got != want {
			t.Fatalf("manager choice %q became %q; want %q", choice, got, want)
		}
	}
	s := NewService(config.Config{APIOnly: true, ChanshuiEnabled: true}, nil, nil)
	if err := s.ensureInitialized(t.Context()); err == nil || !strings.Contains(err.Error(), "接口") {
		t.Fatalf("APIOnly did not explain refusal: %v", err)
	}
	if s.cfg.ChanshuiEnabled {
		t.Fatal("APIOnly enabled model execution")
	}
}
func TestChanshuiSliceBootstrapsBeforeAnyLoadAndPinsFixedBudget(t *testing.T) {
	m, f := managerFixture(t)
	if err := m.checkSlice(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(callsNamed(f, "LoadUnit")) != 0 {
		t.Fatal("missing slice was implicitly loaded before budget creation")
	}
	starts := callsNamed(f, "StartTransientUnit")
	if len(starts) != 1 {
		t.Fatalf("slice starts=%d", len(starts))
	}
	if f.calls[0].method != managerInterface+".GetUnit" {
		t.Fatal("did not inspect loaded unit first")
	}
	values := map[string]any{}
	for _, p := range starts[0].args[2].([]unitProperty) {
		values[p.Name] = p.Value.Value()
	}
	for key, want := range map[string]any{"AddRef": true, "MemoryAccounting": true, "MemoryMax": MemoryCeiling, "MemoryHigh": uint64(450000000), "MemorySwapMax": uint64(0), "TasksMax": uint64(64), "StopWhenUnneeded": true} {
		if values[key] != want {
			t.Errorf("%s=%v want %v", key, values[key], want)
		}
	}
	if len(callsNamed(f, "RefUnit")) != 0 {
		t.Fatal("new slice was referenced twice instead of using atomic creation reference")
	}
	if err := m.release(""); err != nil {
		t.Fatal(err)
	}
	if f.closed != 1 || len(callsNamed(f, "StopUnit")) != 1 {
		t.Fatal("idle availability connection did not reclaim its empty slice")
	}
	refs := callsNamed(f, "UnrefUnit")
	if len(refs) != 1 || refs[0].args[0] != sliceName {
		t.Fatal("slice reference not released")
	}
}
func TestChanshuiSlicePinsCreationBeforeAutomaticCollection(t *testing.T) {
	m, f := managerFixture(t)
	f.expireUnpinned = true
	if err := m.checkSlice(t.Context()); err != nil {
		t.Fatalf("creation lost fixed slice properties before a separate reference: %v", err)
	}
	starts := callsNamed(f, "StartTransientUnit")
	var addRef bool
	for _, property := range starts[0].args[2].([]unitProperty) {
		if property.Name == "AddRef" {
			addRef, _ = property.Value.Value().(bool)
		}
	}
	if !addRef {
		t.Fatal("transient budget creation has no atomic connection reference")
	}
	if len(callsNamed(f, "RefUnit")) != 0 {
		t.Fatal("atomic creation added a duplicate reference")
	}
	if err := m.release(""); err != nil {
		t.Fatal(err)
	}
}
func TestChanshuiSliceOnlyLoadsDiskConfigurationOnUnitExists(t *testing.T) {
	for _, kind := range []string{"disk", "denied"} {
		t.Run(kind, func(t *testing.T) {
			m, f := managerFixture(t)
			name := "org.freedesktop.systemd1.UnitExists"
			if kind == "denied" {
				name = "org.freedesktop.DBus.Error.AccessDenied"
			}
			f.startErr = dbus.Error{Name: name}
			err := m.checkSlice(t.Context())
			if kind == "disk" {
				if err != nil || len(callsNamed(f, "LoadUnit")) != 1 {
					t.Fatalf("disk configuration not loaded/validated: %v", err)
				}
				if len(callsNamed(f, "RefUnit")) != 1 {
					t.Fatal("existing disk configuration was not referenced")
				}
				_ = m.release("")
			} else {
				if err == nil || len(callsNamed(f, "LoadUnit")) != 0 {
					t.Fatal("permission failure fell back to implicit unlimited load")
				}
				_ = f.Close()
			}
		})
	}
}
func TestChanshuiSliceRejectsBadExistingLimitsWithoutChangingThem(t *testing.T) {
	for _, item := range []struct {
		name, key string
		value     any
		missing   bool
	}{
		{"missing-swap", "MemorySwapMax", nil, true}, {"invalid-swap", "MemorySwapMax", "0", false}, {"negative-swap", "MemorySwapMax", int64(-1), false}, {"nonzero-swap", "MemorySwapMax", uint64(1), false},
		{"missing-max", "MemoryMax", nil, true}, {"invalid-max", "MemoryMax", true, false}, {"excessive-max", "MemoryMax", MemoryCeiling + 1, false}, {"no-max", "MemoryMax", uint64(0), false},
		{"missing-tasks", "TasksMax", nil, true}, {"invalid-tasks", "TasksMax", "64", false}, {"excessive-tasks", "TasksMax", uint64(65), false},
	} {
		t.Run(item.name, func(t *testing.T) {
			m, f := managerFixture(t)
			f.exists = true
			if item.missing {
				delete(f.props, item.key)
			} else {
				f.props[item.key] = dbus.MakeVariant(item.value)
			}
			if err := m.checkSlice(t.Context()); err == nil {
				t.Fatal("bad existing limit accepted")
			}
			requireNoRuntimeCreation(t, f)
			if len(callsNamed(f, "StopUnit")) != 0 {
				t.Fatal("bad existing slice was modified")
			}
			_ = f.Close()
		})
	}
	m, f := managerFixture(t)
	f.exists = true
	f.unit["StopWhenUnneeded"] = dbus.MakeVariant(false)
	if err := m.checkSlice(t.Context()); err == nil {
		t.Fatal("disabled empty-group cleanup accepted")
	}
	requireNoRuntimeCreation(t, f)
	_ = f.Close()
}
func TestChanshuiExistingHighAndAccountingRemainCompatible(t *testing.T) {
	for _, custom := range []bool{false, true} {
		m, f := managerFixture(t)
		f.exists = true
		if custom {
			f.props["MemoryHigh"] = dbus.MakeVariant(uint64(480000000))
			f.props["MemoryAccounting"] = dbus.MakeVariant(false)
		}
		if err := m.checkSlice(t.Context()); err != nil {
			t.Fatalf("old valid hard budget rejected: %v", err)
		}
		requireNoRuntimeCreation(t, f)
		_ = m.release("")
	}
}
func TestChanshuiFailedNewSliceReclaimsWhenReadOrLimitsFail(t *testing.T) {
	for _, failure := range []string{"read", "limits"} {
		t.Run(failure, func(t *testing.T) {
			m, f := managerFixture(t)
			switch failure {
			case "read":
				f.propsErr = errors.New("properties denied")
			case "limits":
				f.props["MemoryMax"] = dbus.MakeVariant(MemoryCeiling + 1)
			}
			if err := m.checkSlice(t.Context()); err == nil {
				t.Fatal("failed bootstrap accepted")
			}
			if len(callsNamed(f, "StopUnit")) != 1 || len(callsNamed(f, "UnrefUnit")) != 1 {
				t.Fatal("failed created slice not reclaimed and unreferenced")
			}
			_ = f.Close()
		})
	}
}
func TestChanshuiExistingSliceReferenceFailureDoesNotReplaceConfiguration(t *testing.T) {
	m, f := managerFixture(t)
	f.exists = true
	f.refErr = errors.New("reference denied")
	if err := m.checkSlice(t.Context()); err == nil || !strings.Contains(err.Error(), "无法保持检测限制") {
		t.Fatalf("reference failure not reported: %v", err)
	}
	requireNoRuntimeCreation(t, f)
	if len(callsNamed(f, "StopUnit")) != 0 || len(callsNamed(f, "UnrefUnit")) != 1 {
		t.Fatal("existing failed reference changed budget or leaked a connection reference")
	}
	_ = f.Close()
}
func TestChanshuiReleaseNeverStopsAnotherActiveQueryOrRun(t *testing.T) {
	for _, name := range []string{"transithub-chanshui-query-other.service", "transithub-chanshui-run-other.service"} {
		m, f := managerFixture(t)
		f.exists = true
		f.units = []listedUnit{{Name: name, ActiveState: "active"}}
		if err := m.release("transithub-chanshui-query-finished.service"); err != nil {
			t.Fatal(err)
		}
		if len(callsNamed(f, "StopUnit")) != 0 {
			t.Fatal("closed connection stopped another active query/run")
		}
		if f.closed != 1 || len(callsNamed(f, "UnrefUnit")) != 2 {
			t.Fatal("active sibling prevented connection references from closing")
		}
	}
}
func TestChanshuiReleaseErrorsStillCloseAllConnectionReferences(t *testing.T) {
	for _, failure := range []string{"list", "stop", "close"} {
		t.Run(failure, func(t *testing.T) {
			m, f := managerFixture(t)
			f.exists = true
			switch failure {
			case "list":
				f.listErr = errors.New("cannot list units")
			case "stop":
				f.stopErr = errors.New("cannot stop slice")
			case "close":
				f.closeErr = errors.New("cannot close bus")
			}
			if err := m.release(""); err == nil {
				t.Fatal("cleanup failure swallowed")
			}
			if f.closed != 1 || len(callsNamed(f, "UnrefUnit")) != 1 {
				t.Fatal("cleanup error leaked slice connection reference")
			}
		})
	}
}
func TestChanshuiProcessCloseIsIdempotentAndClosesSlice(t *testing.T) {
	m, f := managerFixture(t)
	f.exists = true
	dir := t.TempDir()
	files := make([]*os.File, 3)
	for i, name := range []string{"in", "out", "err"} {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
	}
	p := &process{manager: m, name: "transithub-chanshui-query-finished.service", input: files[0], output: files[1], stderr: files[2], exit: ExitInfo{Exited: true}}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if f.closed != 1 || len(callsNamed(f, "UnrefUnit")) != 2 || len(callsNamed(f, "StopUnit")) != 1 {
		t.Fatal("normal close leaked references or released twice")
	}
	for _, file := range files {
		if _, err := file.Stat(); err == nil {
			t.Fatal("normal close retained a pipe/file")
		}
	}
}

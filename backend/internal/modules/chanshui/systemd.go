package chanshui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const managerPath dbus.ObjectPath = "/org/freedesktop/systemd1"
const managerInterface = "org.freedesktop.systemd1.Manager"

// Serialize unit creation with stopping the empty shared slice. There is one
// Hub instance, but queries and a run may close/start at the same time.
var unitLifecycle sync.Mutex

type unitProperty struct {
	Name  string
	Value dbus.Variant
}
type unitAux struct {
	Name       string
	Properties []unitProperty
}
type unitExec struct {
	Path          string
	Args          []string
	IgnoreFailure bool
}
type listedUnit struct {
	Name, Description, LoadState, ActiveState, SubState, Following string
	Path                                                           dbus.ObjectPath
	JobID                                                          uint32
	JobType                                                        string
	JobPath                                                        dbus.ObjectPath
}
type managerConnection interface {
	Object(string, dbus.ObjectPath) dbus.BusObject
	Close() error
}
type systemManager struct {
	conn   managerConnection
	binary string
}

func connectManager(ctx context.Context, cfg RunnerConfig) (*systemManager, error) {
	address := "unix:path=/run/dbus/system_bus_socket"
	if cfg.Manager == "user" {
		address = "unix:path=/run/user/" + strconv.Itoa(os.Geteuid()) + "/bus"
	} else if cfg.Manager != "system" {
		return nil, fmt.Errorf("systemd管理器必须设置为system或user")
	}
	c, err := dbus.Dial(address)
	if err != nil {
		return nil, fmt.Errorf("无法连接%s systemd管理器：%w", cfg.Manager, err)
	}
	if err = c.Auth(nil); err == nil {
		err = c.Hello()
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("systemd连接认证失败：%w", err)
	}
	if !c.SupportsUnixFDs() {
		c.Close()
		return nil, fmt.Errorf("systemd连接不支持管道传递，不能安全交付检测密钥")
	}
	m := &systemManager{c, cfg.BinaryPath}
	managerProps, versionErr := m.props(ctx, managerPath, "Manager")
	version, _ := managerProps["Version"].Value().(string)
	end := 0
	for end < len(version) && version[end] >= '0' && version[end] <= '9' {
		end++
	}
	major, _ := strconv.Atoi(version[:end])
	if versionErr != nil || major < 252 {
		c.Close()
		return nil, fmt.Errorf("检测需要systemd 252或更新版本，当前版本无法确认或过旧；请升级后重试")
	}
	if err = m.checkSlice(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return m, nil
}
func (m *systemManager) call(ctx context.Context, method string, args ...any) *dbus.Call {
	return m.conn.Object("org.freedesktop.systemd1", managerPath).CallWithContext(ctx, managerInterface+"."+method, 0, args...)
}
func (m *systemManager) path(ctx context.Context, name string) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	err := m.call(ctx, "GetUnit", name).Store(&p)
	return p, err
}
func (m *systemManager) props(ctx context.Context, p dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var out map[string]dbus.Variant
	err := m.conn.Object("org.freedesktop.systemd1", p).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.systemd1."+iface).Store(&out)
	return out, err
}
func number(v dbus.Variant) uint64 {
	switch n := v.Value().(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case int64:
		if n >= 0 {
			return uint64(n)
		}
	}
	return 0
}
func (m *systemManager) checkSlice(ctx context.Context) (err error) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return fmt.Errorf("当前环境没有cgroup v2，不能启用服务器硬内存限制")
	}
	unitLifecycle.Lock()
	defer unitLifecycle.Unlock()
	created := false
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if created {
			err = errors.Join(err, m.stopEmptySlice(cleanup, ""))
		}
		_ = m.call(cleanup, "UnrefUnit", sliceName).Err
	}()
	p, err := m.path(ctx, sliceName)
	if err != nil {
		if !namedBusError(err, "org.freedesktop.systemd1.NoSuchUnit") {
			return fmt.Errorf("无法确认检测专用内存限制是否存在；请检查systemd连接权限后重试：%w", err)
		}
		properties := []unitProperty{
			{"Description", dbus.MakeVariant("TransitHub isolated model detection budget")},
			// systemd 252 adds this sender reference before queuing the start
			// job. A later RefUnit can lose the transient budget to automatic GC.
			{"AddRef", dbus.MakeVariant(true)},
			{"MemoryAccounting", dbus.MakeVariant(true)}, {"MemoryMax", dbus.MakeVariant(MemoryCeiling)},
			{"MemoryHigh", dbus.MakeVariant(uint64(450000000))}, {"MemorySwapMax", dbus.MakeVariant(uint64(0))},
			{"TasksMax", dbus.MakeVariant(uint64(64))}, {"StopWhenUnneeded", dbus.MakeVariant(true)},
		}
		if startErr := m.call(ctx, "StartTransientUnit", sliceName, "fail", properties, []unitAux{}).Err; startErr != nil {
			if !namedBusError(startErr, "org.freedesktop.systemd1.UnitExists") {
				return fmt.Errorf("无法自动建立检测专用硬内存限制；请检查systemd创建slice的权限并查看升级日志后重试：%w", startErr)
			}
			if err = m.call(ctx, "LoadUnit", sliceName).Store(&p); err != nil {
				return fmt.Errorf("磁盘已有检测限制配置，但无法加载；请检查原slice配置及systemd权限后重试：%w", err)
			}
		} else {
			created = true
			if p, err = m.path(ctx, sliceName); err != nil {
				return fmt.Errorf("检测限制已建立，但无法读取运行对象；请核查systemd运行状态后重试：%w", err)
			}
		}
	}
	if !created {
		if err = m.call(ctx, "RefUnit", sliceName).Err; err != nil {
			return fmt.Errorf("无法保持检测限制供本次启动核验；请检查systemd管理权限后重试：%w", err)
		}
	}
	props, err := m.props(ctx, p, "Slice")
	if err != nil {
		return fmt.Errorf("无法读取检测专用内存限制：%w", err)
	}
	unit, err := m.props(ctx, p, "Unit")
	if err != nil {
		return fmt.Errorf("无法读取检测进程组空闲回收设置：%w", err)
	}
	return validateSlice(props, unit)
}
func namedBusError(err error, name string) bool {
	var busError dbus.Error
	return errors.As(err, &busError) && busError.Name == name
}
func limitValue(v dbus.Variant) (uint64, bool) {
	switch n := v.Value().(type) {
	case uint64:
		return n, true
	case uint32:
		return uint64(n), true
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}
func validateSlice(props, unit map[string]dbus.Variant) error {
	if stop, ok := unit["StopWhenUnneeded"].Value().(bool); !ok || !stop {
		return fmt.Errorf("检测进程组未配置空闲回收，请在slice的[Unit]中设置StopWhenUnneeded=yes后重试；尚未启动检测")
	}
	max, maxOK := limitValue(props["MemoryMax"])
	swap, swapOK := limitValue(props["MemorySwapMax"])
	tasks, tasksOK := limitValue(props["TasksMax"])
	if !maxOK || !swapOK || !tasksOK {
		return fmt.Errorf("无法完整读取检测内存、swap或进程硬限制；请核查systemd版本和原slice配置，尚未启动检测")
	}
	if max == 0 || max > MemoryCeiling || swap != 0 || tasks == 0 || tasks > 64 {
		return fmt.Errorf("检测专用限制不符合要求：内存上限%d字节、swap上限%d字节、进程上限%d；须内存不超过500000000字节、swap为0、进程不超过64；请修正原slice配置后重试", max, swap, tasks)
	}
	return nil
}
func (m *systemManager) start(ctx context.Context, name, role string, cfg RunnerConfig, limit uint64, seconds int, in, out, stderr *os.File) error {
	unitLifecycle.Lock()
	defer unitLifecycle.Unlock()
	props := []unitProperty{
		{"Description", dbus.MakeVariant("TransitHub isolated model detection")}, {"Type", dbus.MakeVariant("exec")},
		{"Slice", dbus.MakeVariant(sliceName)}, {"MemoryAccounting", dbus.MakeVariant(true)}, {"MemoryMax", dbus.MakeVariant(limit)},
		{"MemorySwapMax", dbus.MakeVariant(uint64(0))}, {"KillMode", dbus.MakeVariant("control-group")},
		{"TimeoutStopUSec", dbus.MakeVariant(uint64(5000000))}, {"RuntimeMaxUSec", dbus.MakeVariant(uint64(seconds+10) * 1000000)},
		{"NoNewPrivileges", dbus.MakeVariant(true)}, {"Restart", dbus.MakeVariant("no")},
		{"WorkingDirectory", dbus.MakeVariant(cfg.ProjectDir)},
		{"Environment", dbus.MakeVariant([]string{"GOMAXPROCS=1", "GOMEMLIMIT=" + strconv.FormatUint(limit*3/4, 10)})},
		{"ExecStart", dbus.MakeVariant([]unitExec{{cfg.BinaryPath, []string{cfg.BinaryPath, role, "--db", filepath.Join(cfg.DataDir, "hub.sqlite")}, false}})},
		{"StandardInputFileDescriptor", dbus.MakeVariant(dbus.UnixFD(in.Fd()))},
		{"StandardOutputFileDescriptor", dbus.MakeVariant(dbus.UnixFD(out.Fd()))},
		{"StandardErrorFileDescriptor", dbus.MakeVariant(dbus.UnixFD(stderr.Fd()))},
	}
	if cfg.Manager == "system" {
		props = append(props, unitProperty{"User", dbus.MakeVariant(strconv.Itoa(os.Geteuid()))}, unitProperty{"Group", dbus.MakeVariant(strconv.Itoa(os.Getegid()))})
	}
	if err := m.call(ctx, "StartTransientUnit", name, "fail", props, []unitAux{}).Err; err != nil {
		return fmt.Errorf("systemd拒绝启动受限检测程序，请检查本模块启动权限：%w", err)
	}
	if err := m.call(ctx, "RefUnit", name).Err; err != nil {
		return fmt.Errorf("无法保留检测程序的退出诊断：%w", err)
	}
	return nil
}
func firstExecPath(v dbus.Variant) string {
	x := reflect.ValueOf(v.Value())
	if x.Kind() != reflect.Slice || x.Len() == 0 {
		return ""
	}
	x = x.Index(0)
	for x.Kind() == reflect.Interface {
		x = x.Elem()
	}
	if x.Kind() == reflect.Struct && x.NumField() > 0 {
		if s, ok := x.Field(0).Interface().(string); ok {
			return s
		}
	}
	if x.Kind() == reflect.Slice && x.Len() > 0 {
		if s, ok := x.Index(0).Interface().(string); ok {
			return s
		}
	}
	return ""
}
func (m *systemManager) inspect(ctx context.Context, name string) (ExitInfo, error) {
	p, err := m.path(ctx, name)
	if err != nil {
		return ExitInfo{}, err
	}
	props, err := m.props(ctx, p, "Service")
	if err != nil {
		return ExitInfo{}, err
	}
	if s, _ := props["Slice"].Value().(string); s != sliceName || firstExecPath(props["ExecStart"]) != m.binary {
		return ExitInfo{}, fmt.Errorf("检测程序归属与预期不符，拒绝操作不明进程")
	}
	info := ExitInfo{Result: fmt.Sprint(props["Result"].Value()), MemoryPeak: number(props["MemoryPeak"])}
	group, _ := props["ControlGroup"].Value().(string)
	populated := number(props["MainPID"]) != 0
	if group != "" {
		events, e := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.events"))
		if e == nil {
			populated = populated || strings.Contains(string(events), "populated 1")
		} else if !os.IsNotExist(e) {
			return info, fmt.Errorf("无法确认检测进程组是否清空：%w", e)
		}
	}
	info.Exited = !populated
	info.OOM = info.Result == "oom-kill"
	return info, nil
}
func (m *systemManager) stop(ctx context.Context, name string) error {
	if _, err := m.inspect(ctx, name); err != nil {
		return err
	}
	if err := m.call(ctx, "StopUnit", name, "replace").Err; err != nil {
		return err
	}
	return nil
}
func (m *systemManager) kill(ctx context.Context, name string) error {
	if _, err := m.inspect(ctx, name); err != nil {
		return err
	}
	return m.call(ctx, "KillUnit", name, "all", int32(9)).Err
}
func (m *systemManager) release(name string) (err error) {
	unitLifecycle.Lock()
	defer unitLifecycle.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer func() { err = errors.Join(err, m.conn.Close()) }()
	if name != "" {
		_ = m.call(ctx, "UnrefUnit", name).Err
		_ = m.call(ctx, "ResetFailedUnit", name).Err
	}
	err = m.stopEmptySlice(ctx, name)
	if unrefErr := m.call(ctx, "UnrefUnit", sliceName).Err; unrefErr != nil && !namedBusError(unrefErr, "org.freedesktop.systemd1.NoSuchUnit") {
		err = errors.Join(err, fmt.Errorf("检测程序已关闭，但无法释放内存限制的连接引用；请核查systemd运行状态：%w", unrefErr))
	}
	return err
}

// StopWhenUnneeded is rate limited by systemd (16 automatic stops/10 seconds).
// Explicitly stop only after all our services/jobs and the whole slice are empty.
// The creation lock prevents a new start between the check and StopUnit.
func (m *systemManager) stopEmptySlice(ctx context.Context, released string) error {
	for {
		var units []listedUnit
		if err := m.call(ctx, "ListUnitsByPatterns", []string{}, []string{"transithub-chanshui-run-*.service", "transithub-chanshui-query-*.service"}).Store(&units); err != nil {
			return fmt.Errorf("检测已退出，但无法核查空进程组回收：%w", err)
		}
		ownTransition := false
		for _, u := range units {
			if u.JobID == 0 && (u.ActiveState == "inactive" || u.ActiveState == "failed") {
				continue
			}
			if u.Name != released {
				return nil // Another run/query is active or has a start job queued.
			}
			ownTransition = true
		}
		if !ownTransition {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("检测程序已结束，但仍无法确认结束处理完成，请核查运行状态后重试：%w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	p, err := m.path(ctx, sliceName)
	if err != nil {
		if e, ok := err.(dbus.Error); ok && e.Name == "org.freedesktop.systemd1.NoSuchUnit" {
			return nil
		}
		return fmt.Errorf("无法核查检测空进程组：%w", err)
	}
	props, err := m.props(ctx, p, "Slice")
	if err != nil {
		if e, ok := err.(dbus.Error); ok && (e.Name == "org.freedesktop.DBus.Error.UnknownObject" || e.Name == "org.freedesktop.systemd1.NoSuchUnit") {
			return nil
		}
		return err
	}
	group, _ := props["ControlGroup"].Value().(string)
	if group != "" {
		raw, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.events"))
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("无法确认检测空进程组是否还有程序：%w", err)
		}
		if strings.Contains(string(raw), "populated 1") {
			return nil
		}
	}
	if err := m.call(ctx, "StopUnit", sliceName, "fail").Err; err != nil {
		return fmt.Errorf("检测程序已退出，但空进程组尚未回收，请核查运行状态后重试：%w", err)
	}
	for {
		props, err := m.props(ctx, p, "Unit")
		if err != nil {
			if e, ok := err.(dbus.Error); ok && (e.Name == "org.freedesktop.DBus.Error.UnknownObject" || e.Name == "org.freedesktop.systemd1.NoSuchUnit") {
				return nil
			}
			return err
		}
		state, _ := props["ActiveState"].Value().(string)
		if state == "inactive" || state == "failed" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("检测程序已退出，但空进程组回收等待超时，请核查运行状态后重试：%w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

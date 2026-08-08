package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"transithub/backend/internal/config"
)

type fakeRollbackExecutor struct {
	status     RollbackStatusResponse
	statusErr  error
	startErr   error
	startCalls int
}

func (f *fakeRollbackExecutor) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}

func (f *fakeRollbackExecutor) Status(context.Context) (RollbackStatusResponse, error) {
	return f.status, f.statusErr
}

// newIdleRollbackExecutor 供升级/重启既有测试复用，表示回滚处于空闲、不干扰互检。
func newIdleRollbackExecutor() *fakeRollbackExecutor {
	return &fakeRollbackExecutor{status: RollbackStatusResponse{State: RollbackStateIdle}}
}

func newRollbackTestService(executor RollbackExecutor) *Service {
	return newServiceWithExecutors(
		config.Config{},
		&fakeUpgradeExecutor{status: UpgradeStatusResponse{State: UpgradeStateIdle}},
		&fakeRestartExecutor{status: RestartStatusResponse{State: RestartStateIdle}},
		executor,
	)
}

func TestStartRollbackStartsFixedExecutor(t *testing.T) {
	executor := newIdleRollbackExecutor()
	service := newRollbackTestService(executor)

	response, err := service.StartRollback(context.Background())
	if err != nil {
		t.Fatalf("StartRollback 返回错误：%v", err)
	}
	if executor.startCalls != 1 {
		t.Fatalf("期望启动执行器 1 次，实际 %d 次", executor.startCalls)
	}
	if response.State != RollbackStateStarting {
		t.Fatalf("期望状态 %s，实际 %s", RollbackStateStarting, response.State)
	}
	if response.RequestedAt == "" {
		t.Fatal("期望返回 requestedAt")
	}
}

func TestStartRollbackRejectsWhenRollbackPointMissing(t *testing.T) {
	executor := newIdleRollbackExecutor()
	executor.startErr = ErrRollbackPointMissing
	service := newRollbackTestService(executor)

	_, err := service.StartRollback(context.Background())
	if !errors.Is(err, ErrRollbackPointMissing) {
		t.Fatalf("期望 ErrRollbackPointMissing，实际 %v", err)
	}
}

func TestStartRollbackRejectsWhenUpgradeRunning(t *testing.T) {
	executor := newIdleRollbackExecutor()
	service := newServiceWithExecutors(
		config.Config{},
		&fakeUpgradeExecutor{status: UpgradeStatusResponse{State: UpgradeStateRunning}},
		&fakeRestartExecutor{status: RestartStatusResponse{State: RestartStateIdle}},
		executor,
	)

	_, err := service.StartRollback(context.Background())
	if !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("期望 ErrUpgradeInProgress，实际 %v", err)
	}
	if executor.startCalls != 0 {
		t.Fatalf("互检失败时不应启动回滚，实际启动 %d 次", executor.startCalls)
	}
}

func TestStartRollbackRejectsWhenRestartRunning(t *testing.T) {
	executor := newIdleRollbackExecutor()
	service := newServiceWithExecutors(
		config.Config{},
		&fakeUpgradeExecutor{status: UpgradeStatusResponse{State: UpgradeStateIdle}},
		&fakeRestartExecutor{status: RestartStatusResponse{State: RestartStateRunning}},
		executor,
	)

	_, err := service.StartRollback(context.Background())
	if !errors.Is(err, ErrRestartInProgress) {
		t.Fatalf("期望 ErrRestartInProgress，实际 %v", err)
	}
	if executor.startCalls != 0 {
		t.Fatalf("互检失败时不应启动回滚，实际启动 %d 次", executor.startCalls)
	}
}

func TestStartUpgradeRejectsWhenRollbackRunning(t *testing.T) {
	rollbackExecutor := newIdleRollbackExecutor()
	rollbackExecutor.status = RollbackStatusResponse{State: RollbackStateRunning}
	upgradeExecutor := &fakeUpgradeExecutor{status: UpgradeStatusResponse{State: UpgradeStateIdle}}
	service := newServiceWithExecutors(
		config.Config{},
		upgradeExecutor,
		&fakeRestartExecutor{status: RestartStatusResponse{State: RestartStateIdle}},
		rollbackExecutor,
	)

	_, err := service.StartUpgrade(context.Background())
	if !errors.Is(err, ErrRollbackInProgress) {
		t.Fatalf("期望 ErrRollbackInProgress，实际 %v", err)
	}
	if upgradeExecutor.startCalls != 0 {
		t.Fatalf("回滚执行中不应启动升级，实际启动 %d 次", upgradeExecutor.startCalls)
	}
}

func TestStartRestartRejectsWhenRollbackRunning(t *testing.T) {
	rollbackExecutor := newIdleRollbackExecutor()
	rollbackExecutor.status = RollbackStatusResponse{State: RollbackStateStarting}
	restartExecutor := &fakeRestartExecutor{status: RestartStatusResponse{State: RestartStateIdle}}
	service := newServiceWithExecutors(
		config.Config{},
		&fakeUpgradeExecutor{status: UpgradeStatusResponse{State: UpgradeStateIdle}},
		restartExecutor,
		rollbackExecutor,
	)

	_, err := service.StartRestart(context.Background())
	if !errors.Is(err, ErrRollbackInProgress) {
		t.Fatalf("期望 ErrRollbackInProgress，实际 %v", err)
	}
	if restartExecutor.startCalls != 0 {
		t.Fatalf("回滚执行中不应启动重启，实际启动 %d 次", restartExecutor.startCalls)
	}
}

// 还原点由 deploy/update-source.sh 写入，schemaVersion 是 JSON 数字而非字符串。
// 这里用脚本的真实输出格式解析，避免两侧契约漂移导致还原点永久不可用。
func TestRollbackPointParsesScriptWrittenPayload(t *testing.T) {
	dir := t.TempDir()
	pointPath := filepath.Join(dir, "rollback-point.json")
	payload := `{"commit":"479e40f","version":"V2.0.9","schemaVersion":21,` +
		`"dumpPath":"/opt/transithub/backups/transithub.dump","capturedAt":"2026-08-08T00:00:00.000Z"}`
	if err := os.WriteFile(pointPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("写入还原点失败：%v", err)
	}

	executor := &systemdRollbackExecutor{pointPath: pointPath}
	point, err := executor.Point()
	if err != nil {
		t.Fatalf("解析脚本写入的还原点失败：%v", err)
	}
	if point.Commit != "479e40f" {
		t.Fatalf("期望提交号 479e40f，实际 %s", point.Commit)
	}
	if point.SchemaVersion != 21 {
		t.Fatalf("期望 schema 版本 21，实际 %d", point.SchemaVersion)
	}
}

func TestRollbackPointMissingFileReportsMissing(t *testing.T) {
	executor := &systemdRollbackExecutor{pointPath: filepath.Join(t.TempDir(), "absent.json")}
	if _, err := executor.Point(); !errors.Is(err, ErrRollbackPointMissing) {
		t.Fatalf("期望 ErrRollbackPointMissing，实际 %v", err)
	}
}

func TestRollbackStatusPropagatesRollbackPoint(t *testing.T) {
	executor := newIdleRollbackExecutor()
	executor.status = RollbackStatusResponse{
		State: RollbackStateIdle,
		Point: &RollbackPoint{
			Commit:  "479e40f",
			Version: "V2.0.9",
		},
	}
	service := newRollbackTestService(executor)

	response, err := service.RollbackStatus(context.Background())
	if err != nil {
		t.Fatalf("RollbackStatus 返回错误：%v", err)
	}
	if response.Point == nil {
		t.Fatal("期望返回还原点信息")
	}
	if response.Point.Version != "V2.0.9" {
		t.Fatalf("期望还原点版本 V2.0.9，实际 %s", response.Point.Version)
	}
}

// running 期间被 SIGKILL/OOM/断电中断时 wrapper 无法自写终态。
// 若不兜底，状态永久停在 running，而三方维护互斥以此判定，
// 会让升级、重启、回滚全部永久阻塞，只能人工删状态文件解锁。
func TestSystemdRollbackExecutorReconcilesKilledRunningRollback(t *testing.T) {
	tempDir := t.TempDir()
	statusPath := filepath.Join(tempDir, "rollback-status.json")
	logPath := filepath.Join(tempDir, "rollback.log")
	if err := os.WriteFile(statusPath, []byte(`{"state":"running","startedAt":"2026-08-08T08:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &systemdRollbackExecutor{
		statusPath: statusPath,
		logPath:    logPath,
		pointPath:  filepath.Join(tempDir, "absent-point.json"),
		runCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "systemctl" {
				t.Fatalf("意外的命令：%s %v", name, args)
			}
			return []byte("ActiveState=failed\nResult=signal\nExecMainStatus=0\n"), nil
		},
	}

	status, err := executor.Status(context.Background())
	if err != nil {
		t.Fatalf("兜底改写失败：%v", err)
	}
	if status.State != RollbackStateFailed {
		t.Fatalf("期望被改写为 failed，实际 %+v", status)
	}
	if status.FinishedAt == "" {
		t.Fatal("兜底改写后缺少 finishedAt")
	}
	if !strings.Contains(status.Output, "执行中被中断") {
		t.Fatalf("期望 running 专属提示，实际 %q", status.Output)
	}

	// 必须落盘，否则下次读取仍是 running，互斥依旧锁死。
	persisted, err := readRollbackStatus(statusPath)
	if err != nil {
		t.Fatalf("重读状态失败：%v", err)
	}
	if persisted.State != RollbackStateFailed {
		t.Fatalf("兜底结果未落盘，实际 %+v", persisted)
	}
}

// 单元仍在活跃时不得把正常执行中的回滚误判为失败。
func TestSystemdRollbackExecutorKeepsRunningWhileUnitActive(t *testing.T) {
	tempDir := t.TempDir()
	statusPath := filepath.Join(tempDir, "rollback-status.json")
	if err := os.WriteFile(statusPath, []byte(`{"state":"running","startedAt":"2026-08-08T08:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &systemdRollbackExecutor{
		statusPath: statusPath,
		logPath:    filepath.Join(tempDir, "rollback.log"),
		pointPath:  filepath.Join(tempDir, "absent-point.json"),
		runCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ActiveState=activating\nResult=success\nExecMainStatus=0\n"), nil
		},
	}

	status, err := executor.Status(context.Background())
	if err != nil {
		t.Fatalf("读取状态失败：%v", err)
	}
	if status.State != RollbackStateRunning {
		t.Fatalf("活跃单元应保持 running，实际 %+v", status)
	}
}

// wrapper 在 systemctl show 与重读之间写入终态时，必须采用 wrapper 的结果。
func TestSystemdRollbackExecutorPreservesFinalStatusWrittenDuringReconcile(t *testing.T) {
	tempDir := t.TempDir()
	statusPath := filepath.Join(tempDir, "rollback-status.json")
	if err := os.WriteFile(statusPath, []byte(`{"state":"running","startedAt":"2026-08-08T08:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := &systemdRollbackExecutor{
		statusPath: statusPath,
		logPath:    filepath.Join(tempDir, "rollback.log"),
		pointPath:  filepath.Join(tempDir, "absent-point.json"),
		runCommand: func(context.Context, string, ...string) ([]byte, error) {
			payload := `{"state":"succeeded","startedAt":"2026-08-08T08:00:00Z","finishedAt":"2026-08-08T08:06:00Z","exitCode":0}`
			if err := os.WriteFile(statusPath, []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			return []byte("ActiveState=inactive\nResult=success\nExecMainStatus=0\n"), nil
		},
	}

	status, err := executor.Status(context.Background())
	if err != nil {
		t.Fatalf("读取状态失败：%v", err)
	}
	if status.State != RollbackStateSucceeded {
		t.Fatalf("应保留 wrapper 写入的终态，实际 %+v", status)
	}
}

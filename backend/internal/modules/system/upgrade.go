package system

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	upgradeUnitName          = "transithub-upgrade.service"
	defaultUpgradeStatusPath = "/var/lib/transithub/upgrade-status.json"
	defaultUpgradeLogPath    = "/var/lib/transithub/upgrade.log"
	maximumUpgradeLogBytes   = 32 * 1024
)

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type systemdUpgradeExecutor struct {
	statusPath string
	logPath    string
	runCommand commandRunner
}

func newSystemdUpgradeExecutor() *systemdUpgradeExecutor {
	return &systemdUpgradeExecutor{
		statusPath: defaultUpgradeStatusPath,
		logPath:    defaultUpgradeLogPath,
		runCommand: runCombinedOutput,
	}
}

func runCombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (e *systemdUpgradeExecutor) Start(ctx context.Context) error {
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := e.prepareStart(startedAt); err != nil {
		return fmt.Errorf("准备系统升级状态失败：%w", err)
	}

	runner := e.runCommand
	if runner == nil {
		runner = runCombinedOutput
	}
	output, err := runner(ctx, "systemctl", "start", "--no-block", upgradeUnitName)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	startErr := err
	if detail != "" {
		startErr = fmt.Errorf("%w：%s", err, detail)
	}
	if recordErr := e.recordStartFailure(startedAt, startErr); recordErr != nil {
		return fmt.Errorf("%w（记录失败状态时出错：%v）", startErr, recordErr)
	}
	return startErr
}

func (e *systemdUpgradeExecutor) prepareStart(startedAt string) error {
	statusPath, logPath := e.paths()
	if err := os.MkdirAll(filepath.Dir(statusPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(logPath, 0o644); err != nil {
		return err
	}
	return writeUpgradeStatus(statusPath, UpgradeStatusResponse{
		State:     UpgradeStateStarting,
		StartedAt: startedAt,
	})
}

func (e *systemdUpgradeExecutor) recordStartFailure(startedAt string, startErr error) error {
	statusPath, logPath := e.paths()
	message := fmt.Sprintf("启动系统升级单元失败：%v\n", startErr)
	if err := appendUpgradeLog(logPath, message); err != nil {
		return err
	}
	exitCode := 1
	return writeUpgradeStatus(statusPath, UpgradeStatusResponse{
		State:      UpgradeStateFailed,
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ExitCode:   &exitCode,
	})
}

func (e *systemdUpgradeExecutor) paths() (string, string) {
	statusPath := e.statusPath
	if statusPath == "" {
		statusPath = defaultUpgradeStatusPath
	}
	logPath := e.logPath
	if logPath == "" {
		logPath = defaultUpgradeLogPath
	}
	return statusPath, logPath
}

func writeUpgradeStatus(statusPath string, status UpgradeStatusResponse) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	nextPath := statusPath + ".next"
	if err := os.WriteFile(nextPath, payload, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(nextPath, 0o644); err != nil {
		_ = os.Remove(nextPath)
		return err
	}
	if err := os.Rename(nextPath, statusPath); err != nil {
		_ = os.Remove(nextPath)
		return err
	}
	return nil
}

func appendUpgradeLog(logPath, message string) error {
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(message)
	return err
}

func (e *systemdUpgradeExecutor) Status(ctx context.Context) (UpgradeStatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return UpgradeStatusResponse{}, err
	}
	statusPath, logPath := e.paths()
	payload, err := os.ReadFile(statusPath)
	if errorsIsNotExist(err) {
		return UpgradeStatusResponse{State: UpgradeStateIdle}, nil
	}
	if err != nil {
		return UpgradeStatusResponse{}, err
	}

	var status UpgradeStatusResponse
	if err := json.Unmarshal(payload, &status); err != nil {
		return UpgradeStatusResponse{}, fmt.Errorf("解析升级状态失败：%w", err)
	}
	if !validUpgradeState(status.State) {
		return UpgradeStatusResponse{}, fmt.Errorf("未知的升级状态：%s", status.State)
	}
	// starting 与 running 都需要兜底：wrapper 被 SIGKILL/OOM/断电时无法自写终态，
	// 状态会永久停在 running，而三方维护互斥以此判定，导致全部维护动作永久阻塞。
	if status.State == UpgradeStateStarting || status.State == UpgradeStateRunning {
		var reconcileErr error
		status, reconcileErr = e.reconcileStatus(ctx, status)
		if reconcileErr != nil {
			return UpgradeStatusResponse{}, reconcileErr
		}
	}
	if status.State == UpgradeStateFailed {
		output, logErr := readFileTail(logPath, maximumUpgradeLogBytes)
		if logErr != nil {
			status.Output = fmt.Sprintf("读取升级日志失败：%v", logErr)
		} else {
			status.Output = string(output)
		}
	}
	return status, nil
}

func (e *systemdUpgradeExecutor) reconcileStatus(ctx context.Context, status UpgradeStatusResponse) (UpgradeStatusResponse, error) {
	runner := e.runCommand
	if runner == nil {
		runner = runCombinedOutput
	}
	output, err := runner(
		ctx,
		"systemctl",
		"show",
		upgradeUnitName,
		"--property=ActiveState",
		"--property=Result",
		"--property=ExecMainStatus",
		"--property=Job",
		"--no-pager",
	)
	if err != nil {
		return UpgradeStatusResponse{}, fmt.Errorf("读取系统升级单元状态失败：%w", err)
	}

	properties := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[key] = value
		}
	}
	if properties["Job"] != "" {
		return status, nil
	}
	activeState := properties["ActiveState"]
	if activeState != "failed" && activeState != "inactive" {
		return status, nil
	}

	statusPath, logPath := e.paths()
	latestStatus, err := readUpgradeStatus(statusPath)
	if err != nil {
		return UpgradeStatusResponse{}, err
	}
	stillPending := latestStatus.State == UpgradeStateStarting || latestStatus.State == UpgradeStateRunning
	if !stillPending || latestStatus.StartedAt != status.StartedAt {
		return latestStatus, nil
	}
	status = latestStatus
	interruptedState := status.State

	exitCode := 1
	if parsed, parseErr := strconv.Atoi(properties["ExecMainStatus"]); parseErr == nil && parsed != 0 {
		exitCode = parsed
	}
	status.State = UpgradeStateFailed
	status.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	status.ExitCode = &exitCode
	reason := "系统升级单元异步启动失败"
	switch {
	case interruptedState == UpgradeStateRunning:
		reason = "系统升级执行中被中断且未写入最终状态，请检查升级日志与当前服务版本后再重试"
	case activeState == "inactive":
		reason = "系统升级单元已结束但未写入最终状态"
	}
	message := fmt.Sprintf("%s：\n%s", reason, strings.TrimSpace(string(output)))
	if err := appendUpgradeLog(logPath, message+"\n"); err != nil {
		return UpgradeStatusResponse{}, err
	}
	if err := writeUpgradeStatus(statusPath, status); err != nil {
		return UpgradeStatusResponse{}, err
	}
	return status, nil
}

func readUpgradeStatus(statusPath string) (UpgradeStatusResponse, error) {
	payload, err := os.ReadFile(statusPath)
	if err != nil {
		return UpgradeStatusResponse{}, err
	}
	var status UpgradeStatusResponse
	if err := json.Unmarshal(payload, &status); err != nil {
		return UpgradeStatusResponse{}, fmt.Errorf("解析升级状态失败：%w", err)
	}
	if !validUpgradeState(status.State) {
		return UpgradeStatusResponse{}, fmt.Errorf("未知的升级状态：%s", status.State)
	}
	return status, nil
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

func validUpgradeState(state UpgradeState) bool {
	switch state {
	case UpgradeStateStarting, UpgradeStateRunning, UpgradeStateSucceeded, UpgradeStateFailed:
		return true
	default:
		return false
	}
}

func readFileTail(path string, maximumBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maximumBytes {
		if _, err := file.Seek(-maximumBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(file, maximumBytes))
}

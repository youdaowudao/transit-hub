package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	rollbackUnitName          = "transithub-rollback.service"
	defaultRollbackStatusPath = "/var/lib/transithub/rollback-status.json"
	defaultRollbackLogPath    = "/var/lib/transithub/rollback.log"
	defaultRollbackPointPath  = "/var/lib/transithub/rollback-point.json"
	maximumRollbackLogBytes   = 32 * 1024
)

// ErrRollbackPointMissing 表示尚未记录还原点：升级脚本在切换代码前才会写入它，
// 因此全新部署或从未升级过的实例无法回滚。
var ErrRollbackPointMissing = errors.New("当前没有可用的还原点")

type systemdRollbackExecutor struct {
	statusPath string
	logPath    string
	pointPath  string
	runCommand commandRunner
}

func newSystemdRollbackExecutor() *systemdRollbackExecutor {
	return &systemdRollbackExecutor{
		statusPath: defaultRollbackStatusPath,
		logPath:    defaultRollbackLogPath,
		pointPath:  defaultRollbackPointPath,
		runCommand: runCombinedOutput,
	}
}

func (e *systemdRollbackExecutor) Start(ctx context.Context) error {
	if _, err := e.Point(); err != nil {
		return err
	}

	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := e.prepareStart(startedAt); err != nil {
		return fmt.Errorf("准备版本回滚状态失败：%w", err)
	}

	runner := e.runCommand
	if runner == nil {
		runner = runCombinedOutput
	}
	output, err := runner(ctx, "systemctl", "start", "--no-block", rollbackUnitName)
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

// Point 读取还原点；文件不存在时返回 ErrRollbackPointMissing。
func (e *systemdRollbackExecutor) Point() (RollbackPoint, error) {
	pointPath := e.pointPath
	if pointPath == "" {
		pointPath = defaultRollbackPointPath
	}
	payload, err := os.ReadFile(pointPath)
	if errorsIsNotExist(err) {
		return RollbackPoint{}, ErrRollbackPointMissing
	}
	if err != nil {
		return RollbackPoint{}, err
	}

	var point RollbackPoint
	if err := json.Unmarshal(payload, &point); err != nil {
		return RollbackPoint{}, fmt.Errorf("解析还原点失败：%w", err)
	}
	if strings.TrimSpace(point.Commit) == "" {
		return RollbackPoint{}, fmt.Errorf("%w：还原点缺少提交号", ErrRollbackPointMissing)
	}
	return point, nil
}

func (e *systemdRollbackExecutor) prepareStart(startedAt string) error {
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
	return writeRollbackStatus(statusPath, RollbackStatusResponse{
		State:     RollbackStateStarting,
		StartedAt: startedAt,
	})
}

func (e *systemdRollbackExecutor) recordStartFailure(startedAt string, startErr error) error {
	statusPath, logPath := e.paths()
	message := fmt.Sprintf("启动版本回滚单元失败：%v\n", startErr)
	if err := appendRollbackLog(logPath, message); err != nil {
		return err
	}
	exitCode := 1
	return writeRollbackStatus(statusPath, RollbackStatusResponse{
		State:      RollbackStateFailed,
		StartedAt:  startedAt,
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ExitCode:   &exitCode,
	})
}

func (e *systemdRollbackExecutor) paths() (string, string) {
	statusPath := e.statusPath
	if statusPath == "" {
		statusPath = defaultRollbackStatusPath
	}
	logPath := e.logPath
	if logPath == "" {
		logPath = defaultRollbackLogPath
	}
	return statusPath, logPath
}

func writeRollbackStatus(statusPath string, status RollbackStatusResponse) error {
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

func appendRollbackLog(logPath, message string) error {
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(message)
	return err
}
func (e *systemdRollbackExecutor) Status(ctx context.Context) (RollbackStatusResponse, error) {
	if err := ctx.Err(); err != nil {
		return RollbackStatusResponse{}, err
	}
	statusPath, logPath := e.paths()
	payload, err := os.ReadFile(statusPath)
	if errorsIsNotExist(err) {
		return e.withPoint(RollbackStatusResponse{State: RollbackStateIdle})
	}
	if err != nil {
		return RollbackStatusResponse{}, err
	}

	var status RollbackStatusResponse
	if err := json.Unmarshal(payload, &status); err != nil {
		return RollbackStatusResponse{}, fmt.Errorf("解析回滚状态失败：%w", err)
	}
	if !validRollbackState(status.State) {
		return RollbackStatusResponse{}, fmt.Errorf("未知的回滚状态：%s", status.State)
	}
	// starting 与 running 都需要兜底：wrapper 在 running 期间被 SIGKILL、OOM
	// 或断电中断时无法自写终态，状态会永久停在 running，而三方维护互斥以此判定，
	// 会导致升级、重启、回滚全部永久阻塞。running 窗口覆盖整个构建与健康检查，
	// 远长于 starting，被强杀的概率也更高。
	if status.State == RollbackStateStarting || status.State == RollbackStateRunning {
		status, err = e.reconcileStartingStatus(ctx, status)
		if err != nil {
			return RollbackStatusResponse{}, err
		}
	}
	if status.State == RollbackStateFailed {
		output, logErr := readFileTail(logPath, maximumRollbackLogBytes)
		if logErr != nil {
			status.Output = fmt.Sprintf("读取回滚日志失败：%v", logErr)
		} else {
			status.Output = string(output)
		}
	}
	return e.withPoint(status)
}

// withPoint 附加当前还原点。还原点缺失是正常状态（尚未升级过），
// 此时保持 Point 为空，由前端据此禁用回滚按钮。
func (e *systemdRollbackExecutor) withPoint(status RollbackStatusResponse) (RollbackStatusResponse, error) {
	point, err := e.Point()
	if err != nil {
		if errors.Is(err, ErrRollbackPointMissing) {
			return status, nil
		}
		return RollbackStatusResponse{}, err
	}
	status.Point = &point
	return status, nil
}

func (e *systemdRollbackExecutor) reconcileStartingStatus(ctx context.Context, status RollbackStatusResponse) (RollbackStatusResponse, error) {
	runner := e.runCommand
	if runner == nil {
		runner = runCombinedOutput
	}
	output, err := runner(
		ctx,
		"systemctl",
		"show",
		rollbackUnitName,
		"--property=ActiveState",
		"--property=Result",
		"--property=ExecMainStatus",
		"--property=Job",
		"--no-pager",
	)
	if err != nil {
		return RollbackStatusResponse{}, fmt.Errorf("读取版本回滚单元状态失败：%w", err)
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
	latestStatus, err := readRollbackStatus(statusPath)
	if err != nil {
		return RollbackStatusResponse{}, err
	}
	// 重读一次以避开竞态：wrapper 可能在 systemctl show 与这次读取之间写入终态。
	// 状态仍停在 starting/running 且 StartedAt 未变，才说明确实是同一次回滚残留。
	stillPending := latestStatus.State == RollbackStateStarting || latestStatus.State == RollbackStateRunning
	if !stillPending || latestStatus.StartedAt != status.StartedAt {
		return latestStatus, nil
	}
	status = latestStatus
	interruptedState := status.State

	exitCode := 1
	if parsed, parseErr := strconv.Atoi(properties["ExecMainStatus"]); parseErr == nil && parsed != 0 {
		exitCode = parsed
	}
	status.State = RollbackStateFailed
	status.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	status.ExitCode = &exitCode
	reason := "版本回滚单元异步启动失败"
	switch {
	case interruptedState == RollbackStateRunning:
		// 已进入 running 说明脚本确实跑起来过，随后被强制终止或宿主重启。
		// 此时部署目录可能停在半完成状态，必须提示人工核对再重试。
		reason = "版本回滚执行中被中断且未写入最终状态，请检查回滚日志与当前服务版本后再重试"
	case activeState == "inactive":
		reason = "版本回滚单元已结束但未写入最终状态"
	}
	message := fmt.Sprintf("%s：\n%s", reason, strings.TrimSpace(string(output)))
	if err := appendRollbackLog(logPath, message+"\n"); err != nil {
		return RollbackStatusResponse{}, err
	}
	if err := writeRollbackStatus(statusPath, status); err != nil {
		return RollbackStatusResponse{}, err
	}
	return status, nil
}

func readRollbackStatus(statusPath string) (RollbackStatusResponse, error) {
	payload, err := os.ReadFile(statusPath)
	if err != nil {
		return RollbackStatusResponse{}, err
	}
	var status RollbackStatusResponse
	if err := json.Unmarshal(payload, &status); err != nil {
		return RollbackStatusResponse{}, fmt.Errorf("解析回滚状态失败：%w", err)
	}
	if !validRollbackState(status.State) {
		return RollbackStatusResponse{}, fmt.Errorf("未知的回滚状态：%s", status.State)
	}
	return status, nil
}

func validRollbackState(state RollbackState) bool {
	switch state {
	case RollbackStateIdle, RollbackStateStarting, RollbackStateRunning, RollbackStateSucceeded, RollbackStateFailed:
		return true
	default:
		return false
	}
}

package system

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 回滚 wrapper 写出的状态文件必须能被 RollbackStatusResponse 解析。
// 曾出现 wrapper 写 status/message 而 Go 侧读 state/output 的字段名错位，
// 导致回滚终态在 API 层恒报「未知的回滚状态」。
func TestRollbackWrapperStatusMatchesResponseContract(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/run-source-rollback.sh")
	for _, want := range []string{
		`printf '{"state":"%s","startedAt":"%s","finishedAt":"%s","exitCode":%s}\n'`,
		`printf '{"state":"%s","startedAt":"%s"}\n'`,
		"write_status 'running'",
	} {
		requireTextContains(t, script, want)
	}

	// 失败原因走日志，Go 侧在 failed 时从 rollback.log 读取 Output。
	requireTextNotContains(t, script, `"status":"`)
	requireTextNotContains(t, script, `"message":"`)

	for name, payload := range map[string]string{
		"running":   `{"state":"running","startedAt":"2026-08-08T01:02:03.123456789Z"}`,
		"succeeded": `{"state":"succeeded","startedAt":"2026-08-08T01:02:03.1Z","finishedAt":"2026-08-08T01:05:00.2Z","exitCode":0}`,
		"failed":    `{"state":"failed","startedAt":"2026-08-08T01:02:03.1Z","finishedAt":"2026-08-08T01:05:00.2Z","exitCode":78}`,
	} {
		var status RollbackStatusResponse
		if err := json.Unmarshal([]byte(payload), &status); err != nil {
			t.Fatalf("%s 状态解析失败：%v", name, err)
		}
		if !validRollbackState(status.State) {
			t.Fatalf("%s 状态未通过校验：%q", name, status.State)
		}
		if status.StartedAt == "" {
			t.Fatalf("%s 状态缺少 startedAt", name)
		}
	}
}

// reconcileStartingStatus 以 StartedAt 比对是否为同一次回滚，
// wrapper 必须继承 Go 侧 Start 写入的值，重新取值会让兜底改写永久失配。
func TestRollbackWrapperInheritsStartedAt(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/run-source-rollback.sh")
	for _, want := range []string{
		"read_started_at()",
		`started_at="$(read_started_at)"`,
		`"startedAt"`,
	} {
		requireTextContains(t, script, want)
	}
}

// 信号 trap 必须与升级、重启 wrapper 等齐，缺 HUP 会让状态卡在 starting。
func TestRollbackWrapperCoversAllTerminationSignals(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/run-source-rollback.sh")
	for _, want := range []string{
		`trap 'rm -f "$STATUS_NEXT"' EXIT`,
		`trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR`,
		"trap 'on_signal INT 130' INT",
		"trap 'on_signal TERM 143' TERM",
		"trap 'on_signal HUP 129' HUP",
	} {
		requireTextContains(t, script, want)
	}
}

// 还原点的 commit 必须在 git switch 之前取，否则记录的是新版提交，回滚等于空转。
func TestUpgradeCapturesRollbackPointBeforeSwitch(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/update-source.sh")

	capture := strings.Index(script, `previous_commit="$(git rev-parse HEAD)"`)
	if capture < 0 {
		t.Fatal("未找到升级前的 previous_commit 捕获语句")
	}
	switchAt := strings.Index(script, "git switch --detach")
	if switchAt < 0 {
		t.Fatal("未找到 git switch --detach 语句")
	}
	if capture > switchAt {
		t.Fatalf("previous_commit 捕获位置 %d 晚于 git switch %d", capture, switchAt)
	}
}

// 回滚必须与升级、重启抢同一把维护锁，三者互斥。
func TestRollbackSharesMaintenanceLock(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	for _, want := range []string{
		"LOCK_FILE='/run/lock/transithub-maintenance.lock'",
		"flock -n 9",
	} {
		requireTextContains(t, script, want)
	}
}

// wrapper 与 unit 都必须指向仓库外副本：git switch 会重写仓库内脚本自身。
func TestRollbackRunsFromOutsideRepository(t *testing.T) {
	requireTextContains(t,
		readProjectFileForUpgradeTest(t, "deploy/run-source-rollback.sh"),
		"ROLLBACK_SCRIPT='/opt/transithub/rollback-source.sh'")
	requireTextContains(t,
		readProjectFileForUpgradeTest(t, "deploy/transithub-rollback.service"),
		"ExecStart=/opt/transithub/run-source-rollback.sh")
}

func TestSourceRollbackUnitProvidesGoBuildEnvironment(t *testing.T) {
	unit := readProjectFileForUpgradeTest(t, "deploy/transithub-rollback.service")
	for _, want := range []string{
		"Type=oneshot",
		"WorkingDirectory=/opt/transithub",
		"ExecStart=/opt/transithub/run-source-rollback.sh",
		"TimeoutStartSec=2400",
		"Environment=HOME=/root",
		"Environment=GOPATH=/root/go",
		"Environment=GOMODCACHE=/root/go/pkg/mod",
		"Environment=GOCACHE=/root/.cache/go-build",
	} {
		requireTextContains(t, unit, want)
	}
}

// 回滚脚本的关键契约：先判定再切换代码、构建到临时目录后原子替换、
// 回滚前留一份兜底备份、重启后必须等到健康检查通过。
func TestSourceRollbackScriptContract(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	for _, want := range []string{
		"STATE_DIR='/var/lib/transithub'",
		`ROLLBACK_POINT_FILE="$STATE_DIR/rollback-point.json"`,
		"classify_rollback()",
		"read_schema_version()",
		"pre-rollback.dump",
		"git switch --detach",
		"wait_for_health()",
		`--max-time`,
		"transithub-api.next",
		"dist.next",
	} {
		requireTextContains(t, script, want)
	}
}

func TestProductionComposeUsesCurrentVersionAndRequiredCredentials(t *testing.T) {
	compose := readProjectFileForUpgradeTest(t, "deploy/docker-compose.prod.yml")
	for _, required := range []string{
		"image: deviseo/transithub:v2.3.7",
		"${DATABASE_URL:?",
		"${POSTGRES_PASSWORD:?",
		"${ADMIN_EMAIL:?",
		"${ADMIN_PASSWORD:?",
	} {
		requireTextContains(t, compose, required)
	}
	for _, forbidden := range []string{"v2.1.17", "admin@example.com", "change-this-"} {
		requireTextNotContains(t, compose, forbidden)
	}
}

func TestRollbackHealthProbeTreatsConnectionRefusalAsExpectedControlFlow(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	waitSection := script[strings.Index(script, "wait_for_health() {"):strings.Index(script, "\n}\n\ncleanup()")]
	requireTextContains(t, waitSection, `if health_response="$(curl -fsS --max-time 2 "$HEALTH_URL" 2>&1)"; then`)
	requireTextNotContains(t, waitSection, "set +e")
	requireTextNotContains(t, waitSection, "set -e")
}

func TestRollbackWaitForHealthRetriesConnectionFailureThenSucceeds(t *testing.T) {
	output, attempts, exitCode := runRollbackWaitForHealth(t, 2, false)
	if exitCode != 0 {
		t.Fatalf("wait_for_health exit code = %d, output=%s", exitCode, output)
	}
	if attempts != 3 {
		t.Fatalf("curl attempts = %d, want 3 after two connection failures", attempts)
	}
}

func TestRollbackWaitForHealthReturnsNonZeroAfterPersistentFailure(t *testing.T) {
	output, attempts, exitCode := runRollbackWaitForHealth(t, 0, true)
	if exitCode == 0 {
		t.Fatalf("persistent health failure unexpectedly succeeded: %s", output)
	}
	if attempts != 3 {
		t.Fatalf("curl attempts = %d, want bounded three attempts", attempts)
	}
}

func TestRollbackReportsBothOriginalAndRecoveryErrors(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	restoreStart := strings.Index(script, "restore_previous_artifacts() {")
	restoreEnd := strings.Index(script[restoreStart:], "\n}\n\non_error()")
	if restoreStart < 0 || restoreEnd < 0 {
		t.Fatal("unable to extract restore_previous_artifacts")
	}
	restoreSection := script[restoreStart : restoreStart+restoreEnd+2]
	requireTextContains(t, restoreSection, "local restore_error=''")
	requireTextContains(t, restoreSection, "恢复错误")
	requireTextContains(t, restoreSection, "systemctl restart")
	requireTextContains(t, restoreSection, "wait_for_health")
}

func TestRollbackReportsArtifactRestoreFailureEvenWhenHealthRecovers(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	restoreStart := strings.Index(script, "restore_previous_artifacts() {")
	restoreEnd := strings.Index(script[restoreStart:], "\n}\n\non_error()")
	if restoreStart < 0 || restoreEnd < 0 {
		t.Fatal("unable to extract restore_previous_artifacts")
	}
	restoreSection := script[restoreStart : restoreStart+restoreEnd+2]
	tempDir := t.TempDir()
	binaryPrevious := filepath.Join(tempDir, "transithub-api.previous")
	if err := os.WriteFile(binaryPrevious, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	distPrevious := filepath.Join(tempDir, "dist.previous")
	if err := os.Mkdir(distPrevious, 0o755); err != nil {
		t.Fatal(err)
	}
	distDir := filepath.Join(tempDir, "dist")
	if err := os.Mkdir(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mvPath := filepath.Join(tempDir, "mv")
	mvScript := fmt.Sprintf(`#!/usr/bin/env bash
if [[ "$2" == %q ]]; then
  exit 7
fi
exec /bin/mv "$@"
`, binaryPrevious)
	if err := os.WriteFile(mvPath, []byte(mvScript), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(tempDir, "restore.sh")
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=%q:$PATH
PROJECT_DIR=%q
SERVICE='transithub-api.service'
BINARY=%q
BINARY_PREVIOUS=%q
DIST_DIR=%q
DIST_PREVIOUS=%q
origin_commit=''
dist_backup_ready=1
binary_backup_ready=1
dist_switched=1
binary_switched=1
sudo() { return 0; }
wait_for_health() { return 0; }
%s
if restore_previous_artifacts; then
  exit 0
else
  exit $?
fi
`, tempDir, tempDir, filepath.Join(tempDir, "transithub-api"), binaryPrevious, distDir, distPrevious, restoreSection)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("bash", wrapperPath).CombinedOutput()
	if err == nil {
		t.Fatalf("artifact restore unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(string(output), "恢复错误") || !strings.Contains(string(output), "binary") {
		t.Fatalf("artifact restore error was not reported: %s", output)
	}
}

func TestRollbackRestoresFrontendWhenFailureOccursAfterBackup(t *testing.T) {
	distDir, distPrevious := runPartialArtifactRestore(t, false)
	assertFileContent(t, filepath.Join(distDir, "marker"), "old frontend")
	if _, err := os.Stat(distPrevious); !os.IsNotExist(err) {
		t.Fatalf("dist.previous still exists after restore: %v", err)
	}
}

func TestRollbackRestoresFrontendWhenBackendSwitchFails(t *testing.T) {
	distDir, distPrevious := runPartialArtifactRestore(t, true)
	assertFileContent(t, filepath.Join(distDir, "marker"), "old frontend")
	if _, err := os.Stat(distPrevious); !os.IsNotExist(err) {
		t.Fatalf("dist.previous still exists after restore: %v", err)
	}
}

func TestRollbackSignalHandlerRestoresPartialSwitch(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	start := strings.Index(script, "on_signal() {")
	endRelative := strings.Index(script[start:], "\n}\n\nacquire_maintenance_lock()")
	if start < 0 || endRelative < 0 {
		t.Fatal("unable to extract on_signal")
	}
	signalHandler := script[start : start+endRelative+2]
	requireTextContains(t, signalHandler, "restore_previous_artifacts")
}

func TestRollbackArmsRecoveryBeforeArtifactMutations(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	start := strings.Index(script, "# 前后端产物都构建成功后才落位")
	end := strings.Index(script[start:], "\nsudo systemctl restart")
	if start < 0 || end < 0 {
		t.Fatal("unable to extract artifact switch section")
	}
	section := script[start : start+end]
	for _, check := range []struct {
		flag     string
		mutation string
	}{
		{flag: "dist_backup_ready=1", mutation: `mv -f "$DIST_DIR" "$DIST_PREVIOUS"`},
		{flag: "binary_backup_ready=1", mutation: `mv -f "$BINARY_PREVIOUS_NEXT" "$BINARY_PREVIOUS"`},
		{flag: "dist_switched=1", mutation: `mv -f "$PROJECT_DIR/frontend/dist.next" "$DIST_DIR"`},
		{flag: "binary_switched=1", mutation: `mv -f "$PROJECT_DIR/transithub-api.next" "$BINARY"`},
	} {
		flagAt := strings.Index(section, check.flag)
		mutationAt := strings.Index(section, check.mutation)
		if flagAt < 0 || mutationAt < 0 || flagAt > mutationAt {
			t.Fatalf("%s must be set before %s", check.flag, check.mutation)
		}
	}
	copyAt := strings.Index(section, `cp -p "$BINARY" "$BINARY_PREVIOUS_NEXT"`)
	backupFlagAt := strings.Index(section, "binary_backup_ready=1")
	if copyAt < 0 || backupFlagAt < 0 || copyAt > backupFlagAt {
		t.Fatal("binary backup must be fully copied before its published-backup flag is armed")
	}
}

func TestRollbackDisarmsRecoveryBeforeDiscardingBackups(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	healthAt := strings.Index(script, `health_response="$(wait_for_health)"`)
	backupRemovalAt := strings.Index(script[healthAt:], `rm -rf "$DIST_PREVIOUS"`)
	if healthAt < 0 || backupRemovalAt < 0 {
		t.Fatal("unable to extract successful rollback finalization")
	}
	section := script[healthAt : healthAt+backupRemovalAt]
	for _, want := range []string{
		"recovery_armed=0",
		"origin_commit=''",
		"dist_backup_ready=0",
		"binary_backup_ready=0",
		"dist_switched=0",
		"binary_switched=0",
	} {
		requireTextContains(t, section, want)
	}
	for _, handlerName := range []string{"on_error() {", "on_signal() {"} {
		start := strings.Index(script, handlerName)
		end := strings.Index(script[start:], "\n}")
		if start < 0 || end < 0 {
			t.Fatalf("unable to extract %s", handlerName)
		}
		requireTextContains(t, script[start:start+end], "recovery_armed")
	}
}

func TestRollbackRestoresArtifactsWhenSignalArrivesImmediatelyAfterMutation(t *testing.T) {
	for _, stage := range []string{"dist-backup", "binary-backup", "dist-switch", "binary-switch"} {
		t.Run(stage, func(t *testing.T) {
			distDir, binary, distPrevious, binaryPrevious := runArtifactSignalAfterMutation(t, stage)
			assertFileContent(t, filepath.Join(distDir, "marker"), "old frontend")
			assertFileContent(t, binary, "old backend")
			if _, err := os.Stat(distPrevious); !os.IsNotExist(err) {
				t.Fatalf("dist.previous still exists after signal restore: %v", err)
			}
			if _, err := os.Stat(binaryPrevious); !os.IsNotExist(err) {
				t.Fatalf("binary.previous still exists after signal restore: %v", err)
			}
		})
	}
}

func TestRollbackIgnoresSecondTerminationSignalDuringRecovery(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	restoreStart := strings.Index(script, "restore_previous_artifacts() {")
	restoreEnd := strings.Index(script[restoreStart:], "\n}\n\non_error()")
	signalStart := strings.Index(script, "on_signal() {")
	signalEnd := strings.Index(script[signalStart:], "\n}\n\nacquire_maintenance_lock()")
	if restoreStart < 0 || restoreEnd < 0 || signalStart < 0 || signalEnd < 0 {
		t.Fatal("unable to extract rollback recovery functions")
	}
	restoreFunction := script[restoreStart : restoreStart+restoreEnd+2]
	signalFunction := script[signalStart : signalStart+signalEnd+2]

	tempDir := t.TempDir()
	distDir := filepath.Join(tempDir, "dist")
	distPrevious := filepath.Join(tempDir, "dist.previous")
	for path, marker := range map[string]string{distDir: "new frontend", distPrevious: "old frontend"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(tempDir, "transithub-api")
	binaryPrevious := filepath.Join(tempDir, "transithub-api.previous")
	if err := os.WriteFile(binary, []byte("new backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPrevious, []byte("old backend"), 0o755); err != nil {
		t.Fatal(err)
	}

	wrapperPath := filepath.Join(tempDir, "double-signal.sh")
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
PROJECT_DIR=%q
SERVICE='transithub-api.service'
BINARY=%q
BINARY_PREVIOUS=%q
DIST_DIR=%q
DIST_PREVIOUS=%q
origin_commit=''
recovery_armed=1
dist_backup_ready=1
binary_backup_ready=1
dist_switched=1
binary_switched=1
health_calls=0
sudo() { return 0; }
wait_for_health() {
  health_calls=$((health_calls + 1))
  if ((health_calls == 1)); then
    kill -INT "$$"
  fi
  return 0
}
%s
%s
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
kill -TERM "$$"
`, tempDir, binary, binaryPrevious, distDir, distPrevious, restoreFunction, signalFunction)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("bash", wrapperPath).CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 143 {
		t.Fatalf("double signal exit = %v, want 143, output=%s", err, output)
	}
	assertFileContent(t, filepath.Join(distDir, "marker"), "old frontend")
	assertFileContent(t, binary, "old backend")
}

func runArtifactSignalAfterMutation(t *testing.T, stage string) (string, string, string, string) {
	t.Helper()
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	restoreStart := strings.Index(script, "restore_previous_artifacts() {")
	restoreEnd := strings.Index(script[restoreStart:], "\n}\n\non_error()")
	signalStart := strings.Index(script, "on_signal() {")
	signalEnd := strings.Index(script[signalStart:], "\n}\n\nacquire_maintenance_lock()")
	switchStart := strings.Index(script, "# 前后端产物都构建成功后才落位")
	switchEnd := strings.Index(script[switchStart:], "\nsudo systemctl restart")
	if restoreStart < 0 || restoreEnd < 0 || signalStart < 0 || signalEnd < 0 || switchStart < 0 || switchEnd < 0 {
		t.Fatal("unable to extract rollback signal test sections")
	}
	restoreFunction := script[restoreStart : restoreStart+restoreEnd+2]
	signalFunction := script[signalStart : signalStart+signalEnd+2]
	switchSection := script[switchStart : switchStart+switchEnd]

	tempDir := t.TempDir()
	distDir := filepath.Join(tempDir, "frontend", "dist")
	distNext := filepath.Join(tempDir, "frontend", "dist.next")
	distPrevious := filepath.Join(tempDir, "frontend", "dist.previous")
	for path, marker := range map[string]string{distDir: "old frontend", distNext: "new frontend"} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(tempDir, "transithub-api")
	binaryNext := filepath.Join(tempDir, "transithub-api.next")
	binaryPrevious := filepath.Join(tempDir, "transithub-api.previous")
	if err := os.WriteFile(binary, []byte("old backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryNext, []byte("new backend"), 0o755); err != nil {
		t.Fatal(err)
	}

	wrapperPath := filepath.Join(tempDir, "signal-window.sh")
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
PROJECT_DIR=%q
SERVICE='transithub-api.service'
BINARY=%q
BINARY_PREVIOUS=%q
BINARY_PREVIOUS_NEXT=%q
DIST_DIR=%q
DIST_PREVIOUS=%q
origin_commit=''
recovery_armed=1
dist_backup_ready=0
binary_backup_ready=0
dist_switched=0
binary_switched=0
signal_after=%q
sudo() { return 0; }
wait_for_health() { return 0; }
mv() {
  /bin/mv "$@"
  if [[ "$signal_after" == 'dist-backup' && "$2" == "$DIST_DIR" && "$3" == "$DIST_PREVIOUS" ]] ||
     [[ "$signal_after" == 'binary-backup' && "$2" == "$BINARY_PREVIOUS_NEXT" && "$3" == "$BINARY_PREVIOUS" ]] ||
     [[ "$signal_after" == 'dist-switch' && "$2" == "$PROJECT_DIR/frontend/dist.next" && "$3" == "$DIST_DIR" ]] ||
     [[ "$signal_after" == 'binary-switch' && "$2" == "$PROJECT_DIR/transithub-api.next" && "$3" == "$BINARY" ]]; then
    signal_after=''
    kill -TERM "$$"
  fi
}
%s
%s
trap 'on_signal TERM 143' TERM
%s
`, tempDir, binary, binaryPrevious, binaryPrevious+".next", distDir, distPrevious, stage, restoreFunction, signalFunction, switchSection)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("bash", wrapperPath).CombinedOutput()
	if err == nil {
		t.Fatalf("signal window unexpectedly completed: %s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 143 {
		t.Fatalf("signal window exit = %v, output=%s", err, output)
	}
	return distDir, binary, distPrevious, binaryPrevious
}

func runPartialArtifactRestore(t *testing.T, frontendSwitched bool) (string, string) {
	t.Helper()
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	start := strings.Index(script, "restore_previous_artifacts() {")
	endRelative := strings.Index(script[start:], "\n}\n\non_error()")
	if start < 0 || endRelative < 0 {
		t.Fatal("unable to extract restore_previous_artifacts")
	}
	restoreFunction := script[start : start+endRelative+2]

	tempDir := t.TempDir()
	distDir := filepath.Join(tempDir, "dist")
	distPrevious := filepath.Join(tempDir, "dist.previous")
	if err := os.Mkdir(distPrevious, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distPrevious, "marker"), []byte("old frontend"), 0o644); err != nil {
		t.Fatal(err)
	}
	if frontendSwitched {
		if err := os.Mkdir(distDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(distDir, "marker"), []byte("new frontend"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wrapperPath := filepath.Join(tempDir, "restore.sh")
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
PROJECT_DIR=%q
SERVICE='transithub-api.service'
BINARY=%q
BINARY_PREVIOUS=%q
DIST_DIR=%q
DIST_PREVIOUS=%q
origin_commit=''
dist_backup_ready=1
dist_switched=%d
binary_backup_ready=0
binary_switched=0
sudo() { return 0; }
wait_for_health() { return 0; }
%s
restore_previous_artifacts
`, tempDir, filepath.Join(tempDir, "transithub-api"), filepath.Join(tempDir, "transithub-api.previous"), distDir, distPrevious, boolInt(frontendSwitched), restoreFunction)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", wrapperPath).CombinedOutput(); err != nil {
		t.Fatalf("partial artifact restore failed: %v output=%s", err, output)
	}
	return distDir, distPrevious
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("%s content = %q, want %q", path, content, want)
	}
}

func runRollbackWaitForHealth(t *testing.T, succeedAfter int, alwaysFail bool) (string, int, int) {
	t.Helper()
	tempDir := t.TempDir()
	script := readProjectFileForUpgradeTest(t, "deploy/rollback-source.sh")
	start := strings.Index(script, "wait_for_health() {")
	endRelative := strings.Index(script[start:], "\n}\n\ncleanup()")
	if start < 0 || endRelative < 0 {
		t.Fatal("unable to extract wait_for_health")
	}
	waitFunction := script[start : start+endRelative+2]
	statePath := filepath.Join(tempDir, "curl-count")
	if err := os.WriteFile(statePath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	curlPath := filepath.Join(tempDir, "curl")
	curlScript := fmt.Sprintf(`#!/usr/bin/env bash
set -eu
state=%q
count=$(cat "$state")
count=$((count + 1))
printf '%%s\n' "$count" >"$state"
if %t; then
  exit 7
fi
if ((count <= %d)); then
  exit 7
fi
printf '{"status":"ok"}\n'
`, statePath, alwaysFail, succeedAfter)
	if err := os.WriteFile(curlPath, []byte(curlScript), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(tempDir, "wait.sh")
	wrapper := fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=%q:$PATH
HEALTH_URL='http://127.0.0.1:1/health'
HEALTH_TIMEOUT_SECONDS=3
sleep() { SECONDS=$((SECONDS + 1)); }
%s
if wait_for_health; then
  exit 0
else
  code=$?
  exit "$code"
fi
`, tempDir, waitFunction)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", wrapperPath)
	result, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	countBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	if _, err := fmt.Sscanf(string(countBytes), "%d", &attempts); err != nil {
		t.Fatal(err)
	}
	return string(result), attempts, exitCode
}

func TestQuestionAnswersMigrationDeclaresAdditiveRollbackSafety(t *testing.T) {
	migration := readProjectFileForUpgradeTest(t,
		"backend/internal/database/migrations/000025_connection_health_question_answers.sql")
	requireTextContains(t, migration,
		"-- rollback-safe: additive（仅新增表、列或索引，旧代码可忽略多出的结构）")
}

// 还原点必须记录升级前的 schema 版本，回滚判定依赖它比对档位。
func TestUpgradeCapturesRollbackPointFields(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/update-source.sh")
	for _, want := range []string{
		"rollback-point.json",
		"read_schema_version()",
		"previous_commit=",
		"previous_schema_version=",
	} {
		requireTextContains(t, script, want)
	}
}

// schema_migrations.version 是 TEXT，存完整迁移文件名；直接对它取数值 MAX
// 会让回滚判定必然失败，因此两个脚本都必须先取字符串 MAX 再截数字前缀。
func TestSchemaVersionReadHandlesTextColumn(t *testing.T) {
	for _, relativePath := range []string{
		"deploy/update-source.sh",
		"deploy/rollback-source.sh",
	} {
		script := readProjectFileForUpgradeTest(t, relativePath)
		requireTextContains(t, script, `COALESCE(MAX(version), '000000')`)
		requireTextContains(t, script, `raw="${raw%%_*}"`)
		requireTextNotContains(t, script, "COALESCE(MAX(version), 0)")
	}
}

package system

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 升级、回滚服务由 systemd 以固定 PATH 拉起，不加载 nvm 等登录环境。
// 必需命令要在写还原点、切换代码之前检查，缺失时不能留下任何改动。
func TestMaintenanceScriptsCheckRequiredCommandsBeforeAnyChange(t *testing.T) {
	const preflight = "\nrequire_commands git sudo docker curl node npm go\n"
	for relativePath, firstChanges := range map[string][]string{
		"deploy/update-source.sh": {
			`capture_rollback_point "$previous_commit"`,
			"git switch --detach origin/main",
			"npm ci --registry",
		},
		"deploy/rollback-source.sh": {
			"\ndump_current_database\n",
			`git switch --detach "$target_commit"`,
			"npm ci --registry",
		},
	} {
		script := readProjectFileForUpgradeTest(t, relativePath)
		lockAt := strings.Index(script, "\nacquire_maintenance_lock\n")
		preflightAt := strings.Index(script, preflight)
		if lockAt < 0 || preflightAt < 0 || preflightAt < lockAt {
			t.Fatalf("%s 必须在持锁后检查必需命令：lock=%d preflight=%d", relativePath, lockAt, preflightAt)
		}
		for _, change := range firstChanges {
			changeAt := strings.Index(script, change)
			if changeAt < 0 {
				t.Fatalf("%s 未找到 %q", relativePath, change)
			}
			if preflightAt > changeAt {
				t.Fatalf("%s 的命令检查 %d 晚于 %q %d", relativePath, preflightAt, change, changeAt)
			}
		}
	}
}

func TestRequireCommandsReportsMissingNodeToolchain(t *testing.T) {
	for _, relativePath := range []string{"deploy/update-source.sh", "deploy/rollback-source.sh"} {
		function := extractShellFunction(t, relativePath, "require_commands")
		binDir := t.TempDir()
		writeExecutable(t, filepath.Join(binDir, "git"), "#!/bin/sh\nexit 0\n")

		output, exitCode := runShellSnippet(t, binDir, function+"\nrequire_commands git node npm\necho passed\n")
		if exitCode != 69 {
			t.Fatalf("%s 缺少 node/npm 时退出码应为 69，实际 %d：%s", relativePath, exitCode, output)
		}
		for _, want := range []string{"node npm", binDir, "/usr/local/bin/npm", "尚未改动代码和数据库"} {
			if !strings.Contains(output, want) {
				t.Fatalf("%s 缺命令提示应包含 %q：%s", relativePath, want, output)
			}
		}
		if strings.Contains(output, "passed") {
			t.Fatalf("%s 缺命令后不应继续执行：%s", relativePath, output)
		}

		writeExecutable(t, filepath.Join(binDir, "node"), "#!/bin/sh\nexit 0\n")
		writeExecutable(t, filepath.Join(binDir, "npm"), "#!/bin/sh\nexit 0\n")
		output, exitCode = runShellSnippet(t, binDir, function+"\nrequire_commands git node npm\necho passed\n")
		if exitCode != 0 || !strings.Contains(output, "passed") {
			t.Fatalf("%s 命令齐全时应继续执行，退出码 %d：%s", relativePath, exitCode, output)
		}
	}
}

// 切换源码后、改写前端 dist 前失败，线上仍是旧版，源码要退回原提交；
// 否则下次重试会把未验证的新提交写成还原点。改写产物后则保持不动，交给回滚。
func TestUpgradeRestoresSourceOnlyBeforeArtifactsChange(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, "deploy/update-source.sh")
	requireTextContains(t, script, "source_switched=1\ngit switch --detach origin/main\n")
	npmCIAt := strings.Index(script, "npm ci --registry")
	touchedAt := strings.Index(script, "\nartifacts_touched=1\n")
	buildAt := strings.Index(script, "\nnpm run build\n")
	if npmCIAt < 0 || touchedAt < npmCIAt || buildAt < touchedAt {
		t.Fatalf("artifacts_touched 必须位于 npm ci 与 npm run build 之间：ci=%d touched=%d build=%d", npmCIAt, touchedAt, buildAt)
	}
	for _, handler := range []string{"on_error", "on_signal"} {
		requireTextContains(t, extractShellFunction(t, "deploy/update-source.sh", handler), "restore_source_before_artifacts")
	}

	function := extractShellFunction(t, "deploy/update-source.sh", "restore_source_before_artifacts")
	for _, tc := range []struct {
		name              string
		artifactsTouched  string
		expectOldHead     bool
		expectOutputMatch string
	}{
		{name: "改写产物前失败", artifactsTouched: "0", expectOldHead: true, expectOutputMatch: "已把源码退回升级前的提交"},
		{name: "改写产物后失败", artifactsTouched: "1", expectOldHead: false},
	} {
		repoDir, oldCommit, newCommit := initTwoCommitRepository(t)
		snippet := "PROJECT_DIR=" + shellQuote(repoDir) + "\n" +
			"previous_commit=" + shellQuote(oldCommit) + "\n" +
			"source_switched=1\n" +
			"artifacts_touched=" + tc.artifactsTouched + "\n" +
			function + "\nrestore_source_before_artifacts\n"
		output, exitCode := runShellSnippet(t, "", snippet)
		if exitCode != 0 {
			t.Fatalf("%s：退出码 %d：%s", tc.name, exitCode, output)
		}
		head := gitOutput(t, repoDir, "rev-parse", "HEAD")
		want := newCommit
		if tc.expectOldHead {
			want = oldCommit
		}
		if head != want {
			t.Fatalf("%s：HEAD 应为 %s，实际 %s；输出：%s", tc.name, want, head, output)
		}
		if tc.expectOutputMatch != "" && !strings.Contains(output, tc.expectOutputMatch) {
			t.Fatalf("%s：输出应包含 %q：%s", tc.name, tc.expectOutputMatch, output)
		}
	}
}

// ERR trap 不受 set +e 约束：升级脚本失败时 wrapper 曾额外报一行「wrapper 第 61 行：tee」。
func TestUpgradeWrapperReportsOnlyUpgradeScriptFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stubExit   int
		wantState  string
		wantStatus string
	}{
		{name: "失败", stubExit: 127, wantState: `"state":"failed"`, wantStatus: `"exitCode":127`},
		{name: "成功", stubExit: 0, wantState: `"state":"succeeded"`, wantStatus: `"exitCode":0`},
	} {
		stateDir := t.TempDir()
		stub := filepath.Join(t.TempDir(), "update-source.sh")
		writeExecutable(t, stub, "#!/usr/bin/env bash\necho 'line 237: npm: command not found' >&2\nexit "+strconv.Itoa(tc.stubExit)+"\n")
		wrapper := rewriteWrapper(t, "deploy/run-source-upgrade.sh", map[string]string{
			"UPGRADE_SCRIPT='/opt/transithub/update-source.sh'": "UPGRADE_SCRIPT=" + shellQuote(stub),
			"STATE_DIR='/var/lib/transithub'":                   "STATE_DIR=" + shellQuote(stateDir),
		})

		output, exitCode := runScript(t, wrapper)
		if exitCode != tc.stubExit {
			t.Fatalf("%s：wrapper 退出码应为 %d，实际 %d：%s", tc.name, tc.stubExit, exitCode, output)
		}
		log := readTestFile(t, filepath.Join(stateDir, "upgrade.log"))
		if !strings.Contains(log, "npm: command not found") {
			t.Fatalf("%s：日志应保留升级脚本输出：%s", tc.name, log)
		}
		if strings.Contains(log, "wrapper") || strings.Contains(output, "wrapper") {
			t.Fatalf("%s：不应出现 wrapper 自身的误报：%s", tc.name, log)
		}
		status := readTestFile(t, filepath.Join(stateDir, "upgrade-status.json"))
		if !strings.Contains(status, tc.wantState) || !strings.Contains(status, tc.wantStatus) {
			t.Fatalf("%s：状态文件不符：%s", tc.name, status)
		}
	}
}

// 回滚 wrapper 按退出码给出可读原因；ERR trap 曾让这段映射永远执行不到。
func TestRollbackWrapperReportsReadableReasonForExitCode(t *testing.T) {
	for _, tc := range []struct {
		exitCode int
		want     string
	}{
		{exitCode: 66, want: "当前没有可用还原点"},
		{exitCode: 69, want: "回滚服务找不到必需的命令"},
	} {
		stateDir := t.TempDir()
		stub := filepath.Join(t.TempDir(), "rollback-source.sh")
		writeExecutable(t, stub, "#!/usr/bin/env bash\nexit "+strconv.Itoa(tc.exitCode)+"\n")
		wrapper := rewriteWrapper(t, "deploy/run-source-rollback.sh", map[string]string{
			"ROLLBACK_SCRIPT='/opt/transithub/rollback-source.sh'": "ROLLBACK_SCRIPT=" + shellQuote(stub),
			"STATE_DIR='/var/lib/transithub'":                      "STATE_DIR=" + shellQuote(stateDir),
		})

		output, exitCode := runScript(t, wrapper)
		if exitCode != tc.exitCode {
			t.Fatalf("退出码应为 %d，实际 %d：%s", tc.exitCode, exitCode, output)
		}
		log := readTestFile(t, filepath.Join(stateDir, "rollback.log"))
		if !strings.Contains(log, tc.want) {
			t.Fatalf("退出码 %d 的日志应包含 %q：%s", tc.exitCode, tc.want, log)
		}
		if strings.Contains(log, "版本回滚失败（第") {
			t.Fatalf("退出码 %d 不应落入 wrapper 的通用 ERR 分支：%s", tc.exitCode, log)
		}
		status := readTestFile(t, filepath.Join(stateDir, "rollback-status.json"))
		if !strings.Contains(status, `"state":"failed"`) || !strings.Contains(status, `"exitCode":`+strconv.Itoa(tc.exitCode)) {
			t.Fatalf("状态文件不符：%s", status)
		}
	}
}

func extractShellFunction(t *testing.T, relativePath, name string) string {
	t.Helper()
	script := readProjectFileForUpgradeTest(t, relativePath)
	start := strings.Index(script, "\n"+name+"() {\n")
	if start < 0 {
		t.Fatalf("%s 中未找到函数 %s", relativePath, name)
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s 中函数 %s 没有结束行", relativePath, name)
	}
	return script[start+1 : start+end+3]
}

func rewriteWrapper(t *testing.T, relativePath string, replacements map[string]string) string {
	t.Helper()
	script := readProjectFileForUpgradeTest(t, relativePath)
	for old, replacement := range replacements {
		if strings.Count(script, old) != 1 {
			t.Fatalf("%s 中 %q 应恰好出现一次", relativePath, old)
		}
		script = strings.Replace(script, old, replacement, 1)
	}
	path := filepath.Join(t.TempDir(), filepath.Base(relativePath))
	writeExecutable(t, path, script)
	return path
}

// runShellSnippet 以 set -Eeuo pipefail 执行片段；binDir 非空时 PATH 只含该目录。
func runShellSnippet(t *testing.T, binDir, body string) (string, int) {
	t.Helper()
	header := "set -Eeuo pipefail\n"
	if binDir != "" {
		header += "PATH=" + shellQuote(binDir) + "\n"
	}
	path := filepath.Join(t.TempDir(), "snippet.sh")
	writeExecutable(t, path, "#!/usr/bin/env bash\n"+header+body)
	return runScript(t, path)
}

func runScript(t *testing.T, path string) (string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash 不可用")
	}
	output, err := exec.Command(bash, path).CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("执行 %s 失败：%v", path, err)
	}
	return string(output), exitErr.ExitCode()
}

func initTwoCommitRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := t.TempDir()
	gitOutput(t, repoDir, "init", "-q")
	gitOutput(t, repoDir, "commit", "-q", "--allow-empty", "-m", "old")
	oldCommit := gitOutput(t, repoDir, "rev-parse", "HEAD")
	gitOutput(t, repoDir, "commit", "-q", "--allow-empty", "-m", "new")
	newCommit := gitOutput(t, repoDir, "rev-parse", "HEAD")
	gitOutput(t, repoDir, "switch", "-q", "--detach", newCommit)
	return repoDir, oldCommit, newCommit
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{
		"-C", dir,
		"-c", "user.name=transithub-test",
		"-c", "user.email=transithub-test@example.invalid",
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
	}
	output, err := exec.Command("git", append(base, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败：%v：%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return string(payload)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

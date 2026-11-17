package system

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func chanshuiFunctionBlock(t *testing.T, relative string) string {
	t.Helper()
	script := readProjectFileForUpgradeTest(t, relative)
	const startMarker = "# BEGIN GENERATED CHANSHUI BUILD FUNCTIONS\n"
	const endMarker = "# END GENERATED CHANSHUI BUILD FUNCTIONS\n"
	start := strings.Index(script, startMarker)
	if start < 0 {
		t.Fatalf("%s missing generated functions", relative)
	}
	start += len(startMarker)
	end := strings.Index(script[start:], endMarker)
	if end < 0 {
		t.Fatalf("%s missing generated functions end", relative)
	}
	return script[start : start+end]
}
func TestChanshuiMaintenanceFunctionCopiesMatchCanonicalSource(t *testing.T) {
	source := readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh")
	for _, relative := range []string{"deploy/update-source.sh", "deploy/rollback-source.sh"} {
		if actual := chanshuiFunctionBlock(t, relative); actual != source {
			t.Fatalf("%s generated functions drifted from deploy/chanshui-build.sh", relative)
		}
		script := readProjectFileForUpgradeTest(t, relative)
		main := script[strings.Index(script, "\nacquire_maintenance_lock\n"):]
		if strings.Index(main, "chanshui_acquire_build_lock") > strings.Index(main, "git switch --detach") || !strings.Contains(main, "npm --ignore-scripts run build") {
			t.Fatalf("%s does not hold build lock before switching or can recurse into prebuild", relative)
		}
		if regexp.MustCompile(`(?m)^[ \t]*(source|\.)[ \t]+["'$./a-zA-Z_]`).MatchString(script) {
			t.Fatalf("%s depends on helper surviving source checkout", relative)
		}
	}
}
func makeDetectionArtifact(t *testing.T, path, value string, executable bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0600)
	if executable {
		mode = 0700
	}
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}
func TestChanshuiPairRestoresEveryPublicationFailureAndSignalWindow(t *testing.T) {
	for _, mode := range []string{"fail-before", "signal-after"} {
		for _, stage := range []string{"binary-backup", "web-backup", "binary-switch", "web-switch"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
				web := filepath.Join(root, "chanshui/web/out-build")
				next := filepath.Join(root, "chanshui/.artifacts/build.next")
				previous := filepath.Join(root, "chanshui/.artifacts/build.previous")
				makeDetectionArtifact(t, binary, "old-cli", true)
				makeDetectionArtifact(t, filepath.Join(web, "index.html"), "old-web", false)
				makeDetectionArtifact(t, filepath.Join(next, "transithub-chanshui"), "new-cli", true)
				makeDetectionArtifact(t, filepath.Join(next, "web/index.html"), "new-web", false)
				database := filepath.Join(root, "data/chanshui/hub.sqlite")
				makeDetectionArtifact(t, database, "reports-stay", false)
				src, dst := "", ""
				switch stage {
				case "binary-backup":
					src, dst = binary, filepath.Join(previous, "transithub-chanshui")
				case "web-backup":
					src, dst = web, filepath.Join(previous, "web")
				case "binary-switch":
					src, dst = filepath.Join(next, "transithub-chanshui"), binary
				case "web-switch":
					src, dst = filepath.Join(next, "web"), web
				}
				tools := filepath.Join(root, "tools")
				if err := os.Mkdir(tools, 0700); err != nil {
					t.Fatal(err)
				}
				action := "printf 'injected publish failure\\n' >&2; exit 7"
				if mode == "signal-after" {
					action = "/bin/mv \"$@\"\nkill -TERM \"$PPID\"\nexit 0"
				}
				writeExecutable(t, filepath.Join(tools, "mv"), "#!/usr/bin/env bash\nif [[ \"$2\" == "+shellQuote(src)+" && \"$3\" == "+shellQuote(dst)+" ]]; then\n"+action+"\nfi\nexec /bin/mv \"$@\"\n")
				body := readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh") + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH
chanshui_pair_root=%s
chanshui_pair_stage=%s
chanshui_pair_backup=%s
chanshui_stage_ready=1
chanshui_stage_created=1
failed(){ local code=$?; trap - ERR; trap '' INT TERM HUP; chanshui_restore_pair || exit 71; exit "$code"; }
signalled(){ trap - ERR; trap '' INT TERM HUP; chanshui_restore_pair || exit 71; exit 143; }
trap failed ERR
trap signalled TERM
trap 'chanshui_cleanup_stage || true' EXIT
chanshui_acquire_build_lock "$chanshui_pair_root"
chanshui_publish_pair
`, shellQuote(tools), shellQuote(root), shellQuote(next), shellQuote(previous))
				output, code := runShellSnippet(t, "", body)
				want := 7
				if mode == "signal-after" {
					want = 143
				}
				if code != want {
					t.Fatalf("publication failure=%d want=%d: %s", code, want, output)
				}
				assertFileContent(t, binary, "old-cli")
				assertFileContent(t, filepath.Join(web, "index.html"), "old-web")
				assertFileContent(t, database, "reports-stay")
				if _, err := os.Stat(previous); !os.IsNotExist(err) {
					t.Fatalf("successfully restored backup still present: %v", err)
				}
			})
		}
	}
}
func TestChanshuiBuildLockRejectsConcurrentPublication(t *testing.T) {
	root := t.TempDir()
	source := readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh")
	// A distinct child shell opens the same lock independently, like npm prebuild
	// racing an upgrade; private functions do not recursively take this lock.
	child := filepath.Join(root, "child.sh")
	writeExecutable(t, child, "#!/usr/bin/env bash\nset -Eeuo pipefail\n"+source+"\nchanshui_acquire_build_lock "+shellQuote(root)+"\n")
	body := source + "\nchanshui_acquire_build_lock " + shellQuote(root) + "\nif bash " + shellQuote(child) + "; then exit 99; else status=$?; fi\n[[ $status == 75 ]]\n"
	output, code := runShellSnippet(t, "", body)
	if code != 0 || !strings.Contains(output, "本次未替换检测产物") {
		t.Fatalf("concurrent builder not refused: %d %s", code, output)
	}
}
func TestChanshuiBuildRefusesToDiscardUnrecoveredBackup(t *testing.T) {
	root := t.TempDir()
	makeDetectionArtifact(t, filepath.Join(root, "chanshui/cmd/transit-hub/main.go"), "fixture", false)
	backup := filepath.Join(root, "chanshui/.artifacts/build.previous/transithub-chanshui")
	makeDetectionArtifact(t, backup, "only-good-copy", true)
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go", "node", "npm"} {
		writeExecutable(t, filepath.Join(tools, name), "#!/bin/sh\nexit 0\n")
	}
	body := readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh") + "\nexport PATH=" + shellQuote(tools) + ":$PATH\nfailed(){ local code=$?; trap - ERR; chanshui_restore_pair; exit \"$code\"; };trap failed ERR\nchanshui_build_stage " + shellQuote(root) + " " + shellQuote(filepath.Join(root, "chanshui/.artifacts/build.next")) + "\n"
	output, code := runShellSnippet(t, "", body)
	if code != 65 || !strings.Contains(output, "不会覆盖") {
		t.Fatalf("unrecovered backup not protected: %d %s", code, output)
	}
	assertFileContent(t, backup, "only-good-copy")
}
func TestChanshuiSelfContainedFunctionsBuildOldAndMissingTargetsThenUpgradeAgain(t *testing.T) {
	root := t.TempDir()
	sourceMain := filepath.Join(root, "chanshui/cmd/transit-hub/main.go")
	makeDetectionArtifact(t, sourceMain, "fixture", false)
	if err := os.MkdirAll(filepath.Join(root, "chanshui/web"), 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "data/chanshui/hub.sqlite")
	makeDetectionArtifact(t, database, "reports-stay", false)
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(tools, "node"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(tools, "go"), `#!/usr/bin/env bash
set -e
for arg in "$@"; do if [[ $arg == run ]]; then printf '{"fixture":true}\n';exit 0;fi;done
previous=''
for arg in "$@";do if [[ $previous == -o ]];then printf '%s' "$(cat "$CS_TEST_ROOT/candidate")" >"$arg";chmod 0755 "$arg";fi;previous=$arg;done
`)
	writeExecutable(t, filepath.Join(tools, "npm"), `#!/usr/bin/env bash
set -e
for arg in "$@";do if [[ $arg == ci ]];then exit 0;fi;done
mkdir -p out/_next
cat "$CS_TEST_ROOT/candidate" >out/index.html
`)
	stage := filepath.Join(root, "chanshui/.artifacts/build.next")
	// Use the actual installed-updater copy, not a source statement or a helper
	// file; the target working tree deliberately has no deploy helper at all.
	body := chanshuiFunctionBlock(t, "deploy/rollback-source.sh") + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH CS_TEST_ROOT=%s
chanshui_acquire_build_lock %s
printf old >"$CS_TEST_ROOT/candidate"
chanshui_build_stage "$CS_TEST_ROOT" %s
chanshui_publish_pair
chanshui_commit_pair
chanshui_cleanup_stage
mv %s %s
chanshui_build_stage "$CS_TEST_ROOT" %s
chanshui_publish_pair
chanshui_commit_pair
chanshui_cleanup_stage
[[ "$(cat "$CS_TEST_ROOT/chanshui/.artifacts/bin/transithub-chanshui")" == old ]]
mv %s %s
printf new >"$CS_TEST_ROOT/candidate"
chanshui_build_stage "$CS_TEST_ROOT" %s
chanshui_publish_pair
chanshui_commit_pair
chanshui_cleanup_stage
`, shellQuote(tools), shellQuote(root), shellQuote(root), shellQuote(stage), shellQuote(sourceMain), shellQuote(sourceMain+".hidden"), shellQuote(stage), shellQuote(sourceMain+".hidden"), shellQuote(sourceMain), shellQuote(stage))
	output, code := runShellSnippet(t, "", body)
	if code != 0 {
		t.Fatalf("historical/absent/re-upgrade lifecycle failed: %d %s", code, output)
	}
	assertFileContent(t, filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui"), "new")
	assertFileContent(t, filepath.Join(root, "chanshui/web/out-build/index.html"), "new")
	assertFileContent(t, database, "reports-stay")
}
func TestChanshuiMissingToolsNamesCauseBeforePublication(t *testing.T) {
	tools := t.TempDir()
	writeExecutable(t, filepath.Join(tools, "go"), "#!/bin/sh\nexit 0\n")
	output, code := runShellSnippet(t, tools, readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh")+"\nchanshui_require_commands go node npm python3\n")
	if code != 69 || !strings.Contains(output, "node") || !strings.Contains(output, "npm") || !strings.Contains(output, "python3") || !strings.Contains(output, "尚未替换") {
		t.Fatalf("missing tools not explained before mutation: %d %s", code, output)
	}
}
func TestChanshuiFailedFirstCandidateCanBuildDifferentCandidateOnRetry(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "chanshui/cmd/transit-hub/main.go")
	makeDetectionArtifact(t, main, "fixture", false)
	if err := os.MkdirAll(filepath.Join(root, "chanshui/web"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	web := filepath.Join(root, "chanshui/web/out-build/index.html")
	database := filepath.Join(root, "data/chanshui/hub.sqlite")
	makeDetectionArtifact(t, binary, "old-cli", true)
	makeDetectionArtifact(t, web, "old-web", false)
	makeDetectionArtifact(t, database, "reports-stay", false)
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(tools, "node"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(tools, "go"), `#!/usr/bin/env bash
set -e
if [[ $(cat "$CS_TEST_ROOT/candidate") == fail ]];then printf 'fixture compiler failure\n' >&2;exit 37;fi
for arg in "$@";do if [[ $arg == run ]];then printf '{"fixture":true}\n';exit 0;fi;done
previous=''
for arg in "$@";do if [[ $previous == -o ]];then printf new-cli >"$arg";chmod 0755 "$arg";fi;previous=$arg;done
`)
	writeExecutable(t, filepath.Join(tools, "npm"), `#!/usr/bin/env bash
set -e
for arg in "$@";do if [[ $arg == ci ]];then exit 0;fi;done
mkdir -p out/_next
printf new-web >out/index.html
`)
	body := chanshuiFunctionBlock(t, "deploy/update-source.sh") + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH CS_TEST_ROOT=%s
failed(){ local code=$?; trap - ERR; chanshui_restore_pair || true;exit "$code"; }
trap failed ERR
trap 'chanshui_cleanup_stage || true' EXIT
chanshui_acquire_build_lock "$CS_TEST_ROOT"
chanshui_build_stage "$CS_TEST_ROOT" "$CS_TEST_ROOT/chanshui/.artifacts/build.next"
chanshui_publish_pair
chanshui_commit_pair
`, shellQuote(tools), shellQuote(root))
	makeDetectionArtifact(t, filepath.Join(root, "candidate"), "fail", false)
	output, code := runShellSnippet(t, "", body)
	if code != 37 || !strings.Contains(output, "fixture compiler failure") {
		t.Fatalf("first build failure not preserved: %d %s", code, output)
	}
	assertFileContent(t, binary, "old-cli")
	assertFileContent(t, web, "old-web")
	for _, relative := range []string{"chanshui/.artifacts/build.next", "chanshui/.artifacts/build.previous", "chanshui-build.sh"} {
		if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
			t.Fatalf("failed candidate left mutable runtime dependency/temporary: %s %v", relative, err)
		}
	}
	makeDetectionArtifact(t, filepath.Join(root, "candidate"), "next", false)
	output, code = runShellSnippet(t, "", body)
	if code != 0 {
		t.Fatalf("different candidate retry failed: %d %s", code, output)
	}
	assertFileContent(t, binary, "new-cli")
	assertFileContent(t, web, "new-web")
	assertFileContent(t, database, "reports-stay")
}
func TestChanshuiPublicationAndRestoreFailuresKeepOriginalCauseAndBackup(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
	web := filepath.Join(root, "chanshui/web/out-build")
	stage := filepath.Join(root, "chanshui/.artifacts/build.next")
	backup := filepath.Join(root, "chanshui/.artifacts/build.previous")
	makeDetectionArtifact(t, binary, "old-cli", true)
	makeDetectionArtifact(t, filepath.Join(web, "index.html"), "old-web", false)
	makeDetectionArtifact(t, filepath.Join(stage, "transithub-chanshui"), "new-cli", true)
	makeDetectionArtifact(t, filepath.Join(stage, "web/index.html"), "new-web", false)
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/usr/bin/env bash\nif [[ \"$2\" == " + shellQuote(filepath.Join(stage, "web")) + " ]];then printf 'fixture publish denied\\n' >&2;exit 7;fi\nif [[ \"$2\" == " + shellQuote(filepath.Join(backup, "transithub-chanshui")) + " ]];then printf 'fixture restore denied\\n' >&2;exit 9;fi\nexec /bin/mv \"$@\"\n"
	writeExecutable(t, filepath.Join(tools, "mv"), script)
	body := readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh") + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH
chanshui_pair_root=%s
chanshui_pair_stage=%s
chanshui_pair_backup=%s
chanshui_stage_ready=1
chanshui_stage_created=1
failed(){ local code=$?; trap - ERR;chanshui_restore_pair || true;exit "$code"; }
trap failed ERR
chanshui_acquire_build_lock "$chanshui_pair_root"
chanshui_publish_pair
`, shellQuote(tools), shellQuote(root), shellQuote(stage), shellQuote(backup))
	output, code := runShellSnippet(t, "", body)
	if code != 7 || !strings.Contains(output, "fixture publish denied") || !strings.Contains(output, "fixture restore denied") || !strings.Contains(output, "检测产物恢复失败") {
		t.Fatalf("original/restore failure lost: %d %s", code, output)
	}
	assertFileContent(t, filepath.Join(backup, "transithub-chanshui"), "old-cli")
}

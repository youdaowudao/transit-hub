package system

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChanshuiRestoreKeepsBackupWhenTargetCleanupFails(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"deploy/chanshui-build.sh", "deploy/update-source.sh", "deploy/rollback-source.sh"} {
		for _, failedItem := range []string{"binary", "web"} {
			t.Run(script+"/"+failedItem, func(t *testing.T) {
				root := t.TempDir()
				binary := filepath.Join(root, "chanshui/.artifacts/bin/transithub-chanshui")
				web := filepath.Join(root, "chanshui/web/out-build")
				backup := filepath.Join(root, "chanshui/.artifacts/build.previous")
				database := filepath.Join(root, "data/chanshui/hub.sqlite")
				makeDetectionArtifact(t, binary, "new-cli", true)
				makeDetectionArtifact(t, filepath.Join(web, "index.html"), "new-web", false)
				makeDetectionArtifact(t, filepath.Join(backup, "transithub-chanshui"), "old-cli", true)
				makeDetectionArtifact(t, filepath.Join(backup, "web/index.html"), "old-web", false)
				makeDetectionArtifact(t, database, "reports-stay", false)
				failedTarget := binary
				if failedItem == "web" {
					failedTarget = web
				}
				tools := filepath.Join(root, "tools")
				if err := os.Mkdir(tools, 0700); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, filepath.Join(tools, "python3"), "#!/usr/bin/env bash\nif [[ ${3:-} == "+shellQuote(failedTarget)+" ]]; then printf 'fixture cleanup denied for "+failedItem+"\\n' >&2; exit 53; fi\nexec "+shellQuote(python)+" \"$@\"\n")
				source := readProjectFileForUpgradeTest(t, script)
				if script != "deploy/chanshui-build.sh" {
					source = chanshuiFunctionBlock(t, script)
				}
				body := source + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH
chanshui_pair_root=%s
chanshui_pair_backup=%s
chanshui_backup_created=1
chanshui_bin_backup_ready=1
chanshui_web_backup_ready=1
chanshui_bin_switched=1
chanshui_web_switched=1
if chanshui_restore_pair; then exit 99; else status=$?; fi
[[ "$status" == 1 ]]
`, shellQuote(tools), shellQuote(root), shellQuote(backup))
				output, code := runShellSnippet(t, "", body)
				if code != 0 || !strings.Contains(output, "fixture cleanup denied for "+failedItem) || !strings.Contains(output, "旧备份已保留") {
					t.Fatalf("cleanup failure was not reported with its original cause: %d %s", code, output)
				}
				if failedItem == "web" {
					assertFileContent(t, filepath.Join(backup, "web/index.html"), "old-web")
					assertFileContent(t, filepath.Join(web, "index.html"), "new-web")
					assertFileContent(t, binary, "old-cli")
				} else {
					assertFileContent(t, filepath.Join(backup, "transithub-chanshui"), "old-cli")
					assertFileContent(t, binary, "new-cli")
					assertFileContent(t, filepath.Join(web, "index.html"), "old-web")
				}
				if _, err := os.Stat(filepath.Join(web, "web")); !os.IsNotExist(err) {
					t.Fatalf("restore nested the previous web directory into the live directory: %v", err)
				}
				assertFileContent(t, database, "reports-stay")
			})
		}
	}
}

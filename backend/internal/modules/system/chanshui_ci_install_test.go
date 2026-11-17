package system

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChanshuiDependencyInstallRespectsExistingSkipFlag(t *testing.T) {
	for _, script := range []string{"deploy/chanshui-build.sh", "deploy/update-source.sh", "deploy/rollback-source.sh"} {
		for _, flag := range []string{"unset", "0", "1", "false"} {
			t.Run(script+"/"+flag, func(t *testing.T) {
				root := t.TempDir()
				makeDetectionArtifact(t, filepath.Join(root, "chanshui/cmd/transit-hub/main.go"), "fixture", false)
				makeDetectionArtifact(t, filepath.Join(root, "chanshui/web/package.json"), "{}", false)
				database := filepath.Join(root, "data/chanshui/hub.sqlite")
				makeDetectionArtifact(t, database, "reports-stay", false)
				tools := filepath.Join(root, "tools")
				if err := os.Mkdir(tools, 0700); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, filepath.Join(tools, "node"), "#!/bin/sh\nexit 0\n")
				writeExecutable(t, filepath.Join(tools, "go"), `#!/usr/bin/env bash
set -eu
previous=''
for arg in "$@"; do
 if [[ $arg == run ]];then printf '{"preset_count":35}\n';exit 0;fi
 if [[ $previous == -o ]];then printf 'compiled-cli' >"$arg";chmod 0700 "$arg";fi
 previous=$arg
done
`)
				writeExecutable(t, filepath.Join(tools, "npm"), `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$CI_INSTALL_LOG"
if [[ $1 == run && $2 == build ]];then mkdir -p out/_next;printf 'compiled-web' >out/index.html;fi
`)
				source := readProjectFileForUpgradeTest(t, script)
				if script != "deploy/chanshui-build.sh" {
					source = chanshuiFunctionBlock(t, script)
				}
				flagCommand := "unset CHANSHUI_SKIP_NPM_CI"
				if flag != "unset" {
					flagCommand = "export CHANSHUI_SKIP_NPM_CI=" + shellQuote(flag)
				}
				log := filepath.Join(root, "npm.log")
				body := source + "\n" + fmt.Sprintf(`
export PATH=%s:$PATH CI_INSTALL_LOG=%s
%s
chanshui_build_stage %s %s
`, shellQuote(tools), shellQuote(log), flagCommand, shellQuote(root), shellQuote(filepath.Join(root, "chanshui/.artifacts/build.next")))
				out, code := runShellSnippet(t, "", body)
				if code != 0 {
					t.Fatalf("build failed: %d %s", code, out)
				}
				commands := readTestFile(t, log)
				ciCount := strings.Count(commands, "ci --registry=https://registry.npmjs.org\n")
				want := 1
				if flag == "1" {
					want = 0
				}
				if ciCount != want || strings.Count(commands, "run build\n") != 1 {
					t.Fatalf("skip=%s must install %d times and build once; got %q", flag, want, commands)
				}
				assertFileContent(t, filepath.Join(root, "chanshui/.artifacts/build.next/transithub-chanshui"), "compiled-cli")
				assertFileContent(t, filepath.Join(root, "chanshui/.artifacts/build.next/web/index.html"), "compiled-web")
				assertFileContent(t, filepath.Join(root, "chanshui/.artifacts/build.next/web/bank-manifest.json"), "{\"preset_count\":35}\n")
				assertFileContent(t, database, "reports-stay")
			})
		}
	}
}

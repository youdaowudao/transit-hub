package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Execute the old maintenance build sequence against the new package scripts.
// This detects missing first-upgrade integration even if the new updater works.
func TestSourceUpgradeLegacyBuildIncludesChanshui(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is required for the maintenance lifecycle contract")
	}
	project := t.TempDir()
	tools := filepath.Join(project, "tools")
	for _, dir := range []string{"tools", "frontend", "backend", "chanshui/web", "chanshui/cmd/transit-hub", "scripts", "deploy", "data/chanshui"} {
		if err := os.MkdirAll(filepath.Join(project, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, relative := range []string{"frontend/package.json", "chanshui/web/package.json", "scripts/build-chanshui.sh"} {
		if err := os.WriteFile(filepath.Join(project, relative), []byte(readProjectFileForUpgradeTest(t, relative)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The helper does not exist on the RED baseline; its absence must not make
	// setup fail before the old build sequence is actually executed.
	root := projectRootForChanshuiLegacyTest(t)
	if helper, err := os.ReadFile(filepath.Join(root, "deploy/chanshui-build.sh")); err == nil {
		if err := os.WriteFile(filepath.Join(project, "deploy/chanshui-build.sh"), helper, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "chanshui/cmd/transit-hub/main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(project, "data/chanshui/hub.sqlite")
	if err := os.WriteFile(database, []byte("existing report data must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(tools, "npm"), "#!/usr/bin/env bash\nset -e\nfor arg in \"$@\"; do if [[ $arg == ci ]]; then exit 0; fi; done\nexec "+shellQuote(npm)+" \"$@\"\n")
	writeExecutable(t, filepath.Join(tools, "vue-tsc"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(tools, "vite"), "#!/usr/bin/env bash\nset -e\nmkdir -p dist\nprintf 'compiled Hub page' > dist/index.html\n")
	writeExecutable(t, filepath.Join(tools, "next"), `#!/usr/bin/env bash
set -e
test "${NEXT_PUBLIC_TRANSITHUB:-}" = 1
mkdir -p out/_next/static
printf 'compiled detection page' > out/index.html
printf 'compiled detection script' > out/_next/static/app.js
`)
	writeExecutable(t, filepath.Join(tools, "go"), `#!/usr/bin/env bash
set -e
previous=''
output=''
for arg in "$@"; do
  if [[ $arg == run ]]; then printf '{"preset_count":35,"fixture":true}\n'; exit 0; fi
  if [[ $previous == -o ]]; then output=$arg; fi
  previous=$arg
done
if [[ -z $output ]]; then exit 0; fi
mkdir -p "$(dirname "$output")"
printf '#!/bin/sh\nexit 0\n' > "$output"
chmod 0755 "$output"
`)
	driver := filepath.Join(project, "legacy-upgrade.sh")
	writeExecutable(t, driver, readProjectFileForUpgradeTest(t, "backend/internal/modules/system/testdata/chanshui-legacy-upgrade.sh"))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", driver)
	command.Env = append(os.Environ(), "PROJECT_DIR="+project, "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"), "CHANSHUI_SKIP_NPM_CI=0", "npm_config_cache="+filepath.Join(project, "npm-cache"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy maintenance build failed: %v\n%s", err, output)
	}
	for _, relative := range []string{"frontend/dist/index.html", "transithub-api.next", "chanshui/.artifacts/bin/transithub-chanshui", "chanshui/web/out-build/index.html", "chanshui/web/out-build/_next/static/app.js", "chanshui/web/out-build/bank-manifest.json"} {
		info, err := os.Stat(filepath.Join(project, relative))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("旧升级服务本次构建必须自动生成 %s，实际没有完整产物：%v", relative, err)
		}
	}
	if got := readTestFile(t, database); got != "existing report data must remain" {
		t.Fatalf("building must preserve saved detection data, got %q", got)
	}
}

func projectRootForChanshuiLegacyTest(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve fixture project root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "../../../.."))
}

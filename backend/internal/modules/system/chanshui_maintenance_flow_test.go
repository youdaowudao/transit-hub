package system

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	maintenanceOld  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	maintenanceNew  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	maintenanceBare = "cccccccccccccccccccccccccccccccccccccccc"
)

type maintenanceFixture struct{ base, project, tools, update, rollback string }

func newMaintenanceFixture(t *testing.T, initial string) *maintenanceFixture {
	t.Helper()
	base := t.TempDir()
	f := &maintenanceFixture{base: base, project: filepath.Join(base, "project"), tools: filepath.Join(base, "tools"), update: filepath.Join(base, "installed/update-source.sh"), rollback: filepath.Join(base, "installed/rollback-source.sh")}
	for _, relative := range []string{"tools", "installed", "state", "units", "container", "templates", "project/deploy", "project/frontend", "project/backend/internal/config", "project/data/chanshui"} {
		if err := os.MkdirAll(filepath.Join(base, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"update-source.sh", "rollback-source.sh"} {
		text := readProjectFileForUpgradeTest(t, "deploy/"+name)
		for old, replacement := range map[string]string{
			"PROJECT_DIR='/opt/transithub'":                     "PROJECT_DIR=" + shellQuote(f.project),
			"STATE_DIR='/var/lib/transithub'":                   "STATE_DIR=" + shellQuote(filepath.Join(base, "state")),
			"LOCK_FILE='/run/lock/transithub-maintenance.lock'": "LOCK_FILE=" + shellQuote(filepath.Join(base, "maintenance.lock")),
			"SYSTEMD_UNIT_DIR='/etc/systemd/system'":            "SYSTEMD_UNIT_DIR=" + shellQuote(filepath.Join(base, "units")),
			"HEALTH_TIMEOUT_SECONDS=180":                        "HEALTH_TIMEOUT_SECONDS=1",
			"local attempts=60":                                 "local attempts=1",
		} {
			text = strings.ReplaceAll(text, old, replacement)
		}
		writeExecutable(t, filepath.Join(base, "installed", name), text)
		writeExecutable(t, filepath.Join(f.project, "deploy", name), text)
	}
	for _, name := range []string{"run-source-upgrade.sh", "run-source-rollback.sh", "run-service-restart.sh"} {
		writeExecutable(t, filepath.Join(f.project, "deploy", name), "#!/bin/sh\nexit 0\n")
	}
	for _, name := range []string{"transithub-upgrade.service", "transithub-rollback.service", "transithub-restart.service"} {
		makeDetectionArtifact(t, filepath.Join(f.project, "deploy", name), "fixture maintenance unit", false)
	}
	makeDetectionArtifact(t, filepath.Join(base, "templates/chanshui-build.sh"), readProjectFileForUpgradeTest(t, "deploy/chanshui-build.sh"), false)
	makeDetectionArtifact(t, filepath.Join(base, "target"), maintenanceNew, false)
	makeDetectionArtifact(t, filepath.Join(base, "postgres-data"), "postgres stays", false)
	makeDetectionArtifact(t, filepath.Join(base, "redis-data"), "redis stays", false)
	makeDetectionArtifact(t, filepath.Join(f.project, "data/chanshui/hub.sqlite"), "reports stay", false)
	common := `#!/usr/bin/env python3
from pathlib import Path
import json, os, shutil, sys
base = Path(os.environ['CS_MAINTENANCE_FIXTURE'])
project = base / 'project'
name = Path(sys.argv[0]).name
args = sys.argv[1:]
with (base / 'commands.log').open('a') as log:
    log.write(name + ' ' + ' '.join(args) + '\n')
tags = {'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa': 'old', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb': 'new', 'cccccccccccccccccccccccccccccccccccccccc': 'bare'}
head = (base / 'head').read_text().strip() if (base / 'head').exists() else ''
tag = tags.get(head, 'unknown')
def write(path, value, executable=False):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(value)
    if executable:
        path.chmod(0o755)
def setting(key):
    path = base / key
    return path.read_text().strip() if path.exists() else ''
`
	programs := map[string]string{
		"git": `
if args[:1] == ['-C']:
    args = args[2:]
if args[:2] == ['rev-parse', 'HEAD']:
    print(head)
elif args[:1] == ['switch']:
    commit = args[-1]
    if commit == 'origin/main':
        commit = setting('target')
    if commit not in tags:
        raise SystemExit('fixture unknown commit')
    tag = tags[commit]
    write(base / 'head', commit)
    write(project / 'backend/internal/config/config.go', 'package config\nconst (\n defaultAppVersion = "' + {'old':'V3.1.12','new':'V3.1.14','bare':'V3.1.9'}[tag] + '"\n)\n')
    main = project / 'chanshui/cmd/transit-hub/main.go'
    helper = project / 'deploy/chanshui-build.sh'
    if tag == 'bare':
        if main.exists(): main.unlink()
    else:
        write(main, 'package main\nfunc main() {}\n')
    if tag == 'new':
        write(helper, (base / 'templates/chanshui-build.sh').read_text())
    elif helper.exists():
        helper.unlink()
    package = {'scripts': {'build': 'vue-tsc --noEmit -p tsconfig.app.json && vite build'}}
    if tag == 'new':
        package['scripts']['prebuild'] = 'bash ../scripts/build-chanshui.sh'
    write(project / 'frontend/package.json', json.dumps(package))
elif args[:1] == ['cat-file']:
    if args[-1].split('^')[0] not in tags:
        raise SystemExit(1)
elif args[:1] != ['fetch']:
    raise SystemExit('fixture unexpected git command')
`,
		"go": `
if 'run' in args:
    print(json.dumps({'candidate': tag, 'preset_count': 35}))
    raise SystemExit(0)
kind = 'cli' if './cmd/transit-hub' in args else 'api'
if setting('fail-build') == kind + ':' + head:
    print('fixture ' + kind + ' compiler failure', file=sys.stderr)
    raise SystemExit(41 if kind == 'cli' else 42)
output = Path(args[args.index('-o')+1])
write(output, tag + '-' + kind, True)
`,
		"node": "\nraise SystemExit(0)\n",
		"npm": `
if 'ci' in args:
    raise SystemExit(0)
if 'run' not in args or 'build' not in args:
    raise SystemExit('fixture unexpected npm command')
if Path.cwd() == project / 'chanshui/web':
    if os.environ.get('NEXT_PUBLIC_TRANSITHUB') != '1':
        raise SystemExit('fixture missing Hub export profile')
    if setting('skip-export') != head:
        write(Path('out/index.html'), tag + '-web')
        write(Path('out/_next/static/app.js'), tag + '-script')
else:
    if '--ignore-scripts' not in args:
        raise SystemExit('new maintenance recursively invoked prebuild')
    directory = args[args.index('--outDir')+1] if '--outDir' in args else 'dist'
    write(Path(directory) / 'index.html', tag + '-vue')
`,
		"docker": `
if 'pg_dump' in args:
    output = next(a.split('=',1)[1] for a in args if a.startswith('--file='))
    write(base / 'container' / Path(output).name, 'fixture database dump')
elif 'psql' in args:
    print('000000')
elif args[:1] == ['cp']:
    source = base / 'container' / Path(args[1].split(':',1)[1]).name
    target = Path(args[2])
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
elif 'rm' not in args:
    raise SystemExit('fixture unexpected docker command')
`,
		"curl": `
if setting('fail-health') == head:
    print('fixture health refused', file=sys.stderr)
    raise SystemExit(7)
print('{"status":"ok"}')
`,
		"systemctl":  "\nraise SystemExit(0)\n",
		"journalctl": "\nprint('fixture journal')\n",
		"rm": `
for value in args:
    if value.startswith('-'):
        continue
    target = Path(value).absolute()
    if not target.is_relative_to(base):
        raise SystemExit('fixture refused cleanup outside its root')
    if target.is_symlink() or target.is_file():
        target.unlink()
    elif target.exists():
        shutil.rmtree(target)
`,
	}
	for name, body := range programs {
		writeExecutable(t, filepath.Join(f.tools, name), common+body)
	}
	writeExecutable(t, filepath.Join(f.tools, "sudo"), "#!/usr/bin/env bash\nexec \"$@\"\n")
	f.gitSwitch(t, initial)
	tag := map[string]string{maintenanceOld: "old", maintenanceNew: "new", maintenanceBare: "bare"}[initial]
	makeDetectionArtifact(t, filepath.Join(f.project, "transithub-api"), tag+"-api", true)
	makeDetectionArtifact(t, filepath.Join(f.project, "frontend/dist/index.html"), tag+"-vue", false)
	makeDetectionArtifact(t, filepath.Join(f.project, "chanshui/.artifacts/bin/transithub-chanshui"), tag+"-cli", true)
	makeDetectionArtifact(t, filepath.Join(f.project, "chanshui/web/out-build/index.html"), tag+"-web", false)
	return f
}
func (f *maintenanceFixture) env() []string {
	return append(os.Environ(), "CS_MAINTENANCE_FIXTURE="+f.base, "PATH="+f.tools+string(os.PathListSeparator)+os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1")
}
func (f *maintenanceFixture) gitSwitch(t *testing.T, commit string) {
	t.Helper()
	command := exec.Command(filepath.Join(f.tools, "git"), "switch", "--detach", commit)
	command.Env = f.env()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture source setup failed: %v %s", err, output)
	}
}
func (f *maintenanceFixture) run(t *testing.T, path string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", path)
	command.Env = f.env()
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return string(output), e.ExitCode()
	}
	t.Fatalf("maintenance script failed to execute: %v %s", err, output)
	return "", -1
}
func (f *maintenanceFixture) point(t *testing.T, commit string) {
	t.Helper()
	version := map[string]string{maintenanceOld: "V3.1.12", maintenanceBare: "V3.1.9"}[commit]
	raw, _ := json.Marshal(map[string]any{"commit": commit, "version": version, "schemaVersion": 0, "dumpPath": filepath.Join(f.project, "backups/transithub.dump")})
	makeDetectionArtifact(t, filepath.Join(f.base, "state/rollback-point.json"), string(raw), false)
}
func (f *maintenanceFixture) assertData(t *testing.T) {
	t.Helper()
	assertFileContent(t, filepath.Join(f.project, "data/chanshui/hub.sqlite"), "reports stay")
	assertFileContent(t, filepath.Join(f.base, "postgres-data"), "postgres stays")
	assertFileContent(t, filepath.Join(f.base, "redis-data"), "redis stays")
}
func (f *maintenanceFixture) assertArtifacts(t *testing.T, mainTag, childTag string) {
	t.Helper()
	assertFileContent(t, filepath.Join(f.project, "transithub-api"), mainTag+"-api")
	assertFileContent(t, filepath.Join(f.project, "frontend/dist/index.html"), mainTag+"-vue")
	assertFileContent(t, filepath.Join(f.project, "chanshui/.artifacts/bin/transithub-chanshui"), childTag+"-cli")
	assertFileContent(t, filepath.Join(f.project, "chanshui/web/out-build/index.html"), childTag+"-web")
}

func (f *maintenanceFixture) assertPoint(t *testing.T, name, commit, version string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.base, "state", name))
	if err != nil {
		t.Fatal(err)
	}
	var point map[string]any
	if json.Unmarshal(raw, &point) != nil || point["commit"] != commit || point["version"] != version || point["schemaVersion"] != float64(0) {
		t.Fatalf("maintenance point does not describe actual version: %s", raw)
	}
}

func (f *maintenanceFixture) assertNoStaging(t *testing.T) {
	t.Helper()
	for _, name := range []string{"chanshui/.artifacts/build.next", "chanshui/.artifacts/build.previous"} {
		if _, err := os.Lstat(filepath.Join(f.project, name)); !os.IsNotExist(err) {
			t.Fatalf("maintenance left detection staging %s: %v", name, err)
		}
	}
}

func TestChanshuiRealUpgradeScriptBuildAndFailurePaths(t *testing.T) {
	for _, kind := range []string{"success", "cli", "static", "api"} {
		t.Run(kind, func(t *testing.T) {
			f := newMaintenanceFixture(t, maintenanceOld)
			if kind == "static" {
				makeDetectionArtifact(t, filepath.Join(f.base, "skip-export"), maintenanceNew, false)
				makeDetectionArtifact(t, filepath.Join(f.project, "chanshui/web/out/index.html"), "stale-export", false)
				makeDetectionArtifact(t, filepath.Join(f.project, "chanshui/web/out/_next/app.js"), "stale-script", false)
			} else if kind != "success" {
				makeDetectionArtifact(t, filepath.Join(f.base, "fail-build"), kind+":"+maintenanceNew, false)
			}
			output, code := f.run(t, f.update)
			if kind == "success" {
				if code != 0 {
					t.Fatalf("upgrade=%d %s", code, output)
				}
				f.assertArtifacts(t, "new", "new")
				f.assertPoint(t, "verified-commit.json", maintenanceNew, "V3.1.14")
			} else {
				cause := "fixture " + kind + " compiler failure"
				if kind == "static" {
					cause = "检测页面导出不完整"
				}
				if code == 0 || !strings.Contains(output, cause) {
					t.Fatalf("build failure not reported: %d %s", code, output)
				}
				assertFileContent(t, filepath.Join(f.project, "transithub-api"), "old-api")
				assertFileContent(t, filepath.Join(f.project, "chanshui/.artifacts/bin/transithub-chanshui"), "old-cli")
				assertFileContent(t, filepath.Join(f.project, "chanshui/web/out-build/index.html"), "old-web")
				wantHead := maintenanceOld
				wantVue := "old-vue"
				if kind == "api" {
					wantHead = maintenanceNew
					wantVue = "new-vue"
				}
				assertFileContent(t, filepath.Join(f.base, "head"), wantHead)
				assertFileContent(t, filepath.Join(f.project, "frontend/dist/index.html"), wantVue)
				if _, err := os.Stat(filepath.Join(f.base, "state/verified-commit.json")); !os.IsNotExist(err) {
					t.Fatalf("failed upgrade was marked verified: %v", err)
				}
			}
			f.assertPoint(t, "rollback-point.json", maintenanceOld, "V3.1.12")
			f.assertNoStaging(t)
			f.assertData(t)
		})
	}
}
func TestChanshuiRealRollbackHealthFailureRestoresWholePreviousCombination(t *testing.T) {
	f := newMaintenanceFixture(t, maintenanceNew)
	f.point(t, maintenanceOld)
	makeDetectionArtifact(t, filepath.Join(f.base, "state/verified-commit.json"), `{"commit":"`+maintenanceNew+`","version":"V3.1.14","schemaVersion":0}`, false)
	makeDetectionArtifact(t, filepath.Join(f.base, "fail-health"), maintenanceOld, false)
	output, code := f.run(t, f.rollback)
	defer func() {
		if t.Failed() {
			t.Logf("actual rollback output:\n%s", output)
			commands, _ := os.ReadFile(filepath.Join(f.base, "commands.log"))
			t.Logf("isolated command trace:\n%s", commands)
		}
	}()
	if code == 0 || !strings.Contains(output, "fixture health refused") || !strings.Contains(output, "已恢复到回滚前") {
		t.Fatalf("rollback health failure was not restored: %d %s", code, output)
	}
	if strings.Count(output, "正在恢复回滚前的版本") != 1 {
		t.Fatalf("rollback must restore the complete previous combination exactly once: %s", output)
	}
	f.assertArtifacts(t, "new", "new")
	assertFileContent(t, filepath.Join(f.base, "head"), maintenanceNew)
	f.assertPoint(t, "verified-commit.json", maintenanceNew, "V3.1.14")
	f.assertNoStaging(t)
	f.assertData(t)
}

func TestChanshuiRealUpgradeHealthFailureRestoresDetectionPairOnce(t *testing.T) {
	f := newMaintenanceFixture(t, maintenanceOld)
	makeDetectionArtifact(t, filepath.Join(f.base, "fail-health"), maintenanceNew, false)
	output, code := f.run(t, f.update)
	defer func() {
		if t.Failed() {
			t.Logf("actual upgrade output:\n%s", output)
		}
	}()
	if code == 0 || !strings.Contains(output, "健康接口") {
		t.Fatalf("upgrade health failure not reported: %d %s", code, output)
	}
	if strings.Count(output, "升级失败（第 ") != 1 {
		t.Fatalf("upgrade must handle the failing health check exactly once: %s", output)
	}
	// Main Vue/Go failure handling remains the pre-existing update behavior;
	// the detector's previous complete pair must be restored exactly once.
	f.assertArtifacts(t, "new", "old")
	f.assertPoint(t, "rollback-point.json", maintenanceOld, "V3.1.12")
	f.assertNoStaging(t)
	f.assertData(t)
}

func TestChanshuiRealMaintenanceRollbackToOldTargetThenUpgradeAgain(t *testing.T) {
	for _, target := range []string{maintenanceOld, maintenanceBare} {
		t.Run(target[:4], func(t *testing.T) {
			f := newMaintenanceFixture(t, maintenanceOld)
			if output, code := f.run(t, f.update); code != 0 {
				t.Fatalf("initial upgrade=%d %s", code, output)
			}
			f.point(t, target)
			installedRollback := filepath.Join(f.project, "rollback-source.sh")
			output, code := f.run(t, installedRollback)
			if code != 0 {
				t.Fatalf("rollback=%d %s", code, output)
			}
			tag := "old"
			child := "old"
			version := "V3.1.12"
			if target == maintenanceBare {
				tag = "bare"
				child = "new"
				version = "V3.1.9"
			}
			f.assertArtifacts(t, tag, child)
			f.assertPoint(t, "verified-commit.json", target, version)
			f.assertNoStaging(t)
			if _, err := os.Stat(filepath.Join(f.project, "deploy/chanshui-build.sh")); !os.IsNotExist(err) {
				t.Fatalf("old target unexpectedly retained source helper: %v", err)
			}
			installedUpdate := filepath.Join(f.project, "update-source.sh")
			output, code = f.run(t, installedUpdate)
			if code != 0 {
				t.Fatalf("upgrade after helper-less target=%d %s", code, output)
			}
			f.assertArtifacts(t, "new", "new")
			f.assertPoint(t, "verified-commit.json", maintenanceNew, "V3.1.14")
			f.assertNoStaging(t)
			f.assertData(t)
		})
	}
}

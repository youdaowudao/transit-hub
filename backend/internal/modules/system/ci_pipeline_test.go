package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ciSuiteFixture struct{ root, tools, log string }

func newCISuiteFixture(t *testing.T) ciSuiteFixture {
	t.Helper()
	root := t.TempDir()
	f := ciSuiteFixture{root, filepath.Join(root, "tools"), filepath.Join(root, "commands")}
	for _, dir := range []string{"tools", "scripts", "backend", "frontend", "chanshui/web"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, filepath.Join(root, "scripts/test-full-regression.sh"), readProjectFileForUpgradeTest(t, "scripts/test-full-regression.sh"))
	for _, name := range []string{"git", "node", "go", "npm"} {
		writeExecutable(t, filepath.Join(f.tools, name), `#!/usr/bin/env bash
set -eu
name="${0##*/}"
if [[ $name == go && ${1:-} == env ]]; then
 if [[ ${CI_FIXTURE_FAIL:-} == "$name $*" ]];then printf 'fixture cache query rejected\n' >&2;exit 42;fi
 printf '%s/cache/%s\n' "$CI_FIXTURE_ROOT" "$2";exit 0
fi
if [[ $name == npm && ${1:-} == config ]]; then
 if [[ ${CI_FIXTURE_FAIL:-} == "$name $*" ]];then printf 'fixture cache query rejected\n' >&2;exit 42;fi
 printf '%s/cache/npm\n' "$CI_FIXTURE_ROOT";exit 0
fi
printf '%s %s\n' "$name" "$*" >>"$CI_FIXTURE_LOG"
if [[ ${CI_FIXTURE_FAIL:-} == "$name $*" ]]; then printf 'fixture check rejected\n' >&2;exit 42;fi
`)
	}
	writeExecutable(t, filepath.Join(root, "scripts/build-chanshui.sh"), `#!/usr/bin/env bash
set -eu
printf 'build-chanshui %s|%s|%s|%s|%s\n' "${CHANSHUI_SKIP_NPM_CI:-}" "${GOCACHE:-}" "${GOMODCACHE:-}" "${GOPATH:-}" "${npm_config_cache:-}" >>"$CI_FIXTURE_LOG"
`)
	return f
}

func (f ciSuiteFixture) run(t *testing.T, args []string, fail string) ([]string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(f.root, "scripts/test-full-regression.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+f.tools+":"+os.Getenv("PATH"), "CI_FIXTURE_ROOT="+f.root, "CI_FIXTURE_LOG="+f.log, "CI_FIXTURE_FAIL="+fail)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("suite did not exit: %v %s", ctx.Err(), out)
	}
	data, readErr := os.ReadFile(f.log)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	var commands []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			commands = append(commands, strings.ReplaceAll(line, f.root, "$ROOT"))
		}
	}
	return commands, string(out), err
}

func ciExpectedSuiteCommands() map[string][]string {
	return map[string][]string{
		"guards": {
			"git -C $ROOT diff --check",
			"node --test $ROOT/scripts/check-test-calendar-boundaries.test.mjs",
			"node $ROOT/scripts/check-test-calendar-boundaries.mjs --root $ROOT",
			"node --test $ROOT/scripts/question-answer-review-fixture.test.mjs $ROOT/scripts/question-answer-batch-review-fixture.test.mjs $ROOT/scripts/c1-question-answer-fixture.test.mjs $ROOT/scripts/question-answer-keyword-highlight-fixture.test.mjs",
			"node --test $ROOT/scripts/test-core-regression-membership.test.mjs $ROOT/scripts/test-c43-core-membership.test.mjs",
		},
		"backend":  {"go test ./... -count=1 -v"},
		"frontend": {"npm run test", "npm run typecheck"},
		"chanshui": {
			"go test ./... -count=1",
			"npm test",
			"npm run typecheck",
			"build-chanshui 1|$ROOT/cache/GOCACHE|$ROOT/cache/GOMODCACHE|$ROOT/cache/GOPATH|$ROOT/cache/npm",
		},
	}
}

func TestCISuitesRetainCompleteDiscoveryWithoutDuplicateExecution(t *testing.T) {
	expected := ciExpectedSuiteCommands()
	for _, suite := range []string{"guards", "backend", "frontend", "chanshui", "all", ""} {
		t.Run(suite, func(t *testing.T) {
			f := newCISuiteFixture(t)
			var args []string
			if suite != "" {
				args = []string{suite}
			}
			var want []string
			if suite == "all" || suite == "" {
				for _, group := range []string{"guards", "backend", "frontend", "chanshui"} {
					want = append(want, expected[group]...)
				}
			} else {
				want = expected[suite]
			}
			got, out, err := f.run(t, args, "")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("suite %q commands=%q want=%q err=%v output=%s", suite, got, want, err, out)
			}
		})
	}
}

func TestCISuitesRejectUnknownArgumentsBeforeExecutingChecks(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"backend", "frontend"}, {""}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			f := newCISuiteFixture(t)
			commands, out, err := f.run(t, args, "")
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 64 || len(commands) != 0 || !strings.Contains(out, "guards") {
				t.Fatalf("invalid suite ran checks: commands=%q err=%v out=%s", commands, err, out)
			}
		})
	}
}

func TestCISuiteStopsOnOriginalFailure(t *testing.T) {
	f := newCISuiteFixture(t)
	commands, out, err := f.run(t, []string{"all"}, "go test ./... -count=1 -v")
	want := append(ciExpectedSuiteCommands()["guards"], "go test ./... -count=1 -v")
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 42 || !reflect.DeepEqual(commands, want) || !strings.Contains(out, "fixture check rejected") {
		t.Fatalf("full gate swallowed failure or continued: commands=%q err=%v out=%s", commands, err, out)
	}
}

func TestCICacheLookupFailureStopsBeforeArtifactBuild(t *testing.T) {
	for _, failure := range []string{"go env GOCACHE", "go env GOMODCACHE", "go env GOPATH", "npm config get cache"} {
		t.Run(failure, func(t *testing.T) {
			f := newCISuiteFixture(t)
			commands, out, err := f.run(t, []string{"chanshui"}, failure)
			want := ciExpectedSuiteCommands()["chanshui"][:3]
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 42 || !reflect.DeepEqual(commands, want) || !strings.Contains(out, "fixture cache query rejected") {
				t.Fatalf("cache lookup failure was hidden or artifact build continued: commands=%q err=%v out=%s", commands, err, out)
			}
		})
	}
}

func TestCIAggregateOnlyPassesWhenEveryJobSucceeded(t *testing.T) {
	path := filepath.Join(projectRootForChanshuiLegacyTest(t), "scripts/check-ci-jobs.sh")
	for _, state := range []string{"success", "failure", "cancelled", "skipped", "", "unknown"} {
		for slot := 0; slot < 4; slot++ {
			t.Run(state+"/"+string(rune('0'+slot)), func(t *testing.T) {
				args := []string{"success", "success", "success", "success"}
				args[slot] = state
				out, err := exec.Command("bash", append([]string{path}, args...)...).CombinedOutput()
				if (err == nil) != (state == "success") {
					t.Fatalf("aggregate status %q=%v out=%s", args, err, out)
				}
				if state != "success" && !strings.Contains(string(out), []string{"guards", "backend", "frontend", "chanshui"}[slot]) {
					t.Fatalf("aggregate did not name failed job: %s", out)
				}
			})
		}
	}
	for _, args := range [][]string{nil, {"success"}, {"success", "success", "success", "success", "success"}} {
		out, err := exec.Command("bash", append([]string{path}, args...)...).CombinedOutput()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 64 {
			t.Fatalf("aggregate accepted incomplete result list: %q %v %s", args, err, out)
		}
	}
}

func TestCIWorkflowKeepsProtectedTestAndCrossDirectoryDependencies(t *testing.T) {
	script := readProjectFileForUpgradeTest(t, ".github/workflows/ci.yml")
	for _, want := range []string{
		"  guards:", "  backend:", "  frontend:", "  chanshui:", "  test:",
		"needs: [guards, backend, frontend, chanshui]", "if: ${{ always() }}", "bash scripts/check-ci-jobs.sh",
		"'${{ needs.guards.result }}'", "'${{ needs.backend.result }}'", "'${{ needs.frontend.result }}'", "'${{ needs.chanshui.result }}'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("workflow lost required aggregate or job: %s", want)
		}
	}
	for _, group := range []string{"guards", "backend", "frontend", "chanshui"} {
		if strings.Count(script, "make test-ci CI_SUITE="+group) != 1 {
			t.Errorf("suite %s must execute exactly once", group)
		}
	}
	guardStart := strings.Index(script, "  guards:\n")
	guardEnd := strings.Index(script, "  backend:\n")
	if guardStart < 0 || guardEnd < guardStart {
		t.Fatal("guards isolated job missing")
	}
	guards := script[guardStart:guardEnd]
	for _, want := range []string{"cache: npm", "cache-dependency-path: frontend/package-lock.json", "npm ci --prefix frontend"} {
		if !strings.Contains(guards, want) {
			t.Errorf("cold calendar guard lacks its TypeScript dependency: %s", want)
		}
	}
	if strings.Index(guards, "npm ci --prefix frontend") > strings.Index(guards, "make test-ci CI_SUITE=guards") {
		t.Error("calendar guard executes before TypeScript is installed")
	}
	start := strings.Index(script, "  frontend:\n")
	end := strings.Index(script, "  chanshui:\n")
	if start < 0 || end < start {
		t.Fatal("frontend isolated job missing")
	}
	front := script[start:end]
	for _, want := range []string{"frontend/package-lock.json", "chanshui/web/package-lock.json", "npm ci --prefix frontend", "npm ci --prefix chanshui/web"} {
		if !strings.Contains(front, want) {
			t.Errorf("frontend React cross-directory contract lost: %s", want)
		}
	}
	makefile := readProjectFileForUpgradeTest(t, "Makefile")
	if !strings.Contains(makefile, "CI_SUITE ?= all") || !strings.Contains(makefile, `scripts/test-full-regression.sh "$(CI_SUITE)"`) {
		t.Error("make test-ci must default to all and pass the selected suite")
	}
}

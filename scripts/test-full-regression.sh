#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CI_GROUP="${1-all}"
if (($# > 1)) || [[ ! "$CI_GROUP" =~ ^(all|guards|backend|frontend|chanshui)$ ]]; then
  printf 'CI检查分组无效：仅支持all、guards、backend、frontend、chanshui，并且只能传一个分组。\n' >&2
  exit 64
fi

check_jobs() {
  local jobs
  jobs="$(jobs -pr || true)"
  if [[ -n "$jobs" ]]; then
    echo "background jobs left by test command:"
    echo "$jobs"
    return 1
  fi
}

run_step() {
  local name="$1"
  shift
  local started=$SECONDS
  echo
  echo "==> $name"
  "$@"
  check_jobs
  printf '<== %s (%ss)\n' "$name" "$((SECONDS - started))"
}

run_guards() {
  run_step "git diff whitespace check" git -C "$ROOT_DIR" diff --check

  run_step "test calendar boundary guard tests" \
    node --test "$ROOT_DIR/scripts/check-test-calendar-boundaries.test.mjs"

  run_step "test calendar boundary guard" \
    node "$ROOT_DIR/scripts/check-test-calendar-boundaries.mjs" --root "$ROOT_DIR"

  run_step "question answer fixture safety tests" \
    node --test \
      "$ROOT_DIR/scripts/question-answer-review-fixture.test.mjs" \
      "$ROOT_DIR/scripts/question-answer-batch-review-fixture.test.mjs" \
      "$ROOT_DIR/scripts/c1-question-answer-fixture.test.mjs" \
        "$ROOT_DIR/scripts/question-answer-keyword-highlight-fixture.test.mjs"

  run_step "core regression membership tests" \
    node --test "$ROOT_DIR/scripts/test-core-regression-membership.test.mjs" "$ROOT_DIR/scripts/test-c43-core-membership.test.mjs"
}

run_backend() {
  run_step "backend full Go test suite" \
    bash -c "cd '$ROOT_DIR/backend' && go test ./... -count=1 -v"
}

run_frontend() {
  run_step "frontend full test suite" \
    bash -c "cd '$ROOT_DIR/frontend' && npm run test"

  run_step "frontend typecheck" \
    bash -c "cd '$ROOT_DIR/frontend' && npm run typecheck"
}

run_chanshui() {
  run_step "chanshui full Go module suite" \
    bash -c "cd '$ROOT_DIR/chanshui' && go test ./... -count=1"

  run_step "chanshui original and Hub browser contracts" \
    bash -c "cd '$ROOT_DIR/chanshui/web' && npm test && npm run typecheck"

  local ci_go_cache ci_go_modules ci_go_path ci_npm_cache
  ci_go_cache="$(go env GOCACHE)"
  ci_go_modules="$(go env GOMODCACHE)"
  ci_go_path="$(go env GOPATH)"
  ci_npm_cache="$(npm config get cache)"
  run_step "chanshui deployment artifact build" \
    env CHANSHUI_SKIP_NPM_CI=1 \
      GOCACHE="$ci_go_cache" GOMODCACHE="$ci_go_modules" GOPATH="$ci_go_path" \
      npm_config_cache="$ci_npm_cache" bash "$ROOT_DIR/scripts/build-chanshui.sh"
}

case "$CI_GROUP" in
  all) run_guards; run_backend; run_frontend; run_chanshui ;;
  guards) run_guards ;;
  backend) run_backend ;;
  frontend) run_frontend ;;
  chanshui) run_chanshui ;;
esac

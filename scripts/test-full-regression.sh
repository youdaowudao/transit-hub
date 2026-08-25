#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

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
  echo
  echo "==> $name"
  "$@"
  check_jobs
}

run_step "git diff whitespace check" git -C "$ROOT_DIR" diff --check

run_step "test calendar boundary guard tests" \
  node --test "$ROOT_DIR/scripts/check-test-calendar-boundaries.test.mjs"

run_step "test calendar boundary guard" \
  node "$ROOT_DIR/scripts/check-test-calendar-boundaries.mjs" --root "$ROOT_DIR"

run_step "backend full Go test suite" \
  bash -c "cd '$ROOT_DIR/backend' && go test ./... -count=1"

run_step "frontend full test suite" \
  bash -c "cd '$ROOT_DIR/frontend' && npm run test"

run_step "frontend typecheck" \
  bash -c "cd '$ROOT_DIR/frontend' && npm run typecheck"

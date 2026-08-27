#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-core}"

usage() {
  cat <<'USAGE'
Usage:
  bash scripts/test-core-regression.sh [core|connection-health]

Runs the local core regression gate. This script does not start dev services,
does not run SSH, and does not contact production.
USAGE
}

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

run_connection_health_gate() {
  run_step "git diff whitespace check" git -C "$ROOT_DIR" diff --check

  run_step "test calendar boundary guard tests" \
    node --test "$ROOT_DIR/scripts/check-test-calendar-boundaries.test.mjs"

  run_step "test calendar boundary guard" \
    node "$ROOT_DIR/scripts/check-test-calendar-boundaries.mjs" --root "$ROOT_DIR"

  run_step "backend connection_health and upstream tests" \
    bash -c "cd '$ROOT_DIR/backend' && go test ./internal/modules/connection_health ./internal/modules/upstream -count=1"

  run_step "backend connection_health priority race tests" \
    bash -c "cd '$ROOT_DIR/backend' && go test -race ./internal/modules/connection_health -run 'Test.*(Priority|Scheduler|Refresh|Sync|Regression)' -count=1"

  run_step "frontend connection health regression tests" \
    bash -c "cd '$ROOT_DIR/frontend' && npm run test -- connection-health-async-priority.test.ts connection-health-main-site-error.test.ts connection-health-main-site-error.behavior.test.ts connection-health-multiplier-resolution.test.ts connection-health-production-rank.test.ts connection-health-refresh-coordinator.test.ts connection-health-refresh-flow.test.ts use-connection-health-race.test.ts"

  run_step "frontend typecheck" \
    bash -c "cd '$ROOT_DIR/frontend' && npm run typecheck"
}

case "$MODE" in
  core|connection-health)
    run_connection_health_gate
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

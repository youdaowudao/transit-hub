#!/usr/bin/env bash
set -Eeuo pipefail
chanshui_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$chanshui_root"
go -C chanshui test -p=1 ./internal/hub/... -count=1
go -C chanshui test -p=1 ./internal/api -run '^TestHub' -count=1
TRANSITHUB_CHANSHUI_RESOURCE_TEST=0 go -C backend test -p=1 ./internal/modules/chanshui -count=1
go -C backend test -p=1 ./internal/modules/connection_health -run 'Test.*Audit' -count=1
npm --prefix frontend run test -- chanshui
npm --prefix chanshui/web test
if [[ "${TRANSITHUB_CHANSHUI_RESOURCE_TEST:-}" == "1" ]]; then
  go -C backend test -p=1 ./internal/modules/chanshui -run '^TestChanshuiActual' -count=1 -v
fi

#!/usr/bin/env bash
# Build on a development/build machine. This script never installs or starts services.
set -Eeuo pipefail
chanshui_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export GOTOOLCHAIN=local
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOCACHE="${GOCACHE:-$chanshui_root/backend/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$chanshui_root/backend/.cache/go-mod}"
export GOPATH="${GOPATH:-$chanshui_root/backend/.cache/gopath}"
export GOTMPDIR="${GOTMPDIR:-$chanshui_root/backend/.cache/tmp}"
export TMPDIR="$chanshui_root/.cache/chanshui-build/tmp"
export npm_config_cache="$chanshui_root/.cache/npm"
export NEXT_TELEMETRY_DISABLED=1
export NEXT_PUBLIC_TRANSITHUB=1
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$GOTMPDIR" "$TMPDIR" "$chanshui_root/chanshui/.artifacts/bin"
cd "$chanshui_root/chanshui"
CGO_ENABLED=0 go build -p=1 -trimpath -o .artifacts/bin/transithub-chanshui ./cmd/transit-hub
go run -p=1 ./cmd/hub-manifest > .artifacts/bank-manifest.json
cd web
if [[ "${CHANSHUI_SKIP_NPM_CI:-0}" != "1" ]]; then
  npm ci --registry=https://registry.npmjs.org
fi
npm run build
python3 - "$chanshui_root" <<'PY'
from pathlib import Path
import shutil, sys
root = Path(sys.argv[1]).resolve()
web = root / 'chanshui/web'
# Next's distDir is its compiler output; only the exported HTML is deployable.
source = web / 'out'
assert (source / 'index.html').is_file(), 'Next没有生成out/index.html，未替换现有静态页面'
assert (source / '_next').is_dir(), '静态导出缺少_next目录，未替换现有静态页面'
target = web / 'out-build'
assert target.parent == web and web.is_relative_to(root)
if target.exists():
    shutil.rmtree(target)
shutil.copytree(source, target)
shutil.copyfile(root / 'chanshui/.artifacts/bank-manifest.json', target / 'bank-manifest.json')
print('构建完成：chanshui/.artifacts/bin/transithub-chanshui 和 chanshui/web/out-build；尚未部署或启用。')
PY

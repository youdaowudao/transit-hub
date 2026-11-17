# Canonical function source. The marked copies in both maintenance scripts are
# generated from this file; installed maintenance scripts never source a helper.
chanshui_pair_root=''
chanshui_pair_stage=''
chanshui_pair_backup=''
chanshui_stage_ready=0
chanshui_stage_created=0
chanshui_backup_created=0
chanshui_bin_backup_ready=0
chanshui_web_backup_ready=0
chanshui_bin_switched=0
chanshui_web_switched=0

chanshui_require_commands() {
    local name
    local missing=()
    for name in "$@"; do
        command -v "$name" >/dev/null 2>&1 || missing+=("$name")
    done
    if (("${#missing[@]}" > 0)); then
        printf '检测构建失败：找不到必需命令：%s。请安装后重新执行整站升级，本次尚未替换检测文件。\n' "${missing[*]}" >&2
        return 69
    fi
}

chanshui_acquire_build_lock() {
    local root="$1"
    chanshui_require_commands flock || return $?
    mkdir -p "$root/.cache"
    if ! exec 8>"$root/.cache/chanshui-build.lock"; then
        printf '检测构建失败：无法打开项目内构建锁，请检查.cache目录权限。\n' >&2
        return 73
    fi
    if ! flock -n 8; then
        printf '另一项整站构建或升级正在处理检测文件，本次未替换检测产物，请等待完成后重试。\n' >&2
        return 75
    fi
}

# Only exact generated-artifact locations are removable. No data directory is
# accepted, including when restoring a pair which did not exist beforehand.
chanshui_remove_artifact() {
    python3 - "$1" "$2" <<'PY'
from pathlib import Path
import shutil, sys
root = Path(sys.argv[1]).absolute()
target = Path(sys.argv[2]).absolute()
allowed = {
    root / 'chanshui/.artifacts/build.next',
    root / 'chanshui/.artifacts/build.previous',
    root / 'chanshui/.artifacts/bin/transithub-chanshui',
    root / 'chanshui/web/out-build',
    root / 'chanshui/web/out',
}
if target not in allowed:
    raise SystemExit('拒绝清理非本次检测产物路径：' + str(target))
if target.is_symlink() or target.is_file():
    target.unlink()
elif target.exists():
    shutil.rmtree(target)
PY
}

chanshui_build_stage() {
    local root="$1" stage="$2"
    chanshui_pair_root="$root"
    chanshui_pair_stage="$stage"
    chanshui_pair_backup="$root/chanshui/.artifacts/build.previous"
    chanshui_stage_ready=0
    chanshui_stage_created=0
    chanshui_backup_created=0
    chanshui_bin_backup_ready=0
    chanshui_web_backup_ready=0
    chanshui_bin_switched=0
    chanshui_web_switched=0
    if [[ ! -f "$root/chanshui/cmd/transit-hub/main.go" ]]; then
        printf '目标版本没有模型检测源码，保留已有检测文件和报告，不执行检测构建。\n'
        return 0
    fi
    chanshui_require_commands go node npm python3 || return $?
    if [[ "$stage" != "$root/chanshui/.artifacts/build.next" ]]; then
        printf '检测构建失败：暂存路径不符合固定项目路径，尚未替换检测文件。\n' >&2
        return 65
    fi
    if [[ -e "$chanshui_pair_backup" || -L "$chanshui_pair_backup" ]]; then
        printf '检测构建已停止：上次旧产物备份仍位于%s，请先核查并恢复该备份；本次不会覆盖它。\n' "$chanshui_pair_backup" >&2
        return 65
    fi
    chanshui_stage_created=1
    chanshui_remove_artifact "$root" "$stage"
    mkdir -p "$stage" "$root/chanshui/.artifacts/go-tmp" "$root/.cache/npm"
    export GOTOOLCHAIN=local
    export GOCACHE="${GOCACHE:-$root/backend/.cache/go-build}"
    export GOMODCACHE="${GOMODCACHE:-$root/backend/.cache/go-mod}"
    export GOPATH="${GOPATH:-$root/backend/.cache/gopath}"
    export GOTMPDIR="${GOTMPDIR:-$root/chanshui/.artifacts/go-tmp}"
    export TMPDIR="${TMPDIR:-$root/chanshui/.artifacts/go-tmp}"
    export npm_config_cache="${npm_config_cache:-$root/.cache/npm}"
    export NEXT_PUBLIC_TRANSITHUB=1
    export NEXT_TELEMETRY_DISABLED=1
    mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$GOTMPDIR" "$TMPDIR"
    printf '正在构建模型检测程序（单包并发1），尚未替换检测文件。\n'
    (
        trap - ERR
        cd "$root/chanshui"
        GOMAXPROCS=2 GOPROXY=https://proxy.golang.org,direct CGO_ENABLED=0 go build -p=1 -trimpath -o "$stage/transithub-chanshui" ./cmd/transit-hub
        GOMAXPROCS=2 GOPROXY=https://proxy.golang.org,direct go run -p=1 ./cmd/hub-manifest >"$stage/bank-manifest.json"
    )
    printf '正在构建模型检测静态页面，完成后继续整站构建。\n'
    chanshui_remove_artifact "$root" "$root/chanshui/web/out"
    (
        trap - ERR
        cd "$root/chanshui/web"
        npm ci --registry=https://registry.npmjs.org
        npm run build
    )
    python3 - "$root" "$stage" <<'PY'
from pathlib import Path
import json, shutil, sys
root, stage = map(Path, sys.argv[1:])
source = root / 'chanshui/web/out'
if not (stage / 'transithub-chanshui').is_file() or not (stage / 'transithub-chanshui').stat().st_mode & 0o111:
    raise SystemExit('检测构建失败：暂存中没有可执行的检测程序，未替换现有文件')
if not (source / 'index.html').is_file() or not (source / '_next').is_dir():
    raise SystemExit('检测页面导出不完整，缺少index.html或_next，未替换现有文件')
json.loads((stage / 'bank-manifest.json').read_text())
shutil.copytree(source, stage / 'web')
shutil.copyfile(stage / 'bank-manifest.json', stage / 'web/bank-manifest.json')
PY
    chanshui_stage_ready=1
}

chanshui_publish_pair() {
    if ((chanshui_stage_ready == 0)); then return 0; fi
    local binary="$chanshui_pair_root/chanshui/.artifacts/bin/transithub-chanshui"
    local web="$chanshui_pair_root/chanshui/web/out-build"
    chanshui_backup_created=1
    mkdir -p "$chanshui_pair_backup" "$(dirname "$binary")"
    if [[ -e "$binary" || -L "$binary" ]]; then
        chanshui_bin_backup_ready=1
        mv -f "$binary" "$chanshui_pair_backup/transithub-chanshui"
    fi
    if [[ -e "$web" || -L "$web" ]]; then
        chanshui_web_backup_ready=1
        mv -f "$web" "$chanshui_pair_backup/web"
    fi
    chanshui_bin_switched=1
    mv -f "$chanshui_pair_stage/transithub-chanshui" "$binary"
    chanshui_web_switched=1
    mv -f "$chanshui_pair_stage/web" "$web"
}

chanshui_restore_pair() {
    local failure=0
    local binary="$chanshui_pair_root/chanshui/.artifacts/bin/transithub-chanshui"
    local web="$chanshui_pair_root/chanshui/web/out-build"
    if ((chanshui_bin_switched == 0)) || chanshui_remove_artifact "$chanshui_pair_root" "$binary"; then
        if ((chanshui_bin_backup_ready == 1)) && [[ -e "$chanshui_pair_backup/transithub-chanshui" || -L "$chanshui_pair_backup/transithub-chanshui" ]]; then
            mv -fT "$chanshui_pair_backup/transithub-chanshui" "$binary" || failure=1
        fi
    else
        failure=1
    fi
    if ((chanshui_web_switched == 0)) || chanshui_remove_artifact "$chanshui_pair_root" "$web"; then
        if ((chanshui_web_backup_ready == 1)) && [[ -e "$chanshui_pair_backup/web" || -L "$chanshui_pair_backup/web" ]]; then
            mv -fT "$chanshui_pair_backup/web" "$web" || failure=1
        fi
    else
        failure=1
    fi
    if ((failure != 0)); then
        printf '检测产物恢复失败：请检查%s及稳定产物路径的权限；旧备份已保留，不能重复覆盖。\n' "$chanshui_pair_backup" >&2
        return 1
    fi
    if ((chanshui_backup_created == 1)); then
        chanshui_remove_artifact "$chanshui_pair_root" "$chanshui_pair_backup"
    fi
    chanshui_backup_created=0
    chanshui_bin_backup_ready=0
    chanshui_web_backup_ready=0
    chanshui_bin_switched=0
    chanshui_web_switched=0
}

chanshui_commit_pair() {
    # Once health passed, cleanup failure must never roll back valid files.
    chanshui_bin_backup_ready=0
    chanshui_web_backup_ready=0
    chanshui_bin_switched=0
    chanshui_web_switched=0
    if ((chanshui_backup_created == 1)); then
        chanshui_backup_created=0
        chanshui_remove_artifact "$chanshui_pair_root" "$chanshui_pair_backup"
    fi
}

chanshui_cleanup_stage() {
    if ((chanshui_stage_created == 1)); then
        chanshui_remove_artifact "$chanshui_pair_root" "$chanshui_pair_stage"
    fi
}

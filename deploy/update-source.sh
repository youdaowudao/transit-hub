#!/usr/bin/env bash

set -Eeuo pipefail

PROJECT_DIR='/opt/transithub'
BACKUP_DIR="$PROJECT_DIR/backups"
BACKUP_FILE="$BACKUP_DIR/transithub.dump"
BACKUP_NEXT="$PROJECT_DIR/.transithub.dump.next"
CONTAINER_BACKUP='/tmp/transithub.dump.next'
POSTGRES_CONTAINER='transithub-postgres'
SERVICE='transithub-api.service'
HEALTH_URL='http://127.0.0.1:10621/api/health'
STATE_DIR='/var/lib/transithub'
ROLLBACK_POINT_FILE="$STATE_DIR/rollback-point.json"
VERIFIED_COMMIT_FILE="$STATE_DIR/verified-commit.json"
LOCK_FILE='/run/lock/transithub-maintenance.lock'
SYSTEMD_UNIT_DIR='/etc/systemd/system'
MAINTENANCE_SCRIPTS=(
    'update-source.sh'
    'run-source-upgrade.sh'
    'rollback-source.sh'
    'run-source-rollback.sh'
    'run-service-restart.sh'
)
MAINTENANCE_UNITS=(
    'transithub-upgrade.service'
    'transithub-rollback.service'
    'transithub-restart.service'
)

# BEGIN GENERATED CHANSHUI BUILD FUNCTIONS
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
        if [[ "${CHANSHUI_SKIP_NPM_CI:-0}" != "1" ]]; then
            npm ci --registry=https://registry.npmjs.org
        fi
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
# END GENERATED CHANSHUI BUILD FUNCTIONS

# 升级前所在提交、是否已切换源码、是否已开始改写线上产物。
# 失败时据此决定能否把源码退回原提交，见 restore_source_before_artifacts。
previous_commit=''
source_switched=0
artifacts_touched=0

on_error() {
    local exit_code=$?
    trap - ERR
    trap '' INT TERM HUP
    printf '升级失败（第 %s 行，退出码 %s）：%s\n' "$1" "$exit_code" "$2" >&2
    if [[ "$2" == *systemctl* || "$2" == *curl* || "$2" == *health_response* ]]; then
        print_service_diagnostics
    fi
    chanshui_restore_pair || true
    restore_source_before_artifacts
    exit "$exit_code"
}

print_service_diagnostics() {
    sudo systemctl status "$SERVICE" --no-pager -l >&2 || true
    sudo journalctl -u "$SERVICE" -n 100 --no-pager >&2 || true
}

# 切换源码之后、改写前端 dist 之前失败（备份、npm ci 等）时，线上服务和产物都还是旧版，
# 把源码退回原提交，保持「工作树 = 正在运行的版本」。否则下次重试会把这次未验证的新提交
# 当作还原点写入，回滚就退不回真正的旧版本。已开始改写产物后不再退回，交给一键回滚处理。
restore_source_before_artifacts() {
    if ((source_switched == 0 || artifacts_touched == 1)) || [[ -z "$previous_commit" ]]; then
        return 0
    fi
    if git -C "$PROJECT_DIR" switch --detach "$previous_commit" >/dev/null 2>&1; then
        printf '已把源码退回升级前的提交 %s，线上服务未受影响，处理完上述问题后可直接重新升级。\n' \
            "$previous_commit" >&2
    else
        printf '源码未能退回升级前的提交 %s，重新升级前请先在服务器执行：git -C %s switch --detach %s\n' \
            "$previous_commit" "$PROJECT_DIR" "$previous_commit" >&2
    fi
}

# systemd 以固定 PATH 拉起升级服务，不会加载登录 shell 里的 nvm 等环境。
# 在写还原点、切换代码之前检查必需命令，缺失时直接退出，代码和数据库都不动。
require_commands() {
    local name
    local missing=()
    for name in "$@"; do
        if ! command -v "$name" >/dev/null 2>&1; then
            missing+=("$name")
        fi
    done
    if ((${#missing[@]} == 0)); then
        return 0
    fi
    printf '升级失败：升级服务找不到以下命令：%s。\n' "${missing[*]}" >&2
    printf '升级服务只在这些目录里找命令：%s（不会加载登录 shell 里的 nvm 等环境）。\n' "$PATH" >&2
    printf '处理办法：把缺少的命令安装到 /usr/bin 或 /usr/local/bin；Node.js 若用 nvm 安装，可用 root 执行 ln -sf "$(command -v node)" /usr/local/bin/node 和 ln -sf "$(command -v npm)" /usr/local/bin/npm。\n' >&2
    printf '本次升级尚未改动代码和数据库，处理后直接重新升级即可。\n' >&2
    exit 69
}

# 把仓库内 deploy/ 下的维护脚本与 systemd 单元同步到仓库外的执行位置。
#
# 必须用「写 .next + mv -f」而非直接 cp/tee 覆盖：本脚本自身与 run-source-upgrade.sh
# 此刻正在运行，而 bash 是按文件偏移流式读取脚本的。原地覆盖会让正在执行的脚本
# 从中途读到新内容并崩溃；mv -f 是原子改名，换的是新 inode，已打开的 fd 继续读
# 旧 inode，运行中的进程不受影响，新内容从下一次执行起生效。
#
# 单元文件仅在内容真正变化时才 daemon-reload，避免每次升级都无谓重载。
sync_maintenance_assets() {
    local source_dir="$PROJECT_DIR/deploy"
    local name src dst units_changed=0

    for name in "${MAINTENANCE_SCRIPTS[@]}"; do
        src="$source_dir/$name"
        dst="$PROJECT_DIR/$name"
        [[ -f "$src" ]] || continue
        sudo cp -f "$src" "$dst.next"
        sudo chmod 0755 "$dst.next"
        sudo mv -f "$dst.next" "$dst"
    done

    for name in "${MAINTENANCE_UNITS[@]}"; do
        src="$source_dir/$name"
        dst="$SYSTEMD_UNIT_DIR/$name"
        [[ -f "$src" ]] || continue
        if sudo cmp -s "$src" "$dst"; then
            continue
        fi
        sudo cp -f "$src" "$dst.next"
        sudo chmod 0644 "$dst.next"
        sudo mv -f "$dst.next" "$dst"
        units_changed=1
    done

    if ((units_changed == 1)); then
        sudo systemctl daemon-reload
    fi
}

prepare_go_environment() {
    export HOME="${HOME:-/root}"
    export GOPATH="${GOPATH:-$HOME/go}"
    export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
    export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
    install -d -m 0755 "$GOPATH" "$GOMODCACHE" "$GOCACHE"
}

wait_for_health() {
    local attempts=60
    local attempt
    local health_response
    for ((attempt = 1; attempt <= attempts; attempt++)); do
        if health_response="$(curl -fsS "$HEALTH_URL" 2>/dev/null)"; then
            if printf '%s' "$health_response" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"'; then
                printf '%s\n' "$health_response"
                return 0
            fi
        fi
        sleep 1
    done

    printf '升级失败：健康接口在 %s 秒内未返回 status=ok。\n' "$attempts" >&2
    return 1
}

cleanup() {
    chanshui_cleanup_stage || true
    sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP" >/dev/null 2>&1 || true
    sudo rm -f "$BACKUP_NEXT" >/dev/null 2>&1 || true
}

acquire_maintenance_lock() {
    if ! command -v flock >/dev/null 2>&1; then
        printf '升级失败：服务器缺少 flock 命令。\n' >&2
        exit 127
    fi
    if ! exec 9>"$LOCK_FILE"; then
        printf '升级失败：无法打开维护锁文件。\n' >&2
        exit 73
    fi
    if ! flock -n 9; then
        printf '升级失败：回滚、后台服务重启或其他维护任务正在执行。\n' >&2
        exit 75
    fi
}

# 读取库内已应用的最大迁移版本号，供回滚时判定 schema 是否发生变化。
# schema_migrations.version 是 TEXT，存去掉 .sql 的完整文件名；文件名带零填充的
# 定宽数字前缀，字典序与数值序一致，所以取 MAX 后截出数字前缀即为迁移序号。
# 读取失败时回落到 0，避免升级流程因为还原点元数据而中断。
read_schema_version() {
    local raw
    raw="$(sudo docker exec "$POSTGRES_CONTAINER" psql \
        --username=postgres \
        --dbname=transithub \
        --no-align --tuples-only --quiet \
        --command="SELECT COALESCE(MAX(version), '000000') FROM schema_migrations;" 2>/dev/null |
        tr -d '[:space:]')" || raw=''
    raw="${raw%%_*}"
    if [[ ! "$raw" =~ ^[0-9]+$ ]]; then
        printf '0'
        return 0
    fi
    printf '%s' "$((10#$raw))"
}

write_json_atomic() {
    local target="$1"
    local payload="$2"
    local next="$target.next"

    printf '%s\n' "$payload" | sudo tee "$next" >/dev/null
    sudo chmod 0644 "$next"
    sudo mv -f "$next" "$target"
}

# 切换代码前记录还原点：此刻是取到旧提交号的唯一时机。
capture_rollback_point() {
    local commit="$1"
    local version="$2"
    local schema_version="$3"

    sudo install -d -m 0755 "$STATE_DIR"
    write_json_atomic "$ROLLBACK_POINT_FILE" "$(
        printf '{"commit":"%s","version":"%s","schemaVersion":%s,"dumpPath":"%s","capturedAt":"%s"}' \
            "$commit" "$version" "${schema_version:-0}" "$BACKUP_FILE" \
            "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
    )"
}

# 升级通过健康检查后，把新提交标记为已验证点，作为下次回滚的可信目标。
mark_verified_commit() {
    local commit="$1"
    local version="$2"
    local schema_version="$3"

    sudo install -d -m 0755 "$STATE_DIR"
    write_json_atomic "$VERIFIED_COMMIT_FILE" "$(
        printf '{"commit":"%s","version":"%s","schemaVersion":%s,"verifiedAt":"%s"}' \
            "$commit" "$version" "${schema_version:-0}" \
            "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
    )"
}

read_app_version() {
    local dir="$1"
    sed -n 's/^[[:space:]]*defaultAppVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' \
        "$dir/backend/internal/config/config.go" | head -n 1
}

on_signal() {
    trap - ERR
    trap '' INT TERM HUP
    printf '升级失败：收到 %s 信号，已停止执行。\n' "$1" >&2
    chanshui_restore_pair || true
    restore_source_before_artifacts
    exit "$2"
}

trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap cleanup EXIT
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

acquire_maintenance_lock
require_commands git sudo docker curl node npm go python3
chanshui_acquire_build_lock "$PROJECT_DIR"

cd "$PROJECT_DIR"
git fetch origin main

previous_commit="$(git rev-parse HEAD)"
previous_version="$(read_app_version "$PROJECT_DIR")"
previous_schema_version="$(read_schema_version)"

# 还原点必须在 git switch 之前写入：切换后若升级失败，工作树已漂移到新提交，
# 下次升级如果此时才写还原点，记录的将是未经验证的提交。
# dumpPath 指向固定路径，后续 pg_dump 会写入该路径，此处先行记录不影响正确性。
capture_rollback_point "$previous_commit" "$previous_version" "$previous_schema_version"

source_switched=1
git switch --detach origin/main

sudo mkdir -p "$BACKUP_DIR"
sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP"
sudo docker exec "$POSTGRES_CONTAINER" pg_dump \
    --username=postgres \
    --dbname=transithub \
    --format=custom \
    --file="$CONTAINER_BACKUP"
sudo rm -f "$BACKUP_NEXT"
sudo docker cp "$POSTGRES_CONTAINER:$CONTAINER_BACKUP" "$BACKUP_NEXT"
sudo test -s "$BACKUP_NEXT"
sudo mv -f "$BACKUP_NEXT" "$BACKUP_FILE"
sudo find "$BACKUP_DIR" -maxdepth 1 -type f -name '*.dump' \
    ! -name 'transithub.dump' ! -name 'transithub.pre-rollback.dump' -delete
sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP"

chanshui_build_stage "$PROJECT_DIR" "$PROJECT_DIR/chanshui/.artifacts/build.next"

cd "$PROJECT_DIR/frontend"
npm ci --registry=https://registry.npmjs.org
# npm run build 会直接改写线上前端 dist，从这里起失败不再退回源码。
artifacts_touched=1
npm --ignore-scripts run build

cd "$PROJECT_DIR/backend"
prepare_go_environment
GOPROXY=https://proxy.golang.org,direct CGO_ENABLED=0 go build \
    -o "$PROJECT_DIR/transithub-api.next" \
    ./cmd/api
test -x "$PROJECT_DIR/transithub-api.next"
chanshui_publish_pair
mv -f "$PROJECT_DIR/transithub-api.next" "$PROJECT_DIR/transithub-api"

sudo systemctl restart "$SERVICE"
sudo systemctl is-active --quiet "$SERVICE"

health_response="$(trap - ERR; wait_for_health)"
printf '%s\n' "$health_response"
chanshui_commit_pair

mark_verified_commit \
    "$(git -C "$PROJECT_DIR" rev-parse HEAD)" \
    "$(read_app_version "$PROJECT_DIR")" \
    "$(read_schema_version)"

# 放在健康检查与已验证点之后：升级中途失败时不应把新版维护脚本留在执行位置，
# 否则回滚要依赖的恰好是这批未经验证的脚本。
sync_maintenance_assets

sudo journalctl -u "$SERVICE" -n 100 --no-pager
printf '升级成功：源码已更新，服务已重启并通过健康检查。\n'

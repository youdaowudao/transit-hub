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

on_error() {
    local exit_code=$?
    printf '升级失败（第 %s 行，退出码 %s）：%s\n' "$1" "$exit_code" "$2" >&2
    if [[ "$2" == *systemctl* || "$2" == *curl* || "$2" == *health_response* ]]; then
        print_service_diagnostics
    fi
    exit "$exit_code"
}

print_service_diagnostics() {
    sudo systemctl status "$SERVICE" --no-pager -l >&2 || true
    sudo journalctl -u "$SERVICE" -n 100 --no-pager >&2 || true
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
    printf '升级失败：收到 %s 信号，已停止执行。\n' "$1" >&2
    exit "$2"
}

trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap cleanup EXIT
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

acquire_maintenance_lock

cd "$PROJECT_DIR"
git fetch origin main

previous_commit="$(git rev-parse HEAD)"
previous_version="$(read_app_version "$PROJECT_DIR")"
previous_schema_version="$(read_schema_version)"

# 还原点必须在 git switch 之前写入：切换后若升级失败，工作树已漂移到新提交，
# 下次升级如果此时才写还原点，记录的将是未经验证的提交。
# dumpPath 指向固定路径，后续 pg_dump 会写入该路径，此处先行记录不影响正确性。
capture_rollback_point "$previous_commit" "$previous_version" "$previous_schema_version"

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

cd "$PROJECT_DIR/frontend"
npm ci --registry=https://registry.npmmirror.com
npm run build

cd "$PROJECT_DIR/backend"
prepare_go_environment
GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0 go build \
    -o "$PROJECT_DIR/transithub-api.next" \
    ./cmd/api
test -x "$PROJECT_DIR/transithub-api.next"
mv -f "$PROJECT_DIR/transithub-api.next" "$PROJECT_DIR/transithub-api"

sudo systemctl restart "$SERVICE"
sudo systemctl is-active --quiet "$SERVICE"

health_response="$(wait_for_health)"
printf '%s\n' "$health_response"

mark_verified_commit \
    "$(git -C "$PROJECT_DIR" rev-parse HEAD)" \
    "$(read_app_version "$PROJECT_DIR")" \
    "$(read_schema_version)"

# 放在健康检查与已验证点之后：升级中途失败时不应把新版维护脚本留在执行位置，
# 否则回滚要依赖的恰好是这批未经验证的脚本。
sync_maintenance_assets

sudo journalctl -u "$SERVICE" -n 100 --no-pager
printf '升级成功：源码已更新，服务已重启并通过健康检查。\n'

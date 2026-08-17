#!/usr/bin/env bash

# 源码版本一键回滚：把 /opt/transithub 退回上一次升级前记录的还原点。
# 只处理版本回退，不做数据库整体还原：迁移未变化时数据库一行不动，
# 迁移有新增但均为增量式时保留新结构，检测到破坏性迁移则直接拒绝执行。

set -Eeuo pipefail

PROJECT_DIR='/opt/transithub'
BACKUP_DIR="$PROJECT_DIR/backups"
PRE_ROLLBACK_DUMP="$BACKUP_DIR/transithub.pre-rollback.dump"
PRE_ROLLBACK_NEXT="$PROJECT_DIR/.transithub.pre-rollback.dump.next"
CONTAINER_BACKUP='/tmp/transithub.pre-rollback.dump.next'
POSTGRES_CONTAINER='transithub-postgres'
SERVICE='transithub-api.service'
HEALTH_URL='http://127.0.0.1:10621/api/health'
HEALTH_TIMEOUT_SECONDS=180
STATE_DIR='/var/lib/transithub'
ROLLBACK_POINT_FILE="$STATE_DIR/rollback-point.json"
VERIFIED_COMMIT_FILE="$STATE_DIR/verified-commit.json"
LOCK_FILE='/run/lock/transithub-maintenance.lock'
MIGRATIONS_DIR="$PROJECT_DIR/backend/internal/database/migrations"
BINARY="$PROJECT_DIR/transithub-api"
BINARY_PREVIOUS="$PROJECT_DIR/transithub-api.previous"
DIST_DIR="$PROJECT_DIR/frontend/dist"
DIST_PREVIOUS="$PROJECT_DIR/frontend/dist.previous"

# 记录回滚前的 HEAD 与产物是否已切换，供失败时原样恢复。
origin_commit=''
artifacts_switched=0

# 回滚目标自身起不来时，把服务恢复到回滚前的可用状态。
# 只回退代码与产物，不动数据库：本方案自始不做数据库还原，
# 且 A/B 档的前提就是新结构对旧代码兼容，反向同样成立。
restore_previous_artifacts() {
    local restore_error=''
    printf '正在恢复回滚前的版本，请稍候。\n' >&2

    if ((artifacts_switched == 1)); then
        if [[ -x "$BINARY_PREVIOUS" ]]; then
            if ! mv -f "$BINARY_PREVIOUS" "$BINARY"; then
                restore_error="${restore_error:+$restore_error; }binary artifact restore failed"
            fi
        fi
        if [[ -d "$DIST_PREVIOUS" ]]; then
            if ! rm -rf "$DIST_DIR"; then
                restore_error="${restore_error:+$restore_error; }frontend dist cleanup failed"
            elif ! mv -f "$DIST_PREVIOUS" "$DIST_DIR"; then
                restore_error="${restore_error:+$restore_error; }frontend dist restore failed"
            fi
        fi
    fi

    if [[ -n "$origin_commit" ]]; then
        if ! git -C "$PROJECT_DIR" switch --detach "$origin_commit"; then
            restore_error="${restore_error:+$restore_error; }source commit restore failed"
        fi
    fi

    if sudo systemctl restart "$SERVICE"; then
        if wait_for_health >/dev/null 2>&1; then
            if [[ -z "$restore_error" ]]; then
                printf '已恢复到回滚前的版本，服务通过健康检查。原回滚失败原因见上。\n' >&2
                return 0
            fi
        else
            restore_error="${restore_error:+$restore_error; }wait_for_health exit=$?"
        fi
    else
        restore_error="${restore_error:+$restore_error; }systemctl restart exit=$?"
    fi

    printf '自动恢复失败：恢复错误：%s；服务当前不可用，必须人工介入。原回滚失败原因见上。\n' \
        "$restore_error" >&2
    return 1
}

on_error() {
    local exit_code=$?
    printf '回滚失败（第 %s 行，退出码 %s）：%s\n' "$1" "$exit_code" "$2" >&2
    if [[ "$2" == *systemctl* || "$2" == *curl* || "$2" == *health_response* ]]; then
        print_service_diagnostics
    fi
    trap - ERR
    restore_previous_artifacts || true
    exit "$exit_code"
}

print_service_diagnostics() {
    sudo systemctl status "$SERVICE" --no-pager -l >&2 || true
    sudo journalctl -u "$SERVICE" -n 100 --no-pager >&2 || true
}

prepare_go_environment() {
    export HOME="${HOME:-/root}"
    export GOPATH="${GOPATH:-$HOME/go}"
    export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
    export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
    install -d -m 0755 "$GOPATH" "$GOMODCACHE" "$GOCACHE"
}

wait_for_health() {
    local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
    local health_response=''

    while ((SECONDS < deadline)); do
        if health_response="$(curl -fsS --max-time 2 "$HEALTH_URL" 2>&1)"; then
            if [[ "$health_response" =~ \"status\"[[:space:]]*:[[:space:]]*\"ok\" ]]; then
                printf '%s\n' "$health_response"
                return 0
            fi
        fi
        sleep 1
    done

    printf '回滚失败：健康接口在 %s 秒内未返回 status=ok。\n' "$HEALTH_TIMEOUT_SECONDS" >&2
    if [[ -n "$health_response" ]]; then
        printf '最后一次健康检查输出：%s\n' "$health_response" >&2
    fi
    return 1
}

cleanup() {
    sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP" >/dev/null 2>&1 || true
    sudo rm -f "$PRE_ROLLBACK_NEXT" >/dev/null 2>&1 || true
    sudo rm -rf "$PROJECT_DIR/frontend/dist.next" >/dev/null 2>&1 || true
    sudo rm -f "$PROJECT_DIR/transithub-api.next" >/dev/null 2>&1 || true
}

on_signal() {
    printf '回滚失败：收到 %s 信号，已停止执行。\n' "$1" >&2
    exit "$2"
}

acquire_maintenance_lock() {
    if ! command -v flock >/dev/null 2>&1; then
        printf '回滚失败：服务器缺少 flock 命令。\n' >&2
        exit 127
    fi
    if ! exec 9>"$LOCK_FILE"; then
        printf '回滚失败：无法打开维护锁文件。\n' >&2
        exit 73
    fi
    if ! flock -n 9; then
        printf '回滚失败：源码升级、后台服务重启或其他维护任务正在执行。\n' >&2
        exit 75
    fi
}

read_json_string() {
    local file="$1"
    local key="$2"
    sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$file" | head -n 1
}

read_json_number() {
    local file="$1"
    local key="$2"
    sed -n "s/.*\"$key\"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p" "$file" | head -n 1
}

# schema_migrations.version 是 TEXT，存的是去掉 .sql 的完整文件名（如
# 000019_connection_health_priority_sync_b）。文件名带零填充的定宽数字前缀，
# 字典序与数值序一致，所以取 MAX 之后再截出数字前缀即可得到当前迁移序号。
read_schema_version() {
    local raw
    raw="$(sudo docker exec "$POSTGRES_CONTAINER" psql \
        --username=postgres \
        --dbname=transithub \
        --no-align --tuples-only --quiet \
        --command="SELECT COALESCE(MAX(version), '000000') FROM schema_migrations;" 2>/dev/null |
        tr -d '[:space:]')"
    raw="${raw%%_*}"
    [[ "$raw" =~ ^[0-9]+$ ]] || return 1
    printf '%s' "$((10#$raw))"
}
# 判定回滚档位。
# A 档 additive-none：库内最大迁移版本等于还原点版本，schema 未变化。
# B 档 additive：新增迁移全部声明为 additive，旧代码可容忍多出来的结构。
# C 档 destructive：存在破坏性迁移或缺少声明，拒绝自动回滚。
classify_rollback() {
    local point_schema="$1"
    local current_schema="$2"

    if ((current_schema == point_schema)); then
        printf 'additive-none'
        return 0
    fi

    # current_schema < point_schema：数据库迁移版本低于还原点，这不应发生——
    # 说明还原点记录来自比当前库更新的部署，直接拒绝，避免旧代码面对未知结构。
    if ((current_schema < point_schema)); then
        printf '回滚受阻：当前库迁移版本 (%s) 低于还原点 (%s)，拒绝执行。\n' \
            "$current_schema" "$point_schema" >&2
        printf 'destructive'
        return 0
    fi

    local file version marker
    local unsafe=()
    local seen=()
    for file in "$MIGRATIONS_DIR"/*.sql; do
        [[ -e "$file" ]] || continue
        version="$(basename "$file")"
        version="${version%%_*}"
        [[ "$version" =~ ^[0-9]+$ ]] || continue
        version=$((10#$version))
        ((version > point_schema && version <= current_schema)) || continue
        seen+=("$version")

        marker="$(sed -n 's/^--[[:space:]]*rollback-safe:[[:space:]]*\([a-z-]*\).*/\1/p' "$file" | head -n 1)"
        if [[ "$marker" != 'additive' ]]; then
            unsafe+=("$(basename "$file")：${marker:-缺少 rollback-safe 声明}")
        fi
    done

    # 库内已应用却找不到对应迁移文件，说明无法判断这些变更的性质，一律按破坏性处理。
    local expected=$((current_schema - point_schema))
    if ((${#seen[@]} != expected)); then
        printf '回滚受阻：区间 (%s, %s] 应有 %s 个迁移文件，实际只找到 %s 个。\n' \
            "$point_schema" "$current_schema" "$expected" "${#seen[@]}" >&2
        printf 'destructive'
        return 0
    fi

    if ((${#unsafe[@]} > 0)); then
        printf '回滚受阻：以下迁移不满足增量式回滚条件。\n' >&2
        printf '  - %s\n' "${unsafe[@]}" >&2
        printf 'destructive'
        return 0
    fi

    printf 'additive'
}

write_json_atomic() {
    local target="$1"
    local payload="$2"
    local next="$target.next"

    printf '%s\n' "$payload" | sudo tee "$next" >/dev/null
    sudo chmod 0644 "$next"
    sudo mv -f "$next" "$target"
}

mark_verified_commit() {
    local commit="$1"
    local version="$2"
    local schema_version="$3"

    sudo install -d -m 0755 "$STATE_DIR"
    write_json_atomic "$VERIFIED_COMMIT_FILE" "$(
        printf '{"commit":"%s","version":"%s","schemaVersion":%s,"verifiedAt":"%s"}' \
            "$commit" "$version" "$schema_version" \
            "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
    )"
}

read_app_version() {
    local dir="$1"
    sed -n 's/^[[:space:]]*defaultAppVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' \
        "$dir/backend/internal/config/config.go" | head -n 1
}

dump_current_database() {
    sudo mkdir -p "$BACKUP_DIR"
    sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP"
    sudo docker exec "$POSTGRES_CONTAINER" pg_dump \
        --username=postgres \
        --dbname=transithub \
        --format=custom \
        --file="$CONTAINER_BACKUP"
    sudo rm -f "$PRE_ROLLBACK_NEXT"
    sudo docker cp "$POSTGRES_CONTAINER:$CONTAINER_BACKUP" "$PRE_ROLLBACK_NEXT"
    sudo test -s "$PRE_ROLLBACK_NEXT"
    sudo mv -f "$PRE_ROLLBACK_NEXT" "$PRE_ROLLBACK_DUMP"
    sudo docker exec "$POSTGRES_CONTAINER" rm -f "$CONTAINER_BACKUP"
}
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap cleanup EXIT
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

acquire_maintenance_lock

if [[ ! -s "$ROLLBACK_POINT_FILE" ]]; then
    printf '回滚失败：当前没有可用还原点，请先通过一键升级产生一次还原点。\n' >&2
    exit 66
fi

target_commit="$(read_json_string "$ROLLBACK_POINT_FILE" 'commit')"
target_version="$(read_json_string "$ROLLBACK_POINT_FILE" 'version')"
point_schema_version="$(read_json_number "$ROLLBACK_POINT_FILE" 'schemaVersion')"

# schemaVersion 缺失时默认 0 会把所有迁移判为 B/C 档，产生误判。
# 仅当还原点记录明确存在该字段时才继续；否则说明还原点来自旧版本脚本，拒绝回滚。
if [[ ! "$point_schema_version" =~ ^[0-9]+$ ]]; then
    printf '回滚失败：还原点中 schemaVersion 字段缺失或无效（值=%s）。\n' \
        "${point_schema_version:-（空）}" >&2
    printf '请先执行一次升级以生成包含完整元数据的还原点，再尝试回滚。\n' >&2
    exit 65
fi

if [[ ! "$target_commit" =~ ^[0-9a-f]{7,40}$ ]]; then
    printf '回滚失败：还原点记录的提交号无效（%s）。\n' "$target_commit" >&2
    exit 65
fi

cd "$PROJECT_DIR"

if ! git cat-file -e "$target_commit^{commit}" 2>/dev/null; then
    printf '回滚失败：本地仓库中不存在还原点提交 %s。\n' "$target_commit" >&2
    exit 65
fi

current_commit="$(git rev-parse HEAD)"
if [[ "$current_commit" == "$target_commit" ]]; then
    printf '回滚失败：当前代码已经处于还原点 %s，无需回滚。\n' "$target_commit" >&2
    exit 64
fi

current_schema_version="$(read_schema_version)"
if [[ ! "$current_schema_version" =~ ^[0-9]+$ ]]; then
    printf '回滚失败：无法读取数据库当前迁移版本。\n' >&2
    exit 70
fi

# 判定必须在切回旧代码之前完成：新增的迁移文件只存在于当前（新）提交中。
rollback_tier="$(classify_rollback "$point_schema_version" "$current_schema_version")"
printf '回滚判定：还原点 schema=%s，当前 schema=%s，档位=%s。\n' \
    "$point_schema_version" "$current_schema_version" "$rollback_tier"

if [[ "$rollback_tier" == 'destructive' ]]; then
    printf '回滚失败：检测到破坏性数据库变更，一键回滚已拒绝执行。\n' >&2
    printf '请人工评估后手工处理，避免旧代码在新结构上运行导致故障。\n' >&2
    exit 78
fi

dump_current_database

# 构建失败也要能切回原提交，因此在 switch 之前记录当前 HEAD。
origin_commit="$(git rev-parse HEAD)"

git switch --detach "$target_commit"

cd "$PROJECT_DIR/frontend"
npm ci --registry=https://registry.npmmirror.com
npm run build -- --outDir dist.next --emptyOutDir
test -f "$PROJECT_DIR/frontend/dist.next/index.html"

cd "$PROJECT_DIR/backend"
prepare_go_environment
GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0 go build \
    -o "$PROJECT_DIR/transithub-api.next" \
    ./cmd/api
test -x "$PROJECT_DIR/transithub-api.next"

# 前后端产物都构建成功后才落位，避免构建中途失败留下半新半旧的组合。
# 二进制与 dist 都先留一份，回滚目标起不来时 restore_previous_artifacts 依赖它们。
rm -rf "$DIST_PREVIOUS"
rm -f "$BINARY_PREVIOUS"
if [[ -d "$DIST_DIR" ]]; then
    mv -f "$DIST_DIR" "$DIST_PREVIOUS"
fi
if [[ -x "$BINARY" ]]; then
    cp -p "$BINARY" "$BINARY_PREVIOUS"
fi
mv -f "$PROJECT_DIR/frontend/dist.next" "$DIST_DIR"
mv -f "$PROJECT_DIR/transithub-api.next" "$BINARY"
artifacts_switched=1

sudo systemctl restart "$SERVICE"
sudo systemctl is-active --quiet "$SERVICE"

health_response="$(wait_for_health)"
printf '%s\n' "$health_response"

# 健康检查通过后才丢弃回滚前的产物。
rm -rf "$DIST_PREVIOUS"
rm -f "$BINARY_PREVIOUS"

# 更新已验证点：下次升级用它生成还原点，若不更新则下次会把刚被回滚的坏版本记录为还原点。
mark_verified_commit "$target_commit" "$target_version" "$point_schema_version"

sudo journalctl -u "$SERVICE" -n 100 --no-pager
printf '回滚成功：源码已退回 %s（%s），服务已重启并通过健康检查。\n' \
    "$target_version" "$target_commit"

#!/usr/bin/env bash

# 由 transithub-rollback.service 拉起，串行执行源码回滚并维护状态文件。
# 维护锁由 rollback-source.sh 自身持有，这里只负责状态与日志。

set -Eeuo pipefail

STATE_DIR='/var/lib/transithub'
STATUS_FILE="$STATE_DIR/rollback-status.json"
STATUS_NEXT="$STATE_DIR/rollback-status.json.next"
LOG_FILE="$STATE_DIR/rollback.log"
# 必须指向仓库外的副本：rollback-source.sh 执行期间会 git switch 重写仓库内文件，
# 若从 /opt/transithub/deploy/ 直接执行，脚本自身会被覆盖导致中途中断。
ROLLBACK_SCRIPT='/opt/transithub/rollback-source.sh'

started_at=''

write_status() {
    local state="$1"
    local finished_at="${2:-}"
    local exit_code="${3:-}"

    if [[ -n "$finished_at" ]]; then
        printf '{"state":"%s","startedAt":"%s","finishedAt":"%s","exitCode":%s}\n' \
            "$state" "$started_at" "$finished_at" "$exit_code" >"$STATUS_NEXT"
    else
        printf '{"state":"%s","startedAt":"%s"}\n' \
            "$state" "$started_at" >"$STATUS_NEXT"
    fi
    chmod 0644 "$STATUS_NEXT"
    mv -f "$STATUS_NEXT" "$STATUS_FILE"
}

# 失败原因写入日志而非状态文件：Go 侧在 failed 时从 rollback.log 读取 Output。
finish_failed() {
    local exit_code="$1"
    local message="$2"

    trap - ERR
    printf '%s\n' "$message" | tee -a "$LOG_FILE" >&2
    write_status 'failed' "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')" "$exit_code"
    exit "$exit_code"
}

on_error() {
    local exit_code=$?
    finish_failed "$exit_code" "版本回滚失败（第 $1 行）：$2"
}

on_signal() {
    finish_failed "$2" "版本回滚失败：收到 $1 信号，已停止执行。"
}

# 继承 Go 侧 Start 写入的 startedAt：reconcileStartingStatus 依据它比对是否为同一次回滚，
# 重新取值会导致该比对永久失配，starting 残留无法被兜底改写为 failed。
read_started_at() {
    local raw=''
    if [[ -f "$STATUS_FILE" ]]; then
        raw="$(sed -n 's/.*"startedAt"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$STATUS_FILE" | head -n 1)"
    fi
    if [[ -z "$raw" ]]; then
        raw="$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
    fi
    printf '%s' "$raw"
}

install -d -m 0755 "$STATE_DIR"
: >"$LOG_FILE"
chmod 0644 "$LOG_FILE"
started_at="$(read_started_at)"
write_status 'running'

trap 'rm -f "$STATUS_NEXT"' EXIT
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

if [[ ! -x "$ROLLBACK_SCRIPT" ]]; then
    finish_failed 72 '版本回滚失败：回滚脚本不存在或没有执行权限。'
fi

set +e
"$ROLLBACK_SCRIPT" >>"$LOG_FILE" 2>&1
rollback_exit_code=$?
set -e

if ((rollback_exit_code == 0)); then
    write_status 'succeeded' "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')" 0
    exit 0
fi

case "$rollback_exit_code" in
64) finish_failed 64 '当前代码已经处于还原点，无需回滚。' ;;
65) finish_failed 65 '还原点记录的提交在本地仓库中不存在，无法回滚。' ;;
66) finish_failed 66 '当前没有可用还原点，请先执行过一次一键升级。' ;;
70) finish_failed 70 '无法读取数据库当前迁移版本，回滚已中止。' ;;
73) finish_failed 73 '无法打开维护锁文件，回滚已中止。' ;;
75) finish_failed 75 '源码升级或后台服务重启正在执行，请稍后再试。' ;;
78) finish_failed 78 '检测到破坏性数据库变更，一键回滚已拒绝执行，请人工处理。' ;;
127) finish_failed 127 '服务器缺少 flock 命令，无法安全执行回滚。' ;;
*) finish_failed "$rollback_exit_code" '回滚执行失败，请查看回滚日志排查。' ;;
esac

#!/usr/bin/env bash

set -Eeuo pipefail

UPGRADE_SCRIPT='/opt/transithub/update-source.sh'
STATE_DIR='/var/lib/transithub'
STATUS_FILE="$STATE_DIR/upgrade-status.json"
STATUS_NEXT="$STATE_DIR/upgrade-status.json.next"
LOG_FILE="$STATE_DIR/upgrade.log"

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

on_signal() {
    local signal="$1"
    local exit_code="$2"
    printf '升级失败：收到 %s 信号，已停止执行。\n' "$signal" | tee -a "$LOG_FILE" >&2
    write_status 'failed' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$exit_code"
    exit "$exit_code"
}

# wrapper 自身发生错误（write_status 写入失败等）时写 failed 防止状态残留 running。
on_error() {
    local exit_code=$?
    trap - ERR
    printf '升级失败（wrapper 第 %s 行）：%s\n' "$1" "$2" | tee -a "$LOG_FILE" >&2
    write_status 'failed' "$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')" "$exit_code"
    exit "$exit_code"
}

install -d -m 0755 "$STATE_DIR"
: >"$LOG_FILE"
chmod 0644 "$LOG_FILE"
started_at="$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
write_status 'running'

trap 'rm -f "$STATUS_NEXT"' EXIT
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap 'on_signal INT 130' INT
trap 'on_signal TERM 143' TERM
trap 'on_signal HUP 129' HUP

# 维护锁由 update-source.sh 自行持有：它需要在 git 切换前抢到锁，
# 这里再持一次会因 tee 管道继承 fd 而造成锁生命周期难以判断。
# 必须放在 if 条件里执行：ERR trap 不受 set +e 约束，裸执行时升级脚本一失败就会进入
# on_error，多打一行指向 tee 的误导性错误。条件里的失败不触发 ERR，PIPESTATUS 照常保留。
if "$UPGRADE_SCRIPT" 2>&1 | tee -a "$LOG_FILE"; then
    pipeline_status=(0 0)
else
    pipeline_status=("${PIPESTATUS[@]}")
fi

exit_code="${pipeline_status[0]}"
if [[ "$exit_code" -eq 0 && "${pipeline_status[1]}" -ne 0 ]]; then
    exit_code="${pipeline_status[1]}"
fi

finished_at="$(date -u +'%Y-%m-%dT%H:%M:%S.%NZ')"
if [[ "$exit_code" -eq 0 ]]; then
    write_status 'succeeded' "$finished_at" 0
    exit 0
fi

write_status 'failed' "$finished_at" "$exit_code"
exit "$exit_code"

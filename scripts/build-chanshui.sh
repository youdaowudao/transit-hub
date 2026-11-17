#!/usr/bin/env bash
# Internal npm prebuild hook; deployment is performed by the source upgrader.
set -Eeuo pipefail
chanshui_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
source "$chanshui_root/deploy/chanshui-build.sh"

chanshui_wrapper_error() {
    local code=$?
    trap - ERR
    trap '' INT TERM HUP
    printf '整站构建中的检测步骤失败（退出码%s），正在恢复本次已替换的检测文件。\n' "$code" >&2
    chanshui_restore_pair || true
    exit "$code"
}
chanshui_wrapper_signal() {
    trap - ERR
    trap '' INT TERM HUP
    printf '检测构建收到%s信号，正在恢复本次已替换的检测文件。\n' "$1" >&2
    chanshui_restore_pair || true
    exit "$2"
}
chanshui_require_commands go node npm python3 flock
chanshui_acquire_build_lock "$chanshui_root"
trap chanshui_wrapper_error ERR
trap 'chanshui_cleanup_stage || true' EXIT
trap 'chanshui_wrapper_signal INT 130' INT
trap 'chanshui_wrapper_signal TERM 143' TERM
trap 'chanshui_wrapper_signal HUP 129' HUP
chanshui_build_stage "$chanshui_root" "$chanshui_root/chanshui/.artifacts/build.next"
chanshui_publish_pair
chanshui_commit_pair
printf '整站构建中的检测程序和静态页面已生成，继续原前端构建。\n'

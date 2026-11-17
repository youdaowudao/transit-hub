#!/usr/bin/env bash
set -Eeuo pipefail

if (($# != 4)); then
  printf 'CI结果不完整：必须提供guards、backend、frontend、chanshui四组结果。\n' >&2
  exit 64
fi

ci_names=(guards backend frontend chanshui)
ci_results=("$@")
ci_failed=0
for ci_index in 0 1 2 3; do
  if [[ "${ci_results[$ci_index]}" != success ]]; then
    printf 'CI检查%s未通过（%s），请查看该组日志；本次整体检查未通过。\n' \
      "${ci_names[$ci_index]}" "${ci_results[$ci_index]:-没有返回结果}" >&2
    ci_failed=1
  fi
done
if ((ci_failed)); then exit 1; fi
printf '四组CI检查全部通过。\n'

#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SQL_FILE="$ROOT_DIR/backend/internal/modules/connection_health/testdata/task3_question_answer_keyword_highlight_browser_fixture.sql"
ACTION="${1:-}"

case "$ACTION" in
  prepare|count|cleanup) ;;
  *)
    echo "用法: bash scripts/question-answer-keyword-highlight-fixture.sh prepare|count|cleanup" >&2
    exit 2
    ;;
esac

for required_name in TASK3_BROWSER_DATABASE_URL TASK3_BROWSER_USER_ID TASK3_BROWSER_TARGET_ID; do
  if [[ -z "${!required_name:-}" ]]; then
    echo "缺少必需环境变量: $required_name" >&2
    exit 2
  fi
done

case "$TASK3_BROWSER_DATABASE_URL" in
  postgres://*|postgresql://*) ;;
  *)
    echo "TASK3_BROWSER_DATABASE_URL 必须是 PostgreSQL URL" >&2
    exit 2
    ;;
esac

database_authority="${TASK3_BROWSER_DATABASE_URL#*://}"
database_authority="${database_authority%%/*}"
database_authority="${database_authority##*@}"
case "${database_authority,,}" in
  *,*|*%2c*)
    echo "fixture 不允许多主机 PostgreSQL URL，避免 failover 改写连接目标" >&2
    exit 2
    ;;
esac
case "$database_authority" in
  localhost|localhost:*|127.0.0.1|127.0.0.1:*|\[::1\]|\[::1\]:*) ;;
  *)
    echo "fixture 只允许连接回环 loopback PostgreSQL" >&2
    exit 2
    ;;
esac

if [[ "$TASK3_BROWSER_DATABASE_URL" == *\?* ]]; then
  database_query="${TASK3_BROWSER_DATABASE_URL#*\?}"
  database_query="${database_query%%#*}"
  IFS='&' read -r -a database_parameters <<< "$database_query"
  for database_parameter in "${database_parameters[@]}"; do
    parameter_name="${database_parameter%%=*}"
    parameter_name="${parameter_name,,}"
    if [[ "$parameter_name" == *%* ]]; then
      echo "fixture 不接受转义的数据库连接参数，以免改写连接目标" >&2
      exit 2
    fi
    case "$parameter_name" in
      host|hostaddr|service|servicefile)
        echo "fixture 不允许通过 host 或 service 参数改写数据库连接目标" >&2
        exit 2
        ;;
    esac
  done
fi

sanitized_psql=(
  env
  -u PGHOST
  -u PGHOSTADDR
  -u PGPORT
  -u PGDATABASE
  -u PGUSER
  -u PGPASSWORD
  -u PGPASSFILE
  -u PGSERVICE
  -u PGSERVICEFILE
  psql
)

if ! actual_server_addr="$(
  "${sanitized_psql[@]}" "$TASK3_BROWSER_DATABASE_URL" \
    -X -Atq -v ON_ERROR_STOP=1 \
    -c 'SELECT inet_server_addr()::text;' \
    2>/dev/null
)"; then
  echo "fixture 无法确认实际 PostgreSQL server 地址" >&2
  exit 2
fi
case "$actual_server_addr" in
  127.0.0.1|127.0.0.1/32|::1|::1/128|::ffff:127.*) ;;
  *)
    echo "fixture 实际 PostgreSQL server 不是回环地址" >&2
    exit 2
    ;;
esac

prepare=0
count=0
cleanup=0
declare "$ACTION=1"

"${sanitized_psql[@]}" "$TASK3_BROWSER_DATABASE_URL" \
  -X -Atq -v ON_ERROR_STOP=1 \
  -v "user_id=$TASK3_BROWSER_USER_ID" \
  -v "target_id=$TASK3_BROWSER_TARGET_ID" \
  -v "prepare=$prepare" \
  -v "count=$count" \
  -v "cleanup=$cleanup" \
  -f "$SQL_FILE"

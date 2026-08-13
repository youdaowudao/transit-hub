#!/usr/bin/env bash

set -euo pipefail

readonly container_name="sub2api-postgres"
readonly database_name="sub2api"
readonly database_user="sub2api"
readonly fixture_key="sk-codex-reconcile-20260813-local-only"
readonly fixture_prefix="codex-reconcile-20260813-"

run_sql() {
  docker exec -i "$container_name" psql \
    -v ON_ERROR_STOP=1 \
    -U "$database_user" \
    -d "$database_name" \
    -P pager=off
}

status_fixture() {
  run_sql <<SQL
SELECT id, account_id, request_id, actual_cost, group_id, created_at
FROM usage_logs
WHERE request_id LIKE '${fixture_prefix}%'
ORDER BY id;

SELECT id, name, group_id, status
FROM api_keys
WHERE key = '${fixture_key}';
SQL
}

ensure_fixture() {
  run_sql <<SQL
BEGIN;

DO \$\$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users WHERE id = 1) THEN
    RAISE EXCEPTION 'fixture dependency missing: user 1';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM groups WHERE id = 5) THEN
    RAISE EXCEPTION 'fixture dependency missing: group 5';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = 93) THEN
    RAISE EXCEPTION 'fixture dependency missing: account 93';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM accounts WHERE id = 73) THEN
    RAISE EXCEPTION 'fixture dependency missing: account 73';
  END IF;
END
\$\$;

INSERT INTO api_keys (user_id, key, name, group_id, status)
VALUES (1, '${fixture_key}', '${fixture_prefix}local-only', 5, 'inactive')
ON CONFLICT (key) DO NOTHING;

DO \$\$
DECLARE
  fixture_key_id bigint;
BEGIN
  SELECT id INTO fixture_key_id
  FROM api_keys
  WHERE key = '${fixture_key}'
    AND user_id = 1
    AND group_id = 5
    AND status = 'inactive';

  IF fixture_key_id IS NULL THEN
    RAISE EXCEPTION 'fixture key exists with unexpected ownership or state';
  END IF;

  INSERT INTO usage_logs (
    user_id, api_key_id, account_id, request_id, model,
    total_cost, actual_cost, created_at, group_id, account_stats_cost,
    requested_model, upstream_model
  )
  SELECT
    1, fixture_key_id, 93,
    '${fixture_prefix}bound-account-93',
    'codex-local-reconcile',
    10.00, 10.00, '2026-08-13 18:31:34.927349+08', 5, 10.00,
    'codex-local-reconcile', 'codex-local-reconcile'
  WHERE NOT EXISTS (
    SELECT 1 FROM usage_logs
    WHERE request_id = '${fixture_prefix}bound-account-93'
  );

  INSERT INTO usage_logs (
    user_id, api_key_id, account_id, request_id, model,
    total_cost, actual_cost, created_at, group_id, account_stats_cost,
    requested_model, upstream_model
  )
  SELECT
    1, fixture_key_id, 73,
    '${fixture_prefix}unconnected-account-73',
    'codex-local-reconcile',
    7.25, 7.25, '2026-08-13 18:31:34.927349+08', 5, 7.25,
    'codex-local-reconcile', 'codex-local-reconcile'
  WHERE NOT EXISTS (
    SELECT 1 FROM usage_logs
    WHERE request_id = '${fixture_prefix}unconnected-account-73'
  );
END
\$\$;

COMMIT;
SQL

  status_fixture
}

rollback_fixture() {
  run_sql <<SQL
BEGIN;

DELETE FROM usage_logs
WHERE request_id IN (
  '${fixture_prefix}bound-account-93',
  '${fixture_prefix}unconnected-account-73'
);

DELETE FROM api_keys
WHERE key = '${fixture_key}'
  AND name = '${fixture_prefix}local-only'
  AND user_id = 1
  AND group_id = 5;

COMMIT;
SQL
}

case "${1:-status}" in
  status)
    status_fixture
    ;;
  ensure)
    ensure_fixture
    ;;
  rollback)
    rollback_fixture
    ;;
  *)
    echo "用法: $0 [status|ensure|rollback]" >&2
    exit 2
    ;;
esac

-- 本机 Sub2API v0.2.12 人工测试数据。不是默认自动测试，不产生外部请求。
-- 上海业务日显式传入；旧故障场景：未接入账号有收入，不应伪造接入。
-- SQL 只能通过 scripts/local-sub2api-revenue-fixture.sh current-* 调用。
BEGIN;
\if :fixture_status
SET TRANSACTION READ ONLY;
\endif
SELECT set_config('fixture.action', :'fixture_action', true),
       set_config('fixture.day', :'fixture_day', true) \gset

\if :fixture_status
SELECT group_id, count(*) AS requests, sum(actual_cost) AS fixture_income
FROM usage_logs
WHERE request_id IN (
  'codex-revenue-local-' || replace(:'fixture_day', '-', '') || '-default',
  'codex-revenue-local-' || replace(:'fixture_day', '-', '') || '-openai1-bound',
  'codex-revenue-local-' || replace(:'fixture_day', '-', '') || '-openai1-unconnected',
  'codex-revenue-local-' || replace(:'fixture_day', '-', '') || '-openai2',
  'codex-revenue-local-' || replace(:'fixture_day', '-', '') || '-grok1'
)
GROUP BY group_id ORDER BY group_id;
SELECT id, name, group_id, status FROM api_keys
WHERE name IN ('codex-revenue-local-key-1', 'codex-revenue-local-key-2',
               'codex-revenue-local-key-3', 'codex-revenue-local-key-5');
SELECT id, name, status, schedulable FROM accounts
WHERE name = 'codex-revenue-local-unconnected';
SELECT id, name, platform, status FROM groups
WHERE name = 'codex-revenue-local-empty';
\else
-- 同一套夹具并发准备或清理时串行执行；事务失败全部回滚。
SELECT pg_advisory_xact_lock(hashtext('codex-revenue-local'));

-- 删除前检查所有数据库外键，防止 ON DELETE CASCADE / SET NULL 影响其他数据。
CREATE FUNCTION pg_temp.assert_no_references(target regclass, target_id bigint)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE ref record; used boolean;
BEGIN
  FOR ref IN
    SELECT c.conrelid::regclass AS rel, a.attname AS column_name,
           cardinality(c.conkey) AS column_count
    FROM pg_constraint c
    JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
    WHERE c.contype = 'f' AND c.confrelid = target
  LOOP
    IF ref.column_count <> 1 THEN
      RAISE EXCEPTION 'unsupported composite foreign key: %', ref.rel;
    END IF;
    EXECUTE format('SELECT EXISTS (SELECT 1 FROM %s WHERE %I = $1)',
                   ref.rel, ref.column_name) INTO used USING target_id;
    IF used THEN
      RAISE EXCEPTION 'fixture is referenced by %, refusing cleanup', ref.rel;
    END IF;
  END LOOP;
END $$;

DO $$
DECLARE
  action text := current_setting('fixture.action');
  business_day date := current_setting('fixture.day')::date;
  prefix text := 'codex-revenue-local-' || to_char(business_day, 'YYYYMMDD') || '-';
  marker text := 'codex-revenue-local: inactive local test data';
  fake_account bigint; empty_group bigint; key_id bigint; usage_id bigint; r record;
BEGIN
  IF action NOT IN ('ensure', 'rollback') THEN
    RAISE EXCEPTION 'invalid fixture action';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM users WHERE id = 1 AND role = 'admin'
                 AND status = 'active' AND deleted_at IS NULL) THEN
    RAISE EXCEPTION 'expected local administrator 1 is missing';
  END IF;
  -- 不猜测旧环境 ID，也不重写现有分组、账号或成员关系。
  FOR r IN SELECT * FROM (VALUES
    (1::bigint, 'default', 'anthropic', 3::bigint),
    (2::bigint, 'openai1', 'openai', 2::bigint),
    (3::bigint, 'openai2', 'openai', 4::bigint),
    (5::bigint, 'grok1', 'grok', 1::bigint)
  ) AS expected(group_id, group_name, platform, account_id)
  LOOP
    IF NOT EXISTS (
      SELECT 1 FROM groups g JOIN account_groups ag ON ag.group_id = g.id
      JOIN accounts a ON a.id = ag.account_id
      WHERE g.id = r.group_id AND g.name = r.group_name AND g.platform = r.platform
        AND g.deleted_at IS NULL AND a.id = r.account_id
        AND a.platform = r.platform AND a.deleted_at IS NULL
    ) THEN
      RAISE EXCEPTION 'local group/account dependency changed: %/%', r.group_id, r.account_id;
    END IF;
  END LOOP;

  IF action = 'ensure' THEN
    INSERT INTO groups (name, description, platform, status, rate_multiplier)
    SELECT 'codex-revenue-local-empty', marker, 'openai', 'active', 1
    WHERE NOT EXISTS (SELECT 1 FROM groups WHERE name = 'codex-revenue-local-empty');
    INSERT INTO accounts (name, notes, platform, type, status, schedulable, credentials)
    SELECT 'codex-revenue-local-unconnected', marker, 'openai', 'apikey', 'inactive', false, '{}'::jsonb
    WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE name = 'codex-revenue-local-unconnected');
  END IF;
  -- FOR UPDATE 同时阻止主站新增外键引用，引用检查和删除之间不能留并发窗口。
  PERFORM 1 FROM groups WHERE name = 'codex-revenue-local-empty' FOR UPDATE;
  PERFORM 1 FROM accounts WHERE name = 'codex-revenue-local-unconnected' FOR UPDATE;
  IF (SELECT count(*) FROM groups WHERE name = 'codex-revenue-local-empty') > 1 OR
     EXISTS (SELECT 1 FROM groups WHERE name = 'codex-revenue-local-empty'
             AND (description IS DISTINCT FROM marker OR platform <> 'openai'
                  OR status <> 'active' OR rate_multiplier <> 1 OR deleted_at IS NOT NULL)) THEN
    RAISE EXCEPTION 'fixture group has unexpected ownership or state';
  END IF;
  IF (SELECT count(*) FROM accounts WHERE name = 'codex-revenue-local-unconnected') > 1 OR
     EXISTS (SELECT 1 FROM accounts WHERE name = 'codex-revenue-local-unconnected'
             AND (notes IS DISTINCT FROM marker OR platform <> 'openai' OR type <> 'apikey'
                  OR status <> 'inactive' OR schedulable OR credentials <> '{}'::jsonb
                  OR deleted_at IS NOT NULL)) THEN
    RAISE EXCEPTION 'fixture account has unexpected ownership or state';
  END IF;
  SELECT id INTO empty_group FROM groups WHERE name = 'codex-revenue-local-empty';
  SELECT id INTO fake_account FROM accounts WHERE name = 'codex-revenue-local-unconnected';
  IF empty_group IS NOT NULL THEN
    PERFORM pg_temp.assert_no_references('groups', empty_group);
  END IF;
  IF action = 'ensure' THEN
    INSERT INTO account_groups (account_id, group_id, priority)
    SELECT fake_account, 2, 50
    WHERE NOT EXISTS (SELECT 1 FROM account_groups WHERE account_id = fake_account);
  END IF;
  PERFORM 1 FROM account_groups WHERE account_id = fake_account FOR UPDATE;
  IF EXISTS (SELECT 1 FROM account_groups WHERE account_id = fake_account AND group_id <> 2) OR
     (action = 'ensure' AND
      (SELECT count(*) FROM account_groups WHERE account_id = fake_account AND group_id = 2) <> 1) THEN
    RAISE EXCEPTION 'fixture account membership changed';
  END IF;

  FOR r IN SELECT * FROM (VALUES
    (1::bigint, 3::bigint, 'default', 12.00::numeric, 'fixture-local-anthropic'),
    (2::bigint, 2::bigint, 'openai1-bound', 10.00::numeric, 'fixture-local-openai'),
    (2::bigint, fake_account, 'openai1-unconnected', 7.25::numeric, 'fixture-local-openai'),
    (3::bigint, 4::bigint, 'openai2', 8.00::numeric, 'fixture-local-openai'),
    (5::bigint, 1::bigint, 'grok1', 6.00::numeric, 'fixture-local-grok')
  ) AS expected(group_id, account_id, suffix, income, model)
  LOOP
    IF action = 'ensure' THEN
      INSERT INTO api_keys (user_id, key, name, group_id, status)
      SELECT 1, 'sk-' || md5(random()::text || clock_timestamp()::text) || md5(random()::text),
             'codex-revenue-local-key-' || r.group_id, r.group_id, 'inactive'
      WHERE NOT EXISTS (SELECT 1 FROM api_keys WHERE name = 'codex-revenue-local-key-' || r.group_id);
    END IF;
    PERFORM 1 FROM api_keys WHERE name = 'codex-revenue-local-key-' || r.group_id FOR UPDATE;
    IF (SELECT count(*) FROM api_keys WHERE name = 'codex-revenue-local-key-' || r.group_id) > 1 OR
       EXISTS (SELECT 1 FROM api_keys WHERE name = 'codex-revenue-local-key-' || r.group_id
               AND (user_id <> 1 OR group_id IS DISTINCT FROM r.group_id
                    OR status <> 'inactive' OR quota_used <> 0 OR deleted_at IS NOT NULL)) THEN
      RAISE EXCEPTION 'fixture key ownership or disabled state changed: group %', r.group_id;
    END IF;
    SELECT id INTO key_id FROM api_keys WHERE name = 'codex-revenue-local-key-' || r.group_id;
    PERFORM 1 FROM usage_logs WHERE request_id = prefix || r.suffix FOR UPDATE;
    IF EXISTS (
      SELECT 1 FROM usage_logs WHERE request_id = prefix || r.suffix
      AND (user_id <> 1 OR api_key_id IS DISTINCT FROM key_id
           OR account_id IS DISTINCT FROM r.account_id OR group_id IS DISTINCT FROM r.group_id
           OR actual_cost <> r.income OR total_cost <> r.income
           OR account_stats_cost IS DISTINCT FROM r.income OR model <> r.model
           OR created_at <> (business_day + time '12:00') AT TIME ZONE 'Asia/Shanghai')
    ) OR (SELECT count(*) FROM usage_logs WHERE request_id = prefix || r.suffix) > 1 THEN
      RAISE EXCEPTION 'fixture usage has unexpected content: %', r.suffix;
    END IF;
    IF action = 'ensure' THEN
      INSERT INTO usage_logs (
        user_id, api_key_id, account_id, group_id, request_id,
        model, requested_model, upstream_model, input_tokens, output_tokens,
        input_cost, output_cost, total_cost, actual_cost, account_stats_cost,
        duration_ms, created_at
      ) SELECT 1, key_id, r.account_id, r.group_id, prefix || r.suffix,
               r.model, r.model, r.model, 1000, 500,
               r.income * 0.6, r.income * 0.4, r.income, r.income, r.income,
               1200, (business_day + time '12:00') AT TIME ZONE 'Asia/Shanghai'
      WHERE NOT EXISTS (SELECT 1 FROM usage_logs WHERE request_id = prefix || r.suffix);
    ELSE
      -- usage_logs 也有账单外键，不能因标记流水清理而级联删除账单。
      FOR usage_id IN SELECT id FROM usage_logs WHERE request_id = prefix || r.suffix
      LOOP
        PERFORM pg_temp.assert_no_references('usage_logs', usage_id);
      END LOOP;
      DELETE FROM usage_logs WHERE request_id = prefix || r.suffix;
    END IF;
  END LOOP;

  IF action = 'rollback' THEN
    FOR r IN SELECT id FROM api_keys WHERE name IN (
      'codex-revenue-local-key-1', 'codex-revenue-local-key-2',
      'codex-revenue-local-key-3', 'codex-revenue-local-key-5')
    LOOP
      -- 其他业务日的夹具仍在时保留共享 Key 和账号。
      IF NOT EXISTS (SELECT 1 FROM usage_logs WHERE api_key_id = r.id) THEN
        PERFORM pg_temp.assert_no_references('api_keys', r.id);
        DELETE FROM api_keys WHERE id = r.id;
      END IF;
    END LOOP;
    IF fake_account IS NOT NULL AND
       NOT EXISTS (SELECT 1 FROM usage_logs WHERE account_id = fake_account) THEN
      DELETE FROM account_groups WHERE account_id = fake_account AND group_id = 2;
      PERFORM pg_temp.assert_no_references('accounts', fake_account);
      DELETE FROM accounts WHERE id = fake_account;
    END IF;
    IF empty_group IS NOT NULL THEN
      DELETE FROM groups WHERE id = empty_group;
    END IF;
  END IF;
END $$;
\endif
COMMIT;

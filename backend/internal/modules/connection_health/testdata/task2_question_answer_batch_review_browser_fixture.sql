\set ON_ERROR_STOP on

SELECT
    set_config('task2.user_id', :'user_id', false) AS user_setting,
    set_config('task2.target_id', :'target_id', false) AS target_setting
\gset

\if :prepare
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task2.user_id') || '|' || current_setting('task2.target_id')
));
DO $$
DECLARE
    batch_ids text[] := ARRAY['task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830'];
    fixture_ids text[] := ARRAY[
        'task2-active-20260830-pending',
        'task2-active-20260830-running',
        'task2-older-20260830-correct',
        'task2-older-20260830-incorrect'
    ] || ARRAY(
        SELECT 'task2-bulk-20260830-' || lpad(item::text, 2, '0')
        FROM generate_series(1, 25) AS item
    );
    affected integer;
    inserted integer := 0;
    fixture_now timestamptz := COALESCE(
        NULLIF(current_setting('task2.fixture_now', true), '')::timestamptz,
        now()
    );
    shanghai_day_start timestamptz := date_trunc('day', fixture_now AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai';
    fixture_span interval := fixture_now - shanghai_day_start;
BEGIN
    IF EXISTS (SELECT 1 FROM connection_health_question_answer_records WHERE id = ANY(fixture_ids))
       OR EXISTS (
           SELECT 1 FROM connection_health_question_answer_records
           WHERE user_id = current_setting('task2.user_id')
             AND target_id = current_setting('task2.target_id')
             AND batch_id = ANY(batch_ids)
       ) THEN
        RAISE EXCEPTION 'fixture id already exists';
    END IF;
    IF EXISTS (
        SELECT 1 FROM connection_health_question_answer_records
        WHERE user_id = current_setting('task2.user_id')
          AND target_id = current_setting('task2.target_id')
          AND status IN ('pending', 'running')
    ) THEN
        RAISE EXCEPTION 'non-fixture active batch exists';
    END IF;

    INSERT INTO connection_health_question_answer_records (
        id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
        reasoning_effort, answer_body, status, error_type, answer_judgment, manual_error,
        created_at, started_at, completed_at, updated_at
    ) VALUES
        (
            'task2-active-20260830-pending', current_setting('task2.user_id'), current_setting('task2.target_id'),
            'task2-active-20260830', 'task2-active-model', 'task2-active-pending', 'TASK2 最新等待请求',
            'TASK2 最新等待请求正文', 'medium', '', 'pending', '', NULL, false,
            shanghai_day_start + fixture_span * 0.90, NULL, NULL,
            shanghai_day_start + fixture_span * 0.90
        ),
        (
            'task2-active-20260830-running', current_setting('task2.user_id'), current_setting('task2.target_id'),
            'task2-active-20260830', 'task2-active-model', 'task2-active-running', 'TASK2 最新运行请求',
            'TASK2 最新运行请求正文', 'medium', '', 'running', '', NULL, false,
            shanghai_day_start + fixture_span * 0.91,
            shanghai_day_start + fixture_span * 0.91, NULL,
            shanghai_day_start + fixture_span * 0.91
        );
    GET DIAGNOSTICS affected = ROW_COUNT;
    inserted := inserted + affected;

    INSERT INTO connection_health_question_answer_records (
        id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
        reasoning_effort, answer_body, status, error_type, answer_judgment, manual_error,
        created_at, started_at, completed_at, updated_at
    )
    SELECT
        'task2-bulk-20260830-' || lpad(item::text, 2, '0'),
        current_setting('task2.user_id'), current_setting('task2.target_id'), 'task2-bulk-20260830',
        'task2-model-' || lpad(item::text, 2, '0'),
        'task2-question-' || lpad(item::text, 2, '0'),
        'TASK2 跨页问题 ' || item, 'TASK2 跨页问题正文 ' || item, 'medium',
        CASE WHEN item <= 23 THEN 'TASK2 跨页回答 ' || item ELSE '' END,
        CASE WHEN item <= 23 THEN 'succeeded' WHEN item = 24 THEN 'failed' ELSE 'cancelled' END,
        CASE WHEN item = 24 THEN 'network' ELSE '' END,
        CASE WHEN item <= 5 THEN 'unreviewed' WHEN item <= 14 THEN 'correct' WHEN item <= 23 THEN 'incorrect' ELSE NULL END,
        item BETWEEN 15 AND 23,
        shanghai_day_start + fixture_span * (0.50 + item::double precision * 0.01),
        shanghai_day_start + fixture_span * (0.501 + item::double precision * 0.01),
        shanghai_day_start + fixture_span * (0.502 + item::double precision * 0.01),
        shanghai_day_start + fixture_span * (0.502 + item::double precision * 0.01)
    FROM generate_series(1, 25) AS item;
    GET DIAGNOSTICS affected = ROW_COUNT;
    inserted := inserted + affected;

    INSERT INTO connection_health_question_answer_records (
        id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
        reasoning_effort, answer_body, status, error_type, answer_judgment, manual_error,
        created_at, started_at, completed_at, updated_at
    ) VALUES
        (
            'task2-older-20260830-correct', current_setting('task2.user_id'), current_setting('task2.target_id'),
            'task2-older-20260830', 'task2-older-model', 'task2-older-correct', 'TASK2 更早正确',
            'TASK2 更早正确问题', 'medium', 'TASK2 更早正确回答', 'succeeded', '', 'correct', false,
            shanghai_day_start + fixture_span * 0.18,
            shanghai_day_start + fixture_span * 0.19,
            shanghai_day_start + fixture_span * 0.20,
            shanghai_day_start + fixture_span * 0.20
        ),
        (
            'task2-older-20260830-incorrect', current_setting('task2.user_id'), current_setting('task2.target_id'),
            'task2-older-20260830', 'task2-older-model', 'task2-older-incorrect', 'TASK2 更早错误',
            'TASK2 更早错误问题', 'medium', 'TASK2 更早错误回答', 'succeeded', '', 'incorrect', true,
            shanghai_day_start + fixture_span * 0.21,
            shanghai_day_start + fixture_span * 0.22,
            shanghai_day_start + fixture_span * 0.23,
            shanghai_day_start + fixture_span * 0.23
        );
    GET DIAGNOSTICS affected = ROW_COUNT;
    inserted := inserted + affected;
    IF inserted <> 29 THEN
        RAISE EXCEPTION 'expected exactly twenty-nine inserted rows, got %', inserted;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE id = ANY(fixture_ids)
          AND (
              (created_at AT TIME ZONE 'Asia/Shanghai')::date
                  <> (fixture_now AT TIME ZONE 'Asia/Shanghai')::date
              OR created_at > fixture_now
              OR (started_at IS NOT NULL AND (started_at < created_at OR started_at > fixture_now))
              OR (
                  completed_at IS NOT NULL
                  AND (completed_at < COALESCE(started_at, created_at) OR completed_at > fixture_now)
              )
              OR updated_at < created_at
              OR updated_at > fixture_now
          )
    ) THEN
        RAISE EXCEPTION 'fixture timestamp escaped Shanghai day or lifecycle order';
    END IF;
END $$;
COMMIT;
\endif

\if :count
SELECT format(
    'fixture_total=%s active=%s bulk=%s older=%s pending=%s running=%s succeeded=%s failed=%s cancelled=%s unreviewed=%s correct=%s incorrect=%s non_fixture_active=%s',
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830')),
    count(*) FILTER (WHERE batch_id = 'task2-active-20260830'),
    count(*) FILTER (WHERE batch_id = 'task2-bulk-20260830'),
    count(*) FILTER (WHERE batch_id = 'task2-older-20260830'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status = 'pending'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status = 'running'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status = 'succeeded'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status = 'failed'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status = 'cancelled'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND answer_judgment = 'unreviewed'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND answer_judgment = 'correct'),
    count(*) FILTER (WHERE batch_id IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND answer_judgment = 'incorrect'),
    count(*) FILTER (WHERE batch_id NOT IN ('task2-active-20260830', 'task2-bulk-20260830', 'task2-older-20260830') AND status IN ('pending', 'running'))
)
FROM connection_health_question_answer_records
WHERE user_id = current_setting('task2.user_id')
  AND target_id = current_setting('task2.target_id');
\endif

\if :cleanup
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task2.user_id') || '|' || current_setting('task2.target_id')
));
DO $$
DECLARE
    bulk_ids text[] := ARRAY(
        SELECT 'task2-bulk-20260830-' || lpad(item::text, 2, '0')
        FROM generate_series(1, 25) AS item
    );
    affected integer;
BEGIN
    DELETE FROM connection_health_question_answer_records
    WHERE user_id = current_setting('task2.user_id')
      AND target_id = current_setting('task2.target_id')
      AND (
          (batch_id = 'task2-active-20260830' AND id IN ('task2-active-20260830-pending', 'task2-active-20260830-running'))
          OR (batch_id = 'task2-bulk-20260830' AND id = ANY(bulk_ids))
          OR (batch_id = 'task2-older-20260830' AND id IN ('task2-older-20260830-correct', 'task2-older-20260830-incorrect'))
      );
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 29 THEN
        RAISE EXCEPTION 'expected exactly twenty-nine deleted rows, got %', affected;
    END IF;
END $$;
COMMIT;
\endif

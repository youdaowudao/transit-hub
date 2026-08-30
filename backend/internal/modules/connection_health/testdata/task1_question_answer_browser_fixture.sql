\set ON_ERROR_STOP on

SELECT
    set_config('task1.user_id', :'user_id', false) AS user_setting,
    set_config('task1.target_id', :'target_id', false) AS target_setting
\gset

\if :prepare
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task1.user_id') || '|' || current_setting('task1.target_id')
));
DO $$
DECLARE
    fixture_ids text[] := ARRAY[
        'task1-review-20260830-pending',
        'task1-review-20260830-running',
        'task1-review-20260830-unreviewed-1',
        'task1-review-20260830-unreviewed-long',
        'task1-review-20260830-correct',
        'task1-review-20260830-incorrect',
        'task1-review-20260830-failed',
        'task1-review-20260830-cancelled'
    ];
    affected integer;
BEGIN
    IF EXISTS (
        SELECT 1 FROM connection_health_question_answer_records WHERE id = ANY(fixture_ids)
    ) THEN
        RAISE EXCEPTION 'fixture id already exists';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE user_id = current_setting('task1.user_id')
          AND target_id = current_setting('task1.target_id')
          AND status IN ('pending', 'running')
    ) THEN
        RAISE EXCEPTION 'non-fixture active batch exists';
    END IF;

    INSERT INTO connection_health_question_answer_records (
        id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
        reasoning_effort, answer_body, status, error_type, answer_judgment, manual_error,
        started_at, completed_at
    ) VALUES
        (fixture_ids[1], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-pending', 'TASK1 等待请求', 'TASK1 等待请求正文', 'medium', '', 'pending', '', NULL, false, NULL, NULL),
        (fixture_ids[2], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-running', 'TASK1 运行请求', 'TASK1 运行请求正文', 'medium', '', 'running', '', NULL, false, now(), NULL),
        (fixture_ids[3], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-unreviewed-1', 'TASK1 未复审一', 'TASK1 未复审问题一', 'medium', 'TASK1 未复审回答一', 'succeeded', '', 'unreviewed', false, now() - interval '2 seconds', now()),
        (fixture_ids[4], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-unreviewed-long', 'TASK1 未复审长回答', 'TASK1 未复审长问题', 'medium', 'TASK1 长回答：' || repeat('这是一段用于确认无需展开即可连续阅读的隔离答案。', 40), 'succeeded', '', 'unreviewed', false, now() - interval '2 seconds', now()),
        (fixture_ids[5], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-correct', 'TASK1 已判正确', 'TASK1 已判正确问题', 'medium', 'TASK1 已判正确回答', 'succeeded', '', 'correct', false, now() - interval '2 seconds', now()),
        (fixture_ids[6], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-incorrect', 'TASK1 已判错误', 'TASK1 已判错误问题', 'medium', 'TASK1 已判错误回答', 'succeeded', '', 'incorrect', true, now() - interval '2 seconds', now()),
        (fixture_ids[7], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-failed', 'TASK1 请求失败', 'TASK1 请求失败问题', 'medium', '', 'failed', 'network', NULL, false, now() - interval '2 seconds', now()),
        (fixture_ids[8], current_setting('task1.user_id'), current_setting('task1.target_id'), 'task1-review-20260830-batch', 'task1-model', 'task1-q-cancelled', 'TASK1 已取消', 'TASK1 已取消问题', 'medium', '', 'cancelled', '', NULL, false, NULL, now());
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 8 THEN
        RAISE EXCEPTION 'expected exactly eight inserted rows, got %', affected;
    END IF;
END $$;
COMMIT;
\endif

\if :advance
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task1.user_id') || '|' || current_setting('task1.target_id')
));
DO $$
DECLARE
    expected integer;
    affected integer := 0;
    changed integer;
BEGIN
    SELECT count(*) INTO expected
    FROM connection_health_question_answer_records
    WHERE user_id = current_setting('task1.user_id')
      AND target_id = current_setting('task1.target_id')
      AND batch_id = 'task1-review-20260830-batch'
      AND ((id = 'task1-review-20260830-pending' AND status = 'pending')
        OR (id = 'task1-review-20260830-running' AND status = 'running'));
    IF expected <> 2 THEN
        RAISE EXCEPTION 'expected exactly two old states, got %', expected;
    END IF;

    UPDATE connection_health_question_answer_records
    SET status = 'cancelled', completed_at = now(), error_type = '', answer_judgment = NULL, manual_error = false, updated_at = now()
    WHERE id = 'task1-review-20260830-pending'
      AND user_id = current_setting('task1.user_id')
      AND target_id = current_setting('task1.target_id')
      AND batch_id = 'task1-review-20260830-batch'
      AND status = 'pending';
    GET DIAGNOSTICS changed = ROW_COUNT;
    affected := affected + changed;

    UPDATE connection_health_question_answer_records
    SET status = 'failed', completed_at = now(), error_type = 'network', answer_judgment = NULL, manual_error = false, updated_at = now()
    WHERE id = 'task1-review-20260830-running'
      AND user_id = current_setting('task1.user_id')
      AND target_id = current_setting('task1.target_id')
      AND batch_id = 'task1-review-20260830-batch'
      AND status = 'running';
    GET DIAGNOSTICS changed = ROW_COUNT;
    affected := affected + changed;
    IF affected <> 2 THEN
        RAISE EXCEPTION 'expected exactly two updated rows, got %', affected;
    END IF;
END $$;
COMMIT;
\endif

\if :count
SELECT format(
    'fixture_total=%s pending=%s running=%s succeeded=%s failed=%s cancelled=%s unreviewed=%s correct=%s incorrect=%s non_fixture_active=%s',
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND status = 'pending'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND status = 'running'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND status = 'succeeded'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND status = 'failed'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND status = 'cancelled'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND answer_judgment = 'unreviewed'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND answer_judgment = 'correct'),
    count(*) FILTER (WHERE batch_id = 'task1-review-20260830-batch' AND answer_judgment = 'incorrect'),
    count(*) FILTER (WHERE batch_id <> 'task1-review-20260830-batch' AND status IN ('pending', 'running'))
)
FROM connection_health_question_answer_records
WHERE user_id = current_setting('task1.user_id')
  AND target_id = current_setting('task1.target_id');
\endif

\if :cleanup
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task1.user_id') || '|' || current_setting('task1.target_id')
));
DO $$
DECLARE
    affected integer;
BEGIN
    DELETE FROM connection_health_question_answer_records
    WHERE id = ANY(ARRAY[
            'task1-review-20260830-pending',
            'task1-review-20260830-running',
            'task1-review-20260830-unreviewed-1',
            'task1-review-20260830-unreviewed-long',
            'task1-review-20260830-correct',
            'task1-review-20260830-incorrect',
            'task1-review-20260830-failed',
            'task1-review-20260830-cancelled'
        ])
      AND user_id = current_setting('task1.user_id')
      AND target_id = current_setting('task1.target_id')
      AND batch_id = 'task1-review-20260830-batch';
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 8 THEN
        RAISE EXCEPTION 'expected exactly eight deleted rows, got %', affected;
    END IF;
END $$;
COMMIT;
\endif

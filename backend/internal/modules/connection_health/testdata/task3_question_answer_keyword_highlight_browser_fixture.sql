\set ON_ERROR_STOP on

SELECT
    set_config('task3.user_id', :'user_id', false) AS user_setting,
    set_config('task3.target_id', :'target_id', false) AS target_setting
\gset

\if :prepare
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task3.user_id') || '|' || current_setting('task3.target_id')
));
DO $$
DECLARE
    batch_ids text[] := ARRAY[
        'task3-latest-20260831',
        'task3-old-snapshot-20260831',
        'task3-highlight-limit-20260831',
        'task3-highlight-overlap-20260831'
    ];
    fixture_ids text[] := ARRAY[
        'task3-latest-unreviewed',
        'task3-latest-correct',
        'task3-latest-incorrect',
        'task3-latest-null',
        'task3-latest-empty',
        'task3-latest-html',
        'task3-latest-max-keywords',
        'task3-old-unreviewed',
        'task3-highlight-limit',
        'task3-highlight-overlap'
    ];
    inserted integer;
BEGIN
    IF EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE id = ANY(fixture_ids)
    ) OR EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND batch_id = ANY(batch_ids)
    ) THEN
        RAISE EXCEPTION 'fixture id already exists';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND status IN ('pending', 'running')
          AND id <> ALL(fixture_ids)
    ) THEN
        RAISE EXCEPTION 'non-fixture active batch exists';
    END IF;

    INSERT INTO connection_health_question_answer_records (
        id, user_id, target_id, batch_id, model_name, question_id, question_name, question_body,
        question_keyword_snapshot, reasoning_effort, answer_body, status, error_type,
        answer_judgment, manual_error, created_at, started_at, completed_at, updated_at
    ) VALUES
        (
            'task3-latest-unreviewed', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-unreviewed', 'task3-question-unreviewed',
            'TASK3 未复审关键字', '请返回包含错误码和 Error 的回答。', ARRAY['错误码', 'Error']::text[],
            'medium', 'Error：错误码 401。', 'succeeded', '', 'unreviewed', false,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-correct', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-correct', 'task3-question-correct',
            'TASK3 已判正确', '这条回答已判定正确。', ARRAY['正确']::text[],
            'medium', '这是已判定正确的回答。', 'succeeded', '', 'correct', false,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-incorrect', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-incorrect', 'task3-question-incorrect',
            'TASK3 已判错误', '这条回答已判定错误。', ARRAY['错误']::text[],
            'medium', '这是已判定错误的回答。', 'succeeded', '', 'incorrect', true,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-null', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-null', 'task3-question-null',
            'TASK3 NULL 快照', '这条旧记录没有关键字快照。', NULL,
            'medium', '没有关键字快照。', 'succeeded', '', 'unreviewed', false,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-empty', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-empty', 'task3-question-empty',
            'TASK3 空快照', '这条记录保存了空关键字快照。', ARRAY[]::text[],
            'medium', '空关键字快照不产生高亮。', 'succeeded', '', 'unreviewed', false,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-html', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-html', 'task3-question-html',
            'TASK3 HTML 字面量', '把 HTML 文本当作普通文本返回。', ARRAY['<script>', '[done]']::text[],
            'medium', '<script>alert(1)</script> [done]', 'succeeded', '', 'unreviewed', false,
            now(), now(), now(), now()
        ),
        (
            'task3-latest-max-keywords', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-latest-20260831', 'task3-model-max', 'task3-question-max',
            'TASK3 关键字上限', '验证 20 项和 2048 UTF-8 字节边界。', ARRAY(
                SELECT repeat('😀', 25) || lpad(item::text, 2, '0') || CASE WHEN item <= 8 THEN 'x' ELSE '' END
                FROM generate_series(1, 20) AS item
                ORDER BY item
            ),
            'medium', '边界关键字快照已保存。', 'succeeded', '', 'unreviewed', false,
            now(), now(), now(), now()
        ),
        (
            'task3-old-unreviewed', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-old-snapshot-20260831', 'task3-model-old', 'task3-question-old',
            'TASK3 旧快照', '记录创建后当前题目关键字已经变化。', ARRAY['旧关键字']::text[],
            'medium', '回答同时包含旧关键字和当前配置关键字。', 'succeeded', '', 'unreviewed', false,
            now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour'
        ),
        (
            'task3-highlight-limit', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-highlight-limit-20260831', 'task3-model-highlight-limit', 'task3-question-highlight-limit',
            'TASK3 前三处高亮', '验证第四处命中保持普通文字。', ARRAY['Error']::text[],
            'medium', 'Error one. Error two. Error three. Error four.',
            'succeeded', '', 'unreviewed', false,
            now() - interval '2 hours', now() - interval '2 hours', now() - interval '2 hours', now() - interval '2 hours'
        ),
        (
            'task3-highlight-overlap', current_setting('task3.user_id'), current_setting('task3.target_id'),
            'task3-highlight-overlap-20260831', 'task3-model-highlight-overlap', 'task3-question-highlight-overlap',
            'TASK3 重叠前三处高亮', '验证长关键字优先且第四处保持普通文字。', ARRAY['错误', '错误码']::text[],
            'medium', '错误码 one；错误码 two；错误码 three；错误码 four。', 'succeeded', '', 'unreviewed', false,
            now() - interval '3 hours', now() - interval '3 hours', now() - interval '3 hours', now() - interval '3 hours'
        );
    GET DIAGNOSTICS inserted = ROW_COUNT;

    IF inserted <> 10 THEN
        RAISE EXCEPTION 'expected exactly ten inserted rows, got %', inserted;
    END IF;
    IF (
        SELECT count(*)
        FROM connection_health_question_answer_records
        WHERE user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND batch_id = ANY(batch_ids)
    ) <> 10 THEN
        RAISE EXCEPTION 'expected exactly ten inserted rows after verification';
    END IF;
    IF (
        SELECT count(*)
        FROM connection_health_question_answer_records
        WHERE id = 'task3-latest-null'
          AND user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND question_keyword_snapshot IS NULL
    ) <> 1 OR (
        SELECT count(*)
        FROM connection_health_question_answer_records
        WHERE id = 'task3-latest-empty'
          AND user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND question_keyword_snapshot IS NOT NULL
          AND cardinality(question_keyword_snapshot) = 0
    ) <> 1 THEN
        RAISE EXCEPTION 'expected one null and one empty keyword snapshot';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records AS record
        WHERE record.id = 'task3-latest-max-keywords'
          AND record.user_id = current_setting('task3.user_id')
          AND record.target_id = current_setting('task3.target_id')
          AND cardinality(record.question_keyword_snapshot) = 20
          AND (
              SELECT sum(octet_length(keyword))
              FROM unnest(record.question_keyword_snapshot) AS keyword
          ) = 2048
          AND NOT EXISTS (
              SELECT 1
              FROM unnest(record.question_keyword_snapshot) AS keyword
              WHERE btrim(keyword) = '' OR char_length(keyword) > 64
          )
          AND (
              SELECT count(DISTINCT lower(keyword))
              FROM unnest(record.question_keyword_snapshot) AS keyword
          ) = 20
    ) THEN
        RAISE EXCEPTION 'expected twenty unique keywords totaling exactly 2048 UTF-8 bytes';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE id = 'task3-highlight-limit'
          AND user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND batch_id = 'task3-highlight-limit-20260831'
          AND answer_body = 'Error one. Error two. Error three. Error four.'
          AND question_keyword_snapshot = ARRAY['Error']::text[]
    ) OR NOT EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE id = 'task3-highlight-overlap'
          AND user_id = current_setting('task3.user_id')
          AND target_id = current_setting('task3.target_id')
          AND batch_id = 'task3-highlight-overlap-20260831'
          AND answer_body = '错误码 one；错误码 two；错误码 three；错误码 four。'
          AND question_keyword_snapshot = ARRAY['错误', '错误码']::text[]
    ) THEN
        RAISE EXCEPTION 'expected two ordinary four-match highlight rows';
    END IF;
END $$;
COMMIT;
\endif

\if :count
SELECT format(
    'fixture_total=%s latest=%s old_snapshot=%s highlight_limit=%s highlight_overlap=%s pending=%s running=%s succeeded=%s failed=%s cancelled=%s unreviewed=%s correct=%s incorrect=%s null_snapshot=%s empty_snapshot=%s non_fixture_active=%s',
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831')),
    count(*) FILTER (WHERE batch_id = 'task3-latest-20260831'),
    count(*) FILTER (WHERE batch_id = 'task3-old-snapshot-20260831'),
    count(*) FILTER (WHERE batch_id = 'task3-highlight-limit-20260831'),
    count(*) FILTER (WHERE batch_id = 'task3-highlight-overlap-20260831'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status = 'pending'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status = 'running'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status = 'succeeded'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status = 'failed'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status = 'cancelled'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND answer_judgment = 'unreviewed'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND answer_judgment = 'correct'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND answer_judgment = 'incorrect'),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND question_keyword_snapshot IS NULL),
    count(*) FILTER (WHERE batch_id IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND question_keyword_snapshot IS NOT NULL AND cardinality(question_keyword_snapshot) = 0),
    count(*) FILTER (WHERE batch_id NOT IN ('task3-latest-20260831', 'task3-old-snapshot-20260831', 'task3-highlight-limit-20260831', 'task3-highlight-overlap-20260831') AND status IN ('pending', 'running'))
)
FROM connection_health_question_answer_records
WHERE user_id = current_setting('task3.user_id')
  AND target_id = current_setting('task3.target_id');
\endif

\if :cleanup
BEGIN;
SELECT pg_advisory_xact_lock(hashtext(
    'question-answer|' || current_setting('task3.user_id') || '|' || current_setting('task3.target_id')
));
DO $$
DECLARE
    affected integer;
BEGIN
    DELETE FROM connection_health_question_answer_records
    WHERE user_id = current_setting('task3.user_id')
      AND target_id = current_setting('task3.target_id')
      AND (
          (batch_id = 'task3-latest-20260831' AND id IN (
              'task3-latest-unreviewed', 'task3-latest-correct', 'task3-latest-incorrect',
              'task3-latest-null', 'task3-latest-empty', 'task3-latest-html', 'task3-latest-max-keywords'
          ))
          OR (batch_id = 'task3-old-snapshot-20260831' AND id = 'task3-old-unreviewed')
          OR (batch_id = 'task3-highlight-limit-20260831' AND id = 'task3-highlight-limit')
          OR (batch_id = 'task3-highlight-overlap-20260831' AND id = 'task3-highlight-overlap')
      );
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 10 THEN
        RAISE EXCEPTION 'expected exactly ten deleted rows, got %', affected;
    END IF;
END $$;
COMMIT;
\endif

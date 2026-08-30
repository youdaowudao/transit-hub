-- rollback-safe: destructive（回填答题判定，旧二值程序无法无损区分正确与未复审）
ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS answer_judgment text NULL;

UPDATE connection_health_question_answer_records
SET answer_judgment = CASE
        WHEN manual_error THEN 'incorrect'
        ELSE 'unreviewed'
    END
WHERE status = 'succeeded'
  AND answer_judgment IS NULL;

UPDATE connection_health_question_answer_records
SET answer_judgment = NULL
WHERE status <> 'succeeded'
  AND answer_judgment IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM connection_health_question_answer_records
        WHERE (status = 'succeeded' AND (
                answer_judgment IS NULL
                OR answer_judgment NOT IN ('unreviewed', 'correct', 'incorrect')
            ))
           OR (status <> 'succeeded' AND answer_judgment IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'question answer judgment backfill produced an invalid state';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'connection_health_question_answer_records'::regclass
          AND conname = 'connection_health_question_answer_judgment'
    ) THEN
        ALTER TABLE connection_health_question_answer_records
            ADD CONSTRAINT connection_health_question_answer_judgment
            CHECK (
                (status = 'succeeded'
                    AND answer_judgment IS NOT NULL
                    AND answer_judgment IN ('unreviewed', 'correct', 'incorrect'))
                OR
                (status <> 'succeeded' AND answer_judgment IS NULL)
            );
    END IF;
END $$;

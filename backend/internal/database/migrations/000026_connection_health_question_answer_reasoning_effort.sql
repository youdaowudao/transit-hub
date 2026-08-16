-- rollback-safe: additive（仅新增可空列和约束，旧记录保持 NULL）
ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS reasoning_effort text NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'connection_health_question_answer_records'::regclass
          AND conname = 'connection_health_question_answer_reasoning_effort'
    ) THEN
        ALTER TABLE connection_health_question_answer_records
            ADD CONSTRAINT connection_health_question_answer_reasoning_effort
            CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh'));
    END IF;
END $$;

-- rollback-safe: additive（新增可空来源与真实重复序号；保留历史最终判定）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'connection_health_question_answer_records'::regclass
          AND attname = 'answer_judgment_source' AND NOT attisdropped
    ) THEN
        ALTER TABLE connection_health_question_answer_records
            ADD COLUMN answer_judgment_source text NULL
            CONSTRAINT connection_health_question_answer_judgment_source
                CHECK (answer_judgment_source IS NULL OR answer_judgment_source IN ('automatic', 'manual'));
        UPDATE connection_health_question_answer_records
        SET answer_judgment_source = 'manual'
        WHERE status = 'succeeded' AND answer_judgment IN ('correct', 'incorrect');
    END IF;
END $$;

ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS repeat_index integer NULL
    CONSTRAINT connection_health_question_answer_repeat_index
        CHECK (repeat_index IS NULL OR repeat_index BETWEEN 1 AND 10);

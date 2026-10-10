-- rollback-safe: additive
ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS upstream_status integer NULL
    CONSTRAINT connection_health_question_answer_upstream_status
        CHECK (upstream_status IS NULL OR upstream_status BETWEEN 100 AND 999);

ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS upstream_excerpt text NOT NULL DEFAULT '';

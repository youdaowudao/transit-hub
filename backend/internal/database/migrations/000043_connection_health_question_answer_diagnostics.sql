ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS request_diagnostics jsonb NULL
    CONSTRAINT connection_health_question_answer_diagnostics
        CHECK (request_diagnostics IS NULL OR jsonb_typeof(request_diagnostics) = 'object');

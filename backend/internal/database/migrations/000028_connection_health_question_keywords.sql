-- rollback-safe: additive（仅新增题目关键字和可空历史快照列）
ALTER TABLE connection_health_test_questions
    ADD COLUMN IF NOT EXISTS keywords text[] NOT NULL DEFAULT ARRAY[]::text[];

ALTER TABLE connection_health_question_answer_records
    ADD COLUMN IF NOT EXISTS question_keyword_snapshot text[] NULL;

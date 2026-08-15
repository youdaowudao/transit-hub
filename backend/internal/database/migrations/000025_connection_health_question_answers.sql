CREATE TABLE IF NOT EXISTS connection_health_test_questions (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    name text NOT NULL,
    body text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    is_default boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connection_health_test_questions_name_length CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT connection_health_test_questions_body_length CHECK (char_length(btrim(body)) BETWEEN 1 AND 4000),
    CONSTRAINT connection_health_test_questions_default_enabled CHECK (NOT is_default OR enabled)
);

CREATE INDEX IF NOT EXISTS idx_connection_health_test_questions_user_created
    ON connection_health_test_questions (user_id, created_at, id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_connection_health_test_questions_one_default
    ON connection_health_test_questions (user_id)
    WHERE is_default AND enabled;

CREATE TABLE IF NOT EXISTS connection_health_question_answer_records (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    target_id text NOT NULL,
    batch_id text NOT NULL,
    model_name text NOT NULL,
    question_id text NOT NULL,
    question_name text NOT NULL,
    question_body text NOT NULL,
    answer_body text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending',
    error_type text NOT NULL DEFAULT '',
    manual_error boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connection_health_question_answer_status CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_connection_health_question_answers_target_history
    ON connection_health_question_answer_records (user_id, target_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_connection_health_question_answers_batch
    ON connection_health_question_answer_records (user_id, target_id, batch_id, created_at, id);

CREATE INDEX IF NOT EXISTS idx_connection_health_question_answers_active
    ON connection_health_question_answer_records (user_id, target_id, status)
    WHERE status IN ('pending', 'running');

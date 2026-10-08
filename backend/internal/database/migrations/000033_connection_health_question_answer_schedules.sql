-- rollback-safe: additive C2 schedules and runtime limits; C1 records remain authoritative.
CREATE TABLE IF NOT EXISTS connection_health_workspace_settings (
 user_id text NOT NULL, admin_account_id text NOT NULL DEFAULT '',
 rule_version text NOT NULL DEFAULT 'v2' CHECK (rule_version IN ('v2','legacy')),
 config_generation bigint NOT NULL DEFAULT 0,
 probe_concurrency integer NOT NULL DEFAULT 6 CHECK (probe_concurrency BETWEEN 1 AND 10),
 probe_concurrency_version bigint NOT NULL DEFAULT 0,
 rule_switched_at timestamptz NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (user_id,admin_account_id)
);
ALTER TABLE connection_health_workspace_settings
 ADD COLUMN IF NOT EXISTS max_enabled_question_answer_schedules integer NOT NULL DEFAULT 20 CHECK (max_enabled_question_answer_schedules BETWEEN 1 AND 100),
 ADD COLUMN IF NOT EXISTS max_schedule_targets integer NOT NULL DEFAULT 20 CHECK (max_schedule_targets BETWEEN 1 AND 200),
 ADD COLUMN IF NOT EXISTS max_schedule_requests_per_execution integer NOT NULL DEFAULT 500 CHECK (max_schedule_requests_per_execution BETWEEN 1 AND 10000),
 ADD COLUMN IF NOT EXISTS daily_scheduled_request_limit integer NOT NULL DEFAULT 1000 CHECK (daily_scheduled_request_limit BETWEEN 1 AND 1000000),
 ADD COLUMN IF NOT EXISTS max_active_schedule_executions integer NOT NULL DEFAULT 3 CHECK (max_active_schedule_executions BETWEEN 1 AND 20),
 ADD COLUMN IF NOT EXISTS max_queued_scheduled_requests integer NOT NULL DEFAULT 1000 CHECK (max_queued_scheduled_requests BETWEEN 0 AND 100000),
 ADD COLUMN IF NOT EXISTS schedule_execution_timeout_minutes integer NOT NULL DEFAULT 360 CHECK (schedule_execution_timeout_minutes BETWEEN 30 AND 1440),
 ADD COLUMN IF NOT EXISTS schedule_late_grace_minutes integer NOT NULL DEFAULT 5 CHECK (schedule_late_grace_minutes BETWEEN 0 AND 30),
 ADD COLUMN IF NOT EXISTS question_answer_schedule_limits_version bigint NOT NULL DEFAULT 0 CHECK (question_answer_schedule_limits_version >= 0);
CREATE TABLE IF NOT EXISTS connection_health_question_answer_runtime_settings (
 singleton_key text PRIMARY KEY CHECK (singleton_key='global'),
 question_answer_concurrency integer NOT NULL DEFAULT 15 CHECK (question_answer_concurrency BETWEEN 1 AND 50),
 version bigint NOT NULL DEFAULT 0 CHECK (version >= 0), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS connection_health_question_answer_schedules (
 id text PRIMARY KEY, user_id text NOT NULL, admin_account_id text NOT NULL,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
 target_mode text NOT NULL CHECK (target_mode IN ('groups','accounts')),
 selected_group_ids text[] NOT NULL DEFAULT '{}', selected_account_target_ids text[] NOT NULL DEFAULT '{}',
 target_selection_snapshot jsonb NOT NULL DEFAULT '{}',
 model_names text[] NOT NULL CHECK (cardinality(model_names)>0), question_ids text[] NOT NULL CHECK (cardinality(question_ids)>0),
 reasoning_effort text NOT NULL CHECK (reasoning_effort IN ('low','medium','high','xhigh')),
 repeat_count integer NOT NULL CHECK (repeat_count BETWEEN 1 AND 10),
 peak_start time NOT NULL, peak_end time NOT NULL CHECK (peak_end<>peak_start),
 peak_interval_minutes integer NOT NULL CHECK (peak_interval_minutes BETWEEN 30 AND 1440 AND peak_interval_minutes%30=0),
 off_peak_interval_minutes integer NOT NULL CHECK (off_peak_interval_minutes BETWEEN 30 AND 1440 AND off_peak_interval_minutes%30=0),
 enabled boolean NOT NULL, blocked_reason text NOT NULL DEFAULT '', next_run_at timestamptz NULL,
 version bigint NOT NULL DEFAULT 1 CHECK (version>=1), created_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz NULL,
 CONSTRAINT connection_health_question_answer_schedule_selection CHECK (
  (target_mode='groups' AND cardinality(selected_group_ids)>0 AND cardinality(selected_account_target_ids)=0) OR
  (target_mode='accounts' AND cardinality(selected_account_target_ids)>0 AND cardinality(selected_group_ids)=0)),
 UNIQUE(user_id,admin_account_id,id)
);
-- Module-only test schemas do not own admin_accounts. The real fresh-install
-- migration and runtime startup install this FK whenever the parent exists.
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_question_answer_schedules'::regclass AND conname='connection_health_question_answer_schedule_workspace_fk'
 ) THEN
  ALTER TABLE connection_health_question_answer_schedules ADD CONSTRAINT connection_health_question_answer_schedule_workspace_fk
   FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_question_answer_schedules_due ON connection_health_question_answer_schedules(next_run_at,id)
 WHERE enabled AND deleted_at IS NULL AND blocked_reason='';
CREATE INDEX IF NOT EXISTS idx_question_answer_schedules_workspace ON connection_health_question_answer_schedules(user_id,admin_account_id,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS connection_health_question_answer_schedule_executions (
 id text PRIMARY KEY, user_id text NOT NULL, admin_account_id text NOT NULL,
 schedule_id text NOT NULL REFERENCES connection_health_question_answer_schedules(id) ON DELETE CASCADE,
 trigger text NOT NULL CHECK (trigger IN ('scheduled','run_now')), request_id text NOT NULL DEFAULT '', scheduled_for timestamptz NOT NULL,
 status text NOT NULL CHECK (status IN ('pending','active','completed','partial','failed','skipped','cancelled')), status_reason text NOT NULL DEFAULT '',
 config_snapshot jsonb NOT NULL, planned_target_count integer NOT NULL DEFAULT 0 CHECK (planned_target_count>=0),
 reserved_request_count integer NOT NULL DEFAULT 0 CHECK (reserved_request_count>=0),
 missed_count integer NULL CHECK (missed_count IS NULL OR missed_count>0), missed_from timestamptz NULL, missed_through timestamptz NULL,
 cancel_requested_at timestamptz NULL, termination_cause text NOT NULL DEFAULT '' CHECK (termination_cause IN ('','user_cancel','execution_timeout')),
 version bigint NOT NULL DEFAULT 1 CHECK (version>=1), created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz NULL,
 completed_at timestamptz NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((trigger='scheduled' AND request_id='') OR (trigger='run_now' AND request_id<>'')),
 CHECK ((termination_cause='user_cancel')=(cancel_requested_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_question_answer_schedule_slot ON connection_health_question_answer_schedule_executions(schedule_id,scheduled_for) WHERE trigger='scheduled';
CREATE UNIQUE INDEX IF NOT EXISTS idx_question_answer_schedule_request ON connection_health_question_answer_schedule_executions(schedule_id,request_id) WHERE trigger='run_now';
CREATE UNIQUE INDEX IF NOT EXISTS idx_question_answer_schedule_nonterminal ON connection_health_question_answer_schedule_executions(schedule_id) WHERE status IN ('pending','active');
CREATE INDEX IF NOT EXISTS idx_question_answer_executions_workspace ON connection_health_question_answer_schedule_executions(user_id,admin_account_id,status,scheduled_for,created_at,id);
CREATE INDEX IF NOT EXISTS idx_question_answer_executions_history ON connection_health_question_answer_schedule_executions(schedule_id,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS connection_health_question_answer_schedule_execution_targets (
 id text PRIMARY KEY, execution_id text NOT NULL REFERENCES connection_health_question_answer_schedule_executions(id) ON DELETE CASCADE,
 target_id text NOT NULL, account_snapshot jsonb NOT NULL, matched_groups_snapshot jsonb NOT NULL DEFAULT '[]',
 test_configuration_snapshot jsonb NOT NULL DEFAULT '{}', requested_models text[] NOT NULL, available_models text[] NOT NULL DEFAULT '{}',
 unavailable_models jsonb NOT NULL DEFAULT '[]', planned_request_count integer NOT NULL DEFAULT 0 CHECK (planned_request_count>=0),
 status text NOT NULL CHECK (status IN ('pending','starting','batch_created','failed','skipped','cancelled')), status_reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz NULL, completed_at timestamptz NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(execution_id,target_id)
);
CREATE INDEX IF NOT EXISTS idx_question_answer_execution_targets ON connection_health_question_answer_schedule_execution_targets(execution_id,target_id,status);

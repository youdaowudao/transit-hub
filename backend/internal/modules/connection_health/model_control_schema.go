package connection_health

import "context"

const modelControlSchemaSQL = `-- rollback-safe: additive C3 model controls; C1 records remain authoritative.
CREATE TABLE IF NOT EXISTS connection_health_model_control_rules (
 id text PRIMARY KEY, user_id text NOT NULL, admin_account_id text NOT NULL,
 model_name text NOT NULL CHECK(char_length(model_name) BETWEEN 1 AND 200),
 min_accuracy_percent integer NOT NULL DEFAULT 50 CHECK(min_accuracy_percent BETWEEN 1 AND 100),
 min_judged_answers integer NOT NULL DEFAULT 3 CHECK(min_judged_answers BETWEEN 1 AND 50),
 include_manual boolean NOT NULL DEFAULT true, include_scheduled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>=1), created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(include_manual OR include_scheduled), UNIQUE(user_id,admin_account_id,model_name)
);
CREATE TABLE IF NOT EXISTS connection_health_model_control_targets (
 id text PRIMARY KEY,user_id text NOT NULL,admin_account_id text NOT NULL,target_id text NOT NULL,model_name text NOT NULL,
 account_name text NOT NULL DEFAULT '',closed_entries jsonb NOT NULL DEFAULT '{}',closed_at timestamptz NULL,closed_by text NULL,closed_batch_id text NULL,closed_accuracy_percent double precision NULL,
 conflict_reason text NOT NULL DEFAULT '',pending jsonb NULL,last_pending_id text NOT NULL DEFAULT '',unconfirmed_close jsonb NULL,
 observed_state text NOT NULL DEFAULT 'unverified' CHECK(observed_state IN ('unverified','serving','partially_closed','closed','not_provided','not_isolatable','last_model','account_missing')),
 observed_reason text NOT NULL DEFAULT '',observed_sources jsonb NOT NULL DEFAULT '[]',observed_account_status text NOT NULL DEFAULT '',observed_account_schedulable boolean NULL,observed_at timestamptz NULL,
 last_attempt jsonb NULL,attempt_at timestamptz NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>=1),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,admin_account_id,target_id,model_name),
 FOREIGN KEY(user_id,admin_account_id,model_name) REFERENCES connection_health_model_control_rules(user_id,admin_account_id,model_name) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS connection_health_model_control_events (
 id text PRIMARY KEY,user_id text NOT NULL,admin_account_id text NOT NULL,target_id text NOT NULL DEFAULT '',model_name text NOT NULL,event_type text NOT NULL,
 actor_user_id text NOT NULL,basis jsonb NOT NULL DEFAULT '{}',detail jsonb NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now()
);
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_rules'::regclass AND conname='connection_health_model_control_rules_workspace_fk') THEN
   ALTER TABLE connection_health_model_control_rules ADD CONSTRAINT connection_health_model_control_rules_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_targets'::regclass AND conname='connection_health_model_control_targets_workspace_fk') THEN
   ALTER TABLE connection_health_model_control_targets ADD CONSTRAINT connection_health_model_control_targets_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_events'::regclass AND conname='connection_health_model_control_events_workspace_fk') THEN
   ALTER TABLE connection_health_model_control_events ADD CONSTRAINT connection_health_model_control_events_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_model_control_events_workspace ON connection_health_model_control_events(user_id,admin_account_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_control_events_target ON connection_health_model_control_events(user_id,admin_account_id,target_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_question_answer_records_target_model_recent ON connection_health_question_answer_records(user_id,target_id,model_name,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS connection_health_model_control_settings (
 user_id text NOT NULL, admin_account_id text NOT NULL,
 min_accuracy_percent integer NOT NULL DEFAULT 50 CHECK(min_accuracy_percent BETWEEN 1 AND 100),
 min_judged_answers integer NOT NULL DEFAULT 3 CHECK(min_judged_answers BETWEEN 1 AND 50),
 version bigint NOT NULL DEFAULT 1 CHECK(version>=1), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,admin_account_id)
);
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_settings'::regclass AND conname='connection_health_model_control_settings_workspace_fk') THEN
  ALTER TABLE connection_health_model_control_settings ADD CONSTRAINT connection_health_model_control_settings_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
 END IF;
END $$;
`

func (r *Repository) ensureModelControlSchema(ctx context.Context) error {
	_, err := r.db.Exec(ctx, modelControlSchemaSQL)
	return err
}

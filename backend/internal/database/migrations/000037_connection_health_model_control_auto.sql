-- rollback-safe: automatic controls are opt-in and use existing ownership timestamps.
CREATE TABLE IF NOT EXISTS connection_health_model_control_auto (
 user_id text NOT NULL, admin_account_id text NOT NULL,
 auto_close_enabled boolean NOT NULL DEFAULT false,
 auto_restore_enabled boolean NOT NULL DEFAULT false,
 health_coordination_enabled boolean NOT NULL DEFAULT false,
 auto_close_enabled_at timestamptz NULL, auto_restore_enabled_at timestamptz NULL,
 verify_interval_minutes integer NOT NULL DEFAULT 10 CHECK(verify_interval_minutes BETWEEN 5 AND 60),
 retry_interval_minutes integer NOT NULL DEFAULT 5 CHECK(retry_interval_minutes BETWEEN 1 AND 60),
 version bigint NOT NULL DEFAULT 1 CHECK(version>=1), updated_at timestamptz NOT NULL DEFAULT now(),
 last_scan_started_at timestamptz NULL, last_scan_finished_at timestamptz NULL, last_scan_error_key text NOT NULL DEFAULT '',
 PRIMARY KEY(user_id,admin_account_id)
);
ALTER TABLE connection_health_model_control_targets ADD COLUMN IF NOT EXISTS auto_state jsonb NULL;
ALTER TABLE connection_health_model_control_targets ADD COLUMN IF NOT EXISTS auto_verify_at timestamptz NULL;
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_auto'::regclass AND conname='connection_health_model_control_auto_workspace_fk') THEN
  ALTER TABLE connection_health_model_control_auto ADD CONSTRAINT connection_health_model_control_auto_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
 END IF;
END $$;

CREATE TABLE IF NOT EXISTS signup_risk_settings (
 user_id text NOT NULL,
 admin_account_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 initialized boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (user_id,admin_account_id),
 CHECK (enabled OR NOT initialized)
);
CREATE TABLE IF NOT EXISTS signup_risk_processed_users (
 user_id text NOT NULL,
 admin_account_id text NOT NULL,
 site_key text NOT NULL CHECK (site_key ~ '^[0-9a-f]{64}$'),
 remote_user_id text NOT NULL CHECK (length(remote_user_id) BETWEEN 1 AND 512),
 PRIMARY KEY (user_id,admin_account_id,site_key,remote_user_id),
 FOREIGN KEY (user_id,admin_account_id) REFERENCES signup_risk_settings(user_id,admin_account_id) ON DELETE CASCADE
);
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM pg_constraint WHERE conrelid='signup_risk_settings'::regclass AND conname='signup_risk_settings_workspace_fk'
 ) THEN
  ALTER TABLE signup_risk_settings ADD CONSTRAINT signup_risk_settings_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
 END IF;
END $$;

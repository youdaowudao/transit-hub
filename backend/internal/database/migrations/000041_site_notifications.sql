CREATE TABLE IF NOT EXISTS site_notifications (
 id text PRIMARY KEY,
 user_id text NOT NULL,
 admin_account_id text NOT NULL,
 kind text NOT NULL,
 event_key text NOT NULL,
 subject_type text NOT NULL,
 subject_id text NOT NULL,
 level smallint NOT NULL CHECK (level BETWEEN 1 AND 4),
 discovered_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 content jsonb NOT NULL CHECK (jsonb_typeof(content)='object'),
 read_at timestamptz NULL,
 reminder_completed_at timestamptz NULL,
 UNIQUE (user_id,admin_account_id,kind,event_key)
);
CREATE INDEX IF NOT EXISTS site_notifications_history_idx ON site_notifications(user_id,admin_account_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS site_notifications_unread_idx ON site_notifications(user_id,admin_account_id,created_at DESC,id DESC) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS site_notifications_reminder_idx ON site_notifications(user_id,admin_account_id,level,created_at,id) WHERE reminder_completed_at IS NULL;
CREATE TABLE IF NOT EXISTS site_notification_preferences (
 user_id text NOT NULL,
 admin_account_id text NOT NULL,
 poll_interval_seconds integer NOT NULL DEFAULT 300 CHECK (poll_interval_seconds BETWEEN 60 AND 3600 AND poll_interval_seconds%60=0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (user_id,admin_account_id)
);
CREATE TABLE IF NOT EXISTS upstream_rate_notice_baselines (
 user_id text NOT NULL,
 admin_account_id text NOT NULL,
 site_id text NOT NULL,
 group_key text NOT NULL,
 group_id text NOT NULL,
 group_name text NOT NULL,
 multiplier double precision NOT NULL CHECK (multiplier>=0 AND multiplier<'Infinity'::double precision),
 observed_at timestamptz NOT NULL,
 PRIMARY KEY (user_id,admin_account_id,site_id,group_key)
);
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL THEN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='site_notifications'::regclass AND conname='site_notifications_workspace_fk') THEN
   ALTER TABLE site_notifications ADD CONSTRAINT site_notifications_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='site_notification_preferences'::regclass AND conname='site_notification_preferences_workspace_fk') THEN
   ALTER TABLE site_notification_preferences ADD CONSTRAINT site_notification_preferences_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='upstream_rate_notice_baselines'::regclass AND conname='upstream_rate_notice_baselines_workspace_fk') THEN
   ALTER TABLE upstream_rate_notice_baselines ADD CONSTRAINT upstream_rate_notice_baselines_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
  END IF;
 END IF;
 IF to_regclass('upstream_sites') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='upstream_rate_notice_baselines'::regclass AND conname='upstream_rate_notice_baselines_site_fk') THEN
  ALTER TABLE upstream_rate_notice_baselines ADD CONSTRAINT upstream_rate_notice_baselines_site_fk FOREIGN KEY(site_id) REFERENCES upstream_sites(id) ON DELETE CASCADE;
 END IF;
END $$;

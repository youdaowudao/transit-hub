CREATE TABLE IF NOT EXISTS user_activity_profiles (
 user_id text NOT NULL,admin_account_id text NOT NULL,site_user_id text NOT NULL,
 email text NOT NULL,username text NOT NULL,role text NOT NULL,status text NOT NULL,balance double precision,
 site_created_at timestamptz,last_used_at timestamptz,first_observed_at timestamptz NOT NULL,updated_at timestamptz NOT NULL,
 PRIMARY KEY(user_id,admin_account_id,site_user_id)
);
CREATE TABLE IF NOT EXISTS user_activity_daily (
 user_id text NOT NULL,admin_account_id text NOT NULL,site_user_id text NOT NULL,day date NOT NULL,
 requests bigint NOT NULL,tokens bigint NOT NULL,actual_cost double precision NOT NULL,
 PRIMARY KEY(user_id,admin_account_id,site_user_id,day)
);
CREATE TABLE IF NOT EXISTS user_activity_recharge_daily (
 user_id text NOT NULL,admin_account_id text NOT NULL,site_user_id text NOT NULL,day date NOT NULL,channel text NOT NULL,
 amount double precision NOT NULL,events integer NOT NULL,
 PRIMARY KEY(user_id,admin_account_id,site_user_id,day,channel)
);
CREATE TABLE IF NOT EXISTS user_activity_site_daily (
 user_id text NOT NULL,admin_account_id text NOT NULL,day date NOT NULL,total_balance double precision,user_count integer NOT NULL,observed_at timestamptz NOT NULL,
 PRIMARY KEY(user_id,admin_account_id,day)
);
CREATE TABLE IF NOT EXISTS user_activity_sync_states (
 user_id text NOT NULL,admin_account_id text NOT NULL,status text NOT NULL,error_detail text NOT NULL DEFAULT '',warning_detail text NOT NULL DEFAULT '',
 usage_from date,usage_to date,recharge_from date,recharge_to date,user_count integer NOT NULL DEFAULT 0,last_started_at timestamptz,last_synced_at timestamptz,today_usage_observed_at timestamptz,
 PRIMARY KEY(user_id,admin_account_id)
);
CREATE INDEX IF NOT EXISTS user_activity_daily_workspace_day ON user_activity_daily(user_id,admin_account_id,day);
CREATE INDEX IF NOT EXISTS user_activity_recharge_workspace_day ON user_activity_recharge_daily(user_id,admin_account_id,day);

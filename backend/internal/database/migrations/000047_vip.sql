CREATE TABLE IF NOT EXISTS vip_watch (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    site_user_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('manual', 'dismissed')),
    note text NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, admin_account_id, site_user_id)
);
CREATE TABLE IF NOT EXISTS vip_settings (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    daily_top integer NOT NULL DEFAULT 20 CHECK (daily_top BETWEEN 0 AND 100),
    lifetime_top integer NOT NULL DEFAULT 20 CHECK (lifetime_top BETWEEN 0 AND 100),
    alert_days double precision NOT NULL DEFAULT 2 CHECK (alert_days BETWEEN 0 AND 30),
    alert_balance double precision NOT NULL DEFAULT 20 CHECK (alert_balance BETWEEN 0 AND 1000000),
    alerts_enabled boolean NOT NULL DEFAULT false,
    bot_ids text[] NOT NULL DEFAULT '{}',
    reminder_text text NOT NULL DEFAULT '',
    empty_reminder_text text NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, admin_account_id),
    CHECK (cardinality(bot_ids) <= 1)
);
CREATE TABLE IF NOT EXISTS vip_alert_states (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    site_user_id text NOT NULL,
    state text NOT NULL CHECK (state IN ('low', 'empty')),
    notified_at timestamptz NOT NULL,
    recovered boolean NOT NULL DEFAULT false,
    delivery_status text NOT NULL CHECK (delivery_status IN ('sending', 'sent', 'unknown')),
    PRIMARY KEY (user_id, admin_account_id, site_user_id)
);

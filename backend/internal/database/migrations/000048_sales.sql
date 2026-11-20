CREATE TABLE IF NOT EXISTS sales_excluded_entries (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    site_user_id text NOT NULL,
    day date NOT NULL,
    channel text NOT NULL CHECK (channel IN ('payment','redeem','admin_add','admin_deduct','affiliate')),
    amount double precision NOT NULL CHECK (amount > 0 AND amount < 'Infinity'::float8),
    events integer NOT NULL CHECK (events >= 0),
    PRIMARY KEY (user_id,admin_account_id,site_user_id,day,channel)
);
CREATE TABLE IF NOT EXISTS sales_settings (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    excluded_channels text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (user_id,admin_account_id),
    CHECK (excluded_channels <@ ARRAY['payment','redeem','admin_add','admin_deduct','affiliate']::text[])
);

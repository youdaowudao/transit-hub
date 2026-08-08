ALTER TABLE IF EXISTS upstream_sites
    ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;

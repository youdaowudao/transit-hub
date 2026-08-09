ALTER TABLE IF EXISTS connection_health_policies
    ADD COLUMN IF NOT EXISTS priority_writeback_spread_seconds integer NOT NULL DEFAULT 1;

ALTER TABLE IF EXISTS connection_health_priority_workspace_sync_states
    ADD COLUMN IF NOT EXISTS writeback_spread_seconds integer NOT NULL DEFAULT 1;

ALTER TABLE IF EXISTS connection_health_priority_workspace_sync_states
    ADD COLUMN IF NOT EXISTS pending_target_count integer NOT NULL DEFAULT 0;

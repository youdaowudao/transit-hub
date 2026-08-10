ALTER TABLE IF EXISTS connection_health_priority_workspace_sync_states
    ADD COLUMN IF NOT EXISTS last_write_round_target_count integer NOT NULL DEFAULT 0;

-- Stage A: extend the existing Target checkpoint; no pause table or pause behavior.
ALTER TABLE IF EXISTS connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_action_kind text NOT NULL DEFAULT '' CHECK (pending_action_kind IN ('','status','schedulable','delete'));
ALTER TABLE IF EXISTS connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_schedulable boolean NULL;
ALTER TABLE IF EXISTS connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_source text NOT NULL DEFAULT '' CHECK (pending_source IN ('','automatic','manual','manual_delete','compensate_delete'));
ALTER TABLE IF EXISTS connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_group_ids text[] NOT NULL DEFAULT '{}';
ALTER TABLE IF EXISTS connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_had_automatic_baseline boolean NOT NULL DEFAULT false;

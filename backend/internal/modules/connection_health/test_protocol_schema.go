package connection_health

import "context"

// Keep this list aligned with migration 000029. Repeated execution never revives invalid evidence.
func (r *Repository) ensureTestProtocolSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS connection_health_group_test_configs (
 user_id text NOT NULL, admin_account_id text NOT NULL, admin_group_id text NOT NULL,
 protocol text NOT NULL CHECK (protocol IN ('chat_completions','responses')),
 probe_timeout_seconds integer NOT NULL CHECK (probe_timeout_seconds BETWEEN 5 AND 120),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (user_id,admin_account_id,admin_group_id)
)`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_probe_protocol text NULL CHECK (last_probe_protocol IS NULL OR last_probe_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_probe_timeout_seconds integer NULL CHECK (last_probe_timeout_seconds IS NULL OR last_probe_timeout_seconds BETWEEN 5 AND 120)`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_applied_probe_at timestamptz NULL`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_applied_probe_result text NULL`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_applied_probe_protocol text NULL CHECK (last_applied_probe_protocol IS NULL OR last_applied_probe_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS counter_protocol text NULL CHECK (counter_protocol IS NULL OR counter_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS health_evidence_protocol text NULL CHECK (health_evidence_protocol IS NULL OR health_evidence_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_success_protocol text NULL CHECK (last_success_protocol IS NULL OR last_success_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_credential_failure_at timestamptz NULL`,
		`ALTER TABLE connection_health_states ADD COLUMN IF NOT EXISTS last_credential_failure_reason text NOT NULL DEFAULT ''`,
		`DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='connection_health_states'::regclass AND attname='health_evidence_status' AND NOT attisdropped) THEN
  ALTER TABLE connection_health_states ADD COLUMN health_evidence_status text NOT NULL DEFAULT 'legacy' CHECK (health_evidence_status IN ('legacy','valid','invalid'));
 END IF;
 ALTER TABLE connection_health_states ALTER COLUMN health_evidence_status SET DEFAULT 'invalid';
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_states'::regclass AND conname='connection_health_evidence_source') THEN
  ALTER TABLE connection_health_states ADD CONSTRAINT connection_health_evidence_source CHECK ((health_evidence_status='valid' AND health_evidence_protocol IS NOT NULL) OR (health_evidence_status IN ('legacy','invalid') AND health_evidence_protocol IS NULL));
 END IF;
END $$`,
		`ALTER TABLE connection_health_events ADD COLUMN IF NOT EXISTS request_protocol text NULL CHECK (request_protocol IS NULL OR request_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_events ADD COLUMN IF NOT EXISTS request_timeout_seconds integer NULL CHECK (request_timeout_seconds IS NULL OR request_timeout_seconds BETWEEN 5 AND 120)`,
		`ALTER TABLE connection_health_events ADD COLUMN IF NOT EXISTS probe_disposition text NULL CHECK (probe_disposition IS NULL OR probe_disposition IN ('applied','invalid','stale'))`,
		`ALTER TABLE connection_health_question_answer_records ADD COLUMN IF NOT EXISTS request_protocol text NULL CHECK (request_protocol IS NULL OR request_protocol IN ('chat_completions','responses'))`,
		`ALTER TABLE connection_health_priority_sync_states ADD COLUMN IF NOT EXISTS pending_dispatch_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE connection_health_priority_sync_states ADD COLUMN IF NOT EXISTS pending_owner_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE connection_health_priority_sync_states ADD COLUMN IF NOT EXISTS pending_dispatch_phase text NOT NULL DEFAULT '' CHECK (pending_dispatch_phase IN ('','prepared','sending','uncertain','not_sent','confirmed_applied','confirmed_rejected'))`,
		`ALTER TABLE connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_dispatch_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_owner_id text NOT NULL DEFAULT ''`,
		`ALTER TABLE connection_health_target_action_states ADD COLUMN IF NOT EXISTS pending_dispatch_phase text NOT NULL DEFAULT '' CHECK (pending_dispatch_phase IN ('','prepared','sending','uncertain','not_sent','confirmed_applied','confirmed_rejected'))`,
		`DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_priority_sync_states'::regclass AND conname='connection_health_priority_sync_states_dispatch_identity') THEN
  ALTER TABLE connection_health_priority_sync_states ADD CONSTRAINT connection_health_priority_sync_states_dispatch_identity CHECK (
   (pending_dispatch_id='' AND pending_owner_id='' AND pending_dispatch_phase='') OR
   (pending_dispatch_id<>'' AND pending_owner_id<>'' AND pending_dispatch_phase<>'')
  );
 END IF;
END $$`,
		`DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_target_action_states'::regclass AND conname='connection_health_target_action_states_dispatch_identity') THEN
  ALTER TABLE connection_health_target_action_states ADD CONSTRAINT connection_health_target_action_states_dispatch_identity CHECK (
   (pending_dispatch_id='' AND pending_owner_id='' AND pending_dispatch_phase='') OR
   (pending_dispatch_id<>'' AND pending_owner_id<>'' AND pending_dispatch_phase<>'')
  );
 END IF;
END $$`,
	}
	for _, statement := range statements {
		if _, err := r.db.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

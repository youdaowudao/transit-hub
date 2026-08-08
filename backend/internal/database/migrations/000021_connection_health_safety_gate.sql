-- A-F health safety gate: workspace settings, abnormal queue, circuit epoch,
-- idempotent emergency clear and Sub2API mutation fencing.
CREATE TABLE IF NOT EXISTS connection_health_safety_settings (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    confirmation_observation_count integer NOT NULL DEFAULT 4,
    confirmation_delays_seconds jsonb NOT NULL DEFAULT '[2,5,10]'::jsonb,
    confirmation_jitter_seconds integer NOT NULL DEFAULT 1,
    abnormal_queue_capacity integer NOT NULL DEFAULT 64,
    manual_reserved_slots integer NOT NULL DEFAULT 1,
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id)
);

CREATE TABLE IF NOT EXISTS connection_health_safety_epochs (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    abnormal_queue_epoch bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id)
);

CREATE TABLE IF NOT EXISTS connection_health_abnormal_queue (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    account_id text NOT NULL DEFAULT '',
    model_name text NOT NULL DEFAULT '',
    provider_family text NOT NULL DEFAULT '',
    probe_prompt text NOT NULL DEFAULT '',
    max_probe_tokens integer NOT NULL DEFAULT 1,
    queue_kind text NOT NULL,
    source text NOT NULL,
    incident_id text NOT NULL DEFAULT '',
    fault_domain text NOT NULL DEFAULT '',
    observation_epoch bigint NOT NULL DEFAULT 0,
    normal_generation bigint NOT NULL DEFAULT 0,
    abnormal_queue_epoch bigint NOT NULL DEFAULT 0,
    attempt integer NOT NULL DEFAULT 0,
    required_attempts integer NOT NULL DEFAULT 4,
    confirmation_delays_seconds jsonb NOT NULL DEFAULT '[2,5,10]'::jsonb,
    confirmation_jitter_seconds integer NOT NULL DEFAULT 1,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    action_key text NOT NULL,
    mutation_generation bigint NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'queued',
    claimed_by text NOT NULL DEFAULT '',
    claim_expires_at timestamptz NULL,
    expected_result text NOT NULL DEFAULT '',
    last_result text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, admin_account_id, action_key)
);
CREATE INDEX IF NOT EXISTS idx_connection_health_abnormal_queue_due
    ON connection_health_abnormal_queue (user_id, admin_account_id, state, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_connection_health_abnormal_queue_source
    ON connection_health_abnormal_queue (user_id, admin_account_id, source, abnormal_queue_epoch, state);
CREATE UNIQUE INDEX IF NOT EXISTS idx_connection_health_abnormal_queue_active_domain
    ON connection_health_abnormal_queue (user_id, admin_account_id, fault_domain)
    WHERE fault_domain <> '' AND state IN ('claimed','dispatching');

CREATE TABLE IF NOT EXISTS connection_health_incidents (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    fault_domain text NOT NULL,
    state text NOT NULL DEFAULT 'closed',
    normal_generation bigint NOT NULL DEFAULT 0,
    canary_target_id text NOT NULL DEFAULT '',
    successful_canary_target_id text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, admin_account_id, fault_domain)
);

CREATE TABLE IF NOT EXISTS connection_health_incident_observations (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    fault_domain text NOT NULL,
    normal_generation bigint NOT NULL,
    account_id text NOT NULL,
    target_id text NOT NULL,
    result text NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, fault_domain, normal_generation, account_id)
);

CREATE TABLE IF NOT EXISTS connection_health_target_fault_domains (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    target_id text NOT NULL,
    endpoint text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, target_id)
);

CREATE TABLE IF NOT EXISTS connection_health_safety_audits (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    audit_type text NOT NULL,
    actor text NOT NULL DEFAULT '',
    old_value jsonb NOT NULL DEFAULT '{}'::jsonb,
    new_value jsonb NOT NULL DEFAULT '{}'::jsonb,
    detail text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS connection_health_safety_inventory_snapshots (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    generation bigint NOT NULL,
    complete boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, generation)
);

CREATE TABLE IF NOT EXISTS connection_health_safety_inventory_accounts (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    generation bigint NOT NULL,
    account_id text NOT NULL,
    target_id text NOT NULL,
    active boolean NOT NULL DEFAULT false,
    schedulable boolean NOT NULL DEFAULT false,
    status_known boolean NOT NULL DEFAULT false,
    schedulable_known boolean NOT NULL DEFAULT false,
    capability_known boolean NOT NULL DEFAULT false,
    membership_known boolean NOT NULL DEFAULT false,
    models jsonb NOT NULL DEFAULT '[]'::jsonb,
    group_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_success_at timestamptz NULL,
    confirmed_failure_models integer NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, admin_account_id, generation, account_id)
);

CREATE TABLE IF NOT EXISTS connection_health_mutation_fences (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    account_id text NOT NULL,
    generation bigint NOT NULL DEFAULT 0,
    lease_owner text NOT NULL DEFAULT '',
    fencing_token bigint NOT NULL DEFAULT 0,
    lease_expires_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, account_id)
);

CREATE TABLE IF NOT EXISTS connection_health_emergency_clears (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    idempotency_key text NOT NULL,
    result jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_connection_health_emergency_clears_expiry
    ON connection_health_emergency_clears (expires_at);

CREATE TABLE IF NOT EXISTS connection_health_floor_reservations (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    account_id text NOT NULL,
    incident_id text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    inventory_generation bigint NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    dispatching_at timestamptz NULL,
    readback_at timestamptz NULL,
    snapshot_invalidated_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE connection_health_floor_reservations
    ADD COLUMN IF NOT EXISTS dispatching_at timestamptz NULL;
CREATE INDEX IF NOT EXISTS idx_connection_health_floor_reservations_workspace
    ON connection_health_floor_reservations (user_id, admin_account_id, expires_at);

CREATE TABLE IF NOT EXISTS connection_health_incident_survivors (
    user_id text NOT NULL,
    admin_account_id text NOT NULL,
    incident_id text NOT NULL,
    scope_kind text NOT NULL,
    scope_id text NOT NULL,
    account_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, admin_account_id, incident_id, scope_kind, scope_id)
);

ALTER TABLE connection_health_priority_sync_states
    ADD COLUMN IF NOT EXISTS pending_mutation_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE connection_health_priority_sync_states
    ADD COLUMN IF NOT EXISTS pending_source text NOT NULL DEFAULT '';
ALTER TABLE connection_health_priority_sync_states
    ADD COLUMN IF NOT EXISTS pending_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE connection_health_priority_sync_states
    ADD COLUMN IF NOT EXISTS pending_action_key text NOT NULL DEFAULT '';

ALTER TABLE connection_health_target_action_states
    ADD COLUMN IF NOT EXISTS pending_mutation_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE connection_health_target_action_states
    ADD COLUMN IF NOT EXISTS pending_source text NOT NULL DEFAULT '';
ALTER TABLE connection_health_target_action_states
    ADD COLUMN IF NOT EXISTS pending_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE connection_health_target_action_states
    ADD COLUMN IF NOT EXISTS pending_action_key text NOT NULL DEFAULT '';

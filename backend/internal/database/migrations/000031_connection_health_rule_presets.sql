-- Additive rule/preset upgrade. Runtime tables can be absent on fresh installs.
CREATE TABLE IF NOT EXISTS connection_health_workspace_settings (
 user_id text NOT NULL, admin_account_id text NOT NULL DEFAULT '',
 rule_version text NOT NULL DEFAULT 'v2' CHECK (rule_version IN ('v2','legacy')),
 config_generation bigint NOT NULL DEFAULT 0,
 probe_concurrency integer NOT NULL DEFAULT 6 CHECK (probe_concurrency BETWEEN 1 AND 10),
 probe_concurrency_version bigint NOT NULL DEFAULT 0,
 rule_switched_at timestamptz NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (user_id,admin_account_id)
);
CREATE TABLE IF NOT EXISTS connection_health_rule_presets (
 id text PRIMARY KEY, user_id text NOT NULL, admin_account_id text NOT NULL DEFAULT '',
 name text NOT NULL, kind text NOT NULL CHECK (kind IN ('recommended','legacy_snapshot','legacy_default','custom')),
 failure_threshold integer NOT NULL CONSTRAINT connection_health_rule_presets_failure_range CHECK (kind='legacy_snapshot' OR failure_threshold BETWEEN 2 AND 10),
 success_threshold integer NOT NULL CONSTRAINT connection_health_rule_presets_success_range CHECK (kind='legacy_snapshot' OR success_threshold BETWEEN 1 AND 10),
 cooldown_seconds integer NOT NULL CONSTRAINT connection_health_rule_presets_cooldown_range CHECK (kind='legacy_snapshot' OR cooldown_seconds BETWEEN 60 AND 3600),
 failed_retry_interval_seconds integer NOT NULL CHECK (failed_retry_interval_seconds BETWEEN 60 AND 3600),
 long_failure_after_seconds integer NOT NULL CHECK (long_failure_after_seconds BETWEEN 3600 AND 604800),
 long_failure_interval_seconds integer NOT NULL CHECK (long_failure_interval_seconds BETWEEN 600 AND 86400),
 delay_line_ms jsonb NOT NULL DEFAULT '{"responses":10000,"chat_completions":5000}',
 observation_seconds integer NOT NULL, recovery_step_percent integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (user_id,admin_account_id,name)
);
-- Earlier development candidates used unconditional bounds. Preserve every
-- frozen legacy value while retaining strict bounds for editable presets.
DO $$
DECLARE c record;
BEGIN
 FOR c IN SELECT * FROM (VALUES
  ('connection_health_rule_presets_failure_threshold_check','connection_health_rule_presets_failure_range','kind=''legacy_snapshot'' OR failure_threshold BETWEEN 2 AND 10'),
  ('connection_health_rule_presets_success_threshold_check','connection_health_rule_presets_success_range','kind=''legacy_snapshot'' OR success_threshold BETWEEN 1 AND 10'),
  ('connection_health_rule_presets_cooldown_seconds_check','connection_health_rule_presets_cooldown_range','kind=''legacy_snapshot'' OR cooldown_seconds BETWEEN 60 AND 3600')
 ) AS bounds(old_name,new_name,expression) LOOP
  IF EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_rule_presets'::regclass AND conname=c.old_name) THEN
   EXECUTE format('ALTER TABLE connection_health_rule_presets DROP CONSTRAINT %I',c.old_name);
  END IF;
  IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_rule_presets'::regclass AND conname=c.new_name) THEN
   EXECUTE format('ALTER TABLE connection_health_rule_presets ADD CONSTRAINT %I CHECK (%s)',c.new_name,c.expression);
  END IF;
 END LOOP;
END $$;
CREATE INDEX IF NOT EXISTS idx_connection_health_rule_presets_workspace ON connection_health_rule_presets(user_id,admin_account_id);
ALTER TABLE IF EXISTS connection_health_policies ADD COLUMN IF NOT EXISTS rule_preset_id text NOT NULL DEFAULT '';
ALTER TABLE IF EXISTS connection_health_policies ADD COLUMN IF NOT EXISTS legacy_preset_id text NOT NULL DEFAULT '';
ALTER TABLE IF EXISTS connection_health_states ADD COLUMN IF NOT EXISTS rule_version text NOT NULL DEFAULT '';
ALTER TABLE IF EXISTS connection_health_states ADD COLUMN IF NOT EXISTS failing_since timestamptz NULL;
ALTER TABLE IF EXISTS connection_health_states ADD COLUMN IF NOT EXISTS recheck_pending boolean NOT NULL DEFAULT false;
ALTER TABLE IF EXISTS connection_health_states ADD COLUMN IF NOT EXISTS last_first_token_ms integer NULL;
ALTER TABLE IF EXISTS connection_health_states ADD COLUMN IF NOT EXISTS last_first_event_ms integer NULL;
ALTER TABLE IF EXISTS connection_health_events ADD COLUMN IF NOT EXISTS rule_version text NOT NULL DEFAULT '';
ALTER TABLE IF EXISTS connection_health_events ADD COLUMN IF NOT EXISTS first_token_ms integer NULL;
ALTER TABLE IF EXISTS connection_health_events ADD COLUMN IF NOT EXISTS first_event_ms integer NULL;
ALTER TABLE IF EXISTS connection_health_events ADD COLUMN IF NOT EXISTS long_failure boolean NULL;

-- The migration and runtime switch invoke this same conversion function.
CREATE OR REPLACE FUNCTION connection_health_convert_states(p_user text,p_workspace text,p_rule text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF p_rule NOT IN ('v2','legacy') THEN RAISE EXCEPTION 'invalid health rule'; END IF;
 UPDATE connection_health_states SET
 rule_version=p_rule,
 recheck_pending=CASE WHEN state='disabled' THEN recheck_pending ELSE false END,
 state=CASE WHEN state='disabled' THEN state
  WHEN p_rule='v2' AND state IN ('degraded','recovering') THEN 'degraded'
  WHEN p_rule='v2' AND state='observing' THEN 'suspended'
  WHEN p_rule='legacy' AND state IN ('healthy','suspect') THEN 'healthy' ELSE state END,
 current_weight=CASE WHEN state='disabled' THEN current_weight WHEN state IN ('healthy','suspect') THEN 100 WHEN state IN ('degraded','recovering') THEN 75 ELSE 0 END,
 consecutive_failures=CASE WHEN state='disabled' THEN consecutive_failures WHEN state IN ('healthy','suspect') THEN 0 WHEN p_rule='legacy' AND state='degraded' THEN LEAST(consecutive_failures,1) ELSE consecutive_failures END,
 consecutive_successes=CASE WHEN state='disabled' THEN consecutive_successes WHEN p_rule='v2' AND state='observing' THEN 1 ELSE 0 END,
 cooldown_until=CASE WHEN state IN ('disabled','suspended') THEN cooldown_until ELSE NULL END,
 observing_until=CASE WHEN state='disabled' THEN observing_until ELSE NULL END,
 failing_since=CASE WHEN state IN ('healthy','suspect') OR (p_rule='v2' AND state='observing') THEN NULL ELSE failing_since END
 WHERE user_id=p_user AND admin_account_id=p_workspace AND connection_id LIKE 'sub2api:%' AND rule_version IS DISTINCT FROM p_rule;
END $$;
DO $$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.connection_health_convert_states(text,text,text) SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END $$;
-- End runtime schema.

DO $$
DECLARE w record; p record; sid text; base_name text; chosen_name text; suffix integer;
BEGIN
 IF to_regclass('connection_health_policies') IS NOT NULL THEN
  INSERT INTO connection_health_workspace_settings(user_id,admin_account_id,config_generation,rule_switched_at)
  SELECT DISTINCT user_id,admin_account_id,1,now() FROM connection_health_policies ON CONFLICT DO NOTHING;
 END IF;
 IF to_regclass('connection_health_states') IS NOT NULL THEN
  INSERT INTO connection_health_workspace_settings(user_id,admin_account_id,config_generation,rule_switched_at)
  SELECT DISTINCT user_id,admin_account_id,1,now() FROM connection_health_states ON CONFLICT DO NOTHING;
 END IF;
 FOR w IN SELECT * FROM connection_health_workspace_settings LOOP
  INSERT INTO connection_health_rule_presets(id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,observation_seconds,recovery_step_percent)
  VALUES ('recommended:'||md5(length(w.user_id)::text||':'||w.user_id||w.admin_account_id),w.user_id,w.admin_account_id,'新规则（推荐）','recommended',3,2,300,600,86400,3600,300,25),
   ('legacy_default:'||md5(length(w.user_id)::text||':'||w.user_id||w.admin_account_id),w.user_id,w.admin_account_id,'旧规则（默认值）','legacy_default',3,2,300,600,86400,3600,300,25)
  ON CONFLICT DO NOTHING;
  IF to_regclass('connection_health_policies') IS NOT NULL THEN
   FOR p IN SELECT failure_threshold,success_threshold,cooldown_seconds,observation_seconds,recovery_step_percent,string_agg(name,'、' ORDER BY created_at,id) AS names,count(*) AS count
    FROM connection_health_policies WHERE user_id=w.user_id AND admin_account_id=w.admin_account_id AND legacy_preset_id=''
    GROUP BY failure_threshold,success_threshold,cooldown_seconds,observation_seconds,recovery_step_percent LOOP
    sid:='legacy-snapshot:'||md5(length(w.user_id)::text||':'||w.user_id||w.admin_account_id||':'||p.failure_threshold||':'||p.success_threshold||':'||p.cooldown_seconds||':'||p.observation_seconds||':'||p.recovery_step_percent);
    base_name:='升级前 · '||p.names;
    IF length(base_name)>60 THEN base_name:=left(base_name,45)||'等 '||p.count||' 个策略'; END IF;
    chosen_name:=base_name;suffix:=2;
    WHILE EXISTS (SELECT 1 FROM connection_health_rule_presets WHERE user_id=w.user_id AND admin_account_id=w.admin_account_id AND name=chosen_name AND id<>sid) LOOP
     chosen_name:=base_name||'（'||suffix||'）';suffix:=suffix+1;
    END LOOP;
    INSERT INTO connection_health_rule_presets(id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,observation_seconds,recovery_step_percent)
     VALUES(sid,w.user_id,w.admin_account_id,chosen_name,'legacy_snapshot',p.failure_threshold,p.success_threshold,p.cooldown_seconds,600,86400,3600,p.observation_seconds,p.recovery_step_percent) ON CONFLICT DO NOTHING;
    UPDATE connection_health_policies SET legacy_preset_id=sid,rule_preset_id='recommended:'||md5(length(w.user_id)::text||':'||w.user_id||w.admin_account_id)
     WHERE user_id=w.user_id AND admin_account_id=w.admin_account_id AND legacy_preset_id='' AND failure_threshold=p.failure_threshold AND success_threshold=p.success_threshold AND cooldown_seconds=p.cooldown_seconds AND observation_seconds=p.observation_seconds AND recovery_step_percent=p.recovery_step_percent;
   END LOOP;
  END IF;
 END LOOP;
 IF to_regclass('connection_health_states') IS NOT NULL THEN
  IF to_regclass('connection_health_events') IS NOT NULL THEN
   UPDATE connection_health_states s SET failing_since=(SELECT min(e.created_at) FROM connection_health_events e
    WHERE e.connection_id=s.connection_id AND e.model_name=s.model_name AND e.user_id=s.user_id AND e.admin_account_id=s.admin_account_id
    AND e.request_protocol=s.counter_protocol AND e.probe_disposition='applied'
    AND e.result IN ('network_fluctuation','rate_limited','server_error','auth','model_not_found')
    AND (s.last_success_at IS NULL OR e.created_at>s.last_success_at)
    AND e.created_at>COALESCE((SELECT max(other.created_at) FROM connection_health_events other WHERE other.connection_id=s.connection_id AND other.model_name=s.model_name AND other.request_protocol IS DISTINCT FROM s.counter_protocol),'-infinity'::timestamptz))
   WHERE s.connection_id LIKE 'sub2api:%' AND s.consecutive_failures>0 AND s.failing_since IS NULL AND s.rule_version='';
  END IF;
  FOR w IN SELECT * FROM connection_health_workspace_settings LOOP
   PERFORM connection_health_convert_states(w.user_id,w.admin_account_id,'v2');
  END LOOP;
 END IF;
END $$;

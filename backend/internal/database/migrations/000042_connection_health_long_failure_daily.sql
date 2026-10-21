DO $$ BEGIN
IF to_regclass('connection_health_rule_presets') IS NOT NULL THEN
  UPDATE connection_health_rule_presets
  SET long_failure_interval_seconds=86400, updated_at=now()
  WHERE kind='recommended' AND long_failure_interval_seconds=3600;
END IF;
END $$;

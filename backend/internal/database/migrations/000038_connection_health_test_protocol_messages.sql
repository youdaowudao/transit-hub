-- C4.1: widen only health probe protocol checks; question-answer records stay unchanged.
DO $$
DECLARE
 target record;
 table_id regclass;
 column_number smallint;
 old_constraint record;
BEGIN
 FOR target IN SELECT * FROM (VALUES
  ('connection_health_group_test_configs','protocol',false),
  ('connection_health_states','last_probe_protocol',true),
  ('connection_health_states','last_applied_probe_protocol',true),
  ('connection_health_states','counter_protocol',true),
  ('connection_health_states','health_evidence_protocol',true),
  ('connection_health_states','last_success_protocol',true),
  ('connection_health_events','request_protocol',true)
 ) AS fields(table_name,column_name,nullable)
 LOOP
  table_id := to_regclass(target.table_name);
  IF table_id IS NULL THEN
   CONTINUE;
  END IF;
  SELECT attnum INTO column_number FROM pg_attribute
   WHERE attrelid=table_id AND attname=target.column_name AND NOT attisdropped;
  IF column_number IS NULL THEN
   CONTINUE;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_constraint
   WHERE conrelid=table_id AND contype='c' AND conkey=ARRAY[column_number]
    AND pg_get_constraintdef(oid) LIKE '%chat_completions%'
    AND pg_get_constraintdef(oid) LIKE '%messages%') THEN
   CONTINUE;
  END IF;
  FOR old_constraint IN SELECT conname FROM pg_constraint
   WHERE conrelid=table_id AND contype='c' AND conkey=ARRAY[column_number]
    AND pg_get_constraintdef(oid) LIKE '%chat_completions%'
  LOOP
   EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',table_id,old_constraint.conname);
  END LOOP;
  EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I CHECK (%s%I IN (''chat_completions'',''responses'',''messages''))',
   table_id,target.table_name || '_' || target.column_name || '_values',
   CASE WHEN target.nullable THEN format('%I IS NULL OR ',target.column_name) ELSE '' END,target.column_name);
 END LOOP;
END $$;

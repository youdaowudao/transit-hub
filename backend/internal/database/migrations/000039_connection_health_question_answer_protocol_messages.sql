-- C4.2: allow Messages for question-answer protocol snapshots without rewriting records.
DO $$
DECLARE
 table_id regclass;
 column_number smallint;
 old_constraint record;
BEGIN
 table_id := to_regclass('connection_health_question_answer_records');
 IF table_id IS NULL THEN
  RETURN;
 END IF;
 SELECT attnum INTO column_number FROM pg_attribute
  WHERE attrelid=table_id AND attname='request_protocol' AND NOT attisdropped;
 IF column_number IS NULL THEN
  RETURN;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_constraint
  WHERE conrelid=table_id AND contype='c' AND conkey=ARRAY[column_number]
   AND pg_get_constraintdef(oid) LIKE '%chat_completions%'
   AND pg_get_constraintdef(oid) LIKE '%messages%') THEN
  RETURN;
 END IF;
 FOR old_constraint IN SELECT conname FROM pg_constraint
  WHERE conrelid=table_id AND contype='c' AND conkey=ARRAY[column_number]
   AND pg_get_constraintdef(oid) LIKE '%chat_completions%'
 LOOP
  EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',table_id,old_constraint.conname);
 END LOOP;
 EXECUTE format('ALTER TABLE %s ADD CONSTRAINT connection_health_qa_records_request_protocol_values CHECK (request_protocol IS NULL OR request_protocol IN (''chat_completions'',''responses'',''messages''))',table_id);
END $$;

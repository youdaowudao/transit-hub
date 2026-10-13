-- Reuse the existing rule timestamp as the one-time quality-algorithm boundary.
-- Legacy high thresholds stay unchanged; the physical 1--50 constraint remains.
INSERT INTO connection_health_model_control_settings(user_id,admin_account_id,min_accuracy_percent,min_judged_answers)
SELECT user_id,admin_account_id,50,3 FROM (
 SELECT user_id,admin_account_id FROM connection_health_model_control_settings
 UNION SELECT user_id,admin_account_id FROM connection_health_model_control_targets
 UNION SELECT user_id,admin_account_id FROM connection_health_model_control_auto
) workspaces ON CONFLICT DO NOTHING;
UPDATE connection_health_model_control_settings
SET min_judged_answers=GREATEST(3,min_judged_answers),version=version+1,updated_at=now();

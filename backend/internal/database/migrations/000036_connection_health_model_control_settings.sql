-- rollback-safe: additive workspace settings; legacy rules remain for rollback.
CREATE TABLE IF NOT EXISTS connection_health_model_control_settings (
 user_id text NOT NULL, admin_account_id text NOT NULL,
 min_accuracy_percent integer NOT NULL DEFAULT 50 CHECK(min_accuracy_percent BETWEEN 1 AND 100),
 min_judged_answers integer NOT NULL DEFAULT 3 CHECK(min_judged_answers BETWEEN 1 AND 50),
 version bigint NOT NULL DEFAULT 1 CHECK(version>=1), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,admin_account_id)
);
DO $$ BEGIN
 IF to_regclass('admin_accounts') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='connection_health_model_control_settings'::regclass AND conname='connection_health_model_control_settings_workspace_fk') THEN
  ALTER TABLE connection_health_model_control_settings ADD CONSTRAINT connection_health_model_control_settings_workspace_fk FOREIGN KEY(admin_account_id) REFERENCES admin_accounts(id) ON DELETE CASCADE;
 END IF;
END $$;
INSERT INTO connection_health_model_control_settings(user_id,admin_account_id,min_accuracy_percent,min_judged_answers)
SELECT user_id,admin_account_id,min(min_accuracy_percent),min(min_judged_answers)
FROM connection_health_model_control_rules GROUP BY user_id,admin_account_id
HAVING count(DISTINCT (min_accuracy_percent,min_judged_answers))=1
ON CONFLICT DO NOTHING;

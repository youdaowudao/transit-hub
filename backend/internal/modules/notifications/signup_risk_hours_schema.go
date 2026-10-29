package notifications

// SignupRiskHoursSchemaSQL is kept identical to migration 000045.
const SignupRiskHoursSchemaSQL = `
ALTER TABLE signup_risk_settings
 ADD COLUMN IF NOT EXISTS work_start text NOT NULL DEFAULT '09:00',
 ADD COLUMN IF NOT EXISTS work_end text NOT NULL DEFAULT '18:00',
 ADD COLUMN IF NOT EXISTS time_zone text NOT NULL DEFAULT 'Asia/Singapore';
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='signup_risk_settings'::regclass AND conname='signup_risk_work_hours_check') THEN
  ALTER TABLE signup_risk_settings ADD CONSTRAINT signup_risk_work_hours_check CHECK (
   work_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$' AND
   work_end ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$' AND
   work_start < work_end AND time_zone IN ('Asia/Singapore','Asia/Tokyo','UTC')
  );
 END IF;
END $$;
`

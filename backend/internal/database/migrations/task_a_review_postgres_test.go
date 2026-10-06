package migrations

import (
	"context"
	"testing"
	health "transithub/backend/internal/modules/connection_health"
)

func TestTaskAReviewMigrationPreservesLegacyBoundaryValuesPostgres(t *testing.T) {
	for _, entry := range []string{"migration", "EnsureSchema"} {
		t.Run(entry, func(t *testing.T) {
			pool := openMigrationPostgresPool(t)
			ctx := context.Background()
			r := health.NewRepository(pool)
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_policies(id,user_id,admin_account_id,name,failure_threshold,success_threshold,cooldown_seconds,observation_seconds,recovery_step_percent) VALUES ('low','u','w','旧下界',1,1,30,50,10),('high','u','w','旧上界',12,12,4000,600,40)`); err != nil {
				t.Fatal(err)
			}
			if entry == "migration" {
				sql, err := migrationFiles.ReadFile("000031_connection_health_rule_presets.sql")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, string(sql)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := r.EnsureSchema(ctx); err != nil {
					t.Fatal(err)
				}
				// EnsureSchema owns constraints, not migration backfill. Import snapshot rows explicitly.
				if _, err := pool.Exec(ctx, `INSERT INTO connection_health_rule_presets(id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,observation_seconds,recovery_step_percent)
 SELECT 'snapshot-'||id,user_id,admin_account_id,name,'legacy_snapshot',failure_threshold,success_threshold,cooldown_seconds,600,86400,3600,observation_seconds,recovery_step_percent FROM connection_health_policies;
 UPDATE connection_health_policies SET legacy_preset_id='snapshot-'||id`); err != nil {
					t.Fatal(err)
				}
			}
			presets, err := r.ListRulePresets(ctx, "u", "w")
			if err != nil {
				t.Fatal(err)
			}
			for _, sample := range []struct {
				id                                                string
				failure, success, cooldown, observation, recovery int
			}{{"low", 1, 1, 30, 50, 10}, {"high", 12, 12, 4000, 600, 40}} {
				p, err := r.GetPolicy(ctx, sample.id, "u", "w")
				if err != nil || p == nil {
					t.Fatal(err)
				}
				var snapshot *health.RulePreset
				for i := range presets {
					if presets[i].ID == p.LegacyPresetID {
						snapshot = &presets[i]
					}
				}
				if snapshot == nil || snapshot.Kind != health.PresetLegacySnapshot || snapshot.FailureThreshold != sample.failure || snapshot.SuccessThreshold != sample.success || snapshot.CooldownSeconds != sample.cooldown || snapshot.ObservationSeconds != sample.observation || snapshot.RecoveryStepPercent != sample.recovery {
					t.Fatalf("legacy numeric snapshot lost: policy=%+v snapshot=%+v", p, snapshot)
				}
				if p.FailureThreshold != sample.failure || p.SuccessThreshold != sample.success || p.CooldownSeconds != sample.cooldown {
					t.Fatalf("migration changed frozen legacy columns: %+v", p)
				}
			}
		})
	}
}

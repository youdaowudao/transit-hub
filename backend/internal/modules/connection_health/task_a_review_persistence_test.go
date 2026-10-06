package connection_health

import (
	"context"
	"errors"
	"testing"
)

func TestTaskAReviewSnapshotProtectionAndCopyValidationPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	// An earlier candidate already initialized runtime tables with strict anonymous
	// constraints. EnsureSchema must replace them without losing frozen values.
	if _, err := pool.Exec(ctx, `ALTER TABLE connection_health_rule_presets DROP CONSTRAINT connection_health_rule_presets_failure_range;
 ALTER TABLE connection_health_rule_presets DROP CONSTRAINT connection_health_rule_presets_success_range;
 ALTER TABLE connection_health_rule_presets DROP CONSTRAINT connection_health_rule_presets_cooldown_range;
 ALTER TABLE connection_health_rule_presets ADD CHECK (failure_threshold BETWEEN 2 AND 10);
 ALTER TABLE connection_health_rule_presets ADD CHECK (success_threshold BETWEEN 1 AND 10);
 ALTER TABLE connection_health_rule_presets ADD CHECK (cooldown_seconds BETWEEN 60 AND 3600)`); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connection_health_rule_presets(id,user_id,admin_account_id,name,kind,failure_threshold,success_threshold,cooldown_seconds,failed_retry_interval_seconds,long_failure_after_seconds,long_failure_interval_seconds,observation_seconds,recovery_step_percent) VALUES ('old-low','u','w','旧下界','legacy_snapshot',1,1,30,600,86400,3600,50,10),('old-high','u','w','旧上界','legacy_snapshot',12,12,4000,600,86400,3600,600,40)`); err != nil {
		t.Fatal(err)
	}
	presets, err := r.ListRulePresets(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range presets {
		if preset.Kind != PresetLegacySnapshot {
			continue
		}
		original := preset
		if _, err := r.SaveRulePreset(ctx, preset); !errors.Is(err, requestError(ErrorPresetReadOnly)) {
			t.Fatalf("read-only protection lost priority over current numeric validation: %v", err)
		}
		copy := preset
		copy.ID = ""
		copy.Name = "复制 " + preset.Name
		// Supplying the trusted snapshot kind cannot create an editable out-of-range preset.
		if _, err := r.SaveRulePreset(ctx, copy); !errors.Is(err, requestError(ErrorRequest)) {
			t.Fatalf("forged snapshot kind bypassed custom ranges: %v", err)
		}
		effective := effectivePolicyFromPreset(Policy{RuleVersion: RuleVersionLegacy, RulePreset: &preset})
		if effective.FailureThreshold != original.FailureThreshold || effective.SuccessThreshold != original.SuccessThreshold || effective.CooldownSeconds != original.CooldownSeconds || effective.ObservationSeconds != original.ObservationSeconds || effective.RecoveryStepPercent != original.RecoveryStepPercent {
			t.Fatalf("legacy snapshot silently clamped: preset=%+v effective=%+v", original, effective)
		}
		// Copy keeps the source unchanged; explicit user edits make the custom valid.
		copy.FailureThreshold, copy.SuccessThreshold, copy.CooldownSeconds = 3, 2, 300
		saved, err := r.SaveRulePreset(ctx, copy)
		if err != nil || saved.Kind != PresetCustom || saved.FailureThreshold != 3 || saved.CooldownSeconds != 300 {
			t.Fatalf("explicit valid custom copy failed %+v %v", saved, err)
		}
	}
	for _, column := range []string{"failure_threshold", "success_threshold", "cooldown_seconds"} {
		if _, err := pool.Exec(ctx, `UPDATE connection_health_rule_presets SET `+column+`=CASE WHEN $1='cooldown_seconds' THEN 30 ELSE 12 END WHERE kind='custom'`, column); err == nil {
			t.Fatalf("database editable preset constraint missing for %s", column)
		}
	}
}

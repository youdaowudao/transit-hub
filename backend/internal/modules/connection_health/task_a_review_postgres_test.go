package connection_health

import (
	"context"
	"testing"
)

func TestTaskAReviewQuickCreateUsesEditedRecommendedPresetPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	presets, err := r.ListRulePresets(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	var recommended RulePreset
	for _, p := range presets {
		if p.Kind == PresetRecommended {
			recommended = p
		}
	}
	if recommended.ID == "" {
		t.Fatal("missing recommended")
	}
	recommended.FailureThreshold = 5
	recommended.SuccessThreshold = 3
	recommended.CooldownSeconds = 120
	if _, err = r.SaveRulePreset(ctx, recommended); err != nil {
		t.Fatal(err)
	}
	p := Policy{ID: "quick", UserID: "u", AdminAccountID: "w", Name: "快速创建", Enabled: true, ProbeIntervalSeconds: 60, DailyProbeBudget: 1000}
	if err = r.CreatePolicyAndReplaceGroupConfiguration(ctx, p, nil, "g", "分组", []string{p.ID}, nil, []string{"sub2api:w:1"}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetPolicy(ctx, p.ID, "u", "w")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.RulePresetID != recommended.ID || got.RulePreset == nil || got.RulePreset.FailureThreshold != 5 || got.RulePreset.SuccessThreshold != 3 || got.RulePreset.CooldownSeconds != 120 {
		t.Fatalf("quick create did not persist edited recommended pointer: %+v", got)
	}
	effective := effectivePolicyFromPreset(*got)
	if effective.FailureThreshold != 5 || effective.SuccessThreshold != 3 || effective.CooldownSeconds != 120 {
		t.Fatalf("quick create judgment ignored recommended edits: %+v", effective)
	}
}

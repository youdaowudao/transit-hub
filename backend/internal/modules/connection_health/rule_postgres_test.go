package connection_health

import (
	"context"
	"errors"
	"testing"
	"time"
	"transithub/backend/internal/database/migrations"
)

func TestTaskARulePresetsFreezeAndGenerationPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil || s.RuleVersion != RuleVersionV2 || s.ConfigGeneration != 0 || s.ProbeConcurrency != 6 {
		t.Fatalf("default settings %+v %v", s, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_workspace_settings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read initialized settings", err, count)
	}
	p := Policy{ID: "p", UserID: "u", AdminAccountID: "w", Name: "策略", Enabled: true, FailureThreshold: 8, SuccessThreshold: 9, CooldownSeconds: 900, ObservationSeconds: 50, RecoveryStepPercent: 10, ProbeIntervalSeconds: 60, DailyProbeBudget: 1000}
	if err := r.SavePolicyWithTargets(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := r.GetPolicy(ctx, "p", "u", "w")
	if err != nil || loaded == nil {
		t.Fatal(err)
	}
	if loaded.FailureThreshold != 3 || loaded.SuccessThreshold != 2 || loaded.CooldownSeconds != 300 || loaded.ObservationSeconds != 300 || loaded.RecoveryStepPercent != 25 || loaded.RulePreset == nil || loaded.RuleVersion != RuleVersionV2 || loaded.ConfigGeneration != 1 {
		t.Fatalf("new policy %+v", loaded)
	}
	custom := DefaultRulePreset()
	custom.Name = "自定义"
	custom.UserID = "u"
	custom.AdminAccountID = "w"
	custom.FailureThreshold = 7
	custom, err = r.SaveRulePreset(ctx, custom)
	if err != nil {
		t.Fatal(err)
	}
	copyPreset := custom
	copyPreset.ID = ""
	copyPreset, err = r.SaveRulePreset(ctx, copyPreset)
	if err != nil || copyPreset.Name != "自定义（2）" {
		t.Fatal("duplicate name", copyPreset.Name, err)
	}
	p.RulePresetID = custom.ID
	if err := r.SavePolicyWithTargets(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	p.RulePresetID = ""
	p.FailureThreshold = 10
	if err := r.SavePolicyWithTargets(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err = r.GetPolicy(ctx, "p", "u", "w")
	if err != nil || loaded.RulePresetID != custom.ID || loaded.RulePreset.FailureThreshold != 7 || loaded.FailureThreshold != 3 {
		t.Fatalf("frozen/omitted pointer %+v %v", loaded, err)
	}
	if err := r.DeleteRulePreset(ctx, "u", "w", custom.ID); !errors.Is(err, requestError(ErrorPresetInUse)) {
		t.Fatal("referenced preset allowed delete", err)
	}
	presets, err := r.ListRulePresets(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range presets {
		if preset.Kind == PresetLegacyDefault {
			preset.Name = "attempt"
			if _, err := r.SaveRulePreset(ctx, preset); !errors.Is(err, requestError(ErrorPresetReadOnly)) {
				t.Fatal("read-only preset allowed update", err)
			}
		}
	}
	before, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	after, err := r.SaveWorkspaceProbeConcurrency(ctx, "u", "w", 10, before.ProbeConcurrencyVersion)
	if err != nil || after.ConfigGeneration != before.ConfigGeneration || after.ProbeConcurrencyVersion != before.ProbeConcurrencyVersion+1 {
		t.Fatalf("concurrency %+v %v", after, err)
	}
	if _, err := r.SaveWorkspaceProbeConcurrency(ctx, "u", "w", 1, before.ProbeConcurrencyVersion); !errors.Is(err, requestError(ErrorSettingsConflict)) {
		t.Fatal("stale settings overwrite", err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "1", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatal(err)
	}
	next, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil || next.ConfigGeneration != after.ConfigGeneration+1 {
		t.Fatal("test configuration generation", next, err)
	}
}

func TestTaskARuleConversionAtomicAndStaleClearsOnlyRecheckPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	protocol := TestProtocolChatCompletions
	rows := []ConnectionHealthState{
		{ConnectionID: "sub2api:healthy", State: StateHealthy, CurrentWeight: 12, ConsecutiveFailures: 2},
		{ConnectionID: "sub2api:degraded", State: StateDegraded, CurrentWeight: 0, ConsecutiveFailures: 2, FailingSince: &now},
		{ConnectionID: "sub2api:observing", State: StateObserving, CurrentWeight: 25, ConsecutiveFailures: 2, ConsecutiveSuccesses: 4, FailingSince: &now},
		{ConnectionID: "sub2api:suspended", State: StateSuspended, CurrentWeight: 40, ConsecutiveFailures: 5, CooldownUntil: &now, FailingSince: &now},
		{ConnectionID: "sub2api:disabled", State: StateDisabled, CurrentWeight: 40, ConsecutiveFailures: 5, FailingSince: &now},
		{ConnectionID: "real-id", State: StateObserving, CurrentWeight: 25, ConsecutiveFailures: 2},
	}
	for _, row := range rows {
		row.UserID = "u"
		row.AdminAccountID = "w"
		row.ModelName = "m"
		row.RuleVersion = RuleVersionLegacy
		row.HealthEvidenceStatus = HealthEvidenceValid
		row.HealthEvidenceProtocol = &protocol
		row.RecheckPending = true
		if err := upsertStateWithExecutor(ctx, pool, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionV2); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		id                          string
		state                       State
		weight, failures, successes int
	}{{"healthy", StateHealthy, 100, 0, 0}, {"degraded", StateDegraded, 75, 2, 0}, {"observing", StateSuspended, 0, 2, 1}, {"suspended", StateSuspended, 0, 5, 0}, {"disabled", StateDisabled, 40, 5, 0}} {
		state, err := r.GetState(ctx, "sub2api:"+want.id, "m")
		if err != nil || state.State != want.state || state.CurrentWeight != want.weight || state.ConsecutiveFailures != want.failures || state.ConsecutiveSuccesses != want.successes || state.RuleVersion != RuleVersionV2 || state.HealthEvidenceStatus != HealthEvidenceValid {
			t.Fatalf("conversion %s %+v %v", want.id, state, err)
		}
	}
	real, err := r.GetState(ctx, "real-id", "m")
	if err != nil || real.RuleVersion != RuleVersionLegacy || real.State != StateObserving {
		t.Fatal("compat state converted", real, err)
	}
	settings, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	target := AdminProbeTarget{TargetID: "sub2api:degraded", TestConfiguration: EffectiveTestConfiguration{Status: "default", Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10}, InventoryComplete: true}
	// Capture settings, change configuration, then commit an old request.
	if _, err := pool.Exec(ctx, `UPDATE connection_health_states SET recheck_pending=true WHERE connection_id=$1`, target.TargetID); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveGroupTestConfiguration(ctx, "u", "w", "1", &GroupTestConfiguration{TestProtocolResponses, 30}); err != nil {
		t.Fatal(err)
	}
	old, err := r.GetState(ctx, target.TargetID, "m")
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.CommitTargetProbe(ctx, TargetProbeCommit{UserID: "u", AdminAccountID: "w", Target: target, ModelName: "m", ConfigGeneration: settings.ConfigGeneration, Policy: Policy{RuleVersion: RuleVersionV2, RulePreset: func() *RulePreset { p := DefaultRulePreset(); return &p }(), AutoDegradeEnabled: true}, Outcome: ProbeOutcome{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, Result: ResultOK, LatencyMs: 2}, Now: now.Add(time.Minute), Event: ConnectionHealthEvent{ID: "stale", UserID: "u", AdminAccountID: "w", ConnectionID: target.TargetID, ModelName: "m"}})
	if err != nil || result.Disposition != "stale" {
		t.Fatal("old generation applied", result, err)
	}
	state, err := r.GetState(ctx, target.TargetID, "m")
	if err != nil || state.RecheckPending || state.State != old.State || state.CurrentWeight != old.CurrentWeight || state.ConsecutiveFailures != old.ConsecutiveFailures || state.LastProbeAt != nil {
		t.Fatalf("stale changed state %+v %v", state, err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_conversion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.rule_version='legacy' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_conversion BEFORE UPDATE ON connection_health_states FOR EACH ROW EXECUTE FUNCTION reject_conversion()`); err != nil {
		t.Fatal(err)
	}
	before, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); err == nil {
		t.Fatal("conversion should fail")
	}
	after, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil || after.RuleVersion != before.RuleVersion || after.ConfigGeneration != before.ConfigGeneration {
		t.Fatal("partial switch committed", after, err)
	}
}

func TestTaskAAllConfigurationWritesAdvanceGenerationPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := migrations.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	p := Policy{ID: "p", UserID: "u", AdminAccountID: "w", Name: "策略", Enabled: true, ProbeIntervalSeconds: 60, DailyProbeBudget: 1000}
	assertAdvance := func(name string, write func() error) {
		t.Helper()
		before, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
		if err != nil {
			t.Fatal(err)
		}
		if err := write(); err != nil {
			t.Fatal(name, err)
		}
		after, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
		if err != nil || after.ConfigGeneration != before.ConfigGeneration+1 {
			t.Fatalf("%s generation before=%d after=%d err=%v", name, before.ConfigGeneration, after.ConfigGeneration, err)
		}
	}
	assertAdvance("create", func() error { return r.SavePolicyWithTargets(ctx, p, nil) })
	assertAdvance("update", func() error { return r.SavePolicyWithTargets(ctx, p, nil) })
	assertAdvance("model targets", func() error { return r.ReplaceModelTargets(ctx, p.ID, nil) })
	assertAdvance("target assignment", func() error { return r.ReplacePolicyAssignments(ctx, "u", "w", "target", []string{p.ID}) })
	assertAdvance("target assignment with priority", func() error {
		return r.ReplacePolicyAssignmentsAndRequestPrioritySync(ctx, "u", "w", "target", []string{p.ID}, "generation-1")
	})
	assertAdvance("group exclusions", func() error {
		return r.ReplaceGroupPolicyConfiguration(ctx, "u", "w", "group", "分组", []string{p.ID}, []string{"excluded"}, []string{"target", "excluded"}, nil)
	})
	assertAdvance("group exclusions with priority", func() error {
		return r.ReplaceGroupPolicyConfigurationAndRequestPrioritySync(ctx, "u", "w", "group", "分组", []string{p.ID}, nil, []string{"target"}, nil, "generation-2")
	})
	q := p
	q.ID = "quick"
	q.Name = "快速创建"
	assertAdvance("quick create", func() error {
		return r.CreatePolicyAndReplaceGroupConfiguration(ctx, q, nil, "group", "分组", []string{q.ID}, nil, []string{"target"}, nil)
	})
	q.ID = "quick-sync"
	q.Name = "快速创建同步"
	assertAdvance("quick create with priority", func() error {
		return r.CreatePolicyAndReplaceGroupConfigurationAndRequestPrioritySync(ctx, q, nil, "group", "分组", []string{q.ID}, nil, []string{"target"}, nil, "generation-3")
	})
	assertAdvance("test configuration", func() error {
		return r.SaveGroupTestConfiguration(ctx, "u", "w", "group", &GroupTestConfiguration{TestProtocolResponses, 30})
	})
	var preset RulePreset
	assertAdvance("preset create", func() error {
		preset = DefaultRulePreset()
		preset.UserID = "u"
		preset.AdminAccountID = "w"
		preset.Name = "独立预设"
		var err error
		preset, err = r.SaveRulePreset(ctx, preset)
		return err
	})
	assertAdvance("preset edit", func() error {
		preset.FailureThreshold = 4
		var err error
		preset, err = r.SaveRulePreset(ctx, preset)
		return err
	})
	assertAdvance("preset apply all", func() error { _, err := r.ApplyRulePresetToAll(ctx, "u", "w", preset.ID); return err })
	assertAdvance("restore legacy", func() error { _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); return err })
	assertAdvance("switch v2", func() error { _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionV2); return err })
	assertAdvance("preset delete", func() error { return r.DeleteRulePreset(ctx, "u", "w", preset.ID) })
	assertAdvance("policy delete", func() error { _, err := r.DeletePolicy(ctx, "p", "u", "w"); return err })
}

func TestTaskARemoteClaimAndPermitRejectGenerationChangePostgres(t *testing.T) {
	for _, kind := range []string{ActionKindPriority, ActionKindTarget} {
		t.Run(kind, func(t *testing.T) {
			pool := openQuestionAnswerPostgresPool(t)
			r := NewRepository(pool)
			ctx := context.Background()
			if err := r.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			priority, target := protocolClaims()
			claim := priority
			if kind == ActionKindTarget {
				claim = target
			}
			initial := ConnectionHealthState{ConnectionID: claim.TargetID, ModelName: "m", UserID: "u", AdminAccountID: "w", State: StateHealthy, CurrentWeight: 100, RuleVersion: RuleVersionV2}
			if err := upsertStateWithExecutor(ctx, pool, initial); err != nil {
				t.Fatal(err)
			}
			// Capture rule/generation before reading states, exactly as production does.
			captured, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
			if err != nil {
				t.Fatal(err)
			}
			claim.Guard = RemoteActionHealthGuard{ConfigGeneration: &captured.ConfigGeneration}
			if _, err := pool.Exec(ctx, `INSERT INTO connection_health_runtime_leases(lease_key,owner_id,expires_at,updated_at) VALUES($1,$2,clock_timestamp()+interval '2 minutes',clock_timestamp())`, claim.LeaseKey, claim.OwnerID); err != nil {
				t.Fatal(err)
			}
			if ok, err := r.ClaimRemoteAction(ctx, claim); err != nil || !ok {
				t.Fatal("current generation claim rejected", ok, err)
			}
			if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); err != nil {
				t.Fatal(err)
			}
			// A switch between the rule read and state read must refuse both gates.
			changed, err := r.GetState(ctx, claim.TargetID, "m")
			if err != nil || changed.RuleVersion != RuleVersionLegacy {
				t.Fatal("state read did not see switched rule", changed, err)
			}
			if ok, err := r.ClaimRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatal("rule/state interleaving claim accepted", ok, err)
			}
			if ok, err := r.PermitRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatal("rule/state interleaving permit accepted", ok, err)
			}
			if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionV2); err != nil {
				t.Fatal(err)
			}
			sameRule, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
			if err != nil || sameRule.RuleVersion != captured.RuleVersion || sameRule.ConfigGeneration != captured.ConfigGeneration+2 {
				t.Fatal("ABA setup", sameRule, err)
			}
			if ok, err := r.ClaimRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatal("ABA rule version let stale claim pass", ok, err)
			}
			if ok, err := r.PermitRemoteAction(ctx, claim); ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatal("ABA rule version let stale permit send", ok, err)
			}
		})
	}
}

func TestTaskATargetProbeBatchGenerationAndABAPostgres(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	r := NewRepository(pool)
	ctx := context.Background()
	if err := r.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	protocol := TestProtocolResponses
	at := time.Now().UTC().Truncate(time.Millisecond)
	initial := ConnectionHealthState{ConnectionID: "sub2api:batch", ModelName: "m", UserID: "u", AdminAccountID: "w", State: StateDegraded, CurrentWeight: 75, ConsecutiveFailures: 2, RuleVersion: RuleVersionV2, HealthEvidenceStatus: HealthEvidenceValid, HealthEvidenceProtocol: &protocol, RecheckPending: true, LastProbeAt: &at}
	if err := upsertStateWithExecutor(ctx, pool, initial); err != nil {
		t.Fatal(err)
	}
	captured, err := r.GetWorkspaceHealthSettings(ctx, "u", "w")
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := r.ValidateTargetProbeBatch(ctx, "u", "w", initial.ConnectionID, []string{"m"}, captured.ConfigGeneration); err != nil || !allowed {
		t.Fatal("current batch rejected", allowed, err)
	}
	// Switch twice to keep the rule name equal while invalidating the decision.
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionLegacy); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SwitchWorkspaceRule(ctx, "u", "w", RuleVersionV2); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_health_states SET recheck_pending=true WHERE connection_id=$1`, initial.ConnectionID); err != nil {
		t.Fatal(err)
	}
	before, err := r.GetState(ctx, initial.ConnectionID, "m")
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := r.ValidateTargetProbeBatch(ctx, "u", "w", initial.ConnectionID, []string{"m"}, captured.ConfigGeneration); allowed || err != nil {
		t.Fatal("ABA batch admitted", allowed, err)
	}
	after, err := r.GetState(ctx, initial.ConnectionID, "m")
	if err != nil || after.RecheckPending || after.State != before.State || after.ConsecutiveFailures != before.ConsecutiveFailures || after.ConsecutiveSuccesses != before.ConsecutiveSuccesses || after.CurrentWeight != before.CurrentWeight || after.HealthEvidenceStatus != before.HealthEvidenceStatus || !after.LastProbeAt.Equal(*before.LastProbeAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("stale batch changed health fields before=%+v after=%+v err=%v", before, after, err)
	}
}

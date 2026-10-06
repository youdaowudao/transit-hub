package connection_health

import "testing"

func TestRulePresetValidationAndProtocolFallback(t *testing.T) {
	p := DefaultRulePreset()
	p.DelayLineMs = map[string]int{}
	if !validRulePreset(p) || p.DelayLine(TestProtocolResponses) != 10000 || p.DelayLine(TestProtocolChatCompletions) != 5000 {
		t.Fatal("missing delay keys must keep protocol defaults")
	}
	for _, mutate := range []func(*RulePreset){func(p *RulePreset) { p.FailureThreshold = 1 }, func(p *RulePreset) { p.SuccessThreshold = 11 }, func(p *RulePreset) { p.CooldownSeconds = 59 }, func(p *RulePreset) { p.FailedRetryIntervalSeconds = 3601 }, func(p *RulePreset) { p.LongFailureAfterSeconds = 3599 }, func(p *RulePreset) { p.LongFailureIntervalSeconds = 599 }, func(p *RulePreset) { p.DelayLineMs = map[string]int{"messages": 5000} }, func(p *RulePreset) { p.DelayLineMs = map[string]int{"responses": 999} }} {
		candidate := DefaultRulePreset()
		mutate(&candidate)
		if validRulePreset(candidate) {
			t.Errorf("accepted invalid preset: %+v", candidate)
		}
	}
	if name := uniquePresetName("同名", map[string]bool{"同名": true, "同名（2）": true}); name != "同名（3）" {
		t.Fatalf("unique name=%q", name)
	}
}

func TestTaskABuildProbeCommitEventUsesFirstTokenDelay(t *testing.T) {
	preset := DefaultRulePreset()
	for _, sample := range []struct {
		first  int
		result ResultKey
	}{{11000, ResultSlowResponse}, {1000, ResultOK}, {15000, ResultSlowResponse}} {
		cfg := defaultTestConfiguration()
		cfg.Protocol = TestProtocolResponses
		cfg.ProbeTimeoutSeconds = 30
		input := TargetProbeCommit{UserID: "u", AdminAccountID: "w", Target: AdminProbeTarget{TargetID: "sub2api:w:1", TestConfiguration: cfg}, ModelName: "m", Policy: Policy{RuleVersion: RuleVersionV2, RulePreset: &preset, AutoDegradeEnabled: true}, Outcome: ProbeOutcome{Protocol: TestProtocolResponses, ProbeTimeoutSeconds: 30, Result: ResultOK, LatencyMs: 15000, FirstTokenMs: intPtr(sample.first), NonStreaming: sample.first == 15000}}
		result, event := buildProbeCommitResult(input, nil, cfg)
		if result.State.State != StateHealthy || event.Result != string(sample.result) || event.ErrorKey != "" || event.ErrorDetail != func() string {
			if sample.first == 15000 {
				return "上游未返回流式"
			}
			return ""
		}() || event.FirstTokenMs == nil || *event.FirstTokenMs != sample.first || event.RuleVersion != RuleVersionV2 {
			t.Fatalf("event/state delay mismatch state=%+v event=%+v", result.State, event)
		}
	}
}

func TestTaskANonStreamingFailureMetadataStaysConsistentAndIdempotent(t *testing.T) {
	preset := DefaultRulePreset()
	configuration := defaultTestConfiguration()
	raw := ProbeOutcome{Result: ResultServerError, Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, NonStreaming: true, Detail: `{"error":{"message":"original failure"}}`}
	normalized := probeOutcomeWithAuditMetadata(raw)
	if twice := probeOutcomeWithAuditMetadata(normalized); twice.Detail != normalized.Detail {
		t.Fatal("audit normalization duplicated nonstream note")
	}
	input := TargetProbeCommit{UserID: "u", AdminAccountID: "w", Target: AdminProbeTarget{TargetID: "sub2api:w:1", TestConfiguration: configuration}, ModelName: "m", Policy: Policy{RuleVersion: RuleVersionV2, RulePreset: &preset, AutoDegradeEnabled: true}, Outcome: raw}
	result, event := buildProbeCommitResult(input, nil, configuration)
	model := toModelHealth("m", result.State)
	applyCurrentHealthProjection(&model, result.State, configuration)
	if result.State.LastErrorDetail != normalized.Detail || event.ErrorDetail != normalized.Detail || model.CurrentHealthResult == nil || model.CurrentHealthResult.ErrorDetail != normalized.Detail || model.LastAttempt == nil || model.LastAttempt.ErrorDetail != normalized.Detail {
		t.Fatalf("failure metadata differed: outcome=%+v state=%+v event=%+v model=%+v", normalized, result.State, event, model)
	}
	if raw.Detail != `{"error":{"message":"original failure"}}` {
		t.Fatal("normalization mutated caller outcome")
	}
}

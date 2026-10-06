package connection_health

import (
	"context"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// 本文件实现新的「手动一次性探活」：与策略自动探活/旧 ProbeTarget 完全隔离——
// 不查/写 connection_health_states，不写 connection_health_events，不经过状态机 Transition，
// 不消耗每日探活预算，不触发远端自动降级/恢复。结果只用于弹窗内即时展示，关闭弹窗即丢弃。

// ManualProbeResult 是一次性探活单个模型的 transient 结果，绝不包含上游凭据。
type ManualProbeResult struct {
	RuleVersion          string       `json:"ruleVersion"`
	FirstTokenMs         *int         `json:"firstTokenMs,omitempty"`
	FirstEventMs         *int         `json:"firstEventMs,omitempty"`
	NonStreaming         bool         `json:"nonStreaming,omitempty"`
	RequestPhase         string       `json:"requestPhase,omitempty"`
	Protocol             TestProtocol `json:"protocol"`
	ProbeTimeoutSeconds  int          `json:"probeTimeoutSeconds"`
	ConfigurationChanged bool         `json:"configurationChanged"`
	ModelName            string       `json:"modelName"`
	Result               string       `json:"result"`
	Healthy              bool         `json:"healthy"`
	LatencyMs            *int         `json:"latencyMs"`
	ErrorKey             string       `json:"errorKey"`
	ErrorDetail          string       `json:"errorDetail"`
	ProbedAt             time.Time    `json:"probedAt"`
}

// ManualProbeTarget 对指定 models 逐一发起一次真实轻量探活，直接返回结果，不落任何库。
// models 必须非空（不像旧 ProbeConnection/ProbeTarget 那样把「空」当成「探活全部候选」，
// 手动一次性探活不存在候选池概念，必须由用户在弹窗里显式勾选）。
func (s *Service) ManualProbeTarget(ctx context.Context, userID string, targetID string, models []string) ([]ManualProbeResult, error) {
	requested := make([]string, 0, len(models))
	for _, m := range models {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			requested = append(requested, trimmed)
		}
	}
	if len(requested) == 0 {
		return nil, requestError(ErrorManualModelsRequired)
	}

	session, target, account, adminAccountID, err := s.resolveManualTarget(ctx, userID, targetID)
	if err != nil {
		return nil, err
	}
	if err := s.loadWorkspaceProbeCap(ctx, userID, adminAccountID); err != nil {
		return nil, err
	}
	releaseSlot, acquired := s.sharedProbeLimiter().acquireManual(ctx, userID+"|"+adminAccountID, nil)
	if !acquired {
		return nil, ctx.Err()
	}
	defer releaseSlot()
	settings, err := s.workspaceHealthSettings(ctx, userID, adminAccountID)
	if target.Platform == string(upstream.PlatformNewAPI) {
		settings.RuleVersion = RuleVersionLegacy
	}
	if err != nil {
		return nil, err
	}
	_, _, memberships, found, accountsReadError, err := s.findAdminTargetWithMemberships(ctx, session, adminAccountID, target.AccountID)
	if err != nil {
		return nil, err
	}
	if !found {
		if accountsReadError {
			return nil, requestError(ErrorAccountsFetch)
		}
		return nil, requestError(ErrorProbeTargetNotFound)
	}
	if err := s.configureTestTarget(ctx, userID, adminAccountID, &target, memberships, !accountsReadError); err != nil {
		return nil, err
	}
	effectiveSpecs, _, policyOK := s.currentScheduledProbeSpecs(ctx, userID, adminAccountID, target, memberships, nil)
	if !policyOK {
		return nil, requestError(ErrorUnknown)
	}
	specByModel := make(map[string]probeModelSpec, len(effectiveSpecs))
	for _, spec := range effectiveSpecs {
		specByModel[spec.modelName] = spec
	}
	cred, err := s.resolveProbeCredential(ctx, session, account)
	if err != nil {
		return nil, requestError(reasonToErrorKey(upstream.ProbeCredentialReason(err)))
	}

	results := make([]ManualProbeResult, 0, len(requested))
	for _, modelName := range requested {
		spec, exists := specByModel[modelName]
		if !exists {
			spec = probeModelSpec{modelName: modelName, providerFamily: target.ProviderFamily, maxProbeTokens: 1, policy: Policy{RuleVersion: settings.RuleVersion}}
		}
		outcome := applyOutcomeDelay(s.executeTargetProbe(ctx, target, cred, spec), spec.policy)
		result := ManualProbeResult{RuleVersion: spec.policy.RuleVersion, FirstTokenMs: outcome.FirstTokenMs, FirstEventMs: outcome.FirstEventMs, NonStreaming: outcome.NonStreaming,
			RequestPhase: outcome.RequestPhase,
			Protocol:     outcome.Protocol, ProbeTimeoutSeconds: outcome.ProbeTimeoutSeconds,
			ModelName: modelName, Result: string(outcome.Result), Healthy: outcome.Result == ResultOK || outcome.Result == ResultSlowResponse,
			LatencyMs: intPtr(outcome.LatencyMs), ProbedAt: time.Now(),
		}
		if outcome.Result != ResultOK && outcome.Result != ResultSlowResponse {
			result.ErrorKey = string(outcome.Result)
			result.ErrorDetail = outcome.Detail
		}
		results = append(results, result)
	}
	if target.Platform == string(upstream.PlatformSub2API) {
		configs, readErr := s.repo.ListGroupTestConfigurations(ctx, userID, adminAccountID)
		current := ResolveGroupTestConfiguration(target.TestMemberships, readErr == nil && target.InventoryComplete, configs)
		if !sameEffectiveTestConfiguration(target.TestConfiguration, current) {
			for i := range results {
				results[i].ConfigurationChanged = true
			}
		}
	}
	return results, nil
}

func intPtr(v int) *int { return &v }

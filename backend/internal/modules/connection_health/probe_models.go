package connection_health

import (
	"context"
	"strings"
	"transithub/backend/internal/modules/upstream"
)

const (
	ProbeModeRealModel      = "real_model"
	ProbeModeFirstAvailable = "first_available"
)

// probeModelSource only transports already-read whitelist metadata in memory.
type probeModelSource struct {
	accountType       string
	platform          string
	openAIPassthrough bool
	modelMapping      map[string]string
	modelMappingKnown bool
}

func probeModelSourceFromAccount(a upstream.AdminGroupAccountInfo) probeModelSource {
	return probeModelSource{a.Type, a.Platform, a.OpenAIPassthrough, a.ModelMapping, a.ModelMappingKnown}
}
func probeModelConfirmed(source probeModelSource, model string) bool {
	account := upstream.AdminGroupAccountInfo{Type: source.accountType, Platform: source.platform, OpenAIPassthrough: source.openAIPassthrough, ModelMapping: source.modelMapping, ModelMappingKnown: source.modelMappingKnown}
	supported, known := modelControlSupports(account, model)
	if !supported || !known {
		return false
	}
	if account.Platform == "openai" && account.OpenAIPassthrough {
		return true
	}
	return modelControlMappedModel(account.ModelMapping, model) == model
}

type probeModelSelection struct {
	modelName     string
	kind          string
	skippedModels []string
}

func selectProbeModel(policy Policy, source probeModelSource) probeModelSelection {
	names := []string{}
	for _, target := range policy.ModelTargets {
		if name := strings.TrimSpace(target.ModelName); target.Enabled && name != "" {
			names = append(names, name)
		}
	}
	for index, name := range names {
		if probeModelConfirmed(source, name) {
			result := probeModelSelection{modelName: name}
			if index > 0 {
				result.kind = "fallback"
				result.skippedModels = append([]string{}, names[:index]...)
			}
			return result
		}
	}
	if len(names) == 0 {
		return probeModelSelection{}
	}
	result := probeModelSelection{modelName: names[len(names)-1]}
	if len(names) > 1 {
		result.kind = "last_unconfirmed"
	}
	return result
}
func validateProbeMode(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode != "" && mode != ProbeModeRealModel && mode != ProbeModeFirstAvailable {
		return requestError(ErrorRequest)
	}
	return nil
}

func (s *Service) validateProbeModeWorkspace(ctx context.Context, userID, workspace, mode string) error {
	if strings.TrimSpace(mode) != ProbeModeFirstAvailable {
		return nil
	}
	if s.mySites == nil {
		return requestError(ErrorRequest)
	}
	session, err := s.mySites.RequireSession(ctx, userID, workspace)
	if err != nil || session.Platform != upstream.PlatformSub2API {
		return requestError(ErrorRequest)
	}
	return nil
}

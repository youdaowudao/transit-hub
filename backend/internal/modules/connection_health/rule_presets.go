package connection_health

import (
	"fmt"
	"strings"
	"time"
)

const (
	RuleVersionV2         = "v2"
	RuleVersionLegacy     = "legacy"
	PresetRecommended     = "recommended"
	PresetLegacySnapshot  = "legacy_snapshot"
	PresetLegacyDefault   = "legacy_default"
	PresetCustom          = "custom"
	ErrorPresetReadOnly   = "admin.connectionHealth.errors.presetReadOnly"
	ErrorPresetInUse      = "admin.connectionHealth.errors.presetInUse"
	ErrorPresetBuiltIn    = "admin.connectionHealth.errors.presetBuiltIn"
	ErrorSettingsConflict = "admin.connectionHealth.errors.settingsConflict"
)

type RulePresetPolicy struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type RulePreset struct {
	ID                         string             `json:"id"`
	UserID                     string             `json:"-"`
	AdminAccountID             string             `json:"-"`
	Name                       string             `json:"name"`
	Kind                       string             `json:"kind"`
	FailureThreshold           int                `json:"failureThreshold"`
	SuccessThreshold           int                `json:"successThreshold"`
	CooldownSeconds            int                `json:"cooldownSeconds"`
	FailedRetryIntervalSeconds int                `json:"failedRetryIntervalSeconds"`
	LongFailureAfterSeconds    int                `json:"longFailureAfterSeconds"`
	LongFailureIntervalSeconds int                `json:"longFailureIntervalSeconds"`
	DelayLineMs                map[string]int     `json:"delayLineMs"`
	ObservationSeconds         int                `json:"observationSeconds"`
	RecoveryStepPercent        int                `json:"recoveryStepPercent"`
	Policies                   []RulePresetPolicy `json:"policies"`
	CreatedAt                  time.Time          `json:"createdAt"`
	UpdatedAt                  time.Time          `json:"updatedAt"`
}

type WorkspaceHealthSettings struct {
	UserID                  string     `json:"-"`
	AdminAccountID          string     `json:"-"`
	RuleVersion             string     `json:"ruleVersion"`
	ConfigGeneration        int64      `json:"configGeneration"`
	ProbeConcurrency        int        `json:"probeConcurrency"`
	ProbeConcurrencyVersion int64      `json:"probeConcurrencyVersion"`
	RuleSwitchedAt          *time.Time `json:"ruleSwitchedAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

func defaultWorkspaceHealthSettings(userID, workspace string) WorkspaceHealthSettings {
	return WorkspaceHealthSettings{UserID: userID, AdminAccountID: workspace, RuleVersion: RuleVersionV2, ProbeConcurrency: 6}
}
func DefaultRulePreset() RulePreset {
	return RulePreset{Name: "新规则（推荐）", Kind: PresetRecommended, FailureThreshold: 3, SuccessThreshold: 2, CooldownSeconds: 300, FailedRetryIntervalSeconds: 600, LongFailureAfterSeconds: 86400, LongFailureIntervalSeconds: 3600, DelayLineMs: map[string]int{"responses": 10000, "chat_completions": 5000}, ObservationSeconds: 300, RecoveryStepPercent: 25, Policies: []RulePresetPolicy{}}
}
func RulePresetForPolicy(policy Policy) RulePreset {
	if policy.RulePreset != nil {
		return *policy.RulePreset
	}
	preset := DefaultRulePreset()
	// Only old in-memory callers lack a preset; production reads always attach one.
	if policy.RuleVersion == "" {
		preset.FailureThreshold = defaultInt(policy.FailureThreshold, 3)
		preset.SuccessThreshold = defaultInt(policy.SuccessThreshold, 2)
		preset.CooldownSeconds = defaultInt(policy.CooldownSeconds, 300)
		preset.ObservationSeconds = defaultInt(policy.ObservationSeconds, 300)
		preset.RecoveryStepPercent = defaultInt(policy.RecoveryStepPercent, 25)
	}
	return preset
}
func effectivePolicyFromPreset(policy Policy) Policy {
	preset := RulePresetForPolicy(policy)
	policy.FailureThreshold = preset.FailureThreshold
	policy.SuccessThreshold = preset.SuccessThreshold
	policy.CooldownSeconds = preset.CooldownSeconds
	policy.ObservationSeconds = preset.ObservationSeconds
	policy.RecoveryStepPercent = preset.RecoveryStepPercent
	return policy
}
func (preset RulePreset) DelayLine(protocol TestProtocol) int {
	if line, ok := preset.DelayLineMs[string(protocol)]; ok {
		return line
	}
	if protocol == TestProtocolResponses {
		return 10000
	}
	return 5000
}
func validRulePreset(p RulePreset) bool {
	if strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 120 || p.FailureThreshold < 2 || p.FailureThreshold > 10 || p.SuccessThreshold < 1 || p.SuccessThreshold > 10 || p.CooldownSeconds < 60 || p.CooldownSeconds > 3600 || p.FailedRetryIntervalSeconds < 60 || p.FailedRetryIntervalSeconds > 3600 || p.LongFailureAfterSeconds < 3600 || p.LongFailureAfterSeconds > 604800 || p.LongFailureIntervalSeconds < 600 || p.LongFailureIntervalSeconds > 86400 || p.ObservationSeconds < 1 || p.ObservationSeconds > 3600 || p.RecoveryStepPercent < 1 || p.RecoveryStepPercent > 100 {
		return false
	}
	for protocol, line := range p.DelayLineMs {
		if (protocol != "responses" && protocol != "chat_completions") || line < 1000 || line > 120000 {
			return false
		}
	}
	return true
}
func uniquePresetName(name string, names map[string]bool) string {
	name = strings.TrimSpace(name)
	if !names[name] {
		return name
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s（%d）", name, n)
		if !names[candidate] {
			return candidate
		}
	}
}

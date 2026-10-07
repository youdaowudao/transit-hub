package connection_health

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type ManualPriorityOwnerDecision struct {
	RemoteActionScope
	ExpectedConfigGeneration int64
	Target                   AdminProbeTarget
	GroupIDs                 []string
	CurrentPriority          int
	Mode                     string
	Priority                 int
}
type ManualPriorityOwnerPrepared struct {
	ConfigGeneration int64
	Priority         int
	Noop             bool
}
type manualPriorityOwnerRepository interface {
	PrepareManualPriorityOwner(context.Context, ManualPriorityOwnerDecision) (ManualPriorityOwnerPrepared, error)
}

// Shared with the service precheck and transaction implementation. Effective
// account policies merge across its working groups before model policy choice.
func validateManualPriorityOwner(decision ManualPriorityOwnerDecision, policies []Policy) error {
	if hasMultiplierOnlyPolicy(policies) {
		return requestError(ErrorPriorityMultiplierOnly)
	}
	if decision.Mode != "auto" {
		return nil
	}
	if !hasMultiplierPriorityPolicy(policies) {
		return requestError(ErrorPrioritySortDisabled)
	}
	item := &priorityTargetInventory{target: decision.Target, policies: policies}
	if len(activeHealthPriorityModels(item)) != 0 {
		return nil
	}
	if len(candidateModelSpecs(decision.Target.Models, policies)) == 0 {
		return requestError(ErrorPriorityNoMatchingModels)
	}
	return requestError(ErrorPriorityAutoDegradeDisabled)
}

func manualPriorityPolicies(decision ManualPriorityOwnerDecision, policies []Policy, direct []PolicyAssignment, groups []GroupPolicyAssignment, exclusions []GroupTargetExclusion) []Policy {
	key := decision.UserID + "|" + decision.AdminAccountID
	directPolicies := assignedEnabledPoliciesByTarget(policies, direct)[key][decision.TargetID]
	byGroup := assignedEnabledPoliciesByGroup(policies, groups)[key]
	excluded := groupTargetExclusionIndex(exclusions)[key]
	merged := []Policy{}
	for _, groupID := range decision.GroupIDs {
		inherited := byGroup[groupID]
		if excluded[groupID][decision.TargetID] {
			inherited = nil
		}
		merged = mergePoliciesByID(merged, effectivePoliciesForTarget(directPolicies, inherited))
	}
	return merged
}

// All reads below use the original workspace transaction. The captured
// generation must match before validating policies, selecting a tier and
// incrementing it; no stale eligibility decision borrows the new generation.
func (r *Repository) PrepareManualPriorityOwner(ctx context.Context, decision ManualPriorityOwnerDecision) (ManualPriorityOwnerPrepared, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, decision.UserID, decision.AdminAccountID)
	if err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	defer tx.Rollback(ctx)
	settings, err := getWorkspaceHealthSettings(ctx, tx, decision.UserID, decision.AdminAccountID)
	if err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	if settings.ConfigGeneration != decision.ExpectedConfigGeneration {
		return ManualPriorityOwnerPrepared{}, ErrRemoteActionEvidenceChanged
	}
	policies, direct, groups, exclusions, err := loadManualPriorityConfigurationTx(ctx, tx, decision.UserID, decision.AdminAccountID)
	if err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	if err := validateManualPriorityOwner(decision, manualPriorityPolicies(decision, policies, direct, groups, exclusions)); err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	out := ManualPriorityOwnerPrepared{ConfigGeneration: settings.ConfigGeneration, Priority: decision.Priority}
	if decision.Mode == "auto" && decision.CurrentPriority > 9 {
		out.Noop = true
		out.Priority = decision.CurrentPriority
		return out, tx.Commit(ctx)
	}
	if decision.Mode == "auto" {
		tier := 2
		err := tx.QueryRow(ctx, `SELECT COALESCE(account_tier,2) FROM connection_health_account_configs WHERE user_id=$1 AND admin_account_id=$2 AND target_id=$3`, decision.UserID, decision.AdminAccountID, decision.TargetID).Scan(&tier)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		out.Priority = 99
		if effectiveAccountTier(tier) == 1 {
			out.Priority = 10
		}
	}
	if err := bumpHealthConfigGenerationTx(ctx, tx, decision.UserID, decision.AdminAccountID); err != nil {
		return out, err
	}
	settings, err = getWorkspaceHealthSettings(ctx, tx, decision.UserID, decision.AdminAccountID)
	if err != nil {
		return out, err
	}
	out.ConfigGeneration = settings.ConfigGeneration
	return out, tx.Commit(ctx)
}

func loadManualPriorityConfigurationTx(ctx context.Context, tx pgx.Tx, user, workspace string) ([]Policy, []PolicyAssignment, []GroupPolicyAssignment, []GroupTargetExclusion, error) {
	rows, err := tx.Query(ctx, `SELECT id,user_id,admin_account_id,name,enabled,own_group_id,own_group_name,model_pattern,probe_mode,
	probe_interval_seconds,continue_probe_when_unschedulable,unschedulable_probe_interval_minutes,
	failure_threshold,success_threshold,cooldown_seconds,observation_seconds,recovery_step_percent,auto_degrade_enabled,auto_remote_action_enabled,
	priority_mode,strategy_mode,daily_probe_budget,rule_preset_id,legacy_preset_id,created_at,updated_at
	FROM connection_health_policies WHERE user_id=$1 AND admin_account_id=$2 ORDER BY created_at,id`, user, workspace)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	policies := []Policy{}
	for rows.Next() {
		p, err := scanPolicyRow(rows)
		if err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		policies = append(policies, *p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	settings, err := getWorkspaceHealthSettings(ctx, tx, user, workspace)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for i := range policies {
		p := &policies[i]
		p.RuleVersion, p.ConfigGeneration = settings.RuleVersion, settings.ConfigGeneration
		if p.RulePresetID != "" {
			preset, err := getRulePresetTx(ctx, tx, user, workspace, p.RulePresetID)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			p.RulePreset = &preset
		}
		rows, err := tx.Query(ctx, `SELECT id,policy_id,user_id,admin_account_id,model_name,provider_family,enabled,probe_prompt,max_probe_tokens,created_at,updated_at FROM connection_health_model_targets WHERE policy_id=$1 ORDER BY created_at,id`, p.ID)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for rows.Next() {
			var m ModelTarget
			if err := rows.Scan(&m.ID, &m.PolicyID, &m.UserID, &m.AdminAccountID, &m.ModelName, &m.ProviderFamily, &m.Enabled, &m.ProbePrompt, &m.MaxProbeTokens, &m.CreatedAt, &m.UpdatedAt); err != nil {
				rows.Close()
				return nil, nil, nil, nil, err
			}
			p.ModelTargets = append(p.ModelTargets, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	rows, err = tx.Query(ctx, `SELECT id,user_id,admin_account_id,target_id,policy_id,created_at,updated_at FROM connection_health_policy_assignments WHERE user_id=$1 AND admin_account_id=$2`, user, workspace)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	direct, err := scanPolicyAssignments(rows)
	rows.Close()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT id,user_id,admin_account_id,admin_group_id,admin_group_name,policy_id,created_at,updated_at FROM connection_health_group_policy_assignments WHERE user_id=$1 AND admin_account_id=$2`, user, workspace)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	groups, err := scanGroupPolicyAssignments(rows)
	rows.Close()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT id,user_id,admin_account_id,admin_group_id,target_id,created_at,updated_at FROM connection_health_group_target_exclusions WHERE user_id=$1 AND admin_account_id=$2`, user, workspace)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	exclusions, err := scanGroupTargetExclusions(rows)
	rows.Close()
	return policies, direct, groups, exclusions, err
}

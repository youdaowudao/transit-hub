package connection_health

import "context"

func (f *fakeRepository) fakeConfigGenerationKnown(user, workspace string) bool {
	if _, exists := f.healthConfigGenerations[user+"|"+workspace]; exists {
		return true
	}
	for _, policy := range f.policies {
		if policy.UserID == user && policy.AdminAccountID == workspace && policy.RuleVersion != "" {
			return true
		}
	}
	return false
}

func (f *fakeRepository) fakeWorkspaceHealthSettings(user, workspace string) WorkspaceHealthSettings {
	settings := defaultWorkspaceHealthSettings(user, workspace)
	for _, p := range f.policies {
		if p.UserID == user && p.AdminAccountID == workspace && p.RuleVersion != "" {
			settings.RuleVersion, settings.ConfigGeneration = p.RuleVersion, p.ConfigGeneration
			break
		}
	}
	if generation, ok := f.healthConfigGenerations[user+"|"+workspace]; ok {
		settings.ConfigGeneration = generation
	}
	return settings
}
func (f *fakeRepository) GetWorkspaceHealthSettings(_ context.Context, user, workspace string) (WorkspaceHealthSettings, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	return f.fakeWorkspaceHealthSettings(user, workspace), nil
}
func (f *fakeRepository) bumpFakeConfigGeneration(user, workspace string) int64 {
	next := f.fakeWorkspaceHealthSettings(user, workspace).ConfigGeneration + 1
	if f.healthConfigGenerations == nil {
		f.healthConfigGenerations = map[string]int64{}
	}
	f.healthConfigGenerations[user+"|"+workspace] = next
	for i := range f.policies {
		if f.policies[i].UserID == user && f.policies[i].AdminAccountID == workspace {
			f.policies[i].ConfigGeneration = next
		}
	}
	return next
}
func (f *fakeRepository) SaveAccountTierAndRequestPrioritySync(ctx context.Context, user, workspace, target string, tier int, signature string) (bool, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	key := user + "|" + workspace + "|" + target
	changed := effectiveAccountTier(f.accountTiers[key]) != tier
	if f.accountTiers == nil {
		f.accountTiers = map[string]int{}
	}
	f.accountTiers[key] = tier
	if changed {
		f.bumpFakeConfigGeneration(user, workspace)
		f.priorityWorkspaceMu.Lock()
		f.priorityWorkspaces[user+"|"+workspace] = PriorityWorkspaceSyncState{UserID: user, AdminAccountID: workspace, PendingSignature: signature, LastActionSource: "account_tier_save", LastDecision: "pending", InventoryStatus: "pending"}
		f.priorityWorkspaceMu.Unlock()
	}
	return changed, nil
}
func (f *fakeRepository) PrepareManualPriorityOwner(ctx context.Context, decision ManualPriorityOwnerDecision) (ManualPriorityOwnerPrepared, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	settings := f.fakeWorkspaceHealthSettings(decision.UserID, decision.AdminAccountID)
	if settings.ConfigGeneration != decision.ExpectedConfigGeneration {
		return ManualPriorityOwnerPrepared{}, ErrRemoteActionEvidenceChanged
	}
	if err := validateManualPriorityOwner(decision, manualPriorityPolicies(decision, f.policies, f.assignments, f.groupAssignments, f.groupExclusions)); err != nil {
		return ManualPriorityOwnerPrepared{}, err
	}
	out := ManualPriorityOwnerPrepared{ConfigGeneration: settings.ConfigGeneration, Priority: decision.Priority}
	if decision.Mode == "auto" {
		if decision.CurrentPriority > 9 {
			out.Noop = true
			out.Priority = decision.CurrentPriority
			return out, nil
		}
		out.Priority = 99
		if effectiveAccountTier(f.accountTiers[decision.UserID+"|"+decision.AdminAccountID+"|"+decision.TargetID]) == 1 {
			out.Priority = 10
		}
	}
	out.ConfigGeneration = f.bumpFakeConfigGeneration(decision.UserID, decision.AdminAccountID)
	return out, nil
}

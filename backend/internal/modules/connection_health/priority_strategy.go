package connection_health

import (
	"context"
	"log"
	"sort"
	"time"

	"transithub/backend/internal/modules/upstream"
)

// TargetPriorityActioner 是倍率排序策略对 upstream 模块的唯一写依赖。真实实现根据 session
// 平台更新 New API channel 或 Sub2API account 的 priority，并由 upstream 模块保证字段级写入安全。
type TargetPriorityActioner interface {
	UpdateAdminTargetPriority(session upstream.Session, targetID string, priority int) error
}

// ContextTargetPriorityActioner is optional so existing test and integration
// actioners remain compatible. PlatformService implements it, which keeps all
// production Priority writes within the asynchronous worker deadline.
type ContextTargetPriorityActioner interface {
	UpdateAdminTargetPriorityContext(ctx context.Context, session upstream.Session, targetID string, priority int) error
}

func (s *Service) updateAdminTargetPriority(ctx context.Context, session upstream.Session, targetID string, priority int) error {
	if actioner, ok := s.priorityActions.(ContextTargetPriorityActioner); ok {
		return actioner.UpdateAdminTargetPriorityContext(ctx, session, targetID, priority)
	}
	return s.priorityActions.UpdateAdminTargetPriority(session, targetID, priority)
}

type priorityTargetInventory struct {
	target              AdminProbeTarget
	account             upstream.AdminGroupAccountInfo
	policies            []Policy
	multipliers         []float64
	fallbackMultipliers []float64
	upstreamMultiplier  upstreamMultiplierResolution
	currentPriority     int
	priorityPresent     bool
}

type healthPriorityCandidate struct {
	targetID       string
	item           *priorityTargetInventory
	multiplier     float64
	states         []ConnectionHealthState
	expectedModels int
	healthBand     int
	latencyMs      *int
}

// syncMultiplierPriorities 在每轮探活前同步上游优先级。普通倍率策略仍然「健康优先、倍率次之」，
// 仅倍率策略则完全忽略探活状态。它故意与 job 生成分开，确保未到探活时间的目标也能更新顺序。
func (s *Service) syncMultiplierPriorities(
	ctx context.Context,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
	allSyncStates []PrioritySyncState,
) {
	s.syncMultiplierPrioritiesWithCache(ctx, policies, targetAssignments, groupAssignments, exclusions, allSyncStates, make(adminInventoryCache))
}

func (s *Service) syncCurrentWorkspacePriorities(ctx context.Context, userID string, adminAccountID string) {
	pendingSignature, signatureErr := s.pendingPrioritySyncGeneration(ctx, userID, adminAccountID)
	if signatureErr != nil {
		log.Printf("[connection-health] priority sync load workspace generation failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, signatureErr)
		s.markPriorityWorkspaceHealthSyncFailedDirect(userID, adminAccountID, signatureErr, 1)
		return
	}
	if err := s.syncCurrentWorkspacePrioritiesWithResult(ctx, userID, adminAccountID, pendingSignature); err != nil {
		log.Printf("[connection-health] priority sync failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, pendingSignature, err, 1)
	}
}

func (s *Service) syncCurrentWorkspacePrioritiesWithResult(ctx context.Context, userID string, adminAccountID string, pendingSignatures ...string) error {
	if s.priorityActions == nil || s.platformGroups == nil {
		return nil
	}
	pendingSignature := ""
	if len(pendingSignatures) > 0 {
		pendingSignature = pendingSignatures[0]
	}
	if pendingSignature != "" {
		current, err := s.repo.IsPriorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, pendingSignature)
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
	}
	release, err := s.repo.AcquirePrioritySyncLease(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	defer release()
	if pendingSignature != "" {
		current, err := s.repo.IsPriorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, pendingSignature)
		if err != nil {
			return err
		}
		if !current {
			return nil
		}
	}
	return s.syncCurrentWorkspacePrioritiesLockedWithResult(ctx, userID, adminAccountID, pendingSignature)
}

func (s *Service) syncCurrentWorkspacePrioritiesLockedWithResult(ctx context.Context, userID string, adminAccountID string, pendingSignature string) error {
	policies, err := s.repo.ListPolicies(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	enabled := make([]Policy, 0, len(policies))
	for _, policy := range policies {
		if policy.Enabled {
			enabled = append(enabled, policy)
		}
	}
	assignments, err := s.repo.ListPolicyAssignmentsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	groupAssignments, err := s.repo.ListGroupPolicyAssignmentsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	exclusions, err := s.repo.ListGroupTargetExclusionsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	states, err := s.repo.ListPrioritySyncStates(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
	// Keep a workspace with a pending save in the reconciliation set even when the
	// latest save removed its final policy and there are no target checkpoints left.
	workspaceState, stateErr := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
	if stateErr != nil {
		return stateErr
	}
	if workspaceState != nil && workspaceState.PendingSignature != "" {
		states = append(states, PrioritySyncState{UserID: userID, AdminAccountID: adminAccountID})
	}
	expectedGenerations := map[string]string(nil)
	if pendingSignature != "" {
		expectedGenerations = map[string]string{userID + "|" + adminAccountID: pendingSignature}
	}
	s.syncMultiplierPrioritiesWithCacheLocked(ctx, enabled, assignments, groupAssignments, exclusions, states, make(adminInventoryCache), expectedGenerations)
	return nil
}

func (s *Service) syncMultiplierPrioritiesWithCache(
	ctx context.Context,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
	allSyncStates []PrioritySyncState,
	inventoryCache adminInventoryCache,
) {
	s.syncMultiplierPrioritiesWithCacheMode(ctx, policies, targetAssignments, groupAssignments, exclusions, allSyncStates, inventoryCache, true, nil)
}

func (s *Service) syncMultiplierPrioritiesWithCacheLocked(
	ctx context.Context,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
	allSyncStates []PrioritySyncState,
	inventoryCache adminInventoryCache,
	expectedGenerations map[string]string,
) {
	s.syncMultiplierPrioritiesWithCacheMode(ctx, policies, targetAssignments, groupAssignments, exclusions, allSyncStates, inventoryCache, false, expectedGenerations)
}

func (s *Service) syncMultiplierPrioritiesWithCacheMode(
	ctx context.Context,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
	allSyncStates []PrioritySyncState,
	inventoryCache adminInventoryCache,
	acquireWorkspaceLease bool,
	expectedGenerations map[string]string,
) {
	if s.priorityActions == nil || s.platformGroups == nil {
		return
	}

	assignedTargets := assignedEnabledPoliciesByTarget(policies, targetAssignments)
	assignedGroups := assignedEnabledPoliciesByGroup(policies, groupAssignments)
	excluded := groupTargetExclusionIndex(exclusions)
	workspaceIdentity := make(map[string][2]string)
	for _, state := range allSyncStates {
		key := state.UserID + "|" + state.AdminAccountID
		workspaceIdentity[key] = [2]string{state.UserID, state.AdminAccountID}
	}
	for _, policy := range policies {
		key := policy.UserID + "|" + policy.AdminAccountID
		workspaceIdentity[key] = [2]string{policy.UserID, policy.AdminAccountID}
	}
	for _, assignment := range targetAssignments {
		key := assignment.UserID + "|" + assignment.AdminAccountID
		workspaceIdentity[key] = [2]string{assignment.UserID, assignment.AdminAccountID}
	}
	for _, assignment := range groupAssignments {
		key := assignment.UserID + "|" + assignment.AdminAccountID
		workspaceIdentity[key] = [2]string{assignment.UserID, assignment.AdminAccountID}
	}

	workspaceKeys := make([]string, 0, len(workspaceIdentity))
	for workspaceKey := range workspaceIdentity {
		workspaceKeys = append(workspaceKeys, workspaceKey)
	}
	sort.Strings(workspaceKeys)
	for _, workspaceKey := range workspaceKeys {
		identity := workspaceIdentity[workspaceKey]
		userID, adminAccountID := identity[0], identity[1]
		release := func() {}
		if acquireWorkspaceLease {
			var err error
			release, err = s.repo.AcquirePrioritySyncLease(ctx, userID, adminAccountID)
			if err != nil {
				log.Printf("[connection-health] priority sync acquire workspace lease failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				continue
			}
		}
		func() {
			defer release()
			expectedPendingSignature, jobGeneration := expectedGenerations[workspaceKey]
			if jobGeneration {
				current, err := s.repo.IsPriorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, expectedPendingSignature)
				if err != nil {
					log.Printf("[connection-health] priority sync verify workspace generation failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
					s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1)
					return
				}
				if !current {
					return
				}
			} else {
				workspaceState, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
				if err != nil {
					log.Printf("[connection-health] priority sync load workspace generation failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
					s.markPriorityWorkspaceHealthSyncFailedDirect(userID, adminAccountID, err, 1)
					return
				}
				if workspaceState != nil {
					expectedPendingSignature = workspaceState.PendingSignature
					if expectedPendingSignature != "" && workspaceState.NextReconcileAt != nil && workspaceState.NextReconcileAt.After(time.Now()) {
						return
					}
				}
			}
			failGeneration := func(syncErr error) {
				s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, syncErr, 1)
			}
			if expectedPendingSignature != "" {
				marked, markErr := s.repo.MarkPriorityWorkspaceSyncRunning(ctx, userID, adminAccountID, expectedPendingSignature)
				if markErr != nil {
					log.Printf("[connection-health] priority sync mark running failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, markErr)
					failGeneration(markErr)
					return
				}
				if !marked {
					// A newer save replaced this generation while the worker was queued.
					// Do not write a failure over it; that save has reserved its own run.
					return
				}
			}
			inventorySnapshot, err := s.loadAdminInventory(ctx, userID, adminAccountID, inventoryCache)
			if err != nil {
				log.Printf("[connection-health] priority sync load admin inventory failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				failGeneration(err)
				return
			}
			session := inventorySnapshot.session
			settings, err := s.repo.ListGroupProbeSortSettings(ctx, userID, adminAccountID)
			if err != nil {
				log.Printf("[connection-health] priority sync list fallback multipliers failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				failGeneration(err)
				return
			}
			fallbackByGroup := make(map[string]*float64, len(settings))
			for _, setting := range settings {
				fallbackByGroup[setting.AdminGroupID] = cloneFloat64Pointer(setting.FallbackMultiplier)
			}
			multiplierLookup := upstreamMultiplierLookup{byAccount: make(map[string]upstreamMultiplierResolution)}
			if workspaceUsesMultiplierPriority(assignedTargets[workspaceKey], assignedGroups[workspaceKey]) {
				multiplierLookup = s.upstreamMultiplierResolutionsByAdminAccount(ctx, userID, adminAccountID, string(session.Platform))
			}
			inventory, inventoryComplete, err := s.priorityInventoryForSnapshot(
				inventorySnapshot, adminAccountID, assignedTargets[workspaceKey], assignedGroups[workspaceKey], excluded[workspaceKey],
				fallbackByGroup, multiplierLookup,
			)
			if err != nil {
				log.Printf("[connection-health] priority sync inventory failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				failGeneration(err)
				return
			}
			states, err := s.repo.ListStatesByWorkspace(ctx, userID, adminAccountID)
			if err != nil {
				log.Printf("[connection-health] priority sync list health states failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				failGeneration(err)
				return
			}
			// Checkpoints must be read after acquiring the same workspace lease that covers
			// inventory reads and remote writes. A pre-lease snapshot can be stale even when
			// the write phase itself is serialized.
			syncStates, err := s.repo.ListPrioritySyncStates(ctx, userID, adminAccountID)
			if err != nil {
				log.Printf("[connection-health] priority sync list checkpoints failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				failGeneration(err)
				return
			}
			s.syncWorkspacePriorities(ctx, session, userID, adminAccountID, inventory, inventoryComplete, states, syncStates, expectedPendingSignature)
		}()
	}
}

func workspaceUsesMultiplierPriority(targetPolicies map[string][]Policy, groupPolicies map[string][]Policy) bool {
	for _, policies := range targetPolicies {
		if hasMultiplierPriorityPolicy(policies) {
			return true
		}
	}
	for _, policies := range groupPolicies {
		if hasMultiplierPriorityPolicy(policies) {
			return true
		}
	}
	return false
}

func (s *Service) priorityInventoryForSnapshot(
	snapshot *adminWorkspaceInventory,
	adminAccountID string,
	targetPolicies map[string][]Policy,
	groupPolicies map[string][]Policy,
	excludedByGroup map[string]map[string]bool,
	fallbackByGroup map[string]*float64,
	multiplierLookup upstreamMultiplierLookup,
) (map[string]*priorityTargetInventory, bool, error) {
	session := snapshot.session
	platform := string(session.Platform)
	inventory := make(map[string]*priorityTargetInventory)
	inventoryComplete := true
	for _, groupInventory := range snapshot.groups {
		group := groupInventory.group
		if groupInventory.err != nil {
			// 单个分组失败不阻断其它分组排序；目标如果只存在于失败分组，本轮保持原值。
			inventoryComplete = false
			log.Printf("[connection-health] priority sync group accounts failed group_id=%s err=%v", group.ID, groupInventory.err)
			continue
		}
		for _, account := range groupInventory.accounts {
			targetID := buildTargetID(platform, adminAccountID, account.ID)
			item := inventory[targetID]
			if item == nil {
				item = &priorityTargetInventory{
					target: AdminProbeTarget{
						TargetID: targetID, Platform: platform, AdminGroupID: group.ID, AdminGroupName: group.Name,
						AccountID: account.ID, AccountName: account.Name, AccountStatus: account.Status, AccountWeight: cloneIntPointer(account.Weight),
						ProviderFamily: account.Platform, Models: splitModelList(account.Models),
					},
					account: account,
				}
				if account.Priority != nil {
					item.currentPriority = *account.Priority
					item.priorityPresent = true
				}
				inventory[targetID] = item
			}
			item.upstreamMultiplier = resolutionForAdminAccount(multiplierLookup, account.ID)
			inherited := groupPolicies[group.ID]
			excluded := excludedByGroup[group.ID][targetID]
			if excluded {
				inherited = nil
			}
			effectivePolicies := effectivePoliciesForTarget(targetPolicies[targetID], inherited)
			// 倍率只来自目标实际参与策略继承的分组。先前在排除判断前收集倍率，会让已排除
			// 或无倍率策略的其它成员分组错误地压低当前目标优先级。
			if group.Multiplier != nil && hasMultiplierPriorityPolicy(effectivePolicies) {
				item.multipliers = append(item.multipliers, *group.Multiplier)
			}
			if fallback := fallbackByGroup[group.ID]; fallback != nil && hasHealthMultiplierPriorityPolicy(effectivePolicies) {
				item.fallbackMultipliers = append(item.fallbackMultipliers, *fallback)
			}
			item.policies = mergePoliciesByID(item.policies, effectivePolicies)
		}
	}
	return inventory, inventoryComplete, nil
}

func (s *Service) syncWorkspacePriorities(
	ctx context.Context,
	session upstream.Session,
	userID string,
	adminAccountID string,
	inventory map[string]*priorityTargetInventory,
	inventoryComplete bool,
	healthStates []ConnectionHealthState,
	syncStates []PrioritySyncState,
	expectedPendingSignatures ...string,
) {
	expectedPendingSignature := ""
	if len(expectedPendingSignatures) > 0 {
		expectedPendingSignature = expectedPendingSignatures[0]
	}
	generationCurrent := func() bool {
		current, err := s.priorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, expectedPendingSignature)
		if err != nil {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1)
			return false
		}
		return current
	}
	failedCount := 0
	unavailableCount := 0
	incompleteCount := 0
	statesByTarget := make(map[string][]ConnectionHealthState)
	for _, state := range healthStates {
		if _, isTarget := parseTargetID(state.ConnectionID); isTarget {
			statesByTarget[state.ConnectionID] = append(statesByTarget[state.ConnectionID], state)
		}
	}

	managed := make(map[string]*priorityTargetInventory)
	hardExcludedHealthTargets := make(map[string]struct{})
	missingMultiplier := make(map[string]struct{})
	effectiveMultiplierByTarget := make(map[string]float64)
	desiredByTarget := make(map[string]int)
	multiplierOnlyTargets := make(map[string]float64)
	healthCandidates := make([]healthPriorityCandidate, 0)
	for targetID, item := range inventory {
		if !hasMultiplierPriorityPolicy(item.policies) {
			continue
		}
		if accountHardExcludedFromAdminMonitoring(string(session.Platform), item.account) && !hasMultiplierOnlyPolicy(item.policies) {
			managed[targetID] = item
			hardExcludedHealthTargets[targetID] = struct{}{}
			continue
		}
		if hasMultiplierOnlyPolicy(item.policies) {
			if len(item.multipliers) == 0 {
				missingMultiplier[targetID] = struct{}{}
				continue
			}
			multiplier := minFloat(item.multipliers)
			managed[targetID] = item
			effectiveMultiplierByTarget[targetID] = multiplier
			multiplierOnlyTargets[targetID] = multiplier
			continue
		}
		if item.upstreamMultiplier.status == MultiplierResolutionUnavailable || item.upstreamMultiplier.status == MultiplierResolutionStale || item.upstreamMultiplier.status == MultiplierResolutionUpdating || item.upstreamMultiplier.status == MultiplierResolutionMissing {
			missingMultiplier[targetID] = struct{}{}
			unavailableCount++
		}

		multiplier, available := effectiveHealthSortMultiplier(item)
		if !available {
			activeModels := activeHealthPriorityModels(item)
			activeStates := activeHealthPriorityStates(statesByTarget[targetID], activeModels)
			healthBand := priorityHealthBand(activeStates, len(activeModels))
			managed[targetID] = item
			desiredByTarget[targetID] = desiredHealthBandEndForPlatform(session.Platform, healthBand)
			continue
		}
		activeModels := activeHealthPriorityModels(item)
		activeStates := activeHealthPriorityStates(statesByTarget[targetID], activeModels)
		candidate := healthPriorityCandidate{
			targetID: targetID, item: item, multiplier: multiplier, states: activeStates,
			expectedModels: len(activeModels), healthBand: priorityHealthBand(activeStates, len(activeModels)),
			latencyMs: completeTargetSuccessLatency(activeStates, activeModels),
		}
		managed[targetID] = item
		effectiveMultiplierByTarget[targetID] = multiplier
		healthCandidates = append(healthCandidates, candidate)
	}

	distinctMultiplierOnly := make([]float64, 0)
	seenMultiplierOnly := make(map[float64]struct{})
	for _, multiplier := range multiplierOnlyTargets {
		if _, exists := seenMultiplierOnly[multiplier]; !exists {
			seenMultiplierOnly[multiplier] = struct{}{}
			distinctMultiplierOnly = append(distinctMultiplierOnly, multiplier)
		}
	}
	sort.Float64s(distinctMultiplierOnly)
	multiplierOnlyRank := make(map[float64]int, len(distinctMultiplierOnly))
	for rank, multiplier := range distinctMultiplierOnly {
		multiplierOnlyRank[multiplier] = rank
	}
	for targetID, multiplier := range multiplierOnlyTargets {
		rank := multiplierOnlyRank[multiplier]
		desired := desiredManagedPriorityForPlatformWithExpected(session.Platform, nil, rank, 0)
		if session.Platform == upstream.PlatformSub2API {
			desired = desiredSub2APIMultiplierOnlyPriority(rank)
		}
		desiredByTarget[targetID] = desired
	}

	sortHealthPriorityCandidates(healthCandidates)
	currentBand, bandRank := -1, 0
	for _, candidate := range healthCandidates {
		if candidate.healthBand != currentBand {
			currentBand = candidate.healthBand
			bandRank = 0
		}
		desiredByTarget[candidate.targetID] = desiredHealthPriorityForPlatform(session.Platform, candidate.healthBand, bandRank)
		bandRank++
	}

	storedByTarget := make(map[string]PrioritySyncState, len(syncStates))
	for _, state := range syncStates {
		storedByTarget[state.TargetID] = state
	}

	for targetID, item := range managed {
		// A missing upstream priority is a real value, not zero. The existing
		// field-level priority API cannot clear a priority back to NULL safely,
		// so leave such targets untouched rather than materializing 0.
		if session.Platform == upstream.PlatformSub2API && !item.priorityPresent {
			continue
		}
		multiplier, multiplierAvailable := effectiveMultiplierByTarget[targetID]
		desired := desiredByTarget[targetID]
		stored, exists := storedByTarget[targetID]
		if !exists {
			stored = PrioritySyncState{
				UserID: userID, AdminAccountID: adminAccountID, TargetID: targetID,
				OriginalPriority: item.currentPriority, LastAppliedPriority: item.currentPriority,
			}
		}
		if stored.Conflict {
			continue
		}
		pendingConfirmed := false
		if stored.PendingPriority != nil && item.currentPriority == *stored.PendingPriority {
			stored.LastAppliedPriority = *stored.PendingPriority
			stored.PendingPriority = nil
			pendingConfirmed = true
		}
		if exists && item.currentPriority != stored.LastAppliedPriority && stored.PendingPriority == nil {
			current := item.currentPriority
			stored.Conflict = true
			stored.LastConflictPriority = &current
			if multiplierAvailable {
				stored.EffectiveMultiplier = multiplier
			}
			if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
				log.Printf("[connection-health] priority conflict state save failed target_id=%s err=%v", targetID, err)
				failedCount++
			}
			continue
		}
		if exists && stored.PendingPriority != nil && item.currentPriority != stored.LastAppliedPriority {
			current := item.currentPriority
			stored.Conflict = true
			stored.LastConflictPriority = &current
			if multiplierAvailable {
				stored.EffectiveMultiplier = multiplier
			}
			if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
				log.Printf("[connection-health] priority pending conflict state save failed target_id=%s err=%v", targetID, err)
				failedCount++
			}
			continue
		}
		if _, hardExcluded := hardExcludedHealthTargets[targetID]; hardExcluded {
			if pendingConfirmed {
				if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
					log.Printf("[connection-health] hard-excluded priority confirmation save failed target_id=%s err=%v", targetID, err)
					failedCount++
				}
			}
			continue
		}
		if item.currentPriority != desired {
			pending := desired
			stored.PendingPriority = &pending
			if multiplierAvailable {
				stored.EffectiveMultiplier = multiplier
			}
			if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
				log.Printf("[connection-health] priority sync intent save failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
			if err := s.updateAdminTargetPriority(ctx, session, item.target.AccountID, desired); err != nil {
				log.Printf("[connection-health] priority sync update failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
		}
		stored.LastAppliedPriority = desired
		stored.PendingPriority = nil
		if multiplierAvailable {
			stored.EffectiveMultiplier = multiplier
		}
		stored.Conflict = false
		stored.LastConflictPriority = nil
		if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
			log.Printf("[connection-health] priority sync state save failed target_id=%s err=%v", targetID, err)
			failedCount++
		}
	}

	// 不再被任何倍率策略覆盖的目标恢复接管前优先级。若管理员已经人工改过，则保留人工值。
	for targetID, stored := range storedByTarget {
		if _, stillManaged := managed[targetID]; stillManaged {
			continue
		}
		if _, waitingForMultiplier := missingMultiplier[targetID]; waitingForMultiplier {
			continue
		}
		item := inventory[targetID]
		if session.Platform == upstream.PlatformSub2API && item != nil && !item.priorityPresent {
			// Preserve an upstream NULL priority and keep any prior checkpoint
			// for a later, explicit reconciliation once the value is readable.
			continue
		}
		if item == nil {
			if !inventoryComplete {
				// 分组读取失败时无法证明目标已经消失，保留当前优先级和同步快照，
				// 等下一次完整扫描再决定是否恢复。
				incompleteCount++
				continue
			}
			if stored.Conflict {
				// 已确认目标不再受策略管理，但人工修改过的值不能被原始快照覆盖。
				if err := s.repo.DeletePrioritySyncState(ctx, userID, adminAccountID, targetID); err != nil {
					log.Printf("[connection-health] missing conflicted target priority state delete failed target_id=%s err=%v", targetID, err)
					failedCount++
				}
				continue
			}
			parsed, ok := parseTargetID(targetID)
			if !ok || parsed.adminAccountID != adminAccountID || parsed.platform != string(session.Platform) {
				continue
			}
			pending := stored.OriginalPriority
			stored.PendingPriority = &pending
			if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
				log.Printf("[connection-health] missing target priority restore intent save failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
			if err := s.updateAdminTargetPriority(ctx, session, parsed.accountID, stored.OriginalPriority); err != nil {
				log.Printf("[connection-health] missing target priority restore failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
			if err := s.repo.DeletePrioritySyncState(ctx, userID, adminAccountID, targetID); err != nil {
				log.Printf("[connection-health] missing target priority state delete failed target_id=%s err=%v", targetID, err)
				failedCount++
			}
			continue
		}
		if stored.PendingPriority != nil && item.currentPriority == *stored.PendingPriority {
			stored.LastAppliedPriority = *stored.PendingPriority
			stored.PendingPriority = nil
		}
		if !stored.Conflict && item.currentPriority == stored.LastAppliedPriority && item.currentPriority != stored.OriginalPriority {
			pending := stored.OriginalPriority
			stored.PendingPriority = &pending
			if err := s.repo.UpsertPrioritySyncState(ctx, stored); err != nil {
				log.Printf("[connection-health] priority restore intent save failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
			if err := s.updateAdminTargetPriority(ctx, session, item.target.AccountID, stored.OriginalPriority); err != nil {
				log.Printf("[connection-health] priority restore failed target_id=%s err=%v", targetID, err)
				failedCount++
				continue
			}
			if !generationCurrent() {
				return
			}
		}
		if err := s.repo.DeletePrioritySyncState(ctx, userID, adminAccountID, targetID); err != nil {
			log.Printf("[connection-health] priority sync state delete failed target_id=%s err=%v", targetID, err)
			failedCount++
		}
	}
	if !generationCurrent() {
		return
	}
	if !inventoryComplete {
		incompleteFailures := failedCount + unavailableCount + incompleteCount
		if incompleteFailures == 0 {
			incompleteFailures = 1
		}
		s.markPriorityWorkspaceSyncFailed(
			userID,
			adminAccountID,
			expectedPendingSignature,
			requestError(ErrorPriorityMetadataUnavailable),
			incompleteFailures,
		)
		return
	}
	if failedCount+unavailableCount > 0 {
		detail := requestError(ErrorUnknown)
		if unavailableCount > 0 {
			detail = requestError(ErrorPriorityMetadataUnavailable)
		}
		s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, detail, failedCount+unavailableCount)
		return
	}
	s.markPriorityWorkspaceSyncSucceeded(userID, adminAccountID, expectedPendingSignature)
}

// desiredManagedPriorityForPlatform 按平台真实语义计算优先级：NewAPI 沿用「分数越高越优先」；
// Sub2API 使用紧凑的小数值状态分段，数值越小越优先。
func desiredManagedPriorityForPlatform(platform upstream.Platform, states []ConnectionHealthState, multiplierRank int) int {
	if platform == upstream.PlatformSub2API {
		return desiredSub2APIManagedPriority(states, multiplierRank, len(states))
	}
	return desiredManagedPriority(states, multiplierRank)
}

func desiredManagedPriorityForPlatformWithExpected(platform upstream.Platform, states []ConnectionHealthState, multiplierRank int, expectedModels int) int {
	if platform == upstream.PlatformSub2API {
		return desiredSub2APIManagedPriority(states, multiplierRank, expectedModels)
	}
	score := desiredManagedPriority(states, multiplierRank)
	if len(states) < expectedModels && score != 1 {
		// Missing model states are unconfigured, not healthy. A known suspended/disabled
		// state remains the lowest tier even when another model has not been probed yet.
		priceScore := maxInt(0, 999-multiplierRank)
		score = 10000 + priceScore
	}
	return score
}

func desiredHealthPriorityForPlatform(platform upstream.Platform, healthBand int, tupleRank int) int {
	if platform == upstream.PlatformSub2API {
		bases := []int{10, 100, 1000, 10000, 100000}
		nextBases := []int{100, 1000, 10000, 100000, 100001}
		if healthBand < 0 || healthBand >= len(bases) {
			healthBand = 3
		}
		if healthBand == 4 {
			return bases[healthBand]
		}
		return sub2APIPriorityWithinBand(bases[healthBand], nextBases[healthBand], tupleRank)
	}
	bases := []int{40000, 30000, 20000, 10000, 1}
	if healthBand < 0 || healthBand >= len(bases) {
		healthBand = 3
	}
	if healthBand == 4 {
		return 1
	}
	return bases[healthBand] + maxInt(0, 999-tupleRank)
}

func desiredHealthBandEndForPlatform(platform upstream.Platform, healthBand int) int {
	if healthBand < 0 || healthBand > 4 {
		healthBand = 3
	}
	if platform == upstream.PlatformSub2API {
		return []int{99, 999, 9999, 99999, 100000}[healthBand]
	}
	return []int{40000, 30000, 20000, 10000, 1}[healthBand]
}

// desiredSub2APIManagedPriority 使用 Sub2API「数值越小越优先」的原生语义，并为不同健康
// 状态预留互不重叠的区间：健康 10-99、恢复中 100-999、降级/观察 1000-9999、待探活
// 10000-99999、暂停/禁用 100000。同一状态内 multiplierRank 越小，priority 越小。
// rank 超出区间容量时在区间末尾并列，避免价格排序跨越健康状态边界。
func desiredSub2APIManagedPriority(states []ConnectionHealthState, multiplierRank int, expectedModels int) int {
	for _, state := range states {
		if state.State == StateDisabled || state.State == StateSuspended {
			return 100000
		}
	}
	if len(states) < expectedModels {
		return sub2APIPriorityWithinBand(10000, 100000, multiplierRank)
	}

	base, nextBase := 10, 100
	for _, state := range states {
		switch state.State {
		case StateDegraded, StateObserving:
			base, nextBase = 1000, 10000
		case StateRecovering:
			if base < 100 {
				base, nextBase = 100, 1000
			}
		}
	}
	return sub2APIPriorityWithinBand(base, nextBase, multiplierRank)
}

// sortHealthPriorityCandidates 按生产调度使用的完整比较元组排序。该比较器同时用于
// priority 写回和 admin 详情 rank，避免 priority 在区间末位并列时退化成分组局部顺序。
func sortHealthPriorityCandidates(candidates []healthPriorityCandidate) {
	sort.SliceStable(candidates, func(i int, j int) bool {
		return compareHealthPriorityCandidates(candidates[i], candidates[j]) < 0
	})
}

func compareHealthPriorityCandidates(left healthPriorityCandidate, right healthPriorityCandidate) int {
	if left.healthBand != right.healthBand {
		if left.healthBand < right.healthBand {
			return -1
		}
		return 1
	}
	if left.multiplier != right.multiplier {
		if left.multiplier < right.multiplier {
			return -1
		}
		return 1
	}
	if left.latencyMs == nil || right.latencyMs == nil {
		if left.latencyMs != nil {
			return -1
		}
		if right.latencyMs != nil {
			return 1
		}
	} else if *left.latencyMs != *right.latencyMs {
		if *left.latencyMs < *right.latencyMs {
			return -1
		}
		return 1
	}
	if left.targetID < right.targetID {
		return -1
	}
	if left.targetID > right.targetID {
		return 1
	}
	return 0
}

// multiplier_only 不参与 P 版健康状态分段，继续使用原有 1-9 独立倍率区间。
func desiredSub2APIMultiplierOnlyPriority(multiplierRank int) int {
	return sub2APIPriorityWithinBand(1, 10, multiplierRank)
}

func sub2APIPriorityWithinBand(base int, nextBase int, multiplierRank int) int {
	offset := maxInt(0, multiplierRank)
	return base + minInt(offset, nextBase-base-1)
}

func hasMultiplierPriorityPolicy(policies []Policy) bool {
	for _, policy := range policies {
		if policy.Enabled && normalizePriorityMode(policy.PriorityMode) == PriorityModeMultiplier {
			return true
		}
	}
	return false
}

func hasHealthMultiplierPriorityPolicy(policies []Policy) bool {
	return hasMultiplierPriorityPolicy(policies) && !hasMultiplierOnlyPolicy(policies)
}

func effectiveHealthSortMultiplier(item *priorityTargetInventory) (float64, bool) {
	switch item.upstreamMultiplier.status {
	case MultiplierResolutionResolved:
		if item.upstreamMultiplier.info.effectiveMultiplier != nil {
			return *item.upstreamMultiplier.info.effectiveMultiplier, true
		}
	case MultiplierResolutionDisabled, MultiplierResolutionUnavailable, MultiplierResolutionStale, MultiplierResolutionUpdating, MultiplierResolutionMissing:
		return 0, false
	}
	return uniqueFloat(item.fallbackMultipliers)
}

func uniqueFloat(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if candidate != value {
			return 0, false
		}
	}
	return value, true
}

func activeHealthPriorityModels(item *priorityTargetInventory) map[string]struct{} {
	models := make(map[string]struct{})
	for _, spec := range candidateModelSpecs(item.target.Models, item.policies) {
		if spec.policy.AutoDegradeEnabled {
			models[spec.modelName] = struct{}{}
		}
	}
	return models
}

func activeHealthPriorityStates(states []ConnectionHealthState, activeModels map[string]struct{}) []ConnectionHealthState {
	active := make([]ConnectionHealthState, 0, len(activeModels))
	for _, state := range states {
		if _, ok := activeModels[state.ModelName]; ok {
			active = append(active, state)
		}
	}
	return active
}

func priorityHealthBand(states []ConnectionHealthState, expectedModels int) int {
	for _, state := range states {
		if state.State == StateDisabled || state.State == StateSuspended {
			return 4
		}
	}
	if len(states) < expectedModels {
		return 3
	}
	band := 0
	for _, state := range states {
		switch state.State {
		case StateDegraded, StateObserving:
			return 2
		case StateRecovering:
			band = 1
		}
	}
	return band
}

func completeTargetSuccessLatency(states []ConnectionHealthState, activeModels map[string]struct{}) *int {
	if len(activeModels) == 0 {
		return nil
	}
	latencyByModel := make(map[string]*int, len(states))
	for _, state := range states {
		latencyByModel[state.ModelName] = state.LastSuccessLatencyMs
	}
	maxLatency := 0
	for model := range activeModels {
		latency := latencyByModel[model]
		if latency == nil {
			return nil
		}
		if *latency > maxLatency {
			maxLatency = *latency
		}
	}
	return &maxLatency
}

// hasMultiplierOnlyPolicy 让明确的仅倍率策略成为同一目标的优先级依据。即使目标还叠加了
// 一条负责记录健康状态的探活策略，健康状态也不会重新参与 priority 排名。
func hasMultiplierOnlyPolicy(policies []Policy) bool {
	for _, policy := range policies {
		if policy.Enabled && normalizeStrategyMode(policy.StrategyMode) == StrategyModeMultiplierOnly {
			return true
		}
	}
	return false
}

func minFloat(values []float64) float64 {
	minValue := values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
	}
	return minValue
}

// desiredManagedPriority 计算平台无关的路由分数，并使用互不重叠的区间保证健康状态始终压过价格：
// healthy > recovering > degraded/observing > unconfigured > suspended/disabled。
// 同一健康层级内，倍率排名越靠前（倍率越低）分数越大；平台数值方向由上层映射。
func desiredManagedPriority(states []ConnectionHealthState, multiplierRank int) int {
	priceScore := 999 - multiplierRank
	if priceScore < 0 {
		priceScore = 0
	}
	if len(states) == 0 {
		return 10000 + priceScore
	}

	base := 40000
	weight := 100
	for _, state := range states {
		if state.CurrentWeight < weight {
			weight = state.CurrentWeight
		}
		switch state.State {
		case StateDisabled, StateSuspended:
			return 1
		case StateDegraded, StateObserving:
			if base > 20000 {
				base = 20000
			}
		case StateRecovering:
			if base > 30000 {
				base = 30000
			}
		}
	}
	if base == 30000 {
		base += maxInt(0, minInt(100, weight)) * 50
	} else if base == 20000 {
		base += maxInt(0, minInt(100, weight)) * 10
	}
	return base + priceScore
}

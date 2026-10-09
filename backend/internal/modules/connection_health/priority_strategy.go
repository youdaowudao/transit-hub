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
	accountTier         int
	snapshotStartedAt   time.Time
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
	accountTier       int
	multiplierUnknown bool
	ruleVersion       string
	targetID          string
	item              *priorityTargetInventory
	multiplier        float64
	states            []ConnectionHealthState
	expectedModels    int
	healthBand        int
	latencyMs         *int
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
		s.markPriorityWorkspaceHealthSyncFailedDirect(userID, adminAccountID, signatureErr, 1, nil)
		return
	}
	if err := s.syncCurrentWorkspacePrioritiesWithResult(ctx, userID, adminAccountID, pendingSignature); err != nil {
		log.Printf("[connection-health] priority sync failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, pendingSignature, err, 1, nil)
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
	ctx, release, _, err := s.acquireActionLease(ctx, priorityRuntimeLeaseKey(userID, adminAccountID), true)
	if err != nil {
		return err
	}
	defer release()
	ctx = context.WithValue(ctx, workspacePriorityLeaseContextKey{}, actionLeaseFromContext(ctx))
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
	var err error
	ctx, err = s.captureWorkspaceRules(ctx, userID, adminAccountID)
	if err != nil {
		return err
	}
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
		workspaceCtx := ctx
		if acquireWorkspaceLease {
			var err error
			workspaceCtx, release, _, err = s.acquireActionLease(ctx, priorityRuntimeLeaseKey(userID, adminAccountID), true)
			if err != nil {
				log.Printf("[connection-health] priority sync acquire workspace lease failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
				continue
			}
			workspaceCtx = context.WithValue(workspaceCtx, workspacePriorityLeaseContextKey{}, actionLeaseFromContext(workspaceCtx))
		}
		func() {
			ctx := workspaceCtx
			defer release()
			expectedPendingSignature, jobGeneration := expectedGenerations[workspaceKey]
			if jobGeneration {
				current, err := s.repo.IsPriorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, expectedPendingSignature)
				if err != nil {
					log.Printf("[connection-health] priority sync verify workspace generation failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
					s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1, nil)
					return
				}
				if !current {
					return
				}
			} else {
				workspaceState, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
				if err != nil {
					log.Printf("[connection-health] priority sync load workspace generation failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
					s.markPriorityWorkspaceHealthSyncFailedDirect(userID, adminAccountID, err, 1, nil)
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
				s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, syncErr, 1, nil)
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
			s.rememberActionInventory(userID, adminAccountID, inventorySnapshot)
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
			config, err := s.capturedWorkspaceRules(ctx, userID, adminAccountID, policies)
			if err != nil {
				failGeneration(err)
				return
			}
			for _, item := range inventory {
				item.target.ConfigGeneration = config.ConfigGeneration
				item.target.ConfigGenerationKnown = config.RuleVersion != ""
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
	inventoryComplete := adminInventoryComplete(*snapshot)
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
					snapshotStartedAt: snapshot.snapshotStartedAt,
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
			item.target.TestMemberships = append(item.target.TestMemberships, TestConfigurationSource{AdminGroupID: group.ID, AdminGroupName: group.Name})
			item.target.InventoryComplete = inventoryComplete
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
	if session.Platform == upstream.PlatformSub2API {
		// Read the tier only after the rule generation was captured. A failed
		// tier read cannot silently turn every primary into a standby.
		var err error
		ctx, err = s.captureWorkspaceRulesIfMissing(ctx, userID, adminAccountID, inventory)
		if err != nil {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1, nil)
			return
		}
		tiers, err := s.accountTiersForDecision(ctx, userID, adminAccountID)
		if err != nil {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1, nil)
			return
		}
		for id, item := range inventory {
			item.accountTier = effectiveAccountTier(tiers[id])
		}
	}
	generationCurrent := func() bool {
		current, err := s.priorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, expectedPendingSignature)
		if err != nil {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, err, 1, nil)
			return false
		}
		return current
	}
	if session.Platform == upstream.PlatformSub2API {
		configs, configErr := s.repo.ListGroupTestConfigurations(ctx, userID, adminAccountID)
		for _, item := range inventory {
			item.target.InventoryComplete = inventoryComplete
			item.target.TestConfiguration = ResolveGroupTestConfiguration(string(upstream.PlatformSub2API), item.target.TestMemberships, inventoryComplete && configErr == nil, configs)
		}
		if !inventoryComplete {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, requestError(ErrorPriorityInventoryIncomplete), 1, nil)
			return
		}
	}
	failedCount := 0
	var failedTargets, waitingTargets []priorityTargetFailure
	waitingSince := make(map[string]time.Time)
	previousWaitingSince := map[string]time.Time{}
	if previous, exists := s.priorityWaitingSince.Load(priorityRuntimeLeaseKey(userID, adminAccountID)); exists {
		previousWaitingSince = previous.(map[string]time.Time)
	}
	recordTargetFailure := func(targetID string, err error, visible bool) {
		reason := classifyPriorityTargetFailure(err, visible)
		accountID, _ := scopedActionAccountID(targetID, adminAccountID)
		if reason == "waiting" {
			now := s.actionTime()
			since, exists := previousWaitingSince[targetID]
			if !exists {
				since = now
			}
			waitingSince[targetID] = since
			if now.Sub(since) >= priorityWaitingTimeout {
				reason = "waiting_timeout"
			} else {
				waitingTargets = append(waitingTargets, priorityTargetFailure{targetID: targetID, accountID: accountID, reason: reason, err: err})
				return
			}
		}
		failedCount++
		failedTargets = append(failedTargets, priorityTargetFailure{targetID: targetID, accountID: accountID, reason: reason, err: err})
	}
	blockedMultiplierCount := 0
	processableMultiplierCount := 0
	incompleteCount := 0
	statesByTarget := make(map[string][]ConnectionHealthState)
	for _, state := range healthStates {
		if _, isTarget := parseTargetID(state.ConnectionID); isTarget {
			statesByTarget[state.ConnectionID] = append(statesByTarget[state.ConnectionID], state)
		}
	}

	storedByTarget := make(map[string]PrioritySyncState, len(syncStates))
	reconcileFailedTargets := make(map[string]struct{})
	for _, state := range syncStates {
		storedByTarget[state.TargetID] = state
	}

	if session.Platform == upstream.PlatformSub2API {
		// Previously admitted calls may finish after configuration or multiplier
		// eligibility changes. Their receipts still consume this existing refresh.
		for targetID, stored := range storedByTarget {
			item := inventory[targetID]
			observation := RemoteActionObservation{RemoteActionScope: RemoteActionScope{userID, adminAccountID, targetID}, InventoryComplete: inventoryComplete}
			if item != nil {
				observation.Visible = true
				observation.SnapshotStartedAt = item.snapshotStartedAt
				observation.Status = item.target.AccountStatus
				observation.Weight = item.target.AccountWeight
				observation.Schedulable = item.target.Schedulable
			}
			if item != nil && item.priorityPresent {
				value := item.currentPriority
				observation.Priority = &value
			}
			pair, reconcileErr := s.reconcileActionObservation(ctx, observation)
			if reconcileErr != nil {
				recordTargetFailure(targetID, reconcileErr, inventory[targetID] != nil)
				reconcileFailedTargets[targetID] = struct{}{}
				continue
			}
			if pair.Priority != nil {
				stored = *pair.Priority
				storedByTarget[targetID] = stored
			} else {
				delete(storedByTarget, targetID)
			}
		}
	}

	managed := make(map[string]*priorityTargetInventory)
	if session.Platform == upstream.PlatformSub2API {
		// Ordinary manual ownership takes precedence over loss of policies,
		// exclusions and multiplier blockers. Old 1–9 baselines retain their
		// original multiplier-only comparison and exit restoration semantics.
		for targetID, stored := range storedByTarget {
			item := inventory[targetID]
			if item == nil || !item.priorityPresent || item.currentPriority < 1 || item.currentPriority > 9 || hasMultiplierOnlyPolicy(item.policies) || oldPriorityComparisonBaseline(&stored) {
				continue
			}
			if _, failed := reconcileFailedTargets[targetID]; failed {
				continue
			}
			if err := s.syncSafePriorityTarget(ctx, session, userID, adminAccountID, targetID, item, &stored, 0, nil, nil, expectedPendingSignature, false, false); err != nil {
				recordTargetFailure(targetID, err, true)
				reconcileFailedTargets[targetID] = struct{}{}
			} else {
				delete(storedByTarget, targetID)
			}
		}
	}
	hardExcludedHealthTargets := make(map[string]struct{})
	missingMultiplier := make(map[string]struct{})
	effectiveMultiplierByTarget := make(map[string]float64)
	desiredByTarget := make(map[string]int)
	multiplierOnlyTargets := make(map[string]float64)
	healthCandidates := make([]healthPriorityCandidate, 0)
	for targetID, item := range inventory {
		if _, failed := reconcileFailedTargets[targetID]; failed {
			continue
		}
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
		if session.Platform == upstream.PlatformSub2API {
			activeModels := activeHealthPriorityModels(item)
			activeStates := activeHealthPriorityStates(statesByTarget[targetID], activeModels)
			if !healthStatesUsableForTarget(item.target, activeStates, len(activeModels)) {
				managed[targetID] = item
				hardExcludedHealthTargets[targetID] = struct{}{}
				continue
			}
		}
		if isPriorityMultiplierBlocker(item.upstreamMultiplier.status) {
			missingMultiplier[targetID] = struct{}{}
			if stored, exists := storedByTarget[targetID]; !exists || !stored.Conflict {
				blockedMultiplierCount++
			}
			continue
		}
		processableMultiplierCount++

		multiplier, available := effectiveHealthSortMultiplier(item)
		if !available {
			activeModels := activeHealthPriorityModels(item)
			activeStates := activeHealthPriorityStates(statesByTarget[targetID], activeModels)
			healthBand := priorityHealthBand(activeStates, len(activeModels))
			managed[targetID] = item
			if session.Platform == upstream.PlatformSub2API && item.accountTier == 1 {
				healthCandidates = append(healthCandidates, healthPriorityCandidate{
					targetID: targetID, item: item, accountTier: 1, multiplierUnknown: true,
					states: activeStates, expectedModels: len(activeModels), healthBand: healthBand,
					ruleVersion: priorityRuleVersionForTarget(item.target.Platform, item.policies, activeStates),
					latencyMs:   targetSuccessLatency(item.target, activeStates, activeModels),
				})
				continue
			}
			desiredByTarget[targetID] = desiredHealthBandEndForPlatform(session.Platform, healthBand)
			continue
		}
		activeModels := activeHealthPriorityModels(item)
		activeStates := activeHealthPriorityStates(statesByTarget[targetID], activeModels)
		candidate := healthPriorityCandidate{
			accountTier: item.accountTier,
			targetID:    targetID, item: item, multiplier: multiplier, states: activeStates, ruleVersion: priorityRuleVersionForTarget(item.target.Platform, item.policies, activeStates),
			expectedModels: len(activeModels), healthBand: priorityHealthBand(activeStates, len(activeModels)),
			latencyMs: targetSuccessLatency(item.target, activeStates, activeModels),
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

	for id, desired := range encodeHealthPriorityCandidates(session.Platform, healthCandidates) {
		desiredByTarget[id] = desired
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
		if session.Platform == upstream.PlatformSub2API {
			if !generationCurrent() {
				return
			}
			_, blocked := hardExcludedHealthTargets[targetID]
			var multiplierValue *float64
			if multiplierAvailable {
				copy := multiplier
				multiplierValue = &copy
			}
			if err := s.syncSafePriorityTarget(ctx, session, userID, adminAccountID, targetID, item, &stored, desired, multiplierValue, statesByTarget[targetID], expectedPendingSignature, false, !blocked); err != nil {
				recordTargetFailure(targetID, err, true)
			}
			continue
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
		if _, failed := reconcileFailedTargets[targetID]; failed {
			continue
		}
		if _, stillManaged := managed[targetID]; stillManaged {
			continue
		}
		if _, waitingForMultiplier := missingMultiplier[targetID]; waitingForMultiplier {
			continue
		}
		item := inventory[targetID]
		if session.Platform == upstream.PlatformSub2API {
			if !generationCurrent() {
				return
			}
			if err := s.syncSafePriorityTarget(ctx, session, userID, adminAccountID, targetID, item, &stored, stored.OriginalPriority, nil, nil, expectedPendingSignature, true, true); err != nil {
				recordTargetFailure(targetID, err, inventory[targetID] != nil)
			}
			continue
		}
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
		incompleteFailures := failedCount + incompleteCount
		if incompleteFailures == 0 {
			incompleteFailures = 1
		}
		s.markPriorityWorkspaceSyncFailed(
			userID,
			adminAccountID,
			expectedPendingSignature,
			requestError(ErrorPriorityInventoryIncomplete),
			incompleteFailures, nil,
		)
		return
	}
	if session.Platform == upstream.PlatformSub2API {
		s.priorityWaitingSince.Store(priorityRuntimeLeaseKey(userID, adminAccountID), waitingSince)
		s.logPrioritySyncRound(userID, adminAccountID, failedTargets, waitingTargets, s.actionTime())
	}
	if failedCount > 0 {
		if session.Platform == upstream.PlatformSub2API {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, priorityTargetFailureError(failedTargets), failedCount, failedTargets)
		} else {
			s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, expectedPendingSignature, requestError(ErrorUnknown), failedCount, nil)
		}
		return
	}
	if blockedMultiplierCount > 0 {
		if processableMultiplierCount == 0 {
			s.markPriorityWorkspaceSyncFailed(
				userID, adminAccountID, expectedPendingSignature,
				requestError(ErrorPriorityMetadataUnavailable), blockedMultiplierCount, nil,
			)
			return
		}
		s.markPriorityWorkspaceSyncPartial(userID, adminAccountID, expectedPendingSignature, blockedMultiplierCount, nil)
		return
	}
	s.markPriorityWorkspaceSyncSucceeded(userID, adminAccountID, expectedPendingSignature, nil)
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
	leftSegment, rightSegment := healthPrioritySegment(left), healthPrioritySegment(right)
	if leftSegment != rightSegment {
		if leftSegment < rightSegment {
			return -1
		}
		return 1
	}
	if left.multiplierUnknown != right.multiplierUnknown {
		if !left.multiplierUnknown {
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
	if candidateUsesV2(left) && candidateUsesV2(right) {
		if left.targetID < right.targetID {
			return -1
		}
		if left.targetID > right.targetID {
			return 1
		}
		return 0
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
	return effectiveHealthSortMultiplierFromResolution(item.upstreamMultiplier, item.fallbackMultipliers)
}

func effectiveHealthSortMultiplierFromResolution(resolution upstreamMultiplierResolution, fallbackMultipliers []float64) (float64, bool) {
	switch resolution.status {
	case MultiplierResolutionResolved:
		if resolution.info.effectiveMultiplier != nil {
			return *resolution.info.effectiveMultiplier, true
		}
	case MultiplierResolutionDisabled, MultiplierResolutionUnavailable, MultiplierResolutionStale, MultiplierResolutionUpdating, MultiplierResolutionMissing:
		return 0, false
	}
	return uniqueFloat(fallbackMultipliers)
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

func targetSuccessLatency(target AdminProbeTarget, states []ConnectionHealthState, activeModels map[string]struct{}) *int {
	if target.Platform != string(upstream.PlatformSub2API) {
		return completeTargetSuccessLatency(states, activeModels)
	}
	if !healthStatesUsableForTarget(target, states, len(activeModels)) {
		return nil
	}
	current := append([]ConnectionHealthState(nil), states...)
	for index := range current {
		current[index].LastSuccessLatencyMs = successLatencyForProtocol(current[index], target.TestConfiguration.Protocol)
	}
	return completeTargetSuccessLatency(current, activeModels)
}

func priorityRuleVersion(policies []Policy, states []ConnectionHealthState) string {
	for _, policy := range policies {
		if policy.RuleVersion == RuleVersionV2 {
			return RuleVersionV2
		}
	}
	for _, state := range states {
		if state.RuleVersion == RuleVersionV2 {
			return RuleVersionV2
		}
	}
	return RuleVersionLegacy
}
func candidateUsesV2(candidate healthPriorityCandidate) bool {
	if candidate.item != nil && candidate.item.target.Platform == string(upstream.PlatformNewAPI) {
		return false
	}
	if candidate.ruleVersion == RuleVersionV2 {
		return true
	}
	if candidate.item != nil {
		return priorityRuleVersion(candidate.item.policies, candidate.states) == RuleVersionV2
	}
	return priorityRuleVersion(nil, candidate.states) == RuleVersionV2
}

func priorityRuleVersionForTarget(platform string, policies []Policy, states []ConnectionHealthState) string {
	if platform == string(upstream.PlatformNewAPI) {
		return RuleVersionLegacy
	}
	return priorityRuleVersion(policies, states)
}

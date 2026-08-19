package connection_health

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	RemoteActionSkippedTargetConflict          = "skipped_target_conflict"
	RemoteActionSkippedTargetInitiallyDisabled = "skipped_target_initially_disabled"
	RemoteActionSkippedUpstreamScheduling      = "skipped_upstream_scheduling_disabled"
	RemoteActionSkippedSub2APILastActive       = "skipped_sub2api_group_last_active"
	RemoteActionSkippedSub2APILastUsable       = "skipped_sub2api_group_last_usable"
	RemoteActionSkippedSub2APIInventory        = "skipped_sub2api_group_inventory_incomplete"
)

type targetRemoteActionResult struct {
	remoteAction   string
	adminGroupID   string
	adminGroupName string
}

type targetInventoryObservation struct {
	groupID        string
	groupName      string
	status         string
	statusKnown    bool
	schedulable    bool
	schedulableSet bool
}

type workspaceFloorGuard struct {
	mu                   sync.Mutex
	reservedUnavailable  map[string]struct{}
	inventory            *adminWorkspaceInventory
	inventoryFingerprint string
	snapshotAt           time.Time
}

func newWorkspaceFloorGuard() *workspaceFloorGuard {
	return &workspaceFloorGuard{reservedUnavailable: make(map[string]struct{})}
}

func (g *workspaceFloorGuard) reserveSub2APIInactive(target AdminProbeTarget, inventory adminWorkspaceInventory, scope adminMonitoringScope) targetRemoteActionResult {
	return g.reserveSub2APIMutation(target, inventory, scope)
}

func (g *workspaceFloorGuard) reserveSub2APISchedulableFalse(target AdminProbeTarget, inventory adminWorkspaceInventory, scope adminMonitoringScope) targetRemoteActionResult {
	return g.reserveSub2APIMutation(target, inventory, scope)
}

func (g *workspaceFloorGuard) rememberInventory(inventory adminWorkspaceInventory) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rememberInventoryLocked(inventory)
}

func (g *workspaceFloorGuard) rememberInventoryLocked(inventory adminWorkspaceInventory) {
	if adminInventoryComplete(inventory) {
		fingerprint := adminInventoryFingerprint(inventory)
		if g.inventoryFingerprint != fingerprint {
			g.reservedUnavailable = make(map[string]struct{})
			g.inventoryFingerprint = fingerprint
		}
	}
	g.inventory = &inventory
	g.snapshotAt = time.Now().UTC()
}

func adminInventoryComplete(inventory adminWorkspaceInventory) bool {
	for _, group := range inventory.groups {
		if group.err != nil {
			return false
		}
	}
	return true
}

func (g *workspaceFloorGuard) latestInventory(maxAge time.Duration) (*adminWorkspaceInventory, bool) {
	if g == nil {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inventory == nil || g.snapshotAt.IsZero() || time.Since(g.snapshotAt) > maxAge {
		return nil, false
	}
	return g.inventory, true
}

func (g *workspaceFloorGuard) reserveSub2APIMutation(target AdminProbeTarget, inventory adminWorkspaceInventory, scope adminMonitoringScope) targetRemoteActionResult {
	if g == nil {
		return targetRemoteActionResult{}
	}
	for _, groupInventory := range inventory.groups {
		if groupInventory.err != nil {
			return targetRemoteActionResult{
				remoteAction: RemoteActionSkippedSub2APIInventory,
				adminGroupID: groupInventory.group.ID, adminGroupName: groupInventory.group.Name,
			}
		}
	}
	if !scope.complete {
		return targetRemoteActionResult{
			remoteAction: RemoteActionSkippedSub2APIInventory,
			adminGroupID: target.AdminGroupID, adminGroupName: target.AdminGroupName,
		}
	}
	parsed, ok := parseTargetID(target.TargetID)
	if !ok {
		return targetRemoteActionResult{remoteAction: RemoteActionSkippedSub2APIInventory}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rememberInventoryLocked(inventory)
	// A reservation represents a destructive account mutation already admitted against
	// the cached upstream inventory. A policy/scope change alone cannot prove that the
	// upstream account has become usable again, so reservations remain until a new
	// inventory fingerprint is observed by rememberInventoryLocked.

	monitoredMemberships := make(map[string]struct{})
	for groupID, monitoredTargets := range scope.monitoredByGroup {
		if _, monitored := monitoredTargets[target.TargetID]; monitored {
			monitoredMemberships[groupID] = struct{}{}
		}
	}
	if len(monitoredMemberships) == 0 {
		return targetRemoteActionResult{}
	}

	usableByGroup := make(map[string]map[string]struct{}, len(inventory.groups))
	unknownByGroup := make(map[string]bool, len(inventory.groups))
	memberships := make(map[string]string)
	seenMonitoredByGroup := make(map[string]map[string]struct{}, len(inventory.groups))
	targetObservations := make([]targetInventoryObservation, 0)
	for _, groupInventory := range inventory.groups {
		monitoredTargets := scope.monitoredByGroup[groupInventory.group.ID]
		usableTargets := make(map[string]struct{})
		seenTargets := make(map[string]struct{})
		for _, account := range groupInventory.accounts {
			targetID := buildTargetID(target.Platform, parsed.adminAccountID, account.ID)
			status, statusKnown := normalizeFloorTargetStatus(target.Platform, account.Status)
			if targetID == target.TargetID {
				observation := targetInventoryObservation{
					groupID: groupInventory.group.ID, groupName: groupInventory.group.Name,
					status: status, statusKnown: statusKnown,
					schedulableSet: account.Schedulable != nil,
				}
				if account.Schedulable != nil {
					observation.schedulable = *account.Schedulable
				}
				targetObservations = append(targetObservations, observation)
			}
			if _, monitored := monitoredTargets[targetID]; !monitored {
				continue
			}
			seenTargets[targetID] = struct{}{}
			if targetID == target.TargetID {
				memberships[groupInventory.group.ID] = groupInventory.group.Name
			}
			if target.Platform == string(upstream.PlatformSub2API) && !statusKnown {
				unknownByGroup[groupInventory.group.ID] = true
				continue
			}
			if targetStatusEnabled(target.Platform, status) && account.Schedulable == nil {
				unknownByGroup[groupInventory.group.ID] = true
			}
			if targetStatusEnabled(target.Platform, status) && account.Schedulable != nil && *account.Schedulable {
				usableTargets[targetID] = struct{}{}
			}
		}
		if monitoredTargets != nil {
			usableByGroup[groupInventory.group.ID] = usableTargets
			seenMonitoredByGroup[groupInventory.group.ID] = seenTargets
		}
	}
	for groupID := range monitoredMemberships {
		if _, found := memberships[groupID]; !found {
			return targetRemoteActionResult{remoteAction: RemoteActionSkippedSub2APIInventory, adminGroupID: groupID}
		}
		for targetID := range scope.monitoredByGroup[groupID] {
			if _, found := seenMonitoredByGroup[groupID][targetID]; !found {
				return targetRemoteActionResult{
					remoteAction: RemoteActionSkippedSub2APIInventory,
					adminGroupID: groupID, adminGroupName: memberships[groupID],
				}
			}
		}
	}
	if len(memberships) == 0 {
		return targetRemoteActionResult{remoteAction: RemoteActionSkippedSub2APIInventory}
	}
	if conflictingTargetInventory(targetObservations) {
		observation := targetObservations[0]
		return targetRemoteActionResult{
			remoteAction: RemoteActionSkippedSub2APIInventory,
			adminGroupID: observation.groupID, adminGroupName: observation.groupName,
		}
	}

	groupIDs := make([]string, 0, len(memberships))
	for groupID := range memberships {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		remaining := 0
		candidateUsable := false
		for targetID := range usableByGroup[groupID] {
			if targetID == target.TargetID {
				candidateUsable = true
			}
			if _, reserved := g.reservedUnavailable[targetID]; !reserved {
				remaining++
			}
		}
		if !candidateUsable {
			if unknownByGroup[groupID] {
				return targetRemoteActionResult{
					remoteAction: RemoteActionSkippedSub2APIInventory,
					adminGroupID: groupID, adminGroupName: memberships[groupID],
				}
			}
			// The candidate is already unusable, so the requested destructive action does not
			// consume the last usable slot in this group.
			continue
		}
		if unknownByGroup[groupID] && remaining <= 1 {
			return targetRemoteActionResult{
				remoteAction: RemoteActionSkippedSub2APIInventory,
				adminGroupID: groupID, adminGroupName: memberships[groupID],
			}
		}
		if remaining <= 1 {
			return targetRemoteActionResult{
				remoteAction: RemoteActionSkippedSub2APILastActive,
				adminGroupID: groupID, adminGroupName: memberships[groupID],
			}
		}
	}
	g.reservedUnavailable[target.TargetID] = struct{}{}
	return targetRemoteActionResult{}
}

func normalizeFloorTargetStatus(platform string, status string) (string, bool) {
	if platform != string(upstream.PlatformSub2API) {
		return normalizeTargetStatus(platform, status), true
	}
	normalized := strings.ToLower(strings.TrimSpace(status))
	switch normalized {
	case "active":
		return "active", true
	case "inactive", "disabled", "2":
		return "inactive", true
	default:
		return "", false
	}
}

func adminInventoryFingerprint(inventory adminWorkspaceInventory) string {
	type accountSnapshot struct {
		id, name, status, platform, models string
		schedulable, weight                string
	}
	type groupSnapshot struct {
		id, name, err string
		accounts      []accountSnapshot
	}
	groups := make([]groupSnapshot, 0, len(inventory.groups))
	for _, group := range inventory.groups {
		snapshot := groupSnapshot{id: group.group.ID, name: group.group.Name}
		if group.err != nil {
			snapshot.err = group.err.Error()
		}
		for _, account := range group.accounts {
			accountSnapshot := accountSnapshot{
				id: account.ID, name: account.Name, status: account.Status,
				platform: account.Platform, models: account.Models,
				schedulable: fmt.Sprintf("%t", account.Schedulable != nil && *account.Schedulable),
			}
			if account.Schedulable == nil {
				accountSnapshot.schedulable = "nil"
			}
			if account.Weight != nil {
				accountSnapshot.weight = fmt.Sprintf("%d", *account.Weight)
			} else {
				accountSnapshot.weight = "nil"
			}
			snapshot.accounts = append(snapshot.accounts, accountSnapshot)
		}
		sort.Slice(snapshot.accounts, func(i, j int) bool { return snapshot.accounts[i].id < snapshot.accounts[j].id })
		groups = append(groups, snapshot)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].id < groups[j].id })
	var builder strings.Builder
	builder.WriteString(string(inventory.session.Platform))
	for _, group := range groups {
		fmt.Fprintf(&builder, "|g:%s:%s:%s", group.id, group.name, group.err)
		for _, account := range group.accounts {
			fmt.Fprintf(&builder, "|a:%s:%s:%s:%s:%s:%s:%s", account.id, account.name, account.status, account.platform, account.models, account.schedulable, account.weight)
		}
	}
	return builder.String()
}

func inventoryTargetAlreadyUnavailable(inventory adminWorkspaceInventory, targetID string) bool {
	parsed, ok := parseTargetID(targetID)
	if !ok || parsed.platform != string(upstream.PlatformSub2API) {
		return false
	}
	observations := make([]targetInventoryObservation, 0)
	for _, group := range inventory.groups {
		for _, account := range group.accounts {
			if account.ID != parsed.accountID {
				continue
			}
			status, known := normalizeFloorTargetStatus(parsed.platform, account.Status)
			observation := targetInventoryObservation{
				groupID: group.group.ID, groupName: group.group.Name,
				status: status, statusKnown: known,
				schedulableSet: account.Schedulable != nil,
			}
			if account.Schedulable != nil {
				observation.schedulable = *account.Schedulable
			}
			observations = append(observations, observation)
		}
	}
	if conflictingTargetInventory(observations) {
		return false
	}
	for _, observation := range observations {
		if observation.status != "inactive" && observation.schedulable {
			return false
		}
	}
	return len(observations) > 0
}

func conflictingTargetInventory(observations []targetInventoryObservation) bool {
	if len(observations) == 0 {
		return true
	}
	first := observations[0]
	if !first.statusKnown || !first.schedulableSet {
		return true
	}
	for _, observation := range observations[1:] {
		if !observation.statusKnown || !observation.schedulableSet {
			return true
		}
		if observation.status != first.status || observation.schedulable != first.schedulable {
			return true
		}
	}
	return false
}

func targetActionAuditOnly(action string) bool {
	switch action {
	case RemoteActionSkippedUpstreamScheduling, RemoteActionSkippedSub2APILastActive, RemoteActionSkippedSub2APILastUsable, RemoteActionSkippedSub2APIInventory:
		return true
	default:
		return false
	}
}

// reconcileTargetRemoteAction 把同一账号当前仍启用的全部模型状态聚合成一次上游动作。
// 模型仍独立记录健康，但账号/渠道是共享资源，不能让后执行的健康模型覆盖先前故障模型的停用决定。
func (s *Service) reconcileTargetRemoteAction(
	ctx context.Context,
	userID string,
	adminAccountID string,
	session upstream.Session,
	target AdminProbeTarget,
	specs []probeModelSpec,
) (string, error) {
	result, err := s.reconcileTargetRemoteActionWithFloor(ctx, userID, adminAccountID, session, target, specs, nil, nil, adminMonitoringScope{})
	return result.remoteAction, err
}

func (s *Service) reconcileTargetRemoteActionWithFloor(
	ctx context.Context,
	userID string,
	adminAccountID string,
	session upstream.Session,
	target AdminProbeTarget,
	specs []probeModelSpec,
	floorGuard *workspaceFloorGuard,
	inventory *adminWorkspaceInventory,
	monitoringScope adminMonitoringScope,
) (targetRemoteActionResult, error) {
	return s.reconcileTargetRemoteActionWithFloorMode(ctx, userID, adminAccountID, session, target, specs, floorGuard, inventory, monitoringScope, true)
}

func (s *Service) reconcileTargetRemoteActionWithFloorMode(
	ctx context.Context,
	userID string,
	adminAccountID string,
	session upstream.Session,
	target AdminProbeTarget,
	specs []probeModelSpec,
	floorGuard *workspaceFloorGuard,
	inventory *adminWorkspaceInventory,
	monitoringScope adminMonitoringScope,
	allowSub2APIInactive bool,
) (targetRemoteActionResult, error) {
	controlledModels := make(map[string]struct{})
	for _, spec := range specs {
		if spec.policy.Enabled && policyRemoteActionEnabled(spec.policy) {
			controlledModels[spec.modelName] = struct{}{}
		}
	}
	if len(controlledModels) == 0 {
		return targetRemoteActionResult{}, nil
	}

	allStates, err := s.repo.ListStatesByConnection(ctx, target.TargetID)
	if err != nil {
		return targetRemoteActionResult{}, err
	}
	states := make([]ConnectionHealthState, 0, len(controlledModels))
	for _, state := range allStates {
		if _, active := controlledModels[state.ModelName]; active {
			states = append(states, state)
		}
	}
	if len(states) == 0 {
		return targetRemoteActionResult{}, nil
	}
	statesComplete := len(states) == len(controlledModels)

	stored, err := s.repo.GetTargetActionState(ctx, userID, adminAccountID, target.TargetID)
	if err != nil {
		return targetRemoteActionResult{}, err
	}
	allHealthy, blocked, minWeight := aggregateTargetStates(states)
	allHealthy = allHealthy && statesComplete
	// 普通 degraded 只记录模型健康；只有已经接管或进入暂停/观察/恢复阶段时才修改上游。
	if stored == nil && (!statesComplete || (!blocked && !hasRecoveringState(states))) {
		return targetRemoteActionResult{}, nil
	}
	// 已接管目标只有在全部受控模型都有状态后才能开始恢复。缺失状态不能被当作健康，
	// 但如果已有模型明确进入暂停，仍需允许下面的 blocked 分支继续执行降级动作。
	if stored != nil && !statesComplete && !blocked {
		return targetRemoteActionResult{}, nil
	}
	if target.Schedulable != nil && !*target.Schedulable {
		return targetRemoteActionResult{remoteAction: RemoteActionSkippedUpstreamScheduling}, nil
	}

	currentStatus := normalizeTargetStatus(target.Platform, target.AccountStatus)
	currentWeight := normalizedTargetWeight(target)
	newCheckpoint := false
	if stored == nil {
		originalStatus := currentStatus
		originalWeight := cloneIntPointer(currentWeight)
		// 用户原本就在上游暂停的账号不属于自动恢复对象，探活可以继续，但绝不替用户启用。
		if !targetStatusEnabled(target.Platform, currentStatus) {
			if !legacyTargetWasManaged(states) {
				return targetRemoteActionResult{remoteAction: RemoteActionSkippedTargetInitiallyDisabled}, nil
			}
			// 升级前已由健康模块停用的目标没有动作快照。仅在历史 remote_action 能明确证明
			// 是系统执行的情况下，按旧默认 active/100 建立一次兼容快照。
			originalStatus, originalWeight = legacyOriginalTargetState(target.Platform)
		}
		stored = &TargetActionState{
			UserID: userID, AdminAccountID: adminAccountID, TargetID: target.TargetID,
			OriginalStatus: originalStatus, OriginalWeight: cloneIntPointer(originalWeight),
			LastAppliedStatus: currentStatus, LastAppliedWeight: cloneIntPointer(currentWeight),
		}
		newCheckpoint = true
	} else if targetActionCheckpointConflicted(target, stored, currentStatus, currentWeight) {
		stored.Conflict = true
		stored.PendingStatus = ""
		stored.PendingWeight = nil
		if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
			return targetRemoteActionResult{}, err
		}
		return targetRemoteActionResult{remoteAction: RemoteActionSkippedTargetConflict}, nil
	}
	if stored.Conflict {
		return targetRemoteActionResult{remoteAction: RemoteActionSkippedTargetConflict}, nil
	}

	desiredStatus, desiredWeight := desiredTargetState(target.Platform, allHealthy, blocked, minWeight, *stored)
	if !allowSub2APIInactive && target.Platform == string(upstream.PlatformSub2API) &&
		normalizeTargetStatus(target.Platform, desiredStatus) == "inactive" {
		return targetRemoteActionResult{}, nil
	}
	if newCheckpoint {
		if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
			return targetRemoteActionResult{}, err
		}
	}
	if targetStateEqual(target, currentStatus, currentWeight, desiredStatus, desiredWeight) {
		stored.LastAppliedStatus = desiredStatus
		stored.LastAppliedWeight = cloneIntPointer(desiredWeight)
		stored.PendingStatus = ""
		stored.PendingWeight = nil
		if allHealthy {
			return targetRemoteActionResult{}, s.repo.DeleteTargetActionState(ctx, userID, adminAccountID, target.TargetID)
		}
		return targetRemoteActionResult{}, s.repo.UpsertTargetActionState(ctx, *stored)
	}

	if target.Platform == string(upstream.PlatformSub2API) &&
		normalizeTargetStatus(target.Platform, desiredStatus) == "inactive" &&
		targetStatusEnabled(target.Platform, currentStatus) {
		if floorGuard == nil || inventory == nil {
			stored.PendingStatus = ""
			stored.PendingWeight = nil
			if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
				return targetRemoteActionResult{}, err
			}
			return targetRemoteActionResult{remoteAction: RemoteActionSkippedSub2APIInventory}, nil
		}
		mutationRelease, err := s.repo.AcquireSub2APIMutationLease(ctx, userID, adminAccountID)
		if err != nil {
			return targetRemoteActionResult{}, err
		}
		defer mutationRelease()
		floorGuard.rememberInventory(*inventory)
		floorResult := floorGuard.reserveSub2APIInactive(target, *inventory, monitoringScope)
		if floorResult.remoteAction != "" {
			stored.PendingStatus = ""
			stored.PendingWeight = nil
			if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
				return targetRemoteActionResult{}, err
			}
			return floorResult, nil
		}
	}

	// Persist the intended value before touching the upstream. A later database failure can
	// then be recognized as a completed system write instead of a manual conflict.
	stored.PendingStatus = desiredStatus
	stored.PendingWeight = cloneIntPointer(desiredWeight)
	if err := s.repo.UpsertTargetActionState(ctx, *stored); err != nil {
		return targetRemoteActionResult{}, err
	}
	action, actionErr := s.dispatcher.ApplyTargetState(ctx, session, target, desiredWeight, desiredStatus)
	if actionErr != nil {
		log.Printf("[connection-health] aggregate target action failed target_id=%s action=%s err=%v", target.TargetID, action, actionErr)
		return targetRemoteActionResult{remoteAction: action}, actionErr
	}
	stored.LastAppliedStatus = desiredStatus
	stored.LastAppliedWeight = cloneIntPointer(desiredWeight)
	stored.PendingStatus = ""
	stored.PendingWeight = nil
	if allHealthy {
		return targetRemoteActionResult{remoteAction: action}, s.repo.DeleteTargetActionState(ctx, userID, adminAccountID, target.TargetID)
	}
	return targetRemoteActionResult{remoteAction: action}, s.repo.UpsertTargetActionState(ctx, *stored)
}

// restoreUnmanagedTargetActions 恢复已经失去有效自动动作策略的目标。用户解绑分组、禁用策略、
// 删除最后一个模型或把目标加入排除列表后，都不能把此前由系统暂停的账号永久留在上游。
func (s *Service) restoreUnmanagedTargetActions(
	ctx context.Context,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
	states []TargetActionState,
	inventoryCache adminInventoryCache,
) {
	if len(states) == 0 {
		return
	}
	targetPolicies := assignedEnabledPoliciesByTarget(policies, targetAssignments)
	groupPolicies := assignedEnabledPoliciesByGroup(policies, groupAssignments)
	excluded := groupTargetExclusionIndex(exclusions)
	for _, stored := range states {
		inventory, err := s.loadAdminInventory(ctx, stored.UserID, stored.AdminAccountID, inventoryCache)
		if err != nil {
			log.Printf("[connection-health] restore unmanaged target inventory failed target_id=%s err=%v", stored.TargetID, err)
			continue
		}
		inventoryComplete := true
		for _, groupInventory := range inventory.groups {
			if groupInventory.err != nil {
				inventoryComplete = false
				break
			}
		}
		if !inventoryComplete {
			// 任一分组成员读取失败时无法证明目标已经失去全部管理关系，保持当前状态更安全。
			continue
		}
		var target AdminProbeTarget
		found := false
		targetPolicySet := targetPolicies[stored.UserID+"|"+stored.AdminAccountID][stored.TargetID]
		inheritedPolicies := make([]Policy, 0)
		for _, groupInventory := range inventory.groups {
			if groupInventory.err != nil {
				continue
			}
			for _, account := range groupInventory.accounts {
				targetID := buildTargetID(string(inventory.session.Platform), stored.AdminAccountID, account.ID)
				if targetID != stored.TargetID {
					continue
				}
				if !found {
					target = AdminProbeTarget{
						TargetID: targetID, Platform: string(inventory.session.Platform),
						AdminGroupID: groupInventory.group.ID, AdminGroupName: groupInventory.group.Name,
						AccountID: account.ID, AccountName: account.Name, AccountStatus: account.Status,
						AccountWeight: cloneIntPointer(account.Weight), ProviderFamily: account.Platform,
						Models: splitModelList(account.Models),
					}
					found = true
				}
				workspaceKey := stored.UserID + "|" + stored.AdminAccountID
				if !excluded[workspaceKey][groupInventory.group.ID][targetID] {
					inheritedPolicies = mergePoliciesByID(inheritedPolicies, groupPolicies[workspaceKey][groupInventory.group.ID])
				}
			}
		}
		effectivePolicies := effectivePoliciesForTarget(targetPolicySet, inheritedPolicies)
		if hasRemoteActionModel(candidateModelSpecs(target.Models, effectivePolicies)) {
			continue
		}
		targetVisible := found
		if !found {
			parsed, ok := parseTargetID(stored.TargetID)
			if !ok || parsed.adminAccountID != stored.AdminAccountID || parsed.platform != string(inventory.session.Platform) {
				continue
			}
			// The account can remain upstream after being removed from every group. We no longer
			// have a list snapshot for conflict detection, but restoring the captured original
			// value is safer than leaving a system-disabled account stuck forever.
			target = AdminProbeTarget{
				TargetID: stored.TargetID, Platform: parsed.platform, AccountID: parsed.accountID,
				AccountStatus: stored.LastAppliedStatus, AccountWeight: cloneIntPointer(stored.LastAppliedWeight),
			}
		}
		currentStatus := normalizeTargetStatus(target.Platform, target.AccountStatus)
		currentWeight := normalizedTargetWeight(target)
		if stored.Conflict || (targetVisible && targetActionCheckpointConflicted(target, &stored, currentStatus, currentWeight)) {
			stored.Conflict = true
			stored.PendingStatus = ""
			stored.PendingWeight = nil
			if err := s.repo.UpsertTargetActionState(ctx, stored); err != nil {
				log.Printf("[connection-health] store unmanaged target conflict failed target_id=%s err=%v", stored.TargetID, err)
			}
			continue
		}
		if targetVisible && targetStateEqual(target, currentStatus, currentWeight, stored.OriginalStatus, stored.OriginalWeight) {
			if err := s.repo.DeleteTargetActionState(ctx, stored.UserID, stored.AdminAccountID, stored.TargetID); err != nil {
				log.Printf("[connection-health] clear restored target action state failed target_id=%s err=%v", stored.TargetID, err)
			}
			continue
		}
		stored.PendingStatus = stored.OriginalStatus
		stored.PendingWeight = cloneIntPointer(stored.OriginalWeight)
		if err := s.repo.UpsertTargetActionState(ctx, stored); err != nil {
			log.Printf("[connection-health] store unmanaged target restore intent failed target_id=%s err=%v", stored.TargetID, err)
			continue
		}
		action, actionErr := s.dispatcher.ApplyTargetState(ctx, inventory.session, target, stored.OriginalWeight, stored.OriginalStatus)
		if actionErr != nil {
			log.Printf("[connection-health] restore unmanaged target failed target_id=%s action=%s err=%v", stored.TargetID, action, actionErr)
			continue
		}
		updateAdminInventoryTargetState(inventory, target.AccountID, stored.OriginalStatus, stored.OriginalWeight)
		if err := s.recordTargetEvent(ctx, stored.UserID, stored.AdminAccountID, target, "", "*", "policy_unmanaged_restore", "", "", nil, "", "", action, EventSourceScheduled); err != nil {
			log.Printf("[connection-health] insert unmanaged target restore event failed target_id=%s err=%v", stored.TargetID, err)
			continue
		}
		if err := s.repo.DeleteTargetActionState(ctx, stored.UserID, stored.AdminAccountID, stored.TargetID); err != nil {
			log.Printf("[connection-health] clear unmanaged target action state failed target_id=%s err=%v", stored.TargetID, err)
		}
	}
}

// restoreEmptySub2APIGroups repairs a group that already reached zero active accounts. It only
// uses target action checkpoints that prove connection health previously changed an originally
// active account to inactive. The shared inventory cache avoids an additional Sub2API read.
func (s *Service) restoreEmptySub2APIGroups(ctx context.Context, states []TargetActionState, inventoryCache adminInventoryCache) {
	type workspace struct {
		userID         string
		adminAccountID string
		states         []TargetActionState
	}
	workspaces := make(map[string]*workspace)
	workspaceOrder := make([]string, 0)
	for _, state := range states {
		key := state.UserID + "|" + state.AdminAccountID
		if workspaces[key] == nil {
			workspaces[key] = &workspace{userID: state.UserID, adminAccountID: state.AdminAccountID}
			workspaceOrder = append(workspaceOrder, key)
		}
		workspaces[key].states = append(workspaces[key].states, state)
	}

	for _, workspaceKey := range workspaceOrder {
		ws := workspaces[workspaceKey]
		inventory, err := s.loadAdminInventory(ctx, ws.userID, ws.adminAccountID, inventoryCache)
		if err != nil {
			log.Printf("[connection-health] restore empty group inventory failed user_id=%s admin_account_id=%s err=%v", ws.userID, ws.adminAccountID, err)
			continue
		}
		if inventory.session.Platform != upstream.PlatformSub2API {
			continue
		}
		inventoryComplete := true
		for _, groupInventory := range inventory.groups {
			if groupInventory.err != nil {
				inventoryComplete = false
				break
			}
		}
		if !inventoryComplete {
			continue
		}

		stateByTarget := make(map[string]TargetActionState, len(ws.states))
		for _, state := range ws.states {
			stateByTarget[state.TargetID] = state
		}
		activeByGroup := make(map[string]map[string]struct{}, len(inventory.groups))
		membershipsByTarget := make(map[string][]string)
		targets := make(map[string]AdminProbeTarget)
		groupsByID := make(map[string]adminInventoryGroup, len(inventory.groups))
		groupOrder := make([]string, 0, len(inventory.groups))
		for _, groupInventory := range inventory.groups {
			groupID := groupInventory.group.ID
			groupsByID[groupID] = groupInventory
			groupOrder = append(groupOrder, groupID)
			activeTargets := make(map[string]struct{})
			for _, account := range groupInventory.accounts {
				targetID := buildTargetID(string(upstream.PlatformSub2API), ws.adminAccountID, account.ID)
				membershipsByTarget[targetID] = append(membershipsByTarget[targetID], groupID)
				if _, exists := targets[targetID]; !exists {
					targets[targetID] = AdminProbeTarget{
						TargetID: targetID, Platform: string(upstream.PlatformSub2API),
						AdminGroupID: groupID, AdminGroupName: groupInventory.group.Name,
						AccountID: account.ID, AccountName: account.Name, AccountStatus: account.Status,
						Schedulable: cloneBoolPointer(account.Schedulable), AccountWeight: cloneIntPointer(account.Weight),
						ProviderFamily: account.Platform, Models: splitModelList(account.Models),
					}
				}
				if targetStatusEnabled(string(upstream.PlatformSub2API), normalizeTargetStatus(string(upstream.PlatformSub2API), account.Status)) {
					activeTargets[targetID] = struct{}{}
				}
			}
			activeByGroup[groupID] = activeTargets
		}

		handledGroups := make(map[string]struct{})
		for _, groupID := range groupOrder {
			if _, handled := handledGroups[groupID]; handled || len(activeByGroup[groupID]) > 0 {
				continue
			}
			groupInventory := groupsByID[groupID]
			type restoreCandidate struct {
				target AdminProbeTarget
				state  TargetActionState
			}
			candidates := make([]restoreCandidate, 0)
			pendingUnknown := false
			for _, account := range groupInventory.accounts {
				targetID := buildTargetID(string(upstream.PlatformSub2API), ws.adminAccountID, account.ID)
				state, exists := stateByTarget[targetID]
				if !exists || state.Conflict ||
					normalizeTargetStatus(string(upstream.PlatformSub2API), state.OriginalStatus) != "active" ||
					normalizeTargetStatus(string(upstream.PlatformSub2API), account.Status) != "inactive" {
					continue
				}
				if state.PendingStatus != "" {
					pendingUnknown = true
					continue
				}
				if normalizeTargetStatus(string(upstream.PlatformSub2API), state.LastAppliedStatus) != "inactive" {
					continue
				}
				target := targets[targetID]
				target.AdminGroupID = groupID
				target.AdminGroupName = groupInventory.group.Name
				candidates = append(candidates, restoreCandidate{target: target, state: state})
			}
			if pendingUnknown || len(candidates) == 0 {
				continue
			}
			sort.Slice(candidates, func(i int, j int) bool {
				if !candidates[i].state.UpdatedAt.Equal(candidates[j].state.UpdatedAt) {
					return candidates[i].state.UpdatedAt.After(candidates[j].state.UpdatedAt)
				}
				return candidates[i].target.TargetID < candidates[j].target.TargetID
			})
			chosen := candidates[0]
			for _, membershipGroupID := range membershipsByTarget[chosen.target.TargetID] {
				handledGroups[membershipGroupID] = struct{}{}
			}

			chosen.state.PendingStatus = chosen.state.OriginalStatus
			chosen.state.PendingWeight = cloneIntPointer(chosen.state.OriginalWeight)
			if err := s.repo.UpsertTargetActionState(ctx, chosen.state); err != nil {
				log.Printf("[connection-health] store empty group restore intent failed target_id=%s group_id=%s err=%v", chosen.target.TargetID, groupID, err)
				continue
			}
			action, actionErr := s.dispatcher.ApplyTargetState(ctx, inventory.session, chosen.target, chosen.state.OriginalWeight, chosen.state.OriginalStatus)
			if actionErr != nil {
				log.Printf("[connection-health] restore empty group target failed target_id=%s group_id=%s action=%s err=%v", chosen.target.TargetID, groupID, action, actionErr)
				continue
			}
			chosen.state.LastAppliedStatus = chosen.state.OriginalStatus
			chosen.state.LastAppliedWeight = cloneIntPointer(chosen.state.OriginalWeight)
			chosen.state.PendingStatus = ""
			chosen.state.PendingWeight = nil
			if err := s.repo.UpsertTargetActionState(ctx, chosen.state); err != nil {
				log.Printf("[connection-health] confirm empty group restore failed target_id=%s group_id=%s err=%v", chosen.target.TargetID, groupID, err)
				continue
			}
			updateAdminInventoryTargetState(inventory, chosen.target.AccountID, chosen.state.OriginalStatus, chosen.state.OriginalWeight)
			for _, membershipGroupID := range membershipsByTarget[chosen.target.TargetID] {
				activeByGroup[membershipGroupID][chosen.target.TargetID] = struct{}{}
			}
			if err := s.recordTargetEvent(ctx, ws.userID, ws.adminAccountID, chosen.target, "", "*", "group_zero_restore", "", "", nil, "", "", action, EventSourceScheduled); err != nil {
				log.Printf("[connection-health] insert empty group restore event failed target_id=%s group_id=%s err=%v", chosen.target.TargetID, groupID, err)
			}
		}
	}
}

func updateAdminInventoryTargetState(inventory *adminWorkspaceInventory, accountID string, status string, weight *int) {
	if inventory == nil {
		return
	}
	for groupIndex := range inventory.groups {
		for accountIndex := range inventory.groups[groupIndex].accounts {
			account := &inventory.groups[groupIndex].accounts[accountIndex]
			if account.ID != accountID {
				continue
			}
			account.Status = status
			account.Weight = cloneIntPointer(weight)
		}
	}
}

func hasRemoteActionModel(specs []probeModelSpec) bool {
	for _, spec := range specs {
		if spec.policy.Enabled && policyRemoteActionEnabled(spec.policy) {
			return true
		}
	}
	return false
}

func legacyTargetWasManaged(states []ConnectionHealthState) bool {
	for _, state := range states {
		switch state.LastRemoteAction {
		case RemoteActionSub2APIStatusInactive, "newapi_channel_disabled":
			return true
		}
	}
	return false
}

func legacyOriginalTargetState(platform string) (string, *int) {
	if platform == string(upstream.PlatformNewAPI) {
		weight := 100
		return "1", &weight
	}
	return "active", nil
}

func aggregateTargetStates(states []ConnectionHealthState) (allHealthy bool, blocked bool, minWeight int) {
	allHealthy = true
	minWeight = 100
	for _, state := range states {
		if state.State != StateHealthy {
			allHealthy = false
		}
		if state.CurrentWeight < minWeight {
			minWeight = state.CurrentWeight
		}
		if state.State == StateSuspended || state.State == StateObserving || state.State == StateDisabled || state.CurrentWeight <= 0 {
			blocked = true
		}
	}
	return allHealthy, blocked, minWeight
}

func hasRecoveringState(states []ConnectionHealthState) bool {
	for _, state := range states {
		if state.State == StateRecovering {
			return true
		}
	}
	return false
}

func desiredTargetState(platform string, allHealthy bool, blocked bool, minWeight int, stored TargetActionState) (string, *int) {
	if allHealthy {
		return stored.OriginalStatus, cloneIntPointer(stored.OriginalWeight)
	}
	if platform == string(upstream.PlatformNewAPI) {
		if blocked {
			weight := 0
			return "2", &weight
		}
		weight := scaledTargetWeight(stored.OriginalWeight, minWeight)
		return "1", &weight
	}
	if blocked {
		return "inactive", nil
	}
	return "active", nil
}

// scaledTargetWeight converts the state machine's 0-100 recovery percentage into the
// channel's real weight. Writing the percentage directly could increase traffic for a
// channel whose original weight was below the current recovery percentage.
func scaledTargetWeight(originalWeight *int, percentage int) int {
	base := 100
	if originalWeight != nil {
		base = maxInt(0, *originalWeight)
	}
	percentage = maxInt(0, minInt(100, percentage))
	if base == 0 || percentage == 0 {
		return 0
	}
	// Round up so a positive original weight receives at least one unit during recovery.
	return (base*percentage + 99) / 100
}

func normalizeTargetStatus(platform string, status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if platform == string(upstream.PlatformNewAPI) {
		// New API 只有状态 1 表示启用；2（手动禁用）以及未来/其它非 1 状态都按禁用保护。
		// 空值仅用于兼容旧上游和测试夹具未返回 status 的情况。
		if normalized == "" || normalized == "1" || normalized == "active" || normalized == "enabled" {
			return "1"
		}
		return "2"
	}
	if normalized == "inactive" || normalized == "disabled" || normalized == "2" {
		return "inactive"
	}
	return "active"
}

func targetStatusEnabled(platform string, status string) bool {
	if platform == string(upstream.PlatformNewAPI) {
		return status == "1"
	}
	return status == "active"
}

func normalizedTargetWeight(target AdminProbeTarget) *int {
	if target.Platform != string(upstream.PlatformNewAPI) {
		return nil
	}
	if target.AccountWeight != nil {
		return cloneIntPointer(target.AccountWeight)
	}
	weight := 100
	return &weight
}

func targetActionConflicted(target AdminProbeTarget, stored TargetActionState, currentStatus string, currentWeight *int) bool {
	if currentStatus != normalizeTargetStatus(target.Platform, stored.LastAppliedStatus) {
		return true
	}
	// 老版本/部分上游列表可能不返回 weight；缺失时只比较状态，不能凭空制造人工冲突。
	if target.Platform == string(upstream.PlatformNewAPI) && target.AccountWeight != nil {
		return !equalIntPointers(currentWeight, stored.LastAppliedWeight)
	}
	return false
}

// targetActionCheckpointConflicted reconciles the two-phase action checkpoint. A current
// value matching Pending means the previous upstream write succeeded but its final database
// acknowledgement did not. A value matching neither Pending nor LastApplied is a real manual
// conflict and must not be overwritten.
func targetActionCheckpointConflicted(target AdminProbeTarget, stored *TargetActionState, currentStatus string, currentWeight *int) bool {
	if stored.PendingStatus == "" {
		return targetActionConflicted(target, *stored, currentStatus, currentWeight)
	}
	if targetStateEqual(target, currentStatus, currentWeight, stored.PendingStatus, stored.PendingWeight) {
		stored.LastAppliedStatus = stored.PendingStatus
		stored.LastAppliedWeight = cloneIntPointer(stored.PendingWeight)
		stored.PendingStatus = ""
		stored.PendingWeight = nil
		return false
	}
	return targetActionConflicted(target, *stored, currentStatus, currentWeight)
}

func targetStateEqual(target AdminProbeTarget, currentStatus string, currentWeight *int, desiredStatus string, desiredWeight *int) bool {
	if currentStatus != normalizeTargetStatus(target.Platform, desiredStatus) {
		return false
	}
	if target.Platform == string(upstream.PlatformNewAPI) && target.AccountWeight != nil {
		return equalIntPointers(currentWeight, desiredWeight)
	}
	return target.Platform != string(upstream.PlatformNewAPI) || target.AccountWeight == nil
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalIntPointers(left *int, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

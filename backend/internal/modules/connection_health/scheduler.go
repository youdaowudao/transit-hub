package connection_health

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	schedulerTickInterval = 30 * time.Second
	maxJobsPerTick        = 100
)

// adminProbeJob 是调度器一轮扫描出的、针对一个独立探活目标的到期任务集合。
// 一个目标下可能有多个到期模型，共用一次凭据解析（避免重复命中受保护的 key 接口）。
type adminProbeJob struct {
	userID          string
	adminAccountID  string
	session         upstream.Session
	target          AdminProbeTarget
	account         upstream.AdminGroupAccountInfo
	models          []probeModelSpec
	dueSpecs        []probeModelSpec
	floorGuard      *workspaceFloorGuard
	monitoringScope adminMonitoringScope
}

type probePolicyEventGroup struct {
	resolved       bool
	adminGroupID   string
	adminGroupName string
}

type adminInventoryGroup struct {
	group    upstream.AdminGroupInfo
	accounts []upstream.AdminGroupAccountInfo
	err      error
}

type adminWorkspaceInventory struct {
	snapshotStartedAt time.Time
	groupsComplete    bool
	session           upstream.Session
	groups            []adminInventoryGroup
}

type adminInventoryCacheEntry struct {
	inventory *adminWorkspaceInventory
	err       error
}

type adminInventoryCache map[string]adminInventoryCacheEntry

func (s *Service) fetchAdminAllGroups(ctx context.Context, session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	if reader, ok := s.platformGroups.(PlatformGroupContextReader); ok {
		return reader.FetchAdminAllGroupsContext(ctx, session)
	}
	return s.platformGroups.FetchAdminAllGroups(session)
}

func (s *Service) listAdminGroupAccounts(ctx context.Context, session upstream.Session, group upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	if reader, ok := s.platformGroups.(PlatformGroupContextReader); ok {
		return reader.ListAdminGroupAccountsContext(ctx, session, group)
	}
	return s.platformGroups.ListAdminGroupAccounts(session, group)
}

func (s *Service) resolveProbeCredential(ctx context.Context, session upstream.Session, account upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	if reader, ok := s.platformGroups.(PlatformProbeCredentialContextReader); ok {
		return reader.ResolveProbeCredentialContext(ctx, session, account)
	}
	return s.platformGroups.ResolveProbeCredential(session, account)
}

func (s *Service) loadAdminInventory(ctx context.Context, userID string, adminAccountID string, cache adminInventoryCache) (*adminWorkspaceInventory, error) {
	key := userID + "|" + adminAccountID
	if s.mySites == nil || s.platformGroups == nil {
		return nil, requestError(ErrorSub2APIInventoryIncomplete)
	}
	if cached, ok := cache[key]; ok {
		return cached.inventory, cached.err
	}
	session, err := s.mySites.RequireSession(ctx, userID, adminAccountID)
	if err != nil {
		cache[key] = adminInventoryCacheEntry{err: err}
		return nil, err
	}
	snapshotStartedAt := time.Now()
	groups, err := s.fetchAdminAllGroups(ctx, session)
	if err != nil {
		cache[key] = adminInventoryCacheEntry{err: err}
		return nil, err
	}
	inventory := &adminWorkspaceInventory{snapshotStartedAt: snapshotStartedAt, groupsComplete: true, session: session, groups: make([]adminInventoryGroup, 0, len(groups))}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		accounts, accountsErr := s.listAdminGroupAccounts(ctx, session, group)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inventory.groups = append(inventory.groups, adminInventoryGroup{group: group, accounts: accounts, err: accountsErr})
	}
	guardAdminInventoryTimes(inventory)
	recordSchedulerInventory(ctx, userID, adminAccountID, *inventory)
	s.rememberActionInventory(userID, adminAccountID, inventory)
	cache[key] = adminInventoryCacheEntry{inventory: inventory}
	return inventory, nil
}

// StartScheduler 启动后台探活调度：立即跑一次，之后每 30s 一次。tick 和每个探活 goroutine
// 都有独立的 panic recover，任意一次探活失败或 panic 都不能影响调度器持续运行。
func (s *Service) StartScheduler(ctx context.Context) {
	if s.backgroundTasksDisabled {
		return
	}
	s.startEventRetention(ctx)
	go func() {
		ticker := time.NewTicker(schedulerTickInterval)
		defer ticker.Stop()
		// Keep the 30s clock anchored to scheduler startup. If the immediate
		// round takes longer, its due tick is consumed after that round finishes.
		s.runSchedulerTickSafely(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runSchedulerTickSafely(ctx)
			}
		}
	}()
}

func (s *Service) runSchedulerTickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[connection-health] scheduler tick panic recovered: %v", r)
		}
	}()
	release, acquired, err := s.repo.TryAcquireSchedulerLease(ctx)
	if err != nil {
		log.Printf("[connection-health] acquire scheduler lease failed: %v", err)
		return
	}
	if !acquired {
		return
	}
	defer release()
	s.runSchedulerTick(ctx)
}

// runSchedulerTick 扫描全部已启用策略、旧版 target 分配和新版 admin 分组分配，按 workspace
// 生成独立探活目标。分组新增的账号/渠道会在下一轮扫描时自动继承，无需写入额外 target 行。
func (s *Service) runSchedulerTick(ctx context.Context) {
	ctx, round := s.beginSchedulerRound(ctx)
	defer s.finishSchedulerRound(round)
	if s.platformGroups == nil {
		return
	}
	var err error
	ctx, err = s.captureAllWorkspaceRules(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler capture workspace rules failed: %v", err)
		return
	}
	ctx = withAccountTierDecisionCache(ctx)
	policies, err := s.repo.ListEnabledPolicies(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list policies failed: %v", err)
		return
	}
	assignments, err := s.repo.ListAllPolicyAssignments(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list policy assignments failed: %v", err)
		return
	}
	groupAssignments, err := s.repo.ListAllGroupPolicyAssignments(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list group policy assignments failed: %v", err)
		return
	}
	exclusions, err := s.repo.ListAllGroupTargetExclusions(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list group target exclusions failed: %v", err)
		return
	}
	priorityStates, err := s.repo.ListAllPrioritySyncStates(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list priority sync states failed: %v", err)
		return
	}
	workspaceSyncStates, err := s.repo.ListAllPriorityWorkspaceSyncStates(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list workspace priority sync states failed: %v", err)
		return
	}
	for _, state := range workspaceSyncStates {
		if state.PendingSignature != "" {
			priorityStates = append(priorityStates, PrioritySyncState{UserID: state.UserID, AdminAccountID: state.AdminAccountID})
		}
	}
	targetActionStates, err := s.repo.ListAllTargetActionStates(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler list target action states failed: %v", err)
		return
	}
	if len(assignments) == 0 && len(groupAssignments) == 0 && len(priorityStates) == 0 && len(targetActionStates) == 0 {
		// 没有任何显式或分组分配：不解析凭据、不探活、不修改优先级。
		return
	}

	round.policies, round.targets, round.groups, round.exclusions = policies, assignments, groupAssignments, exclusions
	capReady := make(map[string]bool)
	for _, policy := range policies {
		key := policy.UserID + "|" + policy.AdminAccountID
		if _, read := capReady[key]; read {
			continue
		}
		capReady[key] = s.loadWorkspaceProbeCap(ctx, policy.UserID, policy.AdminAccountID) == nil
	}
	// 优先级同步和探活使用同一份有效策略关系。优先级写入失败只记录日志，不阻断探活。
	inventoryCache := make(adminInventoryCache)
	s.syncMultiplierPrioritiesWithCache(ctx, policies, assignments, groupAssignments, exclusions, priorityStates, inventoryCache)
	s.restoreUnmanagedTargetActions(ctx, policies, assignments, groupAssignments, exclusions, targetActionStates, inventoryCache)
	s.startDeletedAccountSweeps(ctx, inventoryCache)
	if len(policies) == 0 {
		return
	}
	remainingTargetActionStates, err := s.repo.ListAllTargetActionStates(ctx)
	if err != nil {
		log.Printf("[connection-health] scheduler refresh target action states failed: %v", err)
		return
	}
	s.restoreEmptySub2APIGroups(ctx, remainingTargetActionStates, inventoryCache)
	jobs := s.collectAdminProbeJobsWithGroupsAndCache(ctx, policies, assignments, groupAssignments, exclusions, inventoryCache)
	if len(jobs) == 0 {
		return
	}

	var wg sync.WaitGroup
	type probedWorkspace struct {
		userID         string
		adminAccountID string
	}
	probedWorkspaces := make([]probedWorkspace, 0)
	seenWorkspaces := make(map[string]struct{})
	floorGuards := make(map[string]*workspaceFloorGuard)

	for _, j := range jobs {
		wsKey := j.userID + "|" + j.adminAccountID
		if !capReady[wsKey] {
			continue
		}
		releaseSlot, acquired := s.sharedProbeLimiter().acquireAutomatic(ctx, wsKey)
		if !acquired {
			break
		}
		if _, seen := seenWorkspaces[wsKey]; !seen {
			seenWorkspaces[wsKey] = struct{}{}
			probedWorkspaces = append(probedWorkspaces, probedWorkspace{userID: j.userID, adminAccountID: j.adminAccountID})
		}
		if floorGuards[wsKey] == nil {
			floorGuards[wsKey] = newWorkspaceFloorGuard()
			s.setSub2APIFloorGuard(j.userID, j.adminAccountID, floorGuards[wsKey])
		}
		j.floorGuard = floorGuards[wsKey]

		wg.Add(1)
		go s.runAdminProbeJob(ctx, j, releaseSlot, &wg)
	}
	wg.Wait()
	// 探活会在本轮改变健康档位和延迟排序。所有已启动任务完成后复用正式手动探活的
	// workspace 同步入口，让生产 priority 在同一轮反映最新状态，而不是等待下一次 tick。
	for _, workspace := range probedWorkspaces {
		s.syncCurrentWorkspacePriorities(ctx, workspace.userID, workspace.adminAccountID)
	}
}

// runAdminProbeJob 处理单个目标的到期任务：先解析一次凭据；凭据不可用时对每个到期模型记录
// 一次「不可探活」事件并回填 last_probe_at 退避（不驱动状态机、不计入探活预算），
// 凭据可用时逐个模型执行独立探活。
func (s *Service) runAdminProbeJob(ctx context.Context, j adminProbeJob, releaseSlot func(), wg *sync.WaitGroup) {
	defer wg.Done()
	defer releaseSlot()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[connection-health] admin probe goroutine panic recovered target_id=%s: %v", j.target.TargetID, r)
		}
	}()
	ctx, release, acquired, err := s.acquireActionTargetLease(ctx, j.target.TargetID, true)
	if err != nil || !acquired {
		log.Printf("[connection-health] acquire target lease failed target_id=%s err=%v", j.target.TargetID, err)
		return
	}
	defer release()
	refresh, refreshErr := s.refreshAdminTarget(ctx, j.session, j.adminAccountID, j.target.AccountID)
	if refreshErr != nil || refresh.accountsReadError || !refresh.found || refresh.target.TargetID != j.target.TargetID {
		log.Printf("[connection-health] refresh scheduled target failed target_id=%s found=%t partial=%t err=%v", j.target.TargetID, refresh.found, refresh.accountsReadError, refreshErr)
		return
	}
	if err := s.configureTestTarget(ctx, j.userID, j.adminAccountID, &refresh.target, refresh.memberships, !refresh.accountsReadError); err != nil {
		return
	}
	j.target = refresh.target
	j.account = refresh.account
	j.floorGuard.rememberInventory(refresh.inventory)
	monitoringScope, scopeErr := s.loadAdminMonitoringScope(ctx, j.userID, j.adminAccountID, refresh.inventory)
	if scopeErr != nil || !adminMonitoringScopeContainsTarget(monitoringScope, j.target.TargetID) {
		log.Printf("[connection-health] refresh scheduled monitoring scope blocked target_id=%s in_scope=%t err=%v", j.target.TargetID, adminMonitoringScopeContainsTarget(monitoringScope, j.target.TargetID), scopeErr)
		return
	}
	j.monitoringScope = monitoringScope
	currentSpecs, queuedSpecs, policyOK := s.currentScheduledProbeSpecs(ctx, j.userID, j.adminAccountID, j.target, refresh.memberships, j.dueSpecs)
	if !policyOK {
		log.Printf("[connection-health] refresh scheduled policy decision failed target_id=%s", j.target.TargetID)
		return
	}
	j.models = currentSpecs
	if len(currentSpecs) > 0 {
		j.target.ConfigGeneration = currentSpecs[0].policy.ConfigGeneration
		j.target.ConfigGenerationKnown = currentSpecs[0].policy.RuleVersion != ""
	}
	j.dueSpecs = s.recheckAdminProbeSpecs(ctx, j.userID, j.adminAccountID, j.target, queuedSpecs, time.Now().UTC())
	if len(j.dueSpecs) == 0 {
		return
	}

	cred, err := s.platformGroups.ResolveProbeCredential(j.session, j.account)
	if err != nil {
		reason := upstream.ProbeCredentialReason(err)
		s.recordTargetCredentialUnavailable(ctx, j.userID, j.adminAccountID, j.target, j.dueSpecs, reason)
		return
	}
	results := make([]targetProbeResult, 0, len(j.dueSpecs))
	for _, spec := range j.dueSpecs {
		result, err := s.probeTargetOnce(ctx, j.userID, j.adminAccountID, j.target, cred, spec, true)
		if err != nil {
			log.Printf("[connection-health] scheduled target probe failed target_id=%s model=%s err=%v", j.target.TargetID, spec.modelName, err)
			continue
		}
		if result != nil {
			results = append(results, *result)
		}
	}
	if err := s.finishTargetProbeBatchWithFloor(ctx, j.userID, j.adminAccountID, j.session, j.target, j.models, results, EventSourceScheduled, j.floorGuard, &refresh.inventory, j.monitoringScope); err != nil {
		log.Printf("[connection-health] finish scheduled target probe failed target_id=%s err=%v", j.target.TargetID, err)
	}
}

// recheckAdminProbeSpecs closes the gap between scheduler collection and job execution:
// the target lease protects in-process user actions, while this decision pass consumes the
// fresh schedulable value, current state and budget owner immediately before any probe request.
func (s *Service) recheckAdminProbeSpecs(ctx context.Context, userID string, adminAccountID string, target AdminProbeTarget, specs []probeModelSpec, now time.Time) []probeModelSpec {
	if len(specs) == 0 {
		return nil
	}
	due := make([]probeModelSpec, 0, len(specs))
	for _, spec := range specs {
		policies := spec.policies
		if len(policies) == 0 {
			policies = []Policy{spec.policy}
		}
		decision, stateOK := s.effectiveProbeDecisionForSpec(ctx, target, spec, policies, now, nil)
		if !stateOK || !decision.ContinueAutoProbe || decision.NextProbeAt == nil || now.Before(*decision.NextProbeAt) {
			continue
		}
		if state, err := s.repo.GetState(ctx, target.TargetID, spec.modelName); err == nil && state != nil {
			spec.recheckPending = state.RecheckPending
		}
		spec.policies = policies
		due = append(due, spec)
	}
	jobs := s.selectAdminProbeJobsWithBudget(ctx, []adminProbeJob{{userID: userID, adminAccountID: adminAccountID, target: target, models: specs, dueSpecs: due}}, now, len(specs))
	if len(jobs) == 0 {
		return nil
	}
	return jobs[0].dueSpecs
}

func (s *Service) currentScheduledProbeSpecs(ctx context.Context, userID string, adminAccountID string, target AdminProbeTarget, memberships []adminTargetMembership, queued []probeModelSpec) ([]probeModelSpec, []probeModelSpec, bool) {
	policies, err := s.repo.ListPolicies(ctx, userID, adminAccountID)
	if err != nil {
		return nil, nil, false
	}
	directAssignments, err := s.repo.ListPolicyAssignmentsForTarget(ctx, userID, adminAccountID, target.TargetID)
	if err != nil {
		return nil, nil, false
	}
	groupAssignments, err := s.repo.ListGroupPolicyAssignmentsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return nil, nil, false
	}
	exclusions, err := s.repo.ListGroupTargetExclusionsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return nil, nil, false
	}

	policyByID := make(map[string]Policy, len(policies))
	for _, policy := range policies {
		policyByID[policy.ID] = policy
	}
	policySources := make(map[string]probePolicyEventGroup)
	directPolicies := make([]Policy, 0, len(directAssignments))
	for _, assignment := range directAssignments {
		policy, exists := policyByID[assignment.PolicyID]
		if !exists {
			continue
		}
		directPolicies = append(directPolicies, policy)
	}
	inheritedPolicies := make([]Policy, 0)
	excluded := groupTargetExclusionIndex(exclusions)[userID+"|"+adminAccountID]
	for _, membership := range memberships {
		if excluded[membership.groupID][target.TargetID] {
			continue
		}
		for _, assignment := range groupAssignments {
			if assignment.AdminGroupID != membership.groupID {
				continue
			}
			policy, exists := policyByID[assignment.PolicyID]
			if !exists {
				continue
			}
			inheritedPolicies = append(inheritedPolicies, policy)
		}
	}
	effectivePolicies := effectivePoliciesForTarget(directPolicies, inheritedPolicies)
	if hasEnabledTargetPolicy(directPolicies) {
		for _, policy := range effectivePolicies {
			policySources[policy.ID] = probePolicyEventGroup{resolved: true}
		}
	} else {
		for _, policy := range effectivePolicies {
			for _, membership := range memberships {
				if excluded[membership.groupID][target.TargetID] {
					continue
				}
				for _, assignment := range groupAssignments {
					if assignment.AdminGroupID == membership.groupID && assignment.PolicyID == policy.ID {
						policySources[policy.ID] = probePolicyEventGroup{
							resolved: true, adminGroupID: membership.groupID, adminGroupName: membership.groupName,
						}
					}
				}
			}
		}
	}

	allSpecs := candidateModelSpecsForPlatform(target.Models, effectivePolicies, target.Platform)
	for index := range allSpecs {
		allSpecs[index].policySources = policySources
		if source, exists := policySources[allSpecs[index].policy.ID]; exists {
			allSpecs[index].eventGroupResolved = source.resolved
			allSpecs[index].eventAdminGroupID = source.adminGroupID
			allSpecs[index].eventAdminGroupName = source.adminGroupName
		}
	}
	currentByModel := make(map[string]probeModelSpec, len(allSpecs))
	for _, spec := range allSpecs {
		currentByModel[spec.modelName] = spec
	}
	queuedNow := make([]probeModelSpec, 0, len(allSpecs))
	for _, queuedSpec := range queued {
		if spec, exists := currentByModel[queuedSpec.modelName]; exists {
			queuedNow = append(queuedNow, spec)
		}
	}
	return allSpecs, queuedNow, true
}

// recordTargetCredentialUnavailable 在凭据解析失败时，对每个到期模型记录独立重试时间（按探活
// 间隔退避，避免每 30s 反复命中受保护的 key/导出接口）并记录一条 unsupported 事件，
// 事件 error_key 为脱敏 reason。不驱动状态机、不计入探活预算。
func (s *Service) recordTargetCredentialUnavailable(ctx context.Context, userID string, adminAccountID string, target AdminProbeTarget, specs []probeModelSpec, reason string) {
	now := time.Now()
	for _, spec := range specs {
		initial := defaultTargetState(userID, adminAccountID, target, spec.modelName)
		initial.RuleVersion = spec.policy.RuleVersion
		if target.Platform != string(upstream.PlatformSub2API) || initial.RuleVersion == "" {
			initial.RuleVersion = RuleVersionLegacy
		}
		// Credential preparation is a separate retry diagnostic. The repository
		// updates only that diagnostic, without replaying a captured health row.
		next, err := s.repo.RecordTargetCredentialFailure(ctx, initial, reason, now)
		if err != nil {
			log.Printf("[connection-health] upsert unavailable target state failed target_id=%s model=%s err=%v", target.TargetID, spec.modelName, err)
			continue
		}
		eventTarget := targetForProbeSpec(target, spec)
		ruleVersion := next.RuleVersion
		if ruleVersion == "" {
			ruleVersion = initial.RuleVersion
		}
		metadata := targetProbeEventMetadata{RuleVersion: ruleVersion, LongFailure: stateHasLongFailure(next, spec.policy, now)}
		if err := s.recordTargetEvent(ctx, userID, adminAccountID, eventTarget, spec.effectiveBudgetPolicy().ID, spec.modelName, string(ResultUnsupported), string(next.State), string(next.State), nil, reason, "", "", EventSourceScheduled, metadata); err != nil {
			log.Printf("[connection-health] insert unavailable target event failed target_id=%s model=%s err=%v", target.TargetID, spec.modelName, err)
		}
	}
}

// collectAdminProbeJobs 按 workspace 生成独立探活目标，并挑出到期的 (target, model) 任务。
// 调度器用 context.Background() 启动，没有请求态「当前 workspace」，必须用策略自带的
// userID + adminAccountID 复合键读取会话与分组，缓存也用复合键，避免多 workspace 串台。
//
// assignments 是全部 workspace 的「target 显式分配策略」关系：只有分配了至少一条已启用策略的
// target 才会被本函数处理；未分配的 target 不解析凭据、不计入 dueSpecs、不生成任何 job。
func (s *Service) collectAdminProbeJobs(ctx context.Context, policies []Policy, assignments []PolicyAssignment) []adminProbeJob {
	return s.collectAdminProbeJobsWithGroups(ctx, policies, assignments, nil, nil)
}

func (s *Service) collectAdminProbeJobsWithGroups(ctx context.Context, policies []Policy, assignments []PolicyAssignment, groupAssignments []GroupPolicyAssignment, exclusions []GroupTargetExclusion) []adminProbeJob {
	return s.collectAdminProbeJobsWithGroupsAndCache(ctx, policies, assignments, groupAssignments, exclusions, make(adminInventoryCache))
}

func (s *Service) collectAdminProbeJobsWithGroupsAndCache(ctx context.Context, policies []Policy, assignments []PolicyAssignment, groupAssignments []GroupPolicyAssignment, exclusions []GroupTargetExclusion, inventoryCache adminInventoryCache) []adminProbeJob {
	// 按 workspace 归拢策略。
	type workspace struct {
		userID         string
		adminAccountID string
		policies       []Policy
	}
	order := make([]string, 0)
	byWorkspace := make(map[string]*workspace)
	for _, p := range policies {
		key := p.UserID + "|" + p.AdminAccountID
		ws, ok := byWorkspace[key]
		if !ok {
			ws = &workspace{userID: p.UserID, adminAccountID: p.AdminAccountID}
			byWorkspace[key] = ws
			order = append(order, key)
		}
		ws.policies = append(ws.policies, p)
	}

	// assignedByWorkspace: wsKey -> targetId -> 该 target 已分配且已启用的策略列表。
	// 分配指向的策略如果已被禁用/删除（不在 policies/policyByID 中），对应分配行会被忽略，
	// 相当于该 target 暂时没有生效的分配。
	assignedByWorkspace := assignedEnabledPoliciesByTarget(policies, assignments)
	assignedGroupsByWorkspace := assignedEnabledPoliciesByGroup(policies, groupAssignments)
	excludedByWorkspace := groupTargetExclusionIndex(exclusions)

	jobs := make([]adminProbeJob, 0, maxJobsPerTick)
	now := time.Now()

	for _, key := range order {
		ws := byWorkspace[key]
		// 该 workspace 下没有任何 target 被分配过策略：直接跳过，不建 session、不拉分组/账号，
		// 避免为完全没有分配关系的 workspace 发起任何上游调用。
		assignedTargets := assignedByWorkspace[key]
		assignedGroups := assignedGroupsByWorkspace[key]
		if len(assignedTargets) == 0 && len(assignedGroups) == 0 {
			continue
		}
		// 若该 workspace 的策略没有任何启用的模型目标，直接跳过，避免无谓地拉取分组/账号。
		if !hasEnabledModelTarget(ws.policies) {
			continue
		}
		inventory, err := s.loadAdminInventory(ctx, ws.userID, ws.adminAccountID, inventoryCache)
		if err != nil {
			log.Printf("[connection-health] scheduler load admin inventory failed user_id=%s admin_account_id=%s err=%v", ws.userID, ws.adminAccountID, err)
			continue
		}
		if !adminInventoryComplete(*inventory) {
			continue
		}
		testConfigs, configErr := s.repo.ListGroupTestConfigurations(ctx, ws.userID, ws.adminAccountID)
		if configErr != nil {
			continue
		}
		session := inventory.session
		platform := string(session.Platform)
		monitoringScope, scopeErr := buildAdminMonitoringScope(
			ws.userID, ws.adminAccountID, *inventory, policies, assignments, groupAssignments, exclusions,
		)
		if scopeErr != nil {
			log.Printf("[connection-health] scheduler build monitoring scope failed user_id=%s admin_account_id=%s err=%v", ws.userID, ws.adminAccountID, scopeErr)
			continue
		}

		// 账号/渠道可能同时属于多个 admin 分组。先按稳定 targetId 合并所有来源策略，再生成
		// 一次任务，避免同一目标在一轮中被重复探活。
		type targetCandidate struct {
			target        AdminProbeTarget
			account       upstream.AdminGroupAccountInfo
			policies      []Policy
			policySources map[string]probePolicyEventGroup
		}
		candidates := make(map[string]*targetCandidate)
		targetOrder := make([]string, 0)
		for _, groupInventory := range inventory.groups {
			group := groupInventory.group
			if groupInventory.err != nil {
				log.Printf("[connection-health] scheduler list accounts failed group_id=%s err=%v", group.ID, groupInventory.err)
				continue
			}
			for _, acc := range groupInventory.accounts {
				if accountHardExcludedFromAdminMonitoring(platform, acc) {
					continue
				}
				target := AdminProbeTarget{
					TargetID:       buildTargetID(platform, ws.adminAccountID, acc.ID),
					Platform:       platform,
					AdminGroupID:   group.ID,
					AdminGroupName: group.Name,
					AccountID:      acc.ID,
					AccountName:    acc.Name,
					AccountStatus:  acc.Status,
					Schedulable:    cloneBoolPointer(acc.Schedulable),
					AccountWeight:  cloneIntPointer(acc.Weight),
					ProviderFamily: acc.Platform,
					Models:         splitModelList(acc.Models),
				}
				inheritedPolicies := assignedGroups[group.ID]
				if excludedByWorkspace[key][group.ID][target.TargetID] {
					inheritedPolicies = nil
				}
				effectivePolicies := effectivePoliciesForTarget(assignedTargets[target.TargetID], inheritedPolicies)
				if len(effectivePolicies) == 0 {
					continue
				}
				candidate, exists := candidates[target.TargetID]
				if !exists {
					candidate = &targetCandidate{
						target: target, account: acc, policySources: make(map[string]probePolicyEventGroup),
					}
					candidates[target.TargetID] = candidate
					targetOrder = append(targetOrder, target.TargetID)
				}
				if hasEnabledTargetPolicy(assignedTargets[target.TargetID]) {
					for _, policy := range effectivePolicies {
						// An explicit target assignment has no single group owner, even when the
						// target is currently being enumerated through a group membership.
						candidate.policySources[policy.ID] = probePolicyEventGroup{resolved: true}
					}
				} else {
					for _, policy := range inheritedPolicies {
						if _, alreadyResolved := candidate.policySources[policy.ID]; alreadyResolved {
							continue
						}
						candidate.policySources[policy.ID] = probePolicyEventGroup{
							resolved: true, adminGroupID: group.ID, adminGroupName: group.Name,
						}
					}
				}
				candidate.policies = mergePoliciesByID(candidate.policies, effectivePolicies)
			}
		}

		for _, targetID := range targetOrder {
			candidate := candidates[targetID]
			candidate.target.InventoryComplete = adminInventoryComplete(*inventory)
			candidate.target.TestMemberships = inventoryTestMemberships(*inventory, candidate.target.AccountID)
			candidate.target.TestConfiguration = ResolveGroupTestConfiguration(candidate.target.Platform, candidate.target.TestMemberships, candidate.target.InventoryComplete, testConfigs)
			if !candidate.target.TestConfiguration.usable() {
				continue
			}
			specs := candidateModelSpecsForPlatform(candidate.target.Models, candidate.policies, candidate.target.Platform)
			for index := range specs {
				specs[index].policySources = candidate.policySources
				if source, exists := candidate.policySources[specs[index].policy.ID]; exists {
					specs[index].eventGroupResolved = source.resolved
					specs[index].eventAdminGroupID = source.adminGroupID
					specs[index].eventAdminGroupName = source.adminGroupName
				}
			}
			available, _ := targetProbeAvailability(platform, candidate.account.BaseURL, len(specs))
			if !available {
				continue
			}
			dueSpecs := make([]probeModelSpec, 0, len(specs))
			for _, spec := range specs {
				decision, stateOK := s.effectiveProbeDecisionForSpec(ctx, candidate.target, spec, spec.policies, now, nil)
				if !stateOK || !decision.ContinueAutoProbe || decision.NextProbeAt == nil || now.Before(*decision.NextProbeAt) {
					continue
				}
				if state, stateErr := s.repo.GetState(ctx, candidate.target.TargetID, spec.modelName); stateErr == nil && state != nil {
					spec.recheckPending = state.RecheckPending
				}
				dueSpecs = append(dueSpecs, spec)
			}
			if len(dueSpecs) > 0 {
				jobs = append(jobs, adminProbeJob{
					userID: ws.userID, adminAccountID: ws.adminAccountID, session: session,
					target: candidate.target, account: candidate.account, models: specs, dueSpecs: dueSpecs,
					monitoringScope: monitoringScope,
				})
			}
		}
	}
	return s.selectAdminProbeJobsWithBudget(ctx, jobs, now, maxJobsPerTick)
}

// hasEnabledModelTarget 判断一组策略里是否存在至少一个启用策略下的启用模型目标。
func hasEnabledModelTarget(policies []Policy) bool {
	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		for _, t := range p.ModelTargets {
			if t.Enabled {
				return true
			}
		}
	}
	return false
}

// isDue 判断某个 (targetId, model) 组合当前是否到期需要探活。
// 从未探活过立即探活；disabled 状态永不自动探活；cooldown_until 未到不探活；
// 探活间隔在策略配置的基础上，按连续失败次数叠加 2/5/10 分钟退避。
func (s *Service) isDue(ctx context.Context, targetID string, modelName string, policy Policy, now time.Time) bool {
	decision, ok := s.effectiveProbeDecision(ctx, targetID, modelName, []Policy{policy}, nil, now)
	return ok && decision.ContinueAutoProbe && decision.NextProbeAt != nil && !now.Before(*decision.NextProbeAt)
}

func (s *Service) effectiveProbeDecision(ctx context.Context, targetID string, modelName string, policies []Policy, schedulable *bool, now time.Time) (EffectiveProbeDecision, bool) {
	return s.effectiveProbeDecisionWithBudgets(ctx, targetID, modelName, policies, schedulable, now, nil)
}

func (s *Service) effectiveProbeDecisionWithBudgets(ctx context.Context, targetID string, modelName string, policies []Policy, schedulable *bool, now time.Time, budgetUsage map[string]int) (EffectiveProbeDecision, bool) {
	state, err := s.repo.GetState(ctx, targetID, modelName)
	if err != nil {
		log.Printf("[connection-health] get state failed target_id=%s model=%s err=%v", targetID, modelName, err)
		return EffectiveProbeDecision{}, false
	}
	return calculateEffectiveProbeDecisionWithBudgets(policies, schedulable, state, now, budgetUsage), true
}

func (s *Service) effectiveProbeDecisionForSpec(ctx context.Context, target AdminProbeTarget, spec probeModelSpec, policies []Policy, now time.Time, budgetUsage map[string]int) (EffectiveProbeDecision, bool) {
	state, err := s.repo.GetState(ctx, target.TargetID, spec.modelName)
	if err != nil {
		log.Printf("[connection-health] get state failed target_id=%s model=%s err=%v", target.TargetID, spec.modelName, err)
		return EffectiveProbeDecision{}, false
	}
	reuseProbeInterval := probeDecisionCanReuseInterval(state, probeDecisionKey(target, spec))
	decisionState := probeDecisionStateForProtocol(state, target.TestConfiguration.Protocol)
	return calculateEffectiveProbeDecisionWithBudgetAndReuse(policies, target.Schedulable, decisionState, now, budgetUsage, reuseProbeInterval), true
}

func policyWithID(policies []Policy, id string) (Policy, bool) {
	for _, policy := range policies {
		if policy.ID == id {
			return policy, true
		}
	}
	return Policy{}, false
}

// Limit individual model tasks before grouping by account. This prevents a large
// account batch from hiding the single immediate recheck at its end.
type adminProbeModelTask struct {
	job  int
	spec probeModelSpec
}

func orderedAdminProbeTasks(jobs []adminProbeJob) []adminProbeModelTask {
	tasks := []adminProbeModelTask{}
	for index, job := range jobs {
		for _, spec := range job.dueSpecs {
			tasks = append(tasks, adminProbeModelTask{index, spec})
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].spec.recheckPending && !tasks[j].spec.recheckPending })
	return tasks
}

func prioritizeAndLimitProbeJobs(jobs []adminProbeJob, limit int) []adminProbeJob {
	tasks := orderedAdminProbeTasks(jobs)
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return groupAdminProbeTasks(jobs, tasks)
}

// Estimate budgets only after pending tasks have won their stable position.
// These increments remain in memory; probeTargetOnce makes the real claim.
func (s *Service) selectAdminProbeJobsWithBudget(ctx context.Context, jobs []adminProbeJob, now time.Time, limit int) []adminProbeJob {
	budgetUsage := map[string]int{}
	budgetLoaded := map[string]bool{}
	selected := []adminProbeModelTask{}
	for _, task := range orderedAdminProbeTasks(jobs) {
		if len(selected) >= limit {
			break
		}
		job, spec := jobs[task.job], task.spec
		usage := map[string]int{}
		ready := true
		for _, policy := range spec.policies {
			continueProbe, _ := policyProbeCadence(policy, job.target.Schedulable)
			if !continueProbe {
				continue
			}
			key := job.userID + "|" + job.adminAccountID + "|" + policy.ID
			if !budgetLoaded[key] {
				count, err := s.repo.CountProbesToday(ctx, job.userID, job.adminAccountID, policy.ID, probeBudgetDayStart(now))
				if err != nil {
					log.Printf("[connection-health] count policy probe budget failed policy_id=%s err=%v", policy.ID, err)
					ready = false
					break
				}
				budgetUsage[key], budgetLoaded[key] = count, true
			}
			usage[policy.ID] = budgetUsage[key]
		}
		if !ready {
			continue
		}
		decision, ok := s.effectiveProbeDecisionForSpec(ctx, job.target, spec, spec.policies, now, usage)
		if !ok || !decision.ContinueAutoProbe || decision.NextProbeAt == nil || now.Before(*decision.NextProbeAt) {
			continue
		}
		policy, found := policyWithID(spec.policies, decision.BudgetPolicyID)
		if !found {
			continue
		}
		task.spec.budgetPolicy = policy
		selected = append(selected, task)
		budgetUsage[job.userID+"|"+job.adminAccountID+"|"+policy.ID]++
	}
	return groupAdminProbeTasks(jobs, selected)
}

func groupAdminProbeTasks(jobs []adminProbeJob, tasks []adminProbeModelTask) []adminProbeJob {
	result := []adminProbeJob{}
	group := map[int]int{}
	for _, task := range tasks {
		index, exists := group[task.job]
		if !exists {
			index = len(result)
			group[task.job] = index
			copy := jobs[task.job]
			copy.dueSpecs = nil
			result = append(result, copy)
		}
		result[index].dueSpecs = append(result[index].dueSpecs, task.spec)
	}
	return result
}

export type TestProtocol = 'chat_completions' | 'responses'
export interface GroupTestConfiguration {
  protocol: TestProtocol
  probeTimeoutSeconds: number
}
export interface TestConfigurationSource extends GroupTestConfiguration {
  adminGroupId: string
  adminGroupName: string
}
export interface EffectiveTestConfiguration {
  protocol?: TestProtocol
  probeTimeoutSeconds?: number
  status: 'default' | 'inherited' | 'conflict' | 'unavailable'
  sourceGroups: TestConfigurationSource[]
  blockedReason?: string
}
export interface AdminGroupTestConfiguration {
  adminGroupId: string
  adminGroupName: string
  configuration: GroupTestConfiguration | null
  inventoryComplete: boolean
  affectedAccountCount: number
  conflictAccountCount: number
  accounts: Array<{ targetId: string; accountName: string; testConfiguration: EffectiveTestConfiguration }>
}
export interface CurrentHealthResult {
  protocol?: TestProtocol
  status: 'failure' | 'success' | 'unverified'
  at?: string | null
  errorKey?: string
  errorDetail?: string
}
export interface TestAttempt {
  at?: string | null
  protocol?: TestProtocol | null
  probeTimeoutSeconds?: number | null
  disposition?: 'applied' | 'invalid' | 'stale'
  result?: string
  errorKey?: string
  errorDetail?: string
}

// 与后端 backend/internal/modules/connection_health 的 JSON 响应字段一一对应。
// 所有类型都不含 upstream key 明文字段。

export type ConnectionHealthState =
  | 'healthy'
  | 'suspect'
  | 'degraded'
  | 'suspended'
  | 'observing'
  | 'recovering'
  | 'disabled'

export interface ModelHealth {
  ruleVersion?: HealthRuleVersion
  firstTokenMs?: number | null
  firstEventMs?: number | null
  recheckPending?: boolean
  failingSince?: string | null
  credentialUnavailableAt?: string | null
  credentialUnavailableReason?: string
  requestPhase?: 'waiting_headers' | 'reading_body' | string
  requestLatencyMs?: number | null
  requestFirstTokenMs?: number | null
  requestFirstEventMs?: number | null
  requestAt?: string | null
  requestErrorKey?: string
  requestErrorDetail?: string
  requestProtocol?: TestProtocol | null
  requestTimeoutSeconds?: number | null
  probeDisposition?: 'applied' | 'invalid' | 'stale' | null
  currentHealthResult?: CurrentHealthResult
  lastAttempt?: TestAttempt
  modelName: string
  providerFamily: string
  configured: boolean
  state: ConnectionHealthState
  currentWeight: number
  consecutiveFailures: number
  consecutiveSuccesses: number
  lastProbeAt: string | null
  lastSuccessAt: string | null
  lastFailureAt: string | null
  lastLatencyMs: number | null
  lastSuccessLatencyMs?: number | null
  lastErrorKey: string
  lastErrorDetail: string
  lastRemoteAction: string
  probeResult?: string
  elapsedSeconds?: number | null
  nextProbeAt?: string | null
  blockedReason?: string
  effectiveIntervalSeconds?: number
  effectivePolicySources?: EffectiveProbePolicySource[]
  budgetPolicyId?: string
  updatedAt: string | null
}

export interface EffectiveProbePolicySource {
  policyId: string
  policyName: string
  continueAutoProbe: boolean
  effectiveIntervalSeconds: number
}

export interface ConnectionHealth {
  connectionId: string
  upstreamSiteId: string
  upstreamGroupId: string
  upstreamGroupName: string
  upstreamKeyId: string
  groupType: string
  models: ModelHealth[]
}

export interface OwnGroupHealth {
  ownGroupId: string
  ownGroupName: string
  hasConnections: boolean
  connections: ConnectionHealth[]
}

// AdminGroupHealth 系列类型对应后端 GET /api/connection-health/admin-groups 的响应：
// 「当前 admin workspace 下的 admin 全量分组 -> 分组下账号/渠道（独立探活目标）-> 独立探活状态叠加」。
// 探活体系已改为独立目标：账号/渠道本身就是探活目标，不再依赖 real_connections 对接链路。
// 所有字段都不含 upstream key / token / cookie / credentials 明文。
export type AdminGroupType = 'public' | 'exclusive' | 'subscription'

// AdminProbeUnavailableReason 是账号/渠道不可探活的原因枚举（后端脱敏后透出），
// 前端据此展示明确文案，绝不携带任何密钥/上游报文。
export type AdminProbeUnavailableReason =
  | 'credential_unavailable'
  | 'secure_verification_required'
  | 'base_url_unavailable'
  | 'model_unavailable'
  | 'export_unavailable'
  | 'credentials_redacted'

export type PrioritySyncBlockReason =
  | 'binding_missing'
  | 'site_unavailable'
  | 'key_unavailable'
  | 'key_missing'
  | 'key_deleted'
  | 'admin.upstream.errors.announcementAckRequired'
  | 'admin.upstream.errors.upstreamInsufficientBalance'
  | 'admin.upstream.errors.upstreamKeyQuotaExhausted'
  | 'admin.upstream.errors.upstreamKeyExpired'
  | 'admin.upstream.errors.refreshTokenRejected'
  | 'admin.upstream.errors.accessTokenRejected'
  | 'groups_unavailable'
  | 'group_missing'
  | 'group_ambiguous'
  | 'group_not_found'
  | 'multiplier_missing'
  | 'snapshot_stale'
  | 'snapshot_updating'

export interface AdminGroupHealthSummary {
  totalAccounts: number
  probeableAccounts: number
  unprobeableAccounts: number
  healthyModels: number
  // 疑似属于健康档，同时单列用于筛选。
  suspectModels?: number
  // degradedModels 为兼容旧版，仍包含 degraded + observing + recovering。
  degradedModels: number
  observingModels?: number
  recoveringModels?: number
  suspendedModels: number
  disabledModels: number
  pendingModels?: number
  unconfiguredModels: number
  lastProbeAt: string | null
}

// TargetPolicyAssignmentSummary 是某个账号/渠道已分配策略的展示摘要，不含任何敏感字段。
export interface TargetPolicyAssignmentSummary {
  policyId: string
  policyName: string
  enabled: boolean
  priorityMode?: ConnectionHealthPriorityMode
  strategyMode?: ConnectionHealthStrategyMode
  autoRemoteActionEnabled?: boolean
}

// TargetPolicyAssignments 是策略分配管理接口 GET/PUT 的响应体。
export interface TargetPolicyAssignments {
  policyIds: string[]
  policies: TargetPolicyAssignmentSummary[]
}

export interface AdminGroupUnprobedModel {
  modelName: string
  providerFamily: string
  nextProbeAt?: string | null
  blockedReason?: string
  effectiveIntervalSeconds?: number
  effectivePolicySources?: EffectiveProbePolicySource[]
  budgetPolicyId?: string
}

export type AccountTier = 1 | 2

export type PriorityCandidateMode = 'first_active' | 'second_active' | 'safety_lock'
export type PriorityCandidateState = 'candidate' | 'unavailable' | 'safety_lock' | 'out_of_scope'
export type PriorityCandidateRegion = 'normal' | 'hot_standby'
export type PriorityCandidateHealthBand = 'healthy' | 'recovering' | 'degraded'

export interface PriorityCandidateCapacity {
  region: PriorityCandidateRegion
  healthBand: PriorityCandidateHealthBand
  start: number
  end: number
  capacity: number
  actual: number
  remaining: number
  overflow: boolean
}

export interface PriorityCandidateSummary {
  mode: PriorityCandidateMode
  candidatePriorityReady: boolean
  safetyReason?: string
  candidateCount: number
  outOfScopeCount: number
  blockerCount: number
  capacities: PriorityCandidateCapacity[]
}

export interface PriorityCandidateProjection {
  state: PriorityCandidateState
  reason?: string
  rank?: number
  priority?: number
  region?: PriorityCandidateRegion
  healthBand?: PriorityCandidateHealthBand
  successLatencyMs?: number
  multiplier?: number
  priorityEvidence: string
  blocksTakeover: boolean
}

export interface AccountTierResult {
  targetId: string
  accountTier: AccountTier
}

export interface AdminGroupAccount {
  remoteActionPending?: { action: string; dispatchId?: string; phase: string; reason: string; source?: string }
  testConfiguration?: EffectiveTestConfiguration
  id: string
  name: string
  platform: string
  type: string
  status: string
  accountTier?: AccountTier
  mainSiteError?: string
  schedulable?: boolean
  tempUnschedulableUntil?: string | null
  tempUnschedulableKnown?: boolean
  tempUnschedulableActive?: boolean
  tempUnschedulableReason?: string
  rateLimitResetAt?: string | null
  rateLimitKnown?: boolean
  rateLimitActive?: boolean
  overloadUntil?: string | null
  overloadKnown?: boolean
  overloadActive?: boolean
  schedulableSource?: string
  schedulableChangedAt?: string | null
  lastSchedulableAction?: string
  lastSchedulableActionAt?: string | null
  lastSchedulableActionResult?: string
  lastSchedulableActionErrorKey?: string
  upstreamStatusSource?: string
  healthStatusSource?: string
  priority?: number
  concurrency?: number
  // Sub2API admin 转发账号记录自身的 rate_multiplier；保留用于兼容既有接口。
  rateMultiplier?: number
  loadFactor?: number
  weight?: number
  models?: string
  groupIds?: string[]
  // 真实对接记录中该转发账号所使用的上游 API Key 所属分组及其当前倍率。
  // 旧后端或无法可靠关联的账号不返回这些字段，前端按未知值展示。
  upstreamKeyGroupName?: string
  upstreamKeyGroupId?: string
  upstreamKeyGroupMultiplier?: number
  // 独立探活字段：targetId 是稳定探活目标 ID，手动探活/事件按 targetId 走。
  targetId: string
  probeAvailable: boolean
  probeUnavailableReason?: AdminProbeUnavailableReason | string
  modelHealth: ModelHealth[]
  // 新后端单独返回尚无状态的配置模型；旧后端缺失该字段时按空数组兼容。
  unprobedModels?: AdminGroupUnprobedModel[]
  // 策略分配字段：与 probeAvailable 完全解耦——未分配策略仍可手动一次性探活，只是不会被
  // 调度器自动探活。旧后端响应不带这些字段时前端按「未分配」兜底展示，不强制要求存在。
  assignedPolicyIds?: string[]
  assignedPolicies?: TargetPolicyAssignmentSummary[]
  // 实际经过账号级覆盖/分组继承解析后生效的策略，供倍率和健康摘要使用。
  effectivePolicyIds?: string[]
  effectivePolicies?: TargetPolicyAssignmentSummary[]
  hasAssignedPolicy?: boolean
  hasEnabledPolicy?: boolean
  hasEnabledProbePolicy?: boolean
  policyAssignmentSource?: 'none' | 'target' | 'group' | 'mixed' | string
  excludedFromGroupPolicy?: boolean
  priorityManaged?: boolean
  priorityConflict?: boolean
  priorityOriginal?: number
  priorityExpected?: number
  priorityConflictValue?: number
  priorityConflictAt?: string | null
  probeModelsConfigured?: boolean
  effectiveMultiplier?: number | null
  multiplierResolutionStatus?: 'resolved' | 'unassociated' | 'missing' | 'conflict' | 'stale' | 'unavailable' | 'updating' | string
  multiplierSource?: 'upstream_key' | 'local_fallback' | 'last_confirmed' | 'none' | string
  localFallbackMultiplier?: number | null
  upstreamSiteId?: string
  prioritySyncBlocked?: boolean
  prioritySyncBlockReason?: PrioritySyncBlockReason | string
  todayQuestionAnswerSubmitted?: number
  todayQuestionAnswerCorrect?: number
  productionSortOrder?: number
  priorityCandidate?: PriorityCandidateProjection
}

export interface AdminGroupHealth {
  id: string
  name: string
  platform: string
  status: string
  type: AdminGroupType | string
  isExclusive: boolean
  subscriptionType: string
  multiplier: number | null
  multiplierDisplay: string
  probeSortFallbackMultiplier?: number | null
  accountCount: number
  monitoredAccountCount?: number
  excludedAccountCount?: number
  assignedPolicyIds?: string[]
  assignedPolicies?: TargetPolicyAssignmentSummary[]
  hasAssignedPolicy?: boolean
  hasEnabledPolicy?: boolean
  hasEnabledProbePolicy?: boolean
  priorityMode?: ConnectionHealthPriorityMode
  priorityConflictCount?: number
  priorityConflicts?: AdminPriorityConflict[]
  probeModelsConfigured?: boolean
  healthSummary: AdminGroupHealthSummary
  // 仅在上游来源唯一且短期样本可靠时返回；金额单位为核算金额。
  todayCost?: number | null
  recentHourCost?: number | null
  costObservedAt?: string | null
  costMode?: 'exact' | 'retained' | 'unknown' | string
  costSource?: string
  costReason?: string
  costComplete?: boolean
  siteReportedCost?: number | null
  groupAttributedCost?: number | null
  unattributedCost?: number | null
  minProductionRank?: number | null
  priorityCandidateSummary?: PriorityCandidateSummary
  // accountsError 非空（i18n key）表示该分组账号列表加载失败，其余分组不受影响。
  accountsError?: string
  accounts: AdminGroupAccount[]
}

export interface AdminPriorityConflict {
  targetId: string
  accountName: string
  currentPriority?: number
  expectedPriority?: number
  conflictAt?: string | null
}

export interface ConnectionHealthEvent {
  ruleVersion?: HealthRuleVersion
  firstTokenMs?: number | null
  firstEventMs?: number | null
  longFailure?: boolean
  requestPhase?: 'waiting_headers' | 'reading_body' | string
  requestProtocol?: TestProtocol | null
  requestTimeoutSeconds?: number | null
  probeDisposition?: 'applied' | 'invalid' | 'stale' | null
  id: string
  connectionId: string
  modelName: string
  ownGroupName: string
  upstreamSiteId: string
  upstreamGroupName: string
  result: string
  fromState: string
  toState: string
  latencyMs: number | null
  errorKey: string
  errorDetail?: string
  remoteAction: string
  actionSource?: string
  source?: 'manual' | 'scheduled' | 'legacy' | string
  createdAt: string
}

export interface ConnectionHealthOverview {
  totalConnections: number
  healthy: number
  suspect?: number
  degraded: number
  suspended: number
  observing: number
  recovering: number
  disabled: number
  unconfigured: number
  recentEvents: ConnectionHealthEvent[]
}

// 工作台轻量摘要只来自本地健康状态与事件表，不会在页面加载时触发上游探活。
export interface ConnectionHealthStoredSummary {
  totalTargets: number
  healthyTargets: number
  attentionTargets: number
  suspendedTargets: number
  managedTargets: number
  recentFailureEvents: number
  lastProbeAt?: string | null
}

export interface ModelTargetInput {
  id?: string
  modelName: string
  providerFamily: string
  enabled: boolean
  probePrompt?: string
  maxProbeTokens?: number
}

export interface ConnectionHealthModelTarget extends Required<Pick<ModelTargetInput, 'modelName' | 'providerFamily' | 'enabled'>> {
  id: string
  policyId: string
  probePrompt: string
  maxProbeTokens: number
  createdAt: string
  updatedAt: string
}

export interface ConnectionHealthPolicy {
  rulePresetId?: string
  legacyPresetId?: string
  id: string
  name: string
  enabled: boolean
  ownGroupId: string
  ownGroupName: string
  modelPattern: string
  probeMode: string
  probeIntervalSeconds: number
  continueProbeWhenUnschedulable: boolean
  unschedulableProbeIntervalMinutes: number
  failureThreshold: number
  successThreshold: number
  cooldownSeconds: number
  observationSeconds: number
  recoveryStepPercent: number
  autoDegradeEnabled: boolean
  autoRemoteActionEnabled: boolean
  priorityMode?: ConnectionHealthPriorityMode
  strategyMode?: ConnectionHealthStrategyMode
  dailyProbeBudget: number
  createdAt: string
  updatedAt: string
  modelTargets: ConnectionHealthModelTarget[]
}

// ProbeModelCandidate 是手动探活模型选择弹窗里的一行候选模型：来自当前 workspace 启用探活策略
// 下的启用 modelTargets，按连接所属的 own group 匹配（见 composables/useConnectionHealth 的
// matchingProbeCandidates）。不包含任何 upstream key / token 等敏感字段。
export interface ProbeModelCandidate {
  modelName: string
  providerFamily: string
  policyId: string
  policyName: string
  autoRemoteActionEnabled: boolean
  maxProbeTokens: number
}

// ManualProbeModelOption 是手动一次性探活弹窗展示的模型候选，来自后端 server-only 现查
// 上游 /v1/models 的结果，只含安全字段，不含 base_url/key/credentials。
export interface ManualProbeModelOption {
  id: string
  name: string
  ownedBy?: string
  providerFamily?: string
}

// ManualProbeResult 是手动一次性探活单个模型的 transient 结果：只用于弹窗内展示，
// 不对应任何落库的状态/事件记录。
export interface ManualProbeResult {
  ruleVersion?: HealthRuleVersion
  firstTokenMs?: number | null
  firstEventMs?: number | null
  requestPhase?: 'waiting_headers' | 'reading_body' | string
  protocol?: TestProtocol | null
  probeTimeoutSeconds?: number | null
  probeDisposition?: 'applied' | 'invalid' | 'stale' | null
  configurationChanged?: boolean
  modelName: string
  result: string
  healthy: boolean
  latencyMs: number | null
  errorKey: string
  errorDetail: string
  probedAt: string
}

export interface TestQuestion {
  id: string
  name: string
  body: string
  keywords: string[]
  enabled: boolean
  isDefault: boolean
  createdAt: string
  updatedAt: string
}

export interface TestQuestionInput {
  name: string
  body: string
  keywords?: string[] | null
}

export type QuestionAnswerStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled'
export type QuestionAnswerReasoningEffort = 'low' | 'medium' | 'high' | 'xhigh'
export type QuestionAnswerJudgment = 'unreviewed' | 'correct' | 'incorrect'

export interface QuestionAnswerRecord {
  requestProtocol?: TestProtocol | null
  id: string
  targetId: string
  batchId: string
  modelName: string
  questionId: string
  questionName: string
  questionBody: string
  questionKeywordSnapshot: string[] | null
  reasoningEffort: QuestionAnswerReasoningEffort | null
  answerBody: string
  status: QuestionAnswerStatus
  errorType: string
  answerJudgment: QuestionAnswerJudgment | null
  manualError: boolean
  createdAt: string
  startedAt: string | null
  completedAt: string | null
  updatedAt: string
}

export interface QuestionAnswerRequestStats {
  submitted: number
  inProgress: number
  succeeded: number
  failed: number
  cancelled: number
}

export interface QuestionAnswerReviewStats {
  unreviewed: number
  correct: number
  incorrect: number
}

export interface QuestionAnswerModelStats {
  modelName: string
  requests: QuestionAnswerRequestStats
  reviews: QuestionAnswerReviewStats
}

export interface QuestionAnswerStats {
  requests: QuestionAnswerRequestStats
  reviews: QuestionAnswerReviewStats
  byModel: QuestionAnswerModelStats[]
}

export interface QuestionAnswerHistory {
  records: QuestionAnswerRecord[]
  page: number
  pageSize: 20
  totalItems: number
  totalPages: number
  stats: QuestionAnswerStats
  todayStats: QuestionAnswerStats
}

export interface QuestionAnswerFinalization {
  batchId: string
  state: 'pending' | 'failed'
  recovery: 'cancel' | 'service_shutdown' | 'unavailable'
}

export interface QuestionAnswerBatch {
  finalization?: QuestionAnswerFinalization | null
  batchId: string
  records: QuestionAnswerRecord[]
  reasoningEffort: QuestionAnswerReasoningEffort | null
  repeatCount: number
  submittedCount: number
  completedCount: number
  runningCount: number
  active: boolean
  currentModel: string
  currentQuestion: string
  stats: QuestionAnswerStats
}

export interface QuestionAnswerSubmissionSummary {
  modelCount: number
  questionCount: number
  repeatCount: number
  total: number
  validRepeatCount: boolean
  withinBatchLimit: boolean
}

export interface PolicyInput {
  rulePresetId?: string
  id?: string
  name: string
  enabled: boolean
  ownGroupId: string
  ownGroupName: string
  modelPattern?: string
  probeIntervalSeconds?: number
  continueProbeWhenUnschedulable?: boolean
  unschedulableProbeIntervalMinutes?: number
  failureThreshold?: number
  successThreshold?: number
  cooldownSeconds?: number
  observationSeconds?: number
  recoveryStepPercent?: number
  autoDegradeEnabled: boolean
  autoRemoteActionEnabled: boolean
  priorityMode?: ConnectionHealthPriorityMode
  strategyMode?: ConnectionHealthStrategyMode
  dailyProbeBudget?: number
  modelTargets: ModelTargetInput[]
}

export type HealthRuleVersion = 'legacy' | 'v2'
export interface HealthRulePresetInput {
  name: string
  failureThreshold: number
  successThreshold: number
  cooldownSeconds: number
  failedRetryIntervalSeconds: number
  longFailureAfterSeconds: number
  longFailureIntervalSeconds: number
  delayLineMs: Record<string, number>
  observationSeconds: number
  recoveryStepPercent: number
}
export interface HealthRulePreset extends HealthRulePresetInput {
  id: string
  kind: 'recommended' | 'legacy_snapshot' | 'legacy_default' | 'custom'
  createdAt: string
  updatedAt: string
  policies: Array<{ id: string; name: string }>
}
export interface WorkspaceHealthSettings {
  ruleVersion: HealthRuleVersion
  configGeneration: number
  probeConcurrency: number
  probeConcurrencyVersion: number
  ruleSwitchedAt: string | null
  updatedAt: string
}

export type ConnectionHealthPriorityMode = 'none' | 'multiplier'
export type ConnectionHealthStrategyMode = 'health_probe' | 'multiplier_only'

// AdminGroupPolicyConfiguration 对应分组级动态策略配置。排除列表只影响分组继承，不会清除
// 旧版逐 target 显式分配，保证已上线配置继续生效。
export interface AdminGroupPolicyConfiguration {
  adminGroupId: string
  adminGroupName: string
  policyIds: string[]
  policies: TargetPolicyAssignmentSummary[]
  excludedTargetIds: string[]
  probeSortFallbackMultiplier?: number | null
	prioritySyncStatus?: 'pending' | 'running' | 'partial' | 'success' | 'failed' | string
}

export interface PrioritySyncStatus {
  failedTargets?: Array<{ accountId: string; accountName?: string; reason: string }>
  actionDiagnostics?: Array<{ accountId: string; accountName?: string; targetId: string; action: string; dispatchId?: string; phase: string; reason: string; observedAt?: string }>
	workspaceId: string
	status: 'idle' | 'pending' | 'running' | 'partial' | 'success' | 'failed' | string
	errorKey?: string
	pendingSince?: string | null
	lastAttemptAt?: string | null
	lastFailureAt?: string | null
	failedCount: number
}

export interface AdminGroupPolicyConfigurationInput {
  policyIds: string[]
  excludedTargetIds: string[]
  probeSortFallbackMultiplier: number | null
  quickPolicy?: PolicyInput
}

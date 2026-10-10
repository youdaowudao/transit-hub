<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  Activity,
  AlertTriangle,
  ArrowDownUp,
  Bolt,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Clock3,
  Eye,
  Gauge,
  Loader2,
  Radar,
  Settings2,
  ShieldCheck,
  ShieldQuestion,
  Power,
  X,
  Zap,
} from 'lucide-vue-next'
import { Tooltip } from '@/components/ui/tooltip'
import { formatQuestionAnswerAccuracy, questionAnswerAccuracy, questionAnswerAccuracyRatio } from '../../utils/questionAnswers'
import AccountTierEditor from './AccountTierEditor.vue'
import { modelControlText as modelMessage, modelControlSupplyTooltip, modelControlSummaryTooltip } from '../../utils/questionAnswerModelControl'
import AccountPriorityEditor from './AccountPriorityEditor.vue'
import AccountConcurrencyEditor from './AccountConcurrencyEditor.vue'
import {
  connectionHealthMessageKey,
  connectionHealthStateBadgeClass,
  formatConnectionHealthTime,
  formatConnectionHealthElapsed,
  hasValidConnectionHealthTime,
  hasCurrentHealthFailure,
  canOpenManualProbeHistory,
  isTestConfigurationBlocked,
  testProtocolName,
  remoteActionLabelKey,
} from '../../composables/useConnectionHealth'
import type {
  AccountTierResult,
  AccountManagementResult,
  AdminGroupAccount,
  AdminGroupHealth,
  ConnectionHealthState,
} from '../../types/connectionHealth'
import {
  resolveConnectionHealthMultiplierDisplay,
  resolvePrioritySyncBlockReasonMessage,
  prioritySyncBlockReasonTranslationKey,
} from '../../utils/connectionHealthMultiplier'

const props = withDefaults(defineProps<{
  group: AdminGroupHealth
  hideUnmonitoredAccounts: boolean
  questionAnswerUnreadTargetIds: string[]
  actionLoading: boolean
  quickProbePhases?: Record<string, 'starting' | 'queued' | 'running'>
  quickProbeErrors?: Record<string, string>
  quickProbeSuccesses?: Record<string, { modelName: string; latencyMs: number; protocol?: string | null; timeoutSeconds?: number | null }>
  recentSummaryFailures?: string[]
  modelControlFailures?: string[]
}>(), {
  quickProbePhases: () => ({}),
  quickProbeErrors: () => ({}),
  quickProbeSuccesses: () => ({}),
  recentSummaryFailures: () => [],
  modelControlFailures: () => [],
})

const modelSupplyExpanded = ref(false)
const visibleModelSupply = computed(() => modelSupplyExpanded.value ? props.group.modelSupply?.items ?? [] : (props.group.modelSupply?.items ?? []).slice(0, 8))
watch(() => props.group.id, () => { modelSupplyExpanded.value = false })
const emit = defineEmits<{
  (event: 'tier-saved', result: AccountTierResult): void
  (event: 'priority-saved', result: AccountManagementResult): void
  (event: 'concurrency-saved', result: AccountManagementResult): void
  (event: 'setup', group: AdminGroupHealth): void
  (event: 'probe', account: AdminGroupAccount): void
  (event: 'quick-probe', account: AdminGroupAccount): void
  (event: 'view-events', account: AdminGroupAccount): void
  (event: 'update:hide-unmonitored-accounts', value: boolean): void
  (event: 'set-schedulable', account: AdminGroupAccount): void
  (event: 'assign-policy', account: AdminGroupAccount): void
  (event: 'question-answer-view', value: { targetId: string; batchId?: string }): void
  (event: 'question-answer-recent-retry', targetId: string): void
  (event: 'displayed-targets', targetIds: string[]): void
}>()

import { t, te } from '@/locales'
const prefix = 'admin.connectionHealth'
const detailPrefix = `${prefix}.groupDetail`
const expandedTargetId = ref('')
const hasUnreadQuestionAnswer = (account: AdminGroupAccount): boolean =>
  props.questionAnswerUnreadTargetIds.includes(account.targetId)

const formalProbeModels = (account: AdminGroupAccount): string[] => {
  const models = [...(account.modelHealth ?? []), ...(account.unprobedModels ?? [])]
  return Array.from(new Set(models.map(model => model.modelName).filter(Boolean)))
}

const quickProbeModel = (account: AdminGroupAccount): string => {
  const models = formalProbeModels(account)
  return models.includes('gpt-5.6-sol') ? 'gpt-5.6-sol' : (models[0] ?? '')
}

const quickProbeUnavailableReason = (account: AdminGroupAccount): string => {
  if (isTestConfigurationBlocked(account.testConfiguration)) return readableMessage(account.testConfiguration?.blockedReason || 'admin.connectionHealth.errors.testConfigurationUnavailable')
  if (!account.probeAvailable) {
    const reasonKey = `${prefix}.probeUnavailableReasons.${account.probeUnavailableReason ?? ''}`
    return te(reasonKey) ? t(reasonKey) : t(`${detailPrefix}.quickProbe.unavailable`)
  }
  if (!account.hasEnabledProbePolicy) return t(`${detailPrefix}.quickProbe.noPolicy`)
  if (!quickProbeModel(account)) return t(`${detailPrefix}.quickProbe.noModels`)
  return ''
}

const quickProbeLabel = (account: AdminGroupAccount): string => {
  const unavailableReason = quickProbeUnavailableReason(account)
  if (unavailableReason) return t(`${detailPrefix}.quickProbe.disabled`, { reason: unavailableReason })
  const model = quickProbeModel(account)
  const phase = props.quickProbePhases[account.targetId]
  if (phase) {
    return t(`${detailPrefix}.quickProbe.${phase}`, { model })
  }
  return t(`${detailPrefix}.quickProbe.ready`, { model })
}

const quickProbeDisabled = (account: AdminGroupAccount): boolean =>
  Boolean(props.quickProbePhases[account.targetId]) || Boolean(quickProbeUnavailableReason(account))

const emitQuickProbe = (account: AdminGroupAccount) => {
  if (quickProbeDisabled(account)) return
  emit('quick-probe', account)
}

type GroupHealthFilter =
  | { kind: 'all' }
  | { kind: 'monitored' }
  | { kind: 'probeable' }
  | { kind: 'unprobeable' }
  | { kind: 'unconfigured' }
  | { kind: 'modelState'; state: ConnectionHealthState }
  | { kind: 'notProbed' }

type AccountSortField =
  | 'account'
  | 'health'
  | 'strategy'
  | 'priority'
  | 'effectiveMultiplier'
  | 'upstreamMultiplier'
  | 'latency'
  | 'stability'
  | 'latestAccuracy'

type SortDirection = 'asc' | 'desc'
type StateBreakdownItem = {
  key: string
  count: number
  filter: GroupHealthFilter
  tone: string
}

const monitoringEnabled = (account: AdminGroupAccount): boolean =>
  account.hasEnabledProbePolicy ?? account.hasEnabledPolicy ?? account.assignedPolicies?.some((policy) => policy.enabled) ?? Boolean(account.hasAssignedPolicy)

const monitoredCount = computed(() => props.group.monitoredAccountCount ?? props.group.accounts.filter(monitoringEnabled).length)
const lastProbeAt = computed(() => props.group.healthSummary?.lastProbeAt ?? null)
const isNewAPI = computed(() => props.group.platform.toLowerCase().includes('new'))

const strictDegradedCount = computed(() => Math.max(
  0,
  (props.group.healthSummary.degradedModels ?? 0)
    - (props.group.healthSummary.observingModels ?? 0)
    - (props.group.healthSummary.recoveringModels ?? 0),
))

const modelStateFilter = (state: ConnectionHealthState): GroupHealthFilter => ({ kind: 'modelState', state })

const stateBreakdown = computed<StateBreakdownItem[]>(() => [
  { key: 'healthy', count: props.group.healthSummary.healthyModels ?? 0, filter: modelStateFilter('healthy'), tone: 'text-emerald-600 dark:text-emerald-400' },
  { key: 'suspect', count: props.group.healthSummary.suspectModels ?? 0, filter: modelStateFilter('suspect'), tone: 'text-amber-600 dark:text-amber-400' },
  { key: 'degraded', count: strictDegradedCount.value, filter: modelStateFilter('degraded'), tone: 'text-amber-600 dark:text-amber-400' },
  { key: 'suspended', count: props.group.healthSummary.suspendedModels ?? 0, filter: modelStateFilter('suspended'), tone: 'text-red-600 dark:text-red-400' },
  { key: 'observing', count: props.group.healthSummary.observingModels ?? 0, filter: modelStateFilter('observing'), tone: 'text-blue-600 dark:text-blue-400' },
  { key: 'recovering', count: props.group.healthSummary.recoveringModels ?? 0, filter: modelStateFilter('recovering'), tone: 'text-cyan-600 dark:text-cyan-400' },
  { key: 'disabled', count: props.group.healthSummary.disabledModels ?? 0, filter: modelStateFilter('disabled'), tone: 'text-muted-foreground' },
  { key: 'notProbed', count: props.group.healthSummary.pendingModels ?? 0, filter: { kind: 'notProbed' }, tone: 'text-muted-foreground' },
  { key: 'unconfigured', count: props.group.healthSummary.unconfiguredModels ?? 0, filter: { kind: 'unconfigured' }, tone: 'text-muted-foreground' },
  { key: 'unprobeable', count: props.group.healthSummary.unprobeableAccounts ?? 0, filter: { kind: 'unprobeable' }, tone: 'text-amber-600 dark:text-amber-400' },
])

const readableMessage = (rawKey: string): string => t(connectionHealthMessageKey(rawKey, te))

const STATE_PRIORITY: ConnectionHealthState[] = ['suspended', 'disabled', 'degraded', 'observing', 'recovering', 'suspect', 'healthy']
const accountProtocolUnverified = (account: AdminGroupAccount): boolean => account.modelHealth.some(model => model.currentHealthResult?.status === 'unverified')
const aggregateState = (account: AdminGroupAccount): ConnectionHealthState | '' => {
  const present = new Set((account.modelHealth ?? []).map((model) => model.state))
  return STATE_PRIORITY.find((state) => present.has(state)) ?? ''
}

const unprobedModels = (account: AdminGroupAccount) => account.unprobedModels ?? []
const isNotProbed = (account: AdminGroupAccount): boolean =>
  account.probeAvailable && account.probeModelsConfigured !== false && (unprobedModels(account).length > 0 || account.modelHealth.length === 0)

const isSub2API = (account: AdminGroupAccount): boolean => account.targetId.toLowerCase().startsWith('sub2api:')
const hasMainSiteError = (account: AdminGroupAccount): boolean =>
  isSub2API(account) && account.status?.trim().toLowerCase() === 'error'
const mainSiteErrorReason = (account: AdminGroupAccount): string =>
  account.mainSiteError?.trim() || t(`${detailPrefix}.mainSiteErrorReasonUnavailable`)
const schedulableLabel = (account: AdminGroupAccount): string => {
  if (!isSub2API(account)) return t(`${detailPrefix}.notApplicable`)
  if (account.schedulable == null) return t(`${detailPrefix}.schedulableUnknown`)
  return account.schedulable ? t(`${detailPrefix}.schedulableOn`) : t(`${detailPrefix}.schedulableOff`)
}

// Activity is projected with the TransitHub process clock at the existing
// refresh. The browser adds no clock source, timer, or inventory request.
const mainSiteRestrictions = (account: AdminGroupAccount): string[] => {
  if (!isSub2API(account)) return []
  return [
    { kind: 'temporaryPause', known: account.tempUnschedulableKnown, active: account.tempUnschedulableActive, until: account.tempUnschedulableUntil, reason: account.tempUnschedulableReason },
    { kind: 'rateLimit', known: account.rateLimitKnown, active: account.rateLimitActive, until: account.rateLimitResetAt },
    { kind: 'overload', known: account.overloadKnown, active: account.overloadActive, until: account.overloadUntil },
  ].flatMap(restriction => {
    if (restriction.known !== true) return [t(`${detailPrefix}.mainSiteRestrictions.${restriction.kind}Unknown`)]
    if (!restriction.active || !restriction.until) return []
    const message = t(`${detailPrefix}.mainSiteRestrictions.${restriction.kind}Until`, { time: formatConnectionHealthTime(restriction.until) })
    return [restriction.reason ? `${message} · ${restriction.reason}` : message]
  })
}

const remotePendingLabel = (account: AdminGroupAccount): string => {
  const pending = account.remoteActionPending
  if (!pending) return ''
  const prefix = `${detailPrefix}.remoteActionPending.reasons`
  const key = `${prefix}.${pending.reason}`
  return te(key) ? t(key) : t(`${prefix}.unknown`)
}

const upstreamStatusLabel = (account: AdminGroupAccount): string => {
  const status = account.status?.toLowerCase()
  if (status === 'active' || status === '1') return t(`${detailPrefix}.upstreamAccountActive`)
  if (status === 'inactive' || status === '0' || status === 'disabled') return t(`${detailPrefix}.upstreamAccountInactive`)
  return t(`${detailPrefix}.upstreamStatus`, { status: account.status || t(`${detailPrefix}.unknownUpstreamStatus`) })
}

const statusSourceLabel = (source: string | null | undefined): string =>
  t(`${detailPrefix}.statusSourceLabels.${source || 'unknown'}`)

const assignmentLabel = (account: AdminGroupAccount): string => {
  const policies = account.assignedPolicies ?? []
  if (policies.length === 0) return t(`${detailPrefix}.unmonitored`)
  if (policies.length === 1) return policies[0].policyName
  return t(`${detailPrefix}.policyCount`, { name: policies[0].policyName, count: policies.length - 1 })
}

const assignmentSourceLabel = (account: AdminGroupAccount): string =>
  t(`${detailPrefix}.assignmentSources.${account.policyAssignmentSource ?? 'none'}`)

const strategyStateLabel = (account: AdminGroupAccount): string => {
  if (account.hasAssignedPolicy && !account.hasEnabledPolicy) return t(`${detailPrefix}.strategyDisabled`)
  return assignmentSourceLabel(account)
}

const priorityStateLabel = (account: AdminGroupAccount): string => {
  if (!account.priorityManaged) return t(`${detailPrefix}.priorityUnmanaged`)
  if (account.hasEnabledProbePolicy && account.probeModelsConfigured === false) return t(`${detailPrefix}.probeModelsNotConfigured`)
  if (isNotProbed(account)) return t(`${detailPrefix}.priorityPendingProbe`)
  const state = aggregateState(account)
  if (state === 'suspended' || state === 'disabled') return t(`${detailPrefix}.healthSuspended`)
  return t(`${detailPrefix}.priorityManaged`)
}

const lastSchedulableActionLabel = (account: AdminGroupAccount): string => {
  const action = remoteActionLabelKey(account.lastSchedulableAction ?? '')
  if (!action) return ''
  const result = account.lastSchedulableActionResult === 'schedulable_user_action_succeeded'
    ? t(`${detailPrefix}.schedulableActionSucceeded`)
    : t(`${detailPrefix}.schedulableActionFailed`)
  const error = account.lastSchedulableActionErrorKey
    ? ` · ${t(connectionHealthMessageKey(account.lastSchedulableActionErrorKey, te))}`
    : ''
  return t(`${detailPrefix}.lastSchedulableAction`, {
    action: t(action.key, action.params),
    result,
    time: formatConnectionHealthTime(account.lastSchedulableActionAt ?? null),
    error,
  })
}

const blockedReasonLabel = (reason: string | null | undefined): string => {
  if (!reason) return ''
  const key = `${prefix}.probeBlockedReasons.${reason}`
  return te(key) ? t(key) : reason
}

const effectiveSourcesLabel = (accountModel: AdminGroupAccount['modelHealth'][number] | NonNullable<AdminGroupAccount['unprobedModels']>[number]): string =>
  (accountModel.effectivePolicySources ?? [])
    .map(source => t(`${detailPrefix}.models.policySource`, {
      name: source.policyName || source.policyId,
      interval: source.effectiveIntervalSeconds,
      state: source.continueAutoProbe ? t(`${detailPrefix}.models.policyContinues`) : t(`${detailPrefix}.models.policyStops`),
    }))
    .join('；')

const toggleModels = (targetId: string) => {
  expandedTargetId.value = expandedTargetId.value === targetId ? '' : targetId
}

const activeFilter = ref<GroupHealthFilter>({ kind: 'all' })
const sortField = ref<AccountSortField>('health')
const sortDirection = ref<SortDirection>('asc')
const customSortActive = ref(false)

const DEFAULT_SORT_DIRECTIONS: Record<AccountSortField, SortDirection> = {
  account: 'asc',
  health: 'asc',
  strategy: 'asc',
  priority: 'asc',
  effectiveMultiplier: 'asc',
  upstreamMultiplier: 'asc',
  latency: 'asc',
  stability: 'asc',
  latestAccuracy: 'desc',
}

const HEALTH_SORT_RANK: Record<ConnectionHealthState, number> = {
  healthy: 0,
  suspect: 0,
  observing: 1,
  recovering: 2,
  degraded: 3,
  suspended: 4,
  disabled: 5,
}

const filtersEqual = (first: GroupHealthFilter, second: GroupHealthFilter): boolean =>
  first.kind === second.kind
  && (first.kind !== 'modelState' || second.kind === 'modelState' && first.state === second.state)

const filterLabel = computed(() => {
  switch (activeFilter.value.kind) {
    case 'monitored':
      return t(`${detailPrefix}.metrics.monitored`)
    case 'probeable':
      return t(`${detailPrefix}.metrics.probeable`)
    case 'unprobeable':
      return t(`${detailPrefix}.statusBreakdown.unprobeable`)
    case 'notProbed':
      return t(`${detailPrefix}.statusBreakdown.notProbed`)
    case 'unconfigured':
      return t(`${detailPrefix}.statusBreakdown.unconfigured`)
    case 'modelState':
      return t(`${prefix}.stateLabels.${activeFilter.value.state}`)
    default:
      return t(`${detailPrefix}.filters.all`)
  }
})

const applyFilter = (filter: GroupHealthFilter) => {
  activeFilter.value = filter
  expandedTargetId.value = ''
}

const clearFilter = () => applyFilter({ kind: 'all' })

const isFilterActive = (filter: GroupHealthFilter): boolean => filtersEqual(activeFilter.value, filter)

const matchesFilter = (account: AdminGroupAccount): boolean => {
  const filter = activeFilter.value
  switch (filter.kind) {
    case 'monitored':
      return monitoringEnabled(account)
    case 'probeable':
      return account.probeAvailable
    case 'unprobeable':
      return !account.probeAvailable
    case 'notProbed':
      return isNotProbed(account)
    case 'unconfigured':
      return Boolean(account.hasEnabledProbePolicy) && account.probeModelsConfigured === false
        || account.modelHealth.some(model => model.currentHealthResult?.status === 'unverified')
    case 'modelState':
      return account.modelHealth.some(model => model.currentHealthResult?.status !== 'unverified' && (model.state === filter.state || filter.state === 'healthy' && model.state === 'suspect'))
    default:
      return true
  }
}

const shouldHideUnmonitored = (account: AdminGroupAccount): boolean =>
  props.hideUnmonitoredAccounts && !monitoringEnabled(account)

const accountLatency = (account: AdminGroupAccount): number | null => {
  const values = account.modelHealth
    .map(model => model.lastSuccessLatencyMs)
    .filter((value): value is number => value != null && Number.isFinite(value))
  return values.length > 0 ? Math.max(...values) : null
}

const accountHasSlowResponse = (account: AdminGroupAccount): boolean => account.modelHealth.some(model => model.probeResult === 'slow_response' && model.probeDisposition !== 'stale' && model.probeDisposition !== 'invalid')
const accountResponseLabel = (account: AdminGroupAccount): string => account.modelHealth.some(model => model.ruleVersion === 'v2' && model.probeResult === 'slow_response') ? '延迟' : '慢响应'
const accountMeasurement = (account: AdminGroupAccount, field: 'firstTokenMs' | 'firstEventMs'): number | null => {
  const values = account.modelHealth.map(model => model[field]).filter((value): value is number => value != null && Number.isFinite(value))
  return values.length ? Math.max(...values) : null
}

// 稳定性列按「最差模型」聚合：权重取最小、最近失败取最新、连败取最大，
// 让一行的读数不会被同账号里状态较好的模型稀释。
const DAY_SECONDS = 24 * 60 * 60

// 与展开行口径一致：展开行渲染 account.modelHealth 全量，这里不能再按 configured
// 过滤。后端的 configured 是「最后一次错误是否为凭据不可用」，鉴权失败、权重被打到 0
// 的模型会被标成 false，一旦过滤掉，列上只剩健康模型算出 100%，与展开行的读数矛盾。
const aggregatedModelHealth = (account: AdminGroupAccount) => account.modelHealth ?? []

const accountHealthWeight = (account: AdminGroupAccount): number | null => {
  const values = aggregatedModelHealth(account)
    .map(model => model.currentWeight)
    .filter((value): value is number => value != null && Number.isFinite(value))
  return values.length > 0 ? Math.min(...values) : null
}

// 取最近一次失败所在的模型，连同它的 elapsedSeconds 快照一起返回，
// 避免把 A 模型的失败时间和 B 模型的距今秒数拼在一起显示。
const accountLastFailure = (account: AdminGroupAccount) => {
  let latest: { lastFailureAt: string; elapsedSeconds: number | null; at: number } | null = null
  for (const model of aggregatedModelHealth(account)) {
    if (!hasValidConnectionHealthTime(model.lastFailureAt)) continue
    const at = new Date(model.lastFailureAt as string).getTime()
    if (latest && at <= latest.at) continue
    latest = { lastFailureAt: model.lastFailureAt as string, elapsedSeconds: model.elapsedSeconds ?? null, at }
  }
  return latest
}

const accountFailureElapsedSeconds = (account: AdminGroupAccount): number | null => {
  const failure = accountLastFailure(account)
  if (!failure) return null
  if (typeof failure.elapsedSeconds === 'number' && Number.isFinite(failure.elapsedSeconds) && failure.elapsedSeconds >= 0) {
    return failure.elapsedSeconds
  }
  return null
}

// 超过 24 小时统一收口成「24 小时+」：更久的精度对判断当下是否可用没有价值。
// 不到 1 分钟收成「刚刚」，避免副行过长把列撑宽。
const accountStabilityElapsedLabel = (account: AdminGroupAccount): string => {
  const failure = accountLastFailure(account)
  if (!failure) return t(`${detailPrefix}.stabilityColumn.noFailure`)
  const elapsed = accountFailureElapsedSeconds(account)
  if (elapsed != null && elapsed >= DAY_SECONDS) return t(`${detailPrefix}.stabilityColumn.overDay`)
  if (elapsed != null && elapsed < 60) return t(`${detailPrefix}.stabilityColumn.justNow`)
  const value = formatConnectionHealthElapsed(elapsed, failure.lastFailureAt)
  if (!value) return t(`${detailPrefix}.stabilityColumn.unknownElapsed`)
  return t(`${detailPrefix}.stabilityColumn.lastFailure`, { value })
}

const hasStabilityReading = (account: AdminGroupAccount): boolean =>
  account.probeAvailable && aggregatedModelHealth(account).length > 0

// 只在「当前仍处于失败」的模型上取错误原因：后端恢复后仍保留最后一次 errorKey，
// 不能只凭它存在就展示，否则健康账号也会挂着旧报错。
const accountCurrentErrorLabel = (account: AdminGroupAccount): string => {
  const failing = aggregatedModelHealth(account)
    .filter(model => hasCurrentHealthFailure(model) && (model.currentHealthResult?.errorKey || model.lastErrorKey))
  return failing.length > 0 ? readableMessage(failing[0].currentHealthResult?.errorKey || failing[0].lastErrorKey) : ''
}

// 副行压成一条：正常时只说最近一次中断，仍在故障时补上错误原因，
// 因为限流会自行恢复、认证失败不会，这决定要不要动手。
const accountStabilitySubLabel = (account: AdminGroupAccount): string => {
  const elapsed = accountStabilityElapsedLabel(account)
  const error = accountCurrentErrorLabel(account)
  return error ? `${elapsed} · ${error}` : elapsed
}

// 排序权重：权重越低越靠前，同权重按最近中断时间升序（刚断的更需要关注），
// 没有探活读数的目标一律沉底。
const accountStabilityRank = (account: AdminGroupAccount): number | null => {
  if (!hasStabilityReading(account)) return null
  const weight = accountHealthWeight(account) ?? 100
  const elapsed = accountFailureElapsedSeconds(account) ?? DAY_SECONDS
  return weight * (DAY_SECONDS + 1) + Math.min(elapsed, DAY_SECONDS)
}

const accountHealthRank = (account: AdminGroupAccount): number => {
  if (!account.probeAvailable) return 7
  const state = aggregateState(account)
  if (!state) return 6
  return HEALTH_SORT_RANK[state] ?? 6
}

const accountLatestAccuracy = (account: AdminGroupAccount): number | null => questionAnswerAccuracyRatio(account.recentQuestionAnswer)
const formatLatestAccuracy = (account: AdminGroupAccount): string => formatQuestionAnswerAccuracy(account.recentQuestionAnswer ? questionAnswerAccuracy(account.recentQuestionAnswer) : null)
const recentSource = (account: AdminGroupAccount): string => account.recentQuestionAnswer
  ? t(`${prefix}.questionAnswerSchedule.sources.${account.recentQuestionAnswer.source}`) : ''
const recentState = (account: AdminGroupAccount): string => {
  const recent = account.recentQuestionAnswer
  if (!recent) return account.activeNewerQuestionAnswerBatch ? '测试中' : '尚未测试'
  const labels = account.activeNewerQuestionAnswerBatch ? ['测试中'] : []
  if (recent.requests.succeeded === 0) labels.push(recent.requests.failed === recent.requests.submitted ? '最近测试全部失败' : '最近测试已取消')
  else if (accountLatestAccuracy(account) === null) labels.push('待人工判断')
  if (recent.partial) labels.push('部分完成')
  return labels.join(' · ')
}

const accountSortValue = (account: AdminGroupAccount, field: AccountSortField): string | number | null => {
  switch (field) {
    case 'account':
      return (account.name || account.id).toLocaleLowerCase()
    case 'health':
      return accountHealthRank(account)
    case 'strategy':
      return assignmentLabel(account).toLocaleLowerCase()
    case 'priority':
      return account.priority ?? null
    case 'effectiveMultiplier':
      return effectiveMultiplier(account)
    case 'upstreamMultiplier':
      return account.upstreamKeyGroupMultiplier ?? null
    case 'latency':
      return accountLatency(account)
    case 'stability':
      return accountStabilityRank(account)
    case 'latestAccuracy':
      return accountLatestAccuracy(account)
  }
}

const compareSortValues = (
  first: string | number | null,
  second: string | number | null,
  direction: SortDirection,
): number => {
  if (first === null || first === undefined) return second === null || second === undefined ? 0 : 1
  if (second === null || second === undefined) return -1
  const rawDiff = typeof first === 'string' && typeof second === 'string'
    ? first.localeCompare(second)
    : Number(first) - Number(second)
  return direction === 'asc' ? rawDiff : -rawDiff
}

const compareAccountsByName = (first: AdminGroupAccount, second: AdminGroupAccount): number => {
  const nameDiff = (first.name || first.id).localeCompare(second.name || second.id)
  return nameDiff !== 0 ? nameDiff : first.id.localeCompare(second.id)
}

const filteredAccounts = computed(() => props.group.accounts.filter(account =>
  !shouldHideUnmonitored(account) && matchesFilter(account),
))

const sortedAccounts = computed(() => [...filteredAccounts.value].sort((first, second) => {
  if (!customSortActive.value) {
    const orderDiff = compareSortValues(first.productionSortOrder ?? null, second.productionSortOrder ?? null, 'asc')
    return orderDiff !== 0 ? orderDiff : first.targetId.localeCompare(second.targetId)
  }
  const valueDiff = compareSortValues(
    accountSortValue(first, sortField.value),
    accountSortValue(second, sortField.value),
    sortDirection.value,
  )
  if (valueDiff !== 0) return valueDiff
  if (sortField.value === 'latestAccuracy') return compareSortValues(first.productionSortOrder ?? null, second.productionSortOrder ?? null, 'asc')
    || first.targetId.localeCompare(second.targetId)
  return compareAccountsByName(first, second)
}))
watch(() => sortedAccounts.value.map(account => account.targetId).join('\n'), () => emit('displayed-targets', sortedAccounts.value.map(account => account.targetId)), { immediate: true })

const filteredModelHealth = (account: AdminGroupAccount) => {
  const filter = activeFilter.value
  if (filter.kind === 'notProbed') return []
  if (filter.kind === 'unconfigured') return account.modelHealth.filter(model => model.currentHealthResult?.status === 'unverified')
  if (filter.kind !== 'modelState') return account.modelHealth
  return account.modelHealth.filter(model => model.currentHealthResult?.status !== 'unverified' && (model.state === filter.state || filter.state === 'healthy' && model.state === 'suspect'))
}

const filteredUnprobedModels = (account: AdminGroupAccount) => {
  if (activeFilter.value.kind === 'modelState') return []
  if (activeFilter.value.kind === 'notProbed') return unprobedModels(account)
  if (activeFilter.value.kind === 'unconfigured') return []
  return unprobedModels(account)
}

const toggleSort = (field: AccountSortField) => {
  customSortActive.value = true
  if (sortField.value === field) {
    sortDirection.value = sortDirection.value === 'asc' ? 'desc' : 'asc'
    return
  }
  sortField.value = field
  sortDirection.value = DEFAULT_SORT_DIRECTIONS[field]
}

const ariaSort = (field: AccountSortField): 'ascending' | 'descending' | 'none' => {
  if (!customSortActive.value || sortField.value !== field) return 'none'
  return sortDirection.value === 'asc' ? 'ascending' : 'descending'
}

watch(() => props.group.id, () => {
  activeFilter.value = { kind: 'all' }
  sortField.value = 'health'
  sortDirection.value = 'asc'
  customSortActive.value = false
  expandedTargetId.value = ''
})

const formatNumber = (value: number | null | undefined): string => value == null ? '-' : String(value)
const formatMultiplier = (value: number | null | undefined): string => value == null ? '-' : `${value}x`

const multiplierDisplay = (account: AdminGroupAccount) =>
  resolveConnectionHealthMultiplierDisplay(account, props.group.multiplier)

const effectiveMultiplier = (account: AdminGroupAccount): number | null => multiplierDisplay(account).value

const multiplierSourceLabel = (account: AdminGroupAccount): string => {
  return t(`${detailPrefix}.multiplierSources.${multiplierDisplay(account).sourceKey}`)
}

const prioritySyncBlockReasonLabel = (account: AdminGroupAccount): string => {
  const message = resolvePrioritySyncBlockReasonMessage(
    account.prioritySyncBlockReason,
    account.upstreamKeyGroupName,
    account.upstreamKeyGroupId,
  )
  return t(prioritySyncBlockReasonTranslationKey(message.key), message.params)
}

</script>

<template>
  <section class="min-w-0" :aria-label="group.name">
    <header class="flex flex-col gap-4 border-b border-border/50 px-5 py-5 lg:flex-row lg:items-start lg:justify-between">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="truncate text-xl font-semibold text-foreground">{{ group.name }}</h2>
          <span class="rounded-md bg-surface px-2 py-0.5 text-xs text-muted-foreground">{{ group.platform || '-' }}</span>
          <span
            class="rounded-md px-2 py-0.5 text-xs font-medium"
            :class="group.status === 'active' || group.status === '1'
              ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
              : 'bg-muted text-muted-foreground'"
          >
            {{ t(`${prefix}.groupStatusLabels.${group.status === 'active' || group.status === '1' ? 'active' : 'inactive'}`) }}
          </span>
          <span v-if="group.priorityMode === 'multiplier'" class="inline-flex items-center gap-1 rounded-md bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
            <ArrowDownUp class="h-3 w-3" />
            {{ t(`${detailPrefix}.multiplierPriority`) }}
          </span>
        </div>
        <p class="mt-1 text-sm text-muted-foreground">
          {{ t(`${detailPrefix}.subtitle`, { monitored: monitoredCount, total: group.accountCount }) }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <label class="inline-flex h-9 shrink-0 cursor-pointer items-center gap-2 rounded-lg border border-border/60 bg-background px-3 text-xs text-foreground">
          <input
            type="checkbox"
            class="h-4 w-4 rounded border-border accent-primary"
            :checked="hideUnmonitoredAccounts"
            @change="emit('update:hide-unmonitored-accounts', ($event.target as HTMLInputElement).checked)"
          >
          {{ t(`${detailPrefix}.filters.hideUnmonitored`) }}
        </label>
        <button
          type="button"
          class="inline-flex h-9 shrink-0 items-center justify-center gap-2 rounded-lg bg-primary px-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          @click="emit('setup', group)"
        >
          <Settings2 class="h-4 w-4" />
          {{ t(`${detailPrefix}.${group.hasAssignedPolicy ? 'manageMonitoring' : 'enableMonitoring'}`) }}
        </button>
      </div>
    </header>

    <div class="grid border-b border-border/50 sm:grid-cols-2 xl:grid-cols-4">
      <button
        type="button"
        class="border-b border-border/50 px-5 py-4 text-left transition-colors hover:bg-surface/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary sm:border-r xl:border-b-0"
        :class="isFilterActive({ kind: 'all' }) ? 'bg-primary/[0.05]' : ''"
        :aria-pressed="isFilterActive({ kind: 'all' })"
        @click="applyFilter({ kind: 'all' })"
      >
        <span class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Radar class="h-3.5 w-3.5" />{{ t(`${detailPrefix}.metrics.accounts`) }}</span>
        <span class="mt-1 block text-xl font-semibold tabular-nums text-foreground">{{ group.accountCount }}</span>
      </button>
      <button
        type="button"
        class="border-b border-border/50 px-5 py-4 text-left transition-colors hover:bg-surface/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary xl:border-b-0 xl:border-r"
        :class="isFilterActive({ kind: 'monitored' }) ? 'bg-primary/[0.05]' : ''"
        :aria-pressed="isFilterActive({ kind: 'monitored' })"
        @click="applyFilter({ kind: 'monitored' })"
      >
        <span class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><ShieldCheck class="h-3.5 w-3.5" />{{ t(`${detailPrefix}.metrics.monitored`) }}</span>
        <span class="mt-1 block text-xl font-semibold tabular-nums text-foreground">{{ monitoredCount }}</span>
      </button>
      <button
        type="button"
        class="border-b border-border/50 px-5 py-4 text-left transition-colors hover:bg-surface/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary sm:border-b-0 sm:border-r"
        :class="isFilterActive({ kind: 'probeable' }) ? 'bg-primary/[0.05]' : ''"
        :aria-pressed="isFilterActive({ kind: 'probeable' })"
        @click="applyFilter({ kind: 'probeable' })"
      >
        <span class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Gauge class="h-3.5 w-3.5" />{{ t(`${detailPrefix}.metrics.probeable`) }}</span>
        <span class="mt-1 block text-xl font-semibold tabular-nums text-foreground">{{ group.healthSummary.probeableAccounts }}</span>
      </button>
      <div class="px-5 py-4">
        <span class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground"><Clock3 class="h-3.5 w-3.5" />{{ t(`${detailPrefix}.metrics.lastProbe`) }}</span>
        <span class="mt-1 block text-sm font-medium text-foreground">{{ formatConnectionHealthTime(lastProbeAt) }}</span>
      </div>
    </div>

    <div v-if="(group.priorityConflictCount ?? 0) > 0" class="flex items-start gap-2 border-b border-amber-500/25 bg-amber-500/[0.07] px-5 py-3 text-sm text-amber-700 dark:text-amber-400">
      <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
      <div class="min-w-0">
        <p>{{ t(`${detailPrefix}.${group.accounts.some(isSub2API) ? 'sub2apiPriorityConflict' : 'priorityConflict'}`, { count: group.priorityConflictCount ?? 0 }) }}</p>
        <ul v-if="group.priorityConflicts?.length" class="mt-1 space-y-0.5 text-xs">
          <li v-for="conflict in group.priorityConflicts" :key="conflict.targetId">
            {{ t(`${detailPrefix}.priorityConflictTarget`, {
              target: conflict.accountName || conflict.targetId,
              current: conflict.currentPriority ?? '-',
              expected: conflict.expectedPriority ?? '-',
              time: formatConnectionHealthTime(conflict.conflictAt ?? null),
            }) }}
          </li>
        </ul>
      </div>
    </div>

    <section class="border-b border-border/50 bg-surface/20 px-5 py-4" :aria-label="t(`${detailPrefix}.statusBreakdown.title`)">
      <div class="flex flex-col gap-1 sm:flex-row sm:items-baseline sm:justify-between sm:gap-4">
        <h3 class="text-xs font-semibold text-foreground">{{ t(`${detailPrefix}.statusBreakdown.title`) }}</h3>
        <p class="text-xs leading-5 text-muted-foreground">{{ t(`${detailPrefix}.statusBreakdown.hint`) }}</p>
      </div>
      <div class="mt-3 grid grid-cols-2 divide-x divide-y divide-border/50 overflow-hidden rounded-lg border border-border/50 bg-background sm:grid-cols-4 xl:grid-cols-8 xl:divide-y-0">
        <button
          v-for="item in stateBreakdown"
          :key="item.key"
          type="button"
          class="min-w-0 px-3 py-2.5 text-left transition-colors hover:bg-surface/45 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary"
          :class="isFilterActive(item.filter) ? 'bg-primary/[0.06]' : ''"
          :aria-pressed="isFilterActive(item.filter)"
          @click="applyFilter(item.filter)"
        >
          <span class="block truncate text-[11px] font-medium" :class="item.tone">{{ t(`${detailPrefix}.statusBreakdown.${item.key}`) }}</span>
          <span class="mt-0.5 block text-lg font-semibold tabular-nums text-foreground">{{ item.count }}</span>
        </button>
      </div>
    </section>

    <section v-if="group.modelSupply?.items.length" data-testid="model-control-supply" class="border-b border-border/50 bg-surface/20 px-5 py-4">
      <h3 class="text-xs font-semibold">{{ modelMessage('supplyTitle') }}</h3><p class="mt-1 text-xs leading-5 text-muted-foreground">{{ modelMessage('supplyHint') }}<template v-if="group.modelSupply.otherTypeAccounts">。{{ modelMessage('supplyOther', { count: group.modelSupply.otherTypeAccounts }) }}</template></p>
      <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4"><Tooltip v-for="item in visibleModelSupply" :key="item.modelName" :text="modelControlSupplyTooltip(item)" wide><div :data-testid="`model-control-supply-${item.modelName}`" class="w-full min-w-0 rounded-lg border bg-background px-3 py-2.5" :class="item.open === 0 && item.unknown === 0 ? 'border-red-500/40' : item.open <= 1 ? 'border-amber-500/40' : 'border-border/50'"><p class="break-all text-xs font-medium">{{ item.modelName }}</p><p class="mt-1 flex flex-wrap gap-x-3 text-[11px] text-muted-foreground">{{ modelMessage('countOpen') }} <strong class="text-lg font-semibold tabular-nums text-foreground">{{ item.open }}</strong> {{ modelMessage('countClosed') }} <strong class="text-lg font-semibold tabular-nums" :class="item.closed ? 'text-red-600 dark:text-red-400' : 'text-foreground'">{{ item.closed }}</strong></p><p v-if="item.open <= 1" class="mt-1 text-xs" :class="item.open === 0 && item.unknown === 0 ? 'text-red-600 dark:text-red-400' : 'text-amber-600 dark:text-amber-400'">{{ modelMessage(item.open === 1 ? 'supplySingle' : item.unknown ? 'supplyUncertain' : 'supplyNone') }}</p><p v-if="item.unknown" class="mt-1 text-[11px] text-muted-foreground">{{ modelMessage('supplyUnknown', { count: item.unknown }) }}</p></div></Tooltip></div>
      <button v-if="group.modelSupply.items.length > 8" type="button" class="mt-2 text-xs text-muted-foreground underline" @click="modelSupplyExpanded = !modelSupplyExpanded">{{ modelSupplyExpanded ? modelMessage('supplyCollapse') : modelMessage('supplyExpand', { count: group.modelSupply.items.length }) }}</button>
    </section>

    <div class="px-5 py-5">
      <p v-if="group.modelControlError || sortedAccounts.some(account => modelControlFailures.includes(account.targetId))" data-testid="model-control-summary-error" role="alert" class="mb-3 text-xs text-destructive">{{ t('admin.connectionHealth.modelControl.text.summaryError') }}</p>
      <p class="mb-3 text-xs text-muted-foreground">
        {{ customSortActive ? t(`${detailPrefix}.temporarySortHint`) : t(`${detailPrefix}.${group.accounts.some(isSub2API) ? 'sub2apiProductionSortHint' : 'productionSortHint'}`) }}
      </p>
      <div v-if="activeFilter.kind !== 'all'" class="mb-3 flex items-center justify-between gap-3 rounded-lg border border-primary/20 bg-primary/[0.06] px-3 py-2 text-xs text-primary">
        <span>{{ t(`${detailPrefix}.filters.active`, { label: filterLabel }) }}</span>
        <button
          type="button"
          class="inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-primary transition-colors hover:bg-primary/10"
          @click="clearFilter"
        >
          <X class="h-3.5 w-3.5" />
          {{ t(`${detailPrefix}.filters.clear`) }}
        </button>
      </div>
      <div v-if="group.accountsError" class="rounded-lg bg-destructive/10 px-4 py-3 text-sm text-destructive">
        {{ readableMessage(group.accountsError) }}
      </div>
      <div v-else-if="group.accounts.length === 0" class="flex min-h-64 flex-col items-center justify-center text-center">
        <Activity class="h-8 w-8 text-muted-foreground/40" />
        <p class="mt-3 text-sm text-muted-foreground">{{ t(`${detailPrefix}.empty`) }}</p>
      </div>
      <div v-else class="overflow-x-auto rounded-lg border border-border/60">
        <table class="w-full min-w-[72rem] text-sm">
          <thead class="bg-surface/60 text-left text-xs text-muted-foreground">
            <tr>
              <th class="relative w-10 px-3 py-2.5 font-medium"><span class="sr-only">{{ t(`${detailPrefix}.columns.expand`) }}</span></th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('account')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('account')">
                  {{ t(`${detailPrefix}.columns.account`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'account' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'account'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('health')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('health')">
                  {{ t(`${detailPrefix}.columns.health`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'health' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'health'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('strategy')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('strategy')">
                  {{ t(`${detailPrefix}.columns.strategy`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'strategy' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'strategy'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('priority')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('priority')">
                  {{ t(`${detailPrefix}.columns.priority`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'priority' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'priority'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('effectiveMultiplier')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('effectiveMultiplier')">
                  {{ t(`${detailPrefix}.columns.strategyMultiplier`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'effectiveMultiplier' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'effectiveMultiplier'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="px-3 py-2.5 font-medium" :aria-sort="ariaSort('latency')">
                <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-foreground" @click="toggleSort('latency')">
                  {{ t(`${detailPrefix}.columns.latency`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'latency' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'latency'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="w-40 px-3 py-2.5 text-right font-medium" :aria-sort="ariaSort('stability')">
                <button type="button" class="inline-flex items-center justify-end gap-1.5 text-right hover:text-foreground" @click="toggleSort('stability')">
                  {{ t(`${detailPrefix}.columns.stability`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'stability' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'stability'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="w-28 px-3 py-2.5 text-right font-medium" :aria-sort="ariaSort('latestAccuracy')">
                <button type="button" class="inline-flex items-center justify-end gap-1.5 text-right hover:text-foreground" @click="toggleSort('latestAccuracy')">
                  {{ t(`${detailPrefix}.columns.latestAccuracy`) }}
                  <ChevronUp v-if="customSortActive && sortField === 'latestAccuracy' && sortDirection === 'asc'" class="h-3.5 w-3.5" />
                  <ChevronDown v-else-if="customSortActive && sortField === 'latestAccuracy'" class="h-3.5 w-3.5" />
                  <ArrowDownUp v-else class="h-3.5 w-3.5 opacity-50" />
                </button>
              </th>
              <th class="w-28 px-3 py-2.5 text-right font-medium">{{ t(`${detailPrefix}.columns.actions`) }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="sortedAccounts.length === 0">
              <td colspan="10" class="px-4 py-12 text-center text-sm text-muted-foreground">
                {{ t(`${detailPrefix}.filters.noMatches`) }}
              </td>
            </tr>
            <template v-else v-for="account in sortedAccounts" :key="account.targetId">
              <tr class="border-t border-border/40 transition-colors hover:bg-surface/35">
                <td class="px-3 py-3">
                  <button
                    type="button"
                    class="rounded p-1 text-muted-foreground hover:bg-surface hover:text-foreground"
                    :aria-label="t(`${detailPrefix}.columns.expand`)"
                    @click="toggleModels(account.targetId)"
                  >
                    <ChevronDown v-if="expandedTargetId === account.targetId" class="h-4 w-4" />
                    <ChevronRight v-else class="h-4 w-4" />
                  </button>
                </td>
                <td class="px-3 py-3">
                  <div class="max-w-56">
                    <p class="truncate font-medium text-foreground">{{ account.name || account.id }}</p>
                    <AccountTierEditor
                      v-if="isSub2API(account)"
                      :target-id="account.targetId"
                      :account-tier="account.accountTier"
                      @saved="emit('tier-saved', $event)"
                    />
                    <AccountConcurrencyEditor v-if="isSub2API(account)" :account="account" @saved="emit('concurrency-saved', $event)" />
                    <p class="mt-0.5 truncate text-xs text-muted-foreground">
                      {{ account.platform || account.type || '-' }} · {{ upstreamStatusLabel(account) }} · {{ schedulableLabel(account) }}
                    </p>
                    <p v-if="hasMainSiteError(account)" class="mt-0.5 flex items-center gap-1 truncate text-xs font-medium text-destructive">
                      <AlertTriangle class="h-3 w-3 shrink-0" />
                      {{ t(`${detailPrefix}.mainSiteError`, { reason: mainSiteErrorReason(account) }) }}
                    </p>
                    <p v-for="restriction in mainSiteRestrictions(account)" :key="restriction" data-main-site-restriction class="mt-1 whitespace-normal break-words text-xs text-amber-700 dark:text-amber-400">{{ restriction }}</p>
                    <p class="mt-0.5 truncate text-[11px] text-muted-foreground">
                      {{ t(`${detailPrefix}.statusSources`, { upstream: statusSourceLabel(account.upstreamStatusSource), health: statusSourceLabel(account.healthStatusSource), schedulable: statusSourceLabel(account.schedulableSource) }) }}
                    </p>
                    <p v-if="account.remoteActionPending" class="mt-1 whitespace-pre-wrap break-words text-xs text-amber-700 dark:text-amber-400">
                      {{ remotePendingLabel(account) }}
                    </p>
                    <p v-if="account.testConfiguration" class="mt-1 whitespace-normal break-words text-xs text-muted-foreground">
                      {{ t('admin.connectionHealth.testConfiguration.' + account.testConfiguration.status) }}
                      <template v-if="!isTestConfigurationBlocked(account.testConfiguration)"> · {{ testProtocolName(account.testConfiguration.protocol) }} / {{ account.testConfiguration.probeTimeoutSeconds }}s</template>
                      <span v-if="account.testConfiguration.sourceGroups.length"> · {{ account.testConfiguration.sourceGroups.map(source => `${source.adminGroupName || source.adminGroupId}: ${testProtocolName(source.protocol)} / ${source.probeTimeoutSeconds}s`).join('；') }}</span>
                    </p>
                    <p v-if="account.schedulableChangedAt" class="mt-0.5 truncate text-[11px] text-muted-foreground">{{ t(`${detailPrefix}.schedulableChangedAt`, { time: formatConnectionHealthTime(account.schedulableChangedAt) }) }}</p>
                    <p v-if="account.lastSchedulableAction" class="mt-0.5 max-w-72 truncate text-[11px] text-muted-foreground" :title="lastSchedulableActionLabel(account)">{{ lastSchedulableActionLabel(account) }}</p>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <div class="flex flex-col items-start gap-1">
                    <span v-if="!account.hasEnabledProbePolicy" class="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
                      <ShieldQuestion class="h-3 w-3" />{{ t(`${detailPrefix}.notApplicable`) }}
                    </span>
                    <span v-else-if="!account.probeAvailable" class="inline-flex items-center gap-1 rounded-md bg-amber-500/10 px-2 py-1 text-xs font-medium text-amber-600 dark:text-amber-400">
                      <AlertTriangle class="h-3 w-3" />{{ t(`${detailPrefix}.unprobeable`) }}
                    </span>
                    <span v-else-if="account.probeModelsConfigured === false" class="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
                      <Settings2 class="h-3 w-3" />{{ t(`${prefix}.notConfigured`) }}
                    </span>
                    <Tooltip v-else-if="accountProtocolUnverified(account)" :text="t('admin.connectionHealth.testConfiguration.unverifiedHelp')" wide>
                      <span class="rounded-md bg-amber-500/10 px-2 py-1 text-xs text-amber-700 dark:text-amber-400">{{ t('admin.connectionHealth.testConfiguration.unverified') }}</span>
                    </Tooltip>
                    <span v-else-if="!aggregateState(account)" class="inline-flex items-center gap-1 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
                      <ShieldQuestion class="h-3 w-3" />{{ t(`${prefix}.notProbed`) }}
                    </span>
                    <span v-else class="inline-flex items-center rounded-md px-2 py-1 text-xs font-medium" :class="connectionHealthStateBadgeClass(aggregateState(account))">
                      {{ t(`${prefix}.stateLabels.${aggregateState(account)}`) }}
                      <span v-if="account.modelHealth.length > 1" class="ml-1 opacity-70">×{{ account.modelHealth.length }}</span>
                    </span>
                    <span v-if="unprobedModels(account).length > 0 && aggregateState(account)" class="text-[11px] text-muted-foreground">
                      {{ t(`${prefix}.notProbed`) }} ×{{ unprobedModels(account).length }}
                    </span>
                  </div>
                </td>
                <td class="px-3 py-3">
                  <Tooltip :text="assignmentLabel(account)" wide>
                    <div class="max-w-52">
                      <p class="truncate text-xs font-medium" :class="monitoringEnabled(account) ? 'text-foreground' : 'text-muted-foreground'">{{ assignmentLabel(account) }}</p>
                      <p class="mt-0.5 text-[11px] text-muted-foreground">{{ strategyStateLabel(account) }}</p>
                    </div>
                  </Tooltip>
                </td>
                <td class="px-3 py-3 tabular-nums text-foreground">
                  <AccountPriorityEditor v-if="isSub2API(account)" :account="account" :tied="account.priority != null && group.accounts.some(other => other.targetId !== account.targetId && other.priority === account.priority)" @saved="emit('priority-saved', $event)" />
                  <div v-else class="flex items-center gap-1.5">
                    <span>{{ formatNumber(account.priority) }}</span>
                    <span v-if="account.priorityConflict" class="text-[11px] text-amber-600 dark:text-amber-400" :title="t(`${detailPrefix}.priorityConflictShort`)">{{ account.priorityConflictValue ?? account.priority }} → {{ account.priorityExpected ?? '-' }}</span>
                    <ArrowDownUp v-else-if="account.priorityManaged" class="h-3.5 w-3.5 text-primary" />
                  </div>
                  <span v-if="isSub2API(account) && account.priorityConflict" class="text-[11px] text-amber-600 dark:text-amber-400" :title="t(`${detailPrefix}.sub2apiPriorityConflictShort`)">{{ account.priorityConflictValue ?? account.priority }} → {{ account.priorityExpected && account.priorityExpected > 0 ? account.priorityExpected : '-' }}</span>
                  <p v-if="!isSub2API(account)" class="mt-0.5 text-[11px] text-muted-foreground">{{ priorityStateLabel(account) }}</p>
                  <p v-if="account.prioritySyncBlocked" class="mt-1 flex max-w-72 items-start gap-1 text-[11px] leading-4 text-amber-600 dark:text-amber-400">
                    <AlertTriangle class="mt-0.5 h-3 w-3 shrink-0" />
                    <span class="break-words">{{ prioritySyncBlockReasonLabel(account) }}</span>
                  </p>
                </td>
                <td class="px-3 py-3 tabular-nums text-foreground">
                  <span>{{ formatMultiplier(effectiveMultiplier(account)) }}</span>
                  <span class="mt-0.5 block max-w-40 text-[11px] leading-4 text-muted-foreground">
                    {{ multiplierSourceLabel(account) }}
                  </span>
                </td>
                <td class="px-3 py-3 tabular-nums">
                  <span v-if="accountLatency(account) == null" class="text-muted-foreground">-</span>
                  <span v-else :class="accountHasSlowResponse(account) ? 'text-amber-600 dark:text-amber-400' : 'text-foreground'">
                    {{ accountLatency(account) }} ms
                  </span>
                  <span v-if="accountMeasurement(account, 'firstTokenMs') != null" class="mt-0.5 block text-[11px] text-muted-foreground">首字：{{ accountMeasurement(account, 'firstTokenMs') }}ms</span>
                  <span v-if="accountMeasurement(account, 'firstEventMs') != null" class="mt-0.5 block text-[11px] text-muted-foreground">首个事件：{{ accountMeasurement(account, 'firstEventMs') }}ms</span>
                  <span v-if="accountHasSlowResponse(account)" class="mt-0.5 block text-[11px] text-amber-600 dark:text-amber-400">
                    {{ accountResponseLabel(account) }}
                  </span>
                </td>
                <td class="w-40 px-3 py-3 pl-8 text-right tabular-nums">
                  <div class="ml-auto max-w-32">
                    <span v-if="!hasStabilityReading(account)" class="text-muted-foreground">-</span>
                    <span v-else class="text-foreground">{{ accountHealthWeight(account) ?? '-' }}%</span>
                    <span
                      class="mt-0.5 block truncate text-xs text-muted-foreground"
                      :title="hasStabilityReading(account) ? accountStabilitySubLabel(account) : ''"
                    >
                      {{ hasStabilityReading(account) ? accountStabilitySubLabel(account) : t(`${detailPrefix}.stabilityColumn.notProbed`) }}
                    </span>
                  </div>
                </td>
                <td class="w-28 px-3 py-3 text-right tabular-nums">
                  <button v-if="account.recentQuestionAnswer" type="button" class="text-right hover:text-primary" :class="accountLatestAccuracy(account) == null ? 'text-muted-foreground' : 'font-medium text-foreground'" :title="account.recentQuestionAnswer.scheduleName ?? recentSource(account)" @click="emit('question-answer-view', { targetId: account.targetId, batchId: account.recentQuestionAnswer.batchId })">{{ formatLatestAccuracy(account) }}</button>
                  <span v-else class="text-muted-foreground">—</span>
                  <span v-if="account.recentQuestionAnswer" class="mt-0.5 block break-words text-[11px] text-muted-foreground">{{ recentSource(account) }} · {{ formatConnectionHealthTime(account.recentQuestionAnswer.createdAt) }}</span>
                  <span v-if="recentState(account)" class="mt-0.5 block text-[11px] text-muted-foreground">{{ recentState(account) }}</span>
                  <Tooltip v-if="account.modelControl" :text="modelControlSummaryTooltip(account.modelControl)" wide><button data-testid="model-control-summary" type="button" class="mt-1 text-left text-[11px] text-muted-foreground hover:text-primary" @click="emit('question-answer-view', { targetId: account.targetId })"><span>模型 开 {{ account.modelControl.open }} · <span :class="account.modelControl.closed > 0 ? 'text-red-600 dark:text-red-400' : ''">关 {{ account.modelControl.closed }}</span></span><span v-if="account.modelControl.attention > 0" class="block text-amber-600 dark:text-amber-400">{{ modelMessage('summaryAttention', { count: account.modelControl.attention }) }}</span></button></Tooltip>
                  <button v-if="recentSummaryFailures.includes(account.targetId)" type="button" class="mt-1 text-[11px] text-destructive underline" @click="emit('question-answer-recent-retry', account.targetId)">最近结果刷新失败 · 重试</button>
                </td>
                <td class="w-28 px-3 py-3">
                  <div class="account-actions flex flex-col items-end gap-1">
                    <div class="account-actions-primary flex items-center justify-end gap-1">
                      <Tooltip :text="t(`${detailPrefix}.actions.assignPolicy`)">
                      <button
                        type="button"
                        class="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-surface hover:text-primary"
                        :aria-label="t(`${detailPrefix}.actions.assignPolicy`)"
                        @click="emit('assign-policy', account)"
                      >
                        <ShieldCheck class="h-4 w-4" />
                      </button>
                      </Tooltip>
                      <Tooltip :text="hasUnreadQuestionAnswer(account) ? t(`${prefix}.actions.questionAnswerUnread`) : t(`${prefix}.actions.probe`)">
                      <button
                        type="button"
                        class="rounded-md p-1.5 transition-colors disabled:opacity-35"
                        :class="hasUnreadQuestionAnswer(account)
                          ? 'bg-amber-500 text-white shadow-sm ring-2 ring-amber-500/25 hover:bg-amber-600 hover:text-white dark:bg-amber-400 dark:text-zinc-950 dark:hover:bg-amber-300'
                          : 'text-muted-foreground hover:bg-surface hover:text-primary'"
                        :aria-label="hasUnreadQuestionAnswer(account) ? t(`${prefix}.actions.questionAnswerUnread`) : t(`${prefix}.actions.probe`)"
                        :disabled="!canOpenManualProbeHistory(account)"
                        @click="emit('probe', account)"
                      >
                        <Zap class="h-4 w-4" />
                      </button>
                      </Tooltip>
                      <Tooltip :text="quickProbeLabel(account)">
                      <button
                        type="button"
                        class="rounded-md bg-primary p-1.5 text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-35"
                        :aria-label="quickProbeLabel(account)"
                        :disabled="quickProbeDisabled(account)"
                        @click="emitQuickProbe(account)"
                      >
                        <Loader2 v-if="quickProbePhases[account.targetId]" class="h-4 w-4 animate-spin" />
                        <Bolt v-else class="h-4 w-4" />
                      </button>
                      </Tooltip>
                    </div>
                    <div class="account-actions-secondary flex items-center justify-end gap-1">
                      <Tooltip v-if="isSub2API(account) && account.schedulable != null" :text="account.schedulable ? t(`${detailPrefix}.actions.disableScheduling`) : t(`${detailPrefix}.actions.enableScheduling`)" wide>
                      <button
                        type="button"
                        class="rounded-md p-1.5 transition-colors disabled:cursor-not-allowed disabled:opacity-35"
                        :class="account.schedulable
                          ? 'bg-emerald-600 text-white hover:bg-emerald-700 hover:text-white dark:bg-emerald-500 dark:text-white dark:hover:bg-emerald-400 dark:hover:text-white'
                          : 'text-muted-foreground hover:bg-surface hover:text-foreground'"
                        :aria-label="account.schedulable ? t(`${detailPrefix}.actions.disableScheduling`) : t(`${detailPrefix}.actions.enableScheduling`)"
                        :disabled="actionLoading"
                        @click="emit('set-schedulable', account)"
                      >
                        <Power class="h-4 w-4" />
                      </button>
                      </Tooltip>
                      <Tooltip :text="t(`${prefix}.actions.viewEvents`)">
                      <button
                        type="button"
                        class="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-surface hover:text-foreground"
                        :aria-label="t(`${prefix}.actions.viewEvents`)"
                        @click="emit('view-events', account)"
                      >
                        <Eye class="h-4 w-4" />
                      </button>
                      </Tooltip>
                    </div>
                  </div>
                </td>
              </tr>
              <tr v-if="quickProbeSuccesses[account.targetId]" class="quick-probe-success-row border-t border-emerald-500/20 bg-emerald-500/[0.06]">
                <td colspan="10" class="px-12 py-3 text-sm text-emerald-700 dark:text-emerald-400">
                  {{ t(`${detailPrefix}.quickProbe.completed`, {
                    model: quickProbeSuccesses[account.targetId].modelName,
                    latency: quickProbeSuccesses[account.targetId].latencyMs,
                  }) }}
                  · {{ testProtocolName(quickProbeSuccesses[account.targetId].protocol) || t('admin.connectionHealth.testConfiguration.legacy') }}<template v-if="quickProbeSuccesses[account.targetId].timeoutSeconds"> / {{ quickProbeSuccesses[account.targetId].timeoutSeconds }}s</template>
                </td>
              </tr>
              <tr v-if="quickProbeErrors[account.targetId]" class="quick-probe-error-row border-t border-destructive/20 bg-destructive/[0.06]">
                <td colspan="10" class="whitespace-pre-wrap break-words px-12 py-3 text-sm text-destructive">
                  {{ quickProbeErrors[account.targetId] }}
                </td>
              </tr>
              <tr v-if="expandedTargetId === account.targetId" class="border-t border-border/40 bg-surface/25">
                <td colspan="10" class="px-12 py-4">
                  <div v-if="filteredModelHealth(account).length === 0 && filteredUnprobedModels(account).length === 0" class="text-xs text-muted-foreground">{{ t(`${detailPrefix}.models.empty`) }}</div>
                  <div v-else class="grid gap-2 lg:grid-cols-2">
                    <div v-for="model in filteredModelHealth(account)" :key="model.modelName" class="rounded-lg border border-border/50 bg-background px-2.5 py-2">
                      <div class="flex items-center justify-between gap-3">
                        <span class="truncate text-sm font-medium text-foreground">{{ model.modelName }}</span>
                        <Tooltip v-if="model.currentHealthResult?.status === 'unverified'" :text="t('admin.connectionHealth.testConfiguration.unverifiedHelp')" wide>
                          <span class="text-xs text-amber-700">{{ t('admin.connectionHealth.testConfiguration.unverified') }}</span>
                        </Tooltip>
                        <span v-else class="rounded-md px-2 py-0.5 text-xs font-medium" :class="model.configured ? connectionHealthStateBadgeClass(model.state) : 'bg-muted text-muted-foreground'">{{ model.configured ? t(`${prefix}.stateLabels.${model.state}`) : t(`${prefix}.notConfigured`) }}</span>
                      </div>
                      <p v-if="model.currentHealthResult?.status === 'unverified'" class="mt-1 whitespace-normal break-words text-xs text-muted-foreground">{{ t('admin.connectionHealth.testConfiguration.unverifiedHelp') }}</p>
                      <div class="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                        <span v-if="model.firstTokenMs != null">首字：{{ model.firstTokenMs }}ms</span>
                        <span v-if="model.firstEventMs != null">首个事件：{{ model.firstEventMs }}ms</span>
                        <span :class="model.state === 'suspended' ? 'text-destructive' : ''">{{ t(`${detailPrefix}.models.latency`, { value: !model.configured || model.state === 'suspended' ? '-' : (model.lastLatencyMs ?? '-') }) }}</span>
                        <span>{{ t(`${detailPrefix}.models.lastProbe`, { value: formatConnectionHealthTime(model.lastProbeAt) }) }}</span>
                        <span>{{ t(`${detailPrefix}.models.weight`, { value: model.currentWeight }) }}</span>
                        <span v-if="model.nextProbeAt">{{ t(`${detailPrefix}.models.nextProbe`, { value: formatConnectionHealthTime(model.nextProbeAt) }) }}</span>
                        <span v-if="model.blockedReason">{{ blockedReasonLabel(model.blockedReason) }}</span>
                      </div>
                      <div v-if="hasValidConnectionHealthTime(model.lastFailureAt)" class="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs">
                        <span class="font-medium text-destructive">{{ t(`${detailPrefix}.models.lastFailure`, { value: formatConnectionHealthTime(model.lastFailureAt) }) }}</span>
                        <span v-if="formatConnectionHealthElapsed(model.elapsedSeconds, model.lastFailureAt)" class="font-medium text-destructive">{{ t(`${detailPrefix}.models.elapsed`, { value: formatConnectionHealthElapsed(model.elapsedSeconds, model.lastFailureAt) }) }}</span>
                      </div>
                      <div v-if="hasCurrentHealthFailure(model)" class="mt-1 whitespace-pre-wrap break-words text-xs text-destructive/80">
                        <p>{{ readableMessage(model.currentHealthResult?.errorKey || model.lastErrorKey) }}</p>
                        <p>{{ model.currentHealthResult?.errorDetail || model.lastErrorDetail }}</p>
                      </div>
                      <div v-if="model.lastAttempt" class="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">
                        <p>{{ testProtocolName(model.lastAttempt.protocol) || t('admin.connectionHealth.testConfiguration.legacy') }}<template v-if="model.lastAttempt.probeTimeoutSeconds"> / {{ model.lastAttempt.probeTimeoutSeconds }}s</template> · {{ formatConnectionHealthTime(model.lastAttempt.at ?? null) }}</p>
                        <p v-if="model.lastAttempt.disposition === 'invalid'">{{ t('admin.connectionHealth.testConfiguration.invalid') }} · {{ model.lastAttempt.errorDetail || (model.lastAttempt.errorKey ? readableMessage(model.lastAttempt.errorKey) : '') }}</p>
                      </div>
                      <p v-if="effectiveSourcesLabel(model)" class="mt-1 text-xs text-muted-foreground">{{ effectiveSourcesLabel(model) }}</p>
                    </div>
                    <div v-for="model in filteredUnprobedModels(account)" :key="`unprobed:${model.modelName}`" class="rounded-lg border border-border/50 bg-background px-2.5 py-2">
                      <div class="flex items-center justify-between gap-3">
                        <span class="truncate text-sm font-medium text-foreground">{{ model.modelName }}</span>
                        <span class="rounded-md bg-muted px-2 py-0.5 text-xs text-muted-foreground">{{ account.probeModelsConfigured === false ? t(`${prefix}.notConfigured`) : t(`${prefix}.notProbed`) }}</span>
                      </div>
                      <div class="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                        <span>{{ t(`${detailPrefix}.models.latency`, { value: '-' }) }}</span>
                        <span>{{ t(`${detailPrefix}.models.lastProbe`, { value: formatConnectionHealthTime(null) }) }}</span>
                        <span>{{ t(`${detailPrefix}.models.weight`, { value: '-' }) }}</span>
                        <span v-if="model.nextProbeAt">{{ t(`${detailPrefix}.models.nextProbe`, { value: formatConnectionHealthTime(model.nextProbeAt) }) }}</span>
                        <span v-if="model.blockedReason">{{ blockedReasonLabel(model.blockedReason) }}</span>
                      </div>
                      <p v-if="effectiveSourcesLabel(model)" class="mt-1 text-xs text-muted-foreground">{{ effectiveSourcesLabel(model) }}</p>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>

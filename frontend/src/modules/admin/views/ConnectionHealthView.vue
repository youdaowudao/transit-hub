<script setup lang="ts">
import { computed, onActivated, onMounted, ref, watch } from 'vue'
import { useDocumentVisibility, useIntervalFn } from '@vueuse/core'
import { useRoute, useRouter } from 'vue-router'
import {
  Activity,
  AlertTriangle,
  ArrowDownUp,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Gauge,
  Layers,
  Loader2,
  RefreshCw,
  Search,
  Settings2,
  ShieldCheck,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { getPrioritySyncStatus } from '../api/connectionHealth'
import { listUpstreamSites } from '../api/upstream'
import { connectionHealthMessageKey, useConnectionHealth } from '../composables/useConnectionHealth'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import AdminGroupHealthDetail from '../components/dashboard/AdminGroupHealthDetail.vue'
import ConnectionHealthEventsDialog from '../components/dashboard/ConnectionHealthEventsDialog.vue'
import GroupHealthSetupDrawer from '../components/dashboard/GroupHealthSetupDrawer.vue'
import ManualOneTimeProbeDialog from '../components/dashboard/ManualOneTimeProbeDialog.vue'
import type { ManualProbeTargetSummary } from '../components/dashboard/ManualOneTimeProbeDialog.vue'
import PolicyConfigDrawer from '../components/dashboard/PolicyConfigDrawer.vue'
import type { OwnGroupOption } from '../components/dashboard/PolicyConfigDrawer.vue'
import ProbePolicyListDialog from '../components/dashboard/ProbePolicyListDialog.vue'
import type {
  AdminGroupAccount,
  AdminGroupHealth,
	AdminGroupPolicyConfiguration,
  ConnectionHealthPolicy,
  PolicyInput,
	PrioritySyncStatus,
} from '../types/connectionHealth'
import { resolveConnectionHealthStrategyMode } from '../utils/connectionHealthPolicy'
import {
  clearQuestionAnswerUnread,
  createDefaultConnectionHealthPreferences,
  markQuestionAnswerUnread,
  mergeConnectionHealthGroupOrder,
  readConnectionHealthPreferences,
  type ConnectionHealthPreferences,
  writeConnectionHealthPreferences,
} from '../utils/connectionHealthPreferences'

import { t, te } from '@/locales'
const route = useRoute()
const router = useRouter()
const {
  overview,
  groups,
  adminGroups,
  events,
  policies,
  isLoading,
  isActionLoading,
  errorKey,
  loadAll,
  loadAdminGroups,
  loadEvents,
  loadPolicies,
  removePolicy,
  savePolicy,
  updateTargetSchedulable,
} = useConnectionHealth()
const { currentAccount } = useAdminAccounts()

const searchText = ref('')
const selectedType = ref('')
const selectedGroupId = ref('')
const focusedGroupId = ref('')
const selectedConnectionId = ref('')
const eventsDialogOpen = ref(false)
let eventsOpenRequestSequence = 0
const siteNameMap = ref<Map<string, string>>(new Map())
const preferences = ref<ConnectionHealthPreferences>(createDefaultConnectionHealthPreferences())
const groupManagerOpen = ref(false)
const prioritySyncStatus = ref<PrioritySyncStatus | null>(null)
const preferenceScope = computed(() => currentAccount.value?.id ?? 'anonymous')
let loadedPreferenceScope = ''

const groupTypes = ['public', 'exclusive', 'subscription']
const groupTypeLabel = (type: string): string => t(`admin.connectionHealth.groupTypes.${groupTypes.includes(type) ? type : 'public'}`)

const groupCostDisplay = (value: number | null | undefined): string => {
  if (value === null || value === undefined || !Number.isFinite(value)) return t('admin.connectionHealth.costUnknown')
  return t('admin.upstream.currency.cnyValue', { amount: value.toFixed(2) })
}

const groupCostReasonDisplay = (reason?: string): string => {
  switch (reason) {
    case 'shared_source': return '成本来源被多个分组共享，暂不归属'
    case 'unresolved_source': return '上游来源无法唯一确认'
    case 'sample_decode_failed': return '成本样本损坏，等待重新采样'
    case 'retained_sample': return '沿用最近一次已确认样本'
    case 'auth_401': return '上游认证失败，等待退避后重试'
    case 'auth_403': return '上游拒绝访问，等待退避后重试'
    case 'network': return '上游网络不可达，等待退避后重试'
    case 'sample_unavailable': return '当前没有可靠成本样本'
    default: return reason ? '成本暂不可确认' : ''
  }
}

const updatePreferences = (updater: (current: ConnectionHealthPreferences) => ConnectionHealthPreferences) => {
  const next = updater(preferences.value)
  preferences.value = next
  writeConnectionHealthPreferences(preferenceScope.value, next)
}

const loadPreferences = (scope: string) => {
  if (loadedPreferenceScope === scope) return
  preferences.value = readConnectionHealthPreferences(scope)
  loadedPreferenceScope = scope
}

watch(preferenceScope, loadPreferences, { immediate: true })

const filteredGroups = computed(() => {
  const keyword = searchText.value.trim().toLocaleLowerCase()
  return orderedGroups.value
    .filter(group => !preferences.value.hiddenGroupIds.includes(group.id))
    .filter((group) => {
      if (selectedType.value && group.type !== selectedType.value) return false
      if (!keyword) return true
      return group.name.toLocaleLowerCase().includes(keyword)
        || group.platform.toLocaleLowerCase().includes(keyword)
        || group.accounts.some((account) => (account.name || account.id).toLocaleLowerCase().includes(keyword))
    })
})

const emptyGroupMessage = computed(() => {
  if (adminGroups.value.length === 0 || searchText.value.trim() || selectedType.value) {
    return t('admin.connectionHealth.adminEmpty')
  }
  return t('admin.connectionHealth.groupDisplay.noVisibleGroups')
})

const groupMonitoringEnabled = (group: AdminGroupHealth): boolean =>
  group.hasEnabledProbePolicy ?? group.hasEnabledPolicy ?? group.assignedPolicies?.some((policy) => policy.enabled) ?? Boolean(group.hasAssignedPolicy)

const compareDefaultGroups = (first: AdminGroupHealth, second: AdminGroupHealth): number => {
  const firstRank = first.minProductionRank ?? null
  const secondRank = second.minProductionRank ?? null
  if (firstRank == null || secondRank == null) {
    if (firstRank != null) return -1
    if (secondRank != null) return 1
  } else if (firstRank !== secondRank) {
    return firstRank - secondRank
  }
  const monitoredDiff = Number(groupMonitoringEnabled(first)) - Number(groupMonitoringEnabled(second))
  if (monitoredDiff !== 0) return -monitoredDiff
  const nameDiff = first.name.localeCompare(second.name)
  return nameDiff !== 0 ? nameDiff : first.id.localeCompare(second.id)
}

const orderedGroups = computed(() => {
  const currentGroups = adminGroups.value
  const currentIds = currentGroups.map(group => group.id)
  const hasStoredOrder = preferences.value.groupOrder.some(id => currentIds.includes(id))
  if (!hasStoredOrder) return [...currentGroups].sort(compareDefaultGroups)

  const order = mergeConnectionHealthGroupOrder(preferences.value.groupOrder, currentIds)
  const groupMap = new Map(currentGroups.map(group => [group.id, group]))
  return order.flatMap(id => {
    const group = groupMap.get(id)
    return group ? [group] : []
  })
})

const sameStringArray = (first: string[], second: string[]): boolean =>
  first.length === second.length && first.every((value, index) => value === second[index])

watch(
  [() => adminGroups.value.map(group => group.id), preferenceScope],
  ([currentIds, scope]) => {
    if (scope === 'anonymous' || currentIds.length === 0) return
    if (!preferences.value.groupOrder.some(id => currentIds.includes(id))) return
    const mergedOrder = mergeConnectionHealthGroupOrder(preferences.value.groupOrder, currentIds)
    if (sameStringArray(mergedOrder, preferences.value.groupOrder)) return
    updatePreferences(current => ({ ...current, groupOrder: mergedOrder }))
  },
)

const selectedGroup = computed(() => filteredGroups.value.find(group => group.id === selectedGroupId.value) ?? filteredGroups.value[0] ?? null)

watch(filteredGroups, (nextGroups) => {
  if (nextGroups.some((group) => group.id === selectedGroupId.value)) return
  selectedGroupId.value = nextGroups[0]?.id ?? ''
}, { immediate: true })

const routeQueryValue = (value: unknown): string => {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : ''
  return typeof value === 'string' ? value : ''
}

const clearGroupFocusQuery = async () => {
  const query = { ...route.query }
  delete query.focusGroupId
  delete query.focusGroupName
  await router.replace({ query })
}

const consumeRouteGroupFocus = async () => {
  const focusId = routeQueryValue(route.query.focusGroupId).trim()
  const focusName = routeQueryValue(route.query.focusGroupName).trim()
  if (!focusId && !focusName) return
  if (adminGroups.value.length === 0) return

  const target = filteredGroups.value.find(group => (
    (focusId && group.id === focusId) || (!focusId && focusName && group.name === focusName)
  ))
  if (target) {
    selectedGroupId.value = target.id
    focusedGroupId.value = target.id
  }
  await clearGroupFocusQuery()
}

watch(
  [() => route.query.focusGroupId, () => route.query.focusGroupName, filteredGroups],
  () => { void consumeRouteGroupFocus() },
  { immediate: true },
)

const selectGroup = (groupId: string) => {
  selectedGroupId.value = groupId
  focusedGroupId.value = ''
}

const clearGroupHighlight = () => {
  focusedGroupId.value = ''
}

const monitoredGroupCount = computed(() => adminGroups.value.filter(groupMonitoringEnabled).length)
const conflictCount = computed(() => adminGroups.value.reduce((sum, group) => sum + (group.priorityConflictCount ?? 0), 0))
const readableMessage = (rawKey: string): string => t(connectionHealthMessageKey(rawKey, te))

const priorityFailureTime = computed(() => {
	const raw = prioritySyncStatus.value?.lastFailureAt
	if (!raw) return t('admin.connectionHealth.prioritySync.unknownTime')
	const parsed = new Date(raw)
	return Number.isNaN(parsed.getTime())
		? t('admin.connectionHealth.prioritySync.unknownTime')
		: parsed.toLocaleString('zh-CN', { hour12: false })
})

const loadPrioritySyncStatus = async () => {
	try {
		prioritySyncStatus.value = await getPrioritySyncStatus()
	} catch {
		// 同步状态是轻量辅助信息；读取失败不覆盖主列表的真实请求错误。
	}
}

const applySavedGroupConfiguration = (configuration?: AdminGroupPolicyConfiguration) => {
	if (!configuration) return
	const index = adminGroups.value.findIndex(group => group.id === configuration.adminGroupId)
	if (index < 0) return
	const current = adminGroups.value[index]
	const enabledPolicies = configuration.policies.filter(policy => policy.enabled)
	const excluded = new Set(configuration.excludedTargetIds)
	adminGroups.value[index] = {
		...current,
		assignedPolicyIds: [...configuration.policyIds],
		assignedPolicies: [...configuration.policies],
		hasAssignedPolicy: configuration.policyIds.length > 0,
		hasEnabledPolicy: enabledPolicies.length > 0,
		hasEnabledProbePolicy: enabledPolicies.some(policy => (policy.strategyMode ?? 'health_probe') === 'health_probe'),
		priorityMode: enabledPolicies.some(policy => policy.priorityMode === 'multiplier') ? 'multiplier' : 'none',
		probeSortFallbackMultiplier: configuration.probeSortFallbackMultiplier ?? null,
		excludedAccountCount: current.accounts.filter(account => excluded.has(account.targetId)).length,
		accounts: current.accounts.map(account => ({ ...account, excludedFromGroupPolicy: excluded.has(account.targetId) })),
	}
}

const loadSiteNames = async () => {
  try {
    const sites = await listUpstreamSites()
    siteNameMap.value = new Map(sites.map((site) => [site.id, site.name]))
  } catch {
    // 站点名称仅用于事件展示，失败时保留 ID，不阻塞健康主流程。
  }
}

const moveGroup = (groupId: string, offset: -1 | 1) => {
  const ids = orderedGroups.value.map(group => group.id)
  const index = ids.indexOf(groupId)
  const targetIndex = index + offset
  if (index < 0 || targetIndex < 0 || targetIndex >= ids.length) return
  const nextOrder = [...ids]
  ;[nextOrder[index], nextOrder[targetIndex]] = [nextOrder[targetIndex], nextOrder[index]]
  updatePreferences(current => ({ ...current, groupOrder: nextOrder }))
}

const toggleGroupVisibility = (groupId: string) => {
  updatePreferences((current) => {
    const hidden = new Set(current.hiddenGroupIds)
    if (hidden.has(groupId)) hidden.delete(groupId)
    else hidden.add(groupId)
    return { ...current, hiddenGroupIds: Array.from(hidden) }
  })
}

const setHideUnmonitoredAccounts = (value: boolean) => {
  updatePreferences(current => ({ ...current, hideUnmonitoredAccounts: value }))
}

let lastEntryRefreshAt = 0
const refreshOnEntry = () => {
  const now = Date.now()
  if (now - lastEntryRefreshAt < 500) return
  lastEntryRefreshAt = now
  void loadAll()
  void loadEvents()
  void loadPolicies()
  void loadSiteNames()
	void loadPrioritySyncStatus()
}

onMounted(refreshOnEntry)
onActivated(refreshOnEntry)

const documentVisibility = useDocumentVisibility()
let autoRefreshInFlight = false
const autoRefresh = async () => {
  if (documentVisibility.value !== 'visible' || autoRefreshInFlight || probeDialogOpen.value) return
  autoRefreshInFlight = true
  try {
    await Promise.all([loadAll({ silent: true }), loadEvents(selectedConnectionId.value || undefined), loadPrioritySyncStatus()])
  } finally {
    autoRefreshInFlight = false
  }
}
// immediate=false 会让 VueUse 的 interval 保持暂停；这里只关闭首次回调，计时器本身必须启动。
useIntervalFn(() => void autoRefresh(), 30_000, { immediate: true, immediateCallback: false })
watch(documentVisibility, (visibility) => {
  if (visibility === 'visible') void autoRefresh()
})

const refresh = async () => {
  await Promise.all([loadAll(), loadPolicies(), loadEvents(), loadPrioritySyncStatus()])
}

const siteName = (siteId: string): string => siteNameMap.value.get(siteId) ?? siteId

// 分组启用/管理抽屉。
const setupDrawerOpen = ref(false)
const setupGroup = ref<AdminGroupHealth | null>(null)

const openSetup = (group: AdminGroupHealth) => {
  setupGroup.value = group
  setupDrawerOpen.value = true
}

const onSetupSaved = async (configuration?: AdminGroupPolicyConfiguration) => {
  setupDrawerOpen.value = false
	applySavedGroupConfiguration(configuration)
	if (configuration?.prioritySyncStatus === 'pending' && currentAccount.value?.id) {
		prioritySyncStatus.value = { workspaceId: currentAccount.value.id, status: 'pending', failedCount: 0 }
	}
	await Promise.all([loadPolicies(), loadPrioritySyncStatus()])
}

// 手动探活弹窗同时承载正式探活和隔离的一次性测试。
const probeDialogOpen = ref(false)
const probeDialogTarget = ref<ManualProbeTargetSummary | null>(null)

const onProbeAccount = (account: AdminGroupAccount) => {
  if (!selectedGroup.value || !account.probeAvailable) return
  const formalModelMap = new Map<string, { id: string; name: string; providerFamily?: string }>()
  if (account.hasEnabledProbePolicy) {
    for (const model of [...(account.modelHealth ?? []), ...(account.unprobedModels ?? [])]) {
      formalModelMap.set(model.modelName, {
        id: model.modelName,
        name: model.modelName,
        providerFamily: model.providerFamily,
      })
    }
  }
  probeDialogTarget.value = {
    targetId: account.targetId,
    accountName: account.name || account.id,
    platform: selectedGroup.value.platform,
    type: account.type,
    status: account.status,
    groupName: selectedGroup.value.name,
    formalModels: Array.from(formalModelMap.values()),
  }
  probeDialogOpen.value = true
}

const onFormalProbeCompleted = async () => {
  await loadAdminGroups({ silent: true })
}

const onQuestionAnswerStarted = (targetId: string) => {
  updatePreferences(current => markQuestionAnswerUnread(current, targetId))
}

const onQuestionAnswerViewed = (targetId: string) => {
  updatePreferences(current => clearQuestionAnswerUnread(current, targetId))
}

// 策略探活事件。
const openAllEvents = async () => {
  const requestSequence = ++eventsOpenRequestSequence
  const previousConnectionId = selectedConnectionId.value
  selectedConnectionId.value = ''
  const eventsLoaded = await loadEvents()
  if (requestSequence !== eventsOpenRequestSequence) return
  if (eventsLoaded) eventsDialogOpen.value = true
  else selectedConnectionId.value = previousConnectionId
}

const onViewEventsAccount = async (account: AdminGroupAccount) => {
  const requestSequence = ++eventsOpenRequestSequence
  const previousConnectionId = selectedConnectionId.value
  selectedConnectionId.value = account.targetId
  const eventsLoaded = await loadEvents(account.targetId)
  if (requestSequence !== eventsOpenRequestSequence) return
  if (eventsLoaded) eventsDialogOpen.value = true
  else selectedConnectionId.value = previousConnectionId
}

const onSetTargetSchedulable = async (account: AdminGroupAccount) => {
  if (!account.targetId || !account.targetId.toLowerCase().startsWith('sub2api:') || account.schedulable == null) return
  const action = account.schedulable ? '关闭主站调度' : '恢复主站调度'
  if (!confirm(`确认对 ${account.targetId} 执行「${action}」？`)) return
  await updateTargetSchedulable(account.targetId, !account.schedulable)
}

const showAllEvents = async () => {
  const requestSequence = ++eventsOpenRequestSequence
  const eventsLoaded = await loadEvents()
  if (requestSequence !== eventsOpenRequestSequence) return
  if (eventsLoaded) selectedConnectionId.value = ''
}

// 高级策略列表/编辑继续保留，但退出首次主流程。
const policyListDialogOpen = ref(false)
const policyDrawerOpen = ref(false)
const editingPolicy = ref<ConnectionHealthPolicy | null>(null)
const deletingPolicyId = ref('')
const deletePolicyError = ref('')
const ownGroupOptions = computed<OwnGroupOption[]>(() => groups.value.map((group) => ({ id: group.ownGroupId, name: group.ownGroupName || group.ownGroupId })))

const openCreatePolicy = () => {
  editingPolicy.value = null
  policyDrawerOpen.value = true
}

const openEditPolicy = (policy: ConnectionHealthPolicy) => {
  editingPolicy.value = policy
  policyDrawerOpen.value = true
}

const handleSavePolicy = async (input: PolicyInput) => {
  if (await savePolicy(input)) {
    policyDrawerOpen.value = false
    await loadAll({ silent: true })
  }
}

const togglePolicyEnabled = async (policy: ConnectionHealthPolicy) => {
  await savePolicy({
    id: policy.id,
    name: policy.name,
    enabled: !policy.enabled,
    ownGroupId: policy.ownGroupId,
    ownGroupName: policy.ownGroupName,
    probeIntervalSeconds: policy.probeIntervalSeconds,
    failureThreshold: policy.failureThreshold,
    successThreshold: policy.successThreshold,
    cooldownSeconds: policy.cooldownSeconds,
    observationSeconds: policy.observationSeconds,
    recoveryStepPercent: policy.recoveryStepPercent,
    dailyProbeBudget: policy.dailyProbeBudget,
    continueProbeWhenUnschedulable: policy.continueProbeWhenUnschedulable,
    unschedulableProbeIntervalMinutes: policy.unschedulableProbeIntervalMinutes,
    autoDegradeEnabled: policy.autoDegradeEnabled,
    autoRemoteActionEnabled: policy.autoRemoteActionEnabled,
    priorityMode: policy.priorityMode ?? 'none',
    strategyMode: resolveConnectionHealthStrategyMode(policy),
    modelTargets: policy.modelTargets.map((model) => ({
      id: model.id,
      modelName: model.modelName,
      providerFamily: model.providerFamily,
      enabled: model.enabled,
      probePrompt: model.probePrompt,
      maxProbeTokens: model.maxProbeTokens,
    })),
  })
  await loadAll({ silent: true })
}

const handleDeletePolicy = async (policy: ConnectionHealthPolicy) => {
  if (deletingPolicyId.value) return
  deletingPolicyId.value = policy.id
  deletePolicyError.value = ''
  try {
    if (await removePolicy(policy.id)) {
      await loadAll({ silent: true })
    } else {
      deletePolicyError.value = readableMessage(errorKey.value)
    }
  } finally {
    deletingPolicyId.value = ''
  }
}
</script>

<template>
  <div class="space-y-5" @click.capture="clearGroupHighlight">
    <header class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold text-foreground">{{ t('admin.connectionHealth.title') }}</h1>
        <p class="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">{{ t('admin.connectionHealth.simplifiedSubtitle') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="secondary" size="sm" @click="policyListDialogOpen = true">
          <Settings2 class="h-4 w-4" />
          {{ t('admin.connectionHealth.topActions.policies') }}
        </Button>
        <Button variant="secondary" size="sm" @click="openAllEvents">
          <Activity class="h-4 w-4" />
          {{ t('admin.connectionHealth.topActions.events') }}
        </Button>
        <Button variant="secondary" size="sm" :disabled="isLoading" @click="refresh">
          <Loader2 v-if="isLoading" class="h-4 w-4 animate-spin" />
          <RefreshCw v-else class="h-4 w-4" />
          {{ t('admin.connectionHealth.refresh') }}
        </Button>
      </div>
    </header>

		<div
			v-if="prioritySyncStatus?.status === 'failed'"
			class="flex items-start gap-3 rounded-lg border border-destructive/20 bg-destructive/10 px-4 py-3 text-sm text-destructive"
			role="alert"
			aria-live="assertive"
		>
			<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
			<p>
				{{ t('admin.connectionHealth.prioritySync.failed', {
					workspace: prioritySyncStatus.workspaceId,
					time: priorityFailureTime,
					count: prioritySyncStatus.failedCount,
					reason: readableMessage(prioritySyncStatus.errorKey ?? ''),
				}) }}
			</p>
		</div>
		<div
			v-else-if="prioritySyncStatus?.status === 'pending' || prioritySyncStatus?.status === 'running'"
			class="flex items-center gap-2 rounded-lg border border-border/60 bg-muted/40 px-4 py-3 text-sm text-muted-foreground"
			aria-live="polite"
		>
			<Loader2 class="h-4 w-4 shrink-0 animate-spin" />
			{{ t('admin.connectionHealth.prioritySync.pending', { workspace: prioritySyncStatus.workspaceId }) }}
		</div>

    <!-- 汇总与主列表使用同一 admin target 数据源。 -->
    <section class="overflow-hidden rounded-lg border border-border/60 bg-card" :aria-label="t('admin.connectionHealth.summaryLabel')">
      <dl class="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-6">
        <div class="border-b border-r border-border/50 px-4 py-3 xl:border-b-0">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.summary.total') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ overview?.totalConnections ?? 0 }}</dd>
        </div>
        <div class="border-b border-border/50 px-4 py-3 sm:border-r xl:border-b-0">
          <dt class="flex items-center gap-1 text-xs font-medium text-emerald-600 dark:text-emerald-400"><CheckCircle2 class="h-3.5 w-3.5" />{{ t('admin.connectionHealth.stateLabels.healthy') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ overview?.healthy ?? 0 }}</dd>
        </div>
        <div class="border-b border-r border-border/50 px-4 py-3 xl:border-b-0">
          <dt class="flex items-center gap-1 text-xs font-medium text-amber-600 dark:text-amber-400"><AlertTriangle class="h-3.5 w-3.5" />{{ t('admin.connectionHealth.stateLabels.degraded') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ overview?.degraded ?? 0 }}</dd>
        </div>
        <div class="border-b border-border/50 px-4 py-3 sm:border-r xl:border-b-0">
          <dt class="flex items-center gap-1 text-xs font-medium text-destructive"><Gauge class="h-3.5 w-3.5" />{{ t('admin.connectionHealth.stateLabels.suspended') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ overview?.suspended ?? 0 }}</dd>
        </div>
        <div class="border-r border-border/50 px-4 py-3">
          <dt class="flex items-center gap-1 text-xs font-medium text-primary"><ShieldCheck class="h-3.5 w-3.5" />{{ t('admin.connectionHealth.summary.monitoredGroups') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ monitoredGroupCount }}</dd>
        </div>
        <div class="px-4 py-3">
          <dt class="flex items-center gap-1 text-xs font-medium" :class="conflictCount > 0 ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'"><ArrowDownUp class="h-3.5 w-3.5" />{{ t('admin.connectionHealth.summary.priorityConflicts') }}</dt>
          <dd class="mt-1 text-xl font-semibold tabular-nums text-foreground">{{ conflictCount }}</dd>
        </div>
      </dl>
    </section>

    <p v-if="errorKey" class="rounded-lg bg-destructive/10 px-4 py-3 text-sm text-destructive">{{ readableMessage(errorKey) }}</p>

    <section class="overflow-hidden rounded-lg border border-border/60 bg-card text-card-foreground shadow-sm">
      <div v-if="isLoading && adminGroups.length === 0" class="grid min-h-[34rem] lg:grid-cols-[19rem_minmax(0,1fr)]">
        <div class="space-y-3 border-r border-border/50 p-4">
          <div class="h-10 animate-pulse rounded-lg bg-surface" />
          <div v-for="index in 6" :key="index" class="h-16 animate-pulse rounded-lg bg-surface/70" />
        </div>
        <div class="space-y-5 p-6">
          <div class="h-8 w-48 animate-pulse rounded bg-surface" />
          <div class="h-20 animate-pulse rounded-lg bg-surface/70" />
          <div class="h-72 animate-pulse rounded-lg bg-surface/70" />
        </div>
      </div>

      <div v-else class="grid min-h-[34rem] lg:grid-cols-[19rem_minmax(0,1fr)]">
        <aside class="flex min-h-0 flex-col border-b border-border/50 lg:border-b-0 lg:border-r">
          <div class="space-y-3 border-b border-border/50 p-4">
            <div class="relative">
              <Search class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              <input
                v-model="searchText"
                type="search"
                :placeholder="t('admin.connectionHealth.filters.searchGroup')"
                class="h-10 w-full rounded-lg border border-border/60 bg-background pl-9 pr-3 text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
            </div>
            <select v-model="selectedType" class="h-9 w-full rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground">
              <option value="">{{ t('admin.connectionHealth.filters.allTypes') }}</option>
              <option v-for="type in groupTypes" :key="type" :value="type">{{ groupTypeLabel(type) }}</option>
            </select>
            <button
              type="button"
              class="inline-flex h-9 w-full items-center justify-center gap-2 rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground transition-colors hover:bg-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :aria-expanded="groupManagerOpen"
              @click="groupManagerOpen = !groupManagerOpen"
            >
              <Settings2 class="h-4 w-4" />
              {{ t('admin.connectionHealth.groupDisplay.manage') }}
            </button>
            <div v-if="groupManagerOpen" class="space-y-2 rounded-lg border border-border/60 bg-background p-2">
              <div class="flex items-center justify-between gap-2 px-1 text-xs text-muted-foreground">
                <span>{{ t('admin.connectionHealth.groupDisplay.title') }}</span>
                <span>{{ orderedGroups.length }}</span>
              </div>
              <div v-if="orderedGroups.length === 0" class="px-1 py-3 text-xs text-muted-foreground">
                {{ t('admin.connectionHealth.groupDisplay.empty') }}
              </div>
              <div
                v-for="(group, index) in orderedGroups"
                :key="`manage:${group.id}`"
                class="flex items-center gap-2 rounded-md px-1 py-1.5 hover:bg-surface/60"
              >
                <label class="flex min-w-0 flex-1 cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    :checked="!preferences.hiddenGroupIds.includes(group.id)"
                    class="h-4 w-4 rounded border-border accent-primary"
                    @change="toggleGroupVisibility(group.id)"
                  >
                  <span class="min-w-0 truncate text-xs text-foreground">{{ group.name }}</span>
                </label>
                <span class="h-2 w-2 shrink-0 rounded-full" :class="groupMonitoringEnabled(group) ? 'bg-emerald-500' : 'bg-muted-foreground/35'" />
                <button
                  type="button"
                  class="rounded p-1 text-muted-foreground transition-colors hover:bg-surface hover:text-foreground disabled:opacity-30"
                  :aria-label="t('admin.connectionHealth.groupDisplay.moveUp')"
                  :disabled="index === 0"
                  @click="moveGroup(group.id, -1)"
                >
                  <ChevronUp class="h-4 w-4" />
                </button>
                <button
                  type="button"
                  class="rounded p-1 text-muted-foreground transition-colors hover:bg-surface hover:text-foreground disabled:opacity-30"
                  :aria-label="t('admin.connectionHealth.groupDisplay.moveDown')"
                  :disabled="index === orderedGroups.length - 1"
                  @click="moveGroup(group.id, 1)"
                >
                  <ChevronDown class="h-4 w-4" />
                </button>
              </div>
            </div>
          </div>

          <nav class="max-h-[28rem] flex-1 overflow-y-auto p-2 lg:max-h-[calc(100dvh-20rem)]" :aria-label="t('admin.connectionHealth.groupListLabel')">
            <div v-if="filteredGroups.length === 0" class="flex min-h-48 flex-col items-center justify-center px-5 text-center">
              <Layers class="h-8 w-8 text-muted-foreground/40" />
              <p class="mt-3 text-sm text-muted-foreground">{{ emptyGroupMessage }}</p>
            </div>
            <template v-else>
              <button
                v-for="group in filteredGroups"
                :key="group.id"
                type="button"
                class="mb-1 flex w-full items-start gap-3 rounded-lg px-3 py-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                :class="[
                  selectedGroup?.id === group.id ? 'bg-primary/[0.08]' : 'hover:bg-surface/60',
                  focusedGroupId === group.id ? 'outline outline-2 -outline-offset-2 outline-primary/70' : '',
                ]"
                @click="selectGroup(group.id)"
              >
                <span
                  class="mt-1.5 h-2 w-2 shrink-0 rounded-full"
                  :class="groupMonitoringEnabled(group) ? 'bg-emerald-500' : group.priorityMode === 'multiplier' ? 'bg-primary' : 'bg-muted-foreground/35'"
                />
                <span class="min-w-0 flex-1">
                  <span class="flex items-center justify-between gap-2">
                    <span class="truncate text-sm font-medium text-foreground">{{ group.name }}</span>
                    <ArrowDownUp v-if="group.priorityMode === 'multiplier'" class="h-3.5 w-3.5 shrink-0 text-primary" />
                  </span>
                  <span class="mt-1 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                    <span>{{ t('admin.connectionHealth.groupList.monitored', { count: group.monitoredAccountCount ?? 0, total: group.accountCount }) }}</span>
                    <span>{{ group.multiplierDisplay || '-' }}</span>
                  </span>
                  <span class="mt-1 grid grid-cols-2 gap-x-2 text-[11px] leading-tight text-muted-foreground/80" :title="groupCostReasonDisplay(group.costReason)">
                    <span class="truncate">{{ t('admin.connectionHealth.groupList.recentHourCost') }} {{ groupCostDisplay(group.recentHourCost) }}</span>
                    <span class="truncate text-right">{{ t('admin.connectionHealth.groupList.todayCost') }} {{ groupCostDisplay(group.todayCost) }}</span>
                  </span>
                </span>
              </button>
            </template>
          </nav>
        </aside>

        <div v-if="!selectedGroup" class="flex min-h-[30rem] flex-col items-center justify-center text-center">
          <Layers class="h-9 w-9 text-muted-foreground/40" />
          <p class="mt-3 text-sm text-muted-foreground">{{ t('admin.connectionHealth.adminEmpty') }}</p>
        </div>
        <AdminGroupHealthDetail
          v-else
          :key="selectedGroup.id"
          :group="selectedGroup"
          :hide-unmonitored-accounts="preferences.hideUnmonitoredAccounts"
          :question-answer-unread-target-ids="preferences.questionAnswerUnreadTargetIds"
          :action-loading="isActionLoading"
          @setup="openSetup"
          @probe="onProbeAccount"
          @view-events="onViewEventsAccount"
          @set-schedulable="onSetTargetSchedulable"
          @update:hide-unmonitored-accounts="setHideUnmonitoredAccounts"
        />
      </div>
    </section>

    <GroupHealthSetupDrawer
      :open="setupDrawerOpen"
      :group="setupGroup"
      :policies="policies"
      @close="setupDrawerOpen = false"
      @saved="onSetupSaved"
    />

    <ManualOneTimeProbeDialog
      :open="probeDialogOpen"
      :target="probeDialogTarget"
      @close="probeDialogOpen = false"
      @completed="onFormalProbeCompleted"
      @question-answer-started="onQuestionAnswerStarted"
      @question-answer-viewed="onQuestionAnswerViewed"
    />

    <ProbePolicyListDialog
      :open="policyListDialogOpen"
      :policies="policies"
      :deleting-policy-id="deletingPolicyId"
      :delete-error="deletePolicyError"
      @close="policyListDialogOpen = false"
      @create="openCreatePolicy"
      @delete="handleDeletePolicy"
      @edit="openEditPolicy"
      @toggle="togglePolicyEnabled"
    />

    <PolicyConfigDrawer
      :open="policyDrawerOpen"
      :policy="editingPolicy"
      :own-group-options="ownGroupOptions"
      @close="policyDrawerOpen = false"
      @save="handleSavePolicy"
    />

    <ConnectionHealthEventsDialog
      :open="eventsDialogOpen"
      :events="events"
      :groups="groups"
      :admin-groups="adminGroups"
      :selected-connection-id="selectedConnectionId"
      :site-name="siteName"
      @close="eventsDialogOpen = false"
      @view-all="showAllEvents"
    />
  </div>
</template>

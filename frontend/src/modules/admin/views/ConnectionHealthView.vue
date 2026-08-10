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
import { listUpstreamSites } from '../api/upstream'
import { connectionHealthMessageKey, formatConnectionHealthTime, useConnectionHealth } from '../composables/useConnectionHealth'
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
  ConnectionHealthPolicy,
  PolicyInput,
  SafetySettings,
} from '../types/connectionHealth'
import { resolveConnectionHealthStrategyMode } from '../utils/connectionHealthPolicy'
import {
  createDefaultConnectionHealthPreferences,
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
  prioritySync,
  prioritySyncErrorKey,
  groups,
  adminGroups,
  events,
  policies,
  safety,
  isLoading,
  isActionLoading,
  errorKey,
  loadAll,
  loadAdminGroups,
  loadEvents,
  loadPolicies,
  loadSafety,
  removePolicy,
  savePolicy,
  saveSafety,
  emergencyClearSafety,
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
const safetySectionOpen = ref(false)
const safetySaving = ref(false)
const safetyClearing = ref(false)
let pendingEmergencyClearKey = ''
const safetyDraft = ref<SafetySettings>({
  confirmationObservationCount: 4,
  confirmationDelaysSeconds: [2, 5, 10],
  confirmationJitterSeconds: 1,
  abnormalQueueCapacity: 64,
  manualReservedSlots: 1,
  updatedAt: '',
  updatedBy: '',
})
const preferenceScope = computed(() => currentAccount.value?.id ?? 'anonymous')
let loadedPreferenceScope = ''

const safetyDelayCount = computed(() => Math.max(0, Number(safetyDraft.value.confirmationObservationCount || 0) - 1))
const safetyEffectiveAt = computed(() => {
  const value = safety.value?.settings.updatedAt
  if (!value) return ''
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp) || timestamp < Date.UTC(2000, 0, 1)) return ''
  return formatConnectionHealthTime(value)
})

const syncSafetyDraft = (settings: SafetySettings | null | undefined) => {
  if (!settings) return
  safetyDraft.value = {
    ...settings,
    confirmationDelaysSeconds: [...settings.confirmationDelaysSeconds],
  }
}

watch(safety, (next) => {
  if (!safetySectionOpen.value) syncSafetyDraft(next?.settings)
}, { deep: true })

watch(
  () => safetyDraft.value.confirmationObservationCount,
  (nextCount) => {
    const count = Math.max(3, Math.min(5, Number(nextCount) || 4))
    const desiredLength = count - 1
    const current = [...safetyDraft.value.confirmationDelaysSeconds]
    const defaults = [2, 5, 10, 15]
    while (current.length < desiredLength) current.push(defaults[current.length] ?? 15)
    safetyDraft.value.confirmationDelaysSeconds = current.slice(0, desiredLength)
  },
)

const groupTypes = ['public', 'exclusive', 'subscription']
const groupTypeLabel = (type: string): string => t(`admin.connectionHealth.groupTypes.${groupTypes.includes(type) ? type : 'public'}`)

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
const prioritySyncDecisionLabel = (decision: string): string => {
  const key = `admin.connectionHealth.prioritySync.decisions.${decision || 'unknown'}`
  return te(key) ? t(key) : t('admin.connectionHealth.prioritySync.decisions.unknown')
}
const prioritySyncReasonLabel = (reason: string): string => {
  const key = `admin.connectionHealth.prioritySync.reasons.${reason || 'none'}`
  return te(key) ? t(key) : t('admin.connectionHealth.prioritySync.reasons.none')
}
const prioritySyncActionLabel = (source: string): string => {
  const key = `admin.connectionHealth.prioritySync.actionSources.${source || 'unknown'}`
  return te(key) ? t(key) : t('admin.connectionHealth.prioritySync.actionSources.unknown')
}

const loadSiteNames = async () => {
  try {
    const sites = await listUpstreamSites()
    siteNameMap.value = new Map(sites.map((site) => [site.id, site.name]))
  } catch {
    // 站点名称仅用于事件展示，失败时保留 ID，不阻塞健康主流程。
  }
}

const refreshSafety = async () => {
  await loadSafety()
  syncSafetyDraft(safety.value?.settings)
}

const saveSafetySettings = async () => {
  if (safetySaving.value) return
  safetySaving.value = true
  try {
    const input: SafetySettings = {
      ...safetyDraft.value,
      confirmationObservationCount: Number(safetyDraft.value.confirmationObservationCount),
      confirmationDelaysSeconds: safetyDraft.value.confirmationDelaysSeconds.map(value => Number(value)),
      confirmationJitterSeconds: Number(safetyDraft.value.confirmationJitterSeconds),
      abnormalQueueCapacity: Number(safetyDraft.value.abnormalQueueCapacity),
      manualReservedSlots: Number(safetyDraft.value.manualReservedSlots),
    }
    if (await saveSafety(input)) syncSafetyDraft(safety.value?.settings)
  } finally {
    safetySaving.value = false
  }
}

const resetSafetyDraft = () => syncSafetyDraft(safety.value?.settings)

const toggleSafetySection = () => {
  const opening = !safetySectionOpen.value
  safetySectionOpen.value = opening
  if (opening) syncSafetyDraft(safety.value?.settings)
}

const createEmergencyClearUUID = (): string => {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  const bytes = new Uint8Array(16)
  if (globalThis.crypto?.getRandomValues) {
    globalThis.crypto.getRandomValues(bytes)
  } else {
    for (let index = 0; index < bytes.length; index++) bytes[index] = Math.floor(Math.random() * 256)
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

const emergencyClear = async () => {
  if (safetyClearing.value) return
  if (!window.confirm(t('admin.connectionHealth.safety.emergencyClearConfirm'))) return
  safetyClearing.value = true
  try {
    if (!pendingEmergencyClearKey) pendingEmergencyClearKey = createEmergencyClearUUID()
    const result = await emergencyClearSafety(pendingEmergencyClearKey)
    if (result) pendingEmergencyClearKey = ''
  } finally {
    safetyClearing.value = false
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
  void refreshSafety()
  void loadSiteNames()
}

onMounted(refreshOnEntry)
onActivated(refreshOnEntry)

const documentVisibility = useDocumentVisibility()
let autoRefreshInFlight = false
const autoRefresh = async () => {
  if (documentVisibility.value !== 'visible' || autoRefreshInFlight || probeDialogOpen.value) return
  autoRefreshInFlight = true
  try {
    await Promise.all([loadAll({ silent: true }), loadEvents(selectedConnectionId.value || undefined), loadSafety()])
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
  await Promise.all([loadAll(), loadPolicies(), loadEvents(), refreshSafety()])
}

const siteName = (siteId: string): string => siteNameMap.value.get(siteId) ?? siteId

// 分组启用/管理抽屉。
const setupDrawerOpen = ref(false)
const setupGroup = ref<AdminGroupHealth | null>(null)

const openSetup = (group: AdminGroupHealth) => {
  setupGroup.value = group
  setupDrawerOpen.value = true
}

const onSetupSaved = async () => {
  setupDrawerOpen.value = false
  await Promise.all([loadAll({ silent: true }), loadPolicies()])
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
        <Button variant="secondary" size="sm" @click="toggleSafetySection">
          <ShieldCheck class="h-4 w-4" />
          {{ t('admin.connectionHealth.topActions.safety') }}
        </Button>
        <Button variant="destructive" size="sm" :disabled="safetyClearing || !safety" @click="emergencyClear">
          <Loader2 v-if="safetyClearing" class="h-4 w-4 animate-spin" />
          <AlertTriangle v-else class="h-4 w-4" />
          {{ t('admin.connectionHealth.safety.emergencyClear') }}
        </Button>
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

    <section v-if="safetySectionOpen" class="overflow-hidden rounded-lg border border-border/60 bg-card text-card-foreground shadow-sm" :aria-label="t('admin.connectionHealth.safety.title')">
      <div class="flex flex-col gap-2 border-b border-border/50 px-4 py-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h2 class="text-sm font-semibold text-foreground">{{ t('admin.connectionHealth.safety.title') }}</h2>
          <p class="mt-1 max-w-3xl text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.subtitle') }}</p>
        </div>
        <span v-if="safetyEffectiveAt" class="shrink-0 text-xs text-muted-foreground">
          {{ t('admin.connectionHealth.safety.updatedAt', { time: safetyEffectiveAt }) }}
        </span>
      </div>

      <div v-if="safety" class="grid gap-4 p-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div class="space-y-4">
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="space-y-1.5">
              <span class="block text-xs font-medium text-foreground">{{ t('admin.connectionHealth.safety.observationCount') }}</span>
              <input
                v-model.number="safetyDraft.confirmationObservationCount"
                type="number"
                min="3"
                max="5"
                step="1"
                class="h-10 w-full rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
              <span class="block text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.observationCountHint') }}</span>
            </label>

            <label class="space-y-1.5">
              <span class="block text-xs font-medium text-foreground">{{ t('admin.connectionHealth.safety.jitter') }}</span>
              <input
                v-model.number="safetyDraft.confirmationJitterSeconds"
                type="number"
                min="0"
                max="3"
                step="1"
                class="h-10 w-full rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
              <span class="block text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.jitterHint') }}</span>
            </label>

            <label class="space-y-1.5">
              <span class="block text-xs font-medium text-foreground">{{ t('admin.connectionHealth.safety.queueCapacity') }}</span>
              <input
                v-model.number="safetyDraft.abnormalQueueCapacity"
                type="number"
                min="16"
                max="256"
                step="1"
                class="h-10 w-full rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
              <span class="block text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.queueCapacityHint') }}</span>
            </label>

            <label class="space-y-1.5">
              <span class="block text-xs font-medium text-foreground">{{ t('admin.connectionHealth.safety.manualReservedSlots') }}</span>
              <input
                v-model.number="safetyDraft.manualReservedSlots"
                type="number"
                min="0"
                max="1"
                step="1"
                class="h-10 w-full rounded-lg border border-border/60 bg-background px-3 text-sm text-foreground outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
              <span class="block text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.manualReservedSlotsHint') }}</span>
            </label>
          </div>

          <div class="space-y-2">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div>
                <h3 class="text-xs font-medium text-foreground">{{ t('admin.connectionHealth.safety.delays') }}</h3>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.delaysHint') }}</p>
              </div>
              <span class="text-xs tabular-nums text-muted-foreground">{{ t('admin.connectionHealth.safety.delayCount', { count: safetyDelayCount }) }}</span>
            </div>
            <div class="grid gap-2 sm:grid-cols-3">
              <label v-for="index in safetyDelayCount" :key="index" class="space-y-1.5">
                <span class="block text-xs text-muted-foreground">{{ t('admin.connectionHealth.safety.delayItem', { index }) }}</span>
                <input
                  v-model.number="safetyDraft.confirmationDelaysSeconds[index - 1]"
                  type="number"
                  min="1"
                  max="30"
                  step="1"
                  class="h-10 w-full rounded-lg border border-border/60 bg-background px-3 text-sm tabular-nums text-foreground outline-none focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
                >
              </label>
            </div>
          </div>

          <div class="flex flex-wrap items-center justify-end gap-2 border-t border-border/50 pt-3">
            <Button variant="ghost" size="sm" :disabled="safetySaving" @click="resetSafetyDraft">
              <RefreshCw class="h-4 w-4" />
              {{ t('admin.connectionHealth.safety.reset') }}
            </Button>
            <Button size="sm" :disabled="safetySaving" @click="saveSafetySettings">
              <Loader2 v-if="safetySaving" class="h-4 w-4 animate-spin" />
              <CheckCircle2 v-else class="h-4 w-4" />
              {{ safetySaving ? t('admin.connectionHealth.safety.saving') : t('admin.connectionHealth.safety.save') }}
            </Button>
          </div>
        </div>

        <aside class="space-y-3">
          <div class="rounded-lg border border-amber-500/30 bg-amber-500/5 p-3">
            <div class="flex items-start gap-2">
              <AlertTriangle class="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
              <div class="min-w-0">
                <h3 class="text-xs font-semibold text-foreground">{{ t('admin.connectionHealth.safety.emergencyTitle') }}</h3>
                <p class="mt-1 text-xs leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.emergencyDescription') }}</p>
              </div>
            </div>
            <Button class="mt-3 w-full" variant="destructive" size="sm" :disabled="safetyClearing" @click="emergencyClear">
              <Loader2 v-if="safetyClearing" class="h-4 w-4 animate-spin" />
              <AlertTriangle v-else class="h-4 w-4" />
              {{ safetyClearing ? t('admin.connectionHealth.safety.clearing') : t('admin.connectionHealth.safety.emergencyClear') }}
            </Button>
          </div>

          <div class="rounded-lg border border-border/60 bg-background p-3 text-xs">
            <div class="flex items-center justify-between gap-2">
              <span class="font-medium text-foreground">{{ t('admin.connectionHealth.safety.latestClear') }}</span>
              <span v-if="safety.latestEmergencyClear?.idempotent" class="text-muted-foreground">{{ t('admin.connectionHealth.safety.idempotent') }}</span>
            </div>
            <p v-if="safety.latestEmergencyClear" class="mt-2 leading-5 text-muted-foreground">
              {{ t('admin.connectionHealth.safety.clearResult', { cancelled: safety.latestEmergencyClear.cancelled, dispatching: safety.latestEmergencyClear.dispatching, incidents: safety.latestEmergencyClear.incidents }) }}
            </p>
            <p v-else class="mt-2 leading-5 text-muted-foreground">{{ t('admin.connectionHealth.safety.latestClearNone') }}</p>
            <p v-if="safety.latestEmergencyClear" class="mt-1 text-muted-foreground">
              {{ formatConnectionHealthTime(safety.latestEmergencyClear.completedAt) }} · {{ t('admin.connectionHealth.safety.queueEpoch', { epoch: safety.latestEmergencyClear.queueEpoch }) }}
            </p>
          </div>

          <div class="rounded-lg border border-border/60 bg-background p-3 text-xs">
            <h3 class="font-medium text-foreground">{{ t('admin.connectionHealth.safety.queueSummary') }}</h3>
            <dl class="mt-3 grid grid-cols-2 gap-x-3 gap-y-2">
              <div>
                <dt class="text-muted-foreground">{{ t('admin.connectionHealth.safety.queued') }}</dt>
                <dd class="mt-0.5 font-semibold tabular-nums text-foreground">{{ safety.queue.queued }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">{{ t('admin.connectionHealth.safety.claimed') }}</dt>
                <dd class="mt-0.5 font-semibold tabular-nums text-foreground">{{ safety.queue.claimed }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">{{ t('admin.connectionHealth.safety.dispatching') }}</dt>
                <dd class="mt-0.5 font-semibold tabular-nums text-foreground">{{ safety.queue.dispatching }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">{{ t('admin.connectionHealth.safety.guardHeld') }}</dt>
                <dd class="mt-0.5 font-semibold tabular-nums text-foreground">{{ safety.queue.guardHeld }}</dd>
              </div>
              <div class="col-span-2 border-t border-border/50 pt-2">
                <dt class="text-muted-foreground">{{ t('admin.connectionHealth.safety.incidents') }}</dt>
                <dd class="mt-0.5 font-semibold tabular-nums text-foreground">{{ safety.queue.incidents }}</dd>
              </div>
            </dl>
          </div>
        </aside>
      </div>

      <p v-else class="px-4 py-6 text-sm text-muted-foreground">{{ t('admin.connectionHealth.safety.loading') }}</p>
    </section>

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
      <dl v-if="prioritySync && prioritySync.lastWriteRoundTargetCount > 0" class="grid border-t border-border/50 sm:grid-cols-3 xl:grid-cols-6">
        <div class="min-w-0 border-b border-border/50 px-4 py-3 sm:border-r xl:border-b-0">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.decision') }}</dt>
          <dd class="mt-1 truncate text-sm font-medium text-foreground">{{ prioritySyncDecisionLabel(prioritySync.lastDecision) }}</dd>
          <p class="mt-1 truncate text-xs text-muted-foreground">{{ prioritySyncActionLabel(prioritySync.lastActionSource) }}</p>
        </div>
        <div class="border-b border-border/50 px-4 py-3 sm:border-r xl:border-b-0">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.interval') }}</dt>
          <dd class="mt-1 text-sm font-medium tabular-nums text-foreground">{{ t('admin.connectionHealth.prioritySync.intervalPairValue', { write: prioritySync.minWriteIntervalSeconds, reconcile: prioritySync.reconcileIntervalSeconds }) }}</dd>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.writebackSpreadValue', { seconds: prioritySync.writebackSpreadSeconds }) }}</p>
        </div>
        <div class="border-b border-border/50 px-4 py-3 xl:border-b-0 xl:border-r">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.reconcile') }}</dt>
          <dd class="mt-1 text-sm font-medium tabular-nums text-foreground">{{ prioritySync.reconcileSuccessCount }}/{{ prioritySync.reconcileAttemptCount }}</dd>
          <p v-if="prioritySync.lastInventoryError" class="mt-1 truncate text-xs text-destructive">{{ prioritySyncReasonLabel(prioritySync.lastInventoryError) }}</p>
        </div>
        <div class="border-b border-border/50 px-4 py-3 sm:border-r sm:border-b-0">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.snapshots') }}</dt>
          <dd class="mt-1 text-sm font-medium tabular-nums text-foreground">{{ prioritySync.snapshotHitCount }}/{{ prioritySync.snapshotMissCount }}</dd>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.probeEvaluations', { count: prioritySync.probeEvaluationCount }) }}</p>
        </div>
        <div class="border-b border-border/50 px-4 py-3 sm:border-b-0 sm:border-r">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.writes') }}</dt>
          <dd class="mt-1 text-sm font-medium tabular-nums text-foreground">{{ prioritySync.writeSuccessCount }}/{{ prioritySync.writeAttemptCount }}</dd>
        </div>
        <div class="px-4 py-3">
          <dt class="text-xs font-medium text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.pendingAge') }}</dt>
          <dd class="mt-1 text-sm font-medium tabular-nums text-foreground">{{ t('admin.connectionHealth.prioritySync.pendingAgeValue', { seconds: prioritySync.pendingAgeSeconds }) }}</dd>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.lastWriteRoundTargetValue', { count: prioritySync.lastWriteRoundTargetCount }) }}</p>
          <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.pendingTargetsValue', { count: prioritySync.pendingTargetCount }) }}</p>
          <p v-if="prioritySync.lastError || prioritySync.lastSuppressionReason" class="mt-1 truncate text-xs" :class="prioritySync.lastError ? 'text-destructive' : 'text-muted-foreground'">{{ prioritySyncReasonLabel(prioritySync.lastError || prioritySync.lastSuppressionReason) }}</p>
        </div>
      </dl>
      <p v-else-if="!prioritySyncErrorKey" class="border-t border-border/50 px-4 py-3 text-sm text-muted-foreground">{{ t('admin.connectionHealth.prioritySync.noHistory') }}</p>
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

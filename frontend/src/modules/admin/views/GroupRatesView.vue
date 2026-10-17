<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertCircle, ArrowUpDown, Check, ChevronDown, History, KeyRound, Link2, Loader2, Megaphone, RefreshCw, Search, ServerCog, Sparkles, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { readOwnGroupRates, readConnectionMainSiteGroups, changeConnectionMainSiteGroups, syncConnectionHubGroups, MainSiteGroupsApiError, checkScopedRealConnections, listScopedRealConnections, listConnectionsNeedingAttention, ScopedCheckApiError, getMySiteMappingOptions, realConnect, realBind, listAdminResources, listUpstreamKeys, realDisconnect } from '../api/mySites'
import { listUpstreamSiteOptions } from '../api/upstream'
import { useGroupRates } from '../composables/useGroupRates'
import { useAdminAccounts } from '../composables/useAdminAccounts'
import type { GroupRate, GroupRateHistoryRow } from '../types/groupRates'
import type { ConnectionMainSiteGroups, MainSiteGroupMutationResult, OwnGroupRate, AdminResourceOption, ConnectionCapabilities, ImportAccountSettings, ImportConfiguration, ImportDiagnostic, ImportFailure, MySiteMapping, MySiteMappingOwnGroupOption, RealConnectRequest, RealConnectResponse, RealConnection, ConnectionNeedingAttention, ExpectedRealConnection, ScopedCheckRequest, ScopedCheckResult, ScopedGroupRef, UpstreamKeyItem } from '../types/mySites'
import type { ImportApiError } from '../api/mySites'
import { LEGACY_NEW_API_CHANNEL_SUGGESTIONS, NEW_API_CHANNEL_TYPES } from '../types/mySites'
import { isImportAccountPlatform } from '../utils/importPlatforms'

import { t, te, locale } from '@/locales'
const router = useRouter()
const route = useRoute()
const { currentAccount } = useAdminAccounts()

const {
  rates,
  history,
  total,
  page,
  pageSize,
  totalPages,
  types,
  platforms,
  typeFilter,
  platformFilter,
  siteFilter,
  statusFilter,
  sortMode,
  statusCounts,
  serverSupportsStatusFilters,
  isLoading,
  isHistoryLoading,
  isActionLoading,
  errorKey,
  historyErrorKey,
  loadRates,
  applyRates,
  search,
  loadHistory,
  saveType,
} = useGroupRates()

const selectedRate = ref<GroupRate | null>(null)
const isHistoryOpen = ref(false)
const editingRate = ref<GroupRate | null>(null)
const connectingRate = ref<GroupRate | null>(null)
const editTypeValue = ref('')
const connectOwnGroups = ref<string[]>([])
const connectMode = ref<'real' | 'bind'>('real')
const ownGroups = ref<MySiteMappingOwnGroupOption[]>([])
const mySiteMappings = ref<MySiteMapping[]>([])
const hasLoadedMappingOptions = ref(false)
const connectionCapabilities = ref<ConnectionCapabilities | null>(null)
const searchQuery = ref('')
const upstreamSiteOptions = ref<{ id: string; name: string }[]>([])
const focusedRateKey = ref('')
const pendingFocusGroup = ref('')
const pendingFocusGroupId = ref('')
const focusError = ref('')
const siteOptionsError = ref('')
const connectionDataError = ref('')
const connectionDataKnown = ref(false)
const checkSummary = ref('')
let pageGeneration = 0
let pageController: AbortController | null = null
let checkController: AbortController | null = null
let optionsController: AbortController | null = null
let consumedFocusRoute = ''
let deferredRouteChange = false
let lastCheckScope: Omit<ScopedCheckRequest, 'mode'> | null = null
// Retain the existing batch's public identity copies for every local reread.
let checkIdentities = new Map<string, RealConnection>()
const confirmedIds = ref(new Set<string>())
const unconfirmedIds = ref(new Set<string>())
const changedIds = ref(new Set<string>())
const skippedIds = ref(new Set<string>())
const realConnectionsData = ref<RealConnection[]>([])
const ownGroupRatesState = ref<'idle' | 'loading' | 'ready' | 'failed'>('idle')
const ownGroupRatesById = ref(new Map<string, OwnGroupRate>())
const ownGroupRatesError = ref('')
let ownGroupRatesWorkspace = ''
let ownGroupRatesGeneration = 0
let ownGroupRatesController: AbortController | null = null
const resetOwnGroupRates = () => {
  ownGroupRatesController?.abort()
  ownGroupRatesController = null
  ownGroupRatesGeneration++
  ownGroupRatesState.value = 'idle'
  ownGroupRatesById.value = new Map()
  ownGroupRatesError.value = ''
  ownGroupRatesWorkspace = ''
}
const loadOwnGroupRates = async () => {
  const workspace = currentAccount.value?.id ?? ''
  if (!workspace) return
  ownGroupRatesController?.abort()
  const controller = new AbortController()
  ownGroupRatesController = controller
  const generation = ++ownGroupRatesGeneration
  ownGroupRatesWorkspace = workspace
  ownGroupRatesState.value = 'loading'
  ownGroupRatesById.value = new Map()
  ownGroupRatesError.value = ''
  const isCurrent = () => pageIsMounted && generation === ownGroupRatesGeneration && workspace === (currentAccount.value?.id ?? '')
  try {
    const response = await readOwnGroupRates(workspace, controller.signal)
    if (!isCurrent()) return
    ownGroupRatesById.value = new Map(response.groups.map(group => [group.id, group]))
    ownGroupRatesState.value = 'ready'
  } catch (error) {
    if (!isCurrent() || error instanceof Error && error.name === 'AbortError') return
    ownGroupRatesState.value = 'failed'
    const cause = error instanceof ScopedCheckApiError ? error.detail || error.message : displayCause(error)
    const next = error instanceof ScopedCheckApiError ? error.next : '请点“刷新数据”重试'
    ownGroupRatesError.value = `销售倍率暂时读不到：${cause}。销售倍率先不显示；这次销售倍率读取本身不会修改主站。${next}`
  } finally {
    if (isCurrent()) ownGroupRatesController = null
  }
}
const disconnectingRate = ref<{ siteId: string; siteName: string; groupName: string; pricingMapped?: boolean } | null>(null)
const disconnectIsMaintenance = ref(false)
let disconnectGeneration = 0
const attentionItems = ref<ConnectionNeedingAttention[]>([])
const attentionTotal = ref(0)
const attentionPage = ref(1)
const attentionTotalPages = ref(0)
const attentionSiteFilter = ref('')
const attentionExpanded = ref(false)
const attentionLoading = ref(false)
const attentionError = ref('')
const attentionKnown = ref(false)
const attentionCheckSummary = ref('')
const attentionCheckingSite = ref('')
let attentionGeneration = 0
let attentionController: AbortController | null = null
const disconnectConnection = ref<RealConnection | null>(null)
let disconnectWorkspace = ''
const disconnectMode = ref<'unlink' | 'full'>('unlink')
const disconnectRemovePricing = ref(true)
const isDisconnecting = ref(false)
const disconnectError = ref('')
const isCheckingConnections = ref(false)
const connectionCheckError = ref('')
const groupEditingRate = ref<GroupRate | null>(null)
const groupEditorState = ref<'loading' | 'readFailed' | 'ready' | 'submitting' | 'syncing'>('loading')
const groupEditorData = ref<ConnectionMainSiteGroups | null>(null)
const groupEditorSelected = ref<string[]>([])
const groupEditorRemovePricing = ref(true)
const groupEditorReadError = ref('')
const groupEditorResultNotice = ref('')
const groupEditorBusy = computed(() => groupEditorState.value === 'submitting' || groupEditorState.value === 'syncing')
const groupEditorUnchanged = computed(() => {
  const current = groupEditorData.value?.currentGroups.map(group => group.id) ?? []
  return current.length === groupEditorSelected.value.length && current.every(id => groupEditorSelected.value.includes(id))
})
let groupEditorGeneration = 0
let groupEditorWorkspace = ''
let groupEditorConnectionId = ''
let groupEditorExpected: ExpectedRealConnection | null = null
let groupEditorRequestSent = false
let groupEditorController: AbortController | null = null
const isAnyDialogOpen = computed(() => Boolean(isHistoryOpen.value || editingRate.value || connectingRate.value || disconnectingRate.value || groupEditingRate.value))
let previouslyFocusedElement: HTMLElement | null = null
let previousBodyOverflow = ''

const dialogFocusableSelector = [
  'button:not([disabled])',
  'a[href]',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

const handleDialogKeydown = (event: KeyboardEvent) => {
  const dialog = document.querySelector<HTMLElement>('[data-group-rates-dialog]')
  if (!dialog) return
  if (event.key === 'Escape') {
    event.preventDefault()
    if (groupEditingRate.value) { if (!groupEditorBusy.value) closeGroupEditor() }
    else if (disconnectingRate.value && !isDisconnecting.value) closeDisconnect()
    else if (connectingRate.value && !isActionLoading.value) closeConnector()
    else if (editingRate.value && !isActionLoading.value) closeTypeEditor()
    else if (isHistoryOpen.value) closeHistory()
    return
  }
  if (event.key !== 'Tab') return
  const focusableElements = Array.from(dialog.querySelectorAll<HTMLElement>(dialogFocusableSelector))
  if (focusableElements.length === 0) return
  const first = focusableElements[0]
  const last = focusableElements[focusableElements.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}
const selectedGroupType = ref('')
const selectedChannelType = ref(0)
const adminPlatform = computed(() => (currentAccount.value?.platform || '').trim().toLowerCase())
const isAdminReady = computed(() => Boolean(currentAccount.value?.id && ['sub2api', 'newapi'].includes(adminPlatform.value)))
const upstreamKeys = ref<UpstreamKeyItem[]>([])
const selectedKeyId = ref('')
const isLoadingKeys = ref(false)
const selectedAdminGroupId = ref('')
const adminResources = ref<AdminResourceOption[]>([])
const selectedAdminResourceId = ref('')
const isLoadingAdminResources = ref(false)
const addToPricingMapping = ref(true)
const connectOperationId = ref('')
const connectOutcome = ref<'editing' | 'submitting' | 'succeeded' | 'failed' | 'pending'>('editing')
const connectPriorityMode = ref<ImportAccountSettings['priorityMode']>('automatic')
const connectPriority = ref<string | number>('100')
const connectAutomaticPriority = ref<string | number>('100')
const connectConcurrency = ref<string | number>('50')
const connectPassthrough = ref(false)
const connectPoolMode = ref(true)
const connectProbeEnabled = ref(true)
const connectFailure = ref<ImportFailure | null>(null)
const connectResult = ref<RealConnectResponse | null>(null)
const connectRefreshError = ref('')
const connectGroupSelectionNotice = ref('')
let connectorGeneration = 0
let pageIsMounted = true
const connectSubmitLocked = ref(false)
const isC5Import = computed(() => connectMode.value === 'real' && adminPlatform.value === 'sub2api')
const connectUpstreamPlatform = computed(() => (connectingRate.value?.type || '').trim().toLowerCase())
const connectAccountPlatform = computed(() => selectedGroupType.value.trim().toLowerCase())
const supportsConnectPassthrough = computed(() => ['openai', 'anthropic'].includes(connectAccountPlatform.value))
const connectFormLocked = computed(() => isActionLoading.value || ['submitting', 'succeeded', 'pending'].includes(connectOutcome.value))
const validIntegerInput = (value: string | number, minimum: number, maximum: number): boolean => (
  String(value).trim() !== '' && Number.isInteger(Number(value)) && Number(value) >= minimum && Number(value) <= maximum
)
const connectPriorityError = computed(() => validIntegerInput(connectPriority.value, connectPriorityMode.value === 'manual' ? 1 : 10, connectPriorityMode.value === 'manual' ? 9 : 2147483647)
  ? '' : t(connectPriorityMode.value === 'manual' ? 'admin.groupRates.connect.importSettings.manualPriorityError' : 'admin.groupRates.connect.importSettings.automaticPriorityError'))
const connectConcurrencyError = computed(() => validIntegerInput(connectConcurrency.value, 1, 1000)
  ? '' : t('admin.groupRates.connect.importSettings.concurrencyError'))
const resetImportSettings = () => {
  connectPriorityMode.value = 'automatic'
  connectPriority.value = '100'
  connectAutomaticPriority.value = '100'
  connectConcurrency.value = '50'
  connectPassthrough.value = false
  connectPoolMode.value = true
  connectProbeEnabled.value = true
}
const setConnectPriorityMode = (mode: ImportAccountSettings['priorityMode']) => {
  if (connectFormLocked.value || mode === connectPriorityMode.value) return
  if (mode === 'manual') {
    connectAutomaticPriority.value = connectPriority.value
    connectPriority.value = '1'
  } else connectPriority.value = connectAutomaticPriority.value
  connectPriorityMode.value = mode
}
const connectConfiguration = computed<ImportConfiguration | null>(() => connectResult.value?.configuration ?? null)
const connectHasModelWarning = computed(() => connectConfiguration.value?.modelState === 'not_synced')
const connectDiagnostics = computed(() => connectFailure.value?.diagnostics ?? connectResult.value?.diagnostics ?? [])
const connectDiagnosticsUnavailable = computed(() => connectFailure.value?.diagnosticsUnavailable || connectResult.value?.diagnosticsUnavailable)
const importReasonLabel = (reason: string | undefined, target?: ImportDiagnostic['target']): string => {
  if (!reason) return ''
  const labels: Record<string, string> = {
    auth: 'auth', forbidden: 'forbidden', notFound: 'notFound', rateLimited: 'rateLimited',
    upstreamServerError: 'server', networkTimeout: 'timeout', networkUnreachable: 'network', tlsFailed: 'tls',
    invalidResponse: 'invalidResponse', businessRejected: 'businessRejected', refreshTokenRejected: 'refresh',
    accessTokenRejected: 'auth', loginIncomplete: 'loginIncomplete', announcementAckRequired: 'announcement',
    upstreamInsufficientBalance: 'balance', upstreamKeyQuotaExhausted: 'quota', upstreamKeyExpired: 'expired',
  }
  const key = reason.startsWith('admin.upstream.errors.') ? labels[reason.slice('admin.upstream.errors.'.length)] : undefined
  if (key) {
    const object = t(`admin.groupRates.connect.importResult.targets.${target ?? 'peer'}`)
    const label = `admin.groupRates.connect.importResult.reasons.${key}`
    return te(label) ? t(label, { object }) : t('admin.groupRates.connect.importResult.unknownDetail', { object })
  }
  return te(reason) ? t(reason) : ''
}
const diagnosticGuide = (diagnostic: ImportDiagnostic): string => {
  const object = t(`admin.groupRates.connect.importResult.targets.${diagnostic.target}`)
  const prefix = 'admin.groupRates.connect.importResult.guides.'
  const reason = diagnostic.reason ?? ''
  if (['admin.upstream.errors.auth', 'admin.upstream.errors.refreshTokenRejected', 'admin.upstream.errors.accessTokenRejected', 'admin.upstream.errors.loginIncomplete'].includes(reason) || diagnostic.httpStatus === 401) return t(prefix + (diagnostic.target === 'upstream' ? 'upstreamAuth' : 'mainAuth'))
  if (reason === 'admin.upstream.errors.upstreamInsufficientBalance') return t(prefix + 'balance')
  if (reason === 'admin.upstream.errors.upstreamKeyQuotaExhausted') return t(prefix + 'quota')
  if (reason === 'admin.upstream.errors.upstreamKeyExpired') return t(prefix + 'expired')
  if (reason === 'admin.upstream.errors.announcementAckRequired') return t(prefix + 'announcement')
  if (reason === 'admin.upstream.errors.forbidden' || diagnostic.httpStatus === 403) return t(prefix + 'forbidden', { object })
  if (reason === 'admin.upstream.errors.rateLimited' || diagnostic.httpStatus === 429) return t(prefix + 'rateLimited')
  if (['admin.upstream.errors.networkTimeout', 'admin.upstream.errors.networkUnreachable', 'admin.upstream.errors.tlsFailed'].includes(reason)) return t(prefix + 'network', { object })
  if (diagnostic.stage === 'model_sync') return t(prefix + 'model')
  if (diagnostic.stage === 'configuration_check') return t(prefix + 'configuration')
  if (diagnostic.stage === 'account_cleanup' || diagnostic.stage === 'key_cleanup') return t(prefix + 'cleanup', { object })
  if (diagnostic.target === 'local') return t(prefix + 'local')
  return t(prefix + 'records', { object })
}
const connectPreflightGuide = computed(() => {
  if (connectFailure.value?.message === 'admin.adminAccounts.errors.noCurrentAccount') return t('admin.groupRates.connect.importResult.preflightWorkspace')
  if (connectFailure.value?.message === 'admin.mySites.errors.connectionExists') return t('admin.groupRates.connect.importResult.preflightExisting')
  return t('admin.groupRates.connect.importResult.preflightReconnect')
})
const connectModelSummary = computed(() => {
  const config = connectConfiguration.value
  if (!config) return ''
  return t(`admin.groupRates.connect.importResult.models.${config.modelState}`, { count: config.models.length })
})
watch(selectedGroupType, () => {
  if (!isC5Import.value) return
  const kept = connectOwnGroups.value.filter(id => filteredOwnGroups.value.some(group => group.id === id))
  if (kept.length !== connectOwnGroups.value.length) connectGroupSelectionNotice.value = t('admin.groupRates.connect.importSettings.groupsRemoved')
  connectOwnGroups.value = kept
})
watch(supportsConnectPassthrough, (supported) => {
  if (!supported) connectPassthrough.value = false
})
watch([() => currentAccount.value?.id, adminPlatform], () => {
  resetGroupEditor()
  connectorGeneration++
  disconnectGeneration++
  closeDisconnect()
  isDisconnecting.value = false
  invalidateAttention(true)
  attentionSiteFilter.value = ''
  attentionExpanded.value = false
  attentionPage.value = 1
  connectSubmitLocked.value = false
  isActionLoading.value = false
  connectingRate.value = null
  connectOwnGroups.value = []
  ownGroups.value = []
  mySiteMappings.value = []
  upstreamKeys.value = []
  adminResources.value = []
  selectedKeyId.value = ''
  selectedAdminGroupId.value = ''
  selectedAdminResourceId.value = ''
  isLoadingKeys.value = false
  isLoadingAdminResources.value = false
  invalidatePage(true)
  connectionDataKnown.value = false
  realConnectionsData.value = []
  resetOwnGroupRates()
  if (pageIsMounted) void enterPage()
  hasLoadedMappingOptions.value = false
  connectOutcome.value = 'editing'
  connectResult.value = null
  connectFailure.value = null
  realConnectError.value = ''
  connectRefreshError.value = ''
}, { flush: 'sync' })
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null
let pendingSearchValue: string | null = null

const queryValue = (value: unknown): string => {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : ''
  return typeof value === 'string' ? value : ''
}

siteFilter.value = queryValue(route.query.siteId)
pendingFocusGroup.value = queryValue(route.query.focusGroup)
pendingFocusGroupId.value = queryValue(route.query.focusGroupId)

const siteOptions = computed(() => {
  const options = new Map(upstreamSiteOptions.value.map(site => [site.id, site.name]))
  for (const rate of rates.value) {
    if (!options.has(rate.siteId)) options.set(rate.siteId, rate.siteName)
  }
  return Array.from(options, ([id, name]) => ({ id, name }))
    .sort((first, second) => first.name.localeCompare(second.name))
})

const rateFocusKey = (rate: GroupRate): string => `${rate.siteId}\u0000${rate.groupId ? 'id:' + rate.groupId : 'name:' + rate.groupName}`

const routeScopeKey = () => [queryValue(route.query.siteId), queryValue(route.query.focusGroupId), queryValue(route.query.focusGroup)].join('\u0000')
const clearRateHighlight = () => { focusedRateKey.value = '' }
const consumeRouteFocus = async (found?: boolean) => {
  const name = pendingFocusGroup.value.trim()
  const id = pendingFocusGroupId.value.trim()
  if (!name && !id) return
  const target = rates.value.find(rate => rate.siteId === siteFilter.value && (id ? rate.groupId === id : rate.groupName === name))
  if (found === true && target) focusedRateKey.value = rateFocusKey(target)
  else focusError.value = `没有找到要查看的分组${name || id}，或存在多个同名分组；当前筛选保持不变。请检查网站与分组是否仍存在，或调整筛选后查找。`
  pendingFocusGroup.value = ''
  pendingFocusGroupId.value = ''
  const query = { ...route.query }
  delete query.focusGroup
  delete query.focusGroupId
  consumedFocusRoute = [queryValue(query.siteId), '', ''].join('\u0000')
  await router.replace({ query })
}
const displayCause = (error: unknown) => error instanceof Error ? (te(error.message) ? t(error.message) : error.message) : '原因无法确认，请查看Hub后端读取记录'
const showMissingWorkspace = () => {
  isLoading.value = false
  connectionDataKnown.value = false
  realConnectionsData.value = []
  upstreamSiteOptions.value = []
  applyRates({ items: [], total: 0, page: 1, pageSize: 30, totalPages: 0, types: [], platforms: [], statusCounts: { all: 0, mapped: 0, unmapped: 0, deleted: 0 } })
  errorKey.value = '尚未选定当前工作区，本次未读取倍率或账号资料；请先选择工作区，再刷新页面。'
}
const loadUpstreamSiteOptions = async () => {
  const workspace = currentAccount.value?.id ?? ''
  if (!workspace) return
  optionsController?.abort()
  const controller = new AbortController()
  optionsController = controller
  try {
    const response = await listUpstreamSiteOptions(controller.signal)
    if (!pageIsMounted || controller.signal.aborted || workspace !== (currentAccount.value?.id ?? '')) return
    if (response.workspaceAdminAccountId !== workspace) throw new Error('工作区已变化，请刷新重试')
    upstreamSiteOptions.value = response.items
    siteOptionsError.value = ''
  } catch (error) {
    if (!pageIsMounted || controller.signal.aborted || workspace !== (currentAccount.value?.id ?? '')) return
    siteOptionsError.value = `网站选项暂时无法加载：${displayCause(error)}。当前列表保持显示，请点击刷新重试加载选项。`
  }
}

const editTypeOptions = computed(() => {
  const options = new Set(types.value)
  if (editingRate.value?.type) options.add(editingRate.value.type)
  return Array.from(options).sort((first, second) => first.localeCompare(second))
})
const mappedOwnGroupsForRate = (rate: GroupRate): string[] => (
  mySiteMappings.value
    .filter((mapping) => mapping.upstreamTargets.some((target) => target.siteId === rate.siteId && target.groupName === rate.groupName))
    .map((mapping) => mapping.ownGroup)
)

const firstMappedOwnGroupForRate = (rate: GroupRate): string => mappedOwnGroupsForRate(rate)[0] ?? ''

const filteredOwnGroups = computed(() => {
  // new-api admin 不按渠道类型筛选，直接显示全部自有分组
  if (isAdminNewAPI.value) return ownGroups.value
  if (isC5Import.value) {
    return ownGroups.value.filter(group => isImportAccountPlatform(connectAccountPlatform.value) && group.platform.trim().toLowerCase() === connectAccountPlatform.value)
  }
  const upstreamType = (connectingRate.value?.type || selectedGroupType.value).toLowerCase()
  return upstreamType ? ownGroups.value.filter(group => group.platform.toLowerCase() === upstreamType) : ownGroups.value
})
const canSelectOwnGroup = (group: MySiteMappingOwnGroupOption): boolean => (
  !isC5Import.value || !connectAccountPlatform.value || group.platform.trim().toLowerCase() === connectAccountPlatform.value
)

const realConnectionsForRate = (rate: GroupRate): RealConnection[] =>
  realConnectionsData.value.filter(c => (
    c.upstreamSiteId === rate.siteId &&
    (Boolean(c.upstreamGroupId && rate.groupId) && c.upstreamGroupId === rate.groupId || ((!c.upstreamGroupId || !rate.groupId) && c.upstreamGroupName === rate.groupName))
  ))

const realConnectionForRate = (rate: GroupRate): RealConnection | undefined => {
  const matches = realConnectionsForRate(rate)
  return matches.find(connection => !connection.status || connection.status === 'active') ?? matches[0]
}

const isRealConnected = (rate: GroupRate): boolean => {
  const connection = realConnectionForRate(rate)
  return Boolean(connection && (!connection.status || connection.status === 'active'))
}
const isMainAccountMissing = (rate: GroupRate): boolean => realConnectionForRate(rate)?.status === 'missing'
const calculateProfitMargin = (saleMultiplier: number | null | undefined, costMultiplier: number | null): number | null => {
  if (saleMultiplier == null || costMultiplier == null || !Number.isFinite(saleMultiplier) || saleMultiplier <= 0) return null
  return (saleMultiplier - costMultiplier) / saleMultiplier
}
const profitMarginFormatter = new Intl.NumberFormat(locale, { style: 'percent', minimumFractionDigits: 2, maximumFractionDigits: 2 })
const formatProfitMargin = (value: number | null): string => value == null || !Number.isFinite(value)
  ? t('admin.groupRates.common.placeholder') : profitMarginFormatter.format(value)
const profitMarginClass = (value: number | null): string => {
  if (value == null || !Number.isFinite(value)) return 'text-muted-foreground'
  if (value < 0) return 'text-destructive'
  if (value === 0) return 'text-warning'
  return 'text-emerald-600 dark:text-emerald-400'
}
type OwnGroupCellLine = { key: string; id: string; name: string; title: string; value: string; valueTone: 'number' | 'muted'; suffix: string; profitMargin: number | null }
type OwnGroupCells = { kind: 'unknown' | 'blank'; lines: OwnGroupCellLine[] } | { kind: 'lines'; lines: OwnGroupCellLine[] }
const ownGroupCells = (rate: GroupRate): OwnGroupCells => {
  if (!connectionDataKnown.value) return { kind: 'unknown', lines: [] }
  const connections = realConnectionsForRate(rate).filter(c => !c.status || c.status === 'active')
  if (!connections.length) return { kind: 'blank', lines: [] }
  const groups = new Map<string, { id: string; name: string }>()
  for (const connection of connections) {
    const ids = Array.isArray(connection.ownGroupIds) ? connection.ownGroupIds : []
    const names = Array.isArray(connection.ownGroupNames) ? connection.ownGroupNames : []
    for (let i = 0; i < Math.max(ids.length, names.length); i++) {
      const id = (ids[i] ?? '').trim(), name = (names[i] ?? '').trim()
      if (!id && !name) continue
      const key = id ? `id:${id}` : `name:${name}`
      if (!groups.has(key)) groups.set(key, { id, name })
    }
  }
  if (!groups.size) return { kind: 'lines', lines: [{ key: 'missing', id: '', name: '对接记录里没有主站分组', title: '对接记录里没有主站分组', value: '', valueTone: 'muted', suffix: '', profitMargin: null }] }
  const lines = Array.from(groups, ([key, group]): OwnGroupCellLine => {
    const live = ownGroupRatesState.value === 'ready' && group.id ? ownGroupRatesById.value.get(group.id) : undefined
    const name = live ? live.name : group.name || `分组编号 ${group.id}`
    let value: string, valueTone: 'number' | 'muted' = 'muted', suffix = ''
    if (ownGroupRatesState.value === 'loading') value = '读取中'
    else if (ownGroupRatesState.value !== 'ready') value = '暂时读不到'
    else if (!group.id) value = '对接记录没有分组编号'
    else if (!live) value = '读取时主站没有这个分组'
    else {
      value = live.multiplier === null ? '主站未返回倍率' : formatMultiplier(live.multiplier)
      if (live.multiplier !== null) valueTone = 'number'
      const status = live.status.trim().toLowerCase()
      if (status && status !== 'active') suffix = '（已停用）'
    }
    const profitMargin = live ? calculateProfitMargin(live.multiplier, rate.currentMultiplier) : null
    return { key, id: group.id, name, title: name, value, valueTone, suffix, profitMargin }
  })
  lines.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
  return { kind: 'lines', lines }
}

const isPricingMapped = (rate: GroupRate): boolean => rate.pricingMapped ?? mappedOwnGroupsForRate(rate).length > 0

const groupRefsForPage = (): ScopedGroupRef[] => Array.from(new Map(rates.value.map(rate => {
  const ref = { siteId: rate.siteId, groupId: rate.groupId || '', groupName: rate.groupName }
  return [JSON.stringify(ref), ref]
})).values())
const frozenScope = (): Omit<ScopedCheckRequest, 'mode'> => ({ workspaceAdminAccountId: currentAccount.value?.id ?? '', ...(siteFilter.value ? { siteId: siteFilter.value } : { groupRefs: groupRefsForPage() }) })
const connectionMatchesScope = (connection: RealConnection, scope = lastCheckScope): boolean => Boolean(scope && (scope.siteId ? connection.upstreamSiteId === scope.siteId : scope.groupRefs?.some(ref => connection.upstreamSiteId === ref.siteId && ((Boolean(connection.upstreamGroupId && ref.groupId) && connection.upstreamGroupId === ref.groupId) || ((!connection.upstreamGroupId || !ref.groupId) && connection.upstreamGroupName === ref.groupName)))))
const applicableConnection = (connection: RealConnection) => Boolean(connection.adminAccountId?.trim() && (!connection.adminPlatform || connection.adminPlatform === 'sub2api'))
const connectionOperationBlocked = (rate: GroupRate): boolean => {
  if (!connectionDataKnown.value) return true
  const connection = realConnectionForRate(rate)
  if (!connection || !connectionMatchesScope(connection)) return false
  if (unconfirmedIds.value.has(connection.id) || changedIds.value.has(connection.id)) return true
  const frozen = checkIdentities.get(connection.id)
  const unchanged = frozen && sameConnectionIdentity(frozen, connection) && frozen.status === connection.status
  if (skippedIds.value.has(connection.id)) return !unchanged
  if (!applicableConnection(connection)) return false
  return isCheckingConnections.value || !confirmedIds.value.has(connection.id) || !unchanged
}
const operationBlockedReason = '这个账号正在检查或本次未能确认，暂时不能导入、解绑或修改主站分组；请等待检查结束，或点击刷新完成核对。'
// The backend trims the group name with Go strings.TrimSpace before matching by
// name, so such a row could update another group. Same character set as
// utils/questionAnswers.ts; JavaScript trim() misses U+0085.
const goTrimSpaceEdge = /^[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]|[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]$/
const typeEditBlocked = (rate: GroupRate): boolean => goTrimSpaceEdge.test(rate.groupName)
const typeEditBlockedReason = '这个分组的名字首尾带空白字符，暂时不能修改分类，请先保留现有分类；这项限制不影响倍率同步，也不会修改主站。'
const clearCheckState = () => {
  lastCheckScope = null
  checkIdentities.clear()
  confirmedIds.value = new Set()
  unconfirmedIds.value = new Set()
  changedIds.value = new Set()
  skippedIds.value = new Set()
  checkSummary.value = ''
  connectionCheckError.value = ''
}
const invalidatePage = (clear = false) => {
  pageGeneration++
  pageController?.abort()
  checkController?.abort()
  if (isCheckingConnections.value) {
    unconfirmedIds.value = new Set([...unconfirmedIds.value, ...realConnectionsData.value.filter(connection => applicableConnection(connection) && connectionMatchesScope(connection)).map(connection => connection.id)])
  }
  isCheckingConnections.value = false
  attentionCheckingSite.value = ''
  if (clear) clearCheckState()
  return pageGeneration
}
const loadLocalPage = async (options: { generation?: number; focus?: boolean; isCurrent?: () => boolean } = {}): Promise<boolean> => {
  const generation = options.generation ?? invalidatePage()
  const workspace = currentAccount.value?.id ?? ''
  if (!workspace) { showMissingWorkspace(); return false }
  const controller = new AbortController()
  pageController?.abort()
  pageController = controller
  const isCurrent = () => pageIsMounted && !controller.signal.aborted && generation === pageGeneration && workspace === (currentAccount.value?.id ?? '') && (options.isCurrent?.() ?? true)
  try {
    const response = await loadRates(isCurrent, { workspaceAdminAccountId: workspace, siteId: siteFilter.value, signal: controller.signal,
      ...(options.focus ? { focusGroupId: pendingFocusGroupId.value, focusGroup: pendingFocusGroup.value } : {}) }, false)
    if (!response || !isCurrent()) return false
    if (response.workspaceAdminAccountId !== workspace) throw new Error('倍率返回的工作区已变化，请刷新页面')
    isLoading.value = true
    try {
      const connections = await listScopedRealConnections({ workspaceAdminAccountId: workspace, groupRefs: response.items.map(rate => ({ siteId: rate.siteId, groupId: rate.groupId || '', groupName: rate.groupName })) }, controller.signal)
      if (!isCurrent()) return false
      revokeChangedPermissions(connections.items)
      applyRates(response)
      realConnectionsData.value = connections.items
      connectionDataKnown.value = true
      connectionDataError.value = ''
    } catch (error) {
      if (!isCurrent()) return false
      applyRates(response)
      connectionDataKnown.value = false
      connectionDataError.value = `倍率已加载，但本页对接资料暂时无法读取：${displayCause(error)}。当前不会显示导入或解绑按钮，请点击刷新重试；本次没有执行账号操作。`
      return false
    }
    if (options.focus) await consumeRouteFocus(response.focusFound)
    return isCurrent()
  } catch (error) {
    if (isCurrent()) errorKey.value = displayCause(error)
    return false
  } finally { if (isCurrent()) isLoading.value = false }
}
const sameConnectionIdentity = (before: RealConnection, after: RealConnection) => before.id === after.id && before.adminAccountId === after.adminAccountId && before.upstreamSiteId === after.upstreamSiteId && before.upstreamGroupId === after.upstreamGroupId && before.upstreamGroupName === after.upstreamGroupName && (before.adminPlatform || '') === (after.adminPlatform || '')
const revokeChangedPermissions = (connections: RealConnection[]) => {
  const current = new Map(connections.map(connection => [connection.id, connection]))
  const changed = new Set(changedIds.value)
  for (const id of new Set([...confirmedIds.value, ...skippedIds.value])) {
    const latest = current.get(id)
    if (!latest) continue
    const frozen = checkIdentities.get(id)
    if (frozen && sameConnectionIdentity(frozen, latest) && frozen.status === latest.status) continue
    confirmedIds.value.delete(id)
    skippedIds.value.delete(id)
    changed.add(id)
  }
  if (changed.size !== changedIds.value.size) {
    changedIds.value = changed
    connectionCheckError.value = `${connectionCheckError.value ? connectionCheckError.value + ' ' : ''}账号的关联资料在检查后发生变化，旧结果没有确认最新资料；请点击刷新重新核对。`
  }
}
const runScopedCheck = async (mode: 'automatic' | 'manual', scope: Omit<ScopedCheckRequest, 'mode'>, before: RealConnection[], generation = pageGeneration) => {
  if (generation !== pageGeneration || !pageIsMounted || isAnyDialogOpen.value) return
  const workspace = scope.workspaceAdminAccountId
  if (!workspace || workspace !== (currentAccount.value?.id ?? '')) return
  const controller = new AbortController()
  checkController?.abort()
  checkController = controller
  const isCurrent = () => pageIsMounted && generation === pageGeneration && workspace === (currentAccount.value?.id ?? '') && !controller.signal.aborted
  lastCheckScope = scope
  checkIdentities = new Map(before.map(connection => [connection.id, { ...connection }]))
  confirmedIds.value = new Set()
  skippedIds.value = new Set()
  changedIds.value = new Set()
  unconfirmedIds.value = new Set(before.filter(connection => applicableConnection(connection) && connectionMatchesScope(connection, scope)).map(connection => connection.id))
  isCheckingConnections.value = true
  connectionCheckError.value = ''
  checkSummary.value = ''
  let result: ScopedCheckResult | undefined
  let failure = ''
  try { result = await checkScopedRealConnections({ ...scope, mode }, controller.signal) }
  catch (error) {
    if (!isCurrent()) return
    if (error instanceof ScopedCheckApiError) { result = error.result; failure = [displayCause(error), error.detail, error.next].filter(Boolean).join('；') }
    else failure = `账号检查结果暂时无法取得：${displayCause(error)}；仍保留上次状态，请点击刷新重试，并检查Hub后端检查记录。`
  }
  if (!isCurrent()) return
  if (result) {
    unconfirmedIds.value = new Set(result.unconfirmedConnectionIds)
    changedIds.value = new Set(result.changedConnectionIds)
    skippedIds.value = new Set(result.skippedConnectionIds)
  }
  const reread = await loadLocalPage({ generation })
  if (!isCurrent()) return
  if (reread && result) {
    const previous = new Map(before.map(connection => [connection.id, connection]))
    const current = new Map(realConnectionsData.value.map(connection => [connection.id, connection]))
    const confirmed = new Set<string>()
    let confirmedCount = result.confirmedConnectionIds.length
    const changed = new Set([...result.changedConnectionIds, ...changedIds.value])
    for (const id of result.confirmedConnectionIds) {
      const original = previous.get(id), latest = current.get(id)
      // Off-page backend confirmations count for this batch, but cannot
      // authorize a row that has no matching public before/after identity.
      if (!latest) continue
      if (original && sameConnectionIdentity(original, latest) && latest.status === result.confirmedStatuses[id]) { confirmed.add(id); checkIdentities.set(id, { ...original, status: latest.status }) }
      else { changed.add(id); confirmedCount-- }
    }
    const known = new Set([...result.confirmedConnectionIds, ...result.unconfirmedConnectionIds, ...result.changedConnectionIds, ...result.skippedConnectionIds])
    const fresh = realConnectionsData.value.filter(connection => applicableConnection(connection) && connectionMatchesScope(connection, scope) && !known.has(connection.id))
    unconfirmedIds.value = new Set([...result.unconfirmedConnectionIds, ...fresh.map(connection => connection.id)])
    confirmedIds.value = confirmed
    changedIds.value = changed
    checkSummary.value = `本次确认${confirmedCount}条账号对接记录，${unconfirmedIds.value.size + changed.size}条仍显示上次状态。标签数量按本地保存结果统计，不代表所有账号刚刚检查过；未确认的账号可点击刷新重试。` + (skippedIds.value.size ? ` ${skippedIds.value.size}条记录不适用本次检查，保持原来的操作方式。` : '') + (fresh.length ? ` 本页有${fresh.length}条新出现的对接记录尚未检查，请点击刷新核对。` : '') + (changed.size ? ` ${changed.size}条关联资料在检查期间发生变化，本次结果没有覆盖它；请刷新查看最新记录。` : '')
  } else if (!reread) {
    if (result) unconfirmedIds.value = new Set([...unconfirmedIds.value, ...result.confirmedConnectionIds])
    failure = `${failure ? failure + '；' : ''}账号检查已完成，但最新对接状态暂时无法读取：${connectionDataError.value || (errorKey.value ? displayCause(new Error(errorKey.value)) : '原因无法确认，请查看Hub后端读取记录')}；请刷新，本次未执行导入或解绑。`
  } else checkSummary.value = `本次确认0条账号对接记录，${unconfirmedIds.value.size}条仍显示上次状态。标签数量按本地保存结果统计，不代表所有账号刚刚检查过；未确认的账号可点击刷新重试。`
  connectionCheckError.value = [failure, ...(result?.issues.map(issue => `${issue.detail}。${issue.next}`) ?? [])].filter(Boolean).join(' ')
  isCheckingConnections.value = false
}
const enterPage = async () => {
  const generation = invalidatePage(true)
  // A new scope cannot inherit old rows or their connection-read permission.
  connectionDataKnown.value = false
  connectionDataError.value = ''
  realConnectionsData.value = []
  rates.value = []
  page.value = 1
  if (!currentAccount.value?.id) { showMissingWorkspace(); return }
  if (ownGroupRatesWorkspace !== (currentAccount.value?.id ?? '')) void loadOwnGroupRates()
  void loadUpstreamSiteOptions()
  void loadAttention()
  if (await loadLocalPage({ generation, focus: Boolean(pendingFocusGroup.value || pendingFocusGroupId.value) })) {
    const scope = frozenScope()
    const before = realConnectionsData.value.map(connection => ({ ...connection }))
    void runScopedCheck('automatic', scope, before, generation)
  }
}
const checkThenLoadRates = async () => {
  if (isCheckingConnections.value || isAnyDialogOpen.value) return
  if (!currentAccount.value?.id) { showMissingWorkspace(); return }
  void loadOwnGroupRates()
  const scope = frozenScope()
  const before = realConnectionsData.value.map(connection => ({ ...connection }))
  const generation = invalidatePage(true)
  // Freeze before rereading: reordered/shrunk pages must not replace this batch.
  lastCheckScope = scope
  isCheckingConnections.value = true
  void loadUpstreamSiteOptions()
  const loaded = await loadLocalPage({ generation })
  if (generation !== pageGeneration || !pageIsMounted) return
  isCheckingConnections.value = false
  if (loaded) await runScopedCheck('manual', scope, before, generation)
}

const filteredRates = computed(() => {
  if (serverSupportsStatusFilters.value) return rates.value.filter(rate => !rate.deleted)

  const filtered = rates.value.filter(rate => {
    const typeMatch = !typeFilter.value || rate.type === typeFilter.value
    const platformMatch = !platformFilter.value || rate.platform === platformFilter.value
    const siteMatch = !siteFilter.value || rate.siteId === siteFilter.value

    if (statusFilter.value === 'deleted') return false

    if (rate.deleted) return false

    const mappedMatch = statusFilter.value === 'all' ||
      (statusFilter.value === 'mapped' && rate.mapped) ||
      (statusFilter.value === 'unmapped' && !rate.mapped)

    return typeMatch && platformMatch && siteMatch && mappedMatch
  })

  return [...filtered].sort((a, b) => {
    switch (sortMode.value) {
      case 'multiplierAsc':
        return (a.currentMultiplier ?? Infinity) - (b.currentMultiplier ?? Infinity)
      case 'multiplierDesc':
        return (b.currentMultiplier ?? -Infinity) - (a.currentMultiplier ?? -Infinity)
      case 'siteNameAsc':
        return a.siteName.localeCompare(b.siteName)
      case 'groupNameAsc':
        return a.groupName.localeCompare(b.groupName)
    }
  })
})
const canGoPrevious = computed(() => page.value > 1 && !isLoading.value)
const canGoNext = computed(() => page.value < totalPages.value && !isLoading.value)

const applyPendingSearch = () => {
  if (pendingSearchValue === null || isAnyDialogOpen.value) return
  search.value = pendingSearchValue
  pendingSearchValue = null
  page.value = 1
  void loadLocalPage()
}
watch(searchQuery, (value) => {
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  pendingSearchValue = value
  if (isAnyDialogOpen.value) return
  searchDebounceTimer = setTimeout(() => {
    searchDebounceTimer = null
    applyPendingSearch()
  }, 300)
})
const hasAnyRateData = computed(() => ['all', 'mapped', 'unmapped'].some(key => statusCounts.value[key as 'all' | 'mapped' | 'unmapped'] > 0))
const hasActiveRateFilters = computed(() => Boolean(
  searchQuery.value.trim() ||
  typeFilter.value ||
  platformFilter.value ||
  siteFilter.value ||
  statusFilter.value !== 'all',
))

watch(isAnyDialogOpen, async (open) => {
  if (open) {
    if (searchDebounceTimer) { clearTimeout(searchDebounceTimer); searchDebounceTimer = null }
    previouslyFocusedElement = document.activeElement as HTMLElement | null
    previousBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    await nextTick()
    document.querySelector<HTMLElement>('[data-group-rates-dialog] button:not([disabled]), [data-group-rates-dialog] input:not([disabled])')?.focus()
    return
  }
  document.body.style.overflow = previousBodyOverflow
  previouslyFocusedElement?.focus()
  previouslyFocusedElement = null
  if (deferredRouteChange) {
    deferredRouteChange = false
    if (pendingSearchValue !== null) { search.value = pendingSearchValue; pendingSearchValue = null }
    siteFilter.value = queryValue(route.query.siteId)
    pendingFocusGroup.value = queryValue(route.query.focusGroup)
    pendingFocusGroupId.value = queryValue(route.query.focusGroupId)
    void enterPage()
  } else {
    applyPendingSearch()
    if (pageIsMounted && attentionController?.signal.aborted && !attentionLoading.value) void loadAttention()
  }
})

onMounted(async () => {
  document.addEventListener('keydown', handleDialogKeydown)
  await enterPage()
})

onBeforeUnmount(() => {
  pageIsMounted = false
  resetGroupEditor()
  resetOwnGroupRates()
  invalidatePage(true)
  optionsController?.abort()
  invalidateAttention(true)
  disconnectGeneration++
  connectorGeneration++
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer)
  document.removeEventListener('keydown', handleDialogKeydown)
  document.body.style.overflow = previousBodyOverflow
})

const isAdminNewAPI = computed(() => adminPlatform.value === 'newapi')
const needsGroupTypeSelection = computed(() => isC5Import.value || (
  connectMode.value === 'real' &&
  (connectionCapabilities.value?.requiresGroupType ?? (!connectingRate.value?.type && !isAdminNewAPI.value))
) && !connectingRate.value?.type)
const needsChannelTypeSelection = computed(() => connectMode.value === 'real' && (connectionCapabilities.value?.requiresChannelType ?? isAdminNewAPI.value))

// new-api admin：根据自有分组类型过滤可选的渠道类型
// 分组类型已知时只显示对应渠道，未知时显示全部
const filteredChannelTypes = computed(() => {
  const groupType = (connectingRate.value?.type || '').toLowerCase()
  const available = connectionCapabilities.value?.channelTypes?.length
    ? connectionCapabilities.value.channelTypes
    : NEW_API_CHANNEL_TYPES
  const suggestedId = connectionCapabilities.value?.suggestedChannelTypeByGroup?.[groupType]
    ?? LEGACY_NEW_API_CHANNEL_SUGGESTIONS[groupType]
  if (suggestedId) {
    return available.filter(channelType => channelType.id === suggestedId)
  }
  return available
})

const canSubmitConnect = computed(() => {
  if (!isAdminReady.value || !connectingRate.value || connectSubmitLocked.value || connectFormLocked.value) return false
  if (connectOutcome.value === 'failed' && connectFailure.value?.retryAllowed === false) return false
  if (connectMode.value === 'bind') {
    return Boolean(selectedKeyId.value && selectedAdminGroupId.value && selectedAdminResourceId.value)
  }
  if (connectOwnGroups.value.length === 0) return false
  if (isC5Import.value && (!currentAccount.value?.id || !isImportAccountPlatform(connectAccountPlatform.value) || connectPriorityError.value || connectConcurrencyError.value ||
    connectOwnGroups.value.some(id => !filteredOwnGroups.value.some(group => group.id === id && canSelectOwnGroup(group))))) return false
  // sub2api admin：分组类型未知时必须手动选择
  if (needsGroupTypeSelection.value && !selectedGroupType.value) return false
  // new-api admin：必须选择渠道类型
  if (needsChannelTypeSelection.value && !selectedChannelType.value) return false
  return true
})

const handleTypeChange = async (event: Event) => {
  const target = event.target as HTMLSelectElement
  clearRateHighlight()
  typeFilter.value = target.value
  page.value = 1
  await loadLocalPage()
}

const formatMultiplier = (value: number | null): string => {
  if (value === null || !Number.isFinite(value)) return t('admin.groupRates.common.placeholder')
  return t('admin.groupRates.format.multiplier', { value: Number(value.toFixed(4)).toString() })
}

const formatDelta = (delta: number | null): string => {
  if (delta === null || !Number.isFinite(delta)) return t('admin.groupRates.common.placeholder')

  const sign = delta > 0 ? '+' : ''
  const deltaValue = `${sign}${Number(delta.toFixed(4)).toString()}`
  return t('admin.groupRates.format.deltaMultiplier', { value: deltaValue })
}

const formatDateTime = (value: string | number | null): string => {
  if (!value) return t('admin.groupRates.common.placeholder')
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return t('admin.groupRates.common.placeholder')
  return new Intl.DateTimeFormat(locale, {
    timeZone: 'Asia/Singapore',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}

const platformLabel = (platform: string | null): string => {
  if (!platform) return t('admin.groupRates.common.unknown')
  if (platform === 'newapi') return t('admin.groupRates.platforms.newapi')
  if (platform === 'sub2api') return t('admin.groupRates.platforms.sub2api')
  return platform
}

const typeLabel = (type: string | null): string => {
  if (!type) return t('admin.groupRates.common.unknown')
  return type
}

const importPlatformLabel = (platform: string): string => {
  const labelKeys: Record<string, string> = {
    openai: 'groupTypeOpenai', anthropic: 'groupTypeAnthropic', gemini: 'groupTypeGemini', antigravity: 'groupTypeAntigravity', grok: 'groupTypeGrok', kimi: 'groupTypeKimi', zhipu: 'groupTypeZhipu',
    deepseek: 'groupTypeDeepseek', minimax: 'groupTypeMinimax',
  }
  const key = labelKeys[platform]
  return key ? t(`admin.groupRates.connect.${key}`) : typeLabel(platform)
}

const platformClasses = (platform: string | null): string => {
  if (platform === 'newapi') return 'border-sky-400/30 bg-sky-500/10 text-sky-600 dark:text-sky-300'
  if (platform === 'sub2api') return 'border-violet-400/30 bg-violet-500/10 text-violet-600 dark:text-violet-300'
  return 'border-border/60 bg-surface-elevated text-muted-foreground'
}

const typeClasses = (type: string | null): string => {
  if (!type) return 'border-border/60 bg-surface-elevated text-muted-foreground'

  let hash = 0
  for (const char of type) {
    hash = (hash + char.charCodeAt(0)) % 4
  }

  return [
    'border-emerald-400/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300',
    'border-amber-400/30 bg-amber-500/10 text-amber-600 dark:text-amber-300',
    'border-rose-400/30 bg-rose-500/10 text-rose-600 dark:text-rose-300',
    'border-cyan-400/30 bg-cyan-500/10 text-cyan-600 dark:text-cyan-300',
  ][hash]
}

const deltaClasses = (delta: number | null): string => {
  if (delta === null || !Number.isFinite(delta)) return 'bg-surface-elevated text-muted-foreground border-border/50'
  if (delta > 0) return 'bg-red-500/10 text-red-500 border-red-500/20'
  if (delta < 0) return 'bg-emerald-500/10 text-emerald-500 border-emerald-500/20'
  return 'bg-primary/10 text-primary border-primary/20'
}

const historyActionLabel = (rate: GroupRate): string => (
  t('admin.groupRates.actions.viewHistoryForRate', {
    site: rate.siteName,
    group: rate.groupName,
    delta: formatDelta(rate.delta),
  })
)

const openHistory = async (rate: GroupRate) => {
  invalidatePage()
  invalidateAttention()
  selectedRate.value = rate
  isHistoryOpen.value = true
  await loadHistory({
    siteId: rate.siteId,
    groupId: rate.groupId,
    groupName: rate.groupId || rate.groupName,
    platform: rate.platform,
  })
}

const closeHistory = () => {
  isHistoryOpen.value = false
  selectedRate.value = null
}

const openTypeEditor = (rate: GroupRate) => {
  if (typeEditBlocked(rate)) return
  invalidatePage()
  invalidateAttention()
  editingRate.value = rate
  editTypeValue.value = rate.type ?? ''
}

const closeTypeEditor = () => {
  editingRate.value = null
  editTypeValue.value = ''
}

const openConnector = async (rate: GroupRate) => {
  if (connectionOperationBlocked(rate)) return
  invalidatePage()
  invalidateAttention()
  connectorGeneration++
  connectOutcome.value = 'editing'
  connectSubmitLocked.value = false
  connectResult.value = null
  connectFailure.value = null
  realConnectError.value = ''
  connectRefreshError.value = ''
  connectGroupSelectionNotice.value = ''
  resetImportSettings()
  connectingRate.value = rate
  connectOwnGroups.value = []
  connectMode.value = 'real'
  selectedGroupType.value = isC5Import.value && isImportAccountPlatform(connectUpstreamPlatform.value) ? connectUpstreamPlatform.value : ''
  selectedChannelType.value = 0
  addToPricingMapping.value = true
  connectOperationId.value = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
  const generation = connectorGeneration
  const workspace = currentAccount.value?.id
  const isCurrent = () => pageIsMounted && generation === connectorGeneration && workspace === currentAccount.value?.id
  try { await loadMySiteMappingData(false, isCurrent) } catch {
    if (isCurrent()) realConnectError.value = t('admin.groupRates.connect.importSettings.loadFailed')
  }
}

const isActiveResourceStatus = (status: string): boolean => ['1', 'active', 'enabled'].includes(status.toLowerCase())

const resourceStatusLabel = (status: string): string => (
  isActiveResourceStatus(status)
    ? t('admin.groupRates.connect.resourceActive')
    : t('admin.groupRates.connect.resourceInactive')
)

const adminResourceTypeLabel = (resource: AdminResourceOption): string => {
  if (adminPlatform.value === 'newapi') {
    const channelType = NEW_API_CHANNEL_TYPES.find(item => item.id === Number(resource.type))
    if (channelType) return channelType.name
  }
  return resource.platform || resource.type || t('admin.groupRates.common.unknown')
}

const closeConnector = () => {
  if (connectSubmitLocked.value || connectOutcome.value === 'submitting') return
  connectorGeneration++
  connectingRate.value = null
  connectOwnGroups.value = []
  connectMode.value = 'real'
  realConnectError.value = ''
  selectedGroupType.value = ''
  selectedChannelType.value = 0
  upstreamKeys.value = []
  selectedKeyId.value = ''
  isLoadingKeys.value = false
  selectedAdminGroupId.value = ''
  adminResources.value = []
  selectedAdminResourceId.value = ''
  isLoadingAdminResources.value = false
  addToPricingMapping.value = true
  connectOperationId.value = ''
  connectResult.value = null
  connectFailure.value = null
  connectRefreshError.value = ''
  connectOutcome.value = 'editing'
}

const setConnectMode = async (mode: 'real' | 'bind') => {
  if (connectFormLocked.value || mode === connectMode.value) return
  connectorGeneration++
  isLoadingKeys.value = false
  isLoadingAdminResources.value = false
  resetImportSettings()
  connectResult.value = null
  connectFailure.value = null
  connectRefreshError.value = ''
  connectOutcome.value = 'editing'
  connectMode.value = mode
  connectOwnGroups.value = []
  selectedGroupType.value = isC5Import.value && isImportAccountPlatform(connectUpstreamPlatform.value) ? connectUpstreamPlatform.value : ''
  selectedChannelType.value = 0
  selectedKeyId.value = ''
  selectedAdminGroupId.value = ''
  selectedAdminResourceId.value = ''
  adminResources.value = []
  realConnectError.value = ''
  if (mode === 'bind' && connectingRate.value) {
    await loadUpstreamKeys(connectingRate.value)
  }
}

const loadAdminResourcesForGroup = async (groupId: string) => {
  if (connectFormLocked.value) return
  selectedAdminGroupId.value = groupId
  selectedAdminResourceId.value = ''
  adminResources.value = []
  if (!groupId) return
  const generation = connectorGeneration
  const workspace = currentAccount.value?.id
  const isCurrent = () => pageIsMounted && generation === connectorGeneration && workspace === currentAccount.value?.id && selectedAdminGroupId.value === groupId
  isLoadingAdminResources.value = true
  try {
    const resources = await listAdminResources(groupId)
    if (isCurrent()) adminResources.value = resources
  } catch {
    if (isCurrent()) {
      adminResources.value = []
      realConnectError.value = t('admin.groupRates.connect.adminResourcesFailed')
    }
  } finally {
    if (isCurrent()) isLoadingAdminResources.value = false
  }
}

const handleAdminGroupChange = (event: Event) => {
  void loadAdminResourcesForGroup((event.target as HTMLSelectElement).value)
}

const submitTypeEditor = async () => {
  if (!editingRate.value) return
  await saveType(editingRate.value, editTypeValue.value.trim(), async () => { clearCheckState(); await loadLocalPage() })
  closeTypeEditor()
}

const loadMySiteMappingData = async (force = false, isCurrent: () => boolean = () => true) => {
  if (hasLoadedMappingOptions.value && !force) return
  isActionLoading.value = true
  try {
    const options = await getMySiteMappingOptions()
    if (!isCurrent()) return
    ownGroups.value = options.ownGroups
    mySiteMappings.value = options.mappings ?? []
    connectionCapabilities.value = options.connectionCapabilities ?? null
    hasLoadedMappingOptions.value = true
  } finally {
    if (isCurrent() && !connectSubmitLocked.value) isActionLoading.value = false
  }
}

const toggleOwnGroup = (groupId: string) => {
  if (connectFormLocked.value || !filteredOwnGroups.value.some(group => group.id === groupId && canSelectOwnGroup(group))) return
  const index = connectOwnGroups.value.indexOf(groupId)
  if (index === -1) {
    connectOwnGroups.value = [...connectOwnGroups.value, groupId]
  } else {
    connectOwnGroups.value = connectOwnGroups.value.filter(id => id !== groupId)
  }
}

const submitConnector = async () => {
  if (!connectingRate.value || !canSubmitConnect.value) return

  if (connectMode.value === 'bind') {
    await submitBind()
  } else {
    await submitRealConnect()
  }
}

const realConnectError = ref('')

const managedResourceErrorMessage = (error: unknown, fallbackKey: string): string => {
  const key = error instanceof Error ? error.message : ''
  let message = te(key) ? t(key) : t(fallbackKey)
  if (['admin.mySites.errors.resourcesPendingVerification', 'admin.mySites.errors.accountCreationPendingVerification', 'admin.mySites.errors.compensationPendingVerification', 'admin.mySites.errors.upstreamKeyCleanupPendingVerification'].includes(key) && error instanceof Error) {
    const retained = error as Error & { reason?: string; groupId?: string; groupName?: string; adminResourceId?: string; upstreamKeyId?: string }
    if (retained.reason && retained.reason !== key && te(retained.reason)) message += ` ${t(retained.reason)}`
    if (retained.groupId || retained.groupName) message += ` ${t('admin.mySites.errors.blockingGroup', { groupName: retained.groupName || '—', groupId: retained.groupId || '—' })}`
    message += ` ${t('admin.mySites.errors.retainedResources', { adminResourceId: retained.adminResourceId || '—', upstreamKeyId: retained.upstreamKeyId || '—' })}`
  }
  return message
}

const refreshAfterMutation = async (isCurrent: () => boolean = () => true, confirmRefresh = false, localOnly = false): Promise<boolean> => {
  if (!isCurrent()) return false
  if (!localOnly) void loadOwnGroupRates()
  const generation = invalidatePage()
  invalidateAttention()
  try {
    clearCheckState()
    const [connectionsLoaded] = await Promise.all([loadLocalPage({ generation, isCurrent }), localOnly ? Promise.resolve() : loadMySiteMappingData(true, isCurrent), loadAttention(isCurrent)])
    if (!isCurrent()) return false
    // Closing a dialog can resume a search or a new website. That active read
    // owns the page; the deliberately replaced old read has no failure to report.
    if (generation !== pageGeneration) return true
    if (confirmRefresh && (!connectionsLoaded || errorKey.value)) throw new Error('refresh')
    return true
  } catch {
    if (!isCurrent()) return false
    if (generation !== pageGeneration) return true
    errorKey.value = 'admin.groupRates.errors.refreshFailed'
    return false
  }
}

const handlePlatformChange = async (event: Event) => {
  const target = event.target as HTMLSelectElement
  clearRateHighlight()
  platformFilter.value = target.value
  page.value = 1
  await loadLocalPage()
}

const handleSiteChange = async (event: Event) => {
  if (isAnyDialogOpen.value) return
  const target = event.target as HTMLSelectElement
  const previousSite = queryValue(route.query.siteId)
  if (target.value === previousSite) return
  clearRateHighlight()
  const query = { ...route.query }
  if (target.value) query.siteId = target.value
  else delete query.siteId
  delete query.focusGroup
  delete query.focusGroupId
  await router.replace({ query })
}
watch(() => routeScopeKey(), (key) => {
  if (key === consumedFocusRoute) { consumedFocusRoute = ''; return }
  if (isAnyDialogOpen.value) { deferredRouteChange = true; return }
  siteFilter.value = queryValue(route.query.siteId)
  pendingFocusGroup.value = queryValue(route.query.focusGroup)
  pendingFocusGroupId.value = queryValue(route.query.focusGroupId)
  focusedRateKey.value = ''
  focusError.value = ''
  if (pageIsMounted) void enterPage()
})
const goToPage = async (target: number) => {
  const next = Math.min(Math.max(target, 1), totalPages.value || 1)
  if (next === page.value || isAnyDialogOpen.value) return
  page.value = next
  clearRateHighlight()
  await loadLocalPage()
}

const handleStatusChange = async (status: 'all' | 'mapped' | 'unmapped') => {
  if (status === statusFilter.value || isLoading.value) return
  clearRateHighlight()
  statusFilter.value = status
  page.value = 1
  await loadLocalPage()
}

const handleSortChange = async (event: Event) => {
  const target = event.target as HTMLSelectElement
  clearRateHighlight()
  sortMode.value = target.value as 'multiplierAsc' | 'multiplierDesc' | 'siteNameAsc' | 'groupNameAsc'
  page.value = 1
  await loadLocalPage()
}

const submitRealConnect = async () => {
  if (!connectingRate.value || !canSubmitConnect.value || connectSubmitLocked.value) return
  const useImportContract = isC5Import.value
  const generation = connectorGeneration
  const workspace = currentAccount.value?.id ?? ''
  const platform = adminPlatform.value
  const target = connectingRate.value
  const isCurrent = () => pageIsMounted && generation === connectorGeneration && workspace === (currentAccount.value?.id ?? '') && platform === adminPlatform.value && connectingRate.value === target
  if (connectOutcome.value === 'failed') connectOperationId.value = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
  connectSubmitLocked.value = true
  connectOutcome.value = 'submitting'
  connectFailure.value = null
  connectRefreshError.value = ''
  realConnectError.value = ''
  isActionLoading.value = true
  const payload: RealConnectRequest = {
    upstreamSiteId: connectingRate.value.siteId,
    upstreamGroupId: connectingRate.value.groupId ?? '',
    upstreamGroupName: connectingRate.value.groupName,
    groupType: useImportContract ? connectAccountPlatform.value : selectedGroupType.value,
    channelType: selectedChannelType.value || undefined,
    ownGroupIds: [...connectOwnGroups.value],
    addToPricingMapping: addToPricingMapping.value,
    operationId: connectOperationId.value,
    ...(useImportContract ? { accountSettings: {
      priorityMode: connectPriorityMode.value, priority: Number(connectPriority.value), concurrency: Number(connectConcurrency.value),
      passthrough: supportsConnectPassthrough.value && connectPassthrough.value, poolMode: connectPoolMode.value,
      upstreamBillingProbeEnabled: connectProbeEnabled.value,
    } } : {}),
  }
  try {
    const result = useImportContract ? await realConnect(payload, workspace) : await realConnect(payload)
    if (!isCurrent()) return
    if (useImportContract && result.configurationStatus === 'confirmed' && result.configuration?.observation === 'creation' &&
      ['synced', 'not_required'].includes(result.configuration.modelState) && !result.diagnostics?.length && !result.diagnosticsUnavailable) {
      // 本次新建且主站已确认：直接关闭弹窗，列表在后台刷新
      connectSubmitLocked.value = false
      isActionLoading.value = false
      connectOutcome.value = 'editing'
      closeConnector()
      const isCurrentPage = () => pageIsMounted && workspace === (currentAccount.value?.id ?? '') && platform === adminPlatform.value
      if (!await refreshAfterMutation(isCurrentPage, true) && isCurrentPage()) errorKey.value = 'admin.groupRates.connect.importResult.refreshFailed'
    } else if (useImportContract) {
      connectResult.value = result
      connectOutcome.value = 'succeeded'
      if (!await refreshAfterMutation(isCurrent, true) && isCurrent()) connectRefreshError.value = t('admin.groupRates.connect.importResult.refreshFailed')
    } else {
      connectSubmitLocked.value = false
      connectOutcome.value = 'editing'
      closeConnector()
      await refreshAfterMutation()
    }
  } catch (error) {
    if (!isCurrent()) return
    realConnectError.value = managedResourceErrorMessage(error, 'admin.groupRates.connect.realFailed')
    if (useImportContract) {
      const failure = error instanceof Error && error.name === 'ImportApiError' ? error as ImportApiError : null
      connectFailure.value = failure?.trusted ? failure : null
      if (failure?.trusted) {
        const initialDiagnostics = failure.diagnostics?.filter(diagnostic => diagnostic.stage === failure.stage &&
          diagnostic.message !== 'admin.mySites.errors.importModelSyncWarning' &&
          !['account_cleanup', 'key_cleanup'].includes(diagnostic.stage)) ?? []
        const initialDiagnostic = initialDiagnostics.find(diagnostic => diagnostic.message === failure.message) ?? initialDiagnostics[0]
        const message = initialDiagnostic?.message ?? failure.message
        realConnectError.value = te(message) ? t(message) : t('admin.groupRates.connect.realFailed')
      }
      else if (!(error instanceof Error) || !te(error.message)) realConnectError.value = t('admin.mySites.errors.importResponsePending')
      connectOutcome.value = failure?.trusted && ((failure.stage === 'validation' && failure.cleanup === 'not_needed') || (failure.retryAllowed && ['confirmed', 'not_needed'].includes(failure.cleanup))) ? 'failed' : 'pending'
    } else connectOutcome.value = 'editing'
  } finally {
    if (isCurrent() || !useImportContract) {
      connectSubmitLocked.value = false
      isActionLoading.value = false
    }
  }
}

const loadUpstreamKeys = async (rate: GroupRate) => {
  const generation = connectorGeneration
  const workspace = currentAccount.value?.id
  const isCurrent = () => pageIsMounted && generation === connectorGeneration && workspace === currentAccount.value?.id && connectingRate.value === rate && connectMode.value === 'bind'
  isLoadingKeys.value = true
  try {
    const keys = await listUpstreamKeys(rate.siteId, rate.groupId ?? '', rate.groupName)
    if (isCurrent()) upstreamKeys.value = keys
  } catch {
    if (isCurrent()) upstreamKeys.value = []
  } finally {
    if (isCurrent()) isLoadingKeys.value = false
  }
}

const submitBind = async () => {
  if (!connectingRate.value || !canSubmitConnect.value) return
  const selectedKey = upstreamKeys.value.find(k => k.id === selectedKeyId.value)
  if (!selectedKey) return
  const generation = connectorGeneration
  const workspace = currentAccount.value?.id ?? ''
  const platform = adminPlatform.value
  const target = connectingRate.value
  const isCurrent = () => pageIsMounted && generation === connectorGeneration && workspace === (currentAccount.value?.id ?? '') && platform === adminPlatform.value && connectingRate.value === target
  connectSubmitLocked.value = true
  connectOutcome.value = 'submitting'
  realConnectError.value = ''
  isActionLoading.value = true
  try {
    await realBind({
      upstreamSiteId: target.siteId,
      upstreamGroupId: target.groupId ?? '',
      upstreamGroupName: target.groupName,
      upstreamKeyId: selectedKey.id,
      upstreamKey: selectedKey.key,
      ownGroupIds: [selectedAdminGroupId.value],
      groupType: selectedGroupType.value,
      adminGroupId: selectedAdminGroupId.value,
      adminResourceId: selectedAdminResourceId.value,
      addToPricingMapping: addToPricingMapping.value,
      operationId: connectOperationId.value,
    })
    if (!isCurrent()) return
    connectSubmitLocked.value = false
    isActionLoading.value = false
    connectOutcome.value = 'editing'
    closeConnector()
    await refreshAfterMutation(() => pageIsMounted && workspace === (currentAccount.value?.id ?? '') && platform === adminPlatform.value)
  } catch (error) {
    if (!isCurrent()) return
    const key = error instanceof Error ? error.message : ''
    if (platform === 'sub2api' && key === 'admin.mySites.errors.importInProgress') {
      realConnectError.value = t(key)
      connectOutcome.value = 'pending'
    } else {
      realConnectError.value = t(key === 'admin.mySites.errors.connectionExists' ? key : 'admin.groupRates.connect.bindFailed')
      connectOutcome.value = 'editing'
    }
  } finally {
    if (isCurrent()) {
      connectSubmitLocked.value = false
      isActionLoading.value = false
    }
  }
}

const freezeExpectedConnection = (connection: RealConnection, workspace: string): ExpectedRealConnection => ({
  workspaceAdminAccountId: workspace, id: connection.id, upstreamSiteId: connection.upstreamSiteId,
  upstreamGroupId: connection.upstreamGroupId || '', upstreamGroupName: connection.upstreamGroupName,
  upstreamKeyId: connection.upstreamKeyId || '', adminAccountId: connection.adminAccountId || '',
  adminPlatform: connection.adminPlatform || '', upstreamPlatform: connection.upstreamPlatform || '',
  provisioningMode: connection.provisioningMode || '', status: connection.status || '',
  ownGroupIds: [...new Set(connection.ownGroupIds ?? [])].sort(), ownGroupNames: [...new Set(connection.ownGroupNames ?? [])].sort(),
  pricingMappingEnabled: connection.pricingMappingEnabled ?? false,
})
const canEditMainSiteGroups = (rate: GroupRate): boolean => {
  const connection = realConnectionForRate(rate)
  return !rate.deleted && connectionDataKnown.value && Boolean(connection && (!connection.status || connection.status === 'active') && connection.provisioningMode === 'managed' && connection.adminPlatform === 'sub2api' && connection.adminAccountId?.trim())
}
const resetGroupEditor = () => {
  groupEditorGeneration++
  groupEditorController?.abort()
  groupEditorController = null
  groupEditingRate.value = null
  groupEditorData.value = null
  groupEditorSelected.value = []
  groupEditorRemovePricing.value = true
  groupEditorReadError.value = ''
  groupEditorResultNotice.value = ''
  groupEditorExpected = null
  groupEditorWorkspace = ''
  groupEditorConnectionId = ''
  groupEditorRequestSent = false
  groupEditorState.value = 'loading'
}
const closeGroupEditor = () => {
  if (groupEditorBusy.value) return
  const workspace = groupEditorWorkspace
  const sent = groupEditorRequestSent
  resetGroupEditor()
  if (sent) void refreshAfterMutation(() => pageIsMounted && workspace === (currentAccount.value?.id ?? ''), false, false)
}
const readGroupEditor = async () => {
  if (!groupEditingRate.value || groupEditorBusy.value) return
  const generation = ++groupEditorGeneration
  const workspace = groupEditorWorkspace
  groupEditorController?.abort()
  const controller = new AbortController()
  groupEditorController = controller
  groupEditorState.value = 'loading'
  groupEditorData.value = null
  groupEditorExpected = null
  groupEditorReadError.value = ''
  const isCurrent = () => pageIsMounted && Boolean(groupEditingRate.value) && generation === groupEditorGeneration && workspace === (currentAccount.value?.id ?? '')
  try {
    const data = await readConnectionMainSiteGroups(groupEditorConnectionId, workspace, controller.signal)
    if (!isCurrent()) return
    groupEditorData.value = data
    groupEditorExpected = freezeExpectedConnection(data.connection, workspace)
    groupEditorSelected.value = data.currentGroups.map(group => group.id)
    groupEditorRemovePricing.value = true
    groupEditorState.value = 'ready'
  } catch (error) {
    if (!isCurrent() || error instanceof Error && error.name === 'AbortError') return
    groupEditorReadError.value = t('admin.groupRates.groupEditor.readFailed', { reason: displayCause(error) })
    groupEditorState.value = 'readFailed'
  } finally { if (isCurrent()) groupEditorController = null }
}
const openGroupEditor = (rate: GroupRate) => {
  if (!canEditMainSiteGroups(rate) || connectionOperationBlocked(rate) || isAnyDialogOpen.value) return
  const connection = realConnectionForRate(rate)!
  invalidatePage()
  invalidateAttention()
  resetGroupEditor()
  groupEditingRate.value = rate
  groupEditorWorkspace = currentAccount.value?.id ?? ''
  groupEditorConnectionId = connection.id
  void readGroupEditor()
}
const groupEditorResultText = (result: MainSiteGroupMutationResult): string => {
  const fallback: Record<string, string> = {
    unchanged: t('admin.groupRates.groupEditor.unchangedResult'),
    main_changed_hub_not_saved: t('admin.groupRates.groupEditor.mainChangedResult'),
    main_unknown: t('admin.groupRates.groupEditor.mainUnknownResult'),
    hub_unknown: t('admin.groupRates.groupEditor.hubUnknownResult'),
  }
  return [result.message || fallback[result.outcome], result.detail, result.next].filter(Boolean).join('；')
}
const submitGroupEditor = async (sync = false) => {
  const data = groupEditorData.value
  if (!data || !groupEditorExpected || groupEditorState.value !== 'ready' || !sync && (!groupEditorSelected.value.length || groupEditorUnchanged.value) || sync && data.consistent) return
  const workspace = groupEditorWorkspace
  const generation = groupEditorGeneration
  const isCurrent = () => pageIsMounted && Boolean(groupEditingRate.value) && generation === groupEditorGeneration && workspace === (currentAccount.value?.id ?? '')
  const request = { workspaceAdminAccountId: workspace, expectedConnection: groupEditorExpected, expectedMainSiteGroupIds: data.currentGroups.map(group => group.id), removeOldPricingMapping: groupEditorRemovePricing.value }
  groupEditorState.value = sync ? 'syncing' : 'submitting'
  groupEditorResultNotice.value = ''
  groupEditorRequestSent = true
  try {
    const result = sync ? await syncConnectionHubGroups(groupEditorConnectionId, request) : await changeConnectionMainSiteGroups(groupEditorConnectionId, { ...request, targetGroupIds: [...groupEditorSelected.value] })
    if (!isCurrent()) return
    if (result.outcome === 'completed') {
      resetGroupEditor()
      await refreshAfterMutation(() => pageIsMounted && workspace === (currentAccount.value?.id ?? ''), true, false)
      return
    }
    groupEditorResultNotice.value = groupEditorResultText(result)
  } catch (error) {
    if (!isCurrent()) return
    groupEditorResultNotice.value = error instanceof MainSiteGroupsApiError
      ? t('admin.groupRates.groupEditor.' + (error.kind === 'network' ? 'noResponse' : 'invalidResponse'))
      : displayCause(error)
  }
  if (!isCurrent()) return
  groupEditorState.value = 'ready'
  await readGroupEditor()
}
const attentionSiteName = (connection: RealConnection) => connection.siteName || siteOptions.value.find(site => site.id === connection.upstreamSiteId)?.name || connection.upstreamSiteId
const attentionSiteOptions = computed(() => {
  const options = new Map(siteOptions.value.map(site => [site.id, site.name]))
  for (const item of attentionItems.value) if (!options.has(item.connection.upstreamSiteId)) options.set(item.connection.upstreamSiteId, attentionSiteName(item.connection))
  return Array.from(options, ([id, name]) => ({ id, name })).sort((a, b) => a.name.localeCompare(b.name))
})
const invalidateAttention = (clear = false) => {
  attentionGeneration++
  attentionController?.abort()
  attentionLoading.value = false
  if (clear) {
    attentionItems.value = []
    attentionTotal.value = 0
    attentionTotalPages.value = 0
    attentionKnown.value = false
    attentionError.value = ''
    attentionCheckSummary.value = ''
  }
}
const loadAttention = async (additionalCurrent: () => boolean = () => true): Promise<boolean> => {
  const workspace = currentAccount.value?.id ?? ''
  if (!workspace) { invalidateAttention(true); return false }
  const generation = ++attentionGeneration
  const controller = new AbortController()
  attentionController?.abort()
  attentionController = controller
  const siteId = attentionSiteFilter.value
  const requestedPage = attentionPage.value
  const isCurrent = () => pageIsMounted && !controller.signal.aborted && generation === attentionGeneration && workspace === (currentAccount.value?.id ?? '') && additionalCurrent()
  attentionLoading.value = true
  try {
    const response = await listConnectionsNeedingAttention({ workspaceAdminAccountId: workspace, siteId, page: requestedPage }, controller.signal)
    if (!isCurrent()) return false
    attentionItems.value = response.items
    attentionTotal.value = response.total
    attentionTotalPages.value = response.totalPages
    attentionKnown.value = true
    attentionError.value = ''
    if (response.total === 0 && !siteId) attentionExpanded.value = false
    return true
  } catch (error) {
    if (!isCurrent()) return false
    attentionKnown.value = false
    attentionError.value = `已有对接暂时无法读取：${displayCause(error)}。当前不能确认是否有记录需要处理；倍率仍可查看，请重试读取，并检查Hub后端对接读取记录。`
    return false
  } finally {
    if (attentionController === controller && !controller.signal.aborted) attentionController = null
    if (isCurrent()) attentionLoading.value = false
  }
}
const changeAttentionSite = async () => {
  if (isCheckingConnections.value || isAnyDialogOpen.value) return
  attentionPage.value = 1
  attentionItems.value = []
  attentionKnown.value = false
  attentionCheckSummary.value = ''
  await loadAttention()
}
const goToAttentionPage = async (target: number) => {
  if (attentionLoading.value || isCheckingConnections.value || isAnyDialogOpen.value || target < 1 || target > attentionTotalPages.value) return
  attentionPage.value = target
  attentionItems.value = []
  attentionKnown.value = false
  await loadAttention()
}
const checkAttentionSite = async (connection: RealConnection) => {
  if (isCheckingConnections.value || isAnyDialogOpen.value || !attentionKnown.value || !upstreamSiteOptions.value.some(site => site.id === connection.upstreamSiteId)) return
  const workspace = currentAccount.value?.id ?? ''
  const scope = { workspaceAdminAccountId: workspace, siteId: connection.upstreamSiteId }
  const before = Array.from(new Map([...realConnectionsData.value, ...attentionItems.value.map(item => item.connection)].filter(item => item.upstreamSiteId === scope.siteId).map(item => [item.id, { ...item, ownGroupIds: [...item.ownGroupIds], ownGroupNames: [...(item.ownGroupNames ?? [])] }])).values())
  const generation = invalidatePage(true)
  invalidateAttention()
  const controller = new AbortController()
  checkController = controller
  const isCurrent = () => pageIsMounted && generation === pageGeneration && workspace === (currentAccount.value?.id ?? '') && !controller.signal.aborted && !isAnyDialogOpen.value
  lastCheckScope = scope
  checkIdentities = new Map(before.map(connection => [connection.id, connection]))
  unconfirmedIds.value = new Set(before.filter(applicableConnection).map(connection => connection.id))
  isCheckingConnections.value = true
  attentionCheckingSite.value = scope.siteId
  attentionCheckSummary.value = ''
  let result: ScopedCheckResult | undefined
  let failure = ''
  try { result = await checkScopedRealConnections({ ...scope, mode: 'manual' }, controller.signal) }
  catch (error) {
    if (!isCurrent()) return
    if (error instanceof ScopedCheckApiError) { result = error.result; failure = [displayCause(error), error.detail, error.next].filter(Boolean).join('；') }
    else failure = `本站账号核对未完成：${displayCause(error)}；请重新读取已有对接，并查看Hub后端账号检查记录后重试。`
  }
  if (!isCurrent()) return
  const [reread, ratesRead] = await Promise.all([loadAttention(isCurrent), loadLocalPage({ generation, isCurrent })])
  if (!isCurrent()) return
  // Only a before/after public identity and the exact confirmed status can
  // describe a row as checked. This never authorizes remote deletion here.
  const originals = new Map(before.map(item => [item.id, freezeExpectedConnection(item, workspace)]))
  const confirmed = new Set<string>()
  const changed = new Set(result?.changedConnectionIds ?? [])
  if (reread && result) {
    for (const connection of [...attentionItems.value.map(item => item.connection), ...(ratesRead ? realConnectionsData.value : [])]) {
      if (!result.confirmedConnectionIds.includes(connection.id)) continue
      const original = originals.get(connection.id)
      const current = freezeExpectedConnection(connection, workspace)
      if (original && current.status === result.confirmedStatuses[current.id] && JSON.stringify({ ...original, status: current.status }) === JSON.stringify(current)) { confirmed.add(current.id); checkIdentities.set(current.id, { ...connection }) }
      else changed.add(current.id)
    }
  }
  confirmedIds.value = confirmed
  changedIds.value = changed
  skippedIds.value = new Set(result?.skippedConnectionIds ?? [])
  unconfirmedIds.value = new Set([...before.filter(applicableConnection).map(connection => connection.id), ...(result?.unconfirmedConnectionIds ?? [])].filter(id => !confirmed.has(id) && !skippedIds.value.has(id)))
  const attentionConfirmed = attentionItems.value.filter(item => confirmed.has(item.connection.id)).length
  attentionCheckSummary.value = reread
    ? `本站账号核对已结束，本页有${attentionConfirmed}条对接的账号状态已确认；其他记录仍显示上次状态，可重新核对。核对覆盖这个网站全部已有对接，本次没有创建、删除或修改主站账号。`
    : '本站账号核对已结束，但最新已有对接暂时无法读取，当前不能确认本页状态；请重试读取，并查看Hub后端账号检查记录。本次没有创建、删除或修改主站账号。'
  connectionCheckError.value = [failure, ...(result?.issues.map(issue => `${issue.detail}。${issue.next}`) ?? [])].filter(Boolean).join(' ')
  isCheckingConnections.value = false
  attentionCheckingSite.value = ''
}
const selectDisconnect = (connection: RealConnection, labels: { siteId: string; siteName: string; groupName: string; pricingMapped?: boolean }, maintenance: boolean) => {
  invalidatePage()
  invalidateAttention()
  disconnectGeneration++
  disconnectConnection.value = { ...connection, ownGroupIds: [...connection.ownGroupIds], ownGroupNames: [...(connection.ownGroupNames ?? [])] }
  disconnectWorkspace = currentAccount.value?.id ?? ''
  disconnectingRate.value = labels
  disconnectIsMaintenance.value = maintenance
  disconnectMode.value = 'unlink'
  disconnectRemovePricing.value = maintenance ? false : connection.pricingMappingEnabled ?? Boolean(labels.pricingMapped)
  disconnectError.value = ''
}
const openDisconnect = (rate: GroupRate) => {
  if (connectionOperationBlocked(rate)) return
  const selected = realConnectionForRate(rate)
  if (selected) selectDisconnect(selected, { siteId: rate.siteId, siteName: rate.siteName, groupName: rate.groupName, pricingMapped: rate.pricingMapped }, false)
}
const openAttentionDisconnect = (connection: RealConnection) => {
  if (isCheckingConnections.value || !attentionKnown.value || isAnyDialogOpen.value) return
  selectDisconnect(connection, { siteId: connection.upstreamSiteId, siteName: attentionSiteName(connection), groupName: connection.upstreamGroupName }, true)
}
const closeDisconnect = () => {
  disconnectGeneration++
  disconnectingRate.value = null
  disconnectConnection.value = null
  disconnectWorkspace = ''
  disconnectIsMaintenance.value = false
  disconnectMode.value = 'unlink'
  disconnectRemovePricing.value = true
  disconnectError.value = ''
}
const submitDisconnect = async () => {
  const conn = disconnectConnection.value
  if (!disconnectingRate.value || !conn || isDisconnecting.value) return
  const workspace = disconnectWorkspace
  if (workspace !== (currentAccount.value?.id ?? '')) { disconnectError.value = '工作区已变化，本次没有执行解绑；请关闭窗口后在当前工作区重新打开对应记录。'; return }
  const generation = disconnectGeneration
  const maintenance = disconnectIsMaintenance.value
  const isCurrent = () => pageIsMounted && generation === disconnectGeneration && workspace === (currentAccount.value?.id ?? '')
  isDisconnecting.value = true
  disconnectError.value = ''
  try {
    await realDisconnect({ connectionId: conn.id, mode: maintenance ? 'unlink' : disconnectMode.value, removePricingMapping: disconnectRemovePricing.value,
      ...(maintenance ? { expectedConnection: freezeExpectedConnection(conn, workspace) } : {}),
    })
    if (!isCurrent()) return
    closeDisconnect()
    await refreshAfterMutation(() => pageIsMounted && workspace === (currentAccount.value?.id ?? ''), false, maintenance)
  } catch (error) {
    if (!isCurrent()) return
    disconnectError.value = maintenance ? `对接${conn.adminAccountName || conn.id}的本地解除未完成：${displayCause(error)}` : managedResourceErrorMessage(error, 'admin.groupRates.disconnect.failed')
  } finally { if (pageIsMounted && workspace === (currentAccount.value?.id ?? '') && (generation === disconnectGeneration || generation + 1 === disconnectGeneration && !disconnectConnection.value)) isDisconnecting.value = false }
}

const historyTitle = computed(() => {
  if (!selectedRate.value) return t('admin.groupRates.history.title')
  return t('admin.groupRates.history.titleWithGroup', {
    site: selectedRate.value.siteName,
    group: selectedRate.value.groupName,
  })
})

const editTypeTitle = computed(() => {
  if (!editingRate.value) return t('admin.groupRates.edit.title')
  return t('admin.groupRates.edit.titleWithGroup', {
    site: editingRate.value.siteName,
    group: editingRate.value.groupName,
  })
})

const historyRowKey = (row: GroupRateHistoryRow, index: number): string => (
  `${row.siteId}-${row.groupId || row.groupName}-${row.platform ?? 'all'}-${row.createdAt ?? index}`
)

</script>

<template>
  <div class="flex flex-col space-y-6" @click.capture="clearRateHighlight">
    <div class="flex max-w-full w-fit shrink-0 items-center gap-1 overflow-x-auto rounded-lg border border-border/50 bg-surface p-1" role="tablist" :aria-label="t('admin.menu.groupRates')">
      <button
        v-for="tab in (['all', 'mapped', 'unmapped'] as const)"
        :key="tab"
        type="button"
        role="tab"
        :disabled="isCheckingConnections || isAnyDialogOpen"
        :aria-selected="statusFilter === tab"
        aria-controls="group-rates-panel"
        :class="[
          'shrink-0 rounded-md px-4 py-1.5 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
          statusFilter === tab
            ? 'bg-primary text-primary-foreground shadow-sm'
            : 'text-muted-foreground hover:text-foreground hover:bg-surface-elevated'
        ]"
        @click="handleStatusChange(tab)"
      >
        <span>{{ t(`admin.groupRates.tabs.${tab}`) }}</span>
        <span
          class="ml-2 rounded bg-background/60 px-1.5 py-0.5 text-[11px] tabular-nums"
          :class="statusFilter === tab ? 'text-primary-foreground' : 'text-muted-foreground'"
        >
          {{ statusCounts[tab] }}
        </span>
      </button>
    </div>

    <div v-if="attentionError" role="alert" class="shrink-0 rounded-xl border border-warning/20 bg-warning/10 p-4 text-sm text-warning">
      <p class="break-words">{{ attentionError }}</p>
      <Button variant="secondary" size="sm" class="mt-3" :disabled="attentionLoading || isCheckingConnections || isAnyDialogOpen" @click="loadAttention()">重试读取已有对接</Button>
    </div>
    <section v-if="attentionKnown && attentionTotal > 0 || attentionExpanded" class="shrink-0 overflow-hidden rounded-xl border border-border/50 bg-card" aria-label="已有对接维护">
      <div class="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
        <p v-if="attentionKnown && attentionTotal > 0" class="min-w-0 break-words text-sm text-muted-foreground">有{{ attentionTotal }}条已有对接需要查看。上游分组资料目前无法对应这些记录，主站账号和对接仍保留；查看后可核对或解除对接。</p>
        <p v-else class="min-w-0 text-sm text-muted-foreground">{{ attentionKnown ? '当前筛选没有需要查看的已有对接，可调整网站筛选。' : '已有对接资料尚未确认，请重试读取。' }}</p>
        <Button variant="secondary" size="sm" class="shrink-0" :disabled="attentionLoading || isCheckingConnections || isAnyDialogOpen" :aria-expanded="attentionExpanded" aria-controls="group-rates-attention" @click="attentionExpanded = !attentionExpanded">{{ attentionExpanded ? '收起已有对接' : '查看已有对接' }}</Button>
      </div>
      <p class="px-4 pb-4 text-xs text-muted-foreground">只读取Hub保存的资料，不修改主站。</p>
      <div v-if="attentionExpanded" id="group-rates-attention" class="border-t border-border/50">
        <div class="space-y-3 p-4">
          <label class="block text-sm text-muted-foreground">维护网站
            <select v-model="attentionSiteFilter" aria-label="维护网站" class="mt-2 h-10 w-full rounded-lg border border-border/50 bg-surface px-3 text-sm text-foreground sm:max-w-xs" :disabled="attentionLoading || isCheckingConnections || isAnyDialogOpen" @change="changeAttentionSite">
              <option value="">当前工作区全部网站</option>
              <option v-for="site in attentionSiteOptions" :key="site.id" :value="site.id">{{ site.name }}</option>
            </select>
          </label>
          <p class="text-xs text-muted-foreground">网站筛选只改变这里的已有对接；倍率搜索、分类和翻页不会改变此列表。</p>
          <p class="text-xs text-muted-foreground">核对本站账号会检查这个网站全部已有对接的主站账号，不创建、删除或修改主站账号。处理对接会打开解除本地对接选项，不会删除主站账号或上游Key。</p>
          <p v-if="attentionCheckSummary" class="break-words text-sm text-muted-foreground">{{ attentionCheckSummary }}</p>
        </div>
        <div v-if="attentionLoading" class="flex items-center justify-center p-6 text-sm text-muted-foreground"><Loader2 class="mr-2 h-4 w-4 animate-spin" />正在读取已有对接</div>
        <p v-else-if="!attentionKnown" class="p-4 text-sm text-warning">当前无法确认已有对接资料，请重试读取。</p>
        <p v-else-if="attentionItems.length === 0" class="p-4 text-sm text-muted-foreground">当前网站或这一页没有需要查看的已有对接；可调整网站筛选或返回上一页。</p>
        <div v-else class="max-h-96 overflow-auto">
          <table class="w-full min-w-[850px] text-left text-sm">
            <thead class="sticky top-0 border-b border-border/50 bg-surface-elevated"><tr><th class="px-4 py-3">网站 / 上游分组</th><th class="px-4 py-3">主站账号 / 已有对接状态</th><th class="px-4 py-3">站点最后同步</th><th class="px-4 py-3">说明与处理</th></tr></thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="item in attentionItems" :key="item.connection.id" :data-attention-connection-id="item.connection.id" class="align-top">
                <td class="px-4 py-3"><div class="max-w-56 break-words font-medium">{{ attentionSiteName(item.connection) }}</div><div class="mt-1 max-w-56 break-words text-muted-foreground">{{ item.connection.upstreamGroupName || item.connection.upstreamGroupId || '分组名称暂缺' }}</div></td>
                <td class="px-4 py-3"><div class="max-w-56 break-words font-medium">{{ item.connection.adminAccountName || item.connection.adminAccountId || '账号名称暂缺' }}</div><div class="mt-1 text-xs text-muted-foreground">{{ item.connection.status === 'missing' ? '上次核对时主站账号未找到' : item.connection.status === 'active' ? '已保存对接' : '旧对接状态尚未确认' }}</div><div class="mt-1 text-xs text-muted-foreground">对接编号 {{ item.connection.id }}</div></td>
                <td class="px-4 py-3 text-muted-foreground">{{ formatDateTime(item.lastSyncedAt) }}</td>
                <td class="px-4 py-3"><p class="max-w-md break-words text-xs leading-5 text-muted-foreground">{{ item.reason === 'group_not_found' ? 'Hub保存的上游分组资料里未找到这个分组，不能据此确认上游删除。已有对接仍保留，你可以核对主站账号或解除本地对接。' : `这个对接对应的上游分组资料暂时无法确认：${item.detail}。对接和主站账号未改变，请到上游管理核查网站资料。` }}</p>
                  <div class="mt-3 flex flex-wrap gap-2">
                    <Button variant="secondary" size="sm" :disabled="isCheckingConnections || isAnyDialogOpen || !upstreamSiteOptions.some(site => site.id === item.connection.upstreamSiteId)" title="检查这个网站全部已有对接的主站账号，不创建、删除或修改主站账号" @click="checkAttentionSite(item.connection)">核对本站账号</Button>
                    <Button variant="secondary" size="sm" :disabled="isCheckingConnections || isDisconnecting || isAnyDialogOpen" title="打开解除本地对接选项，不会删除主站账号或上游Key" @click="openAttentionDisconnect(item.connection)">处理对接</Button>
                  </div>
                  <p v-if="!upstreamSiteOptions.some(site => site.id === item.connection.upstreamSiteId)" class="mt-2 text-xs text-muted-foreground">网站资料不存在或网站选项未读取，当前不能核对本站账号；仍可解除本地对接。</p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border/50 p-4 text-sm text-muted-foreground">
          <span>共{{ attentionTotal }}条，每页30条，第{{ attentionPage }} / {{ attentionTotalPages }}页</span>
          <div class="flex gap-2"><Button variant="secondary" size="sm" :disabled="attentionPage <= 1 || attentionLoading || isCheckingConnections || isAnyDialogOpen" @click="goToAttentionPage(attentionPage - 1)">上一页已有对接</Button><Button variant="secondary" size="sm" :disabled="attentionPage >= attentionTotalPages || attentionLoading || isCheckingConnections || isAnyDialogOpen" @click="goToAttentionPage(attentionPage + 1)">下一页已有对接</Button></div>
        </div>
      </div>
    </section>

    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 shrink-0">
      <div class="flex w-full flex-1 flex-col gap-3 sm:flex-row sm:flex-wrap lg:flex-nowrap">
        <div class="relative w-full sm:w-80 max-w-sm">
          <Search class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
          <input
            v-model="searchQuery"
            name="groupRateSearch"
            :disabled="isCheckingConnections || isAnyDialogOpen"
            type="text"
            :placeholder="t('admin.groupRates.filters.searchPlaceholder')"
            :aria-label="t('admin.groupRates.filters.searchPlaceholder')"
            autocomplete="off"
            spellcheck="false"
            class="h-10 w-full rounded-lg border border-border/50 bg-surface pl-10 pr-4 text-sm text-foreground outline-none transition-[color,background-color,border-color,box-shadow] placeholder:text-muted-foreground focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/30"
          />
        </div>

        <div class="relative w-full sm:w-56 sm:shrink-0">
          <select
            v-model="siteFilter"
            :disabled="isAnyDialogOpen || isCheckingConnections"
            :aria-label="t('admin.groupRates.filters.siteLabel')"
            class="h-10 w-full appearance-none rounded-lg border border-border/50 bg-surface px-3 pr-8 text-sm text-foreground outline-none transition-[color,background-color,border-color,box-shadow] focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/30"
            @change="handleSiteChange"
          >
            <option value="">{{ t('admin.groupRates.common.allSites') }}</option>
            <option v-for="site in siteOptions" :key="site.id" :value="site.id">{{ site.name }}</option>
          </select>
          <ChevronDown class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        </div>

        <div class="relative w-full sm:w-48 sm:shrink-0">
          <select
            v-model="typeFilter"
            :disabled="isCheckingConnections || isAnyDialogOpen"
            class="h-10 w-full appearance-none rounded-lg border border-border/50 bg-surface px-3 pr-8 text-sm text-foreground outline-none transition-[color,background-color,border-color,box-shadow] focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/30"
            @change="handleTypeChange"
          >
            <option value="">{{ t('admin.groupRates.common.allTypes') }}</option>
            <option v-for="type in types" :key="type" :value="type">{{ typeLabel(type) }}</option>
          </select>
          <ChevronDown class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        </div>

        <div class="relative w-full sm:w-44 sm:shrink-0">
          <select
            v-model="platformFilter"
            :disabled="isCheckingConnections || isAnyDialogOpen"
            class="h-10 w-full appearance-none rounded-lg border border-border/50 bg-surface px-3 pr-8 text-sm text-foreground outline-none transition-[color,background-color,border-color,box-shadow] focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/30"
            @change="handlePlatformChange"
          >
            <option value="">{{ t('admin.groupRates.common.allPlatforms') }}</option>
            <option v-for="platform in platforms" :key="platform" :value="platform">{{ platformLabel(platform) }}</option>
          </select>
          <ChevronDown class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        </div>

        <div class="relative w-full sm:w-52 sm:shrink-0">
          <ArrowUpDown class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground pointer-events-none" />
          <select
            v-model="sortMode"
            :disabled="isCheckingConnections || isAnyDialogOpen"
            class="h-10 w-full appearance-none rounded-lg border border-border/50 bg-surface pl-9 pr-8 text-sm text-foreground outline-none transition-[color,background-color,border-color,box-shadow] focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/30"
            @change="handleSortChange"
          >
            <option value="multiplierAsc">{{ t('admin.groupRates.sort.multiplierAsc') }}</option>
            <option value="multiplierDesc">{{ t('admin.groupRates.sort.multiplierDesc') }}</option>
            <option value="siteNameAsc">{{ t('admin.groupRates.sort.siteNameAsc') }}</option>
            <option value="groupNameAsc">{{ t('admin.groupRates.sort.groupNameAsc') }}</option>
          </select>
          <ChevronDown class="pointer-events-none absolute right-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        </div>
      </div>

      <div class="grid w-full shrink-0 grid-cols-1 gap-2 sm:w-auto sm:grid-cols-2">
        <Button variant="secondary" class="h-10 gap-2" @click="router.push('/admin/group-rate-campaigns?action=create')">
          <Megaphone class="h-4 w-4" />
          {{ t('admin.groupRates.actions.createCampaign') }}
        </Button>
        <Button class="h-10 gap-2 shadow-sm" :disabled="isLoading || isCheckingConnections || isAnyDialogOpen" @click="checkThenLoadRates">
          <Loader2 v-if="isLoading || isCheckingConnections" class="h-4 w-4 animate-spin" />
          <RefreshCw v-else class="h-4 w-4" />
          {{ t('admin.groupRates.actions.refresh') }}
        </Button>
      </div>
    </div>

    <div class="shrink-0 space-y-1 text-sm text-muted-foreground">
      <p>这里显示上次成功读取的可用分组倍率，并保留它们的倍率变化。要取得新倍率，请到上游管理同步对应网站。刷新会检查对应的主站账号，不会创建、删除或修改主站账号。</p>
      <p>“对接的主站分组”按 Hub 保存的对接记录，显示这个上游分组导入或关联的主站账号所在的分组；在主站后台直接改了账号分组，这里不会跟着变。没有对接的分组这三列留空，只标“已用于调价”的也留空。</p>
      <p>“销售倍率”是这些分组在主站上的默认倍率，可与左边的换算后成本倍率对比；不含单独给用户设置的专属倍率，上游和主站的模型标准价不同时只能粗略对比。进入本页或点“刷新数据”时从主站读取，翻页和筛选不重新读取；自动调价或调价活动改价后，请点“刷新数据”查看。读取只查看，不会修改主站。</p>
      <p>利润率按（销售倍率－换算后成本倍率）÷销售倍率估算，与调价映射的口径相同。销售倍率为0、为负数或资料不全时显示“—”，可结合旁边的销售倍率和页面提示查看原因。只查看不会修改主站；上游改价后请先同步对应网站，主站改价后请点“刷新数据”。</p>
      <p v-if="siteFilter">当前查看{{ siteOptions.find(site => site.id === siteFilter)?.name || siteFilter }}。进入或切换网站时只检查这个网站的关联账号；搜索和翻页不会检查其他网站。</p>
      <p v-else>进入时只检查首批30个分组关联的账号；翻页和筛选显示已保存资料，点击刷新可检查当前页。</p>
      <p v-if="isCheckingConnections">正在检查{{ attentionCheckingSite ? (attentionSiteOptions.find(site => site.id === attentionCheckingSite)?.name || attentionCheckingSite) : siteFilter ? (siteOptions.find(site => site.id === siteFilter)?.name || siteFilter) : '当前页' }}的关联账号，倍率可以先查看；账号检查中的操作暂时不可用。</p>
      <p v-else-if="checkSummary">{{ checkSummary }}</p>
      <p>{{ t('admin.groupRates.listDisplayHint') }}</p>
    </div>
    <div v-for="notice in [connectionDataError, siteOptionsError, focusError, ownGroupRatesError].filter(Boolean)" :key="notice" role="alert" class="flex items-start gap-3 rounded-2xl border border-warning/20 bg-warning/10 p-4 text-sm text-warning shrink-0">
      <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" /><span>{{ notice }}</span>
    </div>

    <div v-if="errorKey" class="flex items-start gap-3 rounded-2xl border border-warning/20 bg-warning/10 p-4 text-sm text-warning shrink-0">
      <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
      <span>{{ te(errorKey) ? t(errorKey) : errorKey }}</span>
    </div>

    <div v-if="connectionCheckError" class="flex items-start gap-3 rounded-2xl border border-warning/20 bg-warning/10 p-4 text-sm text-warning shrink-0">
      <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
      <span>{{ connectionCheckError }}</span>
    </div>

    <div id="group-rates-panel" class="flex flex-col overflow-hidden rounded-xl border border-border/50 bg-card shadow-sm" role="tabpanel">
      <div v-if="isLoading" class="flex min-h-48 items-center justify-center text-muted-foreground">
        <Loader2 class="mr-2 h-5 w-5 animate-spin" />
        {{ t('admin.groupRates.status.loading') }}
      </div>

      <div v-else-if="filteredRates.length === 0" class="flex min-h-48 flex-col items-center justify-center px-6 text-center">
        <div class="flex h-12 w-12 items-center justify-center rounded-2xl border border-border/50 bg-surface-elevated text-muted-foreground">
          <History class="h-5 w-5" />
        </div>
        <h3 class="mt-4 font-semibold text-foreground">{{ t(hasActiveRateFilters || hasAnyRateData ? 'admin.groupRates.empty.filteredTitle' : 'admin.groupRates.empty.title') }}</h3>
        <p class="mt-2 max-w-sm text-sm text-muted-foreground">{{ t(hasActiveRateFilters || hasAnyRateData ? 'admin.groupRates.empty.filteredDescription' : 'admin.groupRates.empty.description') }}</p>
      </div>

      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[980px] text-left text-sm relative">
          <thead class="border-b border-border/50 bg-surface-elevated/90 text-xs leading-5 backdrop-blur-sm">
            <tr>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.siteName') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.groupName') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.type') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.platform') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.effectiveMultiplier') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.ownGroups') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.salesMultiplier') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.profitMargin') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.delta') }}</th>
              <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.updatedAt') }}</th>
              <th class="px-6 py-3 text-right font-medium text-muted-foreground">{{ t('admin.groupRates.fields.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/50">
            <tr
              v-for="rate in filteredRates"
              :key="`${rateFocusKey(rate)}\u0000${rate.platform ?? 'all'}`"
              class="transition-colors hover:bg-surface/30"
              :class="focusedRateKey === rateFocusKey(rate) ? 'bg-primary/[0.10] outline outline-1 -outline-offset-1 outline-primary/50' : ''"
              @click="clearRateHighlight"
            >
              <td class="px-4 py-2.5">
                <div class="font-medium text-foreground">{{ rate.siteName }}</div>
              </td>
              <td class="px-4 py-2.5">
                <div class="flex flex-col items-start gap-1">
                  <span class="break-words font-medium text-foreground">{{ rate.groupName }}</span>
                  <span v-if="!connectionDataKnown" class="text-xs text-muted-foreground">对接状态暂时未知，请刷新读取资料</span>
                  <span v-else-if="isMainAccountMissing(rate)" class="inline-flex rounded-md border border-red-500/20 bg-red-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-red-600 dark:text-red-300">{{ t('admin.groupRates.status.mainAccountMissing') }}</span>
                  <span v-else-if="isRealConnected(rate)" class="inline-flex rounded-md border border-emerald-500/20 bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-emerald-600 dark:text-emerald-300">{{ t('admin.groupRates.status.mapped') }}</span>
                  <span v-if="!rate.deleted && isPricingMapped(rate)" class="inline-flex rounded-md border border-amber-500/20 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:text-amber-300">{{ t('admin.groupRates.status.pricingMapped') }}</span>
                </div>
              </td>
              <td class="px-4 py-2.5">
                <div class="flex items-center gap-2">
                  <button type="button" :class="['inline-flex rounded-md border px-2 py-1 text-xs font-semibold uppercase tracking-wider', typeClasses(rate.type)]"
                    :disabled="typeEditBlocked(rate)"
                    :aria-label="typeEditBlocked(rate) ? typeEditBlockedReason : `修改${rate.groupName}的分类，仅保存Hub资料，不修改主站`"
                    :title="typeEditBlocked(rate) ? typeEditBlockedReason : '点击修改分组分类，仅保存Hub资料，不修改主站'" @click="openTypeEditor(rate)">
                    {{ typeLabel(rate.type) }}
                  </button>
                  <span v-if="typeEditBlocked(rate)" class="max-w-56 text-xs text-muted-foreground">{{ typeEditBlockedReason }}</span>
                </div>
              </td>
              <td class="px-4 py-2.5">
                <span :class="['inline-flex rounded-md border px-2 py-1 text-xs font-semibold uppercase tracking-wider', platformClasses(rate.platform)]">
                  {{ platformLabel(rate.platform) }}
                </span>
              </td>
              <td class="px-4 py-2.5 tabular-nums">
                <div class="text-[13px] font-semibold text-foreground">{{ formatMultiplier(rate.currentMultiplier) }}</div>
                <div v-if="rate.upstreamMultiplier != null" class="mt-0.5 text-[11px] text-muted-foreground">
                  {{ t('admin.groupRates.fields.multiplierFormula', {
                    upstream: formatMultiplier(rate.upstreamMultiplier),
                    recharge: Number((rate.rechargeRate ?? 1).toFixed(4)).toString(),
                  }) }}
                </div>
              </td>
              <td class="px-4 py-2.5">
                <span v-if="ownGroupCells(rate).kind === 'unknown'" class="text-xs text-muted-foreground">暂时未知</span>
                <div v-for="line in ownGroupCells(rate).lines" :key="line.key" data-own-group-line class="h-5 leading-5 max-w-48 truncate" :title="line.title">{{ line.name }}</div>
              </td>
              <td class="px-4 py-2.5">
                <span v-if="ownGroupCells(rate).kind === 'unknown'" class="text-xs text-muted-foreground">暂时未知</span>
                <div v-for="line in ownGroupCells(rate).lines" :key="line.key" data-sales-line class="h-5 leading-5 whitespace-nowrap tabular-nums" :class="line.valueTone === 'number' ? 'text-[13px] font-semibold text-foreground' : 'text-xs text-muted-foreground'">{{ line.value }}{{ line.suffix }}</div>
              </td>
              <td class="px-4 py-2.5">
                <span v-if="ownGroupCells(rate).kind === 'unknown'" class="text-xs text-muted-foreground">{{ t('admin.groupRates.common.placeholder') }}</span>
                <div v-for="line in ownGroupCells(rate).lines" :key="line.key" data-profit-margin-line class="h-5 leading-5 whitespace-nowrap text-[13px] tabular-nums font-semibold" :class="profitMarginClass(line.profitMargin)">{{ formatProfitMargin(line.profitMargin) }}{{ line.suffix }}</div>
              </td>
              <td class="px-4 py-2.5">
                <button
                  type="button"
                  :class="[
                    'inline-flex rounded-md border px-2.5 py-1 text-xs font-semibold transition-all hover:-translate-y-px hover:shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                    deltaClasses(rate.delta),
                  ]"
                  :title="historyActionLabel(rate)"
                  :aria-label="historyActionLabel(rate)"
                  @click="openHistory(rate)"
                >
                  {{ formatDelta(rate.delta) }}
                </button>
              </td>
              <td class="px-4 py-2.5 text-muted-foreground tabular-nums">{{ formatDateTime(rate.updatedAt) }}</td>
              <td class="px-4 py-2.5 text-right">
                <div v-if="!rate.deleted && connectionDataKnown" class="flex flex-col items-end gap-1">
                  <span v-if="connectionOperationBlocked(rate)" class="max-w-56 text-xs text-muted-foreground">{{ operationBlockedReason }}</span>
                  <div class="flex flex-nowrap justify-end gap-1.5">
                  <Button
                    v-if="canEditMainSiteGroups(rate)"
                    variant="secondary"
                    size="sm"
                    class="!h-7 gap-1 whitespace-nowrap !px-2.5 !text-xs"
                    :disabled="isActionLoading || isDisconnecting || connectionOperationBlocked(rate)"
                    :title="connectionOperationBlocked(rate) ? operationBlockedReason : t('admin.groupRates.groupEditor.actionHint')"
                    @click="openGroupEditor(rate)"
                  >{{ t('admin.groupRates.groupEditor.action') }}</Button>
                  <Button
                    v-if="isMainAccountMissing(rate)"
                    variant="destructive"
                    size="sm"
                    class="!h-7 gap-1 whitespace-nowrap !px-2.5 !text-xs"
                    :disabled="isActionLoading || isDisconnecting || connectionOperationBlocked(rate)"
                    :title="connectionOperationBlocked(rate) ? operationBlockedReason : undefined"
                    @click="openDisconnect(rate)"
                  >
                    <X class="h-3 w-3" />
                    {{ t('admin.groupRates.disconnect.cleanupMissing') }}
                  </Button>
                  <Button
                    v-else-if="isRealConnected(rate)"
                    variant="destructive"
                    size="sm"
                    class="!h-7 gap-1 whitespace-nowrap !px-2.5 !text-xs"
                    :disabled="isActionLoading || isDisconnecting || connectionOperationBlocked(rate)"
                    :title="connectionOperationBlocked(rate) ? operationBlockedReason : undefined"
                    @click="openDisconnect(rate)"
                  >
                    <X class="h-3 w-3" />
                    {{ t('admin.groupRates.disconnect.action') }}
                  </Button>
                  <Button
                    v-else
                    variant="secondary"
                    size="sm"
                    class="!h-7 gap-1 whitespace-nowrap !px-2.5 !text-xs text-primary hover:text-primary"
                    :disabled="isActionLoading || connectionOperationBlocked(rate)"
                    :title="connectionOperationBlocked(rate) ? operationBlockedReason : undefined"
                    @click="openConnector(rate)"
                  >
                    <Link2 class="h-3 w-3" />
                    {{ t('admin.groupRates.actions.connect') }}
                  </Button>
                  </div>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="flex flex-col gap-3 border-t border-border/50 bg-surface-elevated/30 px-4 py-4 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
          <span>{{ t('admin.groupRates.pagination.total', { total }) }}</span>
          <span>{{ t('admin.groupRates.pagination.pageSize', { pageSize }) }}</span>
          <span>{{ t('admin.groupRates.pagination.currentPage', { page, totalPages }) }}</span>
        </div>

        <div class="flex items-center gap-2">
          <Button variant="secondary" size="sm" :disabled="!canGoPrevious || isCheckingConnections || isAnyDialogOpen" @click="goToPage(page - 1)">
            {{ t('admin.groupRates.pagination.previous') }}
          </Button>
          <Button variant="secondary" size="sm" :disabled="!canGoNext || isCheckingConnections || isAnyDialogOpen" @click="goToPage(page + 1)">
            {{ t('admin.groupRates.pagination.next') }}
          </Button>
        </div>
      </div>
    </div>

    <div v-if="isHistoryOpen" class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div data-group-rates-dialog role="dialog" aria-modal="true" aria-labelledby="group-rate-history-title" tabindex="-1" class="max-h-[calc(100dvh-2rem)] w-full max-w-4xl overflow-hidden overscroll-contain rounded-xl border border-border/50 bg-card shadow-xl">
        <div class="flex items-start justify-between gap-4 border-b border-border/50 p-6">
          <div>
            <h2 id="group-rate-history-title" class="text-xl font-semibold text-foreground">{{ historyTitle }}</h2>
            <p v-if="selectedRate" class="mt-2 text-sm text-muted-foreground">
              {{ t('admin.groupRates.history.subtitle', { platform: platformLabel(selectedRate.platform) }) }}
            </p>
          </div>
          <button class="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground" @click="closeHistory">
            <X class="h-5 w-5" />
            <span class="sr-only">{{ t('admin.groupRates.actions.closeHistory') }}</span>
          </button>
        </div>

        <div v-if="historyErrorKey" class="m-6 flex items-start gap-3 rounded-xl border border-warning/20 bg-warning/10 p-4 text-sm text-warning">
          <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
          <span>{{ t(historyErrorKey) }}</span>
        </div>

        <div v-if="isHistoryLoading" class="flex min-h-[260px] items-center justify-center text-muted-foreground">
          <Loader2 class="mr-2 h-5 w-5 animate-spin" />
          {{ t('admin.groupRates.history.loading') }}
        </div>

        <div v-else-if="history.length === 0" class="flex min-h-[260px] flex-col items-center justify-center px-6 text-center">
          <History class="h-8 w-8 text-muted-foreground" />
          <h3 class="mt-4 font-semibold text-foreground">{{ t('admin.groupRates.history.emptyTitle') }}</h3>
          <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.groupRates.history.emptyDescription') }}</p>
        </div>

        <div v-else class="max-h-[60vh] overflow-auto">
          <table class="w-full min-w-[680px] text-left text-sm">
            <thead class="sticky top-0 border-b border-border/50 bg-surface-elevated">
              <tr>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.siteName') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.groupName') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.type') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.fields.platform') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.history.multiplier') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.history.delta') }}</th>
                <th class="px-6 py-3 font-medium text-muted-foreground">{{ t('admin.groupRates.history.createdAt') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/50">
              <tr v-for="(row, index) in history" :key="historyRowKey(row, index)" class="transition-colors hover:bg-surface/30">
                <td class="px-6 py-4 font-medium text-foreground">{{ row.siteName }}</td>
                <td class="px-6 py-4 text-foreground">
                  <div class="flex items-center gap-1.5">
                    <span>{{ row.groupName }}</span>
                  </div>
                </td>
                <td class="px-6 py-4 text-muted-foreground">{{ typeLabel(row.type) }}</td>
                <td class="px-6 py-4 text-muted-foreground">{{ platformLabel(row.platform) }}</td>
                <td class="px-6 py-4">
                  <span class="font-semibold text-foreground">{{ formatMultiplier(row.currentMultiplier ?? row.multiplier) }}</span>
                  <span v-if="row.currentMultiplier !== null && row.currentMultiplier !== row.multiplier" class="ml-1 text-[10px] text-muted-foreground">{{ formatMultiplier(row.multiplier) }}</span>
                </td>
                <td class="px-6 py-4">
                  <span
                    class="inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-semibold"
                    :class="deltaClasses(row.delta)"
                  >
                    {{ formatDelta(row.delta) }}
                  </span>
                </td>
                <td class="px-6 py-4 text-muted-foreground">{{ formatDateTime(row.createdAt) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div v-if="editingRate" class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div data-group-rates-dialog role="dialog" aria-modal="true" aria-labelledby="group-rate-edit-title" tabindex="-1" class="max-h-[calc(100dvh-2rem)] w-full max-w-md overflow-y-auto overscroll-contain rounded-xl border border-border/50 bg-card shadow-xl">
        <div class="flex items-start justify-between gap-4 border-b border-border/50 p-6">
          <div>
            <h2 id="group-rate-edit-title" class="text-xl font-semibold text-foreground">{{ editTypeTitle }}</h2>
            <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.groupRates.edit.description') }}</p>
          </div>
          <button class="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary" :aria-label="t('admin.groupRates.actions.cancel')" :disabled="isActionLoading" @click="closeTypeEditor">
            <X class="h-5 w-5" />
            <span class="sr-only">{{ t('admin.groupRates.actions.closeEdit') }}</span>
          </button>
        </div>

        <form class="space-y-5 p-6" @submit.prevent="submitTypeEditor">
          <label class="block space-y-2">
            <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.edit.typeLabel') }}</span>
            <select
              v-model="editTypeValue"
              class="h-11 w-full rounded-xl border border-border/70 bg-surface px-4 text-sm text-foreground outline-none transition focus:border-primary focus:ring-1 focus:ring-primary"
              :disabled="isActionLoading"
            >
              <option value="">{{ t('admin.groupRates.edit.typePlaceholder') }}</option>
              <option v-for="type in editTypeOptions" :key="type" :value="type">{{ typeLabel(type) }}</option>
            </select>
          </label>

          <div class="flex justify-end gap-2">
            <Button type="button" variant="secondary" :disabled="isActionLoading" @click="closeTypeEditor">
              {{ t('admin.groupRates.actions.cancel') }}
            </Button>
            <Button type="submit" class="gap-2" :disabled="isActionLoading">
              <Loader2 v-if="isActionLoading" class="h-4 w-4 animate-spin" />
              {{ t('admin.groupRates.actions.saveType') }}
            </Button>
          </div>
        </form>
      </div>
    </div>

    <div v-if="connectingRate" class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div data-group-rates-dialog role="dialog" aria-modal="true" aria-labelledby="group-rate-connect-title" tabindex="-1" class="max-h-[calc(100dvh-2rem)] w-full max-w-2xl overflow-y-auto overscroll-contain rounded-lg border border-border/60 bg-card shadow-xl">
        <div class="flex items-start justify-between gap-4 border-b border-border/50 p-6">
          <div class="min-w-0 flex-1">
            <h2 id="group-rate-connect-title" class="break-words text-xl font-semibold text-foreground">
              {{ t('admin.groupRates.connect.titleWithGroup', { site: connectingRate.siteName, group: connectingRate.groupName }) }}
            </h2>
            <p class="mt-2 text-sm text-muted-foreground">
              {{ connectMode === 'bind' ? t('admin.groupRates.connect.bindDescription') : t('admin.groupRates.connect.realDescription') }}
            </p>
          </div>
          <button class="shrink-0 rounded-lg p-2 text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground" :disabled="isActionLoading" @click="closeConnector">
            <X class="h-5 w-5" />
            <span class="sr-only">{{ t('admin.groupRates.actions.closeConnect') }}</span>
          </button>
        </div>

        <form class="space-y-5 p-6" @submit.prevent="submitConnector">
          <fieldset class="contents" :disabled="connectFormLocked">
          <div class="grid gap-3 sm:grid-cols-2">
            <button
              type="button"
              :class="[
                'flex min-h-24 items-start gap-3 rounded-lg border p-4 text-left transition-colors',
                connectMode === 'real'
                  ? 'border-primary bg-primary/5 text-foreground'
                  : 'border-border/60 bg-surface text-muted-foreground hover:border-primary/40 hover:text-foreground'
              ]"
              :disabled="connectFormLocked"
              @click="setConnectMode('real')"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
                <Sparkles class="h-4 w-4" />
              </span>
              <span class="min-w-0">
                <span class="flex items-center gap-2 text-sm font-semibold">
                  {{ t('admin.groupRates.connect.modeReal') }}
                  <Check v-if="connectMode === 'real'" class="h-4 w-4 text-primary" />
                </span>
                <span class="mt-1 block text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.realDescription') }}</span>
              </span>
            </button>
            <button
              type="button"
              :class="[
                'flex min-h-24 items-start gap-3 rounded-lg border p-4 text-left transition-colors',
                connectMode === 'bind'
                  ? 'border-primary bg-primary/5 text-foreground'
                  : 'border-border/60 bg-surface text-muted-foreground hover:border-primary/40 hover:text-foreground'
              ]"
              :disabled="connectFormLocked"
              @click="setConnectMode('bind')"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
                <Link2 class="h-4 w-4" />
              </span>
              <span class="min-w-0">
                <span class="flex items-center gap-2 text-sm font-semibold">
                  {{ t('admin.groupRates.connect.modeBind') }}
                  <Check v-if="connectMode === 'bind'" class="h-4 w-4 text-primary" />
                </span>
                <span class="mt-1 block text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.bindDescription') }}</span>
              </span>
            </button>
          </div>

          <div class="rounded-xl border border-border/50 bg-surface/50 p-4 space-y-3">
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium text-muted-foreground">{{ t('admin.groupRates.connect.upstreamSiteLabel') }}</span>
              <span class="min-w-0 break-words text-right text-sm font-medium text-foreground">{{ connectingRate?.siteName }}</span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium text-muted-foreground">{{ t('admin.groupRates.connect.upstreamGroupNameLabel') }}</span>
              <span class="min-w-0 break-words text-right text-sm font-medium text-foreground">{{ connectingRate?.groupName }}</span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium text-muted-foreground">{{ t('admin.groupRates.connect.upstreamMultiplierLabel') }}</span>
              <span class="text-sm font-semibold text-primary">{{ formatMultiplier(connectingRate?.currentMultiplier ?? null) }}</span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-xs font-medium text-muted-foreground">{{ t('admin.groupRates.connect.upstreamPlatformLabel') }}</span>
              <span :class="['inline-flex rounded-md border px-2 py-0.5 text-xs font-semibold uppercase tracking-wider', platformClasses(connectingRate?.platform ?? null)]">
                {{ platformLabel(connectingRate?.platform ?? null) }}
              </span>
            </div>
          </div>

          <p v-if="!isAdminReady" class="text-sm text-warning" data-import-admin-unavailable>{{ t('admin.groupRates.connect.adminUnavailable') }}</p>

          <!-- Sub2API 自动创建使用显式账号平台，源分类只初始化默认。 -->
          <div v-if="needsGroupTypeSelection" class="space-y-2">
            <span class="text-sm font-medium text-foreground">{{ t(isC5Import ? 'admin.groupRates.connect.accountPlatformLabel' : 'admin.groupRates.connect.groupTypeLabel') }}</span>
            <div class="relative">
              <select
                v-model="selectedGroupType"
                data-import-platform-select
                class="h-10 w-full rounded-xl border border-border/50 bg-surface px-3 pr-8 text-sm text-foreground outline-none appearance-none transition-all focus:border-primary focus:ring-1 focus:ring-primary"
                :disabled="connectFormLocked"
              >
                <option value="">{{ t('admin.groupRates.connect.groupTypePlaceholder') }}</option>
                <option value="openai">{{ t('admin.groupRates.connect.groupTypeOpenai') }}</option>
                <option value="anthropic">{{ t('admin.groupRates.connect.groupTypeAnthropic') }}</option>
                <option value="gemini">{{ t('admin.groupRates.connect.groupTypeGemini') }}</option>
                <option value="antigravity">{{ t('admin.groupRates.connect.groupTypeAntigravity') }}</option>
                <option v-if="isC5Import" value="grok">{{ t('admin.groupRates.connect.groupTypeGrok') }}</option>
                <option v-if="isC5Import" value="kimi">{{ t('admin.groupRates.connect.groupTypeKimi') }}</option>
                <option v-if="isC5Import" value="zhipu">{{ t('admin.groupRates.connect.groupTypeZhipu') }}</option>
                <option v-if="isC5Import" value="deepseek">{{ t('admin.groupRates.connect.groupTypeDeepseek') }}</option>
                <option v-if="isC5Import" value="minimax">{{ t('admin.groupRates.connect.groupTypeMinimax') }}</option>
              </select>
              <div class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground">
                <ChevronDown class="h-3.5 w-3.5" />
              </div>
            </div>
            <p v-if="isC5Import" class="text-xs leading-5 text-muted-foreground" data-import-platform-hint>{{ t('admin.groupRates.connect.accountPlatformHint') }}</p>
            <p v-if="isC5Import && connectAccountPlatform === 'grok'" class="text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.grokImportHint') }}</p>
          </div>

          <!-- new-api admin：渠道类型选择 -->
          <div v-if="needsChannelTypeSelection" class="space-y-2">
            <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.channelTypeLabel') }}</span>
            <div class="relative">
              <select
                v-model.number="selectedChannelType"
                class="h-10 w-full rounded-xl border border-border/50 bg-surface px-3 pr-8 text-sm text-foreground outline-none appearance-none transition-all focus:border-primary focus:ring-1 focus:ring-primary"
                :disabled="connectFormLocked"
              >
                <option :value="0">{{ t('admin.groupRates.connect.channelTypePlaceholder') }}</option>
                <option v-for="ct in filteredChannelTypes" :key="ct.id" :value="ct.id">{{ ct.name }}</option>
              </select>
              <div class="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground">
                <ChevronDown class="h-3.5 w-3.5" />
              </div>
            </div>
          </div>

          <div v-if="connectMode === 'bind'" class="space-y-2">
            <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.bindSelectKey') }}</span>
            <div v-if="isLoadingKeys" class="flex items-center justify-center py-6 text-muted-foreground">
              <Loader2 class="mr-2 h-4 w-4 animate-spin" />
              {{ t('admin.groupRates.connect.bindKeysLoading') }}
            </div>
            <div v-else-if="upstreamKeys.length === 0" class="px-4 py-6 text-center text-sm text-muted-foreground">
              {{ t('admin.groupRates.connect.bindKeysEmpty') }}
            </div>
            <div v-else class="max-h-48 overflow-auto rounded-xl border border-border/50 bg-surface divide-y divide-border/30">
              <label
                v-for="keyItem in upstreamKeys"
                :key="keyItem.id"
                :class="[
                  'flex items-center gap-3 px-4 py-2.5 cursor-pointer transition-colors',
                  selectedKeyId === keyItem.id ? 'bg-primary/5' : 'hover:bg-surface-elevated'
                ]"
              >
                <input
                  type="radio"
                  :value="keyItem.id"
                  :checked="selectedKeyId === keyItem.id"
                  class="h-4 w-4 border-border text-primary focus:ring-primary"
                  :disabled="connectFormLocked"
                  @change="selectedKeyId = keyItem.id"
                />
                <div class="flex-1 min-w-0">
                  <div class="text-sm font-medium text-foreground truncate">{{ keyItem.name }}</div>
                  <div v-if="keyItem.keyPreview" class="text-xs text-muted-foreground font-mono truncate">{{ keyItem.keyPreview }}</div>
                </div>
                <span v-if="keyItem.groupName" class="inline-flex rounded-md border border-border/50 bg-surface-elevated px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground shrink-0">
                  {{ keyItem.groupName }}
                </span>
                <span
                  :class="[
                    'inline-flex rounded-md border px-1.5 py-0.5 text-[10px] font-semibold shrink-0',
                    isActiveResourceStatus(keyItem.status)
                      ? 'border-emerald-400/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300'
                      : 'border-border/60 bg-surface-elevated text-muted-foreground'
                  ]"
                >
                  {{ resourceStatusLabel(keyItem.status) }}
                </span>
              </label>
            </div>
          </div>

          <section v-if="isC5Import" class="space-y-4" aria-labelledby="import-account-settings-title">
            <div class="space-y-1 text-sm text-muted-foreground" data-import-platforms>
              <p>{{ t('admin.groupRates.connect.upstreamGroupType', { platform: connectUpstreamPlatform ? importPlatformLabel(connectUpstreamPlatform) : t('admin.groupRates.connect.unclassified') }) }}</p>
              <p>{{ t('admin.groupRates.connect.accountPlatform', { platform: connectAccountPlatform ? importPlatformLabel(connectAccountPlatform) : t('admin.groupRates.connect.accountPlatformPending') }) }}</p>
            </div>
            <h3 id="import-account-settings-title" class="text-sm font-semibold text-foreground">{{ t('admin.groupRates.connect.importSettings.title') }}</h3>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="space-y-2">
                <label for="import-priority" class="text-sm font-medium text-foreground">Priority</label>
                <div class="flex gap-2">
                  <button type="button" :aria-pressed="connectPriorityMode === 'automatic'" :disabled="connectFormLocked" class="rounded-lg border border-border/60 px-3 py-1.5 text-sm" :class="connectPriorityMode === 'automatic' ? 'border-primary bg-primary/5 text-primary' : 'text-muted-foreground'" @click="setConnectPriorityMode('automatic')">{{ t('admin.groupRates.connect.importSettings.automatic') }}</button>
                  <button type="button" :aria-pressed="connectPriorityMode === 'manual'" :disabled="connectFormLocked" class="rounded-lg border border-border/60 px-3 py-1.5 text-sm" :class="connectPriorityMode === 'manual' ? 'border-primary bg-primary/5 text-primary' : 'text-muted-foreground'" @click="setConnectPriorityMode('manual')">{{ t('admin.groupRates.connect.importSettings.manual') }}</button>
                </div>
                <input id="import-priority" v-model="connectPriority" type="number" step="1" :min="connectPriorityMode === 'manual' ? 1 : 10" :max="connectPriorityMode === 'manual' ? 9 : 2147483647" :disabled="connectFormLocked" :aria-invalid="Boolean(connectPriorityError)" aria-describedby="import-priority-help" class="h-10 w-full rounded-xl border border-border/50 bg-surface px-3 text-sm text-foreground outline-none focus:border-primary focus:ring-1 focus:ring-primary" />
                <p id="import-priority-help" class="text-xs leading-5" :class="connectPriorityError ? 'text-warning' : 'text-muted-foreground'">{{ connectPriorityError || t(connectPriorityMode === 'manual' ? 'admin.groupRates.connect.importSettings.manualHint' : 'admin.groupRates.connect.importSettings.automaticHint') }}</p>
              </div>
              <div class="space-y-2">
                <label for="import-concurrency" class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.importSettings.concurrency') }}</label>
                <input id="import-concurrency" v-model="connectConcurrency" type="number" min="1" max="1000" step="1" :disabled="connectFormLocked" :aria-invalid="Boolean(connectConcurrencyError)" aria-describedby="import-concurrency-help" class="h-10 w-full rounded-xl border border-border/50 bg-surface px-3 text-sm text-foreground outline-none focus:border-primary focus:ring-1 focus:ring-primary" />
                <p id="import-concurrency-help" class="text-xs leading-5" :class="connectConcurrencyError ? 'text-warning' : 'text-muted-foreground'">{{ connectConcurrencyError || t('admin.groupRates.connect.importSettings.concurrencyHint') }}</p>
              </div>
            </div>
            <div class="space-y-1 text-xs leading-5 text-muted-foreground" data-import-priority-explain>
              <p>{{ t('admin.groupRates.connect.importSettings.priorityModeExplain') }}</p>
              <p>{{ t('admin.groupRates.connect.importSettings.priorityStrategyExplain') }}</p>
            </div>
            <div class="flex items-center justify-between gap-4 rounded-xl border border-border/50 bg-surface px-4 py-3">
              <div class="min-w-0 space-y-1">
                <p class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.importSettings.passthrough') }}</p>
                <p v-if="!supportsConnectPassthrough" class="text-xs text-muted-foreground">{{ t('admin.groupRates.connect.importSettings.passthroughUnsupported') }}</p>
                <p v-if="!connectPassthrough" class="text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.importSettings.modelSyncHint') }}</p>
              </div>
              <button v-if="supportsConnectPassthrough" type="button" role="switch" :aria-label="t('admin.groupRates.connect.importSettings.passthrough')" :aria-checked="connectPassthrough" :disabled="connectFormLocked" class="relative inline-flex h-6 w-11 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-50" :class="connectPassthrough ? 'bg-primary' : 'bg-muted'" @click="connectPassthrough = !connectPassthrough"><span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow-sm transition-transform" :class="connectPassthrough ? 'translate-x-5' : 'translate-x-0'" /></button>
            </div>
            <div class="flex items-center justify-between gap-4 rounded-xl border border-border/50 bg-surface px-4 py-3">
              <span class="text-sm font-medium text-foreground">Pool Mode</span>
              <button type="button" role="switch" aria-label="Pool Mode" :aria-checked="connectPoolMode" :disabled="connectFormLocked" class="relative inline-flex h-6 w-11 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-50" :class="connectPoolMode ? 'bg-primary' : 'bg-muted'" @click="connectPoolMode = !connectPoolMode"><span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow-sm transition-transform" :class="connectPoolMode ? 'translate-x-5' : 'translate-x-0'" /></button>
            </div>
            <div class="flex items-center justify-between gap-4 rounded-xl border border-border/50 bg-surface px-4 py-3">
              <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.importSettings.probe') }}</span>
              <button type="button" role="switch" :aria-label="t('admin.groupRates.connect.importSettings.probe')" :aria-checked="connectProbeEnabled" :disabled="connectFormLocked" class="relative inline-flex h-6 w-11 shrink-0 rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 disabled:opacity-50" :class="connectProbeEnabled ? 'bg-primary' : 'bg-muted'" @click="connectProbeEnabled = !connectProbeEnabled"><span class="pointer-events-none inline-block h-5 w-5 rounded-full bg-white shadow-sm transition-transform" :class="connectProbeEnabled ? 'translate-x-5' : 'translate-x-0'" /></button>
            </div>
          </section>

          <div v-if="connectMode === 'bind'" class="space-y-2">
            <label for="existing-admin-group" class="flex items-center gap-2 text-sm font-medium text-foreground">
              <ServerCog class="h-4 w-4 text-primary" />
              {{ t('admin.groupRates.connect.bindSelectAdminGroup') }}
            </label>
            <div class="relative">
              <select
                id="existing-admin-group"
                :value="selectedAdminGroupId"
                class="h-11 w-full appearance-none rounded-lg border border-border/60 bg-surface px-3 pr-9 text-sm text-foreground outline-none transition focus:border-primary focus:ring-1 focus:ring-primary"
                :disabled="isActionLoading || isLoadingAdminResources"
                @change="handleAdminGroupChange"
              >
                <option value="">{{ t('admin.groupRates.connect.bindAdminGroupPlaceholder') }}</option>
                <option v-for="group in ownGroups" :key="group.id" :value="group.id">
                  {{ group.groupName }} · {{ formatMultiplier(group.multiplier) }}
                </option>
              </select>
              <ChevronDown class="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            </div>
          </div>

          <div v-if="connectMode === 'bind' && selectedAdminGroupId" class="space-y-2">
            <span class="flex items-center gap-2 text-sm font-medium text-foreground">
              <ServerCog class="h-4 w-4 text-primary" />
              {{ t('admin.groupRates.connect.bindSelectAdminResource') }}
            </span>
            <div v-if="isLoadingAdminResources" class="flex items-center justify-center py-6 text-muted-foreground">
              <Loader2 class="mr-2 h-4 w-4 animate-spin" />
              {{ t('admin.groupRates.connect.adminResourcesLoading') }}
            </div>
            <div v-else-if="adminResources.length === 0" class="rounded-lg border border-dashed border-border/70 px-4 py-6 text-center text-sm text-muted-foreground">
              {{ t('admin.groupRates.connect.adminResourcesEmpty') }}
            </div>
            <div v-else class="max-h-48 divide-y divide-border/30 overflow-auto rounded-lg border border-border/60 bg-surface">
              <label
                v-for="resource in adminResources"
                :key="resource.id"
                class="flex cursor-pointer items-center gap-3 px-4 py-3 transition-colors hover:bg-surface-elevated"
                :class="selectedAdminResourceId === resource.id ? 'bg-primary/5' : ''"
              >
                <input
                  v-model="selectedAdminResourceId"
                  type="radio"
                  :value="resource.id"
                  class="h-4 w-4 border-border text-primary focus:ring-primary"
                  :disabled="connectFormLocked"
                />
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium text-foreground">{{ resource.name }}</div>
                  <div class="mt-0.5 truncate text-xs text-muted-foreground">{{ adminResourceTypeLabel(resource) }}</div>
                </div>
                <span :class="['rounded-md border px-2 py-0.5 text-[10px] font-semibold', isActiveResourceStatus(resource.status) ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300' : 'border-border/60 bg-surface-elevated text-muted-foreground']">
                  {{ resourceStatusLabel(resource.status) }}
                </span>
              </label>
            </div>
          </div>

          <div v-if="connectMode === 'real'" class="space-y-2">
            <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.connect.ownGroupLabel') }}</span>
            <p v-if="isC5Import" class="text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.targetGroupsHint') }}</p>
            <p v-if="connectGroupSelectionNotice" class="text-xs text-warning">{{ connectGroupSelectionNotice }}</p>
            <div class="max-h-48 overflow-auto rounded-xl border border-border/50 bg-surface divide-y divide-border/30">
              <label
                v-for="group in filteredOwnGroups"
                :key="group.id"
                class="flex items-center gap-3 px-4 py-2.5 transition-colors"
                :class="canSelectOwnGroup(group) ? 'cursor-pointer hover:bg-surface-elevated' : 'cursor-not-allowed opacity-50'"
              >
                <input
                  type="checkbox"
                  :checked="connectOwnGroups.includes(group.id)"
                  class="h-4 w-4 rounded border-border text-primary focus:ring-primary"
                  :disabled="connectFormLocked || !canSelectOwnGroup(group)"
                  @change="toggleOwnGroup(group.id)"
                />
                <span class="min-w-0 break-words text-sm text-foreground">{{ group.groupName }}</span>
                <span v-if="group.platform" class="inline-flex rounded-md border border-border/50 bg-surface-elevated px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  {{ isC5Import ? importPlatformLabel(group.platform) : group.platform }}
                </span>
                <span class="ml-auto text-xs text-muted-foreground">{{ formatMultiplier(group.multiplier) }}</span>
              </label>
              <div v-if="filteredOwnGroups.length === 0" class="px-4 py-3 text-sm text-muted-foreground">
                {{ t(isC5Import ? (connectAccountPlatform ? 'admin.groupRates.connect.noPlatformGroups' : 'admin.groupRates.connect.selectPlatformFirst') : 'admin.groupRates.connect.ownGroupPlaceholder') }}
              </div>
            </div>
          </div>

          <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-border/60 bg-surface p-4 transition-colors hover:border-primary/40">
            <input
              v-model="addToPricingMapping"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-border text-primary focus:ring-primary"
              :disabled="connectFormLocked"
            />
            <span>
              <span class="flex items-center gap-2 text-sm font-medium text-foreground">
                <KeyRound class="h-4 w-4 text-primary" />
                {{ t('admin.groupRates.connect.addToPricingMapping') }}
              </span>
              <span class="mt-1 block text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.connect.addToPricingMappingHint') }}</span>
            </span>
          </label>

          </fieldset>
          <p v-if="connectOutcome === 'submitting' && isC5Import" role="status" class="text-sm text-muted-foreground">{{ t('admin.groupRates.connect.importResult.submitting') }}</p>
          <div v-if="realConnectError" role="alert" class="flex items-start gap-3 rounded-xl border border-warning/20 bg-warning/10 p-3 text-sm text-warning">
            <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
            <span class="min-w-0 break-words">{{ realConnectError }}</span>
          </div>

          <div v-if="connectFailure" class="space-y-2 break-words text-sm text-warning" data-import-failure>
            <p>{{ t('admin.groupRates.connect.importResult.stage') }}：{{ t(`admin.groupRates.connect.importResult.stages.${connectFailure.stage}`) }}</p>
            <p>{{ t(`admin.groupRates.connect.importResult.cleanup.${connectFailure.cleanup}`) }}</p>
            <p v-if="!connectFailure.diagnostics?.length && connectFailure.reason">{{ importReasonLabel(connectFailure.reason) }}</p>
            <p v-if="connectFailure.retryAllowed">{{ t('admin.groupRates.connect.importResult.retry') }}</p>
            <p v-else-if="connectFailure.stage === 'validation' && connectFailure.cleanup === 'not_needed'">{{ connectPreflightGuide }}</p>
            <p v-if="connectFailure.groupId || connectFailure.groupName">{{ t('admin.mySites.errors.blockingGroup', { groupName: connectFailure.groupName || '—', groupId: connectFailure.groupId || '—' }) }}</p>
            <p v-if="connectFailure.adminResourceId || connectFailure.upstreamKeyId || ['retained', 'pending'].includes(connectFailure.cleanup)">{{ t('admin.groupRates.connect.importResult.resourceIds', { adminResourceId: connectFailure.adminResourceId || t('admin.groupRates.connect.importResult.unknownId'), upstreamKeyId: connectFailure.upstreamKeyId || t('admin.groupRates.connect.importResult.unknownId') }) }}</p>
            <p v-if="connectFailure.upstreamResourceName">{{ t('admin.groupRates.connect.importResult.resourceName') }}：{{ connectFailure.upstreamResourceName }}</p>
          </div>
          <div v-if="connectOutcome === 'pending'" role="status" class="space-y-2 break-words text-sm text-warning" data-import-pending>
            <p>{{ t('admin.groupRates.connect.importResult.pending') }}</p>
            <p>{{ t('admin.groupRates.connect.importResult.operationId') }}：{{ connectOperationId }}</p>
          </div>
          <section v-if="connectOutcome === 'succeeded'" role="status" class="space-y-3 break-words rounded-xl border border-border/50 bg-surface/50 p-4" data-import-success>
            <h3 class="text-sm font-semibold text-foreground">{{ t(connectHasModelWarning ? 'admin.groupRates.connect.importResult.warningSuccess' : connectResult?.configurationStatus === 'confirmed' ? 'admin.groupRates.connect.importResult.confirmed' : 'admin.groupRates.connect.importResult.unavailable') }}</h3>
            <template v-if="connectHasModelWarning">
              <p class="text-sm">{{ t('admin.groupRates.connect.importResult.warningExplanation') }}</p>
              <p class="text-sm">{{ t('admin.groupRates.connect.importResult.warningNext') }}</p>
            </template>
            <template v-if="connectConfiguration">
              <p class="text-sm">{{ connectConfiguration.name }} · {{ t('admin.groupRates.connect.importResult.accountId') }} {{ connectConfiguration.adminAccountId }}</p>
              <p class="text-xs text-muted-foreground">{{ t(`admin.groupRates.connect.importResult.observation.${connectConfiguration.observation}`) }}</p>
              <dl class="grid grid-cols-1 gap-2 text-sm sm:grid-cols-2">
                <div><dt class="text-muted-foreground">Priority</dt><dd>{{ connectConfiguration.priority }}</dd></div>
                <div><dt class="text-muted-foreground">{{ t('admin.groupRates.connect.importSettings.concurrency') }}</dt><dd>{{ connectConfiguration.concurrency }}</dd></div>
                <div><dt class="text-muted-foreground">{{ t('admin.groupRates.connect.importSettings.passthrough') }}</dt><dd>{{ t(connectConfiguration.passthrough ? 'admin.groupRates.connect.importResult.on' : 'admin.groupRates.connect.importResult.off') }}</dd></div>
                <div><dt class="text-muted-foreground">Pool Mode</dt><dd>{{ t(connectConfiguration.poolMode ? 'admin.groupRates.connect.importResult.on' : 'admin.groupRates.connect.importResult.off') }}</dd></div>
                <div><dt class="text-muted-foreground">{{ t('admin.groupRates.connect.importSettings.probe') }}</dt><dd>{{ t(connectConfiguration.upstreamBillingProbeEnabled ? 'admin.groupRates.connect.importResult.on' : 'admin.groupRates.connect.importResult.off') }}</dd></div>
              </dl>
              <p class="text-sm">{{ t('admin.groupRates.connect.ownGroupLabel') }}：<span v-for="(group, index) in connectConfiguration.ownGroups" :key="group.id">{{ index ? '、' : '' }}{{ group.name }}（{{ group.id }}）</span></p>
              <p class="text-sm">{{ connectModelSummary }}</p>
              <details v-if="connectConfiguration.models.length" class="text-sm"><summary class="cursor-pointer">{{ t('admin.groupRates.connect.importResult.modelList') }}</summary><ul class="mt-2 list-inside list-disc"><li v-for="model in connectConfiguration.models" :key="model">{{ model }}</li></ul></details>
            </template>
            <p v-else class="text-sm">{{ t('admin.groupRates.connect.importResult.accountId') }} {{ connectResult?.connection.adminAccountId }}</p>
          </section>
          <div v-if="connectDiagnostics.length" class="space-y-3 break-words text-sm text-warning" data-import-diagnostics>
            <div v-for="(diagnostic, index) in connectDiagnostics" :key="index" class="space-y-1">
              <p class="font-medium">{{ t(diagnostic.stage === 'model_sync' ? 'admin.groupRates.connect.importResult.modelHint' : ['account_cleanup', 'key_cleanup'].includes(diagnostic.stage) ? 'admin.groupRates.connect.importResult.cleanupHint' : 'admin.groupRates.connect.importResult.stage') }}：{{ t(`admin.groupRates.connect.importResult.stages.${diagnostic.stage}`) }}</p>
              <p>{{ t('admin.groupRates.connect.importResult.target') }}：{{ t(`admin.groupRates.connect.importResult.targets.${diagnostic.target}`) }}<span v-if="diagnostic.stage === 'model_sync'">；{{ t('admin.groupRates.connect.importResult.modelReturnedByMain') }}</span></p>
              <p v-if="diagnostic.httpStatus">{{ t('admin.groupRates.connect.importResult.httpStatus') }}：{{ diagnostic.httpStatus }}</p>
              <p v-if="diagnostic.reason">{{ importReasonLabel(diagnostic.reason, diagnostic.target) }}</p>
              <p>{{ t('admin.groupRates.connect.importResult.detail') }}：{{ diagnostic.detail || t('admin.groupRates.connect.importResult.unknownDetail', { object: t(`admin.groupRates.connect.importResult.targets.${diagnostic.target}`) }) }}</p>
              <p>{{ t('admin.groupRates.connect.importResult.next') }}：{{ diagnosticGuide(diagnostic) }}</p>
            </div>
          </div>
          <p v-if="connectDiagnosticsUnavailable" class="text-sm text-warning" data-import-diagnostics-unavailable>{{ t('admin.groupRates.connect.importResult.diagnosticsUnavailable') }}</p>
          <p v-if="connectOutcome === 'succeeded'" class="text-xs text-muted-foreground">{{ t('admin.groupRates.connect.importResult.doneHint') }}</p>
          <p v-if="connectRefreshError" role="alert" class="text-sm text-warning" data-import-refresh-error>{{ connectRefreshError }}</p>

          <div class="flex justify-end gap-2">
            <Button type="button" variant="secondary" :disabled="isActionLoading || connectOutcome === 'submitting'" @click="closeConnector">
              {{ t(connectOutcome === 'succeeded' ? 'admin.groupRates.connect.importResult.done' : 'admin.groupRates.actions.cancel') }}
            </Button>
            <Button v-if="connectOutcome !== 'succeeded'" type="submit" class="gap-2" :disabled="isActionLoading || !canSubmitConnect">
              <Loader2 v-if="isActionLoading" class="h-4 w-4 animate-spin" />
              {{ t(connectMode === 'real' ? 'admin.groupRates.connect.submitManaged' : 'admin.groupRates.connect.submitExisting') }}
            </Button>
          </div>
        </form>
      </div>
    </div>

    <div v-if="groupEditingRate" class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div data-group-rates-dialog role="dialog" aria-modal="true" aria-labelledby="group-rate-editor-title" tabindex="-1" class="max-h-[calc(100dvh-2rem)] w-full max-w-lg overflow-y-auto overscroll-contain rounded-xl border border-border/50 bg-card shadow-xl">
        <div class="flex items-start justify-between gap-4 border-b border-border/50 p-6">
          <div>
            <h2 id="group-rate-editor-title" class="text-xl font-semibold text-foreground">{{ t('admin.groupRates.groupEditor.action') }}</h2>
            <p class="mt-2 text-sm text-muted-foreground">{{ t('admin.groupRates.groupEditor.description', { site: groupEditingRate.siteName, group: groupEditingRate.groupName }) }}</p>
          </div>
          <button aria-label="关闭修改主站分组" class="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground" :disabled="groupEditorBusy" @click="closeGroupEditor"><X class="h-5 w-5" /></button>
        </div>
        <div class="space-y-5 p-6">
          <div v-if="groupEditorResultNotice" role="status" class="flex items-start gap-3 rounded-xl border border-warning/20 bg-warning/10 p-3 text-sm text-warning"><AlertCircle class="mt-0.5 h-4 w-4 shrink-0" /><span class="min-w-0 break-words">{{ groupEditorResultNotice }}</span></div>
          <p v-if="groupEditorState === 'loading'" role="status" class="text-sm text-muted-foreground">{{ t('admin.groupRates.groupEditor.loading') }}</p>
          <div v-if="groupEditorState === 'readFailed'" role="alert" class="rounded-xl border border-warning/20 bg-warning/10 p-3 text-sm text-warning">{{ groupEditorReadError }}</div>
          <template v-if="groupEditorData">
            <div class="space-y-2 break-words text-sm">
              <p>{{ t('admin.groupRates.groupEditor.upstream', { site: groupEditingRate.siteName, group: groupEditingRate.groupName }) }}</p>
              <p>{{ t('admin.groupRates.groupEditor.account', { name: groupEditorData.account.name, id: groupEditorData.account.id }) }}</p>
              <p>{{ t('admin.groupRates.groupEditor.platform', { platform: groupEditorData.account.platform }) }}</p>
              <p class="text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.groupEditor.reconfigure') }}</p>
            </div>
            <div v-if="!groupEditorData.consistent" class="space-y-3 rounded-xl border border-warning/20 bg-warning/10 p-3 text-sm text-warning">
              <p>{{ t('admin.groupRates.groupEditor.inconsistent', { main: groupEditorData.currentGroups.map(group => group.name || group.id).join('、'), hub: groupEditorData.hubGroups.map(group => group.name || group.id).join('、') || '没有分组' }) }}</p>
              <Button variant="secondary" size="sm" :disabled="groupEditorBusy" @click="submitGroupEditor(true)">{{ t('admin.groupRates.groupEditor.sync') }}</Button>
              <p class="text-xs">{{ t('admin.groupRates.groupEditor.syncHint') }}</p>
            </div>
            <fieldset class="space-y-3" :disabled="groupEditorBusy">
              <legend class="mb-3 text-sm font-medium text-foreground">{{ t('admin.groupRates.groupEditor.groups', { platform: groupEditorData.account.platform }) }}</legend>
              <p v-if="!groupEditorData.groups.length" class="text-sm text-muted-foreground">{{ t('admin.groupRates.groupEditor.noGroups') }}</p>
              <label v-for="group in groupEditorData.groups" :key="group.id" class="flex items-center gap-3 rounded-lg border border-border/50 p-3 text-sm">
                <input v-model="groupEditorSelected" type="checkbox" :value="group.id" :disabled="groupEditorBusy" class="h-4 w-4 accent-primary" />
                <span>{{ group.name || group.id }}</span>
                <span v-if="groupEditorData.currentGroups.some(current => current.id === group.id)" class="text-xs text-muted-foreground">{{ t('admin.groupRates.groupEditor.current') }}</span>
                <span v-if="group.status.trim().toLowerCase() !== 'active'" class="text-xs text-muted-foreground">{{ t('admin.groupRates.groupEditor.inactive') }}</span>
              </label>
              <label v-if="groupEditorData.connection.pricingMappingEnabled" class="flex items-start gap-3 rounded-xl border border-border/50 bg-surface/50 p-4">
                <input v-model="groupEditorRemovePricing" type="checkbox" :disabled="groupEditorBusy" class="mt-0.5 h-4 w-4 accent-primary" />
                <span><span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.groupEditor.removePricing') }}</span><span class="mt-1 block text-xs leading-5 text-muted-foreground">{{ t('admin.groupRates.groupEditor.pricingHint') }}</span></span>
              </label>
            </fieldset>
            <div v-if="groupEditorSelected.length" class="space-y-2 text-xs leading-5 text-muted-foreground">
              <p>{{ t('admin.groupRates.groupEditor.' + (groupEditorSelected.length === 1 ? 'singleImpact' : 'multipleImpact')) }}</p>
              <p>{{ t('admin.groupRates.groupEditor.' + (groupEditorSelected.length === 1 ? 'singlePurchase' : 'multiplePurchase')) }}</p>
            </div>
            <p v-if="!groupEditorSelected.length" class="text-sm text-warning">{{ t('admin.groupRates.groupEditor.required') }}</p>
            <p v-else-if="groupEditorUnchanged" class="text-sm text-muted-foreground">{{ t('admin.groupRates.groupEditor.unchanged') }}</p>
          </template>
          <p v-if="groupEditorBusy" role="status" class="text-sm text-muted-foreground">{{ t('admin.groupRates.groupEditor.' + (groupEditorState === 'syncing' ? 'syncing' : 'submitting')) }}</p>
          <div class="flex flex-wrap justify-end gap-2">
            <Button variant="secondary" :disabled="groupEditorBusy" @click="closeGroupEditor">{{ t('admin.groupRates.actions.cancel') }}</Button>
            <Button v-if="groupEditorState !== 'loading'" variant="secondary" :disabled="groupEditorBusy" @click="readGroupEditor">{{ t('admin.groupRates.groupEditor.reread') }}</Button>
            <Button v-if="groupEditorData" :disabled="groupEditorBusy || !groupEditorSelected.length || groupEditorUnchanged" @click="submitGroupEditor(false)">{{ t('admin.groupRates.groupEditor.save') }}</Button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="disconnectingRate" class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm">
      <div data-group-rates-dialog role="dialog" aria-modal="true" aria-labelledby="group-rate-disconnect-title" tabindex="-1" class="max-h-[calc(100dvh-2rem)] w-full max-w-md overflow-y-auto overscroll-contain rounded-xl border border-border/50 bg-card shadow-xl">
        <div class="flex items-start justify-between gap-4 border-b border-border/50 p-6">
          <div>
            <h2 id="group-rate-disconnect-title" class="text-xl font-semibold text-foreground">{{ t(disconnectConnection?.status === 'missing' ? 'admin.groupRates.disconnect.cleanupMissingTitle' : 'admin.groupRates.disconnect.title') }}</h2>
            <p class="mt-2 text-sm text-muted-foreground">
              {{ t('admin.groupRates.disconnect.description', { site: disconnectingRate.siteName, group: disconnectingRate.groupName }) }}
            </p>
          </div>
          <button class="rounded-lg p-2 text-muted-foreground transition-colors hover:bg-surface-line hover:text-foreground" :disabled="isDisconnecting" @click="closeDisconnect">
            <X class="h-5 w-5" />
          </button>
        </div>

        <div v-if="disconnectError" class="mx-6 mt-6 flex items-start gap-3 rounded-xl border border-warning/20 bg-warning/10 p-3 text-sm text-warning">
          <AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
          <span>{{ disconnectError }}</span>
        </div>

        <div class="space-y-4 p-6">
          <div class="space-y-3">
            <label
              v-if="disconnectIsMaintenance || disconnectConnection?.status === 'missing' || disconnectConnection?.canDeleteRemote !== false"
              class="flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition-colors"
              :class="disconnectMode === 'unlink'
                ? 'border-primary bg-primary/5'
                : 'border-border/50 bg-surface hover:bg-surface-elevated'"
            >
              <input
                v-model="disconnectMode"
                type="radio"
                value="unlink"
                class="mt-0.5 h-4 w-4 border-border text-primary focus:ring-primary"
                :disabled="isDisconnecting"
              />
              <div>
                <span class="text-sm font-medium text-foreground">{{ disconnectIsMaintenance ? '仅解除本地对接' : t(disconnectConnection?.status === 'missing' ? 'admin.groupRates.disconnect.cleanupLocalOnly' : 'admin.groupRates.disconnect.unlinkOnly') }}</span>
                <p class="mt-1 text-xs text-muted-foreground">{{ disconnectIsMaintenance ? '不会删除主站账号或上游Key，调价映射按你在窗口中的选择处理。' : t(disconnectConnection?.status === 'missing' ? 'admin.groupRates.disconnect.cleanupLocalOnlyHint' : 'admin.groupRates.disconnect.unlinkOnlyHint') }}</p>
              </div>
            </label>

            <label
              v-if="!disconnectIsMaintenance && disconnectConnection?.status !== 'missing'"
              class="flex cursor-pointer items-start gap-3 rounded-xl border p-4 transition-colors"
              :class="disconnectMode === 'full'
                ? 'border-red-500/50 bg-red-500/5'
                : 'border-border/50 bg-surface hover:bg-surface-elevated'"
            >
              <input
                v-model="disconnectMode"
                type="radio"
                value="full"
                class="mt-0.5 h-4 w-4 border-border text-red-500 focus:ring-red-500"
                :disabled="isDisconnecting"
              />
              <div>
                <span class="text-sm font-medium text-red-600 dark:text-red-400">{{ t('admin.groupRates.disconnect.deleteAll') }}</span>
                <p class="mt-1 text-xs text-red-500/70">{{ t('admin.groupRates.disconnect.deleteAllHint') }}</p>
              </div>
            </label>
          </div>

          <label
            v-if="disconnectConnection?.pricingMappingEnabled || disconnectingRate?.pricingMapped"
            class="flex cursor-pointer items-start gap-3 rounded-lg border border-border/60 bg-surface p-4"
          >
            <input
              v-model="disconnectRemovePricing"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-border text-primary focus:ring-primary"
              :disabled="isDisconnecting"
            />
            <span>
              <span class="text-sm font-medium text-foreground">{{ t('admin.groupRates.disconnect.removePricingMapping') }}</span>
              <span class="mt-1 block text-xs text-muted-foreground">{{ t('admin.groupRates.disconnect.removePricingMappingHint') }}</span>
            </span>
          </label>

          <div class="flex justify-end gap-2">
            <Button variant="secondary" :disabled="isDisconnecting" @click="closeDisconnect">
              {{ t('admin.groupRates.actions.cancel') }}
            </Button>
            <Button
              :variant="disconnectMode === 'full' ? 'destructive' : 'default'"
              class="gap-2"
              :disabled="isDisconnecting"
              @click="submitDisconnect"
            >
              <Loader2 v-if="isDisconnecting" class="h-4 w-4 animate-spin" />
              {{ t('admin.groupRates.disconnect.confirm') }}
            </Button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

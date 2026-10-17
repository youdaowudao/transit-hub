import { costFailureText } from '../utils/upstream'
import { safeScopedErrorText } from '../utils/scopedErrorText'
import { computed, inject, onBeforeUnmount, ref, watch } from 'vue'
import { useAdminAccounts } from './useAdminAccounts'
import { NotificationControllerKey } from './useNotifications'
import {
  createUpstreamSite,
  listUpstreamSites,
  removeUpstreamSite,
  streamSyncAllUpstreamSites,
  syncAllUpstreamSites,
  syncUpstreamSite,
  updateUpstreamSiteEnabled,
  updateUpstreamSite,
  updateSiteSettings,
} from '../api/upstream'
import type { SiteSettings, UpstreamFailure, SiteSyncState, UpstreamMetricValue, UpstreamMetrics, UpstreamSite, UpstreamSiteForm, UpstreamSiteResponse } from '../types/upstream'

const logoClasses = [
  'bg-primary/10 text-primary border-primary/20',
  'bg-accent/10 text-accent border-accent/20',
  'bg-warning/10 text-warning border-warning/20',
  'bg-signal/10 text-signal border-signal/20',
]

const createSiteId = (): string => {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

const siteLogo = (name: string): string => name.trim().charAt(0).toUpperCase() || 'U'

const emptyMetric = (): UpstreamMetricValue => ({ value: null, display: '-' })

const safeUpstreamErrorKey = (error: unknown): string => {
  const key = error instanceof Error ? error.message.trim() : ''
  if (/^(?:admin\.upstream\.errors\.[A-Za-z][A-Za-z0-9]*|auth\.errors\.unauthorized)$/.test(key)) return key
  return 'admin.upstream.errors.unknown'
}

const normalizeMetrics = (metrics: UpstreamSiteResponse['metrics'] | null | undefined): UpstreamMetrics => {
  const singaporeDate = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Asia/Singapore', year: 'numeric', month: '2-digit', day: '2-digit' }).format(new Date())
  const usable = metrics?.todayConsumeTimezone === 'Asia/Singapore' && metrics.todayConsumeDate === singaporeDate && metrics.todayConsume?.value != null && Number.isFinite(metrics.todayConsume.value) && metrics.todayConsume.value >= 0
  const legacy = metrics?.todayConsumeTimezone !== 'Asia/Singapore'
  return {
    todayConsumeDate: metrics?.todayConsumeDate,
    todayConsumeTimezone: metrics?.todayConsumeTimezone,
    todayConsumeErrorDetail: legacy ? "旧费用尚未按新加坡日期重新读取，请同步这个站点；此操作不会修改主站。" + (metrics?.todayConsumeErrorDetail ? " " + metrics.todayConsumeErrorDetail : metrics?.todayConsumeErrorKey ? " " + costFailureText(metrics.todayConsumeErrorKey) : "") : metrics?.todayConsumeErrorDetail,
    todayConsumeAt: metrics?.todayConsumeAt,
    todayConsumeStatus: metrics?.todayConsumeStatus,
    todayConsumeErrorKey: metrics?.todayConsumeErrorKey,
    todayConsumeUpstreamCode: metrics?.todayConsumeUpstreamCode,
    todayConsumeHTTPStatus: metrics?.todayConsumeHTTPStatus,
    todayConsumeFailedAt: metrics?.todayConsumeFailedAt,
    balance: metrics?.balance ?? emptyMetric(),
    todayConsume: usable ? metrics!.todayConsume : emptyMetric(),
    historyRecharge: metrics?.historyRecharge ?? emptyMetric(),
    group: metrics?.group ?? { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' },
    groups: Array.isArray(metrics?.groups) ? metrics.groups : [],
  }
}

const normalizeSite = (site: UpstreamSiteResponse, logoBg: string): UpstreamSite => ({
  ...site,
  groupRatesWarning: site.groupRatesWarning ? {
    message: safeScopedErrorText(site.groupRatesWarning.message),
    detail: safeScopedErrorText(site.groupRatesWarning.detail),
    next: safeScopedErrorText(site.groupRatesWarning.next),
  } : undefined,
  enabled: site.enabled ?? true,
  metrics: normalizeMetrics(site.metrics),
  settings: site.settings ?? { balanceThreshold: null },
  logo: siteLogo(site.name),
  logoBg,
})

export const useUpstreamSites = () => {
  const notifications = inject(NotificationControllerKey, null)
  const { currentAccount } = useAdminAccounts()
  const workspaceId = computed(() => currentAccount.value?.id ?? '')
  const sites = ref<UpstreamSite[]>([])
  const isAdding = ref(false)
  const isRefreshing = ref(false)
  const addErrorKey = ref<string | null>(null)
  const addFailure = ref<UpstreamFailure | null>(null)
  const clearAddFailure = () => { addErrorKey.value = null; addFailure.value = null }
  const enabledUpdatingIds = ref(new Set<string>())
  const enabledErrorKeys = ref(new Map<string, string>())
  const siteSyncStates = ref(new Map<string, SiteSyncState>())
  const syncingSiteIds = ref(new Set<string>())
  const siteOperationVersions = ref(new Map<string, number>())
  const pendingSiteOperations = ref(new Set<string>())
  const enabledSiteCount = computed(() => sites.value.filter((site) => site.enabled).length)
  const connectedCount = computed(() => sites.value.filter((site) => site.enabled && (site.status === 'connected' || site.status === 'syncing')).length)

  // Reading and mutations have separate generations: an older finally must not
  // invalidate the result of a newer mutation. Each workspace owns its maps.
  let epoch = 0
  let disposed = false
  let operationVersions = new Map<string, number>()
  let pending = new Map<string, number>()
  let removedIds = new Set<string>()
  let streamController: AbortController | undefined
  const doneTimers = new Set<ReturnType<typeof setTimeout>>()
  const invalidateRead = (id: string) => {
    const next = new Map(siteOperationVersions.value)
    next.set(id, (next.get(id) ?? 0) + 1)
    siteOperationVersions.value = next
  }
  const beginOperation = (id: string, ownsSite = true) => {
    invalidateRead(id)
    // Only editing replaces the site fields. Independent fields and syncs use
    // the existing read generation; they never supersede an in-flight edit.
    const operations = ownsSite ? operationVersions : siteOperationVersions.value
    const version = ownsSite ? (operations.get(id) ?? 0) + 1 : operations.get(id)!
    if (ownsSite) operations.set(id, version)
    else if (!operationVersions.has(id)) operationVersions.set(id, 0)
    pending.set(id, (pending.get(id) ?? 0) + 1)
    pendingSiteOperations.value = new Set(pending.keys())
    return { id, version, epoch, workspace: workspaceId.value, operations, pending }
  }
  type Operation = ReturnType<typeof beginOperation>
  const sameWorkspace = (token: { epoch: number; workspace: string }) => !disposed && token.epoch === epoch && token.workspace === workspaceId.value
  const isCurrent = (token: Operation) => sameWorkspace(token) && (token.operations === operationVersions ? operationVersions : siteOperationVersions.value).get(token.id) === token.version
  const finishOperation = (token: Operation) => {
    const count = (token.pending.get(token.id) ?? 1) - 1
    if (count > 0) token.pending.set(token.id, count)
    else token.pending.delete(token.id)
    if (sameWorkspace(token)) {
      invalidateRead(token.id)
      pendingSiteOperations.value = new Set(pending.keys())
    }
  }
  const applySite = (remote: UpstreamSiteResponse, preserveWarning = false) => {
    const index = sites.value.findIndex(site => site.id === remote.id)
    if (index < 0 || removedIds.has(remote.id)) return
    const current = sites.value[index]!
    const next = normalizeSite(remote, current.logoBg)
    if (preserveWarning && remote.groupRatesWarning === undefined) next.groupRatesWarning = current.groupRatesWarning
    sites.value[index] = next
  }

  const loadSites = async () => {
    const token = { epoch, workspace: workspaceId.value }
    const versions = new Map(siteOperationVersions.value)
    try {
      const remoteSites = await listUpstreamSites()
      if (!sameWorkspace(token)) return
      // Keep objects changed since this initial/cache load began, including
      // deletions (their version remains even after the object is removed).
      const result = remoteSites.flatMap((remote, index) => {
        if (removedIds.has(remote.id)) return []
        const current = sites.value.find(site => site.id === remote.id)
        if ((versions.get(remote.id) ?? 0) !== (siteOperationVersions.value.get(remote.id) ?? 0)) {
          if (current) return [current]
          if (removedIds.has(remote.id)) return []
          // A stream may start before the initial objects have loaded.
        }
        return [normalizeSite(remote, current?.logoBg ?? logoClasses[index % logoClasses.length]!)]
      })
      const ids = new Set(result.map(site => site.id))
      for (const current of sites.value) if (!ids.has(current.id) && (versions.get(current.id) ?? 0) !== (siteOperationVersions.value.get(current.id) ?? 0)) result.push(current)
      sites.value = result
    } catch {
      // Initial cache failure must not erase a newer operation's site objects.
    }
  }

  const readSiteSnapshot = async (id: string, signal?: AbortSignal, expectedWorkspace = workspaceId.value): Promise<boolean> => {
    if (disposed || signal?.aborted || expectedWorkspace !== workspaceId.value || pending.has(id)) return false
    const token = { epoch, workspace: workspaceId.value }
    const version = siteOperationVersions.value.get(id) ?? 0
    const remote = await listUpstreamSites(id, signal)
    if (!sameWorkspace(token) || signal?.aborted || pending.has(id) || version !== (siteOperationVersions.value.get(id) ?? 0)) return false
    // The API validates this too; retain the guard for adapters/test doubles.
    if (!Array.isArray(remote) || remote.length !== 1 || remote[0]?.id !== id || !remote[0]?.metrics || !Array.isArray(remote[0].metrics.groups)) throw new Error('本站缓存返回内容不完整，请检查 Hub 服务读取日志后重试；不会修改主站。')
    if (!sites.value.some(site => site.id === id)) return false
    applySite(remote[0], true)
    invalidateRead(id)
    return true
  }

  const addSite = async (form: UpstreamSiteForm): Promise<boolean> => {
    const token = { epoch, workspace: workspaceId.value }
    isAdding.value = true
    clearAddFailure()
    try {
      const site = await createUpstreamSite(form)
      if (!sameWorkspace(token)) return false
      removedIds.delete(site.id)
      invalidateRead(site.id)
      sites.value.unshift(normalizeSite(site, logoClasses[sites.value.length % logoClasses.length]!))
      return true
    } catch (error) {
      if (sameWorkspace(token)) {
        addErrorKey.value = safeUpstreamErrorKey(error)
        addFailure.value = error instanceof Error && 'failure' in error ? (error.failure as UpstreamFailure | undefined) ?? null : null
      }
      return false
    } finally {
      if (sameWorkspace(token)) isAdding.value = false
    }
  }

  const updateSite = async (id: string, form: UpstreamSiteForm): Promise<boolean> => {
    const token = beginOperation(id)
    isAdding.value = true
    clearAddFailure()
    try {
      const nextSite = await updateUpstreamSite(id, form)
      if (!isCurrent(token)) return false
      const current = sites.value.find(site => site.id === id)
      if (!current || removedIds.has(id)) return false
      applySite({ ...nextSite, settings: current.settings, enabled: current.enabled })
      return true
    } catch (error) {
      if (isCurrent(token)) {
        addErrorKey.value = safeUpstreamErrorKey(error)
        addFailure.value = error instanceof Error && 'failure' in error ? (error.failure as UpstreamFailure | undefined) ?? null : null
      }
      return false
    } finally {
      finishOperation(token)
      if (sameWorkspace(token)) isAdding.value = false
    }
  }

  const saveSiteSettings = async (id: string, settings: SiteSettings): Promise<void> => {
    const token = beginOperation(id, false)
    const submitted = { ...settings }
    try {
      await updateSiteSettings(id, submitted)
      if (!sameWorkspace(token)) return
      const site = sites.value.find(site => site.id === id)
      if (site) site.settings = submitted
    } finally {
      finishOperation(token)
    }
  }

  const syncSite = async (id: string) => {
    if (!sites.value.find(site => site.id === id)?.enabled || pending.has(id) || removedIds.has(id)) return
    const token = beginOperation(id, false)
    try {
      const nextSite = await syncUpstreamSite(id)
      if (isCurrent(token)) applySite(nextSite)
    } finally {
      finishOperation(token)
    }
  }

  const refreshSingleSite = async (id: string) => {
    if (!sites.value.find(site => site.id === id)?.enabled || syncingSiteIds.value.has(id) || pending.has(id) || removedIds.has(id)) return
    const token = beginOperation(id, false)
    siteSyncStates.value.delete(id)
    syncingSiteIds.value = new Set([...syncingSiteIds.value, id])
    try {
      const nextSite = await syncUpstreamSite(id)
      if (isCurrent(token)) applySite(nextSite)
    } catch (error) {
      if (isCurrent(token)) siteSyncStates.value.set(id, {
        phase: 'error', errorKey: safeUpstreamErrorKey(error),
        errorDetail: error instanceof Error && 'failure' in error ? (error.failure as UpstreamFailure | undefined)?.errorDetail : undefined,
      })
    } finally {
      finishOperation(token)
      if (sameWorkspace(token)) {
        const next = new Set(syncingSiteIds.value); next.delete(id); syncingSiteIds.value = next
      }
    }
  }

  const refreshSites = async () => {
    if (isRefreshing.value) return
    const owner = { epoch, workspace: workspaceId.value }
    const tokens = new Map(sites.value.filter(site => !pending.has(site.id) && !removedIds.has(site.id)).map(site => [site.id, beginOperation(site.id, false)]))
    isRefreshing.value = true
    try {
      const remoteSites = await syncAllUpstreamSites()
      if (!sameWorkspace(owner)) return
      const result: UpstreamSite[] = []
      for (const [index, remote] of remoteSites.entries()) {
        if (removedIds.has(remote.id)) continue
        const token = tokens.get(remote.id)
        const current = sites.value.find(site => site.id === remote.id)
        if ((token && isCurrent(token)) || (!token && !siteOperationVersions.value.has(remote.id))) {
          invalidateRead(remote.id)
          result.push(normalizeSite(remote, current?.logoBg ?? logoClasses[index % logoClasses.length]!))
        } else if (current) result.push(current)
      }
      const ids = new Set(result.map(site => site.id))
      for (const current of sites.value) {
        const token = tokens.get(current.id)
        if (!ids.has(current.id) && (!token || !isCurrent(token))) result.push(current)
      }
      sites.value = result
    } finally {
      for (const token of tokens.values()) finishOperation(token)
      if (sameWorkspace(owner)) isRefreshing.value = false
    }
  }

  const streamRefreshSites = async () => {
    if (isRefreshing.value) return
    const owner = { epoch, workspace: workspaceId.value }
    const tokens = new Map(sites.value.filter(site => !pending.has(site.id) && !removedIds.has(site.id)).map(site => [site.id, beginOperation(site.id, false)]))
    const terminal = new Set<string>()
    const controller = new AbortController()
    streamController = controller
    isRefreshing.value = true
    siteSyncStates.value = new Map([...siteSyncStates.value].filter(([id]) => !tokens.has(id)))
    try {
      await streamSyncAllUpstreamSites(event => {
        if (!sameWorkspace(owner) || controller.signal.aborted) return
        const id = event.siteId
        if (event.event === 'complete') {
          for (const [siteId, token] of tokens) if (!terminal.has(siteId)) { terminal.add(siteId); finishOperation(token) }
          if (streamController === controller) isRefreshing.value = false
          return
        }
        let token = tokens.get(id)
        // Initial load may complete after stream start. Enrol its site only if
        // no newer mutation has claimed it since the stream began.
        if (!token && !removedIds.has(id) && !pending.has(id) && !operationVersions.has(id)) {
          token = beginOperation(id, false); tokens.set(id, token)
        }
        // Determine applicability before releasing this token: finish invalidates
        // reads, and an obsolete result still has to release its own pending work.
        const canApply = token && isCurrent(token) && !removedIds.has(id) && sites.value.find(site => site.id === id)?.enabled !== false
        try {
          if (!canApply) return
          if (event.event === 'syncing') siteSyncStates.value.set(id, { phase: 'syncing' })
          if (event.event === 'done' || event.event === 'error') {
            invalidateRead(id)
            if (event.site) {
              if (!sites.value.some(site => site.id === id) && !removedIds.has(id)) sites.value.push(normalizeSite(event.site, logoClasses[sites.value.length % logoClasses.length]!))
              else applySite(event.site)
            }
            if (event.event === 'done') {
              siteSyncStates.value.set(id, { phase: 'done' })
              const doneState = siteSyncStates.value.get(id)
              const timer = setTimeout(() => {
                doneTimers.delete(timer)
                if (sameWorkspace(owner) && siteSyncStates.value.get(id) === doneState) siteSyncStates.value.delete(id)
              }, 2000)
              doneTimers.add(timer)
            } else siteSyncStates.value.set(id, { phase: 'error', errorKey: event.errorKey, errorDetail: event.errorDetail })
          }
        } finally {
          if ((event.event === 'done' || event.event === 'error') && token && !terminal.has(id)) {
            terminal.add(id)
            if (!canApply && streamController === controller && siteSyncStates.value.get(id)?.phase === 'syncing') siteSyncStates.value.delete(id)
            finishOperation(token)
          }
        }
      }, controller.signal)
    } catch {
      // Preserve the last confirmed objects when the stream is interrupted.
    } finally {
      for (const [id, token] of tokens) if (!terminal.has(id)) finishOperation(token)
      if (streamController === controller) {
        streamController = undefined
        if (sameWorkspace(owner)) isRefreshing.value = false
      }
    }
  }

  const deleteSite = async (id: string) => {
    const token = beginOperation(id, false)
    try {
      await removeUpstreamSite(id)
      if (sameWorkspace(token)) { removedIds.add(id); sites.value = sites.value.filter(site => site.id !== id); notifications?.siteManagementChanged(workspaceId.value, id, false, true) }
    } finally { finishOperation(token) }
  }

  const setSiteEnabled = async (id: string, enabled: boolean) => {
    if (enabledUpdatingIds.value.has(id)) return
    const site = sites.value.find(item => item.id === id)
    if (!site || site.enabled === enabled) return
    const token = beginOperation(id, false)
    enabledUpdatingIds.value = new Set([...enabledUpdatingIds.value, id])
    enabledErrorKeys.value.delete(id)
    try {
      const nextSite = await updateUpstreamSiteEnabled(id, enabled)
      if (!sameWorkspace(token)) return
      const current = sites.value.find(site => site.id === id)
      if (!current || removedIds.has(id)) return
      current.enabled = nextSite.enabled ?? true
      notifications?.siteManagementChanged(workspaceId.value, id, current.enabled)
      if (!enabled) {
        const next = new Set(syncingSiteIds.value); next.delete(id); syncingSiteIds.value = next
        siteSyncStates.value.delete(id)
      }
    } catch (error) {
      if (sameWorkspace(token)) notifications?.siteManagementChanged(workspaceId.value, id)
      if (sameWorkspace(token) && sites.value.some(site => site.id === id)) enabledErrorKeys.value.set(id, error instanceof Error && 'failure' in error ? (error.failure as UpstreamFailure | undefined)?.errorDetail ?? safeUpstreamErrorKey(error) : safeUpstreamErrorKey(error))
    } finally {
      finishOperation(token)
      if (sameWorkspace(token)) { const next = new Set(enabledUpdatingIds.value); next.delete(id); enabledUpdatingIds.value = next }
    }
  }

  watch(workspaceId, () => {
    epoch++
    streamController?.abort()
    for (const timer of doneTimers) clearTimeout(timer)
    doneTimers.clear()
    sites.value = []
    operationVersions = new Map(); pending = new Map(); removedIds = new Set()
    siteOperationVersions.value = new Map(); pendingSiteOperations.value = new Set()
    siteSyncStates.value = new Map(); syncingSiteIds.value = new Set()
    enabledUpdatingIds.value = new Set(); enabledErrorKeys.value = new Map()
    isAdding.value = false; isRefreshing.value = false; clearAddFailure()
    void loadSites()
  }, { flush: 'sync' })

  void loadSites()
  onBeforeUnmount(() => {
    disposed = true; epoch++
    streamController?.abort()
    for (const timer of doneTimers) clearTimeout(timer)
    doneTimers.clear()
  })
  return {
    sites, workspaceId, isAdding, isRefreshing, addErrorKey, addFailure, clearAddFailure,
    connectedCount, enabledSiteCount, enabledUpdatingIds, enabledErrorKeys, siteSyncStates,
    syncingSiteIds, siteOperationVersions, pendingSiteOperations, readSiteSnapshot,
    addSite, updateSite, saveSiteSettings, syncSite, refreshSingleSite, refreshSites,
    streamRefreshSites, deleteSite, setSiteEnabled, loadSites,
  }
}

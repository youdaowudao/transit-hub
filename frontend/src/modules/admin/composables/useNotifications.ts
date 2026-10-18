import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch, type InjectionKey, type Ref } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import {
  getNotificationDetail, getNotificationPreferences, getNotificationSummary, listNotifications,
  NotificationRequestError, saveNotificationPreferences, saveNotificationProgress,
} from '../api/notifications'
import type {
  NotificationDetailResponse, NotificationDisplayedState, NotificationHistoryFilter, NotificationLevel, NotificationProgress,
  NotificationProgressAction, NotificationSummaryQuery, NotificationSummaryResponse, SiteNotification,
} from '../types/notifications'
import { safeScopedErrorText } from '../utils/scopedErrorText'

export interface NotificationControllerOptions {
  workspaceId: Ref<string>
  isSwitching: Ref<boolean>
  enabled?: Ref<boolean>
  navigate?: (location: RouteLocationRaw) => unknown
}

export const hasVisibleNotificationDialog = (): boolean => {
  if (typeof document === 'undefined') return false
  return Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]')).some(element => {
    const style = getComputedStyle(element)
    const rect = element.getBoundingClientRect()
    return style.display !== 'none' && style.visibility !== 'hidden' && style.visibility !== 'collapse' && rect.width > 0 && rect.height > 0
  })
}

export const getNotificationCheckRoute = (detail: NotificationDetailResponse): RouteLocationRaw | null => {
  if (!detail.checkAction?.available) return null
  const action = detail.item.content.action
  if (action?.kind === 'group_rates' && action.siteId) {
    return { path: '/admin/group-rates', query: {
      siteId: action.siteId,
      ...(action.focusGroupId ? { focusGroupId: action.focusGroupId } : action.focusGroup ? { focusGroup: action.focusGroup } : {}),
    } }
  }
  if (action?.kind === 'connection_health') return { path: '/admin/connection-health' }
  return null
}

const reason = (error: unknown): string => error instanceof Error
  ? error.message === 'auth.errors.unauthorized' ? '登录状态已失效，请重新登录后读取当前工作区的通知' : safeScopedErrorText(error.message)
  : '无法确认具体原因，请维护人员核查Hub后端通知接口'
const mayHaveSaved = (error: unknown): boolean => error instanceof NotificationRequestError
  ? error.status === null || error.status >= 500 || error.status >= 200 && error.status < 300
  : error instanceof TypeError

export function useNotifications(options: NotificationControllerOptions) {
  const workspaceId = computed(() => options.workspaceId.value.trim())
  const summary = shallowRef<NotificationSummaryResponse | null>(null)
  const unreadCount = ref(0)
  const pendingCriticalCount = ref(0)
  const intervalSeconds = ref(300)
  const preferenceConfirmed = ref(false)
  const preferenceLoading = ref(false)
  const preferenceSaving = ref(false)
  const preferenceSaved = ref(false)
  const preferenceError = ref('')
  const summaryPreferenceError = ref('')
  const displayedPreferenceError = computed(() => preferenceError.value || summaryPreferenceError.value)
  const summaryLoading = ref(false)
  const summaryError = ref('')
  const managementRefreshPending = ref(false)
  const progressError = ref('')
  const lastCheckedAt = ref<string | null>(null)
  const attentionItems = shallowRef<SiteNotification[]>([])
  const importantItems = shallowRef<SiteNotification[]>([])
  const criticalBatch = shallowRef<SiteNotification[]>([])
  const criticalNextCursor = ref<string | null>(null)
  const criticalTotalCount = ref(0)
  const criticalOpen = ref(false)
  const drawerOpen = ref(false)
  const historyFilter = ref<NotificationHistoryFilter>('all')
  const historyItems = shallowRef<SiteNotification[]>([])
  const historyNextCursor = ref<string | null>(null)
  const historyLoading = ref(false)
  const historyError = ref('')
  const detail = shallowRef<NotificationDetailResponse | null>(null)
  const detailLoading = ref(false)
  const detailError = ref('')
  const busyIds = ref<string[]>([])
  const isVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  let mounted = false
  let stopped = false
  let workspaceEpoch = 0
  let lastCompletedAt: number | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  const requests = new Set<AbortController>()
  const notices = new Map<string, SiteNotification>()
  let historyRequest: AbortController | null = null
  let detailRequest: AbortController | null = null
  let preferenceRequest: AbortController | null = null
  let preferenceSaveRequest: AbortController | null = null
  let flight: { controller: AbortController; promise: Promise<boolean> } | null = null

  const ready = () => mounted && !stopped && Boolean(workspaceId.value) && !options.isSwitching.value && options.enabled?.value !== false
  const requestScope = () => {
    const controller = new AbortController()
    const workspace = workspaceId.value
    const epoch = workspaceEpoch
    requests.add(controller)
    return {
      controller, workspace,
      current: () => ready() && !controller.signal.aborted && epoch === workspaceEpoch && workspace === workspaceId.value,
      finish: () => requests.delete(controller),
    }
  }
  const clearTimer = () => {
    if (timer !== null) clearTimeout(timer)
    timer = null
  }
  const cancelSummary = () => {
    clearTimer()
    flight?.controller.abort()
    flight = null
    summaryLoading.value = false
  }
  const pause = () => {
    workspaceEpoch++
    cancelSummary()
    for (const controller of requests) controller.abort()
    requests.clear()
    historyRequest = detailRequest = preferenceRequest = preferenceSaveRequest = null
    historyLoading.value = detailLoading.value = preferenceLoading.value = preferenceSaving.value = false
    busyIds.value = []
    drawerOpen.value = criticalOpen.value = false
    attentionItems.value = importantItems.value = []
  }
  const reset = () => {
    summary.value = null
    managementRefreshPending.value = false
    notices.clear()
    unreadCount.value = pendingCriticalCount.value = 0
    intervalSeconds.value = 300
    preferenceConfirmed.value = preferenceSaved.value = false
    lastCheckedAt.value = null
    lastCompletedAt = null
    summaryError.value = progressError.value = preferenceError.value = historyError.value = detailError.value = ''
    summaryPreferenceError.value = ''
    attentionItems.value = importantItems.value = criticalBatch.value = historyItems.value = []
    drawerOpen.value = criticalOpen.value = false
    historyNextCursor.value = null
    criticalNextCursor.value = null
    criticalTotalCount.value = 0
    historyFilter.value = 'all'
    detail.value = null
  }
  const mergeNotice = (item: SiteNotification): SiteNotification => {
    const previous = notices.get(item.id)
    const merged = { ...item, readAt: previous?.readAt ?? item.readAt, reminderCompletedAt: previous?.reminderCompletedAt ?? item.reminderCompletedAt }
    notices.set(item.id, merged)
    return merged
  }
  const updateLoadedProgress = () => {
    // Reading a history projection must never restore automatic eligibility.
    const latest = (item: SiteNotification): SiteNotification => {
      const saved = notices.get(item.id)
      return saved ? { ...item, readAt: saved.readAt ?? item.readAt, reminderCompletedAt: saved.reminderCompletedAt ?? item.reminderCompletedAt } : item
    }
    historyItems.value = historyItems.value.map(latest).filter(item => historyFilter.value === 'all' || historyFilter.value === 'unread' && item.subjectActive && !item.readAt || historyFilter.value === 'pending' && item.subjectActive && item.level === 4 && !item.reminderCompletedAt)
    attentionItems.value = attentionItems.value.map(latest).filter(item => item.subjectActive)
    importantItems.value = importantItems.value.map(latest).filter(item => item.subjectActive && !item.reminderCompletedAt)
    criticalBatch.value = criticalBatch.value.map(latest).filter(item => item.subjectActive && !item.reminderCompletedAt)
    if (detail.value) detail.value = { ...detail.value, item: latest(detail.value.item) }
    if (summary.value) summary.value = { ...summary.value, unreadCount: unreadCount.value, pendingCriticalCount: pendingCriticalCount.value,
      active: {
        attention: { ...summary.value.active.attention, items: summary.value.active.attention.items.map(latest).filter(item => item.subjectActive && !item.reminderCompletedAt) },
        important: { ...summary.value.active.important, items: summary.value.active.important.items.map(latest).filter(item => item.subjectActive && !item.reminderCompletedAt) },
        critical: { ...summary.value.active.critical, items: summary.value.active.critical.items.map(latest).filter(item => item.subjectActive && !item.reminderCompletedAt), totalCount: pendingCriticalCount.value },
      },
    }
  }
  const applySubjectStates = (states: NotificationDisplayedState[]) => {
    const byId = new Map(states.map(state => [state.id, state]))
    const project = (item: SiteNotification): SiteNotification => {
      const state = byId.get(item.id)
      return state ? mergeNotice({ ...item, ...state }) : item
    }
    attentionItems.value = attentionItems.value.map(project)
    importantItems.value = importantItems.value.map(project)
    criticalBatch.value = criticalBatch.value.map(project)
    historyItems.value = historyItems.value.map(project)
    if (detail.value) detail.value = { ...detail.value, item: project(detail.value.item) }
    updateLoadedProgress()
  }
  const applyProgress = (items: NotificationProgress[], startingSummary: NotificationSummaryResponse | null) => {
    const sameSummary = summary.value !== null && summary.value === startingSummary && !managementRefreshPending.value
    // History and detail alone cannot prove that an ID contributes to this summary's counts.
    const countedIds = new Set(summary.value ? [
      ...summary.value.active.attention.items, ...summary.value.active.important.items,
      ...summary.value.active.critical.items, ...summary.value.displayedStates,
    ].filter(item => item.subjectActive).map(item => item.id) : [])
    const displayed = [...(drawerOpen.value ? historyItems.value : []), ...attentionItems.value, ...importantItems.value, ...(criticalOpen.value ? criticalBatch.value : []), ...(drawerOpen.value && detail.value ? [detail.value.item] : [])]
    let refreshNeeded = !sameSummary
    cancelSummary()
    for (const progress of items) {
      const previous = notices.get(progress.id)
      if (!previous) { refreshNeeded = true; continue }
      const views = displayed.filter(item => item.id === progress.id)
      const canCount = sameSummary && countedIds.has(progress.id) && views.length > 0 && views.every(item => item.subjectActive) && previous.subjectActive
      if (!canCount) refreshNeeded = true
      if (canCount && !previous.readAt && progress.readAt) unreadCount.value = Math.max(0, unreadCount.value - 1)
      if (canCount && !previous.reminderCompletedAt && progress.reminderCompletedAt) {
        if (previous.level === 4) pendingCriticalCount.value = Math.max(0, pendingCriticalCount.value - 1)
        if (summary.value && previous.level > 1) {
          const key = previous.level === 2 ? 'attention' : previous.level === 3 ? 'important' : 'critical'
          summary.value.active[key].totalCount = Math.max(0, summary.value.active[key].totalCount - 1)
        }
      }
      notices.set(progress.id, { ...previous, readAt: previous.readAt ?? progress.readAt, reminderCompletedAt: previous.reminderCompletedAt ?? progress.reminderCompletedAt })
    }
    updateLoadedProgress()
    if (criticalOpen.value && criticalBatch.value.length === 0) criticalOpen.value = false
    if (refreshNeeded) void refreshSummary()
    else schedule()
  }
  const applyPreference = (seconds: number) => {
    cancelSummary()
    preferenceRequest?.abort()
    preferenceRequest = null
    preferenceLoading.value = false
    intervalSeconds.value = seconds
    preferenceConfirmed.value = true
    summaryPreferenceError.value = ''
    if (summary.value) schedule()
    else void refreshSummary()
  }
  const showReminders = (forceCritical = false) => {
    if (!summary.value || !ready() || !isVisible() || managementRefreshPending.value) return
    importantItems.value = summary.value.active.important.items.filter(item => item.subjectActive && !item.reminderCompletedAt)
    if (criticalOpen.value && !forceCritical) return
    if (drawerOpen.value || hasVisibleNotificationDialog()) return
    const critical = summary.value.active.critical.items.filter(item => item.subjectActive && !item.reminderCompletedAt)
    if (critical.length) {
      criticalBatch.value = critical.slice(0, 20)
      criticalNextCursor.value = summary.value.active.critical.nextCursor
      criticalTotalCount.value = summary.value.active.critical.totalCount
      criticalOpen.value = true
      attentionItems.value = []
      return
    }
    attentionItems.value = summary.value.active.attention.items.filter(item => item.subjectActive && !item.reminderCompletedAt).slice(0, 20)
  }
  const schedule = () => {
    clearTimer()
    if (!ready() || !isVisible() || flight) return
    const remaining = lastCompletedAt === null ? intervalSeconds.value * 1000 : Math.max(0, lastCompletedAt + intervalSeconds.value * 1000 - Date.now())
    timer = setTimeout(() => { timer = null; void refreshSummary() }, remaining)
  }
  const refreshSummary = (cursors: Partial<NotificationSummaryQuery> = {}, forceCritical = false, suppressNewReminders = false): Promise<boolean> => {
    if (!ready()) return Promise.resolve(false)
    if (flight) return flight.promise
    clearTimer()
    const scope = requestScope()
    summaryLoading.value = true
    const candidate = { controller: scope.controller, promise: Promise.resolve(false) }
    flight = candidate
    candidate.promise = (async () => {
      try {
        const displayedIds = [...new Set([...attentionItems.value, ...importantItems.value, ...(criticalOpen.value ? criticalBatch.value : [])].map(item => item.id))]
        const response = await getNotificationSummary({ ...cursors, workspaceAdminAccountId: scope.workspace, ...(displayedIds.length ? { displayedIds } : {}) }, scope.controller.signal)
        if (!scope.current() || flight !== candidate) return false
        const hadCriticalOpen = criticalOpen.value
        const previousCriticalSize = criticalBatch.value.length
        applySubjectStates(response.displayedStates)
        if (criticalOpen.value && criticalBatch.value.length === 0) criticalOpen.value = false
        const emptiedCritical = hadCriticalOpen && !criticalOpen.value
        if (criticalOpen.value) criticalTotalCount.value = managementRefreshPending.value || previousCriticalSize !== criticalBatch.value.length
          ? response.active.critical.totalCount
          : Math.min(criticalTotalCount.value, response.active.critical.totalCount)
        const active = {
          attention: { ...response.active.attention, items: response.active.attention.items.map(mergeNotice).filter(item => item.subjectActive && !item.reminderCompletedAt) },
          important: { ...response.active.important, items: response.active.important.items.map(mergeNotice).filter(item => item.subjectActive && !item.reminderCompletedAt) },
          critical: { ...response.active.critical, items: response.active.critical.items.map(mergeNotice).filter(item => item.subjectActive && !item.reminderCompletedAt) },
        }
        summary.value = { ...response, active }
        unreadCount.value = response.unreadCount
        pendingCriticalCount.value = response.pendingCriticalCount
        summaryError.value = ''
        managementRefreshPending.value = false
        if (response.intervalSeconds !== null) {
          intervalSeconds.value = response.intervalSeconds
          preferenceConfirmed.value = true
          summaryPreferenceError.value = ''
        } else {
          summaryPreferenceError.value = `通知检查间隔暂时无法读取：${response.preferenceWarning ? [response.preferenceWarning.message, response.preferenceWarning.detail, response.preferenceWarning.next].filter(Boolean).join('；') : '原因尚无法确认，请核查Hub后端通知接口'}。消息仍可查看；暂按${intervalSeconds.value / 60}分钟检查，请重试读取通知设置。`
        }
        lastCheckedAt.value = new Date().toISOString()
        if (forceCritical) {
          // An explicit next page may replace an emptied batch, unless the user closed it while waiting.
          if (hadCriticalOpen) {
            criticalBatch.value = active.critical.items.slice(0, 20)
            criticalNextCursor.value = active.critical.nextCursor
            criticalTotalCount.value = active.critical.totalCount
            criticalOpen.value = criticalBatch.value.length > 0
          }
        } else {
          if (cursors.importantCursor) importantItems.value = active.important.items
          if (cursors.attentionCursor) attentionItems.value = active.attention.items
          if (!emptiedCritical && !suppressNewReminders) showReminders()
        }
        return true
      } catch (error) {
        if (scope.current() && flight === candidate) summaryError.value = managementRefreshPending.value
          ? `通知数量暂时无法确认：${reason(error)}。上次成功检查：${lastCheckedAt.value ? new Date(lastCheckedAt.value).toLocaleString() : '尚无'}。旧数量暂不显示，新的自动提醒暂停；请点击“重试读取”。不会修改主站。`
          : `通知暂时无法读取：${reason(error)}。上次成功检查：${lastCheckedAt.value ? new Date(lastCheckedAt.value).toLocaleString() : '尚无'}。原消息保留，请点击‘重试读取’。`
        return false
      } finally {
        scope.finish()
        if (scope.current() && flight === candidate) {
          flight = null
          summaryLoading.value = false
          lastCompletedAt = Date.now()
          schedule()
        }
      }
    })()
    return candidate.promise
  }
  const nextSummaryPage = (level: NotificationLevel) => {
    if (!summary.value || level === 1) return Promise.resolve(false)
    const key = level === 2 ? 'attention' : level === 3 ? 'important' : 'critical'
    const cursor = level === 4 && criticalOpen.value ? criticalNextCursor.value : summary.value.active[key].nextCursor
    if (!cursor) return Promise.resolve(false)
    return refreshSummary({ [level === 2 ? 'attentionCursor' : level === 3 ? 'importantCursor' : 'criticalCursor']: cursor }, level === 4)
  }
  const loadHistory = async (filter: NotificationHistoryFilter = historyFilter.value, cursor?: string): Promise<boolean> => {
    if (!ready()) return false
    historyRequest?.abort()
    const scope = requestScope()
    historyRequest = scope.controller
    historyLoading.value = true
    historyError.value = ''
    try {
      const response = await listNotifications({ workspaceAdminAccountId: scope.workspace, filter, ...(cursor ? { cursor } : {}) }, scope.controller.signal)
      if (!scope.current() || historyRequest !== scope.controller) return false
      historyFilter.value = filter
      historyItems.value = response.items.map(mergeNotice).filter(item => filter === 'all' || filter === 'unread' && item.subjectActive && !item.readAt || filter === 'pending' && item.subjectActive && item.level === 4 && !item.reminderCompletedAt)
      historyNextCursor.value = response.nextCursor
      return true
    } catch (error) {
      if (scope.current() && historyRequest === scope.controller) historyError.value = `当前工作区的消息页未能读取：${reason(error)}。原消息页保留，请重试读取。`
      return false
    } finally {
      scope.finish()
      if (scope.current() && historyRequest === scope.controller) historyLoading.value = false
    }
  }
  const openDrawer = async () => {
    if (!ready()) return
    const workspace = workspaceId.value
    const epoch = workspaceEpoch
    if (criticalOpen.value) {
      criticalOpen.value = false
      await nextTick()
    }
    if (!ready() || workspace !== workspaceId.value || epoch !== workspaceEpoch) return
    attentionItems.value = []
    drawerOpen.value = true
    detailRequest?.abort()
    detailLoading.value = false
    detailError.value = ''
    detail.value = null
    void refreshSummary()
    await loadHistory(!managementRefreshPending.value && unreadCount.value > 0 ? 'unread' : 'all')
  }
  const closeDrawer = () => { drawerOpen.value = false; detailRequest?.abort(); detailLoading.value = false }
  const openDetail = async (id: string, preserveDetail = false): Promise<boolean> => {
    if (!ready()) return false
    const workspace = workspaceId.value
    const epoch = workspaceEpoch
    if (criticalOpen.value) {
      criticalOpen.value = false
      await nextTick()
    }
    if (!ready() || workspace !== workspaceId.value || epoch !== workspaceEpoch) return false
    attentionItems.value = []
    drawerOpen.value = true
    detailRequest?.abort()
    const scope = requestScope()
    detailRequest = scope.controller
    detailLoading.value = true
    detailError.value = ''
    if (!preserveDetail) detail.value = null
    try {
      const response = await getNotificationDetail(scope.workspace, id, scope.controller.signal)
      if (!scope.current() || detailRequest !== scope.controller || !drawerOpen.value) return false
      detail.value = { ...response, item: mergeNotice(response.item) }
      return true
    } catch (error) {
      if (scope.current() && detailRequest === scope.controller) detailError.value = `这条消息的详情未能读取：${reason(error)}。请重新读取消息列表，再打开仍可查看的消息；本次没有标为已读。`
      return false
    } finally {
      scope.finish()
      if (scope.current() && detailRequest === scope.controller) detailLoading.value = false
    }
  }
  const progress = async (action: NotificationProgressAction, ids: string[]): Promise<boolean> => {
    const exactIds = [...new Set(ids)]
    if (!ready() || !exactIds.length || exactIds.some(id => busyIds.value.includes(id))) return false
    const scope = requestScope()
    const startingSummary = summary.value
    busyIds.value = [...busyIds.value, ...exactIds]
    progressError.value = ''
    try {
      const response = await saveNotificationProgress({ workspaceAdminAccountId: scope.workspace, ids: exactIds, action }, scope.controller.signal)
      if (!scope.current()) return false
      applyProgress(response.items, startingSummary)
      return true
    } catch (error) {
      if (!scope.current()) return false
      if (mayHaveSaved(error)) {
        try {
          const checks = await Promise.all(exactIds.map(id => getNotificationDetail(scope.workspace, id, scope.controller.signal)))
          if (!scope.current()) return false
          const confirmed = checks.every(check => (action !== 'read' && action !== 'ack' || Boolean(check.item.readAt)) && (action === 'read' ? check.item.level !== 3 || Boolean(check.item.reminderCompletedAt) : Boolean(check.item.reminderCompletedAt)))
          if (confirmed) {
            for (const check of checks) if (!notices.has(check.item.id)) notices.set(check.item.id, { ...check.item, readAt: null, reminderCompletedAt: null })
            applyProgress(checks.map(check => check.item), startingSummary)
            return true
          }
        } catch { /* 保留原进度和原错误，核对失败不能当作保存成功。 */ }
      }
      if (scope.current()) progressError.value = action === 'ack'
        ? mayHaveSaved(error)
          ? `这${exactIds.length}条消息的知悉保存结果暂时无法确认。请重试读取核对；本次没有修改主站。失败步骤：保存这些消息的知悉；${reason(error)}。原待确认状态保留，可重试这批消息。`
          : `这${exactIds.length}条消息的知悉没有保存：${reason(error)}。原待确认状态保留，请重新读取这些消息后重试；本次没有修改主站。`
        : `这${exactIds.length}条消息的${action === 'read' ? '已读' : '提醒完成'}未能确认保存：${reason(error)}。原状态保留，请重新读取对应消息后重试；本次没有修改主站。`
      return false
    } finally {
      scope.finish()
      if (scope.current()) busyIds.value = busyIds.value.filter(id => !exactIds.includes(id))
    }
  }
  const markDetailRead = async (id: string) => {
    if (!drawerOpen.value || !isVisible() || detail.value?.item.id !== id || detail.value.item.readAt && (detail.value.item.level !== 3 || detail.value.item.reminderCompletedAt)) return false
    return progress('read', [id])
  }
  const markPageRead = () => progress('read', historyItems.value.map(item => item.id))
  const presentAttention = (ids: string[]) => {
    if (!isVisible() || drawerOpen.value || criticalOpen.value || hasVisibleNotificationDialog()) return Promise.resolve(false)
    const displayed = new Set(attentionItems.value.filter(item => !item.reminderCompletedAt).map(item => item.id))
    return progress('present', ids.filter(id => displayed.has(id)))
  }
  const dismissImportant = (ids = importantItems.value.map(item => item.id)) => progress('dismiss', ids)
  const acknowledgeCritical = async (ids = criticalBatch.value.map(item => item.id)) => {
    const workspace = workspaceId.value
    const epoch = workspaceEpoch
    const result = await progress('ack', ids)
    if (result && ready() && workspace === workspaceId.value && epoch === workspaceEpoch && criticalBatch.value.length > 0 && criticalBatch.value.every(item => item.reminderCompletedAt)) criticalOpen.value = false
    return result
  }
  const laterCritical = () => { criticalOpen.value = false }
  const clearAttention = () => { attentionItems.value = [] }
  const loadPreferences = async (): Promise<boolean> => {
    if (!ready()) return false
    preferenceRequest?.abort()
    const scope = requestScope()
    preferenceRequest = scope.controller
    preferenceLoading.value = true
    preferenceError.value = ''
    preferenceSaved.value = false
    try {
      const response = await getNotificationPreferences(scope.workspace, scope.controller.signal)
      if (!scope.current() || preferenceRequest !== scope.controller) return false
      intervalSeconds.value = response.intervalSeconds
      preferenceConfirmed.value = true
      summaryPreferenceError.value = ''
      schedule()
      return true
    } catch (error) {
      if (scope.current() && preferenceRequest === scope.controller) preferenceError.value = `通知检查间隔暂时无法读取：${reason(error)}。消息仍可查看；暂按${intervalSeconds.value / 60}分钟检查，请重试读取通知设置。`
      return false
    } finally {
      scope.finish()
      if (scope.current() && preferenceRequest === scope.controller) preferenceLoading.value = false
    }
  }
  const savePreferences = async (minutes: number): Promise<boolean> => {
    preferenceSaved.value = false
    if (!Number.isInteger(minutes) || minutes < 1 || minutes > 60) {
      preferenceError.value = '通知检查间隔没有保存：请输入1—60之间的整数分钟；原间隔保持不变，请修改后重试。'
      return false
    }
    if (!ready() || preferenceSaving.value) return false
    const scope = requestScope()
    preferenceSaveRequest = scope.controller
    preferenceSaving.value = true
    preferenceError.value = ''
    try {
      const response = await saveNotificationPreferences({ workspaceAdminAccountId: scope.workspace, intervalMinutes: minutes }, scope.controller.signal)
      if (!scope.current() || preferenceSaveRequest !== scope.controller) return false
      applyPreference(response.intervalSeconds)
      preferenceSaved.value = true
      return true
    } catch (error) {
      if (!scope.current() || preferenceSaveRequest !== scope.controller) return false
      if (mayHaveSaved(error)) {
        try {
          const response = await getNotificationPreferences(scope.workspace, scope.controller.signal)
          if (!scope.current() || preferenceSaveRequest !== scope.controller) return false
          if (response.intervalSeconds === minutes * 60 && response.updatedAt !== null) {
            applyPreference(response.intervalSeconds)
            preferenceSaved.value = true
            return true
          }
        } catch { /* 未确认时保留原间隔。 */ }
      }
      if (scope.current()) preferenceError.value = `通知检查间隔未能确认保存：${reason(error)}。原间隔保持显示，请重新读取后再保存。`
      return false
    } finally {
      scope.finish()
      if (scope.current() && preferenceSaveRequest === scope.controller) preferenceSaving.value = false
    }
  }
  const siteManagementChanged = (workspace: string, siteId: string, confirmedEnabled?: boolean, deleted = false) => {
    if (!ready() || workspace !== workspaceId.value) return
    const hadCriticalOpen = criticalOpen.value
    cancelSummary()
    historyRequest?.abort()
    detailRequest?.abort()
    historyRequest = detailRequest = null
    historyLoading.value = detailLoading.value = false
    if (confirmedEnabled === false) {
      const message = deleted ? '相关网站已删除，这条消息保留为历史，不再主动提醒。' : '这个站点已停止管理，这条消息保留为历史，不再主动提醒。'
      const states = [...notices.values()].filter(item => item.subjectType === 'upstream_site' && item.subjectId === siteId).map(item => ({ id: item.id, subjectActive: false, subjectInactiveMessage: message, readAt: item.readAt, reminderCompletedAt: item.reminderCompletedAt }))
      applySubjectStates(states)
      if (criticalOpen.value && !criticalBatch.value.length) criticalOpen.value = false
      if (detail.value?.item.subjectType === 'upstream_site' && detail.value.item.subjectId === siteId && detail.value.checkAction) detail.value = { ...detail.value, checkAction: { available: !deleted && detail.value.checkAction.available, message: deleted ? message : `${message} 这里只查看已保存的倍率，不会自动刷新站点。` } }
    }
    managementRefreshPending.value = true
    summaryError.value = ''
    void refreshSummary({}, false, hadCriticalOpen && !criticalOpen.value)
    if (drawerOpen.value) {
      void loadHistory(historyFilter.value)
      if (detail.value) void openDetail(detail.value.item.id, true)
    }
  }
  const checkDetailAction = () => {
    if (!detail.value || !ready()) return false
    const location = getNotificationCheckRoute(detail.value)
    if (!location || !options.navigate) return false
    closeDrawer()
    options.navigate(location)
    return true
  }
  const handleVisibility = () => {
    clearTimer()
    if (!ready() || !isVisible()) return
    if (lastCompletedAt === null || Date.now() >= lastCompletedAt + intervalSeconds.value * 1000) void refreshSummary()
    else schedule()
  }
  const stop = () => { stopped = true; pause(); reset() }
  watch([workspaceId, () => options.isSwitching.value, () => options.enabled?.value !== false], ([workspace, switching, enabled], previous) => {
    const changed = workspace !== previous?.[0]
    if (changed || switching || !enabled || !workspace) pause()
    if (changed || !workspace) reset()
    if (!ready()) return
    if (changed || lastCompletedAt === null) void refreshSummary()
    else handleVisibility()
  }, { flush: 'sync' })
  onMounted(() => {
    mounted = true
    document.addEventListener('visibilitychange', handleVisibility)
    if (ready()) void refreshSummary()
  })
  onBeforeUnmount(() => {
    stop()
    mounted = false
    document.removeEventListener('visibilitychange', handleVisibility)
  })

  return {
    workspaceId, summary, unreadCount, pendingCriticalCount, intervalSeconds, preferenceConfirmed, preferenceLoading,
    preferenceSaving, preferenceSaved, preferenceError, summaryPreferenceError, displayedPreferenceError, summaryLoading, summaryError, managementRefreshPending, progressError, lastCheckedAt,
    attentionItems, importantItems, criticalBatch, criticalNextCursor, criticalTotalCount, criticalOpen, drawerOpen, historyFilter, historyItems,
    historyNextCursor, historyLoading, historyError, detail, detailLoading, detailError, busyIds,
    refreshSummary, nextSummaryPage, openDrawer, closeDrawer, loadHistory, openDetail, markDetailRead, markPageRead,
    progress, presentAttention, dismissImportant, acknowledgeCritical, laterCritical, clearAttention,
    loadPreferences, savePreferences, siteManagementChanged, checkDetailAction, stop,
  }
}

export type NotificationController = ReturnType<typeof useNotifications>
export const NotificationControllerKey: InjectionKey<NotificationController> = Symbol('current-workspace-notifications')
export const notificationControllerKey = NotificationControllerKey

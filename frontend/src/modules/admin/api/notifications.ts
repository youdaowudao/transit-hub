import {
  authUnauthorizedErrorKey,
  getAccessToken,
  handleAuthExpired,
  isUnauthorizedApiResponse,
} from '@/modules/auth/api/auth'
import { safeScopedErrorText } from '../utils/scopedErrorText'
import type {
  NotificationAction, NotificationActiveBucket, NotificationContent,
  NotificationDisplayedState, NotificationSubjectState, NotificationDetailResponse, NotificationHistoryQuery, NotificationHistoryResponse,
  NotificationLevel, NotificationPreferencesRequest, NotificationPreferencesResponse,
  NotificationProgress, NotificationProgressRequest, NotificationProgressResponse,
  NotificationSamplingState, NotificationSummaryQuery, NotificationSummaryResponse,
  NotificationWarning, SiteNotification,
} from '../types/notifications'

const apiBaseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')

export class NotificationRequestError extends Error {
  readonly status: number | null
  readonly detail: string
  readonly next: string
  readonly retryable: boolean
  readonly workspaceAdminAccountId: string

  constructor(message: string, detail: string, next: string, retryable: boolean, workspace: string, status: number | null = null) {
    message = safeScopedErrorText(message)
    detail = safeScopedErrorText(detail)
    next = safeScopedErrorText(next)
    super([message, detail, next].filter(Boolean).join('；'))
    this.name = 'NotificationRequestError'
    this.status = status
    this.detail = detail
    this.next = next
    this.retryable = retryable
    this.workspaceAdminAccountId = workspace
  }
}

const malformed = (workspace: string): never => {
  throw new NotificationRequestError('通知返回的内容无法确认', '返回结构、消息归属或必要字段不完整；请确认前后端已一起更新；这次读取不会被当作空消息或保存成功', '请重新读取当前工作区的消息或检查间隔；仍失败时请维护人员核查Hub后端通知接口', true, workspace)
}

const record = (value: unknown, workspace: string): Record<string, unknown> => {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return malformed(workspace)
  return value as Record<string, unknown>
}
const text = (value: unknown, workspace: string, max = 512, allowEmpty = false): string => {
  if (typeof value !== 'string' || (!allowEmpty && !value.trim()) || [...value].length > max) return malformed(workspace)
  return value
}
const count = (value: unknown, workspace: string): number => {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) return malformed(workspace)
  return value
}
const date = (value: unknown, workspace: string): string => {
  const result = text(value, workspace, 100)
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(result) || !Number.isFinite(Date.parse(result))) return malformed(workspace)
  return result
}
const nullableDate = (value: unknown, workspace: string): string | null => value === null ? null : date(value, workspace)
const cursor = (value: unknown, workspace: string): string | null => value === null ? null : text(value, workspace, 512)
const interval = (value: unknown, workspace: string): number => {
  const result = count(value, workspace)
  if (result < 60 || result > 3600 || result % 60 !== 0) return malformed(workspace)
  return result
}
const list = (value: unknown, workspace: string, max: number): unknown[] => {
  if (!Array.isArray(value) || value.length > max) return malformed(workspace)
  return value
}
const scope = (value: unknown, workspace: string): Record<string, unknown> => {
  const result = record(value, workspace)
  if (text(result.workspaceAdminAccountId, workspace) !== workspace) return malformed(workspace)
  return result
}
const warning = (value: unknown, workspace: string): NotificationWarning => {
  const result = record(value, workspace)
  return { message: text(result.message, workspace, 2000), detail: text(result.detail, workspace, 4000), next: text(result.next, workspace, 2000) }
}
const progress = (value: unknown, workspace: string): NotificationProgress => {
  const result = record(value, workspace)
  return { id: text(result.id, workspace), readAt: nullableDate(result.readAt, workspace), reminderCompletedAt: nullableDate(result.reminderCompletedAt, workspace) }
}
const subjectState = (value: unknown, workspace: string): NotificationSubjectState => {
  const result = record(value, workspace)
  if (typeof result.subjectActive !== 'boolean') return malformed(workspace)
  const message = text(result.subjectInactiveMessage, workspace, 2000, true)
  if (result.subjectActive ? message !== '' : !message.trim()) return malformed(workspace)
  return { subjectActive: result.subjectActive, subjectInactiveMessage: message }
}
const content = (value: unknown, workspace: string): NotificationContent => {
  const result = record(value, workspace)
  const output: NotificationContent = {
    title: text(result.title, workspace, 160), summary: text(result.summary, workspace, 1000),
    details: list(result.details, workspace, 20).map(value => {
      const item = record(value, workspace)
      return { label: text(item.label, workspace, 160), value: text(item.value, workspace, 1000, true) }
    }),
  }
  if (result.action !== undefined && result.action !== null) {
    const item = record(result.action, workspace)
    const action: NotificationAction = { kind: text(item.kind, workspace, 128) }
    if (!/^[a-z][a-z0-9_.-]{0,127}$/.test(action.kind)) return malformed(workspace)
    for (const key of ['siteId', 'focusGroupId', 'focusGroup'] as const) {
      if (item[key] !== undefined) action[key] = text(item[key], workspace)
    }
    if (action.kind === 'group_rates' && (!action.siteId || (action.focusGroupId && action.focusGroup))) return malformed(workspace)
    if (action.kind !== 'group_rates' && (action.siteId || action.focusGroupId || action.focusGroup)) return malformed(workspace)
    output.action = action
  }
  return output
}
const notice = (value: unknown, workspace: string): SiteNotification => {
  const result = record(value, workspace)
  if (text(result.adminAccountId, workspace) !== workspace) return malformed(workspace)
  const level = count(result.level, workspace)
  if (level < 1 || level > 4) return malformed(workspace)
  return {
    ...progress(result, workspace), ...subjectState(result, workspace), adminAccountId: workspace, kind: text(result.kind, workspace),
    subjectType: text(result.subjectType, workspace), subjectId: text(result.subjectId, workspace),
    level: level as NotificationLevel, discoveredAt: date(result.discoveredAt, workspace),
    createdAt: date(result.createdAt, workspace), content: content(result.content, workspace),
  }
}
const unique = <T extends { id: string }>(items: T[], workspace: string): T[] => {
  if (new Set(items.map(item => item.id)).size !== items.length) return malformed(workspace)
  return items
}
const bucket = (value: unknown, workspace: string, level: NotificationLevel): NotificationActiveBucket => {
  const result = record(value, workspace)
  const items = unique(list(result.items, workspace, 20).map(item => notice(item, workspace)), workspace)
  const totalCount = count(result.totalCount, workspace)
  if (items.some(item => item.level !== level || !item.subjectActive || item.reminderCompletedAt !== null) || totalCount < items.length) return malformed(workspace)
  return { items, totalCount, nextCursor: cursor(result.nextCursor, workspace) }
}
const sampling = (value: unknown, workspace: string): NotificationSamplingState => {
  const result = record(value, workspace)
  if (result.autoSyncEnabled !== null && typeof result.autoSyncEnabled !== 'boolean') return malformed(workspace)
  const sites = list(result.sites, workspace, 10).map(value => {
    const item = record(value, workspace)
    return { siteName: text(item.siteName, workspace), observedAt: nullableDate(item.observedAt, workspace), reason: text(item.reason, workspace, 1000) }
  })
  const unconfirmedSiteCount = result.unconfirmedSiteCount === null ? null : count(result.unconfirmedSiteCount, workspace)
  if (unconfirmedSiteCount !== null && sites.length > unconfirmedSiteCount) return malformed(workspace)
  const output: NotificationSamplingState = { autoSyncEnabled: result.autoSyncEnabled, unconfirmedSiteCount, sites }
  if (result.warning !== undefined) output.warning = warning(result.warning, workspace)
  if ((output.autoSyncEnabled === null || unconfirmedSiteCount === null) && !output.warning) return malformed(workspace)
  return output
}

const aborted = (signal?: AbortSignal): void => {
  if (signal?.aborted) throw new DOMException('通知请求已取消', 'AbortError')
}
const isAbort = (error: unknown): boolean => error !== null && typeof error === 'object' && 'name' in error && error.name === 'AbortError'
const errorText = (value: unknown, fallback: string): string => typeof value === 'string' && value.trim() ? value : fallback

const requestJson = async (path: string, workspace: string, options: RequestInit = {}, intent: 'read' | 'write' = 'read'): Promise<unknown> => {
  const signal = options.signal ?? undefined
  const saving = intent === 'write'
  aborted(signal)
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${apiBaseUrl}/notifications${path}`, {
      ...options,
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(options.headers ?? {}) },
    })
  } catch (error) {
    if (isAbort(error)) throw error
    aborted(signal)
    throw new NotificationRequestError(
      saving ? '通知保存请求没有取得服务端结果' : '通知资料读取请求没有取得服务端结果',
      saving ? '网络连接中断或服务暂时无法访问；消息进度或检查间隔是否已经保存无法确认' : '网络连接中断或服务暂时无法访问；本次读取没有完成，没有修改消息进度或检查间隔',
      saving ? '请恢复网络后重新读取对应消息或检查间隔，核对结果后重试相同操作' : '请恢复网络后重试读取当前工作区的通知资料', true, workspace,
    )
  }
  aborted(signal)
  let payload: unknown
  try {
    payload = JSON.parse(await response.text())
  } catch (error) {
    if (isAbort(error)) throw error
    aborted(signal)
    if (response.status === 401) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new NotificationRequestError(
      '通知返回的内容无法读取',
      saving ? '没有取得完整的JSON结果；消息进度或检查间隔是否已经保存无法确认' : '没有取得完整的JSON结果；这次读取不会被当作空消息，没有修改消息进度或检查间隔',
      '请重新读取对应消息或检查间隔后核对；仍失败时请维护人员检查Hub后端通知接口', true, workspace, response.status,
    )
  }
  aborted(signal)
  const errorPayload = payload !== null && typeof payload === 'object' && !Array.isArray(payload) ? payload as Record<string, unknown> : {}
  if (isUnauthorizedApiResponse(response.status, { message: typeof errorPayload.message === 'string' ? errorPayload.message : undefined })) {
    handleAuthExpired()
    throw new Error(authUnauthorizedErrorKey)
  }
  if (!response.ok) {
    throw new NotificationRequestError(
      errorText(errorPayload.message, '这次通知请求未能完成'),
      errorText(errorPayload.detail, saving ? '服务端没有提供可确认的具体原因；消息进度或检查间隔没有确认保存成功' : '服务端没有提供可确认的具体原因；通知资料读取没有完成，没有修改消息进度或检查间隔'),
      errorText(errorPayload.next, '请重新读取当前工作区的对应消息或检查间隔，核对后重试；仍失败时请检查Hub后端通知接口'),
      typeof errorPayload.retryable === 'boolean' ? errorPayload.retryable : true,
      workspace,
      response.status,
    )
  }
  return payload
}

const parameters = (workspace: string): URLSearchParams => {
  if (!workspace.trim()) throw new NotificationRequestError('没有选择通知所属的工作区', '这次请求没有发送，也没有保存任何进度或设置', '请选择工作区后重试', false, '')
  return new URLSearchParams({ workspaceAdminAccountId: workspace })
}

export const getNotificationSummary = async (query: NotificationSummaryQuery, signal?: AbortSignal): Promise<NotificationSummaryResponse> => {
  const workspace = query.workspaceAdminAccountId
  const params = parameters(workspace)
  for (const key of ['attentionCursor', 'importantCursor', 'criticalCursor'] as const) if (query[key]) params.set(key, query[key])
  const displayedIds = query.displayedIds ?? []
  unique(displayedIds.map(id => ({ id: text(id, workspace) })), workspace)
  if (displayedIds.length > 60) return malformed(workspace)
  const result = scope(await (displayedIds.length
    ? requestJson('/summary', workspace, { method: 'POST', body: JSON.stringify(query), signal }, 'read')
    : requestJson(`/summary?${params}`, workspace, { signal }, 'read')), workspace)
  const displayedStates: NotificationDisplayedState[] = unique(list(result.displayedStates, workspace, 60).map(value => ({ ...progress(value, workspace), ...subjectState(value, workspace) })), workspace)
  if (displayedStates.length !== displayedIds.length || displayedStates.some((item, index) => item.id !== displayedIds[index])) return malformed(workspace)
  const active = record(result.active, workspace)
  const output: NotificationSummaryResponse = {
    workspaceAdminAccountId: workspace, serverTime: date(result.serverTime, workspace),
    intervalSeconds: result.intervalSeconds === null ? null : interval(result.intervalSeconds, workspace),
    unreadCount: count(result.unreadCount, workspace), pendingCriticalCount: count(result.pendingCriticalCount, workspace),
    active: { attention: bucket(active.attention, workspace, 2), important: bucket(active.important, workspace, 3), critical: bucket(active.critical, workspace, 4) },
    samplingState: sampling(result.samplingState, workspace), displayedStates,
  }
  if (result.preferenceWarning !== undefined) output.preferenceWarning = warning(result.preferenceWarning, workspace)
  if (result.signupRisk !== undefined) {
    const risk = record(result.signupRisk, workspace)
    if (typeof risk.enabled !== 'boolean') return malformed(workspace)
    const items = unique(list(risk.items, workspace, 20).map(value => notice(value, workspace)), workspace)
    if (items.some(item => item.kind !== 'sub2api.signup_risk' || item.level !== 3 || !item.subjectActive || item.reminderCompletedAt !== null)) return malformed(workspace)
    output.signupRisk = { enabled: risk.enabled, items }
  }
  if (output.intervalSeconds === null && !output.preferenceWarning) return malformed(workspace)
  if (output.pendingCriticalCount !== output.active.critical.totalCount) return malformed(workspace)
  const activeItems = unique([...output.active.attention.items, ...output.active.important.items, ...output.active.critical.items], workspace)
  const statesById = new Map(displayedStates.map(item => [item.id, item]))
  if (activeItems.some(item => {
    const state = statesById.get(item.id)
    return state && (state.subjectActive !== item.subjectActive || state.subjectInactiveMessage !== item.subjectInactiveMessage || state.readAt !== item.readAt || state.reminderCompletedAt !== item.reminderCompletedAt)
  })) return malformed(workspace)
  return output
}

export const listNotifications = async (query: NotificationHistoryQuery, signal?: AbortSignal): Promise<NotificationHistoryResponse> => {
  const workspace = query.workspaceAdminAccountId
  const params = parameters(workspace)
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.cursor) params.set('cursor', query.cursor)
  if (query.filter) params.set('filter', query.filter)
  const result = scope(await requestJson(`/?${params}`, workspace, { signal }), workspace)
  const max = query.limit === undefined ? 20 : Math.min(query.limit, 50)
  const items = unique(list(result.items, workspace, max).map(item => notice(item, workspace)), workspace)
  if (query.filter === 'unread' && items.some(item => !item.subjectActive || item.readAt !== null) || query.filter === 'pending' && items.some(item => !item.subjectActive || item.level !== 4 || item.reminderCompletedAt !== null)) return malformed(workspace)
  return { workspaceAdminAccountId: workspace, items, nextCursor: cursor(result.nextCursor, workspace) }
}

export const getNotificationDetail = async (workspace: string, id: string, signal?: AbortSignal): Promise<NotificationDetailResponse> => {
  const result = scope(await requestJson(`/${encodeURIComponent(id)}?${parameters(workspace)}`, workspace, { signal }), workspace)
  const item = notice(result.item, workspace)
  if (item.id !== id) return malformed(workspace)
  const output: NotificationDetailResponse = { workspaceAdminAccountId: workspace, item }
  if (result.checkAction !== undefined) {
    const action = record(result.checkAction, workspace)
    if (typeof action.available !== 'boolean') return malformed(workspace)
    output.checkAction = { available: action.available, message: text(action.message, workspace, 1000) }
  }
  return output
}

export const saveNotificationProgress = async (request: NotificationProgressRequest, signal?: AbortSignal): Promise<NotificationProgressResponse> => {
  const workspace = request.workspaceAdminAccountId
  parameters(workspace)
  const result = scope(await requestJson('/progress', workspace, { method: 'POST', body: JSON.stringify(request), signal }, 'write'), workspace)
  const items = unique(list(result.items, workspace, 50).map(item => progress(item, workspace)), workspace)
  const ids = new Set(request.ids)
  if (items.length !== request.ids.length || items.some(item => !ids.has(item.id))) return malformed(workspace)
  if (items.some(item => (request.action === 'read' || request.action === 'ack') && item.readAt === null || request.action !== 'read' && item.reminderCompletedAt === null)) return malformed(workspace)
  return { workspaceAdminAccountId: workspace, items }
}

const preferenceResponse = (value: unknown, workspace: string): NotificationPreferencesResponse => {
  const result = scope(value, workspace)
  return { workspaceAdminAccountId: workspace, intervalSeconds: interval(result.intervalSeconds, workspace), updatedAt: nullableDate(result.updatedAt, workspace) }
}
export const getNotificationPreferences = async (workspace: string, signal?: AbortSignal): Promise<NotificationPreferencesResponse> => (
  preferenceResponse(await requestJson(`/preferences?${parameters(workspace)}`, workspace, { signal }), workspace)
)
export const saveNotificationPreferences = async (request: NotificationPreferencesRequest, signal?: AbortSignal): Promise<NotificationPreferencesResponse> => {
  const workspace = request.workspaceAdminAccountId
  parameters(workspace)
  const result = preferenceResponse(await requestJson('/preferences', workspace, { method: 'PUT', body: JSON.stringify(request), signal }, 'write'), workspace)
  if (result.intervalSeconds !== request.intervalMinutes * 60 || result.updatedAt === null) return malformed(workspace)
  return result
}
import type { SignupRiskPreferences } from '../types/notifications'

const signupRiskPreferenceResponse = (value: unknown, workspace: string): SignupRiskPreferences => {
  const result = scope(value, workspace)
  if (typeof result.enabled !== 'boolean' || typeof result.initialized !== 'boolean' || !result.enabled && result.initialized) return malformed(workspace)
  return { workspaceAdminAccountId: workspace, enabled: result.enabled, initialized: result.initialized, updatedAt: nullableDate(result.updatedAt, workspace) }
}
export const getSignupRiskPreferences = async (workspace: string, signal?: AbortSignal): Promise<SignupRiskPreferences> => (
  signupRiskPreferenceResponse(await requestJson(`/signup-risk-preferences?${parameters(workspace)}`, workspace, { signal }), workspace)
)
export const saveSignupRiskPreferences = async (request: { workspaceAdminAccountId: string; enabled: boolean }, signal?: AbortSignal): Promise<SignupRiskPreferences> => {
  const workspace = request.workspaceAdminAccountId
  parameters(workspace)
  const result = signupRiskPreferenceResponse(await requestJson('/signup-risk-preferences', workspace, { method: 'PUT', body: JSON.stringify(request), signal }, 'write'), workspace)
  if (result.enabled !== request.enabled || result.updatedAt === null) return malformed(workspace)
  return result
}

import type { AdminGroupTestConfiguration, GroupTestConfiguration } from '../types/connectionHealth'
import type {
  AccountTier,
  AccountTierResult,
  AccountManagementResult,
  AccountPriorityInput,
  AdminGroupPolicyConfiguration,
  AdminGroupPolicyConfigurationInput,
  AdminGroupHealth,
  ConnectionHealthEvent,
  ConnectionHealthOverview,
  ConnectionHealthStoredSummary,
  ConnectionHealthPolicy,
  ManualProbeModelOption,
  ManualProbeResult,
  ModelHealth,
  OwnGroupHealth,
  PolicyInput,
	PrioritySyncStatus,
  QuestionAnswerBatch,
  QuestionAnswerJudgment,
  QuestionAnswerReasoningEffort,
  QuestionAnswerHistory,
  QuestionAnswerHistoryScope,
  QuestionAnswerTodaySummary,
  QuestionAnswerQuestionStats,
  QuestionAnswerRecord,
  TestQuestion,
  TestQuestionInput,
  TargetPolicyAssignments,
  HealthRulePreset,
  HealthRulePresetInput,
  WorkspaceHealthSettings,
} from '../types/connectionHealth'
import {
  authUnauthorizedErrorKey,
  getAccessToken,
  handleAuthExpired,
  isUnauthorizedApiResponse,
} from '@/modules/auth/api/auth'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? '/api'

const endpoint = (path: string): string => `${apiBaseUrl.replace(/\/$/, '')}${path}`

const authHeaders = (): HeadersInit => {
  const token = getAccessToken()
  if (!token) return {}
  return { Authorization: `Bearer ${token}` }
}

const questionAnswerContractHeaders: HeadersInit = {
  'X-TransitHub-Question-Answer-Contract': '3',
}

type ApiErrorPayload = {
  message?: string
}

const requestJson = async <T>(path: string, options: RequestInit = {}): Promise<T> => {
  let response: Response
  try {
    response = await fetch(endpoint(path), {
      ...options,
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/json',
        ...authHeaders(),
        ...(options.headers ?? {}),
      },
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('admin.connectionHealth.errors.network')
  }

  const text = await response.text()
  const payload = text ? (JSON.parse(text) as T & ApiErrorPayload) : ({} as T & ApiErrorPayload)

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error(payload.message ?? 'admin.connectionHealth.errors.request')
  }

  return payload
}

export const getConnectionHealthOverview = async (): Promise<ConnectionHealthOverview> =>
  requestJson<ConnectionHealthOverview>('/connection-health/overview')

export const listHealthRulePresets = (): Promise<HealthRulePreset[]> => requestJson('/connection-health/rule-presets')
export const saveHealthRulePreset = (input: HealthRulePresetInput, id?: string): Promise<HealthRulePreset> =>
  requestJson(`/connection-health/rule-presets${id ? '/' + encodeURIComponent(id) : ''}`, { method: id ? 'PUT' : 'POST', body: JSON.stringify(input) })
export const deleteHealthRulePreset = (id: string): Promise<void> =>
  requestJson(`/connection-health/rule-presets/${encodeURIComponent(id)}`, { method: 'DELETE' })
export const applyHealthRulePresetToAll = (id: string): Promise<WorkspaceHealthSettings> =>
  requestJson(`/connection-health/rule-presets/${encodeURIComponent(id)}/apply-all`, { method: 'POST' })
export const switchWorkspaceHealthRule = (operation: 'restore-legacy' | 'switch-v2'): Promise<WorkspaceHealthSettings> =>
  requestJson(`/connection-health/rule-version/${operation}`, { method: 'POST' })
export const getWorkspaceHealthSettings = (): Promise<WorkspaceHealthSettings> => requestJson('/connection-health/workspace-settings')
export const saveWorkspaceProbeConcurrency = (probeConcurrency: number, probeConcurrencyVersion: number): Promise<WorkspaceHealthSettings> =>
  requestJson('/connection-health/workspace-settings', { method: 'PUT', body: JSON.stringify({ probeConcurrency, probeConcurrencyVersion }) })

export const getConnectionHealthStoredSummary = async (): Promise<ConnectionHealthStoredSummary> =>
  requestJson<ConnectionHealthStoredSummary>('/connection-health/stored-summary')

export const getConnectionHealthGroups = async (): Promise<OwnGroupHealth[]> =>
  requestJson<OwnGroupHealth[]>('/connection-health/groups')

const parseAccountTier = (payload: unknown, targetId: string, expectedTier?: AccountTier): AccountTierResult => {
  if (!payload || typeof payload !== 'object') throw new Error('admin.connectionHealth.errors.request')
  const result = payload as Partial<AccountTierResult>
  if (result.targetId !== targetId || (result.accountTier !== 1 && result.accountTier !== 2)
    || (expectedTier !== undefined && result.accountTier !== expectedTier)) {
    throw new Error('admin.connectionHealth.errors.request')
  }
  return { targetId, accountTier: result.accountTier }
}

export const getAccountTier = async (targetId: string): Promise<AccountTierResult> =>
  parseAccountTier(await requestJson<unknown>(`/connection-health/targets/${encodeURIComponent(targetId)}/tier`), targetId)

export const saveAccountTier = async (targetId: string, accountTier: AccountTier): Promise<AccountTierResult> =>
  parseAccountTier(await requestJson<unknown>(`/connection-health/targets/${encodeURIComponent(targetId)}/tier`, {
    method: 'PUT',
    body: JSON.stringify({ accountTier }),
  }), targetId, accountTier)

const requestAccountManagement = async (
  targetId: string,
  field: 'priority-owner' | 'concurrency',
  input: AccountPriorityInput | { concurrency: number },
): Promise<AccountManagementResult> => {
  if (!/^sub2api:[^:]+:.+$/.test(targetId)) throw new Error('admin.connectionHealth.errors.accountManagementSub2apiOnly')
  const unknown = (): AccountManagementResult => ({ targetId, result: 'pending', errorKey: field === 'concurrency'
    ? 'admin.connectionHealth.errors.concurrencyUnconfirmed' : 'admin.connectionHealth.errors.priorityWriteUnconfirmed' })
  let response: Response
  try {
    response = await fetch(endpoint(`/connection-health/targets/${encodeURIComponent(targetId)}/${field}`), {
      method: 'PUT', headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() }, body: JSON.stringify(input),
    })
  } catch { return unknown() }
  let payload: Partial<AccountManagementResult> & ApiErrorPayload
  try { payload = await response.json() } catch { return unknown() }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return unknown()
  if (isUnauthorizedApiResponse(response.status, payload)) {
    handleAuthExpired(); throw new Error(authUnauthorizedErrorKey)
  }
  // 发送前校验拒绝可以重试；5xx、断连和非法回执都不能证明主站没有写入。
  if (!response.ok) {
    if (response.status >= 400 && response.status < 500) throw new Error(payload.message ?? 'admin.connectionHealth.errors.request')
    return unknown()
  }
  if (!payload || payload.targetId !== targetId || !['success', 'not_sent', 'pending', 'noop'].includes(payload.result ?? '')) return unknown()
  if (payload.result === 'success' || payload.result === 'noop') {
    const value = field === 'concurrency' ? payload.concurrency : payload.priority
    if (!Number.isInteger(value) || Number(value) < 1) return unknown()
    if ('concurrency' in input && (payload.concurrency !== input.concurrency
      || (payload.loadFactor !== undefined && (!Number.isFinite(payload.loadFactor) || payload.loadFactor < 0)))) return unknown()
    if ('mode' in input && input.mode === 'manual' && payload.priority !== input.priority) return unknown()
    if ('mode' in input && input.mode === 'auto' && Number(payload.priority) <= 9) return unknown()
  }
  return { ...payload, targetId, result: payload.result! }
}

export const saveAccountPriority = (targetId: string, input: AccountPriorityInput): Promise<AccountManagementResult> => {
  if (input.mode !== 'auto' && (input.mode !== 'manual' || !Number.isInteger(input.priority) || input.priority < 1 || input.priority > 9)) {
    throw new Error('admin.connectionHealth.errors.priorityManualInvalid')
  }
  return requestAccountManagement(targetId, 'priority-owner', input)
}

export const saveAccountConcurrency = (targetId: string, concurrency: number): Promise<AccountManagementResult> => {
  if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > 1000) throw new Error('admin.connectionHealth.errors.concurrencyInvalid')
  return requestAccountManagement(targetId, 'concurrency', { concurrency })
}

const parseAdminGroupHealth = (payload: unknown): AdminGroupHealth[] => {
  if (!Array.isArray(payload)) throw new Error('admin.connectionHealth.errors.request')
  for (const group of payload) {
    if (!group || typeof group !== 'object' || !Array.isArray((group as { accounts?: unknown }).accounts)) {
      throw new Error('admin.connectionHealth.errors.request')
    }
  }
  return payload as AdminGroupHealth[]
}

// getConnectionHealthAdminGroups 拉取 admin 全量分组健康主列表（含账号/渠道与探活叠加）。
// 对应后端新增路由，不影响旧的 getConnectionHealthGroups。
export const getConnectionHealthAdminGroups = async (): Promise<AdminGroupHealth[]> =>
  parseAdminGroupHealth(await requestJson<unknown>('/connection-health/admin-groups'))

export type AdminGroupsRefreshSite = {
  siteId: string
  status: 'success' | 'auth_failed' | 'stale' | 'unavailable' | 'timeout' | 'disabled'
  errorKey?: string
}

export type AdminGroupsRefreshSummary = {
  state: 'success' | 'partial' | 'failure' | 'timeout'
  errorKey?: string
  sites: AdminGroupsRefreshSite[]
}

export type AdminGroupsFreshResponse = {
  groups: AdminGroupHealth[]
  refresh: AdminGroupsRefreshSummary
}

export type AdminGroupsRefreshStage = 'discovering' | 'site_sync' | 'multiplier_refresh' | 'main_groups' | 'complete'

export type AdminGroupsRefreshWaiting = {
  siteId: string
  siteName: string
  phase: string
  elapsedSeconds: number
}

export type AdminGroupsRefreshIssue = {
  siteId?: string
  siteName?: string
  phase: string
  status: string
  errorKey: string
}

export type AdminGroupsRefreshSnapshot = {
  runId: string
  mode?: 'manual' | 'automatic'
  runState: 'running' | 'complete'
  stage: AdminGroupsRefreshStage
  revision: number
  startedAt?: string
  updatedAt?: string
  stageCompletedSites?: number
  stageTotalSites?: number
  waiting?: AdminGroupsRefreshWaiting[]
  issues?: AdminGroupsRefreshIssue[]
}

export type AdminGroupsRefreshTerminal = {
  status: 'success' | 'failed'
  runId?: string
  revision?: number
  groups?: AdminGroupHealth[]
  refresh: AdminGroupsRefreshSummary
  errorKey?: string
  failedStage?: AdminGroupsRefreshStage
}

export type AdminGroupsRefreshConflict = {
  errorKey: string
  runId: string
}

export type AdminGroupsRefreshResult = AdminGroupsRefreshTerminal & {
  conflict?: AdminGroupsRefreshConflict
}

export type AdminGroupsRefreshConnectionState = 'connected' | 'reconnecting'

export type AdminGroupsRefreshStreamOptions = {
  onSnapshot?: (snapshot: AdminGroupsRefreshSnapshot) => void
  onTerminal?: (terminal: AdminGroupsRefreshTerminal) => void
  onConflict?: (conflict: AdminGroupsRefreshConflict) => void
  onConnectionState?: (state: AdminGroupsRefreshConnectionState) => void
  signal?: AbortSignal
}

const terminalRefreshStates = new Set<AdminGroupsRefreshSummary['state']>(['success', 'partial', 'failure', 'timeout'])
const terminalRefreshSiteStatuses = new Set<AdminGroupsRefreshSite['status']>(['success', 'auth_failed', 'stale', 'unavailable', 'timeout', 'disabled'])

// 方案 A 手动刷新：后端等待涉及站点倍率任务全部进入终态后，只读取一轮主站分组和账号。
// 不提供前端状态轮询，也不重复调用 admin-groups。
const parseAdminGroupsFreshResponse = (payload: AdminGroupsFreshResponse | AdminGroupHealth[]): AdminGroupsFreshResponse => {
  if (
    !payload
    || Array.isArray(payload)
    || !Array.isArray(payload.groups)
    || !payload.refresh
    || !terminalRefreshStates.has(payload.refresh.state)
    || !Array.isArray(payload.refresh.sites)
    || payload.refresh.sites.some(site => (
      !site
      || typeof site.siteId !== 'string'
      || !terminalRefreshSiteStatuses.has(site.status)
    ))
  ) {
    throw new Error('admin.connectionHealth.errors.request')
  }
  return {
    ...payload,
    groups: parseAdminGroupHealth(payload.groups),
  }
}

const refreshReconnectDelays = [250, 500, 1_000, 2_000]

class RefreshStreamProtocolError extends Error {}

const refreshStreamProtocolError = (): RefreshStreamProtocolError =>
  new RefreshStreamProtocolError('admin.connectionHealth.errors.request')

const parseRefreshTerminal = (payload: unknown): AdminGroupsRefreshTerminal => {
  if (!payload || typeof payload !== 'object') throw refreshStreamProtocolError()
  const terminal = payload as Partial<AdminGroupsRefreshTerminal>
  if (
    (terminal.status !== 'success' && terminal.status !== 'failed')
    || !terminal.refresh
    || !terminalRefreshStates.has(terminal.refresh.state)
    || !Array.isArray(terminal.refresh.sites)
  ) {
    throw refreshStreamProtocolError()
  }
  if (terminal.status === 'success' && !Array.isArray(terminal.groups)) {
    throw refreshStreamProtocolError()
  }
  if (terminal.status === 'success') {
    try {
      terminal.groups = parseAdminGroupHealth(terminal.groups)
    } catch {
      throw refreshStreamProtocolError()
    }
  }
  return terminal as AdminGroupsRefreshTerminal
}

const parseRefreshSnapshot = (payload: unknown): AdminGroupsRefreshSnapshot => {
  if (!payload || typeof payload !== 'object') throw refreshStreamProtocolError()
  const snapshot = payload as Partial<AdminGroupsRefreshSnapshot>
  if (
    typeof snapshot.runId !== 'string'
    || !snapshot.runId
    || typeof snapshot.revision !== 'number'
    || snapshot.runState !== 'running'
    || typeof snapshot.stage !== 'string'
  ) {
    throw refreshStreamProtocolError()
  }
  return snapshot as AdminGroupsRefreshSnapshot
}

const parseRefreshErrorPayload = async (response: Response): Promise<ApiErrorPayload & Partial<AdminGroupsRefreshConflict>> => {
  try {
    const text = await response.text()
    return text ? JSON.parse(text) as ApiErrorPayload & Partial<AdminGroupsRefreshConflict> : {}
  } catch {
    return {}
  }
}

const waitForRefreshReconnect = (delay: number, signal?: AbortSignal): Promise<void> => new Promise((resolve, reject) => {
  if (signal?.aborted) {
    reject(new DOMException('Aborted', 'AbortError'))
    return
  }
  const timer = setTimeout(() => {
    signal?.removeEventListener('abort', abort)
    resolve()
  }, delay)
  const abort = () => {
    clearTimeout(timer)
    reject(new DOMException('Aborted', 'AbortError'))
  }
  signal?.addEventListener('abort', abort, { once: true })
})

type RefreshStreamReadResult = {
  runId: string
  terminal: AdminGroupsRefreshTerminal | null
}

const readRefreshStream = async (
  response: Response,
  options: AdminGroupsRefreshStreamOptions,
  initialRunId: string,
  onRunId: (runId: string) => void,
): Promise<RefreshStreamReadResult> => {
  if (!response.body) throw refreshStreamProtocolError()
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let runId = initialRunId
  let terminal: AdminGroupsRefreshTerminal | null = null

  const notify = <T>(observer: ((value: T) => void) | undefined, value: T) => {
    try {
      observer?.(value)
    } catch {
      // 观察回调不参与传输控制，尤其不能让已收到的 terminal 重新触发请求。
    }
  }

  const consume = (block: string) => {
    if (terminal) return
    const lines = block.split(/\r?\n/)
    const eventName = lines.find(line => line.startsWith('event:'))?.slice(6).trim() ?? ''
    const data = lines
      .filter(line => line.startsWith('data:'))
      .map(line => line.slice(5).trimStart())
      .join('\n')
    if (!data) return
    let payload: unknown
    try {
      payload = JSON.parse(data) as unknown
    } catch {
      throw refreshStreamProtocolError()
    }
    if (eventName === 'snapshot') {
      const snapshot = parseRefreshSnapshot(payload)
      runId = snapshot.runId
      onRunId(runId)
      notify(options.onSnapshot, snapshot)
      return
    }
    if (eventName === 'terminal') {
      terminal = parseRefreshTerminal(payload)
      if (terminal.runId) {
        runId = terminal.runId
        onRunId(runId)
      }
      notify(options.onTerminal, terminal)
    }
  }

  while (!terminal) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split(/\r?\n\r?\n/)
    buffer = parts.pop() ?? ''
    for (const part of parts) {
      consume(part)
      if (terminal) break
    }
  }
  buffer += decoder.decode()
  // EOF 残留没有 SSE 空行边界，交给外层按断线处理，不能尝试解析半截 JSON。
  if (terminal) {
    try {
      await reader.cancel()
    } catch {
      // terminal 已是权威终态，取消底层 reader 失败不改变业务结果。
    }
  }
  return { runId, terminal }
}

const refreshAdminGroupsWithStream = async (
  initialMethod: 'GET' | 'POST',
  options: AdminGroupsRefreshStreamOptions = {},
): Promise<AdminGroupsRefreshResult> => {
  let method = initialMethod
  let runId = ''
  let conflict: AdminGroupsRefreshConflict | undefined
  let reconnectAttempt = 0
  let reconnecting = false

  const notifyConnectionState = (state: AdminGroupsRefreshConnectionState) => {
    try {
      options.onConnectionState?.(state)
    } catch {
      // 连接状态只用于可见反馈，不参与传输控制。
    }
  }

  for (;;) {
    const controller = new AbortController()
    const abort = () => controller.abort()
    if (options.signal?.aborted) controller.abort()
    else options.signal?.addEventListener('abort', abort, { once: true })

    let reconnectReason: 'network' | 'eof' | null = null
    try {
      const query = runId ? `?${new URLSearchParams({ run_id: runId }).toString()}` : ''
      let response: Response | null = null
      try {
        response = await fetch(endpoint(`/connection-health/admin-groups/refresh${query}`), {
          method,
          headers: {
            Accept: 'text/event-stream',
            'Content-Type': 'application/json',
            ...authHeaders(),
          },
          signal: controller.signal,
        })
      } catch (error) {
        if (error instanceof Error && error.name === 'AbortError') throw error
        if (!runId) throw new Error('admin.connectionHealth.errors.network')
        reconnectReason = 'network'
      }

      if (response && !response.ok) {
        const payload = await parseRefreshErrorPayload(response)
        if (
          response.status === 409
          && method === 'POST'
          && !conflict
          && typeof payload.runId === 'string'
          && payload.runId
        ) {
          conflict = {
            errorKey: typeof payload.errorKey === 'string' && payload.errorKey ? payload.errorKey : 'refresh_run_conflict',
            runId: payload.runId,
          }
          runId = payload.runId
          method = 'GET'
          try {
            options.onConflict?.(conflict)
          } catch {
            // 冲突提示是观察状态，不得阻断接入已有 run。
          }
          continue
        }
        if (isUnauthorizedApiResponse(response.status, payload)) {
          handleAuthExpired()
          throw new Error(authUnauthorizedErrorKey)
        }
        throw new Error(payload.errorKey ?? payload.message ?? 'admin.connectionHealth.errors.request')
      }

      if (response && reconnecting) {
        reconnecting = false
        notifyConnectionState('connected')
      }

      const contentType = response?.headers.get('Content-Type')?.toLowerCase() ?? ''
      if (response && !contentType.includes('text/event-stream')) {
        let rawPayload: AdminGroupsFreshResponse | AdminGroupHealth[]
        try {
          rawPayload = await response.json() as AdminGroupsFreshResponse | AdminGroupHealth[]
        } catch {
          throw refreshStreamProtocolError()
        }
        const payload = parseAdminGroupsFreshResponse(rawPayload)
        const terminal: AdminGroupsRefreshTerminal = {
          status: 'success',
          groups: payload.groups,
          refresh: payload.refresh,
        }
        try {
          options.onTerminal?.(terminal)
        } catch {
          // JSON fallback 与 SSE 一致，终态观察回调不改变已经取得的终态。
        }
        return { ...terminal, conflict }
      }

      if (response) {
        let stream: RefreshStreamReadResult
        try {
          stream = await readRefreshStream(response, options, runId, nextRunId => { runId = nextRunId })
        } catch (error) {
          if (error instanceof Error && error.name === 'AbortError') throw error
          if (error instanceof RefreshStreamProtocolError) throw error
          if (!runId) throw new Error('admin.connectionHealth.errors.network')
          reconnectReason = 'network'
          stream = { runId, terminal: null }
        }
        runId = stream.runId
        if (stream.terminal) return { ...stream.terminal, conflict }
        reconnectReason = 'eof'
      }
    } finally {
      options.signal?.removeEventListener('abort', abort)
    }

    if (!runId || reconnectAttempt >= refreshReconnectDelays.length) {
      throw new Error(reconnectReason === 'network'
        ? 'admin.connectionHealth.errors.network'
        : 'admin.connectionHealth.errors.request')
    }
    reconnecting = true
    notifyConnectionState('reconnecting')
    await waitForRefreshReconnect(refreshReconnectDelays[reconnectAttempt], options.signal)
    reconnectAttempt++
    method = 'GET'
  }
}

export const refreshConnectionHealthAdminGroups = async (
  options: AdminGroupsRefreshStreamOptions = {},
): Promise<AdminGroupsRefreshResult> => refreshAdminGroupsWithStream('POST', options)

export const refreshConnectionHealthAdminGroupsAutomatically = async (
  options: AdminGroupsRefreshStreamOptions = {},
): Promise<AdminGroupsRefreshResult> => refreshAdminGroupsWithStream('GET', options)

export const getPrioritySyncStatus = async (): Promise<PrioritySyncStatus> =>
	requestJson<PrioritySyncStatus>('/connection-health/priority-sync-status')

export const getConnectionHealthEvents = async (connectionId?: string, limit = 100): Promise<ConnectionHealthEvent[]> => {
  const params = new URLSearchParams()
  if (connectionId) params.set('connectionId', connectionId)
  params.set('limit', String(limit))
  return requestJson<ConnectionHealthEvent[]>(`/connection-health/events?${params.toString()}`)
}

// probeConnection 触发一次手动探活。models 为空或未传时保持旧行为：探活该连接匹配到的
// 全部启用模型目标（向后兼容旧调用方）。传入 models 时后端只探活这些模型名对应的匹配目标。
export const probeConnection = async (connectionId: string, models?: string[]): Promise<ModelHealth[]> =>
  requestJson<ModelHealth[]>(`/connection-health/connections/${encodeURIComponent(connectionId)}/probe`, {
    method: 'POST',
    body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
  })

// probeTarget 触发一次独立目标的手动探活：按 targetId + models 走，后端 server-only 解析凭据，
// 请求体绝不携带 base_url/key/credentials。models 为空时后端探活该目标全部候选模型。
export const probeTarget = async (targetId: string, models?: string[], signal?: AbortSignal): Promise<ModelHealth[]> =>
  requestJson<ModelHealth[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/probe`, {
    method: 'POST',
    body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
    signal,
  })

export type ProbeTargetProgressPhase = 'queued' | 'running'

type ProbeTargetStreamEvent = {
  type: 'phase' | 'result' | 'error'
  phase?: ProbeTargetProgressPhase
  results?: ModelHealth[]
  errorKey?: string
}

// probeTargetWithProgress 是正式手动探活的 SSE 版本。只有阶段通过流式事件推送，
// 结果和错误语义与 probeTarget 保持一致；一次性探活继续使用普通 JSON 接口。
export const probeTargetWithProgress = async (
  targetId: string,
  models: string[] | undefined,
  onPhase: (phase: ProbeTargetProgressPhase) => void,
  signal?: AbortSignal,
): Promise<ModelHealth[]> => {
  let response: Response
  try {
    response = await fetch(endpoint(`/connection-health/targets/${encodeURIComponent(targetId)}/probe-stream`), {
      method: 'POST',
      headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json', ...authHeaders() },
      body: models && models.length > 0 ? JSON.stringify({ models }) : undefined,
      signal,
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('admin.connectionHealth.errors.network')
  }

  if (!response.ok) {
    let payload: ApiErrorPayload = {}
    try {
      const text = await response.text()
      payload = text ? JSON.parse(text) as ApiErrorPayload : {}
    } catch {
      // 保持通用请求错误，避免把上游响应原文带到界面。
    }
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error(payload.message ?? 'admin.connectionHealth.errors.request')
  }

  if (!response.body) throw new Error('admin.connectionHealth.errors.request')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let results: ModelHealth[] | null = null
  let streamErrorKey = ''

  const consume = (part: string) => {
    const dataLine = part.split('\n').find(line => line.startsWith('data: '))
    if (!dataLine) return
    try {
      const event = JSON.parse(dataLine.slice(6)) as ProbeTargetStreamEvent
      if (event.type === 'phase' && event.phase) {
        onPhase(event.phase)
      } else if (event.type === 'result') {
        results = event.results ?? []
      } else if (event.type === 'error') {
        streamErrorKey = event.errorKey ?? 'admin.connectionHealth.errors.unknown'
      }
    } catch {
      // 忽略单条格式错误事件，最终没有结果时统一返回请求错误。
    }
  }

  // eslint-disable-next-line no-constant-condition
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split('\n\n')
    buffer = parts.pop() ?? ''
    parts.forEach(consume)
  }
  buffer += decoder.decode()
  if (buffer.trim()) consume(buffer)
  if (streamErrorKey) throw new Error(streamErrorKey)
  if (results === null) throw new Error('admin.connectionHealth.errors.request')
  return results
}

// discoverTargetModels 是手动一次性探活弹窗打开时调用的 server-only 模型发现接口：
// 后端用当前 admin session 临时解析该 target 的 base_url + key 请求上游 /v1/models，
// 这里只拿到安全字段（id/name/ownedBy/providerFamily），前端绝不接触凭据本身。
export const discoverTargetModels = async (targetId: string, signal?: AbortSignal): Promise<ManualProbeModelOption[]> =>
  requestJson<ManualProbeModelOption[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/models`, { signal })

// manualProbeOnce 触发一次「一次性」探活：不写策略状态/事件，结果仅用于弹窗内即时展示。
// models 必须非空——手动一次性探活没有候选池概念，必须由用户在弹窗里显式勾选。
export const manualProbeOnce = async (targetId: string, models: string[], signal?: AbortSignal): Promise<ManualProbeResult[]> =>
  requestJson<ManualProbeResult[]>(`/connection-health/targets/${encodeURIComponent(targetId)}/manual-probe`, {
    method: 'POST',
    body: JSON.stringify({ models }),
    signal,
  })

type TestQuestionPayload = Omit<TestQuestion, 'keywords'> & { keywords?: unknown }
type QuestionAnswerRecordPayload = Omit<QuestionAnswerRecord, 'questionKeywordSnapshot'> & {
  questionKeywordSnapshot?: unknown
}
type QuestionAnswerStatsPayload = Omit<QuestionAnswerBatch['stats'], 'byModel' | 'byQuestion'> & { byModel?: unknown; byQuestion?: unknown }
type QuestionAnswerBatchPayload = Omit<QuestionAnswerBatch, 'records' | 'repeatCount' | 'stats'> & {
  records: QuestionAnswerRecordPayload[]
  repeatCount?: unknown
  stats: QuestionAnswerStatsPayload
}
type QuestionAnswerHistoryPayload = QuestionAnswerHistory

const normalizeTestQuestion = (question: TestQuestionPayload): TestQuestion => ({
  ...question,
  keywords: Array.isArray(question.keywords) ? [...question.keywords] : [],
})

const normalizeQuestionAnswerRecord = (record: QuestionAnswerRecordPayload): QuestionAnswerRecord => ({
  ...record,
  judgmentSource: record.judgmentSource === 'automatic' || record.judgmentSource === 'manual' ? record.judgmentSource : null,
  repeatIndex: typeof record.repeatIndex === 'number' ? record.repeatIndex : null,
  questionKeywordSnapshot: Array.isArray(record.questionKeywordSnapshot)
    ? [...record.questionKeywordSnapshot]
    : null,
})

const normalizeQuestionAnswerQuestionStats = (item: QuestionAnswerQuestionStats): QuestionAnswerQuestionStats => ({
  ...item, normalizedKeywords: [...(item.normalizedKeywords ?? [])],
  requests: { ...item.requests }, reviews: { ...item.reviews },
  byModel: (item.byModel ?? []).map(model => ({ ...model, requests: { ...model.requests }, reviews: { ...model.reviews } })),
})

const normalizeQuestionAnswerStats = (stats: QuestionAnswerStatsPayload): QuestionAnswerBatch['stats'] => ({
  ...stats,
  requests: { ...stats.requests },
  reviews: { ...stats.reviews },
  byQuestion: Array.isArray(stats.byQuestion) ? stats.byQuestion.map(normalizeQuestionAnswerQuestionStats) : [],
  byModel: Array.isArray(stats.byModel)
    ? stats.byModel.map(item => ({
      ...item,
      requests: { ...item.requests },
      reviews: { ...item.reviews },
    }))
    : [],
})

const normalizeQuestionAnswerBatch = (batch: QuestionAnswerBatchPayload): QuestionAnswerBatch => ({
  ...batch,
  repeatCount: typeof batch.repeatCount === 'number' ? batch.repeatCount : 1,
  records: batch.records.map(normalizeQuestionAnswerRecord),
  stats: normalizeQuestionAnswerStats(batch.stats),
})

const normalizeQuestionAnswerHistory = (history: QuestionAnswerHistoryPayload): QuestionAnswerHistory => ({
  ...history,
  batches: history.batches.map(batch => ({ ...batch, models: [...batch.models], questions: batch.questions.map(normalizeQuestionAnswerQuestionStats), stats: normalizeQuestionAnswerStats(batch.stats) })),
  allTimeStats: normalizeQuestionAnswerStats(history.allTimeStats),
  todayStats: normalizeQuestionAnswerStats(history.todayStats),
})

export const listTestQuestions = async (signal?: AbortSignal): Promise<TestQuestion[]> => {
  const questions = await requestJson<TestQuestionPayload[]>('/connection-health/test-questions', { signal })
  return questions.map(normalizeTestQuestion)
}

export const createTestQuestion = async (input: TestQuestionInput): Promise<TestQuestion> => {
  const question = await requestJson<TestQuestionPayload>('/connection-health/test-questions', { method: 'POST', body: JSON.stringify(input) })
  return normalizeTestQuestion(question)
}

export const updateTestQuestion = async (questionId: string, input: TestQuestionInput): Promise<TestQuestion> => {
  const question = await requestJson<TestQuestionPayload>(`/connection-health/test-questions/${encodeURIComponent(questionId)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })
  return normalizeTestQuestion(question)
}

export const setTestQuestionEnabled = async (questionId: string, enabled: boolean): Promise<TestQuestion> => {
  const question = await requestJson<TestQuestionPayload>(`/connection-health/test-questions/${encodeURIComponent(questionId)}/enabled`, {
    method: 'POST',
    body: JSON.stringify({ enabled }),
  })
  return normalizeTestQuestion(question)
}

export const setDefaultTestQuestion = async (questionId: string): Promise<TestQuestion> => {
  const question = await requestJson<TestQuestionPayload>(`/connection-health/test-questions/${encodeURIComponent(questionId)}/default`, { method: 'POST' })
  return normalizeTestQuestion(question)
}

export const deleteTestQuestion = async (questionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/test-questions/${encodeURIComponent(questionId)}`, { method: 'DELETE' })
}

export const startQuestionAnswerBatch = async (
  targetId: string,
  models: string[],
  questionIds: string[],
  reasoningEffort: QuestionAnswerReasoningEffort,
  repeatCountOrSignal: number | AbortSignal = 1,
  nextSignal?: AbortSignal,
): Promise<QuestionAnswerBatch> => {
  const repeatCount = typeof repeatCountOrSignal === 'number' ? repeatCountOrSignal : 1
  const signal = typeof repeatCountOrSignal === 'number' ? nextSignal : repeatCountOrSignal
  const batch = await requestJson<QuestionAnswerBatchPayload>(`/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches`, {
    method: 'POST',
    headers: questionAnswerContractHeaders,
    body: JSON.stringify({ models, questionIds, reasoningEffort, repeatCount }),
    signal,
  })
  return normalizeQuestionAnswerBatch(batch)
}

export const getLatestQuestionAnswerBatch = async (targetId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> => {
  const batch = await requestJson<QuestionAnswerBatchPayload>(`/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/latest`, {
    headers: questionAnswerContractHeaders,
    signal,
  })
  return normalizeQuestionAnswerBatch(batch)
}

export const getQuestionAnswerBatch = async (targetId: string, batchId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> => {
  const batch = await requestJson<QuestionAnswerBatchPayload>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/${encodeURIComponent(batchId)}`,
    { headers: questionAnswerContractHeaders, signal },
  )
  return normalizeQuestionAnswerBatch(batch)
}

export const cancelQuestionAnswerBatch = async (targetId: string, batchId: string, signal?: AbortSignal): Promise<QuestionAnswerBatch> => {
  const batch = await requestJson<QuestionAnswerBatchPayload>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/batches/${encodeURIComponent(batchId)}/cancel`,
    { method: 'POST', headers: questionAnswerContractHeaders, signal },
  )
  return normalizeQuestionAnswerBatch(batch)
}

export const getQuestionAnswerHistory = async (targetId: string, page: number, scope: QuestionAnswerHistoryScope = 'today', signal?: AbortSignal): Promise<QuestionAnswerHistory> => {
  const history = await requestJson<QuestionAnswerHistoryPayload>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/history?page=${page}&scope=${scope}`,
    { headers: questionAnswerContractHeaders, signal },
  )
  return normalizeQuestionAnswerHistory(history)
}

export const getQuestionAnswerSummary = async (targetId: string, signal?: AbortSignal): Promise<QuestionAnswerTodaySummary> => {
  const summary = await requestJson<QuestionAnswerTodaySummary>(`/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/summary`, { headers: questionAnswerContractHeaders, signal })
  return { targetId: summary.targetId, todayStats: { requests: { ...summary.todayStats.requests }, reviews: { ...summary.todayStats.reviews } } }
}

export const setQuestionAnswerJudgment = async (
  targetId: string,
  recordId: string,
  judgment: Exclude<QuestionAnswerJudgment, 'unreviewed'>,
  expectedUpdatedAt: string,
  signal?: AbortSignal,
): Promise<QuestionAnswerRecord> => {
  const record = await requestJson<QuestionAnswerRecordPayload>(
    `/connection-health/targets/${encodeURIComponent(targetId)}/question-answers/records/${encodeURIComponent(recordId)}/judgment`,
    { method: 'PUT', headers: questionAnswerContractHeaders, body: JSON.stringify({ judgment, expectedUpdatedAt }), signal },
  )
  return normalizeQuestionAnswerRecord(record)
}

export interface TargetSchedulableActionResult {
  targetId: string
  schedulable: boolean
  actionSource: string
  actionAt: string
}

export const setTargetSchedulable = async (targetId: string, schedulable: boolean): Promise<TargetSchedulableActionResult> =>
  requestJson<TargetSchedulableActionResult>(`/connection-health/targets/${encodeURIComponent(targetId)}/schedulable`, {
    method: 'POST',
    body: JSON.stringify({ schedulable }),
  })

// getTargetPolicyAssignments / setTargetPolicyAssignments 管理「账号/channel 显式分配策略」
// 关系：只有分配了已启用策略的 target，后台调度器才会自动探活。
export const getTargetPolicyAssignments = async (targetId: string): Promise<TargetPolicyAssignments> =>
  requestJson<TargetPolicyAssignments>(`/connection-health/targets/${encodeURIComponent(targetId)}/policy-assignments`)

export const setTargetPolicyAssignments = async (targetId: string, policyIds: string[]): Promise<TargetPolicyAssignments> =>
  requestJson<TargetPolicyAssignments>(`/connection-health/targets/${encodeURIComponent(targetId)}/policy-assignments`, {
    method: 'PUT',
    body: JSON.stringify({ policyIds }),
  })

export const getAdminGroupPolicyConfiguration = async (adminGroupId: string): Promise<AdminGroupPolicyConfiguration> =>
  requestJson<AdminGroupPolicyConfiguration>(
    `/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/policy-configuration`,
  )

export const setAdminGroupPolicyConfiguration = async (
  adminGroupId: string,
  input: AdminGroupPolicyConfigurationInput,
): Promise<AdminGroupPolicyConfiguration> =>
  requestJson<AdminGroupPolicyConfiguration>(
    `/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/policy-configuration`,
    { method: 'PUT', body: JSON.stringify(input) },
  )

export const disableConnection = async (connectionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/connections/${encodeURIComponent(connectionId)}/disable`, {
    method: 'POST',
  })
}

export const restoreConnection = async (connectionId: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/connections/${encodeURIComponent(connectionId)}/restore`, {
    method: 'POST',
  })
}

export const listConnectionHealthPolicies = async (): Promise<ConnectionHealthPolicy[]> =>
  requestJson<ConnectionHealthPolicy[]>('/connection-health/policies')

export const createConnectionHealthPolicy = async (input: PolicyInput): Promise<ConnectionHealthPolicy> =>
  requestJson<ConnectionHealthPolicy>('/connection-health/policies', {
    method: 'POST',
    body: JSON.stringify(input),
  })

export const updateConnectionHealthPolicy = async (id: string, input: PolicyInput): Promise<ConnectionHealthPolicy> =>
  requestJson<ConnectionHealthPolicy>(`/connection-health/policies/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(input),
  })

export const deleteConnectionHealthPolicy = async (id: string): Promise<void> => {
  await requestJson<{ ok: boolean }>(`/connection-health/policies/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}


export const getAdminGroupTestConfiguration = (adminGroupId: string): Promise<AdminGroupTestConfiguration> =>
  requestJson(`/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/test-configuration`)

export const setAdminGroupTestConfiguration = (adminGroupId: string, configuration: GroupTestConfiguration | null): Promise<AdminGroupTestConfiguration> =>
  requestJson(`/connection-health/admin-groups/${encodeURIComponent(adminGroupId)}/test-configuration`, {
    method: 'PUT', body: JSON.stringify({ configuration }),
  })

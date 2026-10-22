import type { AdminGroupTestConfiguration, GroupTestConfiguration } from '../types/connectionHealth'
import { t, te } from '@/locales'
import { questionAnswerReadableError } from '../utils/questionAnswers'
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
  QuestionAnswerQueue,
  QuestionAnswerJudgment,
  QuestionAnswerReasoningEffort,
  QuestionAnswerHistory,
  QuestionAnswerHistoryScope,
  QuestionAnswerTodaySummary,
  QuestionAnswerRecentSummaryItem,
  ModelControlAccountSummary, ModelControlAutoSettings, ModelControlSettings, ModelControlCounts, ModelControlItem, ModelControlPreview, ModelControlResult, ModelControlBasis, ModelControlOperation, ModelControlPage, ModelControlEvent,
  QuestionAnswerRuntimeSettings,
  QuestionAnswerScheduleLimits,
  QuestionAnswerScheduleInput,
  QuestionAnswerSchedulePreviewInput,
  QuestionAnswerSchedulePreview,
  QuestionAnswerSchedule,
  QuestionAnswerSchedulePage,
  QuestionAnswerScheduleExecution,
  QuestionAnswerScheduleExecutionDetail,
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
  error?: string
  current?: unknown
  activeExecutionId?: string
  detail?: string
  step?: string
  operationResult?: 'not_started' | 'unknown' | 'completed'
  batchId?: string
}

// C2 conflicts carry the authoritative object; callers must not treat 409 as a completed mutation.
export class ConnectionHealthApiError extends Error {
  readonly errorKey: string
  readonly step?: string
  readonly operationResult?: 'not_started' | 'unknown' | 'completed'
  readonly batchId?: string
  constructor(message: string, public readonly status: number, public readonly current?: unknown, public readonly activeExecutionId?: string, metadata?: Pick<ApiErrorPayload, 'message' | 'step' | 'operationResult' | 'batchId'>) {
    super(message)
    this.name = 'ConnectionHealthApiError'
    this.errorKey = metadata?.message ?? message
    this.step = metadata?.step
    this.operationResult = metadata?.operationResult
    this.batchId = metadata?.batchId
  }
}

const questionAnswerOperationFailure = (path: string, options: RequestInit, status: number, key: string, payload: ApiErrorPayload = {}): ConnectionHealthApiError => {
  const step = path.startsWith('/connection-health/question-answer-queue') ? 'queue' : path.endsWith('/cancel') ? 'cancelling' : path.endsWith('/judgment') ? 'judging'
    : options.method?.toUpperCase() === 'POST' ? 'creating' : path.includes('/history') ? 'history' : 'reading'
  const reading = step === 'reading' || step === 'history' || step === 'queue'
  const createValidationKeys = new Set([
    'questionAnswerSelection', 'questionAnswerReasoningEffort', 'questionAnswerRepeatCount', 'questionAnswerBatchLimit', 'questionAnswerActive',
    'testQuestionInvalid', 'testQuestionNotFound', 'testQuestionDisabled', 'modelUnavailable', 'targetNotFound', 'accountsFetch',
    'credentialUnavailable', 'secureVerificationRequired', 'baseUrlUnavailable', 'exportUnavailable', 'credentialsRedacted',
    'modelListUnavailable', 'modelListInvalid', 'testProtocolNotAllowed', 'testConfigurationConflict', 'testConfigurationUnavailable', 'questionAnswerServiceStopped', 'questionAnswerContractMismatch',
  ].map(name => `admin.connectionHealth.errors.${name}`))
  const knownPreparationRefusal = step === 'creating' && status > 0 && status < 500 && (createValidationKeys.has(key) || [
    'admin.upstream.errors.announcementAckRequired', 'admin.upstream.errors.upstreamInsufficientBalance',
    'admin.upstream.errors.upstreamKeyQuotaExhausted', 'admin.upstream.errors.upstreamKeyExpired', 'admin.adminAccounts.errors.noCurrentAccount',
  ].includes(key))
  const knownJudgmentRefusal = step === 'judging' && status > 0 && status < 500 && ['admin.connectionHealth.errors.questionAnswerJudgmentConflict', 'admin.connectionHealth.errors.questionAnswerJudgmentForbidden'].includes(key)
  const result = payload.operationResult ?? (knownPreparationRefusal || knownJudgmentRefusal ? 'not_started' : 'unknown')
  let message = payload.detail
  // New backends supply complete, safe wording. Legacy keys alone prove no
  // mutation outcome except for the known pre-create validation refusals.
  if (!message || !payload.operationResult) {
    const genericReason = key === 'admin.connectionHealth.errors.network' ? '连接后台失败，接口未返回更具体的原因'
      : ['admin.connectionHealth.errors.request', 'admin.connectionHealth.errors.unknown'].includes(key) ? '接口未返回可确认的答题操作结果，也未提供更具体原因' : ''
    const reason = payload.detail || genericReason || (key === 'admin.connectionHealth.errors.questionAnswerStorage'
      ? '接口只返回了答题记录操作未取得确认的分类，没有提供更具体原因'
      : key.endsWith('.internal_error')
        ? '接口返回了答题程序内部错误分类，没有提供更具体原因'
        : te(key) || te(`admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.${key}`)
          ? questionAnswerReadableError(key, t, te) : '接口没有返回可识别的错误分类，也未提供安全的具体原因，请按批次编号和时间核查后台日志')
    const guidance = reading ? '本次只读取状态，没有重新发题；请重新读取批次或历史。'
      : result === 'not_started' ? (step === 'judging' ? '本次没有保存或修改人工判断，请先重新读取这条记录后再操作。'
        : step === 'cancelling' ? '本次没有执行停止操作，请处理已知原因后再操作。' : '本次尚未创建题目，请处理已知原因后再操作。')
        : step === 'judging' ? '目前无法确认这次人工判断是否已保存，请先重新读取这条记录，再决定是否重试。'
          : step === 'cancelling' ? '目前无法确认这批题是否已结束，请先重新读取该批次，不要把本次操作当作已完成。'
            : '目前无法确认这批题是否已提交，请先查看账号最近批次或历史核对，本次不会自动重发。'
    message = `${reason}。${guidance}`
  }
  return new ConnectionHealthApiError(message, status, payload.current, payload.activeExecutionId, { message: key, operationResult: result, step: payload.step ?? step, batchId: payload.batchId })
}

const questionAnswerPayloadObject = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
// Check only identities and the containers that the existing normalizers read.
// Missing optional keyword/repeat/diagnostic fields retain their old defaults.
const validQuestionAnswerRecordPayload = (value: unknown) => questionAnswerPayloadObject(value)
  && ['id', 'targetId', 'batchId', 'updatedAt'].every(field => typeof value[field] === 'string' && value[field] !== '')
  && ['pending', 'running', 'succeeded', 'failed', 'cancelled'].includes(String(value.status))
const validQuestionAnswerQuestionStatsPayload = (value: unknown): boolean => questionAnswerPayloadObject(value)
  && (value.normalizedKeywords == null || Array.isArray(value.normalizedKeywords))
  && (value.byModel == null || (Array.isArray(value.byModel) && value.byModel.every(questionAnswerPayloadObject)))
const validQuestionAnswerStatsPayload = (value: unknown): boolean => questionAnswerPayloadObject(value)
  && questionAnswerPayloadObject(value.requests) && questionAnswerPayloadObject(value.reviews)
  && (!Array.isArray(value.byModel) || value.byModel.every(questionAnswerPayloadObject))
  && (!Array.isArray(value.byQuestion) || value.byQuestion.every(validQuestionAnswerQuestionStatsPayload))
const validQuestionAnswerSuccess = (path: string, value: unknown): boolean => {
  if (!questionAnswerPayloadObject(value)) return false
  if (path.endsWith('/judgment')) {
    const recordID = path.match(/\/records\/([^/]+)\/judgment$/)?.[1]
    return typeof value.id === 'string' && Boolean(recordID) && value.id === decodeURIComponent(recordID!)
  }
  if (path.includes('/question-answers/batches')) return typeof value.batchId === 'string' && Array.isArray(value.records) && value.records.every(validQuestionAnswerRecordPayload) && validQuestionAnswerStatsPayload(value.stats)
  if (path.includes('/question-answers/history')) return Array.isArray(value.batches) && value.batches.every(batch => questionAnswerPayloadObject(batch) && typeof batch.batchId === 'string' && Array.isArray(batch.models) && Array.isArray(batch.questions) && batch.questions.every(validQuestionAnswerQuestionStatsPayload) && validQuestionAnswerStatsPayload(batch.stats)) && validQuestionAnswerStatsPayload(value.todayStats)
  if (path.endsWith('/question-answers/summary')) return typeof value.targetId === 'string' && validQuestionAnswerStatsPayload(value.todayStats)
  return true
}

const requestJson = async <T>(path: string, options: RequestInit = {}): Promise<T> => {
  const queueOperation = path.startsWith('/connection-health/question-answer-queue')
  const answerOperation = path.includes('/question-answers/') || queueOperation
  const answerSessionIdentity = answerOperation ? getAccessToken() : null
  const requireCurrentAnswerSession = () => {
    // An old answer response must not expire a newer login before its caller
    // can reject that response. Other modules retain their existing behavior.
    if (answerOperation && (options.signal?.aborted || answerSessionIdentity !== getAccessToken())) throw new DOMException('Aborted', 'AbortError')
  }
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
    requireCurrentAnswerSession()
    if (error instanceof Error && error.name === 'AbortError') throw error
    if (answerOperation) {
      throw questionAnswerOperationFailure(path, options, 0, 'admin.connectionHealth.errors.network', { detail: '连接后台时中断，接口未返回更具体原因' })
    }
    throw new Error('admin.connectionHealth.errors.network')
  }

  let payload: T & ApiErrorPayload
  try {
    requireCurrentAnswerSession()
    const text = await response.text()
    requireCurrentAnswerSession()
    payload = text ? (JSON.parse(text) as T & ApiErrorPayload) : ({} as T & ApiErrorPayload)
  } catch (error) {
    requireCurrentAnswerSession()
    if (error instanceof Error && error.name === 'AbortError') throw error
    if (!answerOperation) throw error
    if (isUnauthorizedApiResponse(response.status, {})) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw questionAnswerOperationFailure(path, options, response.status, 'admin.connectionHealth.errors.request', { detail: '后台返回的答题内容无法读取，接口未给出可确认的操作结果' })
  }

  if (answerOperation && (payload === null || typeof payload !== 'object' || Array.isArray(payload))) {
    if (isUnauthorizedApiResponse(response.status, {})) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw questionAnswerOperationFailure(path, options, response.status, 'admin.connectionHealth.errors.request', { operationResult: 'unknown' })
  }

  if (!response.ok) {
    if (answerOperation) {
      const body = payload as unknown as Record<string, unknown>
      const message = typeof body.message === 'string' ? body.message : undefined
      if (isUnauthorizedApiResponse(response.status, { message })) {
        handleAuthExpired()
        throw new Error(authUnauthorizedErrorKey)
      }
      const key = message ?? (typeof body.error === 'string' ? body.error : 'admin.connectionHealth.errors.request')
      const validFields = ['message', 'error', 'detail', 'step', 'batchId', 'activeExecutionId'].every(field => body[field] === undefined || typeof body[field] === 'string')
        && (body.operationResult === undefined || body.operationResult === 'not_started' || body.operationResult === 'unknown' || body.operationResult === 'completed')
      if (!validFields) throw questionAnswerOperationFailure(path, options, response.status, key, { operationResult: 'unknown' })
      throw questionAnswerOperationFailure(path, options, response.status, key, payload)
    }
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    const key = payload.message ?? payload.error ?? 'admin.connectionHealth.errors.request'
    throw new ConnectionHealthApiError(key, response.status, payload.current, payload.activeExecutionId)
  }

  if (answerOperation && !validQuestionAnswerSuccess(path, payload)) {
    throw questionAnswerOperationFailure(path, options, response.status, 'admin.connectionHealth.errors.request', { detail: '后台返回的答题内容缺少必要记录或无法读取，未取得可确认的操作结果' })
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
  for (const group of payload as AdminGroupHealth[]) {
    if (group.modelControlError !== undefined && typeof group.modelControlError !== 'string') throw new Error('admin.connectionHealth.errors.request')
    for (const account of group.accounts) {
      if (account.modelControl !== undefined && !validModelControlSummary(account.modelControl)) throw new Error('admin.connectionHealth.errors.request')
      if ((account.modelControlNoActiveProbe !== undefined && typeof account.modelControlNoActiveProbe !== 'boolean')
        || (account.modelFloorLastSources !== undefined && (!Array.isArray(account.modelFloorLastSources) || account.modelFloorLastSources.some(source => !source || typeof source.groupId !== 'string' || typeof source.groupName !== 'string' || typeof source.modelKey !== 'string')))
        || [...(account.modelHealth ?? []), ...(account.unprobedModels ?? [])].some(model => model.modelControlClosed !== undefined && typeof model.modelControlClosed !== 'boolean')) throw new Error('admin.connectionHealth.errors.request')
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
  groupRatesWarning?: import('../types/upstream').GroupRatesWarning
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

export const getQuestionAnswerQueue = (workspace: string, signal?: AbortSignal): Promise<QuestionAnswerQueue> => (
  requestJson<QuestionAnswerQueue>(`/connection-health/question-answer-queue?workspace=${encodeURIComponent(workspace)}`, { headers: questionAnswerContractHeaders, signal })
)

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

export const getQuestionAnswerRecentSummaries = async (targetIds: string[], signal?: AbortSignal): Promise<{ items: QuestionAnswerRecentSummaryItem[]; modelControlError?: string }> => {
  const ids = [...new Set(targetIds)]
  const query = new URLSearchParams()
  for (const id of ids) query.append('targetId', id)
  const response = await requestJson<{ items: QuestionAnswerRecentSummaryItem[]; modelControlError?: string }>(`/connection-health/question-answer-recent-summaries?${query}`, { signal })
  if ((response.modelControlError !== undefined && typeof response.modelControlError !== 'string') || !Array.isArray(response.items) || response.items.length !== ids.length || response.items.some((item, index) =>
    item.targetId !== ids[index] || !validModelControlSummary(item.modelControl) || typeof item.activeNewerBatch !== 'boolean' || (item.recentQuestionAnswer !== null && (!item.recentQuestionAnswer?.batchId || !item.recentQuestionAnswer.requests || !item.recentQuestionAnswer.reviews)))) {
    throw new Error('admin.connectionHealth.errors.request')
  }
  return { modelControlError: response.modelControlError, items: response.items.map(item => ({ ...item, modelControl: item.modelControl === null ? null : { ...item.modelControl }, recentQuestionAnswer: item.recentQuestionAnswer === null ? null : {
    ...item.recentQuestionAnswer, requests: { ...item.recentQuestionAnswer.requests }, reviews: { ...item.recentQuestionAnswer.reviews },
  } })) }
}

const schedulePath = (id: string): string => `/connection-health/question-answer-schedules/${encodeURIComponent(id)}`
const executionPath = (id: string): string => `/connection-health/question-answer-schedule-executions/${encodeURIComponent(id)}`
export const getQuestionAnswerRuntimeSettings = (signal?: AbortSignal): Promise<QuestionAnswerRuntimeSettings> =>
  requestJson('/connection-health/question-answer-runtime-settings', { signal })
export const saveQuestionAnswerRuntimeSettings = (questionAnswerConcurrency: number, expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerRuntimeSettings> =>
  requestJson('/connection-health/question-answer-runtime-settings', { method: 'PUT', body: JSON.stringify({ questionAnswerConcurrency, expectedVersion }), signal })
export const getQuestionAnswerScheduleLimits = (signal?: AbortSignal): Promise<QuestionAnswerScheduleLimits> =>
  requestJson('/connection-health/question-answer-schedule-limits', { signal })
export const saveQuestionAnswerScheduleLimits = (limits: Omit<QuestionAnswerScheduleLimits, 'todayReservedRequests' | 'version' | 'updatedAt'>, expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerScheduleLimits> =>
  requestJson('/connection-health/question-answer-schedule-limits', { method: 'PUT', body: JSON.stringify({ ...limits, expectedVersion }), signal })
export const listQuestionAnswerSchedules = (page = 1, status: 'active' | 'deleted' = 'active', signal?: AbortSignal): Promise<QuestionAnswerSchedulePage<QuestionAnswerSchedule>> =>
  requestJson(`/connection-health/question-answer-schedules?${new URLSearchParams({ page: String(page), pageSize: '20', status })}`, { signal })
export const getQuestionAnswerSchedule = (id: string, signal?: AbortSignal): Promise<QuestionAnswerSchedule> => requestJson(schedulePath(id), { signal })
export const previewQuestionAnswerSchedule = (input: QuestionAnswerSchedulePreviewInput, signal?: AbortSignal): Promise<QuestionAnswerSchedulePreview> =>
  requestJson('/connection-health/question-answer-schedules/preview', { method: 'POST', body: JSON.stringify(input), signal })
export const createQuestionAnswerSchedule = (input: QuestionAnswerScheduleInput, signal?: AbortSignal): Promise<QuestionAnswerSchedule> =>
  requestJson('/connection-health/question-answer-schedules', { method: 'POST', body: JSON.stringify(input), signal })
export const updateQuestionAnswerSchedule = (id: string, input: Omit<QuestionAnswerScheduleInput, 'enabled'>, expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerSchedule> =>
  requestJson(schedulePath(id), { method: 'PUT', body: JSON.stringify({ ...input, expectedVersion }), signal })
export const setQuestionAnswerScheduleState = (id: string, operation: 'enable' | 'disable' | 'revalidate', expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerSchedule> =>
  requestJson(`${schedulePath(id)}/${operation}`, { method: 'POST', body: JSON.stringify({ expectedVersion }), signal })
export const deleteQuestionAnswerSchedule = (id: string, expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerSchedule> =>
  requestJson(`${schedulePath(id)}?${new URLSearchParams({ expectedVersion: String(expectedVersion) })}`, { method: 'DELETE', signal })
export const runQuestionAnswerSchedule = (id: string, requestId: string, signal?: AbortSignal): Promise<QuestionAnswerScheduleExecution> =>
  requestJson(`${schedulePath(id)}/run`, { method: 'POST', body: JSON.stringify({ requestId }), signal })
export const listQuestionAnswerScheduleExecutions = (id: string, page = 1, signal?: AbortSignal): Promise<QuestionAnswerSchedulePage<QuestionAnswerScheduleExecution>> =>
  requestJson(`${schedulePath(id)}/executions?${new URLSearchParams({ page: String(page), pageSize: '20' })}`, { signal })
export const getQuestionAnswerScheduleExecution = (id: string, signal?: AbortSignal): Promise<QuestionAnswerScheduleExecutionDetail> => requestJson(executionPath(id), { signal })
export const cancelQuestionAnswerScheduleExecution = (id: string, expectedVersion: number, signal?: AbortSignal): Promise<QuestionAnswerScheduleExecution> =>
  requestJson(`${executionPath(id)}/cancel`, { method: 'POST', body: JSON.stringify({ expectedVersion }), signal })

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


const validModelControlSummary = (value: unknown): value is ModelControlAccountSummary | null => {
  if (value === null) return true
  if (!value || typeof value !== 'object') return false
  const summary = value as Record<string, unknown>
  return ['open', 'closed', 'attention'].every(key => Number.isInteger(summary[key]) && Number(summary[key]) >= 0)
    && Array.isArray(summary.models) && summary.models.every(model => model && typeof model === 'object'
      && typeof model.modelName === 'string' && ['open', 'closed', 'unknown', 'not_provided', 'account_missing'].includes(model.status)
      && typeof model.attention === 'boolean' && ['no_evidence', 'testing', 'awaiting_review', 'insufficient', 'close_recommended', 'usable'].includes(model.decision)
      && (model.checkedAt === null || (typeof model.checkedAt === 'string' && Number.isFinite(Date.parse(model.checkedAt)))))
}
const modelControlDate = (value: unknown): boolean => value === null || (typeof value === 'string' && value.trim() !== '' && Number.isFinite(Date.parse(value)))
export const isModelControlAutoSettings = (value: unknown): value is ModelControlAutoSettings => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const v = value as ModelControlAutoSettings
  return typeof v.autoCloseEnabled === 'boolean' && typeof v.autoRestoreEnabled === 'boolean' && typeof v.healthCoordinationEnabled === 'boolean'
    && modelControlDate(v.autoCloseEnabledAt) && modelControlDate(v.autoRestoreEnabledAt)
    && Number.isInteger(v.verifyIntervalMinutes) && v.verifyIntervalMinutes >= 5 && v.verifyIntervalMinutes <= 60
    && Number.isInteger(v.retryIntervalMinutes) && v.retryIntervalMinutes >= 1 && v.retryIntervalMinutes <= 60
    && Number.isInteger(v.version) && v.version >= 0 && modelControlDate(v.lastScanStartedAt) && modelControlDate(v.lastScanFinishedAt) && typeof v.lastScanErrorKey === 'string'
}
const parseModelControlAutoSettings = (value: unknown): ModelControlAutoSettings => {
  if (!isModelControlAutoSettings(value)) throw new Error('admin.connectionHealth.errors.request')
  return value
}
const modelControlObject = (value: unknown): value is Record<string, any> => Boolean(value && typeof value === 'object' && !Array.isArray(value))
const modelControlCount = (value: unknown): boolean => Number.isSafeInteger(value) && Number(value) >= 0
const modelControlScore = (value: unknown): boolean => value === null || (typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 100)
const modelControlInvalid = (reason: string): never => { throw new Error(reason) }
const parseModelControlRound = (value: unknown): void => {
  if (value === null) return
  if (!modelControlObject(value) || !Array.isArray(value.questions) || value.questions.length === 0 || !Array.isArray(value.duplicateQuestionIds)) modelControlInvalid('缺少逐题统计')
  const round = value as Record<string, any>, ids = new Set<string>()
  for (const question of round.questions) {
    if (!modelControlObject(question) || typeof question.questionId !== 'string' || !question.questionId || typeof question.questionName !== 'string') modelControlInvalid('缺少逐题统计')
    if (ids.has(question.questionId)) modelControlInvalid('重复题目编号')
    ids.add(question.questionId)
    if (!['correct', 'incorrect', 'unreviewed', 'failed', 'cancelled'].every(key => modelControlCount(question[key]))) modelControlInvalid('非法回答数量')
    if (!modelControlScore(question.accuracyPercent)) modelControlInvalid('非法正确率')
  }
  if (!['correct', 'incorrect', 'unreviewed', 'failed', 'cancelled'].every(key => modelControlCount(round[key]))) modelControlInvalid('非法回答数量')
  if (!modelControlScore(round.accuracyPercent)) modelControlInvalid('非法正确率')
  if (!round.duplicateQuestionIds.every((id: unknown) => typeof id === 'string' && ids.has(id)) || new Set(round.duplicateQuestionIds).size !== round.duplicateQuestionIds.length) modelControlInvalid('重复题目编号')
  if (typeof round.batchId !== 'string' || !round.batchId || !['manual', 'scheduled', 'run_now'].includes(round.source) || (round.scheduleName !== null && typeof round.scheduleName !== 'string') || !modelControlDate(round.createdAt) || round.createdAt === null || !modelControlDate(round.completedAt) || typeof round.running !== 'boolean') modelControlInvalid('缺少逐题统计')
}
const modelControlEntries = (value: unknown): boolean => Array.isArray(value) && value.every(entry => modelControlObject(entry) && typeof entry.key === 'string' && typeof entry.value === 'string' && typeof entry.state === 'string')
const modelControlSources = (value: unknown): boolean => Array.isArray(value) && value.every(source => modelControlObject(source) && typeof source.groupId === 'string' && typeof source.groupName === 'string' && typeof source.key === 'string' && (source.count === null || modelControlCount(source.count)) && (source.ok === undefined || typeof source.ok === 'boolean'))
const modelControlMapping = (value: unknown): boolean => modelControlObject(value) && Object.values(value).every(entry => typeof entry === 'string')
export const parseModelControlItem = (value: unknown, targetId?: string, modelName?: string): ModelControlItem => {
  if (!modelControlObject(value) || typeof value.targetId !== 'string' || typeof value.modelName !== 'string' || !value.targetId || !value.modelName || !modelControlCount(value.version)) modelControlInvalid('对象不匹配')
  const item = value as ModelControlItem
  if ((targetId !== undefined && item.targetId !== targetId) || (modelName !== undefined && item.modelName !== modelName)) modelControlInvalid('对象不匹配')
  parseModelControlRound(item.round); parseModelControlRound(item.previousRound)
  if (!['no_evidence', 'testing', 'awaiting_review', 'insufficient', 'close_recommended', 'usable'].includes(item.decision) || typeof item.decisionReason !== 'string' || typeof item.attention !== 'boolean' || typeof item.accountName !== 'string') modelControlInvalid('对象不匹配')
  const { rule, basis, control, coverage, health } = item
  if (!modelControlObject(rule) || !modelControlCount(rule.version) || rule.modelName !== item.modelName || !Number.isInteger(rule.minAccuracyPercent) || rule.minAccuracyPercent < 1 || rule.minAccuracyPercent > 100 || !Number.isInteger(rule.minJudgedAnswers) || rule.minJudgedAnswers < 1 || rule.minJudgedAnswers > 50 || typeof rule.includeManual !== 'boolean' || typeof rule.includeScheduled !== 'boolean') modelControlInvalid('对象不匹配')
  if (!modelControlObject(basis) || typeof basis.batchId !== 'string' || typeof basis.businessDay !== 'string' || !modelControlCount(basis.ruleVersion) || basis.ruleVersion !== rule.version || basis.decision !== item.decision) modelControlInvalid('对象不匹配')
  if (!modelControlObject(control) || !modelControlMapping(control.closedEntries) || !modelControlDate(control.closedAt) || !modelControlScore(control.closedAccuracyPercent) || typeof control.conflictReason !== 'string' || !modelControlObject(control.observation)) modelControlInvalid('对象不匹配')
  const observation = control.observation
  if (!['unverified', 'serving', 'partially_closed', 'closed', 'not_provided', 'not_isolatable', 'last_model', 'account_missing'].includes(observation.state) || typeof observation.reasonKey !== 'string' || !modelControlSources(observation.sources) || typeof observation.accountStatus !== 'string' || (observation.accountSchedulable !== null && typeof observation.accountSchedulable !== 'boolean') || !modelControlDate(observation.checkedAt)) modelControlInvalid('对象不匹配')
  if (control.pending !== null && (!modelControlObject(control.pending) || !['close', 'restore', 'add', 'close_account'].includes(control.pending.operation) || !['phase', 'receipt', 'state'].every(key => typeof (control.pending as any)[key] === 'string') || !modelControlDate(control.pending.startedAt) || !modelControlDate(control.pending.sendStartedAt))) modelControlInvalid('对象不匹配')
  if (control.accountPending !== null && (!modelControlObject(control.accountPending) || typeof control.accountPending.modelName !== 'string' || typeof control.accountPending.operation !== 'string' || typeof control.accountPending.state !== 'string')) modelControlInvalid('对象不匹配')
  if (control.unconfirmedClose !== null && (!modelControlObject(control.unconfirmedClose) || !modelControlMapping(control.unconfirmedClose.entries) || !modelControlDate(control.unconfirmedClose.sentAt) || !modelControlDate(control.unconfirmedClose.expiresAt))) modelControlInvalid('对象不匹配')
  if (control.lastAttempt !== null && (!modelControlObject(control.lastAttempt) || !modelControlEntries(control.lastAttempt.entries) || !modelControlSources(control.lastAttempt.groups) || !modelControlDate(control.lastAttempt.at) || !['operation', 'outcome', 'reasonKey'].every(key => typeof (control.lastAttempt as any)[key] === 'string'))) modelControlInvalid('对象不匹配')
  if (!modelControlObject(coverage) || !Array.isArray(coverage.schedules) || !coverage.schedules.every(schedule => modelControlObject(schedule) && typeof schedule.id === 'string' && typeof schedule.name === 'string') || !modelControlObject(health) || typeof health.recentlyProbed !== 'boolean' || (health.state !== null && typeof health.state !== 'string')) modelControlInvalid('对象不匹配')
  if (item.healthCoordination !== undefined && typeof item.healthCoordination !== 'boolean') modelControlInvalid('对象不匹配')
  if (item.auto !== undefined) {
    const v = item.auto
    if (!v || !['off', 'none', 'busy', 'manual', 'done', 'waiting', 'retry', 'eligible'].includes(v.state) || !['', 'close', 'restore'].includes(v.operation) || !modelControlDate(v.nextAt) || typeof v.reasonKey !== 'string' || typeof v.changedAt !== 'string' || !modelControlDate(v.changedAt)) modelControlInvalid('对象不匹配')
  }
  return item
}
const parseModelControlItems = <T extends { items: ModelControlItem[] }>(value: T, targetId?: string): T => {
  if (!modelControlObject(value) || !Array.isArray(value.items)) modelControlInvalid('缺少逐题统计')
  value.items.forEach(item => parseModelControlItem(item, targetId))
  return value
}
const parseModelControlPreview = (value: unknown, targetId?: string, modelName?: string): ModelControlPreview => {
  if (!modelControlObject(value)) modelControlInvalid('无效预览结构')
  const preview = value as ModelControlPreview
  parseModelControlItem(preview.item, targetId, modelName)
  if (!modelControlEntries(preview.entries) || !modelControlSources(preview.groups) || typeof preview.blockReasonKey !== 'string' || typeof preview.accountStatus !== 'string' || (preview.accountSchedulable !== null && typeof preview.accountSchedulable !== 'boolean') || (preview.planFingerprint !== null && typeof preview.planFingerprint !== 'string') || (preview.noRemoteWrite !== undefined && typeof preview.noRemoteWrite !== 'boolean') || !modelControlObject(preview.requestHealth) || typeof preview.requestHealth.checked !== 'boolean' || (preview.requestHealth.allowed !== null && typeof preview.requestHealth.allowed !== 'boolean') || typeof preview.requestHealth.reasonKey !== 'string') modelControlInvalid('无效预览结构')
  return preview
}
export const parseModelControlConflictCurrent = (value: unknown, targetId?: string, modelName?: string): ModelControlItem | ModelControlPreview => {
  if (modelControlObject(value) && 'item' in value) return parseModelControlPreview(value, targetId, modelName)
  return parseModelControlItem(value, targetId, modelName)
}
type ModelControlRequestContext = { stage: 'read' | 'manage' | 'preview' | 'execute'; targetId?: string; modelName?: string; targetIds?: string[] }
const modelControlItemRequest = async <T>(path: string, options: RequestInit, parse: (value: T) => T, context: ModelControlRequestContext): Promise<T> => {
  try { return parse(await requestJson<T>(path, options)) }
  catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    if (cause instanceof ConnectionHealthApiError) {
      if (cause.status !== 409 || cause.current === undefined || cause.current === null) throw cause
      try { const current = parseModelControlConflictCurrent(cause.current, context.targetId, context.modelName); if (context.targetIds && !context.targetIds.includes('item' in current ? current.item.targetId : current.targetId)) modelControlInvalid('对象不匹配'); throw new ConnectionHealthApiError(cause.message, cause.status, current, cause.activeExecutionId) }
      catch (invalid) { if (invalid instanceof ConnectionHealthApiError) throw invalid; cause = invalid }
    }
    const reason = cause instanceof SyntaxError ? '返回内容不是可读取的数据' : cause instanceof TypeError ? '返回结构不完整' : cause instanceof Error && !cause.message.startsWith('admin.') ? cause.message : '连接中断或返回内容无法读取'
    const model = context.modelName ?? '模型管理'
    if (context.stage === 'execute') throw new Error(`${model}的操作请求已发送，但返回内容无法读取：${reason}。暂时无法确认主站是否修改，请点“重新读取主站结果”，不要直接重试。`)
    if (context.stage === 'manage') throw new Error(`读取${model}的管理名单时发现${reason}，请重新读取本地管理名单后再处理。`)
    throw new Error(`读取${model}测试结果时发现${reason}，本次尚未发送模型开关。请刷新后重新预览；仍出现时检查前后端是否为同一版本。`)
  }
}
const modelControlPath = '/connection-health/model-control'
export const getModelControlAutoSettings = async (signal?: AbortSignal): Promise<ModelControlAutoSettings> => parseModelControlAutoSettings(await requestJson<unknown>(`${modelControlPath}/auto-settings`, { signal }))
export const saveModelControlAutoSettings = async (settings: ModelControlAutoSettings, expectedVersion: number, signal?: AbortSignal): Promise<ModelControlAutoSettings> => parseModelControlAutoSettings(await requestJson<unknown>(`${modelControlPath}/auto-settings`, { method: 'PUT', body: JSON.stringify({ autoCloseEnabled: settings.autoCloseEnabled, autoRestoreEnabled: settings.autoRestoreEnabled, healthCoordinationEnabled: settings.healthCoordinationEnabled, verifyIntervalMinutes: settings.verifyIntervalMinutes, retryIntervalMinutes: settings.retryIntervalMinutes, expectedVersion }), signal }))
export const getModelControlSettings = (signal?: AbortSignal): Promise<ModelControlSettings> => requestJson(`${modelControlPath}/settings`, { signal })
export const saveModelControlSettings = (settings: Omit<ModelControlSettings, 'version'>, expectedVersion: number, signal?: AbortSignal): Promise<ModelControlSettings> => requestJson(`${modelControlPath}/settings`, { method: 'PUT', body: JSON.stringify({ minAccuracyPercent: settings.minAccuracyPercent, minJudgedAnswers: settings.minJudgedAnswers, expectedVersion }), signal })
export const getModelControlTarget = (targetId: string, signal?: AbortSignal): Promise<{ targetId: string; items: ModelControlItem[]; candidates: string[] }> => modelControlItemRequest(`${modelControlPath}/targets/${encodeURIComponent(targetId)}`, { signal }, value => parseModelControlItems(value, targetId), { stage: 'read', targetId })
export const listModelControlItems = (view: 'attention' | 'all', modelName: string, page: number, signal?: AbortSignal): Promise<ModelControlPage<ModelControlItem> & { counts: ModelControlCounts }> => modelControlItemRequest(`${modelControlPath}/items?${new URLSearchParams({ view, modelName, page: String(page) })}`, { signal }, parseModelControlItems, { stage: 'read' })
export const addModelControlManaged = (targetId: string, modelName: string, signal?: AbortSignal): Promise<ModelControlItem> => modelControlItemRequest(`${modelControlPath}/managed`, { method: 'POST', body: JSON.stringify({ targetId, modelName }), signal }, value => parseModelControlItem(value, targetId, modelName), { stage: 'manage', targetId, modelName })
export const removeModelControlManaged = (targetId: string, modelName: string, expectedVersion: number, abandonClosed: boolean, signal?: AbortSignal): Promise<void> => modelControlItemRequest(`${modelControlPath}/managed`, { method: 'DELETE', body: JSON.stringify({ targetId, modelName, expectedVersion, abandonClosed }), signal }, value => value, { stage: 'manage', targetId, modelName })
export const previewModelControl = (targetId: string, modelName: string, operation: ModelControlOperation, basis: ModelControlBasis, signal?: AbortSignal): Promise<ModelControlPreview> => modelControlItemRequest(`${modelControlPath}/preview`, { method: 'POST', body: JSON.stringify({ targetId, modelName, operation, basis }), signal }, value => parseModelControlPreview(value, targetId, modelName), { stage: 'preview', targetId, modelName })
export const executeModelControl = (targetId: string, modelName: string, operation: ModelControlOperation, basis: ModelControlBasis, planFingerprint: string, confirmWithoutEvidence: boolean, signal?: AbortSignal): Promise<ModelControlResult> => modelControlItemRequest(`${modelControlPath}/${operation === 'close_account' ? 'close-account' : operation}`, { method: 'POST', body: JSON.stringify({ targetId, modelName, basis, planFingerprint, ...(operation !== 'close_account' ? { confirmWithoutEvidence } : {}) }), signal }, value => { if (!modelControlObject(value)) modelControlInvalid('对象不匹配'); parseModelControlItem(value.item, targetId, modelName); if (!['closed', 'restored', 'added', 'partial', 'failed', 'blocked', 'unknown'].includes(value.outcome ?? '') || typeof value.reasonKey !== 'string' || !modelControlEntries(value.entries) || !modelControlSources(value.groups) || !Array.isArray(value.hintKeys) || !value.hintKeys.every(hint => typeof hint === 'string') || (value.errorKey !== undefined && typeof value.errorKey !== 'string')) modelControlInvalid('无效执行结果'); return value }, { stage: 'execute', targetId, modelName })
export const getModelControlVerifyTargets = (signal?: AbortSignal): Promise<{ targetIds: string[] }> => requestJson(`${modelControlPath}/verify-targets`, { signal })
export const verifyModelControl = (targetIds: string[], signal?: AbortSignal): Promise<{ items: ModelControlItem[]; errors: Array<{ targetId: string; reasonKey: string }> }> => modelControlItemRequest(`${modelControlPath}/verify`, { method: 'POST', body: JSON.stringify({ targetIds }), signal }, value => { parseModelControlItems(value); if (value.items.some(item => !targetIds.includes(item.targetId)) || !Array.isArray(value.errors) || !value.errors.every(error => modelControlObject(error) && targetIds.includes(error.targetId) && typeof error.reasonKey === 'string')) modelControlInvalid('对象不匹配'); return value }, { stage: 'read', targetIds })
export const listModelControlEvents = async (targetId: string, modelName: string, page: number, signal?: AbortSignal): Promise<ModelControlPage<ModelControlEvent>> => { const value = await requestJson<ModelControlPage<ModelControlEvent>>(`${modelControlPath}/events?${new URLSearchParams({ targetId, modelName, page: String(page) })}`, { signal }); if (value.items.some(event => event.actorUserId !== undefined && typeof event.actorUserId !== 'string')) throw new Error('admin.connectionHealth.errors.request'); return value }

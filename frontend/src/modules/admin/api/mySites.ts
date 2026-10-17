import { t, te } from '@/locales'
import { safeScopedErrorText } from '../utils/scopedErrorText'
import type {
  OwnGroupRatesResponse,
  RunAutoPricingRequest,
  RunAutoPricingResponse,
  MySiteMapping,
  MySiteMappingOptionsResponse,
  MySiteStatus,
  RealBindRequest,
  RealConnectRequest,
  RealConnectResponse,
  RealConnection,
  RealConnectionCheckResponse,
  RealDisconnectRequest,
  UpstreamKeyItem,
  AdminResourceOption,
  ImportFailure,
  ImportConfiguration,
  ImportDiagnostic,
  ConnectionsNeedingAttentionResponse,
  ConnectionMainSiteGroups,
  ChangeConnectionMainSiteGroupsRequest,
  SyncConnectionHubGroupsRequest,
  MainSiteGroupMutationResult,
} from '../types/mySites'
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

type AdminErrorPayload = {
  message?: string
  detail?: string
  next?: string
  error?: string
  reason?: string
  groupId?: string
  groupName?: string
  adminResourceId?: string
  upstreamKeyId?: string
}

type MySiteMappingRequest = Omit<MySiteMapping, 'lastAutoPricingRun'>

const toMappingRequest = (mapping: MySiteMapping): MySiteMappingRequest => ({
  ownGroup: mapping.ownGroup,
  upstreamTargets: mapping.upstreamTargets.map(target => ({
    siteId: target.siteId,
    groupName: target.groupName,
  })),
  enableAutoPricing: mapping.enableAutoPricing,
  autoPricingSource: mapping.autoPricingSource,
  primaryUpstreamSiteId: mapping.primaryUpstreamSiteId,
  primaryUpstreamGroupName: mapping.primaryUpstreamGroupName,
  autoPricingStrategy: mapping.autoPricingStrategy,
  fixedIncrease: mapping.fixedIncrease,
  percentageIncrease: mapping.percentageIncrease,
  adjustThresholdPercent: mapping.adjustThresholdPercent,
  minMultiplier: mapping.minMultiplier,
  maxMultiplier: mapping.maxMultiplier,
  enableAutoPricingNotify: mapping.enableAutoPricingNotify,
  autoPricingNotifyBotIds: mapping.autoPricingNotifyBotIds,
  autoPricingNotifyTemplate: mapping.autoPricingNotifyTemplate,
})

const normalizeMappings = (value: unknown): MySiteMapping[] => {
  if (!Array.isArray(value)) return []
  return value.flatMap((entry) => {
    if (entry == null || typeof entry !== 'object') return []
    const mapping = entry as MySiteMapping
    if (typeof mapping.ownGroup !== 'string' || !mapping.ownGroup.trim()) return []
    const upstreamTargets = Array.isArray(mapping.upstreamTargets)
      ? mapping.upstreamTargets.filter(target => (
          target != null &&
          typeof target.siteId === 'string' &&
          typeof target.groupName === 'string'
        ))
      : []
    return [{ ...mapping, upstreamTargets }]
  })
}

const normalizeStatus = (status: MySiteStatus): MySiteStatus => ({
  ...status,
  ...(Object.prototype.hasOwnProperty.call(status, 'mappings')
    ? { mappings: normalizeMappings(status.mappings) }
    : {}),
})

const normalizeMappingOptions = (response: MySiteMappingOptionsResponse): MySiteMappingOptionsResponse => ({
  ...response,
  ownGroups: Array.isArray(response.ownGroups) ? response.ownGroups : [],
  mappings: normalizeMappings(response.mappings),
  staleOwnGroups: Array.isArray(response.staleOwnGroups) ? response.staleOwnGroups : [],
  staleTargets: Array.isArray(response.staleTargets) ? response.staleTargets : [],
})

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
    throw new Error('admin.mySites.errors.network')
  }

  const text = await response.text()
  let payload = {} as T & AdminErrorPayload
  if (text) {
    try {
      payload = JSON.parse(text) as T & AdminErrorPayload
    } catch {
      payload = {} as T & AdminErrorPayload
    }
  }

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }

    const error = new Error(payload.message ?? payload.error ?? 'admin.mySites.errors.request')
    if (['admin.mySites.errors.resourcesPendingVerification', 'admin.mySites.errors.accountCreationPendingVerification', 'admin.mySites.errors.compensationPendingVerification', 'admin.mySites.errors.upstreamKeyCleanupPendingVerification'].includes(error.message)) {
      Object.assign(error, {
        reason: typeof payload.reason === 'string' ? payload.reason : undefined,
        groupId: typeof payload.groupId === 'string' ? payload.groupId : undefined,
        groupName: typeof payload.groupName === 'string' ? payload.groupName : undefined,
        adminResourceId: typeof payload.adminResourceId === 'string' ? payload.adminResourceId : undefined,
        upstreamKeyId: typeof payload.upstreamKeyId === 'string' ? payload.upstreamKeyId : undefined,
      })
    }
    throw error
  }

  return payload
}

const importMessageKeys = new Set([
  'admin.mySites.errors.request', 'admin.mySites.errors.connectionExists',
  'admin.mySites.errors.authRequired', 'admin.mySites.errors.adminOnly', 'admin.mySites.errors.unknown', 'auth.errors.unauthorized',
  'admin.mySites.errors.importModelSyncWarning', 'admin.mySites.errors.importSessionRefreshFailed',
  'admin.mySites.errors.importAdminVerificationFailed', 'admin.mySites.errors.importSessionSaveFailed',
  'admin.mySites.errors.importInProgress', 'admin.mySites.errors.importSourceGroupUnavailable', 'admin.mySites.errors.importGroupsLoadFailed',
  'admin.mySites.errors.importSettingsInvalid', 'admin.mySites.errors.importGroupTypeMismatch',
  'admin.mySites.errors.importSettingsUnsupported', 'admin.mySites.errors.importUpstreamKeyFailed',
  'admin.mySites.errors.importModelSyncFailed', 'admin.mySites.errors.importAccountCreateFailed',
  'admin.mySites.errors.importConfigurationUnavailable', 'admin.mySites.errors.importConfigurationMismatch',
  'admin.mySites.errors.importPersistenceFailed', 'admin.mySites.errors.importPersistencePending',
  'admin.mySites.errors.accountCreationPendingVerification', 'admin.mySites.errors.compensationPendingVerification',
  'admin.mySites.errors.upstreamKeyCleanupPendingVerification', 'admin.mySites.errors.resourcesPendingVerification',
  'admin.mySites.errors.safeDeletionUnavailable',
  'admin.adminAccounts.errors.noCurrentAccount',
])
const importReasonKeys = new Set([
  ...importMessageKeys,
  'admin.mySites.errors.importCancelled', 'admin.mySites.errors.importModelListEmpty', 'admin.mySites.errors.importModelListInvalid',
  'admin.connectionHealth.errors.sub2apiGroupLastModelSource',
  'admin.connectionHealth.errors.sub2apiGroupLastUsable',
  'admin.connectionHealth.errors.sub2apiInventoryIncomplete',
  'admin.connectionHealth.errors.remoteActionPending',
  'admin.connectionHealth.testConfiguration.remoteActionPending',
  ...['auth', 'forbidden', 'notFound', 'rateLimited', 'upstreamServerError', 'networkTimeout', 'networkUnreachable', 'tlsFailed', 'invalidResponse', 'businessRejected', 'refreshTokenRejected', 'accessTokenRejected', 'loginIncomplete', 'announcementAckRequired', 'upstreamInsufficientBalance', 'upstreamKeyQuotaExhausted', 'upstreamKeyExpired'].map(key => `admin.upstream.errors.${key}`),
])
const importStages = new Set(['validation', 'upstream_key', 'model_sync', 'account_create', 'configuration_check', 'persistence'])
const importCleanups = new Set(['not_needed', 'confirmed', 'retained', 'pending'])
const isObject = (value: unknown): value is Record<string, unknown> => value != null && typeof value === 'object' && !Array.isArray(value)
const safeText = (value: unknown): value is string => typeof value === 'string' && value.trim().length > 0 && value.length <= 512 && !/[\u0000-\u001f\u007f]/.test(value)
const positiveResourceId = (value: unknown): value is string => typeof value === 'string' && /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value))
const integerInRange = (value: unknown, minimum: number, maximum: number): value is number => typeof value === 'number' && Number.isInteger(value) && value >= minimum && value <= maximum

const diagnosticStages = new Set([...importStages, 'account_cleanup', 'key_cleanup'])
const parseImportDiagnostics = (value: unknown): { diagnostics?: ImportDiagnostic[]; diagnosticsUnavailable?: boolean } => {
  if (value === undefined) return {}
  if (!Array.isArray(value) || value.length > 3) return { diagnosticsUnavailable: true }
  const diagnostics: ImportDiagnostic[] = []
  let unavailable = false
  for (const item of value) {
    if (!isObject(item) || typeof item.stage !== 'string' || !diagnosticStages.has(item.stage) ||
      typeof item.target !== 'string' || !['main', 'upstream', 'local'].includes(item.target) || typeof item.message !== 'string' || !importMessageKeys.has(item.message) ||
      (item.reason !== undefined && (typeof item.reason !== 'string' || !importReasonKeys.has(item.reason))) ||
      (item.httpStatus !== undefined && !integerInRange(item.httpStatus, 100, 599)) ||
      (item.detail !== undefined && !safeText(item.detail))) {
      unavailable = true
      continue
    }
    diagnostics.push({ stage: item.stage as ImportDiagnostic['stage'], target: item.target as ImportDiagnostic['target'], message: item.message,
      ...(typeof item.reason === 'string' ? { reason: item.reason } : {}),
      ...(typeof item.httpStatus === 'number' ? { httpStatus: item.httpStatus } : {}),
      ...(typeof item.detail === 'string' ? { detail: item.detail } : {}),
    })
  }
  return { diagnostics, ...(unavailable ? { diagnosticsUnavailable: true } : {}) }
}

export class ImportApiError extends Error implements ImportFailure {
  readonly stage: ImportFailure['stage']
  readonly cleanup: ImportFailure['cleanup']
  readonly retryAllowed: boolean
  readonly trusted: boolean
  readonly adminResourceId?: string
  readonly upstreamKeyId?: string
  readonly upstreamResourceName?: string
  readonly reason?: string
  readonly groupId?: string
  readonly groupName?: string
  readonly diagnostics?: ImportDiagnostic[]
  readonly diagnosticsUnavailable?: boolean

  constructor(failure: ImportFailure, trusted: boolean) {
    super(failure.message)
    this.name = 'ImportApiError'
    this.stage = failure.stage
    this.cleanup = failure.cleanup
    this.retryAllowed = failure.retryAllowed
    this.trusted = trusted
    this.adminResourceId = failure.adminResourceId
    this.upstreamKeyId = failure.upstreamKeyId
    this.upstreamResourceName = failure.upstreamResourceName
    this.reason = failure.reason
    this.groupId = failure.groupId
    this.groupName = failure.groupName
    this.diagnostics = failure.diagnostics
    this.diagnosticsUnavailable = failure.diagnosticsUnavailable
  }
}

const pendingImportError = (message = 'admin.mySites.errors.importResponsePending'): ImportApiError => new ImportApiError({
  message, stage: 'account_create', cleanup: 'pending', retryAllowed: false,
}, false)

const parseImportFailure = (payload: unknown, status: number): ImportApiError | null => {
  if (!isObject(payload) || typeof payload.message !== 'string' || !importMessageKeys.has(payload.message) ||
    typeof payload.stage !== 'string' || !importStages.has(payload.stage) ||
    typeof payload.cleanup !== 'string' || !importCleanups.has(payload.cleanup) || typeof payload.retryAllowed !== 'boolean') return null
  for (const key of ['adminResourceId', 'upstreamKeyId'] as const) {
    if (payload[key] != null && payload[key] !== '' && !positiveResourceId(payload[key])) return null
  }
  if (payload.upstreamResourceName != null && payload.upstreamResourceName !== '' && !safeText(payload.upstreamResourceName)) return null
  if (payload.cleanup === 'not_needed' && Boolean(payload.adminResourceId || payload.upstreamKeyId)) return null
  if (payload.retryAllowed && (status === 409 || !['not_needed', 'confirmed'].includes(payload.cleanup) ||
    (payload.cleanup === 'not_needed' && Boolean(payload.adminResourceId || payload.upstreamKeyId)))) return null
  return new ImportApiError({
    message: payload.message,
    stage: payload.stage as ImportFailure['stage'],
    cleanup: payload.cleanup as ImportFailure['cleanup'],
    retryAllowed: payload.retryAllowed,
    adminResourceId: positiveResourceId(payload.adminResourceId) ? payload.adminResourceId : undefined,
    upstreamKeyId: positiveResourceId(payload.upstreamKeyId) ? payload.upstreamKeyId : undefined,
    upstreamResourceName: safeText(payload.upstreamResourceName) ? payload.upstreamResourceName : undefined,
    reason: typeof payload.reason === 'string' && importReasonKeys.has(payload.reason) ? payload.reason : undefined,
    groupId: positiveResourceId(payload.groupId) ? payload.groupId : undefined,
    groupName: safeText(payload.groupName) ? payload.groupName : undefined,
    ...parseImportDiagnostics(payload.diagnostics),
  }, true)
}

const parseImportConfiguration = (value: unknown, connection: RealConnection): ImportConfiguration | null => {
  if (!isObject(value) || typeof value.observation !== 'string' || !['creation', 'current'].includes(value.observation) ||
    value.adminAccountId !== connection.adminAccountId || typeof value.platform !== 'string' || value.platform !== connection.groupType || !safeText(value.name) ||
    typeof value.modelState !== 'string' || !['synced', 'not_synced', 'not_required', 'current_whitelist', 'current_unrestricted'].includes(value.modelState) ||
    !integerInRange(value.priority, value.observation === 'creation' ? 1 : 0, 2147483647) || !integerInRange(value.concurrency, 1, value.observation === 'creation' ? 1000 : 2147483647) ||
    typeof value.passthrough !== 'boolean' || typeof value.poolMode !== 'boolean' || typeof value.upstreamBillingProbeEnabled !== 'boolean' ||
    (value.passthrough && !['openai', 'anthropic'].includes(value.platform)) ||
    !Array.isArray(value.ownGroups) || !Array.isArray(value.models)) return null
  const ownGroups: ImportConfiguration['ownGroups'] = []
  for (const group of value.ownGroups) {
    if (!isObject(group) || !positiveResourceId(group.id) || !safeText(group.name) || ownGroups.some(entry => entry.id === group.id)) return null
    ownGroups.push({ id: group.id, name: group.name })
  }
  const models: string[] = []
  for (const model of value.models) {
    if (!safeText(model) || model !== model.trim() || models.includes(model)) return null
    models.push(model)
  }
  const creation = value.observation === 'creation'
  if (creation && ownGroups.length === 0) return null
  if (value.passthrough) {
    if (value.modelState !== 'not_required' || models.length !== 0) return null
  } else if (creation) {
    if (!(value.modelState === 'synced' && models.length > 0 && !models.some(model => model.includes('*'))) && !(value.modelState === 'not_synced' && models.length === 0)) return null
  } else if (!(value.modelState === 'current_whitelist' && models.length > 0) && !(value.modelState === 'current_unrestricted' && models.length === 0)) return null
  if (creation && (ownGroups.length !== connection.ownGroupIds.length || ownGroups.some(group => !connection.ownGroupIds.includes(group.id)))) return null
  return {
    observation: value.observation as ImportConfiguration['observation'], adminAccountId: value.adminAccountId,
    name: value.name, platform: value.platform, priority: value.priority, concurrency: value.concurrency,
    passthrough: value.passthrough, poolMode: value.poolMode, upstreamBillingProbeEnabled: value.upstreamBillingProbeEnabled,
    ownGroups, modelState: value.modelState as ImportConfiguration['modelState'], models,
  }
}

const parseImportSuccess = (payload: unknown, request: RealConnectRequest, workspaceAdminAccountId: string): RealConnectResponse => {
  if (!isObject(payload) || !safeText(workspaceAdminAccountId) || payload.workspaceAdminAccountId !== workspaceAdminAccountId ||
    typeof payload.configurationStatus !== 'string' || !['confirmed', 'unavailable'].includes(payload.configurationStatus) || !isObject(payload.connection)) throw pendingImportError()
  const source = payload.connection
  if (typeof source.id !== 'string' || !/^[a-f0-9]{32}$/.test(source.id) || !positiveResourceId(source.adminAccountId) || !positiveResourceId(source.upstreamKeyId) ||
    source.upstreamSiteId !== request.upstreamSiteId || source.upstreamGroupId !== request.upstreamGroupId ||
    !safeText(source.upstreamGroupName) || !safeText(source.groupType) || source.groupType !== request.groupType || source.adminPlatform !== 'sub2api' ||
    typeof source.status !== 'string' || !['active', 'missing'].includes(source.status) || !Array.isArray(source.ownGroupIds) ||
    !source.ownGroupIds.every(positiveResourceId) || new Set(source.ownGroupIds).size !== source.ownGroupIds.length ||
    !safeText(source.adminAccountName) || !safeText(source.createdAt)) throw pendingImportError()
  const connection: RealConnection = {
    id: source.id, upstreamSiteId: request.upstreamSiteId, upstreamGroupId: request.upstreamGroupId,
    upstreamGroupName: source.upstreamGroupName, upstreamKeyId: source.upstreamKeyId,
    adminAccountId: source.adminAccountId, adminAccountName: source.adminAccountName, ownGroupIds: source.ownGroupIds,
    groupType: request.groupType, adminPlatform: 'sub2api', status: source.status, createdAt: source.createdAt,
    ...(safeText(source.siteName) ? { siteName: source.siteName } : {}),
    ...(safeText(source.connectionName) ? { connectionName: source.connectionName } : {}),
    ...(safeText(source.keyName) ? { keyName: source.keyName } : {}),
    ...(safeText(source.ownGroupName) ? { ownGroupName: source.ownGroupName } : {}),
    ...(Array.isArray(source.ownGroupNames) && source.ownGroupNames.every(safeText) ? { ownGroupNames: source.ownGroupNames } : {}),
    ...(typeof source.pricingMappingEnabled === 'boolean' ? { pricingMappingEnabled: source.pricingMappingEnabled } : {}),
    ...(typeof source.canDeleteRemote === 'boolean' ? { canDeleteRemote: source.canDeleteRemote } : {}),
    ...(safeText(source.provisioningMode) ? { provisioningMode: source.provisioningMode } : {}),
    ...(safeText(source.upstreamPlatform) ? { upstreamPlatform: source.upstreamPlatform } : {}),
  }
  const diagnosticInfo = parseImportDiagnostics(payload.diagnostics)
  let configuration = payload.configurationStatus === 'confirmed' ? parseImportConfiguration(payload.configuration, connection) : null
  const warnings = diagnosticInfo.diagnostics?.filter(item => item.stage === 'model_sync' || item.message === 'admin.mySites.errors.importModelSyncWarning') ?? []
  if (configuration?.modelState === 'not_synced') {
    if (diagnosticInfo.diagnosticsUnavailable || diagnosticInfo.diagnostics?.length !== 1 || warnings.length !== 1 || warnings[0]?.stage !== 'model_sync' || warnings[0]?.target !== 'main' || warnings[0]?.message !== 'admin.mySites.errors.importModelSyncWarning') configuration = null
  } else if (warnings.length > 0) configuration = null
  if (configuration?.observation === 'creation' && diagnosticInfo.diagnosticsUnavailable) configuration = null
  return {
    connection, workspaceAdminAccountId,
    ...diagnosticInfo,
    configurationStatus: configuration ? 'confirmed' : 'unavailable',
    ...(configuration ? { configuration } : { message: 'admin.mySites.errors.importConfigurationUnavailable' }),
  }
}

const requestImportJson = async (req: RealConnectRequest, workspaceAdminAccountId: string): Promise<RealConnectResponse> => {
  if (!safeText(workspaceAdminAccountId)) throw pendingImportError()
  let response: Response
  try {
    response = await fetch(endpoint('/my-sites/real-connect'), {
      method: 'POST', headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() },
      body: JSON.stringify(req),
    })
  } catch { throw pendingImportError() }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch {
    if (response.status === 401) handleAuthExpired()
    throw pendingImportError()
  }
  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) {
      handleAuthExpired()
      const failure = parseImportFailure(payload, response.status)
      if (failure?.stage === 'validation' && failure.cleanup === 'not_needed') throw failure
      throw pendingImportError(authUnauthorizedErrorKey)
    }
    throw parseImportFailure(payload, response.status) ?? pendingImportError()
  }
  return parseImportSuccess(payload, req, workspaceAdminAccountId)
}

export const getMySiteMappingOptions = async (): Promise<MySiteMappingOptionsResponse> => (
  normalizeMappingOptions(await requestJson<MySiteMappingOptionsResponse>('/my-sites/mapping-options'))
)

export const saveMySiteMappings = async (mappings: MySiteMapping[]): Promise<MySiteStatus> => (
  normalizeStatus(await requestJson<MySiteStatus>('/my-sites/mappings', {
    method: 'PUT',
    body: JSON.stringify({ mappings: mappings.map(toMappingRequest) }),
  }))
)

export const realConnect = async (req: RealConnectRequest, workspaceAdminAccountId = ''): Promise<RealConnectResponse> => (
  req.accountSettings ? requestImportJson(req, workspaceAdminAccountId) : requestJson<RealConnectResponse>('/my-sites/real-connect', {
    method: 'POST',
    body: JSON.stringify(req),
  })
)

export const listRealConnections = async (): Promise<RealConnection[]> =>
  requestJson<RealConnection[]>('/my-sites/real-connections')

export const checkRealConnections = async (): Promise<RealConnectionCheckResponse> =>
  requestJson<RealConnectionCheckResponse>('/my-sites/real-connections/check', { method: 'POST' })

export const listUpstreamKeys = async (siteId: string, groupId: string, groupName: string): Promise<UpstreamKeyItem[]> => {
  const params = new URLSearchParams({ siteId, groupId, groupName })
  const items = await requestJson<UpstreamKeyItem[]>(`/my-sites/upstream-keys?${params.toString()}`)
  return Array.isArray(items)
    ? items.map(item => ({
        ...item,
        // Older backends returned the full key. Keep it only as an internal
        // compatibility fallback; the UI renders the non-secret preview.
        keyPreview: item.keyPreview || (item.key ? `${item.key.slice(0, 6)}...${item.key.slice(-4)}` : ''),
      }))
    : []
}

export const listAdminResources = async (groupId: string): Promise<AdminResourceOption[]> => {
  const items = await requestJson<AdminResourceOption[]>(`/my-sites/admin-resources?groupId=${encodeURIComponent(groupId)}`)
  return Array.isArray(items) ? items : []
}

export const realBind = async (req: RealBindRequest): Promise<RealConnectResponse> => (
  requestJson<RealConnectResponse>('/my-sites/real-bind', {
    method: 'POST',
    body: JSON.stringify(req),
  })
)

export const realDisconnect = async (req: RealDisconnectRequest): Promise<void> => {
  if (req.expectedConnection) {
    // The maintenance path is local only. Keep the backend's actual outcome
    // details when a checked delete fails or its commit cannot be confirmed.
    let response: Response
    try { response = await fetch(endpoint('/my-sites/real-disconnect'), { method: 'POST', headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() }, body: JSON.stringify(req) }) }
    catch { throw new Error('解除本地对接的结果暂时无法确认：Hub与服务器的连接中断。请先重新读取已有对接，并查看Hub后端解除记录，确认结果后再重试；本次不会删除主站账号或上游Key。') }
    let payload: unknown
    try { payload = JSON.parse(await response.text()) } catch {
      if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
      throw new Error('解除本地对接的结果暂时无法确认：服务器未返回完整结果。请先重新读取已有对接，并查看Hub后端解除记录，确认结果后再重试；本次不会删除主站账号或上游Key。')
    }
    if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    if (!response.ok) throw new Error(isObject(payload) ? [payload.message, payload.detail, payload.next].map(safeScopedErrorText).filter(Boolean).join('；') || '解除本地对接未完成，具体原因无法确认；请重新读取记录并核查Hub后端解除记录后再重试。' : '解除本地对接未完成，具体原因无法确认；请重新读取记录并核查Hub后端解除记录后再重试。')
    if (!isObject(payload) || (payload.ok !== true && payload.success !== true)) throw new Error('解除本地对接的结果暂时无法确认；请重新读取已有对接，并核查Hub后端解除记录后再重试。')
    return
  }
  await requestJson<{ ok: boolean }>('/my-sites/real-disconnect', {
    method: 'POST',
    body: JSON.stringify(req),
  })
}

export const listConnectionsNeedingAttention = async (request: { workspaceAdminAccountId: string; siteId?: string; page: number }, signal?: AbortSignal): Promise<ConnectionsNeedingAttentionResponse> => {
  const params = new URLSearchParams({ workspaceAdminAccountId: request.workspaceAdminAccountId, page: String(request.page) })
  if (request.siteId) params.set('siteId', request.siteId)
  let response: Response
  try { response = await fetch(endpoint(`/my-sites/real-connections/needs-attention?${params}`), { signal, headers: { Accept: 'application/json', ...authHeaders() } }) }
  catch (error) { if (scopedAbortError(error)) throw error; throw new Error('Hub与服务器的连接中断，对接资料未读取；请重试读取，并检查Hub后端对接读取记录') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (scopedAbortError(error)) throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error('服务器未返回可读取的完整对接资料；请重试读取，并查看Hub后端对接读取记录')
  }
  if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
  if (!response.ok) throw new Error(isObject(payload) ? [payload.message, payload.detail, payload.next].map(scopedSafeText).filter(Boolean).join('；') || '具体原因无法确认，请查看Hub后端对接读取记录' : '具体原因无法确认，请查看Hub后端对接读取记录')
  const fail = () => new Error('服务器返回的工作区、分页或对接资料不完整；请重试读取，并查看Hub后端对接读取记录')
  if (!isObject(payload) || payload.workspaceAdminAccountId !== request.workspaceAdminAccountId || !Number.isSafeInteger(payload.total) || Number(payload.total) < 0 || payload.page !== request.page || payload.pageSize !== 30 || !Number.isSafeInteger(payload.totalPages) || payload.totalPages !== Math.ceil(Number(payload.total) / 30) || !Array.isArray(payload.items) || payload.items.length > 30) throw fail()
  const expectedCount = Math.min(30, Math.max(0, Number(payload.total) - (request.page - 1) * 30))
  if (payload.items.length !== expectedCount) throw fail()
  const ids = new Set<string>()
  for (const item of payload.items) {
    if (!isObject(item) || !isObject(item.connection) || !['group_not_found', 'source_unconfirmed'].includes(String(item.reason)) || typeof item.detail !== 'string' || (item.lastSyncedAt !== null && (!Number.isSafeInteger(item.lastSyncedAt) || Number(item.lastSyncedAt) < 0))) throw fail()
    const connection = item.connection
    if (!scopedIdentityText(connection.id) || ids.has(connection.id) || !scopedIdentityText(connection.upstreamSiteId) || typeof connection.upstreamGroupId !== 'string' || typeof connection.upstreamGroupName !== 'string' || typeof connection.upstreamKeyId !== 'string' || typeof connection.adminAccountId !== 'string' || !['active', 'missing', ''].includes(String(connection.status ?? '')) || !Array.isArray(connection.ownGroupIds) || !connection.ownGroupIds.every(value => typeof value === 'string') || (connection.ownGroupNames !== undefined && (!Array.isArray(connection.ownGroupNames) || !connection.ownGroupNames.every(value => typeof value === 'string'))) || (connection.upstreamKey !== undefined && connection.upstreamKey !== '') || (connection.operationId !== undefined && connection.operationId !== '')) throw fail()
    ids.add(connection.id)
    item.detail = scopedSafeText(item.detail)
  }
  return payload as unknown as ConnectionsNeedingAttentionResponse
}

export const runAutoPricing = async (req: RunAutoPricingRequest): Promise<RunAutoPricingResponse> => {
  const response = await requestJson<RunAutoPricingResponse>('/my-sites/auto-pricing/run', {
    method: 'POST',
    body: JSON.stringify(req),
  })
  return {
    ...response,
    mapping: normalizeMappings([response.mapping])[0] ?? response.mapping,
  }
}

// New backends update one mapping atomically. A generic method-not-supported
// response falls back to the legacy full-array PUT so rolling deployments remain usable.
export const saveMySiteMapping = async (mapping: MySiteMapping, currentMappings: MySiteMapping[]): Promise<MySiteStatus> => {
  try {
    return normalizeStatus(await requestJson<MySiteStatus>('/my-sites/mappings', {
      method: 'PATCH',
      body: JSON.stringify({ mapping: toMappingRequest(mapping) }),
    }))
  } catch (error) {
    if (!(error instanceof Error) || error.message !== 'admin.mySites.errors.request') throw error
    const nextMappings = currentMappings.some(item => item.ownGroup === mapping.ownGroup)
      ? currentMappings.map(item => item.ownGroup === mapping.ownGroup ? mapping : item)
      : [...currentMappings, mapping]
    return saveMySiteMappings(nextMappings)
  }
}

export const removeMySiteMapping = async (ownGroup: string, currentMappings: MySiteMapping[]): Promise<MySiteStatus> => {
  try {
    return normalizeStatus(await requestJson<MySiteStatus>(`/my-sites/mappings/${encodeURIComponent(ownGroup)}`, { method: 'DELETE' }))
  } catch (error) {
    if (!(error instanceof Error) || error.message !== 'admin.mySites.errors.request') throw error
    return saveMySiteMappings(currentMappings.filter(item => item.ownGroup !== ownGroup))
  }
}

export class ScopedCheckApiError extends Error {
  constructor(message: string, readonly detail = '', readonly next = '', readonly result?: import('../types/mySites').ScopedCheckResult) {
    super(message)
    this.name = 'ScopedCheckApiError'
  }
}
const scopedSafeText = safeScopedErrorText
const scopedIdentityText = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 128 && !/[\u0000-\u001f\u007f]/.test(value)
const scopedAbortError = (error: unknown): boolean => error instanceof Error && error.name === 'AbortError'
const parseScopedCheckResult = (value: unknown, workspace: string): import('../types/mySites').ScopedCheckResult | undefined => {
  if (!isObject(value) || value.workspaceAdminAccountId !== workspace || !Number.isSafeInteger(value.total) || Number(value.total) < 0 || typeof value.storageUncertain !== 'boolean' || !isObject(value.confirmedStatuses) || !Array.isArray(value.issues)) return undefined
  const keys = ['confirmedConnectionIds', 'unconfirmedConnectionIds', 'skippedConnectionIds', 'changedConnectionIds'] as const
  const seen = new Set<string>()
  for (const key of keys) {
    if (!Array.isArray(value[key])) return undefined
    for (const id of value[key]) { if (!scopedIdentityText(id) || seen.has(id)) return undefined; seen.add(id) }
  }
  if (seen.size !== value.total) return undefined
  const confirmed = value.confirmedConnectionIds as string[]
  if (Object.keys(value.confirmedStatuses).length !== confirmed.length || !confirmed.every(id => value.confirmedStatuses && isObject(value.confirmedStatuses) && ['active', 'missing'].includes(String(value.confirmedStatuses[id]))) || (value.storageUncertain && confirmed.length !== 0)) return undefined
  const issues: import('../types/mySites').ScopedCheckIssue[] = []
  for (const issue of value.issues) {
    if (!isObject(issue) || !scopedIdentityText(issue.step) || typeof issue.detail !== 'string' || typeof issue.next !== 'string' || typeof issue.retryAllowed !== 'boolean' || (issue.connectionId !== undefined && (!scopedIdentityText(issue.connectionId) || !seen.has(issue.connectionId))) || (issue.accountId !== undefined && !scopedIdentityText(issue.accountId)) || (issue.httpStatus !== undefined && !integerInRange(issue.httpStatus, 100, 599))) return undefined
    issues.push({ step: issue.step, detail: scopedSafeText(issue.detail), next: scopedSafeText(issue.next), retryAllowed: issue.retryAllowed,
      ...(typeof issue.connectionId === 'string' ? { connectionId: issue.connectionId } : {}), ...(typeof issue.accountId === 'string' ? { accountId: issue.accountId } : {}), ...(typeof issue.httpStatus === 'number' ? { httpStatus: issue.httpStatus } : {}) })
  }
  return { workspaceAdminAccountId: workspace, total: Number(value.total), confirmedConnectionIds: [...confirmed], unconfirmedConnectionIds: [...value.unconfirmedConnectionIds as string[]], skippedConnectionIds: [...value.skippedConnectionIds as string[]], changedConnectionIds: [...value.changedConnectionIds as string[]], confirmedStatuses: Object.fromEntries(confirmed.map(id => [id, (value.confirmedStatuses as Record<string, 'active' | 'missing'>)[id]])), issues, storageUncertain: value.storageUncertain }
}
export const checkScopedRealConnections = async (request: import('../types/mySites').ScopedCheckRequest, signal?: AbortSignal): Promise<import('../types/mySites').ScopedCheckResult> => {
  let response: Response
  try { response = await fetch(endpoint('/my-sites/real-connections/check-scoped'), { method: 'POST', signal, headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() }, body: JSON.stringify(request) }) }
  catch (error) { if (scopedAbortError(error)) throw error; throw new ScopedCheckApiError('账号检查结果暂时无法取得', 'Hub与服务器的连接已中断，无法确认本批保存结果；仍显示上次状态', '请点击刷新重试，并检查Hub后端检查记录') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (scopedAbortError(error)) throw error
    if (response.status === 401) { handleAuthExpired(); throw new ScopedCheckApiError(authUnauthorizedErrorKey, 'Hub登录已失效，本次结果无法确认', '请重新登录Hub后刷新') }
    throw new ScopedCheckApiError('账号检查结果无法读取', '服务器没有返回可确认的完整检查结果，仍显示上次状态', '请点击刷新重试，并查看Hub后端检查记录')
  }
  if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new ScopedCheckApiError(authUnauthorizedErrorKey, 'Hub登录已失效，本次结果无法确认', '请重新登录Hub后刷新') }
  const result = parseScopedCheckResult(payload, request.workspaceAdminAccountId)
  if (!response.ok) throw new ScopedCheckApiError(isObject(payload) ? scopedSafeText(payload.message) || '本批账号状态未能检查完成' : '本批账号状态未能检查完成', isObject(payload) ? scopedSafeText(payload.detail) : '', isObject(payload) ? scopedSafeText(payload.next) : '请刷新重试并查看Hub后端记录', result)
  if (!result) throw new ScopedCheckApiError('账号检查结果无法确认', '返回的工作区、账号数量或状态结果不完整，仍显示上次状态', '请刷新重试，并查看Hub后端检查记录')
  return result
}
export const listScopedRealConnections = async (request: { workspaceAdminAccountId: string; groupRefs: import('../types/mySites').ScopedGroupRef[] }, signal?: AbortSignal): Promise<{ workspaceAdminAccountId: string; items: RealConnection[] }> => {
  let response: Response
  try { response = await fetch(endpoint('/my-sites/real-connections/list-scoped'), { method: 'POST', signal, headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() }, body: JSON.stringify(request) }) }
  catch (error) { if (scopedAbortError(error)) throw error; throw new Error('Hub与服务器的连接中断，本页对接资料未读取；请刷新并检查Hub后端连接') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (scopedAbortError(error)) throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error('Hub返回的本页对接资料无法读取；请刷新并查看Hub后端记录')
  }
  if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
  if (!response.ok) throw new Error(isObject(payload) ? [payload.message, payload.detail, payload.next].map(scopedSafeText).filter(Boolean).join('；') || '本页对接资料未读取，请刷新并查看Hub后端记录' : '本页对接资料未读取，请刷新并查看Hub后端记录')
  if (!isObject(payload) || payload.workspaceAdminAccountId !== request.workspaceAdminAccountId || !Array.isArray(payload.items)) throw new Error('本页对接资料的工作区或内容无法确认，请刷新重试')
  const ids = new Set<string>()
  for (const item of payload.items) {
    if (!isObject(item) || !scopedIdentityText(item.id) || ids.has(item.id) || !scopedIdentityText(item.upstreamSiteId) || typeof item.upstreamGroupId !== 'string' || typeof item.upstreamGroupName !== 'string' || typeof item.adminAccountId !== 'string' || typeof item.status !== 'string' || !['active', 'missing', ''].includes(item.status) || (item.upstreamKey !== undefined && item.upstreamKey !== '')) throw new Error('本页对接资料不完整或状态无法确认，请刷新并查看Hub后端记录')
    ids.add(item.id)
  }
  return { workspaceAdminAccountId: request.workspaceAdminAccountId, items: payload.items as RealConnection[] }
}

export const readOwnGroupRates = async (workspace: string, signal?: AbortSignal): Promise<OwnGroupRatesResponse> => {
  const invalid = () => new ScopedCheckApiError('读取主站分组失败', 'Hub 返回的主站分组资料不完整，无法确认', '请点“刷新数据”重试')
  let response: Response
  try { response = await fetch(endpoint('/my-sites/own-group-rates?workspaceAdminAccountId=' + encodeURIComponent(workspace)), { method: 'GET', signal, headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() } }) }
  catch (error) { if (scopedAbortError(error)) throw error; throw new ScopedCheckApiError('读取主站分组失败', 'Hub 后端没有响应', '请确认 Hub 服务正在运行，再点“刷新数据”') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (scopedAbortError(error)) throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw invalid()
  }
  if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
  if (!response.ok) {
    const record = isObject(payload) ? payload : {}
    let message = safeScopedErrorText(record.message)
    if (/^(?:admin|auth)\.[A-Za-z0-9_.]+$/.test(message)) message = te(message) ? t(message) : ''
    message ||= '主站分组读取没有完成'
    throw new ScopedCheckApiError(message, safeScopedErrorText(record.detail) || message, safeScopedErrorText(record.next) || '请点“刷新数据”重试')
  }
  if (!isObject(payload) || typeof payload.workspaceAdminAccountId !== 'string') throw invalid()
  if (payload.workspaceAdminAccountId !== workspace) throw new ScopedCheckApiError('读取主站分组失败', '返回的资料属于另一个工作区，已忽略', '请重新进入本页')
  if (!Array.isArray(payload.groups)) throw invalid()
  const ids = new Set<string>()
  for (const group of payload.groups) {
    if (!isObject(group) || !scopedIdentityText(group.id) || ids.has(group.id) || typeof group.name !== 'string' || typeof group.status !== 'string' || !(group.multiplier === null || typeof group.multiplier === 'number' && Number.isFinite(group.multiplier))) throw invalid()
    ids.add(group.id)
  }
  return payload as unknown as OwnGroupRatesResponse
}

export class MainSiteGroupsApiError extends Error {
  constructor(readonly kind: 'network' | 'invalid') {
    super(kind === 'network' ? 'Hub 后端没有回复，请确认 Hub 服务正在运行后点“重新读取”' : 'Hub 返回的资料不完整或不属于当前工作区，请点“重新读取”')
    this.name = 'MainSiteGroupsApiError'
  }
}
const validMainSiteGroups = (value: unknown, withStatus = false): boolean => {
  if (!Array.isArray(value)) return false
  const ids = new Set<string>()
  for (const group of value) {
    if (!isObject(group) || !scopedIdentityText(group.id) || ids.has(group.id) || typeof group.name !== 'string' || withStatus && typeof group.status !== 'string') return false
    ids.add(group.id)
  }
  return true
}
const validMainSiteGroupConnection = (value: unknown, id: string): value is RealConnection => {
  if (!isObject(value) || value.id !== id || value.upstreamKey !== undefined && value.upstreamKey !== '') return false
  for (const key of ['upstreamSiteId', 'upstreamGroupId', 'upstreamGroupName', 'upstreamKeyId', 'adminAccountId', 'adminAccountName', 'groupType', 'createdAt']) if (typeof value[key] !== 'string') return false
  if (!Array.isArray(value.ownGroupIds) || !value.ownGroupIds.every(scopedIdentityText)) return false
  if (value.ownGroupNames !== undefined && (!Array.isArray(value.ownGroupNames) || !value.ownGroupNames.every(name => typeof name === 'string'))) return false
  for (const key of ['adminPlatform', 'upstreamPlatform', 'provisioningMode', 'status']) if (value[key] !== undefined && typeof value[key] !== 'string') return false
  return value.pricingMappingEnabled === undefined || typeof value.pricingMappingEnabled === 'boolean'
}
const mainSiteGroupsRequest = async (id: string, workspace: string, request?: SyncConnectionHubGroupsRequest | ChangeConnectionMainSiteGroupsRequest, sync = false, signal?: AbortSignal): Promise<unknown> => {
  const path = '/my-sites/real-connections/' + encodeURIComponent(id) + '/main-site-groups' + (sync ? '/hub-record' : '')
  let response: Response
  try {
    response = await fetch(endpoint(path + (request ? '' : '?workspaceAdminAccountId=' + encodeURIComponent(workspace))), {
      method: request ? 'POST' : 'GET', signal,
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...authHeaders() },
      ...(request ? { body: JSON.stringify(request) } : {}),
    })
  } catch (error) { if (scopedAbortError(error)) throw error; throw new MainSiteGroupsApiError('network') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (scopedAbortError(error)) throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new MainSiteGroupsApiError('invalid')
  }
  if (isUnauthorizedApiResponse(response.status, isObject(payload) ? payload : {})) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
  if (!response.ok) throw new Error(isObject(payload) ? [payload.message, payload.detail, payload.next].map(scopedSafeText).filter(Boolean).join('；') || 'Hub 未能处理主站分组，请重新读取并查看 Hub 后端记录' : 'Hub 未能处理主站分组，请重新读取并查看 Hub 后端记录')
  if (!isObject(payload) || payload.workspaceAdminAccountId !== workspace) throw new MainSiteGroupsApiError('invalid')
  return payload
}
export const readConnectionMainSiteGroups = async (id: string, workspace: string, signal?: AbortSignal): Promise<ConnectionMainSiteGroups> => {
  const payload = await mainSiteGroupsRequest(id, workspace, undefined, false, signal)
  if (!isObject(payload) || !validMainSiteGroupConnection(payload.connection, id) || !isObject(payload.account) || !scopedIdentityText(payload.account.id) || typeof payload.account.name !== 'string' || typeof payload.account.platform !== 'string' || !validMainSiteGroups(payload.currentGroups) || !validMainSiteGroups(payload.hubGroups) || !validMainSiteGroups(payload.groups, true) || typeof payload.consistent !== 'boolean') throw new MainSiteGroupsApiError('invalid')
  return payload as unknown as ConnectionMainSiteGroups
}
const mutateConnectionMainSiteGroups = async (id: string, request: SyncConnectionHubGroupsRequest | ChangeConnectionMainSiteGroupsRequest, sync: boolean): Promise<MainSiteGroupMutationResult> => {
  const payload = await mainSiteGroupsRequest(id, request.workspaceAdminAccountId, request, sync)
  if (!isObject(payload) || !['completed', 'unchanged', 'main_changed_hub_not_saved', 'main_unknown', 'hub_unknown'].includes(String(payload.outcome))) throw new MainSiteGroupsApiError('invalid')
  // The outcome and workspace are the confirmation boundary. Display text is
  // bounded and sanitized without retaining the original response envelope.
  return { workspaceAdminAccountId: request.workspaceAdminAccountId, outcome: payload.outcome as MainSiteGroupMutationResult['outcome'],
    message: scopedSafeText(payload.message), detail: scopedSafeText(payload.detail), next: scopedSafeText(payload.next),
    mainSiteGroups: validMainSiteGroups(payload.mainSiteGroups) ? payload.mainSiteGroups as MainSiteGroupMutationResult['mainSiteGroups'] : [],
    ...(validMainSiteGroupConnection(payload.connection, id) ? { connection: payload.connection } : {}),
  }
}
export const changeConnectionMainSiteGroups = (id: string, request: ChangeConnectionMainSiteGroupsRequest): Promise<MainSiteGroupMutationResult> => mutateConnectionMainSiteGroups(id, request, false)
export const syncConnectionHubGroups = (id: string, request: SyncConnectionHubGroupsRequest): Promise<MainSiteGroupMutationResult> => mutateConnectionMainSiteGroups(id, request, true)

import type {
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
  'admin.connectionHealth.errors.sub2apiGroupLastUsable',
  'admin.connectionHealth.errors.sub2apiInventoryIncomplete',
  'admin.connectionHealth.errors.remoteActionPending',
  'admin.connectionHealth.testConfiguration.remoteActionPending',
  ...['auth', 'forbidden', 'notFound', 'rateLimited', 'upstreamServerError', 'networkTimeout', 'networkUnreachable', 'tlsFailed', 'invalidResponse', 'businessRejected'].map(key => `admin.upstream.errors.${key}`),
])
const importStages = new Set(['validation', 'upstream_key', 'model_sync', 'account_create', 'configuration_check', 'persistence'])
const importCleanups = new Set(['not_needed', 'confirmed', 'retained', 'pending'])
const isObject = (value: unknown): value is Record<string, unknown> => value != null && typeof value === 'object' && !Array.isArray(value)
const safeText = (value: unknown): value is string => typeof value === 'string' && value.trim().length > 0 && value.length <= 512 && !/[\u0000-\u001f\u007f]/.test(value)
const positiveResourceId = (value: unknown): value is string => typeof value === 'string' && /^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value))
const integerInRange = (value: unknown, minimum: number, maximum: number): value is number => typeof value === 'number' && Number.isInteger(value) && value >= minimum && value <= maximum

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
  }, true)
}

const parseImportConfiguration = (value: unknown, connection: RealConnection): ImportConfiguration | null => {
  if (!isObject(value) || typeof value.observation !== 'string' || !['creation', 'current'].includes(value.observation) ||
    value.adminAccountId !== connection.adminAccountId || typeof value.platform !== 'string' || value.platform !== connection.groupType || !safeText(value.name) ||
    typeof value.modelState !== 'string' || !['synced', 'not_required', 'current_whitelist', 'current_unrestricted'].includes(value.modelState) ||
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
    if (value.modelState !== 'synced' || models.length === 0 || models.some(model => model.includes('*')) || ownGroups.length === 0) return null
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
  const configuration = payload.configurationStatus === 'confirmed' ? parseImportConfiguration(payload.configuration, connection) : null
  return {
    connection, workspaceAdminAccountId,
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
  await requestJson<{ ok: boolean }>('/my-sites/real-disconnect', {
    method: 'POST',
    body: JSON.stringify(req),
  })
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

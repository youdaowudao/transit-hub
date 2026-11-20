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
  output?: string
}

class SystemApiError extends Error {
  readonly transient: boolean
  readonly status?: number

  constructor(message: string, transient = false, status?: number) {
    super(message)
    this.name = 'SystemApiError'
    this.transient = transient
    this.status = status
  }
}

export const isTransientSystemApiError = (error: unknown): boolean => (
  error instanceof SystemApiError && error.transient
)

// 回滚到不含 /api/system/rollback 路由的旧版本时，轮询会收到 404，
// 前端应转为用 version 接口判断是否已回到还原点版本，而非直接报错。
export const isRollbackRouteNotFound = (error: unknown): boolean => (
  error instanceof SystemApiError && error.status === 404
)

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
    throw new SystemApiError('admin.system.errors.network', true)
  }

  const text = await response.text()
  let payload = {} as T & AdminErrorPayload
  if (text) {
    try {
      payload = JSON.parse(text) as T & AdminErrorPayload
    } catch {
      throw new SystemApiError(
        response.ok ? 'admin.system.errors.request' : 'admin.system.errors.network',
        response.status === 502 || response.status === 503 || response.status === 504,
        response.status,
      )
    }
  }

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    const message = [payload.message, payload.output].filter(Boolean).join('\n') || 'admin.system.errors.request'
    throw new SystemApiError(message, response.status === 502 || response.status === 503 || response.status === 504, response.status)
  }

  return payload
}

export interface SystemVersionResponse {
  version: string
  updateAvailable?: boolean
}

export const getSystemVersion = async (): Promise<SystemVersionResponse> => (
  requestJson<SystemVersionResponse>('/system/version')
)

export type SystemUpgradeState = 'idle' | 'starting' | 'running' | 'succeeded' | 'failed'

export interface SystemUpgradeStartResponse {
  state: 'starting'
  requestedAt: string
}

export interface SystemUpgradeStatusResponse {
  state: SystemUpgradeState
  startedAt?: string
  finishedAt?: string
  exitCode?: number
  output?: string
}

export const startSystemUpgrade = async (): Promise<SystemUpgradeStartResponse> => (
  requestJson<SystemUpgradeStartResponse>('/system/upgrade', { method: 'POST' })
)

export const getSystemUpgradeStatus = async (): Promise<SystemUpgradeStatusResponse> => (
  requestJson<SystemUpgradeStatusResponse>('/system/upgrade')
)

export type SystemRestartState = 'idle' | 'starting' | 'running' | 'succeeded' | 'failed'

export interface SystemRestartStartResponse {
  state: 'starting'
  requestedAt: string
}

export interface SystemRestartStatusResponse {
  state: SystemRestartState
  startedAt?: string
  finishedAt?: string
  exitCode?: number
  output?: string
}

export const startSystemRestart = async (): Promise<SystemRestartStartResponse> => (
  requestJson<SystemRestartStartResponse>('/system/restart', { method: 'POST' })
)

export const getSystemRestartStatus = async (): Promise<SystemRestartStatusResponse> => (
  requestJson<SystemRestartStatusResponse>('/system/restart')
)

export type SystemRollbackState = 'idle' | 'starting' | 'running' | 'succeeded' | 'failed'

export interface SystemRollbackPoint {
  commit: string
  version: string
  schemaVersion: number
  dumpPath?: string
  capturedAt: string
}

export interface SystemRollbackStartResponse {
  state: 'starting'
  requestedAt: string
}

export interface SystemRollbackStatusResponse {
  state: SystemRollbackState
  startedAt?: string
  finishedAt?: string
  exitCode?: number
  output?: string
  point?: SystemRollbackPoint
}

export const startSystemRollback = async (): Promise<SystemRollbackStartResponse> => (
  requestJson<SystemRollbackStartResponse>('/system/rollback', { method: 'POST' })
)

export const getSystemRollbackStatus = async (): Promise<SystemRollbackStatusResponse> => (
  requestJson<SystemRollbackStatusResponse>('/system/rollback')
)

export interface CredentialsStatusResponse {
  configured: boolean
  mode: 'legacy' | 'encrypted' | 'unverified'
  message: string
}

export const getCredentialsStatus = async (): Promise<CredentialsStatusResponse> => {
  let response: Response
  try {
    response = await fetch(endpoint('/system/credentials/status'), { headers: { Accept: 'application/json', ...authHeaders() } })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('连接本站凭据保存状态接口失败；请检查网络后重新打开系统设置。')
  }
  let payload: unknown
  try {
    payload = await response.json()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('凭据保存状态接口未返回完整JSON；请查看本站日志后重新打开系统设置。')
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('凭据保存状态资料格式不完整；请查看本站日志后重新打开系统设置。')
  const value = payload as Record<string, unknown>
  if (!response.ok) {
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error(typeof value.message === 'string' && value.message ? value.message : `读取凭据保存状态未完成（本站HTTP ${response.status}）；请查看本站日志后重试。`)
  }
  if (typeof value.configured !== 'boolean' || typeof value.mode !== 'string' || !['legacy', 'encrypted', 'unverified'].includes(value.mode) || typeof value.message !== 'string') throw new Error('凭据保存状态资料格式不完整；请查看本站日志后重新打开系统设置。')
  return value as unknown as CredentialsStatusResponse
}

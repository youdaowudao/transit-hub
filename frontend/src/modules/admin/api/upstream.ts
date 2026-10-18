import { scopedReadFailureText } from '../utils/scopedErrorText'
import type {
  SiteSettings,
  UpstreamSiteOption,
  UpstreamFailure,
  SyncStreamEvent,
  UpstreamSiteForm,
  UpstreamSiteResponse,
} from '../types/upstream'
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
  failure?: UpstreamFailure
}

export class UpstreamApiError extends Error {
  constructor(readonly key: string, readonly failure?: UpstreamFailure) {
    super(key)
    this.name = 'UpstreamApiError'
  }
}

const safeErrorKey = (key: unknown): string => typeof key === 'string' && /^admin\.upstream\.errors\.[A-Za-z][A-Za-z0-9]*$/.test(key)
  ? key : 'admin.upstream.errors.request'


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
    throw new Error('admin.upstream.errors.network')
  }

  const text = await response.text()
  let payload = {} as T & AdminErrorPayload
  try {
    const parsed = text ? JSON.parse(text) : {}
    if (parsed !== null && typeof parsed === 'object') payload = parsed as T & AdminErrorPayload
  } catch {
    if (response.ok) throw new UpstreamApiError('admin.upstream.errors.invalidResponse')
  }

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }

    // Local missing-site operations use HTTP 404; upstream login failures use
    // HTTP 422 and retain the upstream notFound reason and failure details.
    if (response.status === 404 && payload.message === 'admin.upstream.errors.notFound') {
      throw new UpstreamApiError('admin.upstream.errors.siteNotFound', payload.failure)
    }
    throw new UpstreamApiError(safeErrorKey(payload.message), payload.failure)
  }

  return payload
}

export const listUpstreamSites = async (siteId?: string, signal?: AbortSignal): Promise<UpstreamSiteResponse[]> => {
  const sites = await requestJson<UpstreamSiteResponse[]>(siteId === undefined ? '/upstream-sites' : `/upstream-sites?siteId=${encodeURIComponent(siteId)}`, { signal, ...(siteId === undefined ? {} : { cache: 'no-store' as RequestCache }) })
  if (siteId !== undefined && (!Array.isArray(sites) || sites.length !== 1 || sites[0]?.id !== siteId || !sites[0]?.metrics || !Array.isArray(sites[0].metrics.groups))) {
    throw new UpstreamApiError('admin.upstream.errors.invalidResponse', { stage: 'metrics', errorDetail: '本站缓存返回的站点或分组资料不完整，本次没有取得新结果；请检查 Hub 服务读取日志后重试，不会修改主站。' })
  }
  return sites
}

export const createUpstreamSite = async (form: UpstreamSiteForm): Promise<UpstreamSiteResponse> => (
  requestJson<UpstreamSiteResponse>('/upstream-sites', {
    method: 'POST',
    body: JSON.stringify(form),
  })
)

export const updateUpstreamSite = async (id: string, form: UpstreamSiteForm): Promise<UpstreamSiteResponse> => (
  requestJson<UpstreamSiteResponse>(`/upstream-sites/${id}`, {
    method: 'PUT',
    body: JSON.stringify(form),
  })
)

export const updateUpstreamSiteEnabled = async (id: string, enabled: boolean): Promise<UpstreamSiteResponse> => (
  requestJson<UpstreamSiteResponse>(`/upstream-sites/${id}/enabled`, {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })
)

export const syncUpstreamSite = async (id: string): Promise<UpstreamSiteResponse> => (
  requestJson<UpstreamSiteResponse>(`/upstream-sites/${id}/sync`, { method: 'POST' })
)

export const syncAllUpstreamSites = async (): Promise<UpstreamSiteResponse[]> => (
  requestJson<UpstreamSiteResponse[]>('/upstream-sites/sync-all', { method: 'POST' })
)

export const removeUpstreamSite = async (id: string): Promise<void> => {
  await requestJson<{ success: boolean }>(`/upstream-sites/${id}`, { method: 'DELETE' })
}

export const updateSiteSettings = async (id: string, settings: SiteSettings): Promise<UpstreamSiteResponse> => (
  requestJson<UpstreamSiteResponse>(`/upstream-sites/${id}/settings`, {
    method: 'PATCH',
    body: JSON.stringify(settings),
  })
)

/** 以 SSE 流方式逐站同步，每个站点的进度通过 onEvent 回调实时推送。 */
export const streamSyncAllUpstreamSites = async (
  onEvent: (event: SyncStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> => {
  let response: Response
  try {
    response = await fetch(endpoint('/upstream-sites/sync-stream'), {
      headers: { Accept: 'text/event-stream', ...authHeaders() },
      signal,
    })
  } catch {
    throw new Error('admin.upstream.errors.network')
  }

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, {})) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error('admin.upstream.errors.request')
  }

  const reader = response.body!.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  // eslint-disable-next-line no-constant-condition
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const parts = buffer.split('\n\n')
    buffer = parts.pop()!
    for (const part of parts) {
      const dataLine = part.split('\n').find(l => l.startsWith('data: '))
      if (dataLine) {
        try {
          const event = JSON.parse(dataLine.slice(6)) as SyncStreamEvent
          onEvent(event)
        } catch { /* skip malformed lines */ }
      }
    }
  }
}

export const listUpstreamSiteOptions = async (signal?: AbortSignal): Promise<{ workspaceAdminAccountId: string; items: UpstreamSiteOption[] }> => {
  let response: Response
  try { response = await fetch(endpoint('/upstream-sites/options'), { signal, headers: { Accept: 'application/json', ...authHeaders() } }) }
  catch (error) { if (error instanceof Error && error.name === 'AbortError') throw error; throw new Error('Hub暂时无法连接服务器，请刷新并检查后端连接') }
  let payload: unknown
  try { payload = JSON.parse(await response.text()) } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error('网站名称返回格式无法读取，本次未修改网站或主站账号；请刷新并查看Hub后端网站选项读取记录。')
  }
  const record = payload && typeof payload === 'object' ? payload as Record<string, unknown> : {}
  if (isUnauthorizedApiResponse(response.status, record)) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
  if (!response.ok) throw new Error(scopedReadFailureText(record, '网站名称缓存无法读取，本次未修改网站或主站账号；请刷新并检查Hub后端缓存连接。'))
  if (typeof record.workspaceAdminAccountId !== 'string' || !Array.isArray(record.items) || !record.items.every(item => item && typeof item.id === 'string' && typeof item.name === 'string')) throw new Error('网站名称返回内容不完整，请刷新并查看Hub后端记录')
  return record as { workspaceAdminAccountId: string; items: UpstreamSiteOption[] }
}

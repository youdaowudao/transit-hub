import type {
  SiteSettings,
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
    throw new Error('admin.upstream.errors.network')
  }

  const text = await response.text()
  const payload = text ? JSON.parse(text) as T & AdminErrorPayload : ({} as T & AdminErrorPayload)

  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }

    throw new Error('admin.upstream.errors.request')
  }

  return payload
}

export const listUpstreamSites = async (): Promise<UpstreamSiteResponse[]> => requestJson<UpstreamSiteResponse[]>('/upstream-sites')

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

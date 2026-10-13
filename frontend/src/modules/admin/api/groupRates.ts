import { scopedReadFailureText } from '../utils/scopedErrorText'
import type { GroupRateHistoryQuery, GroupRateHistoryRow, GroupRatesQuery, PaginatedGroupRatesResponse, UpdateGroupRateTypeRequest } from '../types/groupRates'
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
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('admin.groupRates.errors.network')
  }

  let payload: T & AdminErrorPayload
  try { const text = await response.text(); payload = text ? JSON.parse(text) as T & AdminErrorPayload : ({} as T & AdminErrorPayload) }
  catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error('倍率资料返回内容无法读取，本次未修改倍率或主站账号；请刷新重试，并查看Hub后端倍率读取记录。')
  }

  if (!response.ok) {
    const errorPayload = payload != null && typeof payload === 'object' ? payload : {}
    if (isUnauthorizedApiResponse(response.status, errorPayload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }

    throw new Error(scopedReadFailureText(payload, '倍率资料暂时无法读取，本次未修改主站账号；请刷新并查看Hub后端倍率读取记录。'))
  }

  return payload
}

export const listGroupRates = async (query: GroupRatesQuery, signal?: AbortSignal): Promise<PaginatedGroupRatesResponse> => {
  const params = new URLSearchParams({
    page: query.page.toString(),
    search: query.search.trim(),
    type: query.type,
    platform: query.platform,
    status: query.status,
    sort: query.sort,
  })

  for (const key of ['workspaceAdminAccountId', 'siteId', 'focusGroupId', 'focusGroup'] as const) { if (query[key]) params.set(key, query[key]) }
  return requestJson<PaginatedGroupRatesResponse>(`/group-rates?${params.toString()}`, { signal })
}

export const listAllGroupRates = async (query: Omit<GroupRatesQuery, 'page'>): Promise<PaginatedGroupRatesResponse['items']> => {
  const firstPage = await listGroupRates({ ...query, page: 1 })
  const items = [...firstPage.items]
  const totalPages = Math.max(1, firstPage.totalPages || 1)

  for (let page = 2; page <= totalPages; page += 1) {
    const response = await listGroupRates({ ...query, page })
    items.push(...response.items)
  }

  return items
}

export const listGroupRateHistory = async (query: GroupRateHistoryQuery): Promise<GroupRateHistoryRow[]> => {
  const params = new URLSearchParams({
    siteId: query.siteId,
    groupName: query.groupId || query.groupName,
  })

  if (query.platform) params.set('platform', query.platform)

  return requestJson<GroupRateHistoryRow[]>(`/group-rates/history?${params.toString()}`)
}

export const updateGroupRateType = async (request: UpdateGroupRateTypeRequest): Promise<void> => {
  await requestJson<{ success: boolean }>('/group-rates/type', {
    method: 'PATCH',
    body: JSON.stringify(request),
  })
}

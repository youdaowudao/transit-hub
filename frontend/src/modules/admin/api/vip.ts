import { authUnauthorizedErrorKey, getAccessToken, handleAuthExpired, isUnauthorizedApiResponse } from '@/modules/auth/api/auth'
import type { VipScope, VipSummary, VipSettings, VipSearchResponse, VipMutationResponse } from '../types/vip'
const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')
const request = async <T>(scope: VipScope, path: string, method = 'GET', values: Record<string, unknown> = {}, signal?: AbortSignal): Promise<T & VipScope> => {
  const params = new URLSearchParams()
  if (method === 'GET') {
    params.set('workspaceAdminAccountId', scope.workspaceAdminAccountId)
    params.set('workspaceCreatedAt', scope.workspaceCreatedAt)
    for (const [key, value] of Object.entries(values)) params.set(key, String(value))
  }
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${baseUrl}/vip${path}${method === 'GET' ? `?${params}` : ''}`, {
      method, signal,
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(method === 'GET' ? {} : { body: JSON.stringify({ ...values, ...scope }) }),
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('连接本站大户关注接口失败，结果尚未确认；请检查网络并重新读取名单后再操作。')
  }
  let payload: T & VipScope & { message?: string }
  try { payload = await response.json() } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('大户关注接口未返回完整JSON，结果尚未确认；请检查本站服务日志后重新读取。')
  }
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('大户关注接口返回的资料不是完整对象；请检查本站服务日志后重新读取。')
  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error(payload.message ?? `大户关注操作未完成（本站HTTP ${response.status}），具体原因尚未确认；请查看本站服务日志并重新读取。`)
  }
  if (payload.workspaceAdminAccountId !== scope.workspaceAdminAccountId || payload.workspaceCreatedAt !== scope.workspaceCreatedAt) throw new Error('大户关注响应与请求的工作区不一致，已停止显示该响应；请重新读取当前工作区。')
  return payload
}
export const getVipSummary = (scope: VipScope, signal?: AbortSignal) => request<VipSummary>(scope, '/summary', 'GET', {}, signal)
export const searchVipUsers = (scope: VipScope, q: string, signal?: AbortSignal) => request<VipSearchResponse>(scope, '/search', 'GET', { q }, signal)
export const watchVipUser = (scope: VipScope, siteUserId: string, note: string, signal?: AbortSignal) => request<VipMutationResponse>(scope, '/watch', 'POST', { siteUserId, note }, signal)
export const unwatchVipUser = (scope: VipScope, siteUserId: string, signal?: AbortSignal) => request<VipMutationResponse>(scope, '/unwatch', 'POST', { siteUserId }, signal)
export const restoreVipRecommendations = (scope: VipScope, signal?: AbortSignal) => request<VipMutationResponse>(scope, '/dismissed/restore', 'POST', {}, signal)
export const saveVipSettings = (scope: VipScope, settings: VipSettings, signal?: AbortSignal) => request<VipMutationResponse>(scope, '/settings', 'PUT', { ...settings }, signal)

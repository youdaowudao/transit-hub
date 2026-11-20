import { authUnauthorizedErrorKey, getAccessToken, handleAuthExpired } from '@/modules/auth/api/auth'
import type { SalesScope, SalesSummary, SalesRangeQuery, SalesEntryQuery, SalesEntries, SalesEntryKey, SalesMutation, SalesSettings, SalesSettingsResult } from '../types/sales'
const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')
const request = async <T>(scope: SalesScope, path: string, method = 'GET', values: Record<string, unknown> = {}, signal?: AbortSignal): Promise<T & SalesScope> => {
  const params = new URLSearchParams()
  if (method === 'GET') {
    params.set('workspaceAdminAccountId', scope.workspaceAdminAccountId)
    params.set('workspaceCreatedAt', scope.workspaceCreatedAt)
    for (const [key, value] of Object.entries(values)) params.set(key, String(value))
  }
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${baseUrl}/sales${path}${method === 'GET' ? `?${params}` : ''}`, {
      method, signal,
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(method === 'GET' ? {} : { body: JSON.stringify({ ...values, ...scope }) }),
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('连接本站销售入账接口失败，结果尚未确认；请检查网络并重新读取明细后再操作。')
  }
  let payload: T & SalesScope & { message?: string }
  try { payload = await response.json() } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('销售入账接口未返回完整JSON，结果尚未确认；请检查本站服务日志后重新读取。')
  }
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('销售入账接口返回的资料不是完整对象；请检查本站服务日志后重新读取。')
  if (!response.ok) {
    if (response.status === 401) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error(typeof payload.message === 'string' && payload.message ? payload.message : `销售入账操作未完成（本站HTTP ${response.status}），具体原因尚未确认；请查看本站服务日志并重新读取。`)
  }
  if (payload.workspaceAdminAccountId !== scope.workspaceAdminAccountId || payload.workspaceCreatedAt !== scope.workspaceCreatedAt) throw new Error('销售入账响应与请求的工作区不一致，已停止显示该响应；请重新读取当前工作区。')
  return payload
}
export const getSalesSummary = (scope: SalesScope, range: SalesRangeQuery, signal?: AbortSignal) => request<SalesSummary>(scope, '/summary', 'GET', { ...range }, signal)
export const getSalesEntries = (scope: SalesScope, range: SalesRangeQuery, query: SalesEntryQuery, signal?: AbortSignal) => request<SalesEntries>(scope, '/entries', 'GET', { ...range, ...query }, signal)
export const excludeSalesEntries = (scope: SalesScope, entries: SalesEntryKey[], signal?: AbortSignal) => request<SalesMutation>(scope, '/exclusions', 'POST', { entries }, signal)
export const restoreSalesEntries = (scope: SalesScope, entries: SalesEntryKey[], signal?: AbortSignal) => request<SalesMutation>(scope, '/exclusions/delete', 'POST', { entries }, signal)
export const saveSalesSettings = (scope: SalesScope, settings: SalesSettings, signal?: AbortSignal) => request<SalesSettingsResult>(scope, '/settings', 'PUT', { ...settings }, signal)

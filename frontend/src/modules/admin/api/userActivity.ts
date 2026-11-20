import { authUnauthorizedErrorKey, getAccessToken, handleAuthExpired, isUnauthorizedApiResponse } from '@/modules/auth/api/auth'
import type { ActivityScope, ActivitySync, ActivitySummary, ActivityUserList, ActivityDetail, ActivityUserQuery } from '../types/userActivity'
const apiBase = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')
const request = async <T>(scope: ActivityScope, path: string, query: Record<string, string | number> = {}, signal?: AbortSignal, post = false): Promise<T & ActivityScope> => {
  const params = new URLSearchParams()
  if (!post) {
    params.set('workspaceAdminAccountId', scope.workspaceAdminAccountId)
    params.set('workspaceCreatedAt', scope.workspaceCreatedAt)
    for (const [key, value] of Object.entries(query)) if (value !== '') params.set(key, String(value))
  }
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${apiBase}/user-activity${path}${post ? '' : `?${params}`}`, {
      method: post ? 'POST' : 'GET', signal,
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(post ? { body: JSON.stringify(scope) } : {}),
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('连接本站用户分析接口失败，当前结果未确认；请检查网络后重新读取同步状态。')
  }
  let payload: T & ActivityScope & { message?: string }
  try {
    payload = await response.json()
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new Error('本站用户分析接口未返回完整JSON，结果未确认；请检查本站服务日志后重新读取。')
  }
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error('本站用户分析接口返回的资料不是完整对象，结果未确认；请检查本站服务日志后重新读取。')
  }
  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error(payload.message ?? `读取用户分析资料未完成（本站HTTP ${response.status}）；具体原因尚未确认，请查看本站服务日志后重试。`)
  }
  if (payload.workspaceAdminAccountId !== scope.workspaceAdminAccountId || payload.workspaceCreatedAt !== scope.workspaceCreatedAt) throw new Error('用户分析响应与请求的工作区不一致，已停止显示该响应；请重新读取当前工作区。')
  return payload
}
export const getActivitySummary = (scope: ActivityScope, days = 60, signal?: AbortSignal) => request<ActivitySummary>(scope, '/summary', { days }, signal)
export const getActivitySync = (scope: ActivityScope, signal?: AbortSignal) => request<ActivitySync>(scope, '/sync', {}, signal)
export const startActivitySync = (scope: ActivityScope, signal?: AbortSignal) => request<ActivitySync>(scope, '/sync', {}, signal, true)
export const getActivityUsers = (scope: ActivityScope, query: ActivityUserQuery, signal?: AbortSignal) => request<ActivityUserList>(scope, '/users', { ...query }, signal)
export const getActivityDetail = (scope: ActivityScope, id: string, signal?: AbortSignal) => request<ActivityDetail>(scope, `/users/${encodeURIComponent(id)}`, {}, signal)

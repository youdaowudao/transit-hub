import { authUnauthorizedErrorKey, getAccessToken, handleAuthExpired, isUnauthorizedApiResponse } from '@/modules/auth/api/auth'
import type { OperatingInsightsResponse, OperatingInsightsScope } from '../types/operatingInsights'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')

export const getOperatingInsights = async (scope: OperatingInsightsScope, signal?: AbortSignal): Promise<OperatingInsightsResponse> => {
  const query = new URLSearchParams({ workspaceAdminAccountId: scope.workspaceAdminAccountId, workspaceCreatedAt: scope.workspaceCreatedAt })
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${baseUrl}/dashboard/operating-insights?${query}`, {
      signal, headers: { Accept: 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    })
  } catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    throw new Error('连接本站经营提示接口失败；请检查网络后重新展开，本次没有采集或修改主站。')
  }
  let payload: OperatingInsightsResponse & { message?: string }
  try {
    payload = await response.json()
  } catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    throw new Error('经营提示接口没有返回完整JSON；请检查本站服务日志后重新读取。')
  }
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('经营提示接口返回的资料不是完整对象；请检查本站服务日志后重新读取。')
  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) {
      handleAuthExpired()
      throw new Error(authUnauthorizedErrorKey)
    }
    throw new Error(typeof payload.message === 'string' && payload.message ? payload.message : `经营提示读取未完成（本站HTTP ${response.status}）；具体原因尚未确认，请检查本站服务日志后重试。`)
  }
  if (payload.workspaceAdminAccountId !== scope.workspaceAdminAccountId || payload.workspaceCreatedAt !== scope.workspaceCreatedAt) throw new Error('经营提示响应与请求的工作区不一致，已停止显示；请重新展开当前工作区的经营提示。')
  return payload
}

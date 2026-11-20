import { authUnauthorizedErrorKey, getAccessToken, handleAuthExpired, isUnauthorizedApiResponse } from '@/modules/auth/api/auth'
import type { DailyReportPatch, DailyReportPreview, DailyReportScope, DailyReportTest } from '../types/reports'
import type { StrategySettings } from '../types/settings'

const baseUrl = (import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')
const request = async <T>(scope: DailyReportScope, path: string, method = 'GET', values: Record<string, unknown> = {}, signal?: AbortSignal): Promise<T & DailyReportScope> => {
  const query = method === 'GET' ? `?${new URLSearchParams({ workspaceAdminAccountId: scope.workspaceAdminAccountId, workspaceCreatedAt: scope.workspaceCreatedAt })}` : ''
  const followUp = path.endsWith('/daily/test')
    ? '发送结果尚未确认；请先查看Telegram和本站日志，避免重复发送。'
    : method === 'PUT'
      ? '保存结果尚未确认；请先重新读取已保存策略，再决定是否保存。'
      : '本次只是读取，没有发送消息；请检查本站服务日志后重新预览。'
  const token = getAccessToken()
  let response: Response
  try {
    response = await fetch(`${baseUrl}${path}${query}`, {
      method, signal, headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      ...(method === 'GET' ? {} : { body: JSON.stringify({ ...values, ...scope }) }),
    })
  } catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    throw new Error(`连接本站日报接口失败。${followUp}`)
  }
  let payload: T & DailyReportScope & { message?: string }
  try { payload = await response.json() } catch (cause) {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause
    throw new Error(`日报接口未返回完整JSON。${followUp}`)
  }
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) throw new Error(`日报接口返回的资料不是完整对象。${followUp}`)
  if (!response.ok) {
    if (isUnauthorizedApiResponse(response.status, payload)) { handleAuthExpired(); throw new Error(authUnauthorizedErrorKey) }
    throw new Error(typeof payload.message === 'string' && payload.message ? payload.message : `日报操作未完成（本站HTTP ${response.status}），具体原因尚未确认；请先重新读取并核查本站日志。`)
  }
  if (payload.workspaceAdminAccountId !== scope.workspaceAdminAccountId || payload.workspaceCreatedAt !== scope.workspaceCreatedAt) throw new Error(`日报响应与请求的工作区不一致，已停止显示该响应。${followUp}`)
  return payload
}
export const getDailyReportPreview = (scope: DailyReportScope, signal?: AbortSignal) => request<DailyReportPreview>(scope, '/reports/daily/preview', 'GET', {}, signal)
export const testDailyReport = (scope: DailyReportScope, signal?: AbortSignal) => request<DailyReportTest>(scope, '/reports/daily/test', 'POST', {}, signal)
export const saveStrategyWithDailyReport = (scope: DailyReportScope, strategy: StrategySettings, patch: DailyReportPatch, signal?: AbortSignal) => request<StrategySettings>(scope, '/settings/strategy', 'PUT', { ...strategy, ...patch }, signal)

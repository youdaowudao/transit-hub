import { getAccessToken, handleAuthExpired } from '@/modules/auth/api/auth'

export interface ChanshuiTargetInput { reportId?: string; targetId?: string; groupId?: string; accountName?: string; groupName?: string }
export interface ChanshuiRuntime { enabled: boolean; available: boolean; reason?: string; memoryCeilingBytes?: number }
export class ChanshuiError extends Error {
  constructor(message: string, readonly status = 0, readonly details: Record<string, unknown> = {}) { super(message) }
}
type Params = Record<string, unknown>
const resultContentType = 'application/vnd.transithub.chanshui-result+json'
const maxResultBytes = 16 * 1024 * 1024, maxPacketBytes = Math.ceil(maxResultBytes / 3) * 4 + 64 * 1024, maxMetadataBytes = 64 * 1024
const record = (value: unknown): value is Params => !!value && typeof value === 'object' && !Array.isArray(value)
const resultError = (reason: string) => new ChanshuiError(`检测结果${reason}，本次没有替换页面原有内容。请重新读取并核查保存结果；不要直接重复保存、删除或新建检测。`)
const problemMessage = (problem: Params) => [problem.message, problem.detail].filter((value, index, all) => typeof value === 'string' && value && all.indexOf(value) === index).join('。')

async function readResult(response: Response, image: boolean, onWarning?: (warning: Params) => void, largeResult = false): Promise<unknown> {
  // The base64 body is bounded before JSON parsing; a missing size header must
  // never turn an interrupted result into an unbounded in-memory read.
  const reader = response.body?.getReader()
  if (!reader) throw resultError('传输中缺少内容')
  let packet = '', received = 0
  const decoder = new TextDecoder('utf-8', { fatal: true })
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      received += chunk.value.byteLength
      if (received > (largeResult ? maxPacketBytes : 3 * 1024 * 1024)) { await reader.cancel().catch(() => undefined); throw resultError('超过允许大小') }
      packet += decoder.decode(chunk.value, { stream: true })
    }
    packet += decoder.decode()
  } catch (error) { throw error instanceof ChanshuiError ? error : resultError('传输中断或内容编码损坏，无法确认完整结果') }
  finally { reader.releaseLock() }
  let body: unknown
  try { body = JSON.parse(packet) } catch { throw resultError('传输不完整或内容无法读取') }
  if (!record(body)) throw resultError('内容格式不正确')
  const { payloadBase64, ...metadata } = body
  if (new TextEncoder().encode(JSON.stringify(metadata)).byteLength > maxMetadataBytes) throw resultError('说明内容超过允许大小')
  if (body.v !== 1 || typeof body.length !== 'number' || !Number.isInteger(body.length) || body.length < 0) throw resultError('缺少有效版本或原内容长度')
  const contentLimit = largeResult && !image ? maxResultBytes : 2 * 1024 * 1024
  if (body.length > contentLimit) throw resultError(`原内容超过允许大小（最多${largeResult && !image ? 16 : 2}MiB）`)
  if (body.contentType !== (image ? 'image/png' : 'application/json')) throw resultError('内容类型与本次读取不一致')
  if (body.error !== undefined && (!record(body.error) || typeof body.error.message !== 'string' || !body.error.message.trim())) throw resultError('清理说明缺少具体原因')
  if (body.complete !== true) throw resultError(`未完整传输${record(body.error) ? `：${problemMessage(body.error)}` : '，缺少完成确认'}`)
  if (typeof payloadBase64 !== 'string' || payloadBase64.length > Math.ceil(contentLimit / 3) * 4) throw resultError('编码内容缺失或超过允许大小')
  let binary: string
  try {
    if (payloadBase64.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(payloadBase64)) throw new Error('invalid base64')
    binary = atob(payloadBase64)
    if (btoa(binary) !== payloadBase64) throw new Error('non-canonical base64')
  } catch { throw resultError('编码内容损坏或已截断') }
  if (binary.length !== body.length) throw resultError('实际内容长度与原长度不一致，无法确认完整结果')
  let value: unknown
  if (image) {
    if (!binary.length) throw resultError('图片内容为空')
    value = { dataURL: `data:image/png;base64,${payloadBase64}` }
  } else {
    try { value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(binary, char => char.charCodeAt(0)))) }
    catch { throw resultError('正文无法读取，不能确认返回的保存值') }
  }
  if (record(body.error)) {
    if (!onWarning) throw new ChanshuiError(problemMessage(body.error), response.status, body.error)
    onWarning(body.error)
  }
  return value
}
const key = (value: unknown): string => encodeURIComponent(String(value ?? ''))
const query = (params: Params) => {
  const q = new URLSearchParams()
  for (const [name, value] of Object.entries(params)) if (value !== undefined && value !== null && value !== '') q.set(name, String(value))
  return q.size ? `?${q}` : ''
}
export function chanshuiRequestSpec(op: string, p: Params = {}): { path: string; method: string; body?: unknown; revision?: unknown; image?: boolean } {
  switch (op) {
    case 'runtime': return { path: '/runtime', method: 'GET' }
    case 'reference-info': return { path: '/reference-info', method: 'GET' }
    case 'targets': return { path: '/targets', method: 'GET' }
    case 'context': return { path: `/targets/${key(p.targetId)}/context${query({ groupId: p.groupId })}`, method: 'GET' }
    case 'models': return { path: `/targets/${key(p.targetId)}/models`, method: 'POST', body: { groupId: p.groupId, expectedContext: p.expectedContext } }
    case 'reports': return { path: `/reports${query(p)}`, method: 'GET' }
    case 'report': return { path: `/reports/${key(p.reportId)}`, method: 'GET' }
    case 'cross-check': return { path: `/reports/${key(p.reportId)}/cross-check`, method: 'GET' }
    case 'prefix-prompt': return { path: `/prefix-prompts/${key(p.promptId)}`, method: 'GET' }
    case 'items': return { path: `/reports/${key(p.reportId)}/items${query({ cursor: p.cursor, limit: 5 })}`, method: 'GET' }
    case 'image': return { path: `/reports/${key(p.reportId)}/items/${key(p.itemId)}/image`, method: 'GET', image: true }
    case 'start': return { path: '/reports', method: 'POST', body: p }
    case 'retry': return { path: `/reports/${key(p.reportId)}/retry`, method: 'POST', body: { retryId: p.retryId, expectedRetryId: p.expectedRetryId ?? '', itemIds: p.itemIds } }
    case 'cancel': return { path: `/reports/${key(p.reportId)}/cancel`, method: 'POST', body: { expectedRetryId: p.expectedRetryId ?? '' } }
    case 'settings': return { path: '/settings', method: 'GET' }
    case 'save-settings': return { path: '/settings', method: 'PUT', body: p }
    case 'questions': return { path: `/questions${query(p)}`, method: 'GET' }
    case 'question': return { path: `/questions/${key(p.id)}`, method: 'GET' }
    case 'create-question': return { path: '/questions', method: 'POST', body: p }
    case 'save-question': return { path: `/questions/${key((p.definition as Params | undefined)?.id ?? p.id)}`, method: 'PUT', body: p }
    case 'delete-question': return { path: `/questions/${key(p.id)}`, method: 'DELETE', revision: p.revision }
    case 'preview-question': return { path: '/questions/preview', method: 'POST', body: p }
    case 'bank-info': return { path: '/bank-info', method: 'GET' }
    default: throw new ChanshuiError('此页面请求了未开放的操作，请重新打开检测页。')
  }
}

export async function requestChanshui<T = unknown>(op: string, params: Params, workspaceId: string, signal?: AbortSignal, onWarning?: (warning: Params) => void): Promise<T> {
  if (typeof workspaceId !== 'string' || !workspaceId.trim()) throw new ChanshuiError('检测页面没有绑定工作区，尚未读取或修改任何内容。请重新选择工作区后打开检测页。', 409)
  const spec = chanshuiRequestSpec(op, params)
  const token = getAccessToken()
  if (!token) throw new ChanshuiError('登录已失效，请重新登录后打开检测页。', 401)
  const api = String(import.meta.env.VITE_API_BASE_URL ?? '/api').replace(/\/$/, '')
  let response: Response
  try {
    response = await fetch(`${api}/chanshui${spec.path}`, {
      method: spec.method, cache: 'no-store', signal,
      headers: { Authorization: `Bearer ${token}`, 'X-Chanshui-Workspace': workspaceId, Accept: spec.image ? 'image/png' : 'application/json', ...(spec.body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(spec.revision === undefined ? {} : { 'If-Match': String(spec.revision) }) },
      ...(spec.body === undefined ? {} : { body: JSON.stringify(spec.body) }),
    })
  } catch (error) {
    if (signal?.aborted) throw new ChanshuiError('页面已关闭或已切换工作区，本次读取已结束。')
    throw new ChanshuiError('无法连接调度站。请检查网络后重试读取；若刚点击开始，请按原报告编号查询，部分请求可能已经计费。')
  }
  if (response.status === 401) { handleAuthExpired(); throw new ChanshuiError('登录已失效，请重新登录后查看原报告。', 401) }
  if (response.ok && response.headers.get('Content-Type')?.split(';')[0]?.trim().toLowerCase() === resultContentType) return await readResult(response, spec.image === true, onWarning, op === 'items') as T
  if (spec.image && response.ok) {
    const blob = await response.blob()
    if (blob.size > 2 * 1024 * 1024) throw new ChanshuiError('报告图片超过允许大小，请检查此报告的图片记录。')
    const dataURL = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result)); reader.onerror = reject; reader.readAsDataURL(blob) })
    return { dataURL } as T
  }
  const text = await response.text()
  let body: Record<string, unknown>
  try { body = text ? JSON.parse(text) : {} } catch { throw new ChanshuiError(`调度站返回的内容无法读取（HTTP ${response.status}）。请查看运行诊断后重试读取；不要重复新建检测。`, response.status) }
  if (!response.ok) throw new ChanshuiError(typeof body.message === 'string' ? body.message : `检测操作未完成（HTTP ${response.status}），原因尚未确认；请检查运行诊断后重试。`, response.status, body)
  return body as T
}

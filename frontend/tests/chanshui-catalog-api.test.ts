// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { authTokenStorageKey } from '../src/modules/auth/api/auth'
import { chanshuiRequestSpec, requestChanshui } from '../src/modules/admin/api/chanshui'

beforeEach(() => { localStorage.setItem(authTokenStorageKey, 'catalog-token-a') })
afterEach(() => { localStorage.clear(); vi.unstubAllGlobals() })

it('复用targets路由的明确只读选项，旧targets映射不改', async () => {
  expect(chanshuiRequestSpec('catalog')).toEqual({ path: '/targets?catalog=1', method: 'GET' })
  expect(chanshuiRequestSpec('targets')).toEqual({ path: '/targets', method: 'GET' })
  const fetch = vi.fn(async () => new Response('{"catalog":{"workspaceId":"w","groups":[]}}', { status: 200 }))
  vi.stubGlobal('fetch', fetch)
  await requestChanshui('catalog', {}, 'w')
  expect(fetch.mock.calls[0]![0]).toBe('/api/chanshui/targets?catalog=1')
  expect(new Headers(fetch.mock.calls[0]![1]!.headers).get('X-Chanshui-Workspace')).toBe('w')
})

it.each(['token', 'abort'])('旧目录请求%s变化后收到401，不清除新登录或跳转', async kind => {
  let resolve!: (response: Response) => void
  const fetch = vi.fn(() => new Promise<Response>(r => { resolve = r }))
  vi.stubGlobal('fetch', fetch)
  const controller = new AbortController()
  const pending = requestChanshui('catalog', {}, 'w', controller.signal)
  if (kind === 'token') localStorage.setItem(authTokenStorageKey, 'catalog-token-b')
  else controller.abort()
  resolve(new Response('{"message":"old unauthorized"}', { status: 401 }))
  await expect(pending).rejects.toThrow('读取已取消')
  expect(localStorage.getItem(authTokenStorageKey)).toBe(kind === 'token' ? 'catalog-token-b' : 'catalog-token-a')
})

it.each(['network', 'invalid', 'null', 'unknown', 'workspace'])('名单读取%s错误保留真实影响，不显示计费或报告重试说明', async kind => {
  vi.stubGlobal('fetch', vi.fn(async () => {
    if (kind === 'network') throw new Error('offline')
    if (kind === 'invalid') return new Response('broken', { status: 200 })
    if (kind === 'null') return new Response('null', { status: 503 })
    return new Response(kind === 'workspace' ? '{"message":"工作区不匹配"}' : '{}', { status: kind === 'workspace' ? 409 : 503 })
  }))
  const result = await requestChanshui('catalog', {}, 'w').catch(error => error.message)
  expect(result).toMatch(/名单|分组|工作区/)
  expect(result).not.toMatch(/计费|重复新建|原报告/)
  if (kind === 'workspace') expect(result).toContain('重新选择工作区')
})

it.each(['', ' \n\t'])('名单读取的空白原因%j按未知原因显示核查与重试指引，旧操作保持', async message => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ message }), { status: 503 })))
  const error = await requestChanshui('catalog', {}, 'w').catch(error => error.message)
  expect(error).toContain('读取分组和账号失败')
  expect(error).toContain('原因尚未确认')
  expect(error).toContain('检查后端日志')
  expect(error).not.toMatch(/计费|重复新建|原报告/)
  await expect(requestChanshui('targets', {}, 'w')).rejects.toHaveProperty('message', message)
})

it('批次读取和受理使用专用路径，旧登录迟到401不清除当前登录', async () => {
  const request = { batchId: 'a'.repeat(32), tasks: [] }
  expect(chanshuiRequestSpec('start-batch', request)).toEqual({ path: '/batches', method: 'POST', body: request })
  expect(chanshuiRequestSpec('batch', { batchId: request.batchId, offset: 10, view: 'results', status: 'ended', model: 'A/B' }).path).toEqual(`/batches/${request.batchId}?offset=10&model=A%2FB&status=ended&view=results`)
  expect(chanshuiRequestSpec('recover-runtime')).toEqual({ path: '/runtime/recover', method: 'POST' })
  let resolve!: (response: Response) => void
  const fetch = vi.fn(() => new Promise<Response>(r => { resolve = r }))
  vi.stubGlobal('fetch', fetch)
  const pending = requestChanshui('start-batch', request, 'w')
  localStorage.setItem(authTokenStorageKey, 'catalog-token-b')
  resolve(new Response('{"message":"old unauthorized"}', { status: 401 }))
  await expect(pending).rejects.toMatchObject({ details: { operationResult: 'unknown' } })
  expect(localStorage.getItem(authTokenStorageKey)).toBe('catalog-token-b')
})

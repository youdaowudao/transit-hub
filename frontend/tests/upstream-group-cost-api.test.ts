import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { listUpstreamSites, UpstreamApiError } from '@/modules/admin/api/upstream'

const auth = vi.hoisted(() => ({ expired: vi.fn() }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => 'fixture-auth', handleAuthExpired: auth.expired, authUnauthorizedErrorKey: 'auth.errors.unauthorized', isUnauthorizedApiResponse: (status: number) => status === 401 }))
const fetchMock = vi.fn()
const snapshot = (id = 'site a') => ({ id, metrics: { groups: [] } })
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status })
beforeEach(() => { vi.resetAllMocks(); vi.stubGlobal('fetch', fetchMock) })
afterEach(() => { vi.unstubAllGlobals() })

describe('只读单站费用缓存 API', () => {
  it('保留无参数列表入口；过滤时只请求一个编码后的站点并传递取消信号和禁止缓存', async () => {
    fetchMock.mockResolvedValueOnce(reply([snapshot()])).mockResolvedValueOnce(reply([snapshot()]))
    await listUpstreamSites()
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/upstream-sites')
    const controller = new AbortController()
    expect(await listUpstreamSites('site a', controller.signal)).toEqual([snapshot()])
    expect(fetchMock.mock.calls[1]![0]).toBe('/api/upstream-sites?siteId=site%20a')
    expect(fetchMock.mock.calls[1]![1]).toMatchObject({ signal: controller.signal, cache: 'no-store' })
    expect(fetchMock.mock.calls[1]![1].method).toBeUndefined()
  })
  it.each([{}, [], [snapshot('other')], [snapshot(), snapshot()], [{ id: 'site a' }], [{ id: 'site a', metrics: { groups: {} } }]])('拒绝错误站点或不完整的单项快照 %#', async payload => {
    fetchMock.mockResolvedValue(reply(payload))
    await expect(listUpstreamSites('site a')).rejects.toMatchObject({ failure: { stage: 'metrics', errorDetail: expect.stringContaining('本站缓存返回') } })
  })
  it('404 保留具体安全读取原因，而不是丢失为机器键', async () => {
    const detail = '当前工作区找不到这个站点，本次未读取或修改资料；请返回站点列表核查，不会修改主站'
    fetchMock.mockResolvedValue(reply({ message: 'admin.upstream.errors.notFound', failure: { stage: 'metrics', httpStatus: 404, errorDetail: detail } }, 404))
    await expect(listUpstreamSites('site a')).rejects.toMatchObject({ key: 'admin.upstream.errors.siteNotFound', failure: { errorDetail: detail, httpStatus: 404 } })
  })
  it('503 保留本地缓存读取步骤与原因，不改成保存或上游拒绝', async () => {
    const detail = '读取 Hub 保存的本站资料失败：缓存连接中断；请维护人员检查 Hub 缓存连接和读取日志后重试；不会修改主站'
    fetchMock.mockResolvedValue(reply({ message: 'admin.upstream.errors.request', failure: { stage: 'metrics', errorDetail: detail } }, 503))
    await expect(listUpstreamSites('site a')).rejects.toMatchObject({ failure: { stage: 'metrics', errorDetail: detail } })
  })
  it('保留传输取消，调用者可以区分主动关闭与网络失败', async () => {
    const aborted = new DOMException('cancelled', 'AbortError')
    fetchMock.mockRejectedValue(aborted)
    await expect(listUpstreamSites('site a')).rejects.toBe(aborted)
  })
  it('响应体读取取消也继续上抛', async () => {
    const aborted = new DOMException('cancelled', 'AbortError')
    fetchMock.mockResolvedValue({ text: () => Promise.reject(aborted), ok: true })
    await expect(listUpstreamSites('site a')).rejects.toBe(aborted)
  })
  it('旧异常响应无具体原因时不把响应正文当作提示', async () => {
    fetchMock.mockResolvedValue(new Response('fixture-secret-response', { status: 503 }))
    await expect(listUpstreamSites('site a')).rejects.toEqual(new UpstreamApiError('admin.upstream.errors.request'))
  })
  it('沿用认证过期处理', async () => {
    fetchMock.mockResolvedValue(reply({ message: 'auth.errors.unauthorized' }, 401))
    await expect(listUpstreamSites('site a')).rejects.toThrow('auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledTimes(1)
  })
})

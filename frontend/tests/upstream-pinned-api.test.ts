import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createUpstreamSite, updateUpstreamSitePinned } from '@/modules/admin/api/upstream'
import type { UpstreamSiteForm } from '@/modules/admin/types/upstream'

vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => 'fixture-auth', handleAuthExpired: vi.fn(), authUnauthorizedErrorKey: 'auth.errors.unauthorized', isUnauthorizedApiResponse: (status: number) => status === 401 }))
const fetchMock = vi.fn()
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status })
const form: UpstreamSiteForm = { name: '验收站', siteUrl: 'https://fixture.example.com', platform: 'auto', authMode: 'token', account: '', password: '', accessToken: 'fixture-access', refreshToken: '', tokenType: 'Bearer', userId: '', rechargeRate: 1, remark: '' }

beforeEach(() => { vi.resetAllMocks(); vi.stubGlobal('fetch', fetchMock) })
afterEach(() => { vi.unstubAllGlobals() })

describe('上游站点置顶接口', () => {
  it('置顶开关只发送 PATCH /pinned 和 pinned 值', async () => {
    fetchMock.mockResolvedValue(reply({ id: 'site a', pinned: true }))
    await updateUpstreamSitePinned('site-1', true)
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/upstream-sites/site-1/pinned')
    expect(fetchMock.mock.calls[0]![1]).toMatchObject({ method: 'PATCH', body: JSON.stringify({ pinned: true }) })
  })

  it('新增时只有开启置顶才带 pinned，未开启时请求体与原来一致', async () => {
    fetchMock.mockImplementation(async () => reply({ id: 'site-1' }, 201))
    await createUpstreamSite(form)
    expect(fetchMock.mock.calls[0]![1].body).toBe(JSON.stringify(form))
    await createUpstreamSite(form, true)
    expect(JSON.parse(fetchMock.mock.calls[1]![1].body)).toEqual({ ...form, pinned: true })
  })
})

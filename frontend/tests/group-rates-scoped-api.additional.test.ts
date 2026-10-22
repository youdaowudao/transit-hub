import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { listGroupRates } from '@/modules/admin/api/groupRates'
import { listUpstreamSiteOptions } from '@/modules/admin/api/upstream'
import { checkScopedRealConnections, listScopedRealConnections, ScopedCheckApiError } from '@/modules/admin/api/mySites'
const auth = vi.hoisted(() => ({ expired: vi.fn() }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: auth.expired, isUnauthorizedApiResponse: (status: number, payload: { message?: string }) => status === 401 || payload.message === 'auth.errors.unauthorized', authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const result = () => ({ workspaceAdminAccountId: 'workspace', total: 2, confirmedConnectionIds: [], unconfirmedConnectionIds: ['a', 'b'], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: {}, issues: [], storageUncertain: false })
const request = { workspaceAdminAccountId: 'workspace', mode: 'automatic' as const, siteId: 'site' }
beforeEach(() => { auth.expired.mockReset() })
afterEach(() => vi.unstubAllGlobals())
describe('scoped check API explicit evidence', () => {
  it.each([500, 503])('keeps validated batch evidence from HTTP %s', async status => {
    const evidence = { ...result(), storageUncertain: status === 503 }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...evidence, message: '本地保存未完成', detail: '已知原因 token=fixture-secret', next: '请刷新' }), { status })))
    const error = await checkScopedRealConnections(request).catch(e => e)
    expect(error).toBeInstanceOf(ScopedCheckApiError)
    expect(error.result).toEqual(evidence)
    expect(error.detail).not.toContain('fixture-secret')
    expect(auth.expired).not.toHaveBeenCalled()
  })
  it.each([
    { ...result(), workspaceAdminAccountId: 'other' },
    { ...result(), confirmedConnectionIds: ['a'], unconfirmedConnectionIds: ['a', 'b'], confirmedStatuses: { a: 'active' } },
    { ...result(), total: 3 },
    { ...result(), confirmedConnectionIds: ['a'], unconfirmedConnectionIds: ['b'], confirmedStatuses: {} },
    { ...result(), confirmedStatuses: { unknown: 'active' } },
    { ...result(), total: '2' },
    { ...result(), issues: [{ step: 'save', detail: 'unknown', next: 'refresh', retryAllowed: true, connectionId: 'outside' }] },
    { ...result(), storageUncertain: true, confirmedConnectionIds: ['a'], unconfirmedConnectionIds: ['b'], confirmedStatuses: { a: 'active' } },
  ])('rejects incomplete, contradictory or foreign evidence', async evidence => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(evidence), { status: 503 })))
    const error = await checkScopedRealConnections(request).catch(e => e)
    expect(error).toBeInstanceOf(ScopedCheckApiError)
    expect(error.result).toBeUndefined()
  })
  it('keeps AbortError so an obsolete generation can stop silently', async () => {
    const error = new DOMException('fixture cancelled', 'AbortError')
    vi.stubGlobal('fetch', vi.fn(async () => { throw error }))
    await expect(checkScopedRealConnections(request)).rejects.toBe(error)
  })
  it('expires Hub auth only for Hub HTTP401', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ message: 'auth.errors.unauthorized' }), { status: 401 })))
    await expect(checkScopedRealConnections(request)).rejects.toBeInstanceOf(ScopedCheckApiError)
    expect(auth.expired).toHaveBeenCalledTimes(1)
    auth.expired.mockClear()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...result(), issues: [{ step: 'verify', detail: '主站验证失败', next: '请更新主站登录', retryAllowed: true, httpStatus: 401 }] }))))
    expect((await checkScopedRealConnections(request)).unconfirmedConnectionIds).toEqual(['a', 'b'])
    expect(auth.expired).not.toHaveBeenCalled()
  })
  it('rejects local connection credentials and mismatched workspace', async () => {
    for (const response of [{ workspaceAdminAccountId: 'other', items: [] }, { workspaceAdminAccountId: 'workspace', items: [{ id: 'a', upstreamSiteId: 'site', upstreamGroupId: '1', upstreamGroupName: 'Group', adminAccountId: '101', status: 'active', upstreamKey: 'secret' }] }]) {
      vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(response))))
      await expect(listScopedRealConnections({ workspaceAdminAccountId: 'workspace', groupRefs: [] })).rejects.toBeInstanceOf(Error)
    }
  })
})


const localRead = (kind: string) => kind === 'rates'
  ? listGroupRates({ workspaceAdminAccountId: 'workspace', page: 1, search: '', type: '', platform: '', status: 'all', sort: 'multiplierAsc' })
  : listUpstreamSiteOptions()
describe('scoped local read safe error contracts', () => {
  it.each(['rates', 'options'])('gives a plain reason for non-JSON %s errors', async kind => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>token=fixture-secret</html>', { status: 500 })))
    const error = await localRead(kind).catch(error => error)
    expect(error.message).toContain('无法读取')
    expect(error.message).toContain('Hub后端')
    expect(error.message).not.toContain('fixture-secret')
    expect(error.message).not.toContain('Unexpected')
  })
  it.each(['rates', 'options'])('preserves Hub HTTP401 for malformed %s responses', async kind => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('not JSON', { status: 401 })))
    await expect(localRead(kind)).rejects.toHaveProperty('message', 'auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledTimes(1)
  })
  it.each(['rates', 'options'])('bounds and masks all three %s error fields', async kind => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ message: '说明'.repeat(400), detail: 'https://user:fixture-secret@example.test/read?token=fixture-secret；token=fixture-secret；原因'.repeat(60), next: '处理'.repeat(400) }), { status: 500 })))
    const error = await localRead(kind).catch(error => error)
    expect(error.message.length).toBeLessThanOrEqual(1538)
    expect(error.message).not.toContain('fixture-secret')
    expect(error.message).not.toContain('user:')
    expect(error.message).toContain('https://example.test/read')
  })
  it.each(['rates', 'options'])('keeps a body-read AbortError from %s without showing a new failure', async kind => {
    const abort = new DOMException('fixture cancellation', 'AbortError')
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500, text: async () => { throw abort } })))
    await expect(localRead(kind)).rejects.toBe(abort)
  })
})

it.each(['rates', 'options'])('reports a malformed null %s error body in plain language', async kind => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response('null', { status: 500 })))
  const error = await localRead(kind).catch(error => error)
  expect(error.message).toContain('无法读取')
  expect(error.message).toContain('Hub后端')
  expect(error.message).not.toContain('Cannot read')
})

describe('Review2 scoped Hub401 parsing contract', () => {
  it.each(['check', 'list'])('expires Hub auth after a non-JSON401 from %s', async kind => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>expired</html>', { status: 401 })))
    const read = kind === 'check' ? checkScopedRealConnections(request) : listScopedRealConnections({ workspaceAdminAccountId: 'workspace', groupRefs: [] })
    await expect(read).rejects.toHaveProperty('message', 'auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledTimes(1)
  })
  it.each(['check', 'list'])('expires Hub auth after an empty401 from %s', async kind => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })))
    const read = kind === 'check' ? checkScopedRealConnections(request) : listScopedRealConnections({ workspaceAdminAccountId: 'workspace', groupRefs: [] })
    await expect(read).rejects.toHaveProperty('message', 'auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledTimes(1)
  })
})

it.each(['check', 'list'])('prioritizes a body-read AbortError over Hub401 for scoped %s', async kind => {
  const abort = new DOMException('fixture cancellation', 'AbortError')
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401, text: async () => { throw abort } })))
  const read = kind === 'check' ? checkScopedRealConnections(request) : listScopedRealConnections({ workspaceAdminAccountId: 'workspace', groupRefs: [] })
  await expect(read).rejects.toBe(abort)
  expect(auth.expired).not.toHaveBeenCalled()
})

// Account names are plain display text; error details keep their existing guard.
describe('account-name response contract', () => {
  it.each([undefined, '', '  213真实数字名称  ', '完整名称'.repeat(180) + 'ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890尾部', '<b>纯文本</b>\u0000'])('preserves name presence and full plain text independently: %s', async accountName => {
    const issue = { connectionId: 'a', accountId: '213', step: 'detail', detail: '上游维护 363 token=fixture-secret', next: '请刷新', retryAllowed: true, ...(accountName === undefined ? {} : { accountName }) }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...result(), issues: [issue] }))))
    const actual = (await checkScopedRealConnections(request)).issues[0] as any
    expect(Object.hasOwn(actual, 'accountName')).toBe(accountName !== undefined)
    if (accountName !== undefined) expect(actual.accountName).toBe(accountName.replace(/[\u0000-\u001f\u007f-\u009f]/g, '').trim())
    expect(actual.detail).toContain('上游维护 363')
    expect(actual.detail).not.toContain('fixture-secret')
  })
  it.each([{ accountName: null }, { accountName: 213 }, { accountName: {} }, { accountName: [] }])('rejects a present non-string name without accepting partial evidence: $accountName', async ({ accountName }) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...result(), issues: [{ connectionId: 'a', accountId: '213', accountName, step: 'detail', detail: '原因', next: '请刷新', retryAllowed: true }] }))))
    const error = await checkScopedRealConnections(request).catch(error => error)
    expect(error).toBeInstanceOf(ScopedCheckApiError)
    expect(error.result).toBeUndefined()
  })
})

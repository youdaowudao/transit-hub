import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getActivitySummary, getActivitySync, getActivityUsers, getActivityDetail, startActivitySync } from '@/modules/admin/api/userActivity'
import { getVipSummary, searchVipUsers, watchVipUser, unwatchVipUser, restoreVipRecommendations, saveVipSettings } from '@/modules/admin/api/vip'
import { getSalesSummary, getSalesEntries, excludeSalesEntries, restoreSalesEntries, saveSalesSettings } from '@/modules/admin/api/sales'
import { getOperatingInsights } from '@/modules/admin/api/operatingInsights'
import { getDailyReportPreview, testDailyReport, saveStrategyWithDailyReport } from '@/modules/admin/api/reports'
import { getCredentialsStatus } from '@/modules/admin/api/system'
import { scope, strategy, vipSummary } from './fixtures/upstreamAbsorb'
const auth = vi.hoisted(() => ({ expired: vi.fn() }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => 'fixture-hub-auth', handleAuthExpired: auth.expired, authUnauthorizedErrorKey: 'auth.errors.unauthorized', isUnauthorizedApiResponse: (status: number, payload: { message?: string }) => status === 401 || payload.message === 'auth.errors.unauthorized' }))
const fetchMock = vi.fn()
const reply = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status })
const reads = [ ['activity', () => getActivitySummary(scope)], ['sync', () => getActivitySync(scope)], ['vip', () => getVipSummary(scope)], ['sales', () => getSalesSummary(scope, { days: 30 })], ['insights', () => getOperatingInsights(scope)], ['reports', () => getDailyReportPreview(scope)] ] as const
beforeEach(() => { vi.resetAllMocks(); vi.stubGlobal('fetch', fetchMock) })
afterEach(() => { vi.unstubAllGlobals() })

describe.each(reads)('%s API scoped read', (_name, read) => {
  it('keeps the original ID/timestamp and Hub authentication', async () => {
    fetchMock.mockImplementation(async () => reply({ ...scope }))
    await read()
    const [url, options] = fetchMock.mock.calls[0]!
    const query = new URL(url, 'https://fixture.test').searchParams
    expect(query.get('workspaceAdminAccountId')).toBe(scope.workspaceAdminAccountId)
    expect(query.get('workspaceCreatedAt')).toBe(scope.workspaceCreatedAt)
    expect(options.headers.Authorization).toBe('Bearer fixture-hub-auth')
    expect(options.method ?? 'GET').toBe('GET')
  })
  it.each([null, [], ['unexpected'], 'wrong'])('rejects a non-object reply %# with a human message', async value => {
    fetchMock.mockResolvedValue(reply(value))
    await expect(read()).rejects.toThrow(/完整对象|完整JSON|资料/)
  })
  it('rejects malformed JSON without exposing the response', async () => {
    fetchMock.mockResolvedValue(new Response('fixture-private-response'))
    const error = await read().catch(cause => cause)
    expect(error.message).toMatch(/JSON/)
    expect(error.message).not.toContain('fixture-private-response')
  })
  it('rejects both mismatched scope values', async () => {
    for (const patch of [{ workspaceAdminAccountId: 'other' }, { workspaceCreatedAt: '2026-01-01T00:00:00Z' }]) {
      fetchMock.mockResolvedValue(reply({ ...scope, ...patch }))
      await expect(read()).rejects.toThrow(/工作区不一致|工作区/)
    }
  })
  it('does not expire Hub auth for a 502 main-site auth key', async () => {
    fetchMock.mockResolvedValue(reply({ message: 'auth.errors.unauthorized' }, 502))
    await expect(read()).rejects.toThrow()
    expect(auth.expired).not.toHaveBeenCalled()
  })
  it('expires Hub authentication for its own HTTP 401', async () => {
    fetchMock.mockResolvedValue(reply({ message: 'auth.errors.unauthorized' }, 401))
    await expect(read()).rejects.toThrow('auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledOnce()
  })
  it('preserves fetch and response-body cancellation', async () => {
    const aborted = new DOMException('fixture canceled', 'AbortError')
    fetchMock.mockRejectedValueOnce(aborted)
    await expect(read()).rejects.toBe(aborted)
    fetchMock.mockResolvedValue({ ok: true, status: 200, json: () => Promise.reject(aborted) })
    await expect(read()).rejects.toBe(aborted)
  })
  it('does not turn an invalid error message into an object string', async () => {
    fetchMock.mockResolvedValue(reply({ message: { private: 'fixture-secret' } }, 503))
    const error = await read().catch(cause => cause)
    expect(error).toBeInstanceOf(Error)
    expect(error.message).toMatch(/失败|未完成|读取/)
    expect(error.message).not.toMatch(/\[object Object\]|fixture-secret/)
  })
})

it('writes only whitelisted operation DTOs with exact scope; excludes amounts and ranges', async () => {
  fetchMock.mockImplementation(async () => reply({ ...scope }))
  const keys = [{ siteUserId: 'user/1', day: '2026-11-19', channel: 'payment' as const }]
  const calls = [
    () => startActivitySync(scope), () => watchVipUser(scope, 'user/1', 'note'), () => unwatchVipUser(scope, 'user/1'), () => restoreVipRecommendations(scope),
    () => saveVipSettings(scope, vipSummary().settings), () => excludeSalesEntries(scope, keys), () => restoreSalesEntries(scope, keys), () => saveSalesSettings(scope, { excludedChannels: ['admin_add'] }),
    () => testDailyReport(scope), () => saveStrategyWithDailyReport(scope, strategy(), { dailyReportEnabled: false, dailyReportBotIds: [] }),
  ]
  for (const call of calls) await call()
  for (const [, options] of fetchMock.mock.calls) expect(JSON.parse(options.body)).toMatchObject(scope)
  expect(JSON.parse(fetchMock.mock.calls[5]![1].body)).toEqual({ ...scope, entries: keys })
  expect(JSON.parse(fetchMock.mock.calls[6]![1].body)).toEqual({ ...scope, entries: keys })
  expect(JSON.parse(fetchMock.mock.calls[8]![1].body)).toEqual(scope)
  expect(JSON.parse(fetchMock.mock.calls[9]![1].body)).not.toHaveProperty('dailyReportTime')
})
it('passes every local filter, encodes customer IDs and retains cancellation', async () => {
  fetchMock.mockImplementation(async () => reply({ ...scope }))
  const signal = new AbortController().signal
  const query = { page: 2, pageSize: 100, segment: 'inactive', inactiveDays: 45, search: 'somebody', role: 'user', status: 'disabled', sort: 'cost30d', order: 'asc' }
  await getActivityUsers(scope, query, signal)
  const params = new URL(fetchMock.mock.calls[0]![0], 'https://fixture.test').searchParams
  for (const [key, value] of Object.entries(query)) expect(params.get(key)).toBe(String(value))
  expect(fetchMock.mock.calls[0]![1].signal).toBe(signal)
  await getActivityDetail(scope, 'id/with space', signal)
  expect(fetchMock.mock.calls[1]![0]).toContain('/users/id%2Fwith%20space?')
  await searchVipUsers(scope, 'term', signal)
  await getSalesEntries(scope, { from: '2026-11-01', to: '2026-11-19' }, { page: 3, pageSize: 50, channel: 'admin_deduct', q: 'payer', excluded: '1' }, signal)
  const last = new URL(fetchMock.mock.calls.at(-1)![0], 'https://fixture.test').searchParams
  expect(Object.fromEntries(last)).toMatchObject({ page: '3', pageSize: '50', channel: 'admin_deduct', q: 'payer', excluded: '1' })
})

describe('credential status read protocol', () => {
  it('uses only GET without key material', async () => {
    fetchMock.mockResolvedValue(reply({ configured: false, mode: 'legacy', message: '管理凭据仍按原方式保存。' }))
    await getCredentialsStatus()
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/system/credentials/status')
    expect(fetchMock.mock.calls[0]![1].method ?? 'GET').toBe('GET')
    expect(fetchMock.mock.calls[0]![1].body).toBeUndefined()
  })
  it.each([null, [], {}, { configured: 'false', mode: 'legacy', message: 'bad' }, { configured: false, mode: 'other', message: 'bad' }, { configured: false, mode: ['legacy'], message: 'bad' }, { configured: false, mode: { toString: 'bad' }, message: 'bad' }])('rejects a non-object status %#', async payload => {
    fetchMock.mockResolvedValue(reply(payload))
    await expect(getCredentialsStatus()).rejects.toThrow(/资料|格式|完整/)
  })
})

it('credential status preserves AbortError, rejects malformed JSON and never logs out Hub for an upstream 502', async () => {
  const aborted = new DOMException('Canceled', 'AbortError')
  fetchMock.mockRejectedValueOnce(aborted)
  await expect(getCredentialsStatus()).rejects.toBe(aborted)
  fetchMock.mockResolvedValueOnce(new Response('fixture-invalid-json'))
  await expect(getCredentialsStatus()).rejects.toThrow(/JSON/)
  fetchMock.mockResolvedValueOnce(reply({ message: 'auth.errors.unauthorized' }, 502))
  await expect(getCredentialsStatus()).rejects.toThrow()
  expect(auth.expired).not.toHaveBeenCalled()
  fetchMock.mockResolvedValueOnce(reply({ message: 'auth.errors.unauthorized' }, 401))
  await expect(getCredentialsStatus()).rejects.toThrow('auth.errors.unauthorized')
  expect(auth.expired).toHaveBeenCalledOnce()
})

it('keeps a sparse report strategy patch sparse on the wire', async () => {
  fetchMock.mockImplementation(async () => reply({ ...scope }))
  await saveStrategyWithDailyReport(scope, {}, { dailyReportTime: '09:30' })
  expect(JSON.parse(fetchMock.mock.calls[0]![1].body)).toEqual({ ...scope, dailyReportTime: '09:30' })
  await saveStrategyWithDailyReport(scope, { refreshInterval: 120 }, { dailyReportEnabled: false })
  expect(JSON.parse(fetchMock.mock.calls[1]![1].body)).toEqual({ ...scope, refreshInterval: 120, dailyReportEnabled: false })
})

it.each(['all', 'active_1d', 'active_7d', 'active_30d', 'churned_7d', 'declining', 'growing'])('does not send hidden inactiveDays for the %s segment', async segment => {
  fetchMock.mockImplementation(async () => reply({ ...scope }))
  await getActivityUsers(scope, { page: 1, pageSize: 20, segment, inactiveDays: Number.NaN, search: '', role: '', status: '', sort: 'lastUsedAt', order: 'desc' })
  const query = new URL(fetchMock.mock.calls[0]![0], 'https://fixture.test').searchParams
  expect(query.has('inactiveDays')).toBe(false)
  expect(query.get('segment')).toBe(segment)
  expect(query.get('page')).toBe('1')
  expect(query.get('pageSize')).toBe('20')
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getSignupRiskPreferences, saveSignupRiskPreferences, getNotificationSummary, NotificationRequestError } from '@/modules/admin/api/notifications'

vi.mock('@/modules/auth/api/auth', async original => ({ ...await original<typeof import('@/modules/auth/api/auth')>(), getAccessToken: () => null, handleAuthExpired: vi.fn() }))
const when = '2031-01-01T00:00:00Z'
const fetchMock = vi.fn()
const schedule = { workStart: '09:00', workEnd: '18:00', timeZone: 'Asia/Singapore' }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body && typeof body === 'object' && 'initialized' in body ? { ...schedule, serverTime: when, activeUntil: 'enabled' in body && body.enabled ? '2031-01-01T10:00:00Z' : null, nextStartAt: 'enabled' in body && body.enabled ? '2031-01-02T01:00:00Z' : null, ...body } : body), { status, headers: { 'Content-Type': 'application/json' } })
beforeEach(() => { vi.stubGlobal('fetch', fetchMock); fetchMock.mockReset() })
afterEach(() => vi.unstubAllGlobals())

describe('signup risk preference wire contract', () => {
  it('reads and saves only the explicit workspace and complete working-time configuration', async () => {
    fetchMock.mockResolvedValueOnce(response({ workspaceAdminAccountId: 'a & b', enabled: false, initialized: false, updatedAt: null }))
    expect((await getSignupRiskPreferences('a & b')).enabled).toBe(false)
    expect(fetchMock.mock.calls[0]![0]).toContain('workspaceAdminAccountId=a+%26+b')
    fetchMock.mockResolvedValueOnce(response({ workspaceAdminAccountId: 'a', enabled: true, initialized: false, updatedAt: when }))
    await saveSignupRiskPreferences({ workspaceAdminAccountId: 'a', enabled: true, ...schedule })
    expect(fetchMock.mock.calls[1]![1]).toEqual(expect.objectContaining({ method: 'PUT', body: JSON.stringify({ workspaceAdminAccountId: 'a', enabled: true, ...schedule }) }))
  })
  it.each([
    { workspaceAdminAccountId: 'b', enabled: false, initialized: false, updatedAt: null },
    { workspaceAdminAccountId: 'a', enabled: 'false', initialized: false, updatedAt: null },
    { workspaceAdminAccountId: 'a', enabled: false, initialized: true, updatedAt: null },
    { workspaceAdminAccountId: 'a', enabled: false, initialized: false },
    { workspaceAdminAccountId: 'a', enabled: true, initialized: false, updatedAt: 'unknown' },
  ])('never treats malformed or cross-workspace state as a disabled default', async body => {
    fetchMock.mockResolvedValueOnce(response(body))
    await expect(getSignupRiskPreferences('a')).rejects.toBeInstanceOf(NotificationRequestError)
  })
  it.each([
    { workspaceAdminAccountId: 'a', enabled: false, initialized: false, updatedAt: when },
    { workspaceAdminAccountId: 'a', enabled: true, initialized: false, updatedAt: null },
  ])('does not declare an unconfirmed save successful', async body => {
    fetchMock.mockResolvedValueOnce(response(body))
    await expect(saveSignupRiskPreferences({ workspaceAdminAccountId: 'a', enabled: true, ...schedule })).rejects.toBeInstanceOf(NotificationRequestError)
  })
  it('preserves the direct safe failure cause and retry instruction', async () => {
    fetchMock.mockResolvedValueOnce(response({ workspaceAdminAccountId: 'a', message: '保存高风险注册提醒失败', detail: '数据库正在处理冲突操作；本次结果未确认', next: '请重读当前工作区后重试', retryable: true }, 503))
    await expect(saveSignupRiskPreferences({ workspaceAdminAccountId: 'a', enabled: true, ...schedule })).rejects.toThrow('数据库正在处理冲突操作')
  })
})

describe('signup risk supplemental summary projection', () => {
  const riskItem = () => ({ id: 'risk', adminAccountId: 'a', kind: 'sub2api.signup_risk', subjectType: 'admin_account', subjectId: 'a', subjectActive: true, subjectInactiveMessage: '', level: 3, discoveredAt: when, createdAt: when, readAt: null, reminderCompletedAt: null, content: { title: 'Sub2API 高风险注册提醒', summary: '只记录提醒，不会修改主站', details: [] } })
  const body = (item = riskItem()) => ({ workspaceAdminAccountId: 'a', serverTime: when, intervalSeconds: 300, unreadCount: 1, pendingCriticalCount: 0, active: { attention: { items: [], totalCount: 0, nextCursor: null }, important: { items: [], totalCount: 1, nextCursor: null }, critical: { items: [], totalCount: 0, nextCursor: null } }, displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] }, signupRisk: { enabled: true, items: [item], activeUntil: '2031-01-01T10:00:00Z', nextStartAt: '2031-01-02T01:00:00Z' } })
  it('reads a separate pending risk group without changing the normal important page', async () => {
    fetchMock.mockResolvedValueOnce(response(body()))
    const result = await getNotificationSummary({ workspaceAdminAccountId: 'a' })
    expect(result.signupRisk?.items.map(item => item.id)).toEqual(['risk'])
    expect(result.active.important.items).toEqual([])
    expect(result.intervalSeconds).toBe(300)
  })
  it.each([{ kind: 'fixture.fact' }, { level: 4 }, { adminAccountId: 'b' }, { subjectActive: false, subjectInactiveMessage: '已停止' }, { reminderCompletedAt: when }])('rejects contradictory risk state %j', async changes => {
    fetchMock.mockResolvedValueOnce(response(body({ ...riskItem(), ...changes })))
    await expect(getNotificationSummary({ workspaceAdminAccountId: 'a' })).rejects.toBeInstanceOf(NotificationRequestError)
  })
})

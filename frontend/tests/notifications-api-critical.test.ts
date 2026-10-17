import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  getNotificationSummary, listNotifications, getNotificationDetail,
  saveNotificationProgress, getNotificationPreferences, saveNotificationPreferences,
} from '@/modules/admin/api/notifications'

const auth = vi.hoisted(() => ({ expired: vi.fn() }))
vi.mock('@/modules/auth/api/auth', () => ({
  getAccessToken: () => null, handleAuthExpired: auth.expired,
  isUnauthorizedApiResponse: (status: number, payload: { message?: string }) => status === 401 || payload.message === 'auth.errors.unauthorized',
  authUnauthorizedErrorKey: 'auth.errors.unauthorized',
}))

const workspaceAdminAccountId = 'workspace-a'
const when = '2030-01-01T00:00:00Z'
const notice = (id = 'notice-a', level = 4) => ({
  id, adminAccountId: workspaceAdminAccountId, kind: 'fixture.fact', subjectType: 'site', subjectId: 'site-a', subjectActive: true, subjectInactiveMessage: '', level,
  discoveredAt: when, createdAt: when, content: { title: '网站倍率发生变化', summary: '请查看详细信息', details: [{ label: '原倍率', value: '1' }], action: { kind: 'group_rates', siteId: 'site-a', focusGroupId: '7' } },
  readAt: null as string | null, reminderCompletedAt: null as string | null,
})
const bucket = (items: ReturnType<typeof notice>[] = []) => ({ items, totalCount: items.length, nextCursor: null })
const summary = () => ({
  workspaceAdminAccountId, serverTime: when, intervalSeconds: 300, unreadCount: 1, pendingCriticalCount: 1,
  active: { attention: bucket(), important: bucket(), critical: bucket([notice()]) },
  displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] },
})
const respond = (payload: unknown, status = 200) => {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify(payload), { status }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
beforeEach(() => auth.expired.mockReset())
afterEach(() => vi.unstubAllGlobals())

describe('notification API scoped complete decoding', () => {
  it('sends explicit workspace and abort signal, keeping read urgent pending', async () => {
    const body = summary()
    body.active.critical.items[0]!.readAt = when
    const fetchMock = respond(body)
    const controller = new AbortController()
    const result = await getNotificationSummary({ workspaceAdminAccountId, criticalCursor: 'cursor-a' }, controller.signal)
    expect(result.active.critical.items[0]!.readAt).toBe(when)
    expect(result.active.critical.items[0]!.reminderCompletedAt).toBeNull()
    expect(result.pendingCriticalCount).toBe(1)
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('workspaceAdminAccountId=workspace-a')
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain('criticalCursor=cursor-a')
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ signal: controller.signal })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('preserves acquired messages when interval and sampling are unknown', async () => {
    respond({ ...summary(), intervalSeconds: null, preferenceWarning: { message: '间隔读取失败', detail: '数据库存储表缺少必要结构', next: '请维护人员检查启动迁移' }, displayedStates: [], samplingState: { autoSyncEnabled: null, unconfirmedSiteCount: null, sites: [], warning: { message: '状态读取失败', detail: '数据库存储表缺少必要结构', next: '请维护人员检查' } } })
    const result = await getNotificationSummary({ workspaceAdminAccountId })
    expect(result.intervalSeconds).toBeNull()
    expect(result.active.critical.items).toHaveLength(1)
    expect(result.preferenceWarning?.detail).toContain('必要结构')
    expect(result.samplingState.autoSyncEnabled).toBeNull()
  })

  it.each([
    () => ({ ...summary(), workspaceAdminAccountId: 'workspace-b' }),
    () => ({ ...summary(), unreadCount: -1 }),
    () => ({ ...summary(), pendingCriticalCount: '1' }),
    () => ({ ...summary(), serverTime: 'not-a-time' }),
    () => ({ ...summary(), intervalSeconds: 61 }),
    () => ({ ...summary(), intervalSeconds: null }),
    () => ({ ...summary(), active: { ...summary().active, critical: bucket([{ ...notice(), adminAccountId: 'workspace-b' }]) } }),
    () => ({ ...summary(), active: { ...summary().active, critical: bucket([{ ...notice(), level: 3 }]) } }),
    () => ({ ...summary(), active: { ...summary().active, critical: bucket(Array.from({ length: 21 }, (_, i) => notice(`notice-${i}`))) } }),
    () => ({ ...summary(), active: { ...summary().active, critical: { ...bucket([notice()]), totalCount: 0 } } }),
    () => ({ ...summary(), active: { ...summary().active, critical: bucket([{ ...notice(), content: { ...notice().content, details: null } } as unknown as ReturnType<typeof notice>]) } }),
    () => ({ ...summary(), displayedStates: [], samplingState: { autoSyncEnabled: 'false', unconfirmedSiteCount: 0, sites: [] } }),
  ])('rejects incomplete, contradictory or foreign summary evidence', async makeBody => {
    respond(makeBody())
    await expect(getNotificationSummary({ workspaceAdminAccountId })).rejects.toBeInstanceOf(Error)
    expect(auth.expired).not.toHaveBeenCalled()
  })

  it('decodes history and deleted destination detail without marking progress', async () => {
    const fetchMock = respond({ workspaceAdminAccountId, items: [notice()], nextCursor: 'history-next' })
    expect((await listNotifications({ workspaceAdminAccountId, limit: 20, filter: 'pending' })).nextCursor).toBe('history-next')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const detailFetch = respond({ workspaceAdminAccountId, item: notice(), checkAction: { available: false, message: '相关网站已删除，这条提醒保留当时信息' } })
    const result = await getNotificationDetail(workspaceAdminAccountId, 'notice-a')
    expect(result.item.readAt).toBeNull()
    expect(result.checkAction?.available).toBe(false)
    expect(detailFetch).toHaveBeenCalledTimes(1)
    expect(detailFetch.mock.calls[0]?.[1]).not.toMatchObject({ method: 'POST' })
  })

  it('rejects a detail for a different requested ID and a foreign history item', async () => {
    respond({ workspaceAdminAccountId, item: notice('other-notice') })
    await expect(getNotificationDetail(workspaceAdminAccountId, 'notice-a')).rejects.toBeInstanceOf(Error)
    respond({ workspaceAdminAccountId, items: [{ ...notice(), adminAccountId: 'other-workspace' }], nextCursor: null })
    await expect(listNotifications({ workspaceAdminAccountId })).rejects.toBeInstanceOf(Error)
  })

  it('accepts only progress for exactly the submitted IDs', async () => {
    const request = { workspaceAdminAccountId, ids: ['notice-a'], action: 'ack' as const }
    respond({ workspaceAdminAccountId, items: [{ id: 'notice-a', readAt: when, reminderCompletedAt: when }] })
    expect((await saveNotificationProgress(request)).items[0]!.reminderCompletedAt).toBe(when)
    for (const items of [[], [{ id: 'other', readAt: when, reminderCompletedAt: when }], [{ id: 'notice-a', readAt: when, reminderCompletedAt: when }, { id: 'notice-a', readAt: when, reminderCompletedAt: when }], [{ id: 'notice-a', readAt: 'bad-time', reminderCompletedAt: when }]]) {
      respond({ workspaceAdminAccountId, items })
      await expect(saveNotificationProgress(request)).rejects.toBeInstanceOf(Error)
    }
  })

  it('keeps confirmed workspace preference and rejects an unrelated or invalid save result', async () => {
    respond({ workspaceAdminAccountId, intervalSeconds: 300, updatedAt: null })
    expect((await getNotificationPreferences(workspaceAdminAccountId)).intervalSeconds).toBe(300)
    respond({ workspaceAdminAccountId, intervalSeconds: 420, updatedAt: when })
    expect((await saveNotificationPreferences({ workspaceAdminAccountId, intervalMinutes: 7 })).intervalSeconds).toBe(420)
    for (const body of [{ workspaceAdminAccountId: 'other', intervalSeconds: 420, updatedAt: when }, { workspaceAdminAccountId, intervalSeconds: 61, updatedAt: when }, { workspaceAdminAccountId, intervalSeconds: 300, updatedAt: when }]) {
      respond(body)
      await expect(saveNotificationPreferences({ workspaceAdminAccountId, intervalMinutes: 7 })).rejects.toBeInstanceOf(Error)
    }
  })
})

const calls = [
  () => getNotificationSummary({ workspaceAdminAccountId }),
  () => listNotifications({ workspaceAdminAccountId }),
  () => getNotificationDetail(workspaceAdminAccountId, 'notice-a'),
  () => saveNotificationProgress({ workspaceAdminAccountId, ids: ['notice-a'], action: 'ack' }),
  () => getNotificationPreferences(workspaceAdminAccountId),
  () => saveNotificationPreferences({ workspaceAdminAccountId, intervalMinutes: 7 }),
]

describe('all notification API errors and cancellation', () => {
  it.each(calls)('preserves detailed safe errors and uncertain commit guidance', async call => {
    respond({ workspaceAdminAccountId, message: '无法确认通知状态是否已经保存', detail: '提交时连接中断 password=fixture-secret', next: '请重新读取同一条消息后，再使用相同操作重试', retryable: true }, 503)
    const error = await call().catch(error => error)
    expect(error.status).toBe(503)
    expect(error.workspaceAdminAccountId).toBe(workspaceAdminAccountId)
    expect(error.message).toContain('无法确认')
    expect(error.message).toContain('同一条消息')
    expect(error.detail).toContain('连接中断')
    expect(error.message).not.toContain('fixture-secret')
    expect(error.retryable).toBe(true)
    expect(auth.expired).not.toHaveBeenCalled()
  })
  it.each(calls)('preserves AbortError from fetch and response body', async call => {
    const abort = new DOMException('fixture cancellation', 'AbortError')
    vi.stubGlobal('fetch', vi.fn(async () => { throw abort }))
    await expect(call()).rejects.toBe(abort)
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401, text: async () => { throw abort } })))
    await expect(call()).rejects.toBe(abort)
    expect(auth.expired).not.toHaveBeenCalled()
  })
  it.each(calls)('uses existing Hub401 expiry for non-JSON response', async call => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>expired</html>', { status: 401 })))
    await expect(call()).rejects.toHaveProperty('message', 'auth.errors.unauthorized')
    expect(auth.expired).toHaveBeenCalledTimes(1)
  })
  it.each(calls)('reports malformed non-401 responses in plain language', async call => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>password=fixture-secret</html>', { status: 503 })))
    const error = await call().catch(error => error)
    expect(error.message).not.toContain('Unexpected')
    expect(error.message).not.toContain('fixture-secret')
    expect(error.message).toMatch(/读取|保存|检查|返回/)
    expect(auth.expired).not.toHaveBeenCalled()
  })
})

describe('site lifecycle displayed summary is a read operation', () => {
  it('posts explicit displayed IDs and decodes their eligibility without marking progress', async () => {
    const body=summary()
    const fetchMock=respond({...body,active:{...body.active,critical:bucket([{...notice(),subjectActive:true,subjectInactiveMessage:''}])},displayedStates:[{id:'notice-a',subjectActive:true,subjectInactiveMessage:'',readAt:null,reminderCompletedAt:null}]})
    const query={workspaceAdminAccountId,displayedIds:['notice-a']}
    const result=await getNotificationSummary(query)
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({method:'POST'})
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toMatchObject(query)
    expect((result as unknown as {displayedStates:unknown[]}).displayedStates).toHaveLength(1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it('describes a displayed-summary network failure as reading, never an unknown save', async()=>{
    vi.stubGlobal('fetch',vi.fn().mockRejectedValue(new TypeError('network unavailable')))
    await expect(getNotificationSummary({workspaceAdminAccountId,displayedIds:['notice-a']})).rejects.toMatchObject({detail:expect.stringContaining('读取')})
  })
})

describe('notification lifecycle projection validation', () => {
  const state = (id: string) => ({ id, subjectActive: true, subjectInactiveMessage: '', readAt: null, reminderCompletedAt: null })
  it.each([
    [], [state('extra')], [state('a'), state('a')], [state('a'), state('b')],
    [{ ...state('a'), subjectActive: 'true' }], [{ ...state('a'), subjectActive: false }],
    [{ ...state('a'), subjectInactiveMessage: 'contradictory reason' }], [{ ...state('a'), readAt: 'bad date' }],
  ].map(states => ({ states })))('rejects missing, extra, duplicate and contradictory displayed evidence %#', async ({ states }) => {
    respond({ ...summary(), displayedStates: states })
    await expect(getNotificationSummary({ workspaceAdminAccountId, displayedIds: ['a'] })).rejects.toBeInstanceOf(Error)
  })
  it('requires the displayed result to match each requested ID in order', async () => {
    respond({ ...summary(), displayedStates: [state('b'), state('a')] })
    await expect(getNotificationSummary({ workspaceAdminAccountId, displayedIds: ['a', 'b'] })).rejects.toBeInstanceOf(Error)
  })
  it('accepts 60 IDs but rejects oversized, duplicate or invalid request IDs without sending', async () => {
    const ids = Array.from({ length: 60 }, (_, i) => `displayed-${i}`)
    const fetchMock = respond({ ...summary(), displayedStates: ids.map(state) })
    expect((await getNotificationSummary({ workspaceAdminAccountId, displayedIds: ids })).displayedStates).toHaveLength(60)
    for (const invalid of [[...ids, 'sixty-one'], ['same', 'same'], [''], ['x'.repeat(513)]]) {
      await expect(getNotificationSummary({ workspaceAdminAccountId, displayedIds: invalid })).rejects.toBeInstanceOf(Error)
    }
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each([
    { subjectActive: false, subjectInactiveMessage: '这个站点已停止管理' },
    { subjectActive: true, subjectInactiveMessage: 'contradictory reason' },
    { subjectActive: undefined, subjectInactiveMessage: '' },
  ])('rejects inactive or incomplete messages in automatic buckets %#', async projection => {
    respond({ ...summary(), active: { ...summary().active, critical: bucket([{ ...notice(), ...projection }]) } })
    await expect(getNotificationSummary({ workspaceAdminAccountId })).rejects.toBeInstanceOf(Error)
  })
  it('retains inactive history and detail while rejecting it in unread and pending filters', async () => {
    const inactive = { ...notice(), subjectActive: false, subjectInactiveMessage: '这个站点已停止管理，这条消息保留为历史，不再主动提醒。' }
    respond({ workspaceAdminAccountId, items: [inactive], nextCursor: null })
    expect((await listNotifications({ workspaceAdminAccountId, filter: 'all' })).items[0]?.subjectActive).toBe(false)
    for (const filter of ['unread', 'pending'] as const) await expect(listNotifications({ workspaceAdminAccountId, filter })).rejects.toBeInstanceOf(Error)
    respond({ workspaceAdminAccountId, item: inactive })
    expect((await getNotificationDetail(workspaceAdminAccountId, inactive.id)).item.subjectInactiveMessage).toContain('不再主动提醒')
  })
  it('keeps read-only wording for malformed and HTTP-failed POST summaries', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('not JSON', { status: 503 })))
    const malformed = await getNotificationSummary({ workspaceAdminAccountId, displayedIds: ['a'] }).catch(error => error)
    expect(malformed.detail).toContain('没有修改消息进度')
    expect(malformed.detail).not.toContain('是否已经保存')
    respond({}, 503)
    const failed = await getNotificationSummary({ workspaceAdminAccountId, displayedIds: ['a'] }).catch(error => error)
    expect(failed.detail).toContain('读取没有完成')
    expect(failed.detail).not.toContain('保存成功')
  })
})


describe('summary evidence from the same read', () => {
  it('rejects contradictory eligibility or progress between a displayed ID and its summary item', async () => {
    for (const projected of [
      { id: 'notice-a', subjectActive: false, subjectInactiveMessage: '已停止管理', readAt: null, reminderCompletedAt: null },
      { id: 'notice-a', subjectActive: true, subjectInactiveMessage: '', readAt: when, reminderCompletedAt: null },
    ]) {
      respond({ ...summary(), displayedStates: [projected] })
      await expect(getNotificationSummary({ workspaceAdminAccountId, displayedIds: ['notice-a'] })).rejects.toBeInstanceOf(Error)
    }
  })
})

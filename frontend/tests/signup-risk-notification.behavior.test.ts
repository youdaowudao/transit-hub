// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SignupRiskNotificationSettings from '@/modules/admin/components/settings/SignupRiskNotificationSettings.vue'
import NotificationSummary from '@/modules/admin/components/notifications/NotificationSummary.vue'
import { useNotifications, type NotificationController } from '@/modules/admin/composables/useNotifications'
import { NotificationRequestError } from '@/modules/admin/api/notifications'
import type { NotificationSummaryResponse, SignupRiskPreferences, SiteNotification } from '@/modules/admin/types/notifications'

const api = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), summary: vi.fn(), progress: vi.fn() }))
vi.mock('@/modules/admin/api/notifications', async original => ({ ...await original<typeof import('@/modules/admin/api/notifications')>(), getSignupRiskPreferences: api.get, saveSignupRiskPreferences: api.save, getNotificationSummary: api.summary, saveNotificationProgress: api.progress }))
const when = '2031-01-01T00:00:00Z'
const pref = (enabled = false, workspace = 'a'): SignupRiskPreferences => ({ workspaceAdminAccountId: workspace, enabled, initialized: false, updatedAt: when, workStart: '09:00', workEnd: '18:00', timeZone: 'Asia/Singapore', serverTime: when, activeUntil: enabled ? '2031-01-01T10:00:00Z' : null, nextStartAt: enabled ? '2031-01-02T01:00:00Z' : null })
const pending = <T,>() => { let resolve!: (value: T) => void; let reject!: (value: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const wrappers: VueWrapper[] = []
const settings = () => { const w = mount(SignupRiskNotificationSettings, { props: { workspaceId: 'a', platform: 'sub2api' } }); wrappers.push(w); return w }
const button = (w: VueWrapper, label: string) => w.findAll('button').find(item => item.text() === label)!
const fact = (id: string, risk = false, level: 3 | 4 = 3): SiteNotification => ({ id, adminAccountId: 'a', kind: risk ? 'sub2api.signup_risk' : 'fixture.fact', subjectType: 'admin_account', subjectId: 'a', subjectActive: true, subjectInactiveMessage: '', level, discoveredAt: when, createdAt: when, readAt: null, reminderCompletedAt: null, content: { title: id, summary: risk ? 'alice+1@example.com' : '原消息', details: [] } })
const snapshot = (risk: SiteNotification[] = [], normal: SiteNotification[] = [], enabled = true): NotificationSummaryResponse => {
  const all = [...risk, ...normal]
  const bucket = (level: number) => ({ items: all.filter(item => item.level === level).slice(0, 20), totalCount: all.filter(item => item.level === level).length, nextCursor: null })
  return { workspaceAdminAccountId: 'a', serverTime: when, intervalSeconds: 300, unreadCount: all.length, pendingCriticalCount: bucket(4).totalCount, active: { attention: { items: [], totalCount: 0, nextCursor: null }, important: bucket(3), critical: bucket(4) }, samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] }, displayedStates: [], signupRisk: { enabled, items: risk, activeUntil: enabled ? "2031-01-01T10:00:00Z" : null, nextStartAt: enabled ? "2031-01-02T01:00:00Z" : null } }
}
function controller() {
  let value!: NotificationController
  const workspaceId = ref('a'), isSwitching = ref(false)
  const w = mount(defineComponent({ setup() { value = useNotifications({ workspaceId, isSwitching }); return () => h(NotificationSummary, { controller: value }) } }))
  wrappers.push(w); return { value, w, workspaceId, isSwitching }
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(when)); vi.clearAllMocks()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  api.get.mockResolvedValue(pref())
  api.save.mockImplementation(async request => pref(request.enabled, request.workspaceAdminAccountId))
  api.summary.mockResolvedValue(snapshot())
  api.progress.mockImplementation(async request => ({ workspaceAdminAccountId: 'a', items: request.ids.map((id: string) => ({ id, readAt: null, reminderCompletedAt: when })) }))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('signup risk settings complete request paths', () => {
  it('loads, edits and saves only this type switch with all plain-language help', async () => {
    const w = settings(); await flushPromises()
    expect(w.text()).toContain('重要（弹窗提醒）')
    expect(w.text()).toContain('首次完整读取后开始提醒')
    expect(w.text()).toContain('网页每1分钟读取新提醒')
    expect(w.text()).toContain('不会修改主站')
    await w.find('input').setValue(true)
    expect(api.save).not.toHaveBeenCalled()
    await button(w, '保存并开启提醒').trigger('click'); await flushPromises()
    expect(api.save).toHaveBeenCalledWith({ workspaceAdminAccountId: 'a', enabled: true, workStart: '09:00', workEnd: '18:00', timeZone: 'Asia/Singapore' }, expect.anything())
    expect(w.text()).toContain('已开启')
    expect(w.emitted('saved')).toHaveLength(1)
    await w.find('input').setValue(false); await button(w, '保存并关闭提醒').trigger('click'); await flushPromises()
    expect(w.text()).toContain('已关闭')
  })
  it('keeps an unknown load disabled for editing and recovers only by re-reading', async () => {
    api.get.mockRejectedValueOnce(new Error('通知数据库连接中断'))
    const w = settings(); await flushPromises()
    expect(w.text()).toContain('通知数据库连接中断')
    expect(w.text()).toContain('原设置未被当作关闭')
    expect(w.find('input').attributes('disabled')).toBeDefined()
    expect(w.text()).not.toContain('已关闭；')
    api.get.mockResolvedValue(pref(true))
    await button(w, '重试读取设置').trigger('click'); await flushPromises()
    expect((w.find('input').element as HTMLInputElement).checked).toBe(true)
    expect(w.find('input').attributes('disabled')).toBeUndefined()
    expect(api.save).not.toHaveBeenCalled()
  })
  it('confirms an uncertain committed save using the read endpoint', async () => {
    const w = settings(); await flushPromises(); await w.find('input').setValue(true)
    api.save.mockRejectedValueOnce(new NotificationRequestError('提交结果未知', '连接中断', '重读设置', true, 'a', 503))
    api.get.mockResolvedValue(pref(true))
    await button(w, '保存并开启提醒').trigger('click'); await flushPromises()
    expect(w.text()).toContain('已开启')
    expect(w.find('[role="alert"]').exists()).toBe(false)
  })
  it('never declares uncertain or rejected saves successful without matching persisted values', async () => {
    const w = settings(); await flushPromises(); await w.find('input').setValue(true)
    api.save.mockRejectedValueOnce(new Error('工作区已变化，本次没有保存'))
    api.get.mockResolvedValue(pref(false))
    await button(w, '保存并开启提醒').trigger('click'); await flushPromises()
    expect(w.text()).toContain('工作区已变化')
    expect(w.text()).not.toContain('已开启；')
    expect(w.emitted('saved')).toBeUndefined()
    expect(w.text()).toContain('保存结果与本次修改不一致')
    expect(w.find('input').attributes('disabled')).toBeUndefined()
  })
  it('aborts a load as switching starts and ignores the old workspace response', async () => {
    const old = pending<SignupRiskPreferences>(); api.get.mockReturnValueOnce(old.promise)
    const w = settings(); const signal = api.get.mock.calls[0]![1] as AbortSignal
    await w.setProps({ switching: true }); expect(signal.aborted).toBe(true)
    old.resolve(pref(true)); await flushPromises()
    expect((w.find('input').element as HTMLInputElement).checked).toBe(false)
    api.get.mockResolvedValue(pref(false, 'b')); await w.setProps({ workspaceId: 'b', switching: false }); await flushPromises()
    expect(api.get).toHaveBeenLastCalledWith('b', expect.anything())
    expect((w.find('input').element as HTMLInputElement).checked).toBe(false)
  })
  it('aborts an in-flight save at switch or unmount without leaking success', async () => {
    const old = pending<SignupRiskPreferences>(); const w = settings(); await flushPromises()
    await w.find('input').setValue(true); api.save.mockReturnValueOnce(old.promise)
    await button(w, '保存并开启提醒').trigger('click')
    const signal = api.save.mock.calls[0]![1] as AbortSignal
    await w.setProps({ switching: true }); expect(signal.aborted).toBe(true)
    old.resolve(pref(true)); await flushPromises()
    expect(w.emitted('saved')).toBeUndefined(); expect(w.text()).not.toContain('已开启；')
  })
  it('does not contact or enable a non-Sub2API workspace', async () => {
    const w = mount(SignupRiskNotificationSettings, { props: { workspaceId: 'a', platform: 'new-api' } }); wrappers.push(w); await flushPromises()
    expect(api.get).not.toHaveBeenCalled(); expect(api.save).not.toHaveBeenCalled()
    expect(w.text()).toContain('这个工作区未连接 Sub2API')
    expect(button(w, '保存注册提醒设置').attributes('disabled')).toBeDefined()
  })
})

describe('supplemental risk reads preserve existing alert behavior', () => {
  it('shows a single registration surface when a normal summary contains the same pending risk notice', async () => {
    api.summary.mockResolvedValue(snapshot([fact('risk', true)]))
    const { w } = controller(); await flushPromises()
    expect(w.find('[aria-label="高风险注册提醒"]').exists()).toBe(true)
    expect(w.find('[aria-label="重要通知"]').exists()).toBe(false)
    expect(w.findAll('button').filter(b => b.text() === '查看详情')).toHaveLength(1)
  })
  it('does not reset the original five-minute urgent reminder clock', async () => {
    const { value } = controller(); await flushPromises()
    api.summary.mockResolvedValue(snapshot([fact('risk', true)], [fact('urgent', false, 4)]))
    await vi.advanceTimersByTimeAsync(299_999); await flushPromises()
    expect(value.criticalOpen.value).toBe(false)
    expect(value.signupRiskItems.value.map(item => item.id)).toEqual(['risk'])
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(value.criticalOpen.value).toBe(true)
  })
  it('retains the actual second-page cursor on every supplemental read', async () => {
    const first = snapshot([], [fact('ordinary-first')]); first.active.important.nextCursor = 'to-second'
    api.summary.mockResolvedValueOnce(first)
    const { value, w } = controller(); await flushPromises()
    const second = snapshot([], [fact('ordinary-second')]); second.active.important.nextCursor = 'to-third'
    api.summary.mockResolvedValueOnce(second)
    await value.nextSummaryPage(3); await flushPromises()
    const extra = snapshot([fact('risk', true)], [fact('ordinary-second')]); extra.active.important.nextCursor = 'to-third'
    api.summary.mockResolvedValue(extra)
    await vi.advanceTimersByTimeAsync(60_000); await flushPromises()
    expect(api.summary).toHaveBeenLastCalledWith(expect.objectContaining({ importantCursor: 'to-second' }), expect.anything())
    expect(value.importantItems.value.map(item => item.id)).toEqual(['ordinary-second'])
    expect(value.summary.value?.active.important.nextCursor).toBe('to-third')
    expect(w.text()).toContain('ordinary-second'); expect(w.text()).not.toContain('ordinary-first')
    expect(w.text()).toContain('alice+1@example.com')
  })
  it('dismisses only the registration group and does not restore it on repeated polling', async () => {
    const { value, w } = controller(); await flushPromises()
    api.summary.mockResolvedValue(snapshot([fact('risk', true)]))
    await vi.advanceTimersByTimeAsync(60_000); await flushPromises()
    await value.dismissSignupRisk(); await flushPromises()
    expect(api.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'a', action: 'dismiss', ids: ['risk'] }, expect.anything())
    expect(value.signupRiskItems.value).toEqual([])
    expect(value.summary.value?.active.important.totalCount).toBe(0)
    await vi.advanceTimersByTimeAsync(60_000); await flushPromises()
    expect(w.text()).not.toContain('alice+1@example.com')
  })
  it('cancels a hidden in-flight extra read and does not render its late result', async () => {
    let visible = true; vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
    const { value, w } = controller(); await flushPromises()
    const old = pending<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(old.promise)
    await vi.advanceTimersByTimeAsync(60_000)
    const signal = api.summary.mock.calls.at(-1)![1] as AbortSignal
    visible = false; document.dispatchEvent(new Event('visibilitychange'))
    expect(signal.aborted).toBe(true)
    old.resolve(snapshot([fact('old-risk', true)])); await flushPromises()
    expect(value.signupRiskItems.value).toEqual([])
    const before = api.summary.mock.calls.length
    await vi.advanceTimersByTimeAsync(120_000); expect(api.summary).toHaveBeenCalledTimes(before)
    api.summary.mockResolvedValue(snapshot())
    visible = true; document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(before + 1)
    w.unmount(); const after = api.summary.mock.calls.length
    await vi.advanceTimersByTimeAsync(600_000); expect(api.summary).toHaveBeenCalledTimes(after)
  })
  it('preserves the enabled retry schedule and current group after an extra read fails', async () => {
    api.summary.mockResolvedValue(snapshot([fact('risk', true)]))
    const { value } = controller(); await flushPromises()
    api.summary.mockRejectedValueOnce(new Error('通知数据库连接中断'))
    await vi.advanceTimersByTimeAsync(60_000); await flushPromises()
    expect(value.signupRiskEnabled.value).toBe(true)
    expect(value.signupRiskItems.value.map(item => item.id)).toEqual(['risk'])
    expect(value.summaryError.value).toContain('通知数据库连接中断')
    await vi.advanceTimersByTimeAsync(60_000); await flushPromises(); expect(api.summary).toHaveBeenCalledTimes(3)
  })
  it('does not create supplemental timers when the switch is off', async () => {
    api.summary.mockResolvedValue(snapshot([], [], false)); controller(); await flushPromises()
    await vi.advanceTimersByTimeAsync(299_999); expect(api.summary).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); await flushPromises(); expect(api.summary).toHaveBeenCalledTimes(2)
  })
})

// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NotificationSummary from '@/modules/admin/components/notifications/NotificationSummary.vue'
import SignupRiskNotificationSettings from '@/modules/admin/components/settings/SignupRiskNotificationSettings.vue'
import { useNotifications, type NotificationController } from '@/modules/admin/composables/useNotifications'
import type { NotificationSummaryResponse, SignupRiskPreferences, SiteNotification } from '@/modules/admin/types/notifications'

const api = vi.hoisted(() => ({ summary: vi.fn(), progress: vi.fn(), history: vi.fn(), detail: vi.fn(), preferences: vi.fn() }))
vi.mock('@/modules/admin/api/notifications', async original => ({ ...await original<typeof import('@/modules/admin/api/notifications')>(), getNotificationSummary: api.summary, saveNotificationProgress: api.progress, listNotifications: api.history, getNotificationDetail: api.detail, getSignupRiskPreferences: api.preferences }))
const start = '2031-01-01T00:59:00Z'
const fact = (id: string, risk = false): SiteNotification => ({ id, adminAccountId: 'a', kind: risk ? 'sub2api.signup_risk' : 'fixture.fact', subjectType: 'admin_account', subjectId: 'a', subjectActive: true, subjectInactiveMessage: '', level: 3, discoveredAt: start, createdAt: start, readAt: null, reminderCompletedAt: null, content: { title: id, summary: '隔离消息', details: [] } })
const snapshot = (now = start, activeUntil: string | null = null, risks: SiteNotification[] = []): NotificationSummaryResponse => ({
  workspaceAdminAccountId: 'a', serverTime: now, intervalSeconds: 300, unreadCount: 2, pendingCriticalCount: 0,
  active: { attention: { items: [], totalCount: 0, nextCursor: null }, important: { items: [fact('ordinary')], totalCount: 1, nextCursor: null }, critical: { items: [], totalCount: 0, nextCursor: null } },
  displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] },
  signupRisk: { enabled: true, activeUntil, nextStartAt: activeUntil ? '2031-01-02T01:00:00Z' : '2031-01-01T01:00:00Z', items: risks },
})
const pending = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
let wrapper: VueWrapper | undefined
function startController() {
  let controller!: NotificationController
  wrapper = mount(defineComponent({ setup() { controller = useNotifications({ workspaceId: ref('a'), isSwitching: ref(false) }); return () => h(NotificationSummary, { controller }) } }))
  return controller
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(start)); vi.clearAllMocks()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  api.summary.mockResolvedValue(snapshot())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('registration time boundary and progress safeguards', () => {
  it('starts the saved settings state no earlier than receipt allows and ends conservatively', async () => {
    const held = pending<SignupRiskPreferences>()
    api.preferences.mockReturnValueOnce(held.promise)
    wrapper = mount(SignupRiskNotificationSettings, { props: { workspaceId: 'a', platform: 'sub2api' } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(20_000)
    held.resolve({ workspaceAdminAccountId: 'a', enabled: true, initialized: true, updatedAt: start,
      workStart: '09:00', workEnd: '09:01', timeZone: 'Asia/Singapore', serverTime: '2031-01-01T00:59:10Z',
      activeUntil: null, nextStartAt: '2031-01-01T01:00:00Z' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(49_999)
    expect(wrapper.text()).toContain('已开启，当前不在工作时间内')
    await vi.advanceTimersByTimeAsync(1)
    expect(wrapper.text()).toContain('已开启，当前在工作时间内')
    await vi.advanceTimersByTimeAsync(39_999)
    expect(wrapper.text()).toContain('已开启，当前在工作时间内')
    await vi.advanceTimersByTimeAsync(1)
    expect(wrapper.text()).toContain('已开启，当前不在工作时间内')
  })

  it('uses response receipt for start wakeup and never spins after a failed start read', async () => {
    const held = pending<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(held.promise)
    startController(); await flushPromises()
    await vi.advanceTimersByTimeAsync(20_000)
    held.resolve(snapshot('2031-01-01T00:59:10Z')); await flushPromises()
    api.summary.mockRejectedValue(new Error('隔离网络中断'))
    await vi.advanceTimersByTimeAsync(49_999); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(59_999); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(3)
  })

  it('does not show a response whose working end passed while it was in flight', async () => {
    const held = pending<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(held.promise)
    startController(); await flushPromises()
    await vi.advanceTimersByTimeAsync(61_000)
    held.resolve(snapshot(start, '2031-01-01T01:00:00Z', [fact('risk', true)])); await flushPromises()
    expect(wrapper!.text()).not.toContain('risk')
    expect(wrapper!.text()).toContain('ordinary')
    expect(api.progress).not.toHaveBeenCalled()
  })

  it('does not present the previous combined total as an off-hours ordinary total', async () => {
    const risk = fact('risk', true)
    const data = snapshot(start, '2031-01-01T01:00:00Z', [risk])
    data.active.important = { items: [risk, fact('ordinary')], totalCount: 2, nextCursor: null }
    api.summary.mockResolvedValue(data)
    startController(); await flushPromises()
    expect(wrapper!.text()).toContain('共有2条待提醒')
    api.summary.mockRejectedValue(new Error('隔离网络中断'))
    await vi.advanceTimersByTimeAsync(60_001); await flushPromises()
    expect(wrapper!.text()).not.toContain('共有2条待提醒')
    expect(wrapper!.text()).toContain('本摘要1条')
    expect(wrapper!.text()).toContain('ordinary')
    expect(api.progress).not.toHaveBeenCalled()
  })

  it('retains the confirmed ordinary total when no registration contributed to it', async () => {
    api.summary.mockResolvedValue(snapshot(start, '2031-01-01T01:00:00Z'))
    startController(); await flushPromises()
    expect(wrapper!.text()).toContain('共有1条待提醒')
    api.summary.mockRejectedValue(new Error('隔离网络中断'))
    await vi.advanceTimersByTimeAsync(60_001); await flushPromises()
    expect(wrapper!.text()).toContain('共有1条待提醒')
    expect(wrapper!.text()).toContain('ordinary')
  })

  it('marking an off-hours risk history item read never decrements ordinary important count', async () => {
    const risk = fact('risk', true)
    const data = snapshot()
    data.displayedStates = [{ id: risk.id, readAt: null, reminderCompletedAt: null, subjectActive: true, subjectInactiveMessage: '' }]
    api.summary.mockResolvedValue(data)
    api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [risk], nextCursor: null })
    api.progress.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [{ id: 'risk', readAt: start, reminderCompletedAt: start }] })
    const controller = startController(); await flushPromises()
    await controller.openDrawer(); await flushPromises()
    await controller.markPageRead(); await flushPromises()
    expect(controller.summary.value?.active.important.totalCount).toBe(1)
    expect(wrapper!.text()).toContain('ordinary')
    expect(wrapper!.text()).not.toContain('risk')
  })
})

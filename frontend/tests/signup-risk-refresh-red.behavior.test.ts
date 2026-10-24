// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, type Ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminLayout from '@/modules/admin/layout/AdminLayout.vue'
import SettingsView from '@/modules/admin/views/SettingsView.vue'
import NotificationBell from '@/modules/admin/components/notifications/NotificationBell.vue'
import type { NotificationController } from '@/modules/admin/composables/useNotifications'
import type { NotificationSummaryResponse, SiteNotification } from '@/modules/admin/types/notifications'

const api = vi.hoisted(() => ({ summary: vi.fn(), history: vi.fn(), progress: vi.fn(), getRisk: vi.fn(), saveRisk: vi.fn(), current: null as Ref<{ id: string; displayName: string; platform: string; identity: string }> | null }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue'); api.current = ref({ id: 'a', displayName: '隔离工作区', platform: 'sub2api', identity: 'fixture' })
  return { useAdminAccounts: () => ({ currentAccount: api.current, isSwitching: ref(false), isDeleting: ref(false), noticeKey: ref(''), loadCurrentAccount: vi.fn(async () => true) }) }
})
vi.mock('@/modules/admin/api/notifications', async original => ({ ...await original<typeof import('@/modules/admin/api/notifications')>(), getNotificationSummary: api.summary, listNotifications: api.history, saveNotificationProgress: api.progress, getSignupRiskPreferences: api.getRisk, saveSignupRiskPreferences: api.saveRisk, getNotificationPreferences: vi.fn(async () => ({ workspaceAdminAccountId: 'a', intervalSeconds: 300, updatedAt: null })) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: vi.fn(async () => ({ enableRefreshInterval: false, refreshInterval: 60, enableBalanceWarning: false, defaultBalanceThreshold: 10, balanceNotifyBotIds: [], balanceTemplate: '', balanceTemplateFormat: 'text', enableMultiplierAlert: false, multiplierNotifyBotIds: [], multiplierTemplate: '', multiplierTemplateFormat: 'text' })), getNotificationChannelSettings: vi.fn(async () => ({ telegram: [] })), getSmtpSettings: vi.fn(async () => ({ host: '', port: 587, username: '', fromEmail: '', fromName: '', tlsMode: 'starttls', passwordConfigured: false, updatedAt: null })), saveStrategySettings: vi.fn(), saveNotificationChannelSettings: vi.fn(), testNotificationChannel: vi.fn(), saveSmtpSettings: vi.fn(), testSmtpEmail: vi.fn() }))
vi.mock('@/modules/admin/api/system', () => ({ getSystemVersion: vi.fn(async () => ({ version: 'V3.0.53' })), getSystemRestartStatus: vi.fn(async () => ({ state: 'idle' })), getSystemRollbackStatus: vi.fn(async () => ({ state: 'idle' })), getSystemUpgradeStatus: vi.fn(async () => ({ state: 'idle' })), isRollbackRouteNotFound: vi.fn(() => false), isTransientSystemApiError: vi.fn(() => false), startSystemRestart: vi.fn(), startSystemRollback: vi.fn(), startSystemUpgrade: vi.fn() }))
const when = '2031-01-01T00:00:00Z'
const item = (risk: boolean): SiteNotification => ({ id: risk ? 'risk' : 'urgent', adminAccountId: 'a', kind: risk ? 'sub2api.signup_risk' : 'fixture.fact', subjectType: 'admin_account', subjectId: 'a', subjectActive: true, subjectInactiveMessage: '', level: risk ? 3 : 4, discoveredAt: when, createdAt: when, readAt: null, reminderCompletedAt: null, content: { title: risk ? '注册风险' : '原有紧急消息', summary: '只提醒，不修改主站', details: [] } })
const snapshot = (risk = false, enabled = true): NotificationSummaryResponse => {
  const risks = risk ? [item(true)] : []
  return { workspaceAdminAccountId: 'a', serverTime: when, intervalSeconds: 300, unreadCount: 1 + risks.length, pendingCriticalCount: 1, active: { attention: { items: [], totalCount: 0, nextCursor: null }, important: { items: risks, totalCount: risks.length, nextCursor: null }, critical: { items: [item(false)], totalCount: 1, nextCursor: null } }, displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] }, signupRisk: { enabled, items: risks } }
}
const pending = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
const wrappers: VueWrapper[] = []
async function start() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/admin/settings', component: SettingsView }, { path: '/login', component: defineComponent({ template: '<div />' }) }] })
  await router.push('/admin/settings'); await router.isReady()
  const w = mount(AdminLayout, { global: { plugins: [router], stubs: { teleport: true, EmailTemplatesPanel: true, TestQuestionsPanel: true, NotificationTemplateEditor: true, Tooltip: true } } })
  wrappers.push(w); await flushPromises()
  return { w, controller: w.findComponent(NotificationBell).props('controller') as NotificationController }
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(when)); vi.clearAllMocks()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) { return { x: 0, y: 0, top: 0, left: 0, bottom: 40, right: 100, width: 100, height: 40, toJSON: () => ({}) } })
  api.summary.mockResolvedValue(snapshot())
  api.getRisk.mockResolvedValue({ workspaceAdminAccountId: 'a', enabled: true, initialized: true, updatedAt: when })
  api.saveRisk.mockImplementation(async request => ({ workspaceAdminAccountId: 'a', enabled: request.enabled, initialized: false, updatedAt: when }))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('registration-only actions cannot advance another reminder level', () => {
  it('establishes the first normal reminder clock when fast risk history is marked read before the initial summary', async () => {
    const old = pending<NotificationSummaryResponse>()
    api.summary.mockReturnValueOnce(old.promise).mockResolvedValue(snapshot(false, true))
    api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [item(true)], nextCursor: null })
    api.progress.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [{ id: 'risk', readAt: when, reminderCompletedAt: when }] })
    const { controller } = await start()
    const firstSignal = api.summary.mock.calls[0]![1] as AbortSignal
    await controller.openDrawer()
    expect(controller.summary.value).toBeNull()
    expect(await controller.markPageRead()).toBe(true)
    await flushPromises(); controller.closeDrawer()
    expect(firstSignal.aborted).toBe(true)
    old.resolve(snapshot(true)); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(299_999); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(controller.criticalOpen.value).toBe(true)
  })
  it('confirms the new switch after a summary started before its save returns', async () => {
    const old = pending<NotificationSummaryResponse>()
    api.summary.mockReturnValueOnce(old.promise).mockResolvedValue(snapshot(false, true))
    api.getRisk.mockResolvedValue({ workspaceAdminAccountId: 'a', enabled: false, initialized: false, updatedAt: when })
    const { w, controller } = await start()
    await w.find('input[aria-label="Sub2API 高风险注册提醒"]').setValue(true)
    await w.findAll('button').find(b => b.text() === '保存高风险注册提醒')!.trigger('click'); await flushPromises()
    old.resolve(snapshot(false, false)); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2)
    expect(controller.signupRiskEnabled.value).toBe(true)
  })
  it('saving the registration switch preserves the dismissed urgent timer', async () => {
    const { w, controller } = await start()
    expect(controller.criticalOpen.value).toBe(true)
    controller.laterCritical()
    await vi.advanceTimersByTimeAsync(1000)
    api.summary.mockResolvedValue(snapshot(false, false))
    await w.find('input[aria-label="Sub2API 高风险注册提醒"]').setValue(false)
    await w.findAll('button').find(b => b.text() === '保存高风险注册提醒')!.trigger('click'); await flushPromises()
    expect(controller.signupRiskEnabled.value).toBe(false)
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(298_999); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(controller.criticalOpen.value).toBe(true)
  })
  it('a slow registration dismissal crossing an extra read preserves the original urgent timer', async () => {
    api.summary.mockResolvedValue(snapshot(true))
    const { controller } = await start(); controller.laterCritical()
    const held = pending<{ workspaceAdminAccountId: string; items: { id: string; readAt: null; reminderCompletedAt: string }[] }>()
    api.progress.mockReturnValueOnce(held.promise)
    const saved = controller.dismissSignupRisk()
    await vi.advanceTimersByTimeAsync(15_000); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    held.resolve({ workspaceAdminAccountId: 'a', items: [{ id: 'risk', readAt: null, reminderCompletedAt: when }] })
    await saved; await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(284_999); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(controller.criticalOpen.value).toBe(true)
  })
})

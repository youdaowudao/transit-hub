// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NotificationSummary from '@/modules/admin/components/notifications/NotificationSummary.vue'
import { useNotifications, type NotificationController } from '@/modules/admin/composables/useNotifications'
import type { NotificationSummaryResponse, SiteNotification } from '@/modules/admin/types/notifications'

const api = vi.hoisted(() => ({ summary: vi.fn(), progress: vi.fn() }))
vi.mock('@/modules/admin/api/notifications', async original => ({
  ...await original<typeof import('@/modules/admin/api/notifications')>(),
  getNotificationSummary: api.summary, saveNotificationProgress: api.progress,
}))
const when = '2031-01-01T00:00:00Z'
const fact = (id: string, risk = false, level: 3 | 4 = 3): SiteNotification => ({
  id, adminAccountId: 'workspace', kind: risk ? 'sub2api.signup_risk' : 'fixture.fact', subjectType: 'admin_account', subjectId: 'workspace',
  subjectActive: true, subjectInactiveMessage: '', level, discoveredAt: when, createdAt: when, readAt: null, reminderCompletedAt: null,
  content: { title: risk ? 'Sub2API 高风险注册提醒' : '其他提醒', summary: risk ? '站点：隔离站点\n注册邮箱：alice+1@example.com\n用户 ID：42\n注册时间：2031-01-01 08:00:00\n请到 Sub2API 用户管理检查；本提醒不会修改主站。' : '原通知仍按原间隔提醒', details: [] },
})
const snapshot = (risk: SiteNotification[] = [], rest: SiteNotification[] = [], enabled = true): NotificationSummaryResponse => {
  const all = [...risk, ...rest]
  const bucket = (level: number) => ({ items: all.filter(n => n.level === level).slice(0, 20), totalCount: all.filter(n => n.level === level).length, nextCursor: null })
  return { workspaceAdminAccountId: 'workspace', serverTime: when, intervalSeconds: 300, unreadCount: all.length, pendingCriticalCount: bucket(4).totalCount,
    active: { attention: { items: [], totalCount: 0, nextCursor: null }, important: bucket(3), critical: bucket(4) },
    displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] },
    ...{ signupRisk: { enabled, items: risk } },
  }
}
let wrapper: VueWrapper | undefined
function start() {
  let controller!: NotificationController
  wrapper = mount(defineComponent({ setup() {
    controller = useNotifications({ workspaceId: ref('workspace'), isSwitching: ref(false) })
    return () => h(NotificationSummary, { controller })
  } }))
  return controller
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(when)); vi.clearAllMocks()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  api.summary.mockResolvedValue(snapshot())
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('high risk signup regression on the existing notification surface', () => {
  it('checks new signup reminders within 15 seconds without advancing another level reminder', async () => {
    const controller = start(); await flushPromises()
    api.summary.mockResolvedValue(snapshot([fact('risk-42', true)], [fact('urgent', false, 4)]))
    await vi.advanceTimersByTimeAsync(15_000); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2)
    expect(wrapper!.text()).toContain('Sub2API 高风险注册提醒')
    expect(wrapper!.text()).toContain('alice+1@example.com')
    expect(wrapper!.text()).toContain('用户 ID：42')
    expect(wrapper!.text()).toContain('2031-01-01 08:00:00')
    expect(controller.criticalOpen.value).toBe(false)
    expect(api.progress).not.toHaveBeenCalled()
  })

  it('preserves original important pagination while displaying a separate new signup group', async () => {
    const original = Array.from({ length: 20 }, (_, i) => fact(`old-${i}`))
    const first = snapshot([], original)
    first.active.important.totalCount = 30; first.active.important.nextCursor = 'original-page-2'
    api.summary.mockResolvedValue(first)
    const controller = start(); await flushPromises()
    const extra = snapshot([fact('risk-42', true)], original)
    extra.active.important.items = original; extra.active.important.totalCount = 31; extra.active.important.nextCursor = 'original-page-2'
    api.summary.mockResolvedValue(extra)
    await vi.advanceTimersByTimeAsync(15_000); await flushPromises()
    expect(controller.importantItems.value.map(n => n.id)).toEqual(original.map(n => n.id))
    expect(wrapper!.text()).toContain('收起这组注册提醒')
    expect(wrapper!.text()).toContain('alice+1@example.com')
    expect(controller.summary.value?.active.important.nextCursor).toBe('original-page-2')
  })
})

// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getNotificationCheckRoute, hasVisibleNotificationDialog, useNotifications, type NotificationController } from '@/modules/admin/composables/useNotifications'
import { NotificationRequestError } from '@/modules/admin/api/notifications'
import type { NotificationSummaryResponse, SiteNotification } from '@/modules/admin/types/notifications'

const api = vi.hoisted(() => ({ summary: vi.fn(), history: vi.fn(), detail: vi.fn(), progress: vi.fn(), getPreferences: vi.fn(), savePreferences: vi.fn() }))
vi.mock('@/modules/admin/api/notifications', async original => ({ ...await original<typeof import('@/modules/admin/api/notifications')>(), getNotificationSummary: api.summary, listNotifications: api.history, getNotificationDetail: api.detail, saveNotificationProgress: api.progress, getNotificationPreferences: api.getPreferences, saveNotificationPreferences: api.savePreferences }))
const date = '2030-01-01T00:00:00Z'
const item = (id: string, level: 1 | 2 | 3 | 4, workspace = 'a'): SiteNotification => ({ id, adminAccountId: workspace, kind: 'fixture.fact', subjectType: 'site', subjectId: 'site-a', level, discoveredAt: date, createdAt: date, content: { title: id, summary: '保存的事实', details: [] }, readAt: null, reminderCompletedAt: null })
const snapshot = (items: SiteNotification[] = [], workspace = 'a', seconds: number | null = 300): NotificationSummaryResponse => {
  const bucket = (level: number) => ({ items: items.filter(n => n.level === level && !n.reminderCompletedAt), totalCount: items.filter(n => n.level === level && !n.reminderCompletedAt).length, nextCursor: null })
  return { workspaceAdminAccountId: workspace, serverTime: date, intervalSeconds: seconds, ...(seconds === null ? { preferenceWarning: { message: '设置暂不可读', detail: '数据库连接中断', next: '重试读取通知设置', retryable: true } } : {}), unreadCount: items.filter(n => !n.readAt).length, pendingCriticalCount: bucket(4).totalCount, active: { attention: bucket(2), important: bucket(3), critical: bucket(4) }, samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] } }
}
const deferred = <T,>() => { let resolve!: (value: T) => void; let reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const wrappers: VueWrapper[] = []
const failure = (status: number | null) => new NotificationRequestError('保存通知时连接中断', '这些消息的保存结果需要核查', '重新读取这些消息', true, 'a', status)
function start() {
  const workspaceId = ref('a'), isSwitching = ref(false), enabled = ref(true), navigate = vi.fn()
  let controller!: NotificationController
  const wrapper = mount(defineComponent({ setup() { controller = useNotifications({ workspaceId, isSwitching, enabled, navigate }); return {} }, template: '<div />' }))
  wrappers.push(wrapper)
  return { controller, workspaceId, isSwitching, enabled, navigate, wrapper }
}
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(date)); vi.clearAllMocks()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) { const hidden = this.style.display === 'none' || this.style.visibility === 'hidden'; return { width: hidden ? 0 : 10, height: hidden ? 0 : 10, x: 0, y: 0, top: 0, left: 0, bottom: 10, right: 10, toJSON: () => ({}) } })
  api.summary.mockResolvedValue(snapshot())
  api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [], nextCursor: null })
  api.detail.mockImplementation(async (_workspace, id) => ({ workspaceAdminAccountId: 'a', item: item(id, 4) }))
  api.getPreferences.mockResolvedValue({ workspaceAdminAccountId: 'a', intervalSeconds: 300, updatedAt: null })
  api.savePreferences.mockImplementation(async request => ({ workspaceAdminAccountId: request.workspaceAdminAccountId, intervalSeconds: request.intervalMinutes * 60, updatedAt: date }))
  api.progress.mockImplementation(async request => ({ workspaceAdminAccountId: request.workspaceAdminAccountId, items: request.ids.map((id: string) => ({ id, readAt: request.action === 'read' || request.action === 'ack' ? date : null, reminderCompletedAt: request.action === 'read' ? null : date })) }))
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('workspace notification controller complete request paths', () => {
  it('coalesces manual reads with a slow request and schedules only after its completion', async () => {
    const pending = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(pending.promise)
    const { controller } = start(); const same = controller.refreshSummary()
    await vi.advanceTimersByTimeAsync(900_000)
    expect(api.summary).toHaveBeenCalledTimes(1)
    pending.resolve(snapshot()); await same; await flushPromises()
    await vi.advanceTimersByTimeAsync(299_999); expect(api.summary).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); expect(api.summary).toHaveBeenCalledTimes(2)
  })
  it.each([{action:'read',level:1,read:true,reminder:false},{action:'present',level:2,read:false,reminder:true},{action:'dismiss',level:3,read:false,reminder:true},{action:'ack',level:4,read:true,reminder:true}] as const)('recovers a slow first summary immediately after successful $action from fast history', async scenario => {
    const first = deferred<NotificationSummaryResponse>(), old = item('first-history', scenario.level), urgent = item('first-pending',4)
    const completed = {...old,readAt:scenario.read?date:null,reminderCompletedAt:scenario.reminder?date:null}
    api.summary.mockReturnValueOnce(first.promise).mockResolvedValue(snapshot([completed,urgent]))
    api.history.mockResolvedValue({workspaceAdminAccountId:'a',items:[old,urgent],nextCursor:null})
    const {controller}=start();const firstSignal=api.summary.mock.calls[0]![1] as AbortSignal
    await controller.loadHistory();expect(controller.summary.value).toBeNull()
    expect(await controller.progress(scenario.action,[old.id])).toBe(true);await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2);expect(firstSignal.aborted).toBe(true)
    expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.unreadCount.value).toBe(scenario.read?1:2)
    first.resolve(snapshot([old,urgent], 'a',60));await flushPromises()
    expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.intervalSeconds.value).toBe(300)
  })
  it.each([false,true])('recovers a slow first summary immediately after preference confirmation (unknown=%s)', async unknown => {
    const first=deferred<NotificationSummaryResponse>();api.summary.mockReturnValueOnce(first.promise).mockResolvedValue(snapshot([item('first-preference-pending',4)],'a',3600))
    if(unknown){api.savePreferences.mockRejectedValueOnce(failure(500));api.getPreferences.mockResolvedValue({workspaceAdminAccountId:'a',intervalSeconds:3600,updatedAt:date})}
    const {controller}=start();expect(controller.summary.value).toBeNull()
    expect(await controller.savePreferences(60)).toBe(true);await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2);expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.intervalSeconds.value).toBe(3600)
    first.resolve(snapshot([], 'a',300));await flushPromises();expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.intervalSeconds.value).toBe(3600)
    await vi.advanceTimersByTimeAsync(300_000);expect(api.summary).toHaveBeenCalledTimes(2)
  })
  it('pauses hidden timers and makes a single overdue read on visibility recovery', async () => {
    let visible = true; vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
    start(); await flushPromises(); visible = false; document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(1_800_000); expect(api.summary).toHaveBeenCalledTimes(1)
    visible = true; document.dispatchEvent(new Event('visibilitychange')); document.dispatchEvent(new Event('visibilitychange')); await flushPromises()
    expect(api.summary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(299_999); expect(api.summary).toHaveBeenCalledTimes(2)
  })
  it('does not display or finish reminders whose response arrives while hidden', async () => {
    let visible = true; vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
    const pending = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(pending.promise)
    const { controller } = start(); visible = false; document.dispatchEvent(new Event('visibilitychange'))
    pending.resolve(snapshot([item('urgent', 4), item('attention', 2)])); await flushPromises()
    expect(controller.pendingCriticalCount.value).toBe(1); expect(controller.criticalOpen.value).toBe(false); expect(controller.attentionItems.value).toEqual([])
    expect(await controller.presentAttention(['attention'])).toBe(false); expect(api.progress).not.toHaveBeenCalled()
  })
  it('aborts at switch start, ignores old results and finally, and never borrows another workspace interval', async () => {
    const old = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(old.promise).mockResolvedValue(snapshot([], 'b', null))
    const { controller, workspaceId, isSwitching } = start(); const oldSignal = api.summary.mock.calls[0]![1] as AbortSignal
    isSwitching.value = true; expect(oldSignal.aborted).toBe(true)
    workspaceId.value = 'b'; isSwitching.value = false; await flushPromises()
    expect(controller.intervalSeconds.value).toBe(300); expect(controller.preferenceConfirmed.value).toBe(false)
    old.resolve(snapshot([item('old-secret', 4)], 'a', 3600)); await flushPromises()
    expect(controller.summary.value?.workspaceAdminAccountId).toBe('b'); expect(controller.pendingCriticalCount.value).toBe(0); expect(controller.intervalSeconds.value).toBe(300)
    expect(controller.displayedPreferenceError.value).toContain('数据库连接中断')
    await vi.advanceTimersByTimeAsync(300_000); expect(api.summary).toHaveBeenCalledTimes(3)
  })
  it('aborts all work and removes future checks on selection, logout, and unmount', async () => {
    const { controller, enabled, workspaceId, wrapper } = start(); await flushPromises()
    const pending = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(pending.promise)
    void controller.refreshSummary(); const signal = api.summary.mock.calls.at(-1)![1] as AbortSignal
    enabled.value = false; expect(signal.aborted).toBe(true); expect(controller.drawerOpen.value).toBe(false)
    workspaceId.value = ''; await vi.advanceTimersByTimeAsync(900_000); expect(api.summary).toHaveBeenCalledTimes(2)
    wrapper.unmount(); pending.resolve(snapshot([item('late', 4)])); await flushPromises(); expect(controller.summary.value).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })
  it('keeps a fixed urgent batch while later arrivals only change badge counts', async () => {
    const first = Array.from({ length: 20 }, (_, i) => item(`urgent-${i}`, 4)); const firstSummary = snapshot(first)
    firstSummary.pendingCriticalCount = 24; firstSummary.active.critical.totalCount = 24; firstSummary.active.critical.nextCursor = 'page-2'
    api.summary.mockResolvedValueOnce(firstSummary)
    const { controller } = start(); await flushPromises()
    const next = snapshot([item('new-arrival', 4)]); next.pendingCriticalCount = 25; next.active.critical.totalCount = 25
    api.summary.mockResolvedValueOnce(next); await controller.refreshSummary()
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual(first.map(n => n.id)); expect(controller.criticalTotalCount.value).toBe(24); expect(controller.pendingCriticalCount.value).toBe(25)
    expect(await controller.acknowledgeCritical()).toBe(true)
    expect(api.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'a', action: 'ack', ids: first.map(n => n.id) }, expect.anything())
    expect(controller.pendingCriticalCount.value).toBe(5); expect(controller.criticalOpen.value).toBe(false)
  })

  it.each([false,true])('keeps a newer urgent batch and its counts when an older acknowledgement arrives late (new snapshot still included old=%s)', async includedOld => {
    const old=item('old-urgent',4),fresh=item('fresh-urgent',4);api.summary.mockResolvedValueOnce(snapshot([old]));const {controller}=start();await flushPromises()
    const pending=deferred<{workspaceAdminAccountId:string;items:{id:string;readAt:string;reminderCompletedAt:string}[]}>();api.progress.mockReturnValueOnce(pending.promise)
    const acknowledgement=controller.acknowledgeCritical();controller.laterCritical()
    api.summary.mockResolvedValueOnce(snapshot(includedOld?[old,fresh]:[fresh])).mockResolvedValue(snapshot([fresh]));await controller.refreshSummary()
    expect(controller.criticalOpen.value).toBe(true);expect(controller.criticalBatch.value.map(n=>n.id)).toEqual(includedOld?['old-urgent','fresh-urgent']:['fresh-urgent'])
    pending.resolve({workspaceAdminAccountId:'a',items:[{id:'old-urgent',readAt:date,reminderCompletedAt:date}]});expect(await acknowledgement).toBe(true)
    expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.unreadCount.value).toBe(1);expect(controller.criticalOpen.value).toBe(true);expect(controller.criticalBatch.value.some(n=>n.id==='fresh-urgent'&&!n.reminderCompletedAt)).toBe(true)
  })

  it.each([{action:'read',level:1,read:true,reminder:false},{action:'present',level:2,read:false,reminder:true},{action:'dismiss',level:3,read:false,reminder:true},{action:'ack',level:4,read:true,reminder:true}] as const)('keeps newer aggregate correct for late $action without touching a fresh urgent notice', async scenario=>{
    const old=item('old-progress',scenario.level),fresh=item('fresh-progress',4),completed={...old,readAt:scenario.read?date:null,reminderCompletedAt:scenario.reminder?date:null}
    api.summary.mockResolvedValueOnce(snapshot([old]));api.history.mockResolvedValue({workspaceAdminAccountId:'a',items:[old],nextCursor:null});const {controller}=start();await flushPromises();await controller.loadHistory()
    const pending=deferred<{workspaceAdminAccountId:string;items:{id:string;readAt:string|null;reminderCompletedAt:string|null}[]}>();api.progress.mockReturnValueOnce(pending.promise);const operation=controller.progress(scenario.action,[old.id])
    controller.laterCritical();const current=snapshot([completed,fresh]);api.summary.mockResolvedValue(current);await controller.refreshSummary();pending.resolve({workspaceAdminAccountId:'a',items:[{id:old.id,readAt:completed.readAt,reminderCompletedAt:completed.reminderCompletedAt}]});expect(await operation).toBe(true);await flushPromises()
    expect(controller.unreadCount.value).toBe(scenario.read?1:2);expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.criticalBatch.value.map(n=>n.id)).toEqual(['fresh-progress']);expect(controller.criticalOpen.value).toBe(true)
    expect(api.summary).toHaveBeenCalledTimes(3)
  })

  it('uses the same safe aggregate path after an unknown old acknowledgement is confirmed by its exact id',async()=>{
    const old=item('old-unknown',4),fresh=item('fresh-unknown',4);api.summary.mockResolvedValueOnce(snapshot([old]));const {controller}=start();await flushPromises()
    const pending=deferred<never>();api.progress.mockReturnValueOnce(pending.promise);const operation=controller.acknowledgeCritical();controller.laterCritical();api.summary.mockResolvedValue(snapshot([fresh]));await controller.refreshSummary()
    api.detail.mockResolvedValueOnce({workspaceAdminAccountId:'a',item:{...old,readAt:date,reminderCompletedAt:date}});pending.reject(failure(500));expect(await operation).toBe(true);await flushPromises()
    expect(api.detail).toHaveBeenCalledWith('a','old-unknown',expect.anything());expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.unreadCount.value).toBe(1);expect(controller.criticalOpen.value).toBe(true);expect(controller.criticalBatch.value[0]?.id).toBe('fresh-unknown')
  })

  it('preserves the newer pending batch and known saved progress if the single aggregate recheck fails',async()=>{
    const old=item('old-recheck',4),fresh=item('fresh-recheck',4);api.summary.mockResolvedValueOnce(snapshot([old]));const {controller}=start();await flushPromises()
    const pending=deferred<{workspaceAdminAccountId:string;items:{id:string;readAt:string;reminderCompletedAt:string}[]}>();api.progress.mockReturnValueOnce(pending.promise);const operation=controller.acknowledgeCritical();controller.laterCritical();api.summary.mockResolvedValueOnce(snapshot([fresh])).mockRejectedValueOnce(new Error('核对当前摘要时连接中断，请重试'));await controller.refreshSummary()
    pending.resolve({workspaceAdminAccountId:'a',items:[{id:old.id,readAt:date,reminderCompletedAt:date}]});expect(await operation).toBe(true);await flushPromises();expect(controller.pendingCriticalCount.value).toBe(1);expect(controller.criticalOpen.value).toBe(true);expect(controller.summaryError.value).toContain('核对当前摘要时连接中断');expect(controller.progressError.value).toBe('')
  })
  it('loads a manual urgent page using its fixed cursor without confirming either page', async () => {
    const first = snapshot([item('first', 4)]); first.active.critical.nextCursor = 'critical-cursor'; first.active.critical.totalCount = 21; first.pendingCriticalCount = 21
    api.summary.mockResolvedValueOnce(first).mockResolvedValueOnce(snapshot([item('second-page', 4)]))
    const { controller } = start(); await flushPromises(); await controller.nextSummaryPage(4)
    expect(api.summary).toHaveBeenLastCalledWith({ workspaceAdminAccountId: 'a', criticalCursor: 'critical-cursor' }, expect.anything())
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual(['second-page']); expect(api.progress).not.toHaveBeenCalled()
  })
  it('blocks automatic urgent windows behind visible inline roles and retries only on a normal read', async () => {
    const modal = document.createElement('section'); modal.setAttribute('role', 'dialog'); document.body.append(modal)
    expect(hasVisibleNotificationDialog()).toBe(true)
    api.summary.mockResolvedValue(snapshot([item('urgent', 4)])); const { controller } = start(); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false); modal.remove(); await flushPromises(); expect(controller.criticalOpen.value).toBe(false)
    await vi.advanceTimersByTimeAsync(300_000); expect(controller.criticalOpen.value).toBe(true)
  })
  it('does not treat display-none or visibility-hidden dialogs as visible blockers', async () => {
    const hidden = document.createElement('section'); hidden.setAttribute('role', 'alertdialog'); hidden.style.display = 'none'; document.body.append(hidden)
    const hidden2 = document.createElement('section'); hidden2.setAttribute('role', 'dialog'); hidden2.style.visibility = 'hidden'; document.body.append(hidden2)
    expect(hasVisibleNotificationDialog()).toBe(false); api.summary.mockResolvedValue(snapshot([item('urgent', 4)]))
    const { controller } = start(); await flushPromises(); expect(controller.criticalOpen.value).toBe(true)
  })
  it('invalidates slow old summary after progress and keeps late history/detail/progress nulls from resurrecting state', async () => {
    api.summary.mockResolvedValueOnce(snapshot([item('urgent', 4)])); const { controller } = start(); await flushPromises()
    const oldSummary = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(oldSummary.promise); void controller.refreshSummary(); const signal = api.summary.mock.calls.at(-1)![1] as AbortSignal
    expect(await controller.progress('ack', ['urgent'])).toBe(true); expect(signal.aborted).toBe(true)
    oldSummary.resolve(snapshot([item('urgent', 4)])); await flushPromises(); expect(controller.unreadCount.value).toBe(0); expect(controller.pendingCriticalCount.value).toBe(0)
    api.history.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', items: [item('urgent', 4)], nextCursor: null }); await controller.loadHistory()
    expect(controller.historyItems.value[0]?.reminderCompletedAt).toBe(date); expect(controller.historyItems.value[0]?.readAt).toBe(date)
    await controller.openDetail('urgent'); expect(controller.detail.value?.item.reminderCompletedAt).toBe(date)
    api.progress.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', items: [{ id: 'urgent', readAt: null, reminderCompletedAt: null }] }); await controller.progress('read', ['urgent'])
    expect(controller.detail.value?.item.readAt).toBe(date); expect(controller.pendingCriticalCount.value).toBe(0)
  })
  it('invalidates old summary when preferences save and retains that confirmed interval after its late reply', async () => {
    const old = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(old.promise).mockResolvedValue(snapshot([], 'a', 420))
    const { controller } = start(); const signal = api.summary.mock.calls[0]![1] as AbortSignal
    expect(await controller.savePreferences(7)).toBe(true); expect(signal.aborted).toBe(true)
    old.resolve(snapshot([], 'a', 300)); await flushPromises(); expect(controller.intervalSeconds.value).toBe(420); expect(controller.preferenceSaved.value).toBe(true)
    expect(api.summary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(419_999); expect(api.summary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1); expect(api.summary).toHaveBeenCalledTimes(3)
  })
  it('preserves messages and the last successful timestamp on read failure', async () => {
    api.summary.mockResolvedValueOnce(snapshot([item('important', 3)])); const { controller } = start(); await flushPromises()
    const last = controller.lastCheckedAt.value; api.summary.mockRejectedValueOnce(new Error('数据库连接中断，请重试'))
    expect(await controller.refreshSummary()).toBe(false); expect(controller.importantItems.value.map(n => n.id)).toEqual(['important']); expect(controller.lastCheckedAt.value).toBe(last)
    expect(controller.summaryError.value).toContain('数据库连接中断'); expect(controller.summaryError.value).toContain('原消息保留')
  })
  it('uses same-workspace confirmed preference on auxiliary failure, but resets to five minutes in a new workspace', async () => {
    api.summary.mockResolvedValueOnce(snapshot([], 'a', 420)); const { controller, workspaceId } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce(snapshot([], 'a', null)); await controller.refreshSummary(); expect(controller.intervalSeconds.value).toBe(420); expect(controller.displayedPreferenceError.value).toContain('暂按7分钟')
    api.summary.mockResolvedValueOnce(snapshot([], 'b', null)); workspaceId.value = 'b'; await flushPromises(); expect(controller.intervalSeconds.value).toBe(300); expect(controller.displayedPreferenceError.value).toContain('暂按5分钟')
  })
  it('keeps standalone preference GET failure visible across a successful summary and clears it only by a real settings retry', async () => {
    const { controller } = start(); await flushPromises(); api.getPreferences.mockRejectedValueOnce(new Error('读取设置的数据库连接中断'))
    expect(await controller.loadPreferences()).toBe(false); await controller.refreshSummary(); expect(controller.displayedPreferenceError.value).toContain('读取设置的数据库连接中断')
    await controller.loadPreferences(); expect(controller.displayedPreferenceError.value).toBe('')
  })
  it.each([0, 61, 1.5, NaN])('rejects invalid minutes %s without a write', async minutes => {
    const { controller } = start(); await flushPromises(); expect(await controller.savePreferences(minutes)).toBe(false); expect(api.savePreferences).not.toHaveBeenCalled(); expect(controller.intervalSeconds.value).toBe(300)
  })
  it('does not reread a known rejected write or display false preference success', async () => {
    const { controller } = start(); await flushPromises(); api.savePreferences.mockRejectedValueOnce(failure(409))
    expect(await controller.savePreferences(7)).toBe(false); expect(api.getPreferences).not.toHaveBeenCalled(); expect(controller.intervalSeconds.value).toBe(300); expect(controller.preferenceSaved.value).toBe(false)
    expect(controller.preferenceError.value).toContain('原间隔保持显示')
  })
  it('confirms an unknown preference write only by matching the same workspace value and a saved timestamp', async () => {
    const { controller } = start(); await flushPromises(); api.savePreferences.mockRejectedValueOnce(failure(null)); api.getPreferences.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', intervalSeconds: 420, updatedAt: null })
    expect(await controller.savePreferences(7)).toBe(false); expect(controller.intervalSeconds.value).toBe(300)
    api.savePreferences.mockRejectedValueOnce(failure(500)); api.getPreferences.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', intervalSeconds: 420, updatedAt: date })
    expect(await controller.savePreferences(7)).toBe(true); expect(controller.intervalSeconds.value).toBe(420)
    expect(api.getPreferences).toHaveBeenLastCalledWith('a', expect.anything())
  })
  it('confirms unknown acknowledgement by rereading the exact displayed ids; partial confirmation stays pending', async () => {
    api.summary.mockResolvedValue(snapshot([item('x', 4), item('y', 4)])); const { controller } = start(); await flushPromises()
    api.progress.mockRejectedValueOnce(failure(500)); api.detail.mockImplementation(async (_ws, id) => ({ workspaceAdminAccountId: 'a', item: { ...item(id, 4), readAt: date, reminderCompletedAt: id === 'x' ? date : null } }))
    expect(await controller.acknowledgeCritical()).toBe(false); expect(controller.pendingCriticalCount.value).toBe(2); expect(controller.criticalOpen.value).toBe(true); expect(controller.progressError.value).toContain('这2条消息的知悉保存结果暂时无法确认')
    expect(api.detail.mock.calls.map(call => call[1])).toEqual(['x', 'y'])
    api.progress.mockRejectedValueOnce(failure(null)); api.detail.mockImplementation(async (_ws, id) => ({ workspaceAdminAccountId: 'a', item: { ...item(id, 4), readAt: date, reminderCompletedAt: date } }))
    expect(await controller.acknowledgeCritical()).toBe(true); expect(controller.pendingCriticalCount.value).toBe(0); expect(controller.criticalOpen.value).toBe(false)
  })
  it('marks only the loaded page and does not complete urgent reminders on read', async () => {
    const page = [item('normal', 1), item('urgent', 4)]; api.summary.mockResolvedValue(snapshot([page[1]!]))
    api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: page, nextCursor: 'next-history' })
    const { controller } = start(); await flushPromises(); await controller.openDrawer(); expect(api.progress).not.toHaveBeenCalled()
    await controller.markPageRead(); expect(api.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'a', action: 'read', ids: ['normal', 'urgent'] }, expect.anything())
    expect(controller.pendingCriticalCount.value).toBe(1); expect(controller.historyItems.value[1]?.reminderCompletedAt).toBeNull()
  })

  it('removes newly read rows from unread and newly acknowledged rows from pending while all history keeps facts', async () => {
    const normal=item('normal',1),urgent=item('urgent',4);api.summary.mockResolvedValue(snapshot([urgent]));api.history.mockResolvedValue({workspaceAdminAccountId:'a',items:[normal,urgent],nextCursor:null})
    const {controller}=start();await flushPromises();await controller.loadHistory('unread');await controller.markPageRead()
    expect(controller.historyItems.value).toEqual([]);expect(controller.pendingCriticalCount.value).toBe(1)
    api.history.mockResolvedValueOnce({workspaceAdminAccountId:'a',items:[urgent],nextCursor:null});await controller.loadHistory('pending');expect(controller.historyItems.value).toHaveLength(1)
    await controller.acknowledgeCritical(['urgent']);expect(controller.historyItems.value).toEqual([])
    await controller.loadHistory('all');expect(controller.historyItems.value.map(n=>n.id)).toEqual(['normal','urgent']);expect(controller.historyItems.value.every(n=>Boolean(n.readAt))).toBe(true)
  })
  it('retains facts and the previous page after filter or detail failures without marking anything read', async () => {
    api.history.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', items: [item('original', 1)], nextCursor: 'next' }); const { controller } = start(); await flushPromises(); await controller.loadHistory()
    api.history.mockRejectedValueOnce(new Error('当前筛选读取中断')); expect(await controller.loadHistory('unread')).toBe(false); expect(controller.historyFilter.value).toBe('all'); expect(controller.historyItems.value[0]?.id).toBe('original')
    api.detail.mockRejectedValueOnce(new Error('相关消息暂不可读取')); expect(await controller.openDetail('original')).toBe(false); expect(api.progress).not.toHaveBeenCalled(); expect(controller.detailError.value).toContain('本次没有标为已读')
  })
  it('finishes attention only after a visible eligible snapshot and keeps the displayed snapshot after saving', async () => {
    api.summary.mockResolvedValue(snapshot([item('attention', 2)])); const { controller } = start(); await flushPromises()
    expect(await controller.presentAttention(['not-displayed'])).toBe(false); expect(api.progress).not.toHaveBeenCalled()
    expect(await controller.presentAttention(['attention'])).toBe(true); expect(controller.attentionItems.value.map(n => n.id)).toEqual(['attention']); expect(controller.attentionItems.value[0]?.reminderCompletedAt).toBe(date)
    expect(await controller.presentAttention(['attention'])).toBe(false); expect(api.progress).toHaveBeenCalledTimes(1)
  })
  it('uses only fixed check routes and leaves unknown or deleted actions disabled', () => {
    const fact = item('action', 4); const known = { workspaceAdminAccountId: 'a', item: { ...fact, content: { ...fact.content, action: { kind: 'group_rates', siteId: 'site-a', focusGroupId: 'group-a', focusGroup: 'ignored-label' } } }, checkAction: { available: true, message: '只打开页面' } }
    expect(getNotificationCheckRoute(known)).toEqual({ path: '/admin/group-rates', query: { siteId: 'site-a', focusGroupId: 'group-a' } })
    expect(getNotificationCheckRoute({ ...known, checkAction: { available: false, message: '相关网站已删除，这条提醒保留当时信息' } })).toBeNull()
    expect(getNotificationCheckRoute({ ...known, item: { ...fact, content: { ...fact.content, action: { kind: 'unknown-link' } } } })).toBeNull()
  })
})

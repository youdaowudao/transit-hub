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
const item = (id: string, level: 1 | 2 | 3 | 4, workspace = 'a'): SiteNotification => ({ id, adminAccountId: workspace, kind: 'fixture.fact', subjectType: 'site', subjectId: 'site-a', subjectActive: true, subjectInactiveMessage: '', level, discoveredAt: date, createdAt: date, content: { title: id, summary: '保存的事实', details: [] }, readAt: null, reminderCompletedAt: null })
const snapshot = (items: SiteNotification[] = [], workspace = 'a', seconds: number | null = 300): NotificationSummaryResponse => {
  const bucket = (level: number) => ({ items: items.filter(n => n.level === level && !n.reminderCompletedAt), totalCount: items.filter(n => n.level === level && !n.reminderCompletedAt).length, nextCursor: null })
  return { workspaceAdminAccountId: workspace, serverTime: date, intervalSeconds: seconds, ...(seconds === null ? { preferenceWarning: { message: '设置暂不可读', detail: '数据库连接中断', next: '重试读取通知设置', retryable: true } } : {}), unreadCount: items.filter(n => !n.readAt).length, pendingCriticalCount: bucket(4).totalCount, active: { attention: bucket(2), important: bucket(3), critical: bucket(4) }, displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] } }
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
    next.displayedStates = first.map(n => ({ id: n.id, subjectActive: true, subjectInactiveMessage: '', readAt: null, reminderCompletedAt: null }))
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
    expect(api.summary).toHaveBeenLastCalledWith({ workspaceAdminAccountId: 'a', criticalCursor: 'critical-cursor', displayedIds: ['first'] }, expect.anything())
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
    const { controller } = start(); await flushPromises(); await controller.openDrawer(); await controller.loadHistory('all'); expect(api.progress).not.toHaveBeenCalled()
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

// 主代理冻结：已打开批次按明确编号失效，不与摘要第一页求交集。
describe('unmanaged upstream notification lifecycle critical regression', () => {
  it('removes only inactive displayed IDs from an open mixed urgent batch', async () => {
    const a = {...item('managed-a', 4), subjectType:'upstream_site', subjectId:'a', subjectActive:true, subjectInactiveMessage:''}
    const b = {...item('managed-b', 4), subjectType:'upstream_site', subjectId:'b', subjectActive:true, subjectInactiveMessage:''}
    api.summary.mockResolvedValueOnce({...snapshot([a,b]),displayedStates:[]})
    const {controller}=start(); await flushPromises()
    expect(controller.criticalBatch.value.map(n=>n.id)).toEqual(['managed-a','managed-b'])
    const states=[{id:a.id,subjectActive:false,subjectInactiveMessage:'这个站点已停止管理',readAt:null,reminderCompletedAt:null},{id:b.id,subjectActive:true,subjectInactiveMessage:'',readAt:null,reminderCompletedAt:null}]
    api.summary.mockResolvedValueOnce({...snapshot([b]),displayedStates:states})
    await controller.refreshSummary()
    expect(controller.criticalOpen.value).toBe(true)
    expect(controller.criticalBatch.value.map(n=>n.id)).toEqual(['managed-b'])
    await controller.acknowledgeCritical()
    expect(api.progress.mock.calls.at(-1)?.[0].ids).toEqual(['managed-b'])
  })
  it('keeps a displayed second-page urgent ID that is eligible but absent from first-page items', async () => {
    const later={...item('later-page',4),subjectActive:true,subjectInactiveMessage:''}
    const first={...item('first-page',4),subjectActive:true,subjectInactiveMessage:''}
    api.summary.mockResolvedValueOnce({...snapshot([later]),displayedStates:[]})
    const {controller}=start();await flushPromises()
    api.summary.mockResolvedValueOnce({...snapshot([first,later]),active:{...snapshot([first]).active,critical:{items:[first],totalCount:2,nextCursor:'next'}},displayedStates:[{id:later.id,subjectActive:true,subjectInactiveMessage:'',readAt:null,reminderCompletedAt:null}]})
    await controller.refreshSummary()
    expect(controller.criticalBatch.value.map(n=>n.id)).toEqual(['later-page'])
  })
})

describe('management changes and notification lifecycle failure paths', () => {
  const siteItem = (id: string, level: 1 | 2 | 3 | 4, site = 'a'): SiteNotification => ({ ...item(id, level), subjectType: 'upstream_site', subjectId: site })
  const displayed = (notice: SiteNotification, active = true) => ({ id: notice.id, subjectActive: active, subjectInactiveMessage: active ? '' : '这个站点已停止管理，这条消息保留为历史，不再主动提醒。', readAt: notice.readAt, reminderCompletedAt: notice.reminderCompletedAt })
  it('clears an invalidated entire urgent batch without filling it during the same check', async () => {
    const old = siteItem('old-batch', 4), fresh = siteItem('new-batch', 4, 'b')
    api.summary.mockResolvedValueOnce(snapshot([old]))
    const { controller } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce({ ...snapshot([fresh]), displayedStates: [displayed(old, false)] })
    await controller.refreshSummary()
    expect(api.summary.mock.calls.at(-1)?.[0].displayedIds).toEqual([old.id])
    expect(controller.criticalOpen.value).toBe(false); expect(controller.criticalBatch.value).toEqual([])
    api.summary.mockResolvedValueOnce(snapshot([fresh])); await controller.refreshSummary()
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual([fresh.id]); expect(controller.criticalOpen.value).toBe(true)
    expect(api.progress).not.toHaveBeenCalled()
  })
  it('updates attention and important summaries on normal reads and explicit next pages', async () => {
    const attention = siteItem('first-attention', 2), important = siteItem('first-important', 3)
    const original = snapshot([attention, important]); original.active.attention.nextCursor = 'attention-next'; original.active.important.nextCursor = 'important-next'
    api.summary.mockResolvedValueOnce(original)
    const { controller } = start(); await flushPromises()
    const freshAttention = siteItem('new-attention', 2), freshImportant = siteItem('new-important', 3)
    const next = snapshot([attention, important, freshAttention, freshImportant]); next.displayedStates = [displayed(attention), displayed(important)]
    next.active.attention.nextCursor = 'attention-next'; next.active.important.nextCursor = 'important-next'
    api.summary.mockResolvedValueOnce(next); await controller.refreshSummary()
    expect(controller.attentionItems.value.map(n => n.id)).toEqual([attention.id, freshAttention.id])
    expect(controller.importantItems.value.map(n => n.id)).toEqual([important.id, freshImportant.id])
    const secondAttention = siteItem('second-attention', 2), secondImportant = siteItem('second-important', 3)
    const second = snapshot([secondAttention, secondImportant])
    second.active.important.nextCursor = 'important-next'
    api.summary.mockResolvedValueOnce(second); await controller.nextSummaryPage(2)
    expect(controller.attentionItems.value.map(n => n.id)).toEqual([secondAttention.id])
    api.summary.mockResolvedValueOnce(snapshot([secondAttention, secondImportant])); await controller.nextSummaryPage(3)
    expect(controller.importantItems.value.map(n => n.id)).toEqual([secondImportant.id])
    expect(api.progress).not.toHaveBeenCalled()
  })
  it('immediately drops confirmed stopped items, hides old counts through failures and recovers only from a latest summary', async () => {
    const a = siteItem('a-urgent', 4), b = siteItem('b-urgent', 4, 'b')
    api.summary.mockResolvedValueOnce(snapshot([a, b]))
    const { controller } = start(); await flushPromises()
    const old = deferred<NotificationSummaryResponse>(), current = deferred<NotificationSummaryResponse>()
    api.summary.mockReturnValueOnce(old.promise); void controller.refreshSummary()
    const oldSignal = api.summary.mock.calls.at(-1)![1] as AbortSignal
    api.summary.mockReturnValueOnce(current.promise)
    controller.siteManagementChanged('a', 'a', false)
    expect(oldSignal.aborted).toBe(true)
    expect(controller.managementRefreshPending.value).toBe(true)
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual([b.id])
    old.resolve(snapshot([a, b])); await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(true)
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual([b.id])
    current.reject(new Error('数据库连接中断 password=fixture-secret')); await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(true)
    expect(controller.summaryError.value).toContain('通知数量暂时无法确认')
    expect(controller.summaryError.value).toContain('数据库连接中断')
    expect(controller.summaryError.value).not.toContain('fixture-secret')
    expect(controller.unreadCount.value).toBe(2)
    api.summary.mockResolvedValueOnce({ ...snapshot([b]), displayedStates: [displayed(b)] }); await controller.refreshSummary()
    expect(controller.managementRefreshPending.value).toBe(false); expect(controller.unreadCount.value).toBe(1)
    expect(api.progress).not.toHaveBeenCalled()
  })
  it('does not refill a locally cleared urgent batch when its management confirmation reread returns', async () => {
    const old = siteItem('locally-cleared', 4), fresh = siteItem('other-still-pending', 4, 'b')
    api.summary.mockResolvedValueOnce(snapshot([old]))
    const { controller } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce(snapshot([fresh])); controller.siteManagementChanged('a', 'a', false); await flushPromises()
    expect(controller.criticalOpen.value).toBe(false); expect(controller.criticalBatch.value).toEqual([])
    api.summary.mockResolvedValueOnce(snapshot([fresh])); await controller.refreshSummary()
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual([fresh.id])
  })
  it('cancels old history and detail reads and updates the loaded history without changing reading progress', async () => {
    const a = siteItem('old-history', 4)
    api.summary.mockResolvedValueOnce(snapshot([a]))
    api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [a], nextCursor: null })
    api.detail.mockResolvedValue({ workspaceAdminAccountId: 'a', item: a, checkAction: { available: true, message: '只查看保存的信息' } })
    const { controller } = start(); await flushPromises(); await controller.openDrawer(); await controller.loadHistory('all'); await controller.openDetail(a.id)
    const history = deferred<{ workspaceAdminAccountId: string; items: SiteNotification[]; nextCursor: null }>(), detail = deferred<{ workspaceAdminAccountId: string; item: SiteNotification }>()
    api.history.mockReturnValueOnce(history.promise); void controller.loadHistory()
    api.detail.mockReturnValueOnce(detail.promise); void controller.openDetail(a.id, true)
    const historySignal = api.history.mock.calls.at(-1)![1] as AbortSignal, detailSignal = api.detail.mock.calls.at(-1)![2] as AbortSignal
    const inactive = { ...a, ...displayed(a, false) }
    api.summary.mockResolvedValueOnce(snapshot())
    api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [inactive], nextCursor: null })
    api.detail.mockResolvedValue({ workspaceAdminAccountId: 'a', item: inactive, checkAction: { available: true, message: '这个站点已停止管理' } })
    controller.siteManagementChanged('a', 'a', false); await flushPromises()
    expect(historySignal.aborted).toBe(true); expect(detailSignal.aborted).toBe(true)
    history.resolve({ workspaceAdminAccountId: 'a', items: [a], nextCursor: null }); detail.resolve({ workspaceAdminAccountId: 'a', item: a }); await flushPromises()
    expect(controller.historyItems.value[0]?.subjectActive).toBe(false); expect(controller.detail.value?.item.subjectActive).toBe(false)
    expect(controller.detail.value?.item.readAt).toBeNull(); expect(controller.detail.value?.item.reminderCompletedAt).toBeNull()
    expect(api.progress).not.toHaveBeenCalled()
  })
  it('does not let stale active history restore automatic eligibility and allows a later active summary to restore it', async () => {
    const a = siteItem('restorable-important', 3)
    api.summary.mockResolvedValueOnce(snapshot([a])); const { controller } = start(); await flushPromises()
    api.summary.mockRejectedValueOnce(new Error('重新核查中断')); controller.siteManagementChanged('a', 'a', false); await flushPromises()
    api.history.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', items: [a], nextCursor: null }); await controller.loadHistory()
    api.progress.mockResolvedValueOnce({ workspaceAdminAccountId: 'a', items: [{ id: a.id, readAt: date, reminderCompletedAt: date }] })
    await controller.progress('read', [a.id]); await flushPromises()
    expect(controller.importantItems.value).toEqual([])
    api.summary.mockResolvedValueOnce(snapshot([a])); controller.siteManagementChanged('a', 'a', true); await flushPromises()
    // The explicit read above still completes the important reminder; restoration cannot undo progress.
    expect(controller.importantItems.value).toEqual([])
    const untouched = siteItem('restored-urgent', 4)
    api.summary.mockResolvedValueOnce(snapshot([untouched])); await controller.refreshSummary()
    expect(controller.criticalBatch.value[0]?.id).toBe(untouched.id)
  })
  it('does not subtract active unread or pending counts when manually reading and acknowledging stopped history', async () => {
    const active = siteItem('active-other', 4, 'b'), inactive = { ...siteItem('inactive-history', 4), subjectActive: false, subjectInactiveMessage: '已停止管理' }
    api.summary.mockResolvedValue(snapshot([active])); api.history.mockResolvedValue({ workspaceAdminAccountId: 'a', items: [inactive], nextCursor: null })
    api.detail.mockResolvedValue({ workspaceAdminAccountId: 'a', item: inactive })
    const { controller } = start(); await flushPromises(); await controller.openDrawer(); await controller.openDetail(inactive.id)
    const check = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(check.promise)
    expect(await controller.markDetailRead(inactive.id)).toBe(true)
    expect(controller.unreadCount.value).toBe(1); expect(controller.pendingCriticalCount.value).toBe(1)
    expect(await controller.acknowledgeCritical([inactive.id])).toBe(true)
    expect(controller.unreadCount.value).toBe(1); expect(controller.pendingCriticalCount.value).toBe(1)
    expect(controller.detail.value?.item.reminderCompletedAt).toBe(date)
    check.resolve(snapshot([active])); await flushPromises()
  })
  it('treats failed management as unknown until authoritative reading and discards old workspace results', async () => {
    const a = siteItem('unknown-setting', 4)
    api.summary.mockResolvedValueOnce(snapshot([a])); const { controller, workspaceId } = start(); await flushPromises()
    const check = deferred<NotificationSummaryResponse>(); api.summary.mockReturnValueOnce(check.promise)
    controller.siteManagementChanged('a', 'a')
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual([a.id]); expect(controller.managementRefreshPending.value).toBe(true)
    api.summary.mockResolvedValueOnce(snapshot([], 'b')); workspaceId.value = 'b'; await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(false); expect(controller.unreadCount.value).toBe(0)
    check.resolve(snapshot([a])); await flushPromises()
    expect(controller.summary.value?.workspaceAdminAccountId).toBe('b'); expect(controller.criticalBatch.value).toEqual([])
    controller.siteManagementChanged('a', 'a', false)
    expect(controller.managementRefreshPending.value).toBe(false)
  })
})


describe('restoring the same upstream notification subject', () => {
  it('restores untouched old urgent facts from a new summary while preserving their original discovery time', async () => {
    const a = { ...item('restore-same-id', 4), subjectType: 'upstream_site', subjectId: 'a' }
    api.summary.mockResolvedValueOnce(snapshot([a])); const { controller } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce(snapshot()); controller.siteManagementChanged('a', 'a', false); await flushPromises()
    expect(controller.criticalBatch.value).toEqual([])
    api.summary.mockResolvedValueOnce(snapshot([a])); controller.siteManagementChanged('a', 'a', true); await flushPromises()
    expect(controller.criticalBatch.value[0]).toMatchObject({ id: a.id, subjectActive: true, discoveredAt: date, readAt: null, reminderCompletedAt: null })
    expect(api.progress).not.toHaveBeenCalled()
  })
})

// 主代理冻结：Review C1三类交错必须先在C1失败，旧批次/后到消息期望不改。
describe('review lifecycle crossings critical regression', () => {
  const upstreamItem = (id: string, level: 1 | 4, site: string): SiteNotification => ({ ...item(id, level), subjectType: 'upstream_site', subjectId: site })
  const state = (notice: SiteNotification, active = true) => ({ id: notice.id, subjectActive: active, subjectInactiveMessage: active ? '' : '这个站点已停止管理', readAt: null, reminderCompletedAt: null })
  it.each([{ view: 'history', level: 1 }, { view: 'history', level: 4 }, { view: 'detail', level: 1 }, { view: 'detail', level: 4 }] as const)('does not debit B for late active $view A at level $level after a newer summary excludes A', async scenario => {
    const a = upstreamItem('late-eligible-a', scenario.level, 'a'), b = upstreamItem('still-counted-b', 4, 'b')
    api.summary.mockReset().mockResolvedValue(snapshot([b]))
    const { controller } = start(); await flushPromises()
    if (scenario.view === 'history') {
      const old = deferred<{ workspaceAdminAccountId: string; items: SiteNotification[]; nextCursor: null }>()
      api.history.mockReturnValueOnce(old.promise)
      const opening = controller.openDrawer(); await flushPromises()
      expect(controller.unreadCount.value).toBe(1)
      old.resolve({ workspaceAdminAccountId: 'a', items: [a], nextCursor: null }); await opening
    } else {
      const old = deferred<{ workspaceAdminAccountId: string; item: SiteNotification }>()
      api.detail.mockReturnValueOnce(old.promise)
      const opening = controller.openDetail(a.id); await flushPromises(); await controller.refreshSummary()
      old.resolve({ workspaceAdminAccountId: 'a', item: a }); await opening
    }
    const beforeReads = api.summary.mock.calls.length, correction = deferred<NotificationSummaryResponse>()
    api.summary.mockReturnValueOnce(correction.promise)
    expect(await (scenario.level === 4 ? controller.acknowledgeCritical([a.id]) : scenario.view === 'history' ? controller.markPageRead() : controller.markDetailRead(a.id))).toBe(true)
    expect(api.progress.mock.calls.at(-1)?.[0].ids).toEqual([a.id])
    expect(controller.unreadCount.value).toBe(1); expect(controller.pendingCriticalCount.value).toBe(1)
    expect(api.summary.mock.calls.length).toBe(beforeReads + 1)
    correction.resolve(snapshot([b])); await flushPromises()
    expect(controller.unreadCount.value).toBe(1); expect(controller.pendingCriticalCount.value).toBe(1)
  })
  it('shows an explicitly requested valid next page when the prior urgent batch becomes entirely inactive', async () => {
    const a = upstreamItem('page-old-a', 4, 'a'), b = upstreamItem('page-next-b', 4, 'b'), first = snapshot([a])
    first.pendingCriticalCount = first.active.critical.totalCount = 2; first.active.critical.nextCursor = 'original-next'
    api.summary.mockReset().mockResolvedValueOnce(first); const { controller } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce({ ...snapshot([b]), displayedStates: [state(a, false)] })
    expect(await controller.nextSummaryPage(4)).toBe(true)
    expect(controller.criticalOpen.value).toBe(true); expect(controller.criticalBatch.value.map(n => n.id)).toEqual([b.id])
    expect(controller.criticalTotalCount.value).toBe(1)
    await controller.acknowledgeCritical(); expect(api.progress.mock.calls.at(-1)?.[0].ids).toEqual([b.id])
  })
  it('keeps the urgent dialog closed when the user closes it while the explicit page request is pending', async () => {
    const a = upstreamItem('closing-page-a', 4, 'a'), b = upstreamItem('closing-page-b', 4, 'b'), first = snapshot([a])
    first.active.critical.nextCursor = 'original-next'; api.summary.mockReset().mockResolvedValueOnce(first)
    const { controller } = start(); await flushPromises(); const page = deferred<NotificationSummaryResponse>()
    api.summary.mockReturnValueOnce(page.promise); const next = controller.nextSummaryPage(4); controller.laterCritical()
    page.resolve({ ...snapshot([b]), displayedStates: [state(a, false)] }); await next
    expect(controller.criticalOpen.value).toBe(false); expect(api.progress).not.toHaveBeenCalled()
  })
  it('lowers an open urgent total after only an undisplayed site becomes inactive without replacing the batch or cursor', async () => {
    const a = Array.from({ length: 20 }, (_, i) => upstreamItem('visible-a-' + i, 4, 'a')), first = snapshot(a)
    first.unreadCount = first.pendingCriticalCount = first.active.critical.totalCount = 21; first.active.critical.nextCursor = 'original-next'
    api.summary.mockReset().mockResolvedValueOnce(first); const { controller } = start(); await flushPromises()
    api.summary.mockResolvedValueOnce({ ...snapshot(a), displayedStates: a.map(n => state(n)) }); await controller.refreshSummary()
    expect(controller.criticalBatch.value.map(n => n.id)).toEqual(a.map(n => n.id))
    expect(controller.pendingCriticalCount.value).toBe(20); expect(controller.criticalTotalCount.value).toBe(20)
    expect(controller.criticalNextCursor.value).toBe('original-next'); expect(api.progress).not.toHaveBeenCalled()
  })
})

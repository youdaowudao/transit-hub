// @vitest-environment jsdom

import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, type Ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminLayout from '@/modules/admin/layout/AdminLayout.vue'
import SettingsView from '@/modules/admin/views/SettingsView.vue'
import NotificationBell from '@/modules/admin/components/notifications/NotificationBell.vue'
import rateCopyFixture from './fixtures/notificationsRateCopy.json'
import type { SiteNotification, NotificationSummaryResponse } from '@/modules/admin/types/notifications'

const harness = vi.hoisted(() => ({
  current: null as Ref<{ id: string; displayName: string; platform: string; identity: string }> | null,
  switching: null as Ref<boolean> | null,
  summary: vi.fn(), history: vi.fn(), detail: vi.fn(), progress: vi.fn(), getPreferences: vi.fn(), savePreferences: vi.fn(),
  detailResolve: null as ((value: { workspaceAdminAccountId: string; item: SiteNotification }) => void) | null,
  saveStrategy: vi.fn(), saveChannels: vi.fn(),
}))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.current = ref({ id: 'workspace-a', displayName: '隔离工作区', platform: 'sub2api', identity: 'fixture' })
  harness.switching = ref(false)
  return { useAdminAccounts: () => ({ currentAccount: harness.current, isSwitching: harness.switching, isDeleting: ref(false), noticeKey: ref(''), loadCurrentAccount: vi.fn(async () => true) }) }
})
vi.mock('@/modules/admin/api/notifications', async importOriginal => ({
  ...await importOriginal<typeof import('@/modules/admin/api/notifications')>(),
  getNotificationSummary: harness.summary, listNotifications: harness.history, getNotificationDetail: harness.detail,
  saveNotificationProgress: harness.progress, getNotificationPreferences: harness.getPreferences, saveNotificationPreferences: harness.savePreferences,
}))
vi.mock('@/modules/admin/api/settings', () => ({
  getStrategySettings: vi.fn(async () => ({ enableRefreshInterval: false, refreshInterval: 60, enableBalanceWarning: false, defaultBalanceThreshold: 10, balanceNotifyBotIds: [], balanceTemplate: '', balanceTemplateFormat: 'text', enableMultiplierAlert: false, multiplierNotifyBotIds: [], multiplierTemplate: '', multiplierTemplateFormat: 'text' })),
  getNotificationChannelSettings: vi.fn(async () => ({ telegram: [] })),
  saveStrategySettings: harness.saveStrategy, saveNotificationChannelSettings: harness.saveChannels, testNotificationChannel: vi.fn(),
  getSmtpSettings: vi.fn(async () => ({ host: '', port: 587, username: '', fromEmail: '', fromName: '', tlsMode: 'starttls', passwordConfigured: false, updatedAt: null })), saveSmtpSettings: vi.fn(), testSmtpEmail: vi.fn(),
}))
vi.mock('@/modules/admin/api/system', () => ({
  getSystemVersion: vi.fn(async () => ({ version: 'V3.0.5' })),
  getSystemRestartStatus: vi.fn(async () => ({ state: 'idle' })), getSystemRollbackStatus: vi.fn(async () => ({ state: 'idle' })), getSystemUpgradeStatus: vi.fn(async () => ({ state: 'idle' })),
  isRollbackRouteNotFound: vi.fn(() => false), isTransientSystemApiError: vi.fn(() => false), startSystemRestart: vi.fn(), startSystemRollback: vi.fn(), startSystemUpgrade: vi.fn(),
}))

const when = '2030-01-01T00:00:00Z'
const notice = (id: string, level: 1 | 2 | 3 | 4, read = false): SiteNotification => ({
  id, adminAccountId: 'workspace-a', kind: 'fixture.fact', subjectType: 'site', subjectId: 'site-a', subjectActive: true, subjectInactiveMessage: '', level,
  discoveredAt: when, createdAt: when, content: { title: `隔离提醒 ${id}`, summary: '查看事实不会修改主站', details: [] },
  readAt: read ? when : null, reminderCompletedAt: null,
})
const summary = (items: SiteNotification[] = []): NotificationSummaryResponse => {
  const bucket = (level: number) => ({ items: items.filter(item => item.level === level && item.reminderCompletedAt === null), totalCount: items.filter(item => item.level === level && item.reminderCompletedAt === null).length, nextCursor: null })
  return { workspaceAdminAccountId: 'workspace-a', serverTime: when, intervalSeconds: 300, unreadCount: items.filter(item => item.readAt === null).length, pendingCriticalCount: bucket(4).totalCount, active: { attention: bucket(2), important: bucket(3), critical: bucket(4) }, displayedStates: [], samplingState: { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] } }
}
const EmptyPage = defineComponent({ template: '<div>原业务页面</div>' })
const wrappers: VueWrapper[] = []
async function mountLayout(path = '/admin', realTeleport = false) {
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/admin', component: EmptyPage }, { path: '/admin/settings', component: SettingsView }, { path: '/admin/accounts', name: 'AdminAccounts', component: EmptyPage },
    ...['upstream', 'group-rates', 'group-associations', 'connection-health'].map(name => ({ path: `/admin/${name}`, component: EmptyPage })),
    { path: '/login', component: EmptyPage },
  ] })
  await router.push(path); await router.isReady()
  const wrapper = mount(AdminLayout, { attachTo: document.body, global: { plugins: [router], stubs: { ...(realTeleport ? {} : { teleport: true }), EmailTemplatesPanel: true, TestQuestionsPanel: true, NotificationTemplateEditor: true, Tooltip: true } } })
  wrappers.push(wrapper); await flushPromises()
  return { wrapper, router }
}
const button = (wrapper: VueWrapper, label: string) => {
  const found = wrapper.findAll('button').find(item => item.text().replace(/\s/g, '') === label.replace(/\s/g, ''))
  expect(found, label).toBeDefined()
  return found!
}
const bell = (wrapper: VueWrapper) => wrapper.find('button[aria-label^="通知"]')
const clickElement = async (element: HTMLElement) => { await new DOMWrapper(element).trigger('click'); await flushPromises() }

beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date(when)); vi.clearAllMocks()
  harness.current!.value = { id: 'workspace-a', displayName: '隔离工作区', platform: 'sub2api', identity: 'fixture' }
  harness.switching!.value = false
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const hidden = this.style.display === 'none' || this.style.visibility === 'hidden'
    return { x: 0, y: 0, top: 0, left: 0, right: hidden ? 0 : 100, bottom: hidden ? 0 : 40, width: hidden ? 0 : 100, height: hidden ? 0 : 40, toJSON: () => ({}) }
  })
  harness.summary.mockResolvedValue(summary())
  harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [notice('normal-a', 1)], nextCursor: null })
  harness.getPreferences.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', intervalSeconds: 300, updatedAt: null })
  harness.savePreferences.mockImplementation(async request => ({ workspaceAdminAccountId: request.workspaceAdminAccountId, intervalSeconds: request.intervalMinutes * 60, updatedAt: when }))
  harness.progress.mockImplementation(async request => ({ workspaceAdminAccountId: request.workspaceAdminAccountId, items: request.ids.map((id: string) => ({ id, readAt: request.action === 'read' || request.action === 'ack' ? when : null, reminderCompletedAt: request.action === 'read' ? null : when })) }))
})
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks(); localStorage.clear(); document.body.innerHTML = ''
})

describe('notifications existing layout and settings critical entry points', () => {
  it('opens local history from the global bell without marking an entire drawer read', async () => {
    const { wrapper } = await mountLayout()
    expect(bell(wrapper).exists()).toBe(true)
    expect(harness.summary).toHaveBeenCalledTimes(1)
    await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('这里保留当前工作区的消息。已读表示看过；紧急消息还需要点击‘我已知悉’。')
    expect(wrapper.text()).toContain('隔离提醒 normal-a')
    expect(harness.history).toHaveBeenCalledWith(expect.objectContaining({ workspaceAdminAccountId: 'workspace-a' }), expect.anything())
    expect(harness.progress).not.toHaveBeenCalled()
  })

  it('saves only the workspace notification interval from the existing settings page', async () => {
    const { wrapper } = await mountLayout('/admin/settings')
    const save = button(wrapper, '保存通知检查间隔')
    const label = wrapper.findAll('label').find(item => item.text().includes('通知检查间隔（分钟）'))
    expect(label).toBeDefined()
    const id = label!.attributes('for')
    const input = id ? wrapper.findAll('input').find(item => item.attributes('id') === id) : label!.find('input')
    expect(input?.exists()).toBe(true)
    expect((input!.element as HTMLInputElement).value).toBe('5')
    expect(wrapper.text()).toContain('默认每5分钟读取已保存的通知。只影响网页检查，不改变上游同步和主站。')
    await input!.setValue('7'); await save.trigger('click'); await flushPromises()
    expect(harness.savePreferences).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', intervalMinutes: 7 }, expect.anything())
    expect((input!.element as HTMLInputElement).value).toBe('7')
    expect(harness.saveStrategy).not.toHaveBeenCalled(); expect(harness.saveChannels).not.toHaveBeenCalled()
  })

  it('keeps read urgent notices pending after later and reopens only on the next normal check', async () => {
    const items = [notice('urgent-a', 4, true), notice('urgent-b', 4)]
    harness.summary.mockResolvedValue(summary(items))
    const { wrapper } = await mountLayout()
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认2条')
    button(wrapper, '我已知悉这2条')
    await button(wrapper, '稍后查看').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false)
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认2条')
    expect(harness.progress).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(299_999); await flushPromises()
    expect(harness.summary).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1); await flushPromises()
    expect(harness.summary).toHaveBeenCalledTimes(2)
    button(wrapper, '我已知悉这2条')
  })

  it('keeps a single layout timer across existing business routes', async () => {
    const { router } = await mountLayout()
    for (const path of ['/admin/upstream', '/admin/group-rates', '/admin/group-associations', '/admin/connection-health']) {
      await router.push(path); await flushPromises(); expect(harness.summary).toHaveBeenCalledTimes(1)
    }
    await vi.advanceTimersByTimeAsync(300_000); await flushPromises(); expect(harness.summary).toHaveBeenCalledTimes(2)
  })

  it('marks a detail read only after rendering while urgent remains pending until its explicit acknowledgement', async () => {
    const urgent = notice('urgent-detail', 4)
    harness.summary.mockResolvedValue(summary([urgent]))
    harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [urgent], nextCursor: null })
    const pending = new Promise<{ workspaceAdminAccountId: string; item: SiteNotification }>(resolve => { harness.detail.mockImplementationOnce(() => pending); harness.detailResolve = resolve })
    const { wrapper } = await mountLayout(); await bell(wrapper).trigger('click'); await flushPromises()
    const row = wrapper.findAll('button').find(b => b.text().includes('隔离提醒 urgent-detail'))!
    await row.trigger('click'); await flushPromises(); expect(harness.progress).not.toHaveBeenCalled()
    harness.detailResolve!({ workspaceAdminAccountId: 'workspace-a', item: urgent }); await flushPromises()
    expect(wrapper.find('[aria-label="通知详情"]').exists()).toBe(true)
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['urgent-detail'], action: 'read' }, expect.anything())
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认1条')
    await button(wrapper, '我已知悉这1条').trigger('click'); await flushPromises()
    expect(harness.progress).toHaveBeenLastCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['urgent-detail'], action: 'ack' }, expect.anything())
    expect(wrapper.text()).not.toContain('待确认1条')
  })

  it('keeps a displayed attention snapshot for eight seconds, with hover pausing the countdown', async () => {
    harness.summary.mockResolvedValue(summary([notice('attention', 2)])); const { wrapper } = await mountLayout()
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['attention'], action: 'present' }, expect.anything())
    const attention = () => wrapper.find('[aria-label="关注通知"]')
    expect(attention().exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(3000); await attention().trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(20_000); expect(attention().exists()).toBe(true)
    await attention().trigger('mouseleave'); await vi.advanceTimersByTimeAsync(4999); expect(attention().exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(1); expect(attention().exists()).toBe(false)
    expect(harness.progress).toHaveBeenCalledTimes(1)
  })

  it('keeps important reminders visible until an explicit dismiss and retains their historical facts', async () => {
    const important = notice('important', 3); harness.summary.mockResolvedValue(summary([important]))
    harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [important], nextCursor: null })
    const { wrapper } = await mountLayout(); await vi.advanceTimersByTimeAsync(9000)
    expect(wrapper.find('[aria-label="重要通知"]').exists()).toBe(true); expect(harness.progress).not.toHaveBeenCalled()
    await button(wrapper, '收起').trigger('click'); await flushPromises()
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['important'], action: 'dismiss' }, expect.anything())
    expect(wrapper.find('[aria-label="重要通知"]').exists()).toBe(false)
    await bell(wrapper).trigger('click'); await flushPromises(); expect(wrapper.text()).toContain('隔离提醒 important')
  })

  it('pauses attention while either mouse hover or keyboard focus remains inside it', async () => {
    harness.summary.mockResolvedValue(summary([notice('attention', 2)])); const { wrapper } = await mountLayout()
    const attention = wrapper.find('[aria-label="关注通知"]')
    await attention.trigger('mouseenter'); await attention.trigger('focusin')
    await attention.trigger('focusout', { relatedTarget: bell(wrapper).element })
    await vi.advanceTimersByTimeAsync(9000); expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(true)
    await attention.trigger('focusin'); await attention.trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(9000); expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(true)
    const inner = attention.find('button').element
    await attention.trigger('focusout', { relatedTarget: inner })
    await vi.advanceTimersByTimeAsync(9000); expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(true)
    await attention.trigger('focusout', { relatedTarget: bell(wrapper).element }); await vi.advanceTimersByTimeAsync(8000)
    expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(false)
  })

  it.each(['close','detail','workspace'])('clears previous attention pause when removed by %s so the next batch expires normally', async removal => {
    harness.summary.mockResolvedValueOnce(summary([notice('attention-first',2)])).mockResolvedValue(summary([notice('attention-next',2)]));harness.detail.mockResolvedValue({workspaceAdminAccountId:'workspace-a',item:notice('attention-first',2)})
    const {wrapper}=await mountLayout();const section=wrapper.find('[aria-label="关注通知"]');await section.trigger('mouseenter');await section.trigger('focusin')
    const controller=wrapper.findComponent(NotificationBell).props('controller')
    if(removal==='detail'){await section.findAll('button').find(b=>b.text()==='查看详情')!.trigger('click');await flushPromises();controller.closeDrawer()}
    else if(removal==='workspace'){
      harness.switching!.value=true;harness.current!.value={id:'workspace-b',displayName:'新隔离工作区',platform:'sub2api',identity:'fixture'};await flushPromises();expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(false)
      const next=summary([{...notice('attention-next',2),adminAccountId:'workspace-b'}]);next.workspaceAdminAccountId='workspace-b';harness.summary.mockResolvedValue(next);harness.switching!.value=false
    }
    else {await section.find('[aria-label="收起关注提醒"]').trigger('click')}
    if(removal!=='workspace'){await flushPromises();expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(false);await controller.refreshSummary()}
    await flushPromises();expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(8000);expect(wrapper.find('[aria-label="关注通知"]').exists()).toBe(false)
  })

  it('records only the rendered urgent detail on visibility recovery after a hidden response', async () => {
    let visible=true;vi.spyOn(document,'visibilityState','get').mockImplementation(()=>visible?'visible':'hidden')
    const urgent=notice('hidden-detail',4);harness.history.mockResolvedValue({workspaceAdminAccountId:'workspace-a',items:[urgent],nextCursor:null});harness.summary.mockResolvedValue(summary([urgent]))
    let resolve!:(value:{workspaceAdminAccountId:string;item:SiteNotification})=>void;harness.detail.mockReturnValue(new Promise(yes=>{resolve=yes}))
    const {wrapper}=await mountLayout('/admin',true);await bell(wrapper).trigger('click');await flushPromises();const dialog=document.querySelector<HTMLElement>('[role="dialog"]')!
    const row=Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b=>b.textContent?.includes('隔离提醒 hidden-detail'))!;await clickElement(row)
    visible=false;document.dispatchEvent(new Event('visibilitychange'));resolve({workspaceAdminAccountId:'workspace-a',item:urgent});await flushPromises();expect(harness.progress).not.toHaveBeenCalled()
    visible=true;document.dispatchEvent(new Event('visibilitychange'));await flushPromises()
    expect(harness.progress).toHaveBeenCalledTimes(1);expect(harness.progress).toHaveBeenCalledWith({workspaceAdminAccountId:'workspace-a',ids:['hidden-detail'],action:'read'},expect.anything());expect(wrapper.text().replace(/\s/g,'')).toContain('待确认1条')
  })

  it('labels failed sampling as its last read time and never calls it a trusted check', async () => {
    const response=summary();response.samplingState={autoSyncEnabled:false,unconfirmedSiteCount:1,sites:[{siteName:'失败验收网站',reason:'倍率接口没有返回可确认内容，请检查后重试',observedAt:when}]};harness.summary.mockResolvedValue(response)
    const {wrapper}=await mountLayout();await bell(wrapper).trigger('click');await flushPromises();expect(wrapper.text()).toContain('失败验收网站');expect(wrapper.text()).toContain('最近读取时间');expect(wrapper.text()).not.toContain('最近可信检查')
  })

  it('renders the actual rate producer fixed copy including unknown scope and ordinary history', async () => {
    const items=rateCopyFixture.map(row=>({...row,adminAccountId:'workspace-a',subjectActive:true,subjectInactiveMessage:''})) as SiteNotification[]
    harness.summary.mockResolvedValue(summary(items));harness.history.mockResolvedValue({workspaceAdminAccountId:'workspace-a',items,nextCursor:null})
    const {wrapper}=await mountLayout();expect(wrapper.findAll('h3').filter(h=>h.text()==='已登记主站接入的上游倍率发生变化')).toHaveLength(2)
    expect(wrapper.text()).toContain('验收网站·验收分组：上游倍率1×→2×。请检查成本、定价和调度安排；查看或确认不会修改主站。')
    expect(wrapper.text()).toContain('这个网站已有登记的主站接入，但当前无法确认受影响范围，请进入分组健康核查。')
    await button(wrapper,'稍后查看').trigger('click');await bell(wrapper).trigger('click');await flushPromises();expect(wrapper.text()).toContain('上游倍率发生变化；未发现这个分组的已登记主站接入。')
  })

  it('reports read errors without displaying the contradictory no-messages state', async () => {
    harness.summary.mockRejectedValue(new Error('数据库连接中断，请重试读取')); harness.history.mockRejectedValue(new Error('消息页连接中断，请重试'))
    const { wrapper } = await mountLayout(); await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('数据库连接中断'); expect(wrapper.text()).toContain('消息页连接中断')
    expect(wrapper.text()).not.toContain('当前没有已保存的消息。')
  })

  it('restores the confirmed input after preference save failure and shows no saved-success message', async () => {
    harness.savePreferences.mockRejectedValue(new Error('这次保存被数据库拒绝，请核查后重试'))
    const { wrapper } = await mountLayout('/admin/settings'); const input = wrapper.find('#notification-check-interval')
    await input.setValue('7'); await button(wrapper, '保存通知检查间隔').trigger('click'); await flushPromises()
    expect((input.element as HTMLInputElement).value).toBe('5'); expect(wrapper.text()).toContain('原间隔保持显示')
    expect(wrapper.text()).not.toContain('通知检查间隔已确认保存为')
    await input.setValue('1.5'); expect(button(wrapper, '保存通知检查间隔').attributes('disabled')).toBeDefined()
  })

  it('traps keyboard focus, closes with Escape, and restores the global bell focus', async () => {
    const { wrapper } = await mountLayout('/admin', true); (bell(wrapper).element as HTMLElement).focus(); await bell(wrapper).trigger('click'); await flushPromises()
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!; expect(document.activeElement).toBe(dialog)
    const focusable = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button:not([disabled])')); const first = focusable[0]!, last = focusable.at(-1)!
    last.focus(); dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })); await flushPromises(); expect(document.activeElement).toBe(first)
    first.focus(); dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true })); await flushPromises(); expect(document.activeElement).toBe(last)
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })); await flushPromises(); expect(document.querySelector('[role="dialog"]')).toBeNull(); expect(document.activeElement).toBe(bell(wrapper).element)
    expect(harness.progress).not.toHaveBeenCalled()
  })

  it.each([2,3] as const)('restores bell focus when a level %s summary detail entry disappears', async level => {
    const fact=notice('summary-focus',level);harness.summary.mockResolvedValue(summary([fact]));harness.detail.mockResolvedValue({workspaceAdminAccountId:'workspace-a',item:fact})
    harness.progress.mockImplementation(async request=>({workspaceAdminAccountId:'workspace-a',items:request.ids.map((id:string)=>({id,readAt:request.action==='read'?when:null,reminderCompletedAt:when}))}))
    const {wrapper}=await mountLayout('/admin',true);const section=wrapper.find(`section[aria-label="${level===2?'关注':'重要'}通知"]`)
    const view=section.findAll('button').find(b=>b.text()==='查看详情')!;expect(view.exists()).toBe(true)
    ;(view.element as HTMLElement).focus();await view.trigger('click');await flushPromises()
    const dialog=document.querySelector<HTMLElement>('[role="dialog"]')!;expect(dialog.contains(document.activeElement)).toBe(true)
    expect(wrapper.find(`section[aria-label="${level===2?'关注':'重要'}通知"]`).exists()).toBe(false)
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull();expect(document.activeElement).toBe(bell(wrapper).element);expect(document.activeElement).not.toBe(document.body)
  })
  it('keeps keyboard focus inside real drawer when entering and leaving a rendered detail', async () => {
    const fact = notice('keyboard-detail', 1); harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [fact], nextCursor: null }); harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item: fact })
    const { wrapper } = await mountLayout('/admin', true); (bell(wrapper).element as HTMLElement).focus(); await bell(wrapper).trigger('click'); await flushPromises()
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    const row = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('隔离提醒 keyboard-detail'))!
    row.focus(); await clickElement(row)
    expect(dialog.contains(document.activeElement)).toBe(true)
    const back = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('返回消息列表'))!
    back.focus(); await clickElement(back); expect(dialog.contains(document.activeElement)).toBe(true)
    const focusedRow = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('隔离提醒 keyboard-detail'))!
    focusedRow.focus(); await clickElement(focusedRow)
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })); await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull(); expect(document.activeElement).toBe(bell(wrapper).element)
  })

  it('transfers focus through urgent, slow detail, and drawer back to the original bell', async () => {
    const { wrapper } = await mountLayout('/admin', true); const bellElement = bell(wrapper).element as HTMLElement; bellElement.focus()
    const urgent = notice('keyboard-urgent', 4); harness.summary.mockResolvedValue(summary([urgent]))
    let resolve!: (value: { workspaceAdminAccountId: string; item: SiteNotification }) => void
    harness.detail.mockReturnValue(new Promise(yes => { resolve = yes }))
    await vi.advanceTimersByTimeAsync(300_000); await flushPromises()
    const modal = document.querySelector<HTMLElement>('[role="alertdialog"]')!; expect(document.activeElement).toBe(modal)
    const view = Array.from(modal.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent === '查看详情')!
    view.focus(); await clickElement(view)
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!; expect(dialog).not.toBeNull(); expect(dialog.contains(document.activeElement)).toBe(true)
    expect(harness.progress).not.toHaveBeenCalled(); expect(document.querySelector('[role="alertdialog"]')).toBeNull()
    resolve({ workspaceAdminAccountId: 'workspace-a', item: urgent }); await flushPromises(); expect(dialog.contains(document.activeElement)).toBe(true)
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })); await flushPromises()
    expect(document.activeElement).toBe(bellElement); expect(harness.progress).toHaveBeenCalledTimes(1)
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['keyboard-urgent'], action: 'read' }, expect.anything())
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认1条')
  })

  it('keeps focus inside an open drawer while page-read disables its triggering button', async () => {
    const { wrapper } = await mountLayout('/admin', true); (bell(wrapper).element as HTMLElement).focus(); await bell(wrapper).trigger('click'); await flushPromises()
    let resolve!: (value: { workspaceAdminAccountId: string; items: { id: string; readAt: string; reminderCompletedAt: null }[] }) => void
    harness.progress.mockReturnValueOnce(new Promise(yes => { resolve = yes }))
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!; const read = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent==='本页标为已读')!
    read.focus(); await clickElement(read); expect(read.disabled).toBe(true); expect(document.activeElement).toBe(dialog)
    resolve({ workspaceAdminAccountId: 'workspace-a', items: [{ id: 'normal-a', readAt: when, reminderCompletedAt: null }] }); await flushPromises()
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })); await flushPromises(); expect(document.querySelector('[role="dialog"]')).toBeNull(); expect(document.activeElement).toBe(bell(wrapper).element)
  })

  it('keeps focus inside urgent window while a rejected acknowledgement disables its button', async () => {
    harness.summary.mockResolvedValue(summary([notice('urgent-rejected',4)])); const { wrapper } = await mountLayout('/admin',true)
    let reject!: (error: Error) => void; harness.progress.mockReturnValueOnce(new Promise((_yes,no) => { reject=no }))
    const dialog=document.querySelector<HTMLElement>('[role="alertdialog"]')!; const ack=Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b=>b.textContent==='我已知悉这1条')!
    ack.focus(); await clickElement(ack); expect(ack.disabled).toBe(true); expect(document.activeElement).toBe(dialog)
    reject(new Error('这次知悉没有保存，请重新读取')); await flushPromises(); expect(dialog.isConnected).toBe(true)
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); await flushPromises();expect(document.querySelector('[role="alertdialog"]')).toBeNull();expect(wrapper.text().replace(/\s/g,'')).toContain('待确认1条')
  })
})

describe('inactive notification history and management result presentation', () => {
  it('shows stopped-history reasons and historical progress without subtracting another active message', async () => {
    const active = { ...notice('still-managed', 4), subjectType: 'upstream_site', subjectId: 'b' }
    const inactive = { ...notice('stopped-history', 4), subjectType: 'upstream_site', subjectId: 'a', subjectActive: false, subjectInactiveMessage: '这个站点已停止管理，这条消息保留为历史，不再主动提醒。' }
    harness.summary.mockResolvedValue(summary([active]))
    harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [inactive], nextCursor: null })
    harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item: inactive, checkAction: { available: true, message: '这个站点已停止管理；这里只查看已保存的倍率。' } })
    const { wrapper } = await mountLayout(); await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(inactive.subjectInactiveMessage)
    expect(wrapper.text()).toContain('未读（历史消息）'); expect(wrapper.text()).toContain('尚未知悉（已停止主动提醒）')
    expect(wrapper.text()).toContain('“全部消息”保留历史；“未读消息”和“待确认消息”只显示仍在管理的站点及其他有效消息。查看或确认不会修改主站。')
    expect(wrapper.text()).toContain('发现时间是本系统读到变化的时间，可能晚于上游实际调整；停止管理期间不会补采全部变化。')
    const row = wrapper.findAll('button').find(b => b.text().includes('隔离提醒 stopped-history'))!
    await row.trigger('click'); await flushPromises()
    expect(wrapper.find('[aria-label="通知详情"]').text()).toContain(inactive.subjectInactiveMessage)
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: [inactive.id], action: 'read' }, expect.anything())
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认1条')
    await button(wrapper, '我已知悉这1条').trigger('click'); await flushPromises()
    expect(wrapper.text().replace(/\s/g, '')).toContain('待确认1条')
    expect(harness.progress.mock.calls.at(-1)?.[0].ids).toEqual([inactive.id])
  })
  it('hides stale bell numbers during failed management verification and restores them only after a successful read', async () => {
    const active = { ...notice('pending-management', 4), subjectType: 'upstream_site', subjectId: 'a' }
    harness.summary.mockResolvedValueOnce(summary([active]))
    const { wrapper } = await mountLayout()
    const controller = wrapper.findComponent(NotificationBell).props('controller')
    harness.summary.mockRejectedValue(new Error('读取站点状态时数据库连接中断'))
    controller.siteManagementChanged('workspace-a', 'a', false); await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(true)
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false)
    expect(wrapper.findComponent(NotificationBell).text()).toContain('通知数量暂时无法确认')
    expect(wrapper.findComponent(NotificationBell).text()).not.toContain('待确认1条')
    expect(wrapper.findComponent(NotificationBell).find('[aria-live="polite"]').exists()).toBe(false)
    await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('通知数量暂时无法确认：读取站点状态时数据库连接中断')
    expect(wrapper.text()).toContain('旧数量暂不显示，新的自动提醒暂停；请点击“重试读取”。不会修改主站。')
    harness.summary.mockResolvedValueOnce(summary([active])); await button(wrapper, '重试读取').trigger('click'); await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(false)
    expect(wrapper.findComponent(NotificationBell).text().replace(/\s/g, '')).toContain('待确认1条')
  })
})

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
const bellText = (w: VueWrapper) => w.findComponent(NotificationBell).text().replace(/\s/g, '')
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
    expect(bellText(wrapper)).toContain('2条紧急消息待确认')
    button(wrapper, '我已知悉这2条')
    await button(wrapper, '稍后查看').trigger('click'); await flushPromises()
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false)
    expect(bellText(wrapper)).toContain('2条紧急消息待确认')
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
    expect(bellText(wrapper)).toContain('1条紧急消息待确认')
    await button(wrapper, '我已知悉这1条').trigger('click'); await flushPromises()
    expect(harness.progress).toHaveBeenLastCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['urgent-detail'], action: 'ack' }, expect.anything())
    expect(bellText(wrapper)).not.toContain('紧急消息待确认'); expect(bellText(wrapper)).not.toContain('重要提醒待处理')
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
    expect(harness.progress).toHaveBeenCalledTimes(1);expect(harness.progress).toHaveBeenCalledWith({workspaceAdminAccountId:'workspace-a',ids:['hidden-detail'],action:'read'},expect.anything());expect(bellText(wrapper)).toContain('1条紧急消息待确认')
  })

  it('labels failed sampling as its last read time and never calls it a trusted check', async () => {
    const response=summary();response.samplingState={autoSyncEnabled:false,unconfirmedSiteCount:1,sites:[{siteName:'失败验收网站',reason:'倍率接口没有返回可确认内容，请检查后重试',observedAt:when}]};harness.summary.mockResolvedValue(response)
    const {wrapper}=await mountLayout();await bell(wrapper).trigger('click');await flushPromises();expect(wrapper.findAll('p').find(p=>p.text().startsWith('失败验收网站：'))!.isVisible()).toBe(false);await button(wrapper,'查看原因（1个网站）').trigger('click');await flushPromises();expect(wrapper.findAll('p').find(p=>p.text().startsWith('失败验收网站：'))!.isVisible()).toBe(true);expect(wrapper.text()).toContain('失败验收网站');expect(wrapper.text()).toContain('最近读取时间');expect(wrapper.text()).not.toContain('最近可信检查')
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
    expect(bellText(wrapper)).toContain('1条紧急消息待确认')
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
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); await flushPromises();expect(document.querySelector('[role="alertdialog"]')).toBeNull();expect(bellText(wrapper)).toContain('1条紧急消息待确认')
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
    await button(wrapper, '全部消息').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(inactive.subjectInactiveMessage)
    expect(wrapper.text()).toContain('未读（历史消息）'); expect(wrapper.text()).toContain('尚未知悉（已停止主动提醒）')
    expect(wrapper.text()).toContain('“全部消息”保留历史；“未读消息”和“待确认消息”只显示仍在管理的站点及其他有效消息。查看或确认不会修改主站。')
    expect(wrapper.text()).toContain('发现时间是本系统读到变化的时间，可能晚于上游实际调整；停止管理期间不会补采全部变化。')
    const row = wrapper.findAll('button').find(b => b.text().includes('隔离提醒 stopped-history'))!
    await row.trigger('click'); await flushPromises()
    expect(wrapper.find('[aria-label="通知详情"]').text()).toContain(inactive.subjectInactiveMessage)
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: [inactive.id], action: 'read' }, expect.anything())
    expect(bellText(wrapper)).toContain('1条紧急消息待确认')
    await button(wrapper, '我已知悉这1条').trigger('click'); await flushPromises()
    expect(bellText(wrapper)).toContain('1条紧急消息待确认')
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
    for (const text of ['紧急消息待确认', '重要提醒待处理', '有未读消息']) expect(bellText(wrapper)).not.toContain(text)
    expect(wrapper.findComponent(NotificationBell).find('[aria-live="polite"]').exists()).toBe(false)
    await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('通知数量暂时无法确认：读取站点状态时数据库连接中断')
    expect(wrapper.text()).toContain('旧数量暂不显示，新的自动提醒暂停；请点击“重试读取”。不会修改主站。')
    harness.summary.mockResolvedValueOnce(summary([active])); await button(wrapper, '重试读取').trigger('click'); await flushPromises()
    expect(controller.managementRefreshPending.value).toBe(false)
    expect(bellText(wrapper)).toContain('1条紧急消息待确认')
  })
})

describe('notification bell and drawer display states', () => {
  const controller = (w: VueWrapper) => w.findComponent(NotificationBell).props('controller')
  const bellCopy = (w: VueWrapper) => w.findComponent(NotificationBell).text().replace(/\s/g, '')
  const marker = (w: VueWrapper) => bell(w).find('[aria-live="polite"]')
  const visibleMarkerText = (w: VueWrapper) => {
    const clone = marker(w).element.cloneNode(true) as HTMLElement
    clone.querySelectorAll('.sr-only').forEach(node => node.remove())
    return clone.textContent?.trim()
  }
  const heading = (w: VueWrapper) => w.findAll('h3').find(h => h.text() === '待确认')
  const zone = (w: VueWrapper) => {
    expect(heading(w), '待确认区应有独立标题').toBeDefined()
    return heading(w)!.element.parentElement!
  }
  const cards = (w: VueWrapper) => Array.from(zone(w).querySelectorAll<HTMLElement>('div.border-destructive\\/30'))
  const cardButton = (w: VueWrapper, id: string, label: string) => {
    const card = cards(w).find(c => c.textContent?.includes(`隔离提醒 ${id}`))
    expect(card, `待确认卡片 ${id}`).toBeDefined()
    const found = Array.from(card!.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent === label)
    expect(found, label).toBeDefined()
    return new DOMWrapper(found!)
  }
  const row = (w: VueWrapper, id: string) => w.findAll('button').find(b => b.text().includes(`隔离提醒 ${id}`))
  const open = async (items: SiteNotification[], response = summary(items), history = items) => {
    harness.summary.mockResolvedValue(response)
    harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: history, nextCursor: null })
    const { wrapper } = await mountLayout()
    const later = wrapper.findAll('button').find(b => b.text() === '稍后查看')
    if (later) { await later.trigger('click'); await flushPromises() }
    await bell(wrapper).trigger('click'); await flushPromises()
    return wrapper
  }
  const notes = [
    '新功能首次取得可信倍率时开始比较，不补发无法确认的旧变化。',
    '发现时间是本系统读到变化的时间，可能晚于上游实际调整；停止管理期间不会补采全部变化。',
    '手工接入且未在本系统关联的账号，可能无法按主站接入分类。',
  ]

  it('R1 shows a polite red urgent count after later and removes the separate pending button', async () => {
    harness.summary.mockResolvedValue(summary([notice('u1', 4), notice('u2', 4)]))
    const { wrapper } = await mountLayout(); await button(wrapper, '稍后查看').trigger('click'); await flushPromises()
    expect(bellCopy(wrapper)).toContain('2条紧急消息待确认')
    expect(marker(wrapper).classes()).toContain('bg-destructive')
    expect(marker(wrapper).attributes('aria-live')).toBe('polite')
    expect(wrapper.text()).not.toContain('待确认2条')
    expect(wrapper.find('[aria-live="assertive"]').exists()).toBe(false)
    expect(bell(wrapper).attributes('title')).toBe(bell(wrapper).attributes('aria-label'))
  })
  it('R2 shows only the yellow important count', async () => {
    harness.summary.mockResolvedValue(summary([notice('i1', 3)])); const { wrapper } = await mountLayout()
    expect(bellCopy(wrapper)).toContain('1条重要提醒待处理'); expect(marker(wrapper).classes()).toContain('bg-warning')
    expect(bellCopy(wrapper)).not.toContain('紧急消息待确认'); expect(bellCopy(wrapper)).not.toContain('有未读消息')
  })
  it.each([1, 2] as const)('R3 shows an unnumbered dot for level %s unread messages', async level => {
    const items = Array.from({ length: level === 1 ? 3 : 1 }, (_, n) => ({ ...notice(`n${n}`, level), reminderCompletedAt: level === 2 ? when : null }))
    harness.summary.mockResolvedValue(summary(items)); const { wrapper } = await mountLayout()
    expect(bellCopy(wrapper)).toContain('有未读消息'); expect(marker(wrapper).classes()).toContain('bg-primary')
    expect(visibleMarkerText(wrapper)).toBe(''); expect(bellCopy(wrapper)).not.toMatch(/\d/)
  })
  it('R4 gives urgent precedence over important', async () => {
    const wrapper = await open([notice('u1', 4), notice('i1', 3)])
    expect(bellCopy(wrapper)).toContain('1条紧急消息待确认'); expect(marker(wrapper).classes()).toContain('bg-destructive')
    expect(bellCopy(wrapper)).not.toContain('重要提醒待处理'); expect(marker(wrapper).classes()).not.toContain('bg-warning')
  })
  it.each([1, 21])('R5 dismisses just the displayed important batch out of %s', async count => {
    const items = Array.from({ length: Math.min(count, 20) }, (_, n) => notice(`i${n}`, 3))
    const response = summary(items); response.active.important.totalCount = count; response.unreadCount = count
    if (count > 20) response.active.important.nextCursor = 'next-important'
    harness.summary.mockResolvedValue(response); const { wrapper } = await mountLayout()
    expect(bellCopy(wrapper)).toContain(`${count}条重要提醒待处理`)
    await button(wrapper, '收起').trigger('click'); await flushPromises()
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: items.map(i => i.id), action: 'dismiss' }, expect.anything())
    expect(wrapper.find('[aria-label="重要通知"]').exists()).toBe(false)
    expect(bellCopy(wrapper)).not.toContain('紧急消息待确认')
    if (count === 1) {
      expect(bellCopy(wrapper)).toContain('有未读消息'); expect(bellCopy(wrapper)).not.toContain('重要提醒待处理')
    } else {
      expect(bellCopy(wrapper)).toContain('1条重要提醒待处理'); expect(bellCopy(wrapper)).not.toContain('有未读消息')
    }
    expect(controller(wrapper).unreadCount.value).toBe(count)
  })
  it('R6 shows no marker when all messages have been read and handled', async () => {
    const item = { ...notice('done', 1, true), reminderCompletedAt: when }
    harness.summary.mockResolvedValue(summary([item])); const { wrapper } = await mountLayout()
    expect(bell(wrapper).exists()).toBe(true); expect(marker(wrapper).exists()).toBe(false)
    for (const text of ['紧急消息待确认', '重要提醒待处理', '有未读消息']) expect(bellCopy(wrapper)).not.toContain(text)
  })
  it.each([{ level: 4 as const, count: 120, copy: '紧急消息待确认' }, { level: 3 as const, count: 150, copy: '重要提醒待处理' }])('R7 caps the visible level $level count and keeps actual accessible count', async ({ level, count, copy }) => {
    const response = summary([notice('large', level)])
    if (level === 4) { response.pendingCriticalCount = count; response.active.critical.totalCount = count }
    else response.active.important.totalCount = count
    harness.summary.mockResolvedValue(response); const { wrapper } = await mountLayout()
    expect(bellCopy(wrapper)).toContain(`${count}条${copy}`); expect(visibleMarkerText(wrapper)).toBe('99+')
    expect(visibleMarkerText(wrapper)).not.toContain(String(count)); expect(marker(wrapper).find('.sr-only').text()).toBe(`${count}条${copy}`)
  })
  it('R8 defaults to unread only when a confirmed unread count exists', async () => {
    const wrapper = await open([notice('n1', 1)])
    expect(harness.history).toHaveBeenLastCalledWith(expect.objectContaining({ filter: 'unread' }), expect.anything())
    expect(button(wrapper, '未读消息').attributes('aria-pressed')).toBe('true'); expect(button(wrapper, '全部消息').attributes('aria-pressed')).toBe('false')
    controller(wrapper).closeDrawer(); controller(wrapper).unreadCount.value = 0
    await controller(wrapper).openDrawer(); await flushPromises()
    expect(harness.history).toHaveBeenLastCalledWith(expect.objectContaining({ filter: 'all' }), expect.anything())
    expect(button(wrapper, '未读消息').attributes('aria-pressed')).toBe('false')
    controller(wrapper).closeDrawer(); controller(wrapper).managementRefreshPending.value = true; controller(wrapper).unreadCount.value = 2
    harness.summary.mockRejectedValue(new Error('站点状态待重读'))
    await controller(wrapper).openDrawer(); await flushPromises()
    expect(harness.history).toHaveBeenLastCalledWith(expect.objectContaining({ filter: 'all' }), expect.anything())
    expect(button(wrapper, '全部消息').attributes('aria-pressed')).toBe('true')
  })
  it('R9 gives all eight history states their processing tone and discovery time', async () => {
    const items = [notice('urgent-unread', 4), notice('urgent-read', 4, true), { ...notice('acked', 4, true), reminderCompletedAt: when }, notice('important', 3), notice('attention', 2), notice('normal-unread', 1), notice('normal-read', 1, true), { ...notice('stopped', 4), subjectActive: false, subjectInactiveMessage: '这个站点已停止管理，保留历史。' }]
    const wrapper = await open([], summary(), items); await button(wrapper, '全部消息').trigger('click'); await flushPromises()
    const backgrounds = ['bg-destructive/10', 'bg-destructive/10', null, 'bg-warning/10', 'bg-primary/5', 'bg-primary/5', null, null]
    for (let i = 0; i < items.length; i++) {
      const entry = row(wrapper, items[i].id)!; expect(entry, items[i].id).toBeDefined()
      expect(entry.text()).toContain('发现时间：')
      for (const cls of ['bg-destructive/10', 'bg-warning/10', 'bg-primary/5']) expect(entry.classes().includes(cls)).toBe(cls === backgrounds[i])
      expect(entry.find('.font-semibold').exists()).toBe(Boolean(backgrounds[i]))
      expect(entry.classes().includes('text-muted-foreground')).toBe(!backgrounds[i])
      expect(entry.find('.h-2.w-2.bg-primary').exists()).toBe(i === 4 || i === 5)
      expect(entry.find('.line-clamp-2').exists()).toBe(true)
    }
    expect(row(wrapper, 'stopped')!.text()).toContain('未读（历史消息） · 尚未知悉（已停止主动提醒）')
  })
  it('R10 explains color and list action once and removes old repeated copy', async () => {
    const wrapper = await open([notice('n1', 1), notice('n2', 1)])
    const copy = '点开一条消息可查看详情；显示详情后只把这一条标为已读。'
    expect(wrapper.text().split(copy)).toHaveLength(2)
    expect(wrapper.text()).toContain('紧急（红底）：优先确认，点‘我已知悉’后才变灰；重要（黄底）：尽快检查；关注、普通（蓝底）：建议查看或知道即可；其他消息看过后变灰，不再有底色；网站停止管理后保留的历史消息也是灰色。')
    expect(wrapper.text()).toContain('铃铛上：红色数字是待确认的紧急消息，黄色数字是还没收起或查看的重要提醒，小圆点表示还有没看过的消息。')
    expect(wrapper.text()).not.toContain('打开详情；显示后仅标记这一条已读。')
    expect(wrapper.text()).not.toContain('普通：知道即可；关注：建议查看；重要：尽快检查；紧急：优先确认。')
  })
  it('R11 acknowledges exactly one independent urgent card', async () => {
    const wrapper = await open([notice('u1', 4), notice('u2', 4)])
    expect(cards(wrapper)).toHaveLength(2); expect(heading(wrapper)!.text()).toBe('待确认')
    expect(zone(wrapper).textContent).toContain('这些紧急消息需要逐条点‘我已知悉’；只停止提醒，不表示定价或主站已调整。')
    expect(cards(wrapper)[0].tagName).toBe('DIV'); expect(cards(wrapper)[0].querySelector('button h3')).toBeNull()
    expect(cards(wrapper)[0].textContent).toContain('发现时间：')
    await cardButton(wrapper, 'u1', '我已知悉这1条').trigger('click'); await flushPromises()
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['u1'], action: 'ack' }, expect.anything())
    expect(cards(wrapper)).toHaveLength(1); expect(zone(wrapper).textContent).not.toContain('隔离提醒 u1'); expect(zone(wrapper).textContent).toContain('隔离提醒 u2')
  })
  it('R12 hides pending cards at the defined boundaries and uses nextCursor for overflow', async () => {
    const item = notice('u1', 4), response = summary([item]); response.active.critical.nextCursor = 'more'
    const wrapper = await open([item], response)
    expect(cards(wrapper)).toHaveLength(1)
    const more = '还有更多待确认的紧急消息，请点‘待确认消息’查看。'
    expect(zone(wrapper).textContent).toContain(more)
    controller(wrapper).summary.value = summary([item]); await flushPromises(); expect(zone(wrapper).textContent).not.toContain(more)
    harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item })
    await cardButton(wrapper, 'u1', '查看详情').trigger('click'); await flushPromises()
    expect(wrapper.find('[aria-label="通知详情"]').exists()).toBe(true); expect(heading(wrapper)).toBeUndefined()
    await button(wrapper, '返回消息列表').trigger('click'); await flushPromises(); expect(heading(wrapper)).toBeDefined()
    controller(wrapper).managementRefreshPending.value = true; await flushPromises(); expect(heading(wrapper)).toBeUndefined()
    controller(wrapper).managementRefreshPending.value = false; await flushPromises()
    await cardButton(wrapper, 'u1', '我已知悉这1条').trigger('click'); await flushPromises(); expect(heading(wrapper)).toBeUndefined()
    expect(wrapper.text()).not.toContain(more)
  })
  it.each(['rejected', 'unknown', 'confirmed', 'busy'])('R13 preserves acknowledgement outcome and duplicate protection: %s', async outcome => {
    const item = notice('u1', 4), wrapper = await open([item]); expect(cards(wrapper)).toHaveLength(1)
    let resolve!: (value: unknown) => void
    if (outcome === 'rejected') harness.progress.mockRejectedValueOnce(new Error('保存被拒绝，请核查通知权限后重试'))
    if (outcome === 'unknown' || outcome === 'confirmed') {
      harness.progress.mockRejectedValueOnce(new TypeError('连接中断'))
      harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item: outcome === 'confirmed' ? { ...item, readAt: when, reminderCompletedAt: when } : item })
    }
    if (outcome === 'busy') harness.progress.mockReturnValueOnce(new Promise(yes => { resolve = yes }))
    const ack = cardButton(wrapper, 'u1', '我已知悉这1条'); await ack.trigger('click'); await flushPromises()
    if (outcome === 'busy') {
      expect(controller(wrapper).busyIds.value).toContain('u1'); expect(cardButton(wrapper, 'u1', '我已知悉这1条').attributes('disabled')).toBeDefined(); await cardButton(wrapper, 'u1', '我已知悉这1条').trigger('click'); expect(harness.progress).toHaveBeenCalledTimes(1)
      resolve({ workspaceAdminAccountId: 'workspace-a', items: [{ id: 'u1', readAt: when, reminderCompletedAt: when }] }); await flushPromises()
    }
    if (outcome === 'confirmed' || outcome === 'busy') {
      expect(heading(wrapper)).toBeUndefined(); expect(bellCopy(wrapper)).not.toContain('紧急消息待确认'); expect(wrapper.text()).not.toContain('知悉保存结果暂时无法确认')
    } else {
      expect(cards(wrapper)).toHaveLength(1); expect(bellCopy(wrapper)).toContain('1条紧急消息待确认')
      expect(wrapper.text()).toContain(outcome === 'rejected' ? '知悉没有保存' : '知悉保存结果暂时无法确认')
      expect(wrapper.text()).not.toContain(outcome === 'rejected' ? '知悉保存结果暂时无法确认' : '知悉没有保存')
    }
  })
  it('R14 replaces pending cards on normal background summary checks', async () => {
    const wrapper = await open([notice('first', 4)]); expect(cards(wrapper)).toHaveLength(1)
    harness.summary.mockResolvedValue(summary([notice('u1', 4), notice('u2', 4)])); await vi.advanceTimersByTimeAsync(300_000); await flushPromises()
    expect(cards(wrapper)).toHaveLength(2); expect(zone(wrapper).textContent).not.toContain('隔离提醒 first')
    harness.summary.mockResolvedValue(summary()); await vi.advanceTimersByTimeAsync(300_000); await flushPromises()
    expect(heading(wrapper)).toBeUndefined(); expect(bellCopy(wrapper)).not.toContain('紧急消息待确认')
  })
  it('R15 discards a late acknowledgement after switching workspace', async () => {
    const wrapper = await open([notice('u1', 4)]); expect(cards(wrapper)).toHaveLength(1)
    let resolve!: (value: unknown) => void; harness.progress.mockReturnValueOnce(new Promise(yes => { resolve = yes }))
    await cardButton(wrapper, 'u1', '我已知悉这1条').trigger('click'); await flushPromises()
    harness.switching!.value = true; harness.current!.value = { id: 'workspace-b', displayName: '新工作区', platform: 'sub2api', identity: 'fixture' }; await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    const next = summary(); next.workspaceAdminAccountId = 'workspace-b'
    harness.summary.mockResolvedValue(next); harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-b', items: [], nextCursor: null }); harness.switching!.value = false; await flushPromises()
    resolve({ workspaceAdminAccountId: 'workspace-a', items: [{ id: 'u1', readAt: when, reminderCompletedAt: when }] }); await flushPromises()
    await bell(wrapper).trigger('click'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true); expect(heading(wrapper)).toBeUndefined()
    expect(wrapper.text()).not.toContain('隔离提醒 u1'); expect(bellCopy(wrapper)).not.toContain('紧急消息待确认')
  })
  it('R16 folds site reasons with accessible state and resets them on reopening', async () => {
    const response = summary(); response.samplingState = { autoSyncEnabled: false, unconfirmedSiteCount: 2, sites: ['网站甲', '网站乙'].map(siteName => ({ siteName, reason: '读取未确认，请重试', observedAt: when })) }
    const wrapper = await open([], response)
    const reason = () => wrapper.findAll('p').find(p => p.text().startsWith('网站甲：'))!
    expect(reason().isVisible()).toBe(false)
    expect(button(wrapper, '查看原因（2个网站）').attributes('aria-expanded')).toBe('false')
    await button(wrapper, '查看原因（2个网站）').trigger('click'); await flushPromises()
    expect(reason().isVisible()).toBe(true); expect(button(wrapper, '收起原因').attributes('aria-expanded')).toBe('true')
    await button(wrapper, '收起原因').trigger('click'); expect(reason().isVisible()).toBe(false)
    await button(wrapper, '查看原因（2个网站）').trigger('click'); controller(wrapper).closeDrawer(); await flushPromises()
    await bell(wrapper).trigger('click'); await flushPromises(); expect(reason().isVisible()).toBe(false)
    expect(button(wrapper, '查看原因（2个网站）').attributes('aria-expanded')).toBe('false')
  })
  it('R17 shows the normal sync sentence only with a current fully confirmed summary', async () => {
    const response = summary(); response.samplingState.autoSyncEnabled = true
    const wrapper = await open([], response), normal = '自动同步已开启，已启用网站最近一次倍率读取都已确认。'
    expect(wrapper.text()).toContain(normal)
    const warning = { message: '读取配置失败', detail: '无法确认状态', next: '请重试' }
    for (const state of [
      { autoSyncEnabled: false, unconfirmedSiteCount: 0, sites: [] },
      { autoSyncEnabled: null, unconfirmedSiteCount: 0, sites: [], warning },
      { autoSyncEnabled: true, unconfirmedSiteCount: 1, sites: [] },
      { autoSyncEnabled: true, unconfirmedSiteCount: 0, sites: [], warning },
    ]) {
      controller(wrapper).summary.value = { ...response, samplingState: state }; await flushPromises(); expect(wrapper.text()).not.toContain(normal)
    }
    controller(wrapper).summary.value = response; controller(wrapper).managementRefreshPending.value = true; await flushPromises(); expect(wrapper.text()).not.toContain(normal)
    controller(wrapper).managementRefreshPending.value = false; controller(wrapper).summary.value = null; await flushPromises(); expect(wrapper.text()).not.toContain(normal)
  })
  it('R18 folds the original notes in list and detail modes and resets on reopening', async () => {
    const item = notice('n1', 1), wrapper = await open([item])
    const noteRows = () => notes.map(text => wrapper.findAll('p').find(p => p.text() === text)!)
    expect(noteRows().every(p => !p.isVisible())).toBe(true)
    expect(button(wrapper, '关于这些提醒').attributes('aria-expanded')).toBe('false')
    await button(wrapper, '关于这些提醒').trigger('click'); await flushPromises()
    expect(noteRows().every(p => p.isVisible())).toBe(true); expect(button(wrapper, '收起说明').attributes('aria-expanded')).toBe('true')
    await button(wrapper, '收起说明').trigger('click'); expect(noteRows().every(p => !p.isVisible())).toBe(true)
    harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item }); await row(wrapper, 'n1')!.trigger('click'); await flushPromises()
    expect(wrapper.find('[aria-label="通知详情"]').exists()).toBe(true)
    await button(wrapper, '关于这些提醒').trigger('click'); expect(noteRows().every(p => p.isVisible())).toBe(true)
    await button(wrapper, '收起说明').trigger('click'); expect(noteRows().every(p => !p.isVisible())).toBe(true)
    await button(wrapper, '关于这些提醒').trigger('click'); controller(wrapper).closeDrawer(); await flushPromises()
    await bell(wrapper).trigger('click'); await flushPromises(); expect(noteRows().every(p => !p.isVisible())).toBe(true)
  })
  it('R19 retains real Teleport keyboard trapping and Escape focus restoration', async () => {
    const item = notice('u1', 4), response = summary([item]); response.samplingState.unconfirmedSiteCount = 1; response.samplingState.sites = [{ siteName: '键盘网站', reason: '请重试', observedAt: when }]
    harness.summary.mockResolvedValue(response); harness.history.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', items: [item], nextCursor: null })
    const { wrapper } = await mountLayout('/admin', true); await controller(wrapper).openDrawer(); await flushPromises()
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    // R16/R18 require the new controls. This reverse guard also runs on the old layout.
    for (const labels of [['查看原因（1个网站）', '收起原因'], ['关于这些提醒', '收起说明']]) {
      for (const label of labels) {
        const control = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent === label)
        if (control) await clickElement(control)
      }
    }
    const focusable = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button:not([disabled])'))
    const first = focusable[0], last = focusable[focusable.length - 1]
    last.focus(); last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })); expect(document.activeElement).toBe(first)
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true })); expect(document.activeElement).toBe(last)
    const pendingButton = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent === '我已知悉这1条') ?? last
    pendingButton.focus(); pendingButton.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })); await flushPromises()
    expect(document.querySelector('[role="dialog"]')).toBeNull(); expect(document.activeElement).toBe(bell(wrapper).element); expect(document.activeElement).not.toBe(document.body)
  })
  it('R20 keeps urgent cards after viewing while removing only the read item from unread history', async () => {
    const items = [notice('u1', 4), notice('u2', 4)], wrapper = await open(items); expect(cards(wrapper)).toHaveLength(2)
    harness.detail.mockResolvedValue({ workspaceAdminAccountId: 'workspace-a', item: items[0] })
    await cardButton(wrapper, 'u1', '查看详情').trigger('click'); await flushPromises()
    expect(harness.detail).toHaveBeenCalledWith('workspace-a', 'u1', expect.anything())
    expect(harness.progress).toHaveBeenCalledWith({ workspaceAdminAccountId: 'workspace-a', ids: ['u1'], action: 'read' }, expect.anything())
    await button(wrapper, '返回消息列表').trigger('click'); await flushPromises()
    expect(cards(wrapper)).toHaveLength(2); expect(zone(wrapper).textContent).toContain('隔离提醒 u1'); expect(zone(wrapper).textContent).toContain('隔离提醒 u2')
    expect(row(wrapper, 'u1')).toBeUndefined(); expect(row(wrapper, 'u2')).toBeDefined()
    expect(bellCopy(wrapper)).toContain('2条紧急消息待确认'); expect(bellCopy(wrapper)).not.toContain('1条紧急消息待确认')
  })
  it.each(['success', 'failure'])('R21 uses the re-read summary after acknowledging a card outside displayed history: %s', async outcome => {
    const items = [notice('u1', 4), notice('u2', 4), notice('n1', 1)], wrapper = await open(items, summary(items), [items[2]])
    expect(cards(wrapper)).toHaveLength(2); const before = harness.summary.mock.calls.length
    const refreshed = summary([items[1], items[2]]); refreshed.pendingCriticalCount = refreshed.active.critical.totalCount = 5
    if (outcome === 'success') harness.summary.mockResolvedValue(refreshed)
    else harness.summary.mockRejectedValue(new Error('摘要数据库连接中断，请重试'))
    await cardButton(wrapper, 'u1', '我已知悉这1条').trigger('click'); await flushPromises()
    expect(harness.summary).toHaveBeenCalledTimes(before + 1); expect(cards(wrapper)).toHaveLength(1)
    expect(zone(wrapper).textContent).not.toContain('隔离提醒 u1'); expect(zone(wrapper).textContent).toContain('隔离提醒 u2')
    expect(bellCopy(wrapper)).toContain(`${outcome === 'success' ? 5 : 2}条紧急消息待确认`); expect(bellCopy(wrapper)).not.toContain('1条紧急消息待确认')
    if (outcome === 'failure') { expect(wrapper.text()).toContain('通知暂时无法读取'); expect(button(wrapper, '重试读取').isVisible()).toBe(true) }
    else expect(wrapper.text()).not.toContain('通知暂时无法读取')
  })
})

// @vitest-environment jsdom
import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import AdminGroupAccountsDialog from '@/modules/admin/components/dashboard/AdminGroupAccountsDialog.vue'
import ConnectionHealthEventsDialog from '@/modules/admin/components/dashboard/ConnectionHealthEventsDialog.vue'
import { useConnectionHealth, buildConnectionHealthRecordSummary, latestConnectionHealthProbeFailure, remoteActionLabelKey } from '@/modules/admin/composables/useConnectionHealth'
import { t } from '@/locales'
import type { AdminGroupAccount, AdminGroupHealth, ConnectionHealthEvent } from '@/modules/admin/types/connectionHealth'

type Account = AdminGroupAccount & { priorityActionPending?: boolean; priorityUsesMultiplierOnly?: boolean }
type ActionResult = { targetId: string; result: 'success' | 'not_sent' | 'pending' | 'noop'; priority?: number; concurrency?: number; loadFactor?: number; errorKey?: string }
const targetId = 'sub2api:task-b-workspace:task-b-account'
const account = (overrides: Partial<Account> = {}): Account => ({
  id: 'task-b-account', name: 'Task B account', platform: 'openai', type: 'subscription', status: 'active', schedulable: true,
  targetId, priority: 5, concurrency: 1000, loadFactor: 30, accountTier: 1, probeAvailable: true, modelHealth: [],
  hasAssignedPolicy: true, hasEnabledPolicy: true, hasEnabledProbePolicy: true, probeModelsConfigured: true,
  priorityManaged: false, priorityUsesMultiplierOnly: false, priorityActionPending: false, ...overrides,
})
const group = (row: Account, id = 'task-b-group'): AdminGroupHealth => ({
  id, name: id, platform: 'openai', status: 'active', type: 'standard', isExclusive: false, subscriptionType: '',
  multiplier: 1, multiplierDisplay: '1x', accountCount: 1, monitoredAccountCount: 1,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0,
    suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null }, accounts: [row],
})
const wrappers: VueWrapper[] = []
const releases: Array<() => void> = []
const service = useConnectionHealth() as ReturnType<typeof useConnectionHealth> & {
  applyAccountPriority?: (result: ActionResult) => void
  applyAccountConcurrency?: (result: ActionResult) => void
}
let stored: Account
let result: ActionResult['result']
let errorKey: string
let status: number
let writes: Array<{ url: string; body: unknown }>
let readOverride: (() => Promise<Response>) | undefined
let saveOverride: (() => Promise<Response>) | undefined
let failRead: boolean
const json = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })
const payload = () => [group({ ...stored }, 'one'), group({ ...stored }, 'two')]
const deferred = <T,>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  releases.push(() => resolve(json([]) as T))
  return { promise, resolve }
}
beforeEach(() => {
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('task-b-workspace')
  stored = account(); result = 'success'; errorKey = ''; status = 200; writes = []; readOverride = undefined; saveOverride = undefined; failRead = false
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = decodeURIComponent(String(input))
    if (url.endsWith('/admin-groups')) return readOverride ? readOverride() : failRead ? json({ message: 'admin.connectionHealth.errors.network' }, 500) : json(payload())
    if (url.endsWith('/priority-owner') || url.endsWith('/concurrency')) {
      const body = JSON.parse(String(init?.body)); writes.push({ url, body })
      if (saveOverride) return saveOverride()
      if (status !== 200) return json({ message: errorKey }, status)
      if (result === 'success') {
        if (url.endsWith('/priority-owner')) stored = { ...stored, priority: body.mode === 'manual' ? body.priority : stored.accountTier === 1 ? 10 : 99,
          priorityManaged: body.mode === 'auto', priorityExpected: body.mode === 'auto' ? (stored.accountTier === 1 ? 10 : 99) : undefined, priorityActionPending: false }
        else stored = { ...stored, concurrency: body.concurrency, loadFactor: stored.loadFactor ? body.concurrency : stored.loadFactor }
      }
      if (result === 'pending' && url.endsWith('/priority-owner')) stored = { ...stored, priorityActionPending: true, priorityManaged: true }
      return json({ targetId, result, priority: stored.priority, concurrency: stored.concurrency, loadFactor: stored.loadFactor, errorKey })
    }
    return json([])
  }))
})
afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  service.setAdminGroupsWorkspace('')
  for (const release of releases.splice(0)) release()
  await flushPromises()
  vi.unstubAllGlobals(); document.body.innerHTML = ''
})
const detail = (row: Account) => {
  const wrapper = mount(AdminGroupHealthDetail, { props: { group: group(row), hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false } })
  wrappers.push(wrapper); return wrapper
}
const liveDetails = async () => {
  await service.loadAdminGroups()
  const wrapper = mount(defineComponent({ setup: () => () => h('div', service.adminGroups.value.map(entry => h(AdminGroupHealthDetail, {
    group: entry, hideUnmonitoredAccounts: false, questionAnswerUnreadTargetIds: [], actionLoading: false,
    onPrioritySaved: (saved: ActionResult) => service.applyAccountPriority?.(saved),
    onConcurrencySaved: (saved: ActionResult) => service.applyAccountConcurrency?.(saved),
  }))) }))
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
const edit = async (wrapper: VueWrapper, field: 'Priority' | '并发') => {
  await wrapper.findAll(`button[aria-label="编辑${field}"]`)[0]!.trigger('click'); await flushPromises()
}
const save = async (wrapper: VueWrapper, field: 'Priority' | '并发') => {
  await wrapper.findAll(`button[aria-label="保存${field}"]`)[0]!.trigger('click'); await flushPromises()
}

describe('Task B account management', () => {
  it('marks ordinary 1–9 as manual and offers existing-style Priority and concurrency editors', () => {
    const wrapper = detail(account())
    expect(wrapper.text()).toContain('人工 Priority，调度站不管理')
    expect(wrapper.text()).toContain('交给调度站后按主力排序')
    expect(wrapper.text()).toContain('并发 1000')
    expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(true)
    expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(true)
  })

  it('prioritizes pending over unknown/manual and retains real values and the last confirmed value', async () => {
    const wrapper = detail(account({ priorityActionPending: true, priorityUsesMultiplierOnly: undefined, priorityManaged: true, priorityExpected: 10 }))
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
    expect(wrapper.text()).toContain('待写入 10')
    expect(wrapper.text()).not.toContain('人工 Priority')
    expect(wrapper.text()).not.toContain('交给调度站后按主力排序')
    expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
    await wrapper.setProps({ group: group(account({ priorityActionPending: true, priorityUsesMultiplierOnly: false, priorityManaged: true, priorityExpected: 10 })) })
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
  })

  it('keeps old responses and incomplete shared-account policy information unknown after repeated refresh', async () => {
    stored = account({ priorityUsesMultiplierOnly: undefined, priorityManaged: true, priorityExpected: 10 })
    const wrapper = await liveDetails()
    for (let retry = 0; retry < 2; retry++) {
      expect(wrapper.text()).toContain('管理方式暂无法确认')
      expect(wrapper.text()).toContain('上次确认写入 10')
      expect(wrapper.text()).not.toContain('人工 Priority')
      expect(wrapper.text()).not.toContain('交给调度站后按主力排序')
      expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
      expect(wrapper.find('button[aria-label="编辑账号层级"]').exists()).toBe(true)
      expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(true)
      await service.loadAdminGroups(); await flushPromises()
    }
  })

  it.each([true, false])('uses actual multiplier-only management state without manual labels or handoff: managed=%s', managed => {
    const wrapper = detail(account({ priorityUsesMultiplierOnly: true, priorityManaged: managed,
      effectivePolicies: [{ policyId: 'health', policyName: 'Health', enabled: true, priorityMode: 'multiplier', strategyMode: 'health_probe' }] }))
    expect(wrapper.text()).toContain(managed ? '仅倍率：调度站管理' : '仅倍率：未管理')
    expect(wrapper.text()).toContain('Priority 由仅倍率策略管理')
    expect(wrapper.text()).not.toContain('人工 Priority')
    expect(wrapper.text()).not.toContain('交给调度站后按主力排序')
    expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
  })

  it('distinguishes an idle old 1–9 baseline from manual ownership and never displays 0 as a machine value', () => {
    const old = detail(account({ priorityManaged: true, priorityExpected: 1, priorityConflict: true, prioritySyncBlocked: true, prioritySyncBlockReason: 'manual_priority' }))
    expect(old.text()).toContain('旧 Priority 管理记录')
    expect(old.text()).not.toContain('人工 Priority')
    expect(old.text()).not.toContain('交给调度站后按主力排序')
    expect(old.find('button[aria-label="编辑Priority"]').exists()).toBe(true)
    const first = detail(account({ priority: 50, priorityManaged: true, priorityExpected: 0 }))
    expect(first.text()).toContain('尚未确认写入')
    expect(first.text()).not.toContain('上次确认写入 0')
    const omitted = detail(account({ priority: 50, priorityManaged: true, priorityExpected: undefined }))
    expect(omitted.text()).toContain('尚未确认写入')
  })

  it('cancels with zero writes, then saves auto and manual values across every group', async () => {
    const wrapper = await liveDetails()
    await edit(wrapper, 'Priority')
    await wrapper.findAll('button[aria-label="取消编辑Priority"]')[0]!.trigger('click')
    expect(writes).toEqual([])
    await edit(wrapper, 'Priority')
    await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
    await save(wrapper, 'Priority')
    expect(writes[0]?.body).toEqual({ mode: 'auto' })
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 10)).toBe(true)
    await edit(wrapper, 'Priority')
    await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('manual')
    await wrapper.findAll('select[aria-label="人工Priority"]')[0]!.setValue('1')
    await save(wrapper, 'Priority')
    expect(writes[1]?.body).toEqual({ mode: 'manual', priority: 1 })
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 1)).toBe(true)
    expect(wrapper.text()).toContain('人工 Priority，调度站不管理')
    expect(wrapper.text()).not.toContain('旧 Priority 管理记录')
  })

  it('retains original values and retryable drafts for known rejection and explains automatic eligibility', async () => {
    const wrapper = await liveDetails()
    result = 'not_sent'; errorKey = 'admin.connectionHealth.errors.prioritySortDisabled'
    await edit(wrapper, 'Priority'); await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
    await save(wrapper, 'Priority')
    expect(wrapper.text()).toContain('没有开启倍率排序的有效策略')
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 5)).toBe(true)
    expect(wrapper.find('select[aria-label="Priority管理方式"]').exists()).toBe(true)
    result = 'success'; await save(wrapper, 'Priority')
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 10)).toBe(true)
  })

  it('keeps Priority pending across failed and successful refreshes, rejects old responses and sends no duplicate write', async () => {
    const wrapper = await liveDetails(); const stale = payload(); const read = deferred<Response>()
    readOverride = () => read.promise
    const oldRead = service.loadAdminGroups({ silent: true })
    result = 'pending'; errorKey = 'admin.connectionHealth.errors.remoteActionPending'
    await edit(wrapper, 'Priority'); await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
    await save(wrapper, 'Priority')
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
    readOverride = undefined; failRead = true; read.resolve(json(stale)); await oldRead; await flushPromises()
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
    await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
    failRead = false; await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.text()).toContain('待确认，请勿重复修改')
    expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
    expect(writes).toHaveLength(1)
    stored = account({ priority: 10, priorityManaged: true, priorityExpected: 10 })
    await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.text()).not.toContain('待确认，请勿重复修改')
    expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(true)
  })

  it('writes only concurrency input, validates its limits, and requires refresh before retrying an unknown outcome', async () => {
    const wrapper = await liveDetails()
    await edit(wrapper, '并发')
    const input = wrapper.findAll('input[aria-label="账号并发"]')[0]!
    await input.setValue('0'); await save(wrapper, '并发'); expect(writes).toEqual([])
    await input.setValue('20'); await save(wrapper, '并发')
    expect(writes[0]?.body).toEqual({ concurrency: 20 })
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.concurrency === 20 && entry.accounts[0]?.loadFactor === 20)).toBe(true)
    await edit(wrapper, '并发'); await wrapper.findAll('input[aria-label="账号并发"]')[0]!.setValue('25')
    result = 'pending'; errorKey = 'admin.connectionHealth.errors.concurrencyUnconfirmed'; failRead = true
    await save(wrapper, '并发')
    expect(wrapper.text()).toContain('结果未确认，请刷新查看')
    expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(false)
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.concurrency === 20)).toBe(true)
    failRead = false; await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(true)
    expect(writes).toHaveLength(2)
  })

  it('does not apply a response after the editor leaves the workspace', async () => {
    const wrapper = await liveDetails(); const delayed = deferred<Response>()
    saveOverride = () => delayed.promise
    await edit(wrapper, 'Priority'); await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
    await wrapper.findAll('button[aria-label="保存Priority"]')[0]!.trigger('click'); await flushPromises()
    wrapper.unmount(); wrappers.splice(wrappers.indexOf(wrapper), 1)
    service.setAdminGroupsWorkspace('other'); service.adminGroups.value = [group(account({ targetId: 'sub2api:other:other-account', priority: 6 }))]
    delayed.resolve(json({ targetId, result: 'success', priority: 10 })); await flushPromises()
    expect(service.adminGroups.value[0]?.accounts[0]?.priority).toBe(6)
    expect(service.adminGroups.value[0]?.accounts[0]?.targetId).toBe('sub2api:other:other-account')
  })

  it('cancels concurrency without writing and retains its original value and draft on confirmed rejection', async () => {
    const wrapper = await liveDetails()
    await edit(wrapper, '并发'); await wrapper.findAll('input[aria-label="账号并发"]')[0]!.setValue('20')
    await wrapper.findAll('button[aria-label="取消编辑并发"]')[0]!.trigger('click')
    expect(writes).toEqual([])
    await edit(wrapper, '并发'); await wrapper.findAll('input[aria-label="账号并发"]')[0]!.setValue('20')
    result = 'not_sent'; errorKey = 'admin.connectionHealth.errors.accountWriteNotSent'
    await save(wrapper, '并发')
    expect(wrapper.text()).toContain('本次修改确认未发送，原值保持')
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.concurrency === 1000)).toBe(true)
    expect(wrapper.find('input[aria-label="账号并发"]').exists()).toBe(true)
    result = 'success'; await save(wrapper, '并发')
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.concurrency === 20)).toBe(true)
  })

  it.each(['admin.connectionHealth.errors.prioritySortDisabled', 'admin.connectionHealth.errors.priorityAutoDegradeDisabled', 'admin.connectionHealth.errors.priorityNoMatchingModels', 'admin.connectionHealth.errors.remoteActionPending'])('keeps the original Priority and retryable draft on a pre-send API rejection: %s', reason => {
    return (async () => {
      const wrapper = await liveDetails(); status = reason.endsWith('remoteActionPending') ? 409 : 400; errorKey = reason
      await edit(wrapper, 'Priority'); await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
      await save(wrapper, 'Priority')
      expect(wrapper.text()).toContain(t(reason))
      expect(wrapper.find('select[aria-label="Priority管理方式"]').exists()).toBe(true)
      expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 5)).toBe(true)
      expect(wrapper.text()).not.toContain('待确认，请勿重复修改')
    })()
  })

  it.each(['network', '5xx', 'malformed', 'wrong-target'])('treats an unprovable Priority result as pending with zero duplicate writes: %s', failure => {
    return (async () => {
      const wrapper = await liveDetails(); failRead = true
      saveOverride = () => failure === 'network' ? Promise.reject(new Error('transport disconnected'))
        : Promise.resolve(failure === '5xx' ? json({ message: 'error' }, 500)
          : failure === 'malformed' ? new Response('invalid JSON') : json({ targetId: 'sub2api:other:forged', result: 'success', priority: 10 }))
      await edit(wrapper, 'Priority'); await wrapper.findAll('select[aria-label="Priority管理方式"]')[0]!.setValue('auto')
      await save(wrapper, 'Priority')
      expect(wrapper.text()).toContain('待确认，请勿重复修改')
      expect(wrapper.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
      expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 5)).toBe(true)
      expect(writes).toHaveLength(1)
    })()
  })

  it('does not claim concurrency success when its load factor fails readback and keeps pending if refresh still omits that field', async () => {
    const wrapper = await liveDetails(); failRead = true
    saveOverride = () => Promise.resolve(json({ targetId, result: 'success', concurrency: 20, loadFactor: 30 }))
    await edit(wrapper, '并发'); await wrapper.findAll('input[aria-label="账号并发"]')[0]!.setValue('20'); await save(wrapper, '并发')
    expect(wrapper.text()).toContain('结果未确认，请刷新查看')
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.concurrency === 1000)).toBe(true)
    failRead = false; stored = account({ concurrency: 20, loadFactor: undefined })
    await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(false)
    stored = account({ concurrency: 20, loadFactor: 30 }); await service.loadAdminGroups(); await flushPromises()
    expect(wrapper.find('button[aria-label="编辑并发"]').exists()).toBe(true)
    expect(writes).toHaveLength(1)
  })

  it('serializes duplicate Priority editors and disables cancellation while a save is in flight', async () => {
    const wrapper = await liveDetails(); const delayed = deferred<Response>(); saveOverride = () => delayed.promise
    const editors = wrapper.findAll('[data-testid="account-priority-editor"]')
    for (const editor of editors) await editor.get('button[aria-label="编辑Priority"]').trigger('click')
    await editors[0]!.get('select[aria-label="Priority管理方式"]').setValue('auto')
    await editors[0]!.get('button[aria-label="保存Priority"]').trigger('click'); await flushPromises()
    expect(editors[1]!.get('button[aria-label="保存Priority"]').attributes('disabled')).toBeDefined()
    expect(editors[0]!.get('button[aria-label="取消编辑Priority"]').attributes('disabled')).toBeDefined()
    await editors[1]!.get('button[aria-label="保存Priority"]').trigger('click'); expect(writes).toHaveLength(1)
    stored = account({ priority: 10, priorityManaged: true, priorityExpected: 10 }); delayed.resolve(json({ targetId, result: 'success', priority: 10 })); await flushPromises()
    expect(service.adminGroups.value.every(entry => entry.accounts[0]?.priority === 10)).toBe(true)
  })

  it('retains the read-only group accounts dialog and does not add Sub2API editors to NewAPI', async () => {
    const external = detail(account({ targetId: 'newapi:task-b-workspace:channel' }))
    expect(external.find('button[aria-label="编辑Priority"]').exists()).toBe(false)
    expect(external.find('button[aria-label="编辑并发"]').exists()).toBe(false)
    expect(external.text()).not.toContain('管理方式暂无法确认')
    const dialog = mount(AdminGroupAccountsDialog, { props: { open: true, group: group(account()) }, global: { stubs: { Teleport: true } } })
    wrappers.push(dialog); await flushPromises()
    expect(dialog.text()).toContain('并发')
    expect(dialog.text()).toContain('1000')
    expect(dialog.find('button[aria-label="编辑并发"]').exists()).toBe(false)
  })

  it('marks equal current priorities as tied without claiming display order is strict main-site routing', async () => {
    const first = account({ priority: 10, priorityManaged: true, priorityExpected: 10 })
    const second = account({ id: 'other', targetId: 'sub2api:task-b-workspace:other', priority: 10, priorityManaged: true, priorityExpected: 10 })
    const wrapper = detail(first)
    await wrapper.setProps({ group: { ...group(first), accountCount: 2, accounts: [first, second] } })
    expect(wrapper.findAll('[data-testid="account-priority-editor"]').every(editor => editor.text().includes('当前 Priority 并列'))).toBe(true)
    expect(wrapper.text()).toContain('显示先后不代表主站严格选号顺序')
  })

  it('renders manual and automatic audit details in Chinese in both focused and global event dialogs without changing health', async () => {
    const events = [
      { result: 'account_edit_success', remoteAction: 'sub2api_priority_manual_set', errorDetail: 'manual:success' },
      { result: 'account_edit_pending', remoteAction: 'sub2api_priority_manual_set_failed', errorDetail: 'auto:pending' },
      { result: 'account_edit_not_sent', remoteAction: 'sub2api_concurrency_set_failed', errorDetail: 'not_sent' },
    ].map((event, index) => ({ ...event, id: `audit-${index}`, connectionId: targetId, modelName: '*', source: 'manual', actionSource: 'user_action',
      createdAt: '2026-10-07T00:00:00Z', fromState: '', toState: '', latencyMs: null, errorKey: '', upstreamSiteId: '', upstreamGroupName: 'one' } as ConnectionHealthEvent))
    const wrapper = mount(ConnectionHealthEventsDialog, { props: { open: true, events, groups: [], adminGroups: [group(account())], selectedConnectionId: targetId, siteName: () => 'Site' }, global: { stubs: { Teleport: true } } })
    wrappers.push(wrapper); await flushPromises()
    const details = ['人工 · 账号修改已确认成功', '自动（交给调度站） · 账号修改结果待确认', '账号并发 · 账号修改确认未写出，原值保持']
    for (const [index, event] of events.entries()) {
      for (const selected of [targetId, '']) {
        // 全局模式沿用每目标/模型只展示最新事件的既有规则。
        await wrapper.setProps({ selectedConnectionId: selected, events: [event] }); await flushPromises()
        expect(wrapper.text()).toContain(t(remoteActionLabelKey(event.remoteAction!)!.key))
        expect(wrapper.text()).toContain(details[index])
        expect(wrapper.text()).not.toContain('manual:success')
        expect(wrapper.text()).not.toContain('auto:pending')
        expect(wrapper.text()).not.toContain('当前健康中断')
        expect(wrapper.text()).not.toContain('可用率100%')
      }
    }
  })

  it.each(['sub2api_priority_manual_set', 'sub2api_priority_manual_set_failed', 'sub2api_concurrency_set', 'sub2api_concurrency_set_failed'])('shows Chinese audit actions without adding probe failures or availability: %s', action => {
    const mapped = remoteActionLabelKey(action)!
    expect(t(mapped.key, mapped.params)).not.toBe(action)
    const event = { modelName: '*', result: 'account_edit_pending', remoteAction: action, createdAt: '2026-10-07T00:00:00Z' }
    expect(buildConnectionHealthRecordSummary([{ result: 'ok' }, event]).availabilityPct).toBe(100)
    expect(latestConnectionHealthProbeFailure([event])).toBeNull()
    expect(buildConnectionHealthRecordSummary([{ result: 'ok' }, { ...event, result: 'server_error' }]).availabilityPct).toBe(100)
    expect(latestConnectionHealthProbeFailure([{ ...event, modelName: 'gpt-5.6-sol', result: 'server_error' }])).toBeNull()
  })
})

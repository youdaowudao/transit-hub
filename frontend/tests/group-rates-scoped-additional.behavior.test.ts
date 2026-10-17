// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

const h = vi.hoisted(() => ({ route: null as any, calls: [] as { path: string; body: any }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false, currentAccount: null as any }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (value: any) => { h.route.query = value.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); h.currentAccount = ref({ id: 'ws-scoped', platform: 'sub2api' }); return { useAdminAccounts: () => ({ currentAccount: h.currentAccount }) } })
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const deferred = <T>() => { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const rate = (id: string, name = id, site = 'site-a') => ({ siteId: site, siteName: site === 'site-a' ? '范围甲' : '范围乙', groupId: id, groupName: name, platform: 'sub2api', type: 'openai', mapped: true, pricingMapped: true, deleted: false, currentMultiplier: 1, delta: null, deltaPercent: null, updatedAt: '2026-10-12T00:00:00Z' })
const conn = (id: string, group = '1', name = group, status = 'active') => ({ id, upstreamSiteId: 'site-a', upstreamGroupId: group, upstreamGroupName: name, upstreamKeyId: 'key-' + id, adminAccountId: id, adminAccountName: id, ownGroupIds: ['10'], groupType: 'openai', status, adminPlatform: 'sub2api', pricingMappingEnabled: true, canDeleteRemote: status !== 'missing', createdAt: '2026-10-12T00:00:00Z' })
const result = (confirmed = h.connections) => ({ workspaceAdminAccountId: 'ws-scoped', total: h.connections.length, confirmedConnectionIds: confirmed.map(c => c.id), unconfirmedConnectionIds: h.connections.filter(c => !confirmed.includes(c)).map(c => c.id), skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: Object.fromEntries(confirmed.map(c => [c.id, c.status || 'active'])), issues: [], storageUncertain: false })
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w }
const row = (w: VueWrapper, name = '分组甲') => w.findAll('tbody tr').find(r => r.text().includes(name))!
const button = (w: VueWrapper, label: string) => w.findAll('button').find(b => b.text().trim() === label)!
const checks = () => h.calls.filter(c => c.path.includes('/check'))
beforeEach(() => {
  h.currentAccount.value = { id: 'ws-scoped', platform: 'sub2api' }
  h.route = reactive({ query: {} }); h.calls = []; h.rates = [rate('1', '分组甲')]; h.connections = [conn('101', '1', '分组甲')]; h.optionsFail = false
  h.check = async () => json(result()); h.local = async () => json({ workspaceAdminAccountId: 'ws-scoped', items: h.connections })
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    if (String(url).includes('/my-sites/own-group-rates')) return json({ workspaceAdminAccountId: new URL(String(url), 'http://local').searchParams.get('workspaceAdminAccountId'), groups: [] })
    const path = String(url); const body = init.body ? JSON.parse(String(init.body)) : null; h.calls.push({ path, body })
    if (path.includes('/real-connections/check')) return h.check(body)
    if (path.includes('/real-connections/list-scoped')) return h.local(body)
    if (path.endsWith('/real-connections')) return json(h.connections)
    if (path.includes('/upstream-sites/options')) return h.optionsFail ? json({ message: '网站名称缓存无法读取，请刷新重试' }, 500) : json({ workspaceAdminAccountId: 'ws-scoped', items: [{ id: 'site-a', name: '范围甲' }, { id: 'site-b', name: '范围乙' }] })
    if (path.includes('/upstream-sites')) return json([{ id: 'site-a', name: '范围甲' }, { id: 'site-b', name: '范围乙' }])
    if (path.includes('/group-rates?')) { const q = new URL(path, 'http://local').searchParams; return json({ workspaceAdminAccountId: 'ws-scoped', items: h.rates, total: 65, page: q.has('focusGroupId') ? 3 : Number(q.get('page') || 1), pageSize: 30, totalPages: 3, focusFound: q.has('focusGroupId') ? true : undefined, types: ['openai'], platforms: ['sub2api'], statusCounts: { all: 65, mapped: 1, unmapped: 64, deleted: 0 } }) }
    if (path.includes('/group-rates/type')) return json({ success: true })
    if (path.includes('/group-rates/history')) return json([])
    if (path.includes('mapping-options')) return json({ ownGroups: [], mappings: [] })
    if (path.includes('/dashboard')) return json({ platform: 'sub2api' })
    return json({})
  }))
})
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.useRealTimers(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('scoped group rates additional interaction evidence', () => {
  it('does not send local, option or check requests while no workspace is selected', async () => {
    h.currentAccount.value = null
    const w = await open()
    expect(w.text()).toContain('尚未选定当前工作区')
    expect(w.find('tbody tr').exists()).toBe(false)
    expect(h.calls).toHaveLength(0)
    await button(w, '刷新数据').trigger('click'); await flushPromises()
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    h.route.query = { siteId: 'site-b' }; await flushPromises()
    expect(h.calls).toHaveLength(0)
    expect(checks()).toHaveLength(0)
  })
  it('clears the displayed workspace data after its workspace is lost', async () => {
    const w = await open()
    expect(row(w)).toBeDefined()
    const count = h.calls.length
    h.currentAccount.value = null
    await flushPromises()
    expect(w.find('tbody tr').exists()).toBe(false)
    expect(w.text()).toContain('尚未选定当前工作区')
    expect(w.findAll('option').some(option => option.text() === '范围甲')).toBe(false)
    expect(h.calls).toHaveLength(count)
  })

  it.each([false, true])('waits for the asynchronous initial workspace before reading the page (focus=%s)', async focus => {
    h.currentAccount.value = null
    if (focus) { h.route.query = { siteId: 'site-a', focusGroupId: '61', focusGroup: '目标分组' }; h.rates = [rate('61', '目标分组')] }
    const w = await open()
    await Promise.resolve()
    h.currentAccount.value = { id: 'ws-scoped', platform: 'sub2api' }
    await flushPromises()
    const lists = h.calls.filter(c => c.path.includes('/group-rates?'))
    expect(lists).toHaveLength(2)
    for (const list of lists) expect(new URL(list.path, 'http://local').searchParams.get('workspaceAdminAccountId')).toBe('ws-scoped')
    expect(checks()).toHaveLength(1)
    expect(row(w, focus ? '目标分组' : '分组甲')).toBeDefined()
  })

  it('counts website confirmations outside the current page without granting row confirmation', async () => {
    h.route.query = { siteId: 'site-a', focusGroupId: '61', focusGroup: '第3页分组' }
    h.rates = [rate('61', '第3页分组')]
    h.connections = []
    h.check = async () => json({ workspaceAdminAccountId: 'ws-scoped', total: 3, confirmedConnectionIds: ['101', '102', '103'], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: { '101': 'active', '102': 'active', '103': 'active' }, issues: [], storageUncertain: false })
    const w = await open()
    expect(w.text()).toContain('本次确认3条账号对接记录，0条仍显示上次状态')
    expect(w.text()).not.toContain('关联资料在检查期间发生变化')
    expect(row(w, '第3页分组').text()).toContain('配置对接')
    h.connections = [conn('101', '61', '第3页分组')]
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    expect(row(w, '第3页分组').findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(checks()).toHaveLength(1)
  })

  it('manual refresh preserves the clicked page scope after a changed local reread', async () => {
    const w = await open()
    h.rates = [rate('2', '分组乙')]
    h.connections = [conn('202', '2', '分组乙')]
    await button(w, '刷新数据').trigger('click'); await flushPromises()
    const manual = checks().at(-1)!.body
    expect(manual.mode).toBe('manual')
    expect(manual.groupRefs).toEqual([{ siteId: 'site-a', groupId: '1', groupName: '分组甲' }])
    expect(checks()).toHaveLength(2)
  })
  it('a confirmed active duplicate keeps priority over an unconfirmed missing duplicate', async () => {
    h.connections.push(conn('102', '1', '分组甲', 'missing'))
    h.check = async () => json(result([h.connections[0]]))
    const w = await open()
    const disconnect = row(w).findAll('button').find(b => b.text().includes('取消对接'))!
    expect(disconnect.element).toHaveProperty('disabled', false)
    expect(row(w).text()).not.toContain('仅清理本地记录')
    await disconnect.trigger('click'); await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(true)
  })
  it('a status changed before the reread cannot inherit the old confirmation', async () => {
    h.check = async () => { const saved = result(); h.connections = [{ ...h.connections[0], status: 'missing' }]; return json(saved) }
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('清理'))!.element).toHaveProperty('disabled', true)
    expect(w.text()).toContain('关联资料在检查期间发生变化')
  })
  it.each([500, 503])('HTTP %s keeps buttons blocked and performs one local reread', async status => {
    h.check = async () => json({ ...result([]), storageUncertain: status === 503, message: status === 503 ? '保存结果尚未确认' : '保存失败并已回滚', detail: '本次未修改主站', next: '请刷新核对' }, status)
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(h.calls.filter(c => c.path.includes('/list-scoped'))).toHaveLength(2)
    expect(checks()).toHaveLength(1)
    expect(w.text()).toContain(status === 503 ? '保存结果尚未确认' : '保存失败并已回滚')
  })
  it('an old site result does not overwrite a new site after browser navigation', async () => {
    const pending = deferred<Response>()
    const old = result()
    h.check = () => pending.promise
    const w = await open()
    h.rates = [rate('2', '分组乙', 'site-b')]
    h.connections = [{ ...conn('202', '2', '分组乙'), upstreamSiteId: 'site-b' }]
    h.check = async () => json(result())
    h.route.query = { siteId: 'site-b' }; await flushPromises()
    pending.resolve(json({ ...old, issues: [{ step: 'verify', detail: '旧网站错误不可显示', next: '旧提示', retryAllowed: true }] })); await flushPromises()
    expect(row(w, '分组乙')).toBeDefined()
    expect(w.text()).not.toContain('旧网站错误不可显示')
    expect(w.text()).not.toContain('分组甲')
    expect(checks()).toHaveLength(2)
  })
  it('classification editing stays available during checking and only saves local data', async () => {
    const pending = deferred<Response>(); h.check = () => pending.promise
    const w = await open()
    await row(w).get('button[aria-label*="修改分组甲的分类"]').trigger('click'); await flushPromises()
    const edit = w.get('[role="dialog"]')
    expect(edit.text()).toContain('修改分组类型')
    expect(button(w, '刷新数据').element).toHaveProperty('disabled', true)
    await edit.get('select').setValue('openai')
    const save = edit.findAll('button').find(b => b.text().trim() === '保存类型')!
    await save.trigger('click'); await flushPromises()
    expect(h.calls.filter(c => c.path.includes('/group-rates/type'))).toHaveLength(1)
    expect(checks()).toHaveLength(1)
    pending.resolve(json(result())); await flushPromises()
    expect(checks()).toHaveLength(1)
  })
})

describe('Review1 scoped regression from actual page entrances', () => {
  it.each(['account', 'platform', 'status'])('revokes prior confirmation after a later local reread changes %s', async field => {
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', false)
    h.connections = [{ ...h.connections[0], ...(field === 'account' ? { adminAccountId: '909' } : field === 'platform' ? { adminPlatform: 'newapi' } : { status: 'missing' }) }]
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    const operation = row(w).findAll('button').find(b => /取消对接|清理/.test(b.text()))!
    expect(operation.element).toHaveProperty('disabled', true)
    expect(w.text()).toContain('关联资料')
    expect(checks()).toHaveLength(1)
  })

  it('revokes skipped permission when an empty account becomes a valid bound account on local reread', async () => {
    h.connections = [{ ...conn('101', '1', '分组甲'), adminAccountId: '' }]
    h.check = async () => json({ ...result([]), unconfirmedConnectionIds: [], skippedConnectionIds: ['101'] })
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', false)
    h.connections = [{ ...h.connections[0], adminAccountId: '909' }]
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(checks()).toHaveLength(1)
  })

  it('does not reopen unconfirmed old website cleanup when the next website list fails', async () => {
    h.connections = [conn('101', '1', '分组甲', 'missing')]
    h.check = async () => json(result([]))
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('清理'))!.element).toHaveProperty('disabled', true)
    const original = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => String(input).includes('/group-rates?') ? json({ message: '范围乙倍率暂时无法读取', detail: '数据库读取中断，未取得新范围资料', next: '请查看Hub后端倍率读取记录' }, 500) : original(input, init)))
    h.route.query = { siteId: 'site-b' }; await flushPromises()
    for (const operation of w.findAll('tbody button').filter(b => /取消对接|清理|配置对接/.test(b.text()))) expect(operation.element).toHaveProperty('disabled', true)
    expect(w.text()).toContain('范围乙倍率暂时无法读取')
    expect(checks()).toHaveLength(1)
  })

  it('freezes missing cleanup object and options when a previously queued search timer expires', async () => {
    vi.useFakeTimers()
    h.connections = [conn('101', '1', '分组甲', 'missing')]
    const w = await open()
    await w.get('input[type="text"]').setValue('不存在的分组')
    await row(w).findAll('button').find(b => b.text().includes('清理'))!.trigger('click'); await flushPromises()
    const dialog = w.get('[role="dialog"]')
    expect(dialog.text()).toContain('仅清理本地记录')
    await dialog.get('input[type="checkbox"]').setValue(false)
    h.rates = []; h.connections = []
    await vi.advanceTimersByTimeAsync(300); await flushPromises()
    expect(dialog.text()).toContain('仅清理本地记录')
    expect(dialog.text()).not.toContain('删除账号和 Key')
    expect(dialog.find('input[value="full"]').exists()).toBe(false)
    expect((dialog.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
    await dialog.findAll('button').find(b => b.text().trim() === '确定')!.trigger('click'); await flushPromises()
    expect(h.calls.filter(c => c.path.includes('/real-disconnect')).map(c => c.body)).toEqual([{ connectionId: '101', mode: 'unlink', removePricingMapping: false }])
    expect(checks()).toHaveLength(1)
  })

  it('highlights only the exact ID when two same-name groups share the target page', async () => {
    h.route.query = { siteId: 'site-a', focusGroupId: '61', focusGroup: '同名分组' }
    h.rates = [rate('61', '同名分组'), rate('62', '同名分组')]
    h.connections = []
    const w = await open()
    const highlighted = w.findAll('tbody tr').filter(r => r.classes().includes('outline-primary/50'))
    expect(highlighted).toHaveLength(1)
    expect(highlighted[0].element).toBe(w.findAll('tbody tr')[0].element)
    expect(checks()).toHaveLength(1)
  })

  it.each(['rates', 'options'])('shows safe local %s error cause and the exact next step', async target => {
    const original = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (target === 'rates' ? path.includes('/group-rates?') : path.includes('/upstream-sites/options')) return json({ message: '本地资料暂时无法读取', detail: '读取步骤连接中断，本次没有修改主站 token=fixture-secret', next: '请查看Hub后端本地读取记录后刷新' }, 500)
      return original(input, init)
    }))
    const w = await open()
    expect(w.text()).toContain('读取步骤连接中断，本次没有修改主站')
    expect(w.text()).toContain('请查看Hub后端本地读取记录后刷新')
    expect(w.text()).not.toContain('fixture-secret')
  })
})

describe('Review2 completed import and paused search', () => {
  it.each(['successful old mapping read', 'late failed old mapping read'])('shows the successfully resumed search without a false refresh failure or another import/check: %s', async mapping => {
    vi.useFakeTimers()
    h.connections = []
    const lateMapping = deferred<Response>()
    const original = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/my-sites/mapping-options')) {
        if (mapping === 'late failed old mapping read' && h.calls.some(c => c.path.endsWith('/my-sites/real-connect'))) return lateMapping.promise
        return json({ ownGroups: [{ id: '10', groupName: '主代理分组', platform: 'openai', multiplier: 1 }], mappings: [] })
      }
      if (path.endsWith('/my-sites/real-connect')) {
        const req = JSON.parse(String(init?.body)); h.calls.push({ path, body: req })
        h.rates = [rate('1', '搜索后分组')]
        return json({ workspaceAdminAccountId: 'ws-scoped', configurationStatus: 'confirmed', connection: { id: 'c'.repeat(32), upstreamSiteId: req.upstreamSiteId, upstreamGroupId: req.upstreamGroupId, upstreamGroupName: req.upstreamGroupName, adminAccountId: '101', adminAccountName: '已创建账号', upstreamKeyId: '207', ownGroupIds: req.ownGroupIds, groupType: req.groupType, adminPlatform: 'sub2api', status: 'active', createdAt: '2026-10-13T00:00:00Z', ownGroupNames: ['主代理分组'], pricingMappingEnabled: true, canDeleteRemote: true, provisioningMode: 'managed' }, configuration: { observation: 'creation', adminAccountId: '101', name: '已创建账号', platform: req.groupType, priority: 100, concurrency: 50, passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true, ownGroups: [{ id: '10', name: '主代理分组' }], modelState: 'synced', models: ['verified-model'] } })
      }
      return original(input, init)
    }))
    const w = await open()
    await w.get('input[name="groupRateSearch"]').setValue('搜索后分组')
    await row(w).findAll('button').find(b => b.text().includes('配置对接'))!.trigger('click'); await flushPromises()
    const dialog = w.get('[role="dialog"]')
    await dialog.findAll('label').find(label => label.text().includes('主代理分组'))!.get('input[type="checkbox"]').setValue(true)
    await dialog.get('form').trigger('submit'); await flushPromises()
    await vi.advanceTimersByTimeAsync(300); await flushPromises()
    if (mapping === 'late failed old mapping read') { lateMapping.resolve(json({ message: '旧读取在新列表完成后失败' }, 500)); await flushPromises() }
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(row(w, '搜索后分组')).toBeDefined()
    expect(h.calls.filter(c => c.path.includes('/my-sites/real-connect') && !c.path.includes('/real-connections'))).toHaveLength(1)
    expect(checks()).toHaveLength(1)
    expect(w.text()).not.toContain('列表刷新失败')
    expect(w.text()).not.toContain('真实对接创建失败')
    const lastList = h.calls.filter(c => c.path.includes('/group-rates?')).at(-1)!
    expect(new URL(lastList.path, 'http://local').searchParams.get('search')).toBe('搜索后分组')
  })
})


describe('retained skipped operation identity after Review1 fix', () => {
  it.each(['empty', 'other'])('keeps an unchanged %s-platform skipped record usable after ordinary rereads', async variant => {
    h.connections = [{ ...conn('101', '1', '分组甲'), ...(variant === 'empty' ? { adminAccountId: '' } : { adminPlatform: 'newapi' }) }]
    h.check = async () => json({ ...result([]), unconfirmedConnectionIds: [], skippedConnectionIds: ['101'] })
    const w = await open()
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', false)
    expect(checks()).toHaveLength(1)
  })
  it('revokes a skipped other-platform record after it becomes a Sub2API account', async () => {
    h.connections = [{ ...conn('101', '1', '分组甲'), adminPlatform: 'newapi' }]
    h.check = async () => json({ ...result([]), unconfirmedConnectionIds: [], skippedConnectionIds: ['101'] })
    const w = await open()
    h.connections = [{ ...h.connections[0], adminPlatform: 'sub2api' }]
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(checks()).toHaveLength(1)
  })
})

const installCompletedImportFixture = (afterCreate: () => void, readFailure = false) => {
  const original = fetch
  let completed = false
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path.includes('/my-sites/mapping-options')) return json({ ownGroups: [{ id: '10', groupName: '保留分组', platform: 'openai', multiplier: 1 }], mappings: [] })
    if (completed && readFailure && path.includes('/group-rates?')) return json({ message: '倍率本地读取失败', detail: '本次没有取得新列表', next: '请刷新重试' }, 500)
    if (path.endsWith('/my-sites/real-connect')) {
      const req = JSON.parse(String(init?.body)); h.calls.push({ path, body: req })
      completed = true
      afterCreate()
      return json({ workspaceAdminAccountId: 'ws-scoped', configurationStatus: 'confirmed', connection: {
        id: 'd'.repeat(32), upstreamSiteId: req.upstreamSiteId, upstreamGroupId: req.upstreamGroupId, upstreamGroupName: req.upstreamGroupName,
        adminAccountId: '101', adminAccountName: '已创建账号', upstreamKeyId: '207', ownGroupIds: req.ownGroupIds,
        groupType: req.groupType, adminPlatform: 'sub2api', status: 'active', createdAt: '2026-10-13T00:00:00Z', ownGroupNames: ['保留分组'], pricingMappingEnabled: true, canDeleteRemote: true, provisioningMode: 'managed',
      }, configuration: { observation: 'creation', adminAccountId: '101', name: '已创建账号', platform: req.groupType, priority: 100, concurrency: 50,
        passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true, ownGroups: [{ id: '10', name: '保留分组' }], modelState: 'synced', models: ['verified-model'] } })
    }
    return original(input, init)
  }))
}
const openCompletedImport = async (w: VueWrapper) => {
  await row(w).findAll('button').find(b => b.text().includes('配置对接'))!.trigger('click'); await flushPromises()
  const dialog = w.get('[role="dialog"]')
  await dialog.findAll('label').find(label => label.text().includes('保留分组'))!.get('input[type="checkbox"]').setValue(true)
  return dialog
}
describe('Review2 retained new-range and real-failure boundaries', () => {
  it('enters the deferred new website and performs its automatic check after completed import closes', async () => {
    h.connections = []
    installCompletedImportFixture(() => { h.rates = [rate('2', '范围乙分组', 'site-b')] })
    const w = await open()
    const dialog = await openCompletedImport(w)
    h.route.query = { siteId: 'site-b' }; await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(true)
    await dialog.get('form').trigger('submit'); await flushPromises()
    expect(row(w, '范围乙分组')).toBeDefined()
    expect(checks()).toHaveLength(2)
    expect(checks().at(-1)!.body).toMatchObject({ siteId: 'site-b', mode: 'automatic' })
    expect(w.text()).not.toContain('列表刷新失败')
    expect(h.calls.filter(c => c.path.endsWith('/my-sites/real-connect'))).toHaveLength(1)
  })
  it('preserves the completed-import refresh warning for a real failure in the same page generation', async () => {
    h.connections = []
    installCompletedImportFixture(() => undefined, true)
    const w = await open()
    const dialog = await openCompletedImport(w)
    await dialog.get('form').trigger('submit'); await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(w.text()).toContain('导入结果已返回，但列表刷新失败')
    expect(w.text()).toContain('不要因此再次导入')
    expect(w.text()).not.toContain('真实对接创建失败')
    expect(checks()).toHaveLength(1)
    expect(h.calls.filter(c => c.path.endsWith('/my-sites/real-connect'))).toHaveLength(1)
  })
})

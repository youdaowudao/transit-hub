// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

const h = vi.hoisted(() => ({ route: null as any, calls: [] as { path: string; body: any }[], connections: [] as any[], rates: [] as any[], check: null as any, local: null as any, optionsFail: false }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (value: any) => { h.route.query = value.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-scoped', platform: 'sub2api' }) }) } })
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
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('scoped group rates critical business contract', () => {
  it('does not reread locally when a remote result arrives after unmount', async () => {
    const pending = deferred<Response>(); h.check = () => pending.promise
    const w = await open()
    const localBefore = h.calls.filter(c => c.path.includes('/group-rates?') || c.path.includes('/list-scoped')).length
    w.unmount()
    pending.resolve(json(result())); await flushPromises()
    expect(h.calls.filter(c => c.path.includes('/group-rates?') || c.path.includes('/list-scoped'))).toHaveLength(localBefore)
    expect(checks()).toHaveLength(1)
  })
  it('shows local rates and connections while remote checking is still blocked', async () => {
    const pending = deferred<Response>(); h.check = () => pending.promise
    const w = await open()
    expect(row(w)?.text()).toContain('分组甲')
    expect(row(w)?.text()).toContain('已对接')
    expect(row(w)?.text()).not.toContain('配置对接')
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(h.calls.findIndex(c => c.path.includes('/group-rates?'))).toBeLessThan(h.calls.findIndex(c => c.path.includes('/check-scoped')))
    expect(checks()).toHaveLength(1)
    expect(checks()[0].body).toMatchObject({ workspaceAdminAccountId: 'ws-scoped', mode: 'automatic', groupRefs: [{ siteId: 'site-a', groupId: '1', groupName: '分组甲' }] })
    pending.resolve(json(result())); await flushPromises()
  })
  it('keeps failed local connection data unknown and hides import and disconnect', async () => {
    h.local = async () => json({ message: '对接资料读取被中断，请刷新' }, 500)
    const w = await open()
    expect(row(w)?.text()).toContain('分组甲')
    expect(row(w)?.text()).not.toContain('未对接')
    expect(row(w)?.text()).not.toContain('配置对接')
    expect(row(w)?.text()).not.toContain('取消对接')
    expect(w.text()).toContain('本页对接资料暂时无法读取')
    expect(checks()).toHaveLength(0)
  })
  it('does not match two blank group IDs with different names', async () => {
    h.rates = [rate('', '分组甲')]; h.connections = [conn('101', '', '分组乙')]
    const w = await open()
    expect(row(w).text()).not.toContain('取消对接')
    expect(row(w).text()).toContain('配置对接')
  })
  it('sends one site-filtered locating request directly to the target page', async () => {
    h.route.query = { siteId: 'site-a', focusGroupId: '61', focusGroup: '目标组' }; h.rates = [rate('61', '目标组')]; h.connections = []
    const w = await open()
    const lists = h.calls.filter(c => c.path.includes('/group-rates?'))
    expect(lists).toHaveLength(2) // 初次定位与检查完成后的唯一重读。
    const query = new URL(lists[0].path, 'http://local').searchParams
    expect(query.get('siteId')).toBe('site-a'); expect(query.get('focusGroupId')).toBe('61')
    expect(row(w, '目标组')).toBeDefined()
    expect(checks()).toHaveLength(1); expect(checks()[0].body).toMatchObject({ siteId: 'site-a', mode: 'automatic' })
    expect(checks()[0].body.groupRefs).toBeUndefined()
    expect(h.calls.filter(c => c.path.endsWith('/upstream-sites'))).toHaveLength(0)
  })
  it('keeps missing cleanup disabled after a lost result and rereads locally only once', async () => {
    h.connections[0].status = 'missing'; h.check = async () => { throw new TypeError('network disconnected') }
    const w = await open()
    expect(row(w).text()).toContain('主站账号已删除')
    const cleanup = row(w).findAll('button').find(b => b.text().includes('清理'))!
    expect(cleanup.element).toHaveProperty('disabled', true)
    expect(checks()).toHaveLength(1)
    expect(h.calls.filter(c => c.path.includes('/list-scoped'))).toHaveLength(2)
    expect(button(w, '刷新数据').element).toHaveProperty('disabled', false)
    expect(w.text()).toContain('刷新')
  })
  it('does not inherit confirmation after the same connection is rebound before rereading', async () => {
    h.check = async () => { const saved = result(); h.connections = [{ ...h.connections[0], adminAccountId: '202' }]; return json(saved) }
    const w = await open()
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    expect(w.text()).toContain('刷新')
    expect(checks()).toHaveLength(1)
  })
  it('ordinary sorting, status filtering and paging never check again', async () => {
    const w = await open(); const count = checks().length
    const sort = w.findAll('select').find(s => s.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    await button(w, '下一页').trigger('click'); await flushPromises()
    const status = w.findAll('button').find(b => b.text().includes('未对接') && !b.text().includes('配置'))
    if (status) { await status.trigger('click'); await flushPromises() }
    expect(checks()).toHaveLength(count)
    expect(h.calls.filter(c => c.path.includes('/list-scoped')).length).toBeGreaterThan(2)
  })
  it('website option failure leaves the list visible and manual refresh retries options', async () => {
    h.optionsFail = true; const w = await open()
    expect(row(w).text()).toContain('分组甲'); expect(w.text()).toContain('网站选项暂时无法加载')
    const before = h.calls.filter(c => c.path.includes('/upstream-sites/options')).length
    h.optionsFail = false; await button(w, '刷新数据').trigger('click'); await flushPromises()
    expect(h.calls.filter(c => c.path.includes('/upstream-sites/options'))).toHaveLength(before + 1)
    expect(checks().at(-1)!.body.mode).toBe('manual')
    expect(w.text()).not.toContain('网站选项暂时无法加载')
  })
})

describe('accurate per-account names in scoped notices', () => {
  it.each(['automatic', 'manual', 'site'])('shows each complete response name including off-page accounts during %s', async mode => {
    h.connections = [{ ...conn('213', '1', '分组甲'), adminAccountName: '旧保存名称' }]
    if (mode === 'site') h.route.query = { siteId: 'site-a' }
    const longName = '完整账号'.repeat(150) + 'ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890尾部'
    const failed = () => ({ ...result([]), total: 2, unconfirmedConnectionIds: ['213', 'outside'], issues: [
      { connectionId: '213', accountId: '213', accountName: longName, step: 'detail', detail: '主站暂时查不到这个账号，尚不能确认是否已删除，当前仍保留上次状态', next: '请点击刷新进一步核对；本次未创建或删除账号', retryAllowed: true },
      { connectionId: 'outside', accountId: '363', accountName: '页外完整名称363', step: 'detail', detail: '上游维护 213', next: '请重试', retryAllowed: true },
    ] })
    if (mode !== 'manual') h.check = async () => json(failed())
    const w = await open()
    if (mode === 'manual') { h.check = async () => json(failed()); await button(w, '刷新数据').trigger('click'); await flushPromises() }
    expect(w.text()).toContain(`账号「${longName}」`)
    expect(w.text()).toContain('账号「页外完整名称363」')
    expect(w.text()).toContain('上游维护 213')
    expect(w.text()).not.toContain('账号213')
    expect(w.text()).not.toContain('账号363')
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
    h.check = async () => json(result()); await button(w, '刷新数据').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain(`账号「${longName}」`)
    expect(w.text()).not.toContain('页外完整名称363')
  })
  it.each(['missing-field', 'explicit-empty', 'wrong-account', 'changed'])('uses only the pre-check matching connection for %s', async variant => {
    h.connections = [{ ...conn('213', '1', '分组甲'), adminAccountName: '冻结旧名称' }]
    h.check = async () => {
      const payload: any = { ...result([]), issues: [{ connectionId: '213', accountId: variant === 'wrong-account' ? '363' : '213', step: variant === 'changed' ? 'changed' : 'detail', detail: '账号213的关联资料在检查期间发生变化，本次结果没有覆盖它', next: '请刷新', retryAllowed: true, ...(variant === 'explicit-empty' ? { accountName: '' } : {}) }] }
      h.connections = [{ ...h.connections[0], adminAccountId: '363', adminAccountName: '重绑后新名称' }]
      return json(payload)
    }
    const w = await open()
    expect(w.text()).toContain(variant === 'explicit-empty' || variant === 'wrong-account' ? '账号（名称暂未读到）：' : '账号「冻结旧名称」：')
    expect(w.text()).not.toContain('账号「重绑后新名称」')
    expect(w.text()).not.toContain('账号213')
    expect(row(w).findAll('button').find(b => b.text().includes('取消对接'))!.element).toHaveProperty('disabled', true)
  })
  it.each([
    '主站暂时查不到账号213，尚不能确认是否已删除',
    '读取主站账号213时未能确认：上游账号363仍在维护，原数字 213 保留',
    '进一步核对账号213是否已删除时，完整账号列表未能读取完成：上游账号363仍在维护',
    '账号213的关联资料在检查期间发生变化，本次结果没有覆盖它',
  ])('strips only the known local legacy prefix: %s', async detail => {
    h.connections = [{ ...conn('213', '1', '分组甲'), adminAccountName: '213' }]
    h.check = async () => json({ ...result([]), issues: [{ connectionId:'213', accountId:'213', step:'detail', detail, next:'请刷新', retryAllowed:true }] })
    const w = await open()
    expect(w.text()).toContain('账号「213」：')
    expect(w.text()).not.toContain('账号213')
    if (detail.includes('上游账号363')) expect(w.text()).toContain('上游账号363仍在维护')
    if (detail.includes('原数字 213')) expect(w.text()).toContain('原数字 213 保留')
  })
})

it('renders account-name markup as ordinary text without creating an HTML element', async () => {
  const name = '<img src="name-test" onerror="window.nameTestExecuted=1">完整名称'
  h.check = async () => json({ ...result([]), issues: [{ connectionId: '101', accountId: '101', accountName: name, step: 'detail', detail: '主站读取未完成', next: '请刷新', retryAllowed: true }] })
  const w = await open()
  expect(w.text()).toContain(`账号「${name}」：`)
  expect(w.find('img[src="name-test"]').exists()).toBe(false)
  expect((window as any).nameTestExecuted).toBeUndefined()
})

it('matches an omitted old-response account ID only to the same connection with an empty saved ID', async () => {
  h.connections = [{ ...conn('empty-account', '1', '分组甲'), adminAccountId: '', adminAccountName: '没有编号但有保存名称' }]
  h.check = async () => json({ ...result([]), unconfirmedConnectionIds: [], skippedConnectionIds: ['empty-account'], issues: [{ connectionId: 'empty-account', step: 'scope', detail: '这条记录没有主站账号ID，本次保持原状态', next: '请沿原对接入口处理', retryAllowed: false }] })
  const w = await open()
  expect(w.text()).toContain('账号「没有编号但有保存名称」：')
  expect(w.text()).not.toContain('账号empty-account')
})

// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

const h = vi.hoisted(() => ({ workspace: null as any, route: null as any, calls: [] as { path: string; body: any }[], records: [] as any[], attention: null as any, check: null as any, unlink: null as any }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (v: any) => { h.route.query = v.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', () => ({ useAdminAccounts: () => ({ currentAccount: h.workspace }) }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (s: number) => s === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status })
const deferred = <T>() => { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const record = (id: string, site = 'site-a', status = '') => ({ connection: { id, upstreamSiteId: site, siteName: site === 'site-a' ? '维护网站甲' : '维护网站乙', upstreamGroupId: 'gone', upstreamGroupName: '维护组' + id, upstreamKeyId: 'key-' + id, adminAccountId: 'account-' + id, adminAccountName: '原账号' + id, ownGroupIds: ['10'], ownGroupNames: ['自有组'], groupType: 'openai', status, adminPlatform: 'sub2api', upstreamPlatform: 'sub2api', provisioningMode: 'managed', pricingMappingEnabled: true, canDeleteRemote: true, createdAt: '2031-01-01T00:00:00Z' }, reason: 'group_not_found', detail: '保存资料未找到分组', lastSyncedAt: null })
const attentionResponse = (query: URLSearchParams, workspace = h.workspace.value.id) => {
  const page = Number(query.get('page'))
  const values = h.records.filter(item => !query.get('siteId') || item.connection.upstreamSiteId === query.get('siteId'))
  return json({ workspaceAdminAccountId: workspace, total: values.length, page, pageSize: 30, totalPages: Math.ceil(values.length / 30), items: values.slice((page - 1) * 30, page * 30) })
}
const button = (w: VueWrapper, text: string) => w.findAll('button').find(x => x.text().trim() === text)!
const click = async (w: VueWrapper, text: string) => { const b = button(w, text); expect(b).toBeDefined(); await b.trigger('click'); await flushPromises() }
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w }
beforeEach(() => {
  h.workspace = ref({ id: 'ws-maintenance', platform: 'sub2api' }); h.route = reactive({ query: {} }); h.calls = []; h.records = [record('101')]
  h.attention = attentionResponse
  h.check = async (body: any) => json({ workspaceAdminAccountId: body.workspaceAdminAccountId, total: 0, confirmedConnectionIds: [], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: {}, issues: [], storageUncertain: false })
  h.unlink = async (body: any) => { h.records = h.records.filter(item => item.connection.id !== body.connectionId); return json({ ok: true }) }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    if (String(url).includes('/my-sites/own-group-rates')) return json({ workspaceAdminAccountId: new URL(String(url), 'http://local').searchParams.get('workspaceAdminAccountId'), groups: [] })
    const path = String(url), body = init.body ? JSON.parse(String(init.body)) : null; h.calls.push({ path, body })
    if (path.includes('/needs-attention')) return h.attention(new URL(path, 'http://local').searchParams)
    if (path.includes('/check-scoped')) return h.check(body)
    if (path.includes('/list-scoped')) return json({ workspaceAdminAccountId: body.workspaceAdminAccountId, items: [] })
    if (path.includes('/real-disconnect')) return h.unlink(body)
    if (path.includes('/upstream-sites/options')) return json({ workspaceAdminAccountId: h.workspace.value.id, items: [{ id: 'site-a', name: '维护网站甲' }, { id: 'site-b', name: '维护网站乙' }] })
    if (path.includes('/group-rates?')) return json({ workspaceAdminAccountId: h.workspace.value.id, items: [], total: 0, page: 1, pageSize: 30, totalPages: 0, types: [], platforms: [], statusCounts: { all: 0, mapped: 0, unmapped: 0, deleted: 0 } })
    if (path.includes('/mapping-options')) return json({ ownGroups: [], mappings: [] })
    return json({})
  }))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('maintenance scope, confirmed identity and outcomes', () => {
  it('paginates 31 records independently and retains its website when rate sorting changes', async () => {
    h.records = Array.from({ length: 31 }, (_, i) => record(String(i + 1), 'site-b'))
    const w = await open(); await click(w, '查看已有对接')
    await w.get('select[aria-label="维护网站"]').setValue('site-b'); await flushPromises()
    expect(w.findAll('[data-attention-connection-id]')).toHaveLength(30)
    await click(w, '下一页已有对接'); expect(w.findAll('[data-attention-connection-id]')).toHaveLength(1); expect(w.text()).toContain('原账号31')
    const attentionCalls = h.calls.filter(x => x.path.includes('/needs-attention')).length
    const sort = w.findAll('select').find(x => x.find('option[value="groupNameAsc"]').exists())!
    await sort.setValue('groupNameAsc'); await flushPromises()
    expect(h.calls.filter(x => x.path.includes('/needs-attention'))).toHaveLength(attentionCalls)
    expect(w.get('select[aria-label="维护网站"]').element).toHaveProperty('value', 'site-b')
    expect(w.text()).toContain('原账号31')
  })
  it('cannot let a late old-workspace local read replace new records', async () => {
    const pending = deferred<Response>(); h.attention = () => pending.promise
    const w = await open(); h.records = [record('new')]; h.attention = attentionResponse
    h.workspace.value = { id: 'ws-new', platform: 'sub2api' }; await flushPromises()
    pending.resolve(json({ workspaceAdminAccountId: 'ws-maintenance', total: 1, page: 1, pageSize: 30, totalPages: 1, items: [record('old')] })); await flushPromises()
    await click(w, '查看已有对接'); expect(w.text()).toContain('原账号new'); expect(w.text()).not.toContain('原账号old')
  })
  it('manual status confirmation does not survive an account rebind during reread', async () => {
    const w = await open(); await click(w, '查看已有对接')
    h.check = async (body: any) => { h.records[0].connection.adminAccountId = 'changed'; return json({ workspaceAdminAccountId: body.workspaceAdminAccountId, total: 1, confirmedConnectionIds: ['101'], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: { '101': 'active' }, issues: [], storageUncertain: false }) }
    await click(w, '核对本站账号'); expect(w.text()).toContain('本页有0条对接的账号状态已确认')
    await click(w, '处理对接'); await click(w, '确定')
    expect(h.calls.find(x => x.path.includes('/real-disconnect'))!.body.expectedConnection.adminAccountId).toBe('changed')
  })
  it('keeps local unlink available after a failed check without opening remote removal', async () => {
    const w = await open(); await click(w, '查看已有对接'); h.check = async () => json({ message: '本站账号核对未完成', detail: '核对网络中断', next: '请重试核对' }, 503)
    await click(w, '核对本站账号'); expect(w.text()).toContain('核对网络中断')
    await click(w, '处理对接'); expect(w.get('[role="dialog"]').find('input[value="full"]').exists()).toBe(false)
    expect(w.get('[role="dialog"]').text()).toContain('不会删除主站账号或上游Key')
  })
  it('preserves the dialog and exact partial outcome on checked unlink failure', async () => {
    h.records[0].connection.status = 'missing'
    h.unlink = async () => json({ message: '运行记录已清理，对接仍保留', detail: '映射保存连接中断', next: '请先读取对接再核查数据库提交结果' }, 503)
    const w = await open(); await click(w, '查看已有对接'); await click(w, '处理对接'); await click(w, '确定')
    expect(w.get('[role="dialog"]').text()).toContain('运行记录已清理，对接仍保留')
    expect(w.get('[role="dialog"]').text()).toContain('映射保存连接中断')
    expect(w.get('[role="dialog"]').text()).toContain('核查数据库提交结果')
    expect(w.text()).toContain('原账号101'); expect(w.text()).not.toContain('全部回滚')
  })
  it('retains unlink when the website is absent and never sends an empty or broad check', async () => {
    h.records = [record('101', 'site-gone')]
    const w = await open(); await click(w, '查看已有对接')
    expect(button(w, '核对本站账号').element).toHaveProperty('disabled', true)
    await click(w, '处理对接'); await click(w, '确定')
    expect(h.calls.filter(x => x.path.includes('/check-scoped')).every(x => Array.isArray(x.body.groupRefs) && x.body.groupRefs.length === 0)).toBe(true)
    expect(h.calls.find(x => x.path.includes('/real-disconnect'))!.body.expectedConnection.upstreamSiteId).toBe('site-gone')
    expect(w.text()).not.toContain('已有对接需要查看')
  })
  it('does not close a new-workspace dialog when old unlink resolves late', async () => {
    const pending = deferred<Response>(); h.unlink = () => pending.promise
    const w = await open(); await click(w, '查看已有对接'); await click(w, '处理对接'); await click(w, '确定')
    h.records = [record('new')]; h.workspace.value = { id: 'ws-new', platform: 'sub2api' }; await flushPromises()
    await click(w, '查看已有对接'); await click(w, '处理对接')
    pending.resolve(json({ ok: true })); await flushPromises()
    expect(w.get('[role="dialog"]').text()).toContain('维护组new')
    expect(h.calls.filter(x => x.path.includes('/real-disconnect'))).toHaveLength(1)
  })
  it('unknown-source wording cannot claim upstream deletion and explicit mapping removal is opt-in', async () => {
    h.records[0].reason = 'source_unconfirmed'; h.records[0].detail = '网站分组资料为空'
    const w = await open(); await click(w, '查看已有对接'); expect(w.text()).toContain('网站分组资料为空'); expect(w.text()).toContain('对接和主站账号未改变')
    await click(w, '处理对接'); await w.get('[role="dialog"] input[type="checkbox"]').setValue(true); await click(w, '确定')
    expect(h.calls.find(x => x.path.includes('/real-disconnect'))!.body.removePricingMapping).toBe(true)
    expect(h.calls.filter(x => x.path.includes('/mapping-options'))).toHaveLength(0)
  })
})

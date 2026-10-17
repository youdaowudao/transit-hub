// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

const h = vi.hoisted(() => ({ workspace: null as any, route: null as any, requests: [] as string[], read: null as any, connected: false }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ replace: vi.fn(async (v: any) => { h.route.query = v.query }), push: vi.fn() }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', () => ({ useAdminAccounts: () => ({ currentAccount: h.workspace }) }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: () => false }))
const response = (value: unknown) => new Response(JSON.stringify(value), { status: 200 })
const pendingRead = () => { let resolve!: (value: Response) => void; const promise = new Promise<Response>(r => { resolve = r }); return { promise, resolve } }
const connection = (id = 'attention-current', group = 'gone') => ({ id, upstreamSiteId: 'site-a', upstreamGroupId: group, upstreamGroupName: group, upstreamKeyId: 'fixture-key', adminAccountId: 'fixture-account', adminAccountName: id, ownGroupIds: ['10'], ownGroupNames: ['own'], groupType: 'openai', status: 'active', pricingMappingEnabled: true, createdAt: '2031-01-01T00:00:00Z' })
const attention = (query: URLSearchParams, stale = false) => {
  const page = Number(query.get('page') || '1')
  const items = Array.from({ length: 31 }, (_, i) => ({ connection: connection(stale ? 'stale-' + i : 'attention-current-' + i), reason: 'group_not_found', detail: '保存资料未找到这个分组', lastSyncedAt: null }))
  return response({ workspaceAdminAccountId: h.workspace.value.id, total: 31, page, pageSize: 30, totalPages: 2, items: items.slice((page - 1) * 30, page * 30) })
}
const wrappers: VueWrapper[] = []
const button = (w: VueWrapper, name: string) => { const found = w.findAll('button').find(x => x.text().trim() === name); expect(found, name).toBeDefined(); return found! }
const click = async (w: VueWrapper, name: string) => { await button(w, name).trigger('click'); await flushPromises() }
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w }
const openWindow = async (w: VueWrapper, kind: string) => {
  const row = w.get('#group-rates-panel tbody tr')
  if (kind === 'history') await row.get('button[aria-label*="涨跌幅历史"]').trigger('click')
  else if (kind === 'category') await row.get('button[aria-label*="分类"]').trigger('click')
  else if (kind === 'connector') await click(w, '配置对接')
  else await click(w, '取消对接')
  await flushPromises()
  expect(w.find('[role="dialog"]').exists()).toBe(true)
}
const closeWindow = async (w: VueWrapper, kind: string) => {
  const dialog = w.get('[role="dialog"]')
  if (kind === 'history') await dialog.findAll('button').find(x => x.text().includes('关闭历史'))!.trigger('click')
  else await dialog.findAll('button').find(x => x.text().trim() === '取消')!.trigger('click')
  await flushPromises()
  expect(w.find('[role="dialog"]').exists()).toBe(false)
}

beforeEach(() => {
  h.workspace = ref({ id: 'ws-dialog', platform: 'sub2api' }); h.route = reactive({ query: {} }); h.requests = []; h.connected = false; h.read = attention
  vi.stubGlobal('fetch', vi.fn(async (input: string, options: RequestInit = {}) => {
    if (String(input).includes('/my-sites/own-group-rates')) return response({ workspaceAdminAccountId: new URL(String(input), 'http://local').searchParams.get('workspaceAdminAccountId'), groups: [] })
    const path = String(input); h.requests.push(path)
    if (path.includes('/needs-attention')) return h.read(new URL(path, 'http://local').searchParams)
    if (path.includes('/upstream-sites/options')) return response({ workspaceAdminAccountId: h.workspace.value.id, items: [{ id: 'site-a', name: 'fixture site' }] })
    if (path.includes('/list-scoped')) return response({ workspaceAdminAccountId: h.workspace.value.id, items: h.connected ? [connection('live-connection', 'live')] : [] })
    if (path.includes('/check-scoped')) return response({ workspaceAdminAccountId: h.workspace.value.id, total: h.connected ? 1 : 0, confirmedConnectionIds: h.connected ? ['live-connection'] : [], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: h.connected ? { 'live-connection': 'active' } : {}, issues: [], storageUncertain: false })
    if (path.includes('/group-rates?')) return response({ workspaceAdminAccountId: h.workspace.value.id, items: [{ siteId: 'site-a', siteName: 'fixture site', groupId: 'live', groupName: 'live', platform: 'sub2api', type: 'openai', mapped: h.connected, connected: h.connected, deleted: false, upstreamMultiplier: 2, rechargeRate: 1, currentMultiplier: 2, delta: 1, deltaPercent: 100, updatedAt: '2031-01-01T00:00:00Z' }], total: 1, page: 1, pageSize: 30, totalPages: 1, types: ['openai'], platforms: ['sub2api'], statusCounts: { all: 1, mapped: h.connected ? 1 : 0, unmapped: h.connected ? 0 : 1, deleted: 0 } })
    if (path.includes('/group-rates/history')) return response([])
    if (path.includes('/mapping-options')) return response({ ownGroups: [], mappings: [] })
    return response({})
  }))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('maintenance reads interrupted by existing windows', () => {
  it.each(['history', 'category', 'connector', 'disconnect'])('keeps maintenance reachable after %s closes during the first slow read', async kind => {
    h.connected = kind === 'disconnect'
    const pending = pendingRead(); h.read = () => pending.promise
    const w = await open(); await openWindow(w, kind)
    h.read = attention; await closeWindow(w, kind)
    pending.resolve(attention(new URLSearchParams('page=1'), true)); await flushPromises()
    // A completed, still-valid read is allowed to survive opening a window;
    // an aborted read must recover or expose an actionable local retry.
    if (!w.text().includes('查看已有对接')) await click(w, '重试读取已有对接')
    await click(w, '查看已有对接')
    expect(w.text()).toContain('attention-current-0')
    expect(w.text()).not.toContain('stale-')
  })
  it.each(['history', 'category', 'connector', 'disconnect'])('preserves the maintenance website when a slow filtered read is interrupted by %s', async kind => {
    h.connected = kind === 'disconnect'
    const w = await open(); await click(w, '查看已有对接')
    const pending = pendingRead(); h.read = () => pending.promise
    await w.get('select[aria-label="维护网站"]').setValue('site-a'); await flushPromises()
    await openWindow(w, kind); h.read = attention; await closeWindow(w, kind)
    pending.resolve(attention(new URLSearchParams('siteId=site-a&page=1'), true)); await flushPromises()
    if (!w.text().includes('attention-current-0')) await click(w, '重试读取已有对接')
    expect(w.get('select[aria-label="维护网站"]').element).toHaveProperty('value', 'site-a')
    expect(w.text()).toContain('attention-current-0'); expect(w.text()).not.toContain('stale-')
    expect(new URL(h.requests.filter(x => x.includes('/needs-attention')).at(-1)!, 'http://local').searchParams.get('siteId')).toBe('site-a')
  })
  it.each(['history', 'category', 'connector', 'disconnect'])('preserves page 2 when a slow maintenance read is interrupted by %s', async kind => {
    h.connected = kind === 'disconnect'
    const w = await open(); await click(w, '查看已有对接')
    const pending = pendingRead(); h.read = () => pending.promise
    await click(w, '下一页已有对接'); await openWindow(w, kind)
    h.read = attention; await closeWindow(w, kind)
    pending.resolve(attention(new URLSearchParams('page=2'), true)); await flushPromises()
    if (!w.text().includes('attention-current-30')) await click(w, '重试读取已有对接')
    expect(w.findAll('[data-attention-connection-id]')).toHaveLength(1)
    expect(w.text()).toContain('attention-current-30'); expect(w.text()).not.toContain('stale-')
    expect(new URL(h.requests.filter(x => x.includes('/needs-attention')).at(-1)!, 'http://local').searchParams.get('page')).toBe('2')
  })
})

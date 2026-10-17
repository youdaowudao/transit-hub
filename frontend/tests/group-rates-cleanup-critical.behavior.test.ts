// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

const h = vi.hoisted(() => ({ route: null as any, calls: [] as { path: string; body: any }[], attention: [] as any[], failed: false }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (v: any) => { h.route.query = v.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-cleanup', platform: 'sub2api' }) }) } })
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (s: number) => s === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status })
const connection = (id: string, status = 'active') => ({ id, upstreamSiteId: 'site-a', upstreamSiteName: '测试网站', upstreamGroupId: 'gone', upstreamGroupName: '已无倍率的组', upstreamKeyId: 'key-' + id, adminAccountId: id, adminAccountName: '保留账号' + id, ownGroupIds: ['10'], ownGroupNames: ['自有组'], groupType: 'openai', status, adminPlatform: 'sub2api', upstreamPlatform: 'sub2api', provisioningMode: 'managed', pricingMappingEnabled: true, canDeleteRemote: true, createdAt: '2031-01-01T00:00:00Z' })
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w }
const click = async (w: VueWrapper, text: string) => { const b = w.findAll('button').find(x => x.text().includes(text)); expect(b, `button ${text}`).toBeDefined(); await b!.trigger('click'); await flushPromises() }
beforeEach(() => {
  h.route = reactive({ query: {} }); h.calls = []; h.failed = false
  h.attention = ['101', '102'].map((id, i) => ({ connection: connection(id, i ? 'missing' : 'active'), reason: 'group_not_found', detail: 'Hub保存的上游分组资料里未找到这个分组，不能据此确认上游删除。已有对接仍保留，你可以核对账号或解除本地对接。', lastSyncedAt: 1924992000000 }))
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    if (String(url).includes('/my-sites/own-group-rates')) return json({ workspaceAdminAccountId: new URL(String(url), 'http://local').searchParams.get('workspaceAdminAccountId'), groups: [] })
    const path = String(url); const body = init.body ? JSON.parse(String(init.body)) : null; h.calls.push({ path, body })
    if (path.includes('/needs-attention')) return h.failed ? json({ message: '已有对接暂时无法读取', detail: '测试读取连接被中断', next: '请重试读取' }, 503) : json({ workspaceAdminAccountId: 'ws-cleanup', total: h.attention.length, page: 1, pageSize: 30, totalPages: h.attention.length ? 1 : 0, items: h.attention })
    if (path.includes('/check-scoped')) return json({ workspaceAdminAccountId: 'ws-cleanup', total: h.attention.length, confirmedConnectionIds: h.attention.map(x => x.connection.id), unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: Object.fromEntries(h.attention.map(x => [x.connection.id, x.connection.status])), issues: [], storageUncertain: false })
    if (path.includes('/list-scoped')) return json({ workspaceAdminAccountId: 'ws-cleanup', items: [] })
    if (path.includes('/real-disconnect')) { h.attention = h.attention.filter(x => x.connection.id !== body.connectionId); return json({ success: true }) }
    if (path.endsWith('/real-connections')) return json([])
    if (path.includes('/upstream-sites/options')) return json({ workspaceAdminAccountId: 'ws-cleanup', items: [{ id: 'site-a', name: '测试网站' }] })
    if (path.includes('/group-rates?')) return json({ workspaceAdminAccountId: 'ws-cleanup', items: [], total: 0, page: 1, pageSize: 30, totalPages: 0, types: [], platforms: [], statusCounts: { all: 0, mapped: 0, unmapped: 0, deleted: 23 } })
    if (path.includes('/dashboard')) return json({ platform: 'sub2api' })
    if (path.includes('mapping-options')) return json({ ownGroups: [], mappings: [] })
    return json({})
  }))
})
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('cleanup critical maintenance contract', () => {
  it('shows only three rate tabs and explains preserved history', async () => {
    const w = await open()
    expect(w.findAll('[role="tab"]')).toHaveLength(3)
    expect(w.findAll('[role="tab"]').map(x => x.text()).join(' ')).not.toContain('已删除')
    expect(w.text()).toContain('上次成功读取的可用分组倍率')
  })
  it('loads maintenance locally and shows duplicates without inventing rates or checking main site', async () => {
    const w = await open()
    expect(h.calls.some(x => x.path.includes('/needs-attention'))).toBe(true)
    expect(h.calls.filter(x => x.path.includes('/check-scoped')).every(x => !x.body.siteId && x.body.groupRefs?.length === 0)).toBe(true)
    await click(w, '查看已有对接')
    expect(w.text()).toContain('保留账号101'); expect(w.text()).toContain('保留账号102')
    expect(w.text()).toContain('不能据此确认上游删除')
    expect(w.findAll('tbody tr').filter(x => x.text().includes('已无倍率的组'))).toHaveLength(2)
    expect(w.findAll('tbody tr').filter(x => x.text().includes('已无倍率的组')).every(x => !x.text().includes('历史'))).toBe(true)
  })
  it('uses local unlink by default and preserves mapping with a frozen public identity', async () => {
    const w = await open(); const checksBefore = h.calls.filter(x => x.path.includes('/check-scoped')).length; await click(w, '查看已有对接'); await click(w, '处理对接')
    const dialog = w.get('[role="dialog"]')
    expect(dialog.text()).toContain('仅解除本地对接')
    expect(dialog.text()).not.toContain('删除账号和 Key')
    expect((dialog.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)
    await click(w, '确定')
    const request = h.calls.find(x => x.path.includes('/real-disconnect'))!
    expect(request?.body).toMatchObject({ connectionId: '101', mode: 'unlink', removePricingMapping: false, expectedConnection: { workspaceAdminAccountId: 'ws-cleanup', id: '101', upstreamSiteId: 'site-a', upstreamGroupId: 'gone', adminAccountId: '101' } })
    expect(h.calls.filter(x => x.path.includes('/check-scoped'))).toHaveLength(checksBefore)
    expect(h.calls.filter(x => x.path.includes('/sync'))).toHaveLength(0)
    expect(w.text()).toContain('保留账号102'); expect(w.text()).not.toContain('保留账号101')
  })
  it('manual account check uses the real website scope after history is gone', async () => {
    const w = await open(); await click(w, '查看已有对接'); await click(w, '核对本站账号')
    const check = h.calls.filter(x => x.path.includes('/check-scoped')).at(-1)!
    expect(check?.body).toMatchObject({ workspaceAdminAccountId: 'ws-cleanup', mode: 'manual', siteId: 'site-a' })
    expect(check?.body.groupRefs).toBeUndefined()
    expect(h.calls.filter(x => x.path.includes('/sync'))).toHaveLength(0)
  })
  it('does not report a failed maintenance read as zero pending records', async () => {
    h.failed = true; const w = await open()
    expect(w.text()).toContain('已有对接暂时无法读取')
    expect(w.text()).toContain('测试读取连接被中断')
    expect(w.text()).toContain('重试')
    expect(w.findAll('[role="tab"]')).toHaveLength(3)
  })
})

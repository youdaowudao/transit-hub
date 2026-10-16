// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'

// 20261016/4：名字首尾带空白（按 Go strings.TrimSpace 的字符集合）的分组暂不能修改分类，
// 因为后端去空白后按名字匹配，可能改到同站另一组；其他分组照旧按名字保存。

const P11 = '这个分组的名字首尾带空白字符，暂时不能修改分类，请先保留现有分类；这项限制不影响倍率同步，也不会修改主站。'
const h = vi.hoisted(() => ({ route: null as any, calls: [] as { path: string; method: string; body: any }[] }))
vi.mock('vue-router', () => ({ useRoute: () => h.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (v: any) => { h.route.query = v.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-type', platform: 'sub2api' }) }) } })
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (s: number) => s === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status })
const rate = (groupId: string, groupName: string) => ({ siteId: 'site-a', siteName: '测试网站', groupId, groupName, type: 'openai', platform: 'openai', mapped: false, connected: false, pricingMapped: false, deleted: false, upstreamMultiplier: 1, rechargeRate: 1, currentMultiplier: 1, delta: null, deltaPercent: null, updatedAt: '2031-01-01T00:00:00Z' })
const items = [rate('9', 'Claude-Max '), rate('11', '\u0085备用组'), rate('10', '普通组')]
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(GroupRatesView, { attachTo: document.body }); wrappers.push(w); await flushPromises(); return w }
const row = (w: VueWrapper, text: string) => {
  const found = w.findAll('tbody tr').find(x => x.text().includes(text))
  expect(found, `row ${text}`).toBeDefined()
  return found!
}
const typeButton = (w: VueWrapper, text: string) => {
  const found = row(w, text).findAll('button').find(b => b.attributes('aria-label') === P11 || (b.attributes('aria-label') ?? '').includes('的分类'))
  expect(found, `type button ${text}`).toBeDefined()
  return found!
}
beforeEach(() => {
  h.route = reactive({ query: {} }); h.calls = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const path = String(url); const body = init.body ? JSON.parse(String(init.body)) : null; h.calls.push({ path, method: init.method ?? 'GET', body })
    if (path.includes('/group-rates/type')) return json({ success: true })
    if (path.includes('/group-rates?')) return json({ workspaceAdminAccountId: 'ws-type', items, total: items.length, page: 1, pageSize: 30, totalPages: 1, types: ['openai'], platforms: ['openai'], statusCounts: { all: items.length, mapped: 0, unmapped: items.length, deleted: 0 } })
    if (path.includes('/needs-attention')) return json({ workspaceAdminAccountId: 'ws-type', total: 0, page: 1, pageSize: 30, totalPages: 0, items: [] })
    if (path.includes('/check-scoped')) return json({ workspaceAdminAccountId: 'ws-type', total: 0, confirmedConnectionIds: [], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: {}, issues: [], storageUncertain: false })
    if (path.includes('/list-scoped')) return json({ workspaceAdminAccountId: 'ws-type', items: [] })
    if (path.endsWith('/real-connections')) return json([])
    if (path.includes('/upstream-sites/options')) return json({ workspaceAdminAccountId: 'ws-type', items: [{ id: 'site-a', name: '测试网站' }] })
    if (path.includes('/dashboard')) return json({ platform: 'sub2api' })
    if (path.includes('mapping-options')) return json({ ownGroups: [], mappings: [] })
    return json({})
  }))
})
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('group type edit is locked for names with edge whitespace', () => {
  it('locks trailing space and U+0085 names with a visible reason and sends nothing', async () => {
    const w = await open()
    for (const name of ['Claude-Max', '备用组']) {
      const button = typeButton(w, name)
      expect(button.attributes('disabled')).toBeDefined()
      expect(button.attributes('title')).toBe(P11)
      expect(button.attributes('aria-label')).toBe(P11)
      expect(row(w, name).text()).toContain(P11)
      await button.trigger('click')
      await flushPromises()
      expect(w.find('[data-group-rates-dialog]').exists()).toBe(false)
    }
    expect(h.calls.filter(x => x.path.includes('/group-rates/type'))).toHaveLength(0)
  })
  it('keeps ordinary groups editable and saved by name', async () => {
    const w = await open()
    const button = typeButton(w, '普通组')
    expect(button.attributes('disabled')).toBeUndefined()
    expect(row(w, '普通组').text()).not.toContain(P11)
    await button.trigger('click')
    await flushPromises()
    const dialog = w.get('[data-group-rates-dialog]')
    await dialog.get('form').trigger('submit')
    await flushPromises()
    const patch = h.calls.filter(x => x.path.includes('/group-rates/type'))
    expect(patch).toHaveLength(1)
    expect(patch[0]!.method).toBe('PATCH')
    expect(patch[0]!.body).toMatchObject({ siteId: 'site-a', groupName: '普通组' })
  })
})

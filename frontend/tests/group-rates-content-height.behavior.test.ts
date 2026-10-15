// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import type { GroupRate } from '@/modules/admin/types/groupRates'

const h = vi.hoisted(() => ({
  route: null as unknown as { query: Record<string, string> },
  rates: [] as GroupRate[],
  pageSize: 30,
  calls: [] as { path: string; method: string }[],
  unexpected: [] as string[],
  listError: '',
  localError: false,
  listGate: null as Promise<void> | null,
  checkGate: null as Promise<void> | null,
}))
vi.mock('vue-router', () => ({
  useRoute: () => h.route,
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(async ({ query }) => { h.route.query = query }) }),
}))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-height', platform: 'sub2api' }) }) }
})
vi.mock('@/modules/auth/api/auth', () => ({
  getAccessToken: () => null, handleAuthExpired: vi.fn(),
  isUnauthorizedApiResponse: (status: number) => status === 401,
  authUnauthorizedErrorKey: 'auth.errors.unauthorized',
}))

const hint = '列表按当前页条数展开，表头和翻页按钮随列表一起滚动；左右滑动可查看其余列。查看列表不会修改主站。'
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const gate = () => {
  let resolve!: () => void
  const promise = new Promise<void>(done => { resolve = done })
  return { promise, resolve }
}
const rates = (count: number): GroupRate[] => Array.from({ length: count }, (_, index) => ({
  siteId: 'site-height', siteName: '高度测试站点', groupId: String(index + 1),
  groupName: `分组${String(index + 1).padStart(2, '0')}`, type: 'openai', platform: 'sub2api',
  mapped: index % 2 === 0, pricingMapped: index % 2 === 0, deleted: false,
  currentMultiplier: index + 1, delta: null, deltaPercent: null, updatedAt: '2026-10-15T00:00:00Z',
}))
const wrappers: VueWrapper[] = []
const open = async () => {
  const wrapper = mount(GroupRatesView, { attachTo: document.body })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
const panel = (wrapper: VueWrapper) => wrapper.get('#group-rates-panel')
const rows = (wrapper: VueWrapper) => panel(wrapper).findAll('tbody tr')
const names = (wrapper: VueWrapper) => rows(wrapper).map(row => row.findAll('td')[1]!.get('span').text())
const button = (wrapper: VueWrapper, label: string) => {
  const found = wrapper.findAll('button').find(item => item.text().trim() === label)
  expect(found, label).toBeDefined()
  return found!
}
const checks = () => h.calls.filter(call => call.path.includes('/check-scoped'))

beforeEach(() => {
  h.route = reactive({ query: {} })
  h.rates = rates(1)
  h.pageSize = 30
  h.calls = []
  h.unexpected = []
  h.listError = ''
  h.localError = false
  h.listGate = null
  h.checkGate = null
  vi.stubGlobal('fetch', vi.fn(async (input: string, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://height.test')
    const path = url.pathname
    const method = init.method ?? 'GET'
    h.calls.push({ path: url.pathname + url.search, method })
    if (path.endsWith('/group-rates') && method === 'GET') {
      await h.listGate
      if (h.listError) return json({ message: h.listError }, 500)
      const q = url.searchParams
      const base = h.rates.filter(rate => (!q.get('search') || rate.groupName.includes(q.get('search')!))
        && (!q.get('siteId') || rate.siteId === q.get('siteId'))
        && (!q.get('type') || rate.type === q.get('type'))
        && (!q.get('platform') || rate.platform === q.get('platform')))
      const filtered = base.filter(rate => q.get('status') === 'mapped' ? rate.mapped : q.get('status') === 'unmapped' ? !rate.mapped : true)
      if (q.get('sort') === 'multiplierDesc') filtered.sort((a, b) => b.currentMultiplier! - a.currentMultiplier!)
      const page = Number(q.get('page') || 1)
      return json({ workspaceAdminAccountId: 'ws-height', items: filtered.slice((page - 1) * h.pageSize, page * h.pageSize),
        total: filtered.length, page, pageSize: h.pageSize, totalPages: Math.ceil(filtered.length / h.pageSize),
        types: ['openai', 'grok'], platforms: ['sub2api'],
        statusCounts: { all: base.length, mapped: base.filter(rate => rate.mapped).length, unmapped: base.filter(rate => !rate.mapped).length, deleted: 0 } })
    }
    if (path.endsWith('/real-connections/list-scoped') && method === 'POST') {
      return h.localError ? json({ message: '对接资料读取中断，请刷新重试' }, 500) : json({ workspaceAdminAccountId: 'ws-height', items: [] })
    }
    if (path.endsWith('/real-connections/check-scoped') && method === 'POST') {
      await h.checkGate
      return json({ workspaceAdminAccountId: 'ws-height', total: 0, confirmedConnectionIds: [], unconfirmedConnectionIds: [],
        skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: {}, issues: [], storageUncertain: false })
    }
    if (path.endsWith('/real-connections/needs-attention') && method === 'GET') {
      return json({ workspaceAdminAccountId: 'ws-height', items: [], total: 0, page: Number(url.searchParams.get('page')), pageSize: 30, totalPages: 0 })
    }
    if (path.endsWith('/upstream-sites/options') && method === 'GET') {
      return json({ workspaceAdminAccountId: 'ws-height', items: [{ id: 'site-height', name: '高度测试站点' }] })
    }
    if (path.endsWith('/group-rates/history') && method === 'GET') return json([])
    h.unexpected.push(`${method} ${path}`)
    throw new Error(`Unexpected test request: ${method} ${path}`)
  }))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
  localStorage.clear()
  expect(h.unexpected).toEqual([])
})

describe('group rates content height and retained interactions', () => {
  it.each([0, 1, 10, 30])('renders exactly %i current-page rows with consistent empty state', async count => {
    h.rates = rates(count)
    const wrapper = await open()
    expect(names(wrapper)).toEqual(h.rates.map(rate => rate.groupName))
    expect(panel(wrapper).find('table').exists()).toBe(count > 0)
    expect(panel(wrapper).find('h3').exists()).toBe(count === 0)
    expect(wrapper.text()).toContain(hint)
    expect(checks()).toHaveLength(1)
  })

  // jsdom does not measure layout: these assertions protect the agreed CSS structure only.
  it('removes bounded vertical layout while retaining horizontal access and footer order', async () => {
    const wrapper = await open()
    const table = panel(wrapper).get('table')
    const container = table.element.parentElement!
    for (const element of [wrapper.element, panel(wrapper).element, container]) {
      expect(Array.from(element.classList).filter(value => /(^|:)(h-|min-h-|max-h-|flex-1$)/.test(value))).toEqual([])
    }
    expect(container.classList.contains('overflow-x-auto')).toBe(true)
    expect(table.classes()).toContain('min-w-[980px]')
    expect(table.get('thead').classes()).not.toContain('sticky')
    expect(container.nextElementSibling?.textContent).toContain('下一页')
    expect(panel(wrapper).get('tbody').exists()).toBe(true)
  })

  it('shows a compact loading block and then replaces it with rows', async () => {
    const pending = gate()
    h.listGate = pending.promise
    const wrapper = await open()
    expect(panel(wrapper).find('table').exists()).toBe(false)
    expect(panel(wrapper).get('.min-h-48').text()).toContain('加载')
    pending.resolve()
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组01'])
    expect(panel(wrapper).find('.min-h-48').exists()).toBe(false)
  })

  it('uses a compact empty block without leaving a table behind', async () => {
    h.rates = []
    const wrapper = await open()
    expect(panel(wrapper).get('.min-h-48').find('h3').exists()).toBe(true)
    expect(rows(wrapper)).toHaveLength(0)
  })

  it('replaces 30 first-page rows with the single last row and opens its own history', async () => {
    h.rates = rates(31)
    const wrapper = await open()
    expect(names(wrapper)).toEqual(h.rates.slice(0, 30).map(rate => rate.groupName))
    const checkCount = checks().length
    await button(wrapper, '下一页').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组31'])
    expect(panel(wrapper).text()).toContain('31')
    expect(button(wrapper, '下一页').element).toHaveProperty('disabled', true)
    expect(checks()).toHaveLength(checkCount)
    await rows(wrapper)[0]!.get('button[aria-label*="分组31"][aria-label*="历史"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toContain('分组31')
    const history = h.calls.find(call => call.path.includes('/group-rates/history'))!
    expect(new URL(history.path, 'http://height.test').searchParams.get('groupName')).toBe('31')
  })

  it('respects the response page size instead of forcing 30', async () => {
    h.pageSize = 10
    h.rates = rates(11)
    const wrapper = await open()
    expect(rows(wrapper)).toHaveLength(10)
    expect(panel(wrapper).text()).toContain('每页 10 条')
    await button(wrapper, '下一页').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组11'])
  })

  it('filters to one row, zero rows and back without extra account checks', async () => {
    h.rates = rates(30)
    const wrapper = await open()
    const checkCount = checks().length
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const input = wrapper.get('input[name="groupRateSearch"]')
    for (const [query, expected] of [['分组30', ['分组30']], ['不存在的组', []], ['', h.rates.map(rate => rate.groupName)]] as const) {
      await input.setValue(query)
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(names(wrapper)).toEqual(expected)
      expect(panel(wrapper).find('h3').exists()).toBe(expected.length === 0)
    }
    expect(checks()).toHaveLength(checkCount)
  })

  it('retains status tabs and sorting without expanding the remote check scope', async () => {
    h.rates = rates(10)
    const wrapper = await open()
    const checkCount = checks().length
    const tabs = wrapper.findAll('[role="tab"]')
    await tabs[1]!.trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(h.rates.filter(rate => rate.mapped).map(rate => rate.groupName))
    await tabs[2]!.trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(h.rates.filter(rate => !rate.mapped).map(rate => rate.groupName))
    const sort = wrapper.findAll('select').find(select => select.find('option[value="multiplierDesc"]').exists())!
    await sort.setValue('multiplierDesc')
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组10', '分组08', '分组06', '分组04', '分组02'])
    expect(checks()).toHaveLength(checkCount)
  })

  it('preserves a detailed read error and recovers through the original refresh button', async () => {
    h.listError = '倍率记录读取中断：测试长原因'.repeat(8) + '；请刷新并检查Hub读取记录'
    const wrapper = await open()
    expect(wrapper.text()).toContain(h.listError)
    expect(rows(wrapper)).toHaveLength(0)
    h.listError = ''
    await button(wrapper, '刷新数据').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组01'])
    expect(wrapper.text()).not.toContain('测试长原因')
  })

  it('retains unknown connection state and disables refresh during an in-flight check', async () => {
    h.localError = true
    const wrapper = await open()
    expect(rows(wrapper)[0]!.text()).toContain('对接状态暂时未知')
    expect(rows(wrapper)[0]!.text()).not.toContain('配置对接')
    expect(checks()).toHaveLength(0)
    h.localError = false
    const pending = gate()
    h.checkGate = pending.promise
    await button(wrapper, '刷新数据').trigger('click')
    await flushPromises()
    expect(button(wrapper, '刷新数据').element).toHaveProperty('disabled', true)
    pending.resolve()
    await flushPromises()
    expect(button(wrapper, '刷新数据').element).toHaveProperty('disabled', false)
    expect(rows(wrapper)[0]!.text()).not.toContain('对接状态暂时未知')
  })
})

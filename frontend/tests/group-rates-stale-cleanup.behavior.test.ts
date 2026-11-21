// @vitest-environment jsdom
import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import type { RealConnection, ScopedCheckIssue } from '@/modules/admin/types/mySites'

const state = vi.hoisted(() => ({
  route: null as any,
  account: null as any,
  rendered: null as VueWrapper | null,
  calls: [] as { path: string; body: any }[],
  lists: [] as RealConnection[][],
  check: null as null | ((siteId: string, signal?: AbortSignal | null) => Promise<Response>),
  unlink: null as null | ((id: string) => Promise<Response>),
}))
vi.mock('vue-router', () => ({ useRoute: () => state.route, useRouter: () => ({ push: vi.fn(), replace: vi.fn(async (next: any) => { state.route.query = next.query }) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', () => ({ useAdminAccounts: () => state.account }))
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))

const json = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status })
const connection = (id: string, siteId: string, fields: Partial<RealConnection> = {}): RealConnection => ({
  id, upstreamSiteId: siteId, siteName: `网站 ${siteId}`, upstreamGroupId: 'upstream-group', upstreamGroupName: '上游组', upstreamKeyId: `key-${id}`,
  adminAccountId: `account-${id}`, adminAccountName: `主站账号 ${id}`, ownGroupIds: ['2', '1', '2'], ownGroupNames: ['乙', '甲'], groupType: 'openai',
  status: 'missing', adminPlatform: 'sub2api', upstreamPlatform: 'sub2api', provisioningMode: 'managed', pricingMappingEnabled: false,
  createdAt: '2031-01-01T00:00:00Z', ...fields,
})
const checked = (confirmedStatuses: Record<string, 'active' | 'missing'>, issues: ScopedCheckIssue[] = [], workspace = 'ws-cleanup') => json({
  workspaceAdminAccountId: workspace, total: Object.keys(confirmedStatuses).length,
  confirmedConnectionIds: Object.keys(confirmedStatuses), confirmedStatuses, issues,
  unconfirmedConnectionIds: [], changedConnectionIds: [], skippedConnectionIds: [], storageUncertain: false,
})
const namedButton = (wrapper: VueWrapper | DOMWrapper<Element>, text: string) => {
  const button = wrapper.findAll('button').find(item => item.text() === text)
  expect(button, `button ${text}`).toBeDefined()
  return button!
}
const mountPage = async () => {
  const wrapper = mount(GroupRatesView, { attachTo: document.body })
  state.rendered = wrapper
  await flushPromises()
  return wrapper
}
const openedCleanup = (wrapper: VueWrapper) => wrapper.get('[role="dialog"]')
const cleanupChecks = () => state.calls.filter(call => call.path === '/api/my-sites/real-connections/check-scoped' && call.body.mode === 'manual')
const cleanupUnlinks = () => state.calls.filter(call => call.path === '/api/my-sites/real-disconnect')
const pendingResponse = () => {
  let finish!: (response: Response) => void
  const promise = new Promise<Response>(resolve => { finish = resolve })
  return { finish, promise }
}

beforeEach(() => {
  state.route = reactive({ query: {} as Record<string, string> })
  state.account = { currentAccount: ref({ id: 'ws-cleanup', platform: 'sub2api' }) }
  state.calls = []
  state.lists = []
  state.check = null
  state.unlink = null
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit = {}) => {
    const { pathname: path, searchParams } = new URL(String(url), 'http://local')
    const body = init.body ? JSON.parse(String(init.body)) : null
    state.calls.push({ path, body })
    const workspace = state.account.currentAccount.value.id
    if (path.endsWith('/check-scoped')) return body.mode === 'manual' && state.check ? state.check(body.siteId, init.signal) : checked({}, [], workspace)
    if (path.endsWith('/real-connections')) return json(state.lists.shift() ?? [])
    if (path.endsWith('/real-disconnect')) return state.unlink ? state.unlink(body.connectionId) : json({ ok: true })
    if (path.endsWith('/own-group-rates')) return json({ workspaceAdminAccountId: workspace, groups: [] })
    if (path.endsWith('/list-scoped') || path.endsWith('/upstream-sites/options')) return json({ workspaceAdminAccountId: workspace, items: [] })
    if (path.endsWith('/needs-attention') || path.endsWith('/group-rates')) return json({
      workspaceAdminAccountId: workspace, items: [], total: 0, page: 1, pageSize: 30, totalPages: 0,
      types: [], platforms: [], statusCounts: { all: 0, mapped: 0, unmapped: 0 },
    })
    if (path.includes('/dashboard')) return json({ platform: state.account.currentAccount.value.platform, date: searchParams.get('date') })
    return json({})
  }))
})
afterEach(() => { state.rendered?.unmount(); state.rendered = null; vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('group rates stale connection cleanup', () => {
  it('only unlinks explicitly confirmed missing rows that are still missing and have no pricing mapping', async () => {
    const stale = connection('stale', 'a')
    const mapped = connection('mapped', 'a', { pricingMappingEnabled: true })
    const restored = connection('restored', 'a')
    const ready = connection('ready', 'a', { status: '', adminPlatform: '' })
    const deselected = connection('deselected', 'a', { status: 'active' })
    const failing = connection('failing', 'a')
    const unrelated = connection('newapi', 'b', { adminPlatform: 'newapi' })
    state.lists = [[stale, mapped, restored, failing, ready, deselected, unrelated], [stale, mapped, { ...restored, status: 'active' }, failing, { ...ready, status: 'missing' }, { ...deselected, status: 'missing' }, unrelated]]
    state.check = async () => checked({ mapped: 'missing', restored: 'missing', failing: 'missing', ready: 'missing', deselected: 'missing' })
    const firstUnlink = pendingResponse()
    state.unlink = async id => id === 'failing' ? firstUnlink.promise : json({ ok: true })

    const page = await mountPage()
    const callsBefore = state.calls.length
    await namedButton(page, '清理失效对接').trigger('click')
    const dialog = openedCleanup(page)
    expect(dialog.text()).toContain('只删除调度站自己的对接记录，不会修改主站账号和上游 Key。')
    expect(state.calls).toHaveLength(callsBefore)
    await namedButton(dialog, '开始核对').trigger('click')
    await flushPromises()
    expect(cleanupChecks().map(call => call.body)).toEqual([{ workspaceAdminAccountId: 'ws-cleanup', mode: 'manual', siteId: 'a' }])
    expect(dialog.text()).toContain('可以清理（3 条）')
    expect(dialog.text()).toContain('需要逐条处理（1 条）')
    expect(dialog.text()).toContain('主站账号 mapped')
    expect(dialog.text()).not.toMatch(/主站账号 (stale|restored|newapi)/)
    const boxes = dialog.findAll<HTMLInputElement>('input[type="checkbox"]')
    expect(boxes.map(box => box.element.checked)).toEqual([true, true, true])
    await boxes[2]!.setValue(false)
    await namedButton(dialog, '解除选中的 2 条').trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('正在解除第 1/2 条')
    expect(cleanupUnlinks().map(call => call.body.connectionId)).toEqual(['failing'])
    firstUnlink.finish(json({ message: '对接已变化', detail: '工作区对接身份已不同', next: '请重新核对这条记录' }, 409))
    await flushPromises()
    expect(cleanupUnlinks().map(call => call.body.connectionId)).toEqual(['failing', 'ready'])
    expect(cleanupUnlinks()[1]?.body).toEqual({ connectionId: 'ready', mode: 'unlink', removePricingMapping: false, expectedConnection: {
      workspaceAdminAccountId: 'ws-cleanup', id: 'ready', upstreamSiteId: 'a', upstreamGroupId: 'upstream-group', upstreamGroupName: '上游组',
      upstreamKeyId: 'key-ready', adminAccountId: 'account-ready', adminPlatform: '', upstreamPlatform: 'sub2api', provisioningMode: 'managed',
      status: 'missing', ownGroupIds: ['1', '2'], ownGroupNames: ['乙', '甲'], pricingMappingEnabled: false,
    } })
    expect(dialog.text()).toContain('对接已变化')
    expect(dialog.text()).toContain('请重新核对这条记录')
    expect(dialog.text()).toContain('已解除')
    expect(state.calls.filter(call => call.path === '/api/my-sites/real-connections')).toHaveLength(2)
    expect(state.calls.some(call => call.path === '/api/my-sites/real-connections/check')).toBe(false)
    expect(state.calls.slice(state.calls.findLastIndex(call => call.path === '/api/my-sites/real-disconnect') + 1).map(call => call.path)).toEqual(expect.arrayContaining([
      '/api/group-rates', '/api/my-sites/real-connections/list-scoped', '/api/my-sites/real-connections/needs-attention',
    ]))
  })

  it('keeps completed checks, displays site issues, and excludes cancelled or unvisited sites', async () => {
    const rows = [connection('finished', 'first'), connection('failed', 'second'), connection('cancelled', 'third'), connection('unvisited', 'fourth')]
    state.lists = [rows, rows]
    state.check = async (site, signal) => {
      if (site === 'first') return checked({ finished: 'missing' }, [
        { detail: '有些账号的主站登录资料不可用', next: '请重新登录主站后核对', step: 'read_session', retryAllowed: true },
        { detail: '有些账号的状态没能确认', next: '请到主站手动查看', step: 'read_inventory', retryAllowed: true },
      ])
      if (site === 'second') throw new TypeError('network offline')
      return new Promise((_, reject) => signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true }))
    }
    const page = await mountPage()
    await namedButton(page, '清理失效对接').trigger('click')
    const dialog = openedCleanup(page)
    await namedButton(dialog, '开始核对').trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('正在核对第 3/4 个站点：网站 third')
    await namedButton(dialog, '取消').trigger('click')
    await flushPromises()
    expect(cleanupChecks().map(call => call.body.siteId)).toEqual(['first', 'second', 'third'])
    expect(dialog.text()).toContain('没能核对的站点（2 个）')
    expect(dialog.text()).toContain('有些账号的主站登录资料不可用')
    expect(dialog.text()).toContain('请重新登录主站后核对')
    expect(dialog.text()).toContain('有些账号的状态没能确认')
    expect(dialog.text()).toContain('请到主站手动查看')
    expect(dialog.text()).toContain('Hub与服务器的连接已中断')
    expect(dialog.text()).toContain('请点击刷新重试，并检查Hub后端检查记录')
    expect(dialog.text()).toContain('主站账号 finished')
    expect(dialog.text()).not.toMatch(/主站账号 (failed|cancelled|unvisited)/)
    expect(cleanupUnlinks()).toHaveLength(0)
  })

  it('opens the deep link in Sub2API without checking and omits cleanup in NewAPI', async () => {
    state.route.query = { cleanup: 'stale' }
    const page = await mountPage()
    expect(namedButton(openedCleanup(page), '开始核对').exists()).toBe(true)
    expect(state.calls.some(call => call.path === '/api/my-sites/real-connections')).toBe(false)
    expect(cleanupChecks()).toHaveLength(0)
    state.account.currentAccount.value = { id: 'other', platform: 'newapi' }
    await flushPromises()
    expect(page.find('[role="dialog"]').exists()).toBe(false)
    expect(page.findAll('button').some(button => button.text() === '清理失效对接')).toBe(false)
    expect(cleanupChecks()).toHaveLength(0)
  })

  it('ignores late check results when the workspace changes', async () => {
    state.lists = [[connection('old', 'first')]]
    const previousCheck = pendingResponse()
    state.check = async () => previousCheck.promise
    const page = await mountPage()
    await namedButton(page, '清理失效对接').trigger('click')
    await namedButton(openedCleanup(page), '开始核对').trigger('click')
    await flushPromises()
    expect(cleanupChecks()).toHaveLength(1)
    state.account.currentAccount.value = { id: 'another', platform: 'sub2api' }
    await flushPromises()
    await namedButton(page, '清理失效对接').trigger('click')
    previousCheck.finish(checked({ old: 'missing' }))
    await flushPromises()
    const dialog = openedCleanup(page)
    expect(namedButton(dialog, '开始核对').exists()).toBe(true)
    expect(dialog.text()).not.toContain('主站账号 old')
    expect(state.calls.filter(call => call.path === '/api/my-sites/real-connections')).toHaveLength(1)
    expect(cleanupUnlinks()).toHaveLength(0)
  })

  it('stops sending selected unlinks when the workspace changes during an unlink', async () => {
    const rows = [connection('first', 'a'), connection('second', 'a')]
    state.lists = [rows, rows]
    state.check = async () => checked({ first: 'missing', second: 'missing' })
    const currentUnlink = pendingResponse()
    state.unlink = async () => currentUnlink.promise
    const page = await mountPage()
    await namedButton(page, '清理失效对接').trigger('click')
    const dialog = openedCleanup(page)
    await namedButton(dialog, '开始核对').trigger('click')
    await flushPromises()
    await namedButton(dialog, '解除选中的 2 条').trigger('click')
    expect(cleanupUnlinks().map(call => call.body.connectionId)).toEqual(['first'])
    state.account.currentAccount.value = { id: 'another', platform: 'sub2api' }
    currentUnlink.finish(json({ ok: true }))
    await flushPromises()
    expect(page.find('[role="dialog"]').exists()).toBe(false)
    expect(cleanupUnlinks().map(call => call.body.connectionId)).toEqual(['first'])
  })
})

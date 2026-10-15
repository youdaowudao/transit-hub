// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { UpstreamSiteResponse, SyncStreamEvent } from '@/modules/admin/types/upstream'

const h = vi.hoisted(() => ({ list: vi.fn(), stream: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 60 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: h.list, streamSyncAllUpstreamSites: h.stream, syncAllUpstreamSites: vi.fn(), syncUpstreamSite: vi.fn(), createUpstreamSite: vi.fn(), updateUpstreamSite: vi.fn(), updateUpstreamSiteEnabled: vi.fn(), updateSiteSettings: vi.fn(), removeUpstreamSite: vi.fn() }))

const site = (id = 'a', amount: number | null = null, groupName = '原分组'): UpstreamSiteResponse => {
  const group = { id: '1', name: groupName, platform: 'openai', multiplier: 1, multiplierDisplay: '1', todayCost: amount, costMode: amount === null ? 'unknown' : 'exact', costSource: 'group_summary', costComplete: true, costObservedAt: amount === null ? null : '2031-02-03T04:00:00Z' }
  return { id, name: `站点${id}`, baseUrl: 'https://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', account: 'fixture', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, settings: { balanceThreshold: null }, lastSyncedAt: Date.parse('2031-02-03T03:59:00Z'), metrics: { balance: { value: 10, display: '10' }, todayConsume: { value: 0, display: '0' }, historyRecharge: { value: 10, display: '10' }, group, groups: [group], todayConsumeDate: '2031-02-03', todayConsumeTimezone: 'Asia/Singapore', todayConsumeStatus: 'ok' } }
}
let w: VueWrapper | undefined
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:05:00Z')); localStorage.clear(); vi.resetAllMocks(); h.stream.mockResolvedValue(undefined) })
afterEach(() => { w?.unmount(); w = undefined; vi.clearAllTimers(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })
const mountView = async () => { w = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' } } } }); await flushPromises(); return w }
const open = async (view: VueWrapper, index = 0) => { const buttons = view.findAll('button').filter(b => b.text().includes('可用分组')); expect(buttons[index]).toBeDefined(); await buttons[index]!.trigger('click'); await flushPromises() }
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }

// 主代理冻结关键组件业务期望，实现代理不得修改断言以适配实现。
describe('分组费用后台采样后的页面关键回归', () => {
  it('打开时未知，缓存随后有样本便自动显示金额，同一同步时间也接受', async () => {
    h.list.mockResolvedValueOnce([site()]).mockResolvedValue([site('a', 3.25)])
    const view = await mountView(); await open(view)
    await vi.advanceTimersByTimeAsync(5_000); await flushPromises()
    const dialog = view.get('[role="dialog"]')
    expect(dialog.text()).toContain('今日费用 3.25')
    expect(dialog.text()).not.toContain('今日费用暂无法读取')
    expect(h.list.mock.calls.length).toBeGreaterThan(1)
  })
  it('弹窗已打开时SSE替换列表，名称与费用切换到当前对象', async () => {
    let emit!: (event: SyncStreamEvent) => void
    const stream = deferred<void>()
    // Keep the reader active until its terminal event, as the real SSE API does.
    h.stream.mockImplementation((callback: typeof emit) => { emit = callback; return stream.promise })
    h.list.mockResolvedValue([site()])
    const view = await mountView(); await open(view)
    const fresh = site('a', 4.5, '新分组')
    h.list.mockResolvedValue([fresh])
    emit({ event: 'done', siteId: 'a', site: fresh }); await flushPromises()
    const dialog = view.get('[role="dialog"]')
    expect(dialog.text()).toContain('新分组')
    expect(dialog.text()).toContain('今日费用 4.50')
    expect(dialog.text()).not.toContain('原分组')
    expect(dialog.text()).not.toContain('今日费用暂无法读取')
    emit({ event: 'complete', siteId: '' }); stream.resolve(); await flushPromises()
  })
  it('A站补读迟到时已关闭并打开B，不得把A结果写入B弹窗', async () => {
    const late = deferred<UpstreamSiteResponse[]>()
    h.list.mockResolvedValueOnce([site(), site('b')]).mockImplementationOnce(() => late.promise).mockResolvedValue([site('b', 8)])
    const view = await mountView(); await open(view)
    const close = view.get('[role="dialog"]').findAll('button').find(b => b.text() === '关闭')!
    await close.trigger('click'); await open(view, 1)
    late.resolve([site('a', 99, '迟到A')]); await flushPromises()
    const dialog = view.get('[role="dialog"]')
    expect(dialog.text()).toContain('今日费用 8.00')
    expect(dialog.text()).not.toContain('99.00')
    expect(dialog.text()).not.toContain('迟到A')
  })
})

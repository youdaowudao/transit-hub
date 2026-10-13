// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 60 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
const harness = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: harness.list, streamSyncAllUpstreamSites: async () => undefined, syncAllUpstreamSites: vi.fn(), syncUpstreamSite: vi.fn(), createUpstreamSite: vi.fn(), updateUpstreamSite: vi.fn(), updateUpstreamSiteEnabled: vi.fn(), updateSiteSettings: vi.fn(), removeUpstreamSite: vi.fn() }))

let wrapper: VueWrapper | undefined
const metric = { value: 3, display: '3.00' }
const groupFixture = (cost: number | null, complete: boolean, unassigned: number | null) => ({ id: '1', name: '原消费分组', platform: 'openai', multiplier: 1, multiplierDisplay: '1', todayCost: cost, costMode: cost === null ? 'unknown' : 'live', costSource: 'group_summary', costReason: cost === null ? '读取分组费用时未收到完整数据；暂无可用费用，可同步这个站点重试' : '', costComplete: complete, siteReportedCost: cost === null ? null : 3, groupAttributedCost: cost, unattributedCost: unassigned })
const siteFixture = (group = groupFixture(null, false, null), detail = '') => ({ id: 'site', name: '验收-新加坡费用', baseUrl: 'http://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', account: '原账号', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, settings: { balanceThreshold: null }, lastSyncedAt: Date.parse('2031-02-03T04:00:00Z'), metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group, groups: [group], todayConsumeDate: '2031-02-03', todayConsumeAt: '2031-02-03T03:00:00Z', todayConsumeTimezone: 'Asia/Singapore', todayConsumeStatus: detail ? 'unreadable' : 'ok', todayConsumeErrorKey: detail ? 'admin.upstream.errors.invalidResponse' : '', todayConsumeErrorDetail: detail } })

beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:05:06Z')); localStorage.clear() })
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.clearAllTimers(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })
const mountView = async () => {
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  return wrapper
}
const openGroups = async (w: VueWrapper) => {
  const button = w.findAll('button').find(b => b.text().includes('可用分组'))
  expect(button).toBeDefined()
  await button!.trigger('click'); await flushPromises()
  return w.get('[role="dialog"]')
}

// 主代理固定真实Vue组件业务期望，实现代理不得为实现修改。
describe('新加坡费用关键页面回归', () => {
  it('未知费用不显示未完全归属，并说明日期、范围及重试入口', async () => {
    harness.list.mockResolvedValue([siteFixture()])
    const dialog = await openGroups(await mountView())
    expect(dialog.text()).not.toContain('未完全归属')
    expect(dialog.text()).toContain('费用按新加坡当天统计，按使用时的上游分组计算；这里只显示费用，不会修改主站。数据暂缺时可同步这个站点。')
  })
  it('真零可读，不同时显示未知或未完全归属', async () => {
    harness.list.mockResolvedValue([siteFixture(groupFixture(0, true, 0))])
    const dialog = await openGroups(await mountView())
    expect(dialog.text()).toContain('今日费用 0.00')
    expect(dialog.text()).not.toContain('未完全归属')
    expect(dialog.text()).not.toContain('今日费用暂无法读取')
  })
  it('微小未对应正消费不显示为0.00，站点说明只出现一次', async () => {
    const site = siteFixture(groupFixture(2, false, 0.0000000001))
    site.metrics.groups.push({ ...site.metrics.groups[0]!, id: '2', name: '第二分组' })
    harness.list.mockResolvedValue([site])
    const dialog = await openGroups(await mountView())
    expect(dialog.text()).toContain('未完全归属')
    expect(dialog.text()).toContain('少于0.01')
    expect(dialog.text().split('无法对应当前可用分组')).toHaveLength(2)
    expect(dialog.text()).toContain('这里不会分配这部分费用或修改主站')
  })
  it('费用读取详情经过规范化后在卡片和列表原位置保留', async () => {
    const detail = '读取本站新加坡当天总额时缺少 total_actual_cost，费用未更新；可同步这个站点重试，不会修改主站。'
    harness.list.mockResolvedValue([siteFixture(groupFixture(null, false, null), detail)])
    const w = await mountView()
    expect(w.get('div.group.bg-card').text()).toContain(detail)
    await w.get('button[aria-label="列表模式"]').trigger('click'); await flushPromises()
    expect(w.get('tbody tr').text()).toContain(detail)
  })
})

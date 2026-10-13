// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 60 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
const harness = vi.hoisted(() => ({ list: vi.fn(), sync: vi.fn(), stream: vi.fn() }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: harness.list, streamSyncAllUpstreamSites: harness.stream, syncAllUpstreamSites: vi.fn(), syncUpstreamSite: harness.sync, createUpstreamSite: vi.fn(), updateUpstreamSite: vi.fn(), updateUpstreamSiteEnabled: vi.fn(), updateSiteSettings: vi.fn(), removeUpstreamSite: vi.fn() }))

let wrapper: VueWrapper | undefined
const metric = { value: 3, display: '3.00' }
const groupFixture = (cost: number | null, complete: boolean, unassigned: number | null) => ({ id: '1', name: '原消费分组', platform: 'openai', multiplier: 1, multiplierDisplay: '1', todayCost: cost, costMode: cost === null ? 'unknown' : 'live', costSource: 'group_summary', costReason: cost === null ? '读取分组费用时未收到完整数据；暂无可用费用，可同步这个站点重试' : '', costComplete: complete, siteReportedCost: cost === null ? null : 3, groupAttributedCost: cost, unattributedCost: unassigned })
const siteFixture = (group = groupFixture(null, false, null), detail = '') => ({ id: 'site', name: '验收-新加坡费用', baseUrl: 'http://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', account: '原账号', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, settings: { balanceThreshold: null }, lastSyncedAt: Date.parse('2031-02-03T04:00:00Z'), metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group, groups: [group], todayConsumeDate: '2031-02-03', todayConsumeAt: '2031-02-03T03:00:00Z', todayConsumeTimezone: 'Asia/Singapore', todayConsumeStatus: detail ? 'unreadable' : 'ok', todayConsumeErrorKey: detail ? 'admin.upstream.errors.invalidResponse' : '', todayConsumeErrorDetail: detail } })


beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:05:06Z')); localStorage.clear(); harness.stream.mockReset().mockResolvedValue(undefined); harness.sync.mockReset() })
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.clearAllTimers(); vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })
const mountSites = async () => {
 wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { props: ['text'], template: '<span :data-tooltip="text"><slot /></span>' }, SiteSettingsModal: true } } })
 await flushPromises()
 return wrapper
}
describe('Singapore site proofs and transient save outcomes', () => {
 it('masks an old backend amount without timezone proof and retains its concrete prior refusal guidance', async () => {
  const site = siteFixture()
  Object.assign(site.metrics, { todayConsume: { value: 9, display: '9.00' }, todayConsumeTimezone: undefined, todayConsumeErrorKey: 'admin.upstream.errors.announcementAckRequired' })
  harness.list.mockResolvedValue([site])
  const w = await mountSites()
  const card = w.get('div.group.bg-card')
  expect(card.text()).toContain('旧费用尚未按新加坡日期重新读取，请同步这个站点；此操作不会修改主站。')
  expect(card.text()).toContain('上游要求先在网页上确认新公告')
  expect(card.text()).not.toContain('9.00')
  await w.get('button[aria-label="列表模式"]').trigger('click'); await flushPromises()
  expect(w.get('tbody tr').text()).not.toContain('9.00')
 })
 it('preserves the task save detail on a single-site failure while keeping the last certified amount', async () => {
  const detail = '本站资料已保存，当前运行数据未更新；刷新网页无法恢复，请联系维护人员检查存储连接后恢复服务，不会修改主站。'
  const error = Object.assign(new Error('admin.upstream.errors.request'), { failure: { stage: 'save', errorDetail: detail } })
  harness.list.mockResolvedValue([siteFixture()]); harness.sync.mockRejectedValue(error)
  const w = await mountSites()
  const card = w.get('div.group.bg-card')
  const sync = card.findAll('button').find(button => button.find('svg.lucide-refresh-cw').exists())
  expect(sync).toBeDefined(); await sync!.trigger('click'); await flushPromises()
  expect(card.text()).toContain(detail)
  expect(card.text()).toContain('3.00')
  await w.get('button[aria-label="列表模式"]').trigger('click'); await flushPromises()
  expect(w.get('tbody tr').text()).toContain(detail)
 })
 it('keeps an SSE save failure separate from the newer successful fee response', async () => {
  const detail = '数据库保存结果尚未确认，本轮没有更新费用；请维护人员核查实际结果后重试，不会修改主站。'
  harness.list.mockResolvedValue([siteFixture()])
  const w = await mountSites()
  harness.stream.mockImplementation(async (emit: (event: any) => void) => { emit({ event: 'error', siteId: 'site', errorKey: 'admin.upstream.errors.request', errorDetail: detail, site: siteFixture() }); emit({ event: 'complete' }) })
  const refresh = w.findAll('button').find(button => button.text().includes('刷新数据'))
  expect(refresh).toBeDefined(); await refresh!.trigger('click'); await flushPromises()
  expect(w.get('div.group.bg-card').text()).toContain(detail)
  expect(w.get('div.group.bg-card').text()).toContain('3.00')
 })
})

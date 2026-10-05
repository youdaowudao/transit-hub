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
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:05:06Z')); localStorage.clear() })
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers(); localStorage.clear(); document.body.innerHTML = '' })
describe('B cost readability in every upstream presentation', () => {
 it.each([
  ['ANNOUNCEMENT_ACK_REQUIRED', 'admin.upstream.errors.announcementAckRequired', 409, '上游要求先在网页上确认新公告，确认前无法读取数据。'],
  ['INSUFFICIENT_BALANCE', 'admin.upstream.errors.upstreamInsufficientBalance', 403, '上游账户余额不足，上游不允许读取。'],
  ['API_KEY_QUOTA_EXHAUSTED', 'admin.upstream.errors.upstreamKeyQuotaExhausted', 429, '该 Key 在上游的额度已用尽。'],
  ['API_KEY_EXPIRED', 'admin.upstream.errors.upstreamKeyExpired', 403, '该 Key 在上游已过期。'],
  ['UNRECOGNIZED', 'admin.upstream.errors.forbidden', 403, '上游拒绝（代码 UNRECOGNIZED，HTTP 403）'],
 ])('keeps connected and displays dated %s cost failure in cards and rows', async (code, errorKey, status, reason) => {
  harness.list.mockResolvedValue([{ id: 'site', name: '验收-成本不可读', baseUrl: 'http://fixture.invalid', platform: 'newapi', requestedPlatform: 'newapi', account: 'fixture', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null,
   metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [], todayConsumeDate: '2031-02-03', todayConsumeAt: '2031-02-03T03:00:00Z', todayConsumeStatus: 'unreadable', todayConsumeErrorKey: errorKey, todayConsumeUpstreamCode: code, todayConsumeHTTPStatus: status, todayConsumeFailedAt: '2031-02-03T04:00:00Z' }, settings: { balanceThreshold: null }, lastSyncedAt: Date.parse('2031-02-03T04:00:00Z') }])
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  for (const list of [false, true]) {
   if (list) { await wrapper.get('button[aria-label="列表模式"]').trigger('click'); await flushPromises() }
   const item = list ? wrapper.get('tbody tr') : wrapper.get('div.group.bg-card')
   expect(item.text()).toContain('已连接')
   expect(item.text()).not.toContain('2031-02-03')
   expect(item.text()).toContain(reason)
   expect(item.text()).toContain('今日成本不可读')
   expect(item.text()).not.toContain('连接失败')
   expect(item.text()).not.toContain('raw-fixture')
  }
 })
})

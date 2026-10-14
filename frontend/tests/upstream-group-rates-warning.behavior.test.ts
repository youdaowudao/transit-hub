// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { defineComponent, h as element } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import { useUpstreamSites } from '@/modules/admin/composables/useUpstreamSites'
import type { UpstreamSiteResponse, SyncStreamEvent } from '@/modules/admin/types/upstream'
const api = vi.hoisted(() => ({ listUpstreamSites: vi.fn(), syncUpstreamSite: vi.fn(), streamSyncAllUpstreamSites: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
vi.mock('@/modules/admin/api/upstream', () => ({ ...api, createUpstreamSite: vi.fn(), removeUpstreamSite: vi.fn(), syncAllUpstreamSites: vi.fn(), updateSiteSettings: vi.fn(), updateUpstreamSite: vi.fn(), updateUpstreamSiteEnabled: vi.fn() }))
const warning = { message: '测试网站的本地倍率保存结果尚未确认', detail: '数据库提交连接中断，不能确认哪些记录已更新或清理', next: '请核查数据库提交结果后再同步；本次未修改主站账号' }
const fixture = (groupRatesWarning = warning): UpstreamSiteResponse => ({ id: 'site-warning', name: '测试网站', baseUrl: 'https://warning.example', platform: 'sub2api', requestedPlatform: 'sub2api', account: 'test', rechargeRate: 2, enabled: true, remark: '', status: 'connected', errorKey: null, groupRatesWarning, metrics: { balance: { value: 10, display: '10' }, todayConsume: { value: 3, display: '3' }, historyRecharge: { value: 5, display: '5' }, group: { id: '1', name: '有效组', platform: 'openai', multiplier: 1, multiplierDisplay: '1' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: 1924992000000 })
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' } } } }); wrappers.push(w); await flushPromises(); return w }
beforeEach(() => { localStorage.clear(); vi.resetAllMocks(); api.listUpstreamSites.mockResolvedValue([fixture()]); api.syncUpstreamSite.mockImplementation(async () => { const site = fixture(); delete site.groupRatesWarning; return site }); api.streamSyncAllUpstreamSites.mockImplementation(async () => {}) })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); localStorage.clear(); vi.clearAllTimers(); document.body.innerHTML = '' })
describe('safe group rates warning stays separate from site and independent cost status', () => {
  it('shows exact unconfirmed storage outcome on cards and list while preserving connected status', async () => {
    const w = await open(); expect(w.get('[data-group-rates-warning]').text()).toContain(warning.detail); expect(w.text()).toContain('已连接'); expect(w.get('[data-group-rates-warning]').text()).not.toContain('全部回滚')
    const toggle = w.findAll('button').find(b => b.attributes('aria-label') === '列表模式' || b.attributes('title') === '列表模式')
    expect(toggle).toBeDefined(); await toggle!.trigger('click'); await flushPromises()
    expect(w.get('tbody [data-group-rates-warning]').text()).toContain(warning.next)
    expect(w.get('tbody tr').text()).toContain('已连接')
  })
  it('clears a previous warning after a clearly successful single sync without a warning field', async () => {
    const w = await open(); const card = w.findAll('div').find(d => d.classes().includes('group') && d.classes().includes('bg-card'))!
    await card.findAll('button')[0]!.trigger('click'); await flushPromises()
    expect(api.syncUpstreamSite).toHaveBeenCalledWith('site-warning'); expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
  })
  it('keeps the same optional warning for SSE terminal success and clears it on the next success', async () => {
    let service!: ReturnType<typeof useUpstreamSites>
    const w = mount(defineComponent({ setup() { service = useUpstreamSites(); return () => element('div') } })); wrappers.push(w); await flushPromises()
    api.streamSyncAllUpstreamSites.mockImplementationOnce(async (event: (e: SyncStreamEvent) => void) => { event({ event: 'done', siteId: 'site-warning', site: fixture() }); event({ event: 'complete', siteId: '' }) })
    await service.streamRefreshSites(); expect(service.sites.value[0]!.groupRatesWarning).toEqual(warning); expect(service.sites.value[0]!.status).toBe('connected')
    const clean = fixture(); delete clean.groupRatesWarning
    api.streamSyncAllUpstreamSites.mockImplementationOnce(async (event: (e: SyncStreamEvent) => void) => { event({ event: 'done', siteId: 'site-warning', site: clean }); event({ event: 'complete', siteId: '' }) })
    await service.streamRefreshSites(); expect(service.sites.value[0]!.groupRatesWarning).toBeUndefined()
  })
})

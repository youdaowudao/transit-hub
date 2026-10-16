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
const emptyGroups = '这个站点目前没有可显示的分组。'
const groupedFixture = () => {
  const site = fixture()
  site.metrics.groups = [site.metrics.group]
  return site
}
const cleanFixture = (groups = true) => {
  const site = groups ? groupedFixture() : fixture()
  delete site.groupRatesWarning
  return site
}
const button = (w: VueWrapper, text: string) => {
  const found = w.findAll('button').find(b => b.text() === text || b.attributes('aria-label') === text || b.attributes('title') === text)
  expect(found, `button ${text}`).toBeDefined()
  return found!
}
const switchToList = async (w: VueWrapper) => { await button(w, '列表模式').trigger('click'); await flushPromises() }
const openGroups = async (w: VueWrapper, list = false) => { await button(w, list ? '可用分组' : '查看可用分组').trigger('click'); await flushPromises() }
const syncCard = async (w: VueWrapper) => {
  const card = w.findAll('div').find(d => d.classes().includes('group') && d.classes().includes('bg-card'))!
  await card.findAll('button').find(b => b.find('svg.lucide-refresh-cw').exists())!.trigger('click')
  await flushPromises()
}
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' } } } }); wrappers.push(w); await flushPromises(); return w }
beforeEach(() => { localStorage.clear(); vi.resetAllMocks(); api.listUpstreamSites.mockResolvedValue([fixture()]); api.syncUpstreamSite.mockImplementation(async () => { const site = fixture(); delete site.groupRatesWarning; return site }); api.streamSyncAllUpstreamSites.mockImplementation(async () => {}) })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); localStorage.clear(); vi.clearAllTimers(); document.body.innerHTML = '' })
describe('upstream page no longer shows group rates warnings (20261016/4)', () => {
  it('U1 shows no warning on cards, list or modal while keeping status and groups', async () => {
    api.listUpstreamSites.mockResolvedValue([groupedFixture()])
    const w = await open()
    for (const list of [false, true]) {
      if (list) await switchToList(w)
      expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
      for (const text of [warning.message, warning.detail, warning.next]) expect(w.text()).not.toContain(text)
      expect(list ? w.get('tbody tr').text() : w.text()).toContain('已连接')
      await openGroups(w, list)
      const dialog = w.get('[role="dialog"]')
      expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
      for (const text of [warning.message, warning.detail, warning.next]) expect(dialog.text()).not.toContain(text)
      expect(dialog.text()).toContain('有效组')
      expect(dialog.text()).not.toContain(emptyGroups)
      await button(w, '关闭').trigger('click')
      await flushPromises()
    }
  })
  it('U2 hides the groups button for a site with a warning but no groups, in both modes', async () => {
    const withGroups = cleanFixture()
    withGroups.id = 'site-grouped'
    withGroups.name = '有分组站点'
    api.listUpstreamSites.mockResolvedValue([fixture(), withGroups])
    const w = await open()
    expect(w.findAll('button').filter(b => b.text() === '查看可用分组')).toHaveLength(1)
    await switchToList(w)
    expect(w.findAll('button').filter(b => b.text() === '可用分组')).toHaveLength(1)
    const row = w.findAll('tbody tr').find(r => r.text().includes('测试网站'))!
    expect(row.findAll('button').some(b => b.text() === '可用分组')).toBe(false)
  })
  it('U3 keeps the empty-groups note when a sync empties the groups while the modal is open', async () => {
    let current = cleanFixture()
    api.listUpstreamSites.mockImplementation(async () => [current])
    api.syncUpstreamSite.mockImplementation(async () => { current = cleanFixture(false); return current })
    const w = await open()
    await openGroups(w)
    expect(w.get('[role="dialog"]').text()).not.toContain(emptyGroups)
    await syncCard(w)
    expect(api.syncUpstreamSite).toHaveBeenCalledWith('site-warning')
    expect(w.get('[role="dialog"]').text()).toContain(emptyGroups)
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

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
const shortWarning = `${warning.message}。点“可用分组”查看原因和处理办法；不会修改主站。`
const modalExplanation = '下面显示本站当前保存的上游分组；分组资料和倍率记录的处理情况见上面的说明，查看不会修改主站。'
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
const expectModalWarning = (w: VueWrapper) => {
  expect(w.get('[data-groups-modal-warning]').findAll('p').map(p => p.text())).toEqual([warning.message, warning.detail, warning.next, modalExplanation])
}
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' } } } }); wrappers.push(w); await flushPromises(); return w }
beforeEach(() => { localStorage.clear(); vi.resetAllMocks(); api.listUpstreamSites.mockResolvedValue([fixture()]); api.syncUpstreamSite.mockImplementation(async () => { const site = fixture(); delete site.groupRatesWarning; return site }); api.streamSyncAllUpstreamSites.mockImplementation(async () => {}) })
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); localStorage.clear(); vi.clearAllTimers(); document.body.innerHTML = '' })
describe('safe group rates warning stays separate from site and independent cost status', () => {
  it('F1 shows only T1 on cards and list while preserving connected status', async () => {
    const w = await open()
    for (const list of [false, true]) {
      if (list) await switchToList(w)
      const alert = w.get('[data-group-rates-warning]')
      expect(alert.text()).toBe(shortWarning)
      expect(alert.text()).not.toContain(warning.detail)
      expect(alert.text()).not.toContain(warning.next)
      expect(list ? w.get('tbody tr').text() : w.text()).toContain('已连接')
    }
  })
  it('F2 shows the original three lines and T2, then removes both warnings after a clean sync', async () => {
    let current = groupedFixture()
    api.listUpstreamSites.mockImplementation(async () => [current])
    api.syncUpstreamSite.mockImplementation(async () => { current = cleanFixture(); return current })
    const w = await open()
    await openGroups(w)
    expectModalWarning(w)
    await syncCard(w)
    expect(api.syncUpstreamSite).toHaveBeenCalledWith('site-warning')
    expect(w.find('[role="dialog"]').exists()).toBe(true)
    expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
    expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
    expect(w.text()).not.toContain(emptyGroups)
  })
  it.each([false, true])('F3 opens warnings without groups and removes the button after clearing (list=%s)', async (list) => {
    const noWarning = cleanFixture(false)
    noWarning.id = 'site-empty'
    noWarning.name = '无分组无提示站点'
    let current = fixture()
    api.listUpstreamSites.mockImplementation(async (siteId?: string) => siteId ? [current] : [current, noWarning])
    const w = await open()
    if (list) await switchToList(w)
    const label = list ? '可用分组' : '查看可用分组'
    expect(w.findAll('button').filter(b => b.text() === label)).toHaveLength(1)
    await openGroups(w, list)
    expectModalWarning(w)
    expect(w.get('[role="dialog"]').text()).toContain(emptyGroups)
    api.syncUpstreamSite.mockImplementation(async () => { current = cleanFixture(false); return current })
    if (list) {
      const row = w.findAll('tbody tr').find(r => r.text().includes('测试网站'))!
      const sync = row.findAll('button').find(b => b.find('svg.lucide-refresh-cw').exists())!
      expect(sync).toBeDefined()
      await sync.trigger('click')
      await flushPromises()
      expect(api.syncUpstreamSite).toHaveBeenCalledWith('site-warning')
    } else await syncCard(w)
    expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
    expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
    expect(w.get('[role="dialog"]').text()).toContain(emptyGroups)
    await button(w, '关闭').trigger('click')
    await flushPromises()
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(w.findAll('button').filter(b => b.text() === label)).toHaveLength(0)
  })
  it('F4 has no warning blocks on cards, list or modal for a site with groups and no warning', async () => {
    api.listUpstreamSites.mockResolvedValue([cleanFixture()])
    const w = await open()
    expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
    await openGroups(w)
    expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
    expect(w.get('[role="dialog"]').text()).not.toContain(emptyGroups)
    await button(w, '关闭').trigger('click')
    await switchToList(w)
    expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
    await openGroups(w, true)
    expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
    expect(w.get('[role="dialog"]').text()).not.toContain(emptyGroups)
  })
  it('F5 clears both warnings when a sync returns an error site without a warning', async () => {
    let current = groupedFixture()
    api.listUpstreamSites.mockImplementation(async () => [current])
    const failed = cleanFixture()
    failed.status = 'error'
    failed.errorKey = 'admin.upstream.errors.rateLimited'
    api.syncUpstreamSite.mockImplementation(async () => { current = failed; return current })
    const w = await open()
    await openGroups(w)
    expectModalWarning(w)
    await syncCard(w)
    expect(w.find('[data-group-rates-warning]').exists()).toBe(false)
    expect(w.find('[data-groups-modal-warning]').exists()).toBe(false)
    expect(w.text()).toContain('同步失败')
    const card = w.findAll('div').find(d => d.classes().includes('group') && d.classes().includes('bg-card'))!
    expect(card.text()).toContain('异常')
    expect(card.text()).not.toContain('已连接')
  })
  it('clears a previous warning after a clearly successful single sync without a warning field', async () => {
    const w = await open(); const card = w.findAll('div').find(d => d.classes().includes('group') && d.classes().includes('bg-card'))!
    await card.findAll('button').find(b => b.find('svg.lucide-refresh-cw').exists())!.trigger('click'); await flushPromises()
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

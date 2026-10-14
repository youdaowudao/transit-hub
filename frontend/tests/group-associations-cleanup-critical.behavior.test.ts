// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Associations from '@/modules/admin/components/dashboard/GroupAssociationsPanel.vue'
const api = vi.hoisted(() => ({ getConnectionHealthAdminGroups: vi.fn(), getMySiteMappingOptions: vi.fn(), listRealConnections: vi.fn(), listAllGroupRates: vi.fn(), listUpstreamSites: vi.fn(), getNotificationChannelSettings: vi.fn() }))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), getConnectionHealthAdminGroups: api.getConnectionHealthAdminGroups }))
vi.mock('@/modules/admin/api/mySites', async original => ({ ...await original<typeof import('@/modules/admin/api/mySites')>(), getMySiteMappingOptions: api.getMySiteMappingOptions, listRealConnections: api.listRealConnections }))
vi.mock('@/modules/admin/api/groupRates', async original => ({ ...await original<typeof import('@/modules/admin/api/groupRates')>(), listAllGroupRates: api.listAllGroupRates }))
vi.mock('@/modules/admin/api/upstream', async original => ({ ...await original<typeof import('@/modules/admin/api/upstream')>(), listUpstreamSites: api.listUpstreamSites }))
vi.mock('@/modules/admin/api/settings', async original => ({ ...await original<typeof import('@/modules/admin/api/settings')>(), getNotificationChannelSettings: api.getNotificationChannelSettings }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-association-critical' }) }) } })
const stamp = '2031-01-01T00:00:00Z'
const conn = (id: string) => ({ id: 'connection-' + id, upstreamSiteId: 'site', upstreamGroupId: id, upstreamGroupName: '同名组', upstreamKeyId: 'key-' + id, adminAccountId: 'account-' + id, adminAccountName: '账号' + id, ownGroupIds: ['10'], ownGroupNames: ['自有组'], groupType: 'openai', status: 'active', createdAt: stamp })
const site = (id = 'B', multiplier = 9) => ({ id: 'site', name: '测试上游', platform: 'sub2api', status: 'connected', rechargeRate: 2, lastSyncedAt: Date.parse(stamp), metrics: { groups: [{ id, name: '同名组', multiplier, multiplierMode: 'fixed' }] } })
const rate = (id = 'B', multiplier = 9, updatedAt = stamp) => ({ siteId: 'site', siteName: '测试上游', groupId: id, groupName: '同名组', platform: 'sub2api', type: 'openai', deleted: false, mapped: true, upstreamMultiplier: multiplier, currentMultiplier: multiplier * 2, delta: 1, deltaPercent: 12.5, updatedAt })
const wrappers: VueWrapper[] = []
const open = async () => { const w = mount(Associations); wrappers.push(w); await flushPromises(); return w }
const prices = (w: VueWrapper) => { const r = w.findAll('tbody tr').find(x => x.text().includes('同名组'))!; expect(r).toBeDefined(); return r.findAll('td').slice(4, 7).map(x => x.text()) }
beforeEach(() => {
  localStorage.clear(); vi.resetAllMocks()
  api.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: '10', groupName: '自有组', siteName: '主站', multiplier: 30, platform: 'openai', status: 'active', isExclusive: false, subscriptionType: '' }], mappings: [{ ownGroup: '自有组', upstreamTargets: [{ siteId: 'site', groupName: '同名组' }] }] })
  api.listRealConnections.mockResolvedValue([conn('A')]); api.listAllGroupRates.mockResolvedValue([rate()]); api.listUpstreamSites.mockResolvedValue([site()]); api.getConnectionHealthAdminGroups.mockResolvedValue([]); api.getNotificationChannelSettings.mockResolvedValue({ telegram: [] })
})
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.restoreAllMocks(); document.body.innerHTML = ''; localStorage.clear() })
describe('cleanup critical association identity and current price', () => {
  it('cannot borrow a same-name new ID price, change, cost or profit', async () => {
    const w = await open(); const values = prices(w)
    expect(values[0]).not.toContain('9x'); expect(values[0]).not.toContain('+1.00x')
    expect(values[1]).toBe('—'); expect(values[2]).toBe('—')
  })
  it('mixed nonempty identities remain unknown in either connection order', async () => {
    api.listRealConnections.mockResolvedValue([conn('A'), conn('B')]); const first = await open(); const a = prices(first)
    expect(a[1]).toBe('—'); expect(a[2]).toBe('—'); expect(first.text()).toContain('不同分组的对接')
    api.listRealConnections.mockResolvedValue([conn('B'), conn('A')]); const second = await open()
    expect(prices(second)).toEqual(a)
  })
  it('uses the current same-ID group when a saved snapshot is behind and leaves change unknown', async () => {
    api.listRealConnections.mockResolvedValue([conn('B')]); api.listAllGroupRates.mockResolvedValue([rate('B', 2, '2030-12-31T00:00:00Z')])
    const w = await open(); const values = prices(w)
    expect(values[0]).toContain('9x'); expect(values[0]).not.toContain('+1.00x'); expect(values[1]).toBe('18x')
    expect(w.text()).toContain('倍率快照未跟上网站同步')
  })
  it('keeps unique manual name mappings and current groups useful without any rate history', async () => {
    api.listRealConnections.mockResolvedValue([]); api.listAllGroupRates.mockResolvedValue([])
    const values = prices(await open()); expect(values[0]).toContain('9x'); expect(values[1]).toBe('18x'); expect(values[2]).not.toBe('—')
  })
  it('cannot use an old rate when the current same-ID group is absent', async () => {
    api.listAllGroupRates.mockResolvedValue([rate('A', 2)]); api.listUpstreamSites.mockResolvedValue([{ ...site(), metrics: { groups: [] } }])
    const values = prices(await open()); expect(values[0]).not.toContain('2x'); expect(values[1]).toBe('—'); expect(values[2]).toBe('—')
  })
})

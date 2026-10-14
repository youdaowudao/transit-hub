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
const open = async () => { const w = mount(Associations, { global: { stubs: { Teleport: true } } }); wrappers.push(w); await flushPromises(); return w }
const prices = (w: VueWrapper) => { const r = w.findAll('tbody tr').find(x => x.text().includes('同名组'))!; expect(r).toBeDefined(); return r.findAll('td').slice(4, 7).map(x => x.text()) }
beforeEach(() => {
  localStorage.clear(); vi.resetAllMocks()
  api.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: '10', groupName: '自有组', siteName: '主站', multiplier: 30, platform: 'openai', status: 'active', isExclusive: false, subscriptionType: '' }], mappings: [{ ownGroup: '自有组', upstreamTargets: [{ siteId: 'site', groupName: '同名组' }] }] })
  api.listRealConnections.mockResolvedValue([conn('A')]); api.listAllGroupRates.mockResolvedValue([rate()]); api.listUpstreamSites.mockResolvedValue([site()]); api.getConnectionHealthAdminGroups.mockResolvedValue([]); api.getNotificationChannelSettings.mockResolvedValue({ telegram: [] })
})
afterEach(() => { for (const w of wrappers.splice(0)) w.unmount(); vi.restoreAllMocks(); document.body.innerHTML = ''; localStorage.clear() })
describe('association selection keeps same identity across every price display', () => {
  it('selected profit details cannot borrow same-name replacement prices', async () => {
    const w = await open()
    await w.get('button[aria-label="打开利润预算计算器"]').trigger('click'); await flushPromises()
    const dialog = w.get('[role="dialog"]')
    expect(dialog.text()).toContain('同名组')
    expect(dialog.text()).not.toContain('18x')
    expect(dialog.text()).not.toContain('40.00%')
  })
  it('one known ID with a uniquely matching legacy blank ID stays usable regardless of order', async () => {
    api.listRealConnections.mockResolvedValue([conn('B'), { ...conn('legacy'), upstreamGroupId: '' }])
    const first = prices(await open()); expect(first[0]).toContain('9x'); expect(first[1]).toBe('18x')
    api.listRealConnections.mockResolvedValue([{ ...conn('legacy'), upstreamGroupId: '' }, conn('B')])
    expect(prices(await open())).toEqual(first)
  })
  it('ambiguous current same-name groups cannot authorize legacy blank-ID or manual prices', async () => {
    api.listRealConnections.mockResolvedValue([{ ...conn('legacy'), upstreamGroupId: '' }])
    api.listUpstreamSites.mockResolvedValue([{ ...site(), metrics: { groups: [...site().metrics.groups, { id: 'C', name: '同名组', multiplier: 5, multiplierMode: 'fixed' }] } }])
    const values = prices(await open()); expect(values[1]).toBe('—'); expect(values[2]).toBe('—')
  })
  it('same-ID rename preserves the correct current source and its identity-matched rate', async () => {
    api.listRealConnections.mockResolvedValue([conn('B')])
    api.listUpstreamSites.mockResolvedValue([{ ...site(), metrics: { groups: [{ ...site().metrics.groups[0], name: '改名后组' }] } }])
    api.listAllGroupRates.mockResolvedValue([{ ...rate(), groupName: '改名后组' }])
    const values = prices(await open()); expect(values[0]).toContain('9x'); expect(values[1]).toBe('18x')
  })
  it('a confirmed auto group cannot borrow its last fixed rate', async () => {
    api.listRealConnections.mockResolvedValue([conn('B')])
    api.listUpstreamSites.mockResolvedValue([{ ...site(), metrics: { groups: [{ id: 'B', name: '同名组', multiplier: null, multiplierMode: 'auto' }] } }])
    const values = prices(await open()); expect(values[1]).toBe('—'); expect(values[2]).toBe('—'); expect(values[0]).not.toContain('9x')
  })
})

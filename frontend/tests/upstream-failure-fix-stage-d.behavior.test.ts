// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

let wrapper: VueWrapper | undefined
let connections: number
let mappings: number
let deleteStatus: number
const metric = { value: 1, display: '1.00' }
const site = { id: 'fixture-site', name: '验收-删除守卫', baseUrl: 'http://fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', account: 'fixture-account', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null }
const openDelete = async (mode: 'card' | 'list') => {
 wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
 await flushPromises()
 if (mode === 'list') await wrapper.get('button[aria-label="列表模式"]').trigger('click')
 const entry = wrapper.get(mode === 'card' ? 'div.group.bg-card' : 'tbody tr')
 await entry.findAll('button').at(-1)!.trigger('click')
 const confirm = wrapper.get('[role="alertdialog"]').findAll('button').find(button => button.text().includes('确认删除'))
 if (!confirm) throw new Error('delete confirmation entry missing')
 await confirm.trigger('click'); await flushPromises()
 return wrapper
}
beforeEach(() => {
 connections = 2; mappings = 1; deleteStatus = 409
 vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
  if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
  if (options.method === 'DELETE') return new Response(JSON.stringify(deleteStatus === 200 ? { success: true } : { message: 'admin.upstream.errors.siteInUse', failure: { connections, mappings } }), { status: deleteStatus })
  return new Response(JSON.stringify([site]), { status: 200 })
 }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })

describe('upstream stage D deletion guard', () => {
 it.each(['card', 'list'] as const)('keeps the %s entry and confirmation with both reference counts and unlink instructions', async mode => {
  const view = await openDelete(mode)
  const dialog = view.get('[role="alertdialog"]')
  expect(dialog.text()).toContain('2 条对接')
  expect(dialog.text()).toContain('1 条映射')
  expect(dialog.text()).toContain('分组倍率')
  expect(dialog.text()).toContain('调价映射')
  expect(dialog.text()).not.toContain('上游接口请求失败')
  expect(view.get(mode === 'card' ? 'div.group.bg-card' : 'tbody tr').text()).toContain('验收-删除守卫')
 })
 it.each([[0, 1], [2, 0], [0, 0]])('renders zero counts without treating %s/%s as absent', async (connectionCount, mappingCount) => {
  connections = connectionCount; mappings = mappingCount
  const view = await openDelete('card')
  expect(view.get('[role="alertdialog"]').text()).toContain(`${connections} 条对接`)
  expect(view.get('[role="alertdialog"]').text()).toContain(`${mappings} 条映射`)
  expect(view.get('div.group.bg-card').text()).toContain('验收-删除守卫')
 })
 it.each(['card', 'list'] as const)('preserves unreferenced successful removal in %s mode', async mode => {
  deleteStatus = 200
  const view = await openDelete(mode)
  expect(view.find('[role="alertdialog"]').exists()).toBe(false)
  expect(view.findAll(mode === 'card' ? 'div.group.bg-card' : 'tbody tr').some(entry => entry.text().includes('验收-删除守卫'))).toBe(false)
  expect(view.text()).not.toContain('验收-删除守卫')
 })
})

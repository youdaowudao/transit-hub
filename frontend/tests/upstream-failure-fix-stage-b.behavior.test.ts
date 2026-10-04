// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

let wrapper: VueWrapper | undefined
let authMode: 'password' | 'token' | 'user_key' | undefined
let platform: 'newapi' | 'sub2api'
const metric = { value: 1, display: '1.00' }
const site = () => ({ id: 'fixture-site', name: '验收-编辑方式', baseUrl: 'http://fixture.invalid', platform, requestedPlatform: platform, authMode, account: authMode === 'user_key' ? '42' : 'fixture-account', rechargeRate: 1, enabled: true, remark: '原备注', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
beforeEach(() => {
 vi.stubGlobal('fetch', vi.fn(async (url: string) => {
  if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
  return new Response(JSON.stringify([site()]), { status: 200 })
 }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })

describe('upstream failure fix stage B', () => {
 it.each([
  ['password', 'newapi'], ['password', 'sub2api'], ['token', 'sub2api'], ['user_key', 'newapi'], [undefined, 'sub2api'], [undefined, 'newapi'],
 ] as const)('preselects the safe saved %s mode on %s without revealing credentials', async (mode, resolvedPlatform) => {
  authMode = mode; platform = resolvedPlatform
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  await wrapper.get('div.group.bg-card').findAll('button')[2].trigger('click')
  await flushPromises()
  const selected = wrapper.get('input[type="radio"]:checked').element as HTMLInputElement
  expect(selected.value).toBe(mode ?? 'password')
  expect((wrapper.get('#upstream-site-name').element as HTMLInputElement).value).toBe('验收-编辑方式')
  if (mode === 'user_key') {
   expect((wrapper.get('#upstream-site-user-id').element as HTMLInputElement).value).toBe('42')
   expect(wrapper.find('#upstream-site-password').exists()).toBe(false)
  } else {
   expect(wrapper.find('#upstream-site-user-id').exists()).toBe(false)
  }
  for (const id of ['#upstream-site-password', '#upstream-site-access-token', '#upstream-site-refresh-token']) {
   const input = wrapper.find(id)
   if (input.exists()) expect((input.element as HTMLInputElement).value).toBe('')
  }
  if (mode === 'token') {
   expect(wrapper.find('#upstream-site-password').exists()).toBe(false)
   expect(wrapper.find('#upstream-site-access-token').exists()).toBe(true)
   expect(wrapper.find('#upstream-site-refresh-token').exists()).toBe(true)
  }
 })
})

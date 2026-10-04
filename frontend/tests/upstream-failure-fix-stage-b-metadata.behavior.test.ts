// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

let wrapper: VueWrapper | undefined
let authMode: 'password' | 'token' | 'user_key'
let savedForm: Record<string, unknown> | undefined
const metric = { value: 1, display: '1.00' }
const site = () => ({ id: 'fixture-site', name: '验收-原名称', baseUrl: 'http://fixture.invalid', platform: authMode === 'token' ? 'sub2api' : 'newapi', requestedPlatform: authMode === 'token' ? 'sub2api' : 'newapi', authMode, account: authMode === 'user_key' ? '42' : 'fixture-account', rechargeRate: 1, enabled: true, remark: '原备注', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
beforeEach(() => {
 savedForm = undefined
 vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
  if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
  if (options.method === 'PUT') {
   savedForm = JSON.parse(String(options.body)) as Record<string, unknown>
   return new Response(JSON.stringify({ ...site(), name: savedForm.name, remark: savedForm.remark }), { status: 200 })
  }
  return new Response(JSON.stringify([site()]), { status: 200 })
 }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })

describe('upstream stage B metadata-only edit', () => {
 it.each(['password', 'token', 'user_key'] as const)('allows %s name and remark updates with blank credential fields', async mode => {
  authMode = mode
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  await wrapper.get('div.group.bg-card').findAll('button')[2].trigger('click')
  await flushPromises()
  expect((wrapper.get('input[type="radio"]:checked').element as HTMLInputElement).value).toBe(mode)
  const secretInputs = wrapper.findAll('#upstream-site-password, #upstream-site-user-key, #upstream-site-access-token, #upstream-site-refresh-token')
  expect(secretInputs.every(input => (input.element as HTMLInputElement).value === '')).toBe(true)
  await wrapper.get('#upstream-site-name').setValue('验收-改名')
  await wrapper.get('#upstream-site-remark').setValue('新备注')
  const form = wrapper.get('[role="dialog"] form').element as HTMLFormElement
  expect(form.checkValidity()).toBe(true)
  form.requestSubmit()
  await flushPromises()
  expect(savedForm?.name).toBe('验收-改名')
  expect(savedForm?.remark).toBe('新备注')
  expect(['password', 'accessToken', 'refreshToken'].every(field => savedForm?.[field] === '')).toBe(true)
  expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  expect(wrapper.get('div.group.bg-card').text()).toContain('验收-改名')
  expect(wrapper.get('div.group.bg-card').text()).toContain('已连接')
  expect(wrapper.get('div.group.bg-card').text()).not.toContain('登录失败')
 })
 it('still prevents a new user_key site from submitting an empty Key', async () => {
  authMode = 'user_key'
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  const addButton = wrapper.findAll('button').find(button => button.text().includes('新增站点'))
  if (!addButton) throw new Error('add entry missing')
  await addButton.trigger('click')
  await wrapper.get('#upstream-site-name').setValue('验收-新建空Key')
  await wrapper.get('#upstream-site-url').setValue('http://fixture.invalid')
  await wrapper.get('#upstream-site-platform').setValue('newapi')
  await wrapper.get('input[type="radio"][value="user_key"]').setValue(true)
  await wrapper.get('#upstream-site-user-id').setValue('42')
  const form = wrapper.get('[role="dialog"] form').element as HTMLFormElement
  expect(form.checkValidity()).toBe(false)
  form.requestSubmit()
  await flushPromises()
  expect(savedForm).toBeUndefined()
  expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
 })

})

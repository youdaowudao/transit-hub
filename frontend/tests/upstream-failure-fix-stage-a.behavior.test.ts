// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import { createUpstreamSite } from '@/modules/admin/api/upstream'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

let wrapper: VueWrapper | undefined
let payload: Record<string, unknown>
let status: number
let siteError: string | null
const metric = { value: 1, display: '1.00' }
const site = () => ({ id: 'fixture-site', name: '验收-原站点', baseUrl: 'http://fixture.invalid', platform: 'newapi', requestedPlatform: 'newapi', account: 'fixture-account', rechargeRate: 1, enabled: true, remark: '', status: siteError ? 'error' : 'connected', errorKey: siteError, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
const mountView = async () => {
 wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
 await flushPromises()
 return wrapper
}
const openAdd = async () => {
 const view = await mountView()
 const button = view.findAll('button').find(item => item.text().includes('新增站点'))
 if (!button) throw new Error('add entry missing')
 await button.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-添加')
 await view.get('#upstream-site-url').setValue('http://fixture.invalid')
 await view.get('#upstream-site-account').setValue('fixture-account')
 await view.get('#upstream-site-password').setValue('fixture-password')
 return view
}
beforeEach(() => {
 status = 422
 siteError = null
 payload = { message: 'admin.upstream.errors.loginRejected', failure: { errorKey: 'admin.upstream.errors.loginRejected', stage: 'login', httpStatus: 401, upstreamMessage: '用户名或密码错误' } }
 vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
  if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
  if (!options.method) return new Response(JSON.stringify([site()]), { status: 200 })
  return new Response(JSON.stringify(payload), { status })
 }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })
describe('upstream failure fix stage A', () => {
 it.each(['card', 'list'] as const)('local missing-site editing in %s mode preserves the form and avoids upstream 404 instructions', async mode => {
  status = 404; payload = { message: 'admin.upstream.errors.notFound' }
  const view = await mountView()
  if (mode === 'list') await view.get('button[aria-label="列表模式"]').trigger('click')
  const entry = view.get(mode === 'card' ? 'div.group.bg-card' : 'tbody tr')
  await entry.findAll('button')[2].trigger('click')
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('站点不存在')
  expect(alert.text()).not.toContain('上游接口不存在')
  expect(alert.text()).not.toContain('检查站点地址和平台类型')
  expect(view.find('[role="dialog"]').exists()).toBe(true)
  expect(entry.text()).toContain('验收-原站点')
 })
 it.each(['admin.upstream.errors.rateLimited', 'admin.upstream.errors.upstreamServerError'])('refresh failures on cards and rows retain temporary %s guidance', async key => {
  siteError = key
  const view = await mountView()
  for (const mode of ['card', 'list']) {
   if (mode === 'list') await view.get('button[aria-label="列表模式"]').trigger('click')
   const text = view.get(mode === 'card' ? 'div.group.bg-card' : 'tbody tr').text()
   expect(text).toContain('请稍后再试')
   expect(text).not.toContain('刷新令牌已失效')
   expect(text).not.toContain('重新获取后填写')
  }
 })
 it('R5 keeps the failed add dialog open with reason, step, HTTP status and safe upstream hint', async () => {
  const view = await openAdd()
  await view.get('[role="dialog"] form').trigger('submit')
  await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('上游拒绝登录')
  expect(alert.text()).toContain('登录')
  expect(alert.text()).toContain('401')
  expect(alert.text()).toContain('用户名或密码错误')
  expect(alert.text()).not.toContain('上游接口请求失败')
  expect(alert.text()).not.toContain('已保存')
  expect(view.findAll('#upstream-site-name')).toHaveLength(1)
  expect(view.findAll('div.group.bg-card')).toHaveLength(1)
 })
 it('R3 displays both automatic platform attempts without dropping the first reason', async () => {
  payload = { message: 'admin.upstream.errors.autoDetectFailed', failure: { errorKey: 'admin.upstream.errors.autoDetectFailed', attempts: [ { platform: 'newapi', errorKey: 'admin.upstream.errors.loginRejected', stage: 'login', httpStatus: 200 }, { platform: 'sub2api', errorKey: 'admin.upstream.errors.notFound', stage: 'login', httpStatus: 404 } ] } }
  const view = await openAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  for (const value of ['自动识别平台失败', 'NewAPI', 'Sub2API', '上游拒绝登录', '接口不存在', '200', '404']) expect(alert.text()).toContain(value)
 })
 it('400 validation errors name the actual request fields', async () => {
  status = 400; payload = { message: 'admin.upstream.errors.invalidFields', failure: { fields: ['siteUrl', 'userId'] } }
  const view = await openAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('站点 URL')
  expect(alert.text()).toContain('用户 ID')
  expect(alert.text()).not.toContain('上游接口请求失败')
 })
 it('R4 keeps the original connected card and form values on rejected editing', async () => {
  const view = await mountView()
  const card = view.get('div.group.bg-card')
  await card.findAll('button')[2].trigger('click')
  await view.get('#upstream-site-name').setValue('验收-修改失败')
  await view.get('#upstream-site-password').setValue('fixture-new')
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(view.find('[role="dialog"]').exists()).toBe(true)
  expect(view.get('[role="dialog"] [role="alert"]').text()).toContain('上游拒绝登录')
  expect(view.get('div.group.bg-card').text()).toContain('验收-原站点')
  expect(view.get('div.group.bg-card').text()).not.toContain('验收-修改失败')
  expect(view.get('div.group.bg-card').text()).toContain('已连接')
 })
 it.each([['admin.upstream.errors.accessTokenRejected', '访问令牌无效'], ['admin.upstream.errors.auth', '登录失败，请检查账号或密码。'], ['admin.upstream.errors.invalidResponse', '上游返回内容无法解析。']])('cards and rows preserve %s wording', async (key, message) => {
  siteError = key
  const view = await mountView()
  expect(view.get('div.group.bg-card').text()).toContain(message)
  await view.get('button[aria-label="列表模式"]').trigger('click')
  expect(view.get('tbody tr').text()).toContain(message)
 })
 it('a successful add keeps its 201 behavior and closes the dialog', async () => {
  status = 201; payload = { ...site(), id: 'fixture-new-site', name: '验收-添加' }
  const view = await openAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(view.find('[role="dialog"]').exists()).toBe(false)
  expect(view.findAll('div.group.bg-card')).toHaveLength(2)
  expect(view.text()).toContain('验收-添加')
 })
 it('a successful edit keeps its 200 behavior and replaces the original card', async () => {
  status = 200; payload = { ...site(), name: '验收-修改成功' }
  const view = await mountView()
  await view.get('div.group.bg-card').findAll('button')[2].trigger('click')
  await view.get('#upstream-site-name').setValue('验收-修改成功')
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(view.find('[role="dialog"]').exists()).toBe(false)
  expect(view.findAll('div.group.bg-card')).toHaveLength(1)
  expect(view.get('div.group.bg-card').text()).toContain('验收-修改成功')
  expect(view.get('div.group.bg-card').text()).not.toContain('验收-原站点')
 })
 it('invalid JSON adds the API explanation only in the failed form', async () => {
  siteError = 'admin.upstream.errors.invalidResponse'
  payload = { message: 'admin.upstream.errors.invalidResponse', failure: { errorKey: 'admin.upstream.errors.invalidResponse', stage: 'login', httpStatus: 200 } }
  const view = await openAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(view.get('[role="dialog"] [role="alert"]').text()).toContain('上游返回的不是有效 JSON，地址可能不是 API 地址。')
  expect(view.get('div.group.bg-card').text()).toContain('上游返回内容无法解析。')
  expect(view.get('div.group.bg-card').text()).not.toContain('地址可能不是 API 地址')
 })
 it('API preserves the structured error and falls back safely for malformed non-JSON errors', async () => {
  let rejection: unknown
  try { await createUpstreamSite({} as never) } catch (error) { rejection = error }
  expect(rejection).toMatchObject({ message: 'admin.upstream.errors.loginRejected', key: 'admin.upstream.errors.loginRejected', failure: payload.failure })
  vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>fixture-body-private</html>', { status: 502 })))
  await expect(createUpstreamSite({} as never)).rejects.toThrow('admin.upstream.errors.request')
 })
})

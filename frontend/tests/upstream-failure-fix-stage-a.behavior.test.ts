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
 await view.get('input[type="radio"][value="password"]').setValue(true)
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
 const openTokenAdd = async () => {
  const view = await mountView()
  await view.findAll('button').find(item => item.text().includes('新增站点'))!.trigger('click')
  await view.get('#upstream-site-name').setValue('验收-令牌')
  await view.get('#upstream-site-url').setValue('https://api.fixture.invalid')
  await view.get('#upstream-site-refresh-token').setValue('synthetic-refresh')
  return view
 }
 const tokenUsed = '填写的 Refresh Token 可能已被使用，重试前请到上游重新获取。'
 const tokenNotUsed = '这个网站地址上没有登录接口，填写的 Refresh Token 没有被使用。请把网站地址改成你登录上游网页的地址（不是 API 地址），改正后可以用同一个 Refresh Token 重试。'
 it('refresh 404 says the token was not used and points to the website address', async () => {
  payload = { message: 'admin.upstream.errors.notFound', failure: { errorKey: 'admin.upstream.errors.notFound', stage: 'refresh', httpStatus: 404, upstreamMessage: '上游确认该页面不存在' } }
  const view = await openTokenAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]').text()
  expect(alert).toContain('刷新失败（404）：网站地址上没有登录接口。')
  expect(alert).toContain('上游确认该页面不存在')
  expect(alert).toContain('失败步骤：刷新令牌')
  expect(alert).toContain(tokenNotUsed)
  expect(alert).not.toContain(tokenUsed)
  expect((view.get('#upstream-site-refresh-token').element as HTMLInputElement).value).toBe('synthetic-refresh')
 })
 it.each([
  ['refresh', 401, 'admin.upstream.errors.refreshTokenRejected'],
  ['refresh', 0, 'admin.upstream.errors.networkTimeout'],
  ['metrics', 404, 'admin.upstream.errors.notFound'],
 ])('keeps the uncertainty in details without claiming the token was used for %s %s', async (stage, httpStatus, errorKey) => {
  payload = { message: errorKey, failure: { errorKey, stage, httpStatus } }
  const view = await openTokenAdd()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]').text()
  expect(alert).toContain('令牌是否仍可用尚未确认；只保存原填写内容，不会换新令牌。')
  expect(alert).not.toContain(tokenUsed)
  expect(alert).not.toContain(tokenNotUsed)
 })
 it.each([
  '上游登录响应没有返回登录会话，本站无法继续读取账号。请联系上游维护人员核查登录接口的会话返回后重试；本次没有保存，不会修改主站。',
  '上游登录响应带有会话，但没有合法的用户编号，无法确认登录账号。请联系上游维护人员核查登录接口返回的账号资料后重试；本次没有保存，不会修改主站。',
  '上游明确要求两步验证，当前连接窗口暂不支持填写验证码。请在上游账号安全设置中核查登录验证方式后重试；本次没有保存，不会修改主站。',
  '上游没有清楚说明是否需要额外验证，本站无法确认登录完成。请联系上游维护人员核查登录接口的额外验证状态后重试；本次没有保存，不会修改主站。',
 ])('password adding displays the known login cause without an invented verification hint: %s', async reason => {
  payload = { message: 'admin.upstream.errors.loginIncomplete', failure: { errorKey: 'admin.upstream.errors.loginIncomplete', stage: 'login', httpStatus: 200, upstreamMessage: reason } }
  const view = await openAdd()
  await view.get('#upstream-site-platform').setValue('newapi')
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('上游返回的登录资料不完整，当前无法确认登录结果。请查看具体原因后重试。')
  expect(alert.text()).toContain(reason)
  expect(alert.text()).toContain('失败步骤：登录')
  expect(alert.text()).toContain('HTTP 状态：200')
  expect(alert.text()).not.toContain('可能开启了两步验证')
  expect(alert.text()).not.toContain('请改用 Key 或 Token')
  expect(alert.text()).not.toContain('已保存')
  expect(view.get('#upstream-site-name').element).toHaveProperty('value', '验收-添加')
  expect(view.get('#upstream-site-account').element).toHaveProperty('value', 'fixture-account')
  expect(view.get('#upstream-site-password').element).toHaveProperty('value', 'fixture-password')
  expect(view.findAll('div.group.bg-card')).toHaveLength(1)
  expect(view.get('div.group.bg-card').text()).toContain('验收-原站点')
 })
 it.each(['newapi', 'sub2api'])('uses a neutral shared fallback when %s does not return a detailed login cause', async platform => {
  payload = { message: 'admin.upstream.errors.loginIncomplete', failure: { errorKey: 'admin.upstream.errors.loginIncomplete', stage: 'login', httpStatus: 200 } }
  const view = await openAdd()
  await view.get('#upstream-site-platform').setValue(platform)
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('上游返回的登录资料不完整，当前无法确认登录结果。请查看具体原因后重试。')
  expect(alert.text()).toContain('失败步骤：登录')
  expect(alert.text()).toContain('HTTP 状态：200')
  for (const absent of ['两步验证', '改用 Key', '改用 Token', '没有返回登录会话', '没有合法的用户编号', '已保存']) expect(alert.text()).not.toContain(absent)
  expect(view.find('[role="dialog"]').exists()).toBe(true)
 })
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
  expect(alert.text()).toContain('网站地址')
  expect(alert.text()).not.toContain('API 地址')
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
  expect(view.get('[role="dialog"] [role="alert"]').text()).toContain('上游返回的不是有效 JSON，网站地址可能填错了：请填你登录上游网页的地址，不要填 API 地址。')
  expect(view.get('div.group.bg-card').text()).toContain('上游返回内容无法解析。')
  expect(view.get('div.group.bg-card').text()).not.toContain('网站地址可能填错了')
 })
 it('API preserves the structured error and falls back safely for malformed non-JSON errors', async () => {
  let rejection: unknown
  try { await createUpstreamSite({} as never) } catch (error) { rejection = error }
  expect(rejection).toMatchObject({ message: 'admin.upstream.errors.loginRejected', key: 'admin.upstream.errors.loginRejected', failure: payload.failure })
  vi.stubGlobal('fetch', vi.fn(async () => new Response('<html>fixture-body-private</html>', { status: 502 })))
  await expect(createUpstreamSite({} as never)).rejects.toThrow('admin.upstream.errors.request')
 })
})


// Main-agent critical expectations for the approved concise failure/save flow.
describe('force-save critical behavior', () => {
 const openToken = async () => {
  const view = await mountView()
  await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
  await view.get('#upstream-site-name').setValue('验收-强存')
  await view.get('#upstream-site-url').setValue('https://fixture.invalid')
  await view.get('#upstream-site-refresh-token').setValue('fixture-refresh-secret')
  return view
 }
 const submitToken = async (view: VueWrapper) => {
  await view.get('[role="dialog"] form').trigger('submit')
  await flushPromises()
 }
 it.each(['上游明确拒绝测试来源', ''])('shows 401 and the direct reason with technical details initially folded: %s', async reason => {
  payload = { message:'admin.upstream.errors.refreshTokenRejected', failure:{stage:'refresh',httpStatus:401,upstreamMessage:reason} }
  const view = await openToken(); await submitToken(view)
  const alert = view.get('[role="dialog"] [role="alert"]')
  const primary = alert.findAll('p')[0]!.text()
  expect(primary).toContain('401')
  expect(primary).toContain(reason || '上游未提供具体原因')
  expect(primary).not.toMatch(/已失效|已被使用|余额不足/)
  const details = alert.get('details')
  expect((details.element as HTMLDetailsElement).open).toBe(false)
  const requests = vi.mocked(fetch).mock.calls.length
  ;(details.element as HTMLDetailsElement).open = true
  await details.trigger('toggle')
  expect(vi.mocked(fetch).mock.calls.length).toBe(requests)
  expect(alert.findAll('button').some(button => button.text() === '强行保存资料')).toBe(true)
 })
 it('explicitly saves only after failure, then displays unknown balance and pending verification', async () => {
  payload = { message:'admin.upstream.errors.refreshTokenRejected', failure:{stage:'refresh',httpStatus:401,upstreamMessage:'测试拒绝'} }
  const view = await openToken(); await submitToken(view)
  const button = view.get('[role="dialog"] [role="alert"]').findAll('button').find(item => item.text() === '强行保存资料')
  expect(button).toBeDefined()
  status = 201
  const unknown = { value:null, display:'-' }
  payload = { ...site(),id:'fixture-forced',name:'验收-强存',platform:'sub2api',requestedPlatform:'auto',authMode:'token',enabled:false,status:'error',errorKey:'admin.upstream.errors.savedWithoutVerification',metrics:{balance:unknown,todayConsume:unknown,historyRecharge:unknown,group:site().metrics.group,groups:[]} }
  await button!.trigger('click'); await flushPromises()
  const calls = vi.mocked(fetch).mock.calls.filter(([, options]) => options?.method === 'POST')
  expect(JSON.parse(String(calls[0]![1]!.body)).forceSave).toBeUndefined()
  expect(JSON.parse(String(calls[1]![1]!.body))).toMatchObject({forceSave:true, refreshToken:'fixture-refresh-secret'})
  expect(view.find('[role="dialog"]').exists()).toBe(false)
  const card = view.findAll('div.group.bg-card').find(item => item.text().includes('验收-强存'))!
  expect(card.text()).toContain('登录待验证')
  expect(card.text()).not.toContain('已连接')
  expect(localStorage.getItem('refreshToken')).toBeNull()
 })
 it('removes the old force-save option when any input is changed', async () => {
  payload = { message:'admin.upstream.errors.refreshTokenRejected', failure:{stage:'refresh',httpStatus:401} }
  const view = await openToken(); await submitToken(view)
  expect(view.get('[role="dialog"] [role="alert"]').text()).toContain('强行保存资料')
  await view.get('#upstream-site-name').setValue('修改后的名字')
  expect(view.find('[role="dialog"] [role="alert"]').exists()).toBe(false)
 })
})


describe('force-save additional behavior', () => {
 const fillToken = async (view: VueWrapper) => {
  await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
  await view.get('#upstream-site-name').setValue('验收-完整路径')
  await view.get('#upstream-site-url').setValue('https://fixture.invalid')
  await view.get('#upstream-site-refresh-token').setValue('fixture-original-token')
 }
 const openToken = async () => { const view = await mountView(); await fillToken(view); return view }
 const submit = async (view: VueWrapper) => { await view.get('[role="dialog"] form').trigger('submit'); await flushPromises() }
 const reject = (stage = 'refresh') => {
  payload = {message:'admin.upstream.errors.refreshTokenRejected', failure:{stage,httpStatus:401,upstreamMessage:'本次测试拒绝'}}
 }
 const forceButton = (view: VueWrapper) => view.findAll('[role="dialog"] button').find(button => button.text() === '强行保存资料')
 const forcedRecord = () => ({...site(), id:'fixture-pending', name:'验收-待验证', platform:'sub2api', requestedPlatform:'auto', authMode:'token', enabled:false, status:'error', errorKey:'admin.upstream.errors.savedWithoutVerification', metrics:{balance:{value:null,display:'-'},todayConsume:{value:null,display:'-'},historyRecharge:{value:null,display:'-'},group:site().metrics.group,groups:[]}})

 it.each(['verify','metrics'])('offers explicit save after %s without asserting token reuse', async stage => {
  reject(stage); const view = await openToken(); await submit(view)
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.findAll('p')[0]!.text()).toContain('本次测试拒绝')
  expect(forceButton(view)).toBeDefined()
  expect(alert.text()).toContain('令牌可能已换新，保存前请核对当前填写内容。')
  expect(alert.text()).not.toContain('可能已被使用')
 })
 it.each([
  ['save', 'admin.upstream.errors.request', undefined],
  ['check', 'admin.upstream.errors.identityCheckFailed', undefined],
  ['metrics', 'admin.upstream.errors.invalidFields', ['name']],
  ['verify', 'admin.upstream.errors.identityCheckFailed', undefined],
  [undefined, 'admin.upstream.errors.network', undefined],
 ] as const)('does not bypass %s/%s failures', async (stage, message, fields) => {
  payload = {message, failure:{stage,httpStatus:0,fields,upstreamMessage:'本次没有完成保存'}}
  const view = await openToken(); await submit(view)
  expect(forceButton(view)).toBeUndefined()
  expect(view.find('[role="dialog"]').exists()).toBe(true)
 })
 it('does not offer save-only when the browser never receives a server failure result', async () => {
  const view = await openToken()
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('fixture network unavailable') }))
  await submit(view)
  expect(forceButton(view)).toBeUndefined()
  expect(view.get('#upstream-site-refresh-token').element).toHaveProperty('value','fixture-original-token')
 })
 it.each(['password','newapi'])('does not offer Token save-only for %s adding', async mode => {
  reject(); const view = await openToken()
  if (mode === 'newapi') await view.get('#upstream-site-platform').setValue('newapi')
  else await view.get('input[type="radio"][value="password"]').setValue(true)
  await submit(view)
  expect(forceButton(view)).toBeUndefined()
 })
 it.each([
  ['name','#upstream-site-name','更正名称'],
  ['site','#upstream-site-url','https://changed.invalid'],
  ['api','#upstream-site-api-url','https://api.changed.invalid'],
  ['refresh','#upstream-site-refresh-token','fixture-changed-token'],
  ['access','#upstream-site-access-token','fixture-access-token'],
  ['type','#upstream-site-token-type','Custom'],
  ['rate','#upstream-site-recharge-rate','2'],
  ['remark','#upstream-site-remark','更正备注'],
  ['platform','#upstream-site-platform','sub2api'],
 ])('clears the previous decision after changing %s', async (_field, selector, value) => {
  reject(); const view = await openToken(); await submit(view)
  expect(forceButton(view)).toBeDefined()
  await view.get(selector).setValue(value)
  expect(view.find('[role="dialog"] [role="alert"]').exists()).toBe(false)
  expect(forceButton(view)).toBeUndefined()
 })
 it('clears the previous decision after toggling new-site pinning', async () => {
  reject(); const view = await openToken(); await submit(view)
  expect(forceButton(view)).toBeDefined()
  await view.get('[role="dialog"] button[role="switch"]').trigger('click')
  expect(forceButton(view)).toBeUndefined()
  expect(view.find('[role="dialog"] [role="alert"]').exists()).toBe(false)
 })
 it('refolds details on a new failure without making requests when opened', async () => {
  reject(); const view = await openToken(); await submit(view)
  const details = view.get('[role="dialog"] details').element as HTMLDetailsElement
  details.open = true
  const before = vi.mocked(fetch).mock.calls.length
  await view.get('[role="dialog"] details').trigger('toggle')
  expect(vi.mocked(fetch).mock.calls.length).toBe(before)
  payload = {message:'admin.upstream.errors.forbidden',failure:{stage:'refresh',httpStatus:403,upstreamMessage:'另一次明确原因'}}
  await submit(view)
  expect((view.get('[role="dialog"] details').element as HTMLDetailsElement).open).toBe(false)
  expect(view.get('[role="dialog"] [role="alert"]').findAll('p')[0]!.text()).toContain('另一次明确原因')
 })
 it('keeps an unknown database outcome visible and retains input without another save-only offer', async () => {
  reject(); const view = await openToken(); await submit(view)
  status = 503
  payload = {message:'admin.upstream.errors.request',failure:{stage:'save',httpStatus:503,upstreamMessage:'数据库提交结果尚未确认，请维护人员核查实际提交结果'}}
  await forceButton(view)!.trigger('click'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.text()).toContain('数据库提交结果尚未确认')
  expect(alert.text()).not.toContain('未保存，可重试或强行保存资料。')
  expect(forceButton(view)).toBeUndefined()
  expect(view.get('#upstream-site-refresh-token').element).toHaveProperty('value','fixture-original-token')
 })
 it('keeps pending verification visible in both presentations and disables enable before verification', async () => {
  const pending = forcedRecord()
  vi.stubGlobal('fetch', vi.fn(async (_url: string, options: RequestInit = {}) => options.method ? new Response('', {status:200}) : new Response(JSON.stringify([pending]), {status:200})))
  const view = await mountView()
  expect(view.get('div.group.bg-card').text()).toContain('已保存，登录待验证')
  expect(view.get('div.group.bg-card').text()).not.toContain('已连接')
  await view.get('div.group.bg-card').findAll('button')[2]!.trigger('click')
  const enable = view.findAll('[role="dialog"] button[role="switch"]')[0]!
  expect(enable.attributes('disabled')).toBeDefined()
  expect(view.get('[role="dialog"]').text()).toContain('请先编辑站点验证登录，再启用刷新')
  const before = vi.mocked(fetch).mock.calls.length
  await enable.trigger('click')
  expect(vi.mocked(fetch).mock.calls.length).toBe(before)
  expect(forceButton(view)).toBeUndefined()
  await view.findAll('[role="dialog"] button').find(button => button.text() === '取消')!.trigger('click')
  await view.get('button[aria-label="列表模式"]').trigger('click')
  expect(view.get('tbody tr').text()).toContain('已保存，登录待验证')
  expect(view.get('tbody tr').text()).not.toContain('已连接')
 })
 it('never carries a previous forceSave option into the next normal creation', async () => {
  reject(); const view = await openToken(); await submit(view)
  payload = forcedRecord(); status = 201
  await forceButton(view)!.trigger('click'); await flushPromises()
  expect(view.find('[role="dialog"]').exists()).toBe(false)
  await fillToken(view)
  payload = {...site(), id:'fixture-next',name:'验收-完整路径',platform:'sub2api',authMode:'token'}
  await submit(view)
  const posts = vi.mocked(fetch).mock.calls.filter(([, options]) => options?.method === 'POST')
  expect(posts).toHaveLength(3)
  expect(JSON.parse(String(posts[1]![1]!.body)).forceSave).toBe(true)
  expect(JSON.parse(String(posts[2]![1]!.body)).forceSave).toBeUndefined()
 })
})


describe('force-save request ownership', () => {
 const openToken = async () => {
  const view = await mountView()
  await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
  await view.get('#upstream-site-name').setValue('验收-并发保护')
  await view.get('#upstream-site-url').setValue('https://fixture.invalid')
  await view.get('#upstream-site-refresh-token').setValue('fixture-input-token')
  payload = {message:'admin.upstream.errors.refreshTokenRejected',failure:{stage:'refresh',httpStatus:401}}
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  return view
 }
 it('does not submit a second explicit save while the first is waiting', async () => {
  const view = await openToken()
  const button = view.findAll('[role="dialog"] button').find(item => item.text() === '强行保存资料')!
  let finish!: (response: Response) => void
  const pending = new Promise<Response>(resolve => { finish = resolve })
  vi.stubGlobal('fetch', vi.fn(() => pending))
  await button.trigger('click')
  await button.trigger('click')
  expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1)
  expect(view.get('#upstream-site-refresh-token').attributes('disabled')).toBeDefined()
  finish(new Response(JSON.stringify({message:'admin.upstream.errors.request',failure:{stage:'save',httpStatus:503,upstreamMessage:'数据库暂时不可用'}}), {status:503}))
  await flushPromises()
  expect(view.get('[role="dialog"] [role="alert"]').text()).toContain('数据库暂时不可用')
 })
 it('clears the previous failure when closing and reopening the create dialog', async () => {
  const view = await openToken()
  await view.findAll('[role="dialog"] button').find(button => button.text() === '取消')!.trigger('click')
  await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
  expect(view.find('[role="dialog"] [role="alert"]').exists()).toBe(false)
  expect(view.get('#upstream-site-refresh-token').element).toHaveProperty('value','')
  expect(view.findAll('[role="dialog"] button').some(button => button.text() === '强行保存资料')).toBe(false)
 })
 it('accepts Access Token only without inventing a Refresh Token warning', async () => {
  const view = await openToken()
  await view.get('#upstream-site-refresh-token').setValue('')
  await view.get('#upstream-site-access-token').setValue('fixture-access-only')
  payload = {message:'admin.upstream.errors.accessTokenRejected',failure:{stage:'verify',httpStatus:401,upstreamMessage:'访问资料被拒绝'}}
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  const alert = view.get('[role="dialog"] [role="alert"]')
  expect(alert.findAll('p')[0]!.text()).toBe('读取账号失败（401）：访问资料被拒绝')
  expect(alert.text()).not.toContain('令牌可能已换新')
  expect(alert.text()).not.toContain('令牌是否仍可用尚未确认')
  expect(view.findAll('[role="dialog"] button').some(button => button.text() === '强行保存资料')).toBe(true)
 })
})


it('normal Token creation accepts a real zero balance and never sends forceSave', async () => {
 const zero = {value:0,display:'0.00'}
 status = 201
 payload = {...site(),id:'fixture-zero',name:'验收-0余额',platform:'sub2api',requestedPlatform:'auto',authMode:'token',enabled:true,status:'connected',errorKey:null,metrics:{balance:zero,todayConsume:zero,historyRecharge:zero,group:site().metrics.group,groups:[]}}
 const view = await mountView()
 await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-0余额')
 await view.get('#upstream-site-url').setValue('https://fixture.invalid')
 await view.get('#upstream-site-refresh-token').setValue('fixture-zero-balance-refresh')
 await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
 expect(view.find('[role="dialog"]').exists()).toBe(false)
 const posts = vi.mocked(fetch).mock.calls.filter(([,options]) => options?.method === 'POST')
 expect(posts).toHaveLength(1)
 expect(JSON.parse(String(posts[0]![1]!.body))).toMatchObject({authMode:'token',refreshToken:'fixture-zero-balance-refresh'})
 expect(JSON.parse(String(posts[0]![1]!.body)).forceSave).toBeUndefined()
 const card = view.findAll('div.group.bg-card').find(item => item.text().includes('验收-0余额'))!
 expect(card.text()).toContain('已连接')
 expect(card.text()).not.toContain('登录待验证')
 const balance = card.findAll('span').find(span => span.text() === '余额')!.element.parentElement!
 expect(balance.textContent).toContain('0.00 USD')
 expect(balance.textContent).not.toContain('- USD')
})


// jsdom can verify the scroll request and scope; actual viewport visibility is
// verified separately in the dedicated browser because jsdom has no layout.
it('scrolls the rendered Token creation failure into view without scrolling a closed dialog or old password entry', async () => {
 const previous = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollIntoView')
 const targets: HTMLElement[] = []
 const scroll = vi.fn(function(this: HTMLElement, _options?: ScrollIntoViewOptions) { targets.push(this) })
 Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {configurable:true,value:scroll})
 try {
  const view = await mountView()
  const open = async () => {
   await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
   await view.get('#upstream-site-name').setValue('验收-错误可见')
   await view.get('#upstream-site-url').setValue('https://fixture.invalid')
   await view.get('#upstream-site-refresh-token').setValue('fixture-scroll-token')
  }
  await open()
  let finish!: (response: Response) => void
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { finish = resolve })))
  await view.get('[role="dialog"] form').trigger('submit')
  await view.get('[role="dialog"] button[aria-label="取消"]').trigger('click')
  const rejection = {message:'admin.upstream.errors.refreshTokenRejected',failure:{stage:'refresh',httpStatus:401,upstreamMessage:'需要看见的具体原因'}}
  finish(new Response(JSON.stringify(rejection), {status:422})); await flushPromises()
  expect(scroll).not.toHaveBeenCalled()
  expect(view.find('[role="dialog"]').exists()).toBe(false)

  payload = rejection
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(payload), {status})))
  await open()
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(scroll).toHaveBeenCalledTimes(1)
  expect(scroll).toHaveBeenCalledWith({block:'nearest'})
  expect(targets[0]).toBe(view.get('[role="dialog"] [role="alert"]').element)
  expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1)
  expect(targets[0]!.textContent).toContain('401')
  expect(targets[0]!.textContent).toContain('需要看见的具体原因')

  await view.get('input[type="radio"][value="password"]').setValue(true)
  payload = {message:'admin.upstream.errors.loginRejected',failure:{stage:'login',httpStatus:401,upstreamMessage:'旧密码入口拒绝'}}
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(scroll).toHaveBeenCalledTimes(1)
  status = 201; payload = {...site(),id:'fixture-scroll-success'}
  await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
  expect(scroll).toHaveBeenCalledTimes(1)
  expect(view.find('[role="dialog"]').exists()).toBe(false)
 } finally {
  if (previous) Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', previous)
  else Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView')
 }
})


it('keeps force-save progress accurate and restores the normal connection label afterwards', async () => {
 payload={message:'admin.upstream.errors.refreshTokenRejected',failure:{stage:'refresh',httpStatus:401,upstreamMessage:'隔离拒绝'}}
 const view=await mountView()
 await view.findAll('button').find(button=>button.text().includes('新增站点'))!.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-保存进度')
 await view.get('#upstream-site-url').setValue('https://fixture.invalid')
 await view.get('#upstream-site-refresh-token').setValue('fixture-progress-token')
 await view.get('[role="dialog"] form').trigger('submit');await flushPromises()
 let complete!: (response:Response)=>void
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{complete=resolve})))
 await view.get('[role="dialog"] [role="alert"]').findAll('button').find(button=>button.text()==='强行保存资料')!.trigger('click')
 try {
  expect(view.get('[role="dialog"] button[type="submit"]').text()).toContain('保存中')
  expect(view.get('[role="dialog"] button[type="submit"]').text()).not.toContain('连接中')
 } finally {
  complete(new Response(JSON.stringify({message:'admin.upstream.errors.request',failure:{stage:'save',httpStatus:503,upstreamMessage:'隔离验收：未保存'}}),{status:503}));await flushPromises()
 }
 let completeNormal!: (response:Response)=>void
 vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(resolve=>{completeNormal=resolve})))
 await view.get('[role="dialog"] form').trigger('submit')
 try {
  expect(view.get('[role="dialog"] button[type="submit"]').text()).toContain('连接中')
  expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0]![1]!.body)).forceSave).toBeUndefined()
 } finally {
  completeNormal(new Response(JSON.stringify({...site(),id:'fixture-progress-success'}),{status:201}));await flushPromises()
 }
})


it.each([
 ['networkTimeout',0,'连接上游超时'],
 ['networkUnreachable',0,'无法连接上游地址'],
 ['tlsFailed',0,'HTTPS 连接失败'],
 ['announcementAckRequired',403,'确认新公告'],
 ['upstreamInsufficientBalance',401,'余额不足'],
 ['upstreamKeyQuotaExhausted',401,'额度已用尽'],
 ['upstreamKeyExpired',401,'已过期'],
 ['invalidResponse',200,'内容无法解析'],
 ['loginIncomplete',200,'登录资料不完整'],
] as const)('retains known transport or structured refusal %s instead of claiming no reason was provided',async(key,httpStatus,reason)=>{
 payload={message:`admin.upstream.errors.${key}`,failure:{errorKey:`admin.upstream.errors.${key}`,stage:'refresh',httpStatus}}
 const view=await mountView()
 await view.findAll('button').find(button=>button.text().includes('新增站点'))!.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-保留已知原因')
 await view.get('#upstream-site-url').setValue('https://fixture.invalid')
 await view.get('#upstream-site-refresh-token').setValue('fixture-known-reason-token')
 await view.get('[role="dialog"] form').trigger('submit');await flushPromises()
 const first=view.get('[role="dialog"] [role="alert"]').findAll('p')[0]!.text()
 expect(first).toContain(reason)
 expect(first).not.toContain('上游未提供具体原因')
})

it('prefers the direct upstream message over a generic proven error classification',async()=>{
 payload={message:'admin.upstream.errors.networkTimeout',failure:{errorKey:'admin.upstream.errors.networkTimeout',stage:'refresh',httpStatus:0,upstreamMessage:'隔离：连接在指定步骤被取消'}}
 const view=await mountView()
 await view.findAll('button').find(button=>button.text().includes('新增站点'))!.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-直接原因优先')
 await view.get('#upstream-site-url').setValue('https://fixture.invalid')
 await view.get('#upstream-site-refresh-token').setValue('fixture-known-reason-token')
 await view.get('[role="dialog"] form').trigger('submit');await flushPromises()
 const first=view.get('[role="dialog"] [role="alert"]').findAll('p')[0]!.text()
 expect(first).toContain('连接在指定步骤被取消')
 expect(first).not.toContain('连接上游超时')
})


it.each([
 ['refreshTokenRejected',401],
 ['accessTokenRejected',401],
 ['auth',401],
 ['loginRejected',401],
 ['forbidden',403],
] as const)('does not infer a refusal cause from generic %s when no upstream message exists', async (key,httpStatus) => {
 payload = {message:`admin.upstream.errors.${key}`,failure:{errorKey:`admin.upstream.errors.${key}`,stage:'refresh',httpStatus}}
 const view = await mountView()
 await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
 await view.get('#upstream-site-name').setValue('验收-未知拒绝不猜测')
 await view.get('#upstream-site-url').setValue('https://fixture.invalid')
 await view.get('#upstream-site-refresh-token').setValue('fixture-unknown-reason-token')
 await view.get('[role="dialog"] form').trigger('submit'); await flushPromises()
 const first = view.get('[role="dialog"] [role="alert"]').findAll('p')[0]!.text()
 expect(first).toBe(`刷新失败（${httpStatus}）：上游未提供具体原因。`)
 expect(first).not.toMatch(/失效|已被使用|密码|余额不足/)
})

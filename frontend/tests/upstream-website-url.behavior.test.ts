// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
// 网站地址 = baseUrl：登录、续期、同步和打开网页都用它。API 地址只给自动导入用。
const site = 'https://www.fixture.invalid'
const api = 'https://api.fixture.invalid'
// 旧版本单独保存的网站链接：表单不再显示，也不随保存提交，已保存的值保持不变。
const legacyLink = 'https://legacy.fixture.invalid:8443/console/?view=keys#login'
const metric = { value: 20, display: '20.00' }
const makeSite = (apiUrl?: string, websiteUrl?: string) => ({ id: 'fixture-site', name: '验收-双地址', baseUrl: site, websiteUrl, apiUrl, platform: 'sub2api', requestedPlatform: 'sub2api', authMode: 'token', account: '7', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
let wrapper: VueWrapper | undefined
let sites: ReturnType<typeof makeSite>[]
let forms: Record<string, unknown>[]
let saveFailure = false
let persistenceFailure = false
beforeEach(() => {
  sites = [makeSite(api)]; forms = []; saveFailure = false; persistenceFailure = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
    if (options.method === 'PUT' || options.method === 'POST') {
      const form = JSON.parse(String(options.body)); forms.push(form)
      if (persistenceFailure) return new Response(JSON.stringify({ message: 'admin.upstream.errors.request', failure: { stage: 'save', upstreamMessage: '保存本站资料失败：存储连接中断；本站资料未保存，原数据库资料保留；连接恢复并核查后可重试，不会修改主站。' } }), { status: 503 })
      if (saveFailure) return new Response(JSON.stringify({ message: 'admin.upstream.errors.invalidFields', failure: { fields: ['apiUrl'], stage: 'check', upstreamMessage: 'API 地址未保存：请填写带 http:// 或 https:// 和完整域名的地址。本次没有请求上游或保存站点资料，填写的 Refresh Token 未使用，不会修改主站；改正后可重新保存。' } }), { status: 400 })
      const original = options.method === 'POST' ? { ...makeSite(), id: 'created-fixture' } : sites[0]!
      const next = { ...original, name: form.name, websiteUrl: form.websiteUrl === undefined ? original.websiteUrl : form.websiteUrl, apiUrl: form.apiUrl === undefined ? original.apiUrl : form.apiUrl.trim(), baseUrl: form.siteUrl }
      if (options.method === 'POST') sites.push(next)
      else sites[0] = next
      return new Response(JSON.stringify(next), { status: 200 })
    }
    return new Response(JSON.stringify(sites), { status: 200 })
  }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })
const open = async (layout: 'card' | 'list' = 'card') => {
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, SiteSettingsModal: true, Tooltip: { props: ['text'], template: '<span :data-tooltip="text"><slot /></span>' } } } })
  await flushPromises()
  if (layout === 'list') await wrapper.get('button[aria-label="列表模式"]').trigger('click')
  return wrapper
}
const edit = async (w: VueWrapper) => { await w.get('[data-tooltip="修改站点"] button').trigger('click'); await flushPromises() }
const save = async (w: VueWrapper) => { await w.get('[role="dialog"] form').trigger('submit'); await flushPromises() }
const siteLink = (w: VueWrapper) => w.get('[role="dialog"]').findAll('a').find(a => a.text() === '打开网站')

describe('website address for login and API address for imports', () => {
  it.each(['card', 'list'] as const)('opens the website in %s and shows both addresses when editing', async layout => {
    const w = await open(layout)
    const link = w.findAll('a').find(a => a.text() === '验收-双地址')!
    expect(link.attributes('href')).toBe(site)
    expect(link.attributes('href')).not.toBe(api)
    expect(link.attributes('target')).toBe('_blank'); expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(w.text()).toContain('点击站点名称打开上游网站，不会修改主站。')
    await edit(w)
    const dialog = w.get('[role="dialog"]').text()
    expect(dialog).toContain('网站地址'); expect(dialog).toContain('API 地址（选填）')
    expect(w.get('#upstream-site-url').element).toHaveProperty('value', site)
    expect(w.get('#upstream-site-api-url').element).toHaveProperty('value', api)
    expect(w.find('#upstream-site-website-url').exists()).toBe(false)
    expect(siteLink(w)!.attributes('href')).toBe(site)
    expect(siteLink(w)!.attributes('target')).toBe('_blank')
    expect(dialog).toContain('可在那里登录并获取 Refresh Token')
    expect(dialog).not.toContain('打开 API 站点')
    expect(forms).toHaveLength(0)
  })
  it('explains that the API address is only for automatic imports', async () => {
    const w = await open()
    await w.findAll('button').find(b => b.text().includes('新增站点'))!.trigger('click')
    const input = w.get('#upstream-site-api-url')
    expect(input.attributes('required')).toBeUndefined(); expect(input.attributes('type')).toBe('text'); expect(input.attributes('inputmode')).toBe('url')
    expect(w.get('#upstream-site-url').attributes('required')).toBeDefined()
    const dialog = w.get('[role="dialog"]').text()
    expect(dialog).toContain('只在自动导入到主站时使用')
    expect(dialog).toContain('读取一次上游公开的 API 地址，读不到就沿用网站地址')
    expect(dialog).toContain('系统用它登录、续期 Refresh Token 和同步余额')
    expect(w.get('#upstream-site-refresh-token').exists()).toBe(true)
  })
  it.each([undefined, ''])('shows an empty API address for old sites without one (%s)', async value => {
    sites = [makeSite(value)]
    const w = await open(); await edit(w)
    expect((w.get('#upstream-site-api-url').element as HTMLInputElement).value).toBe('')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(site)
  })
  it('saves and clears the API address without credentials or domain migration', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-api-url').setValue('https://other-api.fixture.invalid')
    expect(w.find('#upstream-domain-migration-confirm').exists()).toBe(false)
    await save(w)
    expect(forms).toHaveLength(1)
    expect(forms[0]).toMatchObject({ siteUrl: site, apiUrl: 'https://other-api.fixture.invalid', refreshToken: '', accessToken: '', password: '', authMode: 'token' })
    expect(forms[0]).not.toHaveProperty('websiteUrl')
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    await edit(w)
    expect(w.get('#upstream-site-api-url').element).toHaveProperty('value', 'https://other-api.fixture.invalid')
    await w.get('#upstream-site-api-url').setValue(''); await save(w)
    expect(forms[1]!.apiUrl).toBe('')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(site)
  })
  it('keeps a saved legacy website link untouched when editing', async () => {
    sites = [makeSite(api, legacyLink)]
    const w = await open()
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(legacyLink)
    await edit(w)
    expect(w.find('#upstream-site-website-url').exists()).toBe(false)
    expect(w.get('#upstream-site-url').element).toHaveProperty('value', site)
    await w.get('#upstream-site-api-url').setValue('https://other-api.fixture.invalid'); await save(w)
    expect(forms[0]).not.toHaveProperty('websiteUrl')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(legacyLink)
  })
  it('does not silently fall back for an unsafe legacy website link', async () => {
    sites = [makeSite(api, 'javascript:alert(1)')]
    const w = await open()
    expect(w.findAll('a').some(a => a.text() === '验收-双地址')).toBe(false)
    expect(w.text()).toContain('这个站点以前单独保存的网站链接格式不对，暂时无法打开')
    expect(w.html()).not.toContain('href="javascript:')
  })
  it('cancels edits without changing the saved API address', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-api-url').setValue('https://unsaved.fixture.invalid')
    await w.get('[role="dialog"] button[aria-label="取消"]').trigger('click')
    expect(forms).toHaveLength(0)
    await edit(w); expect(w.get('#upstream-site-api-url').element).toHaveProperty('value', api)
  })
  it('keeps failed input with a precise API field label', async () => {
    const w = await open(); await edit(w); saveFailure = true
    await w.get('#upstream-site-api-url').setValue('api.fixture.invalid'); await save(w)
    const alert = w.get('[role="dialog"] [role="alert"]').text()
    expect(alert).toContain('API 地址（选填）')
    expect(alert).toContain('Refresh Token 未使用')
    expect(w.get('#upstream-site-api-url').element).toHaveProperty('value', 'api.fixture.invalid')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(site)
  })
  it('opens the website shortcut from the website address and disables it when invalid', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-url').setValue('https://new-www.fixture.invalid/path/?q=gone#gone')
    expect(siteLink(w)!.attributes('href')).toBe('https://new-www.fixture.invalid/path')
    expect(w.find('#upstream-domain-migration-confirm').exists()).toBe(true)
    await w.get('#upstream-site-api-url').setValue(api)
    expect(siteLink(w)!.attributes('href')).not.toBe(api)
    await w.get('#upstream-site-url').setValue('')
    expect(siteLink(w)).toBeUndefined()
    expect(w.get('[role="dialog"]').text()).toContain('请先填写有效的网站地址，再打开网站获取登录信息。')
    expect(forms).toHaveLength(0)
  })
  it('creates through the existing token form and sends the website and API addresses', async () => {
    const w = await open(); await w.findAll('button').find(b => b.text().includes('新增站点'))!.trigger('click')
    await w.get('#upstream-site-name').setValue('验收-新增双地址')
    await w.get('#upstream-site-url').setValue(site)
    await w.get('#upstream-site-api-url').setValue(api)
    await w.get('#upstream-site-refresh-token').setValue('synthetic-new-refresh')
    await save(w)
    expect(forms[0]).toMatchObject({ name: '验收-新增双地址', siteUrl: site, apiUrl: api, authMode: 'token', refreshToken: 'synthetic-new-refresh' })
    expect(forms[0]).not.toHaveProperty('websiteUrl')
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(w.findAll('a').find(a => a.text() === '验收-新增双地址')!.attributes('href')).toBe(site)
  })
  it('keeps the new API input and old target on a storage failure', async () => {
    const w = await open(); await edit(w); persistenceFailure = true
    await w.get('#upstream-site-api-url').setValue('https://unsaved.fixture.invalid'); await save(w)
    expect(w.get('[role="dialog"] [role="alert"]').text()).toContain('原数据库资料保留')
    expect(w.get('#upstream-site-api-url').element).toHaveProperty('value', 'https://unsaved.fixture.invalid')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(site)
    expect(forms[0]).toMatchObject({ siteUrl: site, refreshToken: '', accessToken: '', password: '' })
  })
})

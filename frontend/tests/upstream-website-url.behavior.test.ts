// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))
const api = 'https://api.fixture.invalid'
const website = 'https://www.fixture.invalid:8443/console/?view=keys#login'
const metric = { value: 20, display: '20.00' }
const makeSite = (websiteUrl?: string) => ({ id: 'fixture-site', name: '验收-双地址', baseUrl: api, websiteUrl, platform: 'sub2api', requestedPlatform: 'sub2api', authMode: 'token', account: '7', rechargeRate: 1, enabled: true, remark: '', status: 'connected', errorKey: null, metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
let wrapper: VueWrapper | undefined
let sites: ReturnType<typeof makeSite>[]
let forms: Record<string, unknown>[]
let saveFailure = false
let persistenceFailure = false
beforeEach(() => {
  sites = [makeSite(website)]; forms = []; saveFailure = false; persistenceFailure = false
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
    if (options.method === 'PUT' || options.method === 'POST') {
      const form = JSON.parse(String(options.body)); forms.push(form)
      if (persistenceFailure) return new Response(JSON.stringify({ message: 'admin.upstream.errors.request', failure: { stage: 'save', upstreamMessage: '保存本站资料失败：存储连接中断；本站资料未保存，原数据库资料保留；连接恢复并核查后可重试，不会修改主站。' } }), { status: 503 })
      if (saveFailure) return new Response(JSON.stringify({ message: 'admin.upstream.errors.invalidFields', failure: { fields: ['websiteUrl'], stage: 'check', upstreamMessage: '网站地址未保存：只支持 http:// 或 https://。本次没有请求上游或保存站点资料，填写的 Refresh Token 未使用，不会修改主站；改正后可重新保存。' } }), { status: 400 })
      const original = options.method === 'POST' ? { ...makeSite(), id: 'created-fixture' } : sites[0]!
      const next = { ...original, name: form.name, websiteUrl: form.websiteUrl?.trim() ?? original.websiteUrl, baseUrl: form.siteUrl }
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

describe('independent website and API addresses', () => {
  it.each(['card', 'list'] as const)('opens website in %s and keeps API quick access', async layout => {
    const w = await open(layout)
    const link = w.findAll('a').find(a => a.text() === '验收-双地址')!
    expect(link.attributes('href')).toBe(website)
    expect(link.attributes('target')).toBe('_blank'); expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(w.text()).toContain('点击站点名称打开网站；未填网站地址时打开 API 站点，不会修改主站。')
    await edit(w)
    expect(w.get('#upstream-site-url').element).toHaveProperty('value', api)
    expect(w.get('#upstream-site-website-url').element).toHaveProperty('value', website)
    const apiLink = w.get('[role="dialog"]').findAll('a').find(a => a.text() === '打开 API 站点')!
    expect(apiLink.attributes('href')).toBe(api)
    expect(apiLink.attributes('href')).not.toBe(website)
    expect(apiLink.attributes('target')).toBe('_blank')
    expect(w.get('[role="dialog"]').text()).toContain('可到该站登录并获取 Refresh Token')
    expect(forms).toHaveLength(0)
  })
  it.each([undefined, '', '   '])('keeps old API fallback without filling a website %s', async value => {
    sites = [makeSite(value)]
    const w = await open()
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(api)
    await edit(w)
    expect((w.get('#upstream-site-website-url').element as HTMLInputElement).value.trim()).toBe('')
  })
  it('saves and clears website without credentials or domain migration', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-website-url').setValue('https://other.fixture.invalid/path/?q=one#two')
    expect(w.find('#upstream-domain-migration-confirm').exists()).toBe(false)
    await save(w)
    expect(forms).toHaveLength(1)
    expect(forms[0]).toMatchObject({ siteUrl: api, websiteUrl: 'https://other.fixture.invalid/path/?q=one#two', refreshToken: '', accessToken: '', password: '', authMode: 'token' })
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    await edit(w)
    expect(w.get('#upstream-site-website-url').element).toHaveProperty('value', 'https://other.fixture.invalid/path/?q=one#two')
    await w.get('#upstream-site-website-url').setValue(''); await save(w)
    expect(forms[1]!.websiteUrl).toBe('')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(api)
  })
  it('does not silently fall back for a nonempty unsafe website', async () => {
    sites = [makeSite('javascript:alert(1)')]
    const w = await open()
    expect(w.findAll('a').some(a => a.text() === '验收-双地址')).toBe(false)
    expect(w.text()).toContain('这个站点保存的网站地址格式不对，暂时无法打开')
    expect(w.html()).not.toContain('href="javascript:')
  })
  it('cancels edits without changing the saved target', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-website-url').setValue('https://unsaved.fixture.invalid')
    await w.get('[role="dialog"] button[aria-label="取消"]').trigger('click')
    expect(forms).toHaveLength(0)
    await edit(w); expect(w.get('#upstream-site-website-url').element).toHaveProperty('value', website)
  })
  it('keeps failed input with a precise website field label', async () => {
    const w = await open(); await edit(w); saveFailure = true
    await w.get('#upstream-site-website-url').setValue('ftp://fixture.invalid'); await save(w)
    expect(w.get('[role="dialog"] [role="alert"]').text()).toContain('网站地址（选填）')
    expect(w.get('[role="dialog"] [role="alert"]').text()).toContain('Refresh Token 未使用')
    expect(w.get('#upstream-site-website-url').element).toHaveProperty('value', 'ftp://fixture.invalid')
    expect(w.findAll('a').find(a => a.text() === '验收-双地址')!.attributes('href')).toBe(website)
  })
  it('keeps API shortcut independent and disables it when API is invalid', async () => {
    const w = await open(); await edit(w)
    await w.get('#upstream-site-url').setValue('https://new-api.fixture.invalid/path/?q=gone#gone')
    expect(w.get('[role="dialog"]').findAll('a').find(a => a.text() === '打开 API 站点')!.attributes('href')).toBe('https://new-api.fixture.invalid/path')
    expect(w.find('#upstream-domain-migration-confirm').exists()).toBe(true)
    await w.get('#upstream-site-url').setValue('')
    expect(w.get('[role="dialog"]').findAll('a').some(a => a.text() === '打开 API 站点')).toBe(false)
    expect(w.get('[role="dialog"]').text()).toContain('请先填写有效的 API 地址，再打开站点获取登录信息。')
    expect(forms).toHaveLength(0)
  })
  it('adds website as optional while retaining default token fields', async () => {
    const w = await open()
    await w.findAll('button').find(b => b.text().includes('新增站点'))!.trigger('click')
    const input = w.get('#upstream-site-website-url')
    expect(input.attributes('required')).toBeUndefined(); expect(input.attributes('type')).toBe('text'); expect(input.attributes('inputmode')).toBe('url')
    expect(w.get('#upstream-site-refresh-token').exists()).toBe(true)
    expect(w.get('[role="dialog"]').text()).toContain('只改这里不用重新填写登录信息，不会修改主站。')
  })
  it('creates through the existing token form and sends the two independent addresses', async () => {
    const w=await open();await w.findAll('button').find(b=>b.text().includes('新增站点'))!.trigger('click')
    await w.get('#upstream-site-name').setValue('验收-新增双地址')
    await w.get('#upstream-site-url').setValue(api)
    await w.get('#upstream-site-website-url').setValue(website)
    await w.get('#upstream-site-refresh-token').setValue('synthetic-new-refresh')
    await save(w)
    expect(forms[0]).toMatchObject({name:'验收-新增双地址',siteUrl:api,websiteUrl:website,authMode:'token',refreshToken:'synthetic-new-refresh'})
    expect(w.find('[role="dialog"]').exists()).toBe(false)
    expect(w.findAll('a').find(a=>a.text()==='验收-新增双地址')!.attributes('href')).toBe(website)
  })
  it('keeps the new website input and old target on a storage failure',async()=>{
    const w=await open();await edit(w);persistenceFailure=true
    await w.get('#upstream-site-website-url').setValue('https://unsaved.fixture.invalid/');await save(w)
    expect(w.get('[role="dialog"] [role="alert"]').text()).toContain('原数据库资料保留')
    expect(w.get('#upstream-site-website-url').element).toHaveProperty('value','https://unsaved.fixture.invalid/')
    expect(w.findAll('a').find(a=>a.text()==='验收-双地址')!.attributes('href')).toBe(website)
    expect(forms[0]).toMatchObject({siteUrl:api,refreshToken:'',accessToken:'',password:''})
  })
})

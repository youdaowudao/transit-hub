// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { UpstreamSiteResponse } from '@/modules/admin/types/upstream'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

const rejectedKey = 'admin.upstream.errors.refreshTokenRejected'
const help = '上次延长登录有效期时被上游拒绝，本站已暂停自动访问。请点“编辑站点”，用原账号重新登录；原资料和历史费用保留，不会直接修改主站账号。'
const fixture = (): UpstreamSiteResponse => ({ id: 'fixture-site', name: '验收-续期保存', baseUrl: 'https://api.fixture.invalid', websiteUrl: 'https://website.fixture.invalid', platform: 'sub2api', requestedPlatform: 'sub2api', authMode: 'token', account: 'fixture-account', rechargeRate: 1, enabled: true, pinned: false, remark: '原备注', status: 'connected', errorKey: null, metrics: { balance: { value: 7, display: '7' }, todayConsume: { value: 1, display: '1' }, historyRecharge: { value: 10, display: '10' }, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] }, settings: { balanceThreshold: null }, lastSyncedAt: null })
const pause = (site = fixture()): UpstreamSiteResponse => ({ ...site, status: 'error', errorKey: rejectedKey })
let wrapper: VueWrapper | undefined
let saved: UpstreamSiteResponse
let failSnapshot: boolean
let failLogin: boolean
let cacheReads: number
let syncCalls: number
let creationFailure: boolean

beforeEach(() => {
  saved = fixture(); failSnapshot = false; failLogin = false; cacheReads = 0; syncCalls = 0; creationFailure = false
  localStorage.clear()
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
    if (url.endsWith('/sync')) {
      syncCalls++
      saved = pause(saved)
      return new Response(JSON.stringify({ message: rejectedKey, failure: { stage: 'refresh', httpStatus: 401, errorDetail: '上游拒绝了延长本站登录有效期，本次未读取费用，已暂停自动访问。' } }), { status: 422 })
    }
    if (options.method === 'PUT') {
      const form = JSON.parse(String(options.body)) as Record<string, string>
      if (failLogin) return new Response(JSON.stringify({ message: 'admin.upstream.errors.loginRejected', failure: { stage: 'login', httpStatus: 401, upstreamMessage: 'fixture same account login rejected' } }), { status: 422 })
      saved = { ...saved, name: form.name!, remark: form.remark! }
      if (form.accessToken || form.refreshToken || form.password) saved = { ...saved, status: 'connected', errorKey: null }
      return new Response(JSON.stringify(saved), { status: 200 })
    }
    if (options.method === 'POST' && creationFailure) return new Response(JSON.stringify({ message: rejectedKey, failure: { stage: 'refresh', httpStatus: 401, upstreamMessage: 'fixture refresh rejected' } }), { status: 422 })
    cacheReads++
    if (url.includes('siteId=') && failSnapshot) return new Response(JSON.stringify({ message: 'admin.upstream.errors.request', failure: { stage: 'save', httpStatus: 503, errorDetail: '本站数据库连接中断，请检查数据库连接后重试。' } }), { status: 503 })
    return new Response(JSON.stringify([structuredClone(saved)]), { status: 200 })
  }))
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })

const mountView = async (mode: 'card' | 'list') => {
  wrapper = mount(UpstreamView, { global: { stubs: { Teleport: true, Tooltip: { template: '<span><slot /></span>' }, SiteSettingsModal: true } } })
  await flushPromises()
  if (mode === 'list') await wrapper.get('button[aria-label="列表模式"]').trigger('click')
  return wrapper
}
const entry = (mode: 'card' | 'list') => wrapper!.get(mode === 'card' ? 'div.group.bg-card' : 'tbody tr')
const sync = async (mode: 'card' | 'list') => { await entry(mode).findAll('button')[0]!.trigger('click'); await flushPromises() }
const edit = async (mode: 'card' | 'list') => { await entry(mode).findAll('button')[2]!.trigger('click'); await flushPromises() }

describe('续期被拒绝后的站点页面', () => {
  it.each(['card', 'list'] as const)('%s shows a saved pause with the existing editing action and keeps website/API separation', async mode => {
    saved = pause()
    await mountView(mode)
    expect(entry(mode).text()).toContain(help)
    expect(entry(mode).text()).not.toContain('已连接')
    const links = entry(mode).findAll('a')
    expect(links.some(link => link.attributes('href') === saved.websiteUrl)).toBe(true)
    expect(links.some(link => link.attributes('href') === saved.baseUrl)).toBe(false)
    await edit(mode)
    expect((wrapper!.get('#upstream-site-url').element as HTMLInputElement).value).toBe(saved.baseUrl)
  })

  it.each(['card', 'list'] as const)('%s immediately fetches saved state once after the first rejected manual sync', async mode => {
    await mountView(mode)
    const before = cacheReads
    await sync(mode)
    expect(syncCalls).toBe(1)
    expect(cacheReads - before).toBe(1)
    expect(entry(mode).text()).toContain(help)
    expect(entry(mode).text()).not.toContain('连接状态待确认')
    expect(entry(mode).text()).not.toContain('已连接')
  })

  it.each(['card', 'list'] as const)('%s does not claim connected or paused when the saved-state read fails', async mode => {
    await mountView(mode); failSnapshot = true
    await sync(mode)
    expect(entry(mode).text()).toContain('连接状态待确认')
    expect(entry(mode).text()).toContain('本站数据库连接中断')
    expect(entry(mode).text()).toContain('请刷新站点列表查看')
    expect(entry(mode).text()).not.toContain(help)
    expect(entry(mode).text()).not.toContain('已连接')
    expect(entry(mode).text()).toContain('7')
    expect(syncCalls).toBe(1)
  })

  it.each(['card', 'list'] as const)('%s clears the old authentication warning only after accepted new login', async mode => {
    await mountView(mode); await sync(mode); await edit(mode)
    await wrapper!.get('#upstream-site-access-token').setValue('fixture-new-login')
    await wrapper!.get('[role="dialog"] form').trigger('submit'); await flushPromises()
    expect(wrapper!.find('[role="dialog"]').exists()).toBe(false)
    expect(entry(mode).text()).toContain('已连接')
    expect(entry(mode).text()).not.toContain('已暂停自动访问')
    expect(entry(mode).text()).not.toContain('连接状态待确认')
  })

  it.each(['metadata', 'login_failed'] as const)('keeps the saved pause after %s', async action => {
    await mountView('card'); await sync('card'); await edit('card')
    if (action === 'metadata') await wrapper!.get('#upstream-site-remark').setValue('只改备注')
    else { failLogin = true; await wrapper!.get('#upstream-site-access-token').setValue('fixture-rejected-login') }
    await wrapper!.get('[role="dialog"] form').trigger('submit'); await flushPromises()
    expect(entry('card').text()).toContain(help)
    expect(entry('card').text()).not.toContain('已连接')
  })

  it.each(['connected', 'network', 'newapi'] as const)('does not display a saved pause for %s', async state => {
    if (state === 'network') saved = { ...saved, status: 'error', errorKey: 'admin.upstream.errors.networkTimeout' }
    if (state === 'newapi') saved = { ...pause(), platform: 'newapi', requestedPlatform: 'newapi' }
    await mountView('card')
    expect(entry('card').text()).not.toContain(help)
    expect(entry('card').text()).not.toContain('连接状态待确认')
  })

  it('does not claim a saved pause after an unsuccessful creation', async () => {
    creationFailure = true
    await mountView('card')
    await wrapper!.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
    await wrapper!.get('#upstream-site-name').setValue('验收-创建失败')
    await wrapper!.get('#upstream-site-url').setValue('https://new.fixture.invalid')
    await wrapper!.get('#upstream-site-refresh-token').setValue('fixture-refresh')
    await wrapper!.get('[role="dialog"] form').trigger('submit'); await flushPromises()
    expect(wrapper!.find('[role="dialog"]').exists()).toBe(true)
    expect(wrapper!.text()).not.toContain(help)
  })
})

// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamView from '@/modules/admin/views/UpstreamView.vue'
import type { ResolvedUpstreamPlatform, UpstreamAuthMode } from '@/modules/admin/types/upstream'

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/settings', () => ({ getStrategySettings: async () => ({ enableRefreshInterval: false, refreshInterval: 300 }) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthAdminGroups: async () => [] }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: async () => [] }))

const oldURL = 'https://original.invalid'
const newURL = 'https://migrated.invalid'
const confirmSelector = '#upstream-domain-migration-confirm'
const identityHelp = '原站点更换域名时可以在这里修改，核实原账号后会保留原有费用和对接记录。也可以更新凭据或切换登录方式；核对不通过不会保存，不会修改主站。'
const migrationHelp = '请先确认新地址属于原站点。保存时会在新地址核实原账号，成功后本站从新地址读取数据，原费用和对接记录保留。'
const confirmHelp = '我确认这是原站点的新地址，继续保留原有费用和对接记录。'
const mainSiteHelp = '不会自动修改主站已导入账号的地址；主站仍使用旧地址时，请在主站单独更新。'
const unconfirmed = '更换站点地址前，请勾选确认这是原站点的新地址。本次没有请求上游，资料和费用未保存，不会修改主站。'
const refreshWarning = '填写的 Refresh Token 可能已被使用，重试前请到上游重新获取。'
const metric = { value: 20, display: '20.00' }
const makeSite = (id: string, platform: ResolvedUpstreamPlatform = 'newapi', authMode: UpstreamAuthMode = 'user_key') => ({
  id, name: `迁移验收-${id}`, baseUrl: oldURL, platform, requestedPlatform: platform, authMode,
  account: authMode === 'user_key' ? '7' : 'owner@example.com', rechargeRate: 2, enabled: true,
  remark: '保留原备注', status: 'connected', errorKey: null,
  metrics: { balance: metric, todayConsume: metric, historyRecharge: metric, group: { id: '', name: '-', platform: null, multiplier: null, multiplierDisplay: '-' }, groups: [] },
  settings: { balanceThreshold: 5 }, lastSyncedAt: null,
})
let wrapper: VueWrapper | undefined
let sites: ReturnType<typeof makeSite>[]
let savedForms: Record<string, unknown>[]
let status: number
let failure: { stage: string; errorKey: string; upstreamMessage: string }
let pendingSave: Promise<void> | undefined

beforeEach(() => {
  sites = [makeSite('first'), makeSite('second')]
  savedForms = []
  status = 200
  pendingSave = undefined
  failure = { stage: 'verify', errorKey: 'admin.upstream.errors.accessTokenRejected', upstreamMessage: '原登录资料在新地址已失效，请填写新凭据后重试' }
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url.endsWith('/sync-stream')) return new Response('', { status: 200 })
    if (options.method === 'PUT' || options.method === 'POST') {
      const form = JSON.parse(String(options.body)) as Record<string, unknown>
      savedForms.push(form)
      await pendingSave
      if (status >= 400) return new Response(JSON.stringify({ message: failure.errorKey, failure }), { status })
      const id = url.split('/').pop()!
      const index = sites.findIndex(site => site.id === id)
      const original = sites[index] ?? makeSite('created', 'sub2api', 'token')
      const next = { ...original, baseUrl: String(form.siteUrl), remark: String(form.remark), authMode: form.authMode as UpstreamAuthMode }
      if (index >= 0) sites[index] = next
      else sites.push(next)
      return new Response(JSON.stringify(next), { status })
    }
    return new Response(JSON.stringify(sites), { status: 200 })
  }))
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.unstubAllGlobals()
  localStorage.clear()
  document.body.innerHTML = ''
})

const mountView = async (layout: 'card' | 'list' = 'card') => {
  wrapper = mount(UpstreamView, { global: { stubs: {
    Teleport: true, SiteSettingsModal: true,
    Tooltip: { props: ['text'], template: '<span :data-tooltip="text"><slot /></span>' },
  } } })
  await flushPromises()
  if (layout === 'list') await wrapper.get('button[aria-label="列表模式"]').trigger('click')
  return wrapper
}
const edit = async (view: VueWrapper, index = 0) => {
  await view.findAll('[data-tooltip="修改站点"] button')[index]!.trigger('click')
  await flushPromises()
}
const submit = async (view: VueWrapper) => {
  await view.get('[role="dialog"] form').trigger('submit')
  await flushPromises()
}
const confirm = async (view: VueWrapper) => {
  await view.get(confirmSelector).setValue(true)
}
const changeURL = async (view: VueWrapper, value = newURL) => {
  await view.get('#upstream-site-url').setValue(value)
}
const saveButton = (view: VueWrapper) => view.get('[role="dialog"] button[type="submit"]')

describe('same-account domain migration in the existing edit dialog', () => {
  it('requires an explicit declaration before any request and explains the main-site boundary', async () => {
    const view = await mountView()
    await edit(view)
    expect(view.get('[role="dialog"]').text()).toContain(identityHelp)
    expect(view.find(confirmSelector).exists()).toBe(false)
    expect(saveButton(view).attributes('disabled')).toBeUndefined()
    await changeURL(view)
    const dialog = view.get('[role="dialog"]')
    for (const text of [migrationHelp, confirmHelp, mainSiteHelp, `${oldURL} → ${newURL}`]) expect(dialog.text()).toContain(text)
    expect(dialog.text()).not.toContain('更换站点地址、平台或账号请新增站点')
    expect(saveButton(view).attributes('disabled')).toBeDefined()
    await submit(view)
    expect(savedForms).toHaveLength(0)
    expect(view.get('[role="dialog"] [role="alert"]').text()).toBe(unconfirmed)
    await confirm(view)
    expect(saveButton(view).attributes('disabled')).toBeUndefined()
    expect(view.find('[role="dialog"] [role="alert"]').exists()).toBe(false)
  })

  it.each([
    ['card', 'newapi', 'password'], ['card', 'newapi', 'user_key'],
    ['card', 'sub2api', 'password'], ['card', 'sub2api', 'token'],
    ['list', 'newapi', 'password'], ['list', 'newapi', 'user_key'],
    ['list', 'sub2api', 'password'], ['list', 'sub2api', 'token'],
  ] as const)('saves and reopens %s / %s / %s without creating a duplicate', async (layout, platform, mode) => {
    sites[0] = makeSite('first', platform, mode)
    const view = await mountView(layout)
    await edit(view)
    await changeURL(view)
    await confirm(view)
    await submit(view)
    expect(savedForms).toHaveLength(1)
    expect(savedForms[0]).toMatchObject({ confirmDomainMigration: true, siteUrl: newURL, platform, authMode: mode, password: '', accessToken: '', refreshToken: '', remark: '保留原备注', rechargeRate: 2 })
    expect(view.find('[role="dialog"]').exists()).toBe(false)
    const siteLinks = view.findAll('a').filter(link => link.text().startsWith('迁移验收-'))
    expect(siteLinks).toHaveLength(2)
    expect(siteLinks.find(link => link.text() === '迁移验收-first')!.attributes('href')).toBe(newURL)
    await edit(view)
    expect((view.get('#upstream-site-url').element as HTMLInputElement).value).toBe(newURL)
    expect((view.get('#upstream-site-remark').element as HTMLInputElement).value).toBe('保留原备注')
    expect(view.find(confirmSelector).exists()).toBe(false)
    await changeURL(view, `${newURL}/second`)
    expect((view.get(confirmSelector).element as HTMLInputElement).checked).toBe(false)
    expect(saveButton(view).attributes('disabled')).toBeDefined()
  })

  it.each([`${oldURL}///`, `${oldURL}/?page=2#anchor`, ' original.invalid/ '])('keeps a normalized same address on the old edit path: %s', async url => {
    const view = await mountView()
    await edit(view)
    await changeURL(view, url)
    expect(view.find(confirmSelector).exists()).toBe(false)
    expect(saveButton(view).attributes('disabled')).toBeUndefined()
    await submit(view)
    expect(savedForms).toHaveLength(1)
    expect(savedForms[0]).not.toHaveProperty('confirmDomainMigration')
  })

  it('does not silently equate changed hostname case and resets confirmation on every address edit', async () => {
    const view = await mountView()
    await edit(view)
    await changeURL(view, 'https://ORIGINAL.invalid')
    expect(view.find(confirmSelector).exists()).toBe(true)
    await confirm(view)
    await changeURL(view, 'https://ORIGINAL.invalid/')
    expect((view.get(confirmSelector).element as HTMLInputElement).checked).toBe(false)
    await changeURL(view, oldURL)
    expect(view.find(confirmSelector).exists()).toBe(false)
    await changeURL(view)
    expect((view.get(confirmSelector).element as HTMLInputElement).checked).toBe(false)
  })

  it('resets confirmation when closing, reopening, or choosing another edit object', async () => {
    const view = await mountView()
    await edit(view)
    await changeURL(view)
    await confirm(view)
    await view.get('[role="dialog"] button[aria-label="取消"]').trigger('click')
    await edit(view)
    await changeURL(view)
    expect((view.get(confirmSelector).element as HTMLInputElement).checked).toBe(false)
    await confirm(view)
    await edit(view, 1)
    expect((view.get('#upstream-site-name').element as HTMLInputElement).value).toBe('迁移验收-second')
    await changeURL(view)
    expect((view.get(confirmSelector).element as HTMLInputElement).checked).toBe(false)
    expect(savedForms).toHaveLength(0)
  })

  it('disables confirmation and duplicate submission while saving', async () => {
    let release!: () => void
    pendingSave = new Promise<void>(resolve => { release = resolve })
    const view = await mountView()
    await edit(view)
    await changeURL(view)
    await confirm(view)
    await submit(view)
    expect(savedForms).toHaveLength(1)
    expect(saveButton(view).attributes('disabled')).toBeDefined()
    expect(view.get(confirmSelector).attributes('disabled')).toBeDefined()
    await submit(view)
    expect(savedForms).toHaveLength(1)
    release()
    await flushPromises()
    expect(view.find('[role="dialog"]').exists()).toBe(false)
  })

  it('keeps failed edits in the dialog with the original record and actionable reason', async () => {
    status = 422
    const view = await mountView()
    await edit(view)
    await changeURL(view)
    await confirm(view)
    await submit(view)
    const alert = view.get('[role="dialog"] [role="alert"]')
    expect(alert.text()).toContain(failure.upstreamMessage)
    expect(alert.text()).toContain('失败步骤：读取身份')
    expect(alert.text()).toContain('原站点资料和费用保持不变')
    expect((view.get('#upstream-site-url').element as HTMLInputElement).value).toBe(newURL)
    expect(sites[0]!.baseUrl).toBe(oldURL)
    expect(view.findAll('a').find(link => link.text() === '迁移验收-first')!.attributes('href')).toBe(oldURL)
    expect(alert.text()).not.toContain('重试也不会通过')
  })

  it.each(['verify', 'metrics', 'save'])('keeps exactly one Refresh Token reminder in a %s failure', async stage => {
    sites[0] = makeSite('first', 'sub2api', 'token')
    status = 503
    failure = { stage, errorKey: 'admin.upstream.errors.saveFailed', upstreamMessage: `${stage === 'save' ? '本站资料已保存，当前运行数据未更新。' : '上游暂时无法读取，本次未保存。'}${refreshWarning}` }
    const view = await mountView()
    await edit(view)
    await changeURL(view)
    await confirm(view)
    await view.get('#upstream-site-refresh-token').setValue('fixture-new-refresh')
    await submit(view)
    const alert = view.get('[role="dialog"] [role="alert"]')
    expect(alert.text().split(refreshWarning)).toHaveLength(2)
    expect(alert.text()).toContain(failure.upstreamMessage)
    if (stage === 'save') {
      expect(alert.text()).toContain('实际结果：本站资料已保存，当前运行数据未更新。')
      expect(alert.text()).not.toContain('原站点资料和费用保持不变')
    }
  })

  it('keeps domain migration confirmation out of site creation', async () => {
    const view = await mountView()
    await view.findAll('button').find(button => button.text().includes('新增站点'))!.trigger('click')
    await view.get('#upstream-site-name').setValue('新站点')
    await changeURL(view)
    await view.get('#upstream-site-refresh-token').setValue('fixture-create')
    expect(view.find(confirmSelector).exists()).toBe(false)
    expect(view.get('[role="dialog"]').text()).not.toContain(migrationHelp)
    await submit(view)
    expect(savedForms).toHaveLength(1)
    expect(savedForms[0]).not.toHaveProperty('confirmDomainMigration')
  })
})

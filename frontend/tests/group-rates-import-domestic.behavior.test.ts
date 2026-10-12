// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import { realConnect } from '@/modules/admin/api/mySites'
import { importPlatformsCompatible } from '@/modules/admin/utils/importPlatforms'
import type { RealConnectRequest } from '@/modules/admin/types/mySites'

const harness = vi.hoisted(() => ({
  currentAccount: null as any,
  requests: [] as RealConnectRequest[],
  bindings: [] as Record<string, unknown>[],
  importResponse: null as any,
  mappingResponse: null as any,
  connectionResponse: null as any,
  listGroupRates: vi.fn(),
  platform: 'sub2api',
  rate: null as any,
  availableGroups: [] as typeof groups,
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ push: vi.fn(), replace: vi.fn(async () => undefined) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'workspace-c5', displayName: 'C5 隔离工作区' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ getDashboardAdminStatus: vi.fn(async () => ({ platform: harness.platform })) }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: vi.fn(async () => []) }))
vi.mock('@/modules/admin/api/groupRates', () => ({ listGroupRates: harness.listGroupRates, listAllGroupRates: vi.fn(async () => []), listGroupRateHistory: vi.fn(async () => []), updateGroupRateType: vi.fn() }))

const rate = { siteId: 'site-c5', siteName: 'C5 上游', groupId: '21', groupName: 'C5 上游组', type: 'openai', platform: 'sub2api', mapped: false, currentMultiplier: 1, delta: 0, deltaPercent: 0, deleted: false, updatedAt: '2026-10-07T00:00:00Z' }
const groups = [
  { id: '10', groupName: '表单分组甲', platform: 'openai', multiplier: 1 },
  { id: '11', groupName: '表单分组乙', platform: 'openai', multiplier: 1 },
  { id: '12', groupName: 'Gemini 分组', platform: 'gemini', multiplier: 1 },
  { id: '13', groupName: 'Kimi 分组甲', platform: 'kimi', multiplier: 1 },
  { id: '14', groupName: 'Kimi 分组乙', platform: 'kimi', multiplier: 1 },
  { id: '15', groupName: '智谱分组', platform: 'zhipu', multiplier: 1 },
  { id: '16', groupName: 'DeepSeek 分组', platform: 'deepseek', multiplier: 1 },
  { id: '17', groupName: 'MiniMax 分组', platform: 'minimax', multiplier: 1 },
  { id: '18', groupName: 'Anthropic 分组', platform: 'anthropic', multiplier: 1 },
  { id: '19', groupName: 'Antigravity 分组', platform: 'antigravity', multiplier: 1 },
]
const rows = (type = 'openai') => ({ items: [harness.rate ?? { ...rate, type }], total: 1, page: 1, pageSize: 10, totalPages: 1, types: ['openai', 'gemini'], platforms: ['sub2api'] })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const success = (req: RealConnectRequest) => ({
  workspaceAdminAccountId: 'workspace-c5', configurationStatus: 'confirmed',
  connection: {
    id: 'c'.repeat(32), upstreamSiteId: req.upstreamSiteId, upstreamGroupId: req.upstreamGroupId, upstreamGroupName: req.upstreamGroupName,
    adminAccountId: '101', adminAccountName: '本地记录名称', upstreamKeyId: '207', ownGroupIds: req.ownGroupIds,
    groupType: req.groupType, adminPlatform: 'sub2api', status: 'active', createdAt: '2026-10-07T00:00:00Z',
    ownGroupNames: ['主站确认甲', '主站确认乙'], pricingMappingEnabled: true, canDeleteRemote: true, provisioningMode: 'managed',
  },
  configuration: {
    observation: 'creation', adminAccountId: '101', name: '主站读回名称', platform: req.groupType,
    priority: req.accountSettings?.priority ?? 100, concurrency: req.accountSettings?.concurrency ?? 50,
    passthrough: req.accountSettings?.passthrough ?? false, poolMode: req.accountSettings?.poolMode ?? true,
    upstreamBillingProbeEnabled: req.accountSettings?.upstreamBillingProbeEnabled ?? true,
    ownGroups: req.ownGroupIds.map(id => ({ id, name: id === '10' ? '主站确认甲' : '主站确认乙' })),
    modelState: req.accountSettings?.passthrough ? 'not_required' : 'synced',
    models: req.accountSettings?.passthrough ? [] : ['live-c5-model-a', 'live-c5-model-b'],
  },
})
const wrappers: VueWrapper[] = []
const openView = async (type = 'openai') => {
  harness.rate = reactive({ ...rate, type })
  harness.listGroupRates.mockResolvedValue(rows(type))
  const wrapper = mount(GroupRatesView, { attachTo: document.body })
  wrappers.push(wrapper)
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text().trim() === '配置对接')!.trigger('click')
  await flushPromises()
  return wrapper
}
const dialog = (wrapper: VueWrapper) => wrapper.get('[role="dialog"]')
const chooseGroup = async (wrapper: VueWrapper, name = '表单分组甲') => {
  await dialog(wrapper).findAll('label').find(label => label.text().includes(name))!.get('input[type="checkbox"]').setValue(true)
}
const submit = async (wrapper: VueWrapper) => { await dialog(wrapper).get('form').trigger('submit'); await flushPromises() }
const submitButton = (wrapper: VueWrapper) => dialog(wrapper).get('button[type="submit"]')

beforeEach(() => {
  harness.currentAccount.value = { id: 'workspace-c5', displayName: 'C5 隔离工作区' }
  harness.platform = 'sub2api'
  harness.rate = null
  harness.availableGroups = [...groups]
  harness.requests.splice(0)
  harness.bindings.splice(0)
  harness.listGroupRates.mockReset().mockResolvedValue(rows())
  harness.importResponse = async (req: RealConnectRequest) => json(success(req))
  harness.mappingResponse = async () => json({ ownGroups: harness.availableGroups, mappings: [] })
  harness.connectionResponse = async () => json([])
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url === '/api/my-sites/real-connect') {
      const request = JSON.parse(String(options.body))
      harness.requests.push(request)
      return harness.importResponse(request)
    }
    if (url === '/api/my-sites/real-bind') { harness.bindings.push(JSON.parse(String(options.body))); return json({ connection: {} }) }
    if (url === '/api/my-sites/real-connections/check') return json({ checked: 0, active: 0, missing: 0 })
    if (url === '/api/my-sites/real-connections') return harness.connectionResponse()
    if (url === '/api/my-sites/mapping-options') return harness.mappingResponse()
    if (url.startsWith('/api/my-sites/upstream-keys?')) return json([{ id: '207', name: '既有 Key', status: 'active', groupId: '21', groupName: 'C5 上游组' }])
    if (url.startsWith('/api/my-sites/admin-resources?')) return json([{ id: '101', name: '既有主站账号', platform: 'openai', type: 'apikey', status: 'active', groupIds: ['10'] }])
    throw new Error('unexpected test endpoint')
  }))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

const familyHint = '这个上游分组走 OpenAI 兼容接口，可以导进 OpenAI、Kimi、智谱 GLM、DeepSeek、MiniMax 平台的主站分组。一次只能选同一个平台的分组，账号会创建成所选分组的平台。导入会在上游创建一个 Key、在主站创建一个账号。'
const selectionHint = '选的是要在主站创建成哪个平台的账号，下面只列出这个平台的主站分组。'
const modelHint = '导入时由主站同步上游可用模型，并写入模型白名单。'
const labels: Record<string, string> = { openai: 'OpenAI', kimi: 'Kimi', zhipu: '智谱 GLM', deepseek: 'DeepSeek', minimax: 'MiniMax' }
const groupInput = (wrapper: VueWrapper, name: string) => dialog(wrapper).findAll('label').find(label => label.text().includes(name))!.get('input[type="checkbox"]')
const typeSelect = (wrapper: VueWrapper) => dialog(wrapper).findAll('select').find(select => select.find('option[value="openai"]').exists())!
const passthrough = (wrapper: VueWrapper) => dialog(wrapper).find('[role="switch"][aria-label="透传"]')
const familyGroups = (wrapper: VueWrapper) => dialog(wrapper).findAll('label').filter(label => /分组|表单分组/.test(label.text()) && label.find('input[type="checkbox"]').exists())
const assertUnavailable = (wrapper: VueWrapper) => {
  expect(passthrough(wrapper).exists()).toBe(false)
  expect(dialog(wrapper).text()).toContain('此平台不支持透传设置。')
  expect(dialog(wrapper).text()).toContain(modelHint)
}
const assertNoDeveloperWords = (wrapper: VueWrapper) => {
  expect(dialog(wrapper).text()).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)
}

describe('C5.1 domestic group import component through real API contract', () => {
  it('uses the same compatibility decisions for every supported pair, case and empty input', () => {
    const platforms = ['openai', 'kimi', 'zhipu', 'deepseek', 'minimax', 'anthropic', 'gemini', 'antigravity']
    platforms.forEach((source, i) => platforms.forEach((target, j) => {
      expect(importPlatformsCompatible(source, target)).toBe(source === target || i < 5 && j < 5)
    }))
    expect(importPlatformsCompatible(' OPENAI ', ' KiMi ')).toBe(true)
    expect(importPlatformsCompatible('Gemini', ' KIMI ')).toBe(false)
    expect(importPlatformsCompatible('', 'kimi')).toBe(false)
    expect(importPlatformsCompatible('kimi', '')).toBe(false)
    expect(importPlatformsCompatible('grok', 'openai')).toBe(false)
  })

  it.each(['openai', 'kimi', 'zhipu', 'deepseek', 'minimax'])('lists all five compatible platforms for %s, locks other platforms and restores on deselection', async source => {
    const wrapper = await openView(source)
    expect(familyGroups(wrapper)).toHaveLength(7)
    expect(dialog(wrapper).text()).not.toContain('Anthropic 分组')
    expect(dialog(wrapper).text()).not.toContain('Gemini 分组')
    expect(dialog(wrapper).text()).not.toContain('Antigravity 分组')
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`上游分组类型：${labels[source]}`)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain('将创建的主站账号平台：选了主站分组后确定')
    expect(dialog(wrapper).get('[data-import-compatible-platforms]').text()).toBe(familyHint)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-platform-hint]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await submit(wrapper)
    expect(harness.requests).toHaveLength(0)
    await chooseGroup(wrapper, 'Kimi 分组甲')
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain('将创建的主站账号平台：Kimi')
    expect(dialog(wrapper).get('[data-import-platform-lock]').text()).toBe('已选 Kimi 平台的分组，其他平台的分组暂时不能选；取消已选的分组后可以改选。')
    expect(groupInput(wrapper, 'Kimi 分组乙').element).toHaveProperty('disabled', false)
    for (const name of ['表单分组甲', '智谱分组', 'DeepSeek 分组', 'MiniMax 分组']) {
      const input = groupInput(wrapper, name)
      expect(input.element).toHaveProperty('disabled', true)
      await input.trigger('change')
      expect(input.element).toHaveProperty('checked', false)
    }
    await chooseGroup(wrapper, 'Kimi 分组乙')
    await groupInput(wrapper, 'Kimi 分组甲').setValue(false)
    expect(groupInput(wrapper, '表单分组甲').element).toHaveProperty('disabled', true)
    await groupInput(wrapper, 'Kimi 分组乙').setValue(false)
    expect(groupInput(wrapper, '表单分组甲').element).toHaveProperty('disabled', false)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    assertNoDeveloperWords(wrapper)
  })

  it.each([
    ['kimi', 'Kimi 分组甲'], ['zhipu', '智谱分组'], ['deepseek', 'DeepSeek 分组'], ['minimax', 'MiniMax 分组'],
  ])('imports an OpenAI source into %s and directly closes after main-site confirmation', async (target, name) => {
    const wrapper = await openView()
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    await chooseGroup(wrapper, name)
    assertUnavailable(wrapper)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`将创建的主站账号平台：${labels[target]}`)
    assertNoDeveloperWords(wrapper)
    await submit(wrapper)
    expect(harness.requests).toHaveLength(1)
    expect(harness.requests[0]).toMatchObject({ groupType: target, accountSettings: { passthrough: false, priorityMode: 'automatic', priority: 100, concurrency: 50, poolMode: true, upstreamBillingProbeEnabled: true } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('导入已完成')
    expect(wrapper.text()).not.toContain('真实对接创建失败')
  })

  it.each(['anthropic', 'gemini', 'antigravity'])('preserves the %s same-platform group list and hides family text', async source => {
    const wrapper = await openView(source)
    const expected = source === 'anthropic' ? 'Anthropic 分组' : source === 'gemini' ? 'Gemini 分组' : 'Antigravity 分组'
    expect(familyGroups(wrapper)).toHaveLength(1)
    expect(familyGroups(wrapper)[0].text()).toContain(expected)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`上游分组类型：${source}`)
    expect(dialog(wrapper).find('[data-import-compatible-platforms]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    await chooseGroup(wrapper, expected)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`将创建的主站账号平台：${source}`)
    if (source === 'anthropic') expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    else assertUnavailable(wrapper)
    await submit(wrapper)
    expect(harness.requests[0].groupType).toBe(source)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('shows eight platform options for an untyped source, filters by the chosen platform and retains settings/removal text', async () => {
    const wrapper = await openView('')
    const select = typeSelect(wrapper)
    expect(select.findAll('option').filter(option => option.attributes('value') !== '').map(option => option.attributes('value'))).toEqual(['openai', 'anthropic', 'gemini', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax'])
    for (const [value, label] of Object.entries(labels)) expect(select.get(`option[value="${value}"]`).text()).toBe(label)
    expect(dialog(wrapper).get('[data-import-platform-hint]').text()).toBe(selectionHint)
    expect(dialog(wrapper).find('[data-import-platforms]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-compatible-platforms]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await submit(wrapper); expect(harness.requests).toHaveLength(0)
    await select.setValue('openai'); await chooseGroup(wrapper)
    await select.setValue('deepseek')
    expect(familyGroups(wrapper)).toHaveLength(1)
    expect(familyGroups(wrapper)[0].text()).toContain('DeepSeek 分组')
    expect(dialog(wrapper).text()).toContain('平台类型已变化，已移除不再匹配的主站分组选择。')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    assertUnavailable(wrapper)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    await chooseGroup(wrapper, 'DeepSeek 分组')
    assertNoDeveloperWords(wrapper)
    await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({ groupType: 'deepseek', ownGroupIds: ['16'], accountSettings: { passthrough: false } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it.each(['target-selection', 'untyped-dropdown', 'upstream-type-change'])('keeps passthrough off after becoming unavailable through %s and submits off', async route => {
    const wrapper = await openView(route === 'untyped-dropdown' ? '' : 'openai')
    if (route === 'untyped-dropdown') await typeSelect(wrapper).setValue('openai')
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    await passthrough(wrapper).trigger('click')
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('true')
    expect(dialog(wrapper).text()).not.toContain(modelHint)
    if (route === 'target-selection') await chooseGroup(wrapper, 'Kimi 分组甲')
    else if (route === 'untyped-dropdown') await typeSelect(wrapper).setValue('gemini')
    else { harness.rate.type = 'gemini'; await flushPromises() }
    assertUnavailable(wrapper)
    if (route === 'target-selection') await groupInput(wrapper, 'Kimi 分组甲').setValue(false)
    else if (route === 'untyped-dropdown') await typeSelect(wrapper).setValue('openai')
    else { harness.rate.type = 'openai'; await flushPromises() }
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    expect(dialog(wrapper).text()).toContain(modelHint)
    await chooseGroup(wrapper)
    await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({ groupType: 'openai', accountSettings: { passthrough: false } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('only shows platform-lock text when another compatible platform is actually present', async () => {
    harness.availableGroups = groups.filter(group => group.platform === 'kimi')
    const wrapper = await openView('kimi')
    await chooseGroup(wrapper, 'Kimi 分组甲')
    expect(dialog(wrapper).get('[data-import-compatible-platforms]').text()).toBe(familyHint)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    await submit(wrapper)
    expect(harness.requests[0].groupType).toBe('kimi')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('keeps NewAPI main-site channel selection outside expanded domestic import behavior', async () => {
    harness.platform = 'newapi'
    harness.importResponse = async () => json({ connection: {} })
    const wrapper = await openView('kimi')
    expect(dialog(wrapper).find('[data-import-platforms]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-compatible-platforms]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-platform-hint]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    expect(familyGroups(wrapper)).toHaveLength(groups.length)
    await chooseGroup(wrapper, 'Kimi 分组甲')
    const channel = dialog(wrapper).findAll('select').find(select => select.text().includes('请选择渠道类型'))!
    await channel.setValue('1'); await submit(wrapper)
    expect(harness.requests[0]).not.toHaveProperty('accountSettings')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

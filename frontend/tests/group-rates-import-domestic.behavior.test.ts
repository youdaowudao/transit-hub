// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import { realConnect } from '@/modules/admin/api/mySites'
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
  harness.currentAccount = ref({ id: 'workspace-c5', displayName: 'C5 隔离工作区', platform: 'sub2api' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ getDashboardAdminStatus: vi.fn(async () => ({ platform: harness.platform })) }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSiteOptions: vi.fn(async () => ({ workspaceAdminAccountId: harness.currentAccount.value.id, items: [] })) }))
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
  { id: '20', groupName: 'Grok 分组甲', platform: 'grok', multiplier: 1 },
  { id: '21', groupName: 'Grok 分组乙', platform: 'grok', multiplier: 1 },
]
const rows = (type = 'openai') => ({ workspaceAdminAccountId: harness.currentAccount.value.id, items: [harness.rate ?? { ...rate, type }], total: 1, page: 1, pageSize: 10, totalPages: 1, types: ['openai', 'gemini'], platforms: ['sub2api'] })
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
  harness.currentAccount.value = { id: 'workspace-c5', displayName: 'C5 隔离工作区', platform: 'sub2api' }
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
    if (url === '/api/my-sites/real-connections/check-scoped') return json({ workspaceAdminAccountId: harness.currentAccount.value.id, total: 0, confirmedConnectionIds: [], unconfirmedConnectionIds: [], skippedConnectionIds: [], changedConnectionIds: [], confirmedStatuses: {}, issues: [], storageUncertain: false })
    if (url === '/api/my-sites/real-connections/list-scoped') { const response = await harness.connectionResponse(); if (!response.ok) return response; return json({ workspaceAdminAccountId: harness.currentAccount.value.id, items: await response.json() }) }
    if (url === '/api/my-sites/mapping-options') return harness.mappingResponse()
    if (url.startsWith('/api/my-sites/upstream-keys?')) return json([{ id: '207', name: '既有 Key', status: 'active', groupId: '21', groupName: 'C5 上游组' }])
    if (url.startsWith('/api/my-sites/admin-resources?')) return json([{ id: '101', name: '既有主站账号', platform: 'openai', type: 'apikey', status: 'active', groupIds: ['10'] }])
    throw new Error('unexpected test endpoint')
  }))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.unstubAllGlobals(); document.body.innerHTML = '' })

const selectionHint = '默认按上游分类选择，你可以手动修改。这里只确定要创建的主站账号平台；点击导入后才会在上游创建 Key、在主站创建账号。'
const labels: Record<string, string> = { openai: 'OpenAI', anthropic: 'Anthropic', gemini: 'Gemini', antigravity: 'Antigravity', grok: 'Grok', kimi: 'Kimi', zhipu: '智谱 GLM', deepseek: 'DeepSeek', minimax: 'MiniMax' }
const names: Record<string, string> = { openai: '表单分组甲', anthropic: 'Anthropic 分组', gemini: 'Gemini 分组', antigravity: 'Antigravity 分组', grok: 'Grok 分组甲', kimi: 'Kimi 分组甲', zhipu: '智谱分组', deepseek: 'DeepSeek 分组', minimax: 'MiniMax 分组' }
const groupInput = (wrapper: VueWrapper, name: string) => dialog(wrapper).findAll('label').find(label => label.text().includes(name))!.get('input[type="checkbox"]')
const typeSelect = (wrapper: VueWrapper) => dialog(wrapper).get('[data-import-platform-select]')
const passthrough = (wrapper: VueWrapper) => dialog(wrapper).find('[role="switch"][aria-label="透传"]')
const assertNoDeveloperWords = (wrapper: VueWrapper) => expect(dialog(wrapper).text()).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)

describe('explicit platform selection through the import API contract', () => {
  it.each(Object.keys(labels))('defaults to classified %s and always permits all nine choices', async source => {
    const wrapper = await openView(source), select = typeSelect(wrapper)
    expect(select.element).toHaveProperty('value', source)
    expect(select.findAll('option').filter(o => o.attributes('value')).map(o => o.attributes('value'))).toEqual(['openai','anthropic','gemini','antigravity','grok','kimi','zhipu','deepseek','minimax'])
    expect(dialog(wrapper).get('[data-import-platform-hint]').text()).toBe(selectionHint)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`上游标注：${labels[source]}`)
    expect(dialog(wrapper).get('[data-import-platforms]').text()).toContain(`将创建的主站账号平台：${labels[source]}`)
    expect(dialog(wrapper).find('[data-import-compatible-platforms]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-platform-lock]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled',true)
    await chooseGroup(wrapper,names[source]); await submit(wrapper)
    expect(harness.requests[0].groupType).toBe(source)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
  it.each(Object.keys(labels))('imports a differently classified source as explicitly selected %s', async target => {
    const wrapper = await openView(target === 'openai' ? 'grok' : 'openai')
    await typeSelect(wrapper).setValue(target)
    for (const [platform,name] of Object.entries(names)) expect(dialog(wrapper).text().includes(name)).toBe(platform === target)
    await chooseGroup(wrapper,names[target]); assertNoDeveloperWords(wrapper); await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({ groupType:target, accountSettings:{passthrough:false,priority:100,concurrency:50,poolMode:true,upstreamBillingProbeEnabled:true} })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('真实对接创建失败')
  })
  it.each(['','opencode-go','unknown'])('requires a manual selection for unknown/unsupported source %s', async source => {
    const wrapper = await openView(source)
    expect(typeSelect(wrapper).element).toHaveProperty('value','')
    expect(dialog(wrapper).text()).toContain('请先选择主站账号平台。')
    await submit(wrapper); expect(harness.requests).toHaveLength(0)
    await typeSelect(wrapper).setValue('minimax'); await chooseGroup(wrapper,names.minimax); await submit(wrapper)
    expect(harness.requests[0].groupType).toBe('minimax')
  })
  it('clears mismatched targets, retains settings and platform on deselection, and resets on reopening', async () => {
    const wrapper = await openView('openai')
    await chooseGroup(wrapper,names.openai)
    await dialog(wrapper).get('#import-concurrency').setValue('123')
    await typeSelect(wrapper).setValue('kimi')
    expect(dialog(wrapper).text()).toContain('平台已修改，不符合新平台的已选分组已移除，请重新选择。')
    expect(submitButton(wrapper).element).toHaveProperty('disabled',true)
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value','123')
    await chooseGroup(wrapper,names.kimi); await chooseGroup(wrapper,'Kimi 分组乙')
    await groupInput(wrapper,names.kimi).setValue(false); await groupInput(wrapper,'Kimi 分组乙').setValue(false)
    expect(typeSelect(wrapper).element).toHaveProperty('value','kimi')
    await dialog(wrapper).findAll('button').find(b=>b.text().trim()==='取消')!.trigger('click')
    await wrapper.findAll('button').find(b=>b.text().trim()==='配置对接')!.trigger('click'); await flushPromises()
    expect(typeSelect(wrapper).element).toHaveProperty('value','openai')
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value','50')
  })
  it.each(['typed','untyped'])('never restores passthrough after platform becomes unsupported (%s)', async kind => {
    const wrapper = await openView(kind === 'typed' ? 'openai' : '')
    await typeSelect(wrapper).setValue('openai'); await passthrough(wrapper).trigger('click')
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('true')
    await typeSelect(wrapper).setValue('gemini'); expect(passthrough(wrapper).exists()).toBe(false)
    await typeSelect(wrapper).setValue('openai'); expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    await chooseGroup(wrapper); await submit(wrapper)
    expect(harness.requests[0].accountSettings?.passthrough).toBe(false)
  })
  it('does not overwrite explicit platform or groups when the source classification refreshes', async () => {
    const wrapper = await openView('openai'); await typeSelect(wrapper).setValue('kimi'); await chooseGroup(wrapper,names.kimi)
    harness.rate.type = 'gemini'; await flushPromises()
    expect(typeSelect(wrapper).element).toHaveProperty('value','kimi')
    expect(groupInput(wrapper,names.kimi).element).toHaveProperty('checked',true)
    await submit(wrapper); expect(harness.requests[0].groupType).toBe('kimi')
  })
  it('describes how to reload missing targets and discovers them after full page remount', async () => {
    harness.availableGroups = groups.filter(g=>g.platform!=='gemini')
    const first = await openView('gemini')
    expect(dialog(first).text()).toContain('主站暂无这个平台的分组，请先在主站创建分组，再刷新本页面并重新打开导入。')
    expect(submitButton(first).element).toHaveProperty('disabled',true)
    first.unmount(); wrappers.splice(wrappers.indexOf(first),1)
    harness.availableGroups=[...groups]
    const second=await openView('gemini'); await chooseGroup(second,names.gemini); await submit(second)
    expect(harness.requests[0].groupType).toBe('gemini')
  })
  it('preserves NewAPI main channel selection without accountSettings', async () => {
    harness.platform='newapi'; harness.currentAccount.value={id:'workspace-c5',platform:'newapi'}
    harness.importResponse=async()=>json({connection:{}})
    const wrapper=await openView('kimi')
    expect(dialog(wrapper).find('[data-import-platform-select]').exists()).toBe(false)
    await chooseGroup(wrapper,names.kimi)
    await dialog(wrapper).findAll('select').find(s=>s.text().includes('请选择渠道类型'))!.setValue('1'); await submit(wrapper)
    expect(harness.requests[0]).not.toHaveProperty('accountSettings'); expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
})

// Primary-agent Grok expectations fixed before changing production code.
describe('Grok critical RED import behavior', () => {
  const grokHint = '选择 Grok 后会创建 Grok 平台的主站账号；请在下面选择 Grok 分组，再点击“创建并对接”。这里不设置探活或答题。'
  it.each(['grok', ' GrOk '])('defaults classified %s and submits both Grok targets with default settings', async source => {
    const wrapper = await openView(source)
    expect(typeSelect(wrapper).element).toHaveProperty('value','grok')
    expect(typeSelect(wrapper).findAll('option').filter(o=>o.attributes('value')).map(o=>o.attributes('value'))).toEqual(['openai','anthropic','gemini','antigravity','grok','kimi','zhipu','deepseek','minimax'])
    expect(dialog(wrapper).text()).toContain('将创建的主站账号平台：Grok')
    expect(dialog(wrapper).text()).toContain(grokHint)
    expect(dialog(wrapper).text()).not.toContain('Kimi 分组甲')
    expect(passthrough(wrapper).exists()).toBe(false)
    await chooseGroup(wrapper,'Grok 分组甲'); await chooseGroup(wrapper,'Grok 分组乙')
    await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({groupType:'grok',ownGroupIds:['20','21'],accountSettings:{priorityMode:'automatic',priority:100,concurrency:50,passthrough:false,poolMode:true,upstreamBillingProbeEnabled:true}})
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
  it.each(['', 'unknown', 'anthropic'])('allows source %s to select Grok and clears incompatible targets while preserving settings', async source => {
    const wrapper=await openView(source)
    if(source==='anthropic') await chooseGroup(wrapper,'Anthropic 分组')
    await dialog(wrapper).get('#import-concurrency').setValue('73')
    await typeSelect(wrapper).setValue('grok')
    expect(dialog(wrapper).text()).toContain(grokHint)
    expect(submitButton(wrapper).element).toHaveProperty('disabled',true)
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value','73')
    await chooseGroup(wrapper,'Grok 分组甲'); await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({groupType:'grok',ownGroupIds:['20'],accountSettings:{concurrency:73,passthrough:false}})
  })
  it('turns passthrough off entering Grok and keeps it off on return; only Grok shows its hint', async () => {
    const wrapper=await openView('openai')
    expect(dialog(wrapper).text()).not.toContain(grokHint)
    await passthrough(wrapper).trigger('click')
    await typeSelect(wrapper).setValue('grok')
    expect(passthrough(wrapper).exists()).toBe(false)
    expect(dialog(wrapper).text()).toContain(grokHint)
    await typeSelect(wrapper).setValue('openai')
    expect(passthrough(wrapper).attributes('aria-checked')).toBe('false')
    expect(dialog(wrapper).text()).not.toContain(grokHint)
  })
})

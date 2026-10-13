// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import * as localeFns from '@/locales'
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
  dashboardFailure: false,
  bindingResponse: null as any,
  keyResponse: null as any,
  resourceResponse: null as any,
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ push: vi.fn(), replace: vi.fn(async () => undefined) }) }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  harness.currentAccount = ref({ id: 'workspace-c5', displayName: 'C5 隔离工作区', platform: 'sub2api' })
  return { useAdminAccounts: () => ({ currentAccount: harness.currentAccount }) }
})
vi.mock('@/modules/auth/api/auth', () => ({ getAccessToken: () => null, handleAuthExpired: vi.fn(), isUnauthorizedApiResponse: (status: number) => status === 401, authUnauthorizedErrorKey: 'auth.errors.unauthorized' }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ getDashboardAdminStatus: vi.fn(async () => { if (harness.dashboardFailure) throw new Error('dashboard failed'); return { platform: harness.platform } }) }))
vi.mock('@/modules/admin/api/upstream', () => ({ listUpstreamSites: vi.fn(async () => []) }))
vi.mock('@/modules/admin/api/groupRates', () => ({ listGroupRates: harness.listGroupRates, listAllGroupRates: vi.fn(async () => []), listGroupRateHistory: vi.fn(async () => []), updateGroupRateType: vi.fn() }))

const rate = { siteId: 'site-c5', siteName: 'C5 上游', groupId: '21', groupName: 'C5 上游组', type: 'openai', platform: 'sub2api', mapped: false, currentMultiplier: 1, delta: 0, deltaPercent: 0, deleted: false, updatedAt: '2026-10-07T00:00:00Z' }
const groups = [
  { id: '10', groupName: '表单分组甲', platform: 'openai', multiplier: 1 },
  { id: '11', groupName: '表单分组乙', platform: 'openai', multiplier: 1 },
  { id: '12', groupName: 'Gemini 分组', platform: 'gemini', multiplier: 1 },
]
const rows = (type = 'openai') => ({ items: [{ ...rate, type }], total: 1, page: 1, pageSize: 10, totalPages: 1, types: ['openai', 'gemini'], platforms: ['sub2api'] })
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
const failure = (changes: Record<string, unknown> = {}) => ({ message: 'admin.mySites.errors.importModelSyncFailed', stage: 'model_sync', cleanup: 'confirmed', retryAllowed: true, upstreamKeyId: '207', upstreamResourceName: 'C5-isolated-key', ...changes })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(next => { resolve = next }); return { resolve, promise } }
const wrappers: VueWrapper[] = []
const openView = async (type = 'openai') => {
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
  harness.dashboardFailure = false
  harness.bindingResponse = async () => json({ connection: {} })
  harness.keyResponse = async () => json([{ id: '207', name: '既有 Key', status: 'active', groupId: '21', groupName: 'C5 上游组' }])
  harness.resourceResponse = async () => json([{ id: '101', name: '既有主站账号', platform: 'openai', type: 'apikey', status: 'active', groupIds: ['10'] }])
  harness.requests.splice(0)
  harness.bindings.splice(0)
  harness.listGroupRates.mockReset().mockResolvedValue(rows())
  harness.importResponse = async (req: RealConnectRequest) => json(success(req))
  harness.mappingResponse = async () => json({ ownGroups: groups, mappings: [] })
  harness.connectionResponse = async () => json([])
  vi.stubGlobal('fetch', vi.fn(async (url: string, options: RequestInit = {}) => {
    if (url === '/api/my-sites/real-connect') {
      const request = JSON.parse(String(options.body))
      harness.requests.push(request)
      return harness.importResponse(request)
    }
    if (url === '/api/my-sites/real-bind') { harness.bindings.push(JSON.parse(String(options.body))); return harness.bindingResponse() }
    if (url === '/api/my-sites/real-connections/check') return json({ checked: 0, active: 0, missing: 0 })
    if (url === '/api/my-sites/real-connections') return harness.connectionResponse()
    if (url === '/api/my-sites/mapping-options') return harness.mappingResponse()
    if (url.startsWith('/api/my-sites/upstream-keys?')) return harness.keyResponse()
    if (url.startsWith('/api/my-sites/admin-resources?')) return harness.resourceResponse()
    throw new Error('unexpected test endpoint')
  }))
})
afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('C5 actual API to group-rates import component', () => {
  it.each(['configuration-status-array', 'connection-status-array'])('C5 RED rejects coerced top-level success enums as pending: %s', async variant => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result: any = success(req)
      if (variant === 'configuration-status-array') result.configurationStatus = ['confirmed']
      else result.connection.status = ['active']
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(true)
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })

  it('C5 RED does not confirm a coerced observation that bypasses creation constraints', async () => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result: any = success(req)
      result.configuration = { ...result.configuration, observation: ['creation'], priority: 0, concurrency: 2000, ownGroups: [], modelState: 'current_unrestricted', models: [] }
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('配置暂无法读取')
    expect(dialog(wrapper).get('[data-import-success]').text()).not.toContain('主站配置已确认')
    expect(dialog(wrapper).get('[data-import-success]').text()).not.toContain('当前未限制模型')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })

  it('shows all defaults and the Priority explanation, submits both selected groups once and closes after a confirmed creation', async () => {
    const wrapper = await openView()
    expect(dialog(wrapper).get('#import-priority').element).toHaveProperty('value', '100')
    expect(dialog(wrapper).get('[data-import-priority-explain]').text()).toBe('这里的“自动 / 人工”与自动答题无关，只决定导入时写入主站的 Priority 范围：自动填 10 及以上，人工填 1–9。导入后调度站是否探活、是否调整这个数字，以“分组健康”里该分组的策略和页面显示为准。')
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value', '50')
    expect(dialog(wrapper).get('[role="switch"][aria-label="透传"]').attributes('aria-checked')).toBe('false')
    expect(dialog(wrapper).get('[role="switch"][aria-label="Pool Mode"]').attributes('aria-checked')).toBe('true')
    expect(dialog(wrapper).get('[role="switch"][aria-label="自动探测上游声明倍率"]').attributes('aria-checked')).toBe('true')
    await chooseGroup(wrapper); await chooseGroup(wrapper, '表单分组乙')
    await submit(wrapper)
    expect(harness.requests).toHaveLength(1)
    expect(harness.requests[0]).toMatchObject({ ownGroupIds: ['10', '11'], groupType: 'openai', accountSettings: { priorityMode: 'automatic', priority: 100, concurrency: 50, passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('导入已完成')
    expect(wrapper.text()).not.toContain('列表刷新失败')
    expect(wrapper.text()).not.toContain('真实对接创建失败')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '配置对接')!.element).toHaveProperty('disabled', false)
  })

  it.each([
    { field: 'configurationStatus', value: ['unavailable'], outcome: 'pending' },
    { field: 'connection.status', value: ['missing'], outcome: 'pending' },
    { field: 'connection.groupType', value: ['openai'], outcome: 'pending' },
    { field: 'connection.adminPlatform', value: ['sub2api'], outcome: 'pending' },
    { field: 'configuration.observation', value: ['current'], outcome: 'unavailable' },
    { field: 'configuration.modelState', value: ['synced'], outcome: 'unavailable' },
    { field: 'configuration.platform', value: ['openai'], outcome: 'unavailable' },
    { field: 'configuration.observation', value: { value: 'creation' }, outcome: 'unavailable' },
  ])('keeps malformed $field in its safe $outcome outcome', async ({ field, value, outcome }) => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result: any = success(req)
      const [parent, member] = field.split('.')
      if (member) result[parent][member] = value
      else result[parent] = value
      if (field === 'configuration.observation' && Array.isArray(value) && value[0] === 'current') result.configuration.modelState = 'current_whitelist'
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    if (outcome === 'pending') {
      expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(true)
      expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
      expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    } else {
      expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
      const summary = dialog(wrapper).get('[data-import-success]').text()
      expect(summary).toContain('配置暂无法读取')
      expect(summary).not.toContain('主站配置已确认')
      expect(summary).not.toContain('当前白名单')
      expect(summary).not.toContain('已由主站同步')
      expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    }
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })

  it('edits manual and automatic Priority independently and sends all explicit off switches', async () => {
    const wrapper = await openView()
    await dialog(wrapper).get('#import-priority').setValue('345')
    await dialog(wrapper).findAll('button').find(button => button.text() === '人工')!.trigger('click')
    expect(dialog(wrapper).get('#import-priority').element).toHaveProperty('value', '1')
    await dialog(wrapper).get('#import-priority').setValue('7')
    await dialog(wrapper).findAll('button').find(button => button.text() === '自动')!.trigger('click')
    expect(dialog(wrapper).get('#import-priority').element).toHaveProperty('value', '345')
    await dialog(wrapper).findAll('button').find(button => button.text() === '人工')!.trigger('click')
    await dialog(wrapper).get('#import-priority').setValue('7')
    await dialog(wrapper).get('#import-concurrency').setValue('123')
    await dialog(wrapper).get('[aria-label="Pool Mode"]').trigger('click')
    await dialog(wrapper).get('[aria-label="自动探测上游声明倍率"]').trigger('click')
    await chooseGroup(wrapper); await submit(wrapper)
    expect(harness.requests[0].accountSettings).toEqual({ priorityMode: 'manual', priority: 7, concurrency: 123, passthrough: false, poolMode: false, upstreamBillingProbeEnabled: false })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it.each([['#import-priority', ''], ['#import-priority', '1.5'], ['#import-priority', '9'], ['#import-priority', '2147483648'], ['#import-concurrency', ''], ['#import-concurrency', '1.5'], ['#import-concurrency', '0'], ['#import-concurrency', '1001']])('blocks invalid numeric input %s=%s without sending a request', async (selector, value) => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get(selector).setValue(value)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    expect(dialog(wrapper).get(selector).attributes('aria-invalid')).toBe('true')
    await submit(wrapper)
    expect(harness.requests).toHaveLength(0)
  })

  it('requires an unknown platform, clears other-type selections and never restores hidden passthrough true', async () => {
    const wrapper = await openView('')
    expect(dialog(wrapper).text()).not.toContain('表单分组甲')
    const selector = dialog(wrapper).findAll('select').find(select => select.text().includes('请选择分组类型'))!
    await selector.setValue('openai'); await chooseGroup(wrapper)
    await dialog(wrapper).get('[aria-label="透传"]').trigger('click')
    expect(dialog(wrapper).get('[aria-label="透传"]').attributes('aria-checked')).toBe('true')
    await selector.setValue('gemini')
    expect(dialog(wrapper).find('[aria-label="透传"]').exists()).toBe(false)
    expect(dialog(wrapper).text()).toContain('不符合新平台的已选分组已移除')
    expect(dialog(wrapper).text()).toContain('同步失败仍可导入')
    await selector.setValue('openai')
    expect(dialog(wrapper).get('[aria-label="透传"]').attributes('aria-checked')).toBe('false')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await chooseGroup(wrapper); await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({ ownGroupIds: ['10'], accountSettings: { passthrough: false } })
  })

  it('keeps binding existing resources free of new account settings and resets settings on return', async () => {
    const wrapper = await openView()
    await dialog(wrapper).get('#import-concurrency').setValue('123')
    await dialog(wrapper).findAll('button').find(button => button.text().includes('使用已有资源'))!.trigger('click'); await flushPromises()
    expect(dialog(wrapper).find('#import-concurrency').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-priority-explain]').exists()).toBe(false)
    await dialog(wrapper).get('input[type="radio"]').setValue(true)
    await dialog(wrapper).get('#existing-admin-group').setValue('10'); await flushPromises()
    await dialog(wrapper).findAll('input[type="radio"]').at(-1)!.setValue(true)
    await submit(wrapper)
    expect(harness.bindings).toHaveLength(1)
    expect(harness.bindings[0]).not.toHaveProperty('accountSettings')
    expect(harness.requests).toHaveLength(0)
    await wrapper.findAll('button').find(button => button.text().trim() === '配置对接')!.trigger('click'); await flushPromises()
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value', '50')
  })

  it('locks duplicate submits, switches and every close path until completion', async () => {
    const pending = deferred<Response>(); harness.importResponse = () => pending.promise
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(dialog(wrapper).text()).toContain('正在导入并配置主站')
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('disabled', true)
    await dialog(wrapper).get('form').trigger('submit')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(harness.requests).toHaveLength(1)
    pending.resolve(json(success(harness.requests[0]))); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(harness.requests).toHaveLength(1)
  })

  it('C5 RED unlocks an explicitly retryable response after a real pending render without editing inputs', async () => {
    const response = deferred<Response>()
    harness.importResponse = () => response.promise
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('disabled', true)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
    response.resolve(json(failure({ reason: 'admin.upstream.errors.networkTimeout' }), 502))
    await flushPromises()
    expect(dialog(wrapper).find('[data-import-failure]').exists()).toBe(true)
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('disabled', false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', false)
    const operationId = harness.requests[0].operationId
    harness.importResponse = async (req: RealConnectRequest) => json(success(req))
    await submit(wrapper)
    expect(harness.requests).toHaveLength(2)
    expect(harness.requests[1].operationId).not.toBe(operationId)
  })

  it('propagates a real model-sync failure, confirmed cleanup and safe reason, allowing only an explicit new-operation retry', async () => {
    harness.importResponse = async () => json(failure({ reason: 'admin.upstream.errors.networkTimeout' }), 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('未创建主站账号')
    expect(dialog(wrapper).text()).toContain('本次资源清理已确认')
    expect(dialog(wrapper).text()).toContain('207')
    expect(dialog(wrapper).text()).toContain('C5-isolated-key')
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', false)
    const operationId = harness.requests[0].operationId
    harness.importResponse = async (req: RealConnectRequest) => json(success(req))
    await submit(wrapper)
    expect(harness.requests).toHaveLength(2)
    expect(harness.requests[1].operationId).not.toBe(operationId)
  })

  it.each(['retained', 'pending'])('retains real failure resource IDs and blocks retry for cleanup=%s', async cleanup => {
    harness.importResponse = async () => json(failure({ cleanup, retryAllowed: false, adminResourceId: '101', reason: 'admin.connectionHealth.errors.sub2apiGroupLastUsable', groupId: '10', groupName: '受保护组' }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('主站账号 101')
    expect(dialog(wrapper).text()).toContain('上游 Key 编号 207')
    expect(dialog(wrapper).text()).toContain('受保护分组：受保护组（10）')
    expect(dialog(wrapper).text()).toContain('最后一个可用账号')
    expect(dialog(wrapper).text()).toContain('勿再次导入')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await submit(wrapper); expect(harness.requests).toHaveLength(1)
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
  })

  it('retains only the known generated name when an upstream Key ID was never confirmed', async () => {
    harness.importResponse = async () => json(failure({ message: 'admin.mySites.errors.importUpstreamKeyFailed', stage: 'upstream_key', cleanup: 'retained', retryAllowed: false, upstreamKeyId: '' }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('C5-isolated-key')
    expect(dialog(wrapper).text()).toContain('主站账号 尚未取得')
    expect(dialog(wrapper).text()).toContain('上游 Key 编号 尚未取得')
    expect(dialog(wrapper).text()).not.toContain('清理已确认')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
  })

  it.each(['network', 'read', 'malformed', 'missing-contract', 'conflicting-retry', 'bad-id', 'wrong-workspace', 'wrong-platform', 'missing-connection', 'invalid-status', 'old-backend'])('keeps %s without a trustworthy completion or retry contract pending', async variant => {
    harness.importResponse = async (req: RealConnectRequest) => {
      if (variant === 'network') throw new TypeError('network disconnected')
      if (variant === 'read') return { ok: true, status: 200, text: async () => { throw new TypeError('response disconnected') } }
      if (variant === 'malformed') return new Response('{', { status: 502 })
      if (variant === 'missing-contract') return json({ message: 'admin.mySites.errors.importModelSyncFailed' }, 502)
      if (variant === 'conflicting-retry') return json(failure({ cleanup: 'retained', retryAllowed: true }), 409)
      const result = success(req)
      if (variant === 'bad-id') result.connection.id = '..'
      if (variant === 'wrong-workspace') result.workspaceAdminAccountId = 'workspace-other'
      if (variant === 'wrong-platform') result.connection.groupType = 'gemini'
      if (variant === 'missing-connection') return json({ ...result, connection: undefined })
      if (variant === 'invalid-status') result.configurationStatus = 'not_applicable'
      if (variant === 'old-backend') return json({ connection: result.connection })
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-pending]').text()).toContain('勿再次导入')
    expect(dialog(wrapper).text()).toContain(harness.requests[0].operationId)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
    await submit(wrapper); expect(harness.requests).toHaveLength(1)
  })

  it('preserves completed facts with unavailable configuration and keeps list-refresh failures separate', async () => {
    harness.importResponse = async (req: RealConnectRequest) => json({ ...success(req), configuration: { priority: 100 } })
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.connectionResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('导入已完成，配置暂无法读取')
    expect(dialog(wrapper).get('[data-import-refresh-error]').text()).toContain('导入结果已返回，但列表刷新失败')
    expect(dialog(wrapper).text()).not.toContain('真实对接创建失败')
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); expect(harness.requests).toHaveLength(1)
  })

  it.each(['mapping', 'connections', 'rates'])('closes after a confirmed creation and reports %s refresh failure on the page without claiming import failure', async source => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    if (source === 'mapping') harness.mappingResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    if (source === 'connections') harness.connectionResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    if (source === 'rates') harness.listGroupRates.mockRejectedValueOnce(new Error('admin.groupRates.errors.request'))
    await submit(wrapper)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('导入结果已返回，但列表刷新失败；请刷新列表查看。不要因此再次导入。')
    expect(wrapper.text()).toContain('不要因此再次导入')
    expect(wrapper.text()).not.toContain('真实对接创建失败')
    expect(harness.requests).toHaveLength(1)
  })

  it('closes right after a confirmed creation while the list refreshes in the background', async () => {
    const pendingRows = deferred<ReturnType<typeof rows>>()
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.listGroupRates.mockImplementationOnce(() => pendingRows.promise)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    pendingRows.resolve(rows()); await flushPromises()
    expect(wrapper.text()).toContain('C5 上游组')
    expect(wrapper.text()).not.toContain('列表刷新失败')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '配置对接')!.element).toHaveProperty('disabled', false)
    expect(harness.requests).toHaveLength(1)
  })

  it('keeps all close and submit controls frozen while independent refreshes are still loading for a result that stays open', async () => {
    const pendingRows = deferred<ReturnType<typeof rows>>()
    harness.importResponse = async (req: RealConnectRequest) => json({ ...success(req), configuration: { priority: 100 } })
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.listGroupRates.mockImplementationOnce(() => pendingRows.promise)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    const done = dialog(wrapper).findAll('button').find(button => button.text() === '完成')!
    expect(done.element).toHaveProperty('disabled', true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    await dialog(wrapper).get('form').trigger('submit'); expect(harness.requests).toHaveLength(1)
    pendingRows.resolve(rows()); await flushPromises()
    expect(done.element).toHaveProperty('disabled', false)
  })

  it('submits enabled passthrough and closes after the confirmed creation', async () => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('[aria-label="透传"]').trigger('click'); await submit(wrapper)
    expect(harness.requests[0].accountSettings?.passthrough).toBe(true)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('keeps NewAPI main-site requests on the original settings-free API branch', async () => {
    harness.platform = 'newapi'
    harness.currentAccount.value = { id: 'workspace-c5', platform: 'newapi' }
    harness.importResponse = async () => json({ connection: {} })
    const wrapper = await openView(); await chooseGroup(wrapper)
    expect(dialog(wrapper).find('#import-concurrency').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-priority-explain]').exists()).toBe(false)
    const channelSelect = dialog(wrapper).findAll('select').find(select => select.text().includes('请选择渠道类型'))!
    await channelSelect.setValue('1'); await submit(wrapper)
    expect(harness.requests).toHaveLength(1)
    expect(harness.requests[0]).not.toHaveProperty('accountSettings')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it.each(['missing-switch', 'invalid-model-state', 'empty-live-models', 'duplicate-groups', 'wildcard-live-model', 'wrong-account'])('preserves completed binding but never claims confirmed invalid configuration: %s', async variant => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = success(req)
      if (variant === 'missing-switch') return json({ ...result, configuration: { ...result.configuration, poolMode: undefined } })
      if (variant === 'invalid-model-state') result.configuration.modelState = 'current_whitelist'
      if (variant === 'empty-live-models') result.configuration.models = []
      if (variant === 'duplicate-groups') result.configuration.ownGroups.push(result.configuration.ownGroups[0])
      if (variant === 'wildcard-live-model') result.configuration.models = ['model-*']
      if (variant === 'wrong-account') result.configuration.adminAccountId = '102'
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('配置暂无法读取')
    expect(dialog(wrapper).get('[data-import-success]').text()).not.toContain('主站配置已确认')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
  })

  it.each(['unknown-message', 'unknown-stage', 'missing-retry', 'wrong-id-type', 'resource-not-needed', 'confirmed-retry-409'])('does not trust malformed failure contracts: %s', async variant => {
    const response = failure()
    if (variant === 'unknown-message') response.message = 'untrusted raw upstream text'
    if (variant === 'unknown-stage') response.stage = 'unknown'
    if (variant === 'missing-retry') delete (response as Partial<typeof response>).retryAllowed
    if (variant === 'wrong-id-type') (response as any).upstreamKeyId = 207
    if (variant === 'resource-not-needed') response.cleanup = 'not_needed'
    harness.importResponse = async () => json(response, variant === 'confirmed-retry-409' ? 409 : 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(true)
    expect(dialog(wrapper).find('[data-import-failure]').exists()).toBe(false)
    expect(dialog(wrapper).text()).not.toContain('untrusted raw upstream text')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
  })

  it.each(['current_whitelist', 'current_unrestricted', 'not_required'])('shows replay current configuration %s without claiming fresh model sync', async state => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = success(req)
      result.configuration = { ...result.configuration, observation: 'current', priority: 0, concurrency: 2000, ownGroups: [{ id: '12', name: '现有分组' }], modelState: state, passthrough: state === 'not_required', models: state === 'current_whitelist' ? ['alias-*'] : [] }
      result.connection.upstreamGroupName = '主站返回的新分组名称'
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('主站当前配置')
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('现有分组（12）')
    expect(dialog(wrapper).text()).not.toContain('已由主站同步并保存')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
  })

  it.each(['success', 'failure'])('ignores %s arriving after workspace switch and does not refresh the new workspace', async outcome => {
    const pending = deferred<Response>(); harness.importResponse = () => pending.promise
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    const calls = harness.listGroupRates.mock.calls.length
    harness.currentAccount.value = { id: 'workspace-new', displayName: '新工作区', platform: 'sub2api' }
    await flushPromises()
    pending.resolve(outcome === 'success' ? json(success(harness.requests[0])) : json(failure(), 502)); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('主站读回名称')
    expect(wrapper.text()).not.toContain('主站同步上游可用模型失败')
    expect(harness.listGroupRates).toHaveBeenCalledTimes(calls)
  })

  it('ignores a completed response after page unmount', async () => {
    const pending = deferred<Response>(); harness.importResponse = () => pending.promise
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    const calls = harness.listGroupRates.mock.calls.length
    wrapper.unmount(); wrappers.splice(wrappers.indexOf(wrapper), 1)
    pending.resolve(json(success(harness.requests[0]))); await flushPromises()
    expect(harness.listGroupRates).toHaveBeenCalledTimes(calls)
  })

  it.each(['workspace', 'same-id-platform'])('keeps an in-flight post-success refresh from replacing rows after %s changes', async change => {
    const pendingRows = deferred<ReturnType<typeof rows>>()
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.listGroupRates.mockImplementationOnce(() => pendingRows.promise)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    harness.currentAccount.value = change === 'workspace'
      ? { id: 'workspace-new', displayName: '新工作区', platform: 'sub2api' }
      : { id: 'workspace-c5', displayName: 'C5 隔离工作区', platform: 'newapi' }
    pendingRows.resolve({ ...rows(), items: [{ ...rate, groupName: '不得出现的迟到旧工作区分组' }] }); await flushPromises()
    expect(wrapper.text()).not.toContain('不得出现的迟到旧工作区分组')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('reports initial group loading failures without enabling import', async () => {
    harness.mappingResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    const wrapper = await openView()
    expect(dialog(wrapper).text()).toContain('主站分组加载失败')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    expect(harness.requests).toHaveLength(0)
  })

  it('preserves non-sensitive connection response fields while discarding credentials and unrelated data', async () => {
    const request: RealConnectRequest = { upstreamSiteId: 'site-c5', upstreamGroupId: '21', upstreamGroupName: '  C5 上游组  ', groupType: 'openai', ownGroupIds: ['10'], accountSettings: { priorityMode: 'automatic', priority: 100, concurrency: 50, passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true } }
    harness.importResponse = async (req: RealConnectRequest) => { const result = success(req); result.connection.upstreamGroupName = req.upstreamGroupName.trim(); return json({ ...result, privateMarker: 'discarded', connection: { ...result.connection, upstreamKey: 'discarded', credentials: { privateMarker: 'discarded' } } }) }
    const result = await realConnect(request, 'workspace-c5')
    expect(result.connection).toMatchObject({ ownGroupNames: ['主站确认甲', '主站确认乙'], provisioningMode: 'managed', canDeleteRemote: true, pricingMappingEnabled: true })
    expect(result.connection).not.toHaveProperty('upstreamKey')
    expect(result.connection).not.toHaveProperty('credentials')
    expect(result).not.toHaveProperty('privateMarker')
    expect(result.configurationStatus).toBe('confirmed')
  })
})


describe('approved import selection regression', () => {
  it('RED keeps the platform selector available for classified sources', async () => {
    const wrapper = await openView()
    const select = dialog(wrapper).find('select')
    expect(select.exists()).toBe(true)
    expect(select.element).toHaveProperty('value', 'openai')
    expect(select.findAll('option').filter(o => o.attributes('value'))).toHaveLength(8)
    await chooseGroup(wrapper)
    await select.setValue('gemini')
    expect(dialog(wrapper).text()).not.toContain('表单分组甲')
    await chooseGroup(wrapper, 'Gemini 分组')
    await submit(wrapper)
    expect(harness.requests[0]).toMatchObject({ groupType: 'gemini', ownGroupIds: ['12'], accountSettings: { passthrough: false } })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })
  it('RED dashboard failure cannot remove the Sub2API safety contract', async () => {
    harness.dashboardFailure = true
    harness.importResponse = async () => json(failure({ cleanup: 'retained', retryAllowed: false }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(harness.requests[0]).toHaveProperty('accountSettings')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(true)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await submit(wrapper); expect(harness.requests).toHaveLength(1)
  })
  it.each([null, { id: 'workspace-c5', platform: '' }, { id: 'workspace-c5', platform: 'grok' }])('RED unknown main information forbids all creation: %j', async account => {
    harness.currentAccount.value = account
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(harness.requests).toHaveLength(0)
    expect(dialog(wrapper).text()).toContain('当前主站信息未就绪')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
  })
  it('RED busy received by binding locks saving and mode changes', async () => {
    harness.bindingResponse = async () => json({ message: 'admin.mySites.errors.importInProgress' }, 409)
    const wrapper = await openView()
    await dialog(wrapper).findAll('button').find(b => b.text().includes('使用已有资源'))!.trigger('click'); await flushPromises()
    await dialog(wrapper).get('input[type="radio"][value="207"]').setValue(true)
    await dialog(wrapper).get('select').setValue('10'); await flushPromises()
    await dialog(wrapper).get('input[type="radio"][value="101"]').setValue(true)
    await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('这个上游分组正在导入或绑定')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    const automatic = dialog(wrapper).findAll('button').find(b => b.text().includes('自动创建资源'))!
    expect(automatic.element).toHaveProperty('disabled', true)
    await automatic.trigger('click'); await submit(wrapper)
    expect(harness.bindings).toHaveLength(1); expect(harness.requests).toHaveLength(0)
  })
})


describe('binding response isolation and failure display', () => {
  const setupBinding = async (wrapper: VueWrapper) => {
    await dialog(wrapper).findAll('button').find(b=>b.text().includes('使用已有资源'))!.trigger('click'); await flushPromises()
    await dialog(wrapper).get('input[type="radio"][value="207"]').setValue(true)
    await dialog(wrapper).get('#existing-admin-group').setValue('10'); await flushPromises()
    await dialog(wrapper).get('input[type="radio"][value="101"]').setValue(true)
  }
  it.each(['busy','success'])('ignores stale binding %s after workspace changes', async result => {
    const response=deferred<Response>(); harness.bindingResponse=()=>response.promise
    const wrapper=await openView(); await setupBinding(wrapper); await submit(wrapper)
    expect(harness.bindings).toHaveLength(1)
    harness.currentAccount.value={id:'other-workspace',platform:'sub2api'}; await flushPromises()
    await wrapper.findAll('button').find(b=>b.text().trim()==='配置对接')!.trigger('click'); await flushPromises()
    response.resolve(result==='busy'?json({message:'admin.mySites.errors.importInProgress'},409):json({connection:{}})); await flushPromises()
    expect(dialog(wrapper).text()).not.toContain('这个上游分组正在导入或绑定')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).get('#import-priority').element).toHaveProperty('disabled',false)
  })
  it('invalidates the dialog when the same workspace changes platform', async () => {
    const wrapper=await openView()
    harness.currentAccount.value={id:'workspace-c5',platform:'newapi'}; await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(harness.requests).toHaveLength(0)
  })
  it('ignores stale Key loading after returning to automatic creation', async () => {
    const response=deferred<Response>(); harness.keyResponse=()=>response.promise
    const wrapper=await openView()
    await dialog(wrapper).findAll('button').find(b=>b.text().includes('使用已有资源'))!.trigger('click')
    await dialog(wrapper).findAll('button').find(b=>b.text().includes('自动创建资源'))!.trigger('click'); await flushPromises()
    response.resolve(json([{id:'old-key',name:'过期Key',status:'active'}])); await flushPromises()
    expect(dialog(wrapper).text()).not.toContain('过期Key')
    expect(dialog(wrapper).get('[data-import-platform-select]').element).toHaveProperty('value','openai')
  })
  it('shows connectionExists distinctly and keeps other binding failures readable', async () => {
    harness.bindingResponse=async()=>json({message:'admin.mySites.errors.connectionExists'},409)
    const wrapper=await openView(); await setupBinding(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('已经存在')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled',false)
  })
  it.each(['importInProgress','importSourceGroupUnavailable','importGroupsLoadFailed','request'])('shows the new trusted failure %s in Chinese with safe retry semantics', async message => {
    const blocked=['importInProgress','importSourceGroupUnavailable'].includes(message)
    harness.importResponse=async()=>json(failure({message:'admin.mySites.errors.'+message,stage:message==='importSourceGroupUnavailable'?'persistence':'validation',cleanup:blocked?'retained':'not_needed',retryAllowed:!blocked,upstreamKeyId:blocked?'207':undefined}),blocked?409:502)
    const wrapper=await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).not.toContain('admin.mySites.errors.'+message)
    expect(submitButton(wrapper).element).toHaveProperty('disabled',blocked)
  })
})


it('falls back to readable failure text when a trusted translation is unavailable', async () => {
  const original = localeFns.te
  vi.spyOn(localeFns,'te').mockImplementation(key => key==='admin.mySites.errors.importGroupsLoadFailed'?false:original(key))
  harness.importResponse=async()=>json({message:'admin.mySites.errors.importGroupsLoadFailed',stage:'validation',cleanup:'not_needed',retryAllowed:true},502)
  const wrapper=await openView();await chooseGroup(wrapper);await submit(wrapper)
  expect(dialog(wrapper).text()).toContain('真实对接创建失败')
  expect(dialog(wrapper).text()).not.toContain('admin.mySites.errors.importGroupsLoadFailed')
  expect(submitButton(wrapper).element).toHaveProperty('disabled',false)
})


describe('import diagnostics preserve existing safety decisions', () => {
  it.each(['not-array', 'too-many', 'bad-stage', 'bad-target', 'array-target', 'bad-message', 'bad-reason', 'bad-status', 'bad-detail'])('drops illegal diagnostic %s while retaining trusted retry result', async mode => {
    let diagnostics: any = [{ stage: 'upstream_key', target: 'upstream', message: 'admin.mySites.errors.importUpstreamKeyFailed', detail: '上游明确拒绝创建' }]
    if (mode === 'not-array') diagnostics = 'unsafe-raw-details'
    if (mode === 'too-many') diagnostics = Array(4).fill(diagnostics[0])
    if (mode === 'bad-stage') diagnostics[0].stage = 'injected'
    if (mode === 'bad-target') diagnostics[0].target = 'injected'
    if (mode === 'array-target') diagnostics[0].target = ['upstream']
    if (mode === 'bad-message') diagnostics[0].message = 'injected.raw.secret'
    if (mode === 'bad-reason') diagnostics[0].reason = 'injected.raw.secret'
    if (mode === 'bad-status') diagnostics[0].httpStatus = 403.5
    if (mode === 'bad-detail') diagnostics[0].detail = 'unsafe-raw-details\u0000'
    harness.importResponse = async () => json(failure({ message: 'admin.mySites.errors.importUpstreamKeyFailed', stage: 'upstream_key', cleanup: 'not_needed', retryAllowed: true, upstreamKeyId: undefined, upstreamResourceName: undefined, diagnostics }), 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('详细原因无法读取')
    expect(dialog(wrapper).text()).toContain('本次未留下待处理资源')
    expect(dialog(wrapper).text()).not.toContain('unsafe-raw-details')
    expect(dialog(wrapper).text()).not.toContain('injected.raw.secret')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', false)
  })

  it('keeps trusted 401 preflight failure and original auth expiry handling', async () => {
    const { handleAuthExpired } = await import('@/modules/auth/api/auth')
    vi.mocked(handleAuthExpired).mockClear()
    harness.importResponse = async () => json({ message: 'auth.errors.unauthorized', stage: 'validation', cleanup: 'not_needed', retryAllowed: true, diagnostics: [{ stage: 'validation', target: 'local', message: 'auth.errors.unauthorized', detail: '登录已失效，本次未创建账号或 Key' }] }, 401)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(handleAuthExpired).toHaveBeenCalledTimes(1)
    expect(dialog(wrapper).text()).toContain('本次未创建账号或 Key')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', false)
  })

  it('rejects a raw 401 as unknown outcome while still expiring auth', async () => {
    harness.importResponse = async () => json({ message: 'auth.errors.unauthorized' }, 401)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(true)
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    expect(dialog(wrapper).text()).not.toContain('本次未创建账号或 Key')
  })

  it.each(['upstreamInsufficientBalance', 'upstreamKeyQuotaExhausted', 'upstreamKeyExpired', 'announcementAckRequired'])('shows a concrete next action for structured reason %s', async reason => {
    harness.importResponse = async () => json(failure({ diagnostics: [{ stage: 'upstream_key', target: 'upstream', message: 'admin.mySites.errors.importUpstreamKeyFailed', reason: 'admin.upstream.errors.' + reason, httpStatus: 200, detail: '上游给出了明确拒绝原因' }] }), 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const text = dialog(wrapper).text()
    expect(text).toContain('对端 HTTP 状态：200')
    expect(text).toContain('下一步')
    expect(text).toContain('上游')
    expect(text).not.toContain('admin.upstream.errors.' + reason)
  })
})

it('clears a completed warning when reopening and ignores an old warning after workspace switch', async () => {
  harness.importResponse = async (req: RealConnectRequest) => json(warningSuccess(req))
  const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
  expect(dialog(wrapper).text()).toContain('导入成功，模型未同步')
  await dialog(wrapper).findAll('button').find(button => button.text().trim() === '完成')!.trigger('click')
  await wrapper.findAll('button').find(button => button.text().trim() === '配置对接')!.trigger('click')
  await flushPromises()
  expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
  expect(dialog(wrapper).find('[data-import-diagnostics]').exists()).toBe(false)
  const pending = deferred<Response>()
  harness.importResponse = async () => pending.promise
  await chooseGroup(wrapper)
  await dialog(wrapper).get('form').trigger('submit')
  await flushPromises()
  const oldRequest = harness.requests[harness.requests.length - 1]!
  harness.currentAccount.value = { id: 'new-workspace', platform: 'sub2api' }
  await flushPromises()
  pending.resolve(json(warningSuccess(oldRequest)))
  await flushPromises()
  expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  expect(wrapper.find('[data-import-diagnostics]').exists()).toBe(false)
})

describe('R1 concentrated import display regressions', () => {
  it.each(['main', 'upstream'] as const)('keeps the actual %s request target and safe proxy detail without shared reason guesses', async target => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = warningSuccess(req)
      if (target === 'main') result.diagnostics[0] = { ...modelWarningDiagnostic, reason: 'admin.upstream.errors.upstreamServerError', httpStatus: 502, detail: '主站返回：上游返回 HTTP 403，请查看该步骤记录' }
      else return json(failure({ message: 'admin.mySites.errors.importUpstreamKeyFailed', stage: 'upstream_key', cleanup: 'not_needed', retryAllowed: true, upstreamKeyId: undefined, diagnostics: [{ stage: 'upstream_key', target, message: 'admin.mySites.errors.importUpstreamKeyFailed', reason: 'admin.upstream.errors.forbidden', httpStatus: 403, detail: '上游不允许创建 Key' }] }), 502)
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const text = dialog(wrapper).text()
    expect(text).not.toMatch(/防护拦截|限制了来源 IP|上游服务异常|连接上游超时/)
    if (target === 'main') {
      expect(text).toContain('主站返回了服务错误')
      expect(text).toContain('对端 HTTP 状态：502')
      expect(text).toContain('上游返回 HTTP 403')
    } else {
      expect(text).toContain('上游站点拒绝了请求')
      expect(text).toContain('上游不允许创建 Key')
    }
  })

  it('does not let a secondary model warning become the title of a blocking model-stage failure', async () => {
    harness.importResponse = async () => json(failure({ diagnostics: [
      { stage: 'model_sync', target: 'main', message: 'admin.mySites.errors.importModelSyncFailed', detail: 'invalid\u0000initial' },
      { ...modelWarningDiagnostic },
    ] }), 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[role="alert"]').text()).toContain('未创建主站账号')
    expect(dialog(wrapper).text()).toContain(modelWarningDiagnostic.detail)
    expect(dialog(wrapper).text()).toContain('详细原因无法读取')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', false)
  })

  it('keeps passthrough clean-success automatic close and the explicit import refresh warning', async () => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('[aria-label="透传"]').trigger('click')
    harness.connectionResponse = async () => { throw new Error('synthetic refresh failure') }
    await submit(wrapper)
    expect(harness.requests[0]?.accountSettings?.passthrough).toBe(true)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('导入结果已返回，但列表刷新失败')
    expect(wrapper.text()).toContain('不要因此再次导入')
    expect(harness.requests).toHaveLength(1)
  })

  it('does not publish an old clean-success refresh warning after a workspace change', async () => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    const pendingRefresh = deferred<Response>()
    harness.connectionResponse = async () => pendingRefresh.promise
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    harness.currentAccount.value = { id: 'new-workspace', platform: 'sub2api' }
    await flushPromises()
    pendingRefresh.resolve(json({ message: 'admin.mySites.errors.request' }, 500))
    await flushPromises()
    expect(wrapper.text()).not.toContain('导入结果已返回，但列表刷新失败')
    expect(harness.requests).toHaveLength(1)
  })
})

it('keeps a missing import-only reason translation readable without using the shared speculative wording', async () => {
  const original = localeFns.te
  vi.spyOn(localeFns, 'te').mockImplementation(key => key === 'admin.groupRates.connect.importResult.reasons.forbidden' ? false : original(key))
  harness.importResponse = async (req: RealConnectRequest) => json(warningSuccess(req))
  const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
  expect(dialog(wrapper).text()).toContain('未取得可安全显示的具体原因')
  expect(dialog(wrapper).text()).toContain(modelWarningDiagnostic.detail)
  expect(dialog(wrapper).text()).not.toContain('admin.groupRates.connect.importResult.reasons.forbidden')
  expect(dialog(wrapper).text()).not.toMatch(/防护拦截|限制了来源 IP/)
})

// Primary-agent key regression contract for the model-warning change.
// Implementers must preserve these business expectations.
const modelWarningDiagnostic = {
  stage: 'model_sync', target: 'main', message: 'admin.mySites.errors.importModelSyncWarning',
  reason: 'admin.upstream.errors.forbidden', httpStatus: 403,
  detail: '主站返回：请求被拒绝，请检查接口权限',
}
const warningSuccess = (req: RealConnectRequest) => {
  const result: any = success(req)
  result.configuration.modelState = 'not_synced'
  result.configuration.models = []
  result.diagnostics = [{ ...modelWarningDiagnostic }]
  return result
}

describe('model warning primary-agent RED business contract', () => {
  it('R1 RED keeps an old failure reason neutral when optional diagnostics are absent', async () => {
    harness.importResponse = async () => json(failure({ reason: 'admin.upstream.errors.forbidden' }), 502)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).not.toMatch(/防护拦截|限制了来源 IP/)
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
    expect(dialog(wrapper).text()).toContain('未创建主站账号')
    expect(dialog(wrapper).text()).toContain('可重新导入')
  })
  it.each([
    ['admin.upstream.errors.forbidden', 403],
    ['admin.upstream.errors.upstreamServerError', 502],
    ['admin.upstream.errors.networkTimeout', undefined],
  ])('R1 RED shows a neutral main-site cause without inferring protection or upstream failure: %s', async (reason, httpStatus) => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = warningSuccess(req)
      result.diagnostics[0] = { ...modelWarningDiagnostic, reason, httpStatus, detail: '主站模型同步没有返回可用结果' }
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const text = dialog(wrapper).text()
    expect(text).toContain('主站')
    expect(text).not.toMatch(/防护拦截|限制了来源 IP|上游服务异常|连接上游超时/)
    expect(text).not.toContain('余额不足，请充值')
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    expect(harness.requests).toHaveLength(1)
  })

  it('R1 RED preserves the initial failure title when its optional diagnostic is invalid', async () => {
    harness.importResponse = async () => json(failure({
      message: 'admin.mySites.errors.importConfigurationMismatch', stage: 'configuration_check', cleanup: 'retained', retryAllowed: false,
      adminResourceId: '101', diagnostics: [
        { stage: 'configuration_check', target: 'main', message: 'admin.mySites.errors.importConfigurationMismatch', detail: 'invalid\u0000initial' },
        { ...modelWarningDiagnostic },
        { stage: 'account_cleanup', target: 'main', message: 'admin.mySites.errors.compensationPendingVerification', detail: '主站不允许删除当前账号' },
      ],
    }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const primary = dialog(wrapper).get('[role="alert"]').text()
    expect(primary).toContain('主站保存的账号配置与请求不一致')
    expect(primary).not.toContain('模型同步失败')
    expect(dialog(wrapper).text()).toContain(modelWarningDiagnostic.detail)
    expect(dialog(wrapper).text()).toContain('后续清理失败')
    expect(dialog(wrapper).text()).toContain('详细原因无法读取')
    expect(dialog(wrapper).text()).not.toContain('invalid')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
  })

  it('R1 RED closes clean success but says not to import again when list refresh fails', async () => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.connectionResponse = async () => { throw new Error('synthetic refresh failure') }
    await submit(wrapper)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('导入结果已返回，但列表刷新失败')
    expect(wrapper.text()).toContain('不要因此再次导入')
    expect(harness.requests).toHaveLength(1)
  })

  it('shows warning success with confirmed config, locks creation and completion only closes', async () => {
    harness.importResponse = async (req: RealConnectRequest) => json(warningSuccess(req))
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const text = dialog(wrapper).text()
    expect(text).toContain('导入成功，模型未同步')
    expect(text).toContain('主站账号和本地关联已保存')
    expect(text).toContain('尚未建立模型白名单')
    expect(text).toContain('导入成功不代表模型可用')
    expect(text).toContain('请到主站账号设置重新同步并保存模型白名单，无需再次导入')
    expect(text).toContain(modelWarningDiagnostic.detail)
    expect(text).toContain('仅凭 403 无法确认是否余额不足')
    expect(text).toContain('完成只关闭当前结果，不会再次创建账号或修改主站')
    expect(text).not.toContain('未创建主站账号')
    expect(text).not.toContain('模型已同步')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
    await dialog(wrapper).findAll('button').find(button => button.text().trim() === '完成')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(harness.requests).toHaveLength(1)
  })

  it('keeps a trusted preflight 409 failed, says no resources created and forbids another submit', async () => {
    harness.importResponse = async () => json(failure({
      message: 'admin.adminAccounts.errors.noCurrentAccount', stage: 'validation', cleanup: 'not_needed', retryAllowed: false,
      upstreamKeyId: undefined, upstreamResourceName: undefined,
      diagnostics: [{ stage: 'validation', target: 'local', message: 'admin.adminAccounts.errors.noCurrentAccount', detail: '没有选择当前工作区，请先选择工作区' }],
    }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('本次未创建账号或 Key')
    expect(dialog(wrapper).text()).toContain('重新打开导入窗口')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(false)
    expect(dialog(wrapper).text()).not.toContain('账号可能')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })

  it('displays original failure, model warning and cleanup issue together without retry', async () => {
    harness.importResponse = async () => json(failure({
      message: 'admin.mySites.errors.compensationPendingVerification', stage: 'configuration_check', cleanup: 'retained', retryAllowed: false,
      adminResourceId: '101', diagnostics: [
        { stage: 'configuration_check', target: 'main', message: 'admin.mySites.errors.importConfigurationMismatch', detail: 'Priority 要求 100，实际 999' },
        { ...modelWarningDiagnostic },
        { stage: 'account_cleanup', target: 'main', message: 'admin.mySites.errors.compensationPendingVerification', reason: 'admin.upstream.errors.forbidden', httpStatus: 403, detail: '主站不允许删除当前账号' },
      ],
    }), 409)
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    const text = dialog(wrapper).text()
    expect(text).toContain('Priority 要求 100，实际 999')
    expect(text).toContain(modelWarningDiagnostic.detail)
    expect(text).toContain('主站不允许删除当前账号')
    expect(text).toContain('后续清理失败')
    expect(text).toContain('101'); expect(text).toContain('207')
    expect(text).not.toContain('导入成功')
    expect(text).not.toContain('可重新导入')
    expect(submitButton(wrapper).element).toHaveProperty('disabled', true)
  })

  it.each(['missing-warning', 'wrong-target', 'nonempty-models', 'passthrough', 'control-detail', 'oversize-detail'])('keeps confirmed association without accepting invalid warning configuration: %s', async mode => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = warningSuccess(req)
      if (mode === 'missing-warning') delete result.diagnostics
      if (mode === 'wrong-target') result.diagnostics[0].target = 'upstream'
      if (mode === 'nonempty-models') result.configuration.models = ['invented']
      if (mode === 'passthrough') result.configuration.passthrough = true
      if (mode === 'control-detail') result.diagnostics[0].detail = 'unsafe\u0000text'
      if (mode === 'oversize-detail') result.diagnostics[0].detail = 'x'.repeat(513)
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper); await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('配置暂无法读取')
    expect(dialog(wrapper).text()).not.toContain('导入成功，模型未同步')
    expect(dialog(wrapper).text()).not.toContain('模型已同步')
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })

  it('preserves warning success when refreshing lists fails and renders details as plain text', async () => {
    harness.importResponse = async (req: RealConnectRequest) => {
      const result = warningSuccess(req)
      result.diagnostics[0].detail = '<img src=x onerror=alert(1)> 主站提示'
      return json(result)
    }
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.connectionResponse = async () => { throw new Error('synthetic refresh failure') }
    await submit(wrapper)
    expect(dialog(wrapper).text()).toContain('导入成功，模型未同步')
    expect(dialog(wrapper).text()).toContain('列表刷新失败')
    expect(dialog(wrapper).text()).toContain('不要因此再次导入')
    expect(dialog(wrapper).text()).toContain('<img src=x onerror=alert(1)> 主站提示')
    expect(dialog(wrapper).find('img').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    expect(harness.requests).toHaveLength(1)
  })
})

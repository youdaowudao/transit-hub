// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
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
  harness.currentAccount.value = { id: 'workspace-c5', displayName: 'C5 隔离工作区' }
  harness.platform = 'sub2api'
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

  it('shows all defaults directly, submits both selected groups once and shows only server-confirmed settings', async () => {
    const wrapper = await openView()
    expect(dialog(wrapper).get('#import-priority').element).toHaveProperty('value', '100')
    expect(dialog(wrapper).get('#import-concurrency').element).toHaveProperty('value', '50')
    expect(dialog(wrapper).get('[role="switch"][aria-label="透传"]').attributes('aria-checked')).toBe('false')
    expect(dialog(wrapper).get('[role="switch"][aria-label="Pool Mode"]').attributes('aria-checked')).toBe('true')
    expect(dialog(wrapper).get('[role="switch"][aria-label="自动探测上游声明倍率"]').attributes('aria-checked')).toBe('true')
    await chooseGroup(wrapper); await chooseGroup(wrapper, '表单分组乙')
    await submit(wrapper)
    expect(harness.requests).toHaveLength(1)
    expect(harness.requests[0]).toMatchObject({ ownGroupIds: ['10', '11'], groupType: 'openai', accountSettings: { priorityMode: 'automatic', priority: 100, concurrency: 50, passthrough: false, poolMode: true, upstreamBillingProbeEnabled: true } })
    const summary = dialog(wrapper).get('[data-import-success]')
    expect(summary.text()).toContain('主站读回名称')
    expect(summary.text()).toContain('主站确认甲（10）')
    expect(summary.text()).toContain('主站确认乙（11）')
    expect(summary.text()).toContain('已由主站同步并保存 2 个模型')
    expect(summary.text()).toContain('live-c5-model-a')
    expect(summary.text()).not.toContain('表单分组甲')
    expect(summary.text()).not.toContain('本地记录名称')
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit')
    expect(harness.requests).toHaveLength(1)
    await dialog(wrapper).findAll('button').find(button => button.text() === '完成')!.trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
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
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('123')
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
    expect(dialog(wrapper).text()).toContain('已移除不再匹配')
    expect(dialog(wrapper).text()).toContain('导入时由主站同步')
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
    expect(dialog(wrapper).find('[data-import-success]').exists()).toBe(true)
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
    expect(dialog(wrapper).get('[data-import-refresh-error]').text()).toContain('变更已保存，但列表刷新失败')
    expect(dialog(wrapper).text()).not.toContain('真实对接创建失败')
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    await dialog(wrapper).get('form').trigger('submit'); expect(harness.requests).toHaveLength(1)
  })

  it.each(['mapping', 'connections', 'rates'])('keeps confirmed import separate from %s refresh failure', async source => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    if (source === 'mapping') harness.mappingResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    if (source === 'connections') harness.connectionResponse = async () => json({ message: 'admin.mySites.errors.request' }, 500)
    if (source === 'rates') harness.listGroupRates.mockRejectedValueOnce(new Error('admin.groupRates.errors.request'))
    await submit(wrapper)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('主站配置已确认')
    expect(dialog(wrapper).get('[data-import-refresh-error]').text()).toContain('列表刷新失败')
    expect(dialog(wrapper).find('[data-import-pending]').exists()).toBe(false)
    expect(dialog(wrapper).find('[data-import-failure]').exists()).toBe(false)
    expect(dialog(wrapper).find('button[type="submit"]').exists()).toBe(false)
    expect(dialog(wrapper).text()).not.toContain('真实对接创建失败')
    await dialog(wrapper).get('form').trigger('submit'); expect(harness.requests).toHaveLength(1)
  })

  it('keeps all close and submit controls frozen while independent refreshes are still loading', async () => {
    const pendingRows = deferred<ReturnType<typeof rows>>()
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

  it('submits enabled passthrough and accurately displays skipped whitelist creation', async () => {
    const wrapper = await openView(); await chooseGroup(wrapper)
    await dialog(wrapper).get('[aria-label="透传"]').trigger('click'); await submit(wrapper)
    expect(harness.requests[0].accountSettings?.passthrough).toBe(true)
    expect(dialog(wrapper).get('[data-import-success]').text()).toContain('透传开启，本次不建立白名单')
    expect(dialog(wrapper).get('[data-import-success]').find('details').exists()).toBe(false)
    expect(dialog(wrapper).text()).not.toContain('已由主站同步并保存')
  })

  it('keeps NewAPI main-site requests on the original settings-free API branch', async () => {
    harness.platform = 'newapi'
    harness.importResponse = async () => json({ connection: {} })
    const wrapper = await openView(); await chooseGroup(wrapper)
    expect(dialog(wrapper).find('#import-concurrency').exists()).toBe(false)
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
    harness.currentAccount.value = { id: 'workspace-new', displayName: '新工作区' }
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

  it('keeps an in-flight post-success refresh from replacing the new workspace rows', async () => {
    const pendingRows = deferred<ReturnType<typeof rows>>()
    const wrapper = await openView(); await chooseGroup(wrapper)
    harness.listGroupRates.mockImplementationOnce(() => pendingRows.promise)
    await dialog(wrapper).get('form').trigger('submit'); await flushPromises()
    harness.currentAccount.value = { id: 'workspace-new', displayName: '新工作区' }
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

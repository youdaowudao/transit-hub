// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PolicyConfigDrawer from '@/modules/admin/components/dashboard/PolicyConfigDrawer.vue'
import ProbePolicyListDialog from '@/modules/admin/components/dashboard/ProbePolicyListDialog.vue'
import ConnectionHealthEventsDialog from '@/modules/admin/components/dashboard/ConnectionHealthEventsDialog.vue'
import AdminGroupHealthDetail from '@/modules/admin/components/dashboard/AdminGroupHealthDetail.vue'
import type { ConnectionHealthPolicy, ConnectionHealthEvent, AdminGroupHealth } from '@/modules/admin/types/connectionHealth'

const policies = (): ConnectionHealthPolicy[] => ['first', 'second'].map(id => ({
  id, name: id === 'first' ? '第一策略' : '第二策略', enabled: true, ownGroupId: '', ownGroupName: '',
  modelPattern: '', probeMode: '', probeIntervalSeconds: 600, continueProbeWhenUnschedulable: true,
  unschedulableProbeIntervalMinutes: 60, failureThreshold: 8, successThreshold: 7, cooldownSeconds: 100,
  observationSeconds: 50, recoveryStepPercent: 10, autoDegradeEnabled: true, autoRemoteActionEnabled: false,
  priorityMode: 'none', strategyMode: 'health_probe', dailyProbeBudget: 1000000,
  createdAt: '', updatedAt: '', rulePresetId: 'recommended', legacyPresetId: 'snapshot',
  modelTargets: [{ id: 'model', policyId: id, modelName: 'gpt-test', providerFamily: 'openai', enabled: true, probePrompt: '', maxProbeTokens: 1, createdAt: '', updatedAt: '' }],
}))
const preset = (id: string, kind = 'custom') => ({
  id, name: id, kind, failureThreshold: 3, successThreshold: 2, cooldownSeconds: 300,
  failedRetryIntervalSeconds: 600, longFailureAfterSeconds: 86400, longFailureIntervalSeconds: 3600,
  delayLineMs: { responses: 10000, chat_completions: 5000 }, observationSeconds: 300, recoveryStepPercent: 25,
  createdAt: '', updatedAt: '', policies: id === 'recommended' ? policies().map(({ id, name }) => ({ id, name })) : [],
})
let presets = [preset('recommended', 'recommended'), preset('snapshot', 'legacy_snapshot'), preset('default', 'legacy_default'), preset('unused')]
let settings = { ruleVersion: 'v2', configGeneration: 1, probeConcurrency: 6, probeConcurrencyVersion: 1, ruleSwitchedAt: '2026-10-06T12:00:00Z', updatedAt: '' }
let failWrite = false
let writeErrorKey = 'admin.connectionHealth.errors.request'
let failRead = false
let writes: { url: string; body: Record<string, unknown>; method: string }[] = []
const wrappers: VueWrapper[] = []
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
beforeEach(() => {
  presets = [preset('recommended', 'recommended'), preset('snapshot', 'legacy_snapshot'), preset('default', 'legacy_default'), preset('unused')]
  settings = { ruleVersion: 'v2', configGeneration: 1, probeConcurrency: 6, probeConcurrencyVersion: 1, ruleSwitchedAt: '2026-10-06T12:00:00Z', updatedAt: '' }
  failWrite = false; failRead = false; writes = []; writeErrorKey = 'admin.connectionHealth.errors.request'
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input), method = init?.method ?? 'GET'
    if (method === 'GET') {
      if (failRead) return json({ message: 'admin.connectionHealth.errors.request' }, 500)
      if (url.endsWith('/rule-presets')) return json(presets)
      if (url.endsWith('/workspace-settings')) return json(settings)
      if (url.endsWith('/policies')) return json(policies())
    } else {
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      writes.push({ url, method, body })
      if (failWrite) return json({ message: writeErrorKey }, 409)
      if (url.endsWith('/workspace-settings')) { settings = { ...settings, ...body, probeConcurrencyVersion: settings.probeConcurrencyVersion + 1 }; return json(settings) }
      if (url.includes('/rule-version/')) { settings.ruleVersion = url.endsWith('/restore-legacy') ? 'legacy' : 'v2'; settings.configGeneration++; return json(settings) }
      if (url.endsWith('/apply-all')) return json(settings)
      if (method === 'DELETE') { presets = presets.filter(item => !url.endsWith('/' + item.id)); return new Response(null, { status: 204 }) }
      if (url.includes('/rule-presets')) {
        const id = method === 'PUT' ? url.split('/').at(-1)! : 'created'
        const saved = { ...preset(id), ...body, id }
        presets = method === 'PUT' ? presets.map(item => item.id === id ? saved : item) : [...presets, saved]
        return json(saved)
      }
    }
    throw new Error(`Unexpected ${method}: ${url}`)
  }))
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.unstubAllGlobals(); document.body.innerHTML = '' })
const setup = { global: { stubs: { Teleport: true, Transition: false } } }
const list = async () => {
  const wrapper = mount(ProbePolicyListDialog, { ...setup, props: { open: true, policies: policies(), deletingPolicyId: '', deleteError: '', workspaceId: 'ws1', workspacePlatform: 'sub2api' } })
  wrappers.push(wrapper); await flushPromises(); return wrapper
}
const drawer = async () => {
  const wrapper = mount(PolicyConfigDrawer, { ...setup, props: { open: false, policy: policies()[0]!, ownGroupOptions: [], policies: policies(), workspaceId: 'ws1' } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}
const manage = async (wrapper: VueWrapper) => { await wrapper.get('[data-testid="manage-rule-presets"]').trigger('click'); await flushPromises() }
const confirm = async (wrapper: VueWrapper) => { await wrapper.get('[data-testid="confirm-health-rule-action"]').trigger('click'); await flushPromises() }

describe('task A health rule settings and preset interaction', () => {
  it('selects a preset and preserves all policy settings without submitting the five frozen columns', async () => {
    const wrapper = await drawer()
    expect(wrapper.text()).toContain('判定预设')
    expect(wrapper.get('[data-testid="selected-rule-preset-details"]').text()).toContain('连续失败几次关停')
    expect(wrapper.get('[data-testid="selected-rule-preset-details"]').find('input').exists()).toBe(false)
    await wrapper.get('[data-testid="policy-rule-preset"]').setValue('unused')
    await wrapper.get('[data-testid="save-health-policy"]').trigger('click')
    const saved = wrapper.emitted('save')![0]![0] as Record<string, unknown>
    expect(saved).toMatchObject({ rulePresetId: 'unused', probeIntervalSeconds: 600, dailyProbeBudget: 1000000, unschedulableProbeIntervalMinutes: 60 })
    for (const column of ['failureThreshold', 'successThreshold', 'cooldownSeconds', 'observationSeconds', 'recoveryStepPercent']) expect(saved).not.toHaveProperty(column)
    expect(wrapper.text()).toContain('只对主站调度开关已关闭的账号生效')
  })

  it('does not silently replace or save a preset selection when reading presets fails', async () => {
    failRead = true
    const wrapper = await drawer()
    await wrapper.get('[data-testid="save-health-policy"]').trigger('click')
    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect((wrapper.get('[data-testid="policy-rule-preset"]').element as HTMLSelectElement).value).toBe('recommended')
  })

  it('creates the first workspace policy with recommended defaults without writing during reads', async () => {
    presets = []
    const wrapper = await drawer()
    await wrapper.setProps({ open: false, policy: null, policies: [] }); await wrapper.setProps({ open: true }); await flushPromises()
    expect(wrapper.get('[data-testid="policy-rule-preset"]').text()).toContain('新规则（推荐）')
    expect(wrapper.get('[data-testid="selected-rule-preset-details"]').text()).toContain('24 小时')
    expect(writes).toEqual([])
    await wrapper.get('input[type="text"]').setValue('首次策略')
    await wrapper.get('input[placeholder="模型名称，如 gpt-4o-mini"]').setValue('gpt-test')
    await wrapper.get('[data-testid="save-health-policy"]').trigger('click')
    const saved = wrapper.emitted('save')![0]![0] as Record<string, unknown>
    expect(saved.name).toBe('首次策略')
    expect(saved).not.toHaveProperty('rulePresetId')
    expect(saved).not.toHaveProperty('failureThreshold')
    expect(writes).toEqual([])
  })

  it('shows read-only presets with copy only; rejects deletion of built-in and referenced presets', async () => {
    presets.push({ ...preset('referenced'), policies: [{ id: 'first', name: '第一策略' }] })
    const wrapper = await list(); await manage(wrapper)
    for (const id of ['snapshot', 'default']) {
      const row = wrapper.get(`[data-testid="rule-preset-${id}"]`)
      expect(row.find('[data-action="edit"]').exists()).toBe(false)
      expect(row.find('[data-action="delete"]').exists()).toBe(false)
      expect(row.find('[data-action="apply-all"]').exists()).toBe(false)
      expect(row.find('[data-action="copy"]').exists()).toBe(true)
    }
    expect(wrapper.get('[data-testid="rule-preset-recommended"] [data-action="delete"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="rule-preset-referenced"] [data-action="delete"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="rule-preset-snapshot"] [data-action="copy"]').trigger('click')
    expect(wrapper.get('[data-testid="rule-preset-editor"]').text()).toContain('当前规则下不适用')
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes.at(-1)).toMatchObject({ method: 'POST', body: { failureThreshold: 3, observationSeconds: 300 } })
    expect(writes.at(-1)!.body).not.toHaveProperty('id')
    expect(writes.at(-1)!.body).not.toHaveProperty('kind')
  })

  it('requires affected-policy confirmation before modifying a shared preset and retains a failed draft', async () => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-recommended"] [data-action="edit"]').trigger('click')
    await wrapper.get('[data-testid="preset-failureThreshold"]').setValue(4)
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click')
    expect(writes).toHaveLength(0)
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('第一策略')
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('第二策略')
    failWrite = true; await confirm(wrapper)
    expect(wrapper.get('[role="alertdialog"]').find('[role="alert"]').exists()).toBe(true)
    expect(presets[0]!.failureThreshold).toBe(3)
    expect((wrapper.get('[data-testid="preset-failureThreshold"]').element as HTMLInputElement).value).toBe('4')
    failWrite = false; await confirm(wrapper)
    expect(presets[0]!.failureThreshold).toBe(4)
  })

  it.each([30, 4000])('copies an old snapshot with cooldown %s without clamping and requires explicit valid edits', async cooldownSeconds => {
    presets[1] = { ...presets[1]!, failureThreshold: 1, successThreshold: 12, cooldownSeconds }
    const original = structuredClone(presets[1]!)
    const wrapper = await list(); await manage(wrapper)
    const copy = () => wrapper.get('[data-testid="rule-preset-snapshot"] [data-action="copy"]').trigger('click')
    const value = (key: string) => (wrapper.get(`[data-testid="preset-${key}"]`).element as HTMLInputElement).value
    await copy()
    expect(value('failureThreshold')).toBe('1')
    expect(value('successThreshold')).toBe('12')
    expect(value('cooldownSeconds')).toBe(String(cooldownSeconds))
    expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes).toEqual([])
    await wrapper.get('[data-testid="rule-preset-editor"]').findAll('button').find(button => button.text() === '取消')!.trigger('click')
    expect(wrapper.find('[data-testid="rule-preset-editor"]').exists()).toBe(false)
    expect(presets[1]).toEqual(original)
    await copy()
    expect(value('failureThreshold')).toBe('1')
    expect(value('successThreshold')).toBe('12')
    expect(value('cooldownSeconds')).toBe(String(cooldownSeconds))
    await wrapper.get('[data-testid="preset-failureThreshold"]').setValue(2)
    expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="preset-successThreshold"]').setValue(10)
    expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="preset-cooldownSeconds"]').setValue(60)
    expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes).toHaveLength(1)
    expect(writes[0]).toMatchObject({ method: 'POST', body: { name: 'snapshot（副本）', failureThreshold: 2, successThreshold: 10, cooldownSeconds: 60, observationSeconds: original.observationSeconds, recoveryStepPercent: original.recoveryStepPercent } })
    expect(writes[0]!.body).not.toHaveProperty('id')
    expect(writes[0]!.body).not.toHaveProperty('kind')
    expect(presets.find(item => item.id === 'created')).toMatchObject({ kind: 'custom', failureThreshold: 2, successThreshold: 10, cooldownSeconds: 60 })
    expect(presets.find(item => item.id === 'snapshot')).toEqual(original)
    expect(wrapper.find('[data-testid="rule-preset-editor"]').exists()).toBe(false)
  })

  it('confirms applying a preset to all policies and never changes policy controls on failed apply', async () => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-unused"] [data-action="apply-all"]').trigger('click')
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('第一策略')
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('第二策略')
    expect(writes).toHaveLength(0)
    failWrite = true; await confirm(wrapper)
    expect(wrapper.get('[role="alertdialog"]').find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.emitted('rules-changed')).toBeUndefined()
    failWrite = false; await confirm(wrapper)
    expect(writes.at(-1)!.url).toContain('/rule-presets/unused/apply-all')
    expect(wrapper.emitted('rules-changed')).toHaveLength(1)
  })

  it('confirms both workspace rule switches including complete state conversion and policy names', async () => {
    const wrapper = await list()
    expect(wrapper.get('[data-testid="workspace-rule-settings"]').text()).toContain('新规则')
    expect(wrapper.get('[data-testid="workspace-rule-settings"]').text()).toContain('上次切换')
    for (const operation of ['restore-legacy', 'switch-v2']) {
      await wrapper.get(`[data-testid="${operation}"]`).trigger('click')
      expect(wrapper.get('[role="alertdialog"]').text()).toContain('全部 Sub2API 状态')
      expect(wrapper.get('[role="alertdialog"]').text()).toContain('第一策略')
      expect(wrapper.get('[role="alertdialog"]').text()).toContain('第二策略')
      failWrite = true; await confirm(wrapper)
      expect(wrapper.get('[role="alertdialog"]').find('[role="alert"]').exists()).toBe(true)
      expect(settings.ruleVersion).toBe(operation === 'restore-legacy' ? 'v2' : 'legacy')
      failWrite = false; await confirm(wrapper)
      expect(settings.ruleVersion).toBe(operation === 'restore-legacy' ? 'legacy' : 'v2')
      expect(writes.at(-1)!.url).toContain('/rule-version/' + operation)
    }
  })

  it('limits concurrency to 1–10 and retains saved value and failed draft with a versioned write', async () => {
    const wrapper = await list()
    const input = wrapper.get('[data-testid="probe-concurrency"]')
    for (const invalid of [0, 11, 1.5]) {
      await input.setValue(invalid)
      expect(wrapper.get('[data-testid="save-probe-concurrency"]').attributes('disabled')).toBeDefined()
    }
    await input.setValue(10); failWrite = true
    await wrapper.get('[data-testid="save-probe-concurrency"]').trigger('click'); await flushPromises()
    expect(settings.probeConcurrency).toBe(6)
    expect((input.element as HTMLInputElement).value).toBe('10')
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(writes.at(-1)!.body).toEqual({ probeConcurrency: 10, probeConcurrencyVersion: 1 })
    failWrite = false; await wrapper.get('[data-testid="save-probe-concurrency"]').trigger('click'); await flushPromises()
    expect(settings.probeConcurrency).toBe(10)
    expect(wrapper.text()).toContain('只决定同时跑几个，不增加请求次数')
  })

  it('deletes only an unused custom preset after confirmation and retains list on rejection', async () => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-unused"] [data-action="delete"]').trigger('click')
    expect(writes).toHaveLength(0)
    failWrite = true; await confirm(wrapper)
    expect(wrapper.find('[data-testid="rule-preset-unused"]').exists()).toBe(true)
    failWrite = false; await confirm(wrapper)
    expect(wrapper.find('[data-testid="rule-preset-unused"]').exists()).toBe(false)
  })

  it('creates a named custom preset, preserves unfilled protocol defaults and validates every parameter', async () => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.findAll('button').find(button => button.text() === '新增预设')!.trigger('click')
    await wrapper.get('input[aria-label="预设名称"]').setValue('自建规则')
    for (const [key, invalid] of [['failureThreshold', 1], ['successThreshold', 11], ['cooldownSeconds', 59], ['failedRetryIntervalSeconds', 3601], ['longFailureAfterSeconds', 0], ['longFailureIntervalSeconds', 9], ['observationSeconds', 0], ['recoveryStepPercent', 101]] as const) {
      const input = wrapper.get(`[data-testid="preset-${key}"]`), prior = (input.element as HTMLInputElement).value
      await input.setValue(invalid)
      expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeDefined()
      await wrapper.get(`[data-testid="preset-${key}"]`).setValue(prior)
      expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeUndefined()
    }
    await wrapper.get('input[aria-label="Responses 延迟线"]').setValue('')
    expect(wrapper.get('[data-testid="save-rule-preset"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes.at(-1)!.body).toMatchObject({ name: '自建规则', longFailureAfterSeconds: 86400, longFailureIntervalSeconds: 3600, delayLineMs: { chat_completions: 5000 } })
    expect((writes.at(-1)!.body.delayLineMs as Record<string, number>).responses).toBeUndefined()
    expect(wrapper.text()).toContain('自建规则')
    expect(wrapper.find('[data-testid="rule-preset-editor"]').exists()).toBe(false)
  })

  it('cancels a rule switch and shared preset update without writes', async () => {
    const wrapper = await list()
    await wrapper.get('[data-testid="restore-legacy"]').trigger('click')
    await wrapper.get('[role="alertdialog"]').findAll('button').find(button => button.text() === '取消')!.trigger('click')
    await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-recommended"] [data-action="edit"]').trigger('click')
    await wrapper.get('[data-testid="preset-failureThreshold"]').setValue(4)
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click')
    await wrapper.get('[role="alertdialog"]').findAll('button').find(button => button.text() === '取消')!.trigger('click')
    expect(writes).toEqual([])
    expect(presets[0]!.failureThreshold).toBe(3)
    expect((wrapper.get('[data-testid="preset-failureThreshold"]').element as HTMLInputElement).value).toBe('4')
  })

  it('rejects an old settings response after changing workspace and preserves the new saved state', async () => {
    let finishRead!: (response: Response) => void
    const originalFetch = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn((input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/workspace-settings') && !init?.method && !finishRead) return new Promise<Response>(resolve => { finishRead = resolve })
      return originalFetch(input, init)
    }))
    const wrapper = await list()
    settings = { ...settings, ruleVersion: 'legacy', probeConcurrency: 2, probeConcurrencyVersion: 7 }
    await wrapper.setProps({ workspaceId: 'ws2' }); await flushPromises()
    expect(wrapper.get('[data-testid="workspace-rule-settings"]').text()).toContain('旧规则')
    expect((wrapper.get('[data-testid="probe-concurrency"]').element as HTMLInputElement).value).toBe('2')
    finishRead(json({ ...settings, ruleVersion: 'v2', probeConcurrency: 9, probeConcurrencyVersion: 1 }))
    await flushPromises()
    expect(wrapper.get('[data-testid="workspace-rule-settings"]').text()).toContain('旧规则')
    expect((wrapper.get('[data-testid="probe-concurrency"]').element as HTMLInputElement).value).toBe('2')
  })

  it('keeps a newer concurrency save when a pre-save retry read arrives afterward', async () => {
    const wrapper = await list()
    await wrapper.get('[data-testid="probe-concurrency"]').setValue(10)
    failWrite = true; await wrapper.get('[data-testid="save-probe-concurrency"]').trigger('click'); await flushPromises()
    const originalFetch = globalThis.fetch, older = { ...settings }
    let finishRead!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn((input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/workspace-settings') && !init?.method) return new Promise<Response>(resolve => { finishRead = resolve })
      return originalFetch(input, init)
    }))
    await wrapper.findAll('button').find(button => button.text() === '重新读取')!.trigger('click')
    failWrite = false
    await wrapper.get('[data-testid="probe-concurrency"]').setValue(8)
    await wrapper.get('[data-testid="save-probe-concurrency"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('当前已保存：8')
    finishRead(json(older)); await flushPromises()
    expect(wrapper.text()).toContain('当前已保存：8')
    expect(wrapper.text()).not.toContain('当前已保存：6')
    expect((wrapper.get('[data-testid="probe-concurrency"]').element as HTMLInputElement).value).toBe('8')
  })

  it('renders the API settingsConflict reason and reread action while retaining the failed draft', async () => {
    const wrapper = await list()
    await wrapper.get('[data-testid="probe-concurrency"]').setValue(10)
    failWrite = true; writeErrorKey = 'admin.connectionHealth.errors.settingsConflict'
    await wrapper.get('[data-testid="save-probe-concurrency"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('同时探活数已被其他操作修改，请重新读取后重试')
    expect(wrapper.get('[role="alert"]').text()).not.toContain('暂时无法读取分组健康数据')
    expect(wrapper.get('[role="alert"]').findAll('button').some(button => button.text() === '重新读取')).toBe(true)
    expect(settings.probeConcurrency).toBe(6)
    expect((wrapper.get('[data-testid="probe-concurrency"]').element as HTMLInputElement).value).toBe('10')
  })

  it.each([
    ['presetInUse', '此预设正被策略引用，不能删除'],
    ['presetBuiltIn', '内置预设不能删除'],
  ])('renders the actual API %s deletion rejection and keeps cancel and original values', async (key, explanation) => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-unused"] [data-action="delete"]').trigger('click')
    failWrite = true; writeErrorKey = `admin.connectionHealth.errors.${key}`
    await confirm(wrapper)
    expect(wrapper.get('[role="alertdialog"] [role="alert"]').text()).toContain(explanation)
    expect(wrapper.get('[role="alertdialog"] [role="alert"]').text()).not.toContain('暂时无法读取分组健康数据')
    expect(wrapper.find('[data-testid="rule-preset-unused"]').exists()).toBe(true)
    await wrapper.get('[role="alertdialog"]').findAll('button').find(button => button.text() === '取消')!.trigger('click')
    expect(wrapper.find('[role="alertdialog"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="rule-preset-unused"]').exists()).toBe(true)
    expect(writes).toHaveLength(1)
  })

  it('renders the API presetReadOnly reason while preserving the shared-preset edit draft', async () => {
    const wrapper = await list(); await manage(wrapper)
    await wrapper.get('[data-testid="rule-preset-recommended"] [data-action="edit"]').trigger('click')
    await wrapper.get('[data-testid="preset-failureThreshold"]').setValue(4)
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click')
    failWrite = true; writeErrorKey = 'admin.connectionHealth.errors.presetReadOnly'
    await confirm(wrapper)
    expect(wrapper.get('[role="alertdialog"] [role="alert"]').text()).toContain('此预设只读，请复制后修改')
    expect(presets[0]!.failureThreshold).toBe(3)
    expect((wrapper.get('[data-testid="preset-failureThreshold"]').element as HTMLInputElement).value).toBe('4')
    await wrapper.get('[role="alertdialog"]').findAll('button').find(button => button.text() === '取消')!.trigger('click')
    expect((wrapper.get('[data-testid="preset-failureThreshold"]').element as HTMLInputElement).value).toBe('4')
    expect(writes).toHaveLength(1)
  })
})

const event = (ruleVersion: string, id: string): ConnectionHealthEvent => ({
  id, connectionId: 'sub2api:ws1:account', modelName: 'gpt-test', ownGroupName: '分组', upstreamSiteId: '', upstreamGroupName: '分组',
  result: 'slow_response', fromState: 'healthy', toState: 'suspect', latencyMs: 12000, firstTokenMs: 11000,
  firstEventMs: 300, ruleVersion, errorKey: '', remoteAction: '', source: 'scheduled', createdAt: '2026-10-06T12:00:00Z',
})
describe('task A health state and measurement rendering', () => {
  it('recognizes suspect event fallback and labels each historical slow response from its own rule version', async () => {
    const wrapper = mount(ConnectionHealthEventsDialog, { ...setup, props: { open: true, events: [event('v2', 'new'), event('legacy', 'old')], groups: [], adminGroups: [], selectedConnectionId: 'sub2api:ws1:account', siteName: () => '' } })
    wrappers.push(wrapper); await flushPromises()
    expect(wrapper.text()).toContain('疑似')
    expect(wrapper.text()).toContain('首字：11000ms')
    expect(wrapper.text()).toContain('首个事件：300ms')
    const titles = wrapper.findAll('[title]').map(item => item.attributes('title'))
    expect(titles.some(title => title.includes('慢响应'))).toBe(true)
    expect(titles.some(title => title.includes('延迟'))).toBe(true)
    expect(titles.some(title => title.includes('高延迟成功'))).toBe(false)
    expect(wrapper.text()).toContain('100%')
  })

  it('filters suspect accounts independently while retaining their healthy-band count', async () => {
    const model = { modelName: 'gpt-test', providerFamily: 'openai', configured: true, state: 'suspect', currentWeight: 100, consecutiveFailures: 1, consecutiveSuccesses: 0, lastProbeAt: null, lastSuccessAt: null, lastFailureAt: null, lastLatencyMs: 12000, firstTokenMs: 11000, firstEventMs: 300, lastErrorKey: '', lastErrorDetail: '', lastRemoteAction: '', updatedAt: null }
    const account = (id: string, state: string) => ({ id, name: id, platform: 'openai', type: '', status: 'active', schedulable: true, priority: 10, targetId: 'sub2api:ws1:' + id, probeAvailable: true, modelHealth: [{ ...model, state }] })
    const group = { id: 'g', name: '分组', platform: 'openai', status: 'active', type: 'public', isExclusive: false, subscriptionType: '', multiplier: 1, multiplierDisplay: '1', accountCount: 2, healthSummary: { totalAccounts: 2, probeableAccounts: 2, unprobeableAccounts: 0, healthyModels: 2, suspectModels: 1, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 0, lastProbeAt: null }, accounts: [account('疑似账号', 'suspect'), account('正常账号', 'healthy')] } as AdminGroupHealth
    const wrapper = mount(AdminGroupHealthDetail, { ...setup, props: { group, hideUnmonitoredAccounts: false, actionLoading: false, questionAnswerUnreadTargetIds: [] } })
    wrappers.push(wrapper); await flushPromises()
    const suspect = wrapper.findAll('button').find(button => button.text().includes('疑似'))!
    expect(suspect).toBeDefined(); await suspect.trigger('click')
    expect(wrapper.text()).toContain('疑似账号')
    expect(wrapper.text()).not.toContain('正常账号')
    expect(wrapper.text()).toContain('首字')
  })
})

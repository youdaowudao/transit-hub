// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupHealthSetupDrawer from '@/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue'
import HealthRulePresetFields from '@/modules/admin/components/dashboard/HealthRulePresetFields.vue'
import ProbePolicyListDialog from '@/modules/admin/components/dashboard/ProbePolicyListDialog.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import { testProtocolName, useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import { questionAnswerRequestProtocolLabel } from '@/modules/admin/utils/questionAnswers'
import { defaultHealthRulePreset } from '@/modules/admin/utils/healthRulePresets'
import { t } from '@/locales'
import type { AdminGroupHealth, AdminGroupTestConfiguration, GroupTestConfiguration, TestProtocol } from '@/modules/admin/types/connectionHealth'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'

const copy = {
  option: 'Messages（Anthropic 原生接口）',
  platform: 'Messages 是 Anthropic 分组的用户实际调用的接口，只有主站平台为 Anthropic 的分组可以选。',
  behavior: '选 Messages 后，本组账号的健康探活改发 Messages 请求，收到第一段正文就算成功。保存这项配置本身不会修改主站账号；探活出结果后，仍按现有规则自动停用或恢复账号。问答（智商测试）也用 Messages 发送。',
  health: '保存后，如果账号已有的健康结果不是用 Messages 测的，账号会显示“当前协议健康依据待验证”，这是正常的。请在保存后查看本组账号的探活结果；这个提示长时间不消失时，看账号“最近一次尝试”的说明。',
  conflict: '同一个账号所在的其他分组如果已经单独配置过测试请求，协议和等待秒数要设成一样，否则这个账号会显示“配置冲突”并停止探活；没有单独配置的分组不会造成冲突。',
  budget: 'Messages 轻量探活至少请求 128 个输出 token；更高策略预算保留。',
  error: 'Messages 只能用于主站平台为 Anthropic 的分组。',
  probe: '注意：测试请求只带 Base URL 和 API Key，按分组测试配置里选的 Messages 协议发送。上游如果还需要专用代理、OAuth、TLS 证书或自定义请求头，系统拿不到，测试也不会带上，所以结果可能和真实使用不同',
  questionAnswer: 'Messages 问答边生成边接收回答，每题最多输出 64000 个 token（思考也算在内），最长等 20 分钟。回答被这个上限截断时，这道题记为“回答不完整”，不判对错。思考强度按所选档位原样发给上游；档位越高，等待越久、花费越多。',
  legacy: '注意：测试请求只带 Base URL 和 API Key，按分组测试配置里选的协议（Chat Completions 或 Responses）发送。上游如果还需要专用代理、OAuth、TLS 证书或自定义请求头，系统拿不到，测试也不会带上，所以结果可能和真实使用不同',
}
const targetId = 'sub2api:c41-component:1'
const model = { id: 'claude-c41-fixture', name: 'claude-c41-fixture', providerFamily: 'anthropic' }
const group = (platform = 'anthropic'): AdminGroupHealth => ({
  id: 'c41-group', name: 'C4.1 组件分组', platform, status: 'active', type: 'public', isExclusive: false,
  subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: 1,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 1, lastProbeAt: null },
  accounts: [{ id: '1', name: 'C4.1 组件账号', platform: 'anthropic', type: 'apikey', status: 'active', schedulable: true, targetId, probeAvailable: true, models: model.id, modelHealth: [] }],
})
let saved: GroupTestConfiguration | null
let rejectSave: boolean
let inventoryComplete: boolean
let writes: Array<{ url: string; method: string; body: any }>
let unexpected: string[]
let savedPreset: ReturnType<typeof preset>
const preset = () => ({ ...defaultHealthRulePreset(), id: 'custom-c41', name: 'C4.1 延迟线', kind: 'custom', createdAt: '', updatedAt: '', policies: [] })
const testConfiguration = (): AdminGroupTestConfiguration => ({ adminGroupId: 'c41-group', adminGroupName: 'C4.1 组件分组', configuration: saved, inventoryComplete, affectedAccountCount: 1, conflictAccountCount: 0, accounts: [{ targetId, accountName: 'C4.1 组件账号', testConfiguration: { status: saved ? 'inherited' : 'default', protocol: saved?.protocol ?? 'chat_completions', probeTimeoutSeconds: saved?.probeTimeoutSeconds ?? 20, sourceGroups: [] } }] })
const stats = questionAnswerFixtureStats([])
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const wrappers: VueWrapper[] = []
const service = useConnectionHealth()
const setup = { global: { stubs: { Teleport: true, Transition: false } } }
beforeEach(() => {
  vi.useFakeTimers()
  saved = null; rejectSave = false; inventoryComplete = true; writes = []; unexpected = []; savedPreset = preset()
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('c41-component')
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = decodeURIComponent(String(input)), method = init?.method ?? 'GET'
    if (method !== 'GET') {
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      writes.push({ url, method, body })
      if (url.endsWith('/test-configuration')) {
        if (rejectSave) return json({ message: 'admin.connectionHealth.errors.testProtocolNotAllowed' }, 400)
        saved = body.configuration; return json(testConfiguration())
      }
      if (url.endsWith('/manual-probe')) return json([{ modelName: model.id, result: 'ok', healthy: true, latencyMs: 42, firstTokenMs: 42, ruleVersion: 'v2', protocol: 'messages', probeTimeoutSeconds: 20, errorKey: '', errorDetail: '', probedAt: '2026-10-12T00:00:00Z' }])
      if (url.endsWith('/rule-presets/custom-c41')) { savedPreset = { ...savedPreset, ...body }; return json(savedPreset) }
    } else {
      if (url.endsWith('/test-configuration')) return json(testConfiguration())
      if (url.endsWith('/policy-configuration')) return json({ adminGroupId: 'c41-group', policyIds: [], excludedTargetIds: [] })
      if (url.endsWith('/admin-groups')) return json([group()])
      if (url.endsWith('/rule-presets')) return json([savedPreset])
      if (url.endsWith('/workspace-settings')) return json({ ruleVersion: 'v2', configGeneration: 1, probeConcurrency: 6, probeConcurrencyVersion: 1, ruleSwitchedAt: null, updatedAt: '' })
      if (url.endsWith('/policies')) return json([])
      if (url.endsWith('/models')) return json([model])
      if (url.endsWith('/test-questions')) return json([])
      if (url.includes('/question-answers/history?')) return json({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 1, todayStats: stats })
      if (url.endsWith('/question-answers/batches/latest')) return json({ targetId, batchId: '', records: [], active: false, reasoningEffort: 'medium', repeatCount: 1, submittedCount: 0, completedCount: 0, runningCount: 0, currentModel: '', currentQuestion: '', stats })
      if (url.includes('/model-control/targets/')) return json({ targetId, items: [], candidates: [] })
      if (url.endsWith('/model-control/settings')) return json({ minAccuracyPercent: 80, minJudgedAnswers: 3, version: 0 })
      if (url.endsWith('/model-control/auto-settings')) return json({ autoCloseEnabled: false, autoRestoreEnabled: false, healthCoordinationEnabled: false, autoCloseEnabledAt: null, autoRestoreEnabledAt: null, verifyIntervalMinutes: 15, retryIntervalMinutes: 5, version: 0, lastScanStartedAt: null, lastScanFinishedAt: null, lastScanErrorKey: '' })
    }
    unexpected.push(`${method} ${url}`)
    return json({ message: 'admin.connectionHealth.errors.request' }, 500)
  }))
})
afterEach(async () => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  service.setAdminGroupsWorkspace(''); await flushPromises()
  expect(unexpected).toEqual([])
  expect(vi.getTimerCount()).toBe(0)
  vi.useRealTimers(); vi.unstubAllGlobals(); document.body.innerHTML = ''
})
const drawer = async (platform = 'anthropic') => {
  const current = group(platform)
  const wrapper = mount(GroupHealthSetupDrawer, { ...setup, props: { open: false, workspacePlatform: 'sub2api', group: current, allGroups: [current], policies: [] } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}
const click = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === label)
  expect(button, label).toBeDefined(); await button!.trigger('click'); await flushPromises()
}
const dialog = async (protocol: TestProtocol) => {
  const wrapper = mount(ManualOneTimeProbeDialog, { ...setup, props: { open: false, target: { targetId, accountName: 'C4.1 组件账号', platform: 'anthropic', type: 'apikey', status: 'active', groupName: 'C4.1 组件分组', formalModels: [model], testConfiguration: { status: 'inherited', protocol, probeTimeoutSeconds: 20, sourceGroups: [{ adminGroupId: 'c41-group', adminGroupName: 'C4.1 组件分组', protocol, probeTimeoutSeconds: 20 }] } } } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}

describe('C4.1 Messages group setup', () => {
  it.each(['anthropic', ' Anthropic '])('offers Messages for normalized %s, saves, reads back and clears without changing legacy defaults', async (platform) => {
    const wrapper = await drawer(platform), section = wrapper.get('[data-testid="group-test-configuration"]')
    expect(section.get('option[value="messages"]').text()).toBe(copy.option)
    expect(section.text()).toContain(copy.platform)
    expect(section.text()).not.toContain(copy.behavior)
    expect(section.text()).toContain('未单独配置；无其他显式来源时沿用 Chat / 20 秒。')
    await section.get('[data-testid="group-test-protocol"]').setValue('messages')
    for (const key of ['behavior', 'health', 'conflict', 'budget'] as const) expect(section.text()).toContain(copy[key])
    await section.get('[data-testid="group-test-timeout"]').setValue('30')
    await section.get('[data-testid="group-test-save"]').trigger('click'); await flushPromises()
    expect(writes).toEqual([{ url: '/api/connection-health/admin-groups/c41-group/test-configuration', method: 'PUT', body: { configuration: { protocol: 'messages', probeTimeoutSeconds: 30 } } }])
    expect(section.text()).toContain('本组已单独配置')
    await wrapper.setProps({ open: false }); await wrapper.setProps({ open: true }); await flushPromises()
    expect((wrapper.get('[data-testid="group-test-protocol"]').element as HTMLSelectElement).value).toBe('messages')
    expect((wrapper.get('[data-testid="group-test-timeout"]').element as HTMLInputElement).value).toBe('30')
    await wrapper.get('[data-testid="group-test-clear"]').trigger('click'); await flushPromises()
    expect(writes.at(-1)!.body).toEqual({ configuration: null })
    expect((wrapper.get('[data-testid="group-test-protocol"]').element as HTMLSelectElement).value).toBe('chat_completions')
    expect((wrapper.get('[data-testid="group-test-timeout"]').element as HTMLInputElement).value).toBe('20')
    expect(wrapper.text()).not.toContain(copy.behavior)
  })
  it('hides all Messages options and explanation on other platforms', async () => {
    const wrapper = await drawer('openai'), section = wrapper.get('[data-testid="group-test-configuration"]')
    expect(section.find('option[value="messages"]').exists()).toBe(false)
    for (const key of ['platform', 'behavior', 'health', 'conflict', 'budget'] as const) expect(section.text()).not.toContain(copy[key])
    expect((section.get('select').element as HTMLSelectElement).value).toBe('chat_completions')
  })
  it('retains an existing Messages selection on another platform and preserves the draft on rejection', async () => {
    saved = { protocol: 'messages' as TestProtocol, probeTimeoutSeconds: 37 }
    const wrapper = await drawer('openai'), section = wrapper.get('[data-testid="group-test-configuration"]')
    expect(section.get('option[value="messages"]').text()).toBe(copy.option)
    expect((section.get('select').element as HTMLSelectElement).value).toBe('messages')
    expect(section.text()).not.toContain(copy.platform)
    rejectSave = true
    await section.get('[data-testid="group-test-timeout"]').setValue('40')
    await section.get('[data-testid="group-test-save"]').trigger('click'); await flushPromises()
    expect(section.text()).toContain(copy.error)
    expect(section.text()).not.toContain('已保存，影响范围')
    expect((section.get('select').element as HTMLSelectElement).value).toBe('messages')
    expect((section.get('[data-testid="group-test-timeout"]').element as HTMLInputElement).value).toBe('40')
    expect(saved).toEqual({ protocol: 'messages', probeTimeoutSeconds: 37 })
  })
  it('preserves a newly selected Messages draft and the old saved configuration on API rejection', async () => {
    saved = { protocol: 'chat_completions', probeTimeoutSeconds: 20 }
    const wrapper = await drawer(), section = wrapper.get('[data-testid="group-test-configuration"]')
    await section.get('[data-testid="group-test-protocol"]').setValue('messages')
    rejectSave = true
    await section.get('[data-testid="group-test-save"]').trigger('click'); await flushPromises()
    expect(section.text()).toContain(copy.error)
    expect((section.get('select').element as HTMLSelectElement).value).toBe('messages')
    expect(section.text()).toContain(copy.behavior)
    expect(saved).toEqual({ protocol: 'chat_completions', probeTimeoutSeconds: 20 })
  })
  it('keeps the existing inventory guard for Messages saves', async () => {
    inventoryComplete = false
    const wrapper = await drawer()
    await wrapper.get('[data-testid="group-test-protocol"]').setValue('messages')
    expect(wrapper.get('[data-testid="group-test-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="group-test-save"]').trigger('click'); await flushPromises()
    expect(writes).toEqual([])
  })
})

describe('C4.1 Messages rule presets', () => {
  it.each(['v2', 'legacy'] as const)('shows an unconfigured Messages default of six seconds when read-only under %s', (ruleVersion) => {
    const wrapper = mount(HealthRulePresetFields, { props: { modelValue: defaultHealthRulePreset(), ruleVersion, readonly: true } }); wrappers.push(wrapper)
    const row = wrapper.findAll('label').find(candidate => candidate.text().startsWith('Messages'))
    expect(row).toBeDefined(); expect(row!.findAll('span')[1]!.text()).toBe('6 秒 （协议默认值）')
    expect(row!.find('input').exists()).toBe(false)
    expect(defaultHealthRulePreset().delayLineMs).toEqual({ responses: 6000, chat_completions: 6000 })
  })
  it('saves a custom eight-second Messages line and omits its key after clearing', async () => {
    const wrapper = mount(ProbePolicyListDialog, { ...setup, props: { open: true, policies: [], deletingPolicyId: '', deleteError: '', workspaceId: 'c41-component', workspacePlatform: 'sub2api' } }); wrappers.push(wrapper); await flushPromises()
    await wrapper.get('[data-testid="manage-rule-presets"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="rule-preset-custom-c41"] [data-action="edit"]').trigger('click'); await flushPromises()
    const input = wrapper.get('input[aria-label="Messages 延迟线"]')
    expect((input.element as HTMLInputElement).value).toBe(''); expect(input.attributes('placeholder')).toBe('6')
    await input.setValue('8'); await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes.at(-1)!.body.delayLineMs.messages).toBe(8000)
    await wrapper.get('[data-testid="rule-preset-custom-c41"] [data-action="edit"]').trigger('click'); await flushPromises()
    expect((wrapper.get('input[aria-label="Messages 延迟线"]').element as HTMLInputElement).value).toBe('8')
    await wrapper.get('input[aria-label="Messages 延迟线"]').setValue('')
    await wrapper.get('[data-testid="save-rule-preset"]').trigger('click'); await flushPromises()
    expect(writes.at(-1)!.body.delayLineMs).toEqual({ responses: 6000, chat_completions: 6000 })
  })
})

describe('C4.1 Messages probe and question-answer dialogue', () => {
  it('uses the correct Messages notice in all three modes and names the once result', async () => {
    const wrapper = await dialog('messages' as TestProtocol)
    expect(wrapper.text()).toContain(copy.questionAnswer)
    expect(wrapper.text()).toContain(copy.probe); expect(wrapper.text()).not.toContain(copy.budget); expect(wrapper.text()).not.toContain(copy.legacy)
    expect(wrapper.text()).toContain('使用分组配置 · Messages / 20s')
    for (const label of ['正式手动探活', '一次性测试']) {
      await click(wrapper, label)
      expect(wrapper.text()).toContain(copy.probe); expect(wrapper.text()).toContain(copy.budget)
      expect(wrapper.text()).not.toContain(copy.questionAnswer); expect(wrapper.text()).not.toContain(copy.legacy)
    }
    await click(wrapper, '开始测试')
    expect(writes).toHaveLength(1); expect(writes[0]!.url).toContain('/manual-probe')
    const result = wrapper.findAll('li').find(candidate => candidate.text().includes(model.id) && candidate.text().includes('42ms'))
    expect(result).toBeDefined(); expect(result!.text()).toContain('Messages / 20s'); expect(result!.text()).not.toContain('旧记录未记录协议')
  })
  it.each(['chat_completions', 'responses'] as const)('retains the original notice in every %s mode', async (protocol) => {
    const wrapper = await dialog(protocol)
    for (const label of ['问答测试', '正式手动探活', '一次性测试']) {
      await click(wrapper, label)
      expect(wrapper.text()).toContain(copy.legacy)
      expect(wrapper.text()).not.toContain(copy.probe); expect(wrapper.text()).not.toContain(copy.questionAnswer); expect(wrapper.text()).not.toContain(copy.budget)
    }
  })
  it('names Messages in both shared labels and rejects forbidden developer vocabulary in all new page copy', () => {
    expect(testProtocolName('messages')).toBe('Messages')
    expect(questionAnswerRequestProtocolLabel('messages', '旧记录未记录协议')).toBe('Messages')
    expect(testProtocolName('chat_completions')).toBe('Chat Completions'); expect(testProtocolName('responses')).toBe('Responses')
    expect(testProtocolName('bogus')).toBe('')
    expect(t('admin.connectionHealth.errors.testProtocolNotAllowed')).toBe(copy.error)
    for (const key of ['messagesOption', 'messagesPlatform', 'messagesHelp', 'messagesHealthEvidence', 'messagesConflict', 'messagesBudget', 'messagesQuestionAnswerBudget']) expect(t('admin.connectionHealth.testConfiguration.' + key)).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)
    for (const key of ['contractLimitMessages']) expect(t('admin.connectionHealth.manualProbeDialog.' + key)).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)
  })
})

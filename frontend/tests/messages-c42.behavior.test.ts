// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupHealthSetupDrawer from '@/modules/admin/components/dashboard/GroupHealthSetupDrawer.vue'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import { useConnectionHealth } from '@/modules/admin/composables/useConnectionHealth'
import { defaultHealthRulePreset } from '@/modules/admin/utils/healthRulePresets'
import { t, te } from '@/locales'
import QuestionAnswerRecordCard from '@/modules/admin/components/dashboard/QuestionAnswerRecordCard.vue'
import QuestionAnswerScheduleBatchDialog from '@/modules/admin/components/dashboard/QuestionAnswerScheduleBatchDialog.vue'
import type { AdminGroupHealth, AdminGroupTestConfiguration, GroupTestConfiguration, TestProtocol, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
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
  questionAnswer: 'Messages 问答每题最多输出 16000 个 token，思考也算在内；回答因此被截断时，这道题记为“回答不完整”，不判对错。思考强度按所选档位原样发给上游。',
  legacy: '注意：测试请求只带 Base URL 和 API Key，按分组测试配置里选的协议（Chat Completions 或 Responses）发送。上游如果还需要专用代理、OAuth、TLS 证书或自定义请求头，系统拿不到，测试也不会带上，所以结果可能和真实使用不同',
}
const targetId = 'sub2api:c42-component:1'
const model = { id: 'claude-c42-fixture', name: 'claude-c42-fixture', providerFamily: 'anthropic' }
const group = (platform = 'anthropic'): AdminGroupHealth => ({
  id: 'c42-group', name: 'C4.2 组件分组', platform, status: 'active', type: 'public', isExclusive: false,
  subscriptionType: '', multiplier: 1, multiplierDisplay: '1x', accountCount: 1,
  healthSummary: { totalAccounts: 1, probeableAccounts: 1, unprobeableAccounts: 0, healthyModels: 0, degradedModels: 0, suspendedModels: 0, disabledModels: 0, unconfiguredModels: 1, lastProbeAt: null },
  accounts: [{ id: '1', name: 'C4.2 组件账号', platform: 'anthropic', type: 'apikey', status: 'active', schedulable: true, targetId, probeAvailable: true, models: model.id, modelHealth: [] }],
})
let saved: GroupTestConfiguration | null
let rejectSave: boolean
let inventoryComplete: boolean
let writes: Array<{ url: string; method: string; body: any }>
let unexpected: string[]
let savedPreset: ReturnType<typeof preset>
const preset = () => ({ ...defaultHealthRulePreset(), id: 'custom-c42', name: 'C4.2 延迟线', kind: 'custom', createdAt: '', updatedAt: '', policies: [] })
const testConfiguration = (): AdminGroupTestConfiguration => ({ adminGroupId: 'c42-group', adminGroupName: 'C4.2 组件分组', configuration: saved, inventoryComplete, affectedAccountCount: 1, conflictAccountCount: 0, accounts: [{ targetId, accountName: 'C4.2 组件账号', testConfiguration: { status: saved ? 'inherited' : 'default', protocol: saved?.protocol ?? 'chat_completions', probeTimeoutSeconds: saved?.probeTimeoutSeconds ?? 20, sourceGroups: [] } }] })
let records: QuestionAnswerRecord[]
const currentStats = () => questionAnswerFixtureStats(records)
const currentBatch = () => ({ targetId, batchId: records.length ? 'c42-batch' : '', records, active: false, reasoningEffort: 'medium', repeatCount: 1, submittedCount: records.length, completedCount: records.length, runningCount: 0, currentModel: '', currentQuestion: '', stats: currentStats() })
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const wrappers: VueWrapper[] = []
const service = useConnectionHealth()
const setup = { global: { stubs: { Teleport: true, Transition: false } } }
beforeEach(() => {
  vi.useFakeTimers()
  records = []; saved = null; rejectSave = false; inventoryComplete = true; writes = []; unexpected = []; savedPreset = preset()
  service.setAdminGroupsWorkspace(''); service.setAdminGroupsWorkspace('c42-component')
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
      if (url.endsWith('/rule-presets/custom-c42')) { savedPreset = { ...savedPreset, ...body }; return json(savedPreset) }
    } else {
      if (url.endsWith('/test-configuration')) return json(testConfiguration())
      if (url.endsWith('/policy-configuration')) return json({ adminGroupId: 'c42-group', policyIds: [], excludedTargetIds: [] })
      if (url.endsWith('/admin-groups')) return json([group()])
      if (url.endsWith('/rule-presets')) return json([savedPreset])
      if (url.endsWith('/workspace-settings')) return json({ ruleVersion: 'v2', configGeneration: 1, probeConcurrency: 6, probeConcurrencyVersion: 1, ruleSwitchedAt: null, updatedAt: '' })
      if (url.endsWith('/policies')) return json([])
      if (url.endsWith('/models')) return json([model])
      if (url.endsWith('/test-questions')) return json([])
      if (url.includes('/question-answers/history?')) return json({ batches: [], page: 1, pageSize: 20, totalBatches: 0, totalPages: 1, todayStats: currentStats() })
      if (url.endsWith('/question-answers/batches/latest')) return json(currentBatch())
      if (url.endsWith('/question-answers/batches/c42-batch')) return json(currentBatch())
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
  const wrapper = mount(ManualOneTimeProbeDialog, { ...setup, props: { open: false, target: { targetId, accountName: 'C4.2 组件账号', platform: 'anthropic', type: 'apikey', status: 'active', groupName: 'C4.2 组件分组', formalModels: [model], testConfiguration: { status: 'inherited', protocol, probeTimeoutSeconds: 20, sourceGroups: [{ adminGroupId: 'c42-group', adminGroupName: 'C4.2 组件分组', protocol, probeTimeoutSeconds: 20 }] } } } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises(); return wrapper
}


const incomplete = '回答不完整：达到每题 16000 个 token 的输出上限（思考也算在内），这道题不判对错。可以把思考强度调低后重测。'
const unsupported = 'Messages 问答只支持 API Key 类型的账号，这道题没有发出请求。请把账号所在分组的测试请求改回 Chat Completions，或在主站换成 API Key 类型的账号。'
const record = (errorType = ''): QuestionAnswerRecord => {
  const now = new Date().toISOString()
  return { id: `c42-${errorType || 'success'}`, targetId, batchId: 'c42-batch', modelName: model.id, requestProtocol: 'messages', questionId: 'c42-question', questionName: 'C4.2 组件题目', questionBody: 'Only answer OK.', questionKeywordSnapshot: ['OK'], reasoningEffort: 'medium', answerBody: errorType ? '' : 'OK', status: errorType ? 'failed' : 'succeeded', errorType, answerJudgment: errorType ? null : 'correct', judgmentSource: errorType ? null : 'automatic', repeatIndex: 1, manualError: false, createdAt: now, startedAt: now, completedAt: now, updatedAt: now }
}

describe('C4.2 Messages question-answer page behavior', () => {
  it('states the new question-answer protocol after selecting Messages without the old temporary notice', async () => {
    const wrapper = await drawer(), section = wrapper.get('[data-testid="group-test-configuration"]')
    expect(section.text()).not.toContain('问答（智商测试）也用 Messages 发送。')
    await section.get('[data-testid="group-test-protocol"]').setValue('messages')
    expect(section.text()).toContain(copy.behavior)
    expect(section.text()).not.toContain('暂时仍用 Chat Completions')
    await section.get('[data-testid="group-test-protocol"]').setValue('responses')
    expect(section.text()).not.toContain(copy.behavior)
  })
  it('shows a single Messages notice in all modes and the 16000-token guidance only for questions', async () => {
    const wrapper = await dialog('messages')
    expect(wrapper.text()).toContain(copy.probe)
    expect(wrapper.text()).toContain(copy.questionAnswer)
    expect(wrapper.text()).not.toContain(copy.budget)
    expect(wrapper.text()).not.toContain('暂时仍用 Chat Completions')
    expect(wrapper.text()).not.toContain(copy.legacy)
    for (const label of ['正式手动探活', '一次性测试']) {
      await click(wrapper, label)
      expect(wrapper.text()).toContain(copy.probe)
      expect(wrapper.text()).toContain(copy.budget)
      expect(wrapper.text()).not.toContain(copy.questionAnswer)
      expect(wrapper.text()).not.toContain('16000')
      expect(wrapper.text()).not.toContain(copy.legacy)
    }
    await click(wrapper, '问答测试')
    expect(wrapper.text()).toContain(copy.questionAnswer)
    expect(wrapper.text()).not.toContain(copy.budget)
  })
  it.each(['chat_completions', 'responses'] as const)('preserves the old %s notices and hides the Messages question budget', async protocol => {
    const wrapper = await dialog(protocol)
    for (const label of ['问答测试', '正式手动探活', '一次性测试']) {
      await click(wrapper, label)
      expect(wrapper.text()).toContain(copy.legacy)
      expect(wrapper.text()).not.toContain(copy.probe)
      expect(wrapper.text()).not.toContain(copy.questionAnswer)
      expect(wrapper.text()).not.toContain('16000')
      expect(wrapper.text()).not.toContain(copy.budget)
    }
  })
  it('shows both actionable failure explanations and Messages in the existing manual record rows', async () => {
    records = [record('response_incomplete'), record('account_type_unsupported')]
    const wrapper = await dialog('messages')
    expect(wrapper.get('[data-testid="question-answer-processed"] > button').attributes('aria-expanded')).toBe('true')
    await click(wrapper, '失败 2 条')
    const failed = wrapper.get('[data-testid="question-answer-failed-content"]')
    expect(failed.text()).toContain(incomplete)
    expect(failed.text()).toContain(unsupported)
    expect(failed.text()).not.toContain('未能取得可用的答案。')
    expect(failed.text()).not.toContain('上游鉴权失败。')
    for (const row of failed.findAll('li')) {
      expect(row.text()).toContain(`${model.id} · Messages`)
      expect(row.text()).not.toContain('旧记录未记录协议')
      expect(row.text()).not.toContain('Chat Completions ·')
    }
  })
  it('shows Messages on the existing success card and preserves both failure explanations in scheduled read-only batches', async () => {
    const success = mount(QuestionAnswerRecordCard, { props: { record: record(), expanded: true, saving: false, readOnly: true } }); wrappers.push(success)
    expect(success.text()).toContain(`${model.id} · Messages · 力度 medium`)
    expect(success.text()).not.toContain('旧记录未记录协议')
    records = [record('response_incomplete'), record('account_type_unsupported')]
    const scheduled = mount(QuestionAnswerScheduleBatchDialog, { ...setup, props: { targetId, batchId: 'c42-batch', accountName: 'C4.2 组件账号', workspace: 'c42-component' } }); wrappers.push(scheduled); await flushPromises()
    expect(scheduled.text()).toContain(incomplete)
    expect(scheduled.text()).toContain(unsupported)
    expect(scheduled.text()).not.toContain(`${model.id} · Messages`)
    expect(scheduled.find('[data-testid^="question-answer-record-"]').exists()).toBe(false)
  })
  it('removes the temporary notice key and keeps every new sentence in plain language', () => {
    expect(te('admin.connectionHealth.manualProbeDialog.contractLimitMessagesQuestionAnswer')).toBe(false)
    const sentences = [t('admin.connectionHealth.testConfiguration.messagesHelp'), t('admin.connectionHealth.testConfiguration.messagesQuestionAnswerBudget'), t('admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.response_incomplete'), t('admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.account_type_unsupported')]
    expect(sentences[1]).toBe(copy.questionAnswer)
    expect(sentences[2]).toBe(incomplete)
    expect(sentences[3]).toBe(unsupported)
    for (const sentence of sentences) {
      expect(sentence).not.toMatch(/租约|回执|收口|对账|权威|矩阵|source=|schedulable=/)
      expect(sentence).not.toContain('暂时仍用 Chat Completions')
    }
  })
})

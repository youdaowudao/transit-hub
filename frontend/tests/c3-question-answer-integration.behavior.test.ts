// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import QuestionAnswerModelControlPanel from '@/modules/admin/components/dashboard/QuestionAnswerModelControlPanel.vue'
import QuestionAnswerModelControlRow from '@/modules/admin/components/dashboard/QuestionAnswerModelControlRow.vue'
import { modelControlBusyTargets } from '@/modules/admin/utils/questionAnswerModelControl'
import type { ModelControlDecision, ModelControlItem, QuestionAnswerBatch, QuestionAnswerHistory, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'
import { questionAnswerFixtureStats } from './fixtures/c1QuestionAnswerHistory'

const api = vi.hoisted(() => Object.fromEntries(['discoverTargetModels', 'listTestQuestions', 'getQuestionAnswerHistory', 'getLatestQuestionAnswerBatch', 'getQuestionAnswerBatch', 'startQuestionAnswerBatch', 'getModelControlTarget', 'verifyModelControl', 'previewModelControl'].map(name => [name, vi.fn()])))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), ...api }))
const now = '2026-10-10T01:00:00Z', targetId = 'sub2api:ws1:a', nextTargetId = 'sub2api:ws2:b'
const wrappers: VueWrapper[] = []
const deferred = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const answer = (id: string, modelName: string, running = false, unreviewed = false): QuestionAnswerRecord => ({ id: `${id}-record`, targetId, batchId: id, modelName, questionId: 'q1', questionName: '隔离题目', questionBody: '回答答案', questionKeywordSnapshot: ['答案'], reasoningEffort: 'medium', requestProtocol: 'responses', repeatIndex: 1, answerBody: running ? '' : '答案', status: running ? 'running' : 'succeeded', errorType: '', answerJudgment: running ? null : unreviewed ? 'unreviewed' : 'correct', judgmentSource: running || unreviewed ? null : 'automatic', manualError: false, createdAt: now, startedAt: now, completedAt: running ? null : now, updatedAt: now })
const batch = (id: string, modelName = 'A', running = false, unreviewed = false): QuestionAnswerBatch => {
  const record = answer(id, modelName, running, unreviewed)
  return { batchId: id, records: [record], reasoningEffort: 'medium', repeatCount: 1, submittedCount: 1, completedCount: running ? 0 : 1, runningCount: running ? 1 : 0, active: running, currentModel: running ? modelName : '', currentQuestion: running ? '隔离题目' : '', stats: questionAnswerFixtureStats([record]) }
}
const managed = (decision: ModelControlDecision = 'close_recommended', requestedTarget = targetId, modelName = 'A'): ModelControlItem => ({ targetId: requestedTarget, accountName: requestedTarget === targetId ? '旧隔离账号' : '新工作区账号', modelName, version: 1, rule: { modelName, minAccuracyPercent: 50, minJudgedAnswers: 3, includeManual: true, includeScheduled: true, version: 1 }, round: { batchId: 'old-batch', source: 'manual', scheduleName: null, createdAt: now, completedAt: now, running: false, correct: 1, incorrect: 2, unreviewed: 0, failed: 0, cancelled: 0, accuracyPercent: 100 / 3 }, previousRound: null, decision, decisionReason: '', basis: { batchId: 'old-batch', ruleVersion: 1, decision }, control: { closedEntries: { alias: modelName }, closedAt: now, closedAccuracyPercent: 100 / 3, pending: null, unconfirmedClose: null, accountPending: null, conflictReason: '', observation: { state: 'partially_closed', reasonKey: '', sources: [], accountStatus: 'active', accountSchedulable: true, checkedAt: now }, lastAttempt: null }, coverage: { schedules: [] }, health: { recentlyProbed: false, state: null } })
let latest: QuestionAnswerBatch, authority: ModelControlItem, nextAuthority: ModelControlItem
const history = (value: QuestionAnswerBatch): QuestionAnswerHistory => ({ batches: [{ batchId: value.batchId, createdAt: now, startedAt: now, completedAt: value.active ? null : now, requestProtocol: 'responses', reasoningEffort: 'medium', models: [value.records[0]!.modelName], questions: value.stats.byQuestion, repeatCount: 1, active: value.active, stats: value.stats }], page: 1, pageSize: 20, totalBatches: 1, totalPages: 1, todayStats: value.stats })
const target = (id = targetId) => ({ targetId: id, accountName: id === targetId ? '旧隔离账号' : '新工作区账号', platform: 'openai', type: 'apikey', status: 'active', groupName: '隔离分组', formalModels: [] })
const button = (wrapper: VueWrapper, label: string) => { const found = wrapper.findAll('button').find(value => value.text() === label); if (!found) throw new Error(`missing ${label}: ${wrapper.text()}`); return found }
const panel = (wrapper: VueWrapper) => wrapper.getComponent(QuestionAnswerModelControlPanel)
const mounted = async () => {
  const wrapper = mount(ManualOneTimeProbeDialog, { props: { open: false, target: target() }, global: { stubs: { Teleport: true, Transition: false } } })
  wrappers.push(wrapper); await wrapper.setProps({ open: true }); await flushPromises()
  expect(wrapper.findComponent(QuestionAnswerModelControlPanel).exists()).toBe(true)
  expect(wrapper.findComponent(QuestionAnswerModelControlRow).exists()).toBe(true)
  expect(button(panel(wrapper), '关闭此模型').attributes('disabled')).toBeUndefined()
  expect(button(panel(wrapper), '恢复此模型').attributes('disabled')).toBeUndefined()
  return wrapper
}
const acceptBatch = (next: QuestionAnswerBatch, decision: ModelControlDecision) => {
  latest = next; authority = managed(decision); authority.basis = { ...authority.basis, batchId: next.batchId, decision }; authority.round = { ...authority.round!, batchId: next.batchId, running: next.active, completedAt: next.active ? null : now, correct: next.active || decision === 'awaiting_review' ? 0 : 1, incorrect: 0, unreviewed: decision === 'awaiting_review' ? 1 : 0, accuracyPercent: next.active || decision === 'awaiting_review' ? null : 100 }
  if (decision === 'testing') authority.previousRound = managed().round
}
const start = async (wrapper: VueWrapper, next: QuestionAnswerBatch, decision: ModelControlDecision) => {
  api.startQuestionAnswerBatch.mockImplementationOnce(async () => { acceptBatch(next, decision); return structuredClone(next) })
  await button(wrapper, '开始回答').trigger('click'); await flushPromises()
  expect(api.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)
}

beforeEach(() => {
  vi.resetAllMocks(); vi.useFakeTimers({ now: new Date(now) }); modelControlBusyTargets.value = new Set(); latest = batch('old-batch'); authority = managed(); nextAuthority = managed('usable', nextTargetId, 'B')
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
  api.discoverTargetModels.mockImplementation(async (id: string) => [{ id: id === targetId ? 'A' : 'B', name: id === targetId ? 'A' : 'B' }])
  api.listTestQuestions.mockResolvedValue([{ id: 'q1', name: '隔离题目', body: '回答答案', keywords: ['答案'], enabled: true, isDefault: true, createdAt: now, updatedAt: now }])
  api.getLatestQuestionAnswerBatch.mockImplementation(async (id: string) => structuredClone(id === targetId ? latest : batch('new-workspace-batch', 'B')))
  api.getQuestionAnswerBatch.mockImplementation(async (id: string) => structuredClone(id === targetId ? latest : batch('new-workspace-batch', 'B')))
  api.getQuestionAnswerHistory.mockImplementation(async (id: string) => structuredClone(history(id === targetId ? latest : batch('new-workspace-batch', 'B'))))
  api.getModelControlTarget.mockImplementation(async (id: string) => ({ targetId: id, items: [structuredClone(id === targetId ? authority : nextAuthority)], candidates: [] }))
  api.verifyModelControl.mockImplementation(async (ids: string[]) => ({ items: ids.map(id => structuredClone(id === targetId ? authority : nextAuthority)), errors: [] }))
})
afterEach(async () => { for (const wrapper of wrappers.splice(0)) wrapper.unmount(); await flushPromises(); expect(vi.getTimerCount()).toBe(0); modelControlBusyTargets.value = new Set(); vi.useRealTimers(); vi.restoreAllMocks(); document.body.innerHTML = '' })

describe('C3 actual question-answer dialog refreshes authoritative model control across batch transitions', () => {
  it('refreshes on manual batch start and prevents close or restore previews when authority becomes testing', async () => {
    const wrapper = await mounted(); await start(wrapper, batch('new-running', 'A', true), 'testing')
    expect(panel(wrapper).text()).toContain('测试中')
    const close = button(panel(wrapper), '关闭此模型'), restore = button(panel(wrapper), '恢复此模型')
    expect(close.attributes('disabled')).toBeDefined(); expect(restore.attributes('disabled')).toBeDefined()
    await close.trigger('click'); await restore.trigger('click'); await flushPromises(); expect(api.previewModelControl).not.toHaveBeenCalled()
  })
  it.each(['awaiting_review', 'insufficient'] as const)('refreshes a newly accepted immediately terminal batch to %s instead of retaining the old close recommendation', async decision => {
    const wrapper = await mounted(); await start(wrapper, batch('instant-terminal', 'A', false, decision === 'awaiting_review'), decision)
    expect(panel(wrapper).text()).toContain(decision === 'awaiting_review' ? '待人工判题' : '依据不足')
    expect(panel(wrapper).text()).not.toContain('建议关闭'); expect(panel(wrapper).findAll('button').some(value => value.text() === '关闭此模型')).toBe(false)
  })
  it('refreshes the same running batch when it completes, retaining pending review as the latest authority', async () => {
    const wrapper = await mounted(); await start(wrapper, batch('new-running', 'A', true), 'testing')
    acceptBatch(batch('new-running', 'A', false, true), 'awaiting_review')
    await vi.advanceTimersByTimeAsync(2000); await flushPromises()
    expect(panel(wrapper).text()).toContain('待人工判题'); expect(panel(wrapper).text()).not.toContain('建议关闭'); expect(panel(wrapper).findAll('button').some(value => value.text() === '关闭此模型')).toBe(false)
  })
  it('uses the backend scheduled decision when manual source is excluded, without locally inventing testing', async () => {
    authority.rule.includeManual = false; authority.round!.source = 'scheduled'; authority.round!.scheduleName = '来源过滤计划'
    const wrapper = await mounted(), readsBeforeStart = api.getModelControlTarget.mock.calls.length
    api.startQuestionAnswerBatch.mockImplementationOnce(async () => { latest = batch('excluded-manual', 'A', true); return structuredClone(latest) })
    await button(wrapper, '开始回答').trigger('click'); await flushPromises()
    expect(api.getModelControlTarget.mock.calls.length).toBeGreaterThan(readsBeforeStart)
    expect(panel(wrapper).text()).toContain('来源过滤计划'); expect(panel(wrapper).text()).toContain('建议关闭'); expect(panel(wrapper).text()).not.toContain('测试中')
    expect(button(panel(wrapper), '关闭此模型').attributes('disabled')).toBeUndefined(); expect(button(panel(wrapper), '恢复此模型').attributes('disabled')).toBeUndefined()
  })
  it('discards a late old-target batch after switching to a new workspace and preserves the new panel authority', async () => {
    const wrapper = await mounted(), pending = deferred<QuestionAnswerBatch>()
    api.startQuestionAnswerBatch.mockReturnValueOnce(pending.promise); await button(wrapper, '开始回答').trigger('click'); await flushPromises()
    await wrapper.setProps({ target: target(nextTargetId) }); await flushPromises()
    expect(panel(wrapper).get('[data-testid="model-control-row"]').attributes('data-target-id')).toBe(nextTargetId)
    const readsBeforeLateBatch = api.getModelControlTarget.mock.calls.length
    pending.resolve(batch('late-old-target', 'A', true)); await flushPromises()
    expect(panel(wrapper).get('[data-testid="model-control-row"]').attributes('data-model-name')).toBe('B'); expect(panel(wrapper).text()).toContain('新工作区账号'); expect(panel(wrapper).text()).toContain('可用'); expect(panel(wrapper).text()).not.toContain('测试中'); expect(panel(wrapper).text()).not.toContain('旧隔离账号'); expect(api.getModelControlTarget.mock.calls.length).toBe(readsBeforeLateBatch)
  })
})

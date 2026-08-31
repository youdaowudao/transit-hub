// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog, {
  type ManualProbeTargetSummary,
} from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import type {
  QuestionAnswerBatch,
  QuestionAnswerHistory,
  QuestionAnswerModelStats,
  QuestionAnswerRecord,
  QuestionAnswerStats,
} from '@/modules/admin/types/connectionHealth'

const harness = vi.hoisted(() => ({
  discoverModels: vi.fn(),
  listTestQuestions: vi.fn(),
  getQuestionAnswerHistory: vi.fn(),
  getLatestQuestionAnswerBatch: vi.fn(),
  getQuestionAnswerBatch: vi.fn(),
  cancelQuestionAnswerBatch: vi.fn(),
  startQuestionAnswerBatch: vi.fn(),
  setQuestionAnswerJudgment: vi.fn(),
  setTargetIntelligenceWeight: vi.fn(),
}))

vi.mock('@/modules/admin/composables/useConnectionHealth', () => ({
  connectionHealthMessageKey: (key: string) => key,
  connectionHealthRecordColorClass: () => '',
  formatConnectionHealthTime: (value: string) => value,
  useConnectionHealth: () => ({
    discoverModels: harness.discoverModels,
    runManualProbeOnce: vi.fn(),
    manualProbeTarget: vi.fn(),
    errorKey: { value: '' },
  }),
}))

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  cancelQuestionAnswerBatch: harness.cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch: harness.getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch: harness.getQuestionAnswerBatch,
  getQuestionAnswerHistory: harness.getQuestionAnswerHistory,
  listTestQuestions: harness.listTestQuestions,
  setQuestionAnswerJudgment: harness.setQuestionAnswerJudgment,
  setTargetIntelligenceWeight: harness.setTargetIntelligenceWeight,
  startQuestionAnswerBatch: harness.startQuestionAnswerBatch,
}))

const requestStats = (
  submitted: number,
  inProgress: number,
  succeeded: number,
  failed: number,
  cancelled: number,
) => ({ submitted, inProgress, succeeded, failed, cancelled })

const reviewStats = (unreviewed: number, correct: number, incorrect: number) => ({
  unreviewed,
  correct,
  incorrect,
})

const modelStats = (
  modelName: string,
  requests = requestStats(0, 0, 0, 0, 0),
  reviews = reviewStats(0, 0, 0),
): QuestionAnswerModelStats => ({ modelName, requests, reviews })

const stats = (
  requests = requestStats(0, 0, 0, 0, 0),
  reviews = reviewStats(0, 0, 0),
  byModel: QuestionAnswerModelStats[] = [],
): QuestionAnswerStats => ({ requests, reviews, byModel })

const record = (overrides: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({
  id: 'record-1',
  targetId: 'sub2api:ws1:acc-repeat',
  batchId: 'batch-repeat',
  modelName: 'model-a',
  questionId: 'q1',
  questionName: 'Question 1',
  questionBody: 'Question body 1',
  questionKeywordSnapshot: null,
  reasoningEffort: 'medium',
  answerBody: '',
  status: 'pending',
  errorType: '',
  answerJudgment: null,
  manualError: false,
  createdAt: '2026-08-31T00:00:00Z',
  startedAt: null,
  completedAt: null,
  updatedAt: '2026-08-31T00:00:00Z',
  ...overrides,
})

const emptyBatch = (overrides: Partial<QuestionAnswerBatch> = {}): QuestionAnswerBatch => ({
  batchId: '',
  records: [],
  reasoningEffort: null,
  repeatCount: 1,
  submittedCount: 0,
  completedCount: 0,
  runningCount: 0,
  active: false,
  currentModel: '',
  currentQuestion: '',
  stats: stats(),
  ...overrides,
})

const history = (
  lifetime = stats(),
  today = stats(),
): QuestionAnswerHistory => ({
  records: [],
  page: 1,
  pageSize: 20,
  totalItems: 0,
  totalPages: 0,
  stats: lifetime,
  todayStats: today,
})

const questions = Array.from({ length: 6 }, (_, index) => ({
  id: `q${index + 1}`,
  name: `Question ${index + 1}`,
  body: `Question body ${index + 1}`,
  keywords: [],
  enabled: true,
  isDefault: index === 0,
  createdAt: '2026-08-31T00:00:00Z',
  updatedAt: '2026-08-31T00:00:00Z',
}))

const primaryTarget: ManualProbeTargetSummary = {
  targetId: 'sub2api:ws1:acc-repeat',
  accountName: 'Repeat Account',
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  groupName: 'Group Repeat',
  formalModels: [],
  intelligenceWeight: null,
}

const mountedWrappers: VueWrapper[] = []

beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({
    models: [
      { id: 'model-a', name: 'Model A' },
      { id: 'model-b', name: 'Model B' },
    ],
  })
  harness.listTestQuestions.mockReset().mockResolvedValue(questions)
  harness.getQuestionAnswerHistory.mockReset().mockResolvedValue(history())
  harness.getLatestQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.getQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.cancelQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch())
  harness.startQuestionAnswerBatch.mockReset().mockResolvedValue(emptyBatch({ batchId: 'batch-started' }))
  harness.setQuestionAnswerJudgment.mockReset()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

const mountDialog = async () => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: { open: false, target: primaryTarget },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

const repeatSelect = (wrapper: VueWrapper) => {
  const select = wrapper.findAll('select').find(candidate => candidate.findAll('option').length === 10)
  if (!select) throw new Error('missing fixed 1-10 repeat select')
  return select
}

const toggleLabeledCheckbox = async (wrapper: VueWrapper, testId: string, labelText: string) => {
  const container = wrapper.find(`[data-testid="${testId}"]`)
  const label = container.findAll('label').find(candidate => candidate.text().includes(labelText))
  if (!label) throw new Error(`missing ${labelText} checkbox`)
  await label.find('input[type="checkbox"]').trigger('change')
}

const startButton = (wrapper: VueWrapper) => {
  const button = wrapper.findAll('button').find(candidate => candidate.text().trim() === '开始回答')
  if (!button) throw new Error('missing start question-answer button')
  return button
}

describe('question-answer repeat, queue and model statistics', () => {
  it('defaults to one repeat and exposes exactly the fixed 1-10 options', async () => {
    const wrapper = await mountDialog()
    const select = repeatSelect(wrapper)

    expect((select.element as HTMLSelectElement).value).toBe('1')
    expect(select.findAll('option').map(option => option.attributes('value'))).toEqual(
      Array.from({ length: 10 }, (_, index) => String(index + 1)),
    )
  })

  it('shows the full formula and submits the selected repeat count', async () => {
    const wrapper = await mountDialog()
    await toggleLabeledCheckbox(wrapper, 'question-answer-models', 'Model B')
    await toggleLabeledCheckbox(wrapper, 'question-answer-questions', 'Question 2')
    await toggleLabeledCheckbox(wrapper, 'question-answer-questions', 'Question 3')
    await repeatSelect(wrapper).setValue('4')

    expect(wrapper.text()).toContain('模型 2 个 × 问题 3 个 × 每组合 4 次 = 共 24 次请求')
    await startButton(wrapper).trigger('click')
    await flushPromises()

    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledWith(
      primaryTarget.targetId,
      ['model-a', 'model-b'],
      ['q1', 'q2', 'q3'],
      'medium',
      4,
      expect.any(AbortSignal),
    )
  })

  it('blocks 60 requests and explains the actual total, limit and correction direction', async () => {
    harness.discoverModels.mockResolvedValue({ models: [{ id: 'model-a', name: 'Model A' }] })
    const wrapper = await mountDialog()
    for (let index = 2; index <= 6; index += 1) {
      await toggleLabeledCheckbox(wrapper, 'question-answer-questions', `Question ${index}`)
    }
    await repeatSelect(wrapper).setValue('10')

    expect(wrapper.text()).toContain('共 60 次请求')
    expect(wrapper.text()).toContain('50')
    expect(wrapper.text()).toContain('减少模型、问题或次数')
    expect(startButton(wrapper).attributes('disabled')).toBeDefined()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })

  it.each([
    'admin.connectionHealth.errors.questionAnswerRepeatCount',
    'admin.connectionHealth.errors.questionAnswerBatchLimit',
  ])('keeps all four selections after backend rejection %s', async (errorKey) => {
    harness.startQuestionAnswerBatch.mockRejectedValue(new Error(errorKey))
    const wrapper = await mountDialog()
    await toggleLabeledCheckbox(wrapper, 'question-answer-models', 'Model B')
    await toggleLabeledCheckbox(wrapper, 'question-answer-questions', 'Question 2')
    await repeatSelect(wrapper).setValue('4')
    await wrapper.find('input[name="question-answer-reasoning-effort"][value="high"]').setValue(true)

    await startButton(wrapper).trigger('click')
    await flushPromises()

    expect((repeatSelect(wrapper).element as HTMLSelectElement).value).toBe('4')
    expect((wrapper.find('input[name="question-answer-reasoning-effort"][value="high"]').element as HTMLInputElement).checked).toBe(true)
    const checkedModels = wrapper.find('[data-testid="question-answer-models"]')
      .findAll('input[type="checkbox"]')
      .filter(input => (input.element as HTMLInputElement).checked)
    const checkedQuestions = wrapper.find('[data-testid="question-answer-questions"]')
      .findAll('input[type="checkbox"]')
      .filter(input => (input.element as HTMLInputElement).checked)
    expect(checkedModels).toHaveLength(2)
    expect(checkedQuestions).toHaveLength(2)
  })

  it('restores an active repeat count and renders real waiting, running, completed and response-only model buckets', async () => {
    const currentStats = stats(
      requestStats(12, 5, 0, 7, 0),
      reviewStats(0, 0, 0),
      [
        modelStats('model-active', requestStats(10, 5, 0, 5, 0)),
        modelStats('current-failed', requestStats(2, 0, 0, 2, 0)),
      ],
    )
    const active = emptyBatch({
      batchId: 'batch-active-repeat',
      records: [record({ batchId: 'batch-active-repeat', modelName: 'model-active', status: 'running', startedAt: '2026-08-31T00:00:01Z' })],
      reasoningEffort: 'high',
      repeatCount: 4,
      submittedCount: 12,
      completedCount: 7,
      runningCount: 2,
      active: true,
      stats: currentStats,
    })
    const lifetime = stats(
      requestStats(3, 0, 0, 3, 0),
      reviewStats(0, 0, 0),
      [modelStats('lifetime-failed', requestStats(3, 0, 0, 3, 0))],
    )
    const today = stats(
      requestStats(1, 0, 1, 0, 0),
      reviewStats(1, 0, 0),
      [modelStats('today-model', requestStats(1, 0, 1, 0, 0), reviewStats(1, 0, 0))],
    )
    harness.discoverModels.mockResolvedValue({
      models: [
        { id: 'model-active', name: 'Model Active' },
        { id: 'discovery-only-model', name: 'Discovery Only Model' },
      ],
    })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(active)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(lifetime, today))

    const wrapper = await mountDialog()

    expect(wrapper.get('[data-testid="question-answer-configuration"]').text()).toContain('每组合 4 次')
    expect(wrapper.text()).toContain('问答仍在运行，正在等待可复审回答。')
    expect(wrapper.text()).not.toContain('等待 3 · 运行 2 · 完成 7')
    expect(wrapper.text()).not.toContain('正在排队，会自动开始，请勿重复提交')
    expect(wrapper.text()).not.toMatch(/队列第|预计完成|前面还有/)
    const modelStatsToggle = wrapper.findAll('button').find(button => button.text().trim() === '按模型查看')
    if (!modelStatsToggle) throw new Error('missing collapsed model statistics action')
    await modelStatsToggle.trigger('click')
    const modelBucketTexts = wrapper.findAll('[data-testid="question-answer-model-stats"]')
      .map(bucket => bucket.text())
    const modelBucketText = modelBucketTexts.join('\n')
    expect(modelBucketText).toContain('model-active')
    expect(modelBucketText).toContain('current-failed')
    expect(modelBucketText).toContain('lifetime-failed')
    expect(modelBucketText).toContain('today-model')
    expect(modelBucketText).not.toContain('discovery-only-model')
    expect(modelBucketTexts.find(text => text.includes('model-active'))).toContain('失败数5')
    expect(modelBucketTexts.find(text => text.includes('current-failed'))).toContain('失败数2')
    expect(modelBucketTexts.find(text => text.includes('lifetime-failed'))).toContain('失败数3')
    expect(modelBucketTexts.find(text => text.includes('today-model'))).toContain('回答数1')
  })

  it('never shows queue wording or invented position and ETA for a terminal batch', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(emptyBatch({
      batchId: 'batch-terminal',
      repeatCount: 4,
      submittedCount: 1,
      completedCount: 1,
      records: [record({
        batchId: 'batch-terminal',
        status: 'failed',
        errorType: 'network',
        completedAt: '2026-08-31T00:00:02Z',
      })],
      stats: stats(requestStats(1, 0, 0, 1, 0), reviewStats(0, 0, 0), [
        modelStats('terminal-model', requestStats(1, 0, 0, 1, 0)),
      ]),
    }))

    const wrapper = await mountDialog()

    expect(wrapper.text()).not.toContain('正在排队，会自动开始，请勿重复提交')
    expect(wrapper.text()).not.toMatch(/队列第|预计完成|前面还有/)
  })

  it('reconciles current total and model reviews immediately from the authoritative PUT record while GET refreshes hang', async () => {
    const unreviewed = record({
      id: 'record-judgment',
      batchId: 'batch-judgment',
      modelName: 'judge-model',
      questionName: 'Judgment question',
      answerBody: 'Judgment answer',
      status: 'succeeded',
      answerJudgment: 'unreviewed',
      startedAt: '2026-08-31T00:00:01Z',
      completedAt: '2026-08-31T00:00:02Z',
    })
    const judgmentBatch = emptyBatch({
      batchId: 'batch-judgment',
      records: [unreviewed],
      reasoningEffort: 'medium',
      submittedCount: 1,
      completedCount: 1,
      stats: stats(
        requestStats(1, 0, 1, 0, 0),
        reviewStats(1, 0, 0),
        [modelStats('judge-model', requestStats(1, 0, 1, 0, 0), reviewStats(1, 0, 0))],
      ),
    })
    let historyReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(judgmentBatch)
    harness.getQuestionAnswerHistory.mockImplementation(() => {
      historyReads += 1
      return historyReads === 1 ? Promise.resolve(history()) : new Promise(() => {})
    })
    harness.getQuestionAnswerBatch.mockImplementation(() => new Promise(() => {}))
    harness.setQuestionAnswerJudgment.mockResolvedValue({
      ...unreviewed,
      answerJudgment: 'correct',
    })
    const wrapper = await mountDialog()
    const row = wrapper.findAll('li').find(candidate => candidate.text().includes('Judgment question'))
    if (!row) throw new Error('missing judgment row')
    const correct = row.findAll('button').find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')

    await correct.trigger('click')
    await flushPromises()

    expect(harness.setQuestionAnswerJudgment).toHaveBeenCalledTimes(1)
    expect(harness.getQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    const currentStats = wrapper.find('[data-testid="question-answer-stats-review"]')
    expect(wrapper.find('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
    expect(currentStats.text()).toMatch(/正确\s*1/)
    const modelStatsToggle = wrapper.findAll('button').find(button => button.text().trim() === '按模型查看')
    if (!modelStatsToggle) throw new Error('missing model statistics action after judgment')
    await modelStatsToggle.trigger('click')
    const judgeModel = wrapper.findAll('[data-testid="question-answer-model-stats"]')
      .find(bucket => bucket.text().includes('judge-model'))
    if (!judgeModel) throw new Error('missing judge-model statistics')
    expect(judgeModel.text()).toMatch(/正确\s*1/)
  })
})

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
import * as questionAnswerUtils from '@/modules/admin/utils/questionAnswers'

const harness = vi.hoisted(() => ({
  discoverModels: vi.fn(),
  listTestQuestions: vi.fn(),
  getQuestionAnswerHistory: vi.fn(),
  getLatestQuestionAnswerBatch: vi.fn(),
  getQuestionAnswerBatch: vi.fn(),
  cancelQuestionAnswerBatch: vi.fn(),
  startQuestionAnswerBatch: vi.fn(),
  setQuestionAnswerJudgment: vi.fn(),
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
  startQuestionAnswerBatch: harness.startQuestionAnswerBatch,
}))

const stats = (
  submitted: number,
  succeeded: number,
  failed: number,
  correct: number,
  incorrect: number,
): QuestionAnswerStats => ({
  requests: { submitted, inProgress: 0, succeeded, failed, cancelled: 0 },
  reviews: { unreviewed: Math.max(0, succeeded - correct - incorrect), correct, incorrect },
  byModel: [],
})

const modelStats = (
  modelName: string,
  value: QuestionAnswerStats,
): QuestionAnswerModelStats => ({
  modelName,
  requests: value.requests,
  reviews: value.reviews,
})

const record = (overrides: Partial<QuestionAnswerRecord>): QuestionAnswerRecord => ({
  id: 'record-1',
  targetId: 'sub2api:workspace:account',
  batchId: 'batch-current',
  modelName: 'gpt-5.6-sol',
  questionId: 'question-1',
  questionName: 'Question 1',
  questionBody: 'Question body',
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

const target: ManualProbeTargetSummary = {
  targetId: 'sub2api:workspace:account',
  accountName: 'Answer Account',
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  groupName: 'OpenAI Group A',
  formalModels: [],
}

const history = (lifetime: QuestionAnswerStats, today: QuestionAnswerStats): QuestionAnswerHistory => ({
  records: [],
  page: 1,
  pageSize: 20,
  totalItems: 0,
  totalPages: 0,
  stats: lifetime,
  todayStats: today,
})

const batch = (value: QuestionAnswerStats): QuestionAnswerBatch => ({
  batchId: 'batch-current',
  records: [],
  reasoningEffort: 'medium',
  repeatCount: 1,
  submittedCount: value.requests.submitted,
  completedCount: value.requests.succeeded + value.requests.failed + value.requests.cancelled,
  runningCount: 0,
  active: false,
  currentModel: '',
  currentQuestion: '',
  stats: value,
})

const mountedWrappers: VueWrapper[] = []

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({
    models: [
      { id: 'gpt-5.6-sol', name: 'gpt-5.6-sol' },
      { id: 'gpt-5.6-terra', name: 'gpt-5.6-terra' },
    ],
  })
  harness.listTestQuestions.mockReset().mockResolvedValue([{
    id: 'question-1',
    name: 'Question 1',
    body: 'Question body',
    keywords: [],
    enabled: true,
    isDefault: true,
    createdAt: '2026-08-31T00:00:00Z',
    updatedAt: '2026-08-31T00:00:00Z',
  }])
  harness.getQuestionAnswerBatch.mockReset()
  harness.cancelQuestionAnswerBatch.mockReset()
  harness.startQuestionAnswerBatch.mockReset()
  harness.setQuestionAnswerJudgment.mockReset()
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

const mountDialog = async (questionAnswerPreferences?: {
  modelIds: string[]
  questionIds: string[]
  reasoningEffort: 'low' | 'medium' | 'high' | 'xhigh'
  repeatCount: number
}) => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: { open: false, target, ...(questionAnswerPreferences ? { questionAnswerPreferences } : {}) },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

describe('question-answer compact layout primitives', () => {
  it('calculates accuracy from all submitted answers and formats one meaningful decimal', () => {
    const accuracy = (questionAnswerUtils as typeof questionAnswerUtils & {
      questionAnswerAccuracy?: (value: QuestionAnswerStats) => number | null
      formatQuestionAnswerAccuracy?: (value: number | null) => string
    }).questionAnswerAccuracy
    const formatAccuracy = (questionAnswerUtils as typeof questionAnswerUtils & {
      formatQuestionAnswerAccuracy?: (value: number | null) => string
    }).formatQuestionAnswerAccuracy

    expect(typeof accuracy).toBe('function')
    expect(typeof formatAccuracy).toBe('function')
    if (!accuracy || !formatAccuracy) return

    expect(formatAccuracy(accuracy(stats(4, 3, 1, 3, 0)))).toBe('75%')
    expect(formatAccuracy(accuracy(stats(3, 2, 1, 2, 0)))).toBe('66.7%')
    expect(formatAccuracy(accuracy(stats(4, 1, 3, 0, 1)))).toBe('0%')
    expect(formatAccuracy(accuracy(stats(0, 0, 0, 0, 0)))).toBe('-')
  })

  it('partitions only current-batch reviewable, reviewed and failed answers', () => {
    const partition = (questionAnswerUtils as typeof questionAnswerUtils & {
      partitionQuestionAnswerReviewRecords?: (records: QuestionAnswerRecord[]) => {
        pendingReview: QuestionAnswerRecord[]
        reviewed: QuestionAnswerRecord[]
        failed: QuestionAnswerRecord[]
      }
    }).partitionQuestionAnswerReviewRecords

    expect(typeof partition).toBe('function')
    if (!partition) return

    const result = partition([
      record({ id: 'pending' }),
      record({ id: 'running', status: 'running' }),
      record({ id: 'unreviewed', status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'answer' }),
      record({ id: 'correct', status: 'succeeded', answerJudgment: 'correct', answerBody: 'answer' }),
      record({ id: 'incorrect', status: 'succeeded', answerJudgment: 'incorrect', answerBody: 'answer' }),
      record({ id: 'failed', status: 'failed', errorType: 'network' }),
      record({ id: 'cancelled', status: 'cancelled' }),
    ])

    expect(result.pendingReview.map(item => item.id)).toEqual(['unreviewed'])
    expect(result.reviewed.map(item => item.id)).toEqual(['correct', 'incorrect'])
    expect(result.failed.map(item => item.id)).toEqual(['failed'])
  })

  it('renders review, today and lifetime compact statistics with model details collapsed', async () => {
    const reviewBase = stats(3, 2, 1, 2, 0)
    const todayBase = stats(4, 3, 1, 3, 0)
    const lifetimeBase = stats(5, 3, 2, 2, 1)
    const review = { ...reviewBase, byModel: [modelStats('gpt-5.6-sol', reviewBase)] }
    const today = { ...todayBase, byModel: [modelStats('gpt-5.6-terra', todayBase)] }
    const lifetime = { ...lifetimeBase, byModel: [modelStats('gpt-5.6-sol', lifetimeBase)] }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(lifetime, today))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(batch(review))

    const wrapper = await mountDialog()
    const bar = wrapper.find('[data-testid="question-answer-stats-bar"]')
    expect(bar.exists()).toBe(true)
    if (!bar.exists()) return

    expect(bar.findAll('[data-testid^="question-answer-stats-"]').map(item => item.attributes('data-testid'))).toEqual([
      'question-answer-stats-review',
      'question-answer-stats-today',
      'question-answer-stats-lifetime',
    ])
    expect(bar.text()).toContain('复审批次')
    expect(bar.text()).toContain('今日（东八区）')
    expect(bar.text()).toContain('累计')
    for (const label of ['回答数', '失败数', '正确', '错误', '正确率']) {
      expect(bar.text()).toContain(label)
    }
    for (const removedLabel of ['提交', '进行中', '已取消', '待复审', '成功回答', '请求失败']) {
      expect(bar.text()).not.toContain(removedLabel)
    }
    expect(bar.find('[data-testid="question-answer-stats-review"] [data-testid="question-answer-accuracy"]').text()).toBe('66.7%')
    expect(bar.find('[data-testid="question-answer-stats-today"] [data-testid="question-answer-accuracy"]').text()).toBe('75%')
    expect(bar.find('[data-testid="question-answer-stats-lifetime"] [data-testid="question-answer-accuracy"]').text()).toBe('40%')
    expect(bar.find('[data-testid="question-answer-accuracy"]').classes()).toEqual(expect.arrayContaining(['text-2xl', 'text-primary']))
    expect(bar.find('[data-testid="question-answer-periods"]').classes()).toEqual(expect.arrayContaining([
      'grid-cols-1',
      'md:grid-cols-3',
    ]))
    expect(bar.find('[data-testid="question-answer-stats-review"] dl').classes()).toEqual(expect.arrayContaining([
      'grid-cols-3',
      'sm:grid-cols-5',
    ]))
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(0)

    const modelToggle = bar.find('button')
    expect(modelToggle.text()).toContain('按模型查看')
    await modelToggle.trigger('click')
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(3)
    expect(bar.text()).toContain('gpt-5.6-sol')
    expect(bar.text()).toContain('gpt-5.6-terra')
    await modelToggle.trigger('click')
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(0)
  })

  it('places pending answers before collapsed processed answers and configuration', async () => {
    const records = [
      record({ id: 'unreviewed', questionName: 'Needs review', status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'Pending answer' }),
      record({ id: 'correct', questionName: 'Already correct', status: 'succeeded', answerJudgment: 'correct', answerBody: 'Correct answer' }),
      record({ id: 'incorrect', questionName: 'Already incorrect', status: 'succeeded', answerJudgment: 'incorrect', answerBody: 'Incorrect answer' }),
      record({ id: 'failed', questionName: 'Request failed detail', status: 'failed', errorType: 'network' }),
      record({ id: 'cancelled', questionName: 'Cancelled detail', status: 'cancelled' }),
      record({ id: 'running', questionName: 'Running detail', status: 'running' }),
    ]
    const reviewStats = stats(6, 3, 1, 1, 1)
    const reviewBatch: QuestionAnswerBatch = {
      ...batch(reviewStats),
      records,
      submittedCount: 6,
      completedCount: 5,
      active: true,
      runningCount: 1,
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(reviewStats, reviewStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(reviewBatch)

    const wrapper = await mountDialog()
    const orderedSections = wrapper.findAll('[data-question-answer-section]').map(section => section.attributes('data-question-answer-section'))
    expect(orderedSections).toEqual(['stats', 'pending', 'processed', 'configuration', 'history'])

    const pending = wrapper.find('[data-testid="question-answer-pending"]')
    expect(pending.text()).toContain('当前待审回答')
    expect(pending.text()).toContain('Needs review')
    expect(pending.text()).not.toContain('Already correct')
    expect(pending.text()).not.toContain('Request failed detail')
    expect(pending.text()).not.toContain('Running detail')

    const processed = wrapper.find('[data-testid="question-answer-processed"]')
    expect(processed.text()).toContain('本批次已处理 2 条 · 正确 1 · 错误 1')
    expect(processed.text()).not.toContain('Already correct')
    expect(processed.text()).not.toContain('Request failed detail')
    expect(wrapper.text()).not.toContain('Cancelled detail')

    await processed.find('button').trigger('click')
    expect(processed.text()).toContain('Already correct')
    expect(processed.text()).toContain('Already incorrect')
    expect(processed.text()).toContain('失败 1 条')
    expect(processed.text()).not.toContain('Request failed detail')
    const failedToggle = processed.findAll('button').find(button => button.text().includes('失败 1 条'))
    if (!failedToggle) throw new Error('missing failed answers toggle')
    await failedToggle.trigger('click')
    expect(processed.text()).toContain('Request failed detail')
  })

  it('moves a judged answer into the still-collapsed processed section and supports rejudgment', async () => {
    let serverRecords = [
      record({ id: 'first-review', questionName: 'First review', status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'First answer' }),
      record({ id: 'second-review', questionName: 'Second review', status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'Second answer' }),
      record({ id: 'existing-correct', questionName: 'Existing correct', status: 'succeeded', answerJudgment: 'correct', answerBody: 'Existing answer' }),
    ]
    const serverStats = () => stats(
      3,
      3,
      0,
      serverRecords.filter(item => item.answerJudgment === 'correct').length,
      serverRecords.filter(item => item.answerJudgment === 'incorrect').length,
    )
    const serverBatch = (): QuestionAnswerBatch => ({ ...batch(serverStats()), records: serverRecords })
    harness.getQuestionAnswerHistory.mockImplementation(async () => history(serverStats(), serverStats()))
    harness.getLatestQuestionAnswerBatch.mockImplementation(async () => serverBatch())
    harness.getQuestionAnswerBatch.mockImplementation(async () => serverBatch())
    harness.setQuestionAnswerJudgment.mockImplementation(async (_targetId, recordId, judgment) => {
      let authoritative: QuestionAnswerRecord | undefined
      serverRecords = serverRecords.map((item) => {
        if (item.id !== recordId) return item
        authoritative = { ...item, answerJudgment: judgment, manualError: judgment === 'incorrect' }
        return authoritative
      })
      if (!authoritative) throw new Error('missing fixture record')
      return authoritative
    })

    const wrapper = await mountDialog()
    const pending = wrapper.find('[data-testid="question-answer-pending"]')
    const firstReviewCard = pending.findAll('li').find(item => item.text().includes('First review'))
    if (!firstReviewCard) throw new Error('missing first review card')
    const correctButton = firstReviewCard.findAll('button').find(button => button.text().trim() === '正确')
    if (!correctButton) throw new Error('missing correct judgment button')
    await correctButton.trigger('click')
    await flushPromises()

    expect(pending.text()).not.toContain('First review')
    expect(pending.text()).toContain('Second review')
    const processed = wrapper.find('[data-testid="question-answer-processed"]')
    expect(processed.text()).toContain('本批次已处理 2 条 · 正确 2 · 错误 0')
    expect(processed.text()).not.toContain('First review')

    await processed.find('button').trigger('click')
    const firstProcessedCard = processed.findAll('li').find(item => item.text().includes('First review'))
    if (!firstProcessedCard) throw new Error('missing processed first review card')
    const incorrectButton = firstProcessedCard.findAll('button').find(button => button.text().trim() === '错误')
    if (!incorrectButton) throw new Error('missing incorrect rejudgment button')
    await incorrectButton.trigger('click')
    await flushPromises()

    expect(processed.text()).toContain('本批次已处理 2 条 · 正确 1 · 错误 1')
    expect(incorrectButton.attributes('aria-pressed')).toBe('true')
  })

  it('collapses a valid remembered OpenAI configuration and edits through the existing preference event', async () => {
    const empty = stats(0, 0, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(empty, empty))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(empty), batchId: '' })
    const wrapper = await mountDialog({
      modelIds: ['gpt-5.6-sol', 'gpt-5.6-terra'],
      questionIds: ['question-1'],
      reasoningEffort: 'high',
      repeatCount: 3,
    })

    const configuration = wrapper.find('[data-testid="question-answer-configuration"]')
    expect(configuration.text()).toContain('gpt-5.6-sol、gpt-5.6-terra')
    expect(configuration.text()).toContain('问题 1 个 · 推理力度 高 · 每组合 3 次')
    expect(configuration.text()).toContain('已记住（本浏览器，当前管理员的 OpenAI 分组共用）')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    expect(configuration.find('[data-testid="question-answer-questions"]').exists()).toBe(false)
    expect(configuration.findAll('button').some(button => button.text().includes('保存'))).toBe(false)

    const modifyButton = configuration.findAll('button').find(button => button.text().trim() === '修改')
    if (!modifyButton) throw new Error('missing modify configuration button')
    await modifyButton.trigger('click')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(true)
    const terraLabel = configuration.find('[data-testid="question-answer-models"]')
      .findAll('label').find(label => label.text().includes('gpt-5.6-terra'))
    if (!terraLabel) throw new Error('missing terra model option')
    await terraLabel.find('input').trigger('change')

    expect(wrapper.emitted('question-answer-preferences-changed')?.at(-1)?.[0]).toEqual({
      modelIds: ['gpt-5.6-sol'],
      questionIds: ['question-1'],
      reasoningEffort: 'high',
      repeatCount: 3,
    })
  })

  it('collapses a valid remembered configuration regardless of whether models or question data resolve first', async () => {
    const empty = stats(0, 0, 0, 0, 0)
    const preferences = {
      modelIds: ['gpt-5.6-sol'],
      questionIds: ['question-1'],
      reasoningEffort: 'high' as const,
      repeatCount: 3,
    }

    for (const modelsResolveFirst of [false, true]) {
      const modelsResult = deferred<{ models: Array<{ id: string; name: string }> }>()
      const historyResult = deferred<QuestionAnswerHistory>()
      harness.discoverModels.mockReset().mockReturnValue(modelsResult.promise)
      harness.getQuestionAnswerHistory.mockReset().mockReturnValue(historyResult.promise)
      harness.getLatestQuestionAnswerBatch.mockReset().mockResolvedValue({ ...batch(empty), batchId: '' })

      const wrapper = mount(ManualOneTimeProbeDialog, {
        props: { open: false, target, questionAnswerPreferences: preferences },
        global: { stubs: { Teleport: true, Transition: false } },
      })
      mountedWrappers.push(wrapper)
      await wrapper.setProps({ open: true })

      if (modelsResolveFirst) {
        modelsResult.resolve({ models: [{ id: 'gpt-5.6-sol', name: 'gpt-5.6-sol' }] })
        await flushPromises()
        historyResult.resolve(history(empty, empty))
      } else {
        historyResult.resolve(history(empty, empty))
        await flushPromises()
        modelsResult.resolve({ models: [{ id: 'gpt-5.6-sol', name: 'gpt-5.6-sol' }] })
      }
      await flushPromises()

      const configuration = wrapper.find('[data-testid="question-answer-configuration"]')
      expect(configuration.text()).toContain('问题 1 个 · 推理力度 高 · 每组合 3 次')
      expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
      wrapper.unmount()
      mountedWrappers.splice(mountedWrappers.indexOf(wrapper), 1)
    }
  })

  it('shows the initial history load error instead of zero-valued statistics', async () => {
    const empty = stats(0, 0, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockRejectedValue(new Error('admin.connectionHealth.errors.request'))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(empty), batchId: '' })

    const wrapper = await mountDialog()

    expect(wrapper.find('[data-testid="question-answer-stats-error"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="question-answer-stats-bar"]').exists()).toBe(false)
  })

  it('hides the processed region when the batch has neither reviewed nor failed answers', async () => {
    const pendingOnly = stats(1, 1, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(pendingOnly, pendingOnly))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(pendingOnly),
      records: [record({ status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'Pending answer' })],
    })

    const wrapper = await mountDialog()

    expect(wrapper.find('[data-testid="question-answer-pending"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="question-answer-processed"]').exists()).toBe(false)
  })

  it('keeps a brief latest-running hint while reviewing an older batch', async () => {
    const activeStats = stats(3, 1, 0, 0, 0)
    const oldRecord = record({
      id: 'old-reviewed',
      batchId: 'batch-old',
      questionName: 'Older reviewed answer',
      status: 'succeeded',
      answerJudgment: 'correct',
      answerBody: 'Old answer',
    })
    const activeBatch: QuestionAnswerBatch = {
      ...batch(activeStats),
      records: [record({ id: 'latest-running', status: 'running' })],
      submittedCount: 3,
      completedCount: 1,
      runningCount: 1,
      active: true,
    }
    const olderBatch: QuestionAnswerBatch = {
      ...batch(stats(1, 1, 0, 1, 0)),
      batchId: 'batch-old',
      records: [oldRecord],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue({
      ...history(activeStats, activeStats),
      records: [oldRecord],
      totalItems: 1,
      totalPages: 1,
    })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(activeBatch)
    harness.getQuestionAnswerBatch.mockResolvedValue(olderBatch)

    const wrapper = await mountDialog()
    const todayHistory = wrapper.find('[data-testid="question-answer-history"]')
    await todayHistory.find('button').trigger('click')
    const reviewButton = todayHistory.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewButton) throw new Error('missing old batch review button')
    await reviewButton.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="question-answer-latest-running-hint"]').text()).toContain(
      '最新批次仍在后台运行，不影响当前复审。',
    )
    expect(wrapper.find('[data-testid="question-answer-latest-running-hint"]').text()).not.toMatch(/\d/)
    expect(wrapper.find('[data-testid="question-answer-pending"]').text()).not.toContain('正在测试：')
  })

  it('keeps today history collapsed and the leave/start actions outside the scroll container', async () => {
    const todayRecord = record({
      id: 'today-history-record',
      batchId: 'today-history-batch',
      questionName: 'Today historical entry',
      status: 'succeeded',
      answerJudgment: 'correct',
      answerBody: 'Today answer',
    })
    const lifetime = stats(2, 2, 0, 2, 0)
    const today = stats(1, 1, 0, 1, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue({
      ...history(lifetime, today),
      records: [todayRecord],
      totalItems: 21,
      totalPages: 2,
    })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(today), batchId: '' })
    let resolveStart: ((value: QuestionAnswerBatch) => void) | undefined
    harness.startQuestionAnswerBatch.mockImplementation(() => new Promise<QuestionAnswerBatch>((resolve) => {
      resolveStart = resolve
    }))

    const wrapper = await mountDialog({
      modelIds: ['gpt-5.6-sol'],
      questionIds: ['question-1'],
      reasoningEffort: 'medium',
      repeatCount: 1,
    })
    const todayHistory = wrapper.find('[data-testid="question-answer-history"]')
    expect(todayHistory.text()).toContain('今日历史')
    expect(todayHistory.text()).not.toContain('Today historical entry')
    expect(wrapper.find('[data-testid="question-answer-stats-lifetime"] [data-testid="question-answer-accuracy"]').text()).toBe('100%')
    await todayHistory.find('button').trigger('click')
    expect(todayHistory.text()).toContain('Today historical entry')
    expect(todayHistory.findAll('button').some(button => button.text().trim() === '2')).toBe(true)

    const scrollContainer = wrapper.find('[data-testid="question-answer-scroll"]')
    const footer = wrapper.find('[data-testid="question-answer-footer"]')
    expect(scrollContainer.exists()).toBe(true)
    expect(footer.exists()).toBe(true)
    expect(scrollContainer.find('[data-testid="question-answer-footer"]').exists()).toBe(false)
    expect(footer.findAll('button').map(button => button.text().trim())).toEqual(['离开', '开始回答'])

    const startButton = footer.findAll('button')[1]
    await Promise.all([startButton.trigger('click'), startButton.trigger('click')])
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    resolveStart?.(batch(today))
    await flushPromises()

    await footer.findAll('button')[0].trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(harness.cancelQuestionAnswerBatch).not.toHaveBeenCalled()
  })

  it('automatically expands an over-limit remembered configuration and explains the disabled start beside the footer', async () => {
    harness.discoverModels.mockResolvedValue({
      models: [
        { id: 'gpt-5.6-sol', name: 'gpt-5.6-sol' },
        { id: 'gpt-5.6-terra', name: 'gpt-5.6-terra' },
        { id: 'gpt-5.5', name: 'gpt-5.5' },
      ],
    })
    harness.listTestQuestions.mockResolvedValue([
      {
        id: 'question-1', name: 'Question 1', body: 'Question 1 body', keywords: [], enabled: true, isDefault: true,
        createdAt: '2026-08-31T00:00:00Z', updatedAt: '2026-08-31T00:00:00Z',
      },
      {
        id: 'question-2', name: 'Question 2', body: 'Question 2 body', keywords: [], enabled: true, isDefault: false,
        createdAt: '2026-08-31T00:00:00Z', updatedAt: '2026-08-31T00:00:00Z',
      },
    ])
    const empty = stats(0, 0, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(empty, empty))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(empty), batchId: '' })
    const wrapper = await mountDialog({
      modelIds: ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.5'],
      questionIds: ['question-1', 'question-2'],
      reasoningEffort: 'high',
      repeatCount: 9,
    })

    const configuration = wrapper.find('[data-testid="question-answer-configuration"]')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(true)
    expect(configuration.text()).toContain('共 54 次请求')
    const footer = wrapper.find('[data-testid="question-answer-footer"]')
    expect(footer.text()).toContain('单批最多 50 次')
    expect(footer.findAll('button')[1].attributes('disabled')).toBeDefined()
    expect(harness.startQuestionAnswerBatch).not.toHaveBeenCalled()
  })
})

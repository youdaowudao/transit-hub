// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { upgradeQuestionAnswerHistoryFixture } from './fixtures/c1QuestionAnswerHistory'
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

vi.mock('@/modules/admin/composables/useConnectionHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/modules/admin/composables/useConnectionHealth')>()
  return {
    connectionHealthProbeResultLabelKey: actual.connectionHealthProbeResultLabelKey,
    connectionHealthMessageKey: (key: string) => key,
    connectionHealthRecordColorClass: () => '',
    formatConnectionHealthTime: (value: string) => value,
    useConnectionHealth: () => ({
      discoverModels: harness.discoverModels,
      runManualProbeOnce: vi.fn(),
      manualProbeTarget: vi.fn(),
      errorKey: { value: '' },
    }),
  }
})

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  cancelQuestionAnswerBatch: harness.cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch: harness.getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch: harness.getQuestionAnswerBatch,
  getQuestionAnswerHistory: async (...args: unknown[]) => upgradeQuestionAnswerHistoryFixture(await harness.getQuestionAnswerHistory(...args)),
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
  vi.spyOn(window, 'confirm').mockReturnValue(true)
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
  vi.restoreAllMocks()
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  vi.useRealTimers()
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

    expect(formatAccuracy(accuracy(stats(4, 3, 1, 3, 0)))).toBe('100%')
    expect(formatAccuracy(accuracy(stats(3, 2, 1, 2, 0)))).toBe('100%')
    expect(formatAccuracy(accuracy(stats(4, 1, 3, 0, 1)))).toBe('0%')
    expect(formatAccuracy(accuracy(stats(0, 0, 0, 0, 0)))).toBe('—')
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

  it('renders review, today and lifetime compact statistics with model details expanded by default', async () => {
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
    expect(bar.text()).toContain('当前批次')
    expect(bar.text()).toContain('今日（新加坡）')
    expect(bar.text()).toContain('累计')
    for (const label of ['提交', '进行中', '成功', '待人工', '正确', '错误', '失败', '取消', '正确率']) {
      expect(bar.text()).toContain(label)
    }
    for (const removedLabel of ['待复审', '回答数', '失败数']) {
      expect(bar.text()).not.toContain(removedLabel)
    }
    expect(bar.find('[data-testid="question-answer-stats-review"] [data-testid="question-answer-accuracy"]').text()).toBe('100%')
    expect(bar.find('[data-testid="question-answer-stats-today"] [data-testid="question-answer-accuracy"]').text()).toBe('100%')
    expect(bar.find('[data-testid="question-answer-stats-lifetime"] [data-testid="question-answer-accuracy"]').text()).toBe('66.7%')
    expect(bar.find('[data-testid="question-answer-accuracy"]').classes()).toEqual(expect.arrayContaining(['text-2xl', 'text-primary']))
    expect(bar.find('[data-testid="question-answer-periods"]').classes()).toEqual(expect.arrayContaining([
      'grid-cols-1',
      'md:grid-cols-3',
    ]))
    expect(bar.find('[data-testid="question-answer-stats-review"] dl').classes()).toEqual(expect.arrayContaining([
      'grid-cols-3',
    ]))
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(3)
    await bar.findAll('button').find(button => button.text() === '账号汇总')!.trigger('click')
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(0)

    const modelToggle = bar.findAll('button').find(button => button.text() === '按模型')!
    expect(modelToggle.text()).toContain('按模型')
    await modelToggle.trigger('click')
    expect(bar.findAll('[data-testid="question-answer-model-stats"]')).toHaveLength(3)
    expect(bar.text()).toContain('gpt-5.6-sol')
    expect(bar.text()).toContain('gpt-5.6-terra')
    await bar.findAll('button').find(button => button.text() === '账号汇总')!.trigger('click')
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
    expect(pending.text()).toContain('待人工判断')
    expect(pending.text()).toContain('Needs review')
    expect(pending.text()).not.toContain('Already correct')
    expect(pending.text()).not.toContain('Request failed detail')
    expect(pending.text()).not.toContain('Running detail')

    const processed = wrapper.find('[data-testid="question-answer-processed"]')
    expect(processed.text()).toContain('判题结果 2 条 · 正确 1 · 错误 1')
    expect(processed.text()).toContain('Already correct')
    expect(processed.text()).not.toContain('Request failed detail')
    expect(wrapper.text()).not.toContain('Cancelled detail')

    for (const group of processed.findAll('[data-testid="question-answer-result-group"]')) if (group.get('button').attributes('aria-expanded') !== 'true') await group.get('button').trigger('click')
    expect(processed.text()).toContain('Already correct')
    expect(processed.text()).toContain('Already incorrect')
    expect(processed.text()).toContain('失败 1 条')
    expect(processed.text()).not.toContain('Request failed detail')
    const failedToggle = processed.findAll('button').find(button => button.text().includes('失败 1 条'))
    if (!failedToggle) throw new Error('missing failed answers toggle')
    await failedToggle.trigger('click')
    expect(processed.text()).toContain('Request failed detail')
  })

  it('keeps the processed summary visible for an active batch with no reviewed answers', async () => {
    const activeRecords = Array.from({ length: 6 }, (_, index) => record({
      id: `active-progress-${index + 1}`,
      batchId: 'batch-progress',
      status: index === 0 ? 'running' : 'pending',
      createdAt: '2026-09-01T00:00:00Z',
      startedAt: index === 0 ? '2026-09-01T00:00:01Z' : null,
    }))
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 6, inProgress: 6, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(activeStats, activeStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(activeStats),
      batchId: 'batch-progress',
      records: activeRecords,
      submittedCount: 6,
      completedCount: 0,
      runningCount: 1,
      active: true,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('判题结果 0 条 · 正确 0 · 错误 0')
    expect(processed.text()).toContain('共 6 条')
    expect(processed.text()).toContain('未返回 6 条')
    expect(processed.text()).toContain('进行中')
    expect(processed.text()).toContain('已用时')
    const summary = processed.get('[data-testid="question-answer-processed-summary"]')
    expect(summary.classes()).toEqual(expect.arrayContaining(['text-sm', 'font-semibold', 'tabular-nums']))
    const batchReminder = wrapper.get('[data-testid="question-answer-review-batch"]')
    expect(batchReminder.classes()).toEqual(expect.arrayContaining(['text-xs', 'text-muted-foreground']))
  })

  it('uses inProgress for the visible waiting count when pending and running coexist', async () => {
    const mixedRecords = [
      record({ id: 'mixed-correct', batchId: 'batch-mixed', status: 'succeeded', answerJudgment: 'correct', answerBody: 'correct' }),
      record({ id: 'mixed-incorrect', batchId: 'batch-mixed', status: 'succeeded', answerJudgment: 'incorrect', answerBody: 'incorrect' }),
      record({ id: 'mixed-pending-1', batchId: 'batch-mixed', status: 'pending' }),
      record({ id: 'mixed-pending-2', batchId: 'batch-mixed', status: 'pending' }),
      record({ id: 'mixed-running', batchId: 'batch-mixed', status: 'running' }),
    ]
    const mixedStats: QuestionAnswerStats = {
      requests: { submitted: 5, inProgress: 3, succeeded: 2, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 1, incorrect: 1 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(mixedStats, mixedStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(mixedStats),
      batchId: 'batch-mixed',
      records: mixedRecords,
      submittedCount: 5,
      completedCount: 99,
      runningCount: 1,
      active: true,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('判题结果 2 条 · 正确 1 · 错误 1')
    expect(processed.text()).toContain('共 5 条')
    expect(processed.text()).toContain('未返回 3 条')
    expect(processed.text()).not.toContain('未返回 1 条')
  })

  it('shows terminal completion and remaining review count without replacing processed totals', async () => {
    const terminalRecords = [
      record({ id: 'terminal-unreviewed', batchId: 'batch-terminal', status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'pending review' }),
      record({ id: 'terminal-correct', batchId: 'batch-terminal', status: 'succeeded', answerJudgment: 'correct', answerBody: 'correct' }),
      record({ id: 'terminal-failed', batchId: 'batch-terminal', status: 'failed', answerJudgment: null, errorType: 'network' }),
    ]
    const terminalStats: QuestionAnswerStats = {
      requests: { submitted: 3, inProgress: 0, succeeded: 2, failed: 1, cancelled: 0 },
      reviews: { unreviewed: 1, correct: 1, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(terminalStats, terminalStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(terminalStats),
      batchId: 'batch-terminal',
      records: terminalRecords,
      submittedCount: 3,
      completedCount: 3,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('判题结果 1 条 · 正确 1 · 错误 0')
    expect(processed.text()).toContain('共 3 条')
    expect(processed.text()).toContain('待人工判断 1 条')
    expect(processed.text()).toContain('已完成')
    expect(processed.text()).toContain('已用时')
  })

  it('shows completed and pending-review status when every successful answer awaits review', async () => {
    const pendingReviewStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(pendingReviewStats, pendingReviewStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(pendingReviewStats),
      batchId: 'batch-pending-review',
      records: [record({
        id: 'pending-review-only',
        batchId: 'batch-pending-review',
        status: 'succeeded',
        answerJudgment: 'unreviewed',
        answerBody: 'Pending review answer',
        completedAt: '2026-08-31T00:00:03Z',
      })],
      submittedCount: 1,
      completedCount: 1,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('判题结果 0 条 · 正确 0 · 错误 0')
    expect(processed.text()).toContain('共 1 条')
    expect(processed.text()).toContain('待人工判断 1 条')
    expect(processed.text()).toContain('已完成')
  })

  it('marks an all-cancelled terminal batch as terminated instead of completed', async () => {
    const cancelledRecords = [
      record({ id: 'cancelled-1', batchId: 'batch-cancelled', status: 'cancelled' }),
      record({ id: 'cancelled-2', batchId: 'batch-cancelled', status: 'cancelled' }),
    ]
    const cancelledStats: QuestionAnswerStats = {
      requests: { submitted: 2, inProgress: 0, succeeded: 0, failed: 0, cancelled: 2 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(cancelledStats, cancelledStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(cancelledStats),
      batchId: 'batch-cancelled',
      records: cancelledRecords,
      submittedCount: 2,
      completedCount: 2,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('已终止')
    expect(processed.text()).not.toContain('已完成')
  })

  it('marks a terminal batch with inconsistent request statistics as unknown', async () => {
    const inconsistentStats: QuestionAnswerStats = {
      requests: { submitted: 2, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(inconsistentStats, inconsistentStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(inconsistentStats),
      batchId: 'batch-inconsistent',
      records: [record({ id: 'inconsistent-record', batchId: 'batch-inconsistent', status: 'succeeded', answerJudgment: 'correct' })],
      submittedCount: 2,
      completedCount: 1,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('状态未知')
    expect(processed.text()).not.toContain('已完成')
  })

  it('marks a terminal batch with inconsistent review statistics as unknown', async () => {
    const inconsistentStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(inconsistentStats, inconsistentStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(inconsistentStats),
      batchId: 'batch-inconsistent-reviews',
      records: [record({ id: 'inconsistent-review-record', batchId: 'batch-inconsistent-reviews', status: 'succeeded', answerJudgment: 'correct' })],
      submittedCount: 1,
      completedCount: 1,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('状态未知')
    expect(processed.text()).not.toContain('已完成')
  })

  it('marks a terminal batch as unknown when review statistics disagree with record judgments', async () => {
    const internallyConsistentStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(internallyConsistentStats, internallyConsistentStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(internallyConsistentStats),
      batchId: 'batch-review-record-mismatch',
      records: [record({ id: 'review-record-mismatch', batchId: 'batch-review-record-mismatch', status: 'succeeded', answerJudgment: 'correct' })],
      submittedCount: 1,
      completedCount: 1,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('状态未知')
    expect(processed.text()).not.toContain('已完成')
  })

  it('keeps the processed row hidden for a terminal batch with no records', async () => {
    const emptyTerminalStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(emptyTerminalStats, emptyTerminalStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(emptyTerminalStats),
      batchId: 'batch-empty-terminal',
      records: [],
      submittedCount: 1,
      completedCount: 1,
      runningCount: 0,
      active: false,
    })

    const wrapper = await mountDialog()

    expect(wrapper.find('[data-testid="question-answer-processed"]').exists()).toBe(false)
  })

  it('warns in the existing summary after three minutes without auto-terminating', async () => {
    vi.useFakeTimers({ now: new Date('2026-09-01T00:04:00Z') })
    const activeRecord = record({
      id: 'slow-running',
      batchId: 'batch-slow',
      status: 'running',
      createdAt: '2026-09-01T00:00:00Z',
      startedAt: '2026-09-01T00:00:01Z',
    })
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(activeStats, activeStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(activeStats),
      batchId: 'batch-slow',
      records: [activeRecord],
      submittedCount: 1,
      completedCount: 0,
      runningCount: 1,
      active: true,
    })

    const wrapper = await mountDialog()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('已超过 3 分钟')
    expect(processed.text()).toContain('未返回 1 条')
    expect(harness.cancelQuestionAnswerBatch).not.toHaveBeenCalled()
  })

  it('shows synchronization failure instead of a stale completion conclusion', async () => {
    vi.useFakeTimers({ now: new Date('2026-09-01T00:01:00Z') })
    const activeRecord = record({
      id: 'sync-failed-running',
      batchId: 'batch-sync-failed',
      status: 'running',
      createdAt: '2026-09-01T00:00:00Z',
      startedAt: '2026-09-01T00:00:01Z',
    })
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(activeStats, activeStats))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(activeStats),
      batchId: 'batch-sync-failed',
      records: [activeRecord],
      submittedCount: 1,
      completedCount: 0,
      runningCount: 1,
      active: true,
    })
    harness.getQuestionAnswerBatch.mockRejectedValue(new Error('poll failed'))

    const wrapper = await mountDialog()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const processed = wrapper.get('[data-testid="question-answer-processed"]')

    expect(processed.text()).toContain('同步失败')
    expect(processed.text()).not.toContain('已完成')
  })

  it('toggles configuration by title and exposes the current expanded state', async () => {
    const empty = stats(0, 0, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(empty, empty))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(empty), batchId: '' })
    const wrapper = await mountDialog({
      modelIds: ['gpt-5.6-sol'],
      questionIds: ['question-1'],
      reasoningEffort: 'high',
      repeatCount: 1,
    })

    const configuration = wrapper.get('[data-testid="question-answer-configuration"]')
    const toggle = configuration.find('[data-testid="question-answer-configuration-toggle"]')
    expect(toggle.exists()).toBe(true)
    if (!toggle.exists()) return

    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(true)
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
  })

  it('collapses configuration when a new active batch starts', async () => {
    const empty = stats(0, 0, 0, 0, 0)
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    const activeBatch: QuestionAnswerBatch = {
      ...batch(activeStats),
      batchId: 'batch-started',
      records: [record({ id: 'started-pending', batchId: 'batch-started', status: 'pending' })],
      submittedCount: 1,
      completedCount: 0,
      runningCount: 0,
      active: true,
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(history(empty, empty))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({ ...batch(empty), batchId: '' })
    harness.startQuestionAnswerBatch.mockResolvedValue(activeBatch)

    const wrapper = await mountDialog()
    const configuration = wrapper.get('[data-testid="question-answer-configuration"]')
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    if (!start) throw new Error('missing start question-answer button')
    await start.trigger('click')
    await flushPromises()

    const toggle = configuration.get('[data-testid="question-answer-configuration-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    await toggle.trigger('click')
    expect(configuration.find('[data-testid="question-answer-models"] input').attributes('disabled')).toBeDefined()
    expect(configuration.find('#question-answer-repeat-count').attributes('disabled')).toBeDefined()
  })

  it('clears a prior review sync failure when switching back to the latest batch', async () => {
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    const oldRecord = record({
      id: 'sync-failure-old-record',
      batchId: 'batch-sync-failure-old',
      status: 'succeeded',
      answerJudgment: 'correct',
      answerBody: 'Old answer',
      completedAt: '2026-08-31T00:00:03Z',
    })
    const oldHistoryRecord = { ...oldRecord, id: 'sync-failure-old-history-record' }
    const activeBatch: QuestionAnswerBatch = {
      ...batch(activeStats),
      batchId: 'batch-sync-failure-latest',
      records: [record({ id: 'sync-failure-latest-record', batchId: 'batch-sync-failure-latest', status: 'running' })],
      submittedCount: 1,
      completedCount: 0,
      runningCount: 1,
      active: true,
    }
    const oldBatch: QuestionAnswerBatch = {
      ...batch(stats(1, 1, 0, 1, 0)),
      batchId: 'batch-sync-failure-old',
      records: [oldRecord],
    }
    harness.getQuestionAnswerHistory.mockResolvedValue({
      ...history(activeStats, activeStats),
      records: [oldRecord, oldHistoryRecord],
      totalItems: 2,
      totalPages: 1,
    })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(activeBatch)
    harness.getQuestionAnswerBatch
      .mockResolvedValueOnce(oldBatch)
      .mockRejectedValueOnce(new Error('review refresh failed'))

    const wrapper = await mountDialog()
    const todayHistory = wrapper.find('[data-testid="question-answer-history"]')
    await todayHistory.find('button').trigger('click')
    const reviewButtons = () => todayHistory.findAll('button').filter(button => button.text().trim() === '查看该批次')
    const reviewButton = reviewButtons()[0]
    if (!reviewButton) throw new Error('missing old batch review button')
    await reviewButton.trigger('click')
    await flushPromises()
    const retryReviewButton = reviewButtons()[0]
    if (!retryReviewButton) throw new Error('missing old batch retry button')
    await retryReviewButton.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-processed"]').text()).toContain('同步失败')
    const latestButton = wrapper.findAll('button').find(button => button.text().trim() === '返回最新')
    if (!latestButton) throw new Error('missing latest batch button')
    await latestButton.trigger('click')

    const processed = wrapper.get('[data-testid="question-answer-processed"]')
    expect(processed.text()).not.toContain('同步失败')
    expect(processed.text()).toContain('进行中')
  })

  it('does not mark the currently viewed batch as sync-failed when another batch refresh fails', async () => {
    const activeStats: QuestionAnswerStats = {
      requests: { submitted: 1, inProgress: 1, succeeded: 0, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      byModel: [],
    }
    const oldRecord = record({
      id: 'other-batch-record',
      batchId: 'batch-other',
      status: 'succeeded',
      answerJudgment: 'unreviewed',
      answerBody: 'Other batch answer',
    })
    const latestBatch: QuestionAnswerBatch = {
      ...batch(activeStats),
      batchId: 'batch-latest-view',
      records: [record({ id: 'latest-view-record', batchId: 'batch-latest-view', status: 'running' })],
      submittedCount: 1,
      completedCount: 0,
      runningCount: 1,
      active: true,
    }
    harness.getQuestionAnswerHistory.mockResolvedValue({
      ...history(activeStats, activeStats),
      records: [oldRecord],
      totalItems: 1,
      totalPages: 1,
    })
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(latestBatch)
    harness.getQuestionAnswerBatch.mockRejectedValue(new Error('other batch refresh failed'))

    const wrapper = await mountDialog()
    const todayHistory = wrapper.find('[data-testid="question-answer-history"]')
    await todayHistory.find('button').trigger('click')
    const reviewButton = todayHistory.findAll('button').find(button => button.text().trim() === '查看该批次')
    if (!reviewButton) throw new Error('missing other batch review button')
    await reviewButton.trigger('click')
    await flushPromises()

    const processed = wrapper.get('[data-testid="question-answer-processed"]')
    expect(processed.text()).not.toContain('同步失败')
    expect(processed.text()).toContain('进行中')
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
    expect(processed.text()).toContain('判题结果 2 条 · 正确 2 · 错误 0')
    expect(processed.text()).toContain('First review')

    for (const group of processed.findAll('[data-testid="question-answer-result-group"]')) if (group.get('button').attributes('aria-expanded') !== 'true') await group.get('button').trigger('click')
    const firstProcessedCard = processed.findAll('li').find(item => item.text().includes('First review'))
    if (!firstProcessedCard) throw new Error('missing processed first review card')
    const incorrectButton = firstProcessedCard.findAll('button').find(button => button.text().trim() === '错误')
    if (!incorrectButton) throw new Error('missing incorrect rejudgment button')
    await incorrectButton.trigger('click')
    await flushPromises()

    expect(processed.text()).toContain('判题结果 2 条 · 正确 1 · 错误 1')
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

  it('shows the terminal batch status when successful answers are still awaiting review', async () => {
    const pendingOnly = stats(1, 1, 0, 0, 0)
    harness.getQuestionAnswerHistory.mockResolvedValue(history(pendingOnly, pendingOnly))
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...batch(pendingOnly),
      records: [record({ status: 'succeeded', answerJudgment: 'unreviewed', answerBody: 'Pending answer' })],
    })

    const wrapper = await mountDialog()

    expect(wrapper.find('[data-testid="question-answer-pending"]').exists()).toBe(true)
    const processed = wrapper.get('[data-testid="question-answer-processed"]')
    expect(processed.text()).toContain('共 1 条')
    expect(processed.text()).toContain('待人工判断 1 条')
    expect(processed.text()).toContain('已完成')
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
    const reviewButton = todayHistory.findAll('button').find(button => button.text().trim() === '查看该批次')
    if (!reviewButton) throw new Error('missing old batch review button')
    await reviewButton.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="question-answer-latest-running-hint"]').text()).toContain(
      '最新运行批次 #batch-cu',
    )
    expect(wrapper.find('[data-testid="question-answer-latest-running-hint"]').text()).toContain('1/3')
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
    expect(todayHistory.text()).toContain('批次历史')
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
    expect(footer.get('p').classes()).toEqual(expect.arrayContaining(['min-w-0', 'flex-1']))
    expect(footer.findAll('button').every(button => button.classes().includes('whitespace-nowrap'))).toBe(true)
    expect(footer.get('button').element.parentElement?.classList.contains('shrink-0')).toBe(true)

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

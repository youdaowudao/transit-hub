// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog, {
  type ManualProbeTargetSummary,
} from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'
import { shortQuestionAnswerBatchId } from '@/modules/admin/utils/questionAnswers'

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

const emptyStats = {
  requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
  reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
  byModel: [],
}

const records = Array.from({ length: 6 }, (_, index) => ({
  id: `record-${index + 1}`,
  targetId: 'sub2api:ws1:acc-1',
  batchId: 'batch-running',
  modelName: 'model-a',
  questionId: `q${index + 1}`,
  questionName: `Question ${index + 1}`,
  questionBody: `Question body ${index + 1}`,
  questionKeywordSnapshot: null,
  reasoningEffort: 'medium' as const,
  answerBody: '',
  status: index < 5 ? 'running' as const : 'pending' as const,
  errorType: '',
  answerJudgment: null,
  manualError: false,
  createdAt: '2026-08-26T12:00:00Z',
  startedAt: index < 5 ? '2026-08-26T12:00:01Z' : null,
  completedAt: null,
  updatedAt: '2026-08-26T12:00:01Z',
}))

const activeBatch = {
  batchId: 'batch-running',
  records,
  reasoningEffort: 'medium' as const,
  repeatCount: 1,
  submittedCount: 6,
  completedCount: 0,
  runningCount: 5,
  active: true,
  currentModel: 'legacy-model',
  currentQuestion: 'legacy-question',
  stats: {
    requests: { submitted: 6, inProgress: 6, succeeded: 0, failed: 0, cancelled: 0 },
    reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
    byModel: [],
  },
}

const emptyHistory = {
  records: [],
  page: 1,
  pageSize: 20 as const,
  totalItems: 0,
  totalPages: 0,
  stats: emptyStats,
  todayStats: emptyStats,
}

const reviewRecords = [
  {
    ...records[0], id: 'unreviewed-1', questionName: 'Unreviewed one', questionBody: 'Question unreviewed one',
    answerBody: 'Answer unreviewed one', status: 'succeeded' as const, answerJudgment: 'unreviewed' as const,
    completedAt: '2026-08-26T12:00:02Z',
  },
  {
    ...records[0], id: 'unreviewed-2', questionName: 'Unreviewed long', questionBody: 'Question unreviewed long',
    answerBody: `Long unreviewed answer ${'完整长答案。'.repeat(40)}`, status: 'succeeded' as const, answerJudgment: 'unreviewed' as const,
    completedAt: '2026-08-26T12:00:02Z',
  },
  {
    ...records[0], id: 'correct-1', questionName: 'Reviewed correct', questionBody: 'Question reviewed correct',
    answerBody: 'Answer reviewed correct', status: 'succeeded' as const, answerJudgment: 'correct' as const,
    completedAt: '2026-08-26T12:00:02Z',
  },
  {
    ...records[0], id: 'incorrect-1', questionName: 'Reviewed incorrect', questionBody: 'Question reviewed incorrect',
    answerBody: 'Answer reviewed incorrect', status: 'succeeded' as const, answerJudgment: 'incorrect' as const,
    manualError: true, completedAt: '2026-08-26T12:00:02Z',
  },
  {
    ...records[0], id: 'failed-1', questionName: 'Request failed', questionBody: 'Question failed', answerBody: '',
    status: 'failed' as const, answerJudgment: null, errorType: 'network', completedAt: '2026-08-26T12:00:02Z',
  },
  {
    ...records[0], id: 'cancelled-1', questionName: 'Request cancelled', questionBody: 'Question cancelled', answerBody: '',
    status: 'cancelled' as const, answerJudgment: null, completedAt: '2026-08-26T12:00:02Z',
  },
]

const terminalReviewBatch = (nextRecords = reviewRecords) => ({
  ...activeBatch,
  batchId: 'batch-review',
  records: nextRecords,
  submittedCount: nextRecords.length,
  completedCount: nextRecords.length,
  runningCount: 0,
  active: false,
  currentModel: '',
  currentQuestion: '',
  stats: {
    requests: { submitted: nextRecords.length, inProgress: 0, succeeded: 4, failed: 1, cancelled: 1 },
    reviews: {
      unreviewed: nextRecords.filter(record => record.answerJudgment === 'unreviewed').length,
      correct: nextRecords.filter(record => record.answerJudgment === 'correct').length,
      incorrect: nextRecords.filter(record => record.answerJudgment === 'incorrect').length,
    },
    byModel: [],
  },
})

const terminalReviewHistory = (nextRecords = reviewRecords) => ({
  ...emptyHistory,
  records: nextRecords,
  totalItems: nextRecords.length,
  totalPages: 1,
  stats: terminalReviewBatch(nextRecords).stats,
  todayStats: terminalReviewBatch(nextRecords).stats,
})

const historicalBatch = (batchId: string, firstQuestionName = 'Unreviewed one') => {
  const batchRecords = reviewRecords.map((record, index) => ({
    ...record,
    id: `${batchId}-${index + 1}`,
    batchId,
    questionName: index === 0 ? firstQuestionName : record.questionName,
  }))
  return { ...terminalReviewBatch(batchRecords), batchId }
}

const mountedWrappers: VueWrapper[] = []

const primaryTarget: ManualProbeTargetSummary = {
  targetId: 'sub2api:ws1:acc-1',
  accountName: 'Concurrent Account',
  platform: 'sub2api',
  type: 'subscription',
  status: 'active',
  groupName: 'Group A',
  formalModels: [],
  intelligenceWeight: null,
}

const secondaryTarget: ManualProbeTargetSummary = {
  ...primaryTarget,
  targetId: 'sub2api:ws1:acc-2',
  accountName: 'Second Account',
  groupName: 'Group B',
}

beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({ models: [{ id: 'model-a', name: 'Model A' }] })
  harness.listTestQuestions.mockReset().mockResolvedValue(records.map((record, index) => ({
    id: record.questionId,
    name: record.questionName,
    body: record.questionBody,
    keywords: ['当前配置关键字'],
    enabled: true,
    isDefault: index === 0,
    createdAt: record.createdAt,
    updatedAt: record.updatedAt,
  })))
  harness.getQuestionAnswerHistory.mockReset().mockResolvedValue(emptyHistory)
  harness.getLatestQuestionAnswerBatch.mockReset().mockResolvedValue(activeBatch)
  harness.getQuestionAnswerBatch.mockReset().mockResolvedValue(activeBatch)
  harness.cancelQuestionAnswerBatch.mockReset().mockResolvedValue({ ...activeBatch, active: false, runningCount: 0 })
  harness.startQuestionAnswerBatch.mockReset().mockResolvedValue(activeBatch)
  harness.setQuestionAnswerJudgment.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

const mountQuestionAnswerDialog = async () => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: {
      open: false,
      target: primaryTarget,
    },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()

  const questionAnswerButton = wrapper.findAll('button').find(button => button.text().includes('问答测试'))
  if (!questionAnswerButton) throw new Error('missing question-answer mode button')
  await questionAnswerButton.trigger('click')
  await flushPromises()
  const historySection = wrapper.find('[data-testid="question-answer-history"]')
  if (historySection.exists() && !historySection.find('[data-testid="question-answer-history-content"]').exists()) {
    await historySection.find('button').trigger('click')
    await flushPromises()
  }
  return wrapper
}

const mountClosedDialog = () => {
  const wrapper = mount(ManualOneTimeProbeDialog, {
    props: { open: false, target: primaryTarget },
    global: { stubs: { Teleport: true, Transition: false } },
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

const batchWithStatuses = (
  statuses: Array<(typeof records)[number]['status']>,
  active: boolean,
) => ({
  ...activeBatch,
  records: records.map((record, index) => ({
    ...record,
    status: statuses[index],
    answerBody: statuses[index] === 'succeeded' ? `Answer ${index + 1}` : '',
    answerJudgment: statuses[index] === 'succeeded' ? 'unreviewed' as const : null,
    completedAt: statuses[index] === 'pending' || statuses[index] === 'running'
      ? null
      : '2026-08-26T12:00:02Z',
  })),
  completedCount: statuses.filter(status => !['pending', 'running'].includes(status)).length,
  runningCount: statuses.filter(status => status === 'running').length,
  active,
  stats: {
    requests: {
      submitted: statuses.length,
      inProgress: statuses.filter(status => status === 'pending' || status === 'running').length,
      succeeded: statuses.filter(status => status === 'succeeded').length,
      failed: statuses.filter(status => status === 'failed').length,
      cancelled: statuses.filter(status => status === 'cancelled').length,
    },
    reviews: {
      unreviewed: statuses.filter(status => status === 'succeeded').length,
      correct: 0,
      incorrect: 0,
    },
    byModel: [],
  },
})

const rowContaining = (wrapper: VueWrapper, text: string) => {
  const row = wrapper.findAll('li').find(candidate => candidate.text().includes(text))
  if (!row) throw new Error(`missing row containing ${text}`)
  return row
}

const judgmentButtons = (wrapper: ReturnType<typeof rowContaining>) => wrapper.findAll('button').filter((button) => {
  const text = button.text().trim()
  return text.startsWith('正确') || text.startsWith('错误')
})

const openProcessedAnswers = async (wrapper: VueWrapper) => {
  const section = wrapper.get('[data-testid="question-answer-processed"]')
  if (!section.find('[data-testid="question-answer-processed-content"]').exists()) {
    await section.find('button').trigger('click')
  }
  return section
}

const openFailedAnswers = async (wrapper: VueWrapper) => {
  const section = await openProcessedAnswers(wrapper)
  if (!section.find('[data-testid="question-answer-failed-content"]').exists()) {
    const failed = section.findAll('button').find(button => button.text().includes('失败'))
    if (failed) await failed.trigger('click')
  }
  return section
}

const appearsBefore = (first: Element, second: Element) => Boolean(
  first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING,
)

describe('question-answer low-operation review', () => {
  it('opens and reopens in the first question-answer mode with one initialization and the fixed near-viewport layout', async () => {
    const wrapper = mountClosedDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()

    const modeLabels = new Set(['问答测试', '正式手动探活', '一次性测试'])
    const modeButtons = wrapper.findAll('button').filter(button => modeLabels.has(button.text().trim()))
    expect(modeButtons.map(button => button.text().trim())).toEqual([
      '问答测试',
      '正式手动探活',
      '一次性测试',
    ])
    expect(modeButtons[0].classes()).toContain('bg-background')
    expect(harness.listTestQuestions).toHaveBeenCalledTimes(1)
    expect(harness.getLatestQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    expect(harness.getQuestionAnswerHistory).toHaveBeenCalledTimes(1)

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.classes()).toEqual(expect.arrayContaining([
      'h-[calc(100dvh-1rem)]',
      'w-[calc(100vw-1rem)]',
      'max-w-none',
    ]))
    expect(dialog.classes()).not.toContain('max-w-6xl')
    expect(dialog.classes().some(className => className.includes('760px'))).toBe(false)
    expect(dialog.element.parentElement?.classList.contains('p-2')).toBe(true)

    const configuration = wrapper.get('[data-testid="question-answer-configuration"]')
    expect(configuration.find('[data-testid="question-answer-models"]').exists()).toBe(false)
    expect(configuration.text()).toContain('当前进行中批次使用此配置')
    expect(appearsBefore(
      wrapper.get('[data-testid="question-answer-pending"]').element,
      configuration.element,
    )).toBe(true)

    await modeButtons[1].trigger('click')
    const formalButton = wrapper.findAll('button').find(button => button.text().trim() === '正式手动探活')
    expect(formalButton?.classes()).toContain('bg-background')
    const onceButton = wrapper.findAll('button').find(button => button.text().trim() === '一次性测试')
    if (!onceButton) throw new Error('missing once mode after formal switch')
    await onceButton.trigger('click')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '一次性测试')?.classes()).toContain('bg-background')

    await wrapper.setProps({ open: false })
    await flushPromises()
    await wrapper.setProps({ open: true })
    await flushPromises()

    const reopenedQuestionAnswer = wrapper.findAll('button').find(button => button.text().trim() === '问答测试')
    expect(reopenedQuestionAnswer?.classes()).toContain('bg-background')
    expect(harness.listTestQuestions).toHaveBeenCalledTimes(2)
    expect(harness.getLatestQuestionAnswerBatch).toHaveBeenCalledTimes(2)
    expect(harness.getQuestionAnswerHistory).toHaveBeenCalledTimes(2)
  })

  it('clears account A immediately and initializes account B exactly once without accepting A judgment work', async () => {
    const targetBRecord = {
      ...reviewRecords[0],
      id: 'target-b-record',
      targetId: secondaryTarget.targetId,
      batchId: 'batch-target-b',
      questionName: 'Second target question',
      answerBody: 'Second target answer',
      questionKeywordSnapshot: ['Second'],
    }
    const targetBBatch = terminalReviewBatch([targetBRecord])
    let resolveTargetB: ((batch: typeof targetBBatch) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockImplementation((targetId: string) => {
      if (targetId === primaryTarget.targetId) return Promise.resolve(terminalReviewBatch())
      return new Promise<typeof targetBBatch>(resolve => { resolveTargetB = resolve })
    })
    harness.getQuestionAnswerHistory.mockImplementation(async (targetId: string) => (
      targetId === primaryTarget.targetId ? terminalReviewHistory() : emptyHistory
    ))
    let oldJudgmentAborted = false
    harness.setQuestionAnswerJudgment.mockImplementation((
      _targetId: string,
      _recordId: string,
      _judgment: string,
      signal: AbortSignal,
    ) => new Promise((_, reject) => {
      signal.addEventListener('abort', () => {
        oldJudgmentAborted = true
        reject(new DOMException('Aborted', 'AbortError'))
      }, { once: true })
    }))

    const wrapper = mountClosedDialog()
    await wrapper.setProps({ open: true })
    await flushPromises()
    expect(wrapper.text()).toContain('Answer unreviewed one')

    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().startsWith('正确'),
    )
    if (!correct) throw new Error('missing account A judgment button')
    await correct.trigger('click')
    await wrapper.setProps({ target: secondaryTarget })
    await flushPromises()

    expect(oldJudgmentAborted).toBe(true)
    expect(wrapper.text()).not.toContain('Answer unreviewed one')
    expect(harness.listTestQuestions).toHaveBeenCalledTimes(2)
    expect(harness.getLatestQuestionAnswerBatch.mock.calls.map(call => call[0])).toEqual([
      primaryTarget.targetId,
      secondaryTarget.targetId,
    ])
    expect(harness.getQuestionAnswerHistory.mock.calls.map(call => call[0])).toEqual([
      primaryTarget.targetId,
      secondaryTarget.targetId,
    ])

    resolveTargetB?.(targetBBatch)
    await flushPromises()
    expect(wrapper.text()).toContain('Second target answer')
    expect(wrapper.text()).not.toContain('Answer unreviewed one')
  })

  it('highlights only succeeded unreviewed main cards from their own snapshot and never folded history', async () => {
    const base = {
      ...reviewRecords[0],
      batchId: 'batch-highlight-matrix',
      answerBody: '错误码 当前配置关键字 <script>alert(1)</script> [done] Error',
      questionKeywordSnapshot: ['错误', '错误码', '<script>', '[done]', 'Error'] as string[] | null,
    }
    const matrix = [
      { ...base, id: 'highlight-unreviewed', questionName: 'Highlight unreviewed' },
      { ...base, id: 'highlight-correct', questionName: 'Highlight correct', answerJudgment: 'correct' as const },
      { ...base, id: 'highlight-incorrect', questionName: 'Highlight incorrect', answerJudgment: 'incorrect' as const, manualError: true },
      { ...base, id: 'highlight-null', questionName: 'Highlight null', questionKeywordSnapshot: null },
      { ...base, id: 'highlight-empty', questionName: 'Highlight empty', questionKeywordSnapshot: [] },
      { ...base, id: 'highlight-no-hit', questionName: 'Highlight no hit', questionKeywordSnapshot: ['不存在'] },
      { ...base, id: 'highlight-failed', questionName: 'Highlight failed', status: 'failed' as const, answerJudgment: null, errorType: 'network' },
    ]
    const foldedHistoryRecord = {
      ...base,
      id: 'folded-history-highlight',
      batchId: 'batch-old-snapshot',
      questionName: 'Folded history highlight',
      answerBody: '旧关键字 当前配置关键字',
      questionKeywordSnapshot: ['旧关键字'],
    }
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch(matrix))
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([foldedHistoryRecord]))
    harness.getQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch([foldedHistoryRecord]))

    const wrapper = await mountQuestionAnswerDialog()
    const unreviewed = rowContaining(wrapper, 'Highlight unreviewed')
    expect(unreviewed.findAll('mark').map(mark => mark.text())).toEqual(['错误码', '<script>', '[done]'])
    expect(unreviewed.findAll('mark').map(mark => mark.text())).not.toContain('当前配置关键字')
    expect(unreviewed.findAll('mark').map(mark => mark.text())).not.toContain('Error')
    expect(unreviewed.text()).toContain('[done] Error')
    expect(unreviewed.find('script').exists()).toBe(false)
    expect(rowContaining(wrapper, 'Highlight null').findAll('mark')).toHaveLength(0)
    expect(rowContaining(wrapper, 'Highlight null').text()).toContain('无关键字快照')
    expect(rowContaining(wrapper, 'Highlight empty').findAll('mark')).toHaveLength(0)
    expect(rowContaining(wrapper, 'Highlight empty').text()).not.toContain('无关键字快照')
    expect(rowContaining(wrapper, 'Highlight no hit').findAll('mark')).toHaveLength(0)

    await openProcessedAnswers(wrapper)
    await openFailedAnswers(wrapper)
    for (const questionName of ['Highlight correct', 'Highlight incorrect', 'Highlight failed']) {
      expect(rowContaining(wrapper, questionName).findAll('mark')).toHaveLength(0)
    }
    expect(rowContaining(wrapper, 'Folded history highlight').findAll('mark')).toHaveLength(0)

    const reviewOldBatch = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOldBatch) throw new Error('missing old-batch review action')
    await reviewOldBatch.trigger('click')
    await flushPromises()
    const selectedOldBatch = rowContaining(wrapper, 'Folded history highlight')
    expect(selectedOldBatch.findAll('mark').map(mark => mark.text())).toEqual(['旧关键字'])
    expect(selectedOldBatch.findAll('mark').map(mark => mark.text())).not.toContain('当前配置关键字')
  })

  it('places every judgment pair vertically with large correct-above-error actions', async () => {
    const currentRecord = {
      ...reviewRecords[0],
      id: 'large-current-actions',
      batchId: 'batch-large-actions',
      questionName: 'Large current actions',
      answerBody: 'Large current answer',
      questionKeywordSnapshot: ['Large'],
    }
    const historyRecord = {
      ...currentRecord,
      id: 'large-history-actions',
      batchId: 'batch-large-history-actions',
      questionName: 'Large history actions',
      answerBody: 'Large history answer',
    }
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch([currentRecord]))
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([historyRecord]))

    const wrapper = await mountQuestionAnswerDialog()
    const currentRow = rowContaining(wrapper, 'Large current actions')
    const currentButtons = judgmentButtons(currentRow)
    const currentAnswer = currentRow.findAll('p').find(paragraph => paragraph.text().includes('Large current answer'))
    if (!currentAnswer) throw new Error('missing current answer text')
    expect(currentButtons.map(button => button.text().trim())).toEqual(['正确', '错误'])
    expect(currentButtons.every(button => button.classes().includes('min-h-14'))).toBe(true)
    expect(currentRow.classes()).toContain('grid')
    expect(currentRow.classes()).toContain('gap-6')
    expect(currentRow.classes().some(className => className.startsWith('md:grid-cols-'))).toBe(true)
    expect(currentRow.classes()).toContain('md:grid-rows-2')
    expect(appearsBefore(currentButtons[0].element, currentAnswer.element)).toBe(true)
    expect(appearsBefore(currentAnswer.element, currentButtons[1].element)).toBe(true)

    const historyRow = rowContaining(wrapper, 'Large history actions')
    const historyButtons = judgmentButtons(historyRow)
    expect(historyButtons.map(button => button.text().trim())).toEqual(['正确', '错误'])
    expect(historyButtons.every(button => button.classes().includes('min-h-12'))).toBe(true)
    expect(historyButtons[0].element.parentElement).toBe(historyButtons[1].element.parentElement)
    expect(historyButtons[0].element.parentElement?.classList.contains('grid')).toBe(true)
    expect(historyButtons[0].element.parentElement?.classList.contains('gap-2')).toBe(true)
  })

  it('shows an immediate colored saving intent without changing judgment, stats or highlight on failure', async () => {
    const pendingRecord = {
      ...reviewRecords[0],
      id: 'pending-intent-record',
      batchId: 'batch-pending-intent',
      questionName: 'Pending intent record',
      answerBody: '错误码 pending intent',
      questionKeywordSnapshot: ['错误码'],
    }
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch([pendingRecord]))
    harness.getQuestionAnswerHistory.mockResolvedValue(emptyHistory)
    let rejectJudgment: ((error: Error) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((_, reject) => {
      rejectJudgment = reject
    }))

    const wrapper = await mountQuestionAnswerDialog()
    const row = rowContaining(wrapper, 'Pending intent record')
    const incorrect = judgmentButtons(row).find(button => button.text().startsWith('错误'))
    if (!incorrect) throw new Error('missing incorrect intent action')
    await incorrect.trigger('click')
    await flushPromises()

    const savingRow = rowContaining(wrapper, 'Pending intent record')
    const savingButtons = judgmentButtons(savingRow)
    const savingIncorrect = savingButtons.find(button => button.text().startsWith('错误'))
    expect(savingButtons.every(button => button.attributes('disabled') !== undefined)).toBe(true)
    expect(savingIncorrect?.classes()).toContain('bg-red-500/20')
    expect(savingIncorrect?.text()).toContain('保存中')
    expect(savingIncorrect?.attributes('aria-pressed')).toBe('false')
    expect(savingRow.findAll('mark').map(mark => mark.text())).toEqual(['错误码'])
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(1)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('错误0')

    rejectJudgment?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()
    const failedRow = rowContaining(wrapper, 'Pending intent record')
    const failedIncorrect = judgmentButtons(failedRow).find(button => button.text().startsWith('错误'))
    expect(failedIncorrect?.text()).not.toContain('保存中')
    expect(failedIncorrect?.classes()).not.toContain('bg-red-500/20')
    expect(failedIncorrect?.attributes('disabled')).toBeUndefined()
    expect(failedRow.findAll('mark').map(mark => mark.text())).toEqual(['错误码'])
    expect(wrapper.text()).toContain('操作失败')
  })

  it('applies the returned authoritative record before batch refresh and removes it from the default queue', async () => {
    const pendingRecord = {
      ...reviewRecords[0],
      id: 'authoritative-default-record',
      batchId: 'batch-authoritative-default',
      questionName: 'Authoritative default record',
      answerBody: '错误码 authoritative default',
      questionKeywordSnapshot: ['错误码'],
    }
    const authoritativeRecord = { ...pendingRecord, answerJudgment: 'correct' as const, manualError: false }
    const refreshedBatch = terminalReviewBatch([authoritativeRecord])
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch([pendingRecord]))
    harness.getQuestionAnswerHistory.mockResolvedValue(emptyHistory)
    let resolveJudgment: ((record: typeof authoritativeRecord) => void) | undefined
    let resolveBatchRefresh: ((batch: typeof refreshedBatch) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise(resolve => { resolveJudgment = resolve }))
    harness.getQuestionAnswerBatch.mockImplementation(() => new Promise(resolve => { resolveBatchRefresh = resolve }))

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Authoritative default record')).find(
      button => button.text().startsWith('正确'),
    )
    if (!correct) throw new Error('missing authoritative correct action')
    await correct.trigger('click')
    resolveJudgment?.(authoritativeRecord)
    await flushPromises()

    expect(wrapper.text()).not.toContain('Authoritative default record')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')

    resolveBatchRefresh?.(refreshedBatch)
    await flushPromises()
  })

  it('keeps the authoritative all-record color when statistics refresh fails and reports the partial outcome', async () => {
    const pendingRecord = {
      ...reviewRecords[0],
      id: 'authoritative-show-all-record',
      batchId: 'batch-authoritative-show-all',
      questionName: 'Authoritative show-all record',
      answerBody: '错误码 authoritative show all',
      questionKeywordSnapshot: ['错误码'],
    }
    const authoritativeRecord = { ...pendingRecord, answerJudgment: 'incorrect' as const, manualError: true }
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch([pendingRecord]))
    harness.getQuestionAnswerHistory.mockResolvedValue(emptyHistory)
    harness.setQuestionAnswerJudgment.mockResolvedValue(authoritativeRecord)
    harness.getQuestionAnswerBatch.mockRejectedValue(new Error('statistics refresh failed'))

    const wrapper = await mountQuestionAnswerDialog()
    const incorrect = judgmentButtons(rowContaining(wrapper, 'Authoritative show-all record')).find(
      button => button.text().startsWith('错误'),
    )
    if (!incorrect) throw new Error('missing authoritative incorrect action')
    await incorrect.trigger('click')
    await flushPromises()

    await openProcessedAnswers(wrapper)
    const row = rowContaining(wrapper, 'Authoritative show-all record')
    const savedIncorrect = judgmentButtons(row).find(button => button.text().startsWith('错误'))
    expect(row.classes()).toContain('bg-red-500/20')
    expect(savedIncorrect?.classes()).toContain('bg-red-500/20')
    expect(savedIncorrect?.attributes('aria-pressed')).toBe('true')
    expect(row.findAll('mark')).toHaveLength(0)
    expect(wrapper.text()).toContain('判定已保存，统计刷新失败')
    expect(wrapper.text()).not.toContain('判定保存失败')
  })
})

describe('question-answer batch behavior', () => {
  it('shows the actual number of concurrent requests instead of one legacy model-question pair', async () => {
    const wrapper = await mountQuestionAnswerDialog()

    expect(wrapper.text()).toContain('问答仍在运行，正在等待可复审回答。')
    expect(wrapper.text()).not.toContain('正在处理 5 项')
    expect(wrapper.text()).not.toContain('正在测试：legacy-model × legacy-question')
    expect(wrapper.text()).not.toContain('完成 0/6')
  })

  it('polls through slot refill and renders the confirmed completed batch', async () => {
    vi.useFakeTimers()
    const refilledBatch = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const completedBatch = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    harness.getQuestionAnswerBatch
      .mockResolvedValueOnce(refilledBatch)
      .mockResolvedValueOnce(completedBatch)
      .mockResolvedValueOnce(completedBatch)

    const wrapper = await mountQuestionAnswerDialog()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(wrapper.text()).toContain('当前待审回答')
    expect(wrapper.text()).not.toContain('完成 1/6')
    expect(wrapper.text()).not.toContain('正在处理 5 项')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(harness.getQuestionAnswerBatch).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('本次问答已完成，结果和统计已刷新。')
    expect(wrapper.text()).not.toContain('正在处理')
  })

  it('cancels the active batch without showing cancelled answer cards', async () => {
    const cancelledBatch = batchWithStatuses(
      ['cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    harness.cancelQuestionAnswerBatch.mockResolvedValue(cancelledBatch)

    const wrapper = await mountQuestionAnswerDialog()
    const stopButton = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stopButton) throw new Error('missing stop-question-answer button')
    await stopButton.trigger('click')
    await flushPromises()

    expect(harness.cancelQuestionAnswerBatch).toHaveBeenCalledTimes(1)
    expect(harness.cancelQuestionAnswerBatch).toHaveBeenCalledWith(
      'sub2api:ws1:acc-1',
      'batch-running',
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).not.toContain('正在处理')
    expect(wrapper.text()).not.toContain('已终止')
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    expect(wrapper.find('[data-testid="question-answer-processed"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('已终止')
  })

  it('allows judgment only for succeeded records while a batch is active', async () => {
    const mixedRecords = [
      { ...reviewRecords[0], batchId: 'batch-active-review' },
      { ...records[0], id: 'active-pending', batchId: 'batch-active-review', questionName: 'Active pending', status: 'pending' as const, answerJudgment: null },
      { ...records[0], id: 'active-running', batchId: 'batch-active-review', questionName: 'Active running', status: 'running' as const, answerJudgment: null },
      { ...reviewRecords[4], id: 'active-failed', batchId: 'batch-active-review', questionName: 'Active failed' },
      { ...reviewRecords[5], id: 'active-cancelled', batchId: 'batch-active-review', questionName: 'Active cancelled' },
    ]
    harness.getLatestQuestionAnswerBatch.mockResolvedValue({
      ...terminalReviewBatch(mixedRecords),
      batchId: 'batch-active-review',
      active: true,
      completedCount: 3,
      runningCount: 1,
      stats: {
        requests: { submitted: 5, inProgress: 2, succeeded: 1, failed: 1, cancelled: 1 },
        reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
        byModel: [],
      },
    })

    const wrapper = await mountQuestionAnswerDialog()

    expect(judgmentButtons(rowContaining(wrapper, 'Unreviewed one'))).toHaveLength(2)
    for (const label of ['Active pending', 'Active running', 'Active cancelled']) expect(wrapper.text()).not.toContain(label)
    await openFailedAnswers(wrapper)
    expect(judgmentButtons(rowContaining(wrapper, 'Active failed'))).toHaveLength(0)
  })

  it('keeps paginated history visible and on the same page while an active batch runs', async () => {
    const pageOneRecord = {
      ...reviewRecords[2],
      id: 'active-history-page-one',
      batchId: 'older-batch-one',
      questionName: 'Active history page one',
    }
    const pageTwoCorrect = {
      ...reviewRecords[2],
      id: 'active-history-page-two',
      batchId: 'older-batch-two',
      questionName: 'Active history page two',
    }
    const pageTwoIncorrect = {
      ...pageTwoCorrect,
      answerJudgment: 'incorrect' as const,
      manualError: true,
    }
    const historyStats = {
      requests: { submitted: 21, inProgress: 0, succeeded: 21, failed: 0, cancelled: 0 },
      reviews: { unreviewed: 0, correct: 21, incorrect: 0 },
      byModel: [],
    }
    let pageTwoReads = 0
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => {
      if (page === 2) {
        pageTwoReads++
        return {
          ...emptyHistory,
          records: [pageTwoReads === 1 ? pageTwoCorrect : pageTwoIncorrect],
          page: 2,
          totalItems: 21,
          totalPages: 2,
          stats: historyStats,
        }
      }
      return {
        ...emptyHistory,
        records: [pageOneRecord],
        totalItems: 21,
        totalPages: 2,
        stats: historyStats,
      }
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(pageTwoIncorrect)

    const wrapper = await mountQuestionAnswerDialog()

    expect(wrapper.text()).not.toContain('Question 1')
    expect(wrapper.text()).toContain('Active history page one')
    const pageTwo = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwo) throw new Error('missing active-batch history page 2')
    await pageTwo.trigger('click')
    await flushPromises()

    const pageTwoRow = rowContaining(wrapper, 'Active history page two')
    const markIncorrect = judgmentButtons(pageTwoRow).find(button => button.text().trim() === '错误')
    if (!markIncorrect) throw new Error('missing active-batch history judgment button')
    await markIncorrect.trigger('click')
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1',
      2,
      expect.any(AbortSignal),
    )
    expect(judgmentButtons(rowContaining(wrapper, 'Active history page two')).find(
      button => button.text().trim() === '错误',
    )?.attributes('aria-pressed')).toBe('true')
  })

  it('defaults a completed batch to the continuous unreviewed queue', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory())

    const wrapper = await mountQuestionAnswerDialog()

    expect(wrapper.text()).toContain('Answer unreviewed one')
    expect(wrapper.text()).toContain('Long unreviewed answer')
    expect(wrapper.text()).not.toContain('Answer reviewed correct')
    expect(wrapper.text()).not.toContain('Answer reviewed incorrect')
    expect(wrapper.text()).not.toContain('Question failed')
    expect(wrapper.text()).not.toContain('Question cancelled')
    expect(wrapper.text()).not.toContain('展开详情')
    expect(judgmentButtons(rowContaining(wrapper, 'Unreviewed one'))).toHaveLength(2)
    expect(judgmentButtons(rowContaining(wrapper, 'Unreviewed long'))).toHaveLength(2)
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(2)
  })

  it('keeps an unreviewed record and its counts visible when judgment saving fails', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory())
    let rejectSave: ((error: Error) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((_, reject) => {
      rejectSave = reject
    }))
    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')

    await correct.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Answer unreviewed one')
    expect(wrapper.text()).toContain('当前待审回答')

    rejectSave?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()
    expect(wrapper.text()).toContain('Answer unreviewed one')
    expect(wrapper.text()).toContain('当前待审回答')
    expect(harness.getLatestQuestionAnswerBatch).toHaveBeenCalledTimes(1)
  })

  it('removes a record only after a successful judgment and reveals the next item', async () => {
    const afterSaveRecords = reviewRecords.map(record => (
      record.id === 'unreviewed-1'
        ? { ...record, answerJudgment: 'correct' as const, manualError: false }
        : record
    ))
    harness.getLatestQuestionAnswerBatch
      .mockResolvedValueOnce(terminalReviewBatch())
      .mockResolvedValue(terminalReviewBatch(afterSaveRecords))
    harness.getQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch(afterSaveRecords))
    harness.getQuestionAnswerHistory
      .mockResolvedValueOnce(terminalReviewHistory())
      .mockResolvedValue(terminalReviewHistory(afterSaveRecords))
    let resolveSave: ((record: typeof afterSaveRecords[number]) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((resolve) => {
      resolveSave = resolve
    }))
    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')

    await correct.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Answer unreviewed one')

    resolveSave?.(afterSaveRecords[0])
    await flushPromises()
    expect(wrapper.text()).not.toContain('Answer unreviewed one')
    expect(wrapper.text()).toContain('Long unreviewed answer')
  })

  it('does not resurrect a cancelled runtime from a stale judgment refresh', async () => {
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'pending'],
      true,
    )
    const staleActiveAfterJudgment = {
      ...runtimeWithAnswer,
      records: runtimeWithAnswer.records.map((record, index) => (
        index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
      )),
      stats: {
        ...runtimeWithAnswer.stats,
        reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
      },
    }
    const stoppedRuntime = batchWithStatuses(
      ['succeeded', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    let historyReads = 0
    let resolveJudgmentHistory: ((history: typeof emptyHistory) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerBatch.mockResolvedValue(staleActiveAfterJudgment)
    harness.getQuestionAnswerHistory.mockImplementation(() => {
      historyReads++
      if (historyReads === 1) return Promise.resolve(emptyHistory)
      if (historyReads === 2) {
        return new Promise<typeof emptyHistory>(resolve => { resolveJudgmentHistory = resolve })
      }
      return Promise.resolve(emptyHistory)
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(staleActiveAfterJudgment.records[0])
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Question 1')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing active judgment button')
    await correct.trigger('click')
    await flushPromises()
    expect(historyReads).toBe(2)

    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing runtime stop action')
    await stop.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    resolveJudgmentHistory?.(emptyHistory)
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    expect(start?.attributes('disabled')).toBeUndefined()
  })

  it('does not resurrect a completed runtime from a stale judgment refresh', async () => {
    vi.useFakeTimers()
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'pending'],
      true,
    )
    const staleActiveAfterJudgment = {
      ...runtimeWithAnswer,
      records: runtimeWithAnswer.records.map((record, index) => (
        index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
      )),
      stats: {
        ...runtimeWithAnswer.stats,
        reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
      },
    }
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    let batchReads = 0
    let historyReads = 0
    let resolveJudgmentHistory: ((history: typeof emptyHistory) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerBatch.mockImplementation(async () => (
      ++batchReads === 1 ? staleActiveAfterJudgment : completedRuntime
    ))
    harness.getQuestionAnswerHistory.mockImplementation(() => {
      historyReads++
      if (historyReads === 1) return Promise.resolve(emptyHistory)
      if (historyReads === 2) {
        return new Promise<typeof emptyHistory>(resolve => { resolveJudgmentHistory = resolve })
      }
      return Promise.resolve(emptyHistory)
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(staleActiveAfterJudgment.records[0])

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Question 1')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing active judgment button')
    await correct.trigger('click')
    await flushPromises()
    expect(historyReads).toBe(2)

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(batchReads).toBe(3)
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    resolveJudgmentHistory?.(emptyHistory)
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    expect(start?.attributes('disabled')).toBeUndefined()
  })

  it('does not reload an old target or abort the new target load after a late judgment success', async () => {
    harness.getQuestionAnswerHistory.mockImplementation(async (targetId: string) => (
      targetId === 'sub2api:ws1:acc-1' ? terminalReviewHistory() : emptyHistory
    ))
    let targetOneLatestReads = 0
    let targetTwoAborted = false
    let resolveTargetTwoBatch: ((batch: ReturnType<typeof terminalReviewBatch>) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockImplementation((targetId: string, signal?: AbortSignal) => {
      if (targetId === 'sub2api:ws1:acc-1') {
        targetOneLatestReads++
        return Promise.resolve(terminalReviewBatch())
      }
      return new Promise((resolve, reject) => {
        resolveTargetTwoBatch = resolve
        signal?.addEventListener('abort', () => {
          targetTwoAborted = true
          reject(new DOMException('Aborted', 'AbortError'))
        }, { once: true })
      })
    })
    let resolveSave: ((record: typeof reviewRecords[number]) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((resolve) => {
      resolveSave = resolve
    }))

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')
    await correct.trigger('click')
    await wrapper.setProps({
      target: {
        targetId: 'sub2api:ws1:acc-2',
        accountName: 'Second Account',
        platform: 'sub2api',
        type: 'subscription',
        status: 'active',
        groupName: 'Group B',
        formalModels: [],
        intelligenceWeight: null,
      },
    })
    await flushPromises()
    const questionAnswerButton = wrapper.findAll('button').find(button => button.text().includes('问答测试'))
    if (!questionAnswerButton) throw new Error('missing question-answer mode button for second target')
    await questionAnswerButton.trigger('click')
    await flushPromises()

    expect(targetOneLatestReads).toBe(1)
    resolveSave?.(reviewRecords[0])
    await flushPromises()

    expect(targetOneLatestReads).toBe(1)
    expect(targetTwoAborted).toBe(false)
    resolveTargetTwoBatch?.({ ...terminalReviewBatch(), batchId: 'batch-second-target' })
    await flushPromises()
  })

  it('does not show a late judgment failure on a newly selected target', async () => {
    harness.getLatestQuestionAnswerBatch.mockImplementation(async (targetId: string) => (
      targetId === 'sub2api:ws1:acc-1'
        ? terminalReviewBatch()
        : { ...terminalReviewBatch(), batchId: 'batch-second-target' }
    ))
    harness.getQuestionAnswerHistory.mockImplementation(async (targetId: string) => (
      targetId === 'sub2api:ws1:acc-1' ? terminalReviewHistory() : emptyHistory
    ))
    let rejectSave: ((error: Error) => void) | undefined
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((_, reject) => {
      rejectSave = reject
    }))

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')
    await correct.trigger('click')
    await wrapper.setProps({
      target: {
        targetId: 'sub2api:ws1:acc-2',
        accountName: 'Second Account',
        platform: 'sub2api',
        type: 'subscription',
        status: 'active',
        groupName: 'Group B',
        formalModels: [],
        intelligenceWeight: null,
      },
    })
    await flushPromises()
    const questionAnswerButton = wrapper.findAll('button').find(button => button.text().includes('问答测试'))
    if (!questionAnswerButton) throw new Error('missing question-answer mode button for second target')
    await questionAnswerButton.trigger('click')
    await flushPromises()

    rejectSave?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('preserves the selected questions after a successful judgment refresh', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory())
    harness.setQuestionAnswerJudgment.mockResolvedValue(reviewRecords[0])
    const wrapper = await mountQuestionAnswerDialog()
    const secondQuestion = wrapper.findAll('label').find(label => label.text().includes('Question 2'))
    if (!secondQuestion) throw new Error('missing second question option')
    const secondQuestionInput = secondQuestion.find('input[type="checkbox"]')
    await secondQuestionInput.trigger('change')
    expect((secondQuestionInput.element as HTMLInputElement).checked).toBe(true)
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(button => button.text().trim() === '正确')
    if (!correct) throw new Error('missing correct judgment button')

    await correct.trigger('click')
    await flushPromises()

    const refreshedSecondQuestion = wrapper.findAll('label').find(label => label.text().includes('Question 2'))
    if (!refreshedSecondQuestion) throw new Error('missing second question option after judgment refresh')
    expect((refreshedSecondQuestion.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)
  })

  it('shows all request states and mutually exclusive judgments in the all-records view', async () => {
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory())
    const wrapper = await mountQuestionAnswerDialog()
    await openProcessedAnswers(wrapper)
    await openFailedAnswers(wrapper)
    await flushPromises()

    for (const text of ['Answer reviewed correct', 'Answer reviewed incorrect', 'Question failed']) {
      expect(wrapper.text()).toContain(text)
    }
    expect(wrapper.text()).not.toContain('Question cancelled')
    const correctRow = rowContaining(wrapper, 'Reviewed correct')
    const incorrectRow = rowContaining(wrapper, 'Reviewed incorrect')
    const correctButtons = judgmentButtons(correctRow)
    const incorrectButtons = judgmentButtons(incorrectRow)
    expect(correctButtons).toHaveLength(2)
    expect(incorrectButtons).toHaveLength(2)
    expect(correctButtons.find(button => button.text().trim() === '正确')?.attributes('aria-pressed')).toBe('true')
    expect(correctButtons.find(button => button.text().trim() === '错误')?.attributes('aria-pressed')).toBe('false')
    expect(incorrectButtons.find(button => button.text().trim() === '正确')?.attributes('aria-pressed')).toBe('false')
    expect(incorrectButtons.find(button => button.text().trim() === '错误')?.attributes('aria-pressed')).toBe('true')
    expect(judgmentButtons(rowContaining(wrapper, 'Request failed'))).toHaveLength(0)
  })

  it('keeps the current history page and expanded record after rejudging', async () => {
    const pageTwoCorrect = {
      ...reviewRecords[2],
      id: 'page-two-correct',
      batchId: 'older-batch',
      questionName: 'Page two reviewed',
      questionBody: 'Page two question',
      answerBody: 'Page two full answer',
    }
    const pageTwoIncorrect = { ...pageTwoCorrect, answerJudgment: 'incorrect' as const, manualError: true }
    const pageOneHistory = {
      ...terminalReviewHistory(),
      totalItems: 21,
      totalPages: 2,
    }
    let pageTwoReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => {
      if (page !== 2) return pageOneHistory
      pageTwoReads++
      return {
        ...terminalReviewHistory([pageTwoReads === 1 ? pageTwoCorrect : pageTwoIncorrect]),
        page: 2,
        totalItems: 21,
        totalPages: 2,
      }
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(pageTwoIncorrect)

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwo = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwo) throw new Error('missing history page 2')
    await pageTwo.trigger('click')
    await flushPromises()

    const pageTwoRow = rowContaining(wrapper, 'Page two reviewed')
    await pageTwoRow.find('button').trigger('click')
    expect(wrapper.text()).toContain('Page two full answer')
    const markIncorrect = judgmentButtons(pageTwoRow).find(button => button.text().trim() === '错误')
    if (!markIncorrect) throw new Error('missing page-two incorrect button')
    await markIncorrect.trigger('click')
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1',
      2,
      expect.any(AbortSignal),
    )
    const refreshedRow = rowContaining(wrapper, 'Page two reviewed')
    expect(wrapper.text()).toContain('Page two full answer')
    expect(judgmentButtons(refreshedRow).find(button => button.text().trim() === '错误')?.attributes('aria-pressed')).toBe('true')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
  })

  it('offers one review action per page batch and loads all 25 records', async () => {
    const pageRecords = Array.from({ length: 20 }, (_, index) => ({
      ...reviewRecords[0],
      id: `page-record-${index + 1}`,
      batchId: index < 18 ? 'historical-batch-123456789' : 'other-batch-987654321',
      questionName: `Page record ${index + 1}`,
    }))
    const fullRecords = Array.from({ length: 25 }, (_, index) => ({
      ...reviewRecords[0],
      id: `full-record-${index + 1}`,
      batchId: 'historical-batch-123456789',
      questionName: `Full record ${index + 1}`,
      completedAt: `2026-08-30T01:00:${String(index).padStart(2, '0')}Z`,
    }))
    const fullBatch = {
      ...terminalReviewBatch(fullRecords),
      batchId: 'historical-batch-123456789',
      stats: {
        requests: { submitted: 25, inProgress: 0, succeeded: 25, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 25, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue({
      ...terminalReviewHistory(pageRecords), records: pageRecords, totalItems: 25, totalPages: 2,
    })
    harness.getQuestionAnswerBatch.mockResolvedValue(fullBatch)

    const wrapper = await mountQuestionAnswerDialog()
    const reviewButtons = wrapper.findAll('button').filter(button => button.text().trim() === '复审此批次')
    expect(reviewButtons).toHaveLength(2)
    await reviewButtons[0].trigger('click')
    await flushPromises()

    expect(harness.getQuestionAnswerBatch).toHaveBeenCalledWith(
      'sub2api:ws1:acc-1', 'historical-batch-123456789', expect.any(AbortSignal),
    )
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('回答数25')
    expect(wrapper.text()).toContain('historic')
    expect(wrapper.text()).toContain('2026-08-30T01:00:24Z')
    expect(wrapper.text()).toContain('Full record 25')
  })

  it('stops only the active runtime batch and preserves the selected review batch', async () => {
    const oldBatch = historicalBatch('old-review-batch', 'Old review answer')
    const stoppedRecords = records.map(record => ({
      ...record, status: 'cancelled' as const, startedAt: null,
      completedAt: '2026-08-30T02:00:00Z', updatedAt: '2026-08-30T02:00:00Z',
    }))
    const stoppedRuntime = {
      ...activeBatch, records: stoppedRecords, active: false, completedCount: 6, runningCount: 0,
      currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 0, failed: 0, cancelled: 6 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([oldBatch.records[0]]))
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? oldBatch : activeBatch
    ))
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing historical batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    await openProcessedAnswers(wrapper)
    expect(wrapper.text()).toContain('Answer reviewed correct')

    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing runtime stop action')
    await stop.trigger('click')
    await flushPromises()

    expect(harness.cancelQuestionAnswerBatch).toHaveBeenCalledWith(
      'sub2api:ws1:acc-1', 'batch-running', expect.any(AbortSignal),
    )
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
    expect(wrapper.text()).toContain('Old review answer')
    expect(wrapper.text()).toContain('Answer reviewed correct')
  })

  it('keeps the selected review batch when the active runtime batch completes naturally', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('old-natural-review', 'Natural completion must not replace me')
    const runtimeRecords = reviewRecords.map(record => ({ ...record, batchId: activeBatch.batchId }))
    const completedRuntime = { ...terminalReviewBatch(runtimeRecords), batchId: activeBatch.batchId }
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([oldBatch.records[0]]))
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? oldBatch : completedRuntime
    ))

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing historical batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(harness.getQuestionAnswerBatch).toHaveBeenCalledWith(
      'sub2api:ws1:acc-1', activeBatch.batchId, expect.any(AbortSignal),
    )
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
    expect(wrapper.text()).toContain('Natural completion must not replace me')
  })

  it('keeps the history page while a pending historical selection overlaps runtime completion', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('pending-natural-review', 'Pending natural review')
    const pageOne = { ...terminalReviewHistory(), totalItems: 21, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([oldBatch.records[0]]), page: 2, totalItems: 21, totalPages: 2,
    }
    const runtimeRecords = reviewRecords.map(record => ({ ...record, batchId: activeBatch.batchId }))
    const completedRuntime = { ...terminalReviewBatch(runtimeRecords), batchId: activeBatch.batchId }
    let resolveSelection: ((batch: ReturnType<typeof historicalBatch>) => void) | undefined
    const pendingSelection = new Promise<ReturnType<typeof historicalBatch>>(resolve => { resolveSelection = resolve })
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? pendingSelection : completedRuntime
    ))

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing pending historical batch action')
    await reviewOld.trigger('click')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    resolveSelection?.(oldBatch)
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 2, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
  })

  it('keeps the history page while a pending historical selection overlaps runtime cancellation', async () => {
    const oldBatch = historicalBatch('pending-stop-review', 'Pending stop review')
    const pageOne = { ...terminalReviewHistory(), totalItems: 21, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([oldBatch.records[0]]), page: 2, totalItems: 21, totalPages: 2,
    }
    const stoppedRecords = records.map(record => ({
      ...record, status: 'cancelled' as const, startedAt: null,
      completedAt: '2026-08-30T02:00:00Z', updatedAt: '2026-08-30T02:00:00Z',
    }))
    const stoppedRuntime = {
      ...activeBatch, records: stoppedRecords, active: false, completedCount: 6, runningCount: 0,
      currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 0, failed: 0, cancelled: 6 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }
    let resolveSelection: ((batch: ReturnType<typeof historicalBatch>) => void) | undefined
    const pendingSelection = new Promise<ReturnType<typeof historicalBatch>>(resolve => { resolveSelection = resolve })
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? pendingSelection : activeBatch
    ))
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing pending historical batch action')
    await reviewOld.trigger('click')

    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing runtime stop action')
    await stop.trigger('click')
    await flushPromises()
    resolveSelection?.(oldBatch)
    await flushPromises()

    expect(harness.cancelQuestionAnswerBatch).toHaveBeenCalledWith(
      'sub2api:ws1:acc-1', activeBatch.batchId, expect.any(AbortSignal),
    )
    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 2, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
  })

  it('ignores a late selection and refreshes the judged historical batch without reading latest', async () => {
    let resolveFirst: ((batch: ReturnType<typeof historicalBatch>) => void) | undefined
    const first = new Promise<ReturnType<typeof historicalBatch>>(resolve => { resolveFirst = resolve })
    const batchA = historicalBatch('batch-a-late', 'Batch A first')
    const batchB = historicalBatch('batch-b-current', 'Batch B first')
    const batchBAfterRecords = batchB.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const batchBAfter = { ...terminalReviewBatch(batchBAfterRecords), batchId: batchB.batchId }
    const pageOne = { ...terminalReviewHistory(), totalItems: 22, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([batchA.records[0], batchB.records[0]]),
      page: 2, totalItems: 22, totalPages: 2,
    }
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    let batchBReads = 0
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => {
      if (batchId === batchA.batchId) return first
      if (batchId === batchB.batchId) return ++batchBReads === 1 ? batchB : batchBAfter
      return activeBatch
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(batchBAfter.records[0])

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    const actions = wrapper.findAll('button').filter(button => button.text().trim() === '复审此批次')
    await actions[0].trigger('click')
    await actions[1].trigger('click')
    await flushPromises()
    resolveFirst?.(batchA)
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(batchB.batchId),
    )

    const latestReadsBeforeJudgment = harness.getLatestQuestionAnswerBatch.mock.calls.length
    const correct = judgmentButtons(rowContaining(wrapper, 'Batch B first')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing historical judgment button')
    await correct.trigger('click')
    await flushPromises()

    expect(harness.getLatestQuestionAnswerBatch).toHaveBeenCalledTimes(latestReadsBeforeJudgment)
    expect(batchBReads).toBe(2)
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(batchB.batchId),
    )
    expect(wrapper.text()).not.toContain('Batch B first')
    expect(wrapper.text()).toContain('Long unreviewed answer')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(1)
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
  })

  it('does not let a late batch selection overwrite a newer judgment refresh for the same batch', async () => {
    const batchA = historicalBatch('batch-a-pending-selection', 'Batch A pending judgment')
    const batchB = historicalBatch('batch-b-reviewed', 'Batch B current review')
    const batchAAfterRecords = batchA.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const batchAAfter = {
      ...terminalReviewBatch(batchAAfterRecords),
      batchId: batchA.batchId,
    }
    const initialHistory = {
      ...emptyHistory,
      records: [batchA.records[0], batchB.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    const refreshedHistory = {
      ...initialHistory,
      records: [batchAAfter.records[0], batchB.records[0]],
    }
    let historyReads = 0
    let batchAReads = 0
    let resolveBatchASelection: ((batch: typeof batchA) => void) | undefined
    harness.getQuestionAnswerHistory.mockImplementation(() => (
      Promise.resolve(++historyReads === 1 ? initialHistory : refreshedHistory)
    ))
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === batchB.batchId) return Promise.resolve(batchB)
      if (batchId === batchA.batchId && ++batchAReads === 1) {
        return new Promise<typeof batchA>(resolve => { resolveBatchASelection = resolve })
      }
      if (batchId === batchA.batchId) return Promise.resolve(batchAAfter)
      return Promise.resolve(activeBatch)
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(batchAAfter.records[0])

    const wrapper = await mountQuestionAnswerDialog()
    const initialActions = wrapper.findAll('button').filter(button => button.text().trim() === '复审此批次')
    if (initialActions.length !== 2) throw new Error('missing initial historical batch actions')
    await initialActions[1].trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(batchB.batchId),
    )

    const reviewBatchA = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewBatchA) throw new Error('missing pending batch A selection')
    await reviewBatchA.trigger('click')

    const correct = judgmentButtons(rowContaining(wrapper, 'Batch A pending judgment')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing batch A history judgment action')
    await correct.trigger('click')
    await flushPromises()
    expect(batchAReads).toBe(2)

    resolveBatchASelection?.(batchA)
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(batchA.batchId),
    )
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(1)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')
    expect(wrapper.text()).not.toContain('Batch A pending judgment')
    expect(wrapper.text()).toContain('Long unreviewed answer')
  })

  it('keeps a selected historical batch unchanged when judgment saving fails', async () => {
    const oldBatch = historicalBatch('old-save-failure', 'Historical save failure')
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([oldBatch.records[0]]))
    harness.getQuestionAnswerBatch.mockResolvedValue(oldBatch)
    harness.setQuestionAnswerJudgment.mockRejectedValue(new Error('admin.connectionHealth.errors.request'))
    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing historical batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    const latestReads = harness.getLatestQuestionAnswerBatch.mock.calls.length
    const correct = judgmentButtons(rowContaining(wrapper, 'Historical save failure')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing historical judgment button')
    await correct.trigger('click')
    await flushPromises()

    expect(harness.getLatestQuestionAnswerBatch).toHaveBeenCalledTimes(latestReads)
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
    expect(wrapper.text()).toContain('Historical save failure')
    expect(wrapper.text()).toContain('当前待审回答')
  })

  it('keeps other history visible and pageable while reviewing old results under an active runtime', async () => {
    const oldBatch = historicalBatch('old-visible-review')
    const third = {
      ...reviewRecords[0], id: 'third-history-record', batchId: 'third-history-batch',
      questionName: 'Third history answer',
    }
    const pageTwoRecord = {
      ...reviewRecords[0], id: 'page-two-history-record', batchId: 'page-two-history-batch',
      questionName: 'Page two history answer',
    }
    const pageOne = {
      ...terminalReviewHistory([oldBatch.records[0], third]), totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? oldBatch : activeBatch
    ))

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing historical batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Third history answer')
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Page two history answer')
    expect(wrapper.findAll('button').filter(button => button.text().trim() === '复审此批次')).toHaveLength(1)
  })

  it('clears a historical review before re-entering question-answer mode when latest reload fails', async () => {
    const oldBatch = historicalBatch('mode-switch-old-review', 'Mode switch stale review')
    let latestReads = 0
    harness.getLatestQuestionAnswerBatch.mockImplementation(async () => {
      latestReads++
      if (latestReads === 1) return terminalReviewBatch()
      throw new Error('admin.connectionHealth.errors.request')
    })
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([oldBatch.records[0]]))
    harness.getQuestionAnswerBatch.mockResolvedValue(oldBatch)

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing historical batch action before mode switch')
    await reviewOld.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Mode switch stale review')

    const formal = wrapper.findAll('button').find(button => button.text().trim() === '正式手动探活')
    if (!formal) throw new Error('missing formal mode action')
    await formal.trigger('click')
    await flushPromises()
    const questionAnswer = wrapper.findAll('button').find(button => button.text().trim() === '问答测试')
    if (!questionAnswer) throw new Error('missing question-answer mode action')
    await questionAnswer.trigger('click')
    await flushPromises()

    expect(latestReads).toBe(2)
    expect(wrapper.text()).not.toContain('Mode switch stale review')
    expect(wrapper.find('[data-testid="question-answer-review-batch"]').exists()).toBe(false)
  })

  it('keeps start on history page one when an older page request arrives late', async () => {
    const pageOneRecord = {
      ...reviewRecords[2], id: 'start-race-page-one', batchId: 'start-race-old-one',
      questionName: 'Start race page one',
    }
    const pageTwoRecord = {
      ...reviewRecords[2], id: 'start-race-page-two', batchId: 'start-race-old-two',
      questionName: 'Start race page two',
    }
    const pageOne = {
      ...terminalReviewHistory([pageOneRecord]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    const newRuntime = {
      ...activeBatch,
      batchId: 'start-race-new-runtime',
      records: activeBatch.records.map(record => ({ ...record, batchId: 'start-race-new-runtime' })),
    }
    let resolvePageTwo: ((history: typeof pageTwo) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) {
        return new Promise<typeof pageTwo>(resolve => { resolvePageTwo = resolve })
      }
      return Promise.resolve(pageOne)
    })
    harness.startQuestionAnswerBatch.mockResolvedValue(newRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two before start')
    await pageTwoButton.trigger('click')
    await flushPromises()
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    if (!start) throw new Error('missing question-answer start action')
    await start.trigger('click')
    await flushPromises()

    resolvePageTwo?.(pageTwo)
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '1')?.classes()).toContain('bg-primary')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).not.toContain('bg-primary')
  })

  it('keeps a newer history page when the start page-one refresh arrives late', async () => {
    const pageOneRecord = {
      ...reviewRecords[2], id: 'late-start-page-one', batchId: 'late-start-old-one',
      questionName: 'Late start page one',
    }
    const pageTwoRecord = {
      ...reviewRecords[2], id: 'late-start-page-two', batchId: 'late-start-old-two',
      questionName: 'Late start page two',
    }
    const pageOne = {
      ...terminalReviewHistory([pageOneRecord]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    const newRuntime = {
      ...activeBatch,
      batchId: 'late-start-new-runtime',
      records: activeBatch.records.map(record => ({ ...record, batchId: 'late-start-new-runtime' })),
    }
    let pageOneReads = 0
    let resolveStartPageOne: ((history: typeof pageOne) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) return Promise.resolve(pageTwo)
      pageOneReads++
      if (pageOneReads === 1) return Promise.resolve(pageOne)
      return new Promise<typeof pageOne>(resolve => { resolveStartPageOne = resolve })
    })
    harness.startQuestionAnswerBatch.mockResolvedValue(newRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    if (!start) throw new Error('missing question-answer start action')
    await start.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two during start refresh')
    await pageTwoButton.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')

    resolveStartPageOne?.(pageOne)
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.findAll('button').find(button => button.text().trim() === '1')?.classes()).not.toContain('bg-primary')
  })

  it('does not let an older judgment refresh undo a newer judgment from the same batch', async () => {
    const initialRecords = reviewRecords.slice(0, 2)
    const afterFirstRecords = initialRecords.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const afterBothRecords = afterFirstRecords.map((record, index) => (
      index === 1 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const batchWithReviewStats = (nextRecords: typeof afterBothRecords) => ({
      ...terminalReviewBatch(nextRecords),
      stats: {
        requests: { submitted: 2, inProgress: 0, succeeded: 2, failed: 0, cancelled: 0 },
        reviews: {
          unreviewed: nextRecords.filter(record => record.answerJudgment === 'unreviewed').length,
          correct: nextRecords.filter(record => record.answerJudgment === 'correct').length,
          incorrect: 0,
        },
        byModel: [],
      },
    })
    const initialBatch = batchWithReviewStats(initialRecords)
    const afterFirstBatch = batchWithReviewStats(afterFirstRecords)
    const afterBothBatch = batchWithReviewStats(afterBothRecords)
    let resolveFirstRefresh: ((batch: typeof afterFirstBatch) => void) | undefined
    const firstRefresh = new Promise<typeof afterFirstBatch>(resolve => { resolveFirstRefresh = resolve })
    let batchReads = 0
    let historyReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(initialBatch)
    harness.getQuestionAnswerBatch.mockImplementation(() => (
      ++batchReads === 1 ? firstRefresh : Promise.resolve(afterBothBatch)
    ))
    harness.getQuestionAnswerHistory.mockImplementation(() => {
      historyReads++
      if (historyReads === 1) return Promise.resolve(terminalReviewHistory(initialRecords))
      if (historyReads === 2) return Promise.resolve(terminalReviewHistory(afterFirstRecords))
      return Promise.resolve(terminalReviewHistory(afterBothRecords))
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(afterBothRecords[0])

    const wrapper = await mountQuestionAnswerDialog()
    const firstCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!firstCorrect) throw new Error('missing first judgment action')
    await firstCorrect.trigger('click')
    await flushPromises()
    const secondCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed long')).find(
      button => button.text().trim() === '正确',
    )
    if (!secondCorrect) throw new Error('missing second judgment action')
    await secondCorrect.trigger('click')
    await flushPromises()

    expect(wrapper.text()).not.toContain('Long unreviewed answer')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')

    resolveFirstRefresh?.(afterFirstBatch)
    await flushPromises()

    expect(wrapper.text()).not.toContain('Long unreviewed answer')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')
  })

  it('refreshes history stats when same-batch judgment saves finish in reverse click order', async () => {
    const initialRecords = reviewRecords.slice(0, 2)
    const afterSecondRecords = initialRecords.map((record, index) => (
      index === 1 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const afterBothRecords = afterSecondRecords.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const, manualError: false } : record
    ))
    const batchWithReviewStats = (nextRecords: typeof afterBothRecords) => ({
      ...terminalReviewBatch(nextRecords),
      stats: {
        requests: { submitted: 2, inProgress: 0, succeeded: 2, failed: 0, cancelled: 0 },
        reviews: {
          unreviewed: nextRecords.filter(record => record.answerJudgment === 'unreviewed').length,
          correct: nextRecords.filter(record => record.answerJudgment === 'correct').length,
          incorrect: 0,
        },
        byModel: [],
      },
    })
    const historyWithReviewStats = (nextRecords: typeof afterBothRecords) => ({
      ...terminalReviewHistory(nextRecords),
      stats: batchWithReviewStats(nextRecords).stats,
      todayStats: batchWithReviewStats(nextRecords).stats,
    })
    const initialBatch = batchWithReviewStats(initialRecords)
    const afterSecondBatch = batchWithReviewStats(afterSecondRecords)
    const afterBothBatch = batchWithReviewStats(afterBothRecords)
    let resolveFirstSave: ((record: typeof afterBothRecords[number]) => void) | undefined
    let batchReads = 0
    let historyReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(initialBatch)
    harness.setQuestionAnswerJudgment.mockImplementation((_targetId: string, recordId: string) => {
      if (recordId === 'unreviewed-1') {
        return new Promise<typeof afterBothRecords[number]>(resolve => { resolveFirstSave = resolve })
      }
      return Promise.resolve(afterSecondRecords[1])
    })
    harness.getQuestionAnswerBatch.mockImplementation(() => (
      Promise.resolve(++batchReads === 1 ? afterSecondBatch : afterBothBatch)
    ))
    harness.getQuestionAnswerHistory.mockImplementation(() => {
      historyReads++
      if (historyReads === 1) return Promise.resolve(historyWithReviewStats(initialRecords))
      if (historyReads === 2) return Promise.resolve(historyWithReviewStats(afterSecondRecords))
      return Promise.resolve(historyWithReviewStats(afterBothRecords))
    })

    const wrapper = await mountQuestionAnswerDialog()
    const firstCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!firstCorrect) throw new Error('missing first reverse-order judgment action')
    await firstCorrect.trigger('click')
    await flushPromises()
    const secondCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed long')).find(
      button => button.text().trim() === '正确',
    )
    if (!secondCorrect) throw new Error('missing second reverse-order judgment action')
    await secondCorrect.trigger('click')
    await flushPromises()

    resolveFirstSave?.(afterBothRecords[0])
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')
    const allTimeTitle = wrapper.findAll('p').find(paragraph => paragraph.text().trim() === '累计')
    if (!allTimeTitle) throw new Error('missing all-time question-answer stats')
    const allTimePanelText = allTimeTitle.element.parentElement?.parentElement?.textContent ?? ''
    expect(allTimePanelText).not.toContain('待复审')
    expect(allTimePanelText).toContain('正确2')
  })

  it('preserves a pending history page when the active runtime completes', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('pending-page-natural-review', 'Pending page natural review')
    const pageOne = {
      ...terminalReviewHistory([oldBatch.records[0]]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([oldBatch.records[1]]), page: 2, totalItems: 21, totalPages: 2,
    }
    const runtimeRecords = reviewRecords.map(record => ({ ...record, batchId: activeBatch.batchId }))
    const completedRuntime = { ...terminalReviewBatch(runtimeRecords), batchId: activeBatch.batchId }
    let pageTwoCalls = 0
    let resolvePendingPageTwo: ((history: typeof pageTwo) => void) | undefined
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number, signal: AbortSignal) => {
      if (page !== 2) return Promise.resolve(pageOne)
      pageTwoCalls++
      if (pageTwoCalls > 1) return Promise.resolve(pageTwo)
      return new Promise<typeof pageTwo>((resolve, reject) => {
        resolvePendingPageTwo = resolve
        signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      })
    })
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? oldBatch : completedRuntime
    ))

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing pending-page historical batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing pending history page two')
    await pageTwoButton.trigger('click')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    resolvePendingPageTwo?.(pageTwo)
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 2, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
  })

  it('preserves a pending history page when the active runtime is cancelled', async () => {
    const oldBatch = historicalBatch('pending-page-stop-review', 'Pending page stop review')
    const pageOne = {
      ...terminalReviewHistory([oldBatch.records[0]]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([oldBatch.records[1]]), page: 2, totalItems: 21, totalPages: 2,
    }
    const stoppedRecords = records.map(record => ({
      ...record, status: 'cancelled' as const, startedAt: null,
      completedAt: '2026-08-30T02:00:00Z', updatedAt: '2026-08-30T02:00:00Z',
    }))
    const stoppedRuntime = {
      ...activeBatch, records: stoppedRecords, active: false, completedCount: 6, runningCount: 0,
      currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 0, failed: 0, cancelled: 6 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }
    let pageTwoCalls = 0
    let resolvePendingPageTwo: ((history: typeof pageTwo) => void) | undefined
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number, signal: AbortSignal) => {
      if (page !== 2) return Promise.resolve(pageOne)
      pageTwoCalls++
      if (pageTwoCalls > 1) return Promise.resolve(pageTwo)
      return new Promise<typeof pageTwo>((resolve, reject) => {
        resolvePendingPageTwo = resolve
        signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      })
    })
    harness.getQuestionAnswerBatch.mockImplementation(async (_targetId: string, batchId: string) => (
      batchId === oldBatch.batchId ? oldBatch : activeBatch
    ))
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing pending-page stop batch action')
    await reviewOld.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing pending stop history page two')
    await pageTwoButton.trigger('click')
    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing pending-page runtime stop action')
    await stop.trigger('click')
    await flushPromises()
    resolvePendingPageTwo?.(pageTwo)
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 2, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
  })

  it('keeps the existing page-one refresh when the reviewed runtime completes', async () => {
    vi.useFakeTimers()
    const pageOne = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwoRecord = { ...reviewRecords[3], id: 'runtime-complete-page-two', batchId: 'runtime-complete-history' }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    const runtimeRecords = reviewRecords.map(record => ({ ...record, batchId: activeBatch.batchId }))
    const completedRuntime = { ...terminalReviewBatch(runtimeRecords), batchId: activeBatch.batchId }
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    harness.getQuestionAnswerBatch.mockResolvedValue(completedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing runtime completion history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 1, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '1')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(activeBatch.batchId),
    )
  })

  it('keeps the existing page-one refresh when the reviewed runtime is cancelled', async () => {
    const pageOne = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwoRecord = { ...reviewRecords[3], id: 'runtime-stop-page-two', batchId: 'runtime-stop-history' }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    const stoppedRecords = records.map(record => ({
      ...record, status: 'cancelled' as const, startedAt: null,
      completedAt: '2026-08-30T02:00:00Z', updatedAt: '2026-08-30T02:00:00Z',
    }))
    const stoppedRuntime = {
      ...activeBatch, records: stoppedRecords, active: false, completedCount: 6, runningCount: 0,
      currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 0, failed: 0, cancelled: 6 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
        byModel: [],
      },
    }
    harness.getQuestionAnswerHistory.mockImplementation(async (_targetId: string, page: number) => (
      page === 2 ? pageTwo : pageOne
    ))
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing runtime stop history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')

    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing reviewed runtime stop action')
    await stop.trigger('click')
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 1, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '1')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(activeBatch.batchId),
    )
  })

  it('does not revive a completed runtime when selecting its active history snapshot returns late', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('late-active-natural-old', 'Late active natural old review')
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], activeBatch.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    let activeReads = 0
    let resolveActiveSelection: ((batch: typeof activeBatch) => void) | undefined
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      activeReads++
      if (activeReads === 1) {
        return new Promise<typeof activeBatch>(resolve => { resolveActiveSelection = resolve })
      }
      return Promise.resolve(completedRuntime)
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before late active selection')
    await reviewOld.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )

    const reviewActive = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewActive) throw new Error('missing active history batch action')
    await reviewActive.trigger('click')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    resolveActiveSelection?.(activeBatch)
    await flushPromises()

    const reviewBatchText = wrapper.get('[data-testid="question-answer-review-batch"]').text()
    expect(reviewBatchText).toContain(shortQuestionAnswerBatchId(activeBatch.batchId))
    expect(reviewBatchText).not.toContain('仍在运行')
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).not.toContain('进行中')
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
  })

  it('does not revive a cancelled runtime when selecting its active history snapshot returns late', async () => {
    const oldBatch = historicalBatch('late-active-stop-old', 'Late active stop old review')
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], activeBatch.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    const stoppedRuntime = batchWithStatuses(
      ['cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    let resolveActiveSelection: ((batch: typeof activeBatch) => void) | undefined
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      return new Promise<typeof activeBatch>(resolve => { resolveActiveSelection = resolve })
    })
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before late active stop selection')
    await reviewOld.trigger('click')
    await flushPromises()
    const reviewActive = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewActive) throw new Error('missing active history batch before stop')
    await reviewActive.trigger('click')

    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing stop during late active selection')
    await stop.trigger('click')
    await flushPromises()
    resolveActiveSelection?.(activeBatch)
    await flushPromises()

    const reviewBatchText = wrapper.get('[data-testid="question-answer-review-batch"]').text()
    expect(reviewBatchText).toContain(shortQuestionAnswerBatchId(activeBatch.batchId))
    expect(reviewBatchText).not.toContain('仍在运行')
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).not.toContain('进行中')
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
  })

  it('preserves a pending history page when a judgment save completes', async () => {
    const initialBatch = terminalReviewBatch()
    const updatedRecords = reviewRecords.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const updatedBatch = terminalReviewBatch(updatedRecords)
    const pageOne = {
      ...terminalReviewHistory(), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 2, totalItems: 21, totalPages: 2,
      stats: updatedBatch.stats,
      todayStats: updatedBatch.stats,
    }
    let pageTwoCalls = 0
    let resolvePendingPageTwo: ((history: typeof pageTwo) => void) | undefined
    let resolveJudgment: ((record: typeof updatedRecords[number]) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(initialBatch)
    harness.getQuestionAnswerBatch.mockResolvedValue(updatedBatch)
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number, signal: AbortSignal) => {
      if (page !== 2) return Promise.resolve(pageOne)
      pageTwoCalls++
      if (pageTwoCalls > 1) return Promise.resolve(pageTwo)
      return new Promise<typeof pageTwo>((resolve, reject) => {
        resolvePendingPageTwo = resolve
        signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
      })
    })
    harness.setQuestionAnswerJudgment.mockImplementation(() => (
      new Promise<typeof updatedRecords[number]>(resolve => { resolveJudgment = resolve })
    ))

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing pending-page judgment action')
    await correct.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing pending judgment history page two')
    await pageTwoButton.trigger('click')
    resolveJudgment?.(updatedRecords[0])
    await flushPromises()
    resolvePendingPageTwo?.(pageTwo)
    await flushPromises()

    expect(harness.getQuestionAnswerHistory).toHaveBeenLastCalledWith(
      'sub2api:ws1:acc-1', 2, expect.any(AbortSignal),
    )
    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')
  })

  it('cancels a pending historical selection before start and allows selecting the same batch again', async () => {
    const oldBatch = historicalBatch('retry-after-cancel', 'Retry after cancel')
    const newRuntimeBatchId = 'new-runtime-batch'
    const newRuntime = {
      ...activeBatch,
      batchId: newRuntimeBatchId,
      records: activeBatch.records.map(record => ({ ...record, batchId: newRuntimeBatchId })),
    }
    let oldBatchReads = 0
    let firstSelectionAborted = false
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory([oldBatch.records[0]]))
    harness.getQuestionAnswerBatch.mockImplementation((
      _targetId: string,
      batchId: string,
      signal?: AbortSignal,
    ) => {
      if (batchId !== oldBatch.batchId) return Promise.resolve(newRuntime)
      oldBatchReads++
      if (oldBatchReads > 1) return Promise.resolve(oldBatch)
      return new Promise<ReturnType<typeof historicalBatch>>((_, reject) => {
        signal?.addEventListener('abort', () => {
          firstSelectionAborted = true
          reject(new DOMException('Aborted', 'AbortError'))
        }, { once: true })
      })
    })
    harness.startQuestionAnswerBatch.mockResolvedValue(newRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const firstReview = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!firstReview) throw new Error('missing first historical batch action')
    await firstReview.trigger('click')
    await flushPromises()
    expect(oldBatchReads).toBe(1)

    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    if (!start) throw new Error('missing question-answer start action')
    await start.trigger('click')
    await flushPromises()
    expect(firstSelectionAborted).toBe(true)
    expect(harness.startQuestionAnswerBatch).toHaveBeenCalledTimes(1)

    const retryReview = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!retryReview) throw new Error('missing retry historical batch action')
    await retryReview.trigger('click')
    await flushPromises()

    expect(oldBatchReads).toBe(2)
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(oldBatch.batchId),
    )
  })

  it('does not let a late poll overwrite a newer judgment refresh', async () => {
    vi.useFakeTimers()
    const judgableRuntime = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const judgedRecords = judgableRuntime.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const judgedRuntime = {
      ...judgableRuntime,
      records: judgedRecords,
      stats: {
        ...judgableRuntime.stats,
        reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
      },
    }
    let resolveLatePoll: ((batch: typeof judgableRuntime) => void) | undefined
    let batchReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(judgableRuntime)
    harness.getQuestionAnswerBatch.mockImplementation(() => {
      batchReads++
      if (batchReads === 1) {
        return new Promise<typeof judgableRuntime>(resolve => { resolveLatePoll = resolve })
      }
      return Promise.resolve(judgedRuntime)
    })
    harness.setQuestionAnswerJudgment.mockResolvedValue(judgedRecords[0])

    const wrapper = await mountQuestionAnswerDialog()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const correct = judgmentButtons(rowContaining(wrapper, 'Question 1')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing active-runtime judgment action')
    await correct.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')

    resolveLatePoll?.(judgableRuntime)
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
  })

  it('does not let a late poll revive a stopped runtime', async () => {
    vi.useFakeTimers()
    const stoppedRuntime = batchWithStatuses(
      ['cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    let resolveLatePoll: ((batch: typeof activeBatch) => void) | undefined
    harness.getQuestionAnswerBatch.mockImplementation(() => (
      new Promise<typeof activeBatch>(resolve => { resolveLatePoll = resolve })
    ))
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing active-runtime stop action')
    await stop.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    resolveLatePoll?.(activeBatch)
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).not.toContain('进行中')
  })

  it('does not let a late judgment failure pollute a newly started batch', async () => {
    const newRuntimeBatchId = 'runtime-after-late-judgment'
    const newRuntime = {
      ...activeBatch,
      batchId: newRuntimeBatchId,
      records: activeBatch.records.map(record => ({ ...record, batchId: newRuntimeBatchId })),
    }
    let rejectOldJudgment: ((error: Error) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory())
    harness.setQuestionAnswerJudgment.mockImplementation(() => new Promise((_, reject) => {
      rejectOldJudgment = reject
    }))
    harness.startQuestionAnswerBatch.mockResolvedValue(newRuntime)

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing old-batch judgment action')
    await correct.trigger('click')
    await flushPromises()
    const start = wrapper.findAll('button').find(button => button.text().trim() === '开始回答')
    if (!start) throw new Error('missing question-answer start action')
    await start.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-review-batch"]').text()).toContain(
      shortQuestionAnswerBatchId(newRuntimeBatchId),
    )

    rejectOldJudgment?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(true)
  })

  it('keeps page one when clicking back before a late page-two response', async () => {
    const pageOneRecord = {
      ...reviewRecords[2], id: 'intent-page-one', batchId: 'page-one-intent-batch',
      questionName: 'Intent page one result',
    }
    const pageTwoRecord = {
      ...reviewRecords[3], id: 'intent-page-two', batchId: 'page-two-intent-batch',
      questionName: 'Intent page two result',
    }
    const pageOne = {
      ...terminalReviewHistory([pageOneRecord]), page: 1, totalItems: 21, totalPages: 2,
    }
    const pageTwo = {
      ...terminalReviewHistory([pageTwoRecord]), page: 2, totalItems: 21, totalPages: 2,
    }
    let resolveLatePageTwo: ((history: typeof pageTwo) => void) | undefined
    let pageOneReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) {
        return new Promise<typeof pageTwo>(resolve => { resolveLatePageTwo = resolve })
      }
      pageOneReads++
      return Promise.resolve(pageOne)
    })

    const wrapper = await mountQuestionAnswerDialog()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    const pageOneButton = wrapper.findAll('button').find(button => button.text().trim() === '1')
    if (!pageOneButton) throw new Error('missing history page one')
    await pageOneButton.trigger('click')
    await flushPromises()
    expect(pageOneReads).toBe(2)

    resolveLatePageTwo?.(pageTwo)
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '1')?.classes()).toContain('bg-primary')
    expect(wrapper.text()).toContain(shortQuestionAnswerBatchId(pageOneRecord.batchId))
    expect(wrapper.text()).not.toContain(shortQuestionAnswerBatchId(pageTwoRecord.batchId))
  })

  it('does not show a late poll-history failure after a newer page intent', async () => {
    vi.useFakeTimers()
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    const pageOne = { ...terminalReviewHistory(), page: 1, totalItems: 21, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 2, totalItems: 21, totalPages: 2,
    }
    let pageOneReads = 0
    let rejectPollHistory: ((error: Error) => void) | undefined
    harness.getQuestionAnswerBatch.mockResolvedValue(completedRuntime)
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) return Promise.resolve(pageTwo)
      pageOneReads++
      if (pageOneReads === 1) return Promise.resolve(pageOne)
      return new Promise((_, reject) => { rejectPollHistory = reject })
    })

    const wrapper = await mountQuestionAnswerDialog()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing poll-history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    rejectPollHistory?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('does not show a late stop-history failure after a newer page intent', async () => {
    const stoppedRuntime = batchWithStatuses(
      ['cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    const pageOne = { ...terminalReviewHistory(), page: 1, totalItems: 21, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 2, totalItems: 21, totalPages: 2,
    }
    let pageOneReads = 0
    let rejectStopHistory: ((error: Error) => void) | undefined
    harness.cancelQuestionAnswerBatch.mockResolvedValue(stoppedRuntime)
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) return Promise.resolve(pageTwo)
      pageOneReads++
      if (pageOneReads === 1) return Promise.resolve(pageOne)
      return new Promise((_, reject) => { rejectStopHistory = reject })
    })

    const wrapper = await mountQuestionAnswerDialog()
    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing stop-history action')
    await stop.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing stop-history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    rejectStopHistory?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('does not show a late judgment-history failure after a newer page intent', async () => {
    const updatedRecords = reviewRecords.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const updatedBatch = terminalReviewBatch(updatedRecords)
    const pageOne = { ...terminalReviewHistory(), page: 1, totalItems: 21, totalPages: 2 }
    const pageTwo = {
      ...terminalReviewHistory([reviewRecords[2]]), page: 2, totalItems: 21, totalPages: 2,
    }
    let pageOneReads = 0
    let rejectJudgmentHistory: ((error: Error) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(terminalReviewBatch())
    harness.getQuestionAnswerBatch.mockResolvedValue(updatedBatch)
    harness.setQuestionAnswerJudgment.mockResolvedValue(updatedRecords[0])
    harness.getQuestionAnswerHistory.mockImplementation((_targetId: string, page: number) => {
      if (page === 2) return Promise.resolve(pageTwo)
      pageOneReads++
      if (pageOneReads === 1) return Promise.resolve(pageOne)
      return new Promise((_, reject) => { rejectJudgmentHistory = reject })
    })

    const wrapper = await mountQuestionAnswerDialog()
    const correct = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing judgment-history action')
    await correct.trigger('click')
    await flushPromises()
    const pageTwoButton = wrapper.findAll('button').find(button => button.text().trim() === '2')
    if (!pageTwoButton) throw new Error('missing judgment-history page two')
    await pageTwoButton.trigger('click')
    await flushPromises()
    rejectJudgmentHistory?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.findAll('button').find(button => button.text().trim() === '2')?.classes()).toContain('bg-primary')
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('does not show an older same-batch judgment-refresh failure after a newer success', async () => {
    const initialRecords = reviewRecords.slice(0, 2)
    const newerRecords = initialRecords.map(record => ({
      ...record,
      answerJudgment: 'correct' as const,
      manualError: false,
    }))
    const initialBatch = terminalReviewBatch(initialRecords)
    const newerBatch = terminalReviewBatch(newerRecords)
    let rejectOlderRefresh: ((error: Error) => void) | undefined
    let batchReads = 0
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(initialBatch)
    harness.getQuestionAnswerHistory.mockResolvedValue(terminalReviewHistory(initialRecords))
    harness.getQuestionAnswerBatch.mockImplementation(() => {
      batchReads++
      if (batchReads === 1) return new Promise((_, reject) => { rejectOlderRefresh = reject })
      return Promise.resolve(newerBatch)
    })
    harness.setQuestionAnswerJudgment.mockImplementation((
      _targetId: string,
      recordId: string,
    ) => Promise.resolve(recordId === newerRecords[0].id ? newerRecords[0] : newerRecords[1]))

    const wrapper = await mountQuestionAnswerDialog()
    const firstCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed one')).find(
      button => button.text().trim() === '正确',
    )
    if (!firstCorrect) throw new Error('missing older judgment action')
    await firstCorrect.trigger('click')
    await flushPromises()
    const secondCorrect = judgmentButtons(rowContaining(wrapper, 'Unreviewed long')).find(
      button => button.text().trim() === '正确',
    )
    if (!secondCorrect) throw new Error('missing newer judgment action')
    await secondCorrect.trigger('click')
    await flushPromises()
    rejectOlderRefresh?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确2')
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('does not let an old poll overwrite a newer active-runtime selection', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('poll-selection-old', 'Poll selection old review')
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const selectedRecords = runtimeWithAnswer.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const selectedRuntime = {
      ...runtimeWithAnswer,
      records: selectedRecords,
      stats: { ...runtimeWithAnswer.stats, reviews: { unreviewed: 0, correct: 1, incorrect: 0 } },
    }
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], runtimeWithAnswer.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    let runtimeReads = 0
    let resolveOldPoll: ((batch: typeof runtimeWithAnswer) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      runtimeReads++
      if (runtimeReads === 1) {
        return new Promise<typeof runtimeWithAnswer>(resolve => { resolveOldPoll = resolve })
      }
      return Promise.resolve(selectedRuntime)
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before runtime selection')
    await reviewOld.trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const reviewRuntime = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewRuntime) throw new Error('missing active runtime selection')
    await reviewRuntime.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')

    resolveOldPoll?.(runtimeWithAnswer)
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
  })

  it('does not let an old active poll revive a terminal runtime selection', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('poll-terminal-old', 'Poll terminal old review')
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], activeBatch.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    let runtimeReads = 0
    let resolveOldPoll: ((batch: typeof activeBatch) => void) | undefined
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      runtimeReads++
      if (runtimeReads === 1) {
        return new Promise<typeof activeBatch>(resolve => { resolveOldPoll = resolve })
      }
      return Promise.resolve(completedRuntime)
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before terminal runtime selection')
    await reviewOld.trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    const reviewRuntime = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewRuntime) throw new Error('missing terminal runtime selection')
    await reviewRuntime.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    resolveOldPoll?.(activeBatch)
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).not.toContain('进行中')
  })

  it('does not cancel a newer poll when an older runtime selection returns first', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('selection-before-poll-old', 'Selection before poll old review')
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const polledRecords = runtimeWithAnswer.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const polledRuntime = {
      ...runtimeWithAnswer,
      records: polledRecords,
      stats: { ...runtimeWithAnswer.stats, reviews: { unreviewed: 0, correct: 1, incorrect: 0 } },
    }
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], runtimeWithAnswer.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    let runtimeReads = 0
    let resolveSelection: ((batch: typeof runtimeWithAnswer) => void) | undefined
    let resolveNewerPoll: ((batch: typeof polledRuntime) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      runtimeReads++
      if (runtimeReads === 1) {
        return new Promise<typeof runtimeWithAnswer>(resolve => { resolveSelection = resolve })
      }
      return new Promise<typeof polledRuntime>(resolve => { resolveNewerPoll = resolve })
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before selection-first race')
    await reviewOld.trigger('click')
    await flushPromises()
    const reviewRuntime = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewRuntime) throw new Error('missing runtime selection before newer poll')
    await reviewRuntime.trigger('click')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    resolveSelection?.(runtimeWithAnswer)
    await flushPromises()
    resolveNewerPoll?.(polledRuntime)
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
  })

  it('does not let a newer active poll revive a terminal runtime selection', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('terminal-selection-before-poll', 'Terminal selection before poll')
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], activeBatch.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    let runtimeReads = 0
    let resolveSelection: ((batch: typeof completedRuntime) => void) | undefined
    let resolveNewerPoll: ((batch: typeof activeBatch) => void) | undefined
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      runtimeReads++
      if (runtimeReads === 1) {
        return new Promise<typeof completedRuntime>(resolve => { resolveSelection = resolve })
      }
      return new Promise<typeof activeBatch>(resolve => { resolveNewerPoll = resolve })
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before terminal selection race')
    await reviewOld.trigger('click')
    await flushPromises()
    const reviewRuntime = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewRuntime) throw new Error('missing terminal runtime selection before newer poll')
    await reviewRuntime.trigger('click')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    resolveSelection?.(completedRuntime)
    await flushPromises()
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    resolveNewerPoll?.(activeBatch)
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).not.toContain('进行中')
  })

  it('does not show a newer poll failure after a terminal runtime selection', async () => {
    vi.useFakeTimers()
    const oldBatch = historicalBatch('terminal-before-poll-failure', 'Terminal before poll failure')
    const completedRuntime = batchWithStatuses(
      ['succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded', 'succeeded'],
      false,
    )
    const history = {
      ...emptyHistory,
      records: [oldBatch.records[0], activeBatch.records[0]],
      totalItems: 2,
      totalPages: 1,
    }
    let runtimeReads = 0
    let resolveSelection: ((batch: typeof completedRuntime) => void) | undefined
    let rejectNewerPoll: ((error: Error) => void) | undefined
    harness.getQuestionAnswerHistory.mockResolvedValue(history)
    harness.getQuestionAnswerBatch.mockImplementation((_targetId: string, batchId: string) => {
      if (batchId === oldBatch.batchId) return Promise.resolve(oldBatch)
      runtimeReads++
      if (runtimeReads === 1) {
        return new Promise<typeof completedRuntime>(resolve => { resolveSelection = resolve })
      }
      return new Promise((_, reject) => { rejectNewerPoll = reject })
    })

    const wrapper = await mountQuestionAnswerDialog()
    const reviewOld = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewOld) throw new Error('missing old batch before terminal poll failure')
    await reviewOld.trigger('click')
    await flushPromises()
    const reviewRuntime = wrapper.findAll('button').find(button => button.text().trim() === '复审此批次')
    if (!reviewRuntime) throw new Error('missing terminal selection before poll failure')
    await reviewRuntime.trigger('click')
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    resolveSelection?.(completedRuntime)
    await flushPromises()

    rejectNewerPoll?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

  it('does not let a late stop response erase a newer terminal judgment refresh', async () => {
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const judgedRecords = runtimeWithAnswer.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const judgedTerminal = {
      ...batchWithStatuses(
        ['succeeded', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
        false,
      ),
      records: judgedRecords.map((record, index) => (
        index === 0
          ? { ...record, status: 'succeeded' as const, completedAt: '2026-08-26T12:00:02Z' }
          : { ...record, status: 'cancelled' as const, answerJudgment: null, completedAt: '2026-08-26T12:00:02Z' }
      )),
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 1, failed: 0, cancelled: 5 },
        reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
        byModel: [],
      },
    }
    const staleStopped = batchWithStatuses(
      ['succeeded', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
      false,
    )
    let resolveJudgmentBatch: ((batch: typeof judgedTerminal) => void) | undefined
    let resolveStop: ((batch: typeof staleStopped) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerBatch.mockImplementation(() => (
      new Promise<typeof judgedTerminal>(resolve => { resolveJudgmentBatch = resolve })
    ))
    harness.cancelQuestionAnswerBatch.mockImplementation(() => (
      new Promise<typeof staleStopped>(resolve => { resolveStop = resolve })
    ))
    harness.setQuestionAnswerJudgment.mockResolvedValue(judgedRecords[0])

    const wrapper = await mountQuestionAnswerDialog()
    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing stop before judgment refresh')
    await stop.trigger('click')
    await flushPromises()
    const correct = judgmentButtons(rowContaining(wrapper, 'Question 1')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing judgment before late stop response')
    await correct.trigger('click')
    await flushPromises()

    resolveJudgmentBatch?.(judgedTerminal)
    await flushPromises()
    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')

    resolveStop?.(staleStopped)
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')
    expect(wrapper.get('[data-testid="question-answer-pending"]').findAll('li')).toHaveLength(0)
  })

  it('does not show a late stop failure after a newer terminal judgment refresh', async () => {
    const runtimeWithAnswer = batchWithStatuses(
      ['succeeded', 'running', 'running', 'running', 'running', 'running'],
      true,
    )
    const judgedRecords = runtimeWithAnswer.records.map((record, index) => (
      index === 0 ? { ...record, answerJudgment: 'correct' as const } : record
    ))
    const judgedTerminal = {
      ...batchWithStatuses(
        ['succeeded', 'cancelled', 'cancelled', 'cancelled', 'cancelled', 'cancelled'],
        false,
      ),
      records: judgedRecords.map((record, index) => (
        index === 0
          ? { ...record, status: 'succeeded' as const, completedAt: '2026-08-26T12:00:02Z' }
          : { ...record, status: 'cancelled' as const, answerJudgment: null, completedAt: '2026-08-26T12:00:02Z' }
      )),
      stats: {
        requests: { submitted: 6, inProgress: 0, succeeded: 1, failed: 0, cancelled: 5 },
        reviews: { unreviewed: 0, correct: 1, incorrect: 0 },
        byModel: [],
      },
    }
    let resolveJudgmentBatch: ((batch: typeof judgedTerminal) => void) | undefined
    let rejectStop: ((error: Error) => void) | undefined
    harness.getLatestQuestionAnswerBatch.mockResolvedValue(runtimeWithAnswer)
    harness.getQuestionAnswerBatch.mockImplementation(() => (
      new Promise<typeof judgedTerminal>(resolve => { resolveJudgmentBatch = resolve })
    ))
    harness.cancelQuestionAnswerBatch.mockImplementation(() => (
      new Promise((_, reject) => { rejectStop = reject })
    ))
    harness.setQuestionAnswerJudgment.mockResolvedValue(judgedRecords[0])

    const wrapper = await mountQuestionAnswerDialog()
    const stop = wrapper.findAll('button').find(button => button.text().includes('终止本次问答'))
    if (!stop) throw new Error('missing stop before terminal judgment refresh')
    await stop.trigger('click')
    await flushPromises()
    const correct = judgmentButtons(rowContaining(wrapper, 'Question 1')).find(
      button => button.text().trim() === '正确',
    )
    if (!correct) throw new Error('missing judgment before late stop failure')
    await correct.trigger('click')
    await flushPromises()

    resolveJudgmentBatch?.(judgedTerminal)
    await flushPromises()
    rejectStop?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()

    expect(wrapper.get('[data-testid="question-answer-stats-review"]').text()).toContain('正确1')
    expect(wrapper.text()).not.toContain('操作失败，请稍后重试。')
  })

})

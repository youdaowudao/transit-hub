// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import ManualOneTimeProbeDialog from '@/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue'

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

const emptyStats = {
  requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
  reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
}

const records = Array.from({ length: 6 }, (_, index) => ({
  id: `record-${index + 1}`,
  targetId: 'sub2api:ws1:acc-1',
  batchId: 'batch-running',
  modelName: 'model-a',
  questionId: `q${index + 1}`,
  questionName: `Question ${index + 1}`,
  questionBody: `Question body ${index + 1}`,
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
  submittedCount: 6,
  completedCount: 0,
  runningCount: 5,
  active: true,
  currentModel: 'legacy-model',
  currentQuestion: 'legacy-question',
  stats: {
    requests: { submitted: 6, inProgress: 6, succeeded: 0, failed: 0, cancelled: 0 },
    reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
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

const mountedWrappers: VueWrapper[] = []

beforeEach(() => {
  harness.discoverModels.mockReset().mockResolvedValue({ models: [{ id: 'model-a', name: 'Model A' }] })
  harness.listTestQuestions.mockReset().mockResolvedValue(records.map((record, index) => ({
    id: record.questionId,
    name: record.questionName,
    body: record.questionBody,
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
      target: {
        targetId: 'sub2api:ws1:acc-1',
        accountName: 'Concurrent Account',
        platform: 'sub2api',
        type: 'subscription',
        status: 'active',
        groupName: 'Group A',
        formalModels: [],
      },
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
  },
})

const rowContaining = (wrapper: VueWrapper, text: string) => {
  const row = wrapper.findAll('li').find(candidate => candidate.text().includes(text))
  if (!row) throw new Error(`missing row containing ${text}`)
  return row
}

const judgmentButtons = (wrapper: ReturnType<typeof rowContaining>) => wrapper.findAll('button').filter((button) => {
  const text = button.text().trim()
  return text === '正确' || text === '错误'
})

describe('question-answer batch behavior', () => {
  it('shows the actual number of concurrent requests instead of one legacy model-question pair', async () => {
    const wrapper = await mountQuestionAnswerDialog()

    expect(wrapper.text()).toContain('正在处理 5 项')
    expect(wrapper.text()).not.toContain('正在测试：legacy-model × legacy-question')
    expect(wrapper.text()).toContain('完成 0/6')
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
    expect(wrapper.text()).toContain('完成 1/6')
    expect(wrapper.text()).toContain('正在处理 5 项')

    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(harness.getQuestionAnswerBatch).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('完成 6/6')
    expect(wrapper.text()).toContain('本次问答已完成，结果和统计已刷新。')
    expect(wrapper.text()).not.toContain('正在处理')
  })

  it('cancels the active batch and keeps cancelled records behind the show-all action', async () => {
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
    expect(wrapper.text()).toContain('完成 6/6')
    expect(wrapper.text()).not.toContain('正在处理')
    expect(wrapper.text()).not.toContain('已终止')
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)

    const showAll = wrapper.findAll('button').find(button => button.text().trim() === '查看全部')
    if (!showAll) throw new Error('missing show-all button after stopping question answers')
    await showAll.trigger('click')
    expect(wrapper.text().match(/已终止/g)).toHaveLength(6)
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
      },
    })

    const wrapper = await mountQuestionAnswerDialog()

    expect(judgmentButtons(rowContaining(wrapper, 'Unreviewed one'))).toHaveLength(2)
    for (const label of ['Active pending', 'Active running', 'Active failed', 'Active cancelled']) {
      expect(judgmentButtons(rowContaining(wrapper, label))).toHaveLength(0)
    }
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

    expect(wrapper.text()).toContain('Question 1')
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
    const reviewGrid = rowContaining(wrapper, 'Unreviewed one').element.parentElement
    expect(reviewGrid?.classList.contains('grid-cols-1')).toBe(true)
    expect(reviewGrid?.classList.contains('md:grid-cols-2')).toBe(false)
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
    expect(wrapper.text()).toContain('待复审')

    rejectSave?.(new Error('admin.connectionHealth.errors.request'))
    await flushPromises()
    expect(wrapper.text()).toContain('Answer unreviewed one')
    expect(wrapper.text()).toContain('待复审')
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
    const showAll = wrapper.findAll('button').find(button => button.text().trim() === '查看全部')
    if (!showAll) throw new Error('missing show-all button')

    await showAll.trigger('click')
    await flushPromises()

    for (const text of ['Answer reviewed correct', 'Answer reviewed incorrect', 'Question failed', 'Question cancelled']) {
      expect(wrapper.text()).toContain(text)
    }
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
    expect(judgmentButtons(rowContaining(wrapper, 'Request cancelled'))).toHaveLength(0)
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
    const showAll = wrapper.findAll('button').find(button => button.text().trim() === '查看全部')
    if (!showAll) throw new Error('missing show-all button')
    await showAll.trigger('click')
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
})

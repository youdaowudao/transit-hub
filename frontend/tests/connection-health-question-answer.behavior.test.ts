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
  setQuestionAnswerManualError: vi.fn(),
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
  setQuestionAnswerManualError: harness.setQuestionAnswerManualError,
  startQuestionAnswerBatch: harness.startQuestionAnswerBatch,
}))

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
}

const emptyHistory = {
  records: [],
  page: 1,
  pageSize: 20 as const,
  totalItems: 0,
  totalPages: 0,
  stats: { total: 0, normal: 0, errors: 0 },
  todayStats: { total: 0, normal: 0, errors: 0 },
}

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
  harness.setQuestionAnswerManualError.mockReset()
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
    completedAt: statuses[index] === 'pending' || statuses[index] === 'running'
      ? null
      : '2026-08-26T12:00:02Z',
  })),
  completedCount: statuses.filter(status => !['pending', 'running'].includes(status)).length,
  runningCount: statuses.filter(status => status === 'running').length,
  active,
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

  it('cancels the active batch and renders every record as terminated', async () => {
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
    expect(wrapper.text().match(/已终止/g)).toHaveLength(6)
    expect(wrapper.findAll('button').some(button => button.text().includes('终止本次问答'))).toBe(false)
  })
})

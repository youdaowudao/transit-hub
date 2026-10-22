// @vitest-environment jsdom
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import QuestionAnswerExecutionDetails from '@/modules/admin/components/dashboard/QuestionAnswerExecutionDetails.vue'
import type { QuestionAnswerDiagnostics, QuestionAnswerRecord } from '@/modules/admin/types/connectionHealth'

const wrappers: VueWrapper[] = []
const record = (status: 'pending' | 'running'): QuestionAnswerRecord => ({
  id: 'storage-display-record', targetId: 'sub2api:ws1:storage-display-account', batchId: 'storage-display-batch',
  modelName: 'm', questionId: 'q', questionName: '保存过程隔离题', questionBody: 'q', questionKeywordSnapshot: [],
  reasoningEffort: 'medium', requestProtocol: 'chat_completions', repeatIndex: 1, answerBody: '', status, errorType: '',
  answerJudgment: null, judgmentSource: null, manualError: false,
  createdAt: '2026-10-22T01:00:00Z', startedAt: null, completedAt: null, updatedAt: '2026-10-22T01:00:00Z',
})
const diagnostics = (): QuestionAnswerDiagnostics => ({
  version: 1, selectedAt: '2026-10-22T01:00:01Z', timeoutMs: 600000,
  requestStartedAt: '2026-10-22T01:00:02Z', requestFinishedAt: '2026-10-22T01:00:04Z', requestElapsedMs: 2000,
  requestWrittenAt: null, firstResponseByteAt: null, responseHeadersAt: null, lastReceivedAt: null, receivedBytes: 0,
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.restoreAllMocks(); vi.useRealTimers() })

it('explains a retained starting failure as no answer API call rather than a finished HTTP request', () => {
  const r = record('pending')
  const wrapper = mount(QuestionAnswerExecutionDetails, { props: {
    record: r, snapshotAt: '2026-10-22T01:00:12Z', runtime: {
      recordId: r.id, step: 'confirming', pendingResultStatus: 'failed', storageError: 'fixture start write timed out',
      diagnostics: { ...diagnostics(), timeoutMs: null, requestStartedAt: null, requestFinishedAt: null, requestElapsedMs: null,
        failureStep: 'starting', localError: 'fixture start write timed out' },
    },
  } }); wrappers.push(wrapper)
  const stage = wrapper.get('[data-testid="question-answer-process-stage"]').text()
  expect(stage).toContain('没有调用答题接口')
  expect(stage).toContain('fixture start write timed out')
  expect(stage).not.toContain('请求已结束')
  expect(stage).not.toContain('上游失败')
  expect(wrapper.text()).not.toContain('HTTP等待 0.0秒')
})

it('keeps completed HTTP time separate from result-save waiting and freezes the latter on a stale snapshot', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))
  const r = record('running')
  const wrapper = mount(QuestionAnswerExecutionDetails, { props: {
    record: r, snapshotAt: '2026-10-22T01:00:12Z', runtime: {
      recordId: r.id, step: 'confirming', pendingResultStatus: 'succeeded', storageError: 'fixture completion save interrupted', diagnostics: diagnostics(),
    },
  } }); wrappers.push(wrapper)
  const saveWaitIncludes = (elapsed: string) => wrapper.findAll('p').some(p => /保存|从请求结束到现在/.test(p.text()) && p.text().includes(elapsed))
  expect(wrapper.text()).toContain('已取得完整回答')
  expect(wrapper.text()).toContain('HTTP等待 2.0秒')
  expect(saveWaitIncludes('8.0秒')).toBe(true)
  await vi.advanceTimersByTimeAsync(3000)
  expect(saveWaitIncludes('11.0秒')).toBe(true)
  expect(wrapper.text()).toContain('HTTP等待 2.0秒')
  expect(wrapper.text()).not.toContain('HTTP已等待')
  await wrapper.setProps({ stale: true })
  const frozen = wrapper.text()
  await vi.advanceTimersByTimeAsync(20000)
  expect(wrapper.text()).toBe(frozen)
  expect(wrapper.text()).not.toContain('答案正确')
})

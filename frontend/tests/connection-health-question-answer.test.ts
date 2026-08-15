import { readFileSync } from 'node:fs'

import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  cancelQuestionAnswerBatch,
  getQuestionAnswerHistory,
  setQuestionAnswerManualError,
  startQuestionAnswerBatch,
} from '../src/modules/admin/api/connectionHealth'
import type { QuestionAnswerRecord } from '../src/modules/admin/types/connectionHealth'
import {
  isCurrentQuestionAnswerOperation,
  questionAnswerCompletedTodayInShanghai,
  questionAnswerElapsedMilliseconds,
} from '../src/modules/admin/utils/questionAnswers'
import {
  clearQuestionAnswerUnread,
  createDefaultConnectionHealthPreferences,
  markQuestionAnswerUnread,
} from '../src/modules/admin/utils/connectionHealthPreferences'

const dialogSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/ManualOneTimeProbeDialog.vue', import.meta.url),
  'utf8',
)
const questionsSource = readFileSync(
  new URL('../src/modules/admin/components/settings/TestQuestionsPanel.vue', import.meta.url),
  'utf8',
)
const settingsSource = readFileSync(
  new URL('../src/modules/admin/views/SettingsView.vue', import.meta.url),
  'utf8',
)
const healthViewSource = readFileSync(
  new URL('../src/modules/admin/views/ConnectionHealthView.vue', import.meta.url),
  'utf8',
)
const groupDetailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)

const stubStorage = () => vi.stubGlobal('localStorage', { getItem: vi.fn().mockReturnValue(null) })

const questionAnswerRecord = (
  id: string,
  status: QuestionAnswerRecord['status'],
  completedAt: string | null,
): QuestionAnswerRecord => ({
  id,
  targetId: 'target-a',
  batchId: 'batch-a',
  modelName: `model-${id}`,
  questionId: `question-${id}`,
  questionName: `Question ${id}`,
  questionBody: `Body ${id}`,
  answerBody: `Answer ${id}`,
  status,
  errorType: '',
  manualError: false,
  createdAt: '2026-08-15T00:00:00.000Z',
  startedAt: completedAt,
  completedAt,
  updatedAt: completedAt ?? '2026-08-15T00:00:00.000Z',
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('connection health question answers', () => {
  it('rejects late question-answer responses after close, mode switch, or target replacement', () => {
    const scope = { sequence: 7, targetId: 'target-a', batchId: 'batch-a' }
    const current = {
      sequence: 7,
      open: true,
      mode: 'questionAnswer',
      targetId: 'target-a',
      batchId: 'batch-a',
    }

    expect(isCurrentQuestionAnswerOperation(scope, current)).toBe(true)
    expect(isCurrentQuestionAnswerOperation(scope, { ...current, sequence: 8 })).toBe(false)
    expect(isCurrentQuestionAnswerOperation(scope, { ...current, open: false })).toBe(false)
    expect(isCurrentQuestionAnswerOperation(scope, { ...current, mode: 'formal' })).toBe(false)
    expect(isCurrentQuestionAnswerOperation(scope, { ...current, targetId: 'target-b' })).toBe(false)
    expect(isCurrentQuestionAnswerOperation(scope, { ...current, batchId: 'batch-b' })).toBe(false)
  })

  it('calculates answer elapsed time and Shanghai calendar-day membership', () => {
    const completed = {
      ...questionAnswerRecord('timed', 'succeeded', '2026-08-14T16:00:03.250Z'),
      startedAt: '2026-08-14T16:00:01.000Z',
    }
    const running = {
      ...questionAnswerRecord('running', 'running', null),
      startedAt: '2026-08-14T16:00:01.000Z',
    }
    const now = new Date('2026-08-15T02:00:00.000Z')

    expect(questionAnswerElapsedMilliseconds(completed)).toBe(2250)
    expect(questionAnswerElapsedMilliseconds(running, Date.parse('2026-08-14T16:00:04.000Z'))).toBe(3000)
    expect(questionAnswerCompletedTodayInShanghai(completed, now)).toBe(true)
    expect(questionAnswerCompletedTodayInShanghai({ ...completed, completedAt: '2026-08-14T15:59:59.000Z' }, now)).toBe(false)
  })

  it('keeps unread question-answer reminders by stable target ID until the question-answer view is opened', () => {
    const initial = createDefaultConnectionHealthPreferences()
    const marked = markQuestionAnswerUnread(initial, 'target-a')
    const duplicate = markQuestionAnswerUnread(marked, 'target-a')
    const second = markQuestionAnswerUnread(duplicate, 'target-b')

    expect(marked.questionAnswerUnreadTargetIds).toEqual(['target-a'])
    expect(duplicate).toBe(marked)
    expect(clearQuestionAnswerUnread(second, 'target-a').questionAnswerUnreadTargetIds).toEqual(['target-b'])
    expect(dialogSource).toContain("emit('question-answer-started', targetId)")
    expect(dialogSource).toContain("emit('question-answer-viewed', props.target.targetId)")
    expect(healthViewSource).toContain(':question-answer-unread-target-ids="preferences.questionAnswerUnreadTargetIds"')
    expect(healthViewSource).toContain('@question-answer-started="onQuestionAnswerStarted"')
    expect(healthViewSource).toContain('@question-answer-viewed="onQuestionAnswerViewed"')
    expect(groupDetailSource).toContain('questionAnswerUnreadTargetIds.includes(account.targetId)')
    expect(groupDetailSource).toContain('bg-amber-500 text-white')
  })

  it('renders the current batch as expandable responsive result cards without a duplicate latest-answer block', () => {
    expect(dialogSource).not.toContain('latestBatchAnswer')
    expect(dialogSource).toContain("if (count === 2) return 'grid-cols-1 md:grid-cols-2'")
    expect(dialogSource).toContain("return 'grid-cols-1 md:grid-cols-2 xl:grid-cols-3'")
    expect(dialogSource).toContain(':class="currentBatchGridClass"')
    expect(dialogSource).toContain('qaCurrentExpanded.has(record.id)')
    expect(dialogSource).toContain('toggleCurrentQuestionAnswerExpanded(record.id)')
    expect(dialogSource).toContain('{{ record.questionBody }}')
    expect(dialogSource).toContain('{{ questionAnswerCurrentAnswer(record) }}')
  })

  it('emphasizes normal and error counts while keeping totals secondary', () => {
    expect(dialogSource).toContain('questionAnswer.stats.total`, { total: qaHistory.stats.total }')
    expect(dialogSource).toContain('questionAnswer.stats.total`, { total: qaHistory.todayStats.total }')
    expect(dialogSource).toContain('<dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.stats.normal }}</dd>')
    expect(dialogSource).toContain('<dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.stats.errors }}</dd>')
    expect(dialogSource).toContain('<dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.todayStats.normal }}</dd>')
    expect(dialogSource).toContain('<dd class="mt-0.5 text-2xl font-semibold text-foreground">{{ qaHistory.todayStats.errors }}</dd>')
    expect(dialogSource).not.toContain('text-2xl font-semibold text-foreground">{{ qaHistory.stats.total }}')
    expect(dialogSource).not.toContain('text-2xl font-semibold text-foreground">{{ qaHistory.todayStats.total }}')
  })

  it('submits model and question selections and exposes explicit cancellation', async () => {
    stubStorage()
    const batch = {
      batchId: 'batch-1', records: [], submittedCount: 4, completedCount: 0,
      active: true, currentModel: 'model-a', currentQuestion: 'question-a',
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(batch), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...batch, active: false }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await expect(startQuestionAnswerBatch(
      'sub2api:ws1:account-1',
      ['model-a', 'model-b'],
      ['question-a', 'question-b'],
      controller.signal,
    )).resolves.toEqual(batch)
    await cancelQuestionAnswerBatch('sub2api:ws1:account-1', 'batch-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/batches',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ models: ['model-a', 'model-b'], questionIds: ['question-a', 'question-b'] }),
        signal: controller.signal,
      }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/batches/batch-1/cancel',
      expect.objectContaining({ method: 'POST' }),
    )
  })

  it('uses page-scoped history and record-scoped automatic marking endpoints', async () => {
    stubStorage()
    const history = {
      records: [], page: 2, pageSize: 20, totalItems: 21, totalPages: 2,
      stats: { total: 20, normal: 18, errors: 2 },
      todayStats: { total: 4, normal: 3, errors: 1 },
    }
    const record = { id: 'record-9', manualError: true }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(history), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(record), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getQuestionAnswerHistory('sub2api:ws1:account-1', 2)).resolves.toEqual(history)
    await expect(setQuestionAnswerManualError('sub2api:ws1:account-1', 'record-9', true)).resolves.toEqual(record)

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/history?page=2',
      expect.any(Object),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/records/record-9/manual-error',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ manualError: true }) }),
    )
  })

  it('keeps the existing dialog frame and polls without cancelling background work on close', () => {
    const closeBody = dialogSource.match(/const close = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const cleanupBody = dialogSource.match(/const cleanupFrontendWork = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const startBody = dialogSource.match(/const startQuestionAnswers = async \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const stopBody = dialogSource.match(/const stopQuestionAnswers = async \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const modelIndex = dialogSource.indexOf('v-for="model in models"')
    const questionIndex = dialogSource.indexOf('questionAnswer.questionsTitle')
    const currentIndex = dialogSource.indexOf('questionAnswer.currentTitle')
    const historyIndex = dialogSource.indexOf('questionAnswer.historyTitle')

    expect(dialogSource).toContain("type ProbeMode = 'once' | 'formal' | 'questionAnswer'")
    expect(dialogSource).toContain('h-[min(760px,calc(100dvh-2rem))] w-full max-w-6xl')
    expect(dialogSource).toContain('question => question.isDefault')
    expect(dialogSource).toContain('batch.records.map(record => record.modelName)')
    expect(dialogSource).toContain('batch.records.map(record => record.questionId)')
    expect(dialogSource).toContain('selected.value.size * qaSelectedQuestions.value.size')
    expect(dialogSource).toContain('setTimeout(() => void pollQuestionAnswerBatch(), 2000)')
    expect(dialogSource.match(/getQuestionAnswerBatch\(targetId, batchId, controller.signal\)/g)).toHaveLength(2)
    expect(dialogSource).toContain('qaCompletedNotice.value = true')
    expect(dialogSource).toContain('onBeforeUnmount(cleanupFrontendWork)')
    expect(closeBody).toContain('cleanupFrontendWork()')
    expect(closeBody).not.toContain('cancelQuestionAnswerBatch')
    expect(cleanupBody).toContain('cancelQuestionAnswerStart()')
    expect(cleanupBody).toContain('cancelModelDiscovery()')
    expect(cleanupBody).toContain('cancelQuestionAnswerRequests()')
    expect(dialogSource).toContain('qaStarting.value = false')
    expect(dialogSource).toContain('const controller = beginModelDiscovery()')
    expect(dialogSource).toContain("mode !== 'questionAnswer'")
    expect(dialogSource).toContain("record.status === 'pending' || record.status === 'running'")
    expect(dialogSource).toContain('questionAnswerElapsedLabel(record)')
    expect(dialogSource).toContain('qaHistory.todayStats.total')
    expect(dialogSource).toContain('questionAnswer.stats.todayShanghai')
    expect(startBody.indexOf('scheduleQuestionAnswerPoll()')).toBeLessThan(startBody.indexOf('getQuestionAnswerHistory(targetId, 1, controller.signal)'))
    expect(stopBody).toContain('cancelQuestionAnswerBatch(targetId, batchId, controller.signal)')
    expect(stopBody).toContain('questionAnswerScopeIsCurrent(scope)')
    expect(modelIndex).toBeGreaterThan(-1)
    expect(modelIndex).toBeLessThan(questionIndex)
    expect(questionIndex).toBeLessThan(currentIndex)
    expect(currentIndex).toBeLessThan(historyIndex)
  })

  it('uses record IDs, fixed status semantics, and optimistic marking rollback', () => {
    expect(dialogSource.match(/:key="record.id"/g)).toHaveLength(2)
    expect(dialogSource).toContain("record.status === 'failed' || record.manualError")
    expect(dialogSource).toContain("record.status === 'cancelled' || record.status === 'pending' || record.status === 'running'")
    expect(dialogSource).toContain("record.status !== 'succeeded'")
    expect(dialogSource).toContain('manualError: nextValue')
    expect(dialogSource).toContain('manualError: record.manualError')
    expect(dialogSource).toContain("record.manualError ? 'border-red-500/60 bg-red-500/20")
    expect(dialogSource).toContain('<XCircle v-else-if="record.manualError" class="h-5 w-5" />')
    expect(dialogSource).toContain('<CheckCircle2 v-else class="h-5 w-5" />')
    expect(dialogSource).toContain('updateQuestionAnswerStatsForMark(record, nextValue, 1)')
    expect(dialogSource).toContain('updateQuestionAnswerStatsForMark(record, nextValue, -1)')
  })

  it('adds the existing-style settings entry and enforces question limits in the editor', () => {
    expect(settingsSource).toContain("activeTab === 'questions'")
    expect(settingsSource).toContain('<TestQuestionsPanel />')
    expect(questionsSource).toContain('nameLength.value <= 100')
    expect(questionsSource).toContain('bodyLength.value <= 4000')
    expect(questionsSource).toContain('maxlength="100"')
    expect(questionsSource).toContain('maxlength="4000"')
    expect(questionsSource).toContain('showValidationError')
    expect(questionsSource).toContain('createTestQuestion')
    expect(questionsSource).toContain('updateTestQuestion')
    expect(questionsSource).toContain('setTestQuestionEnabled')
    expect(questionsSource).toContain('setDefaultTestQuestion')
    expect(questionsSource).toContain('deleteTestQuestion')
  })
})

import { readFileSync } from 'node:fs'

import { afterEach, describe, expect, it, vi } from 'vitest'
import * as connectionHealthApi from '../src/modules/admin/api/connectionHealth'
import {
  cancelQuestionAnswerBatch,
  getLatestQuestionAnswerBatch,
  getQuestionAnswerBatch,
  getQuestionAnswerHistory,
  startQuestionAnswerBatch,
} from '../src/modules/admin/api/connectionHealth'
import type { QuestionAnswerRecord } from '../src/modules/admin/types/connectionHealth'
import {
  TEST_QUESTION_KEYWORD_BYTES_LIMIT,
  TEST_QUESTION_KEYWORD_COUNT_LIMIT,
  TEST_QUESTION_KEYWORD_RUNE_LIMIT,
  filterQuestionAnswerRecords,
  groupQuestionAnswerHistoryByBatch,
  highlightQuestionAnswer,
  isCurrentQuestionAnswerOperation,
  parseTestQuestionKeywords,
  questionAnswerBatchCompletedAt,
  questionAnswerElapsedMilliseconds,
  questionAnswerReviewStatsFromRecords,
  questionAnswerStatsReconcile,
  replaceQuestionAnswerRecord,
  shortQuestionAnswerBatchId,
  testQuestionKeywordBytes,
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
  questionKeywordSnapshot: null,
  reasoningEffort: null,
  answerBody: `Answer ${id}`,
  status,
  errorType: '',
  answerJudgment: status === 'succeeded' ? 'unreviewed' : null,
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
  it('parses, deduplicates and highlights only the first three literal matches with longer overlap first', () => {
    expect(parseTestQuestionKeywords(' 错误,Error\nerror\r\n错误码 ')).toEqual([
      '错误', 'Error', '错误码',
    ])

    const answer = '错误码 ERROR [done] <script>alert(1)</script>'
    const segments = highlightQuestionAnswer(
      answer,
      ['错误', '错误码', 'error', '[done]', '<script>'],
    )
    expect(segments.filter(segment => segment.highlighted).map(segment => segment.text)).toEqual([
      '错误码', 'ERROR', '[done]',
    ])
    expect(segments.map(segment => segment.text).join('')).toBe(answer)
    expect(segments.filter(segment => !segment.highlighted).map(segment => segment.text).join('')).toContain(
      '<script>alert(1)</script>',
    )
    expect(highlightQuestionAnswer('abcde', ['ab', 'bcde'])
      .filter(segment => segment.highlighted).map(segment => segment.text)).toEqual(['bcde'])
    expect(highlightQuestionAnswer('ERROR错误码', ['error', '错误码'])).toEqual([
      { text: 'ERROR错误码', highlighted: true },
    ])
    expect(highlightQuestionAnswer('plain answer', [])).toEqual([
      { text: 'plain answer', highlighted: false },
    ])
    expect(highlightQuestionAnswer('plain answer', ['missing'])).toEqual([
      { text: 'plain answer', highlighted: false },
    ])
    expect(highlightQuestionAnswer('', ['answer'])).toEqual([])
  })

  it('uses the same 20, 64-code-point and 2048-byte keyword boundaries as the server', () => {
    expect(TEST_QUESTION_KEYWORD_COUNT_LIMIT).toBe(20)
    expect(TEST_QUESTION_KEYWORD_RUNE_LIMIT).toBe(64)
    expect(TEST_QUESTION_KEYWORD_BYTES_LIMIT).toBe(2048)

    const twenty = Array.from({ length: TEST_QUESTION_KEYWORD_COUNT_LIMIT }, (_, index) => `keyword-${index}`)
    expect(parseTestQuestionKeywords(twenty.join(','))).toEqual(twenty)
    const sixtyFourRunes = '界'.repeat(TEST_QUESTION_KEYWORD_RUNE_LIMIT)
    expect(Array.from(sixtyFourRunes)).toHaveLength(64)

    const exactBytes = ['😀', '😁', '😂', '😃', '😄', '😅', '😆', '😉']
      .map(value => value.repeat(TEST_QUESTION_KEYWORD_RUNE_LIMIT))
    expect(testQuestionKeywordBytes(exactBytes)).toBe(TEST_QUESTION_KEYWORD_BYTES_LIMIT)
    expect(testQuestionKeywordBytes([...exactBytes, 'x'])).toBe(TEST_QUESTION_KEYWORD_BYTES_LIMIT + 1)
  })

  it('replaces only the authoritative record with a deep-copied snapshot and recalculates review stats', () => {
    const records: QuestionAnswerRecord[] = [
      { ...questionAnswerRecord('one', 'succeeded', '2026-08-15T00:00:02Z'), questionKeywordSnapshot: ['old'] },
      { ...questionAnswerRecord('two', 'succeeded', '2026-08-15T00:00:03Z'), answerJudgment: 'correct' },
      { ...questionAnswerRecord('three', 'succeeded', '2026-08-15T00:00:04Z'), answerJudgment: 'incorrect' },
      { ...questionAnswerRecord('four', 'succeeded', '2026-08-15T00:00:05Z'), answerJudgment: null },
      questionAnswerRecord('failed', 'failed', '2026-08-15T00:00:06Z'),
    ]
    const authoritative: QuestionAnswerRecord = {
      ...records[0],
      answerJudgment: 'correct',
      questionKeywordSnapshot: ['new'],
    }

    const replaced = replaceQuestionAnswerRecord(records, authoritative)
    expect(replaced.map(record => record.id)).toEqual(records.map(record => record.id))
    expect(replaced[0]).toEqual(authoritative)
    expect(replaced[1]).toBe(records[1])
    replaced[0].questionKeywordSnapshot![0] = 'mutated copy'
    expect(authoritative.questionKeywordSnapshot).toEqual(['new'])
    expect(replaceQuestionAnswerRecord(records, { ...authoritative, id: 'missing' })).toEqual(records)

    expect(questionAnswerReviewStatsFromRecords(records)).toEqual({
      unreviewed: 2,
      correct: 1,
      incorrect: 1,
    })
  })

  it('normalizes missing keyword fields on every old-backend response without inventing snapshots', async () => {
    stubStorage()
    const legacyQuestion = {
      id: 'question-old', name: 'Old question', body: 'Old body', enabled: true, isDefault: true,
      createdAt: '2026-08-15T00:00:00Z', updatedAt: '2026-08-15T00:00:00Z',
    }
    const currentRecord = questionAnswerRecord('old', 'succeeded', '2026-08-15T00:00:02Z')
    const { questionKeywordSnapshot: _snapshot, ...legacyRecord } = currentRecord
    const legacyBatch = {
      batchId: 'batch-old', records: [legacyRecord], reasoningEffort: 'medium', submittedCount: 1,
      completedCount: 1, runningCount: 0, active: false, currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 1, inProgress: 0, succeeded: 1, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
      },
    }
    const legacyHistory = {
      records: [legacyRecord], page: 1, pageSize: 20, totalItems: 1, totalPages: 1,
      stats: legacyBatch.stats, todayStats: legacyBatch.stats,
    }
    const responses = [
      [legacyQuestion], legacyQuestion, legacyQuestion, legacyQuestion, legacyQuestion,
      legacyBatch, legacyBatch, legacyBatch, legacyBatch, legacyHistory, legacyRecord,
    ]
    const fetchMock = vi.fn()
    for (const payload of responses) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(payload), { status: 200 }))
    }
    vi.stubGlobal('fetch', fetchMock)

    await expect(connectionHealthApi.listTestQuestions()).resolves.toMatchObject([{ keywords: [] }])
    await expect(connectionHealthApi.createTestQuestion({ name: 'Old question', body: 'Old body' })).resolves.toMatchObject({ keywords: [] })
    await expect(connectionHealthApi.updateTestQuestion('question-old', { name: 'Old question', body: 'Old body' })).resolves.toMatchObject({ keywords: [] })
    await expect(connectionHealthApi.setTestQuestionEnabled('question-old', true)).resolves.toMatchObject({ keywords: [] })
    await expect(connectionHealthApi.setDefaultTestQuestion('question-old')).resolves.toMatchObject({ keywords: [] })

    const start = await connectionHealthApi.startQuestionAnswerBatch('target-old', ['model'], ['question-old'], 'medium')
    const latest = await connectionHealthApi.getLatestQuestionAnswerBatch('target-old')
    const batch = await connectionHealthApi.getQuestionAnswerBatch('target-old', 'batch-old')
    const cancelled = await connectionHealthApi.cancelQuestionAnswerBatch('target-old', 'batch-old')
    const history = await connectionHealthApi.getQuestionAnswerHistory('target-old', 1)
    const judgment = await connectionHealthApi.setQuestionAnswerJudgment('target-old', 'old', 'correct')
    for (const normalized of [start, latest, batch, cancelled]) {
      expect(normalized.records[0].questionKeywordSnapshot).toBeNull()
    }
    expect(history.records[0].questionKeywordSnapshot).toBeNull()
    expect(judgment.questionKeywordSnapshot).toBeNull()
  })

  it('groups the current history page by first batch appearance', () => {
    const records = [
      { ...questionAnswerRecord('a1', 'succeeded', '2026-08-30T01:00:01Z'), batchId: 'batch-aaaaaaaa' },
      { ...questionAnswerRecord('a2', 'succeeded', '2026-08-30T01:00:02Z'), batchId: 'batch-aaaaaaaa' },
      { ...questionAnswerRecord('b1', 'failed', '2026-08-30T01:00:03Z'), batchId: 'batch-bbbbbbbb' },
    ]

    expect(groupQuestionAnswerHistoryByBatch(records).map(group => ({
      batchId: group.batchId,
      ids: group.records.map(record => record.id),
    }))).toEqual([
      { batchId: 'batch-aaaaaaaa', ids: ['a1', 'a2'] },
      { batchId: 'batch-bbbbbbbb', ids: ['b1'] },
    ])
  })

  it('shortens only long batch IDs for display', () => {
    expect(shortQuestionAnswerBatchId('1234567890')).toBe('12345678')
    expect(shortQuestionAnswerBatchId('short')).toBe('short')
  })

  it('derives completion only from a complete terminal batch', () => {
    const terminal = {
      batchId: 'batch-terminal',
      records: [
        questionAnswerRecord('first', 'succeeded', '2026-08-30T01:00:01Z'),
        questionAnswerRecord('last', 'failed', '2026-08-30T01:00:09Z'),
      ],
      reasoningEffort: null,
      submittedCount: 2,
      completedCount: 2,
      runningCount: 0,
      active: false,
      currentModel: '',
      currentQuestion: '',
      stats: {
        requests: { submitted: 2, inProgress: 0, succeeded: 1, failed: 1, cancelled: 0 },
        reviews: { unreviewed: 1, correct: 0, incorrect: 0 },
      },
    }

    expect(questionAnswerBatchCompletedAt(terminal)).toBe('2026-08-30T01:00:09Z')
    expect(questionAnswerBatchCompletedAt({ ...terminal, active: true })).toBeNull()
    expect(questionAnswerBatchCompletedAt({ ...terminal, records: [] })).toBeNull()
    expect(questionAnswerBatchCompletedAt({
      ...terminal,
      records: [{ ...terminal.records[0], completedAt: null }],
    })).toBeNull()
    expect(questionAnswerBatchCompletedAt({
      ...terminal,
      records: [{ ...terminal.records[0], completedAt: 'not-a-time' }],
    })).toBeNull()
    expect(questionAnswerBatchCompletedAt({
      ...terminal,
      records: [{ ...terminal.records[0], completedAt: '0' }],
    })).toBeNull()
    expect(questionAnswerBatchCompletedAt({
      ...terminal,
      records: [{ ...terminal.records[0], completedAt: '2026-02-30T01:00:00Z' }],
    })).toBeNull()
    expect(questionAnswerBatchCompletedAt({
      ...terminal,
      records: [{ ...terminal.records[0], completedAt: '2024-02-29T01:00:00Z' }],
    })).toBe('2024-02-29T01:00:00Z')
  })

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

  it('calculates elapsed time for completed and running answers', () => {
    const completed = {
      ...questionAnswerRecord('timed', 'succeeded', '2026-08-14T16:00:03.250Z'),
      startedAt: '2026-08-14T16:00:01.000Z',
    }
    const running = {
      ...questionAnswerRecord('running', 'running', null),
      startedAt: '2026-08-14T16:00:01.000Z',
    }
    expect(questionAnswerElapsedMilliseconds(completed)).toBe(2250)
    expect(questionAnswerElapsedMilliseconds(running, Date.parse('2026-08-14T16:00:04.000Z'))).toBe(3000)
  })

  it('filters only succeeded unreviewed answers by default and reconciles both stats equations', () => {
    const unreviewed = questionAnswerRecord('unreviewed', 'succeeded', '2026-08-15T00:00:02.000Z')
    const correct = { ...questionAnswerRecord('correct', 'succeeded', '2026-08-15T00:00:02.000Z'), answerJudgment: 'correct' as const }
    const incorrect = { ...questionAnswerRecord('incorrect', 'succeeded', '2026-08-15T00:00:02.000Z'), answerJudgment: 'incorrect' as const, manualError: true }
    const failed = questionAnswerRecord('failed', 'failed', '2026-08-15T00:00:02.000Z')
    const cancelled = questionAnswerRecord('cancelled', 'cancelled', '2026-08-15T00:00:02.000Z')
    const all = [unreviewed, correct, incorrect, failed, cancelled]

    expect(filterQuestionAnswerRecords(all, false).map(record => record.id)).toEqual(['unreviewed'])
    expect(filterQuestionAnswerRecords(all, true)).toEqual(all)
    expect(questionAnswerStatsReconcile({
      requests: { submitted: 7, inProgress: 2, succeeded: 3, failed: 1, cancelled: 1 },
      reviews: { unreviewed: 1, correct: 1, incorrect: 1 },
    })).toBe(true)
    expect(questionAnswerStatsReconcile({
      requests: { submitted: 7, inProgress: 2, succeeded: 3, failed: 1, cancelled: 0 },
      reviews: { unreviewed: 1, correct: 1, incorrect: 1 },
    })).toBe(false)
    expect(questionAnswerStatsReconcile({
      requests: { submitted: 7, inProgress: 2, succeeded: 3, failed: 1, cancelled: 1 },
      reviews: { unreviewed: 1, correct: 1, incorrect: 0 },
    })).toBe(false)
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

  it('submits model and question selections and exposes explicit cancellation', async () => {
    stubStorage()
    const batch = {
      batchId: 'batch-1', records: [], submittedCount: 4, completedCount: 0,
      reasoningEffort: 'high', active: true, currentModel: 'model-a', currentQuestion: 'question-a',
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
      'high',
      controller.signal,
    )).resolves.toEqual(batch)
    await cancelQuestionAnswerBatch('sub2api:ws1:account-1', 'batch-1')

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/batches',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ models: ['model-a', 'model-b'], questionIds: ['question-a', 'question-b'], reasoningEffort: 'high' }),
        headers: expect.objectContaining({ 'X-TransitHub-Question-Answer-Contract': '2' }),
        signal: controller.signal,
      }),
    )
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/batches/batch-1/cancel',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({ 'X-TransitHub-Question-Answer-Contract': '2' }),
      }),
    )
  })

  it('exposes the four reasoning-effort options, default, locking, restoration, and legacy label', () => {
    expect(dialogSource).toContain("const qaReasoningEffort = ref<QuestionAnswerReasoningEffort>('medium')")
    expect(dialogSource).toContain("{ value: 'low', labelKey: 'low' }")
    expect(dialogSource).toContain("{ value: 'medium', labelKey: 'medium' }")
    expect(dialogSource).toContain("{ value: 'high', labelKey: 'high' }")
    expect(dialogSource).toContain("{ value: 'xhigh', labelKey: 'xhigh' }")
    expect(dialogSource).toContain('if (batch.reasoningEffort)')
    expect(dialogSource).toContain(':disabled="qaSelectionLocked"')
    expect(dialogSource).toContain('questionAnswerReasoningEffortLabel(record.reasoningEffort)')
    expect(dialogSource).toContain('questionAnswerReasoningEffortLabel(qaReviewBatch.reasoningEffort)')
    expect(dialogSource).toContain('reasoningEffort.unspecified')
  })

  it('sends contract 2 on every question-answer read', async () => {
    stubStorage()
    const batch = {
      batchId: 'batch-1', records: [], reasoningEffort: 'medium', submittedCount: 0,
      completedCount: 0, runningCount: 0, active: false, currentModel: '', currentQuestion: '',
      stats: {
        requests: { submitted: 0, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
      },
    }
    const history = {
      records: [], page: 2, pageSize: 20, totalItems: 21, totalPages: 2,
      stats: {
        requests: { submitted: 20, inProgress: 0, succeeded: 18, failed: 1, cancelled: 1 },
        reviews: { unreviewed: 2, correct: 15, incorrect: 1 },
      },
      todayStats: {
        requests: { submitted: 4, inProgress: 1, succeeded: 2, failed: 1, cancelled: 0 },
        reviews: { unreviewed: 1, correct: 1, incorrect: 0 },
      },
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(batch), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(batch), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(history), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getLatestQuestionAnswerBatch('sub2api:ws1:account-1')).resolves.toEqual(batch)
    await expect(getQuestionAnswerBatch('sub2api:ws1:account-1', 'batch-1')).resolves.toEqual(batch)
    await expect(getQuestionAnswerHistory('sub2api:ws1:account-1', 2)).resolves.toEqual(history)

    for (const call of fetchMock.mock.calls) {
      expect(call[1]).toEqual(expect.objectContaining({
        headers: expect.objectContaining({ 'X-TransitHub-Question-Answer-Contract': '2' }),
      }))
    }
  })

  it('uses the record-scoped three-state judgment endpoint without the old writer', async () => {
    stubStorage()
    const api = connectionHealthApi as unknown as {
      setQuestionAnswerJudgment?: (
        targetId: string,
        recordId: string,
        judgment: 'unreviewed' | 'correct' | 'incorrect',
        signal?: AbortSignal,
      ) => Promise<unknown>
    }
    expect(typeof api.setQuestionAnswerJudgment).toBe('function')
    if (!api.setQuestionAnswerJudgment) return

    const record = { id: 'record-9', answerJudgment: 'incorrect', manualError: true }
    const normalizedRecord = { ...record, questionKeywordSnapshot: null }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(record), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await expect(api.setQuestionAnswerJudgment(
      'sub2api:ws1:account-1',
      'record-9',
      'incorrect',
      controller.signal,
    )).resolves.toEqual(normalizedRecord)
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/connection-health/targets/sub2api%3Aws1%3Aaccount-1/question-answers/records/record-9/judgment',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ judgment: 'incorrect' }),
        headers: expect.objectContaining({ 'X-TransitHub-Question-Answer-Contract': '2' }),
        signal: controller.signal,
      }),
    )
  })

  it('uses the fixed near-viewport dialog frame and polls without cancelling background work on close', () => {
    const closeBody = dialogSource.match(/const close = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const cleanupBody = dialogSource.match(/const cleanupFrontendWork = \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const startBody = dialogSource.match(/const startQuestionAnswers = async \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const stopBody = dialogSource.match(/const stopQuestionAnswers = async \(\) => \{([\s\S]*?)\n\}/)?.[1] ?? ''
    const modelIndex = dialogSource.indexOf('v-for="model in models"')
    const questionIndex = dialogSource.indexOf('questionAnswer.questionsTitle')
    const currentIndex = dialogSource.indexOf('questionAnswer.currentTitle')
    const historyIndex = dialogSource.indexOf('questionAnswer.historyTitle')

    expect(dialogSource).toContain("type ProbeMode = 'once' | 'formal' | 'questionAnswer'")
    expect(dialogSource).toContain('h-[calc(100dvh-1rem)] w-[calc(100vw-1rem)] max-w-none')
    expect(dialogSource).not.toContain('h-[min(760px,calc(100dvh-2rem))] w-full max-w-6xl')
    expect(dialogSource).toContain('question => question.isDefault')
    expect(dialogSource).toContain('batch.records.map(record => record.modelName)')
    expect(dialogSource).toContain('batch.records.map(record => record.questionId)')
    expect(dialogSource).toContain('selected.value.size * qaSelectedQuestions.value.size')
    expect(dialogSource).toContain('setTimeout(() => void pollQuestionAnswerBatch(), 2000)')
    expect(dialogSource).toContain('const batchId = qaRuntimeBatch.value.batchId')
    expect(dialogSource).toContain('getQuestionAnswerBatch(targetId, record.batchId, controller.signal)')
    expect(dialogSource).toContain('reviewQuestionAnswerBatch(group.batchId)')
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
    expect(dialogSource).toContain('qaHistory.todayStats.requests.submitted')
    expect(dialogSource).toContain('questionAnswer.stats.todayShanghai')
    expect(startBody.indexOf('scheduleQuestionAnswerPoll()')).toBeLessThan(startBody.indexOf('getQuestionAnswerHistory(targetId, 1, controller.signal)'))
    expect(stopBody).toContain('cancelQuestionAnswerBatch(targetId, batchId, controller.signal)')
    expect(stopBody).toContain('questionAnswerScopeIsCurrent(scope)')
    expect(modelIndex).toBeGreaterThan(-1)
    expect(modelIndex).toBeLessThan(questionIndex)
    expect(questionIndex).toBeLessThan(currentIndex)
    expect(currentIndex).toBeLessThan(historyIndex)
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

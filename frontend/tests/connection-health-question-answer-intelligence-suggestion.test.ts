import { describe, expect, it } from 'vitest'

import type {
  QuestionAnswerBatch,
  QuestionAnswerRecord,
} from '@/modules/admin/types/connectionHealth'
import { questionAnswerIntelligenceSuggestion } from '@/modules/admin/utils/questionAnswers'

const makeRecord = (overrides: Partial<QuestionAnswerRecord> = {}): QuestionAnswerRecord => ({
  id: 'record-1',
  targetId: 'sub2api:ws1:acc-1',
  batchId: 'batch-1',
  modelName: 'model-a',
  questionId: 'question-1',
  questionName: 'Question 1',
  questionBody: 'Question body',
  questionKeywordSnapshot: null,
  reasoningEffort: 'medium',
  answerBody: 'answer',
  status: 'succeeded',
  errorType: '',
  answerJudgment: 'correct',
  manualError: false,
  createdAt: '2026-09-01T01:00:00Z',
  startedAt: '2026-09-01T01:00:01Z',
  completedAt: '2026-09-01T01:00:02Z',
  updatedAt: '2026-09-01T01:00:02Z',
  ...overrides,
})

const makeBatch = (
  records: QuestionAnswerRecord[],
  overrides: Partial<QuestionAnswerBatch> = {},
): QuestionAnswerBatch => ({
  batchId: 'batch-1',
  records,
  reasoningEffort: 'medium',
  repeatCount: 1,
  submittedCount: records.length,
  completedCount: records.length,
  runningCount: 0,
  active: false,
  currentModel: '',
  currentQuestion: '',
  stats: {
    requests: {
      submitted: records.length,
      inProgress: 0,
      succeeded: records.filter(record => record.status === 'succeeded').length,
      failed: records.filter(record => record.status === 'failed').length,
      cancelled: records.filter(record => record.status === 'cancelled').length,
    },
    reviews: { unreviewed: 0, correct: 0, incorrect: 0 },
    byModel: [],
  },
  ...overrides,
})

describe('questionAnswerIntelligenceSuggestion', () => {
  it('blocks active, pending, running, unreviewed, and unknown-status batches', () => {
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord(),
    ], { active: true }))).toEqual({ status: 'blocked' })
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ status: 'pending', answerJudgment: null }),
    ]))).toEqual({ status: 'blocked' })
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ status: 'running', answerJudgment: null }),
    ]))).toEqual({ status: 'blocked' })
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ answerJudgment: 'unreviewed' }),
    ]))).toEqual({ status: 'blocked' })
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ status: 'unknown' as unknown as QuestionAnswerRecord['status'] }),
    ]))).toEqual({ status: 'blocked' })
  })

  it('counts every reviewed success equally and excludes failed and cancelled records', () => {
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ id: 'correct-1', modelName: 'model-a', questionId: 'q1', answerJudgment: 'correct' }),
      makeRecord({ id: 'correct-2', modelName: 'model-a', questionId: 'q1', answerJudgment: 'correct' }),
      makeRecord({ id: 'incorrect-1', modelName: 'model-b', questionId: 'q2', answerJudgment: 'incorrect' }),
      makeRecord({ id: 'failed', status: 'failed', answerJudgment: null, errorType: 'network' }),
      makeRecord({ id: 'cancelled', status: 'cancelled', answerJudgment: null }),
    ], { repeatCount: 2 }))).toEqual({ status: 'ready', value: 67 })
  })

  it('returns none for terminal batches without successful answers, never zero', () => {
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ id: 'failed', status: 'failed', answerJudgment: null }),
      makeRecord({ id: 'cancelled', status: 'cancelled', answerJudgment: null }),
    ]))).toEqual({ status: 'none' })
  })

  it('distinguishes a real zero and ignores conflicting stats and model summaries', () => {
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ answerJudgment: 'incorrect' }),
    ]))).toEqual({ status: 'ready', value: 0 })
    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ id: 'correct', answerJudgment: 'correct' }),
      makeRecord({ id: 'incorrect-1', answerJudgment: 'incorrect' }),
      makeRecord({ id: 'incorrect-2', answerJudgment: 'incorrect' }),
    ]))).toEqual({ status: 'ready', value: 33 })

    expect(questionAnswerIntelligenceSuggestion(makeBatch([
      makeRecord({ id: 'only-correct', answerJudgment: 'correct' }),
      makeRecord({ id: 'only-incorrect', answerJudgment: 'incorrect' }),
    ], {
      stats: {
        requests: { submitted: 99, inProgress: 0, succeeded: 99, failed: 0, cancelled: 0 },
        reviews: { unreviewed: 0, correct: 99, incorrect: 0 },
        byModel: [{
          modelName: 'misleading-model',
          requests: { submitted: 99, inProgress: 0, succeeded: 99, failed: 0, cancelled: 0 },
          reviews: { unreviewed: 0, correct: 99, incorrect: 0 },
        }],
      },
    }))).toEqual({ status: 'ready', value: 50 })
  })
})

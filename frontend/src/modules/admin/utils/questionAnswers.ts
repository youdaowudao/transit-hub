import type { QuestionAnswerRecord, QuestionAnswerStats } from '../types/connectionHealth'

export interface QuestionAnswerOperationScope {
  sequence: number
  targetId: string
  batchId?: string
}

export interface QuestionAnswerDialogSnapshot {
  sequence: number
  open: boolean
  mode: string
  targetId: string | null
  batchId?: string | null
}

export const isCurrentQuestionAnswerOperation = (
  scope: QuestionAnswerOperationScope,
  snapshot: QuestionAnswerDialogSnapshot,
): boolean => (
  scope.sequence === snapshot.sequence
  && snapshot.open
  && snapshot.mode === 'questionAnswer'
  && scope.targetId === snapshot.targetId
  && (scope.batchId === undefined || scope.batchId === snapshot.batchId)
)

export const questionAnswerElapsedMilliseconds = (
  record: QuestionAnswerRecord,
  nowMs = Date.now(),
): number | null => {
  if (!record.startedAt) return null
  const startedAt = Date.parse(record.startedAt)
  const completedAt = record.completedAt ? Date.parse(record.completedAt) : nowMs
  if (!Number.isFinite(startedAt) || !Number.isFinite(completedAt)) return null
  return Math.max(0, completedAt - startedAt)
}

export const filterQuestionAnswerRecords = (
  records: QuestionAnswerRecord[],
  showAll: boolean,
): QuestionAnswerRecord[] => showAll
  ? records
  : records.filter(record => record.status === 'succeeded' && record.answerJudgment === 'unreviewed')

export const questionAnswerStatsReconcile = (stats: QuestionAnswerStats): boolean => (
  stats.requests.submitted === stats.requests.inProgress
    + stats.requests.succeeded
    + stats.requests.failed
    + stats.requests.cancelled
  && stats.requests.succeeded === stats.reviews.unreviewed
    + stats.reviews.correct
    + stats.reviews.incorrect
)

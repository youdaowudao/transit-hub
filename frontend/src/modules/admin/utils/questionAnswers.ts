import type { QuestionAnswerBatch, QuestionAnswerRecord, QuestionAnswerStats } from '../types/connectionHealth'

export interface QuestionAnswerHistoryBatchGroup {
  batchId: string
  records: QuestionAnswerRecord[]
}

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

export const groupQuestionAnswerHistoryByBatch = (
  records: QuestionAnswerRecord[],
): QuestionAnswerHistoryBatchGroup[] => {
  const groups = new Map<string, QuestionAnswerHistoryBatchGroup>()
  for (const record of records) {
    const existing = groups.get(record.batchId)
    if (existing) existing.records.push(record)
    else groups.set(record.batchId, { batchId: record.batchId, records: [record] })
  }
  return Array.from(groups.values())
}

export const shortQuestionAnswerBatchId = (batchId: string): string => (
  batchId.length <= 8 ? batchId : batchId.slice(0, 8)
)

const parseQuestionAnswerCompletedAt = (value: string): number | null => {
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/)
  if (!match) return null
  const [, yearText, monthText, dayText, hourText, minuteText, secondText] = match
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const hour = Number(hourText)
  const minute = Number(minuteText)
  const second = Number(secondText)
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (
    month < 1
    || month > 12
    || day < 1
    || day > daysInMonth[month - 1]
    || hour > 23
    || minute > 59
    || second > 59
  ) return null
  const milliseconds = Date.parse(value)
  return Number.isFinite(milliseconds) ? milliseconds : null
}

export const questionAnswerBatchCompletedAt = (batch: QuestionAnswerBatch): string | null => {
  if (batch.active || batch.records.length === 0) return null
  let latest: { value: string; milliseconds: number } | null = null
  for (const record of batch.records) {
    if (!record.completedAt) return null
    const milliseconds = parseQuestionAnswerCompletedAt(record.completedAt)
    if (milliseconds === null) return null
    if (!latest || milliseconds > latest.milliseconds) latest = { value: record.completedAt, milliseconds }
  }
  return latest?.value ?? null
}

import type { QuestionAnswerRecord } from '../types/connectionHealth'

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

const shanghaiDateKey = (value: string | Date): string | null => {
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.getTime())) return null
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(date)
  const values = Object.fromEntries(parts.map(part => [part.type, part.value]))
  return `${values.year}-${values.month}-${values.day}`
}

export const questionAnswerCompletedTodayInShanghai = (
  record: QuestionAnswerRecord,
  now = new Date(),
): boolean => Boolean(record.completedAt && shanghaiDateKey(record.completedAt) === shanghaiDateKey(now))

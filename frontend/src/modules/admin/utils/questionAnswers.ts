import type {
  QuestionAnswerBatch,
  QuestionAnswerRecord,
  QuestionAnswerReviewStats,
  QuestionAnswerStats,
  QuestionAnswerSubmissionSummary,
} from '../types/connectionHealth'

export const TEST_QUESTION_KEYWORD_COUNT_LIMIT = 20
export const TEST_QUESTION_KEYWORD_RUNE_LIMIT = 64
export const TEST_QUESTION_KEYWORD_BYTES_LIMIT = 2048

export const questionAnswerSubmissionSummary = (
  modelCount: number,
  questionCount: number,
  repeatCount: number,
): QuestionAnswerSubmissionSummary => {
  const validRepeatCount = Number.isInteger(repeatCount) && repeatCount >= 1 && repeatCount <= 10
  const total = modelCount * questionCount * repeatCount
  return {
    modelCount,
    questionCount,
    repeatCount,
    total,
    validRepeatCount,
    withinBatchLimit: validRepeatCount && total <= 50,
  }
}

export interface QuestionAnswerHighlightSegment {
  text: string
  highlighted: boolean
}

const asciiFoldQuestionAnswerText = (value: string): string => {
  let folded = ''
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    folded += code >= 65 && code <= 90 ? String.fromCharCode(code + 32) : value[index]
  }
  return folded
}

export const parseTestQuestionKeywords = (value: string): string[] => {
  const keywords: string[] = []
  const seen = new Set<string>()
  for (const item of value.split(/[,\r\n]+/u)) {
    const keyword = item.trim()
    if (!keyword) continue
    const folded = asciiFoldQuestionAnswerText(keyword)
    if (seen.has(folded)) continue
    seen.add(folded)
    keywords.push(keyword)
  }
  return keywords
}

export const testQuestionKeywordBytes = (keywords: string[]): number => {
  const encoder = new TextEncoder()
  return keywords.reduce((total, keyword) => total + encoder.encode(keyword).byteLength, 0)
}

export const highlightQuestionAnswer = (
  answer: string,
  snapshot: string[],
): QuestionAnswerHighlightSegment[] => {
  if (!answer) return []

  const foldedAnswer = asciiFoldQuestionAnswerText(answer)
  const occupied = new Uint8Array(answer.length)
  const selected: Array<{ start: number; end: number }> = []
  const keywords = snapshot
    .map((keyword, index) => ({ keyword, index, codePointLength: Array.from(keyword).length }))
    .filter(({ keyword }) => keyword.length > 0)
    .sort((left, right) => right.codePointLength - left.codePointLength || left.index - right.index)

  for (const { keyword } of keywords) {
    const foldedKeyword = asciiFoldQuestionAnswerText(keyword)
    let searchFrom = 0
    while (searchFrom <= foldedAnswer.length - foldedKeyword.length) {
      const start = foldedAnswer.indexOf(foldedKeyword, searchFrom)
      if (start < 0) break
      const end = start + foldedKeyword.length
      let overlaps = false
      for (let index = start; index < end; index += 1) {
        if (occupied[index]) {
          overlaps = true
          break
        }
      }
      if (!overlaps) {
        occupied.fill(1, start, end)
        selected.push({ start, end })
      }
      searchFrom = start + 1
    }
  }

  if (selected.length === 0) return [{ text: answer, highlighted: false }]
  selected.sort((left, right) => left.start - right.start)
  const visibleIntervals = selected.slice(0, 3)
  const segments: QuestionAnswerHighlightSegment[] = []
  const appendSegment = (text: string, highlighted: boolean) => {
    if (!text) return
    const previous = segments[segments.length - 1]
    if (previous?.highlighted === highlighted) previous.text += text
    else segments.push({ text, highlighted })
  }
  let cursor = 0
  for (const interval of visibleIntervals) {
    appendSegment(answer.slice(cursor, interval.start), false)
    appendSegment(answer.slice(interval.start, interval.end), true)
    cursor = interval.end
  }
  appendSegment(answer.slice(cursor), false)
  return segments
}

export const replaceQuestionAnswerRecord = (
  records: QuestionAnswerRecord[],
  authoritative: QuestionAnswerRecord,
): QuestionAnswerRecord[] => {
  const result = [...records]
  const index = result.findIndex(record => record.id === authoritative.id)
  if (index < 0) return result
  result[index] = {
    ...authoritative,
    questionKeywordSnapshot: authoritative.questionKeywordSnapshot === null
      ? null
      : [...authoritative.questionKeywordSnapshot],
  }
  return result
}

export const questionAnswerReviewStatsFromRecords = (
  records: QuestionAnswerRecord[],
): QuestionAnswerReviewStats => {
  const stats: QuestionAnswerReviewStats = { unreviewed: 0, correct: 0, incorrect: 0 }
  for (const record of records) {
    if (record.status !== 'succeeded') continue
    if (record.answerJudgment === 'correct') stats.correct += 1
    else if (record.answerJudgment === 'incorrect') stats.incorrect += 1
    else stats.unreviewed += 1
  }
  return stats
}

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

import type {
  EffectiveTestConfiguration,
  AdminGroupHealth,
  ManualProbeModelOption,
  QuestionAnswerBatch,
  QuestionAnswerReasoningEffort,
  QuestionAnswerRecord,
  QuestionAnswerRuntimeItem,
  QuestionAnswerStatus,
  QuestionAnswerReviewStats,
  QuestionAnswerStats,
  QuestionAnswerQuestionStats,
  QuestionAnswerSubmissionSummary,
  TestQuestion,
} from '../types/connectionHealth'
import type { QuestionAnswerSelectionPreferences } from './connectionHealthPreferences'

export const questionAnswerReadableError = (value: string, translate: (key: string) => string, exists: (key: string) => boolean): string => {
  if (exists(value)) return translate(value)
  const typeKey = `admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes.${value}`
  if (exists(typeKey)) return translate(typeKey)
  if (value && !value.startsWith('admin.') && !value.startsWith('auth.')) return value
  return `${translate('admin.connectionHealth.errors.request')}；接口未提供更具体原因，请按批次编号和时间核查后台日志。`
}

export const questionAnswerOperationKey = (error: unknown): string => (
  error instanceof Error && 'errorKey' in error && typeof error.errorKey === 'string' ? error.errorKey
    : error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
)

export const questionAnswerSnapshotTime = (snapshotAt: string | undefined, receivedAt: number, now: number, stale = false): number => {
  const snapshot = snapshotAt ? Date.parse(snapshotAt) : NaN
  return Number.isFinite(snapshot) ? snapshot + (stale ? 0 : Math.max(0, now - receivedAt)) : now
}

export const questionAnswerSavingMessage = (runtime: QuestionAnswerRuntimeItem): string => {
  const diagnostics = runtime.diagnostics
  if (diagnostics?.failureStep === 'starting' && !diagnostics.requestStartedAt) {
    const cause = diagnostics.localError
    if (cause) return `记录这题开始时失败：${cause}；这条题目没有调用答题接口，后续发送已停止。${runtime.storageError && runtime.storageError !== cause ? `保存尚未确认：${runtime.storageError}。` : ''}暂时保留执行位置，请重试保存并结束；不会重新发题。`
    return `调用答题接口前已停止，这条题目没有调用答题接口；${runtime.storageError ? `保存停止结果尚未确认：${runtime.storageError}。请重试保存并结束；不会重新发题。` : '正在保存停止结果，批次结束尚未确认。'}`
  }
  const result = runtime.pendingResultStatus === 'succeeded' ? '已取得完整回答' : '请求已结束'
  return `${result}，${runtime.storageError ? `保存尚未确认：${runtime.storageError}。暂时保留执行位置，请重试保存并结束；不会重新发题。` : '正在保存结果。'}`
}

export const TEST_QUESTION_KEYWORD_COUNT_LIMIT = 20
export const TEST_QUESTION_KEYWORD_RUNE_LIMIT = 64
export const TEST_QUESTION_KEYWORD_BYTES_LIMIT = 2048

export const questionAnswerRequestProtocolLabel = (protocol: string | null | undefined, legacyLabel: string): string =>
  protocol === 'messages' ? 'Messages' : protocol === 'responses' ? 'Responses' : protocol === 'chat_completions' ? 'Chat Completions' : legacyLabel

export const questionAnswerErrorMessageKey = (errorType: string, te: (key: string) => boolean): string => {
  const prefix = 'admin.connectionHealth.manualProbeDialog.questionAnswer.errorTypes'
  const key = `${prefix}.${errorType}`
  return errorType && te(key) ? key : `${prefix}.unknown`
}

// Match Go strings.TrimSpace: normalize CRLF only, then remove Unicode
// White_Space at the two ends. Inner whitespace and punctuation remain exact.
export const questionAnswerDuplicateQuestionNames = (questions: Pick<TestQuestion, 'id' | 'name' | 'body'>[]): string[] => {
  const bodies = new Map<string, string[]>()
  for (const question of questions) {
    const body = question.body.replace(/\r\n/g, '\n').replace(/^[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g, '')
    bodies.set(body, [...(bodies.get(body) ?? []), question.name])
  }
  return [...bodies.values()].filter(names => names.length > 1).flat()
}

export interface QuestionAnswerResolvedSelection {
  modelIds: string[]
  questionIds: string[]
  reasoningEffort: QuestionAnswerReasoningEffort
  repeatCount: number
}

export interface QuestionAnswerBatchTarget {
  testConfiguration?: EffectiveTestConfiguration
  targetId: string
  accountName: string
  platform: string
  type: string
  status: string
  groupIds: string[]
  groupNames: string[]
}

const stableUniqueIds = (ids: string[]): string[] => Array.from(new Set(ids))

export const resolveQuestionAnswerSelection = (
  preferences: QuestionAnswerSelectionPreferences,
  models: ManualProbeModelOption[],
  questions: TestQuestion[],
): QuestionAnswerResolvedSelection => {
  const availableModelIds = new Set(models.map(model => model.id))
  const selectedModelIds = stableUniqueIds(preferences.modelIds)
    .filter(modelId => availableModelIds.has(modelId))
  const enabledQuestions = questions.filter(question => question.enabled)
  const enabledQuestionIds = new Set(enabledQuestions.map(question => question.id))
  const selectedQuestionIds = stableUniqueIds(preferences.questionIds)
    .filter(questionId => enabledQuestionIds.has(questionId))
  const defaultQuestionId = enabledQuestions.find(question => question.isDefault)?.id

  return {
    modelIds: selectedModelIds.length > 0
      ? selectedModelIds
      : models.length > 0
        ? [models.find(model => model.id === 'gpt-5.6-sol')?.id ?? models[0].id]
        : [],
    questionIds: selectedQuestionIds.length > 0
      ? selectedQuestionIds
      : defaultQuestionId
        ? [defaultQuestionId]
        : [],
    reasoningEffort: preferences.reasoningEffort,
    repeatCount: preferences.repeatCount,
  }
}

export const collectQuestionAnswerBatchTargets = (
  groups: AdminGroupHealth[],
): QuestionAnswerBatchTarget[] => {
  const targets = new Map<string, QuestionAnswerBatchTarget>()
  for (const group of groups) {
    for (const account of group.accounts) {
      const targetId = account.targetId.trim()
      if (!targetId) continue
      const existing = targets.get(targetId)
      if (existing) {
        if (!existing.groupIds.includes(group.id)) existing.groupIds.push(group.id)
        if (!existing.groupNames.includes(group.name)) existing.groupNames.push(group.name)
        continue
      }
      targets.set(targetId, {
        targetId,
        testConfiguration: account.testConfiguration,
        accountName: account.name,
        platform: account.platform,
        type: account.type,
        status: account.status,
        groupIds: [group.id],
        groupNames: [group.name],
      })
    }
  }
  return Array.from(targets.values())
}

export const reconcileQuestionAnswerBatchTargetIds = (
  storedIds: string[],
  targets: QuestionAnswerBatchTarget[],
): string[] => {
  const targetIds = new Set(targets.map(target => target.targetId))
  return stableUniqueIds(storedIds).filter(targetId => targetIds.has(targetId))
}

export const selectQuestionAnswerBatchTargets = (
  targets: QuestionAnswerBatchTarget[],
  selectedIds: string[],
): QuestionAnswerBatchTarget[] => {
  const selectedIdSet = new Set(selectedIds)
  return targets.filter(target => selectedIdSet.has(target.targetId))
}

export const compatibleQuestionAnswerModelIds = (
  selectedModelIds: string[],
  availableModels: ManualProbeModelOption[],
): { compatible: string[]; incompatible: string[] } => {
  const availableModelIds = new Set(availableModels.map(model => model.id))
  const selectedIds = stableUniqueIds(selectedModelIds)
  return {
    compatible: selectedIds.filter(modelId => availableModelIds.has(modelId)),
    incompatible: selectedIds.filter(modelId => !availableModelIds.has(modelId)),
  }
}

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

export const questionAnswerAccuracy = (stats: Pick<QuestionAnswerStats, 'reviews'>): number | null => {
  const judged = stats.reviews.correct + stats.reviews.incorrect
  if (judged <= 0) return null
  return Math.round((stats.reviews.correct / judged) * 1000) / 10
}

export const questionAnswerAccuracyRatio = (stats: Pick<QuestionAnswerStats, 'reviews'> | null | undefined): number | null => {
  const judged = stats ? stats.reviews.correct + stats.reviews.incorrect : 0
  return judged > 0 ? stats!.reviews.correct / judged : null
}

export const formatQuestionAnswerAccuracy = (accuracy: number | null): string => {
  if (accuracy === null) return '—'
  return `${Number.isInteger(accuracy) ? accuracy : accuracy.toFixed(1)}%`
}

export interface QuestionAnswerReviewRecordPartition {
  pendingReview: QuestionAnswerRecord[]
  reviewed: QuestionAnswerRecord[]
  failed: QuestionAnswerRecord[]
}

export const partitionQuestionAnswerReviewRecords = (
  records: QuestionAnswerRecord[],
): QuestionAnswerReviewRecordPartition => {
  const result: QuestionAnswerReviewRecordPartition = {
    pendingReview: [],
    reviewed: [],
    failed: [],
  }
  for (const record of records) {
    if (record.status === 'failed') result.failed.push(record)
    else if (record.status === 'succeeded' && (record.answerJudgment === 'correct' || record.answerJudgment === 'incorrect')) {
      result.reviewed.push(record)
    } else if (record.status === 'succeeded') result.pendingReview.push(record)
  }
  return result
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

export const normalizeQuestionAnswerKeywords = (keywords: string[] | null): string[] => {
  const encoder = new TextEncoder()
  return Array.from(new Set((keywords ?? []).map(keyword => asciiFoldQuestionAnswerText(keyword.trim())).filter(Boolean))).sort((a, b) => {
    const left = encoder.encode(a), right = encoder.encode(b)
    for (let i = 0; i < Math.min(left.length, right.length); i++) if (left[i] !== right[i]) return left[i]! - right[i]!
    return left.length - right.length
  })
}

export const questionAnswerRecordMatchesQuestion = (record: QuestionAnswerRecord, question: QuestionAnswerQuestionStats): boolean => (
  record.questionId === question.questionId && record.questionBody === question.questionBody
  && JSON.stringify(normalizeQuestionAnswerKeywords(record.questionKeywordSnapshot)) === JSON.stringify(question.normalizedKeywords)
)

export const questionAnswerSourceLabel = (record: QuestionAnswerRecord): string => (
  record.judgmentSource === 'automatic' ? '自动判定' : record.judgmentSource === 'manual' ? '人工判定' : '历史判定'
)

export interface QuestionAnswerResultGroup {
  key: string
  questionSnapshotKey: string
  questionName: string
  modelName: string
  records: QuestionAnswerRecord[]
  reviews: QuestionAnswerReviewStats
}

export const groupQuestionAnswerResults = (records: QuestionAnswerRecord[], questions: QuestionAnswerQuestionStats[]): QuestionAnswerResultGroup[] => {
  const groups = new Map<string, QuestionAnswerResultGroup>()
  for (const record of records) {
    const question = questions.find(item => questionAnswerRecordMatchesQuestion(record, item))
    const snapshotKey = question?.questionSnapshotKey ?? JSON.stringify([record.questionId, record.questionBody, normalizeQuestionAnswerKeywords(record.questionKeywordSnapshot)])
    const key = JSON.stringify([snapshotKey, record.modelName])
    let group = groups.get(key)
    if (!group) {
      group = { key, questionSnapshotKey: question?.questionSnapshotKey ?? '', questionName: question?.displayQuestionName ?? record.questionName, modelName: record.modelName, records: [], reviews: { unreviewed: 0, correct: 0, incorrect: 0 } }
      groups.set(key, group)
    }
    group.records.push(record)
  }
  for (const group of groups.values()) {
    group.records.sort((a, b) => (a.repeatIndex ?? 11) - (b.repeatIndex ?? 11) || a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
    group.reviews = questionAnswerReviewStatsFromRecords(group.records)
  }
  return Array.from(groups.values())
}

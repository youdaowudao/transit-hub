import { createHash } from 'node:crypto'
import type { QuestionAnswerHistory, QuestionAnswerRecord, QuestionAnswerStats, QuestionAnswerQuestionStats } from '@/modules/admin/types/connectionHealth'
import { normalizeQuestionAnswerKeywords, questionAnswerReviewStatsFromRecords } from '@/modules/admin/utils/questionAnswers'
const shallowStats = (records: QuestionAnswerRecord[]) => {
  const requests = { submitted: records.length, inProgress: 0, succeeded: 0, failed: 0, cancelled: 0 }
  for (const record of records) if (record.status === 'pending' || record.status === 'running') requests.inProgress++; else requests[record.status]++
  return { requests, reviews: questionAnswerReviewStatsFromRecords(records) }
}
export const questionAnswerFixtureStats = (records: QuestionAnswerRecord[]): QuestionAnswerStats => {
  const byModel = [...new Set(records.map(record => record.modelName))].sort().map(modelName => ({ modelName, ...shallowStats(records.filter(record => record.modelName === modelName)) }))
  const questions = new Map<string, QuestionAnswerQuestionStats>()
  for (const record of records) {
    const normalizedKeywords = normalizeQuestionAnswerKeywords(record.questionKeywordSnapshot)
    const key = createHash('sha256').update(JSON.stringify({ questionId: record.questionId, questionBody: record.questionBody, normalizedKeywords })).digest('hex')
    if (questions.has(key)) continue
    const selected = records.filter(item => item.questionId === record.questionId && item.questionBody === record.questionBody && JSON.stringify(normalizeQuestionAnswerKeywords(item.questionKeywordSnapshot)) === JSON.stringify(normalizedKeywords))
    questions.set(key, { questionSnapshotKey: key, questionId: record.questionId, questionBody: record.questionBody, displayQuestionName: record.questionName, normalizedKeywords, ...shallowStats(selected), byModel: [...new Set(selected.map(item => item.modelName))].sort().map(modelName => ({ modelName, ...shallowStats(selected.filter(item => item.modelName === modelName)) })) })
  }
  return { ...shallowStats(records), byModel, byQuestion: [...questions.values()] }
}
// Upgrades historic test builders only; production accepts the v3 batches contract directly.
export const upgradeQuestionAnswerHistoryFixture = (history: any): QuestionAnswerHistory => {
  if (history.batches) return history
  const records: QuestionAnswerRecord[] = history.records ?? []
  const batches = [...new Set(records.map(record => record.batchId))].map(batchId => {
    const selected = records.filter(record => record.batchId === batchId)
    const stats = questionAnswerFixtureStats(selected)
    const active = selected.some(record => record.status === 'pending' || record.status === 'running')
    return { batchId, createdAt: selected[0]!.createdAt, startedAt: selected[0]!.startedAt, completedAt: active ? null : selected.at(-1)!.completedAt, requestProtocol: selected[0]!.requestProtocol ?? null, reasoningEffort: selected[0]!.reasoningEffort, models: [...new Set(selected.map(record => record.modelName))], questions: stats.byQuestion, repeatCount: Math.max(...stats.byQuestion.flatMap(question => question.byModel.map(model => model.requests.submitted))), active, stats }
  })
  return { batches, page: history.page, pageSize: 20, totalBatches: history.totalItems > 0 ? Math.max(batches.length, history.totalPages > 1 ? (history.totalPages - 1) * 20 + batches.length : batches.length) : 0, totalPages: history.totalPages,
    todayStats: { ...history.todayStats, byModel: history.todayStats.byModel ?? [], byQuestion: history.todayStats.byQuestion ?? [] } }
}

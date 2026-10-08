import { t, te } from '@/locales'
import type { QuestionAnswerScheduleInput, QuestionAnswerScheduleExecutionStatus } from '../types/connectionHealth'

// randomUUID is restricted to secure contexts; the fixed HTTP Tailnet entry still
// provides cryptographic getRandomValues. Keep the same UUIDv4 wire contract.
export const questionAnswerScheduleRequestId = (): string => {
  if (typeof globalThis.crypto.randomUUID === 'function') return globalThis.crypto.randomUUID()
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export const questionAnswerScheduleTime = (value: string | null | undefined): string => {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Singapore', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(date) : '—'
}
export const questionAnswerScheduleReason = (reason: string | undefined): string => {
  if (!reason) return ''
  const key = `admin.connectionHealth.questionAnswerSchedule.reasons.${reason}`
  return te(key) ? t(key) : te(reason) ? t(reason) : '运行结果待处理'
}
export const questionAnswerScheduleStatus = (status: QuestionAnswerScheduleExecutionStatus): string => t(`admin.connectionHealth.questionAnswerSchedule.statuses.${status}`)
export const questionAnswerScheduleIsActive = (status: QuestionAnswerScheduleExecutionStatus): boolean => status === 'pending' || status === 'active'

const timeMinute = (value: string): number | null => {
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match || Number(match[1]) > 23 || Number(match[2]) > 59) return null
  return Number(match[1]) * 60 + Number(match[2])
}
export const validQuestionAnswerScheduleGrid = (config: Pick<QuestionAnswerScheduleInput, 'peakStart' | 'peakEnd' | 'peakIntervalMinutes' | 'offPeakIntervalMinutes'>): boolean => {
  const start = timeMinute(config.peakStart), end = timeMinute(config.peakEnd)
  return start !== null && end !== null && start !== end && [config.peakIntervalMinutes, config.offPeakIntervalMinutes].every(value => Number.isInteger(value) && value >= 30 && value <= 1440 && value % 30 === 0)
}

// Singapore has a fixed UTC+8 offset. Both grids are anchored to their window starts.
export const questionAnswerScheduleSlots = (config: Pick<QuestionAnswerScheduleInput, 'peakStart' | 'peakEnd' | 'peakIntervalMinutes' | 'offPeakIntervalMinutes'>, after = Date.now(), hours = 24): string[] => {
  if (!validQuestionAnswerScheduleGrid(config)) return []
  const minute = 60_000, day = 1440 * minute, offset = 480 * minute
  const start = timeMinute(config.peakStart)!, end = timeMinute(config.peakEnd)!
  const midnight = Math.floor((after + offset) / day) * day - offset
  const through = after + hours * 60 * minute
  const slots = new Set<number>()
  for (let date = midnight - day; date <= through + day; date += day) {
    const peakFrom = date + start * minute
    const peakThrough = date + (end <= start ? 1440 + end : end) * minute
    const nextPeak = peakFrom + day
    for (let slot = peakFrom; slot < peakThrough; slot += config.peakIntervalMinutes * minute) if (slot > after && slot <= through) slots.add(slot)
    for (let slot = peakThrough; slot < nextPeak; slot += config.offPeakIntervalMinutes * minute) if (slot > after && slot <= through) slots.add(slot)
  }
  return [...slots].sort((a, b) => a - b).map(slot => new Date(slot).toISOString())
}

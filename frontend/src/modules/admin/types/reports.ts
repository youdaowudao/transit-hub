import type { ActivityScope } from './userActivity'
export type DailyReportScope = ActivityScope
export interface DailyReportPatch { dailyReportEnabled?: boolean; dailyReportTime?: string; dailyReportBotIds?: string[] }
export interface DailyReportBot { id: string; name: string; enabled: boolean }
export interface DailyReportSection {
  title: string
  date: string
  observedAt: string | null
  coverage: { from: string; to: string; complete: boolean }
  text: string
}
export interface DailyReportScheduled {
  attemptId: string
  sendingDate: string
  businessDate: string
  at: string
  botId: string
  name: string
  status: 'sent' | 'failed' | 'unknown'
  reason: string
  attempted: boolean
}
export interface DailyReportPreview extends DailyReportScope {
  businessDate: string
  sections: DailyReportSection[]
  text: string
  observedAt: string
  configuration: { dailyReportEnabled: unknown; dailyReportTime: unknown; dailyReportBotIds: unknown }
  bots: DailyReportBot[]
  pauseReason: string
  scheduled: DailyReportScheduled | null
  scheduledWarning: string
}
export interface DailyReportTest extends DailyReportScope {
  attempted: number
  sent: number
  failed: number
  unknown: number
  results: { botId: string; name: string; status: 'sent' | 'failed' | 'unknown'; reason: string }[]
}

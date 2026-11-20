import type { ActivityScope } from './userActivity'
export type VipScope = ActivityScope
export interface VipSettings {
  dailyTop: number
  lifetimeTop: number
  alertDays: number
  alertBalance: number
  alertsEnabled: boolean
  botIds: string[]
  reminderText: string
  emptyReminderText: string
}
export interface VipRow {
  siteUserId: string
  email: string
  username: string
  role: string
  userStatus: string
  status: 'normal' | 'low' | 'empty' | 'declining' | 'disabled'
  source: { manual: boolean; note: string; dailyRank?: number; lifetimeRank?: number }
  reminder: string
  balance: number | null
  dailyAverage: number | null
  previousAverage: number | null
  daysLeft: number | null
  trend: number | null
  recordedConsumption: number | null
  recordedFrom: string
  observedAt: string | null
  lastUsedAt: string | null
  lastAlertStatus?: 'sent' | 'unknown'
  lastAlertAt?: string
  lastAlertState?: string
  lastAlertRecovered: boolean
}
export interface VipSummary extends VipScope {
  settings: VipSettings
  counts: { all: number; normal: number; low: number; empty: number; declining: number; disabled: number }
  items: VipRow[]
  lastSyncedAt: string | null
  coverage: { from: string; to: string; sevenDays: boolean; fourteenDays: boolean; warnings: string[] }
  dismissedCount: number
  bots: { id: string; name: string; enabled: boolean }[]
  pauseReason: string
}
export interface VipCandidate {
  siteUserId: string
  email: string
  username: string
  balance: number | null
  watched: boolean
  dismissed: boolean
}
export interface VipSearchResponse extends VipScope { items: VipCandidate[] }
export interface VipMutationResponse extends VipScope {
  saved: true
  restored: number
  summary: VipSummary | null
  refreshMessage?: string
}

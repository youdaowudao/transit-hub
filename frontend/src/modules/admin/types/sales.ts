import type { ActivityScope } from './userActivity'
export type SalesScope = ActivityScope
export type SalesChannel = 'payment' | 'redeem' | 'admin_add' | 'admin_deduct' | 'affiliate'
export interface SalesRange { from: string; to: string; days: number }
export interface SalesRangeQuery { from?: string; to?: string; days?: number }
export interface SalesSettings { excludedChannels: SalesChannel[] }
export interface SalesCoverage {
  from: string
  to: string
  complete: boolean
  previousComplete: boolean
  hasKnownData: boolean
  lastSyncedAt: string | null
  warnings: string[]
}
export interface SalesTotals {
  credits: number | null
  deductions: number | null
  payers: number | null
  repeatPayers: number | null
  newPayers: number | null
  registered: number
  registeredPaid: number | null
  conversion: number | null
  averagePerPayer: number | null
}
export interface SalesChannelTotal {
  channel: SalesChannel
  rawAmount: number | null
  excludedAmount: number | null
  effectiveAmount: number | null
  channelExcluded: boolean
}
export interface SalesExclusions { channels: SalesChannel[]; entries: number; amount: number | null; deductions: number | null }
export interface SalesSummary extends SalesScope {
  range: SalesRange
  previousRange: SalesRange
  coverage: SalesCoverage
  settings: SalesSettings
  totals: SalesTotals
  previous: SalesTotals | null
  channels: SalesChannelTotal[]
  daily: { day: string; credits: number | null; deductions: number | null; complete: boolean }[]
  topPayers: { siteUserId: string; email: string; username: string; credits: number; paidDays: number; lastDay: string }[]
  concentration: { top1Share: number | null; top10Share: number | null }
  exclusions: SalesExclusions
}
export interface SalesEntryKey { siteUserId: string; day: string; channel: SalesChannel }
export interface SalesEntry extends SalesEntryKey {
  email: string
  username: string
  rawAmount: number
  events: number
  excludedAmount: number
  excludedEvents: number
  effectiveAmount: number
  hasExclusion: boolean
  channelExcluded: boolean
  exclusionStatus: 'included' | 'excluded' | 'partial' | 'channel'
}
export interface SalesEntryQuery { page: number; pageSize: number; channel: SalesChannel | ''; q: string; excluded: '0' | '1' }
export interface SalesEntries extends SalesScope {
  range: SalesRange
  coverage: SalesCoverage
  settings: SalesSettings
  items: SalesEntry[]
  total: number
  page: number
  pageSize: number
  exclusions: SalesExclusions
}
export interface SalesMutation extends SalesScope {
  requested: number
  changed: number
  unchanged: number
  range: SalesRange
  exclusionSummary: SalesExclusions | null
  refreshMessage?: string
}
export interface SalesSettingsResult extends SalesScope { saved: true; settings: SalesSettings }

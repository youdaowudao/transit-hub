import type { ActivityScope } from './userActivity'
export type OperatingInsightsScope = ActivityScope
export interface OperatingSnapshotTotals {
  todayProfit: number
  todayPurchase: number
  operatingCost: number
  adjustedNetProfit: number
  profitMargin: number | null
}
export interface OperatingSiteEstimate {
  siteId: string
  name: string
  enabled: boolean
  status: string
  rawBalance: number | null
  averageRawCost: number | null
  daysLeft: number | null
  suggestedTopUp: number | null
  lastSyncedAt: string | null
  missingDates: string[]
  reasons: string[]
}
export interface OperatingInsightsResponse extends OperatingInsightsScope {
  range: { from: string; to: string; days: number }
  snapshotTotals: OperatingSnapshotTotals | null
  missingDates: string[]
  siteEstimates: OperatingSiteEstimate[]
  warnings: string[]
  observedAt: string
}

export interface ActivityScope { workspaceAdminAccountId: string; workspaceCreatedAt: string }
export interface ActivitySync {
  status: 'idle' | 'running' | 'success' | 'failed'
  error?: string
  warnings: string[]
  usageFrom: string
  usageTo: string
  rechargeFrom: string
  rechargeTo: string
  userCount: number
  startedAt: string | null
  lastSyncedAt: string | null
  todayUsageObservedAt: string | null
}
export interface ActivityCoverage { usageFrom: string; usageTo: string; rechargeFrom: string; rechargeTo: string; warnings: string[] }
export interface ActivityMetrics { from: string; to: string; requests: number; tokens: number; cost: number; activeUsers: number }
export interface ActivityPeriod { days: number; current: ActivityMetrics | null; previous: ActivityMetrics | null; retention: { retained: number; churned: number; gained: number } | null }
export interface ActivityDay { day: string; requests: number | null; tokens: number | null; cost: number | null; activeUsers: number | null; recharge: number | null; deduction: number | null; temporary: boolean; observedAt?: string }
export interface ActivityUser {
  siteUserId: string
  email: string
  username: string
  role: string
  status: string
  balance: number | null
  createdAt: string | null
  lastUsedAt: string | null
  firstObservedAt: string
  observedAt: string
  cost7d: number | null
  cost30d: number | null
  costPrev7d: number | null
  costPrev30d: number | null
  requests7d: number | null
  requestsPrev7d: number | null
  requestsToday: number | null
  costToday: number | null
  todayUsageObservedAt: string | null
}
export interface ActivitySummary extends ActivityScope {
  sync: ActivitySync
  today: ActivityDay
  periods: Record<string, ActivityPeriod>
  daily: ActivityDay[]
  money: { totalBalance: number | null; observedAt: string | null; todayRecharge: number | null; todayDeduction: number | null; yesterdayRecharge: number | null; yesterdayDeduction: number | null; complete: boolean; channels: { channel: string; amount: number; events: number }[] }
  coverage: ActivityCoverage
  warnings: string[]
  timezone: string
}
export interface ActivityUserList extends ActivityScope { items: ActivityUser[]; total: number; page: number; pageSize: number; sync: ActivitySync; coverage: ActivityCoverage }
export interface ActivityDetail extends ActivityScope { profile: ActivityUser; daily: ActivityDay[]; periods: Record<string, ActivityPeriod>; coverage: ActivityCoverage }
export interface ActivityUserQuery { page: number; pageSize: number; segment: string; inactiveDays: number; search: string; role: string; status: string; sort: string; order: string }

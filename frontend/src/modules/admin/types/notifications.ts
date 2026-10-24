export type NotificationLevel = 1 | 2 | 3 | 4
export type NotificationProgressAction = 'read' | 'present' | 'dismiss' | 'ack'
export type NotificationHistoryFilter = 'all' | 'unread' | 'pending'

export interface NotificationDetailField { label: string; value: string }
export interface NotificationAction {
  kind: string
  siteId?: string
  focusGroupId?: string
  focusGroup?: string
}
export interface NotificationContent {
  title: string
  summary: string
  details: NotificationDetailField[]
  action?: NotificationAction
}
export interface NotificationProgress {
  id: string
  readAt: string | null
  reminderCompletedAt: string | null
}
export interface NotificationSubjectState {
  subjectActive: boolean
  subjectInactiveMessage: string
}
export interface NotificationDisplayedState extends NotificationProgress, NotificationSubjectState {}
export interface SiteNotification extends NotificationProgress, NotificationSubjectState {
  adminAccountId: string
  kind: string
  subjectType: string
  subjectId: string
  level: NotificationLevel
  discoveredAt: string
  createdAt: string
  content: NotificationContent
}
export interface NotificationWarning { message: string; detail: string; next: string }
export interface NotificationActiveBucket {
  items: SiteNotification[]
  totalCount: number
  nextCursor: string | null
}
export interface NotificationSamplingSite { siteName: string; observedAt: string | null; reason: string }
export interface NotificationSamplingState {
  autoSyncEnabled: boolean | null
  unconfirmedSiteCount: number | null
  sites: NotificationSamplingSite[]
  warning?: NotificationWarning
}
export interface NotificationSummaryResponse {
  signupRisk?: { enabled: boolean; items: SiteNotification[] }
  serverTime: string
  workspaceAdminAccountId: string
  intervalSeconds: number | null
  preferenceWarning?: NotificationWarning
  unreadCount: number
  pendingCriticalCount: number
  active: {
    attention: NotificationActiveBucket
    important: NotificationActiveBucket
    critical: NotificationActiveBucket
  }
  samplingState: NotificationSamplingState
  displayedStates: NotificationDisplayedState[]
}

export interface SignupRiskPreferences {
  workspaceAdminAccountId: string
  enabled: boolean
  initialized: boolean
  updatedAt: string | null
}
export interface NotificationHistoryResponse {
  workspaceAdminAccountId: string
  items: SiteNotification[]
  nextCursor: string | null
}
export interface NotificationDetailResponse {
  workspaceAdminAccountId: string
  item: SiteNotification
  checkAction?: { available: boolean; message: string }
}
export interface NotificationProgressResponse {
  workspaceAdminAccountId: string
  items: NotificationProgress[]
}
export interface NotificationPreferencesResponse {
  workspaceAdminAccountId: string
  intervalSeconds: number
  updatedAt: string | null
}
export interface NotificationSummaryQuery {
  workspaceAdminAccountId: string
  attentionCursor?: string
  importantCursor?: string
  criticalCursor?: string
  displayedIds?: string[]
}
export interface NotificationHistoryQuery {
  workspaceAdminAccountId: string
  limit?: number
  cursor?: string
  filter?: NotificationHistoryFilter
}
export interface NotificationProgressRequest {
  workspaceAdminAccountId: string
  ids: string[]
  action: NotificationProgressAction
}
export interface NotificationPreferencesRequest { workspaceAdminAccountId: string; intervalMinutes: number }

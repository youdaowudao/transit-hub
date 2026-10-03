export type CampaignStatus =
  | 'draft'
  | 'scheduled'
  | 'running'
  | 'ending'
  | 'ended'
  | 'partial'
  | 'failed'
  | 'cancelled'

export type CampaignSelectionMode = 'all' | 'type' | 'manual' | 'currentFilter'

export type CampaignAdjustmentMode = 'set' | 'multiply' | 'add'

export type CampaignStartMode = 'now' | 'scheduled' | 'draft'

export type CampaignEndMode = 'scheduled' | 'manual'

export interface CampaignSelectionGroupRef {
  groupName: string
  campaignMultiplier: number | null
}

export interface CampaignSelectionFilter {
  search: string
  type: string
  platform: string
}

export interface CampaignSelection {
  mode: CampaignSelectionMode
  types: string[]
  groups: CampaignSelectionGroupRef[]
  filter: CampaignSelectionFilter
}

export interface CampaignAdjustment {
  mode: CampaignAdjustmentMode
  value: number
}

export interface CampaignNotify {
  enabled: boolean
  botIds: string[]
  recipientsInvalid?: boolean
  recipientsUnavailable?: boolean
  startTemplate: string
  endTemplate: string
}

export interface CampaignSchedule {
  startMode: CampaignStartMode
  startAt: string | null
  endMode: CampaignEndMode
  endAt: string | null
}

export interface CreateGroupRateCampaignRequest {
  name: string
  description: string
  selection: CampaignSelection
  adjustment: CampaignAdjustment
  schedule: CampaignSchedule
  notify: CampaignNotify
}

export interface CampaignPreviewItem {
  groupId: string
  groupName: string
  originalMultiplier: number
  campaignMultiplier: number
  restoredMultiplier: number
}

export interface CampaignPreviewResponse {
  items: CampaignPreviewItem[]
  total: number
}

export interface CampaignSummary {
  total: number
  applied: number
  applyFailed: number
  restored: number
  restoreFailed: number
}

export interface CampaignListItem {
  id: string
  name: string
  status: CampaignStatus
  startMode: CampaignStartMode
  startAt: string | null
  endMode: CampaignEndMode
  endAt: string | null
  startedAt: string | null
  endedAt: string | null
  summary: CampaignSummary
  notifyEnabled: boolean
  notifyRecipientsInvalid?: boolean
  notifyRecipientsUnavailable?: boolean
  createdBy: string
  createdAt: string
  updatedAt: string
  lastExecutedAt: string | null
}

export interface CampaignNotifyDefaults {
  enabled: boolean
  botIds: string[]
  recipientsInvalid?: boolean
  recipientsUnavailable?: boolean
  startTemplate: string
  endTemplate: string
}

export interface GroupRateCampaignsQuery {
  page: number
  pageSize: number
  status: string
}

export interface PaginatedGroupRateCampaignsResponse {
  items: CampaignListItem[]
  total: number
  page: number
  pageSize: number
  totalPages: number
  defaults: CampaignNotifyDefaults
}

export interface CampaignItemView {
  groupId: string
  groupName: string
  originalMultiplier: number | null
  campaignMultiplier: number
  restoredMultiplier: number | null
  applyStatus: string
  restoreStatus: string
  applyReason: string
  restoreReason: string
  appliedAt: string | null
  restoredAt: string | null
}

export interface CampaignDetail extends CampaignListItem {
  description: string
  selection: CampaignSelection
  adjustment: CampaignAdjustment
  notify: CampaignNotify
  items: CampaignItemView[]
}

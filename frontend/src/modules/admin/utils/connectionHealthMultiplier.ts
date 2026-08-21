import type { AdminGroupAccount, AdminGroupHealth, PrioritySyncStatus } from '../types/connectionHealth'

export interface ConnectionHealthMultiplierDisplay {
  value: number | null
  sourceKey: string
}

const usesMultiplierOnly = (account: AdminGroupAccount): boolean =>
  (account.effectivePolicies ?? []).some(policy => policy.enabled && policy.strategyMode === 'multiplier_only')

export const resolveConnectionHealthMultiplierDisplay = (
  account: AdminGroupAccount,
  groupMultiplier: number | null | undefined,
): ConnectionHealthMultiplierDisplay => {
  if (usesMultiplierOnly(account)) {
    return { value: groupMultiplier ?? null, sourceKey: 'adminGroup' }
  }
  if (account.multiplierSource === 'upstream_key') {
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey: account.multiplierResolutionStatus === 'stale' ? 'staleSnapshot' : 'upstreamKey',
    }
  }
  if (account.multiplierSource === 'local_fallback') {
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey: account.multiplierResolutionStatus === 'conflict' ? 'conflictFallback' : 'missingFallback',
    }
  }
  if (account.multiplierSource === 'last_confirmed') {
    const sourceKeyByStatus: Record<string, string> = {
      missing: 'lastConfirmedMissing',
      unavailable: 'lastConfirmedUnavailable',
      stale: 'lastConfirmedStale',
      updating: 'lastConfirmedUpdating',
    }
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey: sourceKeyByStatus[account.multiplierResolutionStatus ?? ''] ?? 'lastConfirmedUnavailable',
    }
  }
  const sourceKeyByStatus: Record<string, string> = {
    unassociated: 'unassociatedBandEnd',
    missing: 'missingFrozen',
    conflict: 'conflictBandEnd',
    stale: 'staleFrozen',
    unavailable: 'unavailableFrozen',
    updating: 'updatingFrozen',
    disabled: 'disabledNonParticipating',
  }
  return {
    value: account.effectiveMultiplier ?? null,
    sourceKey: sourceKeyByStatus[account.multiplierResolutionStatus ?? ''] ?? 'unknownBandEnd',
  }
}

export interface PrioritySyncFailureMessage {
  key: string
  params: Record<string, string | number>
}

const PRIORITY_SYNC_BLOCK_REASONS = new Set([
  'binding_missing',
  'site_unavailable',
  'key_unavailable',
  'key_missing',
  'groups_unavailable',
  'group_not_found',
  'multiplier_missing',
  'snapshot_stale',
  'snapshot_updating',
])

export const prioritySyncBlockReasonKey = (reason: string | null | undefined): string =>
  reason && PRIORITY_SYNC_BLOCK_REASONS.has(reason) ? reason : 'unknown'

export interface PrioritySyncBlocker {
  targetId: string
  accountName: string
  siteId: string
  reason: string
}

export const collectPrioritySyncBlockers = (groups: AdminGroupHealth[]): PrioritySyncBlocker[] => {
  const byTarget = new Map<string, PrioritySyncBlocker>()
  for (const group of groups) {
    for (const account of group.accounts ?? []) {
      if (!account.prioritySyncBlocked || byTarget.has(account.targetId)) continue
      byTarget.set(account.targetId, {
        targetId: account.targetId,
        accountName: account.name || account.id,
        siteId: account.upstreamSiteId ?? '',
        reason: prioritySyncBlockReasonKey(account.prioritySyncBlockReason),
      })
    }
  }
  return [...byTarget.values()]
}

export const resolvePriorityWorkspaceLabel = (
  displayName: string | null | undefined,
  workspaceId: string,
): string => displayName?.trim() || workspaceId

export const resolvePrioritySyncFailureMessage = (
  status: PrioritySyncStatus,
  time: string,
  reason: string,
): PrioritySyncFailureMessage => {
  const params: Record<string, string | number> = {
    workspace: status.workspaceId,
    time,
    reason,
  }
  if (status.status === 'partial') {
    return {
      key: 'admin.connectionHealth.prioritySync.partial',
      params: { ...params, count: status.failedCount },
    }
  }
  if (status.errorKey === 'admin.connectionHealth.errors.priorityInventoryIncomplete') {
    return { key: 'admin.connectionHealth.prioritySync.failedWithoutCount', params }
  }
  return {
    key: 'admin.connectionHealth.prioritySync.failed',
    params: { ...params, count: status.failedCount },
  }
}

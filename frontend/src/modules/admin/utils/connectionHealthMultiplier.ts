import type { AdminGroupAccount, PrioritySyncStatus } from '../types/connectionHealth'

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
  if (status.errorKey === 'admin.connectionHealth.errors.priorityInventoryIncomplete') {
    return { key: 'admin.connectionHealth.prioritySync.failedWithoutCount', params }
  }
  return {
    key: 'admin.connectionHealth.prioritySync.failed',
    params: { ...params, count: status.failedCount },
  }
}

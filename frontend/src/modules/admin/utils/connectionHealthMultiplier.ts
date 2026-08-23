import type { AdminGroupAccount, AdminGroupHealth, PrioritySyncStatus } from '../types/connectionHealth'

export interface ConnectionHealthMultiplierDisplay {
  value: number | null
  sourceKey: string
}

type MultiplierParticipation = 'multiplier_only' | 'health_multiplier' | 'not_participating' | 'unknown'

const resolveMultiplierParticipation = (account: AdminGroupAccount): MultiplierParticipation => {
  if (!Array.isArray(account.effectivePolicies)) return 'unknown'

  const projectionIsValid = account.effectivePolicies.every((policy) => {
    if (!policy || typeof policy !== 'object') return false
    if (typeof policy.enabled !== 'boolean') return false
    if (policy.strategyMode === 'multiplier_only') return policy.priorityMode === 'multiplier'
    if (policy.strategyMode === 'health_probe') {
      return policy.priorityMode === 'none' || policy.priorityMode === 'multiplier'
    }
    return false
  })
  if (!projectionIsValid) return 'unknown'

  if (account.effectivePolicies.some(policy => policy.enabled && policy.strategyMode === 'multiplier_only')) {
    return 'multiplier_only'
  }
  if (account.effectivePolicies.some(policy =>
    policy.enabled && policy.priorityMode === 'multiplier' && policy.strategyMode === 'health_probe',
  )) {
    return 'health_multiplier'
  }
  return 'not_participating'
}

const unknownMultiplierDisplay = (account: AdminGroupAccount): ConnectionHealthMultiplierDisplay => ({
  value: account.effectiveMultiplier ?? null,
  sourceKey: 'unknown',
})

const hasValidEffectiveMultiplier = (account: AdminGroupAccount): boolean =>
  typeof account.effectiveMultiplier === 'number'
  && Number.isFinite(account.effectiveMultiplier)
  && account.effectiveMultiplier > 0

export const resolveConnectionHealthMultiplierDisplay = (
  account: AdminGroupAccount,
  groupMultiplier: number | null | undefined,
): ConnectionHealthMultiplierDisplay => {
  const participation = resolveMultiplierParticipation(account)
  if (participation === 'unknown') return unknownMultiplierDisplay(account)
  if (participation === 'multiplier_only') {
    return { value: groupMultiplier ?? null, sourceKey: 'adminGroup' }
  }
  if (participation === 'not_participating') {
    if (account.multiplierSource !== 'none') return unknownMultiplierDisplay(account)
    return { value: account.effectiveMultiplier ?? null, sourceKey: 'notParticipating' }
  }

  if (account.multiplierSource === 'upstream_key') {
    if (!hasValidEffectiveMultiplier(account)) return unknownMultiplierDisplay(account)
    if (account.multiplierResolutionStatus !== 'resolved' && account.multiplierResolutionStatus !== 'stale') {
      return unknownMultiplierDisplay(account)
    }
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey: account.multiplierResolutionStatus === 'stale' ? 'staleSnapshot' : 'upstreamKey',
    }
  }
  if (account.multiplierSource === 'local_fallback') {
    if (!hasValidEffectiveMultiplier(account)) return unknownMultiplierDisplay(account)
    if (
      account.multiplierResolutionStatus !== 'unassociated'
      && account.multiplierResolutionStatus !== 'missing'
      && account.multiplierResolutionStatus !== 'conflict'
    ) {
      return unknownMultiplierDisplay(account)
    }
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey: account.multiplierResolutionStatus === 'conflict' ? 'conflictFallback' : 'missingFallback',
    }
  }
  if (account.multiplierSource === 'last_confirmed') {
    if (!hasValidEffectiveMultiplier(account)) return unknownMultiplierDisplay(account)
    const sourceKeyByStatus: Record<string, string> = {
      missing: 'lastConfirmedMissing',
      unavailable: 'lastConfirmedUnavailable',
      stale: 'lastConfirmedStale',
      updating: 'lastConfirmedUpdating',
    }
    const sourceKey = sourceKeyByStatus[account.multiplierResolutionStatus ?? '']
    if (!sourceKey) return unknownMultiplierDisplay(account)
    return {
      value: account.effectiveMultiplier ?? null,
      sourceKey,
    }
  }
  if (account.multiplierSource !== 'none') return unknownMultiplierDisplay(account)
  if (account.effectiveMultiplier != null) return unknownMultiplierDisplay(account)

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
    sourceKey: sourceKeyByStatus[account.multiplierResolutionStatus ?? ''] ?? 'unknown',
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
  'group_missing',
  'group_ambiguous',
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
  groupName: string
  groupId: string
  reason: string
}

export interface PrioritySyncBlockReasonMessage {
  key: string
  params: { group: string }
}

const upstreamGroupReference = (groupName: string, groupId: string): string => {
  const name = groupName.trim()
  const id = groupId.trim()
  if (name && id) return `“${name}”（ID ${id}）`
  if (name) return `“${name}”`
  if (id) return `（ID ${id}）`
  return ''
}

export const resolvePrioritySyncBlockReasonMessage = (
  reason: string | null | undefined,
  groupName: string | null | undefined,
  groupId: string | null | undefined,
): PrioritySyncBlockReasonMessage => ({
  key: prioritySyncBlockReasonKey(reason),
  params: { group: upstreamGroupReference(groupName ?? '', groupId ?? '') },
})

export const collectPrioritySyncBlockers = (groups: AdminGroupHealth[]): PrioritySyncBlocker[] => {
  const byTarget = new Map<string, PrioritySyncBlocker>()
  for (const group of groups) {
    for (const account of group.accounts ?? []) {
      if (!account.prioritySyncBlocked || byTarget.has(account.targetId)) continue
      byTarget.set(account.targetId, {
        targetId: account.targetId,
        accountName: account.name || account.id,
        siteId: account.upstreamSiteId ?? '',
        groupName: account.upstreamKeyGroupName ?? '',
        groupId: account.upstreamKeyGroupId ?? '',
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

import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import type { AdminGroupAccount } from '../src/modules/admin/types/connectionHealth'
import {
  prioritySyncBlockReasonKey,
  resolveConnectionHealthMultiplierDisplay,
  resolvePrioritySyncBlockReasonMessage,
} from '../src/modules/admin/utils/connectionHealthMultiplier'

const localeSource = readFileSync(new URL('../src/locales/zh-CN.ts', import.meta.url), 'utf8')
const detailSource = readFileSync(
  new URL('../src/modules/admin/components/dashboard/AdminGroupHealthDetail.vue', import.meta.url),
  'utf8',
)

const account = (overrides: Partial<AdminGroupAccount>): AdminGroupAccount => ({
  id: '100',
  name: 'account',
  platform: 'sub2api',
  type: '',
  status: 'active',
  targetId: 'sub2api:ws1:100',
  probeAvailable: true,
  modelHealth: [],
  intelligenceWeight: null,
  ...overrides,
})

const healthMultiplierPolicy = {
  policyId: 'health-multiplier',
  policyName: 'health multiplier',
  enabled: true,
  priorityMode: 'multiplier' as const,
  strategyMode: 'health_probe' as const,
}

const ordinaryHealthPolicy = {
  policyId: 'health-only',
  policyName: 'health only',
  enabled: true,
  priorityMode: 'none' as const,
  strategyMode: 'health_probe' as const,
}

describe('connection health multiplier resolution display', () => {
  it('uses the current upstream value for resolved and valued stale snapshots', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0.1,
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0.1, sourceKey: 'upstreamKey' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0.2,
      multiplierResolutionStatus: 'stale',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0.2, sourceKey: 'staleSnapshot' })
  })

  it('labels a last confirmed value with the real blocking status', () => {
    const expected = {
      missing: 'lastConfirmedMissing',
      unavailable: 'lastConfirmedUnavailable',
      stale: 'lastConfirmedStale',
      updating: 'lastConfirmedUpdating',
    }
    for (const [status, sourceKey] of Object.entries(expected)) {
      expect(resolveConnectionHealthMultiplierDisplay(account({
        effectivePolicies: [healthMultiplierPolicy],
        effectiveMultiplier: 0.06,
        multiplierResolutionStatus: status,
        multiplierSource: 'last_confirmed',
      }), null)).toEqual({ value: 0.06, sourceKey })
    }
  })

  it('keeps blocked status visible when no historical value exists', () => {
    const expected = {
      missing: 'missingFrozen',
      unavailable: 'unavailableFrozen',
      stale: 'staleFrozen',
      updating: 'updatingFrozen',
    }
    for (const [status, sourceKey] of Object.entries(expected)) {
      expect(resolveConnectionHealthMultiplierDisplay(account({
        effectivePolicies: [healthMultiplierPolicy],
        multiplierResolutionStatus: status,
        multiplierSource: 'none',
      }), null)).toEqual({ value: null, sourceKey })
    }
  })

  it('preserves multiplier-only, local fallback and disabled projections', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [{
        policyId: 'p1',
        policyName: 'p',
        enabled: true,
        priorityMode: 'multiplier',
        strategyMode: 'multiplier_only',
      }],
    }), 0.3)).toEqual({ value: 0.3, sourceKey: 'adminGroup' })
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0.4,
      multiplierResolutionStatus: 'conflict',
      multiplierSource: 'local_fallback',
    }), null)).toEqual({ value: 0.4, sourceKey: 'conflictFallback' })
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'disabled',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'disabledNonParticipating' })
  })

  it('labels accounts outside multiplier Priority as non-participating', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [],
      effectiveMultiplier: 0.18,
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'none',
    }), null)).toEqual({ value: 0.18, sourceKey: 'notParticipating' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [ordinaryHealthPolicy],
      multiplierResolutionStatus: 'unassociated',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'notParticipating' })
  })

  it('keeps band-end labels for active health multiplier Priority', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'unassociated',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unassociatedBandEnd' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'conflict',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'conflictBandEnd' })
  })

  it('uses a neutral unknown label for inconsistent multiplier metadata', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      multiplierResolutionStatus: 'unassociated',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [{
        policyId: 'incomplete',
        policyName: 'incomplete policy',
        enabled: true,
        strategyMode: 'health_probe',
      }],
      multiplierResolutionStatus: 'conflict',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [{
        ...ordinaryHealthPolicy,
        enabled: undefined as unknown as boolean,
      }],
      multiplierResolutionStatus: 'unassociated',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [ordinaryHealthPolicy],
      effectiveMultiplier: 0.2,
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0.2, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0.2,
      multiplierResolutionStatus: 'missing',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0.2, sourceKey: 'unknown' })
  })

  it('handles malformed effective policy rows as unknown metadata', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [null] as unknown as NonNullable<AdminGroupAccount['effectivePolicies']>,
      multiplierResolutionStatus: 'unassociated',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })
  })

  it('requires a valid multiplier value before showing a deterministic health source', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'conflict',
      multiplierSource: 'local_fallback',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      multiplierResolutionStatus: 'missing',
      multiplierSource: 'last_confirmed',
    }), null)).toEqual({ value: null, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0,
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0, sourceKey: 'unknown' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [healthMultiplierPolicy],
      effectiveMultiplier: 0.2,
      multiplierResolutionStatus: 'missing',
      multiplierSource: 'none',
    }), null)).toEqual({ value: 0.2, sourceKey: 'unknown' })
  })

  it('contains user-facing copy for the historical and frozen states', () => {
    expect(localeSource).toContain('最后确认倍率')
    expect(localeSource).toContain('本轮未用于 Priority 写回')
    expect(localeSource).toContain('已保留主站现有 Priority')
    expect(localeSource).toContain('当前策略未使用倍率排序')
    expect(localeSource).toContain('倍率状态未知，未确认参与当前排序')
    expect(localeSource).not.toContain('倍率来源未知，已按当前健康档末位排序')
  })

  it('maps only fixed safe blocker reasons and falls back for unknown values', () => {
    expect(prioritySyncBlockReasonKey('binding_missing')).toBe('binding_missing')
    expect(prioritySyncBlockReasonKey('key_unavailable')).toBe('key_unavailable')
    expect(prioritySyncBlockReasonKey('snapshot_stale')).toBe('snapshot_stale')
    expect(prioritySyncBlockReasonKey('raw upstream response')).toBe('unknown')
    expect(prioritySyncBlockReasonKey(undefined)).toBe('unknown')
  })

  it('describes a missing or ambiguous upstream group with its safe reference', () => {
    expect(resolvePrioritySyncBlockReasonMessage('group_missing', 'GPT Plus - 特惠', '14')).toEqual({
      key: 'group_missing',
      params: { group: '“GPT Plus - 特惠”（ID 14）' },
    })
    expect(resolvePrioritySyncBlockReasonMessage('group_ambiguous', '同名分组', '')).toEqual({
      key: 'group_ambiguous',
      params: { group: '“同名分组”' },
    })
    expect(resolvePrioritySyncBlockReasonMessage('group_missing', '', '14')).toEqual({
      key: 'group_missing',
      params: { group: '（ID 14）' },
    })
    expect(resolvePrioritySyncBlockReasonMessage('group_missing', '', '')).toEqual({
      key: 'group_missing',
      params: { group: '' },
    })
    expect(resolvePrioritySyncBlockReasonMessage('raw upstream response', '', '')).toEqual({
      key: 'unknown',
      params: { group: '' },
    })
    expect(localeSource).toContain('上游分组{group}已不存在')
    expect(localeSource).toContain('不再使用则删除绑定')
    expect(localeSource).toContain('匹配到多个同名分组')
  })

  it('shows the blocker reason beside the unchanged current Priority value', () => {
    expect(detailSource).toContain('{{ formatNumber(account.priority) }}')
    expect(detailSource).toContain('account.prioritySyncBlocked')
    expect(detailSource).toContain('prioritySyncBlockReasonLabel(account)')
    expect(detailSource).toContain('account.upstreamKeyGroupName')
    expect(detailSource).toContain('account.upstreamKeyGroupId')
  })
})

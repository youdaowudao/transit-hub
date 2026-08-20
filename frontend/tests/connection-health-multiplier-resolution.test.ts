import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import type { AdminGroupAccount } from '../src/modules/admin/types/connectionHealth'
import { resolveConnectionHealthMultiplierDisplay } from '../src/modules/admin/utils/connectionHealthMultiplier'

const localeSource = readFileSync(new URL('../src/locales/zh-CN.ts', import.meta.url), 'utf8')

const account = (overrides: Partial<AdminGroupAccount>): AdminGroupAccount => ({
  id: '100',
  name: 'account',
  platform: 'sub2api',
  type: '',
  status: 'active',
  targetId: 'sub2api:ws1:100',
  probeAvailable: true,
  modelHealth: [],
  ...overrides,
})

describe('connection health multiplier resolution display', () => {
  it('uses the current upstream value for resolved and valued stale snapshots', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectiveMultiplier: 0.1,
      multiplierResolutionStatus: 'resolved',
      multiplierSource: 'upstream_key',
    }), null)).toEqual({ value: 0.1, sourceKey: 'upstreamKey' })

    expect(resolveConnectionHealthMultiplierDisplay(account({
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
        multiplierResolutionStatus: status,
        multiplierSource: 'none',
      }), null)).toEqual({ value: null, sourceKey })
    }
  })

  it('preserves multiplier-only, local fallback and disabled projections', () => {
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectivePolicies: [{ policyId: 'p1', policyName: 'p', enabled: true, strategyMode: 'multiplier_only' }],
    }), 0.3)).toEqual({ value: 0.3, sourceKey: 'adminGroup' })
    expect(resolveConnectionHealthMultiplierDisplay(account({
      effectiveMultiplier: 0.4,
      multiplierResolutionStatus: 'conflict',
      multiplierSource: 'local_fallback',
    }), null)).toEqual({ value: 0.4, sourceKey: 'conflictFallback' })
    expect(resolveConnectionHealthMultiplierDisplay(account({
      multiplierResolutionStatus: 'disabled',
      multiplierSource: 'none',
    }), null)).toEqual({ value: null, sourceKey: 'disabledNonParticipating' })
  })

  it('contains user-facing copy for the historical and frozen states', () => {
    expect(localeSource).toContain('最后确认倍率')
    expect(localeSource).toContain('本轮未用于 Priority 写回')
    expect(localeSource).toContain('已保留主站现有 Priority')
  })
})

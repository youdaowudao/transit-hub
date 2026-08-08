import { describe, expect, it } from 'vitest'
import type { UpstreamMetricValue, UpstreamSite } from '@/modules/admin/types/upstream'
import { sortUpstreamSites } from '@/modules/admin/utils/upstream'

const metric = (value: number): UpstreamMetricValue => ({ value, display: String(value) })

const site = (id: string, name: string, enabled: boolean, todayConsume: number): UpstreamSite => ({
  id,
  name,
  enabled,
  baseUrl: `https://${id}.example.com`,
  platform: 'sub2api',
  requestedPlatform: 'sub2api',
  account: 'account',
  rechargeRate: 1,
  remark: '',
  logo: name.slice(0, 1),
  logoBg: '',
  status: 'connected',
  errorKey: null,
  metrics: {
    balance: metric(0),
    todayConsume: metric(todayConsume),
    historyRecharge: metric(0),
    group: { id: '', name: '', platform: null, multiplier: null, multiplierDisplay: '' },
    groups: [],
  },
  settings: { balanceThreshold: null },
  lastSyncedAt: null,
})

describe('sortUpstreamSites', () => {
  it('keeps disabled sites last and orders that section by name', () => {
    const sorted = sortUpstreamSites([
      site('disabled-z', 'Zeta', false, 999),
      site('enabled-low', 'Beta', true, 10),
      site('disabled-a', 'Alpha', false, 1),
      site('enabled-high', 'Gamma', true, 20),
    ], 'todayConsume', 'desc')

    expect(sorted.map(item => item.id)).toEqual([
      'enabled-high',
      'enabled-low',
      'disabled-a',
      'disabled-z',
    ])
  })
})

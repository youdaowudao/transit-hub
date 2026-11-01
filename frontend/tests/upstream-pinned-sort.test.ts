import { describe, expect, it } from 'vitest'
import type { UpstreamMetricValue, UpstreamSite } from '@/modules/admin/types/upstream'
import { sortUpstreamSites } from '@/modules/admin/utils/upstream'

const metric = (value: number): UpstreamMetricValue => ({ value, display: String(value) })

const site = (id: string, enabled: boolean, todayConsume: number, pinned?: boolean): UpstreamSite => ({
  id,
  name: id,
  enabled,
  ...(pinned === undefined ? {} : { pinned }),
  baseUrl: `https://${id}.example.com`,
  platform: 'sub2api',
  requestedPlatform: 'sub2api',
  account: 'account',
  rechargeRate: 1,
  remark: '',
  logo: id.slice(0, 1),
  logoBg: '',
  status: 'connected',
  errorKey: null,
  metrics: {
    balance: metric(todayConsume),
    todayConsume: metric(todayConsume),
    historyRecharge: metric(0),
    group: { id: '', name: '', platform: null, multiplier: null, multiplierDisplay: '' },
    groups: [],
  },
  settings: { balanceThreshold: null },
  lastSyncedAt: null,
})

const sites = () => [
  site('a-plain-high', true, 30),
  site('b-pinned-low', true, 5, true),
  site('c-off-pinned', false, 99, true),
  site('d-pinned-high', true, 20, true),
  site('e-plain-low', true, 1, false),
  site('f-off', false, 50),
]

describe('sortUpstreamSites with pinned sites', () => {
  it('puts enabled pinned sites first, then other enabled sites, and keeps disabled sites last by name', () => {
    expect(sortUpstreamSites(sites(), 'todayConsume', 'desc').map(item => item.id)).toEqual([
      'd-pinned-high', 'b-pinned-low', 'a-plain-high', 'e-plain-low', 'c-off-pinned', 'f-off',
    ])
  })

  it('keeps the current sort field and direction inside the pinned and unpinned groups', () => {
    expect(sortUpstreamSites(sites(), 'todayConsume', 'asc').map(item => item.id)).toEqual([
      'b-pinned-low', 'd-pinned-high', 'e-plain-low', 'a-plain-high', 'c-off-pinned', 'f-off',
    ])
  })

  it('keeps the original order rules when no site is pinned', () => {
    const plain = sites().map(item => ({ ...item, pinned: false }))
    expect(sortUpstreamSites(plain, 'todayConsume', 'desc').map(item => item.id)).toEqual([
      'a-plain-high', 'd-pinned-high', 'b-pinned-low', 'e-plain-low', 'c-off-pinned', 'f-off',
    ])
  })
})

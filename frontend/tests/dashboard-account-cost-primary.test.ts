import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { formatAccountStatsRefreshNotice } from '../src/modules/admin/utils/dashboard'

const source = readFileSync(new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url), 'utf8')

describe('dashboard operating cost primary cards', () => {
  it('uses operating cost, adjusted profit and adjusted margin as primary values', () => {
    expect(source).toContain('liveData.value?.operatingCost')
    expect(source).toContain('liveData.value?.adjustedNetProfit')
    expect(source).toContain('liveData.value?.adjustedProfitMargin')
    expect(source).toContain("case 'todayPurchase':\n      openAccountCostWorkspace()")
  })

  it('shows the account cost workspace without the old duplicate summary', () => {
    expect(source).toContain('<AccountCostWorkspace')
    expect(source).not.toContain('dashboard-operating-cost-details')
  })

  it('keeps the manual account asset entry without automatic account refresh', () => {
    expect(source).not.toContain('accountStatsRefreshNotice')
    expect(source).not.toContain('refreshAccountStats(')
    expect(source).toContain("openAccountCostWorkspace('assets')")
  })

  it('does not report an incomplete refresh when there are no automatic accounts', () => {
    expect(formatAccountStatsRefreshNotice({
      quality: 'missing',
      completedAccounts: 0,
      expectedAccounts: 0,
    })).toBe('')
    expect(formatAccountStatsRefreshNotice({
      quality: 'missing',
      completedAccounts: 1,
      expectedAccounts: 2,
    })).toContain('1/2')
  })
})

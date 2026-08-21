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

  it('shows automatic account refresh failures without clearing the previous result', () => {
    expect(source).toContain('accountStatsRefreshNotice')
    expect(source).toContain('formatAccountStatsRefreshNotice(result)')
    expect(source).toContain('账号自动统计刷新失败')
    expect(source).toContain("openAccountCostWorkspace('assets')")
    expect(source).not.toContain(".catch(() => { /* 账号子状态失败不清空首页已确认数据。 */ })")
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

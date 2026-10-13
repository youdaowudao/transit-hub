// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import StatCard from '@/modules/admin/components/dashboard/StatCard.vue'
import DashboardView from '@/modules/admin/views/DashboardView.vue'

const harness = vi.hoisted(() => ({
  getDashboardMetrics: vi.fn(),
  getDashboardTrends: vi.fn(),
  refreshAccountStats: vi.fn(),
  getGroupProfitToday: vi.fn(),
  listAccountCostLedger: vi.fn(),
  listAccountAssets: vi.fn(),
  listRealConnections: vi.fn(),
  routerPush: vi.fn(),
 snapshot: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: harness.routerPush }),
}))

vi.mock('@vueuse/core', async () => {
  const { ref } = await import('vue')
  return { useMediaQuery: () => ref(false) }
})

vi.mock('@/modules/admin/composables/useDashboardAdmin', async () => {
  const { ref } = await import('vue')
  return {
    useDashboardAdmin: () => ({
      status: ref({ authenticated: true, identity: 'admin@example.com', authMethod: 'admin_key' }),
      isModalOpen: ref(false),
      isSubmitting: ref(false),
      isRefreshingCredentials: ref(false),
      errorKey: ref(null),
      checkStatus: vi.fn(async () => undefined),
      submitLogin: vi.fn(),
      updateAdminCredentials: vi.fn(),
      openModal: vi.fn(),
      closeModal: vi.fn(),
    }),
  }
})

vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  return {
    useAdminAccounts: () => ({
      currentAccount: ref({ id: 'workspace-a', displayName: '工作区 A' }),
    }),
  }
})

vi.mock('@/modules/admin/composables/useDashboardChartTheme', async () => {
  const { ref } = await import('vue')
  return {
    useDashboardChartTheme: () => ({
      theme: ref({
        foreground: '#111111', muted: '#666666', border: '#dddddd', card: '#ffffff',
        primary: '#2255aa', signal: '#228844', warning: '#aa7700', destructive: '#aa2222',
      }),
    }),
  }
})

vi.mock('@/modules/admin/composables/useDashboardDataCache', () => ({
  getDashboardDataSnapshot: harness.snapshot,
  saveDashboardDataSnapshot: vi.fn(),
  updateDashboardOperationalSnapshot: vi.fn(),
}))

vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  getDashboardMetrics: harness.getDashboardMetrics,
  getDashboardTrends: harness.getDashboardTrends,
  refreshAccountStats: harness.refreshAccountStats,
  getGroupProfitToday: harness.getGroupProfitToday,
  getGroupUsageToday: vi.fn(async () => ({ groups: [] })),
  getUpstreamBalanceBreakdown: vi.fn(async () => ({ sites: [] })),
  createAccountBatch: vi.fn(),
  createAccountEvent: vi.fn(),
  createAdditionalCost: vi.fn(),
  getAdditionalCost: vi.fn(),
  updateAdditionalCost: vi.fn(),
  getAccountAsset: vi.fn(),
  getRechargeFeeRate: vi.fn(async () => ({ id: 'rate-current', effectiveDate: '2026-08-22', rate: 0.016, createdAt: '2026-08-22T00:00:00Z' })),
  listRechargeFeeRateHistory: vi.fn(async () => ({ items: [] })),
  listAccountAssets: harness.listAccountAssets,
  listAccountCostLedger: harness.listAccountCostLedger,
  replaceAccountLink: vi.fn(),
  saveRechargeFeeRate: vi.fn(),
}))

vi.mock('@/modules/admin/api/mySites', () => ({
  listRealConnections: harness.listRealConnections,
}))

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getConnectionHealthStoredSummary: vi.fn(async () => null),
}))


let wrapper: VueWrapper | undefined
const oldLive = {
 date: '2031-02-03', timezone: 'Asia/Singapore', todayProfit: 120, siteBalance: 500, upstreamBalance: 200, groupCount: 2,
 todayPurchase: 83, netProfit: 37, operatingCost: 83, adjustedNetProfit: 37, adjustedProfitMargin: 30.833,
 accountSnapshotRunId: 'legacy', accountStatsQuality: 'complete', accountExpectedCount: 1, accountCompletedCount: 1,
 costQuality: { businessDate: '2031-02-03', mode: 'exact', complete: true, confirmedCost: 83, expectedSites: 1, collectedSites: 1, failedSites: 0 },
 additionalCosts: { rechargeFee: 0, accountPurchase: 10, accountRefund: 0, replacementDeduction: 5, accountQuality: 'complete', promotion: 0, fixed: 0, adjustment: 0, total: 10, available: true },
}
const history = { points: [{ date: '2031-02-02', todayProfit: 100, siteBalance: 490, todayPurchase: 11, netProfit: 89, operatingCost: 11, adjustedNetProfit: 89, upstreamBalance: 190, settlementStatus: 'final' }] }
const card = (w: VueWrapper, label: string) => {
 const found = w.findAllComponents(StatCard).find(component => component.props('label') === label)
 if (!found) throw new Error(`missing real card: ${label}`)
 return found
}
beforeEach(() => {
 vi.clearAllMocks()
 vi.useFakeTimers(); vi.setSystemTime(new Date('2031-02-03T04:00:00Z')); localStorage.clear()
 harness.snapshot.mockReturnValue({ adminStatus: { authenticated: true, identity: 'admin@example.com' }, live: oldLive, trends: history, updatedAt: Date.now(), groupProfit: { groups: [{ groupId: 'old', todayProfit: 37 }] } })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.clearAllTimers(); vi.useRealTimers(); localStorage.clear() })
describe('saved dashboard costs wait for the existing API', () => {
 it('displays detailed cost failures and translates legacy failure keys after the API resolves', async () => {
  const detail = '读取新加坡指定日费用失败：上游缺少实际费用字段；本轮未更新，可同步这个站点重试，不会修改主站。'
  harness.getDashboardMetrics.mockResolvedValue({ ...oldLive, todayPurchase: null, operatingCost: null, netProfit: null, adjustedNetProfit: null,
   costQuality: { ...oldLive.costQuality, complete: false, mode: 'unavailable', failures: [
    { siteName: '具体原因站', reason: 'fetch_error', errorDetail: detail },
    { siteName: '旧字段站', reason: 'date_mismatch' },
   ] },
  })
  harness.getDashboardTrends.mockResolvedValue(history)
  harness.getGroupProfitToday.mockImplementation(() => new Promise(() => undefined))
  wrapper = mount(DashboardView, { global: { stubs: { Teleport: true, AdminLoginModal: true, BalanceFilterModal: true, GroupUsageTodayModal: true, UpstreamBalanceBreakdownModal: true, UpstreamKeyUsageTodayModal: true, DailyStatsPanel: true, AccountCostWorkspace: true, DashboardEChart: true } } })
  await flushPromises()
  expect(wrapper.text()).toContain(detail)
  expect(wrapper.text()).toContain('这份费用不属于新加坡今天，请同步这个站点重新读取')
  expect(wrapper.text()).not.toContain('fetch_error')
  expect(wrapper.text()).not.toContain('date_mismatch')
  expect(card(wrapper, '今日总成本').props('value')).toBe('—')
 })
 it('keeps cached revenue, balances and historical trend while masking unproven current fees until the fresh API resolves', async () => {
  let resolveLive: ((value: any) => void) | undefined
  let resolveTrends: ((value: any) => void) | undefined
  harness.getDashboardMetrics.mockImplementation(() => new Promise(resolve => { resolveLive = resolve }))
  harness.getDashboardTrends.mockImplementation(() => new Promise(resolve => { resolveTrends = resolve }))
  harness.getGroupProfitToday.mockImplementation(() => new Promise(() => undefined))
  wrapper = mount(DashboardView, { global: { stubs: { Teleport: true, AdminLoginModal: true, BalanceFilterModal: true, GroupUsageTodayModal: true, UpstreamBalanceBreakdownModal: true, UpstreamKeyUsageTodayModal: true, DailyStatsPanel: true, AccountCostWorkspace: true, DashboardEChart: { name: 'DashboardEChart', props: ['option'], template: '<div data-test="real-history-projection" />' } } } })
  await flushPromises()
  expect(card(wrapper, '今日营收').props('value')).toBe('120.00')
  expect(wrapper.text()).toContain('500.00')
  expect(wrapper.text()).toContain('200.00')
  expect(card(wrapper, '今日总成本').props('value')).toBe('—')
  expect(card(wrapper, '今日净利润').props('value')).toBe('—')
  expect(wrapper.text()).not.toContain('83.00')
  expect(wrapper.text()).not.toContain('37.00')
  const workspace = wrapper.findComponent({ name: 'AccountCostWorkspace' })
  expect((workspace.props('summary') as { accountQuality?: string })?.accountQuality).not.toBe('complete')
  const historyChart = wrapper.findComponent({ name: 'DashboardEChart' })
  expect(JSON.stringify(historyChart.props('option'))).toContain('11')
  resolveLive?.({ ...oldLive, todayPurchase: 40, netProfit: 80, operatingCost: 50, adjustedNetProfit: 70, accountSnapshotRunId: 'sg', costQuality: { ...oldLive.costQuality, confirmedCost: 40 }, additionalCosts: { ...oldLive.additionalCosts, replacementDeduction: 0 } })
  resolveTrends?.(history)
  await flushPromises()
  expect(card(wrapper, '今日总成本').props('value')).toBe('50.00')
  expect(card(wrapper, '今日净利润').props('value')).toBe('70.00')
  expect(card(wrapper, '今日营收').props('value')).toBe('120.00')
  expect(JSON.stringify(historyChart.props('option'))).toContain('11')
  expect(harness.getDashboardMetrics).toHaveBeenCalledTimes(1)
 })
})

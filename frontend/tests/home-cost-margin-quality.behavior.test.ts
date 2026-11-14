// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import DashboardView from '@/modules/admin/views/DashboardView.vue'

const harness = vi.hoisted(() => ({
  adminStatus: { authenticated: true, identity: 'admin@example.com', authMethod: 'admin_key' } as Record<string, unknown>,
  adminModalOpen: false,
  checkAdminStatus: vi.fn(),
  getDashboardMetrics: vi.fn(),
  getDashboardTrends: vi.fn(),
  refreshAccountStats: vi.fn(),
  routerPush: vi.fn(),
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
      status: ref({ ...harness.adminStatus }),
      isModalOpen: ref(harness.adminModalOpen),
      isSubmitting: ref(false),
      isRefreshingCredentials: ref(false),
      errorKey: ref(null),
      checkStatus: harness.checkAdminStatus,
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
  getDashboardDataSnapshot: vi.fn(() => null),
  saveDashboardDataSnapshot: vi.fn(),
  updateDashboardOperationalSnapshot: vi.fn(),
}))

vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  getDashboardMetrics: harness.getDashboardMetrics,
  getDashboardTrends: harness.getDashboardTrends,
  refreshAccountStats: harness.refreshAccountStats,
  getGroupProfitToday: vi.fn(async () => ({ groups: [] })),
  getGroupUsageToday: vi.fn(async () => ({ groups: [] })),
  getUpstreamBalanceBreakdown: vi.fn(async () => ({ sites: [] })),
  createAccountBatch: vi.fn(),
  createAccountEvent: vi.fn(),
  createAdditionalCost: vi.fn(),
  getAccountAsset: vi.fn(),
  getRechargeFeeRate: vi.fn(),
  listAccountAssets: vi.fn(),
  listAccountCostLedger: vi.fn(),
  replaceAccountLink: vi.fn(),
  saveRechargeFeeRate: vi.fn(),
}))

vi.mock('@/modules/admin/api/connectionHealth', () => ({
  getConnectionHealthStoredSummary: vi.fn(async () => null),
}))

const liveMetrics = {
  date: '2026-08-22',
  timezone: 'Asia/Singapore',
  todayProfit: 120,
  siteBalance: 500,
  todayPurchase: 40,
  netProfit: 80,
  upstreamBalance: 200,
  groupCount: 2,
  operatingCost: 50,
  adjustedNetProfit: 70,
  adjustedProfitMargin: 58.333,
  costQuality: { mode: 'exact', complete: true, confirmedCost: 40, expectedSites: 1, collectedSites: 1, failedSites: 0 },
  additionalCosts: {
    rechargeFee: 2, accountPurchase: 10, accountRefund: -2, replacementDeduction: 5,
    promotion: 1, fixed: 3, adjustment: 1, total: 15, available: true,
  },
}

const mountedWrappers: VueWrapper[] = []
const mountDashboard = () => {
  const wrapper = mount(DashboardView, {
    global: {
      stubs: {
        AdminLoginModal: { props: ['open'], template: '<div v-if="open" data-test="admin-login-modal" />' },
        BalanceFilterModal: true,
        DashboardEChart: true,
        GroupUsageTodayModal: true,
        UpstreamBalanceBreakdownModal: true,
        UpstreamKeyUsageTodayModal: true,
        DailyStatsPanel: true,
        AccountCostWorkspace: true,
      },
    },
  })
  mountedWrappers.push(wrapper)
  return wrapper
}

beforeEach(() => {
  harness.adminStatus = { authenticated: true, identity: 'admin@example.com', authMethod: 'admin_key' }
  harness.adminModalOpen = false
  harness.checkAdminStatus.mockReset().mockResolvedValue(undefined)
  harness.getDashboardMetrics.mockReset().mockResolvedValue(liveMetrics)
  harness.getDashboardTrends.mockReset().mockResolvedValue({ points: [] })
  harness.refreshAccountStats.mockReset().mockResolvedValue({
    date: '2026-08-22', snapshotRunId: 'run-1', expectedSites: 1, completedSites: 1,
    quality: 'complete', expectedAccounts: 0, completedAccounts: 0,
  })
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('D8 adjusted profit margin quality', () => {
  it.each([
    { quality: 'ceiling', mode: 'partial', expected: '暂估上限 58.3%' },
    { quality: 'unavailable', mode: 'unavailable', expected: '成本暂不可用' },
    { quality: 'exact', mode: 'partial', expected: '' },
    { quality: undefined, mode: 'exact', expected: '' },
  ])('uses explicit $quality quality and preserves legacy fallback', async ({ quality, mode, expected }) => {
    harness.getDashboardMetrics.mockResolvedValue({
      ...liveMetrics,
      adjustedProfitMarginQuality: quality,
      costQuality: { ...liveMetrics.costQuality, mode, complete: mode === 'exact', collectedSites: mode === 'unavailable' ? 0 : 1 },
    })
    const wrapper = mountDashboard()
    await flushPromises()
    const card = wrapper.findAll('[data-dashboard-core-card]').find(item => item.text().includes('今日利润率'))
      ?? wrapper.findAll('button').find(item => item.text().includes('今日利润率'))
    expect(card, 'profit margin card').toBeDefined()
    if (!card) return
    if (expected) expect(card.text()).toContain(expected)
    if (quality === 'unavailable') {
      expect(card.text()).not.toContain('58.3%')
    } else {
      expect(card.text()).toContain('58.3%')
    }
    if (quality === 'exact' || quality == null) expect(card.text()).not.toContain('暂估上限')
  })
})

describe('D8 nullable operating trends reach the chart', () => {
  it('preserves partial gaps and confirmed zero through response, metrics and chart options', async () => {
    harness.getDashboardTrends.mockResolvedValue({ points: [
      { date: '2026-08-20', todayProfit: 20, siteBalance: 0, upstreamBalance: 0,
        todayPurchase: null, netProfit: null, confirmedCost: null, netProfitCeiling: null,
        operatingCost: null, adjustedNetProfit: null, settlementStatus: 'partial' },
      { date: '2026-08-21', todayProfit: 20, siteBalance: 0, upstreamBalance: 0,
        todayPurchase: null, netProfit: null, confirmedCost: 0, netProfitCeiling: 20,
        operatingCost: 0, adjustedNetProfit: 20, settlementStatus: 'partial_high' },
    ] })
    const wrapper = mountDashboard()
    await flushPromises()
    const chart = wrapper.findAllComponents({ name: 'DashboardEChart' }).find(item => {
      const option = item.props('option') as { series?: unknown[] } | undefined
      return option?.series?.length === 3
    })
    expect(chart, 'performance chart').toBeDefined()
    const option = chart!.props('option') as { series: Array<{ data: Array<{ value: number } | null> }> }
    expect(option.series[1]!.data[0]).toBeNull()
    expect(option.series[2]!.data[0]).toBeNull()
    expect(option.series[1]!.data[1]!.value).toBe(0)
    expect(option.series[2]!.data[1]!.value).toBe(20)
  })
})

describe('V3.1.6 revenue discount on the home page', () => {
  it('shows net revenue on the revenue card with the deducted discount, and keeps gross revenue without a rule', async () => {
    harness.getDashboardMetrics.mockResolvedValue({
      ...liveMetrics,
      additionalCosts: { ...liveMetrics.additionalCosts, revenueDiscount: 7.8, revenueDiscountRate: 0.065, netRevenue: 112.2 },
    })
    const wrapper = mountDashboard()
    await flushPromises()
    const card = wrapper.findAll('[data-dashboard-core-card]').find(item => item.text().includes('今日营收'))
      ?? wrapper.findAll('button').find(item => item.text().includes('今日营收'))
    expect(card, 'revenue card').toBeDefined()
    expect(card!.text()).toContain('112.20')
    expect(card!.text()).toContain('已扣营收折扣 7.80（主站消费 120.00）')
  })

  it('V3.1.7 shows the site-wide part and individual customer gifts when the discount has a split', async () => {
    harness.getDashboardMetrics.mockResolvedValue({
      ...liveMetrics,
      additionalCosts: { ...liveMetrics.additionalCosts, revenueDiscount: 13, revenueDiscountRate: 0.05, netRevenue: 107,
        revenueDiscountDetail: { version: 1, generalRate: 0.05, generalBase: 60, generalDiscount: 3, ledgerCovered: 60, ledgerGift: 10, customers: [] } },
    })
    const wrapper = mountDashboard()
    await flushPromises()
    const card = wrapper.findAll('[data-dashboard-core-card]').find(item => item.text().includes('今日营收'))
      ?? wrapper.findAll('button').find(item => item.text().includes('今日营收'))
    expect(card!.text()).toContain('已扣营收折扣 13.00（整站 3.00 + 单独客户赠送 10.00；主站消费 120.00）')
    expect(card!.text()).not.toContain('已扣营收折扣 13.00（主站消费 120.00）')
  })

  it('keeps the gross amount and no discount line when no discount rule applies', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    const card = wrapper.findAll('[data-dashboard-core-card]').find(item => item.text().includes('今日营收'))
      ?? wrapper.findAll('button').find(item => item.text().includes('今日营收'))
    expect(card!.text()).toContain('120.00')
    expect(card!.text()).not.toContain('已扣营收折扣')
  })

  it('shows revenue unavailable with the plain reason when the discount rate cannot be read', async () => {
    harness.getDashboardMetrics.mockResolvedValue({
      ...liveMetrics, todayProfit: null, adjustedNetProfit: null, adjustedProfitMargin: null,
      metricErrors: { todayProfit: '营收折扣率读取失败，暂不计算成本和利润；请稍后刷新，仍失败请检查本站数据库连接。不会修改主站。' },
      additionalCosts: { ...liveMetrics.additionalCosts, total: null, available: false },
    })
    const wrapper = mountDashboard()
    await flushPromises()
    const card = wrapper.findAll('[data-dashboard-core-card]').find(item => item.text().includes('今日营收'))
      ?? wrapper.findAll('button').find(item => item.text().includes('今日营收'))
    expect(card!.text()).toContain('营收折扣率读取失败')
    expect(card!.text()).not.toContain('120.00')
    expect(card!.text()).not.toContain('已扣营收折扣')
  })

  it('draws net revenue in the trend from the stored discount of each day', async () => {
    harness.getDashboardTrends.mockResolvedValue({ points: [
      { date: '2026-08-20', todayProfit: 100, revenueDiscount: 6, siteBalance: 0, upstreamBalance: 0,
        todayPurchase: 40, netProfit: 60, operatingCost: 40, adjustedNetProfit: 54, settlementStatus: 'final' },
      { date: '2026-08-21', todayProfit: 50, siteBalance: 0, upstreamBalance: 0,
        todayPurchase: 10, netProfit: 40, operatingCost: 10, adjustedNetProfit: 40, settlementStatus: 'final' },
    ] })
    const wrapper = mountDashboard()
    await flushPromises()
    const chart = wrapper.findAllComponents({ name: 'DashboardEChart' }).find(item => {
      const option = item.props('option') as { series?: unknown[] } | undefined
      return option?.series?.length === 3
    })
    expect(chart, 'performance chart').toBeDefined()
    const option = chart!.props('option') as { series: Array<{ data: Array<{ value: number } | null> }> }
    expect(option.series[0]!.data[0]!.value).toBe(94)
    expect(option.series[0]!.data[1]!.value).toBe(50)
  })
})

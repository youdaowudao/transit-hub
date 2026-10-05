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

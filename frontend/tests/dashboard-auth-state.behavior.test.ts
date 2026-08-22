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

vi.mock('@/modules/admin/composables/useDashboardMetrics', async () => {
  const { ref } = await import('vue')
  return {
    useDashboardMetrics: () => {
      const metrics = ref<Array<Record<string, unknown>>>([])
      const liveData = ref<Record<string, any> | null>(null)
      const applyRawData = (live: Record<string, any>) => {
        liveData.value = live
        const points = (value: number | null) => ({
          week: [{ label: '8/21', value, date: '2026-08-21' }, { label: '8/22', value, date: '2026-08-22' }],
          month: [{ label: '8/21', value, date: '2026-08-21' }, { label: '8/22', value, date: '2026-08-22' }],
        })
        metrics.value = [
          { key: 'todayProfit', color: 'primary', current: live.todayProfit, series: points(live.todayProfit) },
          { key: 'siteBalance', color: 'accent', current: live.siteBalance, series: points(live.siteBalance) },
          { key: 'todayPurchase', color: 'warning', current: live.operatingCost, series: points(live.operatingCost) },
          { key: 'netProfit', color: 'signal', current: live.adjustedNetProfit, series: points(live.adjustedNetProfit) },
          { key: 'upstreamBalance', color: 'primary', current: live.upstreamBalance, series: points(live.upstreamBalance) },
        ]
      }
      return { metrics, liveData, applyRawData }
    },
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
  timezone: 'Asia/Shanghai',
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

describe('R01 Dashboard login state', () => {
  it.each([
    { authenticated: true, refreshNotice: '', identityVisible: true, loginVisible: false, noticeVisible: false },
    { authenticated: true, refreshNotice: '账号自动统计未完成（1/2）', identityVisible: true, loginVisible: false, noticeVisible: true },
    { authenticated: false, refreshNotice: '', identityVisible: false, loginVisible: true, noticeVisible: false },
    { authenticated: false, refreshNotice: '上一工作区的旧刷新提示', identityVisible: false, loginVisible: true, noticeVisible: false },
  ])('keeps identity, refresh notice and login prompt mutually exclusive: %o', async ({
    authenticated,
    refreshNotice,
    identityVisible,
    loginVisible,
    noticeVisible,
  }) => {
    harness.adminStatus = authenticated
      ? { authenticated: true, identity: 'admin@example.com', authMethod: 'admin_key' }
      : { authenticated: false }
    const wrapper = mountDashboard()
    await flushPromises()

    ;(wrapper.vm as unknown as { accountStatsRefreshNotice: string }).accountStatsRefreshNotice = refreshNotice
    await nextTick()

    expect(wrapper.text().includes('当前 admin：admin@example.com')).toBe(identityVisible)
    expect(wrapper.text().includes('尚未登录 admin 账户')).toBe(loginVisible)
    expect(wrapper.text().includes(refreshNotice || '不会出现的刷新提示')).toBe(noticeVisible)
    expect(wrapper.text().includes('当前 admin：admin@example.com') && wrapper.text().includes('尚未登录 admin 账户')).toBe(false)
  })

  it('shows the login modal without duplicating the inline login prompt', async () => {
    harness.adminStatus = { authenticated: false }
    harness.adminModalOpen = true
    const wrapper = mountDashboard()
    await flushPromises()

    expect(wrapper.find('[data-test="admin-login-modal"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('尚未登录 admin 账户')
  })
})

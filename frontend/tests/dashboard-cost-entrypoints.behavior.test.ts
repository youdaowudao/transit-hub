// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'
import StatCard from '@/modules/admin/components/dashboard/StatCard.vue'
import DashboardView from '@/modules/admin/views/DashboardView.vue'

const harness = vi.hoisted(() => ({
  getDashboardMetrics: vi.fn(),
  getDashboardTrends: vi.fn(),
  refreshAccountStats: vi.fn(),
  listAccountCostLedger: vi.fn(),
  listAccountAssets: vi.fn(),
  listRealConnections: vi.fn(),
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

vi.mock('@/modules/admin/composables/useDashboardMetrics', async () => {
  const { ref } = await import('vue')
  return {
    useDashboardMetrics: () => {
      const metrics = ref<Array<Record<string, unknown>>>([])
      const liveData = ref<Record<string, any> | null>(null)
      const applyRawData = (live: Record<string, any>) => {
        liveData.value = live
        const points = (previous: number | null, current: number | null) => ({
          week: [{ label: '8/21', value: previous, date: '2026-08-21' }, { label: '8/22', value: current, date: '2026-08-22' }],
          month: [{ label: '8/21', value: previous, date: '2026-08-21' }, { label: '8/22', value: current, date: '2026-08-22' }],
        })
        metrics.value = [
          { key: 'todayProfit', color: 'primary', current: live.todayProfit, series: points(100, live.todayProfit) },
          { key: 'siteBalance', color: 'accent', current: live.siteBalance, series: points(490, live.siteBalance) },
          { key: 'todayPurchase', color: 'warning', current: live.operatingCost, series: points(45, live.operatingCost) },
          { key: 'netProfit', color: 'signal', current: live.adjustedNetProfit, series: points(55, live.adjustedNetProfit) },
          { key: 'upstreamBalance', color: 'primary', current: live.upstreamBalance, series: points(190, live.upstreamBalance) },
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
const mountDashboard = async () => {
  const wrapper = mount(DashboardView, {
    global: {
      stubs: {
        Teleport: true,
        AdminLoginModal: true,
        BalanceFilterModal: true,
        DashboardEChart: true,
        GroupUsageTodayModal: true,
        UpstreamBalanceBreakdownModal: true,
        UpstreamKeyUsageTodayModal: {
          props: ['open'],
          template: '<div v-if="open" data-test="upstream-key-usage">上游 Key 与分组明细</div>',
        },
        DailyStatsPanel: true,
      },
    },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const findCard = (wrapper: VueWrapper, label: string) => {
  const card = wrapper.findAllComponents(StatCard).find(item => item.props('label') === label)
  if (!card) throw new Error(`missing StatCard: ${label}`)
  return card
}

const findButton = (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text().includes(label))
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

beforeEach(() => {
  harness.getDashboardMetrics.mockReset().mockResolvedValue(liveMetrics)
  harness.getDashboardTrends.mockReset().mockResolvedValue({ points: [] })
  harness.refreshAccountStats.mockReset().mockResolvedValue({
    date: '2026-08-22', snapshotRunId: 'run-1', expectedSites: 1, completedSites: 1,
    quality: 'complete', expectedAccounts: 0, completedAccounts: 0,
  })
  harness.listAccountCostLedger.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listAccountAssets.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listRealConnections.mockReset().mockResolvedValue([])
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('Dashboard cost entrypoint behavior', () => {
  it('R02 continues from today total cost to the existing upstream Key and group details without stacking drawers', async () => {
    const wrapper = await mountDashboard()

    await findCard(wrapper, '今日总成本').trigger('click')
    await nextTick()

    const workspace = wrapper.findComponent(AccountCostWorkspace)
    expect(workspace.props('open')).toBe(true)
    expect(workspace.props('initialTab')).toBe('today')

    await findButton(workspace, '查看上游成本明细').trigger('click')
    await nextTick()

    expect(workspace.props('open')).toBe(false)
    expect(wrapper.find('[data-test="upstream-key-usage"]').exists()).toBe(true)
  })

  it('R03 renders the six fixed homepage cost components and keeps their sum equal to total cost', async () => {
    const wrapper = await mountDashboard()
    const section = wrapper.findAll('section').find(item => item.text().includes('成本构成'))
    if (!section) throw new Error('missing homepage cost composition')

    const text = section.text()
    expect(text).toContain('上游直接 ¥35.00')
    expect(text).toContain('买号确认 ¥8.00')
    expect(text).toContain('手续费 ¥2.00')
    expect(text).toContain('活动 ¥1.00')
    expect(text).toContain('固定 ¥3.00')
    expect(text).toContain('调整 ¥1.00')
    expect(text).not.toContain('其他')
    expect(findCard(wrapper, '今日总成本').props('value')).toBe('¥50.00')
  })

  it('R04 opens today cost from net profit and shows the revenue-cost-profit calculation', async () => {
    const wrapper = await mountDashboard()

    await findCard(wrapper, '今日净利润').trigger('click')
    await nextTick()

    const workspace = wrapper.findComponent(AccountCostWorkspace)
    expect(workspace.props('open')).toBe(true)
    expect(workspace.text()).toContain('今日营收 ¥120.00')
    expect(workspace.text()).toContain('今日总成本 ¥50.00')
    expect(workspace.text()).toContain('今日净利润 ¥70.00')
    expect(workspace.text()).toContain('净利润 = 营收 - 总成本')
  })

  it('R05 opens today cost from profit margin and shows the net-profit-to-revenue formula', async () => {
    const wrapper = await mountDashboard()

    await findCard(wrapper, '今日利润率').trigger('click')
    await nextTick()

    const workspace = wrapper.findComponent(AccountCostWorkspace)
    expect(workspace.props('open')).toBe(true)
    expect(workspace.text()).toContain('今日净利润 ¥70.00')
    expect(workspace.text()).toContain('今日营收 ¥120.00')
    expect(workspace.text()).toContain('利润率 = 净利润 ÷ 营收')
    expect(workspace.text()).toContain('58.3%')
  })
})

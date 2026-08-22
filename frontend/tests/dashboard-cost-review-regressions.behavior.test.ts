// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'
import DashboardView from '@/modules/admin/views/DashboardView.vue'

const harness = vi.hoisted(() => ({
  getDashboardMetrics: vi.fn(),
  getDashboardTrends: vi.fn(),
  refreshAccountStats: vi.fn(),
  getRechargeFeeRate: vi.fn(),
  listRechargeFeeRateHistory: vi.fn(),
  saveRechargeFeeRate: vi.fn(),
  listAccountCostLedger: vi.fn(),
  listAccountAssets: vi.fn(),
  listRealConnections: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))

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
  return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'workspace-a', displayName: '工作区 A' }) }) }
})

vi.mock('@/modules/admin/composables/useDashboardMetrics', async () => {
  const { ref } = await import('vue')
  return {
    useDashboardMetrics: () => {
      const metrics = ref<Array<Record<string, unknown>>>([])
      const liveData = ref<Record<string, any> | null>(null)
      const applyRawData = (live: Record<string, any>) => {
        liveData.value = live
        const series = (value: number | null) => ({
          week: [{ label: '8/21', value }, { label: '8/22', value }],
          month: [{ label: '8/21', value }, { label: '8/22', value }],
        })
        metrics.value = [
          { key: 'todayProfit', color: 'primary', current: live.todayProfit, series: series(live.todayProfit) },
          { key: 'siteBalance', color: 'accent', current: live.siteBalance, series: series(live.siteBalance) },
          { key: 'todayPurchase', color: 'warning', current: live.operatingCost, series: series(live.operatingCost) },
          { key: 'netProfit', color: 'signal', current: live.adjustedNetProfit, series: series(live.adjustedNetProfit) },
          { key: 'upstreamBalance', color: 'primary', current: live.upstreamBalance, series: series(live.upstreamBalance) },
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
  getRechargeFeeRate: harness.getRechargeFeeRate,
  listRechargeFeeRateHistory: harness.listRechargeFeeRateHistory,
  listAccountAssets: harness.listAccountAssets,
  listAccountCostLedger: harness.listAccountCostLedger,
  replaceAccountLink: vi.fn(),
  saveRechargeFeeRate: harness.saveRechargeFeeRate,
}))

vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: harness.listRealConnections }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthStoredSummary: vi.fn(async () => null) }))

const baseMetrics = {
  date: '2026-08-22', timezone: 'Asia/Shanghai', todayProfit: 120, siteBalance: 500,
  todayPurchase: 40, netProfit: 80, upstreamBalance: 200, groupCount: 2,
  operatingCost: 55, adjustedNetProfit: 65, adjustedProfitMargin: 54.16,
  costQuality: { mode: 'exact', complete: true, confirmedCost: 40, expectedSites: 1, collectedSites: 1, failedSites: 0 },
  additionalCosts: {
    rechargeFee: 2, accountPurchase: 10, accountRefund: -2,
    accountQuality: 'complete', promotion: 1, fixed: 3, adjustment: 1, total: 15, available: true,
  },
}

const workspaceProps = {
  open: false,
  businessDate: '2026-08-22',
  directCost: 40,
  operatingCost: 55,
  adjustedNetProfit: 65,
  initialTab: 'today' as const,
  workspaceId: 'workspace-a',
}

const wrappers: VueWrapper[] = []

const mountDashboard = async (metrics: Record<string, any>) => {
  harness.getDashboardMetrics.mockResolvedValue(metrics)
  const wrapper = mount(DashboardView, {
    global: {
      stubs: {
        Teleport: true,
        AdminLoginModal: true,
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
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const costCompositionText = (wrapper: VueWrapper) => {
  const section = wrapper.findAll('section').find(item => item.text().includes('成本构成'))
  if (!section) throw new Error('missing homepage cost composition')
  return section.text()
}

const ledgerItem = (id: string, name: string) => ({
  id, type: 'fixed', name, businessDate: '2026-08-22', amount: 1, amountCents: 100,
  sourceId: id, estimated: false, createdAt: '2026-08-22T10:00:00+08:00',
})

beforeEach(() => {
  harness.getDashboardMetrics.mockReset()
  harness.getDashboardTrends.mockReset().mockResolvedValue({ points: [] })
  harness.refreshAccountStats.mockReset().mockResolvedValue({
    date: '2026-08-22', snapshotRunId: 'run-1', expectedSites: 1, completedSites: 1,
    quality: 'complete', expectedAccounts: 0, completedAccounts: 0,
  })
  harness.getRechargeFeeRate.mockReset().mockResolvedValue({
    id: 'rate-current', effectiveDate: '2026-08-22', rate: 0.016, createdAt: '2026-08-22T00:00:00Z',
  })
  harness.listRechargeFeeRateHistory.mockReset().mockResolvedValue({ items: [] })
  harness.saveRechargeFeeRate.mockReset().mockResolvedValue(undefined)
  harness.listAccountCostLedger.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listAccountAssets.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listRealConnections.mockReset().mockResolvedValue([])
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('reviewed dashboard cost regressions', () => {
  it('R03 treats an omitted replacement deduction as zero only for a complete available account summary', async () => {
    const complete = await mountDashboard(baseMetrics)
    expect(costCompositionText(complete)).toContain('上游直接 ¥40.00')

    const missing = await mountDashboard({
      ...baseMetrics,
      operatingCost: null,
      additionalCosts: { ...baseMetrics.additionalCosts, accountQuality: 'missing' },
    })
    expect(costCompositionText(missing)).toContain('上游直接 ¥—')
    expect(costCompositionText(missing)).not.toContain('上游直接 ¥40.00')
  })

  it('R06 loads every page of the current-day ledger before rendering it', async () => {
    harness.listAccountCostLedger
      .mockResolvedValueOnce({ items: [ledgerItem('page-1', '第一页成本')], hasMore: true })
      .mockResolvedValueOnce({ items: [ledgerItem('page-2', '第二页成本')], hasMore: false })

    const wrapper = mount(AccountCostWorkspace, {
      props: workspaceProps,
      global: { stubs: { Teleport: true } },
    })
    wrappers.push(wrapper)
    await wrapper.setProps({ open: true })
    await flushPromises()

    expect(harness.listAccountCostLedger.mock.calls).toEqual([
      [{ from: '2026-08-22', to: '2026-08-22', page: 1, pageSize: 100 }],
      [{ from: '2026-08-22', to: '2026-08-22', page: 2, pageSize: 100 }],
    ])
    expect(wrapper.text()).toContain('第一页成本')
    expect(wrapper.text()).toContain('第二页成本')
  })

  it('R06 reports failure and renders no partial rows when a later current-day page fails', async () => {
    harness.listAccountCostLedger
      .mockResolvedValueOnce({ items: [ledgerItem('page-1', '不完整第一页')], hasMore: true })
      .mockRejectedValueOnce(new Error('page 2 unavailable'))

    const wrapper = mount(AccountCostWorkspace, {
      props: workspaceProps,
      global: { stubs: { Teleport: true } },
    })
    wrappers.push(wrapper)
    await wrapper.setProps({ open: true })
    await flushPromises()

    expect(wrapper.text()).toContain('当日成本变动加载失败')
    expect(wrapper.text()).not.toContain('不完整第一页')
    expect(wrapper.text()).not.toContain('当日暂无成本变动')
  })

  it('R07 keeps the current fee form available when fee history loading fails', async () => {
    harness.getRechargeFeeRate.mockResolvedValue({
      id: 'rate-current', effectiveDate: '2026-08-20', rate: 0.025, createdAt: '2026-08-20T00:00:00Z',
    })
    harness.listRechargeFeeRateHistory.mockRejectedValue(new Error('history initial unavailable'))

    const wrapper = mount(AccountCostWorkspace, {
      props: { ...workspaceProps, initialTab: 'rules' },
      global: { stubs: { Teleport: true } },
    })
    wrappers.push(wrapper)
    await wrapper.setProps({ open: true })
    await flushPromises()

    expect((wrapper.find('input[placeholder="费率 %"]').element as HTMLInputElement).value).toBe('2.5')
    expect((wrapper.find('input[type="date"]').element as HTMLInputElement).value).toBe('2026-08-20')
    expect(wrapper.text()).toContain('费率历史加载失败')
    expect(wrapper.text()).not.toContain('history initial unavailable')
  })

  it('R07 keeps a successful save and emits updated when the following history refresh fails', async () => {
    harness.listRechargeFeeRateHistory
      .mockResolvedValueOnce({ items: [] })
      .mockRejectedValueOnce(new Error('history refresh unavailable'))

    const wrapper = mount(AccountCostWorkspace, {
      props: { ...workspaceProps, initialTab: 'rules' },
      global: { stubs: { Teleport: true } },
    })
    wrappers.push(wrapper)
    await wrapper.setProps({ open: true })
    await flushPromises()

    const saveButton = wrapper.findAll('button').find(button => button.text().includes('保存费率'))
    if (!saveButton) throw new Error('missing save fee rate button')
    await saveButton.trigger('click')
    await flushPromises()

    expect(harness.saveRechargeFeeRate).toHaveBeenCalledWith({ effectiveDate: '2026-08-22', rate: 0.016 })
    expect(wrapper.emitted('updated')).toHaveLength(1)
    expect(wrapper.text()).toContain('费率历史加载失败')
    expect(wrapper.text()).not.toContain('保存失败')
    expect(wrapper.text()).not.toContain('history refresh unavailable')
  })
})

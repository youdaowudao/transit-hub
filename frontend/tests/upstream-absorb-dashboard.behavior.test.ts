// @vitest-environment jsdom
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import DashboardView from '@/modules/admin/views/DashboardView.vue'
import { t } from '@/locales'
import { account, insights, scope } from './fixtures/upstreamAbsorb'
const api = vi.hoisted(() => ({ read: vi.fn(), metrics: vi.fn(), trends: vi.fn(), groups: vi.fn(), balance: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@vueuse/core', async () => { const { ref } = await import('vue'); return { useMediaQuery: () => ref(false) } })
vi.mock('@/modules/admin/composables/useDashboardAdmin', async () => { const { ref } = await import('vue'); return { useDashboardAdmin: () => ({ status: ref({ authenticated: true, identity: 'fixture@example.test', authMethod: 'admin_key' }), isModalOpen: ref(false), isSubmitting: ref(false), isRefreshingCredentials: ref(false), errorKey: ref(null), checkStatus: vi.fn(async () => undefined), submitLogin: vi.fn(), updateAdminCredentials: vi.fn(), openModal: vi.fn(), closeModal: vi.fn() }) } })
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => { const { ref } = await import('vue'); return { useAdminAccounts: () => ({ currentAccount: ref(account()), isSwitching: ref(false) }) } })
vi.mock('@/modules/admin/composables/useDashboardChartTheme', async () => { const { ref } = await import('vue'); return { useDashboardChartTheme: () => ({ theme: ref({ foreground: '#111', muted: '#666', border: '#ddd', card: '#fff', primary: '#25a', signal: '#284', warning: '#a70', destructive: '#a22' }) }) } })
vi.mock('@/modules/admin/composables/useDashboardDataCache', () => ({ getDashboardDataSnapshot: vi.fn(() => null), saveDashboardDataSnapshot: vi.fn(), updateDashboardOperationalSnapshot: vi.fn() }))
vi.mock('@/modules/admin/api/operatingInsights', () => ({ getOperatingInsights: api.read }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  getDashboardMetrics: api.metrics, getDashboardTrends: api.trends, refreshAccountStats: vi.fn(), getGroupProfitToday: api.groups, getGroupUsageToday: vi.fn(async () => ({ groups: [] })), getUpstreamBalanceBreakdown: api.balance,
  createAccountBatch: vi.fn(), createAccountEvent: vi.fn(), createAdditionalCost: vi.fn(), getAdditionalCost: vi.fn(), updateAdditionalCost: vi.fn(), getAccountAsset: vi.fn(),
  getRechargeFeeRate: vi.fn(async () => ({ id: 'rate', effectiveDate: '2026-11-19', rate: 0, createdAt: '2026-11-19T00:00:00Z' })), listRechargeFeeRateHistory: vi.fn(async () => ({ items: [] })), listRevenueDiscountRates: vi.fn(async () => ({ items: [] })), listAccountAssets: vi.fn(async () => ({ items: [] })), listAccountCostLedger: vi.fn(async () => ({ items: [] })), replaceAccountLink: vi.fn(), saveRechargeFeeRate: vi.fn(),
}))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn(async () => ({ items: [] })) }))
vi.mock('@/modules/admin/api/connectionHealth', () => ({ getConnectionHealthStoredSummary: vi.fn(async () => null) }))
let wrapper: ReturnType<typeof mount> | undefined
beforeEach(() => {
  vi.resetAllMocks(); localStorage.clear(); api.read.mockResolvedValue(insights()); api.trends.mockResolvedValue({ points: [] }); api.groups.mockResolvedValue({ groups: [] }); api.balance.mockResolvedValue({ sites: [] })
  api.metrics.mockResolvedValue({ date: '2026-11-20', timezone: 'Asia/Singapore', todayProfit: 120, siteBalance: 500, upstreamBalance: 200, groupCount: 2, todayPurchase: null, netProfit: null, operatingCost: null, adjustedNetProfit: null, adjustedProfitMargin: null, accountStatsQuality: 'unavailable', costQuality: { businessDate: '2026-11-20', mode: 'unavailable', complete: false, confirmedCost: null, expectedSites: 1, collectedSites: 0, failedSites: 0 }, additionalCosts: { rechargeFee: 0, accountPurchase: 0, accountRefund: 0, replacementDeduction: 0, promotion: 0, fixed: 0, adjustment: 0, total: 0, available: false } })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; localStorage.clear() })
it('leaves operating insights idle across the original Dashboard refresh and invokes only its local read when explicitly expanded', async () => {
  wrapper = mount(DashboardView, { global: { stubs: { Teleport: true, AdminLoginModal: true, BalanceFilterModal: true, GroupUsageTodayModal: true, UpstreamBalanceBreakdownModal: true, UpstreamKeyUsageTodayModal: true, DailyStatsPanel: true, AccountCostWorkspace: true, DashboardEChart: true } } }); await flushPromises()
  expect(api.metrics).toHaveBeenCalledOnce(); expect(api.read).not.toHaveBeenCalled()
  await wrapper.get(`button[aria-label="${t('admin.dashboard.dataStatus.refresh')}"]`).trigger('click'); await flushPromises(); expect(api.metrics).toHaveBeenCalledTimes(2); expect(api.read).not.toHaveBeenCalled()
  const expand = wrapper.findAll('button').find(button => button.text() === t('admin.dashboard.operating.expand'))!; await expand.trigger('click'); await flushPromises()
  expect(api.read).toHaveBeenCalledExactlyOnceWith(scope, expect.any(AbortSignal)); expect(api.metrics).toHaveBeenCalledTimes(2); expect(api.groups).toHaveBeenCalledTimes(2); expect(api.balance).toHaveBeenCalledTimes(2)
  const collapse = wrapper.findAll('button').find(button => button.text() === t('admin.dashboard.operating.collapse'))!; await collapse.trigger('click'); await wrapper.get(`button[aria-label="${t('admin.dashboard.dataStatus.refresh')}"]`).trigger('click'); await flushPromises(); expect(api.read).toHaveBeenCalledOnce(); expect(api.metrics).toHaveBeenCalledTimes(3)
})

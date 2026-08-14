import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const dashboardSource = readFileSync(
  new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url),
  'utf8',
)

describe('dashboard group contribution display', () => {
  it('keeps the original revenue mode and adds a profit mode', () => {
    expect(dashboardSource).toContain("type GroupMetricMode = 'profit' | 'revenue'")
    expect(dashboardSource).toContain("const groupMetricModes: GroupMetricMode[] = ['profit', 'revenue']")
    expect(dashboardSource).toContain("? 'admin.dashboard.groups.profitAmount'")
    expect(dashboardSource).toContain(": 'admin.dashboard.groups.revenueAmount'")
  })

  it('keeps authoritative group revenue and shows only direct profit with an explicit remainder', () => {
    expect(dashboardSource).toContain('const groupRevenueTotal = computed(() => groupUsage.value?.totalRevenue ?? groupUsage.value?.total ?? 0)')
    expect(dashboardSource).toContain('const displayedAdjustedNetProfit = computed')
    expect(dashboardSource).toContain('.filter((item) => item.todayProfit != null)')
    expect(dashboardSource).toContain("groupId: '__unallocated_profit__'")
    expect(dashboardSource).toContain("groupName: t('admin.dashboard.groups.unallocatedProfit')")
    expect(dashboardSource).toContain('const unallocatedProfit = displayedAdjustedNetProfit.value - directProfit')
    expect(dashboardSource).toContain("item?.contributionKind === 'unallocated_profit'")
    expect(dashboardSource).toContain('已归属营收')
    expect(dashboardSource).toContain('已归属成本')
  })

  it('does not use connection coverage, key attribution, or unbound cost', () => {
    expect(dashboardSource).not.toContain('groupProfitIssues')
    expect(dashboardSource).not.toContain('unbound_upstream_cost')
    expect(dashboardSource).toContain('item.todayProfit ?? null')
  })

  it('publishes group revenue before unrelated operational reads settle', () => {
    expect(dashboardSource).toContain('const groupRequest = getGroupUsageToday()')
    expect(dashboardSource).toContain('groupUsage.value = value')
    expect(dashboardSource).toContain('const groupProfitRequest = getGroupProfitToday()')
    expect(dashboardSource).toContain('groupProfit.value = value')
    expect(dashboardSource).toContain('const groupRevenueLoading = ref(false)')
    expect(dashboardSource).toContain('const groupProfitLoading = ref(false)')
    expect(dashboardSource).toContain('activeGroupLoadError.value && activeGroupData.value')
    expect(dashboardSource).toContain('const balanceRequest = getUpstreamBalanceBreakdown()')
  })
})

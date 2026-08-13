import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const dashboardSource = readFileSync(
  new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url),
  'utf8',
)

describe('dashboard group profit display boundary', () => {
  it('requires exact group status before plotting profit', () => {
    expect(dashboardSource).toContain("item.status === 'exact' && item.todayProfit != null")
  })

  it('does not hide exact groups only because the global total is partial', () => {
    expect(dashboardSource).not.toContain("if (groupMetricMode.value === 'profit' && !groupProfitAvailable.value) return []")
    expect(dashboardSource).toContain("groupMetricMode === 'profit' && topGroups.length === 0 && !groupProfitAvailable")
  })

  it('keeps concentration unavailable until totalProfit is formal', () => {
    expect(dashboardSource).toContain("? groupUsage.value?.totalProfit")
    expect(dashboardSource).toContain('if (total == null || total <= 0) return null')
  })

  it('keeps unbound cost as a separate diagnostic from blocking issues', () => {
    expect(dashboardSource).toContain('const groupUnboundCost = computed')
    expect(dashboardSource).toContain('groupProfitIssues.length')
    expect(dashboardSource).toContain('groupUnboundCost != null')
  })

  it('shows unbound upstream cost only as a profit contribution', () => {
    expect(dashboardSource).toContain("if (item.contributionKind === 'unbound_upstream_cost') return null")
    expect(dashboardSource).toContain("item.contributionKind === 'unbound_upstream_cost'")
  })
})

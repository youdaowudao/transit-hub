import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const dashboardSource = readFileSync(
  new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url),
  'utf8',
)

describe('dashboard cost composition', () => {
  it('replaces the old expandable duplicate summary with one inline composition', () => {
    expect(dashboardSource).toContain('成本构成')
    expect(dashboardSource).not.toContain('additionalCostsExpanded')
    expect(dashboardSource).not.toContain('dashboard-operating-cost-details')
  })

  it('shows the six fixed cost components and direct account-cost actions without raw records', () => {
    for (const label of ['上游直接', '买号确认', '手续费', '活动', '固定', '调整']) {
      expect(dashboardSource).toContain(`{ label: '${label}', value:`)
    }
    expect(dashboardSource).not.toContain("{ label: '其他', value:")
    expect(dashboardSource).toContain('const displayedTodayCost = computed')
    expect(dashboardSource).toContain("@click=\"openAccountCostWorkspace('today')\">记一笔成本")
    expect(dashboardSource).toContain("@click=\"openAccountCostWorkspace('assets')\">录入买号")
    expect(dashboardSource).not.toContain('v-for="record in liveData.additionalCosts.records"')
  })
})

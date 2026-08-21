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

  it('shows the three cost components and direct account-cost actions without raw records', () => {
    expect(dashboardSource).toContain('上游 {{ formatCny(displayedTodayCost) }}')
    expect(dashboardSource).toContain('买号 {{ formatCny(liveData?.additionalCosts?.accountPurchase ?? null) }}')
    expect(dashboardSource).toContain('其他 {{ formatCny(')
    expect(dashboardSource).toContain('const displayedTodayCost = computed')
    expect(dashboardSource).toContain("@click=\"openAccountCostWorkspace('today')\">记一笔成本")
    expect(dashboardSource).toContain("@click=\"openAccountCostWorkspace('assets')\">录入买号")
    expect(dashboardSource).not.toContain('v-for="record in liveData.additionalCosts.records"')
  })
})

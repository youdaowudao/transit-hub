import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const dashboardSource = readFileSync(
  new URL('../src/modules/admin/views/DashboardView.vue', import.meta.url),
  'utf8',
)

describe('dashboard additional cost summary', () => {
  it('starts collapsed and exposes one expandable summary', () => {
    expect(dashboardSource).toContain('const additionalCostsExpanded = ref(false)')
    expect(dashboardSource).toContain(':aria-expanded="additionalCostsExpanded"')
    expect(dashboardSource).toContain('v-if="additionalCostsExpanded"')
  })

  it('shows a complete cost breakdown without repeating raw records', () => {
    expect(dashboardSource).toContain('上游直接成本')
    expect(dashboardSource).toContain('additionalCostLines')
    expect(dashboardSource).not.toContain('v-for="record in liveData.additionalCosts.records"')
  })
})

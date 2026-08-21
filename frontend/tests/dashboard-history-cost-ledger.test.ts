import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('../src/modules/admin/components/dashboard/DailyStatsPanel.vue', import.meta.url), 'utf8')

describe('dashboard historical operating cost', () => {
  it('uses operating cost and adjusted profit at the first level', () => {
    expect(source).toContain('item.operatingCost')
    expect(source).toContain('item.adjustedNetProfit')
    expect(source).toContain('adjustedMargin(item)')
  })

  it('shows account cost components and keeps legacy gaps explicit', () => {
    expect(source).toContain('item.additionalCosts.accountPurchase')
    expect(source).toContain('item.additionalCosts.accountRefund')
    expect(source).toContain('item.replacementDeduction')
    expect(source).toContain('历史口径不完整')
  })
})

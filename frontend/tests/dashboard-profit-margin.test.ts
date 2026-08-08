import { describe, expect, it } from 'vitest'
import {
  buildProfitMarginSeries,
  calculateProfitMargin,
  computeDelta,
  computeDashboardMetricDelta,
} from '../src/modules/admin/utils/dashboard'

describe('calculateProfitMargin', () => {
  it('calculates an exact margin when revenue and cost are complete', () => {
    const result = calculateProfitMargin({
      revenue: 301.69,
      netProfit: 147.10,
      costComplete: true,
      confirmedCost: 154.59,
      collectedSites: 23,
    })

    expect(result.mode).toBe('exact')
    expect(result.value).toBeCloseTo(147.10 / 301.69 * 100, 12)
  })

  it('returns a ceiling when only 20 of 23 sites are available', () => {
    const result = calculateProfitMargin({
      revenue: 301.69,
      netProfit: null,
      costComplete: false,
      confirmedCost: 154.59,
      collectedSites: 20,
    })

    expect(result.mode).toBe('ceiling')
    expect(result.value).toBeCloseTo((301.69 - 154.59) / 301.69 * 100, 12)
  })

  it('calculates a fallback margin without treating it as exact', () => {
    const result = calculateProfitMargin({
      revenue: 301.69,
      netProfit: 147.10,
      costComplete: false,
      costMode: 'fallback',
      confirmedCost: 154.59,
      collectedSites: 23,
    })

    expect(result.mode).toBe('fallback')
    expect(result.value).toBeCloseTo(147.10 / 301.69 * 100, 12)
  })

  it('returns unavailable instead of 100 percent when all site costs are unavailable', () => {
    expect(calculateProfitMargin({
      revenue: 301.69,
      netProfit: null,
      costComplete: false,
      confirmedCost: 0,
      collectedSites: 0,
    })).toEqual({
      value: null,
      mode: 'unavailable',
    })
  })
})

describe('profit margin series', () => {
  it('keeps a missing profit as null in the margin series', () => {
    const series = buildProfitMarginSeries(
      [
        { label: '8/2', date: '2026-08-02', value: 200 },
        { label: '8/3', date: '2026-08-03', value: 301.69 },
      ],
      [
        { label: '8/2', date: '2026-08-02', value: 88.4 },
        { label: '8/3', date: '2026-08-03', value: null },
      ],
    )

    expect(series).toEqual([
      { label: '8/2', date: '2026-08-02', value: 44.2 },
      { label: '8/3', date: '2026-08-03', value: null },
    ])
    expect(computeDelta(series).unavailable).toBe(true)
  })

  it('marks a one-point series as unavailable instead of a flat zero delta', () => {
    expect(computeDelta([
      { label: '8/3', date: '2026-08-03', value: 48.8 },
    ])).toEqual({
      amount: 0,
      direction: 'flat',
      unavailable: true,
      reason: 'missing_data',
    })
  })
})

describe('revenue delta', () => {
  it('remains available when cost settlement is partial', () => {
    const points = [
      { label: '8/7', date: '2026-08-07', value: 300, status: 'final' },
      { label: '8/8', date: '2026-08-08', value: 348.54, status: 'partial' },
    ]
    const result = computeDashboardMetricDelta('todayProfit', points)
    expect(result.unavailable).toBeUndefined()
    expect(result.direction).toBe('up')
    expect(result.amount).toBeCloseTo(48.54, 12)

    expect(computeDashboardMetricDelta('todayPurchase', points)).toMatchObject({
      unavailable: true,
      reason: 'partial_cost',
    })
  })
})

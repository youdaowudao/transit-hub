// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import DailyStatsPanel from '@/modules/admin/components/dashboard/DailyStatsPanel.vue'

const harness = vi.hoisted(() => ({ getDailyStats: vi.fn() }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ getDailyStats: harness.getDailyStats }))

let wrapper: VueWrapper | undefined
const date = '2031-02-03'
const failure = '读取独立总额超时；本轮费用未更新，可同步这个站点重试，不会修改主站。'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2031-02-03T04:00:00Z'))
  harness.getDailyStats.mockReset()
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.clearAllTimers()
  vi.useRealTimers()
})

describe('current daily cost details reuse confirmed results', () => {
  it.each(['fresh', 'stale', 'retained'])('shows consistent amount and explanation for %s', async state => {
    const usable = state !== 'stale'
    const site = {
      siteId: 'site', siteName: '原账号', platform: 'sub2api', rechargeRate: 2,
      adjustedCost: usable ? 18 : null, status: usable ? 'ok' : 'missing', source: 'live_cache',
      observedAt: usable ? '2031-02-03T03:59:00Z' : undefined,
      errorReason: usable ? undefined : '最近的费用记录已经过期，请同步这个站点重新读取；不会修改主站。',
      lastAttemptStatus: state === 'retained' ? 'failed' : undefined,
      lastAttemptError: state === 'retained' ? failure : undefined,
      lastAttemptAt: state === 'retained' ? '2031-02-03T04:00:00Z' : undefined,
    }
    harness.getDailyStats.mockImplementation(async (from: string, to: string, options: { expand: boolean }) => ({
      items: from <= date && to >= date ? [{
        date, settlementStatus: 'provisional', costQualityMode: state === 'retained' ? 'retained' : usable ? 'exact' : 'unavailable',
        todayProfit: 30, operatingCost: usable ? 18 : null, adjustedNetProfit: usable ? 12 : null,
        siteCosts: options.expand ? [site] : undefined,
      }] : [],
    }))
    wrapper = mount(DailyStatsPanel)
    await flushPromises()
    const inputs = wrapper.findAll('input[type="date"]')
    await inputs[0]!.setValue(date)
    await inputs[1]!.setValue(date)
    await flushPromises()
    await wrapper.find('tbody tr').trigger('click')
    await flushPromises()
    const details = wrapper.findAll('tbody tr')[1]!
    expect(harness.getDailyStats).toHaveBeenLastCalledWith(date, date, { page: 1, pageSize: 31, expand: true })
    if (usable) {
      expect(details.text()).toContain('18.00')
      expect(details.text()).toContain('11:59:00')
      expect(details.text()).toContain('新加坡时间')
      expect(details.text()).not.toContain('03:59:00')
      expect(details.text()).not.toContain('暂无法读取')
    } else {
      expect(details.text()).toContain('费用记录已经过期')
      expect(details.text()).not.toContain('18.00')
      expect(details.text()).not.toContain('沿用截至')
    }
    if (state === 'retained') {
      expect(details.text()).toContain('沿用截至')
      expect(details.text()).toContain(failure)
    } else {
      expect(details.text()).not.toContain('沿用截至')
      expect(details.text()).not.toContain(failure)
    }
  })

  it('keeps past-date amounts and original failure text', async () => {
    harness.getDailyStats.mockResolvedValue({ items: [{
      date: '2031-02-02', settlementStatus: 'final', operatingCost: 19, adjustedNetProfit: 11,
      siteCosts: [{ siteId: 'history', siteName: '历史账号', platform: 'sub2api', rechargeRate: 1,
        adjustedCost: 19, status: 'ok', source: 'dated_query', observedAt: '2031-02-02T03:59:00Z',
        lastAttemptStatus: 'failed', lastAttemptError: '原历史提示',
      }],
    }] })
    wrapper = mount(DailyStatsPanel)
    await flushPromises()
    await wrapper.find('tbody tr').trigger('click')
    await flushPromises()
    const details = wrapper.findAll('tbody tr')[1]!
    expect(details.text()).toContain('19.00')
    expect(details.text()).toContain('原历史提示')
    expect(details.text()).not.toContain('沿用截至')
    expect(details.text()).not.toContain('新加坡时间')
  })
})

describe('V3.1.6 daily stats use net revenue', () => {
  it('shows net revenue, the stored discount and a margin on net revenue', async () => {
    harness.getDailyStats.mockResolvedValue({ items: [{
      date: '2031-02-02', settlementStatus: 'final', todayProfit: 100, operatingCost: 46, adjustedNetProfit: 47,
      revenueDiscount: 6, revenueDiscountRate: 0.06,
      additionalCosts: { rechargeFee: 1.41, accountPurchase: 0, accountRefund: 0, promotion: 0, fixed: 0, adjustment: 0,
        total: 1.41, available: true, revenueDiscount: 6, revenueDiscountRate: 0.06, netRevenue: 94 },
    }, {
      date: '2031-02-01', settlementStatus: 'final', todayProfit: 80, operatingCost: 40, adjustedNetProfit: 40,
      additionalCosts: { rechargeFee: 1.28, accountPurchase: 0, accountRefund: 0, promotion: 0, fixed: 0, adjustment: 0, total: 1.28, available: true },
    }] })
    wrapper = mount(DailyStatsPanel)
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0]!.text()).toContain('94.00')
    expect(rows[0]!.text()).not.toContain('100.00')
    expect(rows[0]!.text()).toContain('50.0%')
    expect(rows[1]!.text()).toContain('80.00')
    expect(rows[1]!.text()).toContain('50.0%')
    await rows[0]!.trigger('click')
    await flushPromises()
    const details = wrapper.findAll('tbody tr')[1]!
    expect(details.text()).toContain('营收折扣（6%）：6.00；主站消费 100.00；营收折扣不计入成本。')
  })

  it('keeps net revenue when the cost summary of that day is unavailable', async () => {
    harness.getDailyStats.mockResolvedValue({ items: [{
      date: '2031-02-02', settlementStatus: 'partial', todayProfit: 100, revenueDiscount: 6, revenueDiscountRate: 0.06,
    }] })
    wrapper = mount(DailyStatsPanel)
    await flushPromises()
    const row = wrapper.findAll('tbody tr')[0]!
    expect(row.text()).toContain('94.00')
    expect(row.text()).not.toContain('100.00')
    await row.trigger('click')
    await flushPromises()
    expect(wrapper.findAll('tbody tr')[1]!.text()).toContain('营收折扣（6%）：6.00；主站消费 100.00')
  })
})

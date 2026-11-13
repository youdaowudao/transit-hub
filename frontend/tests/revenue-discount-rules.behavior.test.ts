// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

// V3.1.6 营收折扣率与手续费率规则管理：成本规则页和今日成本页的行为。
const api = vi.hoisted(() => ({
  createAdditionalCost: vi.fn(), getAdditionalCost: vi.fn(), updateAdditionalCost: vi.fn(), deleteAdditionalCost: vi.fn(),
  listAccountCostLedger: vi.fn(), getRechargeFeeRate: vi.fn(), listRechargeFeeRateHistory: vi.fn(), saveRechargeFeeRate: vi.fn(),
  listRevenueDiscountRates: vi.fn(), saveRevenueDiscountRate: vi.fn(), deleteRevenueDiscountRate: vi.fn(), deleteRechargeFeeRate: vi.fn(),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  ...api, createAccountBatch: vi.fn(), createAccountEvent: vi.fn(), getAccountAsset: vi.fn(),
  listAccountAssets: vi.fn().mockResolvedValue({ items: [], hasMore: false }), replaceAccountLink: vi.fn(), refreshAccountStats: vi.fn(),
}))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn().mockResolvedValue([]) }))

const businessDate = '2026-08-22'
const tomorrow = '2026-08-23'
const rule = (overrides: Record<string, unknown> = {}) => ({
  id: 'discount-1', effectiveDate: '2026-08-20', giftRate: 0.03, newUserRate: 0.015, referralRate: 0.02, totalRate: 0.065,
  createdAt: '2026-08-20T01:00:00Z', ...overrides,
})
const wrappers: VueWrapper[] = []
async function open(tab: 'today' | 'rules' = 'rules', props: Record<string, unknown> = {}) {
  const wrapper = mount(AccountCostWorkspace, {
    props: { open: false, initialTab: tab, businessDate, workspaceId: 'workspace-a', directCost: 20, operatingCost: 30, adjustedNetProfit: 70, ...props },
    global: { stubs: { Teleport: true } },
  })
  wrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}
function button(w: VueWrapper, name: string) {
  const match = w.findAll('button').find(b => b.attributes('aria-label') === name || b.text() === name)
  if (!match) throw new Error(`missing button ${name}`)
  return match
}
const input = (w: VueWrapper, label: string) => w.find(`input[aria-label="${label}"]`)
async function fillDiscount(w: VueWrapper, gift: string, newUser: string, referral: string, date = tomorrow) {
  await input(w, '充值赠送 %').setValue(gift)
  await input(w, '新用户福利 %').setValue(newUser)
  await input(w, '邀请返利 %').setValue(referral)
  await input(w, '折扣率生效日期').setValue(date)
}

beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
  api.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })
  api.getRechargeFeeRate.mockResolvedValue({ rate: 0.016, effectiveDate: businessDate })
  api.listRechargeFeeRateHistory.mockResolvedValue({ items: [] })
  api.listRevenueDiscountRates.mockResolvedValue({ items: [] })
  api.saveRevenueDiscountRate.mockImplementation(async (input: Record<string, unknown>) => ({ id: 'saved', ...input }))
  api.deleteRevenueDiscountRate.mockResolvedValue(undefined)
  api.deleteRechargeFeeRate.mockResolvedValue(undefined)
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks(); localStorage.clear() })

describe('revenue discount rules', () => {
  it('explains the empty state, totals three parts live and saves them as fractions from tomorrow', async () => {
    const w = await open()
    for (const text of ['营收折扣率（从营收中扣除）', '营收折扣不计入成本，不会修改主站', '不是活动面额', '还没有设置折扣率，营收按主站消费原值计算。'])
      expect(w.text()).toContain(text)
    await fillDiscount(w, '3', '1.5', '2')
    expect(w.text()).toContain('合计折扣率 6.5%')
    await button(w, '保存折扣率').trigger('click'); await flushPromises()
    expect(api.saveRevenueDiscountRate).toHaveBeenCalledWith({ effectiveDate: tomorrow, giftRate: 0.03, newUserRate: 0.015, referralRate: 0.02 })
    expect(w.emitted('updated')).toHaveLength(1)
    expect(w.text()).toContain('折扣率已保存，从 2026-08-23 起按 6.5% 扣除营收折扣；之前的日期不受影响，不会修改主站。')
    expect(api.listRevenueDiscountRates).toHaveBeenCalledTimes(2)
  })

  it.each([
    { name: 'past effective date', values: ['3', '1', '2', '2026-08-21'], text: '生效日期最早为明天' },
    { name: 'effective today', values: ['3', '1', '2', businessDate], text: '生效日期最早为明天（今天和之前的日期不变）' },
    { name: 'total reaching 100%', values: ['50', '30', '20', tomorrow], text: '合计必须小于 100%' },
    { name: 'negative part', values: ['-1', '1', '2', tomorrow], text: '每项需在 0 至 100% 之间' },
    { name: 'empty part', values: ['3', '', '2', tomorrow], text: '每项需在 0 至 100% 之间' },
  ])('blocks $name before calling the server', async ({ values, text }) => {
    const w = await open()
    await fillDiscount(w, values[0]!, values[1]!, values[2]!, values[3]!)
    await button(w, '保存折扣率').trigger('click'); await flushPromises()
    expect(api.saveRevenueDiscountRate).not.toHaveBeenCalled()
    expect(w.text()).toContain('折扣率尚未保存')
    expect(w.text()).toContain(text)
    expect(w.emitted('updated')).toBeUndefined()
  })

  it('explains a server-side date refusal in plain words and keeps the old rule', async () => {
    const w = await open()
    await fillDiscount(w, '3', '1', '2')
    api.saveRevenueDiscountRate.mockRejectedValueOnce(new Error('dashboard.revenueDiscount.errors.pastDate'))
    await button(w, '保存折扣率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('折扣率保存未执行：生效日期最早为明天（今天和之前的日期不变），请改为明天或以后。原规则未改变')
    expect(w.text()).not.toContain('dashboard.revenueDiscount')
    expect(w.emitted('updated')).toBeUndefined()
  })

  it('lists history with three parts, prefills the rule in effect from tomorrow and edits a future rule into the form', async () => {
    api.listRevenueDiscountRates.mockResolvedValue({ items: [
      rule({ id: 'future', effectiveDate: '2026-08-25', giftRate: 0.04, newUserRate: 0.01, referralRate: 0.02, totalRate: 0.07 }),
      rule(),
    ] })
    const w = await open()
    expect(w.text()).toContain('2026-08-20 起 · 合计 6.5% · 充值赠送 3% · 新用户福利 1.5% · 邀请返利 2%')
    expect(w.text()).toContain('2026-08-25 起 · 合计 7% · 充值赠送 4% · 新用户福利 1% · 邀请返利 2%')
    expect(input(w, '充值赠送 %').element).toHaveProperty('value', '3')
    expect(input(w, '新用户福利 %').element).toHaveProperty('value', '1.5')
    expect(input(w, '折扣率生效日期').element).toHaveProperty('value', tomorrow)
    await button(w, '编辑 2026-08-25 起的折扣率').trigger('click')
    expect(input(w, '充值赠送 %').element).toHaveProperty('value', '4')
    expect(input(w, '折扣率生效日期').element).toHaveProperty('value', '2026-08-25')
    expect(w.text()).toContain('已把这组比率填入上方表单；生效日期最早为明天，保存后从该日期起生效。')
    expect(api.saveRevenueDiscountRate).not.toHaveBeenCalled()
  })

  it('deletes a rule that has not taken effect only after confirmation', async () => {
    api.listRevenueDiscountRates.mockResolvedValue({ items: [rule({ effectiveDate: tomorrow })] })
    const w = await open()
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(w, '删除 2026-08-23 起的折扣率').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenLastCalledWith(expect.stringContaining('删除 2026-08-23 起的营收折扣率（合计 6.5%）？删除后从该日期起改用更早的一条，没有则不扣折扣'))
    expect(api.deleteRevenueDiscountRate).not.toHaveBeenCalled()
    await button(w, '删除 2026-08-23 起的折扣率').trigger('click'); await flushPromises()
    expect(api.deleteRevenueDiscountRate).toHaveBeenCalledWith('discount-1')
    expect(w.emitted('updated')).toHaveLength(1)
    expect(w.text()).toContain('折扣率已删除；已结算的日期不重算，不会修改主站。')
  })

  it('offers no delete for rules in effect today or earlier and explains how to stop them', async () => {
    api.listRevenueDiscountRates.mockResolvedValue({ items: [rule({ id: 'today', effectiveDate: businessDate }), rule()] })
    const w = await open()
    for (const date of [businessDate, '2026-08-20']) {
      expect(w.findAll('button').some(b => b.attributes('aria-label') === `删除 ${date} 起的折扣率`)).toBe(false)
      expect(button(w, `编辑 ${date} 起的折扣率`).exists()).toBe(true)
    }
    expect(w.text().split('已经生效，不能删除；要停用，请新增一条从明天起生效的折扣率（可都填 0）。').length - 1).toBe(2)
  })

  it('explains the server refusal when a rule took effect before the page noticed the date change', async () => {
    api.listRevenueDiscountRates.mockResolvedValue({ items: [rule({ effectiveDate: tomorrow })] })
    api.deleteRevenueDiscountRate.mockRejectedValueOnce(new Error('dashboard.revenueDiscount.errors.locked'))
    const w = await open()
    await button(w, '删除 2026-08-23 起的折扣率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('折扣率删除未执行：这条折扣率已经生效，删除会改变今天或过去日期的营收，所以不能删除')
    expect(w.text()).not.toContain('折扣率已删除')
    expect(w.emitted('updated')).toBeUndefined()
  })

  it('shows a plain failure when discount history cannot load', async () => {
    api.listRevenueDiscountRates.mockRejectedValue(new Error('折扣历史连接中断'))
    const w = await open()
    expect(w.text()).toContain('折扣率历史加载失败')
    expect(w.text()).toContain('折扣历史连接中断')
  })
})

describe('recharge fee rules', () => {
  const fee = { id: 'fee-1', effectiveDate: '2026-08-01', rate: 0.018, createdAt: '2026-08-01T01:00:00Z' }

  it('fills the form when editing and deletes only after confirmation', async () => {
    api.listRechargeFeeRateHistory.mockResolvedValue({ items: [fee] })
    const w = await open()
    expect(w.text()).toContain('按当天实收营收（主站消费扣除营收折扣后）× 费率估算手续费')
    expect(w.text()).toContain('要改生效日期，请删除旧的再新增')
    await button(w, '编辑 2026-08-01 起的手续费率').trigger('click')
    expect(input(w, '费率 %').element).toHaveProperty('value', '1.8')
    expect(input(w, '费率生效日期').element).toHaveProperty('value', '2026-08-01')
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(w, '删除 2026-08-01 起的手续费率').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenLastCalledWith(expect.stringContaining('删除 2026-08-01 起的手续费率 1.8%？删除后从该日期起改用更早的一条，没有则按默认 1.6%'))
    expect(api.deleteRechargeFeeRate).not.toHaveBeenCalled()
    await button(w, '删除 2026-08-01 起的手续费率').trigger('click'); await flushPromises()
    expect(api.deleteRechargeFeeRate).toHaveBeenCalledWith('fee-1')
    expect(w.text()).toContain('费率已删除；已结算的日期不重算，不会修改主站。')
  })

  it('explains a missing fee rule without claiming it was deleted', async () => {
    api.listRechargeFeeRateHistory.mockResolvedValue({ items: [fee] })
    api.deleteRechargeFeeRate.mockRejectedValueOnce(new Error('dashboard.additionalCost.errors.feeRateNotFound'))
    const w = await open()
    await button(w, '删除 2026-08-01 起的手续费率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('费率删除未执行：这条规则已不存在或不属于当前工作区')
    expect(w.text()).not.toContain('费率已删除')
    expect(w.emitted('updated')).toBeUndefined()
  })
})

describe('today tab revenue explanation', () => {
  const summary = {
    rechargeFee: 1.5, accountPurchase: 0, accountRefund: 0, promotion: 0, fixed: 0, adjustment: 0, total: 1.5, available: true,
  }

  it('explains how net revenue is derived from the three discount parts', async () => {
    const w = await open('today', {
      todayRevenue: 93.5,
      summary: { ...summary, revenueDiscount: 6.5, revenueDiscountRate: 0.065, netRevenue: 93.5, revenueDiscountRule: rule() },
    })
    expect(w.text()).toContain('营收 93.50 = 主站消费 100.00 − 营收折扣 6.50（合计 6.5%：充值赠送 3%、新用户福利 1.5%、邀请返利 2%）。营收折扣不计入成本。')
    expect(w.text()).toContain('营收 = 主站消费 − 营收折扣')
    expect(w.text()).not.toContain('今天没有生效的营收折扣率')
  })

  it('says no discount applies when no rule is set, and offers no promotion type for new costs', async () => {
    const w = await open('today', { todayRevenue: 100, summary })
    expect(w.text()).toContain('今天没有生效的营收折扣率，营收按主站消费原值计算；可在‘成本规则’设置，最早明天起生效。')
    expect(w.find('form select').findAll('option').map(option => option.text())).toEqual(['固定费用', '手工调整'])
  })
})

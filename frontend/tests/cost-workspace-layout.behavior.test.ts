// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

// V3.1.9 成本抽屉整理：结果在上、设置收起；成本规则按手续费、折扣率、赠送记录排列。
const api = vi.hoisted(() => ({
  createAdditionalCost: vi.fn(), getAdditionalCost: vi.fn(), updateAdditionalCost: vi.fn(), deleteAdditionalCost: vi.fn(),
  listAccountCostLedger: vi.fn(), getRechargeFeeRate: vi.fn(), listRechargeFeeRateHistory: vi.fn(), saveRechargeFeeRate: vi.fn(),
  listRevenueDiscountRates: vi.fn(), saveRevenueDiscountRate: vi.fn(), deleteRevenueDiscountRate: vi.fn(), deleteRechargeFeeRate: vi.fn(),
  getGiftLedger: vi.fn(), checkGiftLedgerCustomer: vi.fn(), createGiftLedgerRecord: vi.fn(), updateGiftLedgerRecord: vi.fn(), deleteGiftLedgerRecord: vi.fn(),
  listAccountAssets: vi.fn(), getAccountAsset: vi.fn(), createAccountEvent: vi.fn(), replaceAccountLink: vi.fn(),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({ ...api, createAccountBatch: vi.fn(), refreshAccountStats: vi.fn() }))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn().mockResolvedValue([]) }))

const businessDate = '2026-08-22'
const fee = (overrides: Record<string, unknown> = {}) => ({ id: 'fee-1', effectiveDate: '2026-08-01', rate: 0.018, createdAt: '2026-08-01T00:00:00Z', ...overrides })
const discount = (overrides: Record<string, unknown> = {}) => ({
  id: 'discount-1', effectiveDate: '2026-08-20', giftRate: 0.015, newUserRate: 0.005, referralRate: 0.03, totalRate: 0.05, createdAt: '2026-08-20T00:00:00Z', ...overrides,
})
const giftRecord = (id: string, mainUserId: string) => ({
  id, mainUserId, email: `${mainUserId}@example.com`, rechargeDate: '2026-08-20', paidAmount: 100, giftAmount: 20,
  creditAmount: 120, giftShare: 0.1667, note: '', usedAmount: 10, state: 'using',
})
const customer = (mainUserId: string, overrides: Record<string, unknown> = {}) => ({
  mainUserId, email: `${mainUserId}@example.com`, creditedTotal: 120, usedTotal: 10, remaining: 110,
  todayConsumption: 0, todayCovered: 0, todayUncovered: 0, todayGift: 0, mainBalance: 200,
  records: [giftRecord(`r-${mainUserId}`, mainUserId)], ...overrides,
})
const asset = { id: 'asset-1', identifier: '验收账号', platform: 'openai', channel: '渠道', accountType: 'plus', currentStatus: 'active', statsMode: 'manual', statsQuality: 'complete', hasActiveLink: false, upstreamReferenceUrl: '', batchId: 'batch-1', purchaseCostCents: 1000, quotaTotalMicros: null, performance: null }
const assetDetail = {
  asset, batch: { purchaseUrl: '' }, links: [], events: [], dailyStats: [],
  performance: { averageSaleMultiplier: null, costRecoveryMultiple: null, finalProfitCents: null, breakevenDifferenceCents: null, roi: null, missingFields: [] },
}

const wrappers: VueWrapper[] = []
async function open(tab: 'today' | 'assets' | 'ledger' | 'rules', props: Record<string, unknown> = {}) {
  const wrapper = mount(AccountCostWorkspace, {
    props: { open: false, initialTab: tab, businessDate, workspaceId: 'workspace-a', directCost: 20, operatingCost: 30, adjustedNetProfit: 70, ...props },
    global: { stubs: { Teleport: true } },
  })
  wrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}
const findButton = (w: VueWrapper, name: string) => w.findAll('button').find(b => b.text() === name || b.attributes('aria-label') === name)
async function click(w: VueWrapper, name: string) {
  const target = findButton(w, name)
  if (!target) throw new Error(`missing button ${name}`)
  await target.trigger('click'); await flushPromises()
}
const input = (w: VueWrapper, label: string) => w.find(`input[aria-label="${label}"]`)

beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
  api.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })
  api.getRechargeFeeRate.mockResolvedValue({ rate: 0.018, effectiveDate: businessDate })
  api.listRechargeFeeRateHistory.mockResolvedValue({ items: [] })
  api.listRevenueDiscountRates.mockResolvedValue({ items: [] })
  api.saveRechargeFeeRate.mockResolvedValue({ rate: 0.02, effectiveDate: businessDate })
  api.getGiftLedger.mockResolvedValue({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers: [] })
  api.listAccountAssets.mockResolvedValue({ items: [asset], hasMore: false })
  api.getAccountAsset.mockResolvedValue(assetDetail)
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks(); localStorage.clear() })

describe('today tab: results first, the entry form opens on demand', () => {
  it('keeps the cost form collapsed when opened from a result card and opens it from 记一笔成本', async () => {
    const w = await open('today')
    expect(w.find('input[placeholder="金额"]').exists()).toBe(false)
    expect(w.text()).toContain('固定费用、手工调整等本站额外费用点“记一笔成本”填写；买号请点“录入买号”。')
    await click(w, '记一笔成本')
    expect(w.find('input[placeholder="金额"]').exists()).toBe(true)
    await click(w, '收起')
    expect(w.find('input[placeholder="金额"]').exists()).toBe(false)
  })

  it('opens the entry form directly when entered from the homepage 记一笔成本 button', async () => {
    const w = await open('today', { openCostForm: true })
    expect(w.find('input[placeholder="金额"]').exists()).toBe(true)
  })

  it('lists only non-zero cost lines and merges the zero ones into one sentence', async () => {
    const w = await open('today', { summary: { available: true, accountQuality: 'complete', accountPurchase: 5, accountRefund: 0, rechargeFee: 1.2, promotion: 0, fixed: 0, adjustment: 0, total: 26.2 } })
    const text = w.text()
    const listed = w.findAll('div.justify-between.border-b').map(row => row.find('span').text())
    expect(listed).toEqual(['上游直接成本', '替代成本扣减', '买号确认成本', '充值手续费'])
    expect(text).toContain('其余为 0：退款冲减、活动摊销、固定费用、手工调整')
    expect(text).toContain('账号统计质量：完整')
    expect(w.find('details').text()).toContain('营收 = 主站消费 − 营收折扣')
  })
})

describe('rules tab: fee, discount, gift records in order with current results first', () => {
  it('orders the three blocks fee, discount, gift and keeps every form collapsed', async () => {
    const w = await open('rules')
    const ids = w.findAll('[data-testid="rule-fee"], [data-testid="rule-discount"], [data-testid="gift-ledger-section"]').map(node => node.attributes('data-testid'))
    expect(ids).toEqual(['rule-fee', 'rule-discount', 'gift-ledger-section'])
    for (const label of ['费率 %', '充值赠送 %', '主站用户 ID']) expect(input(w, label).exists()).toBe(false)
    for (const name of ['修改费率', '修改折扣率', '新增一笔']) expect(findButton(w, name)).toBeDefined()
  })

  it('shows fee and discount history open as the result instead of deriving a current value', async () => {
    api.listRechargeFeeRateHistory.mockResolvedValue({ items: [fee(), fee({ id: 'fee-next', effectiveDate: '2026-09-01', rate: 0.02 })] })
    api.listRevenueDiscountRates.mockResolvedValue({ items: [discount()] })
    const w = await open('rules')
    expect(w.find('[data-testid="rule-fee-history"]').attributes('open')).toBeDefined()
    expect(w.find('[data-testid="rule-discount-history"]').attributes('open')).toBeDefined()
    expect(w.find('[data-testid="rule-fee-history"]').text()).toContain('费率生效历史（2 条）')
    expect(w.find('[data-testid="rule-fee-history"]').text()).toContain('2026-09-01')
    expect(w.find('[data-testid="rule-discount-history"]').text()).toContain('2026-08-20 起 · 合计 5%')
    expect(w.text()).toContain('没有设置时按默认 1.6%')
    for (const claim of ['现在按', '现在合计', '起改为']) expect(w.text()).not.toContain(claim)
  })

  it('never claims a current rate when the history cannot be read or a reload after saving fails', async () => {
    api.listRechargeFeeRateHistory.mockResolvedValueOnce({ items: [fee()] }).mockRejectedValueOnce(new Error('读取费率历史时数据库连接中断'))
    api.listRevenueDiscountRates.mockResolvedValueOnce({ items: [discount()] }).mockRejectedValueOnce(new Error('读取折扣率历史时数据库连接中断'))
    api.getRechargeFeeRate.mockResolvedValueOnce({ rate: 0.018, effectiveDate: businessDate }).mockRejectedValueOnce(new Error('当前费率读取中断'))
    const w = await open('rules')
    await click(w, '修改费率')
    await input(w, '费率 %').setValue('2')
    await click(w, '保存费率')
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
    expect(w.text()).toContain('费率历史加载失败')
    expect(w.text()).toContain('折扣率历史加载失败')
    for (const claim of ['现在按', '现在合计', '还没有生效的费率设置']) expect(w.text()).not.toContain(claim)
  })

  it('opens only one rule form at a time and drops a hidden gift edit', async () => {
    api.getGiftLedger.mockResolvedValue({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers: [customer('123')] })
    const w = await open('rules')
    await click(w, '编辑 123@example.com 2026-08-20 的记录')
    expect(w.text()).toContain('正在编辑 123@example.com 2026-08-20 的记录')
    await click(w, '修改费率')
    expect(input(w, '费率 %').exists()).toBe(true)
    expect(input(w, '主站用户 ID').exists()).toBe(false)
    expect(w.text()).not.toContain('正在编辑')
    await click(w, '新增一笔')
    expect(input(w, '费率 %').exists()).toBe(false)
    expect((input(w, '主站用户 ID').element as HTMLInputElement).value).toBe('')
    expect(findButton(w, '保存修改')).toBeUndefined()
  })

  it('closes the fee form after a confirmed save and shows the result', async () => {
    const w = await open('rules')
    await click(w, '修改费率')
    await click(w, '保存费率')
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
    expect(input(w, '费率 %').exists()).toBe(false)
    expect(w.text()).toContain('费率已保存')
  })

  it('keeps the reload entry after an unconfirmed gift save when forms are collapsed or switched', async () => {
    const w = await open('rules')
    await click(w, '新增一笔')
    await input(w, '主站用户 ID').setValue('123')
    await input(w, '赠送金额').setValue('20')
    api.createGiftLedgerRecord.mockRejectedValueOnce(new Error('admin.dashboard.adminAuth.errors.network'))
    await click(w, '保存记录')
    expect(w.text()).toContain('记录保存未能确认')
    for (const name of ['收起', '修改费率', '新增一笔']) {
      await click(w, name)
      expect(w.find('[data-testid="error-reload"]').exists()).toBe(true)
      expect(w.text()).toContain('记录保存未能确认')
    }
    expect((input(w, '主站用户 ID').element as HTMLInputElement).value).toBe('123')
    const reads = api.getGiftLedger.mock.calls.length
    await w.find('[data-testid="error-reload"]').trigger('click'); await flushPromises()
    expect(api.getGiftLedger.mock.calls.length).toBe(reads + 1)
    expect(api.createGiftLedgerRecord).toHaveBeenCalledTimes(1)
  })

  it('keeps a confirmed fee save and its reload button when the form is toggled after the history refresh fails', async () => {
    api.listRechargeFeeRateHistory.mockResolvedValueOnce({ items: [] }).mockRejectedValueOnce(new Error('读取费率历史时数据库连接中断'))
    const w = await open('rules')
    await click(w, '修改费率')
    await click(w, '保存费率')
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
    expect(w.text()).toContain('费率已保存，但规则及历史更新失败')
    for (const name of ['修改费率', '收起', '修改折扣率']) {
      await click(w, name)
      expect(w.text()).toContain('费率已保存，但规则及历史更新失败')
      expect(findButton(w, '重新加载费用')).toBeDefined()
    }
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
  })

  it('shows one collapsed row per gift customer and folds customers beyond the first five', async () => {
    const customers = ['101', '102', '103', '104', '105', '106', '107'].map(id => customer(id))
    api.getGiftLedger.mockResolvedValue({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers })
    const w = await open('rules')
    expect(w.find('[data-testid="gift-ledger-summary"]').text()).toContain('7 位客户 · 7 笔记录')
    const rows = () => w.findAll('[data-testid="gift-ledger-customer"]')
    expect(rows()).toHaveLength(5)
    expect(rows()[0]!.element.tagName).toBe('DETAILS')
    expect(rows()[0]!.attributes('open')).toBeUndefined()
    expect(rows()[0]!.find('summary').text()).toContain('101@example.com（主站 ID 101）')
    expect(rows()[0]!.find('summary').text()).toContain('1 笔 · 剩余 110.00 / 到账 120.00')
    await click(w, '展开其余 2 位客户')
    expect(rows()).toHaveLength(7)
    await click(w, '只显示前 5 位客户')
    expect(rows()).toHaveLength(5)
  })

  it('shows every customer when one beyond the first five has a balance warning', async () => {
    const customers = ['301', '302', '303', '304', '305'].map(id => customer(id)).concat([customer('306', { mainBalance: 10 })])
    api.getGiftLedger.mockResolvedValue({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers })
    const w = await open('rules')
    const rows = w.findAll('[data-testid="gift-ledger-customer"]')
    expect(rows).toHaveLength(6)
    expect(rows[5]!.attributes('open')).toBeDefined()
    expect(rows[5]!.text()).toContain('主站余额比按记录剩余少')
    expect(findButton(w, '展开其余 1 位客户')).toBeUndefined()
  })

  it('opens a customer row when the main-site balance is lower than the recorded remainder', async () => {
    api.getGiftLedger.mockResolvedValue({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers: [customer('201', { mainBalance: 10 })] })
    const w = await open('rules')
    expect(w.find('[data-testid="gift-ledger-customer"]').attributes('open')).toBeDefined()
  })
})

describe('ledger and asset tabs keep settings out of the way', () => {
  it('shows date and type filters and folds the rest into 更多筛选, opened when one is set', async () => {
    const w = await open('ledger')
    const more = () => w.findAll('details').find(node => node.find('summary').text().includes('更多筛选'))!
    expect(more().attributes('open')).toBeUndefined()
    expect(input(w, '查询开始日期').exists()).toBe(true)
    await more().find('input[placeholder="平台"]').setValue('openai'); await flushPromises()
    expect(more().attributes('open')).toBeDefined()
  })

  it('keeps the link and lifecycle forms collapsed on an asset until asked', async () => {
    const w = await open('assets')
    await click(w, '查看 / 操作')
    expect(w.text()).toContain('验收账号')
    expect(w.text()).toContain('来源与关联')
    expect(w.find('form[class*="space-y-3"]').exists()).toBe(false)
    await click(w, '补充或更换上游关联')
    expect(w.text()).toContain('保存关联')
    expect(w.text()).not.toContain('保存事件')
    await click(w, '补充生命周期记录')
    expect(w.text()).toContain('保存事件')
    expect(w.text()).not.toContain('保存关联')
    await click(w, '收起记录表单')
    expect(w.text()).not.toContain('保存事件')
  })
})

// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

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
const source = (type = 'fixed', name = '隔离费用') => ({
  id: `${type}-0`, sourceId: `${type}-source`, type, name, businessDate: '2026-08-20',
  amount: 33.33, amountCents: 3333, originalAmount: 100, usageRate: 0.8, days: 3, estimated: type === 'promotion', createdAt: '2026-08-20T01:00:00Z',
})
const fullSource = (type = 'fixed', name = '隔离费用') => [0, 1, 2].map((day) => ({
  ...source(type, name), id: `${type}-${day}`, businessDate: `2026-08-${20 + day}`, amount: day === 2 ? 33.34 : 33.33,
}))
const wrappers: VueWrapper[] = []
async function open(tab: 'today' | 'ledger' | 'rules' = 'today') {
  const wrapper = mount(AccountCostWorkspace, {
    props: { open: false, initialTab: tab, businessDate, workspaceId: 'workspace-a', directCost: 20, operatingCost: 30, adjustedNetProfit: 70 },
    global: { stubs: { Teleport: true } },
  })
  wrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}
function button(w: VueWrapper, name: string) {
  const match = w.findAll('button').find(b => b.text().includes(name) || b.attributes('aria-label') === name)
  if (!match) throw new Error(`missing button ${name}`)
  return match
}
const amount = (w: VueWrapper) => w.find('input[placeholder="金额"]')
async function enter(w: VueWrapper, value = '100') {
  await w.find('input[placeholder="名称"]').setValue('新增隔离费用')
  await amount(w).setValue(value)
}
beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
  api.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })
  api.getAdditionalCost.mockResolvedValue({ items: fullSource() })
  api.createAdditionalCost.mockResolvedValue({ items: fullSource() })
  api.updateAdditionalCost.mockResolvedValue({ items: fullSource() })
  api.deleteAdditionalCost.mockResolvedValue(undefined)
  api.getRechargeFeeRate.mockResolvedValue({ rate: 0.016, effectiveDate: businessDate })
  api.listRechargeFeeRateHistory.mockResolvedValue({ items: [] })
  api.saveRechargeFeeRate.mockResolvedValue({ rate: 0.02, effectiveDate: businessDate })
  api.listRevenueDiscountRates.mockResolvedValue({ items: [] })
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks(); localStorage.clear() })

describe('manual cost management critical regressions', () => {
  it('makes ledger search and manual mutations mutually exclusive', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: true })
    const w = await open('ledger')
    let resolve!: (value: { items: ReturnType<typeof fullSource>; hasMore: boolean }) => void
    api.listAccountCostLedger.mockReturnValueOnce(new Promise(r => { resolve = r }))
    await button(w, '查询账本').trigger('click')
    expect(button(w, '删除 隔离费用').attributes('disabled')).toBeDefined()
    expect(button(w, '编辑 隔离费用').attributes('disabled')).toBeDefined()
    await button(w, '删除 隔离费用').trigger('click')
    expect(api.getAdditionalCost).not.toHaveBeenCalled()
    resolve({ items: fullSource(), hasMore: false }); await flushPromises()
    expect(button(w, '删除 隔离费用').attributes('disabled')).toBeUndefined()
  })

  it('does not restore a deleted row when a query from before close resolves after the new empty ledger', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    const w = await open('ledger')
    let resolve!: (value: { items: ReturnType<typeof fullSource>; hasMore: boolean }) => void
    api.listAccountCostLedger.mockReturnValueOnce(new Promise(r => { resolve = r }))
    await button(w, '查询账本').trigger('click')
    api.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })
    await w.setProps({ open: false }); await w.setProps({ open: true }); await flushPromises()
    expect(w.text()).toContain('所选范围暂无记录')
    resolve({ items: fullSource(), hasMore: false }); await flushPromises()
    expect(w.text()).toContain('所选范围暂无记录')
    expect(w.text()).not.toContain('隔离费用')
    expect(button(w, '查询账本').attributes('disabled')).toBeUndefined()
  })

  it('does not redirect a failed fee-history reload to the ledger after changing tabs', async () => {
    const w = await open('rules')
    api.listRechargeFeeRateHistory.mockRejectedValueOnce(new Error('费率历史中断'))
    await button(w, '保存费率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('费率已保存')
    await button(w, '今日成本').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain('费率已保存')
    expect(w.findAll('[role="status"]').some(s => s.text().includes('重新加载费用'))).toBe(false)
    const calls = api.listRechargeFeeRateHistory.mock.calls.length
    await button(w, '成本规则').trigger('click'); await flushPromises()
    expect(api.listRechargeFeeRateHistory).toHaveBeenCalledTimes(calls + 1)
  })

  it('does not relabel a failed cost-list reload as a fee-history reload after changing tabs', async () => {
    const w = await open(); await enter(w)
    api.listAccountCostLedger.mockRejectedValueOnce(new Error('费用列表中断'))
    await w.find('form').trigger('submit'); await flushPromises()
    expect(w.text()).toContain('费用已保存')
    await button(w, '成本规则').trigger('click'); await flushPromises()
    expect(w.text()).not.toContain('费用已保存')
    expect(w.findAll('[role="status"]').some(s => s.text().includes('重新加载费用'))).toBe(false)
  })

  it('clears an earlier successful cost notice before rejecting a new invalid draft', async () => {
    const w = await open(); await enter(w)
    await w.find('form').trigger('submit'); await flushPromises()
    expect(w.text()).toContain('费用已保存')
    await enter(w, '-1'); await w.find('form').trigger('submit'); await flushPromises()
    expect(w.text()).toContain('费用尚未保存')
    expect(w.text()).not.toContain('费用已保存')
    expect(amount(w).element).toHaveProperty('value', '-1')
    expect(api.createAdditionalCost).toHaveBeenCalledOnce()
  })

  it('clears an earlier successful fee notice before rejecting a new invalid rate', async () => {
    const w = await open('rules')
    await button(w, '保存费率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('费率已保存')
    await w.find('input[placeholder="费率 %"]').setValue('101')
    await button(w, '保存费率').trigger('click'); await flushPromises()
    expect(w.text()).toContain('费率尚未保存')
    expect(w.text()).not.toContain('费率已保存')
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
  })

  it('clears the prior receipt when a new draft is invalid before native form submission can run', async () => {
    const w = await open(); await enter(w)
    await w.find('form').trigger('submit'); await flushPromises()
    expect(w.text()).toContain('费用已保存')
    await enter(w)
    await w.find('input[placeholder="分摊天数"]').setValue('0')
    expect((w.find('form').element as HTMLFormElement).checkValidity()).toBe(false)
    expect(w.text()).not.toContain('费用已保存')
    expect(api.createAdditionalCost).toHaveBeenCalledOnce()
  })

  it('ignores a late deletion-source read error after forced close while releasing its lock', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    let reject!: (error: Error) => void
    api.getAdditionalCost.mockReturnValueOnce(new Promise((_resolve, r) => { reject = r }))
    const w = await open()
    await button(w, '删除 隔离费用').trigger('click')
    await w.setProps({ open: false }); await w.setProps({ open: true }); await flushPromises()
    reject(new Error('已关闭读取的旧错误')); await flushPromises()
    expect(w.text()).not.toContain('已关闭读取的旧错误')
    expect(button(w, '保存记录').attributes('disabled')).toBeUndefined()
    expect(api.deleteAdditionalCost).not.toHaveBeenCalled()
    expect(window.confirm).not.toHaveBeenCalled()
  })

  it('keeps generated and asset-linked costs read-only in both views', async () => {
    const records = [source('account_purchase', '系统买号成本'), source('account_refund', '系统退款成本'),
      { ...source('fixed', '关联批次费用'), batchId: 'batch-1' },
      { ...source('promotion', '关联账号费用'), accountAssetId: 'asset-1' }]
    api.listAccountCostLedger.mockResolvedValue({ items: records, hasMore: false })
    const w = await open()
    for (const tab of ['今日成本', '成本账本']) {
      await button(w, tab).trigger('click'); await flushPromises()
      for (const record of records) {
        expect(w.text()).toContain(record.name)
        expect(w.findAll('button').some(b => b.text().includes(`编辑 ${record.name}`))).toBe(false)
        expect(w.findAll('button').some(b => b.text().includes(`删除 ${record.name}`))).toBe(false)
      }
    }
    expect(api.getAdditionalCost).not.toHaveBeenCalled()
    expect(api.deleteAdditionalCost).not.toHaveBeenCalled()
  })

  it.each(['fixed', 'promotion', 'adjustment'])('offers edit and whole-entry deletion beside %s in today and ledger', async type => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source(type)], hasMore: false })
    const w = await open()
    expect(button(w, '编辑 隔离费用').exists()).toBe(true)
    expect(button(w, '删除 隔离费用').exists()).toBe(true)
    await button(w, '成本账本').trigger('click'); await flushPromises()
    expect(button(w, '编辑 隔离费用').exists()).toBe(true)
    expect(button(w, '删除 隔离费用').exists()).toBe(true)
    expect(w.text()).toContain('所选日期计入')
    expect(w.text()).toContain('原始输入金额')
  })

  it('closing an existing edit cannot turn its old values into a new cost', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    const w = await open('ledger')
    await button(w, '编辑 隔离费用').trigger('click'); await flushPromises()
    expect(amount(w).element).toHaveProperty('value', '100')
    await w.setProps({ open: false }); await w.setProps({ initialTab: 'today', open: true }); await flushPromises()
    expect(amount(w).element).toHaveProperty('value', '')
    expect(w.find('input[type="date"]').element).toHaveProperty('value', businessDate)
    await enter(w, '12'); await w.find('form').trigger('submit'); await flushPromises()
    expect(api.updateAdditionalCost).not.toHaveBeenCalled()
    expect(api.createAdditionalCost).toHaveBeenCalledWith(expect.objectContaining({ businessDate, days: 30, amount: 12 }))
  })

  it.each(['ledger', 'today'] as const)('keeps a confirmed save successful when the %s reload fails and retry only reads', async failing => {
    const w = await open(); await enter(w)
    if (failing === 'ledger') api.listAccountCostLedger.mockRejectedValueOnce(new Error('账本连接中断'))
    else api.listAccountCostLedger.mockResolvedValueOnce({ items: [], hasMore: false }).mockRejectedValueOnce(new Error('今日明细连接中断'))
    await w.find('form').trigger('submit'); await flushPromises()
    expect(api.createAdditionalCost).toHaveBeenCalledOnce()
    expect(w.emitted('updated')).toHaveLength(1)
    expect(w.text()).toContain('费用已保存')
    expect(w.text()).not.toContain('保存失败')
    expect(amount(w).element).toHaveProperty('value', '')
    await button(w, '重新加载费用').trigger('click'); await flushPromises()
    expect(api.createAdditionalCost).toHaveBeenCalledOnce()
    expect(api.updateAdditionalCost).not.toHaveBeenCalled()
  })

  it('reads the full period before deletion, cancel keeps everything and confirm deletes once', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [fullSource()[2]], hasMore: false })
    const w = await open()
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(w, '删除 隔离费用').trigger('click'); await flushPromises()
    expect(api.getAdditionalCost).toHaveBeenCalledWith('fixed-source')
    const text = vi.mocked(window.confirm).mock.calls[0][0] ?? ''
    for (const part of ['隔离费用', '100', '2026-08-20', '2026-08-22', '3', '不会修改主站']) expect(text).toContain(part)
    expect(api.deleteAdditionalCost).not.toHaveBeenCalled()
    await button(w, '删除 隔离费用').trigger('click'); await flushPromises()
    expect(api.deleteAdditionalCost).toHaveBeenCalledExactlyOnceWith('fixed-source')
    expect(w.emitted('updated')).toHaveLength(1)
  })

  it('does not misreport deletion when its follow-up list read fails', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    const w = await open()
    api.deleteAdditionalCost.mockImplementation(async () => { api.listAccountCostLedger.mockRejectedValueOnce(new Error('账本暂不可读')) })
    await button(w, '删除 隔离费用').trigger('click'); await flushPromises()
    expect(w.text()).toContain('费用已删除')
    await button(w, '重新加载费用').trigger('click'); await flushPromises()
    expect(api.deleteAdditionalCost).toHaveBeenCalledOnce()
    expect(w.text()).not.toContain('删除失败')
  })

  it('serializes full-source reads and releases the lock after forced close without refilling old data', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    let resolve!: (value: { items: ReturnType<typeof fullSource> }) => void
    api.getAdditionalCost.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const w = await open('ledger')
    await button(w, '编辑 隔离费用').trigger('click')
    expect(button(w, '编辑 隔离费用').attributes('disabled')).toBeDefined()
    await w.setProps({ open: false }); await w.setProps({ open: true, initialTab: 'today' }); await flushPromises()
    expect(button(w, '保存记录').attributes('disabled')).toBeDefined()
    expect(button(w, '查看上游成本明细').attributes('disabled')).toBeDefined()
    resolve({ items: fullSource() }); await flushPromises()
    expect(amount(w).element).toHaveProperty('value', '')
    expect(w.text()).not.toContain('编辑手工成本')
    expect(button(w, '保存记录').attributes('disabled')).toBeUndefined()
    expect(api.createAdditionalCost).not.toHaveBeenCalled()
  })

  it('releases a pending write after forced close without replaying it', async () => {
    let resolve!: (value: { items: ReturnType<typeof fullSource> }) => void
    api.createAdditionalCost.mockReturnValueOnce(new Promise(r => { resolve = r }))
    const w = await open(); await enter(w); await w.find('form').trigger('submit')
    await w.setProps({ open: false }); await w.setProps({ open: true }); await flushPromises()
    expect(button(w, '保存记录').attributes('disabled')).toBeDefined()
    resolve({ items: fullSource() }); await flushPromises()
    expect(button(w, '保存记录').attributes('disabled')).toBeUndefined()
    expect(amount(w).element).toHaveProperty('value', '')
    expect(api.createAdditionalCost).toHaveBeenCalledOnce()
  })

  it('previews fixed rounding, promotion estimates and the direction of negative adjustments', async () => {
    const w = await open(); await enter(w)
    await w.find('input[placeholder="分摊天数"]').setValue('3')
    for (const text of ['费用总额', '预计计入总额', '33.33', '33.34', '不会自动每月续记']) expect(w.text()).toContain(text)
    const type = w.find('form select')
    // 活动赠送已停止新增：新增表单只剩固定费用和手工调整。
    expect(type.findAll('option').map(option => option.attributes('value'))).toEqual(['fixed', 'adjustment'])
    await type.setValue('adjustment'); await amount(w).setValue('-5')
    expect(w.text()).toContain('本次减少成本')
    expect(w.find('input[placeholder="分摊天数"]').exists()).toBe(false)
  })

  it('keeps the promotion estimate preview when editing an existing promotion record', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source('promotion', '旧活动')], hasMore: false })
    api.getAdditionalCost.mockResolvedValueOnce({ items: fullSource('promotion', '旧活动') })
    const w = await open('ledger'); await button(w, '编辑 旧活动').trigger('click'); await flushPromises()
    const type = w.find('form select')
    expect(type.findAll('option').map(option => option.attributes('value'))).toEqual(['promotion', 'fixed', 'adjustment'])
    expect((type.element as HTMLSelectElement).value).toBe('promotion')
    await amount(w).setValue('900'); await w.find('input[placeholder="分摊天数"]').setValue('10')
    for (const text of ['赠送总额', '720.00', '72.00', '避免重复计入', '活动赠送已停止新增']) expect(w.text()).toContain(text)
    await button(w, '取消编辑').trigger('click')
    expect(w.find('form select').findAll('option').map(option => option.attributes('value'))).toEqual(['fixed', 'adjustment'])
  })

  it('requires legacy missing original amounts to be re-entered but preserves a recorded zero', async () => {
    api.listAccountCostLedger.mockResolvedValue({ items: [source()], hasMore: false })
    api.getAdditionalCost.mockResolvedValueOnce({ items: fullSource().map(r => ({ ...r, originalAmount: 0 })) })
    const w = await open('ledger'); await button(w, '编辑 隔离费用').trigger('click'); await flushPromises()
    expect(amount(w).element).toHaveProperty('value', '')
    expect(w.text()).toContain('未记录原始总额')
    await w.find('form').trigger('submit'); await flushPromises()
    expect(api.updateAdditionalCost).not.toHaveBeenCalled()
    await button(w, '取消编辑').trigger('click'); await button(w, '成本账本').trigger('click'); await flushPromises()
    api.getAdditionalCost.mockResolvedValueOnce({ items: fullSource().map(r => ({ ...r, originalAmount: 0, amount: 0, amountCents: 0 })) })
    await button(w, '编辑 隔离费用').trigger('click'); await flushPromises()
    expect(amount(w).element).toHaveProperty('value', '0')
  })

  it('explains fee estimation and retains confirmed fee-save success after history read failure', async () => {
    const w = await open('rules')
    expect(w.text()).toContain('按当天实收营收（主站消费扣除营收折扣后）')
    expect(w.text()).toContain('不是支付平台实际账单')
    api.getRechargeFeeRate.mockRejectedValueOnce(new Error('费率读取连接中断'))
    await button(w, '保存费率').trigger('click'); await flushPromises()
    expect(api.saveRechargeFeeRate).toHaveBeenCalledOnce()
    expect(w.emitted('updated')).toHaveLength(1)
    expect(w.text()).toContain('费率已保存')
    expect(w.text()).not.toContain('保存失败')
  })
})

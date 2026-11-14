// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'
import DailyStatsPanel from '@/modules/admin/components/dashboard/DailyStatsPanel.vue'

// V3.1.7 单独客户赠送记录：成本规则页区块、今日成本页和每日明细的营收折扣说明。
const api = vi.hoisted(() => ({
  createAdditionalCost: vi.fn(), getAdditionalCost: vi.fn(), updateAdditionalCost: vi.fn(), deleteAdditionalCost: vi.fn(),
  listAccountCostLedger: vi.fn(), getRechargeFeeRate: vi.fn(), listRechargeFeeRateHistory: vi.fn(), saveRechargeFeeRate: vi.fn(),
  listRevenueDiscountRates: vi.fn(), saveRevenueDiscountRate: vi.fn(), deleteRevenueDiscountRate: vi.fn(), deleteRechargeFeeRate: vi.fn(),
  getGiftLedger: vi.fn(), checkGiftLedgerCustomer: vi.fn(), createGiftLedgerRecord: vi.fn(), updateGiftLedgerRecord: vi.fn(), deleteGiftLedgerRecord: vi.fn(),
  getDailyStats: vi.fn(),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  ...api, createAccountBatch: vi.fn(), createAccountEvent: vi.fn(), getAccountAsset: vi.fn(),
  listAccountAssets: vi.fn().mockResolvedValue({ items: [], hasMore: false }), replaceAccountLink: vi.fn(), refreshAccountStats: vi.fn(),
}))
vi.mock('@/modules/admin/api/mySites', () => ({ listRealConnections: vi.fn().mockResolvedValue([]) }))

const businessDate = '2026-08-22'
const record = (overrides: Record<string, unknown> = {}) => ({
  id: 'r1', mainUserId: '123', email: 'a@example.com', rechargeDate: '2026-08-20', paidAmount: 100, giftAmount: 20,
  creditAmount: 120, giftShare: 0.1667, note: '', usedAmount: 120, state: 'used_up', ...overrides,
})
const customer = (overrides: Record<string, unknown> = {}) => ({
  mainUserId: '123', email: 'a@example.com', creditedTotal: 220, usedTotal: 150, remaining: 70,
  todayConsumption: 40, todayCovered: 30, todayUncovered: 10, todayGift: 5, mainBalance: 68.5,
  records: [record(), record({ id: 'r2', rechargeDate: '2026-08-21', paidAmount: 100, giftAmount: 0, creditAmount: 100, giftShare: 0, usedAmount: 30, state: 'using', note: '补录期初' })],
  ...overrides,
})
const overview = (overrides: Record<string, unknown> = {}) => ({ supported: true, statusAvailable: true, statusError: '', today: businessDate, customers: [] as unknown[], ...overrides })

const wrappers: VueWrapper[] = []
async function open(tab: 'today' | 'rules' = 'rules', props: Record<string, unknown> = {}) {
  const wrapper = mount(AccountCostWorkspace, {
    props: { open: false, initialTab: tab, businessDate, workspaceId: 'workspace-a', directCost: 20, operatingCost: 30, adjustedNetProfit: 70, ...props },
    global: { stubs: { Teleport: true } },
  })
  wrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  // 赠送记录的录入表单默认收起：像用户一样点“新增一笔”展开（主站不支持时按钮不可点）。
  if (tab === 'rules') await expandGift(wrapper)
  return wrapper
}
async function expandGift(w: VueWrapper) {
  if (w.find('input[aria-label="主站用户 ID"]').exists()) return
  const expander = w.findAll('button').find(b => b.text() === '新增一笔')
  if (!expander || expander.attributes('disabled') !== undefined) return
  await expander.trigger('click'); await flushPromises()
}
function button(w: VueWrapper, name: string) {
  const match = w.findAll('button').find(b => b.attributes('aria-label') === name || b.text() === name)
  if (!match) throw new Error(`missing button ${name}`)
  return match
}
const hasButton = (w: VueWrapper, name: string) => w.findAll('button').some(b => b.attributes('aria-label') === name || b.text() === name)
const input = (w: VueWrapper, label: string) => w.find(`input[aria-label="${label}"]`)
const section = (w: VueWrapper) => w.find('[data-testid="gift-ledger-section"]')

beforeEach(() => {
  for (const mock of Object.values(api)) mock.mockReset()
  api.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })
  api.getRechargeFeeRate.mockResolvedValue({ rate: 0.016, effectiveDate: businessDate })
  api.listRechargeFeeRateHistory.mockResolvedValue({ items: [] })
  api.listRevenueDiscountRates.mockResolvedValue({ items: [] })
  api.getGiftLedger.mockResolvedValue(overview())
  api.createGiftLedgerRecord.mockImplementation(async (input: Record<string, unknown>) => ({ ...record(), ...input, id: 'new', email: 'a@example.com' }))
  api.updateGiftLedgerRecord.mockImplementation(async (id: string, input: Record<string, unknown>) => ({ ...record(), ...input, id, email: 'a@example.com' }))
  api.deleteGiftLedgerRecord.mockResolvedValue(undefined)
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.restoreAllMocks(); localStorage.clear() })

describe('gift ledger section on the cost rules page', () => {
  it('explains the feature, the double-count reminder, how to fill and the empty list', async () => {
    const w = await open()
    const text = section(w).text()
    for (const expected of [
      '单独客户赠送记录（从营收中扣除）',
      '少数客户每次赠送比例不同（例如充 100 送 20）时，在这里每送一笔记一笔。',
      '花超过已记录到账总额的部分，是他自己充的钱，按整站折扣率计算。只读主站消费，不会修改主站。',
      '估算上面的整站折扣率时，不要再把这些客户的赠送算进去，否则会重复扣除。',
      '赠送填这次多给的全部余额（包括主站自动送的）。实付默认 0，只填赠送也可以：这一笔到账就只算赠送金额，照样按先充先用，轮到这一笔时花的钱全部当作赠送从营收扣除。知道实付就填上更准：按赠送占到账的比例分摊扣除，实付那部分也算在这一笔里，不会再按整站折扣率重复扣。',
      '补录过去日期的充值时，从那天起他已经花掉的部分会自动算作用掉了；那些日子已经结算过，按整站折扣率计算，不再补扣。',
      '更早的充值请按今天零点的剩余录入，日期填今天；剩余来自几笔比例不同的充值时，按先充先用算出每笔还剩多少，分别录入，先录早的。',
      '还没有单独客户赠送记录，所有客户都按整站折扣率计算。',
    ]) expect(text).toContain(expected)
    for (const label of ['主站用户 ID', '充值日期', '实付金额', '赠送金额', '备注（可选）']) expect(input(w, label).exists()).toBe(true)
    expect(hasButton(w, '查询这个用户')).toBe(true)
    expect(hasButton(w, '保存记录')).toBe(true)
    for (const word of ['ledger', 'FIFO', 'snapshot', 'jsonb']) expect(text).not.toContain(word)
  })

  it('defaults the paid amount to 0 so a gift alone can be saved, and resets it to 0 after saving', async () => {
    const w = await open()
    expect(input(w, '实付金额').element).toHaveProperty('value', '0')
    await input(w, '主站用户 ID').setValue('123')
    await input(w, '充值日期').setValue('2026-08-20')
    await input(w, '赠送金额').setValue('30')
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(api.createGiftLedgerRecord).toHaveBeenCalledWith({ mainUserId: '123', rechargeDate: '2026-08-20', paidAmount: 0, giftAmount: 30, note: '' })
    // 保存后表单收起；再点“新增一笔”时实付仍默认 0、赠送已清空。
    expect(input(w, '实付金额').exists()).toBe(false)
    await expandGift(w)
    expect(input(w, '实付金额').element).toHaveProperty('value', '0')
    expect(input(w, '赠送金额').element).toHaveProperty('value', '')
  })

  it('saves a new record, shows the success text and reloads the list and home numbers', async () => {
    const w = await open()
    await input(w, '主站用户 ID').setValue(' 123 ')
    await input(w, '充值日期').setValue('2026-08-20')
    await input(w, '实付金额').setValue('100')
    await input(w, '赠送金额').setValue('20')
    await input(w, '备注（可选）').setValue('首充')
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(api.createGiftLedgerRecord).toHaveBeenCalledWith({ mainUserId: '123', rechargeDate: '2026-08-20', paidAmount: 100, giftAmount: 20, note: '首充' })
    expect(w.text()).toContain('记录已保存：a@example.com 2026-08-20 实付 100.00 送 20.00。今天起按先充先用计算，已经结算的日期不变，不会修改主站。')
    expect(w.emitted('updated')).toHaveLength(1)
    expect(api.getGiftLedger).toHaveBeenCalledTimes(2)
    expect(input(w, '主站用户 ID').exists()).toBe(false)
    await expandGift(w)
    expect(input(w, '主站用户 ID').element).toHaveProperty('value', '')
  })

  it('looks up a user and explains the zero-o\'clock balance, or explains the failure', async () => {
    api.checkGiftLedgerCustomer.mockResolvedValueOnce({ mainUserId: '123', email: 'a@example.com', balance: 50, todayConsumption: 12.5 })
    const w = await open()
    await input(w, '主站用户 ID').setValue('123')
    await button(w, '查询这个用户').trigger('click'); await flushPromises()
    expect(api.checkGiftLedgerCustomer).toHaveBeenCalledWith('123')
    expect(w.text()).toContain('a@example.com（主站 ID 123）：主站当前余额 50.00，今天已花 12.50。按“今天零点的剩余”录入时，零点余额约为 62.50（今天如有新充值，要再减去）。')
    api.checkGiftLedgerCustomer.mockRejectedValueOnce(new Error('dashboard.giftLedger.errors.userNotFound'))
    await button(w, '查询这个用户').trigger('click'); await flushPromises()
    expect(w.text()).toContain('主站找不到 ID 为 123 的用户，请核对后重新填写；没有查到')
    expect(w.text()).not.toContain('主站当前余额 50.00')
    expect(w.text()).not.toContain('dashboard.giftLedger')
    expect(api.createGiftLedgerRecord).not.toHaveBeenCalled()
  })

  it('shows customer cards with totals, today split, balance and record rows; warns only when balance is short by more than 1', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [
      customer(),
      customer({ mainUserId: '456', email: 'b@example.com', mainBalance: 69.5, todayConsumption: 0, todayCovered: 0, todayUncovered: 0, todayGift: 0, records: [record({ id: 'r3', mainUserId: '456', email: 'b@example.com', usedAmount: 0, state: 'unused' })] }),
    ] }))
    const w = await open()
    const cards = w.findAll('[data-testid="gift-ledger-customer"]')
    expect(cards).toHaveLength(2)
    const first = cards[0]!.text()
    expect(first).toContain('a@example.com（主站 ID 123）')
    expect(first).toContain('已记录到账 220.00 · 累计已用 150.00 · 按记录剩余 70.00')
    expect(first).toContain('今天花了 40.00：其中 30.00 用的是已记录的充值，扣赠送 5.00；10.00 超出记录，按整站折扣率计算。')
    expect(first).toContain('主站当前余额 68.50')
    expect(first).toContain('主站余额比按记录剩余少 1.50：可能有退款、管理员扣了余额或记录填多了，请核对后修改记录；改正前这位客户的赠送可能扣多了。')
    expect(first).toContain('2026-08-20 · 实付 100.00 · 赠送 20.00（占到账 16.67%）· 已用 120.00/120.00 · 已用完')
    expect(first).toContain('2026-08-21 · 实付 100.00 · 赠送 0.00（占到账 0%）· 已用 30.00/100.00 · 正在用')
    expect(first).toContain('补录期初')
    const second = cards[1]!.text()
    expect(second).toContain('今天还没有消费。')
    expect(second).toContain('· 未开始用')
    expect(second).not.toContain('主站余额比按记录剩余少')
    expect(hasButton(w, '编辑 a@example.com 2026-08-20 的记录')).toBe(true)
    expect(hasButton(w, '删除 b@example.com 2026-08-20 的记录')).toBe(true)
  })

  it('edits a record with the user locked, saves with PUT and restores the new form on cancel', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer()] }))
    const w = await open()
    await button(w, '编辑 a@example.com 2026-08-20 的记录').trigger('click')
    expect(input(w, '主站用户 ID').element).toHaveProperty('value', '123')
    expect(input(w, '主站用户 ID').attributes('disabled')).toBeDefined()
    expect(input(w, '实付金额').element).toHaveProperty('value', '100')
    expect(w.text()).toContain('正在编辑 a@example.com 2026-08-20 的记录：主站用户不能改，要换用户请删除后重新录入。保存后今天起按改后的记录重新推算，已经结算的日期不变。')
    expect(hasButton(w, '保存记录')).toBe(false)
    await button(w, '取消编辑').trigger('click')
    expect(hasButton(w, '保存记录')).toBe(true)
    expect(input(w, '主站用户 ID').element).toHaveProperty('value', '')
    expect(input(w, '主站用户 ID').attributes('disabled')).toBeUndefined()
    expect(w.text()).not.toContain('正在编辑 a@example.com')
    await button(w, '编辑 a@example.com 2026-08-20 的记录').trigger('click')
    await input(w, '赠送金额').setValue('30')
    await button(w, '保存修改').trigger('click'); await flushPromises()
    expect(api.updateGiftLedgerRecord).toHaveBeenCalledWith('r1', { mainUserId: '123', rechargeDate: '2026-08-20', paidAmount: 100, giftAmount: 30, note: '' })
    expect(api.createGiftLedgerRecord).not.toHaveBeenCalled()
    expect(w.text()).toContain('记录已保存：a@example.com 2026-08-20 实付 100.00 送 30.00。')
  })

  it('deletes only after confirmation, mentions the used amount when there is one', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer({ records: [record(), record({ id: 'r9', rechargeDate: '2026-08-22', usedAmount: 0, state: 'unused' })] })] }))
    const w = await open()
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(w, '删除 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(window.confirm).toHaveBeenLastCalledWith('这笔已经用掉 120.00。删除 a@example.com 2026-08-20 实付 100.00 送 20.00 的记录？删除表示这笔从没发生过：今天起按剩下的记录重新推算；已经结算的日期不变，不会修改主站。已经用完的记录不要为了整理而删除。')
    expect(api.deleteGiftLedgerRecord).not.toHaveBeenCalled()
    vi.mocked(window.confirm).mockReturnValueOnce(false)
    await button(w, '删除 a@example.com 2026-08-22 的记录').trigger('click'); await flushPromises()
    expect(vi.mocked(window.confirm).mock.lastCall?.[0]).not.toContain('这笔已经用掉')
    await button(w, '删除 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(api.deleteGiftLedgerRecord).toHaveBeenCalledWith('r1')
    expect(w.text()).toContain('记录已删除；今天起按剩下的记录计算，已经结算的日期不变，不会修改主站。')
    expect(w.emitted('updated')).toHaveLength(1)
  })

  it('lists records with the reason when the status cannot be read', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ statusAvailable: false, statusError: '读取客户 a@example.com（主站 ID 123）2026-08-22 的主站消费失败（按客户查询消费：没有收到主站响应（超时））',
      customers: [customer({ usedTotal: null, remaining: null, todayConsumption: null, todayCovered: null, todayUncovered: null, todayGift: null, mainBalance: null, records: [record({ usedAmount: null, state: '' })] })] }))
    const w = await open()
    expect(w.text()).toContain('暂时查不到主站消费，下面只显示记录本身：读取客户 a@example.com（主站 ID 123）2026-08-22 的主站消费失败（按客户查询消费：没有收到主站响应（超时））。请稍后点击‘重新加载费用’；不会修改主站。')
    const card = w.find('[data-testid="gift-ledger-customer"]').text()
    expect(card).toContain('已记录到账 220.00 · 累计已用 — · 按记录剩余 —')
    expect(card).toContain('2026-08-20 · 实付 100.00 · 赠送 20.00（占到账 16.67%）· 已用 —/120.00')
    expect(card).toContain('主站当前余额 —')
    expect(card).not.toContain('今天花了')
    expect(card).not.toContain('今天还没有消费')
  })

  it('disables the form when the main site is not Sub2API', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ supported: false, statusError: '当前主站不是 Sub2API，不能按客户查询消费' }))
    const w = await open()
    expect(w.text()).toContain('当前主站不是 Sub2API，不能按客户查询消费，暂不支持单独客户赠送记录。')
    // 不支持时“新增一笔”不可点，录入表单展开不了，也就没有查询、保存入口。
    expect(button(w, '新增一笔').attributes('disabled')).toBeDefined()
    expect(input(w, '主站用户 ID').exists()).toBe(false)
    expect(hasButton(w, '查询这个用户')).toBe(false)
    expect(hasButton(w, '保存记录')).toBe(false)
  })

  it('explains a list load failure in plain words', async () => {
    api.getGiftLedger.mockRejectedValue(new Error('admin.dashboard.adminAuth.errors.network'))
    const w = await open()
    expect(w.text()).toContain('单独客户赠送记录加载失败：无法连接本站后端，请检查网络及后端服务')
    expect(w.text()).not.toContain('admin.dashboard.adminAuth')
  })

  it.each([
    ['invalidUser', '主站用户 ID 需要填数字（主站后台用户列表里的 ID）；没有保存'],
    ['userNotFound', '主站找不到 ID 为 123 的用户，请核对后重新填写；没有保存'],
    ['invalidAmount', '实付和赠送都要填 0 到 10,000,000 之间的金额（最多两位小数），两者不能都是 0；没有保存'],
    ['invalidNote', '备注最多 200 字；没有保存'],
    ['invalidDate', '充值日期无效，请重新选择；没有保存'],
    ['futureDate', '充值日期不能晚于今天；没有保存'],
    ['tooOld', '充值日期最早可填 60 天前（主站只保留近 90 天消费）。更早的充值请按今天零点的剩余录入，日期填今天；没有保存'],
    ['unsupported', '当前主站不是 Sub2API，不能按客户查询消费，暂不支持单独客户赠送记录。'],
  ])('shows the plain text for %s instead of the key', async (key, text) => {
    const w = await open()
    await input(w, '主站用户 ID').setValue('123')
    await input(w, '实付金额').setValue('100')
    api.createGiftLedgerRecord.mockRejectedValueOnce(new Error(`dashboard.giftLedger.errors.${key}`))
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain(text)
    expect(w.text()).not.toContain('dashboard.giftLedger')
    expect(w.emitted('updated')).toBeUndefined()
  })

  it('carries the backend reason when the user check against the main site fails', async () => {
    const w = await open()
    await input(w, '主站用户 ID').setValue('123')
    await input(w, '实付金额').setValue('100')
    const error = Object.assign(new Error('dashboard.giftLedger.errors.userLookupFailed'), { reason: '读取主站用户资料：HTTP 403，主站提示“admin only”' })
    api.createGiftLedgerRecord.mockRejectedValueOnce(error)
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain('无法向主站核对这个用户（读取主站用户资料：HTTP 403，主站提示“admin only”）；没有保存，请检查主站登录状态后重试')
  })

  it('clears records, lookups, the edit and the draft when the workspace changes, and ignores late answers', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer()] }))
    // 编辑中切换：编辑提示和锁定的用户不能带到新工作区，保存只能是新增。
    const edited = await open()
    await button(edited, '编辑 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(edited.text()).toContain('正在编辑 a@example.com 2026-08-20 的记录')
    // 新工作区的列表还没返回时，也不能显示上一个工作区的客户。
    let resolveB: (value: unknown) => void = () => {}
    api.getGiftLedger.mockReturnValueOnce(new Promise(resolve => { resolveB = resolve }))
    await edited.setProps({ workspaceId: 'workspace-b' }); await flushPromises()
    expect(section(edited).text()).not.toContain('正在编辑')
    expect(section(edited).text()).not.toContain('a@example.com')
    resolveB(overview()); await flushPromises()
    expect(section(edited).text()).toContain('还没有单独客户赠送记录，所有客户都按整站折扣率计算。')
    // 切换工作区后录入表单收起；重新展开时是空白的新增表单。
    expect(input(edited, '主站用户 ID').exists()).toBe(false)
    await expandGift(edited)
    expect((input(edited, '主站用户 ID').element as HTMLInputElement).value).toBe('')
    expect(input(edited, '主站用户 ID').attributes('disabled')).toBeUndefined()
    expect(hasButton(edited, '保存修改')).toBe(false)
    expect(hasButton(edited, '保存记录')).toBe(true)

    // 草稿和查询中切换：草稿清空，旧查询晚返回也不显示。
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer()] }))
    const drafted = await open()
    let resolveCheck: (value: unknown) => void = () => {}
    api.checkGiftLedgerCustomer.mockReturnValueOnce(new Promise(resolve => { resolveCheck = resolve }))
    await input(drafted, '主站用户 ID').setValue('123')
    await input(drafted, '实付金额').setValue('88')
    await button(drafted, '查询这个用户').trigger('click')
    api.getGiftLedger.mockResolvedValue(overview())
    await drafted.setProps({ workspaceId: 'workspace-b' }); await flushPromises()
    resolveCheck({ mainUserId: '123', email: 'a@example.com', balance: 10, todayConsumption: 1 }); await flushPromises()
    expect(section(drafted).text()).not.toContain('a@example.com')
    expect(input(drafted, '主站用户 ID').exists()).toBe(false)
    await expandGift(drafted)
    expect((input(drafted, '主站用户 ID').element as HTMLInputElement).value).toBe('')
    expect((input(drafted, '实付金额').element as HTMLInputElement).value).toBe('0')
    expect(button(drafted, '查询这个用户').attributes('disabled')).toBeUndefined()
  })

  it('offers 重新加载费用 when a save is unconfirmed; the reload only reads and never saves again', async () => {
    const w = await open()
    await input(w, '主站用户 ID').setValue('123')
    await input(w, '实付金额').setValue('100')
    api.createGiftLedgerRecord.mockRejectedValueOnce(new Error('admin.dashboard.adminAuth.errors.network'))
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain('记录保存未能确认：无法连接本站后端，请检查网络及后端服务。暂时无法确认是否已保存，请点击‘重新加载费用’查看记录，再决定是否重试；不会修改主站。')
    const reads = api.getGiftLedger.mock.calls.length
    await w.find('[data-testid="error-reload"]').trigger('click'); await flushPromises()
    expect(api.getGiftLedger.mock.calls.length).toBe(reads + 1)
    expect(api.createGiftLedgerRecord).toHaveBeenCalledTimes(1)
    expect(w.find('[data-testid="error-reload"]').exists()).toBe(false)
    expect(w.text()).not.toContain('记录保存未能确认')
  })

  it('offers 重新加载费用 for an unconfirmed delete and a missing record, but not when nothing was saved', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer()] }))
    const w = await open()
    api.deleteGiftLedgerRecord.mockRejectedValueOnce(new Error('admin.dashboard.adminAuth.errors.network'))
    await button(w, '删除 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain('记录删除未能确认')
    expect(w.find('[data-testid="error-reload"]').exists()).toBe(true)
    api.deleteGiftLedgerRecord.mockRejectedValueOnce(new Error('dashboard.giftLedger.errors.notFound'))
    await button(w, '删除 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(w.find('[data-testid="error-reload"]').exists()).toBe(true)
    await input(w, '主站用户 ID').setValue('x')
    await input(w, '实付金额').setValue('100')
    api.createGiftLedgerRecord.mockRejectedValueOnce(new Error('dashboard.giftLedger.errors.invalidUser'))
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain('主站用户 ID 需要填数字')
    expect(w.find('[data-testid="error-reload"]').exists()).toBe(false)
    api.createGiftLedgerRecord.mockRejectedValueOnce(Object.assign(new Error('dashboard.giftLedger.errors.userLookupFailed'), { reason: '读取主站用户资料：主站有响应，但返回的资料里没有主站用户 ID 123' }))
    await button(w, '保存记录').trigger('click'); await flushPromises()
    expect(w.find('[data-testid="error-reload"]').exists()).toBe(false)
  })

  it('shows a working 重新加载费用 button when the status or the list cannot be read', async () => {
    api.getGiftLedger.mockResolvedValueOnce(overview({ statusAvailable: false, statusError: '主站超时', customers: [customer({ usedTotal: null, remaining: null })] }))
    const w = await open()
    api.getGiftLedger.mockResolvedValueOnce(overview({ customers: [customer()] }))
    const reload = section(w).findAll('button').find(b => b.text() === '重新加载费用')
    expect(reload).toBeDefined()
    await reload!.trigger('click'); await flushPromises()
    expect(section(w).text()).not.toContain('暂时查不到主站消费')
    expect(section(w).text()).toContain('累计已用 150.00')
    api.getGiftLedger.mockRejectedValueOnce(new Error('admin.dashboard.adminAuth.errors.network'))
    const w2 = await open()
    const reload2 = section(w2).findAll('button').find(b => b.text() === '重新加载费用')
    expect(section(w2).text()).toContain('单独客户赠送记录加载失败')
    api.getGiftLedger.mockResolvedValueOnce(overview())
    await reload2!.trigger('click'); await flushPromises()
    expect(section(w2).text()).not.toContain('单独客户赠送记录加载失败')
  })

  it('never shows two 重新加载费用 buttons at once', async () => {
    const reloadButtons = (w: VueWrapper) => w.findAll('button').filter(b => b.text() === '重新加载费用').length
    // 列表连续两次加载失败：第二次失败后顶部出现重新加载入口，区块里的按钮隐藏。
    api.getGiftLedger.mockRejectedValue(new Error('admin.dashboard.adminAuth.errors.network'))
    const w = await open()
    expect(reloadButtons(w)).toBe(1)
    await section(w).findAll('button').find(b => b.text() === '重新加载费用')!.trigger('click'); await flushPromises()
    expect(w.text()).toContain('更新失败')
    expect(reloadButtons(w)).toBe(1)
    // 状态读取失败时保存结果未确认：只保留错误下面的按钮。
    api.getGiftLedger.mockResolvedValue(overview({ statusAvailable: false, statusError: '主站超时', customers: [customer({ usedTotal: null, remaining: null })] }))
    const w2 = await open()
    expect(reloadButtons(w2)).toBe(1)
    await input(w2, '主站用户 ID').setValue('123')
    await input(w2, '实付金额').setValue('100')
    api.createGiftLedgerRecord.mockRejectedValueOnce(new Error('admin.dashboard.adminAuth.errors.network'))
    await button(w2, '保存记录').trigger('click'); await flushPromises()
    expect(w2.find('[data-testid="error-reload"]').exists()).toBe(true)
    expect(reloadButtons(w2)).toBe(1)
  })

  it('explains a delete of a record that no longer exists', async () => {
    api.getGiftLedger.mockResolvedValue(overview({ customers: [customer()] }))
    const w = await open()
    api.deleteGiftLedgerRecord.mockRejectedValueOnce(new Error('dashboard.giftLedger.errors.notFound'))
    await button(w, '删除 a@example.com 2026-08-20 的记录').trigger('click'); await flushPromises()
    expect(w.text()).toContain('这条记录已不存在或不属于当前工作区，请点击‘重新加载费用’确认')
    expect(w.emitted('updated')).toBeUndefined()
  })
})

describe('revenue discount explanation with individual customers', () => {
  const summary = { rechargeFee: 1.5, accountPurchase: 0, accountRefund: 0, promotion: 0, fixed: 0, adjustment: 0, total: 1.5, available: true }
  const detail = (overrides: Record<string, unknown> = {}) => ({
    version: 1, generalRate: 0.05, generalBase: 140, generalDiscount: 7, ledgerCovered: 60, ledgerGift: 10,
    customers: [{ mainUserId: '123', email: 'a@example.com', consumptionMicros: 60e6, coveredMicros: 60e6, uncoveredMicros: 0, giftCents: 1000, lateProtected: false }],
    ...overrides,
  })

  it('splits the discount into the site-wide part and the individual gifts on the today tab', async () => {
    const w = await open('today', { todayRevenue: 183, summary: { ...summary, revenueDiscount: 17, revenueDiscountRate: 0.05, netRevenue: 183, revenueDiscountDetail: detail() } })
    expect(w.text()).toContain('营收 183.00 = 主站消费 200.00 − 营收折扣 17.00。其中整站折扣 7.00 =（主站消费 200.00 − 单独客户已记录部分 60.00）× 5%；单独客户赠送 10.00（1 位客户）。营收折扣不计入成本。')
    expect(w.text()).not.toContain('充值赠送 3%')
  })

  it('writes × 0 when there is no site-wide rule today', async () => {
    const w = await open('today', { todayRevenue: 190, summary: { ...summary, revenueDiscount: 10, revenueDiscountRate: null, netRevenue: 190, revenueDiscountDetail: detail({ generalRate: null, generalDiscount: 0 }) } })
    expect(w.text()).toContain('× 0（今天没有生效的整站折扣率）；单独客户赠送 10.00（1 位客户）')
  })

  it('explains the temporary mismatch instead of a negative subtraction', async () => {
    const w = await open('today', { todayRevenue: 40, summary: { ...summary, revenueDiscount: 10, revenueDiscountRate: 0.05, netRevenue: 40, revenueDiscountDetail: detail({ generalBase: 0, generalDiscount: 0 }) } })
    expect(w.text()).toContain('营收 40.00 = 主站消费 50.00 − 单独客户赠送 10.00。主站消费暂时小于单独客户已记录部分 60.00（两次查询时间相差几十秒），整站折扣暂按 0 计算；稍后刷新会更新。营收折扣不计入成本。')
    expect(w.text()).not.toMatch(/（主站消费 [\d.,]+ − 单独客户已记录部分 [\d.,]+）×/)
  })

  it('keeps the V3.1.6 text when there is no split', async () => {
    const w = await open('today', { todayRevenue: 93.5, summary: { ...summary, revenueDiscount: 6.5, revenueDiscountRate: 0.065, netRevenue: 93.5,
      revenueDiscountRule: { id: 'd', effectiveDate: '2026-08-20', giftRate: 0.03, newUserRate: 0.015, referralRate: 0.02, totalRate: 0.065, createdAt: '' } } })
    expect(w.text()).toContain('营收 93.50 = 主站消费 100.00 − 营收折扣 6.50（合计 6.5%：充值赠送 3%、新用户福利 1.5%、邀请返利 2%）。营收折扣不计入成本。')
    expect(w.text()).not.toContain('单独客户')
  })

  it('shows the split and the late settlement note in daily stats, and the old line without a split', async () => {
    api.getDailyStats.mockResolvedValue({ items: [{
      date: '2026-08-21', settlementStatus: 'final', todayProfit: 200, revenueDiscount: 17, revenueDiscountRate: 0.05,
      revenueDiscountDetail: detail({ customers: [
        { mainUserId: '123', email: 'a@example.com', consumptionMicros: 60e6, coveredMicros: 60e6, uncoveredMicros: 0, giftCents: 1000, lateProtected: false },
        { mainUserId: '456', email: 'b@example.com', consumptionMicros: 30e6, coveredMicros: 0, uncoveredMicros: 30e6, giftCents: 0, lateProtected: true },
      ] }),
    }, {
      date: '2026-08-20', settlementStatus: 'final', todayProfit: 100, revenueDiscount: 6, revenueDiscountRate: 0.06,
    }] })
    const w = mount(DailyStatsPanel)
    wrappers.push(w)
    await flushPromises()
    await w.findAll('tbody tr')[0]!.trigger('click'); await flushPromises()
    const details = w.findAll('tbody tr')[1]!.text()
    expect(details).toContain('营收折扣：17.00（整站 7.00 + 单独客户赠送 10.00）；主站消费 200.00，其中单独客户已记录部分 60.00；营收折扣不计入成本。其中 1 位客户这天是事后补结，为避免与后面日期重复，这天没有扣他们的赠送。')
    expect(details).not.toContain('营收折扣（5%）')
    const rows = w.findAll('tbody tr')
    await rows[rows.length - 1]!.trigger('click'); await flushPromises()
    const all = w.text()
    expect(all).toContain('营收折扣（6%）：6.00；主站消费 100.00；营收折扣不计入成本。')
    expect(all.split('事后补结').length - 1).toBe(1)
  })
})

// @vitest-environment jsdom

import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

const harness = vi.hoisted(() => ({
  createAdditionalCost: vi.fn(),
  getAdditionalCost: vi.fn(),
  updateAdditionalCost: vi.fn(),
  getRechargeFeeRate: vi.fn(),
  listRechargeFeeRateHistory: vi.fn(),
  listAccountAssets: vi.fn(),
  listAccountCostLedger: vi.fn(),
  listRealConnections: vi.fn(),
  routerPush: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: harness.routerPush }),
}))

vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  createAccountBatch: vi.fn(),
  createAccountEvent: vi.fn(),
  createAdditionalCost: harness.createAdditionalCost,
  getAdditionalCost: harness.getAdditionalCost,
  updateAdditionalCost: harness.updateAdditionalCost,
  getAccountAsset: vi.fn(),
  getRechargeFeeRate: harness.getRechargeFeeRate,
  listRechargeFeeRateHistory: harness.listRechargeFeeRateHistory,
  listAccountAssets: harness.listAccountAssets,
  listAccountCostLedger: harness.listAccountCostLedger,
  refreshAccountStats: vi.fn(),
  replaceAccountLink: vi.fn(),
  saveRechargeFeeRate: vi.fn(),
}))

vi.mock('@/modules/admin/api/mySites', () => ({
  listRealConnections: harness.listRealConnections,
}))

const defaultProps = {
  open: false,
  businessDate: '2026-08-22',
  directCost: 40,
  operatingCost: 50,
  adjustedNetProfit: 70,
  summary: {
    rechargeFee: 2,
    accountPurchase: 10,
    accountRefund: -2,
    replacementDeduction: 5,
    accountQuality: 'complete',
    promotion: 1,
    fixed: 3,
    adjustment: 1,
    total: 15,
    available: true,
  },
  initialTab: 'today' as const,
  workspaceId: 'workspace-a',
}

const mountedWrappers: VueWrapper[] = []
const mountWorkspace = async (initialTab: 'today' | 'assets' | 'ledger' | 'rules' = 'today', workspaceId = 'workspace-a') => {
  const wrapper = mount(AccountCostWorkspace, {
    props: { ...defaultProps, initialTab, workspaceId },
    global: { stubs: { Teleport: true } },
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()
  return wrapper
}

const mountWorkspaceWithRealTeleport = async () => {
  const wrapper = mount(AccountCostWorkspace, {
    props: defaultProps,
    attachTo: document.body,
  })
  mountedWrappers.push(wrapper)
  await wrapper.setProps({ open: true })
  await flushPromises()

  const formElement = [...document.body.querySelectorAll('form')].find(item => item.textContent?.includes('记一笔成本'))
  if (!formElement) throw new Error('missing teleported manual cost form')
  return { wrapper, form: new DOMWrapper(formElement) }
}

const findButton = (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => (
    item.text().includes(label) || item.attributes('title') === label || item.attributes('aria-label') === label
  ))
  if (!button) throw new Error(`missing button: ${label}`)
  return button
}

const statusSelect = (wrapper: VueWrapper) => {
  const select = wrapper.findAll('select').find(item => item.text().includes('全部状态'))
  if (!select) throw new Error('missing account status filter')
  return select
}

beforeEach(() => {
  localStorage.clear()
  harness.createAdditionalCost.mockReset().mockResolvedValue({ items: [] })
  harness.getAdditionalCost.mockReset().mockResolvedValue({ items: [] })
  harness.updateAdditionalCost.mockReset().mockResolvedValue({ items: [] })
  harness.getRechargeFeeRate.mockReset().mockResolvedValue({
    id: 'rate-current', effectiveDate: '2026-08-22', rate: 0.016, createdAt: '2026-08-22T08:00:00Z',
  })
  harness.listRechargeFeeRateHistory.mockReset().mockResolvedValue({ items: [] })
  harness.listAccountAssets.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listAccountCostLedger.mockReset().mockResolvedValue({ items: [], hasMore: false })
  harness.listRealConnections.mockReset().mockResolvedValue([])
})

afterEach(() => {
  for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
  localStorage.clear()
})

describe('AccountCostWorkspace regression behavior', () => {
  it('submits a valid manual cost when the user clicks 保存记录', async () => {
    const { wrapper, form } = await mountWorkspaceWithRealTeleport()

    await form.find('select').setValue('fixed')
    await form.find('input[placeholder="名称"]').setValue('本地服务器')
    await form.find('input[placeholder="金额（元）"]').setValue('12.34')
    await form.find('input[type="date"]').setValue('2026-08-22')
    await form.find('input[placeholder="分摊天数"]').setValue('1')
    await form.find('input[placeholder="说明（可选）"]').setValue('月度固定费用')

    const saveButton = form.findAll('button').find(item => item.text().includes('保存记录'))
    if (!saveButton) throw new Error('missing save cost button')
    expect(saveButton.attributes('type')).toBe('submit')
    expect(form.element.isConnected).toBe(true)
    expect((saveButton.element as HTMLButtonElement).form).toBe(form.element)
    expect((form.element as HTMLFormElement).checkValidity()).toBe(true)
    ;(saveButton.element as HTMLButtonElement).click()
    await vi.waitFor(() => expect(wrapper.emitted('updated')).toHaveLength(1))
    await nextTick()

    expect(harness.createAdditionalCost).toHaveBeenCalledOnce()
  })

  it('keeps an invalid manual cost from submitting when the user clicks 保存记录', async () => {
    const { wrapper, form } = await mountWorkspaceWithRealTeleport()
    const saveButton = form.findAll('button').find(item => item.text().includes('保存记录'))
    if (!saveButton) throw new Error('missing save cost button')

    expect(saveButton.attributes('type')).toBe('submit')
    expect((form.element as HTMLFormElement).checkValidity()).toBe(false)
    ;(saveButton.element as HTMLButtonElement).click()
    await flushPromises()

    expect(harness.createAdditionalCost).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toBeUndefined()
  })

  it('R06 queries and renders current-day ledger changes with type, name, amount and source', async () => {
    harness.listAccountCostLedger.mockResolvedValue({
      items: [{
        id: 'ledger-r06-1', type: 'fixed', name: 'R06 固定服务器', businessDate: '2026-08-22',
        amount: 12.34, amountCents: 1234, sourceId: 'source-r06-1', estimated: false,
        createdAt: '2026-08-22T10:30:00+08:00',
      }],
      hasMore: false,
    })

    const wrapper = await mountWorkspace('today')

    expect(harness.listAccountCostLedger).toHaveBeenCalledWith(expect.objectContaining({
      from: '2026-08-22', to: '2026-08-22', page: 1, pageSize: 100,
    }))
    expect(wrapper.text()).toContain('固定')
    expect(wrapper.text()).toContain('R06 固定服务器')
    expect(wrapper.text()).toContain('¥12.34')
    expect(wrapper.text()).toContain('source-r06-1')
  })

  it('R06 distinguishes an empty current-day ledger from a load failure', async () => {
    const emptyWrapper = await mountWorkspace('today')
    expect(emptyWrapper.text()).toContain('当日暂无成本变动')
    expect(emptyWrapper.text()).not.toContain('当日成本变动加载失败')

    harness.listAccountCostLedger.mockRejectedValueOnce(new Error('ledger unavailable'))
    const failedWrapper = await mountWorkspace('today')
    expect(failedWrapper.text()).toContain('当日成本变动加载失败')
    expect(failedWrapper.text()).not.toContain('当日暂无成本变动')
  })

  it('edits a complete source using its earliest day and refreshes both ledger views', async () => {
    const latest = {
      id: 'source-edit-1-1', type: 'fixed', name: '跨日服务器', businessDate: '2026-08-22',
      amount: 33.34, amountCents: 3334, originalAmount: 100, days: 3, sourceId: 'source-edit-1', estimated: false,
      createdAt: '2026-08-22T10:00:00+08:00',
    }
    const first = { ...latest, id: 'source-edit-1-0', businessDate: '2026-08-20', amount: 33.33, amountCents: 3333 }
    harness.listAccountCostLedger.mockResolvedValue({ items: [latest, first], hasMore: false })
    harness.getAdditionalCost.mockResolvedValue({ items: [latest, first] })
    harness.updateAdditionalCost.mockResolvedValue({ items: [
      { ...latest, businessDate: '2026-08-20', amount: 50, amountCents: 5000, days: 2 },
      { ...latest, id: 'source-edit-1-1', businessDate: '2026-08-21', amount: 50, amountCents: 5000, days: 2 },
    ] })

    const wrapper = await mountWorkspace('ledger')
    await findButton(wrapper, '编辑 跨日服务器').trigger('click')
    await flushPromises()

    expect(harness.getAdditionalCost).toHaveBeenCalledWith('source-edit-1')
    expect(wrapper.find('input[type="date"]').element).toHaveProperty('value', '2026-08-20')
    expect(wrapper.find('input[placeholder="金额（元）"]').element).toHaveProperty('value', '100')

    await wrapper.find('input[placeholder="分摊天数"]').setValue('2')
    await wrapper.find('form').trigger('submit')
    await vi.waitFor(() => expect(harness.updateAdditionalCost).toHaveBeenCalledOnce())
    await flushPromises()

    expect(harness.updateAdditionalCost).toHaveBeenCalledWith('source-edit-1', expect.objectContaining({
      type: 'fixed', businessDate: '2026-08-20', amount: 100, days: 2,
    }))
    expect(harness.listAccountCostLedger.mock.calls.length).toBeGreaterThanOrEqual(3)
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it('does not render edit actions for account purchase or refund records', async () => {
    harness.listAccountCostLedger.mockResolvedValue({ items: [
      { id: 'purchase-1', type: 'account_purchase', name: '买号', businessDate: '2026-08-22', amount: 10, amountCents: 1000, sourceId: 'asset-1', estimated: false, createdAt: '2026-08-22T10:00:00+08:00' },
      { id: 'refund-1', type: 'account_refund', name: '退款', businessDate: '2026-08-22', amount: -2, amountCents: -200, sourceId: 'asset-1', estimated: false, createdAt: '2026-08-22T10:01:00+08:00' },
    ], hasMore: false })
    const wrapper = await mountWorkspace('ledger')
    expect(wrapper.text()).not.toContain('编辑 买号')
    expect(wrapper.text()).not.toContain('编辑 退款')
  })

  it('keeps the source form and edit mode when the replacement request fails', async () => {
    const source = {
      id: 'source-fail-1-0', type: 'fixed', name: '失败后保留', businessDate: '2026-08-20',
      amount: 100, amountCents: 10000, originalAmount: 100, days: 3, sourceId: 'source-fail-1',
      estimated: false, createdAt: '2026-08-20T10:00:00+08:00',
    }
    harness.listAccountCostLedger.mockResolvedValue({ items: [source], hasMore: false })
    harness.getAdditionalCost.mockResolvedValue({ items: [source] })
    harness.updateAdditionalCost.mockRejectedValueOnce(new Error('replacement failed'))

    const wrapper = await mountWorkspace('ledger')
    await findButton(wrapper, '编辑 失败后保留').trigger('click')
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(harness.updateAdditionalCost).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('编辑手工成本')
    expect(wrapper.text()).toContain('replacement failed')
    expect(wrapper.find('input[placeholder="金额（元）"]').element).toHaveProperty('value', '100')
    expect(wrapper.emitted('updated')).toBeUndefined()
  })

  it('prevents a second replacement request while the first one is pending', async () => {
    const source = {
      id: 'source-pending-1-0', type: 'fixed', name: '防重复提交', businessDate: '2026-08-20',
      amount: 100, amountCents: 10000, originalAmount: 100, days: 3, sourceId: 'source-pending-1',
      estimated: false, createdAt: '2026-08-20T10:00:00+08:00',
    }
    harness.listAccountCostLedger.mockResolvedValue({ items: [source], hasMore: false })
    harness.getAdditionalCost.mockResolvedValue({ items: [source] })
    let resolveUpdate: (value: { items: Array<typeof source> }) => void = () => undefined
    harness.updateAdditionalCost.mockImplementationOnce(() => new Promise(resolve => { resolveUpdate = resolve }))

    const wrapper = await mountWorkspace('ledger')
    await findButton(wrapper, '编辑 防重复提交').trigger('click')
    await flushPromises()
    const form = wrapper.find('form')
    await form.trigger('submit')
    await form.trigger('submit')
    await nextTick()

    expect(harness.updateAdditionalCost).toHaveBeenCalledOnce()
    resolveUpdate({ items: [source] })
    await flushPromises()
  })

  it('clears the edit source and reloads the active view when the workspace changes', async () => {
    const source = {
      id: 'source-switch-1-0', type: 'fixed', name: '切换工作区', businessDate: '2026-08-20',
      amount: 100, amountCents: 10000, originalAmount: 100, days: 3, sourceId: 'source-switch-1',
      estimated: false, createdAt: '2026-08-20T10:00:00+08:00',
    }
    harness.listAccountCostLedger.mockResolvedValue({ items: [source], hasMore: false })
    harness.getAdditionalCost.mockResolvedValue({ items: [source] })

    const wrapper = await mountWorkspace('ledger')
    await findButton(wrapper, '编辑 切换工作区').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('编辑手工成本')

    await wrapper.setProps({ workspaceId: 'workspace-b' })
    await flushPromises()

    expect(wrapper.text()).not.toContain('编辑手工成本')
    expect(wrapper.text()).toContain('记一笔成本')
    expect(harness.listAccountCostLedger.mock.calls.length).toBeGreaterThanOrEqual(3)
  })

  it('clears workspace-scoped lists and ignores a late response from the previous workspace', async () => {
    const source = {
      id: 'source-late-a-0', type: 'fixed', name: '工作区 A 旧成本', businessDate: '2026-08-20',
      amount: 100, amountCents: 10000, originalAmount: 100, days: 3, sourceId: 'source-late-a',
      estimated: false, createdAt: '2026-08-20T10:00:00+08:00',
    }
    let resolveWorkspaceA: (value: { items: Array<typeof source>; hasMore: boolean }) => void = () => undefined
    harness.listAccountCostLedger.mockImplementationOnce(() => new Promise(resolve => { resolveWorkspaceA = resolve }))
    harness.listAccountCostLedger.mockResolvedValue({ items: [], hasMore: false })

    const wrapper = await mountWorkspace('ledger')
    expect(wrapper.text()).not.toContain('工作区 A 旧成本')

    await wrapper.setProps({ workspaceId: 'workspace-b' })
    await flushPromises()
    expect(wrapper.text()).not.toContain('工作区 A 旧成本')
    expect(wrapper.text()).toContain('所选范围暂无记录')
    expect(wrapper.find('input[placeholder="金额（元）"]').exists()).toBe(false)

    resolveWorkspaceA({ items: [source], hasMore: false })
    await flushPromises()
    expect(wrapper.text()).not.toContain('工作区 A 旧成本')
  })

  it('R07 loads and renders recharge fee history from the current workspace Rules tab', async () => {
    harness.listRechargeFeeRateHistory.mockResolvedValue({
      items: [
        { id: 'rate-new', effectiveDate: '2026-08-20', rate: 0.02, createdAt: '2026-08-20T09:30:00+08:00' },
        { id: 'rate-old', effectiveDate: '2026-08-01', rate: 0.016, createdAt: '2026-08-01T08:00:00+08:00' },
      ],
    })

    const wrapper = await mountWorkspace('rules')

    expect(harness.listRechargeFeeRateHistory).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('费率生效历史')
    expect(wrapper.text()).toContain('2026-08-20')
    expect(wrapper.text()).toContain('2.00%')
    expect(wrapper.text()).toContain('rate-new')
    expect(wrapper.text()).toContain('2026-08-20 09:30')
  })

  it('R08 includes unactivated in the status filter and sends it to the existing asset query', async () => {
    const wrapper = await mountWorkspace('assets')
    const select = statusSelect(wrapper)

    expect(select.text()).toContain('未激活')
    await select.setValue('unactivated')
    await findButton(wrapper, '查询').trigger('click')
    await flushPromises()

    expect(harness.listAccountAssets).toHaveBeenLastCalledWith(expect.objectContaining({
      status: 'unactivated', page: 1, pageSize: 50,
    }))
  })

  it('R09 resets filters before loading a workspace without preferences and restores each workspace independently', async () => {
    const aPreference = { platform: 'openai', channel: '渠道 A', accountType: '订阅号', status: 'dead', search: 'A-账号' }
    localStorage.setItem('transithub.account-assets.filters.v1:workspace-a', JSON.stringify(aPreference))

    const wrapper = await mountWorkspace('assets', 'workspace-a')
    expect(harness.listAccountAssets).toHaveBeenLastCalledWith(expect.objectContaining(aPreference))

    harness.listAccountAssets.mockClear()
    await wrapper.setProps({ workspaceId: 'workspace-b' })
    await nextTick()
    await findButton(wrapper, '查询').trigger('click')
    await flushPromises()

    expect(harness.listAccountAssets).toHaveBeenLastCalledWith(expect.objectContaining({
      platform: '', channel: '', accountType: '', status: '', search: '',
    }))

    await wrapper.setProps({ workspaceId: 'workspace-a' })
    await nextTick()
    await findButton(wrapper, '查询').trigger('click')
    await flushPromises()

    expect(harness.listAccountAssets).toHaveBeenLastCalledWith(expect.objectContaining(aPreference))
    expect(JSON.parse(localStorage.getItem('transithub.account-assets.filters.v1:workspace-a') || '{}')).toEqual(aPreference)
  })

  it('R10 renders complete safe site, connection, Key and main-group labels without credentials', async () => {
    harness.listRealConnections.mockResolvedValue([{
      id: 'connection-r10',
      upstreamSiteId: 'site-id-only',
      siteName: '上海供应站',
      upstreamGroupId: 'upstream-group-1',
      upstreamGroupName: '供应商分组',
      upstreamKeyId: 'key-id-only',
      upstreamKey: 'sk-live-secret-r10',
      keyName: '生产 Key A',
      adminAccountId: 'admin-forward-1',
      adminAccountName: '旧管理员字段',
      connectionName: '转发连接 A',
      ownGroupIds: ['main-group-1'],
      ownGroupNames: ['主站高级组'],
      ownGroupName: '主站高级组',
      groupType: 'vip',
      provisioningMode: 'managed',
      status: 'active',
      upstreamPlatform: 'sub2api',
      createdAt: '2026-08-22T08:00:00+08:00',
    }])

    const wrapper = await mountWorkspace('assets')
    await findButton(wrapper, '录入买号').trigger('click')
    await nextTick()

    const option = wrapper.find('option[value="connection-r10"]')
    expect(option.exists()).toBe(true)
    expect(option.text()).toContain('上海供应站')
    expect(option.text()).toContain('转发连接 A')
    expect(option.text()).toContain('生产 Key A')
    expect(option.text()).toContain('主站高级组')
    expect(option.text()).not.toContain('sk-live-secret-r10')
    expect(option.text()).not.toBe('sub2api · site-id-only · key-id-only')
  })
})

// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountCostWorkspace from '@/modules/admin/components/dashboard/AccountCostWorkspace.vue'

const harness = vi.hoisted(() => ({
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
  createAdditionalCost: vi.fn(),
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

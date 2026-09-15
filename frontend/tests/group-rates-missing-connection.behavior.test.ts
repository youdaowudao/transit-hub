// @vitest-environment jsdom

import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import GroupRatesView from '@/modules/admin/views/GroupRatesView.vue'
import type { GroupRate } from '@/modules/admin/types/groupRates'
import type { RealConnection } from '@/modules/admin/types/mySites'

const harness = vi.hoisted(() => ({
  calls: [] as string[],
  checkRealConnections: vi.fn(),
  listRealConnections: vi.fn(),
  realDisconnect: vi.fn(),
  listGroupRates: vi.fn(),
  routerPush: vi.fn(),
  routerReplace: vi.fn(async () => undefined),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: harness.routerPush, replace: harness.routerReplace }),
}))

vi.mock('@/modules/admin/api/dashboardAdmin', () => ({
  getDashboardAdminStatus: vi.fn(async () => ({ platform: 'sub2api' })),
}))

vi.mock('@/modules/admin/api/upstream', () => ({
  listUpstreamSites: vi.fn(async () => []),
}))

vi.mock('@/modules/admin/api/groupRates', () => ({
  listGroupRates: harness.listGroupRates,
  listAllGroupRates: vi.fn(async () => []),
  listGroupRateHistory: vi.fn(async () => []),
  updateGroupRateType: vi.fn(async () => undefined),
}))

vi.mock('@/modules/admin/api/mySites', () => ({
  checkRealConnections: harness.checkRealConnections,
  listRealConnections: harness.listRealConnections,
  realDisconnect: harness.realDisconnect,
  getMySiteMappingOptions: vi.fn(async () => ({ ownGroups: [], mappings: [] })),
  realConnect: vi.fn(),
  realBind: vi.fn(),
  listAdminResources: vi.fn(async () => []),
  listUpstreamKeys: vi.fn(async () => []),
}))

const rates: GroupRate[] = [
  {
    siteId: 'site-active', siteName: '正常站点', groupId: 'active-group', groupName: '正常分组',
    type: 'openai', platform: 'sub2api', mapped: true, pricingMapped: true, deleted: false,
    currentMultiplier: 1, delta: 0, deltaPercent: 0, updatedAt: '2026-09-13T00:00:00Z',
  },
  {
    siteId: 'site-missing', siteName: '失效站点', groupId: 'missing-group', groupName: '失效分组',
    type: 'openai', platform: 'sub2api', mapped: false, pricingMapped: true, deleted: false,
    currentMultiplier: 2, delta: 0, deltaPercent: 0, updatedAt: '2026-09-13T00:00:00Z',
  },
]

const connections: RealConnection[] = [
  {
    id: 'connection-active', upstreamSiteId: 'site-active', upstreamGroupId: 'active-group', upstreamGroupName: '正常分组',
    upstreamKeyId: 'key-active', adminAccountId: 'account-active', adminAccountName: '正常账号', ownGroupIds: ['1'],
    groupType: 'openai', status: 'active', pricingMappingEnabled: true, canDeleteRemote: true, createdAt: '2026-09-13T00:00:00Z',
  },
  {
    id: 'connection-missing', upstreamSiteId: 'site-missing', upstreamGroupId: 'missing-group', upstreamGroupName: '失效分组',
    upstreamKeyId: 'key-missing', adminAccountId: 'account-missing', adminAccountName: '失效账号', ownGroupIds: ['2'],
    groupType: 'openai', status: 'missing', pricingMappingEnabled: true, canDeleteRemote: false, createdAt: '2026-09-13T00:00:00Z',
  },
]

const wrappers: VueWrapper[] = []

const deferred = <T>() => {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>(next => {
    resolve = next
  })
  return { promise, resolve }
}

const mountView = async () => {
  const wrapper = mount(GroupRatesView)
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const rowByGroup = (wrapper: VueWrapper, group: string) => {
  const row = wrapper.findAll('tbody tr').find(candidate => candidate.text().includes(group))
  if (!row) throw new Error(`missing row ${group}`)
  return row
}

beforeEach(() => {
  harness.calls.splice(0)
  harness.checkRealConnections.mockReset().mockImplementation(async () => {
    harness.calls.push('check')
    return { checked: 2, active: 1, missing: 1 }
  })
  harness.listRealConnections.mockReset().mockImplementation(async () => {
    harness.calls.push('connections')
    return connections
  })
  harness.realDisconnect.mockReset().mockResolvedValue(undefined)
  harness.listGroupRates.mockReset().mockImplementation(async () => {
    harness.calls.push('rates')
    return {
      items: rates, total: rates.length, page: 1, pageSize: 10, totalPages: 1,
      types: ['openai'], platforms: ['sub2api'], statusCounts: { all: 2, mapped: 1, unmapped: 1, deleted: 0 },
    }
  })
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  document.body.innerHTML = ''
})

describe('group rates missing main account behavior', () => {
  it('checks before loading, renders missing separately, and only offers local cleanup', async () => {
    const wrapper = await mountView()

    expect(harness.calls[0]).toBe('check')
    expect(harness.calls.indexOf('check')).toBeLessThan(harness.calls.indexOf('rates'))
    expect(harness.calls.indexOf('check')).toBeLessThan(harness.calls.indexOf('connections'))

    const activeRow = rowByGroup(wrapper, '正常分组')
    expect(activeRow.text()).toContain('已对接')
    expect(activeRow.text()).not.toContain('主站账号已删除')

    const missingRow = rowByGroup(wrapper, '失效分组')
    expect(missingRow.text()).toContain('主站账号已删除')
    expect(missingRow.text()).not.toContain('已对接')
    expect(missingRow.text()).toContain('已用于调价')
    expect(wrapper.text()).not.toContain('admin.groupRates.status.mainAccountMissing')

    await missingRow.findAll('button').at(-1)!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('仅清理本地记录')
    expect(dialog.text()).not.toContain('删除账号和 Key')
    expect((dialog.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true)

    await dialog.get('input[type="checkbox"]').setValue(false)
    expect((dialog.get('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(false)

    const confirm = dialog.findAll('button').find(button => button.text().trim() === '确定')
    if (!confirm) throw new Error('missing cleanup confirm button')
    await confirm.trigger('click')
    await flushPromises()
    expect(harness.realDisconnect).toHaveBeenCalledWith({
      connectionId: 'connection-missing',
      mode: 'unlink',
      removePricingMapping: false,
    })
    expect(harness.listGroupRates).toHaveBeenCalledTimes(2)
    expect(harness.listRealConnections).toHaveBeenCalledTimes(2)
  })

  it('prefers an active connection over a missing duplicate for the same target', async () => {
    harness.listRealConnections.mockResolvedValue([
      ...connections,
      {
        ...connections[1],
        id: 'connection-active-duplicate',
        status: 'active',
        canDeleteRemote: true,
      },
    ])

    const wrapper = await mountView()
    const duplicateRow = rowByGroup(wrapper, '失效分组')
    expect(duplicateRow.text()).toContain('已对接')
    expect(duplicateRow.text()).not.toContain('主站账号已删除')
  })

  it('checks first on manual refresh and retains visible rows with a retry warning when checking fails', async () => {
    const wrapper = await mountView()
    harness.calls.splice(0)
    harness.checkRealConnections.mockImplementationOnce(async () => {
      harness.calls.push('check')
      throw new Error('inventory unavailable')
    })

    const refresh = wrapper.findAll('button').find(button => button.text().includes('刷新数据'))
    if (!refresh) throw new Error('missing refresh button')
    await refresh.trigger('click')
    await flushPromises()

    expect(harness.calls[0]).toBe('check')
    expect(harness.checkRealConnections).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('主站账号核对失败，当前仍显示上次数据。请重试刷新。')
    expect(rowByGroup(wrapper, '失效分组').text()).toContain('主站账号已删除')
  })

  it('keeps refresh locked until the refreshed connection snapshot finishes loading', async () => {
    const wrapper = await mountView()
    const pendingConnections = deferred<RealConnection[]>()
    harness.listRealConnections.mockImplementationOnce(async () => {
      harness.calls.push('connections')
      return pendingConnections.promise
    })

    const refresh = wrapper.findAll('button').find(button => button.text().includes('刷新数据'))
    if (!refresh) throw new Error('missing refresh button')
    await refresh.trigger('click')
    await flushPromises()

    expect(harness.checkRealConnections).toHaveBeenCalledTimes(2)
    expect((refresh.element as HTMLButtonElement).disabled).toBe(true)
    await refresh.trigger('click')
    expect(harness.checkRealConnections).toHaveBeenCalledTimes(2)

    pendingConnections.resolve(connections)
    await flushPromises()
    expect((refresh.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('clears stale connection status when checking succeeds but the refreshed connection list fails', async () => {
    const wrapper = await mountView()
    harness.calls.splice(0)
    harness.listRealConnections.mockImplementationOnce(async () => {
      harness.calls.push('connections')
      throw new Error('connection list unavailable')
    })

    const refresh = wrapper.findAll('button').find(button => button.text().includes('刷新数据'))
    if (!refresh) throw new Error('missing refresh button')
    await refresh.trigger('click')
    await flushPromises()

    expect(harness.calls[0]).toBe('check')
    expect(wrapper.text()).toContain('主站账号核对已完成，但对接状态加载失败。为避免误显示，已隐藏旧对接状态，请重试刷新。')
    const activeRow = rowByGroup(wrapper, '正常分组')
    expect(activeRow.text()).not.toContain('已对接')
    expect(activeRow.text()).toContain('配置对接')
  })
})

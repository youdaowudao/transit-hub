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
  realConnect: vi.fn(),
  getMySiteMappingOptions: vi.fn(),
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
  getMySiteMappingOptions: harness.getMySiteMappingOptions,
  realConnect: harness.realConnect,
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
  harness.realConnect.mockReset().mockResolvedValue(undefined)
  harness.getMySiteMappingOptions.mockReset().mockResolvedValue({ ownGroups: [], mappings: [] })
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


describe('safe complete-disconnect rejection', () => {
  it('keeps sent-but-unconfirmed deletion accurate without claiming no request was sent', async () => {
    harness.realDisconnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.resourcesPendingVerification'), {
      adminResourceId: 'account-active', upstreamKeyId: 'key-active', reason: '',
    }))
    const wrapper = await mountView()
    const row = rowByGroup(wrapper, '正常分组')
    await row.findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[value="full"]').setValue(true)
    await dialog.get('input[type="checkbox"]').setValue(false)
    await dialog.findAll('button').find(button => button.text().trim() === '确定')!.trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('账号删除尚未确认')
    expect(dialog.text()).toContain('Key 和本地记录已保留')
    expect(dialog.text()).toContain('主站账号 account-active')
    expect(dialog.text()).toContain('上游 Key 编号 key-active')
    expect(dialog.text()).not.toContain('当前操作未发送')
    expect(dialog.text()).not.toContain('有远端操作尚未完成核对')
    expect(dialog.text()).not.toContain('请稍后再试；持续出现时请查看分组健康页顶部提示。')
    expect(dialog.text()).not.toContain('最后一个可用账号')
    expect(dialog.get('input[value="full"]').element).toHaveProperty('checked', true)
    expect(dialog.get('input[type="checkbox"]').element).toHaveProperty('checked', false)
    expect(row.text()).toContain('已对接')
    expect(harness.realDisconnect).toHaveBeenCalledTimes(1)
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
  })

  it('describes pending protection without misidentifying the account', async () => {
    harness.realDisconnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.resourcesPendingVerification'), {
      adminResourceId: 'account-active', upstreamKeyId: 'key-active', reason: 'admin.connectionHealth.errors.remoteActionPending', groupId: '1', groupName: '正常分组',
    }))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[value="full"]').setValue(true)
    await dialog.get('input[type="checkbox"]').setValue(false)
    await dialog.findAll('button').find(button => button.text().trim() === '确定')!.trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('有远端操作尚未完成核对，当前操作未发送。请稍后再试；持续出现时请查看分组健康页顶部提示。')
    expect(dialog.text()).not.toContain('同一账号')
    expect(dialog.text()).not.toContain('该账号')
    expect(dialog.text()).not.toContain('最后一个可用账号')
    expect(dialog.text()).toContain('受保护分组：正常分组（1）')
    expect(dialog.get('input[value="full"]').element).toHaveProperty('checked', true)
    expect(dialog.get('input[type="checkbox"]').element).toHaveProperty('checked', false)
    expect(dialog.find('input[value="unlink"]').exists()).toBe(true)
    expect(rowByGroup(wrapper, '正常分组').text()).toContain('已对接')
    expect(harness.realDisconnect).toHaveBeenCalledWith({ connectionId: 'connection-active', mode: 'full', removePricingMapping: false })
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
    expect(harness.listGroupRates).toHaveBeenCalledTimes(1)
  })

  it('shows a fallback reason only once and preserves the local record', async () => {
    harness.realDisconnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.resourcesPendingVerification'), {
      adminResourceId: '101', upstreamKeyId: '207', reason: 'admin.mySites.errors.resourcesPendingVerification',
    }))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[value="full"]').setValue(true)
    await dialog.findAll('button').find(button => button.text().trim() === '确定')!.trigger('click')
    await flushPromises()
    expect(dialog.text().match(/账号删除尚未确认/g)).toHaveLength(1)
    expect(dialog.text()).toContain('Key 和本地记录已保留')
    expect(dialog.text()).not.toContain('账号、Key 和本地记录已保留')
    expect(dialog.text()).toContain('主站账号 101')
    expect(dialog.text()).toContain('上游 Key 编号 207')
    expect(rowByGroup(wrapper, '正常分组').text()).toContain('已对接')
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
  })

  it('keeps sent-but-unconfirmed compensation accurate and retains the creation selection', async () => {
    harness.listRealConnections.mockResolvedValue([])
    harness.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: '10', groupName: '隔离分组', platform: 'openai', multiplier: 1 }], mappings: [] })
    harness.realConnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.compensationPendingVerification'), {
      adminResourceId: '101', upstreamKeyId: '207', reason: '',
    }))
    const wrapper = await mountView()
    const row = rowByGroup(wrapper, '正常分组')
    await row.findAll('button').find(button => button.text().includes('配置对接'))!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"]')
    const selection = dialog.findAll('label').find(label => label.text().includes('隔离分组'))!.get('input[type="checkbox"]')
    await selection.setValue(true)
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(dialog.text()).toContain('账号清理尚未确认')
    expect(dialog.text()).toContain('本地记录未保存')
    expect(dialog.text()).toContain('主站账号 101')
    expect(dialog.text()).toContain('上游 Key 编号 207')
    expect(dialog.text()).not.toContain('本地记录已保留')
    expect(dialog.text()).not.toContain('当前操作未发送')
    expect(dialog.text()).not.toContain('有远端操作尚未完成核对')
    expect(dialog.text()).not.toContain('请稍后再试；持续出现时请查看分组健康页顶部提示。')
    expect(dialog.text()).not.toContain('真实对接创建失败')
    expect(selection.element).toHaveProperty('checked', true)
    expect(row.text()).toContain('配置对接')
    expect(row.text()).not.toContain('已对接')
    expect(harness.realConnect).toHaveBeenCalledTimes(1)
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['admin.connectionHealth.errors.sub2apiGroupLastUsable', '最后一个可用账号'],
    ['admin.connectionHealth.errors.sub2apiInventoryIncomplete', '主站分组账号资料不完整'],
    ['admin.connectionHealth.testConfiguration.remoteActionPending', '远端动作待确认'],
    ['admin.mySites.errors.safeDeletionUnavailable', '安全删除服务暂不可用'],
  ])('retains the dialog, local connection and both modes for %s', async (key, reason) => {
    harness.realDisconnect.mockRejectedValueOnce(new Error(key))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[value="full"]').setValue(true)
    const confirm = dialog.findAll('button').find(button => button.text().trim() === '确定')!
    await confirm.trigger('click'); await flushPromises()
    expect(harness.realDisconnect).toHaveBeenCalledWith({ connectionId: 'connection-active', mode: 'full', removePricingMapping: true })
    expect(dialog.text()).toContain(reason)
    expect(dialog.text()).not.toContain('取消对接失败')
    expect(dialog.find('input[value="unlink"]').exists()).toBe(true)
    expect(dialog.find('input[value="full"]').exists()).toBe(true)
    expect(rowByGroup(wrapper, '正常分组').text()).toContain('已对接')
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
    expect(harness.listGroupRates).toHaveBeenCalledTimes(1)
  })

  it('shows retained resource numbers when deletion outcome is unknown', async () => {
    harness.realDisconnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.resourcesPendingVerification'), { adminResourceId: '101', upstreamKeyId: '207', reason: 'admin.connectionHealth.errors.sub2apiGroupLastUsable', groupId: '10', groupName: '隔离分组' }))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').at(-1)!.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.get('input[value="full"]').setValue(true)
    await dialog.findAll('button').find(button => button.text().trim() === '确定')!.trigger('click')
    await flushPromises()
    expect(dialog.text()).toContain('账号删除尚未确认')
    expect(dialog.text()).toContain('最后一个可用账号')
    expect(dialog.text()).toContain('受保护分组：隔离分组（10）')
    expect(dialog.text()).toContain('主站账号 101')
    expect(dialog.text()).toContain('上游 Key 编号 207')
    expect(rowByGroup(wrapper, '正常分组').text()).toContain('已对接')
    expect(harness.listGroupRates).toHaveBeenCalledTimes(1)
  })
})

describe('safe real-connect compensation rejection', () => {
  it.each([
    ['admin.mySites.errors.accountCreationPendingVerification', '', '账号创建结果尚未确认', '主站账号 —'],
    ['admin.mySites.errors.upstreamKeyCleanupPendingVerification', '101', '主站账号已删除', '主站账号 101'],
  ])('describes unsaved local records and retained keys accurately: %s', async (key, adminResourceId, status, resource) => {
    harness.listRealConnections.mockResolvedValue([])
    harness.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: '10', groupName: '隔离分组', platform: 'openai', multiplier: 1 }], mappings: [] })
    harness.realConnect.mockRejectedValueOnce(Object.assign(new Error(key), { adminResourceId, upstreamKeyId: '207', reason: '' }))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').find(button => button.text().includes('配置对接'))!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.findAll('label').find(label => label.text().includes('隔离分组'))!.get('input[type="checkbox"]').setValue(true)
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(dialog.text()).toContain(status)
    expect(dialog.text()).toContain('本地记录未保存')
    expect(dialog.text()).toContain(resource)
    expect(dialog.text()).toContain('上游 Key 编号 207')
    expect(dialog.text()).not.toContain('本地记录已保留')
    expect(dialog.text()).not.toContain('账号删除尚未确认')
    expect(dialog.text()).not.toContain('真实对接创建失败')
    expect(harness.realDisconnect).not.toHaveBeenCalled()
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['admin.connectionHealth.errors.sub2apiGroupLastUsable', '最后一个可用账号'],
    ['admin.connectionHealth.errors.remoteActionPending', '有远端操作尚未完成核对，当前操作未发送。请稍后再试；持续出现时请查看分组健康页顶部提示。'],
  ])('shows retained resources and keeps the connector on compensation failure: %s', async (reason, text) => {
    harness.listRealConnections.mockResolvedValue([])
    harness.getMySiteMappingOptions.mockResolvedValue({
      ownGroups: [{ id: '10', groupName: '隔离分组', platform: 'openai', multiplier: 1 }], mappings: [],
    })
    harness.realConnect.mockRejectedValueOnce(Object.assign(new Error('admin.mySites.errors.compensationPendingVerification'), {
      adminResourceId: '101', upstreamKeyId: '207', reason, groupId: '10', groupName: '隔离分组',
    }))
    const wrapper = await mountView()
    const row = rowByGroup(wrapper, '正常分组')
    await row.findAll('button').find(button => button.text().includes('配置对接'))!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.findAll('label').find(label => label.text().includes('隔离分组'))!.get('input[type="checkbox"]').setValue(true)
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(harness.realConnect).toHaveBeenCalledTimes(1)
    expect(harness.realConnect).toHaveBeenCalledWith(expect.objectContaining({ ownGroupIds: ['10'], operationId: expect.any(String) }))
    expect(dialog.text()).toContain('账号清理尚未确认')
    expect(dialog.text()).toContain('本地记录未保存')
    expect(dialog.text()).not.toContain('本地记录已保留')
    expect(dialog.text()).toContain(text)
    if (reason === 'admin.connectionHealth.errors.remoteActionPending') {
      expect(dialog.text()).not.toContain('同一账号')
      expect(dialog.text()).not.toContain('该账号')
      expect(dialog.text()).not.toContain('最后一个可用账号')
    }
    expect(dialog.text()).toContain('受保护分组：隔离分组（10）')
    expect(dialog.text()).toContain('主站账号 101')
    expect(dialog.text()).toContain('上游 Key 编号 207')
    expect(dialog.text()).not.toContain('真实对接创建失败')
    expect(row.text()).toContain('配置对接')
    expect(row.text()).not.toContain('已对接')
    expect(dialog.get('input[type="checkbox"]').element).toHaveProperty('checked', true)
    expect(harness.realDisconnect).not.toHaveBeenCalled()
    expect(harness.listRealConnections).toHaveBeenCalledTimes(1)
    expect(harness.listGroupRates).toHaveBeenCalledTimes(1)
  })

  it('keeps the original generic error for an unrecognized create failure', async () => {
    harness.listRealConnections.mockResolvedValue([])
    harness.getMySiteMappingOptions.mockResolvedValue({ ownGroups: [{ id: '10', groupName: '隔离分组', platform: 'openai', multiplier: 1 }], mappings: [] })
    harness.realConnect.mockRejectedValueOnce(new Error('unrecognized-create-error'))
    const wrapper = await mountView()
    await rowByGroup(wrapper, '正常分组').findAll('button').find(button => button.text().includes('配置对接'))!.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[role="dialog"]')
    await dialog.findAll('label').find(label => label.text().includes('隔离分组'))!.get('input[type="checkbox"]').setValue(true)
    await dialog.get('form').trigger('submit')
    await flushPromises()
    expect(dialog.text()).toContain('真实对接创建失败')
    expect(dialog.text()).not.toContain('待核对资源')
    expect(dialog.text()).not.toContain('unrecognized-create-error')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
  })
})

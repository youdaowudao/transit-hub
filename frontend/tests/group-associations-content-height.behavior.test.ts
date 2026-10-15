// @vitest-environment jsdom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import GroupAssociationsPanel from '@/modules/admin/components/dashboard/GroupAssociationsPanel.vue'
import GroupAssociationTargetsDrawer from '@/modules/admin/components/dashboard/GroupAssociationTargetsDrawer.vue'
import type { MySiteMappingOptionsResponse } from '@/modules/admin/types/mySites'
import { groupAssociationsPreferencesStorageKey } from '@/modules/admin/utils/groupAssociationsPreferences'

const api = vi.hoisted(() => ({
  getMySiteMappingOptions: vi.fn(), listRealConnections: vi.fn(), listAllGroupRates: vi.fn(),
  listUpstreamSites: vi.fn(), getConnectionHealthAdminGroups: vi.fn(), getNotificationChannelSettings: vi.fn(),
  saveMySiteMapping: vi.fn(), removeMySiteMapping: vi.fn(), runAutoPricing: vi.fn(),
}))
vi.mock('@/modules/admin/api/mySites', async original => ({
  ...await original<typeof import('@/modules/admin/api/mySites')>(),
  getMySiteMappingOptions: api.getMySiteMappingOptions, listRealConnections: api.listRealConnections,
  saveMySiteMapping: api.saveMySiteMapping, removeMySiteMapping: api.removeMySiteMapping, runAutoPricing: api.runAutoPricing,
}))
vi.mock('@/modules/admin/api/groupRates', async original => ({ ...await original<typeof import('@/modules/admin/api/groupRates')>(), listAllGroupRates: api.listAllGroupRates }))
vi.mock('@/modules/admin/api/upstream', async original => ({ ...await original<typeof import('@/modules/admin/api/upstream')>(), listUpstreamSites: api.listUpstreamSites }))
vi.mock('@/modules/admin/api/connectionHealth', async original => ({ ...await original<typeof import('@/modules/admin/api/connectionHealth')>(), getConnectionHealthAdminGroups: api.getConnectionHealthAdminGroups }))
vi.mock('@/modules/admin/api/settings', async original => ({ ...await original<typeof import('@/modules/admin/api/settings')>(), getNotificationChannelSettings: api.getNotificationChannelSettings }))
vi.mock('@/modules/admin/composables/useAdminAccounts', async () => {
  const { ref } = await import('vue')
  return { useAdminAccounts: () => ({ currentAccount: ref({ id: 'ws-association-height', platform: 'sub2api' }) }) }
})

const hint = '这里列出当前筛选出的全部分组；向下滚动页面可继续查看，点击分组查看详情。查看详情不会修改主站。'
const fixture = (count: number): MySiteMappingOptionsResponse => ({
  ownGroups: Array.from({ length: count }, (_, index) => ({
    id: String(index + 1), groupName: `分组${String(index + 1).padStart(2, '0')}`, siteName: '测试主站',
    multiplier: 1, platform: 'openai', status: 'active', isExclusive: false, subscriptionType: '',
  })),
  mappings: count ? [{ ownGroup: `分组${String(count).padStart(2, '0')}`, upstreamTargets: [{ siteId: 'site-height', groupName: '末组数据源' }] }] : [],
})
const wrappers: VueWrapper[] = []
const unexpected: string[] = []
const open = async () => {
  const wrapper = mount(GroupAssociationsPanel, { attachTo: document.body, global: { stubs: { Teleport: true } } })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
const nav = (wrapper: VueWrapper) => wrapper.get('nav[aria-label="我的分组列表"]')
const groupButtons = (wrapper: VueWrapper) => nav(wrapper).findAll('button')
const names = (wrapper: VueWrapper) => groupButtons(wrapper).map(button => button.get('span.font-medium').text())
const detail = (wrapper: VueWrapper) => wrapper.get('section[aria-label="调价映射详情"]')
const click = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find(item => item.text().trim() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  localStorage.clear()
  unexpected.length = 0
  vi.resetAllMocks()
  api.getMySiteMappingOptions.mockResolvedValue(fixture(1))
  api.listRealConnections.mockResolvedValue([])
  api.listAllGroupRates.mockResolvedValue([])
  api.listUpstreamSites.mockResolvedValue([])
  api.getConnectionHealthAdminGroups.mockResolvedValue([])
  api.getNotificationChannelSettings.mockResolvedValue({ telegram: [] })
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    unexpected.push(String(input))
    throw new Error('Unexpected real request in association layout test')
  }))
})
afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
  localStorage.clear()
  expect(unexpected).toEqual([])
  expect(api.saveMySiteMapping).not.toHaveBeenCalled()
  expect(api.removeMySiteMapping).not.toHaveBeenCalled()
  expect(api.runAutoPricing).not.toHaveBeenCalled()
})

describe('association list content height and retained selection', () => {
  it.each([0, 1, 10, 30])('renders exactly %i visible groups and consistent selection', async count => {
    const data = fixture(count)
    api.getMySiteMappingOptions.mockResolvedValue(data)
    const wrapper = await open()
    expect(names(wrapper)).toEqual(data.ownGroups.map(group => group.groupName))
    expect(nav(wrapper).text()).toContain(hint)
    if (count) {
      expect(detail(wrapper).get('h2').text()).toBe('分组01')
      expect(nav(wrapper).find('.min-h-48').exists()).toBe(false)
    } else {
      expect(nav(wrapper).get('.min-h-48').text()).toContain('暂无')
      expect(detail(wrapper).get('.min-h-48').exists()).toBe(true)
      expect(detail(wrapper).find('h2').exists()).toBe(false)
    }
  })

  // These are structural checks; no mocked dimensions are used as rendering evidence.
  it('removes the sidebar bounds and grid minimum without altering responsive columns', async () => {
    const wrapper = await open()
    const list = nav(wrapper)
    const aside = list.element.closest('aside')!
    const grid = aside.parentElement!
    expect(list.classes()).not.toContain('flex-1')
    expect(list.classes().filter(value => /(^|:)(max-h-|overflow-y-)/.test(value))).toEqual([])
    expect(Array.from(grid.classList).filter(value => /(^|:)(min-h-|h-|max-h-)/.test(value))).toEqual([])
    expect(grid.classList.contains('lg:grid-cols-[19rem_minmax(0,1fr)]')).toBe(true)
    expect(aside.nextElementSibling).toBe(detail(wrapper).element)
  })

  it('does not retain a minimum grid height during loading', async () => {
    let resolve!: (data: MySiteMappingOptionsResponse) => void
    api.getMySiteMappingOptions.mockReturnValue(new Promise<MySiteMappingOptionsResponse>(done => { resolve = done }))
    const wrapper = await open()
    const grid = wrapper.get('.grid.lg\\:grid-cols-\\[19rem_minmax\\(0\\,1fr\\)\\]')
    expect(grid.classes()).not.toContain('min-h-[32rem]')
    expect(wrapper.find('nav').exists()).toBe(false)
    resolve(fixture(1))
    await flushPromises()
    expect(names(wrapper)).toEqual(['分组01'])
  })

  it('selects the last of 30 groups and its own data source without saving', async () => {
    api.getMySiteMappingOptions.mockResolvedValue(fixture(30))
    const wrapper = await open()
    await groupButtons(wrapper).at(-1)!.trigger('click')
    await flushPromises()
    expect(detail(wrapper).get('h2').text()).toBe('分组30')
    expect(detail(wrapper).get('h2').text()).not.toBe('分组01')
    expect(wrapper.findComponent(GroupAssociationTargetsDrawer).props('ownGroup')).toBe('分组30')
    expect(wrapper.findComponent(GroupAssociationTargetsDrawer).props('selected')).toEqual([{ siteId: 'site-height', groupName: '末组数据源' }])
    expect(api.getMySiteMappingOptions).toHaveBeenCalledTimes(1)
  })

  it('searches to one group, none and back while discarding invisible selection', async () => {
    api.getMySiteMappingOptions.mockResolvedValue(fixture(30))
    const wrapper = await open()
    const search = wrapper.get('input[aria-label="搜索调价映射"]')
    await search.setValue('分组30')
    expect(names(wrapper)).toEqual(['分组30'])
    expect(detail(wrapper).get('h2').text()).toBe('分组30')
    await search.setValue('完全不存在')
    expect(names(wrapper)).toEqual([])
    expect(detail(wrapper).find('h2').exists()).toBe(false)
    await search.setValue('')
    expect(names(wrapper)).toHaveLength(30)
    expect(detail(wrapper).get('h2').text()).toBe('分组01')
    expect(api.getMySiteMappingOptions).toHaveBeenCalledTimes(1)
  })

  it('retains associated, unassociated and stale filters and their own selection', async () => {
    const data = fixture(3)
    data.staleOwnGroups = ['分组02']
    api.getMySiteMappingOptions.mockResolvedValue(data)
    const wrapper = await open()
    const tabs = wrapper.get('[role="tablist"]').findAll('button')
    await tabs[1]!.trigger('click')
    expect(names(wrapper)).toEqual(['分组03'])
    expect(detail(wrapper).get('h2').text()).toBe('分组03')
    await tabs[2]!.trigger('click')
    expect(names(wrapper)).toEqual(['分组01', '分组02'])
    expect(detail(wrapper).get('h2').text()).toBe('分组01')
    await tabs[3]!.trigger('click')
    expect(names(wrapper)).toEqual(['分组02'])
    expect(detail(wrapper).get('h2').text()).toBe('分组02')
  })

  it('keeps display order and hidden preferences across remounts and restores hidden selection', async () => {
    api.getMySiteMappingOptions.mockResolvedValue(fixture(3))
    const wrapper = await open()
    await click(wrapper, '管理分组显示')
    await wrapper.findAll('button[aria-label="上移"]')[2]!.trigger('click')
    expect(names(wrapper)).toEqual(['分组01', '分组03', '分组02'])
    await groupButtons(wrapper)[1]!.trigger('click')
    await wrapper.findAll('input[type="checkbox"]')[1]!.setValue(false)
    expect(names(wrapper)).toEqual(['分组01', '分组02'])
    expect(detail(wrapper).get('h2').text()).toBe('分组01')
    const stored = JSON.parse(localStorage.getItem(groupAssociationsPreferencesStorageKey('ws-association-height'))!)
    expect(stored).toMatchObject({ groupOrder: ['1', '3', '2'], hiddenGroupIds: ['3'] })
    wrapper.unmount()
    const remount = await open()
    expect(names(remount)).toEqual(['分组01', '分组02'])
    await click(remount, '管理分组显示')
    await remount.findAll('input[type="checkbox"]')[1]!.setValue(true)
    expect(names(remount)).toEqual(['分组01', '分组03', '分组02'])
  })

  it('clears details when all groups are hidden and recovers through the existing display manager', async () => {
    api.getMySiteMappingOptions.mockResolvedValue(fixture(3))
    const wrapper = await open()
    await click(wrapper, '管理分组显示')
    for (const checkbox of wrapper.findAll('input[type="checkbox"]')) await checkbox.setValue(false)
    expect(groupButtons(wrapper)).toHaveLength(0)
    expect(nav(wrapper).text()).toContain('请在管理分组显示中恢复')
    expect(detail(wrapper).find('h2').exists()).toBe(false)
    await wrapper.findAll('input[type="checkbox"]')[2]!.setValue(true)
    expect(names(wrapper)).toEqual(['分组03'])
    expect(detail(wrapper).get('h2').text()).toBe('分组03')
  })

  it('retains prior rows and detailed failure on refresh, then retries without saving', async () => {
    const wrapper = await open()
    const reason = '读取映射中断：需要查看Hub映射读取记录；可点击重试。'.repeat(6)
    api.getMySiteMappingOptions.mockRejectedValueOnce(new Error(reason))
    await click(wrapper, '刷新')
    expect(wrapper.text()).toContain(reason)
    expect(names(wrapper)).toEqual(['分组01'])
    await click(wrapper, '重试')
    expect(wrapper.text()).not.toContain(reason)
    expect(names(wrapper)).toEqual(['分组01'])
  })

  it('shows the initial read failure without fabricated groups and recovers through retry', async () => {
    const reason = '首次映射读取中断；请重试并查看Hub映射读取记录。'
    api.getMySiteMappingOptions.mockRejectedValueOnce(new Error(reason))
    const wrapper = await open()
    expect(wrapper.text()).toContain(reason)
    expect(groupButtons(wrapper)).toHaveLength(0)
    expect(detail(wrapper).find('h2').exists()).toBe(false)
    await click(wrapper, '重试')
    expect(wrapper.text()).not.toContain(reason)
    expect(names(wrapper)).toEqual(['分组01'])
    expect(detail(wrapper).get('h2').text()).toBe('分组01')
  })

  it('retains groups with an auxiliary read failure and preserves a long group name', async () => {
    const data = fixture(1)
    const name = '完整长名称'.repeat(25)
    data.ownGroups[0]!.groupName = name
    data.mappings = []
    api.getMySiteMappingOptions.mockResolvedValue(data)
    api.listRealConnections.mockRejectedValue(new Error('测试对接读取中断'))
    const wrapper = await open()
    expect(names(wrapper)).toEqual([name])
    expect(detail(wrapper).get('h2').text()).toBe(name)
    expect(wrapper.text()).toContain('真实接入数据加载失败，当前列表可能不完整。')
    expect(nav(wrapper).find('.min-h-48').exists()).toBe(false)
  })
})
